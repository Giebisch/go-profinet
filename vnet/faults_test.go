// SPDX-License-Identifier: MIT

package vnet

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// waitFor polls cond for up to 10 seconds.
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

func pair(t testing.TB, seed uint64) (*Network, *Endpoint, *Endpoint) {
	n := New(seed)
	a := newEP(t, n, EndpointConfig{Name: "a", MAC: mac(1)})
	b := newEP(t, n, EndpointConfig{Name: "b", MAC: mac(2), QueueLen: 8192})
	return n, a, b
}

func TestQueueOverflow(t *testing.T) {
	n := New(1)
	a := newEP(t, n, EndpointConfig{MAC: mac(1)})
	b := newEP(t, n, EndpointConfig{MAC: mac(2), QueueLen: 2})
	for i := 0; i < 5; i++ {
		write(t, a, frame(mac(2), mac(1), uint32(i)))
	}
	got := drain(b)
	if len(got) != 2 || seqOf(t, got[0]) != 0 || seqOf(t, got[1]) != 1 {
		t.Fatalf("got %d frames", len(got))
	}
	if st := b.Stats(); st.Received != 2 || st.Overflow != 3 {
		t.Fatalf("stats %+v", st)
	}
}

func TestLinkUpDown(t *testing.T) {
	_, a, b := pair(t, 1)
	b.SetLinkUp(false)
	if b.LinkUp() {
		t.Fatal("link still up")
	}
	write(t, a, frame(mac(2), mac(1), 1))
	if len(drain(b)) != 0 {
		t.Fatal("frame delivered to a down link")
	}
	if err := b.WriteFrame(context.Background(), frame(mac(1), mac(2), 1)); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("err %v", err)
	}
	if st := b.Stats(); st.LinkDrops != 2 || st.Sent != 0 || st.Received != 0 {
		t.Fatalf("stats %+v", st)
	}
	if len(drain(a)) != 0 {
		t.Fatal("down endpoint sent")
	}
	b.SetLinkUp(true)
	write(t, a, frame(mac(2), mac(1), 2))
	write(t, b, frame(mac(1), mac(2), 3))
	if len(drain(b)) != 1 || len(drain(a)) != 1 {
		t.Fatal("link up again does not deliver")
	}
}

func TestEndpointClose(t *testing.T) {
	n, a, b := pair(t, 1)
	errc := make(chan error, 1)
	go func() {
		_, err := b.ReadFrame(context.Background(), make([]byte, 100))
		errc <- err
	}()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, profinet.ErrClosed) {
			t.Fatalf("read err %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not unblock ReadFrame")
	}
	if err := b.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	if err := b.WriteFrame(context.Background(), frame(mac(1), mac(2), 0)); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("write err %v", err)
	}
	if _, err := b.ReadFrame(context.Background(), nil); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("read err %v", err)
	}
	// A closed endpoint is detached and its address can be reused.
	if eps := n.Endpoints(); len(eps) != 1 || eps[0] != a {
		t.Fatalf("endpoints %v", eps)
	}
	write(t, a, frame(mac(2), mac(1), 1))
	b.receive(frame(mac(2), mac(1), 1))
	if len(drain(b)) != 0 {
		t.Fatal("closed endpoint received")
	}
	b2 := newEP(t, n, EndpointConfig{MAC: mac(2)})
	write(t, a, frame(mac(2), mac(1), 2))
	if len(drain(b2)) != 1 {
		t.Fatal("replacement endpoint missed the frame")
	}
	// detach of an endpoint whose MAC now belongs to another keeps it.
	n.detach(b)
	if n.byMAC[mac(2)] != b2 {
		t.Fatal("detach removed the replacement")
	}
}

func TestContextCancel(t *testing.T) {
	_, a, b := pair(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.ReadFrame(ctx, make([]byte, 10)); !errors.Is(err, context.Canceled) {
		t.Fatalf("read err %v", err)
	}
	if err := a.WriteFrame(ctx, frame(mac(2), mac(1), 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("write err %v", err)
	}
	if a.Stats().Sent != 0 {
		t.Fatal("cancelled write counted")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := b.ReadFrame(ctx, make([]byte, 10)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read err %v", err)
	}
}

func TestNetworkClose(t *testing.T) {
	n := New(1)
	var eps []*Endpoint
	for i := byte(1); i <= 3; i++ {
		eps = append(eps, newEP(t, n, EndpointConfig{MAC: mac(i)}))
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if len(n.Endpoints()) != 0 {
		t.Fatal("endpoints left")
	}
	for _, e := range eps {
		if _, err := e.ReadFrame(context.Background(), nil); !errors.Is(err, profinet.ErrClosed) {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	if _, err := n.NewEndpoint(EndpointConfig{MAC: mac(9)}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
}

func TestTaps(t *testing.T) {
	n, a, b := pair(t, 1)
	var mu sync.Mutex
	var seen [][]byte
	var from []*Endpoint
	n.AddTap(func(e *Endpoint, f []byte) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, append([]byte(nil), f...))
		from = append(from, e)
	})
	var second int
	n.AddTap(func(*Endpoint, []byte) { second++ })
	a.SetFaults(Faults{Loss: 1})
	write(t, a, frame(mac(2), mac(1), 1))
	write(t, b, frame(mac(1), mac(2), 2))
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || second != 2 {
		t.Fatalf("taps saw %d/%d frames", len(seen), second)
	}
	if from[0] != a || from[1] != b || seqOf(t, seen[0]) != 1 || seqOf(t, seen[1]) != 2 {
		t.Fatal("tap order or origin wrong")
	}
	if len(seen[0]) != ethernet.MinFrameLen {
		t.Fatal("tap saw unpadded frame")
	}
	if len(drain(b)) != 0 || len(drain(a)) != 1 {
		t.Fatal("loss not applied after tap")
	}
}

func TestFaultLoss(t *testing.T) {
	_, a, b := pair(t, 7)
	a.SetFaults(Faults{Loss: 1})
	for i := 0; i < 10; i++ {
		write(t, a, frame(mac(2), mac(1), uint32(i)))
	}
	if len(drain(b)) != 0 || a.Stats().Dropped != 10 || a.Stats().Sent != 10 {
		t.Fatalf("stats %+v", a.Stats())
	}
	a.SetFaults(Faults{Loss: 0.5})
	const total = 2000
	for i := 0; i < total; i++ {
		write(t, a, frame(mac(2), mac(1), uint32(i)))
	}
	got := len(drain(b))
	if got < total/4 || got > total*3/4 {
		t.Fatalf("Loss 0.5 delivered %d of %d", got, total)
	}
	if d := a.Stats().Dropped - 10; int(d) != total-got {
		t.Fatalf("dropped %d, delivered %d", d, got)
	}
	a.SetFaults(Faults{})
	write(t, a, frame(mac(2), mac(1), 0))
	if len(drain(b)) != 1 {
		t.Fatal("zero faults drop frames")
	}
}

func TestFaultDuplicate(t *testing.T) {
	_, a, b := pair(t, 7)
	a.SetFaults(Faults{Duplicate: 1})
	for i := 0; i < 5; i++ {
		write(t, a, frame(mac(2), mac(1), uint32(i)))
	}
	got := drain(b)
	if len(got) != 10 {
		t.Fatalf("got %d frames", len(got))
	}
	for i := 0; i < 5; i++ {
		if seqOf(t, got[2*i]) != uint32(i) || !bytes.Equal(got[2*i], got[2*i+1]) {
			t.Fatalf("frame %d not duplicated in place", i)
		}
	}
	// The duplicate is a distinct buffer.
	got[0][20] ^= 0xFF
	if bytes.Equal(got[0], got[1]) {
		t.Fatal("duplicate aliases the original")
	}
	if a.Stats().Duplicated != 5 {
		t.Fatalf("stats %+v", a.Stats())
	}
}

func TestFaultCorrupt(t *testing.T) {
	_, a, b := pair(t, 7)
	a.SetFaults(Faults{Corrupt: 1})
	for i := 0; i < 50; i++ {
		orig := frame(mac(2), mac(1), uint32(i))
		write(t, a, orig)
		got := drain(b)
		if len(got) != 1 || len(got[0]) != len(orig) {
			t.Fatalf("frame %d: %d frames", i, len(got))
		}
		bits := 0
		for j := range orig {
			x := orig[j] ^ got[0][j]
			if x != 0 && j < ethernet.HeaderLen {
				t.Fatalf("header octet %d corrupted", j)
			}
			for ; x != 0; x &= x - 1 {
				bits++
			}
		}
		if bits != 1 {
			t.Fatalf("frame %d: %d bits flipped", i, bits)
		}
	}
	if a.Stats().Corrupted != 50 {
		t.Fatalf("stats %+v", a.Stats())
	}
}

func TestFaultMatch(t *testing.T) {
	_, a, b := pair(t, 7)
	a.SetFaults(Faults{Loss: 1, Match: func(f []byte) bool { return f[14] == 0xEE }})
	pass := frame(mac(2), mac(1), 0x01000000)
	drop := frame(mac(2), mac(1), 0xEE000000)
	write(t, a, pass)
	write(t, a, drop)
	write(t, a, pass)
	if got := drain(b); len(got) != 2 {
		t.Fatalf("got %d frames", len(got))
	}
	if a.Stats().Dropped != 1 {
		t.Fatalf("stats %+v", a.Stats())
	}
}

func TestFaultReorder(t *testing.T) {
	_, a, b := pair(t, 7)
	a.SetFaults(Faults{Reorder: 1, ReorderHold: time.Hour})
	write(t, a, frame(mac(2), mac(1), 1))
	if len(drain(b)) != 0 {
		t.Fatal("held frame delivered")
	}
	write(t, a, frame(mac(2), mac(1), 2))
	got := drain(b)
	if len(got) != 2 || seqOf(t, got[0]) != 2 || seqOf(t, got[1]) != 1 {
		t.Fatalf("got %d frames", len(got))
	}
	// A frame the faults do not match releases the held frame after it.
	a.SetFaults(Faults{Reorder: 1, ReorderHold: time.Hour, Match: func(f []byte) bool { return f[17] != 9 }})
	write(t, a, frame(mac(2), mac(1), 3))
	write(t, a, frame(mac(2), mac(1), 9))
	got = drain(b)
	if len(got) != 2 || seqOf(t, got[0]) != 9 || seqOf(t, got[1]) != 3 {
		t.Fatalf("got %d frames", len(got))
	}
	if a.Stats().Reordered != 2 {
		t.Fatalf("stats %+v", a.Stats())
	}
}

func TestFaultReorderHoldFlush(t *testing.T) {
	for _, hold := range []time.Duration{0, 10 * time.Millisecond} {
		_, a, b := pair(t, 7)
		a.SetFaults(Faults{Reorder: 1, ReorderHold: hold})
		write(t, a, frame(mac(2), mac(1), 1))
		if seqOf(t, read(t, b)) != 1 {
			t.Fatal("wrong frame flushed")
		}
		a.fmu.Lock()
		held := a.held
		a.fmu.Unlock()
		if held != nil {
			t.Fatal("frame still held after flush")
		}
	}
}

func TestFaultReorderWithDelayLine(t *testing.T) {
	_, a, b := pair(t, 7)
	a.SetFaults(Faults{Reorder: 1, ReorderHold: time.Hour, Delay: time.Millisecond})
	write(t, a, frame(mac(2), mac(1), 1))
	write(t, a, frame(mac(2), mac(1), 2))
	if s1, s2 := seqOf(t, read(t, b)), seqOf(t, read(t, b)); s1 != 2 || s2 != 1 {
		t.Fatalf("order %d %d", s1, s2)
	}
	// The hold timer flushes through the delay line.
	a.SetFaults(Faults{Reorder: 1, ReorderHold: time.Millisecond, Delay: time.Millisecond})
	write(t, a, frame(mac(2), mac(1), 3))
	if s := seqOf(t, read(t, b)); s != 3 {
		t.Fatalf("got %d", s)
	}
}

func TestFaultDelay(t *testing.T) {
	_, a, b := pair(t, 7)
	const delay = 30 * time.Millisecond
	a.SetFaults(Faults{Delay: delay})
	start := time.Now()
	write(t, a, frame(mac(2), mac(1), 1))
	if seqOf(t, read(t, b)) != 1 {
		t.Fatal("wrong frame")
	}
	if el := time.Since(start); el < delay {
		t.Fatalf("delivered after %v, delay %v", el, delay)
	}
	// Delayed duplicates arrive too.
	a.SetFaults(Faults{Delay: time.Millisecond, Duplicate: 1})
	write(t, a, frame(mac(2), mac(1), 2))
	for i := 0; i < 2; i++ {
		if s := seqOf(t, read(t, b)); s != 2 {
			t.Fatalf("copy %d: got %d", i, s)
		}
	}
}

func TestFaultJitterKeepsOrder(t *testing.T) {
	_, a, b := pair(t, 7)
	a.SetFaults(Faults{Jitter: 3 * time.Millisecond})
	const total = 300
	for i := 0; i < total; i++ {
		write(t, a, frame(mac(2), mac(1), uint32(i)))
	}
	for i := 0; i < total; i++ {
		if s := seqOf(t, read(t, b)); s != uint32(i) {
			t.Fatalf("frame %d arrived as %d", s, i)
		}
	}
}

func TestCloseWithPendingFaults(t *testing.T) {
	// A held frame is discarded by Close.
	_, a, b := pair(t, 7)
	a.SetFaults(Faults{Reorder: 1, ReorderHold: time.Hour})
	write(t, a, frame(mac(2), mac(1), 1))
	_ = a.Close()
	a.flushHeld() // a late timer finds nothing
	if len(drain(b)) != 0 {
		t.Fatal("held frame delivered after Close")
	}

	// Close stops a delay line waiting for a frame's due time.
	_, c, d := pair(t, 8)
	c.SetFaults(Faults{Delay: time.Hour})
	write(t, c, frame(mac(2), mac(1), 1))
	closed := make(chan struct{})
	go func() { _ = c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close blocked on the delay line")
	}
	if len(drain(d)) != 0 {
		t.Fatal("delayed frame delivered after Close")
	}
}

func TestCloseUnblocksFullDelayLine(t *testing.T) {
	_, a, _ := pair(t, 7)
	a.SetFaults(Faults{Delay: time.Hour})
	errc := make(chan error, 1)
	go func() {
		for {
			if err := a.WriteFrame(context.Background(), frame(mac(2), mac(1), 0)); err != nil {
				errc <- err
				return
			}
		}
	}()
	// One frame waits in the line goroutine, 4096 in the channel, and the
	// next blocks in send.
	waitFor(t, "a full delay line", func() bool { return a.Stats().Sent >= 4098 })
	_ = a.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, profinet.ErrClosed) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("writer still blocked after Close")
	}
}

// decisions sends frames from a fresh endpoint with MAC m on a network
// with seed and returns which sequence numbers survived Loss 0.5 and
// which were corrupted.
func decisions(t *testing.T, seed uint64, noise bool) (delivered []uint32, corrupt []uint32) {
	n := New(seed)
	defer func() { _ = n.Close() }()
	a := newEP(t, n, EndpointConfig{MAC: mac(1)})
	b := newEP(t, n, EndpointConfig{MAC: mac(2), QueueLen: 1024})
	var other *Endpoint
	if noise {
		other = newEP(t, n, EndpointConfig{MAC: mac(3)})
		other.SetFaults(Faults{Loss: 0.5})
	}
	a.SetFaults(Faults{Loss: 0.5, Corrupt: 0.3})
	for i := 0; i < 500; i++ {
		orig := frame(mac(2), mac(1), uint32(i))
		write(t, a, orig)
		if other != nil {
			write(t, other, frame(mac(2), mac(3), 1<<31))
		}
		for _, f := range drain(b) {
			if f[6] != 2 || f[11] != 1 { // from a
				continue
			}
			delivered = append(delivered, uint32(i))
			if !bytes.Equal(f, orig) {
				corrupt = append(corrupt, uint32(i))
			}
		}
	}
	return delivered, corrupt
}

func equalU32(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDeterminism(t *testing.T) {
	d1, c1 := decisions(t, 1234, false)
	d2, c2 := decisions(t, 1234, true)
	if !equalU32(d1, d2) || !equalU32(c1, c2) {
		t.Fatal("same seed and frames gave different faults (another endpoint's traffic must not matter)")
	}
	if len(d1) == 0 || len(d1) == 500 || len(c1) == 0 {
		t.Fatalf("faults not applied: %d delivered, %d corrupted", len(d1), len(c1))
	}
	d3, c3 := decisions(t, 1235, false)
	if equalU32(d1, d3) && equalU32(c1, c3) {
		t.Fatal("different seeds gave identical faults")
	}
}

func TestConcurrentSenders(t *testing.T) {
	n := New(1)
	rx := newEP(t, n, EndpointConfig{MAC: mac(100), QueueLen: 4096})
	const senders, per = 8, 200
	var wg sync.WaitGroup
	for s := 0; s < senders; s++ {
		e := newEP(t, n, EndpointConfig{MAC: mac(byte(s + 1))})
		if s%2 == 0 {
			e.SetFaults(Faults{Jitter: time.Millisecond})
		}
		wg.Add(1)
		go func(e *Endpoint) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if err := e.WriteFrame(context.Background(), frame(ethernet.Broadcast, e.HardwareAddr(), uint32(i))); err != nil {
					t.Error(err)
					return
				}
			}
		}(e)
	}
	next := make(map[byte]uint32)
	for i := 0; i < senders*per; i++ {
		f := read(t, rx)
		src := f[11]
		if s := seqOf(t, f); s != next[src] {
			t.Fatalf("sender %d: got %d, want %d", src, s, next[src])
		}
		next[src]++
	}
	wg.Wait()
}

func BenchmarkDelivery(b *testing.B) {
	n := New(1)
	defer func() { _ = n.Close() }()
	tx, _ := n.NewEndpoint(EndpointConfig{MAC: mac(1)})
	rx, _ := n.NewEndpoint(EndpointConfig{MAC: mac(2)})
	f := frame(mac(2), mac(1), 1)
	buf := make([]byte, 1600)
	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(int64(len(f)))
	for i := 0; i < b.N; i++ {
		if err := tx.WriteFrame(ctx, f); err != nil {
			b.Fatal(err)
		}
		if _, err := rx.ReadFrame(ctx, buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBroadcast(b *testing.B) {
	n := New(1)
	defer func() { _ = n.Close() }()
	tx, _ := n.NewEndpoint(EndpointConfig{MAC: mac(1)})
	var rxs []*Endpoint
	for i := byte(2); i < 10; i++ {
		e, _ := n.NewEndpoint(EndpointConfig{MAC: mac(i)})
		rxs = append(rxs, e)
	}
	f := frame(ethernet.Broadcast, mac(1), 1)
	buf := make([]byte, 1600)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := tx.WriteFrame(ctx, f); err != nil {
			b.Fatal(err)
		}
		for _, e := range rxs {
			if _, err := e.ReadFrame(ctx, buf); err != nil {
				b.Fatal(err)
			}
		}
	}
}
