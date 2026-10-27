// SPDX-License-Identifier: MIT

package dcp

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// DeviceInitiative values (option 6, suboption 1).
const (
	// InitiativeNone: the device sends no DCP Hello after power-on.
	InitiativeNone uint16 = 0
	// InitiativeHello: the device sends DCP Hello after power-on.
	InitiativeHello uint16 = 1
)

// HelloMode says when a device sends DCP Hello: the values of FSHelloMode
// in the FSHelloBlock.
type HelloMode uint32

const (
	// HelloOff sends no Hello.
	HelloOff HelloMode = 0
	// HelloOnLinkUp sends a Hello sequence at power-on and whenever the
	// link comes up, at once.
	HelloOnLinkUp HelloMode = 1
	// HelloOnLinkUpDelayed waits HelloOptions.Delay after the link came
	// up before the first Hello.
	HelloOnLinkUpDelayed HelloMode = 2
)

// String returns the mode's name.
func (m HelloMode) String() string {
	switch m {
	case HelloOff:
		return "off"
	case HelloOnLinkUp:
		return "on link up"
	case HelloOnLinkUpDelayed:
		return "on link up, delayed"
	default:
		return fmt.Sprintf("HelloMode(%d)", uint32(m))
	}
}

// Limits and defaults of a Hello sequence. Vendor documents give a
// default interval of 30 ms and 1 to 15 retries, default 3.
const (
	DefaultHelloCount    = 3
	MaxHelloCount        = 15
	DefaultHelloInterval = 30 * time.Millisecond
	// MinHelloInterval bounds the rate of Hellos: two Hellos of a device
	// are never closer than this, whatever restarts its sequences.
	MinHelloInterval = 10 * time.Millisecond
	// MaxHelloDelay bounds the delay before the first Hello.
	MaxHelloDelay = time.Minute
)

// HelloOptions configure a sequence of DCP Hello requests.
type HelloOptions struct {
	// Count is the number of Hellos of a sequence, 1 to MaxHelloCount;
	// zero means DefaultHelloCount.
	Count int
	// Interval is the time between two Hellos of a sequence, at least
	// MinHelloInterval; zero means DefaultHelloInterval.
	Interval time.Duration
	// Delay is the time before the first Hello, at most MaxHelloDelay.
	Delay time.Duration
}

// Normalize returns the options with their defaults applied, or an error
// matching profinet.ErrInvalidConfig when a value is out of range.
func (o HelloOptions) Normalize() (HelloOptions, error) {
	if o.Count == 0 {
		o.Count = DefaultHelloCount
	}
	if o.Interval == 0 {
		o.Interval = DefaultHelloInterval
	}
	switch {
	case o.Count < 1 || o.Count > MaxHelloCount:
		return o, fmt.Errorf("dcp: %w: Hello count %d (1 to %d)", profinet.ErrInvalidConfig, o.Count, MaxHelloCount)
	case o.Interval < MinHelloInterval:
		return o, fmt.Errorf("dcp: %w: Hello interval %s (at least %s)", profinet.ErrInvalidConfig, o.Interval, MinHelloInterval)
	case o.Delay < 0 || o.Delay > MaxHelloDelay:
		return o, fmt.Errorf("dcp: %w: Hello delay %s (0 to %s)", profinet.ErrInvalidConfig, o.Delay, MaxHelloDelay)
	}
	return o, nil
}

// HelloFrame returns the DCP Hello request of a device: NameOfStation,
// IPParameter, DeviceID, DeviceRole and DeviceInitiative, each with its
// BlockInfo, in the order of the Hello grammar. The IP BlockInfo is the
// identity's IPInfo.
func HelloFrame(id *Identity, xid uint32) *Frame {
	init := id.Initiative
	if !id.HasInitiative {
		init = InitiativeHello
	}
	blk := func(b BlockID, info uint16, data []byte) Block {
		return Block{ID: b, HasInfo: true, Info: info, Data: data}
	}
	return &Frame{FrameID: FrameIDHello, Service: ServiceHello, Type: TypeRequest, Xid: xid, Blocks: []Block{
		blk(DeviceName, 0, []byte(id.NameOfStation)),
		blk(IPParameter, id.IPInfo, AppendIPSuite(nil, id.IP)),
		blk(DeviceID, 0, binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, id.VendorID), id.DeviceID)),
		blk(DeviceRole, 0, []byte{id.Role, 0}),
		blk(Initiative, 0, binary.BigEndian.AppendUint16(nil, init)),
	}}
}

// StartHello starts a sequence of DCP Hello requests: Count Hellos,
// Interval apart, the first after Delay. A sequence that is running is
// stopped first. A Hello is sent only while the station has a name and an
// IP address; the sequence ends early when the responder receives a DCP
// Set (a tool is configuring the device), on StopHello and on Close. Two
// Hellos are never closer than the interval, so restarting sequences
// (link flaps) cannot cause a storm.
func (r *Responder) StartHello(opts HelloOptions) error {
	opts, err := opts.Normalize()
	if err != nil {
		return err
	}
	r.helloMu.Lock()
	defer r.helloMu.Unlock()
	if r.ctx.Err() != nil {
		return fmt.Errorf("dcp: %w", profinet.ErrClosed)
	}
	if r.helloStop != nil {
		r.helloStop()
	}
	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	r.helloStop = cancel
	r.helloDone = done
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(done)
		defer cancel()
		r.helloSequence(ctx, opts)
	}()
	return nil
}

// StopHello stops the running Hello sequence, if any, and waits until it
// has ended.
func (r *Responder) StopHello() {
	r.helloMu.Lock()
	stop, done := r.helloStop, r.helloDone
	r.helloStop, r.helloDone = nil, nil
	r.helloMu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// stopHelloAsync stops the running Hello sequence without waiting; the
// responder's goroutine uses it, since the sequence may wait for it.
func (r *Responder) stopHelloAsync() {
	r.helloMu.Lock()
	if r.helloStop != nil {
		r.helloStop()
		r.helloStop, r.helloDone = nil, nil
	}
	r.helloMu.Unlock()
}

func (r *Responder) helloSequence(ctx context.Context, opts HelloOptions) {
	t := time.NewTimer(opts.Delay)
	defer t.Stop()
	for i := 0; i < opts.Count; i++ {
		if i > 0 {
			t.Reset(opts.Interval)
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
		id := r.station.Identity()
		if id.NameOfStation == "" || id.IP.IsZero() {
			r.log.Debugf("dcp: no Hello: the station has no name or no IP address")
			continue
		}
		id.MAC = r.mux.HardwareAddr()
		xid, ok := r.reserveHello(ctx, t, opts.Interval)
		if !ok {
			return
		}
		r.send(ctx, ethernet.DCPHelloMulticast, HelloFrame(&id, xid), "Hello")
		r.hellos.Add(1)
	}
}

// reserveHello waits until the last Hello of the responder, from any
// sequence, is an interval old, then claims the next send in the same
// critical section, so that a sequence that is being replaced and its
// replacement cannot both send. It returns false when ctx ends first.
func (r *Responder) reserveHello(ctx context.Context, t *time.Timer, interval time.Duration) (uint32, bool) {
	for {
		r.helloMu.Lock()
		if ctx.Err() != nil {
			r.helloMu.Unlock()
			return 0, false
		}
		wait := time.Until(r.helloLast.Add(interval))
		if wait <= 0 {
			r.helloLast = time.Now()
			r.helloXid++
			xid := r.helloXid
			r.helloMu.Unlock()
			return xid, true
		}
		r.helloMu.Unlock()
		t.Reset(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			return 0, false
		}
	}
}
