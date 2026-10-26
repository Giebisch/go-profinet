// SPDX-License-Identifier: MIT

package ethernet

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-profinet"
)

func TestEtherTypeString(t *testing.T) {
	cases := map[EtherType]string{
		EtherTypeIPv4:     "IPv4",
		EtherTypeARP:      "ARP",
		EtherTypeVLAN:     "VLAN",
		EtherTypeProfinet: "PROFINET",
		EtherTypeLLDP:     "LLDP",
		0x86DD:            "0x86DD",
		0x0001:            "0x0001",
	}
	for et, want := range cases {
		if got := et.String(); got != want {
			t.Errorf("%#04x: got %q, want %q", uint16(et), got, want)
		}
	}
}

func TestParseMAC(t *testing.T) {
	want := MAC{0x00, 0x0e, 0xcf, 0xAB, 0x12, 0xFF}
	for _, s := range []string{"00:0e:cf:ab:12:ff", "00-0E-CF-AB-12-FF", "000ecfab12ff", "000ECFAB12FF"} {
		got, err := ParseMAC(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if got != want {
			t.Fatalf("%q: got %v", s, got)
		}
	}
	for _, s := range []string{
		"", "00:0e:cf:ab:12", "00:0e:cf:ab:12:ff:00", "0:0e:cf:ab:12:ff", "000:e:cf:ab:12:ff",
		"zz:0e:cf:ab:12:ff", "00-0e-cf-ab-12", "000ecfab12f", "000ecfab12fg", "00:0e-cf:ab:12:ff",
		"+1:0e:cf:ab:12:ff",
	} {
		if _, err := ParseMAC(s); !errors.Is(err, ErrInvalidMAC) {
			t.Errorf("%q: err %v", s, err)
		}
	}
}

func TestMACStringAndText(t *testing.T) {
	m := MAC{0x01, 0x0E, 0xCF, 0x00, 0xA0, 0xFF}
	if m.String() != "01:0e:cf:00:a0:ff" {
		t.Fatalf("got %q", m.String())
	}
	b, err := json.Marshal(m)
	if err != nil || string(b) != `"01:0e:cf:00:a0:ff"` {
		t.Fatalf("marshal %s %v", b, err)
	}
	var back MAC
	if err := json.Unmarshal(b, &back); err != nil || back != m {
		t.Fatalf("unmarshal %v %v", back, err)
	}
	orig := back
	if err := back.UnmarshalText([]byte("bogus")); !errors.Is(err, ErrInvalidMAC) {
		t.Fatalf("err %v", err)
	}
	if back != orig {
		t.Fatal("failed UnmarshalText modified the MAC")
	}
}

func TestMACPredicates(t *testing.T) {
	cases := []struct {
		m                 MAC
		multi, bcast, zro bool
	}{
		{Broadcast, true, true, false},
		{DCPIdentifyMulticast, true, false, false},
		{DCPHelloMulticast, true, false, false},
		{LLDPMulticast, true, false, false},
		{MAC{}, false, false, true},
		{MAC{0x02, 0, 0, 0, 0, 1}, false, false, false},
		{MAC{0xFE, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, false, false, false},
	}
	for _, c := range cases {
		if c.m.IsMulticast() != c.multi || c.m.IsBroadcast() != c.bcast || c.m.IsZero() != c.zro {
			t.Errorf("%v: multicast=%v broadcast=%v zero=%v", c.m, c.m.IsMulticast(), c.m.IsBroadcast(), c.m.IsZero())
		}
	}
}

func TestVLANTag(t *testing.T) {
	for _, tag := range []VLANTag{
		{}, {Priority: 6, ID: 0}, {Priority: 7, DropEligible: true, ID: 4095}, {Priority: 3, ID: 100},
	} {
		if got := TagFromTCI(tag.TCI()); got != tag {
			t.Errorf("round trip %+v: %+v", tag, got)
		}
	}
	if got := (VLANTag{Priority: 6, ID: 1}).TCI(); got != 0xC001 {
		t.Fatalf("TCI %#04x", got)
	}
	if got := (VLANTag{DropEligible: true}).TCI(); got != 0x1000 {
		t.Fatalf("TCI %#04x", got)
	}
	// Out-of-range fields are masked.
	if got := (VLANTag{Priority: 0xFF, ID: 0xFFFF}).TCI(); got != 0xEFFF {
		t.Fatalf("TCI %#04x", got)
	}
}

func TestEncodeDecodeUntagged(t *testing.T) {
	f := Frame{
		Dst: DCPIdentifyMulticast, Src: MAC{2, 0, 0, 0, 0, 1},
		EtherType: EtherTypeProfinet, Payload: []byte{0xFE, 0xFE, 0x05, 0x00},
	}
	b := f.Encode()
	if len(b) != MinFrameLen {
		t.Fatalf("len %d", len(b))
	}
	if f.HeaderSize() != HeaderLen {
		t.Fatalf("header %d", f.HeaderSize())
	}
	want := []byte{1, 0x0E, 0xCF, 0, 0, 0, 2, 0, 0, 0, 0, 1, 0x88, 0x92, 0xFE, 0xFE, 0x05, 0x00}
	if !bytes.Equal(b[:len(want)], want) {
		t.Fatalf("header % x", b[:len(want)])
	}
	if !bytes.Equal(b[len(want):], make([]byte, MinFrameLen-len(want))) {
		t.Fatal("padding not zero")
	}
	g, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if g.Dst != f.Dst || g.Src != f.Src || g.Tagged || g.EtherType != f.EtherType {
		t.Fatalf("decoded %+v", g)
	}
	if len(g.Payload) != MinFrameLen-HeaderLen || !bytes.Equal(g.Payload[:4], f.Payload) {
		t.Fatalf("payload % x", g.Payload)
	}
	// Payload aliases the input.
	b[HeaderLen] = 0x11
	if g.Payload[0] != 0x11 {
		t.Fatal("payload does not alias the buffer")
	}
}

func TestEncodeDecodeTagged(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAA}, DefaultMTU)
	f := Frame{
		Dst: MAC{2, 0, 0, 0, 0, 2}, Src: MAC{2, 0, 0, 0, 0, 1}, Tagged: true,
		VLAN: VLANTag{Priority: 6, ID: 42}, EtherType: EtherTypeIPv4, Payload: payload,
	}
	b := f.Encode()
	if len(b) != MaxTaggedFrameLen || f.HeaderSize() != HeaderLen+VLANTagLen {
		t.Fatalf("len %d header %d", len(b), f.HeaderSize())
	}
	g, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Tagged || g.VLAN != f.VLAN || g.EtherType != EtherTypeIPv4 || !bytes.Equal(g.Payload, payload) {
		t.Fatalf("decoded %+v", g)
	}
	et, off, ok := PeekEtherType(b)
	if !ok || et != EtherTypeIPv4 || off != HeaderLen+VLANTagLen {
		t.Fatalf("peek %v %d %v", et, off, ok)
	}
	// AppendTo appends after existing content and pads relative to it.
	prefix := []byte{9, 9, 9}
	out := (&Frame{EtherType: EtherTypeARP}).AppendTo(prefix)
	if len(out) != 3+MinFrameLen || !bytes.Equal(out[:3], prefix) {
		t.Fatalf("AppendTo len %d", len(out))
	}
}

func TestDecodeQinQ(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 0x81, 0x00, 0x00, 0x05, 0x81, 0x00, 0x00, 0x07, 0x08, 0x00}
	f, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Tagged || f.VLAN.ID != 5 || f.EtherType != EtherTypeVLAN || len(f.Payload) != 4 {
		t.Fatalf("%+v", f)
	}
}

func TestDecodeErrors(t *testing.T) {
	cases := [][]byte{
		nil,
		make([]byte, HeaderLen-1),
		append(make([]byte, 12), 0x81, 0x00), // tag missing
		append(make([]byte, 12), 0x81, 0x00, 0, 1, 0x08), // tag truncated
		append(make([]byte, 12), 0x05, 0xFF),             // 802.3 length 1535
		append(make([]byte, 12), 0x81, 0x00, 0, 1, 0x00, 0x2E),
	}
	for i, b := range cases {
		if _, err := Decode(b); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("case %d: err %v", i, err)
		}
	}
	// 0x0600 is the smallest EtherType.
	if _, err := Decode(append(make([]byte, 12), 0x06, 0x00)); err != nil {
		t.Fatalf("0x0600: %v", err)
	}
}

func TestPeekEtherType(t *testing.T) {
	if _, _, ok := PeekEtherType(make([]byte, 13)); ok {
		t.Fatal("short frame accepted")
	}
	if _, _, ok := PeekEtherType(append(make([]byte, 12), 0x81, 0x00, 0, 0, 0x08)); ok {
		t.Fatal("truncated tag accepted")
	}
	et, off, ok := PeekEtherType(append(make([]byte, 12), 0x88, 0xCC))
	if !ok || et != EtherTypeLLDP || off != HeaderLen {
		t.Fatalf("%v %d %v", et, off, ok)
	}
	// Peek does not validate the EtherType range, unlike Decode.
	et, _, ok = PeekEtherType(append(make([]byte, 12), 0x00, 0x2E))
	if !ok || et != 0x2E {
		t.Fatalf("%v %v", et, ok)
	}
}

func TestPad(t *testing.T) {
	b := Pad([]byte{1, 2}, 0)
	if len(b) != MinFrameLen || b[0] != 1 || b[59] != 0 {
		t.Fatalf("len %d", len(b))
	}
	long := make([]byte, 100)
	if got := Pad(long, 0); len(got) != 100 {
		t.Fatalf("long frame padded to %d", len(got))
	}
	if got := Pad(make([]byte, 50), 10); len(got) != 70 {
		t.Fatalf("offset pad %d", len(got))
	}
}

func FuzzDecode(f *testing.F) {
	f.Add((&Frame{Dst: Broadcast, Src: MAC{2, 0, 0, 0, 0, 1}, EtherType: EtherTypeARP}).Encode())
	f.Add((&Frame{Dst: DCPIdentifyMulticast, Tagged: true, VLAN: VLANTag{Priority: 6}, EtherType: EtherTypeProfinet, Payload: []byte{0xFE, 0xFE}}).Encode())
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 0x81, 0x00, 0x00, 0x05, 0x81, 0x00, 0x00, 0x07, 0x08, 0x00})
	f.Add(make([]byte, 13))
	f.Add(append(make([]byte, 12), 0x81, 0x00))
	f.Add(append(make([]byte, 12), 0x05, 0xFF))
	f.Fuzz(func(t *testing.T, b []byte) {
		et, off, ok := PeekEtherType(b)
		fr, err := Decode(b)
		if err != nil {
			if !errors.Is(err, profinet.ErrMalformed) {
				t.Fatalf("error %v does not match ErrMalformed", err)
			}
			// Decode only rejects what Peek rejects or 802.3 lengths.
			if ok && et >= 0x0600 {
				t.Fatalf("Decode failed (%v) but Peek returned %v", err, et)
			}
			return
		}
		if !ok || et != fr.EtherType || off != fr.HeaderSize() {
			t.Fatalf("peek %v %d %v vs decode %v %d", et, off, ok, fr.EtherType, fr.HeaderSize())
		}
		if len(fr.Payload) != len(b)-off {
			t.Fatalf("payload len %d of %d", len(fr.Payload), len(b))
		}
		enc := fr.Encode()
		if len(b) >= MinFrameLen {
			// Frames needing no padding re-encode to the same bytes:
			// every TCI bit round-trips.
			if !bytes.Equal(enc, b) {
				t.Fatalf("re-encode differs:\n% x\n% x", b, enc)
			}
		} else if len(enc) != MinFrameLen || !bytes.Equal(enc[:len(b)], b) {
			t.Fatalf("short re-encode differs:\n% x\n% x", b, enc)
		}
		g, err := Decode(enc)
		if err != nil {
			t.Fatalf("re-decode: %v", err)
		}
		if g.Dst != fr.Dst || g.Src != fr.Src || g.Tagged != fr.Tagged || g.VLAN != fr.VLAN || g.EtherType != fr.EtherType {
			t.Fatalf("re-decode %+v vs %+v", g, fr)
		}
	})
}

var sinkFrame Frame

func BenchmarkEncode(b *testing.B) {
	f := Frame{
		Dst: MAC{2, 0, 0, 0, 0, 2}, Src: MAC{2, 0, 0, 0, 0, 1}, Tagged: true,
		VLAN: VLANTag{Priority: 6}, EtherType: EtherTypeProfinet, Payload: make([]byte, 40),
	}
	buf := make([]byte, 0, MaxTaggedFrameLen)
	b.ReportAllocs()
	b.SetBytes(int64(len(f.Encode())))
	for i := 0; i < b.N; i++ {
		buf = f.AppendTo(buf[:0])
	}
}

func BenchmarkEncodeAlloc(b *testing.B) {
	f := Frame{Dst: Broadcast, Src: MAC{2, 0, 0, 0, 0, 1}, EtherType: EtherTypeIPv4, Payload: make([]byte, 1000)}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = f.Encode()
	}
}

func BenchmarkDecode(b *testing.B) {
	raw := (&Frame{
		Dst: MAC{2, 0, 0, 0, 0, 2}, Src: MAC{2, 0, 0, 0, 0, 1}, Tagged: true,
		VLAN: VLANTag{Priority: 6}, EtherType: EtherTypeProfinet, Payload: make([]byte, 40),
	}).Encode()
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		f, err := Decode(raw)
		if err != nil {
			b.Fatal(err)
		}
		sinkFrame = f
	}
}
