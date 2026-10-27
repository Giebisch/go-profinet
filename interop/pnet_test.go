// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

// pnet starts the p-net device and waits until it serves RPC. pn_dev opens
// its RPC socket some time after the start event; in one CI run of about
// 150 it never did (every Connect was answered with ICMP port unreachable
// while DCP and LLDP worked), so a p-net that has not opened its port after
// 15 s is restarted once.
func pnet(e *env, station string) string {
	img := image("pnet")
	e.result.peer("p-net", img, "device")
	for attempt := 1; ; attempt++ {
		c := e.start("pnet", img, 10, "device", "--station", station)
		e.waitEvent(c, 30*time.Second, "start", func(m map[string]any) bool { return m["event"] == "start" })
		if e.udpListening(c, rpc.Port, 15*time.Second) {
			return c
		}
		if attempt == 2 {
			e.t.Fatalf("p-net did not open its RPC port %d (twice)", rpc.Port)
		}
		e.t.Logf("p-net did not open its RPC port %d within 15 s: restarting it", rpc.Port)
		_, _ = docker(context.Background(), "rm", "-f", c)
	}
}

// udpListening waits until a UDP socket of the container is bound to port.
func (e *env) udpListening(container string, port int, timeout time.Duration) bool {
	pattern := fmt.Sprintf(":%04X ", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := docker(context.Background(), "exec", container, "cat", "/proc/net/udp")
		if err == nil && strings.Contains(string(out), pattern) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// TestPnetController: go-profinet as IO-Controller of p-net.
func TestPnetController(t *testing.T) {
	e := newEnv(t)
	dev := pnet(e, "pnet-dut")
	stop := e.capture(dev, "pnet-controller.pcap")
	im1 := pnio.IM1{TagFunction: "otf function", TagLocation: "otf location"}
	im1Hex := hex.EncodeToString(im1.Append(nil))
	out := e.otf("ctrl", 2, 90*time.Second, "controller", "-station", "pnet-dut", "-model", "/otfnode-testdata/pnet-model.json",
		"-implicit", "0:1:0xAFF0", "-wm", "1:1:124:0a0b0c0d", "-wm", "2:1:125:00000003", "-read", "1:1:124",
		"-write", "0:1:0xAFF1:"+im1Hex, "-read", "0:1:0xAFF1",
		"-im0", "-param", "1:1:123:01020304", "-read", "1:1:123", "-write", "2:1:125:00000002", "-read", "2:1:125",
		"-read", "0:1:0xF000", "-read", "0:0x8001:0x802A", "-read", "0:1:0xF841", "-read", "1:1:0x7777", "-write", "1:1:0x7777:00", "-diag",
		"-output", "1:1:0:5a", "-output", "2:1:0:1122334455667788", "-duration", "4s")
	r := report(t, out)
	e.expect("DCP Identify by name, Connect, PrmEnd, ApplicationReady (RT_CLASS_1 legacy)", r["running"] == true, "running=%v error=%v", r["running"], r["error"])
	if r["running"] != true {
		return
	}
	im0, _ := r["im0"].(map[string]any)
	e.expect("Read I&M0", im0 != nil && im0["OrderID"] == "12345 Abcdefghijk" && im0["VendorID"] == 1171.0, "%v", im0)
	recs, _ := r["records"].([]any)
	byKey := map[string]map[string]any{}
	for _, x := range recs {
		m := x.(map[string]any)
		byKey[fmt.Sprintf("%v %v/%v/%v", m["op"], m["slot"], m["subslot"], m["index"])] = m
	}
	e.expect("Parameter write before PrmEnd, read back", byKey["read 1/1/123"]["data"] == "01020304", "%v", byKey["read 1/1/123"])
	e.expect("Write record in Data, read back", byKey["write 2/1/125"]["error"] == nil && byKey["read 2/1/125"]["data"] == "00000002", "%v %v", byKey["write 2/1/125"], byKey["read 2/1/125"])
	real := str(byKey["read 0/1/61440"]["data"])
	e.expect("RealIdentificationData (0xF000)", strings.HasPrefix(real, "0013") && strings.Contains(real, "00000032") && strings.Contains(real, "00000040"), "%s", real)
	pd, _ := hex.DecodeString(str(byKey["read 0/32769/32810"]["data"]))
	blk, _, err := pnio.ReadBlock(pd)
	port, perr := pnio.DecodePDPortDataReal(blk)
	e.expect("PDPortDataReal (0x802A) decoded", err == nil && perr == nil && port.Subslot == 0x8001 && strings.HasPrefix(port.OwnPortID, "port-001"), "%+v %v %v %v", port, err, perr, byKey["read 0/32769/32810"])
	rd, _ := hex.DecodeString(str(byKey["read 0/1/63553"]["data"]))
	ports, err := pnio.DecodePDRealData(rd)
	e.expect("PDRealData (0xF841) decoded", err == nil && len(ports) >= 1 && ports[0].Subslot == 0x8001, "%+v %v %v", ports, err, byKey["read 0/1/63553"])
	e.expect("Read of an unknown index answers a PNIORW read error", strings.HasPrefix(str(byKey["read 1/1/30583"]["status"]), "DE 80"), "%v", byKey["read 1/1/30583"])
	e.expect("Write of an invalid index is rejected with a PNIO status", strings.HasPrefix(str(byKey["write 1/1/30583"]["status"]), "DF 80"), "%v", byKey["write 1/1/30583"])
	impl, _ := r["implicit"].([]any)
	var implData string
	if len(impl) == 1 {
		implData = str(impl[0].(map[string]any)["data"])
	}
	e.expect("ReadImplicit of I&M0 (no AR)", strings.HasPrefix(implData, "00200038"), "%v", impl)
	wmr, _ := r["writeMultiple"].(map[string]any)
	e.expect("WriteMultiple of two records", wmr != nil && wmr["error"] == nil && fmt.Sprint(wmr["status"]) == "[OK OK]" && byKey["read 1/1/124"]["data"] == "0a0b0c0d", "%v %v", wmr, byKey["read 1/1/124"])
	e.expect("Write I&M1 in the AR, read it back", byKey["write 0/1/45041"]["error"] == nil && byKey["read 0/1/45041"]["data"] == im1Hex, "%v %v", byKey["write 0/1/45041"], byKey["read 0/1/45041"])
	e.expect("Read diagnosis (0xF80C)", r["diagnosisError"] == nil, "%v", r["diagnosisError"])
	stats, _ := r["stats"].(map[string]any)
	cons, _ := stats["Consumer"].(map[string]any)
	e.expect("Cyclic input data (consumer)", cons["Received"].(float64) > 100 && cons["Malformed"] == 0.0 && cons["Expired"] == 0.0, "%v", cons)
	prov, _ := stats["Provider"].(map[string]any)
	e.expect("Cyclic output data (provider)", prov["Sent"].(float64) > 100 && prov["SendErrors"] == 0.0, "%v", prov)
	in, _ := r["inputs"].(map[string]any)
	echo, _ := in["2:1"].(map[string]any)
	e.expect("Echo module returns our outputs", echo != nil && echo["iops"] == 128.0 && echo["data"] != "0000000000000000", "%v", echo)
	evs := e.events(dev)
	for _, ev := range []string{"connect", "prmend", "appready-confirmed", "write", "read"} {
		e.expect("p-net reports "+ev, hasEvent(evs, ev), "events %d", len(evs))
	}
	e.expect("Release ends the AR at p-net", e.eventually(dev, 5*time.Second, named("release", "closed")), "")
	e.oracles(stop())
}

// TestPnetLegacyAndAdvanced connects in both startup modes.
func TestPnetStartupModes(t *testing.T) {
	e := newEnv(t)
	pnet(e, "pnet-dut")
	for _, mode := range []string{"legacy", "advanced"} {
		args := []string{"controller", "-station", "pnet-dut", "-model", "/otfnode-testdata/pnet-model.json", "-duration", "1s"}
		if mode == "advanced" {
			args = append(args, "-advanced")
		}
		r := report(t, e.otf("ctrl-"+mode, 2, 60*time.Second, args...))
		e.expect("Startup "+mode+" (RTClass "+map[string]string{"legacy": "1", "advanced": "2"}[mode]+")", r["running"] == true, "%v", r["error"])
	}
	// A 1 ms cycle (reduction ratio 1).
	r := report(t, e.otf("ctrl-1ms", 2, 60*time.Second, "controller", "-station", "pnet-dut", "-model", "/otfnode-testdata/pnet-model.json", "-rr", "1", "-dhf", "50", "-duration", "3s"))
	e.expect("1 ms cycle", r["running"] == true && r["state"] == "Running", "%v %v %v", r["running"], r["state"], r["stats"])
}

// TestPnetAlarms: p-net raises a process alarm and a diagnosis (its
// button 2); the controller receives and acknowledges them.
func TestPnetAlarms(t *testing.T) {
	e := newEnv(t)
	dev := pnet(e, "pnet-dut")
	ctrl := e.startOTF("ctrl", 2, "controller", "-station", "pnet-dut", "-model", "/otfnode-testdata/pnet-model.json", "-duration", "25s", "-wait-alarms", "2")
	e.waitEvent(ctrl, 60*time.Second, "Running", func(m map[string]any) bool { return m["event"] == "state" && m["to"] == "Running" })
	time.Sleep(time.Second)
	// Button 2 steps through process alarm, diagnosis add, update and
	// removal, logbook entry, ... (one action per press).
	for i := 0; i < 4; i++ {
		e.exec(dev, "profinet-interop", "button2")
		time.Sleep(1500 * time.Millisecond)
	}
	r := e.waitEvent(ctrl, 40*time.Second, "report", func(m map[string]any) bool { return m["event"] == "report" })
	al, _ := r["alarms"].([]any)
	var types []string
	for _, a := range al {
		types = append(types, str(a.(map[string]any)["type"]))
	}
	joined := strings.Join(types, ",")
	e.expect("Process alarm received and acknowledged", strings.Contains(joined, "Process"), "alarms %v", types)
	e.expect("Diagnosis alarm received and acknowledged", strings.Contains(joined, "Diagnosis"), "alarms %v", types)
	e.expect("p-net confirms the AlarmAck", e.eventually(dev, 5*time.Second, named("alarm-ack-confirmed", "alarm-confirmed")), "")
	e.expect("AR survives the alarms", r["state"] == "Running", "%v", r["state"])
}

// TestPnetDCP: go-profinet's DCP requester against p-net's responder.
func TestPnetDCP(t *testing.T) {
	e := newEnv(t)
	dev := pnet(e, "pnet-dut")
	ids := last(jsonLines(e.otf("ident", 3, time.Minute, "dcp", "identify")), "dcp")
	devs, _ := ids["devices"].([]any)
	found := false
	for _, d := range devs {
		m := d.(map[string]any)
		if m["nameOfStation"] == "pnet-dut" && m["vendorId"] == 1171.0 && m["deviceId"] == 2.0 {
			found = true
		}
	}
	e.expect("DCP Identify all", found, "%v", devs)
	get := last(jsonLines(e.otf("get", 3, time.Minute, "dcp", "-station", "pnet-dut", "get")), "dcp")
	id, _ := get["identity"].(map[string]any)
	e.expect("DCP Get (all blocks)", get["error"] == nil && id["nameOfStation"] == "pnet-dut", "%v", get)
	set := last(jsonLines(e.otf("setname", 3, time.Minute, "dcp", "-station", "pnet-dut", "set-name", "pnet-renamed")), "dcp")
	e.expect("DCP Set NameOfStation (temporary)", set["error"] == nil, "%v", set)
	ids = last(jsonLines(e.otf("ident2", 3, time.Minute, "dcp", "-station", "pnet-renamed", "identify")), "dcp")
	devs, _ = ids["devices"].([]any)
	e.expect("DCP Identify by the new name", len(devs) == 1, "%v", devs)
	back := last(jsonLines(e.otf("setname2", 3, time.Minute, "dcp", "-station", "pnet-renamed", "set-name", "pnet-dut")), "dcp")
	e.expect("DCP Set NameOfStation back", back["error"] == nil, "%v", back)
	sig := last(jsonLines(e.otf("signal", 3, time.Minute, "dcp", "-station", "pnet-dut", "signal")), "dcp")
	e.expect("DCP Signal", sig["error"] == nil, "%v", sig)
	time.Sleep(time.Second)
	e.expect("p-net flashes its LED on Signal", hasEvent(e.events(dev), "signal"), "")
	ip := last(jsonLines(e.otf("setip", 3, time.Minute, "dcp", "-station", "pnet-dut", "set-ip", e.ip(11)+"/24")), "dcp")
	e.expect("DCP Set IP (temporary)", ip["error"] == nil, "%v", ip)
	get = last(jsonLines(e.otf("get2", 3, time.Minute, "dcp", "-station", "pnet-dut", "get")), "dcp")
	id, _ = get["identity"].(map[string]any)
	ipm, _ := id["ip"].(map[string]any)
	e.expect("DCP Get reflects the new IP", str(ipm["Addr"]) == e.ip(11), "%v", id["ip"])
	rst := last(jsonLines(e.otf("reset", 3, time.Minute, "dcp", "-station", "pnet-dut", "reset")), "dcp")
	e.expect("DCP ResetToFactory (communication)", rst["error"] == nil, "%v", rst)
	time.Sleep(time.Second)
	ids = last(jsonLines(e.otf("ident3", 3, time.Minute, "dcp", "identify")), "dcp")
	devs, _ = ids["devices"].([]any)
	nameless := false
	for _, d := range devs {
		if m := d.(map[string]any); m["nameOfStation"] == "" && m["vendorId"] == 1171.0 {
			nameless = true
		}
	}
	e.expect("Reset clears the station name", nameless, "%v", devs)
}

// TestPnetLLDP: go-profinet's LLDP agent receives p-net's announcements.
func TestPnetLLDP(t *testing.T) {
	e := newEnv(t)
	pnet(e, "pnet-dut")
	if !e.forwardLLDP() {
		e.result.na("LLDP neighbor (p-net)", "the Docker bridge does not forward 01:80:C2:00:00:0E and its group_fwd_mask cannot be set here")
		t.Skip("bridge does not forward LLDP")
	}
	l := last(jsonLines(e.otf("lldp", 3, time.Minute, "lldp", "-wait", "12s")), "lldp")
	ns, _ := l["neighbors"].([]any)
	ok := false
	for _, n := range ns {
		m := n.(map[string]any)
		if strings.HasPrefix(str(m["port"]), "port-001") {
			ok = true
			e.expect("LLDP PNIO PortStatus TLV", m["portStatus"] != nil, "%v", m)
			e.expect("LLDP PNIO ChassisMAC TLV", m["chassisMac"] != nil, "%v", m)
		}
	}
	e.expect("LLDP neighbor (p-net)", ok, "%v", ns)
}

// TestPnetConfigurationMismatch: negative tests of the expected
// configuration. A module p-net does not have in slot 2: the p-net sample
// application plugs whatever module the controller expects (it logs
// "unknown") and reports no ModuleDiff, so the AR runs; this is recorded as
// p-net's behaviour. A model with another DeviceID: the controller must
// refuse the device before it connects.
func TestPnetConfigurationMismatch(t *testing.T) {
	e := newEnv(t)
	pnet(e, "pnet-dut")
	b, err := os.ReadFile(filepath.Join(testdata, "pnet-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, edit func(m map[string]any)) string {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		out, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(e.share, name), out, 0o644); err != nil {
			t.Fatal(err)
		}
		return "/share/" + name
	}
	wrongModule := write("wrong-module.json", func(m map[string]any) {
		m["slots"].([]any)[1].(map[string]any)["module"].(map[string]any)["identNumber"] = 0x41
	})
	wrongDevice := write("wrong-device.json", func(m map[string]any) {
		m["identity"].(map[string]any)["deviceId"] = 0x77
	})
	e.result.peer("p-net", image("pnet"), "device")
	r := report(t, e.otf("module", 2, 90*time.Second, "controller", "-station", "pnet-dut", "-model", wrongModule, "-duration", "1s"))
	e.expect("Unexpected module in slot 2: p-net plugs it (no ModuleDiff), the AR runs", r["running"] == true && r["moduleDiff"] == nil, "running=%v diff=%v", r["running"], r["moduleDiff"])
	r = report(t, e.otf("device", 2, 90*time.Second, "controller", "-station", "pnet-dut", "-model", wrongDevice, "-timeout", "5s"))
	e.expect("Model of another DeviceID: the controller refuses the device", r["running"] == false && strings.Contains(str(r["error"]), "device"), "running=%v error=%v", r["running"], r["error"])
}
