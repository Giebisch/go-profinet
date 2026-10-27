// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/bridge"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/vnet"
)

// multiportModel is testModel with n ports.
func multiportModel(n int) *model.Device {
	m := testModel()
	m.Capabilities.Ports = n
	m.DAP = model.StandardDAP("DAP", 0x00000100, n)
	return m
}

// mpDevice is a multiport device: one bridge port per segment.
type mpDevice struct {
	dev   *device.Device
	br    *bridge.Bridge
	ports []*vnet.Endpoint
}

// segments returns n separate segments, each a cable.
func (e *env) segments(n int) []*vnet.Network {
	out := make([]*vnet.Network, n)
	for i := range out {
		out[i] = vnet.New(uint64(1000 + i))
		net := out[i]
		e.t.Cleanup(func() { _ = net.Close() })
	}
	return out
}

// multiportDevice starts a device with one port on each segment.
func (e *env) multiportDevice(name, ip string, segs ...*vnet.Network) *mpDevice {
	e.t.Helper()
	return e.multiportDeviceMRP(name, ip, nil, segs...)
}

// multiportDeviceMRP starts a device with one port on each segment and
// media redundancy.
func (e *env) multiportDeviceMRP(name, ip string, mrpCfg *device.MRPConfig, segs ...*vnet.Network) *mpDevice {
	e.t.Helper()
	var ports []l2.Port
	var eps []*vnet.Endpoint
	for _, s := range segs {
		ep, err := s.NewEndpoint(vnet.EndpointConfig{Name: name, MAC: e.mac(), Promiscuous: true})
		if err != nil {
			e.t.Fatal(err)
		}
		ports, eps = append(ports, ep), append(eps, ep)
	}
	br, err := bridge.New(bridge.Config{Ports: ports, MAC: e.mac(), Logger: e.log.logger(name + "/bridge")})
	if err != nil {
		e.t.Fatal(err)
	}
	mux := l2.NewMux(br.CPU())
	n := e.stackOn(nil, mux, ip)
	d, err := device.New(device.Config{
		Model: multiportModel(len(segs)), Mux: mux, Bridge: br, StationName: name, IP: suite(ip),
		ListenPacket: n.stack.ListenPacket, NoResponseDelay: true, LLDPInterval: 200 * time.Millisecond, MRP: mrpCfg,
		ApplyIP: func(s dcp.IPSuite, _ bool) error { return n.stack.SetAddr(s.Addr, s.Mask, s.Gateway) },
		Logger:  e.log.logger(name),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		_ = d.Close()
		_ = br.Close()
		for _, ep := range eps {
			_ = ep.Close()
		}
	})
	return &mpDevice{dev: d, br: br, ports: eps}
}

// realPorts reads a device's PDRealData through its AR.
func realPorts(t *testing.T, cd *controller.Device) []pnio.PDPortDataReal {
	t.Helper()
	b, err := cd.ReadRecord(context.Background(), controller.Record{Index: pnio.IndexPDRealData}, 0)
	if err != nil {
		t.Logf("PDRealData: %v", err)
		return nil
	}
	rd, err := pnio.DecodePDRealDataRecord(b)
	if err != nil {
		t.Fatal(err)
	}
	return rd.Ports
}

func peerOf(ports []pnio.PDPortDataReal, n int) (state uint16, chassis, port string) {
	for _, p := range ports {
		if p.Subslot == model.SubslotPort1+uint16(n-1) {
			state = uint16(p.LinkState)
			if len(p.Peers) > 0 {
				// The peer's port ID carries its name of station
				// ("port-001.dev-b") in the default interface mode.
				chassis = p.Peers[0].ChassisID
				port = strings.TrimSuffix(p.Peers[0].PortID, "."+chassis)
			}
		}
	}
	return
}

// TestMultiportLine: a controller, then two-port devices A and B in a
// line. B is reached through A's bridge; each port reports its own
// neighbor; a peer check on a port holds; the topology checks out; a
// cable pulled between A and B takes B offline while A runs on, and B
// comes back with the cable.
func TestMultiportLine(t *testing.T) {
	e := newEnv(t, 300)
	segs := e.segments(3)
	a := e.multiportDevice("dev-a", "192.168.0.10", segs[0], segs[1])
	b := e.multiportDevice("dev-b", "192.168.0.11", segs[1], segs[2])
	ctrl, _ := e.controllerOn(segs[0], "192.168.0.1")
	cfg := func(name string, checks ...controller.PeerCheck) controller.DeviceConfig {
		return controller.DeviceConfig{StationName: name, Model: multiportModel(2), PeerChecks: checks,
			ReconnectMin: 100 * time.Millisecond, ReconnectMax: 300 * time.Millisecond, DataHoldFactor: stallHold, WatchdogFactor: stallHold}
	}
	cdA, err := ctrl.AddDevice(cfg("dev-a"))
	if err != nil {
		t.Fatal(err)
	}
	cdB, err := ctrl.AddDevice(cfg("dev-b", controller.PeerCheck{Subslot: model.SubslotPort1, Station: "dev-a", Port: "port-002"}))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cdA, controller.StateRunning, 10*time.Second)
	waitState(t, cdB, controller.StateRunning, 10*time.Second)

	// Cyclic data with B crosses A's bridge.
	_ = b.dev.SetInput(1, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8}, pnio.IOxSGood)
	_ = cdB.SetOutput(2, 1, []byte{9, 8, 7, 6}, pnio.IOxSGood)
	waitInput(t, cdB, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	waitOutput(t, b.dev, 2, []byte{9, 8, 7, 6})
	if s := a.br.Stats(0); s.Forwarded == 0 {
		t.Fatalf("A forwarded nothing: %+v", s)
	}

	// Each port's own neighbor.
	eventually(t, 5*time.Second, "neighbors", func() bool {
		pa, pb := realPorts(t, cdA), realPorts(t, cdB)
		_, ca, pa2 := peerOf(pa, 2)
		_, cb, pb1 := peerOf(pb, 1)
		stB2, cb2, _ := peerOf(pb, 2)
		return ca == "dev-b" && pa2 == "port-001" && cb == "dev-a" && pb1 == "port-002" && stB2 == uint16(pnio.LinkStateUp) && cb2 == ""
	})
	// B's peer check holds: no remote mismatch.
	if d, err := cdB.ReadDiagnosis(context.Background()); err != nil || len(d) != 0 {
		t.Fatalf("B's diagnosis %+v %v", d, err)
	}
	rep, err := ctrl.CheckTopology(context.Background(), []controller.Link{
		{A: controller.PortRef{Station: "dev-a", Port: "port-002"}, B: controller.PortRef{Station: "dev-b", Port: "port-001"}},
	})
	if err != nil || !rep.OK() {
		t.Fatalf("topology %+v %v", rep, err)
	}

	// The cable between A and B: B goes, A runs on and sees port 2 down.
	a.ports[1].SetLinkUp(false)
	b.ports[0].SetLinkUp(false)
	waitState(t, cdB, controller.StateOffline, 10*time.Second)
	eventually(t, 5*time.Second, "A's port 2 down", func() bool {
		st, c, _ := peerOf(realPorts(t, cdA), 2)
		return st == uint16(pnio.LinkStateDown) && c == ""
	})
	if st, _ := cdA.State(); st != controller.StateRunning {
		t.Fatalf("A %s", st)
	}
	a.ports[1].SetLinkUp(true)
	b.ports[0].SetLinkUp(true)
	waitState(t, cdB, controller.StateRunning, 15*time.Second)
	waitInput(t, cdB, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8})
}

// TestMultiportConfig: a bridge whose ports the model does not declare,
// or a Mux that is not on the bridge, is refused.
func TestMultiportConfig(t *testing.T) {
	e := newEnv(t, 301)
	segs := e.segments(2)
	var ports []l2.Port
	for _, s := range segs {
		ep, err := s.NewEndpoint(vnet.EndpointConfig{MAC: e.mac(), Promiscuous: true})
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, ep)
	}
	br, err := bridge.New(bridge.Config{Ports: ports, MAC: e.mac()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = br.Close() }()
	mux := l2.NewMux(br.CPU())
	defer func() { _ = mux.Close() }()
	if _, err := device.New(device.Config{Model: testModel(), Mux: mux, Bridge: br, StationName: "x"}); err == nil {
		t.Fatal("a one-port model on a two-port bridge")
	}
	other := e.node("")
	if _, err := device.New(device.Config{Model: multiportModel(2), Mux: other.mux, Bridge: br, StationName: "x"}); err == nil {
		t.Fatal("a Mux off the bridge")
	}
}
