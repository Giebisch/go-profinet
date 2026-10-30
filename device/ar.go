// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/alarm"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
)

// ar is one application relationship. Fields set at Connect are immutable;
// state, lastActivity and the alarm bookkeeping are guarded by mu.
type ar struct {
	d *Device

	uuid       rpc.UUID
	typ        pnio.ARType
	sessionKey uint16
	ctrlMAC    ethernet.MAC
	ctrlAddr   net.Addr
	ctrlObject rpc.UUID
	ctrlName   string
	ctrlPort   int
	cmiTimeout time.Duration

	expected []pnio.ExpectedSubmoduleBlockReq
	// match are the submodules whose expected and real ident match;
	// which of them the AR owns is in Device.owner.
	match map[key]bool
	// seq orders the IOCARs by their Connect: ownership released by one
	// goes to the earliest other AR that expects the submodule.
	seq uint64

	inLayout, outLayout rt.Layout
	provider            *rt.Provider
	consumer            *rt.Consumer
	provTask, wdTask    *rt.Task
	cycle               time.Duration

	alarmHigh, alarmLow *alarm.Endpoint
	alarmRef            uint16
	maxAlarmData        uint16
	alarmAckTimeout     time.Duration

	mu    sync.Mutex
	state ARState
	// events are the transitions not yet reported to the handler;
	// notifying is set while a goroutine reports them (see transition).
	events       []stateEvent
	notifying    bool
	lastActivity time.Time
	reason       pnio.AbortReason
	alarmSeq     uint16
	ackWait      map[uint16]chan pnio.AlarmAck
	iocs         map[key]uint8 // the controller's consumer status of our inputs
	cmiTimer     *time.Timer
	// plugs are the submodules handed over to the AR whose Released
	// alarm still has to go out; plugBusy is set while plugs are worked
	// off (owner.go), and prmEndPlug carries the alarm sequence numbers
	// of the controller's PrmEnd (plug) meanwhile.
	plugs      []key
	plugBusy   bool
	prmEndPlug chan uint16
	// heir is the AR of the same controller that replaces this one; it
	// takes over its submodules (guarded by the device's imgMu).
	heir *ar
	// sr is set for an IOCARSR (system redundancy, sr.go).
	sr *srFields
}

func (a *ar) info() ARInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.infoLocked()
}

func (a *ar) infoLocked() ARInfo {
	i := ARInfo{
		UUID: a.uuid, Type: a.typ, SessionKey: a.sessionKey, State: a.state,
		ControllerMAC: a.ctrlMAC, ControllerAddr: a.ctrlAddr, ControllerName: a.ctrlName,
		CycleNanos: int64(a.cycle), CloseReason: a.reason,
	}
	if a.provider != nil {
		i.InputFrameID = a.provider.FrameID()
	}
	if a.consumer != nil {
		i.OutputFrameID = a.consumer.FrameID()
	}
	return i
}

// transition moves the AR to state to, if the state machine allows it.
func (a *ar) transition(to ARState) error {
	a.mu.Lock()
	from := a.state
	if !CanTransition(from, to) {
		a.mu.Unlock()
		return fmt.Errorf("device: AR %s: %w: %s → %s", a.uuid, profinet.ErrInvalidState, from, to)
	}
	a.state = to
	a.events = append(a.events, stateEvent{info: a.infoLocked(), from: from, to: to})
	a.d.log.Debugf("device: AR %s: %s → %s", a.uuid, from, to)
	if a.notifying {
		// Another goroutine is reporting transitions: it reports this one
		// after those before it.
		a.mu.Unlock()
		return nil
	}
	// Report the transitions one at a time and in their order, without
	// holding the lock: a handler may call into the device, even cause
	// the next transition, which is then queued and reported after this.
	a.notifying = true
	for len(a.events) > 0 {
		ev := a.events[0]
		a.events = a.events[1:]
		a.mu.Unlock()
		a.d.met.OnStateChange(ev.info.ControllerName, ev.from.String(), ev.to.String(), nil)
		a.d.handler.StateChanged(ev.info, ev.from, ev.to)
		a.mu.Lock()
	}
	a.notifying = false
	a.mu.Unlock()
	return nil
}

// stateEvent is a transition to report to the handler.
type stateEvent struct {
	info     ARInfo
	from, to ARState
}

func (a *ar) currentState() ARState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *ar) touch() {
	a.mu.Lock()
	a.lastActivity = time.Now()
	a.mu.Unlock()
}

// validateIOCR checks an IOCR request; the returned status names the
// faulty field the way p-net and the standard number them.
func (d *Device) validateIOCR(c *pnio.IOCRBlockReq) *pnio.Status {
	bad := func(field uint8) *pnio.Status {
		s := pnio.ServiceStatus(pnio.CodeConnect, pnio.Code1IOCRBlock, field)
		return &s
	}
	if c.Type != pnio.IOCRInput && c.Type != pnio.IOCROutput {
		return bad(4)
	}
	// The RT class goes before LT: an RT_CLASS_UDP IOCR carries LT 0x0800
	// consistently, and what the device refuses is its RTClass. ErrorCode2
	// 7 is "Error in Parameter IOCRProperties" (Wireshark's checking rules
	// for IOCRBlockReq, pn_io_error_code2_pnio_2 in packet-pn.c), so a
	// Connect for RT_CLASS_UDP, RT_CLASS_3 or RT_CLASS_STREAM is answered
	// DB 81 02 07: this device has a data path for RT_CLASS_1/2 only.
	cls := c.RTClass()
	if cls != pnio.RTClass1 && cls != pnio.RTClass2 {
		return bad(7)
	}
	if c.LT != pnio.LT {
		return bad(6)
	}
	if c.DataLength < rt.MinDataLength || c.DataLength > rt.MaxDataLength {
		return bad(8)
	}
	lo, hi, _ := pnio.FrameIDRange(cls)
	if c.Type == pnio.IOCRInput && (c.FrameID < lo || c.FrameID > hi) {
		return bad(9)
	}
	if c.Type == pnio.IOCROutput && c.FrameID != pnio.FrameIDAssign && (c.FrameID < lo || c.FrameID > hi) {
		return bad(9)
	}
	scf := c.SendClockFactor
	if scf == 0 || scf > 128 || scf&(scf-1) != 0 {
		return bad(10)
	}
	rr := c.ReductionRatio
	if rr == 0 || rr > 512 || rr&(rr-1) != 0 || rr >= 256 && scf > 64 || rr == 512 && scf > 32 {
		return bad(11)
	}
	// MinDeviceInterval bounds the cycle, not the send clock; p-net, too,
	// refuses a shorter cycle for its ReductionRatio.
	if int(scf)*int(rr) < int(d.model.Capabilities.MinDeviceInterval) {
		return bad(11)
	}
	if c.Phase == 0 || c.Phase > rr {
		return bad(12)
	}
	if c.WatchdogFactor == 0 || c.WatchdogFactor > 0x1E00 {
		return bad(15)
	}
	if c.DataHoldFactor == 0 || c.DataHoldFactor > 0x1E00 {
		return bad(16)
	}
	if !c.MulticastMAC.IsZero() {
		return bad(18)
	}
	return nil
}

func (d *Device) validateAlarmCR(a *pnio.AlarmCRBlockReq) *pnio.Status {
	bad := func(field uint8) *pnio.Status {
		s := pnio.ServiceStatus(pnio.CodeConnect, pnio.Code1AlarmCR, field)
		return &s
	}
	switch {
	case a.Type != 1:
		return bad(4)
	case a.TransportUDP():
		// Alarms over UDP (RTA_CLASS_UDP), before LT, which is then 0x0800:
		// ErrorCode2 6 is "Error in Parameter AlarmCRProperties" (Wireshark,
		// pn_io_error_code2_pnio_4).
		return bad(6)
	case a.LT != pnio.LT:
		return bad(5)
	case a.RTATimeoutFactor == 0 || a.RTATimeoutFactor > 100:
		return bad(7)
	case a.RTARetries < 3 || a.RTARetries > 15:
		return bad(8)
	case a.MaxAlarmDataLength < 200 || a.MaxAlarmDataLength > 1432:
		return bad(10)
	}
	return nil
}

// shapes returns the IO shapes of the expected submodules.
func shapes(exp []pnio.ExpectedSubmoduleBlockReq) []rt.SubmoduleIO {
	var out []rt.SubmoduleIO
	for _, b := range exp {
		for _, m := range b.Modules {
			for _, s := range m.Submodules {
				out = append(out, rt.SubmoduleIO{API: m.API, Slot: m.Slot, Subslot: s.Subslot, Type: s.Type(), InputLen: s.InputLength(), OutputLen: s.OutputLength()})
			}
		}
	}
	return out
}

// moduleDiff compares the expected configuration with the real one. It
// returns the ModuleDiffBlock (nil when everything matches) and the
// submodules that match. The submodules in locked match but are owned by
// another IO controller's AR: they are reported as locked.
func (d *Device) moduleDiff(exp []pnio.ExpectedSubmoduleBlockReq, appReadyPending bool, locked map[key]bool) (*pnio.ModuleDiffBlock, map[key]bool) {
	proper := map[key]bool{}
	var apis []pnio.DiffAPI
	addModule := func(api uint32, m pnio.DiffModule) {
		for i := range apis {
			if apis[i].API == api {
				apis[i].Modules = append(apis[i].Modules, m)
				return
			}
		}
		apis = append(apis, pnio.DiffAPI{API: api, Modules: []pnio.DiffModule{m}})
	}
	for _, b := range exp {
		for _, em := range b.Modules {
			rm, ok := d.model.Module(em.Slot)
			if em.API != model.API || !ok {
				dm := pnio.DiffModule{Slot: em.Slot, IdentNumber: em.IdentNumber, State: pnio.ModuleStateNoModule}
				addModule(em.API, dm)
				continue
			}
			dm := pnio.DiffModule{Slot: em.Slot, IdentNumber: rm.IdentNumber, State: pnio.ModuleStateProper}
			if rm.IdentNumber != em.IdentNumber {
				dm.State = pnio.ModuleStateWrong
			}
			for _, es := range em.Submodules {
				rs, ok := rm.Submodule(es.Subslot)
				st := pnio.SubmStateFormat
				switch {
				case !ok:
					st |= pnio.SubmStateIdentNoSubmodule
					dm.Submodules = append(dm.Submodules, pnio.DiffSubmodule{Subslot: es.Subslot, IdentNumber: es.IdentNumber, State: st})
					continue
				case rs.IdentNumber != es.IdentNumber || rs.InputLen != es.InputLength() || rs.OutputLen != es.OutputLength():
					st |= pnio.SubmStateIdentWrong
					dm.Submodules = append(dm.Submodules, pnio.DiffSubmodule{Subslot: es.Subslot, IdentNumber: rs.IdentNumber, State: st})
					continue
				}
				if dm.State == pnio.ModuleStateProper {
					proper[key{em.Slot, es.Subslot}] = true
				}
				if locked[key{em.Slot, es.Subslot}] {
					// Another IO controller's AR owns it (shared device).
					st |= pnio.SubmStateARInfoCtrlLock
					dm.Submodules = append(dm.Submodules, pnio.DiffSubmodule{Subslot: es.Subslot, IdentNumber: rs.IdentNumber, State: st})
					continue
				}
				if appReadyPending {
					// Every submodule of the AR waits for ApplicationReady.
					st |= pnio.SubmStateARInfoAppReady
					dm.Submodules = append(dm.Submodules, pnio.DiffSubmodule{Subslot: es.Subslot, IdentNumber: rs.IdentNumber, State: st})
				}
			}
			if dm.State != pnio.ModuleStateProper || len(dm.Submodules) > 0 {
				addModule(em.API, dm)
			}
		}
	}
	if len(apis) == 0 {
		return nil, proper
	}
	sort.Slice(apis, func(i, j int) bool { return apis[i].API < apis[j].API })
	return &pnio.ModuleDiffBlock{APIs: apis}, proper
}

// connect handles a Connect request.
func (d *Device) connect(ctx context.Context, from net.Addr, req *pnio.ConnectRequest) (*pnio.ConnectResponse, error) {
	arb := &req.AR
	fail := func(code1, code2 uint8) (*pnio.ConnectResponse, error) {
		return nil, &pnio.StatusError{Op: "connect", Status: pnio.ServiceStatus(pnio.CodeConnect, code1, code2)}
	}
	switch arb.Type {
	case pnio.ARTypeIOCARSingle:
	case pnio.ARTypeIOCARSR:
		if d.cfg.SystemRedundancy == nil {
			return fail(pnio.Code1ARBlock, 4)
		}
	case pnio.ARTypeIOSAR:
		if d.cfg.RejectSupervisorAR {
			return fail(pnio.Code1ARBlock, 4)
		}
	default:
		return fail(pnio.Code1ARBlock, 4)
	}
	if !pnio.IsObjectUUID(arb.InitiatorObject) {
		return fail(pnio.Code1ARBlock, 8)
	}
	if arb.ActivityTimeoutFactor == 0 || arb.ActivityTimeoutFactor > 1000 {
		return fail(pnio.Code1ARBlock, 10)
	}
	if arb.ARUUID.IsNil() {
		return fail(pnio.Code1ARBlock, 5)
	}
	iosar := arb.Type == pnio.ARTypeIOSAR
	var in, out *pnio.IOCRBlockReq
	for i := range req.IOCRs {
		c := &req.IOCRs[i]
		if st := d.validateIOCR(c); st != nil {
			return nil, &pnio.StatusError{Op: "connect", Status: *st}
		}
		switch {
		case c.Type == pnio.IOCRInput && in == nil:
			in = c
		case c.Type == pnio.IOCROutput && out == nil:
			out = c
		default:
			return fail(pnio.Code1IOCRBlock, 4)
		}
	}
	if !iosar && (in == nil || out == nil) {
		return fail(pnio.Code1CMRPC, pnio.CMRPCIOCRMissing)
	}
	if !iosar && req.AlarmCR == nil {
		return fail(pnio.Code1CMRPC, pnio.CMRPCAlarmCRCount)
	}
	if req.AlarmCR != nil {
		if st := d.validateAlarmCR(req.AlarmCR); st != nil {
			return nil, &pnio.StatusError{Op: "connect", Status: *st}
		}
	}
	if !iosar && len(req.Expected) == 0 {
		return fail(pnio.Code1ExpSubm, 1)
	}
	if !iosar && (in.SendClockFactor != out.SendClockFactor || in.RTClass() != out.RTClass()) {
		return fail(pnio.Code1IOCRBlock, 10)
	}

	// Layouts follow the expected configuration, which is what the
	// controller computed its offsets from.
	var inL, outL rt.Layout
	if !iosar {
		sh := shapes(req.Expected)
		var err error
		if inL, err = rt.LayoutFromIOCR(in, sh); err != nil {
			d.log.Warnf("device: connect from %s: input IOCR: %v", from, err)
			return fail(pnio.Code1IOCRBlock, 19)
		}
		if outL, err = rt.LayoutFromIOCR(out, sh); err != nil {
			d.log.Warnf("device: connect from %s: output IOCR: %v", from, err)
			return fail(pnio.Code1IOCRBlock, 19)
		}
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return fail(pnio.Code1CMRPC, pnio.CMRPCStateConflict)
	}
	var rerun *ar
	if old := d.ars[arb.ARUUID]; old != nil {
		rerun = old
	}
	// An IO controller holds one IOCAR to a device: a new one from the same
	// controller (initiator MAC and object) replaces the old one, which the
	// device has not yet seen end (the controller lost it first). Without
	// this the new AR would find its submodules locked by the old one
	// until the old one timed out (shared device).
	if rerun == nil && !iosar {
		for _, a := range d.ars {
			if a.typ != pnio.ARTypeIOSAR && a.ctrlMAC == arb.InitiatorMAC && a.ctrlObject == arb.InitiatorObject {
				rerun = a
				break
			}
		}
	}
	// Connects in progress hold a reservation, so that concurrent ones
	// cannot exceed MaxARs between this check and the insertion. Several
	// IO controllers may connect (shared device): each submodule belongs
	// to one of them (claimOwnership).
	// An AR that a Connect in progress replaces counts once: its
	// replacement holds a reservation already.
	count := d.reserved
	for _, a := range d.ars {
		if a != rerun && !d.replacing[a] {
			count++
		}
	}
	if count >= d.model.Capabilities.MaxARs {
		d.mu.Unlock()
		return fail(pnio.Code1CMRPC, pnio.CMRPCOutOfARs)
	}
	d.reserved++
	if !iosar {
		d.reservedIO++
	}
	if rerun != nil {
		d.replacing[rerun] = true
	}
	d.arSeq++
	seq := d.arSeq
	d.mu.Unlock()
	// The reservation ends when the AR enters d.ars (it then counts
	// itself), or when the Connect fails.
	reservedHeld := true
	unreserve := func() {
		if reservedHeld {
			reservedHeld = false
			d.reserved--
			if !iosar {
				d.reservedIO--
			}
		}
	}
	defer func() {
		d.mu.Lock()
		unreserve()
		if rerun != nil {
			delete(d.replacing, rerun)
		}
		d.mu.Unlock()
	}()
	a := &ar{
		d:            d,
		uuid:         arb.ARUUID,
		typ:          arb.Type,
		sessionKey:   arb.SessionKey,
		ctrlMAC:      arb.InitiatorMAC,
		ctrlAddr:     from,
		ctrlObject:   arb.InitiatorObject,
		ctrlName:     arb.StationName,
		ctrlPort:     d.cfg.ControllerRPCPort,
		cmiTimeout:   time.Duration(arb.ActivityTimeoutFactor) * 100 * time.Millisecond,
		expected:     req.Expected,
		inLayout:     inL,
		outLayout:    outL,
		ackWait:      map[uint16]chan pnio.AlarmAck{},
		iocs:         map[key]uint8{},
		lastActivity: time.Now(),
		seq:          seq,
		prmEndPlug:   make(chan uint16, 16),
	}
	if req.ARRPC != nil && req.ARRPC.InitiatorRPCServerPort != 0 {
		a.ctrlPort = int(req.ARRPC.InitiatorRPCServerPort)
	}
	_, a.match = d.moduleDiff(req.Expected, false, nil)
	if rerun != nil {
		// The submodules of the replaced AR go to the new one, not to
		// another controller's.
		d.setHeir(rerun, a)
		d.abort(rerun, pnio.AbortRerun, true)
	}
	if arb.Type == pnio.ARTypeIOCARSR {
		if err := d.srJoin(a, req); err != nil {
			return nil, err
		}
	}
	var locked map[key]bool
	if !iosar {
		locked = d.claimOwnership(a)
	}
	diff, _ := d.moduleDiff(req.Expected, !iosar, locked)
	// A Connect that fails from here on gives its submodules back.
	established := false
	defer func() {
		if !established && !iosar {
			d.releaseOwnership(a)
			d.srLeave(a)
		}
	}()

	resp := &pnio.ConnectResponse{
		AR: pnio.ARBlockRes{Type: arb.Type, ARUUID: arb.ARUUID, SessionKey: arb.SessionKey, ResponderMAC: d.mux.HardwareAddr(), UDPRTPort: pnio.LT},
	}
	if req.ARRPC != nil {
		resp.ARRPC = &pnio.ARRPCBlockReq{InitiatorRPCServerPort: uint16(d.cfg.RPCPort)}
	}
	resp.ModuleDiff = diff
	resp.ARServer = &pnio.ARServerBlock{StationName: d.StationName()}

	if !iosar {
		outID, err := d.allocFrameID(out)
		if err != nil {
			return fail(pnio.Code1CMRPC, pnio.CMRPCOutOfMemory)
		}
		a.cycle = time.Duration(rt.CycleNanos(in.SendClockFactor, in.ReductionRatio))
		resp.IOCRs = []pnio.IOCRBlockRes{
			{Type: in.Type, Reference: in.Reference, FrameID: in.FrameID},
			{Type: out.Type, Reference: out.Reference, FrameID: outID},
		}
		if err := d.openAlarms(a, req.AlarmCR); err != nil {
			d.releaseFrameID(outID)
			return fail(pnio.Code1CMRPC, pnio.CMRPCOutOfMemory)
		}
		resp.AlarmCR = &pnio.AlarmCRBlockRes{Type: 1, LocalAlarmReference: a.alarmRef, MaxAlarmDataLength: a.maxAlarmData}
		d.startCyclic(a, in, out, outID)
	} else if req.AlarmCR != nil {
		if err := d.openAlarms(a, req.AlarmCR); err != nil {
			return fail(pnio.Code1CMRPC, pnio.CMRPCOutOfMemory)
		}
		resp.AlarmCR = &pnio.AlarmCRBlockRes{Type: 1, LocalAlarmReference: a.alarmRef, MaxAlarmDataLength: a.maxAlarmData}
	}

	d.mu.Lock()
	d.ars[a.uuid] = a
	unreserve()
	d.mu.Unlock()
	established = true
	if iosar {
		_ = a.transition(ARStateData)
		d.watchIdle(a)
	} else {
		// The controller found the device: it stops announcing itself. The
		// responder starts after the RPC server, so it may not exist yet.
		d.mu.Lock()
		r := d.dcpR
		d.mu.Unlock()
		if r != nil {
			r.StopHello()
		}
		_ = a.transition(ARStateParameterizing)
		a.mu.Lock()
		a.cmiTimer = time.AfterFunc(a.cmiTimeout, func() {
			if s := a.currentState(); s == ARStateParameterizing || s == ARStateAppReadyPending {
				d.log.Warnf("device: AR %s: CM activity timeout in %s", a.uuid, s)
				d.abort(a, pnio.AbortCMITimeout, true)
			}
		})
		a.mu.Unlock()
	}
	d.log.Infof("device: AR %s (%s) established by %s at %s", a.uuid, a.typ, a.ctrlName, from)
	return resp, nil
}

func (d *Device) allocFrameID(out *pnio.IOCRBlockReq) (uint16, error) {
	lo, hi, _ := pnio.FrameIDRange(out.RTClass())
	d.mu.Lock()
	defer d.mu.Unlock()
	if out.FrameID != pnio.FrameIDAssign && !d.frameIDs[out.FrameID] {
		d.frameIDs[out.FrameID] = true
		return out.FrameID, nil
	}
	for id := uint32(lo); id <= uint32(hi); id++ {
		if !d.frameIDs[uint16(id)] {
			d.frameIDs[uint16(id)] = true
			return uint16(id), nil
		}
	}
	return 0, fmt.Errorf("device: %w: no free FrameID", profinet.ErrResourceExhausted)
}

func (d *Device) releaseFrameID(id uint16) {
	d.mu.Lock()
	delete(d.frameIDs, id)
	d.mu.Unlock()
}

func (d *Device) openAlarms(a *ar, req *pnio.AlarmCRBlockReq) error {
	a.alarmRef = d.alarms.AllocRef()
	a.maxAlarmData = min(req.MaxAlarmDataLength, 1432)
	a.alarmAckTimeout = time.Duration(req.RTATimeoutFactor) * 100 * time.Millisecond * time.Duration(req.RTARetries+1)
	base := alarm.EndpointConfig{
		LocalRef:  a.alarmRef,
		RemoteRef: req.LocalAlarmReference,
		RemoteMAC: a.ctrlMAC,
		Timeout:   time.Duration(req.RTATimeoutFactor) * 100 * time.Millisecond,
		Retries:   int(req.RTARetries),
		OnData:    func(b []byte) { d.alarmData(a, b) },
		OnErr: func(st pnio.Status) {
			d.log.Infof("device: AR %s aborted by the controller: %s", a.uuid, st)
			go d.abort(a, pnio.AbortReason(st.Code2), false)
		},
	}
	var err error
	hi := base
	hi.High = true
	if a.alarmHigh, err = d.alarms.Open(hi); err != nil {
		return err
	}
	if a.alarmLow, err = d.alarms.Open(base); err != nil {
		a.alarmHigh.Close()
		return err
	}
	return nil
}

// startCyclic creates the provider of the input CR and the consumer of
// the output CR and schedules them.
func (d *Device) startCyclic(a *ar, in, out *pnio.IOCRBlockReq, outID uint16) {
	step := in.SendClockFactor * in.ReductionRatio
	a.provider = rt.NewProvider(rt.ProviderConfig{
		FrameID:     in.FrameID,
		DataLength:  in.DataLength,
		Dst:         a.ctrlMAC,
		Src:         d.mux.HardwareAddr(),
		VLAN:        in.TagHeader & 0x0FFF,
		CounterStep: step,
		Send:        d.mux.Send,
		Fill:        func(c []byte) { d.fillInputs(a, c) },
	})
	// Until the application is ready the provider reports STOP.
	a.provider.SetDataStatus(rt.StatePrimary | rt.DataValid | rt.StationOK)
	outCycle := time.Duration(rt.CycleNanos(out.SendClockFactor, out.ReductionRatio))
	a.consumer = rt.NewConsumer(rt.ConsumerConfig{
		FrameID:      outID,
		DataLength:   out.DataLength,
		Src:          a.ctrlMAC,
		Watchdog:     time.Duration(out.DataHoldFactor) * outCycle,
		StartupGrace: a.cmiTimeout,
	})
	a.consumer.OnUpdate(func(c []byte, s rt.DataStatus) { d.storeOutputs(a, c, s) })
	d.disp.Add(a.consumer)
	ctx := d.ctx
	a.provTask = d.sched.Add(a.cycle, func(time.Time) { a.provider.Tick(ctx) })
	a.wdTask = d.sched.Add(outCycle, func(now time.Time) {
		if a.consumer.Check(now) {
			d.log.Warnf("device: AR %s: output data hold time expired", a.uuid)
			go d.abort(a, pnio.AbortConsumerDHT, true)
		}
	})
}

// fillInputs builds the input C_SDU, on the scheduler goroutine.
func (d *Device) fillInputs(a *ar, c []byte) {
	d.imgMu.RLock()
	defer d.imgMu.RUnlock()
	for _, o := range a.inLayout.Objects {
		k := key{o.Slot, o.Subslot}
		in := d.inputs[k]
		dst := c[o.Offset : o.Offset+o.Length]
		if in == nil || !owns(d.owner[k], a) || len(in.data) != int(o.Length) {
			clear(dst)
			c[o.IOPSOffset()] = pnio.IOxSBad
			continue
		}
		copy(dst, in.data)
		c[o.IOPSOffset()] = in.iops
	}
	for _, s := range a.inLayout.IOCS {
		if owns(d.owner[key{s.Slot, s.Subslot}], a) {
			c[s.Offset] = pnio.IOxSGood
		} else {
			c[s.Offset] = pnio.IOxSBad
		}
	}
}

// storeOutputs copies the output C_SDU into the output images, on the
// dispatcher goroutine.
func (d *Device) storeOutputs(a *ar, c []byte, s rt.DataStatus) {
	if !s.Valid() {
		return
	}
	// Of an AR set, the primary's outputs alone are applied.
	if a.sr != nil && !d.srOutput(a, s) {
		return
	}
	d.imgMu.Lock()
	for _, o := range a.outLayout.Objects {
		k := key{o.Slot, o.Subslot}
		out := d.outputs[k]
		if out == nil || !owns(d.owner[k], a) || len(out.data) != int(o.Length) {
			continue
		}
		copy(out.data, c[o.Offset:o.Offset+o.Length])
		out.iops = c[o.IOPSOffset()]
		if !s.Running() {
			out.iops = pnio.IOxSBad
		}
	}
	d.imgMu.Unlock()
	a.mu.Lock()
	for _, st := range a.outLayout.IOCS {
		a.iocs[key{st.Slot, st.Subslot}] = c[st.Offset]
	}
	a.mu.Unlock()
}

// watchIdle releases an IOSAR without traffic for its activity timeout.
func (d *Device) watchIdle(a *ar) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		t := time.NewTicker(max(a.cmiTimeout/4, 50*time.Millisecond))
		defer t.Stop()
		for {
			select {
			case <-t.C:
				a.mu.Lock()
				idle := time.Since(a.lastActivity)
				closed := a.state == ARStateClosed
				a.mu.Unlock()
				if closed {
					return
				}
				if idle > a.cmiTimeout {
					d.log.Infof("device: IOSAR %s idle for %s, released", a.uuid, idle.Round(time.Millisecond))
					d.abort(a, pnio.AbortCMITimeout, false)
					return
				}
			case <-d.ctx.Done():
				return
			}
		}
	}()
}

// controllerName is the station name of the AR's controller; empty for
// no AR (an implicit read).
func (a *ar) controllerName() string {
	if a == nil {
		return ""
	}
	return a.ctrlName
}

// abort closes an AR. When notify is true the controller is told with an
// ERR PDU; a release passes reason 0 and notify false.
func (d *Device) abort(a *ar, reason pnio.AbortReason, notify bool) {
	a.mu.Lock()
	if a.state == ARStateClosed {
		a.mu.Unlock()
		return
	}
	a.reason = reason
	if a.cmiTimer != nil {
		a.cmiTimer.Stop()
	}
	for _, ch := range a.ackWait {
		close(ch)
	}
	a.ackWait = map[uint16]chan pnio.AlarmAck{}
	a.mu.Unlock()
	if reason != 0 {
		d.met.OnAbort(a.ctrlName, fmt.Errorf("device: AR %s: %w: %s", a.uuid, profinet.ErrAborted, reason))
	}
	if notify && a.alarmLow != nil && reason != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		if err := a.alarmLow.SendErr(ctx, pnio.AbortStatus(reason)); err != nil && !errors.Is(err, profinet.ErrClosed) {
			d.log.Debugf("device: AR %s: send abort: %v", a.uuid, err)
		}
		cancel()
	}
	if a.provTask != nil {
		d.sched.Remove(a.provTask)
	}
	if a.wdTask != nil {
		d.sched.Remove(a.wdTask)
	}
	if a.consumer != nil {
		d.disp.Remove(a.consumer)
		d.releaseFrameID(a.consumer.FrameID())
	}
	if a.alarmHigh != nil {
		a.alarmHigh.Close()
	}
	if a.alarmLow != nil {
		a.alarmLow.Close()
	}
	_ = a.transition(ARStateClosed)
	d.mu.Lock()
	if d.ars[a.uuid] == a {
		delete(d.ars, a.uuid)
	}
	d.mu.Unlock()
	// The outputs of a closed IOCAR are no longer valid; its submodules
	// go to the next AR that expects them.
	if a.typ != pnio.ARTypeIOSAR {
		d.releaseOwnership(a)
		d.srLeave(a)
	}
	if reason == 0 {
		d.log.Infof("device: AR %s released", a.uuid)
	} else {
		d.log.Infof("device: AR %s aborted: %s", a.uuid, reason)
	}
}

// prmEnd handles the end of parameterization: the response is sent by the
// caller; the application is asked and ApplicationReady is sent here.
func (d *Device) prmEnd(a *ar) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ctx, cancel := context.WithTimeout(d.ctx, a.cmiTimeout)
		defer cancel()
		if err := d.handler.PrmEnd(ctx, a.info()); err != nil {
			d.log.Warnf("device: AR %s: application not ready: %v", a.uuid, err)
			d.abort(a, pnio.AbortUser, true)
			return
		}
		if a.currentState() != ARStateAppReadyPending {
			return
		}
		// Submodules that match are now owned; the provider runs.
		a.provider.SetDataStatus(rt.DefaultDataStatus)
		if a.sr != nil {
			// Primary or backup acknowledge (once in Data).
			defer d.srStatus(a.sr.set)
		}
		if err := d.applicationReady(ctx, a); err != nil {
			if a.currentState() == ARStateClosed {
				return
			}
			d.log.Warnf("device: AR %s: ApplicationReady: %v", a.uuid, err)
			d.abort(a, pnio.AbortRPCClientCall, true)
			return
		}
		a.mu.Lock()
		if a.cmiTimer != nil {
			a.cmiTimer.Stop()
		}
		a.mu.Unlock()
		if err := a.transition(ARStateData); err != nil {
			d.log.Debugf("device: %v", err)
			return
		}
		a.consumer.Arm(time.Now())
	}()
}

// applicationReady calls the controller's Control operation with the
// IOXBlockReq ApplicationReady.
func (d *Device) applicationReady(ctx context.Context, a *ar) error {
	return d.callApplicationReady(ctx, a, pnio.ControlBlock{Type: pnio.BlockAppReadyReq, ARUUID: a.uuid, SessionKey: a.sessionKey, Command: pnio.CmdApplicationReady})
}

// callApplicationReady sends an ApplicationReady (or its plug variant) to
// the controller and checks its answer.
func (d *Device) callApplicationReady(ctx context.Context, a *ar, blk pnio.ControlBlock) error {
	ua, ok := a.ctrlAddr.(*net.UDPAddr)
	if !ok {
		return fmt.Errorf("device: %w: controller address %v", profinet.ErrInvalidState, a.ctrlAddr)
	}
	dst := &net.UDPAddr{IP: ua.IP, Port: a.ctrlPort}
	body := pnio.AppendNDRRequest(nil, rpcOrder, pnio.DefaultArgsMaximum, blk.Append(nil))
	cctx, cancel := context.WithTimeout(ctx, d.cfg.ApplicationReadyTimeout)
	defer cancel()
	resp, err := d.rpcCli.Do(cctx, dst, rpc.Call{Object: a.ctrlObject, Iface: pnio.ControllerInterface, IfaceVersion: pnio.InterfaceVersion, OpNum: uint16(pnio.OpControl), Idempotent: true, Body: body})
	if err != nil {
		return err
	}
	st, blocks, err := pnio.DecodeNDRResponse(resp.Body, resp.Header.Order())
	if err != nil {
		return err
	}
	if !st.OK() {
		return &pnio.StatusError{Op: "ApplicationReady", Status: st}
	}
	blk2, _, err := pnio.ReadBlock(blocks)
	if err != nil {
		return err
	}
	c, err := pnio.DecodeControlBlock(blk2)
	if err != nil {
		return err
	}
	if c.Type != blk.Type|0x8000 || c.Command&pnio.CmdDone == 0 || c.ARUUID != a.uuid {
		return fmt.Errorf("device: %w: ApplicationReady answered with %s command 0x%04X", profinet.ErrMalformed, c.Type, c.Command)
	}
	return nil
}
