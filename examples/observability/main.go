// SPDX-License-Identifier: MIT

// Command observability wires a controller and a device to log/slog and to
// a profinet.Metrics that keeps Prometheus-style counters, pulls the cable
// and shows the abort, the jittered reconnection backoff and the counters.
// Replace the counters with your metrics library: every callback is a
// counter increment or a histogram observation.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/vnet"
)

// counters is a profinet.Metrics keeping labelled counters.
type counters struct {
	profinet.NopMetrics
	role string
	mu   sync.Mutex
	n    map[string]uint64
}

func (c *counters) inc(name string, labels ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := fmt.Sprintf("profinet_%s_total{role=%q", name, c.role)
	for i := 0; i+1 < len(labels); i += 2 {
		k += fmt.Sprintf(",%s=%q", labels[i], labels[i+1])
	}
	c.n[k+"}"]++
}

func result(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, profinet.ErrTimeout):
		return "timeout"
	case errors.Is(err, profinet.ErrNotFound):
		return "not_found"
	case errors.Is(err, profinet.ErrRejected):
		return "rejected"
	}
	return "error"
}

func (c *counters) OnStateChange(_, _, to string, _ error) { c.inc("state_changes", "to", to) }
func (c *counters) OnConnect(_ string, _ time.Duration, err error) {
	c.inc("connects", "result", result(err))
}
func (c *counters) OnAbort(string, error) { c.inc("aborts") }
func (c *counters) OnRecord(_, op string, _ uint16, _ time.Duration, err error) {
	c.inc("records", "op", op, "result", result(err))
}
func (c *counters) OnAlarm(_, typ string, _ bool) { c.inc("alarms", "type", typ) }
func (c *counters) OnRetry(_ string, _ int, wait time.Duration, err error) {
	c.inc("retries", "cause", result(err))
	fmt.Printf("  retry in %s (jittered)\n", wait.Round(10*time.Millisecond))
}

func (c *counters) print() {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.n))
	for k := range c.n {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s %d\n", k, c.n[k])
	}
}

func main() {
	// Structured logs at Info, without timestamps to keep the output stable.
	logger := profinet.NewSlogLogger(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}}))
	cm := &counters{role: "controller", n: map[string]uint64{}}
	dm := &counters{role: "device", n: map[string]uint64{}}

	network := vnet.New(7)
	defer func() { _ = network.Close() }()
	dn, err := vnetenv.NewNode(network, 10, "192.168.0.10")
	if err != nil {
		log.Fatal(err)
	}
	defer dn.Close()
	dev, err := device.New(device.Config{Model: vnetenv.Model(), Mux: dn.Mux, StationName: "io-1", IP: vnetenv.Suite("192.168.0.10"),
		ListenPacket: dn.Stack.ListenPacket, Metrics: dm})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = dev.Close() }()
	cn, err := vnetenv.NewNode(network, 1, "192.168.0.1")
	if err != nil {
		log.Fatal(err)
	}
	defer cn.Close()
	ctrl, err := controller.New(controller.Config{Mux: cn.Mux, ListenPacket: cn.Stack.ListenPacket, Logger: logger, Metrics: cm})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	io1, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "io-1", Model: vnetenv.Model(),
		ReconnectMin: 100 * time.Millisecond, ReconnectMax: time.Second, ReconnectJitter: 0.2})
	if err != nil {
		log.Fatal(err)
	}
	running := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := io1.WaitState(ctx, controller.StateRunning); err != nil {
			log.Fatal(err)
		}
	}
	running()
	ctx := context.Background()
	if _, err := io1.ReadIM0(ctx); err != nil {
		log.Fatal(err)
	}
	// A record that does not exist: counted with its result.
	_, err = io1.ReadRecord(ctx, controller.Record{Slot: 9, Subslot: 1, Index: 0x10}, 0)
	fmt.Printf("read of a missing slot: not found = %v\n", errors.Is(err, profinet.ErrNotFound))
	time.Sleep(100 * time.Millisecond) // the device enters Data after ApplicationReady
	if err := dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: 1, ErrorType: pnio.ChannelErrLineBreak}); err != nil {
		log.Fatal(err)
	}

	fmt.Println("cable pulled for 1 s")
	dn.Endpoint.SetLinkUp(false)
	time.Sleep(time.Second)
	dn.Endpoint.SetLinkUp(true)
	running()
	time.Sleep(100 * time.Millisecond)
	_ = ctrl.Close()

	fmt.Println()
	cm.print()
	dm.print()
}
