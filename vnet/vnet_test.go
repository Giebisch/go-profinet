// SPDX-License-Identifier: MIT

package vnet

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

const etTest ethernet.EtherType = 0x88B5 // IEEE local experimental

func mac(n byte) ethernet.MAC { return ethernet.MAC{2, 0, 0, 0, 0, n} }

func newEP(t testing.TB, n *Network, cfg EndpointConfig) *Endpoint {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = cfg.MAC.String()
	}
	e, err := n.NewEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// frame builds a test frame whose payload starts with the sequence number.
func frame(dst, src ethernet.MAC, seq uint32) []byte {
	p := binary.BigEndian.AppendUint32(nil, seq)
	return (&ethernet.Frame{Dst: dst, Src: src, EtherType: etTest, Payload: p}).Encode()
}

func tagged(dst, src ethernet.MAC, vid uint16, seq uint32) []byte {
	p := binary.BigEndian.AppendUint32(nil, seq)
	return (&ethernet.Frame{Dst: dst, Src: src, Tagged: true, VLAN: ethernet.VLANTag{Priority: 6, ID: vid}, EtherType: etTest, Payload: p}).Encode()
}

func seqOf(t testing.TB, b []byte) uint32 {
	t.Helper()
	f, err := ethernet.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return binary.BigEndian.Uint32(f.Payload)
}

func write(t testing.TB, e *Endpoint, b []byte) {
	t.Helper()
	if err := e.WriteFrame(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

// drain returns the frames queued at e without blocking. Delivery of
// frames without delay is synchronous, so after WriteFrame returns the
// frame is already queued.
func drain(e *Endpoint) [][]byte {
	var out [][]byte
	for {
		select {
		case f := <-e.rx:
			out = append(out, f)
		default:
			return out
		}
	}
}

// read waits generously for one frame.
func read(t testing.TB, e *Endpoint) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	buf := make([]byte, 2000)
	n, err := e.ReadFrame(ctx, buf)
	if err != nil {
		t.Fatalf("%s: read: %v", e.Name(), err)
	}
	return buf[:n]
}

func TestNetworkBasics(t *testing.T) {
	n := New(42)
	if n.Seed() != 42 {
		t.Fatal("seed")
	}
	a := newEP(t, n, EndpointConfig{Name: "a", MAC: mac(1)})
	if a.Name() != "a" || a.HardwareAddr() != mac(1) || a.MTU() != ethernet.DefaultMTU || cap(a.rx) != DefaultQueueLen || !a.LinkUp() {
		t.Fatalf("defaults: %q %v %d %d", a.Name(), a.HardwareAddr(), a.MTU(), cap(a.rx))
	}
	b := newEP(t, n, EndpointConfig{Name: "b", MAC: mac(2), MTU: 9000, QueueLen: 3})
	if b.MTU() != 9000 || cap(b.rx) != 3 {
		t.Fatal("config not applied")
	}
	if eps := n.Endpoints(); len(eps) != 2 || eps[0] != a || eps[1] != b {
		t.Fatalf("endpoints %v", eps)
	}
}

func TestEndpointConfigErrors(t *testing.T) {
	n := New(1)
	newEP(t, n, EndpointConfig{MAC: mac(1)})
	for _, cfg := range []EndpointConfig{
		{Name: "zero"},
		{Name: "multicast", MAC: ethernet.DCPIdentifyMulticast},
		{Name: "broadcast", MAC: ethernet.Broadcast},
		{Name: "mtu-small", MAC: mac(9), MTU: 45},
		{Name: "mtu-big", MAC: mac(9), MTU: 9001},
		{Name: "mtu-negative", MAC: mac(9), MTU: -1},
		{Name: "duplicate", MAC: mac(1)},
	} {
		if _, err := n.NewEndpoint(cfg); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%s: err %v", cfg.Name, err)
		}
	}
	if _, err := n.NewEndpoint(EndpointConfig{MAC: mac(9), MTU: 46}); err != nil {
		t.Fatalf("MTU 46: %v", err)
	}
	if len(n.Endpoints()) != 2 {
		t.Fatal("failed endpoints were attached")
	}
}

func TestUnicast(t *testing.T) {
	n := New(1)
	a := newEP(t, n, EndpointConfig{MAC: mac(1)})
	b := newEP(t, n, EndpointConfig{MAC: mac(2)})
	c := newEP(t, n, EndpointConfig{MAC: mac(3)})
	write(t, a, frame(mac(2), mac(1), 7))
	got := drain(b)
	if len(got) != 1 || seqOf(t, got[0]) != 7 {
		t.Fatalf("b got %d frames", len(got))
	}
	if len(got[0]) != ethernet.MinFrameLen {
		t.Fatalf("not padded: %d", len(got[0]))
	}
	if len(drain(c)) != 0 || len(drain(a)) != 0 {
		t.Fatal("unicast leaked")
	}
	// Unknown destination and frames to oneself go nowhere.
	write(t, a, frame(mac(99), mac(1), 1))
	write(t, a, frame(mac(1), mac(1), 1))
	for _, e := range []*Endpoint{a, b, c} {
		if f := drain(e); len(f) != 0 {
			t.Fatalf("%s got %d frames", e.Name(), len(f))
		}
	}
	if st := a.Stats(); st.Sent != 3 || st.Received != 0 {
		t.Fatalf("a stats %+v", st)
	}
	if st := b.Stats(); st.Received != 1 {
		t.Fatalf("b stats %+v", st)
	}
}

func TestReadFrameCopiesAndTruncates(t *testing.T) {
	n := New(1)
	a := newEP(t, n, EndpointConfig{MAC: mac(1)})
	b := newEP(t, n, EndpointConfig{MAC: mac(2)})
	src := frame(mac(2), mac(1), 0xAABBCCDD)
	write(t, a, src)
	src[14] = 0 // the network copied the frame
	buf := make([]byte, 16)
	nr, err := b.ReadFrame(context.Background(), buf)
	if err != nil || nr != 16 {
		t.Fatalf("n %d err %v", nr, err)
	}
	if !bytes.Equal(buf[14:16], []byte{0xAA, 0xBB}) {
		t.Fatalf("payload % x", buf)
	}
}

func TestBroadcastAndMulticast(t *testing.T) {
	n := New(1)
	a := newEP(t, n, EndpointConfig{MAC: mac(1)})
	b := newEP(t, n, EndpointConfig{MAC: mac(2)})
	f := newEP(t, n, EndpointConfig{MAC: mac(3), FilterMulticast: true})

	write(t, a, frame(ethernet.Broadcast, mac(1), 1))
	if len(drain(a)) != 0 || len(drain(b)) != 1 || len(drain(f)) != 1 {
		t.Fatal("broadcast not delivered to every other endpoint")
	}

	group := ethernet.DCPIdentifyMulticast
	write(t, a, frame(group, mac(1), 2))
	if len(drain(b)) != 1 {
		t.Fatal("unfiltered endpoint missed multicast")
	}
	if len(drain(f)) != 0 {
		t.Fatal("filtering endpoint got an unjoined group")
	}
	if err := f.JoinMulticast(group); err != nil {
		t.Fatal(err)
	}
	write(t, a, frame(group, mac(1), 3))
	write(t, a, frame(ethernet.DCPHelloMulticast, mac(1), 4))
	got := drain(f)
	if len(got) != 1 || seqOf(t, got[0]) != 3 {
		t.Fatalf("joined endpoint got %d frames", len(got))
	}
	if err := f.LeaveMulticast(group); err != nil {
		t.Fatal(err)
	}
	write(t, a, frame(group, mac(1), 5))
	if len(drain(f)) != 0 {
		t.Fatal("group still received after leave")
	}
	if err := f.LeaveMulticast(group); err != nil {
		t.Fatal("leaving twice:", err)
	}
	if err := f.JoinMulticast(mac(9)); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("join unicast: %v", err)
	}
}

func TestPromiscuous(t *testing.T) {
	n := New(1)
	a := newEP(t, n, EndpointConfig{MAC: mac(1)})
	b := newEP(t, n, EndpointConfig{MAC: mac(2)})
	p := newEP(t, n, EndpointConfig{MAC: mac(3), Promiscuous: true})
	write(t, a, frame(mac(2), mac(1), 1))
	write(t, a, frame(mac(77), mac(1), 2))
	write(t, a, frame(mac(3), mac(1), 3)) // to p itself: delivered once
	got := drain(p)
	if len(got) != 3 || seqOf(t, got[0]) != 1 || seqOf(t, got[1]) != 2 || seqOf(t, got[2]) != 3 {
		t.Fatalf("promiscuous got %d frames", len(got))
	}
	if len(drain(b)) != 1 {
		t.Fatal("b missed its frame")
	}
	// A promiscuous endpoint does not see its own frames.
	write(t, p, frame(mac(1), mac(3), 4))
	if len(drain(p)) != 0 || len(drain(a)) != 1 {
		t.Fatal("promiscuous own frame")
	}
}

func TestVLANMembership(t *testing.T) {
	n := New(1)
	a := newEP(t, n, EndpointConfig{MAC: mac(1)})
	v10 := newEP(t, n, EndpointConfig{MAC: mac(2), VLANs: []uint16{10, 11}})
	all := newEP(t, n, EndpointConfig{MAC: mac(3)})
	v20 := newEP(t, n, EndpointConfig{MAC: mac(4), VLANs: []uint16{20}})
	none := newEP(t, n, EndpointConfig{MAC: mac(5), VLANs: []uint16{}, Promiscuous: true})

	count := func() [4]int {
		return [4]int{len(drain(v10)), len(drain(all)), len(drain(v20)), len(drain(none))}
	}
	write(t, a, tagged(ethernet.Broadcast, mac(1), 10, 1))
	if c := count(); c != [4]int{1, 1, 0, 0} {
		t.Fatalf("VID 10 broadcast: %v", c)
	}
	write(t, a, tagged(ethernet.Broadcast, mac(1), 0, 2))
	if c := count(); c != [4]int{1, 1, 1, 1} {
		t.Fatalf("priority-tagged broadcast: %v", c)
	}
	write(t, a, frame(ethernet.Broadcast, mac(1), 3))
	if c := count(); c != [4]int{1, 1, 1, 1} {
		t.Fatalf("untagged broadcast: %v", c)
	}
	write(t, a, tagged(mac(2), mac(1), 20, 4))
	if c := count(); c != [4]int{0, 0, 0, 0} {
		t.Fatalf("unicast to non-member: %v", c)
	}
	write(t, a, tagged(mac(4), mac(1), 20, 5))
	if c := count(); c != [4]int{0, 0, 1, 0} {
		t.Fatalf("unicast to member: %v", c)
	}
	write(t, a, tagged(mac(2), mac(1), 0, 6))
	if c := count(); c != [4]int{1, 0, 0, 1} {
		t.Fatalf("priority-tagged unicast: %v", c)
	}
	if vid, ok := vlanOf(tagged(mac(1), mac(2), 4095, 0)); !ok || vid != 4095 {
		t.Fatalf("vlanOf %d %v", vid, ok)
	}
	if _, ok := vlanOf(append(make([]byte, 12), 0x81, 0x00, 0)); ok {
		t.Fatal("truncated tag accepted")
	}
}

func TestMTUAndShortFrames(t *testing.T) {
	n := New(1)
	a := newEP(t, n, EndpointConfig{MAC: mac(1), MTU: 100})
	b := newEP(t, n, EndpointConfig{MAC: mac(2)})
	ok := (&ethernet.Frame{Dst: mac(2), Src: mac(1), EtherType: etTest, Payload: make([]byte, 100)}).Encode()
	write(t, a, ok)
	okTagged := (&ethernet.Frame{Dst: mac(2), Src: mac(1), Tagged: true, EtherType: etTest, Payload: make([]byte, 100)}).Encode()
	write(t, a, okTagged)
	if len(drain(b)) != 2 {
		t.Fatal("frames at the MTU not delivered")
	}
	for _, tg := range []bool{false, true} {
		big := (&ethernet.Frame{Dst: mac(2), Src: mac(1), Tagged: tg, EtherType: etTest, Payload: make([]byte, 101)}).Encode()
		if err := a.WriteFrame(context.Background(), big); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Fatalf("tagged=%v: err %v", tg, err)
		}
	}
	if err := a.WriteFrame(context.Background(), make([]byte, 13)); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("short frame: %v", err)
	}
	if st := a.Stats(); st.Sent != 2 {
		t.Fatalf("sent %d", st.Sent)
	}
	// A frame of exactly the header is padded and delivered.
	hdr := (&ethernet.Frame{Dst: mac(2), Src: mac(1), EtherType: etTest}).AppendHeader(nil)
	write(t, a, hdr)
	if got := drain(b); len(got) != 1 || len(got[0]) != ethernet.MinFrameLen {
		t.Fatal("header-only frame")
	}
	// deliver ignores frames shorter than a header.
	n.deliver(a, make([]byte, 5))
}
