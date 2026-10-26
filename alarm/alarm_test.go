// SPDX-License-Identifier: MIT

package alarm

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/vnet"
)

func TestPDUTypeStrings(t *testing.T) {
	want := map[PDUType]string{PDUData: "DATA", PDUNack: "NACK", PDUAck: "ACK", PDUErr: "ERR", 9: "PDUType(9)"}
	for p, w := range want {
		if got := p.String(); got != w {
			t.Errorf("%d: %q", p, got)
		}
	}
}

func TestCodec(t *testing.T) {
	p := PDU{FrameID: pnio.FrameIDAlarmHigh, Dst: 0x0102, Src: 0x0304, Type: PDUData, AddFlags: WindowSize | FlagTACK, SendSeq: 0x7FFF, AckSeq: 0xFFFE, VarPart: []byte{1, 2, 3}}
	b := p.AppendPayload([]byte{0xAA})
	want := []byte{0xAA, 0xFC, 0x01, 0x01, 0x02, 0x03, 0x04, 0x11, 0x11, 0x7F, 0xFF, 0xFF, 0xFE, 0x00, 0x03, 1, 2, 3}
	if !bytes.Equal(b, want) {
		t.Fatalf("% x", b)
	}
	g, err := Decode(b[1:])
	if err != nil {
		t.Fatal(err)
	}
	p.Version = Version
	if g.FrameID != p.FrameID || g.Dst != p.Dst || g.Src != p.Src || g.Type != p.Type || g.Version != Version || g.AddFlags != p.AddFlags ||
		g.SendSeq != p.SendSeq || g.AckSeq != p.AckSeq || !bytes.Equal(g.VarPart, p.VarPart) || !g.TACK() {
		t.Fatalf("decoded %+v", g)
	}
	// Trailing padding is not part of the VarPart.
	if g, err := Decode(append(b[1:], 0, 0, 0)); err != nil || len(g.VarPart) != 3 {
		t.Fatalf("padded: %+v %v", g, err)
	}
	ack := PDU{FrameID: pnio.FrameIDAlarmLow, Type: PDUAck, AddFlags: WindowSize}
	g, err = Decode(ack.AppendPayload(nil))
	if err != nil || g.TACK() || len(g.VarPart) != 0 || g.Type != PDUAck {
		t.Fatalf("ack %+v %v", g, err)
	}
	set := func(i int, v byte) []byte {
		c := append([]byte(nil), b[1:]...)
		c[i] = v
		return c
	}
	cases := []struct {
		name string
		b    []byte
		err  error
	}{
		{"short", b[1:14], profinet.ErrMalformed},
		{"FrameID", set(1, 0x02), profinet.ErrMalformed},
		{"version", set(6, 0x21), profinet.ErrUnsupported},
		{"type 0", set(6, 0x10), profinet.ErrMalformed},
		{"type 5", set(6, 0x15), profinet.ErrMalformed},
		{"VarPartLen", set(13, 4), profinet.ErrMalformed},
	}
	for _, c := range cases {
		if _, err := Decode(c.b); !errors.Is(err, c.err) {
			t.Errorf("%s: err %v", c.name, err)
		}
	}
}

func TestAppendEthernet(t *testing.T) {
	to, from := ethernet.MAC{2, 0, 0, 0, 0, 2}, ethernet.MAC{2, 0, 0, 0, 0, 1}
	for _, c := range []struct {
		id   uint16
		prio uint8
	}{{pnio.FrameIDAlarmHigh, 6}, {pnio.FrameIDAlarmLow, 5}} {
		if Priority(c.id) != c.prio {
			t.Fatalf("priority of 0x%04X", c.id)
		}
		p := PDU{FrameID: c.id, Type: PDUAck}
		b := p.AppendEthernet([]byte{9, 9}, to, from)
		if !bytes.Equal(b[:2], []byte{9, 9}) || len(b) != 2+ethernet.MinFrameLen {
			t.Fatalf("frame of %d octets", len(b))
		}
		f, err := ethernet.Decode(b[2:])
		if err != nil {
			t.Fatal(err)
		}
		if f.Dst != to || f.Src != from || !f.Tagged || f.VLAN.Priority != c.prio || f.VLAN.ID != 0 || f.EtherType != ethernet.EtherTypeProfinet {
			t.Fatalf("frame %+v", f)
		}
		g, err := Decode(f.Payload)
		if err != nil || g.FrameID != c.id || g.Type != PDUAck {
			t.Fatalf("pdu %+v %v", g, err)
		}
	}
}

func TestNextSeq(t *testing.T) {
	for in, out := range map[uint16]uint16{InitialSendSeq: 0, 0: 1, 0x7FFE: 0x7FFF, 0x7FFF: 0} {
		if got := nextSeq(in); got != out {
			t.Errorf("nextSeq(0x%04X) = 0x%04X", in, got)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// pduType returns the RTA PDU type of an Ethernet frame, 0 when it is not
// one.
func pduType(frame []byte) PDUType {
	f, err := ethernet.Decode(frame)
	if err != nil {
		return 0
	}
	p, err := Decode(f.Payload)
	if err != nil {
		return 0
	}
	return p.Type
}

// side is one end of an AR's alarm transport.
type side struct {
	ep  *vnet.Endpoint
	mux *l2.Mux
	mgr *Manager
	mac ethernet.MAC

	mu   sync.Mutex
	data [][]byte
	errs []pnio.Status
}

func (s *side) received() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.data...)
}

func newSide(t *testing.T, n *vnet.Network, last byte) *side {
	t.Helper()
	mac := ethernet.MAC{2, 0, 0, 0, 0, last}
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: mac})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(ep)
	t.Cleanup(func() { _ = m.Close() })
	mgr, err := NewManager(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return &side{ep: ep, mux: m, mgr: mgr, mac: mac}
}

func (s *side) open(t *testing.T, cfg EndpointConfig) *Endpoint {
	t.Helper()
	if cfg.OnData == nil {
		cfg.OnData = func(v []byte) {
			s.mu.Lock()
			s.data = append(s.data, v)
			s.mu.Unlock()
		}
	}
	if cfg.OnErr == nil {
		cfg.OnErr = func(st pnio.Status) {
			s.mu.Lock()
			s.errs = append(s.errs, st)
			s.mu.Unlock()
		}
	}
	e, err := s.mgr.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

// pair opens the endpoints of one priority on two sides: refs 1 (a) and
// 2 (b).
func pair(t *testing.T, timeout time.Duration, retries int) (*side, *side, *Endpoint, *Endpoint) {
	t.Helper()
	n := vnet.New(9)
	t.Cleanup(func() { _ = n.Close() })
	a, b := newSide(t, n, 0xA), newSide(t, n, 0xB)
	ea := a.open(t, EndpointConfig{High: true, LocalRef: 1, RemoteRef: 2, RemoteMAC: b.mac, Timeout: timeout, Retries: retries})
	eb := b.open(t, EndpointConfig{High: true, LocalRef: 2, RemoteRef: 1, RemoteMAC: a.mac, Timeout: timeout, Retries: retries})
	return a, b, ea, eb
}

func TestSendAndAck(t *testing.T) {
	_, b, ea, eb := pair(t, time.Second, 3)
	ctx := context.Background()
	for _, m := range []string{"one", "two", "three"} {
		if err := ea.Send(ctx, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "delivery", func() bool { return len(b.received()) == 3 })
	if got := b.received(); string(got[0]) != "one" || string(got[2]) != "three" {
		t.Fatalf("delivered %q", got)
	}
	// And back.
	if err := eb.Send(ctx, []byte("reply")); err != nil {
		t.Fatal(err)
	}
	sa, sb := ea.Stats(), eb.Stats()
	if sa.Sent != 3 || sa.Acks != 3 || sa.Retransmits != 0 || sa.Received != 1 || sb.Received != 3 || sb.Sent != 1 || sb.Acks != 1 {
		t.Fatalf("stats a %+v b %+v", sa, sb)
	}
	if ea.LocalRef() != 1 || eb.LocalRef() != 2 {
		t.Fatal("LocalRef")
	}
	ea.mu.Lock()
	seq, last := ea.sendSeq, ea.lastRecv
	ea.mu.Unlock()
	// Three DATA PDUs were sent as 0xFFFF, 0 and 1; one was received, the
	// peer's first, 0xFFFF.
	if seq != 1 || last != InitialSendSeq {
		t.Fatalf("a: sendSeq %d lastRecv %d", seq, last)
	}
}

func TestDuplicateReAck(t *testing.T) {
	_, b, ea, eb := pair(t, 50*time.Millisecond, 3)
	var dropped atomic.Int32
	// The first ACK of b is lost: a retransmits, b sees a duplicate and
	// acknowledges it again without delivering it twice.
	b.ep.SetFaults(vnet.Faults{Loss: 1, Match: func(f []byte) bool {
		return pduType(f) == PDUAck && dropped.Add(1) == 1
	}})
	if err := ea.Send(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if sa, sb := ea.Stats(), eb.Stats(); sa.Retransmits < 1 || sb.Duplicates < 1 || sb.Received != 1 {
		t.Fatalf("stats a %+v b %+v", sa, sb)
	}
	time.Sleep(20 * time.Millisecond)
	if len(b.received()) != 1 {
		t.Fatalf("delivered %d times", len(b.received()))
	}
}

func TestNackRetransmit(t *testing.T) {
	a, b, ea, eb := pair(t, time.Minute, 3)
	// The first DATA of a is lost; a NACK from b makes a retransmit at
	// once instead of after the (long) timeout.
	var dropped atomic.Int32
	a.ep.SetFaults(vnet.Faults{Loss: 1, Match: func(f []byte) bool {
		return pduType(f) == PDUData && dropped.Add(1) == 1
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- ea.Send(ctx, []byte("x")) }()
	waitFor(t, "lost DATA", func() bool { return a.ep.Stats().Dropped == 1 })
	nack := PDU{FrameID: pnio.FrameIDAlarmHigh, Dst: 1, Src: 2, Type: PDUNack, AddFlags: WindowSize, SendSeq: InitialSendSeq, AckSeq: InitialAckSeq}
	if err := b.mux.Send(ctx, nack.AppendEthernet(nil, a.mac, b.mac)); err != nil {
		t.Fatal(err)
	}
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	if sa := ea.Stats(); sa.Nacks != 1 || sa.Retransmits != 1 || sa.Sent != 2 {
		t.Fatalf("stats %+v", sa)
	}
	if eb.Stats().Received != 1 {
		t.Fatalf("b %+v", eb.Stats())
	}
	// A NACK while nothing is outstanding is only counted.
	if err := b.mux.Send(ctx, nack.AppendEthernet(nil, a.mac, b.mac)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "nack", func() bool { return ea.Stats().Nacks == 2 })
}

func TestOutOfSequence(t *testing.T) {
	a, _, ea, eb := pair(t, time.Second, 3)
	// A DATA with an unexpected sequence number is answered with a NACK
	// carrying the last sequence number received, TACK or not.
	ctx := context.Background()
	data := PDU{FrameID: pnio.FrameIDAlarmHigh, Dst: 2, Src: 1, Type: PDUData, AddFlags: WindowSize, SendSeq: 5, AckSeq: InitialAckSeq, VarPart: []byte("x")}
	if err := a.mux.Send(ctx, data.AppendEthernet(nil, eb.mgr.mux.HardwareAddr(), a.mac)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "nack", func() bool { return ea.Stats().Nacks == 1 })
	if sb := eb.Stats(); sb.OutOfSequence != 1 || sb.Received != 0 {
		t.Fatalf("b %+v", sb)
	}
	// The real sequence still works.
	if err := ea.Send(ctx, []byte("y")); err != nil {
		t.Fatal(err)
	}
	// A duplicate (of the first DATA, 0xFFFF) without TACK is not
	// acknowledged.
	data.SendSeq = InitialSendSeq
	acks := ea.Stats().Acks
	if err := a.mux.Send(ctx, data.AppendEthernet(nil, eb.mgr.mux.HardwareAddr(), a.mac)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "duplicate", func() bool { return eb.Stats().Duplicates == 1 })
	time.Sleep(20 * time.Millisecond)
	if ea.Stats().Acks != acks {
		t.Fatal("duplicate without TACK acknowledged")
	}
}

func TestPiggyBackedAck(t *testing.T) {
	_, b, ea, eb := pair(t, time.Minute, 3)
	// b loses every ACK it sends: a's DATA is acknowledged by the AckSeq
	// of b's own DATA.
	b.ep.SetFaults(vnet.Faults{Loss: 1, Match: func(f []byte) bool { return pduType(f) == PDUAck }})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- ea.Send(ctx, []byte("x")) }()
	waitFor(t, "delivery", func() bool { return eb.Stats().Received == 1 })
	if err := eb.Send(ctx, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	if sa := ea.Stats(); sa.Acks != 0 || sa.Retransmits != 0 || sa.Received != 1 {
		t.Fatalf("a %+v", sa)
	}
}

func TestSendTimeoutCancelClose(t *testing.T) {
	n := vnet.New(10)
	defer func() { _ = n.Close() }()
	a := newSide(t, n, 1)
	// The peer is absent.
	e := a.open(t, EndpointConfig{LocalRef: 1, RemoteRef: 2, RemoteMAC: ethernet.MAC{2, 0, 0, 0, 0, 0x99}, Timeout: 10 * time.Millisecond, Retries: 2})
	err := e.Send(context.Background(), []byte("x"))
	if !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	if s := e.Stats(); s.Sent != 3 || s.Retransmits != 2 {
		t.Fatalf("stats %+v", s)
	}
	// Cancellation.
	e2 := a.open(t, EndpointConfig{LocalRef: 2, RemoteRef: 2, RemoteMAC: ethernet.MAC{2, 0, 0, 0, 0, 0x99}, Timeout: time.Minute})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := e2.Send(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	// Close during Send, and Send after Close.
	res := make(chan error, 1)
	go func() { res <- e2.Send(context.Background(), nil) }()
	waitFor(t, "send", func() bool { return e2.Stats().Sent == 2 })
	e2.Close()
	if err := <-res; !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if err := e2.Send(context.Background(), nil); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	e2.Close()
	// The reference is free again.
	e3, err := a.mgr.Open(EndpointConfig{LocalRef: 2})
	if err != nil {
		t.Fatal(err)
	}
	e3.Close()
	// A send error is returned.
	a.ep.SetLinkUp(false)
	if err := e.Send(context.Background(), nil); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("err %v", err)
	}
	if err := e.SendErr(context.Background(), pnio.Status{}); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("err %v", err)
	}
}

func TestDefaults(t *testing.T) {
	n := vnet.New(11)
	defer func() { _ = n.Close() }()
	a := newSide(t, n, 1)
	e := a.open(t, EndpointConfig{LocalRef: 7})
	if e.cfg.Timeout != 100*time.Millisecond || e.cfg.Retries != 3 || e.frameID != pnio.FrameIDAlarmLow {
		t.Fatalf("config %+v", e.cfg)
	}
}

func TestSendErr(t *testing.T) {
	_, b, ea, _ := pair(t, time.Second, 3)
	st := pnio.Status{Code: 0xCF, Decode: 0x81, Code1: 0xFD, Code2: 0x05}
	if err := ea.SendErr(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "OnErr", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.errs) == 1 })
	b.mu.Lock()
	got := b.errs[0]
	b.mu.Unlock()
	if got != st {
		t.Fatalf("status %v", got)
	}
	// An ERR without a status reports the zero status.
	p := PDU{FrameID: pnio.FrameIDAlarmHigh, Dst: 2, Src: 1, Type: PDUErr, VarPart: []byte{1}}
	ma := ea.mgr.mux
	if err := ma.Send(context.Background(), p.AppendEthernet(nil, b.mac, ma.HardwareAddr())); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "OnErr", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.errs) == 2 })
	b.mu.Lock()
	got = b.errs[1]
	b.mu.Unlock()
	if !got.OK() {
		t.Fatalf("status %v", got)
	}
}

func TestNoCallbacks(t *testing.T) {
	n := vnet.New(12)
	defer func() { _ = n.Close() }()
	a, b := newSide(t, n, 1), newSide(t, n, 2)
	ea, err := a.mgr.Open(EndpointConfig{LocalRef: 1, RemoteRef: 2, RemoteMAC: b.mac})
	if err != nil {
		t.Fatal(err)
	}
	defer ea.Close()
	eb, err := b.mgr.Open(EndpointConfig{LocalRef: 2, RemoteRef: 1, RemoteMAC: a.mac})
	if err != nil {
		t.Fatal(err)
	}
	defer eb.Close()
	if err := ea.Send(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := ea.SendErr(context.Background(), pnio.Status{Code: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ea.Send(context.Background(), []byte("y")); err != nil {
		t.Fatal(err)
	}
	if eb.Stats().Received != 2 {
		t.Fatalf("stats %+v", eb.Stats())
	}
}

// TestFirstSequence: the first DATA PDU of an endpoint carries 0xFFFF, the
// next 0, as p-net, pnio-rs and profinet-py number them, and the first
// DATA a receiver accepts is 0xFFFF.
func TestFirstSequence(t *testing.T) {
	a, _, ea, eb := pair(t, time.Second, 3)
	var seqs []uint16
	var mu sync.Mutex
	// Match is consulted only while a fault is set; it records and
	// applies none.
	a.ep.SetFaults(vnet.Faults{Delay: time.Nanosecond, Match: func(f []byte) bool {
		if fr, err := ethernet.Decode(f); err == nil {
			if p, err := Decode(fr.Payload); err == nil && p.Type == PDUData {
				mu.Lock()
				seqs = append(seqs, p.SendSeq)
				mu.Unlock()
			}
		}
		return false
	}})
	for i := 0; i < 2; i++ {
		if err := ea.Send(context.Background(), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != 2 || seqs[0] != 0xFFFF || seqs[1] != 0 {
		t.Fatalf("sequence numbers %v", seqs)
	}
	if eb.Stats().Received != 2 || eb.Stats().OutOfSequence != 0 {
		t.Fatalf("b %+v", eb.Stats())
	}
}

func TestSequenceWrap(t *testing.T) {
	_, b, ea, eb := pair(t, time.Second, 3)
	ea.mu.Lock()
	ea.nextSend, ea.sendSeq = 0x7FFE, 0x7FFD
	ea.mu.Unlock()
	eb.mu.Lock()
	eb.expect, eb.lastRecv = 0x7FFE, 0x7FFD
	eb.mu.Unlock()
	for i := 0; i < 4; i++ {
		if err := ea.Send(context.Background(), []byte{byte(i)}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	waitFor(t, "delivery", func() bool { return len(b.received()) == 4 })
	eb.mu.Lock()
	last := eb.lastRecv
	eb.mu.Unlock()
	if last != 1 || eb.Stats().OutOfSequence != 0 {
		t.Fatalf("lastRecv 0x%04X stats %+v", last, eb.Stats())
	}
}

func TestManagerRefsAndRouting(t *testing.T) {
	n := vnet.New(13)
	defer func() { _ = n.Close() }()
	a, b := newSide(t, n, 1), newSide(t, n, 2)
	m := a.mgr
	if r := m.AllocRef(); r != 1 {
		t.Fatalf("first ref %d", r)
	}
	e := a.open(t, EndpointConfig{LocalRef: 2, RemoteRef: 1, RemoteMAC: b.mac})
	a.open(t, EndpointConfig{LocalRef: 3, High: true})
	// 2 and 3 are in use, at either priority.
	if r := m.AllocRef(); r != 4 {
		t.Fatalf("ref %d", r)
	}
	if _, err := m.Open(EndpointConfig{LocalRef: 2}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("duplicate: %v", err)
	}
	// The other priority with the same reference is distinct.
	a.open(t, EndpointConfig{LocalRef: 2, High: true})
	// References wrap from 0xFFFE to 1, skipping those in use.
	m.mu.Lock()
	m.nextRef = 0xFFFE
	m.mu.Unlock()
	if r := m.AllocRef(); r != 0xFFFE {
		t.Fatalf("ref 0x%04X", r)
	}
	if r := m.AllocRef(); r != 1 {
		t.Fatalf("after wrap: ref %d", r)
	}
	if r := m.AllocRef(); r != 4 {
		t.Fatalf("after wrap: ref %d", r)
	}
	// Routing: unknown reference, wrong priority, wrong source MAC, and
	// garbage.
	ctx := context.Background()
	send := func(from *side, p PDU) {
		t.Helper()
		if err := from.mux.Send(ctx, p.AppendEthernet(nil, a.mac, from.mac)); err != nil {
			t.Fatal(err)
		}
	}
	ack := PDU{FrameID: pnio.FrameIDAlarmLow, Dst: 9, Type: PDUAck}
	send(b, ack)
	ack.Dst, ack.FrameID = 3, pnio.FrameIDAlarmLow // 3 is high priority only
	send(b, ack)
	c := newSide(t, n, 3)
	ack.Dst = 2
	send(c, ack)
	waitFor(t, "unknown", func() bool { return m.Unknown() == 3 })
	raw := PDU{FrameID: pnio.FrameIDAlarmLow, Type: PDUAck}
	frame := raw.AppendEthernet(nil, a.mac, b.mac)
	frame[18+6] = 0x51 // RTA version 5
	if err := b.mux.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "malformed", func() bool { return m.Malformed() == 1 })
	// Frames that are not alarms do not reach the manager.
	for _, other := range []ethernet.Frame{
		{Dst: a.mac, Src: b.mac, EtherType: ethernet.EtherTypeLLDP, Payload: []byte{0xFC, 0x01}},
		{Dst: a.mac, Src: b.mac, EtherType: ethernet.EtherTypeProfinet, Payload: []byte{0x80, 0x00}},
		{Dst: a.mac, Src: b.mac, EtherType: ethernet.EtherTypeProfinet},
	} {
		if err := b.mux.Send(ctx, other.Encode()); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "unclaimed", func() bool { return a.mux.Stats().Unclaimed >= 3 })
	if m.Malformed() != 1 || m.Unknown() != 3 {
		t.Fatalf("malformed %d unknown %d", m.Malformed(), m.Unknown())
	}
	// The right source reaches the endpoint.
	send(b, ack)
	waitFor(t, "ack", func() bool { return e.Stats().Acks == 1 })
	// After Close the manager routes nothing; Close twice is fine.
	m.Close()
	m.Close()
	send(b, ack)
	time.Sleep(20 * time.Millisecond)
	if e.Stats().Acks != 1 {
		t.Fatal("routed after Close")
	}
}

func TestNewManagerClosedMux(t *testing.T) {
	n := vnet.New(14)
	defer func() { _ = n.Close() }()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{2, 0, 0, 0, 0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(ep)
	_ = m.Close()
	if _, err := NewManager(m); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, p := range []PDU{
		{FrameID: pnio.FrameIDAlarmHigh, Dst: 1, Src: 2, Type: PDUData, AddFlags: WindowSize | FlagTACK, VarPart: []byte{1, 2, 3}},
		{FrameID: pnio.FrameIDAlarmLow, Type: PDUAck, SendSeq: InitialSendSeq, AckSeq: InitialAckSeq},
		{FrameID: pnio.FrameIDAlarmLow, Type: PDUErr, VarPart: []byte{0xCF, 0x81, 0xFD, 0x05}},
	} {
		f.Add(p.AppendPayload(nil))
	}
	f.Add([]byte{0xFC, 0x01})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := Decode(b)
		if err != nil {
			return
		}
		out := p.AppendPayload(nil)
		n := 14 + len(p.VarPart)
		if !bytes.Equal(out, b[:n]) {
			t.Fatalf("re-encode % x != % x", out, b[:n])
		}
		if binary.BigEndian.Uint16(out[12:]) != uint16(len(p.VarPart)) {
			t.Fatal("VarPartLen")
		}
	})
}

func BenchmarkAppendPayload(b *testing.B) {
	p := PDU{FrameID: pnio.FrameIDAlarmHigh, Dst: 1, Src: 2, Type: PDUData, AddFlags: WindowSize | FlagTACK, VarPart: make([]byte, 64)}
	buf := make([]byte, 0, 128)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf = p.AppendPayload(buf[:0])
	}
}

func BenchmarkDecode(b *testing.B) {
	p := PDU{FrameID: pnio.FrameIDAlarmHigh, Dst: 1, Src: 2, Type: PDUData, AddFlags: WindowSize | FlagTACK, VarPart: make([]byte, 64)}
	buf := p.AppendPayload(nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Decode(buf); err != nil {
			b.Fatal(err)
		}
	}
}

// TestSlowApplication: while OnData blocks, DATA PDUs beyond the delivery
// queue are left unacknowledged (Busy) and retransmitted by the sender, so
// the manager never blocks and nothing is lost.
func TestSlowApplication(t *testing.T) {
	n := vnet.New(11)
	t.Cleanup(func() { _ = n.Close() })
	a, b := newSide(t, n, 0xA), newSide(t, n, 0xB)
	release := make(chan struct{})
	var got atomic.Int32
	ea := a.open(t, EndpointConfig{LocalRef: 1, RemoteRef: 2, RemoteMAC: b.mac, Timeout: 20 * time.Millisecond, Retries: 15})
	eb := b.open(t, EndpointConfig{LocalRef: 2, RemoteRef: 1, RemoteMAC: a.mac, OnData: func([]byte) {
		<-release
		got.Add(1)
	}})
	const total = 40
	done := make(chan error, 1)
	go func() {
		for i := 0; i < total; i++ {
			if err := ea.Send(context.Background(), []byte{byte(i)}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	waitFor(t, "a full queue", func() bool { return eb.Stats().Busy > 0 })
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all delivered", func() bool { return got.Load() == total })
}

func TestAllocRefExhausted(t *testing.T) {
	n := vnet.New(12)
	t.Cleanup(func() { _ = n.Close() })
	s := newSide(t, n, 0xA)
	m := s.mgr
	m.mu.Lock()
	for r := uint16(1); r < 0xFFFF; r++ {
		m.endpoints[epKey{r, false}] = &Endpoint{}
	}
	m.mu.Unlock()
	if r := m.AllocRef(); r != 0 {
		t.Fatalf("ref %d", r)
	}
	if _, err := m.Open(EndpointConfig{LocalRef: 0}); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Fatalf("err %v", err)
	}
	m.mu.Lock()
	m.endpoints = map[epKey]*Endpoint{}
	m.mu.Unlock()
}
