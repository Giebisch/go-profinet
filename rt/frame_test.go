// SPDX-License-Identifier: MIT

package rt

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

func hx(s string) []byte {
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		panic(err)
	}
	return b
}

var (
	macA = ethernet.MAC{0x02, 0, 0, 0, 0, 0x0a}
	macB = ethernet.MAC{0x02, 0, 0, 0, 0, 0x0b}
)

func TestAppendPayload(t *testing.T) {
	f := Frame{FrameID: 0xC000, Data: hx("010203"), CycleCounter: 0x1234, DataStatus: DefaultDataStatus, TransferStatus: 0}
	got := AppendPayload(hx("ff"), &f)
	if want := hx("ff c000 010203 1234 35 00"); !bytes.Equal(got, want) {
		t.Fatalf("payload %x, want %x", got, want)
	}
}

func TestAppendEthernet(t *testing.T) {
	f := Frame{FrameID: 0x8001, Data: hx("aabb"), CycleCounter: 0x0020, DataStatus: 0x25, TransferStatus: 0x01}
	got := AppendEthernet(nil, macB, macA, 5, &f)
	want := hx(`02000000000b 02000000000a 8100 c005 8892
		8001 aabb 0020 25 01`)
	want = append(want, make([]byte, ethernet.MinFrameLen-len(want))...)
	if !bytes.Equal(got, want) {
		t.Fatalf("frame\n got %x\nwant %x", got, want)
	}
	// Priority 6 on VLAN 0 is a priority-tagged frame.
	got = AppendEthernet(hx("ffff"), macB, macA, 0, &f)
	if !bytes.Equal(got[2+12:2+18], hx("8100 c000 8892")) {
		t.Fatalf("priority tag %x", got[14:20])
	}
	if len(got) != 2+ethernet.MinFrameLen {
		t.Fatalf("padding is not relative to the frame start: %d", len(got))
	}
	// A frame beyond the minimum is not padded.
	f.Data = make([]byte, MinDataLength)
	got = AppendEthernet(nil, macB, macA, 0, &f)
	if len(got) != 18+2+MinDataLength+TrailerLen {
		t.Fatalf("%d octets", len(got))
	}
	e, err := ethernet.Decode(got)
	if err != nil || !e.Tagged || e.VLAN.Priority != VLANPriority || e.EtherType != ethernet.EtherTypeProfinet || e.Dst != macB || e.Src != macA {
		t.Fatalf("decoded %+v, %v", e, err)
	}
	d, err := Decode(e.Payload, MinDataLength)
	if err != nil || d.FrameID != 0x8001 || d.CycleCounter != 0x20 || d.DataStatus != 0x25 || d.TransferStatus != 1 {
		t.Fatalf("payload %+v, %v", d, err)
	}
}

func TestDecode(t *testing.T) {
	p := hx("c001 01020304 abcd 35 00")
	f, err := Decode(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	if f.FrameID != 0xC001 || !bytes.Equal(f.Data, hx("01020304")) || f.CycleCounter != 0xABCD || f.DataStatus != 0x35 || f.TransferStatus != 0 {
		t.Fatalf("frame %+v", f)
	}
	// Data aliases the input.
	p[2] = 9
	if f.Data[0] != 9 {
		t.Fatal("Data does not alias")
	}
	// Inferred length.
	f2, err := Decode(p, -1)
	if err != nil || len(f2.Data) != 4 || f2.CycleCounter != 0xABCD {
		t.Fatalf("inferred %+v, %v", f2, err)
	}
	// An explicit length ignores the Ethernet padding after the trailer.
	padded := append(append([]byte(nil), p...), 0, 0, 0)
	f3, err := Decode(padded, 4)
	if err != nil || f3.CycleCounter != 0xABCD || f3.DataStatus != 0x35 {
		t.Fatalf("padded %+v, %v", f3, err)
	}
	// An empty C_SDU.
	if f, err := Decode(hx("c001 0001 35 00"), -1); err != nil || len(f.Data) != 0 || f.CycleCounter != 1 {
		t.Fatalf("empty %+v, %v", f, err)
	}
	if f, err := Decode(hx("c001 0001 35 00"), 0); err != nil || len(f.Data) != 0 {
		t.Fatalf("empty explicit %+v, %v", f, err)
	}

	for name, c := range map[string]struct {
		p []byte
		n int
	}{
		"inferred too short": {hx("c001 0001 35"), -1},
		"empty":              {nil, -1},
		"explicit too short": {hx("c001 0102 0001 35 00"), 3},
		"no trailer":         {hx("c001 01020304"), 4},
		"empty explicit":     {nil, 0},
	} {
		_, err := Decode(c.p, c.n)
		if !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDataStatus(t *testing.T) {
	if DefaultDataStatus != 0x35 {
		t.Fatalf("DefaultDataStatus 0x%02X", uint8(DefaultDataStatus))
	}
	for _, c := range []struct {
		s                                DataStatus
		valid, running, primary, problem bool
		str                              string
	}{
		{DefaultDataStatus, true, true, true, false, "0x35 primary valid run"},
		{0x00, false, false, false, true, "0x00 backup invalid stop problem"},
		{0x25, true, false, true, false, "0x25 primary valid stop"},
		{StationOK | IgnoreDataStatus | Redundancy, false, false, false, false, "0xA2 backup invalid stop"},
		{DataValid | ProviderRun, true, true, false, true, "0x14 backup valid run problem"},
	} {
		if c.s.Valid() != c.valid || c.s.Running() != c.running || c.s.Primary() != c.primary || c.s.StationProblem() != c.problem {
			t.Errorf("0x%02X: valid %v running %v primary %v problem %v", uint8(c.s), c.s.Valid(), c.s.Running(), c.s.Primary(), c.s.StationProblem())
		}
		if got := c.s.String(); got != c.str {
			t.Errorf("0x%02X: String %q, want %q", uint8(c.s), got, c.str)
		}
	}
}

func TestCounterNewer(t *testing.T) {
	for _, c := range []struct {
		c, prev uint16
		want    bool
	}{
		{1, 0, true},
		{0, 0, false},
		{0xFFFF, 0xFFFF, false},
		{0x7FFF, 0, true},
		{0x8000, 0, false},
		{0, 1, false},
		{0, 0xFFFF, true},
		{0x0010, 0xFFF0, true},
		{0xFFF0, 0x0010, false},
		{0x7FFE, 0xFFFF, true},
		{0x7FFF, 0xFFFF, false},
		{32, 0, true},
		{0x8020, 0x0020, false},
		{0x801F, 0x0020, true},
	} {
		if got := CounterNewer(c.c, c.prev); got != c.want {
			t.Errorf("CounterNewer(0x%04X, 0x%04X) = %v", c.c, c.prev, got)
		}
	}
}

func TestCycleNanos(t *testing.T) {
	for _, c := range []struct {
		scf, rr uint16
		want    int64
	}{
		{1, 1, 31250},
		{32, 1, 1_000_000},
		{32, 4, 4_000_000},
		{128, 512, 2_048_000_000},
		{0xFFFF, 0xFFFF, 0xFFFF * 0xFFFF * 31250},
		{0, 8, 0},
	} {
		if got := CycleNanos(c.scf, c.rr); got != c.want {
			t.Errorf("CycleNanos(%d, %d) = %d, want %d", c.scf, c.rr, got, c.want)
		}
	}
}
