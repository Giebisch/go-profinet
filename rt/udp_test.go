// SPDX-License-Identifier: MIT

package rt

import (
	"bytes"
	"errors"
	"testing"

	"github.com/otfabric/go-profinet"
)

// The UDP payload of an RT_CLASS_UDP frame is the RT payload from the
// FrameID on, as Wireshark dissects UDP port 0x8892, without padding.
const goldenUDP = "c000 0102030405060708090a0b0c 0100 35 00"

func TestUDPPayloadGolden(t *testing.T) {
	f := Frame{FrameID: 0xC000, Data: hx("0102030405060708090a0b0c"), CycleCounter: 0x0100, DataStatus: DefaultDataStatus}
	got, err := AppendUDPPayload(hx("ff"), &f)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(hx("ff"), hx(goldenUDP)...); !bytes.Equal(got, want) {
		t.Fatalf("payload %x, want %x", got, want)
	}
	d, err := DecodeUDP(hx(goldenUDP), MinDataLengthUDP)
	if err != nil || d.FrameID != 0xC000 || !bytes.Equal(d.Data, f.Data) || d.CycleCounter != 0x0100 || d.DataStatus != DefaultDataStatus {
		t.Fatalf("%+v %v", d, err)
	}
	if d, err = DecodeUDP(hx(goldenUDP), -1); err != nil || len(d.Data) != 12 {
		t.Fatalf("%+v %v", d, err)
	}
	// Multicast FrameIDs are RT_CLASS_UDP too.
	if _, err := DecodeUDP(hx("f800 00 0000 35 00"), 1); err != nil {
		t.Fatal(err)
	}
}

func TestUDPPayloadErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		p    string
		n    int
	}{
		{"short", "c000 0001 35", -1},
		{"RT_CLASS_2 FrameID", "8000 00 0000 35 00", -1},
		{"DCP FrameID", "fefe 00 0000 35 00", -1},
		{"length mismatch", goldenUDP, 11},
	} {
		if _, err := DecodeUDP(hx(c.p), c.n); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	big := make([]byte, 2+MaxDataLength+1+TrailerLen)
	big[0] = 0xC0
	if _, err := DecodeUDP(big, -1); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("oversized: %v", err)
	}
	if _, err := AppendUDPPayload(nil, &Frame{FrameID: 0x8000}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Errorf("RT_CLASS_2 FrameID: %v", err)
	}
	if _, err := AppendUDPPayload(nil, &Frame{FrameID: 0xC000, Data: make([]byte, MaxDataLength+1)}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Errorf("oversized: %v", err)
	}
	for id, want := range map[uint16]bool{0xBFFF: false, 0xC000: true, 0xF7FF: true, 0xF800: true, 0xFBFF: true, 0xFC01: false} {
		if IsUDPFrameID(id) != want {
			t.Errorf("IsUDPFrameID(0x%04X) = %v", id, !want)
		}
	}
}

func FuzzDecodeUDP(f *testing.F) {
	f.Add(hx(goldenUDP), -1)
	f.Add(hx(goldenUDP), 12)
	f.Add(hx("f800 0000 35 00"), 0)
	f.Add([]byte{0xc0}, -1)
	f.Fuzz(func(t *testing.T, p []byte, n int) {
		fr, err := DecodeUDP(p, n)
		if err != nil {
			return
		}
		out, err := AppendUDPPayload(nil, &fr)
		if err != nil || !bytes.Equal(out, p) {
			t.Fatalf("re-encoded %x (%v) from %x", out, err, p)
		}
	})
}
