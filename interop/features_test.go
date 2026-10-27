// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/mrp"
	"github.com/otfabric/go-profinet/pcap"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/ptcp"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
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

// ipv4UDP builds an Ethernet/IPv4/UDP frame (UDP without checksum).
func ipv4UDP(src, dst ethernet.MAC, sip, dip [4]byte, sport, dport uint16, payload []byte) []byte {
	be := binary.BigEndian
	udp := be.AppendUint16(be.AppendUint16(nil, sport), dport)
	udp = be.AppendUint16(be.AppendUint16(udp, uint16(8+len(payload))), 0)
	udp = append(udp, payload...)
	ip := []byte{0x45, 0, 0, 0, 0, 1, 0, 0, 64, 17, 0, 0}
	be.PutUint16(ip[2:], uint16(20+len(udp)))
	ip = append(append(ip, sip[:]...), dip[:]...)
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(be.Uint16(ip[i:]))
	}
	for sum > 0xFFFF {
		sum = sum&0xFFFF + sum>>16
	}
	be.PutUint16(ip[10:], ^uint16(sum))
	h := ethernet.Frame{Dst: dst, Src: src, EtherType: 0x0800}
	return append(append(h.AppendHeader(nil), ip...), udp...)
}

// TestCodecDecoders: frames go-profinet's codecs build for features whose
// behaviour no open implementation offers (MRP, PTCP delay, the IOCARSR and
// MCR blocks of a Connect) are decoded by tshark and scapy, independent of
// go-profinet, without a malformed frame or an expert warning.
func TestCodecDecoders(t *testing.T) {
	e := newEnv(t)
	e.result.peer("tshark/scapy", image("tshark"), "decoder")
	f, err := os.Create(filepath.Join(e.share, "codecs.pcap"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := pcap.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	src, dst := ethernet.MAC{2, 0, 0, 0, 0, 1}, ethernet.MAC{2, 0, 0, 0, 0, 2}
	add := func(frame []byte) {
		if err := w.WriteFrame(time.Now(), ethernet.Pad(frame, 0)); err != nil {
			t.Fatal(err)
		}
	}
	eth := func(d ethernet.MAC, et ethernet.EtherType, payload []byte) []byte {
		h := ethernet.Frame{Dst: d, Src: src, EtherType: et}
		return append(h.AppendHeader(nil), payload...)
	}
	for _, fr := range []mrp.Frame{
		{Test: &mrp.Test{Prio: mrp.DefaultManagerPrio, SA: src, RingState: mrp.RingClosed, TimeStamp: 5}, Common: &mrp.Common{SequenceID: 1, Domain: mrp.DefaultDomain}},
		{TopologyChange: &mrp.TopologyChange{Prio: mrp.DefaultManagerPrio, SA: src, Interval: 10}, Common: &mrp.Common{SequenceID: 2, Domain: mrp.DefaultDomain}},
		{Link: &mrp.LinkChange{SA: src, PortRole: mrp.PortSecondary, Interval: 20, Blocked: 1}, Common: &mrp.Common{SequenceID: 3, Domain: mrp.DefaultDomain}},
		{Link: &mrp.LinkChange{Up: true, SA: src, Interval: 20}, Common: &mrp.Common{SequenceID: 4, Domain: mrp.DefaultDomain}},
	} {
		b, err := fr.AppendTo(nil)
		if err != nil {
			t.Fatal(err)
		}
		d := mrp.MACControl
		if fr.Test != nil {
			d = mrp.MACTest
		}
		add(eth(d, mrp.EtherType, b))
	}
	req := ptcp.Delay{FrameID: ptcp.FrameIDDelayReq, SequenceID: 7, HasPortMAC: true, PortMAC: src, HasPortParameter: true, RxDelay: 100, TxDelay: 200}
	add(eth(ethernet.LLDPMulticast, ethernet.EtherTypeProfinet, req.AppendTo(nil)))
	res := ptcp.Delay{FrameID: ptcp.FrameIDDelayRes, SequenceID: 7, Delay: 1234, HasPortMAC: true, PortMAC: src, HasPortTime: true, T2TimeStamp: 99}
	add(eth(ethernet.LLDPMulticast, ethernet.EtherTypeProfinet, res.AppendTo(nil)))

	// A Connect request of an IOCARSR with its SRInfoBlock and an
	// MCRBlockReq.
	set := pnio.ARSetID{Discriminator: [6]byte{1, 2, 3, 4, 5, 6}, ConfigID: [8]byte{7, 7, 7, 7, 7, 7, 7, 7}, ARNumber: 1, Resource: 2}
	block := func(b []byte) pnio.RawBlock {
		blk, _, err := pnio.ReadBlock(b)
		if err != nil {
			t.Fatal(err)
		}
		return blk
	}
	cr := pnio.ConnectRequest{
		AR: pnio.ARBlockReq{Type: pnio.ARTypeIOCARSR, ARUUID: set.UUID(), SessionKey: 1, InitiatorMAC: src, InitiatorObject: pnio.ObjectUUID(1, 1, 0xF000),
			Properties: pnio.ARPropStateActive | pnio.ARPropPrmServerCMInit, ActivityTimeoutFactor: 100, UDPRTPort: pnio.LT, StationName: "plc"},
		Other: []pnio.RawBlock{
			block((&pnio.SRInfoBlock{RedundancyDataHoldFactor: 0xC8, Properties: pnio.SRPropInputValidOnBackupAR}).Append(nil)),
			block((&pnio.MCRBlockReq{IOCRReference: 2, AddressResolutionProperties: 1, MCITimeoutFactor: 10, ProviderStationName: "provider"}).Append(nil)),
		},
	}
	body := pnio.AppendNDRRequest(nil, binary.LittleEndian, pnio.DefaultArgsMaximum, cr.Append(nil))
	h := rpc.Header{Type: rpc.TypeRequest, Flags1: rpc.FlagIdempotent, DRep: [3]byte{rpc.DRepLittleEndian, 0, 0},
		Object: pnio.ObjectUUID(1, 2, 0xF000), Iface: pnio.DeviceInterface, Activity: rpc.NewRandomUUID(), IfaceVersion: pnio.InterfaceVersion,
		OpNum: uint16(pnio.OpConnect), IfaceHint: 0xFFFF, ActivityHint: 0xFFFF, BodyLen: uint16(len(body))}
	payload := append(h.AppendTo(nil), body...)
	add(ipv4UDP(src, dst, [4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2}, 49152, rpc.Port, payload))

	// The PROFINET MRP records: Write requests of the adjust and check
	// records, a Read response of PDInterfaceMrpDataReal.
	rpcPDU := func(typ rpc.PacketType, op pnio.OpNum, body []byte) []byte {
		h := rpc.Header{Type: typ, Flags1: rpc.FlagIdempotent, DRep: [3]byte{rpc.DRepLittleEndian, 0, 0},
			Object: pnio.ObjectUUID(1, 2, 0xF000), Iface: pnio.DeviceInterface, Activity: rpc.NewRandomUUID(), IfaceVersion: pnio.InterfaceVersion,
			OpNum: uint16(op), IfaceHint: 0xFFFF, ActivityHint: 0xFFFF, BodyLen: uint16(len(body))}
		return append(h.AppendTo(nil), body...)
	}
	for i, w := range []struct {
		sub  uint16
		idx  pnio.Index
		data []byte
	}{
		{0x8000, pnio.IndexPDInterfaceMrpDataAdjust, (&pnio.PDInterfaceMrpDataAdjust{Domain: mrp.DefaultDomain, Role: pnio.MrpRoleManager, DomainName: "mrpdomain-1",
			Manager: &pnio.MrpManagerParams{Prio: mrp.DefaultManagerPrio, TopChangeT: 10, TopNRmax: 3, TestShortT: 10, TestDefaultT: 20, TestNRmax: 3}}).Append(nil)},
		{0x8000, pnio.IndexPDInterfaceMrpDataAdjust, (&pnio.PDInterfaceMrpDataAdjust{Domain: mrp.DefaultDomain, Role: pnio.MrpRoleClient, DomainName: "mrpdomain-1",
			Client: &pnio.MrpClientParams{LinkDownT: 20, LinkUpT: 20, LinkNRmax: 4}}).Append(nil)},
		{0x8001, pnio.IndexPDPortMrpDataAdjust, (&pnio.PDPortMrpData{Domain: mrp.DefaultDomain}).Append(nil)},
		{0x8000, pnio.IndexPDInterfaceMrpDataCheck, (&pnio.PDInterfaceMrpDataCheck{Domain: mrp.DefaultDomain, Check: pnio.MrpCheckManager}).Append(nil)},
	} {
		q := pnio.WriteReq{Seq: uint16(i), ARUUID: rpc.NewRandomUUID(), Slot: 0, Subslot: w.sub, Index: w.idx, Length: uint32(len(w.data))}
		body := pnio.AppendNDRRequest(nil, binary.LittleEndian, pnio.DefaultArgsMaximum, append(q.Append(nil), w.data...))
		add(ipv4UDP(src, dst, [4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2}, 49152, rpc.Port, rpcPDU(rpc.TypeRequest, pnio.OpWrite, body)))
	}
	real := (&pnio.PDInterfaceMrpDataReal{Domain: mrp.DefaultDomain, Role: pnio.MrpRoleManager, DomainName: "mrpdomain-1", Version: 1,
		RingState: 1, HasRingState: true}).Append(nil)
	rr := pnio.ReadRes{Seq: 9, ARUUID: rpc.NewRandomUUID(), Slot: 0, Subslot: 0x8000, Index: pnio.IndexPDInterfaceMrpDataReal, Length: uint32(len(real))}
	rbody := pnio.AppendNDRResponse(nil, binary.LittleEndian, pnio.Status{}, pnio.DefaultArgsMaximum, append(rr.Append(nil), real...))
	add(ipv4UDP(dst, src, [4]byte{10, 0, 0, 2}, [4]byte{10, 0, 0, 1}, rpc.Port, 49152, rpcPDU(rpc.TypeResponse, pnio.OpRead, rbody)))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	e.expect("MRP Test, TopologyChange, LinkDown, LinkUp; PTCP DelayReq, DelayRes; IOCARSR Connect with SRInfoBlock and MCRBlockReq; MRP records (PDInterfaceMrpDataAdjust/Check/Real, PDPortMrpDataAdjust) encoded (go-profinet)", true, "")
	e.oracles("codecs.pcap")
}

// writeFrames writes Ethernet frames, padded to the minimum size, into a
// capture in the share.
func (e *env) writeFrames(file string, frames ...[]byte) {
	e.t.Helper()
	f, err := os.Create(filepath.Join(e.share, file))
	if err != nil {
		e.t.Fatal(err)
	}
	w, err := pcap.NewWriter(f)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, fr := range frames {
		if err := w.WriteFrame(time.Now(), ethernet.Pad(fr, 0)); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		e.t.Fatal(err)
	}
}

// scapyLayers returns the layer names scapy dissects in each frame of a
// capture in the share, followed by "dcp:<class>" for each DCP block of a
// DCP frame. The scapy adapter counts a UDP datagram, and a DCP frame
// whatever its blocks, as decoded; this looks at the layers themselves.
func (e *env) scapyLayers(file string) [][]string {
	e.t.Helper()
	const script = `import json, sys
from scapy.all import rdpcap
from scapy.contrib import pnio, pnio_dcp, pnio_rpc
out = []
for p in rdpcap(sys.argv[1]):
    names, layer = [], p
    while layer and layer.__class__.__name__ != "NoPayload":
        names.append(layer.__class__.__name__)
        layer = layer.payload
    if p.haslayer(pnio_dcp.ProfinetDCP):
        names += ["dcp:" + type(b).__name__ for b in (p[pnio_dcp.ProfinetDCP].dcp_blocks or [])]
    out.append(names)
print(json.dumps(out))`
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := docker(ctx, "run", "--rm", "-v", e.share+":/share", "--entrypoint", "python3", image("scapy"), "-c", script, "/share/"+file)
	if err != nil {
		e.t.Fatal(err)
	}
	var layers [][]string
	if err := json.Unmarshal(lastLine(out), &layers); err != nil {
		e.t.Fatalf("scapy layers: %v: %s", err, out)
	}
	return layers
}

// TestUDPRTDecoders: an RT_CLASS_UDP frame (the RT payload in a UDP
// datagram to port 34962) and an RT_CLASS_UDP Connect request (RTClass 4,
// LT 0x0800, alarms over UDP), as go-profinet's codecs encode them, are
// decoded by tshark and scapy, independent of go-profinet, without a
// malformed frame or an expert warning. No open implementation sends or
// accepts RT over UDP: this checks the encoding, not the behaviour.
func TestUDPRTDecoders(t *testing.T) {
	e := newEnv(t)
	e.result.peer("tshark/scapy", image("tshark"), "decoder")
	src, dst := ethernet.MAC{2, 0, 0, 0, 0, 1}, ethernet.MAC{2, 0, 0, 0, 0, 2}
	sip, dip := [4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2}
	fr := rt.Frame{FrameID: 0xC000, Data: []byte{0x80, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0x80, 0x80}, CycleCounter: 0x0100, DataStatus: rt.DefaultDataStatus}
	payload, err := rt.AppendUDPPayload(nil, &fr)
	if err != nil {
		t.Fatal(err)
	}
	udpRT := ipv4UDP(src, dst, sip, dip, rt.UDPPort, rt.UDPPort, payload)

	iocr := func(typ pnio.IOCRType, ref, id uint16) pnio.IOCRBlockReq {
		return pnio.IOCRBlockReq{Type: typ, Reference: ref, LT: pnio.LTIP, Properties: uint32(pnio.RTClassUDP), DataLength: rt.MinDataLengthUDP, FrameID: id,
			SendClockFactor: 32, ReductionRatio: 4, Phase: 1, FrameSendOffset: 0xFFFFFFFF, WatchdogFactor: 10, DataHoldFactor: 10, TagHeader: 6 << 13,
			APIs: []pnio.IOCRAPI{{IODataObjects: []pnio.IODataObject{{Slot: 1, Subslot: 1}}, IOCS: []pnio.IODataObject{{Slot: 2, Subslot: 1, FrameOffset: 10}}}}}
	}
	cr := pnio.ConnectRequest{
		AR: pnio.ARBlockReq{Type: pnio.ARTypeIOCARSingle, ARUUID: rpc.NewRandomUUID(), SessionKey: 1, InitiatorObject: pnio.ObjectUUID(1, 1, 0xF000),
			Properties: pnio.ARPropStateActive | pnio.ARPropPrmServerCMInit, ActivityTimeoutFactor: 100, UDPRTPort: 0x8000, StationName: "plc"},
		IOCRs: []pnio.IOCRBlockReq{iocr(pnio.IOCRInput, 1, 0xC000), iocr(pnio.IOCROutput, 2, pnio.FrameIDAssign)},
		AlarmCR: &pnio.AlarmCRBlockReq{Type: 1, LT: pnio.LTIP, Properties: pnio.AlarmCRPropTransportUDP, RTATimeoutFactor: 5, RTARetries: 3,
			LocalAlarmReference: 1, MaxAlarmDataLength: 200, TagHeaderHigh: 6 << 13, TagHeaderLow: 5 << 13},
	}
	body := pnio.AppendNDRRequest(nil, binary.LittleEndian, pnio.DefaultArgsMaximum, cr.Append(nil))
	h := rpc.Header{Type: rpc.TypeRequest, Flags1: rpc.FlagIdempotent, DRep: [3]byte{rpc.DRepLittleEndian, 0, 0},
		Object: pnio.ObjectUUID(1, 2, 0xF000), Iface: pnio.DeviceInterface, Activity: rpc.NewRandomUUID(), IfaceVersion: pnio.InterfaceVersion,
		OpNum: uint16(pnio.OpConnect), IfaceHint: 0xFFFF, ActivityHint: 0xFFFF, BodyLen: uint16(len(body))}
	connect := ipv4UDP(src, dst, sip, dip, 49152, rpc.Port, append(h.AppendTo(nil), body...))
	e.writeFrames("udp-rt.pcap", udpRT, connect)
	e.expect("RT_CLASS_UDP frame on UDP port 34962 and RT_CLASS_UDP Connect (RTClass 4, LT 0x0800, RTA_CLASS_UDP) encoded (go-profinet)", true, "")

	layers := e.scapyLayers("udp-rt.pcap")
	rtLayers := strings.Join(layers[0], "/")
	e.expect("scapy dissects the UDP payload of port 34962 as PROFINET RT (scapy)",
		strings.Contains(rtLayers, "ProfinetIO") && strings.Contains(rtLayers, "PNIORealTimeCyclicPDU"), "%s", rtLayers)
	e.oracles("udp-rt.pcap")
}

// TestDCPDHCPDecoders: DCP frames with DHCP blocks (option 3: the client
// identifier and the control suboption; the IP BlockInfo "set by DHCP"),
// as go-profinet's dcp package encodes them in Identify and Get responses
// and Get and Set requests, are decoded by tshark and scapy without a
// malformed frame or an expert warning. A DHCP option other than 61 and
// 255 (here 12, the host name) is shown by Wireshark as undecoded data:
// that is recorded as a deviation of the decoder, not of the encoding.
func TestDCPDHCPDecoders(t *testing.T) {
	e := newEnv(t)
	e.result.peer("tshark/scapy", image("tshark"), "decoder")
	dev, ctrl := ethernet.MAC{2, 0, 0, 0, 0, 0x20}, ethernet.MAC{2, 0, 0, 0, 0, 0x10}
	frame := func(dst, src ethernet.MAC, f *dcp.Frame) []byte {
		h := ethernet.Frame{Dst: dst, Src: src, EtherType: ethernet.EtherTypeProfinet}
		b, err := f.AppendTo(h.AppendHeader(nil))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	param := func(p dcp.DHCPParameter) []byte {
		b, err := dcp.AppendDHCPParameter(nil, p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	arb, err := dcp.DHCPClientIDArbitrary(0, []byte("dut-1"))
	if err != nil {
		t.Fatal(err)
	}
	id := dcp.Identity{NameOfStation: "dut", TypeOfStation: "go-profinet", VendorID: 0xF000, DeviceID: 1, Role: dcp.RoleIODevice,
		IP:     dcp.IPSuite{Addr: netip.MustParseAddr("192.168.0.10"), Mask: netip.MustParseAddr("255.255.255.0"), Gateway: netip.MustParseAddr("0.0.0.0")},
		IPInfo: dcp.IPInfoDHCP, DHCP: []dcp.DHCPParameter{dcp.DHCPClientIDFromMAC(), dcp.DHCPControlParameter(dcp.DHCPControlOn)}}
	arbID := id
	arbID.DHCP = []dcp.DHCPParameter{arb}
	hostID := id
	hostID.DHCP = []dcp.DHCPParameter{{Code: dcp.DHCPOptHostName, Data: []byte("dut")}}
	ctrlBlocks := func(errs ...dcp.BlockError) []dcp.Block {
		var out []dcp.Block
		for i, code := range errs {
			out = append(out, dcp.Block{ID: dcp.CtrlResponse, Data: dcp.AppendControlResponse(nil, dcp.ControlResponse{Block: []dcp.BlockID{dcp.DHCPControl, dcp.DHCPClientID}[i], Error: code})})
		}
		return out
	}
	frames := [][]byte{
		frame(ctrl, dev, &dcp.Frame{FrameID: dcp.FrameIDIdentifyResponse, Service: dcp.ServiceIdentify, Type: dcp.TypeResponseSuccess, Xid: 1, Blocks: id.Blocks()}),
		frame(ctrl, dev, &dcp.Frame{FrameID: dcp.FrameIDIdentifyResponse, Service: dcp.ServiceIdentify, Type: dcp.TypeResponseSuccess, Xid: 2, Blocks: arbID.Blocks()}),
		frame(dev, ctrl, &dcp.Frame{FrameID: dcp.FrameIDGetSet, Service: dcp.ServiceGet, Type: dcp.TypeRequest, Xid: 3, Blocks: []dcp.Block{{ID: dcp.DHCPClientID}, {ID: dcp.DHCPControl}}}),
		frame(ctrl, dev, &dcp.Frame{FrameID: dcp.FrameIDGetSet, Service: dcp.ServiceGet, Type: dcp.TypeResponseSuccess, Xid: 3, Blocks: []dcp.Block{
			{ID: dcp.DHCPClientID, HasInfo: true, Data: param(dcp.DHCPClientIDFromMAC())},
			{ID: dcp.DHCPControl, HasInfo: true, Data: param(dcp.DHCPControlParameter(dcp.DHCPControlOn))}}}),
		frame(dev, ctrl, &dcp.Frame{FrameID: dcp.FrameIDGetSet, Service: dcp.ServiceSet, Type: dcp.TypeRequest, Xid: 4, Blocks: []dcp.Block{
			{ID: dcp.DHCPControl, HasInfo: true, Info: dcp.QualifierPermanent, Data: param(dcp.DHCPControlParameter(dcp.DHCPControlOff))},
			{ID: dcp.DHCPClientID, HasInfo: true, Info: dcp.QualifierPermanent, Data: param(arb)}}}),
		frame(ctrl, dev, &dcp.Frame{FrameID: dcp.FrameIDGetSet, Service: dcp.ServiceSet, Type: dcp.TypeResponseSuccess, Xid: 4,
			Blocks: ctrlBlocks(dcp.BlockErrorNone, dcp.BlockErrorOptionUnsupported)}),
		frame(ctrl, dev, &dcp.Frame{FrameID: dcp.FrameIDIdentifyResponse, Service: dcp.ServiceIdentify, Type: dcp.TypeResponseSuccess, Xid: 5, Blocks: hostID.Blocks()}),
	}
	e.writeFrames("dcp-dhcp.pcap", frames...)
	e.expect("DCP DHCP blocks (client ID from MAC, arbitrary client ID, control) and IP set by DHCP in Identify/Get responses, Get and Set requests encoded (go-profinet)", true, "")
	// The DHCP blocks decode back to the identity in go-profinet itself.
	f, err := dcp.Decode(frames[0][14:])
	if err != nil {
		t.Fatal(err)
	}
	back, err := dcp.ParseIdentity(dev, f.Blocks)
	e.expect("DHCP options and IP BlockInfo parsed back (go-profinet)", err == nil && back.IPSetByDHCP() && len(back.DHCP) == 2, "%+v %v", back, err)
	// scapy has no class for DCP DHCP blocks: it dissects the first one,
	// and every block after it, as Raw. That is the decoder's limit, not
	// the encoding's (tshark decodes the same blocks): recorded, not hidden.
	raw := 0
	for _, l := range e.scapyLayers("dcp-dhcp.pcap") {
		if slices.Contains(l, "dcp:Raw") {
			raw++
		}
	}
	if raw > 0 {
		e.result.deviation("scapy: DCP option 3 (DHCP) blocks", fmt.Sprintf("scapy has no DCP DHCP block class: in %d of %d frames the first DHCP block and the blocks after it are left Raw", raw, len(frames)))
	} else {
		e.result.add("scapy dissects the DCP DHCP blocks (scapy)", true, "")
	}
	e.oracles("dcp-dhcp.pcap", peerDeviation{Protocol: "PN-DCP", Message: "Undecoded Data, 5 bytes",
		Why: "Wireshark decodes the data of DHCP options 61 and 255 only; option 12 (host name), which go-profinet encodes as code, length and data like the others, is shown as undecoded"})
}
