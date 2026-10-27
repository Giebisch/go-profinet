// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/store"
)

// DefaultStoreKey is the key of a device's state in its Config.Store.
const DefaultStoreKey = "device"

// What a device keeps in its store, and when:
//
//   - The station name and the IP suite: a permanent DCP Set saves the
//     new value; a temporary one saves an empty one, so that the device
//     restarts without it (the "CMINA" behaviour of IEC 61158-6-10, as
//     p-net implements it: a temporary Set clears the stored value).
//   - DHCP options: a permanent DCP Set saves the new option; a temporary
//     one leaves the stored options (as p-net).
//   - I&M1 to I&M4: every accepted write.
//   - ResetToFactory saves what it resets (empty I&M; an empty name and IP
//     suite and the configured DHCP options).
//
// A device saves before it applies: when the store fails, the DCP Set is
// refused with "resource error" and the I&M write with "application write
// error", and nothing changes. Values a controller writes at every start
// (port, interface and MRP adjustments, user records) are not kept.
//
// At start a stored value takes the place of its Config value (Config is
// the factory state). A store that cannot be read or holds a value that
// does not decode is logged, reported by Device.StoreError, and the device
// starts from Config.

// persisted is the stored state, JSON, version 1. A nil field was never
// saved.
type persisted struct {
	Version int                 `json:"version"`
	Name    *string             `json:"name,omitempty"`
	IP      *storedIP           `json:"ip,omitempty"`
	DHCP    []dcp.DHCPParameter `json:"dhcp,omitempty"`
	IM      *storedIM           `json:"im,omitempty"`
}

const persistVersion = 1

type storedIP struct {
	Addr    string `json:"addr"`
	Mask    string `json:"mask"`
	Gateway string `json:"gateway"`
}

type storedIM struct {
	IM1 pnio.IM1 `json:"im1"`
	IM2 pnio.IM2 `json:"im2"`
	IM3 pnio.IM3 `json:"im3"`
	IM4 []byte   `json:"im4"`
}

func toStoredIP(s dcp.IPSuite) *storedIP {
	str := func(a netip.Addr) string {
		if !a.IsValid() {
			return ""
		}
		return a.String()
	}
	return &storedIP{Addr: str(s.Addr), Mask: str(s.Mask), Gateway: str(s.Gateway)}
}

func (s *storedIP) suite() (dcp.IPSuite, error) {
	parse := func(v string) (netip.Addr, error) {
		if v == "" {
			return netip.Addr{}, nil
		}
		a, err := netip.ParseAddr(v)
		if err == nil && !a.Is4() {
			err = fmt.Errorf("%s is not IPv4", v)
		}
		return a, err
	}
	var out dcp.IPSuite
	var err error
	if out.Addr, err = parse(s.Addr); err != nil {
		return out, err
	}
	if out.Mask, err = parse(s.Mask); err != nil {
		return out, err
	}
	out.Gateway, err = parse(s.Gateway)
	return out, err
}

func toStoredIM(m IMData) *storedIM {
	return &storedIM{IM1: m.IM1, IM2: m.IM2, IM3: m.IM3, IM4: append([]byte(nil), m.IM4.Signature[:]...)}
}

func (s *storedIM) data() (IMData, error) {
	if len(s.IM4) != pnio.IMSignatureLen {
		return IMData{}, fmt.Errorf("I&M4 of %d octets", len(s.IM4))
	}
	m := IMData{IM1: s.IM1, IM2: s.IM2, IM3: s.IM3}
	copy(m.IM4.Signature[:], s.IM4)
	return m, nil
}

// loadState reads the stored state and applies it to the device being
// built (no lock needed). Failures are logged and kept in storeErr.
func (d *Device) loadState() {
	b, err := d.cfg.Store.Load(d.ctx, d.storeKey)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	var p persisted
	if err == nil {
		if err = json.Unmarshal(b, &p); err == nil && p.Version != persistVersion {
			err = fmt.Errorf("version %d", p.Version)
		}
	}
	var ip dcp.IPSuite
	var im IMData
	if err == nil && p.IP != nil {
		ip, err = p.IP.suite()
	}
	if err == nil && p.IM != nil {
		im, err = p.IM.data()
	}
	if err == nil && p.Name != nil && *p.Name != "" {
		err = dcp.ValidateStationName(*p.Name)
	}
	if err != nil {
		d.storeErr = fmt.Errorf("device: stored state %q: %w", d.storeKey, err)
		d.log.Warnf("%v; starting from the configuration", d.storeErr)
		return
	}
	d.stored = p
	if p.Name != nil {
		d.name = *p.Name
	}
	if p.IP != nil {
		d.ip, d.ipDHCP = ip, false
	}
	for _, o := range p.DHCP {
		d.dhcp = setDHCP(d.dhcp, o)
	}
	if p.IM != nil {
		d.im = im
	}
}

// persist applies change to a copy of the stored state and saves it; the
// stored state changes only when the save succeeded. Without a store it
// does nothing. It must not be called with d.mu held.
func (d *Device) persist(change func(p *persisted)) error {
	if d.cfg.Store == nil {
		return nil
	}
	d.storeMu.Lock()
	defer d.storeMu.Unlock()
	return d.saveLocked(change)
}

// saveLocked is persist with storeMu held.
func (d *Device) saveLocked(change func(p *persisted)) error {
	p := d.stored
	p.Version = persistVersion
	p.DHCP = cloneDHCP(p.DHCP)
	change(&p)
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := d.cfg.Store.Save(context.Background(), d.storeKey, b); err != nil {
		d.log.Warnf("device: save state %q: %v", d.storeKey, err)
		return fmt.Errorf("device: save state: %w", err)
	}
	d.stored = p
	return nil
}

// writeIM changes the I&M records, saving them first: a failed save is
// an application write error and changes nothing.
func (d *Device) writeIM(set func(m *IMData)) error {
	if d.cfg.Store == nil {
		d.mu.Lock()
		set(&d.im)
		d.mu.Unlock()
		return nil
	}
	d.storeMu.Lock()
	defer d.storeMu.Unlock()
	d.mu.Lock()
	im := d.im
	d.mu.Unlock()
	set(&im)
	if err := d.saveLocked(func(p *persisted) { p.IM = toStoredIM(im) }); err != nil {
		return rwErr(pnio.RWWriteError)
	}
	d.mu.Lock()
	d.im = im
	d.mu.Unlock()
	return nil
}

// StoreError returns why the stored state could not be used at start, or
// nil.
func (d *Device) StoreError() error { return d.storeErr }

// setDHCP replaces or appends a DHCP option.
func setDHCP(opts []dcp.DHCPParameter, p dcp.DHCPParameter) []dcp.DHCPParameter {
	p = dcp.DHCPParameter{Code: p.Code, Data: append([]byte(nil), p.Data...)}
	for i := range opts {
		if opts[i].Code == p.Code {
			opts[i] = p
			return opts
		}
	}
	return append(opts, p)
}

func checkStoreKey(k string) error {
	if err := store.ValidKey(k); err != nil {
		return fmt.Errorf("device: %w: StoreKey %q", profinet.ErrInvalidConfig, k)
	}
	return nil
}
