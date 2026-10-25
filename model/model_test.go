// SPDX-License-Identifier: MIT

package model

import (
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/pnio"
)

func validDevice() *Device {
	return &Device{
		Identity: Identity{
			VendorID: 0xF000, DeviceID: 0x0042, VendorName: "OT Fabric", TypeOfStation: "model test",
			OrderID: "OTF-1", SerialNumber: "0001",
		},
		Capabilities: Capabilities{MaxARs: 1, MinDeviceInterval: 32, ConformanceClass: "A", Ports: 1},
		DAP:          StandardDAP("DAP", 0x100, 1),
		Slots: []Slot{
			{Number: 3, Module: Module{Name: "IO", IdentNumber: 0x30, Submodules: []Submodule{
				{Name: "b", IdentNumber: 0x32, Subslot: 2, InputLen: 1, OutputLen: 1},
				{Name: "a", IdentNumber: 0x31, Subslot: 1, InputLen: 2},
			}}},
			{Number: 1, Module: Module{Name: "DI", IdentNumber: 0x10, Submodules: []Submodule{
				{Name: "DI", IdentNumber: 0x11, Subslot: 1, InputLen: 8, Records: []Record{
					{Index: 0x10, Length: 2, Default: []byte{1, 2}},
					{Index: 0x11, Length: 4},
				}},
			}}},
			{Number: 2, Module: Module{Name: "DO", IdentNumber: 0x20, Submodules: []Submodule{
				{Name: "DO", IdentNumber: 0x21, Subslot: 1, OutputLen: 4},
			}}},
		},
		Catalog: []Module{
			{Name: "DO-dup", IdentNumber: 0x20},
			{Name: "Spare", IdentNumber: 0x40},
			{Name: "Spare-dup", IdentNumber: 0x40},
		},
	}
}

func TestValidateOK(t *testing.T) {
	if err := validDevice().Validate(); err != nil {
		t.Fatal(err)
	}
	// A record without a default is fine, and a NO_IO module too.
	d := validDevice()
	d.Slots = append(d.Slots, Slot{Number: 9, Module: Module{IdentNumber: 9, Submodules: []Submodule{{IdentNumber: 9, Subslot: 1}}}})
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	// Only one of vendor and device ID is enough.
	d = validDevice()
	d.Identity.VendorID = 0
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateErrors(t *testing.T) {
	big := func(n uint16) Submodule { return Submodule{IdentNumber: 1, Subslot: 1, InputLen: n} }
	cases := []struct {
		name string
		mod  func(d *Device)
		want string
	}{
		{"zero IDs", func(d *Device) { d.Identity.VendorID, d.Identity.DeviceID = 0, 0 }, "vendor and device ID"},
		{"long order ID", func(d *Device) { d.Identity.OrderID = strings.Repeat("x", pnio.IMOrderIDLen+1) }, "order ID"},
		{"long serial", func(d *Device) { d.Identity.SerialNumber = strings.Repeat("x", pnio.IMSerialLen+1) }, "serial number"},
		{"DAP without subslot 1", func(d *Device) { d.DAP.Submodules = d.DAP.Submodules[1:] }, "subslot 1"},
		{"slot 0", func(d *Device) { d.Slots[0].Number = 0 }, "slot 0 is the DAP"},
		{"duplicate slot", func(d *Device) { d.Slots[1].Number = 3 }, "slot 3 used twice"},
		{"subslot 0", func(d *Device) { d.Slots[0].Module.Submodules[0].Subslot = 0 }, "subslot 0 is not allowed"},
		{"duplicate subslot", func(d *Device) { d.Slots[0].Module.Submodules[0].Subslot = 1 }, "subslot 1 used twice"},
		{"duplicate DAP subslot", func(d *Device) { d.DAP.Submodules[1].Subslot = SubslotDAP }, "slot 0: subslot 1 used twice"},
		{"input too long", func(d *Device) { d.Slots[0].Module.Submodules[0].InputLen = 1440 }, "longer than the C_SDU"},
		{"output too long", func(d *Device) { d.Slots[0].Module.Submodules[0].OutputLen = 1440 }, "longer than the C_SDU"},
		{"record index", func(d *Device) { d.Slots[1].Module.Submodules[0].Records[0].Index = 0x8000 }, "outside 0..0x7FFF"},
		{"duplicate record", func(d *Device) { d.Slots[1].Module.Submodules[0].Records[1].Index = 0x10 }, "twice"},
		{"default length", func(d *Device) { d.Slots[1].Module.Submodules[0].Records[0].Default = []byte{1} }, "default of 1 octets, length 2"},
		{"too much IO", func(d *Device) {
			d.Slots = []Slot{
				{Number: 1, Module: Module{IdentNumber: 1, Submodules: []Submodule{big(1000)}}},
				{Number: 2, Module: Module{IdentNumber: 1, Submodules: []Submodule{big(1000)}}},
				{Number: 3, Module: Module{IdentNumber: 1, Submodules: []Submodule{big(1000)}}},
			}
		}, "input CR of"},
		{"too much output", func(d *Device) {
			d.Slots = []Slot{
				{Number: 1, Module: Module{IdentNumber: 1, Submodules: []Submodule{{IdentNumber: 1, Subslot: 1, OutputLen: 1000}}}},
				{Number: 2, Module: Module{IdentNumber: 1, Submodules: []Submodule{{IdentNumber: 1, Subslot: 1, OutputLen: 1000}}}},
			}
		}, "output CR of"},
		{"negative MaxARs", func(d *Device) { d.Capabilities.MaxARs = -1 }, "MaxARs -1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDevice()
			c.mod(d)
			err := d.Validate()
			if !errors.Is(err, profinet.ErrInvalidConfig) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestSubmoduleType(t *testing.T) {
	cases := []struct {
		in, out uint16
		want    pnio.SubmoduleType
	}{
		{0, 0, pnio.SubmoduleNoIO},
		{1, 0, pnio.SubmoduleInput},
		{0, 1, pnio.SubmoduleOutput},
		{1, 1, pnio.SubmoduleInputOutput},
	}
	for _, c := range cases {
		s := Submodule{InputLen: c.in, OutputLen: c.out}
		if got := s.Type(); got != c.want {
			t.Errorf("%d/%d: %v, want %v", c.in, c.out, got, c.want)
		}
	}
}

func TestLookups(t *testing.T) {
	d := validDevice()
	s, ok := d.Submodule(1, 1)
	if !ok || s.IdentNumber != 0x11 {
		t.Fatalf("submodule 1/1: %+v %v", s, ok)
	}
	r, ok := s.Record(0x11)
	if !ok || r.Length != 4 {
		t.Fatalf("record %+v %v", r, ok)
	}
	// The returned record is the stored one.
	r.Name = "changed"
	if s.Records[1].Name != "changed" {
		t.Fatal("Record returned a copy")
	}
	if _, ok := s.Record(0x12); ok {
		t.Fatal("record 0x12 found")
	}
	if m, ok := d.Module(0); !ok || m != &d.DAP {
		t.Fatal("slot 0 is not the DAP")
	}
	if m, ok := d.Module(2); !ok || m.IdentNumber != 0x20 {
		t.Fatalf("module 2 %+v %v", m, ok)
	}
	if _, ok := d.Module(7); ok {
		t.Fatal("module 7 found")
	}
	if _, ok := d.Submodule(7, 1); ok {
		t.Fatal("submodule 7/1 found")
	}
	if _, ok := d.Submodule(1, 9); ok {
		t.Fatal("submodule 1/9 found")
	}
	if sm, ok := d.Submodule(0, SubslotInterface); !ok || sm.Name != "Interface" {
		t.Fatalf("interface %+v %v", sm, ok)
	}
}

func TestEachOrder(t *testing.T) {
	d := validDevice()
	type pos struct {
		slot, subslot uint16
		mod           uint32
	}
	var got []pos
	d.Each(func(p PluggedSubmodule) {
		got = append(got, pos{p.Slot, p.Submodule.Subslot, p.ModuleIdent})
		// The submodule points into the model, not into a copy.
		if sm, _ := d.Submodule(p.Slot, p.Submodule.Subslot); sm != p.Submodule {
			t.Errorf("%d/%d is a copy", p.Slot, p.Submodule.Subslot)
		}
	})
	want := []pos{
		{0, SubslotDAP, 0x100}, {0, SubslotInterface, 0x100}, {0, SubslotPort1, 0x100},
		{1, 1, 0x10}, {2, 1, 0x20}, {3, 1, 0x30}, {3, 2, 0x30},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// The model's own order is unchanged.
	if d.Slots[0].Number != 3 || d.Slots[0].Module.Submodules[0].Subslot != 2 {
		t.Fatal("Each reordered the model")
	}
}

func TestModuleTypes(t *testing.T) {
	d := validDevice()
	count := map[uint32]int{}
	var order []uint32
	for _, m := range d.ModuleTypes() {
		count[m.IdentNumber]++
		order = append(order, m.IdentNumber)
	}
	for _, id := range []uint32{0x10, 0x20, 0x30, 0x40} {
		if count[id] != 1 {
			t.Fatalf("module 0x%X %d times in %v", id, count[id], order)
		}
	}
	// The plugged module wins over the catalog entry with its ident.
	for _, m := range d.ModuleTypes() {
		if m.IdentNumber == 0x20 && m.Name != "DO" {
			t.Fatalf("module 0x20 is %q", m.Name)
		}
		if m.IdentNumber == 0x40 && m.Name != "Spare" {
			t.Fatalf("module 0x40 is %q", m.Name)
		}
	}
}

func TestExpected(t *testing.T) {
	d := validDevice()
	exp := d.Expected()
	if len(exp) != 4 {
		t.Fatalf("%d blocks", len(exp))
	}
	slots := []uint16{0, 1, 2, 3}
	for i, b := range exp {
		if len(b.Modules) != 1 || b.Modules[0].Slot != slots[i] || b.Modules[0].API != API {
			t.Fatalf("block %d: %+v", i, b)
		}
	}
	dap := exp[0].Modules[0]
	if dap.IdentNumber != 0x100 || len(dap.Submodules) != 3 || dap.Submodules[0].Subslot != SubslotDAP {
		t.Fatalf("DAP %+v", dap)
	}
	// NO_IO: one input description of length 0.
	if ds := dap.Submodules[0].Descriptions; len(ds) != 1 || ds[0].Type != pnio.DataInput || ds[0].Length != 0 {
		t.Fatalf("NO_IO descriptions %+v", ds)
	}
	di := exp[1].Modules[0].Submodules[0]
	if di.Type() != pnio.SubmoduleInput || di.Properties != uint16(pnio.SubmoduleInput) || di.InputLength() != 8 || len(di.Descriptions) != 1 {
		t.Fatalf("input %+v", di)
	}
	do := exp[2].Modules[0].Submodules[0]
	if do.Type() != pnio.SubmoduleOutput || do.OutputLength() != 4 || len(do.Descriptions) != 1 || do.Descriptions[0].Type != pnio.DataOutput {
		t.Fatalf("output %+v", do)
	}
	io := exp[3].Modules[0].Submodules
	if len(io) != 2 || io[0].Subslot != 1 || io[1].Subslot != 2 {
		t.Fatalf("slot 3 not sorted: %+v", io)
	}
	if io[1].Type() != pnio.SubmoduleInputOutput || len(io[1].Descriptions) != 2 || io[1].InputLength() != 1 || io[1].OutputLength() != 1 {
		t.Fatalf("input-output %+v", io[1])
	}
}

func TestRealIdentification(t *testing.T) {
	d := validDevice()
	id := d.RealIdentification()
	if id.Expected || len(id.APIs) != 1 || id.APIs[0].API != API {
		t.Fatalf("%+v", id)
	}
	slots := id.APIs[0].Slots
	if len(slots) != 4 || slots[0].Slot != 0 || slots[0].IdentNumber != 0x100 || slots[3].Slot != 3 {
		t.Fatalf("slots %+v", slots)
	}
	if s := slots[3].Submodules; len(s) != 2 || s[0].Subslot != 1 || s[0].IdentNumber != 0x31 || s[1].IdentNumber != 0x32 {
		t.Fatalf("slot 3 submodules %+v", s)
	}
}

func TestStandardDAP(t *testing.T) {
	for _, n := range []int{0, 1, 4} {
		m := StandardDAP("X", 0x77, n)
		if m.Name != "X" || m.IdentNumber != 0x77 || len(m.Submodules) != 2+n {
			t.Fatalf("%d ports: %+v", n, m)
		}
		if s, ok := m.Submodule(SubslotDAP); !ok || s.IdentNumber != 1 || s.Name != "X" {
			t.Fatalf("identity %+v", s)
		}
		if s, ok := m.Submodule(SubslotInterface); !ok || s.IdentNumber != 0x8000 {
			t.Fatalf("interface %+v", s)
		}
		for i := 0; i < n; i++ {
			s, ok := m.Submodule(SubslotPort1 + uint16(i))
			if !ok || s.IdentNumber != 0x8001+uint32(i) || s.Name != "Port "+string(rune('1'+i)) || s.Type() != pnio.SubmoduleNoIO {
				t.Fatalf("port %d: %+v", i+1, s)
			}
		}
		if _, ok := m.Submodule(SubslotPort1 + uint16(n)); ok {
			t.Fatalf("%d ports: one port too many", n)
		}
	}
}
