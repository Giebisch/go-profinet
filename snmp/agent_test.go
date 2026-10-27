// SPDX-License-Identifier: MIT

package snmp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
)

var (
	oidSysDescr  = MustParseOID("1.3.6.1.2.1.1.1.0")
	oidSysUpTime = MustParseOID("1.3.6.1.2.1.1.3.0")
	oidSysName   = MustParseOID("1.3.6.1.2.1.1.5.0")
	oidHC        = MustParseOID("1.3.6.1.2.1.31.1.1.1.6")
)

// testMIB has the system, interfaces and snmp groups, a Counter64 column
// (ifHCInOctets, RFC 2863) and the LLDP-MIB.
func testMIB(t testing.TB, stats func() Stats) *MIB {
	t.Helper()
	m := NewMIB()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(m.AddSystem(func() System {
		return System{Descr: "go-profinet test", UpTime: 1234 * time.Millisecond, Contact: "ops", Name: "dut", Location: "lab"}
	}))
	must(m.AddInterfaces(func() []Interface {
		return []Interface{
			{Index: 2, Descr: "port-002", Type: IfTypeEthernetCsmacd, MTU: 1500, Speed: 100000000, PhysAddress: []byte{2, 0, 0, 0, 0, 2},
				AdminStatus: IfStatusUp, OperStatus: IfStatusDown, Counters: &InterfaceCounters{InOctets: 10, OutOctets: 20}},
			{Index: 1, Descr: "port-001", Type: IfTypeEthernetCsmacd, MTU: 1500, PhysAddress: []byte{2, 0, 0, 0, 0, 1},
				AdminStatus: IfStatusUp, OperStatus: IfStatusUp, LastChange: time.Second},
			{Index: 0, Descr: "invalid"},
		}
	}))
	must(m.AddSNMPGroup(stats))
	must(m.AddSubtree(oidHC, func() []VarBind {
		return []VarBind{{Name: oidHC.Append(1), Value: Counter64(1 << 40)}}
	}))
	must(m.AddLLDP(func() LLDPLocal {
		return LLDPLocal{ChassisIDSubtype: LLDPChassisIDLocal, ChassisID: []byte("dut"), SysName: "dut", SysDesc: "go-profinet test",
			Ports: []LLDPLocalPort{{Num: 1, IDSubtype: LLDPPortIDLocal, ID: []byte("port-001.dut")}, {Num: 0}, {Num: 5000}}}
	}, func() []LLDPRemote {
		return []LLDPRemote{
			{TimeMark: 500, LocalPortNum: 1, Index: 1, ChassisIDSubtype: LLDPChassisIDLocal, ChassisID: []byte("plc"), PortIDSubtype: LLDPPortIDLocal, PortID: []byte("port-002.plc"), SysName: "plc"},
			{LocalPortNum: 1, Index: 2, ChassisIDSubtype: LLDPChassisIDMACAddress, ChassisID: []byte{2, 0, 0, 0, 0, 9}, PortIDSubtype: LLDPPortIDMACAddress, PortID: []byte{2, 0, 0, 0, 0, 9}},
			{LocalPortNum: 0, Index: 3},
		}
	}))
	return m
}

func udpConn(t testing.TB) net.PacketConn {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newTestAgent(t testing.TB, cfg Config) *Agent {
	t.Helper()
	if cfg.Community == "" {
		cfg.Community = "public"
	}
	var a *Agent
	if cfg.MIB == nil {
		cfg.MIB = testMIB(t, func() Stats { return a.Stats() })
	}
	a, err := NewAgent(udpConn(t), false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// exchange sends m to the agent and decodes the response; ok is false
// when the agent dropped the request.
func exchange(t testing.TB, a *Agent, m Message) (Message, bool) {
	t.Helper()
	if m.Community == nil {
		m.Community = []byte("public")
	}
	b, err := m.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := a.Handle(b)
	if r == nil {
		return Message{}, false
	}
	if len(r) > a.cfg.MaxMessageSize {
		t.Fatalf("response of %d octets exceeds %d", len(r), a.cfg.MaxMessageSize)
	}
	d, err := Decode(r)
	if err != nil {
		t.Fatal(err)
	}
	if d.PDU.Type != PDUResponse || d.PDU.RequestID != m.PDU.RequestID || d.Version != m.Version {
		t.Fatalf("response %+v to %+v", d, m)
	}
	return d, true
}

func req(v Version, typ PDUType, names ...OID) Message {
	m := Message{Version: v, PDU: PDU{Type: typ, RequestID: 42}}
	for _, n := range names {
		m.PDU.VarBinds = append(m.PDU.VarBinds, VarBind{Name: n, Value: Null()})
	}
	return m
}

// walk walks the agent with GetNext from OID 0.0 to the end.
func walk(t testing.TB, a *Agent, v Version) []VarBind {
	t.Helper()
	var out []VarBind
	name := OID{0, 0}
	for range 1000 {
		r, ok := exchange(t, a, req(v, PDUGetNextRequest, name))
		if !ok {
			t.Fatal("dropped")
		}
		if v == Version1 && r.PDU.ErrorStatus == NoSuchName {
			return out
		}
		vb := r.PDU.VarBinds[0]
		if vb.Value.Type == TypeEndOfMibView {
			return out
		}
		if vb.Name.Compare(name) <= 0 {
			t.Fatalf("%s after %s", vb.Name, name)
		}
		out = append(out, vb)
		name = vb.Name
	}
	t.Fatal("walk does not end")
	return nil
}

func find(vbs []VarBind, s string) (Value, bool) {
	o := MustParseOID(s)
	for _, vb := range vbs {
		if vb.Name.Equal(o) {
			return vb.Value, true
		}
	}
	return Value{}, false
}

func TestWalkV2c(t *testing.T) {
	a := newTestAgent(t, Config{})
	vbs := walk(t, a, Version2c)
	if !slices.IsSortedFunc(vbs, func(x, y VarBind) int { return x.Name.Compare(y.Name) }) {
		t.Fatal("not in lexicographic order")
	}
	for _, tc := range []struct{ oid, want string }{
		{"1.3.6.1.2.1.1.1.0", `OCTET STRING: "go-profinet test"`},
		{"1.3.6.1.2.1.1.2.0", "OID: 0.0"},
		{"1.3.6.1.2.1.1.3.0", "TimeTicks: 123"},
		{"1.3.6.1.2.1.1.4.0", `OCTET STRING: "ops"`},
		{"1.3.6.1.2.1.1.5.0", `OCTET STRING: "dut"`},
		{"1.3.6.1.2.1.1.6.0", `OCTET STRING: "lab"`},
		{"1.3.6.1.2.1.1.7.0", "INTEGER: 72"},
		{"1.3.6.1.2.1.2.1.0", "INTEGER: 3"},
		{"1.3.6.1.2.1.2.2.1.1.1", "INTEGER: 1"},
		{"1.3.6.1.2.1.2.2.1.1.2", "INTEGER: 2"},
		{"1.3.6.1.2.1.2.2.1.2.1", `OCTET STRING: "port-001"`},
		{"1.3.6.1.2.1.2.2.1.3.1", "INTEGER: 6"},
		{"1.3.6.1.2.1.2.2.1.5.2", "Gauge32: 100000000"},
		{"1.3.6.1.2.1.2.2.1.8.2", "INTEGER: 2"},
		{"1.3.6.1.2.1.2.2.1.9.1", "TimeTicks: 100"},
		{"1.3.6.1.2.1.2.2.1.10.2", "Counter32: 10"},
		{"1.3.6.1.2.1.2.2.1.16.2", "Counter32: 20"},
		{"1.3.6.1.2.1.11.30.0", "INTEGER: 2"},
		{"1.3.6.1.2.1.11.32.0", "Counter32: 0"},
		{"1.3.6.1.2.1.31.1.1.1.6.1", "Counter64: 1099511627776"},
		{"1.0.8802.1.1.2.1.3.1.0", "INTEGER: 7"},
		{"1.0.8802.1.1.2.1.3.2.0", `OCTET STRING: "dut"`},
		{"1.0.8802.1.1.2.1.3.3.0", `OCTET STRING: "dut"`},
		{"1.0.8802.1.1.2.1.3.4.0", `OCTET STRING: "go-profinet test"`},
		{"1.0.8802.1.1.2.1.3.7.1.2.1", "INTEGER: 7"},
		{"1.0.8802.1.1.2.1.3.7.1.3.1", `OCTET STRING: "port-001.dut"`},
		{"1.0.8802.1.1.2.1.3.7.1.4.1", `OCTET STRING: ""`},
		{"1.0.8802.1.1.2.1.4.1.1.4.0.1.1", "INTEGER: 7"},
		{"1.0.8802.1.1.2.1.4.1.1.5.0.1.1", `OCTET STRING: "plc"`},
		{"1.0.8802.1.1.2.1.4.1.1.7.0.1.1", `OCTET STRING: "port-002.plc"`},
		{"1.0.8802.1.1.2.1.4.1.1.9.0.1.1", `OCTET STRING: "plc"`},
		{"1.0.8802.1.1.2.1.4.1.1.4.0.1.2", "INTEGER: 4"},
		{"1.0.8802.1.1.2.1.4.1.1.5.0.1.2", "OCTET STRING: \"\\x02\\x00\\x00\\x00\\x00\\t\""},
	} {
		v, ok := find(vbs, tc.oid)
		if !ok || v.String() != tc.want {
			t.Errorf("%s: %v (%v), want %s", tc.oid, v, ok, tc.want)
		}
	}
	// Left out: the interface with index 0, the counters of the
	// interface without them, invalid LLDP ports and rows.
	for _, oid := range []string{"1.3.6.1.2.1.2.2.1.1.0", "1.3.6.1.2.1.2.2.1.10.1", "1.0.8802.1.1.2.1.3.7.1.2.5000", "1.0.8802.1.1.2.1.4.1.1.4.0.0.3"} {
		if _, ok := find(vbs, oid); ok {
			t.Errorf("%s is served", oid)
		}
	}
	// The LLDP-MIB (1.0...) comes before MIB-II (1.3...).
	if vbs[0].Name[1] != 0 {
		t.Fatalf("first %s", vbs[0].Name)
	}
}

func TestWalkV1HidesCounter64(t *testing.T) {
	a := newTestAgent(t, Config{})
	v1 := walk(t, a, Version1)
	v2 := walk(t, a, Version2c)
	if len(v1) != len(v2)-1 {
		t.Fatalf("v1 %d, v2c %d instances", len(v1), len(v2))
	}
	if _, ok := find(v1, "1.3.6.1.2.1.31.1.1.1.6.1"); ok {
		t.Fatal("Counter64 in a v1 walk (RFC 3584 section 4.2.2.1)")
	}
	// A v1 Get of it: noSuchName.
	r, _ := exchange(t, a, req(Version1, PDUGetRequest, oidSysDescr, oidHC.Append(1)))
	if r.PDU.ErrorStatus != NoSuchName || r.PDU.ErrorIndex != 2 {
		t.Fatalf("v1 Get Counter64: %+v", r.PDU)
	}
}

func TestGet(t *testing.T) {
	a := newTestAgent(t, Config{})
	r, _ := exchange(t, a, req(Version2c, PDUGetRequest, oidSysDescr, oidSysName, MustParseOID("1.3.6.1.2.1.1.5.1"),
		MustParseOID("1.3.6.1.2.1.1.8.0"), MustParseOID("1.3.6.1.2.1.2.2.1.1.9"), MustParseOID("1.9")))
	want := []Type{TypeOctetString, TypeOctetString, TypeNoSuchInstance, TypeNoSuchObject, TypeNoSuchInstance, TypeNoSuchObject}
	if r.PDU.ErrorStatus != NoError || len(r.PDU.VarBinds) != len(want) {
		t.Fatalf("%+v", r.PDU)
	}
	for i, w := range want {
		if r.PDU.VarBinds[i].Value.Type != w {
			t.Errorf("%d: %v, want %v", i, r.PDU.VarBinds[i].Value, w)
		}
	}
	// v1: noSuchName with the index of the first missing name, and the
	// variable bindings of the request (RFC 1157 section 4.1.2).
	in := req(Version1, PDUGetRequest, oidSysDescr, MustParseOID("1.3.6.1.2.1.1.8.0"), MustParseOID("1.9"))
	r, _ = exchange(t, a, in)
	if r.PDU.ErrorStatus != NoSuchName || r.PDU.ErrorIndex != 2 || len(r.PDU.VarBinds) != 3 || r.PDU.VarBinds[0].Value.Type != TypeNull {
		t.Fatalf("v1 %+v", r.PDU)
	}
	r, _ = exchange(t, a, req(Version1, PDUGetRequest, oidSysUpTime))
	if r.PDU.ErrorStatus != NoError || r.PDU.VarBinds[0].Value.Type != TypeTimeTicks {
		t.Fatalf("v1 %+v", r.PDU)
	}
	if s := a.Stats(); s.InGetRequests != 3 || s.OutNoSuchNames != 1 || s.OutGetResponses != 3 || s.InPkts != 3 {
		t.Fatalf("stats %+v", s)
	}
}

// TestLLDPTimeFilter: lldpRemTimeMark is a TimeFilter. Walks list each
// row once, at filter 0; a Get of filter t finds the rows changed at or
// after t.
func TestLLDPTimeFilter(t *testing.T) {
	a := newTestAgent(t, Config{})
	sysName := func(filter, port, idx uint32) OID { return OIDLLDPRemTable.Append(1, 9, filter, port, idx) }
	// Walk: rows 1 and 2 of port 1 at filter 0 only.
	var rows []OID
	for _, vb := range walk(t, a, Version2c) {
		if vb.Name.HasPrefix(OIDLLDPRemTable.Append(1, 9)) {
			rows = append(rows, vb.Name)
		}
	}
	if len(rows) != 2 || !rows[0].Equal(sysName(0, 1, 1)) || !rows[1].Equal(sysName(0, 1, 2)) {
		t.Fatalf("walked rows %v", rows)
	}
	for _, c := range []struct {
		name OID
		want Value
	}{
		{sysName(0, 1, 1), OctetString([]byte("plc"))},
		{sysName(1, 1, 1), OctetString([]byte("plc"))},
		{sysName(500, 1, 1), OctetString([]byte("plc"))},
		{sysName(501, 1, 1), Value{Type: TypeNoSuchInstance}},
		// Row 2 changed at 0 (unknown): only filter 0 shows it.
		{sysName(0, 1, 2), OctetString(nil)},
		{sysName(1, 1, 2), Value{Type: TypeNoSuchInstance}},
		// Wrong port, index, column, or not a full instance name.
		{sysName(1, 2, 1), Value{Type: TypeNoSuchInstance}},
		{sysName(1, 1, 3), Value{Type: TypeNoSuchInstance}},
		{OIDLLDPRemTable.Append(1, 11, 1, 1, 1), Value{Type: TypeNoSuchInstance}},
		{OIDLLDPRemTable.Append(1, 9, 1, 1), Value{Type: TypeNoSuchInstance}},
	} {
		r, ok := exchange(t, a, req(Version2c, PDUGetRequest, c.name))
		if !ok || len(r.PDU.VarBinds) != 1 {
			t.Fatalf("%s: %+v", c.name, r)
		}
		got := r.PDU.VarBinds[0].Value
		if got.Type != c.want.Type || string(got.Bytes) != string(c.want.Bytes) {
			t.Errorf("Get %s = %+v, want %+v", c.name, got, c.want)
		}
	}
	// SNMPv1 reports the missing instance as noSuchName.
	r, _ := exchange(t, a, req(Version1, PDUGetRequest, sysName(501, 1, 1)))
	if r.PDU.ErrorStatus != NoSuchName {
		t.Fatalf("v1 Get of a filtered row: %+v", r.PDU)
	}
	r, _ = exchange(t, a, req(Version1, PDUGetRequest, sysName(100, 1, 1)))
	if r.PDU.ErrorStatus != NoError {
		t.Fatalf("v1 Get of a row: %+v", r.PDU)
	}
}

func TestGetNextEnd(t *testing.T) {
	a := newTestAgent(t, Config{})
	end := MustParseOID("1.3.6.1.2.1.31.1.1.1.6.1")
	r, _ := exchange(t, a, req(Version2c, PDUGetNextRequest, end, oidSysDescr))
	if vb := r.PDU.VarBinds[0]; vb.Value.Type != TypeEndOfMibView || !vb.Name.Equal(end) || r.PDU.ErrorStatus != NoError {
		t.Fatalf("%+v", r.PDU)
	}
	if !r.PDU.VarBinds[1].Name.Equal(MustParseOID("1.3.6.1.2.1.1.2.0")) {
		t.Fatalf("next of sysDescr.0: %s", r.PDU.VarBinds[1].Name)
	}
	// From a prefix of a subtree, an OID between rows, and one past a
	// subtree.
	for _, tc := range []struct{ from, want string }{
		{"1.3", "1.3.6.1.2.1.1.1.0"},
		{"1.3.6.1.2.1.2.2.1.1.1.5", "1.3.6.1.2.1.2.2.1.1.2"},
		{"1.3.6.1.2.1.2.2.1.1", "1.3.6.1.2.1.2.2.1.1.1"},
		{"1.3.6.1.2.1.1.9", "1.3.6.1.2.1.2.1.0"},
		{"1.0.8802.1.1.2.1.3.4.0", "1.0.8802.1.1.2.1.3.7.1.2.1"},
	} {
		r, _ := exchange(t, a, req(Version2c, PDUGetNextRequest, MustParseOID(tc.from)))
		if got := r.PDU.VarBinds[0].Name.String(); got != tc.want {
			t.Errorf("next of %s: %s, want %s", tc.from, got, tc.want)
		}
	}
	r, _ = exchange(t, a, req(Version1, PDUGetNextRequest, oidSysDescr, MustParseOID("1.4")))
	if r.PDU.ErrorStatus != NoSuchName || r.PDU.ErrorIndex != 2 {
		t.Fatalf("v1 %+v", r.PDU)
	}
}

func bulk(names []OID, nonRep, maxRep int32) Message {
	m := req(Version2c, PDUGetBulkRequest, names...)
	m.PDU.NonRepeaters, m.PDU.MaxRepetitions = nonRep, maxRep
	return m
}

func TestGetBulk(t *testing.T) {
	a := newTestAgent(t, Config{})
	sys := MustParseOID("1.3.6.1.2.1.1")
	ifIndex := MustParseOID("1.3.6.1.2.1.2.2.1.1")
	ifDescr := MustParseOID("1.3.6.1.2.1.2.2.1.2")
	// N = 1 (sysDescr), then R = 2 columns of the ifTable, M = 3.
	r, _ := exchange(t, a, bulk([]OID{sys, ifIndex, ifDescr}, 1, 3))
	var got []string
	for _, vb := range r.PDU.VarBinds {
		got = append(got, vb.Name.String())
	}
	want := []string{"1.3.6.1.2.1.1.1.0",
		"1.3.6.1.2.1.2.2.1.1.1", "1.3.6.1.2.1.2.2.1.2.1",
		"1.3.6.1.2.1.2.2.1.1.2", "1.3.6.1.2.1.2.2.1.2.2",
		"1.3.6.1.2.1.2.2.1.2.1", "1.3.6.1.2.1.2.2.1.3.1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	// Negative values count as 0; non-repeaters above the number of
	// variable bindings as all of them.
	r, _ = exchange(t, a, bulk([]OID{sys}, -1, -1))
	if len(r.PDU.VarBinds) != 0 {
		t.Fatalf("M=0: %+v", r.PDU)
	}
	r, _ = exchange(t, a, bulk([]OID{sys, ifIndex}, 5, 10))
	if len(r.PDU.VarBinds) != 2 {
		t.Fatalf("N=L: %+v", r.PDU)
	}
	// At the end of the MIB: endOfMibView, and the repetitions stop.
	end := MustParseOID("1.3.6.1.2.1.31.1.1.1.6.1")
	r, _ = exchange(t, a, bulk([]OID{end, MustParseOID("1.3.6.1.2.1.11.32.0")}, 0, 10))
	if len(r.PDU.VarBinds) != 4 || r.PDU.VarBinds[0].Value.Type != TypeEndOfMibView || r.PDU.VarBinds[3].Value.Type != TypeEndOfMibView ||
		r.PDU.VarBinds[2].Value.Type != TypeEndOfMibView || r.PDU.VarBinds[1].Value.Type != TypeCounter64 {
		t.Fatalf("end: %+v", r.PDU.VarBinds)
	}
	r, _ = exchange(t, a, bulk([]OID{end}, 1, 10))
	if len(r.PDU.VarBinds) != 1 || r.PDU.VarBinds[0].Value.Type != TypeEndOfMibView {
		t.Fatalf("non-repeater at end: %+v", r.PDU.VarBinds)
	}
	if s := a.Stats(); s.InGetBulks != 5 {
		t.Fatalf("stats %+v", s)
	}
}

func TestGetBulkBounds(t *testing.T) {
	big := NewMIB()
	if err := big.AddSubtree(OID{1, 3, 9}, func() []VarBind {
		out := make([]VarBind, 1000)
		for i := range out {
			out[i] = VarBind{Name: OID{1, 3, 9, uint32(i + 1)}, Value: String(strings.Repeat("x", 50))}
		}
		return out
	}); err != nil {
		t.Fatal(err)
	}
	// Truncated to the message size (RFC 3416 section 4.2.3).
	a := newTestAgent(t, Config{MIB: big, MaxRepetitions: 1000, MaxBulkVarBinds: 1000})
	r, _ := exchange(t, a, bulk([]OID{{1, 3}}, 0, 1000))
	n := len(r.PDU.VarBinds)
	if n < 10 || n > 30 || r.PDU.ErrorStatus != NoError {
		t.Fatalf("%d variable bindings, %v", n, r.PDU.ErrorStatus)
	}
	// Bounded by MaxRepetitions and MaxBulkVarBinds.
	a = newTestAgent(t, Config{MIB: big, MaxRepetitions: 3, MaxMessageSize: 60000})
	if r, _ := exchange(t, a, bulk([]OID{{1, 3}, {1, 3}}, 0, 100)); len(r.PDU.VarBinds) != 6 {
		t.Fatalf("max-repetitions: %d", len(r.PDU.VarBinds))
	}
	a = newTestAgent(t, Config{MIB: big, MaxRepetitions: 100, MaxBulkVarBinds: 7, MaxMessageSize: 60000})
	if r, _ := exchange(t, a, bulk([]OID{{1, 3}}, 0, 100)); len(r.PDU.VarBinds) != 7 {
		t.Fatalf("max varbinds: %d", len(r.PDU.VarBinds))
	}
	// The non-repeaters are truncated as well.
	a = newTestAgent(t, Config{MIB: big})
	names := make([]OID, 40)
	for i := range names {
		names[i] = OID{1, 3, 9, uint32(i)}
	}
	if r, _ := exchange(t, a, bulk(names, 40, 1)); len(r.PDU.VarBinds) >= 40 || len(r.PDU.VarBinds) == 0 {
		t.Fatalf("non-repeaters: %d", len(r.PDU.VarBinds))
	}
	// A community longer than the message size: not even the header
	// fits.
	a = newTestAgent(t, Config{MIB: big, Community: strings.Repeat("c", 600), MaxMessageSize: MinMessageSize})
	m := bulk([]OID{{1, 3}}, 0, 1)
	m.Community = []byte(strings.Repeat("c", 600))
	if _, ok := exchange(t, a, m); ok {
		t.Fatal("answered")
	}
	if a.Stats().SilentDrops != 1 {
		t.Fatalf("stats %+v", a.Stats())
	}
}

func TestTooBig(t *testing.T) {
	big := NewMIB()
	if err := big.AddScalar(OID{1, 3, 9}, func() Value { return String(strings.Repeat("x", 600)) }); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, Config{MIB: big, MaxMessageSize: 100})
	if a.cfg.MaxMessageSize != MinMessageSize {
		t.Fatalf("MaxMessageSize %d", a.cfg.MaxMessageSize)
	}
	// v2c: tooBig, index 0, no variable bindings (RFC 3416 section 4.2.1).
	r, ok := exchange(t, a, req(Version2c, PDUGetRequest, OID{1, 3, 9, 0}))
	if !ok || r.PDU.ErrorStatus != TooBig || r.PDU.ErrorIndex != 0 || len(r.PDU.VarBinds) != 0 {
		t.Fatalf("v2c %+v", r.PDU)
	}
	// v1: tooBig, index 0, the request's variable bindings (RFC 1157
	// section 4.1.3).
	r, ok = exchange(t, a, req(Version1, PDUGetNextRequest, OID{1, 3}))
	if !ok || r.PDU.ErrorStatus != TooBig || r.PDU.ErrorIndex != 0 || len(r.PDU.VarBinds) != 1 || !r.PDU.VarBinds[0].Name.Equal(OID{1, 3}) {
		t.Fatalf("v1 %+v", r.PDU)
	}
	// v1 with a request that does not fit as a response either: dropped
	// and counted in snmpSilentDrops.
	names := make([]OID, 10)
	for i := range names {
		names[i] = append(OID{1, 3, 9, 0}, make(OID, 60)...)
	}
	if _, ok := exchange(t, a, req(Version1, PDUGetRequest, names...)); ok {
		t.Fatal("answered")
	}
	if s := a.Stats(); s.OutTooBigs != 2 || s.SilentDrops != 1 || s.OutGetResponses != 2 {
		t.Fatalf("stats %+v", s)
	}
}

func TestSet(t *testing.T) {
	a := newTestAgent(t, Config{})
	set := func(v Version) Message {
		m := req(v, PDUSetRequest)
		m.PDU.VarBinds = []VarBind{{Name: oidSysName, Value: String("new")}, {Name: oidSysDescr, Value: String("x")}}
		return m
	}
	r, _ := exchange(t, a, set(Version1))
	if r.PDU.ErrorStatus != NoSuchName || r.PDU.ErrorIndex != 1 || len(r.PDU.VarBinds) != 2 || string(r.PDU.VarBinds[0].Value.Bytes) != "new" {
		t.Fatalf("v1 %+v", r.PDU)
	}
	r, _ = exchange(t, a, set(Version2c))
	if r.PDU.ErrorStatus != NotWritable || r.PDU.ErrorIndex != 1 || len(r.PDU.VarBinds) != 2 {
		t.Fatalf("v2c %+v", r.PDU)
	}
	r, _ = exchange(t, a, req(Version2c, PDUSetRequest))
	if r.PDU.ErrorStatus != NoError || len(r.PDU.VarBinds) != 0 {
		t.Fatalf("empty %+v", r.PDU)
	}
	if v, ok := find(walk(t, a, Version2c), "1.3.6.1.2.1.1.5.0"); !ok || string(v.Bytes) != "dut" {
		t.Fatal("sysName changed")
	}
	if s := a.Stats(); s.InSetRequests != 3 || s.InBadCommunityUses != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestDrops(t *testing.T) {
	a := newTestAgent(t, Config{})
	wrong := req(Version2c, PDUGetRequest, oidSysDescr)
	wrong.Community = []byte("private")
	if _, ok := exchange(t, a, wrong); ok {
		t.Fatal("wrong community answered")
	}
	wrong.Community = []byte("publi")
	if _, ok := exchange(t, a, wrong); ok {
		t.Fatal("prefix of the community answered")
	}
	if a.Handle(unhex(t, "30 08 02 01 03 30 03 02 01 01")) != nil {
		t.Fatal("v3 answered")
	}
	if a.Handle([]byte{0x30, 0x80}) != nil || a.Handle(nil) != nil {
		t.Fatal("garbage answered")
	}
	for _, typ := range []PDUType{PDUResponse, PDUTrapV2, PDUInformRequest, PDUReport} {
		if _, ok := exchange(t, a, req(Version2c, typ, oidSysDescr)); ok {
			t.Fatalf("%s answered", typ)
		}
	}
	trap := unhex(t, "30 0f 02 01 00 04 06 70 75 62 6c 69 63 a4 02 05 00")
	if a.Handle(trap) != nil {
		t.Fatal("trap answered")
	}
	s := a.Stats()
	if s.InBadCommunityNames != 2 || s.InBadVersions != 1 || s.InASNParseErrs != 2 || s.UnsupportedPDUs != 5 || s.InPkts != 10 || s.OutGetResponses != 0 {
		t.Fatalf("stats %+v", s)
	}
	// The snmp group serves the counters.
	r, _ := exchange(t, a, req(Version2c, PDUGetRequest, MustParseOID("1.3.6.1.2.1.11.4.0"), MustParseOID("1.3.6.1.2.1.11.3.0"), MustParseOID("1.3.6.1.2.1.11.1.0")))
	if r.PDU.VarBinds[0].Value.Uint != 2 || r.PDU.VarBinds[1].Value.Uint != 1 || r.PDU.VarBinds[2].Value.Uint != 11 {
		t.Fatalf("snmp group %+v", r.PDU.VarBinds)
	}
}

// allows counts the requests of n at now that the rate limit lets
// through.
func allows(a *Agent, now time.Time, n int) int {
	c := 0
	for range n {
		if a.allow(now) {
			c++
		}
	}
	return c
}

func TestRateLimit(t *testing.T) {
	a := newTestAgent(t, Config{Rate: 10, Burst: 2})
	now := time.Unix(1000, 0)
	if got := allows(a, now, 3); got != 2 {
		t.Fatalf("burst: %d of 3", got)
	}
	if a.allow(now.Add(50*time.Millisecond)) || !a.allow(now.Add(160*time.Millisecond)) {
		t.Fatal("refill")
	}
	// Refilled to the burst at most.
	later := now.Add(time.Hour)
	if got := allows(a, later, 3); got != 2 {
		t.Fatalf("cap: %d of 3", got)
	}
	u := newTestAgent(t, Config{Rate: -1})
	for range 1000 {
		if !u.allow(now) {
			t.Fatal("unlimited")
		}
	}
}

func TestNewAgentConfig(t *testing.T) {
	c := udpConn(t)
	for _, cfg := range []Config{{MIB: NewMIB()}, {Community: "x"}} {
		if _, err := NewAgent(c, false, cfg); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Fatalf("%+v: %v", cfg, err)
		}
	}
	if _, err := NewAgent(nil, false, Config{Community: "x", MIB: NewMIB()}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatal(err)
	}
	a, err := NewAgent(c, false, Config{Community: "x", MIB: NewMIB(), MaxMessageSize: 1 << 20})
	if err != nil || a.cfg.MaxMessageSize != MaxMessageSize || a.cfg.Rate != DefaultRate || a.cfg.Burst != DefaultBurst ||
		a.cfg.MaxRepetitions != DefaultRepetitions || a.cfg.MaxBulkVarBinds != DefaultBulkVarBinds {
		t.Fatalf("%+v %v", a.cfg, err)
	}
	if a.LocalAddr().String() != c.LocalAddr().String() {
		t.Fatal("LocalAddr")
	}
}

func TestMIB(t *testing.T) {
	m := NewMIB()
	fn := func() []VarBind { return nil }
	if err := m.AddSubtree(OID{1, 3, 6}, fn); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		p  OID
		fn func() []VarBind
	}{
		{OID{1, 3}, fn}, {OID{1, 3, 6}, fn}, {OID{1, 3, 6, 1}, fn}, {OID{1}, fn}, {OID{5, 1}, fn}, {OID{1, 4}, nil},
	} {
		if err := m.AddSubtree(tc.p, tc.fn); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%s: %v", tc.p, err)
		}
	}
	if err := m.AddScalar(OID{1, 3, 6, 2}, func() Value { return Null() }); err == nil {
		t.Error("scalar inside a subtree")
	}
	// The provider's instances are filtered and sorted.
	m = NewMIB()
	p := OID{1, 3, 7}
	if err := m.AddSubtree(p, func() []VarBind {
		return []VarBind{
			{Name: OID{1, 3, 7, 3}, Value: Integer(3)},
			{Name: OID{1, 3, 7, 1}, Value: Integer(1)},
			{Name: OID{1, 3, 7, 1}, Value: Integer(9)}, // duplicate
			{Name: OID{1, 3, 7}, Value: Integer(0)},    // the prefix itself
			{Name: OID{1, 3, 8, 1}, Value: Integer(0)}, // outside
			{Name: OID{1, 3, 7, 2}, Value: Value{}},    // no value
			{Name: OID{1, 3, 7, 4}, Value: Value{Type: TypeEndOfMibView}},
			{Name: append(OID{1, 3, 7}, make(OID, MaxOIDLen)...), Value: Integer(0)},
		}
	}); err != nil {
		t.Fatal(err)
	}
	var none bool
	if err := m.AddScalar(OID{1, 3, 5}, func() Value {
		if none {
			return Value{}
		}
		return Integer(5)
	}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, Config{MIB: m})
	got := walk(t, a, Version2c)
	if len(got) != 3 || got[0].Name.String() != "1.3.5.0" || got[1].Value.Int != 1 || got[2].Value.Int != 3 {
		t.Fatalf("%+v", got)
	}
	none = true
	if got := walk(t, a, Version2c); len(got) != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestDisplayStringAndTicks(t *testing.T) {
	s := strings.Repeat("a", 254) + "é" // é is two octets: cut before it
	if v := displayString(s); len(v.Bytes) != 254 {
		t.Fatalf("%d", len(v.Bytes))
	}
	if v := ticks(-time.Second); v.Uint != 0 {
		t.Fatal(v)
	}
	if v := lldpID(bytes.Repeat([]byte{1}, 300)); len(v.Bytes) != 255 {
		t.Fatal(len(v.Bytes))
	}
	m := NewMIB()
	if err := m.AddSystem(func() System { return System{ObjectID: OID{1, 3, 6, 1, 4, 1, 1}, Services: 78} }); err != nil {
		t.Fatal(err)
	}
	if err := m.AddLLDP(func() LLDPLocal { return LLDPLocal{} }, nil); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, Config{MIB: m})
	vbs := walk(t, a, Version2c)
	if v, _ := find(vbs, "1.3.6.1.2.1.1.2.0"); v.String() != "OID: 1.3.6.1.4.1.1" {
		t.Fatal(v)
	}
	if v, _ := find(vbs, "1.3.6.1.2.1.1.7.0"); v.Int != 78 {
		t.Fatal(v)
	}
	if _, ok := find(vbs, "1.0.8802.1.1.2.1.3.2.0"); ok {
		t.Fatal("empty chassis ID served")
	}
	// Overlapping registrations fail.
	if m.AddSystem(func() System { return System{} }) == nil || m.AddLLDP(func() LLDPLocal { return LLDPLocal{} }, nil) == nil {
		t.Fatal("overlap")
	}
	if m.AddInterfaces(func() []Interface { return nil }) != nil || m.AddSNMPGroup(func() Stats { return Stats{} }) != nil {
		t.Fatal("first registration")
	}
	if m.AddInterfaces(func() []Interface { return nil }) == nil || m.AddSNMPGroup(func() Stats { return Stats{} }) == nil {
		t.Fatal("overlap")
	}
	// An overlap within a module's later objects.
	m2 := NewMIB()
	_ = m2.AddScalar(OIDLLDPLocalSystemData.Append(7), func() Value { return Null() })
	if m2.AddLLDP(func() LLDPLocal { return LLDPLocal{} }, nil) == nil {
		t.Fatal("port table overlap")
	}
	m3 := NewMIB()
	_ = m3.AddScalar(OIDLLDPRemTable, func() Value { return Null() })
	if m3.AddLLDP(func() LLDPLocal { return LLDPLocal{} }, nil) == nil {
		t.Fatal("remote table overlap")
	}
	m4 := NewMIB()
	_ = m4.AddScalar(OIDInterfaces.Append(2), func() Value { return Null() })
	if m4.AddInterfaces(func() []Interface { return nil }) == nil {
		t.Fatal("ifTable overlap")
	}
}

// TestServe runs the agent on a UDP socket: requests are answered, the
// rate limit drops, and Close and the context stop Serve.
func TestServe(t *testing.T) {
	conn := udpConn(t)
	var a *Agent
	a, err := NewAgent(conn, true, Config{Community: "public", MIB: testMIB(t, func() Stats { return a.Stats() }), Rate: 1, Burst: 3})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Serve(context.Background()) }()
	cl := udpConn(t)
	ask := func(m Message) (Message, bool) {
		b, _ := m.AppendTo(nil)
		if _, err := cl.WriteTo(b, conn.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		_ = cl.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 2000)
		n, _, err := cl.ReadFrom(buf)
		if err != nil {
			return Message{}, false
		}
		r, err := Decode(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		return r, true
	}
	m := req(Version2c, PDUGetRequest, oidSysName)
	m.Community = []byte("public")
	for i := range 3 {
		r, ok := ask(m)
		if !ok || string(r.PDU.VarBinds[0].Value.Bytes) != "dut" {
			t.Fatalf("request %d: %+v %v", i, r, ok)
		}
	}
	if _, ok := ask(m); ok {
		t.Fatal("rate limit")
	}
	if err := a.Serve(context.Background()); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("second Serve: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Serve(context.Background()); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("Serve after Close: %v", err)
	}
	if s := a.Stats(); s.OutPkts != 3 || s.RateLimited != 1 {
		t.Fatalf("stats %+v", s)
	}
	// Owned: the socket is closed.
	if _, err := conn.WriteTo([]byte{0}, cl.LocalAddr()); err == nil {
		t.Fatal("socket open")
	}
}

func TestServeContext(t *testing.T) {
	a := newTestAgent(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve: %v", err)
	}
	// Serve again with a new context: the old deadline is reset.
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { done <- a.Serve(ctx2) }()
	cl := udpConn(t)
	m := req(Version2c, PDUGetRequest, oidSysName)
	m.Community = []byte("public")
	b, _ := m.AppendTo(nil)
	_, _ = cl.WriteTo(b, a.LocalAddr())
	_ = cl.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := cl.ReadFrom(make([]byte, 2000)); err != nil {
		t.Fatalf("second Serve does not answer: %v", err)
	}
	cancel2()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve: %v", err)
	}
	// Not owned: the socket stays open.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.conn.WriteTo([]byte{0}, cl.LocalAddr()); err != nil {
		t.Fatalf("socket closed: %v", err)
	}
}

// errConn fails reads with a non-timeout error, then times out once.
type errConn struct {
	net.PacketConn
	n atomic.Int32
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func (c *errConn) ReadFrom([]byte) (int, net.Addr, error) {
	if c.n.Add(1) == 1 {
		return 0, nil, timeoutErr{}
	}
	return 0, nil, errors.New("broken")
}

// failWrite fails every write.
type failWrite struct{ net.PacketConn }

func (failWrite) WriteTo([]byte, net.Addr) (int, error) { return 0, errors.New("unreachable") }

func TestServeErrors(t *testing.T) {
	a, err := NewAgent(&errConn{PacketConn: udpConn(t)}, false, Config{Community: "c", MIB: NewMIB()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Serve(context.Background()); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("Serve: %v", err)
	}
	conn := failWrite{udpConn(t)}
	a, err = NewAgent(conn, false, Config{Community: "public", MIB: testMIB(t, func() Stats { return Stats{} })})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Serve(context.Background()) }()
	cl := udpConn(t)
	m := req(Version2c, PDUGetRequest, oidSysName)
	m.Community = []byte("public")
	b, _ := m.AppendTo(nil)
	_, _ = cl.WriteTo(b, conn.LocalAddr())
	deadline := time.Now().Add(2 * time.Second)
	for a.Stats().OutGetResponses == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	_ = a.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := a.Stats(); s.OutGetResponses != 1 || s.OutPkts != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func FuzzAgentHandle(f *testing.F) {
	f.Add(unhex(f, goldenGet))
	f.Add(unhex(f, goldenBulk))
	f.Add(unhex(f, "30 08 02 01 03 30 03 02 01 01"))
	m := bulk([]OID{{1, 3}, {1, 0}}, 1, 50)
	m.Community = []byte("public")
	b, _ := m.AppendTo(nil)
	f.Add(b)
	var a *Agent
	a, err := NewAgent(udpConn(f), false, Config{Community: "public", MIB: testMIB(f, func() Stats { return a.Stats() })})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		r := a.Handle(b)
		if r == nil {
			return
		}
		if len(r) > a.cfg.MaxMessageSize {
			t.Fatalf("response of %d octets", len(r))
		}
		if _, err := Decode(r); err != nil {
			t.Fatalf("response does not decode: %v", err)
		}
	})
}
