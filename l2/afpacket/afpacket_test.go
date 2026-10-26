// SPDX-License-Identifier: MIT

//go:build linux

package afpacket

import (
	"bytes"
	"context"
	"errors"
	"os"
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
