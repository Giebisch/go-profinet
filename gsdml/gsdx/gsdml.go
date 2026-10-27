// SPDX-License-Identifier: MIT

package gsdx

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/gsdml"
)

// IsGSDML reports whether a part looks like a GSDML document: its content
// type names GSDML, or its file name follows the GSDML naming convention
// ("GSDML-...xml"). The content type PI's GSDX specification assigns is
// members-only, so this is a heuristic, not a rule of that specification.
func IsGSDML(part *Part) bool {
	if strings.Contains(asciiLower(part.ContentType), "gsdml") {
		return true
	}
	base := asciiLower(path.Base(part.Name))
	return strings.HasPrefix(base, "gsdml-") && strings.HasSuffix(base, ".xml")
}

// GSDMLParts returns the names of the parts IsGSDML selects, in ZIP order.
func (p *Package) GSDMLParts() []string {
	var out []string
	for _, part := range p.Parts {
		if IsGSDML(part) {
			out = append(out, part.Name)
		}
	}
	return out
}

// GSDML returns a copy of the content of a GSDML part. It fails with
// profinet.ErrNotFound when there is no such part or IsGSDML does not
// select it.
func (p *Package) GSDML(name string) ([]byte, error) {
	part, ok := p.Part(name)
	if !ok || !IsGSDML(part) {
		return nil, fmt.Errorf("gsdx: %w: %s: %w", profinet.ErrNotFound, name, errNotGSDML)
	}
	return p.ReadPart(part.Name)
}

// CheckGSDML parses a GSDML part with gsdml.Parse and checks it with
// gsdml.Check: go-profinet's catalog of the schema and its semantic rules,
// not PI's GSDML checker. The error is that of reading or parsing; the
// findings of the check are in the Report.
func (p *Package) CheckGSDML(name string) (*gsdml.Document, gsdml.Report, error) {
	data, err := p.GSDML(name)
	if err != nil {
		return nil, gsdml.Report{}, err
	}
	doc, err := gsdml.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, gsdml.Report{}, err
	}
	return doc, gsdml.Check(doc), nil
}
