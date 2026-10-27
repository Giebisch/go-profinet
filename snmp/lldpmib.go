// SPDX-License-Identifier: MIT

package snmp

// The LLDP-MIB of IEEE 802.1AB-2005 (LLDP-MIB-200505060000Z, published by
// the IEEE 802.1 working group): lldpMIB ::= { iso std(0) iso8802(8802)
// ieee802dot1(1) ieee802dot1mibs(1) 2 }, lldpObjects ::= { lldpMIB 1 },
// lldpLocalSystemData ::= { lldpObjects 3 }, lldpRemoteSystemsData ::= {
// lldpObjects 4 }. This file serves the local system data (the chassis
// ID, system name and description and lldpLocPortTable) and lldpRemTable.
// The PNO's PROFINET extension of the LLDP-MIB is not implemented: its
// text is not public.

var (
	// OIDLLDPMIB is lldpMIB, 1.0.8802.1.1.2.
	OIDLLDPMIB = OID{1, 0, 8802, 1, 1, 2}
	// OIDLLDPLocalSystemData is lldpLocalSystemData ::= { lldpObjects 3 }.
	OIDLLDPLocalSystemData = OID{1, 0, 8802, 1, 1, 2, 1, 3}
	// OIDLLDPRemTable is lldpRemTable ::= { lldpRemoteSystemsData 1 }.
	OIDLLDPRemTable = OID{1, 0, 8802, 1, 1, 2, 1, 4, 1}
)

// LLDP-MIB chassis and port ID subtypes (LldpChassisIdSubtype,
// LldpPortIdSubtype); they equal the subtypes of the LLDP TLVs.
const (
	LLDPChassisIDMACAddress int32 = 4
	LLDPChassisIDLocal      int32 = 7
	LLDPPortIDMACAddress    int32 = 3
	LLDPPortIDLocal         int32 = 7
)

// LLDPLocal is the local system data of the LLDP-MIB.
type LLDPLocal struct {
	// ChassisIDSubtype and ChassisID are lldpLocChassisIdSubtype and
	// lldpLocChassisId (1 to 255 octets; an empty ID is left out).
	ChassisIDSubtype int32
	ChassisID        []byte
	// SysName and SysDesc are lldpLocSysName and lldpLocSysDesc.
	SysName, SysDesc string
	// Ports are the rows of lldpLocPortTable.
	Ports []LLDPLocalPort
}

// LLDPLocalPort is a row of lldpLocPortTable, INDEX { lldpLocPortNum }.
type LLDPLocalPort struct {
	// Num is lldpLocPortNum (LldpPortNumber, 1 to 4096).
	Num int32
	// IDSubtype, ID and Desc are lldpLocPortIdSubtype, lldpLocPortId and
	// lldpLocPortDesc.
	IDSubtype int32
	ID        []byte
	Desc      string
}

// LLDPRemote is a row of lldpRemTable, INDEX { lldpRemTimeMark,
// lldpRemLocalPortNum, lldpRemIndex }.
type LLDPRemote struct {
	// TimeMark is lldpRemTimeMark (a TimeFilter; 0 shows the row to a
	// manager that does not filter by time).
	TimeMark uint32
	// LocalPortNum is lldpRemLocalPortNum: the local port (1 to 4096)
	// the neighbor was heard on.
	LocalPortNum int32
	// Index is lldpRemIndex (from 1).
	Index int32
	// The remote system's information: lldpRemChassisIdSubtype,
	// lldpRemChassisId, lldpRemPortIdSubtype, lldpRemPortId,
	// lldpRemPortDesc, lldpRemSysName, lldpRemSysDesc.
	ChassisIDSubtype int32
	ChassisID        []byte
	PortIDSubtype    int32
	PortID           []byte
	PortDesc         string
	SysName, SysDesc string
}

// maxPortNumber is the largest LldpPortNumber.
const maxPortNumber = 4096

// lldpID returns an LldpChassisId or LldpPortId: OCTET STRING (SIZE
// (1..255)); an empty one is no value.
func lldpID(b []byte) Value {
	if len(b) == 0 {
		return Value{}
	}
	return OctetString(b[:min(len(b), 255)])
}

// AddLLDP adds the local system data and lldpRemTable of the LLDP-MIB
// with the values local and remote return; remote may be nil (no
// neighbors). Ports and rows with indexes out of their range are left
// out.
func (m *MIB) AddLLDP(local func() LLDPLocal, remote func() []LLDPRemote) error {
	scalars := []struct {
		sub uint32
		get func(l LLDPLocal) Value
	}{
		// lldpLocChassisIdSubtype ::= { lldpLocalSystemData 1 } and
		// the following.
		{1, func(l LLDPLocal) Value { return Integer(l.ChassisIDSubtype) }},
		{2, func(l LLDPLocal) Value { return lldpID(l.ChassisID) }},
		{3, func(l LLDPLocal) Value { return displayString(l.SysName) }},
		{4, func(l LLDPLocal) Value { return displayString(l.SysDesc) }},
	}
	for _, s := range scalars {
		get := s.get
		if err := m.AddScalar(OIDLLDPLocalSystemData.Append(s.sub), func() Value { return get(local()) }); err != nil {
			return err
		}
	}
	// lldpLocPortTable ::= { lldpLocalSystemData 7 }, lldpLocPortEntry
	// ::= { lldpLocPortTable 1 }; columns 2 to 4 (lldpLocPortNum, 1, is
	// not-accessible).
	ports := OIDLLDPLocalSystemData.Append(7)
	entry := ports.Append(1)
	if err := m.AddSubtree(ports, func() []VarBind {
		var out []VarBind
		for _, p := range local().Ports {
			if p.Num < 1 || p.Num > maxPortNumber {
				continue
			}
			idx := uint32(p.Num)
			out = append(out,
				VarBind{Name: entry.Append(2, idx), Value: Integer(p.IDSubtype)},
				VarBind{Name: entry.Append(3, idx), Value: lldpID(p.ID)},
				VarBind{Name: entry.Append(4, idx), Value: displayString(p.Desc)})
		}
		return out
	}); err != nil {
		return err
	}
	if remote == nil {
		remote = func() []LLDPRemote { return nil }
	}
	// lldpRemEntry ::= { lldpRemTable 1 }; columns 4 to 10 (1 to 3 are
	// the not-accessible index).
	rem := OIDLLDPRemTable.Append(1)
	return m.AddSubtree(OIDLLDPRemTable, func() []VarBind {
		var out []VarBind
		for _, r := range remote() {
			if r.LocalPortNum < 1 || r.LocalPortNum > maxPortNumber || r.Index < 1 {
				continue
			}
			col := func(c uint32, v Value) {
				out = append(out, VarBind{Name: rem.Append(c, r.TimeMark, uint32(r.LocalPortNum), uint32(r.Index)), Value: v})
			}
			col(4, Integer(r.ChassisIDSubtype))
			col(5, lldpID(r.ChassisID))
			col(6, Integer(r.PortIDSubtype))
			col(7, lldpID(r.PortID))
			col(8, displayString(r.PortDesc))
			col(9, displayString(r.SysName))
			col(10, displayString(r.SysDesc))
		}
		return out
	})
}
