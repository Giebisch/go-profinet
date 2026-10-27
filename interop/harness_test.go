// SPDX-License-Identifier: MIT

//go:build interop

// Package interop runs go-profinet against the reference implementations
// of otfabric/profinet-interop: p-net and pnio-rs (IO-Devices), profinet-py
// (IO-Controller and DCP tool), pnio-dcp (DCP tool), and checks every
// capture with two independent decoders, scapy and tshark.
//
// Each test creates a Docker bridge network, starts the reference stack in
// one container and go-profinet's otfnode (built here for Linux) in
// another, both with CAP_NET_RAW, so that DCP, LLDP, RT and alarm frames
// travel on Layer 2 between them. Run with make interop (needs Docker).
// The images default to local builds of profinet-interop (make images in
// that repository); PROFINET_INTEROP_<NAME>_IMAGE overrides each.
package interop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// image returns the image of an adapter: PROFINET_INTEROP_<NAME>_IMAGE or
// the local development build profinet-interop-<name>:dev.
func image(name string) string {
	env := "PROFINET_INTEROP_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_IMAGE"
	if v := os.Getenv(env); v != "" {
		return v
	}
	return "profinet-interop-" + name + ":dev"
}

const runnerImage = "go-profinet-interop-runner:dev"

var (
	setupOnce sync.Once
	setupErr  error
	otfnode   string // path of the Linux build of otfnode
	shareRoot string
	testdata  string // interop/testdata, mounted at /otfnode-testdata
	gsdmlData string // gsdml/testdata, mounted at /gsdml-testdata
)

func docker(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// setup builds otfnode for the Docker host's architecture and the runner
// image, once per test binary.
func setup(t *testing.T) {
	t.Helper()
	setupOnce.Do(func() {
		ctx := context.Background()
		arch, err := docker(ctx, "info", "--format", "{{.Architecture}}")
		if err != nil {
			setupErr = fmt.Errorf("Docker is not available: %w", err)
			return
		}
		goarch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64", "amd64": "amd64"}[strings.TrimSpace(string(arch))]
		if goarch == "" {
			goarch = runtime.GOARCH
		}
		dir, err := os.MkdirTemp("", "profinet-interop-")
		if err != nil {
			setupErr = err
			return
		}
		shareRoot = dir
		otfnode = filepath.Join(dir, "otfnode")
		if testdata, err = filepath.Abs("testdata"); err != nil {
			setupErr = err
			return
		}
		if gsdmlData, err = filepath.Abs("../gsdml/testdata"); err != nil {
			setupErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", otfnode, "./otfnode")
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			setupErr = fmt.Errorf("build otfnode: %v: %s", err, out)
			return
		}
		if _, err := docker(ctx, "build", "-q", "-t", runnerImage, "runner"); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
}

// env is the Docker network of one test.
type env struct {
	t          *testing.T
	network    string
	prefix     string
	share      string
	containers []string
	// outputs are what the otfnode runs printed, logged when the test fails.
	outputs []string
	mu      sync.Mutex
	result  *result
}

var subnetSeq struct {
	sync.Mutex
	n int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	setup(t)
	var b [3]byte
	_, _ = rand.Read(b[:])
	subnetSeq.Lock()
	subnetSeq.n++
	n := subnetSeq.n
	subnetSeq.Unlock()
	e := &env{t: t, network: "pn-it-" + hex.EncodeToString(b[:]), prefix: fmt.Sprintf("10.%d.%d.", 90+int(b[0])%40, n%250)}
	e.share = filepath.Join(shareRoot, e.network)
	if err := os.MkdirAll(e.share, 0o777); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := docker(ctx, "network", "create", "--subnet", e.prefix+"0/24", e.network); err != nil {
		t.Fatal(err)
	}
	e.result = newResult(t)
	t.Cleanup(func() {
		if t.Failed() {
			// otfnode's events and report (state changes with their
			// causes, cyclic statistics), and the captures, which the CI
			// uploads with the results.
			for _, o := range e.outputs {
				t.Logf("otfnode %s", o)
			}
			e.keepCaptures()
		}
		for _, c := range e.containers {
			if t.Failed() {
				if out, err := docker(ctx, "logs", "--tail", "60", c); err == nil {
					t.Logf("logs of %s:\n%s", c, out)
				}
				// The raw log of the implementation, for event patterns
				// that did not match.
				if out, err := docker(ctx, "exec", c, "sh", "-c", "tail -n 120 /run/profinet-interop/*.log 2>/dev/null"); err == nil && len(out) > 0 {
					t.Logf("raw log of %s:\n%s", c, out)
				}
			}
			_, _ = docker(ctx, "rm", "-f", c)
		}
		_, _ = docker(ctx, "network", "rm", e.network)
		e.result.write()
	})
	return e
}

func (e *env) ip(host int) string { return fmt.Sprintf("%s%d", e.prefix, host) }

// mac returns the fixed MAC address of a host: tools that run in a new
// container for every operation keep their address, as a real tool on one
// PC does (p-net ignores a second requester for 3 s after a Get or Set).
func (e *env) mac(host int) string {
	var a, b int
	_, _ = fmt.Sscanf(e.prefix, "10.%d.%d.", &a, &b)
	return fmt.Sprintf("02:42:0a:%02x:%02x:%02x", a, b, host)
}

func (e *env) baseArgs(name string, host int) []string {
	return []string{"--network", e.network, "--ip", e.ip(host), "--mac-address", e.mac(host), "--cap-add", "NET_RAW", "--cap-add", "NET_ADMIN",
		"-v", otfnode + ":/otfnode:ro", "-v", e.share + ":/share", "-v", testdata + ":/otfnode-testdata:ro",
		"-v", gsdmlData + ":/gsdml-testdata:ro", "--name", name}
}

// start runs a detached container.
func (e *env) start(name, img string, host int, args ...string) string {
	e.t.Helper()
	full := e.network + "-" + name
	a := append([]string{"run", "-d"}, e.baseArgs(full, host)...)
	a = append(a, img)
	a = append(a, args...)
	if _, err := docker(context.Background(), a...); err != nil {
		e.t.Fatal(err)
	}
	e.mu.Lock()
	e.containers = append(e.containers, full)
	e.mu.Unlock()
	return full
}

// run runs a container to completion and returns its stdout.
func (e *env) run(name, img string, host int, timeout time.Duration, args ...string) []byte {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	full := e.network + "-" + name
	a := append([]string{"run", "--rm"}, e.baseArgs(full, host)...)
	a = append(a, img)
	a = append(a, args...)
	out, err := docker(ctx, a...)
	if err != nil {
		e.t.Fatalf("%v\nstdout:\n%s", err, out)
	}
	return out
}

// otf runs otfnode with args in the runner image.
func (e *env) otf(name string, host int, timeout time.Duration, args ...string) []byte {
	out := e.run(name, runnerImage, host, timeout, append([]string{"/otfnode"}, args...)...)
	e.mu.Lock()
	e.outputs = append(e.outputs, fmt.Sprintf("%s:\n%s", name, out))
	e.mu.Unlock()
	return out
}

// startOTF starts otfnode detached.
func (e *env) startOTF(name string, host int, args ...string) string {
	return e.start(name, runnerImage, host, append([]string{"/otfnode"}, args...)...)
}

// exec runs a command in a running container.
func (e *env) exec(container string, args ...string) []byte {
	e.t.Helper()
	out, err := docker(context.Background(), append([]string{"exec", container}, args...)...)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

// forwardLLDP makes the network's Linux bridge forward LLDP frames
// (group_fwd_mask bit 14), which bridges drop by default. It reports
// whether that worked; it needs a privileged container in the host's
// network namespace (Docker on Linux, Docker Desktop's VM).
func (e *env) forwardLLDP() bool {
	id, err := docker(context.Background(), "network", "inspect", "-f", "{{.Id}}", e.network)
	if err != nil {
		return false
	}
	br := "br-" + strings.TrimSpace(string(id))[:12]
	_, err = docker(context.Background(), "run", "--rm", "--privileged", "--network", "host", runnerImage,
		"sh", "-c", "echo 16384 > /sys/class/net/"+br+"/bridge/group_fwd_mask")
	return err == nil
}

// events returns the JSON lines a container wrote so far.
func (e *env) events(container string) []map[string]any {
	out, _ := docker(context.Background(), "logs", container)
	return jsonLines(out)
}

func jsonLines(b []byte) []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// waitEvent waits for an event of a container that matches.
func (e *env) waitEvent(container string, timeout time.Duration, what string, match func(map[string]any) bool) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ev := range e.events(container) {
			if match(ev) {
				return ev
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("%s: no event %s within %s", container, what, timeout)
	return nil
}

// eventually reports whether a container logs an event that matches
// within timeout: the implementation logs some events (Release) after the
// operation that caused them has returned.
func (e *env) eventually(container string, timeout time.Duration, match func(map[string]any) bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		for _, ev := range e.events(container) {
			if match(ev) {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// until polls cond until it holds or the timeout expires.
func until(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// named matches an event by name.
func named(names ...string) func(map[string]any) bool {
	return func(m map[string]any) bool {
		for _, n := range names {
			if m["event"] == n {
				return true
			}
		}
		return false
	}
}

func hasEvent(evs []map[string]any, name string) bool {
	for _, ev := range evs {
		if ev["event"] == name {
			return true
		}
	}
	return false
}

func last(evs []map[string]any, name string) map[string]any {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i]["event"] == name {
			return evs[i]
		}
	}
	return nil
}

// capture starts tcpdump in the network namespace of container; the
// returned function stops it and returns the capture's path in the share.
func (e *env) capture(container, file string) func() string {
	e.t.Helper()
	return e.captureOn(container, "eth0", "", file)
}

// captureOn captures on one interface of a container, with a pcap filter
// (empty: every frame).
func (e *env) captureOn(container, iface, filter, file string) func() string {
	e.t.Helper()
	full := e.network + "-capture-" + strings.TrimSuffix(file, ".pcap")
	_, err := docker(context.Background(), "run", "-d", "--name", full, "--network", "container:"+container, "--cap-add", "NET_RAW", "--cap-add", "NET_ADMIN",
		"-v", e.share+":/share", runnerImage, "tcpdump", "-i", iface, "-U", "-s", "0", "-w", "/share/"+file, filter)
	if err != nil {
		e.t.Fatal(err)
	}
	e.mu.Lock()
	e.containers = append(e.containers, full)
	e.mu.Unlock()
	time.Sleep(time.Second)
	return func() string {
		_, _ = docker(context.Background(), "stop", "-t", "2", full)
		return file
	}
}

// peerDeviation is an expert info of tshark that a documented deviation of
// the peer causes in the frames it sends (see docs/INTEROPERABILITY.md of
// profinet-interop). It is recorded as the peer's deviation, not as a
// failure of go-profinet.
type peerDeviation struct {
	Protocol, Message string
	Why               string
}

// oracles decodes a capture with scapy and tshark and fails on malformed,
// undecoded or warned-about frames, except for the expert infos of the
// peer's documented deviations.
func (e *env) oracles(file string, deviations ...peerDeviation) {
	e.t.Helper()
	sc := e.run("scapy", image("scapy"), 250, 2*time.Minute, "decode", "/share/"+file)
	var s struct {
		Frames         int            `json:"frames"`
		ByKind         map[string]int `json:"byKind"`
		UndecodedCount int            `json:"undecodedCount"`
		Undecoded      []any          `json:"undecoded"`
		NotDissected   []struct {
			Kind string `json:"kind"`
		} `json:"notDissected"`
		NotDissectedCount int `json:"notDissectedCount"`
	}
	if err := json.Unmarshal(lastLine(sc), &s); err != nil {
		e.t.Fatalf("scapy output: %v: %s", err, sc)
	}
	e.result.add("scapy decode "+file, s.UndecodedCount == 0, fmt.Sprintf("%d frames %v, %d undecoded, %d without a scapy dissector", s.Frames, s.ByKind, s.UndecodedCount, s.NotDissectedCount))
	// scapy has no MRP or PTCP dissector: it reads those frames without
	// decoding them, which is a limit of the decoder, not evidence.
	if s.NotDissectedCount > 0 {
		kinds := map[string]bool{}
		for _, n := range s.NotDissected {
			kinds[n.Kind] = true
		}
		for _, k := range []string{"mrp", "ptcp"} {
			if kinds[k] {
				e.result.deviation("scapy decode "+file+": "+strings.ToUpper(k)+" frames not dissected",
					"decoder limit: scapy "+"has no "+strings.ToUpper(k)+" dissector; the frames are read, not decoded (tshark decodes them)")
			}
		}
	}
	if s.UndecodedCount != 0 {
		e.t.Errorf("scapy could not decode %d frames of %s: %v", s.UndecodedCount, file, s.Undecoded)
	}
	ts := e.run("tshark", image("tshark"), 251, 2*time.Minute, "check", "/share/"+file)
	var w struct {
		Frames    int            `json:"frames"`
		ByProto   map[string]int `json:"byProtocol"`
		Malformed []any          `json:"malformed"`
		Expert    []any          `json:"expert"`
	}
	if err := json.Unmarshal(lastLine(ts), &w); err != nil {
		e.t.Fatalf("tshark output: %v: %s", err, ts)
	}
	var expert []any
	seen := map[int]bool{}
	for _, x := range w.Expert {
		m, _ := x.(map[string]any)
		if m != nil {
			// tshark joins the notes of a frame; "Possible traceroute"
			// is the IP dissector's guess from the TTL of consecutive
			// packets, not a PROFINET decoding problem.
			msg := strings.Trim(traceroute.ReplaceAllString(str(m["message"]), ""), ", ")
			if msg == "" {
				continue
			}
			m["message"] = msg
		}
		matched := false
		for i, d := range deviations {
			if m != nil && m["protocol"] == d.Protocol && str(m["message"]) == d.Message {
				matched = true
				if !seen[i] {
					seen[i] = true
					e.result.deviation(fmt.Sprintf("tshark: %s %q", d.Protocol, d.Message), d.Why)
				}
			}
		}
		if !matched {
			expert = append(expert, x)
		}
	}
	w.Expert = expert
	ok := len(w.Malformed) == 0 && len(w.Expert) == 0
	e.result.add("tshark check "+file, ok, fmt.Sprintf("%d frames %v, %d malformed, %d warnings", w.Frames, w.ByProto, len(w.Malformed), len(w.Expert)))
	if !ok {
		e.t.Errorf("tshark on %s: malformed %v expert %v", file, w.Malformed, w.Expert)
	}
}

// traceroute matches the IP dissector's "Possible traceroute" note.
var traceroute = regexp.MustCompile(`Possible traceroute: hop #\d+, attempt #\d+,?`)

func lastLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	return lines[len(lines)-1]
}

// report returns the "report" document of a controller run.
func report(t *testing.T, out []byte) map[string]any {
	t.Helper()
	r := last(jsonLines(out), "report")
	if r == nil {
		t.Fatalf("no report in:\n%s", out)
	}
	return r
}

// result collects the checks of a test for the interoperability matrix.
type result struct {
	t      *testing.T
	Test   string  `json:"test"`
	Peer   string  `json:"peer"`
	Image  string  `json:"image"`
	Role   string  `json:"role"`
	Checks []check `json:"checks"`
	mu     sync.Mutex
}

type check struct {
	Feature string `json:"feature"`
	// Outcome is pass, fail, n/a (the peer does not implement it) or
	// peer-deviation (the peer deviates from the standard; detail says how).
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

func newResult(t *testing.T) *result { return &result{t: t, Test: t.Name()} }

func (r *result) peer(name, img, role string) { r.Peer, r.Image, r.Role = name, img, role }

func (r *result) add(feature string, ok bool, detail string) {
	o := "pass"
	if !ok {
		o = "fail"
	}
	r.mu.Lock()
	r.Checks = append(r.Checks, check{Feature: feature, Outcome: o, Detail: detail})
	r.mu.Unlock()
}

// deviation records a deviation of the peer from the standard that the
// test observed and attributed.
func (r *result) deviation(feature, why string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Checks = append(r.Checks, check{Feature: feature, Outcome: "peer-deviation", Detail: why})
}

func (r *result) na(feature, why string) {
	r.mu.Lock()
	r.Checks = append(r.Checks, check{Feature: feature, Outcome: "n/a", Detail: why})
	r.mu.Unlock()
}

// keepCaptures copies the test's captures to PROFINET_INTEROP_RESULTS,
// when set.
func (e *env) keepCaptures() {
	dir := os.Getenv("PROFINET_INTEROP_RESULTS")
	if dir == "" {
		return
	}
	files, _ := filepath.Glob(filepath.Join(e.share, "*.pcap"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		name := strings.NewReplacer("/", "_").Replace(e.t.Name()) + "-" + filepath.Base(f)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
}

// write stores the result in PROFINET_INTEROP_RESULTS, when set.
func (r *result) write() {
	dir := os.Getenv("PROFINET_INTEROP_RESULTS")
	if dir == "" || len(r.Checks) == 0 {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	b, _ := json.MarshalIndent(r, "", "  ")
	name := strings.NewReplacer("/", "_", " ", "_").Replace(r.Test) + ".json"
	_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
}

// expect records a check and reports a failure.
func (e *env) expect(feature string, ok bool, format string, a ...any) {
	e.t.Helper()
	detail := fmt.Sprintf(format, a...)
	e.result.add(feature, ok, detail)
	if !ok {
		e.t.Errorf("%s: %s", feature, detail)
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
