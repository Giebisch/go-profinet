// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"

	"github.com/otfabric/go-profinet"
)

// UserStructureIdentifier values of diagnosis and alarm data.
const (
	USIChannelDiagnosis    uint16 = 0x8000
	USIMultiple            uint16 = 0x8001
	USIExtChannelDiagnosis uint16 = 0x8002
	USIQualifiedChannel    uint16 = 0x8003
	USIMaintenance         uint16 = 0x8100
)

// ChannelNumberSubmodule is the channel number of a diagnosis that
// concerns the whole submodule.
const ChannelNumberSubmodule uint16 = 0x8000

// ChannelProperties bit fields.
const (
	ChanTypeMask         uint16 = 0x00FF
	ChanAccumulative     uint16 = 0x0100
	ChanMaintRequired    uint16 = 0x0200
	ChanMaintDemanded    uint16 = 0x0400
	ChanMaintQualified   uint16 = 0x0600
	ChanSpecAllDisappear uint16 = 0 << 11
	ChanSpecAppears      uint16 = 1 << 11
	ChanSpecDisappears   uint16 = 2 << 11
	ChanSpecOtherRemain  uint16 = 3 << 11
	ChanSpecMask         uint16 = 3 << 11
	ChanDirManufacturer  uint16 = 0 << 13
	ChanDirInput         uint16 = 1 << 13
	ChanDirOutput        uint16 = 2 << 13
	ChanDirInOut         uint16 = 3 << 13
)

// Common ChannelErrorType values (IEC 61158-6-10).
const (
	ChannelErrShortCircuit  uint16 = 0x0001
	ChannelErrUndervoltage  uint16 = 0x0002
	ChannelErrOvervoltage   uint16 = 0x0003
	ChannelErrOverload      uint16 = 0x0004
	ChannelErrOvertemp      uint16 = 0x0005
	ChannelErrLineBreak     uint16 = 0x0006
	ChannelErrUpperLimit    uint16 = 0x0007
	ChannelErrLowerLimit    uint16 = 0x0008
	ChannelErrError         uint16 = 0x0009
	ChannelErrDataTransmiss uint16 = 0x8000
	ChannelErrRemoteMismat  uint16 = 0x8001
)

// DiagnosisItem is one channel diagnosis entry. Ext* and Qualifier are
// used by the extended and qualified formats.
type DiagnosisItem struct {
	ChannelNumber     uint16
	ChannelProperties uint16
	ErrorType         uint16
	ExtErrorType      uint16
	ExtAddValue       uint32
	Qualifier         uint32
}

// Appears reports whether the item announces an appearing diagnosis.
func (d *DiagnosisItem) Appears() bool { return d.ChannelProperties&ChanSpecMask == ChanSpecAppears }

func itemSize(usi uint16) int {
	switch usi {
	case USIChannelDiagnosis:
		return 6
	case USIExtChannelDiagnosis:
		return 12
	case USIQualifiedChannel:
		return 16
	default:
		return 0
	}
}

// AppendDiagnosisItems appends items in the format of usi.
func AppendDiagnosisItems(b []byte, usi uint16, items []DiagnosisItem) []byte {
	for _, it := range items {
		b = appendU16(b, it.ChannelNumber)
		b = appendU16(b, it.ChannelProperties)
		b = appendU16(b, it.ErrorType)
		if usi == USIExtChannelDiagnosis || usi == USIQualifiedChannel {
			b = appendU16(b, it.ExtErrorType)
			b = appendU32(b, it.ExtAddValue)
		}
		if usi == USIQualifiedChannel {
			b = appendU32(b, it.Qualifier)
		}
	}
	return b
}

// DecodeDiagnosisItems decodes the items of channel diagnosis data in the
// format of usi.
func DecodeDiagnosisItems(usi uint16, b []byte) ([]DiagnosisItem, error) {
	n := itemSize(usi)
	if n == 0 {
		return nil, fmt.Errorf("pnio: %w: user structure identifier 0x%04X is not a channel diagnosis", profinet.ErrUnsupported, usi)
	}
	if len(b)%n != 0 {
		return nil, fmt.Errorf("pnio: %w: channel diagnosis of %d octets is not a multiple of %d", profinet.ErrMalformed, len(b), n)
	}
	r := newReader(b, "channel diagnosis")
	out := make([]DiagnosisItem, 0, len(b)/n)
	for r.remaining() > 0 {
		it := DiagnosisItem{ChannelNumber: r.u16(), ChannelProperties: r.u16(), ErrorType: r.u16()}
		if usi != USIChannelDiagnosis {
			it.ExtErrorType = r.u16()
			it.ExtAddValue = r.u32()
		}
		if usi == USIQualifiedChannel {
			it.Qualifier = r.u32()
		}
		out = append(out, it)
	}
	return out, r.err
}

// DiagnosisData is a DiagnosisData block (version 1.1, with the API) of
// one submodule.
type DiagnosisData struct {
	API               uint32
	Slot              uint16
	Subslot           uint16
	ChannelNumber     uint16
	ChannelProperties uint16
	// USI is the user structure identifier; for the channel formats Items
	// holds the entries, otherwise Data holds the manufacturer data.
	USI   uint16
	Items []DiagnosisItem
	Data  []byte
}

// Append appends the block.
func (d *DiagnosisData) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockDiagnosisData, 1, 1)
	b = appendU32(b, d.API)
	b = appendU16(b, d.Slot)
	b = appendU16(b, d.Subslot)
	b = appendU16(b, d.ChannelNumber)
	b = appendU16(b, d.ChannelProperties)
	b = appendU16(b, d.USI)
	if itemSize(d.USI) > 0 {
		b = AppendDiagnosisItems(b, d.USI, d.Items)
	} else {
		b = append(b, d.Data...)
	}
	return endBlock(b, s)
}

// DecodeDiagnosisData decodes a DiagnosisData block of version 1.0 (no
// API, which is then 0) or 1.1.
func DecodeDiagnosisData(blk RawBlock) (DiagnosisData, error) {
	var d DiagnosisData
	if blk.Header.Type != BlockDiagnosisData {
		return d, fmt.Errorf("pnio: %w: %s instead of DiagnosisData", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "DiagnosisData")
	if blk.Header.VersionLow >= 1 {
		d.API = r.u32()
	}
	d.Slot = r.u16()
	d.Subslot = r.u16()
	d.ChannelNumber = r.u16()
	d.ChannelProperties = r.u16()
	d.USI = r.u16()
	rest := r.rest()
	if r.err != nil {
		return d, r.err
	}
	if itemSize(d.USI) > 0 {
		items, err := DecodeDiagnosisItems(d.USI, rest)
		d.Items = items
		return d, err
	}
	d.Data = append([]byte(nil), rest...)
	return d, nil
}

// AlarmSpecifier bits.
const (
	AlarmSpecSeqMask       uint16 = 0x07FF
	AlarmSpecChannelDiag   uint16 = 0x0800
	AlarmSpecManufDiag     uint16 = 0x1000
	AlarmSpecSubmoduleDiag uint16 = 0x2000
	AlarmSpecARDiag        uint16 = 0x8000
)

// AlarmNotification is the payload of an alarm (block 0x0001 high, 0x0002
// low priority).
type AlarmNotification struct {
	High                 bool
	Type                 AlarmType
	API                  uint32
	Slot                 uint16
	Subslot              uint16
	ModuleIdentNumber    uint32
	SubmoduleIdentNumber uint32
	Specifier            uint16
	// USI and Data are the optional alarm item; HasItem reports whether
	// it is present.
	HasItem bool
	USI     uint16
	Data    []byte
}

// Sequence returns the alarm sequence number of the specifier.
func (a *AlarmNotification) Sequence() uint16 { return a.Specifier & AlarmSpecSeqMask }

// Append appends the block.
func (a *AlarmNotification) Append(b []byte) []byte {
	t := BlockAlarmNotificationLow
	if a.High {
		t = BlockAlarmNotificationHigh
	}
	b, s := beginBlock(b, t, 1, 0)
	b = appendU16(b, uint16(a.Type))
	b = appendU32(b, a.API)
	b = appendU16(b, a.Slot)
	b = appendU16(b, a.Subslot)
	b = appendU32(b, a.ModuleIdentNumber)
	b = appendU32(b, a.SubmoduleIdentNumber)
	b = appendU16(b, a.Specifier)
	if a.HasItem {
		b = appendU16(b, a.USI)
		b = append(b, a.Data...)
	}
	return endBlock(b, s)
}

// DecodeAlarmNotification decodes an alarm notification block.
func DecodeAlarmNotification(blk RawBlock) (AlarmNotification, error) {
	var a AlarmNotification
	switch blk.Header.Type {
	case BlockAlarmNotificationHigh:
		a.High = true
	case BlockAlarmNotificationLow:
	default:
		return a, fmt.Errorf("pnio: %w: %s instead of AlarmNotification", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "AlarmNotification")
	a.Type = AlarmType(r.u16())
	a.API = r.u32()
	a.Slot = r.u16()
	a.Subslot = r.u16()
	a.ModuleIdentNumber = r.u32()
	a.SubmoduleIdentNumber = r.u32()
	a.Specifier = r.u16()
	if r.err == nil && r.remaining() >= 2 {
		a.HasItem = true
		a.USI = r.u16()
		a.Data = append([]byte(nil), r.rest()...)
	}
	if r.err == nil && r.remaining() != 0 {
		return a, fmt.Errorf("pnio: %w: AlarmNotification with a %d-octet item", profinet.ErrMalformed, r.remaining())
	}
	return a, r.err
}

// AlarmAck acknowledges an alarm (block 0x8001 high, 0x8002 low).
type AlarmAck struct {
	High      bool
	Type      AlarmType
	API       uint32
	Slot      uint16
	Subslot   uint16
	Specifier uint16
	Status    Status
}

// Append appends the block.
func (a *AlarmAck) Append(b []byte) []byte {
	t := BlockAlarmAckLow
	if a.High {
		t = BlockAlarmAckHigh
	}
	b, s := beginBlock(b, t, 1, 0)
	b = appendU16(b, uint16(a.Type))
	b = appendU32(b, a.API)
	b = appendU16(b, a.Slot)
	b = appendU16(b, a.Subslot)
	b = appendU16(b, a.Specifier)
	b = a.Status.Append(b)
	return endBlock(b, s)
}

// DecodeAlarmAck decodes an AlarmAck block.
func DecodeAlarmAck(blk RawBlock) (AlarmAck, error) {
	var a AlarmAck
	switch blk.Header.Type {
	case BlockAlarmAckHigh:
		a.High = true
	case BlockAlarmAckLow:
	default:
		return a, fmt.Errorf("pnio: %w: %s instead of AlarmAck", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "AlarmAck")
	a.Type = AlarmType(r.u16())
	a.API = r.u32()
	a.Slot = r.u16()
	a.Subslot = r.u16()
	a.Specifier = r.u16()
	if st := r.bytes(4); st != nil {
		a.Status = decodeStatus(st)
	}
	return a, r.end()
}

// AckFor returns the acknowledgement of an alarm notification.
func AckFor(n *AlarmNotification, status Status) AlarmAck {
	return AlarmAck{High: n.High, Type: n.Type, API: n.API, Slot: n.Slot, Subslot: n.Subslot, Specifier: n.Specifier, Status: status}
}

// IdentSubmodule is a submodule in identification data.
type IdentSubmodule struct {
	Subslot     uint16
	IdentNumber uint32
}

// IdentSlot is a module in identification data.
type IdentSlot struct {
	Slot        uint16
	IdentNumber uint32
	Submodules  []IdentSubmodule
}

// IdentAPI groups the slots of one API.
type IdentAPI struct {
	API   uint32
	Slots []IdentSlot
}

// IdentificationData is the Real- (0x0013) or ExpectedIdentificationData
// (0x0012) block, version 1.1.
type IdentificationData struct {
	Expected bool
	APIs     []IdentAPI
}

// Append appends the block.
func (d *IdentificationData) Append(b []byte) []byte {
	t := BlockRealIdentData
	if d.Expected {
		t = BlockExpectedIdentData
	}
	b, s := beginBlock(b, t, 1, 1)
	b = appendU16(b, uint16(len(d.APIs)))
	for _, a := range d.APIs {
		b = appendU32(b, a.API)
		b = appendU16(b, uint16(len(a.Slots)))
		for _, sl := range a.Slots {
			b = appendU16(b, sl.Slot)
			b = appendU32(b, sl.IdentNumber)
			b = appendU16(b, uint16(len(sl.Submodules)))
			for _, sm := range sl.Submodules {
				b = appendU16(b, sm.Subslot)
				b = appendU32(b, sm.IdentNumber)
			}
		}
	}
	return endBlock(b, s)
}

// DecodeIdentificationData decodes an identification data block of
// version 1.1.
func DecodeIdentificationData(blk RawBlock) (IdentificationData, error) {
	var d IdentificationData
	switch blk.Header.Type {
	case BlockExpectedIdentData:
		d.Expected = true
	case BlockRealIdentData:
	default:
		return d, fmt.Errorf("pnio: %w: %s instead of IdentificationData", profinet.ErrMalformed, blk.Header.Type)
	}
	if blk.Header.VersionHigh != 1 || blk.Header.VersionLow != 1 {
		return d, fmt.Errorf("pnio: %w: IdentificationData version %d.%d", profinet.ErrUnsupported, blk.Header.VersionHigh, blk.Header.VersionLow)
	}
	r := newReader(blk.Body, "IdentificationData")
	n := r.count(6)
	for i := 0; i < n && r.err == nil; i++ {
		a := IdentAPI{API: r.u32()}
		k := r.count(8)
		for j := 0; j < k && r.err == nil; j++ {
			sl := IdentSlot{Slot: r.u16(), IdentNumber: r.u32()}
			m := r.count(6)
			for x := 0; x < m && r.err == nil; x++ {
				sl.Submodules = append(sl.Submodules, IdentSubmodule{Subslot: r.u16(), IdentNumber: r.u32()})
			}
			a.Slots = append(a.Slots, sl)
		}
		d.APIs = append(d.APIs, a)
	}
	return d, r.end()
}
