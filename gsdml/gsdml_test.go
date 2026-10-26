// SPDX-License-Identifier: MIT

package gsdml

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

func load(t *testing.T, name string) *Document {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	doc, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestPnioRsSample(t *testing.T) {
	doc := load(t, "pnio-rs_sample-16real-32bool.xml")
	if err := Validate(doc); err != nil {
		t.Fatal(err)
	}
	m, err := ToModel(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Identity.VendorID != 0xFFFF || m.Identity.DeviceID != 1 || m.Identity.VendorName != "Core Engineering" || m.Identity.OrderID != "PNIO-SAMPLE" {
		t.Fatalf("identity %+v", m.Identity)
	}
	if len(m.Slots) != 4 {
		t.Fatalf("slots %+v", m.Slots)
	}
	want := []struct{ in, out uint16 }{{64, 0}, {4, 0}, {0, 64}, {0, 4}}
	for i, w := range want {
		s := m.Slots[i].Module.Submodules[0]
		if s.InputLen != w.in || s.OutputLen != w.out {
			t.Fatalf("slot %d: %d/%d", i+1, s.InputLen, s.OutputLen)
		}
	}
	if m.Capabilities.MinDeviceInterval != 32 || m.Capabilities.Ports != 1 || len(m.Capabilities.ReductionRatios) != 10 {
		t.Fatalf("capabilities %+v", m.Capabilities)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := ToModel(doc, "nope"); !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestFixtureLatin1AndPreservation(t *testing.T) {
	doc := load(t, "otf-fixture-latin1.xml")
	if err := Validate(doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Text("T_DAP_Info"); got != "Kopfstation für Prüfungen été" {
		t.Fatalf("latin-1 text %q", got)
	}
	m, err := ToModel(doc, "DAP_A")
	if err != nil {
		t.Fatal(err)
	}
	if m.Capabilities.ConformanceClass != "B" || m.Capabilities.MaxARs != 2 || m.Capabilities.Ports != 2 || !m.Capabilities.LLDP {
		t.Fatalf("capabilities %+v", m.Capabilities)
	}
	if m.Identity.SoftwareRevision != (pnio.SoftwareRevision{Prefix: 'V', FunctionalEnhance: 2, BugFix: 1, InternalChange: 7}) || m.Identity.HardwareRevision != 3 {
		t.Fatalf("revisions %+v", m.Identity)
	}
	// MOD_DI fixed in slot 1, MOD_AO used in 2 and 4; the spare module is
	// only in the catalog.
	if len(m.Slots) != 3 || m.Slots[0].Number != 1 || m.Slots[1].Number != 2 || m.Slots[2].Number != 4 || len(m.Catalog) != 3 {
		t.Fatalf("slots %+v catalog %d", m.Slots, len(m.Catalog))
	}
	di := m.Slots[0].Module.Submodules[0]
	if di.InputLen != 2+1+5 || len(di.Records) != 1 {
		t.Fatalf("DI %+v", di)
	}
	rec := di.Records[0]
	if rec.Index != 100 || !bytes.Equal(rec.Default, []byte{0x01, 0x02, 0x08, 0x05, 0x12, 0x34, 0xFF, 0xFE}) {
		t.Fatalf("record %+v % x", rec, rec.Default)
	}
	ao := m.Slots[1].Module.Submodules[0]
	if ao.Subslot != 2 || ao.OutputLen != 9 || ao.InputLen != 1 {
		t.Fatalf("AO %+v", ao)
	}

	// Unknown attributes and elements survive a round trip.
	out, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Special="kept"`, "FutureFeature", "text is kept", `xml:lang="de"`, "Prüfgerät", `schemaLocation=`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("round trip lost %q", want)
		}
	}
	doc2, err := ParseBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(doc2); err != nil {
		t.Fatal(err)
	}
	out2, _ := Marshal(doc2)
	if !bytes.Equal(out, out2) {
		t.Fatal("encoding is not stable")
	}
	m2, err := ToModel(doc2, "")
	if err != nil || !reflect.DeepEqual(m, m2) {
		t.Fatalf("model differs after the round trip: %v", err)
	}
}

func testModel() *model.Device {
	return &model.Device{
		Identity: model.Identity{VendorID: 0xF000, DeviceID: 0x42, VendorName: "OT Fabric", TypeOfStation: "go-profinet Example Device", Family: "Examples",
			OrderID: "OTF-1", HardwareRevision: 2, SoftwareRevision: pnio.SoftwareRevision{Prefix: 'V', FunctionalEnhance: 1, BugFix: 2}},
		Capabilities: model.Capabilities{MaxARs: 2, MinDeviceInterval: 32, ConformanceClass: "A", Ports: 1, LLDP: true},
		DAP:          model.StandardDAP("DAP", 0x100, 1),
		Slots: []model.Slot{
			{Number: 1, Module: model.Module{Name: "DI8", IdentNumber: 0x10, Submodules: []model.Submodule{{Name: "DI8", IdentNumber: 0x11, Subslot: 1, InputLen: 8,
				Records: []model.Record{{Index: 0x10, Name: "Filter", Length: 2, Default: []byte{0, 3}}}}}}},
			{Number: 3, Module: model.Module{Name: "DO4", IdentNumber: 0x20, Submodules: []model.Submodule{{Name: "DO4", IdentNumber: 0x21, Subslot: 1, OutputLen: 4}}}},
			{Number: 4, Module: model.Module{Name: "DO4", IdentNumber: 0x20, Submodules: []model.Submodule{{Name: "DO4", IdentNumber: 0x21, Subslot: 1, OutputLen: 4}}}},
		},
		Catalog: []model.Module{{Name: "Empty", IdentNumber: 0x30, Submodules: []model.Submodule{{Name: "Empty", IdentNumber: 0x31, Subslot: 1}}}},
	}
}

func TestFromModelRoundTrip(t *testing.T) {
	m := testModel()
	doc, err := FromModel(m, GenerateOptions{SupervisorAR: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc2, err := ParseBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(doc2); err != nil {
		t.Fatal(err)
	}
	m2, err := ToModel(doc2, "")
	if err != nil {
		t.Fatal(err)
	}
	if m2.Identity.VendorID != m.Identity.VendorID || m2.Identity.OrderID != "OTF-1" || m2.Identity.SoftwareRevision != m.Identity.SoftwareRevision {
		t.Fatalf("identity %+v", m2.Identity)
	}
	if len(m2.Slots) != 3 || m2.Slots[2].Number != 4 {
		t.Fatalf("slots %+v", m2.Slots)
	}
	for i := range m.Slots {
		a, b := m.Slots[i].Module.Submodules[0], m2.Slots[i].Module.Submodules[0]
		if a.IdentNumber != b.IdentNumber || a.InputLen != b.InputLen || a.OutputLen != b.OutputLen || len(a.Records) != len(b.Records) {
			t.Fatalf("slot %d: %+v vs %+v", m.Slots[i].Number, a, b)
		}
	}
	if !bytes.Equal(m2.Slots[0].Module.Submodules[0].Records[0].Default, []byte{0, 3}) {
		t.Fatal("record default lost")
	}
	if len(m2.Catalog) != 3 || m2.Capabilities.MaxARs != 2 || !m2.Capabilities.LLDP {
		t.Fatalf("catalog %d caps %+v", len(m2.Catalog), m2.Capabilities)
	}
	if !strings.Contains(string(b), `DNS_CompatibleName="go-profinet-example-device"`) {
		t.Fatal("DNS name")
	}
	if got := FileName(m, "", time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)); got != "GSDML-V2.35-OT_Fabric-Examples-20261026.xml" {
		t.Fatal(got)
	}
	bad := testModel()
	bad.Slots[0].Number = 0
	if _, err := FromModel(bad, GenerateOptions{}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("err %v", err)
	}
}

func TestValidationIssues(t *testing.T) {
	doc := load(t, "otf-fixture-latin1.xml")
	dap := doc.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0]
	dap.ModuleIdentNumber = "12" // not hexadecimal
	dap.PhysicalSlots = "1..x"   // not a value list
	dap.UseableModules.ModuleItemRefs[0].ModuleItemTarget = "MISSING"
	dap.CertificationInfo.ConformanceClass = "Z"                               // not in the enumeration
	dap.ModuleInfo.Name.TextID = "T_NONE"                                      // no such text
	dap.IOConfigData = nil                                                     // required element
	dap.DNSCompatibleName = ""                                                 // required attribute
	doc.ProfileBody.ApplicationProcess.ModuleList.ModuleItems[1].ID = "MOD_DI" // duplicate ID
	err := Validate(doc)
	var ve *ValidationError
	if !errors.As(err, &ve) || !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
	want := []string{"ModuleIdentNumber", "PhysicalSlots", `"MISSING"`, `"Z"`, `"T_NONE"`, "IOConfigData", "DNS_CompatibleName", `"MOD_DI" already used`}
	all := ""
	for _, i := range ve.Issues {
		all += i.String() + "\n"
	}
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("no issue about %s in\n%s", w, all)
		}
	}
	if !strings.Contains(ve.Error(), "issues") {
		t.Fatal(ve.Error())
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"<x/>",
		`<?xml version="1.0" encoding="koi8-r"?><ISO15745Profile xmlns="http://www.profibus.com/GSDML/2003/11/DeviceProfile"/>`,
		`<ISO15745Profile xmlns="urn:other"/>`,
		`<ISO15745Profile xmlns="http://www.profibus.com/GSDML/2003/11/DeviceProfile"><ProfileBody>`,
	} {
		if _, err := ParseBytes([]byte(in)); err == nil {
			t.Errorf("parsed %q", in)
		}
	}
	if _, err := Parse(strings.NewReader("\xEF\xBB\xBF<ISO15745Profile xmlns=\"http://www.profibus.com/GSDML/2003/11/DeviceProfile\"/>")); err != nil {
		t.Fatalf("BOM: %v", err)
	}
}

func TestValueLists(t *testing.T) {
	v, err := ParseValueList(" 1..3 5 7..8 ", 100)
	if err != nil || !reflect.DeepEqual(v, []uint32{1, 2, 3, 5, 7, 8}) {
		t.Fatal(v, err)
	}
	if FormatValueList(v) != "1..3 5 7..8" {
		t.Fatal(FormatValueList(v))
	}
	if _, err := ParseValueList("1..100000", 10); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Fatal(err)
	}
	for _, bad := range []string{"a", "3..1", "1..b"} {
		if _, err := ParseValueList(bad, 10); err == nil {
			t.Fatalf("parsed %q", bad)
		}
	}
	if n, err := ParseUint32("0x00FF"); err != nil || n != 255 {
		t.Fatal(n, err)
	}
	if _, err := ParseUint16("70000"); err == nil {
		t.Fatal("overflow")
	}
	if !ParseBool("1", false) || ParseBool("false", true) || !ParseBool("", true) {
		t.Fatal("bools")
	}
}

func TestDataItemSizes(t *testing.T) {
	for _, dt := range DataTypes {
		n, err := DataItemSize(string(dt), "4")
		switch dt {
		case DataTypeBit, DataTypeBitArea:
			if err == nil {
				t.Errorf("%s has a size", dt)
			}
		default:
			if err != nil || n <= 0 {
				t.Errorf("%s: %d %v", dt, n, err)
			}
		}
	}
	if _, err := DataItemSize("OctetString", ""); err == nil {
		t.Fatal("string without length")
	}
	if _, err := DataItemSize("Bogus", ""); !errors.Is(err, profinet.ErrUnsupported) {
		t.Fatal(err)
	}
}

func FuzzParse(f *testing.F) {
	for _, n := range []string{"pnio-rs_sample-16real-32bool.xml", "otf-fixture-latin1.xml"} {
		b, err := os.ReadFile("testdata/" + n)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		doc, err := ParseBytes(b)
		if err != nil {
			return
		}
		_ = Validate(doc)
		_, _ = ToModel(doc, "")
		if _, err := Marshal(doc); err != nil {
			t.Fatalf("marshal: %v", err)
		}
	})
}

func BenchmarkParse(b *testing.B) {
	data, err := os.ReadFile("testdata/pnio-rs_sample-16real-32bool.xml")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		if _, err := ParseBytes(data); err != nil {
			b.Fatal(err)
		}
	}
}
