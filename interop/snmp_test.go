// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/netstack"
	"github.com/otfabric/go-profinet/pcap"
	"github.com/otfabric/go-profinet/snmp"
	"github.com/otfabric/go-profinet/vnet"
)

// scapySNMP decodes every datagram to or from UDP port 161 of a capture
// with scapy's SNMP layer (scapy.layers.snmp) and reports the frames it
// could not decode completely: no SNMP layer, undecoded octets (Raw) left
// in it, or variable bindings that do not decode.
const scapySNMP = `
import json, sys
from scapy.all import rdpcap, UDP, Raw
from scapy.layers.snmp import SNMP
n, bad, pdus = 0, [], {}
for i, p in enumerate(rdpcap(sys.argv[1]), 1):
    if UDP not in p or 161 not in (p[UDP].sport, p[UDP].dport):
        continue
    n += 1
    try:
        s = p[SNMP]
        if Raw in s:
            raise ValueError("undecoded octets")
        name = s.PDU.__class__.__name__
        pdus[name] = pdus.get(name, 0) + 1
        for v in s.PDU.varbindlist:
            v.oid.val, repr(v.value)
    except Exception as e:
        bad.append({"frame": i, "error": str(e)})
print(json.dumps({"snmp": n, "pdus": pdus, "undecoded": bad}))
`

// snmpWalkCapture runs a device with SNMP on a virtual network, walks it
// with v1 GetNext, v2c GetNext and GetBulk, sends a Get with names that
// do not exist and a Set, and writes the IPv4 and ARP frames to a capture.
// It returns the number of SNMP datagrams.
func snmpWalkCapture(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := pcap.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	nw := vnet.New(1)
	nw.AddTap(func(_ *vnet.Endpoint, frame []byte) {
		if len(frame) < 14 {
			return
		}
		if et := binary.BigEndian.Uint16(frame[12:]); et != 0x0800 && et != 0x0806 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if err := w.WriteFrame(time.Now(), frame); err != nil {
			t.Error(err)
		}
	})
	mask := netip.MustParseAddr("255.255.255.0")
	node := func(last byte, ip string) (*l2.Mux, *netstack.Stack) {
		ep, err := nw.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{2, 0, 0, 0, 0, last}})
		if err != nil {
			t.Fatal(err)
		}
		mux := l2.NewMux(ep)
		st, err := netstack.New(mux, netstack.Config{Addr: netip.MustParseAddr(ip), Mask: mask})
		if err != nil {
			t.Fatal(err)
		}
		return mux, st
	}
	dmux, dst := node(10, "192.168.0.10")
	m := &model.Device{
		Identity:     model.Identity{VendorID: 0xF000, DeviceID: 0x0001, VendorName: "OT Fabric", TypeOfStation: "go-profinet SNMP interop", OrderID: "OTF-SNMP"},
		Capabilities: model.Capabilities{MaxARs: 1, MinDeviceInterval: 32, Ports: 1},
		DAP:          model.StandardDAP("DAP", 0x100, 1),
	}
	dev, err := device.New(device.Config{Model: m, Mux: dmux, StationName: "otf-snmp", ListenPacket: dst.ListenPacket, NoResponseDelay: true,
		IP:   dcp.IPSuite{Addr: netip.MustParseAddr("192.168.0.10"), Mask: mask, Gateway: netip.MustParseAddr("0.0.0.0")},
		SNMP: &device.SNMPConfig{Community: "public", Contact: "interop", Location: "docker"}})
	if err != nil {
		t.Fatal(err)
	}
	_, cst := node(20, "192.168.0.20")
	conn, err := cst.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	to := &net.UDPAddr{IP: net.IPv4(192, 168, 0, 10), Port: snmp.Port}
	datagrams := 0
	var id int32
	do := func(m snmp.Message) snmp.Message {
		id++
		m.Community, m.PDU.RequestID = []byte("public"), id
		b, err := m.AppendTo(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.WriteTo(b, to); err != nil {
			t.Fatal(err)
		}
		datagrams++
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 2000)
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			t.Fatalf("request %d (%s %s): %v", id, m.Version, m.PDU.Type, err)
		}
		datagrams++
		r, err := snmp.Decode(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	vb := func(names ...snmp.OID) []snmp.VarBind {
		var out []snmp.VarBind
		for _, n := range names {
			out = append(out, snmp.VarBind{Name: n, Value: snmp.Null()})
		}
		return out
	}
	walk := func(v snmp.Version, typ snmp.PDUType) {
		name := snmp.OID{1, 0}
		for range 500 {
			r := do(snmp.Message{Version: v, PDU: snmp.PDU{Type: typ, MaxRepetitions: 8, VarBinds: vb(name)}})
			if r.PDU.ErrorStatus != snmp.NoError || len(r.PDU.VarBinds) == 0 {
				return
			}
			last := r.PDU.VarBinds[len(r.PDU.VarBinds)-1]
			if last.Value.Type == snmp.TypeEndOfMibView {
				return
			}
			name = last.Name
		}
		t.Fatal("walk does not end")
	}
	walk(snmp.Version1, snmp.PDUGetNextRequest)
	walk(snmp.Version2c, snmp.PDUGetNextRequest)
	walk(snmp.Version2c, snmp.PDUGetBulkRequest)
	missing := []snmp.OID{snmp.MustParseOID("1.3.6.1.2.1.1.5.0"), snmp.MustParseOID("1.3.6.1.2.1.1.5.1"), snmp.MustParseOID("1.3.6.1.4.1.99.1.0")}
	if r := do(snmp.Message{Version: snmp.Version2c, PDU: snmp.PDU{Type: snmp.PDUGetRequest, VarBinds: vb(missing...)}}); r.PDU.VarBinds[2].Value.Type != snmp.TypeNoSuchObject {
		t.Fatalf("Get: %+v", r.PDU)
	}
	do(snmp.Message{Version: snmp.Version1, PDU: snmp.PDU{Type: snmp.PDUGetRequest, VarBinds: vb(missing...)}})
	set := []snmp.VarBind{{Name: missing[0], Value: snmp.String("new")}}
	do(snmp.Message{Version: snmp.Version2c, PDU: snmp.PDU{Type: snmp.PDUSetRequest, VarBinds: set}})
	do(snmp.Message{Version: snmp.Version1, PDU: snmp.PDU{Type: snmp.PDUSetRequest, VarBinds: set}})
	_ = conn.Close()
	_ = dev.Close()
	_ = nw.Close()
	mu.Lock()
	defer mu.Unlock()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return datagrams
}

// TestSNMPDecoders: a walk of go-profinet's SNMP agent (v1 and v2c,
// GetNext, GetBulk, Get with exceptions, refused Sets) is decoded by
// tshark and by scapy's SNMP layer, independent of go-profinet, without a
// malformed frame, an expert warning or an undecoded datagram.
func TestSNMPDecoders(t *testing.T) {
	e := newEnv(t)
	e.result.peer("tshark/scapy", image("tshark"), "decoder")
	n := snmpWalkCapture(t, filepath.Join(e.share, "snmp.pcap"))
	e.expect("SNMP walk of the device's agent captured (go-profinet)", n > 100, "%d SNMP datagrams", n)
	e.oracles("snmp.pcap")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// tshark: every SNMP datagram is dissected as SNMP with its PDU.
	out, err := docker(ctx, "run", "--rm", "-v", e.share+":/share", "--entrypoint", "tshark", image("tshark"),
		"-n", "-r", "/share/snmp.pcap", "-Y", "snmp", "-T", "fields", "-e", "frame.number", "-e", "snmp.data")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	e.expect("tshark dissects every SNMP datagram as SNMP", len(lines) == n, "%d of %d datagrams", len(lines), n)

	// scapy's SNMP layer.
	out, err = docker(ctx, "run", "--rm", "-v", e.share+":/share", "--entrypoint", "python3", image("scapy"), "-c", scapySNMP, "/share/snmp.pcap")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		SNMP      int            `json:"snmp"`
		PDUs      map[string]int `json:"pdus"`
		Undecoded []any          `json:"undecoded"`
	}
	if err := json.Unmarshal(lastLine(out), &s); err != nil {
		t.Fatalf("scapy output: %v: %s", err, out)
	}
	e.expect("scapy decodes every SNMP datagram (PDU and variable bindings)", s.SNMP == n && len(s.Undecoded) == 0,
		"%d of %d datagrams, PDUs %v, undecoded %v", s.SNMP, n, s.PDUs, s.Undecoded)
}
