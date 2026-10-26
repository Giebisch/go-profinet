// SPDX-License-Identifier: MIT

package lldp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/vnet"
)

func fullFrame() Frame {
	cm := ethernet.MAC{2, 0, 0, 0, 0, 1}
	return Frame{
		ChassisSubtype:    ChassisSubtypeLocal,
		ChassisID:         []byte("plc-1"),
		PortSubtype:       PortSubtypeLocal,
		PortID:            []byte("port-001.plc-1"),
		TTL:               20,
		PortDescription:   "Port 1",
		SystemName:        "plc-1",
		SystemDescription: "go-profinet test device",
		ManagementAddr:    netip.MustParseAddr("192.168.0.10"),
		PortStatus:        &PortStatus{RTClass2: 1, RTClass3: 2},
		ChassisMAC:        &cm,
		Delay:             &Delay{RxLocal: 1, RxRemote: 2, TxLocal: 3, TxRemote: 4, Cable: 5},
		MACPHY:            &MACPHY{Autoneg: 3, PMDCapability: 0x6C00, MAUType: 16},
		Other: []TLV{
			{Type: TypeCapabilities, Value: []byte{0, 0x80, 0, 0x80}},
			{Type: TypeOrgSpecific, Value: []byte{0x00, 0x0E, 0xCF, PNSubtypeMRPPortStatus, 1, 2}},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	f := fullFrame()
	b, err := f.AppendTo([]byte{0xEE})
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 0xEE {
		t.Fatal("prefix lost")
	}
	g, err := Decode(b[1:])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f, g) {
		t.Fatalf("round trip\n got %+v\nwant %+v", g, f)
	}
	// Re-encoding gives the same octets.
	b2, err := g.AppendTo(nil)
	if err != nil || !bytes.Equal(b[1:], b2) {
		t.Fatalf("re-encode %v", err)
	}
	// A truncated LLDPDU decodes only when it ends at a TLV boundary
	// after the mandatory TLVs (an LLDPDU without its End TLV), possibly
	// followed by one octet of padding; every other cut fails.
	var bounds []int
	for p := 0; p < len(b2); {
		p += 2 + int(binary.BigEndian.Uint16(b2[p:])&0x1FF)
		bounds = append(bounds, p)
	}
	for n := 0; n < len(b2); n++ {
		p, h := 0, 0
		for i, q := range bounds {
			if q <= n {
				p, h = q, i+1
			}
		}
		want := h >= 3 && n-p < 2
		if _, err := Decode(b2[:n]); (err == nil) != want {
			t.Fatalf("truncated to %d octets: err %v, want success %v", n, err, want)
		}
	}
}

func TestMinimalFrame(t *testing.T) {
	f := Frame{ChassisSubtype: ChassisSubtypeMAC, ChassisID: []byte{2, 0, 0, 0, 0, 9}, PortSubtype: PortSubtypeMAC, PortID: []byte{2, 0, 0, 0, 0, 8}, TTL: 1}
	b, err := f.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Chassis, port, TTL and End: no optional TLVs.
	if len(b) != 9+9+4+2 {
		t.Fatalf("length %d", len(b))
	}
	g, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if g.ChassisString() != "02:00:00:00:00:09" || g.PortString() != "02:00:00:00:00:08" {
		t.Fatalf("ids %q %q", g.ChassisString(), g.PortString())
	}
	// Without the End TLV, and with padding that is too short for a TLV.
	if g2, err := Decode(append(b[:len(b)-2:len(b)-2], 0)); err != nil || g2.TTL != 1 {
		t.Fatalf("no End: %+v %v", g2, err)
	}
	// A MAC subtype with an ID that is not 6 octets is shown as text.
	g.ChassisID = []byte("abc")
	if g.ChassisString() != "abc" {
		t.Fatal(g.ChassisString())
	}
	g.PortSubtype = PortSubtypeLocal
	g.PortID = []byte{2, 0, 0, 0, 0, 8}
	if g.PortString() != string(g.PortID) {
		t.Fatal(g.PortString())
	}
}

func tlv(t uint8, v ...byte) []byte { return appendTLV(nil, t, v) }

func cat(bs ...[]byte) []byte { return bytes.Join(bs, nil) }

func TestDecodeErrors(t *testing.T) {
	ch := tlv(TypeChassisID, 7, 'a')
	po := tlv(TypePortID, 7, 'p')
	ttl := tlv(TypeTTL, 0, 5)
	end := tlv(TypeEnd)
	cases := map[string][]byte{
		"empty":           nil,
		"End first":       end,
		"port first":      cat(po, ch, ttl, end),
		"TTL second":      cat(ch, ttl, po, end),
		"system name 3rd": cat(ch, po, tlv(TypeSystemName, 'x'), end),
		"short chassis":   cat(tlv(TypeChassisID, 7), po, ttl, end),
		"short port":      cat(ch, tlv(TypePortID, 7), ttl, end),
		"short TTL":       cat(ch, po, tlv(TypeTTL, 1), end),
		"TLV too long":    cat(ch, po, ttl, []byte{0x0A, 0x10, 'x'}),
		"no TTL":          cat(ch, po),
		"no port":         ch,
	}
	for name, b := range cases {
		if _, err := Decode(b); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	// Unknown and incomplete organizational TLVs are kept as Other; a
	// management address that is not IPv4 is ignored.
	odd := [][]byte{
		tlv(TypeOrgSpecific, 0x00, 0x0E),                                // shorter than OUI + subtype
		tlv(TypeOrgSpecific, 0x00, 0x0E, 0xCF, PNSubtypePortStatus, 1),  // short PortStatus
		tlv(TypeOrgSpecific, 0x00, 0x0E, 0xCF, PNSubtypeChassisMAC, 1),  // short chassis MAC
		tlv(TypeOrgSpecific, 0x00, 0x0E, 0xCF, PNSubtypeDelay, 1, 2, 3), // short delay
		tlv(TypeOrgSpecific, 0x00, 0x12, 0x0F, 1, 1),                    // short MAC/PHY
		tlv(TypeOrgSpecific, 0x00, 0x12, 0x0F, 2, 1, 2, 3, 4, 5),        // other 802.3 subtype
		tlv(TypeOrgSpecific, 0x00, 0x80, 0xC2, 1, 0, 1),                 // 802.1
		tlv(TypeManagementAddress, 17, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1),
		tlv(TypeManagementAddress, 5, 1, 1),
	}
	f, err := Decode(cat(ch, po, ttl, cat(odd...), end))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Other) != 7 || f.PortStatus != nil || f.ChassisMAC != nil || f.Delay != nil || f.MACPHY != nil || f.ManagementAddr.IsValid() {
		t.Fatalf("frame %+v", f)
	}
	// TLVs after the End TLV are ignored.
	f, err = Decode(cat(ch, po, ttl, end, tlv(TypeSystemName, 'x')))
	if err != nil || f.SystemName != "" {
		t.Fatalf("after End: %+v %v", f, err)
	}
}

func TestAppendToErrors(t *testing.T) {
	ok := Frame{ChassisID: []byte("c"), PortID: []byte("p")}
	bad := []Frame{
		{PortID: []byte("p")},
		{ChassisID: []byte("c")},
		{ChassisID: bytes.Repeat([]byte("c"), 256), PortID: []byte("p")},
		{ChassisID: []byte("c"), PortID: bytes.Repeat([]byte("p"), 256)},
	}
	for i, f := range bad {
		if out, err := f.AppendTo([]byte{1}); !errors.Is(err, profinet.ErrInvalidConfig) || len(out) != 1 {
			t.Errorf("%d: err %v, %d octets", i, err, len(out))
		}
	}
	for _, o := range []TLV{
		{Type: TypeCapabilities, Value: make([]byte, 512)},
		{Type: TypeEnd},
		{Type: 128, Value: []byte{1}},
	} {
		f := ok
		f.Other = []TLV{o}
		// The output is cut back to what dst held.
		if out, err := f.AppendTo([]byte{1}); !errors.Is(err, profinet.ErrInvalidConfig) || len(out) != 1 {
			t.Errorf("TLV type %d of %d octets: err %v, %d octets", o.Type, len(o.Value), err, len(out))
		}
	}
	// 255-octet IDs and a 511-octet TLV fit; long strings are cut to 255.
	f := Frame{ChassisID: bytes.Repeat([]byte("c"), 255), PortID: bytes.Repeat([]byte("p"), 255), SystemName: strings.Repeat("s", 300),
		Other: []TLV{{Type: TypeCapabilities, Value: make([]byte, 511)}}}
	b, err := f.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Decode(b)
	if err != nil || len(g.SystemName) != 255 || len(g.ChassisID) != 255 || len(g.Other[0].Value) != 511 {
		t.Fatalf("decode %v", err)
	}
	// A management address that is not IPv4 is not sent.
	f = ok
	f.ManagementAddr = netip.MustParseAddr("::1")
	b, _ = f.AppendTo(nil)
	if g, _ := Decode(b); g.ManagementAddr.IsValid() {
		t.Fatal("IPv6 management address sent")
	}
}

func TestBuild(t *testing.T) {
	mac := ethernet.MAC{2, 0, 0, 0, 0, 5}
	f := LocalInfo{}.Build(mac, 20)
	if f.ChassisSubtype != ChassisSubtypeMAC || !bytes.Equal(f.ChassisID, mac[:]) || string(f.PortID) != "port-001" || f.SystemName != "" || *f.ChassisMAC != mac || f.TTL != 20 {
		t.Fatalf("no station name %+v", f)
	}
	n := Neighbor{Frame: f}
	if got := n.Alias(); got != "port-001.02:00:00:00:00:05" {
		t.Fatalf("alias %q", got)
	}
	f = LocalInfo{StationName: "dev", PortName: "port-002", Description: "d", IP: netip.MustParseAddr("10.0.0.1")}.Build(mac, 20)
	if f.ChassisSubtype != ChassisSubtypeLocal || string(f.ChassisID) != "dev" || string(f.PortID) != "port-002.dev" || f.SystemName != "dev" || f.SystemDescription != "d" || f.ManagementAddr != netip.MustParseAddr("10.0.0.1") {
		t.Fatalf("standard %+v", f)
	}
	n = Neighbor{Frame: f}
	if got := n.Alias(); got != "port-002.dev" {
		t.Fatalf("alias %q", got)
	}
	f = LocalInfo{StationName: "dev", Legacy: true}.Build(mac, 20)
	if string(f.PortID) != "port-001" {
		t.Fatalf("legacy %q", f.PortID)
	}
	n = Neighbor{Frame: f}
	if got := n.Alias(); got != "port-001.dev" {
		t.Fatalf("alias %q", got)
	}
}

type recLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recLogger) Debugf(f string, a ...any) {
	l.mu.Lock()
	l.msgs = append(l.msgs, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}
func (l *recLogger) Infof(string, ...any)  {}
func (l *recLogger) Warnf(string, ...any)  {}
func (l *recLogger) Errorf(string, ...any) {}

func (l *recLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.msgs)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type node struct {
	ep  *vnet.Endpoint
	mux *l2.Mux
}

func newNode(t *testing.T, n *vnet.Network, last byte) node {
	t.Helper()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{2, 0, 0, 0, 0, last}, FilterMulticast: true})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(ep)
	t.Cleanup(func() { _ = m.Close() })
	return node{ep: ep, mux: m}
}

func newAgent(t *testing.T, nd node, opts AgentOptions) *Agent {
	t.Helper()
	a, err := NewAgent(nd.mux, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func (nd node) sendLLDP(t *testing.T, f Frame) {
	t.Helper()
	b, err := f.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	nd.sendRaw(t, b)
}

func (nd node) sendRaw(t *testing.T, payload []byte) {
	t.Helper()
	h := ethernet.Frame{Dst: ethernet.LLDPMulticast, Src: nd.mux.HardwareAddr(), EtherType: ethernet.EtherTypeLLDP}
	if err := nd.mux.Send(context.Background(), ethernet.Pad(append(h.AppendHeader(nil), payload...), 0)); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsExchange(t *testing.T) {
	n := vnet.New(3)
	defer func() { _ = n.Close() }()
	na, nb := newNode(t, n, 0xA), newNode(t, n, 0xB)
	a := newAgent(t, na, AgentOptions{Interval: 50 * time.Millisecond, Local: func() LocalInfo {
		return LocalInfo{StationName: "dev-a", PortName: "port-001", IP: netip.MustParseAddr("10.0.0.1")}
	}})
	b := newAgent(t, nb, AgentOptions{Interval: 2 * time.Second, TTLFactor: 1, Local: func() LocalInfo {
		return LocalInfo{StationName: "switch", PortName: "port-007", Legacy: true}
	}})
	waitFor(t, "neighbors", func() bool { return len(a.Neighbors()) == 1 && len(b.Neighbors()) == 1 })
	nbA := a.Neighbors()[0]
	if nbA.MAC != nb.mux.HardwareAddr() || nbA.Frame.TTL != 2 || string(nbA.Frame.ChassisID) != "switch" || !nbA.Expires.After(nbA.Updated) {
		t.Fatalf("neighbor of a %+v", nbA)
	}
	if got := a.Alias(); got != "port-007.switch" {
		t.Fatalf("alias of a %q", got)
	}
	nbB := b.Neighbors()[0]
	// A sub-second interval announces the minimum TTL of 1 s.
	if nbB.MAC != na.mux.HardwareAddr() || nbB.Frame.TTL != 1 || nbB.Frame.ManagementAddr != netip.MustParseAddr("10.0.0.1") {
		t.Fatalf("neighbor of b %+v", nbB)
	}
	if got := b.Alias(); got != "port-001.dev-a" {
		t.Fatalf("alias of b %q", got)
	}
	waitFor(t, "periodic sends", func() bool { return a.Sent() >= 3 })
	if b.Sent() < 1 || a.Malformed() != 0 || b.Malformed() != 0 {
		t.Fatalf("sent %d malformed %d %d", b.Sent(), a.Malformed(), b.Malformed())
	}
	// Close stops sending; Close twice is fine.
	a.Close()
	a.Close()
	s := a.Sent()
	time.Sleep(120 * time.Millisecond)
	if a.Sent() != s {
		t.Fatal("sent after Close")
	}
}

func TestAgentTable(t *testing.T) {
	n := vnet.New(4)
	defer func() { _ = n.Close() }()
	nd, peer := newNode(t, n, 1), newNode(t, n, 2)
	a := newAgent(t, nd, AgentOptions{MaxNeighbors: 2})
	if a.Alias() != "" {
		t.Fatal("alias without neighbors")
	}
	mk := func(chassis, port string, ttl uint16) Frame {
		return Frame{ChassisSubtype: ChassisSubtypeLocal, ChassisID: []byte(chassis), PortSubtype: PortSubtypeLocal, PortID: []byte(port), TTL: ttl}
	}
	peer.sendLLDP(t, mk("b", "port-002", 60))
	peer.sendLLDP(t, mk("b", "port-001", 60))
	peer.sendLLDP(t, mk("a", "port-009", 60)) // over the bound: dropped
	peer.sendRaw(t, []byte{0x02, 0x01})       // malformed
	waitFor(t, "malformed", func() bool { return a.Malformed() == 1 })
	ns := a.Neighbors()
	if len(ns) != 2 || string(ns[0].Frame.PortID) != "port-001" || string(ns[1].Frame.PortID) != "port-002" {
		t.Fatalf("neighbors %+v", ns)
	}
	// An update of a known neighbor is accepted at the bound.
	peer.sendLLDP(t, mk("b", "port-001", 90))
	waitFor(t, "update", func() bool { return a.Neighbors()[0].Frame.TTL == 90 })
	// TTL 0 withdraws a neighbor, which frees a slot.
	peer.sendLLDP(t, mk("b", "port-002", 0))
	waitFor(t, "withdrawal", func() bool { return len(a.Neighbors()) == 1 })
	peer.sendLLDP(t, mk("a", "port-009", 60))
	waitFor(t, "new neighbor", func() bool { return len(a.Neighbors()) == 2 })
	if ns := a.Neighbors(); string(ns[0].Frame.ChassisID) != "a" || a.Alias() != "port-009.a" {
		t.Fatalf("order %+v", ns)
	}
	// Receive-only: SendNow does nothing and nothing was sent.
	if err := a.SendNow(context.Background()); err != nil || a.Sent() != 0 {
		t.Fatalf("receive-only SendNow: %v, sent %d", err, a.Sent())
	}
}

func TestAgentExpiry(t *testing.T) {
	n := vnet.New(5)
	defer func() { _ = n.Close() }()
	a := newAgent(t, newNode(t, n, 1), AgentOptions{})
	now := time.Now()
	f := Frame{ChassisID: []byte("c"), PortID: []byte("p"), TTL: 1}
	a.update(ethernet.MAC{2, 0, 0, 0, 0, 3}, f, now.Add(-2*time.Second))
	f2 := f
	f2.PortID = []byte("q")
	f2.TTL = 120
	a.update(ethernet.MAC{2, 0, 0, 0, 0, 3}, f2, now)
	ns := a.Neighbors()
	if len(ns) != 1 || string(ns[0].Frame.PortID) != "q" {
		t.Fatalf("neighbors %+v", ns)
	}
	// Expired neighbors are also removed when a frame arrives.
	a.update(ethernet.MAC{2, 0, 0, 0, 0, 3}, f2, now.Add(200*time.Second))
	a.mu.Lock()
	l := len(a.neighbors)
	a.mu.Unlock()
	if l != 1 {
		t.Fatalf("%d entries", l)
	}
}

func TestAgentSendErrors(t *testing.T) {
	n := vnet.New(6)
	defer func() { _ = n.Close() }()
	nd := newNode(t, n, 1)
	log := &recLogger{}
	long := strings.Repeat("x", 300)
	a := newAgent(t, nd, AgentOptions{Interval: 20 * time.Millisecond, Logger: log, Local: func() LocalInfo { return LocalInfo{StationName: long} }})
	if err := a.SendNow(context.Background()); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("SendNow: %v", err)
	}
	waitFor(t, "logged send error", func() bool { return log.count() >= 2 })
	if a.Sent() != 0 {
		t.Fatalf("sent %d", a.Sent())
	}
	// A negative interval sends only on SendNow.
	nd2 := newNode(t, n, 2)
	b := newAgent(t, nd2, AgentOptions{Interval: -1, Local: func() LocalInfo { return LocalInfo{StationName: "b"} }})
	if err := b.SendNow(context.Background()); err != nil || b.Sent() != 1 {
		t.Fatalf("SendNow %v sent %d", err, b.Sent())
	}
	nd2.ep.SetLinkUp(false)
	if err := b.SendNow(context.Background()); !errors.Is(err, profinet.ErrLinkDown) || b.Sent() != 1 {
		t.Fatalf("SendNow on a down link: %v", err)
	}
}

type failPort struct {
	*vnet.Endpoint
}

func (failPort) JoinMulticast(ethernet.MAC) error { return profinet.ErrUnsupported }

func TestNewAgentErrors(t *testing.T) {
	n := vnet.New(7)
	defer func() { _ = n.Close() }()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{2, 0, 0, 0, 0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(failPort{ep})
	if _, err := NewAgent(m, AgentOptions{}); !errors.Is(err, profinet.ErrUnsupported) {
		t.Fatalf("join: %v", err)
	}
	_ = m.Close()
	m2 := l2.NewMux(ep)
	_ = m2.Close()
	if _, err := NewAgent(m2, AgentOptions{}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("closed mux: %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	full := fullFrame()
	b, _ := full.AppendTo(nil)
	f.Add(b)
	m, _ := (&Frame{ChassisID: []byte{1}, PortID: []byte{2}}).AppendTo(nil)
	f.Add(m)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := Decode(b)
		if err != nil {
			return
		}
		_ = fr.ChassisString() + fr.PortString()
		out, err := fr.AppendTo(nil)
		if err != nil {
			// Decoded IDs may be empty or longer than AppendTo allows.
			return
		}
		g, err := Decode(out)
		if err != nil {
			t.Fatalf("re-decode: %v", err)
		}
		if g.TTL != fr.TTL || !bytes.Equal(g.ChassisID, fr.ChassisID) || !bytes.Equal(g.PortID, fr.PortID) {
			t.Fatalf("round trip %+v != %+v", g, fr)
		}
	})
}

func BenchmarkDecode(b *testing.B) {
	full := fullFrame()
	buf, err := full.AppendTo(nil)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(buf)))
	for i := 0; i < b.N; i++ {
		if _, err := Decode(buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAppendTo(b *testing.B) {
	full := fullFrame()
	buf := make([]byte, 0, 512)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var err error
		if buf, err = full.AppendTo(buf[:0]); err != nil {
			b.Fatal(err)
		}
	}
}
