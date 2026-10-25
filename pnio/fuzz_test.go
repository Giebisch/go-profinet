// SPDX-License-Identifier: MIT

package pnio

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/otfabric/go-profinet/rpc"
)

func FuzzConnectRequest(f *testing.F) {
	c := testConnectRequest()
	f.Add(c.Append(nil))
	min := ConnectRequest{AR: testARBlockReq()}
	f.Add(min.Append(nil))
	f.Add([]byte{})
	f.Add(hx(goldenARBlockReq)[:20])
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := DecodeConnectRequest(b)
		if err != nil {
			return
		}
		again, err := DecodeConnectRequest(c.Append(nil))
		if err != nil {
			t.Fatalf("re-encoded request fails: %v", err)
		}
		if !reflect.DeepEqual(again, c) {
			t.Fatalf("round trip\n got %+v\nwant %+v", again, c)
		}
	})
}

// stable checks that a decoded value survives encoding and decoding.
func stable[T any](t *testing.T, name string, v T, enc func(T) []byte, dec func(RawBlock) (T, error)) {
	t.Helper()
	b := enc(v)
	blk, rest, err := ReadBlock(b)
	if err != nil || len(rest) != 0 {
		t.Fatalf("%s: re-encoded block: %v, %d trailing", name, err, len(rest))
	}
	again, err := dec(blk)
	if err != nil {
		t.Fatalf("%s: re-encoded block fails: %v", name, err)
	}
	if !reflect.DeepEqual(again, v) {
		t.Fatalf("%s: round trip\n got %+v\nwant %+v", name, again, v)
	}
}

func FuzzBlocks(f *testing.F) {
	a := testAlarm()
	ack := AckFor(&a, Status{})
	im0 := testIM0()
	q := testReadReq()
	resp := testConnectResponse()
	req := testConnectRequest()
	for _, b := range [][]byte{
		req.Append(nil),
		resp.Append(nil),
		(&ControlBlock{Type: BlockAppReadyPlugReq, AlarmSequence: 3}).Append(nil),
		q.Append(nil),
		(&ReadRes{Length: 4}).Append(nil),
		(&WriteReq{Index: 1}).Append(nil),
		(&WriteRes{Status: AbortStatus(AbortUser)}).Append(nil),
		im0.Append(nil),
		(&IM1{TagFunction: "f"}).Append(nil),
		(&IM2{}).Append(nil),
		(&IM3{}).Append(nil),
		(&IM4{}).Append(nil),
		(&DiagnosisData{USI: USIQualifiedChannel, Items: testItems()}).Append(nil),
		a.Append(nil),
		ack.Append(nil),
		(&IdentificationData{Expected: true, APIs: []IdentAPI{{Slots: []IdentSlot{{Submodules: []IdentSubmodule{{}}}}}}}).Append(nil),
		AppendWriteMultiple(nil, []WriteMultipleItem{{Data: hx("01")}, {Data: hx("0203")}}),
	} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = SplitBlocks(b)
		_, _ = SplitWriteMultiple(b)
		_, _ = DecodeConnectResponse(b)
		for _, usi := range []uint16{USIChannelDiagnosis, USIExtChannelDiagnosis, USIQualifiedChannel, USIMaintenance} {
			_, _ = DecodeDiagnosisItems(usi, b)
		}
		blk, _, err := ReadBlock(b)
		if err != nil {
			return
		}
		// Decode the block as every type, so that each decoder sees
		// every body, not only the one of its own type.
		for _, typ := range []BlockType{blk.Header.Type, BlockARReq, BlockIOCRReq, BlockAlarmCRReq, BlockExpectedSubmReq,
			BlockARRPCReq, BlockPrmEndPlugReq, BlockReadReq, BlockReadRes, BlockWriteReq, BlockWriteRes, BlockIM0,
			BlockIM1, BlockIM2, BlockIM3, BlockIM4, BlockDiagnosisData, BlockAlarmNotificationHigh, BlockAlarmAckLow,
			BlockRealIdentData} {
			blk.Header.Type = typ
			if v, err := DecodeARBlockReq(blk); err == nil {
				stable(t, "ARBlockReq", v, func(v ARBlockReq) []byte { return v.Append(nil) }, DecodeARBlockReq)
			}
			if v, err := DecodeARBlockRes(blk); err == nil {
				stable(t, "ARBlockRes", v, func(v ARBlockRes) []byte { return v.Append(nil) }, DecodeARBlockRes)
			}
			if v, err := DecodeIOCRBlockReq(blk); err == nil {
				stable(t, "IOCRBlockReq", v, func(v IOCRBlockReq) []byte { return v.Append(nil) }, DecodeIOCRBlockReq)
			}
			if v, err := DecodeIOCRBlockRes(blk); err == nil {
				stable(t, "IOCRBlockRes", v, func(v IOCRBlockRes) []byte { return v.Append(nil) }, DecodeIOCRBlockRes)
			}
			if v, err := DecodeAlarmCRBlockReq(blk); err == nil {
				stable(t, "AlarmCRBlockReq", v, func(v AlarmCRBlockReq) []byte { return v.Append(nil) }, DecodeAlarmCRBlockReq)
			}
			if v, err := DecodeAlarmCRBlockRes(blk); err == nil {
				stable(t, "AlarmCRBlockRes", v, func(v AlarmCRBlockRes) []byte { return v.Append(nil) }, DecodeAlarmCRBlockRes)
			}
			if v, err := DecodeExpectedSubmoduleBlockReq(blk); err == nil {
				stable(t, "ExpectedSubmoduleBlockReq", v, func(v ExpectedSubmoduleBlockReq) []byte { return v.Append(nil) }, DecodeExpectedSubmoduleBlockReq)
			}
			if v, err := DecodeModuleDiffBlock(blk); err == nil {
				stable(t, "ModuleDiffBlock", v, func(v ModuleDiffBlock) []byte { return v.Append(nil) }, DecodeModuleDiffBlock)
			}
			if v, err := DecodeARServerBlock(blk); err == nil {
				stable(t, "ARServerBlock", v, func(v ARServerBlock) []byte { return v.Append(nil) }, DecodeARServerBlock)
			}
			if v, err := DecodeARRPCBlockReq(blk); err == nil {
				stable(t, "ARRPCBlockReq", v, func(v ARRPCBlockReq) []byte { return v.Append(nil) }, DecodeARRPCBlockReq)
			}
			if v, err := DecodeControlBlock(blk); err == nil {
				stable(t, "ControlBlock", v, func(v ControlBlock) []byte { return v.Append(nil) }, DecodeControlBlock)
			}
			if v, err := DecodeReadReq(blk); err == nil {
				stable(t, "ReadReq", v, func(v ReadReq) []byte { return v.Append(nil) }, DecodeReadReq)
			}
			if v, err := DecodeReadRes(blk); err == nil {
				stable(t, "ReadRes", v, func(v ReadRes) []byte { return v.Append(nil) }, DecodeReadRes)
			}
			if v, err := DecodeWriteReq(blk); err == nil {
				stable(t, "WriteReq", v, func(v WriteReq) []byte { return v.Append(nil) }, DecodeWriteReq)
			}
			if v, err := DecodeWriteRes(blk); err == nil {
				stable(t, "WriteRes", v, func(v WriteRes) []byte { return v.Append(nil) }, DecodeWriteRes)
			}
			if v, err := DecodeIM0(blk); err == nil && v.SoftwareRevision.Prefix != 0 {
				stable(t, "I&M0", v, func(v IM0) []byte { return v.Append(nil) }, DecodeIM0)
			}
			if v, err := DecodeIM1(blk); err == nil {
				stable(t, "I&M1", v, func(v IM1) []byte { return v.Append(nil) }, DecodeIM1)
			}
			if v, err := DecodeIM2(blk); err == nil {
				stable(t, "I&M2", v, func(v IM2) []byte { return v.Append(nil) }, DecodeIM2)
			}
			if v, err := DecodeIM3(blk); err == nil {
				stable(t, "I&M3", v, func(v IM3) []byte { return v.Append(nil) }, DecodeIM3)
			}
			if v, err := DecodeIM4(blk); err == nil {
				stable(t, "I&M4", v, func(v IM4) []byte { return v.Append(nil) }, DecodeIM4)
			}
			if v, err := DecodeDiagnosisData(blk); err == nil {
				stable(t, "DiagnosisData", v, func(v DiagnosisData) []byte { return v.Append(nil) }, DecodeDiagnosisData)
			}
			if v, err := DecodeAlarmNotification(blk); err == nil {
				stable(t, "AlarmNotification", v, func(v AlarmNotification) []byte { return v.Append(nil) }, DecodeAlarmNotification)
			}
			if v, err := DecodeAlarmAck(blk); err == nil {
				stable(t, "AlarmAck", v, func(v AlarmAck) []byte { return v.Append(nil) }, DecodeAlarmAck)
			}
			if v, err := DecodeIdentificationData(blk); err == nil {
				stable(t, "IdentificationData", v, func(v IdentificationData) []byte { return v.Append(nil) }, DecodeIdentificationData)
			}
		}
	})
}

func FuzzNDR(f *testing.F) {
	req := testConnectRequest()
	blocks := req.Append(nil)
	f.Add(AppendNDRRequest(nil, binary.BigEndian, DefaultArgsMaximum, blocks))
	f.Add(AppendNDRRequest(nil, binary.LittleEndian, DefaultArgsMaximum, blocks))
	f.Add(AppendNDRResponse(nil, binary.BigEndian, Status{}, 64, hx("8114 0002 0100")))
	f.Add(AbortStatus(AbortUser).Append(nil))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, o := range []rpc.ByteOrder{binary.BigEndian, binary.LittleEndian} {
			if m, blk, err := DecodeNDRRequest(b, o); err == nil {
				if len(blk) > len(b)-NDRHeaderLen {
					t.Fatalf("request blocks of %d octets from %d", len(blk), len(b))
				}
				// AppendNDRRequest announces ArgsMaximum as the
				// MaximumCount, which must cover the blocks; the decoder
				// takes MaximumCount from the wire.
				if int64(m) >= int64(len(blk)) {
					again := AppendNDRRequest(nil, o, m, blk)
					m2, blk2, err := DecodeNDRRequest(again, o)
					if err != nil || m2 != m || !bytes.Equal(blk2, blk) {
						t.Fatalf("request round trip: %v", err)
					}
				}
			}
			if s, blk, err := DecodeNDRResponse(b, o); err == nil {
				again := AppendNDRResponse(nil, o, s, DefaultArgsMaximum, blk)
				s2, blk2, err := DecodeNDRResponse(again, o)
				if err != nil || s2 != s || !bytes.Equal(blk2, blk) {
					t.Fatalf("response round trip: %v", err)
				}
			}
		}
	})
}
