// SPDX-License-Identifier: MIT

// Command chaos runs an AR over a virtual network with injected faults
// (loss, duplication, reordering, corruption, jitter), pulls the cable for
// a while and shows the controller re-establish the AR and the counters
// of what happened. The fault injection is seeded: the same seed gives the
// same faults.
package main

import (
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
	network := vnet.New(42)
	defer func() { _ = network.Close() }()
	dn, err := vnetenv.NewNode(network, 10, "192.168.0.10")
	if err != nil {
		log.Fatal(err)
	}
	defer dn.Close()
	dev, err := device.New(device.Config{Model: vnetenv.Model(), Mux: dn.Mux, StationName: "io-1", IP: vnetenv.Suite("192.168.0.10"), ListenPacket: dn.Stack.ListenPacket})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = dev.Close() }()
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
	faults := vnet.Faults{Loss: 0.05, Duplicate: 0.02, Reorder: 0.02, Corrupt: 0.01, Jitter: 2 * time.Millisecond}
	dn.Endpoint.SetFaults(faults)
	cn.Endpoint.SetFaults(faults)
	fmt.Printf("faults on both endpoints: %.0f%% loss, %.0f%% duplicates, %.0f%% reordered, %.0f%% corrupted, %s jitter\n",
		faults.Loss*100, faults.Duplicate*100, faults.Reorder*100, faults.Corrupt*100, faults.Jitter)
	io1, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "io-1", Model: vnetenv.Model(), ReductionRatio: 8, ReconnectMax: 500 * time.Millisecond,
		OnState: func(from, to controller.State, err error) {
			if err != nil {
				fmt.Printf("  %s → %s: %v\n", from, to, err)
			} else {
				fmt.Printf("  %s → %s\n", from, to)
			}
		}})
	if err != nil {
		log.Fatal(err)
	}
	wait := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := io1.WaitState(ctx, controller.StateRunning); err != nil {
			log.Fatal(err)
		}
	}
	wait()
	_ = dev.SetInput(1, 1, []byte("CHAOS-OK"), pnio.IOxSGood)
	time.Sleep(2 * time.Second)
	in, _, _ := io1.Input(1, 1)
	fmt.Printf("inputs through the faults: %q\n", in)
	fmt.Println("cable pulled for 300 ms")
	dn.Endpoint.SetLinkUp(false)
	time.Sleep(300 * time.Millisecond)
	dn.Endpoint.SetLinkUp(true)
	time.Sleep(200 * time.Millisecond)
	wait()
	time.Sleep(time.Second)
	in, _, _ = io1.Input(1, 1)
	st := io1.Stats()
	fmt.Printf("after the cable: %q; %d connects, %d aborts\n", in, st.Connects, st.Aborts)
	fmt.Printf("current AR: %d frames sent, %d received, %d stale, %d malformed\n",
		st.Provider.Sent, st.Consumer.Received, st.Consumer.Stale, st.Consumer.Malformed)
	d, c := dn.Endpoint.Stats(), cn.Endpoint.Stats()
	fmt.Printf("injected: %d dropped, %d duplicated, %d reordered, %d corrupted\n",
		d.Dropped+c.Dropped, d.Duplicated+c.Duplicated, d.Reordered+c.Reordered, d.Corrupted+c.Corrupted)
}
