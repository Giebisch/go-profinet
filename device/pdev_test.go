// SPDX-License-Identifier: MIT

package device

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/lldp"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

// pdevPeer is a device access AR of a raw controller, for PDev records.
type pdevPeer struct {
	t  *testing.T
	p  *peer
	ar rpc.UUID
}

func newPDevPeer(t *testing.T, n *tnet, d *Device) *pdevPeer {
	p := n.peer(d, "192.168.0.2", true)
	sar := p.ioSAR()
	if _, err := p.connect(sar); err != nil {
		t.Fatal(err)
	}
	return &pdevPeer{t: t, p: p, ar: sar.AR.ARUUID}
}

func (x *pdevPeer) read(subslot uint16, idx pnio.Index) ([]byte, error) {
	return x.p.rc.Read(context.Background(), x.ar, controller.Record{Slot: 0, Subslot: subslot, Index: idx}, 0)
}

func (x *pdevPeer) write(subslot uint16, idx pnio.Index, data []byte) error {
	return x.p.rc.Write(context.Background(), x.ar, controller.Record{Slot: 0, Subslot: subslot, Index: idx}, data)
}

func check(subslot uint16, peers ...pnio.CheckPeer) []byte {
	c := pnio.PDPortDataCheck{Subslot: subslot, Peers: peers}
	return c.Append(nil)
}

func TestPDevRecords(t *testing.T) {
	n := newNet(t)
	hellos := countHellos(n)
	d, _ := n.device(nil, func(c *Config) {
		c.Hello = dcp.HelloOnLinkUp
		c.HelloOptions = dcp.HelloOptions{Count: 1}
	})
	eventually(t, 5*time.Second, "Hello at start", func() bool { return hellos.Load() == 1 })
	x := newPDevPeer(t, n, d)
	const port = model.SubslotPort1

	// Nothing written: empty records.
	for _, idx := range []pnio.Index{pnio.IndexPDPortDataCheck, pnio.IndexPDPortDataAdjust} {
		if b, err := x.read(port, idx); err != nil || len(b) != 0 {
			t.Fatalf("%v: % x %v", idx, b, err)
		}
	}
	if b, err := x.read(0, pnio.IndexPDExpectedData); err != nil || len(b) != 0 {
		t.Fatalf("PDExpectedData % x %v", b, err)
	}

	// A peer check is stored and read back.
	c := check(port, pnio.CheckPeer{PortID: "port-001", ChassisID: "plc"})
	if err := x.write(port, pnio.IndexPDPortDataCheck, c); err != nil {
		t.Fatal(err)
	}
	if b, err := x.read(port, pnio.IndexPDPortDataCheck); err != nil || !bytes.Equal(b, c) {
		t.Fatalf("read back % x %v", b, err)
	}
	// Refused: another submodule, a block for another port, two peers,
	// checks the device does not implement, garbage.
	wantStatus(t, x.write(model.SubslotInterface, pnio.IndexPDPortDataCheck, c), pnio.RWInvalidIndex, 0)
	wantStatus(t, x.write(port, pnio.IndexPDPortDataCheck, check(0x8002, pnio.CheckPeer{PortID: "p"})), pnio.RWInvalidParameter, 0)
	wantStatus(t, x.write(port, pnio.IndexPDPortDataCheck, check(port, pnio.CheckPeer{PortID: "a"}, pnio.CheckPeer{PortID: "b"})), pnio.RWInvalidParameter, 0)
	mau := pnio.PDPortDataCheck{Subslot: port, Other: []pnio.RawBlock{{Header: pnio.BlockHeader{Type: pnio.BlockCheckMAUType, VersionHigh: 1}, Body: []byte{0, 0x10}}}}
	wantStatus(t, x.write(port, pnio.IndexPDPortDataCheck, mau.Append(nil)), pnio.RWFeatureNotSupport, 0)
	wantStatus(t, x.write(port, pnio.IndexPDPortDataCheck, []byte{1, 2, 3}), pnio.RWWriteLength, 0)
	wantStatus(t, x.write(port, pnio.IndexPDPortDataCheck, append(c, 0, 0, 0, 0)), pnio.RWWriteLength, 0)
	wantStatus(t, x.write(port, pnio.IndexPDPortDataCheck, append(append([]byte(nil), c...), 0, 1)), pnio.RWWriteLength, 0)
	wantStatus(t, x.write(port, pnio.IndexPDPortDataCheck, (&pnio.IM1{}).Append(nil)), pnio.RWInvalidParameter, 0)
	// Real data is read-only.
	wantStatus(t, x.write(port, pnio.IndexPDPortDataReal, c), pnio.RWAccessDenied, 0)

	// Adjustments: no LLDP, no DCP Hello.
	if !d.ports[0].lldpA.Transmitting() {
		t.Fatal("LLDP off before any adjustment")
	}
	a := pnio.PDPortDataAdjust{Subslot: port, PeerToPeerBoundary: pnio.PeerToPeerNoLLDP, HasPeerToPeerBoundary: true,
		DCPBoundary: pnio.DCPBoundaryHello, HasDCPBoundary: true}
	if err := x.write(port, pnio.IndexPDPortDataAdjust, a.Append(nil)); err != nil {
		t.Fatal(err)
	}
	if d.ports[0].lldpA.Transmitting() {
		t.Fatal("LLDP still sent")
	}
	d.startHello()
	time.Sleep(100 * time.Millisecond)
	if hellos.Load() != 1 {
		t.Fatal("Hello despite the DCP boundary")
	}
	if b, err := x.read(port, pnio.IndexPDPortDataAdjust); err != nil || !bytes.Equal(b, a.Append(nil)) {
		t.Fatalf("adjust read back % x %v", b, err)
	}
	// Refused: unknown bits, adjustments the device cannot honour.
	bad := a
	bad.PeerToPeerBoundary = 1 << 7
	wantStatus(t, x.write(port, pnio.IndexPDPortDataAdjust, bad.Append(nil)), pnio.RWInvalidParameter, 0)
	bad = a
	bad.DCPBoundary = 1 << 5
	wantStatus(t, x.write(port, pnio.IndexPDPortDataAdjust, bad.Append(nil)), pnio.RWInvalidParameter, 0)
	mauAdj := pnio.PDPortDataAdjust{Subslot: port, Other: []pnio.RawBlock{{Header: pnio.BlockHeader{Type: pnio.BlockAdjustMAUType, VersionHigh: 1}, Body: []byte{0, 0, 0, 0x10, 0, 0}}}}
	wantStatus(t, x.write(port, pnio.IndexPDPortDataAdjust, mauAdj.Append(nil)), pnio.RWFeatureNotSupport, 0)
	wantStatus(t, x.write(port, pnio.IndexPDPortDataAdjust, (&pnio.PDPortDataAdjust{Subslot: 0x8009}).Append(nil)), pnio.RWInvalidParameter, 0)
	wantStatus(t, x.write(0x8009, pnio.IndexPDPortDataAdjust, a.Append(nil)), pnio.RWInvalidSlot, 0)

	// PDExpectedData holds the check and the adjustment.
	b, err := x.read(0, pnio.IndexPDExpectedData)
	if err != nil || !bytes.Contains(b, c) || !bytes.Contains(b, a.Append(nil)) {
		t.Fatalf("PDExpectedData % x %v", b, err)
	}
	// An empty adjustment record removes the adjustments.
	if err := x.write(port, pnio.IndexPDPortDataAdjust, (&pnio.PDPortDataAdjust{Subslot: port}).Append(nil)); err != nil {
		t.Fatal(err)
	}
	if !d.ports[0].lldpA.Transmitting() {
		t.Fatal("LLDP not sent again")
	}
	d.startHello()
	eventually(t, 5*time.Second, "Hello again", func() bool { return hellos.Load() == 2 })
	// An empty check record removes the check.
	if err := x.write(port, pnio.IndexPDPortDataCheck, check(port)); err != nil {
		t.Fatal(err)
	}
	if b, _ := x.read(port, pnio.IndexPDPortDataCheck); len(b) != 0 {
		t.Fatalf("check after removal % x", b)
	}

	// The interface: PDInterfaceDataReal, alone and in PDRealData.
	b, err = x.read(model.SubslotInterface, pnio.IndexPDInterfaceDataReal)
	if err != nil {
		t.Fatal(err)
	}
	blk, _, _ := pnio.ReadBlock(b)
	ifd, err := pnio.DecodePDInterfaceDataReal(blk)
	if err != nil || ifd.ChassisID != "dut" || ifd.MAC != d.mux.HardwareAddr() || ifd.IP.String() != devIP {
		t.Fatalf("%+v %v", ifd, err)
	}
	if _, err := x.read(port, pnio.IndexPDInterfaceDataReal); err == nil {
		t.Fatal("PDInterfaceDataReal of a port")
	}
	b, err = x.read(0, pnio.IndexPDRealData)
	rd, derr := pnio.DecodePDRealDataRecord(b)
	if err != nil || derr != nil || rd.Interface == nil || rd.Interface.ChassisID != "dut" || rd.InterfaceSubslot != model.SubslotInterface || len(rd.Ports) != 1 {
		t.Fatalf("%+v %v %v", rd, err, derr)
	}
}

// lldpPeer is an LLDP neighbor of the device.
func lldpPeer(t *testing.T, n *tnet, name, port string) *lldp.Agent {
	nd := n.node("")
	a, err := lldp.NewAgent(nd.mux, lldp.AgentOptions{Interval: 100 * time.Millisecond, TTLFactor: 3,
		Local: func() lldp.LocalInfo { return lldp.LocalInfo{StationName: name, PortName: port} }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func TestPDevInterfaceAdjust(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil, func(c *Config) { c.LLDPInterval = 100 * time.Millisecond })
	x := newPDevPeer(t, n, d)
	// A receiver sees the device's LLDP port ID.
	rx := n.node("")
	agent, err := lldp.NewAgent(rx.mux, lldp.AgentOptions{Interval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	portID := func() string {
		nb, ok := agent.Latest()
		if !ok {
			return ""
		}
		return nb.Frame.PortString()
	}
	eventually(t, 5*time.Second, "standard port ID", func() bool { return portID() == "port-001.dut" })
	b, err := x.read(model.SubslotInterface, pnio.IndexPDInterfaceAdjust)
	blk, _, _ := pnio.ReadBlock(b)
	if ia, derr := pnio.DecodePDInterfaceAdjust(blk); err != nil || derr != nil || ia.MultipleInterfaceMode != pnio.InterfaceModeNameOfDevice {
		t.Fatalf("%+v %v %v", ia, err, derr)
	}
	if err := x.write(model.SubslotInterface, pnio.IndexPDInterfaceAdjust, (&pnio.PDInterfaceAdjust{}).Append(nil)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "legacy port ID", func() bool { return portID() == "port-001" })
	wantStatus(t, x.write(model.SubslotInterface, pnio.IndexPDInterfaceAdjust, (&pnio.PDInterfaceAdjust{MultipleInterfaceMode: 6}).Append(nil)), pnio.RWInvalidParameter, 0)
	wantStatus(t, x.write(model.SubslotPort1, pnio.IndexPDInterfaceAdjust, (&pnio.PDInterfaceAdjust{}).Append(nil)), pnio.RWInvalidIndex, 0)
	if _, err := x.read(model.SubslotPort1, pnio.IndexPDInterfaceAdjust); err == nil {
		t.Fatal("PDInterfaceAdjust of a port")
	}
	if _, err := x.read(model.SubslotInterface, pnio.IndexPDPortDataCheck); err == nil {
		t.Fatal("PDPortDataCheck of the interface")
	}
	b, err = x.read(0, pnio.IndexPDExpectedData)
	if err != nil || !bytes.Contains(b, (&pnio.PDInterfaceAdjust{}).Append(nil)) {
		t.Fatalf("PDExpectedData % x %v", b, err)
	}
}

// mismatch returns the extended error types of the port's remote
// mismatch diagnoses.
func mismatch(d *Device) []uint16 {
	var out []uint16
	for _, it := range d.Diagnoses(0, model.SubslotPort1) {
		if it.ErrorType == pnio.ChannelErrRemoteMismat && it.ChannelNumber == pdevChannel {
			out = append(out, it.ExtErrorType)
		}
	}
	return out
}

func waitMismatch(t *testing.T, d *Device, want ...uint16) {
	t.Helper()
	eventually(t, 5*time.Second, "remote mismatch", func() bool { return equalTypes(mismatch(d), want) })
}

func TestPDevPeerCheck(t *testing.T) {
	n := newNet(t)
	d, nd := n.device(nil)
	x := newPDevPeer(t, n, d)
	const port = model.SubslotPort1
	sw := lldpPeer(t, n, "sw", "port-003")
	eventually(t, 5*time.Second, "neighbor", func() bool { _, ok := d.ports[0].lldpA.Latest(); return ok })

	write := func(peer pnio.CheckPeer) {
		t.Helper()
		if err := x.write(port, pnio.IndexPDPortDataCheck, check(port, peer)); err != nil {
			t.Fatal(err)
		}
	}
	// The right peer: no diagnosis.
	write(pnio.CheckPeer{PortID: "port-003", ChassisID: "sw"})
	time.Sleep(200 * time.Millisecond)
	waitMismatch(t, d)
	// Wrong name of station, wrong port, both.
	write(pnio.CheckPeer{PortID: "port-003", ChassisID: "plc"})
	waitMismatch(t, d, extPeerStationMismatch)
	write(pnio.CheckPeer{PortID: "port-002", ChassisID: "sw"})
	waitMismatch(t, d, extPeerPortMismatch)
	write(pnio.CheckPeer{PortID: "port-002", ChassisID: "plc"})
	waitMismatch(t, d, extPeerPortMismatch, extPeerStationMismatch)
	// Right again: cleared.
	write(pnio.CheckPeer{PortID: "port-003", ChassisID: "sw"})
	waitMismatch(t, d)

	// The neighbor leaves: its information times out.
	sw.Close()
	waitMismatch(t, d, extNoPeer)
	sw = lldpPeer(t, n, "sw", "port-003")
	waitMismatch(t, d)

	// Link down: no peer at once; link up: the peer is back.
	nd.ep.SetLinkUp(false)
	waitMismatch(t, d, extNoPeer)
	if pd := d.portData(); pd[0].LinkState != pnio.LinkStateDown || len(pd[0].Peers) != 0 {
		t.Fatalf("port data while down %+v", pd[0])
	}
	nd.ep.SetLinkUp(true)
	waitMismatch(t, d)

	// Contradictory neighbors: the check follows the neighbor heard most
	// recently and settles when the other one is gone.
	other := lldpPeer(t, n, "intruder", "port-001")
	eventually(t, 5*time.Second, "a second neighbor shown as a mismatch", func() bool { return len(mismatch(d)) > 0 })
	other.Close()
	waitMismatch(t, d)
	// Removing the check clears its diagnosis.
	sw.Close()
	waitMismatch(t, d, extNoPeer)
	if err := x.write(port, pnio.IndexPDPortDataCheck, check(port)); err != nil {
		t.Fatal(err)
	}
	waitMismatch(t, d)
}

func TestPeerMismatch(t *testing.T) {
	nb := lldp.Neighbor{Frame: lldp.Frame{ChassisID: []byte("sw"), PortID: []byte("port-003")}}
	c := &pnio.PDPortDataCheck{Peers: []pnio.CheckPeer{{PortID: "port-003", ChassisID: "sw"}}}
	if got := peerMismatch(c, nb, true); got != nil {
		t.Fatalf("legacy port ID: %v", got)
	}
	if got := peerMismatch(nil, nb, false); got != nil {
		t.Fatalf("no check: %v", got)
	}
	if got := peerMismatch(&pnio.PDPortDataCheck{}, nb, false); got != nil {
		t.Fatalf("no peers: %v", got)
	}
}
