// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"time"

	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/lldp"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

// The physical device (PDev): the checks and adjustments a controller
// writes to the port and interface submodules (PDPortDataCheck,
// PDPortDataAdjust, PDInterfaceAdjust), and their effects. What a device
// with one Ethernet port and no frame forwarding can honour is accepted;
// the rest is refused, never stored as if applied
// (docs/research/SOFTWARE-ONLY-ROADMAP.md, 1B).

// Remote mismatch extended channel error types (ChannelErrorType 0x8001),
// as p-net reports them.
const (
	extPeerStationMismatch uint16 = 0x8000
	extPeerPortMismatch    uint16 = 0x8001
	extNoPeer              uint16 = 0x8005
)

// pdevChannel is the channel of the port diagnoses: the whole submodule.
const pdevChannel uint16 = 0x8000

// pdevState is the PDev state of the device, guarded by Device.mu.
type pdevState struct {
	checks  map[uint16]*pnio.PDPortDataCheck
	adjusts map[uint16]*pnio.PDPortDataAdjust
	// ifAdjust is the written PDInterfaceAdjust, nil before a write.
	ifAdjust *pnio.PDInterfaceAdjust
	// active are the extended error types of the remote mismatch
	// diagnoses each port reports.
	active map[uint16][]uint16
}

// interfaceMode returns the MultipleInterfaceMode in effect: the
// device's LLDP port ID includes the name of station unless a controller
// wrote otherwise.
func (p *pdevState) interfaceMode() uint32 {
	if p.ifAdjust == nil {
		return pnio.InterfaceModeNameOfDevice
	}
	return p.ifAdjust.MultipleInterfaceMode
}

// helloBlocked reports whether the DCPBoundaries block DCP Hello on
// every one of the device's ports (subslots): then it sends none.
func (p *pdevState) helloBlocked(subslots []uint16) bool {
	for _, sub := range subslots {
		a := p.adjusts[sub]
		if a == nil || !a.HasDCPBoundary || a.DCPBoundary&pnio.DCPBoundaryHello == 0 {
			return false
		}
	}
	return len(subslots) > 0
}

// isPort reports whether slot/subslot is a port submodule of the DAP.
func (d *Device) isPort(slot, subslot uint16) bool {
	if slot != 0 || subslot < model.SubslotPort1 || subslot > model.SubslotPort1+0xFE {
		return false
	}
	_, ok := d.model.Submodule(slot, subslot)
	return ok
}

func isInterface(slot, subslot uint16) bool {
	return slot == 0 && subslot == model.SubslotInterface
}

// oneBlock decodes record data that is one block, followed by nothing
// but up to three octets of padding.
func oneBlock(data []byte) (pnio.RawBlock, error) {
	blk, rest, err := pnio.ReadBlock(data)
	if err != nil {
		return blk, rwErr(pnio.RWWriteLength)
	}
	if len(rest) > 3 {
		return blk, rwErr(pnio.RWWriteLength)
	}
	for _, b := range rest {
		if b != 0 {
			return blk, rwErr(pnio.RWWriteLength)
		}
	}
	return blk, nil
}

// errUnsupportedPDev refuses a check or adjustment the device does not
// implement. Which PNIORW code PI specifies here is not public; p-net
// answers "invalid area".
var errUnsupportedPDev = rwErr(pnio.RWFeatureNotSupport)

// writePDev serves a write of a PDev record; handled is false for other
// indexes.
func (d *Device) writePDev(q pnio.WriteReq, data []byte) (handled bool, err error) {
	switch q.Index {
	case pnio.IndexPDPortDataCheck, pnio.IndexPDPortDataAdjust:
		if !d.isPort(q.Slot, q.Subslot) {
			return true, errInvalidIndex
		}
	case pnio.IndexPDInterfaceAdjust:
		if !isInterface(q.Slot, q.Subslot) {
			return true, errInvalidIndex
		}
	case pnio.IndexPDPortDataReal, pnio.IndexPDInterfaceDataReal, pnio.IndexPDRealData, pnio.IndexPDExpectedData:
		return true, rwErr(pnio.RWAccessDenied)
	default:
		return false, nil
	}
	blk, err := oneBlock(data)
	if err != nil {
		return true, err
	}
	switch q.Index {
	case pnio.IndexPDPortDataCheck:
		return true, d.writeCheck(q, blk)
	case pnio.IndexPDPortDataAdjust:
		return true, d.writeAdjust(q, blk)
	default:
		return true, d.writeInterfaceAdjust(blk)
	}
}

func (d *Device) writeCheck(q pnio.WriteReq, blk pnio.RawBlock) error {
	c, err := pnio.DecodePDPortDataCheck(blk)
	if err != nil || c.Slot != q.Slot || c.Subslot != q.Subslot {
		return rwErr(pnio.RWInvalidParameter)
	}
	// MAU type, link state and line delay checks need what a library
	// on a general-purpose OS does not know reliably (the real MAU type,
	// a measured line delay) or a diagnosis whose code is not public.
	if len(c.Other) > 0 {
		return errUnsupportedPDev
	}
	// One port has one peer.
	if c.Peers != nil && len(c.Peers) != 1 {
		return rwErr(pnio.RWInvalidParameter)
	}
	d.mu.Lock()
	if c.Peers == nil {
		delete(d.pd.checks, q.Subslot)
	} else {
		d.pd.checks[q.Subslot] = &c
	}
	d.mu.Unlock()
	d.pokePDev()
	return nil
}

func (d *Device) writeAdjust(q pnio.WriteReq, blk pnio.RawBlock) error {
	a, err := pnio.DecodePDPortDataAdjust(blk)
	if err != nil || a.Slot != q.Slot || a.Subslot != q.Subslot {
		return rwErr(pnio.RWInvalidParameter)
	}
	// Domain and multicast boundaries (other than DCP's) act on
	// forwarding the bridge does not filter by; MAU type, link state and
	// preamble belong to the operating system.
	if len(a.Other) > 0 {
		return errUnsupportedPDev
	}
	const p2pKnown = pnio.PeerToPeerNoLLDP | pnio.PeerToPeerNoPTCPDelay | pnio.PeerToPeerNoPathDelay
	const dcpKnown = pnio.DCPBoundaryIdentify | pnio.DCPBoundaryHello
	if a.PeerToPeerBoundary&^p2pKnown != 0 || a.DCPBoundary&^dcpKnown != 0 {
		return rwErr(pnio.RWInvalidParameter)
	}
	d.mu.Lock()
	if !a.HasPeerToPeerBoundary && !a.HasDCPBoundary {
		delete(d.pd.adjusts, q.Subslot)
	} else {
		d.pd.adjusts[q.Subslot] = &a
	}
	d.mu.Unlock()
	d.applyAdjust()
	return nil
}

// applyAdjust applies the adjustments of each port: LLDP transmission
// (the device sends no PTCP or path delay requests and no DCP Identify,
// so those bits hold already) and the DCP boundary: on a multiport device
// the bridge stops forwarding DCP Identify and Hello across the port; a
// device sends no Hello while every port blocks it.
func (d *Device) applyAdjust() {
	d.mu.Lock()
	noLLDP := make([]bool, len(d.ports))
	groups := make([][]ethernet.MAC, len(d.ports))
	for i, p := range d.ports {
		if a := d.pd.adjusts[p.subslot]; a != nil {
			noLLDP[i] = a.HasPeerToPeerBoundary && a.PeerToPeerBoundary&pnio.PeerToPeerNoLLDP != 0
			if a.HasDCPBoundary {
				groups[i] = dcpGroups(a.DCPBoundary)
			}
		}
	}
	blocked := d.pd.helloBlocked(d.portSubslots())
	r := d.dcpR
	d.mu.Unlock()
	for i, p := range d.ports {
		if p.lldpA != nil {
			p.lldpA.SetTransmit(!noLLDP[i])
		}
		if d.cfg.Bridge != nil {
			d.cfg.Bridge.SetBoundary(i, groups[i])
		}
	}
	if blocked && r != nil {
		r.StopHello()
	}
}

// portSubslots returns the port submodules of the device's ports.
func (d *Device) portSubslots() []uint16 {
	out := make([]uint16, len(d.ports))
	for i, p := range d.ports {
		out[i] = p.subslot
	}
	return out
}

func (d *Device) writeInterfaceAdjust(blk pnio.RawBlock) error {
	a, err := pnio.DecodePDInterfaceAdjust(blk)
	if err != nil || a.MultipleInterfaceMode&^pnio.InterfaceModeNameOfDevice != 0 {
		return rwErr(pnio.RWInvalidParameter)
	}
	d.mu.Lock()
	changed := d.pd.interfaceMode() != a.MultipleInterfaceMode
	d.pd.ifAdjust = &a
	d.mu.Unlock()
	if changed {
		// Announce the new port IDs at once.
		for _, p := range d.ports {
			if p.lldpA == nil {
				continue
			}
			if err := p.lldpA.SendNow(d.ctx); err != nil {
				d.log.Debugf("device: port %d: LLDP: %v", p.n, err)
			}
		}
	}
	return nil
}

// readPDev serves a read of a PDev record; handled is false for other
// indexes.
func (d *Device) readPDev(q pnio.ReadReq) (b []byte, handled bool, err error) {
	switch q.Index {
	case pnio.IndexPDPortDataCheck, pnio.IndexPDPortDataAdjust:
		if !d.isPort(q.Slot, q.Subslot) {
			return nil, true, errInvalidIndex
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if q.Index == pnio.IndexPDPortDataCheck {
			if c := d.pd.checks[q.Subslot]; c != nil {
				return c.Append(nil), true, nil
			}
		} else if a := d.pd.adjusts[q.Subslot]; a != nil {
			return a.Append(nil), true, nil
		}
		// Nothing written: no data, as p-net answers.
		return []byte{}, true, nil
	case pnio.IndexPDInterfaceAdjust:
		if !isInterface(q.Slot, q.Subslot) {
			return nil, true, errInvalidIndex
		}
		d.mu.Lock()
		a := pnio.PDInterfaceAdjust{MultipleInterfaceMode: d.pd.interfaceMode()}
		d.mu.Unlock()
		return a.Append(nil), true, nil
	case pnio.IndexPDInterfaceDataReal:
		if !isInterface(q.Slot, q.Subslot) {
			return nil, true, errInvalidIndex
		}
		ifd := d.interfaceData()
		return ifd.Append(nil), true, nil
	case pnio.IndexPDRealData:
		rd := pnio.PDRealData{Ports: d.portData()}
		if _, ok := d.model.Submodule(0, model.SubslotInterface); ok {
			ifd := d.interfaceData()
			rd.Interface, rd.InterfaceSubslot = &ifd, model.SubslotInterface
		}
		return pnio.AppendPDRealDataRecord(nil, model.API, &rd), true, nil
	case pnio.IndexPDExpectedData:
		return d.expectedPDev(), true, nil
	default:
		return nil, false, nil
	}
}

// interfaceData returns the PDInterfaceDataReal: the name of station as
// the chassis ID, the MAC address and the IP suite.
func (d *Device) interfaceData() pnio.PDInterfaceDataReal {
	d.mu.Lock()
	defer d.mu.Unlock()
	return pnio.PDInterfaceDataReal{ChassisID: d.name, MAC: d.mux.HardwareAddr(), IP: d.ip.Addr, Mask: d.ip.Mask, Gateway: d.ip.Gateway}
}

// expectedPDev returns PDExpectedData: the written checks and
// adjustments, each submodule's in a MultipleBlockHeader.
func (d *Device) expectedPDev() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	var b []byte
	if a := d.pd.ifAdjust; a != nil {
		b = pnio.AppendMultipleBlockHeader(b, model.API, 0, model.SubslotInterface, a.Append)
	}
	for _, sm := range d.model.DAP.Submodules {
		c, a := d.pd.checks[sm.Subslot], d.pd.adjusts[sm.Subslot]
		if c == nil && a == nil {
			continue
		}
		b = pnio.AppendMultipleBlockHeader(b, model.API, 0, sm.Subslot, func(b []byte) []byte {
			if c != nil {
				b = c.Append(b)
			}
			if a != nil {
				b = a.Append(b)
			}
			return b
		})
	}
	if b == nil {
		b = []byte{}
	}
	return b
}

// pokePDev asks the evaluator to check the ports again.
func (d *Device) pokePDev() {
	select {
	case d.pdPoke <- struct{}{}:
	default:
	}
}

// evalPDev evaluates the port checks whenever something they depend on
// changes: a write, the LLDP neighbors, the link. One goroutine does it,
// so diagnoses change in order and the alarms they cause never block
// LLDP or RPC.
func (d *Device) evalPDev() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.pdPoke:
		}
		for _, sm := range d.model.DAP.Submodules {
			if d.isPort(0, sm.Subslot) {
				d.evalPort(sm.Subslot)
			}
		}
	}
}

// peerMismatch returns the extended error types of the remote mismatch
// between a port's check and its peer, none when they match. The peer is
// the neighbor heard most recently, as on a point-to-point link; a peer
// in the "standard" LLDP mode sends "port-001.<its name>", which matches
// the expected port "port-001".
func peerMismatch(c *pnio.PDPortDataCheck, peer lldp.Neighbor, found bool) []uint16 {
	if c == nil || len(c.Peers) == 0 {
		return nil
	}
	if !found {
		return []uint16{extNoPeer}
	}
	want := c.Peers[0]
	chassis, port := peer.Frame.ChassisString(), peer.Frame.PortString()
	var out []uint16
	if port != want.PortID && port != want.PortID+"."+chassis {
		out = append(out, extPeerPortMismatch)
	}
	if chassis != want.ChassisID {
		out = append(out, extPeerStationMismatch)
	}
	return out
}

func (d *Device) evalPort(subslot uint16) {
	d.mu.Lock()
	c := d.pd.checks[subslot]
	active := d.pd.active[subslot]
	d.mu.Unlock()
	var peer lldp.Neighbor
	found := false
	if p := d.portBySubslot(subslot); p != nil && p.lldpA != nil && p.linkUp() {
		peer, found = p.lldpA.Latest()
	}
	want := peerMismatch(c, peer, found)
	if equalTypes(want, active) {
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	if len(active) > 0 {
		if err := d.RemoveDiagnosis(ctx, 0, subslot, pdevChannel, pnio.ChannelErrRemoteMismat); err != nil {
			d.log.Debugf("device: port %#x: %v", subslot, err)
		}
	}
	for _, ext := range want {
		item := pnio.DiagnosisItem{ChannelNumber: pdevChannel, ErrorType: pnio.ChannelErrRemoteMismat, ExtErrorType: ext}
		if err := d.AddDiagnosis(ctx, 0, subslot, item); err != nil {
			d.log.Debugf("device: port %#x: %v", subslot, err)
		}
	}
	d.mu.Lock()
	d.pd.active[subslot] = want
	d.mu.Unlock()
	if len(want) > 0 {
		d.log.Infof("device: port %#x: remote mismatch %#x (expected %+v)", subslot, want, c.Peers)
	}
}

func equalTypes(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
