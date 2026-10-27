// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/lldp"
	"github.com/otfabric/go-profinet/netstack"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/snmp"
	"github.com/otfabric/go-profinet/vnet"
)

// TestNoLeaks runs every component through a full session (an AR with
// cyclic data, records, an alarm, a device access AR, DCP, LLDP and SNMP) several
// times, closes everything, and checks that the goroutines return to the
// count before the first run: nothing is left running or blocked on a
// socket.
func TestNoLeaks(t *testing.T) {
	// Not in parallel: other tests' goroutines would be counted.
	settle := func() int {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	before := settle()
	for round := 0; round < 3; round++ {
		session(t, uint64(round))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := settle()
		if n <= before {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("%d goroutines before, %d after closing everything:\n%s", before, n, buf[:runtime.Stack(buf, true)])
		}
	}
}

// session runs one network with a controller and a device, then closes it.
func session(t *testing.T, seed uint64) {
	t.Helper()
	net0 := vnet.New(seed)
	defer func() { _ = net0.Close() }()
	type node struct {
		ep  *vnet.Endpoint
		mux *l2.Mux
		st  *netstack.Stack
	}
	newNode := func(last byte, ip string) node {
		ep, err := net0.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{2, 0, 0, 0, 1, last}})
		if err != nil {
			t.Fatal(err)
		}
		mux := l2.NewMux(ep)
		st, err := netstack.New(mux, netstack.Config{Addr: netip.MustParseAddr(ip), Mask: mask})
		if err != nil {
			t.Fatal(err)
		}
		return node{ep, mux, st}
	}
	closeNode := func(n node) {
		_ = n.st.Close()
		_ = n.mux.Close()
		_ = n.ep.Close()
	}
	dn := newNode(10, "192.168.0.10")
	defer closeNode(dn)
	dev, err := device.New(device.Config{Model: testModel(), Mux: dn.mux, StationName: "dut", IP: suite("192.168.0.10"),
		ListenPacket: dn.st.ListenPacket, NoResponseDelay: true, LLDPInterval: 50 * time.Millisecond,
		SNMP: &device.SNMPConfig{Community: "public"}})
	if err != nil {
		t.Fatal(err)
	}
	cn := newNode(1, "192.168.0.1")
	defer closeNode(cn)
	ctrl, err := controller.New(controller.Config{Mux: cn.mux, ListenPacket: cn.st.ListenPacket, StationName: "plc"})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := lldp.NewAgent(cn.mux, lldp.AgentOptions{Interval: -1})
	if err != nil {
		t.Fatal(err)
	}
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel()})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	ctx := context.Background()
	if _, err := cd.ReadIM0(ctx); err != nil {
		t.Fatal(err)
	}
	// The device enters Data just after the controller is Running.
	eventually(t, 5*time.Second, "AR in Data at the device", func() bool {
		ars := dev.ARs()
		return len(ars) == 1 && ars[0].State == device.ARStateData
	})
	if err := dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: 1, ErrorType: pnio.ChannelErrLineBreak}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.DCP().Identify(ctx, dcp.FilterAll(), dcp.IdentifyOptions{Wait: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	rc := controller.NewRecordClient(ctrl.RPC(), &net.UDPAddr{IP: net.IPv4(192, 168, 0, 10), Port: rpc.Port}, dev.ObjectUUID())
	sar, err := rc.OpenSupervisor(ctx, controller.SupervisorConfig{InitiatorMAC: cn.mux.HardwareAddr(), InitiatorObject: ctrl.ObjectUUID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := sar.Release(ctx); err != nil {
		t.Fatal(err)
	}
	// An SNMP walk of the device.
	sc, err := cn.st.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	(&snmpClient{t: t, conn: sc, dst: &net.UDPAddr{IP: net.IPv4(192, 168, 0, 10), Port: snmp.Port}}).walkBulk()
	_ = sc.Close()
	ag.Close()
	if err := ctrl.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dev.Close(); err != nil {
		t.Fatal(err)
	}
}
