// SPDX-License-Identifier: MIT

package l2

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/vnet"
)

const etTest ethernet.EtherType = 0x88B5

func mac(n byte) ethernet.MAC { return ethernet.MAC{2, 0, 0, 0, 0, n} }

// fakePort is a Port whose ReadFrame returns what the test feeds it.
type fakePort struct {
	in     chan []byte
	errc   chan error
	closed chan struct{}
	once   sync.Once
}

func newFakePort() *fakePort {
	return &fakePort{in: make(chan []byte, 16), errc: make(chan error, 1), closed: make(chan struct{})}
}

func (p *fakePort) ReadFrame(ctx context.Context, b []byte) (int, error) {
	select {
	case f := <-p.in:
		return copy(b, f), nil
	case err := <-p.errc:
		return 0, err
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-p.closed:
		return 0, profinet.ErrClosed
	}
}

func (p *fakePort) WriteFrame(context.Context, []byte) error { return errors.New("fake: write") }
func (p *fakePort) HardwareAddr() ethernet.MAC               { return mac(0xFA) }
func (p *fakePort) MTU() int                                 { return 1500 }
func (p *fakePort) Close() error                             { p.once.Do(func() { close(p.closed) }); return nil }

type setup struct {
	net    *vnet.Network
	tx, rx *vnet.Endpoint
	mux    *Mux
}

func newSetup(t testing.TB) *setup {
	t.Helper()
	n := vnet.New(1)
	tx, err := n.NewEndpoint(vnet.EndpointConfig{Name: "tx", MAC: mac(1)})
	if err != nil {
		t.Fatal(err)
	}
	rx, err := n.NewEndpoint(vnet.EndpointConfig{Name: "rx", MAC: mac(2), FilterMulticast: true})
	if err != nil {
		t.Fatal(err)
	}
	m := NewMux(rx)
	t.Cleanup(func() {
		m.Close()
		n.Close()
	})
	return &setup{net: n, tx: tx, rx: rx, mux: m}
}

func (s *setup) send(t testing.TB, et ethernet.EtherType, payload ...byte) {
	t.Helper()
	f := (&ethernet.Frame{Dst: mac(2), Src: mac(1), EtherType: et, Payload: payload}).Encode()
	if err := s.tx.WriteFrame(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func recv(t testing.TB, s *Subscription) ethernet.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := s.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func pnFrame(id uint16) *ethernet.Frame {
	return &ethernet.Frame{EtherType: ethernet.EtherTypeProfinet, Payload: binary.BigEndian.AppendUint16(nil, id)}
}

func TestMatchers(t *testing.T) {
	m := MatchEtherType(ethernet.EtherTypeLLDP)
	if !m(&ethernet.Frame{EtherType: ethernet.EtherTypeLLDP}) || m(&ethernet.Frame{EtherType: ethernet.EtherTypeProfinet}) {
		t.Fatal("MatchEtherType")
	}
	ids := MatchFrameIDs(0x8000, 0xBFFF)
	for id, want := range map[uint16]bool{0x7FFF: false, 0x8000: true, 0x9ABC: true, 0xBFFF: true, 0xC000: false, 0: false, 0xFFFF: false} {
		if got := ids(pnFrame(id)); got != want {
			t.Errorf("FrameID %#04x: %v", id, got)
		}
	}
	if ids(&ethernet.Frame{EtherType: ethernet.EtherTypeProfinet, Payload: []byte{0x80}}) {
		t.Error("one-octet payload matched")
	}
	if ids(&ethernet.Frame{EtherType: etTest, Payload: []byte{0x80, 0x00}}) {
		t.Error("other EtherType matched")
	}
	one := MatchFrameIDs(0xFEFE, 0xFEFE)
	if !one(pnFrame(0xFEFE)) || one(pnFrame(0xFEFD)) || one(pnFrame(0xFEFF)) {
		t.Error("single-ID range")
	}
	if MatchFrameIDs(5, 4)(pnFrame(4)) || MatchFrameIDs(5, 4)(pnFrame(5)) {
		t.Error("empty range matched")
	}
	full := MatchFrameIDs(0, 0xFFFF)
	if !full(pnFrame(0)) || !full(pnFrame(0xFFFF)) {
		t.Error("full range")
	}
}

func TestJoinMulticastHelper(t *testing.T) {
	s := newSetup(t)
	group := ethernet.DCPIdentifyMulticast
	if err := JoinMulticast(s.rx, group); err != nil {
		t.Fatal(err)
	}
	sub, _ := s.mux.Subscribe(MatchEtherType(etTest), 4)
	f := (&ethernet.Frame{Dst: group, Src: mac(1), EtherType: etTest}).Encode()
	if err := s.tx.WriteFrame(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, sub); got.Dst != group {
		t.Fatalf("dst %v", got.Dst)
	}
	if err := JoinMulticast(s.rx, mac(5)); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
	if err := JoinMulticast(newFakePort(), group); err != nil {
		t.Fatalf("non-multicast port: %v", err)
	}
}

func TestDispatch(t *testing.T) {
	s := newSetup(t)
	if s.mux.Port() != s.rx || s.mux.HardwareAddr() != mac(2) {
		t.Fatal("Port/HardwareAddr")
	}
	if s.mux.Err() != nil {
		t.Fatal("Err while running")
	}
	a, err := s.mux.Subscribe(MatchEtherType(etTest), 8)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := s.mux.Subscribe(func(f *ethernet.Frame) bool { return f.EtherType != ethernet.EtherTypeARP }, 8)
	pn, _ := s.mux.Subscribe(MatchFrameIDs(0x8000, 0x8000), 8)

	s.send(t, etTest, 1)
	s.send(t, ethernet.EtherTypeProfinet, 0x80, 0x00, 2)
	s.send(t, ethernet.EtherTypeARP, 3)
	s.send(t, 0x0005, 4) // 802.3 length: malformed
	waitFor(t, "4 frames", func() bool { return s.mux.Stats().Received == 4 })

	if f := recv(t, a); f.EtherType != etTest || f.Payload[0] != 1 || f.Src != mac(1) {
		t.Fatalf("a got %+v", f)
	}
	if f := recv(t, all); f.EtherType != etTest {
		t.Fatalf("all got %v", f.EtherType)
	}
	if f := recv(t, all); f.EtherType != ethernet.EtherTypeProfinet {
		t.Fatalf("all got %v", f.EtherType)
	}
	if f := recv(t, pn); f.Payload[2] != 2 {
		t.Fatalf("pn got % x", f.Payload)
	}
	st := s.mux.Stats()
	if st != (MuxStats{Received: 4, Malformed: 1, Unclaimed: 1}) {
		t.Fatalf("stats %+v", st)
	}
	if err := s.mux.Send(context.Background(), (&ethernet.Frame{Dst: mac(1), Src: mac(2), EtherType: etTest}).Encode()); err != nil {
		t.Fatal(err)
	}
	if s.mux.Stats().Sent != 1 || s.tx.Stats().Received != 1 {
		t.Fatal("send not counted or delivered")
	}
}

func TestSubscribeConfig(t *testing.T) {
	s := newSetup(t)
	if _, err := s.mux.Subscribe(nil, 1); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
	sub, err := s.mux.Subscribe(MatchEtherType(etTest), 0)
	if err != nil {
		t.Fatal(err)
	}
	if cap(sub.free) != 2 {
		t.Fatalf("queue of %d, want 1 (+1 spare)", cap(sub.free)-1)
	}
}

func TestOverflow(t *testing.T) {
	s := newSetup(t)
	sub, _ := s.mux.Subscribe(MatchEtherType(etTest), 2)
	other, _ := s.mux.Subscribe(MatchEtherType(etTest), 16)
	for i := byte(0); i < 5; i++ {
		s.send(t, etTest, i)
	}
	waitFor(t, "5 frames", func() bool { return s.mux.Stats().Received == 5 })
	if sub.Overflow() != 3 || other.Overflow() != 0 || s.mux.Stats().Overflow != 3 {
		t.Fatalf("overflow %d/%d/%d", sub.Overflow(), other.Overflow(), s.mux.Stats().Overflow)
	}
	// The oldest frames were kept.
	for i := byte(0); i < 2; i++ {
		if f := recv(t, sub); f.Payload[0] != i {
			t.Fatalf("got %d, want %d", f.Payload[0], i)
		}
	}
	for i := byte(0); i < 5; i++ {
		if f := recv(t, other); f.Payload[0] != i {
			t.Fatalf("other got %d", f.Payload[0])
		}
	}
	// After draining, the queue takes frames again; the frame returned by
	// the last Recv stays valid while new frames arrive.
	recvNone(t, sub)
	s.send(t, etTest, 10)
	s.send(t, etTest, 11)
	if f := recv(t, sub); f.Payload[0] != 10 {
		t.Fatalf("got %d", f.Payload[0])
	}
	f := recv(t, sub)
	held := f.Payload[0]
	s.send(t, etTest, 12)
	s.send(t, etTest, 13)
	waitFor(t, "frames 12 and 13", func() bool { return s.mux.Stats().Received == 9 })
	if f.Payload[0] != held || held != 11 {
		t.Fatalf("buffer of the current frame reused: %d", f.Payload[0])
	}
	if sub.Overflow() != 3 {
		t.Fatalf("overflow %d", sub.Overflow())
	}
}

// recvNone checks that nothing is queued.
func recvNone(t testing.TB, s *Subscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want deadline exceeded", err)
	}
}

func TestRecvContext(t *testing.T) {
	s := newSetup(t)
	sub, _ := s.mux.Subscribe(MatchEtherType(etTest), 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sub.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	errc := make(chan error, 1)
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		_, err := sub.Recv(ctx)
		errc <- err
	}()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not unblock Recv")
	}
	// The subscription still works afterwards.
	s.send(t, etTest, 9)
	if f := recv(t, sub); f.Payload[0] != 9 {
		t.Fatal("wrong frame")
	}
}

func TestSubscriptionClose(t *testing.T) {
	s := newSetup(t)
	sub, _ := s.mux.Subscribe(MatchEtherType(etTest), 4)
	s.send(t, etTest, 1)
	s.send(t, etTest, 2)
	waitFor(t, "2 frames", func() bool { return s.mux.Stats().Received == 2 })
	sub.Close()
	sub.Close()
	select {
	case <-sub.Done():
	default:
		t.Fatal("Done not closed")
	}
	// Queued frames first, then ErrClosed.
	for i := byte(1); i <= 2; i++ {
		if f := recv(t, sub); f.Payload[0] != i {
			t.Fatalf("got %d", f.Payload[0])
		}
	}
	if _, err := sub.Recv(context.Background()); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	// It no longer claims frames.
	s.send(t, etTest, 3)
	waitFor(t, "unclaimed frame", func() bool { return s.mux.Stats().Unclaimed == 1 })
	// deliver to a closed subscription is a silent success.
	if !sub.deliver([]byte{1}) {
		t.Fatal("deliver to closed subscription reported overflow")
	}
}

func TestMuxClose(t *testing.T) {
	s := newSetup(t)
	sub, _ := s.mux.Subscribe(MatchEtherType(etTest), 4)
	s.send(t, etTest, 1)
	waitFor(t, "a frame", func() bool { return s.mux.Stats().Received == 1 })
	errc := make(chan error, 1)
	idle, _ := s.mux.Subscribe(MatchEtherType(ethernet.EtherTypeLLDP), 1)
	go func() {
		_, err := idle.Recv(context.Background())
		errc <- err
	}()
	if err := s.mux.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.mux.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	select {
	case <-s.mux.Done():
	default:
		t.Fatal("Done not closed")
	}
	if s.mux.Err() != nil {
		t.Fatalf("Err after Close: %v", s.mux.Err())
	}
	select {
	case err := <-errc:
		if !errors.Is(err, profinet.ErrClosed) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Mux.Close did not unblock Recv")
	}
	select {
	case <-sub.Done():
	default:
		t.Fatal("subscription not closed")
	}
	if f := recv(t, sub); f.Payload[0] != 1 {
		t.Fatal("queued frame lost")
	}
	if _, err := sub.Recv(context.Background()); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if err := s.mux.Send(context.Background(), make([]byte, 60)); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("send err %v", err)
	}
	if _, err := s.mux.Subscribe(MatchEtherType(etTest), 1); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("subscribe err %v", err)
	}
	// The port stays open.
	if !s.rx.LinkUp() {
		t.Fatal("link")
	}
	if err := s.rx.WriteFrame(context.Background(), (&ethernet.Frame{Dst: mac(1), Src: mac(2), EtherType: etTest}).Encode()); err != nil {
		t.Fatalf("port closed by Mux.Close: %v", err)
	}
}

func TestPortErrors(t *testing.T) {
	p := newFakePort()
	m := NewMux(p)
	defer m.Close()
	sub, _ := m.Subscribe(MatchEtherType(etTest), 1)
	p.in <- []byte{1, 2, 3} // too short: malformed
	waitFor(t, "malformed frame", func() bool { return m.Stats().Malformed == 1 })
	if err := m.Send(context.Background(), nil); err == nil || m.Stats().Sent != 0 {
		t.Fatal("port write error not returned or counted as sent")
	}
	boom := errors.New("device gone")
	p.errc <- boom
	select {
	case <-m.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not stop")
	}
	if !errors.Is(m.Err(), boom) {
		t.Fatalf("Err %v", m.Err())
	}
	if _, err := sub.Recv(context.Background()); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if err := m.Close(); err != nil || !errors.Is(m.Err(), boom) {
		t.Fatalf("Close %v, Err %v", err, m.Err())
	}

	// Closing the port stops the reader with an error matching ErrClosed.
	s := newSetup(t)
	s.rx.Close()
	select {
	case <-s.mux.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not stop")
	}
	if !errors.Is(s.mux.Err(), profinet.ErrClosed) {
		t.Fatalf("Err %v", s.mux.Err())
	}
}

func TestSendLinkDown(t *testing.T) {
	s := newSetup(t)
	s.rx.SetLinkUp(false)
	if err := s.mux.Send(context.Background(), (&ethernet.Frame{Dst: mac(1), Src: mac(2), EtherType: etTest}).Encode()); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("err %v", err)
	}
	if s.mux.Stats().Sent != 0 {
		t.Fatal("failed send counted")
	}
}

func TestConcurrentSubscribe(t *testing.T) {
	s := newSetup(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			f := (&ethernet.Frame{Dst: mac(2), Src: mac(1), EtherType: etTest}).Encode()
			if err := s.tx.WriteFrame(context.Background(), f); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 50; i++ {
		sub, err := s.mux.Subscribe(MatchEtherType(etTest), 2)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		_, _ = sub.Recv(ctx)
		cancel()
		sub.Close()
	}
	close(stop)
	wg.Wait()
	st := s.mux.Stats()
	if st.Received == 0 || st.Malformed != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func BenchmarkMuxDispatch(b *testing.B) {
	n := vnet.New(1)
	defer n.Close()
	tx, _ := n.NewEndpoint(vnet.EndpointConfig{MAC: mac(1)})
	rx, _ := n.NewEndpoint(vnet.EndpointConfig{MAC: mac(2), QueueLen: 4096})
	m := NewMux(rx)
	defer m.Close()
	// Decoy subscriptions that the dispatcher evaluates first.
	for i := uint16(0); i < 4; i++ {
		s, _ := m.Subscribe(MatchFrameIDs(0x100*i, 0x100*i+0xFF), 4)
		defer s.Close()
	}
	sub, _ := m.Subscribe(MatchFrameIDs(0x8000, 0xBFFF), 256)
	defer sub.Close()
	f := (&ethernet.Frame{Dst: mac(2), Src: mac(1), EtherType: ethernet.EtherTypeProfinet, Payload: []byte{0x80, 0x01, 0, 0}}).Encode()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if err := tx.WriteFrame(ctx, f); err != nil {
			b.Fatal(err)
		}
		if _, err := sub.Recv(ctx); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "frames/s")
}
