// SPDX-License-Identifier: MIT

// Command discovery starts three IO-Devices on a virtual network, one of
// them without a name or address, finds them with DCP Identify, names and
// addresses the new one explicitly, and shows the LLDP neighbors.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/lldp"
	"github.com/otfabric/go-profinet/vnet"
)

func main() {
	network := vnet.New(2)
	defer network.Close()
	for i, name := range []string{"io-1", "io-2", ""} {
		ip := ""
		if name != "" {
			ip = fmt.Sprintf("192.168.0.%d", 10+i)
		}
		n, err := vnetenv.NewNode(network, byte(10+i), ip)
		if err != nil {
			log.Fatal(err)
		}
		defer n.Close()
		cfg := device.Config{Model: vnetenv.Model(), Mux: n.Mux, StationName: name, ListenPacket: n.Stack.ListenPacket, LLDPInterval: 100 * time.Millisecond,
			ApplyIP: func(s dcp.IPSuite, _ bool) error { return n.Stack.SetAddr(s.Addr, s.Mask, s.Gateway) }}
		if ip != "" {
			cfg.IP = vnetenv.Suite(ip)
		}
		d, err := device.New(cfg)
		if err != nil {
			log.Fatal(err)
		}
		defer d.Close()
	}
	tool, err := vnetenv.NewNode(network, 1, "")
	if err != nil {
		log.Fatal(err)
	}
	defer tool.Close()
	client, err := dcp.NewClient(tool.Mux, dcp.ClientOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	ids, err := client.Identify(ctx, dcp.FilterAll(), dcp.IdentifyOptions{ResponseDelay: 10, Wait: 500 * time.Millisecond})
	if err != nil {
		log.Fatal(err)
	}
	for _, id := range ids {
		fmt.Println("found", id.String())
		if id.NameOfStation == "" {
			// Configuring a device is always an explicit decision.
			if err := client.SetName(ctx, id.MAC, "io-3", false); err != nil {
				log.Fatal(err)
			}
			if err := client.SetIP(ctx, id.MAC, vnetenv.Suite("192.168.0.12"), false); err != nil {
				log.Fatal(err)
			}
			fmt.Println("named", id.MAC, "io-3 at 192.168.0.12")
		}
	}
	id, err := client.IdentifyName(ctx, "io-3", time.Second)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("io-3 now:", id.String())

	agent, err := lldp.NewAgent(tool.Mux, lldp.AgentOptions{Interval: -1})
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()
	time.Sleep(300 * time.Millisecond)
	for _, n := range agent.Neighbors() {
		fmt.Printf("LLDP neighbor %s port %s (%s)\n", n.Frame.ChassisString(), n.Frame.PortString(), n.MAC)
	}
}
