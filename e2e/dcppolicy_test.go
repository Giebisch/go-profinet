// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/vnet"
)

// auditRecorder collects a device's DCP audit events.
type auditRecorder struct {
	mu sync.Mutex
	ev []dcp.AuditEvent
}

func (a *auditRecorder) add(e dcp.AuditEvent) {
	a.mu.Lock()
	a.ev = append(a.ev, e)
	a.mu.Unlock()
}

func (a *auditRecorder) count(o dcp.AuditOutcome) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, e := range a.ev {
		if e.Outcome == o {
			n++
		}
	}
	return n
}

// TestDCPReadOnly: a device in read-only DCP mode is found and connected
// by a controller as usual, while DCP Set and ResetToFactory from a tool
// are refused, audited, and leave the station and its AR untouched.
func TestDCPReadOnly(t *testing.T) {
	e := newEnv(t, 7)
	audit := &auditRecorder{}
	dev, dn := e.device("dut", "192.168.0.10", testModel(), nil, func(c *device.Config) {
		c.DCPPolicy = dcp.Policy{ReadOnly: true, Audit: audit.add}
	})
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel()})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	ar := cd.ARUUID()

	tool := e.node("192.168.0.50")
	cli, err := dcp.NewClient(tool.mux, dcp.ClientOptions{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	ctx := context.Background()
	mac := dn.mux.HardwareAddr()
	var se *dcp.SetError
	for name, err := range map[string]error{
		"name":  cli.SetName(ctx, mac, "renamed", true),
		"ip":    cli.SetIP(ctx, mac, suite("192.168.0.99"), true),
		"reset": cli.ResetToFactory(ctx, mac, dcp.ResetAllData),
	} {
		if !errors.As(err, &se) || se.Code != dcp.BlockErrorSetNotPossible {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Identify and Get are still answered; Signal still works.
	if id, err := cli.IdentifyName(ctx, "dut", time.Second); err != nil || id.IP.Addr != suite("192.168.0.10").Addr {
		t.Fatalf("identify %+v %v", id, err)
	}
	if err := cli.Signal(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if dev.StationName() != "dut" || dev.IP().Addr != suite("192.168.0.10").Addr {
		t.Fatalf("station changed: %s %s", dev.StationName(), dev.IP())
	}
	if st, err := cd.State(); st != controller.StateRunning || cd.ARUUID() != ar {
		t.Fatalf("AR after the refused Sets: %s %v", st, err)
	}
	if n := audit.count(dcp.AuditReadOnly); n != 3 {
		t.Fatalf("%d read-only audit events", n)
	}
	if n := audit.count(dcp.AuditApplied); n != 1 {
		t.Fatalf("%d applied audit events (Signal)", n)
	}
}

// heapInUse returns the heap in use after a collection.
func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

// TestDCPFlood floods a device with DCP Identify and Set requests from
// random (spoofed) source addresses. The rate limits hold the responses
// within (Sources+1) × (Burst + Rate × duration), memory does not grow
// with the number of sources, the AR keeps running and a tool is answered
// once the flood ends.
func TestDCPFlood(t *testing.T) {
	base := runtime.NumGoroutine()
	func() {
		e := newEnv(t, 99)
		const sources = 16
		idLim, setLim := dcp.RateLimit{Rate: 20, Burst: 4}, dcp.RateLimit{Rate: 5, Burst: 2}
		audit := &auditRecorder{}
		dev, dn := e.device("dut", "192.168.0.10", testModel(), nil, func(c *device.Config) {
			c.DCPPolicy = dcp.Policy{IdentifyLimit: idLim, SetLimit: setLim, Sources: sources, Audit: audit.add}
		})
		devMAC := dn.mux.HardwareAddr()
		var identifyResp, setResp atomic.Int64
		e.net.AddTap(func(_ *vnet.Endpoint, frame []byte) {
			if len(frame) < 18 || ethernet.MAC(frame[6:12]) != devMAC {
				return
			}
			switch id, _ := frameID(frame); {
			case id == dcp.FrameIDIdentifyResponse:
				identifyResp.Add(1)
			case id == dcp.FrameIDGetSet && frame[16] == byte(dcp.ServiceSet):
				setResp.Add(1)
			}
		})
		ctrl, _ := e.controller("192.168.0.1")
		cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), DataHoldFactor: stallHold, WatchdogFactor: stallHold})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, cd, controller.StateRunning, 10*time.Second)
		ar := cd.ARUUID()
		attacker := e.node("")
		rng := rand.New(rand.NewPCG(99, 1))
		ctx := context.Background()
		identify, err := (&dcp.Frame{FrameID: dcp.FrameIDIdentifyRequest, Service: dcp.ServiceIdentify, Type: dcp.TypeRequest, Blocks: dcp.FilterAll()}).AppendTo(nil)
		if err != nil {
			t.Fatal(err)
		}
		signal, err := (&dcp.Frame{FrameID: dcp.FrameIDGetSet, Service: dcp.ServiceSet, Type: dcp.TypeRequest,
			Blocks: []dcp.Block{{ID: dcp.CtrlSignal, HasInfo: true, Data: binary.BigEndian.AppendUint16(nil, dcp.SignalFlashOnce)}}}).AppendTo(nil)
		if err != nil {
			t.Fatal(err)
		}
		flood := func(n int) {
			for i := 0; i < n; i++ {
				src := ethernet.MAC{0x02, byte(rng.Uint32()), byte(rng.Uint32()), byte(rng.Uint32()), byte(rng.Uint32()), byte(rng.Uint32())}
				dst, payload := ethernet.DCPIdentifyMulticast, identify
				if i%2 == 1 {
					dst, payload = devMAC, signal
				}
				binary.BigEndian.PutUint32(payload[4:], uint32(i)) // Xid
				f := ethernet.Frame{Dst: dst, Src: src, EtherType: ethernet.EtherTypeProfinet, Payload: payload}
				_ = attacker.mux.Send(ctx, f.Encode())
				if i%50 == 0 {
					time.Sleep(time.Millisecond)
				}
			}
		}
		start := time.Now()
		flood(2000)
		warm := heapInUse()
		flood(20000)
		grown := heapInUse()
		elapsed := time.Since(start).Seconds() + 0.5 // the responses in flight
		time.Sleep(200 * time.Millisecond)
		bound := func(l dcp.RateLimit) int64 { return int64(float64(sources+1) * (float64(l.Burst) + l.Rate*elapsed)) }
		if n := identifyResp.Load(); n > bound(idLim) || n == 0 {
			t.Fatalf("%d Identify responses in %.1f s (bound %d)", n, elapsed, bound(idLim))
		}
		if n := setResp.Load(); n > bound(setLim) || n == 0 {
			t.Fatalf("%d Set responses in %.1f s (bound %d)", n, elapsed, bound(setLim))
		}
		// 20000 more sources than the warm-up must not cost memory: the
		// tables are of fixed size. The margin absorbs the test's own
		// allocations and the network's queues.
		if grown > warm+8<<20 {
			t.Fatalf("heap grew from %d to %d during the flood", warm, grown)
		}
		if n := audit.count(dcp.AuditRateLimited); n == 0 || n > 22000 {
			t.Fatalf("%d rate-limit audit events", n)
		}
		t.Logf("%d Identify and %d Set responses to 22000 requests in %.1f s; heap %d → %d", identifyResp.Load(), setResp.Load(), elapsed, warm, grown)
		// The AR survived; a tool is answered once the flood is over.
		if st, err := cd.State(); st != controller.StateRunning || cd.ARUUID() != ar {
			t.Fatalf("after the flood: %s %v", st, err)
		}
		tool := e.node("192.168.0.50")
		cli, err := dcp.NewClient(tool.mux, dcp.ClientOptions{})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, 5*time.Second, "Identify answered after the flood", func() bool {
			id, err := cli.IdentifyName(ctx, "dut", 200*time.Millisecond)
			return err == nil && id.MAC == devMAC
		})
		_ = cli.Close()
		_ = e.net.Close()
		_ = ctrl.Close()
		_ = dev.Close()
	}()
	checkLeaks(t, base)
}
