// SPDX-License-Identifier: MIT

package mrp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/bridge"
	"github.com/otfabric/go-profinet/vnet"
)

// node is a ring station: a two-port bridge with an MRP entity.
type node struct {
	br     *bridge.Bridge
	ent    *Entity
	ports  [2]*vnet.Endpoint
	cpu    *l2.Mux
	cpuMAC ethernet.MAC
	got    atomic.Int64 // test frames received on the CPU
}

// ring builds n stations in a ring: station i's port 2 is cabled to
// station i+1's port 1. Station 0 is the manager.
func ring(t *testing.T, n int, profile Profile) ([]*node, []*vnet.Network) {
	t.Helper()
	segs := make([]*vnet.Network, n)
	for i := range segs {
		segs[i] = vnet.New(uint64(i + 1))
		s := segs[i]
		t.Cleanup(func() { _ = s.Close() })
	}
	nodes := make([]*node, n)
	for i := range nodes {
		nd := &node{cpuMAC: ethernet.MAC{0x02, 0x10, byte(i), 0, 0, 0}}
		// Port 1 on segment i-1 (the cable from the previous station),
		// port 2 on segment i.
		prev := segs[(i+n-1)%n]
		for k, s := range []*vnet.Network{prev, segs[i]} {
			ep, err := s.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{0x02, 0x10, byte(i), 0, 0, byte(k + 1)}, Promiscuous: true})
			if err != nil {
				t.Fatal(err)
			}
			nd.ports[k] = ep
		}
		br, err := bridge.New(bridge.Config{Ports: []l2.Port{nd.ports[0], nd.ports[1]}, MAC: nd.cpuMAC})
		if err != nil {
			t.Fatal(err)
		}
		nd.br = br
		m0, m1 := l2.NewMux(br.Port(0)), l2.NewMux(br.Port(1))
		nd.cpu = l2.NewMux(br.CPU())
		role := RoleClient
		if i == 0 {
			role = RoleManager
		}
		ent, err := New(Config{Role: role, Ports: [2]*l2.Mux{m0, m1}, Index: [2]int{0, 1}, Switch: br, MAC: nd.cpuMAC, Profile: profile})
		if err != nil {
			t.Fatal(err)
		}
		nd.ent = ent
		sub, err := nd.cpu.Subscribe(l2.MatchEtherType(0x88B5), 1024)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				if _, err := sub.Recv(context.Background()); err != nil {
					return
				}
				nd.got.Add(1)
			}
		}()
		t.Cleanup(func() {
			_ = ent.Close()
			sub.Close()
			_ = nd.cpu.Close()
			_ = m0.Close()
			_ = m1.Close()
			_ = br.Close()
		})
		nodes[i] = nd
	}
	return nodes, segs
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func broadcast(t *testing.T, nd *node) {
	t.Helper()
	f := ethernet.Frame{Dst: ethernet.Broadcast, Src: nd.cpuMAC, EtherType: 0x88B5, Payload: []byte{1, 2, 3, 4}}
	if err := nd.cpu.Send(context.Background(), f.Encode()); err != nil {
		t.Fatal(err)
	}
}

// cut takes the cable of segment s down at both ends.
func cut(nodes []*node, s int, up bool) {
	n := len(nodes)
	nodes[s].ports[1].SetLinkUp(up)
	nodes[(s+1)%n].ports[0].SetLinkUp(up)
}

func TestRing(t *testing.T) {
	nodes, _ := ring(t, 3, Profile200)
	mgr := nodes[0].ent
	// The tests come around: closed, the manager's secondary port blocked.
	eventually(t, 5*time.Second, "ring closed", func() bool {
		s := mgr.Status()
		return !s.RingOpen && s.State == StateChkRC && s.Blocked[1] && !s.Blocked[0]
	})
	if s := nodes[1].ent.Status(); s.Blocked[0] || s.Blocked[1] || s.State != StatePTIdle && s.State != StateDEIdle {
		t.Fatalf("client %+v", s)
	}
	// No loop: one broadcast reaches each other station once.
	before := [3]int64{nodes[0].got.Load(), nodes[1].got.Load(), nodes[2].got.Load()}
	broadcast(t, nodes[1])
	time.Sleep(300 * time.Millisecond)
	if d0, d2, d1 := nodes[0].got.Load()-before[0], nodes[2].got.Load()-before[2], nodes[1].got.Load()-before[1]; d0 != 1 || d2 != 1 || d1 != 0 {
		t.Fatalf("broadcast delivered %d, %d (and %d to the sender): a loop or a hole", d0, d2, d1)
	}

	// A cable between the clients breaks: the manager opens the ring and
	// station 2 is reached the other way round.
	start := time.Now()
	cut(nodes, 1, false)
	eventually(t, 5*time.Second, "ring open", func() bool {
		s := mgr.Status()
		return s.RingOpen && !s.Blocked[1] && s.Transitions >= 1
	})
	recovered := false
	for !recovered && time.Since(start) < 5*time.Second {
		b := nodes[2].got.Load()
		broadcast(t, nodes[1])
		time.Sleep(10 * time.Millisecond)
		recovered = nodes[2].got.Load() > b
	}
	if !recovered {
		t.Fatal("station 2 unreachable with the ring open")
	}
	t.Logf("recovery after the cable break: %s (profile %s; measured on vnet, not a claim)", time.Since(start), Profile200.Name)

	// The cable back: closed again, still no loop.
	cut(nodes, 1, true)
	eventually(t, 5*time.Second, "ring closed again", func() bool {
		s := mgr.Status()
		return !s.RingOpen && s.Blocked[1]
	})
	eventually(t, 5*time.Second, "clients forwarding", func() bool {
		s := nodes[2].ent.Status()
		return !s.Blocked[0] && !s.Blocked[1]
	})
	before = [3]int64{nodes[0].got.Load(), nodes[1].got.Load(), nodes[2].got.Load()}
	broadcast(t, nodes[2])
	time.Sleep(300 * time.Millisecond)
	if d0, d1 := nodes[0].got.Load()-before[0], nodes[1].got.Load()-before[1]; d0 != 1 || d1 != 1 {
		t.Fatalf("after closing: %d, %d", d0, d1)
	}
	if s := mgr.Status(); s.TestsSent == 0 || s.TestsReceived == 0 || s.TopologyChanges == 0 || s.Role != RoleManager {
		t.Fatalf("manager %+v", s)
	}
	if s := nodes[1].ent.Status(); s.Forwarded == 0 || s.TopologyChanges == 0 || s.LinkChanges == 0 {
		t.Fatalf("client %+v", s)
	}
}

// TestManagerLink: the manager's own ring port goes down: one port, no
// test can come around, the ring is open; back up, it closes.
func TestManagerLink(t *testing.T) {
	nodes, _ := ring(t, 3, Profile200)
	mgr := nodes[0].ent
	eventually(t, 5*time.Second, "ring closed", func() bool { return !mgr.Status().RingOpen })
	cut(nodes, 0, false) // the manager's port 2 and station 1's port 1
	eventually(t, 5*time.Second, "ring open", func() bool {
		s := mgr.Status()
		return s.RingOpen && s.State == StatePrmUp
	})
	cut(nodes, 0, true)
	eventually(t, 5*time.Second, "ring closed", func() bool {
		s := mgr.Status()
		return !s.RingOpen && s.State == StateChkRC
	})
	cut(nodes, 2, false) // the manager's port 1: port 2 becomes the primary
	eventually(t, 5*time.Second, "ring open", func() bool { return mgr.Status().RingOpen })
	cut(nodes, 2, true)
	eventually(t, 5*time.Second, "ring closed", func() bool { return !mgr.Status().RingOpen })
}

// TestOpenRingStart: a line, not a ring: the manager's tests never come
// back, so it forwards on both ports after TSTNRmax.
func TestOpenRingStart(t *testing.T) {
	segs := []*vnet.Network{vnet.New(1), vnet.New(2)}
	for _, s := range segs {
		t.Cleanup(func() { _ = s.Close() })
	}
	var ports []l2.Port
	for i, s := range segs {
		ep, err := s.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{0x02, 0x20, 0, 0, 0, byte(i + 1)}, Promiscuous: true})
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, ep)
		// A far end that is up and sends nothing back.
		if _, err := s.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{0x02, 0x21, 0, 0, 0, byte(i + 1)}}); err != nil {
			t.Fatal(err)
		}
	}
	br, err := bridge.New(bridge.Config{Ports: ports, MAC: ethernet.MAC{0x02, 0x20, 0, 0, 0, 9}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = br.Close() }()
	m0, m1 := l2.NewMux(br.Port(0)), l2.NewMux(br.Port(1))
	defer func() { _ = m0.Close(); _ = m1.Close() }()
	var changes atomic.Int32
	ent, err := New(Config{Role: RoleManager, Ports: [2]*l2.Mux{m0, m1}, Index: [2]int{0, 1}, Switch: br, MAC: ethernet.MAC{0x02, 0x20, 0, 0, 0, 9},
		OnChange: func() { changes.Add(1) }, Logger: profinet.NopLogger()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ent.Close() }()
	eventually(t, 5*time.Second, "open ring forwarding", func() bool {
		s := ent.Status()
		return s.RingOpen && !s.Blocked[1] && s.State == StateChkRO
	})
	if br.State(1) != bridge.Forwarding || changes.Load() == 0 {
		t.Fatal("bridge state")
	}
}

func TestEntityConfig(t *testing.T) {
	net := vnet.New(1)
	defer func() { _ = net.Close() }()
	ep, _ := net.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{0x02, 0, 0, 0, 0, 1}, Promiscuous: true})
	ep2, _ := net.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{0x02, 0, 0, 0, 0, 2}, Promiscuous: true})
	br, err := bridge.New(bridge.Config{Ports: []l2.Port{ep, ep2}, MAC: ethernet.MAC{0x02, 0, 0, 0, 0, 3}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = br.Close() }()
	m := l2.NewMux(br.Port(0))
	defer func() { _ = m.Close() }()
	mac := ethernet.MAC{0x02, 0, 0, 0, 0, 3}
	for name, c := range map[string]Config{
		"role":       {Role: RoleDisabled, Ports: [2]*l2.Mux{m, m}, Index: [2]int{0, 1}, Switch: br, MAC: mac},
		"no ports":   {Role: RoleClient, Switch: br, MAC: mac},
		"no switch":  {Role: RoleClient, Ports: [2]*l2.Mux{m, m}, Index: [2]int{0, 1}, MAC: mac},
		"no MAC":     {Role: RoleClient, Ports: [2]*l2.Mux{m, m}, Index: [2]int{0, 1}, Switch: br},
		"same index": {Role: RoleClient, Ports: [2]*l2.Mux{m, m}, Switch: br, MAC: mac},
	} {
		if _, err := New(c); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for r, want := range map[Role]string{RoleClient: "client", RoleManager: "manager", RoleDisabled: "disabled"} {
		if r.String() != want {
			t.Errorf("%d: %s", r, r)
		}
	}
}
