// SPDX-License-Identifier: MIT

// Package ethernet encodes and decodes Ethernet II frames with an optional
// IEEE 802.1Q tag: the layer every PROFINET frame (DCP, LLDP, RT, alarms)
// travels on.
//
// The package is independent of any operating system API: frames are byte
// slices that a port of package l2 (a raw socket, BPF, Npcap or the virtual
// network of package vnet) sends and receives.
package ethernet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/otfabric/go-profinet"
)

// Sizes of an Ethernet II frame without the frame check sequence, which
// the network interface adds and strips.
const (
	// HeaderLen is the length of the destination, source and EtherType.
	HeaderLen = 14
	// VLANTagLen is the length of an IEEE 802.1Q tag (TPID and TCI).
	VLANTagLen = 4
	// MinFrameLen is the minimum frame length without the FCS: shorter
	// frames are padded with zeros on transmission.
	MinFrameLen = 60
	// DefaultMTU is the payload size of a standard Ethernet frame.
	DefaultMTU = 1500
	// MaxFrameLen is the largest untagged frame of the default MTU,
	// without the FCS.
	MaxFrameLen = HeaderLen + DefaultMTU
	// MaxTaggedFrameLen is the largest tagged frame of the default MTU,
	// without the FCS.
	MaxTaggedFrameLen = MaxFrameLen + VLANTagLen
)

// EtherType identifies the protocol of a frame's payload.
type EtherType uint16

// EtherTypes used by PROFINET and by the minimal IP stack of package
// netstack.
const (
	EtherTypeIPv4     EtherType = 0x0800
	EtherTypeARP      EtherType = 0x0806
	EtherTypeVLAN     EtherType = 0x8100
	EtherTypeProfinet EtherType = 0x8892
	EtherTypeLLDP     EtherType = 0x88CC
)

// String returns the name of a known EtherType or its hexadecimal value.
func (t EtherType) String() string {
	switch t {
	case EtherTypeIPv4:
		return "IPv4"
	case EtherTypeARP:
		return "ARP"
	case EtherTypeVLAN:
		return "VLAN"
	case EtherTypeProfinet:
		return "PROFINET"
	case EtherTypeLLDP:
		return "LLDP"
	default:
		return fmt.Sprintf("0x%04X", uint16(t))
	}
}

// MAC is an IEEE 802 MAC-48 address.
type MAC [6]byte

// Well-known destination addresses.
var (
	// Broadcast is the Ethernet broadcast address.
	Broadcast = MAC{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	// DCPIdentifyMulticast is the destination of DCP Identify requests
	// (IEC 61158-6-10, multicast MAC address range of PROFINET).
	DCPIdentifyMulticast = MAC{0x01, 0x0E, 0xCF, 0x00, 0x00, 0x00}
	// DCPHelloMulticast is the destination of DCP Hello requests.
	DCPHelloMulticast = MAC{0x01, 0x0E, 0xCF, 0x00, 0x00, 0x01}
	// LLDPMulticast is the nearest-bridge address LLDP is sent to
	// (IEEE 802.1AB).
	LLDPMulticast = MAC{0x01, 0x80, 0xC2, 0x00, 0x00, 0x0E}
)

// ErrInvalidMAC reports a string that is not a MAC address.
var ErrInvalidMAC = errors.New("ethernet: invalid MAC address")

// ParseMAC parses a MAC-48 address written as six hexadecimal octets
// separated by ':' or '-', or as twelve hexadecimal digits.
func ParseMAC(s string) (MAC, error) {
	var m MAC
	var parts []string
	switch {
	case strings.Contains(s, ":"):
		parts = strings.Split(s, ":")
	case strings.Contains(s, "-"):
		parts = strings.Split(s, "-")
	case len(s) == 12:
		for i := 0; i < 12; i += 2 {
			parts = append(parts, s[i:i+2])
		}
	}
	if len(parts) != 6 {
		return m, fmt.Errorf("%w: %q", ErrInvalidMAC, s)
	}
	for i, p := range parts {
		if len(p) != 2 {
			return m, fmt.Errorf("%w: %q", ErrInvalidMAC, s)
		}
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return m, fmt.Errorf("%w: %q", ErrInvalidMAC, s)
		}
		m[i] = byte(v)
	}
	return m, nil
}

// String formats the address as six lower-case hexadecimal octets
// separated by colons.
func (m MAC) String() string {
	const hex = "0123456789abcdef"
	b := make([]byte, 0, 17)
	for i, v := range m {
		if i > 0 {
			b = append(b, ':')
		}
		b = append(b, hex[v>>4], hex[v&0x0F])
	}
	return string(b)
}

// MarshalText implements encoding.TextMarshaler.
func (m MAC) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (m *MAC) UnmarshalText(b []byte) error {
	v, err := ParseMAC(string(b))
	if err != nil {
		return err
	}
	*m = v
	return nil
}

// IsMulticast reports whether the group bit is set (broadcast included).
func (m MAC) IsMulticast() bool { return m[0]&0x01 != 0 }

// IsBroadcast reports whether m is the broadcast address.
func (m MAC) IsBroadcast() bool { return m == Broadcast }

// IsZero reports whether m is 00:00:00:00:00:00.
func (m MAC) IsZero() bool { return m == MAC{} }

// VLANTag is the tag control information of an IEEE 802.1Q tag.
type VLANTag struct {
	// Priority is the priority code point, 0..7. PROFINET RT frames use
	// priority 6, alarms 5 (high) and 3 (low) by default.
	Priority uint8
	// DropEligible is the DEI bit.
	DropEligible bool
	// ID is the VLAN identifier, 0..4095. 0 marks a priority-tagged frame.
	ID uint16
}

// TCI returns the 16-bit tag control information.
func (t VLANTag) TCI() uint16 {
	v := uint16(t.Priority&0x07)<<13 | t.ID&0x0FFF
	if t.DropEligible {
		v |= 1 << 12
	}
	return v
}

// TagFromTCI decodes the tag control information.
func TagFromTCI(tci uint16) VLANTag {
	return VLANTag{Priority: uint8(tci >> 13), DropEligible: tci&(1<<12) != 0, ID: tci & 0x0FFF}
}

// Frame is a decoded Ethernet II frame. Payload aliases the buffer the
// frame was decoded from.
type Frame struct {
	Dst, Src MAC
	// Tagged reports whether the frame carries an IEEE 802.1Q tag.
	Tagged bool
	// VLAN is the tag; it is meaningful only when Tagged is true.
	VLAN      VLANTag
	EtherType EtherType
	// Payload is everything after the EtherType, including any padding
	// the sender added to reach the minimum frame size. Protocols carry
	// their own length fields to strip it.
	Payload []byte
}

// Decode decodes an Ethernet II frame. The frame must not include the FCS.
// The returned frame's Payload aliases b. One 802.1Q tag is decoded; a
// second (QinQ) tag is left in the payload with EtherType VLAN.
func Decode(b []byte) (Frame, error) {
	var f Frame
	if len(b) < HeaderLen {
		return f, fmt.Errorf("%w: ethernet frame of %d octets", profinet.ErrMalformed, len(b))
	}
	copy(f.Dst[:], b[0:6])
	copy(f.Src[:], b[6:12])
	f.EtherType = EtherType(binary.BigEndian.Uint16(b[12:14]))
	off := HeaderLen
	if f.EtherType == EtherTypeVLAN {
		if len(b) < HeaderLen+VLANTagLen {
			return f, fmt.Errorf("%w: truncated 802.1Q tag", profinet.ErrMalformed)
		}
		f.Tagged = true
		f.VLAN = TagFromTCI(binary.BigEndian.Uint16(b[14:16]))
		f.EtherType = EtherType(binary.BigEndian.Uint16(b[16:18]))
		off += VLANTagLen
	}
	if f.EtherType < 0x0600 {
		return f, fmt.Errorf("%w: 802.3 length field %d instead of an EtherType", profinet.ErrMalformed, f.EtherType)
	}
	f.Payload = b[off:]
	return f, nil
}

// HeaderSize returns the length of the frame's header including its tag.
func (f *Frame) HeaderSize() int {
	if f.Tagged {
		return HeaderLen + VLANTagLen
	}
	return HeaderLen
}

// AppendHeader appends the header of f (destination, source, optional
// tag, EtherType) to dst.
func (f *Frame) AppendHeader(dst []byte) []byte {
	dst = append(dst, f.Dst[:]...)
	dst = append(dst, f.Src[:]...)
	if f.Tagged {
		dst = binary.BigEndian.AppendUint16(dst, uint16(EtherTypeVLAN))
		dst = binary.BigEndian.AppendUint16(dst, f.VLAN.TCI())
	}
	return binary.BigEndian.AppendUint16(dst, uint16(f.EtherType))
}

// AppendTo appends the encoded frame to dst, padding it with zeros to
// MinFrameLen.
func (f *Frame) AppendTo(dst []byte) []byte {
	start := len(dst)
	dst = f.AppendHeader(dst)
	dst = append(dst, f.Payload...)
	return Pad(dst, start)
}

// Encode returns the encoded frame in a new slice.
func (f *Frame) Encode() []byte {
	return f.AppendTo(make([]byte, 0, f.HeaderSize()+max(len(f.Payload), MinFrameLen)))
}

// Pad pads the frame that starts at frame[start] with zeros to
// MinFrameLen and returns the extended slice.
func Pad(frame []byte, start int) []byte {
	for len(frame)-start < MinFrameLen {
		frame = append(frame, 0)
	}
	return frame
}

// PeekEtherType returns the EtherType of the encoded frame b, looking past
// one 802.1Q tag, and the offset of the payload. ok is false when b is too
// short.
func PeekEtherType(b []byte) (t EtherType, payload int, ok bool) {
	if len(b) < HeaderLen {
		return 0, 0, false
	}
	t = EtherType(binary.BigEndian.Uint16(b[12:14]))
	if t != EtherTypeVLAN {
		return t, HeaderLen, true
	}
	if len(b) < HeaderLen+VLANTagLen {
		return 0, 0, false
	}
	return EtherType(binary.BigEndian.Uint16(b[16:18])), HeaderLen + VLANTagLen, true
}
