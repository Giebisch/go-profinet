// SPDX-License-Identifier: MIT

package gsdx

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/url"
	"strings"
)

const (
	nsContentTypes  = "http://schemas.openxmlformats.org/package/2006/content-types"
	nsRelationships = "http://schemas.openxmlformats.org/package/2006/relationships"
)

// Default maps a file extension to a content type ([Content_Types].xml).
type Default struct {
	Extension   string `xml:"Extension,attr" json:"extension"`
	ContentType string `xml:"ContentType,attr" json:"contentType"`
}

// Override gives the content type of one part ([Content_Types].xml).
type Override struct {
	PartName    string `xml:"PartName,attr" json:"partName"`
	ContentType string `xml:"ContentType,attr" json:"contentType"`
}

type xmlTypes struct {
	XMLName   xml.Name
	Defaults  []Default  `xml:"Default"`
	Overrides []Override `xml:"Override"`
}

// readContentTypes parses [Content_Types].xml and resolves the content
// type of every part (Ecma-376 Part 2, ZIP mapping of content types: an
// Override for the part name wins over the Default for its extension;
// both compare ASCII case-insensitively).
func (p *Package) readContentTypes(data []byte) error {
	var t xmlTypes
	if err := xml.Unmarshal(data, &t); err != nil {
		return errMalformed("%s: %v", ContentTypesName, err)
	}
	if t.XMLName.Local != "Types" {
		return errMalformed("%s: root element %s, not Types", ContentTypesName, t.XMLName.Local)
	}
	if t.XMLName.Space != nsContentTypes {
		p.issue("", "%s: namespace %q, not %q", ContentTypesName, t.XMLName.Space, nsContentTypes)
	}
	p.Defaults, p.Overrides = t.Defaults, t.Overrides
	defaults := map[string]string{}
	for _, d := range t.Defaults {
		k := asciiLower(d.Extension)
		if _, dup := defaults[k]; dup {
			p.issue("", "%s: two Default elements for extension %q", ContentTypesName, d.Extension)
			continue
		}
		if d.ContentType == "" {
			p.issue("", "%s: Default for extension %q has no content type", ContentTypesName, d.Extension)
		}
		defaults[k] = d.ContentType
	}
	overrides := map[string]string{}
	for _, o := range t.Overrides {
		k := asciiLower(o.PartName)
		if _, dup := overrides[k]; dup {
			p.issue("", "%s: two Override elements for part %q", ContentTypesName, o.PartName)
			continue
		}
		if o.ContentType == "" {
			p.issue(o.PartName, "%s: Override has no content type", ContentTypesName)
		}
		overrides[k] = o.ContentType
		if _, ok := p.Part(o.PartName); !ok {
			p.issue(o.PartName, "%s: Override for a part that does not exist", ContentTypesName)
		}
	}
	for _, part := range p.Parts {
		if ct, ok := overrides[asciiLower(part.Name)]; ok {
			part.ContentType = ct
		} else if ext := extension(part.Name); ext != "" {
			part.ContentType = defaults[asciiLower(ext)]
		}
		if part.ContentType == "" {
			// Ecma-376 Part 2, "Content types": every part has one.
			p.issue(part.Name, "no content type: no Override for the part and no Default for its extension")
		}
	}
	return nil
}

// Target modes of a relationship.
const (
	TargetModeInternal = "Internal"
	TargetModeExternal = "External"
)

// Relationship is one relationship of a relationships part.
type Relationship struct {
	// Source is the part name of the source, "/" for the package.
	Source string `json:"source"`
	ID     string `json:"id"`
	Type   string `json:"type"`
	// Target is the target as written.
	Target string `json:"target"`
	// TargetMode is TargetModeInternal (also when the attribute is
	// absent) or TargetModeExternal, or what the file says otherwise.
	TargetMode string `json:"targetMode"`
	// ResolvedTarget is the part name an internal target resolves to,
	// relative to the source part (RFC 3986 reference resolution, as
	// Ecma-376 Part 2, "Resolving relative references", requires), with
	// any fragment removed. Empty for an external target or one that
	// cannot be resolved.
	ResolvedTarget string `json:"resolvedTarget,omitempty"`
}

type xmlRelationships struct {
	XMLName       xml.Name
	Relationships []struct {
		ID         string `xml:"Id,attr"`
		Type       string `xml:"Type,attr"`
		Target     string `xml:"Target,attr"`
		TargetMode string `xml:"TargetMode,attr"`
	} `xml:"Relationship"`
}

// readRelationships reads every relationships part and attaches the
// relationships to their source. Errors in a relationships part are
// reported as issues: the rest of the package can still be inspected.
func (p *Package) readRelationships() {
	for _, part := range p.Parts {
		source, ok := sourceOfRelsPart(part.Name)
		if !ok {
			continue
		}
		if part.ContentType != ContentTypeRelationships {
			p.issue(part.Name, "relationships part with content type %q", part.ContentType)
		}
		var src *Part
		if source != "/" {
			if src, ok = p.Part(source); !ok {
				p.issue(part.Name, "relationships part of %s, which does not exist", source)
				continue
			}
			if isRelsPart(src.Name) {
				// Ecma-376 Part 2, "Relationships": a relationships
				// part shall not have relationships.
				p.issue(part.Name, "relationships part of a relationships part")
				continue
			}
			source = src.Name
		}
		rels, err := p.parseRelationships(part.Name, source)
		if err != nil {
			p.issue(part.Name, "%v", err)
			continue
		}
		if src == nil {
			p.Relationships = rels
		} else {
			src.Relationships = rels
		}
	}
}

func (p *Package) parseRelationships(relsPart, source string) ([]Relationship, error) {
	data, _ := p.data(relsPart)
	var x xmlRelationships
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, err
	}
	if x.XMLName.Local != "Relationships" || x.XMLName.Space != nsRelationships {
		p.issue(relsPart, "root element {%s}%s, not {%s}Relationships", x.XMLName.Space, x.XMLName.Local, nsRelationships)
	}
	base, err := url.Parse(source)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	var out []Relationship
	for _, xr := range x.Relationships {
		r := Relationship{Source: source, ID: xr.ID, Type: xr.Type, Target: xr.Target, TargetMode: xr.TargetMode}
		if r.TargetMode == "" {
			r.TargetMode = TargetModeInternal
		}
		switch {
		case r.ID == "":
			p.issue(relsPart, "relationship without Id")
		case ids[r.ID]:
			p.issue(relsPart, "two relationships with Id %q", r.ID)
		}
		ids[r.ID] = true
		if r.Type == "" {
			p.issue(relsPart, "relationship %q without Type", r.ID)
		}
		switch r.TargetMode {
		case TargetModeInternal:
			r.ResolvedTarget = p.resolveTarget(relsPart, base, r)
		case TargetModeExternal:
		default:
			p.issue(relsPart, "relationship %q: TargetMode %q", r.ID, r.TargetMode)
		}
		out = append(out, r)
	}
	return out, nil
}

func (p *Package) resolveTarget(relsPart string, base *url.URL, r Relationship) string {
	ref, err := url.Parse(r.Target)
	if err != nil || r.Target == "" {
		p.issue(relsPart, "relationship %q: target %q is not a URI reference", r.ID, r.Target)
		return ""
	}
	if ref.Scheme != "" || ref.Host != "" {
		p.issue(relsPart, "relationship %q: internal target %q is an absolute URI", r.ID, r.Target)
		return ""
	}
	res := base.ResolveReference(ref)
	name := res.EscapedPath()
	if _, ok := p.Part(name); !ok {
		p.issue(relsPart, "relationship %q: target %s does not exist", r.ID, name)
	}
	return name
}

// CoreProperties is the core properties part (Ecma-376 Part 2, "Core
// properties"): the Dublin Core and OPC elements, as written.
type CoreProperties struct {
	Part           string `json:"part"`
	Title          string `json:"title,omitempty"`
	Subject        string `json:"subject,omitempty"`
	Creator        string `json:"creator,omitempty"`
	Keywords       string `json:"keywords,omitempty"`
	Description    string `json:"description,omitempty"`
	LastModifiedBy string `json:"lastModifiedBy,omitempty"`
	Revision       string `json:"revision,omitempty"`
	Version        string `json:"version,omitempty"`
	Category       string `json:"category,omitempty"`
	ContentStatus  string `json:"contentStatus,omitempty"`
	Identifier     string `json:"identifier,omitempty"`
	Language       string `json:"language,omitempty"`
	// Created, Modified and LastPrinted are W3CDTF dates as written.
	Created     string `json:"created,omitempty"`
	Modified    string `json:"modified,omitempty"`
	LastPrinted string `json:"lastPrinted,omitempty"`
}

func (p *Package) readCoreProperties() {
	var targets []string
	for _, r := range p.Relationships {
		if (r.Type == RelTypeCoreProperties || r.Type == relTypeCorePropertiesOffice) && r.ResolvedTarget != "" {
			targets = append(targets, r.ResolvedTarget)
		}
	}
	if len(targets) == 0 {
		return
	}
	if len(targets) > 1 {
		p.issue("/_rels/.rels", "%d core properties relationships, at most one is allowed", len(targets))
	}
	data, ok := p.data(targets[0])
	if !ok {
		return
	}
	part, _ := p.Part(targets[0])
	cp := &CoreProperties{Part: part.Name}
	if part.ContentType != ContentTypeCoreProperties {
		p.issue(part.Name, "core properties part with content type %q", part.ContentType)
	}
	fields := map[string]*string{
		"title": &cp.Title, "subject": &cp.Subject, "creator": &cp.Creator,
		"keywords": &cp.Keywords, "description": &cp.Description,
		"lastModifiedBy": &cp.LastModifiedBy, "revision": &cp.Revision,
		"version": &cp.Version, "category": &cp.Category,
		"contentStatus": &cp.ContentStatus, "identifier": &cp.Identifier,
		"language": &cp.Language, "created": &cp.Created,
		"modified": &cp.Modified, "lastPrinted": &cp.LastPrinted,
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	var cur *string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			p.issue(part.Name, "core properties: %v", err)
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			cur = nil
			if depth == 2 {
				cur = fields[t.Name.Local]
			}
		case xml.EndElement:
			depth--
			cur = nil
		case xml.CharData:
			if cur != nil {
				*cur += strings.TrimSpace(string(t))
			}
		}
	}
	p.CoreProperties = cp
}
