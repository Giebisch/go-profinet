// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/rpc"
)

// Blocks of system redundancy (IOCARSR) and multicast communication
// relations (MCR) in a Connect request: codecs only. Their layouts are
// those of Wireshark's PNIO dissector
// (docs/research/EVIDENCE-AR-SR-MCR-UDP.md, B and C). No open
// implementation of their behaviour exists to test against; the IO-Device
// still refuses IOCARSR and multicast IOCRs.

// SRProperties bits of the SRInfoBlock.
const (
	// SRPropInputValidOnBackupAR: the device delivers valid input data
	// on the backup AR (Mode 0).
	SRPropInputValidOnBackupAR uint32 = 1 << 0
	// SRPropMode selects the mode in which a backup AR marks its input
	// data with DataStatus.DataValid.
	SRPropMode uint32 = 1 << 2
)

// SRInfoBlock is the SRInfoBlock (0x010A) of an IOCARSR Connect.
type SRInfoBlock struct {
	// RedundancyDataHoldFactor bounds the time the device waits for the
	// primary AR's data (3-0xC7 optional, 0xC8 and up mandatory; the
	// unit is not public).
	RedundancyDataHoldFactor uint16
	Properties               uint32
}

// Append appends the block.
func (s *SRInfoBlock) Append(b []byte) []byte {
	b, st := beginBlock(b, BlockSRInfo, 1, 0)
	b = appendU16(b, s.RedundancyDataHoldFactor)
	b = appendU32(b, s.Properties)
	return endBlock(b, st)
}

// DecodeSRInfoBlock decodes an SRInfoBlock.
func DecodeSRInfoBlock(blk RawBlock) (SRInfoBlock, error) {
	var s SRInfoBlock
	if blk.Header.Type != BlockSRInfo {
		return s, fmt.Errorf("pnio: %w: %s instead of SRInfoBlock", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "SRInfoBlock")
	s.RedundancyDataHoldFactor = r.u16()
	s.Properties = r.u32()
	return s, r.err
}

// ARSetID is the structure of an IOCARSR's ARUUID: the ARs of one AR set
// share Discriminator and ConfigID and differ in their AR number.
type ARSetID struct {
	Discriminator [6]byte
	ConfigID      [8]byte
	// ARNumber is 1 to 4: the AR's place in its set.
	ARNumber uint8
	// Resource is 2 when the device shall allocate two ARs for the set.
	Resource uint8
}

// SplitARUUID returns the structure of an IOCARSR's ARUUID.
func SplitARUUID(u rpc.UUID) ARSetID {
	var s ARSetID
	copy(s.Discriminator[:], u[:6])
	copy(s.ConfigID[:], u[6:14])
	sel := uint16(u[14])<<8 | uint16(u[15])
	s.ARNumber = uint8(sel & 0x0007)
	s.Resource = uint8(sel&0x0018) >> 3
	return s
}

// UUID returns the ARUUID of the AR.
func (s ARSetID) UUID() rpc.UUID {
	var u rpc.UUID
	copy(u[:6], s.Discriminator[:])
	copy(u[6:14], s.ConfigID[:])
	sel := uint16(s.ARNumber)&0x0007 | uint16(s.Resource&0x3)<<3
	u[14], u[15] = byte(sel>>8), byte(sel)
	return u
}

// MCRBlockReq is the MCRBlockReq (0x0106) of a Connect: a multicast
// consumer IOCR and the name of the station that provides its data.
type MCRBlockReq struct {
	IOCRReference uint16
	// AddressResolutionProperties is not decoded: its bit layout is not
	// public (p-net reads two octets where Wireshark reads four).
	AddressResolutionProperties uint32
	MCITimeoutFactor            uint16
	ProviderStationName         string
}

// Append appends the block.
func (m *MCRBlockReq) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockMCRReq, 1, 0)
	b = appendU16(b, m.IOCRReference)
	b = appendU32(b, m.AddressResolutionProperties)
	b = appendU16(b, m.MCITimeoutFactor)
	b = appendU16(b, uint16(len(m.ProviderStationName)))
	b = append(b, m.ProviderStationName...)
	return endBlock(b, s)
}

// DecodeMCRBlockReq decodes an MCRBlockReq.
func DecodeMCRBlockReq(blk RawBlock) (MCRBlockReq, error) {
	var m MCRBlockReq
	if blk.Header.Type != BlockMCRReq {
		return m, fmt.Errorf("pnio: %w: %s instead of MCRBlockReq", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "MCRBlockReq")
	m.IOCRReference = r.u16()
	m.AddressResolutionProperties = r.u32()
	m.MCITimeoutFactor = r.u16()
	m.ProviderStationName = string(r.bytes(int(r.u16())))
	return m, r.err
}
