// SPDX-License-Identifier: MIT

// Command hello shows DCP Hello: the IO-Controller is configured for a
// device that is not there yet, gives up and waits 30 s before its next
// attempt. The device then starts, announces itself with a DCP Hello, and
// the controller connects at once, with the MAC and IP address from the
// Hello and without an Identify. It needs no privileges.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/vnet"
)

func main() {
	network := vnet.New(1)
	defer func() { _ = network.Close() }()

	cn, err := vnetenv.NewNode(network, 1, "192.168.0.1")
	if err != nil {
		log.Fatal(err)
	}
	defer cn.Close()
	ctrl, err := controller.New(controller.Config{Mux: cn.Mux, ListenPacket: cn.Stack.ListenPacket, StationName: "plc-1"})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	io1, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "io-1", Model: vnetenv.Model(),
		ReconnectMin: 30 * time.Second, ReconnectMax: 30 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	// The first attempt finds nothing.
	for {
		if st, err := io1.State(); st == controller.StateOffline && err != nil {
			fmt.Println("first attempt:", err)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Println("controller: next attempt in 30 s")

	// The device starts with DCP Hello on.
	dn, err := vnetenv.NewNode(network, 10, "192.168.0.10")
	if err != nil {
		log.Fatal(err)
	}
	defer dn.Close()
	m := vnetenv.Model()
	m.Capabilities.DCPHello = true
	start := time.Now()
	dev, err := device.New(device.Config{
		Model: m, Mux: dn.Mux, StationName: "io-1", IP: vnetenv.Suite("192.168.0.10"),
		ListenPacket: dn.Stack.ListenPacket,
		Hello:        dcp.HelloOnLinkUp, // 3 Hellos, 30 ms apart (dcp.HelloOptions)
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = dev.Close() }()
	fmt.Println("device: started, sending DCP Hello")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := io1.WaitState(ctx, controller.StateRunning); err != nil {
		log.Fatal(err)
	}
	s := io1.Stats()
	fmt.Printf("controller: Running %s after the device started (connections through a Hello: %d)\n",
		time.Since(start).Round(time.Millisecond), s.Hellos)
}
