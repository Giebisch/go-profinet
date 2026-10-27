// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

// rpcOrder is the data representation of the calls the device makes; it
// must match the drep the RPC client sends (little endian).
var rpcOrder rpc.ByteOrder = binary.LittleEndian

// serveRPC dispatches a call of the PNIO device interface.
func (d *Device) serveRPC(ctx context.Context, req *rpc.Request) ([]byte, error) {
	if !pnio.IsObjectUUID(req.Header.Object) && !req.Header.Object.IsNil() {
		return nil, &rpc.Fault{Reject: true, Status: rpc.StatusUnknownIf}
	}
	o := req.Order()
	argsMax, blocks, err := pnio.DecodeNDRRequest(req.Body, o)
	op := pnio.OpNum(req.Header.OpNum)
	if err != nil {
		return d.statusResponse(o, op, err, argsMax), nil
	}
	switch op {
	case pnio.OpConnect:
		creq, err := pnio.DecodeConnectRequest(blocks)
		if err != nil {
			return d.statusResponse(o, op, err, argsMax), nil
		}
		resp, err := d.connect(ctx, req.Addr, &creq)
		if err != nil {
			d.log.Warnf("device: connect from %s rejected: %v", req.Addr, err)
			return d.statusResponse(o, op, err, argsMax), nil
		}
		return pnio.AppendNDRResponse(nil, o, pnio.Status{}, argsMax, resp.Append(nil)), nil
	case pnio.OpRelease, pnio.OpControl:
		out, err := d.control(op, blocks)
		if err != nil {
			return d.statusResponse(o, op, err, argsMax), nil
		}
		return pnio.AppendNDRResponse(nil, o, pnio.Status{}, argsMax, out), nil
	case pnio.OpRead, pnio.OpReadImplicit:
		out, st := d.read(ctx, op == pnio.OpReadImplicit, blocks)
		return pnio.AppendNDRResponse(nil, o, st, argsMax, out), nil
	case pnio.OpWrite:
		out, st := d.write(ctx, blocks)
		return pnio.AppendNDRResponse(nil, o, st, argsMax, out), nil
	default:
		return nil, &rpc.Fault{Reject: true, Status: rpc.StatusOpRangeError}
	}
}

// statusResponse returns the NDR response of a failed call.
func (d *Device) statusResponse(o rpc.ByteOrder, op pnio.OpNum, err error, argsMax uint32) []byte {
	var se *pnio.StatusError
	st := pnio.ServiceStatus(serviceCode(op), pnio.Code1CMRPC, pnio.CMRPCStateConflict)
	if errors.As(err, &se) {
		st = se.Status
		if st.Code == 0 || st.Code == pnio.CodePNIO {
			st.Code = serviceCode(op)
		}
	}
	return pnio.AppendNDRResponse(nil, o, st, argsMax, nil)
}

func serviceCode(op pnio.OpNum) uint8 {
	switch op {
	case pnio.OpConnect:
		return pnio.CodeConnect
	case pnio.OpRelease:
		return pnio.CodeRelease
	case pnio.OpControl:
		return pnio.CodeControl
	case pnio.OpRead, pnio.OpReadImplicit:
		return pnio.CodeRead
	case pnio.OpWrite:
		return pnio.CodeWrite
	default:
		return pnio.CodePNIO
	}
}

// findAR returns the AR of a request, checking its session key.
func (d *Device) findAR(uuid rpc.UUID, sessionKey uint16, checkKey bool) (*ar, error) {
	d.mu.Lock()
	a := d.ars[uuid]
	d.mu.Unlock()
	if a == nil || checkKey && a.sessionKey != sessionKey {
		return nil, &pnio.StatusError{Op: "find AR", Status: pnio.ServiceStatus(pnio.CodePNIO, pnio.Code1CMRPC, pnio.CMRPCARUUIDUnknown)}
	}
	a.touch()
	return a, nil
}

// control handles Release and Control calls.
func (d *Device) control(op pnio.OpNum, blocks []byte) ([]byte, error) {
	blk, _, err := pnio.ReadBlock(blocks)
	if err != nil {
		return nil, &pnio.StatusError{Op: "control", Status: pnio.ServiceStatus(serviceCode(op), pnio.Code1CMRPC, pnio.CMRPCArgsLength)}
	}
	c, err := pnio.DecodeControlBlock(blk)
	if err != nil {
		return nil, err
	}
	a, err := d.findAR(c.ARUUID, c.SessionKey, true)
	if err != nil {
		return nil, err
	}
	stateErr := func() error {
		return &pnio.StatusError{Op: "control", Status: pnio.ServiceStatus(serviceCode(op), pnio.Code1CMDEV, 0)}
	}
	resp := c.Response()
	switch {
	case op == pnio.OpRelease && c.Type == pnio.BlockReleaseReq && c.Command&pnio.CmdRelease != 0:
		out := resp.Append(nil)
		// The response goes out before the AR is torn down.
		go d.abort(a, 0, false)
		return out, nil
	case op == pnio.OpControl && c.Type == pnio.BlockPrmEndReq && c.Command&pnio.CmdPrmEnd != 0:
		if err := a.transition(ARStateAppReadyPending); err != nil {
			return nil, stateErr()
		}
		d.prmEnd(a)
		return resp.Append(nil), nil
	case op == pnio.OpControl && c.Type == pnio.BlockPrmEndPlugReq && c.Command&pnio.CmdPrmEnd != 0:
		// The controller parameterized a submodule handed over to it
		// (owner.go); it is answered with ApplicationReady (plug).
		a.mu.Lock()
		busy := a.plugBusy
		a.mu.Unlock()
		if !busy || a.currentState() != ARStateData {
			return nil, stateErr()
		}
		select {
		case a.prmEndPlug <- c.AlarmSequence:
		default:
		}
		return resp.Append(nil), nil
	case op == pnio.OpControl && c.Type == pnio.BlockPrmBeginReq && c.Command&pnio.CmdPrmBegin != 0:
		if a.currentState() != ARStateData || a.typ == pnio.ARTypeIOSAR {
			return nil, stateErr()
		}
		if err := a.transition(ARStateParameterizing); err != nil {
			return nil, stateErr()
		}
		a.provider.SetDataStatus(a.provider.DataStatus() &^ 0x10)
		return resp.Append(nil), nil
	default:
		return nil, &pnio.StatusError{Op: "control", Status: pnio.ServiceStatus(serviceCode(op), pnio.Code1PrmEnd, 8)}
	}
}

// read handles Read and ReadImplicit.
func (d *Device) read(ctx context.Context, implicit bool, blocks []byte) ([]byte, pnio.Status) {
	blk, _, err := pnio.ReadBlock(blocks)
	if err != nil {
		return nil, pnio.ServiceStatus(pnio.CodeRead, pnio.Code1CMRPC, pnio.CMRPCArgsLength)
	}
	q, err := pnio.DecodeReadReq(blk)
	if err != nil {
		return nil, asStatus(err, false)
	}
	var a *ar
	if !implicit {
		if a, err = d.findAR(q.ARUUID, 0, false); err != nil {
			return nil, asStatus(err, false)
		}
	} else if !q.TargetARUUID.IsNil() {
		d.mu.Lock()
		a = d.ars[q.TargetARUUID]
		d.mu.Unlock()
	}
	data, err := d.readRecord(ctx, a, q)
	if err != nil {
		return nil, asStatus(err, false)
	}
	if uint32(len(data)) > q.Length {
		data = data[:q.Length]
	}
	h := pnio.ReadRes{Seq: q.Seq, ARUUID: q.ARUUID, API: q.API, Slot: q.Slot, Subslot: q.Subslot, Index: q.Index, Length: uint32(len(data))}
	return append(h.Append(nil), data...), pnio.Status{}
}

// write handles Write, including WriteMultiple.
func (d *Device) write(ctx context.Context, blocks []byte) ([]byte, pnio.Status) {
	blk, rest, err := pnio.ReadBlock(blocks)
	if err != nil {
		return nil, pnio.ServiceStatus(pnio.CodeWrite, pnio.Code1CMRPC, pnio.CMRPCArgsLength)
	}
	q, err := pnio.DecodeWriteReq(blk)
	if err != nil {
		return nil, asStatus(err, true)
	}
	if int(q.Length) > len(rest) {
		st := pnio.RWStatus(true, pnio.RWWriteLength, 0)
		return d.writeRes(q, st), st
	}
	a, err := d.findAR(q.ARUUID, 0, false)
	if err != nil {
		st := asStatus(err, true)
		return d.writeRes(q, st), st
	}
	data := rest[:q.Length]
	if q.Index != pnio.IndexWriteMultiple {
		st := asStatus(d.writeRecord(ctx, a, q, data), true)
		return d.writeRes(q, st), st
	}
	items, err := pnio.SplitWriteMultiple(data)
	if err != nil {
		st := pnio.RWStatus(true, pnio.RWWriteLength, 0)
		return d.writeRes(q, st), st
	}
	// The response starts with the IODWriteResHeader of the WriteMultiple
	// itself (RecordDataLength 0), then one per item, as p-net answers and
	// Wireshark decodes it.
	outer := q
	outer.Length = 0
	out := d.writeRes(outer, pnio.Status{})
	var first pnio.Status
	for _, it := range items {
		h := it.Header
		h.ARUUID = q.ARUUID
		st := asStatus(d.writeRecord(ctx, a, h, it.Data), true)
		if first.OK() && !st.OK() {
			first = st
		}
		out = append(out, d.writeRes(h, st)...)
	}
	return out, first
}

func (d *Device) writeRes(q pnio.WriteReq, st pnio.Status) []byte {
	r := pnio.WriteRes{Seq: q.Seq, ARUUID: q.ARUUID, API: q.API, Slot: q.Slot, Subslot: q.Subslot, Index: q.Index, Length: q.Length, Status: st}
	return r.Append(nil)
}

// asStatus turns an error into the PNIO status of a read or write.
func asStatus(err error, write bool) pnio.Status {
	if err == nil {
		return pnio.Status{}
	}
	var se *pnio.StatusError
	if errors.As(err, &se) {
		st := se.Status
		if st.Code == 0 || st.Code == pnio.CodePNIO {
			st.Code = pnio.CodeRead
			if write {
				st.Code = pnio.CodeWrite
			}
		}
		return st
	}
	code := pnio.RWReadError
	if write {
		code = pnio.RWWriteError
	}
	switch {
	case errors.Is(err, profinet.ErrNotFound):
		code = pnio.RWInvalidIndex
	case errors.Is(err, profinet.ErrUnsupported):
		code = pnio.RWFeatureNotSupport
	case errors.Is(err, profinet.ErrInvalidState):
		code = pnio.RWStateConflict
	}
	return pnio.RWStatus(write, code, 0)
}

var errInvalidIndex = RecordError(pnio.RWInvalidIndex, 0)

func rwErr(code1 uint8) error { return RecordError(code1, 0) }

var _ = fmt.Sprint
