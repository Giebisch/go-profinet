// SPDX-License-Identifier: MIT

// Package rpc implements the connectionless DCE RPC protocol (DCE 1.1 RPC,
// chapter 12, "CL") that PROFINET IO uses for its acyclic services over
// UDP: the packet header, fragmentation and reassembly, a client with
// retransmission and an at-most-once server with a response cache.
//
// The package knows nothing of PROFINET beyond the well-known port; the
// PNIO interfaces, operations and NDR bodies are in package pnio.
package rpc

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
)

// Port is the UDP port of PROFINET's RPC endpoint (0x8894).
const Port = 34964

// HeaderLen is the length of a CL packet header.
const HeaderLen = 80

// Version is the RPC protocol version of CL packets.
const Version = 4

// PacketType is the ptype of a CL packet.
type PacketType uint8

// Packet types.
const (
	TypeRequest   PacketType = 0
	TypePing      PacketType = 1
	TypeResponse  PacketType = 2
	TypeFault     PacketType = 3
	TypeWorking   PacketType = 4
	TypeNoCall    PacketType = 5
	TypeReject    PacketType = 6
	TypeAck       PacketType = 7
	TypeCancel    PacketType = 8
	TypeFack      PacketType = 9
	TypeCancelAck PacketType = 10
)

func (t PacketType) String() string {
	names := [...]string{"request", "ping", "response", "fault", "working", "nocall", "reject", "ack", "cancel", "fack", "cancel_ack"}
	if int(t) < len(names) {
		return names[t]
	}
	return fmt.Sprintf("ptype(%d)", uint8(t))
}

// Flags1 bits.
const (
	FlagLastFrag   uint8 = 0x02
	FlagFrag       uint8 = 0x04
	FlagNoFack     uint8 = 0x08
	FlagMaybe      uint8 = 0x10
	FlagIdempotent uint8 = 0x20
	FlagBroadcast  uint8 = 0x40
)

// Flags2 bits.
const (
	Flag2CancelPending uint8 = 0x02
)

// Data representation: the first octet of drep. PROFINET devices use
// little-endian ASCII IEEE floating point (0x10 0x00 0x00); big endian
// is accepted.
const (
	DRepLittleEndian uint8 = 0x10
	DRepBigEndian    uint8 = 0x00
)

// Reject and fault status codes (DCE 1.1 RPC, appendix E).
const (
	StatusOpRangeError   uint32 = 0x1C010002 // nca_op_rng_error
	StatusUnknownIf      uint32 = 0x1C010003 // nca_unk_if
	StatusWrongBootTime  uint32 = 0x1C010006 // nca_wrong_boot_time
	StatusYouCrashed     uint32 = 0x1C010009 // nca_s_you_crashed
	StatusProtoError     uint32 = 0x1C01000B // nca_proto_error
	StatusOutArgsTooBig  uint32 = 0x1C010013 // nca_out_args_too_big
	StatusServerTooBusy  uint32 = 0x1C010014 // nca_server_too_busy
	StatusUnsupportedTyp uint32 = 0x1C010017 // nca_unsupported_type
	StatusFaultUnspec    uint32 = 0x1C000012 // nca_s_fault_unspec
)

// Header is the header of a CL packet. The UUIDs are in canonical order;
// the integer fields are converted from the packet's data representation.
type Header struct {
	Type     PacketType
	Flags1   uint8
	Flags2   uint8
	DRep     [3]byte
	Serial   uint16 // serial_hi << 8 | serial_lo
	Object   UUID
	Iface    UUID
	Activity UUID
	// ServerBoot is the server's boot time; a client sends 0 until it
	// learned it from a response.
	ServerBoot uint32
	// IfaceVersion has the major version in the low 16 bits.
	IfaceVersion uint32
	SeqNum       uint32
	OpNum        uint16
	IfaceHint    uint16
	ActivityHint uint16
	// BodyLen is the length of the body that follows the header.
	BodyLen   uint16
	FragNum   uint16
	AuthProto uint8
}

// ByteOrder decodes and appends integers in one byte order;
// binary.LittleEndian and binary.BigEndian implement it.
type ByteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// Order returns the byte order of the header's data representation.
func (h *Header) Order() ByteOrder {
	if h.DRep[0]&0xF0 == DRepLittleEndian {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

// AppendTo appends the encoded header to dst.
func (h *Header) AppendTo(dst []byte) []byte {
	o := h.Order()
	dst = append(dst, Version, byte(h.Type), h.Flags1, h.Flags2, h.DRep[0], h.DRep[1], h.DRep[2], byte(h.Serial>>8))
	dst = h.Object.Append(dst, o)
	dst = h.Iface.Append(dst, o)
	dst = h.Activity.Append(dst, o)
	dst = o.AppendUint32(dst, h.ServerBoot)
	dst = o.AppendUint32(dst, h.IfaceVersion)
	dst = o.AppendUint32(dst, h.SeqNum)
	dst = o.AppendUint16(dst, h.OpNum)
	dst = o.AppendUint16(dst, h.IfaceHint)
	dst = o.AppendUint16(dst, h.ActivityHint)
	dst = o.AppendUint16(dst, h.BodyLen)
	dst = o.AppendUint16(dst, h.FragNum)
	return append(dst, h.AuthProto, byte(h.Serial))
}

// DecodeHeader decodes a CL packet header from b and returns it with the
// body (aliasing b), which is BodyLen octets long.
func DecodeHeader(b []byte) (Header, []byte, error) {
	var h Header
	if len(b) < HeaderLen {
		return h, nil, fmt.Errorf("rpc: %w: packet of %d octets", profinet.ErrMalformed, len(b))
	}
	if b[0] != Version {
		return h, nil, fmt.Errorf("rpc: %w: RPC version %d", profinet.ErrMalformed, b[0])
	}
	h.Type = PacketType(b[1])
	if h.Type > TypeCancelAck {
		return h, nil, fmt.Errorf("rpc: %w: packet type %d", profinet.ErrMalformed, b[1])
	}
	h.Flags1, h.Flags2 = b[2], b[3]
	copy(h.DRep[:], b[4:7])
	o := h.Order()
	h.Serial = uint16(b[7])<<8 | uint16(b[79])
	h.Object = DecodeUUID(b[8:], o)
	h.Iface = DecodeUUID(b[24:], o)
	h.Activity = DecodeUUID(b[40:], o)
	h.ServerBoot = o.Uint32(b[56:])
	h.IfaceVersion = o.Uint32(b[60:])
	h.SeqNum = o.Uint32(b[64:])
	h.OpNum = o.Uint16(b[68:])
	h.IfaceHint = o.Uint16(b[70:])
	h.ActivityHint = o.Uint16(b[72:])
	h.BodyLen = o.Uint16(b[74:])
	h.FragNum = o.Uint16(b[76:])
	h.AuthProto = b[78]
	body := b[HeaderLen:]
	if int(h.BodyLen) > len(body) {
		return h, nil, fmt.Errorf("rpc: %w: body length %d exceeds the %d octets present", profinet.ErrMalformed, h.BodyLen, len(body))
	}
	if h.AuthProto != 0 {
		return h, nil, fmt.Errorf("rpc: %w: authentication protocol %d", profinet.ErrUnsupported, h.AuthProto)
	}
	return h, body[:h.BodyLen], nil
}

// Fault reports a FAULT or REJECT packet. It matches profinet.ErrRejected.
type Fault struct {
	// Reject is true for a REJECT, false for a FAULT.
	Reject bool
	// Status is the status code of the packet.
	Status uint32
}

func (f *Fault) Error() string {
	kind := "fault"
	if f.Reject {
		kind = "reject"
	}
	name := ""
	switch f.Status {
	case StatusOpRangeError:
		name = " (operation number out of range)"
	case StatusUnknownIf:
		name = " (unknown interface)"
	case StatusWrongBootTime:
		name = " (wrong boot time)"
	case StatusProtoError:
		name = " (protocol error)"
	case StatusServerTooBusy:
		name = " (server too busy)"
	case StatusFaultUnspec:
		name = " (unspecified fault)"
	}
	return fmt.Sprintf("rpc: %s 0x%08X%s", kind, f.Status, name)
}

// Is makes a Fault match profinet.ErrRejected, profinet.ErrUnsupported for
// an unknown interface or operation, and profinet.ErrResourceExhausted for
// a busy server.
func (f *Fault) Is(target error) bool {
	switch target {
	case profinet.ErrRejected:
		return true
	case profinet.ErrUnsupported:
		return f.Status == StatusUnknownIf || f.Status == StatusOpRangeError || f.Status == StatusUnsupportedTyp
	case profinet.ErrResourceExhausted:
		return f.Status == StatusServerTooBusy
	}
	return false
}
