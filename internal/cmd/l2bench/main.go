// SPDX-License-Identifier: MIT

//go:build linux

// Command l2bench measures go-profinet's Linux transport on a veth pair
// (scripts/l2-bench.sh creates one in a container): it is a measurement
// tool, not a test, and it asserts nothing.
//
//	l2bench -a pnA -b pnB [-duration 30s] [-rr 1] [-label baseline] [-json out.json]
//
// Three measurements:
//
//   - flood: the raw AF_PACKET port (l2/afpacket) sends frames from a to b
//     as fast as it can; b reads them. Frames per second, losses, CPU time
//     and heap allocations per frame.
//   - pingpong: a sends a frame, b echoes it, a reads it: the round-trip
//     time of ReadFrame and WriteFrame, percentiles.
//   - cyclic: an IO-Controller on a and an IO-Device on b (osport, the
//     netstack IP stack) exchange cyclic data at SendClockFactor 32 ×
//     ReductionRatio rr (1 ms at rr 1). A separate raw socket on each end,
//     with kernel receive timestamps (SO_TIMESTAMPNS), records the arrival
//     of every cyclic frame, so that the intervals are measured outside
//     the code under test: percentiles, the largest interval, frames
//     missing from the cycle counter, late frames (more than 1.5 cycles),
//     reconnects, watchdog expiries, CPU time, allocations and garbage
//     collections of the process.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/afpacket"
	"github.com/otfabric/go-profinet/l2/osport"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/netstack"
	"github.com/otfabric/go-profinet/pnio"
)

// Report is what l2bench prints (JSON with -json).
type Report struct {
	Label     string    `json:"label"`
	Date      time.Time `json:"date"`
	GoVersion string    `json:"goVersion"`
	Kernel    string    `json:"kernel"`
	CPUs      int       `json:"cpus"`
	GOARCH    string    `json:"goarch"`
	Flood     *Flood    `json:"flood,omitempty"`
	PingPong  *Latency  `json:"pingpong,omitempty"`
	Cyclic    *Cyclic   `json:"cyclic,omitempty"`
}

// Flood is the flood measurement.
type Flood struct {
	Frames         int     `json:"frames"`
	Received       int     `json:"received"`
	FramesPerSec   float64 `json:"framesPerSec"`
	CPUNsPerFrame  float64 `json:"cpuNsPerFrame"`
	AllocsPerFrame float64 `json:"allocsPerFrame"`
	BytesPerFrame  float64 `json:"bytesPerFrame"`
}

// Latency are percentiles of a duration, in microseconds.
type Latency struct {
	Samples int     `json:"samples"`
	MinUs   float64 `json:"minUs"`
	P50Us   float64 `json:"p50Us"`
	P95Us   float64 `json:"p95Us"`
	P99Us   float64 `json:"p99Us"`
	P999Us  float64 `json:"p999Us"`
	MaxUs   float64 `json:"maxUs"`
	MeanUs  float64 `json:"meanUs"`
}

// Direction is one direction of the cyclic exchange.
type Direction struct {
	Frames    int     `json:"frames"`
	Missing   int     `json:"missing"`
	Late      int     `json:"late"`
	Intervals Latency `json:"intervals"`
}

// Cyclic is the cyclic measurement.
type Cyclic struct {
	CycleUs          float64   `json:"cycleUs"`
	Seconds          float64   `json:"seconds"`
	DeviceToCtrl     Direction `json:"deviceToController"`
	CtrlToDevice     Direction `json:"controllerToDevice"`
	Reconnects       uint64    `json:"reconnects"`
	Expiries         uint64    `json:"watchdogExpiries"`
	SchedCycles      uint64    `json:"schedulerCycles"`
	SchedMissed      uint64    `json:"schedulerMissed"`
	ProviderSent     uint64    `json:"providerSent"`
	SchedMaxLateUs   float64   `json:"schedulerMaxLatenessUs"`
	SchedMeanLateUs  float64   `json:"schedulerMeanLatenessUs"`
	CPUPercent       float64   `json:"cpuPercent"`
	AllocsPerSec     float64   `json:"allocsPerSec"`
	AllocBytesPerSec float64   `json:"allocBytesPerSec"`
	GCs              uint32    `json:"gcs"`
	GCPauseTotalUs   float64   `json:"gcPauseTotalUs"`
	GCPauseMaxUs     float64   `json:"gcPauseMaxUs"`
}

func main() {
	a := flag.String("a", "pnA", "first end of the veth pair (controller)")
	b := flag.String("b", "pnB", "second end (device)")
	dur := flag.Duration("duration", 30*time.Second, "length of the cyclic measurement")
	rr := flag.Uint("rr", 1, "reduction ratio (send clock 1 ms × rr / 1)")
	frames := flag.Int("frames", 200000, "frames of the flood")
	pings := flag.Int("pings", 20000, "round trips of the ping-pong")
	label := flag.String("label", "", "a name for this run")
	out := flag.String("json", "", "write the report to this file")
	only := flag.String("only", "", "run one measurement: flood, pingpong or cyclic")
	memprofile := flag.String("memprofile", "", "write an allocation profile to this file at the end")
	flag.Parse()
	if *memprofile != "" {
		runtime.MemProfileRate = 1
		defer func() {
			f, err := os.Create(*memprofile)
			if err != nil {
				fail(err)
			}
			_ = pprof.Lookup("allocs").WriteTo(f, 0)
			_ = f.Close()
		}()
	}
	var uts syscall.Utsname
	_ = syscall.Uname(&uts)
	rep := Report{Label: *label, Date: time.Now().UTC(), GoVersion: runtime.Version(), Kernel: cstr(uts.Release[:]), CPUs: runtime.NumCPU(), GOARCH: runtime.GOARCH}
	var err error
	if *only == "" || *only == "flood" {
		if rep.Flood, err = flood(*a, *b, *frames); err != nil {
			fail(err)
		}
	}
	if *only == "" || *only == "pingpong" {
		if rep.PingPong, err = pingpong(*a, *b, *pings); err != nil {
			fail(err)
		}
	}
	if *only == "" || *only == "cyclic" {
		if rep.Cyclic, err = cyclic(*a, *b, *dur, uint16(*rr)); err != nil {
			fail(err)
		}
	}
	js, _ := json.MarshalIndent(rep, "", "  ")
	if *out != "" {
		if err := os.WriteFile(*out, js, 0o644); err != nil {
			fail(err)
		}
	}
	fmt.Println(string(js))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "l2bench:", err)
	os.Exit(1)
}

func cstr(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}

// benchType is the EtherType of the flood and ping-pong frames (IEEE 802
// local experimental 2).
const benchType = 0x88B6

func benchFrame(src ethernet.MAC, seq uint32) []byte {
	h := ethernet.Frame{Dst: ethernet.Broadcast, Src: src, EtherType: benchType}
	b := h.AppendHeader(make([]byte, 0, 64))
	b = binary.BigEndian.AppendUint32(b, seq)
	return ethernet.Pad(b, 0)
}

func benchSeq(b []byte) (uint32, bool) {
	if len(b) < 18 || binary.BigEndian.Uint16(b[12:]) != benchType {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[14:]), true
}

type usage struct {
	cpu     time.Duration
	mallocs uint64
	bytes   uint64
	gcs     uint32
	pause   uint64
}

func measure() usage {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	cpu := time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	return usage{cpu: cpu, mallocs: ms.Mallocs, bytes: ms.TotalAlloc, gcs: ms.NumGC, pause: ms.PauseTotalNs}
}

func openPair(a, b string) (*afpacket.Port, *afpacket.Port, error) {
	pa, err := afpacket.Open(a, afpacket.Options{})
	if err != nil {
		return nil, nil, err
	}
	pb, err := afpacket.Open(b, afpacket.Options{})
	if err != nil {
		_ = pa.Close()
		return nil, nil, err
	}
	return pa, pb, nil
}

// flood sends n frames from a to b as fast as the port takes them.
func flood(a, b string, n int) (*Flood, error) {
	pa, pb, err := openPair(a, b)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pa.Close(); _ = pb.Close() }()
	var got atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			k, err := pb.ReadFrame(ctx, buf)
			if err != nil {
				return
			}
			if _, ok := benchSeq(buf[:k]); ok {
				got.Add(1)
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	frames := make([][]byte, 256)
	for i := range frames {
		frames[i] = benchFrame(pa.HardwareAddr(), uint32(i))
	}
	runtime.GC()
	u0 := measure()
	start := time.Now()
	wctx := context.Background()
	for i := range n {
		// Pacing by the socket: a full send queue is retried.
		for {
			if err := pa.WriteFrame(wctx, frames[i%len(frames)]); err == nil {
				break
			} else if !errors.Is(err, syscall.ENOBUFS) {
				cancel()
				return nil, err
			}
			runtime.Gosched()
		}
	}
	// Wait for the tail.
	deadline := time.Now().Add(2 * time.Second)
	for got.Load() < int64(n) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	el := time.Since(start)
	u1 := measure()
	cancel()
	_ = pb.Close()
	<-done
	r := int(got.Load())
	return &Flood{Frames: n, Received: r, FramesPerSec: float64(r) / el.Seconds(),
		CPUNsPerFrame:  float64(u1.cpu-u0.cpu) / float64(n),
		AllocsPerFrame: float64(u1.mallocs-u0.mallocs) / float64(n),
		BytesPerFrame:  float64(u1.bytes-u0.bytes) / float64(n)}, nil
}

// pingpong measures n round trips a → b → a.
func pingpong(a, b string, n int) (*Latency, error) {
	pa, pb, err := openPair(a, b)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pa.Close(); _ = pb.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		buf := make([]byte, 2048)
		for {
			k, err := pb.ReadFrame(ctx, buf)
			if err != nil {
				return
			}
			if seq, ok := benchSeq(buf[:k]); ok {
				_ = pb.WriteFrame(ctx, benchFrame(pb.HardwareAddr(), seq))
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	buf := make([]byte, 2048)
	rtts := make([]time.Duration, 0, n)
	for i := range n {
		seq := uint32(i)
		start := time.Now()
		if err := pa.WriteFrame(ctx, benchFrame(pa.HardwareAddr(), seq)); err != nil {
			return nil, err
		}
		rctx, rc := context.WithTimeout(ctx, time.Second)
		for {
			k, err := pa.ReadFrame(rctx, buf)
			if err != nil {
				rc()
				return nil, fmt.Errorf("pingpong %d: %w", i, err)
			}
			if s, ok := benchSeq(buf[:k]); ok && s == seq && ethernet.MAC(buf[6:12]) == pb.HardwareAddr() {
				break
			}
		}
		rc()
		rtts = append(rtts, time.Since(start))
	}
	l := percentiles(rtts)
	return &l, nil
}

func percentiles(d []time.Duration) Latency {
	if len(d) == 0 {
		return Latency{}
	}
	slices.Sort(d)
	us := func(x time.Duration) float64 { return math.Round(float64(x)/10) / 100 }
	at := func(q float64) float64 { return us(d[min(len(d)-1, int(q*float64(len(d))))]) }
	var sum time.Duration
	for _, x := range d {
		sum += x
	}
	return Latency{Samples: len(d), MinUs: us(d[0]), P50Us: at(0.50), P95Us: at(0.95), P99Us: at(0.99), P999Us: at(0.999),
		MaxUs: us(d[len(d)-1]), MeanUs: us(sum / time.Duration(len(d)))}
}

// sniffer records the kernel receive time and cycle counter of the
// cyclic frames that arrive on an interface.
type sniffer struct {
	fd      int
	mu      sync.Mutex
	times   []int64
	counter []uint16
}

// newSniffer records the cyclic frames (FrameID 0x8000 to 0xFBFF) that
// arrive on an interface: on each end of the pair, the other end's.
func newSniffer(name string) (*sniffer, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, int(htons(0x8892)))
	if err != nil {
		return nil, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: htons(0x8892), Ifindex: ifi.Index}); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TIMESTAMPNS, 1); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	tv := syscall.NsecToTimeval(int64(200 * time.Millisecond))
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	return &sniffer{fd: fd}, nil
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func (s *sniffer) run(stop <-chan struct{}) {
	buf := make([]byte, 2048)
	oob := make([]byte, 128)
	for {
		select {
		case <-stop:
			return
		default:
		}
		n, oobn, _, from, err := syscall.Recvmsg(s.fd, buf, oob, 0)
		if err != nil {
			continue
		}
		if ll, ok := from.(*syscall.SockaddrLinklayer); ok && ll.Pkttype == syscall.PACKET_OUTGOING {
			continue
		}
		// FrameID at 14 (untagged: the kernel strips a VLAN tag); the
		// APDU status ends the frame (RT pads the C_SDU, not the frame):
		// cycle counter (2), data status, transfer status.
		if n < 60 {
			continue
		}
		if id := binary.BigEndian.Uint16(buf[14:]); id < 0x8000 || id > 0xFBFF {
			continue
		}
		ts, ok := rxTime(oob[:oobn])
		if !ok {
			continue
		}
		s.mu.Lock()
		s.times = append(s.times, ts)
		s.counter = append(s.counter, binary.BigEndian.Uint16(buf[n-4:n-2]))
		s.mu.Unlock()
	}
}

// rxTime returns the SCM_TIMESTAMPNS time of a received frame.
func rxTime(oob []byte) (int64, bool) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return 0, false
	}
	for _, m := range msgs {
		if m.Header.Level == syscall.SOL_SOCKET && m.Header.Type == syscall.SO_TIMESTAMPNS && len(m.Data) >= int(unsafe.Sizeof(syscall.Timespec{})) {
			ts := (*syscall.Timespec)(unsafe.Pointer(&m.Data[0]))
			return ts.Nano(), true
		}
	}
	return 0, false
}

func (s *sniffer) direction(cycle time.Duration, step uint16) Direction {
	s.mu.Lock()
	defer s.mu.Unlock()
	var iv []time.Duration
	d := Direction{Frames: len(s.times)}
	for i := 1; i < len(s.times); i++ {
		gap := time.Duration(s.times[i] - s.times[i-1])
		iv = append(iv, gap)
		if gap > cycle*3/2 {
			d.Late++
		}
		// The cycle counter advances by SendClockFactor × ReductionRatio
		// per frame; a larger step is missing frames.
		if adv := s.counter[i] - s.counter[i-1]; adv > step {
			d.Missing += int(adv/step) - 1
		}
	}
	d.Intervals = percentiles(iv)
	return d
}

func benchModel() *model.Device {
	return &model.Device{
		Identity:     model.Identity{VendorID: 0xF000, DeviceID: 0x0B01, VendorName: "OT Fabric", TypeOfStation: "l2bench device", OrderID: "L2BENCH", SerialNumber: "1", HardwareRevision: 1, SoftwareRevision: pnio.SoftwareRevision{Prefix: 'V', FunctionalEnhance: 1}},
		Capabilities: model.Capabilities{MaxARs: 1, MinDeviceInterval: 32, ConformanceClass: "A", Ports: 1},
		DAP:          model.StandardDAP("DAP", 0x00000100, 1),
		Slots: []model.Slot{
			{Number: 1, Module: model.Module{Name: "in", IdentNumber: 0x10, Submodules: []model.Submodule{{Name: "in", IdentNumber: 0x11, Subslot: 1, InputLen: 32}}}},
			{Number: 2, Module: model.Module{Name: "out", IdentNumber: 0x20, Submodules: []model.Submodule{{Name: "out", IdentNumber: 0x21, Subslot: 1, OutputLen: 32}}}},
		},
	}
}

var mask = netip.MustParseAddr("255.255.255.0")

// cyclic runs a controller on a and a device on b for d.
func cyclic(a, b string, d time.Duration, rr uint16) (*Cyclic, error) {
	open := func(name, ip string) (*l2.Mux, *netstack.Stack, func(), error) {
		p, err := osport.Open(name, osport.Options{})
		if err != nil {
			return nil, nil, nil, err
		}
		mux := l2.NewMux(p)
		st, err := netstack.New(mux, netstack.Config{Addr: netip.MustParseAddr(ip), Mask: mask})
		if err != nil {
			_ = mux.Close()
			_ = p.Close()
			return nil, nil, nil, err
		}
		return mux, st, func() { _ = st.Close(); _ = mux.Close(); _ = p.Close() }, nil
	}
	cm, cs, closeA, err := open(a, "10.98.0.1")
	if err != nil {
		return nil, err
	}
	defer closeA()
	dm, ds, closeB, err := open(b, "10.98.0.2")
	if err != nil {
		return nil, err
	}
	defer closeB()
	m := benchModel()
	dev, err := device.New(device.Config{Model: m, Mux: dm, StationName: "l2bench-dut", IP: dcp.IPSuite{Addr: netip.MustParseAddr("10.98.0.2"), Mask: mask},
		ListenPacket: ds.ListenPacket, Logger: profinet.NopLogger(), DisableLLDP: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = dev.Close() }()
	ctrl, err := controller.New(controller.Config{Mux: cm, ListenPacket: cs.ListenPacket, Logger: profinet.NopLogger()})
	if err != nil {
		return nil, err
	}
	defer func() { _ = ctrl.Close() }()
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "l2bench-dut", Model: m, ReductionRatio: rr, DataHoldFactor: 1000, WatchdogFactor: 1000})
	if err != nil {
		return nil, err
	}
	wctx, wc := context.WithTimeout(context.Background(), 20*time.Second)
	err = cd.WaitState(wctx, controller.StateRunning)
	wc()
	if err != nil {
		return nil, fmt.Errorf("not running: %w", err)
	}
	sa, err := newSniffer(a)
	if err != nil {
		return nil, err
	}
	sb, err := newSniffer(b)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 32)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); sa.run(stop) }()
	go func() { defer wg.Done(); sb.run(stop) }()
	// The applications update their data every cycle, as a real one would.
	go func() {
		defer wg.Done()
		t := time.NewTicker(time.Millisecond)
		defer t.Stop()
		var n byte
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			n++
			data[0] = n
			_ = dev.SetInput(1, 1, data, pnio.IOxSGood)
			_ = cd.SetOutput(2, 1, data, pnio.IOxSGood)
		}
	}()
	time.Sleep(500 * time.Millisecond)
	s0 := cd.Stats()
	runtime.GC()
	u0 := measure()
	var ms0 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	sa.mu.Lock()
	sa.times, sa.counter = nil, nil
	sa.mu.Unlock()
	sb.mu.Lock()
	sb.times, sb.counter = nil, nil
	sb.mu.Unlock()
	start := time.Now()
	time.Sleep(d)
	el := time.Since(start)
	u1 := measure()
	var ms1 runtime.MemStats
	runtime.ReadMemStats(&ms1)
	s1 := cd.Stats()
	close(stop)
	wg.Wait()
	_ = syscall.Close(sa.fd)
	_ = syscall.Close(sb.fd)
	cycle := time.Duration(rr) * time.Millisecond
	step := 32 * rr
	var maxPause uint64
	for i := ms0.NumGC; i < ms1.NumGC && i < ms0.NumGC+256; i++ {
		maxPause = max(maxPause, ms1.PauseNs[i%256])
	}
	return &Cyclic{
		CycleUs: float64(cycle.Microseconds()), Seconds: el.Seconds(),
		DeviceToCtrl: sa.direction(cycle, step), CtrlToDevice: sb.direction(cycle, step),
		Reconnects: s1.Connects - s0.Connects, Expiries: s1.Consumer.Expired - s0.Consumer.Expired,
		SchedCycles:      s1.Cycle.Cycles - s0.Cycle.Cycles,
		SchedMissed:      s1.Cycle.Missed - s0.Cycle.Missed,
		ProviderSent:     s1.Provider.Sent - s0.Provider.Sent,
		SchedMaxLateUs:   float64(s1.Cycle.MaxLateness.Microseconds()),
		SchedMeanLateUs:  float64(s1.Cycle.MeanLateness.Microseconds()),
		CPUPercent:       100 * float64(u1.cpu-u0.cpu) / float64(el),
		AllocsPerSec:     float64(u1.mallocs-u0.mallocs) / el.Seconds(),
		AllocBytesPerSec: float64(u1.bytes-u0.bytes) / el.Seconds(),
		GCs:              u1.gcs - u0.gcs,
		GCPauseTotalUs:   float64(u1.pause-u0.pause) / 1000,
		GCPauseMaxUs:     float64(maxPause) / 1000,
	}, nil
}
