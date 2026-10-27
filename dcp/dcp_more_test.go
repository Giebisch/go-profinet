// SPDX-License-Identifier: MIT

package dcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/vnet"
)

// recLogger records the messages logged at each level.
type recLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recLogger) add(level, f string, a []any) {
	l.mu.Lock()
	l.msgs = append(l.msgs, level+" "+fmt.Sprintf(f, a...))
	l.mu.Unlock()
}
func (l *recLogger) Debugf(f string, a ...any) { l.add("DEBUG", f, a) }
func (l *recLogger) Infof(f string, a ...any)  { l.add("INFO", f, a) }
func (l *recLogger) Warnf(f string, a ...any)  { l.add("WARN", f, a) }
func (l *recLogger) Errorf(f string, a ...any) { l.add("ERROR", f, a) }

func (l *recLogger) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// rawDev is a hand-driven endpoint that sends and receives DCP frames.
type rawDev struct {
	ep  *vnet.Endpoint
	mux *l2.Mux
	sub *l2.Subscription
}

func newRaw(t *testing.T, n *vnet.Network, mac ethernet.MAC) *rawDev {
	t.Helper()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: mac})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(ep)
	t.Cleanup(func() { _ = m.Close() })
	sub, err := m.Subscribe(l2.MatchEtherType(ethernet.EtherTypeProfinet), 64)
	if err != nil {
		t.Fatal(err)
	}
	return &rawDev{ep: ep, mux: m, sub: sub}
}

func (d *rawDev) send(t *testing.T, dst ethernet.MAC, f *Frame) {
	t.Helper()
	b, err := f.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	d.sendPayload(t, dst, b)
}

func (d *rawDev) sendPayload(t *testing.T, dst ethernet.MAC, payload []byte) {
	t.Helper()
	hdr := ethernet.Frame{Dst: dst, Src: d.mux.HardwareAddr(), EtherType: ethernet.EtherTypeProfinet}
	b := append(hdr.AppendHeader(nil), payload...)
	if err := d.mux.Send(context.Background(), ethernet.Pad(b, 0)); err != nil {
		t.Fatal(err)
	}
}

// recv returns the next DCP frame (copied) and its source.
func (d *rawDev) recv(t *testing.T, timeout time.Duration) (ethernet.MAC, Frame, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		ef, err := d.sub.Recv(ctx)
		if err != nil {
			return ethernet.MAC{}, Frame{}, false
		}
		f, err := Decode(append([]byte(nil), ef.Payload...))
		if err != nil {
			continue
		}
		return ef.Src, f, true
	}
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

var (
	clientMAC = ethernet.MAC{2, 0, 0, 0, 0, 0x10}
	rawMAC    = ethernet.MAC{2, 0, 0, 0, 0, 0x77}
)

// newClientEnv returns a client on a network plus a raw endpoint.
func newClientEnv(t *testing.T, opts ClientOptions) (*vnet.Network, *Client, *rawDev) {
	t.Helper()
	n := vnet.New(11)
	t.Cleanup(func() { _ = n.Close() })
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: clientMAC})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(ep)
	t.Cleanup(func() { _ = m.Close() })
	if opts.Timeout == 0 {
		opts.Timeout = 200 * time.Millisecond
	}
	c, err := NewClient(m, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return n, c, newRaw(t, n, rawMAC)
}

func TestStrings(t *testing.T) {
	services := map[ServiceID]string{ServiceGet: "Get", ServiceSet: "Set", ServiceIdentify: "Identify", ServiceHello: "Hello", 9: "Service(9)"}
	for s, want := range services {
		if got := s.String(); got != want {
			t.Errorf("service %d: %q", s, got)
		}
	}
	types := map[ServiceType]string{TypeRequest: "Request", TypeResponseSuccess: "Response", TypeResponseUnsupported: "ResponseUnsupported", 3: "ServiceType(3)"}
	for s, want := range types {
		if got := s.String(); got != want {
			t.Errorf("type %d: %q", s, got)
		}
	}
	blocks := map[BlockID]string{
		IPMAC: "IP/MAC", IPParameter: "IP/Parameter", IPFullSuite: "IP/FullSuite",
		DeviceVendor: "Device/Vendor", DeviceName: "Device/NameOfStation", DeviceID: "Device/DeviceID",
		DeviceRole: "Device/Role", DeviceOpts: "Device/Options", DeviceAlias: "Device/AliasName",
		DeviceInst: "Device/Instance", DeviceOEMID: "Device/OEMDeviceID", DeviceGW: "Device/StandardGateway",
		DeviceRSI: "Device/RSIProperties", CtrlStart: "Control/Start", CtrlStop: "Control/Stop",
		CtrlSignal: "Control/Signal", CtrlResponse: "Control/Response", CtrlFactory: "Control/FactoryReset",
		CtrlReset: "Control/ResetToFactory", Initiative: "DeviceInitiative", AllSelector: "All",
		DHCPHostName: "DHCP/12", NewBlockID(OptionNMEDomain, 2): "7/2",
	}
	for b, want := range blocks {
		if got := b.String(); got != want {
			t.Errorf("block 0x%04X: %q, want %q", uint16(b), got, want)
		}
	}
	errs := map[BlockError]string{
		BlockErrorNone: "OK", BlockErrorOptionUnsupported: "option not supported",
		BlockErrorSuboptionUnsupport: "suboption not supported or no data set", BlockErrorSuboptionNotSet: "suboption not set",
		BlockErrorResource: "resource error", BlockErrorSetNotPossible: "SET not possible by local reasons",
		BlockErrorInOperation: "in operation, SET not possible", 77: "block error 77",
	}
	for e, want := range errs {
		if got := e.String(); got != want {
			t.Errorf("block error %d: %q", e, got)
		}
	}
	b := NewBlockID(OptionControl, 4)
	if b != CtrlResponse || b.Option() != OptionControl || b.Suboption() != 4 {
		t.Fatalf("block ID %v", b)
	}
	s := IPSuite{Addr: netip.MustParseAddr("10.0.0.1"), Mask: netip.MustParseAddr("255.0.0.0"), Gateway: netip.MustParseAddr("10.0.0.254")}
	if got := s.String(); got != "10.0.0.1/255.0.0.0 gw 10.0.0.254" {
		t.Fatalf("suite %q", got)
	}
	id := Identity{MAC: ethernet.MAC{2, 0, 0, 0, 0, 1}, NameOfStation: "plc", VendorID: 0x2A, DeviceID: 0x0101, IP: s}
	if got := id.String(); got != `02:00:00:00:00:01 "plc" vendor 0x002A device 0x0101 10.0.0.1/255.0.0.0 gw 10.0.0.254` {
		t.Fatalf("identity %q", got)
	}
	se := &SetError{MAC: id.MAC, Block: DeviceName, Code: BlockErrorInOperation}
	if got := se.Error(); got != "dcp: 02:00:00:00:00:01 rejected Device/NameOfStation: in operation, SET not possible" {
		t.Fatalf("set error %q", got)
	}
	if !errors.Is(se, profinet.ErrRejected) || errors.Is(se, profinet.ErrUnsupported) || errors.Is(se, profinet.ErrTimeout) {
		t.Fatal("SetError.Is")
	}
	if !errors.Is(&SetError{Code: BlockErrorSuboptionUnsupport}, profinet.ErrUnsupported) {
		t.Fatal("suboption unsupported should match ErrUnsupported")
	}
}

func TestIPSuiteHelpers(t *testing.T) {
	if !(IPSuite{}).IsZero() {
		t.Fatal("zero value not zero")
	}
	z := IPSuite{Addr: netip.IPv4Unspecified(), Mask: netip.IPv4Unspecified()}
	if !z.IsZero() {
		t.Fatal("0.0.0.0/0.0.0.0 not zero")
	}
	if (IPSuite{Addr: netip.MustParseAddr("1.2.3.4")}).IsZero() {
		t.Fatal("address set but zero")
	}
	if (IPSuite{Mask: netip.MustParseAddr("255.0.0.0")}).IsZero() {
		t.Fatal("mask set but zero")
	}
	// Non-IPv4 and unset addresses encode as zeros.
	b := AppendIPSuite(nil, IPSuite{Addr: netip.MustParseAddr("::1"), Mask: netip.MustParseAddr("255.255.255.0")})
	if !bytes.Equal(b, []byte{0, 0, 0, 0, 255, 255, 255, 0, 0, 0, 0, 0}) {
		t.Fatalf("suite %x", b)
	}
	if _, err := DecodeIPSuite(b[:11]); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
	cr := ControlResponse{Block: DeviceName, Error: BlockErrorResource}
	got, err := DecodeControlResponse(AppendControlResponse(nil, cr))
	if err != nil || got != cr {
		t.Fatalf("control response %+v %v", got, err)
	}
	if _, err := DecodeControlResponse([]byte{1, 2}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
}

func TestIdentityBlocks(t *testing.T) {
	base := Identity{MAC: ethernet.MAC{2, 1, 2, 3, 4, 5}, NameOfStation: "n", TypeOfStation: "t", VendorID: 1, DeviceID: 2, Role: RoleIODevice,
		IP: IPSuite{Addr: netip.MustParseAddr("10.0.0.2"), Mask: netip.MustParseAddr("255.0.0.0"), Gateway: netip.MustParseAddr("0.0.0.0")}, IPInfo: IPInfoSet}
	ids := func(bs []Block) []BlockID {
		var out []BlockID
		for _, b := range bs {
			out = append(out, b.ID)
		}
		return out
	}
	plain := base.Blocks()
	want := []BlockID{DeviceVendor, DeviceName, DeviceID, DeviceRole, DeviceOpts, IPParameter}
	if fmt.Sprint(ids(plain)) != fmt.Sprint(want) {
		t.Fatalf("blocks %v", ids(plain))
	}
	full := base
	full.HasInstance, full.Instance = true, 0x0102
	full.HasOEM, full.OEMVendorID, full.OEMDeviceID = true, 0x00AA, 0x00BB
	full.HasRSI, full.RSIProperties = true, 0x0003
	fb := full.Blocks()
	want = []BlockID{DeviceVendor, DeviceName, DeviceID, DeviceRole, DeviceInst, DeviceOEMID, DeviceRSI, DeviceOpts, IPParameter}
	if fmt.Sprint(ids(fb)) != fmt.Sprint(want) {
		t.Fatalf("blocks %v", ids(fb))
	}
	// The derived options list every block plus IP and control blocks.
	opt, _ := (&Frame{Blocks: fb}).Find(DeviceOpts)
	got, err := ParseIdentity(full.MAC, fb)
	if err != nil {
		t.Fatal(err)
	}
	wantOpts := []BlockID{IPMAC, IPParameter, DeviceVendor, DeviceName, DeviceID, DeviceRole, DeviceInst, DeviceOEMID, DeviceRSI, CtrlStart, CtrlStop, CtrlSignal, CtrlResponse, CtrlReset}
	if fmt.Sprint(got.Options) != fmt.Sprint(wantOpts) || len(opt.Data) != 2*len(wantOpts) {
		t.Fatalf("options %v", got.Options)
	}
	got.Options = nil
	full.Options = nil
	if fmt.Sprint(got) != fmt.Sprint(full) {
		t.Fatalf("round trip\n got %+v\nwant %+v", got, full)
	}
	// Explicit options are sent as given.
	ex := base
	ex.Options = []BlockID{DeviceName}
	b, ok := ex.Block(DeviceOpts)
	if !ok || !bytes.Equal(b.Data, []byte{0x02, 0x02}) {
		t.Fatalf("options block %+v", b)
	}
	b, ok = ex.Block(IPMAC)
	if !ok || !bytes.Equal(b.Data, ex.MAC[:]) || !b.HasInfo {
		t.Fatalf("MAC block %+v", b)
	}
	if _, ok := ex.Block(DeviceOEMID); ok {
		t.Fatal("OEM block without HasOEM")
	}
	if _, ok := ex.Block(DHCPHostName); ok {
		t.Fatal("DHCP block")
	}
}

func TestParseIdentity(t *testing.T) {
	mac := ethernet.MAC{2, 0, 0, 0, 0, 9}
	id, err := ParseIdentity(mac, []Block{
		{ID: IPFullSuite, Info: IPInfoDHCP, Data: append(AppendIPSuite(nil, IPSuite{Addr: netip.MustParseAddr("1.2.3.4"), Mask: netip.MustParseAddr("255.255.255.0")}), 8, 8, 8, 8)},
		{ID: DeviceAlias, Data: []byte("port-001.sw")},
		{ID: DeviceRSI, Data: []byte{1}}, // too short: ignored
		{ID: DeviceOpts, Data: []byte{1, 2, 3}},
		{ID: DHCPHostName, Data: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id.MAC != mac || id.IP.Addr != netip.MustParseAddr("1.2.3.4") || id.IPInfo != IPInfoDHCP || id.Alias != "port-001.sw" || id.HasRSI || len(id.Options) != 1 || id.Options[0] != IPParameter {
		t.Fatalf("identity %+v", id)
	}
	bad := []Block{
		{ID: DeviceID, Data: []byte{1, 2, 3}},
		{ID: DeviceRole},
		{ID: DeviceInst, Data: []byte{1}},
		{ID: DeviceOEMID, Data: []byte{1, 2}},
		{ID: IPParameter, Data: make([]byte, 11)},
	}
	for _, b := range bad {
		if _, err := ParseIdentity(mac, []Block{b}); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: err %v", b.ID, err)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	hdr := func(fid uint16, s ServiceID, typ ServiceType, data []byte) []byte {
		b := binary.BigEndian.AppendUint16(nil, fid)
		b = append(b, byte(s), byte(typ), 0, 0, 0, 1, 0, 0)
		b = binary.BigEndian.AppendUint16(b, uint16(len(data)))
		return append(b, data...)
	}
	cases := map[string][]byte{
		"short":            make([]byte, 11),
		"not DCP":          hdr(0x8000, ServiceIdentify, TypeRequest, nil),
		"length too big":   append(hdr(FrameIDGetSet, ServiceSet, TypeRequest, nil)[:10], 0, 5, 1, 2),
		"odd Get":          hdr(FrameIDGetSet, ServiceGet, TypeRequest, []byte{2, 2, 2}),
		"truncated header": hdr(FrameIDIdentifyRequest, ServiceIdentify, TypeRequest, []byte{2, 2, 0}),
		"block too long":   hdr(FrameIDIdentifyRequest, ServiceIdentify, TypeRequest, []byte{2, 2, 0, 9, 'a'}),
		"no BlockInfo":     hdr(FrameIDIdentifyResponse, ServiceIdentify, TypeResponseSuccess, []byte{2, 2, 0, 1, 'a', 0}),
	}
	for name, b := range cases {
		if _, err := Decode(b); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	// A Control/Response block in a response has no BlockInfo; an odd
	// block is padded; the last odd block may omit its padding.
	f, err := Decode(hdr(FrameIDGetSet, ServiceSet, TypeResponseSuccess, []byte{5, 4, 0, 3, 2, 2, 0, 0, 2, 2, 0, 3, 0, 0, 'x'}))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Blocks) != 2 || f.Blocks[0].HasInfo || !bytes.Equal(f.Blocks[0].Data, []byte{2, 2, 0}) || !f.Blocks[1].HasInfo || string(f.Blocks[1].Data) != "x" {
		t.Fatalf("blocks %+v", f.Blocks)
	}
	if f.IsRequest() {
		t.Fatal("response is a request")
	}
	if _, ok := f.Find(DeviceName); !ok {
		t.Fatal("Find")
	}
	if _, ok := f.Find(DeviceVendor); ok {
		t.Fatal("Find absent")
	}
	// An AllSelector filter in a Hello/Set request has no BlockInfo.
	f, err = Decode(hdr(FrameIDHello, ServiceHello, TypeRequest, []byte{0xFF, 0xFF, 0, 0}))
	if err != nil || len(f.Blocks) != 1 || f.Blocks[0].HasInfo || !f.IsRequest() {
		t.Fatalf("all selector %+v %v", f, err)
	}
}

func TestAppendToErrors(t *testing.T) {
	prefix := []byte{0xAA, 0xBB}
	f := Frame{FrameID: FrameIDGetSet, Service: ServiceSet, Type: TypeRequest, Blocks: []Block{{ID: DeviceName, HasInfo: true, Data: make([]byte, 0xFFFE)}}}
	out, err := f.AppendTo(prefix)
	if !errors.Is(err, profinet.ErrInvalidConfig) || !bytes.Equal(out, prefix) {
		t.Fatalf("oversize block: %v %x", err, out[:min(len(out), 4)])
	}
	f.Blocks = []Block{{ID: DeviceName, HasInfo: true, Data: make([]byte, MaxDataLen)}}
	out, err = f.AppendTo(prefix)
	if !errors.Is(err, profinet.ErrInvalidConfig) || !bytes.Equal(out, prefix) {
		t.Fatalf("oversize frame: %v", err)
	}
	// Exactly MaxDataLen fits.
	f.Blocks = []Block{{ID: DeviceName, Data: make([]byte, MaxDataLen-4)}}
	if _, err := f.AppendTo(nil); err != nil {
		t.Fatal(err)
	}
	g := Frame{FrameID: FrameIDGetSet, Service: ServiceGet, Type: TypeRequest, Blocks: make([]Block, MaxDataLen/2+1)}
	if _, err := g.AppendTo(nil); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("oversize Get: %v", err)
	}
}

func TestValidSuite(t *testing.T) {
	p := netip.MustParseAddr
	cases := []struct {
		addr, mask, gw string
		ok             bool
	}{
		{"192.168.0.10", "255.255.255.0", "0.0.0.0", true},
		{"192.168.0.10", "255.255.255.0", "192.168.0.1", true},
		{"192.168.0.10", "255.255.255.0", "192.168.0.10", true}, // gateway == address
		{"192.168.0.10", "255.255.255.0", "192.168.1.1", false}, // gateway outside
		{"192.168.0.0", "255.255.255.0", "0.0.0.0", false},      // network address
		{"192.168.0.255", "255.255.255.0", "0.0.0.0", false},    // broadcast address
		{"10.0.0.1", "255.255.255.254", "0.0.0.0", true},        // /31
		{"10.0.0.0", "255.255.255.255", "0.0.0.0", true},        // /32
		{"10.0.0.1", "0.0.0.0", "0.0.0.0", false},               // empty mask
		{"10.0.0.1", "255.0.255.0", "0.0.0.0", false},           // non-contiguous
		{"0.1.2.3", "255.0.0.0", "0.0.0.0", false},
		{"127.0.0.1", "255.0.0.0", "0.0.0.0", false},
		{"224.0.0.1", "255.0.0.0", "0.0.0.0", false},
		{"255.255.255.255", "255.0.0.0", "0.0.0.0", false},
		{"::1", "255.0.0.0", "0.0.0.0", false},
		{"10.0.0.1", "::", "0.0.0.0", false},
	}
	for _, c := range cases {
		s := IPSuite{Addr: p(c.addr), Mask: p(c.mask), Gateway: p(c.gw)}
		if got := validSuite(s); got != c.ok {
			t.Errorf("%v: %v, want %v", s, got, c.ok)
		}
	}
	if !validSuite(IPSuite{Addr: p("10.0.0.1"), Mask: p("255.0.0.0")}) {
		t.Error("unset gateway rejected")
	}
}

func TestMatches(t *testing.T) {
	id := Identity{MAC: ethernet.MAC{2, 0, 0, 0, 0, 1}, NameOfStation: "a", VendorID: 1, DeviceID: 2}
	if matches(&id, nil) {
		t.Fatal("empty filter matched")
	}
	if !matches(&id, Filter{{ID: IPMAC, Data: id.MAC[:]}}) || matches(&id, Filter{{ID: IPMAC, Data: []byte{1}}}) {
		t.Fatal("MAC filter")
	}
	if matches(&id, FilterAlias("x")) || matches(&id, FilterAlias("")) {
		t.Fatal("alias without an alias")
	}
	id.Alias = "x"
	if !matches(&id, FilterAlias("x")) || matches(&id, FilterAlias("y")) {
		t.Fatal("alias filter")
	}
	if matches(&id, Filter{{ID: DeviceOEMID, Data: []byte{0, 1, 0, 2}}}) {
		t.Fatal("absent block matched")
	}
	if !matches(&id, append(FilterName("a"), FilterDeviceID(1, 2)...)) || matches(&id, append(FilterName("a"), FilterDeviceID(1, 3)...)) {
		t.Fatal("combined filter")
	}
}

func TestHellos(t *testing.T) {
	_, c, raw := newClientEnv(t, ClientOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	ch := c.Hellos(ctx, 0)
	ch2 := c.Hellos(context.Background(), 1)
	id := Identity{NameOfStation: "hello-dev", VendorID: 7, DeviceID: 8, Role: RoleIODevice}
	raw.send(t, ethernet.DCPHelloMulticast, &Frame{FrameID: FrameIDHello, Service: ServiceHello, Type: TypeRequest, Xid: 99, Blocks: id.Blocks()})
	select {
	case got := <-ch:
		if got.MAC != rawMAC || got.NameOfStation != "hello-dev" || got.VendorID != 7 || got.DeviceID != 8 {
			t.Fatalf("hello %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
	}
	<-ch2
	// A second Hello overflows ch2 (buffer 1) once it is full; it must
	// not block delivery to ch.
	for i := 0; i < 3; i++ {
		raw.send(t, ethernet.DCPHelloMulticast, &Frame{FrameID: FrameIDHello, Service: ServiceHello, Type: TypeRequest, Xid: 100, Blocks: id.Blocks()})
	}
	for i := 0; i < 3; i++ {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("hello lost")
		}
	}
	// A malformed Hello identity is counted.
	raw.send(t, ethernet.DCPHelloMulticast, &Frame{FrameID: FrameIDHello, Service: ServiceHello, Type: TypeRequest, Blocks: []Block{{ID: DeviceID, HasInfo: true, Data: []byte{1}}}})
	waitFor(t, "malformed hello", func() bool { return c.Malformed() == 1 })
	cancel()
	for range ch {
	}
	// Closing the client closes the remaining channel.
	_ = c.Close()
	for range ch2 {
	}
}

func TestClientMalformedAndIgnored(t *testing.T) {
	_, c, raw := newClientEnv(t, ClientOptions{})
	// Undecodable DCP frame.
	raw.sendPayload(t, clientMAC, []byte{0xFE, 0xFF, 5, 1, 0, 0, 0, 1, 0, 0, 0, 40})
	waitFor(t, "malformed", func() bool { return c.Malformed() == 1 })
	// Requests other than Hello and responses nobody waits for are dropped.
	raw.send(t, clientMAC, &Frame{FrameID: FrameIDIdentifyRequest, Service: ServiceIdentify, Type: TypeRequest, Blocks: FilterAll()})
	raw.send(t, clientMAC, &Frame{FrameID: FrameIDIdentifyResponse, Service: ServiceIdentify, Type: TypeResponseSuccess, Xid: 12345})
	// A malformed Identify response is counted when it is parsed.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			ef, err := raw.sub.Recv(ctx)
			if err != nil {
				return
			}
			f, err := Decode(ef.Payload)
			if err != nil || ef.Src != clientMAC || f.Service != ServiceIdentify {
				continue
			}
			// A response of the wrong type, then a broken identity.
			for _, r := range []*Frame{
				{FrameID: FrameIDIdentifyResponse, Service: ServiceIdentify, Type: TypeResponseUnsupported, Xid: f.Xid},
				{FrameID: FrameIDIdentifyResponse, Service: ServiceIdentify, Type: TypeResponseSuccess, Xid: f.Xid, Blocks: []Block{{ID: DeviceRole, HasInfo: true}}},
			} {
				b, _ := r.AppendTo(nil)
				hdr := ethernet.Frame{Dst: clientMAC, Src: rawMAC, EtherType: ethernet.EtherTypeProfinet}
				_ = raw.mux.Send(ctx, ethernet.Pad(append(hdr.AppendHeader(nil), b...), 0))
			}
			return
		}
	}()
	ids, err := c.Identify(context.Background(), nil, IdentifyOptions{Wait: 300 * time.Millisecond})
	if err != nil || len(ids) != 0 {
		t.Fatalf("identify %v %v", ids, err)
	}
	if c.Malformed() != 2 {
		t.Fatalf("malformed %d", c.Malformed())
	}
}

func TestClientQueueFull(t *testing.T) {
	log := &recLogger{}
	_, c, raw := newClientEnv(t, ClientOptions{Logger: log})
	x, _ := c.register(1)
	defer c.unregister(x)
	for i := 0; i < 3; i++ {
		raw.send(t, clientMAC, &Frame{FrameID: FrameIDGetSet, Service: ServiceGet, Type: TypeResponseSuccess, Xid: x})
	}
	waitFor(t, "queue full warning", func() bool { return log.has("queue of transaction") })
}

func TestNewClientClosedMux(t *testing.T) {
	n := vnet.New(1)
	defer func() { _ = n.Close() }()
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: clientMAC})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(ep)
	_ = m.Close()
	if _, err := NewClient(m, ClientOptions{}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("client: %v", err)
	}
	if _, err := NewResponder(m, &fakeStation{}, ResponderOptions{}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("responder: %v", err)
	}
}

func TestClientRetriesDefaults(t *testing.T) {
	_, c, _ := newClientEnv(t, ClientOptions{Retries: -1})
	if c.opts.Retries != 0 || c.opts.Timeout != 200*time.Millisecond {
		t.Fatalf("opts %+v", c.opts)
	}
	_, c2, _ := newClientEnv(t, ClientOptions{Timeout: -1})
	if c2.opts.Retries != 3 || c2.opts.Timeout != time.Second {
		t.Fatalf("opts %+v", c2.opts)
	}
	// The context ends a transaction waiting for its response.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c2.Get(ctx, rawMAC); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("get: %v", err)
	}
	// A closed client fails at once.
	_ = c2.Close()
	if _, err := c2.IdentifyName(context.Background(), "x", time.Minute); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("identify name: %v", err)
	}
}

func TestTransactRetransmit(t *testing.T) {
	e := newEnv(t, 1)
	ep := e.net.Endpoints()[1]
	if ep.HardwareAddr() != e.macs[0] {
		t.Fatal("endpoint order")
	}
	var dropped atomic.Int32
	ep.SetFaults(vnet.Faults{Loss: 1, Match: func(frame []byte) bool {
		// Drop the first response of the device.
		return dropped.Add(1) == 1
	}})
	bs, err := e.client.Get(context.Background(), e.macs[0], DeviceName)
	if err != nil || len(bs) != 1 || string(bs[0].Data) != "dev-a" {
		t.Fatalf("get %v %v", bs, err)
	}
	if dropped.Load() < 2 || ep.Stats().Dropped != 1 {
		t.Fatalf("dropped %d, stats %+v", dropped.Load(), ep.Stats())
	}
}

func TestIdentifyDuplicates(t *testing.T) {
	e := newEnv(t, 2)
	for _, ep := range e.net.Endpoints()[1:] {
		ep.SetFaults(vnet.Faults{Duplicate: 1})
	}
	ids, err := e.client.Identify(context.Background(), FilterAll(), IdentifyOptions{Wait: 300 * time.Millisecond})
	if err != nil || len(ids) != 2 || ids[0].MAC == ids[1].MAC {
		t.Fatalf("identify %v %v", ids, err)
	}
	if e.net.Endpoints()[1].Stats().Duplicated == 0 {
		t.Fatal("no duplicate sent")
	}
}

func TestIdentifyContext(t *testing.T) {
	e := newEnv(t, 1)
	// A deadline after a response returns the results without an error.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ids, err := e.client.Identify(ctx, FilterAll(), IdentifyOptions{Wait: time.Minute})
	if err != nil || len(ids) != 1 {
		t.Fatalf("identify %v %v", ids, err)
	}
	// Cancellation without results returns the context error.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	ids, err = e.client.Identify(ctx2, FilterName("nobody"), IdentifyOptions{Wait: time.Minute})
	if !errors.Is(err, context.DeadlineExceeded) || len(ids) != 0 {
		t.Fatalf("identify %v %v", ids, err)
	}
	// Max stops early; a zero wait still collects.
	ids, err = e.client.Identify(context.Background(), FilterAll(), IdentifyOptions{Max: 1})
	if err != nil || len(ids) != 1 {
		t.Fatalf("identify %v %v", ids, err)
	}
	// Close during Identify.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = e.client.Close()
	}()
	if _, err := e.client.Identify(context.Background(), FilterName("nobody"), IdentifyOptions{Wait: time.Minute}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
}

func TestSendErrors(t *testing.T) {
	_, c, _ := newClientEnv(t, ClientOptions{})
	c.mux.Port().(*vnet.Endpoint).SetLinkUp(false)
	ctx := context.Background()
	if _, err := c.Identify(ctx, nil, IdentifyOptions{}); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("identify: %v", err)
	}
	if _, err := c.Get(ctx, rawMAC); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("get: %v", err)
	}
	if _, err := c.GetIdentity(ctx, rawMAC); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("get identity: %v", err)
	}
	if err := c.Set(ctx, rawMAC); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("set without blocks: %v", err)
	}
	if err := c.SetName(ctx, rawMAC, "", false); !errors.Is(err, profinet.ErrLinkDown) {
		t.Fatalf("set name: %v", err)
	}
	for _, s := range []IPSuite{
		{Addr: netip.MustParseAddr("::1")},
		{Mask: netip.MustParseAddr("::1")},
		{Gateway: netip.MustParseAddr("::1")},
	} {
		if err := c.SetIP(ctx, rawMAC, s, false); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Fatalf("set IP %v: %v", s, err)
		}
	}
	// An oversized request fails to encode.
	if _, err := c.Identify(ctx, FilterName(strings.Repeat("a", 2000)), IdentifyOptions{}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("oversize identify: %v", err)
	}
}

// fakeDevice answers each Get/Set request of the client with the frame
// reply returns (nil sends nothing).
func fakeDevice(t *testing.T, raw *rawDev, reply func(req Frame) []*Frame) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for {
			ef, err := raw.sub.Recv(ctx)
			if err != nil {
				return
			}
			f, err := Decode(append([]byte(nil), ef.Payload...))
			if err != nil || f.FrameID != FrameIDGetSet || f.Type != TypeRequest {
				continue
			}
			for _, r := range reply(f) {
				b, _ := r.AppendTo(nil)
				hdr := ethernet.Frame{Dst: ef.Src, Src: raw.mux.HardwareAddr(), EtherType: ethernet.EtherTypeProfinet}
				_ = raw.mux.Send(ctx, ethernet.Pad(append(hdr.AppendHeader(nil), b...), 0))
			}
		}
	}()
}

func TestClientAgainstFakeDevice(t *testing.T) {
	n, c, raw := newClientEnv(t, ClientOptions{Timeout: 100 * time.Millisecond, Retries: -1})
	other := newRaw(t, n, ethernet.MAC{2, 0, 0, 0, 0, 0x88})
	var mode atomic.Int32
	fakeDevice(t, raw, func(req Frame) []*Frame {
		resp := &Frame{FrameID: FrameIDGetSet, Service: req.Service, Type: TypeResponseSuccess, Xid: req.Xid}
		switch mode.Load() {
		case 0: // broken control response
			resp.Blocks = []Block{{ID: CtrlResponse, Data: []byte{1}}}
		case 1: // no acknowledgement at all
		case 2: // unsupported service, preceded by a response from another station
			b, _ := (&Frame{FrameID: FrameIDGetSet, Service: req.Service, Type: TypeResponseSuccess, Xid: req.Xid}).AppendTo(nil)
			hdr := ethernet.Frame{Dst: clientMAC, Src: other.mux.HardwareAddr(), EtherType: ethernet.EtherTypeProfinet}
			_ = other.mux.Send(context.Background(), ethernet.Pad(append(hdr.AppendHeader(nil), b...), 0))
			resp.Type = TypeResponseUnsupported
		case 3: // a response of a different service
			resp.Service = ServiceIdentify
		case 4: // Get with a malformed identity
			resp.Blocks = []Block{{ID: DeviceID, HasInfo: true, Data: []byte{1}}}
		}
		return []*Frame{resp}
	})
	ctx := context.Background()
	if _, err := c.Get(ctx, rawMAC, DeviceName); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("get: %v", err)
	}
	if err := c.Signal(ctx, rawMAC); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("set: %v", err)
	}
	mode.Store(1)
	if err := c.Signal(ctx, rawMAC); !errors.Is(err, profinet.ErrMalformed) || !strings.Contains(err.Error(), "no response for the block") {
		t.Fatalf("set unacked: %v", err)
	}
	// Start and Stop need no acknowledgement.
	if err := c.Set(ctx, rawMAC, SetBlock{ID: CtrlStart}, SetBlock{ID: CtrlStop}); err != nil {
		t.Fatalf("set start/stop: %v", err)
	}
	mode.Store(2)
	if _, err := c.Get(ctx, rawMAC, DeviceName); !errors.Is(err, profinet.ErrUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
	mode.Store(3)
	if _, err := c.Get(ctx, rawMAC, DeviceName); !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("other service: %v", err)
	}
	mode.Store(4)
	if _, err := c.GetIdentity(ctx, rawMAC); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("identity: %v", err)
	}
	// Close during a transaction.
	mode.Store(3)
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = c.Close()
	}()
	c.opts.Timeout = time.Minute
	if _, err := c.Get(ctx, rawMAC, DeviceName); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
}

// responderEnv is a responder for one fake station plus a raw requester.
func responderEnv(t *testing.T, st *fakeStation, opts ResponderOptions, mac ethernet.MAC, mtu int) (*Responder, *rawDev, *vnet.Endpoint) {
	t.Helper()
	n := vnet.New(5)
	t.Cleanup(func() { _ = n.Close() })
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: mac, MTU: mtu})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(ep)
	t.Cleanup(func() { _ = m.Close() })
	r, err := NewResponder(m, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, newRaw(t, n, rawMAC), ep
}

var devMAC = ethernet.MAC{2, 0, 0, 0, 0, 0x20}

func testStation() *fakeStation {
	return &fakeStation{id: Identity{NameOfStation: "dev", VendorID: 1, DeviceID: 2, Alias: "port-001.sw",
		IP: IPSuite{Addr: netip.MustParseAddr("10.0.0.5"), Mask: netip.MustParseAddr("255.0.0.0"), Gateway: netip.MustParseAddr("0.0.0.0")}}}
}

func TestResponderFilters(t *testing.T) {
	r, raw, _ := responderEnv(t, testStation(), ResponderOptions{NoResponseDelay: true}, devMAC, 0)
	identify := func(filter []Block) bool {
		raw.send(t, ethernet.DCPIdentifyMulticast, &Frame{FrameID: FrameIDIdentifyRequest, Service: ServiceIdentify, Type: TypeRequest, Xid: 1, ResponseDelay: 100, Blocks: filter})
		_, f, ok := raw.recv(t, 200*time.Millisecond)
		return ok && f.FrameID == FrameIDIdentifyResponse
	}
	yes := []Filter{FilterAll(), FilterAlias("port-001.sw"), {{ID: IPMAC, Data: devMAC[:]}}, FilterName("dev"), FilterDeviceID(1, 2)}
	no := []Filter{nil, FilterAlias("other"), {{ID: IPMAC, Data: rawMAC[:]}}, FilterName("x"), {{ID: DeviceOEMID, Data: []byte{0, 1, 0, 2}}}}
	for _, f := range yes {
		if !identify(f) {
			t.Errorf("filter %v did not match", f)
		}
	}
	for _, f := range no {
		if identify(f) {
			t.Errorf("filter %v matched", f)
		}
	}
	st := r.Stats()
	if st.Identify != uint64(len(yes)) || st.Ignored != uint64(len(no)) {
		t.Fatalf("stats %+v", st)
	}
}

// delayedMAC returns a MAC whose identify spread is at least min.
func delayedMAC(t *testing.T, min time.Duration) ethernet.MAC {
	t.Helper()
	n := vnet.New(1)
	defer func() { _ = n.Close() }()
	for i := 0; i < 255; i++ {
		mac := ethernet.MAC{2, 0, 0, 0, 1, byte(i)}
		ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: mac})
		if err != nil {
			t.Fatal(err)
		}
		m := l2.NewMux(ep)
		r, err := NewResponder(m, &fakeStation{}, ResponderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		d := r.delay
		_ = r.Close()
		_ = m.Close()
		if d >= min {
			return mac
		}
	}
	t.Fatal("no MAC with a long spread")
	return ethernet.MAC{}
}

func TestResponderDelay(t *testing.T) {
	mac := delayedMAC(t, 5*time.Millisecond)
	r, raw, _ := responderEnv(t, testStation(), ResponderOptions{MaxPendingIdentify: 1}, mac, 0)
	req := &Frame{FrameID: FrameIDIdentifyRequest, Service: ServiceIdentify, Type: TypeRequest, Xid: 1, ResponseDelay: 1000, Blocks: FilterAll()}
	// A delayed response arrives.
	start := time.Now()
	raw.send(t, ethernet.DCPIdentifyMulticast, req)
	if _, f, ok := raw.recv(t, 2*time.Second); !ok || f.Xid != 1 {
		t.Fatal("no delayed response")
	}
	if time.Since(start) < r.delay/2 {
		t.Fatalf("response after %v, delay %v", time.Since(start), r.delay)
	}
	// With the pending bound reached, a request is ignored.
	waitFor(t, "idle", func() bool { return r.pendingIdentify.Load() == 0 })
	r.pendingIdentify.Store(1)
	raw.send(t, ethernet.DCPIdentifyMulticast, req)
	waitFor(t, "ignored", func() bool { return r.Stats().Ignored == 1 })
	if _, _, ok := raw.recv(t, 100*time.Millisecond); ok {
		t.Fatal("response over the pending bound")
	}
	r.pendingIdentify.Store(0)
	// Closing with a delayed response pending drops it. The response is
	// held for 5 s, so that it is still pending when the test looks: with
	// the MAC's own delay of a few milliseconds a slow runner could miss
	// the pending response altogether. (The frame sent below orders this
	// write before the responder reads the delay.)
	r.delay = 5 * time.Second
	raw.send(t, ethernet.DCPIdentifyMulticast, req)
	waitFor(t, "pending", func() bool { return r.pendingIdentify.Load() == 1 })
	_ = r.Close()
	if r.pendingIdentify.Load() != 0 {
		t.Fatal("pending response after Close")
	}
}

func TestResponderGetSet(t *testing.T) {
	st := testStation()
	log := &recLogger{}
	r, raw, _ := responderEnv(t, st, ResponderOptions{NoResponseDelay: true, Logger: log}, devMAC, 200)
	xid := uint32(0)
	call := func(dst ethernet.MAC, f *Frame) (Frame, bool) {
		xid++
		f.FrameID, f.Type, f.Xid = FrameIDGetSet, TypeRequest, xid
		raw.send(t, dst, f)
		_, resp, ok := raw.recv(t, 300*time.Millisecond)
		if ok && resp.Xid != xid {
			t.Fatalf("xid %d, want %d", resp.Xid, xid)
		}
		return resp, ok
	}
	codes := func(f Frame) []BlockError {
		var out []BlockError
		for _, b := range f.Blocks {
			cr, err := DecodeControlResponse(b.Data)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, cr.Error)
		}
		return out
	}
	resp, ok := call(devMAC, &Frame{Service: ServiceGet, Blocks: []Block{{ID: IPFullSuite}, {ID: DeviceOEMID}, {ID: DHCPHostName}, {ID: AllSelector}}})
	if !ok || len(resp.Blocks) < 4 {
		t.Fatalf("get %+v", resp)
	}
	if got := codes(Frame{Blocks: resp.Blocks[:3]}); !slices.Equal(got, []BlockError{BlockErrorSuboptionUnsupport, BlockErrorSuboptionUnsupport, BlockErrorOptionUnsupported}) {
		t.Fatalf("get codes %s", got)
	}
	if resp.Blocks[3].ID != IPMAC || !bytes.Equal(resp.Blocks[3].Data, devMAC[:]) {
		t.Fatalf("get all %+v", resp.Blocks[3])
	}
	set := []Block{
		{ID: NewBlockID(OptionIP, 9), HasInfo: true},                          // unsupported suboption
		{ID: DHCPHostName, HasInfo: true, Data: []byte("x")},                  // unsupported option
		{ID: IPParameter, HasInfo: true, Data: []byte{1, 2, 3}},               // malformed suite
		{ID: CtrlSignal, HasInfo: true, Data: []byte{0, 1}},                   // wrong signal value
		{ID: CtrlSignal, HasInfo: true},                                       // no signal value
		{ID: CtrlFactory, HasInfo: true},                                      // legacy reset
		{ID: DeviceName, HasInfo: true},                                       // remove the name
		{ID: IPParameter, HasInfo: true, Data: AppendIPSuite(nil, IPSuite{})}, // remove the address
		{ID: NewBlockID(OptionControl, 9), HasInfo: true},
	}
	resp, ok = call(devMAC, &Frame{Service: ServiceSet, Blocks: set})
	if !ok {
		t.Fatal("no set response")
	}
	want := []BlockError{BlockErrorSuboptionUnsupport, BlockErrorOptionUnsupported, BlockErrorSetNotPossible, BlockErrorSetNotPossible, BlockErrorSetNotPossible, BlockErrorNone, BlockErrorNone, BlockErrorNone, BlockErrorSuboptionUnsupport}
	if got := codes(resp); !slices.Equal(got, want) {
		t.Fatalf("set codes %v, want %v", got, want)
	}
	st.mu.Lock()
	if len(st.resets) != 1 || st.resets[0] != ResetAllData || st.id.NameOfStation != "" || !st.id.IP.IsZero() || st.signals != 0 {
		t.Fatalf("station %+v", st)
	}
	st.mu.Unlock()
	// An unknown service is answered with ResponseUnsupported.
	resp, ok = call(devMAC, &Frame{Service: 9})
	if !ok || resp.Type != TypeResponseUnsupported || resp.Service != 9 {
		t.Fatalf("unknown service %+v", resp)
	}
	// Get/Set not addressed to the device, responses and Identify requests
	// with the wrong service are ignored; malformed frames are counted.
	if _, ok := call(ethernet.Broadcast, &Frame{Service: ServiceGet, Blocks: []Block{{ID: DeviceName}}}); ok {
		t.Fatal("answered a broadcast Get")
	}
	raw.send(t, devMAC, &Frame{FrameID: FrameIDGetSet, Service: ServiceGet, Type: TypeResponseSuccess})
	raw.send(t, ethernet.DCPIdentifyMulticast, &Frame{FrameID: FrameIDIdentifyRequest, Service: ServiceGet, Type: TypeRequest})
	raw.sendPayload(t, devMAC, []byte{0xFE, 0xFD, 3, 0, 0, 0, 0, 1, 0, 0, 0, 3, 1, 2, 3})
	waitFor(t, "stats", func() bool { s := r.Stats(); return s.Malformed == 1 && s.Ignored == 2 })
	if s := r.Stats(); s.Get != 1 || s.Set != 1 {
		t.Fatalf("stats %+v", s)
	}
	// A response that does not encode is logged.
	st.mu.Lock()
	st.id.NameOfStation = strings.Repeat("n", MaxDataLen)
	st.mu.Unlock()
	if _, ok := call(devMAC, &Frame{Service: ServiceGet, Blocks: []Block{{ID: DeviceName}}}); ok {
		t.Fatal("oversize response sent")
	}
	if !log.has("ERROR dcp: encode Get response") {
		t.Fatalf("log %v", log.msgs)
	}
	// A response that exceeds the MTU of the port is logged.
	st.mu.Lock()
	st.id.NameOfStation = strings.Repeat("n", 300)
	st.mu.Unlock()
	if _, ok := call(devMAC, &Frame{Service: ServiceGet, Blocks: []Block{{ID: DeviceName}}}); ok {
		t.Fatal("response beyond the MTU sent")
	}
	waitFor(t, "send warning", func() bool { return log.has("WARN dcp: send Get response") })
}

func BenchmarkDecode(b *testing.B) {
	id := Identity{NameOfStation: "plc-1.line-2", TypeOfStation: "go-profinet", VendorID: 1, DeviceID: 2, Role: RoleIODevice, HasInstance: true,
		IP: IPSuite{Addr: netip.MustParseAddr("10.0.0.5"), Mask: netip.MustParseAddr("255.0.0.0"), Gateway: netip.MustParseAddr("0.0.0.0")}}
	f := Frame{FrameID: FrameIDIdentifyResponse, Service: ServiceIdentify, Type: TypeResponseSuccess, Xid: 1, Blocks: id.Blocks()}
	buf, err := f.AppendTo(nil)
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
	id := Identity{NameOfStation: "plc-1.line-2", TypeOfStation: "go-profinet", VendorID: 1, DeviceID: 2, Role: RoleIODevice, HasInstance: true}
	f := Frame{FrameID: FrameIDIdentifyResponse, Service: ServiceIdentify, Type: TypeResponseSuccess, Xid: 1, Blocks: id.Blocks()}
	buf := make([]byte, 0, 512)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var err error
		if buf, err = f.AppendTo(buf[:0]); err != nil {
			b.Fatal(err)
		}
	}
}

func TestIPSuiteStringZero(t *testing.T) {
	if got := (IPSuite{}).String(); got != "0.0.0.0/0.0.0.0 gw 0.0.0.0" {
		t.Fatal(got)
	}
}
