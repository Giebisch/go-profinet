// SPDX-License-Identifier: MIT

// Package bridge is the switch of a multiport device: a learning Ethernet
// bridge between its physical ports, with the device's interface attached
// as one more port (the "CPU port").
//
// Frames are classified by their destination address:
//
//   - Link-local frames (01-80-C2-00-00-00 to -0F: LLDP, PTCP delay) and
//     MRP frames (01-15-4E-00-00-01 to -04) belong to the port they arrived
//     on: they reach that port's local [l2.Port] ([Bridge.Port]) and are
//     never forwarded. What the device sends on a local port leaves on that
//     port only, whatever its state: the LLDP agent and the MRP entity of
//     each port use these.
//   - Frames to the interface's MAC address reach the CPU port
//     ([Bridge.CPU]); broadcast and other multicast frames reach it and
//     are flooded to the other forwarding ports; other unicast frames go
//     to the port their destination was learned on, or are flooded.
//   - What the device sends on the CPU port goes to the port its
//     destination was learned on, or is flooded to every forwarding port.
//
// A port is forwarding or blocked ([Bridge.SetState]): a blocked port
// carries only its local frames, as a ring port that MRP blocks. A port
// can also be a boundary for multicast groups ([Bridge.SetBoundary]):
// frames to those groups are neither forwarded to nor from it; they still
// reach the CPU port.
//
// The forwarding database is bounded: when it is full new stations are
// not learned (their frames are flooded). Entries age out, and
// [Bridge.Flush] forgets them all, as MRP topology changes demand. Nothing
// here is hardware switching: forwarding takes a goroutine hop and a copy
// per frame, so the bridge suits tests and software devices, not
// deterministic real-time forwarding.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
)

// State is the forwarding state of a port.
type State int32

const (
	// Forwarding: the port carries every frame.
	Forwarding State = iota
	// Blocked: the port carries only its local frames.
	Blocked
)

func (s State) String() string {
	if s == Blocked {
		return "blocked"
	}
	return "forwarding"
}

// Defaults.
const (
	DefaultQueueLen = 512
	DefaultAgeTime  = 300 * time.Second
	DefaultMaxFDB   = 1024
)

// Config configures a bridge.
type Config struct {
	// Ports are the physical ports, port 1 first; at least one. A bridge
	// forwards what is not addressed to it, so each port must receive
	// every frame on its link: promiscuous, without a multicast filter (a
	// vnet endpoint with Promiscuous set, a raw socket in promiscuous
	// mode).
	Ports []l2.Port
	// MAC is the interface's MAC address, the address of the CPU port;
	// required, and distinct from the ports' addresses.
	MAC ethernet.MAC
	// QueueLen is the receive queue of the CPU port and of every local
	// port; zero means DefaultQueueLen. A full queue drops the frame.
	QueueLen int
	// AgeTime is how long a learned station is kept without a frame from
	// it; zero means DefaultAgeTime.
	AgeTime time.Duration
	// MaxFDB bounds the forwarding database; zero means DefaultMaxFDB.
	MaxFDB int
	// Logger receives diagnostics; nil logs nothing.
	Logger profinet.Logger
}

// PortStats are the counters of a physical port.
type PortStats struct {
	Received  uint64 // frames read from the port
	Sent      uint64 // frames written to the port
	Local     uint64 // received frames delivered to the local port
	Forwarded uint64 // received frames forwarded to other ports
	Dropped   uint64 // received frames dropped (blocked, boundary, full queue)
	SendErrs  uint64 // writes that failed
}

// Bridge is a learning bridge between the physical ports of a device.
type Bridge struct {
	cfg   Config
	log   profinet.Logger
	ports []*phys
	cpu   *vport

	mu  sync.RWMutex
	fdb map[ethernet.MAC]fdbEntry

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type fdbEntry struct {
	port int
	seen time.Time
}

type phys struct {
	b     *Bridge
	i     int
	p     l2.Port
	mac   ethernet.MAC
	local *vport
	state atomic.Int32

	bmu      sync.RWMutex
	boundary map[ethernet.MAC]bool

	received, sent, forwarded, dropped, sendErrs atomic.Uint64
}

// New starts a bridge over cfg.Ports. Close stops it; it does not close
// the physical ports.
func New(cfg Config) (*Bridge, error) {
	if len(cfg.Ports) == 0 {
		return nil, fmt.Errorf("bridge: no ports: %w", profinet.ErrInvalidConfig)
	}
	if cfg.MAC.IsZero() || cfg.MAC.IsMulticast() {
		return nil, fmt.Errorf("bridge: interface MAC %s: %w", cfg.MAC, profinet.ErrInvalidConfig)
	}
	if cfg.QueueLen <= 0 {
		cfg.QueueLen = DefaultQueueLen
	}
	if cfg.AgeTime <= 0 {
		cfg.AgeTime = DefaultAgeTime
	}
	if cfg.MaxFDB <= 0 {
		cfg.MaxFDB = DefaultMaxFDB
	}
	log := cfg.Logger
	if log == nil {
		log = profinet.NopLogger()
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bridge{cfg: cfg, log: log, fdb: map[ethernet.MAC]fdbEntry{}, ctx: ctx, cancel: cancel}
	seen := map[ethernet.MAC]bool{cfg.MAC: true}
	for i, p := range cfg.Ports {
		if p == nil {
			cancel()
			return nil, fmt.Errorf("bridge: port %d is nil: %w", i+1, profinet.ErrInvalidConfig)
		}
		mac := p.HardwareAddr()
		if seen[mac] {
			cancel()
			return nil, fmt.Errorf("bridge: port %d: MAC %s used twice: %w", i+1, mac, profinet.ErrInvalidConfig)
		}
		seen[mac] = true
		ph := &phys{b: b, i: i, p: p, mac: mac, boundary: map[ethernet.MAC]bool{}}
		ph.local = newVport(b, mac, p.MTU(), cfg.QueueLen, ph.writeLocal, ph.linkUp)
		b.ports = append(b.ports, ph)
	}
	mtu := cfg.Ports[0].MTU()
	for _, p := range cfg.Ports[1:] {
		mtu = min(mtu, p.MTU())
	}
	b.cpu = newVport(b, cfg.MAC, mtu, cfg.QueueLen, b.writeCPU, b.anyLinkUp)
	for _, ph := range b.ports {
		b.wg.Add(1)
		go ph.run()
	}
	return b, nil
}

// CPU returns the interface's port: build the device's [l2.Mux] on it.
func (b *Bridge) CPU() l2.Port { return b.cpu }

// NumPorts returns the number of physical ports.
func (b *Bridge) NumPorts() int { return len(b.ports) }

// Port returns the local port of physical port i (0-based): its link-local
// and MRP frames, and a way to send on that port alone. It reports the
// physical port's MAC address and link state.
func (b *Bridge) Port(i int) l2.Port { return b.ports[i].local }

// Physical returns physical port i (0-based).
func (b *Bridge) Physical(i int) l2.Port { return b.ports[i].p }

// SetState sets the forwarding state of port i (0-based). Blocking a port
// forgets the stations learned on it.
func (b *Bridge) SetState(i int, s State) {
	ph := b.ports[i]
	if State(ph.state.Swap(int32(s))) == s {
		return
	}
	b.log.Debugf("bridge: port %d %s", i+1, s)
	if s == Blocked {
		b.forget(i)
	}
}

// State returns the forwarding state of port i (0-based).
func (b *Bridge) State(i int) State { return State(b.ports[i].state.Load()) }

// SetBoundary makes port i (0-based) a boundary for the given multicast
// groups: frames to them are not forwarded to or from it. nil clears it.
func (b *Bridge) SetBoundary(i int, groups []ethernet.MAC) {
	ph := b.ports[i]
	m := map[ethernet.MAC]bool{}
	for _, g := range groups {
		m[g] = true
	}
	ph.bmu.Lock()
	ph.boundary = m
	ph.bmu.Unlock()
}

// Flush forgets every learned station.
func (b *Bridge) Flush() {
	b.mu.Lock()
	clear(b.fdb)
	b.mu.Unlock()
}

// Stats returns the counters of port i (0-based).
func (b *Bridge) Stats(i int) PortStats {
	ph := b.ports[i]
	return PortStats{Received: ph.received.Load(), Sent: ph.sent.Load(), Local: ph.local.delivered.Load(),
		Forwarded: ph.forwarded.Load(), Dropped: ph.dropped.Load(), SendErrs: ph.sendErrs.Load()}
}

// Learned returns the port (0-based) a station was learned on.
func (b *Bridge) Learned(mac ethernet.MAC) (int, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	e, ok := b.fdb[mac]
	if !ok || time.Since(e.seen) > b.cfg.AgeTime {
		return 0, false
	}
	return e.port, true
}

// Close stops the bridge: the CPU and local ports report
// profinet.ErrClosed. The physical ports stay open.
func (b *Bridge) Close() error {
	b.once.Do(func() {
		b.cancel()
		b.cpu.close()
		for _, ph := range b.ports {
			ph.local.close()
		}
	})
	b.wg.Wait()
	return nil
}

// LinkLocal reports whether frames to dst stay on the port they arrive
// on: the IEEE 802.1D reserved group addresses and MRP's.
func LinkLocal(dst ethernet.MAC) bool {
	if dst[0] == 0x01 && dst[1] == 0x80 && dst[2] == 0xC2 && dst[3] == 0 && dst[4] == 0 && dst[5] <= 0x0F {
		return true
	}
	return dst[0] == 0x01 && dst[1] == 0x15 && dst[2] == 0x4E && dst[3] == 0 && dst[4] == 0 && dst[5] >= 0x01 && dst[5] <= 0x04
}

func (b *Bridge) anyLinkUp() bool {
	for _, ph := range b.ports {
		if ph.linkUp() {
			return true
		}
	}
	return false
}

func (b *Bridge) learn(src ethernet.MAC, port int) {
	if src.IsMulticast() || src.IsZero() {
		return
	}
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.fdb[src]; ok {
		// Refresh at most a few times per age time, not per frame.
		if e.port == port && now.Sub(e.seen) < min(time.Second, b.cfg.AgeTime/4) {
			return
		}
	} else if len(b.fdb) >= b.cfg.MaxFDB {
		// Full: expire what is old, else leave the station unlearned.
		for mac, e := range b.fdb {
			if now.Sub(e.seen) > b.cfg.AgeTime {
				delete(b.fdb, mac)
			}
		}
		if len(b.fdb) >= b.cfg.MaxFDB {
			return
		}
	}
	b.fdb[src] = fdbEntry{port: port, seen: now}
}

func (b *Bridge) forget(port int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for mac, e := range b.fdb {
		if e.port == port {
			delete(b.fdb, mac)
		}
	}
}

// lookup returns the forwarding port of a unicast destination, -1 when it
// is unknown or that port cannot carry it now.
func (b *Bridge) lookup(dst ethernet.MAC) int {
	i, ok := b.Learned(dst)
	if !ok || b.State(i) != Forwarding || !b.ports[i].linkUp() {
		return -1
	}
	return i
}

// writeCPU sends a frame of the device's interface.
func (b *Bridge) writeCPU(ctx context.Context, frame []byte) error {
	dst := dstOf(frame)
	if !dst.IsMulticast() {
		if i := b.lookup(dst); i >= 0 {
			return b.ports[i].write(ctx, frame)
		}
	}
	var errs []error
	sent := false
	for _, ph := range b.ports {
		if !ph.carries(dst) {
			continue
		}
		if err := ph.write(ctx, frame); err != nil {
			errs = append(errs, err)
			continue
		}
		sent = true
	}
	if !sent && len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// flood forwards a frame received on port from to every other port that
// carries it.
func (b *Bridge) flood(from int, frame []byte) {
	dst := dstOf(frame)
	for _, ph := range b.ports {
		if ph.i == from || !ph.carries(dst) {
			continue
		}
		_ = ph.write(b.ctx, frame)
	}
}

func dstOf(frame []byte) ethernet.MAC {
	var m ethernet.MAC
	copy(m[:], frame[:6])
	return m
}

func srcOf(frame []byte) ethernet.MAC {
	var m ethernet.MAC
	copy(m[:], frame[6:12])
	return m
}

func (ph *phys) linkUp() bool {
	if ls, ok := ph.p.(l2.LinkStater); ok {
		return ls.LinkUp()
	}
	return true
}

// carries reports whether the port forwards a frame to dst now.
func (ph *phys) carries(dst ethernet.MAC) bool {
	if State(ph.state.Load()) != Forwarding || !ph.linkUp() {
		return false
	}
	if dst.IsMulticast() {
		ph.bmu.RLock()
		defer ph.bmu.RUnlock()
		return !ph.boundary[dst]
	}
	return true
}

func (ph *phys) write(ctx context.Context, frame []byte) error {
	if err := ph.p.WriteFrame(ctx, frame); err != nil {
		ph.sendErrs.Add(1)
		return err
	}
	ph.sent.Add(1)
	return nil
}

// writeLocal sends a frame of the port's local entity on that port only.
func (ph *phys) writeLocal(ctx context.Context, frame []byte) error {
	return ph.write(ctx, frame)
}

func (ph *phys) run() {
	defer ph.b.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, err := ph.p.ReadFrame(ph.b.ctx, buf)
		if err != nil {
			if ph.b.ctx.Err() != nil {
				return
			}
			if errors.Is(err, profinet.ErrClosed) {
				ph.b.log.Debugf("bridge: port %d closed", ph.i+1)
				return
			}
			ph.b.log.Debugf("bridge: port %d: %v", ph.i+1, err)
			select {
			case <-ph.b.ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		if n < ethernet.HeaderLen {
			continue
		}
		ph.received.Add(1)
		ph.handle(append([]byte(nil), buf[:n]...))
	}
}

func (ph *phys) handle(frame []byte) {
	b := ph.b
	dst := dstOf(frame)
	if LinkLocal(dst) {
		if !ph.local.deliver(frame) {
			ph.dropped.Add(1)
		}
		return
	}
	for _, o := range b.ports {
		if dst == o.mac {
			// To a port's own address: its local entity's.
			if !o.local.deliver(frame) {
				ph.dropped.Add(1)
			}
			return
		}
	}
	if State(ph.state.Load()) != Forwarding {
		ph.dropped.Add(1)
		return
	}
	b.learn(srcOf(frame), ph.i)
	if dst == b.cfg.MAC {
		if !b.cpu.deliver(frame) {
			ph.dropped.Add(1)
		}
		return
	}
	if dst.IsMulticast() {
		b.cpu.deliver(frame)
		ph.bmu.RLock()
		blocked := ph.boundary[dst]
		ph.bmu.RUnlock()
		if blocked {
			ph.dropped.Add(1)
			return
		}
		ph.forwarded.Add(1)
		b.flood(ph.i, frame)
		return
	}
	if i := b.lookup(dst); i >= 0 {
		if i == ph.i {
			// The destination is on the port the frame came from.
			ph.dropped.Add(1)
			return
		}
		ph.forwarded.Add(1)
		_ = b.ports[i].write(b.ctx, frame)
		return
	}
	ph.forwarded.Add(1)
	b.flood(ph.i, frame)
}

// vport is a port of the bridge's own: the CPU port or a local port.
type vport struct {
	b         *Bridge
	mac       ethernet.MAC
	mtu       int
	q         chan []byte
	out       func(ctx context.Context, frame []byte) error
	link      func() bool
	done      chan struct{}
	once      sync.Once
	delivered atomic.Uint64
	overflow  atomic.Uint64
}

func newVport(b *Bridge, mac ethernet.MAC, mtu, qlen int, out func(context.Context, []byte) error, link func() bool) *vport {
	return &vport{b: b, mac: mac, mtu: mtu, q: make(chan []byte, qlen), out: out, link: link, done: make(chan struct{})}
}

func (v *vport) deliver(frame []byte) bool {
	select {
	case <-v.done:
		return false
	default:
	}
	select {
	case v.q <- frame:
		v.delivered.Add(1)
		return true
	default:
		v.overflow.Add(1)
		return false
	}
}

func (v *vport) close() { v.once.Do(func() { close(v.done) }) }

// ReadFrame implements l2.Port.
func (v *vport) ReadFrame(ctx context.Context, p []byte) (int, error) {
	select {
	case f := <-v.q:
		return copy(p, f), nil
	case <-v.done:
		return 0, profinet.ErrClosed
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// WriteFrame implements l2.Port.
func (v *vport) WriteFrame(ctx context.Context, p []byte) error {
	select {
	case <-v.done:
		return profinet.ErrClosed
	default:
	}
	if len(p) < ethernet.HeaderLen {
		return fmt.Errorf("bridge: frame of %d octets: %w", len(p), profinet.ErrMalformed)
	}
	if len(p) < ethernet.MinFrameLen {
		p = append(append(make([]byte, 0, ethernet.MinFrameLen), p...), make([]byte, ethernet.MinFrameLen-len(p))...)
	}
	return v.out(ctx, p)
}

// HardwareAddr implements l2.Port.
func (v *vport) HardwareAddr() ethernet.MAC { return v.mac }

// MTU implements l2.Port.
func (v *vport) MTU() int { return v.mtu }

// LinkUp implements l2.LinkStater: a local port's physical link; the CPU
// port is up while any physical port is.
func (v *vport) LinkUp() bool { return v.link() }

// Close implements l2.Port. It does not stop the bridge: a mux built on
// the port may close it while the bridge runs on.
func (v *vport) Close() error { return nil }

// Overflow returns the frames dropped because the port's queue was full.
func (v *vport) Overflow() uint64 { return v.overflow.Load() }
