// SPDX-License-Identifier: MIT

package netstack

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
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
		s.Close()
		mux.Close()
		ep.Close()
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
	cb.SetReadDeadline(time.Now().Add(2 * time.Second))
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
	ca.SetReadDeadline(time.Now().Add(2 * time.Second))
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
	c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := c.ReadFrom(make([]byte, 10)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	c.SetReadDeadline(time.Time{})
	done := make(chan error)
	go func() {
		_, _, err := c.ReadFrom(make([]byte, 10))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	c.SetReadDeadline(time.Now())
	if err := <-done; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	go func() {
		_, _, err := c.ReadFrom(make([]byte, 10))
		done <- err
	}()
	c.SetReadDeadline(time.Time{})
	c.Close()
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
	cu.SetReadDeadline(time.Now().Add(time.Second))
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
