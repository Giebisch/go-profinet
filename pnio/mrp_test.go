// SPDX-License-Identifier: MIT

package pnio

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/otfabric/go-profinet"
)

var allFF = [16]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

func TestMrpGolden(t *testing.T) {
	// PDPortMrpDataAdjust: header (type 0x0214, length 20, version 1.0),
	// two octets of padding to align to four, the domain UUID.
	p := PDPortMrpData{Domain: allFF}
	want := hx("0214 0014 0100 0000 ffffffffffffffffffffffffffffffff")
	if got := p.Append(nil); !bytes.Equal(got, want) {
		t.Fatalf("port adjust\n got % x\nwant % x", got, want)
	}
	// PDInterfaceMrpDataAdjust, role client, domain name "mrpdomain-1",
	// MrpClientParams: header, pad 2 (offset 8), UUID (24), role (26),
	// pad 2 (28), name length and name (40), sub-block of 12 (52):
	// length 52-4 = 48.
	a := PDInterfaceMrpDataAdjust{Domain: allFF, Role: MrpRoleClient, DomainName: "mrpdomain-1", Client: &MrpClientParams{LinkDownT: 20, LinkUpT: 20, LinkNRmax: 4}}
	want = hx("0211 0030 0100 0000 ffffffffffffffffffffffffffffffff 0001 0000 0b 6d7270646f6d61696e2d31 " +
		"0217 0008 0100 0014 0014 0004")
	if got := a.Append(nil); !bytes.Equal(got, want) {
		t.Fatalf("interface adjust\n got % x\nwant % x", got, want)
	}
}

func TestMrpRoundTrip(t *testing.T) {
	one := func(b []byte) RawBlock {
		blk, rest, err := ReadBlock(b)
		if err != nil || len(rest) != 0 {
			t.Fatalf("% x: %v (%d left)", b, err, len(rest))
		}
		return blk
	}
	adj := PDInterfaceMrpDataAdjust{Domain: [16]byte{1, 2, 3}, Role: MrpRoleManager, DomainName: "ring",
		Manager: &MrpManagerParams{Prio: 0x8000, TopChangeT: 10, TopNRmax: 3, TestShortT: 10, TestDefaultT: 20, TestNRmax: 3},
		Other:   []RawBlock{{Header: BlockHeader{Type: 0x0218, Length: 6, VersionHigh: 1}, Body: []byte{0, 0, 0, 1}}}}
	if got, err := DecodePDInterfaceMrpDataAdjust(one(adj.Append(nil))); err != nil || !reflect.DeepEqual(got, adj) {
		t.Fatalf("adjust %+v %v", got, err)
	}
	for _, real := range []PDInterfaceMrpDataReal{
		{Domain: allFF, Role: MrpRoleManager, DomainName: "mrpdomain-1", Version: 1, RingState: 1, HasRingState: true},
		{Domain: allFF, Role: MrpRoleClient, DomainName: "abc", Version: 1},
	} {
		if got, err := DecodePDInterfaceMrpDataReal(one(real.Append(nil))); err != nil || !reflect.DeepEqual(got, real) {
			t.Fatalf("real %+v %v", got, err)
		}
	}
	chk := PDInterfaceMrpDataCheck{Domain: allFF, Check: MrpCheckManager | MrpCheckDomainUUID}
	if got, err := DecodePDInterfaceMrpDataCheck(one(chk.Append(nil))); err != nil || got != chk {
		t.Fatalf("check %+v %v", got, err)
	}
	for _, p := range []PDPortMrpData{{Domain: allFF}, {Real: true, Domain: [16]byte{9}}} {
		if got, err := DecodePDPortMrpData(one(p.Append(nil))); err != nil || got != p {
			t.Fatalf("port %+v %v", got, err)
		}
	}
	for _, bt := range []BlockType{BlockPDInterfaceMrpDataAdjust, BlockPDInterfaceMrpDataReal, BlockPDInterfaceMrpDataCheck,
		BlockPDPortMrpDataAdjust, BlockPDPortMrpDataReal, BlockMrpManagerParams, BlockMrpClientParams, BlockMrpRingStateData} {
		if bt.String()[0] == '0' {
			t.Errorf("%#x has no name", uint16(bt))
		}
	}
}

func TestMrpDecodeErrors(t *testing.T) {
	wrongType := RawBlock{Header: BlockHeader{Type: BlockPDPortDataCheck, VersionHigh: 1}}
	newer := RawBlock{Header: BlockHeader{Type: BlockPDInterfaceMrpDataAdjust, VersionHigh: 1, VersionLow: 1}, Body: make([]byte, 40)}
	short := RawBlock{Header: BlockHeader{Type: BlockPDInterfaceMrpDataReal, VersionHigh: 1}, Body: []byte{0, 0, 1}}
	if _, err := DecodePDInterfaceMrpDataAdjust(wrongType); !errors.Is(err, profinet.ErrMalformed) {
		t.Error(err)
	}
	if _, err := DecodePDInterfaceMrpDataAdjust(newer); !errors.Is(err, profinet.ErrUnsupported) {
		t.Errorf("v1.1: %v", err)
	}
	if _, err := DecodePDInterfaceMrpDataReal(short); !errors.Is(err, profinet.ErrMalformed) {
		t.Error(err)
	}
	if _, err := DecodePDInterfaceMrpDataReal(wrongType); err == nil {
		t.Error("real of a wrong type")
	}
	if _, err := DecodePDInterfaceMrpDataCheck(wrongType); err == nil {
		t.Error("check of a wrong type")
	}
	if _, err := DecodePDPortMrpData(wrongType); err == nil {
		t.Error("port of a wrong type")
	}
	// A truncated sub-block.
	a := PDInterfaceMrpDataAdjust{Domain: allFF, Role: MrpRoleClient, Client: &MrpClientParams{}}
	b := a.Append(nil)
	blk, _, err := ReadBlock(b)
	if err != nil {
		t.Fatal(err)
	}
	blk.Body = blk.Body[:len(blk.Body)-3]
	if _, err := DecodePDInterfaceMrpDataAdjust(blk); err == nil {
		t.Error("truncated sub-block")
	}
	// A sub-block too short for its fields.
	m := PDInterfaceMrpDataAdjust{Domain: allFF, Role: MrpRoleManager, Other: []RawBlock{{Header: BlockHeader{Type: BlockMrpManagerParams, VersionHigh: 1}, Body: []byte{1}}}}
	blk, _, _ = ReadBlock(m.Append(nil))
	if _, err := DecodePDInterfaceMrpDataAdjust(blk); err == nil {
		t.Error("short MrpManagerParams")
	}
}

func FuzzMRP(f *testing.F) {
	f.Add((&PDInterfaceMrpDataAdjust{Domain: allFF, Role: MrpRoleClient, DomainName: "d", Client: &MrpClientParams{}}).Append(nil))
	f.Add((&PDInterfaceMrpDataAdjust{Domain: allFF, Role: MrpRoleManager, Manager: &MrpManagerParams{}}).Append(nil))
	f.Add((&PDInterfaceMrpDataReal{Domain: allFF, Role: MrpRoleManager, Version: 1, HasRingState: true}).Append(nil))
	f.Add((&PDInterfaceMrpDataCheck{Check: 3}).Append(nil))
	f.Add((&PDPortMrpData{Real: true}).Append(nil))
	f.Fuzz(func(t *testing.T, b []byte) {
		blk, _, err := ReadBlock(b)
		if err != nil {
			return
		}
		if v, err := DecodePDInterfaceMrpDataAdjust(blk); err == nil {
			if _, err := DecodePDInterfaceMrpDataAdjust(oneBlock(t, v.Append(nil))); err != nil {
				t.Fatalf("re-encoded adjust: %v", err)
			}
		}
		if v, err := DecodePDInterfaceMrpDataReal(blk); err == nil {
			if _, err := DecodePDInterfaceMrpDataReal(oneBlock(t, v.Append(nil))); err != nil {
				t.Fatalf("re-encoded real: %v", err)
			}
		}
		_, _ = DecodePDInterfaceMrpDataCheck(blk)
		_, _ = DecodePDPortMrpData(blk)
	})
}
