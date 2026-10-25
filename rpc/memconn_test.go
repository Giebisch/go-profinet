// SPDX-License-Identifier: MIT

package rpc

import (
	"errors"
	"net"
	"sync"
	"time"
)

// memAddr is the address of a memConn.
type memAddr string

func (a memAddr) Network() string { return "mem" }
func (a memAddr) String() string  { return string(a) }

// memNet is an in-memory datagram network: a fast, deterministic
// net.PacketConn for the server and client tests and the fuzz target.
type memNet struct {
	mu    sync.Mutex
	conns map[memAddr]*memConn
}

func newMemNet() *memNet { return &memNet{conns: map[memAddr]*memConn{}} }

type datagram struct {
	b    []byte
	from net.Addr
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// memConn is one socket of a memNet. Datagrams to unknown addresses or
// to a full queue are dropped, as UDP would.
type memConn struct {
	net  *memNet
	addr memAddr
	in   chan datagram
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	deadline time.Time
	wake     chan struct{}
	writeErr func(p []byte) error

	// readErrs are returned by ReadFrom before any datagram.
	readErrs chan error
}

func (n *memNet) listen(addr string) *memConn {
	c := &memConn{
		net:      n,
		addr:     memAddr(addr),
		in:       make(chan datagram, 1024),
		done:     make(chan struct{}),
		wake:     make(chan struct{}),
		readErrs: make(chan error, 4),
	}
	n.mu.Lock()
	n.conns[c.addr] = c
	n.mu.Unlock()
	return c
}

func (c *memConn) setWriteErr(f func(p []byte) error) {
	c.mu.Lock()
	c.writeErr = f
	c.mu.Unlock()
}

func (c *memConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		dl, wake := c.deadline, c.wake
		c.mu.Unlock()
		select {
		case err := <-c.readErrs:
			return 0, nil, err
		default:
		}
		var t *time.Timer
		var tc <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, nil, timeoutError{}
			}
			t = time.NewTimer(d)
			tc = t.C
		}
		stop := func() {
			if t != nil {
				t.Stop()
			}
		}
		select {
		case d := <-c.in:
			stop()
			return copy(p, d.b), d.from, nil
		case err := <-c.readErrs:
			stop()
			return 0, nil, err
		case <-c.done:
			stop()
			return 0, nil, net.ErrClosed
		case <-tc:
			return 0, nil, timeoutError{}
		case <-wake:
			stop()
		}
	}
}

func (c *memConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	c.mu.Lock()
	we := c.writeErr
	c.mu.Unlock()
	if we != nil {
		if err := we(p); err != nil {
			return 0, err
		}
	}
	c.net.mu.Lock()
	dst := c.net.conns[memAddr(addr.String())]
	c.net.mu.Unlock()
	if dst == nil {
		return len(p), nil
	}
	select {
	case dst.in <- datagram{b: append([]byte(nil), p...), from: c.addr}:
	default:
	}
	return len(p), nil
}

func (c *memConn) Close() error {
	err := errors.New("memconn: already closed")
	c.once.Do(func() {
		err = nil
		close(c.done)
		c.net.mu.Lock()
		delete(c.net.conns, c.addr)
		c.net.mu.Unlock()
	})
	return err
}

func (c *memConn) LocalAddr() net.Addr { return c.addr }

func (c *memConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

func (c *memConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	close(c.wake)
	c.wake = make(chan struct{})
	c.mu.Unlock()
	return nil
}

func (c *memConn) SetWriteDeadline(time.Time) error { return nil }

// recv reads one datagram within timeout and decodes its header.
func (c *memConn) recv(timeout time.Duration) (Header, []byte, net.Addr, bool) {
	c.mu.Lock()
	c.deadline = time.Time{}
	c.mu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		select {
		case d := <-c.in:
			h, body, err := DecodeHeader(d.b)
			if err != nil {
				continue
			}
			return h, append([]byte(nil), body...), d.from, true
		case <-t.C:
			return Header{}, nil, nil, false
		}
	}
}

// sendPkt encodes h with body and sends it to addr.
func (c *memConn) sendPkt(addr net.Addr, h Header, body []byte) {
	h.BodyLen = uint16(len(body))
	if h.DRep == ([3]byte{}) {
		h.DRep = [3]byte{DRepLittleEndian}
	}
	_, _ = c.WriteTo(append(h.AppendTo(nil), body...), addr)
}
