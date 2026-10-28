// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/mrp"
	"github.com/otfabric/go-profinet/pnio"
)

// ringState reads PDInterfaceMrpDataReal through the device's AR.
func ringState(t *testing.T, cd *controller.Device) (pnio.PDInterfaceMrpDataReal, bool) {
	t.Helper()
	b, err := cd.ReadRecord(context.Background(), controller.Record{Slot: 0, Subslot: model.SubslotInterface, Index: pnio.IndexPDInterfaceMrpDataReal}, 0)
	if err != nil {
		t.Logf("PDInterfaceMrpDataReal: %v", err)
		return pnio.PDInterfaceMrpDataReal{}, false
	}
	blk, _, err := pnio.ReadBlock(b)
	if err != nil {
		t.Fatal(err)
	}
	r, err := pnio.DecodePDInterfaceMrpDataReal(blk)
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

// TestMRPRing: three devices in a ring, A the manager (its third port
// leads to the controller), B and C clients, roles written by the
// controller. The ring closes (A blocks its secondary port); a cable
// between B and C breaks: the ring opens and C is reached the other way
// round, its AR running on; the cable back: the ring closes again.
func TestMRPRing(t *testing.T) {
	e := newEnv(t, 310)
	segs := e.segments(4)
	// A: p1-B, p2-C, p3-controller. B: p1-A, p2-C. C: p1-B, p2-A.
	// A ring needs its manager from the start (a ring of clients alone
	// forwards around): A is commissioned as the manager; the controller
	// writes the same roles again.
	cl := &device.MRPConfig{Role: mrp.RoleClient, Profile: mrp.Profile200}
	a := e.multiportDeviceMRP("ring-a", "192.168.0.10", &device.MRPConfig{Role: mrp.RoleManager, Profile: mrp.Profile200}, segs[0], segs[2], segs[3])
	b := e.multiportDeviceMRP("ring-b", "192.168.0.11", cl, segs[0], segs[1])
	c := e.multiportDeviceMRP("ring-c", "192.168.0.12", cl, segs[1], segs[2])
	ctrl, _ := e.controllerOn(segs[3], "192.168.0.1")
	add := func(name string, ports int, role mrp.Role) *controller.Device {
		cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: name, Model: multiportModel(ports),
			MRP:          &controller.MRPSetting{Role: role},
			ReconnectMin: 100 * time.Millisecond, ReconnectMax: 300 * time.Millisecond, DataHoldFactor: stallHold, WatchdogFactor: stallHold})
		if err != nil {
			t.Fatal(err)
		}
		return cd
	}
	cdA := add("ring-a", 3, mrp.RoleManager)
	waitState(t, cdA, controller.StateRunning, 15*time.Second)
	cdB := add("ring-b", 2, mrp.RoleClient)
	cdC := add("ring-c", 2, mrp.RoleClient)
	waitState(t, cdB, controller.StateRunning, 15*time.Second)
	waitState(t, cdC, controller.StateRunning, 15*time.Second)

	eventually(t, 5*time.Second, "ring closed", func() bool {
		st, ok := a.dev.MRPStatus()
		return ok && st.Role == mrp.RoleManager && !st.RingOpen && st.Blocked[1]
	})
	if r, ok := ringState(t, cdA); !ok || r.Role != pnio.MrpRoleManager || !r.HasRingState || r.RingState != uint16(mrp.RingClosed) {
		t.Fatalf("A's PDInterfaceMrpDataReal %+v", r)
	}
	if r, ok := ringState(t, cdC); !ok || r.Role != pnio.MrpRoleClient || r.HasRingState {
		t.Fatalf("C's PDInterfaceMrpDataReal %+v", r)
	}
	_ = c.dev.SetInput(1, 1, []byte{1, 1, 2, 2, 3, 3, 4, 4}, pnio.IOxSGood)
	waitInput(t, cdC, 1, []byte{1, 1, 2, 2, 3, 3, 4, 4})

	// The cable B-C breaks.
	start := time.Now()
	b.ports[1].SetLinkUp(false)
	c.ports[0].SetLinkUp(false)
	eventually(t, 5*time.Second, "ring open", func() bool {
		st, _ := a.dev.MRPStatus()
		return st.RingOpen && !st.Blocked[1] && st.Transitions >= 1
	})
	_ = c.dev.SetInput(1, 1, []byte{5, 5, 6, 6, 7, 7, 8, 8}, pnio.IOxSGood)
	waitInput(t, cdC, 1, []byte{5, 5, 6, 6, 7, 7, 8, 8})
	t.Logf("C's inputs again %s after the cable break (vnet, profile %s; measured, not claimed)", time.Since(start), mrp.Profile200.Name)
	if st, _ := cdC.State(); st != controller.StateRunning {
		t.Fatalf("C %s", st)
	}
	if r, ok := ringState(t, cdA); !ok || r.RingState != uint16(mrp.RingOpen) {
		t.Fatalf("A reports %+v", r)
	}

	// The cable back.
	b.ports[1].SetLinkUp(true)
	c.ports[0].SetLinkUp(true)
	eventually(t, 5*time.Second, "ring closed again", func() bool {
		st, _ := a.dev.MRPStatus()
		return !st.RingOpen && st.Blocked[1]
	})
	_ = c.dev.SetInput(1, 1, []byte{9, 9, 9, 9, 9, 9, 9, 9}, pnio.IOxSGood)
	waitInput(t, cdC, 1, []byte{9, 9, 9, 9, 9, 9, 9, 9})
	for _, cd := range []*controller.Device{cdA, cdB, cdC} {
		if st, _ := cd.State(); st != controller.StateRunning {
			t.Fatalf("%s %s", cd.Name(), st)
		}
	}
}

// TestMRPRecords: the MRP records of a device without MRP are refused; a
// check is refused; a port adjust of another domain is refused.
func TestMRPRecords(t *testing.T) {
	e := newEnv(t, 311)
	segs := e.segments(3)
	d := e.multiportDeviceMRP("mrp-r", "192.168.0.10", &device.MRPConfig{Role: mrp.RoleClient}, segs[0], segs[1], segs[2])
	_ = d
	ctrl, _ := e.controllerOn(segs[2], "192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "mrp-r", Model: multiportModel(3), DataHoldFactor: stallHold, WatchdogFactor: stallHold})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)
	ctx := context.Background()
	chk := pnio.PDInterfaceMrpDataCheck{Domain: mrp.DefaultDomain, Check: pnio.MrpCheckManager}
	if err := cd.WriteRecord(ctx, controller.Record{Subslot: model.SubslotInterface, Index: pnio.IndexPDInterfaceMrpDataCheck}, chk.Append(nil)); err == nil {
		t.Fatal("check accepted")
	}
	pa := pnio.PDPortMrpData{Domain: [16]byte{1}}
	if err := cd.WriteRecord(ctx, controller.Record{Subslot: model.SubslotPort1, Index: pnio.IndexPDPortMrpDataAdjust}, pa.Append(nil)); err == nil {
		t.Fatal("another domain accepted")
	}
	pa.Domain = mrp.DefaultDomain
	if err := cd.WriteRecord(ctx, controller.Record{Subslot: model.SubslotPort1 + 2, Index: pnio.IndexPDPortMrpDataAdjust}, pa.Append(nil)); err == nil {
		t.Fatal("a port off the ring accepted")
	}
	if err := cd.WriteRecord(ctx, controller.Record{Subslot: model.SubslotPort1, Index: pnio.IndexPDPortMrpDataAdjust}, pa.Append(nil)); err != nil {
		t.Fatal(err)
	}
	if b, err := cd.ReadRecord(ctx, controller.Record{Subslot: model.SubslotPort1, Index: pnio.IndexPDPortMrpDataReal}, 0); err != nil || len(b) == 0 {
		t.Fatalf("port real % x %v", b, err)
	}
	if b, err := cd.ReadRecord(ctx, controller.Record{Subslot: model.SubslotPort1 + 2, Index: pnio.IndexPDPortMrpDataReal}, 0); err != nil || len(b) != 0 {
		t.Fatalf("port real of a port off the ring % x %v", b, err)
	}
	// Off: both ring ports forward.
	off := pnio.PDInterfaceMrpDataAdjust{Domain: mrp.DefaultDomain, Role: pnio.MrpRoleOff, DomainName: "mrpdomain-1"}
	if err := cd.WriteRecord(ctx, controller.Record{Subslot: model.SubslotInterface, Index: pnio.IndexPDInterfaceMrpDataAdjust}, off.Append(nil)); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.dev.MRPStatus(); ok {
		t.Fatal("MRP still running")
	}
	if r, ok := ringState(t, cd); !ok || r.Role != pnio.MrpRoleOff {
		t.Fatalf("%+v", r)
	}

	// A device without MRP refuses the records.
	e2 := newEnv(t, 312)
	dev, _ := e2.device("plain", "192.168.0.20", testModel(), nil)
	_ = dev
	ctrl2, _ := e2.controller("192.168.0.2")
	cd2, _ := ctrl2.AddDevice(controller.DeviceConfig{StationName: "plain", Model: testModel()})
	waitState(t, cd2, controller.StateRunning, 10*time.Second)
	if _, err := cd2.ReadRecord(ctx, controller.Record{Subslot: model.SubslotInterface, Index: pnio.IndexPDInterfaceMrpDataReal}, 0); err == nil {
		t.Fatal("a device without MRP served PDInterfaceMrpDataReal")
	}
}

// TestMRPAutoRing: three automatic managers (MRA, a role of the devices'
// configuration: PROFINET's MRP_Role has none) settle on A, the best
// priority, as the manager; A's PDInterfaceMrpDataReal reports what it
// acts as, the manager of a closed ring.
func TestMRPAutoRing(t *testing.T) {
	e := newEnv(t, 330)
	segs := e.segments(4)
	auto := &device.MRPConfig{Role: mrp.RoleAuto, Profile: mrp.Profile200}
	a := e.multiportDeviceMRP("auto-a", "192.168.0.10", &device.MRPConfig{Role: mrp.RoleAuto, Prio: 0x9000, Profile: mrp.Profile200}, segs[0], segs[2], segs[3])
	b := e.multiportDeviceMRP("auto-b", "192.168.0.11", auto, segs[0], segs[1])
	c := e.multiportDeviceMRP("auto-c", "192.168.0.12", auto, segs[1], segs[2])
	ctrl, _ := e.controllerOn(segs[3], "192.168.0.1")
	cdA, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "auto-a", Model: multiportModel(3),
		ReconnectMin: 100 * time.Millisecond, ReconnectMax: 300 * time.Millisecond, DataHoldFactor: stallHold, WatchdogFactor: stallHold})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cdA, controller.StateRunning, 15*time.Second)
	eventually(t, 5*time.Second, "A manages a closed ring, B and C its clients", func() bool {
		sa, _ := a.dev.MRPStatus()
		sb, _ := b.dev.MRPStatus()
		sc, _ := c.dev.MRPStatus()
		return sa.Role == mrp.RoleAuto && sa.Acting == mrp.RoleManager && !sa.RingOpen && sa.Blocked[1] &&
			sb.Acting == mrp.RoleClient && sb.ManagerPrio == 0x9000 && sc.Acting == mrp.RoleClient && sc.ManagerPrio == 0x9000
	})
	if r, ok := ringState(t, cdA); !ok || r.Role != pnio.MrpRoleManager || !r.HasRingState || r.RingState != uint16(mrp.RingClosed) {
		t.Fatalf("A's PDInterfaceMrpDataReal %+v", r)
	}
}
