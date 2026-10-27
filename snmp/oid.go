// SPDX-License-Identifier: MIT

package snmp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/otfabric/go-profinet"
)

// MaxOIDLen is the largest number of sub-identifiers of an OBJECT
// IDENTIFIER: "there are at most 128 sub-identifiers in a value, and each
// sub-identifier has a maximum value of 2^32-1" (RFC 2578 section 3.5).
const MaxOIDLen = 128

// OID is an OBJECT IDENTIFIER, one element per sub-identifier.
type OID []uint32

// ParseOID parses the dotted form "1.3.6.1.2.1.1.1.0" (a leading dot is
// allowed).
func ParseOID(s string) (OID, error) {
	s = strings.TrimPrefix(s, ".")
	if s == "" {
		return nil, fmt.Errorf("snmp: %w: empty OID", profinet.ErrInvalidConfig)
	}
	parts := strings.Split(s, ".")
	if len(parts) > MaxOIDLen {
		return nil, fmt.Errorf("snmp: %w: OID of %d sub-identifiers", profinet.ErrInvalidConfig, len(parts))
	}
	o := make(OID, len(parts))
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("snmp: %w: OID %q: %v", profinet.ErrInvalidConfig, s, err)
		}
		o[i] = uint32(v)
	}
	return o, nil
}

// MustParseOID is ParseOID for constants; it panics on an error.
func MustParseOID(s string) OID {
	o, err := ParseOID(s)
	if err != nil {
		panic(err)
	}
	return o
}

// String returns the dotted form.
func (o OID) String() string {
	var sb strings.Builder
	for i, v := range o {
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(strconv.FormatUint(uint64(v), 10))
	}
	return sb.String()
}

// Compare orders OIDs lexicographically, sub-identifier by
// sub-identifier, a prefix before its extensions: the order of GetNext
// (RFC 3416 section 4.2.2).
func (o OID) Compare(p OID) int {
	for i := 0; i < len(o) && i < len(p); i++ {
		switch {
		case o[i] < p[i]:
			return -1
		case o[i] > p[i]:
			return 1
		}
	}
	switch {
	case len(o) < len(p):
		return -1
	case len(o) > len(p):
		return 1
	}
	return 0
}

// Equal reports whether o and p are the same OID.
func (o OID) Equal(p OID) bool { return o.Compare(p) == 0 }

// HasPrefix reports whether p is a prefix of o (or o itself).
func (o OID) HasPrefix(p OID) bool {
	if len(p) > len(o) {
		return false
	}
	for i := range p {
		if o[i] != p[i] {
			return false
		}
	}
	return true
}

// Append returns a new OID: o followed by sub.
func (o OID) Append(sub ...uint32) OID {
	out := make(OID, 0, len(o)+len(sub))
	return append(append(out, o...), sub...)
}

// validate checks what X.690 8.19 can encode: at least two
// sub-identifiers, the first 0, 1 or 2, the second below 40 unless the
// first is 2, and the first two combined into one 32-bit sub-identifier.
func (o OID) validate() error {
	if len(o) < 2 || len(o) > MaxOIDLen {
		return fmt.Errorf("snmp: %w: OID of %d sub-identifiers", profinet.ErrInvalidConfig, len(o))
	}
	if o[0] > 2 || (o[0] < 2 && o[1] >= 40) || uint64(o[0])*40+uint64(o[1]) > 0xFFFFFFFF {
		return fmt.Errorf("snmp: %w: OID %s cannot be encoded", profinet.ErrInvalidConfig, o)
	}
	return nil
}

func appendBase128(b []byte, v uint32) []byte {
	n := 1
	for x := v >> 7; x != 0; x >>= 7 {
		n++
	}
	for i := n - 1; i > 0; i-- {
		b = append(b, byte(v>>(7*i))|0x80)
	}
	return append(b, byte(v&0x7F))
}

// appendOIDContent appends the content octets of o (X.690 8.19); o must
// be valid.
func appendOIDContent(b []byte, o OID) []byte {
	b = appendBase128(b, o[0]*40+o[1])
	for _, v := range o[2:] {
		b = appendBase128(b, v)
	}
	return b
}

// oidContentLen is the number of content octets of a valid o.
func oidContentLen(o OID) int {
	n := 0
	f := func(v uint32) {
		n++
		for v >>= 7; v != 0; v >>= 7 {
			n++
		}
	}
	f(o[0]*40 + o[1])
	for _, v := range o[2:] {
		f(v)
	}
	return n
}

// decodeOID decodes the content octets of an OBJECT IDENTIFIER. Each
// sub-identifier is in its minimal form (no leading 0x80 octet, X.690
// 8.19.2) and fits 32 bits; there are at most MaxOIDLen of them.
func decodeOID(c []byte) (OID, error) {
	if len(c) == 0 {
		return nil, malformed("empty OBJECT IDENTIFIER")
	}
	if len(c) > 5*MaxOIDLen {
		return nil, malformed("OBJECT IDENTIFIER of %d octets", len(c))
	}
	// Count first, so that the allocation is exact and bounded.
	n := 0
	for _, x := range c {
		if x&0x80 == 0 {
			n++
		}
	}
	if c[len(c)-1]&0x80 != 0 {
		return nil, malformed("truncated sub-identifier")
	}
	if n+1 > MaxOIDLen {
		return nil, malformed("OBJECT IDENTIFIER of %d sub-identifiers", n+1)
	}
	o := make(OID, 0, n+1)
	var v uint64
	start := true
	for _, x := range c {
		if start && x == 0x80 {
			return nil, malformed("sub-identifier not in its minimal form")
		}
		start = false
		v = v<<7 | uint64(x&0x7F)
		if v > 0xFFFFFFFF {
			return nil, malformed("sub-identifier exceeds 32 bits")
		}
		if x&0x80 != 0 {
			continue
		}
		if len(o) == 0 {
			switch {
			case v < 40:
				o = append(o, 0, uint32(v))
			case v < 80:
				o = append(o, 1, uint32(v-40))
			default:
				o = append(o, 2, uint32(v-80))
			}
		} else {
			o = append(o, uint32(v))
		}
		v, start = 0, true
	}
	return o, nil
}
