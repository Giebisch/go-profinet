// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/rpc"
)

// NDRHeaderLen is the length of the NDR argument header of PNIO calls.
const NDRHeaderLen = 20

// DefaultArgsMaximum is the ArgsMaximum a request announces: the largest
// response the caller accepts.
const DefaultArgsMaximum = 16696

// AppendNDRRequest appends the NDR request header and the blocks: the
// ArgsMaximum, ArgsLength and the conformant varying array header
// (MaximumCount, Offset, ActualCount), in the byte order o.
func AppendNDRRequest(b []byte, o rpc.ByteOrder, argsMax uint32, blocks []byte) []byte {
	b = o.AppendUint32(b, argsMax)
	b = o.AppendUint32(b, uint32(len(blocks)))
	b = o.AppendUint32(b, argsMax)
	b = o.AppendUint32(b, 0)
	b = o.AppendUint32(b, uint32(len(blocks)))
	return append(b, blocks...)
}

// DecodeNDRRequest returns ArgsMaximum and the blocks of a request body.
func DecodeNDRRequest(body []byte, o rpc.ByteOrder) (uint32, []byte, error) {
	if len(body) < NDRHeaderLen {
		return 0, nil, &StatusError{Op: "decode NDR request", Status: ServiceStatus(CodePNIO, Code1CMRPC, CMRPCArgsLength)}
	}
	argsMax := o.Uint32(body)
	argsLen := o.Uint32(body[4:])
	maxCount := o.Uint32(body[8:])
	offset := o.Uint32(body[12:])
	actual := o.Uint32(body[16:])
	rest := body[NDRHeaderLen:]
	if argsLen != actual || offset != 0 || actual > maxCount || int64(actual) > int64(len(rest)) {
		return 0, nil, &StatusError{Op: "decode NDR request", Status: ServiceStatus(CodePNIO, Code1CMRPC, CMRPCArgsLength)}
	}
	return argsMax, rest[:actual], nil
}

// AppendNDRResponse appends the NDR response header (PNIOStatus first)
// and the blocks.
func AppendNDRResponse(b []byte, o rpc.ByteOrder, status Status, argsMax uint32, blocks []byte) []byte {
	b = status.Append(b)
	b = o.AppendUint32(b, uint32(len(blocks)))
	b = o.AppendUint32(b, argsMax)
	b = o.AppendUint32(b, 0)
	b = o.AppendUint32(b, uint32(len(blocks)))
	return append(b, blocks...)
}

// DecodeNDRResponse returns the status and the blocks of a response body.
// A status other than OK is returned as is, not as an error; the blocks
// of a failed response are usually empty.
func DecodeNDRResponse(body []byte, o rpc.ByteOrder) (Status, []byte, error) {
	if len(body) < NDRHeaderLen {
		// A short failure response carries only the status.
		if len(body) >= 4 {
			if s := decodeStatus(body); !s.OK() {
				return s, nil, nil
			}
		}
		return Status{}, nil, fmt.Errorf("pnio: %w: NDR response of %d octets", profinet.ErrMalformed, len(body))
	}
	s := decodeStatus(body)
	argsLen := o.Uint32(body[4:])
	actual := o.Uint32(body[16:])
	rest := body[NDRHeaderLen:]
	if argsLen != actual || int64(actual) > int64(len(rest)) {
		return s, nil, fmt.Errorf("pnio: %w: NDR ArgsLength %d, ActualCount %d, %d octets present", profinet.ErrMalformed, argsLen, actual, len(rest))
	}
	return s, rest[:actual], nil
}
