// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/pnio"
)

// stateRec records a device's AR transitions; a PrmEnd it refuses aborts.
type stateRec struct {
	device.NopHandler
	mu       sync.Mutex
	states   []device.ARState
	failPrm  bool
	prmEnds  int
	recWrite map[pnio.Index][]byte
}

func (h *stateRec) StateChanged(_ device.ARInfo, _, to device.ARState) {
	h.mu.Lock()
	h.states = append(h.states, to)
	h.mu.Unlock()
}

func (h *stateRec) PrmEnd(context.Context, device.ARInfo) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prmEnds++
	if h.failPrm && h.prmEnds > 1 {
		return errors.New("refused")
	}
	return nil
}

func (h *stateRec) WriteRecord(_ context.Context, r device.RecordRequest, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.recWrite == nil {
		h.recWrite = map[pnio.Index][]byte{}
	}
	h.recWrite[r.Index] = append([]byte(nil), data...)
	return nil
}

func (h *stateRec) snapshot() ([]device.ARState, map[pnio.Index][]byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := map[pnio.Index][]byte{}
	for k, v := range h.recWrite {
		m[k] = v
	}
	return append([]device.ARState(nil), h.states...), m
}

// TestReparameterize: the controller re-parameterizes a running device:
// PrmBegin takes the device's AR back to Parameterizing, the new parameter
// is written, PrmEnd and ApplicationReady bring it to Data again; cyclic
// data goes on, and a reconnect writes the new value.
func TestReparameterize(t *testing.T) {
	e := newEnv(t, 31)
	h := &stateRec{}
	dev, _ := e.device("dut", "192.168.0.10", testModel(), h)
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(),
		Parameters: []controller.Parameter{{Slot: 2, Subslot: 1, Index: 0x10, Data: []byte{0, 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := cd.Reparameterize(ctx, nil); !errors.Is(err, profinet.ErrInvalidState) {
		t.Fatalf("before running: %v", err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	eventually(t, 2*time.Second, "device in Data", func() bool {
		ars := dev.ARs()
		return len(ars) == 1 && ars[0].State == device.ARStateData
	})
	_ = dev.SetInput(1, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8}, pnio.IOxSGood)
	waitInput(t, cd, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	before, _ := h.snapshot()

	start := time.Now()
	if err := cd.Reparameterize(ctx, []controller.Parameter{{Slot: 2, Subslot: 1, Index: 0x10, Data: []byte{0, 9}}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("re-parameterized in %s", time.Since(start))
	// The device enters Data when the answer to its ApplicationReady
	// arrives, just after Reparameterize returned.
	want := []device.ARState{device.ARStateParameterizing, device.ARStateAppReadyPending, device.ARStateData}
	var added []device.ARState
	eventually(t, 2*time.Second, "the device back in Data", func() bool {
		states, _ := h.snapshot()
		added = states[len(before):]
		return len(added) == len(want)
	})
	if added[0] != want[0] || added[1] != want[1] || added[2] != want[2] {
		t.Fatalf("device transitions %v, want %v", added, want)
	}
	_, writes := h.snapshot()
	if string(writes[0x10]) != string([]byte{0, 9}) {
		t.Fatalf("record %v", writes[0x10])
	}
	if st, _ := cd.State(); st != controller.StateRunning {
		t.Fatalf("controller %s", st)
	}
	_ = dev.SetInput(1, 1, []byte{8, 7, 6, 5, 4, 3, 2, 1}, pnio.IOxSGood)
	waitInput(t, cd, 1, []byte{8, 7, 6, 5, 4, 3, 2, 1})

	// A reconnect writes the new value.
	h.mu.Lock()
	h.recWrite = nil
	h.mu.Unlock()
	if err := dev.Close(); err != nil {
		t.Fatal(err)
	}
	e.device("dut", "192.168.0.11", testModel(), h)
	eventually(t, 15*time.Second, "the new value written at the reconnect", func() bool {
		_, w := h.snapshot()
		return string(w[0x10]) == string([]byte{0, 9})
	})
}

// TestReparameterizeRefused: a device whose application refuses the
// second PrmEnd aborts the AR: Reparameterize reports it, and the
// controller reconnects.
func TestReparameterizeRefused(t *testing.T) {
	e := newEnv(t, 32)
	h := &stateRec{failPrm: true}
	e.device("dut", "192.168.0.10", testModel(), h)
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(), ReconnectMin: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	err = cd.Reparameterize(context.Background(), nil)
	if err == nil {
		t.Fatal("a refused PrmEnd succeeded")
	}
	t.Logf("refused: %v", err)
}
