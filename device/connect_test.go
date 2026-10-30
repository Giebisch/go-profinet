// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

func TestConnectIOCRValidation(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	p := n.peer(d, "192.168.0.2", true)
	both := func(f func(c *pnio.IOCRBlockReq)) func(r *pnio.ConnectRequest) {
		return func(r *pnio.ConnectRequest) {
			for i := range r.IOCRs {
				f(&r.IOCRs[i])
			}
		}
	}
	in := func(f func(c *pnio.IOCRBlockReq)) func(r *pnio.ConnectRequest) {
		return func(r *pnio.ConnectRequest) { f(&r.IOCRs[0]) }
	}
	out := func(f func(c *pnio.IOCRBlockReq)) func(r *pnio.ConnectRequest) {
		return func(r *pnio.ConnectRequest) { f(&r.IOCRs[1]) }
	}
	cases := []struct {
		name  string
		mod   func(r *pnio.ConnectRequest)
		code1 uint8
		code2 uint8
	}{
		{"IOCR type", in(func(c *pnio.IOCRBlockReq) { c.Type = pnio.IOCRMCProvider }), pnio.Code1IOCRBlock, 4},
		{"LT", in(func(c *pnio.IOCRBlockReq) { c.LT = 0x0800 }), pnio.Code1IOCRBlock, 6},
		{"RT class 3", both(func(c *pnio.IOCRBlockReq) { c.Properties = uint32(pnio.RTClass3) }), pnio.Code1IOCRBlock, 7},
		{"RT class UDP", both(func(c *pnio.IOCRBlockReq) { c.Properties = uint32(pnio.RTClassUDP) }), pnio.Code1IOCRBlock, 7},
		// A complete RT_CLASS_UDP request as p-net would validate it (LT
		// 0x0800, a C_SDU below 40 octets, no initiator MAC address, alarms
		// over UDP): refused for its IOCRProperties, never accepted.
		{"RT_CLASS_UDP connect", func(r *pnio.ConnectRequest) {
			for i := range r.IOCRs {
				r.IOCRs[i].Properties, r.IOCRs[i].LT, r.IOCRs[i].DataLength = uint32(pnio.RTClassUDP), pnio.LTIP, 12
			}
			r.AR.InitiatorMAC, r.AR.UDPRTPort = ethernet.MAC{}, 0x8000
			r.AlarmCR.Properties, r.AlarmCR.LT = pnio.AlarmCRPropTransportUDP, pnio.LTIP
		}, pnio.Code1IOCRBlock, 7},
		{"DataLength short", in(func(c *pnio.IOCRBlockReq) { c.DataLength = 39 }), pnio.Code1IOCRBlock, 8},
		{"DataLength long", in(func(c *pnio.IOCRBlockReq) { c.DataLength = 1441 }), pnio.Code1IOCRBlock, 8},
		{"input FrameID of class 2", in(func(c *pnio.IOCRBlockReq) { c.FrameID = 0x8000 }), pnio.Code1IOCRBlock, 9},
		{"input FrameID assign", in(func(c *pnio.IOCRBlockReq) { c.FrameID = pnio.FrameIDAssign }), pnio.Code1IOCRBlock, 9},
		{"output FrameID out of range", out(func(c *pnio.IOCRBlockReq) { c.FrameID = 0x0100 }), pnio.Code1IOCRBlock, 9},
		{"SCF zero", in(func(c *pnio.IOCRBlockReq) { c.SendClockFactor = 0 }), pnio.Code1IOCRBlock, 10},
		{"SCF too big", in(func(c *pnio.IOCRBlockReq) { c.SendClockFactor = 256 }), pnio.Code1IOCRBlock, 10},
		{"SCF not a power of two", in(func(c *pnio.IOCRBlockReq) { c.SendClockFactor = 48 }), pnio.Code1IOCRBlock, 10},
		{"cycle below MinDeviceInterval", in(func(c *pnio.IOCRBlockReq) { c.SendClockFactor, c.ReductionRatio = 8, 2 }), pnio.Code1IOCRBlock, 11},
		{"RR zero", in(func(c *pnio.IOCRBlockReq) { c.ReductionRatio = 0 }), pnio.Code1IOCRBlock, 11},
		{"RR not a power of two", in(func(c *pnio.IOCRBlockReq) { c.ReductionRatio = 3 }), pnio.Code1IOCRBlock, 11},
		{"RR too big", in(func(c *pnio.IOCRBlockReq) { c.ReductionRatio = 1024 }), pnio.Code1IOCRBlock, 11},
		{"RR 256 with SCF 128", in(func(c *pnio.IOCRBlockReq) { c.SendClockFactor, c.ReductionRatio = 128, 256 }), pnio.Code1IOCRBlock, 11},
		{"RR 512 with SCF 64", in(func(c *pnio.IOCRBlockReq) { c.SendClockFactor, c.ReductionRatio = 64, 512 }), pnio.Code1IOCRBlock, 11},
		{"phase zero", in(func(c *pnio.IOCRBlockReq) { c.Phase = 0 }), pnio.Code1IOCRBlock, 12},
		{"phase beyond RR", in(func(c *pnio.IOCRBlockReq) { c.Phase = 129 }), pnio.Code1IOCRBlock, 12},
		{"watchdog zero", in(func(c *pnio.IOCRBlockReq) { c.WatchdogFactor = 0 }), pnio.Code1IOCRBlock, 15},
		{"watchdog too big", in(func(c *pnio.IOCRBlockReq) { c.WatchdogFactor = 0x1E01 }), pnio.Code1IOCRBlock, 15},
		{"data hold zero", in(func(c *pnio.IOCRBlockReq) { c.DataHoldFactor = 0 }), pnio.Code1IOCRBlock, 16},
		{"data hold too big", in(func(c *pnio.IOCRBlockReq) { c.DataHoldFactor = 0x1E01 }), pnio.Code1IOCRBlock, 16},
		{"multicast MAC", in(func(c *pnio.IOCRBlockReq) { c.MulticastMAC = ethernet.MAC{1, 0x0E, 0xCF, 0, 0, 1} }), pnio.Code1IOCRBlock, 18},
		{"two input IOCRs", func(r *pnio.ConnectRequest) { r.IOCRs[1] = r.IOCRs[0] }, pnio.Code1IOCRBlock, 4},
		{"missing output IOCR", func(r *pnio.ConnectRequest) { r.IOCRs = r.IOCRs[:1] }, pnio.Code1CMRPC, pnio.CMRPCIOCRMissing},
		{"missing AlarmCR", func(r *pnio.ConnectRequest) { r.AlarmCR = nil }, pnio.Code1CMRPC, pnio.CMRPCAlarmCRCount},
		{"no expected submodules", func(r *pnio.ConnectRequest) { r.Expected = nil }, pnio.Code1ExpSubm, 1},
		{"SCF mismatch", out(func(c *pnio.IOCRBlockReq) { c.SendClockFactor = 64 }), pnio.Code1IOCRBlock, 10},
		{"RT class mismatch", out(func(c *pnio.IOCRBlockReq) { c.Properties = uint32(pnio.RTClass2) }), pnio.Code1IOCRBlock, 10},
		{"input layout beyond DataLength", in(func(c *pnio.IOCRBlockReq) { c.APIs[0].IODataObjects[0].FrameOffset = c.DataLength }), pnio.Code1IOCRBlock, 19},
		{"output layout beyond DataLength", out(func(c *pnio.IOCRBlockReq) { c.APIs[0].IOCS[0].FrameOffset = c.DataLength }), pnio.Code1IOCRBlock, 19},
		{"layout for unknown submodule", func(r *pnio.ConnectRequest) { r.Expected = r.Expected[:1] }, pnio.Code1IOCRBlock, 19},
		// The AlarmCR block.
		{"AlarmCR type", func(r *pnio.ConnectRequest) { r.AlarmCR.Type = 2 }, pnio.Code1AlarmCR, 4},
		{"AlarmCR LT", func(r *pnio.ConnectRequest) { r.AlarmCR.LT = 0x0800 }, pnio.Code1AlarmCR, 5},
		{"AlarmCR over UDP", func(r *pnio.ConnectRequest) { r.AlarmCR.Properties = 2 }, pnio.Code1AlarmCR, 6},
		{"AlarmCR over UDP with LT 0x0800", func(r *pnio.ConnectRequest) {
			r.AlarmCR.Properties, r.AlarmCR.LT = pnio.AlarmCRPropTransportUDP, pnio.LTIP
		}, pnio.Code1AlarmCR, 6},
		{"RTA timeout zero", func(r *pnio.ConnectRequest) { r.AlarmCR.RTATimeoutFactor = 0 }, pnio.Code1AlarmCR, 7},
		{"RTA timeout too big", func(r *pnio.ConnectRequest) { r.AlarmCR.RTATimeoutFactor = 101 }, pnio.Code1AlarmCR, 7},
		{"RTA retries few", func(r *pnio.ConnectRequest) { r.AlarmCR.RTARetries = 2 }, pnio.Code1AlarmCR, 8},
		{"RTA retries many", func(r *pnio.ConnectRequest) { r.AlarmCR.RTARetries = 16 }, pnio.Code1AlarmCR, 8},
		{"alarm data short", func(r *pnio.ConnectRequest) { r.AlarmCR.MaxAlarmDataLength = 199 }, pnio.Code1AlarmCR, 10},
		{"alarm data long", func(r *pnio.ConnectRequest) { r.AlarmCR.MaxAlarmDataLength = 1433 }, pnio.Code1AlarmCR, 10},
		// The AR block.
		{"AR type", func(r *pnio.ConnectRequest) { r.AR.Type = 0x0077 }, pnio.Code1ARBlock, 4},
		{"initiator object", func(r *pnio.ConnectRequest) { r.AR.InitiatorObject = rpc.NewRandomUUID() }, pnio.Code1ARBlock, 8},
		{"activity timeout zero", func(r *pnio.ConnectRequest) { r.AR.ActivityTimeoutFactor = 0 }, pnio.Code1ARBlock, 10},
		{"activity timeout too big", func(r *pnio.ConnectRequest) { r.AR.ActivityTimeoutFactor = 1001 }, pnio.Code1ARBlock, 10},
		{"nil AR UUID", func(r *pnio.ConnectRequest) { r.AR.ARUUID = rpc.NilUUID }, pnio.Code1ARBlock, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := p.ioCAR()
			c.mod(req)
			_, err := p.connect(req)
			wantStatus(t, err, c.code1, c.code2)
			var se *pnio.StatusError
			errors.As(err, &se)
			if se.Status.Code != pnio.CodeConnect || se.Status.Decode != pnio.DecodePNIO {
				t.Fatalf("status %s", se.Status)
			}
		})
	}
	if ars := d.ARs(); len(ars) != 0 {
		t.Fatalf("ARs after rejections: %+v", ars)
	}
}

// A MinDeviceInterval above the largest send clock still admits the cycles
// that reach it.
func TestConnectMinDeviceInterval128ms(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil, func(c *Config) { c.Model.Capabilities.MinDeviceInterval = 4096 })
	p := n.peer(d, "192.168.0.2", true)

	req := p.ioCAR()
	for i := range req.IOCRs {
		req.IOCRs[i].ReductionRatio = 64
	}
	_, err := p.connect(req)
	wantStatus(t, err, pnio.Code1IOCRBlock, 11)

	if _, err := p.connect(p.ioCAR()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectAcceptedVariants(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	p := n.peer(d, "192.168.0.2", true)
	cases := []struct {
		name string
		mod  func(r *pnio.ConnectRequest)
		inID uint16
		out  uint16 // the expected output FrameID; 0: any of class 1
	}{
		{"RT class 2", func(r *pnio.ConnectRequest) {
			r.IOCRs[0].Properties, r.IOCRs[1].Properties = uint32(pnio.RTClass2), uint32(pnio.RTClass2)
			r.IOCRs[0].FrameID = 0x8001
		}, 0x8001, 0x8000},
		{"explicit output FrameID", func(r *pnio.ConnectRequest) { r.IOCRs[1].FrameID = 0xC123 }, 0xC000, 0xC123},
		{"RR 256 with SCF 64", func(r *pnio.ConnectRequest) {
			for i := range r.IOCRs {
				r.IOCRs[i].SendClockFactor, r.IOCRs[i].ReductionRatio = 64, 256
			}
		}, 0xC000, 0},
		{"RR 512 with SCF 32", func(r *pnio.ConnectRequest) {
			for i := range r.IOCRs {
				r.IOCRs[i].ReductionRatio = 512
			}
		}, 0xC000, 0},
		{"SCF below MinDeviceInterval, cycle above", func(r *pnio.ConnectRequest) {
			for i := range r.IOCRs {
				r.IOCRs[i].SendClockFactor = 16
			}
		}, 0xC000, 0},
		{"ARRPC block", func(r *pnio.ConnectRequest) { r.ARRPC = &pnio.ARRPCBlockReq{InitiatorRPCServerPort: rpc.Port} }, 0xC000, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := p.ioCAR()
			c.mod(req)
			resp, err := p.connect(req)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.IOCRs) != 2 || resp.IOCRs[0].FrameID != c.inID {
				t.Fatalf("IOCRs %+v", resp.IOCRs)
			}
			if c.out != 0 && resp.IOCRs[1].FrameID != c.out {
				t.Fatalf("output FrameID 0x%04X, want 0x%04X", resp.IOCRs[1].FrameID, c.out)
			}
			if req.ARRPC != nil && (resp.ARRPC == nil || resp.ARRPC.InitiatorRPCServerPort != rpc.Port) {
				t.Fatalf("ARRPC %+v", resp.ARRPC)
			}
			if resp.AlarmCR == nil || resp.AlarmCR.MaxAlarmDataLength != 200 || resp.ARServer == nil || resp.ARServer.StationName != "dut" {
				t.Fatalf("response %+v", resp)
			}
			if resp.ModuleDiff == nil {
				t.Fatal("no ModuleDiff while waiting for ApplicationReady")
			}
			ars := d.ARs()
			if len(ars) != 1 || ars[0].InputFrameID != c.inID || ars[0].OutputFrameID != resp.IOCRs[1].FrameID || ars[0].ControllerName != "plc" {
				t.Fatalf("ARs %+v", ars)
			}
			if err := p.release(req); err != nil {
				t.Fatal(err)
			}
			waitAR(t, d, req.AR.ARUUID, ARStateClosed)
		})
	}
}

func TestConnectARLimits(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	// Two IOSARs exhaust MaxARs 2; a third is refused, an IOCAR too.
	s1, s2 := p.ioSAR(), p.ioSAR()
	for _, r := range []*pnio.ConnectRequest{s1, s2} {
		if _, err := p.connect(r); err != nil {
			t.Fatal(err)
		}
	}
	_, err := p.connect(p.ioSAR())
	wantStatus(t, err, pnio.Code1CMRPC, pnio.CMRPCOutOfARs)
	_, err = p.connect(p.ioCAR())
	wantStatus(t, err, pnio.Code1CMRPC, pnio.CMRPCOutOfARs)
	if !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Fatalf("err %v", err)
	}
	// A rerun of an existing AR replaces it.
	if _, err := p.connect(s1); err != nil {
		t.Fatal(err)
	}
	h.waitReason(t, s1.AR.ARUUID, pnio.AbortRerun)
	if st, ok := arState(d, s1.AR.ARUUID); !ok || st != ARStateData {
		t.Fatalf("rerun AR %v %v", st, ok)
	}
	// IOSARs end with a release.
	for _, r := range []*pnio.ConnectRequest{s1, s2} {
		if err := p.release(r); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "no ARs", func() bool { return len(d.ARs()) == 0 })

	// A second controller's IOCAR is accepted (shared device), with
	// every submodule locked: the first owns them. A third exceeds MaxARs.
	c1 := p.ioCAR()
	p.establish(c1)
	c2 := p.ioCAR()
	c2.AR.InitiatorObject[11] ^= 0xFF // another controller: the same one would replace c1
	resp, err := p.connect(c2)
	if err != nil {
		t.Fatal(err)
	}
	for _, api := range resp.ModuleDiff.APIs {
		for _, m := range api.Modules {
			for _, sm := range m.Submodules {
				if sm.State&(0xF<<7) != pnio.SubmStateARInfoCtrlLock {
					t.Fatalf("slot %d subslot %d state %#x, want locked by IO controller", m.Slot, sm.Subslot, sm.State)
				}
			}
		}
	}
	c3 := p.ioCAR()
	c3.AR.InitiatorObject[11] ^= 0x0F
	_, err = p.connect(c3)
	wantStatus(t, err, pnio.Code1CMRPC, pnio.CMRPCOutOfARs)
	if err := p.release(c2); err != nil {
		t.Fatal(err)
	}
	waitAR(t, d, c2.AR.ARUUID, ARStateClosed)
	// A rerun of the IOCAR aborts the old one.
	if _, err := p.connect(c1); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "rerun states", func() bool { return len(h.states(c1.AR.ARUUID)) >= 6 })
	if got := h.states(c1.AR.ARUUID); got[4] != ARStateClosed || got[5] != ARStateParameterizing {
		t.Fatalf("states %v", got)
	}
	if r, _ := h.reason(c1.AR.ARUUID); r != pnio.AbortRerun {
		t.Fatalf("reason %s", r)
	}
}

func TestConnectRejectSupervisor(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil, func(c *Config) { c.RejectSupervisorAR = true })
	p := n.peer(d, "192.168.0.2", false)
	_, err := p.connect(p.ioSAR())
	wantStatus(t, err, pnio.Code1ARBlock, 4)
}

// slowDataHandler holds back its report of the first transition to Data
// until release is closed.
type slowDataHandler struct {
	*recHandler
	once    sync.Once
	inData  chan struct{}
	release chan struct{}
}

func (h *slowDataHandler) StateChanged(ar ARInfo, from, to ARState) {
	if to == ARStateData {
		h.once.Do(func() {
			close(h.inData)
			<-h.release
		})
	}
	h.recHandler.StateChanged(ar, from, to)
}

// TestStateChangedInOrder: a transition caused while the handler is still
// busy with the previous one is reported after it. Here PrmBegin arrives
// while the handler is reporting Data; the handler must see Data before
// Parameterizing, not after.
func TestStateChangedInOrder(t *testing.T) {
	n := newNet(t)
	h := &slowDataHandler{recHandler: newRecHandler(), inData: make(chan struct{}), release: make(chan struct{})}
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	req := p.ioCAR()
	_ = p.establish(req)
	select {
	case <-h.inData:
	case <-time.After(5 * time.Second):
		t.Fatal("no transition to Data")
	}
	if err := p.control(req, pnio.BlockPrmBeginReq, pnio.CmdPrmBegin); err != nil {
		close(h.release)
		t.Fatal(err)
	}
	close(h.release)
	want := []ARState{ARStateNone, ARStateParameterizing, ARStateAppReadyPending, ARStateData, ARStateParameterizing}
	eventually(t, 2*time.Second, "state sequence", func() bool { return len(h.states(req.AR.ARUUID)) == len(want) })
	if got := h.states(req.AR.ARUUID); !slices.Equal(got, want) {
		t.Fatalf("states %v, want %v", got, want)
	}
}

func TestConnectStateSequence(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	req := p.ioCAR()
	resp := p.establish(req)
	if resp.ModuleDiff == nil {
		t.Fatal("no ModuleDiff")
	}
	if p.readyCnt.Load() != 1 {
		t.Fatalf("%d ApplicationReady calls", p.readyCnt.Load())
	}
	ars := d.ARs()
	if len(ars) != 1 || ars[0].Type != pnio.ARTypeIOCARSingle || ars[0].SessionKey != 7 || ars[0].ControllerMAC != p.mux.HardwareAddr() || ars[0].CycleNanos != int64(128*time.Millisecond) {
		t.Fatalf("ARs %+v", ars)
	}
	// PrmEnd twice is a state conflict.
	err := p.prmEnd(req)
	wantStatus(t, err, pnio.Code1CMDEV, 0)
	// PrmBegin goes back to Parameterizing, PrmEnd to Data again.
	if err := p.control(req, pnio.BlockPrmBeginReq, pnio.CmdPrmBegin); err != nil {
		t.Fatal(err)
	}
	waitAR(t, d, req.AR.ARUUID, ARStateParameterizing)
	// PrmBegin outside Data is refused.
	wantStatus(t, p.control(req, pnio.BlockPrmBeginReq, pnio.CmdPrmBegin), pnio.Code1CMDEV, 0)
	if err := p.prmEnd(req); err != nil {
		t.Fatal(err)
	}
	waitAR(t, d, req.AR.ARUUID, ARStateData)
	// A wrong session key, an unknown AR and an unknown control block.
	bad := *req
	bad.AR.SessionKey++
	wantStatus(t, p.release(&bad), pnio.Code1CMRPC, pnio.CMRPCARUUIDUnknown)
	bad = *req
	bad.AR.ARUUID = rpc.NewRandomUUID()
	wantStatus(t, p.prmEnd(&bad), pnio.Code1CMRPC, pnio.CMRPCARUUIDUnknown)
	wantStatus(t, p.control(req, pnio.BlockPrmEndReq, pnio.CmdRelease), pnio.Code1PrmEnd, 8)
	if err := p.release(req); err != nil {
		t.Fatal(err)
	}
	waitAR(t, d, req.AR.ARUUID, ARStateClosed)
	want := []ARState{ARStateNone, ARStateParameterizing, ARStateAppReadyPending, ARStateData,
		ARStateParameterizing, ARStateAppReadyPending, ARStateData, ARStateClosed}
	eventually(t, 2*time.Second, "state sequence", func() bool { return len(h.states(req.AR.ARUUID)) == len(want) })
	got := h.states(req.AR.ARUUID)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("states %v, want %v", got, want)
		}
	}
	if r, _ := h.reason(req.AR.ARUUID); r != 0 {
		t.Fatalf("release reason %s", r)
	}
	// After the release the outputs are invalid.
	if _, iops, ok := d.Output(2, 1); !ok || iops != pnio.IOxSBad {
		t.Fatalf("output IOPS %02X %v", iops, ok)
	}
}

func TestPrmBeginIOSAR(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	p := n.peer(d, "192.168.0.2", false)
	req := p.ioSAR()
	// An IOSAR with an alarm CR.
	cr := p.ioCAR().AlarmCR
	req.AlarmCR = cr
	resp, err := p.connect(req)
	if err != nil || resp.AlarmCR == nil {
		t.Fatalf("%+v %v", resp, err)
	}
	wantStatus(t, p.control(req, pnio.BlockPrmBeginReq, pnio.CmdPrmBegin), pnio.Code1CMDEV, 0)
	wantStatus(t, p.prmEnd(req), pnio.Code1CMDEV, 0)
	// Alarms do not go to IOSARs.
	if err := d.SendAlarm(context.Background(), Alarm{Type: pnio.AlarmProcess, Slot: 1, Subslot: 1}); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
}

func TestConnectResources(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	p := n.peer(d, "192.168.0.2", true)
	// Every FrameID of class 1 in use.
	d.mu.Lock()
	for id := uint32(pnio.FrameIDRTC1Min); id <= uint32(pnio.FrameIDRTC1Max); id++ {
		d.frameIDs[uint16(id)] = true
	}
	d.mu.Unlock()
	_, err := p.connect(p.ioCAR())
	wantStatus(t, err, pnio.Code1CMRPC, pnio.CMRPCOutOfMemory)
	d.mu.Lock()
	d.frameIDs = map[uint16]bool{0xC123: true}
	d.mu.Unlock()
	// An explicit FrameID in use is replaced by a free one.
	req := p.ioCAR()
	req.IOCRs[1].FrameID = 0xC123
	resp, err := p.connect(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.IOCRs[1].FrameID != 0xC000 {
		t.Fatalf("output FrameID 0x%04X", resp.IOCRs[1].FrameID)
	}
	// A closed device refuses connections.
	_ = d.Close()
	_, err = d.connect(context.Background(), &net.UDPAddr{}, p.ioCAR())
	wantStatus(t, err, pnio.Code1CMRPC, pnio.CMRPCStateConflict)
	if d.Close() != nil {
		t.Fatal("second Close")
	}
}

func TestConnectMalformed(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	p := n.peer(d, "192.168.0.2", false)
	ctx := context.Background()
	call := func(op pnio.OpNum, body []byte, obj rpc.UUID) (*rpc.Response, error) {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return p.cli.Do(cctx, &net.UDPAddr{IP: net.ParseIP(devIP).To4(), Port: rpc.Port}, rpc.Call{Object: obj, Iface: pnio.DeviceInterface, IfaceVersion: pnio.InterfaceVersion, OpNum: uint16(op), Body: body})
	}
	status := func(r *rpc.Response) pnio.Status {
		t.Helper()
		st, _, err := pnio.DecodeNDRResponse(r.Body, r.Header.Order())
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	// A short NDR header.
	r, err := call(pnio.OpConnect, []byte{1, 2, 3}, d.ObjectUUID())
	if err != nil {
		t.Fatal(err)
	}
	if st := status(r); st.Code != pnio.CodeConnect || st.Code1 != pnio.Code1CMRPC || st.Code2 != pnio.CMRPCArgsLength {
		t.Fatalf("status %s", st)
	}
	// Garbage blocks in a valid NDR header.
	body := pnio.AppendNDRRequest(nil, rpcOrder, pnio.DefaultArgsMaximum, []byte{0x01, 0x01, 0x00, 0x02, 1, 0})
	r, err = call(pnio.OpConnect, body, rpc.NilUUID)
	if err != nil {
		t.Fatal(err)
	}
	if st := status(r); st.OK() || st.Code != pnio.CodeConnect {
		t.Fatalf("status %s", st)
	}
	// An unknown operation and a foreign object UUID are rejected.
	var f *rpc.Fault
	if _, err := call(pnio.OpNum(9), pnio.AppendNDRRequest(nil, rpcOrder, pnio.DefaultArgsMaximum, nil), d.ObjectUUID()); !errors.As(err, &f) || !f.Reject || f.Status != rpc.StatusOpRangeError {
		t.Fatalf("err %v", err)
	}
	if _, err := call(pnio.OpRead, pnio.AppendNDRRequest(nil, rpcOrder, pnio.DefaultArgsMaximum, nil), rpc.NewRandomUUID()); !errors.As(err, &f) || f.Status != rpc.StatusUnknownIf {
		t.Fatalf("err %v", err)
	}
	// Malformed control blocks.
	r, err = call(pnio.OpControl, pnio.AppendNDRRequest(nil, rpcOrder, pnio.DefaultArgsMaximum, []byte{1}), d.ObjectUUID())
	if err != nil {
		t.Fatal(err)
	}
	if st := status(r); st.Code != pnio.CodeControl || st.Code2 != pnio.CMRPCArgsLength {
		t.Fatalf("status %s", st)
	}
	other := pnio.ControlBlock{Type: pnio.BlockType(0x0019)}
	r, err = call(pnio.OpRelease, pnio.AppendNDRRequest(nil, rpcOrder, pnio.DefaultArgsMaximum, other.Append(nil)), d.ObjectUUID())
	if err != nil {
		t.Fatal(err)
	}
	if st := status(r); st.Code != pnio.CodeRelease || st.Code1 != pnio.Code1CMRPC || st.Code2 != pnio.CMRPCStateConflict {
		t.Fatalf("status %s", st)
	}
	// Short NDR headers of the record services.
	for _, c := range []struct {
		op   pnio.OpNum
		code uint8
	}{{pnio.OpRead, pnio.CodeRead}, {pnio.OpReadImplicit, pnio.CodeRead}, {pnio.OpWrite, pnio.CodeWrite}, {pnio.OpRelease, pnio.CodeRelease}, {pnio.OpControl, pnio.CodeControl}} {
		r, err := call(c.op, []byte{1}, d.ObjectUUID())
		if err != nil {
			t.Fatal(err)
		}
		if st := status(r); st.Code != c.code || st.Code2 != pnio.CMRPCArgsLength {
			t.Fatalf("%s: status %s", c.op, st)
		}
	}
	// Status codes of unknown operations.
	if serviceCode(pnio.OpNum(9)) != pnio.CodePNIO {
		t.Fatal("service code")
	}
}

func TestPrmEndFailures(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)

	// The application refuses.
	h.mu.Lock()
	h.prmEnd = func(context.Context, ARInfo) error { return errors.New("not ready") }
	h.mu.Unlock()
	req := p.ioCAR()
	if _, err := p.connect(req); err != nil {
		t.Fatal(err)
	}
	if err := p.prmEnd(req); err != nil {
		t.Fatal(err)
	}
	h.waitReason(t, req.AR.ARUUID, pnio.AbortUser)

	// The AR is released while the application gets ready.
	release := make(chan struct{})
	h.mu.Lock()
	h.prmEnd = func(ctx context.Context, _ ARInfo) error {
		<-release
		return nil
	}
	h.mu.Unlock()
	req = p.ioCAR()
	if _, err := p.connect(req); err != nil {
		t.Fatal(err)
	}
	if err := p.prmEnd(req); err != nil {
		t.Fatal(err)
	}
	if err := p.release(req); err != nil {
		t.Fatal(err)
	}
	h.waitReason(t, req.AR.ARUUID, 0)
	close(release)
	h.mu.Lock()
	h.prmEnd = nil
	h.mu.Unlock()

	// ApplicationReady answers that fail the AR.
	ok := func(cb pnio.ControlBlock) pnio.ControlBlock { return cb.Response() }
	answers := []struct {
		name string
		body func(cb pnio.ControlBlock) []byte
	}{
		{"error status", func(pnio.ControlBlock) []byte {
			return pnio.AppendNDRResponse(nil, rpcOrder, pnio.ServiceStatus(pnio.CodeControl, pnio.Code1CMRPC, pnio.CMRPCARUUIDUnknown), 0, nil)
		}},
		{"malformed NDR", func(pnio.ControlBlock) []byte { return []byte{0, 0, 0, 0, 1} }},
		{"no block", func(pnio.ControlBlock) []byte { return pnio.AppendNDRResponse(nil, rpcOrder, pnio.Status{}, 0, nil) }},
		{"not a control block", func(pnio.ControlBlock) []byte {
			other := pnio.ControlBlock{Type: pnio.BlockType(0x0019)}
			return pnio.AppendNDRResponse(nil, rpcOrder, pnio.Status{}, 0, other.Append(nil))
		}},
		{"not done", func(cb pnio.ControlBlock) []byte {
			r := ok(cb)
			r.Command = pnio.CmdApplicationReady
			return pnio.AppendNDRResponse(nil, rpcOrder, pnio.Status{}, 0, r.Append(nil))
		}},
		{"other AR", func(cb pnio.ControlBlock) []byte {
			r := ok(cb)
			r.ARUUID = rpc.NewRandomUUID()
			return pnio.AppendNDRResponse(nil, rpcOrder, pnio.Status{}, 0, r.Append(nil))
		}},
		{"no answer", func(pnio.ControlBlock) []byte {
			time.Sleep(2500 * time.Millisecond)
			return nil
		}},
	}
	for _, a := range answers {
		t.Run(a.name, func(t *testing.T) {
			p.setOnReady(a.body)
			req := p.ioCAR()
			if _, err := p.connect(req); err != nil {
				t.Fatal(err)
			}
			if err := p.prmEnd(req); err != nil {
				t.Fatal(err)
			}
			h.waitReason(t, req.AR.ARUUID, pnio.AbortRPCClientCall)
			// Let a late answer go before the next AR.
			eventually(t, 3*time.Second, "AR gone", func() bool { return len(d.ARs()) == 0 })
		})
	}
	p.setOnReady(nil)

	// An ApplicationReady that fails after the AR was released.
	hold := make(chan struct{})
	p.setOnReady(func(pnio.ControlBlock) []byte {
		<-hold
		return pnio.AppendNDRResponse(nil, rpcOrder, pnio.ServiceStatus(pnio.CodeControl, pnio.Code1CMRPC, 5), 0, nil)
	})
	req = p.ioCAR()
	if _, err := p.connect(req); err != nil {
		t.Fatal(err)
	}
	calls := p.readyCnt.Load()
	if err := p.prmEnd(req); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "ApplicationReady call", func() bool { return p.readyCnt.Load() > calls })
	if err := p.release(req); err != nil {
		t.Fatal(err)
	}
	h.waitReason(t, req.AR.ARUUID, 0)
	close(hold)
	p.setOnReady(nil)

	// A controller address that is not UDP.
	a := &ar{d: d, ctrlAddr: fakeAddr{}}
	if err := d.applicationReady(context.Background(), a); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
}

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake" }

func TestTimeouts(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	// No PrmEnd within the CM activity timeout.
	req := p.ioCAR()
	req.AR.ActivityTimeoutFactor = 2
	if _, err := p.connect(req); err != nil {
		t.Fatal(err)
	}
	h.waitReason(t, req.AR.ARUUID, pnio.AbortCMITimeout)
	// No output frames within the data hold time once the AR is
	// established (before that the startup grace applies). 10 cycles of
	// 32 ms: long enough for the AR to reach Data on a stalled runner.
	req = p.ioCAR()
	for i := range req.IOCRs {
		req.IOCRs[i].ReductionRatio, req.IOCRs[i].DataHoldFactor = 32, 10
	}
	p.establish(req)
	h.waitReason(t, req.AR.ARUUID, pnio.AbortConsumerDHT)
	// A released IOSAR stops its idle watch.
	s := p.ioSAR()
	s.AR.ActivityTimeoutFactor = 4
	if _, err := p.connect(s); err != nil {
		t.Fatal(err)
	}
	if err := p.release(s); err != nil {
		t.Fatal(err)
	}
	h.waitReason(t, s.AR.ARUUID, 0)
	time.Sleep(250 * time.Millisecond)
	// An idle IOSAR is released.
	s = p.ioSAR()
	s.AR.ActivityTimeoutFactor = 2
	if _, err := p.connect(s); err != nil {
		t.Fatal(err)
	}
	h.waitReason(t, s.AR.ARUUID, pnio.AbortCMITimeout)
}

func TestAbortByController(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	req := p.ioCAR()
	resp := p.establish(req)
	_, lo := p.openAlarms(req, resp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lo.SendErr(ctx, pnio.AbortStatus(pnio.AbortUser)); err != nil {
		t.Fatal(err)
	}
	h.waitReason(t, req.AR.ARUUID, pnio.AbortUser)
}

func TestConnectModuleDiff(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	p := n.peer(d, "192.168.0.2", true)
	req := p.ioCAR()
	// Slot 2 with a wrong submodule, slot 1 with an extra subslot, a
	// missing slot 7 and a module of another API.
	req.Expected[2].Modules[0].Submodules[0].IdentNumber = 0x98
	req.Expected[1].Modules[0].Submodules = append(req.Expected[1].Modules[0].Submodules,
		pnio.ExpectedSubmodule{Subslot: 2, IdentNumber: 0x12, Descriptions: pnio.NewDataDescriptions(pnio.SubmoduleNoIO, 0, 0)})
	req.Expected = append(req.Expected,
		pnio.ExpectedSubmoduleBlockReq{Modules: []pnio.ExpectedModule{{API: 0, Slot: 7, IdentNumber: 0x70,
			Submodules: []pnio.ExpectedSubmodule{{Subslot: 1, IdentNumber: 0x71, Descriptions: pnio.NewDataDescriptions(pnio.SubmoduleNoIO, 0, 0)}}}}},
		pnio.ExpectedSubmoduleBlockReq{Modules: []pnio.ExpectedModule{{API: 1, Slot: 1, IdentNumber: 0x10,
			Submodules: []pnio.ExpectedSubmodule{{Subslot: 1, IdentNumber: 0x11, Descriptions: pnio.NewDataDescriptions(pnio.SubmoduleNoIO, 0, 0)}}}}},
	)
	// The other API comes first in the request; the diff is sorted.
	req.Expected[0], req.Expected[len(req.Expected)-1] = req.Expected[len(req.Expected)-1], req.Expected[0]
	p.establish(req)
	b, err := p.rc.Read(context.Background(), req.AR.ARUUID, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexModuleDiffAR}, 0)
	if err != nil {
		t.Fatal(err)
	}
	blk, _, _ := pnio.ReadBlock(b)
	diff, err := pnio.DecodeModuleDiffBlock(blk)
	if err != nil || len(diff.APIs) != 2 || diff.APIs[0].API != 0 || diff.APIs[1].API != 1 {
		t.Fatalf("diff %+v %v", diff, err)
	}
	mods := map[uint16]pnio.DiffModule{}
	for _, m := range diff.APIs[0].Modules {
		mods[m.Slot] = m
	}
	if m := mods[7]; m.State != pnio.ModuleStateNoModule {
		t.Fatalf("slot 7 %+v", m)
	}
	if m := mods[1]; m.State != pnio.ModuleStateProper || len(m.Submodules) != 1 || m.Submodules[0].Subslot != 2 || m.Submodules[0].State&pnio.SubmStateIdentNoSubmodule == 0 {
		t.Fatalf("slot 1 %+v", m)
	}
	if m := mods[2]; len(m.Submodules) != 1 || m.Submodules[0].State&pnio.SubmStateIdentWrong == 0 || m.Submodules[0].IdentNumber != 0x21 {
		t.Fatalf("slot 2 %+v", m)
	}
	if m := diff.APIs[1].Modules; len(m) != 1 || m[0].State != pnio.ModuleStateNoModule {
		t.Fatalf("API 1 %+v", m)
	}
	// The wrong submodule exchanges no data.
	d.mu.Lock()
	a := d.ars[req.AR.ARUUID]
	d.mu.Unlock()
	if d.ownerOf(2, 1) == a || d.ownerOf(1, 1) != a {
		t.Fatalf("owners: 2/1 %v, 1/1 %v", d.ownerOf(2, 1), d.ownerOf(1, 1))
	}
}

// TestConcurrentConnects: of several IOCAR Connects that arrive at the
// same time, MaxARs (2) are accepted; the reservation keeps the others
// from slipping between the check and the insertion, and every submodule
// has exactly one owner.
func TestConcurrentConnects(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	const peers = 6
	ps := make([]*peer, peers)
	for i := range ps {
		ps[i] = n.peer(d, fmt.Sprintf("192.168.0.%d", 100+i), false)
	}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	start := make(chan struct{})
	for i := range ps {
		wg.Add(1)
		go func(p *peer) {
			defer wg.Done()
			req := p.ioCAR()
			<-start
			// A Connect is accepted or refused out of ARs; one that got no
			// answer (an RPC timeout on a loaded runner) is sent again.
			for try := 0; try < 3; try++ {
				_, err := p.connect(req)
				if err == nil {
					accepted.Add(1)
					return
				}
				var se *pnio.StatusError
				if errors.As(err, &se) {
					if se.Status.Code2 != pnio.CMRPCOutOfARs {
						t.Errorf("refused with %v", err)
					}
					return
				}
			}
			t.Errorf("no answer to a Connect")
		}(ps[i])
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("%d IOCARs accepted", accepted.Load())
	}
	ars := d.ARs()
	if len(ars) != 2 {
		t.Fatalf("ARs %+v", ars)
	}
	d.mu.Lock()
	a1, a2 := d.ars[ars[0].UUID], d.ars[ars[1].UUID]
	d.mu.Unlock()
	for k := range a1.match {
		if o := d.ownerOf(k.slot, k.subslot); o != a1 && o != a2 {
			t.Fatalf("submodule %v without an owner", k)
		}
	}
}

func TestConnectSameControllerReplaces(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	// A controller that lost its AR connects anew before the device saw the
	// old one end: the new AR replaces the old one and owns the submodules,
	// though another controller's AR expects them too.
	c1 := p.ioCAR()
	p.establish(c1)
	other := p.ioCAR()
	other.AR.InitiatorObject[11] ^= 0xFF
	if _, err := p.connect(other); err != nil {
		t.Fatal(err)
	}
	c2 := p.ioCAR()
	resp, err := p.connect(c2)
	if err != nil {
		t.Fatal(err)
	}
	for _, api := range resp.ModuleDiff.APIs {
		for _, m := range api.Modules {
			for _, sm := range m.Submodules {
				if sm.State&(0xF<<7) == pnio.SubmStateARInfoCtrlLock {
					t.Fatalf("slot %d subslot %d locked", m.Slot, sm.Subslot)
				}
			}
		}
	}
	h.waitReason(t, c1.AR.ARUUID, pnio.AbortRerun)
	if _, ok := arState(d, c1.AR.ARUUID); ok {
		t.Fatal("old AR still there")
	}
}

// TestRerunCountsOnce: while a Connect replaces an AR (a rerun), the AR
// and its replacement count once against MaxARs (2): another controller
// is accepted.
func TestRerunCountsOnce(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	p1 := n.peer(d, "192.168.0.2", true)
	p2 := n.peer(d, "192.168.0.3", true)
	c1 := p1.ioCAR()
	p1.establish(c1)
	// The rerun of c1 in progress: a reservation that replaces it.
	d.mu.Lock()
	old := d.ars[c1.AR.ARUUID]
	d.reserved++
	d.replacing[old] = true
	d.mu.Unlock()
	if _, err := p2.connect(p2.ioCAR()); err != nil {
		t.Fatalf("another controller during a rerun: %v", err)
	}
	d.mu.Lock()
	d.reserved--
	delete(d.replacing, old)
	d.mu.Unlock()
	// Without the rerun the device is full.
	c3 := p2.ioCAR()
	c3.AR.InitiatorObject[11] ^= 0xFF
	_, err := p2.connect(c3)
	wantStatus(t, err, pnio.Code1CMRPC, pnio.CMRPCOutOfARs)
}

// TestTwoConnectsBothAccepted: two IO controllers connecting at once to a
// device with room for two ARs are both accepted. A Connect's reservation
// ends when its AR enters the device's table; before, the AR counted
// twice until the handler returned, and the other Connect could be
// refused "out of AR resources".
func TestTwoConnectsBothAccepted(t *testing.T) {
	for round := range 30 {
		n := newNet(t)
		d, _ := n.device(nil)
		ps := []*peer{n.peer(d, "192.168.0.100", false), n.peer(d, "192.168.0.101", false)}
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, len(ps))
		for i, p := range ps {
			req := p.ioCAR()
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for try := 0; try < 3; try++ {
					var se *pnio.StatusError
					if _, errs[i] = p.connect(req); errs[i] == nil || errors.As(errs[i], &se) {
						return
					}
				}
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: controller %d refused: %v", round, i, err)
			}
		}
		_ = d.Close()
	}
}
