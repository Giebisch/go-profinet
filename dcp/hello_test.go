// SPDX-License-Identifier: MIT

package dcp

import (
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// helloVector is a Hello.req built field by field from the layout that
// Wireshark's DCP dissector and p-net agree on (docs/research/
// EVIDENCE-DCP-PDEV.md, A.5): station "dut", 192.168.0.50/24, gateway
// 192.168.0.1, vendor 0x0493, device 0x0002, IO-Device, DeviceInitiative 1.
const helloVector = "010ecf000001" + "020000000001" + "8892" + "fefc" +
	"0600" + "00000001" + "0000" + "0036" +
	"0202" + "0005" + "0000" + "647574" + "00" +
	"0102" + "000e" + "0001" + "c0a80032" + "ffffff00" + "c0a80001" +
	"0203" + "0006" + "0000" + "0493" + "0002" +
	"0204" + "0004" + "0000" + "0100" +
	"0601" + "0004" + "0000" + "0001"

func helloIdentity() Identity {
	return Identity{NameOfStation: "dut", VendorID: 0x0493, DeviceID: 0x0002, Role: RoleIODevice,
		IP:     IPSuite{Addr: netip.MustParseAddr("192.168.0.50"), Mask: netip.MustParseAddr("255.255.255.0"), Gateway: netip.MustParseAddr("192.168.0.1")},
		IPInfo: IPInfoSet}
}

func TestHelloFrameGolden(t *testing.T) {
	id := helloIdentity()
	hdr := ethernet.Frame{Dst: ethernet.DCPHelloMulticast, Src: ethernet.MAC{2, 0, 0, 0, 0, 1}, EtherType: ethernet.EtherTypeProfinet}
	b, err := HelloFrame(&id, 1).AppendTo(hdr.AppendHeader(nil))
	if err != nil {
		t.Fatal(err)
	}
	b = ethernet.Pad(b, 0)
	if got := hex.EncodeToString(b); got != helloVector {
		t.Fatalf("Hello frame\n got %s\nwant %s", got, helloVector)
	}
	// It decodes back to the identity, DeviceInitiative included.
	f, err := Decode(b[14:])
	if err != nil || f.Service != ServiceHello || f.Type != TypeRequest || f.Xid != 1 || f.ResponseDelay != 0 {
		t.Fatalf("%+v %v", f, err)
	}
	got, err := ParseIdentity(hdr.Src, f.Blocks)
	if err != nil || got.NameOfStation != "dut" || got.IP != id.IP || got.IPInfo != IPInfoSet || !got.HasInitiative || got.Initiative != InitiativeHello || got.Role != RoleIODevice {
		t.Fatalf("%+v %v", got, err)
	}
	// An identity that states its initiative keeps it.
	id.Initiative, id.HasInitiative = InitiativeNone, true
	if blk, _ := HelloFrame(&id, 2).Find(Initiative); hex.EncodeToString(blk.Data) != "0000" {
		t.Fatalf("initiative % x", blk.Data)
	}
}

func TestIdentityInitiative(t *testing.T) {
	id := helloIdentity()
	id.Initiative, id.HasInitiative = InitiativeHello, true
	blocks := id.Blocks()
	var opts []BlockID
	found := false
	for _, b := range blocks {
		if b.ID == Initiative {
			found = hex.EncodeToString(b.Data) == "0001"
		}
		if b.ID == DeviceOpts {
			p, _ := ParseIdentity(ethernet.MAC{}, []Block{b})
			opts = p.Options
		}
	}
	if !found {
		t.Fatal("no DeviceInitiative block")
	}
	listed := false
	for _, o := range opts {
		listed = listed || o == Initiative
	}
	if !listed {
		t.Fatalf("DeviceInitiative not among the options %v", opts)
	}
	if _, err := ParseIdentity(ethernet.MAC{}, []Block{{ID: Initiative, HasInfo: true, Data: []byte{1}}}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("short block: %v", err)
	}
	// Without HasInitiative the block is not sent.
	id.HasInitiative = false
	for _, b := range id.Blocks() {
		if b.ID == Initiative {
			t.Fatal("DeviceInitiative without HasInitiative")
		}
	}
}

func TestHelloOptions(t *testing.T) {
	o, err := HelloOptions{}.Normalize()
	if err != nil || o.Count != DefaultHelloCount || o.Interval != DefaultHelloInterval || o.Delay != 0 {
		t.Fatalf("%+v %v", o, err)
	}
	for _, bad := range []HelloOptions{{Count: MaxHelloCount + 1}, {Count: -1}, {Interval: time.Millisecond}, {Delay: -1}, {Delay: MaxHelloDelay + 1}} {
		if _, err := bad.Normalize(); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	for m, s := range map[HelloMode]string{HelloOff: "off", HelloOnLinkUp: "on link up", HelloOnLinkUpDelayed: "on link up, delayed", 9: "HelloMode(9)"} {
		if m.String() != s {
			t.Errorf("%d: %q", m, m.String())
		}
	}
}

// recvHello returns the next Hello on the raw peer, with its destination.
func recvHello(t *testing.T, d *rawDev, timeout time.Duration) (ethernet.MAC, Frame, time.Time, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		ef, err := d.sub.Recv(ctx)
		if err != nil {
			return ethernet.MAC{}, Frame{}, time.Time{}, false
		}
		f, err := Decode(append([]byte(nil), ef.Payload...))
		if err != nil || f.Service != ServiceHello {
			continue
		}
		return ef.Dst, f, time.Now(), true
	}
}

func TestHelloSequence(t *testing.T) {
	st := testStation()
	r, raw, _ := responderEnv(t, st, ResponderOptions{NoResponseDelay: true}, devMAC, 0)
	start := time.Now()
	if err := r.StartHello(HelloOptions{Count: 3, Interval: 40 * time.Millisecond, Delay: 60 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	var times []time.Time
	xids := map[uint32]bool{}
	for i := 0; i < 3; i++ {
		dst, f, at, ok := recvHello(t, raw, 2*time.Second)
		if !ok {
			t.Fatalf("Hello %d missing", i+1)
		}
		if dst != ethernet.DCPHelloMulticast || f.FrameID != FrameIDHello || f.Type != TypeRequest {
			t.Fatalf("Hello to %s: %+v", dst, f)
		}
		id, err := ParseIdentity(devMAC, f.Blocks)
		if err != nil || id.NameOfStation != "dev" || id.IP != st.id.IP {
			t.Fatalf("%+v %v", id, err)
		}
		xids[f.Xid] = true
		times = append(times, at)
	}
	// A Hello is received no earlier than it is sent, and sent no earlier
	// than its schedule: the delay, then one interval each. (The gaps
	// between receptions say nothing: a late reception of one Hello
	// shortens the next gap.)
	for i, at := range times {
		if d, want := at.Sub(start), 60*time.Millisecond+time.Duration(i)*40*time.Millisecond; d < want {
			t.Errorf("Hello %d after %s, scheduled at %s", i+1, d, want)
		}
	}
	if len(xids) != 3 {
		t.Errorf("Xids %v", xids)
	}
	if _, _, _, ok := recvHello(t, raw, 200*time.Millisecond); ok {
		t.Fatal("more than Count Hellos")
	}
	if r.Stats().Hello != 3 {
		t.Fatalf("stats %+v", r.Stats())
	}
}

func TestHelloStopsOnSet(t *testing.T) {
	r, raw, _ := responderEnv(t, testStation(), ResponderOptions{NoResponseDelay: true}, devMAC, 0)
	if err := r.StartHello(HelloOptions{Count: MaxHelloCount, Interval: 30 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := recvHello(t, raw, 2*time.Second); !ok {
		t.Fatal("no Hello")
	}
	raw.send(t, devMAC, &Frame{FrameID: FrameIDGetSet, Service: ServiceSet, Type: TypeRequest, Xid: 5,
		Blocks: []Block{{ID: CtrlSignal, HasInfo: true, Data: []byte{0x01, 0x00}}}})
	waitFor(t, "Set handled", func() bool { return r.Stats().Set == 1 })
	sent := r.Stats().Hello
	time.Sleep(150 * time.Millisecond)
	if n := r.Stats().Hello; n > sent+1 {
		t.Fatalf("%d Hellos after the Set", n-sent)
	}
	if n := r.Stats().Hello; n >= MaxHelloCount {
		t.Fatalf("the sequence ran to its end: %d", n)
	}
}

func TestHelloNeedsNameAndIP(t *testing.T) {
	for name, mod := range map[string]func(*Identity){
		"no name": func(id *Identity) { id.NameOfStation = "" },
		"no IP":   func(id *Identity) { id.IP = IPSuite{} },
	} {
		t.Run(name, func(t *testing.T) {
			st := testStation()
			mod(&st.id)
			r, raw, _ := responderEnv(t, st, ResponderOptions{}, devMAC, 0)
			if err := r.StartHello(HelloOptions{Count: 2, Interval: MinHelloInterval}); err != nil {
				t.Fatal(err)
			}
			if _, _, _, ok := recvHello(t, raw, 150*time.Millisecond); ok {
				t.Fatal("Hello without a name or an IP address")
			}
		})
	}
}

// TestHelloNoStorm restarts the sequence far faster than its interval, as
// a flapping link would: the Hellos stay an interval apart.
func TestHelloNoStorm(t *testing.T) {
	r, raw, _ := responderEnv(t, testStation(), ResponderOptions{}, devMAC, 0)
	const interval = 50 * time.Millisecond
	// Receive concurrently: the arrival times matter.
	arrivals := make(chan time.Time, 64)
	go func() {
		defer close(arrivals)
		for {
			_, _, at, ok := recvHello(t, raw, 300*time.Millisecond)
			if !ok {
				return
			}
			arrivals <- at
		}
	}()
	stop := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(stop) {
		if err := r.StartHello(HelloOptions{Count: 1, Interval: interval}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.StopHello()
	var last time.Time
	n := 0
	for at := range arrivals {
		if !last.IsZero() && at.Sub(last) < interval-15*time.Millisecond {
			t.Errorf("Hellos %s apart", at.Sub(last))
		}
		last = at
		n++
	}
	if n == 0 || n > 8 {
		t.Fatalf("%d Hellos in 300 ms at a 50 ms interval", n)
	}
}

func TestHelloStopAndClose(t *testing.T) {
	r, raw, _ := responderEnv(t, testStation(), ResponderOptions{}, devMAC, 0)
	r.StopHello() // nothing running
	if err := r.StartHello(HelloOptions{Count: 2, Interval: time.Second, Delay: time.Second}); err != nil {
		t.Fatal(err)
	}
	r.StopHello()
	if _, _, _, ok := recvHello(t, raw, 100*time.Millisecond); ok {
		t.Fatal("Hello after StopHello")
	}
	if err := r.StartHello(HelloOptions{Count: 2, Interval: time.Second, Delay: time.Second}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waits for the Hello sequence")
	}
	if err := r.StartHello(HelloOptions{}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("after Close: %v", err)
	}
	if err := r.StartHello(HelloOptions{Count: 99}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("bad options: %v", err)
	}
}

// TestHelloToClient: the requester side receives the responder's Hello.
func TestHelloToClient(t *testing.T) {
	_, c, raw := newClientEnv(t, ClientOptions{})
	ch := c.Hellos(context.Background(), 4)
	st := testStation()
	st.id.Initiative, st.id.HasInitiative = InitiativeHello, true
	r, err := NewResponder(raw.mux, st, ResponderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if err := r.StartHello(HelloOptions{Count: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-ch:
		if id.NameOfStation != "dev" || id.MAC != raw.mux.HardwareAddr() || id.Initiative != InitiativeHello || !strings.HasPrefix(id.IP.Addr.String(), "10.") {
			t.Fatalf("%+v", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Hello at the client")
	}
}
