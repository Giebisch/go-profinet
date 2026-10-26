// SPDX-License-Identifier: MIT

// Command device runs the example IO-Device on a real network interface:
//
//	sudo go run ./examples/device -i eth0 -name io-1
//
// It needs the privileges of the raw Ethernet backend (CAP_NET_RAW on
// Linux, the access_bpf group or root on macOS, Npcap on Windows). The
// operating system's IP stack serves RPC on UDP 34964; the device's IP
// address is the interface's. It counts its inputs up once a second and
// prints the outputs it receives. Its GSDML is what examples/gsdml writes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/osport"
	"github.com/otfabric/go-profinet/pnio"
)

func main() {
	iface := flag.String("i", "", "network interface")
	name := flag.String("name", "io-1", "station name")
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
	dev, err := device.New(device.Config{
		Model: vnetenv.Model(), Mux: mux, StationName: *name, IP: ifaceSuite(*iface),
		Logger: profinet.NewStdLogger(nil),
		// On a real interface the operating system owns the address; a DCP
		// Set is recorded and reported, and applying it is left to the
		// administrator.
		ApplyIP: func(s dcp.IPSuite, _ bool) error {
			log.Printf("DCP Set IP %s: configure the interface accordingly", s)
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = dev.Close() }()
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
		_ = dev.SetInput(1, 1, []byte{n, n, n, n, n, n, n, n}, pnio.IOxSGood)
		if out, iops, ok := dev.Output(2, 1); ok && iops == pnio.IOxSGood {
			fmt.Printf("outputs % x\n", out)
		}
	}
}

// ifaceSuite returns the first IPv4 address of the interface.
func ifaceSuite(name string) dcp.IPSuite {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return dcp.IPSuite{}
	}
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			ip, _ := netip.AddrFromSlice(n.IP.To4())
			mask, _ := netip.AddrFromSlice(net.IP(n.Mask).To4())
			return dcp.IPSuite{Addr: ip, Mask: mask, Gateway: netip.IPv4Unspecified()}
		}
	}
	return dcp.IPSuite{}
}
