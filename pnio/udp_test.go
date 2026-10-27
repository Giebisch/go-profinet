// SPDX-License-Identifier: MIT

package pnio

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

// Golden blocks of an RT_CLASS_UDP Connect, from the layouts of
// Wireshark's PNIO dissector: IOCRProperties RTClass 4, LT 0x0800 (the
// value p-net requires for RT_CLASS_UDP), AlarmCRProperties bit 1.
const (
	goldenUDPIOCR    = "0102 002a 0100 0001 0001 0800 00000004 000c c000 0020 0004 0001 0000 ffffffff 000a 000a c000 000000000000 0000"
	goldenUDPAlarmCR = "0103 0016 0100 0001 0800 00000002 0005 0003 0001 00c8 c000 a000"
)

func TestUDPBlocks(t *testing.T) {
	c := IOCRBlockReq{Type: IOCRInput, Reference: 1, LT: LTIP, Properties: uint32(RTClassUDP), DataLength: 12, FrameID: 0xC000,
		SendClockFactor: 32, ReductionRatio: 4, Phase: 1, FrameSendOffset: 0xFFFFFFFF, WatchdogFactor: 10, DataHoldFactor: 10, TagHeader: 0xC000}
	if got := hex.EncodeToString(c.Append(nil)); got != strings.ReplaceAll(goldenUDPIOCR, " ", "") {
		t.Fatalf("IOCR %s", got)
	}
	gc, err := DecodeIOCRBlockReq(oneBlock(t, unhex(t, goldenUDPIOCR)))
	if err != nil || !reflect.DeepEqual(gc, c) || gc.RTClass() != RTClassUDP {
		t.Fatalf("%+v %v", gc, err)
	}
	a := AlarmCRBlockReq{Type: 1, LT: LTIP, Properties: AlarmCRPropTransportUDP, RTATimeoutFactor: 5, RTARetries: 3,
		LocalAlarmReference: 1, MaxAlarmDataLength: 200, TagHeaderHigh: 0xC000, TagHeaderLow: 0xA000}
	if got := hex.EncodeToString(a.Append(nil)); got != strings.ReplaceAll(goldenUDPAlarmCR, " ", "") {
		t.Fatalf("AlarmCR %s", got)
	}
	ga, err := DecodeAlarmCRBlockReq(oneBlock(t, unhex(t, goldenUDPAlarmCR)))
	if err != nil || ga != a || !ga.TransportUDP() {
		t.Fatalf("%+v %v", ga, err)
	}

	req := ConnectRequest{AR: ARBlockReq{UDPRTPort: UDPRTPort}, IOCRs: []IOCRBlockReq{c}}
	if !req.UsesUDP() {
		t.Fatal("RT_CLASS_UDP IOCR not detected")
	}
	req.IOCRs[0].Properties = uint32(RTClass2)
	if req.UsesUDP() {
		t.Fatal("RT_CLASS_2 reported as UDP")
	}
	req.AlarmCR = &a
	if !req.UsesUDP() {
		t.Fatal("RTA_CLASS_UDP AlarmCR not detected")
	}
	req.AlarmCR.Properties = 0
	if req.UsesUDP() {
		t.Fatal("RTA_CLASS_1 reported as UDP")
	}
}

func TestRTClassString(t *testing.T) {
	for c, want := range map[RTClass]string{RTClass1: "RT_CLASS_1(legacy)", RTClass2: "RT_CLASS_1", RTClass3: "RT_CLASS_3",
		RTClassUDP: "RT_CLASS_UDP", RTClassStream: "RT_CLASS_STREAM", 9: "RTClass(9)"} {
		if got := c.String(); got != want {
			t.Errorf("%d: %q, want %q", uint8(c), got, want)
		}
	}
}
