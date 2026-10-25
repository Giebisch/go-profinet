// SPDX-License-Identifier: MIT

package pnio

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/rpc"
)

var (
	testARUUID = rpc.UUID{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	testMAC    = ethernet.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
)

func testARBlockReq() ARBlockReq {
	return ARBlockReq{
		Type:                  ARTypeIOCARSingle,
		ARUUID:                testARUUID,
		SessionKey:            2,
		InitiatorMAC:          testMAC,
		InitiatorObject:       ObjectUUID(1, 0x0203, 0x0405),
		Properties:            ARPropStartupAdvanced | ARPropPrmServerCMInit | ARPropStateActive,
		ActivityTimeoutFactor: 100,
		UDPRTPort:             0x8892,
		StationName:           "plc",
	}
}

const goldenARBlockReq = `0101 0039 0100
	0001
	11111111111111111111111111111111
	0002
	020000000001
	dea000006c9711d18271 0001 0203 0405
	40000011
	0064
	8892
	0003 706c63`

func TestARBlockReq(t *testing.T) {
	a := testARBlockReq()
	b := a.Append(nil)
	checkGolden(t, "ARBlockReq", b, hx(goldenARBlockReq))
	blk := readOne(t, b)
	got, err := DecodeARBlockReq(blk)
	if err != nil {
		t.Fatal(err)
	}
	if got != a {
		t.Fatalf("round trip\n got %+v\nwant %+v", got, a)
	}

	// The longest station name, 240 octets, is accepted.
	a.StationName = strings.Repeat("s", 240)
	if got, err := DecodeARBlockReq(readOne(t, a.Append(nil))); err != nil || got.StationName != a.StationName {
		t.Fatalf("240-octet name: %v", err)
	}
	// 241 octets is ErrorCode2 12, the StationNameLength field.
	a.StationName = strings.Repeat("s", 241)
	_, err = DecodeARBlockReq(readOne(t, a.Append(nil)))
	wantStatus(t, err, Status{CodeConnect, DecodePNIO, Code1ARBlock, 12})

	wantStatus(t, decodeErr(DecodeARBlockReq, withVersion(blk, 0, 0)), Status{CodeConnect, DecodePNIO, Code1ARBlock, 2})
	wantStatus(t, decodeErr(DecodeARBlockReq, withVersion(blk, 1, 1)), Status{CodeConnect, DecodePNIO, Code1ARBlock, 3})
	wantMalformed(t, decodeErr(DecodeARBlockReq, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeARBlockReq, trailing(blk)))
	// Truncated in the fixed part, before the name length.
	wantMalformed(t, decodeErr(DecodeARBlockReq, truncated(blk, 10)))
}

// decodeErr returns the error of a decode function.
func decodeErr[T any](fn func(RawBlock) (T, error), blk RawBlock) error {
	_, err := fn(blk)
	if err == nil {
		return errors.New("decode succeeded")
	}
	return err
}

func TestARBlockRes(t *testing.T) {
	a := ARBlockRes{Type: ARTypeIOCARSingle, ARUUID: testARUUID, SessionKey: 2, ResponderMAC: ethernet.MAC{2, 0, 0, 0, 0, 2}, UDPRTPort: 0x8892}
	b := a.Append(nil)
	checkGolden(t, "ARBlockRes", b, hx(`8101 001e 0100 0001 11111111111111111111111111111111 0002 020000000002 8892`))
	blk := readOne(t, b)
	got, err := DecodeARBlockRes(blk)
	if err != nil || got != a {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	wantMalformed(t, decodeErr(DecodeARBlockRes, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeARBlockRes, trailing(blk)))
}

func testIOCRBlockReq() IOCRBlockReq {
	return IOCRBlockReq{
		Type:            IOCRInput,
		Reference:       1,
		LT:              LT,
		Properties:      uint32(RTClass2),
		DataLength:      40,
		FrameID:         0xC000,
		SendClockFactor: 32,
		ReductionRatio:  1,
		Phase:           1,
		Sequence:        0,
		FrameSendOffset: 0xFFFFFFFF,
		WatchdogFactor:  3,
		DataHoldFactor:  3,
		TagHeader:       0xC000,
		APIs: []IOCRAPI{{
			API:           0,
			IODataObjects: []IODataObject{{Slot: 0, Subslot: 1, FrameOffset: 0}},
			IOCS:          []IODataObject{{Slot: 0, Subslot: 0x8000, FrameOffset: 5}},
		}},
	}
}

func TestIOCRBlockReq(t *testing.T) {
	c := testIOCRBlockReq()
	b := c.Append(nil)
	checkGolden(t, "IOCRBlockReq", b, hx(`0102 003e 0100
		0001 0001 8892 00000002 0028 c000 0020 0001 0001 0000 ffffffff 0003 0003 c000 000000000000
		0001
		00000000 0001 0000 0001 0000 0001 0000 8000 0005`))
	if c.RTClass() != RTClass2 {
		t.Fatalf("RTClass %d", c.RTClass())
	}
	blk := readOne(t, b)
	got, err := DecodeIOCRBlockReq(blk)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip\n got %+v\nwant %+v", got, c)
	}

	// Several APIs, one without IOCS, one without data objects.
	c.Type = IOCROutput
	c.MulticastMAC = ethernet.MAC{1, 0x0e, 0xcf, 0, 0, 0}
	c.Properties = uint32(RTClass1) | 0x10
	c.APIs = []IOCRAPI{
		{API: 0, IODataObjects: []IODataObject{{0, 1, 0}, {1, 1, 3}}},
		{API: 0x3A00, IOCS: []IODataObject{{2, 1, 7}}},
	}
	got, err = DecodeIOCRBlockReq(readOne(t, c.Append(nil)))
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	if got.RTClass() != RTClass1 {
		t.Fatalf("RTClass %d", got.RTClass())
	}

	wantStatus(t, decodeErr(DecodeIOCRBlockReq, withVersion(blk, 2, 0)), Status{CodeConnect, DecodePNIO, Code1IOCRBlock, 2})
	wantMalformed(t, decodeErr(DecodeIOCRBlockReq, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeIOCRBlockReq, trailing(blk)))

	// A hostile NumberOfAPIs is refused by the count bound.
	bad := readOne(t, b)
	bad.Body = append([]byte(nil), bad.Body...)
	binary.BigEndian.PutUint16(bad.Body[38:], 0xFFFF)
	wantMalformed(t, decodeErr(DecodeIOCRBlockReq, bad))
	// So is a hostile NumberOfIODataObjects.
	binary.BigEndian.PutUint16(bad.Body[38:], 1)
	binary.BigEndian.PutUint16(bad.Body[44:], 0x1000)
	wantMalformed(t, decodeErr(DecodeIOCRBlockReq, bad))
}

func TestIOCRBlockRes(t *testing.T) {
	c := IOCRBlockRes{Type: IOCROutput, Reference: 2, FrameID: 0xC001}
	b := c.Append(nil)
	checkGolden(t, "IOCRBlockRes", b, hx("8102 0008 0100 0002 0002 c001"))
	blk := readOne(t, b)
	if got, err := DecodeIOCRBlockRes(blk); err != nil || got != c {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	wantMalformed(t, decodeErr(DecodeIOCRBlockRes, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeIOCRBlockRes, trailing(blk)))
}

func testAlarmCR() AlarmCRBlockReq {
	return AlarmCRBlockReq{Type: 1, LT: LT, Properties: 0, RTATimeoutFactor: 1, RTARetries: 3, LocalAlarmReference: 1, MaxAlarmDataLength: 200, TagHeaderHigh: 0xC000, TagHeaderLow: 0xA000}
}

func TestAlarmCRBlockReq(t *testing.T) {
	a := testAlarmCR()
	b := a.Append(nil)
	// BlockLength 22.
	checkGolden(t, "AlarmCRBlockReq", b, hx("0103 0016 0100 0001 8892 00000000 0001 0003 0001 00c8 c000 a000"))
	blk := readOne(t, b)
	if got, err := DecodeAlarmCRBlockReq(blk); err != nil || got != a {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	wantStatus(t, decodeErr(DecodeAlarmCRBlockReq, withVersion(blk, 1, 2)), Status{CodeConnect, DecodePNIO, Code1AlarmCR, 3})
	wantMalformed(t, decodeErr(DecodeAlarmCRBlockReq, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeAlarmCRBlockReq, trailing(blk)))
}

func TestAlarmCRBlockRes(t *testing.T) {
	a := AlarmCRBlockRes{Type: 1, LocalAlarmReference: 3, MaxAlarmDataLength: 200}
	b := a.Append(nil)
	checkGolden(t, "AlarmCRBlockRes", b, hx("8103 0008 0100 0001 0003 00c8"))
	blk := readOne(t, b)
	if got, err := DecodeAlarmCRBlockRes(blk); err != nil || got != a {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	wantMalformed(t, decodeErr(DecodeAlarmCRBlockRes, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeAlarmCRBlockRes, trailing(blk)))
}

func TestNewDataDescriptions(t *testing.T) {
	for _, c := range []struct {
		t       SubmoduleType
		types   []uint16
		in, out uint16
	}{
		{SubmoduleNoIO, []uint16{DataInput}, 0, 0},
		{SubmoduleInput, []uint16{DataInput}, 4, 0},
		{SubmoduleOutput, []uint16{DataOutput}, 0, 2},
		{SubmoduleInputOutput, []uint16{DataInput, DataOutput}, 4, 2},
	} {
		d := NewDataDescriptions(c.t, c.in, c.out)
		if len(d) != len(c.types) {
			t.Fatalf("%s: %d descriptions", c.t, len(d))
		}
		for i, dd := range d {
			if dd.Type != c.types[i] || dd.LengthIOCS != 1 || dd.LengthIOPS != 1 {
				t.Fatalf("%s: description %d = %+v", c.t, i, dd)
			}
		}
		sm := ExpectedSubmodule{Properties: uint16(c.t), Descriptions: d}
		if sm.Type() != c.t || sm.InputLength() != c.in || sm.OutputLength() != c.out {
			t.Fatalf("%s: type %s in %d out %d", c.t, sm.Type(), sm.InputLength(), sm.OutputLength())
		}
	}
	// The submodule type ignores the other property bits.
	sm := ExpectedSubmodule{Properties: 0x0020 | uint16(SubmoduleOutput)}
	if sm.Type() != SubmoduleOutput || sm.InputLength() != 0 || sm.OutputLength() != 0 {
		t.Fatalf("type %s", sm.Type())
	}
}

func testExpected() ExpectedSubmoduleBlockReq {
	return ExpectedSubmoduleBlockReq{Modules: []ExpectedModule{
		{API: 0, Slot: 0, IdentNumber: 1, Submodules: []ExpectedSubmodule{
			{Subslot: 1, IdentNumber: 1, Properties: uint16(SubmoduleNoIO), Descriptions: NewDataDescriptions(SubmoduleNoIO, 0, 0)},
			{Subslot: 0x8000, IdentNumber: 0x8000, Properties: uint16(SubmoduleNoIO), Descriptions: NewDataDescriptions(SubmoduleNoIO, 0, 0)},
		}},
		{API: 0, Slot: 1, IdentNumber: 0x10, Submodules: []ExpectedSubmodule{
			{Subslot: 1, IdentNumber: 0x11, Properties: uint16(SubmoduleInput), Descriptions: NewDataDescriptions(SubmoduleInput, 8, 0)},
			{Subslot: 2, IdentNumber: 0x12, Properties: uint16(SubmoduleOutput), Descriptions: NewDataDescriptions(SubmoduleOutput, 0, 2)},
			{Subslot: 3, IdentNumber: 0x13, Properties: uint16(SubmoduleInputOutput), Descriptions: NewDataDescriptions(SubmoduleInputOutput, 4, 6)},
		}},
	}}
}

func TestExpectedSubmoduleBlockReq(t *testing.T) {
	e := ExpectedSubmoduleBlockReq{Modules: []ExpectedModule{{API: 0, Slot: 1, IdentNumber: 0x10, Submodules: []ExpectedSubmodule{
		{Subslot: 1, IdentNumber: 0x11, Properties: uint16(SubmoduleInputOutput), Descriptions: NewDataDescriptions(SubmoduleInputOutput, 2, 4)},
	}}}}
	b := e.Append(nil)
	checkGolden(t, "ExpectedSubmoduleBlockReq", b, hx(`0104 0026 0100
		0001
		00000000 0001 00000010 0000 0001
		0001 00000011 0003
		0001 0002 01 01
		0002 0004 01 01`))
	blk := readOne(t, b)
	got, err := DecodeExpectedSubmoduleBlockReq(blk)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("round trip %+v, %v", got, err)
	}

	// Every submodule type: the number of descriptions follows the type.
	e = testExpected()
	got, err = DecodeExpectedSubmoduleBlockReq(readOne(t, e.Append(nil)))
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("round trip\n got %+v\nwant %+v\n%v", got, e, err)
	}
	for _, m := range got.Modules {
		for _, sm := range m.Submodules {
			want := 1
			if sm.Type() == SubmoduleInputOutput {
				want = 2
			}
			if len(sm.Descriptions) != want {
				t.Fatalf("%s: %d descriptions", sm.Type(), len(sm.Descriptions))
			}
		}
	}

	wantStatus(t, decodeErr(DecodeExpectedSubmoduleBlockReq, withVersion(blk, 2, 0)), Status{CodeConnect, DecodePNIO, Code1ExpSubm, 2})
	wantMalformed(t, decodeErr(DecodeExpectedSubmoduleBlockReq, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeExpectedSubmoduleBlockReq, trailing(blk)))

	// An invalid DataDescription type (only 1 and 2 exist).
	bad := readOne(t, b)
	bad.Body = append([]byte(nil), bad.Body...)
	binary.BigEndian.PutUint16(bad.Body[30:], 3)
	err = decodeErr(DecodeExpectedSubmoduleBlockReq, bad)
	wantMalformed(t, err)
	if !strings.Contains(err.Error(), "data description type 3") {
		t.Fatalf("error %q", err)
	}
	// A hostile module count.
	binary.BigEndian.PutUint16(bad.Body[0:], 3)
	wantMalformed(t, decodeErr(DecodeExpectedSubmoduleBlockReq, bad))
}

func TestModuleDiffBlock(t *testing.T) {
	m := ModuleDiffBlock{APIs: []DiffAPI{{API: 0, Modules: []DiffModule{{
		Slot: 1, IdentNumber: 0x10, State: ModuleStateProper,
		Submodules: []DiffSubmodule{{Subslot: 1, IdentNumber: 0x11, State: SubmStateFormat | SubmStateIdentWrong}},
	}}}}}
	b := m.Append(nil)
	checkGolden(t, "ModuleDiffBlock", b, hx(`8104 001c 0100
		0001 00000000 0001
		0001 00000010 0002 0001
		0001 00000011 9000`))
	blk := readOne(t, b)
	got, err := DecodeModuleDiffBlock(blk)
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("round trip %+v, %v", got, err)
	}

	// Several APIs and a module without submodules.
	m.APIs = append(m.APIs, DiffAPI{API: 0x3A00, Modules: []DiffModule{{Slot: 4, IdentNumber: 0x40, State: ModuleStateNoModule}}})
	got, err = DecodeModuleDiffBlock(readOne(t, m.Append(nil)))
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	// An empty block.
	if got, err := DecodeModuleDiffBlock(readOne(t, (&ModuleDiffBlock{}).Append(nil))); err != nil || got.APIs != nil {
		t.Fatalf("empty: %+v, %v", got, err)
	}

	wantMalformed(t, decodeErr(DecodeModuleDiffBlock, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeModuleDiffBlock, trailing(blk)))
}

func TestARServerBlock(t *testing.T) {
	for _, c := range []struct {
		name, golden string
	}{
		// 6 + 2 + 3 = 11 octets, padded to 12.
		{"dev", "8106 0008 0100 0003 646576 00"},
		// 6 + 2 + 4 = 12 octets, no padding.
		{"devi", "8106 0008 0100 0004 64657669"},
		// 6 + 2 = 8 octets, no padding.
		{"", "8106 0004 0100 0000"},
		// 6 + 2 + 1 = 9 octets, three octets of padding.
		{"d", "8106 0008 0100 0001 64 000000"},
	} {
		a := ARServerBlock{StationName: c.name}
		b := a.Append(nil)
		checkGolden(t, "ARServerBlock "+c.name, b, hx(c.golden))
		if len(b)%4 != 0 {
			t.Fatalf("%q: %d octets", c.name, len(b))
		}
		got, err := DecodeARServerBlock(readOne(t, b))
		if err != nil || got != a {
			t.Fatalf("%q: round trip %+v, %v", c.name, got, err)
		}
	}
	// Padding is relative to the start of the block, not of the buffer.
	b := (&ARServerBlock{StationName: "dev"}).Append(hx("aa"))
	checkGolden(t, "ARServerBlock after one octet", b, hx("aa 8106 0008 0100 0003 646576 00"))

	blk := readOne(t, (&ARServerBlock{StationName: "dev"}).Append(nil))
	wantMalformed(t, decodeErr(DecodeARServerBlock, truncated(blk, 2)))
	wantMalformed(t, decodeErr(DecodeARServerBlock, RawBlock{Header: blk.Header, Body: hx("00")}))
}

func TestARRPCBlockReq(t *testing.T) {
	a := ARRPCBlockReq{InitiatorRPCServerPort: 0x8894}
	b := a.Append(nil)
	checkGolden(t, "ARRPCBlockReq", b, hx("0107 0004 0100 8894"))
	blk := readOne(t, b)
	if got, err := DecodeARRPCBlockReq(blk); err != nil || got != a {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	wantMalformed(t, decodeErr(DecodeARRPCBlockReq, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeARRPCBlockReq, trailing(blk)))
}

func TestDecodeErrorsAreMalformed(t *testing.T) {
	// The decoders never return a bare error: a reader failure matches
	// ErrMalformed, a version failure ErrRejected via *StatusError.
	blk := readOne(t, (&ARRPCBlockReq{}).Append(nil))
	err := decodeErr(DecodeARRPCBlockReq, truncated(blk, 2))
	if !errors.Is(err, profinet.ErrMalformed) || errors.Is(err, profinet.ErrRejected) {
		t.Fatalf("error %v", err)
	}
}
