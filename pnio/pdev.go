// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"
	"net/netip"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// The records and blocks of port and interface parameterization (the
// physical device, PDev). Layouts as Wireshark's PNIO dissector and p-net
// agree on them (docs/research/EVIDENCE-DCP-PDEV.md, B.1).

// Block types of the PDev records.
const (
	BlockPDPortDataCheck          BlockType = 0x0200
	BlockPDPortDataAdjust         BlockType = 0x0202
	BlockAdjustDomainBoundary     BlockType = 0x0209
	BlockCheckPeers               BlockType = 0x020A
	BlockCheckLineDelay           BlockType = 0x020B
	BlockCheckMAUType             BlockType = 0x020C
	BlockAdjustMAUType            BlockType = 0x020E
	BlockAdjustMulticastBoundary  BlockType = 0x0210
	BlockAdjustLinkState          BlockType = 0x021B
	BlockCheckLinkState           BlockType = 0x021C
	BlockCheckMAUTypeDifference   BlockType = 0x021F
	BlockAdjustPeerToPeerBoundary BlockType = 0x0224
	BlockAdjustDCPBoundary        BlockType = 0x0225
	BlockAdjustPreambleLength     BlockType = 0x0226
	BlockPDInterfaceAdjust        BlockType = 0x0250
)

// Indexes of the PDev records.
const (
	IndexPDPortDataCheck     Index = 0x802B
	IndexPDPortDataAdjust    Index = 0x802F
	IndexPDInterfaceAdjust   Index = 0x8071
	IndexPDInterfaceDataReal Index = 0x8080
)

// PeerToPeerBoundary bits (AdjustPeerToPeerBoundary).
const (
	// PeerToPeerNoLLDP: the port sends no LLDP frames.
	PeerToPeerNoLLDP uint32 = 1 << 0
	// PeerToPeerNoPTCPDelay: the port sends no PTCP delay requests.
	PeerToPeerNoPTCPDelay uint32 = 1 << 1
	// PeerToPeerNoPathDelay: the port sends no path delay requests.
	PeerToPeerNoPathDelay uint32 = 1 << 2
)

// DCPBoundary bits (AdjustDCPBoundary): the port blocks the egress of
// these DCP multicast frames.
const (
	DCPBoundaryIdentify uint32 = 1 << 0 // to 01-0E-CF-00-00-00
	DCPBoundaryHello    uint32 = 1 << 1 // to 01-0E-CF-00-00-01
)

// MultipleInterfaceMode bit (PDInterfaceAdjust).
const (
	// InterfaceModeNameOfDevice: the LLDP port ID is "port-xyz.<name
	// of station>" instead of "port-xyz".
	InterfaceModeNameOfDevice uint32 = 1 << 0
)

// CheckPeer is an expected neighbor of a port (CheckPeers): its LLDP port
// ID and chassis ID, for a PROFINET device "port-001" and its name of
// station.
type CheckPeer struct {
	PortID    string `json:"portId"`
	ChassisID string `json:"chassisId"`
}

// PDPortDataCheck is the record of the checks of a port (index 0x802B of
// the port submodule).
type PDPortDataCheck struct {
	Slot    uint16 `json:"slot"`
	Subslot uint16 `json:"subslot"`
	// Peers are the expected neighbors (CheckPeers); nil means no
	// CheckPeers block.
	Peers []CheckPeer `json:"peers,omitempty"`
	// Other are the sub-blocks other than CheckPeers, undecoded.
	Other []RawBlock `json:"-"`
}

// PDPortDataAdjust is the record of the adjustments of a port (index
// 0x802F of the port submodule).
type PDPortDataAdjust struct {
	Slot    uint16 `json:"slot"`
	Subslot uint16 `json:"subslot"`
	// PeerToPeerBoundary (PeerToPeer* bits) when HasPeerToPeerBoundary.
	PeerToPeerBoundary    uint32 `json:"peerToPeerBoundary,omitempty"`
	HasPeerToPeerBoundary bool   `json:"-"`
	// DCPBoundary (DCPBoundary* bits) when HasDCPBoundary.
	DCPBoundary    uint32 `json:"dcpBoundary,omitempty"`
	HasDCPBoundary bool   `json:"-"`
	// Other are the other sub-blocks, undecoded.
	Other []RawBlock `json:"-"`
}

// PDInterfaceAdjust is the record of the interface's adjustments (index
// 0x8071 of the interface submodule).
type PDInterfaceAdjust struct {
	// MultipleInterfaceMode holds InterfaceMode* bits.
	MultipleInterfaceMode uint32 `json:"multipleInterfaceMode"`
}

// PDInterfaceDataReal is the real data of the interface (index 0x8080 of
// the interface submodule, and part of PDRealData): its chassis ID (the
// name of station), MAC address and IP suite.
type PDInterfaceDataReal struct {
	ChassisID string       `json:"chassisId"`
	MAC       ethernet.MAC `json:"mac"`
	IP        netip.Addr   `json:"ip"`
	Mask      netip.Addr   `json:"mask"`
	Gateway   netip.Addr   `json:"gateway"`
}

// appendRaw appends a block undecoded.
func appendRaw(b []byte, blk RawBlock) []byte {
	b, s := beginBlock(b, blk.Header.Type, blk.Header.VersionHigh, blk.Header.VersionLow)
	b = append(b, blk.Body...)
	return endBlock(b, s)
}

// splitSubBlocks splits the sub-blocks of a record. Up to three octets of
// zero padding may follow the last one (p-net pads a record to four
// octets, Wireshark's layout has no padding).
func splitSubBlocks(b []byte) ([]RawBlock, error) {
	blocks, err := SplitBlocks(b)
	if err == nil {
		return blocks, nil
	}
	for n := 1; n <= 3 && n <= len(b) && b[len(b)-n] == 0; n++ {
		if blocks, e := SplitBlocks(b[:len(b)-n]); e == nil {
			return blocks, nil
		}
	}
	return nil, err
}

// Append appends the block.
func (p *PDPortDataCheck) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockPDPortDataCheck, 1, 0)
	b = append(b, 0, 0)
	b = appendU16(b, p.Slot)
	b = appendU16(b, p.Subslot)
	if p.Peers != nil {
		var ps int
		b, ps = beginBlock(b, BlockCheckPeers, 1, 0)
		b = append(b, uint8(min(len(p.Peers), 255)))
		for i, peer := range p.Peers {
			if i == 255 {
				break
			}
			b = appendString8(b, peer.PortID)
			b = appendString8(b, peer.ChassisID)
		}
		b = endBlock(b, ps)
	}
	for _, o := range p.Other {
		b = appendRaw(b, o)
	}
	return endBlock(pad4(b, s), s)
}

// DecodePDPortDataCheck decodes a PDPortDataCheck block.
func DecodePDPortDataCheck(blk RawBlock) (PDPortDataCheck, error) {
	var p PDPortDataCheck
	if blk.Header.Type != BlockPDPortDataCheck {
		return p, fmt.Errorf("pnio: %w: %s instead of PDPortDataCheck", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "PDPortDataCheck")
	r.skip(2)
	p.Slot = r.u16()
	p.Subslot = r.u16()
	if r.err != nil {
		return p, r.err
	}
	subs, err := splitSubBlocks(r.rest())
	if err != nil {
		return p, err
	}
	for _, sb := range subs {
		if sb.Header.Type != BlockCheckPeers || p.Peers != nil {
			p.Other = append(p.Other, sb)
			continue
		}
		sr := newReader(sb.Body, "CheckPeers")
		n := int(sr.u8())
		p.Peers = make([]CheckPeer, 0, min(n, sr.remaining()/2))
		for i := 0; i < n && sr.err == nil; i++ {
			var peer CheckPeer
			peer.PortID = sr.string8()
			peer.ChassisID = sr.string8()
			p.Peers = append(p.Peers, peer)
		}
		if sr.err != nil {
			return p, sr.err
		}
	}
	return p, nil
}

// appendAdjust appends an adjust sub-block of a u32 value and its
// AdjustProperties, padded to four octets as p-net sends it.
func appendAdjust32(b []byte, t BlockType, v uint32) []byte {
	b, s := beginBlock(b, t, 1, 0)
	b = pad4(b, s)
	b = appendU32(b, v)
	b = appendU16(b, 0) // AdjustProperties
	return endBlock(pad4(b, s), s)
}

// Append appends the block.
func (p *PDPortDataAdjust) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockPDPortDataAdjust, 1, 0)
	b = append(b, 0, 0)
	b = appendU16(b, p.Slot)
	b = appendU16(b, p.Subslot)
	if p.HasPeerToPeerBoundary {
		b = appendAdjust32(b, BlockAdjustPeerToPeerBoundary, p.PeerToPeerBoundary)
	}
	if p.HasDCPBoundary {
		b = appendAdjust32(b, BlockAdjustDCPBoundary, p.DCPBoundary)
	}
	for _, o := range p.Other {
		b = appendRaw(b, o)
	}
	return endBlock(pad4(b, s), s)
}

// DecodePDPortDataAdjust decodes a PDPortDataAdjust block.
func DecodePDPortDataAdjust(blk RawBlock) (PDPortDataAdjust, error) {
	var p PDPortDataAdjust
	if blk.Header.Type != BlockPDPortDataAdjust {
		return p, fmt.Errorf("pnio: %w: %s instead of PDPortDataAdjust", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "PDPortDataAdjust")
	r.skip(2)
	p.Slot = r.u16()
	p.Subslot = r.u16()
	if r.err != nil {
		return p, r.err
	}
	subs, err := splitSubBlocks(r.rest())
	if err != nil {
		return p, err
	}
	for _, sb := range subs {
		var dst *uint32
		var has *bool
		switch sb.Header.Type {
		case BlockAdjustPeerToPeerBoundary:
			dst, has = &p.PeerToPeerBoundary, &p.HasPeerToPeerBoundary
		case BlockAdjustDCPBoundary:
			dst, has = &p.DCPBoundary, &p.HasDCPBoundary
		default:
		}
		if dst == nil || *has {
			p.Other = append(p.Other, sb)
			continue
		}
		sr := newReader(sb.Body, sb.Header.Type.String())
		sr.align4()
		*dst = sr.u32()
		if sr.err != nil {
			return p, sr.err
		}
		*has = true
	}
	return p, nil
}

// Append appends the block.
func (p *PDInterfaceAdjust) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockPDInterfaceAdjust, 1, 0)
	b = pad4(b, s)
	b = appendU32(b, p.MultipleInterfaceMode)
	return endBlock(b, s)
}

// DecodePDInterfaceAdjust decodes a PDInterfaceAdjust block.
func DecodePDInterfaceAdjust(blk RawBlock) (PDInterfaceAdjust, error) {
	var p PDInterfaceAdjust
	if blk.Header.Type != BlockPDInterfaceAdjust {
		return p, fmt.Errorf("pnio: %w: %s instead of PDInterfaceAdjust", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "PDInterfaceAdjust")
	r.align4()
	p.MultipleInterfaceMode = r.u32()
	return p, r.err
}

func appendAddr(b []byte, a netip.Addr) []byte {
	if !a.Is4() {
		return append(b, 0, 0, 0, 0)
	}
	v := a.As4()
	return append(b, v[:]...)
}

func (r *reader) addr() netip.Addr {
	v := r.bytes(4)
	if len(v) < 4 {
		return netip.Addr{}
	}
	return netip.AddrFrom4([4]byte(v))
}

// Append appends the block.
func (p *PDInterfaceDataReal) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockPDInterfaceDataReal, 1, 0)
	b = appendString8(b, p.ChassisID)
	b = pad4(b, s)
	b = append(b, p.MAC[:]...)
	b = pad4(b, s)
	b = appendAddr(b, p.IP)
	b = appendAddr(b, p.Mask)
	b = appendAddr(b, p.Gateway)
	return endBlock(b, s)
}

// DecodePDInterfaceDataReal decodes a PDInterfaceDataReal block.
func DecodePDInterfaceDataReal(blk RawBlock) (PDInterfaceDataReal, error) {
	var p PDInterfaceDataReal
	if blk.Header.Type != BlockPDInterfaceDataReal {
		return p, fmt.Errorf("pnio: %w: %s instead of PDInterfaceDataReal", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "PDInterfaceDataReal")
	p.ChassisID = r.string8()
	r.align4()
	p.MAC = r.mac()
	r.align4()
	p.IP = r.addr()
	p.Mask = r.addr()
	p.Gateway = r.addr()
	return p, r.err
}

// PDRealData is the content of the PDRealData record (index 0xF841): the
// real data of the interface and of its ports.
type PDRealData struct {
	// Interface is the PDInterfaceDataReal, nil when absent.
	Interface *PDInterfaceDataReal `json:"interface,omitempty"`
	// InterfaceSlot and InterfaceSubslot address the interface submodule.
	InterfaceSlot    uint16           `json:"interfaceSlot"`
	InterfaceSubslot uint16           `json:"interfaceSubslot"`
	Ports            []PDPortDataReal `json:"ports"`
}

// AppendMultipleBlockHeader appends a MultipleBlockHeader for a
// submodule around the blocks body appends.
func AppendMultipleBlockHeader(b []byte, api uint32, slot, subslot uint16, body func([]byte) []byte) []byte {
	b, s := beginBlock(b, BlockMultipleBlockHeader, 1, 0)
	b = append(b, 0, 0)
	b = appendU32(b, api)
	b = appendU16(b, slot)
	b = appendU16(b, subslot)
	b = body(b)
	return endBlock(b, s)
}

// AppendPDRealDataRecord appends the PDRealData record: the interface's
// PDInterfaceDataReal, then each port's PDPortDataReal, each in a
// MultipleBlockHeader.
func AppendPDRealDataRecord(b []byte, api uint32, rd *PDRealData) []byte {
	if rd.Interface != nil {
		b = AppendMultipleBlockHeader(b, api, rd.InterfaceSlot, rd.InterfaceSubslot, rd.Interface.Append)
	}
	return AppendPDRealData(b, api, rd.Ports)
}

// DecodePDRealDataRecord decodes the PDRealData record: the interface's
// and the ports' real data, at the top level or in MultipleBlockHeaders.
// Other blocks are skipped.
func DecodePDRealDataRecord(b []byte) (PDRealData, error) {
	var rd PDRealData
	err := decodePDRealData(b, 0, 0, &rd)
	return rd, err
}

func decodePDRealData(b []byte, slot, subslot uint16, rd *PDRealData) error {
	blocks, err := SplitBlocks(b)
	if err != nil {
		return err
	}
	for _, blk := range blocks {
		switch blk.Header.Type {
		case BlockPDPortDataReal:
			p, err := DecodePDPortDataReal(blk)
			if err != nil {
				return err
			}
			rd.Ports = append(rd.Ports, p)
		case BlockPDInterfaceDataReal:
			p, err := DecodePDInterfaceDataReal(blk)
			if err != nil {
				return err
			}
			rd.Interface, rd.InterfaceSlot, rd.InterfaceSubslot = &p, slot, subslot
		case BlockMultipleBlockHeader:
			r := newReader(blk.Body, "MultipleBlockHeader")
			r.skip(2)
			r.u32()
			s, ss := r.u16(), r.u16()
			if r.err != nil {
				return r.err
			}
			if err := decodePDRealData(r.rest(), s, ss, rd); err != nil {
				return err
			}
		default:
		}
	}
	return nil
}
