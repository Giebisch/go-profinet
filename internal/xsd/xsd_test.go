// SPDX-License-Identifier: MIT

package xsd

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLoadDir(t *testing.T) {
	s, err := LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	item := s.Elements["Item"]
	if item == nil {
		t.Fatal("no Item")
	}
	if !item.Attributes["ID"].Required || item.Attributes["Note"].Required || item.Attributes["Kind"].Type != "token" {
		t.Fatalf("Item attributes %+v", item.Attributes)
	}
	want := map[string]bool{"Name": true, "Info": true, "Leaf": true, "Nested": true}
	if !reflect.DeepEqual(item.Children, want) {
		t.Fatalf("Item children %v", item.Children)
	}
	if !s.Elements["Nested"].Attributes["Depth"].Required {
		t.Fatal("Nested Depth")
	}
	if _, ok := s.Elements["Leaf"].Attributes["Lang"]; !ok {
		t.Fatal("Leaf Lang")
	}
	if r := s.Elements["Root"]; !r.Children["Item"] || !r.Children["Self"] {
		t.Fatalf("Root %+v", r)
	}
}

func TestErrors(t *testing.T) {
	if _, err := LoadDir(t.TempDir()); err == nil {
		t.Fatal("empty dir")
	}
	if _, err := Parse(strings.NewReader("<notschema/>")); err == nil {
		t.Fatal("not a schema")
	}
	if _, err := Parse(strings.NewReader("<xs:schema")); err == nil {
		t.Fatal("truncated")
	}
	dir := t.TempDir()
	os.WriteFile(dir+"/bad.xsd", []byte("<x/>"), 0o644)
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("bad file")
	}
	if _, err := LoadDir(dir + "/missing"); err == nil {
		t.Fatal("missing dir")
	}
}
