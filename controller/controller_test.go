// SPDX-License-Identifier: MIT

package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/alarm"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
)

func TestStateMachine(t *testing.T) {
	names := map[State]string{
		StateOffline: "Offline", StateResolving: "Resolving", StateConnecting: "Connecting", StateParameterizing: "Parameterizing",
		StateWaitAppReady: "WaitApplicationReady", StateRunning: "Running", StateStopped: "Stopped", State(42): "State(?)",
	}
	for s, want := range names {
		if s.String() != want {
			t.Errorf("%d: %q", s, s.String())
		}
	}
	next := map[State]State{
		StateOffline: StateResolving, StateResolving: StateConnecting, StateConnecting: StateParameterizing,
		StateParameterizing: StateWaitAppReady, StateWaitAppReady: StateRunning,
	}
	for from := StateOffline; from <= StateStopped+1; from++ {
		for to := StateOffline; to <= StateStopped+1; to++ {
			want := from <= StateRunning && to <= StateStopped && (next[from] == to && from != StateRunning ||
				to == StateStopped || to == StateOffline && from != StateOffline)
			if got := CanTransition(from, to); got != want {
				t.Errorf("%s → %s: %v", from, to, got)
			}
		}
	}
}

func TestNewErrors(t *testing.T) {
	n := newNet(t)
	if _, err := New(Config{}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
	nd := n.node(ctrlIP)
	for _, failAt := range []int32{1, 2} {
		var calls atomic.Int32
		lp := func(ctx context.Context, network, address string) (net.PacketConn, error) {
			if calls.Add(1) == failAt {
				return nil, errors.New("no sockets")
			}
			return nd.stack.ListenPacket(ctx, network, address)
		}
		if _, err := New(Config{Mux: nd.mux, ListenPacket: lp}); err == nil {
			t.Fatalf("ListenPacket failure %d accepted", failAt)
		}
	}
	closed := n.node("192.168.0.99")
	closed.mux.Close()
	<-closed.mux.Done()
	if _, err := New(Config{Mux: closed.mux, ListenPacket: closed.stack.ListenPacket}); err == nil {
		t.Fatal("closed mux accepted")
	}
}

func TestNewDefaults(t *testing.T) {
	n := newNet(t)
	nd := n.node(ctrlIP)
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP: %v", err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	c, err := New(Config{Mux: nd.mux, RPCPort: port})
	if err != nil {
		t.Skipf("OS UDP stack: %v", err)
	}
	defer c.Close()
	if c.cfg.StationName != "go-profinet-controller" || c.ObjectUUID() != pnio.ObjectUUID(1, 0x0001, 0xF000) {
		t.Fatalf("%q %s", c.cfg.StationName, c.ObjectUUID())
	}
	if c.DCP() == nil || c.RPC() == nil || len(c.Devices()) != 0 {
		t.Fatal("accessors")
	}
	if _, ok := c.Device("x"); ok {
		t.Fatal("device x")
	}
}

func TestAddDeviceValidation(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller(func(cfg *Config) { cfg.VendorID, cfg.DeviceID, cfg.Instance = 0x1234, 0x5678, 3 })
	if c.ObjectUUID() != pnio.ObjectUUID(3, 0x5678, 0x1234) {
		t.Fatalf("object %s", c.ObjectUUID())
	}
	if _, err := c.AddDevice(DeviceConfig{StationName: "Bad_Name!", Model: testModel()}); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := c.AddDevice(DeviceConfig{StationName: "dut"}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("nil model %v", err)
	}
	bad := testModel()
	bad.Slots[0].Number = 0
	if _, err := c.AddDevice(DeviceConfig{StationName: "dut", Model: bad}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("invalid model %v", err)
	}
	cd, err := c.AddDevice(DeviceConfig{StationName: "dut", Model: testModel(), ReconnectMin: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddDevice(DeviceConfig{StationName: "dut", Model: testModel()}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("duplicate %v", err)
	}
	if got, ok := c.Device("dut"); !ok || got != cd || len(c.Devices()) != 1 {
		t.Fatal("Device lookup")
	}
	// Defaults.
	if cd.cfg.SendClockFactor != 32 || cd.cfg.ReductionRatio != 4 || cd.cfg.DataHoldFactor != 3 || cd.cfg.WatchdogFactor != 3 ||
		cd.cfg.ActivityTimeout != 10*time.Second || cd.cfg.RTATimeoutFactor != 5 || cd.cfg.RTARetries != 3 || cd.cfg.RPCTimeout != 5*time.Second ||
		cd.cfg.ReconnectMax != 10*time.Second {
		t.Fatalf("defaults %+v", cd.cfg)
	}
	// Before an AR there are no records.
	ctx := context.Background()
	if _, err := cd.ReadRecord(ctx, Record{}, 0); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	if err := cd.WriteRecord(ctx, Record{}, nil); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	if _, err := cd.WriteMultiple(ctx, nil); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	if _, err := cd.ReadIM0(ctx); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	if _, err := cd.ReadDiagnosis(ctx); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	if !cd.ARUUID().IsNil() || cd.ModuleDiff() != nil || !cd.MAC().IsZero() || !cd.IP().IsZero() {
		t.Fatal("getters before an AR")
	}
	// Process images.
	if err := cd.SetOutput(9, 1, nil, pnio.IOxSGood); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if err := cd.SetOutput(2, 1, []byte{1}, pnio.IOxSGood); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
	if _, _, ok := cd.Input(2, 1); ok {
		t.Fatal("output-only submodule has inputs")
	}
	if in, iops, ok := cd.Input(0, 1); !ok || len(in) != 0 || iops != pnio.IOxSBad {
		t.Fatalf("NO_IO input %x %02X %v", in, iops, ok)
	}
	if _, ok := cd.ConsumerStatus(2, 1); ok {
		t.Fatal("consumer status before an AR")
	}
	// WaitState gives up with its context.
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := cd.WaitState(cctx, StateRunning); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	// The device is not found.
	if err := waitErr(t, cd); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	cd.Close()
	if _, ok := c.Device("dut"); ok {
		t.Fatal("closed device still listed")
	}
	if st, _ := cd.State(); st != StateStopped {
		t.Fatalf("state %s", st)
	}
	c.Close()
	if _, err := c.AddDevice(DeviceConfig{StationName: "dut", Model: testModel()}); !errors.Is(err, profinet.ErrClosed) {
		t.Fatalf("after Close %v", err)
	}
	if c.Close() != nil {
		t.Fatal("second Close")
	}
}

func TestResolveErrors(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	f := n.fake("fake")
	add := func(cfg DeviceConfig) *Device {
		t.Helper()
		cfg.StationName = "fake"
		if cfg.Model == nil {
			cfg.Model = testModel()
		}
		cfg.ReconnectMin, cfg.ReductionRatio, cfg.DataHoldFactor = time.Hour, 512, 0x1E00
		cd, err := c.AddDevice(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return cd
	}
	// Another device type.
	f.set(func(f *fakeDev) { f.identity.DeviceID = 0x0099 })
	cd := add(DeviceConfig{})
	if err := waitErr(t, cd); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
	cd.Close()
	// No IP address.
	f.set(func(f *fakeDev) { f.identity.DeviceID, f.identity.IP = 0x0042, dcp.IPSuite{} })
	cd = add(DeviceConfig{})
	if err := waitErr(t, cd); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("err %v", err)
	}
	cd.Close()
	// Assigned by the controller.
	cd = add(DeviceConfig{AssignIP: true, IP: suite(devIP), PermanentIP: true})
	waitState(t, cd, StateWaitAppReady, 5*time.Second)
	if cd.IP() != suite(devIP) || cd.MAC() != f.mux.HardwareAddr() {
		t.Fatalf("IP %s MAC %s", cd.IP(), cd.MAC())
	}
	cd.Close()
}

func TestAssignIPRefused(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	n.device("", func(cfg *device.Config) {
		cfg.ApplyIP = func(dcp.IPSuite, bool) error { return errors.New("no") }
	})
	cd, err := c.AddDevice(DeviceConfig{StationName: "dut", Model: testModel(), AssignIP: true, IP: suite(devIP), ReconnectMin: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	err = waitErr(t, cd)
	var se *dcp.SetError
	if !errors.As(err, &se) || se.Code != dcp.BlockErrorSetNotPossible {
		t.Fatalf("err %v", err)
	}
}

func TestConnectErrors(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	f := n.fake("fake")
	cases := []struct {
		name    string
		connect func(req *pnio.ConnectRequest, resp *pnio.ConnectResponse) pnio.Status
		raw     func(op pnio.OpNum, blocks []byte) []byte
		check   func(err error) bool
	}{
		{"rejected", func(*pnio.ConnectRequest, *pnio.ConnectResponse) pnio.Status {
			return pnio.ServiceStatus(pnio.CodeConnect, pnio.Code1CMRPC, pnio.CMRPCOutOfARs)
		}, nil, func(err error) bool { return errors.Is(err, profinet.ErrResourceExhausted) }},
		{"other AR", func(_ *pnio.ConnectRequest, r *pnio.ConnectResponse) pnio.Status {
			r.AR.ARUUID = rpc.NewRandomUUID()
			return pnio.Status{}
		}, nil, func(err error) bool { return errors.Is(err, profinet.ErrMalformed) }},
		{"no AlarmCR", func(_ *pnio.ConnectRequest, r *pnio.ConnectResponse) pnio.Status {
			r.AlarmCR = nil
			return pnio.Status{}
		}, nil, func(err error) bool { return errors.Is(err, profinet.ErrMalformed) }},
		{"input FrameID changed", func(_ *pnio.ConnectRequest, r *pnio.ConnectResponse) pnio.Status {
			r.IOCRs[0].FrameID++
			return pnio.Status{}
		}, nil, func(err error) bool { return errors.Is(err, profinet.ErrMalformed) }},
		{"no output IOCR", func(_ *pnio.ConnectRequest, r *pnio.ConnectResponse) pnio.Status {
			r.IOCRs = r.IOCRs[:1]
			return pnio.Status{}
		}, nil, func(err error) bool { return errors.Is(err, profinet.ErrMalformed) }},
		{"output FrameID unassigned", func(_ *pnio.ConnectRequest, r *pnio.ConnectResponse) pnio.Status {
			r.IOCRs[1].FrameID = pnio.FrameIDAssign
			r.IOCRs = append(r.IOCRs, pnio.IOCRBlockRes{Type: pnio.IOCRMCConsumer, FrameID: 0xF800})
			return pnio.Status{}
		}, nil, func(err error) bool { return errors.Is(err, profinet.ErrMalformed) }},
		{"undecodable", nil, func(op pnio.OpNum, _ []byte) []byte {
			if op != pnio.OpConnect {
				return nil
			}
			return pnio.AppendNDRResponse(nil, order, pnio.Status{}, 0, []byte{0x81, 0x01, 0, 50, 1, 0})
		}, func(err error) bool { return err != nil }},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			f.set(func(f *fakeDev) { f.connect, f.raw = cs.connect, cs.raw })
			cd, err := c.AddDevice(DeviceConfig{StationName: "fake", Model: testModel(), ReconnectMin: time.Hour, ReductionRatio: 512, DataHoldFactor: 0x1E00})
			if err != nil {
				t.Fatal(err)
			}
			defer cd.Close()
			if err := waitErr(t, cd); !cs.check(err) {
				t.Fatalf("err %v", err)
			}
			if st, _ := cd.State(); st != StateOffline {
				t.Fatalf("state %s", st)
			}
			if !cd.ARUUID().IsNil() {
				t.Fatal("AR kept")
			}
			c.mu.Lock()
			ars := len(c.byAR)
			c.mu.Unlock()
			if ars != 0 {
				t.Fatalf("%d ARs registered", ars)
			}
		})
	}
	f.set(func(f *fakeDev) { f.connect, f.raw = nil, nil })

	// PrmEnd refused.
	f.set(func(f *fakeDev) {
		f.control = func(cb pnio.ControlBlock) pnio.Status {
			if cb.Type == pnio.BlockPrmEndReq {
				return pnio.ServiceStatus(pnio.CodeControl, pnio.Code1CMDEV, 0)
			}
			return pnio.Status{}
		}
	})
	cd, err := c.AddDevice(DeviceConfig{StationName: "fake", Model: testModel(), ReconnectMin: time.Hour, ReductionRatio: 512, DataHoldFactor: 0x1E00})
	if err != nil {
		t.Fatal(err)
	}
	err = waitErr(t, cd)
	wantStatus(t, err, pnio.Code1CMDEV, 0)
	if cd.Stats().Connects != 1 || cd.Stats().Aborts != 1 {
		t.Fatalf("stats %+v", cd.Stats())
	}
	cd.Close()
	f.set(func(f *fakeDev) { f.control = nil })

	// A parameter write fails.
	f.set(func(f *fakeDev) {
		f.raw = func(op pnio.OpNum, _ []byte) []byte {
			if op != pnio.OpWrite {
				return nil
			}
			return pnio.AppendNDRResponse(nil, order, pnio.RWStatus(true, pnio.RWAccessDenied, 0), 0, nil)
		}
	})
	cd, err = c.AddDevice(DeviceConfig{StationName: "fake", Model: testModel(), ReconnectMin: time.Hour, ReductionRatio: 512, DataHoldFactor: 0x1E00,
		Parameters: []Parameter{{Slot: 2, Subslot: 1, Index: 0x10, Data: []byte{0, 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, waitErr(t, cd), pnio.RWAccessDenied, 0)
	cd.Close()
	f.set(func(f *fakeDev) { f.raw = nil })
}

func TestApplicationReady(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	f := n.fake("fake")
	ctx := context.Background()

	// No ApplicationReady within the activity timeout.
	cd, err := c.AddDevice(DeviceConfig{StationName: "fake", Model: testModel(), ReconnectMin: time.Hour, ReductionRatio: 512, DataHoldFactor: 0x1E00, ActivityTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, cd); !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	cd.Close()
	for len(f.requests) > 0 {
		<-f.requests
	}

	// Closed while waiting: the AR is released.
	var released atomic.Bool
	f.set(func(f *fakeDev) {
		f.control = func(cb pnio.ControlBlock) pnio.Status {
			if cb.Type == pnio.BlockReleaseReq {
				released.Store(true)
				// A failed release is only logged.
				return pnio.ServiceStatus(pnio.CodeRelease, pnio.Code1CMRPC, pnio.CMRPCARUUIDUnknown)
			}
			return pnio.Status{}
		}
	})
	cd, err = c.AddDevice(DeviceConfig{StationName: "fake", Model: testModel(), ReconnectMin: time.Hour, ReductionRatio: 512, DataHoldFactor: 0x1E00})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, StateWaitAppReady, 5*time.Second)
	req := <-f.requests
	// The controller's RPC server checks the ApplicationReady.
	cb := pnio.ControlBlock{Type: pnio.BlockAppReadyReq, ARUUID: req.AR.ARUUID, SessionKey: req.AR.SessionKey, Command: pnio.CmdApplicationReady}
	bad := cb
	bad.SessionKey++
	st, _, err := f.appReady(c, rpc.Port, bad)
	if err != nil || st.Code1 != pnio.Code1CMRPC || st.Code2 != pnio.CMRPCARUUIDUnknown {
		t.Fatalf("wrong session key: %s %v", st, err)
	}
	bad = cb
	bad.ARUUID = rpc.NewRandomUUID()
	if st, _, err := f.appReady(c, rpc.Port, bad); err != nil || st.Code2 != pnio.CMRPCARUUIDUnknown {
		t.Fatalf("unknown AR: %s %v", st, err)
	}
	bad = cb
	bad.Type = pnio.BlockPrmEndReq
	if st, _, err := f.appReady(c, rpc.Port, bad); err != nil || st.Code1 != pnio.Code1AppReady || st.Code2 != 8 {
		t.Fatalf("PrmEnd block: %s %v", st, err)
	}
	cd.Close()
	if !released.Load() {
		t.Fatal("no release")
	}
	f.set(func(f *fakeDev) { f.control = nil })

	// A plug ApplicationReady is accepted; the device then runs.
	cd, err = c.AddDevice(DeviceConfig{StationName: "fake", Model: testModel(), ReconnectMin: time.Hour, ReductionRatio: 512, DataHoldFactor: 0x1E00, RTATimeoutFactor: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer cd.Close()
	waitState(t, cd, StateWaitAppReady, 5*time.Second)
	req = <-f.requests
	cb = pnio.ControlBlock{Type: pnio.BlockAppReadyPlugReq, ARUUID: req.AR.ARUUID, SessionKey: req.AR.SessionKey, Command: pnio.CmdApplicationReady}
	st, blocks, err := f.appReady(c, rpc.Port, cb)
	if err != nil || !st.OK() {
		t.Fatalf("ApplicationReady %s %v", st, err)
	}
	blk, _, _ := pnio.ReadBlock(blocks)
	if res, err := pnio.DecodeControlBlock(blk); err != nil || res.Command&pnio.CmdDone == 0 || res.ARUUID != req.AR.ARUUID {
		t.Fatalf("response %+v %v", res, err)
	}
	waitState(t, cd, StateRunning, 5*time.Second)
	if s := cd.Stats(); s.Connects != 1 {
		t.Fatalf("stats %+v", s)
	}
	_ = ctx
}

func TestAlarmAckFailure(t *testing.T) {
	n := newNet(t)
	c, cn := n.controller()
	f := n.fake("fake")
	alarms := make(chan AlarmEvent, 4)
	// A long data hold time: the fake sends no cyclic frames.
	cd, err := c.AddDevice(DeviceConfig{StationName: "fake", Model: testModel(), ReconnectMin: time.Hour, ReductionRatio: 512, DataHoldFactor: 0x1E00, RTATimeoutFactor: 1,
		OnAlarm: func(a AlarmEvent) { alarms <- a }})
	if err != nil {
		t.Fatal(err)
	}
	defer cd.Close()
	waitState(t, cd, StateWaitAppReady, 5*time.Second)
	req := <-f.requests
	// A raw alarm DATA PDU: the fake has no alarm endpoint and never
	// acknowledges the controller's AlarmAck.
	note := pnio.AlarmNotification{Type: pnio.AlarmProcess, Slot: 1, Subslot: 1, ModuleIdentNumber: 0x10, SubmoduleIdentNumber: 0x11}
	p := alarm.PDU{FrameID: pnio.FrameIDAlarmLow, Dst: req.AlarmCR.LocalAlarmReference, Src: 0x77, Type: alarm.PDUData, Version: alarm.Version,
		AddFlags: alarm.WindowSize | alarm.FlagTACK, SendSeq: alarm.InitialSendSeq, AckSeq: alarm.InitialAckSeq, VarPart: note.Append(nil)}
	if err := f.mux.Send(context.Background(), p.AppendEthernet(nil, cn.mux.HardwareAddr(), f.mux.HardwareAddr())); err != nil {
		t.Fatal(err)
	}
	a := <-alarms
	if a.Device != "fake" || a.Notification.Type != pnio.AlarmProcess || a.Notification.SubmoduleIdentNumber != 0x11 {
		t.Fatalf("alarm %+v", a)
	}
	if err := waitErr(t, cd); !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	if cd.Stats().Alarms != 1 {
		t.Fatalf("stats %+v", cd.Stats())
	}
}

func TestAbortByDevice(t *testing.T) {
	n := newNet(t)
	c, cn := n.controller()
	f := n.fake("fake")
	cd, err := c.AddDevice(DeviceConfig{StationName: "fake", Model: testModel(), ReconnectMin: time.Hour, ReductionRatio: 512, DataHoldFactor: 0x1E00})
	if err != nil {
		t.Fatal(err)
	}
	defer cd.Close()
	waitState(t, cd, StateWaitAppReady, 5*time.Second)
	req := <-f.requests
	p := alarm.PDU{FrameID: pnio.FrameIDAlarmLow, Dst: req.AlarmCR.LocalAlarmReference, Src: 0x77, Type: alarm.PDUErr, Version: alarm.Version,
		AddFlags: alarm.WindowSize, SendSeq: alarm.InitialSendSeq, AckSeq: alarm.InitialAckSeq, VarPart: pnio.AbortStatus(pnio.AbortUser).Append(nil)}
	if err := f.mux.Send(context.Background(), p.AppendEthernet(nil, cn.mux.HardwareAddr(), f.mux.HardwareAddr())); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, cd); !errors.Is(err, profinet.ErrAborted) {
		t.Fatalf("err %v", err)
	}
}

func TestServeRPC(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	ctx := context.Background()
	call := func(op pnio.OpNum, body []byte) (pnio.Status, error) {
		h := rpc.Header{OpNum: uint16(op), DRep: [3]byte{rpc.DRepLittleEndian}}
		b, err := c.serveRPC(ctx, &rpc.Request{Header: h, Body: body})
		if err != nil {
			return pnio.Status{}, err
		}
		st, _, err := pnio.DecodeNDRResponse(b, order)
		return st, err
	}
	ndr := func(blocks []byte) []byte { return pnio.AppendNDRRequest(nil, order, pnio.DefaultArgsMaximum, blocks) }
	if st, err := call(pnio.OpControl, []byte{1}); err != nil || st.Code != pnio.CodeControl || st.Code2 != pnio.CMRPCArgsLength {
		t.Fatalf("short NDR %s %v", st, err)
	}
	var f *rpc.Fault
	if _, err := call(pnio.OpRead, ndr(nil)); !errors.As(err, &f) || !f.Reject || f.Status != rpc.StatusOpRangeError {
		t.Fatalf("err %v", err)
	}
	if st, err := call(pnio.OpControl, ndr([]byte{1})); err != nil || st.Code2 != pnio.CMRPCArgsLength {
		t.Fatalf("short block %s %v", st, err)
	}
	other := pnio.ControlBlock{Type: pnio.BlockType(0x0019)}
	if st, err := call(pnio.OpControl, ndr(other.Append(nil))); err != nil || st.Code1 != pnio.Code1AppReady || st.Code2 != 1 {
		t.Fatalf("not a control block %s %v", st, err)
	}
	ar := pnio.ControlBlock{Type: pnio.BlockAppReadyReq, ARUUID: rpc.NewRandomUUID(), Command: pnio.CmdApplicationReady}
	b := ar.Append(nil)
	b[4] = 2 // BlockVersionHigh
	if st, err := call(pnio.OpControl, ndr(b)); err != nil || st.OK() {
		t.Fatalf("bad version %s %v", st, err)
	}
	ar.Command = pnio.CmdPrmEnd
	if st, err := call(pnio.OpControl, ndr(ar.Append(nil))); err != nil || st.Code1 != pnio.Code1AppReady || st.Code2 != 8 {
		t.Fatalf("not ApplicationReady %s %v", st, err)
	}
	ar.Command = pnio.CmdApplicationReady
	if st, err := call(pnio.OpControl, ndr(ar.Append(nil))); err != nil || st.Code2 != pnio.CMRPCARUUIDUnknown {
		t.Fatalf("unknown AR %s %v", st, err)
	}
}

func TestRunningDevice(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	dev, dn := n.device(devIP)
	var mu sync.Mutex
	var trans []State
	alarms := make(chan AlarmEvent, 8)
	cd, err := c.AddDevice(DeviceConfig{StationName: "dut", Model: testModel(), ReconnectMin: 50 * time.Millisecond, ReconnectMax: 100 * time.Millisecond, DataHoldFactor: 100,
		Parameters: []Parameter{{Slot: 2, Subslot: 1, Index: 0x10, Data: []byte{0, 2}}},
		OnAlarm:    func(a AlarmEvent) { alarms <- a },
		OnState: func(_, to State, _ error) {
			mu.Lock()
			trans = append(trans, to)
			mu.Unlock()
		}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, StateRunning, 10*time.Second)
	ctx := context.Background()
	if cd.Name() != "dut" || cd.MAC() != dn.mux.HardwareAddr() || cd.IP() != suite(devIP) || cd.ARUUID().IsNil() {
		t.Fatalf("getters %s %s %s", cd.MAC(), cd.IP(), cd.ARUUID())
	}
	// The diff of the Connect only reports the pending ApplicationReady.
	if diff := cd.ModuleDiff(); diff == nil || len(diff.APIs) != 1 || len(diff.APIs[0].Modules) != 4 {
		t.Fatalf("diff %+v", diff)
	} else {
		for _, m := range diff.APIs[0].Modules {
			if m.State != pnio.ModuleStateProper {
				t.Fatalf("module %+v", m)
			}
		}
	}
	ars := dev.ARs()
	if len(ars) != 1 || ars[0].UUID != cd.ARUUID() || ars[0].InputFrameID < pnio.FrameIDRTC1Min {
		t.Fatalf("device ARs %+v", ars)
	}
	// What Connect negotiated, as the device sees it.
	info, ok := cd.AR()
	if !ok || info.UUID != cd.ARUUID() || info.Type != pnio.ARTypeIOCARSingle || info.RTClass != pnio.RTClass1 || info.Advanced ||
		info.Input.FrameID != ars[0].InputFrameID || info.Output.FrameID != ars[0].OutputFrameID || info.Input.Type != pnio.IOCRInput ||
		info.Input.DataLength < 40 || info.Input.Cycle != 4*time.Millisecond || info.Output.DataHoldFactor != 100 ||
		info.LocalAlarmRef == 0 || info.DeviceAddr != devAddr().String() || len(info.Input.Objects) == 0 || len(info.Output.IOCS) == 0 {
		t.Fatalf("AR info %+v (device %+v)", info, ars[0])
	}
	// The parameter was written.
	if b, err := cd.ReadRecord(ctx, Record{Slot: 2, Subslot: 1, Index: 0x10}, 0); err != nil || !bytes.Equal(b, []byte{0, 2}) {
		t.Fatalf("parameter %x %v", b, err)
	}
	// Records.
	if err := cd.WriteRecord(ctx, Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 3}); err != nil {
		t.Fatal(err)
	}
	sts, err := cd.WriteMultiple(ctx, []WriteItem{{Record: Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{0, 4}}, {Record: Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{1}}})
	if err != nil || len(sts) != 2 || !sts[0].OK() || sts[1].Code1 != pnio.RWWriteLength {
		t.Fatalf("write multiple %v %v", sts, err)
	}
	if im0, err := cd.ReadIM0(ctx); err != nil || im0.OrderID != "OTF-CTL-1" {
		t.Fatalf("I&M0 %+v %v", im0, err)
	}
	if err := dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: 1, ErrorType: pnio.ChannelErrLineBreak}); err != nil {
		t.Fatal(err)
	}
	if a := <-alarms; a.Notification.Type != pnio.AlarmDiagnosis {
		t.Fatalf("alarm %+v", a)
	}
	// A high-priority alarm is acknowledged on the high priority.
	if err := dev.SendAlarm(ctx, device.Alarm{High: true, Type: pnio.AlarmProcess, Slot: 3, Subslot: 1}); err != nil {
		t.Fatal(err)
	}
	if a := <-alarms; !a.Notification.High || a.Notification.Slot != 3 {
		t.Fatalf("alarm %+v", a)
	}
	if dd, err := cd.ReadDiagnosis(ctx); err != nil || len(dd) != 1 {
		t.Fatalf("diagnosis %+v %v", dd, err)
	}
	if dd, err := recordsOf(t, cd).ReadDiagnosis(ctx, cd.ARUUID(), 1, 1, true); err != nil || len(dd) != 1 || dd[0].Slot != 1 {
		t.Fatalf("submodule diagnosis %+v %v", dd, err)
	}
	if dd, err := recordsOf(t, cd).ReadDiagnosis(ctx, cd.ARUUID(), 3, 1, true); err != nil || len(dd) != 0 {
		t.Fatalf("submodule diagnosis %+v %v", dd, err)
	}
	// Cyclic data both ways.
	if err := dev.SetInput(1, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8}, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	if err := cd.SetOutput(2, 1, []byte{4, 3, 2, 1}, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "cyclic data", func() bool {
		in, iops, _ := cd.Input(1, 1)
		out, _, _ := dev.Output(2, 1)
		cs, ok := cd.ConsumerStatus(2, 1)
		return bytes.Equal(in, []byte{1, 2, 3, 4, 5, 6, 7, 8}) && iops == pnio.IOxSGood && bytes.Equal(out, []byte{4, 3, 2, 1}) && ok && cs == pnio.IOxSGood
	})
	if s := cd.Stats(); s.Connects != 1 || s.Provider.Sent == 0 || s.Consumer.Received == 0 || s.Alarms != 2 {
		t.Fatalf("stats %+v", s)
	}
	// Link down: the input data hold time expires; then the AR comes
	// back.
	dn.ep.SetLinkUp(false)
	waitState(t, cd, StateOffline, 5*time.Second)
	if _, err := cd.State(); !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	if s := cd.Stats(); s.Aborts != 1 || s.Consumer.Received == 0 {
		t.Fatalf("stats after teardown %+v", s)
	}
	if _, iops, _ := cd.Input(1, 1); iops != pnio.IOxSBad {
		t.Fatalf("input IOPS after teardown %02X", iops)
	}
	eventually(t, 5*time.Second, "device abort", func() bool { return len(dev.ARs()) == 0 })
	dn.ep.SetLinkUp(true)
	waitState(t, cd, StateRunning, 10*time.Second)
	// The device closes: it aborts the AR.
	dev.Close()
	waitState(t, cd, StateOffline, 5*time.Second)
	if _, err := cd.State(); !errors.Is(err, profinet.ErrAborted) {
		t.Fatalf("err %v", err)
	}
	mu.Lock()
	got := append([]State(nil), trans...)
	mu.Unlock()
	want := []State{StateResolving, StateConnecting, StateParameterizing, StateWaitAppReady, StateRunning, StateOffline}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("transitions %v", got)
		}
	}
	cd.Close()
	if st, _ := cd.State(); st != StateStopped {
		t.Fatalf("state %s", st)
	}
}

// recordsOf returns the record client of the current AR.
func recordsOf(t *testing.T, d *Device) *RecordClient {
	t.Helper()
	a, err := d.records()
	if err != nil {
		t.Fatal(err)
	}
	return a.records
}

func TestStartupVariants(t *testing.T) {
	cases := []struct {
		name string
		ctrl func(*Config)
		dev  DeviceConfig
		cls  pnio.RTClass
	}{
		{"advanced", nil, DeviceConfig{Advanced: true}, pnio.RTClass2},
		{"RPC port", func(c *Config) { c.RPCPort = 34970 }, DeviceConfig{}, pnio.RTClass1},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			n := newNet(t)
			var opts []func(*Config)
			if cs.ctrl != nil {
				opts = append(opts, cs.ctrl)
			}
			c, _ := n.controller(opts...)
			dev, _ := n.device(devIP)
			cfg := cs.dev
			cfg.StationName, cfg.Model = "dut", testModel()
			cd, err := c.AddDevice(cfg)
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, cd, StateRunning, 10*time.Second)
			ars := dev.ARs()
			lo, hi, _ := pnio.FrameIDRange(cs.cls)
			if len(ars) != 1 || ars[0].InputFrameID < lo || ars[0].InputFrameID > hi || ars[0].OutputFrameID < lo || ars[0].OutputFrameID > hi {
				t.Fatalf("ARs %+v", ars)
			}
			c.Close()
			eventually(t, 5*time.Second, "release", func() bool { return len(dev.ARs()) == 0 })
		})
	}
}

func TestFrameIDAllocation(t *testing.T) {
	c := &Controller{devices: map[string]*Device{}, nextIn: map[pnio.RTClass]uint16{pnio.RTClass1: pnio.FrameIDRTC1Max}}
	if id := c.allocInputFrameID(pnio.RTClass1); id != pnio.FrameIDRTC1Max {
		t.Fatalf("0x%04X", id)
	}
	// The allocation wraps to the start of the range.
	if id := c.allocInputFrameID(pnio.RTClass1); id != pnio.FrameIDRTC1Min {
		t.Fatalf("wrap: 0x%04X", id)
	}
	c.nextIn[pnio.RTClass1] = pnio.FrameIDRTC1Min
	c.devices["a"] = &Device{ar: &arState{inFrameID: pnio.FrameIDRTC1Min}}
	if id := c.allocInputFrameID(pnio.RTClass1); id != pnio.FrameIDRTC1Min+1 {
		t.Fatalf("0x%04X", id)
	}
	// Another class allocates in its own range, from its start.
	if id := c.allocInputFrameID(pnio.RTClass2); id != pnio.FrameIDRTC2Min {
		t.Fatalf("class 2: 0x%04X", id)
	}
	if id := c.allocInputFrameID(pnio.RTClass1); id < pnio.FrameIDRTC1Min || id > pnio.FrameIDRTC1Max {
		t.Fatalf("class 1 after class 2: 0x%04X", id)
	}
	// Every FrameID in use.
	for id := uint32(pnio.FrameIDRTC1Min); id <= uint32(pnio.FrameIDRTC1Max); id++ {
		c.devices[fmt.Sprint(id)] = &Device{ar: &arState{inFrameID: uint16(id)}}
	}
	if id := c.allocInputFrameID(pnio.RTClass1); id != pnio.FrameIDRTC1Min {
		t.Fatalf("exhausted: 0x%04X", id)
	}
}

func TestProperSubmodules(t *testing.T) {
	d := &Device{cfg: DeviceConfig{Model: testModel()}}
	all := d.properSubmodules(nil)
	if len(all) != 6 {
		t.Fatalf("%v", all)
	}
	diff := &pnio.ModuleDiffBlock{APIs: []pnio.DiffAPI{{Modules: []pnio.DiffModule{
		{Slot: 0, State: pnio.ModuleStateProper, Submodules: []pnio.DiffSubmodule{{Subslot: 1, State: pnio.SubmStateIdentOK | pnio.SubmStateARInfoAppReady}}},
		{Slot: 1, State: pnio.ModuleStateWrong},
		{Slot: 2, State: pnio.ModuleStateProper, Submodules: []pnio.DiffSubmodule{{Subslot: 1, State: pnio.SubmStateIdentWrong}}},
	}}}}
	p := d.properSubmodules(diff)
	if !p[subKey{0, 1}] || p[subKey{1, 1}] || p[subKey{2, 1}] || !p[subKey{3, 1}] {
		t.Fatalf("%v", p)
	}
}

func TestImages(t *testing.T) {
	d := &Device{cfg: DeviceConfig{Model: testModel()}, outputs: map[subKey]*ioData{}, inputs: map[subKey]*ioData{}, iocs: map[subKey]uint8{}}
	d.outputs[subKey{2, 1}] = &ioData{data: []byte{1, 2, 3, 4}, iops: pnio.IOxSGood}
	d.outputs[subKey{3, 1}] = &ioData{data: []byte{5, 6}, iops: pnio.IOxSGood}
	d.inputs[subKey{1, 1}] = &ioData{data: make([]byte, 8)}
	d.inputs[subKey{3, 1}] = &ioData{data: make([]byte, 2)}
	inL, _ := rt.ComputeLayout(pnio.IOCRInput, d.subIO())
	outL, _ := rt.ComputeLayout(pnio.IOCROutput, d.subIO())
	a := &arState{inLayout: inL, outLayout: outL, proper: map[subKey]bool{{1, 1}: true, {2, 1}: true}}
	c := make([]byte, outL.DataLength)
	d.fillOutputs(a, c)
	o2, _ := outL.Find(0, 2, 1)
	o3, _ := outL.Find(0, 3, 1)
	if !bytes.Equal(c[o2.Offset:o2.Offset+4], []byte{1, 2, 3, 4}) || c[o2.IOPSOffset()] != pnio.IOxSGood || c[o3.IOPSOffset()] != pnio.IOxSBad {
		t.Fatalf("outputs %x", c)
	}
	if s, _ := outL.FindIOCS(0, 1, 1); c[s.Offset] != pnio.IOxSGood {
		t.Fatalf("IOCS 1/1 %x", c)
	}
	if s, _ := outL.FindIOCS(0, 3, 1); c[s.Offset] != pnio.IOxSBad {
		t.Fatalf("IOCS 3/1 %x", c)
	}
	in := make([]byte, inL.DataLength)
	i1, _ := inL.Find(0, 1, 1)
	copy(in[i1.Offset:], []byte{8, 7, 6, 5, 4, 3, 2, 1})
	in[i1.IOPSOffset()] = pnio.IOxSGood
	d.storeInputs(a, in, 0)
	if v, _, _ := d.Input(1, 1); !bytes.Equal(v, make([]byte, 8)) {
		t.Fatalf("invalid frame stored %x", v)
	}
	d.storeInputs(a, in, rt.DataValid|rt.StatePrimary)
	if v, iops, _ := d.Input(1, 1); !bytes.Equal(v, []byte{8, 7, 6, 5, 4, 3, 2, 1}) || iops != pnio.IOxSBad {
		t.Fatalf("stopped %x %02X", v, iops)
	}
	d.storeInputs(a, in, rt.DefaultDataStatus)
	if _, iops, _ := d.Input(1, 1); iops != pnio.IOxSGood {
		t.Fatalf("running %02X", iops)
	}
	// Alarm data that is not a notification is ignored.
	d.c = &Controller{log: profinet.NopLogger()}
	d.alarmData(a, false, []byte{1})
	pe := pnio.ControlBlock{Type: pnio.BlockPrmEndReq}
	d.alarmData(a, false, pe.Append(nil))
	if d.alarmsRx.Load() != 0 {
		t.Fatal("counted")
	}
	// Invalid transitions are ignored.
	d.setState(StateRunning, errors.New("x"))
	if st, err := d.State(); st != StateOffline || err != nil {
		t.Fatalf("%s %v", st, err)
	}
}

func TestLayoutTooLong(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	n.fake("fake")
	for _, dir := range []string{"input", "output"} {
		m := testModel()
		big := func(slot uint16) model.Slot {
			sm := model.Submodule{IdentNumber: 0x50, Subslot: 1, InputLen: 1000}
			if dir == "output" {
				sm.InputLen, sm.OutputLen = 0, 1000
			}
			return model.Slot{Number: slot, Module: model.Module{IdentNumber: 0x50, Submodules: []model.Submodule{sm}}}
		}
		m.Slots = []model.Slot{big(1), big(2)}
		// The model already rejects a direction that does not fit a C_SDU.
		if err := m.Validate(); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Fatalf("%s: %v", dir, err)
		}
		if _, err := c.AddDevice(DeviceConfig{StationName: "fake", Model: m, ReconnectMin: time.Hour}); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Fatalf("%s: %v", dir, err)
		}
	}
}
