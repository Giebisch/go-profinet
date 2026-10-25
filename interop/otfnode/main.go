// SPDX-License-Identifier: MIT

// Command otfnode is go-profinet's side of the interoperability tests: it
// runs in a Linux container on the same network as a reference stack of
// otfabric/profinet-interop and acts as IO-Controller, IO-Device or DCP
// tool on a real interface (AF_PACKET), printing JSON. The interop tests
// (build tag interop) build it and start it.
//
//	otfnode controller -i eth0 -station NAME -model FILE [flags]
//	otfnode device     -i eth0 -station NAME [-model FILE] [-gsdml-out FILE]
//	otfnode dcp        -i eth0 identify|get|set-name|set-ip|signal|reset ...
//	otfnode lldp       -i eth0 -wait 12s
//
// A model FILE is a model.Device in JSON or a GSDML file.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/gsdml"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/osport"
	"github.com/otfabric/go-profinet/lldp"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pcap"
	"github.com/otfabric/go-profinet/pnio"
)

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: otfnode controller|device|dcp|lldp ..."))
	}
	var err error
	switch os.Args[1] {
	case "controller":
		err = runController(os.Args[2:])
	case "device":
		err = runDevice(os.Args[2:])
	case "dcp":
		err = runDCP(os.Args[2:])
	case "lldp":
		err = runLLDP(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	emit(map[string]any{"event": "error", "error": err.Error()})
	os.Exit(1)
}

var outMu sync.Mutex

func emit(v any) {
	outMu.Lock()
	defer outMu.Unlock()
	b, _ := json.Marshal(v)
	os.Stdout.Write(append(b, '\n'))
}

// tappedPort records every frame sent and received into a pcap file.
type tappedPort struct {
	l2.Port
	w *pcap.Writer
}

func (p *tappedPort) ReadFrame(ctx context.Context, b []byte) (int, error) {
	n, err := p.Port.ReadFrame(ctx, b)
	if err == nil {
		_ = p.w.WriteFrame(time.Now(), b[:n])
	}
	return n, err
}

func (p *tappedPort) WriteFrame(ctx context.Context, b []byte) error {
	err := p.Port.WriteFrame(ctx, b)
	if err == nil {
		_ = p.w.WriteFrame(time.Now(), ethernet.Pad(append([]byte(nil), b...), 0))
	}
	return err
}

func openPort(iface, pcapFile string) (*l2.Mux, func(), error) {
	p, err := osport.Open(iface, osport.Options{})
	if err != nil {
		return nil, nil, err
	}
	port := p
	var f *os.File
	if pcapFile != "" {
		if f, err = os.Create(pcapFile); err != nil {
			p.Close()
			return nil, nil, err
		}
		w, err := pcap.NewWriter(f)
		if err != nil {
			p.Close()
			return nil, nil, err
		}
		port = &tappedPort{Port: p, w: w}
	}
	mux := l2.NewMux(port)
	return mux, func() {
		mux.Close()
		p.Close()
		if f != nil {
			f.Close()
		}
	}, nil
}

func loadModel(path, dap string) (*model.Device, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".xml") {
		doc, err := gsdml.ParseBytes(b)
		if err != nil {
			return nil, err
		}
		return gsdml.ToModel(doc, dap)
	}
	var m model.Device
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, m.Validate()
}

// ifaceSuite returns the first IPv4 suite of the interface.
func ifaceSuite(name string) dcp.IPSuite {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return dcp.IPSuite{}
	}
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			ip, _ := netip.AddrFromSlice(n.IP.To4())
			mask, _ := netip.AddrFromSlice(net.IP(n.Mask).To4())
			return dcp.IPSuite{Addr: ip, Mask: mask, Gateway: netip.IPv4Unspecified()}
		}
	}
	return dcp.IPSuite{}
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

// parseAddr parses slot:subslot[:index[:hex]].
func parseAddr(s string, parts int) (slot, subslot uint16, index pnio.Index, data []byte, err error) {
	f := strings.Split(s, ":")
	if len(f) < parts {
		return 0, 0, 0, nil, fmt.Errorf("%q: want %d fields", s, parts)
	}
	num := func(x string) (uint16, error) {
		v, err := strconv.ParseUint(x, 0, 16)
		return uint16(v), err
	}
	if slot, err = num(f[0]); err != nil {
		return
	}
	if subslot, err = num(f[1]); err != nil {
		return
	}
	if len(f) > 2 {
		var v uint16
		if v, err = num(f[2]); err != nil {
			return
		}
		index = pnio.Index(v)
	}
	if len(f) > 3 {
		data, err = hex.DecodeString(f[3])
	}
	return
}

func runController(args []string) error {
	fs := flag.NewFlagSet("controller", flag.ExitOnError)
	iface := fs.String("i", "eth0", "interface")
	station := fs.String("station", "", "station name of the device")
	modelFile := fs.String("model", "", "expected configuration (model JSON or GSDML)")
	dap := fs.String("dap", "", "GSDML device access point ID")
	advanced := fs.Bool("advanced", false, "advanced startup with RTClass 2")
	rr := fs.Uint("rr", 8, "reduction ratio (send clock 1 ms)")
	dhf := fs.Uint("dhf", 10, "data hold factor")
	dur := fs.Duration("duration", 3*time.Second, "how long to run the AR")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to wait for Running")
	im0 := fs.Bool("im0", false, "read I&M0")
	diag := fs.Bool("diag", false, "read the device diagnosis")
	alarms := fs.Int("wait-alarms", 0, "wait for this many alarms")
	pcapFile := fs.String("pcap", "", "write the frames to this pcap file")
	assignIP := fs.String("assign-ip", "", "assign this IP/prefix with DCP before connecting")
	var outputs, reads, writes, params multi
	fs.Var(&outputs, "output", "slot:subslot:0:hex output data (repeatable)")
	fs.Var(&reads, "read", "slot:subslot:index record to read while running (repeatable)")
	fs.Var(&writes, "write", "slot:subslot:index:hex record to write while running (repeatable)")
	fs.Var(&params, "param", "slot:subslot:index:hex parameter written before PrmEnd (repeatable)")
	fs.Parse(args)
	m, err := loadModel(*modelFile, *dap)
	if err != nil {
		return fmt.Errorf("model: %w", err)
	}
	mux, closePort, err := openPort(*iface, *pcapFile)
	if err != nil {
		return err
	}
	defer closePort()
	ctrl, err := controller.New(controller.Config{Mux: mux, StationName: "otf-controller"})
	if err != nil {
		return err
	}
	defer ctrl.Close()
	var alarmMu sync.Mutex
	var gotAlarms []map[string]any
	alarmCh := make(chan struct{}, 64)
	cfg := controller.DeviceConfig{
		StationName: *station, Model: m, Advanced: *advanced, ReductionRatio: uint16(*rr),
		DataHoldFactor: uint16(*dhf), WatchdogFactor: uint16(*dhf), ReconnectMax: 2 * time.Second,
		OnState: func(from, to controller.State, err error) {
			e := map[string]any{"event": "state", "from": from.String(), "to": to.String()}
			if err != nil {
				e["error"] = err.Error()
			}
			emit(e)
		},
		OnAlarm: func(a controller.AlarmEvent) {
			n := a.Notification
			e := map[string]any{"event": "alarm", "type": n.Type.String(), "slot": n.Slot, "subslot": n.Subslot, "high": n.High,
				"usi": n.USI, "data": hex.EncodeToString(n.Data), "sequence": n.Sequence()}
			emit(e)
			alarmMu.Lock()
			gotAlarms = append(gotAlarms, e)
			alarmMu.Unlock()
			alarmCh <- struct{}{}
		},
	}
	if *assignIP != "" {
		pfx, err := netip.ParsePrefix(*assignIP)
		if err != nil {
			return err
		}
		mask := net.CIDRMask(pfx.Bits(), 32)
		ma, _ := netip.AddrFromSlice(mask)
		cfg.IP = dcp.IPSuite{Addr: pfx.Addr(), Mask: ma, Gateway: netip.IPv4Unspecified()}
		cfg.AssignIP = true
	}
	for _, p := range params {
		slot, sub, idx, data, err := parseAddr(p, 4)
		if err != nil {
			return err
		}
		cfg.Parameters = append(cfg.Parameters, controller.Parameter{Slot: slot, Subslot: sub, Index: idx, Data: data})
	}
	d, err := ctrl.AddDevice(cfg)
	if err != nil {
		return err
	}
	report := map[string]any{"event": "report", "station": *station}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := d.WaitState(ctx, controller.StateRunning); err != nil {
		st, last := d.State()
		report["running"] = false
		report["state"] = st.String()
		if last != nil {
			report["error"] = last.Error()
		}
		emit(report)
		return nil
	}
	report["running"] = true
	report["mac"] = d.MAC().String()
	report["ip"] = d.IP().Addr.String()
	if diff := d.ModuleDiff(); diff != nil {
		report["moduleDiff"] = diff
	}
	for _, o := range outputs {
		slot, sub, _, data, err := parseAddr(o, 4)
		if err != nil {
			return err
		}
		if err := d.SetOutput(slot, sub, data, pnio.IOxSGood); err != nil {
			return err
		}
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	if *im0 {
		v, err := d.ReadIM0(rctx)
		if err != nil {
			report["im0Error"] = err.Error()
		} else {
			report["im0"] = v
		}
	}
	var results []map[string]any
	for _, w := range writes {
		slot, sub, idx, data, err := parseAddr(w, 4)
		if err != nil {
			return err
		}
		r := map[string]any{"op": "write", "slot": slot, "subslot": sub, "index": uint16(idx)}
		if err := d.WriteRecord(rctx, controller.Record{Slot: slot, Subslot: sub, Index: idx}, data); err != nil {
			r["error"] = err.Error()
			var se *pnio.StatusError
			if errors.As(err, &se) {
				r["status"] = se.Status.String()
			}
		}
		results = append(results, r)
	}
	for _, rd := range reads {
		slot, sub, idx, _, err := parseAddr(rd, 3)
		if err != nil {
			return err
		}
		r := map[string]any{"op": "read", "slot": slot, "subslot": sub, "index": uint16(idx)}
		v, err := d.ReadRecord(rctx, controller.Record{Slot: slot, Subslot: sub, Index: idx}, 0)
		if err != nil {
			r["error"] = err.Error()
			var se *pnio.StatusError
			if errors.As(err, &se) {
				r["status"] = se.Status.String()
			}
		} else {
			r["data"] = hex.EncodeToString(v)
		}
		results = append(results, r)
	}
	report["records"] = results
	if *diag {
		v, err := d.ReadDiagnosis(rctx)
		if err != nil {
			report["diagnosisError"] = err.Error()
		} else {
			report["diagnosis"] = v
		}
	}
	deadline := time.After(*dur)
	for waiting := *alarms; waiting > 0; {
		select {
		case <-alarmCh:
			waiting--
		case <-deadline:
			waiting = 0
			deadline = nil
		}
	}
	if deadline != nil {
		<-deadline
	}
	inputs := map[string]any{}
	m.Each(func(p model.PluggedSubmodule) {
		if p.Submodule.InputLen == 0 {
			return
		}
		v, iops, _ := d.Input(p.Slot, p.Submodule.Subslot)
		inputs[fmt.Sprintf("%d:%d", p.Slot, p.Submodule.Subslot)] = map[string]any{"data": hex.EncodeToString(v), "iops": iops}
	})
	report["inputs"] = inputs
	st, last := d.State()
	report["state"] = st.String()
	if last != nil {
		report["lastError"] = last.Error()
	}
	stats := d.Stats()
	report["stats"] = stats
	alarmMu.Lock()
	report["alarms"] = gotAlarms
	alarmMu.Unlock()
	emit(report)
	return d.Close()
}

// otfHandler reports the device's events as JSON.
type otfHandler struct {
	device.NopHandler
}

func (otfHandler) StateChanged(ar device.ARInfo, from, to device.ARState) {
	e := map[string]any{"event": "ar", "ar": ar.UUID.String(), "type": ar.Type.String(), "from": from.String(), "to": to.String(), "controller": ar.ControllerName}
	if to == device.ARStateClosed {
		e["reason"] = ar.CloseReason.String()
	}
	emit(e)
}

func (otfHandler) WriteRecord(_ context.Context, r device.RecordRequest, data []byte) error {
	emit(map[string]any{"event": "write", "slot": r.Slot, "subslot": r.Subslot, "index": uint16(r.Index), "data": hex.EncodeToString(data)})
	if r.Modeled {
		return nil
	}
	return device.RecordError(pnio.RWInvalidIndex, 0)
}

func (otfHandler) AlarmAck(_ device.ARInfo, a pnio.AlarmAck) {
	emit(map[string]any{"event": "alarm-ack", "type": a.Type.String(), "status": a.Status.String()})
}

func (otfHandler) Signal() { emit(map[string]any{"event": "signal"}) }

func (otfHandler) ResetToFactory(mode uint16) dcp.BlockError {
	emit(map[string]any{"event": "reset", "mode": mode})
	return dcp.BlockErrorNone
}

func (otfHandler) NameChanged(name string, permanent bool) {
	emit(map[string]any{"event": "name", "name": name, "permanent": permanent})
}

func (otfHandler) IPChanged(s dcp.IPSuite, permanent bool) {
	emit(map[string]any{"event": "ip", "ip": s.String(), "permanent": permanent})
}

// defaultModel is the device otfnode serves when no model is given: the
// examples' device with an IO module in slot 3.
func defaultModel() *model.Device {
	return &model.Device{
		Identity: model.Identity{VendorID: 0xF000, DeviceID: 0x0101, VendorName: "OT Fabric", TypeOfStation: "go-profinet interop device",
			Family: "Interop", OrderID: "OTF-INTEROP-1", SerialNumber: "0001", HardwareRevision: 1,
			SoftwareRevision: pnio.SoftwareRevision{Prefix: 'V', FunctionalEnhance: 1}},
		Capabilities: model.Capabilities{MaxARs: 2, MinDeviceInterval: 32, ConformanceClass: "B", Ports: 1, LLDP: true},
		DAP:          model.StandardDAP("go-profinet interop DAP", 0x00000100, 1),
		Slots: []model.Slot{
			{Number: 1, Module: model.Module{Name: "8 octets in", IdentNumber: 0x10, Submodules: []model.Submodule{{Name: "Inputs", IdentNumber: 0x11, Subslot: 1, InputLen: 8}}}},
			{Number: 2, Module: model.Module{Name: "4 octets out", IdentNumber: 0x20, Submodules: []model.Submodule{{Name: "Outputs", IdentNumber: 0x21, Subslot: 1, OutputLen: 4,
				Records: []model.Record{{Index: 0x0010, Name: "Mode", Length: 2, Default: []byte{0, 1}}}}}}},
			{Number: 3, Module: model.Module{Name: "2 in 2 out", IdentNumber: 0x30, Submodules: []model.Submodule{{Name: "IO", IdentNumber: 0x31, Subslot: 1, InputLen: 2, OutputLen: 2}}}},
		},
	}
}

func runDevice(args []string) error {
	fs := flag.NewFlagSet("device", flag.ExitOnError)
	iface := fs.String("i", "eth0", "interface")
	station := fs.String("station", "otf-device", "station name")
	modelFile := fs.String("model", "", "device model (model JSON or GSDML); default: the built-in interop device")
	gsdmlOut := fs.String("gsdml-out", "", "write the device's GSDML to this file and continue")
	pcapFile := fs.String("pcap", "", "write the frames to this pcap file")
	alarmAfter := fs.Duration("alarm-after", 0, "raise a diagnosis on slot 1 this long after the AR reaches Data")
	acceptIP := fs.Bool("apply-ip", true, "apply DCP Set IP to the interface (needs CAP_NET_ADMIN)")
	fs.Parse(args)
	m := defaultModel()
	if *modelFile != "" {
		var err error
		if m, err = loadModel(*modelFile, ""); err != nil {
			return err
		}
	}
	if *gsdmlOut != "" {
		doc, err := gsdml.FromModel(m, gsdml.GenerateOptions{SupervisorAR: true})
		if err != nil {
			return err
		}
		b, err := gsdml.Marshal(doc)
		if err != nil {
			return err
		}
		if err := os.WriteFile(*gsdmlOut, b, 0o644); err != nil {
			return err
		}
	}
	mux, closePort, err := openPort(*iface, *pcapFile)
	if err != nil {
		return err
	}
	defer closePort()
	cfg := device.Config{Model: m, Mux: mux, StationName: *station, IP: ifaceSuite(*iface), Handler: otfHandler{}, Logger: profinet.NopLogger()}
	if *acceptIP {
		cfg.ApplyIP = func(s dcp.IPSuite, _ bool) error { return setIP(*iface, s) }
	}
	dev, err := device.New(cfg)
	if err != nil {
		return err
	}
	defer dev.Close()
	emit(map[string]any{"event": "start", "station": *station, "mac": mux.HardwareAddr().String(), "ip": cfg.IP.Addr.String()})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	var n byte
	alarmed := false
	var dataSince time.Time
	lastOut := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			emit(map[string]any{"event": "stop"})
			return nil
		case <-t.C:
		}
		n++
		m.Each(func(p model.PluggedSubmodule) {
			s := p.Submodule
			if s.InputLen > 0 {
				v := make([]byte, s.InputLen)
				for i := range v {
					v[i] = n + byte(i)
				}
				dev.SetInput(p.Slot, s.Subslot, v, pnio.IOxSGood)
			}
			if s.OutputLen > 0 {
				if v, iops, ok := dev.Output(p.Slot, s.Subslot); ok && iops == pnio.IOxSGood {
					k := fmt.Sprintf("%d:%d", p.Slot, s.Subslot)
					if h := hex.EncodeToString(v); lastOut[k] != h {
						lastOut[k] = h
						emit(map[string]any{"event": "output", "slot": p.Slot, "subslot": s.Subslot, "data": h})
					}
				}
			}
		})
		inData := false
		for _, a := range dev.ARs() {
			if a.State == device.ARStateData && a.Type == pnio.ARTypeIOCARSingle {
				inData = true
			}
		}
		switch {
		case !inData:
			dataSince = time.Time{}
		case dataSince.IsZero():
			dataSince = time.Now()
		case *alarmAfter > 0 && !alarmed && time.Since(dataSince) > *alarmAfter:
			alarmed = true
			err := dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: 1, ChannelProperties: pnio.ChanDirInput | 4, ErrorType: pnio.ChannelErrLineBreak})
			e := map[string]any{"event": "diagnosis-sent"}
			if err != nil {
				e["error"] = err.Error()
			}
			emit(e)
		}
	}
}

func runDCP(args []string) error {
	fs := flag.NewFlagSet("dcp", flag.ExitOnError)
	iface := fs.String("i", "eth0", "interface")
	wait := fs.Duration("wait", 2*time.Second, "identify wait")
	mac := fs.String("mac", "", "target MAC")
	station := fs.String("station", "", "target station (resolved with Identify)")
	permanent := fs.Bool("permanent", false, "permanent instead of temporary")
	fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("dcp: operation required")
	}
	mux, closePort, err := openPort(*iface, "")
	if err != nil {
		return err
	}
	defer closePort()
	c, err := dcp.NewClient(mux, dcp.ClientOptions{})
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()
	target := func() (ethernet.MAC, error) {
		if *mac != "" {
			return ethernet.ParseMAC(*mac)
		}
		id, err := c.IdentifyName(ctx, *station, *wait)
		return id.MAC, err
	}
	op := fs.Arg(0)
	res := map[string]any{"event": "dcp", "op": op}
	switch op {
	case "identify":
		f := dcp.FilterAll()
		if *station != "" {
			f = dcp.FilterName(*station)
		}
		ids, err := c.Identify(ctx, f, dcp.IdentifyOptions{Wait: *wait, ResponseDelay: 10})
		if err != nil {
			return err
		}
		res["devices"] = ids
	case "get":
		m, err := target()
		if err != nil {
			return err
		}
		id, err := c.GetIdentity(ctx, m)
		if err != nil {
			res["error"] = err.Error()
		}
		res["identity"] = id
	case "set-name":
		m, err := target()
		if err != nil {
			return err
		}
		if err := c.SetName(ctx, m, fs.Arg(1), *permanent); err != nil {
			res["error"] = err.Error()
		}
	case "set-ip":
		m, err := target()
		if err != nil {
			return err
		}
		pfx, err := netip.ParsePrefix(fs.Arg(1))
		if err != nil {
			return err
		}
		ma, _ := netip.AddrFromSlice(net.CIDRMask(pfx.Bits(), 32))
		if err := c.SetIP(ctx, m, dcp.IPSuite{Addr: pfx.Addr(), Mask: ma, Gateway: netip.IPv4Unspecified()}, *permanent); err != nil {
			res["error"] = err.Error()
		}
	case "signal":
		m, err := target()
		if err != nil {
			return err
		}
		if err := c.Signal(ctx, m); err != nil {
			res["error"] = err.Error()
		}
	case "reset":
		m, err := target()
		if err != nil {
			return err
		}
		if err := c.ResetToFactory(ctx, m, dcp.ResetCommunication); err != nil {
			res["error"] = err.Error()
		}
	default:
		return fmt.Errorf("dcp: unknown operation %q", op)
	}
	emit(res)
	return nil
}

func runLLDP(args []string) error {
	fs := flag.NewFlagSet("lldp", flag.ExitOnError)
	iface := fs.String("i", "eth0", "interface")
	wait := fs.Duration("wait", 12*time.Second, "how long to listen")
	fs.Parse(args)
	mux, closePort, err := openPort(*iface, "")
	if err != nil {
		return err
	}
	defer closePort()
	a, err := lldp.NewAgent(mux, lldp.AgentOptions{Interval: -1})
	if err != nil {
		return err
	}
	defer a.Close()
	time.Sleep(*wait)
	var out []map[string]any
	for _, n := range a.Neighbors() {
		out = append(out, map[string]any{"mac": n.MAC.String(), "chassis": n.Frame.ChassisString(), "port": n.Frame.PortString(), "ttl": n.Frame.TTL,
			"systemDescription": n.Frame.SystemDescription, "portStatus": n.Frame.PortStatus, "chassisMac": n.Frame.ChassisMAC, "alias": n.Alias()})
	}
	emit(map[string]any{"event": "lldp", "neighbors": out})
	return nil
}
