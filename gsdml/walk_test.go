// SPDX-License-Identifier: MIT

package gsdml

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fill gives every element of the catalog one instance, recursively, and
// every attribute and text the value val: a document with every element the
// generated validators check.
func fill(v reflect.Value, val string, depth int, seen map[reflect.Type]int) {
	t := v.Type()
	if seen[t] > 1 || depth > 40 {
		return
	}
	seen[t]++
	defer func() { seen[t]-- }()
	for i := 0; i < t.NumField(); i++ {
		f, sf := v.Field(i), t.Field(i)
		if !sf.IsExported() || sf.Name == "XMLName" || sf.Name == "Attrs" || sf.Name == "Unknown" {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString(val)
		case reflect.Pointer:
			if f.Type().Elem().Kind() == reflect.Struct {
				p := reflect.New(f.Type().Elem())
				fill(p.Elem(), val, depth+1, seen)
				f.Set(p)
			}
		case reflect.Slice:
			if et := f.Type().Elem(); et.Kind() == reflect.Pointer && et.Elem().Kind() == reflect.Struct {
				p := reflect.New(et.Elem())
				fill(p.Elem(), val, depth+1, seen)
				f.Set(reflect.Append(reflect.MakeSlice(f.Type(), 0, 1), p))
			}
		default:
		}
	}
}

// TestValidateEveryElement runs the validator of every element of the
// catalog: with every attribute empty (the required ones are reported),
// with values no type accepts, and with plausible ones; and writes and reads
// back the full document.
func TestValidateEveryElement(t *testing.T) {
	for _, val := range []string{"", "\xff not a value", "1", "0x10", "true", "ID_1"} {
		doc := &Document{}
		fill(reflect.ValueOf(doc).Elem(), val, 0, map[reflect.Type]int{})
		err := Validate(doc)
		var ve *ValidationError
		if !errors.As(err, &ve) || len(ve.Issues) == 0 {
			t.Fatalf("%q: no issues: %v", val, err)
		}
		if val == "" && !strings.Contains(err.Error(), "missing required attribute") {
			t.Fatalf("%q: %v", val, ve.Issues[:3])
		}
		b, err := Marshal(doc)
		if err != nil {
			if val == "\xff not a value" {
				continue // invalid UTF-8 cannot be written as XML
			}
			t.Fatalf("%q: marshal: %v", val, err)
		}
		again, err := ParseBytes(b)
		if err != nil {
			t.Fatalf("%q: parse: %v", val, err)
		}
		if !errors.As(Validate(again), &ve) {
			t.Fatalf("%q: the document read back validates", val)
		}
	}
	// Elements without their required children.
	doc := &Document{ProfileBody: &ProfileBody{ApplicationProcess: &ApplicationProcess{}}}
	fill(reflect.ValueOf(doc.ProfileBody.ApplicationProcess).Elem(), "1", 0, map[reflect.Type]int{})
	clearSlices(reflect.ValueOf(doc).Elem(), 0)
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "missing required element") {
		t.Fatalf("without children: %v", err)
	}
}

// clearSlices empties every list of elements, so that the validators report
// their missing children.
func clearSlices(v reflect.Value, depth int) {
	if depth > 40 {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		if !t.Field(i).IsExported() {
			continue
		}
		switch f.Kind() {
		case reflect.Pointer:
			if !f.IsNil() && f.Elem().Kind() == reflect.Struct {
				clearSlices(f.Elem(), depth+1)
			}
		case reflect.Slice:
			if f.Type().Elem().Kind() == reflect.Pointer && f.Type().Elem().Elem().Kind() == reflect.Struct && t.Field(i).Name != "Unknown" {
				f.Set(reflect.Zero(f.Type()))
			}
		default:
		}
	}
}
