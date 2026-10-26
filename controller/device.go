// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/alarm"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
)

// Parameter is a record written to a device before the end of
// parameterization.
type Parameter struct {
	Slot, Subslot uint16
	Index         pnio.Index
	Data          []byte
}

// AlarmEvent is an alarm a device sent; the controller acknowledges it.
type AlarmEvent struct {
	Device       string
	Notification pnio.AlarmNotification
	Received     time.Time
}

// DeviceConfig configures a device of the controller.
type DeviceConfig struct {
	// StationName is the device's NameOfStation; required.
	StationName string
	// Model is the expected configuration; required. Its identity's vendor
	// and device ID must match what the device reports in DCP.
	Model *model.Device
	// IP is the IP suite the device should have. When AssignIP is true
	// and the device reports a different one, it is set with DCP
	// (temporarily, or permanently with PermanentIP). Without AssignIP the
	// device's own address is used.
	IP          dcp.IPSuite
	AssignIP    bool
	PermanentIP bool
	// SendClockFactor (units of 31.25 µs) and ReductionRatio define the
	// cycle; zero means 32 (1 ms) and 4: a cycle of 4 ms.
	SendClockFactor, ReductionRatio uint16
	// DataHoldFactor and WatchdogFactor are in cycles; zero means 3.
	DataHoldFactor, WatchdogFactor uint16
	// Advanced selects the advanced startup mode with RTClass 2;
	// otherwise the legacy startup with RTClass 1 is used.
	Advanced bool
	// ActivityTimeout is the CM initiator activity timeout; zero means
	// 10 s.
	ActivityTimeout time.Duration
	// RTATimeoutFactor (units of 100 ms) and RTARetries configure the
	// alarm transport; zero means 5 and 3.
	RTATimeoutFactor, RTARetries uint16
	// Parameters are written after Connect and before PrmEnd.
	Parameters []Parameter
	// RPCTimeout bounds each RPC call; zero means 5 s.
	RPCTimeout time.Duration
	// ReconnectMin and ReconnectMax bound the backoff between attempts;
	// zero means 200 ms and 10 s.
	ReconnectMin, ReconnectMax time.Duration
	// OnAlarm receives every alarm, on the alarm goroutine of the AR; it
	// must not block.
	OnAlarm func(AlarmEvent)
	// OnState receives every state change.
	OnState func(from, to State, err error)
}

// State is the state of a device's lifecycle on the controller.
type State uint8

// States. A device goes Offline → Resolving → Connecting →
// Parameterizing → WaitApplicationReady → Running; a failure in any of
// them goes back to Offline (and after the backoff to Resolving); Close
// goes to Stopped.
const (
	StateOffline State = iota
	StateResolving
	StateConnecting
	StateParameterizing
	StateWaitAppReady
	StateRunning
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateOffline:
		return "Offline"
	case StateResolving:
		return "Resolving"
	case StateConnecting:
		return "Connecting"
	case StateParameterizing:
		return "Parameterizing"
	case StateWaitAppReady:
		return "WaitApplicationReady"
	case StateRunning:
		return "Running"
	case StateStopped:
		return "Stopped"
	default:
		return "State(?)"
	}
}

var stateTransitions = map[State][]State{
	StateOffline:        {StateResolving, StateStopped},
	StateResolving:      {StateConnecting, StateOffline, StateStopped},
	StateConnecting:     {StateParameterizing, StateOffline, StateStopped},
	StateParameterizing: {StateWaitAppReady, StateOffline, StateStopped},
	StateWaitAppReady:   {StateRunning, StateOffline, StateStopped},
	StateRunning:        {StateOffline, StateStopped},
	StateStopped:        {},
}

// CanTransition reports whether the lifecycle allows from → to.
func CanTransition(from, to State) bool {
	for _, s := range stateTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// DeviceStats are the counters of a device.
type DeviceStats struct {
	Connects, Aborts uint64
	Provider         rt.ProviderStats
	Consumer         rt.ConsumerStats
	Cycle            rt.TaskStats
	Alarms           uint64
}

// Device is a device of the controller.
type Device struct {
	c   *Controller
	cfg DeviceConfig

	mu        sync.Mutex
	state     State
	lastErr   error
	mac       ethernet.MAC
	ip        dcp.IPSuite
	ar        *arState
	diff      *pnio.ModuleDiffBlock
	waiters   []chan struct{}
	outputs   map[subKey]*ioData
	inputs    map[subKey]*ioData
	iocs      map[subKey]uint8
	connects  atomic.Uint64
	aborts    atomic.Uint64
	alarmsRx  atomic.Uint64
	lastStats DeviceStats

	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

type subKey struct {
	slot, subslot uint16
}

type ioData struct {
	data []byte
	iops uint8
}

// arState is one established (or establishing) AR.
type arState struct {
	uuid       rpc.UUID
	sessionKey uint16
	records    *RecordClient
	inFrameID  uint16
	outFrameID uint16
	inLayout   rt.Layout
	outLayout  rt.Layout
	class      pnio.RTClass
	localRef   uint16
	remoteRef  uint16
	addr       net.Addr
	proper     map[subKey]bool
	provider   *rt.Provider
	consumer   *rt.Consumer
	provTask   *rt.Task
	wdTask     *rt.Task
	alarmHigh  *alarm.Endpoint
	alarmLow   *alarm.Endpoint
	appReady   chan struct{}
	appOnce    sync.Once
	failed     chan error
	failOnce   sync.Once
	failErr    error // set once by fail, before the send on failed
}

func (a *arState) fail(err error) {
	a.failOnce.Do(func() {
		a.failErr = err
		a.failed <- err
	})
}

// AddDevice adds a device and starts its lifecycle.
func (c *Controller) AddDevice(cfg DeviceConfig) (*Device, error) {
	if err := dcp.ValidateStationName(cfg.StationName); err != nil {
		return nil, err
	}
	if cfg.Model == nil {
		return nil, fmt.Errorf("controller: %w: device %q has no model", profinet.ErrInvalidConfig, cfg.StationName)
	}
	if err := cfg.Model.Validate(); err != nil {
		return nil, err
	}
	if cfg.SendClockFactor == 0 {
		cfg.SendClockFactor = 32
	}
	if cfg.ReductionRatio == 0 {
		cfg.ReductionRatio = 4
	}
	if cfg.DataHoldFactor == 0 {
		cfg.DataHoldFactor = 3
	}
	if cfg.WatchdogFactor == 0 {
		cfg.WatchdogFactor = 3
	}
	if cfg.ActivityTimeout <= 0 {
		cfg.ActivityTimeout = 10 * time.Second
	}
	if cfg.RTATimeoutFactor == 0 {
		cfg.RTATimeoutFactor = 5
	}
	if cfg.RTARetries == 0 {
		cfg.RTARetries = 3
	}
	if cfg.RPCTimeout <= 0 {
		cfg.RPCTimeout = 5 * time.Second
	}
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = 200 * time.Millisecond
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 10 * time.Second
	}
	d := &Device{
		c:       c,
		cfg:     cfg,
		outputs: map[subKey]*ioData{},
		inputs:  map[subKey]*ioData{},
		iocs:    map[subKey]uint8{},
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	cfg.Model.Each(func(p model.PluggedSubmodule) {
		k := subKey{p.Slot, p.Submodule.Subslot}
		t := p.Submodule.Type()
		if t.HasOutput() {
			d.outputs[k] = &ioData{data: make([]byte, p.Submodule.OutputLen), iops: pnio.IOxSGood}
		}
		if t == pnio.SubmoduleNoIO || t.HasInput() {
			d.inputs[k] = &ioData{data: make([]byte, p.Submodule.InputLen), iops: pnio.IOxSBad}
		}
	})
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("controller: %w", profinet.ErrClosed)
	}
	if c.devices[cfg.StationName] != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("controller: %w: device %q added twice", profinet.ErrInvalidConfig, cfg.StationName)
	}
	c.devices[cfg.StationName] = d
	c.mu.Unlock()
	c.wg.Add(1)
	go d.run()
	return d, nil
}

// Name returns the device's station name.
func (d *Device) Name() string { return d.cfg.StationName }

// State returns the lifecycle state and the error of the last failure.
func (d *Device) State() (State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state, d.lastErr
}

// MAC returns the device's MAC address once it was resolved.
func (d *Device) MAC() ethernet.MAC {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mac
}

// IP returns the device's IP suite once it was resolved.
func (d *Device) IP() dcp.IPSuite {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ip
}

// ModuleDiff returns the ModuleDiffBlock of the last Connect, nil when the
// real configuration matched.
func (d *Device) ModuleDiff() *pnio.ModuleDiffBlock {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.diff
}

// ARUUID returns the UUID of the current AR, nil when there is none.
func (d *Device) ARUUID() rpc.UUID {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ar == nil {
		return rpc.NilUUID
	}
	return d.ar.uuid
}

func (d *Device) inputFrameID() uint16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ar == nil {
		return 0
	}
	return d.ar.inFrameID
}

// WaitState waits until the device is in state s.
func (d *Device) WaitState(ctx context.Context, s State) error {
	for {
		d.mu.Lock()
		if d.state == s {
			d.mu.Unlock()
			return nil
		}
		ch := make(chan struct{})
		d.waiters = append(d.waiters, ch)
		d.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d *Device) setState(to State, err error) {
	d.mu.Lock()
	from := d.state
	if from == to || !CanTransition(from, to) {
		d.mu.Unlock()
		return
	}
	d.state = to
	if err != nil {
		d.lastErr = err
	}
	ws := d.waiters
	d.waiters = nil
	d.mu.Unlock()
	for _, w := range ws {
		close(w)
	}
	d.c.log.Debugf("controller: %s: %s → %s", d.cfg.StationName, from, to)
	if d.cfg.OnState != nil {
		d.cfg.OnState(from, to, err)
	}
}

// SetOutput sets the output data and provider status of a submodule for
// the next cyclic frames.
func (d *Device) SetOutput(slot, subslot uint16, data []byte, iops uint8) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	o, ok := d.outputs[subKey{slot, subslot}]
	if !ok {
		return fmt.Errorf("controller: %s output %d/%d: %w", d.cfg.StationName, slot, subslot, profinet.ErrNotFound)
	}
	if len(data) != len(o.data) {
		return fmt.Errorf("controller: %s output %d/%d: %w: %d octets instead of %d", d.cfg.StationName, slot, subslot, profinet.ErrInvalidConfig, len(data), len(o.data))
	}
	copy(o.data, data)
	o.iops = iops
	return nil
}

// Input returns a copy of the input data of a submodule and its provider
// status from the last cyclic frame.
func (d *Device) Input(slot, subslot uint16) (data []byte, iops uint8, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	in, found := d.inputs[subKey{slot, subslot}]
	if !found {
		return nil, 0, false
	}
	return append([]byte(nil), in.data...), in.iops, true
}

// ConsumerStatus returns the device's consumer status (IOCS) of a
// submodule's outputs.
func (d *Device) ConsumerStatus(slot, subslot uint16) (uint8, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.iocs[subKey{slot, subslot}]
	return v, ok
}

// Stats returns the counters of the device and of its current AR.
func (d *Device) Stats() DeviceStats {
	d.mu.Lock()
	a := d.ar
	s := d.lastStats
	d.mu.Unlock()
	s.Connects = d.connects.Load()
	s.Aborts = d.aborts.Load()
	s.Alarms = d.alarmsRx.Load()
	if a != nil {
		s.Provider = a.provider.Stats()
		s.Consumer = a.consumer.Stats()
		s.Cycle = a.provTask.Stats()
	}
	return s
}

// Records returns the record client of the current AR, and an error
// matching profinet.ErrInvalidState when there is no AR.
func (d *Device) records() (*arState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ar == nil || d.state < StateParameterizing || d.state > StateRunning {
		return nil, fmt.Errorf("controller: %s: %w: no application relationship (%s)", d.cfg.StationName, profinet.ErrInvalidState, d.state)
	}
	return d.ar, nil
}

// ReadRecord reads a record within the AR.
func (d *Device) ReadRecord(ctx context.Context, rec Record, maxLen uint32) ([]byte, error) {
	a, err := d.records()
	if err != nil {
		return nil, err
	}
	return a.records.Read(ctx, a.uuid, rec, maxLen)
}

// WriteRecord writes a record within the AR.
func (d *Device) WriteRecord(ctx context.Context, rec Record, data []byte) error {
	a, err := d.records()
	if err != nil {
		return err
	}
	return a.records.Write(ctx, a.uuid, rec, data)
}

// WriteMultiple writes several records in one call.
func (d *Device) WriteMultiple(ctx context.Context, items []WriteItem) ([]pnio.Status, error) {
	a, err := d.records()
	if err != nil {
		return nil, err
	}
	return a.records.WriteMultiple(ctx, a.uuid, items)
}

// ReadIM0 reads the device's I&M0.
func (d *Device) ReadIM0(ctx context.Context) (pnio.IM0, error) {
	a, err := d.records()
	if err != nil {
		return pnio.IM0{}, err
	}
	return a.records.ReadIM0(ctx, a.uuid, 0, model.SubslotDAP)
}

// ReadDiagnosis reads the diagnosis of the whole device.
func (d *Device) ReadDiagnosis(ctx context.Context) ([]pnio.DiagnosisData, error) {
	a, err := d.records()
	if err != nil {
		return nil, err
	}
	return a.records.ReadDiagnosis(ctx, a.uuid, 0, 0, false)
}

// Close releases the AR (when established) and stops the lifecycle. The
// device is removed from the controller.
func (d *Device) Close() error {
	d.once.Do(func() { close(d.stop) })
	<-d.stopped
	d.c.mu.Lock()
	if d.c.devices[d.cfg.StationName] == d {
		delete(d.c.devices, d.cfg.StationName)
	}
	d.c.mu.Unlock()
	return nil
}

func (d *Device) stopping() bool {
	select {
	case <-d.stop:
		return true
	default:
		return false
	}
}

// run is the device's lifecycle.
func (d *Device) run() {
	defer d.c.wg.Done()
	defer close(d.stopped)
	backoff := d.cfg.ReconnectMin
	for {
		if d.stopping() {
			d.setState(StateStopped, nil)
			return
		}
		ran, err := d.session()
		if ran {
			backoff = d.cfg.ReconnectMin
		}
		if d.stopping() {
			d.setState(StateStopped, nil)
			return
		}
		d.setState(StateOffline, err)
		if err != nil {
			d.c.log.Warnf("controller: %s: %v (retry in %s)", d.cfg.StationName, err, backoff)
		}
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-d.stop:
			t.Stop()
		}
		backoff = min(backoff*2, d.cfg.ReconnectMax)
	}
}

// session resolves, connects and runs one AR until it fails or the device
// is closed. ran reports whether the AR reached Running; err is nil only
// when the device was stopped.
func (d *Device) session() (ran bool, err error) {
	ctx, cancel := context.WithCancel(d.c.ctx)
	defer cancel()
	go func() {
		select {
		case <-d.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	d.setState(StateResolving, nil)
	id, err := d.resolve(ctx)
	if err != nil {
		return false, err
	}
	d.setState(StateConnecting, nil)
	a, err := d.connect(ctx, id)
	if err != nil {
		return false, err
	}
	defer d.teardown(a)
	d.setState(StateParameterizing, nil)
	if err := d.parameterize(ctx, a); err != nil {
		a.fail(err)
		return false, err
	}
	d.setState(StateWaitAppReady, nil)
	t := time.NewTimer(d.cfg.ActivityTimeout)
	defer t.Stop()
	select {
	case <-a.appReady:
	case err := <-a.failed:
		return false, err
	case <-t.C:
		err := fmt.Errorf("controller: %s: %w: no ApplicationReady within %s", d.cfg.StationName, profinet.ErrTimeout, d.cfg.ActivityTimeout)
		a.fail(err)
		return false, err
	case <-ctx.Done():
		d.release(a)
		return false, nil
	}
	d.setState(StateRunning, nil)
	a.consumer.Arm(time.Now())
	d.c.log.Infof("controller: %s: AR %s running", d.cfg.StationName, a.uuid)
	select {
	case err := <-a.failed:
		return true, err
	case <-ctx.Done():
		d.release(a)
		return true, nil
	}
}

// resolve finds the device with DCP and makes sure it has the configured
// IP suite.
func (d *Device) resolve(ctx context.Context) (dcp.Identity, error) {
	id, err := d.c.dcp.IdentifyName(ctx, d.cfg.StationName, time.Second)
	if err != nil {
		return id, fmt.Errorf("controller: resolve %s: %w", d.cfg.StationName, err)
	}
	mi := d.cfg.Model.Identity
	if id.VendorID != mi.VendorID || id.DeviceID != mi.DeviceID {
		return id, fmt.Errorf("controller: resolve %s: %w: vendor/device 0x%04X/0x%04X, expected 0x%04X/0x%04X",
			d.cfg.StationName, profinet.ErrInvalidConfig, id.VendorID, id.DeviceID, mi.VendorID, mi.DeviceID)
	}
	if d.cfg.AssignIP && !d.cfg.IP.IsZero() && id.IP != d.cfg.IP {
		d.c.log.Infof("controller: %s: assigning IP %s (was %s)", d.cfg.StationName, d.cfg.IP, id.IP)
		if err := d.c.dcp.SetIP(ctx, id.MAC, d.cfg.IP, d.cfg.PermanentIP); err != nil {
			return id, fmt.Errorf("controller: assign IP to %s: %w", d.cfg.StationName, err)
		}
		id.IP = d.cfg.IP
	}
	if id.IP.IsZero() || !id.IP.Addr.Is4() {
		return id, fmt.Errorf("controller: resolve %s: %w: the device has no IP address", d.cfg.StationName, profinet.ErrInvalidState)
	}
	d.mu.Lock()
	d.mac, d.ip = id.MAC, id.IP
	d.mu.Unlock()
	return id, nil
}

func (d *Device) subIO() []rt.SubmoduleIO {
	var out []rt.SubmoduleIO
	d.cfg.Model.Each(func(p model.PluggedSubmodule) {
		out = append(out, rt.SubmoduleIO{API: model.API, Slot: p.Slot, Subslot: p.Submodule.Subslot, Type: p.Submodule.Type(), InputLen: p.Submodule.InputLen, OutputLen: p.Submodule.OutputLen})
	})
	return out
}

var sessionKeys atomic.Uint32

// connect sends the Connect request and sets up the cyclic and alarm
// machinery of the new AR.
func (d *Device) connect(ctx context.Context, id dcp.Identity) (*arState, error) {
	cfg := &d.cfg
	cls := pnio.RTClass1
	props := pnio.ARPropStateActive | pnio.ARPropPrmServerCMInit
	if cfg.Advanced {
		cls = pnio.RTClass2
		props |= pnio.ARPropStartupAdvanced
	}
	subs := d.subIO()
	inL, err := rt.ComputeLayout(pnio.IOCRInput, subs)
	if err != nil {
		return nil, err
	}
	outL, err := rt.ComputeLayout(pnio.IOCROutput, subs)
	if err != nil {
		return nil, err
	}
	inID := d.c.allocInputFrameID(cls)
	localRef := d.c.alarms.AllocRef()
	timeoutFactor := uint16(min(max(cfg.ActivityTimeout/(100*time.Millisecond), 1), 1000))
	a := &arState{
		uuid:       rpc.NewRandomUUID(),
		sessionKey: uint16(sessionKeys.Add(1)),
		inFrameID:  inID,
		inLayout:   inL,
		outLayout:  outL,
		class:      cls,
		localRef:   localRef,
		appReady:   make(chan struct{}),
		failed:     make(chan error, 1),
	}
	addr := &net.UDPAddr{IP: id.IP.Addr.AsSlice(), Port: rpc.Port}
	a.addr = addr
	a.records = NewRecordClient(d.c.rpcCli, addr, pnio.ObjectUUID(max(id.Instance, 1), id.DeviceID, id.VendorID))
	tag := uint16(6) << 13
	iocr := func(t pnio.IOCRType, frameID uint16, l rt.Layout, ref uint16) pnio.IOCRBlockReq {
		return pnio.IOCRBlockReq{
			Type: t, Reference: ref, LT: pnio.LT, Properties: uint32(cls), DataLength: l.DataLength, FrameID: frameID,
			SendClockFactor: cfg.SendClockFactor, ReductionRatio: cfg.ReductionRatio, Phase: 1, FrameSendOffset: 0xFFFFFFFF,
			WatchdogFactor: cfg.WatchdogFactor, DataHoldFactor: cfg.DataHoldFactor, TagHeader: tag, APIs: l.IOCRAPIs(),
		}
	}
	req := &pnio.ConnectRequest{
		AR: pnio.ARBlockReq{
			Type: pnio.ARTypeIOCARSingle, ARUUID: a.uuid, SessionKey: a.sessionKey,
			InitiatorMAC: d.c.mux.HardwareAddr(), InitiatorObject: d.c.object, Properties: props,
			ActivityTimeoutFactor: timeoutFactor, UDPRTPort: pnio.LT, StationName: d.c.cfg.StationName,
		},
		IOCRs: []pnio.IOCRBlockReq{iocr(pnio.IOCRInput, inID, inL, 1), iocr(pnio.IOCROutput, pnio.FrameIDAssign, outL, 2)},
		AlarmCR: &pnio.AlarmCRBlockReq{
			Type: 1, LT: pnio.LT, RTATimeoutFactor: cfg.RTATimeoutFactor, RTARetries: cfg.RTARetries,
			LocalAlarmReference: localRef, MaxAlarmDataLength: 200, TagHeaderHigh: 6 << 13, TagHeaderLow: 5 << 13,
		},
		Expected: cfg.Model.Expected(),
	}
	if d.c.cfg.RPCPort != rpc.Port {
		req.ARRPC = &pnio.ARRPCBlockReq{InitiatorRPCServerPort: uint16(d.c.cfg.RPCPort)}
	}
	// The AR is known before the response, because the device may send
	// ApplicationReady quickly.
	d.c.registerAR(a.uuid, d)
	cctx, cancel := context.WithTimeout(ctx, cfg.RPCTimeout)
	resp, err := a.records.Connect(cctx, req)
	cancel()
	if err != nil {
		d.c.unregisterAR(a.uuid)
		return nil, fmt.Errorf("controller: connect %s: %w", cfg.StationName, err)
	}
	if resp.AR.ARUUID != a.uuid || resp.AlarmCR == nil {
		d.c.unregisterAR(a.uuid)
		return nil, fmt.Errorf("controller: connect %s: %w: response for another AR or without AlarmCR", cfg.StationName, profinet.ErrMalformed)
	}
	for _, r := range resp.IOCRs {
		switch r.Type {
		case pnio.IOCROutput:
			a.outFrameID = r.FrameID
		case pnio.IOCRInput:
			if r.FrameID != inID {
				d.c.unregisterAR(a.uuid)
				return nil, fmt.Errorf("controller: connect %s: %w: the device changed the input FrameID", cfg.StationName, profinet.ErrMalformed)
			}
		default:
		}
	}
	if a.outFrameID == 0 || a.outFrameID == pnio.FrameIDAssign {
		d.c.unregisterAR(a.uuid)
		return nil, fmt.Errorf("controller: connect %s: %w: no output FrameID", cfg.StationName, profinet.ErrMalformed)
	}
	d.connects.Add(1)
	a.remoteRef = resp.AlarmCR.LocalAlarmReference
	a.proper = d.properSubmodules(resp.ModuleDiff)
	d.mu.Lock()
	d.ar = a
	d.diff = resp.ModuleDiff
	d.mu.Unlock()
	if err := d.startAlarms(a, localRef, resp.AlarmCR.LocalAlarmReference, id.MAC); err != nil {
		d.teardown(a)
		return nil, err
	}
	d.startCyclic(a, id.MAC)
	return a, nil
}

// properSubmodules returns the submodules the ModuleDiffBlock does not
// report as wrong or missing.
func (d *Device) properSubmodules(diff *pnio.ModuleDiffBlock) map[subKey]bool {
	proper := map[subKey]bool{}
	d.cfg.Model.Each(func(p model.PluggedSubmodule) {
		proper[subKey{p.Slot, p.Submodule.Subslot}] = true
	})
	if diff == nil {
		return proper
	}
	for _, api := range diff.APIs {
		for _, m := range api.Modules {
			if m.State != pnio.ModuleStateProper {
				for k := range proper {
					if k.slot == m.Slot {
						delete(proper, k)
					}
				}
				continue
			}
			for _, s := range m.Submodules {
				ident := s.State & (0xF << 11)
				if ident != pnio.SubmStateIdentOK {
					delete(proper, subKey{m.Slot, s.Subslot})
				}
			}
		}
	}
	return proper
}

func (d *Device) startAlarms(a *arState, localRef, remoteRef uint16, mac ethernet.MAC) error {
	base := alarm.EndpointConfig{
		LocalRef: localRef, RemoteRef: remoteRef, RemoteMAC: mac,
		Timeout: time.Duration(d.cfg.RTATimeoutFactor) * 100 * time.Millisecond, Retries: int(d.cfg.RTARetries),
		OnErr: func(st pnio.Status) {
			a.fail(fmt.Errorf("controller: %s: %w by the device: %s", d.cfg.StationName, profinet.ErrAborted, st))
		},
	}
	var err error
	hi := base
	hi.High = true
	hi.OnData = func(b []byte) { d.alarmData(a, true, b) }
	if a.alarmHigh, err = d.c.alarms.Open(hi); err != nil {
		return err
	}
	base.OnData = func(b []byte) { d.alarmData(a, false, b) }
	if a.alarmLow, err = d.c.alarms.Open(base); err != nil {
		return err
	}
	return nil
}

// alarmData handles an alarm notification: report it and acknowledge it
// on the same priority.
func (d *Device) alarmData(a *arState, high bool, b []byte) {
	blk, _, err := pnio.ReadBlock(b)
	if err != nil {
		return
	}
	n, err := pnio.DecodeAlarmNotification(blk)
	if err != nil {
		d.c.log.Debugf("controller: %s: alarm: %v", d.cfg.StationName, err)
		return
	}
	d.alarmsRx.Add(1)
	if d.cfg.OnAlarm != nil {
		d.cfg.OnAlarm(AlarmEvent{Device: d.cfg.StationName, Notification: n, Received: time.Now()})
	}
	ack := pnio.AckFor(&n, pnio.Status{})
	ep := a.alarmLow
	if high {
		ep = a.alarmHigh
	}
	if err := ep.Send(d.c.ctx, ack.Append(nil)); err != nil {
		if errors.Is(err, profinet.ErrTimeout) {
			a.fail(fmt.Errorf("controller: %s: alarm ack: %w", d.cfg.StationName, err))
		}
	}
}

func (d *Device) startCyclic(a *arState, mac ethernet.MAC) {
	cfg := &d.cfg
	cycle := time.Duration(rt.CycleNanos(cfg.SendClockFactor, cfg.ReductionRatio))
	a.provider = rt.NewProvider(rt.ProviderConfig{
		FrameID: a.outFrameID, DataLength: a.outLayout.DataLength, Dst: mac, Src: d.c.mux.HardwareAddr(),
		CounterStep: cfg.SendClockFactor * cfg.ReductionRatio, Send: d.c.mux.Send,
		Fill: func(c []byte) { d.fillOutputs(a, c) },
	})
	a.consumer = rt.NewConsumer(rt.ConsumerConfig{
		FrameID: a.inFrameID, DataLength: a.inLayout.DataLength, Src: mac,
		Watchdog:     time.Duration(cfg.DataHoldFactor) * cycle,
		StartupGrace: cfg.ActivityTimeout,
	})
	a.consumer.OnUpdate(func(c []byte, s rt.DataStatus) { d.storeInputs(a, c, s) })
	d.c.disp.Add(a.consumer)
	ctx := d.c.ctx
	a.provTask = d.c.sched.Add(cycle, func(time.Time) { a.provider.Tick(ctx) })
	a.wdTask = d.c.sched.Add(cycle, func(now time.Time) {
		if a.consumer.Check(now) {
			a.fail(fmt.Errorf("controller: %s: %w: input data hold time expired", d.cfg.StationName, profinet.ErrTimeout))
		}
	})
}

func (d *Device) fillOutputs(a *arState, c []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, o := range a.outLayout.Objects {
		k := subKey{o.Slot, o.Subslot}
		out := d.outputs[k]
		dst := c[o.Offset : o.Offset+o.Length]
		if out == nil || !a.proper[k] {
			clear(dst)
			c[o.IOPSOffset()] = pnio.IOxSBad
			continue
		}
		copy(dst, out.data)
		c[o.IOPSOffset()] = out.iops
	}
	for _, s := range a.outLayout.IOCS {
		if a.proper[subKey{s.Slot, s.Subslot}] {
			c[s.Offset] = pnio.IOxSGood
		} else {
			c[s.Offset] = pnio.IOxSBad
		}
	}
}

func (d *Device) storeInputs(a *arState, c []byte, s rt.DataStatus) {
	if !s.Valid() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, o := range a.inLayout.Objects {
		k := subKey{o.Slot, o.Subslot}
		in := d.inputs[k]
		if in == nil || !a.proper[k] {
			continue
		}
		copy(in.data, c[o.Offset:o.Offset+o.Length])
		in.iops = c[o.IOPSOffset()]
		if !s.Running() {
			in.iops = pnio.IOxSBad
		}
	}
	for _, st := range a.inLayout.IOCS {
		d.iocs[subKey{st.Slot, st.Subslot}] = c[st.Offset]
	}
}

// parameterize writes the parameters and ends the parameterization.
func (d *Device) parameterize(ctx context.Context, a *arState) error {
	for _, p := range d.cfg.Parameters {
		cctx, cancel := context.WithTimeout(ctx, d.cfg.RPCTimeout)
		err := a.records.Write(cctx, a.uuid, Record{API: model.API, Slot: p.Slot, Subslot: p.Subslot, Index: p.Index}, p.Data)
		cancel()
		if err != nil {
			return fmt.Errorf("controller: %s: parameter %d/%d %s: %w", d.cfg.StationName, p.Slot, p.Subslot, p.Index, err)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, d.cfg.RPCTimeout)
	defer cancel()
	err := a.records.Control(cctx, pnio.ControlBlock{Type: pnio.BlockPrmEndReq, ARUUID: a.uuid, SessionKey: a.sessionKey, Command: pnio.CmdPrmEnd})
	if err != nil {
		return fmt.Errorf("controller: %s: PrmEnd: %w", d.cfg.StationName, err)
	}
	return nil
}

// applicationReady is called by the RPC server for the AR's
// ApplicationReady; it reports whether the AR is waiting for it.
func (d *Device) applicationReady(cb pnio.ControlBlock) bool {
	d.mu.Lock()
	a := d.ar
	d.mu.Unlock()
	if a == nil || a.uuid != cb.ARUUID || a.sessionKey != cb.SessionKey {
		return false
	}
	a.appOnce.Do(func() { close(a.appReady) })
	return true
}

// release ends the AR with a Release call.
func (d *Device) release(a *arState) {
	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.RPCTimeout)
	defer cancel()
	err := a.records.Control(ctx, pnio.ControlBlock{Type: pnio.BlockReleaseReq, ARUUID: a.uuid, SessionKey: a.sessionKey, Command: pnio.CmdRelease})
	if err != nil {
		d.c.log.Debugf("controller: %s: release: %v", d.cfg.StationName, err)
	}
}

// teardown stops the cyclic and alarm machinery of an AR.
func (d *Device) teardown(a *arState) {
	// Mark the AR failed so that a late failure is not reported, then
	// tell the device unless it aborted the AR itself.
	a.fail(nil)
	if err := a.failErr; err != nil {
		d.aborts.Add(1)
		if a.alarmLow != nil && !errors.Is(err, profinet.ErrAborted) {
			reason := pnio.AbortConsumerDHT
			if !errors.Is(err, profinet.ErrTimeout) {
				reason = pnio.AbortUser
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_ = a.alarmLow.SendErr(ctx, pnio.AbortStatus(reason))
			cancel()
		}
	}
	if a.provTask != nil {
		d.c.sched.Remove(a.provTask)
	}
	if a.wdTask != nil {
		d.c.sched.Remove(a.wdTask)
	}
	if a.consumer != nil {
		d.c.disp.Remove(a.consumer)
	}
	if a.alarmHigh != nil {
		a.alarmHigh.Close()
	}
	if a.alarmLow != nil {
		a.alarmLow.Close()
	}
	d.c.unregisterAR(a.uuid)
	d.mu.Lock()
	if a.provider != nil {
		d.lastStats.Provider = a.provider.Stats()
		d.lastStats.Consumer = a.consumer.Stats()
		d.lastStats.Cycle = a.provTask.Stats()
	}
	if d.ar == a {
		d.ar = nil
	}
	for _, in := range d.inputs {
		in.iops = pnio.IOxSBad
	}
	d.mu.Unlock()
}
