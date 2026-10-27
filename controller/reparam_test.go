// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/pnio"
)

// prmHandler counts PrmEnds; from the second on it refuses (fail) or
// delays (delay) them.
type prmHandler struct {
	device.NopHandler
	n     atomic.Int32
	fail  bool
	delay time.Duration
}

func (h *prmHandler) PrmEnd(ctx context.Context, _ device.ARInfo) error {
	if h.n.Add(1) < 2 {
		return nil
	}
	if h.delay > 0 {
		select {
		case <-time.After(h.delay):
		case <-ctx.Done():
		}
	}
	if h.fail {
		return errors.New("refused")
	}
	return nil
}

func runningWith(t *testing.T, h device.Handler, cfg DeviceConfig) (*Device, *device.Device) {
	t.Helper()
	n := newNet(t)
	c, _ := n.controller()
	dev, _ := n.device(devIP, func(dc *device.Config) { dc.Handler = h })
	cfg.StationName, cfg.Model = "dut", testModel()
	cd, err := c.AddDevice(cfg)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, StateRunning, 10*time.Second)
	return cd, dev
}

func TestReparameterize(t *testing.T) {
	h := &prmHandler{}
	cd, dev := runningWith(t, h, DeviceConfig{Parameters: []Parameter{{Slot: 2, Subslot: 1, Index: 0x10, Data: []byte{0, 2}}}})
	ctx := context.Background()
	if err := cd.Reparameterize(ctx, []Parameter{{Slot: 2, Subslot: 1, Index: 0x10, Data: []byte{0, 7}}}); err != nil {
		t.Fatal(err)
	}
	if b, err := cd.ReadRecord(ctx, Record{Slot: 2, Subslot: 1, Index: 0x10}, 0); err != nil || b[1] != 7 {
		t.Fatalf("record % x %v", b, err)
	}
	if h.n.Load() != 2 {
		t.Fatalf("%d PrmEnds", h.n.Load())
	}
	ps := cd.parameters()
	if len(ps) != 1 || ps[0].Data[1] != 7 {
		t.Fatalf("parameters %+v", ps)
	}
	if st, _ := cd.State(); st != StateRunning {
		t.Fatalf("state %s", st)
	}
	// A write the device refuses ends the re-parameterization; the AR
	// stays, in Parameterizing at the device until the controller gives
	// up on it.
	err := cd.Reparameterize(ctx, []Parameter{{Slot: 2, Subslot: 1, Index: 0x7777, Data: []byte{1}}})
	var se *pnio.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("refused write: %v", err)
	}
	if ars := dev.ARs(); len(ars) != 1 {
		t.Fatalf("device ARs %+v", ars)
	}
}

func TestReparameterizeStates(t *testing.T) {
	n := newNet(t)
	c, _ := n.controller()
	cd, err := c.AddDevice(DeviceConfig{StationName: "nobody", Model: testModel()})
	if err != nil {
		t.Fatal(err)
	}
	if err := cd.Reparameterize(context.Background(), nil); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("not running: %v", err)
	}
}

func TestReparameterizeRefused(t *testing.T) {
	cd, _ := runningWith(t, &prmHandler{fail: true}, DeviceConfig{ReconnectMin: time.Hour})
	if err := cd.Reparameterize(context.Background(), nil); !errors.Is(err, profinet.ErrAborted) {
		t.Fatalf("refused PrmEnd: %v", err)
	}
}

func TestReparameterizeTimeout(t *testing.T) {
	cd, _ := runningWith(t, &prmHandler{delay: 5 * time.Second}, DeviceConfig{ActivityTimeout: 300 * time.Millisecond, ReconnectMin: time.Hour})
	start := time.Now()
	if err := cd.Reparameterize(context.Background(), nil); !errors.Is(err, profinet.ErrTimeout) || time.Since(start) > 3*time.Second {
		t.Fatalf("no ApplicationReady: %v after %s", err, time.Since(start))
	}
}

func TestReparameterizeContext(t *testing.T) {
	cd, _ := runningWith(t, &prmHandler{delay: 5 * time.Second}, DeviceConfig{ReconnectMin: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := cd.Reparameterize(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestMergeParameters(t *testing.T) {
	old := []Parameter{{Slot: 1, Subslot: 1, Index: 1, Data: []byte{1}}, {Slot: 2, Subslot: 1, Index: 1, Data: []byte{2}}}
	upd := []Parameter{{Slot: 2, Subslot: 1, Index: 1, Data: []byte{9}}, {Slot: 3, Subslot: 1, Index: 5, Data: []byte{3}}}
	got := mergeParameters(old, upd)
	if len(got) != 3 || got[0].Data[0] != 1 || got[1].Data[0] != 9 || got[2].Index != 5 {
		t.Fatalf("%+v", got)
	}
	upd[0].Data[0] = 0
	if got[1].Data[0] != 9 || old[1].Data[0] != 2 {
		t.Fatal("merge shares data")
	}
}
