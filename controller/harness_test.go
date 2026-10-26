// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/alarm"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/netstack"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/vnet"
)

var mask = netip.MustParseAddr("255.255.255.0")

const (
	ctrlIP = "192.168.0.1"
	devIP  = "192.168.0.10"
)

// testModel returns a device with an 8-octet input module in slot 1, a
// 4-octet output module with a parameter record in slot 2 and a 2-in /
// 2-out module in slot 3.
func testModel() *model.Device {
	return &model.Device{
		Identity: model.Identity{
			VendorID: 0xF000, DeviceID: 0x0042, VendorName: "OT Fabric", TypeOfStation: "go-profinet controller test",
			OrderID: "OTF-CTL-1", SerialNumber: "0001", HardwareRevision: 1,
		},
		Capabilities: model.Capabilities{MaxARs: 2, MinDeviceInterval: 32, ConformanceClass: "A", Ports: 1},
		DAP:          model.StandardDAP("DAP", 0x00000100, 1),
		Slots: []model.Slot{
			{Number: 1, Module: model.Module{Name: "DI8", IdentNumber: 0x10, Submodules: []model.Submodule{{Name: "DI8", IdentNumber: 0x11, Subslot: 1, InputLen: 8}}}},
			{Number: 2, Module: model.Module{Name: "DO4", IdentNumber: 0x20, Submodules: []model.Submodule{{Name: "DO4", IdentNumber: 0x21, Subslot: 1, OutputLen: 4,
				Records: []model.Record{{Index: 0x0010, Name: "Mode", Length: 2, Default: []byte{0, 1}}}}}}},
			{Number: 3, Module: model.Module{Name: "IO2", IdentNumber: 0x30, Submodules: []model.Submodule{{Name: "IO2", IdentNumber: 0x31, Subslot: 1, InputLen: 2, OutputLen: 2}}}},
		},
	}
}

type node struct {
	ep    *vnet.Endpoint
	mux   *l2.Mux
	stack *netstack.Stack
}

type tnet struct {
	t   *testing.T
	net *vnet.Network
	n   atomic.Uint32
}

func newNet(t *testing.T) *tnet {
	t.Helper()
	n := &tnet{t: t, net: vnet.New(1)}
	t.Cleanup(func() { _ = n.net.Close() })
	return n
}

func (n *tnet) node(ip string) *node {
	n.t.Helper()
	i := n.n.Add(1)
	ep, err := n.net.NewEndpoint(vnet.EndpointConfig{Name: ip, MAC: ethernet.MAC{0x02, 0, 0, 0, 0, byte(i)}})
	if err != nil {
		n.t.Fatal(err)
	}
	mux := l2.NewMux(ep)
	cfg := netstack.Config{}
	if ip != "" {
		cfg.Addr, cfg.Mask = netip.MustParseAddr(ip), mask
	}
	st, err := netstack.New(mux, cfg)
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() {
		_ = st.Close()
		_ = mux.Close()
		_ = ep.Close()
	})
	return &node{ep: ep, mux: mux, stack: st}
}

func suite(ip string) dcp.IPSuite {
	return dcp.IPSuite{Addr: netip.MustParseAddr(ip), Mask: mask, Gateway: netip.MustParseAddr("0.0.0.0")}
}

func (n *tnet) controller(opts ...func(*Config)) (*Controller, *node) {
	n.t.Helper()
	nd := n.node(ctrlIP)
	cfg := Config{Mux: nd.mux, ListenPacket: nd.stack.ListenPacket, StationName: "plc", DCP: dcp.ClientOptions{Timeout: 300 * time.Millisecond}}
	for _, o := range opts {
		o(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { _ = c.Close() })
	return c, nd
}

// device starts a real IO-Device "dut".
func (n *tnet) device(ip string, opts ...func(*device.Config)) (*device.Device, *node) {
	n.t.Helper()
	nd := n.node(ip)
	cfg := device.Config{
		Model: testModel(), Mux: nd.mux, StationName: "dut", ListenPacket: nd.stack.ListenPacket,
		ApplyIP:         func(s dcp.IPSuite, _ bool) error { return nd.stack.SetAddr(s.Addr, s.Mask, s.Gateway) },
		NoResponseDelay: true, DisableLLDP: true,
	}
	if ip != "" {
		cfg.IP = suite(ip)
	}
	for _, o := range opts {
		o(&cfg)
	}
	d, err := device.New(cfg)
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { _ = d.Close() })
	return d, nd
}

// fakeDev is a hand-made IO-Device: a DCP responder and an RPC server
// whose answers the test crafts.
type fakeDev struct {
	*node
	t   *testing.T
	cli *rpc.Client
	am  *alarm.Manager

	mu       sync.Mutex
	identity dcp.Identity
	// raw answers a call with a whole NDR response body; nil falls
	// through to the defaults.
	raw func(op pnio.OpNum, blocks []byte) []byte
	// connect edits the default Connect response; a non-OK status
	// rejects the Connect.
	connect func(req *pnio.ConnectRequest, resp *pnio.ConnectResponse) pnio.Status
	// control answers PrmEnd and Release; nil answers OK.
	control  func(cb pnio.ControlBlock) pnio.Status
	requests chan *pnio.ConnectRequest
}

func (n *tnet) fake(name string) *fakeDev {
	n.t.Helper()
	nd := n.node(devIP)
	f := &fakeDev{node: nd, t: n.t, requests: make(chan *pnio.ConnectRequest, 16)}
	f.identity = dcp.Identity{NameOfStation: name, VendorID: 0xF000, DeviceID: 0x0042, Role: dcp.RoleIODevice, IP: suite(devIP), IPInfo: dcp.IPInfoSet}
	r, err := dcp.NewResponder(nd.mux, fakeStation{f}, dcp.ResponderOptions{NoResponseDelay: true})
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { _ = r.Close() })
	sc, err := nd.stack.ListenPacket(context.Background(), "udp4", ":34964")
	if err != nil {
		n.t.Fatal(err)
	}
	srv := rpc.NewServer(sc, true, rpc.ServerOptions{})
	srv.Register(pnio.DeviceInterface, pnio.InterfaceVersion, rpc.HandlerFunc(f.serve))
	n.t.Cleanup(func() { _ = srv.Close() })
	cc, err := nd.stack.ListenUDP(0)
	if err != nil {
		n.t.Fatal(err)
	}
	f.cli = rpc.NewClient(cc, true, rpc.ClientOptions{})
	n.t.Cleanup(func() { _ = f.cli.Close() })
	if f.am, err = alarm.NewManager(nd.mux); err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(f.am.Close)
	return f
}

func (f *fakeDev) set(fn func(f *fakeDev)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

type fakeStation struct{ f *fakeDev }

func (s fakeStation) Identity() dcp.Identity {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return s.f.identity
}

func (s fakeStation) SetName(name string, _ bool) dcp.BlockError {
	s.f.mu.Lock()
	s.f.identity.NameOfStation = name
	s.f.mu.Unlock()
	return dcp.BlockErrorNone
}

func (s fakeStation) SetIP(ip dcp.IPSuite, _ bool) dcp.BlockError {
	s.f.mu.Lock()
	s.f.identity.IP = ip
	s.f.mu.Unlock()
	return dcp.BlockErrorNone
}

func (fakeStation) Signal() dcp.BlockError               { return dcp.BlockErrorNone }
func (fakeStation) ResetToFactory(uint16) dcp.BlockError { return dcp.BlockErrorNone }

func (f *fakeDev) serve(_ context.Context, req *rpc.Request) ([]byte, error) {
	o := req.Order()
	argsMax, blocks, err := pnio.DecodeNDRRequest(req.Body, o)
	if err != nil {
		return nil, err
	}
	op := pnio.OpNum(req.Header.OpNum)
	f.mu.Lock()
	raw, connect, control := f.raw, f.connect, f.control
	f.mu.Unlock()
	if raw != nil {
		if b := raw(op, blocks); b != nil {
			return b, nil
		}
	}
	switch op {
	case pnio.OpConnect:
		creq, err := pnio.DecodeConnectRequest(blocks)
		if err != nil {
			return nil, err
		}
		select {
		case f.requests <- &creq:
		default:
		}
		resp := &pnio.ConnectResponse{AR: pnio.ARBlockRes{Type: creq.AR.Type, ARUUID: creq.AR.ARUUID, SessionKey: creq.AR.SessionKey, ResponderMAC: f.mux.HardwareAddr(), UDPRTPort: pnio.LT}}
		for _, c := range creq.IOCRs {
			id := c.FrameID
			if c.Type == pnio.IOCROutput {
				id = 0xC100
			}
			resp.IOCRs = append(resp.IOCRs, pnio.IOCRBlockRes{Type: c.Type, Reference: c.Reference, FrameID: id})
		}
		resp.AlarmCR = &pnio.AlarmCRBlockRes{Type: 1, LocalAlarmReference: 0x77, MaxAlarmDataLength: 200}
		st := pnio.Status{}
		if connect != nil {
			st = connect(&creq, resp)
		}
		if !st.OK() {
			return pnio.AppendNDRResponse(nil, o, st, argsMax, nil), nil
		}
		return pnio.AppendNDRResponse(nil, o, pnio.Status{}, argsMax, resp.Append(nil)), nil
	case pnio.OpControl, pnio.OpRelease:
		blk, _, err := pnio.ReadBlock(blocks)
		if err != nil {
			return nil, err
		}
		cb, err := pnio.DecodeControlBlock(blk)
		if err != nil {
			return nil, err
		}
		if control != nil {
			if st := control(cb); !st.OK() {
				return pnio.AppendNDRResponse(nil, o, st, argsMax, nil), nil
			}
		}
		res := cb.Response()
		return pnio.AppendNDRResponse(nil, o, pnio.Status{}, argsMax, res.Append(nil)), nil
	default:
	}
	return nil, &rpc.Fault{Reject: true, Status: rpc.StatusOpRangeError}
}

// appReady sends ApplicationReady to the controller.
func (f *fakeDev) appReady(c *Controller, port int, cb pnio.ControlBlock) (pnio.Status, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body := pnio.AppendNDRRequest(nil, order, pnio.DefaultArgsMaximum, cb.Append(nil))
	resp, err := f.cli.Do(ctx, &net.UDPAddr{IP: net.ParseIP(ctrlIP).To4(), Port: port}, rpc.Call{Object: c.ObjectUUID(), Iface: pnio.ControllerInterface, IfaceVersion: pnio.InterfaceVersion, OpNum: uint16(pnio.OpControl), Body: body})
	if err != nil {
		return pnio.Status{}, nil, err
	}
	return pnio.DecodeNDRResponse(resp.Body, resp.Header.Order())
}

func waitState(t *testing.T, d *Device, s State, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := d.WaitState(ctx, s); err != nil {
		st, last := d.State()
		t.Fatalf("%s: waiting for %s: in %s (last error %v)", d.Name(), s, st, last)
	}
}

// waitErr waits for a failure of the device's lifecycle and returns it.
func waitErr(t *testing.T, d *Device) error {
	t.Helper()
	var err error
	eventually(t, 10*time.Second, "a failure", func() bool {
		_, err = d.State()
		return err != nil
	})
	return err
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func wantStatus(t *testing.T, err error, code1, code2 uint8) {
	t.Helper()
	var se *pnio.StatusError
	if !errors.As(err, &se) || se.Status.Code1 != code1 || se.Status.Code2 != code2 {
		t.Fatalf("err %v, want code1 0x%02X code2 0x%02X", err, code1, code2)
	}
}
