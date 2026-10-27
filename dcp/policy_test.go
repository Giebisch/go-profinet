// SPDX-License-Identifier: MIT

package dcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

func TestLimiter(t *testing.T) {
	if l := newLimiter(RateLimit{}, 4); l != nil {
		t.Fatal("limiter without a rate")
	}
	var none *limiter
	if ok, _ := none.allow(ethernet.MAC{1}, time.Now()); !ok {
		t.Fatal("nil limiter refused")
	}
	l := newLimiter(RateLimit{Rate: 10, Burst: 2}, 2)
	t0 := time.Unix(1000, 0)
	a, b, c, d := ethernet.MAC{1}, ethernet.MAC{2}, ethernet.MAC{3}, ethernet.MAC{4}
	step := func(src ethernet.MAC, at time.Duration, wantOK, wantFirst bool) {
		t.Helper()
		ok, first := l.allow(src, t0.Add(at))
		if ok != wantOK || first != wantFirst {
			t.Fatalf("%s at %v: %v %v, want %v %v", src, at, ok, first, wantOK, wantFirst)
		}
	}
	// A burst of 2, then refused; only the first refusal starts an episode.
	step(a, 0, true, false)
	step(a, 0, true, false)
	step(a, 0, false, true)
	step(a, 0, false, false)
	// 100 ms later one token is back.
	step(a, 100*time.Millisecond, true, false)
	step(a, 100*time.Millisecond, false, true)
	// b takes the second slot; c finds no free or idle slot and uses the
	// shared overflow budget, as does d.
	step(b, 100*time.Millisecond, true, false)
	step(b, 100*time.Millisecond, true, false)
	step(c, 100*time.Millisecond, true, false)
	step(d, 100*time.Millisecond, true, false)
	step(c, 100*time.Millisecond, false, true)
	step(d, 100*time.Millisecond, false, false)
	// After a second every bucket is full again: c evicts an idle source
	// and gets its own bucket.
	step(c, 1100*time.Millisecond, true, false)
	step(c, 1100*time.Millisecond, true, false)
	step(c, 1100*time.Millisecond, false, true)
	if len(l.table) != 2 {
		t.Fatalf("table of %d", len(l.table))
	}
}

func TestPolicyNormalize(t *testing.T) {
	for _, p := range []Policy{{Sources: -1}, {Sources: maxSources + 1}, {SetLimit: RateLimit{Rate: math.NaN()}}} {
		if _, err := NewResponder(nil, nil, ResponderOptions{Policy: p}); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%+v: %v", p, err)
		}
	}
	p, err := Policy{}.normalize()
	if err != nil || p.Sources != defaultSources {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestAuditStrings(t *testing.T) {
	for o, want := range map[AuditOutcome]string{AuditApplied: "applied", AuditRefused: "refused", AuditReadOnly: "read-only",
		AuditRateLimited: "rate-limited", AuditUnsupported: "unsupported", 9: "AuditOutcome(9)"} {
		if o.String() != want {
			t.Errorf("%d: %s", o, o)
		}
	}
	e := AuditEvent{Source: ethernet.MAC{2, 0, 0, 0, 0, 1}, Service: ServiceSet, Block: DeviceName, Qualifier: 1, Outcome: AuditReadOnly, Error: BlockErrorSetNotPossible}
	if got := e.String(); got != "dcp audit: Set from 02:00:00:00:00:01 block Device/NameOfStation qualifier 1 read-only: SET not possible by local reasons" {
		t.Errorf("%q", got)
	}
	e = AuditEvent{Source: ethernet.MAC{2, 0, 0, 0, 0, 1}, Service: ServiceIdentify, Outcome: AuditRateLimited}
	if got := e.String(); got != "dcp audit: Identify from 02:00:00:00:00:01 rate-limited" {
		t.Errorf("%q", got)
	}
}

// auditLog collects audit events.
type auditLog struct {
	mu sync.Mutex
	ev []AuditEvent
}

func (a *auditLog) add(e AuditEvent) {
	a.mu.Lock()
	a.ev = append(a.ev, e)
	a.mu.Unlock()
}

func (a *auditLog) events() []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditEvent(nil), a.ev...)
}

// TestReadOnly: Set blocks that change the device are refused with "SET
// not possible by local reasons" and audited; Signal, Identify and Get
// still work; the station is untouched.
func TestReadOnly(t *testing.T) {
	st := testStation()
	audit := &auditLog{}
	var logBuf strings.Builder
	var logMu sync.Mutex
	log := profinet.NewSlogLogger(slog.NewTextHandler(lockedWriter{&logMu, &logBuf}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r, c, _ := newResponderClient(t, st, ResponderOptions{Logger: log, Policy: Policy{ReadOnly: true, Audit: audit.add}})
	ctx := context.Background()
	for name, err := range map[string]error{
		"name":  c.SetName(ctx, devMAC, "other", true),
		"ip":    c.SetIP(ctx, devMAC, IPSuite{}, false),
		"reset": c.ResetToFactory(ctx, devMAC, ResetCommunication),
		"dhcp":  c.SetDHCP(ctx, devMAC, DHCPControlParameter(DHCPControlOn), false),
	} {
		if !isBlockError(err, BlockErrorSetNotPossible) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := c.Signal(ctx, devMAC); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, devMAC, SetBlock{ID: CtrlStart}, SetBlock{ID: CtrlStop}); err != nil {
		t.Fatal(err)
	}
	if id, err := c.GetIdentity(ctx, devMAC); err != nil || id.NameOfStation != "dev" {
		t.Fatalf("get %+v %v", id, err)
	}
	if ids, err := c.Identify(ctx, FilterAll(), IdentifyOptions{Wait: 100 * time.Millisecond}); err != nil || len(ids) != 1 {
		t.Fatalf("identify %v %v", ids, err)
	}
	st.mu.Lock()
	if st.id.NameOfStation != "dev" || st.id.IP.IsZero() || len(st.resets) != 0 || st.signals != 1 {
		t.Fatalf("station changed: %+v", st)
	}
	st.mu.Unlock()
	ev := audit.events()
	var got []string
	for _, e := range ev {
		if e.Source != clientMAC || e.Service != ServiceSet || e.Time.IsZero() {
			t.Fatalf("event %+v", e)
		}
		got = append(got, fmt.Sprintf("%s %s", e.Block, e.Outcome))
	}
	want := "Device/NameOfStation read-only,IP/Parameter read-only,Control/ResetToFactory read-only,DHCP/Control read-only,Control/Signal applied"
	if strings.Join(sortedCopy(got), ",") != strings.Join(sortedCopy(strings.Split(want, ",")), ",") {
		t.Fatalf("audit %v", got)
	}
	if s := r.Stats(); s.Refused != 4 {
		t.Fatalf("stats %+v", s)
	}
	logMu.Lock()
	out := logBuf.String()
	logMu.Unlock()
	if !strings.Contains(out, `msg="dcp audit"`) || !strings.Contains(out, "source=02:00:00:00:00:10") || !strings.Contains(out, "block=Device/NameOfStation") ||
		!strings.Contains(out, "outcome=read-only") || !strings.Contains(out, `error="SET not possible by local reasons"`) {
		t.Fatalf("log %s", out)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TestDefaultAudits: without a policy every Set block is still logged, and
// a refused block and an unsupported service are audited.
func TestDefaultAudits(t *testing.T) {
	st := testStation()
	log := &recLogger{}
	audit := &auditLog{}
	r, c, raw := newResponderClient(t, st, ResponderOptions{Logger: log, Policy: Policy{Audit: audit.add}})
	ctx := context.Background()
	if err := c.SetName(ctx, devMAC, "renamed", false); err != nil {
		t.Fatal(err)
	}
	if err := c.ResetToFactory(ctx, devMAC, ResetDevice); !isBlockError(err, BlockErrorSetNotPossible) {
		t.Fatal(err)
	}
	raw.send(t, devMAC, &Frame{FrameID: FrameIDGetSet, Service: 9, Type: TypeRequest, Xid: 1})
	waitFor(t, "audit", func() bool { return len(audit.events()) == 3 })
	ev := audit.events()
	if ev[0].Block != DeviceName || ev[0].Outcome != AuditApplied || ev[1].Block != CtrlReset || ev[1].Outcome != AuditRefused ||
		ev[1].Error != BlockErrorSetNotPossible || ev[1].Qualifier != ResetDevice || ev[2].Service != 9 || ev[2].Outcome != AuditUnsupported || ev[2].Source != rawMAC {
		t.Fatalf("events %+v", ev)
	}
	if !log.has("INFO dcp audit: Set from 02:00:00:00:00:10 block Device/NameOfStation qualifier 0 applied") {
		t.Fatalf("log %v", log.msgs)
	}
	if s := r.Stats(); s.Refused != 1 || s.RateLimited != 0 {
		t.Fatalf("stats %+v", s)
	}
}

// TestRateLimits: Identify responses and Set requests beyond a source's
// budget are dropped; another source keeps its own budget; one audit
// event per episode.
func TestRateLimits(t *testing.T) {
	st := testStation()
	audit := &auditLog{}
	r, c, raw := newResponderClient(t, st, ResponderOptions{Policy: Policy{
		IdentifyLimit: RateLimit{Rate: 0.001, Burst: 2}, SetLimit: RateLimit{Rate: 0.001, Burst: 1}, Audit: audit.add}})
	identify := func(d *rawDev, xid uint32) bool {
		d.send(t, ethernet.DCPIdentifyMulticast, &Frame{FrameID: FrameIDIdentifyRequest, Service: ServiceIdentify, Type: TypeRequest, Xid: xid, Blocks: FilterAll()})
		_, f, ok := d.recv(t, 150*time.Millisecond)
		return ok && f.FrameID == FrameIDIdentifyResponse
	}
	for i, want := range []bool{true, true, false, false} {
		if got := identify(raw, uint32(i)); got != want {
			t.Fatalf("identify %d: %v", i, got)
		}
	}
	// Requests that the device does not answer anyway cost nothing.
	raw.send(t, ethernet.DCPIdentifyMulticast, &Frame{FrameID: FrameIDIdentifyRequest, Service: ServiceIdentify, Type: TypeRequest, Xid: 9, Blocks: FilterName("x")})
	ctx := context.Background()
	if err := c.SetName(ctx, devMAC, "a", false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetName(ctx, devMAC, "b", false); !errors.Is(err, profinet.ErrTimeout) {
		t.Fatalf("second set: %v", err)
	}
	// The client is another source: its Identify is answered.
	if ids, err := c.Identify(ctx, FilterAll(), IdentifyOptions{Wait: 100 * time.Millisecond}); err != nil || len(ids) != 1 {
		t.Fatalf("other source %v %v", ids, err)
	}
	s := r.Stats()
	if s.RateLimited < 3 || s.Identify != 3 {
		t.Fatalf("stats %+v", s)
	}
	var limited []string
	for _, e := range audit.events() {
		if e.Outcome == AuditRateLimited {
			limited = append(limited, fmt.Sprintf("%s %s", e.Service, e.Source))
		}
	}
	if strings.Join(limited, ",") != "Identify 02:00:00:00:00:77,Set 02:00:00:00:00:10" {
		t.Fatalf("rate-limit events %v", limited)
	}
}
