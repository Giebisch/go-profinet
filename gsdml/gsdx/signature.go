// SPDX-License-Identifier: MIT

package gsdx

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"hash"
	"net/url"
	"strings"
	"time"
)

// NotVerifiedReason is the Reason of every Signature.
const NotVerifiedReason = "not verified: go-profinet does not canonicalize XML (C14N, the OPC relationship transform), " +
	"does not check the SignatureValue and does not evaluate trust in the certificate; " +
	"the PI signing policy for GSDX is members-only"

// DigestCheck is what Open found when it compared the digest of a part
// reference with the part.
type DigestCheck string

// The outcomes of a digest comparison.
const (
	// DigestNotChecked: the reference has transforms (canonicalization
	// or the relationship transform, not implemented), an unknown digest
	// method, or no part to compare with. DigestNote says which.
	DigestNotChecked DigestCheck = "not-checked"
	// DigestMatch: the digest over the part's octets equals DigestValue.
	DigestMatch DigestCheck = "match"
	// DigestMismatch: the part changed after it was signed, or the
	// DigestValue is wrong.
	DigestMismatch DigestCheck = "mismatch"
)

// Reference is a ds:Reference of a signature: from SignedInfo (usually
// to the package-specific ds:Object) or from the ds:Manifest of an Object
// (the parts and relationships parts that were signed).
type Reference struct {
	URI  string `json:"uri"`
	Type string `json:"type,omitempty"`
	// ObjectID is the Id of the ds:Object whose Manifest holds the
	// reference; empty for a SignedInfo reference.
	ObjectID     string   `json:"objectId,omitempty"`
	Transforms   []string `json:"transforms,omitempty"`
	DigestMethod string   `json:"digestMethod"`
	DigestValue  string   `json:"digestValue"`
	// PartName and ContentType are the part name and the ContentType
	// query of a part reference ("/a.xml?ContentType=...").
	PartName    string      `json:"partName,omitempty"`
	ContentType string      `json:"contentType,omitempty"`
	Digest      DigestCheck `json:"digest"`
	DigestNote  string      `json:"digestNote,omitempty"`
}

// Certificate is what crypto/x509 parses from a signer certificate. It is
// reported, not validated: no chain is built and nothing is trusted.
type Certificate struct {
	// Source is "KeyInfo" or the name of the certificate part.
	Source             string    `json:"source"`
	Subject            string    `json:"subject,omitempty"`
	Issuer             string    `json:"issuer,omitempty"`
	SerialNumber       string    `json:"serialNumber,omitempty"`
	NotBefore          time.Time `json:"notBefore"`
	NotAfter           time.Time `json:"notAfter"`
	SignatureAlgorithm string    `json:"signatureAlgorithm,omitempty"`
	SHA256             string    `json:"sha256,omitempty"`
	// Error is set when the certificate could not be parsed.
	Error string `json:"error,omitempty"`
}

// Signature is the metadata of one OPC digital signature (Ecma-376 Part 2,
// "Digital signatures"): a signature part reached from the Digital
// Signature Origin part through a signature relationship.
//
// Signatures are NOT verified. Verifying one needs XML canonicalization
// (C14N) of SignedInfo and of the package-specific Object, the OPC
// relationship transform, the check of the SignatureValue with the
// signer's key, and a trust decision on the certificate, which for a GSDX
// follows PI's members-only signing policy. None of that is implemented:
// Verified is always false and Reason says why.
//
// What Open does compare is narrower: for a part reference of the Manifest
// that has no ds:Transforms, the digest over the part's octets. XML
// Signature (W3C XML Signature Syntax and Processing, "Reference
// Processing Model") digests the octet stream the URI dereferences when no
// transform applies, and OPC signatures are XML signatures whose part
// references dereference the part's content. A DigestMatch says that this
// part is the one that was digested; it does not say that the signature is
// valid, since the Manifest itself is covered only by the unverified
// SignatureValue.
type Signature struct {
	Part                   string `json:"part"`
	ID                     string `json:"id,omitempty"`
	CanonicalizationMethod string `json:"canonicalizationMethod,omitempty"`
	SignatureMethod        string `json:"signatureMethod,omitempty"`
	// SignedInfoReferences are the references of ds:SignedInfo.
	SignedInfoReferences []Reference `json:"signedInfoReferences,omitempty"`
	// PartReferences are the references of the ds:Manifest elements.
	PartReferences []Reference `json:"partReferences,omitempty"`
	// SignatureTime and SignatureTimeFormat are the OPC SignatureTime
	// signature property, as written; empty when absent.
	SignatureTime       string        `json:"signatureTime,omitempty"`
	SignatureTimeFormat string        `json:"signatureTimeFormat,omitempty"`
	Certificates        []Certificate `json:"certificates,omitempty"`
	// Verified is always false; Reason says why.
	Verified bool   `json:"verified"`
	Reason   string `json:"reason"`
}

const (
	nsDSig = "http://www.w3.org/2000/09/xmldsig#"

	DigestSHA1   = "http://www.w3.org/2000/09/xmldsig#sha1"
	DigestSHA256 = "http://www.w3.org/2001/04/xmlenc#sha256"
	DigestSHA384 = "http://www.w3.org/2001/04/xmldsig-more#sha384"
	DigestSHA512 = "http://www.w3.org/2001/04/xmlenc#sha512"
)

type xmlAlgo struct {
	Algorithm string `xml:"Algorithm,attr"`
}

type xmlReference struct {
	URI        string `xml:"URI,attr"`
	Type       string `xml:"Type,attr"`
	Transforms struct {
		Transforms []xmlAlgo `xml:"http://www.w3.org/2000/09/xmldsig# Transform"`
	} `xml:"http://www.w3.org/2000/09/xmldsig# Transforms"`
	DigestMethod xmlAlgo `xml:"http://www.w3.org/2000/09/xmldsig# DigestMethod"`
	DigestValue  string  `xml:"http://www.w3.org/2000/09/xmldsig# DigestValue"`
}

type xmlSignature struct {
	XMLName    xml.Name
	ID         string `xml:"Id,attr"`
	SignedInfo struct {
		CanonicalizationMethod xmlAlgo        `xml:"http://www.w3.org/2000/09/xmldsig# CanonicalizationMethod"`
		SignatureMethod        xmlAlgo        `xml:"http://www.w3.org/2000/09/xmldsig# SignatureMethod"`
		References             []xmlReference `xml:"http://www.w3.org/2000/09/xmldsig# Reference"`
	} `xml:"http://www.w3.org/2000/09/xmldsig# SignedInfo"`
	KeyInfo struct {
		X509Data []struct {
			Certificates []string `xml:"http://www.w3.org/2000/09/xmldsig# X509Certificate"`
		} `xml:"http://www.w3.org/2000/09/xmldsig# X509Data"`
	} `xml:"http://www.w3.org/2000/09/xmldsig# KeyInfo"`
	Objects []struct {
		ID       string `xml:"Id,attr"`
		Manifest []struct {
			References []xmlReference `xml:"http://www.w3.org/2000/09/xmldsig# Reference"`
		} `xml:"http://www.w3.org/2000/09/xmldsig# Manifest"`
		Properties []struct {
			Property []struct {
				Time []struct {
					Format string `xml:"http://schemas.openxmlformats.org/package/2006/digital-signature Format"`
					Value  string `xml:"http://schemas.openxmlformats.org/package/2006/digital-signature Value"`
				} `xml:"http://schemas.openxmlformats.org/package/2006/digital-signature SignatureTime"`
			} `xml:"http://www.w3.org/2000/09/xmldsig# SignatureProperty"`
		} `xml:"http://www.w3.org/2000/09/xmldsig# SignatureProperties"`
	} `xml:"http://www.w3.org/2000/09/xmldsig# Object"`
}

// readSignatures finds the Digital Signature Origin part through its
// package relationship and every signature part through the origin's
// signature relationships (Ecma-376 Part 2, "Digital signature parts").
func (p *Package) readSignatures() {
	var origins []string
	for _, r := range p.Relationships {
		if r.Type == RelTypeSignatureOrigin && r.ResolvedTarget != "" {
			origins = append(origins, r.ResolvedTarget)
		}
	}
	if len(origins) == 0 {
		return
	}
	if len(origins) > 1 {
		p.issue("/_rels/.rels", "%d digital signature origin relationships, at most one is allowed", len(origins))
	}
	origin, ok := p.Part(origins[0])
	if !ok {
		return
	}
	if origin.ContentType != ContentTypeSignatureOrigin {
		p.issue(origin.Name, "signature origin part with content type %q", origin.ContentType)
	}
	for _, r := range origin.Relationships {
		if r.Type != RelTypeSignature || r.ResolvedTarget == "" {
			continue
		}
		part, ok := p.Part(r.ResolvedTarget)
		if !ok {
			continue
		}
		if part.ContentType != ContentTypeSignature {
			p.issue(part.Name, "signature part with content type %q", part.ContentType)
		}
		p.Signatures = append(p.Signatures, p.readSignature(part))
	}
}

func (p *Package) readSignature(part *Part) *Signature {
	s := &Signature{Part: part.Name, Reason: NotVerifiedReason}
	data, _ := p.data(part.Name)
	var x xmlSignature
	if err := xml.Unmarshal(data, &x); err != nil {
		p.issue(part.Name, "signature: %v", err)
		return s
	}
	if x.XMLName.Space != nsDSig || x.XMLName.Local != "Signature" {
		p.issue(part.Name, "root element {%s}%s, not {%s}Signature", x.XMLName.Space, x.XMLName.Local, nsDSig)
	}
	s.ID = x.ID
	s.CanonicalizationMethod = x.SignedInfo.CanonicalizationMethod.Algorithm
	s.SignatureMethod = x.SignedInfo.SignatureMethod.Algorithm
	for _, xr := range x.SignedInfo.References {
		ref := newReference(xr, "")
		ref.DigestNote = "a reference into the signature markup: its digest needs XML canonicalization"
		s.SignedInfoReferences = append(s.SignedInfoReferences, ref)
	}
	for _, o := range x.Objects {
		for _, m := range o.Manifest {
			for _, xr := range m.References {
				s.PartReferences = append(s.PartReferences, p.partReference(part.Name, newReference(xr, o.ID)))
			}
		}
		for _, props := range o.Properties {
			for _, prop := range props.Property {
				for _, t := range prop.Time {
					s.SignatureTime = strings.TrimSpace(t.Value)
					s.SignatureTimeFormat = strings.TrimSpace(t.Format)
				}
			}
		}
	}
	for _, xd := range x.KeyInfo.X509Data {
		for _, c := range xd.Certificates {
			der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c), ""))
			if err != nil {
				s.Certificates = append(s.Certificates, Certificate{Source: "KeyInfo", Error: "base64: " + err.Error()})
				continue
			}
			s.Certificates = append(s.Certificates, certificate("KeyInfo", der))
		}
	}
	// A certificate may be stored in its own part instead (Ecma-376
	// Part 2, "Digital signature certificate part"), in DER.
	for _, r := range part.Relationships {
		if r.Type != RelTypeCertificate || r.ResolvedTarget == "" {
			continue
		}
		if der, ok := p.data(r.ResolvedTarget); ok {
			cpart, _ := p.Part(r.ResolvedTarget)
			s.Certificates = append(s.Certificates, certificate(cpart.Name, der))
		}
	}
	return s
}

func newReference(xr xmlReference, objectID string) Reference {
	ref := Reference{
		URI:          xr.URI,
		Type:         xr.Type,
		ObjectID:     objectID,
		DigestMethod: xr.DigestMethod.Algorithm,
		DigestValue:  strings.TrimSpace(xr.DigestValue),
		Digest:       DigestNotChecked,
	}
	for _, t := range xr.Transforms.Transforms {
		ref.Transforms = append(ref.Transforms, t.Algorithm)
	}
	return ref
}

// partReference resolves a Manifest reference to its part and, when it
// has no transforms, compares the digest over the part's octets.
func (p *Package) partReference(sigPart string, ref Reference) Reference {
	u := ref.URI
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	if i := strings.IndexByte(u, '?'); i >= 0 {
		ref.ContentType = contentTypeQuery(u[i+1:])
		u = u[:i]
	}
	if !strings.HasPrefix(u, "/") {
		ref.DigestNote = "not a part reference"
		return ref
	}
	ref.PartName = u
	part, ok := p.Part(u)
	if !ok {
		p.issue(sigPart, "signed part %s does not exist", u)
		ref.DigestNote = "the part does not exist"
		return ref
	}
	if ref.ContentType != "" && ref.ContentType != part.ContentType {
		p.issue(sigPart, "signed part %s: reference says content type %q, the package %q", u, ref.ContentType, part.ContentType)
	}
	if len(ref.Transforms) > 0 {
		ref.DigestNote = "transforms (XML canonicalization or the relationship transform) are not implemented"
		return ref
	}
	h := digest(ref.DigestMethod)
	if h == nil {
		ref.DigestNote = "digest method not supported"
		return ref
	}
	want, err := base64.StdEncoding.DecodeString(ref.DigestValue)
	if err != nil {
		ref.DigestNote = "DigestValue is not base64"
		return ref
	}
	data, _ := p.data(u)
	h.Write(data)
	if bytes.Equal(h.Sum(nil), want) {
		ref.Digest = DigestMatch
	} else {
		ref.Digest = DigestMismatch
	}
	return ref
}

func digest(method string) hash.Hash {
	switch method {
	case DigestSHA1:
		return sha1.New()
	case DigestSHA256:
		return sha256.New()
	case DigestSHA384:
		return sha512.New384()
	case DigestSHA512:
		return sha512.New()
	}
	return nil
}

func certificate(source string, der []byte) Certificate {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return Certificate{Source: source, Error: err.Error()}
	}
	fp := sha256.Sum256(der)
	return Certificate{
		Source:             source,
		Subject:            c.Subject.String(),
		Issuer:             c.Issuer.String(),
		SerialNumber:       c.SerialNumber.Text(16),
		NotBefore:          c.NotBefore,
		NotAfter:           c.NotAfter,
		SignatureAlgorithm: c.SignatureAlgorithm.String(),
		SHA256:             hex.EncodeToString(fp[:]),
	}
}

// contentTypeQuery returns the ContentType parameter of a part reference
// query. It is percent-decoded as a URI component, not as a form: the "+"
// of "+xml" content types stays a "+".
func contentTypeQuery(q string) string {
	for _, kv := range strings.Split(q, "&") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k != "ContentType" {
			continue
		}
		if dec, err := url.PathUnescape(v); err == nil {
			return dec
		}
		return v
	}
	return ""
}
