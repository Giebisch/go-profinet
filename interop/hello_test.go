// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/pcap"
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
