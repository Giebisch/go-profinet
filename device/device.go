// SPDX-License-Identifier: MIT

// Package device implements a PROFINET IO-Device (conformance class A,
// RT_CLASS_1/2): the DCP responder, the RPC server for Connect, Read,
// Write, Control and Release, application relationships with their
// explicit state machine, cyclic input production and output consumption,
// alarms, diagnosis and identification and maintenance records.
//
// The application describes the device with a [model.Device], provides
// input data with [Device.SetInput], reads output data with
// [Device.Output] and answers its own records through a [Handler]. Process
// data and protocol processing are kept apart: the device copies data
// into and out of the cyclic frames on its own goroutines.
package device

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/alarm"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/bridge"
	"github.com/otfabric/go-profinet/lldp"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
)

// ListenPacketFunc opens a UDP socket; net.ListenConfig.ListenPacket and
// netstack.Stack.ListenPacket have this signature.
type ListenPacketFunc func(ctx context.Context, network, address string) (net.PacketConn, error)

// IMData are the writable I&M records of the device (I&M1 to I&M4).
type IMData struct {
	IM1 pnio.IM1
	IM2 pnio.IM2
	IM3 pnio.IM3
	IM4 pnio.IM4
}

// Config configures a [Device].
type Config struct {
	// Model describes the device; required.
	Model *model.Device
	// Mux is the Layer 2 port; required. With a Bridge it is the mux of
	// the bridge's CPU port (the device's interface).
	Mux *l2.Mux
	// Bridge makes the device a multiport device: each bridge port is a
	// device port (port submodules 0x8001, 0x8002, ... of the model's
	// DAP, which must declare them), with its own MAC address, LLDP agent
	// and link state. nil: the device has one port, Mux's. The caller
	// owns the bridge and closes it after the device.
	Bridge *bridge.Bridge
	// StationName and IP are the initial station name and IP suite.
	StationName string
	IP          dcp.IPSuite
	// ListenPacket opens the RPC sockets. nil uses the operating system's
	// UDP stack.
	ListenPacket ListenPacketFunc
	// ApplyIP applies a DCP Set of the IP suite to the IP stack the RPC
	// sockets use. nil only records the new suite (and reports it in
	// DCP); an application on a real interface configures the operating
	// system itself (Handler.IPChanged is called either way).
	ApplyIP func(s dcp.IPSuite, permanent bool) error
	// RPCPort is the port of the RPC server. Zero means 34964.
	RPCPort int
	// ControllerRPCPort is the controller's port that ApplicationReady is
	// sent to when the Connect request has no ARRPCBlockReq. Zero means
	// 34964.
	ControllerRPCPort int
	// Handler receives the events and records the device does not handle
	// itself. nil uses NopHandler.
	Handler Handler
	// IM holds the initial I&M1 to I&M4 records.
	IM IMData
	// RejectSupervisorAR refuses IOSAR (device access) connections.
	RejectSupervisorAR bool
	// DisableLLDP stops the device from sending LLDP.
	DisableLLDP bool
	// LLDPInterval is the LLDP transmit interval. Zero means 5 s.
	LLDPInterval time.Duration
	// NoResponseDelay answers DCP Identify at once (for tests).
	NoResponseDelay bool
	// Hello makes the device announce itself with DCP Hello requests, so
	// that its IO controller can connect without an Identify: a sequence
	// at start and, with a port that reports its link state
	// (l2.LinkStater), whenever the link comes up, while no IO controller
	// is connected. A DCP Set and a Connect end the sequence. The device's
	// DeviceInitiative then reports Hello. Zero (dcp.HelloOff) sends none.
	Hello dcp.HelloMode
	// HelloOptions configure each sequence: the number of Hellos and
	// their interval, and the delay after the link came up in mode
	// dcp.HelloOnLinkUpDelayed. Zero values are the defaults of
	// dcp.HelloOptions.
	HelloOptions dcp.HelloOptions
	// DHCP are the DHCP options (DCP option 3) the device reports in
	// Identify and Get responses, for example dcp.DHCPClientIDFromMAC()
	// and dcp.DHCPControlParameter(dcp.DHCPControlOn). The library has no
	// DHCP client: addressing stays with the operating system, and the
	// device only reports what the application configures.
	DHCP []dcp.DHCPParameter
	// IPFromDHCP reports the initial IP suite as set by DHCP (IP
	// BlockInfo 0x0002) until a DCP Set changes it.
	IPFromDHCP bool
	// SetDHCP receives a DCP Set of a DHCP option; when it returns
	// dcp.BlockErrorNone the device reports the new value. nil refuses
	// every such Set with "option not supported".
	SetDHCP func(p dcp.DHCPParameter, permanent bool) dcp.BlockError
	// DCPPolicy restricts the DCP responder: a read-only mode (Set
	// refused, Identify and Get answered), rate limits per source for
	// Identify responses and Set requests, and audit events for every Set,
	// ResetToFactory and refused request. The zero value restricts nothing.
	DCPPolicy dcp.Policy
	// ApplicationReadyTimeout bounds the ApplicationReady call. Zero
	// means 6 s (p-net's 3 tries of 2 s).
	ApplicationReadyTimeout time.Duration
	// Logger receives diagnostics. nil logs nothing.
	Logger profinet.Logger
}

type key struct {
	slot, subslot uint16
}

type ioData struct {
	data []byte
	iops uint8
}

// Device is a PROFINET IO-Device. Its methods are safe for concurrent use.
type Device struct {
	cfg     Config
	log     profinet.Logger
	model   *model.Device
	handler Handler
	object  rpc.UUID

	mux    *l2.Mux
	dcpR   *dcp.Responder
	alarms *alarm.Manager
	disp   *rt.Dispatcher
	sched  *rt.Scheduler
	ports  []*port
	rpcSrv *rpc.Server
	rpcCli *rpc.Client

	// mu guards the station state and the ARs.
	mu   sync.Mutex
	name string
	ip   dcp.IPSuite
	ars  map[rpc.UUID]*ar
	// reserved counts the Connects in progress, reservedIO those of
	// IOCARs; arSeq numbers the Connects.
	reserved   int
	reservedIO int
	arSeq      uint64
	frameIDs   map[uint16]bool
	records    map[recKey][]byte
	im         IMData
	diags      map[key][]pnio.DiagnosisItem
	closed     bool
	pd         pdevState

	// pdPoke wakes the PDev check evaluator.
	pdPoke chan struct{}

	// dhcp are the DHCP options DCP reports; ipDHCP marks ip as set by
	// DHCP. Both under mu.
	dhcp   []dcp.DHCPParameter
	ipDHCP bool

	// imgMu guards the process data images; it is never held while
	// another lock is taken.
	imgMu   sync.RWMutex
	inputs  map[key]*ioData
	outputs map[key]*ioData
	// owner is the IOCAR that owns each submodule and claimers the IOCARs
	// that may (shared device, owner.go).
	owner    map[key]*ar
	claimers map[*ar]bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type recKey struct {
	slot, subslot uint16
	index         pnio.Index
}

// New starts an IO-Device: the DCP responder, LLDP, the alarm and cyclic
// machinery and the RPC server.
func New(cfg Config) (*Device, error) {
	if cfg.Model == nil || cfg.Mux == nil {
		return nil, fmt.Errorf("device: %w: Model and Mux are required", profinet.ErrInvalidConfig)
	}
	if err := cfg.Model.Validate(); err != nil {
		return nil, err
	}
	if cfg.StationName != "" {
		if err := dcp.ValidateStationName(cfg.StationName); err != nil {
			return nil, err
		}
	}
	if err := validateDHCP(cfg.DHCP); err != nil {
		return nil, err
	}
	if cfg.Hello > dcp.HelloOnLinkUpDelayed {
		return nil, fmt.Errorf("device: %w: Hello mode %s", profinet.ErrInvalidConfig, cfg.Hello)
	}
	if cfg.Hello != dcp.HelloOff {
		opts, err := cfg.HelloOptions.Normalize()
		if err != nil {
			return nil, err
		}
		if cfg.Hello == dcp.HelloOnLinkUp {
			opts.Delay = 0
		}
		cfg.HelloOptions = opts
	}
	if cfg.RPCPort == 0 {
		cfg.RPCPort = rpc.Port
	}
	if cfg.ControllerRPCPort == 0 {
		cfg.ControllerRPCPort = rpc.Port
	}
	if cfg.ApplicationReadyTimeout <= 0 {
		cfg.ApplicationReadyTimeout = 6 * time.Second
	}
	if cfg.ListenPacket == nil {
		lc := &net.ListenConfig{}
		cfg.ListenPacket = lc.ListenPacket
	}
	if cfg.Model.Capabilities.MaxARs == 0 {
		cfg.Model.Capabilities.MaxARs = 2
	}
	log := cfg.Logger
	if log == nil {
		log = profinet.NopLogger()
	}
	h := cfg.Handler
	if h == nil {
		h = NopHandler{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Device{
		cfg:      cfg,
		log:      log,
		model:    cfg.Model,
		handler:  h,
		object:   pnio.ObjectUUID(max(cfg.Model.Identity.Instance, 1), cfg.Model.Identity.DeviceID, cfg.Model.Identity.VendorID),
		mux:      cfg.Mux,
		name:     cfg.StationName,
		ip:       cfg.IP,
		dhcp:     cloneDHCP(cfg.DHCP),
		ipDHCP:   cfg.IPFromDHCP,
		ars:      map[rpc.UUID]*ar{},
		frameIDs: map[uint16]bool{},
		records:  map[recKey][]byte{},
		im:       cfg.IM,
		diags:    map[key][]pnio.DiagnosisItem{},
		pd:       pdevState{checks: map[uint16]*pnio.PDPortDataCheck{}, adjusts: map[uint16]*pnio.PDPortDataAdjust{}, active: map[uint16][]uint16{}},
		pdPoke:   make(chan struct{}, 1),
		inputs:   map[key]*ioData{},
		owner:    map[key]*ar{},
		claimers: map[*ar]bool{},
		outputs:  map[key]*ioData{},
		ctx:      ctx,
		cancel:   cancel,
	}
	d.model.Each(func(p model.PluggedSubmodule) {
		k := key{p.Slot, p.Submodule.Subslot}
		t := p.Submodule.Type()
		if t == pnio.SubmoduleNoIO || t.HasInput() {
			d.inputs[k] = &ioData{data: make([]byte, p.Submodule.InputLen), iops: pnio.IOxSGood}
		}
		if t.HasOutput() {
			d.outputs[k] = &ioData{data: make([]byte, p.Submodule.OutputLen), iops: pnio.IOxSBad}
		}
		for _, r := range p.Submodule.Records {
			v := r.Default
			if v == nil {
				v = make([]byte, r.Length)
			}
			d.records[recKey{p.Slot, p.Submodule.Subslot, r.Index}] = append([]byte(nil), v...)
		}
	})
	if err := d.start(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func (d *Device) start() error {
	var err error
	d.sched = rt.NewScheduler()
	if d.alarms, err = alarm.NewManager(d.mux); err != nil {
		return err
	}
	if d.disp, err = rt.NewDispatcher(d.mux); err != nil {
		return err
	}
	if err := d.openPorts(); err != nil {
		return err
	}
	// LLDP before DCP: the DCP responder reads its alias.
	if !d.cfg.DisableLLDP {
		if err := d.startLLDP(); err != nil {
			return err
		}
	}
	cconn, err := d.cfg.ListenPacket(d.ctx, "udp4", ":0")
	if err != nil {
		return fmt.Errorf("device: RPC client socket: %w", err)
	}
	d.rpcCli = rpc.NewClient(cconn, true, rpc.ClientOptions{Logger: d.log, Retransmit: 2 * time.Second, Activities: 2})
	conn, err := d.cfg.ListenPacket(d.ctx, "udp4", ":"+strconv.Itoa(d.cfg.RPCPort))
	if err != nil {
		return fmt.Errorf("device: RPC socket: %w", err)
	}
	d.rpcSrv = rpc.NewServer(conn, true, rpc.ServerOptions{Logger: d.log})
	d.rpcSrv.Register(pnio.DeviceInterface, pnio.InterfaceVersion, rpc.HandlerFunc(d.serveRPC))
	// DCP last: once the device can be found, everything a controller
	// talks to is in place.
	r, err := dcp.NewResponder(d.mux, station{d}, dcp.ResponderOptions{Logger: d.log, NoResponseDelay: d.cfg.NoResponseDelay, Policy: d.cfg.DCPPolicy})
	if err != nil {
		return err
	}
	// Under mu: the RPC server already runs, and a Connect reads it.
	d.mu.Lock()
	d.dcpR = r
	d.mu.Unlock()
	d.wg.Add(1)
	go d.evalPDev()
	if d.hasLinkState() {
		d.wg.Add(1)
		go d.watchLinks()
	}
	if d.cfg.Hello != dcp.HelloOff {
		d.startHello()
	}
	return nil
}

// helloLinkPoll is how often the device polls the link state of its port
// (Hello on link up, port data, port checks).
const helloLinkPoll = 50 * time.Millisecond

// startHello starts a Hello sequence unless an IO controller is
// connected or the link is down.
func (d *Device) startHello() {
	if !d.anyLinkUp() {
		return
	}
	d.mu.Lock()
	busy := d.hasIOAR() || d.reservedIO > 0 || d.closed || d.pd.helloBlocked(d.portSubslots())
	d.mu.Unlock()
	if busy {
		return
	}
	if err := d.dcpR.StartHello(d.cfg.HelloOptions); err != nil {
		d.log.Debugf("device: Hello: %v", err)
	}
}

// Close aborts every AR and stops the device.
func (d *Device) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	ars := make([]*ar, 0, len(d.ars))
	for _, a := range d.ars {
		ars = append(ars, a)
	}
	d.mu.Unlock()
	for _, a := range ars {
		d.abort(a, pnio.AbortDeactivated, true)
	}
	d.cancel()
	if d.rpcSrv != nil {
		_ = d.rpcSrv.Close()
	}
	if d.rpcCli != nil {
		_ = d.rpcCli.Close()
	}
	d.closePorts()
	if d.dcpR != nil {
		_ = d.dcpR.Close()
	}
	if d.disp != nil {
		d.disp.Close()
	}
	if d.alarms != nil {
		d.alarms.Close()
	}
	d.wg.Wait()
	if d.sched != nil {
		d.sched.Close()
	}
	return nil
}

// ObjectUUID returns the device's PNIO object UUID.
func (d *Device) ObjectUUID() rpc.UUID { return d.object }

// RPCAddr returns the address of the RPC server's socket.
func (d *Device) RPCAddr() net.Addr { return d.rpcSrv.LocalAddr() }

// StationName returns the current station name.
func (d *Device) StationName() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.name
}

// IP returns the current IP suite.
func (d *Device) IP() dcp.IPSuite {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ip
}

// SetInput sets the input data and provider status of a submodule; the
// next cyclic frame of every AR carries it. data must have the
// submodule's input length.
func (d *Device) SetInput(slot, subslot uint16, data []byte, iops uint8) error {
	d.imgMu.Lock()
	defer d.imgMu.Unlock()
	in, ok := d.inputs[key{slot, subslot}]
	if !ok {
		return fmt.Errorf("device: input %d/%d: %w", slot, subslot, profinet.ErrNotFound)
	}
	if len(data) != len(in.data) {
		return fmt.Errorf("device: input %d/%d: %w: %d octets instead of %d", slot, subslot, profinet.ErrInvalidConfig, len(data), len(in.data))
	}
	copy(in.data, data)
	in.iops = iops
	return nil
}

// Output returns a copy of the output data of a submodule and its provider
// status as the controller last sent them. ok is false for a submodule
// without outputs. Until the first frame the data is zero and the status
// bad.
func (d *Device) Output(slot, subslot uint16) (data []byte, iops uint8, ok bool) {
	d.imgMu.RLock()
	defer d.imgMu.RUnlock()
	out, found := d.outputs[key{slot, subslot}]
	if !found {
		return nil, 0, false
	}
	return append([]byte(nil), out.data...), out.iops, true
}

// ARs returns the application relationships.
func (d *Device) ARs() []ARInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]ARInfo, 0, len(d.ars))
	for _, a := range d.ars {
		out = append(out, a.info())
	}
	return out
}

// station adapts the device to dcp.Station.
type station struct{ d *Device }

func (s station) Identity() dcp.Identity {
	d := s.d
	d.mu.Lock()
	defer d.mu.Unlock()
	id := dcp.Identity{
		NameOfStation: d.name,
		TypeOfStation: d.model.Identity.TypeOfStation,
		VendorID:      d.model.Identity.VendorID,
		DeviceID:      d.model.Identity.DeviceID,
		Role:          dcp.RoleIODevice,
		HasInstance:   true,
		Instance:      max(d.model.Identity.Instance, 1),
		IP:            d.ip,
	}
	if d.cfg.Hello != dcp.HelloOff {
		id.Initiative, id.HasInitiative = dcp.InitiativeHello, true
	}
	if !d.ip.IsZero() {
		id.IPInfo = dcp.IPInfoSet
		if d.ipDHCP {
			id.IPInfo = dcp.IPInfoDHCP
		}
	}
	id.DHCP = cloneDHCP(d.dhcp)
	id.Alias = d.alias()
	return id
}

func (d *Device) hasIOAR() bool {
	for _, a := range d.ars {
		if a.typ != pnio.ARTypeIOSAR && a.currentState() != ARStateClosed {
			return true
		}
	}
	return false
}

func (s station) SetName(name string, permanent bool) dcp.BlockError {
	d := s.d
	d.mu.Lock()
	changed := d.name != name
	d.name = name
	var abort []*ar
	if changed {
		for _, a := range d.ars {
			abort = append(abort, a)
		}
	}
	d.mu.Unlock()
	// A new name invalidates the ARs established under the old one.
	for _, a := range abort {
		d.abort(a, pnio.AbortDCPNameChanged, true)
	}
	d.handler.NameChanged(name, permanent)
	return dcp.BlockErrorNone
}

func (s station) SetIP(ip dcp.IPSuite, permanent bool) dcp.BlockError {
	d := s.d
	d.mu.Lock()
	if d.ip.Addr != ip.Addr && d.hasIOAR() {
		d.mu.Unlock()
		return dcp.BlockErrorSetNotPossible
	}
	d.mu.Unlock()
	if d.cfg.ApplyIP != nil {
		if err := d.cfg.ApplyIP(ip, permanent); err != nil {
			d.log.Warnf("device: apply IP %s: %v", ip, err)
			return dcp.BlockErrorSetNotPossible
		}
	}
	d.mu.Lock()
	d.ip = ip
	d.ipDHCP = false
	d.mu.Unlock()
	d.handler.IPChanged(ip, permanent)
	return dcp.BlockErrorNone
}

func (s station) Signal() dcp.BlockError {
	s.d.handler.Signal()
	return dcp.BlockErrorNone
}

func (s station) ResetToFactory(mode uint16) dcp.BlockError {
	d := s.d
	code := d.handler.ResetToFactory(mode)
	if code != dcp.BlockErrorNone {
		return code
	}
	d.mu.Lock()
	var abort []*ar
	for _, a := range d.ars {
		abort = append(abort, a)
	}
	d.mu.Unlock()
	for _, a := range abort {
		d.abort(a, pnio.AbortDCPReset, true)
	}
	m := mode >> 1
	d.mu.Lock()
	if m == 1 || m >= 3 {
		d.im = IMData{}
		d.model.Each(func(p model.PluggedSubmodule) {
			for _, r := range p.Submodule.Records {
				v := r.Default
				if v == nil {
					v = make([]byte, r.Length)
				}
				d.records[recKey{p.Slot, p.Submodule.Subslot, r.Index}] = append([]byte(nil), v...)
			}
		})
	}
	resetComm := m >= 2
	if resetComm {
		d.name = ""
		d.dhcp = cloneDHCP(d.cfg.DHCP)
	}
	d.mu.Unlock()
	if resetComm {
		s.SetIP(dcp.IPSuite{}, true)
		d.handler.NameChanged("", true)
	}
	return dcp.BlockErrorNone
}

func (d *Device) lldpInfo(p *port) lldp.LocalInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return lldp.LocalInfo{
		StationName: d.name,
		PortName:    p.name(),
		Description: d.model.Identity.TypeOfStation,
		IP:          d.ip.Addr,
		// PDInterfaceAdjust: without NameOfDevice the port ID is the
		// port's name alone.
		Legacy: d.pd.interfaceMode()&pnio.InterfaceModeNameOfDevice == 0,
	}
}
