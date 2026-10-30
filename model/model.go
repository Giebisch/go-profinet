// SPDX-License-Identifier: MIT

// Package model describes a PROFINET IO device in Go: its identity, the
// device access point, the modules plugged into its slots and the
// submodules with their IO data and parameter records.
//
// One model drives three things: the real configuration an IO-Device
// (package device) serves, the expected configuration an IO-Controller
// (package controller) connects with, and the GSDML file package gsdml
// generates for engineering tools. Package gsdml also derives models from
// GSDML files.
package model

import (
	"fmt"
	"sort"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/pnio"
)

// Well-known subslots of the device access point.
const (
	// SubslotDAP is the subslot of the DAP's identity submodule.
	SubslotDAP uint16 = 1
	// SubslotInterface is the subslot of the first interface.
	SubslotInterface uint16 = 0x8000
	// SubslotPort1 is the subslot of the first port of the first
	// interface.
	SubslotPort1 uint16 = 0x8001
)

// Record is a parameter record of a submodule.
type Record struct {
	// Index is the record index, 0..0x7FFF for user records.
	Index pnio.Index `json:"index"`
	// Name names the record in GSDML.
	Name string `json:"name,omitempty"`
	// Length is the exact length a write must have.
	Length uint16 `json:"length"`
	// Default is the value before the first write (zeros when nil).
	Default []byte `json:"default,omitempty"`
	// ReadOnly rejects writes.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// Submodule is a submodule type: its ident number and IO data shape.
type Submodule struct {
	Name        string `json:"name"`
	IdentNumber uint32 `json:"identNumber"`
	// Subslot is where the submodule is plugged in its module.
	Subslot uint16 `json:"subslot"`
	// InputLen and OutputLen are the IO data lengths; both zero make a
	// NO_IO submodule.
	InputLen  uint16   `json:"inputLength,omitempty"`
	OutputLen uint16   `json:"outputLength,omitempty"`
	Records   []Record `json:"records,omitempty"`
	// Info is a free text for GSDML.
	Info string `json:"info,omitempty"`
}

// Type returns the submodule type implied by the data lengths.
func (s *Submodule) Type() pnio.SubmoduleType {
	switch {
	case s.InputLen > 0 && s.OutputLen > 0:
		return pnio.SubmoduleInputOutput
	case s.InputLen > 0:
		return pnio.SubmoduleInput
	case s.OutputLen > 0:
		return pnio.SubmoduleOutput
	default:
		return pnio.SubmoduleNoIO
	}
}

// Record returns the record with an index.
func (s *Submodule) Record(i pnio.Index) (*Record, bool) {
	for k := range s.Records {
		if s.Records[k].Index == i {
			return &s.Records[k], true
		}
	}
	return nil, false
}

// Module is a module type with the submodules it carries.
type Module struct {
	Name        string      `json:"name"`
	IdentNumber uint32      `json:"identNumber"`
	Submodules  []Submodule `json:"submodules"`
	Info        string      `json:"info,omitempty"`
}

// Submodule returns the submodule in a subslot.
func (m *Module) Submodule(subslot uint16) (*Submodule, bool) {
	for i := range m.Submodules {
		if m.Submodules[i].Subslot == subslot {
			return &m.Submodules[i], true
		}
	}
	return nil, false
}

// Slot is a module plugged into a slot.
type Slot struct {
	Number uint16 `json:"slot"`
	Module Module `json:"module"`
}

// Identity is the identification of the device type and instance.
type Identity struct {
	VendorID   uint16 `json:"vendorId"`
	DeviceID   uint16 `json:"deviceId"`
	VendorName string `json:"vendorName"`
	// TypeOfStation is the device type name (DCP Device/Vendor).
	TypeOfStation string `json:"typeOfStation"`
	Family        string `json:"family,omitempty"`
	OrderID       string `json:"orderId"`
	SerialNumber  string `json:"serialNumber,omitempty"`
	// HardwareRevision and SoftwareRevision are reported in I&M0.
	HardwareRevision uint16                `json:"hardwareRevision"`
	SoftwareRevision pnio.SoftwareRevision `json:"softwareRevision"`
	// Instance is the DeviceInstance (DCP, object UUID).
	Instance uint16 `json:"instance"`
}

// Capabilities are the communication properties the device declares.
type Capabilities struct {
	// MaxARs is the number of application relationships served at once.
	MaxARs int `json:"maxArs"`
	// MinDeviceInterval is the shortest cycle, SendClockFactor ×
	// ReductionRatio, in units of 31.25 µs (32 = 1 ms, at most 4096 =
	// 128 ms).
	MinDeviceInterval uint16 `json:"minDeviceInterval"`
	// ReductionRatios are the supported reduction ratios; empty means
	// 1, 2, 4, ..., 512.
	ReductionRatios []uint16 `json:"reductionRatios,omitempty"`
	// SupportedRTClasses lists the RT classes ("RT_CLASS_1").
	SupportedRTClasses []string `json:"supportedRtClasses,omitempty"`
	// ConformanceClass is "A", "B" or "C".
	ConformanceClass string `json:"conformanceClass"`
	// LLDP reports whether the device sends LLDP (conformance class B).
	LLDP bool `json:"lldp"`
	// Ports is the number of Ethernet ports.
	Ports int `json:"ports"`
	// DCPHello reports whether the device can send DCP Hello (GSDML
	// DCP_HelloSupported; device.Config.Hello enables it).
	DCPHello bool `json:"dcpHello,omitempty"`
}

// Device is the description of an IO-Device.
type Device struct {
	Identity     Identity     `json:"identity"`
	Capabilities Capabilities `json:"capabilities"`
	// DAP is the device access point plugged in slot 0. Its submodules
	// include the identity (subslot 1), the interface (0x8000) and the
	// ports (0x8001...).
	DAP Module `json:"dap"`
	// Slots are the modules plugged in slots 1 and up.
	Slots []Slot `json:"slots"`
	// Catalog lists further module types the device accepts (for GSDML);
	// the plugged ones are added automatically.
	Catalog []Module `json:"catalog,omitempty"`
}

// API is the application process identifier of every submodule this
// model describes (profile-independent API 0).
const API uint32 = 0

// Module returns the module in a slot (slot 0 is the DAP).
func (d *Device) Module(slot uint16) (*Module, bool) {
	if slot == 0 {
		return &d.DAP, true
	}
	for i := range d.Slots {
		if d.Slots[i].Number == slot {
			return &d.Slots[i].Module, true
		}
	}
	return nil, false
}

// Submodule returns a plugged submodule.
func (d *Device) Submodule(slot, subslot uint16) (*Submodule, bool) {
	m, ok := d.Module(slot)
	if !ok {
		return nil, false
	}
	return m.Submodule(subslot)
}

// PluggedSubmodule is a submodule with its position.
type PluggedSubmodule struct {
	Slot        uint16
	ModuleIdent uint32
	Submodule   *Submodule
}

// Each calls fn for every plugged submodule in slot and subslot order,
// the DAP first.
func (d *Device) Each(fn func(p PluggedSubmodule)) {
	for _, m := range d.modules() {
		subs := append([]Submodule(nil), m.mod.Submodules...)
		sort.Slice(subs, func(i, j int) bool { return subs[i].Subslot < subs[j].Subslot })
		for i := range subs {
			s, _ := m.mod.Submodule(subs[i].Subslot)
			fn(PluggedSubmodule{Slot: m.slot, ModuleIdent: m.mod.IdentNumber, Submodule: s})
		}
	}
}

type slotModule struct {
	slot uint16
	mod  *Module
}

func (d *Device) modules() []slotModule {
	out := []slotModule{{0, &d.DAP}}
	idx := make([]int, len(d.Slots))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return d.Slots[idx[a]].Number < d.Slots[idx[b]].Number })
	for _, i := range idx {
		out = append(out, slotModule{d.Slots[i].Number, &d.Slots[i].Module})
	}
	return out
}

// ModuleTypes returns the distinct module types of the slots: the plugged
// modules and the catalog, by ident number. The DAP is not included.
func (d *Device) ModuleTypes() []Module {
	seen := map[uint32]bool{}
	var out []Module
	add := func(m Module) {
		if !seen[m.IdentNumber] {
			seen[m.IdentNumber] = true
			out = append(out, m)
		}
	}
	for _, s := range d.Slots {
		add(s.Module)
	}
	for _, m := range d.Catalog {
		add(m)
	}
	return out
}

// Validate checks the description: unique slots and subslots, a DAP with
// its identity submodule, IO lengths the C_SDU can carry, records in the
// user range.
func (d *Device) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("model: %w: "+format, append([]any{profinet.ErrInvalidConfig}, a...)...)
	}
	if d.Identity.VendorID == 0 && d.Identity.DeviceID == 0 {
		return bad("vendor and device ID are zero")
	}
	if len(d.Identity.OrderID) > pnio.IMOrderIDLen {
		return bad("order ID %q longer than %d", d.Identity.OrderID, pnio.IMOrderIDLen)
	}
	if len(d.Identity.SerialNumber) > pnio.IMSerialLen {
		return bad("serial number %q longer than %d", d.Identity.SerialNumber, pnio.IMSerialLen)
	}
	if _, ok := d.DAP.Submodule(SubslotDAP); !ok {
		return bad("the DAP has no submodule in subslot 1")
	}
	slots := map[uint16]bool{0: true}
	for _, s := range d.Slots {
		if s.Number == 0 {
			return bad("slot 0 is the DAP's")
		}
		if slots[s.Number] {
			return bad("slot %d used twice", s.Number)
		}
		slots[s.Number] = true
	}
	var err error
	for _, m := range d.modules() {
		subslots := map[uint16]bool{}
		for _, s := range m.mod.Submodules {
			if s.Subslot == 0 {
				err = bad("slot %d: subslot 0 is not allowed", m.slot)
			}
			if subslots[s.Subslot] {
				err = bad("slot %d: subslot %d used twice", m.slot, s.Subslot)
			}
			subslots[s.Subslot] = true
			if int(s.InputLen) > 1439 || int(s.OutputLen) > 1439 {
				err = bad("slot %d subslot %d: IO data longer than the C_SDU", m.slot, s.Subslot)
			}
			idx := map[pnio.Index]bool{}
			for _, r := range s.Records {
				if !r.Index.IsUserIndex() {
					err = bad("slot %d subslot %d: record index %s outside 0..0x7FFF", m.slot, s.Subslot, r.Index)
				}
				if idx[r.Index] {
					err = bad("slot %d subslot %d: record index %s twice", m.slot, s.Subslot, r.Index)
				}
				idx[r.Index] = true
				if r.Default != nil && len(r.Default) != int(r.Length) {
					err = bad("slot %d subslot %d: record %s default of %d octets, length %d", m.slot, s.Subslot, r.Index, len(r.Default), r.Length)
				}
			}
			if err != nil {
				return err
			}
		}
	}
	// Each direction must fit one C_SDU: the input CR carries the input
	// (and NO_IO) data with their IOPS and the IOCS of the outputs, the
	// output CR the output data with their IOPS and the IOCS of the
	// inputs.
	in, out := 0, 0
	d.Each(func(p PluggedSubmodule) {
		switch p.Submodule.Type() {
		case pnio.SubmoduleInputOutput:
			in += int(p.Submodule.InputLen) + 2
			out += int(p.Submodule.OutputLen) + 2
		case pnio.SubmoduleOutput:
			out += int(p.Submodule.OutputLen) + 1
			in++
		default:
			in += int(p.Submodule.InputLen) + 1
			out++
		}
	})
	if in > 1440 {
		return bad("input CR of %d octets exceeds the C_SDU maximum 1440", in)
	}
	if out > 1440 {
		return bad("output CR of %d octets exceeds the C_SDU maximum 1440", out)
	}
	if d.Capabilities.MaxARs < 0 {
		return bad("MaxARs %d", d.Capabilities.MaxARs)
	}
	return nil
}

// WithSlots returns a copy of the device with the DAP and only the given
// slots: the expected configuration of an IO controller that shares the
// device with others and owns only these modules. Slots the device does
// not have are ignored.
func (d *Device) WithSlots(slots ...uint16) *Device {
	keep := map[uint16]bool{}
	for _, s := range slots {
		keep[s] = true
	}
	c := *d
	c.Slots = nil
	for _, s := range d.Slots {
		if keep[s.Number] {
			c.Slots = append(c.Slots, s)
		}
	}
	return &c
}

// Expected returns the ExpectedSubmoduleBlockReq blocks (one per slot)
// that describe the plugged configuration, as a controller sends them.
func (d *Device) Expected() []pnio.ExpectedSubmoduleBlockReq {
	var out []pnio.ExpectedSubmoduleBlockReq
	for _, m := range d.modules() {
		em := pnio.ExpectedModule{API: API, Slot: m.slot, IdentNumber: m.mod.IdentNumber}
		for _, s := range m.mod.Submodules {
			t := s.Type()
			em.Submodules = append(em.Submodules, pnio.ExpectedSubmodule{
				Subslot:      s.Subslot,
				IdentNumber:  s.IdentNumber,
				Properties:   uint16(t),
				Descriptions: pnio.NewDataDescriptions(t, s.InputLen, s.OutputLen),
			})
		}
		sort.Slice(em.Submodules, func(i, j int) bool { return em.Submodules[i].Subslot < em.Submodules[j].Subslot })
		out = append(out, pnio.ExpectedSubmoduleBlockReq{Modules: []pnio.ExpectedModule{em}})
	}
	return out
}

// RealIdentification returns the RealIdentificationData of the plugged
// configuration.
func (d *Device) RealIdentification() pnio.IdentificationData {
	api := pnio.IdentAPI{API: API}
	for _, m := range d.modules() {
		sl := pnio.IdentSlot{Slot: m.slot, IdentNumber: m.mod.IdentNumber}
		for _, s := range m.mod.Submodules {
			sl.Submodules = append(sl.Submodules, pnio.IdentSubmodule{Subslot: s.Subslot, IdentNumber: s.IdentNumber})
		}
		sort.Slice(sl.Submodules, func(i, j int) bool { return sl.Submodules[i].Subslot < sl.Submodules[j].Subslot })
		api.Slots = append(api.Slots, sl)
	}
	return pnio.IdentificationData{APIs: []pnio.IdentAPI{api}}
}

// StandardDAP returns a DAP module with the identity submodule (subslot
// 1), one interface (0x8000) and n ports (0x8001...). The ident numbers
// follow the common convention of 0x00000001 for the DAP identity,
// 0x00008000 for the interface and 0x00008001... for the ports.
func StandardDAP(name string, ident uint32, ports int) Module {
	m := Module{Name: name, IdentNumber: ident, Submodules: []Submodule{
		{Name: name, IdentNumber: 0x00000001, Subslot: SubslotDAP},
		{Name: "Interface", IdentNumber: 0x00008000, Subslot: SubslotInterface},
	}}
	for i := 0; i < ports; i++ {
		m.Submodules = append(m.Submodules, Submodule{Name: fmt.Sprintf("Port %d", i+1), IdentNumber: 0x00008001 + uint32(i), Subslot: SubslotPort1 + uint16(i)})
	}
	return m
}
