// SPDX-License-Identifier: MIT

package rpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
)

type recLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recLogger) add(level, f string, a []any) {
	l.mu.Lock()
	l.msgs = append(l.msgs, level+" "+fmt.Sprintf(f, a...))
	l.mu.Unlock()
}
func (l *recLogger) Debugf(f string, a ...any) { l.add("DEBUG", f, a) }
func (l *recLogger) Infof(f string, a ...any)  { l.add("INFO", f, a) }
func (l *recLogger) Warnf(f string, a ...any)  { l.add("WARN", f, a) }
func (l *recLogger) Errorf(f string, a ...any) { l.add("ERROR", f, a) }

func (l *recLogger) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPacketTypeNames(t *testing.T) {
	want := []string{"request", "ping", "response", "fault", "working", "nocall", "reject", "ack", "cancel", "fack", "cancel_ack"}
	for i, w := range want {
		if got := PacketType(i).String(); got != w {
			t.Errorf("type %d: %q", i, got)
		}
	}
	if got := PacketType(11).String(); got != "ptype(11)" {
		t.Errorf("type 11: %q", got)
	}
}

func TestHeaderByteOrders(t *testing.T) {
	obj := MustParseUUID("01020304-0506-0708-090a-0b0c0d0e0f10")
	for _, drep := range [][3]byte{{DRepLittleEndian, 0, 0}, {DRepBigEndian, 0, 0}} {
		h := Header{Type: TypeResponse, Flags1: FlagFrag | FlagLastFrag, Flags2: Flag2CancelPending, DRep: drep, Serial: 0xA1B2,
			Object: obj, Iface: testIface, Activity: NewRandomUUID(), ServerBoot: 0x01020304, IfaceVersion: 0x00010002,
			SeqNum: 0x0A0B0C0D, OpNum: 0x0102, IfaceHint: 0xFFFF, ActivityHint: 0x1234, BodyLen: 3, FragNum: 0x0506}
		b := append(h.AppendTo(nil), 1, 2, 3, 4)
		if len(b) != HeaderLen+4 || b[0] != Version || b[7] != 0xA1 || b[79] != 0xB2 {
			t.Fatalf("% x", b[:8])
		}
		var o binary.ByteOrder = binary.BigEndian
		if drep[0] == DRepLittleEndian {
			o = binary.LittleEndian
		}
		if h.Order() != o || o.Uint32(b[64:]) != h.SeqNum || o.Uint16(b[68:]) != h.OpNum || o.Uint32(b[8:]) != 0x01020304 {
			t.Fatalf("order of drep %x", drep[0])
		}
		g, body, err := DecodeHeader(b)
		if err != nil || g != h || !bytes.Equal(body, []byte{1, 2, 3}) {
			t.Fatalf("decode %+v %v", g, err)
		}
	}
	h := Header{DRep: [3]byte{DRepLittleEndian}, AuthProto: 1}
	if _, _, err := DecodeHeader(h.AppendTo(nil)); !errors.Is(err, profinet.ErrUnsupported) {
		t.Fatalf("auth: %v", err)
	}
	h = Header{Type: TypeCancelAck + 1}
	if _, _, err := DecodeHeader(h.AppendTo(nil)); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("type: %v", err)
	}
}

func TestFaultErrors(t *testing.T) {
	cases := []struct {
		f    Fault
		text string
	}{
		{Fault{Status: StatusOpRangeError}, "rpc: fault 0x1C010002 (operation number out of range)"},
		{Fault{Reject: true, Status: StatusUnknownIf}, "rpc: reject 0x1C010003 (unknown interface)"},
		{Fault{Status: StatusWrongBootTime}, "rpc: fault 0x1C010006 (wrong boot time)"},
		{Fault{Reject: true, Status: StatusProtoError}, "rpc: reject 0x1C01000B (protocol error)"},
		{Fault{Status: StatusServerTooBusy}, "rpc: fault 0x1C010014 (server too busy)"},
		{Fault{Status: StatusFaultUnspec}, "rpc: fault 0x1C000012 (unspecified fault)"},
		{Fault{Status: StatusYouCrashed}, "rpc: fault 0x1C010009"},
	}
	for _, c := range cases {
		if got := c.f.Error(); got != c.text {
			t.Errorf("%q, want %q", got, c.text)
		}
		if !errors.Is(&c.f, profinet.ErrRejected) || errors.Is(&c.f, profinet.ErrTimeout) {
			t.Errorf("%v: Is", c.f)
		}
	}
	for _, st := range []uint32{StatusUnknownIf, StatusOpRangeError, StatusUnsupportedTyp} {
		if !errors.Is(&Fault{Status: st}, profinet.ErrUnsupported) {
			t.Errorf("0x%08X does not match ErrUnsupported", st)
		}
	}
	if errors.Is(&Fault{Status: StatusProtoError}, profinet.ErrUnsupported) || errors.Is(&Fault{Status: StatusProtoError}, profinet.ErrResourceExhausted) {
		t.Error("protocol error matches too much")
	}
	err := Errorf(StatusOutArgsTooBig)
	var f *Fault
	if !errors.As(err, &f) || f.Reject || f.Status != StatusOutArgsTooBig {
		t.Fatalf("Errorf %v", err)
	}
}

func TestUUIDText(t *testing.T) {
	u := NewRandomUUID()
	b, err := u.MarshalText()
	if err != nil || string(b) != u.String() {
		t.Fatal(err)
	}
	var v UUID
	if err := v.UnmarshalText([]byte("zz")); !errors.Is(err, profinet.ErrInvalidConfig) || !v.IsNil() {
		t.Fatalf("unmarshal %v", err)
	}
	if _, err := ParseUUID("gea00001-6c97-11d1-8271-00a02442df7d"); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("bad hex %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustParseUUID did not panic")
		}
	}()
	MustParseUUID("x")
}

func TestPacketsDefaults(t *testing.T) {
	ps := packets(Header{DRep: [3]byte{DRepLittleEndian}}, make([]byte, DefaultMaxFragmentBody+1), 0)
	if len(ps) != 2 {
		t.Fatalf("%d packets", len(ps))
	}
	h0, b0, _ := DecodeHeader(ps[0])
	h1, b1, _ := DecodeHeader(ps[1])
	if len(b0) != DefaultMaxFragmentBody || len(b1) != 1 || h0.Flags1 != FlagFrag|FlagNoFack || h1.Flags1 != FlagFrag|FlagNoFack|FlagLastFrag || h1.FragNum != 1 {
		t.Fatalf("headers %+v %+v", h0, h1)
	}
	// An empty body is one packet without fragment flags.
	ps = packets(Header{}, nil, 10)
	if h, _, _ := DecodeHeader(ps[0]); len(ps) != 1 || h.Flags1 != 0 {
		t.Fatalf("empty: %d packets", len(ps))
	}
	// A last fragment below fragments already received is an error.
	r := newReassembly(0)
	_, _, _ = r.add(&Header{Flags1: FlagFrag, FragNum: 3}, []byte{1})
	if _, _, err := r.add(&Header{Flags1: FlagFrag | FlagLastFrag, FragNum: 1}, nil); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
	// An empty last fragment completes a message.
	r = newReassembly(0)
	_, _, _ = r.add(&Header{Flags1: FlagFrag}, []byte{1})
	out, ok, err := r.add(&Header{Flags1: FlagFrag | FlagLastFrag, FragNum: 1}, nil)
	if !ok || err != nil || !bytes.Equal(out, []byte{1}) {
		t.Fatalf("%v %v %v", out, ok, err)
	}
}

// clientEnv is a client on a memNet and the peer socket that plays the
// server.
func clientEnv(t *testing.T, opts ClientOptions) (*Client, *memConn, *memConn) {
	t.Helper()
	n := newMemNet()
	cc := n.listen("client")
	peer := n.listen("peer")
	t.Cleanup(func() { _ = peer.Close() })
	c := NewClient(cc, true, opts)
	t.Cleanup(func() { _ = c.Close() })
	return c, cc, peer
}

type result struct {
	r   *Response
	err error
}

func doAsync(c *Client, ctx context.Context, addr net.Addr, call Call) chan result {
	ch := make(chan result, 1)
	go func() {
		r, err := c.Do(ctx, addr, call)
		ch <- result{r, err}
	}()
	return ch
}

func wait(t *testing.T, ch chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("call did not return")
		return result{}
	}
}

func expect(t *testing.T, peer *memConn, typ PacketType) (Header, []byte, net.Addr) {
	t.Helper()
	h, body, from, ok := peer.recv(5 * time.Second)
	if !ok {
		t.Fatalf("no %s packet", typ)
	}
	if h.Type != typ {
		t.Fatalf("got %s, want %s", h.Type, typ)
	}
	return h, body, from
}

func respond(req Header, t PacketType) Header {
	h := req
	h.Type = t
	h.Flags1 = 0
	h.ServerBoot = 77
	return h
}

func TestClientFack(t *testing.T) {
	// One activity, so that the second call reuses what the first learned.
	c, _, peer := clientEnv(t, ClientOptions{Retransmit: time.Minute, Activities: 1})
	ch := doAsync(c, context.Background(), peer.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, Body: []byte("x")})
	req, _, from := expect(t, peer, TypeRequest)
	// Unrelated packets: another sequence number, a malformed response
	// body, a ping, and an unknown activity.
	other := respond(req, TypeResponse)
	other.SeqNum++
	peer.sendPkt(from, other, []byte("old"))
	peer.sendPkt(from, respond(req, TypePing), nil)
	stray := respond(req, TypeResponse)
	stray.Activity = NewRandomUUID()
	peer.sendPkt(from, stray, nil)
	_, _ = peer.WriteTo([]byte{1, 2, 3}, from)
	// Two fragments that want a FACK each.
	f0 := respond(req, TypeResponse)
	f0.Flags1 = FlagFrag
	f0.Serial = 0x0102
	peer.sendPkt(from, f0, []byte("ab"))
	fh, fb, _ := expect(t, peer, TypeFack)
	if fh.SeqNum != req.SeqNum || fh.Activity != req.Activity || len(fb) != 16 || binary.LittleEndian.Uint16(fb[12:]) != 0x0102 {
		t.Fatalf("FACK %+v % x", fh, fb)
	}
	f1 := respond(req, TypeResponse)
	f1.Flags1 = FlagFrag | FlagLastFrag
	f1.FragNum = 1
	peer.sendPkt(from, f1, []byte("cd"))
	expect(t, peer, TypeFack)
	ack, _, _ := expect(t, peer, TypeAck)
	if ack.SeqNum != req.SeqNum {
		t.Fatalf("ACK %+v", ack)
	}
	r := wait(t, ch)
	if r.err != nil || string(r.r.Body) != "abcd" || r.r.Header.ServerBoot != 77 {
		t.Fatalf("result %+v %v", r.r, r.err)
	}
	if c.Malformed() != 1 {
		t.Fatalf("malformed %d", c.Malformed())
	}
	// The next call carries the server boot time it learned.
	ch = doAsync(c, context.Background(), peer.LocalAddr(), Call{Iface: testIface, Idempotent: true})
	req, _, from = expect(t, peer, TypeRequest)
	if req.ServerBoot != 77 || req.Flags1&FlagIdempotent == 0 {
		t.Fatalf("request %+v", req)
	}
	peer.sendPkt(from, respond(req, TypeResponse), nil)
	if r := wait(t, ch); r.err != nil {
		t.Fatal(r.err)
	}
	// An idempotent call is not acknowledged.
	if h, _, _, ok := peer.recv(50 * time.Millisecond); ok {
		t.Fatalf("unexpected %s", h.Type)
	}
}

func TestClientNoCallAndWorking(t *testing.T) {
	c, _, peer := clientEnv(t, ClientOptions{Retransmit: time.Minute, Timeout: 5 * time.Second})
	ch := doAsync(c, context.Background(), peer.LocalAddr(), Call{Iface: testIface})
	req, _, from := expect(t, peer, TypeRequest)
	// NOCALL makes the client resend at once, long before Retransmit.
	peer.sendPkt(from, respond(req, TypeNoCall), nil)
	again, _, _ := expect(t, peer, TypeRequest)
	if again.SeqNum != req.SeqNum || again.Activity != req.Activity {
		t.Fatalf("resend %+v", again)
	}
	// A FAULT with a short body has status 0.
	peer.sendPkt(from, respond(req, TypeFault), []byte{1})
	r := wait(t, ch)
	var f *Fault
	if !errors.As(r.err, &f) || f.Status != 0 || f.Reject {
		t.Fatalf("err %v", r.err)
	}

	// WORKING postpones the retransmission.
	c2, _, peer2 := clientEnv(t, ClientOptions{Retransmit: 400 * time.Millisecond})
	ch = doAsync(c2, context.Background(), peer2.LocalAddr(), Call{Iface: testIface})
	req, _, from = expect(t, peer2, TypeRequest)
	requests := 1
	end := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(end) {
		peer2.sendPkt(from, respond(req, TypeWorking), nil)
		if h, _, _, ok := peer2.recv(20 * time.Millisecond); ok && h.Type == TypeRequest {
			requests++
		}
	}
	peer2.sendPkt(from, respond(req, TypeResponse), []byte("done"))
	if r := wait(t, ch); r.err != nil || string(r.r.Body) != "done" {
		t.Fatalf("result %v", r.err)
	}
	if requests != 1 {
		t.Fatalf("%d requests sent while the server was working", requests)
	}
	expect(t, peer2, TypeAck)
	// Without WORKING the request is retransmitted.
	ch = doAsync(c2, context.Background(), peer2.LocalAddr(), Call{Iface: testIface})
	req, _, from = expect(t, peer2, TypeRequest)
	expect(t, peer2, TypeRequest)
	peer2.sendPkt(from, respond(req, TypeReject), binary.LittleEndian.AppendUint32(nil, StatusWrongBootTime))
	if r := wait(t, ch); !errors.As(r.err, &f) || !f.Reject || f.Status != StatusWrongBootTime {
		t.Fatalf("err %v", r.err)
	}
}

func TestClientReassemblyError(t *testing.T) {
	c, _, peer := clientEnv(t, ClientOptions{Retransmit: time.Minute})
	ch := doAsync(c, context.Background(), peer.LocalAddr(), Call{Iface: testIface})
	req, _, from := expect(t, peer, TypeRequest)
	h := respond(req, TypeResponse)
	h.Flags1 = FlagFrag | FlagNoFack
	h.FragNum = MaxFragments
	peer.sendPkt(from, h, []byte{1})
	if r := wait(t, ch); !errors.Is(r.err, profinet.ErrResourceExhausted) {
		t.Fatalf("err %v", r.err)
	}
}

func TestClientSendErrors(t *testing.T) {
	log := &recLogger{}
	c, cc, peer := clientEnv(t, ClientOptions{Retransmit: 30 * time.Millisecond, Logger: log})
	boom := errors.New("boom")
	cc.setWriteErr(func([]byte) error { return boom })
	if _, err := c.Do(context.Background(), peer.LocalAddr(), Call{}); !errors.Is(err, boom) {
		t.Fatalf("first send: %v", err)
	}
	// The retransmission fails.
	var n atomic.Int32
	cc.setWriteErr(func([]byte) error {
		if n.Add(1) > 1 {
			return boom
		}
		return nil
	})
	if _, err := c.Do(context.Background(), peer.LocalAddr(), Call{}); !errors.Is(err, boom) {
		t.Fatalf("retransmit: %v", err)
	}
	// The resend after NOCALL fails; so does the ACK of a response, which
	// is only logged.
	c2, cc2, peer2 := clientEnv(t, ClientOptions{Retransmit: time.Minute, Logger: log})
	ch := doAsync(c2, context.Background(), peer2.LocalAddr(), Call{})
	req, _, from := expect(t, peer2, TypeRequest)
	cc2.setWriteErr(func([]byte) error { return boom })
	peer2.sendPkt(from, respond(req, TypeNoCall), nil)
	if r := wait(t, ch); !errors.Is(r.err, boom) {
		t.Fatalf("nocall resend: %v", r.err)
	}
	cc2.setWriteErr(nil)
	ch = doAsync(c2, context.Background(), peer2.LocalAddr(), Call{})
	req, _, from = expect(t, peer2, TypeRequest)
	cc2.setWriteErr(func([]byte) error { return boom })
	peer2.sendPkt(from, respond(req, TypeResponse), nil)
	if r := wait(t, ch); r.err != nil {
		t.Fatalf("response: %v", r.err)
	}
	if !log.has("DEBUG rpc: send ack") {
		t.Fatalf("log %v", log.msgs)
	}
}

// A Close that closes the socket while a call retransmits makes the call
// fail with ErrClosed, not with the socket's error (regression).
func TestClientCloseDuringRetransmit(t *testing.T) {
	c, cc, peer := clientEnv(t, ClientOptions{Retransmit: 10 * time.Millisecond})
	var writes atomic.Int32
	cc.setWriteErr(func([]byte) error {
		if writes.Add(1) == 2 {
			_ = c.Close()
			return net.ErrClosed
		}
		return nil
	})
	if _, err := c.Do(context.Background(), peer.LocalAddr(), Call{}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
}

func TestClientActivities(t *testing.T) {
	c, _, peer := clientEnv(t, ClientOptions{Activities: 1, Retransmit: time.Minute})
	ch := doAsync(c, context.Background(), peer.LocalAddr(), Call{})
	req, _, from := expect(t, peer, TypeRequest)
	// The only activity is busy: the next call waits for it until its
	// deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Do(ctx, peer.LocalAddr(), Call{}); !errors.Is(err, profinet.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := c.Do(ctx2, peer.LocalAddr(), Call{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	waiting := doAsync(c, context.Background(), peer.LocalAddr(), Call{})
	peer.sendPkt(from, respond(req, TypeResponse), nil)
	if r := wait(t, ch); r.err != nil {
		t.Fatal(r.err)
	}
	expect(t, peer, TypeAck)
	// The waiting call gets the activity with the next sequence number.
	req2, _, from := expect(t, peer, TypeRequest)
	if req2.Activity != req.Activity || req2.SeqNum != req.SeqNum+1 || req2.ServerBoot != 77 {
		t.Fatalf("request %+v", req2)
	}
	peer.sendPkt(from, respond(req2, TypeResponse), nil)
	if r := wait(t, waiting); r.err != nil {
		t.Fatal(r.err)
	}
	// Another server: the activity forgets the boot time.
	n2 := peer.net.listen("peer2")
	defer func() { _ = n2.Close() }()
	ch = doAsync(c, context.Background(), n2.LocalAddr(), Call{})
	req3, _, _ := expect(t, n2, TypeRequest)
	if req3.ServerBoot != 0 {
		t.Fatalf("boot %d for a new server", req3.ServerBoot)
	}
	// A call waiting for an activity fails when the client is closed.
	waiting = doAsync(c, context.Background(), n2.LocalAddr(), Call{})
	time.Sleep(20 * time.Millisecond)
	_ = c.Close()
	for _, w := range []chan result{ch, waiting} {
		if r := wait(t, w); !errors.Is(r.err, profinet.ErrClosed) {
			t.Fatalf("err %v", r.err)
		}
	}
	// A call after Close fails with ErrClosed every time, although an
	// activity is free (regression: the select picked at random and the
	// call failed with the socket's error).
	for i := 0; i < 50; i++ {
		if _, err := c.Do(context.Background(), n2.LocalAddr(), Call{}); !errors.Is(err, profinet.ErrClosed) {
			t.Fatalf("after close: %v", err)
		}
	}
}

func TestClientConnOwnership(t *testing.T) {
	n := newMemNet()
	cc := n.listen("c")
	c := NewClient(cc, false, ClientOptions{})
	if c.LocalAddr() != cc.LocalAddr() {
		t.Fatal("LocalAddr")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// The socket stays open.
	if _, err := cc.WriteTo([]byte{1}, memAddr("x")); err != nil {
		t.Fatalf("socket closed: %v", err)
	}
	_ = cc.Close()
	// An owned socket is closed, and its error reported.
	cc = n.listen("d")
	c = NewClient(cc, true, ClientOptions{})
	_ = cc.Close()
	if err := c.Close(); err == nil {
		t.Fatal("double close not reported")
	}
}

func TestClientReadErrors(t *testing.T) {
	log := &recLogger{}
	n := newMemNet()
	cc := n.listen("c")
	c := NewClient(cc, false, ClientOptions{Logger: log})
	cc.readErrs <- timeoutError{}
	cc.readErrs <- errors.New("transient")
	waitFor(t, "warning", func() bool { return log.has("WARN rpc: client read: transient") })
	// Closing the socket under the client stops its reader.
	_ = cc.Close()
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not stop")
	}
	_ = c.Close()
}

// memServer is a server with the echo handler on a memNet.
func memServer(t *testing.T, opts ServerOptions, h HandlerFunc) (*Server, *memConn, *memConn) {
	t.Helper()
	n := newMemNet()
	sc := n.listen("server")
	peer := n.listen("peer")
	t.Cleanup(func() { _ = peer.Close() })
	s := NewServer(sc, true, opts)
	if h == nil {
		h = func(ctx context.Context, r *Request) ([]byte, error) {
			if r.Order() != r.Header.Order() {
				return nil, errors.New("order")
			}
			return append([]byte("echo:"), r.Body...), nil
		}
	}
	s.Register(testIface, 1, h)
	t.Cleanup(func() { _ = s.Close() })
	return s, sc, peer
}

func reqHeader(act UUID, seq uint32) Header {
	return Header{Type: TypeRequest, DRep: [3]byte{DRepLittleEndian}, Iface: testIface, IfaceVersion: 1, Activity: act, SeqNum: seq}
}

func noPacket(t *testing.T, peer *memConn) {
	t.Helper()
	if h, _, _, ok := peer.recv(50 * time.Millisecond); ok {
		t.Fatalf("unexpected %s", h.Type)
	}
}

func TestServerSequences(t *testing.T) {
	s, _, peer := memServer(t, ServerOptions{}, nil)
	addr := s.LocalAddr()
	act := NewRandomUUID()
	peer.sendPkt(addr, reqHeader(act, 5), []byte("a"))
	if _, b, _ := expect(t, peer, TypeResponse); string(b) != "echo:a" {
		t.Fatalf("body %q", b)
	}
	// A retransmission is answered from the cache.
	peer.sendPkt(addr, reqHeader(act, 5), []byte("a"))
	expect(t, peer, TypeResponse)
	// A PING of a done call returns the response.
	ping := reqHeader(act, 5)
	ping.Type = TypePing
	peer.sendPkt(addr, ping, nil)
	expect(t, peer, TypeResponse)
	// After the ACK the cached response is gone: a retransmission gets
	// nothing, a PING a NOCALL.
	ack := reqHeader(act, 5)
	ack.Type = TypeAck
	peer.sendPkt(addr, ack, nil)
	peer.sendPkt(addr, reqHeader(act, 5), []byte("a"))
	noPacket(t, peer)
	peer.sendPkt(addr, ping, nil)
	expect(t, peer, TypeNoCall)
	// An older sequence number is ignored.
	peer.sendPkt(addr, reqHeader(act, 4), []byte("old"))
	noPacket(t, peer)
	// A newer one replaces the call.
	peer.sendPkt(addr, reqHeader(act, 6), []byte("b"))
	if h, b, _ := expect(t, peer, TypeResponse); string(b) != "echo:b" || h.SeqNum != 6 || h.ServerBoot != s.BootTime() {
		t.Fatalf("response %+v %q", h, b)
	}
	// Sequence numbers wrap: 0xFFFFFFF0 then 1 is a new call.
	peer.sendPkt(addr, reqHeader(act, 0xFFFFFFF0), []byte("c"))
	expect(t, peer, TypeResponse)
	peer.sendPkt(addr, reqHeader(act, 1), []byte("d"))
	if h, b, _ := expect(t, peer, TypeResponse); string(b) != "echo:d" || h.SeqNum != 1 {
		t.Fatalf("after wrap %+v %q", h, b)
	}
	// A PING of a call still receiving fragments gets NOCALL; FACKs and
	// client-side packets are ignored.
	frag := reqHeader(act, 2)
	frag.Flags1 = FlagFrag | FlagNoFack
	peer.sendPkt(addr, frag, []byte("x"))
	ping.SeqNum = 2
	peer.sendPkt(addr, ping, nil)
	expect(t, peer, TypeNoCall)
	for _, typ := range []PacketType{TypeFack, TypeResponse, TypeFault, TypeWorking, TypeNoCall, TypeReject, TypeCancelAck} {
		h := reqHeader(act, 2)
		h.Type = typ
		peer.sendPkt(addr, h, nil)
	}
	noPacket(t, peer)
	st := s.Stats()
	if st.Requests != 4 || st.Retransmits < 3 || st.Rejected != 0 || st.Busy != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestServerReassemblyReject(t *testing.T) {
	s, _, peer := memServer(t, ServerOptions{MaxMessage: 4}, nil)
	act := NewRandomUUID()
	h := reqHeader(act, 1)
	h.Flags1 = FlagFrag | FlagNoFack
	peer.sendPkt(s.LocalAddr(), h, []byte("12345"))
	rh, body, _ := expect(t, peer, TypeReject)
	if binary.LittleEndian.Uint32(body) != StatusProtoError || rh.SeqNum != 1 {
		t.Fatalf("reject %+v % x", rh, body)
	}
	// The retransmission gets the cached REJECT.
	peer.sendPkt(s.LocalAddr(), h, []byte("12345"))
	expect(t, peer, TypeReject)
	if st := s.Stats(); st.Rejected != 1 || st.Retransmits != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestServerEviction(t *testing.T) {
	s, _, peer := memServer(t, ServerOptions{MaxActivities: 2}, nil)
	a1, a2, a3 := NewRandomUUID(), NewRandomUUID(), NewRandomUUID()
	for _, a := range []UUID{a1, a2, a3} {
		peer.sendPkt(s.LocalAddr(), reqHeader(a, 1), nil)
		expect(t, peer, TypeResponse)
	}
	// a1, seen longest ago, was evicted for a3.
	s.mu.Lock()
	_, has1 := s.acts[a1]
	_, has2 := s.acts[a2]
	_, has3 := s.acts[a3]
	s.mu.Unlock()
	if has1 || !has2 || !has3 {
		t.Fatalf("activities %v %v %v", has1, has2, has3)
	}
	if s.Stats().Busy != 0 {
		t.Fatalf("stats %+v", s.Stats())
	}
}

func TestServerSweep(t *testing.T) {
	s, _, peer := memServer(t, ServerOptions{ResponseTTL: 10 * time.Millisecond}, nil)
	peer.sendPkt(s.LocalAddr(), reqHeader(NewRandomUUID(), 1), nil)
	expect(t, peer, TypeResponse)
	waitFor(t, "sweep", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.acts) == 0
	})
}

func TestServerHandlerTimeoutAndClose(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var returned atomic.Int32
	s, sc, peer := memServer(t, ServerOptions{HandlerTimeout: 50 * time.Millisecond, Workers: 1}, func(ctx context.Context, r *Request) ([]byte, error) {
		defer returned.Add(1)
		started <- struct{}{}
		if r.Header.OpNum == 1 {
			<-release
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	// The handler's context ends after HandlerTimeout: a FAULT.
	peer.sendPkt(s.LocalAddr(), reqHeader(NewRandomUUID(), 1), nil)
	_, body, _ := expect(t, peer, TypeFault)
	if binary.LittleEndian.Uint32(body) != StatusFaultUnspec {
		t.Fatalf("status % x", body)
	}
	// A retransmission while the call executes is counted, not answered;
	// a second call waits for the only worker.
	busy := NewRandomUUID()
	h := reqHeader(busy, 1)
	h.OpNum = 1
	peer.sendPkt(s.LocalAddr(), h, nil)
	<-started
	<-started
	peer.sendPkt(s.LocalAddr(), h, nil)
	peer.sendPkt(s.LocalAddr(), reqHeader(NewRandomUUID(), 1), nil)
	waitFor(t, "retransmit", func() bool { return s.Stats().Retransmits == 1 && s.Stats().Requests == 3 })
	ping := h
	ping.Type = TypePing
	peer.sendPkt(s.LocalAddr(), ping, nil)
	expect(t, peer, TypeWorking)
	// Close cancels the running handler and the waiting call; nothing is
	// sent after it.
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(release)
	}()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if returned.Load() != 2 {
		t.Fatalf("%d handlers returned", returned.Load())
	}
	noPacket(t, peer)
	if err := sc.Close(); err == nil {
		t.Fatal("server did not close its socket")
	}
}

func TestServerConnAndErrors(t *testing.T) {
	log := &recLogger{}
	n := newMemNet()
	sc := n.listen("s")
	peer := n.listen("p")
	defer func() { _ = peer.Close() }()
	s := NewServer(sc, false, ServerOptions{Logger: log})
	s.Register(testIface, 1, HandlerFunc(func(context.Context, *Request) ([]byte, error) { return []byte("ok"), nil }))
	sc.readErrs <- timeoutError{}
	sc.readErrs <- errors.New("transient")
	waitFor(t, "warning", func() bool { return log.has("WARN rpc: server read: transient") })
	// A failing write is logged.
	sc.setWriteErr(func([]byte) error { return errors.New("no route") })
	peer.sendPkt(s.LocalAddr(), reqHeader(NewRandomUUID(), 1), nil)
	waitFor(t, "write error", func() bool { return log.has("DEBUG rpc: send to p: no route") })
	sc.setWriteErr(nil)
	// Malformed packets are counted and logged.
	_, _ = peer.WriteTo(make([]byte, 10), s.LocalAddr())
	waitFor(t, "malformed", func() bool { return s.Stats().Malformed == 1 })
	if !log.has("DEBUG rpc: drop packet from p") {
		t.Fatalf("log %v", log.msgs)
	}
	// The socket is not closed by a server that does not own it.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("socket was closed: %v", err)
	}
	// A server whose socket is closed under it stops reading.
	sc2 := n.listen("s2")
	s2 := NewServer(sc2, false, ServerOptions{})
	_ = sc2.Close()
	_ = s2.Close()
}

// FuzzServer feeds arbitrary datagrams to a server and checks that it
// keeps answering.
func FuzzServer(f *testing.F) {
	le := [3]byte{DRepLittleEndian}
	act := MustParseUUID("11111111-2222-3333-4444-555555555555")
	seed := func(h Header, body []byte) {
		h.DRep = le
		h.BodyLen = uint16(len(body))
		f.Add(append(h.AppendTo(nil), body...))
	}
	seed(Header{Type: TypeRequest, Iface: testIface, IfaceVersion: 1, Activity: act, SeqNum: 1}, []byte("x"))
	seed(Header{Type: TypeRequest, Iface: testIface, IfaceVersion: 1, Activity: act, SeqNum: 2, Flags1: FlagFrag}, []byte("frag"))
	seed(Header{Type: TypeRequest, Iface: testIface, IfaceVersion: 1, Activity: act, SeqNum: 2, Flags1: FlagFrag | FlagLastFrag, FragNum: 1}, nil)
	seed(Header{Type: TypeRequest, Iface: NewRandomUUID(), Activity: act, SeqNum: 3}, nil)
	seed(Header{Type: TypePing, Activity: act, SeqNum: 1}, nil)
	seed(Header{Type: TypeAck, Activity: act, SeqNum: 1}, nil)
	seed(Header{Type: TypeCancel, Activity: act}, nil)
	f.Add([]byte{4, 0, 0})

	n := newMemNet()
	sc := n.listen("server")
	peer := n.listen("fuzzer")
	s := NewServer(sc, true, ServerOptions{MaxActivities: 8, Workers: 2, MaxMessage: 4096})
	s.Register(testIface, 1, HandlerFunc(func(_ context.Context, r *Request) ([]byte, error) {
		if len(r.Body) > 0 && r.Body[0] == 0xFF {
			return nil, &Fault{Reject: true, Status: StatusOpRangeError}
		}
		return r.Body, nil
	}))
	f.Cleanup(func() {
		_ = s.Close()
		_ = peer.Close()
	})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = peer.WriteTo(b, s.LocalAddr())
		// The server handles packets in order: a PING on a fresh activity
		// is answered with NOCALL once b was processed.
		probe := Header{Type: TypePing, DRep: le, Activity: NewRandomUUID(), SeqNum: 1}
		_, _ = peer.WriteTo(probe.AppendTo(nil), s.LocalAddr())
		for {
			h, _, _, ok := peer.recv(5 * time.Second)
			if !ok {
				t.Fatal("server stopped answering")
			}
			if h.Activity == probe.Activity {
				if h.Type != TypeNoCall {
					t.Fatalf("probe answered with %s", h.Type)
				}
				return
			}
		}
	})
}

func BenchmarkHeaderAppend(b *testing.B) {
	h := Header{Type: TypeRequest, DRep: [3]byte{DRepLittleEndian}, Object: NewRandomUUID(), Iface: testIface, Activity: NewRandomUUID(), SeqNum: 1, BodyLen: 100}
	buf := make([]byte, 0, HeaderLen)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf = h.AppendTo(buf[:0])
	}
}

func BenchmarkDecodeHeader(b *testing.B) {
	h := Header{Type: TypeRequest, DRep: [3]byte{DRepLittleEndian}, Object: NewRandomUUID(), Iface: testIface, Activity: NewRandomUUID(), SeqNum: 1, BodyLen: 100}
	buf := append(h.AppendTo(nil), make([]byte, 100)...)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := DecodeHeader(buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoopbackCall(b *testing.B) {
	sc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	s := NewServer(sc, true, ServerOptions{})
	defer func() { _ = s.Close() }()
	s.Register(testIface, 1, HandlerFunc(func(_ context.Context, r *Request) ([]byte, error) { return r.Body, nil }))
	cc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	c := NewClient(cc, true, ClientOptions{})
	defer func() { _ = c.Close() }()
	call := Call{Iface: testIface, IfaceVersion: 1, Body: make([]byte, 64)}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := c.Do(context.Background(), s.LocalAddr(), call); err != nil {
			b.Fatal(err)
		}
	}
}

// TestClientActivity: calls on one Activity share its UUID and number their
// sequence consecutively, wait for each other, and are independent of the
// client's pool (p-net ties an AR to the activity of its Connect, so every
// call of an AR uses one). An idempotent call is not acknowledged (p-net
// does not process ACK packets).
func TestClientActivity(t *testing.T) {
	c, _, peer := clientEnv(t, ClientOptions{Activities: 1, Retransmit: time.Minute})
	a := c.NewActivity()
	if a.UUID().IsNil() {
		t.Fatal("nil activity UUID")
	}
	do := func(ctx context.Context, call Call) chan result {
		ch := make(chan result, 1)
		go func() {
			r, err := c.DoActivity(ctx, a, peer.LocalAddr(), call)
			ch <- result{r, err}
		}()
		return ch
	}
	first := do(context.Background(), Call{Idempotent: true})
	req, _, from := expect(t, peer, TypeRequest)
	if req.Activity != a.UUID() || req.Flags1&FlagIdempotent == 0 {
		t.Fatalf("request %+v", req)
	}
	// The activity is busy: a second call on it waits until its deadline,
	// while the pool's activity serves another call.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.DoActivity(ctx, a, peer.LocalAddr(), Call{}); !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("busy activity: %v", err)
	}
	pool := doAsync(c, context.Background(), peer.LocalAddr(), Call{})
	preq, _, pfrom := expect(t, peer, TypeRequest)
	if preq.Activity == a.UUID() {
		t.Fatal("pool call on the AR's activity")
	}
	peer.sendPkt(pfrom, respond(preq, TypeResponse), nil)
	if r := wait(t, pool); r.err != nil {
		t.Fatal(r.err)
	}
	expect(t, peer, TypeAck)

	second := do(context.Background(), Call{Idempotent: true})
	peer.sendPkt(from, respond(req, TypeResponse), nil)
	if r := wait(t, first); r.err != nil {
		t.Fatal(r.err)
	}
	req2, _, from := expect(t, peer, TypeRequest)
	if req2.Activity != a.UUID() || req2.SeqNum != req.SeqNum+1 || req2.ServerBoot != 77 {
		t.Fatalf("second request %+v (first %+v)", req2, req)
	}
	peer.sendPkt(from, respond(req2, TypeResponse), nil)
	if r := wait(t, second); r.err != nil {
		t.Fatal(r.err)
	}
	noPacket(t, peer)

	_ = c.Close()
	if _, err := c.DoActivity(context.Background(), a, peer.LocalAddr(), Call{}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}
