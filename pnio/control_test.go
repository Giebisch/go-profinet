// SPDX-License-Identifier: MIT

package pnio

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet/rpc"
)

func TestControlBlockGolden(t *testing.T) {
	c := ControlBlock{Type: BlockPrmEndReq, ARUUID: testARUUID, SessionKey: 2, Command: CmdPrmEnd}
	// BlockLength 28: reserved, ARUUID, SessionKey, reserved, command,
	// properties.
	checkGolden(t, "PrmEnd", c.Append(nil), hx(`0110 001c 0100 0000 11111111111111111111111111111111 0002 0000 0001 0000`))

	// A non-plug block ignores AlarmSequence on encode.
	c.AlarmSequence = 7
	checkGolden(t, "PrmEnd with sequence", c.Append(nil), hx(`0110 001c 0100 0000 11111111111111111111111111111111 0002 0000 0001 0000`))

	p := ControlBlock{Type: BlockAppReadyPlugReq, ARUUID: testARUUID, SessionKey: 2, Command: CmdApplicationReady, Properties: 0x0001, AlarmSequence: 0x0123}
	checkGolden(t, "AppReadyPlug", p.Append(nil), hx(`0113 001c 0100 0000 11111111111111111111111111111111 0002 0123 0002 0001`))
}

func TestControlBlockRoundTrip(t *testing.T) {
	for _, typ := range []BlockType{
		BlockPrmEndReq, BlockPrmEndPlugReq, BlockAppReadyReq, BlockAppReadyPlugReq,
		BlockReleaseReq, BlockReadyForCompReq, BlockReadyForRTC3Req, BlockPrmBeginReq,
		BlockPrmEndRes, BlockPrmEndPlugRes, BlockAppReadyRes, BlockAppReadyPlugR,
		BlockReleaseRes, BlockReadyForCompR, BlockReadyForRTC3R, BlockPrmBeginRes,
	} {
		c := ControlBlock{Type: typ, ARUUID: testARUUID, SessionKey: 9, Command: CmdRelease, Properties: 3}
		if isPlug(typ) {
			c.AlarmSequence = 0x07FF
		}
		b := c.Append(nil)
		if len(b) != 32 || binary.BigEndian.Uint16(b[2:]) != 28 {
			t.Fatalf("%s: %d octets", typ, len(b))
		}
		got, err := DecodeControlBlock(readOne(t, b))
		if err != nil || got != c {
			t.Fatalf("%s: round trip %+v, %v", typ, got, err)
		}
	}
}

func TestDecodeControlBlockSequence(t *testing.T) {
	// A non-plug block drops the reserved field even when it is set.
	b := (&ControlBlock{Type: BlockAppReadyPlugReq, AlarmSequence: 5}).Append(nil)
	binary.BigEndian.PutUint16(b, uint16(BlockAppReadyReq))
	got, err := DecodeControlBlock(readOne(t, b))
	if err != nil || got.AlarmSequence != 0 || got.Type != BlockAppReadyReq {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestDecodeControlBlockErrors(t *testing.T) {
	blk := readOne(t, (&ControlBlock{Type: BlockReleaseReq, ARUUID: testARUUID}).Append(nil))
	for _, typ := range []BlockType{BlockARReq, BlockReadReq, BlockIM0, BlockSubmoduleListReq, BlockType(0x0115), BlockARRes} {
		bad := blk
		bad.Header.Type = typ
		err := decodeErr(DecodeControlBlock, bad)
		wantMalformed(t, err)
		if !strings.Contains(err.Error(), "is not a control block") {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	wantStatus(t, decodeErr(DecodeControlBlock, withVersion(blk, 2, 0)), Status{CodeRelease, DecodePNIO, Code1Release, 2})
	wantStatus(t, decodeErr(DecodeControlBlock, withVersion(blk, 1, 1)), Status{CodeRelease, DecodePNIO, Code1Release, 3})
	pe := blk
	pe.Header.Type = BlockPrmEndReq
	wantStatus(t, decodeErr(DecodeControlBlock, withVersion(pe, 2, 0)), Status{CodeControl, DecodePNIO, Code1PrmEnd, 2})
	ar := blk
	ar.Header.Type = BlockAppReadyReq
	wantStatus(t, decodeErr(DecodeControlBlock, withVersion(ar, 2, 0)), Status{CodeControl, DecodePNIO, Code1AppReady, 2})
	wantMalformed(t, decodeErr(DecodeControlBlock, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeControlBlock, trailing(blk)))
}

func TestControlBlockResponse(t *testing.T) {
	c := ControlBlock{Type: BlockAppReadyPlugReq, ARUUID: testARUUID, SessionKey: 4, Command: CmdApplicationReady, Properties: 1, AlarmSequence: 12}
	r := c.Response()
	want := ControlBlock{Type: BlockAppReadyPlugR, ARUUID: testARUUID, SessionKey: 4, Command: CmdDone, AlarmSequence: 12}
	if r != want {
		t.Fatalf("Response %+v, want %+v", r, want)
	}
	c = ControlBlock{Type: BlockPrmEndReq, ARUUID: testARUUID, SessionKey: 4, Command: CmdPrmEnd}
	r = c.Response()
	if r.Type != BlockPrmEndRes || r.Command != CmdDone {
		t.Fatalf("Response %+v", r)
	}
	// A response of a response stays a response.
	if rr := r.Response(); rr.Type != BlockPrmEndRes {
		t.Fatalf("Response %+v", rr)
	}
}

func testReadReq() ReadReq {
	return ReadReq{Seq: 3, ARUUID: testARUUID, API: 0, Slot: 1, Subslot: 1, Index: IndexIM0, Length: 4096, TargetARUUID: rpc.UUID{0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22}}
}

func TestReadReq(t *testing.T) {
	q := testReadReq()
	b := q.Append(nil)
	// 64 octets with BlockLength 60.
	checkGolden(t, "IODReadReqHeader", b, hx(`0009 003c 0100
		0003 11111111111111111111111111111111 00000000 0001 0001 0000 aff0 00001000
		22222222222222222222222222222222
		0000000000000000`))
	if len(b) != RecordHeaderLen {
		t.Fatalf("%d octets", len(b))
	}
	blk := readOne(t, b)
	got, err := DecodeReadReq(blk)
	if err != nil || got != q {
		t.Fatalf("round trip %+v, %v", got, err)
	}

	// An older header without the target AR UUID: it stays nil.
	short := truncated(blk, 24)
	got, err = DecodeReadReq(short)
	want := q
	want.TargetARUUID = rpc.UUID{}
	if err != nil || got != want {
		t.Fatalf("short header %+v, %v", got, err)
	}
	// Fewer than 16 octets after the length: also no target AR UUID.
	got, err = DecodeReadReq(truncated(blk, 10))
	if err != nil || got != want {
		t.Fatalf("partial target %+v, %v", got, err)
	}

	wantMalformed(t, decodeErr(DecodeReadReq, truncated(blk, 25)))
	bad := blk
	bad.Header.Type = BlockWriteReq
	wantMalformed(t, decodeErr(DecodeReadReq, bad))
	wantStatus(t, decodeErr(DecodeReadReq, withVersion(blk, 2, 0)), Status{CodeRead, DecodePNIO, Code1RWRecord, 2})
}

func TestReadRes(t *testing.T) {
	q := ReadRes{Seq: 3, ARUUID: testARUUID, API: 0x3A00, Slot: 1, Subslot: 0x8001, Index: IndexIM1, Length: 54, AddValue1: 0xAAAA, AddValue2: 0x5555}
	b := q.Append(nil)
	checkGolden(t, "IODReadResHeader", b, hx(`8009 003c 0100
		0003 11111111111111111111111111111111 00003a00 0001 8001 0000 aff1 00000036 aaaa 5555
		0000000000000000000000000000000000000000`))
	blk := readOne(t, b)
	got, err := DecodeReadRes(blk)
	if err != nil || got != q {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	// The padding is not required.
	if got, err := DecodeReadRes(truncated(blk, 20)); err != nil || got != q {
		t.Fatalf("without padding %+v, %v", got, err)
	}
	wantMalformed(t, decodeErr(DecodeReadRes, truncated(blk, 21)))
	bad := blk
	bad.Header.Type = BlockReadReq
	wantMalformed(t, decodeErr(DecodeReadRes, bad))
}

func TestWriteReq(t *testing.T) {
	q := WriteReq{Seq: 0xFFFF, ARUUID: testARUUID, API: 0, Slot: 0, Subslot: 1, Index: IndexIM1, Length: 54}
	b := q.Append(nil)
	checkGolden(t, "IODWriteReqHeader", b, hx(`0008 003c 0100
		ffff 11111111111111111111111111111111 00000000 0000 0001 0000 aff1 00000036
		000000000000000000000000000000000000000000000000`))
	blk := readOne(t, b)
	got, err := DecodeWriteReq(blk)
	if err != nil || got != q {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	wantMalformed(t, decodeErr(DecodeWriteReq, truncated(blk, 25)))
	bad := blk
	bad.Header.Type = BlockWriteRes
	wantMalformed(t, decodeErr(DecodeWriteReq, bad))
	wantStatus(t, decodeErr(DecodeWriteReq, withVersion(blk, 1, 1)), Status{CodeWrite, DecodePNIO, Code1RWRecord, 3})
}

func TestWriteRes(t *testing.T) {
	q := WriteRes{Seq: 1, ARUUID: testARUUID, Slot: 1, Subslot: 1, Index: 0x0100, Length: 2, AddValue1: 1, AddValue2: 2, Status: RWStatus(true, RWInvalidIndex, 0)}
	b := q.Append(nil)
	checkGolden(t, "IODWriteResHeader", b, hx(`8008 003c 0100
		0001 11111111111111111111111111111111 00000000 0001 0001 0000 0100 00000002 0001 0002
		df80b000
		00000000000000000000000000000000`))
	blk := readOne(t, b)
	got, err := DecodeWriteRes(blk)
	if err != nil || got != q {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	// Truncated in the status: the status stays zero and decoding fails.
	got, err = DecodeWriteRes(truncated(blk, 17))
	wantMalformed(t, err)
	if !got.Status.OK() {
		t.Fatalf("status %s", got.Status)
	}
	bad := blk
	bad.Header.Type = BlockReadRes
	wantMalformed(t, decodeErr(DecodeWriteRes, bad))
}

func TestWriteMultiple(t *testing.T) {
	items := []WriteMultipleItem{
		{Header: WriteReq{Seq: 1, ARUUID: testARUUID, Slot: 1, Subslot: 1, Index: 0x10}, Data: hx("010203")},
		{Header: WriteReq{Seq: 2, ARUUID: testARUUID, Slot: 1, Subslot: 2, Index: 0x11}, Data: hx("0405060708")},
		{Header: WriteReq{Seq: 3, ARUUID: testARUUID, Slot: 2, Subslot: 1, Index: 0x12}, Data: hx("09")},
	}
	b := AppendWriteMultiple(hx("ff"), items)
	b = b[1:]
	// 64+3 padded to 68, 64+5 padded to 72 and the last, 64+1, unpadded.
	if len(b) != 68+72+65 {
		t.Fatalf("%d octets", len(b))
	}
	if !bytes.Equal(b[64:68], hx("010203 00")) || !bytes.Equal(b[68+64:68+72], hx("0405060708 000000")) {
		t.Fatalf("padding %x / %x", b[64:68], b[68+64:68+72])
	}
	// Each header carries the length of its data.
	if l := binary.BigEndian.Uint32(b[68+36:]); l != 5 {
		t.Fatalf("second length %d", l)
	}
	got, err := SplitWriteMultiple(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(items) {
		t.Fatalf("%d items", len(got))
	}
	for i := range items {
		want := items[i]
		want.Header.Length = uint32(len(want.Data))
		if !reflect.DeepEqual(got[i], want) {
			t.Fatalf("item %d\n got %+v\nwant %+v", i, got[i], want)
		}
	}
	if out, err := SplitWriteMultiple(nil); err != nil || out != nil {
		t.Fatalf("empty: %v, %v", out, err)
	}
	if b := AppendWriteMultiple(nil, nil); len(b) != 0 {
		t.Fatalf("empty append %x", b)
	}

	// Trailing padding after the last item is tolerated.
	padded := append(append([]byte(nil), b...), 0, 0, 0)
	if got, err := SplitWriteMultiple(padded); err != nil || len(got) != 3 {
		t.Fatalf("trailing padding: %d, %v", len(got), err)
	}

	t.Run("non-zero padding", func(t *testing.T) {
		bad := append([]byte(nil), b...)
		bad[67] = 1
		out, err := SplitWriteMultiple(bad)
		wantMalformed(t, err)
		if len(out) != 1 {
			t.Fatalf("%d items before the error", len(out))
		}
	})
	t.Run("length beyond data", func(t *testing.T) {
		bad := append([]byte(nil), b[:68+64+2]...)
		_, err := SplitWriteMultiple(bad)
		wantMalformed(t, err)
		if !strings.Contains(err.Error(), "WriteMultiple record of 5 octets exceeds the 2 present") {
			t.Fatalf("error %q", err)
		}
	})
	t.Run("truncated header", func(t *testing.T) {
		_, err := SplitWriteMultiple(b[:10])
		wantMalformed(t, err)
	})
	t.Run("not a write header", func(t *testing.T) {
		bad := append([]byte(nil), b...)
		binary.BigEndian.PutUint16(bad[68:], uint16(BlockReadReq))
		out, err := SplitWriteMultiple(bad)
		wantMalformed(t, err)
		if len(out) != 1 {
			t.Fatalf("%d items before the error", len(out))
		}
	})
}
