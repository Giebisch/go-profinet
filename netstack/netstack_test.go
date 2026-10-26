// SPDX-License-Identifier: MIT

package netstack

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/vnet"
)

var mask24 = netip.MustParseAddr("255.255.255.0")

func newStack(t *testing.T, n *vnet.Network, last byte, addr string) *Stack {
	t.Helper()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{Name: addr, MAC: ethernet.MAC{2, 0, 0, 0, 0, last}})
	if err != nil {
		t.Fatal(err)
	}
	mux := l2.NewMux(ep)
	cfg := Config{ARPTimeout: 300 * time.Millisecond}
	if addr != "" {
		cfg.Addr = netip.MustParseAddr(addr)
		cfg.Mask = mask24
	}
	s, err := New(mux, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = mux.Close()
		_ = ep.Close()
	})
	return s
}

func TestUDPExchange(t *testing.T) {
	n := vnet.New(1)
	a := newStack(t, n, 1, "192.168.0.1")
	b := newStack(t, n, 2, "192.168.0.2")
	ca, err := a.ListenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := b.ListenPacket(context.Background(), "udp", ":34964")
	if err != nil {
		t.Fatal(err)
	}
	dst := &net.UDPAddr{IP: net.IPv4(192, 168, 0, 2), Port: 34964}
	if _, err := ca.WriteTo([]byte("hello"), dst); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	_ = cb.SetReadDeadline(time.Now().Add(2 * time.Second))
	nr, from, err := cb.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:nr]) != "hello" {
		t.Fatalf("got %q", buf[:nr])
	}
	if _, err := cb.WriteTo([]byte("world"), from); err != nil {
		t.Fatal(err)
	}
	_ = ca.SetReadDeadline(time.Now().Add(2 * time.Second))
	nr, _, err = ca.ReadFrom(buf)
	if err != nil || string(buf[:nr]) != "world" {
		t.Fatalf("got %q, %v", buf[:nr], err)
	}
	if st := b.Stats(); st.ReceivedIP != 1 || st.ARPRequests != 1 {
		t.Fatalf("stats %+v", st)
	}
	if ca.LocalAddr().String() != "192.168.0.1:49152" {
		t.Fatalf("local addr %v", ca.LocalAddr())
	}
}

func TestReadDeadlineAndClose(t *testing.T) {
	n := vnet.New(1)
	a := newStack(t, n, 1, "10.0.0.1")
	c, _ := a.ListenUDP(1000)
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := c.ReadFrom(make([]byte, 10)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	_ = c.SetReadDeadline(time.Time{})
	done := make(chan error)
	go func() {
		_, _, err := c.ReadFrom(make([]byte, 10))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	_ = c.SetReadDeadline(time.Now())
	if err := <-done; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	// The deadline of the previous read has passed: clear it before the
	// next read starts, or that read times out at once.
	_ = c.SetReadDeadline(time.Time{})
	go func() {
		_, _, err := c.ReadFrom(make([]byte, 10))
		done <- err
	}()
	_ = c.Close()
	if err := <-done; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if _, err := c.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if _, err := a.ListenUDP(1000); err != nil {
		t.Fatalf("port not released: %v", err)
	}
	if _, err := a.ListenUDP(1000); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
}

func TestARPTimeoutAndNoAddress(t *testing.T) {
	n := vnet.New(1)
	a := newStack(t, n, 1, "10.0.0.1")
	c, _ := a.ListenUDP(0)
	_, err := c.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 9), Port: 1})
	if !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	_, err = c.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(10, 1, 0, 9), Port: 1})
	if !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	u := newStack(t, n, 2, "")
	cu, _ := u.ListenUDP(0)
	if _, err := cu.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1}); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	// SetAddr makes it reachable.
	if err := u.SetAddr(netip.MustParseAddr("10.0.0.2"), mask24, netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	c2, _ := a.ListenUDP(0)
	if _, err := c2.WriteTo([]byte{7}, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: int(cu.port)}); err != nil {
		t.Fatal(err)
	}
	_ = cu.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4)
	if nr, _, err := cu.ReadFrom(buf); err != nil || nr != 1 || buf[0] != 7 {
		t.Fatalf("%d %v", nr, err)
	}
	if err := u.SetAddr(netip.MustParseAddr("::1"), mask24, netip.Addr{}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
}

func TestChecksum(t *testing.T) {
	// RFC 1071 example.
	b := []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7}
	if got := checksum(b, 0); got != ^uint16(0xddf2) {
		t.Fatalf("checksum %04x", got)
	}
}

// rawEP attaches an endpoint without a stack, through which a test sends
// hand-crafted frames to a stack and reads what the stack sends.
func rawEP(t *testing.T, n *vnet.Network, last byte) *vnet.Endpoint {
	t.Helper()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{Name: "raw", MAC: ethernet.MAC{2, 0, 0, 0, 0, last}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	return ep
}

// newStackCfg is newStack with a full configuration.
func newStackCfg(t *testing.T, n *vnet.Network, last byte, cfg Config) (*Stack, *l2.Mux, *vnet.Endpoint) {
	t.Helper()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{2, 0, 0, 0, 0, last}})
	if err != nil {
		t.Fatal(err)
	}
	mux := l2.NewMux(ep)
	s, err := New(mux, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = mux.Close()
		_ = ep.Close()
	})
	return s, mux, ep
}

func macOf(last byte) ethernet.MAC { return ethernet.MAC{2, 0, 0, 0, 0, last} }

func ip4(s string) netip.Addr { return netip.MustParseAddr(s) }

func sendRaw(t *testing.T, ep *vnet.Endpoint, b []byte) {
	t.Helper()
	if err := ep.WriteFrame(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

// readRaw reads the next frame the raw endpoint receives.
func readRaw(t *testing.T, ep *vnet.Endpoint) ethernet.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	buf := make([]byte, 1600)
	nr, err := ep.ReadFrame(ctx, buf)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ethernet.Decode(buf[:nr])
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// expectNoRaw checks that the raw endpoint receives nothing for a moment.
func expectNoRaw(t *testing.T, ep *vnet.Endpoint) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := ep.ReadFrame(ctx, make([]byte, 1600)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected frame (err %v)", err)
	}
}

func arpFrame(dst ethernet.MAC, op uint16, sha ethernet.MAC, spa netip.Addr, tha ethernet.MAC, tpa netip.Addr) []byte {
	p := []byte{0, 1, 8, 0, 6, 4, byte(op >> 8), byte(op)}
	p = append(p, sha[:]...)
	s4 := spa.As4()
	p = append(p, s4[:]...)
	p = append(p, tha[:]...)
	t4 := tpa.As4()
	p = append(p, t4[:]...)
	return (&ethernet.Frame{Dst: dst, Src: sha, EtherType: ethernet.EtherTypeARP, Payload: p}).Encode()
}

type ipOpts struct {
	flags      uint16
	proto      byte
	badIPSum   bool
	badUDPSum  bool
	zeroUDPSum bool
}

// udpFrame builds an IPv4/UDP frame with valid checksums unless opts say
// otherwise.
func udpFrame(dst, src ethernet.MAC, sip, dip netip.Addr, sport, dport uint16, data []byte, o ipOpts) []byte {
	proto := o.proto
	if proto == 0 {
		proto = protoUDP
	}
	ip := []byte{0x45, 0, 0, 0, 0, 1, byte(o.flags >> 8), byte(o.flags), 64, proto, 0, 0}
	binary.BigEndian.PutUint16(ip[2:], uint16(ipHeaderLen+udpHeaderLen+len(data)))
	s4, d4 := sip.As4(), dip.As4()
	ip = append(ip, s4[:]...)
	ip = append(ip, d4[:]...)
	sum := checksum(ip, 0)
	if o.badIPSum {
		sum++
	}
	binary.BigEndian.PutUint16(ip[10:], sum)
	u := binary.BigEndian.AppendUint16(nil, sport)
	u = binary.BigEndian.AppendUint16(u, dport)
	u = binary.BigEndian.AppendUint16(u, uint16(udpHeaderLen+len(data)))
	u = append(u, 0, 0)
	u = append(u, data...)
	if !o.zeroUDPSum {
		us := checksum(u, pseudoSum(sip, dip, len(u)))
		if us == 0 {
			us = 0xFFFF
		}
		if o.badUDPSum {
			us ^= 0x0101
		}
		binary.BigEndian.PutUint16(u[6:], us)
	}
	return (&ethernet.Frame{Dst: dst, Src: src, EtherType: ethernet.EtherTypeIPv4, Payload: append(ip, u...)}).Encode()
}

func readWithin(t *testing.T, c net.PacketConn) ([]byte, net.Addr) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 2000)
	nr, from, err := c.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:nr], from
}

func TestNewConfigErrors(t *testing.T) {
	n := vnet.New(1)
	ep := rawEP(t, n, 1)
	mux := l2.NewMux(ep)
	defer func() { _ = mux.Close() }()
	for _, cfg := range []Config{
		{Addr: ip4("fe80::1"), Mask: mask24},
		{Addr: ip4("10.0.0.1")},
		{Addr: ip4("10.0.0.1"), Mask: ip4("ffff::")},
		{Addr: ip4("10.0.0.1"), Mask: mask24, Gateway: ip4("::1")},
	} {
		if _, err := New(mux, cfg); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%+v: err %v", cfg, err)
		}
	}
	_ = mux.Close()
	if _, err := New(mux, Config{}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("closed mux: %v", err)
	}
}

func TestDefaults(t *testing.T) {
	n := vnet.New(1)
	s, _, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24, Gateway: ip4("10.0.0.254")})
	if s.cfg.ARPTimeout != time.Second || s.cfg.QueueLen != 64 {
		t.Fatalf("defaults %+v", s.cfg)
	}
	a, m, g := s.Addr()
	if a != ip4("10.0.0.1") || m != mask24 || g != ip4("10.0.0.254") {
		t.Fatalf("Addr %v %v %v", a, m, g)
	}
	if err := s.SetAddr(netip.Addr{}, netip.Addr{}, netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	c, _ := s.ListenUDP(7)
	if c.LocalAddr().String() != "0.0.0.0:7" {
		t.Fatalf("local addr %v", c.LocalAddr())
	}
}

func TestARPRequestReply(t *testing.T) {
	n := vnet.New(1)
	s := newStack(t, n, 1, "10.0.0.1")
	raw := rawEP(t, n, 9)
	rawIP := ip4("10.0.0.9")
	sendRaw(t, raw, arpFrame(ethernet.Broadcast, 1, macOf(9), rawIP, ethernet.MAC{}, ip4("10.0.0.1")))
	f := readRaw(t, raw)
	if f.EtherType != ethernet.EtherTypeARP || f.Dst != macOf(9) || f.Src != macOf(1) {
		t.Fatalf("reply %+v", f)
	}
	p := f.Payload
	want := []byte{0, 1, 8, 0, 6, 4, 0, 2, 2, 0, 0, 0, 0, 1, 10, 0, 0, 1, 2, 0, 0, 0, 0, 9, 10, 0, 0, 9}
	if !bytes.Equal(p[:arpLen], want) {
		t.Fatalf("reply % x", p[:arpLen])
	}
	if st := s.Stats(); st.ARPRequests != 1 || st.ARPReplys != 0 {
		t.Fatalf("stats %+v", st)
	}
	// The requester was learned: a datagram to it needs no ARP.
	c, _ := s.ListenUDP(0)
	if _, err := c.WriteTo([]byte("x"), &net.UDPAddr{IP: rawIP.AsSlice(), Port: 9}); err != nil {
		t.Fatal(err)
	}
	if f := readRaw(t, raw); f.EtherType != ethernet.EtherTypeIPv4 || f.Dst != macOf(9) {
		t.Fatalf("got %v to %v", f.EtherType, f.Dst)
	}
	// A request for another address is not answered.
	sendRaw(t, raw, arpFrame(ethernet.Broadcast, 1, macOf(9), rawIP, ethernet.MAC{}, ip4("10.0.0.2")))
	expectNoRaw(t, raw)
	if s.Stats().ARPRequests != 2 {
		t.Fatalf("stats %+v", s.Stats())
	}
}

func TestARPLearning(t *testing.T) {
	n := vnet.New(1)
	s := newStack(t, n, 1, "10.0.0.1")
	raw := rawEP(t, n, 9)
	c, _ := s.ListenUDP(0)
	write := func(ip string) {
		t.Helper()
		if _, err := c.WriteTo([]byte("x"), &net.UDPAddr{IP: net.ParseIP(ip), Port: 9}); err != nil {
			t.Fatal(err)
		}
	}
	// Gratuitous ARP announcement: learned, not answered.
	sendRaw(t, raw, arpFrame(ethernet.Broadcast, 1, macOf(9), ip4("10.0.0.9"), ethernet.MAC{}, ip4("10.0.0.9")))
	// Unsolicited reply with a different hardware address for .10.
	sendRaw(t, raw, arpFrame(macOf(1), 2, macOf(10), ip4("10.0.0.10"), macOf(1), ip4("10.0.0.1")))
	waitFor(t, func() bool { st := s.Stats(); return st.ARPRequests == 1 && st.ARPReplys == 1 })
	write("10.0.0.9")
	if f := readRaw(t, raw); f.EtherType != ethernet.EtherTypeIPv4 || f.Dst != macOf(9) {
		t.Fatalf("got %v to %v", f.EtherType, f.Dst)
	}
	s.mu.RLock()
	learned := s.arp[ip4("10.0.0.10")]
	s.mu.RUnlock()
	if learned != macOf(10) {
		t.Fatalf("learned %v", learned)
	}
	// A probe (sender 0.0.0.0) and malformed or unknown ARP are not learned.
	sendRaw(t, raw, arpFrame(ethernet.Broadcast, 1, macOf(11), ip4("0.0.0.0"), ethernet.MAC{}, ip4("10.0.0.11")))
	bad := arpFrame(ethernet.Broadcast, 1, macOf(12), ip4("10.0.0.12"), ethernet.MAC{}, ip4("10.0.0.1"))
	bad[15] = 6 // hardware type 6
	sendRaw(t, raw, bad)
	bad = arpFrame(ethernet.Broadcast, 1, macOf(12), ip4("10.0.0.12"), ethernet.MAC{}, ip4("10.0.0.1"))
	bad[18] = 8 // hardware length 8
	sendRaw(t, raw, bad)
	short := (&ethernet.Frame{Dst: ethernet.Broadcast, Src: macOf(9), EtherType: ethernet.EtherTypeARP, Payload: make([]byte, 20)}).Encode()
	sendRaw(t, raw, short[:ethernet.HeaderLen+20])
	sendRaw(t, raw, arpFrame(macOf(1), 3, macOf(13), ip4("10.0.0.13"), macOf(1), ip4("10.0.0.1")))
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, ok := s.arp[ip4("10.0.0.13")]
		return ok
	})
	s.mu.RLock()
	_, probe := s.arp[ip4("0.0.0.0")]
	_, b12 := s.arp[ip4("10.0.0.12")]
	s.mu.RUnlock()
	if probe || b12 {
		t.Fatal("probe or malformed ARP learned")
	}
	if st := s.Stats(); st.ARPRequests != 2 || st.ARPReplys != 1 {
		t.Fatalf("stats %+v", st)
	}
	expectNoRaw(t, raw)
	// SetAddr clears the cache: the next datagram to .9 needs ARP.
	if err := s.SetAddr(ip4("10.0.0.1"), mask24, netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = c.WriteTo([]byte("x"), &net.UDPAddr{IP: net.ParseIP("10.0.0.9"), Port: 9}) }()
	if f := readRaw(t, raw); f.EtherType != ethernet.EtherTypeARP || f.Dst != ethernet.Broadcast {
		t.Fatalf("got %v to %v", f.EtherType, f.Dst)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestARPResolveByReply(t *testing.T) {
	n := vnet.New(1)
	s, _, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24, ARPTimeout: 10 * time.Second})
	raw := rawEP(t, n, 9)
	c, _ := s.ListenUDP(4000)
	errc := make(chan error, 1)
	go func() {
		_, err := c.WriteTo([]byte("ping"), &net.UDPAddr{IP: net.ParseIP("10.0.0.9"), Port: 5000})
		errc <- err
	}()
	req := readRaw(t, raw)
	if req.EtherType != ethernet.EtherTypeARP || req.Dst != ethernet.Broadcast {
		t.Fatalf("got %v to %v", req.EtherType, req.Dst)
	}
	want := []byte{0, 1, 8, 0, 6, 4, 0, 1, 2, 0, 0, 0, 0, 1, 10, 0, 0, 1, 0, 0, 0, 0, 0, 0, 10, 0, 0, 9}
	if !bytes.Equal(req.Payload[:arpLen], want) {
		t.Fatalf("request % x", req.Payload[:arpLen])
	}
	sendRaw(t, raw, arpFrame(macOf(1), 2, macOf(9), ip4("10.0.0.9"), macOf(1), ip4("10.0.0.1")))
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	f := readRaw(t, raw)
	checkUDP(t, f, macOf(9), ip4("10.0.0.1"), ip4("10.0.0.9"), 4000, 5000, "ping")
}

// checkUDP verifies a frame sent by the stack.
func checkUDP(t *testing.T, f ethernet.Frame, dstMAC ethernet.MAC, src, dst netip.Addr, sport, dport uint16, data string) {
	t.Helper()
	if f.EtherType != ethernet.EtherTypeIPv4 || f.Dst != dstMAC {
		t.Fatalf("got %v to %v", f.EtherType, f.Dst)
	}
	p := f.Payload
	if p[0] != 0x45 || p[8] != 64 || p[9] != protoUDP || binary.BigEndian.Uint16(p[6:8]) != 0x4000 {
		t.Fatalf("IP header % x", p[:ipHeaderLen])
	}
	if checksum(p[:ipHeaderLen], 0) != 0 {
		t.Fatal("bad IP checksum")
	}
	total := int(binary.BigEndian.Uint16(p[2:4]))
	if total != ipHeaderLen+udpHeaderLen+len(data) {
		t.Fatalf("total length %d", total)
	}
	if netip.AddrFrom4([4]byte(p[12:16])) != src || netip.AddrFrom4([4]byte(p[16:20])) != dst {
		t.Fatalf("addresses % x", p[12:20])
	}
	u := p[ipHeaderLen:total]
	if binary.BigEndian.Uint16(u[0:2]) != sport || binary.BigEndian.Uint16(u[2:4]) != dport || int(binary.BigEndian.Uint16(u[4:6])) != len(u) {
		t.Fatalf("UDP header % x", u[:udpHeaderLen])
	}
	if binary.BigEndian.Uint16(u[6:8]) == 0 || checksum(u, pseudoSum(src, dst, len(u))) != 0 {
		t.Fatal("bad UDP checksum")
	}
	if string(u[udpHeaderLen:]) != data {
		t.Fatalf("data %q", u[udpHeaderLen:])
	}
}

func TestReceiveFiltering(t *testing.T) {
	n := vnet.New(1)
	s, _, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24, QueueLen: 1})
	raw := rawEP(t, n, 9)
	c, _ := s.ListenUDP(5000)
	src, own := ip4("10.0.0.9"), ip4("10.0.0.1")
	send := func(dst netip.Addr, dport uint16, o ipOpts) {
		t.Helper()
		sendRaw(t, raw, udpFrame(macOf(1), macOf(9), src, dst, 7, dport, []byte("d"), o))
	}
	// Each case must not reach the socket; the counter shows it was seen.
	type exp struct{ rx, sum, frag, nosock uint64 }
	cases := []struct {
		name string
		do   func()
		want exp
	}{
		{"bad IP checksum", func() { send(own, 5000, ipOpts{badIPSum: true}) }, exp{0, 1, 0, 0}},
		{"bad UDP checksum", func() { send(own, 5000, ipOpts{badUDPSum: true}) }, exp{1, 2, 0, 0}},
		{"more fragments", func() { send(own, 5000, ipOpts{flags: 0x2000}) }, exp{1, 2, 1, 0}},
		{"fragment offset", func() { send(own, 5000, ipOpts{flags: 0x4001}) }, exp{1, 2, 2, 0}},
		{"no socket", func() { send(own, 5001, ipOpts{}) }, exp{2, 2, 2, 1}},
		{"other host", func() { send(ip4("10.0.0.2"), 5000, ipOpts{}) }, exp{3, 2, 2, 1}},
		{"TCP", func() { send(own, 5000, ipOpts{proto: 6}) }, exp{4, 2, 2, 1}},
	}
	for _, tc := range cases {
		tc.do()
		waitFor(t, func() bool {
			st := s.Stats()
			return st.ReceivedIP+st.BadChecksum+st.Fragments+st.NoSocket >= tc.want.rx+tc.want.sum+tc.want.frag+tc.want.nosock
		})
		st := s.Stats()
		if got := (exp{st.ReceivedIP, st.BadChecksum, st.Fragments, st.NoSocket}); got != tc.want {
			t.Fatalf("%s: counters %+v, want %+v", tc.name, got, tc.want)
		}
	}

	// Malformed IP is dropped without counting; a valid IP header with a
	// malformed UDP header counts as received IP.
	good := udpFrame(macOf(1), macOf(9), src, own, 7, 5000, []byte("d"), ipOpts{})
	mangle := func(f func(p []byte) []byte) {
		b := append([]byte(nil), good...)
		p := f(b[ethernet.HeaderLen:])
		sendRaw(t, raw, append(b[:ethernet.HeaderLen:ethernet.HeaderLen], p...))
	}
	fixIP := func(p []byte) {
		p[10], p[11] = 0, 0
		binary.BigEndian.PutUint16(p[10:], checksum(p[:ipHeaderLen], 0))
	}
	mangle(func(p []byte) []byte { p[0] = 0x65; return p })                                     // IPv6 version
	mangle(func(p []byte) []byte { p[0] = 0x44; fixIP(p); return p })                           // IHL 16
	mangle(func(p []byte) []byte { binary.BigEndian.PutUint16(p[2:], 19); fixIP(p); return p }) // total < IHL
	mangle(func(p []byte) []byte { binary.BigEndian.PutUint16(p[2:], 2000); fixIP(p); return p })
	mangle(func(p []byte) []byte { binary.BigEndian.PutUint16(p[2:], 24); fixIP(p); return p }) // UDP too short
	mangle(func(p []byte) []byte { binary.BigEndian.PutUint16(p[24:], 4); return p })           // UDP length < 8
	mangle(func(p []byte) []byte { binary.BigEndian.PutUint16(p[24:], 100); return p })         // UDP length > IP
	short := (&ethernet.Frame{Dst: macOf(1), Src: macOf(9), EtherType: ethernet.EtherTypeIPv4, Payload: []byte{0x45}}).AppendHeader(nil)
	sendRaw(t, raw, append(short, 0x45, 0))

	// A valid datagram after all of them: the only one delivered.
	send(own, 5000, ipOpts{zeroUDPSum: true})
	data, from := readWithin(t, c)
	if string(data) != "d" || from.String() != "10.0.0.9:7" {
		t.Fatalf("got %q from %v", data, from)
	}
	st := s.Stats()
	want := Stats{ReceivedIP: 4 + 3 + 1, BadChecksum: 2, Fragments: 2, NoSocket: 1}
	if st != want {
		t.Fatalf("stats %+v, want %+v", st, want)
	}

	// Socket overflow with a queue of one.
	send(own, 5000, ipOpts{})
	send(own, 5000, ipOpts{})
	waitFor(t, func() bool { return s.Stats().Overflow == 1 })
	readWithin(t, c)
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := c.ReadFrom(make([]byte, 10)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

func TestBroadcastUDP(t *testing.T) {
	n := vnet.New(1)
	s := newStack(t, n, 1, "10.0.0.1")
	b := newStack(t, n, 2, "10.0.0.2")
	none := newStack(t, n, 3, "")
	raw := rawEP(t, n, 9)
	cs, _ := s.ListenUDP(0)
	cb, _ := b.ListenUDP(6000)
	cn, _ := none.ListenUDP(6000)

	// Limited broadcast reaches every stack, with or without address.
	if _, err := cs.WriteTo([]byte("all"), &net.UDPAddr{IP: net.IPv4bcast, Port: 6000}); err != nil {
		t.Fatal(err)
	}
	if d, _ := readWithin(t, cb); string(d) != "all" {
		t.Fatalf("b got %q", d)
	}
	if d, _ := readWithin(t, cn); string(d) != "all" {
		t.Fatalf("unaddressed stack got %q", d)
	}
	f := readRaw(t, raw)
	checkUDP(t, f, ethernet.Broadcast, ip4("10.0.0.1"), ip4("255.255.255.255"), cs.port, 6000, "all")

	// Directed broadcast reaches the subnet only.
	if _, err := cs.WriteTo([]byte("net"), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 255), Port: 6000}); err != nil {
		t.Fatal(err)
	}
	if d, _ := readWithin(t, cb); string(d) != "net" {
		t.Fatalf("b got %q", d)
	}
	checkUDP(t, readRaw(t, raw), ethernet.Broadcast, ip4("10.0.0.1"), ip4("10.0.0.255"), cs.port, 6000, "net")
	_ = cn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := cn.ReadFrom(make([]byte, 10)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("unaddressed stack: %v", err)
	}
	// A stack without address sends a limited broadcast from 0.0.0.0.
	if _, err := cn.WriteTo([]byte("dhcp"), &net.UDPAddr{IP: net.IPv4bcast, Port: 6000}); err != nil {
		t.Fatal(err)
	}
	checkUDP(t, readRaw(t, raw), ethernet.Broadcast, ip4("0.0.0.0"), ip4("255.255.255.255"), cn.port, 6000, "dhcp")
	if d, from := readWithin(t, cb); string(d) != "dhcp" || from.String() != "0.0.0.0:"+strconv.Itoa(int(cn.port)) {
		t.Fatalf("b got %q from %v", d, from)
	}
	// A mask without an address: only limited broadcast is accepted.
	if err := none.SetAddr(netip.Addr{}, mask24, netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	if isBroadcast(ip4("10.0.0.255"), netip.Addr{}, mask24) || !isBroadcast(ip4("255.255.255.255"), netip.Addr{}, netip.Addr{}) {
		t.Fatal("isBroadcast")
	}
}

func TestGatewayRouting(t *testing.T) {
	n := vnet.New(1)
	s, _, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24, Gateway: ip4("10.0.0.254"), ARPTimeout: 10 * time.Second})
	gw := rawEP(t, n, 0xFE)
	c, _ := s.ListenUDP(4000)
	errc := make(chan error, 1)
	go func() {
		_, err := c.WriteTo([]byte("far"), &net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 53})
		errc <- err
	}()
	req := readRaw(t, gw)
	if req.EtherType != ethernet.EtherTypeARP || netip.AddrFrom4([4]byte(req.Payload[24:28])) != ip4("10.0.0.254") {
		t.Fatalf("ARP request % x", req.Payload)
	}
	sendRaw(t, gw, arpFrame(macOf(1), 2, macOf(0xFE), ip4("10.0.0.254"), macOf(1), ip4("10.0.0.1")))
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	checkUDP(t, readRaw(t, gw), macOf(0xFE), ip4("10.0.0.1"), ip4("8.8.8.8"), 4000, 53, "far")

	// A reply from outside the subnet arrives through the gateway; its
	// source is not learned (it is off-link).
	sendRaw(t, gw, udpFrame(macOf(1), macOf(0xFE), ip4("8.8.8.8"), ip4("10.0.0.1"), 53, 4000, []byte("ans"), ipOpts{}))
	if d, from := readWithin(t, c); string(d) != "ans" || from.String() != "8.8.8.8:53" {
		t.Fatalf("got %q from %v", d, from)
	}
	s.mu.RLock()
	_, learned := s.arp[ip4("8.8.8.8")]
	s.mu.RUnlock()
	if learned {
		t.Fatal("off-link source learned")
	}
	// The cached gateway serves another off-link destination at once.
	if _, err := c.WriteTo([]byte("far2"), &net.UDPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 53}); err != nil {
		t.Fatal(err)
	}
	checkUDP(t, readRaw(t, gw), macOf(0xFE), ip4("10.0.0.1"), ip4("1.1.1.1"), 4000, 53, "far2")

	// An unspecified gateway is no route.
	if err := s.SetAddr(ip4("10.0.0.1"), mask24, ip4("0.0.0.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 53}); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestListenPacketAddresses(t *testing.T) {
	n := vnet.New(1)
	s := newStack(t, n, 1, "10.0.0.1")
	ctx := context.Background()
	for _, c := range []struct {
		network, address string
		port             int
	}{
		{"udp", "0.0.0.0:5000", 5000},
		{"udp4", "10.0.0.1:5001", 5001},
		{"udp", "[::]:5002", 5002},
		{"udp", "localhost:5003", 5003},
		{"udp", ":5004", 5004},
		{"udp", ":0", ephemeralLo},
		{"udp4", "station:0", ephemeralLo + 1},
	} {
		pc, err := s.ListenPacket(ctx, c.network, c.address)
		if err != nil {
			t.Fatalf("%s %q: %v", c.network, c.address, err)
		}
		if got := pc.LocalAddr().(*net.UDPAddr).Port; got != c.port {
			t.Fatalf("%q: port %d, want %d", c.address, got, c.port)
		}
	}
	for _, addr := range []string{"nocolon", "host:99999", "host:abc", ":-1", "1.2.3.4:5:6"} {
		if _, err := s.ListenPacket(ctx, "udp", addr); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%q: err %v", addr, err)
		}
	}
	if _, err := s.ListenPacket(ctx, "udp", "0.0.0.0:5000"); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("port in use: %v", err)
	}
	for _, network := range []string{"tcp", "udp6", "ip4", ""} {
		if _, err := s.ListenPacket(ctx, network, ":1"); !errors.Is(err, profinet.ErrUnsupported) {
			t.Errorf("%q: err %v", network, err)
		}
	}
}

func TestEphemeralPorts(t *testing.T) {
	n := vnet.New(1)
	s, _, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24, QueueLen: 1})
	// Taking a port in the range explicitly makes ListenUDP(0) skip it.
	if _, err := s.ListenUDP(ephemeralLo); err != nil {
		t.Fatal(err)
	}
	c, err := s.ListenUDP(0)
	if err != nil || c.port != ephemeralLo+1 {
		t.Fatalf("port %v err %v", c, err)
	}
	for p := ephemeralLo + 2; p <= 65535; p++ {
		if _, err := s.ListenUDP(0); err != nil {
			t.Fatalf("port %d: %v", p, err)
		}
	}
	if _, err := s.ListenUDP(0); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Fatalf("err %v", err)
	}
	// Releasing one makes it available again, after the wrap-around.
	_ = c.Close()
	c2, err := s.ListenUDP(0)
	if err != nil || c2.port != ephemeralLo+1 {
		t.Fatalf("port %v err %v", c2, err)
	}
	// Closing a socket twice, or one whose port was reused, keeps the new one.
	_ = c.Close()
	s.mu.RLock()
	kept := s.socks[ephemeralLo+1] == c2
	s.mu.RUnlock()
	if !kept {
		t.Fatal("stale Close removed the new socket")
	}
}

func TestWriteErrors(t *testing.T) {
	n := vnet.New(1)
	s, _, ep := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24})
	c, _ := s.ListenUDP(0)
	if _, err := c.WriteTo([]byte{1}, &net.IPAddr{IP: net.IPv4(10, 0, 0, 2)}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("IPAddr: %v", err)
	}
	if _, err := c.WriteTo([]byte{1}, &net.UDPAddr{Port: 1}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("nil IP: %v", err)
	}
	bc := &net.UDPAddr{IP: net.IPv4bcast, Port: 1}
	if _, err := c.WriteTo(make([]byte, 1473), bc); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("over MTU: %v", err)
	}
	if nw, err := c.WriteTo(make([]byte, 1472), bc); err != nil || nw != 1472 {
		t.Fatalf("at MTU: %d %v", nw, err)
	}
	ep.SetLinkUp(false)
	if _, err := c.WriteTo([]byte{1}, bc); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("link down: %v", err)
	}
	// ARP cannot be sent either.
	if _, err := c.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1}); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("link down ARP: %v", err)
	}
	ep.SetLinkUp(true)
	if st := s.Stats(); st.SentIP != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestWriteDeadline(t *testing.T) {
	n := vnet.New(1)
	s, _, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24, ARPTimeout: time.Minute})
	c, _ := s.ListenUDP(0)
	dst := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1}
	// A deadline that expires while ARP waits.
	if err := c.SetWriteDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteTo([]byte{1}, dst); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	// A deadline in the past fails at once.
	if err := c.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteTo([]byte{1}, dst); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	// SetDeadline set the read deadline too.
	if _, _, err := c.ReadFrom(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read err %v", err)
	}
	// Clearing it lets broadcasts through.
	_ = c.SetDeadline(time.Time{})
	if _, err := c.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4bcast, Port: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestStackClose(t *testing.T) {
	n := vnet.New(1)
	s, mux, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24, ARPTimeout: time.Minute})
	c1, _ := s.ListenUDP(1)
	c2, _ := s.ListenUDP(2)
	readErr := make(chan error, 1)
	go func() {
		_, _, err := c1.ReadFrom(make([]byte, 10))
		readErr <- err
	}()
	writeErr := make(chan error, 1)
	go func() {
		_, err := c2.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1})
		writeErr <- err
	}()
	// Wait until the writer is waiting for ARP.
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.waiters[ip4("10.0.0.2")]) == 1
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	for name, ch := range map[string]chan error{"read": readErr, "write": writeErr} {
		select {
		case err := <-ch:
			want := net.ErrClosed
			if name == "write" {
				want = profinet.ErrClosed
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s err %v", name, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s not unblocked by Close", name)
		}
	}
	if _, err := s.ListenUDP(3); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("listen err %v", err)
	}
	if _, err := c2.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4bcast, Port: 1}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
	// The stack's subscription is gone; the Mux is still usable.
	if mux.Err() != nil {
		t.Fatal(mux.Err())
	}
}

func TestMuxCloseStopsStack(t *testing.T) {
	n := vnet.New(1)
	s, mux, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24})
	c, _ := s.ListenUDP(1)
	_ = mux.Close()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("stack did not stop")
	}
	if _, err := c.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
}

func TestUDPChecksumZeroSentAsOnes(t *testing.T) {
	n := vnet.New(1)
	s := newStack(t, n, 1, "10.0.0.1")
	b := newStack(t, n, 2, "10.0.0.2")
	raw := rawEP(t, n, 9)
	cs, _ := s.ListenUDP(4000)
	cb, _ := b.ListenUDP(6000)
	src, dst := ip4("10.0.0.1"), ip4("255.255.255.255")
	// Find a payload whose computed checksum is 0 (RFC 768: sent as 0xFFFF).
	var data []byte
	for v := 0; v < 1<<16 && data == nil; v++ {
		u := []byte{0x0F, 0xA0, 0x17, 0x70, 0, 10, 0, 0, byte(v >> 8), byte(v)}
		if checksum(u, pseudoSum(src, dst, len(u))) == 0 {
			data = u[8:]
		}
	}
	if data == nil {
		t.Fatal("no payload with a zero checksum")
	}
	if _, err := cs.WriteTo(data, &net.UDPAddr{IP: net.IPv4bcast, Port: 6000}); err != nil {
		t.Fatal(err)
	}
	f := readRaw(t, raw)
	if sum := binary.BigEndian.Uint16(f.Payload[ipHeaderLen+6:]); sum != 0xFFFF {
		t.Fatalf("checksum %#04x", sum)
	}
	if got, _ := readWithin(t, cb); !bytes.Equal(got, data) {
		t.Fatalf("got % x", got)
	}
	if b.Stats().BadChecksum != 0 {
		t.Fatal("0xFFFF checksum rejected")
	}
}

func TestReadDeadlineChangeWhileBlocked(t *testing.T) {
	n := vnet.New(1)
	s := newStack(t, n, 1, "10.0.0.1")
	c, _ := s.ListenUDP(1)
	_ = c.SetReadDeadline(time.Now().Add(time.Hour))
	errc := make(chan error, 1)
	go func() {
		_, _, err := c.ReadFrom(make([]byte, 10))
		errc <- err
	}()
	// Extending the deadline while blocked keeps the read waiting.
	for i := 0; i < 3; i++ {
		time.Sleep(5 * time.Millisecond)
		_ = c.SetReadDeadline(time.Now().Add(time.Hour))
	}
	select {
	case err := <-errc:
		t.Fatalf("read returned early: %v", err)
	default:
	}
	_ = c.Close()
	if err := <-errc; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if _, _, err := c.ReadFrom(make([]byte, 10)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after close: %v", err)
	}
}

func TestShortARPAndSubnetHelpers(t *testing.T) {
	n := vnet.New(1)
	s := newStack(t, n, 1, "10.0.0.1")
	// Frames from the network are padded; a short ARP payload only comes
	// from a port that does not pad.
	s.handleARP(context.Background(), &ethernet.Frame{EtherType: ethernet.EtherTypeARP, Payload: make([]byte, arpLen-1)})
	if st := s.Stats(); st.ARPRequests != 0 || st.ARPReplys != 0 {
		t.Fatalf("stats %+v", st)
	}
	s.handleIP(&ethernet.Frame{EtherType: ethernet.EtherTypeIPv4, Payload: []byte{0x45, 0, 0, 20}})
	if s.Stats().ReceivedIP != 0 {
		t.Fatal("short IP counted")
	}
	if sameSubnet(ip4("10.0.0.1"), ip4("10.0.0.2"), netip.Addr{}) {
		t.Fatal("sameSubnet without mask")
	}
	if !sameSubnet(ip4("10.0.0.1"), ip4("10.0.0.2"), mask24) || sameSubnet(ip4("10.0.1.1"), ip4("10.0.0.2"), mask24) {
		t.Fatal("sameSubnet")
	}
}

// TestARPCacheClearedDuringResolve covers a resolution whose answer was
// removed again (SetAddr clears the cache between the reply and the
// waiter reading it): the writer reports a timeout instead of sending to a
// zero address.
func TestARPCacheClearedDuringResolve(t *testing.T) {
	n := vnet.New(1)
	s, _, _ := newStackCfg(t, n, 1, Config{Addr: ip4("10.0.0.1"), Mask: mask24, ARPTimeout: time.Minute})
	c, _ := s.ListenUDP(0)
	errc := make(chan error, 1)
	go func() {
		_, err := c.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1})
		errc <- err
	}()
	next := ip4("10.0.0.2")
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.waiters[next]) == 1
	})
	s.mu.Lock()
	for _, w := range s.waiters[next] {
		close(w)
	}
	delete(s.waiters, next)
	s.mu.Unlock()
	select {
	case err := <-errc:
		if !errors.Is(err, profinet.ErrTimeout) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("writer not woken")
	}
}
