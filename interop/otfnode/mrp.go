// SPDX-License-Identifier: MIT

//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/bridge"
	"github.com/otfabric/go-profinet/l2/osport"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/mrp"
)

// ProbeType is the EtherType of the probe frames (IEEE 802 local
// experimental): broadcast frames that the ring's bridges flood, counted
// by every node to find loops (a probe seen twice) and holes (not seen).
const ProbeType = 0x88B5

// runMRP runs a multiport device with MRP on two ring ports (veth ends the
// harness moves into the container): a node of a ring with independent
// MRP implementations.
//
//	otfnode mrp -role manager|client [-ports ringA,ringB] [-profile 200|500]
//
// It prints a "ring" event whenever its MRP state changes and a "stats"
// event every second. SIGUSR1 sends a burst of -burst probe frames, 1 ms
// apart, and prints a "probes" event; SIGUSR2 starts and stops a stream
// of probes every millisecond (a "stream" event when it stops) to measure
// how long the ring carries no traffic during a fault.
func runMRP(args []string) error {
	fs := flag.NewFlagSet("mrp", flag.ExitOnError)
	role := fs.String("role", "client", "MRP role: manager or client")
	portList := fs.String("ports", "ringA,ringB", "the two ring ports")
	station := fs.String("station", "otf-ring", "station name")
	profile := fs.Int("profile", 200, "MRP recovery profile in ms: 200 or 500")
	burst := fs.Int("burst", 100, "probe frames per SIGUSR1")
	wait := fs.Duration("wait", time.Minute, "how long to wait for the ring ports to appear")
	_ = fs.Parse(args)

	cfg := &device.MRPConfig{Role: mrp.RoleClient, Profile: mrp.Profile200}
	switch *role {
	case "manager":
		cfg.Role = mrp.RoleManager
	case "client":
	default:
		return fmt.Errorf("unknown role %q", *role)
	}
	switch *profile {
	case 200:
	case 500:
		cfg.Profile = mrp.Profile500
	default:
		return fmt.Errorf("unknown profile %d", *profile)
	}
	names := strings.Split(*portList, ",")
	if len(names) != 2 {
		return fmt.Errorf("-ports needs two interfaces, got %q", *portList)
	}
	emit(map[string]any{"event": "start", "role": *role, "ports": names})
	deadline := time.Now().Add(*wait)
	for _, n := range names {
		for {
			if _, err := os.Stat("/sys/class/net/" + n); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("ring port %s did not appear", n)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	var ports []l2.Port
	for _, n := range names {
		if err := setLinkUp(n); err != nil {
			return err
		}
		p, err := osport.Open(n, osport.Options{Promiscuous: true})
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		ports = append(ports, p)
	}
	// The interface's address: the first port's, locally administered and
	// distinct from both ports'.
	mac := ports[0].HardwareAddr()
	mac[0] |= 0x02
	mac[5] ^= 0x80
	br, err := bridge.New(bridge.Config{Ports: ports, MAC: mac, Logger: profinet.NopLogger()})
	if err != nil {
		return err
	}
	defer func() { _ = br.Close() }()
	cpu := br.CPU()
	mux := l2.NewMux(cpu)
	defer func() { _ = mux.Close() }()
	m := defaultModel()
	m.Capabilities.Ports = 2
	m.DAP = model.StandardDAP("DAP", 0x00000100, 2)
	dev, err := device.New(device.Config{Model: m, Mux: mux, Bridge: br, StationName: *station, Handler: otfHandler{},
		Logger: profinet.NopLogger(), MRP: cfg})
	if err != nil {
		return err
	}
	defer func() { _ = dev.Close() }()
	emit(map[string]any{"event": "ready", "mac": cpu.HardwareAddr().String()})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGUSR1, syscall.SIGUSR2)
	var seq uint32
	probe := func() {
		seq++
		f := make([]byte, 0, 60)
		f = append(f, ethernet.Broadcast[:]...)
		src := cpu.HardwareAddr()
		f = append(f, src[:]...)
		f = binary.BigEndian.AppendUint16(f, ProbeType)
		f = append(f, "OTFPROBE"...)
		f = binary.BigEndian.AppendUint32(f, seq)
		_ = cpu.WriteFrame(ctx, ethernet.Pad(f, 0))
	}
	status := func() map[string]any {
		st, _ := dev.MRPStatus()
		ev := map[string]any{"state": st.State, "open": st.RingOpen, "blocked": []bool{st.Blocked[0], st.Blocked[1]},
			"transitions": st.Transitions, "testsSent": st.TestsSent, "testsReceived": st.TestsReceived,
			"topologyChanges": st.TopologyChanges, "linkChanges": st.LinkChanges, "forwarded": st.Forwarded, "malformed": st.Malformed}
		var ps []map[string]any
		for i := range ports {
			s := br.Stats(i)
			ps = append(ps, map[string]any{"received": s.Received, "sent": s.Sent, "forwarded": s.Forwarded, "dropped": s.Dropped,
				"state": br.State(i).String(), "link": linkUp(ports[i])})
		}
		ev["ports"] = ps
		return ev
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var last string
	lastStats := time.Now()
	var stream *time.Ticker
	var streamC <-chan time.Time
	var streamSent uint32
	var streamStart time.Time
	for {
		select {
		case <-ctx.Done():
			emit(map[string]any{"event": "stop"})
			return nil
		case s := <-sig:
			if s == syscall.SIGUSR1 {
				first := seq + 1
				for range *burst {
					probe()
					time.Sleep(time.Millisecond)
				}
				emit(map[string]any{"event": "probes", "sent": *burst, "first": first})
				continue
			}
			if stream == nil {
				stream = time.NewTicker(time.Millisecond)
				streamC, streamSent, streamStart = stream.C, 0, time.Now()
				emit(map[string]any{"event": "stream-start"})
			} else {
				stream.Stop()
				stream, streamC = nil, nil
				emit(map[string]any{"event": "stream", "sent": streamSent, "ms": time.Since(streamStart).Milliseconds()})
			}
		case <-streamC:
			probe()
			streamSent++
		case <-tick.C:
			st := status()
			k := fmt.Sprint(st["state"], st["open"], st["blocked"])
			if k != last {
				last = k
				st["event"] = "ring"
				emit(st)
			}
			if time.Since(lastStats) >= time.Second {
				lastStats = time.Now()
				st["event"] = "stats"
				emit(st)
			}
		}
	}
}

// linkUp reports a port's link state where the port knows it.
func linkUp(p l2.Port) bool {
	if l, ok := p.(l2.LinkStater); ok {
		return l.LinkUp()
	}
	return true
}
