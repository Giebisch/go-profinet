// SPDX-License-Identifier: MIT

package rpc

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
)

// Request is a reassembled call a [Server] hands to its [Handler].
type Request struct {
	Header Header
	Body   []byte
	// Addr is the address the request came from.
	Addr net.Addr
}

// Order returns the byte order of the request's data representation, in
// which an NDR body is encoded.
func (r *Request) Order() ByteOrder { return r.Header.Order() }

// Handler serves the calls of one interface. A returned *Fault is sent as
// a FAULT (or REJECT); any other error as a FAULT with
// StatusFaultUnspec.
type Handler interface {
	ServeRPC(ctx context.Context, req *Request) ([]byte, error)
}

// HandlerFunc adapts a function to [Handler].
type HandlerFunc func(ctx context.Context, req *Request) ([]byte, error)

// ServeRPC calls f.
func (f HandlerFunc) ServeRPC(ctx context.Context, req *Request) ([]byte, error) { return f(ctx, req) }

// ServerOptions configure a [Server].
type ServerOptions struct {
	// MaxFragmentBody bounds the body of one response packet. Zero means
	// DefaultMaxFragmentBody.
	MaxFragmentBody int
	// MaxMessage bounds a reassembled request. Zero means
	// DefaultMaxMessage.
	MaxMessage int
	// MaxActivities bounds the activities the server keeps state for.
	// Zero means 64. A new activity beyond it is rejected with
	// StatusServerTooBusy once no idle activity can be evicted.
	MaxActivities int
	// Workers bounds the handlers running at the same time; further
	// complete requests wait for a worker (and are answered WORKING to a
	// ping meanwhile). Zero means 8.
	Workers int
	// ResponseTTL is how long a response is kept to answer a
	// retransmitted request. Zero means 30 s.
	ResponseTTL time.Duration
	// HandlerTimeout bounds one handler call. Zero means 30 s.
	HandlerTimeout time.Duration
	// Logger receives diagnostics. nil logs nothing.
	Logger profinet.Logger
}

type registration struct {
	major   uint16
	handler Handler
}

type actState uint8

const (
	actReceiving actState = iota
	actExecuting
	actDone
)

type serverActivity struct {
	seq      uint32
	state    actState
	asm      *reassembly
	resp     [][]byte
	addr     net.Addr
	lastSeen time.Time
	// touched orders the activities by their last use for eviction; the
	// clock cannot, as two uses can share a timestamp on Windows.
	touched uint64
}

// ServerStats are the counters of a [Server].
type ServerStats struct {
	Requests, Retransmits, Malformed, Rejected, Busy uint64
}

// Server serves CL RPC calls on one UDP socket with at-most-once
// semantics: a retransmitted request is answered from the response cache
// instead of being executed again.
type Server struct {
	conn  net.PacketConn
	owned bool
	opts  ServerOptions
	log   profinet.Logger
	boot  uint32

	mu       sync.Mutex
	handlers map[UUID]registration
	acts     map[UUID]*serverActivity
	clock    uint64 // the last value of serverActivity.touched

	sem chan struct{}

	requests, retransmits, malformed, rejected, busy atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewServer starts a server on conn. When owned is true, Close closes
// conn. Register interfaces before requests arrive; calls of an interface
// that is not registered are rejected with StatusUnknownIf.
func NewServer(conn net.PacketConn, owned bool, opts ServerOptions) *Server {
	if opts.MaxFragmentBody <= 0 {
		opts.MaxFragmentBody = DefaultMaxFragmentBody
	}
	if opts.MaxMessage <= 0 {
		opts.MaxMessage = DefaultMaxMessage
	}
	if opts.MaxActivities <= 0 {
		opts.MaxActivities = 64
	}
	if opts.Workers <= 0 {
		opts.Workers = 8
	}
	if opts.ResponseTTL <= 0 {
		opts.ResponseTTL = 30 * time.Second
	}
	if opts.HandlerTimeout <= 0 {
		opts.HandlerTimeout = 30 * time.Second
	}
	log := opts.Logger
	if log == nil {
		log = profinet.NopLogger()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		conn:     conn,
		owned:    owned,
		opts:     opts,
		log:      log,
		boot:     uint32(time.Now().Unix()),
		handlers: make(map[UUID]registration),
		acts:     make(map[UUID]*serverActivity),
		sem:      make(chan struct{}, opts.Workers),
		ctx:      ctx,
		cancel:   cancel,
	}
	s.wg.Add(2)
	go s.readLoop()
	go s.sweep()
	return s
}

// Register serves the interface iface, major version major, with h.
func (s *Server) Register(iface UUID, major uint16, h Handler) {
	s.mu.Lock()
	s.handlers[iface] = registration{major: major, handler: h}
	s.mu.Unlock()
}

// BootTime returns the server boot time sent in every packet.
func (s *Server) BootTime() uint32 { return s.boot }

// LocalAddr returns the address of the server's socket.
func (s *Server) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// Stats returns a snapshot of the counters.
func (s *Server) Stats() ServerStats {
	return ServerStats{
		Requests:    s.requests.Load(),
		Retransmits: s.retransmits.Load(),
		Malformed:   s.malformed.Load(),
		Rejected:    s.rejected.Load(),
		Busy:        s.busy.Load(),
	}
}

// Close stops the server and waits for running handlers, whose context is
// cancelled.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		s.cancel()
		if s.owned {
			err = s.conn.Close()
		} else {
			_ = s.conn.SetReadDeadline(time.Now())
		}
		s.wg.Wait()
	})
	return err
}

func (s *Server) sweep() {
	defer s.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			now := time.Now()
			s.mu.Lock()
			for id, a := range s.acts {
				if a.state != actExecuting && now.Sub(a.lastSeen) > s.opts.ResponseTTL {
					delete(s.acts, id)
				}
			}
			s.mu.Unlock()
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Server) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, addr, err := s.conn.ReadFrom(buf)
		if err != nil {
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			s.log.Warnf("rpc: server read: %v", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		h, body, err := DecodeHeader(buf[:n])
		if err != nil {
			s.malformed.Add(1)
			s.log.Debugf("rpc: drop packet from %s: %v", addr, err)
			continue
		}
		s.packet(addr, &h, body)
	}
}

func (s *Server) packet(addr net.Addr, h *Header, body []byte) {
	switch h.Type {
	case TypeRequest:
		s.request(addr, h, body)
	case TypePing:
		s.mu.Lock()
		a := s.acts[h.Activity]
		var resp [][]byte
		t := TypeNoCall
		if a != nil && a.seq == h.SeqNum {
			s.touch(a)
			switch a.state {
			case actExecuting:
				t = TypeWorking
			case actDone:
				resp = a.resp
			case actReceiving:
			}
		}
		s.mu.Unlock()
		if resp != nil {
			s.retransmits.Add(1)
			s.write(resp, addr)
			return
		}
		s.control(addr, h, t, nil)
	case TypeAck:
		s.mu.Lock()
		if a := s.acts[h.Activity]; a != nil && a.seq == h.SeqNum && a.state == actDone {
			a.resp = nil
		}
		s.mu.Unlock()
	case TypeFack, TypeCancel:
		if h.Type == TypeCancel {
			s.control(addr, h, TypeCancelAck, nil)
		}
	case TypeResponse, TypeFault, TypeWorking, TypeNoCall, TypeReject, TypeCancelAck:
		// Client-side packets.
	}
}

func (s *Server) request(addr net.Addr, h *Header, body []byte) {
	s.mu.Lock()
	a := s.acts[h.Activity]
	if a != nil {
		switch {
		case h.SeqNum < a.seq && a.seq-h.SeqNum < 1<<31:
			s.mu.Unlock()
			return // an old call
		case h.SeqNum == a.seq:
			s.touch(a)
			switch a.state {
			case actDone:
				resp := a.resp
				s.mu.Unlock()
				s.retransmits.Add(1)
				if resp != nil {
					s.write(resp, addr)
				}
				return
			case actExecuting:
				s.mu.Unlock()
				s.retransmits.Add(1)
				return
			case actReceiving:
			}
		default:
			// A new call on this activity replaces the previous one.
			*a = serverActivity{seq: h.SeqNum, asm: newReassembly(s.opts.MaxMessage)}
		}
	} else {
		if len(s.acts) >= s.opts.MaxActivities && !s.evictLocked() {
			s.mu.Unlock()
			s.busy.Add(1)
			s.control(addr, h, TypeReject, s.status(h, StatusServerTooBusy))
			return
		}
		a = &serverActivity{seq: h.SeqNum, asm: newReassembly(s.opts.MaxMessage)}
		s.acts[h.Activity] = a
	}
	a.addr = addr
	s.touch(a)
	if h.Flags1&FlagFrag != 0 && h.Flags1&FlagNoFack == 0 {
		defer s.control(addr, h, TypeFack, fackBody(h.DRep, h.Serial))
	}
	full, ok, err := a.asm.add(h, body)
	if err != nil {
		a.state = actDone
		a.asm = nil
		a.resp = s.faultPackets(h, true, StatusProtoError)
		resp := a.resp
		s.mu.Unlock()
		s.rejected.Add(1)
		s.write(resp, addr)
		return
	}
	if !ok {
		s.mu.Unlock()
		return
	}
	a.state = actExecuting
	a.asm = nil
	reg, known := s.handlers[h.Iface]
	s.mu.Unlock()

	s.requests.Add(1)
	if !known || uint16(h.IfaceVersion) != reg.major {
		s.rejected.Add(1)
		s.finish(h, a, s.faultPackets(h, true, StatusUnknownIf))
		return
	}
	req := &Request{Header: *h, Body: full, Addr: addr}
	// At most one call per activity executes, so the goroutines waiting
	// for a worker are bounded by MaxActivities.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case s.sem <- struct{}{}:
		case <-s.ctx.Done():
			return
		}
		defer func() { <-s.sem }()
		ctx, cancel := context.WithTimeout(s.ctx, s.opts.HandlerTimeout)
		defer cancel()
		out, err := reg.handler.ServeRPC(ctx, req)
		var pkts [][]byte
		if err != nil {
			var f *Fault
			if errors.As(err, &f) {
				pkts = s.faultPackets(h, f.Reject, f.Status)
			} else {
				s.log.Warnf("rpc: handler for opnum %d from %s: %v", h.OpNum, addr, err)
				pkts = s.faultPackets(h, false, StatusFaultUnspec)
			}
		} else {
			rh := s.responseHeader(h, TypeResponse)
			pkts = packets(rh, out, s.opts.MaxFragmentBody)
		}
		s.finish(h, a, pkts)
	}()
}

// touch records a use of an activity; s.mu is held.
func (s *Server) touch(a *serverActivity) {
	a.lastSeen = time.Now()
	s.clock++
	a.touched = s.clock
}

// evictLocked removes the idle activity used longest ago.
func (s *Server) evictLocked() bool {
	var oldest UUID
	var t uint64
	found := false
	for id, a := range s.acts {
		if a.state == actExecuting {
			continue
		}
		if !found || a.touched < t {
			oldest, t, found = id, a.touched, true
		}
	}
	if found {
		delete(s.acts, oldest)
	}
	return found
}

func (s *Server) finish(h *Header, a *serverActivity, pkts [][]byte) {
	s.mu.Lock()
	addr := a.addr
	if a.seq == h.SeqNum {
		a.state = actDone
		a.resp = pkts
		s.touch(a)
	}
	s.mu.Unlock()
	if s.ctx.Err() == nil {
		s.write(pkts, addr)
	}
}

func (s *Server) responseHeader(h *Header, t PacketType) Header {
	r := *h
	r.Type = t
	r.Flags1 = 0
	r.Flags2 = 0
	r.ServerBoot = s.boot
	r.FragNum = 0
	r.BodyLen = 0
	r.Serial = 0
	return r
}

func (s *Server) status(h *Header, status uint32) []byte {
	return h.Order().AppendUint32(nil, status)
}

func (s *Server) faultPackets(h *Header, reject bool, status uint32) [][]byte {
	t := TypeFault
	if reject {
		t = TypeReject
	}
	rh := s.responseHeader(h, t)
	return packets(rh, s.status(h, status), s.opts.MaxFragmentBody)
}

func (s *Server) control(addr net.Addr, h *Header, t PacketType, body []byte) {
	rh := s.responseHeader(h, t)
	rh.BodyLen = uint16(len(body))
	if t == TypeFack {
		rh.FragNum = h.FragNum
	}
	b := rh.AppendTo(make([]byte, 0, HeaderLen+len(body)))
	s.write([][]byte{append(b, body...)}, addr)
}

func (s *Server) write(pkts [][]byte, addr net.Addr) {
	for _, p := range pkts {
		if _, err := s.conn.WriteTo(p, addr); err != nil {
			s.log.Debugf("rpc: send to %s: %v", addr, err)
			return
		}
	}
}

// Errorf returns a FAULT with status code, for handlers.
func Errorf(status uint32) error { return &Fault{Status: status} }
