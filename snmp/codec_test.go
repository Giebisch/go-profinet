// SPDX-License-Identifier: MIT

package snmp

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// goldenGet is an SNMPv1 GetRequest for sysDescr.0 with community
// "public" and request-id 1, assembled by hand from RFC 1157 section 4 and
// X.690:
//
//	30 26                       Message SEQUENCE, 38 octets
//	   02 01 00                 version INTEGER 0 (version-1)
//	   04 06 "public"           community OCTET STRING
//	   a0 19                    GetRequest-PDU [0], 25 octets
//	      02 01 01              request-id 1
//	      02 01 00              error-status 0
//	      02 01 00              error-index 0
//	      30 0e                 VarBindList
//	         30 0c              VarBind
//	            06 08 2b 06 01 02 01 01 01 00   1.3.6.1.2.1.1.1.0 (1*40+3 = 0x2b)
//	            05 00           NULL
const goldenGet = "30 26 02 01 00 04 06 70 75 62 6c 69 63 a0 19 02 01 01 02 01 00 02 01 00 30 0e 30 0c 06 08 2b 06 01 02 01 01 01 00 05 00"

// goldenBulk is an SNMPv2c GetBulkRequest (RFC 3416 section 3: [5],
// non-repeaters and max-repetitions in place of error-status and
// error-index) for lldpLocalSystemData, request-id 0x1234, non-repeaters
// 0, max-repetitions 10:
//
//	30 27                       Message
//	   02 01 01                 version 1 (v2c, RFC 1901)
//	   04 06 "public"
//	   a5 1a                    GetBulkRequest-PDU [5]
//	      02 02 12 34           request-id 0x1234
//	      02 01 00              non-repeaters 0
//	      02 01 0a              max-repetitions 10
//	      30 0e 30 0c
//	         06 08 28 c4 62 01 01 02 01 03   1.0.8802.1.1.2.1.3 (1*40+0 = 0x28;
//	                                         8802 = 68*128+98: c4 62)
//	         05 00
const goldenBulk = "30 27 02 01 01 04 06 70 75 62 6c 69 63 a5 1a 02 02 12 34 02 01 00 02 01 0a 30 0e 30 0c 06 08 28 c4 62 01 01 02 01 03 05 00"

func TestGoldenGetRequest(t *testing.T) {
	b := unhex(t, goldenGet)
	m, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != Version1 || string(m.Community) != "public" || m.PDU.Type != PDUGetRequest || m.PDU.RequestID != 1 ||
		len(m.PDU.VarBinds) != 1 || m.PDU.VarBinds[0].Name.String() != "1.3.6.1.2.1.1.1.0" || m.PDU.VarBinds[0].Value.Type != TypeNull {
		t.Fatalf("decoded %+v", m)
	}
	out, err := (&Message{Version: Version1, Community: []byte("public"), PDU: PDU{Type: PDUGetRequest, RequestID: 1,
		VarBinds: []VarBind{{Name: MustParseOID("1.3.6.1.2.1.1.1.0"), Value: Null()}}}}).AppendTo(nil)
	if err != nil || !bytes.Equal(out, b) {
		t.Fatalf("encoded % x %v\nwant    % x", out, err, b)
	}
}

func TestGoldenGetBulkRequest(t *testing.T) {
	b := unhex(t, goldenBulk)
	m, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != Version2c || m.PDU.Type != PDUGetBulkRequest || m.PDU.RequestID != 0x1234 || m.PDU.NonRepeaters != 0 ||
		m.PDU.MaxRepetitions != 10 || !m.PDU.VarBinds[0].Name.Equal(OIDLLDPLocalSystemData) {
		t.Fatalf("decoded %+v", m)
	}
	out, err := m.AppendTo(nil)
	if err != nil || !bytes.Equal(out, b) {
		t.Fatalf("encoded % x %v", out, err)
	}
}

// TestGoldenValues: the TLV of each value type, from X.690 8.3 (INTEGER,
// two's complement, minimal), 8.7 (OCTET STRING), 8.8 (NULL), 8.19 (OBJECT
// IDENTIFIER) and the application tags of RFC 2578 section 7.1.
func TestGoldenValues(t *testing.T) {
	for _, tc := range []struct {
		v   Value
		hex string
	}{
		{Integer(0), "02 01 00"},
		{Integer(127), "02 01 7f"},
		{Integer(128), "02 02 00 80"}, // a leading zero keeps it positive
		{Integer(256), "02 02 01 00"},
		{Integer(-1), "02 01 ff"},
		{Integer(-128), "02 01 80"},
		{Integer(-129), "02 02 ff 7f"},
		{Integer(-1 << 31), "02 04 80 00 00 00"},
		{Integer(1<<31 - 1), "02 04 7f ff ff ff"},
		{String("ab"), "04 02 61 62"},
		{String(""), "04 00"},
		{Null(), "05 00"},
		{ObjectIdentifier(OID{1, 3, 6, 1}), "06 03 2b 06 01"},
		{ObjectIdentifier(OIDZeroDotZero), "06 01 00"},
		{ObjectIdentifier(OID{2, 999, 3}), "06 03 88 37 03"}, // X.690 8.19.5 example: 2*40+999 = 1079
		{ObjectIdentifier(OID{1, 3, 0xFFFFFFFF}), "06 06 2b 8f ff ff ff 7f"},
		{IPAddress(netip.MustParseAddr("192.168.0.1")), "40 04 c0 a8 00 01"},
		{Counter32(0), "41 01 00"},
		{Counter32(0xFFFFFFFF), "41 05 00 ff ff ff ff"},
		{Gauge32(100000000), "42 04 05 f5 e1 00"},
		{TimeTicks(128), "43 02 00 80"},
		{Counter64(1<<64 - 1), "46 09 00 ff ff ff ff ff ff ff ff"},
		{Counter64(1 << 32), "46 05 01 00 00 00 00"},
		{Value{Type: TypeNoSuchObject}, "80 00"},
		{Value{Type: TypeNoSuchInstance}, "81 00"},
		{Value{Type: TypeEndOfMibView}, "82 00"},
	} {
		want := unhex(t, tc.hex)
		if err := tc.v.valid(); err != nil {
			t.Fatalf("%v: %v", tc.v, err)
		}
		got := appendValue(nil, tc.v)
		if !bytes.Equal(got, want) || valueLen(tc.v) != len(want) {
			t.Errorf("%v: % x (len %d), want % x", tc.v, got, valueLen(tc.v), want)
			continue
		}
		tag, c, rest, err := readTLV(got)
		if err != nil || len(rest) != 0 {
			t.Fatalf("%v: %v", tc.v, err)
		}
		d, err := decodeValue(tag, c)
		if err != nil || d.String() != tc.v.String() {
			t.Errorf("%v: decoded %v %v", tc.v, d, err)
		}
	}
}

func TestLengths(t *testing.T) {
	for _, n := range []int{0, 127, 128, 255, 256, 65535, 65536, 1 << 24} {
		b := appendLength(nil, n)
		if len(b) != lengthLen(n) {
			t.Errorf("%d: % x, lengthLen %d", n, b, lengthLen(n))
		}
	}
	// A non-minimal long form is allowed (RFC 3417 section 8).
	tag, c, rest, err := readTLV(unhex(t, "04 82 00 02 61 62 ff"))
	if err != nil || tag != 0x04 || string(c) != "ab" || len(rest) != 1 {
		t.Fatalf("non-minimal length: %x %q %v %v", tag, c, rest, err)
	}
}

func TestDecodeRejects(t *testing.T) {
	for _, tc := range []struct{ name, hex string }{
		{"empty", ""},
		{"one octet", "30"},
		{"indefinite length", "30 80 00 00"},
		{"five length octets", "30 85 00 00 00 00 01 00"},
		{"truncated length", "30 82 00"},
		{"length beyond input", "30 05 02 01 00"},
		{"high tag number", "3f 01 00"},
		{"not a sequence", "04 00"},
		{"trailing octets", "30 00 00"},
		{"no version", "30 00"},
		{"version not INTEGER", "30 03 04 01 00"},
		{"version too long", "30 07 02 05 00 80 00 00 00"},
		{"non-minimal version", "30 04 02 02 00 01"},
		{"empty INTEGER", "30 02 02 00"},
		{"no community", "30 03 02 01 00"},
		{"no PDU", "30 05 02 01 00 04 00"},
		{"octets after PDU", "30 0d 02 01 00 04 00 a0 05 02 01 00 02 01 00"},
		{"unknown PDU", "30 07 02 01 00 04 00 af 00"},
		{"GetBulk in v1", "30 07 02 01 00 04 00 a5 00"},
		{"Inform in v1", "30 07 02 01 00 04 00 a6 00"},
		{"Trap in v2c", "30 07 02 01 01 04 00 a4 00"},
		{"no request-id", "30 07 02 01 00 04 00 a0 00"},
		{"no error-status", "30 0a 02 01 00 04 00 a0 03 02 01 01"},
		{"no error-index", "30 0d 02 01 00 04 00 a0 06 02 01 01 02 01 00"},
		{"no varbind list", "30 10 02 01 00 04 00 a0 09 02 01 01 02 01 00 02 01 00"},
		{"octets after list", "30 13 02 01 00 04 00 a0 0c 02 01 01 02 01 00 02 01 00 30 00 00"},
		{"truncated varbind", "30 13 02 01 00 04 00 a0 0c 02 01 01 02 01 00 02 01 00 30 01 30"},
		{"varbind not SEQUENCE", "30 14 02 01 00 04 00 a0 0d 02 01 01 02 01 00 02 01 00 30 02 04 00"},
		{"name not OID", "30 16 02 01 00 04 00 a0 0f 02 01 01 02 01 00 02 01 00 30 04 30 02 04 00"},
		{"empty OID", "30 18 02 01 00 04 00 a0 11 02 01 01 02 01 00 02 01 00 30 06 30 04 06 00 05 00"},
		{"no value", "30 17 02 01 00 04 00 a0 10 02 01 01 02 01 00 02 01 00 30 05 30 03 06 01 2b"},
		{"octets after value", "30 1b 02 01 00 04 00 a0 14 02 01 01 02 01 00 02 01 00 30 09 30 07 06 01 2b 05 00 05 00"},
		{"NULL with content", "30 1a 02 01 00 04 00 a0 13 02 01 01 02 01 00 02 01 00 30 08 30 06 06 01 2b 05 01 00"},
		{"unknown value type", "30 19 02 01 00 04 00 a0 12 02 01 01 02 01 00 02 01 00 30 07 30 05 06 01 2b 44 00"},
	} {
		if _, err := Decode(unhex(t, tc.hex)); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func TestDecodeVersions(t *testing.T) {
	// An SNMPv3 message (RFC 3412 section 6): version 3, then
	// msgGlobalData, which is not a community.
	_, err := Decode(unhex(t, "30 08 02 01 03 30 03 02 01 01"))
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Version != 3 || !errors.Is(err, profinet.ErrUnsupported) || ve.Error() != "snmp: unsupported version 3" {
		t.Fatalf("v3: %v", err)
	}
	// A v1 Trap is returned with its type only.
	m, err := Decode(unhex(t, "30 09 02 01 00 04 00 a4 02 05 00"))
	if err != nil || m.PDU.Type != PDUTrapV1 {
		t.Fatalf("trap: %+v %v", m, err)
	}
}

func TestDecodeLimits(t *testing.T) {
	if _, err := Decode(make([]byte, MaxMessageSize+1)); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("size: %v", err)
	}
	vbs := make([]VarBind, MaxVarBinds+1)
	for i := range vbs {
		vbs[i] = VarBind{Name: OID{1, 3}, Value: Null()}
	}
	m := Message{Version: Version2c, Community: []byte("c"), PDU: PDU{Type: PDUGetRequest, VarBinds: vbs}}
	b, err := m.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(b); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("varbinds: %v", err)
	}
	m.PDU.VarBinds = vbs[:MaxVarBinds]
	b, _ = m.AppendTo(nil)
	if d, err := Decode(b); err != nil || len(d.PDU.VarBinds) != MaxVarBinds {
		t.Fatalf("max varbinds: %v", err)
	}
}

func TestDecodeValueRejects(t *testing.T) {
	for _, tc := range []struct {
		tag byte
		hex string
	}{
		{0x02, "00 7f"},          // non-minimal positive
		{0x02, "ff 80"},          // non-minimal negative
		{0x02, "01 00 00 00 00"}, // beyond Integer32
		{0x02, ""},
		{0x41, "80"},                // negative Counter32
		{0x41, "01 00 00 00 00"},    // 2^32
		{0x42, "00 00 ff ff ff ff"}, // six octets
		{0x46, "01 00 00 00 00 00 00 00 00"},
		{0x43, ""},
		{0x40, "01 02 03"},
		{0x06, ""},
		{0x06, "2b 80 01"},          // a sub-identifier with a leading 0x80
		{0x06, "2b 86"},             // truncated
		{0x06, "2b 90 80 80 80 00"}, // beyond 32 bits
		{0x80, "00"},
		{0x30, ""},
	} {
		if _, err := decodeValue(tc.tag, unhex(t, tc.hex)); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("0x%02x % s: %v", tc.tag, tc.hex, err)
		}
	}
	long := make([]byte, 5*MaxOIDLen+1)
	if _, err := decodeOID(long); err == nil {
		t.Error("long OID")
	}
	many := bytes.Repeat([]byte{1}, MaxOIDLen)
	if _, err := decodeOID(many); err == nil {
		t.Error("129 sub-identifiers")
	}
	o, err := decodeOID(bytes.Repeat([]byte{1}, MaxOIDLen-1))
	if err != nil || len(o) != MaxOIDLen {
		t.Errorf("128 sub-identifiers: %d %v", len(o), err)
	}
	for _, tc := range []struct {
		hex  string
		want string
	}{{"27", "0.39"}, {"28", "1.0"}, {"4f", "1.39"}, {"50", "2.0"}, {"8f ff ff ff 7f", "2.4294967215"}} {
		o, err := decodeOID(unhex(t, tc.hex))
		if err != nil || o.String() != tc.want {
			t.Errorf("%s: %v %v", tc.hex, o, err)
		}
	}
}

func TestEncodeRejects(t *testing.T) {
	good := VarBind{Name: OID{1, 3}, Value: Null()}
	for _, m := range []Message{
		{Version: 3, PDU: PDU{Type: PDUGetRequest}},
		{Version: Version1, PDU: PDU{Type: PDUTrapV1}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{good, {Name: OID{1}, Value: Null()}}}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{{Name: OID{3, 1}, Value: Null()}}}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{{Name: OID{1, 40}, Value: Null()}}}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{{Name: OID{2, 0xFFFFFFFF}, Value: Null()}}}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{{Name: OID{1, 3}, Value: Value{}}}}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{{Name: OID{1, 3}, Value: Value{Type: TypeInteger, Int: 1 << 31}}}}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{{Name: OID{1, 3}, Value: Value{Type: TypeCounter32, Uint: 1 << 32}}}}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{{Name: OID{1, 3}, Value: Value{Type: TypeIPAddress, Bytes: []byte{1}}}}}},
		{Version: Version1, PDU: PDU{Type: PDUGetRequest, VarBinds: []VarBind{{Name: OID{1, 3}, Value: ObjectIdentifier(OID{9})}}}},
	} {
		if _, err := m.AppendTo(nil); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%+v: %v", m, err)
		}
	}
}

func TestRoundTripAllPDUs(t *testing.T) {
	vbs := []VarBind{
		{Name: MustParseOID("1.3.6.1.2.1.1.3.0"), Value: TimeTicks(12345)},
		{Name: MustParseOID(".1.3.6.1.2.1.31.1.1.1.6.1"), Value: Counter64(1 << 40)},
		{Name: OID{1, 3, 6, 1, 4, 1, 99999, 1}, Value: OctetString(bytes.Repeat([]byte{0xAB}, 300))},
		{Name: OID{1, 3, 6, 1, 2, 1, 4, 20, 1, 1, 10, 0, 0, 1}, Value: IPAddress(netip.MustParseAddr("10.0.0.1"))},
		{Name: OID{1, 3, 6, 1, 2, 1, 1, 2, 0}, Value: ObjectIdentifier(OID{1, 3, 6, 1, 4, 1, 24686})},
		{Name: OID{1, 3, 6, 1, 2, 1, 2, 2, 1, 5, 1}, Value: Gauge32(10)},
		{Name: OID{1, 3, 6, 1, 2, 1, 11, 1, 0}, Value: Counter32(7)},
		{Name: OID{1, 3, 6, 1, 2, 1, 1, 7, 0}, Value: Integer(-5)},
		{Name: OID{1, 3, 6, 1, 2, 1, 1, 8, 0}, Value: Value{Type: TypeNoSuchObject}},
	}
	for _, typ := range []PDUType{PDUGetRequest, PDUGetNextRequest, PDUResponse, PDUSetRequest, PDUGetBulkRequest, PDUInformRequest, PDUTrapV2, PDUReport} {
		m := Message{Version: Version2c, Community: []byte("secret"), PDU: PDU{Type: typ, RequestID: -7, ErrorStatus: NotWritable, ErrorIndex: 3,
			NonRepeaters: 1, MaxRepetitions: 20, VarBinds: vbs}}
		b, err := m.AppendTo(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != m.messageLen(sumLen(vbs)) {
			t.Fatalf("%s: messageLen %d, encoded %d", typ, m.messageLen(sumLen(vbs)), len(b))
		}
		d, err := Decode(b)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		b2, _ := d.AppendTo(nil)
		if !bytes.Equal(b, b2) || d.PDU.Type != typ || d.PDU.RequestID != -7 {
			t.Fatalf("%s: round trip differs", typ)
		}
		if typ == PDUGetBulkRequest {
			if d.PDU.NonRepeaters != 1 || d.PDU.MaxRepetitions != 20 || d.PDU.ErrorStatus != 0 {
				t.Fatalf("bulk fields %+v", d.PDU)
			}
		} else if d.PDU.ErrorStatus != NotWritable || d.PDU.ErrorIndex != 3 || d.PDU.NonRepeaters != 0 {
			t.Fatalf("%s fields %+v", typ, d.PDU)
		}
	}
}

func sumLen(vbs []VarBind) int {
	n := 0
	for i := range vbs {
		n += varBindLen(&vbs[i])
	}
	return n
}

func TestOID(t *testing.T) {
	for _, s := range []string{"", ".", "1.x", "1..2", "1.4294967296"} {
		if _, err := ParseOID(s); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%q: %v", s, err)
		}
	}
	if _, err := ParseOID(strings.Repeat("1.", MaxOIDLen) + "1"); err == nil {
		t.Error("129 sub-identifiers")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("MustParseOID did not panic")
			}
		}()
		MustParseOID("bad")
	}()
	a, b := MustParseOID("1.3.6.1"), MustParseOID("1.3.6.1.2")
	if a.Compare(b) != -1 || b.Compare(a) != 1 || a.Compare(a) != 0 || !b.HasPrefix(a) || a.HasPrefix(b) ||
		MustParseOID("1.3.7").Compare(b) != 1 || MustParseOID("1.3.5.9").Compare(a) != -1 || MustParseOID("1.4").HasPrefix(a) {
		t.Fatal("order")
	}
	if c := a.Append(9); c.String() != "1.3.6.1.9" || a.String() != "1.3.6.1" {
		t.Fatalf("append %s %s", c, a)
	}
}

func TestStrings(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{Version1.String(), "v1"}, {Version2c.String(), "v2c"}, {Version(3).String(), "Version(3)"},
		{PDUGetRequest.String(), "GetRequest"}, {PDUGetNextRequest.String(), "GetNextRequest"}, {PDUResponse.String(), "Response"},
		{PDUSetRequest.String(), "SetRequest"}, {PDUTrapV1.String(), "Trap"}, {PDUGetBulkRequest.String(), "GetBulkRequest"},
		{PDUInformRequest.String(), "InformRequest"}, {PDUTrapV2.String(), "SNMPv2-Trap"}, {PDUReport.String(), "Report"},
		{PDUType(0xAF).String(), "PDUType(0xaf)"},
		{NoError.String(), "noError"}, {NotWritable.String(), "notWritable"}, {InconsistentName.String(), "inconsistentName"},
		{ErrorStatus(19).String(), "ErrorStatus(19)"}, {ErrorStatus(-1).String(), "ErrorStatus(-1)"},
		{TypeInteger.String(), "INTEGER"}, {TypeOctetString.String(), "OCTET STRING"}, {TypeNull.String(), "NULL"},
		{TypeObjectIdentifier.String(), "OBJECT IDENTIFIER"}, {TypeIPAddress.String(), "IpAddress"}, {TypeCounter32.String(), "Counter32"},
		{TypeGauge32.String(), "Gauge32"}, {TypeTimeTicks.String(), "TimeTicks"}, {TypeCounter64.String(), "Counter64"},
		{TypeNoSuchObject.String(), "noSuchObject"}, {TypeNoSuchInstance.String(), "noSuchInstance"}, {TypeEndOfMibView.String(), "endOfMibView"},
		{Type(0x44).String(), "Type(0x44)"},
		{Integer(-3).String(), "INTEGER: -3"}, {String("x").String(), `OCTET STRING: "x"`}, {ObjectIdentifier(OID{1, 3}).String(), "OID: 1.3"},
		{IPAddress(netip.MustParseAddr("1.2.3.4")).String(), "IpAddress: 1.2.3.4"}, {Value{Type: TypeIPAddress}.String(), "IpAddress: invalid"},
		{Gauge32(5).String(), "Gauge32: 5"}, {Null().String(), "NULL"},
	} {
		if tc.got != tc.want {
			t.Errorf("%q, want %q", tc.got, tc.want)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(unhex(f, goldenGet))
	f.Add(unhex(f, goldenBulk))
	f.Add(unhex(f, "30 08 02 01 03 30 03 02 01 01"))
	f.Add(unhex(f, "30 09 02 01 00 04 00 a4 02 05 00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := Decode(b)
		if err != nil {
			return
		}
		if m.PDU.Type == PDUTrapV1 {
			return
		}
		out, err := m.AppendTo(nil)
		if err != nil {
			t.Fatalf("decoded message does not encode: %v", err)
		}
		d, err := Decode(out)
		if err != nil {
			t.Fatalf("re-decode: %v", err)
		}
		out2, _ := d.AppendTo(nil)
		if !bytes.Equal(out, out2) {
			t.Fatal("encoding not stable")
		}
	})
}

func FuzzDecodeOID(f *testing.F) {
	f.Add(unhex(f, "2b 06 01 02 01 01 01 00"))
	f.Add(unhex(f, "28 c4 62 01 01 02"))
	f.Add(unhex(f, "8f ff ff ff 7f"))
	f.Fuzz(func(t *testing.T, b []byte) {
		o, err := decodeOID(b)
		if err != nil {
			return
		}
		if err := o.validate(); err != nil {
			t.Fatalf("decoded %s invalid: %v", o, err)
		}
		if out := appendOIDContent(nil, o); !bytes.Equal(out, b) || oidContentLen(o) != len(b) {
			t.Fatalf("% x re-encodes as % x", b, out)
		}
		p, err := ParseOID(o.String())
		if err != nil || !p.Equal(o) {
			t.Fatalf("parse %s: %v", o, err)
		}
	})
}

func FuzzDecodeValue(f *testing.F) {
	f.Add(byte(0x02), unhex(f, "ff 7f"))
	f.Add(byte(0x46), unhex(f, "00 ff ff ff ff ff ff ff ff"))
	f.Add(byte(0x40), unhex(f, "c0 a8 00 01"))
	f.Add(byte(0x06), unhex(f, "2b 06"))
	f.Add(byte(0x80), []byte{})
	f.Fuzz(func(t *testing.T, tag byte, c []byte) {
		v, err := decodeValue(tag, c)
		if err != nil {
			return
		}
		if err := v.valid(); err != nil {
			t.Fatalf("decoded %v invalid: %v", v, err)
		}
		out := appendValue(nil, v)
		if len(out) != valueLen(v) {
			t.Fatalf("valueLen %d, encoded %d", valueLen(v), len(out))
		}
		_, c2, _, err := readTLV(out)
		if err != nil || !bytes.Equal(c2, c) {
			t.Fatalf("% x re-encodes as % x", c, c2)
		}
	})
}
