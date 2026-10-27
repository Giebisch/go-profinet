// SPDX-License-Identifier: MIT

package gsdml

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/model"
)

func TestApplyRef(t *testing.T) {
	cases := []struct {
		ref  Ref
		want []byte
		err  bool
	}{
		{Ref{ByteOffset: "0", DataType: "Unsigned8", DefaultValue: "200"}, []byte{200, 0, 0, 0, 0, 0, 0, 0}, false},
		{Ref{ByteOffset: "0", DataType: "Unsigned32", DefaultValue: "0x01020304"}, []byte{1, 2, 3, 4, 0, 0, 0, 0}, false},
		{Ref{ByteOffset: "0", DataType: "Integer32", DefaultValue: "-1"}, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0, 0, 0, 0}, false},
		{Ref{ByteOffset: "0", DataType: "Float32", DefaultValue: "1.5"}, []byte{0x3F, 0xC0, 0, 0, 0, 0, 0, 0}, false},
		{Ref{ByteOffset: "0", DataType: "Float64", DefaultValue: "1"}, []byte{0x3F, 0xF0, 0, 0, 0, 0, 0, 0}, false},
		{Ref{ByteOffset: "1", DataType: "VisibleString", Length: "3", DefaultValue: "abc"}, []byte{0, 'a', 'b', 'c', 0, 0, 0, 0}, false},
		{Ref{ByteOffset: "2", DataType: "OctetString", Length: "2", DefaultValue: "0xAA,0xBB"}, []byte{0, 0, 0xAA, 0xBB, 0, 0, 0, 0}, false},
		{Ref{ByteOffset: "7", BitOffset: "7", DataType: "Bit", DefaultValue: "1"}, []byte{0, 0, 0, 0, 0, 0, 0, 0x80}, false},
		{Ref{ByteOffset: "0", BitOffset: "4", BitLength: "4", DataType: "BitArea", DefaultValue: "15"}, []byte{0xF0, 0, 0, 0, 0, 0, 0, 0}, false},
		{Ref{ByteOffset: "x", DataType: "Unsigned8", DefaultValue: "1"}, nil, true},
		{Ref{ByteOffset: "7", DataType: "Unsigned16", DefaultValue: "1"}, nil, true},
		{Ref{ByteOffset: "0", DataType: "Unsigned8", DefaultValue: "300"}, nil, true},
		{Ref{ByteOffset: "0", DataType: "Integer8", DefaultValue: "x"}, nil, true},
		{Ref{ByteOffset: "0", DataType: "Float32", DefaultValue: "x"}, nil, true},
		{Ref{ByteOffset: "0", DataType: "Float64", DefaultValue: "x"}, nil, true},
		{Ref{ByteOffset: "0", DataType: "OctetString", Length: "1", DefaultValue: "zz"}, nil, true},
		{Ref{ByteOffset: "0", BitOffset: "6", BitLength: "4", DataType: "BitArea", DefaultValue: "1"}, nil, true},
		{Ref{ByteOffset: "9", DataType: "Bit", DefaultValue: "1"}, nil, true},
		{Ref{ByteOffset: "0", DataType: "Bogus", DefaultValue: "1"}, nil, true},
	}
	for i, c := range cases {
		rec := make([]byte, 8)
		err := applyRef(rec, &c.ref)
		if c.err {
			if err == nil {
				t.Errorf("%d: no error", i)
			}
			continue
		}
		if err != nil || !bytes.Equal(rec, c.want) {
			t.Errorf("%d: % x %v", i, rec, err)
		}
	}
}

func TestToRecordErrors(t *testing.T) {
	doc := &Document{}
	for _, r := range []ParameterRecordDataItem{
		{Index: "x", Length: "1"},
		{Index: "40000", Length: "1"},
		{Index: "1", Length: "70000"},
		{Index: "1", Length: "2", Consts: []*Const{{Data: "0x01,0x02,0x03"}}},
		{Index: "1", Length: "2", Consts: []*Const{{Data: "nope"}}},
		{Index: "1", Length: "2", Refs: []*Ref{{ByteOffset: "5", DataType: "Unsigned8", DefaultValue: "1"}}},
	} {
		if _, err := toRecord(doc, &r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	r, err := toRecord(doc, &ParameterRecordDataItem{Index: "0x10", Length: "2", Consts: []*Const{{ByteOffset: "1", Data: "0x07"}}})
	if err != nil || r.Index != 0x10 || !bytes.Equal(r.Default, []byte{0, 7}) {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestToModelErrors(t *testing.T) {
	valid := func() *Document {
		doc, err := FromModel(testModel(), GenerateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	cases := []func(*Document){
		func(d *Document) { d.ProfileBody = nil },
		func(d *Document) { d.ProfileBody.ApplicationProcess.DeviceAccessPointList = nil },
		func(d *Document) { d.ProfileBody.DeviceIdentity.VendorID = "zz" },
		func(d *Document) { d.ProfileBody.DeviceIdentity.DeviceID = "zz" },
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0].ModuleIdentNumber = "zz"
		},
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0].UseableModules.ModuleItemRefs[0].ModuleItemTarget = "none"
		},
		func(d *Document) { d.ProfileBody.ApplicationProcess.ModuleList.ModuleItems[0].ModuleIdentNumber = "zz" },
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.ModuleList.ModuleItems[0].VirtualSubmoduleList.VirtualSubmoduleItems[0].SubmoduleIdentNumber = "zz"
		},
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.ModuleList.ModuleItems[0].VirtualSubmoduleList.VirtualSubmoduleItems[0].IOData.Input.DataItems[0].Length = ""
		},
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.ModuleList.ModuleItems[1].VirtualSubmoduleList.VirtualSubmoduleItems[0].IOData.Output.DataItems[0].DataType = "Bogus"
		},
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0].SystemDefinedSubmoduleList.InterfaceSubmoduleItems[0].SubslotNumber = "zz"
		},
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0].SystemDefinedSubmoduleList.PortSubmoduleItems[0].SubmoduleIdentNumber = "zz"
		},
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0].UseableModules.ModuleItemRefs[0].FixedInSlots = "x"
		},
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.ModuleList.ModuleItems[0].VirtualSubmoduleList.VirtualSubmoduleItems[0].RecordDataList.ParameterRecordDataItems[0].Index = "x"
		},
		func(d *Document) {
			d.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0].VirtualSubmoduleList.VirtualSubmoduleItems[0].SubmoduleIdentNumber = "zz"
		},
	}
	for i, mutate := range cases {
		d := valid()
		mutate(d)
		if _, err := ToModel(d, ""); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	// Too much IO data in one submodule.
	d := valid()
	in := d.ProfileBody.ApplicationProcess.ModuleList.ModuleItems[0].VirtualSubmoduleList.VirtualSubmoduleItems[0].IOData.Input
	in.DataItems[0].Length = "1500"
	if _, err := ToModel(d, ""); !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("err %v", err)
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"My Device 3000!": "my-device-3000", "": "device-", "Port-001": "device-port-001",
		strings.Repeat("a", 70): strings.Repeat("a", 63), "--x--": "x",
	} {
		if got := dnsName(in); got != want {
			t.Errorf("dnsName(%q) = %q, want %q", in, got, want)
		}
	}
	if r := parseSoftwareRelease(""); r.Prefix != 'V' {
		t.Fatal(r)
	}
	if r := parseSoftwareRelease("P3.4.5.6"); r.Prefix != 'P' || r.InternalChange != 5 {
		t.Fatal(r)
	}
	if formatOctets([]byte{1, 0xAB}) != "0x01,0xAB" {
		t.Fatal(formatOctets([]byte{1, 0xAB}))
	}
	if _, err := parseOctets("0x100"); err == nil {
		t.Fatal("octet overflow")
	}
	m := &model.Device{Identity: model.Identity{}}
	if got := FileName(m, "V2.4", testTime); got != "GSDML-V2.4-Vendor-Device-20260101.xml" {
		t.Fatal(got)
	}
	doc := &Document{}
	if doc.Text("x") != "x" {
		t.Fatal("Text without a text list")
	}
	if (Issue{Path: "a", Message: "b"}).String() != "a: b" {
		t.Fatal("issue string")
	}
	if e := (&ValidationError{Issues: []Issue{{Path: "a", Message: "b"}}}); e.Error() != "gsdml: a: b" {
		t.Fatal(e.Error())
	}
}

func TestCharsetsAndLimits(t *testing.T) {
	for _, enc := range []string{"UTF-8", "us-ascii", "windows-1252"} {
		in := `<?xml version="1.0" encoding="` + enc + `"?><ISO15745Profile xmlns="http://www.profibus.com/GSDML/2003/11/DeviceProfile"/>`
		if _, err := ParseBytes([]byte(in)); err != nil {
			t.Errorf("%s: %v", enc, err)
		}
	}
	big := io.MultiReader(strings.NewReader("<a>"), io.LimitReader(zeros{}, MaxDocumentSize+10))
	if _, err := Parse(big); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Fatalf("err %v", err)
	}
	if err := Encode(failWriter{}, &Document{}); err == nil {
		t.Fatal("write error not reported")
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

var testTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// TestDCPHelloAndProtocols: DCP_HelloSupported follows the model, and the
// generated interface declares only the protocols the device implements
// (LLDP; no SNMP agent exists).
func TestDCPHelloAndProtocols(t *testing.T) {
	for _, hello := range []bool{false, true} {
		m := testModel()
		m.Capabilities.DCPHello = hello
		doc, err := FromModel(m, GenerateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("SNMP")) {
			t.Fatal("generated GSDML declares SNMP")
		}
		back, err := ParseBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		dap := back.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItems[0]
		m2, err := ToModel(back, dap.ID)
		if err != nil || m2.Capabilities.DCPHello != hello {
			t.Fatalf("hello %v: %+v %v", hello, m2.Capabilities, err)
		}
	}
}
