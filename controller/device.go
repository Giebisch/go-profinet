// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
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
	// DataHoldFactor and WatchdogFactor are in cycles; zero means 10.
	// PLCs commonly use 3, which a general-purpose operating system does
	// not hold: its scheduling stalls of 10 to 20 ms expire a hold of 3
	// cycles of 4 ms (see docs/CYCLIC.md).
	DataHoldFactor, WatchdogFactor uint16
	// Advanced selects the advanced startup mode with RTClass 2;
	// otherwise the legacy startup with RTClass 1 is used.
	Advanced bool
	// RTClass states the RT class of the IOCRs; zero means the class of
	// the startup mode (RTClass2 with Advanced, RTClass1 without).
	// AddDevice refuses any other class with profinet.ErrUnsupported:
	// the controller has a data path for RT_CLASS_1/2 on Ethernet only,
	// not for RT_CLASS_UDP, RT_CLASS_3 or RT_CLASS_STREAM.
	RTClass pnio.RTClass
	// ActivityTimeout is the CM initiator activity timeout; zero means
	// 10 s.
	ActivityTimeout time.Duration
	// RTATimeoutFactor (units of 100 ms) and RTARetries configure the
	// alarm transport; zero means 5 and 3.
	RTATimeoutFactor, RTARetries uint16
	// Parameters are written after Connect and before PrmEnd.
	Parameters []Parameter
	// PeerChecks are the expected neighbors of the device's ports; they
	// are written (PDPortDataCheck) before Parameters, and the device
	// reports a remote mismatch diagnosis while a port's neighbor differs.
	PeerChecks []PeerCheck
	// MRP gives the device a media redundancy role (MRP records written
	// after the peer checks, before Parameters); nil writes none.
	MRP *MRPSetting
	// SystemRedundancy makes the AR an IOCARSR of an S2 AR set:
	// EXPERIMENTAL, not interoperable (sr.go).
	SystemRedundancy *SRSetting
	// RPCTimeout bounds each RPC call; zero means 5 s.
	RPCTimeout time.Duration
	// ReconnectMin and ReconnectMax bound the backoff between attempts;
	// zero means 200 ms and 10 s. ReconnectJitter (0 to 1) shortens each
	// wait randomly by up to that fraction, so that devices that failed
	// together do not reconnect together; zero waits exactly.
	ReconnectMin, ReconnectMax time.Duration
	ReconnectJitter            float64
	// OnAlarm receives every alarm, on the alarm goroutine of the AR; it
	// must not block.
	OnAlarm func(AlarmEvent)
	// OnState receives every state change, before WaitState returns for it.
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
	// Hellos is the number of connections that a DCP Hello of the
	// device started, without an Identify.
	Hellos   uint64
	Provider rt.ProviderStats
	Consumer rt.ConsumerStats
	Cycle    rt.TaskStats
	Alarms   uint64
}

// Device is a device of the controller.
type Device struct {
	c   *Controller
	cfg DeviceConfig

	mu sync.Mutex
	// reparam serializes Reparameterize.
	reparam sync.Mutex
	state   State
	lastErr error
	mac     ethernet.MAC
	ip      dcp.IPSuite
	ar      *arState
	diff    *pnio.ModuleDiffBlock
	// srPrimary is the IOCARSR's primary request, srAck the device's
	// last redundancy acknowledge (sr.go).
	srPrimary bool
	srAck     rt.DataStatus
	srAckSeen bool
	waiters   []chan struct{}
	// notified is the last state whose OnState callback has run: WaitState
	// waits for it. setMu orders the transitions and their callbacks.
	notified State
	setMu    sync.Mutex
	outputs  map[subKey]*ioData
	inputs   map[subKey]*ioData
	iocs     map[subKey]uint8
	connects atomic.Uint64
	aborts   atomic.Uint64
	hellos   atomic.Uint64
	// hello is the identity of the device's last DCP Hello and helloAt
	// its arrival (guarded by mu); helloWake ends the wait between two
	// connection attempts.
	hello     *dcp.Identity
	helloAt   time.Time
	helloWake chan struct{}
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
	// reAppReady is the ApplicationReady a re-parameterization awaits
	// (guarded by Device.mu); nil when none runs.
	reAppReady chan struct{}
	// dead is closed by fail.
	dead chan struct{}
	// plugPending are the submodules handed over by a Released alarm
	// whose ApplicationReady (plug) is awaited, by alarm sequence number
	// (guarded by Device.mu).
	plugPending map[uint16]subKey
	failed      chan error
	failOnce    sync.Once
	failErr     error // set once by fail, before the send on failed
}

func (a *arState) fail(err error) {
	a.failOnce.Do(func() {
		a.failErr = err
		if a.dead != nil {
			close(a.dead)
		}
		a.failed <- err
	})
}

// startupClass is the RT class of the IOCRs in a startup mode: the legacy
// encoding RTClass1 in legacy startup, RTClass2 in advanced startup.
func startupClass(advanced bool) pnio.RTClass {
	if advanced {
		return pnio.RTClass2
	}
	return pnio.RTClass1
}

// defaultHoldFactor is the default watchdog and data hold factor.
const defaultHoldFactor = 10

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
	if cfg.SystemRedundancy != nil {
		if err := cfg.SystemRedundancy.validate(); err != nil {
			return nil, err
		}
	}
	checks, err := peerCheckParameters(cfg.Model, cfg.PeerChecks)
	if err != nil {
		return nil, err
	}
	mrps, err := mrpParameters(cfg.Model, cfg.MRP)
	if err != nil {
		return nil, err
	}
	cfg.Parameters = append(append(checks, mrps...), cfg.Parameters...)
	if want := startupClass(cfg.Advanced); cfg.RTClass != 0 && cfg.RTClass != want {
		return nil, fmt.Errorf("controller: %w: device %q: %s (this startup mode uses %s)", profinet.ErrUnsupported, cfg.StationName, cfg.RTClass, want)
	}
	if cfg.SendClockFactor == 0 {
		cfg.SendClockFactor = 32
	}
	if cfg.ReductionRatio == 0 {
		cfg.ReductionRatio = 4
	}
	if cfg.DataHoldFactor == 0 {
		cfg.DataHoldFactor = defaultHoldFactor
	}
	if cfg.WatchdogFactor == 0 {
		cfg.WatchdogFactor = defaultHoldFactor
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
	if cfg.ReconnectJitter < 0 || cfg.ReconnectJitter > 1 {
		return nil, fmt.Errorf("controller: %w: device %q: reconnect jitter %v (0 to 1)", profinet.ErrInvalidConfig, cfg.StationName, cfg.ReconnectJitter)
	}
	d := &Device{
		c:         c,
		cfg:       cfg,
		srPrimary: cfg.SystemRedundancy != nil && cfg.SystemRedundancy.Primary,
		outputs:   map[subKey]*ioData{},
		inputs:    map[subKey]*ioData{},
		iocs:      map[subKey]uint8{},
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
		helloWake: make(chan struct{}, 1),
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
		if d.notified == s {
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
	d.setMu.Lock()
	defer d.setMu.Unlock()
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
	d.mu.Unlock()
	d.c.log.Debugf("controller: %s: %s → %s", d.cfg.StationName, from, to)
	// OnState runs before WaitState returns, so that a caller of WaitState
	// sees every callback of the transitions it waited for.
	d.c.met.OnStateChange(d.cfg.StationName, from.String(), to.String(), err)
	if d.cfg.OnState != nil {
		d.cfg.OnState(from, to, err)
	}
	d.mu.Lock()
	d.notified = to
	ws := d.waiters
	d.waiters = nil
	d.mu.Unlock()
	for _, w := range ws {
		close(w)
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
	s.Hellos = d.hellos.Load()
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
	start := time.Now()
	b, err := a.records.Read(ctx, a.uuid, rec, maxLen)
	err = d.lost(a, err)
	d.c.met.OnRecord(d.cfg.StationName, "read", uint16(rec.Index), time.Since(start), err)
	return b, err
}

// WriteRecord writes a record within the AR.
func (d *Device) WriteRecord(ctx context.Context, rec Record, data []byte) error {
	a, err := d.records()
	if err != nil {
		return err
	}
	start := time.Now()
	err = d.lost(a, a.records.Write(ctx, a.uuid, rec, data))
	d.c.met.OnRecord(d.cfg.StationName, "write", uint16(rec.Index), time.Since(start), err)
	return err
}

// WriteMultiple writes several records in one call.
func (d *Device) WriteMultiple(ctx context.Context, items []WriteItem) ([]pnio.Status, error) {
	a, err := d.records()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	sts, err := a.records.WriteMultiple(ctx, a.uuid, items)
	// A WriteMultiple the device refuses as a whole answers one status.
	if err == nil && len(sts) == 1 {
		if lerr := d.lost(a, &pnio.StatusError{Op: "write multiple", Status: sts[0]}); errors.Is(lerr, profinet.ErrAborted) {
			err = lerr
		}
	} else {
		err = d.lost(a, err)
	}
	d.c.met.OnRecord(d.cfg.StationName, "write", uint16(pnio.IndexWriteMultiple), time.Since(start), err)
	return sts, err
}

// ReadIM0 reads the device's I&M0.
func (d *Device) ReadIM0(ctx context.Context) (pnio.IM0, error) {
	a, err := d.records()
	if err != nil {
		return pnio.IM0{}, err
	}
	start := time.Now()
	im0, err := a.records.ReadIM0(ctx, a.uuid, 0, model.SubslotDAP)
	err = d.lost(a, err)
	d.c.met.OnRecord(d.cfg.StationName, "read", uint16(pnio.IndexIM0), time.Since(start), err)
	return im0, err
}

// ReadDiagnosis reads the diagnosis of the whole device.
func (d *Device) ReadDiagnosis(ctx context.Context) ([]pnio.DiagnosisData, error) {
	a, err := d.records()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	diag, err := a.records.ReadDiagnosis(ctx, a.uuid, 0, 0, false)
	err = d.lost(a, err)
	d.c.met.OnRecord(d.cfg.StationName, "read", uint16(pnio.IndexDiagDMQSDevice), time.Since(start), err)
	return diag, err
}

// lost ends the AR when the device answered a call within it with "AR UUID
// unknown": the device has closed the AR (after a watchdog or data hold
// expiry it may not have told us, as an ERR PDU can be lost), so the
// lifecycle reconnects at once instead of waiting for its own watchdog. The
// error is returned, and also matches profinet.ErrAborted.
func (d *Device) lost(a *arState, err error) error {
	var se *pnio.StatusError
	if !errors.As(err, &se) || se.Status.Decode != pnio.DecodePNIO || se.Status.Code1 != pnio.Code1CMRPC || se.Status.Code2 != pnio.CMRPCARUUIDUnknown {
		return err
	}
	err = fmt.Errorf("controller: %s: %w: the device no longer knows the AR: %w", d.cfg.StationName, profinet.ErrAborted, err)
	a.fail(err)
	return err
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
	bo := profinet.Backoff{Min: d.cfg.ReconnectMin, Max: d.cfg.ReconnectMax, Jitter: d.cfg.ReconnectJitter}
	attempt := 0
	for {
		if d.stopping() {
			d.setState(StateStopped, nil)
			return
		}
		ran, err := d.session()
		if ran {
			attempt = 0
		}
		attempt++
		backoff := bo.Delay(attempt, rand.Float64())
		if d.stopping() {
			d.setState(StateStopped, nil)
			return
		}
		d.setState(StateOffline, err)
		if err != nil {
			// Lifecycle errors usually name the device already.
			msg := err.Error()
			if !strings.Contains(msg, d.cfg.StationName) {
				msg = "controller: " + d.cfg.StationName + ": " + msg
			}
			d.c.log.Warnf("%s (retry in %s)", msg, backoff.Round(time.Millisecond))
			d.c.met.OnRetry(d.cfg.StationName, attempt, backoff, err)
		}
		// A Hello that arrived while the device was connecting or
		// running left a wake-up behind: it counts only while the Hello
		// is fresh enough to stand in for an Identify.
		if d.freshHello() {
			d.c.log.Debugf("controller: %s: Hello: reconnecting now", d.cfg.StationName)
			continue
		}
		select {
		case <-d.helloWake:
		default:
		}
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-d.helloWake:
			t.Stop()
			d.c.log.Debugf("controller: %s: Hello: reconnecting now", d.cfg.StationName)
		case <-d.stop:
			t.Stop()
		}
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
	// A Hello that arrived while connecting has served its purpose: it is
	// not to bring back the device after this AR ends (by then the device
	// may be another one, or gone).
	d.mu.Lock()
	d.hello = nil
	d.mu.Unlock()
	select {
	case <-d.helloWake:
	default:
	}
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

// helloFresh is how long a DCP Hello stands in for an Identify.
const helloFresh = 3 * time.Second

// offerHello records a Hello of the device and, if it waits between two
// connection attempts, wakes it.
func (d *Device) offerHello(id dcp.Identity) {
	d.mu.Lock()
	d.hello, d.helloAt = &id, time.Now()
	d.mu.Unlock()
	select {
	case d.helloWake <- struct{}{}:
	default:
	}
}

// freshHello reports whether a recent Hello with an IP address waits to
// be used.
func (d *Device) freshHello() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hello != nil && time.Since(d.helloAt) <= helloFresh && !d.hello.IP.IsZero()
}

// takeHello returns the identity of a recent Hello with an IP address and
// forgets it.
func (d *Device) takeHello() (dcp.Identity, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	h, at := d.hello, d.helloAt
	d.hello = nil
	if h == nil || time.Since(at) > helloFresh || h.IP.IsZero() {
		return dcp.Identity{}, false
	}
	return *h, true
}

// resolve finds the device with DCP, from a recent Hello or with an
// Identify, and makes sure it has the configured IP suite.
func (d *Device) resolve(ctx context.Context) (dcp.Identity, error) {
	id, fromHello := d.takeHello()
	if fromHello {
		d.c.log.Infof("controller: %s: resolved by its Hello: %s", d.cfg.StationName, id.MAC)
		d.hellos.Add(1)
	} else {
		var err error
		if id, err = d.c.dcp.IdentifyName(ctx, d.cfg.StationName, time.Second); err != nil {
			return id, fmt.Errorf("controller: resolve %s: %w", d.cfg.StationName, err)
		}
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
	cls := startupClass(cfg.Advanced)
	props := pnio.ARPropStateActive | pnio.ARPropPrmServerCMInit
	if cfg.Advanced {
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
		plugPending: map[uint16]subKey{},
		uuid:        rpc.NewRandomUUID(),
		sessionKey:  uint16(sessionKeys.Add(1)),
		inFrameID:   inID,
		inLayout:    inL,
		outLayout:   outL,
		class:       cls,
		localRef:    localRef,
		appReady:    make(chan struct{}),
		failed:      make(chan error, 1),
		dead:        make(chan struct{}),
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
	arType := pnio.ARTypeIOCARSingle
	var srBlocks []pnio.RawBlock
	if sr := cfg.SystemRedundancy; sr != nil {
		id, blk, err := sr.srBlocks()
		if err != nil {
			return nil, err
		}
		arType, a.uuid, srBlocks = pnio.ARTypeIOCARSR, id.UUID(), []pnio.RawBlock{blk}
	}
	req := &pnio.ConnectRequest{
		Other: srBlocks,
		AR: pnio.ARBlockReq{
			Type: arType, ARUUID: a.uuid, SessionKey: a.sessionKey,
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
	start := time.Now()
	resp, err := a.records.Connect(cctx, req)
	cancel()
	d.c.met.OnConnect(cfg.StationName, time.Since(start), err)
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
// report as wrong, missing or locked (owned by another IO controller's or
// a supervisor's AR on a shared device).
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
				switch s.State & (0xF << 7) {
				case pnio.SubmStateARInfoSuperLock, pnio.SubmStateARInfoCtrlLock, pnio.SubmStateARInfoSupvLock:
					delete(proper, subKey{m.Slot, s.Subslot})
					continue
				default:
				}
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
	d.c.met.OnAlarm(d.cfg.StationName, n.Type.String(), high)
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
		return
	}
	if n.Type == pnio.AlarmReleased {
		go d.takeReleased(a, n)
	}
}

// takeReleased takes over a submodule a shared device handed to this AR
// with a Released alarm: its parameters are written, then PrmEnd (plug)
// with the alarm's sequence number; the device answers with
// ApplicationReady (plug), from which on the submodule's data count.
func (d *Device) takeReleased(a *arState, n pnio.AlarmNotification) {
	k := subKey{n.Slot, n.Subslot}
	if _, ok := d.cfg.Model.Submodule(n.Slot, n.Subslot); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(d.c.ctx, d.cfg.RPCTimeout)
	defer cancel()
	for _, p := range d.parameters() {
		if p.Slot != n.Slot || p.Subslot != n.Subslot {
			continue
		}
		if err := a.records.Write(ctx, a.uuid, Record{API: model.API, Slot: p.Slot, Subslot: p.Subslot, Index: p.Index}, p.Data); err != nil {
			d.c.log.Warnf("controller: %s: parameter %d/%d %s of a released submodule: %v", d.cfg.StationName, p.Slot, p.Subslot, p.Index, err)
			return
		}
	}
	d.mu.Lock()
	a.plugPending[n.Sequence()] = k
	d.mu.Unlock()
	err := a.records.Control(ctx, pnio.ControlBlock{Type: pnio.BlockPrmEndPlugReq, ARUUID: a.uuid, SessionKey: a.sessionKey,
		Command: pnio.CmdPrmEnd, AlarmSequence: n.Sequence()})
	if err != nil {
		d.mu.Lock()
		delete(a.plugPending, n.Sequence())
		d.mu.Unlock()
		d.c.log.Warnf("controller: %s: PrmEnd (plug) of submodule %d/%d: %v", d.cfg.StationName, n.Slot, n.Subslot, err)
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
	if cfg.SystemRedundancy != nil {
		d.mu.Lock()
		primary := d.srPrimary
		d.mu.Unlock()
		a.provider.SetDataStatus(srDataStatus(primary))
	}
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
	if d.cfg.SystemRedundancy != nil {
		d.mu.Lock()
		d.srAck, d.srAckSeen = s&(rt.RedundancyMask|rt.Redundancy), true
		d.mu.Unlock()
	}
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
	for _, p := range d.parameters() {
		// A submodule another controller owns (shared device) is not
		// this one's to parameterize; its parameters are written when it
		// is handed over (takeReleased).
		if _, own := a.proper[subKey{p.Slot, p.Subslot}]; !own {
			if _, inModel := d.cfg.Model.Submodule(p.Slot, p.Subslot); inModel {
				continue
			}
		}
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
	if cb.Type == pnio.BlockAppReadyPlugReq {
		// A submodule handed over (takeReleased) is now this AR's.
		d.mu.Lock()
		k, ok := a.plugPending[cb.AlarmSequence]
		if ok {
			delete(a.plugPending, cb.AlarmSequence)
			a.proper[k] = true
		}
		d.mu.Unlock()
		if ok {
			d.c.log.Infof("controller: %s: submodule %d/%d taken over", d.cfg.StationName, k.slot, k.subslot)
			return true
		}
		// Otherwise it stands for the AR's ApplicationReady, as some
		// devices send it.
	}
	d.mu.Lock()
	re := a.reAppReady
	a.reAppReady = nil
	d.mu.Unlock()
	if re != nil {
		close(re)
		return true
	}
	a.appOnce.Do(func() { close(a.appReady) })
	return true
}

// Reparameterize writes parameters to the device while its AR runs: a
// PrmBegin, the writes, a PrmEnd, then it waits for the device's
// ApplicationReady (bounded by DeviceConfig.ActivityTimeout). Cyclic data
// goes on throughout; a device stops sending alarms until its
// ApplicationReady is acknowledged. The parameters also replace those of
// the same slot, subslot and index in the device's configuration, so that
// a reconnect writes them again. Only one re-parameterization of a device
// runs at a time.
//
// It returns profinet.ErrInvalidState unless the device is running. A
// PrmBegin refused for the device's state (it is completing the
// ApplicationReady of its startup) is sent again for up to a second. When
// a step fails the AR is left as the device keeps it (a device that
// refused PrmBegin still runs with its old parameters); a device that
// sends no ApplicationReady makes it fail with profinet.ErrTimeout and
// aborts the AR (profinet.ErrAborted when the AR ends first), which then
// reconnects.
func (d *Device) Reparameterize(ctx context.Context, params []Parameter) error {
	d.reparam.Lock()
	defer d.reparam.Unlock()
	d.mu.Lock()
	a := d.ar
	running := a != nil && d.state == StateRunning
	d.mu.Unlock()
	if !running {
		return fmt.Errorf("controller: %s: re-parameterize: %w: not running", d.cfg.StationName, profinet.ErrInvalidState)
	}
	ready := make(chan struct{})
	d.mu.Lock()
	a.reAppReady = ready
	d.mu.Unlock()
	clear := func() {
		d.mu.Lock()
		if a.reAppReady == ready {
			a.reAppReady = nil
		}
		d.mu.Unlock()
	}
	call := func(what string, f func(ctx context.Context) error) error {
		cctx, cancel := context.WithTimeout(ctx, d.cfg.RPCTimeout)
		defer cancel()
		if err := f(cctx); err != nil {
			clear()
			return fmt.Errorf("controller: %s: re-parameterize: %s: %w", d.cfg.StationName, what, err)
		}
		return nil
	}
	// A device that is still completing its ApplicationReady (the
	// controller runs a moment before the device enters Data) answers a
	// state conflict: PrmBegin is sent again, for up to a second.
	if err := call("PrmBegin", func(ctx context.Context) error {
		deadline := time.Now().Add(time.Second)
		for {
			err := a.records.Control(ctx, pnio.ControlBlock{Type: pnio.BlockPrmBeginReq, ARUUID: a.uuid, SessionKey: a.sessionKey, Command: pnio.CmdPrmBegin})
			if err == nil || !errors.Is(err, profinet.ErrInvalidState) || time.Now().After(deadline) {
				return err
			}
			select {
			case <-time.After(20 * time.Millisecond):
			case <-a.dead:
				return err
			case <-ctx.Done():
				return err
			}
		}
	}); err != nil {
		return err
	}
	for _, p := range params {
		if err := call(fmt.Sprintf("parameter %d/%d %s", p.Slot, p.Subslot, p.Index), func(ctx context.Context) error {
			return a.records.Write(ctx, a.uuid, Record{API: model.API, Slot: p.Slot, Subslot: p.Subslot, Index: p.Index}, p.Data)
		}); err != nil {
			return err
		}
	}
	d.mu.Lock()
	d.cfg.Parameters = mergeParameters(d.cfg.Parameters, params)
	d.mu.Unlock()
	if err := call("PrmEnd", func(ctx context.Context) error {
		return a.records.Control(ctx, pnio.ControlBlock{Type: pnio.BlockPrmEndReq, ARUUID: a.uuid, SessionKey: a.sessionKey, Command: pnio.CmdPrmEnd})
	}); err != nil {
		return err
	}
	t := time.NewTimer(d.cfg.ActivityTimeout)
	defer t.Stop()
	select {
	case <-ready:
		d.c.log.Infof("controller: %s: re-parameterized (%d parameters)", d.cfg.StationName, len(params))
		return nil
	case <-a.dead:
		clear()
		return fmt.Errorf("controller: %s: re-parameterize: %w", d.cfg.StationName, profinet.ErrAborted)
	case <-t.C:
		clear()
		err := fmt.Errorf("controller: %s: re-parameterize: %w: no ApplicationReady within %s", d.cfg.StationName, profinet.ErrTimeout, d.cfg.ActivityTimeout)
		a.fail(err)
		return err
	case <-ctx.Done():
		clear()
		return ctx.Err()
	}
}

// parameters returns the device's parameters (Reparameterize changes
// them).
func (d *Device) parameters() []Parameter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg.Parameters
}

// mergeParameters replaces the parameters of the same slot, subslot and
// index and appends the others.
func mergeParameters(old, upd []Parameter) []Parameter {
	out := append([]Parameter(nil), old...)
next:
	for _, p := range upd {
		p.Data = append([]byte(nil), p.Data...)
		for i := range out {
			if out[i].Slot == p.Slot && out[i].Subslot == p.Subslot && out[i].Index == p.Index {
				out[i] = p
				continue next
			}
		}
		out = append(out, p)
	}
	return out
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
		d.c.met.OnAbort(d.cfg.StationName, err)
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
	// The statistics before d.mu: a frame still being handled holds the
	// consumer's lock and waits for d.mu (storeInputs).
	var last DeviceStats
	if a.provider != nil {
		last.Provider, last.Consumer, last.Cycle = a.provider.Stats(), a.consumer.Stats(), a.provTask.Stats()
	}
	d.mu.Lock()
	if a.provider != nil {
		d.lastStats.Provider, d.lastStats.Consumer, d.lastStats.Cycle = last.Provider, last.Consumer, last.Cycle
	}
	if d.ar == a {
		d.ar = nil
	}
	for _, in := range d.inputs {
		in.iops = pnio.IOxSBad
	}
	d.mu.Unlock()
}
