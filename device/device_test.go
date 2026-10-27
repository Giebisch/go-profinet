// SPDX-License-Identifier: MIT

package device

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/alarm"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
)

func TestARStateMachine(t *testing.T) {
	names := map[ARState]string{
		ARStateNone: "None", ARStateParameterizing: "Parameterizing", ARStateAppReadyPending: "ApplicationReadyPending",
		ARStateData: "Data", ARStateClosed: "Closed", ARState(99): "ARState(?)",
	}
	for s, want := range names {
		if s.String() != want {
			t.Errorf("%d: %q", s, s.String())
		}
	}
	allowed := map[[2]ARState]bool{
		{ARStateNone, ARStateParameterizing}: true, {ARStateNone, ARStateData}: true, {ARStateNone, ARStateClosed}: true,
		{ARStateParameterizing, ARStateAppReadyPending}: true, {ARStateParameterizing, ARStateClosed}: true,
		{ARStateAppReadyPending, ARStateData}: true, {ARStateAppReadyPending, ARStateClosed}: true,
		{ARStateData, ARStateParameterizing}: true, {ARStateData, ARStateClosed}: true,
	}
	for from := ARStateNone; from <= ARStateClosed+1; from++ {
		for to := ARStateNone; to <= ARStateClosed+1; to++ {
			if got := CanTransition(from, to); got != allowed[[2]ARState{from, to}] {
				t.Errorf("%s → %s: %v", from, to, got)
			}
		}
	}
}

func TestNopHandler(t *testing.T) {
	var h Handler = NopHandler{}
	ctx := context.Background()
	if _, err := h.ReadRecord(ctx, RecordRequest{}); asStatus(err, false).Code1 != pnio.RWInvalidIndex {
		t.Fatalf("read %v", err)
	}
	if err := h.WriteRecord(ctx, RecordRequest{Modeled: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteRecord(ctx, RecordRequest{}, nil); asStatus(err, true).Code1 != pnio.RWInvalidIndex {
		t.Fatalf("write %v", err)
	}
	if err := h.PrmEnd(ctx, ARInfo{}); err != nil {
		t.Fatal(err)
	}
	if h.ResetToFactory(dcp.ResetAllData) != dcp.BlockErrorNone {
		t.Fatal("reset refused")
	}
	h.StateChanged(ARInfo{}, ARStateNone, ARStateData)
	h.AlarmAck(ARInfo{}, pnio.AlarmAck{})
	h.Signal()
	h.NameChanged("x", true)
	h.IPChanged(dcp.IPSuite{}, false)
	var se *pnio.StatusError
	if err := RecordError(pnio.RWBusy, 7); !errors.As(err, &se) || se.Status.Decode != pnio.DecodePNIORW || se.Status.Code1 != pnio.RWBusy || se.Status.Code2 != 7 {
		t.Fatalf("RecordError %v", err)
	}
}

func TestNewErrors(t *testing.T) {
	n := newNet(t)
	nd := n.node("192.168.0.50")
	if _, err := New(Config{Mux: nd.mux}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("nil model: %v", err)
	}
	if _, err := New(Config{Model: testModel()}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("nil mux: %v", err)
	}
	bad := testModel()
	bad.Identity.VendorID, bad.Identity.DeviceID = 0, 0
	if _, err := New(Config{Model: bad, Mux: nd.mux}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("invalid model: %v", err)
	}
	if _, err := New(Config{Model: testModel(), Mux: nd.mux, StationName: "Bad_Name!"}); err == nil {
		t.Fatal("invalid station name accepted")
	}
	for _, failAt := range []int32{1, 2} {
		var calls atomic.Int32
		lp := func(ctx context.Context, network, address string) (net.PacketConn, error) {
			if calls.Add(1) == failAt {
				return nil, errors.New("no sockets")
			}
			return nd.stack.ListenPacket(ctx, network, address)
		}
		if _, err := New(Config{Model: testModel(), Mux: nd.mux, ListenPacket: lp, DisableLLDP: true}); err == nil {
			t.Fatalf("ListenPacket failure %d accepted", failAt)
		}
	}
	// A closed port fails at the first subscription.
	closed := n.node("192.168.0.51")
	_ = closed.mux.Close()
	if _, err := New(Config{Model: testModel(), Mux: closed.mux, ListenPacket: closed.stack.ListenPacket}); err == nil {
		t.Fatal("closed mux accepted")
	}
}

func TestNewDefaults(t *testing.T) {
	n := newNet(t)
	nd := n.node("192.168.0.52")
	m := testModel()
	m.Capabilities.MaxARs = 0
	m.Identity.Instance = 0
	// No ListenPacket: the operating system's UDP stack on a free port.
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP: %v", err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	_ = l.Close()
	d, err := New(Config{Model: m, Mux: nd.mux, RPCPort: port, DisableLLDP: true})
	if err != nil {
		t.Skipf("OS UDP stack: %v", err)
	}
	defer func() { _ = d.Close() }()
	if m.Capabilities.MaxARs != 2 {
		t.Fatalf("MaxARs %d", m.Capabilities.MaxARs)
	}
	if d.ObjectUUID() != pnio.ObjectUUID(1, m.Identity.DeviceID, m.Identity.VendorID) {
		t.Fatalf("object %s", d.ObjectUUID())
	}
	if a, ok := d.RPCAddr().(*net.UDPAddr); !ok || a.Port != port {
		t.Fatalf("RPC address %v", d.RPCAddr())
	}
	if d.StationName() != "" || !d.IP().IsZero() {
		t.Fatalf("%q %s", d.StationName(), d.IP())
	}
	id := station{d}.Identity()
	if id.IPInfo != 0 || id.Instance != 1 || id.Alias != "" || id.Role != dcp.RoleIODevice {
		t.Fatalf("identity %+v", id)
	}
}

func TestProcessImages(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	if d.StationName() != "dut" || d.IP() != suite(devIP) {
		t.Fatalf("%q %s", d.StationName(), d.IP())
	}
	if a, ok := d.RPCAddr().(*net.UDPAddr); !ok || a.Port != rpc.Port {
		t.Fatalf("RPC address %v", d.RPCAddr())
	}
	if err := d.SetInput(9, 1, nil, pnio.IOxSGood); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if err := d.SetInput(2, 1, []byte{1, 2, 3, 4}, pnio.IOxSGood); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("output-only submodule: %v", err)
	}
	if err := d.SetInput(1, 1, []byte{1}, pnio.IOxSGood); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
	if err := d.SetInput(4, 1, nil, pnio.IOxSGood); err != nil {
		t.Fatalf("NO_IO: %v", err)
	}
	if _, _, ok := d.Output(1, 1); ok {
		t.Fatal("input-only submodule has outputs")
	}
	if out, iops, ok := d.Output(2, 1); !ok || iops != pnio.IOxSBad || !bytes.Equal(out, make([]byte, 4)) {
		t.Fatalf("initial output %x %02X %v", out, iops, ok)
	}
	id := station{d}.Identity()
	if id.NameOfStation != "dut" || id.IPInfo != dcp.IPInfoSet || id.VendorID != 0xF000 || id.TypeOfStation != "go-profinet device test" {
		t.Fatalf("identity %+v", id)
	}
	if li := d.lldpInfo(); li.StationName != "dut" || li.IP != suite(devIP).Addr || li.PortName != "port-001" {
		t.Fatalf("LLDP %+v", li)
	}
}

func TestCyclicWithController(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	cn := n.node("192.168.0.1")
	ctrl, err := controller.New(controller.Config{Mux: cn.mux, ListenPacket: cn.stack.ListenPacket, StationName: "plc"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), ReductionRatio: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cd.WaitState(ctx, controller.StateRunning); err != nil {
		t.Fatal(err)
	}
	waitAR(t, d, cd.ARUUID(), ARStateData)
	in := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if err := d.SetInput(1, 1, in, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	if err := cd.SetOutput(2, 1, []byte{9, 8, 7, 6}, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	if err := cd.SetOutput(3, 1, []byte{5, 5}, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "cyclic data", func() bool {
		got, iops, _ := cd.Input(1, 1)
		out, oiops, _ := d.Output(2, 1)
		out3, _, _ := d.Output(3, 1)
		return bytes.Equal(got, in) && iops == pnio.IOxSGood && bytes.Equal(out, []byte{9, 8, 7, 6}) && oiops == pnio.IOxSGood && bytes.Equal(out3, []byte{5, 5})
	})
	// The controller's consumer status of our inputs.
	ars := d.ARs()
	if len(ars) != 1 {
		t.Fatalf("ARs %+v", ars)
	}
	d.mu.Lock()
	a := d.ars[ars[0].UUID]
	d.mu.Unlock()
	eventually(t, 5*time.Second, "IOCS", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.iocs[key{1, 1}] == pnio.IOxSGood
	})
	// A diagnosis alarm is acknowledged by the controller.
	if err := d.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: 1, ErrorType: pnio.ChannelErrShortCircuit}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "alarm ack", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.acks) == 1
	})
	if err := d.RemoveDiagnosis(ctx, 1, 1, 1, pnio.ChannelErrShortCircuit); err != nil {
		t.Fatal(err)
	}
	// Closing the controller releases the AR.
	_ = ctrl.Close()
	waitAR(t, d, ars[0].UUID, ARStateClosed)
	if _, iops, _ := d.Output(2, 1); iops != pnio.IOxSBad {
		t.Fatalf("output IOPS after release %02X", iops)
	}
	// Aborting a closed AR does nothing.
	d.abort(a, pnio.AbortUser, true)
	if r := a.info().CloseReason; r != 0 {
		t.Fatalf("reason %s", r)
	}
}

func TestImages(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	subs := subIO(testModel())
	inL, _ := rt.ComputeLayout(pnio.IOCRInput, subs)
	outL, _ := rt.ComputeLayout(pnio.IOCROutput, subs)
	a := &ar{d: d, inLayout: inL, outLayout: outL, iocs: map[key]uint8{}, match: map[key]bool{{1, 1}: true, {2, 1}: true}}
	d.claimOwnership(a)
	if err := d.SetInput(1, 1, []byte{1, 1, 1, 1, 1, 1, 1, 1}, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	c := make([]byte, inL.DataLength)
	d.fillInputs(a, c)
	o1, _ := inL.Find(0, 1, 1)
	o3, _ := inL.Find(0, 3, 1)
	if !bytes.Equal(c[o1.Offset:o1.Offset+8], []byte{1, 1, 1, 1, 1, 1, 1, 1}) || c[o1.IOPSOffset()] != pnio.IOxSGood || c[o3.IOPSOffset()] != pnio.IOxSBad {
		t.Fatalf("inputs %x", c)
	}
	s2, _ := inL.FindIOCS(0, 2, 1)
	s3, _ := inL.FindIOCS(0, 3, 1)
	if c[s2.Offset] != pnio.IOxSGood || c[s3.Offset] != pnio.IOxSBad {
		t.Fatalf("IOCS %x", c)
	}
	// Outputs: an invalid frame is ignored, a stopped provider's data is
	// bad, a running one's good.
	out := make([]byte, outL.DataLength)
	o2, _ := outL.Find(0, 2, 1)
	copy(out[o2.Offset:], []byte{4, 3, 2, 1})
	out[o2.IOPSOffset()] = pnio.IOxSGood
	d.storeOutputs(a, out, 0)
	if v, _, _ := d.Output(2, 1); !bytes.Equal(v, make([]byte, 4)) {
		t.Fatalf("invalid frame stored %x", v)
	}
	d.storeOutputs(a, out, rt.DataValid|rt.StatePrimary)
	if v, iops, _ := d.Output(2, 1); !bytes.Equal(v, []byte{4, 3, 2, 1}) || iops != pnio.IOxSBad {
		t.Fatalf("stopped %x %02X", v, iops)
	}
	d.storeOutputs(a, out, rt.DefaultDataStatus)
	if v, iops, _ := d.Output(2, 1); !bytes.Equal(v, []byte{4, 3, 2, 1}) || iops != pnio.IOxSGood {
		t.Fatalf("running %x %02X", v, iops)
	}
	// Slot 3 is not proper: its outputs stay.
	if v, _, _ := d.Output(3, 1); !bytes.Equal(v, []byte{0, 0}) {
		t.Fatalf("slot 3 %x", v)
	}
}

func TestStation(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	applyErr := atomic.Bool{}
	d, _ := n.device(h, func(c *Config) {
		apply := c.ApplyIP
		c.ApplyIP = func(s dcp.IPSuite, perm bool) error {
			if applyErr.Load() {
				return errors.New("read-only stack")
			}
			return apply(s, perm)
		}
	})
	p := n.peer(d, "192.168.0.2", true)
	st := station{d}

	// Signal.
	if st.Signal() != dcp.BlockErrorNone || h.signals != 1 {
		t.Fatal("signal")
	}
	// The same name keeps the ARs; another aborts them.
	req := p.ioCAR()
	p.establish(req)
	if st.SetName("dut", false) != dcp.BlockErrorNone || len(d.ARs()) != 1 {
		t.Fatal("same name")
	}
	// An IP change is refused with an IOCAR; the same address is fine.
	if st.SetIP(suite("192.168.0.11"), false) != dcp.BlockErrorSetNotPossible {
		t.Fatal("IP change with an AR accepted")
	}
	if st.SetIP(suite(devIP), true) != dcp.BlockErrorNone {
		t.Fatal("same IP refused")
	}
	if st.SetName("other", true) != dcp.BlockErrorNone || d.StationName() != "other" {
		t.Fatal("rename")
	}
	h.waitReason(t, req.AR.ARUUID, pnio.AbortDCPNameChanged)
	// The stack refuses the address.
	applyErr.Store(true)
	if st.SetIP(suite("192.168.0.11"), false) != dcp.BlockErrorSetNotPossible || d.IP() != suite(devIP) {
		t.Fatal("ApplyIP error ignored")
	}
	applyErr.Store(false)
	// An IOSAR does not keep the address.
	sar := p.ioSAR()
	if _, err := p.connect(sar); err != nil {
		t.Fatal(err)
	}
	if st.SetIP(suite("192.168.0.11"), false) != dcp.BlockErrorNone || d.IP() != suite("192.168.0.11") {
		t.Fatal("IP change with an IOSAR refused")
	}
	if st.SetIP(suite(devIP), false) != dcp.BlockErrorNone {
		t.Fatal("IP change back refused")
	}
	// Without ApplyIP the suite is only recorded.
	d.cfg.ApplyIP = nil
	if st.SetIP(suite(devIP), false) != dcp.BlockErrorNone {
		t.Fatal("SetIP without ApplyIP")
	}
	h.mu.Lock()
	if len(h.names) != 2 || h.names[1] != "other" || len(h.ips) != 4 {
		t.Fatalf("names %v ips %v", h.names, h.ips)
	}
	h.mu.Unlock()
}

func TestResetToFactory(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h, func(c *Config) { c.IM = IMData{IM1: pnio.IM1{TagFunction: "pump"}} })
	p := n.peer(d, "192.168.0.2", false)
	st := station{d}
	ctx := context.Background()
	write := func() {
		t.Helper()
		sar := p.ioSAR()
		if _, err := p.connect(sar); err != nil {
			t.Fatal(err)
		}
		if err := p.rc.Write(ctx, sar.AR.ARUUID, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{7, 7}); err != nil {
			t.Fatal(err)
		}
	}
	record := func() []byte {
		d.mu.Lock()
		defer d.mu.Unlock()
		return append([]byte(nil), d.records[recKey{2, 1, 0x10}]...)
	}

	// The handler refuses.
	h.resetCode = dcp.BlockErrorResource
	if st.ResetToFactory(dcp.ResetAllData) != dcp.BlockErrorResource {
		t.Fatal("refusal ignored")
	}
	h.resetCode = dcp.BlockErrorNone

	// Application data: records and I&M, the name stays.
	write()
	if st.ResetToFactory(dcp.ResetApplicationData) != dcp.BlockErrorNone {
		t.Fatal("reset refused")
	}
	if len(d.ARs()) != 0 || !bytes.Equal(record(), []byte{0, 1}) || d.im.IM1.TagFunction != "" || d.StationName() != "dut" || d.IP().IsZero() {
		t.Fatalf("after application reset: %d ARs, record %x, name %q", len(d.ARs()), record(), d.StationName())
	}
	// Communication: name and IP, the records stay.
	write()
	if st.ResetToFactory(dcp.ResetCommunication) != dcp.BlockErrorNone {
		t.Fatal("reset refused")
	}
	if len(d.ARs()) != 0 || !bytes.Equal(record(), []byte{7, 7}) || d.StationName() != "" || !d.IP().IsZero() {
		t.Fatalf("after communication reset: record %x, name %q, IP %s", record(), d.StationName(), d.IP())
	}
	// All data.
	st.SetName("dut", false)
	st.SetIP(suite(devIP), false)
	write()
	if st.ResetToFactory(dcp.ResetAllData) != dcp.BlockErrorNone {
		t.Fatal("reset refused")
	}
	if !bytes.Equal(record(), []byte{0, 1}) || d.StationName() != "" || !d.IP().IsZero() {
		t.Fatalf("after full reset: record %x, name %q", record(), d.StationName())
	}
}

func TestAlarms(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	ctx := context.Background()
	if err := d.SendAlarm(ctx, Alarm{Type: pnio.AlarmProcess, Slot: 9, Subslot: 1}); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("unknown slot %v", err)
	}
	if err := d.SendAlarm(ctx, Alarm{Type: pnio.AlarmProcess, Slot: 1, Subslot: 9}); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("unknown subslot %v", err)
	}
	if err := d.SendAlarm(ctx, Alarm{Type: pnio.AlarmProcess, Slot: 1, Subslot: 1}); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("no AR %v", err)
	}
	req := p.ioCAR()
	resp, err := p.connect(req)
	if err != nil {
		t.Fatal(err)
	}
	// An AR that is not in Data yet receives no alarm.
	if err := d.SendAlarm(ctx, Alarm{Type: pnio.AlarmProcess, Slot: 1, Subslot: 1}); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("parameterizing AR %v", err)
	}
	if err := p.prmEnd(req); err != nil {
		t.Fatal(err)
	}
	waitAR(t, d, req.AR.ARUUID, ARStateData)

	// The controller acknowledges by its mode.
	var mode atomic.Int32 // 0 ack OK, 1 ack with an error, 2 no ack
	got := make(chan pnio.AlarmNotification, 8)
	p.openAlarms(req, resp, func(_ bool, an pnio.AlarmNotification, ep *alarm.Endpoint) {
		got <- an
		switch mode.Load() {
		case 0:
			ack := pnio.AckFor(&an, pnio.Status{})
			go func() { _ = ep.Send(context.Background(), ack.Append(nil)) }()
		case 1:
			ack := pnio.AckFor(&an, pnio.ServiceStatus(pnio.CodePNIO, pnio.Code1AlarmAck, 1))
			go func() { _ = ep.Send(context.Background(), ack.Append(nil)) }()
		}
	})
	al := Alarm{High: true, Type: pnio.AlarmProcess, Slot: 3, Subslot: 1, HasItem: true, USI: 0x0042, Data: []byte("hot"), Specifier: pnio.AlarmSpecManufDiag}
	if err := d.SendAlarm(ctx, al); err != nil {
		t.Fatal(err)
	}
	an := <-got
	if !an.High || an.ModuleIdentNumber != 0x30 || an.SubmoduleIdentNumber != 0x31 || string(an.Data) != "hot" || an.Specifier&pnio.AlarmSpecManufDiag == 0 {
		t.Fatalf("notification %+v", an)
	}
	eventually(t, 2*time.Second, "first ack", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.acks) == 1
	})
	// Low priority, with an error acknowledgement.
	mode.Store(1)
	al.High = false
	var se *pnio.StatusError
	if err := d.SendAlarm(ctx, al); !errors.As(err, &se) || se.Status.Code1 != pnio.Code1AlarmAck {
		t.Fatalf("err %v", err)
	}
	if an := <-got; an.High || an.Sequence() != 1 {
		t.Fatalf("low notification %+v", an)
	}
	// Too long for MaxAlarmDataLength.
	if err := d.SendAlarm(ctx, Alarm{Type: pnio.AlarmProcess, Slot: 3, Subslot: 1, HasItem: true, USI: 1, Data: make([]byte, 300)}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
	// No acknowledgement: the caller's context, then the device's timer.
	mode.Store(2)
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	if err := d.SendAlarm(cctx, al); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	cancel()
	<-got
	if err := d.SendAlarm(ctx, al); !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	<-got
	// The AR is aborted while an alarm waits for its acknowledgement.
	done := make(chan error, 1)
	go func() { done <- d.SendAlarm(ctx, al) }()
	<-got
	if err := p.release(req); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, profinet.ErrAborted) {
		t.Fatalf("err %v", err)
	}
	eventually(t, 2*time.Second, "second ack", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.acks) == 2
	})
}

func TestAlarmSendFailure(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	p := n.peer(d, "192.168.0.2", true)
	req := p.ioCAR()
	req.AlarmCR.RTATimeoutFactor = 1
	p.establish(req)
	// Nobody acknowledges the transport: the alarm fails and the AR is
	// aborted.
	if err := d.SendAlarm(context.Background(), Alarm{Type: pnio.AlarmProcess, Slot: 1, Subslot: 1}); !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	h.waitReason(t, req.AR.ARUUID, pnio.AbortAlarmSend)
}

func TestAlarmData(t *testing.T) {
	n := newNet(t)
	h := newRecHandler()
	d, _ := n.device(h)
	a := &ar{d: d, ackWait: map[uint16]chan pnio.AlarmAck{}}
	d.alarmData(a, []byte{1})
	other := pnio.ControlBlock{Type: pnio.BlockPrmEndReq}
	d.alarmData(a, other.Append(nil))
	ack := (&pnio.AlarmAck{Type: pnio.AlarmProcess}).Append(nil)
	d.alarmData(a, shortBlock(ack))
	ch := make(chan pnio.AlarmAck, 1)
	a.ackWait[5] = ch
	d.alarmData(a, (&pnio.AlarmAck{Type: pnio.AlarmProcess, Specifier: 5}).Append(nil))
	select {
	case got := <-ch:
		if got.Specifier != 5 {
			t.Fatalf("ack %+v", got)
		}
	default:
		t.Fatal("no ack delivered")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.acks) != 1 {
		t.Fatalf("acks %+v", h.acks)
	}
}
