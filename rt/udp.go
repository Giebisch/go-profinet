// SPDX-License-Identifier: MIT

package rt

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
)

// RT_CLASS_UDP carries the cyclic frame in a UDP datagram to port
// [UDPPort]: Wireshark registers its PROFINET RT dissector for UDP port
// 0x8892 and reads the FrameID at offset 0 of the UDP payload
// (docs/research/EVIDENCE-AR-SR-MCR-UDP.md, D). The payload is therefore
// FrameID, C_SDU, CycleCounter, DataStatus and TransferStatus, as on
// Ethernet, without the padding to the minimum Ethernet frame size
// (inferred from that dissection). These are codecs only: no open
// implementation has an RT_CLASS_UDP data path to test against, and
// neither the controller nor the device of this module sends or accepts
// RT over UDP.

// UDPPort is the UDP destination port of RT_CLASS_UDP frames (34962).
const UDPPort = 0x8892

// MinDataLengthUDP is the smallest C_SDU of an RT_CLASS_UDP IOCR. Without
// Ethernet padding, the 40 octets of RT_CLASS_1 do not apply; p-net
// accepts 12..1440 for RT_CLASS_UDP (observed in that implementation,
// which cites the PROFINET protocol specification).
const MinDataLengthUDP = 12

// FrameID ranges of RT_CLASS_UDP, as Wireshark labels them: 0xC000-0xF7FF
// unicast, 0xF800-0xFBFF multicast.
const (
	FrameIDUDPMin   uint16 = 0xC000
	FrameIDUDPMax   uint16 = 0xF7FF
	FrameIDUDPMCMin uint16 = 0xF800
	FrameIDUDPMCMax uint16 = 0xFBFF
)

// IsUDPFrameID reports whether id is a FrameID of RT_CLASS_UDP, unicast
// or multicast.
func IsUDPFrameID(id uint16) bool { return id >= FrameIDUDPMin && id <= FrameIDUDPMCMax }

// AppendUDPPayload appends the UDP payload of an RT_CLASS_UDP frame:
// FrameID, C_SDU and the trailer, unpadded. It fails for a FrameID outside
// the RT_CLASS_UDP ranges and for a C_SDU longer than [MaxDataLength].
func AppendUDPPayload(dst []byte, f *Frame) ([]byte, error) {
	if !IsUDPFrameID(f.FrameID) {
		return dst, fmt.Errorf("rt: %w: FrameID 0x%04X is not RT_CLASS_UDP", profinet.ErrInvalidConfig, f.FrameID)
	}
	if len(f.Data) > MaxDataLength {
		return dst, fmt.Errorf("rt: %w: C_SDU of %d octets (at most %d)", profinet.ErrInvalidConfig, len(f.Data), MaxDataLength)
	}
	return AppendPayload(dst, f), nil
}

// DecodeUDP decodes the UDP payload of an RT_CLASS_UDP frame. The C_SDU
// is the payload between the FrameID and the trailer: a datagram carries
// no padding, so with dataLen ≥ 0 the payload must be exactly
// 2 + dataLen + 4 octets. Data aliases p.
func DecodeUDP(p []byte, dataLen int) (Frame, error) {
	var f Frame
	if len(p) < 2+TrailerLen {
		return f, fmt.Errorf("rt: %w: UDP RT payload of %d octets", profinet.ErrMalformed, len(p))
	}
	id := binary.BigEndian.Uint16(p)
	if !IsUDPFrameID(id) {
		return f, fmt.Errorf("rt: %w: FrameID 0x%04X is not RT_CLASS_UDP", profinet.ErrMalformed, id)
	}
	n := len(p) - 2 - TrailerLen
	if n > MaxDataLength {
		return f, fmt.Errorf("rt: %w: C_SDU of %d octets (at most %d)", profinet.ErrMalformed, n, MaxDataLength)
	}
	if dataLen >= 0 && n != dataLen {
		return f, fmt.Errorf("rt: %w: UDP RT payload of %d octets for a C_SDU of %d", profinet.ErrMalformed, len(p), dataLen)
	}
	return Decode(p, n)
}
