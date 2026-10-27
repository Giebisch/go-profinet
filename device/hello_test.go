// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"encoding/binary"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/vnet"
)

// countHellos counts the DCP Hellos sent on the network.
func countHellos(n *tnet) *atomic.Int64 {
	var c atomic.Int64
	n.net.AddTap(func(_ *vnet.Endpoint, f []byte) {
		if len(f) >= 16 && binary.BigEndian.Uint16(f[12:]) == uint16(ethernet.EtherTypeProfinet) && binary.BigEndian.Uint16(f[14:]) == dcp.FrameIDHello {
			c.Add(1)
		}
	})
	return &c
}

func TestHelloConfig(t *testing.T) {
	n := newNet(t)
	nd := n.node(devIP)
	for _, cfg := range []Config{
		{Model: testModel(), Mux: nd.mux, Hello: 3},
		{Model: testModel(), Mux: nd.mux, Hello: dcp.HelloOnLinkUp, HelloOptions: dcp.HelloOptions{Count: 99}},
	} {
		if _, err := New(cfg); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%+v: %v", cfg.HelloOptions, err)
		}
	}
}

// TestHelloDevice: Hellos at start and on link up while no IO controller
// is connected; DeviceInitiative reports Hello; none once an IOCAR is up.
func TestHelloDevice(t *testing.T) {
	n := newNet(t)
	hellos := countHellos(n)
	d, nd := n.device(nil, func(c *Config) {
		c.Hello = dcp.HelloOnLinkUp
		c.HelloOptions = dcp.HelloOptions{Count: 2, Interval: 20 * time.Millisecond}
	})
	eventually(t, 5*time.Second, "2 Hellos at start", func() bool { return hellos.Load() == 2 })
	// DCP reports the initiative.
	cn := n.node("192.168.0.2")
	cli, err := dcp.NewClient(cn.mux, dcp.ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := cli.IdentifyName(ctx, "dut", 200*time.Millisecond)
	if err != nil || !id.HasInitiative || id.Initiative != dcp.InitiativeHello {
		t.Fatalf("%+v %v", id, err)
	}
	// Link up again: another sequence.
	nd.ep.SetLinkUp(false)
	time.Sleep(3 * helloLinkPoll)
	nd.ep.SetLinkUp(true)
	eventually(t, 5*time.Second, "Hellos on link up", func() bool { return hellos.Load() == 4 })
	// With an IOCAR, a link flap brings no Hello.
	p := n.peer(d, "192.168.0.3", true)
	p.establish(p.ioCAR())
	nd.ep.SetLinkUp(false)
	time.Sleep(3 * helloLinkPoll)
	nd.ep.SetLinkUp(true)
	time.Sleep(4 * helloLinkPoll)
	if hellos.Load() != 4 {
		t.Fatalf("%d Hellos with an IO controller connected", hellos.Load())
	}
}

// TestHelloStopsOnConnect: a Connect ends a running sequence.
func TestHelloStopsOnConnect(t *testing.T) {
	n := newNet(t)
	hellos := countHellos(n)
	d, _ := n.device(nil, func(c *Config) {
		c.Hello = dcp.HelloOnLinkUpDelayed
		c.HelloOptions = dcp.HelloOptions{Count: dcp.MaxHelloCount, Interval: 30 * time.Millisecond, Delay: 10 * time.Millisecond}
	})
	eventually(t, 5*time.Second, "first Hello", func() bool { return hellos.Load() >= 1 })
	p := n.peer(d, "192.168.0.3", true)
	if _, err := p.connect(p.ioCAR()); err != nil {
		t.Fatal(err)
	}
	sent := hellos.Load()
	time.Sleep(200 * time.Millisecond)
	if got := hellos.Load(); got > sent+1 || got >= dcp.MaxHelloCount {
		t.Fatalf("%d Hellos after the Connect (%d before)", got, sent)
	}
}

// TestNoHelloByDefault: devices stay silent unless configured.
func TestNoHelloByDefault(t *testing.T) {
	n := newNet(t)
	hellos := countHellos(n)
	n.device(nil)
	time.Sleep(200 * time.Millisecond)
	if hellos.Load() != 0 {
		t.Fatal("Hello from a device without Hello")
	}
}
