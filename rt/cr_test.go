// SPDX-License-Identifier: MIT

package rt

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/vnet"
)

// capture is a Send func that records copies of the frames.
type capture struct {
	mu     sync.Mutex
	frames [][]byte
	ctxs   []context.Context
	err    error
}

func (c *capture) send(ctx context.Context, frame []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, append([]byte(nil), frame...))
	c.ctxs = append(c.ctxs, ctx)
	return c.err
}

func (c *capture) last(t *testing.T) (ethernet.Frame, Frame) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.frames) == 0 {
		t.Fatal("no frame sent")
	}
	e, err := ethernet.Decode(c.frames[len(c.frames)-1])
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(e.Payload, MinDataLength)
	if err != nil {
		t.Fatal(err)
	}
	return e, f
}

type ctxKey struct{}

func TestProviderTick(t *testing.T) {
	var c capture
	fills := 0
	p := NewProvider(ProviderConfig{
		FrameID: 0xC000, DataLength: MinDataLength, Dst: macB, Src: macA, VLAN: 3, CounterStep: 32,
		Send: c.send,
		Fill: func(d []byte) { fills++; d[0] = byte(fills) },
	})
	if p.FrameID() != 0xC000 || p.DataStatus() != DefaultDataStatus {
		t.Fatalf("FrameID 0x%04X DataStatus %s", p.FrameID(), p.DataStatus())
	}
	ctx := context.WithValue(context.Background(), ctxKey{}, 1)
	p.Update(func(d []byte) {
		if len(d) != MinDataLength {
			t.Fatalf("C_SDU of %d octets", len(d))
		}
		d[1], d[39] = 0xAA, 0xBB
	})
	p.Tick(ctx)
	e, f := c.last(t)
	if e.Dst != macB || e.Src != macA || !e.Tagged || e.VLAN.Priority != 6 || e.VLAN.ID != 3 || e.EtherType != ethernet.EtherTypeProfinet {
		t.Fatalf("header %+v", e)
	}
	if f.FrameID != 0xC000 || f.CycleCounter != 32 || f.DataStatus != DefaultDataStatus || f.TransferStatus != 0 {
		t.Fatalf("frame %+v", f)
	}
	if f.Data[0] != 1 || f.Data[1] != 0xAA || f.Data[39] != 0xBB {
		t.Fatalf("data %x", f.Data)
	}
	if c.ctxs[0].Value(ctxKey{}) != 1 {
		t.Fatal("context not passed to Send")
	}

	// The counter advances by the step, wrapping at 16 bits.
	p.SetDataStatus(DefaultDataStatus &^ DataValid)
	p.SetDestination(ethernet.MAC{2, 0, 0, 0, 0, 0x0c})
	for i := 0; i < 2047; i++ {
		p.Tick(ctx)
	}
	e, f = c.last(t)
	if f.CycleCounter != 0 { // 2048 × 32 = 65536
		t.Fatalf("counter 0x%04X after 2048 ticks", f.CycleCounter)
	}
	if f.DataStatus.Valid() || p.DataStatus().Valid() || e.Dst != (ethernet.MAC{2, 0, 0, 0, 0, 0x0c}) {
		t.Fatalf("status %s dst %s", f.DataStatus, e.Dst)
	}
	if f.Data[0] != byte(2048%256) || fills != 2048 {
		t.Fatalf("Fill called %d times", fills)
	}
	if s := p.Stats(); s.Sent != 2048 || s.SendErrors != 0 {
		t.Fatalf("stats %+v", s)
	}

	// Send errors are counted, not sent.
	c.mu.Lock()
	c.err = errors.New("down")
	c.mu.Unlock()
	p.Tick(ctx)
	p.Tick(ctx)
	if s := p.Stats(); s.Sent != 2048 || s.SendErrors != 2 {
		t.Fatalf("stats %+v", s)
	}
}

func TestProviderWithoutFill(t *testing.T) {
	var c capture
	p := NewProvider(ProviderConfig{FrameID: 0x8000, DataLength: 50, Dst: macB, Src: macA, CounterStep: 1, Send: c.send})
	p.Tick(context.Background())
	c.mu.Lock()
	b := c.frames[0]
	c.mu.Unlock()
	// 18 octets of header, FrameID, 50 octets of zeros, the trailer.
	want := append(hx("02000000000b 02000000000a 8100 c000 8892 8000"), make([]byte, 50)...)
	want = append(want, hx("0001 35 00")...)
	if !bytes.Equal(b, want) {
		t.Fatalf("frame\n got %x\nwant %x", b, want)
	}
}

func TestProviderTickAllocs(t *testing.T) {
	p := NewProvider(ProviderConfig{FrameID: 0xC000, DataLength: MaxDataLength, Dst: macB, Src: macA, CounterStep: 1,
		Send: func(context.Context, []byte) error { return nil }, Fill: func([]byte) {}})
	ctx := context.Background()
	if n := testing.AllocsPerRun(100, func() { p.Tick(ctx) }); n != 0 {
		t.Fatalf("Tick allocates %.1f times", n)
	}
	// The smallest C_SDU is padded within the preallocated buffer too.
	p = NewProvider(ProviderConfig{FrameID: 0xC000, DataLength: 0, Send: func(context.Context, []byte) error { return nil }})
	if n := testing.AllocsPerRun(100, func() { p.Tick(ctx) }); n != 0 {
		t.Fatalf("Tick allocates %.1f times", n)
	}
}

// rxFrame encodes a cyclic frame and decodes it as received.
func rxFrame(t testing.TB, src ethernet.MAC, id uint16, data []byte, counter uint16, ds DataStatus) *ethernet.Frame {
	t.Helper()
	b := AppendEthernet(nil, macB, src, 0, &Frame{FrameID: id, Data: data, CycleCounter: counter, DataStatus: ds})
	e, err := ethernet.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return &e
}

func TestConsumerHandle(t *testing.T) {
	c := NewConsumer(ConsumerConfig{FrameID: 0xC001, DataLength: 4, Src: macA, Watchdog: time.Second})
	if c.FrameID() != 0xC001 {
		t.Fatal("FrameID")
	}
	type upd struct {
		data []byte
		s    DataStatus
	}
	var updates []upd
	c.OnUpdate(func(d []byte, s DataStatus) { updates = append(updates, upd{append([]byte(nil), d...), s}) })

	// Nothing received yet.
	ok := c.Read(func(d []byte, s DataStatus) {
		if !bytes.Equal(d, make([]byte, 4)) || s != 0 {
			t.Fatalf("initial %x %s", d, s)
		}
	})
	if ok {
		t.Fatal("Read ok before the first frame")
	}

	now := time.Now()
	c.Handle(rxFrame(t, macA, 0xC001, hx("01020304"), 10, DefaultDataStatus), now)
	ok = c.Read(func(d []byte, s DataStatus) {
		if !bytes.Equal(d, hx("01020304")) || s != DefaultDataStatus {
			t.Fatalf("after frame %x %s", d, s)
		}
	})
	if !ok {
		t.Fatal("Read not ok")
	}

	// Wrong source, malformed (short, wrong FrameID), stale counters.
	c.Handle(rxFrame(t, macB, 0xC001, hx("ffffffff"), 11, DefaultDataStatus), now)
	short := rxFrame(t, macA, 0xC001, hx("01"), 11, DefaultDataStatus)
	short.Payload = short.Payload[:2+1+TrailerLen]
	c.Handle(short, now)
	c.Handle(rxFrame(t, macA, 0xC002, hx("ffffffff"), 11, DefaultDataStatus), now)
	c.Handle(rxFrame(t, macA, 0xC001, hx("ffffffff"), 10, DefaultDataStatus), now)        // duplicate
	c.Handle(rxFrame(t, macA, 0xC001, hx("ffffffff"), 9, DefaultDataStatus), now)         // older
	c.Handle(rxFrame(t, macA, 0xC001, hx("ffffffff"), 10+0x8000, DefaultDataStatus), now) // half a wrap ahead
	s := c.Stats()
	if s.Received != 1 || s.WrongSource != 1 || s.Malformed != 2 || s.Stale != 3 || s.LastCounter != 10 || s.Expired != 0 {
		t.Fatalf("stats %+v", s)
	}

	// IgnoreDataStatus: the counter advances, the data and status stay.
	c.Handle(rxFrame(t, macA, 0xC001, hx("ffffffff"), 11, IgnoreDataStatus|DefaultDataStatus), now)
	c.Read(func(d []byte, st DataStatus) {
		if !bytes.Equal(d, hx("01020304")) || st != DefaultDataStatus {
			t.Fatalf("after ignore %x %s", d, st)
		}
	})
	// A wrap of the counter is newer.
	c3 := uint16(11 + 0x7FFF)
	c4 := c3 + 0x7FFF // wraps to 0x0009
	c.Handle(rxFrame(t, macA, 0xC001, hx("05060708"), c3, 0x25), now)
	c.Handle(rxFrame(t, macA, 0xC001, hx("090a0b0c"), c4, 0x35), now)
	if s := c.Stats(); s.Received != 4 || s.LastCounter != 0x0009 {
		t.Fatalf("stats %+v", s)
	}
	want := []upd{
		{hx("01020304"), DefaultDataStatus},
		{hx("01020304"), DefaultDataStatus},
		{hx("05060708"), 0x25},
		{hx("090a0b0c"), 0x35},
	}
	if len(updates) != len(want) {
		t.Fatalf("%d updates", len(updates))
	}
	for i := range want {
		if !bytes.Equal(updates[i].data, want[i].data) || updates[i].s != want[i].s {
			t.Fatalf("update %d: %x %s", i, updates[i].data, updates[i].s)
		}
	}

	// A zero source accepts any; SetSource changes it.
	c.SetSource(ethernet.MAC{})
	c.Handle(rxFrame(t, macB, 0xC001, hx("00000001"), 100, 0x35), now)
	c.SetSource(macB)
	c.Handle(rxFrame(t, macA, 0xC001, hx("00000002"), 101, 0x35), now)
	if s := c.Stats(); s.Received != 5 || s.WrongSource != 2 {
		t.Fatalf("stats %+v", s)
	}
	// The first frame is accepted whatever its counter.
	c2 := NewConsumer(ConsumerConfig{FrameID: 0xC001, DataLength: 4})
	c2.Handle(rxFrame(t, macA, 0xC001, hx("00000001"), 0, 0x35), now)
	if s := c2.Stats(); s.Received != 1 || s.LastCounter != 0 {
		t.Fatalf("first frame %+v", s)
	}
}

func TestConsumerWatchdog(t *testing.T) {
	start := time.Now()
	c := NewConsumer(ConsumerConfig{FrameID: 0xC001, DataLength: 4, Watchdog: 100 * time.Millisecond})
	if c.Check(start) {
		t.Fatal("expired at start")
	}
	// Measured from the creation: a second later it has expired, once.
	late := start.Add(time.Second)
	if !c.Check(late) {
		t.Fatal("not expired")
	}
	if c.Check(late) || c.Check(late.Add(time.Hour)) {
		t.Fatal("expired twice")
	}
	if s := c.Stats(); s.Expired != 1 {
		t.Fatalf("stats %+v", s)
	}
	// A frame re-arms it.
	c.Handle(rxFrame(t, macA, 0xC001, hx("00000001"), 1, 0x35), late)
	if c.Check(late.Add(100 * time.Millisecond)) {
		t.Fatal("expired at exactly the watchdog time")
	}
	if !c.Check(late.Add(101 * time.Millisecond)) {
		t.Fatal("not expired after the re-arm")
	}
	if s := c.Stats(); s.Expired != 2 {
		t.Fatalf("stats %+v", s)
	}
	// A stale frame does not re-arm it.
	c.Handle(rxFrame(t, macA, 0xC001, hx("00000001"), 1, 0x35), late.Add(time.Second))
	if c.Check(late.Add(time.Hour)) {
		t.Fatal("expired again without a new frame")
	}

	// Without a watchdog the consumer never expires.
	c = NewConsumer(ConsumerConfig{FrameID: 0xC001, DataLength: 4})
	if c.Check(start.Add(24 * time.Hour)) {
		t.Fatal("expired without a watchdog")
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDispatcher(t *testing.T) {
	n := vnet.New(1)
	defer n.Close()
	epA, err := n.NewEndpoint(vnet.EndpointConfig{Name: "a", MAC: macA})
	if err != nil {
		t.Fatal(err)
	}
	epB, err := n.NewEndpoint(vnet.EndpointConfig{Name: "b", MAC: macB})
	if err != nil {
		t.Fatal(err)
	}
	mux := l2.NewMux(epB)
	defer mux.Close()
	d, err := NewDispatcher(mux)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	c1 := NewConsumer(ConsumerConfig{FrameID: 0xC001, DataLength: MinDataLength, Src: macA})
	c2 := NewConsumer(ConsumerConfig{FrameID: 0x8001, DataLength: MinDataLength, Src: macA})
	d.Add(c1)
	d.Add(c2)
	got := make(chan byte, 16)
	c2.OnUpdate(func(b []byte, _ DataStatus) { got <- b[0] })

	send := func(id uint16, counter uint16, first byte) {
		t.Helper()
		data := make([]byte, MinDataLength)
		data[0] = first
		b := AppendEthernet(nil, macB, macA, 0, &Frame{FrameID: id, Data: data, CycleCounter: counter, DataStatus: DefaultDataStatus})
		if err := epA.WriteFrame(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	send(0xC001, 1, 0x11)
	send(0x8001, 1, 0x22)
	send(0xC002, 1, 0x33) // no consumer
	send(0xFC01, 1, 0x44) // an alarm FrameID: not the dispatcher's
	eventually(t, "routing", func() bool {
		return c1.Stats().Received == 1 && c2.Stats().Received == 1 && d.Unknown() == 1
	})
	if b := <-got; b != 0x22 {
		t.Fatalf("c2 got 0x%02X", b)
	}
	c1.Read(func(b []byte, s DataStatus) {
		if b[0] != 0x11 || s != DefaultDataStatus {
			t.Fatalf("c1 data %x %s", b[:1], s)
		}
	})

	// A replacement consumer for the same FrameID takes over; removing
	// the old one does nothing.
	c1b := NewConsumer(ConsumerConfig{FrameID: 0xC001, DataLength: MinDataLength})
	d.Add(c1b)
	d.Remove(c1)
	send(0xC001, 2, 0x55)
	eventually(t, "replacement", func() bool { return c1b.Stats().Received == 1 })
	if c1.Stats().Received != 1 {
		t.Fatal("the replaced consumer still receives")
	}
	// Removed consumers' frames count as unknown.
	d.Remove(c1b)
	d.Remove(c2)
	send(0xC001, 3, 0x66)
	send(0x8001, 2, 0x77)
	eventually(t, "unknown after Remove", func() bool { return d.Unknown() == 3 })
	if c1b.Stats().Received != 1 || c2.Stats().Received != 1 {
		t.Fatal("removed consumers still receive")
	}

	d.Close()
	d.Close() // idempotent
	// After Close the subscription is gone: frames are unclaimed.
	before := mux.Stats().Unclaimed
	send(0xC001, 4, 0)
	eventually(t, "unclaimed after Close", func() bool { return mux.Stats().Unclaimed == before+1 })
}

func TestDispatcherClosedMux(t *testing.T) {
	n := vnet.New(2)
	defer n.Close()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: macA})
	if err != nil {
		t.Fatal(err)
	}
	mux := l2.NewMux(ep)
	mux.Close()
	if d, err := NewDispatcher(mux); err == nil || d != nil {
		t.Fatalf("NewDispatcher on a closed mux: %v", err)
	}
}

func TestDispatcherMuxStops(t *testing.T) {
	n := vnet.New(3)
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: macA})
	if err != nil {
		t.Fatal(err)
	}
	mux := l2.NewMux(ep)
	d, err := NewDispatcher(mux)
	if err != nil {
		t.Fatal(err)
	}
	// Closing the mux ends the dispatcher's goroutine; Close returns.
	mux.Close()
	done := make(chan struct{})
	go func() { d.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hangs after the mux stopped")
	}
	n.Close()
}
