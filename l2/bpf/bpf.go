// SPDX-License-Identifier: MIT

//go:build darwin || freebsd || netbsd || dragonfly

// Package bpf is the macOS, FreeBSD, NetBSD and DragonFly backend of l2.Port: a Berkeley Packet
// Filter device (/dev/bpf*) attached to one network interface.
//
// Opening a port needs read and write access to a /dev/bpf device: root,
// or on macOS membership of the access_bpf group that Wireshark's ChmodBPF
// helper creates. The device is set to immediate mode, writes complete
// headers (the source address is the frame's), does not return the frames
// the host sends, and runs in promiscuous mode only when asked.
package bpf

import (
	"context"
	"errors"
	"fmt"
	"net"
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
}

// Port is a BPF device bound to one interface. It implements l2.Port and
// l2.LinkStater.
type Port struct {
	iface *net.Interface
	mac   ethernet.MAC
	fd    int

	readMu  sync.Mutex
	pending []byte // records read but not returned yet
	buf     []byte

	writeMu sync.Mutex
	closed  chan struct{}
	once    sync.Once
}

// readTimeout is how long a read waits before it checks for cancellation.
const readTimeout = 100 * time.Millisecond

func ioctl(fd int, req uint, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

// Open opens a BPF device on the named interface.
func Open(name string, opts Options) (*Port, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("bpf: %w: interface %q: %v", profinet.ErrNotFound, name, err)
	}
	if len(ifi.HardwareAddr) != 6 {
		return nil, fmt.Errorf("bpf: %w: interface %q is not Ethernet", profinet.ErrUnsupported, name)
	}
	fd := -1
	var lastErr error
	for i := 0; i < 256; i++ {
		fd, err = syscall.Open(fmt.Sprintf("/dev/bpf%d", i), syscall.O_RDWR|syscall.O_CLOEXEC, 0)
		if err == nil {
			break
		}
		lastErr = err
		if errors.Is(err, syscall.EBUSY) {
			continue
		}
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return nil, fmt.Errorf("bpf: open /dev/bpf%d: %w (needs root or the access_bpf group)", i, err)
		}
		if errors.Is(err, syscall.ENOENT) {
			break
		}
	}
	if fd < 0 {
		return nil, fmt.Errorf("bpf: %w: no free /dev/bpf device: %v", profinet.ErrResourceExhausted, lastErr)
	}
	ok := false
	defer func() {
		if !ok {
			_ = syscall.Close(fd)
		}
	}()
	// The buffer length must be set before the interface.
	blen := uint32(1 << 16)
	_ = ioctl(fd, syscall.BIOCSBLEN, unsafe.Pointer(&blen))
	var ifr [32]byte // struct ifreq
	copy(ifr[:16], name)
	if err := ioctl(fd, syscall.BIOCSETIF, unsafe.Pointer(&ifr[0])); err != nil {
		return nil, fmt.Errorf("bpf: attach to %s: %w", name, err)
	}
	one := uint32(1)
	zero := uint32(0)
	for _, s := range []struct {
		req  uint
		arg  *uint32
		what string
	}{
		{syscall.BIOCIMMEDIATE, &one, "immediate mode"},
		{syscall.BIOCSHDRCMPLT, &one, "complete headers"},
		{syscall.BIOCSSEESENT, &zero, "see-sent"},
	} {
		if err := ioctl(fd, s.req, unsafe.Pointer(s.arg)); err != nil {
			return nil, fmt.Errorf("bpf: %s: %w", s.what, err)
		}
	}
	if opts.Promiscuous {
		if err := ioctl(fd, syscall.BIOCPROMISC, nil); err != nil {
			return nil, fmt.Errorf("bpf: promiscuous: %w", err)
		}
	}
	tv := syscall.NsecToTimeval(int64(readTimeout))
	if err := ioctl(fd, syscall.BIOCSRTIMEOUT, unsafe.Pointer(&tv)); err != nil {
		return nil, fmt.Errorf("bpf: read timeout: %w", err)
	}
	if err := ioctl(fd, syscall.BIOCGBLEN, unsafe.Pointer(&blen)); err != nil {
		return nil, fmt.Errorf("bpf: buffer length: %w", err)
	}
	ok = true
	return &Port{iface: ifi, mac: ethernet.MAC(ifi.HardwareAddr), fd: fd, buf: make([]byte, blen), closed: make(chan struct{})}, nil
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

// LinkUp implements l2.LinkStater.
func (p *Port) LinkUp() bool {
	ifi, err := net.InterfaceByIndex(p.iface.Index)
	return err == nil && ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagRunning != 0
}

// ReadFrame implements l2.Port.
func (p *Port) ReadFrame(ctx context.Context, b []byte) (int, error) {
	p.readMu.Lock()
	defer p.readMu.Unlock()
	for {
		// A closed port reads nothing, not even the frames left over from
		// the last read of the device.
		select {
		case <-p.closed:
			return 0, fmt.Errorf("bpf: %w", profinet.ErrClosed)
		default:
		}
		if rec, rest, ok := nextRecord(p.pending); ok {
			p.pending = rest
			if rec == nil {
				continue
			}
			return copy(b, rec), nil
		}
		p.pending = nil
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := syscall.Read(p.fd, p.buf)
		if err != nil {
			select {
			case <-p.closed:
				return 0, fmt.Errorf("bpf: %w", profinet.ErrClosed)
			default:
			}
			if errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			return 0, fmt.Errorf("bpf: read %s: %w", p.iface.Name, err)
		}
		p.pending = p.buf[:n]
	}
}

// hdrLen is the size of struct bpf_hdr up to its padding.
var hdrLen = int(unsafe.Sizeof(syscall.BpfHdr{}))

// wordAlign rounds up to the alignment of BPF records.
func wordAlign(n int) int {
	const a = int(unsafe.Sizeof(int32(0)))
	return (n + a - 1) &^ (a - 1)
}

// nextRecord returns the frame of the first record of b and the rest. A
// truncated record ends the buffer (ok false); a frame the snapshot cut
// short is skipped (rec nil, ok true).
func nextRecord(b []byte) (rec, rest []byte, ok bool) {
	if len(b) < hdrLen {
		return nil, nil, false
	}
	h := (*syscall.BpfHdr)(unsafe.Pointer(&b[0]))
	start := int(h.Hdrlen)
	end := start + int(h.Caplen)
	if start < hdrLen-2 || end > len(b) {
		return nil, nil, false
	}
	next := wordAlign(end)
	if next > len(b) {
		next = len(b)
	}
	if h.Caplen < h.Datalen {
		return nil, b[next:], true
	}
	return b[start:end], b[next:], true
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
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	select {
	case <-p.closed:
		return fmt.Errorf("bpf: %w", profinet.ErrClosed)
	default:
	}
	if _, err := syscall.Write(p.fd, frame); err != nil {
		if errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENXIO) {
			return fmt.Errorf("bpf: write %s: %w", p.iface.Name, profinet.ErrLinkDown)
		}
		return fmt.Errorf("bpf: write %s: %w", p.iface.Name, err)
	}
	return nil
}

// Close implements l2.Port. A read in progress returns within the read
// timeout.
func (p *Port) Close() error {
	var err error
	p.once.Do(func() {
		close(p.closed)
		p.writeMu.Lock()
		// Wait for the reader so that the descriptor is not reused under it.
		p.readMu.Lock()
		err = syscall.Close(p.fd)
		p.readMu.Unlock()
		p.writeMu.Unlock()
	})
	return err
}
