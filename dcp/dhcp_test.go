// SPDX-License-Identifier: MIT

package dcp

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/vnet"
)

func unhexDCP(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Golden DHCP blocks from the layout of Wireshark's DCP dissector: option
// 3, the DHCP option code as suboption, DCPBlockLength, BlockInfo (in
// responses) or BlockQualifier (in Set requests), then the DHCP option
// code, the parameter length and the data, padded to an even length.
const (
	// Identify response: DHCP/ClientID "use the MAC address",
	// DHCP/Control "use DHCP", IP/Parameter with BlockInfo 0x0002 "IP set
	// by DHCP".
	goldenDHCPIdentify = "feff 05 01 00000007 0000 002e" +
		"0102 000e 0002 c0a8000a ffffff00 c0a80001" +
		"033d 0005 0000 3d0101 00" +
		"03ff 0005 0000 ff0102 00" +
		"0202 0004 0000 6470"
	// Set request: DHCP/Control "do not use DHCP", permanent.
	goldenDHCPSet = "fefd 04 00 00000009 0000 0012" +
		"03ff 0005 0001 ff0100 00" +
		"033d 0004 0000 3d00 "
)

func TestDHCPGolden(t *testing.T) {
	f := Frame{FrameID: FrameIDIdentifyResponse, Service: ServiceIdentify, Type: TypeResponseSuccess, Xid: 7, Blocks: []Block{
		{ID: IPParameter, HasInfo: true, Info: IPInfoDHCP, Data: unhexDCP(t, "c0a8000a ffffff00 c0a80001")},
		{ID: DHCPClientID, HasInfo: true, Data: mustDHCP(t, DHCPClientIDFromMAC())},
		{ID: DHCPControl, HasInfo: true, Data: mustDHCP(t, DHCPControlParameter(DHCPControlOn))},
		{ID: DeviceName, HasInfo: true, Data: []byte("dp")},
	}}
	got, err := f.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := unhexDCP(t, goldenDHCPIdentify); !bytes.Equal(got, want) {
		t.Fatalf("Identify response\n got %x\nwant %x", got, want)
	}
	d, err := Decode(got)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ParseIdentity(devMAC, d.Blocks)
	if err != nil {
		t.Fatal(err)
	}
	if !id.IPSetByDHCP() || id.IPConflict() || len(id.DHCP) != 2 || id.NameOfStation != "dp" {
		t.Fatalf("%+v", id)
	}
	if p, ok := id.DHCPOption(DHCPOptClientID); !ok || p.String() != "DHCP client ID MAC address" {
		t.Fatalf("client ID %v %v", p, ok)
	}
	if p, ok := id.DHCPOption(DHCPOptControl); !ok || p.String() != "DHCP control 2" {
		t.Fatalf("control %v %v", p, ok)
	}
	if _, ok := id.DHCPOption(DHCPOptHostName); ok {
		t.Fatal("host name")
	}
	// The parsed parameters do not alias the frame.
	got[len(got)-14] = 0xEE
	if p, _ := id.DHCPOption(DHCPOptControl); p.Data[0] != DHCPControlOn {
		t.Fatal("parameter aliases the frame")
	}

	set := Frame{FrameID: FrameIDGetSet, Service: ServiceSet, Type: TypeRequest, Xid: 9, Blocks: []Block{
		{ID: DHCPControl, HasInfo: true, Info: QualifierPermanent, Data: mustDHCP(t, DHCPControlParameter(DHCPControlOff))},
		{ID: DHCPClientID, HasInfo: true, Data: []byte{DHCPOptClientID, 0}},
	}}
	if got, err := set.AppendTo(nil); err != nil || !bytes.Equal(got, unhexDCP(t, goldenDHCPSet)) {
		t.Fatalf("Set request\n got %x %v\nwant %s", got, err, goldenDHCPSet)
	}
}

func mustDHCP(t *testing.T, p DHCPParameter) []byte {
	t.Helper()
	b, err := AppendDHCPParameter(nil, p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDHCPParameter(t *testing.T) {
	arb, err := DHCPClientIDArbitrary(0, []byte("plc"))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(mustDHCP(t, arb)); got != "3d0400706c63" {
		t.Fatalf("arbitrary %s", got)
	}
	cases := []struct {
		p    DHCPParameter
		kind DHCPClientIDKind
		id   string
		str  string
	}{
		{DHCPClientIDFromMAC(), ClientIDMAC, "", "DHCP client ID MAC address"},
		{DHCPParameter{Code: DHCPOptClientID, Data: []byte{0}}, ClientIDNameOfStation, "", "DHCP client ID NameOfStation"},
		{arb, ClientIDArbitrary, "plc", "DHCP client ID arbitrary 706c63"},
	}
	for _, c := range cases {
		k, id, ok := c.p.ClientID()
		if !ok || k != c.kind || string(id) != c.id || c.p.String() != c.str {
			t.Errorf("%v: %s %q %v %q", c.p.Data, k, id, ok, c.p.String())
		}
		if c.p.BlockID() != DHCPClientID {
			t.Errorf("block %s", c.p.BlockID())
		}
	}
	for _, p := range []DHCPParameter{{Code: DHCPOptClientID}, DHCPControlParameter(1)} {
		if _, _, ok := p.ClientID(); ok {
			t.Errorf("%v is no client ID", p)
		}
	}
	if s := (DHCPParameter{Code: DHCPOptHostName, Data: []byte("ab")}).String(); s != "DHCP option 12 6162" {
		t.Errorf("host name %q", s)
	}
	if s := (DHCPParameter{Code: DHCPOptClientID}).String(); s != "DHCP option 61 " {
		t.Errorf("empty client ID %q", s)
	}
	if s := DHCPClientIDKind(9).String(); s != "ClientIDKind(9)" {
		t.Errorf("kind %q", s)
	}
	for b, want := range map[BlockID]string{DHCPClientID: "DHCP/ClientID", DHCPControl: "DHCP/Control", NewBlockID(OptionDHCP, DHCPOptFQDN): "DHCP/81"} {
		if b.String() != want {
			t.Errorf("%s, want %s", b, want)
		}
	}
	// Errors.
	if _, err := DHCPClientIDArbitrary(0, nil); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Error(err)
	}
	if _, err := DHCPClientIDArbitrary(0, make([]byte, 255)); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Error(err)
	}
	if _, err := AppendDHCPParameter(nil, DHCPParameter{Code: 12, Data: make([]byte, 256)}); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Error(err)
	}
	for _, b := range []string{"", "3d", "3d02 01", "3d00 01"} {
		if _, err := DecodeDHCPParameter(unhexDCP(t, b)); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%q: %v", b, err)
		}
	}
}

func TestIdentityDHCP(t *testing.T) {
	id := Identity{NameOfStation: "a", IPInfo: IPInfoDHCP | IPInfoConflict, DHCP: []DHCPParameter{DHCPClientIDFromMAC(), {Code: 12, Data: make([]byte, 256)}}}
	bs := id.Blocks()
	// The oversized parameter is left out; the client ID is reported
	// after the IP block and listed in Device/Options.
	var ids []BlockID
	for _, b := range bs {
		ids = append(ids, b.ID)
	}
	if i, j := slices.Index(ids, IPParameter), slices.Index(ids, DHCPClientID); i < 0 || j != i+1 || slices.Contains(ids, NewBlockID(OptionDHCP, 12)) {
		t.Fatalf("blocks %v", ids)
	}
	opts, _ := id.Block(DeviceOpts)
	if !bytes.Contains(opts.Data, []byte{0x03, 0x3d}) {
		t.Fatalf("options % x", opts.Data)
	}
	got, err := ParseIdentity(devMAC, append(bs,
		Block{ID: DHCPHostName, Data: []byte{DHCPOptClientID, 0}}, // code differs from the suboption
		Block{ID: DHCPControl, Data: []byte{0xFF}},                // malformed
	))
	if err != nil || len(got.DHCP) != 1 || !got.IPSetByDHCP() || !got.IPConflict() {
		t.Fatalf("%+v %v", got, err)
	}
}

// dhcpStation accepts DHCP options.
type dhcpStation struct {
	*fakeStation
	mu   sync.Mutex
	set  []DHCPParameter
	perm bool
}

func (s *dhcpStation) SetDHCP(p DHCPParameter, permanent bool) BlockError {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Code == DHCPOptHostName {
		return BlockErrorSetNotPossible
	}
	s.set = append(s.set, p)
	s.perm = permanent
	return BlockErrorNone
}

func TestResponderDHCP(t *testing.T) {
	ctx := context.Background()
	// A station without DHCP options and without a DHCPSetter: option 3
	// is unsupported for Get and Set.
	_, c, _ := newResponderClient(t, testStation())
	if _, err := c.Get(ctx, devMAC, DHCPClientID); !isBlockError(err, BlockErrorOptionUnsupported) {
		t.Fatalf("get %v", err)
	}
	if err := c.SetDHCP(ctx, devMAC, DHCPControlParameter(DHCPControlOn), true); !isBlockError(err, BlockErrorOptionUnsupported) || !errors.Is(err, profinet.ErrUnsupported) {
		t.Fatalf("set %v", err)
	}

	// A station that reports DHCP options answers Get of them; a code it
	// does not report is an unsupported suboption. Without a DHCPSetter a
	// Set is refused.
	st := testStation()
	st.id.DHCP = []DHCPParameter{DHCPClientIDFromMAC()}
	st.id.IPInfo = IPInfoDHCP
	_, c, _ = newResponderClient(t, st)
	bs, err := c.Get(ctx, devMAC, DHCPClientID)
	if err != nil || len(bs) != 1 || bs[0].ID != DHCPClientID || !bytes.Equal(bs[0].Data, []byte{0x3d, 1, 1}) {
		t.Fatalf("get %+v %v", bs, err)
	}
	if _, err := c.Get(ctx, devMAC, DHCPControl); !isBlockError(err, BlockErrorSuboptionUnsupport) {
		t.Fatalf("get control %v", err)
	}
	if id, err := c.GetIdentity(ctx, devMAC); err != nil || !id.IPSetByDHCP() {
		t.Fatalf("identity %+v %v", id, err)
	}
	if err := c.SetDHCP(ctx, devMAC, DHCPControlParameter(DHCPControlOn), true); !isBlockError(err, BlockErrorOptionUnsupported) {
		t.Fatalf("set %v", err)
	}

	// With a DHCPSetter the Set reaches the station.
	ds := &dhcpStation{fakeStation: testStation()}
	_, c, _ = newResponderClient(t, ds)
	if err := c.SetDHCP(ctx, devMAC, DHCPControlParameter(DHCPControlOn), true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDHCP(ctx, devMAC, DHCPParameter{Code: DHCPOptHostName, Data: []byte("x")}, false); !isBlockError(err, BlockErrorSetNotPossible) {
		t.Fatalf("host name %v", err)
	}
	if err := c.SetDHCP(ctx, devMAC, DHCPParameter{Code: 1, Data: make([]byte, 300)}, false); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("oversized %v", err)
	}
	// A value that is not a DHCP option of the block's code is refused
	// before the station sees it.
	for _, data := range [][]byte{{0xFF}, {DHCPOptClientID, 1, 1}} {
		err := c.Set(ctx, devMAC, SetBlock{ID: DHCPControl, Data: data})
		if !isBlockError(err, BlockErrorSetNotPossible) {
			t.Fatalf("%x: %v", data, err)
		}
	}
	ds.mu.Lock()
	if len(ds.set) != 1 || ds.set[0].Code != DHCPOptControl || ds.set[0].Data[0] != DHCPControlOn || !ds.perm {
		t.Fatalf("station %+v", ds.set)
	}
	ds.mu.Unlock()
}

func isBlockError(err error, code BlockError) bool {
	var se *SetError
	return errors.As(err, &se) && se.Code == code
}

// newResponderClient starts a responder for st at devMAC (answering
// Identify at once) and a client and a raw endpoint on the same network.
func newResponderClient(t *testing.T, st Station, opts ...ResponderOptions) (*Responder, *Client, *rawDev) {
	t.Helper()
	n, c, raw := newClientEnv(t, ClientOptions{})
	ep, err := n.NewEndpoint(vnet.EndpointConfig{MAC: devMAC})
	if err != nil {
		t.Fatal(err)
	}
	m := l2.NewMux(ep)
	t.Cleanup(func() { _ = m.Close() })
	var o ResponderOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	o.NoResponseDelay = true
	r, err := NewResponder(m, st, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, c, raw
}

func FuzzDecodeDHCPParameter(f *testing.F) {
	f.Add([]byte{0x3d, 1, 1})
	f.Add([]byte{0xff, 1, 2})
	f.Add([]byte{0x3d, 4, 0, 'p', 'l', 'c'})
	f.Add([]byte{0x0c})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := DecodeDHCPParameter(b)
		if err != nil {
			return
		}
		out, err := AppendDHCPParameter(nil, p)
		if err != nil || !bytes.Equal(out, b) {
			t.Fatalf("re-encoded %x (%v) from %x", out, err, b)
		}
		_, _, _ = p.ClientID()
		_ = p.String()
	})
}
