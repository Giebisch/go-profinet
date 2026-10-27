// SPDX-License-Identifier: MIT

package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

// shortBlock returns a block with the header of full and a one-octet
// body: decoders reject it.
func shortBlock(full []byte) []byte {
	return []byte{full[0], full[1], 0, 3, full[4], full[5], 0}
}

func recordHandler() *recHandler {
	h := newRecHandler()
	h.read = func(r RecordRequest) ([]byte, error) {
		switch r.Index {
		case 0x7000:
			return []byte(fmt.Sprintf("user %d/%d len %d", r.Slot, r.Subslot, r.Length)), nil
		case 0x7001:
			return nil, RecordError(pnio.RWInvalidRange, 0x42)
		case 0x7002:
			return nil, profinet.ErrUnsupported
		case 0x7003:
			return nil, profinet.ErrInvalidState
		case 0x7004:
			return nil, errors.New("boom")
		case 0x7005:
			return nil, fmt.Errorf("x: %w", profinet.ErrNotFound)
		default:
		}
		return NopHandler{}.ReadRecord(context.Background(), r)
	}
	h.write = func(r RecordRequest, data []byte) error {
		switch {
		case r.Index == 0x10 && r.Modeled && bytes.Equal(data, []byte{9, 9}):
			return RecordError(pnio.RWInvalidParameter, 1)
		case r.Index == 0x7000 && !r.Modeled:
			return nil
		case r.Index == 0x7004:
			return errors.New("boom")
		}
		return NopHandler{}.WriteRecord(context.Background(), r, data)
	}
	return h
}

func TestRecords(t *testing.T) {
	n := newNet(t)
	h := recordHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	ctx := context.Background()
	sar := p.ioSAR()
	if _, err := p.connect(sar); err != nil {
		t.Fatal(err)
	}
	ar := sar.AR.ARUUID
	read := func(slot, subslot uint16, idx pnio.Index) ([]byte, error) {
		return p.rc.Read(ctx, ar, controller.Record{Slot: slot, Subslot: subslot, Index: idx}, 0)
	}
	write := func(slot, subslot uint16, idx pnio.Index, data []byte) error {
		return p.rc.Write(ctx, ar, controller.Record{Slot: slot, Subslot: subslot, Index: idx}, data)
	}
	rwCode := func(err error, code1, code2 uint8) {
		t.Helper()
		wantStatus(t, err, code1, code2)
	}

	// Addressing.
	_, err := p.rc.Read(ctx, ar, controller.Record{API: 1, Slot: 0, Subslot: 1, Index: pnio.IndexIM0}, 0)
	rwCode(err, pnio.RWInvalidArea, 0)
	rwCode(p.rc.Write(ctx, ar, controller.Record{API: 1, Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 0}), pnio.RWInvalidArea, 0)
	_, err = read(9, 1, pnio.IndexIM0)
	rwCode(err, pnio.RWInvalidSlot, 0)
	rwCode(write(9, 1, 0x10, []byte{0, 0}), pnio.RWInvalidSlot, 0)
	_, err = p.rc.Read(ctx, rpc.NewRandomUUID(), controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM0}, 0)
	rwCode(err, pnio.Code1CMRPC, pnio.CMRPCARUUIDUnknown)
	rwCode(p.rc.Write(ctx, rpc.NewRandomUUID(), controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 0}), pnio.Code1CMRPC, pnio.CMRPCARUUIDUnknown)

	// I&M0 to I&M4 at the DAP, and nowhere else.
	im0, err := p.rc.ReadIM0(ctx, ar, 0, 1)
	if err != nil || im0.OrderID != "OTF-DEV-1" || im0.VendorID != 0xF000 || im0.SerialNumber != "0001" {
		t.Fatalf("I&M0 %+v %v", im0, err)
	}
	for _, idx := range []pnio.Index{pnio.IndexIM0, pnio.IndexIM1, pnio.IndexIM2, pnio.IndexIM3, pnio.IndexIM4} {
		if _, err := read(0, 1, idx); err != nil {
			t.Fatalf("%s: %v", idx, err)
		}
		_, err := read(1, 1, idx)
		rwCode(err, pnio.RWInvalidIndex, 0)
	}
	ims := map[pnio.Index][]byte{
		pnio.IndexIM1: (&pnio.IM1{TagFunction: "f", TagLocation: "l"}).Append(nil),
		pnio.IndexIM2: (&pnio.IM2{Date: "2026-01-02 03:04"}).Append(nil),
		pnio.IndexIM3: (&pnio.IM3{Descriptor: "d"}).Append(nil),
		pnio.IndexIM4: (&pnio.IM4{Signature: [pnio.IMSignatureLen]byte{1, 2, 3}}).Append(nil),
	}
	for idx, b := range ims {
		if err := write(0, 1, idx, b); err != nil {
			t.Fatalf("%s: %v", idx, err)
		}
		got, err := read(0, 1, idx)
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("%s: %x %v, want %x", idx, got, err, b)
		}
		rwCode(write(1, 1, idx, b), pnio.RWInvalidIndex, 0)
		rwCode(write(0, 1, idx, []byte{1, 2}), pnio.RWWriteLength, 0)
		rwCode(write(0, 1, idx, append(append([]byte(nil), b...), 0)), pnio.RWWriteLength, 0)
		rwCode(write(0, 1, idx, shortBlock(b)), pnio.RWInvalidParameter, 0)
	}
	rwCode(write(0, 1, pnio.IndexIM2, (&pnio.IM2{Date: "tomorrow"}).Append(nil)), pnio.RWInvalidParameter, 0)
	rwCode(write(0, 1, pnio.IndexIM0, make([]byte, 60)), pnio.RWAccessDenied, 0)

	// Identification.
	if b, err := read(0, 0, pnio.IndexIM0Filter); err != nil || len(b) == 0 {
		t.Fatalf("I&M0 filter %x %v", b, err)
	}
	ident := func(slot, subslot uint16, idx pnio.Index) (pnio.IdentificationData, error) {
		b, err := read(slot, subslot, idx)
		if err != nil {
			return pnio.IdentificationData{}, err
		}
		blk, _, err := pnio.ReadBlock(b)
		if err != nil {
			return pnio.IdentificationData{}, err
		}
		return pnio.DecodeIdentificationData(blk)
	}
	id, err := ident(3, 1, pnio.IndexRealIdentAPI)
	if err != nil || len(id.APIs) != 1 || len(id.APIs[0].Slots) != 5 {
		t.Fatalf("real ident API %+v %v", id, err)
	}
	id, err = ident(3, 1, pnio.IndexRealIdentSlot)
	if err != nil || len(id.APIs[0].Slots) != 1 || id.APIs[0].Slots[0].Slot != 3 {
		t.Fatalf("real ident slot %+v %v", id, err)
	}
	id, err = ident(0, 0x8000, pnio.IndexRealIdentSubslot)
	if err != nil || len(id.APIs[0].Slots) != 1 || len(id.APIs[0].Slots[0].Submodules) != 1 || id.APIs[0].Slots[0].Submodules[0].Subslot != 0x8000 {
		t.Fatalf("real ident subslot %+v %v", id, err)
	}
	// The IOSAR expects nothing.
	id, err = ident(0, 1, pnio.IndexExpectedIdentAR)
	if err != nil || !id.Expected || len(id.APIs) != 0 {
		t.Fatalf("expected ident of the IOSAR %+v %v", id, err)
	}
	// Without an AR there is no expected identification nor module diff.
	_, err = p.rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexExpectedIdentAR}, 0)
	rwCode(err, pnio.RWStateConflict, 0)
	_, err = p.rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexModuleDiffAR}, 0)
	rwCode(err, pnio.RWStateConflict, 0)
	b, err := read(0, 1, pnio.IndexModuleDiffAR)
	if err != nil || len(b) == 0 {
		t.Fatalf("module diff %x %v", b, err)
	}

	// LogBook and RecordInputData.
	if b, err := read(0, 0, pnio.IndexLogBook); err != nil || len(b) != 16 {
		t.Fatalf("logbook %x %v", b, err)
	}
	if err := d.SetInput(1, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8}, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	b, err = read(1, 1, pnio.IndexRecordInputData)
	if err != nil || !bytes.HasSuffix(b, []byte{0, 8, 1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("record input %x %v", b, err)
	}
	_, err = read(2, 1, pnio.IndexRecordInputData)
	rwCode(err, pnio.RWInvalidSlot, 0)

	// Modeled records.
	if b, err := read(2, 1, 0x10); err != nil || !bytes.Equal(b, []byte{0, 1}) {
		t.Fatalf("default %x %v", b, err)
	}
	if b, err := read(2, 1, 0x11); err != nil || !bytes.Equal(b, []byte{0}) {
		t.Fatalf("zero default %x %v", b, err)
	}
	if err := write(2, 1, 0x10, []byte{4, 2}); err != nil {
		t.Fatal(err)
	}
	if b, _ := read(2, 1, 0x10); !bytes.Equal(b, []byte{4, 2}) {
		t.Fatalf("after write %x", b)
	}
	rwCode(write(2, 1, 0x10, []byte{9, 9}), pnio.RWInvalidParameter, 1)
	if b, _ := read(2, 1, 0x10); !bytes.Equal(b, []byte{4, 2}) {
		t.Fatalf("after rejected write %x", b)
	}
	rwCode(write(2, 1, 0x10, []byte{1}), pnio.RWWriteLength, 0)
	rwCode(write(2, 1, 0x11, []byte{1}), pnio.RWAccessDenied, 0)
	// A read with a short RecordDataLength is truncated.
	if b, err := p.rc.Read(ctx, ar, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, 1); err != nil || !bytes.Equal(b, []byte{4}) {
		t.Fatalf("truncated %x %v", b, err)
	}

	// User records of the handler.
	if b, err := read(3, 1, 0x7000); err != nil || string(b) != "user 3/1 len 4096" {
		t.Fatalf("user record %q %v", b, err)
	}
	if b, err := p.rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 3, Subslot: 1, Index: 0x7000}, 0); err != nil || string(b) != "user 3/1 len 4096" {
		t.Fatalf("implicit user record %q %v", b, err)
	}
	_, err = read(3, 1, 0x7001)
	rwCode(err, pnio.RWInvalidRange, 0x42)
	_, err = read(3, 1, 0x7002)
	rwCode(err, pnio.RWFeatureNotSupport, 0)
	_, err = read(3, 1, 0x7003)
	rwCode(err, pnio.RWStateConflict, 0)
	_, err = read(3, 1, 0x7004)
	rwCode(err, pnio.RWReadError, 0)
	_, err = read(3, 1, 0x7005)
	rwCode(err, pnio.RWInvalidIndex, 0)
	_, err = read(3, 1, 0x7777)
	rwCode(err, pnio.RWInvalidIndex, 0)
	// An unknown index of the device range goes to the handler too.
	_, err = read(0, 0, 0xFBFF)
	rwCode(err, pnio.RWInvalidIndex, 0)
	if err := write(3, 1, 0x7000, []byte("x")); err != nil {
		t.Fatal(err)
	}
	rwCode(write(3, 1, 0x7004, []byte("x")), pnio.RWWriteError, 0)
	rwCode(write(3, 1, 0x7777, []byte("x")), pnio.RWInvalidIndex, 0)
	rwCode(write(3, 1, pnio.IndexRecordInputData, []byte("x")), pnio.RWInvalidIndex, 0)

	// WriteMultiple.
	sts, err := p.rc.WriteMultiple(ctx, ar, []controller.WriteItem{
		{Record: controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{5, 5}},
		{Record: controller.Record{Slot: 2, Subslot: 1, Index: 0x11}, Data: []byte{5}},
	})
	if err != nil || len(sts) != 2 || !sts[0].OK() || sts[1].Code1 != pnio.RWAccessDenied {
		t.Fatalf("write multiple %v %v", sts, err)
	}
}

func TestRecordsWithIOCAR(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil, func(c *Config) { c.ApplicationReadyTimeout = 20 * time.Second })
	p := n.peer(d, "192.168.0.2", true)
	ctx := context.Background()
	req := p.ioCAR()
	// Slot 3 is expected with another module.
	req.Expected[3].Modules[0].IdentNumber = 0x99
	hold := make(chan struct{})
	p.setOnReady(func(pnio.ControlBlock) []byte {
		<-hold
		return nil
	})
	if _, err := p.connect(req); err != nil {
		t.Fatal(err)
	}
	ar := req.AR.ARUUID
	diff := func() pnio.ModuleDiffBlock {
		t.Helper()
		b, err := p.rc.Read(ctx, ar, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexModuleDiffAR}, 0)
		if err != nil {
			t.Fatal(err)
		}
		blk, _, err := pnio.ReadBlock(b)
		if err != nil {
			t.Fatal(err)
		}
		m, err := pnio.DecodeModuleDiffBlock(blk)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	// While waiting for ApplicationReady every submodule is listed.
	if err := p.prmEnd(req); err != nil {
		t.Fatal(err)
	}
	waitAR(t, d, ar, ARStateAppReadyPending)
	if m := diff(); len(m.APIs) != 1 || len(m.APIs[0].Modules) != 5 {
		t.Fatalf("diff while pending %+v", m)
	}
	close(hold)
	waitAR(t, d, ar, ARStateData)
	// In Data only the wrong module.
	if m := diff(); len(m.APIs) != 1 || len(m.APIs[0].Modules) != 1 || m.APIs[0].Modules[0].Slot != 3 || m.APIs[0].Modules[0].State != pnio.ModuleStateWrong {
		t.Fatalf("diff in Data %+v", m)
	}
	// Expected identification of the AR, explicitly and with
	// ReadImplicit's target AR.
	for _, c := range []struct {
		idx   pnio.Index
		slots int
		subs  int
	}{{pnio.IndexExpectedIdentAR, 5, 3}, {pnio.IndexExpectedIdentSlot, 1, 3}, {pnio.IndexExpectedIdentSubslot, 1, 1}} {
		b, err := p.rc.Read(ctx, ar, controller.Record{Slot: 0, Subslot: 1, Index: c.idx}, 0)
		if err != nil {
			t.Fatal(err)
		}
		blk, _, _ := pnio.ReadBlock(b)
		id, err := pnio.DecodeIdentificationData(blk)
		if err != nil || !id.Expected || len(id.APIs) != 1 || len(id.APIs[0].Slots) != c.slots || len(id.APIs[0].Slots[0].Submodules) != c.subs {
			t.Fatalf("%s: %+v %v", c.idx, id, err)
		}
	}
	q := pnio.ReadReq{Seq: 1, Slot: 3, Subslot: 1, Index: pnio.IndexExpectedIdentSlot, Length: 4096, TargetARUUID: ar}
	out, st := d.read(ctx, true, q.Append(nil))
	if !st.OK() || len(out) == 0 {
		t.Fatalf("implicit read with target AR: %s", st)
	}
	blk, rest, _ := pnio.ReadBlock(out)
	if h, err := pnio.DecodeReadRes(blk); err != nil || int(h.Length) != len(rest) {
		t.Fatalf("read res %+v %v", h, err)
	}
	// An unknown target AR is no AR.
	q.TargetARUUID = rpc.NewRandomUUID()
	if _, st := d.read(ctx, true, q.Append(nil)); st.Code1 != pnio.RWStateConflict {
		t.Fatalf("status %s", st)
	}
}

func TestReadWriteMalformed(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	ctx := context.Background()
	if _, st := d.read(ctx, false, []byte{1}); st.Code != pnio.CodeRead || st.Code2 != pnio.CMRPCArgsLength {
		t.Fatalf("read status %s", st)
	}
	w := pnio.WriteReq{Index: 0x10}
	if _, st := d.read(ctx, false, w.Append(nil)); st.OK() || st.Code != pnio.CodeRead {
		t.Fatalf("read with a write header: %s", st)
	}
	if _, st := d.write(ctx, []byte{1}); st.Code != pnio.CodeWrite || st.Code2 != pnio.CMRPCArgsLength {
		t.Fatalf("write status %s", st)
	}
	r := pnio.ReadReq{Index: 0x10}
	if _, st := d.write(ctx, r.Append(nil)); st.OK() || st.Code != pnio.CodeWrite {
		t.Fatalf("write with a read header: %s", st)
	}
	// RecordDataLength beyond the data present.
	w = pnio.WriteReq{Slot: 2, Subslot: 1, Index: 0x10, Length: 10}
	out, st := d.write(ctx, append(w.Append(nil), 1, 2))
	if st.Code1 != pnio.RWWriteLength || len(out) == 0 {
		t.Fatalf("write status %s", st)
	}
	// A WriteMultiple that does not split.
	w = pnio.WriteReq{ARUUID: rpc.NewRandomUUID(), Index: pnio.IndexWriteMultiple, Length: 3}
	if _, st := d.write(ctx, append(w.Append(nil), 1, 2, 3)); st.Code1 != pnio.Code1CMRPC {
		t.Fatalf("unknown AR first: %s", st)
	}
	d.mu.Lock()
	d.ars[w.ARUUID] = &ar{d: d, uuid: w.ARUUID, typ: pnio.ARTypeIOSAR, state: ARStateData}
	d.mu.Unlock()
	if _, st := d.write(ctx, append(w.Append(nil), 1, 2, 3)); st.Code1 != pnio.RWWriteLength {
		t.Fatalf("write multiple status %s", st)
	}
}

func TestAsStatus(t *testing.T) {
	cases := []struct {
		err   error
		write bool
		want  pnio.Status
	}{
		{nil, false, pnio.Status{}},
		{RecordError(pnio.RWBusy, 3), false, pnio.RWStatus(false, pnio.RWBusy, 3)},
		{RecordError(pnio.RWBusy, 3), true, pnio.RWStatus(true, pnio.RWBusy, 3)},
		{&pnio.StatusError{Status: pnio.ServiceStatus(pnio.CodePNIO, pnio.Code1CMRPC, 5)}, true, pnio.ServiceStatus(pnio.CodeWrite, pnio.Code1CMRPC, 5)},
		{&pnio.StatusError{Status: pnio.ServiceStatus(pnio.CodeConnect, pnio.Code1CMRPC, 5)}, false, pnio.ServiceStatus(pnio.CodeConnect, pnio.Code1CMRPC, 5)},
		{fmt.Errorf("w: %w", profinet.ErrNotFound), true, pnio.RWStatus(true, pnio.RWInvalidIndex, 0)},
		{profinet.ErrUnsupported, false, pnio.RWStatus(false, pnio.RWFeatureNotSupport, 0)},
		{profinet.ErrInvalidState, true, pnio.RWStatus(true, pnio.RWStateConflict, 0)},
		{errors.New("x"), false, pnio.RWStatus(false, pnio.RWReadError, 0)},
		{errors.New("x"), true, pnio.RWStatus(true, pnio.RWWriteError, 0)},
	}
	for i, c := range cases {
		if got := asStatus(c.err, c.write); got != c.want {
			t.Errorf("%d: %s, want %s", i, got, c.want)
		}
	}
}

func TestDiagnosisRecords(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	p := n.peer(d, "192.168.0.2", false)
	ctx := context.Background()
	// Without an AR the diagnoses are recorded, the alarms not sent.
	add := func(slot, subslot uint16, it pnio.DiagnosisItem) {
		t.Helper()
		if err := d.AddDiagnosis(ctx, slot, subslot, it); !errors.Is(err, profinet.ErrInvalidState) {
			t.Fatalf("err %v", err)
		}
	}
	add(1, 1, pnio.DiagnosisItem{ChannelNumber: 1, ChannelProperties: pnio.ChanDirInput, ErrorType: pnio.ChannelErrLineBreak})
	add(3, 1, pnio.DiagnosisItem{ChannelNumber: 2, ErrorType: pnio.ChannelErrOvertemp, ExtErrorType: 0x8001, ExtAddValue: 7})
	add(0, 0x8001, pnio.DiagnosisItem{ChannelNumber: 0x8000, ErrorType: pnio.ChannelErrError})
	add(0, 0x8000, pnio.DiagnosisItem{ChannelNumber: 0x8000, ErrorType: pnio.ChannelErrError})
	add(0, 0x8000, pnio.DiagnosisItem{ChannelNumber: 1, ErrorType: pnio.ChannelErrOverload})
	if err := d.AddDiagnosis(ctx, 9, 1, pnio.DiagnosisItem{}); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if got := d.Diagnoses(1, 1); len(got) != 1 || !got[0].Appears() {
		t.Fatalf("diagnoses %+v", got)
	}
	count := func(slot, subslot uint16, idx pnio.Index, sub bool) []pnio.DiagnosisData {
		t.Helper()
		var dd []pnio.DiagnosisData
		var err error
		if idx == 0 {
			dd, err = p.rc.ReadDiagnosis(ctx, rpc.NilUUID, slot, subslot, sub)
		} else {
			var b []byte
			b, err = p.rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: slot, Subslot: subslot, Index: idx}, 0)
			if err == nil {
				blks, _ := pnio.SplitBlocks(b)
				for _, blk := range blks {
					x, err := pnio.DecodeDiagnosisData(blk)
					if err != nil {
						t.Fatal(err)
					}
					dd = append(dd, x)
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		return dd
	}
	if dd := count(0, 0, 0, false); len(dd) != 4 || dd[0].Subslot != 0x8000 || len(dd[0].Items) != 2 || dd[1].Subslot != 0x8001 || dd[2].Slot != 1 || dd[3].Slot != 3 || dd[3].USI != pnio.USIExtChannelDiagnosis || dd[2].USI != pnio.USIChannelDiagnosis {
		t.Fatalf("device %+v", dd)
	}
	if dd := count(1, 1, 0, true); len(dd) != 1 || dd[0].Slot != 1 {
		t.Fatalf("subslot %+v", dd)
	}
	if dd := count(2, 1, pnio.IndexDiagChannelSubslot, false); len(dd) != 0 {
		t.Fatalf("empty subslot %+v", dd)
	}
	for _, idx := range []pnio.Index{pnio.IndexDiagChannelSlot, pnio.IndexDiagAllSlot, pnio.IndexDiagDMQSSlot} {
		if dd := count(3, 1, idx, false); len(dd) != 1 || dd[0].Slot != 3 {
			t.Fatalf("%s %+v", idx, dd)
		}
	}
	for _, idx := range []pnio.Index{pnio.IndexDiagChannelAR, pnio.IndexDiagAllAR, pnio.IndexDiagDMQSAR, pnio.IndexDiagChannelAPI, pnio.IndexDiagAllAPI, pnio.IndexDiagDMQSAPI} {
		if dd := count(0, 0, idx, false); len(dd) != 4 {
			t.Fatalf("%s %+v", idx, dd)
		}
	}
	if err := d.RemoveDiagnosis(ctx, 1, 1, 1, pnio.ChannelErrLineBreak); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	if err := d.RemoveDiagnosis(ctx, 1, 1, 1, pnio.ChannelErrLineBreak); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	// One of two diagnoses of a subslot.
	if err := d.RemoveDiagnosis(ctx, 0, 0x8000, 1, pnio.ChannelErrOverload); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	if got := d.Diagnoses(0, 0x8000); len(got) != 1 || got[0].ChannelNumber != 0x8000 {
		t.Fatalf("remaining %+v", got)
	}
	if dd := count(0, 0, 0, false); len(dd) != 3 {
		t.Fatalf("after removal %+v", dd)
	}
}
