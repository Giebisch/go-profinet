// SPDX-License-Identifier: MIT

package pnio

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
)

// BlockType identifies a block.
type BlockType uint16

// Block types used by this package (IEC 61158-6-10). Response blocks have
// the high bit set.
const (
	BlockAlarmNotificationHigh BlockType = 0x0001
	BlockAlarmNotificationLow  BlockType = 0x0002
	BlockWriteReq              BlockType = 0x0008
	BlockReadReq               BlockType = 0x0009
	BlockDiagnosisData         BlockType = 0x0010
	BlockExpectedIdentData     BlockType = 0x0012
	BlockRealIdentData         BlockType = 0x0013
	BlockSubstituteValue       BlockType = 0x0014
	BlockRecordInputData       BlockType = 0x0015
	BlockRecordOutputData      BlockType = 0x0016
	BlockARData                BlockType = 0x0018
	BlockLogBookData           BlockType = 0x0019
	BlockAPIData               BlockType = 0x001A
	BlockIM0                   BlockType = 0x0020
	BlockIM1                   BlockType = 0x0021
	BlockIM2                   BlockType = 0x0022
	BlockIM3                   BlockType = 0x0023
	BlockIM4                   BlockType = 0x0024
	BlockIM0FilterSubmodule    BlockType = 0x0030
	BlockIM0FilterModule       BlockType = 0x0031
	BlockIM0FilterDevice       BlockType = 0x0032

	BlockARReq            BlockType = 0x0101
	BlockIOCRReq          BlockType = 0x0102
	BlockAlarmCRReq       BlockType = 0x0103
	BlockExpectedSubmReq  BlockType = 0x0104
	BlockPrmServerReq     BlockType = 0x0105
	BlockMCRReq           BlockType = 0x0106
	BlockARRPCReq         BlockType = 0x0107
	BlockARVendorReq      BlockType = 0x0108
	BlockIRInfo           BlockType = 0x0109
	BlockSRInfo           BlockType = 0x010A
	BlockARFSUBlock       BlockType = 0x010B
	BlockRSInfo           BlockType = 0x010C
	BlockPrmEndReq        BlockType = 0x0110
	BlockPrmEndPlugReq    BlockType = 0x0111
	BlockAppReadyReq      BlockType = 0x0112
	BlockAppReadyPlugReq  BlockType = 0x0113
	BlockReleaseReq       BlockType = 0x0114
	BlockReadyForCompReq  BlockType = 0x0116
	BlockReadyForRTC3Req  BlockType = 0x0117
	BlockPrmBeginReq      BlockType = 0x0118
	BlockSubmoduleListReq BlockType = 0x0119

	BlockPDPortDataReal      BlockType = 0x020F
	BlockPDInterfaceDataReal BlockType = 0x0240
	BlockMultipleBlockHeader BlockType = 0x0400

	BlockAlarmAckHigh  BlockType = 0x8001
	BlockAlarmAckLow   BlockType = 0x8002
	BlockWriteRes      BlockType = 0x8008
	BlockReadRes       BlockType = 0x8009
	BlockARRes         BlockType = 0x8101
	BlockIOCRRes       BlockType = 0x8102
	BlockAlarmCRRes    BlockType = 0x8103
	BlockModuleDiff    BlockType = 0x8104
	BlockPrmServerRes  BlockType = 0x8105
	BlockARServer      BlockType = 0x8106
	BlockARRPCRes      BlockType = 0x8107
	BlockARVendorRes   BlockType = 0x8108
	BlockPrmEndRes     BlockType = 0x8110
	BlockPrmEndPlugRes BlockType = 0x8111
	BlockAppReadyRes   BlockType = 0x8112
	BlockAppReadyPlugR BlockType = 0x8113
	BlockReleaseRes    BlockType = 0x8114
	BlockReadyForCompR BlockType = 0x8116
	BlockReadyForRTC3R BlockType = 0x8117
	BlockPrmBeginRes   BlockType = 0x8118
)

func (t BlockType) String() string {
	switch t {
	case BlockAlarmNotificationHigh:
		return "AlarmNotification-High"
	case BlockAlarmNotificationLow:
		return "AlarmNotification-Low"
	case BlockWriteReq:
		return "IODWriteReqHeader"
	case BlockReadReq:
		return "IODReadReqHeader"
	case BlockDiagnosisData:
		return "DiagnosisData"
	case BlockRealIdentData:
		return "RealIdentificationData"
	case BlockExpectedIdentData:
		return "ExpectedIdentificationData"
	case BlockPDPortDataReal:
		return "PDPortDataReal"
	case BlockPDInterfaceDataReal:
		return "PDInterfaceDataReal"
	case BlockPDPortDataCheck:
		return "PDPortDataCheck"
	case BlockPDPortDataAdjust:
		return "PDPortDataAdjust"
	case BlockAdjustDomainBoundary:
		return "AdjustDomainBoundary"
	case BlockCheckPeers:
		return "CheckPeers"
	case BlockCheckLineDelay:
		return "CheckLineDelay"
	case BlockCheckMAUType:
		return "CheckMAUType"
	case BlockAdjustMAUType:
		return "AdjustMAUType"
	case BlockAdjustMulticastBoundary:
		return "AdjustMulticastBoundary"
	case BlockAdjustLinkState:
		return "AdjustLinkState"
	case BlockCheckLinkState:
		return "CheckLinkState"
	case BlockCheckMAUTypeDifference:
		return "CheckMAUTypeDifference"
	case BlockAdjustPeerToPeerBoundary:
		return "AdjustPeerToPeerBoundary"
	case BlockAdjustDCPBoundary:
		return "AdjustDCPBoundary"
	case BlockAdjustPreambleLength:
		return "AdjustPreambleLength"
	case BlockPDInterfaceAdjust:
		return "PDInterfaceAdjust"
	case BlockMultipleBlockHeader:
		return "MultipleBlockHeader"
	case BlockIM0:
		return "I&M0"
	case BlockIM1:
		return "I&M1"
	case BlockIM2:
		return "I&M2"
	case BlockIM3:
		return "I&M3"
	case BlockIM4:
		return "I&M4"
	case BlockARReq:
		return "ARBlockReq"
	case BlockIOCRReq:
		return "IOCRBlockReq"
	case BlockAlarmCRReq:
		return "AlarmCRBlockReq"
	case BlockExpectedSubmReq:
		return "ExpectedSubmoduleBlockReq"
	case BlockARRPCReq:
		return "ARRPCBlockReq"
	case BlockPrmEndReq:
		return "IODControlReq(PrmEnd)"
	case BlockAppReadyReq:
		return "IOXControlReq(ApplicationReady)"
	case BlockReleaseReq:
		return "ReleaseBlockReq"
	case BlockPrmBeginReq:
		return "IODControlReq(PrmBegin)"
	case BlockPrmEndPlugReq:
		return "IODControlReq(PrmEnd, plug)"
	case BlockAppReadyPlugReq:
		return "IOXControlReq(ApplicationReady, plug)"
	case BlockAlarmAckHigh:
		return "AlarmAck-High"
	case BlockAlarmAckLow:
		return "AlarmAck-Low"
	case BlockWriteRes:
		return "IODWriteResHeader"
	case BlockReadRes:
		return "IODReadResHeader"
	case BlockARRes:
		return "ARBlockRes"
	case BlockIOCRRes:
		return "IOCRBlockRes"
	case BlockAlarmCRRes:
		return "AlarmCRBlockRes"
	case BlockModuleDiff:
		return "ModuleDiffBlock"
	case BlockARServer:
		return "ARServerBlock"
	case BlockARRPCRes:
		return "ARRPCBlockRes"
	case BlockPrmEndRes:
		return "IODControlRes(PrmEnd)"
	case BlockAppReadyRes:
		return "IOXControlRes(ApplicationReady)"
	case BlockReleaseRes:
		return "ReleaseBlockRes"
	case BlockPrmBeginRes:
		return "IODControlRes(PrmBegin)"
	default:
		return fmt.Sprintf("Block(0x%04X)", uint16(t))
	}
}

// BlockHeaderLen is the length of a block header: type, length and
// version.
const BlockHeaderLen = 6

// BlockHeader is the header of every block. Length counts the octets
// after the length field (the version included).
type BlockHeader struct {
	Type        BlockType
	Length      uint16
	VersionHigh uint8
	VersionLow  uint8
}

// RawBlock is an undecoded block: its header and the body after the
// version octets.
type RawBlock struct {
	Header BlockHeader
	Body   []byte
}

// ReadBlock decodes the block at the start of b and returns it and the
// octets that follow it. The body aliases b.
func ReadBlock(b []byte) (RawBlock, []byte, error) {
	if len(b) < BlockHeaderLen {
		return RawBlock{}, nil, fmt.Errorf("pnio: %w: block header of %d octets", profinet.ErrMalformed, len(b))
	}
	h := BlockHeader{
		Type:        BlockType(binary.BigEndian.Uint16(b)),
		Length:      binary.BigEndian.Uint16(b[2:]),
		VersionHigh: b[4],
		VersionLow:  b[5],
	}
	if h.Length < 2 {
		return RawBlock{}, nil, fmt.Errorf("pnio: %w: %s of length %d", profinet.ErrMalformed, h.Type, h.Length)
	}
	end := 4 + int(h.Length)
	if end > len(b) {
		return RawBlock{}, nil, fmt.Errorf("pnio: %w: %s of length %d exceeds the %d octets present", profinet.ErrMalformed, h.Type, h.Length, len(b)-4)
	}
	return RawBlock{Header: h, Body: b[BlockHeaderLen:end]}, b[end:], nil
}

// SplitBlocks decodes a sequence of blocks that fills b exactly.
func SplitBlocks(b []byte) ([]RawBlock, error) {
	var out []RawBlock
	for len(b) > 0 {
		blk, rest, err := ReadBlock(b)
		if err != nil {
			return out, err
		}
		out = append(out, blk)
		b = rest
	}
	return out, nil
}

// beginBlock appends a block header with a zero length and returns the
// offset of its start, to be passed to endBlock.
func beginBlock(b []byte, t BlockType, vh, vl uint8) ([]byte, int) {
	start := len(b)
	b = appendU16(b, uint16(t))
	b = append(b, 0, 0, vh, vl)
	return b, start
}

// endBlock fills in the length of the block that starts at start.
func endBlock(b []byte, start int) []byte {
	binary.BigEndian.PutUint16(b[start+2:], uint16(len(b)-start-4))
	return b
}

// AppendRaw appends a raw block.
func (r RawBlock) Append(b []byte) []byte {
	b, s := beginBlock(b, r.Header.Type, r.Header.VersionHigh, r.Header.VersionLow)
	b = append(b, r.Body...)
	return endBlock(b, s)
}

// checkVersion checks the version of a block against the high version it
// must have, returning the low version.
func checkVersion(blk RawBlock, high uint8, maxLow uint8) error {
	var field uint8
	switch {
	case blk.Header.VersionHigh != high:
		field = 2
	case blk.Header.VersionLow > maxLow:
		field = 3
	default:
		return nil
	}
	return &StatusError{Op: "decode " + blk.Header.Type.String(), Status: blockError(blk.Header.Type, field)}
}
