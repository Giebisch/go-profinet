// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/vnet"
)

// TestChaosMonkey drives a controller with three devices through a seeded
// random sequence of disruptions while the ARs run: fault profiles on
// random endpoints, link flaps, device restarts, renames, devices removed
// from and added back to the controller, diagnosis storms, record and
// output traffic. Then the network calms down and every device must be back
// in Running, exchanging fresh data in both directions and answering
// records, and closing everything must leave no goroutine behind.
//
// PROFINET_CHAOS_SEED reproduces a run; PROFINET_MONKEY_STEPS sets the
// number of disruptions (default 40). Every step is logged.
func TestChaosMonkey(t *testing.T) {
	steps := 40
	if s := os.Getenv("PROFINET_MONKEY_STEPS"); s != "" {
		steps, _ = strconv.Atoi(s)
	}
	seed := uint64(1000)
	if s := os.Getenv("PROFINET_CHAOS_SEED"); s != "" {
		seed, _ = strconv.ParseUint(s, 10, 64)
	}
	base := runtime.NumGoroutine()
	func() {
		monkey(t, seed, steps)
	}()
	checkLeaks(t, base)
}

type monkeyDev struct {
	name, ip string
	node     *node
	dev      *device.Device
	cd       *controller.Device
}

func monkey(t *testing.T, seed uint64, steps int) {
	rng := rand.New(rand.NewPCG(seed, seed^0x5eed))
	e := newEnv(t, seed)
	ctrl, cn := e.controller("192.168.0.1")
	newDevice := func(m *monkeyDev) {
		d, err := device.New(device.Config{Model: testModel(), Mux: m.node.mux, StationName: m.name, IP: suite(m.ip),
			ListenPacket: m.node.stack.ListenPacket, NoResponseDelay: true, LLDPInterval: 200 * time.Millisecond,
			ApplyIP: func(s dcp.IPSuite, _ bool) error { return m.node.stack.SetAddr(s.Addr, s.Mask, s.Gateway) }})
		if err != nil {
			t.Fatal(err)
		}
		m.dev = d
	}
	addToController := func(m *monkeyDev) {
		cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: m.name, Model: testModel(), ReductionRatio: 8,
			DataHoldFactor: 10, WatchdogFactor: 10, RPCTimeout: 2 * time.Second, ReconnectMin: 50 * time.Millisecond, ReconnectMax: 500 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		m.cd = cd
	}
	devs := make([]*monkeyDev, 3)
	for i := range devs {
		m := &monkeyDev{name: fmt.Sprintf("monkey-%d", i), ip: fmt.Sprintf("192.168.0.%d", 20+i)}
		m.node = e.node(m.ip)
		newDevice(m)
		addToController(m)
		devs[i] = m
	}
	defer func() {
		for _, m := range devs {
			_ = m.dev.Close()
		}
		_ = ctrl.Close()
		_ = e.net.Close()
	}()
	for _, m := range devs {
		waitState(t, m.cd, controller.StateRunning, 30*time.Second)
	}
	endpoints := []*vnet.Endpoint{cn.ep}
	for _, m := range devs {
		endpoints = append(endpoints, m.node.ep)
	}
	ctx := context.Background()
	for i := 0; i < steps; i++ {
		m := devs[rng.IntN(len(devs))]
		switch k := rng.IntN(9); k {
		case 0:
			p := profiles[rng.IntN(len(profiles))]
			ep := endpoints[rng.IntN(len(endpoints))]
			t.Logf("step %d: faults %s on %s", i, p.name, ep.Name())
			ep.SetFaults(p.faults)
		case 1:
			t.Logf("step %d: calm", i)
			for _, ep := range endpoints {
				ep.SetFaults(vnet.Faults{})
			}
		case 2:
			d := time.Duration(50+rng.IntN(750)) * time.Millisecond
			ep := endpoints[rng.IntN(len(endpoints))]
			t.Logf("step %d: link of %s down for %s", i, ep.Name(), d)
			ep.SetLinkUp(false)
			time.Sleep(d)
			ep.SetLinkUp(true)
		case 3:
			t.Logf("step %d: restart device %s", i, m.name)
			_ = m.dev.Close()
			time.Sleep(time.Duration(rng.IntN(300)) * time.Millisecond)
			newDevice(m)
		case 4:
			t.Logf("step %d: rename %s away and back", i, m.name)
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			if id, err := ctrl.DCP().IdentifyName(cctx, m.name, 300*time.Millisecond); err == nil {
				if err := ctrl.DCP().SetName(cctx, id.MAC, m.name+"-away", false); err == nil {
					time.Sleep(time.Duration(rng.IntN(500)) * time.Millisecond)
					_ = ctrl.DCP().SetName(cctx, id.MAC, m.name, false)
				}
			}
			cancel()
			// A lost Set leaves the device renamed: take the name back.
			if m.dev.StationName() != m.name {
				t.Logf("step %d: %s stayed %q; restarting it", i, m.name, m.dev.StationName())
				_ = m.dev.Close()
				newDevice(m)
			}
		case 5:
			t.Logf("step %d: remove %s from the controller and add it back", i, m.name)
			_ = m.cd.Close()
			addToController(m)
		case 6:
			t.Logf("step %d: diagnosis storm on %s", i, m.name)
			for j := 0; j < 5; j++ {
				_ = m.dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: uint16(j), ErrorType: pnio.ChannelErrOverload})
			}
			for j := 0; j < 5; j++ {
				_ = m.dev.RemoveDiagnosis(ctx, 1, 1, uint16(j), pnio.ChannelErrOverload)
			}
		case 7:
			v := []byte{0, byte(rng.IntN(256))}
			t.Logf("step %d: write record of %s: %x", i, m.name, v)
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_ = m.cd.WriteRecord(cctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, v)
			cancel()
		default:
			v := []byte{byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))}
			t.Logf("step %d: outputs of %s: %x", i, m.name, v)
			_ = m.cd.SetOutput(2, 1, v, pnio.IOxSGood)
		}
		time.Sleep(time.Duration(rng.IntN(400)) * time.Millisecond)
	}

	// Calm: every device recovers and works.
	t.Logf("calm down")
	for _, ep := range endpoints {
		ep.SetFaults(vnet.Faults{})
		ep.SetLinkUp(true)
	}
	for _, m := range devs {
		waitState(t, m.cd, controller.StateRunning, 90*time.Second)
	}
	for n, m := range devs {
		in := []byte{1, 2, 3, 4, 5, 6, 7, byte(n)}
		out := []byte{9, 9, 9, byte(n)}
		if err := m.dev.SetInput(1, 1, in, pnio.IOxSGood); err != nil {
			t.Fatal(err)
		}
		if err := m.cd.SetOutput(2, 1, out, pnio.IOxSGood); err != nil {
			t.Fatal(err)
		}
		eventually(t, 30*time.Second, m.name+" data both ways", func() bool {
			got, iops, _ := m.cd.Input(1, 1)
			o, oiops, _ := m.dev.Output(2, 1)
			return bytes.Equal(got, in) && iops == pnio.IOxSGood && bytes.Equal(o, out) && oiops == pnio.IOxSGood
		})
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := m.cd.WriteRecord(cctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, byte(n + 1)}); err != nil {
			cancel()
			t.Fatalf("%s: write after the chaos: %v", m.name, err)
		}
		if b, err := m.cd.ReadRecord(cctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, 0); err != nil || !bytes.Equal(b, []byte{0, byte(n + 1)}) {
			cancel()
			t.Fatalf("%s: read after the chaos: %x %v", m.name, b, err)
		}
		cancel()
		st := m.cd.Stats()
		t.Logf("%s: %d connects, %d aborts, %d alarms", m.name, st.Connects, st.Aborts, st.Alarms)
	}
}
