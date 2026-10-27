// SPDX-License-Identifier: MIT

package dcp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
)

// SetError reports a block of a Set (or Get) request that the device
// rejected with a BlockError. It matches profinet.ErrRejected.
type SetError struct {
	MAC   ethernet.MAC
	Block BlockID
	Code  BlockError
}

func (e *SetError) Error() string {
	return fmt.Sprintf("dcp: %s rejected %s: %s", e.MAC, e.Block, e.Code)
}

// Is makes a SetError match profinet.ErrRejected, and profinet.ErrUnsupported
// for the codes that report an unsupported option.
func (e *SetError) Is(target error) bool {
	if target == profinet.ErrRejected {
		return true
	}
	return target == profinet.ErrUnsupported && (e.Code == BlockErrorOptionUnsupported || e.Code == BlockErrorSuboptionUnsupport)
}

// ClientOptions configure a [Client].
type ClientOptions struct {
	// Timeout is how long Get and Set wait for the response to one
	// transmission. Zero means 1 s.
	Timeout time.Duration
	// Retries is how often Get and Set are retransmitted (with the same
	// transaction ID) when no response arrives. Zero means 3, which
	// outlasts the 3 s a device such as p-net ignores a requester while it
	// serves another; a negative value disables retransmission.
	Retries int
	// Logger receives diagnostics. nil logs nothing.
	Logger profinet.Logger
}

// Client is a DCP requester on one port. It is safe for concurrent use;
// every request has its own transaction ID.
type Client struct {
	mux  *l2.Mux
	sub  *l2.Subscription
	opts ClientOptions
	log  profinet.Logger

	xid atomic.Uint32

	mu      sync.Mutex
	pending map[uint32]chan response
	hellos  []chan Identity

	malformed atomic.Uint64

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

type response struct {
	src   ethernet.MAC
	frame Frame
}

// NewClient starts a requester on mux.
func NewClient(mux *l2.Mux, opts ClientOptions) (*Client, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = time.Second
	}
	if opts.Retries == 0 {
		opts.Retries = 3
	} else if opts.Retries < 0 {
		opts.Retries = 0
	}
	log := opts.Logger
	if log == nil {
		log = profinet.NopLogger()
	}
	sub, err := mux.Subscribe(l2.MatchFrameIDs(FrameIDHello, FrameIDIdentifyResponse), 256)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		mux:     mux,
		sub:     sub,
		opts:    opts,
		log:     log,
		pending: make(map[uint32]chan response),
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	// Transaction IDs start from a value derived from the MAC address
	// and the time, so that two requesters on a segment rarely collide.
	m := mux.HardwareAddr()
	c.xid.Store(binary.BigEndian.Uint32(m[2:]) ^ uint32(time.Now().UnixNano()))
	go c.run(ctx)
	return c, nil
}

// Close stops the requester. Pending requests fail with profinet.ErrClosed.
func (c *Client) Close() error {
	c.once.Do(func() {
		c.cancel()
		c.sub.Close()
		<-c.done
	})
	return nil
}

// Malformed returns the number of DCP frames that could not be decoded.
func (c *Client) Malformed() uint64 { return c.malformed.Load() }

func (c *Client) run(ctx context.Context) {
	defer close(c.done)
	for {
		ef, err := c.sub.Recv(ctx)
		if err != nil {
			return
		}
		f, err := Decode(ef.Payload)
		if err != nil {
			c.malformed.Add(1)
			c.log.Debugf("dcp: drop frame from %s: %v", ef.Src, err)
			continue
		}
		if f.Service == ServiceHello && f.Type == TypeRequest {
			c.hello(ef.Src, &f)
			continue
		}
		if f.Type == TypeRequest {
			continue
		}
		// Copy what the waiter keeps: the subscription reuses its buffer.
		cp := Frame{FrameID: f.FrameID, Service: f.Service, Type: f.Type, Xid: f.Xid}
		cp.Blocks = make([]Block, len(f.Blocks))
		for i, b := range f.Blocks {
			b.Data = append([]byte(nil), b.Data...)
			cp.Blocks[i] = b
		}
		c.mu.Lock()
		ch := c.pending[f.Xid]
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		select {
		case ch <- response{src: ef.Src, frame: cp}:
		default:
			c.log.Warnf("dcp: drop response from %s: queue of transaction 0x%08X full", ef.Src, f.Xid)
		}
	}
}

func (c *Client) hello(src ethernet.MAC, f *Frame) {
	id, err := ParseIdentity(src, f.Blocks)
	if err != nil {
		c.malformed.Add(1)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.hellos {
		select {
		case ch <- id:
		default:
		}
	}
}

// Hellos returns a channel that receives the identity of every device
// that sends a DCP Hello, until ctx is done. Hellos that arrive while the
// channel is full are dropped.
func (c *Client) Hellos(ctx context.Context, buffer int) <-chan Identity {
	if buffer < 1 {
		buffer = 16
	}
	ch := make(chan Identity, buffer)
	c.mu.Lock()
	c.hellos = append(c.hellos, ch)
	c.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-c.done:
		}
		c.mu.Lock()
		for i, v := range c.hellos {
			if v == ch {
				c.hellos = append(c.hellos[:i:i], c.hellos[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		close(ch)
	}()
	return ch
}

func (c *Client) register(queue int) (uint32, chan response) {
	ch := make(chan response, queue)
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		x := c.xid.Add(1)
		if _, used := c.pending[x]; !used {
			c.pending[x] = ch
			return x, ch
		}
	}
}

func (c *Client) unregister(x uint32) {
	c.mu.Lock()
	delete(c.pending, x)
	c.mu.Unlock()
}

func (c *Client) send(ctx context.Context, dst ethernet.MAC, f *Frame) error {
	hdr := ethernet.Frame{Dst: dst, Src: c.mux.HardwareAddr(), EtherType: ethernet.EtherTypeProfinet}
	b := hdr.AppendHeader(make([]byte, 0, 128))
	b, err := f.AppendTo(b)
	if err != nil {
		return err
	}
	return c.mux.Send(ctx, ethernet.Pad(b, 0))
}

// Filter is the list of blocks an Identify request filters on; a device
// answers when it matches every block.
type Filter []Block

// FilterAll matches every device.
func FilterAll() Filter { return Filter{{ID: AllSelector}} }

// FilterName matches the device with this NameOfStation.
func FilterName(name string) Filter { return Filter{{ID: DeviceName, Data: []byte(name)}} }

// FilterAlias matches the device with this AliasName.
func FilterAlias(alias string) Filter { return Filter{{ID: DeviceAlias, Data: []byte(alias)}} }

// FilterDeviceID matches devices of one vendor and device ID.
func FilterDeviceID(vendor, device uint16) Filter {
	return Filter{{ID: DeviceID, Data: binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, vendor), device)}}
}

// FilterIP matches the device with this IP suite.
func FilterIP(s IPSuite) Filter { return Filter{{ID: IPParameter, Data: AppendIPSuite(nil, s)}} }

// IdentifyOptions tune an Identify request.
type IdentifyOptions struct {
	// ResponseDelay is the ResponseDelayFactor: devices spread their
	// responses over ResponseDelay × 10 ms. Zero or one means no spread.
	ResponseDelay uint16
	// Wait is how long responses are collected. Zero means the spread
	// plus 1 s.
	Wait time.Duration
	// Max stops collecting after this many responses; zero collects
	// until Wait elapses.
	Max int
}

// Identify multicasts an Identify request and collects the responses.
// It returns the devices found when the wait elapses, ctx is done or Max
// responses arrived; finding no device is not an error. A device that
// answers twice (a duplicate frame) is reported once.
func (c *Client) Identify(ctx context.Context, filter Filter, opts IdentifyOptions) ([]Identity, error) {
	if len(filter) == 0 {
		filter = FilterAll()
	}
	wait := opts.Wait
	if wait <= 0 {
		wait = time.Duration(opts.ResponseDelay)*10*time.Millisecond + time.Second
	}
	x, ch := c.register(256)
	defer c.unregister(x)
	f := Frame{FrameID: FrameIDIdentifyRequest, Service: ServiceIdentify, Type: TypeRequest, Xid: x, ResponseDelay: opts.ResponseDelay}
	for _, b := range filter {
		b.HasInfo = false
		f.Blocks = append(f.Blocks, b)
	}
	if opts.ResponseDelay == 0 {
		f.ResponseDelay = 1
	}
	if err := c.send(ctx, ethernet.DCPIdentifyMulticast, &f); err != nil {
		return nil, fmt.Errorf("dcp: identify: %w", err)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	var out []Identity
	seen := map[ethernet.MAC]bool{}
	for {
		select {
		case r := <-ch:
			if r.frame.Service != ServiceIdentify || r.frame.Type != TypeResponseSuccess || seen[r.src] {
				continue
			}
			id, err := ParseIdentity(r.src, r.frame.Blocks)
			if err != nil {
				c.malformed.Add(1)
				c.log.Debugf("dcp: identify response from %s: %v", r.src, err)
				continue
			}
			seen[r.src] = true
			out = append(out, id)
			if opts.Max > 0 && len(out) >= opts.Max {
				return out, nil
			}
		case <-t.C:
			return out, nil
		case <-ctx.Done():
			if len(out) > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return out, nil
			}
			return out, ctx.Err()
		case <-c.done:
			return out, fmt.Errorf("dcp: identify: %w", profinet.ErrClosed)
		}
	}
}

// IdentifyName finds the device with a station name. It returns an error
// matching profinet.ErrNotFound when none answers within wait (1 s when
// zero), and the first answer when several devices claim the name (see
// [Client.Identify] to detect duplicates).
func (c *Client) IdentifyName(ctx context.Context, name string, wait time.Duration) (Identity, error) {
	ids, err := c.Identify(ctx, FilterName(name), IdentifyOptions{Wait: wait, Max: 1})
	if err != nil {
		return Identity{}, err
	}
	if len(ids) == 0 {
		return Identity{}, fmt.Errorf("dcp: station %q: %w", name, profinet.ErrNotFound)
	}
	return ids[0], nil
}

// transact sends a unicast Get or Set and waits for its response,
// retransmitting with the same transaction ID.
func (c *Client) transact(ctx context.Context, dst ethernet.MAC, f *Frame) (Frame, error) {
	x, ch := c.register(4)
	defer c.unregister(x)
	f.Xid = x
	f.FrameID = FrameIDGetSet
	f.Type = TypeRequest
	for attempt := 0; attempt <= c.opts.Retries; attempt++ {
		if err := c.send(ctx, dst, f); err != nil {
			return Frame{}, fmt.Errorf("dcp: %s %s: %w", f.Service, dst, err)
		}
		t := time.NewTimer(c.opts.Timeout)
		for {
			select {
			case r := <-ch:
				if r.src != dst || r.frame.Service != f.Service {
					continue
				}
				t.Stop()
				if r.frame.Type == TypeResponseUnsupported {
					return r.frame, fmt.Errorf("dcp: %s %s: %w: service not supported by the device", f.Service, dst, profinet.ErrUnsupported)
				}
				return r.frame, nil
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return Frame{}, ctx.Err()
			case <-c.done:
				t.Stop()
				return Frame{}, fmt.Errorf("dcp: %s: %w", f.Service, profinet.ErrClosed)
			}
			break
		}
	}
	return Frame{}, fmt.Errorf("dcp: %s %s: %w", f.Service, dst, profinet.ErrTimeout)
}

// Get reads blocks from the device with MAC address dst. The result holds
// the blocks the device returned; a block it rejected is reported as a
// *SetError (the first one) alongside the blocks it did return.
func (c *Client) Get(ctx context.Context, dst ethernet.MAC, ids ...BlockID) ([]Block, error) {
	if len(ids) == 0 {
		ids = []BlockID{AllSelector}
	}
	f := Frame{Service: ServiceGet}
	for _, id := range ids {
		f.Blocks = append(f.Blocks, Block{ID: id})
	}
	r, err := c.transact(ctx, dst, &f)
	if err != nil {
		return nil, err
	}
	var out []Block
	var first error
	for _, b := range r.Blocks {
		if b.ID == CtrlResponse {
			cr, err := DecodeControlResponse(b.Data)
			if err != nil {
				return out, err
			}
			if cr.Error != BlockErrorNone && first == nil {
				first = &SetError{MAC: dst, Block: cr.Block, Code: cr.Error}
			}
			continue
		}
		out = append(out, b)
	}
	return out, first
}

// GetIdentity reads the identity blocks of one device. It asks for the
// blocks by name, not with the AllSelector, which devices such as p-net do
// not answer for Get.
func (c *Client) GetIdentity(ctx context.Context, dst ethernet.MAC) (Identity, error) {
	bs, err := c.Get(ctx, dst, DeviceName, IPParameter, DeviceVendor, DeviceID, DeviceRole, DeviceInst)
	if err != nil && len(bs) == 0 {
		return Identity{}, err
	}
	return ParseIdentity(dst, bs)
}

// SetBlock is one block of a Set request.
type SetBlock struct {
	ID BlockID
	// Qualifier is the BlockQualifier: QualifierTemporary or
	// QualifierPermanent for name and IP, the mode for ResetToFactory.
	Qualifier uint16
	Data      []byte
}

// Set writes blocks to the device with MAC address dst. It returns a
// *SetError for the first block the device rejected, and an error
// matching profinet.ErrMalformed when a block is not acknowledged.
//
// Set changes the configuration of an industrial device. Only call it on
// an explicit request of the user.
func (c *Client) Set(ctx context.Context, dst ethernet.MAC, blocks ...SetBlock) error {
	if len(blocks) == 0 {
		return fmt.Errorf("dcp: set: %w: no blocks", profinet.ErrInvalidConfig)
	}
	f := Frame{Service: ServiceSet}
	for _, b := range blocks {
		f.Blocks = append(f.Blocks, Block{ID: b.ID, HasInfo: true, Info: b.Qualifier, Data: b.Data})
	}
	r, err := c.transact(ctx, dst, &f)
	if err != nil {
		return err
	}
	acked := map[BlockID]bool{}
	for _, b := range r.Blocks {
		if b.ID != CtrlResponse {
			continue
		}
		cr, err := DecodeControlResponse(b.Data)
		if err != nil {
			return err
		}
		if cr.Error != BlockErrorNone {
			return &SetError{MAC: dst, Block: cr.Block, Code: cr.Error}
		}
		acked[cr.Block] = true
	}
	for _, b := range blocks {
		if b.ID != CtrlStart && b.ID != CtrlStop && !acked[b.ID] {
			return fmt.Errorf("dcp: set %s on %s: %w: no response for the block", b.ID, dst, profinet.ErrMalformed)
		}
	}
	return nil
}

func qualifier(permanent bool) uint16 {
	if permanent {
		return QualifierPermanent
	}
	return QualifierTemporary
}

// SetName assigns a station name. An empty name removes the name. The name
// is validated first.
func (c *Client) SetName(ctx context.Context, dst ethernet.MAC, name string, permanent bool) error {
	if name != "" {
		if err := ValidateStationName(name); err != nil {
			return err
		}
	}
	return c.Set(ctx, dst, SetBlock{ID: DeviceName, Qualifier: qualifier(permanent), Data: []byte(name)})
}

// SetIP assigns an IP suite. An all-zero suite removes the address.
func (c *Client) SetIP(ctx context.Context, dst ethernet.MAC, s IPSuite, permanent bool) error {
	for _, a := range []struct {
		name string
		ok   bool
	}{{"address", !s.Addr.IsValid() || s.Addr.Is4()}, {"mask", !s.Mask.IsValid() || s.Mask.Is4()}, {"gateway", !s.Gateway.IsValid() || s.Gateway.Is4()}} {
		if !a.ok {
			return fmt.Errorf("dcp: set IP: %w: %s is not IPv4", profinet.ErrInvalidConfig, a.name)
		}
	}
	return c.Set(ctx, dst, SetBlock{ID: IPParameter, Qualifier: qualifier(permanent), Data: AppendIPSuite(nil, s)})
}

// SetDHCP writes one DHCP option (DCP option 3, the option code as
// suboption). Devices without DHCP support answer "option not supported"
// (a *SetError matching profinet.ErrUnsupported).
func (c *Client) SetDHCP(ctx context.Context, dst ethernet.MAC, p DHCPParameter, permanent bool) error {
	v, err := AppendDHCPParameter(nil, p)
	if err != nil {
		return err
	}
	return c.Set(ctx, dst, SetBlock{ID: p.BlockID(), Qualifier: qualifier(permanent), Data: v})
}

// Signal makes the device flash its signal LED.
func (c *Client) Signal(ctx context.Context, dst ethernet.MAC) error {
	return c.Set(ctx, dst, SetBlock{ID: CtrlSignal, Data: binary.BigEndian.AppendUint16(nil, SignalFlashOnce)})
}

// ResetToFactory resets the device; mode is one of the Reset* values.
func (c *Client) ResetToFactory(ctx context.Context, dst ethernet.MAC, mode uint16) error {
	return c.Set(ctx, dst, SetBlock{ID: CtrlReset, Qualifier: mode})
}
