// SPDX-License-Identifier: MIT

package pnio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/rpc"
)

var ndrArgsStatus = Status{CodePNIO, DecodePNIO, Code1CMRPC, CMRPCArgsLength}

func TestNDRRequest(t *testing.T) {
	blocks := hx("0114 0004 0100 0102")
	for _, c := range []struct {
		name   string
		order  rpc.ByteOrder
		golden string
	}{
		{"big endian", binary.BigEndian, "00004138 00000008 00004138 00000000 00000008"},
		{"little endian", binary.LittleEndian, "38410000 08000000 38410000 00000000 08000000"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := AppendNDRRequest(nil, c.order, DefaultArgsMaximum, blocks)
			checkGolden(t, "NDR request", b, append(hx(c.golden), blocks...))
			argsMax, got, err := DecodeNDRRequest(b, c.order)
			if err != nil || argsMax != DefaultArgsMaximum || !bytes.Equal(got, blocks) {
				t.Fatalf("decode %d %x %v", argsMax, got, err)
			}
			// Octets after ActualCount (padding of the RPC body) are cut.
			_, got, err = DecodeNDRRequest(append(b, 0, 0), c.order)
			if err != nil || !bytes.Equal(got, blocks) {
				t.Fatalf("with trailing octets %x %v", got, err)
			}
		})
	}

	// An empty request.
	b := AppendNDRRequest(nil, binary.BigEndian, 100, nil)
	if len(b) != NDRHeaderLen {
		t.Fatalf("%d octets", len(b))
	}
	if m, got, err := DecodeNDRRequest(b, binary.BigEndian); err != nil || m != 100 || len(got) != 0 {
		t.Fatalf("empty: %d %x %v", m, got, err)
	}
}

func TestDecodeNDRRequestErrors(t *testing.T) {
	good := AppendNDRRequest(nil, binary.BigEndian, 64, hx("0114 0002 0100"))
	put := func(off int, v uint32) []byte {
		b := append([]byte(nil), good...)
		binary.BigEndian.PutUint32(b[off:], v)
		return b
	}
	for name, b := range map[string][]byte{
		"short":                   good[:NDRHeaderLen-1],
		"empty":                   nil,
		"ArgsLength != ActualCnt": put(4, 7),
		"Offset != 0":             put(12, 1),
		"ActualCount > MaxCount":  put(8, 5),
		"ActualCount beyond data": put(16, 7),
		"both counts beyond data": func() []byte { b := put(4, 9); binary.BigEndian.PutUint32(b[16:], 9); return b }(),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := DecodeNDRRequest(b, binary.BigEndian)
			wantStatus(t, err, ndrArgsStatus)
		})
	}
	// The wrong byte order sees inconsistent counts.
	if _, _, err := DecodeNDRRequest(good, binary.LittleEndian); err == nil {
		t.Fatal("decoded with the wrong byte order")
	}
}

func TestNDRResponse(t *testing.T) {
	blocks := hx("8114 0002 0100")
	st := RWStatus(false, RWInvalidIndex, 0)
	for _, c := range []struct {
		name   string
		order  rpc.ByteOrder
		golden string
	}{
		// The PNIOStatus is always big endian, the counts follow drep.
		{"big endian", binary.BigEndian, "de80b000 00000006 00000040 00000000 00000006"},
		{"little endian", binary.LittleEndian, "de80b000 06000000 40000000 00000000 06000000"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := AppendNDRResponse(nil, c.order, st, 64, blocks)
			checkGolden(t, "NDR response", b, append(hx(c.golden), blocks...))
			s, got, err := DecodeNDRResponse(b, c.order)
			if err != nil || s != st || !bytes.Equal(got, blocks) {
				t.Fatalf("decode %s %x %v", s, got, err)
			}
		})
	}
	b := AppendNDRResponse(nil, binary.BigEndian, Status{}, 0, nil)
	if s, got, err := DecodeNDRResponse(b, binary.BigEndian); err != nil || !s.OK() || len(got) != 0 {
		t.Fatalf("empty OK: %s %x %v", s, got, err)
	}
}

func TestDecodeNDRResponseErrors(t *testing.T) {
	// A short failure response carrying only the status.
	st := ServiceStatus(CodeConnect, Code1CMRPC, CMRPCOutOfARs)
	s, blocks, err := DecodeNDRResponse(st.Append(nil), binary.BigEndian)
	if err != nil || s != st || blocks != nil {
		t.Fatalf("short failure: %s %x %v", s, blocks, err)
	}
	// A short response whose status is OK is malformed.
	for _, b := range [][]byte{nil, hx("de80"), hx("00000000"), make([]byte, NDRHeaderLen-1)} {
		_, _, err := DecodeNDRResponse(b, binary.BigEndian)
		wantMalformed(t, err)
	}

	good := AppendNDRResponse(nil, binary.BigEndian, Status{}, 64, hx("8114 0002 0100"))
	bad := append([]byte(nil), good...)
	binary.BigEndian.PutUint32(bad[4:], 5)
	s, _, err = DecodeNDRResponse(bad, binary.BigEndian)
	wantMalformed(t, err)
	if !s.OK() {
		t.Fatalf("status %s", s)
	}
	bad = append([]byte(nil), good...)
	binary.BigEndian.PutUint32(bad[4:], 7)
	binary.BigEndian.PutUint32(bad[16:], 7)
	_, _, err = DecodeNDRResponse(bad, binary.BigEndian)
	wantMalformed(t, err)
	if errors.Is(err, profinet.ErrRejected) {
		t.Fatal("a malformed response is not a rejection")
	}
	// The status of a malformed failure response is still returned.
	bad = AppendNDRResponse(nil, binary.BigEndian, st, 64, nil)
	binary.BigEndian.PutUint32(bad[16:], 1)
	if s, _, err := DecodeNDRResponse(bad, binary.BigEndian); err == nil || s != st {
		t.Fatalf("%s %v", s, err)
	}
}
