// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rt"
)

// srHold is the data hold of the IOCARSRs, in 4 ms cycles: the device
// notices a failed primary when it expires (1 s).
const srHold = 250

// srPair starts a device with system redundancy (experimental) and the
// two controllers of a redundant pair, controller 1 primary.
func srPair(t *testing.T, e *env, hold uint16) (*device.Device, [2]*controller.Device, [2]*node) {
	t.Helper()
	dev, _ := e.device("sr-dev", "192.168.0.10", testModel(), nil, func(c *device.Config) { c.SystemRedundancy = &device.SRConfig{} })
	var cds [2]*controller.Device
	var nodes [2]*node
	for i := range cds {
		ctrl, n := e.controller([]string{"192.168.0.1", "192.168.0.2"}[i])
		cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "sr-dev", Model: testModel(),
			SystemRedundancy: &controller.SRSetting{Discriminator: [6]byte{0xA, 0xB, 0xC, 0xD, 0xE, 0xF}, ConfigID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
				ARNumber: uint8(i + 1), Primary: i == 0, RedundancyDataHoldFactor: hold, InputValidOnBackup: true},
			ReconnectMin: time.Hour, DataHoldFactor: srHold, WatchdogFactor: srHold})
		if err != nil {
			t.Fatal(err)
		}
		cds[i], nodes[i] = cd, n
	}
	for _, cd := range cds {
		waitState(t, cd, controller.StateRunning, 10*time.Second)
	}
	return dev, cds, nodes
}

func waitAck(t *testing.T, cd *controller.Device, want rt.DataStatus) {
	t.Helper()
	eventually(t, 5*time.Second, "redundancy acknowledge "+want.String(), func() bool {
		s, ok := cd.RedundancyAck()
		return ok && s&rt.RedundancyMask == want
	})
}

// TestSystemRedundancy: an S2 AR set (experimental). Both IOCARSRs run;
// the outputs come from the primary, the inputs reach both; the backup is
// told it is one and cannot write; a switchover hands the outputs to the
// other AR; when the primary fails, the backup takes over within the
// redundancy data hold time.
func TestSystemRedundancy(t *testing.T) {
	e := newEnv(t, 320)
	dev, cds, nodes := srPair(t, e, 2000)
	if l := lockedSlots(cds[1]); len(l) != 0 {
		t.Fatalf("the backup sees locks: %v", l)
	}
	_ = cds[0].SetOutput(2, 1, []byte{0x11, 0x11, 0x11, 0x11}, pnio.IOxSGood)
	_ = cds[1].SetOutput(2, 1, []byte{0x22, 0x22, 0x22, 0x22}, pnio.IOxSGood)
	waitOutput(t, dev, 2, []byte{0x11, 0x11, 0x11, 0x11})
	_ = dev.SetInput(1, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8}, pnio.IOxSGood)
	waitInput(t, cds[0], 1, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	waitInput(t, cds[1], 1, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	waitAck(t, cds[0], rt.PrimaryAck)
	waitAck(t, cds[1], rt.BackupAckWithData)
	if st := dev.SystemRedundancy(); len(st) != 1 || len(st[0].ARs) != 2 || st[0].Primary != 1 {
		t.Fatalf("sets %+v", st)
	}
	// The backup may not write.
	var se *pnio.StatusError
	if err := cds[1].WriteRecord(context.Background(), controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 3}); !errors.As(err, &se) || se.Status.Code1 != 0xCC {
		t.Fatalf("the backup's write: %v", err)
	}
	if err := cds[0].WriteRecord(context.Background(), controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 3}); err != nil {
		t.Fatalf("the primary's write: %v", err)
	}

	// Switchover: AR 2 primary.
	start := time.Now()
	_ = cds[0].SetPrimary(false)
	_ = cds[1].SetPrimary(true)
	waitOutput(t, dev, 2, []byte{0x22, 0x22, 0x22, 0x22})
	t.Logf("switchover: outputs of AR 2 after %s (vnet; measured, not claimed)", time.Since(start))
	waitAck(t, cds[1], rt.PrimaryAck)
	waitAck(t, cds[0], rt.BackupAckWithData)

	// The primary (controller 2) fails: its link goes. The device holds
	// the outputs; controller 1 takes over before RDHT runs out.
	start = time.Now()
	nodes[1].ep.SetLinkUp(false)
	eventually(t, 10*time.Second, "no primary", func() bool {
		st := dev.SystemRedundancy()
		return len(st) == 1 && st[0].Primary == 0
	})
	if v, iops, _ := dev.Output(2, 1); iops != pnio.IOxSGood || v[0] != 0x22 {
		t.Fatalf("outputs not held: % x %#x", v, iops)
	}
	_ = cds[0].SetOutput(2, 1, []byte{0x33, 0x33, 0x33, 0x33}, pnio.IOxSGood)
	_ = cds[0].SetPrimary(true)
	waitOutput(t, dev, 2, []byte{0x33, 0x33, 0x33, 0x33})
	t.Logf("failover: outputs of AR 1 %s after the primary's link went, with a data hold of %d cycles (vnet; measured, not claimed)", time.Since(start), srHold)
	if st, _ := cds[0].State(); st != controller.StateRunning {
		t.Fatalf("AR 1 %s", st)
	}
}

// TestSystemRedundancyRDHT: without a primary for the redundancy data
// hold time the device aborts the set with reason 38.
func TestSystemRedundancyRDHT(t *testing.T) {
	e := newEnv(t, 321)
	dev, cds, _ := srPair(t, e, 300)
	_ = cds[0].SetOutput(2, 1, []byte{0x11, 0x11, 0x11, 0x11}, pnio.IOxSGood)
	waitOutput(t, dev, 2, []byte{0x11, 0x11, 0x11, 0x11})
	// The primary steps back; nobody steps up.
	_ = cds[0].SetPrimary(false)
	waitAck(t, cds[1], rt.BackupAckWithData|rt.Redundancy)
	for _, cd := range cds {
		eventually(t, 5*time.Second, "abort with RDHT expired", func() bool {
			st, err := cd.State()
			return st != controller.StateRunning && err != nil
		})
		// The device aborts with reason 38 (0x26).
		if _, err := cd.State(); !strings.Contains(err.Error(), "FD 26") || !strings.Contains(err.Error(), pnio.AbortSRRDHTExpired.String()) {
			t.Fatalf("%s: %v", cd.Name(), err)
		}
	}
}

// TestSystemRedundancyGate: without the experimental option an IOCARSR is
// refused, as before.
func TestSystemRedundancyGate(t *testing.T) {
	e := newEnv(t, 322)
	e.device("sr-off", "192.168.0.10", testModel(), nil)
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "sr-off", Model: testModel(), ReconnectMin: time.Hour,
		SystemRedundancy: &controller.SRSetting{ARNumber: 1, Primary: true}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "refused", func() bool { _, err := cd.State(); return err != nil })
	_, err = cd.State()
	var se *pnio.StatusError
	if !errors.As(err, &se) || se.Status.Code1 != pnio.Code1ARBlock {
		t.Fatalf("%v", err)
	}
	if _, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "x", Model: testModel(), SystemRedundancy: &controller.SRSetting{ARNumber: 3}}); err == nil {
		t.Fatal("AR number 3 accepted")
	}
	if err := cd.SetPrimary(true); err != nil {
		t.Fatal(err)
	}
}
