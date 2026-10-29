// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/pnio"
)

// recorder is a profinet.Metrics that records its calls as text. A
// duration may be zero: Windows' clock can measure an in-process Connect
// as no time at all.
type recorder struct {
	profinet.NopMetrics
	mu     sync.Mutex
	events []string
	errs   []error
}

func (r *recorder) add(err error, f string, a ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(f, a...))
	if err != nil {
		r.errs = append(r.errs, err)
	}
}

func (r *recorder) OnStateChange(station, from, to string, err error) {
	r.add(err, "state %s %s>%s", station, from, to)
}

func (r *recorder) OnConnect(station string, d time.Duration, err error) {
	r.add(err, "connect %s %v", station, err == nil && d >= 0)
}

func (r *recorder) OnAbort(station string, err error) {
	r.add(err, "abort %s %v", station, errors.Is(err, profinet.ErrAborted) || errors.Is(err, profinet.ErrTimeout))
}

func (r *recorder) OnRecord(station, op string, index uint16, d time.Duration, err error) {
	r.add(err, "record %s %s %#x %v", station, op, index, err == nil && d >= 0)
}

func (r *recorder) OnAlarm(station, typ string, high bool) {
	r.add(nil, "alarm %s %s %v", station, typ, high)
}

func (r *recorder) OnRetry(station string, attempt int, wait time.Duration, err error) {
	r.add(err, "retry %s %d %v", station, attempt, wait > 0)
}

func (r *recorder) has(e string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.events {
		if x == e {
			return true
		}
	}
	return false
}

// TestMetrics: a controller's and a device's Metrics see the same AR from
// both ends: its states, the Connect, records, an alarm and the abort
// when the device goes away, and the controller's retry.
func TestMetrics(t *testing.T) {
	n := newNet(t)
	var cm, dm recorder
	c, _ := n.controller(func(cfg *Config) { cfg.Metrics = &cm })
	dev, _ := n.device(devIP, func(cfg *device.Config) { cfg.Metrics = &dm })
	cd, err := c.AddDevice(DeviceConfig{StationName: "dut", Model: testModel(), ReconnectMin: 50 * time.Millisecond, ReconnectMax: 100 * time.Millisecond,
		ReconnectJitter: 0.5, DataHoldFactor: 100})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, StateRunning, 10*time.Second)
	ctx := context.Background()
	if err := cd.WriteRecord(ctx, Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := cd.ReadRecord(ctx, Record{Slot: 2, Subslot: 1, Index: 0x10}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := cd.WriteMultiple(ctx, []WriteItem{{Record: Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{0, 4}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "device AR in Data", func() bool {
		ars := dev.ARs()
		return len(ars) == 1 && ars[0].State == device.ARStateData
	})
	if err := dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: 1, ErrorType: pnio.ChannelErrLineBreak}); err != nil {
		t.Fatal(err)
	}
	ctrl := c.cfg.StationName
	for _, e := range []string{
		"state dut Offline>Resolving", "state dut WaitApplicationReady>Running", "connect dut true",
		"record dut write 0x10 true", "record dut read 0x10 true", "record dut write 0xe040 true",
		"alarm dut Diagnosis false",
	} {
		eventually(t, 5*time.Second, "controller "+e, func() bool { return cm.has(e) })
	}
	for _, e := range []string{
		"connect " + ctrl + " true", "state " + ctrl + " ApplicationReadyPending>Data", "record " + ctrl + " write 0x10 true",
		"record " + ctrl + " read 0x10 true", "alarm " + ctrl + " Diagnosis false",
	} {
		eventually(t, 5*time.Second, "device "+e, func() bool { return dm.has(e) })
	}
	// The device goes away: the controller's watchdog aborts the AR and it
	// retries.
	_ = dev.Close()
	eventually(t, 10*time.Second, "abort", func() bool { return cm.has("abort dut true") })
	eventually(t, 10*time.Second, "retry", func() bool { return cm.has("retry dut 1 true") })
}

func TestReconnectJitterValidation(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	for _, j := range []float64{-0.1, 1.5} {
		if _, err := c.AddDevice(DeviceConfig{StationName: "dut", Model: testModel(), ReconnectJitter: j}); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("jitter %v: %v", j, err)
		}
	}
}
