// SPDX-License-Identifier: MIT

package l2

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// Matcher selects the frames a [Subscription] receives. It is called on
// the demultiplexer's goroutine with a frame that is valid only during the
// call; it must not block or retain f.
type Matcher func(f *ethernet.Frame) bool

// MatchEtherType matches frames of one EtherType.
func MatchEtherType(t ethernet.EtherType) Matcher {
	return func(f *ethernet.Frame) bool { return f.EtherType == t }
}

// MatchFrameIDs matches PROFINET frames (EtherType 0x8892) whose FrameID
// is in [lo, hi].
func MatchFrameIDs(lo, hi uint16) Matcher {
	return func(f *ethernet.Frame) bool {
		if f.EtherType != ethernet.EtherTypeProfinet || len(f.Payload) < 2 {
			return false
		}
		id := binary.BigEndian.Uint16(f.Payload)
		return id >= lo && id <= hi
	}
}

// MuxStats are the counters of a [Mux].
type MuxStats struct {
	// Received is the number of frames read from the port.
	Received uint64
	// Malformed is the number of frames that were too short to decode.
	Malformed uint64
	// Unclaimed is the number of frames no subscription matched.
	Unclaimed uint64
	// Overflow is the number of frames dropped because the queue of a
	// matching subscription was full.
	Overflow uint64
	// Sent is the number of frames written to the port.
	Sent uint64
}

// Mux shares one [Port] between several protocol engines. One goroutine
// reads frames and copies each into the bounded queue of every
// subscription that matches it; a full queue drops the frame (and counts
// it) instead of blocking the port. Sending goes straight to the port.
//
// A Mux is safe for concurrent use.
type Mux struct {
	port   Port
	maxLen int

	mu   sync.RWMutex
	subs []*Subscription

	received, malformed, unclaimed, overflow, sent atomic.Uint64

	cancel context.CancelFunc
	done   chan struct{}
	err    error
	once   sync.Once
}

// NewMux starts demultiplexing frames read from p. It does not take
// ownership of p: closing the Mux stops the reader; closing p is the
// caller's job (and also stops the reader).
func NewMux(p Port) *Mux {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Mux{
		port:   p,
		maxLen: p.MTU() + ethernet.HeaderLen + ethernet.VLANTagLen,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go m.run(ctx)
	return m
}

// Port returns the port the Mux reads from.
func (m *Mux) Port() Port { return m.port }

// HardwareAddr returns the MAC address of the port.
func (m *Mux) HardwareAddr() ethernet.MAC { return m.port.HardwareAddr() }

// Send writes one frame to the port.
func (m *Mux) Send(ctx context.Context, frame []byte) error {
	select {
	case <-m.done:
		return fmt.Errorf("l2: send: %w", profinet.ErrClosed)
	default:
	}
	if err := m.port.WriteFrame(ctx, frame); err != nil {
		return err
	}
	m.sent.Add(1)
	return nil
}

// Stats returns a snapshot of the counters.
func (m *Mux) Stats() MuxStats {
	return MuxStats{
		Received:  m.received.Load(),
		Malformed: m.malformed.Load(),
		Unclaimed: m.unclaimed.Load(),
		Overflow:  m.overflow.Load(),
		Sent:      m.sent.Load(),
	}
}

// Done is closed when the reader stops: after Close, or when the port
// failed or was closed.
func (m *Mux) Done() <-chan struct{} { return m.done }

// Err returns the error that stopped the reader, nil while it runs and
// after Close.
func (m *Mux) Err() error {
	select {
	case <-m.done:
		return m.err
	default:
		return nil
	}
}

// Close stops the reader and closes every subscription. It does not
// close the port.
func (m *Mux) Close() error {
	m.once.Do(func() {
		m.cancel()
	})
	<-m.done
	return nil
}

func (m *Mux) run(ctx context.Context) {
	defer func() {
		m.mu.Lock()
		subs := m.subs
		m.subs = nil
		m.mu.Unlock()
		for _, s := range subs {
			s.closeLocal()
		}
		close(m.done)
	}()
	buf := make([]byte, m.maxLen)
	for {
		n, err := m.port.ReadFrame(ctx, buf)
		if err != nil {
			if ctx.Err() == nil {
				m.err = err
			}
			return
		}
		m.received.Add(1)
		f, err := ethernet.Decode(buf[:n])
		if err != nil {
			m.malformed.Add(1)
			continue
		}
		m.dispatch(&f, buf[:n])
	}
}

func (m *Mux) dispatch(f *ethernet.Frame, raw []byte) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	claimed := false
	for _, s := range m.subs {
		if !s.match(f) {
			continue
		}
		claimed = true
		if !s.deliver(raw) {
			m.overflow.Add(1)
			s.overflow.Add(1)
		}
	}
	if !claimed {
		m.unclaimed.Add(1)
	}
}

// Subscribe registers a subscription for the frames match selects, with a
// queue of queueLen frames (at least 1). Frames that arrive while the
// queue is full are dropped.
func (m *Mux) Subscribe(match Matcher, queueLen int) (*Subscription, error) {
	if match == nil {
		return nil, fmt.Errorf("l2: subscribe: %w: nil matcher", profinet.ErrInvalidConfig)
	}
	if queueLen < 1 {
		queueLen = 1
	}
	s := &Subscription{
		mux:   m,
		match: match,
		bufs:  make([][]byte, queueLen+1),
		free:  make(chan int, queueLen+1),
		ready: make(chan item, queueLen+1),
		done:  make(chan struct{}),
		cur:   -1,
	}
	for i := range s.bufs {
		s.bufs[i] = make([]byte, m.maxLen)
	}
	// One buffer more than the queue holds: the one the consumer is
	// reading stays out of circulation until its next Recv.
	for i := 0; i < queueLen; i++ {
		s.free <- i
	}
	s.spare = queueLen
	select {
	case <-m.done:
		return nil, fmt.Errorf("l2: subscribe: %w", profinet.ErrClosed)
	default:
	}
	m.mu.Lock()
	m.subs = append(m.subs, s)
	m.mu.Unlock()
	return s, nil
}

func (m *Mux) remove(s *Subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, v := range m.subs {
		if v == s {
			m.subs = append(m.subs[:i:i], m.subs[i+1:]...)
			return
		}
	}
}

type item struct {
	idx, n int
}

// Subscription is a bounded queue of frames from a [Mux]. Recv must be
// called from one goroutine at a time.
type Subscription struct {
	mux   *Mux
	match Matcher

	bufs  [][]byte
	free  chan int
	ready chan item
	spare int
	cur   int

	overflow atomic.Uint64
	done     chan struct{}
	once     sync.Once
}

func (s *Subscription) deliver(raw []byte) bool {
	select {
	case <-s.done:
		return true
	default:
	}
	select {
	case idx := <-s.free:
		n := copy(s.bufs[idx], raw)
		s.ready <- item{idx: idx, n: n}
		return true
	default:
		return false
	}
}

// Recv returns the next frame. The frame and its payload are valid until
// the next call of Recv or Close. Recv returns ctx.Err() when ctx is done
// and an error matching profinet.ErrClosed after Close.
func (s *Subscription) Recv(ctx context.Context) (ethernet.Frame, error) {
	if s.cur >= 0 {
		s.free <- s.cur
		s.cur = -1
	} else if s.spare >= 0 {
		// The extra buffer joins the circulation on the first Recv.
		s.free <- s.spare
		s.spare = -1
	}
	select {
	case it := <-s.ready:
		s.cur = it.idx
		return ethernet.Decode(s.bufs[it.idx][:it.n])
	case <-ctx.Done():
		return ethernet.Frame{}, ctx.Err()
	case <-s.done:
		// Frames queued before the close are still delivered.
		select {
		case it := <-s.ready:
			s.cur = it.idx
			return ethernet.Decode(s.bufs[it.idx][:it.n])
		default:
		}
		return ethernet.Frame{}, fmt.Errorf("l2: recv: %w", profinet.ErrClosed)
	}
}

// Overflow returns the number of frames dropped because the queue was
// full.
func (s *Subscription) Overflow() uint64 { return s.overflow.Load() }

// Done is closed when the subscription is closed, by Close or because the
// Mux stopped.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Close removes the subscription from the Mux.
func (s *Subscription) Close() {
	s.mux.remove(s)
	s.closeLocal()
}

func (s *Subscription) closeLocal() {
	s.once.Do(func() { close(s.done) })
}
