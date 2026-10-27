// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

func (d *Device) im0() pnio.IM0 {
	id := d.model.Identity
	sup := pnio.IMSupportIM1 | pnio.IMSupportIM2 | pnio.IMSupportIM3 | pnio.IMSupportIM4
	return pnio.IM0{
		VendorID:         id.VendorID,
		OrderID:          id.OrderID,
		SerialNumber:     id.SerialNumber,
		HardwareRevision: id.HardwareRevision,
		SoftwareRevision: id.SoftwareRevision,
		VersionMajor:     1,
		VersionMinor:     1,
		Supported:        sup,
	}
}

func isDAP(slot, subslot uint16) bool { return slot == 0 && subslot == model.SubslotDAP }

// readRecord serves one record. a is nil for an implicit read without a
// target AR.
func (d *Device) readRecord(ctx context.Context, a *ar, q pnio.ReadReq) ([]byte, error) {
	if q.API != model.API {
		return nil, rwErr(pnio.RWInvalidArea)
	}
	// Indices below 0xC000 address one subslot, which must exist.
	if q.Index < 0xC000 {
		if _, ok := d.model.Submodule(q.Slot, q.Subslot); !ok {
			return nil, rwErr(pnio.RWInvalidSlot)
		}
	}
	if b, ok, err := d.readPDev(q); ok {
		return b, err
	}
	if b, ok, err := d.readMRP(q); ok {
		return b, err
	}
	switch q.Index {
	case pnio.IndexIM0, pnio.IndexIM1, pnio.IndexIM2, pnio.IndexIM3, pnio.IndexIM4:
		if !isDAP(q.Slot, q.Subslot) {
			return nil, errInvalidIndex
		}
		d.mu.Lock()
		im := d.im
		d.mu.Unlock()
		switch q.Index {
		case pnio.IndexIM0:
			m := d.im0()
			return m.Append(nil), nil
		case pnio.IndexIM1:
			return im.IM1.Append(nil), nil
		case pnio.IndexIM2:
			return im.IM2.Append(nil), nil
		case pnio.IndexIM3:
			return im.IM3.Append(nil), nil
		default:
			return im.IM4.Append(nil), nil
		}
	case pnio.IndexIM0Filter:
		return d.im0Filter(), nil
	case pnio.IndexRealIdentAPI, pnio.IndexRealIdentAR, pnio.IndexRealIdentSlot, pnio.IndexRealIdentSubslot:
		return d.realIdent(q), nil
	case pnio.IndexExpectedIdentAR, pnio.IndexExpectedIdentSlot, pnio.IndexExpectedIdentSubslot:
		if a == nil {
			return nil, rwErr(pnio.RWStateConflict)
		}
		return expectedIdent(a, q), nil
	case pnio.IndexModuleDiffAR:
		if a == nil {
			return nil, rwErr(pnio.RWStateConflict)
		}
		var locked map[key]bool
		if a.typ != pnio.ARTypeIOSAR {
			locked = d.lockedFor(a)
		}
		diff, _ := d.moduleDiff(a.expected, a.currentState() != ARStateData && a.typ != pnio.ARTypeIOSAR, locked)
		if diff == nil {
			diff = &pnio.ModuleDiffBlock{}
		}
		return diff.Append(nil), nil
	case pnio.IndexDiagChannelSubslot, pnio.IndexDiagAllSubslot, pnio.IndexDiagDMQSSubslot,
		pnio.IndexDiagChannelSlot, pnio.IndexDiagAllSlot, pnio.IndexDiagDMQSSlot,
		pnio.IndexDiagChannelAR, pnio.IndexDiagAllAR, pnio.IndexDiagDMQSAR,
		pnio.IndexDiagChannelAPI, pnio.IndexDiagAllAPI, pnio.IndexDiagDMQSAPI, pnio.IndexDiagDMQSDevice:
		return d.diagnosis(q), nil
	case pnio.IndexRecordInputData:
		return d.recordInput(a, q)
	case pnio.IndexPDPortDataReal:
		for _, p := range d.portData() {
			if p.Slot == q.Slot && p.Subslot == q.Subslot {
				return p.Append(nil), nil
			}
		}
		return nil, errInvalidIndex
	case pnio.IndexLogBook:
		// LogBookData with the local time and no entries.
		b := []byte{0x00, 0x19, 0, 0, 1, 0}
		b = binary.BigEndian.AppendUint64(b, uint64(time.Now().Unix()))
		b = binary.BigEndian.AppendUint16(b, 0)
		binary.BigEndian.PutUint16(b[2:], uint16(len(b)-4))
		return b, nil
	default:
	}
	if q.Index.IsUserIndex() {
		d.mu.Lock()
		v, ok := d.records[recKey{q.Slot, q.Subslot, q.Index}]
		v = append([]byte(nil), v...)
		d.mu.Unlock()
		if ok {
			return v, nil
		}
	}
	info := ARInfo{}
	if a != nil {
		info = a.info()
	}
	return d.handler.ReadRecord(ctx, RecordRequest{AR: info, API: q.API, Slot: q.Slot, Subslot: q.Subslot, Index: q.Index, Length: q.Length})
}

// writeRecord serves one write.
func (d *Device) writeRecord(ctx context.Context, a *ar, q pnio.WriteReq, data []byte) error {
	if q.API != model.API {
		return rwErr(pnio.RWInvalidArea)
	}
	sm, ok := d.model.Submodule(q.Slot, q.Subslot)
	if !ok {
		return rwErr(pnio.RWInvalidSlot)
	}
	// A submodule another IO controller owns is not this one's to write
	// (a device access AR may write anything).
	if a.typ != pnio.ARTypeIOSAR {
		if o := d.ownerOf(q.Slot, q.Subslot); o != nil && !owns(o, a) {
			return rwErr(pnio.RWAccessDenied)
		}
		if !srPrimary(a) {
			return errNotPrimary
		}
	}
	req := RecordRequest{AR: a.info(), API: q.API, Slot: q.Slot, Subslot: q.Subslot, Index: q.Index, Length: q.Length}
	switch q.Index {
	case pnio.IndexIM1, pnio.IndexIM2, pnio.IndexIM3, pnio.IndexIM4:
		if !isDAP(q.Slot, q.Subslot) {
			return errInvalidIndex
		}
		blk, rest, err := pnio.ReadBlock(data)
		if err != nil || len(rest) != 0 {
			return rwErr(pnio.RWWriteLength)
		}
		var set func(m *IMData)
		switch q.Index {
		case pnio.IndexIM1:
			m, err := pnio.DecodeIM1(blk)
			if err != nil {
				return rwErr(pnio.RWInvalidParameter)
			}
			set = func(im *IMData) { im.IM1 = m }
		case pnio.IndexIM2:
			m, err := pnio.DecodeIM2(blk)
			if err != nil || !pnio.ValidIMDate(m.Date) {
				return rwErr(pnio.RWInvalidParameter)
			}
			set = func(im *IMData) { im.IM2 = m }
		case pnio.IndexIM3:
			m, err := pnio.DecodeIM3(blk)
			if err != nil {
				return rwErr(pnio.RWInvalidParameter)
			}
			set = func(im *IMData) { im.IM3 = m }
		default:
			m, err := pnio.DecodeIM4(blk)
			if err != nil {
				return rwErr(pnio.RWInvalidParameter)
			}
			set = func(im *IMData) { im.IM4 = m }
		}
		// The I&M records are saved before they change; concurrent writes
		// are serialized by the save.
		return d.writeIM(set)
	case pnio.IndexIM0:
		return rwErr(pnio.RWAccessDenied)
	default:
	}
	if handled, err := d.writePDev(q, data); handled {
		return err
	}
	if handled, err := d.writeMRP(q, data); handled {
		return err
	}
	if !q.Index.IsUserIndex() {
		return errInvalidIndex
	}
	if r, ok := sm.Record(q.Index); ok {
		if r.ReadOnly {
			return rwErr(pnio.RWAccessDenied)
		}
		if len(data) != int(r.Length) {
			return rwErr(pnio.RWWriteLength)
		}
		req.Modeled = true
		if err := d.handler.WriteRecord(ctx, req, data); err != nil {
			return err
		}
		d.mu.Lock()
		d.records[recKey{q.Slot, q.Subslot, q.Index}] = append([]byte(nil), data...)
		d.mu.Unlock()
		return nil
	}
	return d.handler.WriteRecord(ctx, req, data)
}

func (d *Device) im0Filter() []byte {
	// I&M0FilterDataSubmodule: the DAP identity submodule carries I&M0;
	// I&M0FilterDataDevice: it represents the device.
	sub := []byte{0x00, 0x30, 0, 0, 1, 0}
	dev := []byte{0x00, 0x32, 0, 0, 1, 0}
	dap := d.model.DAP
	sm, _ := dap.Submodule(model.SubslotDAP)
	for _, p := range []*[]byte{&sub, &dev} {
		b := *p
		b = binary.BigEndian.AppendUint16(b, 1) // APIs
		b = binary.BigEndian.AppendUint32(b, model.API)
		b = binary.BigEndian.AppendUint16(b, 1) // modules
		b = binary.BigEndian.AppendUint16(b, 0)
		b = binary.BigEndian.AppendUint32(b, dap.IdentNumber)
		b = binary.BigEndian.AppendUint16(b, 1) // submodules
		b = binary.BigEndian.AppendUint16(b, model.SubslotDAP)
		b = binary.BigEndian.AppendUint32(b, sm.IdentNumber)
		binary.BigEndian.PutUint16(b[2:], uint16(len(b)-4))
		*p = b
	}
	return append(sub, dev...)
}

func filterIdent(id pnio.IdentificationData, slot, subslot uint16, bySlot, bySubslot bool) pnio.IdentificationData {
	if !bySlot {
		return id
	}
	out := pnio.IdentificationData{Expected: id.Expected}
	for _, a := range id.APIs {
		na := pnio.IdentAPI{API: a.API}
		for _, s := range a.Slots {
			if s.Slot != slot {
				continue
			}
			ns := pnio.IdentSlot{Slot: s.Slot, IdentNumber: s.IdentNumber}
			for _, sm := range s.Submodules {
				if !bySubslot || sm.Subslot == subslot {
					ns.Submodules = append(ns.Submodules, sm)
				}
			}
			na.Slots = append(na.Slots, ns)
		}
		out.APIs = append(out.APIs, na)
	}
	return out
}

// portData returns the PDPortDataReal of the DAP's ports. Each of the
// device's ports reports its link state (up when the port does not report
// it) and the neighbors its LLDP agent knows as its peers; port
// submodules the device has no port for are down.
func (d *Device) portData() []pnio.PDPortDataReal {
	var out []pnio.PDPortDataReal
	for _, sm := range d.model.DAP.Submodules {
		if sm.Subslot < model.SubslotPort1 || sm.Subslot >= model.SubslotPort1+0xFF {
			continue
		}
		n := sm.Subslot - model.SubslotPort1 + 1
		p := pnio.PDPortDataReal{Slot: 0, Subslot: sm.Subslot, OwnPortID: fmt.Sprintf("port-%03d", n),
			LinkState: pnio.LinkStateDown, MediaType: 1}
		if dp := d.portBySubslot(sm.Subslot); dp != nil && dp.linkUp() {
			p.LinkState = pnio.LinkStateUp
			p.MAUType = 0x0010 // 100BASE-TX full duplex
			if dp.lldpA != nil {
				for _, nb := range dp.lldpA.Neighbors() {
					peer := pnio.PDPortPeer{PortID: nb.Frame.PortString(), ChassisID: nb.Frame.ChassisString(), MAC: nb.MAC}
					if nb.Frame.Delay != nil {
						peer.LineDelay = nb.Frame.Delay.Cable
					}
					p.Peers = append(p.Peers, peer)
				}
			}
		}
		out = append(out, p)
	}
	return out
}

func (d *Device) realIdent(q pnio.ReadReq) []byte {
	id := d.model.RealIdentification()
	id = filterIdent(id, q.Slot, q.Subslot, q.Index == pnio.IndexRealIdentSlot || q.Index == pnio.IndexRealIdentSubslot, q.Index == pnio.IndexRealIdentSubslot)
	return id.Append(nil)
}

func expectedIdent(a *ar, q pnio.ReadReq) []byte {
	id := pnio.IdentificationData{Expected: true}
	apis := map[uint32]int{}
	for _, b := range a.expected {
		for _, m := range b.Modules {
			i, ok := apis[m.API]
			if !ok {
				i = len(id.APIs)
				apis[m.API] = i
				id.APIs = append(id.APIs, pnio.IdentAPI{API: m.API})
			}
			s := pnio.IdentSlot{Slot: m.Slot, IdentNumber: m.IdentNumber}
			for _, sm := range m.Submodules {
				s.Submodules = append(s.Submodules, pnio.IdentSubmodule{Subslot: sm.Subslot, IdentNumber: sm.IdentNumber})
			}
			id.APIs[i].Slots = append(id.APIs[i].Slots, s)
		}
	}
	id = filterIdent(id, q.Slot, q.Subslot, q.Index != pnio.IndexExpectedIdentAR, q.Index == pnio.IndexExpectedIdentSubslot)
	return id.Append(nil)
}

// diagnosis returns the DiagnosisData blocks in the scope of the index.
func (d *Device) diagnosis(q pnio.ReadReq) []byte {
	d.mu.Lock()
	keys := make([]key, 0, len(d.diags))
	for k, v := range d.diags {
		if len(v) == 0 {
			continue
		}
		switch q.Index & 0xF000 {
		case 0x8000:
			if k.slot != q.Slot || k.subslot != q.Subslot {
				continue
			}
		case 0xC000:
			if k.slot != q.Slot {
				continue
			}
		default:
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].slot != keys[j].slot {
			return keys[i].slot < keys[j].slot
		}
		return keys[i].subslot < keys[j].subslot
	})
	var out []byte
	for _, k := range keys {
		items := d.diags[k]
		usi := pnio.USIChannelDiagnosis
		for _, it := range items {
			if it.ExtErrorType != 0 || it.ExtAddValue != 0 {
				usi = pnio.USIExtChannelDiagnosis
			}
		}
		dd := pnio.DiagnosisData{API: model.API, Slot: k.slot, Subslot: k.subslot, ChannelNumber: pnio.ChannelNumberSubmodule, ChannelProperties: pnio.ChanSpecAppears, USI: usi, Items: items}
		out = dd.Append(out)
	}
	d.mu.Unlock()
	return out
}

// recordInput answers RecordInputDataObjectElement (0x8028): the input
// data of a submodule with its provider status and the controller's
// consumer status.
func (d *Device) recordInput(a *ar, q pnio.ReadReq) ([]byte, error) {
	d.imgMu.RLock()
	in, ok := d.inputs[key{q.Slot, q.Subslot}]
	var data []byte
	var iops uint8
	if ok {
		data = append([]byte(nil), in.data...)
		iops = in.iops
	}
	d.imgMu.RUnlock()
	if !ok {
		return nil, rwErr(pnio.RWInvalidSlot)
	}
	// The controller's consumer status of these inputs, from the AR's
	// output frames; bad without an AR or before the first frame.
	iocs := pnio.IOxSBad
	if a != nil {
		a.mu.Lock()
		if v, ok := a.iocs[key{q.Slot, q.Subslot}]; ok {
			iocs = v
		}
		a.mu.Unlock()
	}
	b := []byte{0x00, 0x15, 0, 0, 1, 0}
	b = append(b, 1, iocs, 1, iops)
	b = binary.BigEndian.AppendUint16(b, uint16(len(data)))
	b = append(b, data...)
	binary.BigEndian.PutUint16(b[2:], uint16(len(b)-4))
	return b, nil
}
