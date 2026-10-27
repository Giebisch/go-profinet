// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/pcap"
	"github.com/otfabric/go-profinet/pnio"
)

// countHellos returns the DCP Hellos of a capture in the share, by source
// MAC address.
func (e *env) countHellos(file string) map[ethernet.MAC]int {
	e.t.Helper()
	f, err := os.Open(filepath.Join(e.share, file))
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r, err := pcap.NewReader(f)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[ethernet.MAC]int{}
	for {
		_, b, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			e.t.Fatal(err)
		}
		if len(b) >= 16 && binary.BigEndian.Uint16(b[12:]) == uint16(ethernet.EtherTypeProfinet) && binary.BigEndian.Uint16(b[14:]) == dcp.FrameIDHello {
			out[ethernet.MAC(b[6:12])]++
		}
	}
}

// TestOtfHelloDecoders: go-profinet's device sends DCP Hello; tshark and
// scapy, independent of go-profinet, decode the Hellos without a
// malformed frame or an expert warning.
func TestOtfHelloDecoders(t *testing.T) {
	e := newEnv(t)
	e.result.peer("tshark/scapy", image("tshark"), "decoder")
	// The capture runs in a bystander on the bridge before the device
	// starts: its Hellos follow the start at once.
	sink := e.start("sink", runnerImage, 5, "sleep", "120")
	stop := e.capture(sink, "otf-hello.pcap")
	dev := e.startOTF("dut", 20, "device", "-station", "otf-hello", "-hello", "1")
	e.waitEvent(dev, 30*time.Second, "start", func(m map[string]any) bool { return m["event"] == "start" })
	time.Sleep(time.Second)
	file := stop()
	devMAC, err := ethernet.ParseMAC(e.mac(20))
	if err != nil {
		t.Fatal(err)
	}
	n := e.countHellos(file)[devMAC]
	e.expect("DCP Hello sent by the device (go-profinet)", n == 3, "%d Hellos (3 expected)", n)
	e.oracles(file)
}

// TestPnetHello: p-net sends DCP Hello when it starts with a name and an IP
// address; go-profinet's controller, which gave up on the device and waits
// 30 s before its next attempt, connects at once through the Hello.
func TestPnetHello(t *testing.T) {
	e := newEnv(t)
	sink := e.start("sink", runnerImage, 5, "sleep", "180")
	stop := e.capture(sink, "pnet-hello.pcap")
	ctrl := e.startOTF("ctrl", 2, "controller", "-station", "pnet-hello", "-model", "/otfnode-testdata/pnet-model.json",
		"-reconnect-min", "30s", "-timeout", "60s", "-duration", "2s")
	e.waitEvent(ctrl, 30*time.Second, "first resolve failed", func(m map[string]any) bool { return m["event"] == "state" && m["to"] == "Offline" })
	start := time.Now()
	pnet(e, "pnet-hello")
	rep := e.waitEvent(ctrl, 60*time.Second, "report", func(m map[string]any) bool { return m["event"] == "report" })
	file := stop()
	hellos := 0
	for _, c := range e.countHellos(file) {
		hellos += c
	}
	e.expect("DCP Hello sent by p-net", hellos > 0, "%d Hellos", hellos)
	stats, _ := rep["stats"].(map[string]any)
	e.expect("Controller connects through the device's Hello, without waiting for its retry",
		rep["running"] == true && stats != nil && stats["Hellos"].(float64) >= 1 && time.Since(start) < 25*time.Second,
		"running %v, stats %v, %s after p-net started", rep["running"], stats, time.Since(start))
	e.oracles(file)
}

// pnetPortMismatch returns the remote mismatch diagnoses (extended error
// types) of p-net's first port in a controller report.
func pnetPortMismatch(rep map[string]any) ([]float64, bool) {
	diag, ok := rep["diagnosis"].([]any)
	if !ok {
		return nil, false
	}
	var out []float64
	for _, d := range diag {
		m, _ := d.(map[string]any)
		if m["Subslot"] != float64(0x8001) {
			continue
		}
		items, _ := m["Items"].([]any)
		for _, it := range items {
			im, _ := it.(map[string]any)
			if im["ErrorType"] == float64(0x8001) {
				out = append(out, im["ExtErrorType"].(float64))
			}
		}
	}
	return out, true
}

// TestPnetPortCheck: go-profinet's controller writes PDPortDataCheck
// (CheckPeers) to p-net's port before PrmEnd; p-net, independent of
// go-profinet, decodes it (its log echoes the wanted peer) and evaluates it
// against its LLDP neighbor, a go-profinet device, with the diagnoses
// go-profinet's device reports in the same situations. Docker Desktop's
// bridge reflects p-net's own LLDP frames back to it, and p-net, which does
// not drop its own frames, then takes itself for a neighbor too; the check
// of the right neighbor is not applicable there.
func TestPnetPortCheck(t *testing.T) {
	e := newEnv(t)
	if !e.forwardLLDP() {
		e.result.na("PDPortDataCheck evaluated by p-net", "the Docker bridge does not forward LLDP here")
		t.Skip("bridge does not forward LLDP")
	}
	e.startOTF("peer", 20, "device", "-station", "otf-peer")
	dev := pnet(e, "pnet-dut")
	time.Sleep(7 * time.Second) // p-net hears the peer's LLDP (5 s interval)
	check := func(station, port string) map[string]any {
		c := pnio.PDPortDataCheck{Subslot: 0x8001, Peers: []pnio.CheckPeer{{PortID: port, ChassisID: station}}}
		out := e.otf("ctrl-"+station, 2, 90*time.Second, "controller", "-station", "pnet-dut", "-model", "/otfnode-testdata/pnet-model.json",
			"-param", "0:0x8001:0x802B:"+hex.EncodeToString(c.Append(nil)), "-diag", "-duration", "2s")
		return report(t, out)
	}
	pnetLog := func(pattern string) bool {
		out := string(e.exec(dev, "sh", "-c", "grep -c -F '"+pattern+"' /run/profinet-interop/pn_dev.log || true"))
		n := strings.TrimSpace(out)
		return n != "" && n != "0"
	}
	rep := check("otf-peer", "port-001")
	got, ok := pnetPortMismatch(rep)
	e.expect("PDPortDataCheck accepted by p-net (written before PrmEnd)", rep["running"] == true, "running %v error %v", rep["running"], rep["error"])
	if pnetLog(`mismatch diagnosis: "pnet-dut"`) {
		e.result.na("CheckPeers, the right neighbor: no remote mismatch (p-net)", "this bridge reflects p-net's own LLDP and p-net takes itself for a neighbor")
	} else {
		e.expect("CheckPeers, the right neighbor: no remote mismatch (p-net)", ok && len(got) == 0, "%v", rep["diagnosis"])
	}
	rep = check("nobody", "port-001")
	got, ok = pnetPortMismatch(rep)
	e.expect("CheckPeers, another name of station: remote mismatch 0x8000 (p-net)", ok && len(got) == 1 && got[0] == 0x8000, "%v", rep["diagnosis"])
	e.expect("CheckPeers decoded by p-net as written (its log)", pnetLog("Wanted first peer station name: nobody Port: port-001"), "")
}

// TestPnetSharedDevice: two go-profinet controllers share p-net (built for
// two ARs). A connects first and owns slot 1; B expects slots 1 and 2:
// p-net, independent of go-profinet, reports slot 1 to B as locked by
// another IO controller and gives B slot 2. When A is gone, p-net gives
// slot 1 to B with a Released alarm (one per submodule it hands over: the
// three of the DAP and slot 1).
func TestPnetSharedDevice(t *testing.T) {
	e := newEnv(t)
	pnet(e, "pnet-dut")
	ctrlA := e.startOTF("ctrl-a", 2, "controller", "-station", "pnet-dut", "-model", "/otfnode-testdata/pnet-model.json",
		"-slots", "1", "-duration", "6s", "-timeout", "60s")
	e.waitEvent(ctrlA, 60*time.Second, "A running", func(m map[string]any) bool { return m["event"] == "state" && m["to"] == "Running" })
	ctrlB := e.startOTF("ctrl-b", 3, "controller", "-station", "pnet-dut", "-model", "/otfnode-testdata/pnet-model.json",
		"-slots", "1,2", "-duration", "20s", "-wait-alarms", "4", "-timeout", "60s")
	repA := e.waitEvent(ctrlA, 90*time.Second, "A report", func(m map[string]any) bool { return m["event"] == "report" })
	repB := e.waitEvent(ctrlB, 90*time.Second, "B report", func(m map[string]any) bool { return m["event"] == "report" })
	e.expect("Two IOCARs at once: both controllers reach Running (p-net)", repA["running"] == true && repB["running"] == true, "A %v B %v", repA["error"], repB["error"])
	locked := map[float64]bool{}
	if diff, ok := repB["moduleDiff"].(map[string]any); ok {
		apis, _ := diff["APIs"].([]any)
		for _, api := range apis {
			mods, _ := api.(map[string]any)["Modules"].([]any)
			for _, m := range mods {
				mm := m.(map[string]any)
				subs, _ := mm["Submodules"].([]any)
				for _, s := range subs {
					if st, _ := s.(map[string]any)["State"].(float64); int(st)&(0xF<<7) == int(pnio.SubmStateARInfoCtrlLock) {
						locked[mm["Slot"].(float64)] = true
					}
				}
			}
		}
	}
	e.expect("ModuleDiff to the second controller: slot 1 locked by IO controller, slot 2 its own (p-net)", locked[1] && !locked[2], "%v", repB["moduleDiff"])
	released := false
	alarms, _ := repB["alarms"].([]any)
	for _, a := range alarms {
		m := a.(map[string]any)
		released = released || (m["type"] == "Released" && m["slot"] == float64(1))
	}
	e.expect("Released alarm to the second controller when the owner leaves (p-net)", released, "%v", alarms)
	in, _ := repB["inputs"].(map[string]any)
	s1, _ := in["1:1"].(map[string]any)
	e.expect("Slot 1 taken over (PrmEnd and ApplicationReady for the plugged submodule): its input valid at the second controller (p-net)",
		s1 != nil && s1["iops"] == float64(pnio.IOxSGood), "%v", in)
}
