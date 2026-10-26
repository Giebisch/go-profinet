// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// PDPortPeer is a neighbor of a port as its LLDP reports it.
type PDPortPeer struct {
	// PortID and ChassisID are the neighbor's LLDP port and chassis IDs:
	// for a PROFINET device, "port-001" and its NameOfStation.
	PortID    string `json:"portId"`
	ChassisID string `json:"chassisId"`
	// LineDelay is the measured line delay in nanoseconds (0: not
	// measured).
	LineDelay uint32       `json:"lineDelay"`
	MAC       ethernet.MAC `json:"mac"`
}

// PDPortDataReal is the real data of a port (index 0x802A of the port
// submodule, and part of PDRealData 0xF841): its own port ID, its
// neighbors and its link.
type PDPortDataReal struct {
	Slot      uint16       `json:"slot"`
	Subslot   uint16       `json:"subslot"`
	OwnPortID string       `json:"ownPortId"`
	Peers     []PDPortPeer `json:"peers"`
	// MAUType is the IEEE 802.3 MAU type of the link (0: unknown;
	// 0x0010: 100BASE-TX full duplex).
	MAUType           uint16 `json:"mauType"`
	DomainBoundary    uint32 `json:"domainBoundary"`
	MulticastBoundary uint32 `json:"multicastBoundary"`
	// LinkState carries the link state (low octet: 1 up, 2 down) and the
	// port state (high octet).
	LinkState uint16 `json:"linkState"`
	// MediaType is 0 unknown, 1 copper, 2 fiber, 3 radio.
	MediaType uint32 `json:"mediaType"`
}

// Link states of PDPortDataReal.LinkState (low octet).
const (
	LinkStateUp   uint16 = 1
	LinkStateDown uint16 = 2
)

// pad4 appends zero octets until the block that starts at start has a
// length that is a multiple of four.
func pad4(b []byte, start int) []byte {
	for (len(b)-start)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// align4 skips the padding that aligns the reader of a block body to four
// octets from the start of the block (the body starts after the six octets
// of the header).
func (r *reader) align4() {
	r.skip((4 - (BlockHeaderLen+r.off)%4) % 4)
}

func appendString8(b []byte, s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	return append(append(b, uint8(len(s))), s...)
}

func (r *reader) string8() string { return string(r.bytes(int(r.u8()))) }

// Append appends the block.
func (p *PDPortDataReal) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockPDPortDataReal, 1, 0)
	b = append(b, 0, 0)
	b = appendU16(b, p.Slot)
	b = appendU16(b, p.Subslot)
	b = appendString8(b, p.OwnPortID)
	b = append(b, uint8(min(len(p.Peers), 255)))
	b = pad4(b, s)
	for i, peer := range p.Peers {
		if i == 255 {
			break
		}
		b = appendString8(b, peer.PortID)
		b = appendString8(b, peer.ChassisID)
		b = pad4(b, s)
		b = appendU32(b, peer.LineDelay)
		b = append(b, peer.MAC[:]...)
		b = pad4(b, s)
	}
	b = appendU16(b, p.MAUType)
	b = pad4(b, s)
	b = appendU32(b, p.DomainBoundary)
	b = appendU32(b, p.MulticastBoundary)
	b = appendU16(b, p.LinkState)
	b = pad4(b, s)
	b = appendU32(b, p.MediaType)
	return endBlock(b, s)
}

// DecodePDPortDataReal decodes a PDPortDataReal block.
func DecodePDPortDataReal(blk RawBlock) (PDPortDataReal, error) {
	var p PDPortDataReal
	if blk.Header.Type != BlockPDPortDataReal {
		return p, fmt.Errorf("pnio: %w: %s instead of PDPortDataReal", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "PDPortDataReal")
	r.skip(2)
	p.Slot = r.u16()
	p.Subslot = r.u16()
	p.OwnPortID = r.string8()
	n := int(r.u8())
	r.align4()
	for i := 0; i < n && r.err == nil; i++ {
		var peer PDPortPeer
		peer.PortID = r.string8()
		peer.ChassisID = r.string8()
		r.align4()
		peer.LineDelay = r.u32()
		peer.MAC = r.mac()
		r.align4()
		p.Peers = append(p.Peers, peer)
	}
	p.MAUType = r.u16()
	r.align4()
	p.DomainBoundary = r.u32()
	p.MulticastBoundary = r.u32()
	p.LinkState = r.u16()
	r.align4()
	p.MediaType = r.u32()
	// Later versions append the RTClass 3 port status and more; they are
	// not decoded.
	if r.err != nil {
		return p, r.err
	}
	return p, nil
}

// AppendPDRealData appends the PDRealData record (index 0xF841) of the
// ports: one MultipleBlockHeader per port submodule, holding its
// PDPortDataReal.
func AppendPDRealData(b []byte, api uint32, ports []PDPortDataReal) []byte {
	for i := range ports {
		p := &ports[i]
		var s int
		b, s = beginBlock(b, BlockMultipleBlockHeader, 1, 0)
		b = append(b, 0, 0)
		b = appendU32(b, api)
		b = appendU16(b, p.Slot)
		b = appendU16(b, p.Subslot)
		b = p.Append(b)
		b = endBlock(b, s)
	}
	return b
}

// DecodePDRealData decodes the PDRealData record (index 0xF841) and
// returns the PDPortDataReal blocks it holds, at the top level or in
// MultipleBlockHeaders; the other blocks (PDInterfaceDataReal, ...) are
// skipped.
func DecodePDRealData(b []byte) ([]PDPortDataReal, error) {
	blocks, err := SplitBlocks(b)
	if err != nil {
		return nil, err
	}
	var out []PDPortDataReal
	for _, blk := range blocks {
		switch blk.Header.Type {
		case BlockPDPortDataReal:
			p, err := DecodePDPortDataReal(blk)
			if err != nil {
				return out, err
			}
			out = append(out, p)
		case BlockMultipleBlockHeader:
			if len(blk.Body) < 10 {
				return out, fmt.Errorf("pnio: %w: MultipleBlockHeader of %d octets", profinet.ErrMalformed, len(blk.Body))
			}
			inner, err := DecodePDRealData(blk.Body[10:])
			out = append(out, inner...)
			if err != nil {
				return out, err
			}
		default:
		}
	}
	return out, nil
}
