// SPDX-License-Identifier: MIT

// Command gengsdml generates the GSDML document types of package gsdml
// (gsdml/zz_generated.go) and their reference (gsdml/SCHEMA.md) from the
// catalog gsdml/schema/catalog.json.
//
// Usage, from the module root:
//
//	go run ./internal/cmd/gengsdml          # write the generated files
//	go run ./internal/cmd/gengsdml -check   # fail when they are out of date
//	go run ./internal/cmd/gengsdml -xsd DIR # compare the catalog with the
//	                                        # GSDML XSDs in DIR
//
// The official GSDML schemas are published by PROFIBUS & PROFINET
// International to its members under a licence that does not allow
// redistributing them, so they are not in this repository and are not
// read by default. Someone holding a licensed copy can check the catalog
// against them with -xsd; the report names elements and attributes that
// the catalog lacks or that the schema does not define. See
// docs/CODEGEN.md.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/otfabric/go-profinet/internal/xsd"
)

type simpleType struct {
	Doc     string   `json:"doc"`
	Pattern string   `json:"pattern"`
	Enum    []string `json:"enum"`
	Uint    int      `json:"uint"`
}

type element struct {
	Name     string   `json:"name"`
	Doc      string   `json:"doc"`
	Attrs    []string `json:"attrs"`
	Children []string `json:"children"`
	Text     string   `json:"text"`
}

type catalog struct {
	Comment     string                `json:"comment"`
	Namespace   string                `json:"namespace"`
	Root        string                `json:"root"`
	Versions    []string              `json:"versions"`
	SimpleTypes map[string]simpleType `json:"simpleTypes"`
	Elements    []element             `json:"elements"`
}

type attr struct {
	name, typ string
	required  bool
	// present marks an attribute that must be written even when empty
	// (a trailing ~): required by the schema, but empty is a valid value.
	present bool
}

type child struct {
	name     string
	min, max int // max -1 unbounded
}

func parseAttr(s string) (attr, error) {
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return attr{}, fmt.Errorf("attribute %q: want name:type", s)
	}
	a := attr{name: s[:i], typ: s[i+1:]}
	switch {
	case strings.HasSuffix(a.typ, "!"):
		a.required = true
		a.typ = strings.TrimSuffix(a.typ, "!")
	case strings.HasSuffix(a.typ, "~"):
		a.present = true
		a.typ = strings.TrimSuffix(a.typ, "~")
	}
	return a, nil
}

func parseChild(s string) child {
	switch {
	case strings.HasSuffix(s, "?"):
		return child{strings.TrimSuffix(s, "?"), 0, 1}
	case strings.HasSuffix(s, "*"):
		return child{strings.TrimSuffix(s, "*"), 0, -1}
	case strings.HasSuffix(s, "+"):
		return child{strings.TrimSuffix(s, "+"), 1, -1}
	default:
		return child{s, 1, 1}
	}
}

// goName turns an XML name into an exported Go identifier.
func goName(s string) string {
	s = strings.TrimPrefix(s, "xml:")
	var b strings.Builder
	up := true
	for _, r := range s {
		if r == '_' || r == '-' || r == '.' || r == ':' {
			up = true
			continue
		}
		if up && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		up = false
		b.WriteRune(r)
	}
	// "Id" as a word (TextId, VendorId) becomes "ID"; "Identity" stays.
	return idWord.ReplaceAllString(b.String(), "ID$1")
}

var idWord = regexp.MustCompile(`Id([A-Z]|$)`)

func load(path string) (*catalog, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var c catalog
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, raw, c.check()
}

// check validates the catalog: every child and type is defined, names are
// unique, patterns compile.
func (c *catalog) check() error {
	names := map[string]bool{}
	for _, e := range c.Elements {
		if names[e.Name] {
			return fmt.Errorf("element %s defined twice", e.Name)
		}
		names[e.Name] = true
	}
	if !names[c.Root] {
		return fmt.Errorf("root %s is not defined", c.Root)
	}
	for n, t := range c.SimpleTypes {
		if t.Pattern != "" {
			if _, err := regexp.Compile(t.Pattern); err != nil {
				return fmt.Errorf("type %s: %w", n, err)
			}
		}
	}
	for _, e := range c.Elements {
		seen := map[string]bool{}
		for _, s := range e.Attrs {
			a, err := parseAttr(s)
			if err != nil {
				return fmt.Errorf("element %s: %w", e.Name, err)
			}
			if _, ok := c.SimpleTypes[a.typ]; !ok {
				return fmt.Errorf("element %s attribute %s: unknown type %s", e.Name, a.name, a.typ)
			}
			if seen[a.name] {
				return fmt.Errorf("element %s: attribute %s twice", e.Name, a.name)
			}
			seen[a.name] = true
		}
		for _, s := range e.Children {
			ch := parseChild(s)
			if !names[ch.name] {
				return fmt.Errorf("element %s: child %s is not defined", e.Name, ch.name)
			}
		}
		if e.Text != "" && len(e.Children) > 0 {
			return fmt.Errorf("element %s: text and children", e.Name)
		}
	}
	return nil
}

func generateGo(c *catalog, raw []byte) ([]byte, error) {
	var b bytes.Buffer
	sum := sha256.Sum256(raw)
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	p("// Code generated by internal/cmd/gengsdml from gsdml/schema/catalog.json; DO NOT EDIT.\n\n")
	p("// SPDX-License-Identifier: MIT\n\n")
	p("package gsdml\n\n")
	p("import (\n\t\"encoding/xml\"\n\t\"regexp\"\n)\n\n")
	p("// CatalogSHA256 is the SHA-256 of the catalog the types were generated from.\n")
	p("const CatalogSHA256 = %q\n\n", fmt.Sprintf("%x", sum))
	p("// Namespace is the XML namespace of GSDML documents.\n")
	p("const Namespace = %q\n\n", c.Namespace)
	p("// SchemaVersions lists the GSDML schema versions the catalog was written for.\n")
	p("var SchemaVersions = []string{")
	for i, v := range c.Versions {
		if i > 0 {
			p(", ")
		}
		p("%q", v)
	}
	p("}\n\n")

	// Simple types.
	tnames := make([]string, 0, len(c.SimpleTypes))
	for n := range c.SimpleTypes {
		tnames = append(tnames, n)
	}
	sort.Strings(tnames)
	p("// simpleTypes holds the lexical rules of the catalog's attribute types.\n")
	p("var simpleTypes = map[string]simpleType{\n")
	for _, n := range tnames {
		t := c.SimpleTypes[n]
		p("\t%q: {doc: %q", n, t.Doc)
		if t.Pattern != "" {
			p(", pattern: regexp.MustCompile(%q)", t.Pattern)
		}
		if len(t.Enum) > 0 {
			p(", enum: map[string]bool{")
			for i, v := range t.Enum {
				if i > 0 {
					p(", ")
				}
				p("%q: true", v)
			}
			p("}")
		}
		if t.Uint > 0 {
			p(", uintBits: %d", t.Uint)
		}
		p("},\n")
	}
	p("}\n\n")

	// DataType constants.
	if dt, ok := c.SimpleTypes["DataType"]; ok {
		p("// DataType is the data type of a DataItem or a Ref.\ntype DataType string\n\n")
		p("// Data types.\nconst (\n")
		for _, v := range dt.Enum {
			p("\tDataType%s DataType = %q\n", goName(strings.NewReplacer("+", "Plus", " ", "_", ".", "_").Replace(v)), v)
		}
		p(")\n\n")
		p("// DataTypes lists every DataType value.\nvar DataTypes = []DataType{")
		for i, v := range dt.Enum {
			if i > 0 {
				p(", ")
			}
			p("%q", v)
		}
		p("}\n\n")
	}

	for _, e := range c.Elements {
		gn := goName(e.Name)
		if e.Doc != "" {
			p("// %s is the GSDML element %s: %s\n", gn, e.Name, lowerFirst(e.Doc))
		} else {
			p("// %s is the GSDML element %s.\n", gn, e.Name)
		}
		p("type %s struct {\n", gn)
		if e.Name == c.Root {
			p("\tXMLName xml.Name `xml:\"%s %s\"`\n", c.Namespace, e.Name)
		} else {
			p("\tXMLName xml.Name `xml:\"%s\"`\n", e.Name)
		}
		for _, s := range e.Attrs {
			a, _ := parseAttr(s)
			tag := a.name
			if strings.HasPrefix(a.name, "xml:") {
				tag = "http://www.w3.org/XML/1998/namespace " + strings.TrimPrefix(a.name, "xml:")
			}
			omit := ",omitempty"
			if a.required || a.present {
				omit = ""
			}
			req := ""
			switch {
			case a.required:
				req = ", required"
			case a.present:
				req = ", always written, may be empty"
			}
			p("\t// %s is the attribute %s (%s%s).\n", goName(a.name), a.name, a.typ, req)
			p("\t%s string `xml:\"%s,attr%s\"`\n", goName(a.name), tag, omit)
		}
		if e.Text != "" {
			p("\t// Value is the text content.\n\tValue string `xml:\",chardata\"`\n")
		}
		for _, s := range e.Children {
			ch := parseChild(s)
			cn := goName(ch.name)
			if ch.max == 1 {
				p("\t%s *%s `xml:\"%s\"`\n", cn, cn, ch.name)
			} else {
				p("\t%s []*%s `xml:\"%s\"`\n", plural(cn), cn, ch.name)
			}
		}
		p("\t// Attrs holds the attributes the catalog does not define, preserved\n\t// as they were read.\n")
		p("\tAttrs []xml.Attr `xml:\",any,attr\"`\n")
		if e.Text == "" {
			p("\t// Unknown holds the child elements the catalog does not define,\n\t// preserved (and written after the known ones).\n")
			p("\tUnknown []*Node `xml:\",any\"`\n")
		}
		p("}\n\n")

		// validate
		p("func (x *%s) validate(v *validator) {\n", gn)
		p("\tif x == nil {\n\t\treturn\n\t}\n")
		p("\tv.push(%q)\n\tdefer v.pop()\n", e.Name)
		for _, s := range e.Attrs {
			a, _ := parseAttr(s)
			p("\tv.attr(%q, x.%s, %q, %v)\n", a.name, goName(a.name), a.typ, a.required)
		}
		for _, s := range e.Children {
			ch := parseChild(s)
			cn := goName(ch.name)
			if ch.max == 1 {
				if ch.min == 1 {
					p("\tif x.%s == nil {\n\t\tv.missing(%q)\n\t}\n", cn, ch.name)
				}
				p("\tx.%s.validate(v)\n", cn)
			} else {
				if ch.min == 1 {
					p("\tif len(x.%s) == 0 {\n\t\tv.missing(%q)\n\t}\n", plural(cn), ch.name)
				}
				p("\tfor _, c := range x.%s {\n\t\tc.validate(v)\n\t}\n", plural(cn))
			}
		}
		p("\tv.unknownAttrs(x.Attrs)\n")
		if e.Text == "" {
			p("\tv.unknown(x.Unknown)\n")
		}
		p("}\n\n")
	}
	src, err := format.Source(b.Bytes())
	if err != nil {
		return b.Bytes(), fmt.Errorf("format: %w", err)
	}
	return src, nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	if len(s) > 1 && s[1] >= 'A' && s[1] <= 'Z' {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func plural(s string) string {
	switch {
	case strings.HasSuffix(s, "y") && !strings.HasSuffix(s, "ay"):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s"):
		return s + "es"
	default:
		return s + "s"
	}
}

func generateDoc(c *catalog) []byte {
	var b bytes.Buffer
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	p("<!-- Code generated by internal/cmd/gengsdml from gsdml/schema/catalog.json; DO NOT EDIT. -->\n\n")
	p("# GSDML catalog\n\n")
	p("The GSDML elements and attributes package `gsdml` models, generated from\n")
	p("[schema/catalog.json](schema/catalog.json). Namespace `%s`; written for\n", c.Namespace)
	p("schema versions %s. Attributes marked **required** must be present and\n", strings.Join(c.Versions, ", "))
	p("not empty, \"required, may be empty\" ones are always written;\n")
	p("children marked `?` are optional, `*` repeated, `+` one or more. Elements\n")
	p("and attributes a document has beyond these are kept and written back.\n\n")
	p("## Attribute types\n\n| Type | Rule |\n|------|------|\n")
	names := make([]string, 0, len(c.SimpleTypes))
	for n := range c.SimpleTypes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t := c.SimpleTypes[n]
		rule := t.Doc
		if len(t.Enum) > 0 && len(t.Enum) <= 8 {
			rule += " One of: `" + strings.Join(t.Enum, "`, `") + "`."
		} else if len(t.Enum) > 8 {
			rule += fmt.Sprintf(" %d values.", len(t.Enum))
		}
		p("| `%s` | %s |\n", n, rule)
	}
	p("\n## Elements\n\n")
	for _, e := range c.Elements {
		p("### %s\n\n", e.Name)
		if e.Doc != "" {
			p("%s\n\n", e.Doc)
		}
		if e.Text != "" {
			p("Text content (`%s`).\n\n", e.Text)
		}
		if len(e.Attrs) > 0 {
			p("| Attribute | Type | |\n|-----------|------|---|\n")
			for _, s := range e.Attrs {
				a, _ := parseAttr(s)
				r := ""
				if a.required {
					r = "**required**"
				} else if a.present {
					r = "required, may be empty"
				}
				p("| `%s` | `%s` | %s |\n", a.name, a.typ, r)
			}
			p("\n")
		}
		if len(e.Children) > 0 {
			p("Children: ")
			for i, s := range e.Children {
				if i > 0 {
					p(", ")
				}
				ch := parseChild(s)
				suffix := strings.TrimPrefix(s, ch.name)
				p("[%s](#%s)%s", ch.name, strings.ToLower(ch.name), suffix)
			}
			p("\n\n")
		}
	}
	return b.Bytes()
}

func main() {
	root := flag.String("root", ".", "module root")
	check := flag.Bool("check", false, "fail when the generated files are out of date")
	xsdDir := flag.String("xsd", "", "compare the catalog with the GSDML XSDs in this directory")
	flag.Parse()
	c, raw, err := load(filepath.Join(*root, "gsdml", "schema", "catalog.json"))
	if err != nil {
		fail(err)
	}
	if *xsdDir != "" {
		if err := compareXSD(c, *xsdDir); err != nil {
			fail(err)
		}
		return
	}
	src, err := generateGo(c, raw)
	if err != nil {
		fail(err)
	}
	outputs := map[string][]byte{
		filepath.Join(*root, "gsdml", "zz_generated.go"): src,
		filepath.Join(*root, "gsdml", "SCHEMA.md"):       generateDoc(c),
	}
	stale := false
	for path, want := range outputs {
		if *check {
			have, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(have, want) {
				fmt.Fprintf(os.Stderr, "gengsdml: %s is out of date; run go generate ./gsdml\n", path)
				stale = true
			}
			continue
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			fail(err)
		}
	}
	if stale {
		os.Exit(1)
	}
}

// compareXSD reports the differences between the catalog and the element
// and attribute declarations of the XSDs in dir.
func compareXSD(c *catalog, dir string) error {
	s, err := xsd.LoadDir(dir)
	if err != nil {
		return err
	}
	diffs := 0
	for _, e := range c.Elements {
		decl, ok := s.Elements[e.Name]
		if !ok {
			fmt.Printf("element %s: not declared in the schema\n", e.Name)
			diffs++
			continue
		}
		for _, sa := range e.Attrs {
			a, _ := parseAttr(sa)
			name := strings.TrimPrefix(a.name, "xml:")
			info, ok := decl.Attributes[name]
			if !ok {
				fmt.Printf("element %s attribute %s: not declared in the schema\n", e.Name, a.name)
				diffs++
				continue
			}
			if info.Required != (a.required || a.present) {
				fmt.Printf("element %s attribute %s: required %v in the catalog, %v in the schema\n", e.Name, a.name, a.required || a.present, info.Required)
				diffs++
			}
		}
		known := map[string]bool{}
		for _, sa := range e.Attrs {
			a, _ := parseAttr(sa)
			known[strings.TrimPrefix(a.name, "xml:")] = true
		}
		var missing []string
		for n := range decl.Attributes {
			if !known[n] {
				missing = append(missing, n)
			}
		}
		sort.Strings(missing)
		for _, n := range missing {
			fmt.Printf("element %s attribute %s: in the schema, not in the catalog (preserved, not modelled)\n", e.Name, n)
		}
		kids := map[string]bool{}
		for _, ch := range e.Children {
			kids[parseChild(ch).name] = true
		}
		var extra []string
		for n := range decl.Children {
			if !kids[n] {
				extra = append(extra, n)
			}
		}
		sort.Strings(extra)
		for _, n := range extra {
			fmt.Printf("element %s child %s: in the schema, not in the catalog (preserved, not modelled)\n", e.Name, n)
		}
	}
	fmt.Printf("%d differences that need attention\n", diffs)
	if diffs > 0 {
		return fmt.Errorf("the catalog disagrees with the schema in %d places", diffs)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gengsdml:", err)
	os.Exit(1)
}
