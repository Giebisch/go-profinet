// SPDX-License-Identifier: MIT

package rpc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
)

// ClientOptions configure a [Client].
type ClientOptions struct {
	// Timeout bounds a call whose context has no deadline. Zero means 5 s.
	Timeout time.Duration
	// Retransmit is the interval at which an unanswered request is sent
	// again (with the same activity and sequence number, so that the
	// server executes it at most once). Zero means 1 s.
	Retransmit time.Duration
	// MaxFragmentBody bounds the body of one request packet. Zero means
	// DefaultMaxFragmentBody.
	MaxFragmentBody int
	// MaxMessage bounds a reassembled response. Zero means
	// DefaultMaxMessage.
	MaxMessage int
	// Activities is the number of calls that can be in progress at the
	// same time; each uses its own activity UUID. Further calls wait.
	// Zero means 4.
	Activities int
	// BigEndian sends requests in big-endian data representation instead
	// of the little-endian one PROFINET devices use.
	BigEndian bool
	// Logger receives diagnostics. nil logs nothing.
	Logger profinet.Logger
}

// Call is one remote procedure call.
type Call struct {
	Object       UUID
	Iface        UUID
	IfaceVersion uint32
	OpNum        uint16
	// Idempotent marks a call the server may execute more than once.
	Idempotent bool
	Body       []byte
}

// Response is the result of a call.
type Response struct {
	Header Header
	Body   []byte
}

type activity struct {
	id   UUID
	seq  uint32
	boot uint32
	peer string
}

type clientCall struct {
	act *activity
	in  chan []byte
	// done is closed when the call returns: the read loop stops waiting
	// to hand it a packet.
	done chan struct{}
}

// Client issues calls over one UDP socket. It is safe for concurrent use.
type Client struct {
	conn  net.PacketConn
	owned bool
	opts  ClientOptions
	log   profinet.Logger
	drep  [3]byte

	acts chan *activity

	mu    sync.Mutex
	calls map[UUID]*clientCall

	malformed atomic.Uint64

	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// NewClient returns a client that uses conn. When owned is true, Close
// closes conn.
func NewClient(conn net.PacketConn, owned bool, opts ClientOptions) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.Retransmit <= 0 {
		opts.Retransmit = time.Second
	}
	if opts.MaxFragmentBody <= 0 {
		opts.MaxFragmentBody = DefaultMaxFragmentBody
	}
	if opts.MaxMessage <= 0 {
		opts.MaxMessage = DefaultMaxMessage
	}
	if opts.Activities <= 0 {
		opts.Activities = 4
	}
	log := opts.Logger
	if log == nil {
		log = profinet.NopLogger()
	}
	c := &Client{
		conn:  conn,
		owned: owned,
		opts:  opts,
		log:   log,
		acts:  make(chan *activity, opts.Activities),
		calls: make(map[UUID]*clientCall),
		done:  make(chan struct{}),
	}
	c.drep = [3]byte{DRepLittleEndian, 0, 0}
	if opts.BigEndian {
		c.drep[0] = DRepBigEndian
	}
	for i := 0; i < opts.Activities; i++ {
		c.acts <- &activity{id: NewRandomUUID()}
	}
	c.wg.Add(1)
	go c.readLoop()
	return c
}

// LocalAddr returns the address of the client's socket.
func (c *Client) LocalAddr() net.Addr { return c.conn.LocalAddr() }

// Malformed returns the number of packets that could not be decoded.
func (c *Client) Malformed() uint64 { return c.malformed.Load() }

// Close stops the client; calls in progress fail with profinet.ErrClosed.
func (c *Client) Close() error {
	var err error
	c.once.Do(func() {
		close(c.done)
		if c.owned {
			err = c.conn.Close()
		} else {
			// Unblock the reader without closing a socket we do not own.
			_ = c.conn.SetReadDeadline(time.Now())
		}
		c.wg.Wait()
	})
	return err
}

func (c *Client) readLoop() {
	defer c.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, _, err := c.conn.ReadFrom(buf)
		if err != nil {
			select {
			case <-c.done:
				return
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			c.log.Warnf("rpc: client read: %v", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		h, _, err := DecodeHeader(buf[:n])
		if err != nil {
			c.malformed.Add(1)
			continue
		}
		c.mu.Lock()
		call := c.calls[h.Activity]
		c.mu.Unlock()
		if call == nil {
			continue
		}
		// The read loop waits for the call to take the packet rather than
		// drop it: a fragmented response arrives as a burst, and dropping
		// what does not fit lost the same last fragments again on every
		// retransmission of the response, so the call never completed.
		// The socket's receive buffer holds what arrives meanwhile.
		p := append([]byte(nil), buf[:n]...)
		select {
		case call.in <- p:
		case <-call.done:
		case <-c.done:
			return
		}
	}
}

// Do performs a call to the server at addr and returns its response.
//
// The request is retransmitted every Retransmit until a response, a
// fault or the deadline; the server's at-most-once semantics make the
// retransmissions safe for calls that are not idempotent. A FAULT or
// REJECT is returned as a *Fault.
func (c *Client) Do(ctx context.Context, addr net.Addr, call Call) (*Response, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opts.Timeout)
		defer cancel()
	}
	// Fail a call after Close deterministically: the select below picks
	// at random when an activity is also free.
	select {
	case <-c.done:
		return nil, fmt.Errorf("rpc: %w", profinet.ErrClosed)
	default:
	}
	var act *activity
	select {
	case act = <-c.acts:
	case <-ctx.Done():
		return nil, c.ctxErr(ctx)
	case <-c.done:
		return nil, fmt.Errorf("rpc: %w", profinet.ErrClosed)
	}
	defer func() { c.acts <- act }()
	return c.do(ctx, act, addr, call)
}

// Activity is a client activity of its own: every call on it carries the
// same activity UUID with increasing sequence numbers, and its calls run
// one at a time. PROFINET devices tie an application relationship to the
// activity of its Connect (p-net does), so a controller makes every call
// of an AR on one Activity.
type Activity struct {
	sem chan struct{}
	act activity
}

// NewActivity returns a new activity of the client.
func (c *Client) NewActivity() *Activity {
	a := &Activity{sem: make(chan struct{}, 1)}
	a.act.id = NewRandomUUID()
	return a
}

// UUID returns the activity's UUID.
func (a *Activity) UUID() UUID { return a.act.id }

// DoActivity performs a call on the activity a, waiting for a call in
// progress on it to complete.
func (c *Client) DoActivity(ctx context.Context, a *Activity, addr net.Addr, call Call) (*Response, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opts.Timeout)
		defer cancel()
	}
	select {
	case <-c.done:
		return nil, fmt.Errorf("rpc: %w", profinet.ErrClosed)
	default:
	}
	select {
	case a.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, c.ctxErr(ctx)
	case <-c.done:
		return nil, fmt.Errorf("rpc: %w", profinet.ErrClosed)
	}
	defer func() { <-a.sem }()
	return c.do(ctx, &a.act, addr, call)
}

func (c *Client) do(ctx context.Context, act *activity, addr net.Addr, call Call) (*Response, error) {
	if act.peer != addr.String() {
		act.peer = addr.String()
		act.boot = 0
	}
	act.seq++
	h := Header{
		Type:         TypeRequest,
		DRep:         c.drep,
		Object:       call.Object,
		Iface:        call.Iface,
		Activity:     act.id,
		ServerBoot:   act.boot,
		IfaceVersion: call.IfaceVersion,
		SeqNum:       act.seq,
		OpNum:        call.OpNum,
		IfaceHint:    0xFFFF,
		ActivityHint: 0xFFFF,
	}
	if call.Idempotent {
		h.Flags1 |= FlagIdempotent
	}
	pkts := packets(h, call.Body, c.opts.MaxFragmentBody)

	cc := &clientCall{act: act, in: make(chan []byte, 64), done: make(chan struct{})}
	c.mu.Lock()
	c.calls[act.id] = cc
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.calls, act.id)
		c.mu.Unlock()
		close(cc.done)
	}()

	send := func() error {
		for _, p := range pkts {
			if _, err := c.conn.WriteTo(p, addr); err != nil {
				// A Close racing with a (re)transmission closes the socket
				// under it: report the close, not the socket's error.
				select {
				case <-c.done:
					return fmt.Errorf("rpc: %w", profinet.ErrClosed)
				default:
				}
				return fmt.Errorf("rpc: send to %s: %w", addr, err)
			}
		}
		return nil
	}
	if err := send(); err != nil {
		return nil, err
	}
	re := time.NewTimer(c.opts.Retransmit)
	defer re.Stop()
	asm := newReassembly(c.opts.MaxMessage)
	for {
		select {
		case p := <-cc.in:
			rh, body, err := DecodeHeader(p)
			if err != nil || rh.SeqNum != act.seq {
				continue
			}
			switch rh.Type {
			case TypeResponse:
				if rh.Flags1&FlagFrag != 0 && rh.Flags1&FlagNoFack == 0 {
					c.sendFack(addr, &rh)
				}
				full, ok, err := asm.add(&rh, body)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
				act.boot = rh.ServerBoot
				if !call.Idempotent {
					c.sendAck(addr, &rh)
				}
				return &Response{Header: rh, Body: full}, nil
			case TypeFault, TypeReject:
				act.boot = rh.ServerBoot
				f := &Fault{Reject: rh.Type == TypeReject}
				if len(body) >= 4 {
					f.Status = rh.Order().Uint32(body)
				}
				return nil, f
			case TypeWorking:
				resetTimer(re, c.opts.Retransmit)
			case TypeNoCall:
				// The server lost the request (or never got all of it).
				if err := send(); err != nil {
					return nil, err
				}
				resetTimer(re, c.opts.Retransmit)
			default:
			}
		case <-re.C:
			if err := send(); err != nil {
				return nil, err
			}
			re.Reset(c.opts.Retransmit)
		case <-ctx.Done():
			return nil, c.ctxErr(ctx)
		case <-c.done:
			return nil, fmt.Errorf("rpc: %w", profinet.ErrClosed)
		}
	}
}

func (c *Client) ctxErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("rpc: %w: %w", profinet.ErrTimeout, ctx.Err())
	}
	return ctx.Err()
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func (c *Client) reply(addr net.Addr, rh *Header, t PacketType, body []byte) {
	h := *rh
	h.Type = t
	h.Flags1 = 0
	h.Flags2 = 0
	h.DRep = c.drep
	h.BodyLen = uint16(len(body))
	b := h.AppendTo(make([]byte, 0, HeaderLen+len(body)))
	b = append(b, body...)
	if _, err := c.conn.WriteTo(b, addr); err != nil {
		c.log.Debugf("rpc: send %s to %s: %v", t, addr, err)
	}
}

func (c *Client) sendAck(addr net.Addr, rh *Header) { c.reply(addr, rh, TypeAck, nil) }

func (c *Client) sendFack(addr net.Addr, rh *Header) {
	c.reply(addr, rh, TypeFack, fackBody(c.drep, rh.Serial))
}

// fackBody encodes the body of a FACK: version 0, window size, the
// largest transmission unit and fragment size, the serial number of the
// acknowledged fragment and an empty selective acknowledgement.
func fackBody(drep [3]byte, serial uint16) []byte {
	var o ByteOrder = binary.BigEndian
	if drep[0]&0xF0 == DRepLittleEndian {
		o = binary.LittleEndian
	}
	b := []byte{0, 0}
	b = o.AppendUint16(b, 16)
	b = o.AppendUint32(b, DefaultMaxMessage)
	b = o.AppendUint32(b, DefaultMaxFragmentBody+HeaderLen)
	b = o.AppendUint16(b, serial)
	b = o.AppendUint16(b, 0)
	return b
}
