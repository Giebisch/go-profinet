// SPDX-License-Identifier: MIT

package pnio

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet/rpc"
)

// RPC interfaces of PROFINET IO (interface version 1).
var (
	// DeviceInterface is served by IO-Devices: Connect, Release, Read,
	// Write, Control, ReadImplicit.
	DeviceInterface = rpc.MustParseUUID("dea00001-6c97-11d1-8271-00a02442df7d")
	// ControllerInterface is served by IO-Controllers: the device's
	// Control (ApplicationReady) call.
	ControllerInterface = rpc.MustParseUUID("dea00002-6c97-11d1-8271-00a02442df7d")
	// SupervisorInterface is served by IO-Supervisors.
	SupervisorInterface = rpc.MustParseUUID("dea00003-6c97-11d1-8271-00a02442df7d")
	// ParameterServerInterface is served by parameter servers.
	ParameterServerInterface = rpc.MustParseUUID("dea00004-6c97-11d1-8271-00a02442df7d")
	// EPMInterface is the DCE endpoint mapper (version 3).
	EPMInterface = rpc.MustParseUUID("e1af8308-5d1f-11c9-91a4-08002b14a0fa")
	// NDRTransferSyntax is the NDR transfer syntax UUID (version 2).
	NDRTransferSyntax = rpc.MustParseUUID("8a885d04-1ceb-11c9-9fe8-08002b104860")
)

// InterfaceVersion is the version of the PNIO interfaces.
const InterfaceVersion = 1

// ObjectUUID returns the PNIO object UUID of a device or controller
// instance: DEA00000-6C97-11D1-8271-<instance><device ID><vendor ID>.
func ObjectUUID(instance, deviceID, vendorID uint16) rpc.UUID {
	u := rpc.MustParseUUID("dea00000-6c97-11d1-8271-000000000000")
	binary.BigEndian.PutUint16(u[10:], instance)
	binary.BigEndian.PutUint16(u[12:], deviceID)
	binary.BigEndian.PutUint16(u[14:], vendorID)
	return u
}

// IsObjectUUID reports whether u has the PNIO object UUID prefix.
func IsObjectUUID(u rpc.UUID) bool {
	p := rpc.MustParseUUID("dea00000-6c97-11d1-8271-000000000000")
	return [10]byte(u[:10]) == [10]byte(p[:10])
}

// OpNum is an operation of the PNIO interfaces.
type OpNum uint16

// Operations.
const (
	OpConnect      OpNum = 0
	OpRelease      OpNum = 1
	OpRead         OpNum = 2
	OpWrite        OpNum = 3
	OpControl      OpNum = 4
	OpReadImplicit OpNum = 5
)

func (o OpNum) String() string {
	switch o {
	case OpConnect:
		return "Connect"
	case OpRelease:
		return "Release"
	case OpRead:
		return "Read"
	case OpWrite:
		return "Write"
	case OpControl:
		return "Control"
	case OpReadImplicit:
		return "ReadImplicit"
	default:
		return fmt.Sprintf("OpNum(%d)", uint16(o))
	}
}

// ARType is the type of an application relationship.
type ARType uint16

// AR types.
const (
	ARTypeIOCARSingle     ARType = 0x0001
	ARTypeIOSAR           ARType = 0x0006 // supervisor (device access) AR
	ARTypeIOCARSingleRTC3 ARType = 0x0010
	ARTypeIOCARSR         ARType = 0x0020
)

func (t ARType) String() string {
	switch t {
	case ARTypeIOCARSingle:
		return "IOCARSingle"
	case ARTypeIOSAR:
		return "IOSAR"
	case ARTypeIOCARSingleRTC3:
		return "IOCARSingle-RTC3"
	case ARTypeIOCARSR:
		return "IOCAR-SR"
	default:
		return fmt.Sprintf("ARType(0x%04X)", uint16(t))
	}
}

// ARProperties bits.
const (
	ARPropStateActive         uint32 = 0x00000001
	ARPropSupervisorTakeover  uint32 = 0x00000008
	ARPropPrmServerCMInit     uint32 = 0x00000010 // parameterization by the CM initiator
	ARPropDeviceAccess        uint32 = 0x00000100
	ARPropCombinedObjectCont  uint32 = 0x20000000
	ARPropStartupAdvanced     uint32 = 0x40000000
	ARPropPullModuleAlarmAllo uint32 = 0x80000000
)

// IOCRType is the type of an IO communication relationship.
type IOCRType uint16

// IOCR types.
const (
	IOCRInput      IOCRType = 1 // the device provides, the controller consumes
	IOCROutput     IOCRType = 2 // the controller provides, the device consumes
	IOCRMCProvider IOCRType = 3
	IOCRMCConsumer IOCRType = 4
)

func (t IOCRType) String() string {
	switch t {
	case IOCRInput:
		return "Input"
	case IOCROutput:
		return "Output"
	case IOCRMCProvider:
		return "MulticastProvider"
	case IOCRMCConsumer:
		return "MulticastConsumer"
	default:
		return fmt.Sprintf("IOCRType(%d)", uint16(t))
	}
}

// RTClass is the real-time class in IOCRProperties bits 0-3.
type RTClass uint8

// RT classes. RTClass1 is the legacy encoding of unsynchronized RT; in
// advanced startup mode devices expect RTClass2 for the same traffic.
const (
	RTClass1      RTClass = 1
	RTClass2      RTClass = 2
	RTClass3      RTClass = 3
	RTClassUDP    RTClass = 4
	RTClassStream RTClass = 5
)

// LT is the Ethernet type carried in IOCR and AlarmCR blocks.
const LT = 0x8892

// FrameID ranges of cyclic RT frames.
const (
	FrameIDRTC1Min   uint16 = 0xC000
	FrameIDRTC1Max   uint16 = 0xF7FF
	FrameIDRTC1MCMin uint16 = 0xF800
	FrameIDRTC1MCMax uint16 = 0xFBFF
	FrameIDRTC2Min   uint16 = 0x8000
	FrameIDRTC2Max   uint16 = 0xBBFF
	FrameIDRTC2MCMin uint16 = 0xBC00
	FrameIDRTC2MCMax uint16 = 0xBFFF
	FrameIDAlarmHigh uint16 = 0xFC01
	FrameIDAlarmLow  uint16 = 0xFE01
	// FrameIDAssign in an output IOCR request asks the device to assign
	// the FrameID.
	FrameIDAssign uint16 = 0xFFFF
)

// FrameIDRange returns the unicast FrameID range of an RT class.
func FrameIDRange(c RTClass) (lo, hi uint16, ok bool) {
	switch c {
	case RTClass1, RTClassUDP:
		return FrameIDRTC1Min, FrameIDRTC1Max, true
	case RTClass2:
		return FrameIDRTC2Min, FrameIDRTC2Max, true
	case RTClass3:
		return 0x0100, 0x7FFF, true
	case RTClassStream:
		return 0, 0, false
	default:
		return 0, 0, false
	}
}

// ControlCommand bits of IODControl and IOXControl blocks.
const (
	CmdPrmEnd            uint16 = 0x0001
	CmdApplicationReady  uint16 = 0x0002
	CmdRelease           uint16 = 0x0004
	CmdDone              uint16 = 0x0008
	CmdReadyForCompanion uint16 = 0x0010
	CmdReadyForRTC3      uint16 = 0x0020
	CmdPrmBegin          uint16 = 0x0040
)

// SubmoduleType is the type in SubmoduleProperties bits 0-1.
type SubmoduleType uint8

// Submodule types.
const (
	SubmoduleNoIO        SubmoduleType = 0
	SubmoduleInput       SubmoduleType = 1
	SubmoduleOutput      SubmoduleType = 2
	SubmoduleInputOutput SubmoduleType = 3
)

func (t SubmoduleType) String() string {
	switch t {
	case SubmoduleNoIO:
		return "NO_IO"
	case SubmoduleInput:
		return "INPUT"
	case SubmoduleOutput:
		return "OUTPUT"
	case SubmoduleInputOutput:
		return "INPUT_OUTPUT"
	default:
		return fmt.Sprintf("SubmoduleType(%d)", uint8(t))
	}
}

// HasInput reports whether the type produces input data.
func (t SubmoduleType) HasInput() bool { return t == SubmoduleInput || t == SubmoduleInputOutput }

// HasOutput reports whether the type consumes output data.
func (t SubmoduleType) HasOutput() bool { return t == SubmoduleOutput || t == SubmoduleInputOutput }

// DataDescription types.
const (
	DataInput  uint16 = 1
	DataOutput uint16 = 2
)

// IOxS values: the provider and consumer status octets.
const (
	IOxSGood uint8 = 0x80
	IOxSBad  uint8 = 0x00
)

// ModuleState values of the ModuleDiffBlock.
const (
	ModuleStateNoModule   uint16 = 0
	ModuleStateWrong      uint16 = 1
	ModuleStateProper     uint16 = 2
	ModuleStateSubstitute uint16 = 3
)

// SubmoduleState fields (FormatIndicator 1).
const (
	SubmStateFormat           uint16 = 0x8000
	SubmStateQualifiedInfo    uint16 = 0x0008
	SubmStateMaintRequired    uint16 = 0x0010
	SubmStateMaintDemanded    uint16 = 0x0020
	SubmStateDiagInfo         uint16 = 0x0040
	SubmStateARInfoOwn        uint16 = 0 << 7
	SubmStateARInfoAppReady   uint16 = 1 << 7
	SubmStateARInfoSuperLock  uint16 = 2 << 7
	SubmStateARInfoCtrlLock   uint16 = 3 << 7
	SubmStateARInfoSupvLock   uint16 = 4 << 7
	SubmStateIdentOK          uint16 = 0 << 11
	SubmStateIdentSubstitute  uint16 = 1 << 11
	SubmStateIdentWrong       uint16 = 2 << 11
	SubmStateIdentNoSubmodule uint16 = 3 << 11
)

// Index is a record index.
type Index uint16

// Record indices.
const (
	IndexExpectedIdentSubslot Index = 0x8000
	IndexRealIdentSubslot     Index = 0x8001
	IndexDiagChannelSubslot   Index = 0x800A
	IndexDiagAllSubslot       Index = 0x800B
	IndexDiagDMQSSubslot      Index = 0x800C
	IndexRecordInputData      Index = 0x8028
	IndexRecordOutputData     Index = 0x8029
	IndexIM0                  Index = 0xAFF0
	IndexIM1                  Index = 0xAFF1
	IndexIM2                  Index = 0xAFF2
	IndexIM3                  Index = 0xAFF3
	IndexIM4                  Index = 0xAFF4
	IndexExpectedIdentSlot    Index = 0xC000
	IndexRealIdentSlot        Index = 0xC001
	IndexDiagChannelSlot      Index = 0xC00A
	IndexDiagAllSlot          Index = 0xC00B
	IndexDiagDMQSSlot         Index = 0xC00C
	IndexExpectedIdentAR      Index = 0xE000
	IndexRealIdentAR          Index = 0xE001
	IndexModuleDiffAR         Index = 0xE002
	IndexDiagChannelAR        Index = 0xE00A
	IndexDiagAllAR            Index = 0xE00B
	IndexDiagDMQSAR           Index = 0xE00C
	IndexWriteMultiple        Index = 0xE040
	IndexRealIdentAPI         Index = 0xF000
	IndexDiagChannelAPI       Index = 0xF00A
	IndexDiagAllAPI           Index = 0xF00B
	IndexDiagDMQSAPI          Index = 0xF00C
	IndexARDataAPI            Index = 0xF020
	IndexDiagDMQSDevice       Index = 0xF80C
	IndexARDataDevice         Index = 0xF820
	IndexAPIData              Index = 0xF821
	IndexLogBook              Index = 0xF830
	IndexIM0Filter            Index = 0xF840
	IndexPDRealData           Index = 0xF841
	IndexPDExpectedData       Index = 0xF842
)

// IsUserIndex reports whether the index is in the user-specific range
// 0x0000..0x7FFF.
func (i Index) IsUserIndex() bool { return i <= 0x7FFF }

func (i Index) String() string { return fmt.Sprintf("0x%04X", uint16(i)) }

// AlarmType is the type of an alarm.
type AlarmType uint16

// Alarm types.
const (
	AlarmDiagnosis            AlarmType = 0x0001
	AlarmProcess              AlarmType = 0x0002
	AlarmPull                 AlarmType = 0x0003
	AlarmPlug                 AlarmType = 0x0004
	AlarmStatus               AlarmType = 0x0005
	AlarmUpdate               AlarmType = 0x0006
	AlarmMediaRedundancy      AlarmType = 0x0007
	AlarmControlledBySuper    AlarmType = 0x0008
	AlarmReleased             AlarmType = 0x0009
	AlarmPlugWrongSubmodule   AlarmType = 0x000A
	AlarmReturnOfSubmodule    AlarmType = 0x000B
	AlarmDiagnosisDisappears  AlarmType = 0x000C
	AlarmPortDataChange       AlarmType = 0x000E
	AlarmNetworkComponentProb AlarmType = 0x0011
	AlarmPullModule           AlarmType = 0x001F
)

func (t AlarmType) String() string {
	switch t {
	case AlarmDiagnosis:
		return "Diagnosis"
	case AlarmProcess:
		return "Process"
	case AlarmPull:
		return "Pull"
	case AlarmPlug:
		return "Plug"
	case AlarmStatus:
		return "Status"
	case AlarmUpdate:
		return "Update"
	case AlarmMediaRedundancy:
		return "MediaRedundancy"
	case AlarmControlledBySuper:
		return "ControlledBySupervisor"
	case AlarmReleased:
		return "Released"
	case AlarmPlugWrongSubmodule:
		return "PlugWrongSubmodule"
	case AlarmReturnOfSubmodule:
		return "ReturnOfSubmodule"
	case AlarmDiagnosisDisappears:
		return "DiagnosisDisappears"
	case AlarmPortDataChange:
		return "PortDataChangeNotification"
	case AlarmNetworkComponentProb:
		return "NetworkComponentProblem"
	case AlarmPullModule:
		return "PullModule"
	default:
		return fmt.Sprintf("AlarmType(0x%04X)", uint16(t))
	}
}
