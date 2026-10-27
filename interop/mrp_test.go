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
	Ring    string `json:"ring"`
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
	State           string `json:"state"`
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
	e := newEnv(t)
	e.result.peer(peer, image(peer), "MRP "+peerRole+" and client (go-profinet: MRP "+otfRole+")")
	r := &ring{e: e, peer: peer}
	r.n1 = e.ringNode("n1", runnerImage, "/otfnode", "mrp", "-role", otfRole)
	r.n2 = e.ringNode("n2", image(peer), "node", "--role", peerRole)
	r.n3 = e.ringNode("n3", image(peer), "node", "--role", "mrc")
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
