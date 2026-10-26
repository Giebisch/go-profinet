// SPDX-License-Identifier: MIT

// Package lldp implements the Link Layer Discovery Protocol (IEEE 802.1AB)
// with the organizationally specific TLVs PROFINET uses (IEC 61158-6-10:
// OUI 00-0E-CF) and the IEEE 802.3 MAC/PHY TLV: the frame codec, and an
// agent that announces the local port and keeps a table of neighbors with
// expiry.
//
// PROFINET builds topology on LLDP: a device derives its AliasName from
// its neighbor's port and chassis IDs, and controllers read the neighbor
// table (PDPortDataReal) to check the wiring. Generic LLDP describes
// links; the PROFINET TLVs add the RT class port status, the chassis MAC
// and the line delay.
package lldp

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// TLV types.
const (
	TypeEnd               uint8 = 0
	TypeChassisID         uint8 = 1
	TypePortID            uint8 = 2
	TypeTTL               uint8 = 3
	TypePortDescription   uint8 = 4
	TypeSystemName        uint8 = 5
	TypeSystemDescription uint8 = 6
	TypeCapabilities      uint8 = 7
	TypeManagementAddress uint8 = 8
	TypeOrgSpecific       uint8 = 127
)

// Chassis and port ID subtypes.
const (
	ChassisSubtypeMAC   uint8 = 4
	ChassisSubtypeLocal uint8 = 7
	PortSubtypeMAC      uint8 = 3
	PortSubtypeLocal    uint8 = 7
)

// Organizationally unique identifiers.
var (
	OUIProfinet = [3]byte{0x00, 0x0E, 0xCF}
	OUIIEEE8023 = [3]byte{0x00, 0x12, 0x0F}
)

// PROFINET TLV subtypes (OUI 00-0E-CF).
const (
	PNSubtypeDelay         uint8 = 1
	PNSubtypePortStatus    uint8 = 2
	PNSubtypeAlias         uint8 = 3
	PNSubtypeMRPPortStatus uint8 = 4
	PNSubtypeChassisMAC    uint8 = 5
	PNSubtypePTCPStatus    uint8 = 6
)

// TLV is an undecoded TLV. Value aliases the decoded buffer.
type TLV struct {
	Type  uint8
	Value []byte
}

// PortStatus is the PROFINET PortStatus TLV: the state of RT_CLASS_2 and
// RT_CLASS_3 on the port (0 off).
type PortStatus struct {
	RTClass2 uint16 `json:"rtClass2"`
	RTClass3 uint16 `json:"rtClass3"`
}

// Delay is the PROFINET measured delay TLV, in nanoseconds.
type Delay struct {
	RxLocal, RxRemote, TxLocal, TxRemote, Cable uint32
}

// MACPHY is the IEEE 802.3 MAC/PHY configuration/status TLV.
type MACPHY struct {
	// Autoneg: bit 0 supported, bit 1 enabled.
	Autoneg uint8 `json:"autoneg"`
	// PMDCapability is the advertised capability bitmap.
	PMDCapability uint16 `json:"pmdCapability"`
	// MAUType is the operational MAU type (RFC 4836), 16 for
	// 100BASE-TX full duplex.
	MAUType uint16 `json:"mauType"`
}

// Frame is a decoded LLDP data unit.
type Frame struct {
	ChassisSubtype uint8  `json:"chassisSubtype"`
	ChassisID      []byte `json:"chassisId"`
	PortSubtype    uint8  `json:"portSubtype"`
	PortID         []byte `json:"portId"`
	// TTL is the time to live in seconds; 0 withdraws the information.
	TTL               uint16        `json:"ttl"`
	PortDescription   string        `json:"portDescription,omitempty"`
	SystemName        string        `json:"systemName,omitempty"`
	SystemDescription string        `json:"systemDescription,omitempty"`
	ManagementAddr    netip.Addr    `json:"managementAddress,omitempty"`
	PortStatus        *PortStatus   `json:"portStatus,omitempty"`
	ChassisMAC        *ethernet.MAC `json:"chassisMac,omitempty"`
	Delay             *Delay        `json:"delay,omitempty"`
	MACPHY            *MACPHY       `json:"macPhy,omitempty"`
	// Other holds the TLVs this package does not decode.
	Other []TLV `json:"-"`
}

// ChassisString returns the chassis ID as text: a MAC address for the MAC
// subtype, the octets otherwise.
func (f *Frame) ChassisString() string {
	return idString(f.ChassisSubtype, ChassisSubtypeMAC, f.ChassisID)
}

// PortString returns the port ID as text.
func (f *Frame) PortString() string { return idString(f.PortSubtype, PortSubtypeMAC, f.PortID) }

func idString(sub, macSub uint8, b []byte) string {
	if sub == macSub && len(b) == 6 {
		return ethernet.MAC(b).String()
	}
	return string(b)
}

func appendTLV(b []byte, t uint8, v []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(t)<<9|uint16(len(v)))
	return append(b, v...)
}

func org(oui [3]byte, sub uint8, v []byte) []byte {
	return append([]byte{oui[0], oui[1], oui[2], sub}, v...)
}

// AppendTo appends the encoded LLDPDU (the Ethernet payload) to dst.
func (f *Frame) AppendTo(dst []byte) ([]byte, error) {
	if len(f.ChassisID) < 1 || len(f.ChassisID) > 255 || len(f.PortID) < 1 || len(f.PortID) > 255 {
		return dst, fmt.Errorf("lldp: %w: chassis and port IDs must be 1..255 octets", profinet.ErrInvalidConfig)
	}
	start := len(dst)
	dst = appendTLV(dst, TypeChassisID, append([]byte{f.ChassisSubtype}, f.ChassisID...))
	dst = appendTLV(dst, TypePortID, append([]byte{f.PortSubtype}, f.PortID...))
	dst = appendTLV(dst, TypeTTL, binary.BigEndian.AppendUint16(nil, f.TTL))
	for _, s := range []struct {
		t uint8
		v string
	}{{TypePortDescription, f.PortDescription}, {TypeSystemName, f.SystemName}, {TypeSystemDescription, f.SystemDescription}} {
		if s.v != "" {
			v := s.v
			if len(v) > 255 {
				v = v[:255]
			}
			dst = appendTLV(dst, s.t, []byte(v))
		}
	}
	if f.PortStatus != nil {
		v := binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, f.PortStatus.RTClass2), f.PortStatus.RTClass3)
		dst = appendTLV(dst, TypeOrgSpecific, org(OUIProfinet, PNSubtypePortStatus, v))
	}
	if f.ChassisMAC != nil {
		dst = appendTLV(dst, TypeOrgSpecific, org(OUIProfinet, PNSubtypeChassisMAC, f.ChassisMAC[:]))
	}
	if f.Delay != nil {
		var v []byte
		for _, x := range []uint32{f.Delay.RxLocal, f.Delay.RxRemote, f.Delay.TxLocal, f.Delay.TxRemote, f.Delay.Cable} {
			v = binary.BigEndian.AppendUint32(v, x)
		}
		dst = appendTLV(dst, TypeOrgSpecific, org(OUIProfinet, PNSubtypeDelay, v))
	}
	if f.MACPHY != nil {
		v := []byte{f.MACPHY.Autoneg}
		v = binary.BigEndian.AppendUint16(v, f.MACPHY.PMDCapability)
		v = binary.BigEndian.AppendUint16(v, f.MACPHY.MAUType)
		dst = appendTLV(dst, TypeOrgSpecific, org(OUIIEEE8023, 1, v))
	}
	if f.ManagementAddr.Is4() {
		a := f.ManagementAddr.As4()
		v := []byte{5, 1, a[0], a[1], a[2], a[3], 2, 0, 0, 0, 1, 0}
		dst = appendTLV(dst, TypeManagementAddress, v)
	}
	for _, t := range f.Other {
		if len(t.Value) > 511 {
			return dst[:start], fmt.Errorf("lldp: %w: TLV of %d octets", profinet.ErrInvalidConfig, len(t.Value))
		}
		// The type field has 7 bits, and an End TLV would cut the LLDPDU.
		if t.Type == TypeEnd || t.Type > TypeOrgSpecific {
			return dst[:start], fmt.Errorf("lldp: %w: TLV type %d", profinet.ErrInvalidConfig, t.Type)
		}
		dst = appendTLV(dst, t.Type, t.Value)
	}
	return appendTLV(dst, TypeEnd, nil), nil
}

// Decode decodes an LLDPDU. The mandatory Chassis ID, Port ID and TTL
// TLVs must come first, in that order; decoding stops at the End TLV (or
// the end of b, since padding may follow a missing End TLV).
func Decode(b []byte) (Frame, error) {
	var f Frame
	i := 0
	for len(b) >= 2 {
		h := binary.BigEndian.Uint16(b)
		t := uint8(h >> 9)
		n := int(h & 0x01FF)
		if 2+n > len(b) {
			return f, fmt.Errorf("lldp: %w: TLV %d of length %d exceeds the %d octets left", profinet.ErrMalformed, t, n, len(b)-2)
		}
		v := b[2 : 2+n]
		b = b[2+n:]
		switch i {
		case 0, 1, 2:
			want := [...]uint8{TypeChassisID, TypePortID, TypeTTL}[i]
			if t != want {
				return f, fmt.Errorf("lldp: %w: TLV %d where TLV %d is mandatory", profinet.ErrMalformed, t, want)
			}
		}
		i++
		switch t {
		case TypeEnd:
			return f, nil
		case TypeChassisID:
			if n < 2 {
				return f, fmt.Errorf("lldp: %w: chassis ID of %d octets", profinet.ErrMalformed, n)
			}
			f.ChassisSubtype, f.ChassisID = v[0], append([]byte(nil), v[1:]...)
		case TypePortID:
			if n < 2 {
				return f, fmt.Errorf("lldp: %w: port ID of %d octets", profinet.ErrMalformed, n)
			}
			f.PortSubtype, f.PortID = v[0], append([]byte(nil), v[1:]...)
		case TypeTTL:
			if n < 2 {
				return f, fmt.Errorf("lldp: %w: TTL of %d octets", profinet.ErrMalformed, n)
			}
			f.TTL = binary.BigEndian.Uint16(v)
		case TypePortDescription:
			f.PortDescription = string(v)
		case TypeSystemName:
			f.SystemName = string(v)
		case TypeSystemDescription:
			f.SystemDescription = string(v)
		case TypeManagementAddress:
			if n >= 6 && v[0] == 5 && v[1] == 1 {
				f.ManagementAddr = netip.AddrFrom4([4]byte(v[2:6]))
			}
		case TypeOrgSpecific:
			if !f.decodeOrg(v) {
				f.Other = append(f.Other, TLV{Type: t, Value: append([]byte(nil), v...)})
			}
		default:
			f.Other = append(f.Other, TLV{Type: t, Value: append([]byte(nil), v...)})
		}
	}
	if i < 3 {
		return f, fmt.Errorf("lldp: %w: LLDPDU without its mandatory TLVs", profinet.ErrMalformed)
	}
	return f, nil
}

func (f *Frame) decodeOrg(v []byte) bool {
	if len(v) < 4 {
		return false
	}
	oui, sub, d := [3]byte(v[:3]), v[3], v[4:]
	switch {
	case oui == OUIProfinet && sub == PNSubtypePortStatus && len(d) >= 4:
		f.PortStatus = &PortStatus{RTClass2: binary.BigEndian.Uint16(d), RTClass3: binary.BigEndian.Uint16(d[2:])}
	case oui == OUIProfinet && sub == PNSubtypeChassisMAC && len(d) >= 6:
		m := ethernet.MAC(d[:6])
		f.ChassisMAC = &m
	case oui == OUIProfinet && sub == PNSubtypeDelay && len(d) >= 20:
		f.Delay = &Delay{
			RxLocal: binary.BigEndian.Uint32(d), RxRemote: binary.BigEndian.Uint32(d[4:]),
			TxLocal: binary.BigEndian.Uint32(d[8:]), TxRemote: binary.BigEndian.Uint32(d[12:]),
			Cable: binary.BigEndian.Uint32(d[16:]),
		}
	case oui == OUIIEEE8023 && sub == 1 && len(d) >= 5:
		f.MACPHY = &MACPHY{Autoneg: d[0], PMDCapability: binary.BigEndian.Uint16(d[1:]), MAUType: binary.BigEndian.Uint16(d[3:])}
	default:
		return false
	}
	return true
}
