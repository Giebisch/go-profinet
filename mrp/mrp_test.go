// SPDX-License-Identifier: MIT

package mrp

import (
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var sa = ethernet.MAC{2, 0, 0, 0, 0, 1}

const ff16 = "ffffffffffffffffffffffffffffffff"

// Golden PDUs, built field by field from the layouts Wireshark's MRP
// dissector, the Linux UAPI header and microchip-ung/mrp agree on.
func TestGolden(t *testing.T) {
	for name, c := range map[string]struct {
		hex   string
		frame Frame
	}{
		"MRP_Test": {
			"0001" + "0212 8000 020000000001 0000 0001 0000 00000064" + "0112 0001" + ff16 + "0000",
			Frame{Version: 1, Test: &Test{Prio: DefaultManagerPrio, SA: sa, PortRole: PortPrimary, RingState: RingClosed, TimeStamp: 100},
				Common: &Common{SequenceID: 1, Domain: DefaultDomain}},
		},
		"MRP_TopologyChange": {
			"0001" + "030a 8000 020000000001 000a" + "0112 0002" + ff16 + "0000",
			Frame{Version: 1, TopologyChange: &TopologyChange{Prio: DefaultManagerPrio, SA: sa, Interval: 10}, Common: &Common{SequenceID: 2, Domain: DefaultDomain}},
		},
		"MRP_LinkDown, padded": {
			"0001" + "040c 020000000001 0001 0014 0001 0000" + "0112 0003" + ff16 + "0000",
			Frame{Version: 1, Link: &LinkChange{SA: sa, PortRole: PortSecondary, Interval: 20, Blocked: 1}, Common: &Common{SequenceID: 3, Domain: DefaultDomain}},
		},
		"MRP_LinkUp with an option": {
			"0001" + "050c 020000000001 0000 0014 0000 0000" + "7f05 080006 0102 00" + "0112 0004" + ff16 + "0000",
			Frame{Version: 1, Link: &LinkChange{Up: true, SA: sa, Interval: 20}, Options: []Option{{OUI: [3]byte{8, 0, 6}, Data: []byte{1, 2}}},
				Common: &Common{SequenceID: 4, Domain: DefaultDomain}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			want := unhex(t, c.hex)
			got, err := c.frame.AppendTo(nil)
			if err != nil || hex.EncodeToString(got) != hex.EncodeToString(want) {
				t.Fatalf("encode\n got %x %v\nwant %x", got, err, want)
			}
			f, err := Decode(append(want, make([]byte, 12)...)) // Ethernet padding after MRP_End
			if err != nil || !reflect.DeepEqual(f, c.frame) {
				t.Fatalf("decode %+v %v", f, err)
			}
		})
	}
}

// TestUnpadded: microchip-ung/mrp sends MRP_LinkDown/Up without the
// padding Wireshark expects; both decode.
func TestUnpadded(t *testing.T) {
	f, err := Decode(unhex(t, "0001"+"040c 020000000001 0001 0014 0001"+"0112 0003"+ff16+"0000"))
	if err != nil || f.Link == nil || f.Common == nil || f.Common.SequenceID != 3 || f.Link.Blocked != 1 {
		t.Fatalf("%+v %v", f, err)
	}
	// A link change followed by MRP_End and zeros.
	f, err = Decode(unhex(t, "0001"+"040c 020000000001 0001 0014 0001"+"0000 0000"))
	if err != nil || f.Link == nil || f.Common != nil {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestOtherTLVs(t *testing.T) {
	in := Frame{Other: []TLV{{Type: TypeInTest, Value: make([]byte, 18)}, {Type: TypeInLinkStatusPoll, Value: make([]byte, 10)}}}
	b, err := in.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(b)
	if err != nil || len(f.Other) != 2 || f.Other[0].Type != TypeInTest || f.Version != Version {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := (&Frame{Other: []TLV{{Type: TypeEnd}}}).AppendTo(nil); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("End as a TLV: %v", err)
	}
	if _, err := (&Frame{Options: []Option{{Data: make([]byte, 253)}}}).AppendTo(nil); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("long option: %v", err)
	}
	for ty, s := range map[Type]string{TypeEnd: "MRP_End", TypeTopologyChange: "MRP_TopologyChange", TypeInTopologyChange: "MRP_InTopologyChange",
		TypeInLinkDown: "MRP_InLinkDown", TypeInLinkUp: "MRP_InLinkUp", TypeLinkDown: "MRP_LinkDown", TypeLinkUp: "MRP_LinkUp", TypeTest: "MRP_Test",
		TypeCommon: "MRP_Common", TypeOption: "MRP_Option", TypeInTest: "MRP_InTest", TypeInLinkStatusPoll: "MRP_InLinkStatusPoll", 0x33: "MRP_TLV(0x33)"} {
		if ty.String() != s {
			t.Errorf("%d: %s", ty, ty)
		}
	}
}

func TestMalformed(t *testing.T) {
	for name, h := range map[string]string{
		"empty":           "",
		"no End":          "0001",
		"TLV too long":    "0001 0212 8000",
		"short Test":      "0001 0202 8000 0000",
		"short TC":        "0001 0302 8000 0000",
		"short link":      "0001 0402 0000 0000",
		"short Common":    "0001 0102 0000 0000",
		"short option":    "0001 7f02 0000 0000",
		"truncated after": "0001 0112 0001" + ff16,
	} {
		if _, err := Decode(unhex(t, h)); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func FuzzDecode(f *testing.F) {
	for _, fr := range []Frame{
		{Test: &Test{SA: sa}, Common: &Common{}},
		{Link: &LinkChange{SA: sa}, Options: []Option{{Data: []byte{1}}}, Common: &Common{}},
		{TopologyChange: &TopologyChange{}, Other: []TLV{{Type: TypeInTest, Value: make([]byte, 18)}}},
	} {
		b, _ := fr.AppendTo(nil)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := Decode(b)
		if err != nil {
			return
		}
		// What decodes re-encodes and decodes to the same frame.
		out, err := fr.AppendTo(nil)
		if err != nil {
			return
		}
		again, err := Decode(out)
		if err != nil || !reflect.DeepEqual(normalize(again), normalize(fr)) {
			t.Fatalf("re-encoded frame %x: %+v %v, was %+v", out, again, err, fr)
		}
	})
}

// normalize drops what the encoder fixes (the version of zero).
func normalize(f Frame) Frame {
	if f.Version == 0 {
		f.Version = Version
	}
	return f
}
