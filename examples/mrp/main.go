// SPDX-License-Identifier: MIT

// Command mrp builds a ring of three stations on virtual cables: each has
// a two-port bridge and an MRP entity; the first is the ring manager, the
// others are clients. It cuts a cable, shows the manager open the ring
// (forward on both ports) within the 200 ms profile, and close it again
// when the cable is back.
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/bridge"
	"github.com/otfabric/go-profinet/mrp"
	"github.com/otfabric/go-profinet/vnet"
)

type station struct {
	ports [2]*vnet.Endpoint
	ent   *mrp.Entity
}

func main() {
	const n = 3
	// Cable i joins station i's port 2 and station i+1's port 1.
	cables := make([]*vnet.Network, n)
	for i := range cables {
		cables[i] = vnet.New(uint64(i + 1))
		defer func() { _ = cables[i].Close() }()
	}
	stations := make([]*station, n)
	for i := range stations {
		st := &station{}
		cpu := ethernet.MAC{0x02, 0x10, byte(i), 0, 0, 0}
		for k, c := range []*vnet.Network{cables[(i+n-1)%n], cables[i]} {
			ep, err := c.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{0x02, 0x10, byte(i), 0, 0, byte(k + 1)}, Promiscuous: true})
			if err != nil {
				log.Fatal(err)
			}
			st.ports[k] = ep
		}
		br, err := bridge.New(bridge.Config{Ports: []l2.Port{st.ports[0], st.ports[1]}, MAC: cpu})
		if err != nil {
			log.Fatal(err)
		}
		defer func() { _ = br.Close() }()
		role := mrp.RoleClient
		if i == 0 {
			role = mrp.RoleManager
		}
		ports := [2]*l2.Mux{l2.NewMux(br.Port(0)), l2.NewMux(br.Port(1))}
		st.ent, err = mrp.New(mrp.Config{Role: role, Ports: ports, Index: [2]int{0, 1}, Switch: br, MAC: cpu, Profile: mrp.Profile200})
		if err != nil {
			log.Fatal(err)
		}
		defer func() { _ = st.ent.Close() }()
		stations[i] = st
	}
	mgr := stations[0].ent
	wait := func(what string, open bool) time.Duration {
		start := time.Now()
		for time.Since(start) < 10*time.Second {
			s := mgr.Status()
			if s.RingOpen == open && s.Blocked[1] == !open {
				return time.Since(start)
			}
			time.Sleep(time.Millisecond)
		}
		log.Fatalf("%s: %+v", what, mgr.Status())
		return 0
	}
	wait("ring closed", false)
	s := mgr.Status()
	fmt.Printf("ring closed: manager %s, port 2 blocked %v (no loop)\n", s.State, s.Blocked[1])
	for i, st := range stations[1:] {
		c := st.ent.Status()
		fmt.Printf("  client %d: %s, ports blocked %v\n", i+1, c.State, c.Blocked)
	}

	fmt.Println("cable 1 (station 1 to station 2) cut")
	stations[1].ports[1].SetLinkUp(false)
	stations[2].ports[0].SetLinkUp(false)
	d := wait("ring open", true)
	s = mgr.Status()
	fmt.Printf("ring open after %s (profile limit 200 ms): manager %s, port 2 blocked %v\n", d.Round(10*time.Millisecond), s.State, s.Blocked[1])

	fmt.Println("cable 1 back")
	stations[1].ports[1].SetLinkUp(true)
	stations[2].ports[0].SetLinkUp(true)
	wait("ring closed again", false)
	s = mgr.Status()
	fmt.Printf("ring closed again: manager %s, %d topology changes announced\n", s.State, s.TopologyChanges)
}
