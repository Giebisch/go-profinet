// SPDX-License-Identifier: MIT

// Package e2e runs IO-Controllers against IO-Devices of this module on the
// virtual Ethernet network of package vnet: every message, RPC included,
// is a serialized Ethernet frame. The tests need no privileges.
package e2e

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	profinet "github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/netstack"
	"github.com/otfabric/go-profinet/pcap"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/vnet"
)

var mask = netip.MustParseAddr("255.255.255.0")

// testModel returns a device with an 8-octet input module in slot 1, a
// 4-octet output module with a parameter record in slot 2 and a 2-in /
// 2-out module in slot 3.
func testModel() *model.Device {
	return &model.Device{
		Identity: model.Identity{
			VendorID: 0xF000, DeviceID: 0x0042, VendorName: "OT Fabric", TypeOfStation: "go-profinet e2e",
			OrderID: "OTF-E2E-1", SerialNumber: "0001", HardwareRevision: 1,
			SoftwareRevision: pnio.SoftwareRevision{Prefix: 'V', FunctionalEnhance: 1},
		},
		Capabilities: model.Capabilities{MaxARs: 2, MinDeviceInterval: 32, ConformanceClass: "A", Ports: 1},
		DAP:          model.StandardDAP("DAP", 0x00000100, 1),
		Slots: []model.Slot{
			{Number: 1, Module: model.Module{Name: "DI8", IdentNumber: 0x10, Submodules: []model.Submodule{{Name: "DI8", IdentNumber: 0x11, Subslot: 1, InputLen: 8}}}},
			{Number: 2, Module: model.Module{Name: "DO4", IdentNumber: 0x20, Submodules: []model.Submodule{{Name: "DO4", IdentNumber: 0x21, Subslot: 1, OutputLen: 4,
				Records: []model.Record{{Index: 0x0010, Name: "Mode", Length: 2, Default: []byte{0, 1}}}}}}},
			{Number: 3, Module: model.Module{Name: "IO2", IdentNumber: 0x30, Submodules: []model.Submodule{{Name: "IO2", IdentNumber: 0x31, Subslot: 1, InputLen: 2, OutputLen: 2}}}},
		},
	}
}

// node is one endpoint of the virtual network with its IP stack.
type node struct {
	ep    *vnet.Endpoint
	mux   *l2.Mux
	stack *netstack.Stack
}

type env struct {
	t   *testing.T
	net *vnet.Network
	mu  sync.Mutex
	n   byte
	log *testLog
}

// testLog keeps the last lines the devices and controllers of a test log,
// printed when the test fails.
type testLog struct {
	mu    sync.Mutex
	start time.Time
	lines []string
}

const testLogLines = 2000

func (l *testLog) logger(name string) profinet.Logger { return nodeLog{l, name} }

func (l *testLog) add(name, level, f string, a ...any) {
	line := fmt.Sprintf("%8.3fs %-5s %s: %s", time.Since(l.start).Seconds(), level, name, fmt.Sprintf(f, a...))
	l.mu.Lock()
	if len(l.lines) == testLogLines {
		l.lines = l.lines[1:]
	}
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *testLog) dump(t *testing.T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		t.Log(line)
	}
}

type nodeLog struct {
	l    *testLog
	name string
}

func (n nodeLog) Debugf(f string, a ...any) { n.l.add(n.name, "DEBUG", f, a...) }
func (n nodeLog) Infof(f string, a ...any)  { n.l.add(n.name, "INFO", f, a...) }
func (n nodeLog) Warnf(f string, a ...any)  { n.l.add(n.name, "WARN", f, a...) }
func (n nodeLog) Errorf(f string, a ...any) { n.l.add(n.name, "ERROR", f, a...) }

func newEnv(t *testing.T, seed uint64) *env {
	t.Helper()
	e := &env{t: t, net: vnet.New(seed), log: &testLog{start: time.Now()}}
	if dir := os.Getenv("PROFINET_PCAP_DIR"); dir != "" {
		f, err := os.Create(fmt.Sprintf("%s/%s.pcap", dir, sanitize(t.Name())))
		if err == nil {
			w, err := pcap.NewWriter(f)
			if err == nil {
				e.net.AddTap(func(_ *vnet.Endpoint, frame []byte) { _ = w.WriteFrame(time.Now(), frame) })
			}
			t.Cleanup(func() { _ = f.Close() })
		}
	}
	t.Cleanup(func() {
		_ = e.net.Close()
		if t.Failed() {
			e.log.dump(t)
			t.Logf("vnet seed %d", seed)
		}
	})
	return e
}

func sanitize(s string) string {
	b := []byte(s)
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			b[i] = '_'
		}
	}
	return string(b)
}

func (e *env) node(ip string) *node {
	e.t.Helper()
	e.mu.Lock()
	e.n++
	n := e.n
	e.mu.Unlock()
	ep, err := e.net.NewEndpoint(vnet.EndpointConfig{Name: ip, MAC: ethernet.MAC{0x02, 0x00, 0x00, 0x00, 0x00, n}})
	if err != nil {
		e.t.Fatal(err)
	}
	mux := l2.NewMux(ep)
	cfg := netstack.Config{}
	if ip != "" {
		cfg.Addr, cfg.Mask = netip.MustParseAddr(ip), mask
	}
	st, err := netstack.New(mux, cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		_ = st.Close()
		_ = mux.Close()
		_ = ep.Close()
	})
	return &node{ep: ep, mux: mux, stack: st}
}

func suite(ip string) dcp.IPSuite {
	return dcp.IPSuite{Addr: netip.MustParseAddr(ip), Mask: mask, Gateway: netip.MustParseAddr("0.0.0.0")}
}

func (e *env) device(name, ip string, m *model.Device, h device.Handler, opts ...func(*device.Config)) (*device.Device, *node) {
	e.t.Helper()
	n := e.node(ip)
	cfg := device.Config{
		Model: m, Mux: n.mux, StationName: name, ListenPacket: n.stack.ListenPacket,
		ApplyIP: func(s dcp.IPSuite, _ bool) error {
			return n.stack.SetAddr(s.Addr, s.Mask, s.Gateway)
		},
		Handler: h, NoResponseDelay: true, LLDPInterval: 200 * time.Millisecond,
		Logger: e.log.logger(name),
	}
	if ip != "" {
		cfg.IP = suite(ip)
	}
	for _, o := range opts {
		o(&cfg)
	}
	d, err := device.New(cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = d.Close() })
	return d, n
}

func (e *env) controller(ip string) (*controller.Controller, *node) {
	e.t.Helper()
	n := e.node(ip)
	c, err := controller.New(controller.Config{Mux: n.mux, ListenPacket: n.stack.ListenPacket, StationName: "plc", Logger: e.log.logger("plc@" + ip)})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = c.Close() })
	return c, n
}

func waitState(t *testing.T, d *controller.Device, s controller.State, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := d.WaitState(ctx, s); err != nil {
		st, last := d.State()
		t.Fatalf("%s: waiting for %s: in %s (last error %v)", d.Name(), s, st, last)
	}
}

// eventually polls cond until it holds or the timeout elapses.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
