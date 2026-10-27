// SPDX-License-Identifier: MIT

// Package ptcp encodes and decodes the delay frames of the Precision
// Transparent Clock Protocol (PTCP, IEC 61158-6-10): DelayReq and the delay
// responses with which neighbors measure the line delay of a link. The
// layouts are those of Wireshark's PTCP dissector
// (docs/research/EVIDENCE-MRP-PTCP-MGMT.md, B). It is a codec only: a
// general-purpose OS has no hardware timestamps, so go-profinet measures no
// line delay and reports it as "not measured", as p-net does; PTCP
// synchronization (IRT) is out of scope.
package ptcp

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// FrameIDs of the delay frames (in the PROFINET RT frame, EtherType
// 0x8892). Wireshark names 0xFF41 DelayRes, 0xFF42 DelayFuRes and 0xFF43
// the response's follow-up; which response comes with a follow-up is not
// verified.
const (
	FrameIDDelayReq   uint16 = 0xFF40
	FrameIDDelayRes   uint16 = 0xFF41
	FrameIDDelayFuRes uint16 = 0xFF42
	FrameIDDelayFu    uint16 = 0xFF43
)

// IsDelay reports whether a FrameID is in the delay range 0xFF40-0xFF5F.
func IsDelay(frameID uint16) bool { return frameID >= 0xFF40 && frameID <= 0xFF5F }

// TLV types.
const (
	tlvEnd            = 0x00
	tlvDelayParameter = 0x06
	tlvPortParameter  = 0x05
	tlvPortTime       = 0x07
)

// HeaderLen is the length of a delay PDU's header after the FrameID.
const HeaderLen = 20

// Delay is a delay PDU: the request, a response or a follow-up.
type Delay struct {
	FrameID    uint16
	SequenceID uint16
	// Delay is Delay1ns (nanoseconds; in a follow-up the responder's
	// delay).
	Delay uint32
	// PortMAC is DelayParameter's PortMACAddress when HasPortMAC.
	PortMAC    ethernet.MAC
	HasPortMAC bool
	// RxDelay and TxDelay are PortParameter's T2PortRxDelay and
	// T3PortTxDelay (ns) when HasPortParameter.
	RxDelay, TxDelay uint32
	HasPortParameter bool
	// T2TimeStamp is PortTime's T2TimeStamp (ns) when HasPortTime.
	T2TimeStamp uint32
	HasPortTime bool
}

func appendTLV(b []byte, typ int, value []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(typ)<<9|uint16(len(value))&0x1FF)
	return append(b, value...)
}

// AppendTo appends the PDU, FrameID first.
func (d *Delay) AppendTo(b []byte) []byte {
	be := binary.BigEndian
	b = be.AppendUint16(b, d.FrameID)
	b = append(b, make([]byte, 12)...)
	b = be.AppendUint16(b, d.SequenceID)
	b = append(b, 0, 0)
	b = be.AppendUint32(b, d.Delay)
	if d.HasPortMAC {
		b = appendTLV(b, tlvDelayParameter, d.PortMAC[:])
	}
	if d.HasPortParameter {
		b = appendTLV(b, tlvPortParameter, be.AppendUint32(be.AppendUint32([]byte{0, 0}, d.RxDelay), d.TxDelay))
	}
	if d.HasPortTime {
		b = appendTLV(b, tlvPortTime, be.AppendUint32([]byte{0, 0}, d.T2TimeStamp))
	}
	return appendTLV(b, tlvEnd, nil)
}

// Decode decodes a delay PDU, FrameID first. Unknown TLVs are skipped.
func Decode(b []byte) (Delay, error) {
	var d Delay
	be := binary.BigEndian
	if len(b) < 2+HeaderLen {
		return d, fmt.Errorf("ptcp: %w: %d octets", profinet.ErrMalformed, len(b))
	}
	d.FrameID = be.Uint16(b)
	if !IsDelay(d.FrameID) {
		return d, fmt.Errorf("ptcp: %w: FrameID 0x%04X is not a delay frame", profinet.ErrMalformed, d.FrameID)
	}
	d.SequenceID = be.Uint16(b[14:])
	d.Delay = be.Uint32(b[18:])
	data := b[2+HeaderLen:]
	for {
		if len(data) < 2 {
			return d, fmt.Errorf("ptcp: %w: no End TLV", profinet.ErrMalformed)
		}
		h := be.Uint16(data)
		typ, l := int(h>>9), int(h&0x1FF)
		if typ == tlvEnd {
			return d, nil
		}
		if 2+l > len(data) {
			return d, fmt.Errorf("ptcp: %w: TLV %d of length %d exceeds the frame", profinet.ErrMalformed, typ, l)
		}
		v := data[2 : 2+l]
		data = data[2+l:]
		switch {
		case typ == tlvDelayParameter && l >= 6:
			d.PortMAC, d.HasPortMAC = ethernet.MAC(v[:6]), true
		case typ == tlvPortParameter && l >= 10:
			d.RxDelay, d.TxDelay, d.HasPortParameter = be.Uint32(v[2:]), be.Uint32(v[6:]), true
		case typ == tlvPortTime && l >= 6:
			d.T2TimeStamp, d.HasPortTime = be.Uint32(v[2:]), true
		case typ == tlvDelayParameter || typ == tlvPortParameter || typ == tlvPortTime:
			return d, fmt.Errorf("ptcp: %w: TLV %d of %d octets", profinet.ErrMalformed, typ, l)
		default:
		}
	}
}
