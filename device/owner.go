// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"sort"

	"github.com/otfabric/go-profinet/pnio"
)

// Shared device: several IO controllers connect, each with its own IOCAR,
// and each submodule belongs to one of them, the first whose Connect
// expects it (with the right identification). The others see it as
// "locked by IO controller" in their ModuleDiff, receive no data of it
// (IOPS bad) and cannot write it. When the owner's AR ends, the submodule
// goes to the earliest other AR that expects it, which receives a
// "Released" alarm. p-net behaves the same way
// (docs/research/EVIDENCE-AR-SR-MCR-UDP.md, A.1).
//
// The owner table is guarded by imgMu, the lock of the process data: the
// cyclic path reads it for every frame, and a Connect claims all its
// submodules in one critical section, so that of two concurrent Connects
// for a submodule exactly one wins.

// handover is a submodule given to another AR.
type handover struct {
	a *ar
	k key
}

// claimOwnership gives an IOCAR the submodules it expects that no other AR
// owns, and returns those another AR owns.
func (d *Device) claimOwnership(a *ar) map[key]bool {
	locked := map[key]bool{}
	d.imgMu.Lock()
	defer d.imgMu.Unlock()
	d.claimers[a] = true
	for k := range a.match {
		if o := d.owner[k]; o != nil && o != a {
			if !sameSet(o, a) {
				locked[k] = true
			}
			continue
		}
		d.owner[k] = a
	}
	return locked
}

// owns reports whether the owner o of a submodule lets a use it: o is a,
// or both are ARs of one AR set (system redundancy).
func owns(o, a *ar) bool {
	return o == a || sameSet(o, a)
}

// setHeir makes an AR the heir of the one it replaces.
func (d *Device) setHeir(old, heir *ar) {
	d.imgMu.Lock()
	old.heir = heir
	d.imgMu.Unlock()
}

// releaseOwnership takes an AR's submodules from it: their outputs become
// invalid and each goes to the AR replacing it (which claims them on its
// Connect) or else to the earliest other AR that expects it, which is told
// with a "Released" alarm.
func (d *Device) releaseOwnership(a *ar) {
	var handed []handover
	// The other AR of a set takes the submodules over silently.
	heir := d.srOther(a)
	d.imgMu.Lock()
	if a.heir == nil && heir != nil {
		a.heir = heir
	}
	delete(d.claimers, a)
	var others []*ar
	for o := range d.claimers {
		others = append(others, o)
	}
	sort.Slice(others, func(i, j int) bool { return others[i].seq < others[j].seq })
	for k, o := range d.owner {
		if o != a {
			continue
		}
		delete(d.owner, k)
		if out := d.outputs[k]; out != nil {
			out.iops = pnio.IOxSBad
		}
		if h := a.heir; h != nil && h.match[k] {
			d.owner[k] = h
			continue
		}
		for _, c := range others {
			if c.match[k] {
				d.owner[k] = c
				handed = append(handed, handover{c, k})
				break
			}
		}
	}
	d.imgMu.Unlock()
	for _, h := range handed {
		d.log.Infof("device: submodule %d/%d released by AR %s to AR %s", h.k.slot, h.k.subslot, a.uuid, h.a.uuid)
		d.queuePlug(h.a, h.k)
	}
}

// queuePlug queues the Released alarm of a submodule handed over to an AR.
// The submodules are worked off one at a time, as p-net does: the
// Released alarm, the controller's PrmEnd (plug) with the alarm's
// sequence number, the device's ApplicationReady (plug).
func (d *Device) queuePlug(a *ar, k key) {
	a.mu.Lock()
	a.plugs = append(a.plugs, k)
	start := !a.plugBusy
	a.plugBusy = true
	a.mu.Unlock()
	if start {
		d.wg.Add(1)
		go d.runPlugs(a)
	}
}

func (d *Device) runPlugs(a *ar) {
	defer d.wg.Done()
	for {
		a.mu.Lock()
		if len(a.plugs) == 0 || a.state == ARStateClosed {
			a.plugs, a.plugBusy = nil, false
			a.mu.Unlock()
			return
		}
		k := a.plugs[0]
		a.plugs = a.plugs[1:]
		a.mu.Unlock()
		d.plug(a, k)
	}
}

// plug tells an AR that a submodule is now its own and waits for the
// controller to take it: its PrmEnd (plug), answered with ApplicationReady
// (plug). A controller that does not answer within the AR's activity
// timeout keeps the submodule; the next one is worked off.
func (d *Device) plug(a *ar, k key) {
	if a.currentState() != ARStateData || a.alarmLow == nil {
		return
	}
	m, ok := d.model.Module(k.slot)
	if !ok {
		return
	}
	sm, ok := m.Submodule(k.subslot)
	if !ok {
		return
	}
	actx, cancel := context.WithTimeout(d.ctx, a.cmiTimeout+AlarmAckTimeout)
	seq, err := d.sendAlarmSeq(actx, a, Alarm{Type: pnio.AlarmReleased, Slot: k.slot, Subslot: k.subslot}, m.IdentNumber, sm.IdentNumber)
	cancel()
	if err != nil {
		d.log.Debugf("device: Released alarm to AR %s: %v", a.uuid, err)
		return
	}
	// The controller has its activity timeout from the acknowledgement on.
	ctx, cancel := context.WithTimeout(d.ctx, a.cmiTimeout)
	defer cancel()
	for {
		select {
		case s := <-a.prmEndPlug:
			if s != seq {
				continue
			}
		case <-ctx.Done():
			d.log.Infof("device: AR %s: no PrmEnd (plug) for submodule %d/%d", a.uuid, k.slot, k.subslot)
			return
		}
		break
	}
	blk := pnio.ControlBlock{Type: pnio.BlockAppReadyPlugReq, ARUUID: a.uuid, SessionKey: a.sessionKey, Command: pnio.CmdApplicationReady, AlarmSequence: seq}
	if err := d.callApplicationReady(d.ctx, a, blk); err != nil {
		d.log.Infof("device: AR %s: ApplicationReady (plug) of submodule %d/%d: %v", a.uuid, k.slot, k.subslot, err)
	}
}

// ownerOf returns the AR that owns a submodule, nil when none does.
func (d *Device) ownerOf(slot, subslot uint16) *ar {
	d.imgMu.RLock()
	defer d.imgMu.RUnlock()
	return d.owner[key{slot, subslot}]
}

// lockedFor returns the submodules an AR expects that another AR owns.
func (d *Device) lockedFor(a *ar) map[key]bool {
	locked := map[key]bool{}
	d.imgMu.RLock()
	defer d.imgMu.RUnlock()
	for k := range a.match {
		if o := d.owner[k]; o != nil && !owns(o, a) {
			locked[k] = true
		}
	}
	return locked
}
