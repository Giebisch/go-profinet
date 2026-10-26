// SPDX-License-Identifier: MIT

package lldp

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
)

// LocalInfo is what an agent announces about its port.
type LocalInfo struct {
	// StationName is the NameOfStation; empty sends the MAC address as
	// the chassis ID.
	StationName string
	// PortName is the port's name, "port-001" for the first port.
	PortName string
	// Description is sent as the system description.
	Description string
	// IP is sent as the management address when valid.
	IP netip.Addr
	// Legacy sends the port ID without the station name (the LLDP mode
	// of GSDML versions before 2.3).
	Legacy bool
}

// Build returns the LLDPDU an agent with this information sends from mac,
// in the "standard" mode of IEC 61158-6-10: chassis ID the station name,
// port ID "<port>.<station>".
func (l LocalInfo) Build(mac ethernet.MAC, ttl uint16) Frame {
	port := l.PortName
	if port == "" {
		port = "port-001"
	}
	f := Frame{
		TTL:               ttl,
		SystemDescription: l.Description,
		PortStatus:        &PortStatus{},
		ChassisMAC:        &mac,
		MACPHY:            &MACPHY{Autoneg: 0x03, PMDCapability: 0x6C00, MAUType: 16},
		ManagementAddr:    l.IP,
	}
	if l.StationName == "" {
		f.ChassisSubtype, f.ChassisID = ChassisSubtypeMAC, append([]byte(nil), mac[:]...)
		f.PortSubtype, f.PortID = PortSubtypeLocal, []byte(port)
	} else {
		f.ChassisSubtype, f.ChassisID = ChassisSubtypeLocal, []byte(l.StationName)
		f.PortSubtype = PortSubtypeLocal
		if l.Legacy {
			f.PortID = []byte(port)
		} else {
			f.PortID = []byte(port + "." + l.StationName)
		}
		f.SystemName = l.StationName
	}
	return f
}

// Neighbor is an entry of the neighbor table.
type Neighbor struct {
	// MAC is the source address of the neighbor's frames.
	MAC     ethernet.MAC `json:"mac"`
	Frame   Frame        `json:"frame"`
	Updated time.Time    `json:"updated"`
	Expires time.Time    `json:"expires"`
}

// Alias returns the PROFINET AliasName of the device connected to this
// neighbor: the neighbor's port ID, followed by "." and its chassis ID
// unless the port ID already carries the station name.
func (n *Neighbor) Alias() string {
	p := n.Frame.PortString()
	if strings.Contains(p, ".") {
		return p
	}
	return p + "." + n.Frame.ChassisString()
}

// AgentOptions configure an [Agent].
type AgentOptions struct {
	// Interval is the transmit interval; zero means 5 s. A negative value
	// makes the agent receive only.
	Interval time.Duration
	// TTLFactor × Interval is the TTL announced; zero means 4.
	TTLFactor int
	// Local returns the information to announce; it is called for every
	// frame. nil makes the agent receive only.
	Local func() LocalInfo
	// MaxNeighbors bounds the table; zero means 16.
	MaxNeighbors int
	Logger       profinet.Logger
}

// Agent sends LLDP frames periodically and keeps the neighbor table.
type Agent struct {
	mux  *l2.Mux
	sub  *l2.Subscription
	opts AgentOptions

	mu        sync.Mutex
	neighbors map[string]*Neighbor

	malformed atomic.Uint64
	sent      atomic.Uint64

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewAgent starts an agent on mux.
func NewAgent(mux *l2.Mux, opts AgentOptions) (*Agent, error) {
	if opts.Interval == 0 {
		opts.Interval = 5 * time.Second
	}
	if opts.TTLFactor <= 0 {
		opts.TTLFactor = 4
	}
	if opts.MaxNeighbors <= 0 {
		opts.MaxNeighbors = 16
	}
	if opts.Logger == nil {
		opts.Logger = profinet.NopLogger()
	}
	if err := l2.JoinMulticast(mux.Port(), ethernet.LLDPMulticast); err != nil {
		return nil, err
	}
	sub, err := mux.Subscribe(l2.MatchEtherType(ethernet.EtherTypeLLDP), 32)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &Agent{mux: mux, sub: sub, opts: opts, neighbors: map[string]*Neighbor{}, cancel: cancel}
	a.wg.Add(1)
	go a.recv(ctx)
	if opts.Interval > 0 && opts.Local != nil {
		a.wg.Add(1)
		go a.send(ctx)
	}
	return a, nil
}

// Close stops the agent.
func (a *Agent) Close() {
	a.once.Do(func() {
		a.cancel()
		a.sub.Close()
		a.wg.Wait()
	})
}

// Sent returns the number of frames sent.
func (a *Agent) Sent() uint64 { return a.sent.Load() }

// Malformed returns the number of frames that could not be decoded.
func (a *Agent) Malformed() uint64 { return a.malformed.Load() }

// SendNow sends one frame at once, for example after the station name
// changed.
func (a *Agent) SendNow(ctx context.Context) error {
	if a.opts.Local == nil {
		return nil
	}
	ttl := uint16(min(int(a.opts.Interval.Seconds())*a.opts.TTLFactor, 65535))
	f := a.opts.Local().Build(a.mux.HardwareAddr(), max(ttl, 1))
	h := ethernet.Frame{Dst: ethernet.LLDPMulticast, Src: a.mux.HardwareAddr(), EtherType: ethernet.EtherTypeLLDP}
	b := h.AppendHeader(make([]byte, 0, 256))
	b, err := f.AppendTo(b)
	if err != nil {
		return err
	}
	if err := a.mux.Send(ctx, ethernet.Pad(b, 0)); err != nil {
		return err
	}
	a.sent.Add(1)
	return nil
}

func (a *Agent) send(ctx context.Context) {
	defer a.wg.Done()
	t := time.NewTicker(a.opts.Interval)
	defer t.Stop()
	for {
		if err := a.SendNow(ctx); err != nil && ctx.Err() == nil {
			a.opts.Logger.Debugf("lldp: send: %v", err)
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

func (a *Agent) recv(ctx context.Context) {
	defer a.wg.Done()
	for {
		ef, err := a.sub.Recv(ctx)
		if err != nil {
			return
		}
		// A frame of our own address is our own LLDP coming back (a bridge
		// that reflects it, a capture of outgoing frames): it is not a
		// neighbor.
		if ef.Src == a.mux.HardwareAddr() {
			continue
		}
		f, err := Decode(ef.Payload)
		if err != nil {
			a.malformed.Add(1)
			continue
		}
		a.update(ef.Src, f, time.Now())
	}
}

func (a *Agent) update(src ethernet.MAC, f Frame, now time.Time) {
	k := string(f.ChassisID) + "\x00" + string(f.PortID)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked(now)
	if f.TTL == 0 {
		delete(a.neighbors, k)
		return
	}
	n, ok := a.neighbors[k]
	if !ok {
		if len(a.neighbors) >= a.opts.MaxNeighbors {
			return
		}
		n = &Neighbor{}
		a.neighbors[k] = n
	}
	n.MAC, n.Frame, n.Updated = src, f, now
	n.Expires = now.Add(time.Duration(f.TTL) * time.Second)
}

func (a *Agent) expireLocked(now time.Time) {
	for k, n := range a.neighbors {
		if now.After(n.Expires) {
			delete(a.neighbors, k)
		}
	}
}

// Neighbors returns the neighbors whose information has not expired,
// ordered by chassis and port ID.
func (a *Agent) Neighbors() []Neighbor {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked(time.Now())
	out := make([]Neighbor, 0, len(a.neighbors))
	for _, n := range a.neighbors {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool {
		if c := strings.Compare(string(out[i].Frame.ChassisID), string(out[j].Frame.ChassisID)); c != 0 {
			return c < 0
		}
		return string(out[i].Frame.PortID) < string(out[j].Frame.PortID)
	})
	return out
}

// Alias returns the AliasName derived from the first neighbor, or "" when
// there is none.
func (a *Agent) Alias() string {
	ns := a.Neighbors()
	if len(ns) == 0 {
		return ""
	}
	return ns[0].Alias()
}
