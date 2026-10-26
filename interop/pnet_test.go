// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// pnet starts the p-net device.
func pnet(e *env, station string) string {
	img := image("pnet")
	e.result.peer("p-net", img, "device")
	c := e.start("pnet", img, 10, "device", "--station", station)
	e.waitEvent(c, 30*time.Second, "start", func(m map[string]any) bool { return m["event"] == "start" })
	time.Sleep(2 * time.Second) // pn_dev initialises its sockets after the start event
	return c
}

// TestPnetController: go-profinet as IO-Controller of p-net.
func TestPnetController(t *testing.T) {
	e := newEnv(t)
	dev := pnet(e, "pnet-dut")
	stop := e.capture(dev, "pnet-controller.pcap")
	out := e.otf("ctrl", 2, 90*time.Second, "controller", "-station", "pnet-dut", "-model", "/otfnode-testdata/pnet-model.json",
		"-im0", "-param", "1:1:123:01020304", "-read", "1:1:123", "-write", "2:1:125:00000002", "-read", "2:1:125",
		"-read", "0:1:0xF000", "-read", "1:1:0x7777", "-write", "1:1:0x7777:00", "-diag",
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
	e.expect("Read of an unknown index answers a PNIORW read error", strings.HasPrefix(str(byKey["read 1/1/30583"]["status"]), "DE 80"), "%v", byKey["read 1/1/30583"])
	e.expect("Write of an invalid index is rejected with a PNIO status", strings.HasPrefix(str(byKey["write 1/1/30583"]["status"]), "DF 80"), "%v", byKey["write 1/1/30583"])
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
