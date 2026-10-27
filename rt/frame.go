// SPDX-License-Identifier: MIT

// Package rt implements cyclic real-time PROFINET IO (RT_CLASS_1/2,
// unsynchronized): the codec of cyclic frames, the layout of the IO data
// in a frame, providers and consumers with their watchdog, and the
// scheduler that drives them.
//
// The cyclic frame is FrameID, the C_SDU (the IO data objects with their
// provider status and the consumer status octets), CycleCounter,
// DataStatus and TransferStatus, on EtherType 0x8892 with an 802.1Q tag of
// priority 6.
//
// Functional correctness on a virtual network does not establish real-time
// behaviour on hardware: see the measurements in docs/CYCLIC.md.
package rt

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// DataStatus is the APDU status octet of a cyclic frame.
type DataStatus uint8

// DataStatus bits.
const (
	StatePrimary     DataStatus = 0x01
	Redundancy       DataStatus = 0x02
	DataValid        DataStatus = 0x04
	ProviderRun      DataStatus = 0x10
	StationOK        DataStatus = 0x20 // StationProblemIndicator: 1 = normal
	IgnoreDataStatus DataStatus = 0x80
)

// DefaultDataStatus is primary, valid, running and without a station
// problem (0x35).
const DefaultDataStatus = StatePrimary | DataValid | ProviderRun | StationOK

// The DataStatus bits State, Redundancy and DataValid (mask 0x07) of an
// IOCARSR's frames: on the output CR the controller's request, on the
// input CR the device's acknowledgement (Wireshark's PNIO RT dissector).
const (
	RedundancyMask DataStatus = 0x07
	// Output CR (controller to device).
	PrimaryRequest DataStatus = 0x05
	BackupRequest  DataStatus = 0x04
	// Input CR (device to controller).
	PrimaryAck           DataStatus = 0x05
	BackupAckWithData    DataStatus = 0x04
	BackupAckWithoutData DataStatus = 0x00
	PrimaryMissingData   DataStatus = 0x06
	PrimaryMissingNoData DataStatus = 0x02
	PrimaryFault         DataStatus = 0x07
)

// Valid reports whether the data is valid.
func (s DataStatus) Valid() bool { return s&DataValid != 0 }

// Running reports whether the provider is in RUN.
func (s DataStatus) Running() bool { return s&ProviderRun != 0 }

// Primary reports whether the provider is the primary one.
func (s DataStatus) Primary() bool { return s&StatePrimary != 0 }

// StationProblem reports whether the provider signals a station problem.
func (s DataStatus) StationProblem() bool { return s&StationOK == 0 }

// String lists the set bits.
func (s DataStatus) String() string {
	out := fmt.Sprintf("0x%02X", uint8(s))
	if s.Primary() {
		out += " primary"
	} else {
		out += " backup"
	}
	if s.Valid() {
		out += " valid"
	} else {
		out += " invalid"
	}
	if s.Running() {
		out += " run"
	} else {
		out += " stop"
	}
	if s.StationProblem() {
		out += " problem"
	}
	return out
}

// Limits of the C_SDU.
const (
	MinDataLength = 40
	MaxDataLength = 1440
	// TrailerLen is CycleCounter, DataStatus and TransferStatus.
	TrailerLen = 4
)

// CycleUnit is the unit of SendClockFactor and of the cycle counter.
const CycleUnit = 31250 // nanoseconds

// VLANPriority is the 802.1Q priority of cyclic RT frames.
const VLANPriority = 6

// Frame is a decoded cyclic frame. Data aliases the decoded buffer.
type Frame struct {
	FrameID        uint16
	Data           []byte
	CycleCounter   uint16
	DataStatus     DataStatus
	TransferStatus uint8
}

// Decode decodes the PROFINET payload (FrameID first) of a cyclic frame
// whose C_SDU is dataLen octets long. A negative dataLen takes the C_SDU
// to be everything between the FrameID and the trailer, which is right
// for frames of at least the minimum size.
func Decode(p []byte, dataLen int) (Frame, error) {
	var f Frame
	if dataLen < 0 {
		dataLen = len(p) - 2 - TrailerLen
	}
	if dataLen < 0 || len(p) < 2+dataLen+TrailerLen {
		return f, fmt.Errorf("rt: %w: cyclic frame of %d octets for a C_SDU of %d", profinet.ErrMalformed, len(p), dataLen)
	}
	f.FrameID = binary.BigEndian.Uint16(p)
	f.Data = p[2 : 2+dataLen]
	t := p[2+dataLen:]
	f.CycleCounter = binary.BigEndian.Uint16(t)
	f.DataStatus = DataStatus(t[2])
	f.TransferStatus = t[3]
	return f, nil
}

// AppendPayload appends the PROFINET payload of a cyclic frame.
func AppendPayload(dst []byte, f *Frame) []byte {
	dst = binary.BigEndian.AppendUint16(dst, f.FrameID)
	dst = append(dst, f.Data...)
	dst = binary.BigEndian.AppendUint16(dst, f.CycleCounter)
	return append(dst, byte(f.DataStatus), f.TransferStatus)
}

// AppendEthernet appends a complete Ethernet frame carrying f, tagged
// with priority 6 and VLAN id vid.
func AppendEthernet(dst []byte, to, from ethernet.MAC, vid uint16, f *Frame) []byte {
	h := ethernet.Frame{Dst: to, Src: from, Tagged: true, VLAN: ethernet.VLANTag{Priority: VLANPriority, ID: vid}, EtherType: ethernet.EtherTypeProfinet}
	start := len(dst)
	dst = h.AppendHeader(dst)
	dst = AppendPayload(dst, f)
	return ethernet.Pad(dst, start)
}

// CounterNewer reports whether cycle counter c is newer than prev: the
// modular difference is in [1, 0x7FFF].
func CounterNewer(c, prev uint16) bool {
	d := c - prev
	return d != 0 && d < 0x8000
}

// CycleNanos returns the cycle time of SendClockFactor × ReductionRatio.
func CycleNanos(sendClockFactor, reductionRatio uint16) int64 {
	return int64(sendClockFactor) * int64(reductionRatio) * CycleUnit
}
