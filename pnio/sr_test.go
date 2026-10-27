// SPDX-License-Identifier: MIT

package pnio

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/rpc"
)

// Golden blocks built from the layouts of Wireshark's PNIO dissector.
func TestSRInfoBlock(t *testing.T) {
	const v = "010a0008 0100 00c8 00000005"
	s := SRInfoBlock{RedundancyDataHoldFactor: 0xC8, Properties: SRPropInputValidOnBackupAR | SRPropMode}
	if got := hex.EncodeToString(s.Append(nil)); got != strings.ReplaceAll(v, " ", "") {
		t.Fatalf("encode %s", got)
	}
	got, err := DecodeSRInfoBlock(oneBlock(t, unhex(t, v)))
	if err != nil || got != s {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := DecodeSRInfoBlock(RawBlock{Header: BlockHeader{Type: BlockSRInfo}, Body: []byte{0}}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatal(err)
	}
	if _, err := DecodeSRInfoBlock(RawBlock{Header: BlockHeader{Type: BlockMCRReq}}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatal(err)
	}
}

func TestMCRBlockReq(t *testing.T) {
	const v = "0106000f 0100 0002 00000001 000a 0003 706c63"
	m := MCRBlockReq{IOCRReference: 2, AddressResolutionProperties: 1, MCITimeoutFactor: 10, ProviderStationName: "plc"}
	if got := hex.EncodeToString(m.Append(nil)); got != strings.ReplaceAll(v, " ", "") {
		t.Fatalf("encode %s", got)
	}
	got, err := DecodeMCRBlockReq(oneBlock(t, unhex(t, v)))
	if err != nil || got != m {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := DecodeMCRBlockReq(RawBlock{Header: BlockHeader{Type: BlockMCRReq}, Body: []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 9}}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatal(err)
	}
	if _, err := DecodeMCRBlockReq(RawBlock{Header: BlockHeader{Type: BlockSRInfo}}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatal(err)
	}
}

func TestARSetID(t *testing.T) {
	id := ARSetID{Discriminator: [6]byte{1, 2, 3, 4, 5, 6}, ConfigID: [8]byte{9, 9, 9, 9, 9, 9, 9, 9}, ARNumber: 2, Resource: 2}
	u := id.UUID()
	if u[14] != 0x00 || u[15] != 0x12 {
		t.Fatalf("selector % x", u[14:])
	}
	if got := SplitARUUID(u); got != id {
		t.Fatalf("%+v", got)
	}
	// Bits outside the selector's fields are dropped.
	if got := SplitARUUID(rpc.UUID{15: 0xFF, 14: 0xFF}); got.ARNumber != 7 || got.Resource != 3 {
		t.Fatalf("%+v", got)
	}
}

func FuzzSR(f *testing.F) {
	f.Add((&SRInfoBlock{RedundancyDataHoldFactor: 3}).Append(nil))
	f.Add((&MCRBlockReq{ProviderStationName: "p"}).Append(nil))
	f.Fuzz(func(t *testing.T, b []byte) {
		blk, _, err := ReadBlock(b)
		if err != nil {
			return
		}
		if s, err := DecodeSRInfoBlock(blk); err == nil {
			if _, err := DecodeSRInfoBlock(oneBlock(t, s.Append(nil))); err != nil {
				t.Fatal(err)
			}
		}
		if m, err := DecodeMCRBlockReq(blk); err == nil && len(m.ProviderStationName) < 0xFFF0 {
			if _, err := DecodeMCRBlockReq(oneBlock(t, m.Append(nil))); err != nil {
				t.Fatal(err)
			}
		}
	})
}
