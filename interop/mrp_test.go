// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The MRP tests build rings of three nodes: go-profinet (otfnode mrp: a
// multiport device on the l2/bridge with its MRP entity) and two nodes of
// an independent MRP implementation, joined by veth cables. Two peers of
// profinet-interop run in turn:
//
//   - umrp (giometti/umrp): the MRP state machine in user space on raw
//     sockets, with the adapter's ifdriver for generic bridge port states;
//     runs on any Linux kernel.
//   - mrp (microchip-ung/mrp, unmodified): Microchip's daemon for the Linux
//     bridge's MRP support; needs a host kernel with CONFIG_BRIDGE_MRP that
//     accepts an MRP instance on a software bridge (without
//     CONFIG_NET_SWITCHDEV). Docker Desktop has no bridge MRP and the
//     GitHub-hosted runners have switchdev built in: n/a on both. Each node is a
// container without a Docker network; a privileged helper creates a veth
// pair for every cable and moves its ends into the two nodes' network
// namespaces. Taking one end of a cable down is a cable break: both ends
// lose their link.
//
// Ring (cable names after their ends):
//
//	n1.ringB — n2.ringA,  n2.ringB — n3.ringA,  n3.ringB — n1.ringA
//
// A node's ringA is its primary ring port, ringB its secondary. Probes
// (otfnode's broadcast frames of EtherType 0x88B5) find loops and holes:
// each peer node counts the probes its bridge delivers to it, and a ring
// without a loop or hole delivers each probe exactly once.

// mrpPeers are the profinet-interop adapters of the MRP tests.
var mrpPeers = []string{"umrp", "mrp"}

// peerSupported reports whether a peer runs on this Docker host: the
// kernel adapter needs bridge MRP support.
func peerSupported(peer string) (bool, string) {
	if peer != "mrp" {
		return true, ""
	}
	_, err := docker(context.Background(), "run", "--rm", "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", image(peer), "kernel-mrp")
	if err != nil {
		return false, "the Docker host's kernel does not accept an MRP instance on a software bridge (no CONFIG_BRIDGE_MRP, or CONFIG_NET_SWITCHDEV built in); microchip-ung/mrp needs it: " + err.Error()
	}
	return true, ""
}

// eachPeer runs a ring test against every MRP peer.
func eachPeer(t *testing.T, test func(t *testing.T, peer string)) {
	for _, peer := range mrpPeers {
		t.Run(peer, func(t *testing.T) {
			setup(t)
			if ok, why := peerSupported(peer); !ok {
				r := newResult(t)
				r.peer(peer, image(peer), "MRP manager and client (go-profinet: MRP manager and client)")
				r.na("MRP ring with "+peer, why)
				r.write()
				t.Skip(why)
			}
			test(t, peer)
		})
	}
}

// ringNode starts a ring node: a container without a network.
func (e *env) ringNode(name, img string, args ...string) string {
	e.t.Helper()
	full := e.network + "-" + name
	a := []string{"run", "-d", "--name", full, "--network", "none", "--cap-add", "NET_RAW", "--cap-add", "NET_ADMIN",
		"-v", otfnode + ":/otfnode:ro", img}
	if _, err := docker(context.Background(), append(a, args...)...); err != nil {
		e.t.Fatal(err)
	}
	e.mu.Lock()
	e.containers = append(e.containers, full)
	e.ringNodes = append(e.ringNodes, full)
	e.mu.Unlock()
	return full
}

func (e *env) pid(container string) string {
	e.t.Helper()
	out, err := docker(context.Background(), "inspect", "-f", "{{.State.Pid}}", container)
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// host runs a shell command in the Docker host's namespaces (privileged).
func (e *env) host(script string) error {
	_, err := docker(context.Background(), "run", "--rm", "--privileged", "--pid", "host", "--network", "host", runnerImage, "sh", "-c", script)
	return err
}

var cableSeq struct {
	n int
}

// cable joins a's ringB and b's ringA with a veth pair.
func (e *env) cable(a, b string) {
	e.t.Helper()
	e.mu.Lock()
	cableSeq.n++
	x, y := fmt.Sprintf("mrpc%dx", cableSeq.n), fmt.Sprintf("mrpc%dy", cableSeq.n)
	e.mu.Unlock()
	pa, pb := e.pid(a), e.pid(b)
	script := fmt.Sprintf("ip link add %[1]s type veth peer name %[2]s && ip link set %[1]s netns %[3]s && ip link set %[2]s netns %[4]s && "+
		"nsenter -t %[3]s -n ip link set %[1]s name ringB && nsenter -t %[4]s -n ip link set %[2]s name ringA", x, y, pa, pb)
	if err := e.host(script); err != nil {
		e.t.Fatal(err)
	}
}

// setLink sets a ring port of a node up or down.
func (e *env) setLink(container, port string, up bool) {
	e.t.Helper()
	state := "down"
	if up {
		state = "up"
	}
	if err := e.host(fmt.Sprintf("nsenter -t %s -n ip link set %s %s", e.pid(container), port, state)); err != nil {
		e.t.Fatal(err)
	}
}

// peerStatus is the status document of a peer node (Flushes -1: not
// observable).
type peerStatus struct {
	Ring string `json:"ring"`
	// Role is what the node acts as, MRM or MRC (contract 1.2).
	Role    string `json:"role"`
	Flushes int    `json:"flushes"`
	Probes  int    `json:"probes"`
	Ports   []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Oper  string `json:"oper"`
	} `json:"ports"`
}

func (e *env) peerStatus(container string) peerStatus {
	e.t.Helper()
	var s peerStatus
	out := e.exec(container, "profinet-interop", "status")
	if err := json.Unmarshal(lastLine(out), &s); err != nil {
		e.t.Fatalf("status of %s: %v: %s", container, err, out)
	}
	return s
}

// otfRing is otfnode's last ring or stats event.
type otfRing struct {
	State string `json:"state"`
	// Acting is what otfnode acts as (manager or client), Manager the
	// manager an automatic manager follows.
	Acting          string `json:"acting"`
	Manager         string `json:"manager"`
	Open            bool   `json:"open"`
	Blocked         []bool `json:"blocked"`
	TopologyChanges int    `json:"topologyChanges"`
	LinkChanges     int    `json:"linkChanges"`
	TestsReceived   int    `json:"testsReceived"`
	Malformed       int    `json:"malformed"`
}

func (e *env) otfRing(container string) otfRing {
	var r otfRing
	evs := e.events(container)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i]["event"] == "ring" || evs[i]["event"] == "stats" {
			b, _ := json.Marshal(evs[i])
			_ = json.Unmarshal(b, &r)
			break
		}
	}
	return r
}

// ring is a ring of otfnode (n1) and two peer nodes (n2, n3).
type ring struct {
	e          *env
	peer       string
	n1, n2, n3 string
}

// newRing starts the ring: otfnode with role otfRole, n2 with peerRole,
// n3 a client.
func newRing(t *testing.T, peer, otfRole, peerRole string) *ring {
	return newRingArgs(t, peer, "MRP "+peerRole+" and client (go-profinet: MRP "+otfRole+")",
		[]string{"-role", otfRole}, []string{"--role", peerRole}, []string{"--role", "mrc"})
}

// newRingArgs starts a ring with otfnode's and the peer nodes' flags.
func newRingArgs(t *testing.T, peer, roles string, otf, n2, n3 []string) *ring {
	e := newEnv(t)
	e.result.peer(peer, image(peer), roles)
	r := &ring{e: e, peer: peer}
	r.n1 = e.ringNode("n1", runnerImage, append([]string{"/otfnode", "mrp"}, otf...)...)
	r.n2 = e.ringNode("n2", image(peer), append([]string{"node"}, n2...)...)
	r.n3 = e.ringNode("n3", image(peer), append([]string{"node"}, n3...)...)
	e.waitEvent(r.n1, 30*time.Second, "start", named("start"))
	e.cable(r.n1, r.n2)
	e.cable(r.n2, r.n3)
	e.cable(r.n3, r.n1)
	e.waitEvent(r.n1, 30*time.Second, "ready", named("ready"))
	for _, n := range []string{r.n2, r.n3} {
		if !until(30*time.Second, func() bool { return e.peerStatus(n).Ring != "" }) {
			t.Fatalf("%s: %s did not start", n, peer)
		}
	}
	return r
}

// burst sends a burst of probes from otfnode and returns how many each
// peer node received.
func (r *ring) burst() (sent int, got2, got3 int) {
	e := r.e
	e.t.Helper()
	b2, b3 := e.peerStatus(r.n2).Probes, e.peerStatus(r.n3).Probes
	before := len(e.events(r.n1))
	if _, err := docker(context.Background(), "kill", "-s", "USR1", r.n1); err != nil {
		e.t.Fatal(err)
	}
	var ev map[string]any
	if !until(10*time.Second, func() bool {
		evs := e.events(r.n1)
		for _, x := range evs[min(before, len(evs)):] {
			if x["event"] == "probes" {
				ev = x
				return true
			}
		}
		return false
	}) {
		e.t.Fatal("otfnode sent no probes")
	}
	time.Sleep(300 * time.Millisecond)
	sent = int(ev["sent"].(float64))
	return sent, e.peerStatus(r.n2).Probes - b2, e.peerStatus(r.n3).Probes - b3
}

// exact sends a burst and checks that each peer node received every probe
// once: no loop, no hole.
func (r *ring) exact(feature string) {
	r.e.t.Helper()
	sent, g2, g3 := r.burst()
	r.e.expect(feature, g2 == sent && g3 == sent, "probes sent %d, received by n2 %d, by n3 %d", sent, g2, g3)
}

// outage streams probes (one per ms) across a cable break at the node
// beyond it and returns the ring's measured outage: lost probes times the
// mean interval. The cable is restored afterwards.
func (r *ring) outage(beyond, node, port string) (lostMS float64, sent, got int) {
	e := r.e
	e.t.Helper()
	b := e.peerStatus(beyond).Probes
	_, _ = docker(context.Background(), "kill", "-s", "USR2", r.n1)
	time.Sleep(300 * time.Millisecond)
	e.setLink(node, port, false)
	time.Sleep(2 * time.Second)
	before := len(e.events(r.n1))
	_, _ = docker(context.Background(), "kill", "-s", "USR2", r.n1)
	var ev map[string]any
	until(10*time.Second, func() bool {
		evs := e.events(r.n1)
		for _, x := range evs[min(before, len(evs)):] {
			if x["event"] == "stream" {
				ev = x
				return true
			}
		}
		return false
	})
	time.Sleep(300 * time.Millisecond)
	if ev == nil {
		e.t.Fatal("otfnode: no stream event")
	}
	sent = int(ev["sent"].(float64))
	got = e.peerStatus(beyond).Probes - b
	ms := ev["ms"].(float64)
	if sent > 0 {
		lostMS = float64(sent-got) * ms / float64(sent)
	}
	return lostMS, sent, got
}

// TestMRPOtfManager: go-profinet is the ring's manager, the peer's two
// nodes are clients. The ring closes (the manager blocks its secondary port),
// carries every probe once, opens when the cable between the clients
// breaks (the clients flush their FDBs on the manager's TopologyChange),
// reaches the far client the other way round, and closes again.
func TestMRPOtfManager(t *testing.T) { eachPeer(t, testOtfManager) }

func testOtfManager(t *testing.T, peer string) {
	r := newRing(t, peer, "manager", "mrc")
	e := r.e
	pcap := "mrp-otf-manager-" + peer + ".pcap"
	stop := e.captureOn(r.n1, "ringA", "ether proto 0x88e3", pcap)
	closed := until(15*time.Second, func() bool {
		o := e.otfRing(r.n1)
		return !o.Open && len(o.Blocked) == 2 && !o.Blocked[0] && o.Blocked[1] && e.peerStatus(r.n2).Ring == "PT_IDLE" && e.peerStatus(r.n3).Ring == "PT_IDLE"
	})
	o := e.otfRing(r.n1)
	e.expect("MRP ring closed: go-profinet MRM, "+peer+" MRCs", closed, "otfnode %+v, n2 %s, n3 %s", o, e.peerStatus(r.n2).Ring, e.peerStatus(r.n3).Ring)
	e.expect("MRP test frames: go-profinet MRM receives its tests through "+peer+" MRCs", o.TestsReceived > 0 && o.Malformed == 0, "%+v", o)
	if !closed {
		return
	}
	r.exact("MRP closed ring: no loop, no hole (go-profinet MRM, " + peer + " MRCs)")

	f2 := e.peerStatus(r.n2).Flushes
	lost, sent, got := r.outage(r.n2, r.n2, "ringB")
	opened := until(5*time.Second, func() bool { o := e.otfRing(r.n1); return o.Open && !o.Blocked[1] })
	e.expect("MRP ring open: go-profinet MRM unblocks on a cable break between "+peer+" MRCs", opened, "%+v", e.otfRing(r.n1))
	if f2 < 0 {
		e.result.na("MRP FDB flush: "+peer+" MRC flushes on go-profinet's TopologyChange", "the kernel flushes; the adapter cannot observe it")
	} else {
		e.expect("MRP FDB flush: "+peer+" MRC flushes on go-profinet's TopologyChange", e.peerStatus(r.n2).Flushes > f2, "flushes %d → %d", f2, e.peerStatus(r.n2).Flushes)
	}
	e.result.add("MRP measured outage, cable break (go-profinet MRM, "+peer+" MRCs, 200 ms profile)", true,
		fmt.Sprintf("%.0f ms without traffic at the far client (%d of %d probes lost); a measurement, not a conformance claim", lost, sent-got, sent))
	t.Logf("outage %.0f ms (%d/%d probes)", lost, sent-got, sent)
	r.exact("MRP open ring: no loop, no hole (go-profinet MRM, " + peer + " MRCs)")

	e.setLink(r.n2, "ringB", true)
	reclosed := until(10*time.Second, func() bool { o := e.otfRing(r.n1); return !o.Open && o.Blocked[1] })
	e.expect("MRP ring closes again (go-profinet MRM, "+peer+" MRCs)", reclosed, "%+v", e.otfRing(r.n1))
	r.exact("MRP closed again: no loop, no hole (go-profinet MRM, " + peer + " MRCs)")
	stop()
	e.oracles(pcap)
}

// TestMRPOtfClient: the peer is the ring's manager, go-profinet a client
// next to it. The ring closes (the peer blocks its secondary port),
// carries every probe once; the cable between go-profinet and the other
// client breaks: go-profinet reports its link down, the peer opens the
// ring, go-profinet flushes on its TopologyChange; the cable back: closed.
func TestMRPOtfClient(t *testing.T) { eachPeer(t, testOtfClient) }

func testOtfClient(t *testing.T, peer string) {
	r := newRing(t, peer, "client", "mrm")
	e := r.e
	pcap := "mrp-otf-client-" + peer + ".pcap"
	stop := e.captureOn(r.n1, "ringB", "ether proto 0x88e3", pcap)
	// Closed: CHK_RC with one ring port blocked, usually the secondary;
	// a manager may swap its port roles after a link change of its
	// primary port (umrp does).
	closed := until(15*time.Second, func() bool {
		s := e.peerStatus(r.n2)
		return s.Ring == "CHK_RC" && len(s.Ports) == 2 && (s.Ports[0].State == "blocked") != (s.Ports[1].State == "blocked")
	})
	e.expect("MRP ring closed: "+peer+" MRM, go-profinet MRC", closed, "n2 %+v, otfnode %+v", e.peerStatus(r.n2), e.otfRing(r.n1))
	if !closed {
		return
	}
	o := e.otfRing(r.n1)
	e.expect("MRP go-profinet MRC: both ring ports forwarding, ring closed", !o.Open && !o.Blocked[0] && !o.Blocked[1] && o.Malformed == 0, "%+v", o)
	r.exact("MRP closed ring: no loop, no hole (" + peer + " MRM, go-profinet MRC)")

	tc := o.TopologyChanges
	lc := o.LinkChanges
	lost, sent, got := r.outage(r.n3, r.n3, "ringB")
	opened := until(5*time.Second, func() bool { return e.peerStatus(r.n2).Ring == "CHK_RO" })
	e.expect("MRP ring open: "+peer+" MRM on go-profinet's link change", opened, "n2 %+v", e.peerStatus(r.n2))
	o = e.otfRing(r.n1)
	e.expect("MRP link change: go-profinet MRC reports its link down", o.LinkChanges > lc, "link changes %d → %d", lc, o.LinkChanges)
	e.expect("MRP TopologyChange: go-profinet MRC receives "+peer+"'s and flushes", o.TopologyChanges > tc, "topology changes %d → %d", tc, o.TopologyChanges)
	e.result.add("MRP measured outage, cable break ("+peer+" MRM, go-profinet MRC, 200 ms profile)", true,
		fmt.Sprintf("%.0f ms without traffic at the far client (%d of %d probes lost); a measurement, not a conformance claim", lost, sent-got, sent))
	t.Logf("outage %.0f ms (%d/%d probes)", lost, sent-got, sent)
	r.exact("MRP open ring: no loop, no hole (" + peer + " MRM, go-profinet MRC)")

	e.setLink(r.n3, "ringB", true)
	reclosed := until(10*time.Second, func() bool { return e.peerStatus(r.n2).Ring == "CHK_RC" })
	e.expect("MRP ring closes again ("+peer+" MRM, go-profinet MRC)", reclosed, "n2 %+v", e.peerStatus(r.n2))
	r.exact("MRP closed again: no loop, no hole (" + peer + " MRM, go-profinet MRC)")
	stop()
	e.oracles(pcap)
}

// TestMRPRingFaults: seeded cable faults (PROFINET_INTEROP_SEED, default
// 1) on a ring of go-profinet's manager and the peer's clients: each round
// breaks a random cable for a random time and restores it; after every
// change the ring must settle (open or closed as the cables say) and
// carry every probe exactly once.
func TestMRPRingFaults(t *testing.T) { eachPeer(t, testRingFaults) }

func testRingFaults(t *testing.T, peer string) {
	seed := uint64(1)
	if s := os.Getenv("PROFINET_INTEROP_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	rng := rand.New(rand.NewPCG(seed, 0x6d7270))
	r := newRing(t, peer, "manager", "mrc")
	e := r.e
	settled := func(open bool) bool {
		return until(10*time.Second, func() bool {
			o := e.otfRing(r.n1)
			return o.Open == open && len(o.Blocked) == 2 && o.Blocked[1] == !open
		})
	}
	if !settled(false) {
		t.Fatalf("ring did not close: %+v", e.otfRing(r.n1))
	}
	// The cables by one of their ends.
	cables := []struct{ node, port, name string }{{r.n1, "ringB", "n1-n2"}, {r.n2, "ringB", "n2-n3"}, {r.n3, "ringB", "n3-n1"}}
	rounds, ok := 8, true
	for i := range rounds {
		c := cables[rng.IntN(len(cables))]
		down := time.Duration(200+rng.IntN(1300)) * time.Millisecond
		e.setLink(c.node, c.port, false)
		time.Sleep(down)
		if !settled(true) {
			ok = false
			t.Errorf("round %d (seed %d): %s down %s: ring did not open: %+v", i, seed, c.name, down, e.otfRing(r.n1))
			break
		}
		sent, g2, g3 := r.burst()
		if g2 != sent || g3 != sent {
			ok = false
			t.Errorf("round %d (seed %d): %s down: probes %d, n2 %d, n3 %d", i, seed, c.name, sent, g2, g3)
		}
		e.setLink(c.node, c.port, true)
		if !settled(false) {
			ok = false
			t.Errorf("round %d (seed %d): %s up: ring did not close: %+v", i, seed, c.name, e.otfRing(r.n1))
			break
		}
		sent, g2, g3 = r.burst()
		if g2 != sent || g3 != sent {
			ok = false
			t.Errorf("round %d (seed %d): %s up: probes %d, n2 %d, n3 %d", i, seed, c.name, sent, g2, g3)
		}
	}
	e.result.add("MRP seeded cable faults, go-profinet MRM with "+peer+" MRCs: settles, no loop, no hole", ok, fmt.Sprintf("seed %d, %d rounds", seed, rounds))
}

// TestMRPAuto: automatic managers (MRA). go-profinet and the peer's two
// nodes are all MRAs; the best (the lowest priority) manages and the
// others act as its clients:
//
//   - go-profinet has the best priority: it manages, the peers give way;
//   - a peer node has: go-profinet gives way to it; that node fails (both
//     its cables down) and go-profinet, the next best, manages the rest;
//     the node comes back and go-profinet gives way again.
//
// After each change the ring carries every probe exactly once.
//
// The ring runs the 500 ms profile: an MRA client takes the manager role
// after TSTNRmax test intervals without the manager's tests, which in the
// 200 ms profile is shorter than a loaded micro-VM may stall a node (a
// kernel MRA took over during a 280 ms stall of otfnode's tests).
func TestMRPAuto(t *testing.T) { eachPeer(t, testAuto) }

func testAuto(t *testing.T, peer string) {
	const best, worse = "36864", "41216" // 0x9000; 0xA100, worse than the default 0xA000
	otf := func(args ...string) []string { return append([]string{"-role", "auto", "-profile", "500"}, args...) }
	mra := func(args ...string) []string { return append([]string{"--role", "mra", "--recv", "500"}, args...) }
	t.Run("go-profinet-best", func(t *testing.T) {
		r := newRingArgs(t, peer, "MRP automatic managers (go-profinet: MRA)",
			otf("-prio", best), mra(), mra())
		e := r.e
		// go-profinet's own frames (its ring port's address): the peer's
		// MRP_Options carry the OUI 00-00-00, which tshark does not decode.
		mac := strings.TrimSpace(string(e.exec(r.n1, "cat", "/sys/class/net/ringA/address")))
		pcap := "mrp-auto-" + peer + ".pcap"
		stop := e.captureOn(r.n1, "ringA", "ether proto 0x88e3 and ether src "+mac, pcap)
		// Each peer node gives way: it acts as a client at least once
		// (umrp does not stay one: flapping, below).
		var gave [2]bool
		ok := until(20*time.Second, func() bool {
			o := e.otfRing(r.n1)
			for i, n := range []string{r.n2, r.n3} {
				gave[i] = gave[i] || e.peerStatus(n).Role == "MRC"
			}
			return o.Acting == "manager" && !o.Open && len(o.Blocked) == 2 && o.Blocked[1] && gave[0] && gave[1]
		})
		e.expect("MRA: go-profinet (best priority) manages, "+peer+" MRAs give way", ok, "otfnode %+v, n2 %+v, n3 %+v", e.otfRing(r.n1), e.peerStatus(r.n2), e.peerStatus(r.n3))
		if ok {
			r.exactMRA("MRA ring closed by go-profinet: no loop, no hole (" + peer + " MRAs)")
			mark, own := r.marks(), r.otfMark()
			time.Sleep(3 * time.Second)
			r.exactMRA("MRA ring still closed by go-profinet: no loop, no hole (" + peer + " MRAs)")
			c := r.otfActingChanges(own)
			e.expect("MRA: go-profinet stays the manager", c == 0 && e.otfRing(r.n1).Acting == "manager", "acting changes %d in 3 s of the settled ring", c)
			r.flapping(mark)
		}
		stop()
		e.oracles(pcap)
	})
	t.Run("peer-best", func(t *testing.T) {
		r := newRingArgs(t, peer, "MRP automatic managers (go-profinet: MRA)",
			otf(), mra("--prio", best), mra("--prio", worse))
		e := r.e
		managed := func() bool {
			o := e.otfRing(r.n1)
			s2 := e.peerStatus(r.n2)
			return s2.Role == "MRM" && s2.Ring == "CHK_RC" && o.Acting == "client" && !o.Blocked[0] && !o.Blocked[1] &&
				e.peerStatus(r.n3).Role == "MRC"
		}
		ok := until(20*time.Second, managed)
		e.expect("MRA: go-profinet gives way to a better "+peer+" MRA", ok, "otfnode %+v, n2 %+v, n3 %+v", e.otfRing(r.n1), e.peerStatus(r.n2), e.peerStatus(r.n3))
		if !ok {
			return
		}
		r.exactMRA("MRA ring closed by " + peer + ": no loop, no hole (go-profinet MRA as client)")
		mark, own := r.marks(), r.otfMark()
		time.Sleep(3 * time.Second)
		r.exactMRA("MRA ring still closed by " + peer + ": no loop, no hole (go-profinet MRA as client)")
		c := r.otfActingChanges(own)
		e.expect("MRA: go-profinet stays a client of the better "+peer+" MRA", c == 0 && e.otfRing(r.n1).Acting == "client",
			"acting changes %d in 3 s of the settled ring, %+v", c, e.otfRing(r.n1))
		r.flapping(mark)

		// The manager fails: both its cables down.
		e.setLink(r.n2, "ringA", false)
		e.setLink(r.n2, "ringB", false)
		took := until(20*time.Second, func() bool {
			return e.otfRing(r.n1).Acting == "manager" && e.peerStatus(r.n3).Role == "MRC"
		})
		e.expect("MRA: go-profinet (next best) manages when the "+peer+" manager fails", took, "otfnode %+v, n3 %+v", e.otfRing(r.n1), e.peerStatus(r.n3))
		if took {
			sent, _, g3 := r.burst()
			e.expect("MRA after the manager failed: no loop, no hole (go-profinet manages, "+peer+" MRA as client)", g3 == sent, "probes sent %d, received by n3 %d", sent, g3)
		}

		// It comes back: go-profinet gives way again.
		e.setLink(r.n2, "ringA", true)
		e.setLink(r.n2, "ringB", true)
		back := until(20*time.Second, managed)
		e.expect("MRA: go-profinet gives way again when the better "+peer+" MRA returns", back, "otfnode %+v, n2 %+v, n3 %+v", e.otfRing(r.n1), e.peerStatus(r.n2), e.peerStatus(r.n3))
		if back {
			r.exactMRA("MRA ring closed again by " + peer + ": no loop, no hole")
		}
	})
}

// changes counts the changes of a field of a node's ring events.
func changes(evs []map[string]any, field string) int {
	n, last := 0, ""
	for _, ev := range evs {
		if ev["event"] != "ring" {
			continue
		}
		if r, _ := ev[field].(string); r != last {
			if last != "" {
				n++
			}
			last = r
		}
	}
	return n
}

// otfMark returns how many events otfnode printed so far.
func (r *ring) otfMark() int { return len(r.e.events(r.n1)) }

// otfActingChanges counts otfnode's changes between manager and client
// since mark (from the last event before it).
func (r *ring) otfActingChanges(mark int) int {
	evs := r.e.events(r.n1)
	return changes(evs[min(max(mark-1, 0), len(evs)):], "acting")
}

// exactMRA is exact for a ring of automatic managers: probes counted
// twice while a peer node changed role during the burst are that peer's
// deviation (umrp's: as a client it takes the manager role back, which
// stops the manager's tests, so the manager opens the ring per MRP, and
// gives way again, which closes it before the manager's next test), not a
// failure of go-profinet. Without a role change they fail.
func (r *ring) exactMRA(feature string) {
	e := r.e
	e.t.Helper()
	mark := r.marks()
	sent, g2, g3 := r.burst()
	ok := g2 == sent && g3 == sent
	detail := fmt.Sprintf("probes sent %d, received by n2 %d, by n3 %d", sent, g2, g3)
	if !ok {
		if c := r.roleChanges(mark); c > 0 {
			e.result.deviation(feature, fmt.Sprintf("%s while %s nodes changed role %d times during the burst (their MRA reclaiming the manager role)", detail, r.peer, c))
			return
		}
	}
	e.expect(feature, ok, "%s", detail)
}

// roleChanges counts the peer nodes' role changes since mark.
func (r *ring) roleChanges(mark [2]int) int {
	n := 0
	for i, c := range []string{r.n2, r.n3} {
		evs := r.e.events(c)
		// From the last event before the mark: a change at the first
		// event after it counts.
		n += changes(evs[min(max(mark[i]-1, 0), len(evs)):], "role")
	}
	return n
}

// marks returns how many events each peer node printed so far.
func (r *ring) marks() [2]int {
	return [2]int{len(r.e.events(r.n2)), len(r.e.events(r.n3))}
}

// flapping records peer nodes that changed role in a ring that had
// settled (since mark). umrp's MRA, as a client, ignores the tests of the
// manager it gave way to (its ring_mac) in its test monitor, takes the
// manager role back after TSTNRmax intervals and gives way again on the
// next NAck, whoever the manager is (two umrp nodes alike).
func (r *ring) flapping(mark [2]int) {
	e := r.e
	for i, n := range []string{r.n2, r.n3} {
		evs := e.events(n)
		c := changes(evs[min(max(mark[i]-1, 0), len(evs)):], "role")
		if c == 0 {
			continue
		}
		why := fmt.Sprintf("%d role changes on %s in 3 s of a settled ring", c, n[strings.LastIndex(n, "-")+1:])
		if r.peer == "umrp" {
			why += ": umrp's MRC test monitor ignores the tests of the manager it gave way to " +
				"(state_machine.c, mrp_mra_recv_ring_test) and reclaims the role after TSTNRmax intervals; seen with a umrp manager too"
		}
		e.result.deviation("MRA: "+r.peer+" MRA as a client keeps taking the manager role back", why)
		return
	}
}
