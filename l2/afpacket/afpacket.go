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
	closeMu sync.Once
	closed  chan struct{}

	// The read state, guarded by readMu: ReadFrame reuses its buffers
	// and keeps the cancellation of the context of the previous call, so
	// that a read allocates nothing.
	readMu   sync.Mutex
	rbuf     []byte
	oob      [cmsgSpaceAux]byte
	from     syscall.RawSockaddrLinklayer
	iov      syscall.Iovec
	msg      syscall.Msghdr
	watched  context.Context
	stopWait func() bool
	fired    chan struct{}
	deadline time.Time
	// readFn is the recvmsg callback of raw.Read, made once (a closure
	// made per call would escape); its results.
	readFn  func(fd uintptr) bool
	rbufCur []byte
	rn      int
	roobn   int
	rpkt    uint8
	rerr    error

	// The write state, guarded by writeMu, with its callback.
	writeMu sync.Mutex
	writeFn func(fd uintptr) bool
	wframe  []byte
	werr    error

	// The SIOCGIFFLAGS state of LinkUp, guarded by linkMu: struct ifreq
	// (the name, then the flags, a short, in the union; 40 octets on
	// 64-bit platforms, 32 on 32-bit ones) and the callback.
	linkMu  sync.Mutex
	ifr     [40]byte
	linkFn  func(fd uintptr)
	linkErr syscall.Errno
}

const (
	// packetAuxdata is PACKET_AUXDATA, which syscall does not define on
	// every architecture.
	packetAuxdata = 8

	tpStatusVLANValid     = 1 << 4
	tpStatusVLANTPIDValid = 1 << 6
)

// cmsgSpaceAux is CMSG_SPACE(sizeof(struct tpacket_auxdata)), with room
// to spare: the ancillary data of one frame.
const cmsgSpaceAux = 64

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
			_ = syscall.Close(fd)
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
	p.readFn = func(fd uintptr) bool {
		p.msg.Namelen = uint32(syscall.SizeofSockaddrLinklayer)
		p.msg.SetControllen(len(p.oob))
		p.rn, p.roobn, p.rpkt, p.rerr = recvmsg(fd, p, p.rbufCur)
		return p.rerr != syscall.EAGAIN
	}
	p.writeFn = func(fd uintptr) bool {
		_, p.werr = syscall.Write(int(fd), p.wframe)
		return p.werr != syscall.EAGAIN
	}
	copy(p.ifr[:syscall.IFNAMSIZ-1], ifi.Name)
	p.linkFn = func(fd uintptr) {
		_, _, p.linkErr = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&p.ifr[0])))
	}
	p.file = os.NewFile(uintptr(fd), "afpacket:"+name)
	if p.raw, err = p.file.SyscallConn(); err != nil {
		_ = p.file.Close()
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

// LinkUp implements l2.LinkStater: the interface is up and running
// (IFF_UP and IFF_RUNNING, read with SIOCGIFFLAGS on the port's socket:
// no netlink dump, no allocation).
func (p *Port) LinkUp() bool {
	p.linkMu.Lock()
	defer p.linkMu.Unlock()
	if err := p.raw.Control(p.linkFn); err != nil || p.linkErr != 0 {
		return false
	}
	flags := *(*uint16)(unsafe.Pointer(&p.ifr[syscall.IFNAMSIZ]))
	return flags&syscall.IFF_UP != 0 && flags&syscall.IFF_RUNNING != 0
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
// frames and skips the frames the host sent. It allocates nothing: the
// receive buffer and ancillary data are the port's, and the watch of the
// context (which interrupts the read when ctx ends) stays in place while
// successive calls use the same context.
func (p *Port) ReadFrame(ctx context.Context, b []byte) (int, error) {
	p.readMu.Lock()
	defer p.readMu.Unlock()
	dl, _ := ctx.Deadline()
	if !dl.Equal(p.deadline) {
		_ = p.file.SetReadDeadline(dl)
		p.deadline = dl
	}
	p.watch(ctx)
	if cap(p.rbuf) < len(b)+4 {
		p.rbuf = make([]byte, len(b)+4)
	}
	buf := p.rbuf[:len(b)+4]
	for {
		n, oobn, pkttype, err := p.recv(buf)
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
		if pkttype == syscall.PACKET_OUTGOING {
			continue
		}
		frame := buf[:n]
		if tci, tpid, ok := vlanFromAux(p.oob[:oobn]); ok && n >= 12 {
			frame = insertTag(buf, n, tpid, tci)
		}
		return copy(b, frame), nil
	}
}

// watch makes the end of ctx interrupt a read, with a deadline in the
// past; it replaces the watch of another context. readMu held.
func (p *Port) watch(ctx context.Context) {
	if ctx == p.watched {
		return
	}
	p.unwatch()
	p.watched = ctx
	if ctx.Done() == nil {
		return
	}
	fired := make(chan struct{})
	p.fired = fired
	p.stopWait = context.AfterFunc(ctx, func() {
		defer close(fired)
		_ = p.file.SetReadDeadline(time.Now())
	})
}

// unwatch removes the watch of the previous context. A callback that has
// started must finish first: run late, it would set the deadline of a
// read whose context has none. The deadline is then set again. readMu
// held.
func (p *Port) unwatch() {
	if p.stopWait == nil {
		return
	}
	if !p.stopWait() {
		<-p.fired
		_ = p.file.SetReadDeadline(p.deadline)
	}
	p.stopWait, p.fired = nil, nil
}

// recv reads one frame with recvmsg into buf and the port's ancillary data
// buffer, without the allocations of syscall.Recvmsg (the source address
// and the parsed messages). readMu held.
func (p *Port) recv(buf []byte) (n, oobn int, pkttype uint8, err error) {
	p.iov.Base = &buf[0]
	p.iov.SetLen(len(buf))
	p.msg.Name = (*byte)(unsafe.Pointer(&p.from))
	p.msg.Namelen = uint32(syscall.SizeofSockaddrLinklayer)
	p.msg.Iov = &p.iov
	p.msg.Iovlen = 1
	p.msg.Control = &p.oob[0]
	p.msg.SetControllen(len(p.oob))
	p.rbufCur = buf
	err = p.raw.Read(p.readFn)
	p.rbufCur = nil
	if err == nil {
		err = p.rerr
	}
	if err != nil {
		return 0, 0, 0, err
	}
	return p.rn, p.roobn, p.rpkt, nil
}

// vlanFromAux returns the tag the kernel removed, from the auxiliary
// data (a walk over the control messages that allocates nothing).
func vlanFromAux(oob []byte) (tci, tpid uint16, ok bool) {
	for len(oob) >= syscall.SizeofCmsghdr {
		h := (*syscall.Cmsghdr)(unsafe.Pointer(&oob[0]))
		l := int(h.Len)
		if l < syscall.SizeofCmsghdr || l > len(oob) {
			return 0, 0, false
		}
		data := oob[cmsgAlign(syscall.SizeofCmsghdr):l]
		if h.Level == syscall.SOL_PACKET && h.Type == packetAuxdata && len(data) >= int(unsafe.Sizeof(tpacketAuxdata{})) {
			aux := (*tpacketAuxdata)(unsafe.Pointer(&data[0]))
			if aux.Status&tpStatusVLANValid == 0 {
				return 0, 0, false
			}
			tpid = uint16(ethernet.EtherTypeVLAN)
			if aux.Status&tpStatusVLANTPIDValid != 0 && aux.VLANTPID != 0 {
				tpid = aux.VLANTPID
			}
			return aux.VLANTCI, tpid, true
		}
		next := cmsgAlign(l)
		if next >= len(oob) {
			break
		}
		oob = oob[next:]
	}
	return 0, 0, false
}

// cmsgAlign rounds a control message length up to the alignment of the
// platform (CMSG_ALIGN).
func cmsgAlign(n int) int {
	const a = int(unsafe.Sizeof(uintptr(0)))
	return (n + a - 1) &^ (a - 1)
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
	// The socket is bound to the interface: a plain write sends on it,
	// without the address that sendto would convert (and allocate).
	p.writeMu.Lock()
	p.wframe = frame
	err := p.raw.Write(p.writeFn)
	if err == nil {
		err = p.werr
	}
	p.wframe = nil
	p.writeMu.Unlock()
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
		p.readMu.Lock()
		p.unwatch()
		p.readMu.Unlock()
	})
	return err
}
