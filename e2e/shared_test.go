// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

// Shared device: several IO controllers connect to one device, each owning
// the submodules it connects first (docs/DEVICE.md).

func sharedModel() *model.Device {
	m := testModel()
	m.Capabilities.MaxARs = 3
	return m
}

// sharedController adds the device to a new controller, expecting only the
// given slots.
func (e *env) sharedController(ip string, slots []uint16, alarms chan<- controller.AlarmEvent) *controller.Device {
	e.t.Helper()
	ctrl, _ := e.controller(ip)
	cfg := controller.DeviceConfig{StationName: "shared", Model: sharedModel().WithSlots(slots...),
		ReconnectMin: 100 * time.Millisecond, ReconnectMax: 500 * time.Millisecond}
	if alarms != nil {
		cfg.OnAlarm = func(a controller.AlarmEvent) { alarms <- a }
	}
	cd, err := ctrl.AddDevice(cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	return cd
}

// lockedSlots returns the slots whose submodules a controller's ModuleDiff
// reports as locked by another IO controller.
func lockedSlots(cd *controller.Device) map[uint16]bool {
	out := map[uint16]bool{}
	if diff := cd.ModuleDiff(); diff != nil {
		for _, api := range diff.APIs {
			for _, m := range api.Modules {
				for _, sm := range m.Submodules {
					if sm.State&(0xF<<7) == pnio.SubmStateARInfoCtrlLock {
						out[m.Slot] = true
					}
				}
			}
		}
	}
	return out
}

func waitOutput(t *testing.T, dev *device.Device, slot uint16, want []byte) {
	t.Helper()
	eventually(t, 5*time.Second, fmt.Sprintf("output of slot %d % x", slot, want), func() bool {
		v, iops, _ := dev.Output(slot, 1)
		return bytes.Equal(v, want) && iops == pnio.IOxSGood
	})
}

func waitInput(t *testing.T, cd *controller.Device, slot uint16, want []byte) {
	t.Helper()
	eventually(t, 5*time.Second, fmt.Sprintf("input of slot %d % x", slot, want), func() bool {
		v, iops, _ := cd.Input(slot, 1)
		return bytes.Equal(v, want) && iops == pnio.IOxSGood
	})
}

// TestSharedDevice: controller A owns slots 1 and 3, B slot 2; both
// exchange data at once; A leaves and B runs on; a third controller that
// wants B's slot finds it locked and cannot write it; when B is gone, the
// slot goes to the third, which is told with a Released alarm.
func TestSharedDevice(t *testing.T) {
	e := newEnv(t, 81)
	dev, _ := e.device("shared", "192.168.0.10", sharedModel(), nil)
	alarmsA := make(chan controller.AlarmEvent, 16)
	alarmsB := make(chan controller.AlarmEvent, 16)
	alarmsC := make(chan controller.AlarmEvent, 16)
	a := e.sharedController("192.168.0.1", []uint16{1, 3}, alarmsA)
	waitState(t, a, controller.StateRunning, 10*time.Second)
	b := e.sharedController("192.168.0.2", []uint16{2}, alarmsB)
	waitState(t, b, controller.StateRunning, 10*time.Second)
	// The DAP belongs to A, B's own slot to B.
	if l := lockedSlots(b); !l[0] || l[2] {
		t.Fatalf("B's locked slots %v", l)
	}
	if l := lockedSlots(a); len(l) != 0 {
		t.Fatalf("A's locked slots %v", l)
	}

	// 2: both exchange cyclic data at the same time; 7, 8: inputs reach
	// their owner, mixed IO goes both ways for its owner only.
	_ = dev.SetInput(1, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8}, pnio.IOxSGood)
	_ = dev.SetInput(3, 1, []byte{9, 9}, pnio.IOxSGood)
	_ = a.SetOutput(3, 1, []byte{0xA3, 0xA3}, pnio.IOxSGood)
	_ = b.SetOutput(2, 1, []byte{0xB2, 0xB2, 0xB2, 0xB2}, pnio.IOxSGood)
	waitInput(t, a, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	waitInput(t, a, 3, []byte{9, 9})
	waitOutput(t, dev, 3, []byte{0xA3, 0xA3})
	waitOutput(t, dev, 2, []byte{0xB2, 0xB2, 0xB2, 0xB2})

	// 9: an alarm of slot 2 goes to B only, one of slot 1 to A only.
	ctx := context.Background()
	if err := dev.SendAlarm(ctx, device.Alarm{Type: pnio.AlarmProcess, Slot: 2, Subslot: 1}); err != nil {
		t.Fatal(err)
	}
	if err := dev.SendAlarm(ctx, device.Alarm{Type: pnio.AlarmProcess, Slot: 1, Subslot: 1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ch   chan controller.AlarmEvent
		slot uint16
	}{{alarmsB, 2}, {alarmsA, 1}} {
		select {
		case ev := <-c.ch:
			if ev.Notification.Slot != c.slot {
				t.Fatalf("alarm of slot %d to the owner of slot %d", ev.Notification.Slot, c.slot)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no alarm of slot %d", c.slot)
		}
	}
	select {
	case ev := <-alarmsA:
		t.Fatalf("A got %+v", ev.Notification)
	case ev := <-alarmsB:
		t.Fatalf("B got %+v", ev.Notification)
	case <-time.After(200 * time.Millisecond):
	}
	// The diagnosis alarm of a slot goes to its owner.
	if err := dev.AddDiagnosis(ctx, 2, 1, pnio.DiagnosisItem{ChannelNumber: 1, ErrorType: pnio.ChannelErrOverload}); err != nil {
		t.Fatal(err)
	}
	if ev := <-alarmsB; ev.Notification.Type != pnio.AlarmDiagnosis {
		t.Fatalf("%+v", ev.Notification)
	}

	// 4: a third controller wants B's slot: it connects, the slot is
	// locked, its outputs do not reach the device, its writes are denied.
	c := e.sharedController("192.168.0.3", []uint16{2}, alarmsC)
	waitState(t, c, controller.StateRunning, 10*time.Second)
	if l := lockedSlots(c); !l[2] {
		t.Fatalf("C's locked slots %v", l)
	}
	_ = c.SetOutput(2, 1, []byte{0xC2, 0xC2, 0xC2, 0xC2}, pnio.IOxSGood)
	time.Sleep(300 * time.Millisecond)
	if v, _, _ := dev.Output(2, 1); !bytes.Equal(v, []byte{0xB2, 0xB2, 0xB2, 0xB2}) {
		t.Fatalf("C overwrote B's outputs: % x", v)
	}
	err := c.WriteRecord(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 9})
	var se *pnio.StatusError
	if !errors.As(err, &se) || se.Status.Code1 != pnio.RWAccessDenied {
		t.Fatalf("C's write: %v", err)
	}
	if err := b.WriteRecord(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 7}); err != nil {
		t.Fatalf("B's write: %v", err)
	}

	// 3: A leaves; B runs on.
	_ = a.Close()
	time.Sleep(300 * time.Millisecond)
	if st, _ := b.State(); st != controller.StateRunning {
		t.Fatalf("B %s after A left", st)
	}
	_ = b.SetOutput(2, 1, []byte{0xB3, 0xB3, 0xB3, 0xB3}, pnio.IOxSGood)
	waitOutput(t, dev, 2, []byte{0xB3, 0xB3, 0xB3, 0xB3})

	// 6: B goes; slot 2 goes to C with a Released alarm, and C's
	// outputs reach the device.
	_ = b.Close()
	deadline := time.After(10 * time.Second)
	for released := false; !released; {
		select {
		case ev := <-alarmsC:
			released = ev.Notification.Type == pnio.AlarmReleased && ev.Notification.Slot == 2
		case <-deadline:
			t.Fatal("no Released alarm at C")
		}
	}
	waitOutput(t, dev, 2, []byte{0xC2, 0xC2, 0xC2, 0xC2})
}

// TestSharedOwnershipTimeout: the owner's link goes down; its AR times out
// at the device and the slot goes to the controller that waited for it.
func TestSharedOwnershipTimeout(t *testing.T) {
	e := newEnv(t, 82)
	dev, _ := e.device("shared", "192.168.0.10", sharedModel(), nil)
	ctrlB, nb := e.controller("192.168.0.2")
	b, err := ctrlB.AddDevice(controller.DeviceConfig{StationName: "shared", Model: sharedModel().WithSlots(2), ReconnectMin: time.Hour, ReconnectMax: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, b, controller.StateRunning, 10*time.Second)
	alarmsC := make(chan controller.AlarmEvent, 16)
	c := e.sharedController("192.168.0.3", []uint16{2}, alarmsC)
	waitState(t, c, controller.StateRunning, 10*time.Second)
	_ = c.SetOutput(2, 1, []byte{0xC2, 0xC2, 0xC2, 0xC2}, pnio.IOxSGood)
	nb.ep.SetLinkUp(false)
	waitOutput(t, dev, 2, []byte{0xC2, 0xC2, 0xC2, 0xC2})
	if l := len(dev.ARs()); l != 1 {
		t.Fatalf("%d ARs at the device", l)
	}
}

// TestSharedRace: two controllers connect at the same moment and both want
// slot 2: exactly one owns it.
func TestSharedRace(t *testing.T) {
	for i := 0; i < 5; i++ {
		e := newEnv(t, uint64(83+i))
		dev, _ := e.device("shared", "192.168.0.10", sharedModel(), nil)
		var cds [2]*controller.Device
		var wg sync.WaitGroup
		for j := range cds {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				cds[j] = e.sharedController(fmt.Sprintf("192.168.0.%d", j+1), []uint16{2}, nil)
			}(j)
		}
		wg.Wait()
		for _, cd := range cds {
			waitState(t, cd, controller.StateRunning, 10*time.Second)
		}
		owners := 0
		for j, cd := range cds {
			if !lockedSlots(cd)[2] {
				owners++
				_ = cd.SetOutput(2, 1, []byte{byte(j), 1, 2, 3}, pnio.IOxSGood)
				waitOutput(t, dev, 2, []byte{byte(j), 1, 2, 3})
			}
		}
		if owners != 1 {
			t.Fatalf("round %d: %d owners of slot 2", i, owners)
		}
	}
}

// TestSharedChurn: controllers connect, write, release and reconnect at
// the same time; nothing deadlocks, races or leaks an owner, and in the end
// each slot has exactly one owner exchanging data.
func TestSharedChurn(t *testing.T) {
	e := newEnv(t, 90)
	dev, _ := e.device("shared", "192.168.0.10", sharedModel(), nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	for j, slots := range [][]uint16{{1, 3}, {2}, {2, 3}} {
		wg.Add(1)
		go func(j int, slots []uint16) {
			defer wg.Done()
			ctrl, _ := e.controller(fmt.Sprintf("192.168.0.%d", j+1))
			for round := 0; round < 4; round++ {
				cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "shared", Model: sharedModel().WithSlots(slots...),
					ReconnectMin: 50 * time.Millisecond, ReconnectMax: 200 * time.Millisecond})
				if err != nil {
					t.Error(err)
					return
				}
				wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				if err := cd.WaitState(wctx, controller.StateRunning); err != nil {
					cancel()
					t.Errorf("controller %d round %d: %v", j, round, err)
					return
				}
				cancel()
				for _, s := range slots {
					if s == 2 {
						_ = cd.WriteRecord(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, byte(round)})
					}
				}
				time.Sleep(time.Duration(50+20*j) * time.Millisecond)
				_ = cd.Close()
			}
		}(j, slots)
	}
	wg.Wait()
	eventually(t, 10*time.Second, "all ARs gone", func() bool { return len(dev.ARs()) == 0 })
	// Afterwards a single controller gets every slot.
	cd := e.sharedController("192.168.0.9", []uint16{1, 2, 3}, nil)
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	if l := lockedSlots(cd); len(l) != 0 {
		t.Fatalf("locked after the churn: %v", l)
	}
	_ = cd.SetOutput(2, 1, []byte{4, 4, 4, 4}, pnio.IOxSGood)
	waitOutput(t, dev, 2, []byte{4, 4, 4, 4})
}

// TestSharedTakeoverParameters: a controller leaves the parameters of a
// submodule another one owns alone, and writes them when it takes the
// submodule over.
func TestSharedTakeoverParameters(t *testing.T) {
	e := newEnv(t, 91)
	dev, _ := e.device("shared", "192.168.0.10", sharedModel(), nil)
	b := e.sharedController("192.168.0.2", []uint16{2}, nil)
	waitState(t, b, controller.StateRunning, 10*time.Second)
	ctrlC, _ := e.controller("192.168.0.3")
	c, err := ctrlC.AddDevice(controller.DeviceConfig{StationName: "shared", Model: sharedModel().WithSlots(2),
		Parameters: []controller.Parameter{{Slot: 2, Subslot: 1, Index: 0x10, Data: []byte{0, 0x42}}}})
	if err != nil {
		t.Fatal(err)
	}
	// While B owns slot 2, C runs without it and leaves its parameter
	// alone; when B is gone, C takes slot 2 over and writes it.
	waitState(t, c, controller.StateRunning, 10*time.Second)
	if v, _, _ := dev.Output(2, 1); v == nil {
		t.Fatal("no slot 2")
	}
	_ = b.Close()
	eventually(t, 10*time.Second, "parameter written at the takeover", func() bool {
		v, err := c.ReadRecord(context.Background(), controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, 0)
		return err == nil && bytes.Equal(v, []byte{0, 0x42})
	})
	ctx := context.Background()
	if v, err := c.ReadRecord(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, 0); err != nil || !bytes.Equal(v, []byte{0, 0x42}) {
		t.Fatalf("parameter % x %v", v, err)
	}
	_ = c.SetOutput(2, 1, []byte{7, 7, 7, 7}, pnio.IOxSGood)
	waitOutput(t, dev, 2, []byte{7, 7, 7, 7})
}
