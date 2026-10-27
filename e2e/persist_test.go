// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/store"
)

// stopDevice stops a device and its node, as a power cycle does.
func stopDevice(dev *device.Device, n *node) {
	_ = dev.Close()
	_ = n.stack.Close()
	_ = n.mux.Close()
	_ = n.ep.Close()
}

func withStore(s store.Store) func(*device.Config) {
	return func(c *device.Config) { c.Store = s }
}

// dcpTool returns a DCP client on its own node.
func (e *env) dcpTool(ip string) *dcp.Client {
	e.t.Helper()
	n := e.node(ip)
	cli, err := dcp.NewClient(n.mux, dcp.ClientOptions{Timeout: 500 * time.Millisecond})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// readIM1 reads I&M1 of the device at ip implicitly (without an AR).
func (e *env) readIM1(dev *device.Device, ip string) pnio.IM1 {
	e.t.Helper()
	cn := e.node("192.168.0.2")
	conn, err := cn.stack.ListenUDP(0)
	if err != nil {
		e.t.Fatal(err)
	}
	cli := rpc.NewClient(conn, true, rpc.ClientOptions{})
	defer func() { _ = cli.Close() }()
	rc := controller.NewRecordClient(cli, &net.UDPAddr{IP: net.ParseIP(ip), Port: rpc.Port}, dev.ObjectUUID())
	b, err := rc.Read(context.Background(), rpc.NilUUID, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM1}, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	blk, _, err := pnio.ReadBlock(b)
	if err != nil {
		e.t.Fatal(err)
	}
	m, err := pnio.DecodeIM1(blk)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

// TestPersistRestart: a device with a store keeps its permanent station
// name and IP suite and its I&M records across a restart (Close, then a
// new Device on the same store); a temporary name is gone after it; a
// ResetToFactory is kept as well.
func TestPersistRestart(t *testing.T) {
	e := newEnv(t, 21)
	st := store.NewMemory()
	ctx := context.Background()
	tool := e.dcpTool("192.168.0.50")

	dev, n := e.device("factory", "192.168.0.10", testModel(), nil, withStore(st))
	mac := n.mux.HardwareAddr()
	if err := tool.SetName(ctx, mac, "plant-a", true); err != nil {
		t.Fatal(err)
	}
	if err := tool.SetIP(ctx, mac, suite("192.168.0.20"), true); err != nil {
		t.Fatal(err)
	}
	stopDevice(dev, n)

	// Restart: the stored name and suite replace the configured ones.
	dev, n = e.device("factory", "192.168.0.20", testModel(), nil, withStore(st))
	if dev.StationName() != "plant-a" || dev.IP() != suite("192.168.0.20") || dev.StoreError() != nil {
		t.Fatalf("after restart: %q %s %v", dev.StationName(), dev.IP(), dev.StoreError())
	}
	// A temporary name lasts until the restart, and clears the stored one.
	mac = n.mux.HardwareAddr()
	if err := tool.SetName(ctx, mac, "temporary", false); err != nil {
		t.Fatal(err)
	}
	if dev.StationName() != "temporary" {
		t.Fatalf("temporary name %q", dev.StationName())
	}
	stopDevice(dev, n)
	dev, n = e.device("factory", "192.168.0.20", testModel(), nil, withStore(st))
	if dev.StationName() != "" || dev.IP() != suite("192.168.0.20") {
		t.Fatalf("after a temporary name: %q %s", dev.StationName(), dev.IP())
	}

	// I&M1 written in an AR survives the restart.
	mac = n.mux.HardwareAddr()
	if err := tool.SetName(ctx, mac, "plant-b", true); err != nil {
		t.Fatal(err)
	}
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "plant-b", Model: testModel(), ReconnectMin: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	im1 := pnio.IM1{TagFunction: "pump", TagLocation: "hall 3"}
	if err := cd.WriteRecord(ctx, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM1}, im1.Append(nil)); err != nil {
		t.Fatal(err)
	}
	_ = cd.Close()
	stopDevice(dev, n)
	dev, n = e.device("factory", "192.168.0.20", testModel(), nil, withStore(st))
	if got := e.readIM1(dev, "192.168.0.20"); got != im1 {
		t.Fatalf("I&M1 after restart %+v", got)
	}

	// ResetToFactory (communication): an empty name and suite, after the
	// restart too.
	if err := tool.ResetToFactory(ctx, n.mux.HardwareAddr(), dcp.ResetCommunication); err != nil {
		t.Fatal(err)
	}
	stopDevice(dev, n)
	dev, _ = e.device("factory", "192.168.0.20", testModel(), nil, withStore(st))
	if dev.StationName() != "" || !dev.IP().IsZero() {
		t.Fatalf("after reset and restart: %q %s", dev.StationName(), dev.IP())
	}
}

// failingStore refuses every Save.
type failingStore struct{ store.Memory }

func (*failingStore) Save(context.Context, string, []byte) error { return errors.New("disk full") }

// TestPersistFailure: when the store cannot save, a permanent and a
// temporary DCP Set are refused with "resource error" and an I&M write
// with "application write error"; nothing changes.
func TestPersistFailure(t *testing.T) {
	e := newEnv(t, 22)
	ctx := context.Background()
	tool := e.dcpTool("192.168.0.50")
	dev, n := e.device("dut", "192.168.0.10", testModel(), nil, withStore(&failingStore{}))
	mac := n.mux.HardwareAddr()
	var se *dcp.SetError
	for _, err := range []error{
		tool.SetName(ctx, mac, "other", true),
		tool.SetName(ctx, mac, "other", false),
		tool.SetIP(ctx, mac, suite("192.168.0.30"), true),
		tool.ResetToFactory(ctx, mac, dcp.ResetCommunication),
	} {
		if !errors.As(err, &se) || se.Code != dcp.BlockErrorResource {
			t.Fatalf("Set with a failing store: %v", err)
		}
	}
	if dev.StationName() != "dut" || dev.IP() != suite("192.168.0.10") {
		t.Fatalf("changed: %q %s", dev.StationName(), dev.IP())
	}
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel()})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	im1 := pnio.IM1{TagFunction: "x"}
	err = cd.WriteRecord(ctx, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM1}, im1.Append(nil))
	var pe *pnio.StatusError
	if !errors.As(err, &pe) || pe.Status.Code1 != pnio.RWWriteError {
		t.Fatalf("I&M write with a failing store: %v", err)
	}
	if got := e.readIM1(dev, "192.168.0.10"); got != (pnio.IM1{}) {
		t.Fatalf("I&M1 changed: %+v", got)
	}
}

// TestPersistDir: the directory store across restarts: a corrupt current
// version falls back on the previous one; a state that is corrupt beyond
// that, or of an unknown version, is reported by StoreError and the
// device starts from its configuration.
func TestPersistDir(t *testing.T) {
	e := newEnv(t, 23)
	ctx := context.Background()
	tool := e.dcpTool("192.168.0.50")
	path := filepath.Join(t.TempDir(), "state")
	open := func() *store.Dir {
		d, err := store.OpenDir(path, store.DirOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	dev, n := e.device("dut", "192.168.0.10", testModel(), nil, withStore(open()))
	mac := n.mux.HardwareAddr()
	if err := tool.SetName(ctx, mac, "first", true); err != nil {
		t.Fatal(err)
	}
	if err := tool.SetName(ctx, mac, "second", true); err != nil {
		t.Fatal(err)
	}
	stopDevice(dev, n)
	// A torn current version: the previous one is used.
	cur := filepath.Join(path, device.DefaultStoreKey+".v")
	b, err := os.ReadFile(cur)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, b[:len(b)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	dev, n = e.device("dut", "192.168.0.10", testModel(), nil, withStore(open()))
	if dev.StationName() != "first" || dev.StoreError() != nil {
		t.Fatalf("recovered %q %v", dev.StationName(), dev.StoreError())
	}
	stopDevice(dev, n)
	// Both versions corrupt: the configuration, and StoreError.
	for _, f := range []string{cur, cur + ".prev"} {
		if err := os.WriteFile(f, []byte("garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dev, n = e.device("dut", "192.168.0.10", testModel(), nil, withStore(open()))
	if dev.StationName() != "dut" || !errors.Is(dev.StoreError(), store.ErrCorrupt) {
		t.Fatalf("corrupt: %q %v", dev.StationName(), dev.StoreError())
	}
	stopDevice(dev, n)
	// An intact file of an unknown version, and one that is not JSON.
	for _, v := range []string{`{"version":2,"name":"x"}`, `not json`, `{"version":1,"name":"Bad Name!"}`,
		`{"version":1,"ip":{"addr":"::1"}}`, `{"version":1,"im":{"im4":"AAEC"}}`} {
		s := open()
		if err := s.Save(ctx, device.DefaultStoreKey, []byte(v)); err != nil {
			t.Fatal(err)
		}
		dev, n = e.device("dut", "192.168.0.10", testModel(), nil, withStore(s))
		if dev.StationName() != "dut" || dev.StoreError() == nil {
			t.Fatalf("%s: %q %v", v, dev.StationName(), dev.StoreError())
		}
		stopDevice(dev, n)
	}
	// A key that cannot name a value is a configuration error.
	if _, err := device.New(device.Config{Model: testModel(), Mux: e.node("192.168.0.60").mux, Store: open(), StoreKey: "Bad Key"}); err == nil {
		t.Fatal("invalid StoreKey accepted")
	}
}

// TestPersistDHCP: a permanent DCP Set of a DHCP option is kept across a
// restart, a temporary one is not.
func TestPersistDHCP(t *testing.T) {
	e := newEnv(t, 24)
	ctx := context.Background()
	st := store.NewMemory()
	tool := e.dcpTool("192.168.0.50")
	accept := func(c *device.Config) {
		c.SetDHCP = func(dcp.DHCPParameter, bool) dcp.BlockError { return dcp.BlockErrorNone }
	}
	dev, n := e.device("dut", "192.168.0.10", testModel(), nil, withStore(st), accept)
	mac := n.mux.HardwareAddr()
	if err := tool.SetDHCP(ctx, mac, dcp.DHCPClientIDFromMAC(), true); err != nil {
		t.Fatal(err)
	}
	if err := tool.SetDHCP(ctx, mac, dcp.DHCPControlParameter(dcp.DHCPControlOn), false); err != nil {
		t.Fatal(err)
	}
	if len(dev.DHCP()) != 2 {
		t.Fatalf("before restart %v", dev.DHCP())
	}
	stopDevice(dev, n)
	dev, _ = e.device("dut", "192.168.0.10", testModel(), nil, withStore(st), accept)
	got := dev.DHCP()
	if len(got) != 1 || got[0].Code != dcp.DHCPClientIDFromMAC().Code {
		t.Fatalf("after restart %v", got)
	}
}
