// SPDX-License-Identifier: MIT

package snmp

import (
	"time"
	"unicode/utf8"
)

// OIDs of MIB-II (RFC 1213 section 6, SNMPv2-MIB of RFC 3418, IF-MIB of
// RFC 2863).
var (
	// OIDSystem is the system group, mib-2 1 (RFC 3418 section 2).
	OIDSystem = OID{1, 3, 6, 1, 2, 1, 1}
	// OIDInterfaces is the interfaces group, mib-2 2 (RFC 2863
	// section 6).
	OIDInterfaces = OID{1, 3, 6, 1, 2, 1, 2}
	// OIDSNMP is the snmp group, mib-2 11 (RFC 3418 section 2).
	OIDSNMP = OID{1, 3, 6, 1, 2, 1, 11}
	// OIDZeroDotZero is zeroDotZero (RFC 2578 section 2), the
	// sysObjectID of a System without one.
	OIDZeroDotZero = OID{0, 0}
)

// System is the system group of MIB-II.
type System struct {
	// Descr is sysDescr, "a textual description of the entity".
	Descr string
	// ObjectID is sysObjectID, the vendor's identification of the
	// entity; nil serves zeroDotZero.
	ObjectID OID
	// UpTime is sysUpTime: the time since the agent's network
	// management portion was (re)initialized.
	UpTime time.Duration
	// Contact, Name and Location are sysContact, sysName and
	// sysLocation.
	Contact, Name, Location string
	// Services is sysServices: the sum of 2^(L−1) over the layers L
	// the entity serves. "a node which is a host offering application
	// services would have a value of 72 (2^(4-1) + 2^(7-1))" (RFC 3418,
	// sysServices); zero serves 72.
	Services int32
}

// DisplayString and SnmpAdminString are at most 255 octets (RFC 2579
// section 2, RFC 3411 section 5).
const maxDisplayString = 255

// displayString returns s cut to 255 octets at a character boundary.
func displayString(s string) Value {
	if len(s) > maxDisplayString {
		s = s[:maxDisplayString]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return String(s)
}

// ticks converts a duration to TimeTicks, hundredths of a second, which
// wrap at 2^32 (RFC 2578 section 7.1.8).
func ticks(d time.Duration) Value {
	if d < 0 {
		d = 0
	}
	return TimeTicks(uint32(uint64(d / (10 * time.Millisecond))))
}

// AddSystem adds the system group (sysDescr to sysServices, RFC 3418
// section 2) with the values fn returns.
func (m *MIB) AddSystem(fn func() System) error {
	objs := []struct {
		sub uint32
		get func(s System) Value
	}{
		{1, func(s System) Value { return displayString(s.Descr) }},
		{2, func(s System) Value {
			if len(s.ObjectID) == 0 {
				return ObjectIdentifier(OIDZeroDotZero)
			}
			return ObjectIdentifier(s.ObjectID)
		}},
		{3, func(s System) Value { return ticks(s.UpTime) }},
		{4, func(s System) Value { return displayString(s.Contact) }},
		{5, func(s System) Value { return displayString(s.Name) }},
		{6, func(s System) Value { return displayString(s.Location) }},
		{7, func(s System) Value {
			if s.Services <= 0 || s.Services > 127 {
				return Integer(72)
			}
			return Integer(s.Services)
		}},
	}
	for _, o := range objs {
		get := o.get
		if err := m.AddScalar(OIDSystem.Append(o.sub), func() Value { return get(fn()) }); err != nil {
			return err
		}
	}
	return nil
}

// Values of the interfaces group (RFC 2863 section 6, IANAifType-MIB).
const (
	IfTypeEthernetCsmacd int32 = 6
	IfStatusUp           int32 = 1
	IfStatusDown         int32 = 2
)

// Interface is a row of the ifTable (RFC 2863 section 6).
type Interface struct {
	// Index is ifIndex, from 1.
	Index int32
	// Descr is ifDescr.
	Descr string
	// Type is ifType (IfTypeEthernetCsmacd).
	Type int32
	// MTU is ifMtu in octets.
	MTU int32
	// Speed is ifSpeed in bit/s.
	Speed uint32
	// PhysAddress is ifPhysAddress, the MAC address.
	PhysAddress []byte
	// AdminStatus and OperStatus are ifAdminStatus and ifOperStatus
	// (IfStatusUp, IfStatusDown).
	AdminStatus, OperStatus int32
	// LastChange is ifLastChange: the sysUpTime when the interface
	// entered its operational state.
	LastChange time.Duration
	// Counters are the traffic counters; nil leaves their columns out.
	Counters *InterfaceCounters
}

// InterfaceCounters are the Counter32 columns of the ifTable.
type InterfaceCounters struct {
	InOctets, InUcastPkts, InDiscards, InErrors     uint32
	OutOctets, OutUcastPkts, OutDiscards, OutErrors uint32
}

// AddInterfaces adds ifNumber and the ifTable (RFC 2863 section 6) with
// the interfaces fn returns. Rows whose Index is below 1 are left out.
func (m *MIB) AddInterfaces(fn func() []Interface) error {
	if err := m.AddScalar(OIDInterfaces.Append(1), func() Value { return Integer(int32(len(fn()))) }); err != nil {
		return err
	}
	entry := OIDInterfaces.Append(2, 1)
	return m.AddSubtree(OIDInterfaces.Append(2), func() []VarBind {
		var out []VarBind
		for _, it := range fn() {
			if it.Index < 1 {
				continue
			}
			col := func(c uint32, v Value) { out = append(out, VarBind{Name: entry.Append(c, uint32(it.Index)), Value: v}) }
			col(1, Integer(it.Index))
			col(2, displayString(it.Descr))
			col(3, Integer(it.Type))
			col(4, Integer(it.MTU))
			col(5, Gauge32(it.Speed))
			col(6, OctetString(it.PhysAddress))
			col(7, Integer(it.AdminStatus))
			col(8, Integer(it.OperStatus))
			col(9, ticks(it.LastChange))
			if c := it.Counters; c != nil {
				col(10, Counter32(c.InOctets))
				col(11, Counter32(c.InUcastPkts))
				col(13, Counter32(c.InDiscards))
				col(14, Counter32(c.InErrors))
				col(16, Counter32(c.OutOctets))
				col(17, Counter32(c.OutUcastPkts))
				col(19, Counter32(c.OutDiscards))
				col(20, Counter32(c.OutErrors))
			}
		}
		return out
	})
}

// AddSNMPGroup adds the snmp group (RFC 3418 section 2; the counters
// RFC 3418 marks obsolete as RFC 1213 section 6.11 defines them) with the
// counters fn returns, usually an Agent's Stats. snmpEnableAuthenTraps is
// disabled(2): the agent sends no traps.
func (m *MIB) AddSNMPGroup(fn func() Stats) error {
	objs := []struct {
		sub uint32
		get func(s Stats) uint64
	}{
		{1, func(s Stats) uint64 { return s.InPkts }},
		{2, func(s Stats) uint64 { return s.OutPkts }},
		{3, func(s Stats) uint64 { return s.InBadVersions }},
		{4, func(s Stats) uint64 { return s.InBadCommunityNames }},
		{5, func(s Stats) uint64 { return s.InBadCommunityUses }},
		{6, func(s Stats) uint64 { return s.InASNParseErrs }},
		{15, func(s Stats) uint64 { return s.InGetRequests }},
		{16, func(s Stats) uint64 { return s.InGetNexts }},
		{17, func(s Stats) uint64 { return s.InSetRequests }},
		{20, func(s Stats) uint64 { return s.OutTooBigs }},
		{21, func(s Stats) uint64 { return s.OutNoSuchNames }},
		{28, func(s Stats) uint64 { return s.OutGetResponses }},
		{31, func(s Stats) uint64 { return s.SilentDrops }},
		// snmpProxyDrops: the agent is no proxy.
		{32, func(Stats) uint64 { return 0 }},
	}
	for _, o := range objs {
		get := o.get
		// Counter32 wraps at 2^32 (RFC 2578 section 7.1.6).
		if err := m.AddScalar(OIDSNMP.Append(o.sub), func() Value { return Counter32(uint32(get(fn()))) }); err != nil {
			return err
		}
	}
	return m.AddScalar(OIDSNMP.Append(30), func() Value { return Integer(2) })
}
