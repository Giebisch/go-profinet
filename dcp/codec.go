// SPDX-License-Identifier: MIT

// Package dcp implements the PROFINET Discovery and basic Configuration
// Protocol (IEC 61158-6-10): the codec of DCP frames and blocks, a
// requester (Identify, Get, Set, Hello) and a responder for IO-Devices.
//
// DCP runs directly on Ethernet (EtherType 0x8892). Identify requests go
// to the multicast address 01-0E-CF-00-00-00, Get and Set requests to the
// MAC address of one device.
//
// A Set changes the station name or the IP suite of an industrial device,
// or resets it; the requester only ever sends one when the application
// calls [Client.Set] (or one of its helpers) explicitly.
package dcp

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/otfabric/go-profinet"
)

// FrameID values of DCP (the first two octets of the PROFINET payload).
const (
	FrameIDHello            uint16 = 0xFEFC
	FrameIDGetSet           uint16 = 0xFEFD
	FrameIDIdentifyRequest  uint16 = 0xFEFE
	FrameIDIdentifyResponse uint16 = 0xFEFF
)

// HeaderLen is the length of the DCP header after the FrameID.
const HeaderLen = 10

// MaxDataLen bounds the DCPDataLength of a frame: the payload of a
// standard Ethernet frame less the FrameID and the DCP header.
const MaxDataLen = 1500 - 2 - HeaderLen

// ServiceID is the DCP service.
type ServiceID uint8

// Services.
const (
	ServiceGet      ServiceID = 3
	ServiceSet      ServiceID = 4
	ServiceIdentify ServiceID = 5
	ServiceHello    ServiceID = 6
)

func (s ServiceID) String() string {
	switch s {
	case ServiceGet:
		return "Get"
	case ServiceSet:
		return "Set"
	case ServiceIdentify:
		return "Identify"
	case ServiceHello:
		return "Hello"
	default:
		return fmt.Sprintf("Service(%d)", uint8(s))
	}
}

// ServiceType distinguishes requests from responses.
type ServiceType uint8

// Service types.
const (
	TypeRequest ServiceType = 0
	// TypeResponseSuccess is a response whose service was executed.
	TypeResponseSuccess ServiceType = 1
	// TypeResponseUnsupported is the response to a service the device
	// does not support.
	TypeResponseUnsupported ServiceType = 5
)

func (t ServiceType) String() string {
	switch t {
	case TypeRequest:
		return "Request"
	case TypeResponseSuccess:
		return "Response"
	case TypeResponseUnsupported:
		return "ResponseUnsupported"
	default:
		return fmt.Sprintf("ServiceType(%d)", uint8(t))
	}
}

// Option is the option of a block; with the suboption it names the
// block's content.
type Option uint8

// Options.
const (
	OptionIP               Option = 1
	OptionDeviceProperties Option = 2
	OptionDHCP             Option = 3
	OptionControl          Option = 5
	OptionDeviceInitiative Option = 6
	OptionNMEDomain        Option = 7
	OptionAll              Option = 0xFF
)

// BlockID is an option and suboption pair, option in the high octet.
type BlockID uint16

// NewBlockID returns the block ID of an option and suboption.
func NewBlockID(o Option, sub uint8) BlockID { return BlockID(uint16(o)<<8 | uint16(sub)) }

// Option returns the option.
func (b BlockID) Option() Option { return Option(b >> 8) }

// Suboption returns the suboption.
func (b BlockID) Suboption() uint8 { return uint8(b) }

// Block IDs. The names follow IEC 61158-6-10.
const (
	IPMAC        BlockID = 0x0101 // MAC address (Get only)
	IPParameter  BlockID = 0x0102 // IP address, subnet mask, gateway
	IPFullSuite  BlockID = 0x0103 // IP parameter and DNS servers
	DeviceVendor BlockID = 0x0201 // TypeOfStation, the vendor's device name
	DeviceName   BlockID = 0x0202 // NameOfStation
	DeviceID     BlockID = 0x0203 // VendorID and DeviceID
	DeviceRole   BlockID = 0x0204 // IO-Device, IO-Controller, ...
	DeviceOpts   BlockID = 0x0205 // the blocks the device supports
	DeviceAlias  BlockID = 0x0206 // AliasName (identify filter only)
	DeviceInst   BlockID = 0x0207 // DeviceInstance
	DeviceOEMID  BlockID = 0x0208 // OEM VendorID and DeviceID
	DeviceGW     BlockID = 0x0209 // StandardGateway
	DeviceRSI    BlockID = 0x020A // RSIProperties
	DHCPHostName BlockID = 0x030C
	DHCPVendor   BlockID = 0x033C
	DHCPClientID BlockID = 0x033D
	DHCPControl  BlockID = 0x03FF
	CtrlStart    BlockID = 0x0501 // start of a Set transaction
	CtrlStop     BlockID = 0x0502 // end of a Set transaction
	CtrlSignal   BlockID = 0x0503 // flash the device's signal LED
	CtrlResponse BlockID = 0x0504 // result of one Set block
	CtrlFactory  BlockID = 0x0505 // FactoryReset (legacy)
	CtrlReset    BlockID = 0x0506 // ResetToFactory
	Initiative   BlockID = 0x0601 // DeviceInitiative (Hello)
	AllSelector  BlockID = 0xFFFF // Identify all / Get all
)

func (b BlockID) String() string {
	switch b {
	case IPMAC:
		return "IP/MAC"
	case IPParameter:
		return "IP/Parameter"
	case IPFullSuite:
		return "IP/FullSuite"
	case DeviceVendor:
		return "Device/Vendor"
	case DeviceName:
		return "Device/NameOfStation"
	case DeviceID:
		return "Device/DeviceID"
	case DeviceRole:
		return "Device/Role"
	case DeviceOpts:
		return "Device/Options"
	case DeviceAlias:
		return "Device/AliasName"
	case DeviceInst:
		return "Device/Instance"
	case DeviceOEMID:
		return "Device/OEMDeviceID"
	case DeviceGW:
		return "Device/StandardGateway"
	case DeviceRSI:
		return "Device/RSIProperties"
	case CtrlStart:
		return "Control/Start"
	case CtrlStop:
		return "Control/Stop"
	case CtrlSignal:
		return "Control/Signal"
	case CtrlResponse:
		return "Control/Response"
	case CtrlFactory:
		return "Control/FactoryReset"
	case CtrlReset:
		return "Control/ResetToFactory"
	case Initiative:
		return "DeviceInitiative"
	case AllSelector:
		return "All"
	default:
		return fmt.Sprintf("%d/%d", b.Option(), b.Suboption())
	}
}

// BlockError is the error code of a Control/Response block.
type BlockError uint8

// Block errors.
const (
	BlockErrorNone               BlockError = 0
	BlockErrorOptionUnsupported  BlockError = 1
	BlockErrorSuboptionUnsupport BlockError = 2
	BlockErrorSuboptionNotSet    BlockError = 3
	BlockErrorResource           BlockError = 4
	BlockErrorSetNotPossible     BlockError = 5
	BlockErrorInOperation        BlockError = 6
)

func (e BlockError) String() string {
	switch e {
	case BlockErrorNone:
		return "OK"
	case BlockErrorOptionUnsupported:
		return "option not supported"
	case BlockErrorSuboptionUnsupport:
		return "suboption not supported or no data set"
	case BlockErrorSuboptionNotSet:
		return "suboption not set"
	case BlockErrorResource:
		return "resource error"
	case BlockErrorSetNotPossible:
		return "SET not possible by local reasons"
	case BlockErrorInOperation:
		return "in operation, SET not possible"
	default:
		return fmt.Sprintf("block error %d", uint8(e))
	}
}

// Qualifier values of a Set block.
const (
	// QualifierTemporary stores the value until the next power cycle.
	QualifierTemporary uint16 = 0
	// QualifierPermanent stores the value permanently.
	QualifierPermanent uint16 = 1
)

// ResetToFactory modes (the BlockQualifier of Control/ResetToFactory,
// shifted left by one with bit 0 unused).
const (
	ResetApplicationData  uint16 = 1 << 1
	ResetCommunication    uint16 = 2 << 1
	ResetEngineering      uint16 = 3 << 1
	ResetAllData          uint16 = 4 << 1
	ResetDevice           uint16 = 8 << 1
	ResetAndRestoreDevice uint16 = 9 << 1
)

// BlockInfo bits of IP blocks in responses.
const (
	IPInfoSet      uint16 = 0x0001 // an IP address is set
	IPInfoDHCP     uint16 = 0x0002 // set by DHCP
	IPInfoConflict uint16 = 0x0080 // address conflict detected
)

// Device roles (bits of Device/Role).
const (
	RoleIODevice     uint8 = 0x01
	RoleIOController uint8 = 0x02
	RoleIOMultidev   uint8 = 0x04
	RolePNSupervisor uint8 = 0x08
)

// SignalFlashOnce is the signal value of Control/Signal that makes a
// device flash its LED (the usual 3 Hz for 3 s pattern).
const SignalFlashOnce uint16 = 0x0100

// Block is one block of a DCP frame. In responses Info is the BlockInfo,
// in Set requests the BlockQualifier; Identify filter blocks and Get
// requests have none (HasInfo false).
type Block struct {
	ID      BlockID
	HasInfo bool
	Info    uint16
	Data    []byte
}

// Frame is a decoded DCP frame without the Ethernet header.
type Frame struct {
	FrameID uint16
	Service ServiceID
	Type    ServiceType
	// Xid is the transaction ID a response echoes.
	Xid uint32
	// ResponseDelay is the ResponseDelayFactor of an Identify request
	// (Reserved, zero, otherwise).
	ResponseDelay uint16
	// Blocks are the blocks of the frame. For a Get request each block
	// has only its ID.
	Blocks []Block
}

// IsRequest reports whether the frame is a request.
func (f *Frame) IsRequest() bool { return f.Type == TypeRequest }

// hasInfo reports whether the blocks of a frame with this service and
// type carry the BlockInfo/BlockQualifier field.
func hasInfo(s ServiceID, t ServiceType) bool {
	if t != TypeRequest {
		// Response blocks carry BlockInfo, except Control/Response, which
		// the decoder handles separately.
		return true
	}
	// Set request blocks carry a BlockQualifier; Hello requests carry
	// BlockInfo like Identify responses.
	return s == ServiceSet || s == ServiceHello
}

// Decode decodes the PROFINET payload of a DCP frame, starting with the
// FrameID. Block data aliases b.
func Decode(b []byte) (Frame, error) {
	var f Frame
	if len(b) < 2+HeaderLen {
		return f, fmt.Errorf("dcp: %w: frame of %d octets", profinet.ErrMalformed, len(b))
	}
	f.FrameID = binary.BigEndian.Uint16(b)
	switch f.FrameID {
	case FrameIDHello, FrameIDGetSet, FrameIDIdentifyRequest, FrameIDIdentifyResponse:
	default:
		return f, fmt.Errorf("dcp: %w: FrameID 0x%04X is not DCP", profinet.ErrMalformed, f.FrameID)
	}
	f.Service = ServiceID(b[2])
	f.Type = ServiceType(b[3])
	f.Xid = binary.BigEndian.Uint32(b[4:])
	f.ResponseDelay = binary.BigEndian.Uint16(b[8:])
	n := int(binary.BigEndian.Uint16(b[10:]))
	data := b[12:]
	if n > len(data) {
		return f, fmt.Errorf("dcp: %w: DCPDataLength %d exceeds the %d octets present", profinet.ErrMalformed, n, len(data))
	}
	data = data[:n]
	if f.Service == ServiceGet && f.Type == TypeRequest {
		if len(data)%2 != 0 {
			return f, fmt.Errorf("dcp: %w: Get request of odd length %d", profinet.ErrMalformed, len(data))
		}
		f.Blocks = make([]Block, 0, len(data)/2)
		for i := 0; i < len(data); i += 2 {
			f.Blocks = append(f.Blocks, Block{ID: BlockID(binary.BigEndian.Uint16(data[i:]))})
		}
		return f, nil
	}
	withInfo := hasInfo(f.Service, f.Type)
	for len(data) > 0 {
		if len(data) < 4 {
			return f, fmt.Errorf("dcp: %w: truncated block header", profinet.ErrMalformed)
		}
		blk := Block{ID: BlockID(binary.BigEndian.Uint16(data))}
		l := int(binary.BigEndian.Uint16(data[2:]))
		data = data[4:]
		if l > len(data) {
			return f, fmt.Errorf("dcp: %w: block %s of length %d exceeds the %d octets left", profinet.ErrMalformed, blk.ID, l, len(data))
		}
		body := data[:l]
		// Control/Response blocks have no BlockInfo; an AllSelector
		// filter has no data at all.
		if withInfo && blk.ID != CtrlResponse && !(f.Type == TypeRequest && blk.ID == AllSelector) {
			if l < 2 {
				return f, fmt.Errorf("dcp: %w: block %s too short for its BlockInfo", profinet.ErrMalformed, blk.ID)
			}
			blk.HasInfo = true
			blk.Info = binary.BigEndian.Uint16(body)
			body = body[2:]
		}
		blk.Data = body
		f.Blocks = append(f.Blocks, blk)
		data = data[l:]
		if l%2 == 1 && len(data) > 0 {
			data = data[1:] // padding to an even length
		}
	}
	return f, nil
}

// AppendTo appends the encoded frame (FrameID first) to dst.
func (f *Frame) AppendTo(dst []byte) ([]byte, error) {
	start := len(dst)
	dst = binary.BigEndian.AppendUint16(dst, f.FrameID)
	dst = append(dst, byte(f.Service), byte(f.Type))
	dst = binary.BigEndian.AppendUint32(dst, f.Xid)
	dst = binary.BigEndian.AppendUint16(dst, f.ResponseDelay)
	lenAt := len(dst)
	dst = append(dst, 0, 0)
	if f.Service == ServiceGet && f.Type == TypeRequest {
		for _, b := range f.Blocks {
			dst = binary.BigEndian.AppendUint16(dst, uint16(b.ID))
		}
	} else {
		for _, b := range f.Blocks {
			l := len(b.Data)
			if b.HasInfo {
				l += 2
			}
			if l > 0xFFFF {
				return dst[:start], fmt.Errorf("dcp: %w: block %s of %d octets", profinet.ErrInvalidConfig, b.ID, l)
			}
			dst = binary.BigEndian.AppendUint16(dst, uint16(b.ID))
			dst = binary.BigEndian.AppendUint16(dst, uint16(l))
			if b.HasInfo {
				dst = binary.BigEndian.AppendUint16(dst, b.Info)
			}
			dst = append(dst, b.Data...)
			if l%2 == 1 {
				dst = append(dst, 0)
			}
		}
	}
	n := len(dst) - lenAt - 2
	if n > MaxDataLen {
		return dst[:start], fmt.Errorf("dcp: %w: DCPDataLength %d exceeds %d", profinet.ErrInvalidConfig, n, MaxDataLen)
	}
	binary.BigEndian.PutUint16(dst[lenAt:], uint16(n))
	return dst, nil
}

// Find returns the first block with the given ID.
func (f *Frame) Find(id BlockID) (Block, bool) {
	for _, b := range f.Blocks {
		if b.ID == id {
			return b, true
		}
	}
	return Block{}, false
}

// IPSuite is the content of an IP/Parameter block.
type IPSuite struct {
	Addr, Mask, Gateway netip.Addr
}

// String formats the suite as "addr/mask gw gateway"; unset addresses
// print as 0.0.0.0.
func (s IPSuite) String() string {
	a := func(x netip.Addr) string {
		if !x.IsValid() {
			return "0.0.0.0"
		}
		return x.String()
	}
	return fmt.Sprintf("%s/%s gw %s", a(s.Addr), a(s.Mask), a(s.Gateway))
}

// IsZero reports whether no address is set.
func (s IPSuite) IsZero() bool {
	return (!s.Addr.IsValid() || s.Addr.IsUnspecified()) && (!s.Mask.IsValid() || s.Mask.IsUnspecified())
}

// AppendIPSuite appends the 12 octets of an IP/Parameter block's data.
func AppendIPSuite(dst []byte, s IPSuite) []byte {
	for _, a := range []netip.Addr{s.Addr, s.Mask, s.Gateway} {
		var v [4]byte
		if a.Is4() {
			v = a.As4()
		}
		dst = append(dst, v[:]...)
	}
	return dst
}

// DecodeIPSuite decodes the data of an IP/Parameter or IP/FullSuite block.
func DecodeIPSuite(b []byte) (IPSuite, error) {
	if len(b) < 12 {
		return IPSuite{}, fmt.Errorf("dcp: %w: IP parameter of %d octets", profinet.ErrMalformed, len(b))
	}
	return IPSuite{
		Addr:    netip.AddrFrom4([4]byte(b[0:4])),
		Mask:    netip.AddrFrom4([4]byte(b[4:8])),
		Gateway: netip.AddrFrom4([4]byte(b[8:12])),
	}, nil
}

// ControlResponse is the content of a Control/Response block: the result
// of one block of a Set request.
type ControlResponse struct {
	Block BlockID
	Error BlockError
}

// AppendControlResponse appends a Control/Response block's data.
func AppendControlResponse(dst []byte, r ControlResponse) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(r.Block))
	return append(dst, byte(r.Error))
}

// DecodeControlResponse decodes a Control/Response block's data.
func DecodeControlResponse(b []byte) (ControlResponse, error) {
	if len(b) < 3 {
		return ControlResponse{}, fmt.Errorf("dcp: %w: control response of %d octets", profinet.ErrMalformed, len(b))
	}
	return ControlResponse{Block: BlockID(binary.BigEndian.Uint16(b)), Error: BlockError(b[2])}, nil
}
