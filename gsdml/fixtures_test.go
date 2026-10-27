// SPDX-License-Identifier: MIT

package gsdml

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

// The synthetic fixtures (testdata/fixtures, MIT): what each device access
// point must become.
var fixtureDAPs = map[string][]struct {
	dap      string
	ports    int
	slots    []uint16
	records  int
	inputs   uint16 // octets of input data
	outputs  uint16
	shared   bool
	deviceAR bool
}{
	"GSDML-V2.43-OTFabric-ModularFixture-20261027.xml": {
		{dap: "DAP_1P", ports: 1, slots: []uint16{1, 2, 3}, records: 3, inputs: 2 + 8, outputs: 2},
		{dap: "DAP_2P", ports: 2, slots: []uint16{1, 2, 3, 4}, records: 3, inputs: 2 + 8 + 1, outputs: 2 + 1, shared: true, deviceAR: true},
	},
	"GSDML-V2.35-OTFabric-CompactFixture-20261027.xml": {
		{dap: "DAP_C", ports: 1, slots: []uint16{1, 2, 3}, records: 1, inputs: 5 + 2 + 4, outputs: 1 + 6},
	},
}

// TestFixtures: every fixture parses, passes Check without an issue,
// round-trips, and each device access point becomes the model it
// describes.
func TestFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/fixtures/GSDML-*.xml")
	if err != nil || len(files) != len(fixtureDAPs) {
		t.Fatalf("fixtures %v %v", files, err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := ParseBytes(b)
			if err != nil {
				t.Fatal(err)
			}
			if r := Check(doc); len(r.Issues) != 0 {
				t.Fatalf("issues: %+v", r.Issues)
			}
			out, err := Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			doc2, err := ParseBytes(out)
			if err != nil {
				t.Fatal(err)
			}
			out2, err := Marshal(doc2)
			if err != nil || !bytes.Equal(out, out2) {
				t.Fatalf("round trip differs: %v", err)
			}
			want := fixtureDAPs[filepath.Base(f)]
			if len(doc.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems) != len(want) {
				t.Fatalf("DAPs")
			}
			for _, w := range want {
				m, err := ToModel(doc, w.dap)
				if err != nil {
					t.Fatalf("%s: %v", w.dap, err)
				}
				if err := m.Validate(); err != nil {
					t.Fatalf("%s: %v", w.dap, err)
				}
				var slots []uint16
				var in, out uint16
				records := 0
				m.Each(func(p model.PluggedSubmodule) {
					if p.Slot != 0 && (len(slots) == 0 || slots[len(slots)-1] != p.Slot) {
						slots = append(slots, p.Slot)
					}
					in += p.Submodule.InputLen
					out += p.Submodule.OutputLen
					records += len(p.Submodule.Records)
				})
				if m.Capabilities.Ports != w.ports || len(slots) != len(w.slots) || in != w.inputs || out != w.outputs || records != w.records {
					t.Fatalf("%s: ports %d slots %v in %d out %d records %d", w.dap, m.Capabilities.Ports, slots, in, out, records)
				}
				for i := range slots {
					if slots[i] != w.slots[i] {
						t.Fatalf("%s: slots %v", w.dap, slots)
					}
				}
				var d *DeviceAccessPointItem
				for _, x := range doc.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems {
					if x.ID == w.dap {
						d = x
					}
				}
				if ParseBool(d.SharedDeviceSupported, false) != w.shared || ParseBool(d.DeviceAccessSupported, false) != w.deviceAR {
					t.Fatalf("%s: shared %q device access %q", w.dap, d.SharedDeviceSupported, d.DeviceAccessSupported)
				}
			}
		})
	}
}

// TestFixtureRecords: the parameter records take the defaults of their
// Refs, at their offsets, with the Const octets.
func TestFixtureRecords(t *testing.T) {
	b, err := os.ReadFile("testdata/fixtures/GSDML-V2.43-OTFabric-ModularFixture-20261027.xml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ToModel(doc, "DAP_2P")
	if err != nil {
		t.Fatal(err)
	}
	want := map[pnio.Index][]byte{
		100: {3, 1},              // filter 3, wire break diagnosis on (bit 0)
		101: {0},                 // substitute value 0
		102: {1, 2, 0, 50, 0, 0}, // const 1, range 2, filter 50, offset 0
	}
	got := map[pnio.Index][]byte{}
	m.Each(func(p model.PluggedSubmodule) {
		for _, r := range p.Submodule.Records {
			got[r.Index] = r.Default
		}
	})
	for idx, w := range want {
		if !bytes.Equal(got[idx], w) {
			t.Errorf("record %d: % x, want % x", idx, got[idx], w)
		}
	}
}

// TestFixtureEvidence: the evidence file names every fixture and records
// no outcome without a tool version and a date.
func TestFixtureEvidence(t *testing.T) {
	b, err := os.ReadFile("testdata/fixtures/evidence.json")
	if err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Fixtures []struct {
			File string   `json:"file"`
			DAPs []string `json:"daps"`
		} `json:"fixtures"`
		Imports []struct {
			Tool, Version, Date, Fixture, Outcome string
		} `json:"imports"`
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatal(err)
	}
	if len(ev.Fixtures) != len(fixtureDAPs) {
		t.Fatalf("fixtures %+v", ev.Fixtures)
	}
	for _, f := range ev.Fixtures {
		if _, ok := fixtureDAPs[f.File]; !ok {
			t.Errorf("unknown fixture %s", f.File)
		}
		if _, err := os.Stat(filepath.Join("testdata/fixtures", f.File)); err != nil {
			t.Error(err)
		}
	}
	for _, im := range ev.Imports {
		switch im.Outcome {
		case "not-run":
		case "imported", "imported-with-warnings", "rejected":
			if im.Version == "" || im.Date == "" {
				t.Errorf("%s: an outcome without version and date", im.Tool)
			}
		default:
			t.Errorf("%s: outcome %q", im.Tool, im.Outcome)
		}
	}
}
