// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/pnio"
)

// TestControllerDevice establishes an AR between a virtual controller and a
// virtual device and exchanges cyclic data both ways.
func TestControllerDevice(t *testing.T) {
	e := newEnv(t, 1)
	dev, _ := e.device("dut", "192.168.0.10", testModel(), nil)
	ctrl, _ := e.controller("192.168.0.1")
	cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "dut", Model: testModel(),
		Parameters: []controller.Parameter{{Slot: 2, Subslot: 1, Index: 0x0010, Data: []byte{0, 7}}}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cd, controller.StateRunning, 10*time.Second)

	if err := dev.SetInput(1, 1, []byte{1, 2, 3, 4, 5, 6, 7, 8}, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	if err := cd.SetOutput(2, 1, []byte{9, 8, 7, 6}, pnio.IOxSGood); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "inputs at the controller", func() bool {
		in, iops, _ := cd.Input(1, 1)
		return bytes.Equal(in, []byte{1, 2, 3, 4, 5, 6, 7, 8}) && iops == pnio.IOxSGood
	})
	eventually(t, 2*time.Second, "outputs at the device", func() bool {
		out, iops, _ := dev.Output(2, 1)
		return bytes.Equal(out, []byte{9, 8, 7, 6}) && iops == pnio.IOxSGood
	})
	ctx := context.Background()
	v, err := cd.ReadRecord(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x0010}, 0)
	if err != nil || !bytes.Equal(v, []byte{0, 7}) {
		t.Fatalf("parameter record %x %v", v, err)
	}
	im0, err := cd.ReadIM0(ctx)
	if err != nil || im0.OrderID != "OTF-E2E-1" || im0.VendorID != 0xF000 {
		t.Fatalf("I&M0 %+v %v", im0, err)
	}
	ars := dev.ARs()
	if len(ars) != 1 || ars[0].ControllerName != "plc" {
		t.Fatalf("ARs %+v", ars)
	}
	if err := cd.Close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "release at the device", func() bool { return len(dev.ARs()) == 0 })
}
