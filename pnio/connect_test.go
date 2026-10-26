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

func testConnectRequest() ConnectRequest {
	in := testIOCRBlockReq()
	out := testIOCRBlockReq()
	out.Type, out.Reference, out.FrameID = IOCROutput, 2, FrameIDAssign
	alarm := testAlarmCR()
	return ConnectRequest{
		AR:       testARBlockReq(),
		IOCRs:    []IOCRBlockReq{in, out},
		AlarmCR:  &alarm,
		Expected: []ExpectedSubmoduleBlockReq{testExpected()},
		ARRPC:    &ARRPCBlockReq{InitiatorRPCServerPort: 0xC001},
		Other: []RawBlock{
			{Header: BlockHeader{Type: BlockARVendorReq, Length: 6, VersionHigh: 1}, Body: hx("01020304")},
			{Header: BlockHeader{Type: BlockMCRReq, Length: 2, VersionHigh: 1}, Body: []byte{}},
		},
	}
}

func TestConnectRequestRoundTrip(t *testing.T) {
	c := testConnectRequest()
	b := c.Append(nil)

	// The blocks appear in the documented order.
	blocks, err := SplitBlocks(b)
	if err != nil {
		t.Fatal(err)
	}
	var types []BlockType
	for _, blk := range blocks {
		types = append(types, blk.Header.Type)
	}
	want := []BlockType{BlockARReq, BlockIOCRReq, BlockIOCRReq, BlockAlarmCRReq, BlockExpectedSubmReq, BlockARRPCReq, BlockARVendorReq, BlockMCRReq}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("block order %v, want %v", types, want)
	}
	if !bytes.HasPrefix(b, hx(goldenARBlockReq)) {
		t.Fatal("ARBlockReq is not first")
	}

	got, err := DecodeConnectRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip\n got %+v\nwant %+v", got, c)
	}
	// Re-encoding reproduces the octets.
	if !bytes.Equal(got.Append(nil), b) {
		t.Fatal("re-encoding differs")
	}

	// The minimal request is the ARBlockReq alone.
	min := ConnectRequest{AR: testARBlockReq()}
	got, err = DecodeConnectRequest(min.Append(nil))
	if err != nil || !reflect.DeepEqual(got, min) {
		t.Fatalf("minimal: %+v, %v", got, err)
	}
}

func TestDecodeConnectRequestErrors(t *testing.T) {
	ar := testARBlockReq()
	arb := ar.Append(nil)
	alarm := testAlarmCR()

	connectStatus := func(c1, c2 uint8) Status { return Status{CodeConnect, DecodePNIO, c1, c2} }

	t.Run("split error", func(t *testing.T) {
		_, err := DecodeConnectRequest(append(append([]byte(nil), arb...), 0x01))
		wantStatus(t, err, connectStatus(Code1CMRPC, CMRPCArgsLength))
	})
	t.Run("missing ARBlockReq", func(t *testing.T) {
		_, err := DecodeConnectRequest(alarm.Append(nil))
		wantStatus(t, err, connectStatus(Code1CMRPC, CMRPCUnknownBlocks))
		if !errors.Is(err, profinet.ErrUnsupported) {
			t.Fatalf("%v does not match ErrUnsupported", err)
		}
		_, err = DecodeConnectRequest(nil)
		wantStatus(t, err, connectStatus(Code1CMRPC, CMRPCUnknownBlocks))
	})
	t.Run("duplicate ARBlockReq", func(t *testing.T) {
		_, err := DecodeConnectRequest(ar.Append(arb))
		wantStatus(t, err, connectStatus(Code1CMRPC, CMRPCUnknownBlocks))
	})
	t.Run("two AlarmCRs", func(t *testing.T) {
		b := alarm.Append(alarm.Append(append([]byte(nil), arb...)))
		_, err := DecodeConnectRequest(b)
		wantStatus(t, err, connectStatus(Code1CMRPC, CMRPCAlarmCRCount))
	})
	t.Run("ARBlockReq version", func(t *testing.T) {
		b := append([]byte(nil), arb...)
		b[4] = 2
		_, err := DecodeConnectRequest(b)
		wantStatus(t, err, connectStatus(Code1ARBlock, 2))
	})
	t.Run("long station name", func(t *testing.T) {
		long := testARBlockReq()
		long.StationName = string(make([]byte, 241))
		_, err := DecodeConnectRequest(long.Append(nil))
		wantStatus(t, err, connectStatus(Code1ARBlock, 12))
	})

	// A block that fails with a plain decoding error is reported as a
	// faulty BlockLength (field 1) of that block.
	for _, c := range []struct {
		name  string
		block []byte
		want  Status
	}{
		{"ARBlockReq", RawBlock{Header: BlockHeader{Type: BlockARReq, VersionHigh: 1}, Body: arb[6:20]}.Append(nil), connectStatus(Code1ARBlock, 1)},
		{"IOCRBlockReq", RawBlock{Header: BlockHeader{Type: BlockIOCRReq, VersionHigh: 1}, Body: hx("0001")}.Append(nil), connectStatus(Code1IOCRBlock, 1)},
		{"AlarmCRBlockReq", RawBlock{Header: BlockHeader{Type: BlockAlarmCRReq, VersionHigh: 1}, Body: hx("0001")}.Append(nil), connectStatus(Code1AlarmCR, 1)},
		{"ExpectedSubmoduleBlockReq", RawBlock{Header: BlockHeader{Type: BlockExpectedSubmReq, VersionHigh: 1}, Body: hx("0001")}.Append(nil), connectStatus(Code1ExpSubm, 1)},
		{"ARRPCBlockReq", RawBlock{Header: BlockHeader{Type: BlockARRPCReq, VersionHigh: 1}, Body: hx("00")}.Append(nil), connectStatus(Code1ARRPC, 1)},
	} {
		t.Run("malformed "+c.name, func(t *testing.T) {
			b := c.block
			if c.name != "ARBlockReq" {
				b = append(append([]byte(nil), arb...), c.block...)
			}
			_, err := DecodeConnectRequest(b)
			wantStatus(t, err, c.want)
			if !errors.Is(err, profinet.ErrMalformed) {
				t.Fatalf("%v does not match ErrMalformed", err)
			}
		})
	}

	t.Run("invalid data description", func(t *testing.T) {
		e := ExpectedSubmoduleBlockReq{Modules: []ExpectedModule{{Submodules: []ExpectedSubmodule{{Descriptions: []DataDescription{{Type: 7}}}}}}}
		_, err := DecodeConnectRequest(e.Append(append([]byte(nil), arb...)))
		wantStatus(t, err, connectStatus(Code1ExpSubm, 1))
	})
}

func TestAsConnectError(t *testing.T) {
	se := &StatusError{Op: "x", Status: AbortStatus(AbortUser)}
	if got := asConnectError(BlockIOCRReq, se); got != error(se) {
		t.Fatalf("a *StatusError is not passed through: %v", got)
	}
	err := asConnectError(BlockARVendorReq, errors.New("boom"))
	wantStatus(t, err, Status{CodeConnect, DecodePNIO, Code1CMRPC, CMRPCUnknownBlocks})
}

func testConnectResponse() ConnectResponse {
	return ConnectResponse{
		AR:         ARBlockRes{Type: ARTypeIOCARSingle, ARUUID: testARUUID, SessionKey: 2, ResponderMAC: ethernet.MAC{2, 0, 0, 0, 0, 2}, UDPRTPort: 0x8892},
		IOCRs:      []IOCRBlockRes{{Type: IOCRInput, Reference: 1, FrameID: 0xC000}, {Type: IOCROutput, Reference: 2, FrameID: 0xC001}},
		AlarmCR:    &AlarmCRBlockRes{Type: 1, LocalAlarmReference: 3, MaxAlarmDataLength: 200},
		ModuleDiff: &ModuleDiffBlock{APIs: []DiffAPI{{API: 0, Modules: []DiffModule{{Slot: 1, IdentNumber: 0x10, State: ModuleStateWrong}}}}},
		ARServer:   &ARServerBlock{StationName: "dev"},
		ARRPC:      &ARRPCBlockReq{InitiatorRPCServerPort: 0xC003},
		Other:      []RawBlock{{Header: BlockHeader{Type: BlockARVendorRes, Length: 4, VersionHigh: 1}, Body: hx("abcd")}},
	}
}

func TestConnectResponseRoundTrip(t *testing.T) {
	c := testConnectResponse()
	b := c.Append(nil)
	blocks, err := SplitBlocks(b)
	if err != nil {
		t.Fatal(err)
	}
	var types []BlockType
	for _, blk := range blocks {
		types = append(types, blk.Header.Type)
	}
	want := []BlockType{BlockARRes, BlockIOCRRes, BlockIOCRRes, BlockAlarmCRRes, BlockModuleDiff, BlockARServer, BlockARRPCRes, BlockARVendorRes}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("block order %v, want %v", types, want)
	}
	// The ARRPCBlockRes has the ARRPCBlockReq layout with type 0x8107.
	checkGolden(t, "ARRPCBlockRes", blocks[6].Append(nil), hx("8107 0004 0100 c003"))

	got, err := DecodeConnectResponse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip\n got %+v\nwant %+v", got, c)
	}
	if !bytes.Equal(got.Append(nil), b) {
		t.Fatal("re-encoding differs")
	}

	min := ConnectResponse{AR: c.AR}
	got, err = DecodeConnectResponse(min.Append(nil))
	if err != nil || !reflect.DeepEqual(got, min) {
		t.Fatalf("minimal: %+v, %v", got, err)
	}
}

func TestDecodeConnectResponseErrors(t *testing.T) {
	c := testConnectResponse()
	arb := c.AR.Append(nil)

	_, err := DecodeConnectResponse(append(append([]byte(nil), arb...), 0))
	wantMalformed(t, err)

	_, err = DecodeConnectResponse(c.AlarmCR.Append(nil))
	wantMalformed(t, err)
	if errors.Is(err, profinet.ErrRejected) {
		t.Fatal("a missing ARBlockRes is not a rejection")
	}

	for _, typ := range []BlockType{BlockARRes, BlockIOCRRes, BlockAlarmCRRes, BlockModuleDiff, BlockARServer, BlockARRPCRes} {
		bad := RawBlock{Header: BlockHeader{Type: typ, VersionHigh: 1}, Body: hx("00")}.Append(nil)
		b := bad
		if typ != BlockARRes {
			b = append(append([]byte(nil), arb...), bad...)
		}
		if _, err := DecodeConnectResponse(b); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("malformed %s: %v", typ, err)
		}
	}
}
