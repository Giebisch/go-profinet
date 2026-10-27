// SPDX-License-Identifier: MIT

package gsdx

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
)

func hasIssue(p *Package, part, substr string) bool {
	for _, i := range p.Issues {
		if (part == "" || i.Part == part) && strings.Contains(i.Message, substr) {
			return true
		}
	}
	return false
}

func mustOpen(t *testing.T, ents ...ent) *Package {
	t.Helper()
	p, err := openBytes(zipOf(t, ents...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenFixture(t *testing.T) {
	p := mustOpen(t, fixture(t)...)
	if len(p.Parts) != 12 {
		t.Fatalf("%d parts, want 12 (no [Content_Types].xml, no folder)", len(p.Parts))
	}
	want := map[string]string{
		"/docProps/core.xml":                  ContentTypeCoreProperties,
		"/_rels/.rels":                        ContentTypeRelationships,
		gsdmlName:                             "application/xml",
		"/DATA/BLOB.BIN":                      "application/octet-stream", // case-insensitive lookup
		"/_xmlsignatures/origin.sigs":         ContentTypeSignatureOrigin,
		"/_xmlsignatures/sig1.xml":            ContentTypeSignature,
		"/_xmlsignatures/certs/signer.cer":    ContentTypeCertificate,
		"/_xmlsignatures/_rels/sig1.xml.rels": ContentTypeRelationships,
	}
	for name, ct := range want {
		part, ok := p.Part(name)
		if !ok || part.ContentType != ct {
			t.Errorf("%s: %+v, want content type %s", name, part, ct)
		}
	}
	blob, _ := p.Part("/data/blob.bin")
	if blob.Method != zip.Store || blob.UncompressedSize != uint64(len(blobData)) || blob.CRC32 != crc32.ChecksumIEEE([]byte(blobData)) {
		t.Errorf("blob: %+v", blob)
	}
	if len(p.Issues) != 0 {
		t.Errorf("issues: %v", p.Issues)
	}

	// Relationships: package, relative with fragment, cycle, external.
	if len(p.Relationships) != 4 || p.Relationships[0].ResolvedTarget != "/docProps/core.xml" || p.Relationships[0].Source != "/" {
		t.Fatalf("package relationships: %+v", p.Relationships)
	}
	if r := p.Relationships[3]; r.TargetMode != TargetModeExternal || r.ResolvedTarget != "" {
		t.Errorf("external: %+v", r)
	}
	g, _ := p.Part(gsdmlName)
	if len(g.Relationships) != 1 || g.Relationships[0].ResolvedTarget != "/data/blob.bin" || g.Relationships[0].Source != gsdmlName {
		t.Errorf("GSDML relationships: %+v", g.Relationships)
	}
	if blob.Relationships[0].ResolvedTarget != gsdmlName || blob.Relationships[0].TargetMode != TargetModeInternal {
		t.Errorf("blob relationships: %+v", blob.Relationships)
	}
	if got := p.Unreachable(); !reflect.DeepEqual(got, []string{"/orphan.bin"}) {
		t.Errorf("Unreachable = %v", got)
	}

	cp := p.CoreProperties
	if cp == nil || cp.Title != "OTF test device" || cp.Creator != "go-profinet tests" || cp.Created != "2026-10-27T12:00:00Z" ||
		cp.Modified != "2026-10-27T13:00:00Z" || cp.Version != "1.0" || cp.Keywords != "" || cp.Part != "/docProps/core.xml" {
		t.Errorf("core properties: %+v", cp)
	}

	if len(p.Signatures) != 1 {
		t.Fatalf("%d signatures", len(p.Signatures))
	}
	s := p.Signatures[0]
	if s.Verified || s.Reason != NotVerifiedReason || s.ID != "idPackageSignature" || s.Part != "/_xmlsignatures/sig1.xml" {
		t.Errorf("signature: %+v", s)
	}
	if s.SignatureTime != "2026-10-27T12:00:00Z" || s.SignatureTimeFormat != "YYYY-MM-DDThh:mm:ssTZD" {
		t.Errorf("signature time %q %q", s.SignatureTime, s.SignatureTimeFormat)
	}
	if !strings.Contains(s.CanonicalizationMethod, "c14n") || !strings.Contains(s.SignatureMethod, "ecdsa-sha256") {
		t.Errorf("methods %q %q", s.CanonicalizationMethod, s.SignatureMethod)
	}
	if len(s.SignedInfoReferences) != 1 || s.SignedInfoReferences[0].URI != "#idPackageObject" || s.SignedInfoReferences[0].Digest != DigestNotChecked {
		t.Errorf("SignedInfo references: %+v", s.SignedInfoReferences)
	}
	refs := s.PartReferences
	if len(refs) != 3 {
		t.Fatalf("part references: %+v", refs)
	}
	if refs[0].PartName != gsdmlName || refs[0].ContentType != "application/xml" || refs[0].Digest != DigestMatch || refs[0].ObjectID != "idPackageObject" {
		t.Errorf("GSDML reference: %+v", refs[0])
	}
	if refs[1].Digest != DigestMismatch || refs[1].DigestMethod != DigestSHA1 {
		t.Errorf("blob reference: %+v", refs[1])
	}
	if refs[2].Digest != DigestNotChecked || len(refs[2].Transforms) != 2 || !strings.Contains(refs[2].DigestNote, "transforms") {
		t.Errorf("relationships reference: %+v", refs[2])
	}
	if len(s.Certificates) != 2 {
		t.Fatalf("certificates: %+v", s.Certificates)
	}
	for i, c := range s.Certificates {
		if c.Error != "" || !strings.Contains(c.Subject, "CN=OTF test signer") || c.Subject != c.Issuer || c.SerialNumber != "2a" ||
			c.NotBefore.Year() != 2026 || c.NotAfter.Year() != 2027 || len(c.SHA256) != 64 {
			t.Errorf("certificate %d: %+v", i, c)
		}
	}
	if s.Certificates[0].Source != "KeyInfo" || s.Certificates[1].Source != "/_xmlsignatures/certs/signer.cer" {
		t.Errorf("certificate sources %q %q", s.Certificates[0].Source, s.Certificates[1].Source)
	}

	// GSDML discovery.
	if got := p.GSDMLParts(); !reflect.DeepEqual(got, []string{gsdmlName}) {
		t.Fatalf("GSDMLParts = %v", got)
	}
	b, err := p.GSDML(gsdmlName)
	if err != nil || !bytes.Equal(b, gsdmlFixture(t)) {
		t.Fatalf("GSDML: %v", err)
	}
	b[0] = 'X' // a copy
	if again, _ := p.GSDML(gsdmlName); again[0] == 'X' {
		t.Error("GSDML returned the package's own buffer")
	}
	doc, rep, err := p.CheckGSDML(gsdmlName)
	if err != nil || doc == nil || rep.Err() != nil {
		t.Fatalf("CheckGSDML: %v %v", err, rep.Err())
	}
	if _, err := p.GSDML("/data/blob.bin"); !errors.Is(err, profinet.ErrNotFound) {
		t.Errorf("GSDML of a non-GSDML part: %v", err)
	}
	if _, _, err := p.CheckGSDML("/nope.xml"); !errors.Is(err, profinet.ErrNotFound) {
		t.Errorf("CheckGSDML of a missing part: %v", err)
	}
	if _, err := p.ReadPart("/nope"); !errors.Is(err, profinet.ErrNotFound) {
		t.Errorf("ReadPart: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	ents := fixture(t)
	in := zipOf(t, ents...)
	p, err := openBytes(in)
	if err != nil {
		t.Fatal(err)
	}
	p.Comment = "kept"
	var out bytes.Buffer
	n, err := p.WriteTo(&out)
	if err != nil || n != int64(out.Len()) {
		t.Fatalf("WriteTo: %d %v", n, err)
	}
	zin, _ := zip.NewReader(bytes.NewReader(in), int64(len(in)))
	zout, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zin.File) != len(zout.File) || zout.Comment != "kept" {
		t.Fatalf("%d entries in, %d out, comment %q", len(zin.File), len(zout.File), zout.Comment)
	}
	for i, fi := range zin.File {
		fo := zout.File[i]
		if fi.Name != fo.Name || fi.Method != fo.Method || fi.CRC32 != fo.CRC32 || fi.UncompressedSize64 != fo.UncompressedSize64 ||
			!fi.Modified.Equal(fo.Modified) {
			t.Errorf("entry %d: %+v != %+v", i, fi.FileHeader, fo.FileHeader)
		}
		if read(t, fi) != read(t, fo) {
			t.Errorf("entry %s: content differs", fi.Name)
		}
	}
	p2, err := openBytes(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	p.entries, p2.entries, p.byName, p2.byName = nil, nil, nil, nil
	if !reflect.DeepEqual(p, p2) {
		t.Error("the written package does not open to the same model")
	}
}

func read(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	var b bytes.Buffer
	if _, err := b.ReadFrom(rc); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

type failWriter struct{ after int }

func (w *failWriter) Write(b []byte) (int, error) {
	if w.after <= 0 {
		return 0, errors.New("disk full")
	}
	w.after--
	return len(b), nil
}

func TestWriteToErrors(t *testing.T) {
	p := mustOpen(t, fixture(t)...)
	p.Comment = "x"
	if _, err := p.WriteTo(&failWriter{}); err == nil {
		t.Fatal("no error with a failing writer")
	}
	for after := 1; after < 100; after++ {
		if _, err := p.WriteTo(&failWriter{after: after}); err == nil {
			break
		}
	}
	p.Comment = strings.Repeat("c", 70000) // too long for a ZIP comment
	if _, err := p.WriteTo(&bytes.Buffer{}); err == nil {
		t.Error("no error for a comment longer than 65535 octets")
	}
}

func TestOpenFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.gsdx")
	if err := os.WriteFile(name, zipOf(t, minimal()...), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := OpenFile(name)
	if err != nil || len(p.Parts) != 2 {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := OpenFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("no error for a missing file")
	}
	if _, err := OpenFile(t.TempDir()); err == nil {
		t.Error("no error for a directory")
	}
}

func TestHostileNames(t *testing.T) {
	for _, name := range []string{
		"/abs.xml", "../x.xml", "a/../b.xml", "a//b.xml", "./a.xml", "a/.", `a\b.xml`, "C:/x.xml",
		"a/%2e%2E/b.xml", "a/%2e/b.xml", "a%2Fb.xml", "a%5cb.xml", "a\x00b", "a\nb", "a\x7fb",
	} {
		_, err := openBytes(zipOf(t, minimal(ent{name: name, data: []byte("x")})...))
		if !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%q: %v, want ErrMalformed", name, err)
		}
	}
	for _, dup := range [][2]string{{"x.xml", "X.XML"}, {"x.xml", "x.xml"}, {"d/", "D/"}} {
		_, err := openBytes(zipOf(t, minimal(ent{name: dup[0]}, ent{name: dup[1]})...))
		if !errors.Is(err, profinet.ErrMalformed) || !strings.Contains(err.Error(), "equivalent") {
			t.Errorf("%v: %v", dup, err)
		}
	}
	// An empty name.
	if _, err := openBytes(zipOf(t, minimal(ent{name: ""})...)); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("empty name: %v", err)
	}
	// A folder entry with content.
	b := raw(t, zip.FileHeader{Name: "d/", Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte("x")), CompressedSize64: 1, UncompressedSize64: 1}, []byte("x"))
	if _, err := openBytes(b); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("folder with content: %v", err)
	}
}

func TestNameIssues(t *testing.T) {
	cases := map[string]string{
		"a./b.xml":               "ends with",
		"a%zz.xml":               "malformed percent",
		"a%4.xml":                "malformed percent",
		"a%41.xml":               "unreserved",
		"caf\u00e9.xml":          "pchar",
		"a b.xml":                "pchar",
		"big.xml/[0].piece":      "interleaved",
		"big.xml/[1].last.piece": "interleaved",
	}
	for name, msg := range cases {
		p := mustOpen(t, minimal(ent{name: name, data: []byte("x")})...)
		if !hasIssue(p, "/"+name, msg) {
			t.Errorf("%q: issues %v, want %q", name, p.Issues, msg)
		}
	}
	p := mustOpen(t, minimal(ent{name: "ok-_~!$&'()*+,;=:@%20.xml", data: []byte("x")})...)
	if len(p.Issues) != 0 {
		t.Errorf("issues for a valid name: %v", p.Issues)
	}
}

func TestLimits(t *testing.T) {
	bomb := make([]byte, 4<<20) // zeros: deflate ratio around 1000
	_, err := openBytes(zipOf(t, minimal(ent{name: "bomb.bin", data: bomb})...))
	if !errors.Is(err, profinet.ErrResourceExhausted) || !strings.Contains(err.Error(), "ratio") {
		t.Errorf("bomb: %v", err)
	}
	// Stored, the same data passes the ratio but not a smaller part limit.
	b := zipOf(t, minimal(ent{name: "big.bin", data: bomb, method: 1})...)
	if _, err := Open(bytes.NewReader(b), int64(len(b)), Options{}); err != nil {
		t.Errorf("stored 4 MiB: %v", err)
	}
	if _, err := Open(bytes.NewReader(b), int64(len(b)), Options{MaxPartSize: 1 << 20}); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Errorf("part size: %v", err)
	}
	if _, err := Open(bytes.NewReader(b), int64(len(b)), Options{MaxTotalSize: 1 << 20}); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Errorf("total size: %v", err)
	}
	if _, err := Open(bytes.NewReader(b), int64(len(b)), Options{MaxParts: 2}); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Errorf("parts: %v", err)
	}
	if _, err := Open(bytes.NewReader(b), int64(len(b)), Options{MaxRatio: 1}); err != nil {
		t.Errorf("stored data has ratio 1: %v", err)
	}
}

// raw writes an entry with a header that lies.
func raw(t *testing.T, h zip.FileHeader, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range minimal() {
		w, _ := zw.Create(e.name)
		_, _ = w.Write(e.data)
	}
	w, err := zw.CreateRaw(&h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(payload)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLyingHeaders(t *testing.T) {
	data := []byte("0123456789abcdef")
	crc := crc32.ChecksumIEEE(data)
	// Declares fewer octets than it holds: read stops one octet later.
	b := raw(t, zip.FileHeader{Name: "x.bin", Method: zip.Store, CRC32: crc, CompressedSize64: 16, UncompressedSize64: 4}, data)
	if _, err := openBytes(b); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("short declared size: %v", err)
	}
	// A wrong CRC-32.
	b = raw(t, zip.FileHeader{Name: "x.bin", Method: zip.Store, CRC32: crc ^ 1, CompressedSize64: 16, UncompressedSize64: 16}, data)
	if _, err := openBytes(b); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("CRC: %v", err)
	}
	// A compression method archive/zip does not know.
	b = raw(t, zip.FileHeader{Name: "x.bin", Method: 99, CRC32: crc, CompressedSize64: 16, UncompressedSize64: 16}, data)
	if _, err := openBytes(b); !errors.Is(err, profinet.ErrUnsupported) {
		t.Errorf("method: %v", err)
	}
	// A truncated deflate stream.
	b = raw(t, zip.FileHeader{Name: "x.bin", Method: zip.Deflate, CRC32: crc, CompressedSize64: 3, UncompressedSize64: 16}, []byte{0x01, 0x02, 0x03})
	if _, err := openBytes(b); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("deflate: %v", err)
	}
	// A ratio claim with a zero compressed size.
	b = raw(t, zip.FileHeader{Name: "x.bin", Method: zip.Deflate, CRC32: crc, CompressedSize64: 0, UncompressedSize64: RatioFloor + 1}, nil)
	if _, err := openBytes(b); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Errorf("zero compressed size: %v", err)
	}
	// Corrupt local header offset: the entry cannot be opened.
	b = zipOf(t, minimal()...)
	off := bytes.LastIndex(b, []byte("PK\x01\x02")) + 42
	binary.LittleEndian.PutUint32(b[off:], 0xfffff)
	if _, err := openBytes(b); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("bad offset: %v", err)
	}
}

func TestNotAZip(t *testing.T) {
	if _, err := openBytes([]byte("not a zip")); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("%v", err)
	}
}

func TestContentTypes(t *testing.T) {
	if _, err := openBytes(zipOf(t, ent{name: "a.xml", data: []byte("<a/>")})); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("no content types: %v", err)
	}
	for _, bad := range []string{"<Types", "<Other/>"} {
		if _, err := openBytes(zipOf(t, ent{name: ContentTypesName, data: []byte(bad)})); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// Case-insensitive name of the content types stream, wrong namespace,
	// duplicates, empty content types, an override for a missing part, a
	// part without content type and one without extension.
	ct := `<Types xmlns="urn:wrong"><Default Extension="xml" ContentType="application/xml"/><Default Extension="XML" ContentType="text/xml"/>` +
		`<Default Extension="e" ContentType=""/>` +
		`<Override PartName="/a.xml" ContentType="text/plain"/><Override PartName="/A.XML" ContentType="x/y"/>` +
		`<Override PartName="/b.xml" ContentType=""/><Override PartName="/gone.xml" ContentType="x/y"/></Types>`
	p := mustOpen(t,
		ent{name: "[content_types].XML", data: []byte(ct)},
		ent{name: "a.xml"}, ent{name: "b.xml"}, ent{name: "c.dat"}, ent{name: "noext"},
	)
	for _, want := range [][2]string{
		{"", "namespace"}, {"", `two Default elements for extension "XML"`}, {"", `Default for extension "e" has no content type`},
		{"", `two Override elements for part "/A.XML"`}, {"/b.xml", "Override has no content type"},
		{"/gone.xml", "does not exist"}, {"/c.dat", "no content type"}, {"/noext", "no content type"},
	} {
		if !hasIssue(p, want[0], want[1]) {
			t.Errorf("missing issue %v in %v", want, p.Issues)
		}
	}
	if a, _ := p.Part("/a.xml"); a.ContentType != "text/plain" {
		t.Errorf("override: %q", a.ContentType)
	}
	if len(p.Defaults) != 3 || len(p.Overrides) != 4 {
		t.Errorf("defaults %v overrides %v", p.Defaults, p.Overrides)
	}
}

func TestRelationshipIssues(t *testing.T) {
	p := mustOpen(t, minimal(
		ent{name: "_rels/missing.xml.rels", data: rels()},
		ent{name: "_rels/_rels/.rels.rels", data: rels()},
		ent{name: "_rels/a.xml.rels", data: rels(
			[]string{"", "urn:t", "a.xml"},
			[]string{"d", "urn:t", "a.xml"},
			[]string{"d", "", "a.xml"},
			[]string{"m", "urn:t", "a.xml", "Sideways"},
			[]string{"abs", "urn:t", "http://example.com/x"},
			[]string{"bad", "urn:t", "%zz"},
			[]string{"empty", "urn:t", ""},
			[]string{"dangling", "urn:t", "nothere.xml"},
			[]string{"up", "urn:t", "../../a.xml"},
		)},
		ent{name: "b.bin"},
		ent{name: "_rels/b.bin.rels", data: []byte("<Relationships")},
		ent{name: "c.bin"},
		ent{name: "_rels/c.bin.rels", data: []byte(`<Wrong xmlns="urn:x"/>`)},
	)...)
	for _, want := range [][2]string{
		{"/_rels/missing.xml.rels", "does not exist"},
		{"/_rels/_rels/.rels.rels", "relationships part of a relationships part"},
		{"/_rels/a.xml.rels", "without Id"},
		{"/_rels/a.xml.rels", `two relationships with Id "d"`},
		{"/_rels/a.xml.rels", "without Type"},
		{"/_rels/a.xml.rels", `TargetMode "Sideways"`},
		{"/_rels/a.xml.rels", "absolute URI"},
		{"/_rels/a.xml.rels", `target "%zz" is not a URI`},
		{"/_rels/a.xml.rels", `target "" is not a URI`},
		{"/_rels/a.xml.rels", "target /nothere.xml does not exist"},
		{"/_rels/b.bin.rels", "EOF"},
		{"/_rels/c.bin.rels", "root element"},
	} {
		if !hasIssue(p, want[0], want[1]) {
			t.Errorf("missing issue %v in %v", want, p.Issues)
		}
	}
	a, _ := p.Part("/a.xml")
	if len(a.Relationships) != 9 || a.Relationships[8].ResolvedTarget != "/a.xml" {
		t.Errorf("relationships of a.xml: %+v", a.Relationships)
	}
	// A relationships part with the wrong content type.
	p = mustOpen(t,
		ent{name: ContentTypesName, data: contentTypes(map[string]string{"xml": "application/xml"}, [2]string{"/_rels/.rels", "text/plain"})},
		ent{name: "_rels/.rels", data: rels([]string{"r", "urn:t", "a.xml"})}, ent{name: "a.xml"})
	if !hasIssue(p, "/_rels/.rels", "relationships part with content type") {
		t.Errorf("issues %v", p.Issues)
	}
}

func TestRelationshipCycles(t *testing.T) {
	// a -> b -> c -> a, and a part pointing to itself: Unreachable ends.
	p := mustOpen(t,
		ent{name: ContentTypesName, data: contentTypes(baseDefaults)},
		ent{name: "_rels/.rels", data: rels([]string{"r", "urn:t", "a.xml"}, []string{"r2", "urn:t", "/a.xml"})},
		ent{name: "a.xml"}, ent{name: "_rels/a.xml.rels", data: rels([]string{"r", "urn:t", "b.xml"}, []string{"s", "urn:t", "a.xml"})},
		ent{name: "b.xml"}, ent{name: "_rels/b.xml.rels", data: rels([]string{"r", "urn:t", "c.xml"}, []string{"x", "urn:t", "nothere.xml"})},
		ent{name: "c.xml"}, ent{name: "_rels/c.xml.rels", data: rels([]string{"r", "urn:t", "a.xml"})},
		ent{name: "island.xml"}, ent{name: "_rels/island.xml.rels", data: rels([]string{"r", "urn:t", "island.xml"})},
	)
	if got := p.Unreachable(); !reflect.DeepEqual(got, []string{"/island.xml"}) {
		t.Errorf("Unreachable = %v", got)
	}
}

func TestCoreProperties(t *testing.T) {
	core := ent{name: "core.xml", data: []byte(`<cp:coreProperties xmlns:cp="urn:cp" xmlns:dc="urn:dc"><dc:title>T</dc:title><dc:subject>S`)}
	p := mustOpen(t,
		ent{name: ContentTypesName, data: contentTypes(baseDefaults)},
		ent{name: "_rels/.rels", data: rels(
			[]string{"a", relTypeCorePropertiesOffice, "core.xml"},
			[]string{"b", RelTypeCoreProperties, "core.xml"},
		)},
		core,
	)
	if p.CoreProperties == nil || p.CoreProperties.Title != "T" {
		t.Fatalf("%+v", p.CoreProperties)
	}
	for _, want := range []string{"2 core properties relationships", "core properties part with content type", "core properties: XML syntax error"} {
		if !hasIssue(p, "", want) {
			t.Errorf("missing issue %q in %v", want, p.Issues)
		}
	}
	// A relationship to a core properties part that does not exist.
	p = mustOpen(t,
		ent{name: ContentTypesName, data: contentTypes(baseDefaults)},
		ent{name: "_rels/.rels", data: rels([]string{"a", RelTypeCoreProperties, "core.xml"})},
	)
	if p.CoreProperties != nil {
		t.Errorf("%+v", p.CoreProperties)
	}
}

func TestIssueString(t *testing.T) {
	if s := (Issue{Message: "m"}).String(); s != "m" {
		t.Error(s)
	}
	if s := (Issue{Part: "/p", Message: "m"}).String(); s != "/p: m" {
		t.Error(s)
	}
}

func TestRelsNames(t *testing.T) {
	for source, rels := range map[string]string{"/": "/_rels/.rels", "/a/b.xml": "/a/_rels/b.xml.rels"} {
		if got := relsPartFor(source); got != rels {
			t.Errorf("relsPartFor(%s) = %s", source, got)
		}
		if got, ok := sourceOfRelsPart(rels); !ok || got != source {
			t.Errorf("sourceOfRelsPart(%s) = %s %v", rels, got, ok)
		}
	}
	for _, name := range []string{"/a/.rels", "/a/_rels/b.xml", "/a/_rels/.rels"} {
		if _, ok := sourceOfRelsPart(name); ok {
			t.Errorf("%s is not a relationships part", name)
		}
	}
	if extension("/a.b/c") != "" || extension("/a/b.XML") != "XML" {
		t.Error("extension")
	}
}

func TestSignatureIssues(t *testing.T) {
	cert := selfSigned(t)
	s512 := sha512.Sum512([]byte("<a/>"))
	s384 := sha512.Sum384([]byte("<a/>"))
	sig := `<Signature xmlns="http://www.w3.org/2000/09/xmldsig#"><SignedInfo/>
<KeyInfo><X509Data><X509Certificate>!!!</X509Certificate><X509Certificate>AAAA</X509Certificate></X509Data></KeyInfo>
<Object><Manifest>
<Reference URI="#frag"><DigestMethod Algorithm="` + DigestSHA1 + `"/><DigestValue>AAAA</DigestValue></Reference>
<Reference URI="/missing.xml"><DigestMethod Algorithm="` + DigestSHA1 + `"/><DigestValue>AAAA</DigestValue></Reference>
<Reference URI="/a.xml?ContentType=text/wrong#x"><DigestMethod Algorithm="urn:md5"/><DigestValue>AAAA</DigestValue></Reference>
<Reference URI="/a.xml"><DigestMethod Algorithm="` + DigestSHA256 + `"/><DigestValue>not base64!</DigestValue></Reference>
<Reference URI="/a.xml?%zz"><DigestMethod Algorithm="` + DigestSHA512 + `"/><DigestValue>` + b64(s512[:]) + `</DigestValue></Reference>
<Reference URI="/a.xml"><DigestMethod Algorithm="` + DigestSHA384 + `"/><DigestValue>` + b64(s384[:]) + `</DigestValue></Reference>
</Manifest></Object></Signature>`
	p := mustOpen(t,
		ent{name: ContentTypesName, data: contentTypes(baseDefaults)},
		ent{name: "_rels/.rels", data: rels(
			[]string{"o1", RelTypeSignatureOrigin, "origin.xml"},
			[]string{"o2", RelTypeSignatureOrigin, "origin2.xml"},
		)},
		ent{name: "a.xml", data: []byte("<a/>")},
		ent{name: "origin.xml"},
		ent{name: "_rels/origin.xml.rels", data: rels(
			[]string{"s1", RelTypeSignature, "s1.xml"},
			[]string{"s2", RelTypeSignature, "s2.xml"},
			[]string{"s3", RelTypeSignature, "s3.xml"},
			[]string{"s4", RelTypeSignature, "gone.xml"},
			[]string{"x", "urn:other", "a.xml"},
		)},
		ent{name: "s1.xml", data: []byte(sig)},
		ent{name: "_rels/s1.xml.rels", data: rels(
			[]string{"c1", RelTypeCertificate, "bad.cer"},
			[]string{"c2", RelTypeCertificate, "nothere.cer"},
		)},
		ent{name: "bad.cer", data: []byte("junk")},
		ent{name: "s2.xml", data: []byte("<Signature")},
		ent{name: "s3.xml", data: []byte(`<Signature xmlns="urn:x"/>`)},
	)
	for _, want := range []string{
		"2 digital signature origin relationships", "signature origin part with content type",
		"signature part with content type", "signed part /missing.xml does not exist",
		`reference says content type "text/wrong"`, "signature: XML syntax error", "root element {urn:x}Signature",
	} {
		if !hasIssue(p, "", want) {
			t.Errorf("missing issue %q in %v", want, p.Issues)
		}
	}
	if len(p.Signatures) != 3 {
		t.Fatalf("%d signatures", len(p.Signatures))
	}
	s := p.Signatures[0]
	notes := []string{"not a part reference", "does not exist", "not supported", "not base64", "", ""}
	digests := []DigestCheck{DigestNotChecked, DigestNotChecked, DigestNotChecked, DigestNotChecked, DigestMatch, DigestMatch}
	for i, r := range s.PartReferences {
		if !strings.Contains(r.DigestNote, notes[i]) || r.Digest != digests[i] {
			t.Errorf("reference %d: %+v", i, r)
		}
	}
	if len(s.Certificates) != 3 || !strings.HasPrefix(s.Certificates[0].Error, "base64") || s.Certificates[1].Error == "" || s.Certificates[2].Error == "" ||
		s.Certificates[2].Source != "/bad.cer" {
		t.Errorf("certificates: %+v", s.Certificates)
	}
	for _, s := range p.Signatures {
		if s.Verified || s.Reason != NotVerifiedReason {
			t.Errorf("%+v", s)
		}
	}
	_ = cert

	// An origin relationship to a part that does not exist.
	p = mustOpen(t,
		ent{name: ContentTypesName, data: contentTypes(baseDefaults)},
		ent{name: "_rels/.rels", data: rels([]string{"o1", RelTypeSignatureOrigin, "origin.xml"})},
	)
	if len(p.Signatures) != 0 {
		t.Error(p.Signatures)
	}
}

func TestGSDMLDiscovery(t *testing.T) {
	p := mustOpen(t,
		ent{name: ContentTypesName, data: contentTypes(baseDefaults, [2]string{"/dev.desc", "application/x-GSDML+xml"})},
		ent{name: "dev.desc", data: []byte("not xml")},
		ent{name: "gsdml-v2.35-x.XML", data: []byte("<x/>")},
		ent{name: "gsdml-notes.txt"},
		ent{name: "GSDML.xml"},
	)
	if got := p.GSDMLParts(); !reflect.DeepEqual(got, []string{"/dev.desc", "/gsdml-v2.35-x.XML"}) {
		t.Errorf("GSDMLParts = %v", got)
	}
	if _, _, err := p.CheckGSDML("/dev.desc"); !errors.Is(err, profinet.ErrMalformed) {
		t.Errorf("CheckGSDML of junk: %v", err)
	}
}
