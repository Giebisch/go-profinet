// SPDX-License-Identifier: MIT

package e2e

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
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

func running(t *testing.T, e *env, name, ip string, cfg controller.DeviceConfig) (*device.Device, *controller.Controller, *controller.Device) {
	t.Helper()
	dev, _ := e.device(name, ip, testModel(), nil)
	ctrl, _ := e.controller("192.168.0.1")
	cfg.StationName = name
	if cfg.Model == nil {
		cfg.Model = testModel()
	}
	cd, err := ctrl.AddDevice(cfg)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	// The device enters Data when it receives the response to its
	// ApplicationReady, just after the controller.
	eventually(t, 2*time.Second, "device AR in Data", func() bool {
		ars := dev.ARs()
		return len(ars) == 1 && ars[0].State == device.ARStateData
	})
	return dev, ctrl, cd
}

func TestAlarmsAndDiagnosis(t *testing.T) {
	e := newEnv(t, 2)
	alarms := make(chan controller.AlarmEvent, 16)
	dev, _, cd := running(t, e, "dut", "192.168.0.10", controller.DeviceConfig{OnAlarm: func(a controller.AlarmEvent) { alarms <- a }})
	ctx := context.Background()

	item := pnio.DiagnosisItem{ChannelNumber: 3, ChannelProperties: pnio.ChanDirInput | 4, ErrorType: pnio.ChannelErrLineBreak}
	if err := dev.AddDiagnosis(ctx, 1, 1, item); err != nil {
		t.Fatal(err)
	}
	a := <-alarms
	n := a.Notification
	if n.Type != pnio.AlarmDiagnosis || n.Slot != 1 || n.Subslot != 1 || n.ModuleIdentNumber != 0x10 || n.SubmoduleIdentNumber != 0x11 || !n.HasItem || n.USI != pnio.USIChannelDiagnosis {
		t.Fatalf("alarm %+v", n)
	}
	items, err := pnio.DecodeDiagnosisItems(n.USI, n.Data)
	if err != nil || len(items) != 1 || items[0].ErrorType != pnio.ChannelErrLineBreak || !items[0].Appears() {
		t.Fatalf("items %+v %v", items, err)
	}
	diag, err := cd.ReadDiagnosis(ctx)
	if err != nil || len(diag) != 1 || diag[0].Slot != 1 || len(diag[0].Items) != 1 || diag[0].Items[0].ChannelNumber != 3 {
		t.Fatalf("diagnosis %+v %v", diag, err)
	}
	// An extended diagnosis, then the removal of both.
	ext := pnio.DiagnosisItem{ChannelNumber: 4, ChannelProperties: 4, ErrorType: pnio.ChannelErrOvertemp, ExtErrorType: 0x8001, ExtAddValue: 99}
	if err := dev.AddDiagnosis(ctx, 1, 1, ext); err != nil {
		t.Fatal(err)
	}
	if a := <-alarms; a.Notification.USI != pnio.USIExtChannelDiagnosis {
		t.Fatalf("ext alarm %+v", a.Notification)
	}
	if err := dev.RemoveDiagnosis(ctx, 1, 1, 3, pnio.ChannelErrLineBreak); err != nil {
		t.Fatal(err)
	}
	if a := <-alarms; a.Notification.Type != pnio.AlarmDiagnosis {
		t.Fatalf("removal with diagnosis remaining: %+v", a.Notification)
	}
	if err := dev.RemoveDiagnosis(ctx, 1, 1, 4, pnio.ChannelErrOvertemp); err != nil {
		t.Fatal(err)
	}
	if a := <-alarms; a.Notification.Type != pnio.AlarmDiagnosisDisappears {
		t.Fatalf("last removal: %+v", a.Notification)
	}
	if err := dev.RemoveDiagnosis(ctx, 1, 1, 4, pnio.ChannelErrOvertemp); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if diag, err := cd.ReadDiagnosis(ctx); err != nil || len(diag) != 0 {
		t.Fatalf("diagnosis after removal %+v %v", diag, err)
	}
	// A high-priority process alarm with manufacturer data.
	err = dev.SendAlarm(ctx, device.Alarm{High: true, Type: pnio.AlarmProcess, Slot: 3, Subslot: 1, HasItem: true, USI: 0x0042, Data: []byte("hot")})
	if err != nil {
		t.Fatal(err)
	}
	a = <-alarms
	if !a.Notification.High || a.Notification.Type != pnio.AlarmProcess || string(a.Notification.Data) != "hot" {
		t.Fatalf("process alarm %+v", a.Notification)
	}
	if err := dev.SendAlarm(ctx, device.Alarm{Type: pnio.AlarmProcess, Slot: 9, Subslot: 1}); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if cd.Stats().Alarms != 5 {
		t.Fatalf("stats %+v", cd.Stats())
	}
}

func TestRecordServices(t *testing.T) {
	e := newEnv(t, 3)
	_, _, cd := running(t, e, "dut", "192.168.0.10", controller.DeviceConfig{})
	ctx := context.Background()
	var se *pnio.StatusError
	// Unknown index, slot, write length, read-only I&M0.
	_, err := cd.ReadRecord(ctx, controller.Record{Slot: 1, Subslot: 1, Index: 0x7777}, 0)
	if !errors.As(err, &se) || se.Status.Code1 != pnio.RWInvalidIndex || !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	_, err = cd.ReadRecord(ctx, controller.Record{Slot: 9, Subslot: 1, Index: 0x10}, 0)
	if !errors.As(err, &se) || se.Status.Code1 != pnio.RWInvalidSlot {
		t.Fatalf("err %v", err)
	}
	err = cd.WriteRecord(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{1, 2, 3})
	if !errors.As(err, &se) || se.Status.Code1 != pnio.RWWriteLength || se.Status.Code != pnio.CodeWrite {
		t.Fatalf("err %v", err)
	}
	err = cd.WriteRecord(ctx, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM0}, make([]byte, 60))
	if !errors.As(err, &se) || se.Status.Code1 != pnio.RWAccessDenied {
		t.Fatalf("err %v", err)
	}
	// I&M1 write and read back.
	im1 := pnio.IM1{TagFunction: "pump", TagLocation: "hall 3"}
	if err := cd.WriteRecord(ctx, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM1}, im1.Append(nil)); err != nil {
		t.Fatal(err)
	}
	b, err := cd.ReadRecord(ctx, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	blk, _, _ := pnio.ReadBlock(b)
	if got, err := pnio.DecodeIM1(blk); err != nil || got != im1 {
		t.Fatalf("I&M1 %+v %v", got, err)
	}
	bad := pnio.IM2{Date: "yesterday"}
	if err := cd.WriteRecord(ctx, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM2}, bad.Append(nil)); !errors.As(err, &se) {
		t.Fatalf("err %v", err)
	}
	// WriteMultiple with one bad item.
	sts, err := cd.WriteMultiple(ctx, []controller.WriteItem{
		{Record: controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{0, 3}},
		{Record: controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{0}},
		{Record: controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM3}, Data: (&pnio.IM3{Descriptor: "x"}).Append(nil)},
	})
	if err != nil || len(sts) != 3 || !sts[0].OK() || sts[1].Code1 != pnio.RWWriteLength || !sts[2].OK() {
		t.Fatalf("write multiple %v %v", sts, err)
	}
	if v, _ := cd.ReadRecord(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, 0); !bytes.Equal(v, []byte{0, 3}) {
		t.Fatalf("record after write multiple %x", v)
	}
	// Identification data and the module diff.
	for _, idx := range []pnio.Index{pnio.IndexRealIdentAPI, pnio.IndexRealIdentAR, pnio.IndexExpectedIdentAR, pnio.IndexRealIdentSlot, pnio.IndexExpectedIdentSubslot} {
		b, err := cd.ReadRecord(ctx, controller.Record{Slot: 1, Subslot: 1, Index: idx}, 0)
		if err != nil {
			t.Fatalf("%s: %v", idx, err)
		}
		blk, _, _ := pnio.ReadBlock(b)
		id, err := pnio.DecodeIdentificationData(blk)
		if err != nil || len(id.APIs) != 1 {
			t.Fatalf("%s: %+v %v", idx, id, err)
		}
	}
	for _, idx := range []pnio.Index{pnio.IndexModuleDiffAR, pnio.IndexIM0Filter, pnio.IndexLogBook, pnio.IndexRecordInputData, pnio.IndexDiagAllSubslot, pnio.IndexDiagDMQSAPI} {
		if _, err := cd.ReadRecord(ctx, controller.Record{Slot: 1, Subslot: 1, Index: idx}, 0); err != nil {
			t.Fatalf("%s: %v", idx, err)
		}
	}
	// A read with a small RecordDataLength is truncated.
	b, err = cd.ReadRecord(ctx, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM0}, 10)
	if err != nil || len(b) != 10 {
		t.Fatalf("%d %v", len(b), err)
	}
}

func TestReadImplicitAndSupervisorAR(t *testing.T) {
	e := newEnv(t, 4)
	dev, dn := e.device("dut", "192.168.0.10", testModel(), nil)
	cn := e.node("192.168.0.2")
	conn, _ := cn.stack.ListenUDP(0)
	cli := rpc.NewClient(conn, true, rpc.ClientOptions{})
	defer func() { _ = cli.Close() }()
	rc := controller.NewRecordClient(cli, &net.UDPAddr{IP: net.IPv4(192, 168, 0, 10), Port: rpc.Port}, dev.ObjectUUID())
	ctx := context.Background()
	im0, err := rc.ReadIM0(ctx, rpc.NilUUID, 0, 1)
	if err != nil || im0.SerialNumber != "0001" {
		t.Fatalf("implicit I&M0 %+v %v", im0, err)
	}
	// A device access AR: Connect without IOCRs, then reads and writes.
	ar := rpc.NewRandomUUID()
	req := &pnio.ConnectRequest{AR: pnio.ARBlockReq{Type: pnio.ARTypeIOSAR, ARUUID: ar, SessionKey: 1, InitiatorMAC: cn.mux.HardwareAddr(),
		InitiatorObject: pnio.ObjectUUID(1, 2, 3), Properties: pnio.ARPropStateActive | pnio.ARPropDeviceAccess, ActivityTimeoutFactor: 3, UDPRTPort: pnio.LT, StationName: "tool"}}
	resp, err := rc.Connect(ctx, req)
	if err != nil || resp.AR.ResponderMAC != dn.mux.HardwareAddr() || resp.ARServer == nil || resp.ARServer.StationName != "dut" {
		t.Fatalf("IOSAR connect %+v %v", resp, err)
	}
	if err := rc.Write(ctx, ar, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{1, 1}); err != nil {
		t.Fatal(err)
	}
	if v, err := rc.Read(ctx, ar, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, 0); err != nil || !bytes.Equal(v, []byte{1, 1}) {
		t.Fatalf("%x %v", v, err)
	}
	if ars := dev.ARs(); len(ars) != 1 || ars[0].Type != pnio.ARTypeIOSAR || ars[0].State != device.ARStateData {
		t.Fatalf("ARs %+v", ars)
	}
	// Idle for longer than its activity timeout (300 ms), it is released.
	eventually(t, 3*time.Second, "IOSAR idle release", func() bool { return len(dev.ARs()) == 0 })
	_, err = rc.Read(ctx, ar, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, 0)
	var se *pnio.StatusError
	if !errors.As(err, &se) || se.Status.Code1 != pnio.Code1CMRPC || se.Status.Code2 != pnio.CMRPCARUUIDUnknown {
		t.Fatalf("err %v", err)
	}
	// Connect rejections carry the faulty field.
	bad := *req
	bad.AR.Type = 0x0077
	bad.AR.ARUUID = rpc.NewRandomUUID()
	if _, err := rc.Connect(ctx, &bad); !errors.As(err, &se) || se.Status.Code != pnio.CodeConnect || se.Status.Code1 != pnio.Code1ARBlock || se.Status.Code2 != 4 {
		t.Fatalf("err %v", err)
	}
	bad = *req
	bad.AR.Type = pnio.ARTypeIOCARSingle
	bad.AR.ARUUID = rpc.NewRandomUUID()
	if _, err := rc.Connect(ctx, &bad); !errors.As(err, &se) || se.Status.Code2 != pnio.CMRPCIOCRMissing {
		t.Fatalf("err %v", err)
	}
}

func TestModuleDiff(t *testing.T) {
	e := newEnv(t, 5)
	dev, _ := e.device("dut", "192.168.0.10", testModel(), nil)
	ctrl, _ := e.controller("192.168.0.1")
	exp := testModel()
	exp.Slots[0].Module.IdentNumber = 0x99                                                                                                                                                // wrong module in slot 1
	exp.Slots[1].Module.Submodules[0].IdentNumber = 0x98                                                                                                                                  // wrong submodule in slot 2
	exp.Slots = append(exp.Slots, model.Slot{Number: 7, Module: model.Module{Name: "X", IdentNumber: 0x70, Submodules: []model.Submodule{{IdentNumber: 0x71, Subslot: 1, InputLen: 1}}}}) // missing
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: exp})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	diff := cd.ModuleDiff()
	if diff == nil || len(diff.APIs) != 1 {
		t.Fatalf("diff %+v", diff)
	}
	states := map[uint16]uint16{}
	for _, m := range diff.APIs[0].Modules {
		states[m.Slot] = m.State
	}
	if states[1] != pnio.ModuleStateWrong || states[7] != pnio.ModuleStateNoModule || states[2] != pnio.ModuleStateProper {
		t.Fatalf("module states %v", states)
	}
	// The matching submodule in slot 3 still exchanges data; slot 1 does
	// not.
	_ = dev.SetInput(3, 1, []byte{5, 5}, pnio.IOxSGood)
	_ = dev.SetInput(1, 1, []byte{1, 1, 1, 1, 1, 1, 1, 1}, pnio.IOxSGood)
	eventually(t, 2*time.Second, "slot 3 input", func() bool {
		in, iops, _ := cd.Input(3, 1)
		return bytes.Equal(in, []byte{5, 5}) && iops == pnio.IOxSGood
	})
	if _, iops, _ := cd.Input(1, 1); iops != pnio.IOxSBad {
		t.Fatalf("slot 1 IOPS %02X", iops)
	}
}

func TestIPAssignmentAndRename(t *testing.T) {
	e := newEnv(t, 6)
	// The device starts without an IP address.
	dev, _ := e.device("dut", "", testModel(), nil)
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), IP: suite("192.168.0.20"), AssignIP: true})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	if dev.IP().Addr.String() != "192.168.0.20" {
		t.Fatalf("device IP %s", dev.IP())
	}
	// Renaming the device with DCP aborts the AR; the controller then
	// cannot find it under its old name.
	if err := ctrl.DCP().SetName(context.Background(), cd.MAC(), "renamed", false); err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateOffline, 5*time.Second)
	if _, last := cd.State(); !errors.Is(last, profinet.ErrAborted) && !errors.Is(last, profinet.ErrNotFound) {
		t.Fatalf("last error %v", last)
	}
	if dev.StationName() != "renamed" {
		t.Fatal(dev.StationName())
	}
	// Changing the IP of a device with an AR is refused.
	if err := ctrl.DCP().SetName(context.Background(), cd.MAC(), "dut", false); err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	err = ctrl.DCP().SetIP(context.Background(), cd.MAC(), suite("192.168.0.30"), false)
	var se *dcp.SetError
	if !errors.As(err, &se) || se.Code != dcp.BlockErrorSetNotPossible {
		t.Fatalf("err %v", err)
	}
}

func TestManyDevices(t *testing.T) {
	e := newEnv(t, 7)
	ctrl, _ := e.controller("192.168.0.1")
	const n = 12
	devs := make([]*device.Device, n)
	cds := make([]*controller.Device, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("dev-%02d", i)
		devs[i], _ = e.device(name, fmt.Sprintf("192.168.0.%d", 10+i), testModel(), nil)
		var err error
		cds[i], err = ctrl.AddDevice(controller.DeviceConfig{StationName: name, Model: testModel()})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		waitState(t, cds[i], controller.StateRunning, 20*time.Second)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := []byte{byte(i), 1, 2, 3, 4, 5, 6, byte(i)}
			_ = devs[i].SetInput(1, 1, v, pnio.IOxSGood)
			_ = cds[i].SetOutput(2, 1, []byte{byte(i), 9, 9, byte(i)}, pnio.IOxSGood)
			eventually(t, 5*time.Second, "data", func() bool {
				in, _, _ := cds[i].Input(1, 1)
				out, _, _ := devs[i].Output(2, 1)
				return bytes.Equal(in, v) && bytes.Equal(out, []byte{byte(i), 9, 9, byte(i)})
			})
			if _, err := cds[i].ReadIM0(context.Background()); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	ids := map[uint16]bool{}
	for _, cd := range cds {
		ar := devs[0].ARs()
		_ = ar
		_ = cd
	}
	for _, d := range devs {
		for _, a := range d.ARs() {
			if ids[a.InputFrameID] {
				t.Fatalf("FrameID 0x%04X used twice", a.InputFrameID)
			}
			ids[a.InputFrameID] = true
		}
	}
}

func TestRecovery(t *testing.T) {
	e := newEnv(t, 8)
	var states atomic.Int32
	dev, dn := e.device("dut", "192.168.0.10", testModel(), nil)
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), ReconnectMax: 300 * time.Millisecond,
		OnState: func(_, to controller.State, _ error) {
			if to == controller.StateRunning {
				states.Add(1)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	// Link down: both watchdogs expire.
	dn.ep.SetLinkUp(false)
	waitState(t, cd, controller.StateOffline, 5*time.Second)
	eventually(t, 5*time.Second, "device abort", func() bool { return len(dev.ARs()) == 0 })
	dn.ep.SetLinkUp(true)
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	// Device restart: a new device instance on the same port.
	_ = dev.Close()
	waitState(t, cd, controller.StateOffline, 5*time.Second)
	dev2, err := device.New(device.Config{Model: testModel(), Mux: dn.mux, StationName: "dut", IP: suite("192.168.0.10"),
		ListenPacket: dn.stack.ListenPacket, NoResponseDelay: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dev2.Close() }()
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	if states.Load() < 3 || cd.Stats().Connects < 3 || cd.Stats().Aborts < 2 {
		t.Fatalf("running %d times, stats %+v", states.Load(), cd.Stats())
	}
	// Controller restart: the device aborts the old AR and accepts a new
	// one from the new controller.
	_ = ctrl.Close()
	eventually(t, 5*time.Second, "release on controller close", func() bool { return len(dev2.ARs()) == 0 })
	ctrl2, _ := e.controller("192.168.0.2")
	cd2, _ := ctrl2.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel()})
	waitState(t, cd2, controller.StateRunning, 10*time.Second)
}

// slowHandler delays the application's readiness.
type slowHandler struct {
	device.NopHandler
	delay time.Duration
}

func (h slowHandler) PrmEnd(ctx context.Context, _ device.ARInfo) error {
	select {
	case <-time.After(h.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestActivityTimeout(t *testing.T) {
	e := newEnv(t, 9)
	dev, _ := e.device("dut", "192.168.0.10", testModel(), slowHandler{delay: 5 * time.Second})
	ctrl, _ := e.controller("192.168.0.1")
	cd, _ := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), ActivityTimeout: 300 * time.Millisecond, ReconnectMin: time.Hour})
	waitState(t, cd, controller.StateWaitAppReady, 5*time.Second)
	waitState(t, cd, controller.StateOffline, 5*time.Second)
	eventually(t, 3*time.Second, "device abort", func() bool { return len(dev.ARs()) == 0 })
}

func TestSecondControllerRejected(t *testing.T) {
	e := newEnv(t, 10)
	_, _, _ = running(t, e, "dut", "192.168.0.10", controller.DeviceConfig{})
	ctrl2, _ := e.controller("192.168.0.2")
	cd2, _ := ctrl2.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), ReconnectMin: time.Hour})
	eventually(t, 5*time.Second, "connect failure", func() bool { _, err := cd2.State(); return err != nil })
	_, last := cd2.State()
	var se *pnio.StatusError
	if !errors.As(last, &se) || se.Status.Code1 != pnio.Code1CMRPC || se.Status.Code2 != pnio.CMRPCOutOfARs || !errors.Is(last, profinet.ErrResourceExhausted) {
		t.Fatalf("last error %v", last)
	}
}

func TestResetToFactory(t *testing.T) {
	e := newEnv(t, 11)
	dev, _, cd := running(t, e, "dut", "192.168.0.10", controller.DeviceConfig{ReconnectMin: time.Hour})
	ctrl := e.net
	_ = ctrl
	c := cd
	ctx := context.Background()
	cl, _ := e.controller("192.168.0.3")
	if err := cl.DCP().ResetToFactory(ctx, c.MAC(), dcp.ResetCommunication); err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateOffline, 5*time.Second)
	if dev.StationName() != "" || !dev.IP().IsZero() {
		t.Fatalf("after reset: %q %s", dev.StationName(), dev.IP())
	}
	if err := cl.DCP().Signal(ctx, c.MAC()); err != nil {
		t.Fatal(err)
	}
}

// TestTopology: two devices learn each other with LLDP and report it in
// PDPortDataReal (0x802A) and PDRealData (0xF841), which a tool reads
// implicitly to draw the topology.
func TestTopology(t *testing.T) {
	e := newEnv(t, 11)
	m := testModel()
	devA, _ := e.device("dev-a", "192.168.0.10", m, nil)
	_, dnB := e.device("dev-b", "192.168.0.11", testModel(), nil)
	cn := e.node("192.168.0.2")
	conn, _ := cn.stack.ListenUDP(0)
	cli := rpc.NewClient(conn, true, rpc.ClientOptions{})
	defer func() { _ = cli.Close() }()
	rc := controller.NewRecordClient(cli, &net.UDPAddr{IP: net.IPv4(192, 168, 0, 10), Port: rpc.Port}, devA.ObjectUUID())
	ctx := context.Background()
	var ports []pnio.PDPortDataReal
	eventually(t, 5*time.Second, "dev-a sees dev-b", func() bool {
		b, err := rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 0, Subslot: 0xFFFF, Index: pnio.IndexPDRealData}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if ports, err = pnio.DecodePDRealData(b); err != nil {
			t.Fatal(err)
		}
		return len(ports) == 1 && len(ports[0].Peers) == 1
	})
	p := ports[0]
	peer := p.Peers[0]
	if p.Subslot != 0x8001 || p.OwnPortID != "port-001" || p.LinkState != pnio.LinkStateUp || peer.ChassisID != "dev-b" || peer.PortID != "port-001.dev-b" || peer.MAC != dnB.mux.HardwareAddr() {
		t.Fatalf("port %+v (dev-b is %s)", p, dnB.mux.HardwareAddr())
	}
	b, err := rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 0, Subslot: 0x8001, Index: pnio.IndexPDPortDataReal}, 0)
	if err != nil {
		t.Fatal(err)
	}
	blk, _, err := pnio.ReadBlock(b)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := pnio.DecodePDPortDataReal(blk); err != nil || got.Peers[0].ChassisID != "dev-b" {
		t.Fatalf("0x802A %+v %v", got, err)
	}
	// Only a port subslot has port data.
	_, err = rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexPDPortDataReal}, 0)
	var se *pnio.StatusError
	if !errors.As(err, &se) || se.Status.Code1 != pnio.RWInvalidIndex {
		t.Fatalf("0x802A of the DAP: %v", err)
	}
}
