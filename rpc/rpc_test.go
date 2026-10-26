// SPDX-License-Identifier: MIT

package rpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
)

var testIface = MustParseUUID("dea00001-6c97-11d1-8271-00a02442df7d")

func listen(t *testing.T) net.PacketConn {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// lossy drops the first n packets written through it.
type lossy struct {
	net.PacketConn
	drop atomic.Int32
}

func (l *lossy) WriteTo(p []byte, a net.Addr) (int, error) {
	if l.drop.Add(-1) >= 0 {
		return len(p), nil
	}
	return l.PacketConn.WriteTo(p, a)
}

func echoServer(t *testing.T, conn net.PacketConn, opts ServerOptions, calls *atomic.Int32) *Server {
	t.Helper()
	s := NewServer(conn, true, opts)
	s.Register(testIface, 1, HandlerFunc(func(ctx context.Context, r *Request) ([]byte, error) {
		calls.Add(1)
		switch r.Header.OpNum {
		case 0:
			return append([]byte("echo:"), r.Body...), nil
		case 1:
			return nil, &Fault{Status: 0xDEADBEEF}
		case 2:
			return nil, errors.New("boom")
		case 3:
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
			}
			return []byte("slow"), nil
		case 4:
			return bytes.Repeat([]byte{0xAB}, 10000), nil
		default:
			return nil, &Fault{Reject: true, Status: StatusOpRangeError}
		}
	}))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCallAndFaults(t *testing.T) {
	var calls atomic.Int32
	srv := echoServer(t, listen(t), ServerOptions{}, &calls)
	c := NewClient(listen(t), true, ClientOptions{Timeout: 2 * time.Second, Retransmit: 100 * time.Millisecond})
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	obj := NewRandomUUID()
	r, err := c.Do(ctx, srv.LocalAddr(), Call{Object: obj, Iface: testIface, IfaceVersion: 1, OpNum: 0, Body: []byte("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if string(r.Body) != "echo:hi" || r.Header.Object != obj || r.Header.ServerBoot != srv.BootTime() {
		t.Fatalf("response %q %+v", r.Body, r.Header)
	}
	_, err = c.Do(ctx, srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, OpNum: 1})
	var f *Fault
	if !errors.As(err, &f) || f.Status != 0xDEADBEEF || f.Reject || !errors.Is(err, profinet.ErrRejected) {
		t.Fatalf("err %v", err)
	}
	_, err = c.Do(ctx, srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, OpNum: 2})
	if !errors.As(err, &f) || f.Status != StatusFaultUnspec {
		t.Fatalf("err %v", err)
	}
	_, err = c.Do(ctx, srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, OpNum: 9})
	if !errors.As(err, &f) || !f.Reject || !errors.Is(err, profinet.ErrUnsupported) {
		t.Fatalf("err %v", err)
	}
	_, err = c.Do(ctx, srv.LocalAddr(), Call{Iface: NewRandomUUID(), IfaceVersion: 1})
	if !errors.As(err, &f) || f.Status != StatusUnknownIf {
		t.Fatalf("err %v", err)
	}
	_, err = c.Do(ctx, srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 2})
	if !errors.As(err, &f) || f.Status != StatusUnknownIf {
		t.Fatalf("version: err %v", err)
	}
	if !errors.Is(&Fault{Status: StatusServerTooBusy}, profinet.ErrResourceExhausted) {
		t.Fatal("busy fault")
	}
	if f.Error() == "" || (&Fault{Status: 1}).Error() == "" {
		t.Fatal("empty error")
	}
}

func TestFragmentation(t *testing.T) {
	for _, big := range []bool{false, true} {
		t.Run(fmt.Sprint("bigendian=", big), func(t *testing.T) {
			var calls atomic.Int32
			srv := echoServer(t, listen(t), ServerOptions{MaxFragmentBody: 100}, &calls)
			c := NewClient(listen(t), true, ClientOptions{MaxFragmentBody: 64, BigEndian: big})
			defer func() { _ = c.Close() }()
			body := make([]byte, 5000)
			for i := range body {
				body[i] = byte(i)
			}
			r, err := c.Do(context.Background(), srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, Body: body})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(r.Body[5:], body) {
				t.Fatal("body mismatch")
			}
			r, err = c.Do(context.Background(), srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, OpNum: 4})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Body) != 10000 {
				t.Fatalf("response of %d octets, want 10000", len(r.Body))
			}
		})
	}
}

func TestAtMostOnce(t *testing.T) {
	var calls atomic.Int32
	sc := &lossy{PacketConn: listen(t)}
	sc.drop.Store(2) // the first two responses are lost
	srv := echoServer(t, sc, ServerOptions{}, &calls)
	c := NewClient(listen(t), true, ClientOptions{Retransmit: 30 * time.Millisecond})
	defer func() { _ = c.Close() }()
	r, err := c.Do(context.Background(), srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, Body: []byte("x")})
	if err != nil || string(r.Body) != "echo:x" {
		t.Fatalf("%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("executed %d times", calls.Load())
	}
	if srv.Stats().Retransmits < 2 {
		t.Fatalf("stats %+v", srv.Stats())
	}
	// A slow handler is not executed again by retransmissions.
	calls.Store(0)
	r, err = c.Do(context.Background(), srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, OpNum: 3})
	if err != nil || string(r.Body) != "slow" || calls.Load() != 1 {
		t.Fatalf("%v %d", err, calls.Load())
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	sink := listen(t)
	defer func() { _ = sink.Close() }()
	c := NewClient(listen(t), true, ClientOptions{Timeout: 100 * time.Millisecond, Retransmit: 20 * time.Millisecond, Activities: 1})
	_, err := c.Do(context.Background(), sink.LocalAddr(), Call{Iface: testIface})
	if !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = c.Do(ctx, sink.LocalAddr(), Call{Iface: testIface})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = c.Close()
	}()
	_, err = c.Do(context.Background(), sink.LocalAddr(), Call{Iface: testIface})
	if !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("err %v", err)
	}
}

func TestConcurrentCalls(t *testing.T) {
	var calls atomic.Int32
	srv := echoServer(t, listen(t), ServerOptions{Workers: 4}, &calls)
	c := NewClient(listen(t), true, ClientOptions{Activities: 8})
	defer func() { _ = c.Close() }()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := []byte(fmt.Sprint(i))
			r, err := c.Do(context.Background(), srv.LocalAddr(), Call{Iface: testIface, IfaceVersion: 1, Body: msg})
			if err != nil {
				errs <- err
				return
			}
			if string(r.Body) != "echo:"+string(msg) {
				errs <- fmt.Errorf("call %d got %q", i, r.Body)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// raw sends hand-made packets to a server.
func raw(t *testing.T, srv *Server, h Header, body []byte) []Header {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Error(err)
		return nil
	}
	defer func() { _ = c.Close() }()
	h.BodyLen = uint16(len(body))
	if h.DRep == ([3]byte{}) {
		h.DRep = [3]byte{DRepLittleEndian}
	}
	b := append(h.AppendTo(nil), body...)
	if _, err := c.WriteTo(b, srv.LocalAddr()); err != nil {
		t.Error(err)
		return nil
	}
	var out []Header
	buf := make([]byte, 2000)
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	for {
		n, _, err := c.ReadFrom(buf)
		if err != nil {
			return out
		}
		rh, _, err := DecodeHeader(buf[:n])
		if err != nil {
			t.Error(err)
			return out
		}
		out = append(out, rh)
	}
}

func TestServerControlPackets(t *testing.T) {
	var calls atomic.Int32
	srv := echoServer(t, listen(t), ServerOptions{}, &calls)
	act := NewRandomUUID()
	if got := raw(t, srv, Header{Type: TypePing, Activity: act, SeqNum: 1}, nil); len(got) != 1 || got[0].Type != TypeNoCall {
		t.Fatalf("ping: %+v", got)
	}
	if got := raw(t, srv, Header{Type: TypeCancel, Activity: act}, nil); len(got) != 1 || got[0].Type != TypeCancelAck {
		t.Fatalf("cancel: %+v", got)
	}
	// A fragment that wants a FACK.
	got := raw(t, srv, Header{Type: TypeRequest, Iface: testIface, IfaceVersion: 1, Activity: act, SeqNum: 5, Flags1: FlagFrag}, []byte{1, 2})
	if len(got) != 1 || got[0].Type != TypeFack {
		t.Fatalf("fack: %+v", got)
	}
	// A slow call answers a ping with WORKING.
	act2 := NewRandomUUID()
	go raw(t, srv, Header{Type: TypeRequest, Iface: testIface, IfaceVersion: 1, OpNum: 3, Activity: act2, SeqNum: 1}, nil)
	time.Sleep(50 * time.Millisecond)
	if got := raw(t, srv, Header{Type: TypePing, Activity: act2, SeqNum: 1}, nil); len(got) != 1 || got[0].Type != TypeWorking {
		t.Fatalf("ping while working: %+v", got)
	}
	time.Sleep(300 * time.Millisecond)
	if got := raw(t, srv, Header{Type: TypePing, Activity: act2, SeqNum: 1}, nil); len(got) != 1 || got[0].Type != TypeResponse {
		t.Fatalf("ping after done: %+v", got)
	}
	raw(t, srv, Header{Type: TypeAck, Activity: act2, SeqNum: 1}, nil)
	// Garbage is counted.
	c := listen(t)
	_, _ = c.WriteTo([]byte{1, 2, 3}, srv.LocalAddr())
	_ = c.Close()
	time.Sleep(20 * time.Millisecond)
	if srv.Stats().Malformed != 1 {
		t.Fatalf("stats %+v", srv.Stats())
	}
}

func TestServerBusy(t *testing.T) {
	var calls atomic.Int32
	srv := echoServer(t, listen(t), ServerOptions{MaxActivities: 1, Workers: 1}, &calls)
	go raw(t, srv, Header{Type: TypeRequest, Iface: testIface, IfaceVersion: 1, OpNum: 3, Activity: NewRandomUUID(), SeqNum: 1}, nil)
	time.Sleep(50 * time.Millisecond)
	got := raw(t, srv, Header{Type: TypeRequest, Iface: testIface, IfaceVersion: 1, Activity: NewRandomUUID(), SeqNum: 1}, nil)
	if len(got) != 1 || got[0].Type != TypeReject {
		t.Fatalf("got %+v", got)
	}
	if srv.Stats().Busy != 1 {
		t.Fatalf("stats %+v", srv.Stats())
	}
}

func TestHeaderAndUUID(t *testing.T) {
	u := MustParseUUID("DEA00001-6C97-11D1-8271-00A02442DF7D")
	if u.String() != "dea00001-6c97-11d1-8271-00a02442df7d" {
		t.Fatal(u)
	}
	le := u.Append(nil, binary.LittleEndian)
	if le[0] != 0x01 || le[3] != 0xDE || DecodeUUID(le, binary.LittleEndian) != u {
		t.Fatalf("% x", le)
	}
	var v UUID
	if err := v.UnmarshalText([]byte(u.String())); err != nil || v != u {
		t.Fatal(err)
	}
	if _, err := ParseUUID("nope"); err == nil {
		t.Fatal("parsed")
	}
	r := NewRandomUUID()
	if r[6]>>4 != 4 || r.IsNil() {
		t.Fatal(r)
	}
	h := Header{Type: TypeRequest, DRep: [3]byte{DRepBigEndian}, Serial: 0x1234, SeqNum: 7, OpNum: 3, BodyLen: 2, Object: u}
	b := append(h.AppendTo(nil), 9, 9)
	g, body, err := DecodeHeader(b)
	if err != nil || g != h || len(body) != 2 {
		t.Fatalf("%+v %v", g, err)
	}
	for _, bad := range [][]byte{b[:79], append([]byte{5}, b[1:]...), append([]byte{4, 99}, b[2:]...), b[:81]} {
		if _, _, err := DecodeHeader(bad); err == nil {
			t.Fatal("decoded bad header")
		}
	}
	if TypeFack.String() != "fack" || PacketType(99).String() != "ptype(99)" {
		t.Fatal("names")
	}
}

func TestReassembly(t *testing.T) {
	r := newReassembly(10)
	h := Header{Flags1: FlagFrag, FragNum: 1, Flags2: 0}
	if _, ok, err := r.add(&h, []byte{3, 4}); ok || err != nil {
		t.Fatal(ok, err)
	}
	h.FragNum = 1
	if _, ok, _ := r.add(&h, []byte{3, 4}); ok {
		t.Fatal("dup completed")
	}
	h.FragNum, h.Flags1 = 2, FlagFrag|FlagLastFrag
	if _, ok, err := r.add(&h, []byte{5}); ok || err != nil {
		t.Fatal(ok, err)
	}
	h.FragNum, h.Flags1 = 0, FlagFrag
	out, ok, err := r.add(&h, []byte{1, 2})
	if !ok || err != nil || !bytes.Equal(out, []byte{1, 2, 3, 4, 5}) {
		t.Fatal(out, ok, err)
	}
	r = newReassembly(3)
	if _, _, err := r.add(&Header{Flags1: FlagFrag}, []byte{1, 2, 3, 4}); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Fatal(err)
	}
	r = newReassembly(0)
	_, _, _ = r.add(&Header{Flags1: FlagFrag | FlagLastFrag, FragNum: 1}, nil)
	if _, _, err := r.add(&Header{Flags1: FlagFrag, FragNum: 2}, nil); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatal(err)
	}
	if _, _, err := r.add(&Header{Flags1: FlagFrag | FlagLastFrag, FragNum: 0}, nil); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatal(err)
	}
	if _, _, err := r.add(&Header{Flags1: FlagFrag, FragNum: 300}, nil); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Fatal(err)
	}
}

func FuzzDecodeHeader(f *testing.F) {
	h := Header{Type: TypeRequest, DRep: [3]byte{DRepLittleEndian}, BodyLen: 3}
	f.Add(append(h.AppendTo(nil), 1, 2, 3))
	f.Fuzz(func(t *testing.T, b []byte) {
		h, body, err := DecodeHeader(b)
		if err != nil {
			return
		}
		out := append(h.AppendTo(nil), body...)
		h2, body2, err := DecodeHeader(out)
		if err != nil || h2 != h || !bytes.Equal(body, body2) {
			t.Fatalf("round trip: %v", err)
		}
		asm := newReassembly(1000)
		_, _, _ = asm.add(&h, body)
	})
}
