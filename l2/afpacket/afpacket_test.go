// SPDX-License-Identifier: MIT

//go:build linux

package afpacket

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// pair opens the two ends of a veth pair named by PROFINET_L2_IFACE_A and
// PROFINET_L2_IFACE_B (scripts/l2-test.sh creates them in a container with
// CAP_NET_RAW). Without them the real Layer 2 tests are skipped.
func pair(t *testing.T) (*Port, *Port) {
	t.Helper()
	a, b := os.Getenv("PROFINET_L2_IFACE_A"), os.Getenv("PROFINET_L2_IFACE_B")
	if a == "" || b == "" {
		t.Skip("real Layer 2 test: set PROFINET_L2_IFACE_A and PROFINET_L2_IFACE_B (see scripts/l2-test.sh)")
	}
	pa, err := Open(a, Options{})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Open(b, Options{})
	if err != nil {
		_ = pa.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pa.Close()
		_ = pb.Close()
	})
	return pa, pb
}

// recv reads frames until one with the EtherType arrives.
func recv(t *testing.T, p *Port, et ethernet.EtherType) ethernet.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	buf := make([]byte, 2000)
	for {
		n, err := p.ReadFrame(ctx, buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		f, err := ethernet.Decode(append([]byte(nil), buf[:n]...))
		if err == nil && f.EtherType == et {
			return f
		}
	}
}

func TestUnicastMulticastAndTags(t *testing.T) {
	a, b := pair(t)
	ctx := context.Background()
	// Unicast.
	f := ethernet.Frame{Dst: b.HardwareAddr(), Src: a.HardwareAddr(), EtherType: ethernet.EtherTypeProfinet, Payload: []byte{0xFE, 0xFD, 1, 2, 3}}
	if err := a.WriteFrame(ctx, f.Encode()); err != nil {
		t.Fatal(err)
	}
	got := recv(t, b, ethernet.EtherTypeProfinet)
	if got.Src != a.HardwareAddr() || !bytes.HasPrefix(got.Payload, f.Payload) {
		t.Fatalf("unicast %+v", got)
	}
	// The DCP multicast arrives without joining (all-multicast).
	f.Dst = ethernet.DCPIdentifyMulticast
	_ = a.WriteFrame(ctx, f.Encode())
	if got := recv(t, b, ethernet.EtherTypeProfinet); got.Dst != ethernet.DCPIdentifyMulticast {
		t.Fatalf("multicast %+v", got)
	}
	// A priority-tagged frame keeps its tag, which the kernel strips and
	// the port restores from the auxiliary data.
	tagged := ethernet.Frame{Dst: b.HardwareAddr(), Src: a.HardwareAddr(), Tagged: true, VLAN: ethernet.VLANTag{Priority: 6}, EtherType: ethernet.EtherTypeLLDP, Payload: []byte{9, 9}}
	_ = a.WriteFrame(ctx, tagged.Encode())
	got = recv(t, b, ethernet.EtherTypeLLDP)
	if !got.Tagged || got.VLAN.Priority != 6 {
		t.Fatalf("tag lost: %+v", got)
	}
	if err := b.JoinMulticast(ethernet.LLDPMulticast); err != nil {
		t.Fatal(err)
	}
	if err := b.LeaveMulticast(ethernet.LLDPMulticast); err != nil {
		t.Fatal(err)
	}
	if !a.LinkUp() || a.MTU() < 1500 || a.Name() == "" {
		t.Fatalf("link %v mtu %d", a.LinkUp(), a.MTU())
	}
}

func TestOwnFramesAndClose(t *testing.T) {
	a, _ := pair(t)
	// A frame a sends is not read back on a.
	f := ethernet.Frame{Dst: ethernet.Broadcast, Src: a.HardwareAddr(), EtherType: 0x88B5, Payload: []byte{1}}
	_ = a.WriteFrame(context.Background(), f.Encode())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	buf := make([]byte, 2000)
	for {
		n, err := a.ReadFrame(ctx, buf)
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err %v", err)
			}
			break
		}
		if g, err := ethernet.Decode(buf[:n]); err == nil && g.EtherType == 0x88B5 {
			t.Fatal("read our own frame")
		}
	}
	// A read blocked in Close ends with ErrClosed. Frames that arrive on
	// the link before (the kernel sends one now and then) are read past.
	done := make(chan error)
	go func() {
		for {
			if _, err := a.ReadFrame(context.Background(), buf); err != nil {
				done <- err
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	_ = a.Close()
	if err := <-done; !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("read after close: %v", err)
	}
	if err := a.WriteFrame(context.Background(), f.Encode()); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open("no-such-if0", Options{}); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if _, err := Open("lo", Options{}); !errors.Is(err, profinet.ErrUnsupported) && err == nil {
		t.Fatal("loopback accepted")
	}
}

func TestInsertTag(t *testing.T) {
	buf := make([]byte, 20)
	copy(buf, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 0x88, 0x92, 0xAA, 0xBB})
	got := insertTag(buf, 16, 0x8100, 0xC000)
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 0x81, 0x00, 0xC0, 0x00, 0x88, 0x92, 0xAA, 0xBB}
	if !bytes.Equal(got, want) {
		t.Fatalf("% x", got)
	}
	if _, _, ok := vlanFromAux([]byte{1, 2}); ok {
		t.Fatal("garbage aux data")
	}
}

// TestNoAllocations: once warmed up, a write and a read of a frame
// allocate nothing (race builds instrument allocations: skipped there).
func TestNoAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates")
	}
	a, b := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := ethernet.Frame{Dst: b.HardwareAddr(), Src: a.HardwareAddr(), EtherType: 0x88B5, Payload: make([]byte, 46)}
	frame := f.Encode()
	buf := make([]byte, 1600)
	roundTrip := func() {
		if err := a.WriteFrame(ctx, frame); err != nil {
			t.Fatal(err)
		}
		for {
			n, err := b.ReadFrame(ctx, buf)
			if err != nil {
				t.Fatal(err)
			}
			if n >= 14 && buf[12] == 0x88 && buf[13] == 0xB5 {
				return
			}
		}
	}
	roundTrip()
	if n := testing.AllocsPerRun(200, roundTrip); n != 0 {
		t.Fatalf("%v allocations per write and read", n)
	}
	if n := testing.AllocsPerRun(100, func() { _ = a.LinkUp() }); n != 0 {
		t.Fatalf("%v allocations per LinkUp", n)
	}
}

// TestContextChanges: a read whose context ended leaves nothing behind for
// the next read with another context; a cancelled context interrupts a
// read that blocks.
func TestContextChanges(t *testing.T) {
	a, b := pair(t)
	buf := make([]byte, 2000)
	// A cancelled context ends the read at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.ReadFrame(ctx, buf); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	// A context cancelled while the read blocks interrupts it.
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel2() }()
	start := time.Now()
	for {
		if _, err := b.ReadFrame(ctx2, buf); err != nil {
			if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
				t.Fatalf("interrupted read: %v after %s", err, time.Since(start))
			}
			break
		}
	}
	// The next read, without a deadline, still receives.
	f := ethernet.Frame{Dst: b.HardwareAddr(), Src: a.HardwareAddr(), EtherType: 0x88B5, Payload: []byte{7}}
	go func() { time.Sleep(50 * time.Millisecond); _ = a.WriteFrame(context.Background(), f.Encode()) }()
	got := recv(t, b, 0x88B5)
	if got.Payload[0] != 7 {
		t.Fatalf("after cancellation: %+v", got)
	}
	// A context with a deadline, then one without.
	ctx3, cancel3 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel3()
	for {
		if _, err := b.ReadFrame(ctx3, buf); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline: %v", err)
			}
			break
		}
	}
	go func() { time.Sleep(50 * time.Millisecond); _ = a.WriteFrame(context.Background(), f.Encode()) }()
	if got := recv(t, b, 0x88B5); got.Payload[0] != 7 {
		t.Fatalf("after a deadline: %+v", got)
	}
}

// TestLinkDown: taking the peer down clears the link (IFF_RUNNING).
func TestLinkDown(t *testing.T) {
	a, b := pair(t)
	if !a.LinkUp() {
		t.Fatal("link down at start")
	}
	if out, err := exec.Command("ip", "link", "set", b.Name(), "down").CombinedOutput(); err != nil {
		t.Skipf("cannot set the link down: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "link", "set", b.Name(), "up").Run() })
	deadline := time.Now().Add(2 * time.Second)
	for a.LinkUp() {
		if time.Now().After(deadline) {
			t.Fatal("link still up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b.LinkUp() {
		t.Fatal("the downed end reports its link up")
	}
	if err := exec.Command("ip", "link", "set", b.Name(), "up").Run(); err != nil {
		t.Fatal(err)
	}
	for !a.LinkUp() {
		if time.Now().After(deadline.Add(2 * time.Second)) {
			t.Fatal("link not back")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
