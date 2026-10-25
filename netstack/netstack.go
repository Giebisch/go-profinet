// SPDX-License-Identifier: MIT

// Package netstack is a minimal IPv4 stack (ARP, IPv4 without fragments,
// UDP) on an [l2.Mux]. It gives a PROFINET endpoint on a virtual network
// the UDP sockets that RPC needs, so that every octet between a virtual
// controller and a virtual device — DCP, RT, alarms and RPC alike — is a
// serialized Ethernet frame that faults can be injected into and that
// Wireshark can dissect.
//
// An address can be changed at run time with [Stack.SetAddr], which is how
// a virtual IO-Device applies a DCP Set of its IP suite.
//
// The stack is meant for virtual networks and tests. On a real interface
// use the operating system's stack (net.ListenPacket).
package netstack

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
)

const (
	ipHeaderLen  = 20
	udpHeaderLen = 8
	arpLen       = 28
	protoUDP     = 17
	ephemeralLo  = 49152
)

// Config configures a [Stack].
type Config struct {
	// Addr is the initial IPv4 address; the zero value leaves the stack
	// without an address until SetAddr (it answers no ARP and accepts
	// only broadcasts).
	Addr netip.Addr
	// Mask is the subnet mask, for example 255.255.255.0.
	Mask netip.Addr
	// Gateway is the default gateway; unset means none.
	Gateway netip.Addr
	// ARPTimeout bounds one address resolution. Zero means 1 s.
	ARPTimeout time.Duration
	// QueueLen is the receive queue length of each socket. Zero means
	// 64 datagrams.
	QueueLen int
}

// Stats are the counters of a [Stack].
type Stats struct {
	ReceivedIP, SentIP     uint64
	BadChecksum, Fragments uint64
	NoSocket, Overflow     uint64
	ARPRequests, ARPReplys uint64
}

// Stack is a minimal IPv4/UDP stack. It is safe for concurrent use.
type Stack struct {
	mux *l2.Mux
	cfg Config
	mac ethernet.MAC

	mu      sync.RWMutex
	addr    netip.Addr
	mask    netip.Addr
	gw      netip.Addr
	arp     map[netip.Addr]ethernet.MAC
	waiters map[netip.Addr][]chan struct{}
	socks   map[uint16]*PacketConn
	nextEph uint16

	ipID atomic.Uint32

	stats struct {
		rxIP, txIP, badSum, frag, noSock, overflow, arpReq, arpRep atomic.Uint64
	}

	sub    *l2.Subscription
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// New starts a stack on mux. The stack subscribes to IPv4 and ARP frames.
func New(mux *l2.Mux, cfg Config) (*Stack, error) {
	if cfg.ARPTimeout <= 0 {
		cfg.ARPTimeout = time.Second
	}
	if cfg.QueueLen <= 0 {
		cfg.QueueLen = 64
	}
	if err := checkAddr(cfg.Addr, cfg.Mask, cfg.Gateway); err != nil {
		return nil, err
	}
	sub, err := mux.Subscribe(func(f *ethernet.Frame) bool {
		return f.EtherType == ethernet.EtherTypeIPv4 || f.EtherType == ethernet.EtherTypeARP
	}, 256)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Stack{
		mux:     mux,
		cfg:     cfg,
		mac:     mux.HardwareAddr(),
		addr:    cfg.Addr,
		mask:    cfg.Mask,
		gw:      cfg.Gateway,
		arp:     make(map[netip.Addr]ethernet.MAC),
		waiters: make(map[netip.Addr][]chan struct{}),
		socks:   make(map[uint16]*PacketConn),
		nextEph: ephemeralLo,
		sub:     sub,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go s.run(ctx)
	return s, nil
}

func checkAddr(addr, mask, gw netip.Addr) error {
	for _, a := range []netip.Addr{addr, mask, gw} {
		if a.IsValid() && !a.Is4() {
			return fmt.Errorf("netstack: %w: %s is not an IPv4 address", profinet.ErrInvalidConfig, a)
		}
	}
	if addr.IsValid() && !mask.IsValid() {
		return fmt.Errorf("netstack: %w: address without a mask", profinet.ErrInvalidConfig)
	}
	return nil
}

// SetAddr changes the address, mask and gateway. Zero values remove them.
// Sockets stay open; datagrams they send afterwards carry the new source
// address. The ARP cache is cleared.
func (s *Stack) SetAddr(addr, mask, gw netip.Addr) error {
	if err := checkAddr(addr, mask, gw); err != nil {
		return err
	}
	s.mu.Lock()
	s.addr, s.mask, s.gw = addr, mask, gw
	s.arp = make(map[netip.Addr]ethernet.MAC)
	s.mu.Unlock()
	return nil
}

// Addr returns the current address, mask and gateway.
func (s *Stack) Addr() (addr, mask, gw netip.Addr) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.addr, s.mask, s.gw
}

// Stats returns a snapshot of the counters.
func (s *Stack) Stats() Stats {
	return Stats{
		ReceivedIP:  s.stats.rxIP.Load(),
		SentIP:      s.stats.txIP.Load(),
		BadChecksum: s.stats.badSum.Load(),
		Fragments:   s.stats.frag.Load(),
		NoSocket:    s.stats.noSock.Load(),
		Overflow:    s.stats.overflow.Load(),
		ARPRequests: s.stats.arpReq.Load(),
		ARPReplys:   s.stats.arpRep.Load(),
	}
}

// Close closes every socket and stops the stack.
func (s *Stack) Close() error {
	s.once.Do(func() {
		s.cancel()
		s.sub.Close()
		<-s.done
		s.mu.Lock()
		socks := s.socks
		s.socks = map[uint16]*PacketConn{}
		s.mu.Unlock()
		for _, c := range socks {
			c.closeLocal()
		}
	})
	return nil
}

func (s *Stack) run(ctx context.Context) {
	defer close(s.done)
	for {
		f, err := s.sub.Recv(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, profinet.ErrClosed) {
				return
			}
			continue
		}
		switch f.EtherType {
		case ethernet.EtherTypeARP:
			s.handleARP(ctx, &f)
		case ethernet.EtherTypeIPv4:
			s.handleIP(&f)
		default:
		}
	}
}

// ListenUDP opens a UDP socket on port; 0 picks an ephemeral port.
func (s *Stack) ListenUDP(port uint16) (*PacketConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return nil, fmt.Errorf("netstack: %w", profinet.ErrClosed)
	default:
	}
	if port == 0 {
		for i := 0; i < 65536-ephemeralLo; i++ {
			p := s.nextEph
			s.nextEph++
			if s.nextEph == 0 {
				s.nextEph = ephemeralLo
			}
			if s.socks[p] == nil {
				port = p
				break
			}
		}
		if port == 0 {
			return nil, fmt.Errorf("netstack: %w: no ephemeral port", profinet.ErrResourceExhausted)
		}
	}
	if s.socks[port] != nil {
		return nil, fmt.Errorf("netstack: %w: port %d in use", profinet.ErrInvalidConfig, port)
	}
	c := &PacketConn{
		stack: s,
		port:  port,
		rx:    make(chan datagram, s.cfg.QueueLen),
		done:  make(chan struct{}),
	}
	c.rchanged = make(chan struct{})
	s.socks[port] = c
	return c, nil
}

// ListenPacket opens a UDP socket on the port of address, which has the
// form ":port" or "ip:port"; the IP is ignored. It matches the signature
// RPC endpoints take for opening their sockets.
func (s *Stack) ListenPacket(_ context.Context, network, address string) (net.PacketConn, error) {
	if network != "udp" && network != "udp4" {
		return nil, fmt.Errorf("netstack: %w: network %q", profinet.ErrUnsupported, network)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		_, port, err2 := net.SplitHostPort(address)
		if err2 != nil {
			return nil, fmt.Errorf("netstack: %w: address %q", profinet.ErrInvalidConfig, address)
		}
		var p uint64
		if _, err := fmt.Sscan(port, &p); err != nil || p > 65535 {
			return nil, fmt.Errorf("netstack: %w: address %q", profinet.ErrInvalidConfig, address)
		}
		return s.ListenUDP(uint16(p))
	}
	return s.ListenUDP(ap.Port())
}

func (s *Stack) handleARP(ctx context.Context, f *ethernet.Frame) {
	p := f.Payload
	if len(p) < arpLen {
		return
	}
	// Ethernet / IPv4 only.
	if binary.BigEndian.Uint16(p[0:2]) != 1 || binary.BigEndian.Uint16(p[2:4]) != 0x0800 || p[4] != 6 || p[5] != 4 {
		return
	}
	op := binary.BigEndian.Uint16(p[6:8])
	var sha ethernet.MAC
	copy(sha[:], p[8:14])
	spa := netip.AddrFrom4([4]byte(p[14:18]))
	tpa := netip.AddrFrom4([4]byte(p[24:28]))

	s.mu.Lock()
	own := s.addr
	if spa.IsValid() && !spa.IsUnspecified() {
		s.arp[spa] = sha
		for _, w := range s.waiters[spa] {
			close(w)
		}
		delete(s.waiters, spa)
	}
	s.mu.Unlock()

	switch op {
	case 1:
		s.stats.arpReq.Add(1)
		if own.IsValid() && tpa == own {
			_ = s.sendARP(ctx, 2, sha, own, spa)
		}
	case 2:
		s.stats.arpRep.Add(1)
	}
}

func (s *Stack) sendARP(ctx context.Context, op uint16, tha ethernet.MAC, spa, tpa netip.Addr) error {
	dst := tha
	if op == 1 {
		dst = ethernet.Broadcast
		tha = ethernet.MAC{}
	}
	b := make([]byte, 0, ethernet.MinFrameLen)
	hdr := ethernet.Frame{Dst: dst, Src: s.mac, EtherType: ethernet.EtherTypeARP}
	b = hdr.AppendHeader(b)
	b = binary.BigEndian.AppendUint16(b, 1)
	b = binary.BigEndian.AppendUint16(b, 0x0800)
	b = append(b, 6, 4)
	b = binary.BigEndian.AppendUint16(b, op)
	b = append(b, s.mac[:]...)
	a4 := spa.As4()
	b = append(b, a4[:]...)
	b = append(b, tha[:]...)
	t4 := tpa.As4()
	b = append(b, t4[:]...)
	return s.mux.Send(ctx, ethernet.Pad(b, 0))
}

func (s *Stack) handleIP(f *ethernet.Frame) {
	p := f.Payload
	if len(p) < ipHeaderLen || p[0]>>4 != 4 {
		return
	}
	ihl := int(p[0]&0x0F) * 4
	total := int(binary.BigEndian.Uint16(p[2:4]))
	if ihl < ipHeaderLen || total < ihl || total > len(p) {
		return
	}
	if checksum(p[:ihl], 0) != 0 {
		s.stats.badSum.Add(1)
		return
	}
	if flags := binary.BigEndian.Uint16(p[6:8]); flags&0x2000 != 0 || flags&0x1FFF != 0 {
		s.stats.frag.Add(1)
		return
	}
	s.stats.rxIP.Add(1)
	src := netip.AddrFrom4([4]byte(p[12:16]))
	dst := netip.AddrFrom4([4]byte(p[16:20]))

	s.mu.Lock()
	own, mask := s.addr, s.mask
	if src.IsValid() && !src.IsUnspecified() && own.IsValid() && sameSubnet(src, own, mask) {
		s.arp[src] = f.Src
	}
	s.mu.Unlock()

	if !(own.IsValid() && dst == own) && !isBroadcast(dst, own, mask) {
		return
	}
	if p[9] != protoUDP {
		return
	}
	u := p[ihl:total]
	if len(u) < udpHeaderLen {
		return
	}
	ulen := int(binary.BigEndian.Uint16(u[4:6]))
	if ulen < udpHeaderLen || ulen > len(u) {
		return
	}
	u = u[:ulen]
	if sum := binary.BigEndian.Uint16(u[6:8]); sum != 0 {
		if checksum(u, pseudoSum(src, dst, ulen)) != 0 {
			s.stats.badSum.Add(1)
			return
		}
	}
	sport := binary.BigEndian.Uint16(u[0:2])
	dport := binary.BigEndian.Uint16(u[2:4])
	s.mu.RLock()
	c := s.socks[dport]
	s.mu.RUnlock()
	if c == nil {
		s.stats.noSock.Add(1)
		return
	}
	data := append([]byte(nil), u[udpHeaderLen:]...)
	from := &net.UDPAddr{IP: src.AsSlice(), Port: int(sport)}
	select {
	case c.rx <- datagram{data: data, from: from}:
	default:
		s.stats.overflow.Add(1)
	}
}

func isBroadcast(dst, own, mask netip.Addr) bool {
	if dst == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return true
	}
	if !own.IsValid() || !mask.IsValid() {
		return false
	}
	o, m, d := own.As4(), mask.As4(), dst.As4()
	for i := range d {
		if d[i] != o[i]&m[i]|^m[i] {
			return false
		}
	}
	return true
}

func sameSubnet(a, b, mask netip.Addr) bool {
	if !mask.IsValid() {
		return false
	}
	x, y, m := a.As4(), b.As4(), mask.As4()
	for i := range m {
		if x[i]&m[i] != y[i]&m[i] {
			return false
		}
	}
	return true
}

// resolve returns the MAC address to send a datagram for dst to.
func (s *Stack) resolve(ctx context.Context, dst netip.Addr) (ethernet.MAC, error) {
	s.mu.Lock()
	own, mask, gw := s.addr, s.mask, s.gw
	if isBroadcast(dst, own, mask) {
		s.mu.Unlock()
		return ethernet.Broadcast, nil
	}
	if !own.IsValid() {
		s.mu.Unlock()
		return ethernet.MAC{}, fmt.Errorf("netstack: %w: no IP address", profinet.ErrInvalidState)
	}
	next := dst
	if !sameSubnet(dst, own, mask) {
		if !gw.IsValid() || gw.IsUnspecified() {
			s.mu.Unlock()
			return ethernet.MAC{}, fmt.Errorf("netstack: %w: no route to %s", profinet.ErrNotFound, dst)
		}
		next = gw
	}
	if mac, ok := s.arp[next]; ok {
		s.mu.Unlock()
		return mac, nil
	}
	w := make(chan struct{})
	s.waiters[next] = append(s.waiters[next], w)
	s.mu.Unlock()

	deadline := time.Now().Add(s.cfg.ARPTimeout)
	for attempt := 0; attempt < 3; attempt++ {
		if err := s.sendARP(ctx, 1, ethernet.MAC{}, own, next); err != nil {
			return ethernet.MAC{}, err
		}
		t := time.NewTimer(time.Until(deadline) / time.Duration(3-attempt))
		select {
		case <-w:
			t.Stop()
			s.mu.RLock()
			mac, ok := s.arp[next]
			s.mu.RUnlock()
			if ok {
				return mac, nil
			}
			return ethernet.MAC{}, fmt.Errorf("netstack: %w: ARP for %s", profinet.ErrTimeout, next)
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ethernet.MAC{}, ctx.Err()
		case <-s.done:
			t.Stop()
			return ethernet.MAC{}, fmt.Errorf("netstack: %w", profinet.ErrClosed)
		}
	}
	return ethernet.MAC{}, fmt.Errorf("netstack: %w: ARP for %s", profinet.ErrTimeout, next)
}

func (s *Stack) sendUDP(ctx context.Context, sport uint16, dst netip.AddrPort, data []byte) error {
	if len(data) > s.mux.Port().MTU()-ipHeaderLen-udpHeaderLen {
		return fmt.Errorf("netstack: %w: datagram of %d octets exceeds the MTU (no IP fragmentation)", profinet.ErrInvalidConfig, len(data))
	}
	mac, err := s.resolve(ctx, dst.Addr())
	if err != nil {
		return err
	}
	src, _, _ := s.Addr()
	if !src.IsValid() {
		src = netip.IPv4Unspecified()
	}
	total := ipHeaderLen + udpHeaderLen + len(data)
	b := make([]byte, 0, ethernet.HeaderLen+total+ethernet.MinFrameLen)
	hdr := ethernet.Frame{Dst: mac, Src: s.mac, EtherType: ethernet.EtherTypeIPv4}
	b = hdr.AppendHeader(b)
	ip := len(b)
	b = append(b, 0x45, 0)
	b = binary.BigEndian.AppendUint16(b, uint16(total))
	b = binary.BigEndian.AppendUint16(b, uint16(s.ipID.Add(1)))
	b = binary.BigEndian.AppendUint16(b, 0x4000) // DF
	b = append(b, 64, protoUDP, 0, 0)
	s4, d4 := src.As4(), dst.Addr().As4()
	b = append(b, s4[:]...)
	b = append(b, d4[:]...)
	binary.BigEndian.PutUint16(b[ip+10:], checksum(b[ip:ip+ipHeaderLen], 0))
	u := len(b)
	b = binary.BigEndian.AppendUint16(b, sport)
	b = binary.BigEndian.AppendUint16(b, dst.Port())
	b = binary.BigEndian.AppendUint16(b, uint16(udpHeaderLen+len(data)))
	b = append(b, 0, 0)
	b = append(b, data...)
	sum := checksum(b[u:], pseudoSum(src, dst.Addr(), udpHeaderLen+len(data)))
	if sum == 0 {
		sum = 0xFFFF
	}
	binary.BigEndian.PutUint16(b[u+6:], sum)
	if err := s.mux.Send(ctx, ethernet.Pad(b, 0)); err != nil {
		return err
	}
	s.stats.txIP.Add(1)
	return nil
}

func pseudoSum(src, dst netip.Addr, length int) uint32 {
	s4, d4 := src.As4(), dst.As4()
	var sum uint32
	sum += uint32(s4[0])<<8 | uint32(s4[1])
	sum += uint32(s4[2])<<8 | uint32(s4[3])
	sum += uint32(d4[0])<<8 | uint32(d4[1])
	sum += uint32(d4[2])<<8 | uint32(d4[3])
	sum += protoUDP
	sum += uint32(length)
	return sum
}

// checksum is the Internet checksum of b, started with initial.
func checksum(b []byte, initial uint32) uint16 {
	sum := initial
	for len(b) >= 2 {
		sum += uint32(b[0])<<8 | uint32(b[1])
		b = b[2:]
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xFFFF + sum>>16
	}
	return ^uint16(sum)
}

type datagram struct {
	data []byte
	from *net.UDPAddr
}

type deadline struct {
	t time.Time
}

// PacketConn is a UDP socket of a [Stack]. It implements net.PacketConn.
type PacketConn struct {
	stack *Stack
	port  uint16
	rx    chan datagram
	done  chan struct{}
	once  sync.Once
	wdl   atomic.Pointer[deadline]

	dmu       sync.Mutex
	rdeadline time.Time
	rchanged  chan struct{}
}

// ReadFrom implements net.PacketConn. A read deadline set while ReadFrom
// is blocked takes effect immediately.
func (c *PacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.dmu.Lock()
		dl, changed := c.rdeadline, c.rchanged
		c.dmu.Unlock()
		var timeout <-chan time.Time
		var t *time.Timer
		if !dl.IsZero() {
			w := time.Until(dl)
			if w <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			t = time.NewTimer(w)
			timeout = t.C
		}
		select {
		case d := <-c.rx:
			if t != nil {
				t.Stop()
			}
			return copy(p, d.data), d.from, nil
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case <-changed:
			if t != nil {
				t.Stop()
			}
		case <-c.done:
			if t != nil {
				t.Stop()
			}
			return 0, nil, net.ErrClosed
		}
	}
}

// WriteTo implements net.PacketConn. addr must be a *net.UDPAddr with an
// IPv4 address.
func (c *PacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, fmt.Errorf("netstack: %w: address %v", profinet.ErrInvalidConfig, addr)
	}
	ip, ok := netip.AddrFromSlice(ua.IP)
	if !ok {
		return 0, fmt.Errorf("netstack: %w: address %v", profinet.ErrInvalidConfig, addr)
	}
	ip = ip.Unmap()
	ctx := context.Background()
	if d := c.wdl.Load(); d != nil && !d.t.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, d.t)
		defer cancel()
	}
	if err := c.stack.sendUDP(ctx, c.port, netip.AddrPortFrom(ip, uint16(ua.Port)), p); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, os.ErrDeadlineExceeded
		}
		return 0, err
	}
	return len(p), nil
}

// Close implements net.PacketConn.
func (c *PacketConn) Close() error {
	c.stack.mu.Lock()
	if c.stack.socks[c.port] == c {
		delete(c.stack.socks, c.port)
	}
	c.stack.mu.Unlock()
	c.closeLocal()
	return nil
}

func (c *PacketConn) closeLocal() { c.once.Do(func() { close(c.done) }) }

// LocalAddr implements net.PacketConn.
func (c *PacketConn) LocalAddr() net.Addr {
	a, _, _ := c.stack.Addr()
	if !a.IsValid() {
		a = netip.IPv4Unspecified()
	}
	return &net.UDPAddr{IP: a.AsSlice(), Port: int(c.port)}
}

// SetDeadline implements net.PacketConn.
func (c *PacketConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}

// SetReadDeadline implements net.PacketConn.
func (c *PacketConn) SetReadDeadline(t time.Time) error {
	c.dmu.Lock()
	c.rdeadline = t
	close(c.rchanged)
	c.rchanged = make(chan struct{})
	c.dmu.Unlock()
	return nil
}

// SetWriteDeadline implements net.PacketConn.
func (c *PacketConn) SetWriteDeadline(t time.Time) error {
	c.wdl.Store(&deadline{t})
	return nil
}
