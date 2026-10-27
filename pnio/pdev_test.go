// SPDX-License-Identifier: MIT

package pnio

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// Golden vectors: synthetic, built field by field from the layouts that
// Wireshark's PNIO dissector and p-net agree on
// (docs/research/EVIDENCE-DCP-PDEV.md, B.1).
func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func oneBlock(t *testing.T, b []byte) RawBlock {
	t.Helper()
	blk, rest, err := ReadBlock(b)
	if err != nil || len(rest) != 0 {
		t.Fatalf("%v, %d octets left", err, len(rest))
	}
	return blk
}

func TestPDPortDataCheckGolden(t *testing.T) {
	const v = "02 00 00 1c 01 00 00 00 00 00 80 01 02 0a 00 10 01 00 01 08 70 6f 72 74 2d 30 30 31 03 70 6c 63"
	want := PDPortDataCheck{Slot: 0, Subslot: 0x8001, Peers: []CheckPeer{{PortID: "port-001", ChassisID: "plc"}}}
	if got := hex.EncodeToString(want.Append(nil)); got != strings.ReplaceAll(v, " ", "") {
		t.Fatalf("encode\n got %s\nwant %s", got, v)
	}
	got, err := DecodePDPortDataCheck(oneBlock(t, unhex(t, v)))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	// Other checks are kept undecoded and encoded back; the record is
	// padded to four octets.
	want.Other = []RawBlock{{Header: BlockHeader{Type: BlockCheckMAUType, Length: 4, VersionHigh: 1}, Body: []byte{0, 0x10}}}
	b := want.Append(nil)
	if len(b)%4 != 0 {
		t.Fatalf("record of %d octets", len(b))
	}
	got, err = DecodePDPortDataCheck(oneBlock(t, b))
	if err != nil || len(got.Peers) != 1 || len(got.Other) != 1 || got.Other[0].Header.Type != BlockCheckMAUType {
		t.Fatalf("%+v %v", got, err)
	}
	if BlockCheckPeers.String() != "CheckPeers" || BlockPDPortDataCheck.String() != "PDPortDataCheck" {
		t.Fatal("names")
	}
}

func TestPDPortDataAdjustGolden(t *testing.T) {
	// Wireshark's minimal form (no trailing padding) decodes.
	const ws = "02 02 00 16 01 00 00 00 00 00 80 01 02 24 00 0a 01 00 00 00 00 00 00 01 00 00"
	got, err := DecodePDPortDataAdjust(oneBlock(t, unhex(t, ws)))
	if err != nil || got.Subslot != 0x8001 || !got.HasPeerToPeerBoundary || got.PeerToPeerBoundary != PeerToPeerNoLLDP {
		t.Fatalf("%+v %v", got, err)
	}
	// go-profinet sends p-net's form: each sub-block padded to four.
	const pnet = "02 02 00 18 01 00 00 00 00 00 80 01 02 24 00 0c 01 00 00 00 00 00 00 01 00 00 00 00"
	a := PDPortDataAdjust{Subslot: 0x8001, PeerToPeerBoundary: PeerToPeerNoLLDP, HasPeerToPeerBoundary: true}
	if b := hex.EncodeToString(a.Append(nil)); b != strings.ReplaceAll(pnet, " ", "") {
		t.Fatalf("encode\n got %s\nwant %s", b, pnet)
	}
	// AdjustDCPBoundary, Wireshark's form, after PeerToPeerBoundary.
	const both = "02 02 00 26 01 00 00 00 00 00 80 01 02 24 00 0c 01 00 00 00 00 00 00 01 00 00 00 00 02 25 00 0a 01 00 00 00 00 00 00 03 00 00"
	got, err = DecodePDPortDataAdjust(oneBlock(t, unhex(t, both)))
	if err != nil || !got.HasDCPBoundary || got.DCPBoundary != DCPBoundaryIdentify|DCPBoundaryHello || got.PeerToPeerBoundary != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	// Unsupported adjustments are kept; a repeated one too.
	mau := RawBlock{Header: BlockHeader{Type: BlockAdjustMAUType, VersionHigh: 1}, Body: []byte{0, 0, 0, 0x10, 0, 0}}
	a.Other = []RawBlock{mau}
	got, err = DecodePDPortDataAdjust(oneBlock(t, a.Append(nil)))
	if err != nil || len(got.Other) != 1 || got.Other[0].Header.Type != BlockAdjustMAUType {
		t.Fatalf("%+v %v", got, err)
	}
	twice := PDPortDataAdjust{Other: []RawBlock{{Header: BlockHeader{Type: BlockAdjustDCPBoundary, VersionHigh: 1}, Body: []byte{0, 0, 0, 0, 0, 1, 0, 0}}},
		DCPBoundary: 2, HasDCPBoundary: true}
	got, err = DecodePDPortDataAdjust(oneBlock(t, twice.Append(nil)))
	if err != nil || got.DCPBoundary != 2 || len(got.Other) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPDInterfaceAdjustGolden(t *testing.T) {
	const v = "02 50 00 08 01 00 00 00 00 00 00 01"
	a := PDInterfaceAdjust{MultipleInterfaceMode: InterfaceModeNameOfDevice}
	if b := hex.EncodeToString(a.Append(nil)); b != strings.ReplaceAll(v, " ", "") {
		t.Fatalf("encode %s", b)
	}
	got, err := DecodePDInterfaceAdjust(oneBlock(t, unhex(t, v)))
	if err != nil || got != a {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPDInterfaceDataReal(t *testing.T) {
	p := PDInterfaceDataReal{ChassisID: "dut", MAC: ethernet.MAC{2, 0, 0, 0, 0, 1},
		IP: netip.MustParseAddr("192.168.0.10"), Mask: netip.MustParseAddr("255.255.255.0"), Gateway: netip.MustParseAddr("0.0.0.0")}
	b := p.Append(nil)
	// hdr(6) len(1) "dut" pad(2) MAC(6) pad(2) IP mask gateway(12)
	const v = "02 40 00 1c 01 00 03 64 75 74 00 00 02 00 00 00 00 01 00 00 c0 a8 00 0a ff ff ff 00 00 00 00 00"
	if got := hex.EncodeToString(b); got != strings.ReplaceAll(v, " ", "") {
		t.Fatalf("encode\n got %s\nwant %s", got, v)
	}
	got, err := DecodePDInterfaceDataReal(oneBlock(t, b))
	if err != nil || got != p {
		t.Fatalf("%+v %v", got, err)
	}
	// An address that is not IPv4 is sent as zero.
	p.Gateway = netip.Addr{}
	if got, _ := DecodePDInterfaceDataReal(oneBlock(t, p.Append(nil))); got.Gateway != netip.AddrFrom4([4]byte{}) {
		t.Fatalf("gateway %s", got.Gateway)
	}
}

func TestPDRealDataRecord(t *testing.T) {
	rd := PDRealData{
		Interface:     &PDInterfaceDataReal{ChassisID: "io-1", MAC: ethernet.MAC{2, 0, 0, 0, 0, 1}, IP: netip.MustParseAddr("10.0.0.1"), Mask: netip.MustParseAddr("255.0.0.0"), Gateway: netip.MustParseAddr("0.0.0.0")},
		InterfaceSlot: 0, InterfaceSubslot: 0x8000,
		Ports: []PDPortDataReal{{Subslot: 0x8001, OwnPortID: "port-001", LinkState: LinkStateUp, Peers: []PDPortPeer{{PortID: "port-001", ChassisID: "plc"}}}},
	}
	b := AppendPDRealDataRecord(nil, 0, &rd)
	got, err := DecodePDRealDataRecord(b)
	if err != nil || !reflect.DeepEqual(got, rd) {
		t.Fatalf("%+v %v", got, err)
	}
	// The ports-only decoder still reads the record.
	ports, err := DecodePDRealData(b)
	if err != nil || len(ports) != 1 || ports[0].Peers[0].ChassisID != "plc" {
		t.Fatalf("%+v %v", ports, err)
	}
}

func TestPDevMalformed(t *testing.T) {
	other := RawBlock{Header: BlockHeader{Type: BlockIM0}}
	if _, err := DecodePDPortDataCheck(other); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("check: %v", err)
	}
	if _, err := DecodePDPortDataAdjust(other); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("adjust: %v", err)
	}
	if _, err := DecodePDInterfaceAdjust(other); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("interface adjust: %v", err)
	}
	if _, err := DecodePDInterfaceDataReal(other); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("interface data real: %v", err)
	}
	bad := map[string]func() error{
		"check too short": func() error {
			_, err := DecodePDPortDataCheck(RawBlock{Header: BlockHeader{Type: BlockPDPortDataCheck}, Body: []byte{0, 0, 0}})
			return err
		},
		"adjust too short": func() error {
			_, err := DecodePDPortDataAdjust(RawBlock{Header: BlockHeader{Type: BlockPDPortDataAdjust}, Body: []byte{0}})
			return err
		},
		"hostile peer count": func() error {
			_, err := DecodePDPortDataCheck(oneBlock(t, unhex(t, "02 00 00 0f 01 00 00 00 00 00 80 01 02 0a 00 03 01 00 ff")))
			return err
		},
		"garbage sub-block": func() error {
			_, err := DecodePDPortDataCheck(oneBlock(t, unhex(t, "02 00 00 0c 01 00 00 00 00 00 80 01 02 0a 00 ff")))
			return err
		},
		"truncated adjust value": func() error {
			_, err := DecodePDPortDataAdjust(oneBlock(t, unhex(t, "02 02 00 10 01 00 00 00 00 00 80 01 02 24 00 04 01 00 00 00")))
			return err
		},
		"truncated if adjust": func() error {
			_, err := DecodePDInterfaceAdjust(RawBlock{Header: BlockHeader{Type: BlockPDInterfaceAdjust}, Body: []byte{0, 0}})
			return err
		},
		"truncated if data": func() error {
			_, err := DecodePDInterfaceDataReal(RawBlock{Header: BlockHeader{Type: BlockPDInterfaceDataReal}, Body: []byte{9}})
			return err
		},
		"record bad mbh":       func() error { _, err := DecodePDRealDataRecord(unhex(t, "04 00 00 04 01 00 00 00")); return err },
		"record bad port":      func() error { _, err := DecodePDRealDataRecord(unhex(t, "02 0f 00 03 01 00 00")); return err },
		"record bad interface": func() error { _, err := DecodePDRealDataRecord(unhex(t, "02 40 00 03 01 00 09")); return err },
		"record bad inner": func() error {
			_, err := DecodePDRealDataRecord(unhex(t, "04 00 00 0f 01 00 00 00 00 00 00 00 00 00 80 00 02 40 00"))
			return err
		},
		"record garbage": func() error { _, err := DecodePDRealDataRecord([]byte{1, 2, 3}); return err },
	}
	for name, f := range bad {
		if err := f(); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func FuzzPDev(f *testing.F) {
	f.Add(PDPortDataCheckSeed())
	a := PDPortDataAdjust{Subslot: 0x8001, PeerToPeerBoundary: 1, HasPeerToPeerBoundary: true, DCPBoundary: 3, HasDCPBoundary: true}
	f.Add(a.Append(nil))
	f.Add((&PDInterfaceAdjust{MultipleInterfaceMode: 1}).Append(nil))
	f.Add(AppendPDRealDataRecord(nil, 0, &PDRealData{Interface: &PDInterfaceDataReal{ChassisID: "x"}, Ports: []PDPortDataReal{{OwnPortID: "port-001"}}}))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = DecodePDRealDataRecord(b)
		blk, _, err := ReadBlock(b)
		if err != nil {
			return
		}
		if c, err := DecodePDPortDataCheck(blk); err == nil {
			if _, err := DecodePDPortDataCheck(oneBlock(t, c.Append(nil))); err != nil {
				t.Fatalf("re-encoded check: %v", err)
			}
		}
		if a, err := DecodePDPortDataAdjust(blk); err == nil {
			if _, err := DecodePDPortDataAdjust(oneBlock(t, a.Append(nil))); err != nil {
				t.Fatalf("re-encoded adjust: %v", err)
			}
		}
		_, _ = DecodePDInterfaceAdjust(blk)
		_, _ = DecodePDInterfaceDataReal(blk)
	})
}

// PDPortDataCheckSeed is a fuzz seed.
func PDPortDataCheckSeed() []byte {
	c := PDPortDataCheck{Subslot: 0x8001, Peers: []CheckPeer{{PortID: "port-001", ChassisID: "plc"}, {PortID: "port-002", ChassisID: "sw"}}}
	return c.Append(nil)
}
