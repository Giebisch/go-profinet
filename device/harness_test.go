// SPDX-License-Identifier: MIT

package device

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
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/netstack"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
	"github.com/otfabric/go-profinet/vnet"
)

var mask = netip.MustParseAddr("255.255.255.0")

const devIP = "192.168.0.10"

// testModel returns a device with an 8-octet input module in slot 1, a
// 4-octet output module with a writable and a read-only record in slot 2,
// a 2-in / 2-out module in slot 3 and a NO_IO module in slot 4.
func testModel() *model.Device {
	return &model.Device{
		Identity: model.Identity{
			VendorID: 0xF000, DeviceID: 0x0042, VendorName: "OT Fabric", TypeOfStation: "go-profinet device test",
			OrderID: "OTF-DEV-1", SerialNumber: "0001", HardwareRevision: 1,
			SoftwareRevision: pnio.SoftwareRevision{Prefix: 'V', FunctionalEnhance: 1},
		},
		Capabilities: model.Capabilities{MaxARs: 2, MinDeviceInterval: 32, ConformanceClass: "A", Ports: 1},
		DAP:          model.StandardDAP("DAP", 0x00000100, 1),
		Slots: []model.Slot{
			{Number: 1, Module: model.Module{Name: "DI8", IdentNumber: 0x10, Submodules: []model.Submodule{{Name: "DI8", IdentNumber: 0x11, Subslot: 1, InputLen: 8}}}},
			{Number: 2, Module: model.Module{Name: "DO4", IdentNumber: 0x20, Submodules: []model.Submodule{{Name: "DO4", IdentNumber: 0x21, Subslot: 1, OutputLen: 4,
				Records: []model.Record{
					{Index: 0x0010, Name: "Mode", Length: 2, Default: []byte{0, 1}},
					{Index: 0x0011, Name: "Fixed", Length: 1, ReadOnly: true},
				}}}}},
			{Number: 3, Module: model.Module{Name: "IO2", IdentNumber: 0x30, Submodules: []model.Submodule{{Name: "IO2", IdentNumber: 0x31, Subslot: 1, InputLen: 2, OutputLen: 2}}}},
			{Number: 4, Module: model.Module{Name: "NOIO", IdentNumber: 0x40, Submodules: []model.Submodule{{Name: "NOIO", IdentNumber: 0x41, Subslot: 1}}}},
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
	t.Cleanup(func() { n.net.Close() })
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
		st.Close()
		mux.Close()
		ep.Close()
	})
	return &node{ep: ep, mux: mux, stack: st}
}

func suite(ip string) dcp.IPSuite {
	return dcp.IPSuite{Addr: netip.MustParseAddr(ip), Mask: mask, Gateway: netip.MustParseAddr("0.0.0.0")}
}

// device starts a device "dut" at devIP with the test model.
func (n *tnet) device(h Handler, opts ...func(*Config)) (*Device, *node) {
	n.t.Helper()
	nd := n.node(devIP)
	cfg := Config{
		Model: testModel(), Mux: nd.mux, StationName: "dut", IP: suite(devIP), ListenPacket: nd.stack.ListenPacket,
		ApplyIP: func(s dcp.IPSuite, _ bool) error {
			if s.IsZero() {
				return nd.stack.SetAddr(netip.Addr{}, netip.Addr{}, netip.Addr{})
			}
			return nd.stack.SetAddr(s.Addr, s.Mask, s.Gateway)
		},
		Handler: h, NoResponseDelay: true, LLDPInterval: 100 * time.Millisecond,
		ApplicationReadyTimeout: 2 * time.Second,
	}
	for _, o := range opts {
		o(&cfg)
	}
	d, err := New(cfg)
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { d.Close() })
	return d, nd
}

// peer is a hand-made IO-Controller: an RPC client for the device
// interface, an RPC server for ApplicationReady and an alarm manager.
type peer struct {
	*node
	t      *testing.T
	d      *Device
	cli    *rpc.Client
	rc     *controller.RecordClient
	alarms *alarm.Manager

	mu       sync.Mutex
	onReady  func(cb pnio.ControlBlock) []byte // returns the whole NDR response body; nil answers OK
	readyCnt atomic.Int32
}

func (n *tnet) peer(d *Device, ip string, server bool) *peer {
	n.t.Helper()
	nd := n.node(ip)
	conn, err := nd.stack.ListenUDP(0)
	if err != nil {
		n.t.Fatal(err)
	}
	p := &peer{node: nd, t: n.t, d: d}
	p.cli = rpc.NewClient(conn, true, rpc.ClientOptions{Retransmit: 300 * time.Millisecond})
	n.t.Cleanup(func() { p.cli.Close() })
	p.rc = controller.NewRecordClient(p.cli, &net.UDPAddr{IP: net.ParseIP(devIP).To4(), Port: rpc.Port}, d.ObjectUUID())
	if p.alarms, err = alarm.NewManager(nd.mux); err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(p.alarms.Close)
	if server {
		sc, err := nd.stack.ListenPacket(context.Background(), "udp4", ":34964")
		if err != nil {
			n.t.Fatal(err)
		}
		srv := rpc.NewServer(sc, true, rpc.ServerOptions{})
		srv.Register(pnio.ControllerInterface, pnio.InterfaceVersion, rpc.HandlerFunc(p.serve))
		n.t.Cleanup(func() { srv.Close() })
	}
	return p
}

func (p *peer) setOnReady(f func(cb pnio.ControlBlock) []byte) {
	p.mu.Lock()
	p.onReady = f
	p.mu.Unlock()
}

func (p *peer) serve(_ context.Context, req *rpc.Request) ([]byte, error) {
	o := req.Order()
	argsMax, blocks, err := pnio.DecodeNDRRequest(req.Body, o)
	if err != nil {
		return nil, err
	}
	blk, _, err := pnio.ReadBlock(blocks)
	if err != nil {
		return nil, err
	}
	cb, err := pnio.DecodeControlBlock(blk)
	if err != nil {
		return nil, err
	}
	p.readyCnt.Add(1)
	p.mu.Lock()
	f := p.onReady
	p.mu.Unlock()
	if f != nil {
		if b := f(cb); b != nil {
			return b, nil
		}
	}
	res := cb.Response()
	return pnio.AppendNDRResponse(nil, o, pnio.Status{}, argsMax, res.Append(nil)), nil
}

func subIO(m *model.Device) []rt.SubmoduleIO {
	var out []rt.SubmoduleIO
	m.Each(func(p model.PluggedSubmodule) {
		out = append(out, rt.SubmoduleIO{API: model.API, Slot: p.Slot, Subslot: p.Submodule.Subslot, Type: p.Submodule.Type(), InputLen: p.Submodule.InputLen, OutputLen: p.Submodule.OutputLen})
	})
	return out
}

// ioCAR returns a valid IOCAR Connect request for the test model with a
// long cycle and data hold time, so that no output frames are needed.
func (p *peer) ioCAR() *pnio.ConnectRequest {
	p.t.Helper()
	m := testModel()
	subs := subIO(m)
	inL, err := rt.ComputeLayout(pnio.IOCRInput, subs)
	if err != nil {
		p.t.Fatal(err)
	}
	outL, err := rt.ComputeLayout(pnio.IOCROutput, subs)
	if err != nil {
		p.t.Fatal(err)
	}
	iocr := func(t pnio.IOCRType, frameID uint16, l rt.Layout, ref uint16) pnio.IOCRBlockReq {
		return pnio.IOCRBlockReq{
			Type: t, Reference: ref, LT: pnio.LT, Properties: uint32(pnio.RTClass1), DataLength: l.DataLength, FrameID: frameID,
			SendClockFactor: 32, ReductionRatio: 128, Phase: 1, FrameSendOffset: 0xFFFFFFFF,
			WatchdogFactor: 3, DataHoldFactor: 0x1E00, TagHeader: 6 << 13, APIs: l.IOCRAPIs(),
		}
	}
	return &pnio.ConnectRequest{
		AR: pnio.ARBlockReq{
			Type: pnio.ARTypeIOCARSingle, ARUUID: rpc.NewRandomUUID(), SessionKey: 7,
			InitiatorMAC: p.mux.HardwareAddr(), InitiatorObject: pnio.ObjectUUID(1, 1, 0xF000),
			Properties: pnio.ARPropStateActive | pnio.ARPropPrmServerCMInit, ActivityTimeoutFactor: 200,
			UDPRTPort: pnio.LT, StationName: "plc",
		},
		IOCRs: []pnio.IOCRBlockReq{iocr(pnio.IOCRInput, 0xC000, inL, 1), iocr(pnio.IOCROutput, pnio.FrameIDAssign, outL, 2)},
		AlarmCR: &pnio.AlarmCRBlockReq{
			Type: 1, LT: pnio.LT, RTATimeoutFactor: 5, RTARetries: 3,
			LocalAlarmReference: p.alarms.AllocRef(), MaxAlarmDataLength: 200, TagHeaderHigh: 6 << 13, TagHeaderLow: 5 << 13,
		},
		Expected: m.Expected(),
	}
}

// ioSAR returns a device access Connect request.
func (p *peer) ioSAR() *pnio.ConnectRequest {
	return &pnio.ConnectRequest{AR: pnio.ARBlockReq{
		Type: pnio.ARTypeIOSAR, ARUUID: rpc.NewRandomUUID(), SessionKey: 1, InitiatorMAC: p.mux.HardwareAddr(),
		InitiatorObject: pnio.ObjectUUID(1, 2, 3), Properties: pnio.ARPropStateActive | pnio.ARPropDeviceAccess,
		ActivityTimeoutFactor: 200, UDPRTPort: pnio.LT, StationName: "tool",
	}}
}

func (p *peer) connect(req *pnio.ConnectRequest) (*pnio.ConnectResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.rc.Connect(ctx, req)
}

func (p *peer) control(req *pnio.ConnectRequest, typ pnio.BlockType, cmd uint16) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.rc.Control(ctx, pnio.ControlBlock{Type: typ, ARUUID: req.AR.ARUUID, SessionKey: req.AR.SessionKey, Command: cmd})
}

func (p *peer) prmEnd(req *pnio.ConnectRequest) error {
	return p.control(req, pnio.BlockPrmEndReq, pnio.CmdPrmEnd)
}

func (p *peer) release(req *pnio.ConnectRequest) error {
	return p.control(req, pnio.BlockReleaseReq, pnio.CmdRelease)
}

// openAlarms opens the controller side of the alarm CR; onData receives
// the notifications.
func (p *peer) openAlarms(req *pnio.ConnectRequest, resp *pnio.ConnectResponse, onData func(high bool, n pnio.AlarmNotification, ep *alarm.Endpoint)) (hi, lo *alarm.Endpoint) {
	p.t.Helper()
	base := alarm.EndpointConfig{
		LocalRef: req.AlarmCR.LocalAlarmReference, RemoteRef: resp.AlarmCR.LocalAlarmReference, RemoteMAC: resp.AR.ResponderMAC,
		Timeout: 200 * time.Millisecond, Retries: 3,
	}
	open := func(high bool) *alarm.Endpoint {
		cfg := base
		cfg.High = high
		var ep *alarm.Endpoint
		var once sync.WaitGroup
		once.Add(1)
		cfg.OnData = func(b []byte) {
			once.Wait()
			blk, _, err := pnio.ReadBlock(b)
			if err != nil {
				return
			}
			n, err := pnio.DecodeAlarmNotification(blk)
			if err != nil {
				return
			}
			if onData != nil {
				onData(high, n, ep)
			}
		}
		var err error
		if ep, err = p.alarms.Open(cfg); err != nil {
			p.t.Fatal(err)
		}
		once.Done()
		p.t.Cleanup(ep.Close)
		return ep
	}
	return open(true), open(false)
}

// establish connects an IOCAR and ends its parameterization; it returns
// once the device has the AR in Data.
func (p *peer) establish(req *pnio.ConnectRequest) *pnio.ConnectResponse {
	p.t.Helper()
	resp, err := p.connect(req)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := p.prmEnd(req); err != nil {
		p.t.Fatal(err)
	}
	waitAR(p.t, p.d, req.AR.ARUUID, ARStateData)
	return resp
}

func arState(d *Device, u rpc.UUID) (ARState, bool) {
	for _, a := range d.ARs() {
		if a.UUID == u {
			return a.State, true
		}
	}
	return ARStateClosed, false
}

func waitAR(t *testing.T, d *Device, u rpc.UUID, s ARState) {
	t.Helper()
	eventually(t, 5*time.Second, "AR in "+s.String(), func() bool {
		st, ok := arState(d, u)
		if s == ARStateClosed {
			return !ok
		}
		return ok && st == s
	})
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

// wantStatus checks that err is a *pnio.StatusError with the codes.
func wantStatus(t *testing.T, err error, code1, code2 uint8) {
	t.Helper()
	var se *pnio.StatusError
	if !errors.As(err, &se) || se.Status.Code1 != code1 || se.Status.Code2 != code2 {
		t.Fatalf("err %v, want code1 0x%02X code2 0x%02X", err, code1, code2)
	}
}

// recHandler records the events of the device.
type recHandler struct {
	NopHandler
	mu        sync.Mutex
	trans     map[rpc.UUID][]ARState
	reasons   map[rpc.UUID]pnio.AbortReason
	acks      []pnio.AlarmAck
	names     []string
	ips       []dcp.IPSuite
	signals   int
	resetCode dcp.BlockError
	prmEnd    func(ctx context.Context, ar ARInfo) error
	read      func(r RecordRequest) ([]byte, error)
	write     func(r RecordRequest, data []byte) error
}

func newRecHandler() *recHandler {
	return &recHandler{trans: map[rpc.UUID][]ARState{}, reasons: map[rpc.UUID]pnio.AbortReason{}}
}

func (h *recHandler) StateChanged(ar ARInfo, from, to ARState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.trans[ar.UUID]) == 0 {
		h.trans[ar.UUID] = append(h.trans[ar.UUID], from)
	}
	h.trans[ar.UUID] = append(h.trans[ar.UUID], to)
	if to == ARStateClosed {
		h.reasons[ar.UUID] = ar.CloseReason
	}
}

func (h *recHandler) states(u rpc.UUID) []ARState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ARState(nil), h.trans[u]...)
}

func (h *recHandler) reason(u rpc.UUID) (pnio.AbortReason, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.reasons[u]
	return r, ok
}

func (h *recHandler) waitReason(t *testing.T, u rpc.UUID, want pnio.AbortReason) {
	t.Helper()
	eventually(t, 5*time.Second, "AR close reason "+want.String(), func() bool {
		r, ok := h.reason(u)
		return ok && r == want
	})
}

func (h *recHandler) AlarmAck(_ ARInfo, ack pnio.AlarmAck) {
	h.mu.Lock()
	h.acks = append(h.acks, ack)
	h.mu.Unlock()
}

func (h *recHandler) Signal() {
	h.mu.Lock()
	h.signals++
	h.mu.Unlock()
}

func (h *recHandler) ResetToFactory(uint16) dcp.BlockError {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.resetCode
}

func (h *recHandler) NameChanged(name string, _ bool) {
	h.mu.Lock()
	h.names = append(h.names, name)
	h.mu.Unlock()
}

func (h *recHandler) IPChanged(s dcp.IPSuite, _ bool) {
	h.mu.Lock()
	h.ips = append(h.ips, s)
	h.mu.Unlock()
}

func (h *recHandler) PrmEnd(ctx context.Context, ar ARInfo) error {
	h.mu.Lock()
	f := h.prmEnd
	h.mu.Unlock()
	if f != nil {
		return f(ctx, ar)
	}
	return nil
}

func (h *recHandler) ReadRecord(ctx context.Context, r RecordRequest) ([]byte, error) {
	if h.read != nil {
		return h.read(r)
	}
	return h.NopHandler.ReadRecord(ctx, r)
}

func (h *recHandler) WriteRecord(ctx context.Context, r RecordRequest, data []byte) error {
	if h.write != nil {
		return h.write(r, data)
	}
	return h.NopHandler.WriteRecord(ctx, r, data)
}
