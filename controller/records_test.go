// SPDX-License-Identifier: MIT

package controller

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

func devAddr() net.Addr { return &net.UDPAddr{IP: net.ParseIP(devIP).To4(), Port: rpc.Port} }

func TestRecordClientWithDevice(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	dev, _ := n.device(devIP)
	rc := NewRecordClient(c.RPC(), devAddr(), dev.ObjectUUID())
	if rc.Addr().String() != devAddr().String() {
		t.Fatalf("addr %v", rc.Addr())
	}
	ctx := context.Background()
	im0, err := rc.ReadIM0(ctx, rpc.NilUUID, 0, 1)
	if err != nil || im0.OrderID != "OTF-CTL-1" {
		t.Fatalf("I&M0 %+v %v", im0, err)
	}
	if _, err := rc.ReadIM0(ctx, rpc.NilUUID, 1, 1); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("I&M0 of slot 1: %v", err)
	}
	if _, err := rc.ReadDiagnosis(ctx, rpc.NilUUID, 9, 1, true); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("diagnosis of slot 9: %v", err)
	}
	ar := rpc.NewRandomUUID()
	req := &pnio.ConnectRequest{AR: pnio.ARBlockReq{Type: pnio.ARTypeIOSAR, ARUUID: ar, SessionKey: 3, InitiatorMAC: c.mux.HardwareAddr(),
		InitiatorObject: c.ObjectUUID(), Properties: pnio.ARPropStateActive | pnio.ARPropDeviceAccess, ActivityTimeoutFactor: 100, UDPRTPort: pnio.LT, StationName: "tool"}}
	resp, err := rc.Connect(ctx, req)
	if err != nil || resp.AR.ARUUID != ar {
		t.Fatalf("connect %+v %v", resp, err)
	}
	if err := rc.Write(ctx, ar, Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if b, err := rc.Read(ctx, ar, Record{Slot: 2, Subslot: 1, Index: 0x10}, 0); err != nil || !bytes.Equal(b, []byte{1, 2}) {
		t.Fatalf("%x %v", b, err)
	}
	wantStatus(t, rc.Write(ctx, ar, Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{1}), pnio.RWWriteLength, 0)
	// A WriteMultiple within an unknown AR fails as a whole.
	sts, err := rc.WriteMultiple(ctx, rpc.NewRandomUUID(), []WriteItem{{Record: Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{0, 0}}})
	if err != nil || len(sts) != 1 || sts[0].Code1 != pnio.Code1CMRPC {
		t.Fatalf("write multiple %v %v", sts, err)
	}
	// Control of an IOSAR: PrmEnd is a state conflict.
	wantStatus(t, rc.Control(ctx, pnio.ControlBlock{Type: pnio.BlockPrmEndReq, ARUUID: ar, SessionKey: 3, Command: pnio.CmdPrmEnd}), pnio.Code1CMDEV, 0)
	if err := rc.Control(ctx, pnio.ControlBlock{Type: pnio.BlockReleaseReq, ARUUID: ar, SessionKey: 3, Command: pnio.CmdRelease}); err != nil {
		t.Fatal(err)
	}
	// A rejected Connect.
	req.AR.Type = 0x77
	req.AR.ARUUID = rpc.NewRandomUUID()
	if _, err := rc.Connect(ctx, req); !errors.As(err, new(*pnio.StatusError)) {
		t.Fatalf("err %v", err)
	}
	if got := (Record{API: 1, Slot: 2, Subslot: 3, Index: 0x10}).String(); got != "1/2/0x0003 index 0x0010" {
		t.Fatalf("%q", got)
	}
}

func TestRecordClientMalformed(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	f := n.fake("fake")
	rc := NewRecordClient(c.RPC(), devAddr(), pnio.ObjectUUID(1, 2, 3))
	ctx := context.Background()
	ar := rpc.NewRandomUUID()
	answer := func(body []byte) {
		f.set(func(f *fakeDev) { f.raw = func(pnio.OpNum, []byte) []byte { return body } })
	}
	ok := func(blocks []byte) []byte {
		return pnio.AppendNDRResponse(nil, order, pnio.Status{}, pnio.DefaultArgsMaximum, blocks)
	}
	failed := func(st pnio.Status) []byte {
		return pnio.AppendNDRResponse(nil, order, st, pnio.DefaultArgsMaximum, nil)
	}
	readRes := func(length uint32, data []byte) []byte {
		h := pnio.ReadRes{ARUUID: ar, Slot: 0, Subslot: 1, Index: pnio.IndexIM0, Length: length}
		return ok(append(h.Append(nil), data...))
	}
	stErr := pnio.RWStatus(false, pnio.RWBusy, 0)
	rec := Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM0}
	notBlock := []byte{1}
	other := pnio.ControlBlock{Type: pnio.BlockType(0x0019)}
	im1 := pnio.IM1{TagFunction: "x"}

	// Read.
	for _, c := range []struct {
		name string
		body []byte
		bad  func(err error) bool
	}{
		{"short body", []byte{1, 2}, func(err error) bool { return errors.Is(err, profinet.ErrMalformed) }},
		{"status", failed(stErr), func(err error) bool { return errors.As(err, new(*pnio.StatusError)) }},
		{"no block", ok(nil), func(err error) bool { return errors.Is(err, profinet.ErrMalformed) }},
		{"not a ReadRes", ok(other.Append(nil)), func(err error) bool { return err != nil }},
		{"length beyond data", readRes(10, []byte{1}), func(err error) bool { return errors.Is(err, profinet.ErrMalformed) }},
	} {
		answer(c.body)
		if _, err := rc.Read(ctx, ar, rec, 0); !c.bad(err) {
			t.Fatalf("read %s: %v", c.name, err)
		}
	}
	// ReadIM0 of data that is not I&M0.
	answer(readRes(1, notBlock))
	if _, err := rc.ReadIM0(ctx, ar, 0, 1); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
	b := im1.Append(nil)
	answer(readRes(uint32(len(b)), b))
	if _, err := rc.ReadIM0(ctx, ar, 0, 1); err == nil {
		t.Fatal("I&M1 decoded as I&M0")
	}
	// ReadDiagnosis of data that is not diagnosis.
	answer(readRes(1, notBlock))
	if _, err := rc.ReadDiagnosis(ctx, ar, 0, 0, false); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
	answer(readRes(uint32(len(b)), b))
	if dd, err := rc.ReadDiagnosis(ctx, ar, 0, 0, false); err == nil || len(dd) != 0 {
		t.Fatalf("%+v %v", dd, err)
	}

	// Write: the status of the WriteRes counts.
	wres := pnio.WriteRes{ARUUID: ar, Index: 0x10, Status: pnio.RWStatus(true, pnio.RWAccessDenied, 0)}
	answer(ok(wres.Append(nil)))
	wantStatus(t, rc.Write(ctx, ar, Record{Index: 0x10}, nil), pnio.RWAccessDenied, 0)
	answer(failed(stErr))
	wantStatus(t, rc.Write(ctx, ar, Record{Index: 0x10}, nil), pnio.RWBusy, 0)
	answer([]byte{1, 2})
	if err := rc.Write(ctx, ar, Record{Index: 0x10}, nil); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}

	// WriteMultiple.
	items := []WriteItem{{Record: Record{Index: 0x10}, Data: []byte{1}}}
	answer(failed(stErr))
	if _, err := rc.WriteMultiple(ctx, ar, items); !errors.As(err, new(*pnio.StatusError)) {
		t.Fatalf("err %v", err)
	}
	answer(ok(nil))
	if _, err := rc.WriteMultiple(ctx, ar, items); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
	answer(ok(other.Append(nil)))
	if _, err := rc.WriteMultiple(ctx, ar, items); err == nil {
		t.Fatal("ControlBlock decoded as WriteRes")
	}
	answer([]byte{1, 2})
	if _, err := rc.WriteMultiple(ctx, ar, items); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}

	// Control: the answer must acknowledge the request.
	cb := pnio.ControlBlock{Type: pnio.BlockPrmEndReq, ARUUID: ar, SessionKey: 1, Command: pnio.CmdPrmEnd}
	res := cb.Response()
	wrongAR := res
	wrongAR.ARUUID = rpc.NewRandomUUID()
	notDone := res
	notDone.Command = pnio.CmdPrmEnd
	for _, c := range []struct {
		name string
		body []byte
	}{
		{"short body", []byte{1, 2}},
		{"status", failed(stErr)},
		{"no block", ok(nil)},
		{"not a control block", ok(other.Append(nil))},
		{"other AR", ok(wrongAR.Append(nil))},
		{"not done", ok(notDone.Append(nil))},
		{"request type", ok(cb.Append(nil))},
	} {
		answer(c.body)
		if err := rc.Control(ctx, cb); err == nil {
			t.Fatalf("control %s accepted", c.name)
		}
	}
	answer(ok(res.Append(nil)))
	if err := rc.Control(ctx, cb); err != nil {
		t.Fatal(err)
	}

	// Connect.
	answer([]byte{1, 2})
	if _, err := rc.Connect(ctx, &pnio.ConnectRequest{}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
	answer(ok([]byte{0x81, 0x01, 0, 50}))
	if _, err := rc.Connect(ctx, &pnio.ConnectRequest{}); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}

	// Transport failures.
	f.set(func(f *fakeDev) { f.raw = nil })
	dead := NewRecordClient(c.RPC(), &net.UDPAddr{IP: net.ParseIP("192.168.0.77").To4(), Port: rpc.Port}, pnio.ObjectUUID(1, 2, 3))
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := dead.Read(cctx, ar, rec, 0); err == nil {
		t.Fatal("read from nowhere")
	}
	if err := dead.Write(cctx, ar, rec, nil); err == nil {
		t.Fatal("write to nowhere")
	}
	if _, err := dead.WriteMultiple(cctx, ar, items); err == nil {
		t.Fatal("write multiple to nowhere")
	}
	if err := dead.Control(cctx, cb); err == nil {
		t.Fatal("control of nowhere")
	}
	if _, err := dead.Connect(cctx, &pnio.ConnectRequest{}); err == nil {
		t.Fatal("connect to nowhere")
	}
}
