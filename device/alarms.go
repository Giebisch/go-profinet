// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

// Alarm is an alarm the application raises.
type Alarm struct {
	// High sends the alarm with high priority.
	High    bool
	Type    pnio.AlarmType
	Slot    uint16
	Subslot uint16
	// USI and Data are the optional alarm item (for example the
	// manufacturer data of a process alarm); HasItem includes it.
	HasItem bool
	USI     uint16
	Data    []byte
	// Specifier bits other than the sequence number (pnio.AlarmSpec*).
	Specifier uint16
}

// alarmData handles a DATA PDU from the controller: an AlarmAck.
func (d *Device) alarmData(a *ar, b []byte) {
	a.touch()
	blk, _, err := pnio.ReadBlock(b)
	if err != nil {
		d.log.Debugf("device: AR %s: alarm data: %v", a.uuid, err)
		return
	}
	switch blk.Header.Type {
	case pnio.BlockAlarmAckHigh, pnio.BlockAlarmAckLow:
		ack, err := pnio.DecodeAlarmAck(blk)
		if err != nil {
			d.log.Debugf("device: AR %s: alarm ack: %v", a.uuid, err)
			return
		}
		a.mu.Lock()
		ch := a.ackWait[ack.Specifier&pnio.AlarmSpecSeqMask]
		delete(a.ackWait, ack.Specifier&pnio.AlarmSpecSeqMask)
		a.mu.Unlock()
		if ch != nil {
			ch <- ack
		}
		d.handler.AlarmAck(a.info(), ack)
	default:
		d.log.Debugf("device: AR %s: unexpected %s on the alarm CR", a.uuid, blk.Header.Type)
	}
}

// SendAlarm sends an alarm to the controller of every AR in the Data
// state that owns the submodule, and waits for each acknowledgement. It
// returns an error matching profinet.ErrNotFound for an unknown
// submodule, profinet.ErrInvalidState when no AR can receive it,
// profinet.ErrTimeout when an acknowledgement did not arrive and a
// *pnio.StatusError when the controller acknowledged with an error.
func (d *Device) SendAlarm(ctx context.Context, al Alarm) error {
	m, ok := d.model.Module(al.Slot)
	if !ok {
		return fmt.Errorf("device: alarm: slot %d: %w", al.Slot, profinet.ErrNotFound)
	}
	sm, ok := m.Submodule(al.Subslot)
	if !ok {
		return fmt.Errorf("device: alarm: subslot %d/%d: %w", al.Slot, al.Subslot, profinet.ErrNotFound)
	}
	d.mu.Lock()
	var targets []*ar
	for _, a := range d.ars {
		if a.typ != pnio.ARTypeIOSAR && a.alarmLow != nil {
			targets = append(targets, a)
		}
	}
	d.mu.Unlock()
	var sent int
	var errs []error
	for _, a := range targets {
		if a.currentState() != ARStateData {
			continue
		}
		sent++
		if err := d.sendAlarm(ctx, a, al, m.IdentNumber, sm.IdentNumber); err != nil {
			errs = append(errs, err)
		}
	}
	if sent == 0 {
		return fmt.Errorf("device: alarm: %w: no AR in the Data state", profinet.ErrInvalidState)
	}
	return errors.Join(errs...)
}

func (d *Device) sendAlarm(ctx context.Context, a *ar, al Alarm, modIdent, subIdent uint32) error {
	a.mu.Lock()
	seq := a.alarmSeq
	a.alarmSeq = (a.alarmSeq + 1) & pnio.AlarmSpecSeqMask
	ch := make(chan pnio.AlarmAck, 1)
	a.ackWait[seq] = ch
	a.mu.Unlock()
	n := pnio.AlarmNotification{
		High: al.High, Type: al.Type, API: model.API, Slot: al.Slot, Subslot: al.Subslot,
		ModuleIdentNumber: modIdent, SubmoduleIdentNumber: subIdent,
		Specifier: al.Specifier&^pnio.AlarmSpecSeqMask | seq,
		HasItem:   al.HasItem, USI: al.USI, Data: al.Data,
	}
	body := n.Append(nil)
	if len(body) > int(a.maxAlarmData) {
		a.mu.Lock()
		delete(a.ackWait, seq)
		a.mu.Unlock()
		return fmt.Errorf("device: alarm: %w: %d octets exceed MaxAlarmDataLength %d", profinet.ErrInvalidConfig, len(body), a.maxAlarmData)
	}
	ep := a.alarmLow
	if al.High {
		ep = a.alarmHigh
	}
	if err := ep.Send(ctx, body); err != nil {
		a.mu.Lock()
		delete(a.ackWait, seq)
		a.mu.Unlock()
		if errors.Is(err, profinet.ErrTimeout) {
			go d.abort(a, pnio.AbortAlarmSend, true)
		}
		return fmt.Errorf("device: alarm to AR %s: %w", a.uuid, err)
	}
	// The application acknowledgement comes as a DATA PDU of the
	// controller; it has as long as the transport would retransmit, and at
	// least AlarmAckTimeout, to arrive.
	t := time.NewTimer(max(a.alarmAckTimeout, AlarmAckTimeout))
	defer t.Stop()
	select {
	case ack, ok := <-ch:
		if !ok {
			return fmt.Errorf("device: alarm to AR %s: %w", a.uuid, profinet.ErrAborted)
		}
		if !ack.Status.OK() {
			return &pnio.StatusError{Op: "alarm ack", Status: ack.Status}
		}
		return nil
	case <-t.C:
		a.mu.Lock()
		delete(a.ackWait, seq)
		a.mu.Unlock()
		return fmt.Errorf("device: alarm to AR %s: %w: no AlarmAck", a.uuid, profinet.ErrTimeout)
	case <-ctx.Done():
		a.mu.Lock()
		delete(a.ackWait, seq)
		a.mu.Unlock()
		return ctx.Err()
	}
}

// AlarmAckTimeout is the shortest time the device waits for the
// controller's AlarmAck after the transport acknowledged the alarm.
const AlarmAckTimeout = 5 * time.Second

// AddDiagnosis records a diagnosis of a submodule and reports it to the
// controllers with a diagnosis alarm. The item's ChannelProperties
// specifier is set to "appears". The diagnosis is recorded even when no
// AR can receive the alarm (it is then reported as an error matching
// profinet.ErrInvalidState).
func (d *Device) AddDiagnosis(ctx context.Context, slot, subslot uint16, item pnio.DiagnosisItem) error {
	if _, ok := d.model.Submodule(slot, subslot); !ok {
		return fmt.Errorf("device: diagnosis %d/%d: %w", slot, subslot, profinet.ErrNotFound)
	}
	item.ChannelProperties = item.ChannelProperties&^pnio.ChanSpecMask | pnio.ChanSpecAppears
	d.mu.Lock()
	k := key{slot, subslot}
	d.diags[k] = append(d.diags[k], item)
	d.mu.Unlock()
	return d.diagAlarm(ctx, pnio.AlarmDiagnosis, slot, subslot, item, true)
}

// RemoveDiagnosis removes the diagnoses of a submodule channel with the
// error type and reports their disappearance. It returns an error matching
// profinet.ErrNotFound when there is none.
func (d *Device) RemoveDiagnosis(ctx context.Context, slot, subslot, channel, errorType uint16) error {
	d.mu.Lock()
	k := key{slot, subslot}
	var kept []pnio.DiagnosisItem
	var removed *pnio.DiagnosisItem
	for _, it := range d.diags[k] {
		if it.ChannelNumber == channel && it.ErrorType == errorType {
			c := it
			removed = &c
			continue
		}
		kept = append(kept, it)
	}
	d.diags[k] = kept
	d.mu.Unlock()
	if removed == nil {
		return fmt.Errorf("device: diagnosis %d/%d channel %d type %d: %w", slot, subslot, channel, errorType, profinet.ErrNotFound)
	}
	spec := pnio.ChanSpecDisappears
	if len(kept) > 0 {
		spec = pnio.ChanSpecOtherRemain
	}
	removed.ChannelProperties = removed.ChannelProperties&^pnio.ChanSpecMask | spec
	t := pnio.AlarmDiagnosis
	if len(kept) == 0 {
		t = pnio.AlarmDiagnosisDisappears
	}
	return d.diagAlarm(ctx, t, slot, subslot, *removed, len(kept) > 0)
}

// Diagnoses returns the recorded diagnoses of a submodule.
func (d *Device) Diagnoses(slot, subslot uint16) []pnio.DiagnosisItem {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]pnio.DiagnosisItem(nil), d.diags[key{slot, subslot}]...)
}

func (d *Device) diagAlarm(ctx context.Context, t pnio.AlarmType, slot, subslot uint16, item pnio.DiagnosisItem, remaining bool) error {
	usi := pnio.USIChannelDiagnosis
	if item.ExtErrorType != 0 || item.ExtAddValue != 0 {
		usi = pnio.USIExtChannelDiagnosis
	}
	spec := pnio.AlarmSpecChannelDiag
	if remaining {
		spec |= pnio.AlarmSpecSubmoduleDiag | pnio.AlarmSpecARDiag
	}
	return d.SendAlarm(ctx, Alarm{
		Type: t, Slot: slot, Subslot: subslot,
		HasItem: true, USI: usi, Data: pnio.AppendDiagnosisItems(nil, usi, []pnio.DiagnosisItem{item}),
		Specifier: spec,
	})
}
