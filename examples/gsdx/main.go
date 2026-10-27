// SPDX-License-Identifier: MIT

// Command gsdx inspects an OPC container holding a GSDML file: it builds a
// small package in a temporary directory (the example device's GSDML, a
// content types stream and a package relationship), opens it with gsdx,
// lists its parts and checks the GSDML part. gsdx is a generic container
// inspector: it applies none of PI's GSDX rules and verifies no signature.
package main

import (
	"archive/zip"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/gsdml"
	"github.com/otfabric/go-profinet/gsdml/gsdx"
)

func main() {
	doc, err := gsdml.FromModel(vnetenv.Model(), gsdml.GenerateOptions{})
	if err != nil {
		log.Fatal(err)
	}
	g, err := gsdml.Marshal(doc)
	if err != nil {
		log.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "gsdx-example")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	name := filepath.Join(dir, "example.gsdx")
	entries := []struct{ name, data string }{
		{gsdx.ContentTypesName, `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="` + gsdx.ContentTypeRelationships + `"/>` +
			`<Default Extension="xml" ContentType="application/xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="r1" Type="urn:example:gsdml" Target="GSDML/GSDML-V2.35-Example.xml"/></Relationships>`},
		{"GSDML/GSDML-V2.35-Example.xml", string(g)},
	}
	f, err := os.Create(name)
	if err != nil {
		log.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := w.Write([]byte(e.data)); err != nil {
			log.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}

	p, err := gsdx.OpenFile(name)
	if err != nil {
		log.Fatal(err)
	}
	for _, part := range p.Parts {
		fmt.Printf("part %s (%s, %d octets)\n", part.Name, part.ContentType, part.UncompressedSize)
	}
	fmt.Printf("%d OPC issue(s), %d signature(s)\n", len(p.Issues), len(p.Signatures))
	for _, n := range p.GSDMLParts() {
		_, rep, err := p.CheckGSDML(n)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("GSDML %s: %d error(s), %d warning(s) (go-profinet's catalog, not PI's checker)\n",
			n, rep.Count(gsdml.SeverityError), rep.Count(gsdml.SeverityWarning))
		if rep.Err() != nil {
			log.Fatal(rep.Err())
		}
	}
}
