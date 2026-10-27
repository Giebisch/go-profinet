// SPDX-License-Identifier: MIT

// Package snmp implements a read-only SNMPv1/SNMPv2c agent (RFC 1157,
// RFC 1901, RFC 3416) with the BER codec of its messages (RFC 3417) and
// helpers for the MIB modules a PROFINET device of conformance class B
// offers: the system, interfaces and snmp groups of MIB-II (RFC 1213,
// RFC 3418, RFC 2863) and the LLDP-MIB of IEEE 802.1AB-2005.
//
// The agent answers GetRequest, GetNextRequest and GetBulkRequest from a
// [MIB] and refuses every SetRequest; it sends no notifications (traps).
// SNMPv3 and its user-based security model are not implemented: a v3
// message is dropped and counted. The community string is the only access
// control, and it travels in clear text: run the agent on isolated
// networks only.
//
// The agent serves on a net.PacketConn the caller opens (the operating
// system's UDP socket, or a netstack socket on a virtual network); it
// starts no goroutines of its own until [Agent.Serve].
package snmp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
)

// Port is the UDP port of an SNMP agent (RFC 3417 section 3.1).
const Port = 161

// Defaults of [Config].
const (
	// DefaultMaxMessageSize keeps a response in one Ethernet frame
	// without IP fragmentation: 1500 − 20 (IPv4) − 8 (UDP).
	DefaultMaxMessageSize = 1472
	// MinMessageSize is the smallest MaxMessageSize: every SNMP entity
	// accepts messages of at least 484 octets (RFC 3417 section 3.2).
	MinMessageSize      = 484
	DefaultRepetitions  = 32
	DefaultBulkVarBinds = 256
	DefaultRate         = 1000
	DefaultBurst        = 200
)

// Config configures an [Agent].
type Config struct {
	// Community is the community string a request must carry;
	// required. It is compared in constant time.
	Community string
	// MIB is the tree the agent serves; required.
	MIB *MIB
	// MaxMessageSize bounds a response; zero means
	// DefaultMaxMessageSize, and values below MinMessageSize are
	// raised to it. A Get or GetNext whose response would be larger is
	// answered with tooBig; a GetBulk response is truncated.
	MaxMessageSize int
	// MaxRepetitions caps the max-repetitions of a GetBulkRequest; zero
	// means DefaultRepetitions.
	MaxRepetitions int
	// MaxBulkVarBinds caps the variable bindings of a GetBulk response;
	// zero means DefaultBulkVarBinds.
	MaxBulkVarBinds int
	// Rate and Burst limit the requests the agent processes: Rate per
	// second on average, Burst at once. Excess datagrams are dropped
	// and counted (Stats.RateLimited). Zero means DefaultRate and
	// DefaultBurst; a negative Rate disables the limit.
	Rate  float64
	Burst int
	// Logger receives diagnostics. nil logs nothing.
	Logger profinet.Logger
}

// Stats are the counters of an [Agent]. The first ones are the objects
// of the snmp group (RFC 3418 section 2, RFC 1213 section 6.11);
// [MIB.AddSNMPGroup] serves them.
type Stats struct {
	// InPkts counts the datagrams the agent processed (snmpInPkts).
	InPkts uint64
	// OutPkts counts the responses sent (snmpOutPkts).
	OutPkts uint64
	// InBadVersions counts messages of a version other than v1 and v2c
	// (snmpInBadVersions).
	InBadVersions uint64
	// InBadCommunityNames counts messages with a wrong community
	// (snmpInBadCommunityNames).
	InBadCommunityNames uint64
	// InBadCommunityUses counts requests the community does not allow
	// (snmpInBadCommunityUses); always zero: a SetRequest is answered
	// as read-only, not refused for its community.
	InBadCommunityUses uint64
	// InASNParseErrs counts messages that did not decode
	// (snmpInASNParseErrs).
	InASNParseErrs uint64
	// InGetRequests, InGetNexts and InSetRequests count the requests
	// of each type (snmpInGetRequests, snmpInGetNexts,
	// snmpInSetRequests); InGetBulks has no object of its own.
	InGetRequests uint64
	InGetNexts    uint64
	InGetBulks    uint64
	InSetRequests uint64
	// OutTooBigs, OutNoSuchNames and OutGetResponses count the
	// responses with tooBig, with noSuchName and every response
	// (snmpOutTooBigs, snmpOutNoSuchNames, snmpOutGetResponses).
	OutTooBigs      uint64
	OutNoSuchNames  uint64
	OutGetResponses uint64
	// SilentDrops counts requests whose tooBig response did not fit
	// either (snmpSilentDrops, RFC 3416 section 4.2.1).
	SilentDrops uint64
	// UnsupportedPDUs counts well-formed messages that are not requests
	// (responses, traps, informs, reports): an agent ignores them.
	UnsupportedPDUs uint64
	// RateLimited counts datagrams dropped by the rate limit.
	RateLimited uint64
}

type counters struct {
	inPkts, outPkts, inBadVersions, inBadCommunityNames, inASNParseErrs                  atomic.Uint64
	inGetRequests, inGetNexts, inGetBulks, inSetRequests                                 atomic.Uint64
	outTooBigs, outNoSuchNames, outGetResponses, silentDrops, unsupportedPDUs, rateLimit atomic.Uint64
}

// Agent is a read-only SNMPv1/v2c agent. Its methods are safe for
// concurrent use.
type Agent struct {
	conn      net.PacketConn
	owned     bool
	cfg       Config
	community [sha256.Size]byte
	log       profinet.Logger
	c         counters

	// lim guards the token bucket.
	lim    sync.Mutex
	tokens float64
	last   time.Time

	mu      sync.Mutex
	serving bool
	closed  bool
	serveWG sync.WaitGroup
}

// NewAgent returns an agent that serves on conn once [Agent.Serve] runs.
// With owned, Close closes conn.
func NewAgent(conn net.PacketConn, owned bool, cfg Config) (*Agent, error) {
	if conn == nil || cfg.MIB == nil || cfg.Community == "" {
		return nil, fmt.Errorf("snmp: %w: a connection, a MIB and a community are required", profinet.ErrInvalidConfig)
	}
	if cfg.MaxMessageSize == 0 {
		cfg.MaxMessageSize = DefaultMaxMessageSize
	}
	cfg.MaxMessageSize = min(max(cfg.MaxMessageSize, MinMessageSize), MaxMessageSize)
	if cfg.MaxRepetitions <= 0 {
		cfg.MaxRepetitions = DefaultRepetitions
	}
	if cfg.MaxBulkVarBinds <= 0 {
		cfg.MaxBulkVarBinds = DefaultBulkVarBinds
	}
	if cfg.Rate == 0 {
		cfg.Rate = DefaultRate
	}
	if cfg.Burst <= 0 {
		cfg.Burst = DefaultBurst
	}
	a := &Agent{conn: conn, owned: owned, cfg: cfg, log: cfg.Logger, community: sha256.Sum256([]byte(cfg.Community)), tokens: float64(cfg.Burst)}
	if a.log == nil {
		a.log = profinet.NopLogger()
	}
	return a, nil
}

// LocalAddr returns the address of the agent's socket.
func (a *Agent) LocalAddr() net.Addr { return a.conn.LocalAddr() }

// Stats returns a snapshot of the counters.
func (a *Agent) Stats() Stats {
	c := &a.c
	return Stats{
		InPkts: c.inPkts.Load(), OutPkts: c.outPkts.Load(), InBadVersions: c.inBadVersions.Load(),
		InBadCommunityNames: c.inBadCommunityNames.Load(), InASNParseErrs: c.inASNParseErrs.Load(),
		InGetRequests: c.inGetRequests.Load(), InGetNexts: c.inGetNexts.Load(), InGetBulks: c.inGetBulks.Load(),
		InSetRequests: c.inSetRequests.Load(), OutTooBigs: c.outTooBigs.Load(), OutNoSuchNames: c.outNoSuchNames.Load(),
		OutGetResponses: c.outGetResponses.Load(), SilentDrops: c.silentDrops.Load(), UnsupportedPDUs: c.unsupportedPDUs.Load(),
		RateLimited: c.rateLimit.Load(),
	}
}

// pastDeadline unblocks a pending ReadFrom.
var pastDeadline = time.Unix(1, 0)

// Serve answers requests until ctx is done or the agent is closed; it
// returns nil after Close, ctx.Err() when ctx is done, and the error of
// the socket otherwise. Only one Serve runs at a time.
func (a *Agent) Serve(ctx context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("snmp: serve: %w", profinet.ErrClosed)
	}
	if a.serving {
		a.mu.Unlock()
		return fmt.Errorf("snmp: serve: %w: already serving", profinet.ErrInvalidState)
	}
	a.serving = true
	a.serveWG.Add(1)
	// Under mu: Close sets a past deadline under mu as well, and this
	// must not undo it.
	_ = a.conn.SetReadDeadline(time.Time{})
	a.mu.Unlock()
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			a.mu.Lock()
			_ = a.conn.SetReadDeadline(pastDeadline)
			a.mu.Unlock()
		case <-quit:
		}
	}()
	defer func() {
		close(quit)
		wg.Wait()
		a.mu.Lock()
		a.serving = false
		a.mu.Unlock()
		a.serveWG.Done()
	}()
	buf := make([]byte, MaxMessageSize+1)
	for {
		n, addr, err := a.conn.ReadFrom(buf)
		if err != nil {
			a.mu.Lock()
			closed := a.closed
			a.mu.Unlock()
			switch {
			case closed:
				return nil
			case ctx.Err() != nil:
				return ctx.Err()
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return fmt.Errorf("snmp: read: %w", err)
		}
		if !a.allow(time.Now()) {
			a.c.rateLimit.Add(1)
			continue
		}
		resp := a.Handle(buf[:n])
		if resp == nil {
			continue
		}
		if _, err := a.conn.WriteTo(resp, addr); err != nil {
			a.log.Debugf("snmp: response to %s: %v", addr, err)
			continue
		}
		a.c.outPkts.Add(1)
	}
}

// Close stops Serve and waits for it to return; with an owned
// connection it closes the connection. It is safe to call twice.
func (a *Agent) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	_ = a.conn.SetReadDeadline(pastDeadline)
	a.mu.Unlock()
	a.serveWG.Wait()
	if a.owned {
		return a.conn.Close()
	}
	return nil
}

// allow takes a token from the bucket.
func (a *Agent) allow(now time.Time) bool {
	if a.cfg.Rate < 0 {
		return true
	}
	a.lim.Lock()
	defer a.lim.Unlock()
	if !a.last.IsZero() {
		a.tokens = min(a.tokens+now.Sub(a.last).Seconds()*a.cfg.Rate, float64(a.cfg.Burst))
	}
	a.last = now
	if a.tokens < 1 {
		return false
	}
	a.tokens--
	return true
}

// Handle processes one request datagram and returns the response to
// send, or nil when the request is dropped. Serve calls it for every
// datagram; it is exported for transports that are not a
// net.PacketConn. It does not apply the rate limit.
func (a *Agent) Handle(b []byte) []byte {
	a.c.inPkts.Add(1)
	m, err := Decode(b)
	if err != nil {
		var ve *VersionError
		if errors.As(err, &ve) {
			// A message of another version is discarded and counted
			// (RFC 1157 section 4.1 step 2, RFC 3412 section 4.2.1).
			a.c.inBadVersions.Add(1)
		} else {
			a.c.inASNParseErrs.Add(1)
		}
		a.log.Debugf("snmp: drop: %v", err)
		return nil
	}
	got := sha256.Sum256(m.Community)
	if subtle.ConstantTimeCompare(got[:], a.community[:]) != 1 {
		// A message that fails authentication is discarded (RFC 1157
		// section 4.1, step 3); no authenticationFailure trap is sent.
		a.c.inBadCommunityNames.Add(1)
		return nil
	}
	switch m.PDU.Type {
	case PDUGetRequest:
		a.c.inGetRequests.Add(1)
	case PDUGetNextRequest:
		a.c.inGetNexts.Add(1)
	case PDUGetBulkRequest:
		a.c.inGetBulks.Add(1)
	case PDUSetRequest:
		a.c.inSetRequests.Add(1)
	default:
		// Responses, traps, informs and reports go to managers and
		// notification receivers, not to a command responder: dropped.
		a.c.unsupportedPDUs.Add(1)
		return nil
	}
	resp := a.respond(&m)
	if resp == nil {
		a.c.silentDrops.Add(1)
		return nil
	}
	a.c.outGetResponses.Add(1)
	return resp
}

// respond builds the response to a request; nil when even its tooBig
// form exceeds MaxMessageSize.
func (a *Agent) respond(req *Message) []byte {
	v1 := req.Version == Version1
	view := a.cfg.MIB.view(v1)
	resp := Message{Version: req.Version, Community: req.Community, PDU: PDU{Type: PDUResponse, RequestID: req.PDU.RequestID}}
	in := req.PDU.VarBinds
	var out []VarBind
	fail := func(status ErrorStatus, index int) {
		// An error response has the variable bindings of the request
		// (RFC 1157 section 4.1.2 to 4.1.5: "of identical form";
		// RFC 3416 section 4.2.5 for SetRequest).
		resp.PDU.ErrorStatus, resp.PDU.ErrorIndex, out = status, int32(index), in
	}
	switch req.PDU.Type {
	case PDUGetRequest:
		out = make([]VarBind, 0, len(in))
		for i, vb := range in {
			val := view.get(vb.Name)
			if v1 && val.Type.IsException() {
				// RFC 1157 section 4.1.2: noSuchName and the index of the
				// first name that is not available.
				fail(NoSuchName, i+1)
				break
			}
			out = append(out, VarBind{Name: vb.Name, Value: val})
		}
	case PDUGetNextRequest:
		out = make([]VarBind, 0, len(in))
		for i, vb := range in {
			next, ok := view.next(vb.Name)
			if !ok {
				if v1 {
					// RFC 1157 section 4.1.3: no name lexicographically
					// follows.
					fail(NoSuchName, i+1)
					break
				}
				// RFC 3416 section 4.2.2: endOfMibView with the name of
				// the request.
				next = VarBind{Name: vb.Name, Value: Value{Type: TypeEndOfMibView}}
			}
			out = append(out, next)
		}
	case PDUGetBulkRequest:
		return a.bulk(&resp, view, req)
	default: // PDUSetRequest
		if len(in) > 0 {
			if v1 {
				// RFC 1157 section 4.1.5: an object "not available for
				// set operations in the relevant MIB view" is answered
				// with noSuchName and its index. readOnly(4) is defined
				// in section 4.1.1 but no procedure of RFC 1157 returns
				// it, and RFC 3584 section 4.4 maps SNMPv2's notWritable
				// to noSuchName for an SNMPv1 manager as well.
				fail(NoSuchName, 1)
			} else {
				// RFC 3416 section 4.2.5, step 2: no variable with the
				// name's prefix can be created or modified: notWritable,
				// the index of the first variable binding.
				fail(NotWritable, 1)
			}
		}
	}
	if resp.PDU.ErrorStatus == NoSuchName {
		a.c.outNoSuchNames.Add(1)
	}
	if b, ok := a.encode(&resp, out); ok {
		return b
	}
	// Too big: v1 answers "of identical form" with tooBig and index 0
	// (RFC 1157 section 4.1.2), v2c with tooBig, index 0 and no variable
	// bindings (RFC 3416 section 4.2.1).
	resp.PDU.ErrorStatus, resp.PDU.ErrorIndex = TooBig, 0
	out = nil
	if v1 {
		out = in
	}
	b, ok := a.encode(&resp, out)
	if !ok {
		// "the snmpSilentDrops counter is incremented and the resultant
		// message is discarded" (RFC 3416 section 4.2.1).
		return nil
	}
	a.c.outTooBigs.Add(1)
	return b
}

// encode encodes resp with vbs if it fits MaxMessageSize.
func (a *Agent) encode(resp *Message, vbs []VarBind) ([]byte, bool) {
	n := 0
	for i := range vbs {
		n += varBindLen(&vbs[i])
	}
	size := resp.messageLen(n)
	if size > a.cfg.MaxMessageSize {
		return nil, false
	}
	return resp.appendTo(make([]byte, 0, size), vbs), true
}

// bulk answers a GetBulkRequest (RFC 3416 section 4.2.3): N non-repeaters
// once, then up to M repetitions of the R others, truncated at the end to
// fit MaxMessageSize and MaxBulkVarBinds.
func (a *Agent) bulk(resp *Message, view *view, req *Message) []byte {
	if resp.messageLen(0) > a.cfg.MaxMessageSize {
		return nil
	}
	in := req.PDU.VarBinds
	n := min(max(int(req.PDU.NonRepeaters), 0), len(in))
	m := min(max(int(req.PDU.MaxRepetitions), 0), a.cfg.MaxRepetitions)
	r := len(in) - n
	var out []VarBind
	size := 0
	full := false
	add := func(vb VarBind) bool {
		l := varBindLen(&vb)
		if len(out) == a.cfg.MaxBulkVarBinds || resp.messageLen(size+l) > a.cfg.MaxMessageSize {
			full = true
			return false
		}
		out = append(out, vb)
		size += l
		return true
	}
	next := func(name OID) VarBind {
		vb, ok := view.next(name)
		if !ok {
			return VarBind{Name: name, Value: Value{Type: TypeEndOfMibView}}
		}
		return vb
	}
	for i := 0; i < n && !full; i++ {
		add(next(in[i].Name))
	}
	cur := make([]OID, r)
	for j := range cur {
		cur[j] = in[n+j].Name
	}
	for rep := 0; rep < m && r > 0 && !full; rep++ {
		ended := true
		for j := 0; j < r; j++ {
			vb := next(cur[j])
			if !add(vb) {
				break
			}
			cur[j] = vb.Name
			if vb.Value.Type != TypeEndOfMibView {
				ended = false
			}
		}
		if ended {
			// Every repeater is at the end: "the variable bindings may
			// be truncated after the (N + (i * R))-th variable binding".
			break
		}
	}
	return resp.appendTo(make([]byte, 0, resp.messageLen(size)), out)
}
