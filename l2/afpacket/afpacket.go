// SPDX-License-Identifier: MIT

//go:build linux

// Package afpacket is the Linux backend of l2.Port: a raw AF_PACKET socket
// bound to one network interface.
//
// Opening a port needs the CAP_NET_RAW capability (root, or
//
//	sudo setcap cap_net_raw+ep ./your-binary
//
// on the executable). The socket receives every EtherType, drops the
// frames the host sends itself, receives all multicast groups of the
// interface (PACKET_MR_ALLMULTI, so that DCP and LLDP multicasts arrive)
// and restores the 802.1Q tag that the kernel strips from received
// frames, from the packet's auxiliary data.
package afpacket

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// Options configure a port.
type Options struct {
	// Promiscuous receives unicast frames for other addresses too.
	Promiscuous bool
	// NoAllMulticast receives only the multicast groups joined with
	// JoinMulticast instead of all.
	NoAllMulticast bool
}

// Port is an AF_PACKET socket on one interface. It implements l2.Port,
// l2.MulticastPort and l2.LinkStater.
type Port struct {
	iface   *net.Interface
	mac     ethernet.MAC
	file    *os.File
	raw     syscall.RawConn
	readMu  sync.Mutex
	closeMu sync.Once
	closed  chan struct{}
}

const (
	// packetAuxdata is PACKET_AUXDATA, which syscall does not define on
	// every architecture.
	packetAuxdata = 8

	tpStatusVLANValid     = 1 << 4
	tpStatusVLANTPIDValid = 1 << 6
)

// tpacketAuxdata is struct tpacket_auxdata.
type tpacketAuxdata struct {
	Status   uint32
	Len      uint32
	SnapLen  uint32
	Mac      uint16
	Net      uint16
	VLANTCI  uint16
	VLANTPID uint16
}

// packetMreq is struct packet_mreq.
type packetMreq struct {
	Ifindex int32
	Type    uint16
	Alen    uint16
	Address [8]byte
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// Open opens a port on the named interface.
func Open(name string, opts Options) (*Port, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("afpacket: %w: interface %q: %v", profinet.ErrNotFound, name, err)
	}
	if len(ifi.HardwareAddr) != 6 {
		return nil, fmt.Errorf("afpacket: %w: interface %q is not Ethernet", profinet.ErrUnsupported, name)
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return nil, fmt.Errorf("afpacket: open %s: %w (needs CAP_NET_RAW)", name, err)
		}
		return nil, fmt.Errorf("afpacket: open %s: %w", name, err)
	}
	ok := false
	defer func() {
		if !ok {
			syscall.Close(fd)
		}
	}()
	sa := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ALL), Ifindex: ifi.Index}
	if err := syscall.Bind(fd, sa); err != nil {
		return nil, fmt.Errorf("afpacket: bind %s: %w", name, err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_PACKET, packetAuxdata, 1); err != nil {
		return nil, fmt.Errorf("afpacket: PACKET_AUXDATA: %w", err)
	}
	p := &Port{iface: ifi, mac: ethernet.MAC(ifi.HardwareAddr), closed: make(chan struct{})}
	if !opts.NoAllMulticast {
		if err := setMembership(fd, ifi.Index, syscall.PACKET_ADD_MEMBERSHIP, syscall.PACKET_MR_ALLMULTI, nil); err != nil {
			return nil, fmt.Errorf("afpacket: all-multicast on %s: %w", name, err)
		}
	}
	if opts.Promiscuous {
		if err := setMembership(fd, ifi.Index, syscall.PACKET_ADD_MEMBERSHIP, syscall.PACKET_MR_PROMISC, nil); err != nil {
			return nil, fmt.Errorf("afpacket: promiscuous on %s: %w", name, err)
		}
	}
	p.file = os.NewFile(uintptr(fd), "afpacket:"+name)
	if p.raw, err = p.file.SyscallConn(); err != nil {
		p.file.Close()
		ok = true
		return nil, err
	}
	ok = true
	return p, nil
}

func setMembership(fd, ifindex, op int, typ uint16, addr []byte) error {
	m := packetMreq{Ifindex: int32(ifindex), Type: typ, Alen: uint16(len(addr))}
	copy(m.Address[:], addr)
	// SetsockoptString passes the octets and their length, which is what
	// setsockopt needs, on every architecture (386 included).
	raw := (*[unsafe.Sizeof(packetMreq{})]byte)(unsafe.Pointer(&m))
	return syscall.SetsockoptString(fd, syscall.SOL_PACKET, op, string(raw[:]))
}

// HardwareAddr implements l2.Port.
func (p *Port) HardwareAddr() ethernet.MAC { return p.mac }

// MTU implements l2.Port.
func (p *Port) MTU() int {
	if p.iface.MTU <= 0 {
		return ethernet.DefaultMTU
	}
	return p.iface.MTU
}

// Name returns the interface name.
func (p *Port) Name() string { return p.iface.Name }

// LinkUp implements l2.LinkStater: the interface is up and running.
func (p *Port) LinkUp() bool {
	ifi, err := net.InterfaceByIndex(p.iface.Index)
	return err == nil && ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagRunning != 0
}

// JoinMulticast implements l2.MulticastPort.
func (p *Port) JoinMulticast(group ethernet.MAC) error {
	return p.membership(syscall.PACKET_ADD_MEMBERSHIP, group)
}

// LeaveMulticast implements l2.MulticastPort.
func (p *Port) LeaveMulticast(group ethernet.MAC) error {
	return p.membership(syscall.PACKET_DROP_MEMBERSHIP, group)
}

func (p *Port) membership(op int, group ethernet.MAC) error {
	var err error
	cerr := p.raw.Control(func(fd uintptr) {
		err = setMembership(int(fd), p.iface.Index, op, syscall.PACKET_MR_MULTICAST, group[:])
	})
	if cerr != nil {
		return fmt.Errorf("afpacket: %w", profinet.ErrClosed)
	}
	return err
}

// ReadFrame implements l2.Port. It restores the 802.1Q tag of tagged
// frames and skips the frames the host sent.
func (p *Port) ReadFrame(ctx context.Context, b []byte) (int, error) {
	p.readMu.Lock()
	defer p.readMu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		_ = p.file.SetReadDeadline(dl)
	} else {
		_ = p.file.SetReadDeadline(time.Time{})
	}
	stop := context.AfterFunc(ctx, func() { _ = p.file.SetReadDeadline(time.Now()) })
	defer stop()
	buf := make([]byte, len(b)+4)
	oob := make([]byte, syscall.CmsgSpace(int(unsafe.Sizeof(tpacketAuxdata{}))))
	for {
		var n, oobn int
		var from syscall.Sockaddr
		var rerr error
		err := p.raw.Read(func(fd uintptr) bool {
			n, oobn, _, from, rerr = syscall.Recvmsg(int(fd), buf, oob, 0)
			return rerr != syscall.EAGAIN
		})
		if err == nil {
			err = rerr
		}
		if err != nil {
			select {
			case <-p.closed:
				return 0, fmt.Errorf("afpacket: %w", profinet.ErrClosed)
			default:
			}
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return 0, context.DeadlineExceeded
			}
			return 0, fmt.Errorf("afpacket: read %s: %w", p.iface.Name, err)
		}
		if ll, ok := from.(*syscall.SockaddrLinklayer); ok && ll.Pkttype == syscall.PACKET_OUTGOING {
			continue
		}
		frame := buf[:n]
		if tci, tpid, ok := vlanFromAux(oob[:oobn]); ok && n >= 12 {
			frame = insertTag(buf, n, tpid, tci)
		}
		return copy(b, frame), nil
	}
}

// vlanFromAux returns the tag the kernel removed, from the auxiliary data.
func vlanFromAux(oob []byte) (tci, tpid uint16, ok bool) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return 0, 0, false
	}
	for _, m := range msgs {
		if m.Header.Level != syscall.SOL_PACKET || m.Header.Type != packetAuxdata || len(m.Data) < int(unsafe.Sizeof(tpacketAuxdata{})) {
			continue
		}
		aux := (*tpacketAuxdata)(unsafe.Pointer(&m.Data[0]))
		if aux.Status&tpStatusVLANValid == 0 {
			return 0, 0, false
		}
		tpid = uint16(ethernet.EtherTypeVLAN)
		if aux.Status&tpStatusVLANTPIDValid != 0 && aux.VLANTPID != 0 {
			tpid = aux.VLANTPID
		}
		return aux.VLANTCI, tpid, true
	}
	return 0, 0, false
}

// insertTag puts the tag back after the source address; buf has room for
// the four octets.
func insertTag(buf []byte, n int, tpid, tci uint16) []byte {
	copy(buf[16:n+4], buf[12:n])
	binary.BigEndian.PutUint16(buf[12:], tpid)
	binary.BigEndian.PutUint16(buf[14:], tci)
	return buf[:n+4]
}

// WriteFrame implements l2.Port.
func (p *Port) WriteFrame(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame := b
	if len(frame) < ethernet.MinFrameLen {
		frame = ethernet.Pad(append(make([]byte, 0, ethernet.MinFrameLen), b...), 0)
	}
	sa := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ALL), Ifindex: p.iface.Index, Halen: 6}
	copy(sa.Addr[:], frame[:6])
	var werr error
	err := p.raw.Write(func(fd uintptr) bool {
		werr = syscall.Sendto(int(fd), frame, 0, sa)
		return werr != syscall.EAGAIN
	})
	if err == nil {
		err = werr
	}
	if err != nil {
		select {
		case <-p.closed:
			return fmt.Errorf("afpacket: %w", profinet.ErrClosed)
		default:
		}
		if errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENXIO) {
			return fmt.Errorf("afpacket: write %s: %w", p.iface.Name, profinet.ErrLinkDown)
		}
		return fmt.Errorf("afpacket: write %s: %w", p.iface.Name, err)
	}
	return nil
}

// Close implements l2.Port.
func (p *Port) Close() error {
	var err error
	p.closeMu.Do(func() {
		close(p.closed)
		err = p.file.Close()
	})
	return err
}
