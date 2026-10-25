// SPDX-License-Identifier: MIT

package pnio

import (
	"fmt"

	"github.com/otfabric/go-profinet"
)

// I&M field sizes.
const (
	IMOrderIDLen     = 20
	IMSerialLen      = 16
	IMTagFunctionLen = 32
	IMTagLocationLen = 22
	IMDateLen        = 16
	IMDescriptorLen  = 54
	IMSignatureLen   = 54
)

// IM_Supported bits of I&M0.
const (
	IMSupportIM1 uint16 = 1 << 1
	IMSupportIM2 uint16 = 1 << 2
	IMSupportIM3 uint16 = 1 << 3
	IMSupportIM4 uint16 = 1 << 4
)

// SoftwareRevision is the IM_Software_Revision of I&M0.
type SoftwareRevision struct {
	// Prefix is 'V' (officially released), 'R' (revision), 'P'
	// (prototype), 'U' (under field test) or 'T' (test device).
	Prefix            byte
	FunctionalEnhance uint8
	BugFix            uint8
	InternalChange    uint8
}

// String formats the revision as "V1.2.3".
func (s SoftwareRevision) String() string {
	return fmt.Sprintf("%c%d.%d.%d", s.Prefix, s.FunctionalEnhance, s.BugFix, s.InternalChange)
}

// IM0 is the identification record I&M0 (index 0xAFF0).
type IM0 struct {
	VendorID         uint16
	OrderID          string
	SerialNumber     string
	HardwareRevision uint16
	SoftwareRevision SoftwareRevision
	RevisionCounter  uint16
	ProfileID        uint16
	ProfileType      uint16
	VersionMajor     uint8
	VersionMinor     uint8
	// Supported is the bitmap of IMSupport* values.
	Supported uint16
}

// Append appends the I&M0 block.
func (m *IM0) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockIM0, 1, 0)
	b = appendU16(b, m.VendorID)
	b = fixedString(b, m.OrderID, IMOrderIDLen)
	b = fixedString(b, m.SerialNumber, IMSerialLen)
	b = appendU16(b, m.HardwareRevision)
	p := m.SoftwareRevision.Prefix
	if p == 0 {
		p = 'V'
	}
	b = append(b, p, m.SoftwareRevision.FunctionalEnhance, m.SoftwareRevision.BugFix, m.SoftwareRevision.InternalChange)
	b = appendU16(b, m.RevisionCounter)
	b = appendU16(b, m.ProfileID)
	b = appendU16(b, m.ProfileType)
	b = append(b, m.VersionMajor, m.VersionMinor)
	b = appendU16(b, m.Supported)
	return endBlock(b, s)
}

// DecodeIM0 decodes an I&M0 block.
func DecodeIM0(blk RawBlock) (IM0, error) {
	var m IM0
	if blk.Header.Type != BlockIM0 {
		return m, fmt.Errorf("pnio: %w: %s instead of I&M0", profinet.ErrMalformed, blk.Header.Type)
	}
	r := newReader(blk.Body, "I&M0")
	m.VendorID = r.u16()
	m.OrderID = trimString(r.bytes(IMOrderIDLen))
	m.SerialNumber = trimString(r.bytes(IMSerialLen))
	m.HardwareRevision = r.u16()
	m.SoftwareRevision = SoftwareRevision{Prefix: r.u8(), FunctionalEnhance: r.u8(), BugFix: r.u8(), InternalChange: r.u8()}
	m.RevisionCounter = r.u16()
	m.ProfileID = r.u16()
	m.ProfileType = r.u16()
	m.VersionMajor = r.u8()
	m.VersionMinor = r.u8()
	m.Supported = r.u16()
	return m, r.end()
}

// IM1 is the I&M1 record (index 0xAFF1): the plant designation.
type IM1 struct {
	TagFunction string
	TagLocation string
}

// IM2 is the I&M2 record (index 0xAFF2): the installation date,
// "YYYY-MM-DD HH:MM".
type IM2 struct {
	Date string
}

// IM3 is the I&M3 record (index 0xAFF3): a descriptor.
type IM3 struct {
	Descriptor string
}

// IM4 is the I&M4 record (index 0xAFF4): the signature, binary.
type IM4 struct {
	Signature [IMSignatureLen]byte
}

// Append appends the I&M1 block.
func (m *IM1) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockIM1, 1, 0)
	b = fixedString(b, m.TagFunction, IMTagFunctionLen)
	b = fixedString(b, m.TagLocation, IMTagLocationLen)
	return endBlock(b, s)
}

// Append appends the I&M2 block.
func (m *IM2) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockIM2, 1, 0)
	b = fixedString(b, m.Date, IMDateLen)
	return endBlock(b, s)
}

// Append appends the I&M3 block.
func (m *IM3) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockIM3, 1, 0)
	b = fixedString(b, m.Descriptor, IMDescriptorLen)
	return endBlock(b, s)
}

// Append appends the I&M4 block.
func (m *IM4) Append(b []byte) []byte {
	b, s := beginBlock(b, BlockIM4, 1, 0)
	b = append(b, m.Signature[:]...)
	return endBlock(b, s)
}

func imBody(blk RawBlock, t BlockType, n int) (*reader, error) {
	if blk.Header.Type != t {
		return nil, fmt.Errorf("pnio: %w: %s instead of %s", profinet.ErrMalformed, blk.Header.Type, t)
	}
	if len(blk.Body) != n {
		return nil, fmt.Errorf("pnio: %w: %s of %d octets instead of %d", profinet.ErrMalformed, t, len(blk.Body), n)
	}
	return newReader(blk.Body, t.String()), nil
}

// DecodeIM1 decodes an I&M1 block.
func DecodeIM1(blk RawBlock) (IM1, error) {
	r, err := imBody(blk, BlockIM1, IMTagFunctionLen+IMTagLocationLen)
	if err != nil {
		return IM1{}, err
	}
	return IM1{TagFunction: trimString(r.bytes(IMTagFunctionLen)), TagLocation: trimString(r.bytes(IMTagLocationLen))}, nil
}

// DecodeIM2 decodes an I&M2 block.
func DecodeIM2(blk RawBlock) (IM2, error) {
	r, err := imBody(blk, BlockIM2, IMDateLen)
	if err != nil {
		return IM2{}, err
	}
	return IM2{Date: trimString(r.bytes(IMDateLen))}, nil
}

// DecodeIM3 decodes an I&M3 block.
func DecodeIM3(blk RawBlock) (IM3, error) {
	r, err := imBody(blk, BlockIM3, IMDescriptorLen)
	if err != nil {
		return IM3{}, err
	}
	return IM3{Descriptor: trimString(r.bytes(IMDescriptorLen))}, nil
}

// DecodeIM4 decodes an I&M4 block.
func DecodeIM4(blk RawBlock) (IM4, error) {
	r, err := imBody(blk, BlockIM4, IMSignatureLen)
	if err != nil {
		return IM4{}, err
	}
	var m IM4
	copy(m.Signature[:], r.bytes(IMSignatureLen))
	return m, nil
}

// ValidIMDate reports whether s is empty or of the form
// "YYYY-MM-DD HH:MM" (or the shorter "YYYY-MM-DD").
func ValidIMDate(s string) bool {
	if s == "" {
		return true
	}
	if len(s) != 10 && len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 4, 7:
			if c != '-' {
				return false
			}
		case 10:
			if c != ' ' {
				return false
			}
		case 13:
			if c != ':' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
