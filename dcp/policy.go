// SPDX-License-Identifier: MIT

package dcp

import (
	"fmt"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// Policy restricts what a [Responder] does: a read-only mode, rate limits
// per source MAC address, and audit events. Its zero value restricts
// nothing and audits nothing, which is the responder's default.
//
// These are local hardening measures of this library. They are not a
// PROFINET security class and do not authenticate anybody: a source MAC
// address is whatever the sender writes into the frame.
type Policy struct {
	// ReadOnly refuses every Set block that changes the device (name, IP,
	// DHCP options, ResetToFactory, FactoryReset, unknown blocks) with
	// BlockErrorSetNotPossible, "SET not possible by local reasons".
	// Control/Start, Control/Stop and Control/Signal, which change nothing,
	// are still answered; Identify and Get are answered as before.
	ReadOnly bool
	// IdentifyLimit bounds the Identify responses to one source MAC
	// address; SetLimit bounds the Set requests a source may make (the
	// others are dropped unanswered). Zero rates do not limit.
	IdentifyLimit, SetLimit RateLimit
	// Sources is the number of source addresses each limit tracks, in a
	// table of fixed size: zero means 64, at most 1024. When the table
	// is full and no tracked source is idle, a new source shares one
	// overflow budget with the other untracked sources, so that spoofed
	// addresses can neither grow memory nor multiply the budget.
	Sources int
	// Audit receives an event for every Set block (ResetToFactory
	// included), every refused request and every source that starts being
	// rate-limited. It is called on the responder's goroutine and must
	// not block. The responder also logs each event at Info level.
	Audit func(AuditEvent)
}

// RateLimit is a token bucket: Burst requests at once, refilled at Rate
// per second.
type RateLimit struct {
	// Rate is the sustained number of requests per second; zero or less
	// is no limit.
	Rate float64
	// Burst is the bucket size; zero or less means 1.
	Burst int
}

// maxSources bounds Policy.Sources: the table is scanned per request.
const maxSources = 1024

// defaultSources is the table size when Policy.Sources is zero.
const defaultSources = 64

func (p Policy) normalize() (Policy, error) {
	if p.Sources < 0 || p.Sources > maxSources {
		return p, fmt.Errorf("dcp: %w: %d rate-limited sources (0..%d)", profinet.ErrInvalidConfig, p.Sources, maxSources)
	}
	if p.Sources == 0 {
		p.Sources = defaultSources
	}
	for _, l := range []RateLimit{p.IdentifyLimit, p.SetLimit} {
		if l.Rate != l.Rate { // NaN
			return p, fmt.Errorf("dcp: %w: rate limit NaN", profinet.ErrInvalidConfig)
		}
	}
	return p, nil
}

// AuditOutcome is what the responder did with a request.
type AuditOutcome uint8

// Audit outcomes.
const (
	// AuditApplied: the Set block was executed.
	AuditApplied AuditOutcome = iota + 1
	// AuditRefused: the Set block was refused with Error.
	AuditRefused
	// AuditReadOnly: the Set block was refused by the read-only mode.
	AuditReadOnly
	// AuditRateLimited: the source exceeded its rate limit; its requests
	// are dropped until it is under the limit again (one event per
	// episode).
	AuditRateLimited
	// AuditUnsupported: a request of a service the responder does not
	// support was answered with ResponseUnsupported.
	AuditUnsupported
)

func (o AuditOutcome) String() string {
	switch o {
	case AuditApplied:
		return "applied"
	case AuditRefused:
		return "refused"
	case AuditReadOnly:
		return "read-only"
	case AuditRateLimited:
		return "rate-limited"
	case AuditUnsupported:
		return "unsupported"
	default:
		return fmt.Sprintf("AuditOutcome(%d)", uint8(o))
	}
}

// AuditEvent is one audited request or Set block.
type AuditEvent struct {
	Time time.Time
	// Source is the source MAC address of the request.
	Source ethernet.MAC
	// Service is the DCP service of the request.
	Service ServiceID
	// Block is the option and suboption of a Set block (zero for a whole
	// request); Qualifier its BlockQualifier.
	Block     BlockID
	Qualifier uint16
	Outcome   AuditOutcome
	// Error is the BlockError of a refused Set block.
	Error BlockError
}

// String formats the event for a log line.
func (e AuditEvent) String() string {
	s := fmt.Sprintf("dcp audit: %s from %s", e.Service, e.Source)
	if e.Block != 0 {
		s += fmt.Sprintf(" block %s qualifier %d", e.Block, e.Qualifier)
	}
	s += " " + e.Outcome.String()
	if e.Error != BlockErrorNone {
		s += ": " + e.Error.String()
	}
	return s
}

// bucket is the token bucket of one source.
type bucket struct {
	mac     ethernet.MAC
	used    bool
	limited bool
	tokens  float64
	last    time.Time
}

// refill adds the tokens earned since the last request.
func (b *bucket) refill(l RateLimit, now time.Time) {
	if d := now.Sub(b.last); d > 0 {
		b.tokens += d.Seconds() * l.Rate
	}
	if burst := float64(l.burst()); b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
}

func (l RateLimit) burst() int { return max(l.Burst, 1) }

// take spends a token. first reports the first refusal after an allowed
// request (the start of a rate-limited episode).
func (b *bucket) take() (ok, first bool) {
	if b.tokens >= 1 {
		b.tokens--
		b.limited = false
		return true, false
	}
	first = !b.limited
	b.limited = true
	return false, first
}

// limiter rate-limits by source MAC address in a fixed-size table. It is
// used by one goroutine.
type limiter struct {
	lim      RateLimit
	table    []bucket
	overflow bucket
}

func newLimiter(l RateLimit, sources int) *limiter {
	if l.Rate <= 0 {
		return nil
	}
	return &limiter{lim: l, table: make([]bucket, sources), overflow: bucket{tokens: float64(l.burst())}}
}

// allow reports whether a request from src may be served at now, and
// whether it is the first refusal of an episode. A nil limiter allows
// everything.
func (l *limiter) allow(src ethernet.MAC, now time.Time) (ok, first bool) {
	if l == nil {
		return true, false
	}
	var free, idle *bucket
	for i := range l.table {
		b := &l.table[i]
		switch {
		case b.used && b.mac == src:
			b.refill(l.lim, now)
			return b.take()
		case !b.used:
			if free == nil {
				free = b
			}
		case idle == nil:
			// A source whose bucket has refilled completely can be
			// forgotten: a new bucket for it would start full too.
			b.refill(l.lim, now)
			if b.tokens >= float64(l.lim.burst()) {
				idle = b
			}
		}
	}
	if free == nil {
		free = idle
	}
	if free == nil {
		l.overflow.refill(l.lim, now)
		return l.overflow.take()
	}
	*free = bucket{mac: src, used: true, tokens: float64(l.lim.burst()), last: now}
	return free.take()
}
