// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

// order is the data representation of the calls the controller makes; it
// matches the drep the RPC client sends (little endian).
var order rpc.ByteOrder = binary.LittleEndian

// RecordClient performs the acyclic PNIO services of one device: Read,
// Write, ReadImplicit, Control and Release. It is safe for concurrent use.
type RecordClient struct {
	rpc    *rpc.Client
	addr   net.Addr
	object rpc.UUID
	seq    atomic.Uint32
}

// NewRecordClient returns a client for the device at addr (its RPC
// endpoint, port 34964) with PNIO object UUID object (see
// pnio.ObjectUUID).
func NewRecordClient(c *rpc.Client, addr net.Addr, object rpc.UUID) *RecordClient {
	return &RecordClient{rpc: c, addr: addr, object: object}
}

// Addr returns the device's RPC address.
func (r *RecordClient) Addr() net.Addr { return r.addr }

// call performs one PNIO call and returns the status and blocks of the
// response.
func (r *RecordClient) call(ctx context.Context, op pnio.OpNum, blocks []byte) (pnio.Status, []byte, error) {
	body := pnio.AppendNDRRequest(nil, order, pnio.DefaultArgsMaximum, blocks)
	resp, err := r.rpc.Do(ctx, r.addr, rpc.Call{Object: r.object, Iface: pnio.DeviceInterface, IfaceVersion: pnio.InterfaceVersion, OpNum: uint16(op), Body: body})
	if err != nil {
		return pnio.Status{}, nil, err
	}
	return pnio.DecodeNDRResponse(resp.Body, resp.Header.Order())
}

// Record addresses one record.
type Record struct {
	API     uint32
	Slot    uint16
	Subslot uint16
	Index   pnio.Index
}

func (rec Record) String() string {
	return fmt.Sprintf("%d/%d/0x%04X index %s", rec.API, rec.Slot, rec.Subslot, rec.Index)
}

// Read reads a record within an AR (ar is the AR UUID) or, with a nil ar,
// implicitly (ReadImplicit, no AR needed). maxLen bounds the data; zero
// means 4096. A PNIO error is returned as a *pnio.StatusError.
func (r *RecordClient) Read(ctx context.Context, ar rpc.UUID, rec Record, maxLen uint32) ([]byte, error) {
	if maxLen == 0 {
		maxLen = 4096
	}
	op := pnio.OpRead
	if ar.IsNil() {
		op = pnio.OpReadImplicit
	}
	q := pnio.ReadReq{Seq: uint16(r.seq.Add(1)), ARUUID: ar, API: rec.API, Slot: rec.Slot, Subslot: rec.Subslot, Index: rec.Index, Length: maxLen}
	st, blocks, err := r.call(ctx, op, q.Append(nil))
	if err != nil {
		return nil, fmt.Errorf("controller: read %s: %w", rec, err)
	}
	if !st.OK() {
		return nil, &pnio.StatusError{Op: "read " + rec.String(), Status: st}
	}
	blk, rest, err := pnio.ReadBlock(blocks)
	if err != nil {
		return nil, fmt.Errorf("controller: read %s: %w", rec, err)
	}
	h, err := pnio.DecodeReadRes(blk)
	if err != nil {
		return nil, fmt.Errorf("controller: read %s: %w", rec, err)
	}
	if int(h.Length) > len(rest) {
		return nil, fmt.Errorf("controller: read %s: %w: RecordDataLength %d, %d octets present", rec, profinet.ErrMalformed, h.Length, len(rest))
	}
	return append([]byte(nil), rest[:h.Length]...), nil
}

// Write writes a record within an AR.
func (r *RecordClient) Write(ctx context.Context, ar rpc.UUID, rec Record, data []byte) error {
	q := pnio.WriteReq{Seq: uint16(r.seq.Add(1)), ARUUID: ar, API: rec.API, Slot: rec.Slot, Subslot: rec.Subslot, Index: rec.Index, Length: uint32(len(data))}
	st, blocks, err := r.call(ctx, pnio.OpWrite, append(q.Append(nil), data...))
	if err != nil {
		return fmt.Errorf("controller: write %s: %w", rec, err)
	}
	if blk, _, err := pnio.ReadBlock(blocks); err == nil {
		if res, err := pnio.DecodeWriteRes(blk); err == nil && !res.Status.OK() {
			st = res.Status
		}
	}
	if !st.OK() {
		return &pnio.StatusError{Op: "write " + rec.String(), Status: st}
	}
	return nil
}

// WriteItem is one record of a WriteMultiple.
type WriteItem struct {
	Record Record
	Data   []byte
}

// WriteMultiple writes several records in one call (index 0xE040). It
// returns the status of each item; err reports a transport failure or a
// failed call without per-item results.
func (r *RecordClient) WriteMultiple(ctx context.Context, ar rpc.UUID, items []WriteItem) ([]pnio.Status, error) {
	mi := make([]pnio.WriteMultipleItem, len(items))
	for i, it := range items {
		mi[i] = pnio.WriteMultipleItem{
			Header: pnio.WriteReq{Seq: uint16(r.seq.Add(1)), ARUUID: ar, API: it.Record.API, Slot: it.Record.Slot, Subslot: it.Record.Subslot, Index: it.Record.Index},
			Data:   it.Data,
		}
	}
	data := pnio.AppendWriteMultiple(nil, mi)
	q := pnio.WriteReq{Seq: uint16(r.seq.Add(1)), ARUUID: ar, API: 0xFFFFFFFF, Slot: 0xFFFF, Subslot: 0xFFFF, Index: pnio.IndexWriteMultiple, Length: uint32(len(data))}
	st, blocks, err := r.call(ctx, pnio.OpWrite, append(q.Append(nil), data...))
	if err != nil {
		return nil, fmt.Errorf("controller: write multiple: %w", err)
	}
	raws, err := pnio.SplitBlocks(blocks)
	if err != nil || len(raws) == 0 {
		if !st.OK() {
			return nil, &pnio.StatusError{Op: "write multiple", Status: st}
		}
		return nil, fmt.Errorf("controller: write multiple: %w: no results", profinet.ErrMalformed)
	}
	out := make([]pnio.Status, 0, len(raws))
	for _, b := range raws {
		res, err := pnio.DecodeWriteRes(b)
		if err != nil {
			return out, err
		}
		out = append(out, res.Status)
	}
	return out, nil
}

// Control sends a control block (PrmEnd, PrmBegin) and checks the Done
// acknowledgement.
func (r *RecordClient) Control(ctx context.Context, c pnio.ControlBlock) error {
	op := pnio.OpControl
	if c.Type == pnio.BlockReleaseReq {
		op = pnio.OpRelease
	}
	st, blocks, err := r.call(ctx, op, c.Append(nil))
	if err != nil {
		return fmt.Errorf("controller: %s: %w", c.Type, err)
	}
	if !st.OK() {
		return &pnio.StatusError{Op: c.Type.String(), Status: st}
	}
	blk, _, err := pnio.ReadBlock(blocks)
	if err != nil {
		return fmt.Errorf("controller: %s: %w", c.Type, err)
	}
	res, err := pnio.DecodeControlBlock(blk)
	if err != nil {
		return fmt.Errorf("controller: %s: %w", c.Type, err)
	}
	if res.Type != c.Type|0x8000 || res.Command&pnio.CmdDone == 0 || res.ARUUID != c.ARUUID {
		return fmt.Errorf("controller: %s: %w: answered by %s with command 0x%04X", c.Type, profinet.ErrMalformed, res.Type, res.Command)
	}
	return nil
}

// Connect sends a Connect request and returns the decoded response.
func (r *RecordClient) Connect(ctx context.Context, req *pnio.ConnectRequest) (*pnio.ConnectResponse, error) {
	st, blocks, err := r.call(ctx, pnio.OpConnect, req.Append(nil))
	if err != nil {
		return nil, fmt.Errorf("controller: connect: %w", err)
	}
	if !st.OK() {
		return nil, &pnio.StatusError{Op: "connect", Status: st}
	}
	resp, err := pnio.DecodeConnectResponse(blocks)
	if err != nil {
		return nil, fmt.Errorf("controller: connect: %w", err)
	}
	return &resp, nil
}

// ReadIM0 reads I&M0 of a submodule (the DAP: slot 0, subslot 1).
func (r *RecordClient) ReadIM0(ctx context.Context, ar rpc.UUID, slot, subslot uint16) (pnio.IM0, error) {
	b, err := r.Read(ctx, ar, Record{Slot: slot, Subslot: subslot, Index: pnio.IndexIM0}, 0)
	if err != nil {
		return pnio.IM0{}, err
	}
	blk, _, err := pnio.ReadBlock(b)
	if err != nil {
		return pnio.IM0{}, err
	}
	return pnio.DecodeIM0(blk)
}

// ReadDiagnosis reads the diagnosis of the device (index 0xF80C), or of
// one submodule when slot and subslot are given with sub true (0x800C).
func (r *RecordClient) ReadDiagnosis(ctx context.Context, ar rpc.UUID, slot, subslot uint16, sub bool) ([]pnio.DiagnosisData, error) {
	rec := Record{Index: pnio.IndexDiagDMQSDevice}
	if sub {
		rec = Record{Slot: slot, Subslot: subslot, Index: pnio.IndexDiagDMQSSubslot}
	}
	b, err := r.Read(ctx, ar, rec, 0)
	if err != nil {
		return nil, err
	}
	blocks, err := pnio.SplitBlocks(b)
	if err != nil {
		return nil, err
	}
	out := make([]pnio.DiagnosisData, 0, len(blocks))
	for _, blk := range blocks {
		d, err := pnio.DecodeDiagnosisData(blk)
		if err != nil {
			return out, err
		}
		out = append(out, d)
	}
	return out, nil
}
