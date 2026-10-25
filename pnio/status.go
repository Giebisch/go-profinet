// SPDX-License-Identifier: MIT

package pnio

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
)

// Status is a PNIOStatus: ErrorCode, ErrorDecode, ErrorCode1 and
// ErrorCode2. The zero value is "no error".
type Status struct {
	Code   uint8 // ErrorCode: the service that failed
	Decode uint8 // ErrorDecode: how to read Code1 and Code2
	Code1  uint8
	Code2  uint8
}

// ErrorCode values.
const (
	CodeOK         uint8 = 0x00
	CodePNIO       uint8 = 0x81
	CodeRTA        uint8 = 0xCF
	CodeAlarmAck   uint8 = 0xDA
	CodeConnect    uint8 = 0xDB
	CodeRelease    uint8 = 0xDC
	CodeControl    uint8 = 0xDD
	CodeRead       uint8 = 0xDE
	CodeWrite      uint8 = 0xDF
	DecodePNIORW   uint8 = 0x80 // Read and Write: Code1 is class and code
	DecodePNIO     uint8 = 0x81 // Code1 names the faulty block or protocol machine
	DecodeVendor   uint8 = 0x82 // manufacturer specific
	Code1RTAAbort  uint8 = 0xFD // with DecodePNIO: Code2 is an abort reason
	Code1CMDEV     uint8 = 0x3D
	Code1CMRPC     uint8 = 0x40
	Code1AlarmAck  uint8 = 0x3C
	Code1RWRecord  uint8 = 0x08 // faulty Read/Write record header
	Code1PrmEnd    uint8 = 0x14
	Code1PrmEndPlg uint8 = 0x15
	Code1AppReady  uint8 = 0x16
	Code1AppRdyPlg uint8 = 0x17
	Code1PrmBegin  uint8 = 0x18
	Code1Release   uint8 = 0x28
	Code1ARBlock   uint8 = 0x01
	Code1IOCRBlock uint8 = 0x02
	Code1ExpSubm   uint8 = 0x03
	Code1AlarmCR   uint8 = 0x04
	Code1ARRPC     uint8 = 0x07
)

// ErrorCode1 values of ErrorDecode PNIORW (Read and Write).
const (
	RWReadError         uint8 = 0xA0
	RWWriteError        uint8 = 0xA1
	RWModuleFailure     uint8 = 0xA2
	RWBusy              uint8 = 0xA7
	RWVersionConflict   uint8 = 0xA8
	RWFeatureNotSupport uint8 = 0xA9
	RWInvalidIndex      uint8 = 0xB0
	RWWriteLength       uint8 = 0xB1
	RWInvalidSlot       uint8 = 0xB2
	RWTypeConflict      uint8 = 0xB3
	RWInvalidArea       uint8 = 0xB4
	RWStateConflict     uint8 = 0xB5
	RWAccessDenied      uint8 = 0xB6
	RWInvalidRange      uint8 = 0xB7
	RWInvalidParameter  uint8 = 0xB8
	RWInvalidType       uint8 = 0xB9
	RWBackup            uint8 = 0xBA
	RWReadConstraint    uint8 = 0xC0
	RWWriteConstraint   uint8 = 0xC1
	RWResourceBusy      uint8 = 0xC2
	RWResourceUnavail   uint8 = 0xC3
)

// ErrorCode2 values of ErrorCode1 CMRPC.
const (
	CMRPCArgsLength    uint8 = 0x00
	CMRPCUnknownBlocks uint8 = 0x01
	CMRPCIOCRMissing   uint8 = 0x02
	CMRPCAlarmCRCount  uint8 = 0x03
	CMRPCOutOfARs      uint8 = 0x04
	CMRPCARUUIDUnknown uint8 = 0x05
	CMRPCStateConflict uint8 = 0x06
	CMRPCOutOfMemory   uint8 = 0x08
)

// AbortReason is the ErrorCode2 of an RTA abort (PNIOStatus CF 81 FD xx).
type AbortReason uint8

// Abort reasons.
const (
	AbortSequence         AbortReason = 0x01
	AbortInstanceClosed   AbortReason = 0x02
	AbortOutOfMemory      AbortReason = 0x03
	AbortAddProvCons      AbortReason = 0x04
	AbortConsumerDHT      AbortReason = 0x05
	AbortCMITimeout       AbortReason = 0x06
	AbortAlarmOpen        AbortReason = 0x07
	AbortAlarmSend        AbortReason = 0x08
	AbortAlarmAckSend     AbortReason = 0x09
	AbortAlarmDataTooLong AbortReason = 0x0A
	AbortAlarmInd         AbortReason = 0x0B
	AbortRPCClientCall    AbortReason = 0x0C
	AbortUser             AbortReason = 0x0D
	AbortRerun            AbortReason = 0x0E
	AbortReleaseInd       AbortReason = 0x0F
	AbortDeactivated      AbortReason = 0x10
	AbortRemoved          AbortReason = 0x11
	AbortProtocol         AbortReason = 0x12
	AbortNameResolution   AbortReason = 0x13
	AbortRPCBind          AbortReason = 0x14
	AbortRPCConnect       AbortReason = 0x15
	AbortRPCRead          AbortReason = 0x16
	AbortRPCWrite         AbortReason = 0x17
	AbortRPCControl       AbortReason = 0x18
	AbortPullPlug         AbortReason = 0x19
	AbortAPRemoved        AbortReason = 0x1A
	AbortLinkDown         AbortReason = 0x1B
	AbortMCRegister       AbortReason = 0x1C
	AbortNotSynchronized  AbortReason = 0x1D
	AbortWrongTopology    AbortReason = 0x1E
	AbortDCPNameChanged   AbortReason = 0x1F
	AbortDCPReset         AbortReason = 0x20
	AbortPDEVCheck        AbortReason = 0x24
)

func (r AbortReason) String() string {
	switch r {
	case AbortSequence:
		return "sequence number error"
	case AbortInstanceClosed:
		return "instance closed"
	case AbortOutOfMemory:
		return "out of memory"
	case AbortAddProvCons:
		return "add provider or consumer failed"
	case AbortConsumerDHT:
		return "consumer data hold timer expired"
	case AbortCMITimeout:
		return "CM initiator activity timeout"
	case AbortAlarmOpen:
		return "alarm open failed"
	case AbortAlarmSend:
		return "alarm send failed"
	case AbortAlarmAckSend:
		return "alarm ack send failed"
	case AbortAlarmDataTooLong:
		return "alarm data too long"
	case AbortAlarmInd:
		return "alarm indication error"
	case AbortRPCClientCall:
		return "RPC client call failed"
	case AbortUser:
		return "abort requested"
	case AbortRerun:
		return "re-run aborts existing AR"
	case AbortReleaseInd:
		return "release indication received"
	case AbortDeactivated:
		return "device deactivated"
	case AbortRemoved:
		return "removed"
	case AbortProtocol:
		return "protocol violation"
	case AbortNameResolution:
		return "name resolution error"
	case AbortRPCBind:
		return "RPC bind error"
	case AbortRPCConnect:
		return "RPC connect error"
	case AbortRPCRead:
		return "RPC read error"
	case AbortRPCWrite:
		return "RPC write error"
	case AbortRPCControl:
		return "RPC control error"
	case AbortPullPlug:
		return "forbidden pull or plug"
	case AbortAPRemoved:
		return "AP removed"
	case AbortLinkDown:
		return "link down"
	case AbortMCRegister:
		return "multicast register failed"
	case AbortNotSynchronized:
		return "not synchronized"
	case AbortWrongTopology:
		return "wrong topology"
	case AbortDCPNameChanged:
		return "DCP station name changed"
	case AbortDCPReset:
		return "DCP reset to factory"
	case AbortPDEVCheck:
		return "PDEV check failed"
	default:
		return fmt.Sprintf("abort reason 0x%02X", uint8(r))
	}
}

// AbortStatus returns the PNIOStatus of an RTA abort.
func AbortStatus(r AbortReason) Status {
	return Status{Code: CodeRTA, Decode: DecodePNIO, Code1: Code1RTAAbort, Code2: uint8(r)}
}

// RWStatus returns the PNIOStatus of a failed Read or Write.
func RWStatus(write bool, code1, code2 uint8) Status {
	c := CodeRead
	if write {
		c = CodeWrite
	}
	return Status{Code: c, Decode: DecodePNIORW, Code1: code1, Code2: code2}
}

// ServiceStatus returns a PNIOStatus with ErrorDecode PNIO.
func ServiceStatus(code, code1, code2 uint8) Status {
	return Status{Code: code, Decode: DecodePNIO, Code1: code1, Code2: code2}
}

// blockError is the status of a block that failed to decode; field is
// the index of the faulty field (0 type, 1 length, 2 version high, 3
// version low, then the block's fields in order).
func blockError(t BlockType, field uint8) Status {
	c1 := Code1CMRPC
	code := CodeConnect
	switch t {
	case BlockARReq:
		c1 = Code1ARBlock
	case BlockIOCRReq:
		c1 = Code1IOCRBlock
	case BlockExpectedSubmReq:
		c1 = Code1ExpSubm
	case BlockAlarmCRReq:
		c1 = Code1AlarmCR
	case BlockARRPCReq:
		c1 = Code1ARRPC
	case BlockPrmEndReq:
		c1, code = Code1PrmEnd, CodeControl
	case BlockPrmEndPlugReq:
		c1, code = Code1PrmEndPlg, CodeControl
	case BlockPrmBeginReq:
		c1, code = Code1PrmBegin, CodeControl
	case BlockAppReadyReq:
		c1, code = Code1AppReady, CodeControl
	case BlockAppReadyPlugReq:
		c1, code = Code1AppRdyPlg, CodeControl
	case BlockReleaseReq:
		c1, code = Code1Release, CodeRelease
	case BlockReadReq, BlockWriteReq:
		c1 = Code1RWRecord
		code = CodeRead
		if t == BlockWriteReq {
			code = CodeWrite
		}
	default:
		field = CMRPCUnknownBlocks
	}
	return Status{Code: code, Decode: DecodePNIO, Code1: c1, Code2: field}
}

// OK reports whether s is "no error".
func (s Status) OK() bool { return s == Status{} }

// Uint32 returns the four octets as a big-endian integer.
func (s Status) Uint32() uint32 {
	return uint32(s.Code)<<24 | uint32(s.Decode)<<16 | uint32(s.Code1)<<8 | uint32(s.Code2)
}

// StatusFromUint32 is the inverse of Uint32.
func StatusFromUint32(v uint32) Status {
	return Status{Code: uint8(v >> 24), Decode: uint8(v >> 16), Code1: uint8(v >> 8), Code2: uint8(v)}
}

// Append appends the four octets.
func (s Status) Append(b []byte) []byte { return append(b, s.Code, s.Decode, s.Code1, s.Code2) }

// String formats the status as "DE 80 B0 00 (read: access: invalid index)".
func (s Status) String() string {
	if s.OK() {
		return "OK"
	}
	return fmt.Sprintf("%02X %02X %02X %02X (%s)", s.Code, s.Decode, s.Code1, s.Code2, s.describe())
}

func (s Status) describe() string {
	svc := map[uint8]string{
		CodePNIO: "PNIO", CodeRTA: "RTA", CodeAlarmAck: "alarm ack", CodeConnect: "connect",
		CodeRelease: "release", CodeControl: "control", CodeRead: "read", CodeWrite: "write",
	}[s.Code]
	if svc == "" {
		svc = fmt.Sprintf("service 0x%02X", s.Code)
	}
	switch s.Decode {
	case DecodePNIORW:
		return svc + ": " + rwName(s.Code1)
	case DecodePNIO:
		if s.Code1 == Code1RTAAbort {
			return svc + ": abort: " + AbortReason(s.Code2).String()
		}
		return fmt.Sprintf("%s: %s, code %d", svc, code1Name(s.Code1), s.Code2)
	case DecodeVendor:
		return svc + ": manufacturer specific"
	default:
		return svc
	}
}

func rwName(c uint8) string {
	switch c {
	case RWReadError:
		return "application: read error"
	case RWWriteError:
		return "application: write error"
	case RWModuleFailure:
		return "application: module failure"
	case RWBusy:
		return "application: busy"
	case RWVersionConflict:
		return "application: version conflict"
	case RWFeatureNotSupport:
		return "application: feature not supported"
	case RWInvalidIndex:
		return "access: invalid index"
	case RWWriteLength:
		return "access: write length error"
	case RWInvalidSlot:
		return "access: invalid slot/subslot"
	case RWTypeConflict:
		return "access: type conflict"
	case RWInvalidArea:
		return "access: invalid area/API"
	case RWStateConflict:
		return "access: state conflict"
	case RWAccessDenied:
		return "access: access denied"
	case RWInvalidRange:
		return "access: invalid range"
	case RWInvalidParameter:
		return "access: invalid parameter"
	case RWInvalidType:
		return "access: invalid type"
	case RWBackup:
		return "access: backup"
	case RWReadConstraint:
		return "resource: read constraint conflict"
	case RWWriteConstraint:
		return "resource: write constraint conflict"
	case RWResourceBusy:
		return "resource: busy"
	case RWResourceUnavail:
		return "resource: unavailable"
	default:
		return fmt.Sprintf("code 0x%02X", c)
	}
}

func code1Name(c uint8) string {
	switch c {
	case Code1ARBlock:
		return "faulty ARBlockReq"
	case Code1IOCRBlock:
		return "faulty IOCRBlockReq"
	case Code1ExpSubm:
		return "faulty ExpectedSubmoduleBlockReq"
	case Code1AlarmCR:
		return "faulty AlarmCRBlockReq"
	case Code1ARRPC:
		return "faulty ARRPCBlockReq"
	case Code1RWRecord:
		return "faulty record"
	case Code1PrmEnd:
		return "faulty IODControlReq"
	case Code1PrmEndPlg:
		return "faulty IODControlReq (plug)"
	case Code1PrmBegin:
		return "faulty IODControlReq (PrmBegin)"
	case Code1AppReady:
		return "faulty IOXControlReq"
	case Code1AppRdyPlg:
		return "faulty IOXControlReq (plug)"
	case Code1Release:
		return "faulty ReleaseBlock"
	case Code1AlarmAck:
		return "ALARM_ACK"
	case Code1CMDEV:
		return "CMDEV"
	case Code1CMRPC:
		return "CMRPC"
	default:
		return fmt.Sprintf("0x%02X", c)
	}
}

// StatusError reports a PNIOStatus other than OK, returned by a peer or
// produced by a decoder. It matches profinet.ErrRejected and, by class,
// ErrNotFound (invalid index, slot or API), ErrUnsupported (feature not
// supported), ErrInvalidState (state conflicts), ErrResourceExhausted (out
// of AR resources, busy) and ErrAborted (RTA aborts).
type StatusError struct {
	// Op names the operation, for example "read 0xAFF0".
	Op     string
	Status Status
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("pnio: %s: %s", e.Op, e.Status)
}

// Is implements the matching described on the type.
func (e *StatusError) Is(target error) bool {
	s := e.Status
	switch target {
	case profinet.ErrRejected:
		return true
	case profinet.ErrMalformed:
		return s.Decode == DecodePNIO && s.Code1 <= Code1Release && s.Code1 != 0
	case profinet.ErrNotFound:
		return s.Decode == DecodePNIORW && (s.Code1 == RWInvalidIndex || s.Code1 == RWInvalidSlot || s.Code1 == RWInvalidArea) ||
			s.Decode == DecodePNIO && s.Code1 == Code1CMRPC && s.Code2 == CMRPCARUUIDUnknown
	case profinet.ErrUnsupported:
		return s.Decode == DecodePNIORW && s.Code1 == RWFeatureNotSupport ||
			s.Decode == DecodePNIO && s.Code1 == Code1CMRPC && s.Code2 == CMRPCUnknownBlocks
	case profinet.ErrInvalidState:
		return s.Decode == DecodePNIORW && s.Code1 == RWStateConflict ||
			s.Decode == DecodePNIO && (s.Code1 == Code1CMDEV && s.Code2 == 0 || s.Code1 == Code1CMRPC && s.Code2 == CMRPCStateConflict)
	case profinet.ErrResourceExhausted:
		return s.Decode == DecodePNIORW && (s.Code1 == RWBusy || s.Code1 == RWResourceBusy || s.Code1 == RWResourceUnavail) ||
			s.Decode == DecodePNIO && s.Code1 == Code1CMRPC && (s.Code2 == CMRPCOutOfARs || s.Code2 == CMRPCOutOfMemory)
	case profinet.ErrAborted:
		return s.Code == CodeRTA || s.Code1 == Code1RTAAbort
	}
	return false
}

// decodeStatus reads a status from four octets.
func decodeStatus(b []byte) Status {
	return StatusFromUint32(binary.BigEndian.Uint32(b))
}
