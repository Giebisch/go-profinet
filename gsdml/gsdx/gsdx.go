// SPDX-License-Identifier: MIT

// Package gsdx inspects GSDX containers: Open Packaging Conventions (OPC)
// packages, ISO/IEC 29500-2 / Ecma-376 Part 2, which are public.
//
// It is a generic, safe container inspector. It reads the ZIP under strict
// limits (part count, per-part and total uncompressed size, compression
// ratio), rejects hostile part names, and reports the package model: the
// parts with their content types, the package and part relationships, the
// core properties, the metadata of the OPC digital signatures and the parts
// that look like GSDML documents. WriteTo re-emits every part unchanged.
//
// What it does not do: the GSDX Container Specification of PROFIBUS &
// PROFINET International (PI order 2.752), its signing policy and MRCD are
// published to PI members only. This package applies no PI-specific rule,
// never says that a file is a valid GSDX, and does not verify signatures:
// see Signature. Rules of OPC itself that a package breaks without being
// dangerous to read are reported as Issues, not as errors.
//
// Clause references are to the requirement titles of Ecma-376 Part 2
// (Open Packaging Conventions); their numbers differ between editions.
package gsdx

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/otfabric/go-profinet"
)

// Default limits of Options. They are generous for a device description
// (a GSDML file is at most a few MB) and small enough that a hostile file
// cannot exhaust memory.
const (
	DefaultMaxParts     = 4096
	DefaultMaxPartSize  = 64 << 20
	DefaultMaxTotalSize = 256 << 20
	DefaultMaxRatio     = 200
	// RatioFloor is the uncompressed size under which the compression
	// ratio is not checked: small runs of repeated text compress far
	// beyond any sensible ratio without being a threat.
	RatioFloor = 64 << 10
)

// Options bound what Open reads. A zero field takes its default.
type Options struct {
	// MaxParts bounds the number of ZIP entries.
	MaxParts int
	// MaxPartSize bounds the uncompressed size of one entry.
	MaxPartSize int64
	// MaxTotalSize bounds the uncompressed size of all entries together.
	MaxTotalSize int64
	// MaxRatio bounds uncompressed/compressed size of an entry larger than
	// RatioFloor (a zip bomb has ratios in the thousands).
	MaxRatio int64
}

func (o Options) withDefaults() Options {
	if o.MaxParts <= 0 {
		o.MaxParts = DefaultMaxParts
	}
	if o.MaxPartSize <= 0 {
		o.MaxPartSize = DefaultMaxPartSize
	}
	if o.MaxTotalSize <= 0 {
		o.MaxTotalSize = DefaultMaxTotalSize
	}
	if o.MaxRatio <= 0 {
		o.MaxRatio = DefaultMaxRatio
	}
	return o
}

// Well-known names, content types and relationship types of OPC.
const (
	ContentTypesName = "[Content_Types].xml"

	ContentTypeRelationships   = "application/vnd.openxmlformats-package.relationships+xml"
	ContentTypeCoreProperties  = "application/vnd.openxmlformats-package.core-properties+xml"
	ContentTypeSignatureOrigin = "application/vnd.openxmlformats-package.digital-signature-origin"
	ContentTypeSignature       = "application/vnd.openxmlformats-package.digital-signature-xmlsignature+xml"
	ContentTypeCertificate     = "application/vnd.openxmlformats-package.digital-signature-certificate"

	RelTypeCoreProperties  = "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties"
	RelTypeSignatureOrigin = "http://schemas.openxmlformats.org/package/2006/relationships/digital-signature/origin"
	RelTypeSignature       = "http://schemas.openxmlformats.org/package/2006/relationships/digital-signature/signature"
	RelTypeCertificate     = "http://schemas.openxmlformats.org/package/2006/relationships/digital-signature/certificate"

	// relTypeCorePropertiesOffice is the core properties relationship type
	// Microsoft Office writes; consumers accept both.
	relTypeCorePropertiesOffice = "http://schemas.openxmlformats.org/officedocument/2006/relationships/metadata/core-properties"
)

// Part is one part of the package: a ZIP entry other than
// [Content_Types].xml and folder entries.
type Part struct {
	// Name is the part name: "/" followed by the ZIP item name.
	Name string `json:"name"`
	// ContentType is resolved from [Content_Types].xml: an Override for
	// the part name, else the Default for its extension, both compared
	// case-insensitively (Ecma-376 Part 2, "Content types" and the ZIP
	// mapping of content types). Empty when neither matches.
	ContentType string `json:"contentType"`
	// Method is the ZIP compression method (zip.Store or zip.Deflate).
	Method           uint16    `json:"method"`
	CompressedSize   uint64    `json:"compressedSize"`
	UncompressedSize uint64    `json:"uncompressedSize"`
	CRC32            uint32    `json:"crc32"`
	Modified         time.Time `json:"modified"`
	// Relationships are those whose source is this part, read from its
	// relationships part (<dir>/_rels/<name>.rels).
	Relationships []Relationship `json:"relationships,omitempty"`
}

// Issue is a rule of OPC the package breaks that does not stop it from
// being read.
type Issue struct {
	Part    string `json:"part,omitempty"`
	Message string `json:"message"`
}

func (i Issue) String() string {
	if i.Part == "" {
		return i.Message
	}
	return i.Part + ": " + i.Message
}

// entry is one ZIP entry as read, kept for WriteTo.
type entry struct {
	header zip.FileHeader
	data   []byte
	part   *Part // nil for [Content_Types].xml and folders
}

// Package is an opened OPC package. It holds every entry in memory (within
// the limits of Options) and does not keep the reader.
type Package struct {
	// Parts in ZIP order.
	Parts []*Part `json:"parts"`
	// Defaults and Overrides of [Content_Types].xml, as written.
	Defaults  []Default  `json:"defaults"`
	Overrides []Override `json:"overrides,omitempty"`
	// Relationships are the package relationships (_rels/.rels).
	Relationships []Relationship `json:"relationships,omitempty"`
	// CoreProperties is the core properties part, nil when there is none.
	CoreProperties *CoreProperties `json:"coreProperties,omitempty"`
	// Signatures are the OPC digital signatures, none verified.
	Signatures []*Signature `json:"signatures,omitempty"`
	// Comment is the ZIP archive comment.
	Comment string `json:"comment,omitempty"`
	// Issues are the OPC rules the package breaks.
	Issues []Issue `json:"issues,omitempty"`

	entries []*entry
	byName  map[string]*entry // ASCII-lowercased part name
}

func errMalformed(format string, a ...any) error {
	return fmt.Errorf("gsdx: %w: %s", profinet.ErrMalformed, fmt.Sprintf(format, a...))
}

func errLimit(format string, a ...any) error {
	return fmt.Errorf("gsdx: %w: %s", profinet.ErrResourceExhausted, fmt.Sprintf(format, a...))
}

func (p *Package) issue(part, format string, a ...any) {
	p.Issues = append(p.Issues, Issue{Part: part, Message: fmt.Sprintf(format, a...)})
}

// OpenFile opens the package in the file at path with the default Options.
func OpenFile(path string) (*Package, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return Open(f, st.Size(), Options{})
}

// Open reads the package from r. It returns an error wrapping
// profinet.ErrMalformed for input that is not a ZIP, has no
// [Content_Types].xml, has a part name that is absolute after mapping,
// contains a "." or ".." segment, an empty segment or a backslash, or two
// entries whose names are equal ignoring ASCII case; and
// profinet.ErrResourceExhausted when a limit of opts is exceeded. Every
// entry is decompressed and its CRC-32 checked.
func Open(r io.ReaderAt, size int64, opts Options) (*Package, error) {
	opts = opts.withDefaults()
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, errMalformed("%v", err)
	}
	if len(zr.File) > opts.MaxParts {
		return nil, errLimit("%d ZIP entries, at most %d", len(zr.File), opts.MaxParts)
	}
	p := &Package{Comment: zr.Comment, byName: map[string]*entry{}}
	seen := map[string]string{}
	var total int64
	var ctypes *entry
	for _, f := range zr.File {
		if err := checkItemName(f.Name); err != nil {
			return nil, err
		}
		folded := asciiLower(f.Name)
		if prev, ok := seen[folded]; ok {
			// Ecma-376 Part 2, "Part names": a package shall not
			// contain equivalent part names, and part name equivalence
			// is ASCII case-insensitive.
			return nil, errMalformed("ZIP entries %q and %q are equivalent names", prev, f.Name)
		}
		seen[folded] = f.Name
		if f.UncompressedSize64 > uint64(opts.MaxPartSize) {
			return nil, errLimit("entry %q: %d octets uncompressed, at most %d", f.Name, f.UncompressedSize64, opts.MaxPartSize)
		}
		if f.UncompressedSize64 > RatioFloor {
			if f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > uint64(opts.MaxRatio) {
				return nil, errLimit("entry %q: compression ratio above %d", f.Name, opts.MaxRatio)
			}
		}
		total += int64(f.UncompressedSize64)
		if total > opts.MaxTotalSize {
			return nil, errLimit("more than %d octets uncompressed in total", opts.MaxTotalSize)
		}
		data, err := readEntry(f)
		if err != nil {
			return nil, err
		}
		e := &entry{header: f.FileHeader, data: data}
		p.entries = append(p.entries, e)
		switch {
		case strings.HasSuffix(f.Name, "/"):
			if len(data) != 0 {
				return nil, errMalformed("folder entry %q has content", f.Name)
			}
		case folded == asciiLower(ContentTypesName):
			ctypes = e
		default:
			part := &Part{
				Name:             "/" + f.Name,
				Method:           f.Method,
				CompressedSize:   f.CompressedSize64,
				UncompressedSize: f.UncompressedSize64,
				CRC32:            f.CRC32,
				Modified:         f.Modified,
			}
			e.part = part
			p.Parts = append(p.Parts, part)
			p.byName[asciiLower(part.Name)] = e
			for _, msg := range partNameIssues(part.Name) {
				p.issue(part.Name, "%s", msg)
			}
		}
	}
	if ctypes == nil {
		// Ecma-376 Part 2, ZIP mapping of content types: the Content
		// Types stream is required.
		return nil, errMalformed("no %s", ContentTypesName)
	}
	if err := p.readContentTypes(ctypes.data); err != nil {
		return nil, err
	}
	p.readRelationships()
	p.readCoreProperties()
	p.readSignatures()
	return p, nil
}

// readEntry decompresses an entry, reading at most one octet more than its
// header declares (which Open has bounded), and checks its CRC-32: an
// entry that inflates beyond its declared size is rejected before it costs
// more memory than the limits allow.
func readEntry(f *zip.File) ([]byte, error) {
	limit := int64(f.UncompressedSize64)
	if f.Method != zip.Store && f.Method != zip.Deflate {
		return nil, fmt.Errorf("gsdx: %w: entry %q: compression method %d", profinet.ErrUnsupported, f.Name, f.Method)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, errMalformed("entry %q: %v", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, errMalformed("entry %q: %v", f.Name, err)
	}
	if int64(len(data)) != limit {
		return nil, errMalformed("entry %q: %d octets, header declares %d", f.Name, len(data), f.UncompressedSize64)
	}
	// archive/zip checks the CRC at EOF only when it has a CRC to check;
	// check it here in every case.
	if crc32.ChecksumIEEE(data) != f.CRC32 {
		return nil, errMalformed("entry %q: CRC-32 mismatch", f.Name)
	}
	return data, nil
}

// Part returns the part with the given name (ASCII case-insensitive).
func (p *Package) Part(name string) (*Part, bool) {
	e, ok := p.byName[asciiLower(name)]
	if !ok {
		return nil, false
	}
	return e.part, true
}

// ReadPart returns a copy of the content of a part.
func (p *Package) ReadPart(name string) ([]byte, error) {
	e, ok := p.byName[asciiLower(name)]
	if !ok {
		return nil, fmt.Errorf("gsdx: %w: part %q", profinet.ErrNotFound, name)
	}
	return bytes.Clone(e.data), nil
}

func (p *Package) data(name string) ([]byte, bool) {
	e, ok := p.byName[asciiLower(name)]
	if !ok {
		return nil, false
	}
	return e.data, true
}

// Unreachable returns the parts no relationship chain from the package
// relationships reaches, sorted, leaving out relationships parts (they are
// reached through their source). Cycles of relationships are allowed.
func (p *Package) Unreachable() []string {
	seen := map[string]bool{}
	queue := []Relationship{}
	queue = append(queue, p.Relationships...)
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		if r.TargetMode == TargetModeExternal || r.ResolvedTarget == "" {
			continue
		}
		k := asciiLower(r.ResolvedTarget)
		if seen[k] {
			continue
		}
		seen[k] = true
		if part, ok := p.Part(r.ResolvedTarget); ok {
			queue = append(queue, part.Relationships...)
		}
	}
	var out []string
	for _, part := range p.Parts {
		if !seen[asciiLower(part.Name)] && !isRelsPart(part.Name) {
			out = append(out, part.Name)
		}
	}
	sort.Strings(out)
	return out
}

// WriteTo writes the package as a ZIP: every entry in its original order,
// with its name, compression method, modification time, comment and
// content unchanged, so that nothing is lost by opening and writing a
// package. The compressed octets may differ from the input's: entries are
// compressed again.
func (p *Package) WriteTo(w io.Writer) (int64, error) {
	cw := &countWriter{w: w}
	zw := zip.NewWriter(cw)
	for _, e := range p.entries {
		h := zip.FileHeader{
			Name:     e.header.Name,
			Comment:  e.header.Comment,
			Method:   e.header.Method,
			Modified: e.header.Modified,
		}
		h.SetMode(e.header.Mode())
		fw, err := zw.CreateHeader(&h)
		if err != nil {
			return cw.n, err
		}
		if _, err := fw.Write(e.data); err != nil {
			return cw.n, err
		}
	}
	if p.Comment != "" {
		if err := zw.SetComment(p.Comment); err != nil {
			return cw.n, err
		}
	}
	err := zw.Close()
	return cw.n, err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

var errNotGSDML = errors.New("not a GSDML part")
