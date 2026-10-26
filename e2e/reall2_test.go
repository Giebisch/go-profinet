// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/osport"
	"github.com/otfabric/go-profinet/netstack"
	"github.com/otfabric/go-profinet/pnio"
)

// TestRealL2 runs a controller and a device over the operating system's
// raw Ethernet backend, on the two ends of a link named by
// PROFINET_L2_IFACE_A and PROFINET_L2_IFACE_B (a veth pair in
// scripts/l2-test.sh, or two NICs connected by a cable). RPC runs on the
// module's IP stack over the same ports, so every frame crosses the link.
func TestRealL2(t *testing.T) {
	ia, ib := os.Getenv("PROFINET_L2_IFACE_A"), os.Getenv("PROFINET_L2_IFACE_B")
	if ia == "" || ib == "" {
		t.Skip("real Layer 2 test: set PROFINET_L2_IFACE_A and PROFINET_L2_IFACE_B (see scripts/l2-test.sh)")
	}
	open := func(name, ip string) (*l2.Mux, *netstack.Stack) {
		p, err := osport.Open(name, osport.Options{})
		if err != nil {
			t.Fatal(err)
		}
		mux := l2.NewMux(p)
		st, err := netstack.New(mux, netstack.Config{Addr: netip.MustParseAddr(ip), Mask: mask})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = st.Close()
			_ = mux.Close()
			_ = p.Close()
		})
		return mux, st
	}
	cm, cs := open(ia, "10.99.0.1")
	dm, ds := open(ib, "10.99.0.2")
	dev, err := device.New(device.Config{Model: testModel(), Mux: dm, StationName: "real-l2-dut", IP: suite("10.99.0.2"), ListenPacket: ds.ListenPacket})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dev.Close() }()
	ctrl, err := controller.New(controller.Config{Mux: cm, ListenPacket: cs.ListenPacket})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	// A 1 ms cycle with a data hold time of 200 cycles. The test proves
	// cyclic data over the operating system's raw Ethernet backend and
	// measures its timing; it does not assert hard real time, which a
	// general-purpose OS does not give: a CI runner under the race
	// detector stalls for tens of milliseconds now and then (36 ms seen on
	// a macOS runner, which a 30 ms hold did not survive). See
	// docs/CYCLIC.md.
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "real-l2-dut", Model: testModel(), ReductionRatio: 1, DataHoldFactor: 200, WatchdogFactor: 200})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 15*time.Second)
	_ = dev.SetInput(1, 1, []byte{1, 3, 5, 7, 9, 11, 13, 15}, pnio.IOxSGood)
	_ = cd.SetOutput(2, 1, []byte{2, 4, 6, 8}, pnio.IOxSGood)
	eventually(t, 5*time.Second, "data over real Layer 2", func() bool {
		in, _, _ := cd.Input(1, 1)
		out, _, _ := dev.Output(2, 1)
		return bytes.Equal(in, []byte{1, 3, 5, 7, 9, 11, 13, 15}) && bytes.Equal(out, []byte{2, 4, 6, 8})
	})
	if _, err := cd.ReadIM0(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Run a few seconds at a 1 ms cycle and report the timing.
	time.Sleep(3 * time.Second)
	s := cd.Stats()
	t.Logf("1 ms cycle over %s/%s: provider %+v consumer %+v cycle %+v", ia, ib, s.Provider, s.Consumer, s.Cycle)
	if st, _ := cd.State(); st != controller.StateRunning {
		t.Fatalf("state %s", st)
	}
}
