// SPDX-License-Identifier: MIT

package mrp

import (
	"testing"
	"time"

	"github.com/otfabric/go-profinet/ethernet"
)

func TestManagerOption(t *testing.T) {
	m := ManagerOption{Type: SubTestMgrNAck, Prio: 0xA000, SA: ethernet.MAC{2, 0, 0, 0, 0, 1}, OtherPrio: 0, OtherSA: ethernet.MAC{2, 0, 0, 0, 0, 2}}
	f := Frame{Options: []Option{NewManagerOption(m)}, Common: &Common{SequenceID: 7, Domain: DefaultDomain}}
	b, err := f.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Version, MRP_Option of 24 octets and its padding, MRP_Common.
	if b[2] != byte(TypeOption) || b[3] != 24 || b[2+2+24] != 0 || b[2+2+24+2] != byte(TypeCommon) {
		t.Fatalf("% x", b)
	}
	g, err := Decode(b)
	if err != nil || len(g.Options) != 1 || g.Common == nil {
		t.Fatalf("%+v %v", g, err)
	}
	got, ok := g.Options[0].ManagerOption()
	if !ok || got != m {
		t.Fatalf("%+v %v", got, ok)
	}
	if sa, ok := g.sa(); !ok || sa != m.SA {
		t.Fatalf("sa %v %v", sa, ok)
	}
	// Wireshark's layout after the Siemens OUI: an Ed1Type without
	// manufacturer data.
	if o := NewManagerOption(m); o.OUI != OUISiemens || o.Data[0] != 0 {
		t.Fatalf("% x % x", o.OUI, o.Data)
	}
	zero := Option{Data: NewManagerOption(m).Data}
	if got, ok := zero.ManagerOption(); !ok || got != m {
		t.Fatalf("OUI 00-00-00 %+v %v", got, ok)
	}
	siemens := Option{OUI: OUISiemens, Data: append([]byte{0x01}, NewManagerOption(m).Data[3:]...)}
	if got, ok := siemens.ManagerOption(); !ok || got != m {
		t.Fatalf("siemens %+v %v", got, ok)
	}
	for _, o := range []Option{{}, {Data: []byte{0}}, {Data: []byte{0, 0, 0, byte(SubAutoMgr), 0x12, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}}} {
		if _, ok := o.ManagerOption(); ok {
			t.Errorf("%+v decoded", o)
		}
	}
}

func TestBetter(t *testing.T) {
	a, b := ethernet.MAC{2, 0, 0, 0, 0, 1}, ethernet.MAC{2, 0, 0, 0, 0, 2}
	for _, c := range []struct {
		p    uint16
		a    ethernet.MAC
		q    uint16
		b    ethernet.MAC
		want bool
	}{
		{0x8000, b, 0xA000, a, true},
		{0xA000, a, 0x8000, b, false},
		{0xA000, a, 0xA000, b, true},
		{0xA000, b, 0xA000, a, false},
		{0xA000, a, 0xA000, a, false},
	} {
		if got := Better(c.p, c.a, c.q, c.b); got != c.want {
			t.Errorf("Better(%04x %s, %04x %s) = %v", c.p, c.a, c.q, c.b, got)
		}
	}
	if RoleAuto.String() != "auto" {
		t.Error(RoleAuto)
	}
}

// The automatic managers run the 500 ms profile here: a client takes over
// after TSTNRmax test intervals without a better manager's test, which in
// the 200 ms profile is 80 ms, less than a loaded test runner may delay a
// goroutine.

func autos(n int) []Role {
	r := make([]Role, n)
	for i := range r {
		r[i] = RoleAuto
	}
	return r
}

// settled waits until station m manages the ring, closed, and every other
// running station is its client and forwards.
func settled(t *testing.T, nodes []*node, m int, others []int) {
	t.Helper()
	eventually(t, 5*time.Second, "one manager", func() bool {
		s := nodes[m].ent.Status()
		if s.Acting != RoleManager || s.RingOpen || !s.Blocked[1] {
			return false
		}
		for _, i := range others {
			c := nodes[i].ent.Status()
			if c.Acting != RoleClient || c.Manager != nodes[m].cpuMAC || c.Blocked[0] || c.Blocked[1] {
				return false
			}
		}
		return true
	})
}

func noLoop(t *testing.T, nodes []*node, from int, to []int) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	before := make([]int64, len(nodes))
	for i, nd := range nodes {
		before[i] = nd.got.Load()
	}
	broadcast(t, nodes[from])
	time.Sleep(300 * time.Millisecond)
	for _, i := range to {
		if d := nodes[i].got.Load() - before[i]; d != 1 {
			t.Fatalf("station %d got the broadcast %d times: a loop or a hole", i, d)
		}
	}
}

// TestAutoRing: three automatic managers settle on the best (the lowest
// address at equal priority) as the manager; the ring closes without a
// loop or a hole.
func TestAutoRing(t *testing.T) {
	nodes, _ := ringOf(t, autos(3), nil, Profile500)
	settled(t, nodes, 0, []int{1, 2})
	if s := nodes[1].ent.Status(); s.Role != RoleAuto || s.ManagerPrio != DefaultAutoPrio {
		t.Fatalf("%+v", s)
	}
	noLoop(t, nodes, 1, []int{0, 2})
}

// TestAutoPriority: the lower priority wins over the lower address, and a
// configured client takes part.
func TestAutoPriority(t *testing.T) {
	nodes, _ := ringOf(t, []Role{RoleAuto, RoleClient, RoleAuto}, []uint16{0, 0, 0x9000}, Profile500)
	settled(t, nodes, 2, []int{0})
	// The configured client forwards once the manager reports the ring
	// closed.
	eventually(t, 5*time.Second, "the client forwarding", func() bool {
		s := nodes[1].ent.Status()
		return s.Acting == RoleClient && !s.Blocked[0] && !s.Blocked[1]
	})
	noLoop(t, nodes, 1, []int{0, 2})
}

// TestAutoTakeOver: when the manager fails, the next best automatic
// manager stops seeing its tests and manages the ring.
func TestAutoTakeOver(t *testing.T) {
	nodes, _ := ringOf(t, autos(4), nil, Profile500)
	settled(t, nodes, 0, []int{1, 2, 3})
	// Station 0 fails: both its cables go down.
	_ = nodes[0].ent.Close()
	cut(nodes, 0, false)
	cut(nodes, 3, false)
	defer func() {
		if t.Failed() {
			for i, nd := range nodes[1:] {
				t.Logf("station %d: %+v", i+1, nd.ent.Status())
			}
		}
	}()
	eventually(t, 5*time.Second, "station 1 manages", func() bool {
		s := nodes[1].ent.Status()
		c2, c3 := nodes[2].ent.Status(), nodes[3].ent.Status()
		return s.Acting == RoleManager && c2.Acting == RoleClient && c2.Manager == nodes[1].cpuMAC &&
			c3.Acting == RoleClient && c3.Manager == nodes[1].cpuMAC
	})
	noLoop(t, nodes, 2, []int{1, 3})
	// Station 0 comes back as a fresh automatic manager: it is the best,
	// and the others give way to it.
	cut(nodes, 0, true)
	cut(nodes, 3, true)
	ent, err := New(Config{Role: RoleAuto, Ports: nodes[0].ent.cfg.Ports, Index: nodes[0].ent.cfg.Index, Switch: nodes[0].br, MAC: nodes[0].cpuMAC, Profile: Profile500})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ent.Close() })
	nodes[0].ent = ent
	settled(t, nodes, 0, []int{1, 2, 3})
	noLoop(t, nodes, 2, []int{0, 1, 3})
}

// TestDecodeKernelMRATest: the Linux bridge's MRA test frame, whose last
// MRP_Option claims more octets than the frame has, decodes.
func TestDecodeKernelMRATest(t *testing.T) {
	f := Frame{Test: &Test{Prio: 0xA000, SA: ethernet.MAC{2, 0, 0, 0, 0, 9}, RingState: RingClosed}, Common: &Common{SequenceID: 3, Domain: DefaultDomain}}
	b, err := f.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	b = b[:len(b)-2] // MRP_End
	// OUI, three octets of sub-option, the AutoMgr sub-TLV with an
	// uninitialised length, two octets of padding: 10 written, 15 claimed.
	b = append(b, byte(TypeOption), 15, 0, 0, 0, 0, 0, 0, byte(SubAutoMgr), 0xAA, 0, 0, byte(TypeEnd), 0)
	g, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if g.Test == nil || g.Test.SA != f.Test.SA || g.Common == nil || len(g.Options) != 1 {
		t.Fatalf("%+v", g)
	}
	if _, err := Decode(append(b[:len(b)-14], byte(TypeOption), 15, 0, 0)); err == nil {
		t.Error("an option without its OUI decoded")
	}
}
