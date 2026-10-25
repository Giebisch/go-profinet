// SPDX-License-Identifier: MIT

package rt

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
)

// ProviderConfig configures a [Provider].
type ProviderConfig struct {
	FrameID    uint16
	DataLength uint16
	// Dst is the consumer's MAC address, Src the provider's.
	Dst, Src ethernet.MAC
	// VLAN is the VLAN id of the 802.1Q tag (priority 6); 0 sends
	// priority-tagged frames.
	VLAN uint16
	// CounterStep is SendClockFactor × ReductionRatio: the cycle counter
	// advances by it with every frame.
	CounterStep uint16
	// Send transmits a frame; it must not retain the buffer.
	Send func(ctx context.Context, frame []byte) error
	// Fill, when set, is called with the C_SDU before every frame, under
	// the provider's lock, to bring it up to date. It must not block or
	// retain the slice.
	Fill func(cSDU []byte)
}

// ProviderStats are the counters of a [Provider].
type ProviderStats struct {
	Sent, SendErrors uint64
}

// Provider sends the cyclic frames of one IOCR. The application updates
// the C_SDU with Update; the scheduler calls Tick once per cycle. Tick
// encodes into a preallocated buffer and does not allocate.
type Provider struct {
	cfg ProviderConfig

	mu      sync.Mutex
	data    []byte
	status  DataStatus
	counter uint16
	frame   []byte

	sent, errs atomic.Uint64
}

// NewProvider returns a provider with an all-zero C_SDU and
// DefaultDataStatus.
func NewProvider(cfg ProviderConfig) *Provider {
	return &Provider{
		cfg:    cfg,
		data:   make([]byte, cfg.DataLength),
		status: DefaultDataStatus,
		frame:  make([]byte, 0, ethernet.HeaderLen+ethernet.VLANTagLen+2+int(cfg.DataLength)+TrailerLen+ethernet.MinFrameLen),
	}
}

// FrameID returns the FrameID the provider sends.
func (p *Provider) FrameID() uint16 { return p.cfg.FrameID }

// Update calls fn with the C_SDU under the provider's lock; fn may change
// it in place and must not retain it.
func (p *Provider) Update(fn func(cSDU []byte)) {
	p.mu.Lock()
	fn(p.data)
	p.mu.Unlock()
}

// SetDataStatus sets the DataStatus of the frames to come.
func (p *Provider) SetDataStatus(s DataStatus) {
	p.mu.Lock()
	p.status = s
	p.mu.Unlock()
}

// DataStatus returns the DataStatus the provider sends.
func (p *Provider) DataStatus() DataStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// SetDestination changes the destination MAC address.
func (p *Provider) SetDestination(dst ethernet.MAC) {
	p.mu.Lock()
	p.cfg.Dst = dst
	p.mu.Unlock()
}

// Tick sends one frame.
func (p *Provider) Tick(ctx context.Context) {
	p.mu.Lock()
	p.counter += p.cfg.CounterStep
	if p.cfg.Fill != nil {
		p.cfg.Fill(p.data)
	}
	f := Frame{FrameID: p.cfg.FrameID, Data: p.data, CycleCounter: p.counter, DataStatus: p.status}
	p.frame = AppendEthernet(p.frame[:0], p.cfg.Dst, p.cfg.Src, p.cfg.VLAN, &f)
	err := p.cfg.Send(ctx, p.frame)
	p.mu.Unlock()
	if err != nil {
		p.errs.Add(1)
		return
	}
	p.sent.Add(1)
}

// Stats returns a snapshot of the counters.
func (p *Provider) Stats() ProviderStats {
	return ProviderStats{Sent: p.sent.Load(), SendErrors: p.errs.Load()}
}

// ConsumerConfig configures a [Consumer].
type ConsumerConfig struct {
	FrameID    uint16
	DataLength uint16
	// Src is the provider's MAC address; frames from other addresses are
	// counted and dropped. Zero accepts any source.
	Src ethernet.MAC
	// Watchdog is the data hold time: when no valid frame arrives for
	// this long, the consumer expires.
	Watchdog time.Duration
}

// ConsumerStats are the counters of a [Consumer].
type ConsumerStats struct {
	Received uint64
	// Stale counts frames whose cycle counter did not advance (duplicates
	// and reordered frames).
	Stale uint64
	// Malformed counts frames too short for the C_SDU.
	Malformed uint64
	// WrongSource counts frames from another MAC address.
	WrongSource uint64
	// Expired counts watchdog expiries.
	Expired uint64
	// LastCounter is the cycle counter of the last accepted frame.
	LastCounter uint16
}

// Consumer receives the cyclic frames of one IOCR, keeps the newest C_SDU
// and watches the data hold time.
type Consumer struct {
	cfg ConsumerConfig

	mu       sync.Mutex
	data     []byte
	status   DataStatus
	counter  uint16
	have     bool
	lastRx   time.Time
	expired  bool
	onUpdate func(cSDU []byte, s DataStatus)

	received, stale, malformed, wrongSrc, expiries atomic.Uint64
}

// NewConsumer returns a consumer whose watchdog starts now.
func NewConsumer(cfg ConsumerConfig) *Consumer {
	return &Consumer{cfg: cfg, data: make([]byte, cfg.DataLength), lastRx: time.Now()}
}

// FrameID returns the FrameID the consumer receives.
func (c *Consumer) FrameID() uint16 { return c.cfg.FrameID }

// OnUpdate registers a function called, on the receiving goroutine and
// under the consumer's lock, with every accepted C_SDU. It must not block
// or retain the slice.
func (c *Consumer) OnUpdate(fn func(cSDU []byte, s DataStatus)) {
	c.mu.Lock()
	c.onUpdate = fn
	c.mu.Unlock()
}

// SetSource changes the expected provider MAC address.
func (c *Consumer) SetSource(src ethernet.MAC) {
	c.mu.Lock()
	c.cfg.Src = src
	c.mu.Unlock()
}

// Handle processes one received frame.
func (c *Consumer) Handle(f *ethernet.Frame, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cfg.Src.IsZero() && f.Src != c.cfg.Src {
		c.wrongSrc.Add(1)
		return
	}
	fr, err := Decode(f.Payload, int(c.cfg.DataLength))
	if err != nil || fr.FrameID != c.cfg.FrameID {
		c.malformed.Add(1)
		return
	}
	if c.have && !CounterNewer(fr.CycleCounter, c.counter) {
		c.stale.Add(1)
		return
	}
	if fr.DataStatus&IgnoreDataStatus == 0 {
		copy(c.data, fr.Data)
		c.status = fr.DataStatus
	}
	c.counter = fr.CycleCounter
	c.have = true
	c.lastRx = now
	c.expired = false
	c.received.Add(1)
	if c.onUpdate != nil {
		c.onUpdate(c.data, c.status)
	}
}

// Check reports whether the watchdog expired at now. It returns true once
// per expiry; a frame that arrives afterwards re-arms it.
func (c *Consumer) Check(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expired || c.cfg.Watchdog <= 0 || now.Sub(c.lastRx) <= c.cfg.Watchdog {
		return false
	}
	c.expired = true
	c.expiries.Add(1)
	return true
}

// Read calls fn with the newest C_SDU and DataStatus under the consumer's
// lock. ok is false until the first frame arrived.
func (c *Consumer) Read(fn func(cSDU []byte, s DataStatus)) (ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c.data, c.status)
	return c.have
}

// Stats returns a snapshot of the counters.
func (c *Consumer) Stats() ConsumerStats {
	c.mu.Lock()
	last := c.counter
	c.mu.Unlock()
	return ConsumerStats{
		Received:    c.received.Load(),
		Stale:       c.stale.Load(),
		Malformed:   c.malformed.Load(),
		WrongSource: c.wrongSrc.Load(),
		Expired:     c.expiries.Load(),
		LastCounter: last,
	}
}

// Dispatcher routes received cyclic frames to consumers by FrameID. It
// owns one subscription of an l2.Mux covering the RT_CLASS_1 and
// RT_CLASS_2 FrameID ranges.
type Dispatcher struct {
	sub *l2.Subscription

	mu        sync.RWMutex
	consumers map[uint16]*Consumer

	unknown atomic.Uint64
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

// NewDispatcher starts dispatching cyclic frames received on mux.
func NewDispatcher(mux *l2.Mux) (*Dispatcher, error) {
	sub, err := mux.Subscribe(l2.MatchFrameIDs(0x0100, 0xFBFF), 1024)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{sub: sub, consumers: map[uint16]*Consumer{}, cancel: cancel, done: make(chan struct{})}
	go d.run(ctx)
	return d, nil
}

// Add routes frames with the consumer's FrameID to it. A consumer already
// registered for the FrameID is replaced.
func (d *Dispatcher) Add(c *Consumer) {
	d.mu.Lock()
	d.consumers[c.cfg.FrameID] = c
	d.mu.Unlock()
}

// Remove stops routing to c.
func (d *Dispatcher) Remove(c *Consumer) {
	d.mu.Lock()
	if d.consumers[c.cfg.FrameID] == c {
		delete(d.consumers, c.cfg.FrameID)
	}
	d.mu.Unlock()
}

// Unknown returns the number of frames with a FrameID no consumer takes.
func (d *Dispatcher) Unknown() uint64 { return d.unknown.Load() }

// Close stops the dispatcher.
func (d *Dispatcher) Close() {
	d.once.Do(func() {
		d.cancel()
		d.sub.Close()
		<-d.done
	})
}

func (d *Dispatcher) run(ctx context.Context) {
	defer close(d.done)
	for {
		f, err := d.sub.Recv(ctx)
		if err != nil {
			return
		}
		if len(f.Payload) < 2 {
			continue
		}
		id := binary.BigEndian.Uint16(f.Payload)
		d.mu.RLock()
		c := d.consumers[id]
		d.mu.RUnlock()
		if c == nil {
			d.unknown.Add(1)
			continue
		}
		c.Handle(&f, time.Now())
	}
}
