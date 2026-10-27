// SPDX-License-Identifier: MIT

package bridge

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/vnet"
)

var (
	cpuMAC = ethernet.MAC{0x02, 0xB0, 0, 0, 0, 0x01}
	p1MAC  = ethernet.MAC{0x02, 0xB0, 0, 0, 0, 0x11}
	p2MAC  = ethernet.MAC{0x02, 0xB0, 0, 0, 0, 0x12}
	p3MAC  = ethernet.MAC{0x02, 0xB0, 0, 0, 0, 0x13}
	hostA  = ethernet.MAC{0x02, 0xAA, 0, 0, 0, 0x01}
	hostB  = ethernet.MAC{0x02, 0xBB, 0, 0, 0, 0x01}
	hostC  = ethernet.MAC{0x02, 0xCC, 0, 0, 0, 0x01}
	lldpG  = ethernet.MAC{0x01, 0x80, 0xC2, 0, 0, 0x0E}
	mrpG   = ethernet.MAC{0x01, 0x15, 0x4E, 0, 0, 0x01}
	dcpG   = ethernet.MAC{0x01, 0x0E, 0xCF, 0, 0, 0}
)

// env is a bridge with n ports, each on its own segment with one host.
type env struct {
	t     *testing.T
	b     *Bridge
	ports []*vnet.Endpoint
	hosts []*vnet.Endpoint
}

func newEnv(t *testing.T, n int, cfg Config) *env {
	t.Helper()
	e := &env{t: t}
	pm := []ethernet.MAC{p1MAC, p2MAC, p3MAC}
	hm := []ethernet.MAC{hostA, hostB, hostC}
	for i := 0; i < n; i++ {
		net := vnet.New(uint64(i + 1))
		t.Cleanup(func() { _ = net.Close() })
		p, err := net.NewEndpoint(vnet.EndpointConfig{Name: "port", MAC: pm[i], Promiscuous: true})
		if err != nil {
			t.Fatal(err)
		}
		h, err := net.NewEndpoint(vnet.EndpointConfig{Name: "host", MAC: hm[i]})
		if err != nil {
			t.Fatal(err)
		}
		e.ports, e.hosts = append(e.ports, p), append(e.hosts, h)
		cfg.Ports = append(cfg.Ports, p)
	}
	if cfg.MAC.IsZero() {
		cfg.MAC = cpuMAC
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	e.b = b
	return e
}

func frame(dst, src ethernet.MAC, tag byte) []byte {
	f := ethernet.Frame{Dst: dst, Src: src, EtherType: 0x88B5, Payload: []byte{tag, 1, 2, 3}}
	return f.Encode()
}

func send(t *testing.T, p l2.Port, f []byte) {
	t.Helper()
	if err := p.WriteFrame(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

// stash keeps the frames recv read while it looked for another tag.
var (
	stashMu sync.Mutex
	stash   = map[l2.Port][][]byte{}
)

// recv returns the next frame with the given tag on p, nil after the
// timeout. Frames with other tags are kept for later calls.
func recv(p l2.Port, tag byte, timeout time.Duration) []byte {
	stashMu.Lock()
	for i, f := range stash[p] {
		if f[14] == tag {
			stash[p] = append(stash[p][:i:i], stash[p][i+1:]...)
			stashMu.Unlock()
			return f
		}
	}
	stashMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	buf := make([]byte, 2048)
	for {
		n, err := p.ReadFrame(ctx, buf)
		if err != nil {
			return nil
		}
		if n < 15 {
			continue
		}
		f := append([]byte(nil), buf[:n]...)
		if f[14] == tag {
			return f
		}
		stashMu.Lock()
		stash[p] = append(stash[p], f)
		stashMu.Unlock()
	}
}

// sentBy waits until a port endpoint has sent n frames.
func sentBy(t *testing.T, p *vnet.Endpoint, n uint64) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for p.Stats().Sent < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s sent %d, want %d", p.HardwareAddr(), p.Stats().Sent, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

const wait = 2 * time.Second
const quiet = 100 * time.Millisecond

func TestConfig(t *testing.T) {
	net := vnet.New(1)
	defer func() { _ = net.Close() }()
	p, _ := net.NewEndpoint(vnet.EndpointConfig{MAC: p1MAC})
	for name, cfg := range map[string]Config{
		"no ports":      {MAC: cpuMAC},
		"no MAC":        {Ports: []l2.Port{p}},
		"multicast":     {Ports: []l2.Port{p}, MAC: dcpG},
		"nil port":      {Ports: []l2.Port{nil}, MAC: cpuMAC},
		"MAC twice":     {Ports: []l2.Port{p, p}, MAC: cpuMAC},
		"MAC of a port": {Ports: []l2.Port{p}, MAC: p1MAC},
	} {
		if _, err := New(cfg); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestForwarding(t *testing.T) {
	e := newEnv(t, 3, Config{})
	cpu := e.b.CPU()
	if cpu.HardwareAddr() != cpuMAC || e.b.Port(1).HardwareAddr() != p2MAC || e.b.NumPorts() != 3 || e.b.Physical(0) != l2.Port(e.ports[0]) {
		t.Fatal("addresses")
	}
	// Unknown unicast is flooded to the other ports (host C's own filter
	// drops it: port 3 sent it), not to the CPU.
	send(t, e.hosts[0], frame(hostB, hostA, 1))
	if recv(e.hosts[1], 1, wait) == nil {
		t.Fatal("not flooded")
	}
	sentBy(t, e.ports[2], 1)
	if recv(cpu, 1, quiet) != nil {
		t.Fatal("unknown unicast reached the CPU")
	}
	// hostA was learned on port 1; B answers: only port 1 carries it.
	if i, ok := e.b.Learned(hostA); !ok || i != 0 {
		t.Fatalf("learned %d %v", i, ok)
	}
	send(t, e.hosts[1], frame(hostA, hostB, 2))
	if recv(e.hosts[0], 2, wait) == nil {
		t.Fatal("not forwarded")
	}
	if recv(e.hosts[2], 2, quiet) != nil {
		t.Fatal("known unicast flooded")
	}
	// To the interface: the CPU only.
	send(t, e.hosts[0], frame(cpuMAC, hostA, 3))
	if recv(cpu, 3, wait) == nil {
		t.Fatal("not delivered to the CPU")
	}
	if recv(e.hosts[1], 3, quiet) != nil {
		t.Fatal("the interface's frame was forwarded")
	}
	// Broadcast: the CPU and the other ports.
	send(t, e.hosts[2], frame(ethernet.Broadcast, hostC, 4))
	if recv(cpu, 4, wait) == nil || recv(e.hosts[0], 4, wait) == nil || recv(e.hosts[1], 4, wait) == nil {
		t.Fatal("broadcast")
	}
	// The CPU's unicast goes to the learned port; unknown is flooded.
	send(t, cpu, frame(hostB, cpuMAC, 5))
	if recv(e.hosts[1], 5, wait) == nil {
		t.Fatal("CPU unicast")
	}
	if recv(e.hosts[0], 5, quiet) != nil {
		t.Fatal("CPU unicast flooded")
	}
	before0, before2 := e.ports[0].Stats().Sent, e.ports[2].Stats().Sent
	send(t, cpu, frame(ethernet.MAC{0x02, 0xEE, 0, 0, 0, 9}, cpuMAC, 6))
	sentBy(t, e.ports[0], before0+1)
	sentBy(t, e.ports[2], before2+1)
	// The destination on the port the frame came from: filtered.
	send(t, e.hosts[0], frame(hostA, hostA, 7))
	if recv(e.hosts[1], 7, quiet) != nil {
		t.Fatal("hairpin")
	}
	// Short frames from the CPU are padded.
	if err := cpu.WriteFrame(context.Background(), frame(ethernet.Broadcast, cpuMAC, 8)[:15]); err != nil {
		t.Fatal(err)
	}
	if f := recv(e.hosts[0], 8, wait); len(f) < ethernet.MinFrameLen {
		t.Fatalf("padded frame %d", len(f))
	}
	if err := cpu.WriteFrame(context.Background(), []byte{1, 2}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("runt: %v", err)
	}
	if s := e.b.Stats(0); s.Received == 0 || s.Forwarded == 0 || s.Sent == 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestLinkLocal(t *testing.T) {
	e := newEnv(t, 2, Config{})
	// LLDP and MRP stay on their port: the local port of port 1 gets
	// them, port 2 and the CPU do not.
	send(t, e.hosts[0], frame(lldpG, hostA, 1))
	send(t, e.hosts[0], frame(mrpG, hostA, 2))
	if recv(e.b.Port(0), 1, wait) == nil || recv(e.b.Port(0), 2, wait) == nil {
		t.Fatal("not local")
	}
	if recv(e.hosts[1], 1, quiet) != nil || recv(e.b.CPU(), 1, quiet) != nil || recv(e.b.Port(1), 1, quiet) != nil {
		t.Fatal("link-local frame left its port")
	}
	// The local port sends on its port alone.
	send(t, e.b.Port(1), frame(lldpG, p2MAC, 3))
	if recv(e.hosts[1], 3, wait) == nil {
		t.Fatal("local send")
	}
	if recv(e.hosts[0], 3, quiet) != nil {
		t.Fatal("local send left on another port")
	}
	// Unicast to a port's own address is its local entity's.
	send(t, e.hosts[0], frame(p1MAC, hostA, 4))
	if recv(e.b.Port(0), 4, wait) == nil {
		t.Fatal("to the port's address")
	}
	if s := e.b.Stats(0); s.Local < 3 {
		t.Fatalf("stats %+v", s)
	}
	for _, d := range []ethernet.MAC{{0x01, 0x80, 0xC2, 0, 0, 0x10}, {0x01, 0x15, 0x4E, 0, 0, 0x05}, dcpG} {
		if LinkLocal(d) {
			t.Errorf("%s link-local", d)
		}
	}
}

func TestBlocked(t *testing.T) {
	e := newEnv(t, 2, Config{})
	send(t, e.hosts[1], frame(ethernet.Broadcast, hostB, 1))
	if recv(e.hosts[0], 1, wait) == nil {
		t.Fatal("forwarding")
	}
	if _, ok := e.b.Learned(hostB); !ok {
		t.Fatal("not learned")
	}
	e.b.SetState(1, Blocked)
	e.b.SetState(1, Blocked)
	if e.b.State(1) != Blocked || e.b.State(1).String() != "blocked" || Forwarding.String() != "forwarding" {
		t.Fatal("state")
	}
	if _, ok := e.b.Learned(hostB); ok {
		t.Fatal("blocking keeps the port's stations")
	}
	// Nothing crosses a blocked port either way, and the CPU does not
	// hear it; its local frames still pass.
	send(t, e.hosts[1], frame(ethernet.Broadcast, hostB, 2))
	send(t, e.hosts[0], frame(ethernet.Broadcast, hostA, 3))
	send(t, e.b.CPU(), frame(ethernet.Broadcast, cpuMAC, 4))
	send(t, e.hosts[1], frame(mrpG, hostB, 5))
	send(t, e.b.Port(1), frame(mrpG, p2MAC, 6))
	if recv(e.hosts[0], 2, quiet) != nil || recv(e.b.CPU(), 2, quiet) != nil || recv(e.hosts[1], 3, quiet) != nil || recv(e.hosts[1], 4, quiet) != nil {
		t.Fatal("a blocked port carried data")
	}
	if recv(e.b.Port(1), 5, wait) == nil || recv(e.hosts[1], 6, wait) == nil {
		t.Fatal("local frames on a blocked port")
	}
	if s := e.b.Stats(1); s.Dropped == 0 {
		t.Fatalf("stats %+v", s)
	}
	e.b.SetState(1, Forwarding)
	send(t, e.hosts[1], frame(ethernet.Broadcast, hostB, 7))
	if recv(e.hosts[0], 7, wait) == nil {
		t.Fatal("forwarding again")
	}
}

func TestLinkDown(t *testing.T) {
	e := newEnv(t, 2, Config{})
	if !e.b.CPU().(l2.LinkStater).LinkUp() {
		t.Fatal("CPU link")
	}
	e.ports[1].SetLinkUp(false)
	if e.b.Port(1).(l2.LinkStater).LinkUp() || !e.b.CPU().(l2.LinkStater).LinkUp() {
		t.Fatal("link states")
	}
	send(t, e.hosts[0], frame(ethernet.Broadcast, hostA, 1))
	if recv(e.hosts[1], 1, quiet) != nil {
		t.Fatal("forwarded to a port that is down")
	}
	e.ports[0].SetLinkUp(false)
	if e.b.CPU().(l2.LinkStater).LinkUp() {
		t.Fatal("CPU up with every port down")
	}
}

func TestBoundary(t *testing.T) {
	e := newEnv(t, 2, Config{})
	e.b.SetBoundary(1, []ethernet.MAC{dcpG})
	// DCP multicast neither leaves nor enters through port 2; the CPU
	// still hears it; other groups pass.
	send(t, e.hosts[0], frame(dcpG, hostA, 1))
	send(t, e.hosts[1], frame(dcpG, hostB, 2))
	send(t, e.hosts[0], frame(ethernet.Broadcast, hostA, 3))
	if recv(e.hosts[1], 1, quiet) != nil || recv(e.hosts[0], 2, quiet) != nil {
		t.Fatal("DCP crossed the boundary")
	}
	if recv(e.b.CPU(), 1, wait) == nil || recv(e.b.CPU(), 2, wait) == nil {
		t.Fatal("the CPU missed DCP")
	}
	if recv(e.hosts[1], 3, wait) == nil {
		t.Fatal("broadcast blocked")
	}
	e.b.SetBoundary(1, nil)
	send(t, e.hosts[0], frame(dcpG, hostA, 4))
	if recv(e.hosts[1], 4, wait) == nil {
		t.Fatal("boundary cleared")
	}
}

func TestFDBBoundAndFlush(t *testing.T) {
	e := newEnv(t, 2, Config{MaxFDB: 2, AgeTime: 200 * time.Millisecond})
	for i := byte(1); i <= 4; i++ {
		send(t, e.hosts[0], frame(ethernet.Broadcast, ethernet.MAC{0x02, 0xAA, 0, 0, 0, i}, i))
		if recv(e.hosts[1], i, wait) == nil {
			t.Fatal("broadcast")
		}
	}
	n := 0
	for i := byte(1); i <= 4; i++ {
		if _, ok := e.b.Learned(ethernet.MAC{0x02, 0xAA, 0, 0, 0, i}); ok {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d stations learned, bound 2", n)
	}
	// Aged out, a new station is learned in their place.
	time.Sleep(250 * time.Millisecond)
	send(t, e.hosts[0], frame(ethernet.Broadcast, hostA, 9))
	recv(e.hosts[1], 9, wait)
	if _, ok := e.b.Learned(hostA); !ok {
		t.Fatal("not learned after ageing")
	}
	e.b.Flush()
	if _, ok := e.b.Learned(hostA); ok {
		t.Fatal("flush")
	}
}

func TestQueueOverflowAndClose(t *testing.T) {
	e := newEnv(t, 1, Config{QueueLen: 2})
	for i := 0; i < 20; i++ {
		send(t, e.hosts[0], frame(cpuMAC, hostA, 1))
	}
	deadline := time.Now().Add(wait)
	for e.b.cpu.Overflow() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if e.b.cpu.Overflow() == 0 {
		t.Fatal("no overflow")
	}
	cpu, loc := e.b.CPU(), e.b.Port(0)
	if err := cpu.Close(); err != nil || cpu.MTU() != 1500 {
		t.Fatal("close of a mux's port stops nothing")
	}
	if err := e.b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = e.b.Close()
	buf := make([]byte, 100)
	for _, p := range []l2.Port{cpu, loc} {
		// Queued frames may come first; then ErrClosed.
		for {
			_, err := p.ReadFrame(context.Background(), buf)
			if err != nil {
				if !errors.Is(err, profinet.ErrClosed) {
					t.Fatal(err)
				}
				break
			}
		}
		if err := p.WriteFrame(context.Background(), frame(hostA, cpuMAC, 1)); !errors.Is(err, profinet.ErrClosed) {
			t.Fatalf("write after close: %v", err)
		}
	}
	if e.b.cpu.deliver([]byte{1}) {
		t.Fatal("delivered to a closed port")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e2 := newEnv(t, 1, Config{})
	if _, err := e2.b.CPU().ReadFrame(ctx, buf); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestLine: three bridges in a line (as three two-port devices),
// hosts at the ends reach each other and every CPU.
func TestLine(t *testing.T) {
	segs := make([]*vnet.Network, 4)
	for i := range segs {
		segs[i] = vnet.New(uint64(10 + i))
		t.Cleanup(func() { _ = segs[i].Close() })
	}
	ep := func(n *vnet.Network, mac ethernet.MAC, prom bool) *vnet.Endpoint {
		e, err := n.NewEndpoint(vnet.EndpointConfig{MAC: mac, Promiscuous: prom})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	left := ep(segs[0], hostA, false)
	right := ep(segs[3], hostB, false)
	var cpus []l2.Port
	for i := 0; i < 3; i++ {
		a := ep(segs[i], ethernet.MAC{0x02, 0xD0, byte(i), 0, 0, 1}, true)
		b := ep(segs[i+1], ethernet.MAC{0x02, 0xD0, byte(i), 0, 0, 2}, true)
		br, err := New(Config{Ports: []l2.Port{a, b}, MAC: ethernet.MAC{0x02, 0xD0, byte(i), 0, 0, 0}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = br.Close() })
		cpus = append(cpus, br.CPU())
	}
	send(t, left, frame(hostB, hostA, 1))
	if f := recv(right, 1, wait); f == nil || !bytes.Equal(f[6:12], hostA[:]) {
		t.Fatal("end to end")
	}
	send(t, right, frame(ethernet.MAC{0x02, 0xD0, 1, 0, 0, 0}, hostB, 2))
	if recv(cpus[1], 2, wait) == nil {
		t.Fatal("to the middle device")
	}
	send(t, cpus[0], frame(hostB, ethernet.MAC{0x02, 0xD0, 0, 0, 0, 0}, 3))
	if recv(right, 3, wait) == nil {
		t.Fatal("from the first device")
	}
}
