// SPDX-License-Identifier: MIT

package alarm

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/pnio"
)

// EndpointConfig configures one priority of the alarm transport of an AR.
type EndpointConfig struct {
	// High selects the high priority (FrameID 0xFC01).
	High bool
	// LocalRef is this side's alarm reference, RemoteRef the peer's.
	LocalRef, RemoteRef uint16
	// RemoteMAC is the peer's address.
	RemoteMAC ethernet.MAC
	// Timeout is the retransmission timeout (RTATimeoutFactor × 100 ms);
	// Retries the number of retransmissions (RTARetries).
	Timeout time.Duration
	Retries int
	// OnData receives the VarPart of every DATA PDU accepted in sequence,
	// on the endpoint's own goroutine (it may call Send). The slice is a
	// copy.
	OnData func(varPart []byte)
	// OnErr is called when the peer sends an ERR PDU (an abort).
	OnErr func(status pnio.Status)
}

// EndpointStats are the counters of an [Endpoint].
type EndpointStats struct {
	Sent, Received, Retransmits, Duplicates, OutOfSequence, Acks, Nacks uint64
}

// Endpoint is the alarm sender and receiver of one priority of one AR:
// window size 1, one DATA PDU outstanding at a time.
type Endpoint struct {
	cfg     EndpointConfig
	frameID uint16
	mgr     *Manager

	sendMu sync.Mutex // one Send at a time

	mu       sync.Mutex
	sendSeq  uint16
	lastRecv uint16
	expect   uint16
	waitSeq  int32 // the sequence number awaiting its ACK, -1 when none
	acked    chan struct{}
	nacked   chan struct{}

	deliver chan []byte
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup

	sent, received, retrans, dups, oos, acks, nacks atomic.Uint64
}

func newEndpoint(m *Manager, cfg EndpointConfig) *Endpoint {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 100 * time.Millisecond
	}
	if cfg.Retries <= 0 {
		cfg.Retries = 3
	}
	e := &Endpoint{
		cfg:      cfg,
		frameID:  pnio.FrameIDAlarmLow,
		mgr:      m,
		sendSeq:  InitialSendSeq,
		lastRecv: InitialAckSeq,
		expect:   nextSeq(InitialSendSeq),
		waitSeq:  -1,
		deliver:  make(chan []byte, 16),
		done:     make(chan struct{}),
	}
	if cfg.High {
		e.frameID = pnio.FrameIDAlarmHigh
	}
	e.wg.Add(1)
	go e.run()
	return e
}

// LocalRef returns the endpoint's alarm reference.
func (e *Endpoint) LocalRef() uint16 { return e.cfg.LocalRef }

// Stats returns a snapshot of the counters.
func (e *Endpoint) Stats() EndpointStats {
	return EndpointStats{
		Sent: e.sent.Load(), Received: e.received.Load(), Retransmits: e.retrans.Load(),
		Duplicates: e.dups.Load(), OutOfSequence: e.oos.Load(), Acks: e.acks.Load(), Nacks: e.nacks.Load(),
	}
}

func (e *Endpoint) run() {
	defer e.wg.Done()
	for {
		select {
		case d := <-e.deliver:
			if e.cfg.OnData != nil {
				e.cfg.OnData(d)
			}
		case <-e.done:
			return
		}
	}
}

func (e *Endpoint) transmit(ctx context.Context, p *PDU) error {
	p.FrameID = e.frameID
	p.Dst = e.cfg.RemoteRef
	p.Src = e.cfg.LocalRef
	p.Version = Version
	b := p.AppendEthernet(make([]byte, 0, 64+len(p.VarPart)), e.cfg.RemoteMAC, e.mgr.mux.HardwareAddr())
	return e.mgr.mux.Send(ctx, b)
}

// Send transmits a DATA PDU carrying varPart and waits for its transport
// acknowledgement, retransmitting it every Timeout up to Retries times.
// It returns an error matching profinet.ErrTimeout when the peer never
// acknowledged (the owner then aborts the AR).
func (e *Endpoint) Send(ctx context.Context, varPart []byte) error {
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	select {
	case <-e.done:
		return fmt.Errorf("alarm: %w", profinet.ErrClosed)
	default:
	}
	e.mu.Lock()
	seq := nextSeq(e.sendSeq)
	e.sendSeq = seq
	e.waitSeq = int32(seq)
	acked := make(chan struct{})
	nacked := make(chan struct{}, 1)
	e.acked, e.nacked = acked, nacked
	ack := e.lastRecv
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.waitSeq = -1
		e.mu.Unlock()
	}()

	p := PDU{Type: PDUData, AddFlags: WindowSize | FlagTACK, SendSeq: seq, AckSeq: ack, VarPart: varPart}
	for attempt := 0; attempt <= e.cfg.Retries; attempt++ {
		if attempt > 0 {
			e.retrans.Add(1)
			e.mu.Lock()
			p.AckSeq = e.lastRecv
			e.mu.Unlock()
		}
		if err := e.transmit(ctx, &p); err != nil {
			return fmt.Errorf("alarm: send: %w", err)
		}
		e.sent.Add(1)
		t := time.NewTimer(e.cfg.Timeout)
		select {
		case <-acked:
			t.Stop()
			return nil
		case <-nacked:
			t.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-e.done:
			t.Stop()
			return fmt.Errorf("alarm: %w", profinet.ErrClosed)
		}
	}
	return fmt.Errorf("alarm: DATA %d not acknowledged after %d retransmissions: %w", seq, e.cfg.Retries, profinet.ErrTimeout)
}

// SendErr sends an ERR PDU with status, the abort of the AR.
func (e *Endpoint) SendErr(ctx context.Context, status pnio.Status) error {
	e.mu.Lock()
	p := PDU{Type: PDUErr, AddFlags: WindowSize, SendSeq: e.sendSeq, AckSeq: e.lastRecv, VarPart: status.Append(nil)}
	e.mu.Unlock()
	return e.transmit(ctx, &p)
}

func (e *Endpoint) ackLocked(seq uint16) {
	if e.waitSeq >= 0 && uint16(e.waitSeq) == seq {
		e.waitSeq = -1
		close(e.acked)
	}
}

// handle processes a PDU addressed to this endpoint, on the manager's
// goroutine.
func (e *Endpoint) handle(p *PDU) {
	switch p.Type {
	case PDUErr:
		var st pnio.Status
		if len(p.VarPart) >= 4 {
			st = pnio.StatusFromUint32(binary.BigEndian.Uint32(p.VarPart))
		}
		if e.cfg.OnErr != nil {
			e.cfg.OnErr(st)
		}
	case PDUAck:
		e.acks.Add(1)
		e.mu.Lock()
		e.ackLocked(p.AckSeq)
		e.mu.Unlock()
	case PDUNack:
		e.nacks.Add(1)
		e.mu.Lock()
		if e.waitSeq >= 0 {
			select {
			case e.nacked <- struct{}{}:
			default:
			}
		}
		e.mu.Unlock()
	case PDUData:
		e.mu.Lock()
		e.ackLocked(p.AckSeq) // a DATA PDU may acknowledge ours
		var reply *PDU
		var data []byte
		switch p.SendSeq {
		case e.expect:
			e.lastRecv = p.SendSeq
			e.expect = nextSeq(p.SendSeq)
			data = append([]byte(nil), p.VarPart...)
			e.received.Add(1)
			reply = &PDU{Type: PDUAck, AddFlags: WindowSize, SendSeq: e.sendSeq, AckSeq: p.SendSeq}
		case e.lastRecv:
			e.dups.Add(1)
			reply = &PDU{Type: PDUAck, AddFlags: WindowSize, SendSeq: e.sendSeq, AckSeq: p.SendSeq}
		default:
			e.oos.Add(1)
			reply = &PDU{Type: PDUNack, AddFlags: WindowSize, SendSeq: e.sendSeq, AckSeq: e.lastRecv}
		}
		e.mu.Unlock()
		if reply != nil && (p.TACK() || reply.Type == PDUNack) {
			_ = e.transmit(context.Background(), reply)
		}
		if data != nil {
			select {
			case e.deliver <- data:
			case <-e.done:
			}
		}
	}
}

// Close stops the endpoint and unregisters it.
func (e *Endpoint) Close() {
	e.once.Do(func() {
		close(e.done)
		e.mgr.remove(e)
		e.wg.Wait()
	})
}

type epKey struct {
	ref  uint16
	high bool
}

// Manager receives the RTA PDUs of a port and routes them to endpoints by
// their destination reference and priority.
type Manager struct {
	mux *l2.Mux
	sub *l2.Subscription

	mu        sync.Mutex
	endpoints map[epKey]*Endpoint
	nextRef   uint16

	malformed, unknown atomic.Uint64

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// NewManager starts routing alarm PDUs received on mux.
func NewManager(mux *l2.Mux) (*Manager, error) {
	sub, err := mux.Subscribe(func(f *ethernet.Frame) bool {
		if f.EtherType != ethernet.EtherTypeProfinet || len(f.Payload) < 2 {
			return false
		}
		id := binary.BigEndian.Uint16(f.Payload)
		return id == pnio.FrameIDAlarmHigh || id == pnio.FrameIDAlarmLow
	}, 256)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{mux: mux, sub: sub, endpoints: map[epKey]*Endpoint{}, nextRef: 1, cancel: cancel, done: make(chan struct{})}
	go m.run(ctx)
	return m, nil
}

// AllocRef returns an alarm reference no endpoint uses.
func (m *Manager) AllocRef() uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		r := m.nextRef
		m.nextRef++
		if m.nextRef == 0xFFFF {
			m.nextRef = 1
		}
		if m.endpoints[epKey{r, false}] == nil && m.endpoints[epKey{r, true}] == nil {
			return r
		}
	}
}

// Open creates and registers an endpoint.
func (m *Manager) Open(cfg EndpointConfig) (*Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := epKey{cfg.LocalRef, cfg.High}
	if m.endpoints[k] != nil {
		return nil, fmt.Errorf("alarm: %w: alarm reference %d in use", profinet.ErrInvalidConfig, cfg.LocalRef)
	}
	e := newEndpoint(m, cfg)
	m.endpoints[k] = e
	return e, nil
}

func (m *Manager) remove(e *Endpoint) {
	m.mu.Lock()
	k := epKey{e.cfg.LocalRef, e.cfg.High}
	if m.endpoints[k] == e {
		delete(m.endpoints, k)
	}
	m.mu.Unlock()
}

// Malformed returns the number of PDUs that could not be decoded.
func (m *Manager) Malformed() uint64 { return m.malformed.Load() }

// Unknown returns the number of PDUs for an unknown reference.
func (m *Manager) Unknown() uint64 { return m.unknown.Load() }

// Close stops routing. Endpoints are not closed.
func (m *Manager) Close() {
	m.once.Do(func() {
		m.cancel()
		m.sub.Close()
		<-m.done
	})
}

func (m *Manager) run(ctx context.Context) {
	defer close(m.done)
	for {
		f, err := m.sub.Recv(ctx)
		if err != nil {
			return
		}
		p, err := Decode(f.Payload)
		if err != nil {
			m.malformed.Add(1)
			continue
		}
		m.mu.Lock()
		e := m.endpoints[epKey{p.Dst, p.FrameID == pnio.FrameIDAlarmHigh}]
		m.mu.Unlock()
		if e == nil || (!e.cfg.RemoteMAC.IsZero() && f.Src != e.cfg.RemoteMAC) {
			m.unknown.Add(1)
			continue
		}
		e.handle(&p)
	}
}
