// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/lldp"
	"github.com/otfabric/go-profinet/snmp"
)

// snmpGet sends one request from a socket of nd to the device's agent.
func snmpGet(t *testing.T, conn net.PacketConn, typ snmp.PDUType, names ...snmp.OID) snmp.Message {
	t.Helper()
	m := snmp.Message{Version: snmp.Version2c, Community: []byte("public"), PDU: snmp.PDU{Type: typ, RequestID: 9}}
	for _, n := range names {
		m.PDU.VarBinds = append(m.PDU.VarBinds, snmp.VarBind{Name: n, Value: snmp.Null()})
	}
	b, err := m.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	dst := &net.UDPAddr{IP: net.ParseIP(devIP).To4(), Port: snmp.Port}
	buf := make([]byte, 2000)
	for range 5 {
		if _, err := conn.WriteTo(b, dst); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		r, err := snmp.Decode(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	t.Fatal("no SNMP response")
	return snmp.Message{}
}

func snmpWalk(t *testing.T, conn net.PacketConn) map[string]snmp.Value {
	t.Helper()
	out := map[string]snmp.Value{}
	name := snmp.OID{0, 0}
	for range 500 {
		r := snmpGet(t, conn, snmp.PDUGetNextRequest, name)
		vb := r.PDU.VarBinds[0]
		if vb.Value.Type == snmp.TypeEndOfMibView {
			return out
		}
		out[vb.Name.String()] = vb.Value
		name = vb.Name
	}
	t.Fatal("walk does not end")
	return nil
}

func TestSNMP(t *testing.T) {
	n := newNet(t)
	d, nd := n.device(nil, func(c *Config) {
		c.SNMP = &SNMPConfig{Community: "public", Contact: "ops@example.com", Location: "cell 4"}
	})
	// A neighbor that sends LLDP.
	cn := n.node("192.168.0.2")
	ag, err := lldp.NewAgent(cn.mux, lldp.AgentOptions{Interval: 50 * time.Millisecond, Local: func() lldp.LocalInfo {
		return lldp.LocalInfo{StationName: "plc", PortName: "port-002", Description: "test PLC"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	conn, err := cn.stack.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	eventually(t, 5*time.Second, "LLDP neighbor", func() bool { return len(d.ports[0].lldpA.Neighbors()) == 1 })
	vals := snmpWalk(t, conn)
	mac := nd.mux.HardwareAddr()
	for oid, want := range map[string]string{
		"1.3.6.1.2.1.1.1.0":               `OCTET STRING: "go-profinet device test"`,
		"1.3.6.1.2.1.1.2.0":               "OID: 0.0",
		"1.3.6.1.2.1.1.4.0":               `OCTET STRING: "ops@example.com"`,
		"1.3.6.1.2.1.1.5.0":               `OCTET STRING: "dut"`,
		"1.3.6.1.2.1.1.6.0":               `OCTET STRING: "cell 4"`,
		"1.3.6.1.2.1.1.7.0":               "INTEGER: 72",
		"1.3.6.1.2.1.2.1.0":               "INTEGER: 1",
		"1.3.6.1.2.1.2.2.1.2.1":           `OCTET STRING: "port-001"`,
		"1.3.6.1.2.1.2.2.1.5.1":           "Gauge32: 100000000",
		"1.3.6.1.2.1.2.2.1.6.1":           snmp.OctetString(mac[:]).String(),
		"1.3.6.1.2.1.2.2.1.8.1":           "INTEGER: 1",
		"1.0.8802.1.1.2.1.3.1.0":          "INTEGER: 7",
		"1.0.8802.1.1.2.1.3.2.0":          `OCTET STRING: "dut"`,
		"1.0.8802.1.1.2.1.3.3.0":          `OCTET STRING: "dut"`,
		"1.0.8802.1.1.2.1.3.4.0":          `OCTET STRING: "go-profinet device test"`,
		"1.0.8802.1.1.2.1.3.7.1.3.1":      `OCTET STRING: "port-001.dut"`,
		"1.0.8802.1.1.2.1.4.1.1.5.0.1.1":  `OCTET STRING: "plc"`,
		"1.0.8802.1.1.2.1.4.1.1.7.0.1.1":  `OCTET STRING: "port-002.plc"`,
		"1.0.8802.1.1.2.1.4.1.1.9.0.1.1":  `OCTET STRING: "plc"`,
		"1.0.8802.1.1.2.1.4.1.1.10.0.1.1": `OCTET STRING: "test PLC"`,
	} {
		if v, ok := vals[oid]; !ok || v.String() != want {
			t.Errorf("%s: %v, want %s", oid, v, want)
		}
	}
	if _, ok := vals["1.3.6.1.2.1.1.3.0"]; !ok {
		t.Error("no sysUpTime")
	}
	// sysName follows a DCP Set of the station name; ifOperStatus the
	// link.
	station{d}.SetName("renamed", false)
	r := snmpGet(t, conn, snmp.PDUGetRequest, snmp.MustParseOID("1.3.6.1.2.1.1.5.0"))
	if string(r.PDU.VarBinds[0].Value.Bytes) != "renamed" {
		t.Fatalf("sysName %v", r.PDU.VarBinds[0].Value)
	}
	nd.ep.SetLinkUp(false)
	if d.snmpInterfaces()[0].OperStatus != snmp.IfStatusDown {
		t.Fatal("ifOperStatus with the link down")
	}
}

func TestSNMPConfig(t *testing.T) {
	n := newNet(t)
	nd := n.node(devIP)
	if _, err := New(Config{Model: testModel(), Mux: nd.mux, ListenPacket: nd.stack.ListenPacket, SNMP: &SNMPConfig{}}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("no community: %v", err)
	}
	// The port is taken.
	busy, err := nd.stack.ListenPacket(context.Background(), "udp4", ":1161")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	if _, err := New(Config{Model: testModel(), Mux: nd.mux, ListenPacket: nd.stack.ListenPacket, SNMP: &SNMPConfig{Community: "c", Port: 1161}}); err == nil {
		t.Fatal("port taken")
	}
	// Without LLDP there is no LLDP-MIB; a description overrides the
	// type of station.
	d, err := New(Config{Model: testModel(), Mux: nd.mux, ListenPacket: nd.stack.ListenPacket, DisableLLDP: true, StationName: "dut", IP: suite(devIP),
		SNMP: &SNMPConfig{Community: "public", Description: "custom", ObjectID: snmp.OID{1, 3, 6, 1, 4, 1, 99}, Rate: -1}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	cn := n.node("192.168.0.2")
	conn, err := cn.stack.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	vals := snmpWalk(t, conn)
	if vals["1.3.6.1.2.1.1.1.0"].String() != `OCTET STRING: "custom"` || vals["1.3.6.1.2.1.1.2.0"].String() != "OID: 1.3.6.1.4.1.99" {
		t.Fatalf("%v", vals)
	}
	for oid := range vals {
		if oid[:3] == "1.0" {
			t.Fatalf("LLDP-MIB without LLDP: %s", oid)
		}
	}
}
