// SPDX-License-Identifier: MIT

package rpc

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/otfabric/go-profinet"
)

// UUID is a DCE UUID in its canonical (big-endian, string) octet order.
// On the wire the first three fields follow the data representation of
// the packet; [UUID.Append] and [DecodeUUID] convert.
type UUID [16]byte

// NilUUID is the all-zero UUID.
var NilUUID UUID

// ParseUUID parses the 8-4-4-4-12 hexadecimal form.
func ParseUUID(s string) (UUID, error) {
	var u UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, fmt.Errorf("rpc: %w: UUID %q", profinet.ErrInvalidConfig, s)
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return u, fmt.Errorf("rpc: %w: UUID %q", profinet.ErrInvalidConfig, s)
	}
	copy(u[:], b)
	return u, nil
}

// MustParseUUID is ParseUUID that panics on error, for constants.
func MustParseUUID(s string) UUID {
	u, err := ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

// NewRandomUUID returns a version 4 UUID.
func NewRandomUUID() UUID {
	var u UUID
	if _, err := rand.Read(u[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	u[6] = u[6]&0x0F | 0x40
	u[8] = u[8]&0x3F | 0x80
	return u
}

// String returns the 8-4-4-4-12 form in lower case.
func (u UUID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:], u[10:])
	return string(b[:])
}

// MarshalText implements encoding.TextMarshaler.
func (u UUID) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (u *UUID) UnmarshalText(b []byte) error {
	v, err := ParseUUID(string(b))
	if err != nil {
		return err
	}
	*u = v
	return nil
}

// IsNil reports whether u is the nil UUID.
func (u UUID) IsNil() bool { return u == NilUUID }

// Append appends the wire form of u in the given byte order.
func (u UUID) Append(dst []byte, order ByteOrder) []byte {
	if order == ByteOrder(binary.BigEndian) {
		return append(dst, u[:]...)
	}
	dst = order.AppendUint32(dst, binary.BigEndian.Uint32(u[0:4]))
	dst = order.AppendUint16(dst, binary.BigEndian.Uint16(u[4:6]))
	dst = order.AppendUint16(dst, binary.BigEndian.Uint16(u[6:8]))
	return append(dst, u[8:]...)
}

// DecodeUUID decodes the wire form of a UUID from the first 16 octets of b.
func DecodeUUID(b []byte, order binary.ByteOrder) UUID {
	var u UUID
	binary.BigEndian.PutUint32(u[0:4], order.Uint32(b[0:4]))
	binary.BigEndian.PutUint16(u[4:6], order.Uint16(b[4:6]))
	binary.BigEndian.PutUint16(u[6:8], order.Uint16(b[6:8]))
	copy(u[8:], b[8:16])
	return u
}
