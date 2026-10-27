// SPDX-License-Identifier: MIT

package gsdx

import (
	"bytes"
	"testing"
)

// FuzzOpen: no input panics, no limit is exceeded, and what opens writes
// out and opens again to the same parts and content.
func FuzzOpen(f *testing.F) {
	f.Add(zipOf(f, fixture(f)...))
	f.Add(zipOf(f, minimal()...))
	f.Add(zipOf(f, minimal(ent{name: "../x"})...))
	f.Add([]byte("PK\x05\x06"))
	opts := Options{MaxPartSize: 1 << 20, MaxTotalSize: 4 << 20}
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := Open(bytes.NewReader(b), int64(len(b)), opts)
		if err != nil {
			return
		}
		_ = p.Unreachable()
		for _, name := range p.GSDMLParts() {
			_, _, _ = p.CheckGSDML(name)
		}
		var out bytes.Buffer
		if _, err := p.WriteTo(&out); err != nil {
			t.Fatalf("WriteTo: %v", err)
		}
		p2, err := Open(bytes.NewReader(out.Bytes()), int64(out.Len()), opts)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if len(p2.Parts) != len(p.Parts) {
			t.Fatalf("%d parts, %d after a round trip", len(p.Parts), len(p2.Parts))
		}
		for i, part := range p.Parts {
			a, _ := p.ReadPart(part.Name)
			c, _ := p2.ReadPart(p2.Parts[i].Name)
			if part.Name != p2.Parts[i].Name || !bytes.Equal(a, c) {
				t.Fatalf("part %s changed in a round trip", part.Name)
			}
		}
	})
}
