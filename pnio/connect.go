// SPDX-License-Identifier: MIT

package pnio

import (
	"errors"
	"fmt"

	"github.com/otfabric/go-profinet"
)

// ConnectRequest is the set of blocks of a Connect call.
type ConnectRequest struct {
	AR       ARBlockReq
	IOCRs    []IOCRBlockReq
	AlarmCR  *AlarmCRBlockReq
	Expected []ExpectedSubmoduleBlockReq
	ARRPC    *ARRPCBlockReq
	// Other holds blocks this package does not decode (ARVendorBlockReq,
	// PrmServerBlockReq, MCRBlockReq, IRInfoBlock, ...), in order.
	Other []RawBlock
}

// Append appends the blocks in the order IEC 61158-6-10 lists them.
func (c *ConnectRequest) Append(b []byte) []byte {
	b = c.AR.Append(b)
	for i := range c.IOCRs {
		b = c.IOCRs[i].Append(b)
	}
	if c.AlarmCR != nil {
		b = c.AlarmCR.Append(b)
	}
	for i := range c.Expected {
		b = c.Expected[i].Append(b)
	}
	if c.ARRPC != nil {
		b = c.ARRPC.Append(b)
	}
	for _, o := range c.Other {
		b = o.Append(b)
	}
	return b
}

// DecodeConnectRequest decodes the blocks of a Connect call. A malformed
// block is reported as a *StatusError carrying the PNIO status the
// device answers with.
func DecodeConnectRequest(b []byte) (ConnectRequest, error) {
	var c ConnectRequest
	blocks, err := SplitBlocks(b)
	if err != nil {
		return c, &StatusError{Op: "decode connect", Status: ServiceStatus(CodeConnect, Code1CMRPC, CMRPCArgsLength)}
	}
	seenAR := false
	for _, blk := range blocks {
		switch blk.Header.Type {
		case BlockARReq:
			if seenAR {
				return c, &StatusError{Op: "decode connect", Status: ServiceStatus(CodeConnect, Code1CMRPC, CMRPCUnknownBlocks)}
			}
			seenAR = true
			c.AR, err = DecodeARBlockReq(blk)
		case BlockIOCRReq:
			var io IOCRBlockReq
			io, err = DecodeIOCRBlockReq(blk)
			c.IOCRs = append(c.IOCRs, io)
		case BlockAlarmCRReq:
			if c.AlarmCR != nil {
				return c, &StatusError{Op: "decode connect", Status: ServiceStatus(CodeConnect, Code1CMRPC, CMRPCAlarmCRCount)}
			}
			var a AlarmCRBlockReq
			a, err = DecodeAlarmCRBlockReq(blk)
			c.AlarmCR = &a
		case BlockExpectedSubmReq:
			var e ExpectedSubmoduleBlockReq
			e, err = DecodeExpectedSubmoduleBlockReq(blk)
			c.Expected = append(c.Expected, e)
		case BlockARRPCReq:
			var a ARRPCBlockReq
			a, err = DecodeARRPCBlockReq(blk)
			c.ARRPC = &a
		default:
			c.Other = append(c.Other, blk)
		}
		if err != nil {
			return c, asConnectError(blk.Header.Type, err)
		}
	}
	if !seenAR {
		return c, &StatusError{Op: "decode connect", Status: ServiceStatus(CodeConnect, Code1CMRPC, CMRPCUnknownBlocks)}
	}
	return c, nil
}

// asConnectError turns a decoding error into the PNIO status of the faulty
// block.
func asConnectError(t BlockType, err error) error {
	var se *StatusError
	if errors.As(err, &se) {
		return se
	}
	return &StatusError{Op: "decode " + t.String(), Status: blockError(t, 1)}
}

// ConnectResponse is the set of blocks of a Connect response.
type ConnectResponse struct {
	AR         ARBlockRes
	IOCRs      []IOCRBlockRes
	AlarmCR    *AlarmCRBlockRes
	ModuleDiff *ModuleDiffBlock
	ARServer   *ARServerBlock
	ARRPC      *ARRPCBlockReq // the ARRPCBlockRes has the same layout
	Other      []RawBlock
}

// Append appends the blocks.
func (c *ConnectResponse) Append(b []byte) []byte {
	b = c.AR.Append(b)
	for i := range c.IOCRs {
		b = c.IOCRs[i].Append(b)
	}
	if c.AlarmCR != nil {
		b = c.AlarmCR.Append(b)
	}
	if c.ModuleDiff != nil {
		b = c.ModuleDiff.Append(b)
	}
	if c.ARServer != nil {
		b = c.ARServer.Append(b)
	}
	if c.ARRPC != nil {
		var s int
		b, s = beginBlock(b, BlockARRPCRes, 1, 0)
		b = appendU16(b, c.ARRPC.InitiatorRPCServerPort)
		b = endBlock(b, s)
	}
	for _, o := range c.Other {
		b = o.Append(b)
	}
	return b
}

// DecodeConnectResponse decodes the blocks of a Connect response.
func DecodeConnectResponse(b []byte) (ConnectResponse, error) {
	var c ConnectResponse
	blocks, err := SplitBlocks(b)
	if err != nil {
		return c, err
	}
	seenAR := false
	for _, blk := range blocks {
		switch blk.Header.Type {
		case BlockARRes:
			seenAR = true
			c.AR, err = DecodeARBlockRes(blk)
		case BlockIOCRRes:
			var io IOCRBlockRes
			io, err = DecodeIOCRBlockRes(blk)
			c.IOCRs = append(c.IOCRs, io)
		case BlockAlarmCRRes:
			var a AlarmCRBlockRes
			a, err = DecodeAlarmCRBlockRes(blk)
			c.AlarmCR = &a
		case BlockModuleDiff:
			var m ModuleDiffBlock
			m, err = DecodeModuleDiffBlock(blk)
			c.ModuleDiff = &m
		case BlockARServer:
			var a ARServerBlock
			a, err = DecodeARServerBlock(blk)
			c.ARServer = &a
		case BlockARRPCRes:
			var a ARRPCBlockReq
			a, err = DecodeARRPCBlockReq(blk)
			c.ARRPC = &a
		default:
			c.Other = append(c.Other, blk)
		}
		if err != nil {
			return c, err
		}
	}
	if !seenAR {
		return c, fmt.Errorf("pnio: %w: connect response without ARBlockRes", profinet.ErrMalformed)
	}
	return c, nil
}
