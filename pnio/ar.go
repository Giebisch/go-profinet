// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/rpc"
)

// ARBlockReq opens an application relationship.
type ARBlockReq struct {
	Type       ARType
	ARUUID     rpc.UUID
	SessionKey uint16
	// InitiatorMAC is the controller's MAC address, the destination of the
	// device's RT and alarm frames.
	InitiatorMAC ethernet.MAC
	// InitiatorObject is the controller's PNIO object UUID, the object of
	// the device's ApplicationReady call.
	InitiatorObject rpc.UUID
	Properties      uint32
	// ActivityTimeoutFactor is the CM initiator activity timeout in units
	// of 100 ms (1..1000).
	ActivityTimeoutFactor uint16
	UDPRTPort             uint16
	StationName           string
}

// Append appends the block.
func (a *ARBlockReq) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockARReq, 1, 0)
	b = appendU16(b, uint16(a.Type))
	b = append(b, a.ARUUID[:]...)
	b = appendU16(b, a.SessionKey)
	b = append(b, a.InitiatorMAC[:]...)
	b = append(b, a.InitiatorObject[:]...)
	b = appendU32(b, a.Properties)
	b = appendU16(b, a.ActivityTimeoutFactor)
	b = appendU16(b, a.UDPRTPort)
	b = appendU16(b, uint16(len(a.StationName)))
	b = append(b, a.StationName...)
	return endBlock(b, s)
}

// DecodeARBlockReq decodes the body of an ARBlockReq.
func DecodeARBlockReq(blk RawBlock) (ARBlockReq, error) {
	var a ARBlockReq
	if err := checkVersion(blk, 1, 0); err != nil {
		return a, err
	}
	r := newReader(blk.Body, "ARBlockReq")
	a.Type = ARType(r.u16())
	a.ARUUID = r.uuid()
	a.SessionKey = r.u16()
	a.InitiatorMAC = r.mac()
	a.InitiatorObject = r.uuid()
	a.Properties = r.u32()
	a.ActivityTimeoutFactor = r.u16()
	a.UDPRTPort = r.u16()
	n := int(r.u16())
	if n > 240 {
		return a, &StatusError{Op: "decode ARBlockReq", Status: blockError(BlockARReq, 12)}
	}
	a.StationName = string(r.bytes(n))
	return a, r.end()
}

// ARBlockRes answers an ARBlockReq.
type ARBlockRes struct {
	Type         ARType
	ARUUID       rpc.UUID
	SessionKey   uint16
	ResponderMAC ethernet.MAC
	UDPRTPort    uint16
}

// Append appends the block.
func (a *ARBlockRes) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockARRes, 1, 0)
	b = appendU16(b, uint16(a.Type))
	b = append(b, a.ARUUID[:]...)
	b = appendU16(b, a.SessionKey)
	b = append(b, a.ResponderMAC[:]...)
	b = appendU16(b, a.UDPRTPort)
	return endBlock(b, s)
}

// DecodeARBlockRes decodes the body of an ARBlockRes.
func DecodeARBlockRes(blk RawBlock) (ARBlockRes, error) {
	var a ARBlockRes
	r := newReader(blk.Body, "ARBlockRes")
	a.Type = ARType(r.u16())
	a.ARUUID = r.uuid()
	a.SessionKey = r.u16()
	a.ResponderMAC = r.mac()
	a.UDPRTPort = r.u16()
	return a, r.end()
}

// IODataObject places the data of one submodule (or its IOCS) in a frame.
type IODataObject struct {
	Slot, Subslot uint16
	// FrameOffset is the offset in the C_SDU.
	FrameOffset uint16
}

// IOCRAPI lists the data objects and consumer status octets of one API in
// an IOCR.
type IOCRAPI struct {
	API           uint32
	IODataObjects []IODataObject
	IOCS          []IODataObject
}

// IOCRBlockReq describes one IO communication relationship.
type IOCRBlockReq struct {
	Type       IOCRType
	Reference  uint16
	LT         uint16
	Properties uint32
	// DataLength is the length of the C_SDU (40..1440).
	DataLength uint16
	FrameID    uint16
	// SendClockFactor × 31.25 µs is the send clock; ReductionRatio
	// multiplies it into the cycle.
	SendClockFactor uint16
	ReductionRatio  uint16
	Phase           uint16
	Sequence        uint16
	FrameSendOffset uint32
	WatchdogFactor  uint16
	DataHoldFactor  uint16
	TagHeader       uint16
	MulticastMAC    ethernet.MAC
	APIs            []IOCRAPI
}

// RTClass returns the RT class of the properties.
func (c *IOCRBlockReq) RTClass() RTClass { return RTClass(c.Properties & 0x0F) }

// Append appends the block.
func (c *IOCRBlockReq) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockIOCRReq, 1, 0)
	b = appendU16(b, uint16(c.Type))
	b = appendU16(b, c.Reference)
	b = appendU16(b, c.LT)
	b = appendU32(b, c.Properties)
	b = appendU16(b, c.DataLength)
	b = appendU16(b, c.FrameID)
	b = appendU16(b, c.SendClockFactor)
	b = appendU16(b, c.ReductionRatio)
	b = appendU16(b, c.Phase)
	b = appendU16(b, c.Sequence)
	b = appendU32(b, c.FrameSendOffset)
	b = appendU16(b, c.WatchdogFactor)
	b = appendU16(b, c.DataHoldFactor)
	b = appendU16(b, c.TagHeader)
	b = append(b, c.MulticastMAC[:]...)
	b = appendU16(b, uint16(len(c.APIs)))
	for _, a := range c.APIs {
		b = appendU32(b, a.API)
		b = appendU16(b, uint16(len(a.IODataObjects)))
		for _, o := range a.IODataObjects {
			b = appendU16(b, o.Slot)
			b = appendU16(b, o.Subslot)
			b = appendU16(b, o.FrameOffset)
		}
		b = appendU16(b, uint16(len(a.IOCS)))
		for _, o := range a.IOCS {
			b = appendU16(b, o.Slot)
			b = appendU16(b, o.Subslot)
			b = appendU16(b, o.FrameOffset)
		}
	}
	return endBlock(b, s)
}

// DecodeIOCRBlockReq decodes the body of an IOCRBlockReq.
func DecodeIOCRBlockReq(blk RawBlock) (IOCRBlockReq, error) {
	var c IOCRBlockReq
	if err := checkVersion(blk, 1, 0); err != nil {
		return c, err
	}
	r := newReader(blk.Body, "IOCRBlockReq")
	c.Type = IOCRType(r.u16())
	c.Reference = r.u16()
	c.LT = r.u16()
	c.Properties = r.u32()
	c.DataLength = r.u16()
	c.FrameID = r.u16()
	c.SendClockFactor = r.u16()
	c.ReductionRatio = r.u16()
	c.Phase = r.u16()
	c.Sequence = r.u16()
	c.FrameSendOffset = r.u32()
	c.WatchdogFactor = r.u16()
	c.DataHoldFactor = r.u16()
	c.TagHeader = r.u16()
	c.MulticastMAC = r.mac()
	n := r.count(8)
	for i := 0; i < n && r.err == nil; i++ {
		var a IOCRAPI
		a.API = r.u32()
		m := r.count(6)
		for j := 0; j < m && r.err == nil; j++ {
			a.IODataObjects = append(a.IODataObjects, IODataObject{Slot: r.u16(), Subslot: r.u16(), FrameOffset: r.u16()})
		}
		m = r.count(6)
		for j := 0; j < m && r.err == nil; j++ {
			a.IOCS = append(a.IOCS, IODataObject{Slot: r.u16(), Subslot: r.u16(), FrameOffset: r.u16()})
		}
		c.APIs = append(c.APIs, a)
	}
	return c, r.end()
}

// IOCRBlockRes answers an IOCRBlockReq with the FrameID in use.
type IOCRBlockRes struct {
	Type      IOCRType
	Reference uint16
	FrameID   uint16
}

// Append appends the block.
func (c *IOCRBlockRes) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockIOCRRes, 1, 0)
	b = appendU16(b, uint16(c.Type))
	b = appendU16(b, c.Reference)
	b = appendU16(b, c.FrameID)
	return endBlock(b, s)
}

// DecodeIOCRBlockRes decodes the body of an IOCRBlockRes.
func DecodeIOCRBlockRes(blk RawBlock) (IOCRBlockRes, error) {
	r := newReader(blk.Body, "IOCRBlockRes")
	c := IOCRBlockRes{Type: IOCRType(r.u16()), Reference: r.u16(), FrameID: r.u16()}
	return c, r.end()
}

// AlarmCRBlockReq configures the alarm communication relationship.
type AlarmCRBlockReq struct {
	Type       uint16
	LT         uint16
	Properties uint32
	// RTATimeoutFactor × 100 ms is the alarm retransmission timeout
	// (1..100); RTARetries the number of retransmissions (3..15).
	RTATimeoutFactor uint16
	RTARetries       uint16
	// LocalAlarmReference is the initiator's alarm endpoint.
	LocalAlarmReference uint16
	MaxAlarmDataLength  uint16
	TagHeaderHigh       uint16
	TagHeaderLow        uint16
}

// Append appends the block.
func (a *AlarmCRBlockReq) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockAlarmCRReq, 1, 0)
	b = appendU16(b, a.Type)
	b = appendU16(b, a.LT)
	b = appendU32(b, a.Properties)
	b = appendU16(b, a.RTATimeoutFactor)
	b = appendU16(b, a.RTARetries)
	b = appendU16(b, a.LocalAlarmReference)
	b = appendU16(b, a.MaxAlarmDataLength)
	b = appendU16(b, a.TagHeaderHigh)
	b = appendU16(b, a.TagHeaderLow)
	return endBlock(b, s)
}

// DecodeAlarmCRBlockReq decodes the body of an AlarmCRBlockReq.
func DecodeAlarmCRBlockReq(blk RawBlock) (AlarmCRBlockReq, error) {
	var a AlarmCRBlockReq
	if err := checkVersion(blk, 1, 0); err != nil {
		return a, err
	}
	r := newReader(blk.Body, "AlarmCRBlockReq")
	a.Type = r.u16()
	a.LT = r.u16()
	a.Properties = r.u32()
	a.RTATimeoutFactor = r.u16()
	a.RTARetries = r.u16()
	a.LocalAlarmReference = r.u16()
	a.MaxAlarmDataLength = r.u16()
	a.TagHeaderHigh = r.u16()
	a.TagHeaderLow = r.u16()
	return a, r.end()
}

// AlarmCRBlockRes answers an AlarmCRBlockReq with the responder's alarm
// endpoint.
type AlarmCRBlockRes struct {
	Type                uint16
	LocalAlarmReference uint16
	MaxAlarmDataLength  uint16
}

// Append appends the block.
func (a *AlarmCRBlockRes) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockAlarmCRRes, 1, 0)
	b = appendU16(b, a.Type)
	b = appendU16(b, a.LocalAlarmReference)
	b = appendU16(b, a.MaxAlarmDataLength)
	return endBlock(b, s)
}

// DecodeAlarmCRBlockRes decodes the body of an AlarmCRBlockRes.
func DecodeAlarmCRBlockRes(blk RawBlock) (AlarmCRBlockRes, error) {
	r := newReader(blk.Body, "AlarmCRBlockRes")
	a := AlarmCRBlockRes{Type: r.u16(), LocalAlarmReference: r.u16(), MaxAlarmDataLength: r.u16()}
	return a, r.end()
}

// DataDescription describes the input or output data of a submodule.
type DataDescription struct {
	// Type is DataInput or DataOutput.
	Type       uint16
	Length     uint16
	LengthIOCS uint8
	LengthIOPS uint8
}

// ExpectedSubmodule is one submodule the controller expects.
type ExpectedSubmodule struct {
	Subslot     uint16
	IdentNumber uint32
	// Properties: bits 0-1 the SubmoduleType, then SharedInput,
	// ReduceInputSubmoduleDataLength, ReduceOutputSubmoduleDataLength,
	// DiscardIOXS.
	Properties   uint16
	Descriptions []DataDescription
}

// Type returns the submodule type.
func (e *ExpectedSubmodule) Type() SubmoduleType { return SubmoduleType(e.Properties & 0x03) }

// InputLength returns the input data length (0 without input).
func (e *ExpectedSubmodule) InputLength() uint16 {
	for _, d := range e.Descriptions {
		if d.Type == DataInput {
			return d.Length
		}
	}
	return 0
}

// OutputLength returns the output data length (0 without output).
func (e *ExpectedSubmodule) OutputLength() uint16 {
	for _, d := range e.Descriptions {
		if d.Type == DataOutput {
			return d.Length
		}
	}
	return 0
}

// ExpectedModule is one module the controller expects in a slot.
type ExpectedModule struct {
	API         uint32
	Slot        uint16
	IdentNumber uint32
	Properties  uint16
	Submodules  []ExpectedSubmodule
}

// ExpectedSubmoduleBlockReq lists the modules and submodules a controller
// expects. One block usually describes one slot.
type ExpectedSubmoduleBlockReq struct {
	Modules []ExpectedModule
}

// NewDataDescriptions returns the data descriptions of a submodule type
// with the given lengths: a NO_IO submodule is described as an input of
// length 0.
func NewDataDescriptions(t SubmoduleType, in, out uint16) []DataDescription {
	var d []DataDescription
	if t == SubmoduleNoIO || t.HasInput() {
		d = append(d, DataDescription{Type: DataInput, Length: in, LengthIOCS: 1, LengthIOPS: 1})
	}
	if t.HasOutput() {
		d = append(d, DataDescription{Type: DataOutput, Length: out, LengthIOCS: 1, LengthIOPS: 1})
	}
	return d
}

// Append appends the block.
func (e *ExpectedSubmoduleBlockReq) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockExpectedSubmReq, 1, 0)
	b = appendU16(b, uint16(len(e.Modules)))
	for _, m := range e.Modules {
		b = appendU32(b, m.API)
		b = appendU16(b, m.Slot)
		b = appendU32(b, m.IdentNumber)
		b = appendU16(b, m.Properties)
		b = appendU16(b, uint16(len(m.Submodules)))
		for _, sm := range m.Submodules {
			b = appendU16(b, sm.Subslot)
			b = appendU32(b, sm.IdentNumber)
			b = appendU16(b, sm.Properties)
			for _, d := range sm.Descriptions {
				b = appendU16(b, d.Type)
				b = appendU16(b, d.Length)
				b = append(b, d.LengthIOCS, d.LengthIOPS)
			}
		}
	}
	return endBlock(b, s)
}

// DecodeExpectedSubmoduleBlockReq decodes the body of an
// ExpectedSubmoduleBlockReq. The number of data descriptions of a
// submodule follows from its type: two for INPUT_OUTPUT, one otherwise.
func DecodeExpectedSubmoduleBlockReq(blk RawBlock) (ExpectedSubmoduleBlockReq, error) {
	var e ExpectedSubmoduleBlockReq
	if err := checkVersion(blk, 1, 0); err != nil {
		return e, err
	}
	r := newReader(blk.Body, "ExpectedSubmoduleBlockReq")
	n := r.count(14)
	for i := 0; i < n && r.err == nil; i++ {
		m := ExpectedModule{API: r.u32(), Slot: r.u16(), IdentNumber: r.u32(), Properties: r.u16()}
		k := r.count(14)
		for j := 0; j < k && r.err == nil; j++ {
			sm := ExpectedSubmodule{Subslot: r.u16(), IdentNumber: r.u32(), Properties: r.u16()}
			nd := 1
			if sm.Type() == SubmoduleInputOutput {
				nd = 2
			}
			for d := 0; d < nd && r.err == nil; d++ {
				dd := DataDescription{Type: r.u16(), Length: r.u16(), LengthIOCS: r.u8(), LengthIOPS: r.u8()}
				if r.err == nil && dd.Type != DataInput && dd.Type != DataOutput {
					return e, fmt.Errorf("pnio: %w: data description type %d", profinet.ErrMalformed, dd.Type)
				}
				sm.Descriptions = append(sm.Descriptions, dd)
			}
			m.Submodules = append(m.Submodules, sm)
		}
		e.Modules = append(e.Modules, m)
	}
	return e, r.end()
}

// DiffSubmodule is a submodule whose state differs from the expected one.
type DiffSubmodule struct {
	Subslot     uint16
	IdentNumber uint32
	State       uint16
}

// DiffModule is a module in the ModuleDiffBlock.
type DiffModule struct {
	Slot        uint16
	IdentNumber uint32
	State       uint16
	Submodules  []DiffSubmodule
}

// DiffAPI groups the modules of one API in the ModuleDiffBlock.
type DiffAPI struct {
	API     uint32
	Modules []DiffModule
}

// ModuleDiffBlock reports how the real configuration differs from the
// expected one.
type ModuleDiffBlock struct {
	APIs []DiffAPI
}

// Append appends the block.
func (m *ModuleDiffBlock) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockModuleDiff, 1, 0)
	b = appendU16(b, uint16(len(m.APIs)))
	for _, a := range m.APIs {
		b = appendU32(b, a.API)
		b = appendU16(b, uint16(len(a.Modules)))
		for _, md := range a.Modules {
			b = appendU16(b, md.Slot)
			b = appendU32(b, md.IdentNumber)
			b = appendU16(b, md.State)
			b = appendU16(b, uint16(len(md.Submodules)))
			for _, sm := range md.Submodules {
				b = appendU16(b, sm.Subslot)
				b = appendU32(b, sm.IdentNumber)
				b = appendU16(b, sm.State)
			}
		}
	}
	return endBlock(b, s)
}

// DecodeModuleDiffBlock decodes the body of a ModuleDiffBlock.
func DecodeModuleDiffBlock(blk RawBlock) (ModuleDiffBlock, error) {
	var m ModuleDiffBlock
	r := newReader(blk.Body, "ModuleDiffBlock")
	n := r.count(6)
	for i := 0; i < n && r.err == nil; i++ {
		a := DiffAPI{API: r.u32()}
		k := r.count(10)
		for j := 0; j < k && r.err == nil; j++ {
			md := DiffModule{Slot: r.u16(), IdentNumber: r.u32(), State: r.u16()}
			ns := r.count(8)
			for x := 0; x < ns && r.err == nil; x++ {
				md.Submodules = append(md.Submodules, DiffSubmodule{Subslot: r.u16(), IdentNumber: r.u32(), State: r.u16()})
			}
			a.Modules = append(a.Modules, md)
		}
		m.APIs = append(m.APIs, a)
	}
	return m, r.end()
}

// ARServerBlock carries the responder's station name.
type ARServerBlock struct {
	StationName string
}

// Append appends the block, padded to a multiple of four octets.
func (a *ARServerBlock) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockARServer, 1, 0)
	b = appendU16(b, uint16(len(a.StationName)))
	b = append(b, a.StationName...)
	b = padTo(b, s, 4)
	return endBlock(b, s)
}

// DecodeARServerBlock decodes the body of an ARServerBlock.
func DecodeARServerBlock(blk RawBlock) (ARServerBlock, error) {
	r := newReader(blk.Body, "ARServerBlock")
	n := int(r.u16())
	a := ARServerBlock{StationName: string(r.bytes(n))}
	return a, r.err
}

// ARRPCBlockReq carries the initiator's RPC server port.
type ARRPCBlockReq struct {
	InitiatorRPCServerPort uint16
}

// Append appends the block.
func (a *ARRPCBlockReq) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockARRPCReq, 1, 0)
	b = appendU16(b, a.InitiatorRPCServerPort)
	return endBlock(b, s)
}

// DecodeARRPCBlockReq decodes the body of an ARRPCBlockReq.
func DecodeARRPCBlockReq(blk RawBlock) (ARRPCBlockReq, error) {
	r := newReader(blk.Body, "ARRPCBlockReq")
	a := ARRPCBlockReq{InitiatorRPCServerPort: r.u16()}
	return a, r.end()
}
