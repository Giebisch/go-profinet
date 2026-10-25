// SPDX-License-Identifier: MIT

package pnio

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
)

// hx decodes a hexadecimal string, ignoring blanks and newlines.
func hx(s string) []byte {
	s = strings.Join(strings.Fields(s), "")
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// readOne decodes the single block that fills b.
func readOne(t testing.TB, b []byte) RawBlock {
	t.Helper()
	blk, rest, err := ReadBlock(b)
	if err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("ReadBlock left %d octets", len(rest))
	}
	return blk
}

// checkGolden compares an encoding against the expected octets.
func checkGolden(t *testing.T, name string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("%s encoding\n got %x\nwant %x", name, got, want)
	}
}

// wantStatus checks that err is a *StatusError carrying s.
func wantStatus(t *testing.T, err error, s Status) {
	t.Helper()
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v (%T) is not a *StatusError", err, err)
	}
	if se.Status != s {
		t.Fatalf("status %s, want %s", se.Status, s)
	}
	if !errors.Is(err, profinet.ErrRejected) {
		t.Fatalf("%v does not match ErrRejected", err)
	}
}

// wantMalformed checks that err matches profinet.ErrMalformed.
func wantMalformed(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("error %v does not match ErrMalformed", err)
	}
}

// withVersion returns a copy of blk with another version.
func withVersion(blk RawBlock, high, low uint8) RawBlock {
	blk.Header.VersionHigh, blk.Header.VersionLow = high, low
	return blk
}

// truncated returns a copy of blk whose body lacks its last n octets.
func truncated(blk RawBlock, n int) RawBlock {
	blk.Body = blk.Body[:len(blk.Body)-n]
	return blk
}

// trailing returns a copy of blk whose body has one octet more.
func trailing(blk RawBlock) RawBlock {
	blk.Body = append(append([]byte(nil), blk.Body...), 0)
	return blk
}

func TestReadBlock(t *testing.T) {
	b := hx("0114 0004 0100 aabb 0115")
	blk, rest, err := ReadBlock(b)
	if err != nil {
		t.Fatal(err)
	}
	want := BlockHeader{Type: BlockReleaseReq, Length: 4, VersionHigh: 1, VersionLow: 0}
	if blk.Header != want {
		t.Fatalf("header %+v, want %+v", blk.Header, want)
	}
	if !bytes.Equal(blk.Body, hx("aabb")) || !bytes.Equal(rest, hx("0115")) {
		t.Fatalf("body %x rest %x", blk.Body, rest)
	}
	// The body aliases the input.
	b[6] = 0xcc
	if blk.Body[0] != 0xcc {
		t.Fatal("body does not alias the input")
	}

	// Length 2: a block with only the version.
	blk, rest, err = ReadBlock(hx("8114 0002 0100"))
	if err != nil || len(blk.Body) != 0 || len(rest) != 0 {
		t.Fatalf("empty body: %v %x %x", err, blk.Body, rest)
	}
}

func TestReadBlockErrors(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":         nil,
		"short header":  hx("0101 0002 01"),
		"length 0":      hx("0101 0000 0100"),
		"length 1":      hx("0101 0001 0100"),
		"beyond data":   hx("0101 0005 0100 0000"),
		"beyond by one": hx("0101 0004 0100 00"),
	} {
		t.Run(name, func(t *testing.T) {
			_, rest, err := ReadBlock(b)
			wantMalformed(t, err)
			if rest != nil {
				t.Fatalf("rest %x on error", rest)
			}
		})
	}
}

func TestSplitBlocks(t *testing.T) {
	b := hx("0114 0004 0100 0102  8114 0002 0100  0009 0003 0100 ff")
	blocks, err := SplitBlocks(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[0].Header.Type != BlockReleaseReq || blocks[1].Header.Type != BlockReleaseRes || blocks[2].Header.Type != BlockReadReq {
		t.Fatalf("blocks %+v", blocks)
	}
	if !bytes.Equal(blocks[2].Body, []byte{0xff}) {
		t.Fatalf("third body %x", blocks[2].Body)
	}
	if out, err := SplitBlocks(nil); err != nil || out != nil {
		t.Fatalf("SplitBlocks(nil) = %v, %v", out, err)
	}

	// A truncated second block returns the first and the error.
	out, err := SplitBlocks(hx("0114 0002 0100  8114 0009 0100"))
	wantMalformed(t, err)
	if len(out) != 1 || out[0].Header.Type != BlockReleaseReq {
		t.Fatalf("partial result %+v", out)
	}
}

func TestRawBlockAppend(t *testing.T) {
	r := RawBlock{Header: BlockHeader{Type: BlockARVendorReq, VersionHigh: 1, VersionLow: 1}, Body: hx("deadbeef")}
	got := r.Append(hx("ff"))
	checkGolden(t, "RawBlock", got, hx("ff 0108 0006 0101 deadbeef"))
	back := readOne(t, got[1:])
	if back.Header.Length != 6 || !bytes.Equal(back.Body, r.Body) || back.Header.Type != r.Header.Type || back.Header.VersionLow != 1 {
		t.Fatalf("round trip %+v", back)
	}
}

func TestCheckVersion(t *testing.T) {
	blk := RawBlock{Header: BlockHeader{Type: BlockIOCRReq, Length: 2, VersionHigh: 1, VersionLow: 0}}
	if err := checkVersion(blk, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := checkVersion(withVersion(blk, 1, 1), 1, 1); err != nil {
		t.Fatal(err)
	}
	err := checkVersion(withVersion(blk, 2, 0), 1, 0)
	wantStatus(t, err, Status{Code: CodeConnect, Decode: DecodePNIO, Code1: Code1IOCRBlock, Code2: 2})
	if !strings.Contains(err.Error(), "decode IOCRBlockReq") {
		t.Fatalf("error text %q", err)
	}
	if !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("%v does not match ErrMalformed", err)
	}
	// A VersionLow beyond the supported one names field 3 (VersionLow).
	err = checkVersion(withVersion(blk, 1, 1), 1, 0)
	wantStatus(t, err, Status{Code: CodeConnect, Decode: DecodePNIO, Code1: Code1IOCRBlock, Code2: 3})
}

func TestBlockError(t *testing.T) {
	for _, c := range []struct {
		t    BlockType
		want Status
	}{
		{BlockARReq, Status{CodeConnect, DecodePNIO, Code1ARBlock, 5}},
		{BlockIOCRReq, Status{CodeConnect, DecodePNIO, Code1IOCRBlock, 5}},
		{BlockExpectedSubmReq, Status{CodeConnect, DecodePNIO, Code1ExpSubm, 5}},
		{BlockAlarmCRReq, Status{CodeConnect, DecodePNIO, Code1AlarmCR, 5}},
		{BlockARRPCReq, Status{CodeConnect, DecodePNIO, Code1ARRPC, 5}},
		{BlockPrmEndReq, Status{CodeControl, DecodePNIO, Code1PrmEnd, 5}},
		{BlockPrmBeginReq, Status{CodeControl, DecodePNIO, Code1PrmBegin, 5}},
		{BlockAppReadyReq, Status{CodeControl, DecodePNIO, Code1AppReady, 5}},
		{BlockReleaseReq, Status{CodeRelease, DecodePNIO, Code1Release, 5}},
		{BlockReadReq, Status{CodeRead, DecodePNIO, Code1RWRecord, 5}},
		{BlockWriteReq, Status{CodeWrite, DecodePNIO, Code1RWRecord, 5}},
		// Blocks without their own ErrorCode1 report CMRPC unknown blocks.
		{BlockARVendorReq, Status{CodeConnect, DecodePNIO, Code1CMRPC, CMRPCUnknownBlocks}},
		{BlockPrmEndPlugReq, Status{CodeControl, DecodePNIO, Code1PrmEndPlg, 5}},
		{BlockAppReadyPlugReq, Status{CodeControl, DecodePNIO, Code1AppRdyPlg, 5}},
	} {
		if got := blockError(c.t, 5); got != c.want {
			t.Errorf("blockError(%s) = %s, want %s", c.t, got, c.want)
		}
	}
}

func TestBlockTypeString(t *testing.T) {
	for bt, want := range map[BlockType]string{
		BlockAlarmNotificationHigh: "AlarmNotification-High",
		BlockAlarmNotificationLow:  "AlarmNotification-Low",
		BlockWriteReq:              "IODWriteReqHeader",
		BlockReadReq:               "IODReadReqHeader",
		BlockDiagnosisData:         "DiagnosisData",
		BlockRealIdentData:         "RealIdentificationData",
		BlockExpectedIdentData:     "ExpectedIdentificationData",
		BlockIM0:                   "I&M0",
		BlockIM1:                   "I&M1",
		BlockIM2:                   "I&M2",
		BlockIM3:                   "I&M3",
		BlockIM4:                   "I&M4",
		BlockARReq:                 "ARBlockReq",
		BlockIOCRReq:               "IOCRBlockReq",
		BlockAlarmCRReq:            "AlarmCRBlockReq",
		BlockExpectedSubmReq:       "ExpectedSubmoduleBlockReq",
		BlockARRPCReq:              "ARRPCBlockReq",
		BlockPrmEndReq:             "IODControlReq(PrmEnd)",
		BlockAppReadyReq:           "IOXControlReq(ApplicationReady)",
		BlockReleaseReq:            "ReleaseBlockReq",
		BlockPrmBeginReq:           "IODControlReq(PrmBegin)",
		BlockAlarmAckHigh:          "AlarmAck-High",
		BlockAlarmAckLow:           "AlarmAck-Low",
		BlockWriteRes:              "IODWriteResHeader",
		BlockReadRes:               "IODReadResHeader",
		BlockARRes:                 "ARBlockRes",
		BlockIOCRRes:               "IOCRBlockRes",
		BlockAlarmCRRes:            "AlarmCRBlockRes",
		BlockModuleDiff:            "ModuleDiffBlock",
		BlockARServer:              "ARServerBlock",
		BlockARRPCRes:              "ARRPCBlockRes",
		BlockPrmEndRes:             "IODControlRes(PrmEnd)",
		BlockAppReadyRes:           "IOXControlRes(ApplicationReady)",
		BlockReleaseRes:            "ReleaseBlockRes",
		BlockPrmBeginRes:           "IODControlRes(PrmBegin)",
		BlockMCRReq:                "Block(0x0106)",
		BlockType(0xABCD):          "Block(0xABCD)",
	} {
		if got := bt.String(); got != want {
			t.Errorf("BlockType(0x%04X).String() = %q, want %q", uint16(bt), got, want)
		}
	}
}

func TestReader(t *testing.T) {
	r := newReader(hx("01 0203 04050607 0809"), "test")
	if r.u8() != 1 || r.u16() != 0x0203 || r.u32() != 0x04050607 {
		t.Fatal("reads")
	}
	if r.remaining() != 2 {
		t.Fatalf("remaining %d", r.remaining())
	}
	c := r.copyBytes(1)
	if !bytes.Equal(c, []byte{8}) {
		t.Fatalf("copyBytes %x", c)
	}
	r.b[7] = 0xff
	if c[0] != 8 {
		t.Fatal("copyBytes aliases the input")
	}
	if err := r.end(); err == nil {
		t.Fatal("end with a trailing octet succeeded")
	} else {
		wantMalformed(t, err)
	}

	// Sticky error: every read after the first short one is zero.
	r = newReader(hx("0102"), "sticky")
	if r.u32() != 0 || r.err == nil {
		t.Fatal("short u32")
	}
	if r.u8() != 0 || r.u16() != 0 || r.bytes(1) != nil || r.copyBytes(1) != nil || r.rest() != nil {
		t.Fatal("reads after the error")
	}
	if !r.uuid().IsNil() || !r.mac().IsZero() {
		t.Fatal("uuid/mac after the error")
	}
	if !strings.Contains(r.err.Error(), "sticky truncated at offset 0 (need 4 of 2 octets)") {
		t.Fatalf("error %q", r.err)
	}
	// The first error is kept.
	first := r.err
	r.skip(10)
	if r.end() != first {
		t.Fatal("error replaced")
	}

	// Negative lengths are refused.
	r = newReader(hx("00"), "neg")
	if r.bytes(-1) != nil || r.err == nil {
		t.Fatal("negative length accepted")
	}

	// rest consumes everything.
	r = newReader(hx("0102 0304"), "rest")
	r.u16()
	if !bytes.Equal(r.rest(), hx("0304")) || r.remaining() != 0 || r.end() != nil {
		t.Fatal("rest")
	}
}

func TestReaderCount(t *testing.T) {
	// Two elements of three octets each fit exactly.
	r := newReader(hx("0002 000000 000000"), "count")
	if n := r.count(3); n != 2 || r.err != nil {
		t.Fatalf("count = %d, %v", n, r.err)
	}
	// A count beyond the octets left fails without returning it.
	r = newReader(hx("0003 000000 000000"), "count")
	if n := r.count(3); n != 0 {
		t.Fatalf("hostile count = %d", n)
	}
	wantMalformed(t, r.err)
	if !strings.Contains(r.err.Error(), "count 3 exceeds the 6 octets left") {
		t.Fatalf("error %q", r.err)
	}
	// A huge count with nothing behind it.
	r = newReader(hx("ffff"), "count")
	if n := r.count(1); n != 0 || r.err == nil {
		t.Fatalf("count = %d, %v", n, r.err)
	}
	// A missing count field.
	r = newReader(hx("00"), "count")
	if n := r.count(1); n != 0 || r.err == nil {
		t.Fatalf("count = %d, %v", n, r.err)
	}
}

func TestPadAndStrings(t *testing.T) {
	b := padTo(hx("aa bb"), 0, 4)
	checkGolden(t, "padTo", b, hx("aabb0000"))
	b = padTo(hx("aa bb cc dd"), 0, 4)
	checkGolden(t, "padTo aligned", b, hx("aabbccdd"))
	b = padTo(hx("ff aa"), 1, 4)
	checkGolden(t, "padTo offset", b, hx("ffaa000000"))

	checkGolden(t, "fixedString pad", fixedString(nil, "ab", 4), []byte("ab  "))
	checkGolden(t, "fixedString exact", fixedString(nil, "abcd", 4), []byte("abcd"))
	checkGolden(t, "fixedString truncate", fixedString(nil, "abcdef", 4), []byte("abcd"))
	checkGolden(t, "fixedString empty", fixedString(nil, "", 3), []byte("   "))

	for in, want := range map[string]string{
		"ab  ":       "ab",
		"ab\x00\x00": "ab",
		"a b \x00 ":  "a b",
		"    ":       "",
		"":           "",
		" x":         " x",
	} {
		if got := trimString([]byte(in)); got != want {
			t.Errorf("trimString(%q) = %q, want %q", in, got, want)
		}
	}
}
