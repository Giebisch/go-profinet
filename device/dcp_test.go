// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/dcp"
)

// dcpClient returns a DCP client on the test network.
func (n *tnet) dcpClient() *dcp.Client {
	n.t.Helper()
	cn := n.node("192.168.0.2")
	cli, err := dcp.NewClient(cn.mux, dcp.ClientOptions{Timeout: 500 * time.Millisecond})
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestDHCPConfig(t *testing.T) {
	n := newNet(t)
	nd := n.node(devIP)
	for _, opts := range [][]dcp.DHCPParameter{
		{{Code: dcp.DHCPOptHostName, Data: make([]byte, 256)}},
		{dcp.DHCPClientIDFromMAC(), dcp.DHCPClientIDFromMAC()},
	} {
		if _, err := New(Config{Model: testModel(), Mux: nd.mux, DHCP: opts}); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%v: %v", opts, err)
		}
	}
}

// TestDHCPOptions: the device reports its configured DHCP options and an
// IP set by DHCP in Identify and Get; a Set of a DHCP option is refused
// without Config.SetDHCP.
func TestDHCPOptions(t *testing.T) {
	n := newNet(t)
	d, nd := n.device(nil, func(c *Config) {
		c.DHCP = []dcp.DHCPParameter{dcp.DHCPClientIDFromMAC(), dcp.DHCPControlParameter(dcp.DHCPControlOn)}
		c.IPFromDHCP = true
	})
	cli := n.dcpClient()
	ctx := context.Background()
	id, err := cli.IdentifyName(ctx, "dut", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !id.IPSetByDHCP() || len(id.DHCP) != 2 {
		t.Fatalf("identity %+v", id)
	}
	if p, ok := id.DHCPOption(dcp.DHCPOptControl); !ok || p.Data[0] != dcp.DHCPControlOn {
		t.Fatalf("control %v", p)
	}
	mac := nd.mux.HardwareAddr()
	bs, err := cli.Get(ctx, mac, dcp.DHCPClientID)
	if err != nil || len(bs) != 1 {
		t.Fatalf("get %+v %v", bs, err)
	}
	var se *dcp.SetError
	if err := cli.SetDHCP(ctx, mac, dcp.DHCPControlParameter(dcp.DHCPControlOff), true); !errors.As(err, &se) || se.Code != dcp.BlockErrorOptionUnsupported {
		t.Fatalf("set %v", err)
	}
	// A DCP Set of the IP suite: the address is no longer from DHCP.
	if err := cli.SetIP(ctx, mac, suite("192.168.0.20"), false); err != nil {
		t.Fatal(err)
	}
	if id, err := cli.GetIdentity(ctx, mac); err != nil || id.IPSetByDHCP() || id.IPInfo != dcp.IPInfoSet {
		t.Fatalf("identity after Set IP %+v %v", id, err)
	}
	if got := d.DHCP(); len(got) != 2 {
		t.Fatalf("DHCP %v", got)
	}
}

// TestSetDHCP: with Config.SetDHCP a Set reaches the application and the
// device reports the new value; ResetToFactory restores the configured
// options.
func TestSetDHCP(t *testing.T) {
	n := newNet(t)
	var mu sync.Mutex
	var got []dcp.DHCPParameter
	d, nd := n.device(nil, func(c *Config) {
		c.DHCP = []dcp.DHCPParameter{dcp.DHCPControlParameter(dcp.DHCPControlOff)}
		c.SetDHCP = func(p dcp.DHCPParameter, _ bool) dcp.BlockError {
			if p.Code == dcp.DHCPOptHostName {
				return dcp.BlockErrorSetNotPossible
			}
			mu.Lock()
			got = append(got, p)
			mu.Unlock()
			return dcp.BlockErrorNone
		}
	})
	cli := n.dcpClient()
	ctx := context.Background()
	mac := nd.mux.HardwareAddr()
	if err := cli.SetDHCP(ctx, mac, dcp.DHCPControlParameter(dcp.DHCPControlOn), true); err != nil {
		t.Fatal(err)
	}
	if err := cli.SetDHCP(ctx, mac, dcp.DHCPClientIDFromMAC(), true); err != nil {
		t.Fatal(err)
	}
	var se *dcp.SetError
	if err := cli.SetDHCP(ctx, mac, dcp.DHCPParameter{Code: dcp.DHCPOptHostName, Data: []byte("h")}, true); !errors.As(err, &se) || se.Code != dcp.BlockErrorSetNotPossible {
		t.Fatalf("host name %v", err)
	}
	mu.Lock()
	if len(got) != 2 {
		t.Fatalf("handler saw %v", got)
	}
	mu.Unlock()
	opts := d.DHCP()
	if len(opts) != 2 || opts[0].Data[0] != dcp.DHCPControlOn || opts[1].Code != dcp.DHCPOptClientID {
		t.Fatalf("options %v", opts)
	}
	if err := cli.ResetToFactory(ctx, mac, dcp.ResetCommunication); err != nil {
		t.Fatal(err)
	}
	if opts := d.DHCP(); len(opts) != 1 || opts[0].Data[0] != dcp.DHCPControlOff {
		t.Fatalf("after reset %v", opts)
	}
}

// TestDCPPolicy: a read-only device refuses DCP Set with "SET not
// possible by local reasons" and audits it; an invalid policy is refused.
func TestDCPPolicy(t *testing.T) {
	n := newNet(t)
	if _, err := New(Config{Model: testModel(), Mux: n.node("").mux, DCPPolicy: dcp.Policy{Sources: -1}}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("invalid policy: %v", err)
	}
	var mu sync.Mutex
	var events []dcp.AuditEvent
	d, nd := n.device(nil, func(c *Config) {
		c.DCPPolicy = dcp.Policy{ReadOnly: true, Audit: func(e dcp.AuditEvent) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}}
	})
	cli := n.dcpClient()
	var se *dcp.SetError
	if err := cli.SetName(context.Background(), nd.mux.HardwareAddr(), "other", true); !errors.As(err, &se) || se.Code != dcp.BlockErrorSetNotPossible {
		t.Fatalf("set name %v", err)
	}
	if d.StationName() != "dut" {
		t.Fatalf("renamed to %q", d.StationName())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Outcome != dcp.AuditReadOnly || events[0].Block != dcp.DeviceName {
		t.Fatalf("events %+v", events)
	}
}
