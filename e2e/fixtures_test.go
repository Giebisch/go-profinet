// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/gsdml"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

// fixtureModel reads a device access point of a synthetic GSDML fixture.
func fixtureModel(t *testing.T, file, dap string) *model.Device {
	t.Helper()
	b, err := os.ReadFile("../gsdml/testdata/fixtures/" + file)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := gsdml.ParseBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	m, err := gsdml.ToModel(doc, dap)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// exerciseFixture runs an AR with a fixture's model: parameters other than
// the defaults, every output and input, I&M0, a record read back.
func exerciseFixture(t *testing.T, cd *controller.Device, m *model.Device, setInput func(slot, subslot uint16, data []byte)) {
	t.Helper()
	ctx := context.Background()
	if im0, err := cd.ReadIM0(ctx); err != nil || im0.VendorID != m.Identity.VendorID || im0.OrderID != m.Identity.OrderID {
		t.Fatalf("I&M0 %+v %v", im0, err)
	}
	m.Each(func(p model.PluggedSubmodule) {
		s := p.Submodule
		if s.InputLen > 0 {
			in := bytes.Repeat([]byte{byte(p.Slot)}, int(s.InputLen))
			setInput(p.Slot, s.Subslot, in)
			waitInputSub(t, cd, p.Slot, s.Subslot, in)
		}
		if s.OutputLen > 0 {
			if err := cd.SetOutput(p.Slot, s.Subslot, bytes.Repeat([]byte{0xA0 | byte(p.Slot)}, int(s.OutputLen)), pnio.IOxSGood); err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range s.Records {
			b, err := cd.ReadRecord(ctx, controller.Record{Slot: p.Slot, Subslot: s.Subslot, Index: r.Index}, 0)
			if err != nil || len(b) != int(r.Length) {
				t.Fatalf("record %d/%d %s: % x %v", p.Slot, s.Subslot, r.Index, b, err)
			}
		}
	})
}

func waitInputSub(t *testing.T, cd *controller.Device, slot, subslot uint16, want []byte) {
	t.Helper()
	eventually(t, 5*time.Second, "input", func() bool {
		in, iops, ok := cd.Input(slot, subslot)
		return ok && iops == pnio.IOxSGood && bytes.Equal(in, want)
	})
}

// TestFixtureDevices: a go-profinet device and controller configured from
// each device access point of the synthetic GSDML fixtures connect, set
// parameters and exchange data; the two-port DAP runs as a multiport
// device shared by two controllers (its SharedDeviceSupported).
func TestFixtureDevices(t *testing.T) {
	for _, c := range []struct{ file, dap string }{
		{"GSDML-V2.43-OTFabric-ModularFixture-20261027.xml", "DAP_1P"},
		{"GSDML-V2.35-OTFabric-CompactFixture-20261027.xml", "DAP_C"},
	} {
		t.Run(c.dap, func(t *testing.T) {
			e := newEnv(t, 41)
			m := fixtureModel(t, c.file, c.dap)
			dev, _ := e.device("fixture", "192.168.0.10", m, nil)
			ctrl, _ := e.controller("192.168.0.1")
			// A parameter other than its default: the first record's
			// first octet + 1.
			var params []controller.Parameter
			m.Each(func(p model.PluggedSubmodule) {
				for _, r := range p.Submodule.Records {
					v := append([]byte(nil), r.Default...)
					v[0]++
					params = append(params, controller.Parameter{Slot: p.Slot, Subslot: p.Submodule.Subslot, Index: r.Index, Data: v})
				}
			})
			cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "fixture", Model: fixtureModel(t, c.file, c.dap), Parameters: params})
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, cd, controller.StateRunning, 10*time.Second)
			// The ModuleDiff, when the device sends one, has every module
			// proper.
			if diff := cd.ModuleDiff(); diff != nil {
				for _, api := range diff.APIs {
					for _, mod := range api.Modules {
						if mod.State != pnio.ModuleStateProper {
							t.Fatalf("module diff %+v", diff)
						}
					}
				}
			}
			exerciseFixture(t, cd, m, func(slot, subslot uint16, data []byte) { _ = dev.SetInput(slot, subslot, data, pnio.IOxSGood) })
			for _, p := range params {
				b, err := cd.ReadRecord(context.Background(), controller.Record{Slot: p.Slot, Subslot: p.Subslot, Index: p.Index}, 0)
				if err != nil || !bytes.Equal(b, p.Data) {
					t.Fatalf("parameter %d/%d %s: % x %v", p.Slot, p.Subslot, p.Index, b, err)
				}
			}
		})
	}
	t.Run("DAP_2P", func(t *testing.T) {
		e := newEnv(t, 42)
		const file = "GSDML-V2.43-OTFabric-ModularFixture-20261027.xml"
		m := fixtureModel(t, file, "DAP_2P")
		segs := e.segments(2)
		dev := e.multiportDeviceModel("fixture-2p", "192.168.0.10", m, nil, segs[0], segs[1])
		// Two controllers on the two ports, each with a part of the
		// modules (shared device).
		var cds []*controller.Device
		for i, part := range [][]uint16{{1, 2}, {3, 4}} {
			ctrl, _ := e.controllerOn(segs[i], []string{"192.168.0.1", "192.168.0.2"}[i])
			cd, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "fixture-2p", Model: fixtureModel(t, file, "DAP_2P").WithSlots(part...)})
			if err != nil {
				t.Fatal(err)
			}
			cds = append(cds, cd)
		}
		for _, cd := range cds {
			waitState(t, cd, controller.StateRunning, 15*time.Second)
		}
		for i, part := range [][]uint16{{1, 2}, {3, 4}} {
			exerciseFixture(t, cds[i], m.WithSlots(part...), func(slot, subslot uint16, data []byte) {
				_ = dev.dev.SetInput(slot, subslot, data, pnio.IOxSGood)
			})
		}
		if ars := dev.dev.ARs(); len(ars) != 2 {
			t.Fatalf("ARs %+v", ars)
		}
	})
}
