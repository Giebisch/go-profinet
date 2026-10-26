// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedUpToDate is make generate-check as a test: the committed
// files are what the catalog generates.
func TestGeneratedUpToDate(t *testing.T) {
	c, raw, err := load("../../../gsdml/schema/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	src, err := generateGo(c, raw)
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile("../../../gsdml/zz_generated.go")
	if err != nil || !bytes.Equal(have, src) {
		t.Fatal("gsdml/zz_generated.go is out of date: run go generate ./gsdml")
	}
	doc, err := os.ReadFile("../../../gsdml/SCHEMA.md")
	if err != nil || !bytes.Equal(doc, generateDoc(c)) {
		t.Fatal("gsdml/SCHEMA.md is out of date: run go generate ./gsdml")
	}
}

func TestCatalogChecks(t *testing.T) {
	bad := []string{
		`{"root": "A", "elements": [{"name": "A"}, {"name": "A"}]}`,
		`{"root": "B", "elements": [{"name": "A"}]}`,
		`{"root": "A", "simpleTypes": {"x": {"pattern": "("}}, "elements": [{"name": "A"}]}`,
		`{"root": "A", "elements": [{"name": "A", "attrs": ["noType"]}]}`,
		`{"root": "A", "elements": [{"name": "A", "attrs": ["a:missing"]}]}`,
		`{"root": "A", "simpleTypes": {"s": {}}, "elements": [{"name": "A", "attrs": ["a:s", "a:s"]}]}`,
		`{"root": "A", "elements": [{"name": "A", "children": ["B"]}]}`,
		`{"root": "A", "elements": [{"name": "A", "text": "string", "children": ["A"]}]}`,
		`{"root": "A", "unknownField": 1, "elements": [{"name": "A"}]}`,
	}
	dir := t.TempDir()
	for i, b := range bad {
		p := filepath.Join(dir, "c.json")
		_ = os.WriteFile(p, []byte(b), 0o644)
		if _, _, err := load(p); err == nil {
			t.Errorf("%d: accepted %s", i, b)
		}
	}
	if _, _, err := load(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file")
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{
		"TextId": "TextID", "DeviceIdentity": "DeviceIdentity", "DNS_CompatibleName": "DNSCompatibleName",
		"xml:lang": "Lang", "RT_Class3Properties": "RTClass3Properties", "ModuleIdentNumber": "ModuleIdentNumber",
	} {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Entry": "Entries", "Day": "Days", "Class": "Classes", "Item": "Items"} {
		if got := plural(in); got != want {
			t.Errorf("plural(%q) = %q", in, got)
		}
	}
	if lowerFirst("") != "" || lowerFirst("ID list") != "ID list" || lowerFirst("The x") != "the x" {
		t.Fatal("lowerFirst")
	}
	if c := parseChild("A+"); c.min != 1 || c.max != -1 {
		t.Fatal(c)
	}
}

func TestCompareXSD(t *testing.T) {
	c := &catalog{Elements: []element{
		{Name: "Item", Attrs: []string{"ID:x!", "Kind:x!", "Missing:x"}, Children: []string{"Name"}},
		{Name: "Absent"},
	}}
	err := compareXSD(c, "../../xsd/testdata")
	if err == nil || !strings.Contains(err.Error(), "3 places") {
		t.Fatalf("err %v", err)
	}
	if err := compareXSD(c, t.TempDir()); err == nil {
		t.Fatal("no schema")
	}
}
