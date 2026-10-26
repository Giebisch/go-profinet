// SPDX-License-Identifier: MIT

// Command controller runs an IO-Controller on a real network interface and
// connects the example device (examples/device, or any device with the same
// configuration):
//
//	sudo go run ./examples/controller -i eth0 -device io-1
//
// It prints the inputs and writes a counter to the outputs.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/osport"
	"github.com/otfabric/go-profinet/pnio"
)

func main() {
	iface := flag.String("i", "", "network interface")
	name := flag.String("device", "io-1", "station name of the device")
	cycle := flag.Uint("rr", 4, "reduction ratio (cycle = 1 ms × rr)")
	flag.Parse()
	if *iface == "" {
		log.Fatal("-i is required")
	}
	port, err := osport.Open(*iface, osport.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = port.Close() }()
	mux := l2.NewMux(port)
	defer func() { _ = mux.Close() }()
	ctrl, err := controller.New(controller.Config{Mux: mux, Logger: profinet.NewStdLogger(nil)})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	d, err := ctrl.AddDevice(controller.DeviceConfig{StationName: *name, Model: vnetenv.Model(), ReductionRatio: uint16(*cycle),
		OnState: func(from, to controller.State, err error) { log.Printf("%s: %s → %s %v", *name, from, to, err) }})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var n byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n++
		_ = d.SetOutput(2, 1, []byte{n, 0, 0, n}, pnio.IOxSGood)
		if in, iops, _ := d.Input(1, 1); iops == pnio.IOxSGood {
			fmt.Printf("inputs % x\n", in)
		}
	}
}
