// SPDX-License-Identifier: MIT

// Package controller implements a PROFINET IO-Controller (conformance
// class A, RT_CLASS_1/2): it finds its devices by station name with DCP,
// optionally assigns their IP address, establishes an application
// relationship with each, parameterizes it, exchanges cyclic IO data,
// acknowledges alarms, reads and writes records and re-establishes the AR
// after a failure.
//
// Each device runs its own lifecycle on its own goroutine; a failure of one
// does not affect the others. Process data is exchanged through
// [Device.SetOutput] and [Device.Input].
package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/alarm"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
)

// ListenPacketFunc opens a UDP socket; net.ListenConfig.ListenPacket and
// netstack.Stack.ListenPacket have this signature.
type ListenPacketFunc func(ctx context.Context, network, address string) (net.PacketConn, error)

// Config configures a [Controller].
type Config struct {
	// Mux is the Layer 2 port; required.
	Mux *l2.Mux
	// ListenPacket opens the RPC sockets. nil uses the operating system's
	// UDP stack.
	ListenPacket ListenPacketFunc
	// RPCPort is the port of the controller's RPC server, which receives
	// the devices' ApplicationReady. Devices send it to 34964, the
	// default.
	RPCPort int
	// StationName is the controller's name in the AR (CMInitiatorStation
	// Name). Empty means "go-profinet-controller".
	StationName string
	// VendorID, DeviceID and Instance form the controller's object UUID.
	VendorID, DeviceID, Instance uint16
	// DCP configures the DCP requester.
	DCP dcp.ClientOptions
	// Logger receives diagnostics. nil logs nothing.
	Logger profinet.Logger
}

// Controller is a PROFINET IO-Controller. It is safe for concurrent use.
type Controller struct {
	cfg    Config
	log    profinet.Logger
	object rpc.UUID

	mux    *l2.Mux
	dcp    *dcp.Client
	alarms *alarm.Manager
	disp   *rt.Dispatcher
	sched  *rt.Scheduler
	rpcSrv *rpc.Server
	rpcCli *rpc.Client

	mu      sync.Mutex
	devices map[string]*Device
	byAR    map[rpc.UUID]*Device
	nextIn  uint16
	closed  bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New starts a controller.
func New(cfg Config) (*Controller, error) {
	if cfg.Mux == nil {
		return nil, fmt.Errorf("controller: %w: Mux is required", profinet.ErrInvalidConfig)
	}
	if cfg.RPCPort == 0 {
		cfg.RPCPort = rpc.Port
	}
	if cfg.StationName == "" {
		cfg.StationName = "go-profinet-controller"
	}
	if cfg.ListenPacket == nil {
		lc := &net.ListenConfig{}
		cfg.ListenPacket = lc.ListenPacket
	}
	if cfg.Instance == 0 {
		cfg.Instance = 1
	}
	if cfg.VendorID == 0 && cfg.DeviceID == 0 {
		// The vendor ID range PI reserves for testing and development.
		cfg.VendorID, cfg.DeviceID = 0xF000, 0x0001
	}
	log := cfg.Logger
	if log == nil {
		log = profinet.NopLogger()
	}
	if cfg.DCP.Logger == nil {
		cfg.DCP.Logger = log
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{
		cfg:     cfg,
		log:     log,
		object:  pnio.ObjectUUID(cfg.Instance, cfg.DeviceID, cfg.VendorID),
		mux:     cfg.Mux,
		devices: map[string]*Device{},
		byAR:    map[rpc.UUID]*Device{},
		nextIn:  pnio.FrameIDRTC1Min,
		ctx:     ctx,
		cancel:  cancel,
	}
	if err := c.start(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Controller) start() error {
	var err error
	c.sched = rt.NewScheduler()
	if c.dcp, err = dcp.NewClient(c.mux, c.cfg.DCP); err != nil {
		return err
	}
	if c.alarms, err = alarm.NewManager(c.mux); err != nil {
		return err
	}
	if c.disp, err = rt.NewDispatcher(c.mux); err != nil {
		return err
	}
	srv, err := c.cfg.ListenPacket(c.ctx, "udp4", ":"+strconv.Itoa(c.cfg.RPCPort))
	if err != nil {
		return fmt.Errorf("controller: RPC server socket: %w", err)
	}
	c.rpcSrv = rpc.NewServer(srv, true, rpc.ServerOptions{Logger: c.log})
	c.rpcSrv.Register(pnio.ControllerInterface, pnio.InterfaceVersion, rpc.HandlerFunc(c.serveRPC))
	cli, err := c.cfg.ListenPacket(c.ctx, "udp4", ":0")
	if err != nil {
		return fmt.Errorf("controller: RPC client socket: %w", err)
	}
	c.rpcCli = rpc.NewClient(cli, true, rpc.ClientOptions{Logger: c.log, Activities: 16, Retransmit: 500 * time.Millisecond})
	return nil
}

// DCP returns the controller's DCP requester, for discovery and explicit
// configuration of devices.
func (c *Controller) DCP() *dcp.Client { return c.dcp }

// RPC returns the controller's RPC client.
func (c *Controller) RPC() *rpc.Client { return c.rpcCli }

// ObjectUUID returns the controller's PNIO object UUID.
func (c *Controller) ObjectUUID() rpc.UUID { return c.object }

// Devices returns the devices added to the controller.
func (c *Controller) Devices() []*Device {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Device, 0, len(c.devices))
	for _, d := range c.devices {
		out = append(out, d)
	}
	return out
}

// Device returns the device with a station name.
func (c *Controller) Device(name string) (*Device, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.devices[name]
	return d, ok
}

// Close releases every AR and stops the controller.
func (c *Controller) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	devs := make([]*Device, 0, len(c.devices))
	for _, d := range c.devices {
		devs = append(devs, d)
	}
	c.mu.Unlock()
	var wg sync.WaitGroup
	for _, d := range devs {
		wg.Add(1)
		go func(d *Device) {
			defer wg.Done()
			d.Close()
		}(d)
	}
	wg.Wait()
	c.cancel()
	c.wg.Wait()
	if c.rpcSrv != nil {
		c.rpcSrv.Close()
	}
	if c.rpcCli != nil {
		c.rpcCli.Close()
	}
	if c.disp != nil {
		c.disp.Close()
	}
	if c.alarms != nil {
		c.alarms.Close()
	}
	if c.dcp != nil {
		c.dcp.Close()
	}
	if c.sched != nil {
		c.sched.Close()
	}
	return nil
}

// allocInputFrameID returns a FrameID for the input CR of a new AR.
func (c *Controller) allocInputFrameID(cls pnio.RTClass) uint16 {
	lo, hi, _ := pnio.FrameIDRange(cls)
	c.mu.Lock()
	defer c.mu.Unlock()
	used := map[uint16]bool{}
	for _, d := range c.devices {
		if id := d.inputFrameID(); id != 0 {
			used[id] = true
		}
	}
	for i := 0; i <= int(hi-lo); i++ {
		id := lo + uint16((int(c.nextIn-lo)+i)%int(hi-lo+1))
		if !used[id] {
			c.nextIn = id + 1
			if c.nextIn > hi || c.nextIn < lo {
				c.nextIn = lo
			}
			return id
		}
	}
	return lo
}

func (c *Controller) registerAR(u rpc.UUID, d *Device) {
	c.mu.Lock()
	c.byAR[u] = d
	c.mu.Unlock()
}

func (c *Controller) unregisterAR(u rpc.UUID) {
	c.mu.Lock()
	delete(c.byAR, u)
	c.mu.Unlock()
}

// serveRPC serves the controller interface: the devices' ApplicationReady.
func (c *Controller) serveRPC(_ context.Context, req *rpc.Request) ([]byte, error) {
	o := req.Order()
	argsMax, blocks, err := pnio.DecodeNDRRequest(req.Body, o)
	fail := func(st pnio.Status) ([]byte, error) {
		return pnio.AppendNDRResponse(nil, o, st, argsMax, nil), nil
	}
	if err != nil {
		return fail(pnio.ServiceStatus(pnio.CodeControl, pnio.Code1CMRPC, pnio.CMRPCArgsLength))
	}
	if pnio.OpNum(req.Header.OpNum) != pnio.OpControl {
		return nil, &rpc.Fault{Reject: true, Status: rpc.StatusOpRangeError}
	}
	blk, _, err := pnio.ReadBlock(blocks)
	if err != nil {
		return fail(pnio.ServiceStatus(pnio.CodeControl, pnio.Code1CMRPC, pnio.CMRPCArgsLength))
	}
	cb, err := pnio.DecodeControlBlock(blk)
	if err != nil {
		var se *pnio.StatusError
		if errors.As(err, &se) {
			return fail(se.Status)
		}
		return fail(pnio.ServiceStatus(pnio.CodeControl, pnio.Code1AppReady, 1))
	}
	if cb.Type != pnio.BlockAppReadyReq && cb.Type != pnio.BlockAppReadyPlugReq || cb.Command&pnio.CmdApplicationReady == 0 {
		return fail(pnio.ServiceStatus(pnio.CodeControl, pnio.Code1AppReady, 8))
	}
	c.mu.Lock()
	d := c.byAR[cb.ARUUID]
	c.mu.Unlock()
	if d == nil || !d.applicationReady(cb) {
		return fail(pnio.ServiceStatus(pnio.CodeControl, pnio.Code1CMRPC, pnio.CMRPCARUUIDUnknown))
	}
	resp := cb.Response()
	return pnio.AppendNDRResponse(nil, o, pnio.Status{}, argsMax, resp.Append(nil)), nil
}
