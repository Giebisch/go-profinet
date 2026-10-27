// SPDX-License-Identifier: MIT

package snmp

import (
	"fmt"

	"github.com/otfabric/go-profinet"
)

// The subset of the Basic Encoding Rules (ITU-T X.690) that SNMP uses
// (RFC 3417 section 8): single-octet identifiers, definite lengths only.
// "When encoding the length field, only the definite form is used; use of
// the indefinite form encoding is prohibited. Note that when using the
// definite-long form, it is permissible to use more than the minimum
// number of length octets necessary to encode the length field" (RFC 3417
// section 8). The decoder therefore accepts non-minimal long lengths but
// never the indefinite form, and every length is checked against the
// octets that remain before anything is allocated.

// Identifier octets (tags).
const (
	tagInteger     byte = 0x02
	tagOctetString byte = 0x04
	tagNull        byte = 0x05
	tagOID         byte = 0x06
	tagSequence    byte = 0x30
)

// maxLengthOctets bounds the long form of a length: four octets describe
// more than any UDP datagram holds.
const maxLengthOctets = 4

func malformed(format string, a ...any) error {
	return fmt.Errorf("snmp: %w: "+format, append([]any{profinet.ErrMalformed}, a...)...)
}

// appendLength appends a definite length in its minimal form.
func appendLength(b []byte, n int) []byte {
	switch {
	case n < 0x80:
		return append(b, byte(n))
	case n <= 0xFF:
		return append(b, 0x81, byte(n))
	case n <= 0xFFFF:
		return append(b, 0x82, byte(n>>8), byte(n))
	case n <= 0xFFFFFF:
		return append(b, 0x83, byte(n>>16), byte(n>>8), byte(n))
	default:
		return append(b, 0x84, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
}

// lengthLen is the number of octets appendLength writes for n.
func lengthLen(n int) int {
	switch {
	case n < 0x80:
		return 1
	case n <= 0xFF:
		return 2
	case n <= 0xFFFF:
		return 3
	case n <= 0xFFFFFF:
		return 4
	default:
		return 5
	}
}

// tlvLen is the size of a TLV with a one-octet tag and n content octets.
func tlvLen(n int) int { return 1 + lengthLen(n) + n }

// appendTLV appends a TLV with content v.
func appendTLV(b []byte, tag byte, v []byte) []byte {
	return append(appendLength(append(b, tag), len(v)), v...)
}

// readTLV reads one TLV from b and returns its tag, its content and the
// octets that follow it.
func readTLV(b []byte) (tag byte, content, rest []byte, err error) {
	if len(b) < 2 {
		return 0, nil, nil, malformed("truncated TLV (%d octets)", len(b))
	}
	tag = b[0]
	if tag&0x1F == 0x1F {
		// The high-tag-number form: no SNMP type uses it.
		return 0, nil, nil, malformed("multi-octet tag 0x%02x", tag)
	}
	l := int(b[1])
	off := 2
	if l&0x80 != 0 {
		n := l & 0x7F
		if n == 0 {
			return 0, nil, nil, malformed("indefinite length")
		}
		if n > maxLengthOctets {
			return 0, nil, nil, malformed("%d length octets", n)
		}
		if len(b) < 2+n {
			return 0, nil, nil, malformed("truncated length")
		}
		l = 0
		for _, c := range b[2 : 2+n] {
			l = l<<8 | int(c)
		}
		off = 2 + n
	}
	if l > len(b)-off {
		return 0, nil, nil, malformed("length %d exceeds the %d remaining octets", l, len(b)-off)
	}
	return tag, b[off : off+l], b[off+l:], nil
}

// expect reads a TLV and checks its tag.
func expect(b []byte, tag byte, what string) (content, rest []byte, err error) {
	t, c, r, err := readTLV(b)
	if err != nil {
		return nil, nil, err
	}
	if t != tag {
		return nil, nil, malformed("%s: tag 0x%02x, want 0x%02x", what, t, tag)
	}
	return c, r, nil
}

// appendInt appends the content octets of a two's complement INTEGER in
// its minimal form (X.690 8.3).
func appendInt(b []byte, v int64) []byte {
	n := 1
	for x := v; x > 127 || x < -128; x >>= 8 {
		n++
	}
	for i := n - 1; i >= 0; i-- {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

// appendUint appends the content octets of a non-negative INTEGER in its
// minimal form: a leading zero octet when the top bit is set.
func appendUint(b []byte, v uint64) []byte {
	n := 1
	for x := v; x > 127; x >>= 8 {
		n++
	}
	for i := n - 1; i >= 0; i-- {
		if i >= 8 {
			b = append(b, 0)
			continue
		}
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

// minimal checks X.690 8.3.2: "the bits of the first octet and bit 8 of
// the second octet shall not all be ones; and shall not all be zero".
func minimal(c []byte) error {
	if len(c) == 0 {
		return malformed("empty INTEGER")
	}
	if len(c) > 1 && ((c[0] == 0 && c[1]&0x80 == 0) || (c[0] == 0xFF && c[1]&0x80 != 0)) {
		return malformed("INTEGER not in its minimal form")
	}
	return nil
}

// decodeInt32 decodes the content octets of an INTEGER that must fit
// Integer32 (RFC 2578 section 7.1.1).
func decodeInt32(c []byte) (int32, error) {
	if err := minimal(c); err != nil {
		return 0, err
	}
	if len(c) > 4 {
		return 0, malformed("INTEGER of %d octets exceeds Integer32", len(c))
	}
	v := int64(int8(c[0]))
	for _, x := range c[1:] {
		v = v<<8 | int64(x)
	}
	return int32(v), nil
}

// decodeUint decodes the content octets of a non-negative INTEGER of at
// most bits bits (32: Counter32, Gauge32, TimeTicks; 64: Counter64).
func decodeUint(c []byte, bits int) (uint64, error) {
	if err := minimal(c); err != nil {
		return 0, err
	}
	if c[0]&0x80 != 0 {
		return 0, malformed("negative unsigned value")
	}
	if len(c) > bits/8+1 || (len(c) == bits/8+1 && c[0] != 0) {
		return 0, malformed("unsigned value of %d octets exceeds %d bits", len(c), bits)
	}
	var v uint64
	for _, x := range c {
		v = v<<8 | uint64(x)
	}
	return v, nil
}

// appendIntTLV appends an INTEGER TLV.
func appendIntTLV(b []byte, v int64) []byte {
	var tmp [9]byte
	return appendTLV(b, tagInteger, appendInt(tmp[:0], v))
}

// intTLVLen is the size of an INTEGER TLV of v.
func intTLVLen(v int64) int {
	var tmp [9]byte
	return tlvLen(len(appendInt(tmp[:0], v)))
}

// readInt32 reads an INTEGER TLV that fits Integer32.
func readInt32(b []byte, what string) (int32, []byte, error) {
	c, rest, err := expect(b, tagInteger, what)
	if err != nil {
		return 0, nil, err
	}
	v, err := decodeInt32(c)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", what, err)
	}
	return v, rest, nil
}
