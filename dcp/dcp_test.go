// SPDX-License-Identifier: MIT

package dcp

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/vnet"
)

type fakeStation struct {
	mu       sync.Mutex
	id       Identity
	signals  int
	resets   []uint16
	locked   bool
	lastPerm bool
}

func (s *fakeStation) Identity() Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

func (s *fakeStation) SetName(name string, permanent bool) BlockError {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked {
		return BlockErrorInOperation
	}
	s.id.NameOfStation = name
	s.lastPerm = permanent
	return BlockErrorNone
}

func (s *fakeStation) SetIP(ip IPSuite, permanent bool) BlockError {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id.IP = ip
	s.id.IPInfo = IPInfoSet
	if ip.IsZero() {
		s.id.IPInfo = 0
	}
	s.lastPerm = permanent
	return BlockErrorNone
}

func (s *fakeStation) Signal() BlockError {
	s.mu.Lock()
	s.signals++
	s.mu.Unlock()
	return BlockErrorNone
}

func (s *fakeStation) ResetToFactory(mode uint16) BlockError {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resets = append(s.resets, mode)
	if mode == ResetDevice {
		return BlockErrorSetNotPossible
	}
	return BlockErrorNone
}

type env struct {
	net      *vnet.Network
	client   *Client
	stations []*fakeStation
	macs     []ethernet.MAC
}

func newEnv(t *testing.T, devices int) *env {
	t.Helper()
	e := &env{net: vnet.New(7)}
	t.Cleanup(func() { e.net.Close() })
	mk := func(i byte) *l2.Mux {
		ep, err := e.net.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{2, 0, 0, 0, 0, i}})
		if err != nil {
			t.Fatal(err)
		}
		m := l2.NewMux(ep)
		t.Cleanup(func() { m.Close() })
		return m
	}
	cm := mk(0x10)
	c, err := NewClient(cm, ClientOptions{Timeout: 200 * time.Millisecond, Retries: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	e.client = c
	for i := 0; i < devices; i++ {
		m := mk(byte(0x20 + i))
		st := &fakeStation{id: Identity{
			NameOfStation: "dev-" + string(rune('a'+i)),
			TypeOfStation: "go-profinet test",
			VendorID:      0x1234, DeviceID: uint16(0x10 + i),
			Role:        RoleIODevice,
			HasInstance: true, Instance: 0x0001,
			IP:     IPSuite{Addr: netip.AddrFrom4([4]byte{192, 168, 0, byte(10 + i)}), Mask: netip.MustParseAddr("255.255.255.0"), Gateway: netip.MustParseAddr("0.0.0.0")},
			IPInfo: IPInfoSet,
		}}
		r, err := NewResponder(m, st, ResponderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close() })
		e.stations = append(e.stations, st)
		e.macs = append(e.macs, m.HardwareAddr())
	}
	return e
}

func TestIdentifyAllAndByName(t *testing.T) {
	e := newEnv(t, 5)
	ctx := context.Background()
	ids, err := e.client.Identify(ctx, FilterAll(), IdentifyOptions{ResponseDelay: 10, Wait: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 5 {
		t.Fatalf("found %d devices", len(ids))
	}
	id, err := e.client.IdentifyName(ctx, "dev-c", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if id.MAC != e.macs[2] || id.DeviceID != 0x12 || id.VendorID != 0x1234 || id.TypeOfStation != "go-profinet test" || id.Role != RoleIODevice || !id.HasInstance {
		t.Fatalf("identity %+v", id)
	}
	if id.IP.Addr != netip.AddrFrom4([4]byte{192, 168, 0, 12}) || id.IPInfo != IPInfoSet {
		t.Fatalf("ip %v", id.IP)
	}
	if len(id.Options) == 0 {
		t.Fatal("no options")
	}
	if _, err := e.client.IdentifyName(ctx, "nobody", 100*time.Millisecond); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	ids, _ = e.client.Identify(ctx, FilterDeviceID(0x1234, 0x13), IdentifyOptions{Wait: 200 * time.Millisecond})
	if len(ids) != 1 || ids[0].MAC != e.macs[3] {
		t.Fatalf("by device ID: %v", ids)
	}
	ids, _ = e.client.Identify(ctx, FilterIP(e.stations[4].id.IP), IdentifyOptions{Wait: 200 * time.Millisecond})
	if len(ids) != 1 || ids[0].MAC != e.macs[4] {
		t.Fatalf("by IP: %v", ids)
	}
}

func TestGetSet(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	mac := e.macs[0]
	bs, err := e.client.Get(ctx, mac, DeviceName, IPParameter, IPMAC)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 3 || string(bs[0].Data) != "dev-a" || !bytes.Equal(bs[2].Data, mac[:]) {
		t.Fatalf("blocks %+v", bs)
	}
	_, err = e.client.Get(ctx, mac, DeviceName, NewBlockID(OptionDHCP, 61))
	var se *SetError
	if !errors.As(err, &se) || se.Code != BlockErrorOptionUnsupported || !errors.Is(err, profinet.ErrUnsupported) {
		t.Fatalf("err %v", err)
	}
	if err := e.client.SetName(ctx, mac, "new-name.plant1", true); err != nil {
		t.Fatal(err)
	}
	id, err := e.client.GetIdentity(ctx, mac)
	if err != nil || id.NameOfStation != "new-name.plant1" || !e.stations[0].lastPerm {
		t.Fatalf("identity %+v %v", id, err)
	}
	suite := IPSuite{Addr: netip.MustParseAddr("10.1.2.3"), Mask: netip.MustParseAddr("255.255.0.0"), Gateway: netip.MustParseAddr("10.1.0.1")}
	if err := e.client.SetIP(ctx, mac, suite, false); err != nil {
		t.Fatal(err)
	}
	if got := e.stations[0].Identity().IP; got != suite || e.stations[0].lastPerm {
		t.Fatalf("ip %v", got)
	}
	if err := e.client.SetName(ctx, mac, "Bad_Name", true); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("err %v", err)
	}
	// A device rejects names the client did not validate.
	err = e.client.Set(ctx, mac, SetBlock{ID: DeviceName, Qualifier: QualifierPermanent, Data: []byte("port-001")})
	if !errors.As(err, &se) || se.Code != BlockErrorSetNotPossible || !errors.Is(err, profinet.ErrRejected) {
		t.Fatalf("err %v", err)
	}
	bad := IPSuite{Addr: netip.MustParseAddr("10.1.2.3"), Mask: netip.MustParseAddr("255.0.255.0"), Gateway: netip.MustParseAddr("0.0.0.0")}
	if err := e.client.SetIP(ctx, mac, bad, false); !errors.As(err, &se) {
		t.Fatalf("err %v", err)
	}
	e.stations[0].mu.Lock()
	e.stations[0].locked = true
	e.stations[0].mu.Unlock()
	if err := e.client.SetName(ctx, mac, "other", true); !errors.As(err, &se) || se.Code != BlockErrorInOperation {
		t.Fatalf("err %v", err)
	}
	if err := e.client.Signal(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if err := e.client.ResetToFactory(ctx, mac, ResetCommunication); err != nil {
		t.Fatal(err)
	}
	if err := e.client.ResetToFactory(ctx, mac, ResetDevice); !errors.As(err, &se) {
		t.Fatalf("err %v", err)
	}
	if e.stations[0].signals != 1 || len(e.stations[0].resets) != 2 {
		t.Fatalf("station %+v", e.stations[0])
	}
	// Set inside a Start/Stop transaction.
	err = e.client.Set(ctx, mac, SetBlock{ID: CtrlStart}, SetBlock{ID: CtrlSignal, Data: []byte{1, 0}}, SetBlock{ID: CtrlStop})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetTimeout(t *testing.T) {
	e := newEnv(t, 0)
	err := e.client.Signal(context.Background(), ethernet.MAC{2, 9, 9, 9, 9, 9})
	if !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.client.Signal(ctx, ethernet.MAC{2, 9, 9, 9, 9, 9}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestStationNames(t *testing.T) {
	good := []string{"a", "plc-1", "line1.cell2.robot-3", "port-1", "port-0012", "port-001x", "1.2.3", "xa--b"}
	bad := []string{"", "-a", "a-", "A", "a..b", "a_b", "1.2.3.4", "port-001", "port-001-00002", "port-001.x", "xn--abc", string(make([]byte, 241)), "a." + string(bytes.Repeat([]byte{'b'}, 64))}
	for _, n := range good {
		if err := ValidateStationName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateStationName(n); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestCodecRoundTrip(t *testing.T) {
	frames := []Frame{
		{FrameID: FrameIDIdentifyRequest, Service: ServiceIdentify, Type: TypeRequest, Xid: 1, ResponseDelay: 255, Blocks: []Block{{ID: AllSelector}}},
		{FrameID: FrameIDIdentifyRequest, Service: ServiceIdentify, Type: TypeRequest, Xid: 2, Blocks: []Block{{ID: DeviceName, Data: []byte("abc")}, {ID: DeviceID, Data: []byte{0, 1, 0, 2}}}},
		{FrameID: FrameIDGetSet, Service: ServiceGet, Type: TypeRequest, Xid: 3, Blocks: []Block{{ID: DeviceName}, {ID: IPParameter}}},
		{FrameID: FrameIDGetSet, Service: ServiceSet, Type: TypeRequest, Xid: 4, Blocks: []Block{{ID: DeviceName, HasInfo: true, Info: 1, Data: []byte("odd")}, {ID: CtrlStop, HasInfo: true}}},
		{FrameID: FrameIDGetSet, Service: ServiceSet, Type: TypeResponseSuccess, Xid: 5, Blocks: []Block{{ID: CtrlResponse, Data: []byte{2, 2, 0}}}},
		{FrameID: FrameIDHello, Service: ServiceHello, Type: TypeRequest, Xid: 6, Blocks: []Block{{ID: DeviceName, HasInfo: true, Data: []byte("x")}}},
	}
	for i, f := range frames {
		b, err := f.AppendTo(nil)
		if err != nil {
			t.Fatal(err)
		}
		g, err := Decode(b)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		b2, _ := g.AppendTo(nil)
		if !bytes.Equal(b, b2) {
			t.Fatalf("%d: %x != %x", i, b, b2)
		}
	}
	// Truncations never panic and fail.
	b, _ := frames[3].AppendTo(nil)
	for n := 0; n < len(b)-1; n++ {
		if _, err := Decode(b[:n]); err == nil && n < 12 {
			t.Fatalf("decoded %d octets", n)
		}
	}
}

func FuzzDecode(f *testing.F) {
	seeds := []Frame{
		{FrameID: FrameIDIdentifyResponse, Service: ServiceIdentify, Type: TypeResponseSuccess, Blocks: (&Identity{NameOfStation: "a", VendorID: 1}).Blocks()},
		{FrameID: FrameIDGetSet, Service: ServiceGet, Type: TypeRequest, Blocks: []Block{{ID: AllSelector}}},
	}
	for _, s := range seeds {
		b, _ := s.AppendTo(nil)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := Decode(b)
		if err != nil {
			return
		}
		_, _ = ParseIdentity(ethernet.MAC{}, fr.Blocks)
		out, err := fr.AppendTo(nil)
		if err != nil {
			return
		}
		if _, err := Decode(out); err != nil {
			t.Fatalf("re-decode: %v", err)
		}
	})
}
