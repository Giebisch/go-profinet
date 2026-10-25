// SPDX-License-Identifier: MIT

// Package vnet is an in-memory Ethernet network: a switch with any number
// of endpoints, each an [l2.Port] with its own MAC address, that carries
// serialized Ethernet frames between them. It needs no privileges and no
// network interface, which makes it the transport of the unit, end-to-end,
// chaos and stress tests of this module.
//
// Delivery follows a learning-free switch: a unicast frame goes to the
// endpoint that owns its destination address (and to promiscuous
// endpoints), a broadcast or multicast frame to every other endpoint that
// accepts the group, and a frame with an 802.1Q tag only to endpoints that
// are members of its VLAN. Frames are copied on every hop, so no endpoint
// sees another's buffers.
//
// Faults are injected on the egress of an endpoint: loss, duplication,
// corruption, reordering, delay and jitter, decided by a pseudo-random
// generator seeded from the network seed and the endpoint's MAC address.
// The same seed and the same frames from an endpoint give the same faults,
// independent of what other endpoints send.
//
// The package is protocol independent: nothing in it knows PROFINET.
package vnet

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// DefaultQueueLen is the receive queue length of an endpoint whose
// configuration leaves it zero.
const DefaultQueueLen = 512

// Faults are the impairments applied to the frames an endpoint sends.
// Probabilities are in [0, 1]. The zero value is a perfect link.
type Faults struct {
	// Loss is the probability that a frame is dropped.
	Loss float64
	// Duplicate is the probability that a frame is delivered twice.
	Duplicate float64
	// Corrupt is the probability that one bit of the frame's payload
	// (beyond the 14-octet header) is inverted. Real NICs drop such
	// frames by their FCS; this models a corruption the FCS missed and
	// exercises the decoders.
	Corrupt float64
	// Reorder is the probability that a frame is held back and delivered
	// after the next frame of the same endpoint (or after ReorderHold
	// when none follows).
	Reorder float64
	// ReorderHold bounds how long a reordered frame waits for a
	// successor. Zero means 5 ms.
	ReorderHold time.Duration
	// Delay is added to the delivery of every frame.
	Delay time.Duration
	// Jitter is the bound of a uniformly distributed extra delay. Frames
	// of one endpoint keep their order under jitter; only Reorder
	// reorders.
	Jitter time.Duration
	// Match restricts the faults to the frames it returns true for. nil
	// applies them to every frame.
	Match func(frame []byte) bool
}

func (f *Faults) active() bool {
	return f.Loss > 0 || f.Duplicate > 0 || f.Corrupt > 0 || f.Reorder > 0 || f.Delay > 0 || f.Jitter > 0
}

// Tap is called with every frame an endpoint sends, before faults are
// applied, on the sender's goroutine. It must not retain or modify frame.
type Tap func(from *Endpoint, frame []byte)

// Network is a virtual Ethernet segment. It is safe for concurrent use.
type Network struct {
	seed uint64

	mu        sync.RWMutex
	endpoints []*Endpoint
	byMAC     map[ethernet.MAC]*Endpoint
	taps      []Tap
	closed    bool
}

// New returns an empty network whose fault decisions derive from seed.
func New(seed uint64) *Network {
	return &Network{seed: seed, byMAC: make(map[ethernet.MAC]*Endpoint)}
}

// Seed returns the seed of the network, to be logged by a failing test so
// that it can be reproduced.
func (n *Network) Seed() uint64 { return n.seed }

// AddTap registers a tap that observes every frame sent on the network.
func (n *Network) AddTap(t Tap) {
	n.mu.Lock()
	n.taps = append(n.taps, t)
	n.mu.Unlock()
}

// EndpointConfig configures an endpoint.
type EndpointConfig struct {
	// Name identifies the endpoint in errors and statistics.
	Name string
	// MAC is the endpoint's address; it must be a unicast address unique
	// on the network.
	MAC ethernet.MAC
	// MTU is the largest payload the endpoint sends. Zero means 1500.
	MTU int
	// QueueLen is the length of the receive queue; frames that arrive
	// while it is full are dropped. Zero means DefaultQueueLen.
	QueueLen int
	// VLANs lists the VLAN identifiers the endpoint is a member of. A
	// tagged frame with a non-zero VLAN ID reaches the endpoint only when
	// the ID is listed. nil makes the endpoint a member of every VLAN.
	// Untagged and priority-tagged frames always reach it.
	VLANs []uint16
	// FilterMulticast makes the endpoint receive only the multicast
	// groups it joined (and broadcast); otherwise it receives every
	// multicast frame.
	FilterMulticast bool
	// Promiscuous makes the endpoint receive unicast frames addressed to
	// other endpoints too.
	Promiscuous bool
}

// EndpointStats are the counters of an endpoint.
type EndpointStats struct {
	Sent, Received uint64
	// Dropped counts frames lost by fault injection on the endpoint's
	// egress.
	Dropped uint64
	// Duplicated, Corrupted and Reordered count injected faults.
	Duplicated, Corrupted, Reordered uint64
	// Overflow counts frames dropped because the receive queue was full.
	Overflow uint64
	// LinkDrops counts frames dropped because the link was down.
	LinkDrops uint64
}

// NewEndpoint attaches a new endpoint to the network.
func (n *Network) NewEndpoint(cfg EndpointConfig) (*Endpoint, error) {
	if cfg.MAC.IsMulticast() || cfg.MAC.IsZero() {
		return nil, fmt.Errorf("vnet: endpoint %q: %w: MAC %s is not a unicast address", cfg.Name, profinet.ErrInvalidConfig, cfg.MAC)
	}
	if cfg.MTU == 0 {
		cfg.MTU = ethernet.DefaultMTU
	}
	if cfg.MTU < 46 || cfg.MTU > 9000 {
		return nil, fmt.Errorf("vnet: endpoint %q: %w: MTU %d", cfg.Name, profinet.ErrInvalidConfig, cfg.MTU)
	}
	if cfg.QueueLen <= 0 {
		cfg.QueueLen = DefaultQueueLen
	}
	h := fnv.New64a()
	h.Write(cfg.MAC[:])
	e := &Endpoint{
		net:    n,
		cfg:    cfg,
		rx:     make(chan []byte, cfg.QueueLen),
		closed: make(chan struct{}),
		groups: make(map[ethernet.MAC]bool),
		rng:    rand.New(rand.NewPCG(n.seed, h.Sum64())),
	}
	e.up.Store(true)
	if cfg.VLANs != nil {
		e.vlans = make(map[uint16]bool, len(cfg.VLANs))
		for _, v := range cfg.VLANs {
			e.vlans[v] = true
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, fmt.Errorf("vnet: %w", profinet.ErrClosed)
	}
	if _, dup := n.byMAC[cfg.MAC]; dup {
		return nil, fmt.Errorf("vnet: endpoint %q: %w: MAC %s already in use", cfg.Name, profinet.ErrInvalidConfig, cfg.MAC)
	}
	n.byMAC[cfg.MAC] = e
	n.endpoints = append(n.endpoints, e)
	return e, nil
}

// Endpoints returns the endpoints attached to the network.
func (n *Network) Endpoints() []*Endpoint {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return append([]*Endpoint(nil), n.endpoints...)
}

// Close detaches and closes every endpoint.
func (n *Network) Close() error {
	n.mu.Lock()
	n.closed = true
	eps := n.endpoints
	n.mu.Unlock()
	for _, e := range eps {
		_ = e.Close()
	}
	return nil
}

func (n *Network) detach(e *Endpoint) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.byMAC[e.cfg.MAC] == e {
		delete(n.byMAC, e.cfg.MAC)
	}
	for i, v := range n.endpoints {
		if v == e {
			n.endpoints = append(n.endpoints[:i:i], n.endpoints[i+1:]...)
			break
		}
	}
}

// deliver hands a frame to its receivers. It copies the frame for each.
func (n *Network) deliver(from *Endpoint, frame []byte) {
	if len(frame) < ethernet.HeaderLen {
		return
	}
	var dst ethernet.MAC
	copy(dst[:], frame[:6])
	vid, tagged := vlanOf(frame)
	n.mu.RLock()
	defer n.mu.RUnlock()
	if !dst.IsMulticast() {
		if to := n.byMAC[dst]; to != nil && to != from && to.acceptsVLAN(vid, tagged) {
			to.receive(frame)
		}
		for _, e := range n.endpoints {
			if e != from && e.cfg.Promiscuous && e.cfg.MAC != dst && e.acceptsVLAN(vid, tagged) {
				e.receive(frame)
			}
		}
		return
	}
	for _, e := range n.endpoints {
		if e == from || !e.acceptsVLAN(vid, tagged) || !e.acceptsGroup(dst) {
			continue
		}
		e.receive(frame)
	}
}

func vlanOf(frame []byte) (uint16, bool) {
	if len(frame) >= ethernet.HeaderLen+ethernet.VLANTagLen &&
		ethernet.EtherType(uint16(frame[12])<<8|uint16(frame[13])) == ethernet.EtherTypeVLAN {
		return (uint16(frame[14])<<8 | uint16(frame[15])) & 0x0FFF, true
	}
	return 0, false
}

// Endpoint is a port on a virtual network. It implements l2.Port,
// l2.MulticastPort and l2.LinkStater.
type Endpoint struct {
	net *Network
	cfg EndpointConfig

	rx     chan []byte
	closed chan struct{}
	once   sync.Once

	up atomic.Bool

	gmu    sync.RWMutex
	groups map[ethernet.MAC]bool
	vlans  map[uint16]bool

	// fmu guards the fault state of the egress: the generator, the held
	// frame of a reorder and the delay line.
	fmu    sync.Mutex
	faults Faults
	rng    *rand.Rand
	held   []byte
	holdT  *time.Timer
	line   chan delayed
	lineWG sync.WaitGroup

	sent, received, dropped, duplicated, corrupted, reordered, overflow, linkDrops atomic.Uint64
}

type delayed struct {
	due   time.Time
	frame []byte
}

// Name returns the endpoint's configured name.
func (e *Endpoint) Name() string { return e.cfg.Name }

// HardwareAddr returns the endpoint's MAC address.
func (e *Endpoint) HardwareAddr() ethernet.MAC { return e.cfg.MAC }

// MTU returns the endpoint's MTU.
func (e *Endpoint) MTU() int { return e.cfg.MTU }

// SetFaults replaces the faults applied to the frames the endpoint sends.
func (e *Endpoint) SetFaults(f Faults) {
	e.fmu.Lock()
	e.faults = f
	e.fmu.Unlock()
}

// SetLinkUp sets the state of the endpoint's link. While it is down, the
// endpoint neither sends nor receives: WriteFrame fails with an error
// matching profinet.ErrLinkDown and frames addressed to it are dropped.
func (e *Endpoint) SetLinkUp(up bool) { e.up.Store(up) }

// LinkUp reports whether the endpoint's link is up.
func (e *Endpoint) LinkUp() bool { return e.up.Load() }

// JoinMulticast adds a group the endpoint receives when it filters
// multicast.
func (e *Endpoint) JoinMulticast(group ethernet.MAC) error {
	if !group.IsMulticast() {
		return fmt.Errorf("vnet: %w: %s is not a multicast address", profinet.ErrInvalidConfig, group)
	}
	e.gmu.Lock()
	e.groups[group] = true
	e.gmu.Unlock()
	return nil
}

// LeaveMulticast removes a group.
func (e *Endpoint) LeaveMulticast(group ethernet.MAC) error {
	e.gmu.Lock()
	delete(e.groups, group)
	e.gmu.Unlock()
	return nil
}

func (e *Endpoint) acceptsGroup(g ethernet.MAC) bool {
	if g.IsBroadcast() || !e.cfg.FilterMulticast {
		return true
	}
	e.gmu.RLock()
	defer e.gmu.RUnlock()
	return e.groups[g]
}

func (e *Endpoint) acceptsVLAN(vid uint16, tagged bool) bool {
	if !tagged || vid == 0 || e.vlans == nil {
		return true
	}
	return e.vlans[vid]
}

// Stats returns a snapshot of the endpoint's counters.
func (e *Endpoint) Stats() EndpointStats {
	return EndpointStats{
		Sent:       e.sent.Load(),
		Received:   e.received.Load(),
		Dropped:    e.dropped.Load(),
		Duplicated: e.duplicated.Load(),
		Corrupted:  e.corrupted.Load(),
		Reordered:  e.reordered.Load(),
		Overflow:   e.overflow.Load(),
		LinkDrops:  e.linkDrops.Load(),
	}
}

func (e *Endpoint) receive(frame []byte) {
	if !e.up.Load() {
		e.linkDrops.Add(1)
		return
	}
	select {
	case <-e.closed:
		return
	default:
	}
	c := make([]byte, len(frame))
	copy(c, frame)
	select {
	case e.rx <- c:
		e.received.Add(1)
	default:
		e.overflow.Add(1)
	}
}

// ReadFrame implements l2.Port.
func (e *Endpoint) ReadFrame(ctx context.Context, p []byte) (int, error) {
	select {
	case f := <-e.rx:
		return copy(p, f), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-e.closed:
		return 0, fmt.Errorf("vnet: endpoint %q: %w", e.cfg.Name, profinet.ErrClosed)
	}
}

// WriteFrame implements l2.Port. The frame is padded to the minimum frame
// size and copied; p is not retained.
func (e *Endpoint) WriteFrame(ctx context.Context, p []byte) error {
	select {
	case <-e.closed:
		return fmt.Errorf("vnet: endpoint %q: %w", e.cfg.Name, profinet.ErrClosed)
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !e.up.Load() {
		e.linkDrops.Add(1)
		return fmt.Errorf("vnet: endpoint %q: %w", e.cfg.Name, profinet.ErrLinkDown)
	}
	if len(p) < ethernet.HeaderLen {
		return fmt.Errorf("vnet: endpoint %q: %w: frame of %d octets", e.cfg.Name, profinet.ErrMalformed, len(p))
	}
	_, off, _ := ethernet.PeekEtherType(p)
	if len(p)-off > e.cfg.MTU {
		return fmt.Errorf("vnet: endpoint %q: %w: payload of %d octets exceeds the MTU %d", e.cfg.Name, profinet.ErrInvalidConfig, len(p)-off, e.cfg.MTU)
	}
	frame := make([]byte, len(p), max(len(p), ethernet.MinFrameLen))
	copy(frame, p)
	frame = ethernet.Pad(frame, 0)
	e.sent.Add(1)

	e.net.mu.RLock()
	taps := e.net.taps
	e.net.mu.RUnlock()
	for _, t := range taps {
		t(e, frame)
	}
	e.egress(frame)
	return nil
}

// egress applies the faults and delivers.
func (e *Endpoint) egress(frame []byte) {
	e.fmu.Lock()
	f := e.faults
	if !f.active() || (f.Match != nil && !f.Match(frame)) {
		held := e.takeHeldLocked()
		line := e.line
		e.fmu.Unlock()
		e.send(line, frame, 0)
		if held != nil {
			e.send(line, held, 0)
		}
		return
	}
	if e.rng.Float64() < f.Loss {
		e.fmu.Unlock()
		e.dropped.Add(1)
		return
	}
	if e.rng.Float64() < f.Corrupt && len(frame) > ethernet.HeaderLen {
		i := ethernet.HeaderLen + e.rng.IntN(len(frame)-ethernet.HeaderLen)
		frame[i] ^= 1 << e.rng.IntN(8)
		e.corrupted.Add(1)
	}
	dup := e.rng.Float64() < f.Duplicate
	delay := f.Delay
	if f.Jitter > 0 {
		delay += time.Duration(e.rng.Int64N(int64(f.Jitter) + 1))
	}
	if delay > 0 && e.line == nil {
		e.line = make(chan delayed, 4096)
		e.lineWG.Add(1)
		go e.runLine(e.line)
	}
	line := e.line
	if e.held == nil && e.rng.Float64() < f.Reorder {
		e.held = frame
		e.reordered.Add(1)
		hold := f.ReorderHold
		if hold <= 0 {
			hold = 5 * time.Millisecond
		}
		e.holdT = time.AfterFunc(hold, e.flushHeld)
		e.fmu.Unlock()
		return
	}
	held := e.takeHeldLocked()
	e.fmu.Unlock()

	e.send(line, frame, delay)
	if dup {
		e.duplicated.Add(1)
		c := append([]byte(nil), frame...)
		e.send(line, c, delay)
	}
	if held != nil {
		e.send(line, held, delay)
	}
}

func (e *Endpoint) takeHeldLocked() []byte {
	h := e.held
	if h != nil {
		e.held = nil
		e.holdT.Stop()
	}
	return h
}

func (e *Endpoint) flushHeld() {
	e.fmu.Lock()
	h := e.held
	e.held = nil
	line := e.line
	e.fmu.Unlock()
	if h != nil {
		e.send(line, h, 0)
	}
}

// send delivers now, or through the delay line so that delayed frames
// keep their order.
func (e *Endpoint) send(line chan delayed, frame []byte, delay time.Duration) {
	if line == nil {
		e.net.deliver(e, frame)
		return
	}
	select {
	case line <- delayed{due: time.Now().Add(delay), frame: frame}:
	case <-e.closed:
	}
}

func (e *Endpoint) runLine(line chan delayed) {
	defer e.lineWG.Done()
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	for {
		select {
		case d := <-line:
			if w := time.Until(d.due); w > 0 {
				t.Reset(w)
				select {
				case <-t.C:
				case <-e.closed:
					return
				}
			}
			e.net.deliver(e, d.frame)
		case <-e.closed:
			return
		}
	}
}

// Close detaches the endpoint from the network and unblocks ReadFrame.
func (e *Endpoint) Close() error {
	e.once.Do(func() {
		close(e.closed)
		e.net.detach(e)
		e.fmu.Lock()
		if e.holdT != nil {
			e.holdT.Stop()
		}
		e.held = nil
		e.fmu.Unlock()
		e.lineWG.Wait()
	})
	return nil
}
