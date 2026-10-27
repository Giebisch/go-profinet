// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/snmp"
)

// snmpClient sends requests built with package snmp's encoder from a
// netstack socket and decodes the responses.
type snmpClient struct {
	t    *testing.T
	conn net.PacketConn
	dst  *net.UDPAddr
	id   int32
}

func (c *snmpClient) do(m snmp.Message) snmp.Message {
	c.t.Helper()
	c.id++
	m.Version, m.Community, m.PDU.RequestID = snmp.Version2c, []byte("public"), c.id
	b, err := m.AppendTo(nil)
	if err != nil {
		c.t.Fatal(err)
	}
	buf := make([]byte, 2000)
	// UDP: retry a lost datagram, ignore a late response to an earlier
	// try.
	for range 5 {
		if _, err := c.conn.WriteTo(b, c.dst); err != nil {
			c.t.Fatal(err)
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			n, _, err := c.conn.ReadFrom(buf)
			if err != nil {
				break
			}
			r, err := snmp.Decode(buf[:n])
			if err != nil {
				c.t.Fatalf("response: %v", err)
			}
			if r.PDU.RequestID == c.id {
				return r
			}
		}
	}
	c.t.Fatal("no SNMP response")
	return snmp.Message{}
}

func (c *snmpClient) walkNext() []snmp.VarBind {
	c.t.Helper()
	var out []snmp.VarBind
	name := snmp.OID{0, 0}
	for range 1000 {
		r := c.do(snmp.Message{PDU: snmp.PDU{Type: snmp.PDUGetNextRequest, VarBinds: []snmp.VarBind{{Name: name, Value: snmp.Null()}}}})
		vb := r.PDU.VarBinds[0]
		if vb.Value.Type == snmp.TypeEndOfMibView {
			return out
		}
		out = append(out, vb)
		name = vb.Name
	}
	c.t.Fatal("GetNext walk does not end")
	return nil
}

func (c *snmpClient) walkBulk() []snmp.VarBind {
	c.t.Helper()
	var out []snmp.VarBind
	name := snmp.OID{0, 0}
	for range 1000 {
		r := c.do(snmp.Message{PDU: snmp.PDU{Type: snmp.PDUGetBulkRequest, MaxRepetitions: 10, VarBinds: []snmp.VarBind{{Name: name, Value: snmp.Null()}}}})
		for _, vb := range r.PDU.VarBinds {
			if vb.Value.Type == snmp.TypeEndOfMibView {
				return out
			}
			out = append(out, vb)
			name = vb.Name
		}
	}
	c.t.Fatal("GetBulk walk does not end")
	return nil
}

// TestSNMPWalk: a device with SNMP on the virtual network, connected to a
// controller and next to another device, is walked with GetNext and with
// GetBulk; both walks agree and show the system group, the interface and
// the LLDP-MIB with the other device as a neighbor.
func TestSNMPWalk(t *testing.T) {
	e := newEnv(t, 7)
	n := e.node("192.168.0.10")
	dev, err := device.New(device.Config{
		Model: testModel(), Mux: n.mux, StationName: "dut", IP: suite("192.168.0.10"), ListenPacket: n.stack.ListenPacket,
		NoResponseDelay: true, LLDPInterval: 200 * time.Millisecond, Logger: e.log.logger("dut"),
		SNMP: &device.SNMPConfig{Community: "public", Contact: "ops", Location: "line 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close() })
	e.device("neighbor", "192.168.0.11", testModel(), nil)
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel()})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)

	cn := e.node("192.168.0.20")
	conn, err := cn.stack.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &snmpClient{t: t, conn: conn, dst: &net.UDPAddr{IP: net.IPv4(192, 168, 0, 10), Port: snmp.Port}}

	remName := snmp.MustParseOID("1.0.8802.1.1.2.1.4.1.1.9")
	hasNeighbor := func(vbs []snmp.VarBind) bool {
		for _, vb := range vbs {
			if vb.Name.HasPrefix(remName) && string(vb.Value.Bytes) == "neighbor" {
				return true
			}
		}
		return false
	}
	var next []snmp.VarBind
	eventually(t, 5*time.Second, "the neighbor in lldpRemTable", func() bool {
		next = c.walkNext()
		return hasNeighbor(next)
	})
	bulk := c.walkBulk()
	got := map[string]string{}
	for _, vb := range next {
		got[vb.Name.String()] = vb.Value.String()
	}
	// The walks agree except for the values that change between them.
	volatile := map[string]bool{"1.3.6.1.2.1.1.3.0": true}
	for _, vb := range bulk {
		if v, ok := got[vb.Name.String()]; !ok || (v != vb.Value.String() && !volatile[vb.Name.String()] && !vb.Name.HasPrefix(snmp.OIDSNMP)) {
			t.Errorf("GetBulk %s = %v, GetNext %v", vb.Name, vb.Value, v)
		}
	}
	if len(bulk) != len(next) {
		t.Fatalf("GetBulk %d instances, GetNext %d", len(bulk), len(next))
	}
	for oid, want := range map[string]string{
		"1.3.6.1.2.1.1.1.0":      `OCTET STRING: "go-profinet e2e"`,
		"1.3.6.1.2.1.1.4.0":      `OCTET STRING: "ops"`,
		"1.3.6.1.2.1.1.5.0":      `OCTET STRING: "dut"`,
		"1.3.6.1.2.1.1.6.0":      `OCTET STRING: "line 1"`,
		"1.3.6.1.2.1.2.1.0":      "INTEGER: 1",
		"1.3.6.1.2.1.2.2.1.8.1":  "INTEGER: 1",
		"1.0.8802.1.1.2.1.3.2.0": `OCTET STRING: "dut"`,
	} {
		if got[oid] != want {
			t.Errorf("%s: %s, want %s", oid, got[oid], want)
		}
	}
	// A Set is refused; the AR keeps running.
	r := c.do(snmp.Message{PDU: snmp.PDU{Type: snmp.PDUSetRequest, VarBinds: []snmp.VarBind{{Name: snmp.MustParseOID("1.3.6.1.2.1.1.5.0"), Value: snmp.String("x")}}}})
	if r.PDU.ErrorStatus != snmp.NotWritable || r.PDU.ErrorIndex != 1 {
		t.Fatalf("Set: %+v", r.PDU)
	}
	if st, _ := cd.State(); st != controller.StateRunning {
		t.Fatalf("controller %s", st)
	}
	if dev.StationName() != "dut" {
		t.Fatal("sysName changed")
	}
}

// TestSNMPLLDPIndexStable: the rows of lldpRemTable keep their
// lldpRemIndex while neighbors come and go: a neighbor that appears gets
// the next index, the others keep theirs, a neighbor that left and
// returns gets a new one. Each row's time mark is its arrival in
// sysUpTime units, and a Get with that TimeFilter value finds it.
func TestSNMPLLDPIndexStable(t *testing.T) {
	e := newEnv(t, 8)
	n := e.node("192.168.0.10")
	dev, err := device.New(device.Config{
		Model: testModel(), Mux: n.mux, StationName: "dut", IP: suite("192.168.0.10"), ListenPacket: n.stack.ListenPacket,
		NoResponseDelay: true, LLDPInterval: 200 * time.Millisecond, Logger: e.log.logger("dut"),
		SNMP: &device.SNMPConfig{Community: "public"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close() })
	cn := e.node("192.168.0.20")
	conn, err := cn.stack.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &snmpClient{t: t, conn: conn, dst: &net.UDPAddr{IP: net.IPv4(192, 168, 0, 10), Port: snmp.Port}}
	remName := snmp.MustParseOID("1.0.8802.1.1.2.1.4.1.1.9")
	type row struct{ filter, port, index uint32 }
	// rows walks lldpRemSysName: the walk's instances (filter 0).
	rows := func() map[string]row {
		out := map[string]row{}
		for _, vb := range c.walkNext() {
			if vb.Name.HasPrefix(remName) && len(vb.Name) == len(remName)+3 {
				out[string(vb.Value.Bytes)] = row{vb.Name[len(remName)], vb.Name[len(remName)+1], vb.Name[len(remName)+2]}
			}
		}
		return out
	}
	waitRows := func(what string, ok func(map[string]row) bool) map[string]row {
		t.Helper()
		var r map[string]row
		eventually(t, 10*time.Second, what, func() bool { r = rows(); return ok(r) })
		return r
	}
	// get reads lldpRemSysName with a TimeFilter value.
	get := func(filter, port, index uint32) snmp.Value {
		r := c.do(snmp.Message{PDU: snmp.PDU{Type: snmp.PDUGetRequest, VarBinds: []snmp.VarBind{{Name: remName.Append(filter, port, index), Value: snmp.Null()}}}})
		return r.PDU.VarBinds[0].Value
	}
	uptime := func() uint32 {
		r := c.do(snmp.Message{PDU: snmp.PDU{Type: snmp.PDUGetRequest, VarBinds: []snmp.VarBind{{Name: snmp.MustParseOID("1.3.6.1.2.1.1.3.0"), Value: snmp.Null()}}}})
		return uint32(r.PDU.VarBinds[0].Value.Uint)
	}

	time.Sleep(300 * time.Millisecond)
	beforeA := uptime()
	a, _ := e.device("na", "192.168.0.11", testModel(), nil)
	r := waitRows("neighbor na", func(r map[string]row) bool { _, ok := r["na"]; return ok })
	if r["na"].filter != 0 || r["na"].port != 1 || r["na"].index != 1 {
		t.Fatalf("na %+v", r["na"])
	}
	if v := get(beforeA, 1, 1); string(v.Bytes) != "na" {
		t.Fatalf("Get with the filter before na arrived: %+v", v)
	}
	if v := get(uptime()+100, 1, 1); v.Type != snmp.TypeNoSuchInstance {
		t.Fatalf("Get with a filter after na arrived: %+v", v)
	}

	e.device("nb", "192.168.0.12", testModel(), nil)
	r = waitRows("neighbor nb", func(r map[string]row) bool { _, ok := r["nb"]; return ok })
	if r["na"].index != 1 || r["nb"].index != 2 {
		t.Fatalf("after nb: %+v", r)
	}
	// na leaves: nb keeps its index.
	_ = a.Close()
	r = waitRows("na gone", func(r map[string]row) bool { _, ok := r["na"]; return !ok })
	if r["nb"].index != 2 {
		t.Fatalf("after na left: %+v", r)
	}
	// na returns: a new index.
	e.device("na", "192.168.0.13", testModel(), nil)
	r = waitRows("na back", func(r map[string]row) bool { _, ok := r["na"]; return ok })
	if r["na"].index != 3 || r["nb"].index != 2 {
		t.Fatalf("after na returned: %+v", r)
	}
}
