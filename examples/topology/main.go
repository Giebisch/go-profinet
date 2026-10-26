// SPDX-License-Identifier: MIT

// Command topology starts three IO-Devices that learn their neighbors with
// LLDP, then reads the port data (PDRealData, index 0xF841) of each with
// ReadImplicit, as an engineering tool draws the topology. The virtual
// network is one segment that floods LLDP, like a hub: every device sees
// the other two at its port. Behind a switch each port sees one neighbor.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/vnet"
)

func main() {
	network := vnet.New(5)
	defer func() { _ = network.Close() }()
	for i, name := range []string{"io-1", "io-2", "io-3"} {
		ip := fmt.Sprintf("192.168.0.%d", 10+i)
		n, err := vnetenv.NewNode(network, byte(10+i), ip)
		if err != nil {
			log.Fatal(err)
		}
		defer n.Close()
		d, err := device.New(device.Config{Model: vnetenv.Model(), Mux: n.Mux, StationName: name, IP: vnetenv.Suite(ip),
			ListenPacket: n.Stack.ListenPacket, LLDPInterval: 100 * time.Millisecond})
		if err != nil {
			log.Fatal(err)
		}
		defer func() { _ = d.Close() }()
	}
	cn, err := vnetenv.NewNode(network, 1, "192.168.0.1")
	if err != nil {
		log.Fatal(err)
	}
	defer cn.Close()
	ctrl, err := controller.New(controller.Config{Mux: cn.Mux, ListenPacket: cn.Stack.ListenPacket})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	time.Sleep(500 * time.Millisecond) // a few LLDP rounds
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ids, err := ctrl.DCP().Identify(ctx, dcp.FilterAll(), dcp.IdentifyOptions{Wait: 500 * time.Millisecond})
	if err != nil {
		log.Fatal(err)
	}
	for _, id := range ids {
		rc := controller.NewRecordClient(ctrl.RPC(), &net.UDPAddr{IP: id.IP.Addr.AsSlice(), Port: rpc.Port},
			pnio.ObjectUUID(max(id.Instance, 1), id.DeviceID, id.VendorID))
		b, err := rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexPDRealData}, 0)
		if err != nil {
			log.Fatal(err)
		}
		ports, err := pnio.DecodePDRealData(b)
		if err != nil {
			log.Fatal(err)
		}
		for _, p := range ports {
			fmt.Printf("%s %s (link %d):", id.NameOfStation, p.OwnPortID, p.LinkState)
			for _, peer := range p.Peers {
				fmt.Printf(" %s/%s", peer.ChassisID, peer.PortID)
			}
			fmt.Println()
		}
	}
}
