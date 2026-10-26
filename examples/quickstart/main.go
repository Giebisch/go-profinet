// SPDX-License-Identifier: MIT

// Command quickstart runs an IO-Controller and an IO-Device on a virtual
// Ethernet network in one process: the controller finds the device with
// DCP, establishes an application relationship, writes a parameter and
// exchanges cyclic data, then releases the AR. It needs no privileges.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/vnet"
)

func main() {
	network := vnet.New(1)
	defer func() { _ = network.Close() }()

	// The IO-Device "io-1" at 192.168.0.10.
	dn, err := vnetenv.NewNode(network, 10, "192.168.0.10")
	if err != nil {
		log.Fatal(err)
	}
	defer dn.Close()
	dev, err := device.New(device.Config{
		Model: vnetenv.Model(), Mux: dn.Mux, StationName: "io-1", IP: vnetenv.Suite("192.168.0.10"),
		ListenPacket: dn.Stack.ListenPacket,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = dev.Close() }()

	// The IO-Controller at 192.168.0.1.
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
	io1, err := ctrl.AddDevice(controller.DeviceConfig{
		StationName: "io-1", Model: vnetenv.Model(),
		Parameters: []controller.Parameter{{Slot: 2, Subslot: 1, Index: 0x0010, Data: []byte{0, 2}}},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := io1.WaitState(ctx, controller.StateRunning); err != nil {
		log.Fatal(err)
	}
	fmt.Println("AR established:", io1.ARUUID())

	// Inputs travel from the device to the controller, outputs back.
	_ = dev.SetInput(1, 1, []byte("PROFINET"), pnio.IOxSGood)
	_ = io1.SetOutput(2, 1, []byte{1, 2, 3, 4}, pnio.IOxSGood)
	for {
		in, _, _ := io1.Input(1, 1)
		out, _, _ := dev.Output(2, 1)
		if bytes.Equal(in, []byte("PROFINET")) && bytes.Equal(out, []byte{1, 2, 3, 4}) {
			fmt.Printf("controller reads %q, device reads % x\n", in, out)
			break
		}
		select {
		case <-ctx.Done():
			log.Fatal("no data")
		case <-time.After(5 * time.Millisecond):
		}
	}
	im0, err := io1.ReadIM0(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("I&M0: order %s serial %s software %s\n", im0.OrderID, im0.SerialNumber, im0.SoftwareRevision)
	fmt.Printf("stats: %+v\n", io1.Stats().Consumer)
}
