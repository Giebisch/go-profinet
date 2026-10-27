// SPDX-License-Identifier: MIT

package gsdx

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// The fixtures are small OPC packages built here with archive/zip; no
// PI-owned file is used. The GSDML part is go-profinet's own fixture.

type ent struct {
	name   string
	data   []byte
	method uint16
}

func zipOf(t testing.TB, ents ...ent) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range ents {
		m := e.method
		if m == 0 {
			m = zip.Deflate
		}
		if m == 1 { // marker for Store
			m = zip.Store
		}
		h := &zip.FileHeader{Name: e.name, Method: m, Modified: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openBytes(b []byte) (*Package, error) {
	return Open(bytes.NewReader(b), int64(len(b)), Options{})
}

func contentTypes(defaults map[string]string, overrides ...[2]string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="` + nsContentTypes + `">`)
	for _, ext := range sortedKeys(defaults) {
		fmt.Fprintf(&b, `<Default Extension="%s" ContentType="%s"/>`, ext, defaults[ext])
	}
	for _, o := range overrides {
		fmt.Fprintf(&b, `<Override PartName="%s" ContentType="%s"/>`, o[0], o[1])
	}
	b.WriteString(`</Types>`)
	return []byte(b.String())
}

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// rels builds a relationships part from Id, Type, Target[, TargetMode].
func rels(rs ...[]string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="` + nsRelationships + `">`)
	for _, r := range rs {
		mode := ""
		if len(r) > 3 {
			mode = fmt.Sprintf(` TargetMode="%s"`, r[3])
		}
		fmt.Fprintf(&b, `<Relationship Id="%s" Type="%s" Target="%s"%s/>`, r[0], r[1], r[2], mode)
	}
	b.WriteString(`</Relationships>`)
	return []byte(b.String())
}

var baseDefaults = map[string]string{"rels": ContentTypeRelationships, "xml": "application/xml", "bin": "application/octet-stream"}

func minimal(extra ...ent) []ent {
	return append([]ent{
		{name: ContentTypesName, data: contentTypes(baseDefaults)},
		{name: "_rels/.rels", data: rels([]string{"r1", "urn:x", "a.xml"})},
		{name: "a.xml", data: []byte("<a/>")},
	}, extra...)
}

func selfSigned(t testing.TB) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x2a),
		Subject:      pkix.Name{CommonName: "OTF test signer", Organization: []string{"OT Fabric tests"}},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

const (
	gsdmlName = "/GSDML/GSDML-V2.35-OTF-Test-20261027.xml"
	blobData  = "unknown vendor data \x00\x01\x02"
	ctGSDXSig = ContentTypeSignature
)

func gsdmlFixture(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile("../testdata/otf-fixture-latin1.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func signatureXML(cert []byte, gsdml []byte) []byte {
	s256 := sha256.Sum256(gsdml)
	s1 := sha1.Sum([]byte("not the blob"))
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Signature Id="idPackageSignature" xmlns="http://www.w3.org/2000/09/xmldsig#">
 <SignedInfo>
  <CanonicalizationMethod Algorithm="http://www.w3.org/TR/2001/REC-xml-c14n-20010315"/>
  <SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha256"/>
  <Reference URI="#idPackageObject" Type="http://www.w3.org/2000/09/xmldsig#Object">
   <Transforms><Transform Algorithm="http://www.w3.org/TR/2001/REC-xml-c14n-20010315"/></Transforms>
   <DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>
   <DigestValue>AAAA</DigestValue>
  </Reference>
 </SignedInfo>
 <SignatureValue>AAAA</SignatureValue>
 <KeyInfo><X509Data><X509Certificate>
` + b64(cert) + `
 </X509Certificate></X509Data></KeyInfo>
 <Object Id="idPackageObject">
  <Manifest>
   <Reference URI="` + gsdmlName + `?ContentType=application/xml">
    <DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>
    <DigestValue>` + b64(s256[:]) + `</DigestValue>
   </Reference>
   <Reference URI="/data/blob.bin?ContentType=application/octet-stream">
    <DigestMethod Algorithm="http://www.w3.org/2000/09/xmldsig#sha1"/>
    <DigestValue>` + b64(s1[:]) + `</DigestValue>
   </Reference>
   <Reference URI="/_rels/.rels?ContentType=application/vnd.openxmlformats-package.relationships+xml">
    <Transforms>
     <Transform Algorithm="http://schemas.openxmlformats.org/package/2006/RelationshipTransform"/>
     <Transform Algorithm="http://www.w3.org/TR/2001/REC-xml-c14n-20010315"/>
    </Transforms>
    <DigestMethod Algorithm="http://www.w3.org/2000/09/xmldsig#sha1"/>
    <DigestValue>AAAA</DigestValue>
   </Reference>
  </Manifest>
  <SignatureProperties>
   <SignatureProperty Id="idSignatureTime" Target="#idPackageSignature">
    <mdssi:SignatureTime xmlns:mdssi="http://schemas.openxmlformats.org/package/2006/digital-signature">
     <mdssi:Format>YYYY-MM-DDThh:mm:ssTZD</mdssi:Format>
     <mdssi:Value>2026-10-27T12:00:00Z</mdssi:Value>
    </mdssi:SignatureTime>
   </SignatureProperty>
  </SignatureProperties>
 </Object>
</Signature>`)
}

// fixture is a complete package: core properties, a GSDML part, an unknown
// part, relative relationships with a cycle, an external relationship and
// a signature with its certificate both in KeyInfo and in its own part.
func fixture(t testing.TB) []ent {
	t.Helper()
	cert := selfSigned(t)
	g := gsdmlFixture(t)
	return []ent{
		{name: ContentTypesName, data: contentTypes(map[string]string{
			"rels": ContentTypeRelationships, "xml": "application/xml", "bin": "application/octet-stream",
			"sigs": ContentTypeSignatureOrigin, "cer": ContentTypeCertificate,
		},
			[2]string{"/docProps/core.xml", ContentTypeCoreProperties},
			[2]string{"/_xmlsignatures/sig1.xml", ctGSDXSig},
		)},
		{name: "_rels/.rels", data: rels(
			[]string{"rId1", RelTypeCoreProperties, "docProps/core.xml"},
			[]string{"rId2", RelTypeSignatureOrigin, "_xmlsignatures/origin.sigs"},
			[]string{"rId3", "http://example.invalid/gsdml", "/GSDML/GSDML-V2.35-OTF-Test-20261027.xml"},
			[]string{"rId4", "http://example.invalid/web", "https://example.com/x", TargetModeExternal},
		)},
		{name: "docProps/core.xml", data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
 <dc:title>OTF test device</dc:title>
 <dc:creator>go-profinet tests</dc:creator>
 <dcterms:created xsi:type="dcterms:W3CDTF">2026-10-27T12:00:00Z</dcterms:created>
 <dcterms:modified xsi:type="dcterms:W3CDTF">2026-10-27T13:00:00Z</dcterms:modified>
 <cp:version>1.0</cp:version>
 <cp:keywords><nested>ignored</nested></cp:keywords>
</cp:coreProperties>`)},
		{name: "GSDML/", data: nil},
		{name: gsdmlName[1:], data: g},
		{name: "GSDML/_rels/GSDML-V2.35-OTF-Test-20261027.xml.rels", data: rels([]string{"rId1", "urn:blob", "../data/blob.bin#frag"})},
		{name: "data/blob.bin", data: []byte(blobData), method: 1},
		{name: "data/_rels/blob.bin.rels", data: rels([]string{"rId1", "urn:back", "../GSDML/GSDML-V2.35-OTF-Test-20261027.xml"})},
		{name: "_xmlsignatures/origin.sigs", data: nil},
		{name: "_xmlsignatures/_rels/origin.sigs.rels", data: rels([]string{"rId1", RelTypeSignature, "sig1.xml"})},
		{name: "_xmlsignatures/sig1.xml", data: signatureXML(cert, g)},
		{name: "_xmlsignatures/_rels/sig1.xml.rels", data: rels([]string{"rId1", RelTypeCertificate, "certs/signer.cer"})},
		{name: "_xmlsignatures/certs/signer.cer", data: cert},
		{name: "orphan.bin", data: []byte("nobody points here")},
	}
}
