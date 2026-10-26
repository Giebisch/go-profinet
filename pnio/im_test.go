// SPDX-License-Identifier: MIT

package pnio

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func testIM0() IM0 {
	return IM0{
		VendorID:         0x002A,
		OrderID:          "6ES7 123",
		SerialNumber:     "S123",
		HardwareRevision: 3,
		SoftwareRevision: SoftwareRevision{Prefix: 'V', FunctionalEnhance: 1, BugFix: 2, InternalChange: 3},
		RevisionCounter:  7,
		ProfileID:        0xF600,
		ProfileType:      0x0004,
		VersionMajor:     1,
		VersionMinor:     1,
		Supported:        IMSupportIM1 | IMSupportIM2 | IMSupportIM3 | IMSupportIM4,
	}
}

func TestIM0(t *testing.T) {
	m := testIM0()
	b := m.Append(nil)
	// BlockLength 56, 60 octets on the wire.
	want := hx("0020 0038 0100 002a") // header and VendorID
	want = append(want, []byte("6ES7 123            ")...)
	want = append(want, []byte("S123            ")...)
	want = append(want, hx("0003 56 01 02 03 0007 f600 0004 01 01 001e")...)
	checkGolden(t, "I&M0", b, want)
	if len(b) != 60 {
		t.Fatalf("%d octets", len(b))
	}
	// The offsets of the reference layout.
	if b[46] != 'V' || b[56] != 1 || b[57] != 1 || !bytes.Equal(b[58:60], hx("001e")) || !bytes.Equal(b[44:46], hx("0003")) {
		t.Fatalf("layout %s", hex.EncodeToString(b))
	}
	blk := readOne(t, b)
	got, err := DecodeIM0(blk)
	if err != nil || got != m {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	if m.SoftwareRevision.String() != "V1.2.3" {
		t.Fatalf("SoftwareRevision %s", m.SoftwareRevision)
	}

	// A zero prefix is sent as 'V'.
	m.SoftwareRevision.Prefix = 0
	got, err = DecodeIM0(readOne(t, m.Append(nil)))
	if err != nil || got.SoftwareRevision.Prefix != 'V' {
		t.Fatalf("prefix %q, %v", got.SoftwareRevision.Prefix, err)
	}
	// Long strings are truncated to the field.
	m.OrderID = strings.Repeat("O", 25)
	m.SerialNumber = strings.Repeat("S", 17)
	b = m.Append(nil)
	if len(b) != 60 {
		t.Fatalf("%d octets with long strings", len(b))
	}
	got, err = DecodeIM0(readOne(t, b))
	if err != nil || got.OrderID != strings.Repeat("O", 20) || got.SerialNumber != strings.Repeat("S", 16) {
		t.Fatalf("truncation %+v, %v", got, err)
	}

	bad := blk
	bad.Header.Type = BlockIM1
	wantMalformed(t, decodeErr(DecodeIM0, bad))
	wantMalformed(t, decodeErr(DecodeIM0, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeIM0, trailing(blk)))
}

func TestIM1to4(t *testing.T) {
	im1 := IM1{TagFunction: "pump", TagLocation: "hall 3"}
	b := im1.Append(nil)
	want := append(hx("0021 0038 0100"), []byte("pump"+strings.Repeat(" ", 28)+"hall 3"+strings.Repeat(" ", 16))...)
	checkGolden(t, "I&M1", b, want)
	if got, err := DecodeIM1(readOne(t, b)); err != nil || got != im1 {
		t.Fatalf("I&M1 round trip %+v, %v", got, err)
	}

	im2 := IM2{Date: "2026-10-25 12:00"}
	b = im2.Append(nil)
	checkGolden(t, "I&M2", b, append(hx("0022 0012 0100"), []byte("2026-10-25 12:00")...))
	if got, err := DecodeIM2(readOne(t, b)); err != nil || got != im2 {
		t.Fatalf("I&M2 round trip %+v, %v", got, err)
	}

	im3 := IM3{Descriptor: "spare"}
	b = im3.Append(nil)
	checkGolden(t, "I&M3", b, append(hx("0023 0038 0100"), []byte("spare"+strings.Repeat(" ", 49))...))
	if got, err := DecodeIM3(readOne(t, b)); err != nil || got != im3 {
		t.Fatalf("I&M3 round trip %+v, %v", got, err)
	}

	var im4 IM4
	im4.Signature[0], im4.Signature[53] = 0xAB, 0xCD
	b = im4.Append(nil)
	if len(b) != 60 || !bytes.Equal(b[:6], hx("0024 0038 0100")) || b[6] != 0xAB || b[59] != 0xCD || b[7] != 0 {
		t.Fatalf("I&M4 %x", b)
	}
	if got, err := DecodeIM4(readOne(t, b)); err != nil || got != im4 {
		t.Fatalf("I&M4 round trip %+v, %v", got, err)
	}

	// Trailing NULs of the visible strings are dropped too.
	raw := RawBlock{Header: BlockHeader{Type: BlockIM2, Length: 18, VersionHigh: 1}, Body: append([]byte("2026-10-25"), 0, 0, 0, 0, 0, 0)}
	if got, err := DecodeIM2(raw); err != nil || got.Date != "2026-10-25" {
		t.Fatalf("NUL padding %+v, %v", got, err)
	}
	// Long strings are truncated.
	b = (&IM3{Descriptor: strings.Repeat("d", 60)}).Append(nil)
	if got, err := DecodeIM3(readOne(t, b)); err != nil || got.Descriptor != strings.Repeat("d", 54) {
		t.Fatalf("truncation %+v, %v", got, err)
	}
}

func TestIMDecodeErrors(t *testing.T) {
	blocks := map[BlockType]RawBlock{
		BlockIM1: readOne(t, (&IM1{}).Append(nil)),
		BlockIM2: readOne(t, (&IM2{}).Append(nil)),
		BlockIM3: readOne(t, (&IM3{}).Append(nil)),
		BlockIM4: readOne(t, (&IM4{}).Append(nil)),
	}
	decoders := map[BlockType]func(RawBlock) error{
		BlockIM1: func(b RawBlock) error { _, err := DecodeIM1(b); return err },
		BlockIM2: func(b RawBlock) error { _, err := DecodeIM2(b); return err },
		BlockIM3: func(b RawBlock) error { _, err := DecodeIM3(b); return err },
		BlockIM4: func(b RawBlock) error { _, err := DecodeIM4(b); return err },
	}
	for typ, dec := range decoders {
		blk := blocks[typ]
		if err := dec(blk); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		wrong := blk
		wrong.Header.Type = BlockIM0
		err := dec(wrong)
		wantMalformed(t, err)
		if !strings.Contains(err.Error(), "I&M0 instead of "+typ.String()) {
			t.Fatalf("%s: %v", typ, err)
		}
		err = dec(truncated(blk, 1))
		wantMalformed(t, err)
		if !strings.Contains(err.Error(), "octets instead of") {
			t.Fatalf("%s: %v", typ, err)
		}
		wantMalformed(t, dec(trailing(blk)))
	}
}

func TestValidIMDate(t *testing.T) {
	for s, want := range map[string]bool{
		"":                  true,
		"2026-10-25":        true,
		"2026-10-25 12:34":  true,
		"0000-00-00 00:00":  true,
		"2026-10-2":         false,
		"2026-10-25 12:3":   false,
		"2026-10-25 12:345": false,
		"2026/10/25":        false,
		"2026-10/25":        false,
		"2026-10-25T12:34":  false,
		"2026-10-25 12-34":  false,
		"2026-1a-25":        false,
		"x026-10-25":        false,
		"2026-10-25 1a:34":  false,
		"2026-10-25 12:3x":  false,
		"20261025 12:34:00": false,
	} {
		if got := ValidIMDate(s); got != want {
			t.Errorf("ValidIMDate(%q) = %v, want %v", s, got, want)
		}
	}
}
