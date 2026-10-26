// SPDX-License-Identifier: MIT

package pnio

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// goldenPDPortDataReal is a PDPortDataReal of port 0/0x8001 with one peer,
// laid out by hand from IEC 61158-6-10 (every padding aligns to four
// octets from the start of the block).
const goldenPDPortDataReal = "020f0048 0100 0000 0000 8001" +
	"08 706f72742d303031" + "01" + "0000" + // OwnPortID "port-001", one peer, padding
	"08 706f72742d303031" + "07 73746174696f6e" + "000000" + // PeerPortID, PeerChassisID "station", padding
	"00000064" + "020000000002" + "0000" + // LineDelay 100 ns, PeerMAC, padding
	"0010" + "0000" + // MAUType 100BASE-TX FD, padding
	"00000000" + "00000000" + "0001" + "0000" + "00000001" // boundaries, LinkState up, padding, MediaType copper

func testPDPort() PDPortDataReal {
	return PDPortDataReal{Slot: 0, Subslot: 0x8001, OwnPortID: "port-001",
		Peers:   []PDPortPeer{{PortID: "port-001", ChassisID: "station", LineDelay: 100, MAC: ethernet.MAC{2, 0, 0, 0, 0, 2}}},
		MAUType: 0x0010, LinkState: LinkStateUp, MediaType: 1}
}

func TestPDPortDataRealGolden(t *testing.T) {
	p := testPDPort()
	b := p.Append(nil)
	if !bytes.Equal(b, hx(goldenPDPortDataReal)) {
		t.Fatalf("encoding\n got %x\nwant %x", b, hx(goldenPDPortDataReal))
	}
	blk, rest, err := ReadBlock(b)
	if err != nil || len(rest) != 0 {
		t.Fatal(err)
	}
	got, err := DecodePDPortDataReal(blk)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("decoded %+v %v", got, err)
	}
	// Without peers, and with names that need no padding.
	q := PDPortDataReal{Slot: 0, Subslot: 0x8002, OwnPortID: "port-002.dev", LinkState: LinkStateDown}
	blk, _, _ = ReadBlock(q.Append(nil))
	if got, err := DecodePDPortDataReal(blk); err != nil || got.OwnPortID != q.OwnPortID || len(got.Peers) != 0 || got.LinkState != LinkStateDown {
		t.Fatalf("no peers %+v %v", got, err)
	}
	if len(q.Append(nil))%4 != 0 {
		t.Fatal("block not aligned")
	}
	// Errors.
	if _, err := DecodePDPortDataReal(RawBlock{Header: BlockHeader{Type: BlockIM0}}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("type: %v", err)
	}
	trunc := hx(goldenPDPortDataReal)[:40]
	if _, err := DecodePDPortDataReal(RawBlock{Header: BlockHeader{Type: BlockPDPortDataReal}, Body: trunc[6:]}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("truncated: %v", err)
	}
	if BlockPDPortDataReal.String() != "PDPortDataReal" || BlockMultipleBlockHeader.String() != "MultipleBlockHeader" || BlockPDInterfaceDataReal.String() != "PDInterfaceDataReal" {
		t.Fatal("block names")
	}
}

func TestPDRealData(t *testing.T) {
	ports := []PDPortDataReal{testPDPort(), {Slot: 0, Subslot: 0x8002, OwnPortID: "port-002", LinkState: LinkStateDown}}
	b := AppendPDRealData(nil, 0, ports)
	// An interface block among them is skipped, a bare port block taken.
	b = (&RawBlock{Header: BlockHeader{Type: BlockPDInterfaceDataReal, VersionHigh: 1}, Body: []byte{0, 0}}).Append(b)
	b = ports[0].Append(b)
	got, err := DecodePDRealData(b)
	if err != nil || len(got) != 3 || !reflect.DeepEqual(got[0], ports[0]) || got[1].Subslot != 0x8002 || got[2].OwnPortID != "port-001" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := DecodePDRealData([]byte{1, 2, 3}); err == nil {
		t.Fatal("garbage decoded")
	}
	short := (&RawBlock{Header: BlockHeader{Type: BlockMultipleBlockHeader, VersionHigh: 1}, Body: []byte{0, 0}}).Append(nil)
	if _, err := DecodePDRealData(short); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("short MultipleBlockHeader: %v", err)
	}
	bad := (&RawBlock{Header: BlockHeader{Type: BlockPDPortDataReal, VersionHigh: 1}, Body: []byte{0, 0}}).Append(nil)
	if _, err := DecodePDRealData(bad); err == nil {
		t.Fatal("bad port block decoded")
	}
	nested := AppendPDRealData(nil, 0, nil)
	nested = append(nested, AppendPDRealData(nil, 0, []PDPortDataReal{{OwnPortID: "x"}})...)
	nested[len(nested)-1] ^= 0xFF
	if _, err := DecodePDRealData(nested[:len(nested)-2]); err == nil {
		t.Fatal("truncated nested block decoded")
	}
}

func FuzzPDRealData(f *testing.F) {
	f.Add(AppendPDRealData(nil, 0, []PDPortDataReal{testPDPort()}))
	f.Add(hx(goldenPDPortDataReal))
	f.Fuzz(func(t *testing.T, b []byte) {
		ports, err := DecodePDRealData(b)
		if err != nil {
			return
		}
		again, err := DecodePDRealData(AppendPDRealData(nil, 0, ports))
		if err != nil || len(again) != len(ports) {
			t.Fatalf("round trip: %v", err)
		}
		if !reflect.DeepEqual(again, ports) {
			t.Fatalf("round trip\n got %+v\nwant %+v", again, ports)
		}
	})
}
