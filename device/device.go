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
	// Mux is the Layer 2 port; required.
	Mux *l2.Mux
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
	lldpA  *lldp.Agent
	rpcSrv *rpc.Server
	rpcCli *rpc.Client

	// mu guards the station state and the ARs.
	mu       sync.Mutex
	name     string
	ip       dcp.IPSuite
	ars      map[rpc.UUID]*ar
	frameIDs map[uint16]bool
	records  map[recKey][]byte
	im       IMData
	diags    map[key][]pnio.DiagnosisItem
	closed   bool

	// imgMu guards the process data images; it is never held while
	// another lock is taken.
	imgMu   sync.RWMutex
	inputs  map[key]*ioData
	outputs map[key]*ioData

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
		ars:      map[rpc.UUID]*ar{},
		frameIDs: map[uint16]bool{},
		records:  map[recKey][]byte{},
		im:       cfg.IM,
		diags:    map[key][]pnio.DiagnosisItem{},
		inputs:   map[key]*ioData{},
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
		d.Close()
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
	if d.dcpR, err = dcp.NewResponder(d.mux, station{d}, dcp.ResponderOptions{Logger: d.log, NoResponseDelay: d.cfg.NoResponseDelay}); err != nil {
		return err
	}
	conn, err := d.cfg.ListenPacket(d.ctx, "udp4", ":"+strconv.Itoa(d.cfg.RPCPort))
	if err != nil {
		return fmt.Errorf("device: RPC socket: %w", err)
	}
	d.rpcSrv = rpc.NewServer(conn, true, rpc.ServerOptions{Logger: d.log})
	d.rpcSrv.Register(pnio.DeviceInterface, pnio.InterfaceVersion, rpc.HandlerFunc(d.serveRPC))
	cconn, err := d.cfg.ListenPacket(d.ctx, "udp4", ":0")
	if err != nil {
		return fmt.Errorf("device: RPC client socket: %w", err)
	}
	d.rpcCli = rpc.NewClient(cconn, true, rpc.ClientOptions{Logger: d.log, Retransmit: 2 * time.Second, Activities: 2})
	if !d.cfg.DisableLLDP {
		d.lldpA, err = lldp.NewAgent(d.mux, lldp.AgentOptions{
			Interval: d.cfg.LLDPInterval,
			Local:    d.lldpInfo,
			Logger:   d.log,
		})
		if err != nil {
			return err
		}
	}
	return nil
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
		d.rpcSrv.Close()
	}
	if d.rpcCli != nil {
		d.rpcCli.Close()
	}
	if d.lldpA != nil {
		d.lldpA.Close()
	}
	if d.dcpR != nil {
		d.dcpR.Close()
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
	if !d.ip.IsZero() {
		id.IPInfo = dcp.IPInfoSet
	}
	if d.lldpA != nil {
		id.Alias = d.lldpA.Alias()
	}
	return id
}

func (d *Device) hasIOAR() bool {
	for _, a := range d.ars {
		if a.typ != pnio.ARTypeIOSAR && a.state != ARStateClosed {
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
	}
	d.mu.Unlock()
	if resetComm {
		s.SetIP(dcp.IPSuite{}, true)
		d.handler.NameChanged("", true)
	}
	return dcp.BlockErrorNone
}

func (d *Device) lldpInfo() lldp.LocalInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return lldp.LocalInfo{
		StationName: d.name,
		PortName:    "port-001",
		Description: d.model.Identity.TypeOfStation,
		IP:          d.ip.Addr,
	}
}
