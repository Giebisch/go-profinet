// SPDX-License-Identifier: MIT

package gsdml

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
)

// checkDoc generates the test model's GSDML, lets mod change it, and
// checks it.
func checkDoc(t *testing.T, mod func(*Document)) Report {
	t.Helper()
	doc, err := FromModel(testModel(), GenerateOptions{SupervisorAR: true})
	if err != nil {
		t.Fatal(err)
	}
	if mod != nil {
		mod(doc)
	}
	// Through XML and back, as a file would be read.
	b, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc, err = ParseBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return Check(doc)
}

func issuesOf(r Report, rule string, s Severity) []Issue {
	var out []Issue
	for _, i := range r.Issues {
		if i.Rule == rule && i.Severity == s {
			out = append(out, i)
		}
	}
	return out
}

func dap(doc *Document) *DeviceAccessPointItem {
	return doc.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0]
}

func modules(doc *Document) []*ModuleItem {
	return doc.ProfileBody.ApplicationProcess.ModuleList.ModuleItems
}

// TestCheckClean: what go-profinet generates has no issue at all.
func TestCheckClean(t *testing.T) {
	r := checkDoc(t, nil)
	if len(r.Issues) != 0 || r.SchemaVersion != "2.35" || r.Err() != nil {
		t.Fatalf("%+v", r)
	}
	// The fixtures: valid, their vendor extensions unvalidated.
	for f, unvalidated := range map[string]int{"testdata/otf-fixture-latin1.xml": 2, "testdata/pnio-rs_sample-16real-32bool.xml": 0} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := ParseBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		r := Check(doc)
		if r.Count(SeverityError) != 0 || r.Count(SeverityWarning) != 0 || r.Count(SeverityUnvalidated) != unvalidated {
			t.Errorf("%s: %+v", f, r.Issues)
		}
		if err := Validate(doc); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestCheckIODataLength(t *testing.T) {
	r := checkDoc(t, func(doc *Document) {
		sm := modules(doc)[0].VirtualSubmoduleList.VirtualSubmoduleItems[0]
		sm.IOData.Input.DataItems = append(sm.IOData.Input.DataItems, &DataItem{DataType: "OctetString", Length: "1432", TextID: sm.IOData.Input.DataItems[0].TextID})
	})
	got := issuesOf(r, RuleIODataLength, SeverityError)
	if len(got) != 1 || !strings.Contains(got[0].Message, "1440 octets of input data") {
		t.Fatalf("%+v", r.Issues)
	}
	var ve *ValidationError
	if err := r.Err(); !errors.As(err, &ve) || len(ve.Issues) != 1 || !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("%v", err)
	}
	// Output too; exactly 1439 octets fit.
	r = checkDoc(t, func(doc *Document) {
		sm := modules(doc)[1].VirtualSubmoduleList.VirtualSubmoduleItems[0]
		sm.IOData.Output.DataItems = []*DataItem{{DataType: "OctetString", Length: "1439", TextID: sm.IOData.Output.DataItems[0].TextID}}
	})
	if len(r.Issues) != 0 {
		t.Fatalf("1439 octets: %+v", r.Issues)
	}
	r = checkDoc(t, func(doc *Document) {
		sm := modules(doc)[1].VirtualSubmoduleList.VirtualSubmoduleItems[0]
		sm.IOData.Output.DataItems = []*DataItem{{DataType: "Unsigned64", TextID: sm.IOData.Output.DataItems[0].TextID}, {DataType: "OctetString", Length: "1432", TextID: sm.IOData.Output.DataItems[0].TextID}}
	})
	if len(issuesOf(r, RuleIODataLength, SeverityError)) != 1 {
		t.Fatalf("output: %+v", r.Issues)
	}
}

func TestCheckSlots(t *testing.T) {
	r := checkDoc(t, func(doc *Document) {
		d := dap(doc)
		d.PhysicalSlots = "0..4"
		refs := d.UseableModules.ModuleItemRefs
		refs[0].AllowedInSlots = "1..6" // 5 and 6 are not physical
		refs[0].FixedInSlots = "1"
		refs[1].AllowedInSlots = "2..4"
		refs[1].UsedInSlots = "1"  // not allowed
		refs[1].FixedInSlots = "1" // already fixed
	})
	got := issuesOf(r, RuleSlots, SeverityWarning)
	text := ""
	for _, i := range got {
		text += i.Message + "\n"
	}
	for _, want := range []string{"slot 5 is not among the PhysicalSlots", "slot 6 is not among", "UsedInSlots: slot 1 is not in AllowedInSlots", "FixedInSlots: slot 1 is not in AllowedInSlots", "slot 1 is also fixed by module"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	if r.Err() != nil {
		t.Fatalf("warnings fail validation: %v", r.Err())
	}
}

func TestCheckIdentAndRecords(t *testing.T) {
	r := checkDoc(t, func(doc *Document) {
		ms := modules(doc)
		ms[1].ModuleIdentNumber = ms[0].ModuleIdentNumber
		ms[0].VirtualSubmoduleList.VirtualSubmoduleItems[0].RecordDataList.ParameterRecordDataItems[0].Index = "0x8000"
	})
	if len(issuesOf(r, RuleIdentNumber, SeverityWarning)) != 1 || len(issuesOf(r, RuleRecordIndex, SeverityWarning)) != 1 || r.Err() != nil {
		t.Fatalf("%+v", r.Issues)
	}
}

func TestCheckSubslotsAndRatios(t *testing.T) {
	r := checkDoc(t, func(doc *Document) {
		sd := dap(doc).SystemDefinedSubmoduleList
		sd.InterfaceSubmoduleItems[0].SubslotNumber = "28672" // 0x7000
		sd.PortSubmoduleItems[0].SubslotNumber = "36864"      // 0x9000
		sd.InterfaceSubmoduleItems[0].ApplicationRelations.TimingProperties = &TimingProperties{ReductionRatio: "1 2 3 1024"}
	})
	if len(issuesOf(r, RuleSubslot, SeverityWarning)) != 2 || len(issuesOf(r, RuleReductionRatio, SeverityWarning)) != 2 {
		t.Fatalf("%+v", r.Issues)
	}
}

// TestCheckNewerSchema: a document of a newer schema version, with
// elements and attributes the catalog does not know, stays valid; what it
// adds is reported as unvalidated.
func TestCheckNewerSchema(t *testing.T) {
	r := checkDoc(t, func(doc *Document) {
		for i, a := range doc.Attrs {
			if strings.HasSuffix(a.Name.Local, "schemaLocation") {
				doc.Attrs[i].Value = strings.Replace(a.Value, "V2.35", "V2.50", 1)
			}
		}
		d := dap(doc)
		d.Attrs = append(d.Attrs, xml.Attr{Name: xml.Name{Local: "SecureAccessSupported"}, Value: "true"})
		d.Unknown = append(d.Unknown, &Node{XMLName: xml.Name{Local: "FutureList"}})
	})
	if r.SchemaVersion != "2.50" || len(issuesOf(r, RuleSchemaVersion, SeverityUnvalidated)) != 1 || len(issuesOf(r, RuleUnknown, SeverityUnvalidated)) != 2 || r.Err() != nil {
		t.Fatalf("%+v", r)
	}
	for _, v := range []string{"2.45", "2.4", "2.3"} {
		if newerThanCatalog(v) {
			t.Errorf("%s newer than the catalog", v)
		}
	}
	if !newerThanCatalog("2.46") || newerThanCatalog("x") || versionNumber("2") != 0 || versionNumber("V2.5") != 250 || versionNumber("2.x") != 0 {
		t.Fatal("versions")
	}
	// No schemaLocation: no version.
	if v := schemaVersion(&Document{}); v != "" {
		t.Fatal(v)
	}
}

func TestSeverityJSON(t *testing.T) {
	b, err := json.Marshal(Issue{Path: "p", Message: "m", Severity: SeverityWarning, Rule: RuleSlots})
	if err != nil || string(b) != `{"path":"p","message":"m","severity":"warning","rule":"slots"}` {
		t.Fatalf("%s %v", b, err)
	}
	for s, want := range map[Severity]string{SeverityError: "error", SeverityUnvalidated: "unvalidated", 7: "Severity(7)"} {
		if s.String() != want {
			t.Errorf("%d: %s", s, s)
		}
	}
	// A document without a body has no semantic issues to report.
	semantic(&Document{}, &validator{})
	semantic(&Document{ProfileBody: &ProfileBody{ApplicationProcess: &ApplicationProcess{}}}, &validator{})
}

func TestReportJSONRoundTrip(t *testing.T) {
	in := Report{Issues: []Issue{{Path: "a", Message: "m", Severity: SeverityWarning, Rule: RuleSlots}, {Severity: SeverityUnvalidated}, {}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Report
	if err := json.Unmarshal(b, &out); err != nil || !reflect.DeepEqual(in, out) {
		t.Fatalf("%s: %+v %v", b, out, err)
	}
	var s Severity
	if err := s.UnmarshalText([]byte("fatal")); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatal(err)
	}
}
