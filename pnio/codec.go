// SPDX-License-Identifier: MIT

// Package pnio encodes and decodes the PROFINET IO application layer
// (IEC 61158-6-10): the blocks of the connect, control, release, read and
// write services, the NDR argument headers they travel in over RPC, the
// PNIO status, identification and maintenance records, diagnosis data,
// alarm notifications and identification data.
//
// Every block is a typed structure with an Append method and a decode
// function. Blocks are always big endian; the NDR header around them uses
// the RPC packet's data representation.
package pnio

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/rpc"
)

// reader is a big-endian cursor with a sticky error: after the first
// short read every further read returns zero values and Err reports what
// was missing.
type reader struct {
	b   []byte
	off int
	err error
	ctx string
}

func newReader(b []byte, ctx string) *reader { return &reader{b: b, ctx: ctx} }

func (r *reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if n < 0 || len(r.b)-r.off < n {
		r.err = fmt.Errorf("pnio: %w: %s truncated at offset %d (need %d of %d octets)", profinet.ErrMalformed, r.ctx, r.off, n, len(r.b)-r.off)
		return false
	}
	return true
}

func (r *reader) u8() uint8 {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) bytes(n int) []byte {
	if !r.need(n) {
		return nil
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}

func (r *reader) copyBytes(n int) []byte {
	b := r.bytes(n)
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

func (r *reader) skip(n int) { r.bytes(n) }

func (r *reader) uuid() rpc.UUID {
	b := r.bytes(16)
	if b == nil {
		return rpc.UUID{}
	}
	return rpc.UUID(b)
}

func (r *reader) mac() ethernet.MAC {
	b := r.bytes(6)
	if b == nil {
		return ethernet.MAC{}
	}
	return ethernet.MAC(b)
}

func (r *reader) rest() []byte {
	if r.err != nil {
		return nil
	}
	v := r.b[r.off:]
	r.off = len(r.b)
	return v
}

func (r *reader) remaining() int { return len(r.b) - r.off }

// count reads a 16-bit element count and checks that at least min octets
// per element remain, so that a hostile count cannot make the decoder
// allocate or loop beyond the input.
func (r *reader) count(minSize int) int {
	n := int(r.u16())
	if r.err == nil && n*minSize > r.remaining() {
		r.err = fmt.Errorf("pnio: %w: %s: count %d exceeds the %d octets left", profinet.ErrMalformed, r.ctx, n, r.remaining())
		return 0
	}
	return n
}

// end fails when octets are left over.
func (r *reader) end() error {
	if r.err == nil && r.off != len(r.b) {
		r.err = fmt.Errorf("pnio: %w: %s: %d trailing octets", profinet.ErrMalformed, r.ctx, len(r.b)-r.off)
	}
	return r.err
}

func appendU16(b []byte, v uint16) []byte { return binary.BigEndian.AppendUint16(b, v) }
func appendU32(b []byte, v uint32) []byte { return binary.BigEndian.AppendUint32(b, v) }

// padTo appends zeros until the length of b since start is a multiple of
// align.
func padTo(b []byte, start, align int) []byte {
	for (len(b)-start)%align != 0 {
		b = append(b, 0)
	}
	return b
}

// fixedString appends s padded with blanks (or truncated) to n octets, as
// the visible strings of I&M records are.
func fixedString(b []byte, s string, n int) []byte {
	if len(s) > n {
		s = s[:n]
	}
	b = append(b, s...)
	for i := len(s); i < n; i++ {
		b = append(b, ' ')
	}
	return b
}

// trimString returns the visible string of a fixed-size field without its
// trailing blanks and NULs.
func trimString(b []byte) string {
	n := len(b)
	for n > 0 && (b[n-1] == ' ' || b[n-1] == 0) {
		n--
	}
	return string(b[:n])
}
