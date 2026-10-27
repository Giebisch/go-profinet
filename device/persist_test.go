// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/store"
)

// flakyStore fails Save while fail is set.
type flakyStore struct {
	*store.Memory
	fail bool
}

func (s *flakyStore) Save(ctx context.Context, key string, v []byte) error {
	if s.fail {
		return errors.New("disk full")
	}
	return s.Memory.Save(ctx, key, v)
}

func withStoreCfg(s store.Store, key string) func(*Config) {
	return func(c *Config) { c.Store, c.StoreKey = s, key }
}

func stored(t *testing.T, s store.Store, key string) persisted {
	t.Helper()
	b, err := s.Load(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPersistState: each DCP Set, I&M write and reset saves the state the
// device restarts with, and a restarted device takes it.
func TestPersistState(t *testing.T) {
	n := newNet(t)
	st := store.NewMemory()
	d, _ := n.device(nil, withStoreCfg(st, "unit"))
	s := station{d}
	if code := s.SetName("plant-1", true); code != dcp.BlockErrorNone {
		t.Fatal(code)
	}
	if p := stored(t, st, "unit"); p.Version != persistVersion || p.Name == nil || *p.Name != "plant-1" {
		t.Fatalf("%+v", p)
	}
	if code := s.SetName("temp", false); code != dcp.BlockErrorNone || d.StationName() != "temp" {
		t.Fatal(code)
	}
	if p := stored(t, st, "unit"); *p.Name != "" {
		t.Fatalf("a temporary name was stored: %+v", p)
	}
	if code := s.SetIP(suite("192.168.0.77"), true); code != dcp.BlockErrorNone {
		t.Fatal(code)
	}
	if p := stored(t, st, "unit"); p.IP == nil || p.IP.Addr != "192.168.0.77" || p.IP.Gateway != "0.0.0.0" {
		t.Fatalf("%+v", p.IP)
	}
	// A temporary suite stores an empty one.
	if code := s.SetIP(suite("192.168.0.78"), false); code != dcp.BlockErrorNone {
		t.Fatal(code)
	}
	if p := stored(t, st, "unit"); p.IP.Addr != "" {
		t.Fatalf("%+v", p.IP)
	}
	// I&M.
	if err := d.writeIM(func(m *IMData) { m.IM1.TagFunction = "pump"; m.IM4.Signature[0] = 9 }); err != nil {
		t.Fatal(err)
	}
	if p := stored(t, st, "unit"); p.IM == nil || p.IM.IM1.TagFunction != "pump" || p.IM.IM4[0] != 9 {
		t.Fatalf("%+v", p.IM)
	}
	_ = d.Close()

	// A new device on the store: the stored values.
	d2, _ := n.device(nil, withStoreCfg(st, "unit"))
	d2.mu.Lock()
	im := d2.im
	d2.mu.Unlock()
	if d2.StationName() != "" || !d2.IP().IsZero() || im.IM1.TagFunction != "pump" || im.IM4.Signature[0] != 9 || d2.StoreError() != nil {
		t.Fatalf("restarted: %q %s %+v %v", d2.StationName(), d2.IP(), im, d2.StoreError())
	}
	// ResetToFactory saves what it resets.
	if code := (station{d2}).ResetToFactory(dcp.ResetAllData); code != dcp.BlockErrorNone {
		t.Fatal(code)
	}
	p := stored(t, st, "unit")
	if p.IM == nil || p.IM.IM1.TagFunction != "" || p.Name == nil || *p.Name != "" || p.DHCP != nil {
		t.Fatalf("after reset %+v", p)
	}
}

// TestPersistFailures: a failing store refuses the change and keeps the
// device as it was.
func TestPersistFailures(t *testing.T) {
	n := newNet(t)
	st := &flakyStore{Memory: store.NewMemory()}
	d, _ := n.device(nil, withStoreCfg(st, "f"), func(c *Config) {
		c.SetDHCP = func(dcp.DHCPParameter, bool) dcp.BlockError { return dcp.BlockErrorNone }
	})
	st.fail = true
	s := station{d}
	for name, code := range map[string]dcp.BlockError{
		"name":  s.SetName("x", true),
		"ip":    s.SetIP(suite("192.168.0.99"), true),
		"dhcp":  s.SetDHCP(dcp.DHCPClientIDFromMAC(), true),
		"reset": s.ResetToFactory(dcp.ResetCommunication),
	} {
		if code != dcp.BlockErrorResource {
			t.Errorf("%s: %v", name, code)
		}
	}
	if d.StationName() != "dut" || d.IP() != suite(devIP) || len(d.DHCP()) != 0 {
		t.Fatalf("changed: %q %s %v", d.StationName(), d.IP(), d.DHCP())
	}
	err := d.writeIM(func(m *IMData) { m.IM1.TagFunction = "x" })
	var pe *pnio.StatusError
	if !errors.As(err, &pe) || pe.Status.Code1 != pnio.RWWriteError {
		t.Fatalf("I&M: %v", err)
	}
	// A temporary DHCP option does not touch the store.
	if code := s.SetDHCP(dcp.DHCPClientIDFromMAC(), false); code != dcp.BlockErrorNone {
		t.Fatal(code)
	}
	st.fail = false
	if code := s.SetDHCP(dcp.DHCPControlParameter(dcp.DHCPControlOn), true); code != dcp.BlockErrorNone {
		t.Fatal(code)
	}
	if p := stored(t, st, "f"); len(p.DHCP) != 1 {
		t.Fatalf("%+v", p.DHCP)
	}
	// Without a store, I&M writes still apply.
	d2, _ := newNet(t).device(nil)
	if err := d2.writeIM(func(m *IMData) { m.IM2.Date = "2026-10-28 10:00" }); err != nil {
		t.Fatal(err)
	}
	if err := d2.persist(func(*persisted) {}); err != nil {
		t.Fatal(err)
	}
}

// TestPersistLoad: what a device does with stored state it cannot use.
func TestPersistLoad(t *testing.T) {
	for name, tc := range map[string]struct {
		value string
		ok    bool
	}{
		"not json":      {"{", false},
		"version 2":     {`{"version":2}`, false},
		"bad address":   {`{"version":1,"ip":{"addr":"x"}}`, false},
		"ipv6 mask":     {`{"version":1,"ip":{"addr":"10.0.0.1","mask":"::1"}}`, false},
		"bad gateway":   {`{"version":1,"ip":{"addr":"10.0.0.1","mask":"255.0.0.0","gateway":"y"}}`, false},
		"short I&M4":    {`{"version":1,"im":{"im4":"AA=="}}`, false},
		"invalid name":  {`{"version":1,"name":"-bad-"}`, false},
		"dhcp and name": {`{"version":1,"name":"ok-name","dhcp":[{"code":61,"data":"AA=="}]}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			st := store.NewMemory()
			if err := st.Save(context.Background(), DefaultStoreKey, []byte(tc.value)); err != nil {
				t.Fatal(err)
			}
			d, _ := newNet(t).device(nil, withStoreCfg(st, ""))
			if tc.ok != (d.StoreError() == nil) {
				t.Fatalf("StoreError %v", d.StoreError())
			}
			if !tc.ok && d.StationName() != "dut" {
				t.Fatalf("name %q", d.StationName())
			}
			if tc.ok && (d.StationName() != "ok-name" || len(d.DHCP()) != 1) {
				t.Fatalf("%q %v", d.StationName(), d.DHCP())
			}
		})
	}
	// A key that cannot name a value.
	nd := newNet(t).node("192.168.0.9")
	if _, err := New(Config{Model: testModel(), Mux: nd.mux, Store: store.NewMemory(), StoreKey: "Bad Key"}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("bad key: %v", err)
	}
}
