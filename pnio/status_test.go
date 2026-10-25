// SPDX-License-Identifier: MIT

package pnio

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
)

func TestStatusBasics(t *testing.T) {
	var zero Status
	if !zero.OK() || zero.String() != "OK" || zero.Uint32() != 0 {
		t.Fatalf("zero status %s", zero)
	}
	s := Status{0xDE, 0x80, 0xB0, 0x01}
	if s.OK() {
		t.Fatal("non-zero status is OK")
	}
	if s.Uint32() != 0xDE80B001 {
		t.Fatalf("Uint32 %08X", s.Uint32())
	}
	if StatusFromUint32(0xDE80B001) != s {
		t.Fatal("StatusFromUint32")
	}
	for _, v := range []uint32{0, 1, 0xCF81FD05, 0xFFFFFFFF, 0x80000000} {
		if got := StatusFromUint32(v).Uint32(); got != v {
			t.Fatalf("round trip %08X -> %08X", v, got)
		}
	}
	checkGolden(t, "Status", s.Append(hx("ff")), hx("ff de80b001"))
	if decodeStatus(hx("de80b001")) != s {
		t.Fatal("decodeStatus")
	}
}

func TestStatusConstructors(t *testing.T) {
	if s := AbortStatus(AbortConsumerDHT); s != (Status{0xCF, 0x81, 0xFD, 0x05}) {
		t.Fatalf("AbortStatus %s", s)
	}
	if s := RWStatus(false, RWInvalidSlot, 3); s != (Status{0xDE, 0x80, 0xB2, 3}) {
		t.Fatalf("RWStatus read %s", s)
	}
	if s := RWStatus(true, RWAccessDenied, 0); s != (Status{0xDF, 0x80, 0xB6, 0}) {
		t.Fatalf("RWStatus write %s", s)
	}
	if s := ServiceStatus(CodeConnect, Code1CMRPC, CMRPCOutOfARs); s != (Status{0xDB, 0x81, 0x40, 0x04}) {
		t.Fatalf("ServiceStatus %s", s)
	}
}

func TestStatusString(t *testing.T) {
	for _, c := range []struct {
		s    Status
		want string
	}{
		{RWStatus(false, RWInvalidIndex, 0), "DE 80 B0 00 (read: access: invalid index)"},
		{RWStatus(true, RWWriteLength, 2), "DF 80 B1 02 (write: access: write length error)"},
		{AbortStatus(AbortUser), "CF 81 FD 0D (RTA: abort: abort requested)"},
		{AbortStatus(0x77), "CF 81 FD 77 (RTA: abort: abort reason 0x77)"},
		{ServiceStatus(CodeConnect, Code1ARBlock, 12), "DB 81 01 0C (connect: faulty ARBlockReq, code 12)"},
		{ServiceStatus(CodeConnect, 0x99, 1), "DB 81 99 01 (connect: 0x99, code 1)"},
		{Status{CodeRelease, DecodeVendor, 1, 2}, "DC 82 01 02 (release: manufacturer specific)"},
		{Status{CodeControl, 0x00, 1, 2}, "DD 00 01 02 (control)"},
		{Status{0x42, 0x55, 1, 2}, "42 55 01 02 (service 0x42)"},
		{Status{CodePNIO, DecodePNIO, Code1CMRPC, 5}, "81 81 40 05 (PNIO: CMRPC, code 5)"},
		{Status{CodeAlarmAck, DecodePNIO, Code1AlarmAck, 0}, "DA 81 3C 00 (alarm ack: ALARM_ACK, code 0)"},
		{Status{CodeRead, DecodePNIORW, 0xEE, 0}, "DE 80 EE 00 (read: code 0xEE)"},
	} {
		if got := c.s.String(); got != c.want {
			t.Errorf("%08X: %q, want %q", c.s.Uint32(), got, c.want)
		}
	}
}

func TestRWNames(t *testing.T) {
	for c, want := range map[uint8]string{
		RWReadError:         "application: read error",
		RWWriteError:        "application: write error",
		RWModuleFailure:     "application: module failure",
		RWBusy:              "application: busy",
		RWVersionConflict:   "application: version conflict",
		RWFeatureNotSupport: "application: feature not supported",
		RWInvalidIndex:      "access: invalid index",
		RWWriteLength:       "access: write length error",
		RWInvalidSlot:       "access: invalid slot/subslot",
		RWTypeConflict:      "access: type conflict",
		RWInvalidArea:       "access: invalid area/API",
		RWStateConflict:     "access: state conflict",
		RWAccessDenied:      "access: access denied",
		RWInvalidRange:      "access: invalid range",
		RWInvalidParameter:  "access: invalid parameter",
		RWInvalidType:       "access: invalid type",
		RWBackup:            "access: backup",
		RWReadConstraint:    "resource: read constraint conflict",
		RWWriteConstraint:   "resource: write constraint conflict",
		RWResourceBusy:      "resource: busy",
		RWResourceUnavail:   "resource: unavailable",
		0x00:                "code 0x00",
	} {
		if got := rwName(c); got != want {
			t.Errorf("rwName(0x%02X) = %q, want %q", c, got, want)
		}
		// The description of a read status carries the name.
		if d := RWStatus(false, c, 0).describe(); d != "read: "+want {
			t.Errorf("describe 0x%02X = %q", c, d)
		}
	}
}

func TestCode1Names(t *testing.T) {
	for c, want := range map[uint8]string{
		Code1ARBlock:   "faulty ARBlockReq",
		Code1IOCRBlock: "faulty IOCRBlockReq",
		Code1ExpSubm:   "faulty ExpectedSubmoduleBlockReq",
		Code1AlarmCR:   "faulty AlarmCRBlockReq",
		Code1ARRPC:     "faulty ARRPCBlockReq",
		Code1RWRecord:  "faulty record",
		Code1PrmEnd:    "faulty IODControlReq",
		Code1AppReady:  "faulty IOXControlReq",
		Code1Release:   "faulty ReleaseBlock",
		Code1AlarmAck:  "ALARM_ACK",
		Code1CMDEV:     "CMDEV",
		Code1CMRPC:     "CMRPC",
		0x05:           "0x05",
	} {
		if got := code1Name(c); got != want {
			t.Errorf("code1Name(0x%02X) = %q, want %q", c, got, want)
		}
	}
}

func TestAbortReasonString(t *testing.T) {
	for r, want := range map[AbortReason]string{
		AbortSequence:         "sequence number error",
		AbortInstanceClosed:   "instance closed",
		AbortOutOfMemory:      "out of memory",
		AbortAddProvCons:      "add provider or consumer failed",
		AbortConsumerDHT:      "consumer data hold timer expired",
		AbortCMITimeout:       "CM initiator activity timeout",
		AbortAlarmOpen:        "alarm open failed",
		AbortAlarmSend:        "alarm send failed",
		AbortAlarmAckSend:     "alarm ack send failed",
		AbortAlarmDataTooLong: "alarm data too long",
		AbortAlarmInd:         "alarm indication error",
		AbortRPCClientCall:    "RPC client call failed",
		AbortUser:             "abort requested",
		AbortRerun:            "re-run aborts existing AR",
		AbortReleaseInd:       "release indication received",
		AbortDeactivated:      "device deactivated",
		AbortRemoved:          "removed",
		AbortProtocol:         "protocol violation",
		AbortNameResolution:   "name resolution error",
		AbortRPCBind:          "RPC bind error",
		AbortRPCConnect:       "RPC connect error",
		AbortRPCRead:          "RPC read error",
		AbortRPCWrite:         "RPC write error",
		AbortRPCControl:       "RPC control error",
		AbortPullPlug:         "forbidden pull or plug",
		AbortAPRemoved:        "AP removed",
		AbortLinkDown:         "link down",
		AbortMCRegister:       "multicast register failed",
		AbortNotSynchronized:  "not synchronized",
		AbortWrongTopology:    "wrong topology",
		AbortDCPNameChanged:   "DCP station name changed",
		AbortDCPReset:         "DCP reset to factory",
		AbortPDEVCheck:        "PDEV check failed",
		0x00:                  "abort reason 0x00",
		0x21:                  "abort reason 0x21",
	} {
		if got := r.String(); got != want {
			t.Errorf("AbortReason(0x%02X) = %q, want %q", uint8(r), got, want)
		}
	}
	// The codes match the reference table (IEC 61158-6-10, p-net).
	if AbortConsumerDHT != 0x05 || AbortDCPNameChanged != 0x1F || AbortDCPReset != 0x20 || AbortPDEVCheck != 0x24 {
		t.Fatal("abort reason codes")
	}
}

func TestStatusErrorIs(t *testing.T) {
	sentinels := []error{
		profinet.ErrRejected, profinet.ErrMalformed, profinet.ErrNotFound, profinet.ErrUnsupported,
		profinet.ErrInvalidState, profinet.ErrResourceExhausted, profinet.ErrAborted,
	}
	for _, c := range []struct {
		s     Status
		match []error // besides ErrRejected
	}{
		// Malformed: a faulty block, ErrorDecode PNIO, ErrorCode1 1..0x28.
		{ServiceStatus(CodeConnect, Code1ARBlock, 1), []error{profinet.ErrMalformed}},
		{ServiceStatus(CodeConnect, Code1ARRPC, 4), []error{profinet.ErrMalformed}},
		{ServiceStatus(CodeRead, Code1RWRecord, 2), []error{profinet.ErrMalformed}},
		{ServiceStatus(CodeControl, Code1PrmEnd, 2), []error{profinet.ErrMalformed}},
		{ServiceStatus(CodeRelease, Code1Release, 2), []error{profinet.ErrMalformed}},
		{ServiceStatus(CodeConnect, 0x29, 0), nil},
		{ServiceStatus(CodeConnect, 0, 0), nil},
		// Not found.
		{RWStatus(false, RWInvalidIndex, 0), []error{profinet.ErrNotFound}},
		{RWStatus(true, RWInvalidSlot, 0), []error{profinet.ErrNotFound}},
		{RWStatus(false, RWInvalidArea, 0), []error{profinet.ErrNotFound}},
		{ServiceStatus(CodeControl, Code1CMRPC, CMRPCARUUIDUnknown), []error{profinet.ErrNotFound}},
		// Unsupported.
		{RWStatus(false, RWFeatureNotSupport, 0), []error{profinet.ErrUnsupported}},
		{ServiceStatus(CodeConnect, Code1CMRPC, CMRPCUnknownBlocks), []error{profinet.ErrUnsupported}},
		// Invalid state.
		{RWStatus(true, RWStateConflict, 0), []error{profinet.ErrInvalidState}},
		{ServiceStatus(CodeConnect, Code1CMDEV, 0), []error{profinet.ErrInvalidState}},
		{ServiceStatus(CodeConnect, Code1CMDEV, 1), nil},
		{ServiceStatus(CodeControl, Code1CMRPC, CMRPCStateConflict), []error{profinet.ErrInvalidState}},
		// Resource exhausted.
		{RWStatus(false, RWBusy, 0), []error{profinet.ErrResourceExhausted}},
		{RWStatus(false, RWResourceBusy, 0), []error{profinet.ErrResourceExhausted}},
		{RWStatus(false, RWResourceUnavail, 0), []error{profinet.ErrResourceExhausted}},
		{ServiceStatus(CodeConnect, Code1CMRPC, CMRPCOutOfARs), []error{profinet.ErrResourceExhausted}},
		{ServiceStatus(CodeConnect, Code1CMRPC, CMRPCOutOfMemory), []error{profinet.ErrResourceExhausted}},
		// Aborted.
		{AbortStatus(AbortLinkDown), []error{profinet.ErrAborted}},
		{Status{CodeRTA, DecodeVendor, 1, 1}, []error{profinet.ErrAborted}},
		{Status{CodeConnect, DecodeVendor, Code1RTAAbort, 1}, []error{profinet.ErrAborted}},
		// Only rejected.
		{RWStatus(false, RWReadError, 0), nil},
		{ServiceStatus(CodeConnect, Code1CMRPC, CMRPCArgsLength), nil},
		{ServiceStatus(CodeConnect, Code1CMRPC, CMRPCIOCRMissing), nil},
		{Status{CodeRead, DecodeVendor, RWInvalidIndex, 0}, nil},
		// The RW class codes do not apply under ErrorDecode PNIO.
		{ServiceStatus(CodeRead, RWInvalidIndex, 0), nil},
	} {
		err := error(&StatusError{Op: "op", Status: c.s})
		for _, target := range sentinels {
			want := target == profinet.ErrRejected
			for _, m := range c.match {
				want = want || m == target
			}
			if got := errors.Is(err, target); got != want {
				t.Errorf("%s: errors.Is(%v) = %v, want %v", c.s, target, got, want)
			}
		}
		// Unrelated errors never match.
		if errors.Is(err, profinet.ErrTimeout) || errors.Is(err, profinet.ErrClosed) || errors.Is(err, errors.New("x")) {
			t.Errorf("%s matches an unrelated error", c.s)
		}
		// Wrapping keeps the classes.
		if !errors.Is(fmt.Errorf("wrapped: %w", err), profinet.ErrRejected) {
			t.Errorf("%s: wrapped error does not match", c.s)
		}
	}
}

func TestStatusErrorText(t *testing.T) {
	err := &StatusError{Op: "read 0xAFF0", Status: RWStatus(false, RWInvalidIndex, 0)}
	if got := err.Error(); got != "pnio: read 0xAFF0: DE 80 B0 00 (read: access: invalid index)" {
		t.Fatalf("Error() = %q", got)
	}
	var se *StatusError
	if !errors.As(fmt.Errorf("x: %w", err), &se) || !strings.HasPrefix(se.Op, "read") {
		t.Fatal("errors.As")
	}
}
