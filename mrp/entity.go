// SPDX-License-Identifier: MIT

package mrp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
)

// The MRP entity: a ring client (MRC) or a ring manager (MRM) on the two
// ring ports of a switch (package l2/bridge).
//
// The state machines and the default timers follow what two open
// implementations do (microchip-ung/mrp, the Linux bridge's MRP) and the
// MRP frame and PROFINET record layouts that Wireshark dissects
// (docs/research/EVIDENCE-MRP-PTCP-MGMT.md, A.2). They are not checked
// against IEC 62439-2, whose text is not public: the entity is meant for
// rings of software devices and tests, and no recovery time is claimed
// for it: tests measure it.
//
// The manager sends MRP_Test frames on both ring ports. While they come
// back the ring is closed, and the manager blocks its secondary port; when
// TSTNRmax of them are missed, or a client reports a link down, the ring
// is open and the manager forwards on both ports. Each change is announced
// with MRP_TopologyChange, on which every client flushes its forwarding
// database. A client forwards the MRP frames of others between its ring
// ports, reports its port links with MRP_LinkDown/LinkUp, and keeps a port
// that comes up blocked until the manager's TopologyChange (or its link
// timers run out), so that a closing ring never loops.

// Role is the MRP role of an entity.
type Role int

// Roles; the values are those of the PROFINET MRP_Role field.
const (
	RoleDisabled Role = 0
	RoleClient   Role = 1
	RoleManager  Role = 2
)

func (r Role) String() string {
	switch r {
	case RoleClient:
		return "client"
	case RoleManager:
		return "manager"
	default:
		return "disabled"
	}
}

// Profile is a set of timers: the recovery time class it is named after.
type Profile struct {
	Name string
	// TopologyChange: TOPchgT, TOPNRmax.
	TopChangeInterval time.Duration
	TopChangeRepeat   int
	// Test frames: TSTdefaultT, TSTNRmax.
	TestInterval time.Duration
	TestMax      int
	// Link change frames: LNKdownT = LNKupT, LNKNRmax.
	LinkInterval time.Duration
	LinkRepeat   int
}

// The profiles of microchip-ung/mrp and umrp (UNVERIFIED against
// IEC 62439-2). The 30 ms and 10 ms profiles need timers a general-purpose
// OS does not keep; they are not offered.
var (
	Profile500 = Profile{Name: "500ms", TopChangeInterval: 20 * time.Millisecond, TopChangeRepeat: 3,
		TestInterval: 50 * time.Millisecond, TestMax: 5, LinkInterval: 100 * time.Millisecond, LinkRepeat: 4}
	Profile200 = Profile{Name: "200ms", TopChangeInterval: 10 * time.Millisecond, TopChangeRepeat: 3,
		TestInterval: 20 * time.Millisecond, TestMax: 3, LinkInterval: 20 * time.Millisecond, LinkRepeat: 4}
)

// Switch is what the entity needs of the switch its ring ports belong to;
// *bridge.Bridge implements it.
type Switch interface {
	// SetState makes a port (0-based) forward (blocked false) or carry
	// only its local frames.
	SetBlocked(port int, blocked bool)
	// Flush forgets the learned stations.
	Flush()
}

// Config configures an entity.
type Config struct {
	Role Role
	// Ports are the muxes of the two ring ports' local ports (frames to
	// the MRP groups arrive there; frames sent there leave on that port),
	// the primary first; Index their port numbers in Switch.
	Ports [2]*l2.Mux
	Index [2]int
	// Switch blocks and flushes; required.
	Switch Switch
	// MAC is the MRP_SA, the switch's (interface) MAC address; required.
	MAC ethernet.MAC
	// Domain is the MRP domain UUID; zero means DefaultDomain.
	Domain [16]byte
	// Prio is the manager priority; zero means DefaultManagerPrio.
	Prio uint16
	// Profile is the timer profile; zero means Profile200.
	Profile Profile
	// OnChange is called after the ring state or a port state changed,
	// never with the entity's lock held; it must not block.
	OnChange func()
	Logger   profinet.Logger
}

// Status is the state of an entity.
type Status struct {
	Role  Role
	State string
	// RingOpen is the ring state the manager determined (a client: as
	// the manager last reported it).
	RingOpen bool
	// Blocked reports the ring ports the entity blocks.
	Blocked [2]bool
	// Transitions counts ring openings (manager).
	Transitions uint16
	// Counters.
	TestsSent, TestsReceived, TopologyChanges, LinkChanges, Forwarded, Malformed uint64
}

// Entity is a running MRP client or manager.
type Entity struct {
	cfg  Config
	log  profinet.Logger
	subs [2]*l2.Subscription

	mu          sync.Mutex
	state       string
	up          [2]bool
	blocked     [2]bool
	ringOpen    bool
	transitions uint16
	misses      int
	seq         uint16
	start       time.Time
	// linkTimer is the client's wait for a TopologyChange after a port
	// came up; it unblocks the port when it runs out.
	linkTimer *time.Timer
	// linkGen numbers a client's link changes: a burst of link change
	// frames stops when a newer change starts, so that a LinkDown is
	// never repeated after the link came back.
	linkGen uint64
	// testNow asks the manager's loop for a test round now.
	testNow chan struct{}

	testsSent, testsRecv, topChanges, linkChanges, forwarded, malformed atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Entity states (microchip-ung/mrp, utils.h).
const (
	StateACStat1 = "AC_STAT1" // no ring port up
	StatePrmUp   = "PRM_UP"   // manager: the primary port alone is up
	StateChkRO   = "CHK_RO"   // manager: ring open
	StateChkRC   = "CHK_RC"   // manager: ring closed
	StateDEIdle  = "DE_IDLE"  // client: one port up
	StatePT      = "PT"       // client: the second port came up, blocked
	StatePTIdle  = "PT_IDLE"  // client: both ports forwarding
	StateDE      = "DE"       // client: a port went down
)

// New starts an entity.
func New(cfg Config) (*Entity, error) {
	if cfg.Role != RoleClient && cfg.Role != RoleManager {
		return nil, fmt.Errorf("mrp: role %s: %w", cfg.Role, profinet.ErrInvalidConfig)
	}
	if cfg.Ports[0] == nil || cfg.Ports[1] == nil || cfg.Switch == nil || cfg.MAC.IsZero() || cfg.Index[0] == cfg.Index[1] {
		return nil, fmt.Errorf("mrp: two ring ports, a switch and a MAC address are required: %w", profinet.ErrInvalidConfig)
	}
	if cfg.Domain == ([16]byte{}) {
		cfg.Domain = DefaultDomain
	}
	if cfg.Prio == 0 {
		cfg.Prio = DefaultManagerPrio
	}
	if cfg.Profile.TestInterval == 0 {
		cfg.Profile = Profile200
	}
	if cfg.Logger == nil {
		cfg.Logger = profinet.NopLogger()
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Entity{cfg: cfg, log: cfg.Logger, state: StateACStat1, ringOpen: true, start: time.Now(), ctx: ctx, cancel: cancel, testNow: make(chan struct{}, 1)}
	for i, m := range cfg.Ports {
		s, err := m.Subscribe(l2.MatchEtherType(EtherType), 256)
		if err != nil {
			cancel()
			for _, o := range e.subs {
				if o != nil {
					o.Close()
				}
			}
			return nil, err
		}
		e.subs[i] = s
	}
	// Until the state machine decides, a manager blocks its secondary
	// port and a client the port that is not up yet: no loop at start.
	e.setBlocked(0, false)
	e.setBlocked(1, cfg.Role == RoleManager)
	for i := range e.subs {
		e.wg.Add(1)
		go e.recv(i)
	}
	e.wg.Add(1)
	go e.run()
	return e, nil
}

// Close stops the entity. The ring ports keep their last state.
func (e *Entity) Close() error {
	e.cancel()
	for _, s := range e.subs {
		s.Close()
	}
	e.wg.Wait()
	e.mu.Lock()
	if e.linkTimer != nil {
		e.linkTimer.Stop()
	}
	e.mu.Unlock()
	return nil
}

// Status returns the entity's state.
func (e *Entity) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Status{Role: e.cfg.Role, State: e.state, RingOpen: e.ringOpen, Blocked: e.blocked, Transitions: e.transitions,
		TestsSent: e.testsSent.Load(), TestsReceived: e.testsRecv.Load(), TopologyChanges: e.topChanges.Load(),
		LinkChanges: e.linkChanges.Load(), Forwarded: e.forwarded.Load(), Malformed: e.malformed.Load()}
}

func (e *Entity) changed() {
	if e.cfg.OnChange != nil {
		e.cfg.OnChange()
	}
}

// setBlocked sets a ring port's state in the switch; mu held or not yet
// shared.
func (e *Entity) setBlocked(i int, b bool) {
	e.blocked[i] = b
	e.cfg.Switch.SetBlocked(e.cfg.Index[i], b)
}

func (e *Entity) linkUp(i int) bool {
	if ls, ok := e.cfg.Ports[i].Port().(l2.LinkStater); ok {
		return ls.LinkUp()
	}
	return true
}

// run follows the links and, for a manager, sends the test frames.
func (e *Entity) run() {
	defer e.wg.Done()
	poll := min(e.cfg.Profile.TestInterval, 20*time.Millisecond)
	t := time.NewTicker(poll)
	defer t.Stop()
	nextTest := time.Now()
	for {
		now := false
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
		case <-e.testNow:
			now = true
		}
		for i := range e.up {
			if now := e.linkUp(i); now != e.upLocked(i) {
				e.link(i, now)
			}
		}
		if e.cfg.Role == RoleManager && (now || !time.Now().Before(nextTest)) {
			nextTest = time.Now().Add(e.cfg.Profile.TestInterval)
			e.testTick()
		}
	}
}

func (e *Entity) upLocked(i int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.up[i]
}

// link handles a ring port's link change.
func (e *Entity) link(i int, up bool) {
	e.mu.Lock()
	e.up[i] = up
	other := 1 - i
	var notify []func()
	if e.cfg.Role == RoleManager {
		notify = e.managerLink()
	} else {
		notify = e.clientLink(i, up, other)
	}
	e.mu.Unlock()
	e.log.Debugf("mrp: %s: port %d link %v", e.cfg.Role, i+1, up)
	for _, f := range notify {
		f()
	}
	e.changed()
}

// managerLink: mu held.
func (e *Entity) managerLink() []func() {
	switch {
	case e.up[0] && e.up[1]:
		// Both up: the ring may be closed; the secondary port stays
		// blocked while the tests decide (testTick, managerRecv).
		if e.state != StateChkRC {
			e.state = StateChkRO
			e.misses = 0
			e.setBlocked(1, true)
		}
	case e.up[0] || e.up[1]:
		// One port: it is the primary; nothing can loop.
		if !e.up[0] {
			e.swap()
		}
		e.state = StatePrmUp
		e.setBlocked(0, false)
		e.setBlocked(1, false)
		if !e.ringOpen {
			e.ringOpen = true
			e.transitions++
			return []func(){func() { e.sendTopologyChange(0) }}
		}
	default:
		e.state = StateACStat1
	}
	return nil
}

// clientLink: mu held.
func (e *Entity) clientLink(i int, up bool, other int) []func() {
	e.linkGen++
	if !up {
		e.setBlocked(i, true)
		if e.up[other] {
			e.setBlocked(other, false)
			e.state = StateDE
			if e.linkTimer != nil {
				e.linkTimer.Stop()
			}
			return []func(){func() { e.sendLinkChange(other, false, i) }}
		}
		e.state = StateACStat1
		return nil
	}
	if !e.up[other] {
		// The first port up: it forwards.
		e.setBlocked(i, false)
		e.state = StateDEIdle
		return nil
	}
	// The second port up: blocked until the manager's TopologyChange,
	// or until the link change frames have all gone out unanswered.
	e.setBlocked(i, true)
	e.state = StatePT
	wait := e.cfg.Profile.LinkInterval * time.Duration(e.cfg.Profile.LinkRepeat)
	if e.linkTimer != nil {
		e.linkTimer.Stop()
	}
	e.linkTimer = time.AfterFunc(wait, func() { e.unblockAfterWait(i) })
	return []func(){func() { e.sendLinkChange(other, true, i) }}
}

func (e *Entity) unblockAfterWait(i int) {
	e.mu.Lock()
	if e.state != StatePT || !e.up[i] {
		e.mu.Unlock()
		return
	}
	e.setBlocked(i, false)
	e.state = StatePTIdle
	e.mu.Unlock()
	e.log.Debugf("mrp: client: port %d forwarding without a TopologyChange", i+1)
	e.changed()
}

// swap makes the secondary port the primary: mu held.
func (e *Entity) swap() {
	e.cfg.Ports[0], e.cfg.Ports[1] = e.cfg.Ports[1], e.cfg.Ports[0]
	e.cfg.Index[0], e.cfg.Index[1] = e.cfg.Index[1], e.cfg.Index[0]
	e.subs[0], e.subs[1] = e.subs[1], e.subs[0]
	e.up[0], e.up[1] = e.up[1], e.up[0]
	e.blocked[0], e.blocked[1] = e.blocked[1], e.blocked[0]
}

// testTick sends the manager's test frames and counts the misses.
func (e *Entity) testTick() {
	e.mu.Lock()
	var notify []func()
	if e.up[0] && e.up[1] {
		e.misses++
		if e.misses > e.cfg.Profile.TestMax && (!e.ringOpen || e.blocked[1]) {
			// The tests do not come (back): the ring is open, and the
			// secondary port forwards.
			if !e.ringOpen {
				e.transitions++
			}
			e.ringOpen = true
			e.state = StateChkRO
			e.setBlocked(1, false)
			notify = append(notify, func() { e.sendTopologyChange(0) })
			e.log.Infof("mrp: manager: ring open (%d tests missed)", e.misses-1)
		}
	}
	up := e.up
	state := e.ringState()
	transitions := e.transitions
	e.mu.Unlock()
	for _, f := range notify {
		f()
	}
	if len(notify) > 0 {
		e.cfg.Switch.Flush()
		e.changed()
	}
	for i := range up {
		if up[i] {
			e.send(i, MACTest, Frame{Test: &Test{Prio: e.cfg.Prio, SA: e.cfg.MAC, PortRole: PortRole(i),
				RingState: state, Transition: transitions, TimeStamp: e.timestamp()}})
			e.testsSent.Add(1)
		}
	}
}

func (e *Entity) ringState() RingState {
	if e.ringOpen {
		return RingOpen
	}
	return RingClosed
}

func (e *Entity) timestamp() uint32 { return uint32(time.Since(e.start).Milliseconds()) }

// recv handles the MRP frames of a ring port.
func (e *Entity) recv(i int) {
	defer e.wg.Done()
	sub := e.subs[i]
	for {
		ef, err := sub.Recv(e.ctx)
		if err != nil {
			return
		}
		f, err := Decode(ef.Payload)
		if err != nil {
			e.malformed.Add(1)
			continue
		}
		if f.Common != nil && f.Common.Domain != e.cfg.Domain {
			continue
		}
		port := e.portOf(sub)
		if port < 0 {
			continue
		}
		if e.cfg.Role == RoleManager {
			e.managerRecv(port, f)
		} else {
			e.clientRecv(port, ef, f)
		}
	}
}

// portOf returns the current index of a subscription (the manager swaps
// its ports).
func (e *Entity) portOf(s *l2.Subscription) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, o := range e.subs {
		if o == s {
			return i
		}
	}
	return -1
}

func (e *Entity) managerRecv(port int, f Frame) {
	switch {
	case f.Test != nil:
		if f.Test.SA != e.cfg.MAC {
			// Another manager's test: one manager per ring here.
			return
		}
		e.testsRecv.Add(1)
		e.mu.Lock()
		if f.Test.Transition != e.transitions {
			// Sent before the ring last opened: still on its way
			// through a client's queues, it proves nothing about the
			// ring now.
			e.mu.Unlock()
			return
		}
		e.misses = 0
		var closed bool
		if e.up[0] && e.up[1] && (e.ringOpen || !e.blocked[1]) {
			// Our test came around: the ring is closed.
			e.ringOpen = false
			e.state = StateChkRC
			e.setBlocked(1, true)
			closed = true
		}
		e.mu.Unlock()
		if closed {
			e.log.Infof("mrp: manager: ring closed")
			e.cfg.Switch.Flush()
			e.sendTopologyChange(e.cfg.Profile.TopChangeInterval)
			e.changed()
		}
	case f.Link != nil:
		e.linkChanges.Add(1)
		if f.Link.Up {
			// A client's port came up: the ring may close now. Test it
			// at once rather than at the next interval; a test that
			// comes around closes it (a LinkUp alone may be a late one
			// of a ring that is open again).
			select {
			case e.testNow <- struct{}{}:
			default:
			}
			return
		}
		e.mu.Lock()
		var opened bool
		if !e.ringOpen {
			e.ringOpen = true
			e.transitions++
			e.state = StateChkRO
			e.setBlocked(1, false)
			opened = true
		}
		e.mu.Unlock()
		if opened {
			e.log.Infof("mrp: manager: ring open (link down at %s)", f.Link.SA)
			e.cfg.Switch.Flush()
			e.sendTopologyChange(0)
			e.changed()
		}
	}
	_ = port
}

// sa returns the MRP_SA of a frame, the station that sent it.
func (f *Frame) sa() (ethernet.MAC, bool) {
	switch {
	case f.Test != nil:
		return f.Test.SA, true
	case f.TopologyChange != nil:
		return f.TopologyChange.SA, true
	case f.Link != nil:
		return f.Link.SA, true
	}
	return ethernet.MAC{}, false
}

func (e *Entity) clientRecv(port int, ef ethernet.Frame, f Frame) {
	other := 1 - port
	// Forward the frames of others to the other ring port: a client is
	// a transit station of the ring. Its own frames that come back stop
	// here, so that nothing circles a ring without a manager.
	if sa, ok := f.sa(); ok && sa == e.cfg.MAC {
		return
	}
	if e.upLocked(other) {
		raw := append(append(make([]byte, 0, ethernet.HeaderLen+len(ef.Payload)), ef.Dst[:]...), ef.Src[:]...)
		raw = binary.BigEndian.AppendUint16(raw, uint16(EtherType))
		raw = append(raw, ef.Payload...)
		if err := e.cfg.Ports[other].Send(e.ctx, raw); err == nil {
			e.forwarded.Add(1)
		}
	}
	switch {
	case f.Test != nil:
		e.testsRecv.Add(1)
		e.mu.Lock()
		e.ringOpen = f.Test.RingState == RingOpen
		// The manager reports the ring closed: its secondary port is
		// blocked, so a port waiting for it may forward.
		unblocked := e.state == StatePT && !e.ringOpen
		if unblocked {
			e.unblockPTLocked()
		}
		e.mu.Unlock()
		if unblocked {
			e.changed()
		}
	case f.TopologyChange != nil:
		e.topChanges.Add(1)
		interval := time.Duration(f.TopologyChange.Interval) * time.Millisecond
		e.mu.Lock()
		// A port waiting for the manager unblocks on a TopologyChange of
		// a closed ring (this manager sends those with an interval; an
		// opening one, interval 0, may still be on its way from before
		// the port came up) or once the manager's tests say closed.
		closedTC := f.TopologyChange.Interval > 0 || !e.ringOpen
		switch {
		case e.state == StatePT && !closedTC:
		case e.state == StatePT:
			e.unblockPTLocked()
		case e.state == StateDE:
			e.state = StateDEIdle
		}
		e.mu.Unlock()
		if interval == 0 {
			e.cfg.Switch.Flush()
		} else {
			time.AfterFunc(interval, e.cfg.Switch.Flush)
		}
		e.changed()
	}
}

// unblockPTLocked ends a client's wait for the manager: its ports that
// are up forward (mu held).
func (e *Entity) unblockPTLocked() {
	for i := range e.blocked {
		if e.up[i] {
			e.setBlocked(i, false)
		}
	}
	e.state = StatePTIdle
	if e.linkTimer != nil {
		e.linkTimer.Stop()
	}
}

// sendTopologyChange sends MRP_TopologyChange on both ring ports,
// TOPNRmax times.
func (e *Entity) sendTopologyChange(interval time.Duration) {
	e.topChanges.Add(1)
	f := Frame{TopologyChange: &TopologyChange{Prio: e.cfg.Prio, SA: e.cfg.MAC, Interval: uint16(interval / time.Millisecond)}}
	for n := 0; n < e.cfg.Profile.TopChangeRepeat; n++ {
		for i := range e.cfg.Ports {
			if e.upLocked(i) {
				e.send(i, MACControl, f)
			}
		}
		if n+1 < e.cfg.Profile.TopChangeRepeat {
			select {
			case <-e.ctx.Done():
				return
			case <-time.After(e.cfg.Profile.TopChangeInterval):
			}
		}
	}
}

// sendLinkChange sends a client's MRP_LinkDown or LinkUp on ring port
// via about port about, LNKNRmax times, without blocking the caller.
func (e *Entity) sendLinkChange(via int, up bool, about int) {
	e.linkChanges.Add(1)
	f := Frame{Link: &LinkChange{Up: up, SA: e.cfg.MAC, PortRole: PortRole(about),
		Interval: uint16(e.cfg.Profile.LinkInterval / time.Millisecond), Blocked: 1}}
	e.mu.Lock()
	gen := e.linkGen
	e.mu.Unlock()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for n := 0; n < e.cfg.Profile.LinkRepeat; n++ {
			// Stop when the link changed again: a newer change started,
			// or the port's link (read now, not as last polled) is no
			// longer what this burst reports. A LinkDown that arrives
			// after the link came back would open a closed ring.
			e.mu.Lock()
			stale := e.linkGen != gen
			e.mu.Unlock()
			if stale || e.linkUp(about) != up {
				return
			}
			e.send(via, MACControl, f)
			select {
			case <-e.ctx.Done():
				return
			case <-time.After(e.cfg.Profile.LinkInterval):
			}
		}
	}()
}

// send sends an MRP frame on a ring port with the next sequence number.
func (e *Entity) send(i int, dst ethernet.MAC, f Frame) {
	e.mu.Lock()
	e.seq++
	f.Common = &Common{SequenceID: e.seq, Domain: e.cfg.Domain}
	m := e.cfg.Ports[i]
	e.mu.Unlock()
	h := ethernet.Frame{Dst: dst, Src: m.HardwareAddr(), EtherType: EtherType}
	b := h.AppendHeader(make([]byte, 0, 96))
	b, err := f.AppendTo(b)
	if err != nil {
		e.log.Debugf("mrp: encode: %v", err)
		return
	}
	if err := m.Send(e.ctx, ethernet.Pad(b, 0)); err != nil && !errors.Is(err, context.Canceled) && e.ctx.Err() == nil {
		e.log.Debugf("mrp: port %d: %v", i+1, err)
	}
}
