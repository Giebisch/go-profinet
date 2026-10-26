// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/rpc"
)

// ControlBlock is the IODControlReq/Res (PrmBegin, PrmEnd), IOXControlReq/
// Res (ApplicationReady) and ReleaseBlockReq/Res: they share one layout.
type ControlBlock struct {
	// Type is the block type, for example BlockPrmEndReq or
	// BlockAppReadyRes.
	Type       BlockType
	ARUUID     rpc.UUID
	SessionKey uint16
	// Command is a Cmd* bit.
	Command    uint16
	Properties uint16
	// AlarmSequence is the AlarmSequenceNumber of the plug variants
	// (BlockPrmEndPlugReq, BlockAppReadyPlugReq).
	AlarmSequence uint16
}

func isPlug(t BlockType) bool {
	return t&0x7FFF == BlockPrmEndPlugReq || t&0x7FFF == BlockAppReadyPlugReq
}

// Append appends the block.
func (c *ControlBlock) Append(b []byte) []byte {
	b, s := beginBlock(b, c.Type, 1, 0)
	b = append(b, 0, 0)
	b = append(b, c.ARUUID[:]...)
	b = appendU16(b, c.SessionKey)
	if isPlug(c.Type) {
		b = appendU16(b, c.AlarmSequence)
	} else {
		b = append(b, 0, 0)
	}
	b = appendU16(b, c.Command)
	b = appendU16(b, c.Properties)
	return endBlock(b, s)
}

// DecodeControlBlock decodes one of the control blocks.
func DecodeControlBlock(blk RawBlock) (ControlBlock, error) {
	c := ControlBlock{Type: blk.Header.Type}
	switch blk.Header.Type & 0x7FFF {
	case BlockPrmEndReq, BlockPrmEndPlugReq, BlockAppReadyReq, BlockAppReadyPlugReq,
		BlockReleaseReq, BlockReadyForCompReq, BlockReadyForRTC3Req, BlockPrmBeginReq:
	default:
		return c, fmt.Errorf("pnio: %w: %s is not a control block", profinet.ErrMalformed, blk.Header.Type)
	}
	if err := checkVersion(blk, 1, 0); err != nil {
		return c, err
	}
	r := newReader(blk.Body, blk.Header.Type.String())
	r.skip(2)
	c.ARUUID = r.uuid()
	c.SessionKey = r.u16()
	seq := r.u16()
	if isPlug(c.Type) {
		c.AlarmSequence = seq
	}
	c.Command = r.u16()
	c.Properties = r.u16()
	return c, r.end()
}

// Response returns the response block that acknowledges c: the response
// type with the Done command.
func (c *ControlBlock) Response() ControlBlock {
	return ControlBlock{Type: c.Type | 0x8000, ARUUID: c.ARUUID, SessionKey: c.SessionKey, Command: CmdDone, AlarmSequence: c.AlarmSequence}
}

// RecordHeaderLen is the length of a read or write header block.
const RecordHeaderLen = 64

// ReadReq is the IODReadReqHeader.
type ReadReq struct {
	Seq     uint16
	ARUUID  rpc.UUID
	API     uint32
	Slot    uint16
	Subslot uint16
	Index   Index
	// Length is the RecordDataLength: the most the reader accepts.
	Length uint32
	// TargetARUUID selects the AR of an implicit read of AR-related
	// records; nil otherwise.
	TargetARUUID rpc.UUID
}

// Append appends the block.
func (q *ReadReq) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockReadReq, 1, 0)
	b = appendU16(b, q.Seq)
	b = append(b, q.ARUUID[:]...)
	b = appendU32(b, q.API)
	b = appendU16(b, q.Slot)
	b = appendU16(b, q.Subslot)
	b = append(b, 0, 0)
	b = appendU16(b, uint16(q.Index))
	b = appendU32(b, q.Length)
	b = append(b, q.TargetARUUID[:]...)
	b = append(b, make([]byte, 8)...)
	return endBlock(b, s)
}

// DecodeReadReq decodes an IODReadReqHeader. Older controllers send the
// header without the target AR UUID; it is then nil.
func DecodeReadReq(blk RawBlock) (ReadReq, error) {
	var q ReadReq
	if blk.Header.Type != BlockReadReq {
		return q, fmt.Errorf("pnio: %w: %s instead of IODReadReqHeader", profinet.ErrMalformed, blk.Header.Type)
	}
	if err := checkVersion(blk, 1, 0); err != nil {
		return q, err
	}
	r := newReader(blk.Body, "IODReadReqHeader")
	q.Seq = r.u16()
	q.ARUUID = r.uuid()
	q.API = r.u32()
	q.Slot = r.u16()
	q.Subslot = r.u16()
	r.skip(2)
	q.Index = Index(r.u16())
	q.Length = r.u32()
	if r.remaining() >= 16 {
		q.TargetARUUID = r.uuid()
	}
	return q, r.err
}

// ReadRes is the IODReadResHeader; the record data follows it.
type ReadRes struct {
	Seq       uint16
	ARUUID    rpc.UUID
	API       uint32
	Slot      uint16
	Subslot   uint16
	Index     Index
	Length    uint32
	AddValue1 uint16
	AddValue2 uint16
}

// Append appends the block.
func (q *ReadRes) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockReadRes, 1, 0)
	b = appendU16(b, q.Seq)
	b = append(b, q.ARUUID[:]...)
	b = appendU32(b, q.API)
	b = appendU16(b, q.Slot)
	b = appendU16(b, q.Subslot)
	b = append(b, 0, 0)
	b = appendU16(b, uint16(q.Index))
	b = appendU32(b, q.Length)
	b = appendU16(b, q.AddValue1)
	b = appendU16(b, q.AddValue2)
	b = append(b, make([]byte, 20)...)
	return endBlock(b, s)
}

// DecodeReadRes decodes an IODReadResHeader.
func DecodeReadRes(blk RawBlock) (ReadRes, error) {
	var q ReadRes
	if blk.Header.Type != BlockReadRes {
		return q, fmt.Errorf("pnio: %w: %s instead of IODReadResHeader", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "IODReadResHeader")
	q.Seq = r.u16()
	q.ARUUID = r.uuid()
	q.API = r.u32()
	q.Slot = r.u16()
	q.Subslot = r.u16()
	r.skip(2)
	q.Index = Index(r.u16())
	q.Length = r.u32()
	q.AddValue1 = r.u16()
	q.AddValue2 = r.u16()
	return q, r.err
}

// WriteReq is the IODWriteReqHeader; the record data follows it.
type WriteReq struct {
	Seq     uint16
	ARUUID  rpc.UUID
	API     uint32
	Slot    uint16
	Subslot uint16
	Index   Index
	Length  uint32
}

// Append appends the block.
func (q *WriteReq) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockWriteReq, 1, 0)
	b = appendU16(b, q.Seq)
	b = append(b, q.ARUUID[:]...)
	b = appendU32(b, q.API)
	b = appendU16(b, q.Slot)
	b = appendU16(b, q.Subslot)
	b = append(b, 0, 0)
	b = appendU16(b, uint16(q.Index))
	b = appendU32(b, q.Length)
	b = append(b, make([]byte, 24)...)
	return endBlock(b, s)
}

// DecodeWriteReq decodes an IODWriteReqHeader.
func DecodeWriteReq(blk RawBlock) (WriteReq, error) {
	var q WriteReq
	if blk.Header.Type != BlockWriteReq {
		return q, fmt.Errorf("pnio: %w: %s instead of IODWriteReqHeader", profinet.ErrMalformed, blk.Header.Type)
	}
	if err := checkVersion(blk, 1, 0); err != nil {
		return q, err
	}
	r := newReader(blk.Body, "IODWriteReqHeader")
	q.Seq = r.u16()
	q.ARUUID = r.uuid()
	q.API = r.u32()
	q.Slot = r.u16()
	q.Subslot = r.u16()
	r.skip(2)
	q.Index = Index(r.u16())
	q.Length = r.u32()
	return q, r.err
}

// WriteRes is the IODWriteResHeader.
type WriteRes struct {
	Seq       uint16
	ARUUID    rpc.UUID
	API       uint32
	Slot      uint16
	Subslot   uint16
	Index     Index
	Length    uint32
	AddValue1 uint16
	AddValue2 uint16
	Status    Status
}

// Append appends the block.
func (q *WriteRes) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockWriteRes, 1, 0)
	b = appendU16(b, q.Seq)
	b = append(b, q.ARUUID[:]...)
	b = appendU32(b, q.API)
	b = appendU16(b, q.Slot)
	b = appendU16(b, q.Subslot)
	b = append(b, 0, 0)
	b = appendU16(b, uint16(q.Index))
	b = appendU32(b, q.Length)
	b = appendU16(b, q.AddValue1)
	b = appendU16(b, q.AddValue2)
	b = q.Status.Append(b)
	b = append(b, make([]byte, 16)...)
	return endBlock(b, s)
}

// DecodeWriteRes decodes an IODWriteResHeader.
func DecodeWriteRes(blk RawBlock) (WriteRes, error) {
	var q WriteRes
	if blk.Header.Type != BlockWriteRes {
		return q, fmt.Errorf("pnio: %w: %s instead of IODWriteResHeader", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "IODWriteResHeader")
	q.Seq = r.u16()
	q.ARUUID = r.uuid()
	q.API = r.u32()
	q.Slot = r.u16()
	q.Subslot = r.u16()
	r.skip(2)
	q.Index = Index(r.u16())
	q.Length = r.u32()
	q.AddValue1 = r.u16()
	q.AddValue2 = r.u16()
	if st := r.bytes(4); st != nil {
		q.Status = decodeStatus(st)
	}
	return q, r.err
}

// WriteMultipleItem is one record of a WriteMultiple (index 0xE040).
type WriteMultipleItem struct {
	Header WriteReq
	Data   []byte
}

// AppendWriteMultiple appends the record data of a WriteMultiple: each
// write header and its data, padded to a multiple of four octets except
// the last.
func AppendWriteMultiple(b []byte, items []WriteMultipleItem) []byte {
	start := len(b)
	for i, it := range items {
		h := it.Header
		h.Length = uint32(len(it.Data))
		b = h.Append(b)
		b = append(b, it.Data...)
		if i < len(items)-1 {
			b = padTo(b, start, 4)
		}
	}
	return b
}

// SplitWriteMultiple decodes the record data of a WriteMultiple.
func SplitWriteMultiple(b []byte) ([]WriteMultipleItem, error) {
	var out []WriteMultipleItem
	start := len(b)
	for len(b) > 0 {
		blk, rest, err := ReadBlock(b)
		if err != nil {
			return out, err
		}
		h, err := DecodeWriteReq(blk)
		if err != nil {
			return out, err
		}
		if int(h.Length) > len(rest) {
			return out, fmt.Errorf("pnio: %w: WriteMultiple record of %d octets exceeds the %d present", profinet.ErrMalformed, h.Length, len(rest))
		}
		out = append(out, WriteMultipleItem{Header: h, Data: rest[:h.Length]})
		b = rest[h.Length:]
		for len(b) > 0 && (start-len(b))%4 != 0 {
			if b[0] != 0 {
				return out, fmt.Errorf("pnio: %w: non-zero padding in WriteMultiple", profinet.ErrMalformed)
			}
			b = b[1:]
		}
	}
	return out, nil
}
