// SPDX-License-Identifier: MIT

// Package gsdml reads, validates and writes GSDML (General Station
// Description Markup Language) files, the XML device descriptions of
// PROFINET IO devices, and converts between them and the device models of
// package model.
//
// The document types (zz_generated.go) are generated from the catalog
// schema/catalog.json by internal/cmd/gengsdml; SCHEMA.md lists what they
// model. Elements and attributes the catalog does not define are kept in
// the Unknown and Attrs fields and written back, so reading and writing a
// file does not lose information. Comments and the order of unknown
// elements among known ones are not preserved.
//
// The official GSDML schemas are licensed by PROFIBUS & PROFINET
// International to its members and are not part of this package; Validate
// checks the structural and lexical rules of the catalog and the
// references between elements, which covers what the device model relies
// on. It is not a substitute for PI's GSDML checker.
package gsdml

//go:generate go run ../internal/cmd/gengsdml -root ..

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/otfabric/go-profinet"
)

// MaxDocumentSize bounds the size of a document Parse reads. Vendor files
// reach 15 MB; the bound leaves room.
const MaxDocumentSize = 64 << 20

// Node is an XML element the catalog does not define, kept verbatim.
type Node struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Content []byte     `xml:",innerxml"`
}

// Document is a parsed GSDML file.
type Document = ISO15745Profile

// Parse reads a GSDML document. UTF-8 (with or without a byte order mark)
// and ISO-8859-1 encodings are accepted.
func Parse(r io.Reader) (*Document, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxDocumentSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxDocumentSize {
		return nil, fmt.Errorf("gsdml: %w: document larger than %d octets", profinet.ErrResourceExhausted, MaxDocumentSize)
	}
	return ParseBytes(data)
}

// ParseBytes reads a GSDML document from data.
func ParseBytes(data []byte) (*Document, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = charsetReader
	var doc Document
	if err := d.Decode(&doc); err != nil {
		return nil, fmt.Errorf("gsdml: %w: %v", profinet.ErrMalformed, err)
	}
	if doc.XMLName.Space != Namespace {
		return nil, fmt.Errorf("gsdml: %w: namespace %q is not the GSDML namespace", profinet.ErrMalformed, doc.XMLName.Space)
	}
	normalize(&doc)
	return &doc, nil
}

const nsXML = "http://www.w3.org/XML/1998/namespace"

// normalize rewrites the namespaced names of preserved attributes and
// elements into their prefixed form ("xsi:schemaLocation") with the
// prefixes the document declared, and drops the namespace declarations
// encoding/xml would otherwise write twice. Encoding the result writes
// the same names back, so that parsing and encoding are stable.
func normalize(doc *Document) {
	prefixes := map[string]string{nsXML: "xml"}
	walk(reflect.ValueOf(doc), func(attrs *[]xml.Attr, _ *Node) {
		if attrs == nil {
			return
		}
		for _, a := range *attrs {
			if a.Name.Space == "xmlns" {
				if _, ok := prefixes[a.Value]; !ok {
					prefixes[a.Value] = a.Name.Local
				}
			}
		}
	})
	walk(reflect.ValueOf(doc), func(attrs *[]xml.Attr, n *Node) {
		if n != nil {
			if p, ok := prefixes[n.XMLName.Space]; ok && n.XMLName.Space != "" {
				n.XMLName = xml.Name{Local: p + ":" + n.XMLName.Local}
			}
			attrs = &n.Attrs
		}
		out := (*attrs)[:0]
		for _, a := range *attrs {
			switch {
			case a.Name.Space == "" && a.Name.Local == "xmlns":
				continue // the default namespace is written by the encoder
			case a.Name.Space == "xmlns":
				a.Name = xml.Name{Local: "xmlns:" + a.Name.Local}
			case a.Name.Space != "":
				if p, ok := prefixes[a.Name.Space]; ok {
					a.Name = xml.Name{Local: p + ":" + a.Name.Local}
				}
			}
			out = append(out, a)
		}
		*attrs = out
	})
}

// walk calls fn with the Attrs of every element and with every preserved
// Node of the document.
func walk(v reflect.Value, fn func(attrs *[]xml.Attr, n *Node)) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		if n, ok := v.Interface().(*Node); ok {
			fn(nil, n)
			return
		}
		walk(v.Elem(), fn)
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walk(v.Index(i), fn)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if a, ok := f.Addr().Interface().(*[]xml.Attr); ok {
				fn(a, nil)
				continue
			}
			if f.Kind() == reflect.Pointer || f.Kind() == reflect.Slice {
				walk(f, fn)
			}
		}
	default:
	}
}

func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	case "iso-8859-1", "latin1", "iso8859-1", "iso_8859-1", "windows-1252", "cp1252":
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		out.Grow(len(b))
		for _, c := range b {
			out.WriteRune(rune(c))
		}
		return &out, nil
	default:
		return nil, fmt.Errorf("gsdml: %w: encoding %q", profinet.ErrUnsupported, label)
	}
}

// Encode writes the document as indented UTF-8 XML with an XML
// declaration.
func Encode(w io.Writer, doc *Document) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	e := xml.NewEncoder(w)
	e.Indent("", "  ")
	doc.XMLName = xml.Name{Space: Namespace, Local: "ISO15745Profile"}
	if err := e.Encode(doc); err != nil {
		return fmt.Errorf("gsdml: encode: %w", err)
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// Marshal returns the encoded document.
func Marshal(doc *Document) ([]byte, error) {
	var b bytes.Buffer
	if err := Encode(&b, doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

type simpleType struct {
	doc      string
	pattern  *regexp.Regexp
	enum     map[string]bool
	uintBits int
}

// Issue is a problem Validate found.
type Issue struct {
	// Path is the element path, for example
	// "ISO15745Profile/ProfileBody/DeviceIdentity".
	Path    string `json:"path"`
	Message string `json:"message"`
	// Severity grades the issue; Validate reports only errors.
	Severity Severity `json:"severity"`
	// Rule names the rule (Rule*); empty means RuleStructure.
	Rule string `json:"rule,omitempty"`
}

func (i Issue) String() string { return i.Path + ": " + i.Message }

// ValidationError lists the issues of a document. It matches
// profinet.ErrMalformed.
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 1 {
		return "gsdml: " + e.Issues[0].String()
	}
	return fmt.Sprintf("gsdml: %d issues, the first: %s", len(e.Issues), e.Issues[0])
}

// Is makes a ValidationError match profinet.ErrMalformed.
func (e *ValidationError) Is(target error) bool { return target == profinet.ErrMalformed }

type validator struct {
	path   []string
	issues []Issue
	ids    map[string]string
	refs   []ref
	texts  map[string]bool
	tids   []ref
}

type ref struct {
	path, kind, id string
}

func (v *validator) push(e string) { v.path = append(v.path, e) }
func (v *validator) pop()          { v.path = v.path[:len(v.path)-1] }

func (v *validator) report(format string, a ...any) {
	v.issues = append(v.issues, Issue{Path: strings.Join(v.path, "/"), Message: fmt.Sprintf(format, a...), Rule: RuleStructure})
}

func (v *validator) missing(child string) { v.report("missing required element %s", child) }

func (v *validator) attr(name, value, typ string, required bool) {
	if value == "" {
		if required {
			v.report("missing required attribute %s", name)
		}
		return
	}
	t := simpleTypes[typ]
	switch {
	case t.enum != nil && !t.enum[value]:
		v.report("attribute %s: %q is not a %s", name, value, typ)
		return
	case t.pattern != nil && !t.pattern.MatchString(value):
		v.report("attribute %s: %q is not a valid %s", name, value, typ)
		return
	case t.uintBits > 0:
		if _, err := parseUint(value, t.uintBits); err != nil {
			v.report("attribute %s: %v", name, err)
			return
		}
	}
	if !utf8.ValidString(value) {
		v.report("attribute %s: invalid UTF-8", name)
	}
	path := strings.Join(v.path, "/")
	switch typ {
	case "ID":
		if name == "TextId" {
			v.texts[value] = true
			return
		}
		if prev, dup := v.ids[value]; dup {
			v.report("ID %q already used at %s", value, prev)
			return
		}
		v.ids[value] = path
	case "IDRef":
		if name == "TextId" {
			v.tids = append(v.tids, ref{path, name, value})
		} else {
			v.refs = append(v.refs, ref{path, name, value})
		}
	}
}

// Validate checks the document against the catalog's structural and
// lexical rules, checks that every ID is unique and every reference
// (module, submodule, value item, graphic, category and text references)
// resolves, and that no submodule's IO data exceed an RT frame. It returns
// nil or a *ValidationError with the errors; Check reports the warnings
// and the unvalidated content as well.
func Validate(doc *Document) error {
	r := Check(doc)
	return r.Err()
}

// parseUint parses a decimal or 0x hexadecimal unsigned value of bits
// bits.
func parseUint(s string, bits int) (uint64, error) {
	s = strings.TrimSpace(s)
	base := 10
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s, base = s[2:], 16
	}
	v, err := strconv.ParseUint(s, base, bits)
	if err != nil {
		return 0, fmt.Errorf("%q is not an unsigned %d-bit value", s, bits)
	}
	return v, nil
}

// ParseUint32 parses a decimal or 0x hexadecimal attribute value.
func ParseUint32(s string) (uint32, error) {
	v, err := parseUint(s, 32)
	return uint32(v), err
}

// ParseUint16 parses a decimal or 0x hexadecimal attribute value.
func ParseUint16(s string) (uint16, error) {
	v, err := parseUint(s, 16)
	return uint16(v), err
}

// ParseBool parses a Boolean attribute value; empty is def.
func ParseBool(s string, def bool) bool {
	switch strings.TrimSpace(s) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		return def
	}
}

// ParseValueList parses a ValueList attribute ("1..4 6") into its values.
// It bounds the expansion to max values.
func ParseValueList(s string, limit int) ([]uint32, error) {
	var out []uint32
	for _, tok := range strings.Fields(s) {
		lo, hi := tok, tok
		if i := strings.Index(tok, ".."); i >= 0 {
			lo, hi = tok[:i], tok[i+2:]
		}
		a, err := strconv.ParseUint(lo, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("gsdml: %w: value list %q", profinet.ErrMalformed, s)
		}
		b, err := strconv.ParseUint(hi, 10, 32)
		if err != nil || b < a {
			return nil, fmt.Errorf("gsdml: %w: value list %q", profinet.ErrMalformed, s)
		}
		if uint64(len(out))+b-a+1 > uint64(limit) {
			return nil, fmt.Errorf("gsdml: %w: value list %q has more than %d values", profinet.ErrResourceExhausted, s, limit)
		}
		for x := a; x <= b; x++ {
			out = append(out, uint32(x))
		}
	}
	return out, nil
}

// FormatValueList writes values as a ValueList, joining consecutive values
// into ranges.
func FormatValueList(values []uint32) string {
	var parts []string
	for i := 0; i < len(values); {
		j := i
		for j+1 < len(values) && values[j+1] == values[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("%d..%d", values[i], values[j]))
		} else {
			parts = append(parts, strconv.FormatUint(uint64(values[i]), 10))
		}
		i = j + 1
	}
	return strings.Join(parts, " ")
}

// Text returns the text of a TextId in the primary language, or the id
// itself when there is none.
func (doc *Document) Text(id string) string {
	if doc.ProfileBody == nil || doc.ProfileBody.ApplicationProcess == nil || doc.ProfileBody.ApplicationProcess.ExternalTextList == nil {
		return id
	}
	if pl := doc.ProfileBody.ApplicationProcess.ExternalTextList.PrimaryLanguage; pl != nil {
		for _, t := range pl.Texts {
			if t.TextID == id {
				return t.Value
			}
		}
	}
	return id
}
