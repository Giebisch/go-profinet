// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/pnio"
)

// TestPnioRsController: go-profinet as IO-Controller of pnio-rs, with the
// expected configuration read from pnio-rs's own GSDML.
func TestPnioRsController(t *testing.T) {
	e := newEnv(t)
	img := image("pnio-rs")
	e.result.peer("pnio-rs", img, "device")
	dev := e.start("pniors", img, 30, "device", "--station", "pnio-dut")
	e.waitEvent(dev, 30*time.Second, "Idle", func(m map[string]any) bool { return m["event"] == "ar-state" })
	stop := e.capture(dev, "pniors-controller.pcap")
	one := hex.EncodeToString([]byte{0x3f, 0x80, 0, 0})
	im1 := "002100380100" + strings.Repeat("61", 32) + strings.Repeat("62", 22)
	out := e.otf("ctrl", 2, 90*time.Second, "controller", "-station", "pnio-dut", "-model", "/gsdml-testdata/pnio-rs_sample-16real-32bool.xml",
		"-advanced", "-im0", "-write", "0:1:0xAFF1:"+im1, "-read", "0:1:0xAFF1", "-read", "1:1:0x800C", "-read", "0:1:0xAFF4",
		"-output", "3:1:0:"+strings.Repeat(one, 16), "-output", "4:1:0:a5a5a5a5", "-duration", "4s")
	r := report(t, out)
	e.expect("GSDML-derived expected configuration; Connect, PrmEnd, ApplicationReady (advanced, RTClass 2)", r["running"] == true, "%v", r["error"])
	if r["running"] != true {
		return
	}
	im0, _ := r["im0"].(map[string]any)
	e.expect("Read I&M0", im0 != nil && im0["OrderID"] == "PNIO-SAMPLE", "%v", im0)
	recs, _ := r["records"].([]any)
	var gotIM1, diag, im4 map[string]any
	for _, x := range recs {
		m := x.(map[string]any)
		switch fmt.Sprintf("%v %v", m["op"], m["index"]) {
		case "read 45041":
			gotIM1 = m
		case "read 32780":
			diag = m
		case "read 45044":
			im4 = m
		}
	}
	e.expect("Write and read back I&M1", gotIM1 != nil && gotIM1["data"] == im1, "%v", gotIM1)
	e.result.na("Diagnosis records (0x800C)", "pnio-rs answers every diagnosis index with invalid index (documented limitation)")
	e.expect("Diagnosis record answers invalid index as pnio-rs documents", diag != nil && strings.HasPrefix(str(diag["status"]), "DE 80 B0"), "%v", diag)
	e.expect("I&M4 answers invalid index as pnio-rs documents", im4 != nil && strings.HasPrefix(str(im4["status"]), "DE 80 B0"), "%v", im4)
	in, _ := r["inputs"].(map[string]any)
	reals, _ := in["1:1"].(map[string]any)
	bools, _ := in["2:1"].(map[string]any)
	e.expect("Cyclic echo of 16 REAL outputs", reals != nil && reals["data"] == strings.Repeat(one, 16) && reals["iops"] == 128.0, "%v", reals)
	e.expect("Cyclic echo of 32 BOOL outputs", bools != nil && bools["data"] == "a5a5a5a5", "%v", bools)
	evs := e.events(dev)
	e.expect("pnio-rs reaches Data", func() bool {
		for _, ev := range evs {
			if ev["event"] == "ar-state" && ev["state"] == "Data" {
				return true
			}
		}
		return false
	}(), "%v", evs)
	e.expect("Release ends the AR at pnio-rs", e.eventually(dev, 5*time.Second, func(ev map[string]any) bool {
		return ev["event"] == "ar-state" && strings.Contains(str(ev["state"]), "ControllerRelease")
	}), "")
	// Legacy startup with RT_CLASS_1 FrameIDs is outside pnio-rs's range.
	lr := report(t, e.otf("ctrl-legacy", 2, 60*time.Second, "controller", "-station", "pnio-dut", "-model", "/gsdml-testdata/pnio-rs_sample-16real-32bool.xml", "-timeout", "6s"))
	e.expect("pnio-rs rejects RT_CLASS_1 FrameIDs (0xC000..) with a Connect error, which the controller reports", lr["running"] != true && strings.Contains(str(lr["error"]), "connect"), "%v", lr["error"])
	e.oracles(stop())
}

// TestPnioRsDCP: go-profinet's DCP requester against pnio-rs.
func TestPnioRsDCP(t *testing.T) {
	e := newEnv(t)
	img := image("pnio-rs")
	e.result.peer("pnio-rs", img, "device")
	dev := e.start("pniors", img, 30, "device", "--station", "pnio-dut")
	e.waitEvent(dev, 30*time.Second, "Idle", func(m map[string]any) bool { return m["event"] == "ar-state" })
	ids := last(jsonLines(e.otf("ident", 3, time.Minute, "dcp", "-station", "pnio-dut", "identify")), "dcp")
	devs, _ := ids["devices"].([]any)
	e.expect("DCP Identify by name", len(devs) == 1 && devs[0].(map[string]any)["vendorId"] == 65535.0, "%v", devs)
	all := last(jsonLines(e.otf("identall", 3, time.Minute, "dcp", "identify")), "dcp")
	devs, _ = all["devices"].([]any)
	e.expect("DCP Identify all", len(devs) >= 1, "%v", devs)
	e.result.na("DCP Get/Set NameOfStation/Signal/Reset (pnio-rs)", "pnio-rs v0.1.0 does not implement them")
}

// TestProfinetPyController: profinet-py, an independent IO-Controller,
// against go-profinet's IO-Device, configured from the GSDML the device
// generates.
func TestProfinetPyController(t *testing.T) {
	e := newEnv(t)
	img := image("profinet-py")
	e.result.peer("profinet-py", img, "controller")
	dev := e.startOTF("dut", 20, "device", "-station", "otf-dut", "-gsdml-out", "/share/otf-dut.xml", "-alarm-after", "1s")
	e.waitEvent(dev, 30*time.Second, "start", func(m map[string]any) bool { return m["event"] == "start" })
	stop := e.capture(dev, "profinet-py-controller.pcap")
	out := e.run("pypn", img, 3, 3*time.Minute, "controller", "--station", "otf-dut", "--gsdml", "/share/otf-dut.xml",
		"--output", "2:1:a1b2c3d4", "--output", "3:1:beef", "--read-input", "1:1", "--read-input", "3:1", "--duration", "3")
	var rep struct {
		Steps []struct {
			Step  string `json:"step"`
			OK    bool   `json:"ok"`
			Value any    `json:"value"`
			Error string `json:"error"`
		} `json:"steps"`
		Alarms []any `json:"alarms"`
	}
	if err := json.Unmarshal(lastLine(out), &rep); err != nil {
		t.Fatalf("profinet-py output: %v\n%s", err, out)
	}
	steps := map[string]int{}
	for i, s := range rep.Steps {
		steps[s.Step] = i
	}
	ok := func(name string) bool { i, found := steps[name]; return found && rep.Steps[i].OK }
	val := func(name string) string {
		if i, found := steps[name]; found {
			return str(rep.Steps[i].Value)
		}
		return ""
	}
	e.expect("DCP discovery by profinet-py", ok("discover"), "%v", rep.Steps)
	e.expect("Read I&M0 (IOSAR device access)", ok("read-im0") && strings.Contains(val("read-im0"), "OTF-INTEROP-1"), "%s", val("read-im0"))
	e.expect("Read I&M2..I&M4", ok("read-im2") && ok("read-im3") && ok("read-im4"), "")
	e.expect("Write and read a user record", ok("write-record") && val("read-record") == "0005", "%s", val("read-record"))
	e.expect("ReadImplicit I&M0", ok("read-implicit-im0") && strings.HasPrefix(val("read-implicit-im0"), "00200038"), "%s", val("read-implicit-im0"))
	e.expect("Invalid index answered with an error", !ok("read-invalid-index"), "")
	e.expect("ModuleDiff read", ok("module-diff"), "%s", val("module-diff"))
	e.expect("Slot discovery (RealIdentificationData)", ok("discover-slots") && strings.Contains(val("discover-slots"), "slot=3"), "%s", val("discover-slots"))
	e.result.deviation("I&M1 write by profinet-py", "profinet-py writes BlockLength 58 with 2 padding octets; p-net, pnio-rs and go-profinet use 56; go-profinet answers DF 80 B8 00")
	e.expect("GSDML generated by go-profinet parsed by profinet-py; cyclic AR (IOCARSingle) established", ok("start-cyclic"), "%v", rep.Steps)
	e.expect("Cyclic inputs at profinet-py", ok("input 1:1") && len(val("input 1:1")) == 16 && val("input 1:1") != "0000000000000000", "%s", val("input 1:1"))
	stats := func() map[string]any {
		if i, found := steps["cyclic-stats"]; found {
			m, _ := rep.Steps[i].Value.(map[string]any)
			return m
		}
		return nil
	}()
	e.expect("Cyclic statistics without missed frames", ok("cyclic-stats") && stats["frames_missed"] == 0.0 && stats["frames_invalid"] == 0.0 && stats["frames_received"].(float64) > 50, "%v", stats)
	evs := e.events(dev)
	outputs := map[string]bool{}
	for _, ev := range evs {
		if ev["event"] == "output" {
			outputs[fmt.Sprintf("%v:%v=%v", ev["slot"], ev["subslot"], ev["data"])] = true
		}
	}
	e.expect("Cyclic outputs from profinet-py at the device", outputs["2:1=a1b2c3d4"] && outputs["3:1=beef"], "%v", outputs)
	var arTypes []string
	for _, ev := range evs {
		if ev["event"] == "ar" && ev["to"] == "Data" {
			arTypes = append(arTypes, str(ev["type"]))
		}
	}
	e.expect("IOSAR and IOCARSingle ARs from profinet-py", strings.Contains(strings.Join(arTypes, ","), "IOSAR") && strings.Contains(strings.Join(arTypes, ","), "IOCARSingle"), "%v", arTypes)
	e.expect("Diagnosis alarm sent to profinet-py and acknowledged", hasEvent(evs, "diagnosis-sent") && last(evs, "diagnosis-sent")["error"] == nil, "%v", last(evs, "diagnosis-sent"))
	e.oracles(stop(), peerDeviation{Protocol: "PNIO-CM", Message: "Trailing stray characters",
		Why: "profinet-py writes I&M1 with BlockLength 58 and two padding octets; the standard block is 56 octets (tshark flags the extra octets of its Write request)"})
}

// TestPnioRsReparameterize: pnio-rs answers PrmBegin in Data with
// DD 81 3D 03 (CMDEV state conflict): not applicable; its AR runs on.
func TestPnioRsReparameterize(t *testing.T) {
	e := newEnv(t)
	img := image("pnio-rs")
	e.result.peer("pnio-rs", img, "device")
	dev := e.start("pniors", img, 30, "device", "--station", "pnio-dut")
	e.waitEvent(dev, 30*time.Second, "start", named("start"))
	im1 := hex.EncodeToString((&pnio.IM1{TagFunction: "reparameterized"}).Append(nil))
	r := report(t, e.otf("ctrl", 2, 90*time.Second, "controller", "-station", "pnio-dut", "-model", "/gsdml-testdata/pnio-rs_sample-16real-32bool.xml",
		"-advanced", "-reparam", "0:1:0xAFF1:"+im1, "-duration", "2s"))
	if r["running"] != true {
		t.Fatalf("not running: %v", r["error"])
	}
	var re map[string]any
	for _, x := range r["records"].([]any) {
		if m := x.(map[string]any); m["op"] == "reparameterize" {
			re = m
		}
	}
	if re != nil && re["error"] == nil {
		e.expect("Re-parameterization in Data (pnio-rs)", re["state"] == "Running", "%v", re)
		return
	}
	e.result.na("Re-parameterization in Data (pnio-rs)", "pnio-rs refuses PrmBegin in Data: "+str(re["error"]))
	e.expect("A refused PrmBegin leaves the AR running (pnio-rs)", re != nil && re["state"] == "Running" && r["state"] == "Running", "%v %v", re, r["state"])
}

// TestProfinetPyReparameterize: profinet-py, an independent IO-Controller,
// re-parameterizes go-profinet's IO-Device in Data: PrmBegin, a record
// write, PrmEnd; the device answers with ApplicationReady and its AR goes
// back to Data while cyclic data flows.
func TestProfinetPyReparameterize(t *testing.T) {
	e := newEnv(t)
	img := image("profinet-py")
	e.result.peer("profinet-py", img, "controller")
	dev := e.startOTF("dut", 20, "device", "-station", "otf-dut", "-gsdml-out", "/share/otf-dut.xml")
	e.waitEvent(dev, 30*time.Second, "start", named("start"))
	stop := e.capture(dev, "profinet-py-reparameterize.pcap")
	out := e.run("pypn", img, 3, 3*time.Minute, "controller", "--station", "otf-dut", "--gsdml", "/share/otf-dut.xml",
		"--read-input", "1:1", "--duration", "2", "--reparam", "2:1:0x10:0009")
	var rep struct {
		Steps []struct {
			Step  string `json:"step"`
			OK    bool   `json:"ok"`
			Value any    `json:"value"`
			Error string `json:"error"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(lastLine(out), &rep); err != nil {
		t.Fatalf("profinet-py output: %v\n%s", err, out)
	}
	res := map[string]string{}
	for _, st := range rep.Steps {
		if st.OK {
			res[st.Step] = "ok"
		} else {
			res[st.Step] = st.Error
		}
	}
	e.expect("Cyclic AR (IOCARSingle) by profinet-py", res["start-cyclic"] == "ok", "%v", res)
	e.expect("PrmBegin in Data accepted (profinet-py)", res["prm-begin"] == "ok", "%s", res["prm-begin"])
	e.expect("Record write between PrmBegin and PrmEnd (profinet-py)", res["reparam-write 2:1:0x10"] == "ok", "%s", res["reparam-write 2:1:0x10"])
	e.expect("PrmEnd after re-parameterization (profinet-py)", res["prm-end"] == "ok", "%s", res["prm-end"])
	e.expect("ApplicationReady after re-parameterization, answered by profinet-py", res["reparam-application-ready"] == "ok", "%s", res["reparam-application-ready"])
	var path []string
	for _, ev := range e.events(dev) {
		if ev["event"] == "ar" && ev["type"] == "IOCARSingle" {
			path = append(path, str(ev["to"]))
		}
	}
	got := strings.Join(path, ",")
	e.expect("Device AR: Data, Parameterizing, ApplicationReadyPending, Data", strings.Contains(got, "Data,Parameterizing,ApplicationReadyPending,Data"), "%s", got)
	e.expect("Cyclic inputs after re-parameterization", res["input 1:1"] == "ok", "%s", res["input 1:1"])
	e.oracles(stop(), peerDeviation{Protocol: "PNIO-CM", Message: "Trailing stray characters",
		Why: "profinet-py writes I&M1 with BlockLength 58 and two padding octets (its scenario's write-im1 step)"})
}

// TestProfinetPyDCP: profinet-py's DCP tool against go-profinet's
// responder.
func TestProfinetPyDCP(t *testing.T) {
	e := newEnv(t)
	img := image("profinet-py")
	e.result.peer("profinet-py", img, "dcp-tool")
	dev := e.startOTF("dut", 20, "device", "-station", "otf-dut")
	start := e.waitEvent(dev, 30*time.Second, "start", func(m map[string]any) bool { return m["event"] == "start" })
	mac := str(start["mac"])
	py := func(name string, args ...string) map[string]any {
		var d map[string]any
		out := e.run(name, img, 3, 2*time.Minute, append([]string{"dcp"}, args...)...)
		if err := json.Unmarshal(lastLine(out), &d); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		return d
	}
	stepOK := func(d map[string]any) (bool, string) {
		steps, _ := d["steps"].([]any)
		if len(steps) == 0 {
			return false, "no steps"
		}
		s := steps[0].(map[string]any)
		return s["ok"] == true, str(s["value"]) + str(s["error"])
	}
	ok, v := stepOK(py("ident", "identify"))
	e.expect("DCP Identify all", ok && strings.Contains(v, "otf-dut"), "%s", v)
	ok, v = stepOK(py("get", "get", "--mac", mac))
	e.expect("DCP Get name", ok, "%s", v)
	ok, v = stepOK(py("setname", "set-name", "--mac", mac, "py-renamed"))
	e.expect("DCP Set NameOfStation", ok, "%s", v)
	time.Sleep(500 * time.Millisecond)
	e.expect("Device applies the name", func() bool {
		for _, ev := range e.events(dev) {
			if ev["event"] == "name" && ev["name"] == "py-renamed" {
				return true
			}
		}
		return false
	}(), "")
	ok, v = stepOK(py("setip", "set-ip", "--mac", mac, e.ip(21), "255.255.255.0", "0.0.0.0"))
	e.expect("DCP Set IP", ok, "%s", v)
	ok, v = stepOK(py("signal", "signal", "--mac", mac))
	e.expect("DCP Signal", ok, "%s", v)
	ok, v = stepOK(py("reset", "reset", "--mac", mac))
	e.expect("DCP ResetToFactory", ok, "%s", v)
	time.Sleep(500 * time.Millisecond)
	evs := e.events(dev)
	e.expect("Device reports Signal and Reset", hasEvent(evs, "signal") && hasEvent(evs, "reset"), "")
}

// TestPnioDCP: pnio-dcp (Codewerk) against go-profinet's responder.
func TestPnioDCP(t *testing.T) {
	e := newEnv(t)
	img := image("pnio-dcp")
	e.result.peer("pnio-dcp", img, "dcp-tool")
	dev := e.startOTF("dut", 20, "device", "-station", "otf-dut")
	start := e.waitEvent(dev, 30*time.Second, "start", func(m map[string]any) bool { return m["event"] == "start" })
	mac := str(start["mac"])
	stop := e.capture(dev, "pnio-dcp.pcap")
	pd := func(name string, args ...string) map[string]any {
		var d map[string]any
		out := e.run(name, img, 4, 2*time.Minute, append([]string{"dcp"}, args...)...)
		if err := json.Unmarshal(lastLine(out), &d); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		return d
	}
	d := pd("identall", "identify-all", "--timeout", "2")
	e.expect("DCP Identify all", d["ok"] == true && strings.Contains(str(d["devices"]), "otf-dut"), "%v", d)
	d = pd("getname", "get-name", "--mac", mac)
	e.expect("DCP Get NameOfStation", d["value"] == "otf-dut", "%v", d)
	d = pd("setname", "set-name", "--mac", mac, "dcp-renamed", "--temporary")
	e.expect("DCP Set NameOfStation (temporary)", d["code"] == 0.0, "%v", d)
	d = pd("getname2", "get-name", "--mac", mac)
	e.expect("DCP Get reflects the new name", d["value"] == "dcp-renamed", "%v", d)
	d = pd("setip", "set-ip", "--mac", mac, e.ip(22), "255.255.255.0", "0.0.0.0", "--temporary")
	e.expect("DCP Set IP (temporary)", d["code"] == 0.0, "%v", d)
	d = pd("getip", "get-ip", "--mac", mac)
	e.expect("DCP Get IP", d["value"] == e.ip(22), "%v", d)
	d = pd("blink", "blink", "--mac", mac)
	e.expect("DCP Signal (blink)", d["code"] == 0.0, "%v", d)
	d = pd("reset", "reset", "--mac", mac)
	e.expect("DCP ResetToFactory", d["code"] == 0.0, "%v", d)
	e.oracles(stop())
}
