// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"net"

	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

// RecordRequest identifies the record of a Read or Write.
type RecordRequest struct {
	// AR is the application relationship of the request; it is the zero
	// ARInfo for an implicit read (ReadImplicit).
	AR      ARInfo
	API     uint32
	Slot    uint16
	Subslot uint16
	Index   pnio.Index
	// Length is the RecordDataLength of a read: the most the reader
	// accepts.
	Length uint32
	// Modeled reports a record of the model, which the device has
	// already validated and stored when WriteRecord is called.
	Modeled bool
}

// Handler receives what the device leaves to the application. Every method
// is called on a device goroutine and should return promptly. Embed
// [NopHandler] to implement only some of them.
type Handler interface {
	// ReadRecord reads a record the device does not serve itself: user
	// indices (0..0x7FFF) without a model.Record, and indices the device
	// does not know. Return a *pnio.StatusError (see RecordError) to
	// answer with a PNIO status.
	ReadRecord(ctx context.Context, r RecordRequest) ([]byte, error)

	// WriteRecord writes such a record. Writes of model.Records are
	// validated and stored by the device and then reported here; an error
	// rejects the write.
	WriteRecord(ctx context.Context, r RecordRequest, data []byte) error

	// PrmEnd reports the end of parameterization of an AR. When it
	// returns nil the device sends ApplicationReady; an error aborts the
	// AR. It may block until the application is ready (within the CM
	// activity timeout of the AR).
	PrmEnd(ctx context.Context, ar ARInfo) error

	// StateChanged reports every state transition of an AR, one at a time
	// and in the order the AR went through them. It may be called from a
	// different goroutine than the one that caused the transition, and
	// after that goroutine went on.
	StateChanged(ar ARInfo, from, to ARState)

	// AlarmAck reports the acknowledgement of an alarm by the controller.
	AlarmAck(ar ARInfo, ack pnio.AlarmAck)

	// Signal is a DCP request to flash the signal LED.
	Signal()

	// ResetToFactory is a DCP request to reset; mode is one of the
	// dcp.Reset* values. The device then aborts its ARs and resets what
	// the mode covers. Return an error code to refuse.
	ResetToFactory(mode uint16) dcp.BlockError

	// NameChanged and IPChanged report a DCP Set.
	NameChanged(name string, permanent bool)
	IPChanged(s dcp.IPSuite, permanent bool)
}

// NopHandler is a Handler that accepts everything and serves no records of
// its own.
type NopHandler struct{}

// ReadRecord answers "invalid index".
func (NopHandler) ReadRecord(context.Context, RecordRequest) ([]byte, error) {
	return nil, RecordError(pnio.RWInvalidIndex, 0)
}

// WriteRecord accepts writes of model records and rejects others with
// "invalid index".
func (NopHandler) WriteRecord(_ context.Context, r RecordRequest, _ []byte) error {
	if r.Modeled {
		return nil
	}
	return RecordError(pnio.RWInvalidIndex, 0)
}

// PrmEnd returns nil: the application is ready at once.
func (NopHandler) PrmEnd(context.Context, ARInfo) error { return nil }

// StateChanged does nothing.
func (NopHandler) StateChanged(ARInfo, ARState, ARState) {}

// AlarmAck does nothing.
func (NopHandler) AlarmAck(ARInfo, pnio.AlarmAck) {}

// Signal does nothing.
func (NopHandler) Signal() {}

// ResetToFactory accepts.
func (NopHandler) ResetToFactory(uint16) dcp.BlockError { return dcp.BlockErrorNone }

// NameChanged does nothing.
func (NopHandler) NameChanged(string, bool) {}

// IPChanged does nothing.
func (NopHandler) IPChanged(dcp.IPSuite, bool) {}

// RecordError returns the error a Handler returns to answer a Read or
// Write with an ErrorDecode PNIORW status: code1 is one of the pnio.RW*
// values, code2 is application specific. The device fills in the
// ErrorCode of the service.
func RecordError(code1, code2 uint8) error {
	return &pnio.StatusError{Op: "record", Status: pnio.Status{Decode: pnio.DecodePNIORW, Code1: code1, Code2: code2}}
}

// ARState is the state of an application relationship on the device.
type ARState uint8

// AR states. An IOCAR goes Parameterizing → AppReadyPending → Data; a
// PrmBegin returns it from Data to Parameterizing; an IOSAR (device access)
// is in Data from its Connect. Every state can go to Closed.
const (
	ARStateNone ARState = iota
	ARStateParameterizing
	ARStateAppReadyPending
	ARStateData
	ARStateClosed
)

func (s ARState) String() string {
	switch s {
	case ARStateNone:
		return "None"
	case ARStateParameterizing:
		return "Parameterizing"
	case ARStateAppReadyPending:
		return "ApplicationReadyPending"
	case ARStateData:
		return "Data"
	case ARStateClosed:
		return "Closed"
	default:
		return "ARState(?)"
	}
}

// transitions is the table of allowed AR state transitions.
var transitions = map[ARState][]ARState{
	ARStateNone:            {ARStateParameterizing, ARStateData, ARStateClosed},
	ARStateParameterizing:  {ARStateAppReadyPending, ARStateClosed},
	ARStateAppReadyPending: {ARStateData, ARStateClosed},
	ARStateData:            {ARStateParameterizing, ARStateClosed},
	ARStateClosed:          {},
}

// CanTransition reports whether the state machine allows from → to.
func CanTransition(from, to ARState) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ARInfo describes an application relationship.
type ARInfo struct {
	UUID           rpc.UUID
	Type           pnio.ARType
	SessionKey     uint16
	State          ARState
	ControllerMAC  ethernet.MAC
	ControllerAddr net.Addr
	ControllerName string
	// InputFrameID and OutputFrameID are the FrameIDs of the cyclic
	// frames (zero for an IOSAR).
	InputFrameID, OutputFrameID uint16
	// Cycle is the cycle time of the IOCRs.
	CycleNanos int64
	// CloseReason is set once the AR is closed: the abort reason, or 0
	// after a release.
	CloseReason pnio.AbortReason
}
