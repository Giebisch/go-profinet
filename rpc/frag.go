// SPDX-License-Identifier: MIT

package rpc

import (
	"fmt"

	"github.com/otfabric/go-profinet"
)

// DefaultMaxFragmentBody is the largest body of one packet this package
// sends: a UDP datagram of 1464 octets (header included), which fits an
// Ethernet frame without IP fragmentation and matches what PROFINET
// devices accept.
const DefaultMaxFragmentBody = 1464 - HeaderLen

// DefaultMaxMessage bounds a reassembled request or response body.
const DefaultMaxMessage = 64 * 1024

// MaxFragments bounds the fragments of one message.
const MaxFragments = 256

// packets splits a body into the packets of one request or response,
// derived from the template header h.
func packets(h Header, body []byte, maxFrag int) [][]byte {
	if maxFrag <= 0 {
		maxFrag = DefaultMaxFragmentBody
	}
	n := (len(body) + maxFrag - 1) / maxFrag
	if n == 0 {
		n = 1
	}
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		lo := i * maxFrag
		hi := min(lo+maxFrag, len(body))
		p := h
		p.FragNum = uint16(i)
		p.BodyLen = uint16(hi - lo)
		if n > 1 {
			p.Flags1 |= FlagFrag | FlagNoFack
			if i == n-1 {
				p.Flags1 |= FlagLastFrag
			}
		}
		b := p.AppendTo(make([]byte, 0, HeaderLen+hi-lo))
		out = append(out, append(b, body[lo:hi]...))
	}
	return out
}

// reassembly collects the fragments of one message.
type reassembly struct {
	frags [][]byte
	have  int
	last  int // index of the last fragment, -1 until seen
	size  int
	max   int
}

func newReassembly(maxMessage int) *reassembly {
	if maxMessage <= 0 {
		maxMessage = DefaultMaxMessage
	}
	return &reassembly{last: -1, max: maxMessage}
}

// add adds a fragment. It returns the complete body when every fragment
// has arrived.
func (r *reassembly) add(h *Header, body []byte) ([]byte, bool, error) {
	if h.Flags1&FlagFrag == 0 {
		return append([]byte(nil), body...), true, nil
	}
	n := int(h.FragNum)
	if n >= MaxFragments {
		return nil, false, fmt.Errorf("rpc: %w: fragment number %d", profinet.ErrResourceExhausted, n)
	}
	if r.last >= 0 && n > r.last {
		return nil, false, fmt.Errorf("rpc: %w: fragment %d after the last fragment %d", profinet.ErrMalformed, n, r.last)
	}
	if h.Flags1&FlagLastFrag != 0 {
		if r.last >= 0 && r.last != n {
			return nil, false, fmt.Errorf("rpc: %w: two last fragments", profinet.ErrMalformed)
		}
		if len(r.frags) > n+1 {
			return nil, false, fmt.Errorf("rpc: %w: last fragment %d below received fragments", profinet.ErrMalformed, n)
		}
		r.last = n
	}
	for len(r.frags) <= n {
		r.frags = append(r.frags, nil)
	}
	if r.frags[n] != nil {
		return nil, false, nil // duplicate
	}
	if r.size+len(body) > r.max {
		return nil, false, fmt.Errorf("rpc: %w: message exceeds %d octets", profinet.ErrResourceExhausted, r.max)
	}
	r.frags[n] = append(make([]byte, 0, len(body)+1), body...)
	if len(body) == 0 {
		r.frags[n] = []byte{}
	}
	r.have++
	r.size += len(body)
	if r.last < 0 || r.have != r.last+1 {
		return nil, false, nil
	}
	out := make([]byte, 0, r.size)
	for _, f := range r.frags {
		out = append(out, f...)
	}
	return out, true, nil
}
