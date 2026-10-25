// SPDX-License-Identifier: MIT

package pnio

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
)

func testItems() []DiagnosisItem {
	return []DiagnosisItem{
		{ChannelNumber: 0, ChannelProperties: ChanSpecAppears | ChanDirInput | 0x01, ErrorType: ChannelErrShortCircuit, ExtErrorType: 0x8000, ExtAddValue: 0x01020304, Qualifier: 0x00000080},
		{ChannelNumber: ChannelNumberSubmodule, ChannelProperties: ChanSpecDisappears | ChanAccumulative, ErrorType: ChannelErrRemoteMismat, ExtErrorType: 0x8005, ExtAddValue: 0xFFFFFFFF, Qualifier: 0xF8000000},
	}
}

func TestDiagnosisItems(t *testing.T) {
	items := testItems()
	for _, c := range []struct {
		usi    uint16
		golden string
		keep   func(DiagnosisItem) DiagnosisItem
	}{
		{USIChannelDiagnosis, "0000 2801 0001  8000 1100 8001",
			func(d DiagnosisItem) DiagnosisItem { d.ExtErrorType, d.ExtAddValue, d.Qualifier = 0, 0, 0; return d }},
		{USIExtChannelDiagnosis, "0000 2801 0001 8000 01020304  8000 1100 8001 8005 ffffffff",
			func(d DiagnosisItem) DiagnosisItem { d.Qualifier = 0; return d }},
		{USIQualifiedChannel, "0000 2801 0001 8000 01020304 00000080  8000 1100 8001 8005 ffffffff f8000000",
			func(d DiagnosisItem) DiagnosisItem { return d }},
	} {
		b := AppendDiagnosisItems(nil, c.usi, items)
		checkGolden(t, "items", b, hx(c.golden))
		got, err := DecodeDiagnosisItems(c.usi, b)
		if err != nil {
			t.Fatalf("0x%04X: %v", c.usi, err)
		}
		want := []DiagnosisItem{c.keep(items[0]), c.keep(items[1])}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("0x%04X: round trip\n got %+v\nwant %+v", c.usi, got, want)
		}
		// An item size off by one octet is malformed.
		_, err = DecodeDiagnosisItems(c.usi, b[:len(b)-1])
		wantMalformed(t, err)
		// No items.
		if got, err := DecodeDiagnosisItems(c.usi, nil); err != nil || len(got) != 0 {
			t.Fatalf("empty: %v, %v", got, err)
		}
	}
	for _, usi := range []uint16{USIMultiple, USIMaintenance, 0x0001, 0x7FFF} {
		_, err := DecodeDiagnosisItems(usi, nil)
		if !errors.Is(err, profinet.ErrUnsupported) {
			t.Fatalf("USI 0x%04X: %v", usi, err)
		}
	}
	if !items[0].Appears() || items[1].Appears() {
		t.Fatal("Appears")
	}
	if (&DiagnosisItem{ChannelProperties: ChanSpecOtherRemain}).Appears() {
		t.Fatal("Appears for other-remain")
	}
}

func TestDiagnosisData(t *testing.T) {
	d := DiagnosisData{API: 0x3A00, Slot: 1, Subslot: 1, ChannelNumber: ChannelNumberSubmodule, ChannelProperties: ChanSpecAppears, USI: USIExtChannelDiagnosis, Items: testItems()[:1]}
	d.Items[0].Qualifier = 0
	b := d.Append(nil)
	checkGolden(t, "DiagnosisData", b, hx(`0010 001c 0101
		00003a00 0001 0001 8000 0800 8002
		0000 2801 0001 8000 01020304`))
	blk := readOne(t, b)
	got, err := DecodeDiagnosisData(blk)
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("round trip %+v, %v", got, err)
	}

	// Version 1.0 has no API.
	v10 := RawBlock{Header: BlockHeader{Type: BlockDiagnosisData, VersionHigh: 1, VersionLow: 0}, Body: hx("0002 0003 8000 0800 8000  0000 0801 0001")}
	got, err = DecodeDiagnosisData(v10)
	want := DiagnosisData{Slot: 2, Subslot: 3, ChannelNumber: 0x8000, ChannelProperties: 0x0800, USI: USIChannelDiagnosis,
		Items: []DiagnosisItem{{ChannelNumber: 0, ChannelProperties: 0x0801, ErrorType: 1}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("v1.0\n got %+v\nwant %+v\n%v", got, want, err)
	}

	// Manufacturer data is kept as is, copied.
	m := DiagnosisData{Slot: 1, Subslot: 1, ChannelNumber: ChannelNumberSubmodule, USI: 0x1234, Data: hx("deadbeef")}
	b = m.Append(nil)
	checkGolden(t, "manufacturer DiagnosisData", b, hx("0010 0014 0101 00000000 0001 0001 8000 0000 1234 deadbeef"))
	blk = readOne(t, b)
	got, err = DecodeDiagnosisData(blk)
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("manufacturer round trip %+v, %v", got, err)
	}
	blk.Body[len(blk.Body)-1] = 0
	if got.Data[3] != 0xef {
		t.Fatal("manufacturer data aliases the input")
	}
	// Maintenance (0x8100) is not a channel format: kept as data.
	mt := DiagnosisData{USI: USIMaintenance, Data: hx("0f00 0004 0100 0000 0001")}
	if got, err := DecodeDiagnosisData(readOne(t, mt.Append(nil))); err != nil || !reflect.DeepEqual(got, mt) {
		t.Fatalf("maintenance %+v, %v", got, err)
	}

	// Errors.
	bad := readOne(t, d.Append(nil))
	wrong := bad
	wrong.Header.Type = BlockAlarmNotificationLow
	wantMalformed(t, decodeErr(DecodeDiagnosisData, wrong))
	wantMalformed(t, decodeErr(DecodeDiagnosisData, RawBlock{Header: bad.Header, Body: bad.Body[:13]}))
	// An item of the wrong size.
	got, err = DecodeDiagnosisData(truncated(bad, 1))
	wantMalformed(t, err)
	if got.USI != USIExtChannelDiagnosis {
		t.Fatalf("fields before the items: %+v", got)
	}
}

func testAlarm() AlarmNotification {
	return AlarmNotification{High: true, Type: AlarmDiagnosis, API: 0, Slot: 1, Subslot: 1, ModuleIdentNumber: 0x10, SubmoduleIdentNumber: 0x11,
		Specifier: AlarmSpecChannelDiag | AlarmSpecSubmoduleDiag | 0x0005, HasItem: true, USI: USIChannelDiagnosis, Data: hx("0000 0801 0001")}
}

func TestAlarmNotification(t *testing.T) {
	a := testAlarm()
	b := a.Append(nil)
	checkGolden(t, "AlarmNotification", b, hx(`0001 001e 0100
		0001 00000000 0001 0001 00000010 00000011 2805
		8000 0000 0801 0001`))
	blk := readOne(t, b)
	got, err := DecodeAlarmNotification(blk)
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	if got.Sequence() != 5 {
		t.Fatalf("Sequence %d", got.Sequence())
	}
	if (&AlarmNotification{Specifier: 0xFFFF}).Sequence() != 0x07FF {
		t.Fatal("Sequence mask")
	}

	// Without an item, low priority.
	n := AlarmNotification{Type: AlarmPull, Slot: 2, Subslot: 1, Specifier: 1}
	b = n.Append(nil)
	checkGolden(t, "AlarmNotification without item", b, hx("0002 0016 0100 0003 00000000 0002 0001 00000000 00000000 0001"))
	got, err = DecodeAlarmNotification(readOne(t, b))
	if err != nil || !reflect.DeepEqual(got, n) {
		t.Fatalf("round trip %+v, %v", got, err)
	}
	// A USI without data.
	u := AlarmNotification{Type: AlarmProcess, HasItem: true, USI: 0x0001}
	got, err = DecodeAlarmNotification(readOne(t, u.Append(nil)))
	if err != nil || !reflect.DeepEqual(got, u) {
		t.Fatalf("USI only %+v, %v", got, err)
	}

	// One octet after the specifier is neither nothing nor an item.
	err = decodeErr(DecodeAlarmNotification, trailing(readOne(t, n.Append(nil))))
	wantMalformed(t, err)
	if !strings.Contains(err.Error(), "1-octet item") {
		t.Fatalf("error %q", err)
	}
	wantMalformed(t, decodeErr(DecodeAlarmNotification, truncated(readOne(t, n.Append(nil)), 1)))
	wrong := blk
	wrong.Header.Type = BlockAlarmAckHigh
	wantMalformed(t, decodeErr(DecodeAlarmNotification, wrong))
}

func TestAlarmAck(t *testing.T) {
	a := testAlarm()
	ack := AckFor(&a, Status{})
	want := AlarmAck{High: true, Type: AlarmDiagnosis, Slot: 1, Subslot: 1, Specifier: a.Specifier}
	if ack != want {
		t.Fatalf("AckFor %+v", ack)
	}
	b := ack.Append(nil)
	checkGolden(t, "AlarmAck", b, hx("8001 0012 0100 0001 00000000 0001 0001 2805 00000000"))
	got, err := DecodeAlarmAck(readOne(t, b))
	if err != nil || got != ack {
		t.Fatalf("round trip %+v, %v", got, err)
	}

	low := AlarmNotification{Type: AlarmPlug, API: 7, Slot: 3, Subslot: 2, Specifier: 9}
	ack = AckFor(&low, Status{CodeAlarmAck, DecodePNIO, Code1AlarmAck, 1})
	b = ack.Append(nil)
	checkGolden(t, "AlarmAck low", b, hx("8002 0012 0100 0004 00000007 0003 0002 0009 da813c01"))
	blk := readOne(t, b)
	got, err = DecodeAlarmAck(blk)
	if err != nil || got != ack || got.High {
		t.Fatalf("round trip %+v, %v", got, err)
	}

	wrong := blk
	wrong.Header.Type = BlockAlarmNotificationLow
	wantMalformed(t, decodeErr(DecodeAlarmAck, wrong))
	got, err = DecodeAlarmAck(truncated(blk, 1))
	wantMalformed(t, err)
	if !got.Status.OK() {
		t.Fatal("status of a truncated ack")
	}
	wantMalformed(t, decodeErr(DecodeAlarmAck, trailing(blk)))
}

func TestIdentificationData(t *testing.T) {
	d := IdentificationData{APIs: []IdentAPI{{API: 0, Slots: []IdentSlot{{Slot: 0, IdentNumber: 1, Submodules: []IdentSubmodule{{Subslot: 1, IdentNumber: 1}, {Subslot: 0x8000, IdentNumber: 2}}}}}}}
	b := d.Append(nil)
	checkGolden(t, "RealIdentificationData", b, hx(`0013 001e 0101
		0001 00000000 0001
		0000 00000001 0002
		0001 00000001  8000 00000002`))
	blk := readOne(t, b)
	got, err := DecodeIdentificationData(blk)
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("round trip %+v, %v", got, err)
	}

	e := IdentificationData{Expected: true, APIs: []IdentAPI{
		{API: 0, Slots: []IdentSlot{{Slot: 1, IdentNumber: 0x10}}},
		{API: 0x3A00},
	}}
	b = e.Append(nil)
	if b[1] != 0x12 {
		t.Fatalf("expected block type %x", b[:2])
	}
	got, err = DecodeIdentificationData(readOne(t, b))
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("expected round trip %+v, %v", got, err)
	}

	wrong := blk
	wrong.Header.Type = BlockDiagnosisData
	wantMalformed(t, decodeErr(DecodeIdentificationData, wrong))
	for _, v := range [][2]uint8{{1, 0}, {2, 1}, {1, 2}} {
		err := decodeErr(DecodeIdentificationData, withVersion(blk, v[0], v[1]))
		if !errors.Is(err, profinet.ErrUnsupported) {
			t.Fatalf("version %d.%d: %v", v[0], v[1], err)
		}
	}
	wantMalformed(t, decodeErr(DecodeIdentificationData, truncated(blk, 1)))
	wantMalformed(t, decodeErr(DecodeIdentificationData, trailing(blk)))
}
