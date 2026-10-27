// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"

	"github.com/otfabric/go-profinet"
)

// The PROFINET MRP records of the interface and port submodules. The
// layouts are those Wireshark's PNIO dissector reads (block version 1.0;
// docs/research/EVIDENCE-MRP-PTCP-MGMT.md, A.4); the multi-instance
// versions (1.1 of the adjust and check blocks, 1.2 of the real block) are
// not implemented. The unit of the timer fields of MrpManagerParams and
// MrpClientParams is not public: they are carried as numbers.

// MRP block types.
const (
	BlockPDInterfaceMrpDataAdjust BlockType = 0x0211
	BlockPDInterfaceMrpDataReal   BlockType = 0x0212
	BlockPDInterfaceMrpDataCheck  BlockType = 0x0213
	BlockPDPortMrpDataAdjust      BlockType = 0x0214
	BlockPDPortMrpDataReal        BlockType = 0x0215
	BlockMrpManagerParams         BlockType = 0x0216
	BlockMrpClientParams          BlockType = 0x0217
	BlockMrpRingStateData         BlockType = 0x0219
)

// MRP record indexes.
const (
	IndexPDInterfaceMrpDataReal   Index = 0x8050
	IndexPDInterfaceMrpDataCheck  Index = 0x8051
	IndexPDInterfaceMrpDataAdjust Index = 0x8052
	IndexPDPortMrpDataAdjust      Index = 0x8053
	IndexPDPortMrpDataReal        Index = 0x8054
)

// MRP_Role values of the PNIO blocks.
const (
	MrpRoleOff     uint16 = 0
	MrpRoleClient  uint16 = 1
	MrpRoleManager uint16 = 2
)

// MRP_Check bits of PDInterfaceMrpDataCheck.
const (
	MrpCheckManager    uint32 = 0x01
	MrpCheckDomainUUID uint32 = 0x02
)

// MrpManagerParams are a manager's timers.
type MrpManagerParams struct {
	Prio, TopChangeT, TopNRmax, TestShortT, TestDefaultT, TestNRmax uint16
}

// MrpClientParams are a client's timers.
type MrpClientParams struct {
	LinkDownT, LinkUpT, LinkNRmax uint16
}

// PDInterfaceMrpDataAdjust is the MRP configuration a controller writes to
// the interface submodule (index 0x8052).
type PDInterfaceMrpDataAdjust struct {
	Domain     [16]byte
	Role       uint16
	DomainName string
	Manager    *MrpManagerParams
	Client     *MrpClientParams
	// Other are sub-blocks this package does not decode.
	Other []RawBlock
}

// PDInterfaceMrpDataReal is the MRP state of the interface (index 0x8050).
type PDInterfaceMrpDataReal struct {
	Domain     [16]byte
	Role       uint16
	DomainName string
	Version    uint16
	// RingState is MrpRingStateData (a manager's): 0 open, 1 closed.
	RingState    uint16
	HasRingState bool
	Other        []RawBlock
}

// PDInterfaceMrpDataCheck is what the device is to check (index 0x8051).
type PDInterfaceMrpDataCheck struct {
	Domain [16]byte
	Check  uint32
}

// PDPortMrpData is PDPortMrpDataAdjust (index 0x8053) or
// PDPortMrpDataReal (0x8054): a ring port's domain.
type PDPortMrpData struct {
	Real   bool
	Domain [16]byte
}

func appendSub(b []byte, t BlockType, body func([]byte, int) []byte) []byte {
	b, s := beginBlock(b, t, 1, 0)
	b = body(b, s)
	return endBlock(b, s)
}

func appendMrpParams(b []byte, m *MrpManagerParams, c *MrpClientParams) []byte {
	if m != nil {
		b = appendSub(b, BlockMrpManagerParams, func(b []byte, s int) []byte {
			for _, v := range []uint16{m.Prio, m.TopChangeT, m.TopNRmax, m.TestShortT, m.TestDefaultT, m.TestNRmax} {
				b = appendU16(b, v)
			}
			return pad4(b, s)
		})
	}
	if c != nil {
		b = appendSub(b, BlockMrpClientParams, func(b []byte, s int) []byte {
			for _, v := range []uint16{c.LinkDownT, c.LinkUpT, c.LinkNRmax} {
				b = appendU16(b, v)
			}
			return pad4(b, s)
		})
	}
	return b
}

// Append appends the block (version 1.0).
func (p *PDInterfaceMrpDataAdjust) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockPDInterfaceMrpDataAdjust, 1, 0)
	b = pad4(b, s)
	b = append(b, p.Domain[:]...)
	b = appendU16(b, p.Role)
	b = pad4(b, s)
	b = appendString8(b, p.DomainName)
	b = pad4(b, s)
	b = appendMrpParams(b, p.Manager, p.Client)
	for _, o := range p.Other {
		b = o.Append(b)
	}
	return endBlock(b, s)
}

func decodeMrpSubs(rest []byte, m **MrpManagerParams, c **MrpClientParams, ring *uint16, hasRing *bool) ([]RawBlock, error) {
	subs, err := splitSubBlocks(rest)
	if err != nil {
		return nil, err
	}
	var other []RawBlock
	for _, sb := range subs {
		r := newReader(sb.Body, sb.Header.Type.String())
		switch {
		case sb.Header.Type == BlockMrpManagerParams && m != nil:
			*m = &MrpManagerParams{Prio: r.u16(), TopChangeT: r.u16(), TopNRmax: r.u16(), TestShortT: r.u16(), TestDefaultT: r.u16(), TestNRmax: r.u16()}
		case sb.Header.Type == BlockMrpClientParams && c != nil:
			*c = &MrpClientParams{LinkDownT: r.u16(), LinkUpT: r.u16(), LinkNRmax: r.u16()}
		case sb.Header.Type == BlockMrpRingStateData && ring != nil:
			*ring, *hasRing = r.u16(), true
		default:
			other = append(other, sb)
			continue
		}
		if r.err != nil {
			return nil, r.err
		}
	}
	return other, nil
}

func checkBlock(blk RawBlock, t BlockType, name string) error {
	if blk.Header.Type != t {
		return fmt.Errorf("pnio: %w: %s instead of %s", profinet.ErrMalformed, blk.Header.Type, name)
	}
	if blk.Header.VersionHigh != 1 || blk.Header.VersionLow != 0 {
		return fmt.Errorf("pnio: %w: %s version %d.%d (1.0 is implemented)", profinet.ErrUnsupported, name, blk.Header.VersionHigh, blk.Header.VersionLow)
	}
	return nil
}

func (r *reader) domain() [16]byte {
	var d [16]byte
	copy(d[:], r.bytes(16))
	return d
}

// DecodePDInterfaceMrpDataAdjust decodes the block (version 1.0).
func DecodePDInterfaceMrpDataAdjust(blk RawBlock) (PDInterfaceMrpDataAdjust, error) {
	var p PDInterfaceMrpDataAdjust
	if err := checkBlock(blk, BlockPDInterfaceMrpDataAdjust, "PDInterfaceMrpDataAdjust"); err != nil {
		return p, err
	}
	r := newReader(blk.Body, "PDInterfaceMrpDataAdjust")
	r.align4()
	p.Domain = r.domain()
	p.Role = r.u16()
	r.align4()
	p.DomainName = r.string8()
	r.align4()
	if r.err != nil {
		return p, r.err
	}
	var err error
	p.Other, err = decodeMrpSubs(r.rest(), &p.Manager, &p.Client, nil, nil)
	return p, err
}

// Append appends the block (version 1.0).
func (p *PDInterfaceMrpDataReal) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockPDInterfaceMrpDataReal, 1, 0)
	b = pad4(b, s)
	b = append(b, p.Domain[:]...)
	b = appendU16(b, p.Role)
	b = appendString8(b, p.DomainName)
	b = appendU16(b, p.Version)
	b = pad4(b, s)
	if p.HasRingState {
		b = appendSub(b, BlockMrpRingStateData, func(b []byte, s int) []byte {
			return pad4(appendU16(b, p.RingState), s)
		})
	}
	for _, o := range p.Other {
		b = o.Append(b)
	}
	return endBlock(b, s)
}

// DecodePDInterfaceMrpDataReal decodes the block (version 1.0).
func DecodePDInterfaceMrpDataReal(blk RawBlock) (PDInterfaceMrpDataReal, error) {
	var p PDInterfaceMrpDataReal
	if err := checkBlock(blk, BlockPDInterfaceMrpDataReal, "PDInterfaceMrpDataReal"); err != nil {
		return p, err
	}
	r := newReader(blk.Body, "PDInterfaceMrpDataReal")
	r.align4()
	p.Domain = r.domain()
	p.Role = r.u16()
	p.DomainName = r.string8()
	p.Version = r.u16()
	r.align4()
	if r.err != nil {
		return p, r.err
	}
	var err error
	p.Other, err = decodeMrpSubs(r.rest(), nil, nil, &p.RingState, &p.HasRingState)
	return p, err
}

// Append appends the block (version 1.0).
func (p *PDInterfaceMrpDataCheck) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockPDInterfaceMrpDataCheck, 1, 0)
	b = pad4(b, s)
	b = append(b, p.Domain[:]...)
	b = appendU32(b, p.Check)
	return endBlock(b, s)
}

// DecodePDInterfaceMrpDataCheck decodes the block (version 1.0).
func DecodePDInterfaceMrpDataCheck(blk RawBlock) (PDInterfaceMrpDataCheck, error) {
	var p PDInterfaceMrpDataCheck
	if err := checkBlock(blk, BlockPDInterfaceMrpDataCheck, "PDInterfaceMrpDataCheck"); err != nil {
		return p, err
	}
	r := newReader(blk.Body, "PDInterfaceMrpDataCheck")
	r.align4()
	p.Domain = r.domain()
	p.Check = r.u32()
	return p, r.err
}

// Append appends PDPortMrpDataAdjust or, with Real, PDPortMrpDataReal
// (version 1.0).
func (p *PDPortMrpData) Append(b []byte) []byte {
	t := BlockPDPortMrpDataAdjust
	if p.Real {
		t = BlockPDPortMrpDataReal
	}
	b, s := beginBlock(b, t, 1, 0)
	b = pad4(b, s)
	b = append(b, p.Domain[:]...)
	return endBlock(b, s)
}

// DecodePDPortMrpData decodes PDPortMrpDataAdjust or PDPortMrpDataReal
// (version 1.0).
func DecodePDPortMrpData(blk RawBlock) (PDPortMrpData, error) {
	var p PDPortMrpData
	name := "PDPortMrpDataAdjust"
	if blk.Header.Type == BlockPDPortMrpDataReal {
		p.Real, name = true, "PDPortMrpDataReal"
	}
	if err := checkBlock(blk, map[bool]BlockType{false: BlockPDPortMrpDataAdjust, true: BlockPDPortMrpDataReal}[p.Real], name); err != nil {
		return p, err
	}
	r := newReader(blk.Body, name)
	r.align4()
	p.Domain = r.domain()
	return p, r.err
}
