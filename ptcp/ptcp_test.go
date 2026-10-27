// SPDX-License-Identifier: MIT

package ptcp

import (
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// golden is a DelayReq as Wireshark's PTCP dissector reads it (checked
// with tshark: SequenceID 7, PortMACAddress, T2PortRxDelay 100 ns,
// T3PortTxDelay 200 ns): a TLV's length counts its alignment padding.
const golden = "ff40" + "000000000000000000000000" + "0007" + "0000" + "00000000" +
	"0c06" + "020000000001" +
	"0a0a" + "0000" + "00000064" + "000000c8" +
	"0000"

func TestGolden(t *testing.T) {
	d := Delay{FrameID: FrameIDDelayReq, SequenceID: 7, PortMAC: ethernet.MAC{2, 0, 0, 0, 0, 1}, HasPortMAC: true,
		RxDelay: 100, TxDelay: 200, HasPortParameter: true}
	if got := hex.EncodeToString(d.AppendTo(nil)); got != golden {
		t.Fatalf("encode\n got %s\nwant %s", got, golden)
	}
	b, _ := hex.DecodeString(golden)
	got, err := Decode(append(b, make([]byte, 8)...))
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("%+v %v", got, err)
	}
	res := Delay{FrameID: FrameIDDelayRes, SequenceID: 7, Delay: 1234, T2TimeStamp: 99, HasPortTime: true}
	if got, err := Decode(res.AppendTo(nil)); err != nil || !reflect.DeepEqual(got, res) {
		t.Fatalf("%+v %v", got, err)
	}
	if !IsDelay(FrameIDDelayFu) || IsDelay(0xFF00) || IsDelay(0xFF60) {
		t.Fatal("IsDelay")
	}
}

func TestMalformed(t *testing.T) {
	hdr := "ff41" + strings.Repeat("00", 20)
	for name, h := range map[string]string{
		"short":            "ff40",
		"not delay":        "ff00" + strings.Repeat("00", 20) + "0000",
		"no End":           hdr,
		"TLV too long":     hdr + "0c10" + "0000",
		"short port MAC":   hdr + "0c02" + "0000" + "0000",
		"short port param": hdr + "0a02" + "0000" + "0000",
		"short port time":  hdr + "0e02" + "0000" + "0000",
	} {
		b, _ := hex.DecodeString(h)
		if _, err := Decode(b); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Unknown TLVs are skipped.
	b, _ := hex.DecodeString(hdr + "0202" + "abcd" + "0000")
	if _, err := Decode(b); err != nil {
		t.Fatal(err)
	}
}

func FuzzDecode(f *testing.F) {
	b, _ := hex.DecodeString(golden)
	f.Add(b)
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := Decode(b)
		if err != nil {
			return
		}
		if _, err := Decode(d.AppendTo(nil)); err != nil {
			t.Fatalf("re-encoded: %v", err)
		}
	})
}
