// SPDX-License-Identifier: MIT

// Package mrp encodes and decodes the frames of the Media Redundancy
// Protocol (IEC 62439-2) that PROFINET rings use: MRP_Test,
// MRP_TopologyChange, MRP_LinkDown and MRP_LinkUp with MRP_Common and
// MRP_Option. The layouts are those of Wireshark's MRP dissector, the Linux
// kernel's MRP UAPI header and microchip-ung/mrp
// (docs/research/EVIDENCE-MRP-PTCP-MGMT.md, A.1). It has no state machine:
// it is the codec a ring manager or client is built on, and it lets tools
// read the MRP traffic of a ring.
package mrp

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// EtherType is the EtherType of MRP.
const EtherType ethernet.EtherType = 0x88E3

// Destination addresses.
var (
	// MACTest receives MRP_Test frames.
	MACTest = ethernet.MAC{0x01, 0x15, 0x4E, 0x00, 0x00, 0x01}
	// MACControl receives MRP_TopologyChange, MRP_LinkDown and MRP_LinkUp.
	MACControl = ethernet.MAC{0x01, 0x15, 0x4E, 0x00, 0x00, 0x02}
	// MACInTest and MACInControl are the interconnection counterparts.
	MACInTest    = ethernet.MAC{0x01, 0x15, 0x4E, 0x00, 0x00, 0x03}
	MACInControl = ethernet.MAC{0x01, 0x15, 0x4E, 0x00, 0x00, 0x04}
)

// Version is the MRP_Version this package sends.
const Version uint16 = 1

// Type is the type of a TLV.
type Type uint8

// TLV types.
const (
	TypeEnd              Type = 0x00
	TypeCommon           Type = 0x01
	TypeTest             Type = 0x02
	TypeTopologyChange   Type = 0x03
	TypeLinkDown         Type = 0x04
	TypeLinkUp           Type = 0x05
	TypeInTest           Type = 0x06
	TypeInTopologyChange Type = 0x07
	TypeInLinkDown       Type = 0x08
	TypeInLinkUp         Type = 0x09
	TypeInLinkStatusPoll Type = 0x0A
	TypeOption           Type = 0x7F
)

// String returns the TLV's name.
func (t Type) String() string {
	switch t {
	case TypeEnd:
		return "MRP_End"
	case TypeCommon:
		return "MRP_Common"
	case TypeTest:
		return "MRP_Test"
	case TypeTopologyChange:
		return "MRP_TopologyChange"
	case TypeLinkDown:
		return "MRP_LinkDown"
	case TypeLinkUp:
		return "MRP_LinkUp"
	case TypeInTest:
		return "MRP_InTest"
	case TypeInTopologyChange:
		return "MRP_InTopologyChange"
	case TypeInLinkDown:
		return "MRP_InLinkDown"
	case TypeInLinkUp:
		return "MRP_InLinkUp"
	case TypeInLinkStatusPoll:
		return "MRP_InLinkStatusPoll"
	case TypeOption:
		return "MRP_Option"
	default:
		return fmt.Sprintf("MRP_TLV(0x%02X)", uint8(t))
	}
}

// PortRole is the role of a ring port.
type PortRole uint16

// Port roles.
const (
	PortPrimary         PortRole = 0
	PortSecondary       PortRole = 1
	PortInterconnection PortRole = 2
)

// RingState is the state of the ring a manager reports.
type RingState uint16

// Ring states.
const (
	RingOpen   RingState = 0
	RingClosed RingState = 1
)

// DefaultManagerPrio is the default priority of a ring manager; a lower
// value is a higher priority.
const DefaultManagerPrio uint16 = 0x8000

// DefaultDomain is the default MRP domain UUID (all ones), as the Linux
// kernel and microchip-ung/mrp use it.
var DefaultDomain = [16]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

// Test is MRP_Test: a ring manager's test frame.
type Test struct {
	Prio       uint16
	SA         ethernet.MAC
	PortRole   PortRole
	RingState  RingState
	Transition uint16
	// TimeStamp is the sender's time in milliseconds.
	TimeStamp uint32
}

// TopologyChange is MRP_TopologyChange: the manager tells the clients to
// flush their forwarding tables after Interval milliseconds.
type TopologyChange struct {
	Prio     uint16
	SA       ethernet.MAC
	Interval uint16
}

// LinkChange is MRP_LinkDown or MRP_LinkUp: a client reports a ring port's
// link.
type LinkChange struct {
	// Up selects MRP_LinkUp.
	Up       bool
	SA       ethernet.MAC
	PortRole PortRole
	// Interval is in milliseconds.
	Interval uint16
	// Blocked is 1 when the client can receive and forward on a blocked
	// port, 0 when it cannot.
	Blocked uint16
}

// Common is MRP_Common: the sequence number and the domain.
type Common struct {
	SequenceID uint16
	Domain     [16]byte
}

// Option is MRP_Option: an organisation's data.
type Option struct {
	OUI  [3]byte
	Data []byte
}

// TLV is a TLV this package does not decode (the interconnection TLVs).
type TLV struct {
	Type  Type
	Value []byte
}

// Frame is an MRP PDU (after the Ethernet header).
type Frame struct {
	Version        uint16
	Test           *Test
	TopologyChange *TopologyChange
	Link           *LinkChange
	Common         *Common
	Options        []Option
	Other          []TLV
}

// padded reports whether a TLV of type t is followed by padding to four
// octets (counted from the first TLV), as Wireshark's dissector reads it.
func padded(t Type) bool {
	switch t {
	case TypeTest, TypeLinkDown, TypeLinkUp, TypeInTest, TypeInLinkDown, TypeInLinkUp, TypeOption:
		return true
	default:
		return false
	}
}

func appendTLV(b []byte, start int, t Type, value []byte) []byte {
	b = append(b, byte(t), byte(len(value)))
	b = append(b, value...)
	if padded(t) {
		for (len(b)-start)%4 != 0 {
			b = append(b, 0)
		}
	}
	return b
}

// AppendTo appends the encoded PDU: MRP_Version, the frame's TLV
// (MRP_Test, MRP_TopologyChange or a link change), its options and other
// TLVs, MRP_Common and MRP_End.
func (f *Frame) AppendTo(b []byte) ([]byte, error) {
	v := f.Version
	if v == 0 {
		v = Version
	}
	b = binary.BigEndian.AppendUint16(b, v)
	start := len(b)
	be := binary.BigEndian
	if t := f.Test; t != nil {
		val := be.AppendUint16(nil, t.Prio)
		val = append(val, t.SA[:]...)
		val = be.AppendUint16(val, uint16(t.PortRole))
		val = be.AppendUint16(val, uint16(t.RingState))
		val = be.AppendUint16(val, t.Transition)
		val = be.AppendUint32(val, t.TimeStamp)
		b = appendTLV(b, start, TypeTest, val)
	}
	if t := f.TopologyChange; t != nil {
		val := be.AppendUint16(nil, t.Prio)
		val = append(val, t.SA[:]...)
		val = be.AppendUint16(val, t.Interval)
		b = appendTLV(b, start, TypeTopologyChange, val)
	}
	if l := f.Link; l != nil {
		t := TypeLinkDown
		if l.Up {
			t = TypeLinkUp
		}
		val := append([]byte(nil), l.SA[:]...)
		val = be.AppendUint16(val, uint16(l.PortRole))
		val = be.AppendUint16(val, l.Interval)
		val = be.AppendUint16(val, l.Blocked)
		b = appendTLV(b, start, t, val)
	}
	for _, o := range f.Options {
		if 3+len(o.Data) > 255 {
			return nil, fmt.Errorf("mrp: %w: option of %d octets", profinet.ErrInvalidConfig, 3+len(o.Data))
		}
		b = appendTLV(b, start, TypeOption, append(o.OUI[:], o.Data...))
	}
	for _, t := range f.Other {
		if len(t.Value) > 255 || t.Type == TypeEnd {
			return nil, fmt.Errorf("mrp: %w: TLV %s of %d octets", profinet.ErrInvalidConfig, t.Type, len(t.Value))
		}
		b = appendTLV(b, start, t.Type, t.Value)
	}
	if c := f.Common; c != nil {
		val := be.AppendUint16(nil, c.SequenceID)
		val = append(val, c.Domain[:]...)
		b = appendTLV(b, start, TypeCommon, val)
	}
	return append(b, byte(TypeEnd), 0), nil
}

// Decode decodes an MRP PDU (after the Ethernet header). It accepts
// TLVs with and without the padding to four octets (Wireshark's layout
// and microchip-ung/mrp's differ for MRP_LinkDown/Up) and ignores what
// follows MRP_End (the Ethernet padding).
func Decode(b []byte) (Frame, error) {
	var f Frame
	if len(b) < 2 {
		return f, fmt.Errorf("mrp: %w: %d octets", profinet.ErrMalformed, len(b))
	}
	f.Version = binary.BigEndian.Uint16(b)
	data := b[2:]
	off := 0
	be := binary.BigEndian
	need := func(t Type, v []byte, n int) error {
		if len(v) < n {
			return fmt.Errorf("mrp: %w: %s of %d octets, at least %d", profinet.ErrMalformed, t, len(v), n)
		}
		return nil
	}
	for {
		if off+2 > len(data) {
			return f, fmt.Errorf("mrp: %w: no MRP_End", profinet.ErrMalformed)
		}
		t, l := Type(data[off]), int(data[off+1])
		if t == TypeEnd {
			return f, nil
		}
		if off+2+l > len(data) {
			return f, fmt.Errorf("mrp: %w: %s of length %d exceeds the frame", profinet.ErrMalformed, t, l)
		}
		v := data[off+2 : off+2+l]
		off += 2 + l
		switch t {
		case TypeTest:
			if err := need(t, v, 18); err != nil {
				return f, err
			}
			f.Test = &Test{Prio: be.Uint16(v), SA: ethernet.MAC(v[2:8]), PortRole: PortRole(be.Uint16(v[8:])),
				RingState: RingState(be.Uint16(v[10:])), Transition: be.Uint16(v[12:]), TimeStamp: be.Uint32(v[14:])}
		case TypeTopologyChange:
			if err := need(t, v, 10); err != nil {
				return f, err
			}
			f.TopologyChange = &TopologyChange{Prio: be.Uint16(v), SA: ethernet.MAC(v[2:8]), Interval: be.Uint16(v[8:])}
		case TypeLinkDown, TypeLinkUp:
			if err := need(t, v, 12); err != nil {
				return f, err
			}
			f.Link = &LinkChange{Up: t == TypeLinkUp, SA: ethernet.MAC(v[0:6]), PortRole: PortRole(be.Uint16(v[6:])),
				Interval: be.Uint16(v[8:]), Blocked: be.Uint16(v[10:])}
		case TypeCommon:
			if err := need(t, v, 18); err != nil {
				return f, err
			}
			c := &Common{SequenceID: be.Uint16(v)}
			copy(c.Domain[:], v[2:18])
			f.Common = c
		case TypeOption:
			if err := need(t, v, 3); err != nil {
				return f, err
			}
			f.Options = append(f.Options, Option{OUI: [3]byte(v[:3]), Data: append([]byte(nil), v[3:]...)})
		default:
			f.Other = append(f.Other, TLV{Type: t, Value: append([]byte(nil), v...)})
		}
		// Padding to four octets, when the zeros are padding: what
		// follows them is a TLV other than MRP_End.
		if pad := (4 - off%4) % 4; padded(t) && pad > 0 && off+pad+2 <= len(data) {
			zeros := true
			for _, c := range data[off : off+pad] {
				zeros = zeros && c == 0
			}
			if zeros && data[off+pad] != byte(TypeEnd) {
				off += pad
			}
		}
	}
}
