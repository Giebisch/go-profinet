// SPDX-License-Identifier: MIT

package profinet

import "errors"

// Sentinel errors shared by every package of the module. They are usually
// wrapped with detail; test with errors.Is. Typed errors that carry a
// PROFINET error code (a PNIO status, a DCP block error, an RPC fault)
// match one of these as well, so that a caller can test for the class of a
// failure without knowing the code.
var (
	// ErrClosed reports an operation on a controller, device, connection
	// or port that was closed by the application.
	ErrClosed = errors.New("profinet: closed")

	// ErrTimeout reports that the peer did not answer within the
	// configured time: a DCP request, an RPC call, an alarm that was not
	// acknowledged, or a cyclic consumer whose watchdog expired.
	ErrTimeout = errors.New("profinet: timeout")

	// ErrMalformed reports input that cannot be decoded: a frame, a block
	// or a document that is truncated, has an invalid length, an invalid
	// field value or trailing data where none is allowed. Network input
	// that fails to decode is dropped and counted; it never panics.
	ErrMalformed = errors.New("profinet: malformed")

	// ErrUnsupported reports a request for a service, option, block type
	// or feature this implementation (or, for a remote error, the peer)
	// does not provide.
	ErrUnsupported = errors.New("profinet: unsupported")

	// ErrInvalidConfig reports a configuration value that a controller,
	// device, port or codec cannot be constructed or used with.
	ErrInvalidConfig = errors.New("profinet: invalid configuration")

	// ErrInvalidState reports an operation that is not allowed in the
	// current state of a state machine, for example a record write on an
	// application relationship that is not established.
	ErrInvalidState = errors.New("profinet: invalid state")

	// ErrRejected reports that the peer answered with an error: a PNIO
	// status other than OK, a DCP block error or an RPC fault or reject.
	// The typed error that carries the code wraps or matches it.
	ErrRejected = errors.New("profinet: rejected by peer")

	// ErrAborted reports that an application relationship ended without
	// a release: a watchdog expired, the peer sent an abort, the CM
	// activity timeout elapsed or the link went down.
	ErrAborted = errors.New("profinet: application relationship aborted")

	// ErrNotFound reports that no device answered a DCP request for a
	// station name or MAC address, or that a slot, subslot, record index
	// or application relationship does not exist.
	ErrNotFound = errors.New("profinet: not found")

	// ErrResourceExhausted reports that a bounded resource is used up:
	// the number of application relationships, a queue, a reassembly
	// buffer or the number of concurrent requests.
	ErrResourceExhausted = errors.New("profinet: resource exhausted")

	// ErrLinkDown reports that the Ethernet link of a port is down.
	ErrLinkDown = errors.New("profinet: link down")
)
