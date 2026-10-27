// SPDX-License-Identifier: MIT

package e2e

import (
	"encoding/binary"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/vnet"
)

// dcpCounter counts the DCP Identify requests and Hellos on the network.
type dcpCounter struct{ identify, hello atomic.Int64 }

func frameID(frame []byte) (uint16, bool) {
	if len(frame) < 16 {
		return 0, false
	}
	et, off := binary.BigEndian.Uint16(frame[12:]), 14
	if et == uint16(ethernet.EtherTypeVLAN) && len(frame) >= 20 {
		et, off = binary.BigEndian.Uint16(frame[16:]), 18
	}
	if et != uint16(ethernet.EtherTypeProfinet) {
		return 0, false
	}
	return binary.BigEndian.Uint16(frame[off:]), true
}

func (e *env) countDCP() *dcpCounter {
	c := &dcpCounter{}
	e.net.AddTap(func(_ *vnet.Endpoint, frame []byte) {
		switch id, _ := frameID(frame); id {
		case dcp.FrameIDIdentifyRequest:
			c.identify.Add(1)
		case dcp.FrameIDHello:
			c.hello.Add(1)
		}
	})
	return c
}

// helloDevice starts a device that sends DCP Hello.
func (e *env) helloDevice(name, ip string, mode dcp.HelloMode, opts dcp.HelloOptions) (*device.Device, *node) {
	e.t.Helper()
	n := e.node(ip)
	return e.helloDeviceOn(n, name, ip, mode, opts), n
}

// helloDeviceOn starts a device that sends DCP Hello on an existing node
// (a restart keeps the device's MAC address).
func (e *env) helloDeviceOn(n *node, name, ip string, mode dcp.HelloMode, opts dcp.HelloOptions) *device.Device {
	e.t.Helper()
	d, err := device.New(device.Config{
		Model: testModel(), Mux: n.mux, StationName: name, IP: suite(ip), ListenPacket: n.stack.ListenPacket,
		ApplyIP:         func(s dcp.IPSuite, _ bool) error { return n.stack.SetAddr(s.Addr, s.Mask, s.Gateway) },
		NoResponseDelay: true, LLDPInterval: 200 * time.Millisecond,
		Hello: mode, HelloOptions: opts,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = d.Close() })
	return d
}

// slowRetry is a reconnect backoff long enough that only a Hello can
// explain a fast connection.
func slowRetry(name string) controller.DeviceConfig {
	return controller.DeviceConfig{StationName: name, Model: testModel(), ReconnectMin: 20 * time.Second, ReconnectMax: 20 * time.Second}
}

// TestHelloDiscovery: a device that starts after its controller gave up
// on it announces itself, and the controller connects at once, with no
// Identify, instead of waiting out its backoff.
func TestHelloDiscovery(t *testing.T) {
	e := newEnv(t, 61)
	cnt := e.countDCP()
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(slowRetry("hello-dut"))
	if err != nil {
		t.Fatal(err)
	}
	// The first Identify finds nothing: the controller waits 20 s.
	eventually(t, 5*time.Second, "first resolve failed", func() bool { st, err := cd.State(); return st == controller.StateOffline && err != nil })
	identifies := cnt.identify.Load()
	start := time.Now()
	e.helloDevice("hello-dut", "192.168.0.10", dcp.HelloOnLinkUp, dcp.HelloOptions{})
	waitState(t, cd, controller.StateRunning, 5*time.Second)
	t.Logf("device start to Running via Hello: %s (%d Hellos)", time.Since(start), cnt.hello.Load())
	if n := cnt.identify.Load() - identifies; n != 0 {
		t.Fatalf("%d Identify requests after the device started", n)
	}
	if s := cd.Stats(); s.Hellos != 1 || s.Connects != 1 {
		t.Fatalf("stats %+v", s)
	}
	// Once connected, the device sends no further Hellos.
	sent := cnt.hello.Load()
	time.Sleep(300 * time.Millisecond)
	if cnt.hello.Load() != sent {
		t.Fatal("Hellos while connected")
	}
}

// TestHelloIgnored: with IgnoreHello the controller waits out its backoff.
func TestHelloIgnored(t *testing.T) {
	e := newEnv(t, 62)
	n := e.node("192.168.0.1")
	ctrl, err := controller.New(controller.Config{Mux: n.mux, ListenPacket: n.stack.ListenPacket, IgnoreHello: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	cd, err := ctrl.AddDevice(slowRetry("hello-dut"))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "first resolve failed", func() bool { st, err := cd.State(); return st == controller.StateOffline && err != nil })
	e.helloDevice("hello-dut", "192.168.0.10", dcp.HelloOnLinkUp, dcp.HelloOptions{})
	time.Sleep(time.Second)
	if st, _ := cd.State(); st == controller.StateRunning || cd.Stats().Hellos != 0 {
		t.Fatalf("state %s, stats %+v: the Hello was used", st, cd.Stats())
	}
}

// TestHelloLinkFlap: after a link loss long enough to end the AR, the
// device's Hello on link up reconnects it without waiting for the backoff.
func TestHelloLinkFlap(t *testing.T) {
	e := newEnv(t, 63)
	ctrl, _ := e.controller("192.168.0.1")
	_, dn := e.helloDevice("flap-dut", "192.168.0.10", dcp.HelloOnLinkUpDelayed, dcp.HelloOptions{Delay: 50 * time.Millisecond, Interval: 50 * time.Millisecond})
	cfg := slowRetry("flap-dut")
	cfg.ReconnectMin = 300 * time.Millisecond
	cd, err := ctrl.AddDevice(cfg)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	for i := 0; i < 3; i++ {
		dn.ep.SetLinkUp(false)
		eventually(t, 10*time.Second, "AR lost", func() bool { st, _ := cd.State(); return st != controller.StateRunning })
		// The failed reconnects stretch the backoff to 20 s.
		time.Sleep(time.Second)
		dn.ep.SetLinkUp(true)
		// Back long before the 20 s backoff ends: through the device's
		// Hello on link up, or an Identify the controller had already
		// started.
		start := time.Now()
		waitState(t, cd, controller.StateRunning, 5*time.Second)
		t.Logf("flap %d: link up to Running in %s", i+1, time.Since(start))
	}
	if cd.Stats().Hellos == 0 {
		t.Fatal("no reconnect used a Hello")
	}
}

// TestHelloRestart: a restarted device is back in Running through its
// Hello.
func TestHelloRestart(t *testing.T) {
	e := newEnv(t, 64)
	ctrl, _ := e.controller("192.168.0.1")
	d, n := e.helloDevice("rs-dut", "192.168.0.10", dcp.HelloOnLinkUp, dcp.HelloOptions{})
	// After the AR is lost the controller waits 20 s: only the Hello
	// explains a fast reconnect.
	cd, err := ctrl.AddDevice(slowRetry("rs-dut"))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	_ = d.Close()
	eventually(t, 10*time.Second, "waiting to reconnect", func() bool { st, _ := cd.State(); return st == controller.StateOffline })
	before := cd.Stats().Hellos
	start := time.Now()
	e.helloDeviceOn(n, "rs-dut", "192.168.0.10", dcp.HelloOnLinkUp, dcp.HelloOptions{})
	waitState(t, cd, controller.StateRunning, 5*time.Second)
	t.Logf("restart to Running: %s", time.Since(start))
	if cd.Stats().Hellos <= before {
		t.Fatal("reconnected without the Hello")
	}
}

// TestHelloAssignIP: the Hello carries the device's own address; a
// controller that assigns another one sets it with DCP first.
func TestHelloAssignIP(t *testing.T) {
	e := newEnv(t, 65)
	cnt := e.countDCP()
	ctrl, _ := e.controller("192.168.0.1")
	cfg := slowRetry("ip-dut")
	cfg.IP, cfg.AssignIP = suite("192.168.0.42"), true
	cd, err := ctrl.AddDevice(cfg)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "first resolve failed", func() bool { st, err := cd.State(); return st == controller.StateOffline && err != nil })
	identifies := cnt.identify.Load()
	d, _ := e.helloDevice("ip-dut", "192.168.0.10", dcp.HelloOnLinkUp, dcp.HelloOptions{})
	waitState(t, cd, controller.StateRunning, 5*time.Second)
	if cnt.identify.Load() != identifies || cd.Stats().Hellos != 1 || cd.IP().Addr.String() != "192.168.0.42" {
		t.Fatalf("identify %d, stats %+v, IP %s", cnt.identify.Load()-identifies, cd.Stats(), cd.IP())
	}
	if ip := d.IP(); ip.Addr.String() != "192.168.0.42" {
		t.Fatalf("device IP %s", ip)
	}
}

// TestHelloFaults: lost Hellos fall back to Identify; duplicated and
// delayed ones do no harm.
func TestHelloFaults(t *testing.T) {
	isHello := func(f []byte) bool { id, ok := frameID(f); return ok && id == dcp.FrameIDHello }
	for name, f := range map[string]vnet.Faults{
		"all lost":   {Loss: 1, Match: isHello},
		"duplicated": {Duplicate: 1, Match: isHello},
		"delayed":    {Delay: 200 * time.Millisecond, Jitter: 100 * time.Millisecond, Match: isHello},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, 66)
			ctrl, _ := e.controller("192.168.0.1")
			cfg := slowRetry("f-dut")
			cfg.ReconnectMin, cfg.ReconnectMax = 500*time.Millisecond, 500*time.Millisecond
			cd, err := ctrl.AddDevice(cfg)
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, 5*time.Second, "first resolve failed", func() bool { st, err := cd.State(); return st == controller.StateOffline && err != nil })
			n := e.node("192.168.0.10")
			n.ep.SetFaults(f)
			d, err := device.New(device.Config{Model: testModel(), Mux: n.mux, StationName: "f-dut", IP: suite("192.168.0.10"),
				ListenPacket: n.stack.ListenPacket, NoResponseDelay: true, Hello: dcp.HelloOnLinkUp})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = d.Close() }()
			waitState(t, cd, controller.StateRunning, 10*time.Second)
			if name == "all lost" && cd.Stats().Hellos != 0 {
				t.Fatalf("a lost Hello was used: %+v", cd.Stats())
			}
			if s := cd.Stats(); s.Connects != 1 {
				t.Fatalf("stats %+v", s)
			}
		})
	}
}
