// SPDX-License-Identifier: MIT

//go:build darwin || freebsd || netbsd || dragonfly

package bpf

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// pair opens the ends of a link named by PROFINET_L2_IFACE_A and _B (a
// feth pair on macOS: ifconfig feth0 create; ifconfig feth1 create;
// ifconfig feth0 peer feth1). Without them the tests are skipped.
func pair(t *testing.T) (*Port, *Port) {
	t.Helper()
	a, b := os.Getenv("PROFINET_L2_IFACE_A"), os.Getenv("PROFINET_L2_IFACE_B")
	if a == "" || b == "" {
		t.Skip("real Layer 2 test: set PROFINET_L2_IFACE_A and PROFINET_L2_IFACE_B")
	}
	pa, err := Open(a, Options{})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Open(b, Options{})
	if err != nil {
		pa.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pa.Close()
		pb.Close()
	})
	return pa, pb
}

func TestExchange(t *testing.T) {
	a, b := pair(t)
	f := ethernet.Frame{Dst: b.HardwareAddr(), Src: a.HardwareAddr(), EtherType: ethernet.EtherTypeProfinet, Payload: []byte{0xFE, 0xFD, 7}}
	if err := a.WriteFrame(context.Background(), f.Encode()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	buf := make([]byte, 2000)
	for {
		n, err := b.ReadFrame(ctx, buf)
		if err != nil {
			t.Fatal(err)
		}
		g, err := ethernet.Decode(buf[:n])
		if err == nil && g.EtherType == ethernet.EtherTypeProfinet && bytes.HasPrefix(g.Payload, f.Payload) {
			break
		}
	}
	if !a.LinkUp() || a.MTU() < 1500 || a.Name() == "" {
		t.Fatal("link")
	}
	a.Close()
	if _, err := a.ReadFrame(context.Background(), buf); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if err := a.WriteFrame(context.Background(), f.Encode()); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open("no-such-if0", Options{}); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}

// record encodes a BPF record as the kernel lays it out.
func record(frame []byte, caplen int) []byte {
	b := make([]byte, wordAlign(hdrLen-2+caplen))
	var h syscall.BpfHdr
	binary.NativeEndian.PutUint32(b[unsafe.Offsetof(h.Caplen):], uint32(caplen))
	binary.NativeEndian.PutUint32(b[unsafe.Offsetof(h.Datalen):], uint32(len(frame)))
	binary.NativeEndian.PutUint16(b[unsafe.Offsetof(h.Hdrlen):], uint16(hdrLen-2))
	copy(b[hdrLen-2:], frame[:caplen])
	return b
}

func TestNextRecord(t *testing.T) {
	f1 := bytes.Repeat([]byte{1}, 61)
	f2 := bytes.Repeat([]byte{2}, 64)
	buf := append(record(f1, len(f1)), record(f2, 10)...) // f2 truncated by the snapshot
	buf = append(buf, record(f2, len(f2))...)
	rec, rest, ok := nextRecord(buf)
	if !ok || !bytes.Equal(rec, f1) {
		t.Fatalf("first %v %x", ok, rec)
	}
	rec, rest, ok = nextRecord(rest)
	if !ok || rec != nil {
		t.Fatalf("truncated record not skipped: %v %x", ok, rec)
	}
	rec, rest, ok = nextRecord(rest)
	if !ok || !bytes.Equal(rec, f2) || len(rest) != 0 {
		t.Fatalf("third %v %x %d", ok, rec, len(rest))
	}
	if _, _, ok := nextRecord(rest); ok {
		t.Fatal("empty buffer")
	}
	bad := record(f1, len(f1))
	var h syscall.BpfHdr
	binary.NativeEndian.PutUint32(bad[unsafe.Offsetof(h.Caplen):], 5000)
	if _, _, ok := nextRecord(bad); ok {
		t.Fatal("record beyond the buffer")
	}
}
