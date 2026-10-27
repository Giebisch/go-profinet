// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Net-SNMP tests run go-profinet's device (otfnode device -snmp) on a
// Docker network and read its SNMP agent with Net-SNMP's command-line
// manager (profinet-interop's netsnmp adapter: snmpget, snmpgetnext,
// snmpbulkget, snmpwalk, snmpbulkwalk, snmpset), an implementation of the
// manager side independent of go-profinet. Net-SNMP decodes every
// response itself; the adapter reports what it decoded.

// snmpBind is a variable binding as Net-SNMP decoded it.
type snmpBind struct {
	OID   string `json:"oid"`
	Type  string `json:"type"`
	Value string `json:"value"`
	Hex   string `json:"hex"`
}

// snmpResult is the netsnmp adapter's report of one operation.
type snmpResult struct {
	Exit         int        `json:"exit"`
	VarBinds     []snmpBind `json:"varbinds"`
	ErrorStatus  string     `json:"errorStatus"`
	FailedObject string     `json:"failedObject"`
	Timeout      bool       `json:"timeout"`
	OK           bool       `json:"ok"`
	Stderr       string     `json:"stderr"`
}

// values returns the bindings that carry a value (no exception).
func (r snmpResult) values() []snmpBind {
	var out []snmpBind
	for _, b := range r.VarBinds {
		switch b.Type {
		case "noSuchObject", "noSuchInstance", "endOfMibView":
		default:
			out = append(out, b)
		}
	}
	return out
}

func (r snmpResult) byOID() map[string]snmpBind {
	out := map[string]snmpBind{}
	for _, b := range r.VarBinds {
		out[b.OID] = b
	}
	return out
}

var netsnmpSeq struct {
	sync.Mutex
	n int
}

// netsnmp runs one Net-SNMP operation against the device at host.
func (e *env) netsnmp(host int, op string, args ...string) snmpResult {
	e.t.Helper()
	netsnmpSeq.Lock()
	netsnmpSeq.n++
	name := fmt.Sprintf("netsnmp-%d", netsnmpSeq.n)
	netsnmpSeq.Unlock()
	a := append([]string{"snmp", op, "--host", e.ip(host)}, args...)
	out := e.run(name, image("netsnmp"), 240, 3*time.Minute, a...)
	var r snmpResult
	if err := json.Unmarshal(lastLine(out), &r); err != nil {
		e.t.Fatalf("netsnmp %s: %v: %s", op, err, out)
	}
	return r
}

// counter reads one Counter32 of the agent with Net-SNMP.
func (e *env) snmpCounter(host int, oid string) int {
	e.t.Helper()
	r := e.netsnmp(host, "get", oid)
	if len(r.VarBinds) != 1 || r.VarBinds[0].Type != "Counter32" {
		e.t.Fatalf("counter %s: %+v", oid, r)
	}
	v, err := strconv.Atoi(r.VarBinds[0].Value)
	if err != nil {
		e.t.Fatalf("counter %s: %v", oid, err)
	}
	return v
}

const (
	oidSysDescr     = "1.3.6.1.2.1.1.1.0"
	oidSysObjectID  = "1.3.6.1.2.1.1.2.0"
	oidSysUpTime    = "1.3.6.1.2.1.1.3.0"
	oidSysName      = "1.3.6.1.2.1.1.5.0"
	oidIfNumber     = "1.3.6.1.2.1.2.1.0"
	oidIfPhys       = "1.3.6.1.2.1.2.2.1.6.1"
	oidLLDPRem      = "1.0.8802.1.1.2.1.4.1"
	oidLLDPRemName  = "1.0.8802.1.1.2.1.4.1.1.9"
	oidLLDPLocName  = "1.0.8802.1.1.2.1.3.3.0"
	oidInPkts       = "1.3.6.1.2.1.11.1.0"
	oidInBadVersion = "1.3.6.1.2.1.11.3.0"
	oidInBadComm    = "1.3.6.1.2.1.11.4.0"
	oidInASNParse   = "1.3.6.1.2.1.11.6.0"
)

// startSNMPDevice starts otfnode as a device with SNMP at host 2 and
// waits until Net-SNMP reads its sysName.
func startSNMPDevice(e *env, station string, host int, extra ...string) string {
	e.t.Helper()
	c := e.startOTF(station, host, append([]string{"device", "-i", "eth0", "-station", station, "-snmp", "public", "-apply-ip=false"}, extra...)...)
	e.waitEvent(c, 30*time.Second, "start", named("start"))
	if !until(30*time.Second, func() bool { return e.netsnmp(host, "get", "--timeout", "1", oidSysName).OK }) {
		e.t.Fatalf("%s: no SNMP answer", station)
	}
	return c
}

// TestNetSNMPRead: Get, GetNext, GetBulk and walks of go-profinet's agent
// with Net-SNMP, in SNMPv1 and v2c: the system group, the interfaces, the
// LLDP-MIB's local data; GetNext and GetBulk walks agree.
func TestNetSNMPRead(t *testing.T) {
	e := newEnv(t)
	e.result.peer("Net-SNMP", image("netsnmp"), "SNMP manager (go-profinet: SNMP agent of the IO-Device)")
	dev := startSNMPDevice(e, "dut", 2)
	stop := e.capture(dev, "netsnmp-read.pcap")

	r := e.netsnmp(2, "get", oidSysDescr, oidSysObjectID, oidSysUpTime, oidSysName)
	m := r.byOID()
	e.expect("SNMP Get v2c of the system group (Net-SNMP)", r.OK && (m[oidSysDescr].Type == "Hex-STRING" || m[oidSysDescr].Type == "STRING") &&
		m[oidSysObjectID].Type == "OID" && m[oidSysUpTime].Type == "Timeticks" && hexText(m[oidSysName].Hex) == "dut",
		"%+v", r.VarBinds)
	r = e.netsnmp(2, "get", "--version", "1", oidSysName)
	e.expect("SNMP Get v1 (Net-SNMP)", r.OK && len(r.VarBinds) == 1 && hexText(r.VarBinds[0].Hex) == "dut", "%+v", r)

	r = e.netsnmp(2, "getnext", oidSysDescr)
	e.expect("SNMP GetNext (Net-SNMP)", r.OK && len(r.VarBinds) == 1 && r.VarBinds[0].OID == oidSysObjectID, "%+v", r.VarBinds)

	// GetBulk: sysDescr as a non-repeater, then 3 repetitions of the
	// successors of sysObjectID and ifNumber.
	r = e.netsnmp(2, "bulkget", "--non-rep", "1", "--max-rep", "3", "1.3.6.1.2.1.1", "1.3.6.1.2.1.1.1.0", "1.3.6.1.2.1.2.1")
	got := []string{}
	for _, b := range r.VarBinds {
		got = append(got, b.OID)
	}
	want := []string{oidSysDescr, oidSysObjectID, oidIfNumber, oidSysUpTime, "1.3.6.1.2.1.2.2.1.1.1", "1.3.6.1.2.1.1.4.0", "1.3.6.1.2.1.2.2.1.2.1"}
	e.expect("SNMP GetBulk with non-repeaters and repetitions (Net-SNMP)", r.OK && strings.Join(got, " ") == strings.Join(want, " "),
		"got %v, want %v", got, want)

	r = e.netsnmp(2, "get", oidIfNumber, oidIfPhys, "1.3.6.1.2.1.2.2.1.3.1")
	m = r.byOID()
	e.expect("SNMP ifTable: one Ethernet port with the device's MAC (Net-SNMP)", r.OK && m[oidIfNumber].Value == "1" &&
		m["1.3.6.1.2.1.2.2.1.3.1"].Value == "6" && len(m[oidIfPhys].Hex) == 12, "%+v", r.VarBinds)
	r = e.netsnmp(2, "get", oidLLDPLocName)
	e.expect("SNMP LLDP-MIB lldpLocSysName (Net-SNMP)", r.OK && len(r.VarBinds) == 1 && hexText(r.VarBinds[0].Hex) == "dut", "%+v", r.VarBinds)

	// Full walks: v2c GetNext, v2c GetBulk, v1 GetNext. They agree except
	// for the values that change between them.
	volatile := func(oid string) bool { return oid == oidSysUpTime || strings.HasPrefix(oid, "1.3.6.1.2.1.11.") }
	walk := func(op string, args ...string) map[string]snmpBind {
		out := map[string]snmpBind{}
		for _, root := range []string{"1.3.6.1.2.1", "1.0.8802"} {
			r := e.netsnmp(2, op, append(args, root)...)
			if !r.OK {
				t.Errorf("%s %s: %+v", op, root, r)
			}
			for _, b := range r.values() {
				out[b.OID] = b
			}
		}
		return out
	}
	next := walk("walk")
	bulk := walk("bulkwalk", "--max-rep", "8")
	v1 := walk("walk", "--version", "1")
	diff := func(a, b map[string]snmpBind) []string {
		var d []string
		for k, x := range a {
			y, ok := b[k]
			if !ok || (!volatile(k) && (x.Type != y.Type || x.Value != y.Value)) {
				d = append(d, k)
			}
		}
		for k := range b {
			if _, ok := a[k]; !ok {
				d = append(d, k)
			}
		}
		return d
	}
	e.expect("SNMP walks agree: GetNext and GetBulk (Net-SNMP)", len(next) > 30 && len(diff(next, bulk)) == 0,
		"%d instances, differences %v", len(next), diff(next, bulk))
	e.expect("SNMP walks agree: v1 and v2c (Net-SNMP)", len(diff(next, v1)) == 0, "differences %v", diff(next, v1))
	stop()
	e.oracles("netsnmp-read.pcap")
}

// hexText decodes the hex form of an octet string.
func hexText(h string) string {
	b, _ := hex.DecodeString(h)
	return string(b)
}

// TestNetSNMPErrors: what an agent answers to requests it cannot satisfy,
// as Net-SNMP reports it: names that do not exist (noSuchObject,
// noSuchInstance, v1 noSuchName), the end of the MIB, a Set (notWritable;
// v1 noSuchName), a wrong community (no answer), the largest OID, a GetBulk
// far beyond what a response holds, and a Get of more names than fit.
func TestNetSNMPErrors(t *testing.T) {
	e := newEnv(t)
	e.result.peer("Net-SNMP", image("netsnmp"), "SNMP manager (go-profinet: SNMP agent of the IO-Device)")
	startSNMPDevice(e, "dut", 2)

	r := e.netsnmp(2, "get", "1.3.6.1.2.1.1.99.0", "1.3.6.1.2.1.1.5.1", "1.3.6.1.4.1.99999.1.0")
	m := r.byOID()
	e.expect("SNMP v2c Get of unknown names: noSuchObject, noSuchInstance (Net-SNMP)",
		m["1.3.6.1.2.1.1.99.0"].Type == "noSuchObject" && m["1.3.6.1.2.1.1.5.1"].Type == "noSuchInstance" && m["1.3.6.1.4.1.99999.1.0"].Type == "noSuchObject",
		"%+v", r.VarBinds)
	r = e.netsnmp(2, "get", "--version", "1", oidSysName, "1.3.6.1.2.1.1.5.1")
	e.expect("SNMP v1 Get of an unknown name: noSuchName, index 2 (Net-SNMP)", r.ErrorStatus == "noSuchName" && r.FailedObject == "1.3.6.1.2.1.1.5.1", "%+v", r)
	r = e.netsnmp(2, "getnext", "1.3.6.1.2.1.11.999", "2.1")
	e.expect("SNMP v2c GetNext past the end: endOfMibView (Net-SNMP)", len(r.VarBinds) == 2 && r.VarBinds[1].Type == "endOfMibView", "%+v", r.VarBinds)

	r = e.netsnmp(2, "set", "--set", oidSysName, "s", "renamed")
	e.expect("SNMP v2c Set refused: notWritable (Net-SNMP)", r.ErrorStatus == "notWritable", "%+v", r)
	r = e.netsnmp(2, "set", "--version", "1", "--set", oidSysName, "s", "renamed")
	e.expect("SNMP v1 Set refused: noSuchName (RFC 3584) (Net-SNMP)", r.ErrorStatus == "noSuchName" || r.ErrorStatus == "readOnly", "%+v", r)
	r = e.netsnmp(2, "get", oidSysName)
	e.expect("SNMP Set changed nothing (Net-SNMP)", hexText(r.VarBinds[0].Hex) == "dut", "%+v", r.VarBinds)

	bad := e.snmpCounter(2, oidInBadComm)
	r = e.netsnmp(2, "get", "--community", "wrong", "--timeout", "1", "--retries", "0", oidSysName)
	e.expect("SNMP wrong community: no answer, snmpInBadCommunityNames counts it (Net-SNMP)",
		r.Timeout && e.snmpCounter(2, oidInBadComm) == bad+1, "%+v", r)

	// The largest OID Net-SNMP sends (128 sub-identifiers).
	// The largest OIDs Net-SNMP sends (128 sub-identifiers): one below
	// sysDescr (an object without that instance), one below no object.
	r = e.netsnmp(2, "get", "1.3.6.1.2.1.1.1"+strings.Repeat(".1", 120), "1.3.6.1.4.1.99999"+strings.Repeat(".1", 121))
	e.expect("SNMP Get of 128-sub-identifier names: noSuchInstance, noSuchObject (Net-SNMP)", len(r.VarBinds) == 2 &&
		r.VarBinds[0].Type == "noSuchInstance" && r.VarBinds[1].Type == "noSuchObject", "%+v", r)

	// GetBulk of 10000 repetitions: the agent caps them and answers what
	// fits in a message.
	r = e.netsnmp(2, "bulkget", "--max-rep", "10000", "1.3.6.1.2.1")
	e.expect("SNMP GetBulk of 10000 repetitions: a truncated answer that fits (Net-SNMP)", r.OK && len(r.VarBinds) > 5, "%d bindings, %+v", len(r.VarBinds), r.ErrorStatus)

	// A Get of 120 names whose answer exceeds the message size: tooBig.
	var names []string
	for i := 0; i < 120; i++ {
		names = append(names, oidSysDescr)
	}
	r = e.netsnmp(2, "get", names...)
	e.expect("SNMP Get whose answer does not fit: tooBig (Net-SNMP)", r.ErrorStatus == "tooBig", "%+v", r.ErrorStatus)
}

// malformedSNMP sends hostile datagrams to UDP 161 of the agent from a
// Python socket: none is an SNMP request the agent may answer.
const malformedSNMP = `
import socket, sys, random, time
host = sys.argv[1]
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
def tlv(t, v):
    n = len(v)
    if n < 128:
        return bytes([t, n]) + v
    l = n.to_bytes((n.bit_length() + 7) // 8, "big")
    return bytes([t, 0x80 | len(l)]) + l + v
def msg(version, community, pdu):
    return tlv(0x30, tlv(0x02, bytes([version])) + tlv(0x04, community) + pdu)
getsysname = tlv(0xA0, tlv(0x02, b"\x01") + tlv(0x02, b"\x00") + tlv(0x02, b"\x00") +
                 tlv(0x30, tlv(0x30, tlv(0x06, bytes([0x2b, 6, 1, 2, 1, 1, 5, 0])) + b"\x05\x00")))
sent = {"parse": 0, "version": 0}
def send(b, kind):
    # Paced below the agent's rate limit (1000/s, bursts of 200): every
    # datagram reaches the decoder.
    s.sendto(b, (host, 161))
    sent[kind] += 1
    time.sleep(0.004)
send(b"\x30\x82\xff\xff\x02\x01", "parse")                           # truncated
send(b"\x30\x84\x7f\xff\xff\xff\x02\x01\x01", "parse")               # 2 GiB length
send(msg(1, b"public", getsysname)[:-3], "parse")                    # cut short
send(msg(1, b"public", getsysname) + b"\x00\x00", "parse")           # trailing octets
send(tlv(0x30, tlv(0x02, b"\x01") + tlv(0x04, b"public") + b"\xa0\x80"), "parse")  # indefinite length
send(msg(5, b"public", getsysname), "version")                       # no such version
send(msg(3, b"public", getsysname), "version")                       # SNMPv3 header as v2c
rnd = random.Random(int(sys.argv[2]))
for _ in range(200):                                                 # seeded garbage
    send(bytes(rnd.getrandbits(8) for _ in range(rnd.randint(1, 300))), "parse")
print(sent["parse"], sent["version"])
`

// floodSNMP sends 3000 valid Gets of sysName as fast as a socket can and
// counts the answers that arrive within two seconds of the last one.
const floodSNMP = `
import socket, sys, time
host = sys.argv[1]
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 1 << 22)
def tlv(t, v):
    return bytes([t, len(v)]) + v
req = tlv(0x30, tlv(0x02, b"\x01") + tlv(0x04, b"public") + tlv(0xA0, tlv(0x02, b"\x07") + tlv(0x02, b"\x00") + tlv(0x02, b"\x00") +
      tlv(0x30, tlv(0x30, tlv(0x06, bytes([0x2b, 6, 1, 2, 1, 1, 5, 0])) + b"\x05\x00"))))
start = time.time()
for _ in range(3000):
    s.sendto(req, (host, 161))
elapsed = time.time() - start
s.settimeout(2)
answers = 0
try:
    while True:
        s.recv(2000)
        answers += 1
except socket.timeout:
    pass
print(3000, answers, round(elapsed, 3))
`

// TestNetSNMPMalformed: hostile datagrams (truncated, oversized length,
// cut short, trailing octets, indefinite length, unknown versions, 200
// seeded random datagrams) are dropped and counted in the snmp group, which
// Net-SNMP reads; the agent goes on answering.
func TestNetSNMPMalformed(t *testing.T) {
	e := newEnv(t)
	e.result.peer("Net-SNMP", image("netsnmp"), "SNMP manager (go-profinet: SNMP agent of the IO-Device)")
	startSNMPDevice(e, "dut", 2)
	parse0, ver0 := e.snmpCounter(2, oidInASNParse), e.snmpCounter(2, oidInBadVersion)
	const seed = "20261028"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := docker(ctx, "run", "--rm", "--network", e.network, "--ip", e.ip(241), "--entrypoint", "python3", image("scapy"),
		"-c", malformedSNMP, e.ip(2), seed)
	if err != nil {
		t.Fatal(err)
	}
	var nParse, nVersion int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &nParse, &nVersion); err != nil {
		t.Fatalf("sender: %v: %s", err, out)
	}
	// The counters are read after the datagrams; reading them is a
	// request itself, so they settle once the agent processed all.
	var parse, ver int
	settled := until(10*time.Second, func() bool {
		parse, ver = e.snmpCounter(2, oidInASNParse), e.snmpCounter(2, oidInBadVersion)
		return parse-parse0 >= nParse && ver-ver0 >= nVersion
	})
	// Seeded random datagrams can be well-formed by chance only with a
	// probability far below one in 200; each must be counted once.
	e.expect("SNMP malformed datagrams counted in snmpInASNParseErrs (Net-SNMP reads it)", settled && parse-parse0 == nParse,
		"sent %d, counted %d (seed %s)", nParse, parse-parse0, seed)
	e.expect("SNMP unknown versions (5, 3) counted in snmpInBadVersions (Net-SNMP reads it)", settled && ver-ver0 == nVersion,
		"sent %d, counted %d", nVersion, ver-ver0)
	r := e.netsnmp(2, "get", oidSysName)
	e.expect("SNMP agent answers after the hostile datagrams (Net-SNMP)", r.OK && hexText(r.VarBinds[0].Hex) == "dut", "%+v", r)
}

// TestNetSNMPFlood: 3000 valid requests at once. The agent answers at most
// a burst of 200 plus 1000 per second (its rate limit) and drops the rest;
// it answers Net-SNMP afterwards.
func TestNetSNMPFlood(t *testing.T) {
	e := newEnv(t)
	e.result.peer("Net-SNMP", image("netsnmp"), "SNMP manager (go-profinet: SNMP agent of the IO-Device)")
	startSNMPDevice(e, "dut", 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := docker(ctx, "run", "--rm", "--network", e.network, "--ip", e.ip(242), "--entrypoint", "python3", image("scapy"),
		"-c", floodSNMP, e.ip(2))
	if err != nil {
		t.Fatal(err)
	}
	var sent, answers int
	var elapsed float64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d %g", &sent, &answers, &elapsed); err != nil {
		t.Fatalf("flood: %v: %s", err, out)
	}
	limit := 200 + int(1000*(elapsed+0.1))
	e.expect("SNMP flood of 3000 requests: answers bounded by the rate limit", answers > 0 && answers <= limit,
		"%d answers to %d requests sent in %.3f s (limit %d)", answers, sent, elapsed, limit)
	time.Sleep(500 * time.Millisecond)
	r := e.netsnmp(2, "get", oidSysName)
	e.expect("SNMP agent answers after the flood (Net-SNMP)", r.OK && hexText(r.VarBinds[0].Hex) == "dut", "%+v", r)
}

// TestNetSNMPLLDP: lldpRemTable through Net-SNMP while LLDP neighbors come
// and go on the network: empty, one neighbor, two (the first keeps its
// lldpRemIndex), the first leaves (the second keeps its index), the first
// returns (a new index). A Get with a TimeFilter value after a row's last
// change does not find it; one before does.
func TestNetSNMPLLDP(t *testing.T) {
	e := newEnv(t)
	e.result.peer("Net-SNMP", image("netsnmp"), "SNMP manager (go-profinet: SNMP agent and LLDP of the IO-Device)")
	if !e.forwardLLDP() {
		e.result.na("SNMP lldpRemTable with changing neighbors (Net-SNMP)", "the Docker bridge does not forward LLDP and its group_fwd_mask cannot be set here")
		t.Skip("bridge does not forward LLDP")
	}
	startSNMPDevice(e, "dut", 2, "-lldp-interval", "1s")
	type row struct{ filter, port, index string }
	rows := func() map[string]row {
		out := map[string]row{}
		r := e.netsnmp(2, "walk", oidLLDPRemName)
		for _, b := range r.values() {
			idx := strings.Split(strings.TrimPrefix(b.OID, oidLLDPRemName+"."), ".")
			if len(idx) == 3 {
				out[hexText(b.Hex)] = row{idx[0], idx[1], idx[2]}
			}
		}
		return out
	}
	waitRows := func(what string, ok func(map[string]row) bool) (map[string]row, bool) {
		var r map[string]row
		found := until(40*time.Second, func() bool { r = rows(); return ok(r) })
		return r, found
	}
	has := func(names ...string) func(map[string]row) bool {
		return func(r map[string]row) bool {
			for _, n := range names {
				if _, ok := r[n]; !ok {
					return false
				}
			}
			return true
		}
	}
	r := rows()
	e.expect("SNMP lldpRemTable empty without neighbors (Net-SNMP)", len(r) == 0, "%v", r)

	nb1 := e.startOTF("nb1", 3, "device", "-i", "eth0", "-station", "nb1", "-apply-ip=false", "-lldp-interval", "1s")
	r, ok := waitRows("nb1", has("nb1"))
	e.expect("SNMP lldpRemTable shows a neighbor: filter 0, port 1, index 1 (Net-SNMP)", ok && r["nb1"] == row{"0", "1", "1"}, "%v", r)

	e.startOTF("nb2", 4, "device", "-i", "eth0", "-station", "nb2", "-apply-ip=false", "-lldp-interval", "1s")
	r, ok = waitRows("nb2", has("nb1", "nb2"))
	e.expect("SNMP lldpRemTable: a second neighbor gets index 2, the first keeps 1 (Net-SNMP)", ok && r["nb1"].index == "1" && r["nb2"].index == "2", "%v", r)

	// TimeFilter: a Get with a filter value after nb2 arrived does not
	// find its row; filter 1 (before) does.
	up := e.netsnmp(2, "get", oidSysUpTime)
	now, _ := strconv.Atoi(up.VarBinds[0].Value)
	g := e.netsnmp(2, "get", fmt.Sprintf("%s.1.1.2", oidLLDPRemName), fmt.Sprintf("%s.%d.1.2", oidLLDPRemName, now+1000))
	e.expect("SNMP lldpRemTimeMark as a TimeFilter: rows changed since t (Net-SNMP)", len(g.VarBinds) == 2 &&
		hexText(g.VarBinds[0].Hex) == "nb2" && g.VarBinds[1].Type == "noSuchInstance", "%+v", g.VarBinds)

	if _, err := docker(context.Background(), "stop", "-t", "2", nb1); err != nil {
		t.Fatal(err)
	}
	r, ok = waitRows("nb1 gone", func(r map[string]row) bool { _, in := r["nb1"]; return !in && len(r) == 1 })
	e.expect("SNMP lldpRemTable: a neighbor leaves (TTL), the other keeps its index (Net-SNMP)", ok && r["nb2"].index == "2", "%v", r)

	e.startOTF("nb1b", 5, "device", "-i", "eth0", "-station", "nb1", "-apply-ip=false", "-lldp-interval", "1s")
	r, ok = waitRows("nb1 back", has("nb1", "nb2"))
	e.expect("SNMP lldpRemTable: a returning neighbor gets a new index (Net-SNMP)", ok && r["nb1"].index == "3" && r["nb2"].index == "2", "%v", r)
}
