// SPDX-License-Identifier: MIT

package device

import (
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/mrp"
	"github.com/otfabric/go-profinet/pnio"
)

// Media redundancy: a multiport device runs an MRP client or manager
// (package mrp) on two of its ports, configured by Config.MRP and by the
// PROFINET MRP records a controller writes (PDInterfaceMrpDataAdjust
// 0x8052, PDPortMrpDataAdjust 0x8053), and reported in
// PDInterfaceMrpDataReal (0x8050) and PDPortMrpDataReal (0x8054). The
// timers of MrpManagerParams and MrpClientParams are accepted but not
// applied (their unit is not public): the entity runs Config.MRP.Profile.
// PDInterfaceMrpDataCheck (0x8051) is refused: the device does not
// evaluate it.

// MRPConfig configures media redundancy.
type MRPConfig struct {
	// Role is the initial role; a controller may change it.
	// mrp.RoleAuto (MRA) is a role of this configuration only: PROFINET's
	// MRP_Role has no value for it, and PDInterfaceMrpDataReal reports
	// what the entity acts as (client or manager).
	Role mrp.Role
	// Domain is the MRP domain UUID (zero: mrp.DefaultDomain) and
	// DomainName its name.
	Domain     [16]byte
	DomainName string
	// Prio is the manager priority; zero means mrp.DefaultManagerPrio.
	Prio uint16
	// Profile is the timer profile; zero means mrp.Profile200.
	Profile mrp.Profile
	// RingPorts are the ring ports (1-based); zero means ports 1 and 2.
	RingPorts [2]int
}

// mrpState is the device's MRP state, guarded by Device.mu.
type mrpState struct {
	cfg     MRPConfig
	ent     *mrp.Entity
	adjust  *pnio.PDInterfaceMrpDataAdjust
	portAdj map[uint16]*pnio.PDPortMrpData
}

// startMRP checks the configuration and starts the entity of the
// configured role.
func (d *Device) startMRP() error {
	c := *d.cfg.MRP
	if d.cfg.Bridge == nil {
		return fmt.Errorf("device: MRP needs a multiport device (Config.Bridge): %w", profinet.ErrInvalidConfig)
	}
	if c.RingPorts == [2]int{} {
		c.RingPorts = [2]int{1, 2}
	}
	for _, p := range c.RingPorts {
		if p < 1 || p > len(d.ports) {
			return fmt.Errorf("device: MRP ring port %d of %d ports: %w", p, len(d.ports), profinet.ErrInvalidConfig)
		}
	}
	if c.RingPorts[0] == c.RingPorts[1] {
		return fmt.Errorf("device: MRP ring ports %v: %w", c.RingPorts, profinet.ErrInvalidConfig)
	}
	if c.Domain == ([16]byte{}) {
		c.Domain = mrp.DefaultDomain
	}
	if c.DomainName == "" {
		c.DomainName = "mrpdomain-1"
	}
	d.mrpSt = &mrpState{cfg: c, portAdj: map[uint16]*pnio.PDPortMrpData{}}
	return d.setMRPRole(c.Role, c.Domain, c.DomainName)
}

// setMRPRole stops the running entity and starts one of the new role.
func (d *Device) setMRPRole(role mrp.Role, domain [16]byte, name string) error {
	d.mu.Lock()
	st := d.mrpSt
	old := st.ent
	st.ent = nil
	st.cfg.Role, st.cfg.Domain, st.cfg.DomainName = role, domain, name
	c := st.cfg
	d.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if role == mrp.RoleDisabled {
		// No ring: both ports forward.
		for _, p := range c.RingPorts {
			d.cfg.Bridge.SetBlocked(p-1, false)
		}
		return nil
	}
	ports := [2]*l2.Mux{d.ports[c.RingPorts[0]-1].mux, d.ports[c.RingPorts[1]-1].mux}
	ent, err := mrp.New(mrp.Config{Role: role, Ports: ports, Index: [2]int{c.RingPorts[0] - 1, c.RingPorts[1] - 1},
		Switch: d.cfg.Bridge, MAC: d.mux.HardwareAddr(), Domain: c.Domain, Prio: c.Prio, Profile: c.Profile, Logger: d.log})
	if err != nil {
		return err
	}
	d.mu.Lock()
	st.ent = ent
	d.mu.Unlock()
	d.log.Infof("device: MRP %s on ports %d and %d", role, c.RingPorts[0], c.RingPorts[1])
	return nil
}

func (d *Device) stopMRP() {
	d.mu.Lock()
	var ent *mrp.Entity
	if d.mrpSt != nil {
		ent, d.mrpSt.ent = d.mrpSt.ent, nil
	}
	d.mu.Unlock()
	if ent != nil {
		_ = ent.Close()
	}
}

// MRPStatus returns the status of the device's MRP entity; ok is false
// when it runs none.
func (d *Device) MRPStatus() (mrp.Status, bool) {
	d.mu.Lock()
	var ent *mrp.Entity
	if d.mrpSt != nil {
		ent = d.mrpSt.ent
	}
	d.mu.Unlock()
	if ent == nil {
		return mrp.Status{}, false
	}
	return ent.Status(), true
}

// isRingPort reports whether a port submodule is an MRP ring port: mu
// held.
func (d *Device) isRingPort(subslot uint16) bool {
	if d.mrpSt == nil {
		return false
	}
	for _, p := range d.mrpSt.cfg.RingPorts {
		if model.SubslotPort1+uint16(p-1) == subslot {
			return true
		}
	}
	return false
}

func isMRPIndex(i pnio.Index) bool {
	return i >= pnio.IndexPDInterfaceMrpDataReal && i <= pnio.IndexPDPortMrpDataReal
}

// readMRP serves a read of an MRP record; handled is false for other
// indexes.
func (d *Device) readMRP(q pnio.ReadReq) (b []byte, handled bool, err error) {
	if !isMRPIndex(q.Index) {
		return nil, false, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.mrpSt
	if st == nil {
		return nil, true, errInvalidIndex
	}
	switch q.Index {
	case pnio.IndexPDInterfaceMrpDataReal, pnio.IndexPDInterfaceMrpDataAdjust, pnio.IndexPDInterfaceMrpDataCheck:
		if !isInterface(q.Slot, q.Subslot) {
			return nil, true, errInvalidIndex
		}
	default:
		if !d.isPort(q.Slot, q.Subslot) {
			return nil, true, errInvalidIndex
		}
	}
	switch q.Index {
	case pnio.IndexPDInterfaceMrpDataReal:
		role := st.cfg.Role
		var es mrp.Status
		if st.ent != nil {
			es = st.ent.Status()
			if role == mrp.RoleAuto {
				role = es.Acting
			}
		}
		r := pnio.PDInterfaceMrpDataReal{Domain: st.cfg.Domain, Role: uint16(role), DomainName: st.cfg.DomainName, Version: mrp.Version}
		if st.ent != nil && role == mrp.RoleManager {
			r.HasRingState = true
			if !es.RingOpen {
				r.RingState = uint16(mrp.RingClosed)
			}
		}
		return r.Append(nil), true, nil
	case pnio.IndexPDInterfaceMrpDataAdjust:
		if st.adjust != nil {
			return st.adjust.Append(nil), true, nil
		}
	case pnio.IndexPDPortMrpDataAdjust:
		if a := st.portAdj[q.Subslot]; a != nil {
			return a.Append(nil), true, nil
		}
	case pnio.IndexPDPortMrpDataReal:
		if d.isRingPort(q.Subslot) && st.cfg.Role != mrp.RoleDisabled {
			r := pnio.PDPortMrpData{Real: true, Domain: st.cfg.Domain}
			return r.Append(nil), true, nil
		}
	default:
		// The check: nothing written is ever stored (writeMRP).
	}
	return []byte{}, true, nil
}

// writeMRP serves a write of an MRP record; handled is false for other
// indexes.
func (d *Device) writeMRP(q pnio.WriteReq, data []byte) (handled bool, err error) {
	if !isMRPIndex(q.Index) {
		return false, nil
	}
	d.mu.Lock()
	st := d.mrpSt
	d.mu.Unlock()
	if st == nil {
		return true, errInvalidIndex
	}
	switch q.Index {
	case pnio.IndexPDInterfaceMrpDataReal, pnio.IndexPDPortMrpDataReal:
		return true, rwErr(pnio.RWAccessDenied)
	case pnio.IndexPDInterfaceMrpDataCheck:
		if !isInterface(q.Slot, q.Subslot) {
			return true, errInvalidIndex
		}
		return true, errUnsupportedPDev
	default:
	}
	blk, err := oneBlock(data)
	if err != nil {
		return true, err
	}
	if q.Index == pnio.IndexPDPortMrpDataAdjust {
		if !d.isPort(q.Slot, q.Subslot) {
			return true, errInvalidIndex
		}
		p, err := pnio.DecodePDPortMrpData(blk)
		if err != nil || p.Real {
			return true, rwErr(pnio.RWInvalidParameter)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if !d.isRingPort(q.Subslot) || p.Domain != st.cfg.Domain {
			return true, rwErr(pnio.RWInvalidParameter)
		}
		st.portAdj[q.Subslot] = &p
		return true, nil
	}
	if !isInterface(q.Slot, q.Subslot) {
		return true, errInvalidIndex
	}
	a, err := pnio.DecodePDInterfaceMrpDataAdjust(blk)
	if err != nil {
		return true, rwErr(pnio.RWInvalidParameter)
	}
	if a.Role > pnio.MrpRoleManager || len(a.DomainName) == 0 || len(a.DomainName) > 240 {
		return true, rwErr(pnio.RWInvalidParameter)
	}
	d.mu.Lock()
	same := st.cfg.Role == mrp.Role(a.Role) && st.cfg.Domain == a.Domain && st.cfg.DomainName == a.DomainName
	st.adjust = &a
	if a.Manager != nil && a.Manager.Prio != 0 {
		st.cfg.Prio = a.Manager.Prio
	}
	d.mu.Unlock()
	if same {
		return true, nil
	}
	if err := d.setMRPRole(mrp.Role(a.Role), a.Domain, a.DomainName); err != nil {
		d.log.Infof("device: MRP: %v", err)
		return true, rwErr(pnio.RWInvalidParameter)
	}
	return true, nil
}
