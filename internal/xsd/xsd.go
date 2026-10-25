// SPDX-License-Identifier: MIT

// Package xsd reads the element and attribute declarations of a set of
// XML Schema files: which attributes each element name has (and whether
// they are required) and which child elements it may contain. It resolves
// named complex types, extension and restriction bases, attribute groups
// and model groups. It is not a validating schema processor; it serves
// the GSDML generator's comparison of its catalog with a schema.
package xsd

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Attr describes an attribute declaration.
type Attr struct {
	Required bool
	Type     string
}

// Element merges the declarations of every element with one name.
type Element struct {
	Name       string
	Attributes map[string]Attr
	Children   map[string]bool
}

// Schema is the merged view of the files.
type Schema struct {
	Elements map[string]*Element
}

type node struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []node     `xml:",any"`
}

func (n *node) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

const nsXSD = "http://www.w3.org/2001/XMLSchema"

func local(qname string) string {
	if i := strings.LastIndex(qname, ":"); i >= 0 {
		return qname[i+1:]
	}
	return qname
}

type loader struct {
	types      map[string]*node
	attrGroups map[string]*node
	groups     map[string]*node
	globals    map[string]*node
	out        *Schema
	visiting   map[*node]bool
}

// LoadDir reads every .xsd file of dir.
func LoadDir(dir string) (*Schema, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.xsd"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("xsd: no .xsd files in %s", dir)
	}
	sort.Strings(paths)
	var roots []*node
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		n, err := parse(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("xsd: %s: %w", p, err)
		}
		roots = append(roots, n)
	}
	return Load(roots...)
}

// Parse reads one schema document.
func Parse(r io.Reader) (*Schema, error) {
	n, err := parse(r)
	if err != nil {
		return nil, err
	}
	return Load(n)
}

func parse(r io.Reader) (*node, error) {
	var n node
	d := xml.NewDecoder(r)
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := d.Decode(&n); err != nil {
		return nil, err
	}
	if n.XMLName.Local != "schema" || n.XMLName.Space != nsXSD {
		return nil, fmt.Errorf("root element %s is not an XML Schema", n.XMLName.Local)
	}
	return &n, nil
}

// Load merges parsed schema documents.
func Load(roots ...*node) (*Schema, error) {
	l := &loader{
		types: map[string]*node{}, attrGroups: map[string]*node{}, groups: map[string]*node{}, globals: map[string]*node{},
		out: &Schema{Elements: map[string]*Element{}}, visiting: map[*node]bool{},
	}
	for _, r := range roots {
		for i := range r.Children {
			c := &r.Children[i]
			if c.XMLName.Space != nsXSD {
				continue
			}
			name := c.attr("name")
			switch c.XMLName.Local {
			case "complexType":
				l.types[name] = c
			case "attributeGroup":
				l.attrGroups[name] = c
			case "group":
				l.groups[name] = c
			case "element":
				l.globals[name] = c
			}
		}
	}
	names := make([]string, 0, len(l.globals))
	for n := range l.globals {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		l.element(l.globals[n])
	}
	return l.out, nil
}

func (l *loader) get(name string) *Element {
	e := l.out.Elements[name]
	if e == nil {
		e = &Element{Name: name, Attributes: map[string]Attr{}, Children: map[string]bool{}}
		l.out.Elements[name] = e
	}
	return e
}

// element records an element declaration (or reference).
func (l *loader) element(n *node) string {
	if ref := n.attr("ref"); ref != "" {
		g := l.globals[local(ref)]
		if g == nil {
			return local(ref)
		}
		return l.element(g)
	}
	name := n.attr("name")
	if name == "" {
		return ""
	}
	if l.visiting[n] {
		return name
	}
	l.visiting[n] = true
	e := l.get(name)
	if t := n.attr("type"); t != "" {
		if ct := l.types[local(t)]; ct != nil {
			l.complexType(ct, e)
		}
	}
	for i := range n.Children {
		c := &n.Children[i]
		if c.XMLName.Space == nsXSD && c.XMLName.Local == "complexType" {
			l.complexType(c, e)
		}
	}
	return name
}

// complexType adds the attributes and children of a complex type to e.
func (l *loader) complexType(n *node, e *Element) {
	for i := range n.Children {
		l.content(&n.Children[i], e)
	}
}

func (l *loader) content(c *node, e *Element) {
	if c.XMLName.Space != nsXSD {
		return
	}
	switch c.XMLName.Local {
	case "attribute":
		name := c.attr("name")
		if name == "" {
			name = local(c.attr("ref"))
		}
		if name != "" {
			e.Attributes[name] = Attr{Required: c.attr("use") == "required", Type: local(c.attr("type"))}
		}
	case "attributeGroup":
		if g := l.attrGroups[local(c.attr("ref"))]; g != nil {
			for i := range g.Children {
				l.content(&g.Children[i], e)
			}
		}
	case "group":
		if g := l.groups[local(c.attr("ref"))]; g != nil {
			for i := range g.Children {
				l.content(&g.Children[i], e)
			}
		}
	case "sequence", "choice", "all", "complexContent", "simpleContent":
		for i := range c.Children {
			l.content(&c.Children[i], e)
		}
	case "extension", "restriction":
		if base := l.types[local(c.attr("base"))]; base != nil && !l.visiting[base] {
			l.visiting[base] = true
			l.complexType(base, e)
			delete(l.visiting, base)
		}
		for i := range c.Children {
			l.content(&c.Children[i], e)
		}
	case "element":
		if name := l.element(c); name != "" {
			e.Children[name] = true
		}
	}
}
