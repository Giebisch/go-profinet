// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/vnet"
)

// seeds returns the chaos seeds: PROFINET_CHAOS_SEED reproduces one run,
// PROFINET_CHAOS_RUNS sets how many seeds run (default 3).
func seeds(t *testing.T) []uint64 {
	if s := os.Getenv("PROFINET_CHAOS_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return []uint64{v}
	}
	n := 3
	if s := os.Getenv("PROFINET_CHAOS_RUNS"); s != "" {
		n, _ = strconv.Atoi(s)
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = uint64(1000 + i)
	}
	return out
}

// checkLeaks fails when goroutines started by the test outlive it.
func checkLeaks(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	t.Fatalf("goroutine leak: %d running, %d before\n%s", runtime.NumGoroutine(), base, buf[:n])
}

var profiles = []struct {
	name   string
	faults vnet.Faults
}{
	{"loss", vnet.Faults{Loss: 0.05}},
	{"duplicate", vnet.Faults{Duplicate: 0.1}},
	{"reorder", vnet.Faults{Reorder: 0.1, ReorderHold: 2 * time.Millisecond}},
	{"corrupt", vnet.Faults{Corrupt: 0.05}},
	{"heavy-loss", vnet.Faults{Loss: 0.4}},
	{"delay-jitter", vnet.Faults{Delay: time.Millisecond, Jitter: 3 * time.Millisecond}},
	{"everything", vnet.Faults{Loss: 0.02, Duplicate: 0.02, Reorder: 0.02, Corrupt: 0.01, Jitter: time.Millisecond}},
}

// TestChaos runs an AR under impaired links: the AR must reach Running
// (re-establishing it after aborts if needed), the data must arrive, and
// nothing may leak or crash.
func TestChaos(t *testing.T) {
	for _, p := range profiles {
		for _, seed := range seeds(t) {
			t.Run(fmt.Sprintf("%s/seed=%d", p.name, seed), func(t *testing.T) {
				base := runtime.NumGoroutine()
				func() {
					e := newEnv(t, seed)
					dev, dn := e.device("dut", "192.168.0.10", testModel(), nil)
					ctrl, cn := e.controller("192.168.0.1")
					dn.ep.SetFaults(p.faults)
					cn.ep.SetFaults(p.faults)
					cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), ReductionRatio: 8, DataHoldFactor: 10,
						RPCTimeout: 2 * time.Second, ReconnectMax: 200 * time.Millisecond})
					if err != nil {
						t.Fatal(err)
					}
					waitState(t, cd, controller.StateRunning, 90*time.Second)
					_ = dev.SetInput(1, 1, []byte{8, 7, 6, 5, 4, 3, 2, 1}, pnio.IOxSGood)
					eventually(t, 60*time.Second, "inputs under chaos", func() bool {
						in, _, _ := cd.Input(1, 1)
						return bytes.Equal(in, []byte{8, 7, 6, 5, 4, 3, 2, 1})
					})
					// Acyclic services retransmit through the faults.
					// Generous: 40% loss on a loaded CI machine still makes progress.
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					for i := 0; i < 5; i++ {
						if _, err := cd.ReadIM0(ctx); err != nil {
							if st, _ := cd.State(); st != controller.StateRunning {
								waitState(t, cd, controller.StateRunning, 90*time.Second)
								continue
							}
							t.Fatalf("read under chaos: %v", err)
						}
					}
					// Let the cyclic exchange run under the faults for a while.
					time.Sleep(1500 * time.Millisecond)
					st := dn.ep.Stats()
					ct := cn.ep.Stats()
					injected := st.Dropped + st.Duplicated + st.Reordered + st.Corrupted + ct.Dropped + ct.Duplicated + ct.Reordered + ct.Corrupted
					if p.faults.Delay == 0 && p.faults.Jitter == 0 && injected == 0 {
						t.Fatalf("no faults injected: %+v %+v", st, ct)
					}
					t.Logf("device %+v controller %+v; AR aborts %d", st, ct, cd.Stats().Aborts)
					_ = e.net.Close()
					_ = ctrl.Close()
					_ = dev.Close()
				}()
				checkLeaks(t, base)
			})
		}
	}
}

// TestMalformedInput floods a running device and controller with random
// and truncated PROFINET frames and RPC packets; the AR must survive.
func TestMalformedInput(t *testing.T) {
	base := runtime.NumGoroutine()
	func() {
		e := newEnv(t, 42)
		dev, dn := e.device("dut", "192.168.0.10", testModel(), nil)
		ctrl, cn := e.controller("192.168.0.1")
		cd, _ := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), DataHoldFactor: 20})
		waitState(t, cd, controller.StateRunning, 10*time.Second)
		ar := cd.ARUUID()
		attacker := e.node("192.168.0.66")
		rng := rand.New(rand.NewPCG(42, 1))
		targets := []ethernet.MAC{dn.mux.HardwareAddr(), cn.mux.HardwareAddr(), ethernet.DCPIdentifyMulticast, ethernet.LLDPMulticast}
		frameIDs := []uint16{dcp.FrameIDGetSet, dcp.FrameIDIdentifyRequest, dcp.FrameIDIdentifyResponse, pnio.FrameIDAlarmHigh, pnio.FrameIDAlarmLow, 0xC000, 0xC001, 0x8000}
		conn, _ := attacker.stack.ListenUDP(0)
		ctx := context.Background()
		for i := 0; i < 3000; i++ {
			dst := targets[rng.IntN(len(targets))]
			n := rng.IntN(120)
			payload := make([]byte, n)
			for j := range payload {
				payload[j] = byte(rng.Uint32())
			}
			et := ethernet.EtherTypeProfinet
			if rng.IntN(5) == 0 {
				et = ethernet.EtherTypeLLDP
			} else if n >= 2 {
				binary.BigEndian.PutUint16(payload, frameIDs[rng.IntN(len(frameIDs))])
			}
			f := ethernet.Frame{Dst: dst, Src: attacker.mux.HardwareAddr(), EtherType: et, Payload: payload}
			_ = attacker.mux.Send(ctx, f.Encode())
			if i%10 == 0 {
				// Random RPC packets, some with a valid header.
				h := rpc.Header{Type: rpc.PacketType(rng.IntN(11)), DRep: [3]byte{0x10}, Activity: rpc.NewRandomUUID(), Iface: pnio.DeviceInterface, IfaceVersion: 1, OpNum: uint16(rng.IntN(7))}
				body := make([]byte, rng.IntN(200))
				for j := range body {
					body[j] = byte(rng.Uint32())
				}
				if rng.IntN(3) == 0 {
					body = pnio.AppendNDRRequest(nil, binary.LittleEndian, 100, body)
				}
				h.BodyLen = uint16(len(body))
				pkt := append(h.AppendTo(nil), body...)
				if rng.IntN(4) == 0 {
					pkt = pkt[:rng.IntN(len(pkt))]
				}
				port := rpc.Port
				_, _ = conn.WriteTo(pkt, &net.UDPAddr{IP: net.IPv4(192, 168, 0, byte(1+9*rng.IntN(2))), Port: port})
			}
			if i%100 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
		// The AR is the same one, still running.
		if st, err := cd.State(); st != controller.StateRunning || cd.ARUUID() != ar {
			t.Fatalf("after the flood: %s %v", st, err)
		}
		if _, err := cd.ReadIM0(ctx); err != nil {
			t.Fatal(err)
		}
		if len(dev.ARs()) != 1 {
			t.Fatal("device lost its AR")
		}
		_ = e.net.Close()
		_ = ctrl.Close()
		_ = dev.Close()
	}()
	checkLeaks(t, base)
}

// TestReconnectStorm adds and removes devices and controllers repeatedly
// and concurrently.
func TestReconnectStorm(t *testing.T) {
	base := runtime.NumGoroutine()
	func() {
		e := newEnv(t, 77)
		const n = 4
		devs := make([]*device.Device, n)
		for i := range devs {
			devs[i], _ = e.device(fmt.Sprintf("s%d", i), fmt.Sprintf("192.168.0.%d", 10+i), testModel(), nil)
		}
		ctrl, _ := e.controller("192.168.0.1")
		for round := 0; round < 5; round++ {
			done := make(chan error, n)
			for i := 0; i < n; i++ {
				go func(i int) {
					cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: fmt.Sprintf("s%d", i), Model: testModel()})
					if err != nil {
						done <- err
						return
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					err = cd.WaitState(ctx, controller.StateRunning)
					_ = cd.Close()
					done <- err
				}(i)
			}
			for i := 0; i < n; i++ {
				if err := <-done; err != nil {
					t.Fatalf("round %d: %v", round, err)
				}
			}
		}
		for _, d := range devs {
			eventually(t, 5*time.Second, "releases", func() bool { return len(d.ARs()) == 0 })
		}
		_ = ctrl.Close()
		for _, d := range devs {
			_ = d.Close()
		}
		_ = e.net.Close()
	}()
	checkLeaks(t, base)
}
