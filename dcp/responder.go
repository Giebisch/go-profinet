// SPDX-License-Identifier: MIT

package dcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
)

// Station is what a [Responder] answers for: the device's identity and the
// effect of Set requests. An IO-Device implements it; its methods are
// called on the responder's goroutine, one at a time.
type Station interface {
	// Identity returns the current identity.
	Identity() Identity

	// SetName assigns (or with an empty name removes) the station name.
	SetName(name string, permanent bool) BlockError
	// SetIP assigns (or with a zero suite removes) the IP suite.
	SetIP(s IPSuite, permanent bool) BlockError
	// Signal flashes the signal LED.
	Signal() BlockError
	// ResetToFactory resets the device; mode is one of the Reset* values.
	ResetToFactory(mode uint16) BlockError
}

// DHCPSetter is implemented by a [Station] that accepts a DCP Set of the
// DHCP options (option 3). A responder whose station does not implement
// it refuses such a Set with BlockErrorOptionUnsupported, as p-net answers
// a Set of a block it only reports (DeviceInitiative). go-profinet has no
// DHCP client: the station decides what a DHCP option means for the
// operating system's addressing.
type DHCPSetter interface {
	// SetDHCP stores (and applies) one DHCP option; p.Code is the
	// block's suboption.
	SetDHCP(p DHCPParameter, permanent bool) BlockError
}

// ResponderOptions configure a [Responder].
type ResponderOptions struct {
	// Logger receives diagnostics. nil logs nothing.
	Logger profinet.Logger
	// NoResponseDelay answers Identify requests at once instead of
	// spreading the response over the requested delay. For tests.
	NoResponseDelay bool
	// MaxPendingIdentify bounds the delayed Identify responses waiting to
	// be sent. Zero means 16.
	MaxPendingIdentify int
	// Policy restricts the responder: read-only, rate limits, audit
	// events. The zero value restricts nothing.
	Policy Policy
}

// ResponderStats are the counters of a [Responder].
type ResponderStats struct {
	Identify, Get, Set, Malformed, Ignored uint64
	// RateLimited counts the Identify responses and Set requests dropped
	// by the policy's rate limits; Refused the Set blocks refused, by the
	// read-only mode or the station.
	RateLimited, Refused uint64
	// Hello is the number of DCP Hello requests sent.
	Hello uint64
}

// Responder answers DCP requests for one [Station] on a port.
type Responder struct {
	mux     *l2.Mux
	sub     *l2.Subscription
	station Station
	opts    ResponderOptions
	log     profinet.Logger
	delay   time.Duration

	identify, get, set, malformed, ignored, hellos atomic.Uint64
	rateLimited, refused                           atomic.Uint64
	pendingIdentify                                atomic.Int32

	// identifyLim and setLim are the policy's rate limits (nil: none);
	// only the run goroutine uses them.
	identifyLim, setLim *limiter

	// helloMu guards the running Hello sequence (see StartHello).
	helloMu   sync.Mutex
	helloStop context.CancelFunc
	helloDone chan struct{}
	helloLast time.Time
	helloXid  uint32

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewResponder starts answering DCP requests on mux.
func NewResponder(mux *l2.Mux, station Station, opts ResponderOptions) (*Responder, error) {
	if opts.MaxPendingIdentify <= 0 {
		opts.MaxPendingIdentify = 16
	}
	pol, err := opts.Policy.normalize()
	if err != nil {
		return nil, err
	}
	opts.Policy = pol
	log := opts.Logger
	if log == nil {
		log = profinet.NopLogger()
	}
	sub, err := mux.Subscribe(l2.MatchFrameIDs(FrameIDGetSet, FrameIDIdentifyRequest), 64)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Responder{mux: mux, sub: sub, station: station, opts: opts, log: log, ctx: ctx, cancel: cancel,
		identifyLim: newLimiter(pol.IdentifyLimit, pol.Sources), setLim: newLimiter(pol.SetLimit, pol.Sources)}
	// The spread of Identify responses: a fixed fraction of the window,
	// derived from the MAC address as IEC 61158-6-10 suggests.
	h := fnv.New32a()
	m := mux.HardwareAddr()
	h.Write(m[:])
	r.delay = time.Duration(h.Sum32()%1000) * time.Microsecond * 10
	r.wg.Add(1)
	go r.run(ctx)
	return r, nil
}

// Stats returns a snapshot of the counters.
func (r *Responder) Stats() ResponderStats {
	return ResponderStats{
		Identify:  r.identify.Load(),
		Get:       r.get.Load(),
		Set:       r.set.Load(),
		Malformed: r.malformed.Load(),
		Ignored:   r.ignored.Load(),
		Hello:     r.hellos.Load(),

		RateLimited: r.rateLimited.Load(),
		Refused:     r.refused.Load(),
	}
}

// Close stops the responder.
func (r *Responder) Close() error {
	r.once.Do(func() {
		// Under helloMu: StartHello starts no sequence after this.
		r.helloMu.Lock()
		r.cancel()
		r.helloMu.Unlock()
		r.sub.Close()
		r.wg.Wait()
	})
	return nil
}

func (r *Responder) run(ctx context.Context) {
	defer r.wg.Done()
	own := r.mux.HardwareAddr()
	for {
		ef, err := r.sub.Recv(ctx)
		if err != nil {
			return
		}
		f, err := Decode(ef.Payload)
		if err != nil {
			r.malformed.Add(1)
			r.log.Debugf("dcp: drop request from %s: %v", ef.Src, err)
			continue
		}
		if f.Type != TypeRequest {
			continue
		}
		switch {
		case f.FrameID == FrameIDIdentifyRequest && f.Service == ServiceIdentify:
			r.handleIdentify(ctx, ef.Src, &f)
		case f.FrameID == FrameIDGetSet && ef.Dst == own && f.Service == ServiceGet:
			r.get.Add(1)
			r.handleGet(ctx, ef.Src, &f)
		case f.FrameID == FrameIDGetSet && ef.Dst == own && f.Service == ServiceSet:
			if !r.admit(r.setLim, ef.Src, ServiceSet) {
				continue
			}
			r.set.Add(1)
			r.handleSet(ctx, ef.Src, &f)
		case f.FrameID == FrameIDGetSet && ef.Dst == own:
			r.audit(AuditEvent{Source: ef.Src, Service: f.Service, Outcome: AuditUnsupported})
			r.reply(ctx, ef.Src, &Frame{FrameID: FrameIDGetSet, Service: f.Service, Type: TypeResponseUnsupported, Xid: f.Xid})
		default:
			r.ignored.Add(1)
		}
	}
}

// matches reports whether the identity matches every filter block.
func matches(id *Identity, filter []Block) bool {
	if len(filter) == 0 {
		return false
	}
	for _, fb := range filter {
		switch fb.ID {
		case AllSelector:
		case DeviceAlias:
			if id.Alias == "" || id.Alias != string(fb.Data) {
				return false
			}
		case IPMAC:
			if !bytes.Equal(fb.Data, id.MAC[:]) {
				return false
			}
		default:
			b, ok := id.Block(fb.ID)
			if !ok || !bytes.Equal(b.Data, fb.Data) {
				return false
			}
		}
	}
	return true
}

func (r *Responder) handleIdentify(ctx context.Context, src ethernet.MAC, f *Frame) {
	id := r.station.Identity()
	id.MAC = r.mux.HardwareAddr()
	if !matches(&id, f.Blocks) {
		r.ignored.Add(1)
		return
	}
	if !r.admit(r.identifyLim, src, ServiceIdentify) {
		return
	}
	r.identify.Add(1)
	resp := &Frame{FrameID: FrameIDIdentifyResponse, Service: ServiceIdentify, Type: TypeResponseSuccess, Xid: f.Xid, Blocks: id.Blocks()}
	delay := time.Duration(0)
	if f.ResponseDelay > 1 && !r.opts.NoResponseDelay {
		window := time.Duration(f.ResponseDelay) * 10 * time.Millisecond
		delay = r.delay % window
	}
	if delay == 0 {
		r.reply(ctx, src, resp)
		return
	}
	if int(r.pendingIdentify.Load()) >= r.opts.MaxPendingIdentify {
		r.ignored.Add(1)
		return
	}
	r.pendingIdentify.Add(1)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.pendingIdentify.Add(-1)
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-t.C:
			r.reply(ctx, src, resp)
		case <-ctx.Done():
		}
	}()
}

func (r *Responder) handleGet(ctx context.Context, src ethernet.MAC, f *Frame) {
	id := r.station.Identity()
	id.MAC = r.mux.HardwareAddr()
	resp := &Frame{FrameID: FrameIDGetSet, Service: ServiceGet, Type: TypeResponseSuccess, Xid: f.Xid}
	for _, b := range f.Blocks {
		if b.ID == AllSelector {
			resp.Blocks = append(resp.Blocks, Block{ID: IPMAC, HasInfo: true, Data: append([]byte(nil), id.MAC[:]...)})
			resp.Blocks = append(resp.Blocks, id.Blocks()...)
			continue
		}
		if blk, ok := id.Block(b.ID); ok {
			resp.Blocks = append(resp.Blocks, blk)
			continue
		}
		code := BlockErrorSuboptionUnsupport
		switch b.ID.Option() {
		case OptionIP, OptionDeviceProperties:
		case OptionDHCP:
			// The option is supported when the station reports any DHCP
			// option; a code it does not report is then an unsupported
			// suboption.
			if len(id.DHCP) == 0 {
				code = BlockErrorOptionUnsupported
			}
		default:
			code = BlockErrorOptionUnsupported
		}
		resp.Blocks = append(resp.Blocks, Block{ID: CtrlResponse, Data: AppendControlResponse(nil, ControlResponse{Block: b.ID, Error: code})})
	}
	r.reply(ctx, src, resp)
}

func (r *Responder) handleSet(ctx context.Context, src ethernet.MAC, f *Frame) {
	// A tool is configuring the device: it no longer announces itself.
	r.stopHelloAsync()
	resp := &Frame{FrameID: FrameIDGetSet, Service: ServiceSet, Type: TypeResponseSuccess, Xid: f.Xid}
	for _, b := range f.Blocks {
		outcome := AuditApplied
		var code BlockError
		if r.opts.Policy.ReadOnly && changes(b.ID) {
			outcome, code = AuditReadOnly, BlockErrorSetNotPossible
		} else if code = r.apply(b); code != BlockErrorNone {
			outcome = AuditRefused
		}
		if code != BlockErrorNone {
			r.refused.Add(1)
		}
		if b.ID != CtrlStart && b.ID != CtrlStop {
			r.audit(AuditEvent{Source: src, Service: ServiceSet, Block: b.ID, Qualifier: b.Info, Outcome: outcome, Error: code})
		}
		resp.Blocks = append(resp.Blocks, Block{ID: CtrlResponse, Data: AppendControlResponse(nil, ControlResponse{Block: b.ID, Error: code})})
	}
	r.reply(ctx, src, resp)
}

// changes reports whether a Set block changes the device: every block but
// the transaction brackets and Signal.
func changes(id BlockID) bool {
	return id != CtrlStart && id != CtrlStop && id != CtrlSignal
}

// admit applies a rate limit to a request from src; a dropped request is
// counted, and audited when it starts an episode.
func (r *Responder) admit(l *limiter, src ethernet.MAC, s ServiceID) bool {
	ok, first := l.allow(src, time.Now())
	if ok {
		return true
	}
	r.rateLimited.Add(1)
	if first {
		r.audit(AuditEvent{Source: src, Service: s, Outcome: AuditRateLimited})
	}
	return false
}

// audit reports an event to the policy's callback and the logger, with
// fields when the logger takes them.
func (r *Responder) audit(e AuditEvent) {
	e.Time = time.Now()
	if fl, ok := r.log.(profinet.FieldLogger); ok {
		kv := []any{"source", e.Source.String(), "service", e.Service.String(), "outcome", e.Outcome.String()}
		if e.Block != 0 {
			kv = append(kv, "block", e.Block.String(), "qualifier", e.Qualifier)
		}
		if e.Error != BlockErrorNone {
			kv = append(kv, "error", e.Error.String())
		}
		fl.InfoKV("dcp audit", kv...)
	} else {
		r.log.Infof("%s", e)
	}
	if r.opts.Policy.Audit != nil {
		r.opts.Policy.Audit(e)
	}
}

func (r *Responder) apply(b Block) BlockError {
	permanent := b.Info&QualifierPermanent != 0
	switch b.ID {
	case CtrlStart, CtrlStop:
		return BlockErrorNone
	case DeviceName:
		name := string(b.Data)
		if name != "" && ValidateStationName(name) != nil {
			return BlockErrorSetNotPossible
		}
		return r.station.SetName(name, permanent)
	case IPParameter:
		s, err := DecodeIPSuite(b.Data)
		if err != nil {
			return BlockErrorSetNotPossible
		}
		if !s.IsZero() && !validSuite(s) {
			return BlockErrorSetNotPossible
		}
		return r.station.SetIP(s, permanent)
	case CtrlSignal:
		if len(b.Data) < 2 || binary.BigEndian.Uint16(b.Data) != SignalFlashOnce {
			return BlockErrorSetNotPossible
		}
		return r.station.Signal()
	case CtrlReset:
		return r.station.ResetToFactory(b.Info)
	case CtrlFactory:
		return r.station.ResetToFactory(ResetAllData)
	default:
		if b.ID.Option() == OptionDHCP {
			return r.setDHCP(b, permanent)
		}
		switch b.ID.Option() {
		case OptionIP, OptionDeviceProperties, OptionControl:
			return BlockErrorSuboptionUnsupport
		default:
			return BlockErrorOptionUnsupported
		}
	}
}

// setDHCP passes a Set of a DHCP option to the station, if it accepts
// one.
func (r *Responder) setDHCP(b Block, permanent bool) BlockError {
	ds, ok := r.station.(DHCPSetter)
	if !ok {
		return BlockErrorOptionUnsupported
	}
	p, err := DecodeDHCPParameter(b.Data)
	if err != nil || p.Code != b.ID.Suboption() {
		return BlockErrorSetNotPossible
	}
	p.Data = append([]byte(nil), p.Data...)
	return ds.SetDHCP(p, permanent)
}

// validSuite checks an IP suite of a Set: a unicast address, a contiguous
// mask, an address that is not the subnet's network or broadcast address,
// and a gateway that is unset, equal to the address (PROFINET's way of
// saying "no gateway") or in the subnet.
func validSuite(s IPSuite) bool {
	if !s.Addr.Is4() || !s.Mask.Is4() {
		return false
	}
	a4 := s.Addr.As4()
	if a4[0] == 0 || a4[0] == 127 || a4[0] >= 224 {
		return false
	}
	addr := binary.BigEndian.Uint32(a4[:])
	m4 := s.Mask.As4()
	m := binary.BigEndian.Uint32(m4[:])
	if inv := ^m; m == 0 || inv&(inv+1) != 0 {
		return false
	}
	if host := addr &^ m; m < 0xFFFFFFFE && (host == 0 || host == ^m) {
		return false
	}
	if s.Gateway.Is4() && !s.Gateway.IsUnspecified() && s.Gateway != s.Addr {
		g4 := s.Gateway.As4()
		if binary.BigEndian.Uint32(g4[:])&m != addr&m {
			return false
		}
	}
	return true
}

func (r *Responder) reply(ctx context.Context, dst ethernet.MAC, f *Frame) {
	r.send(ctx, dst, f, f.Service.String()+" response")
}

func (r *Responder) send(ctx context.Context, dst ethernet.MAC, f *Frame, what string) {
	hdr := ethernet.Frame{Dst: dst, Src: r.mux.HardwareAddr(), EtherType: ethernet.EtherTypeProfinet}
	b := hdr.AppendHeader(make([]byte, 0, 256))
	b, err := f.AppendTo(b)
	if err != nil {
		r.log.Errorf("dcp: encode %s: %v", what, err)
		return
	}
	if err := r.mux.Send(ctx, ethernet.Pad(b, 0)); err != nil && ctx.Err() == nil {
		r.log.Warnf("dcp: send %s to %s: %v", what, dst, err)
	}
}
