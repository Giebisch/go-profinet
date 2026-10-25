// SPDX-License-Identifier: MIT

// Package alarm implements the acyclic real-time transport of PROFINET
// alarms (RTA, IEC 61158-6-10): the codec of RTA PDUs and the alarm
// protocol machines of one AR — a sender (APMS) and a receiver (APMR) per
// priority, with sequence numbers, acknowledgement, retransmission and
// the ERR PDU that aborts an AR.
//
// RTA PDUs travel on EtherType 0x8892 with FrameID 0xFC01 (high priority)
// or 0xFE01 (low priority), tagged with 802.1Q priority 6 or 5.
package alarm

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/pnio"
)

// PDUType is the type of an RTA PDU.
type PDUType uint8

// PDU types.
const (
	PDUData PDUType = 1
	PDUNack PDUType = 2
	PDUAck  PDUType = 3
	PDUErr  PDUType = 4
)

func (t PDUType) String() string {
	switch t {
	case PDUData:
		return "DATA"
	case PDUNack:
		return "NACK"
	case PDUAck:
		return "ACK"
	case PDUErr:
		return "ERR"
	default:
		return fmt.Sprintf("PDUType(%d)", uint8(t))
	}
}

// Version is the RTA version in the high nibble of the PDU type octet.
const Version = 1

// HeaderLen is the length of the RTA header after the FrameID, the
// VarPartLen included.
const HeaderLen = 12

// AddFlags bits.
const (
	WindowSize uint8 = 0x01
	FlagTACK   uint8 = 0x10
)

// Initial sequence numbers: the first DATA PDU has sequence number 0, and
// the acknowledgement of "nothing received yet" is 0xFFFE.
const (
	InitialSendSeq uint16 = 0xFFFF
	InitialAckSeq  uint16 = 0xFFFE
)

// nextSeq returns the sequence number after s (15-bit wrap, 0xFFFF
// becomes 0).
func nextSeq(s uint16) uint16 { return (s + 1) & 0x7FFF }

// PDU is a decoded RTA PDU.
type PDU struct {
	FrameID  uint16
	Dst, Src uint16
	Type     PDUType
	Version  uint8
	AddFlags uint8
	SendSeq  uint16
	AckSeq   uint16
	// VarPart is the payload: the alarm notification or ack block of a
	// DATA PDU, the PNIOStatus of an ERR PDU. It aliases the decoded
	// buffer.
	VarPart []byte
}

// TACK reports whether the sender asks for a transport acknowledgement.
func (p *PDU) TACK() bool { return p.AddFlags&FlagTACK != 0 }

// Decode decodes the PROFINET payload (FrameID first) of an RTA PDU.
func Decode(b []byte) (PDU, error) {
	var p PDU
	if len(b) < 2+HeaderLen {
		return p, fmt.Errorf("alarm: %w: RTA PDU of %d octets", profinet.ErrMalformed, len(b))
	}
	p.FrameID = binary.BigEndian.Uint16(b)
	if p.FrameID != pnio.FrameIDAlarmHigh && p.FrameID != pnio.FrameIDAlarmLow {
		return p, fmt.Errorf("alarm: %w: FrameID 0x%04X", profinet.ErrMalformed, p.FrameID)
	}
	p.Dst = binary.BigEndian.Uint16(b[2:])
	p.Src = binary.BigEndian.Uint16(b[4:])
	p.Type = PDUType(b[6] & 0x0F)
	p.Version = b[6] >> 4
	p.AddFlags = b[7]
	p.SendSeq = binary.BigEndian.Uint16(b[8:])
	p.AckSeq = binary.BigEndian.Uint16(b[10:])
	n := int(binary.BigEndian.Uint16(b[12:]))
	if p.Version != Version {
		return p, fmt.Errorf("alarm: %w: RTA version %d", profinet.ErrUnsupported, p.Version)
	}
	if p.Type < PDUData || p.Type > PDUErr {
		return p, fmt.Errorf("alarm: %w: PDU type %d", profinet.ErrMalformed, p.Type)
	}
	if n > len(b)-14 {
		return p, fmt.Errorf("alarm: %w: VarPartLen %d exceeds the %d octets present", profinet.ErrMalformed, n, len(b)-14)
	}
	p.VarPart = b[14 : 14+n]
	return p, nil
}

// AppendPayload appends the PROFINET payload of the PDU.
func (p *PDU) AppendPayload(dst []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, p.FrameID)
	dst = binary.BigEndian.AppendUint16(dst, p.Dst)
	dst = binary.BigEndian.AppendUint16(dst, p.Src)
	v := p.Version
	if v == 0 {
		v = Version
	}
	dst = append(dst, v<<4|uint8(p.Type)&0x0F, p.AddFlags)
	dst = binary.BigEndian.AppendUint16(dst, p.SendSeq)
	dst = binary.BigEndian.AppendUint16(dst, p.AckSeq)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(p.VarPart)))
	return append(dst, p.VarPart...)
}

// Priority returns the VLAN priority of the PDU's FrameID: 6 high, 5 low.
func Priority(frameID uint16) uint8 {
	if frameID == pnio.FrameIDAlarmHigh {
		return 6
	}
	return 5
}

// AppendEthernet appends the complete tagged Ethernet frame of the PDU.
func (p *PDU) AppendEthernet(dst []byte, to, from ethernet.MAC) []byte {
	h := ethernet.Frame{Dst: to, Src: from, Tagged: true, VLAN: ethernet.VLANTag{Priority: Priority(p.FrameID)}, EtherType: ethernet.EtherTypeProfinet}
	start := len(dst)
	dst = h.AppendHeader(dst)
	dst = p.AppendPayload(dst)
	return ethernet.Pad(dst, start)
}
