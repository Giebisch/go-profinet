// SPDX-License-Identifier: MIT

package pnio

import (
	"testing"

	"github.com/otfabric/go-profinet/rpc"
)

func TestObjectUUID(t *testing.T) {
	u := ObjectUUID(1, 0x0203, 0x002A)
	if got := u.String(); got != "dea00000-6c97-11d1-8271-00010203002a" {
		t.Fatalf("ObjectUUID = %s", got)
	}
	if !IsObjectUUID(u) {
		t.Fatal("IsObjectUUID(ObjectUUID)")
	}
	// Only the first ten octets are checked.
	if !IsObjectUUID(rpc.MustParseUUID("dea00000-6c97-11d1-8271-ffffffffffff")) {
		t.Fatal("suffix checked")
	}
	for _, s := range []string{
		"dea00001-6c97-11d1-8271-00a02442df7d", // the device interface
		"dea00000-6c97-11d1-8272-000100020003",
		"00000000-0000-0000-0000-000000000000",
	} {
		if IsObjectUUID(rpc.MustParseUUID(s)) {
			t.Errorf("IsObjectUUID(%s)", s)
		}
	}
	if DeviceInterface.String() != "dea00001-6c97-11d1-8271-00a02442df7d" ||
		ControllerInterface.String() != "dea00002-6c97-11d1-8271-00a02442df7d" ||
		SupervisorInterface.String() != "dea00003-6c97-11d1-8271-00a02442df7d" ||
		ParameterServerInterface.String() != "dea00004-6c97-11d1-8271-00a02442df7d" ||
		EPMInterface.String() != "e1af8308-5d1f-11c9-91a4-08002b14a0fa" ||
		NDRTransferSyntax.String() != "8a885d04-1ceb-11c9-9fe8-08002b104860" {
		t.Fatal("interface UUIDs")
	}
}

func TestFrameIDRange(t *testing.T) {
	for _, c := range []struct {
		c      RTClass
		lo, hi uint16
		ok     bool
	}{
		{RTClass1, 0xC000, 0xF7FF, true},
		{RTClassUDP, 0xC000, 0xF7FF, true},
		{RTClass2, 0x8000, 0xBBFF, true},
		{RTClass3, 0x0100, 0x7FFF, true},
		{RTClassStream, 0, 0, false},
		{0, 0, 0, false},
		{9, 0, 0, false},
	} {
		lo, hi, ok := FrameIDRange(c.c)
		if lo != c.lo || hi != c.hi || ok != c.ok {
			t.Errorf("FrameIDRange(%d) = %04X %04X %v", c.c, lo, hi, ok)
		}
	}
}

func TestIDStrings(t *testing.T) {
	for _, c := range []struct {
		got, want string
	}{
		{OpConnect.String(), "Connect"},
		{OpRelease.String(), "Release"},
		{OpRead.String(), "Read"},
		{OpWrite.String(), "Write"},
		{OpControl.String(), "Control"},
		{OpReadImplicit.String(), "ReadImplicit"},
		{OpNum(9).String(), "OpNum(9)"},

		{ARTypeIOCARSingle.String(), "IOCARSingle"},
		{ARTypeIOSAR.String(), "IOSAR"},
		{ARTypeIOCARSingleRTC3.String(), "IOCARSingle-RTC3"},
		{ARTypeIOCARSR.String(), "IOCAR-SR"},
		{ARType(0x0002).String(), "ARType(0x0002)"},

		{IOCRInput.String(), "Input"},
		{IOCROutput.String(), "Output"},
		{IOCRMCProvider.String(), "MulticastProvider"},
		{IOCRMCConsumer.String(), "MulticastConsumer"},
		{IOCRType(0).String(), "IOCRType(0)"},

		{SubmoduleNoIO.String(), "NO_IO"},
		{SubmoduleInput.String(), "INPUT"},
		{SubmoduleOutput.String(), "OUTPUT"},
		{SubmoduleInputOutput.String(), "INPUT_OUTPUT"},
		{SubmoduleType(4).String(), "SubmoduleType(4)"},

		{IndexIM0.String(), "0xAFF0"},
		{Index(0x0001).String(), "0x0001"},

		{AlarmDiagnosis.String(), "Diagnosis"},
		{AlarmProcess.String(), "Process"},
		{AlarmPull.String(), "Pull"},
		{AlarmPlug.String(), "Plug"},
		{AlarmStatus.String(), "Status"},
		{AlarmUpdate.String(), "Update"},
		{AlarmMediaRedundancy.String(), "MediaRedundancy"},
		{AlarmControlledBySuper.String(), "ControlledBySupervisor"},
		{AlarmReleased.String(), "Released"},
		{AlarmPlugWrongSubmodule.String(), "PlugWrongSubmodule"},
		{AlarmReturnOfSubmodule.String(), "ReturnOfSubmodule"},
		{AlarmDiagnosisDisappears.String(), "DiagnosisDisappears"},
		{AlarmPortDataChange.String(), "PortDataChangeNotification"},
		{AlarmNetworkComponentProb.String(), "NetworkComponentProblem"},
		{AlarmPullModule.String(), "PullModule"},
		{AlarmType(0x0080).String(), "AlarmType(0x0080)"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestSubmoduleTypeIO(t *testing.T) {
	for _, c := range []struct {
		t       SubmoduleType
		in, out bool
	}{
		{SubmoduleNoIO, false, false},
		{SubmoduleInput, true, false},
		{SubmoduleOutput, false, true},
		{SubmoduleInputOutput, true, true},
	} {
		if c.t.HasInput() != c.in || c.t.HasOutput() != c.out {
			t.Errorf("%s: HasInput %v HasOutput %v", c.t, c.t.HasInput(), c.t.HasOutput())
		}
	}
}

func TestIndexIsUserIndex(t *testing.T) {
	for i, want := range map[Index]bool{0: true, 0x7FFF: true, 0x8000: false, IndexIM0: false, 0xFFFF: false} {
		if i.IsUserIndex() != want {
			t.Errorf("Index(0x%04X).IsUserIndex() = %v", uint16(i), !want)
		}
	}
}
