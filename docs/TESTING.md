# Testing

go-profinet is tested in tiers, from codecs to independent implementations
on real Layer 2 networks.

| Tier | What | How to run |
|------|------|------------|
| Unit | every codec with golden vectors written from the standard, every state machine, every option and error path | `make test` (race detector) |
| Fuzz | every decoder of network input and of documents (18 targets): no panic, and what decodes re-encodes to the same value | `make fuzz` (`FUZZTIME`, 30 s each; 10 s in CI) |
| End to end | controller and devices on the virtual network: startup modes, records, alarms, diagnosis, ModuleDiff, IP assignment and rename, many devices, recovery, activity timeouts, a second controller, reset, topology | `make test-e2e` |
| Chaos | the same with seeded loss, duplication, corruption, reordering, delay and jitter, a reconnect storm, malformed input of every kind | part of `make test-e2e` (seeds are logged; `vnet.New(seed)` reproduces) |
| Chaos monkey | a controller with three devices under a seeded random sequence of disruptions (fault profiles, link flaps, device restarts, renames, devices removed and re-added, diagnosis storms, record and output traffic); afterwards every AR must be Running with fresh data both ways and working records, and no goroutine may be left (`TestChaosMonkey`; 40 steps, 400 in the extended workflow; `PROFINET_CHAOS_SEED` and `PROFINET_MONKEY_STEPS` reproduce a run). Verified with seeds 7, 42, 99 (200 steps), 1000–1002 | part of `make test-e2e` |
| Leaks | three full sessions (an AR with cyclic data, records, an alarm, a device access AR, DCP, LLDP), then everything closed: the goroutine count returns to its baseline (`TestNoLeaks`; it fails with 11 goroutines left when the controller is not closed) | part of `make test-e2e` |
| Real Layer 2 | the AF_PACKET (Linux) and BPF (macOS) backends on a veth or feth pair, and an AR over them | `make l2-test` (Docker), CI jobs `l2-linux`, `l2-macos` |
| Interop | p-net, pnio-rs, profinet-py and pnio-dcp in containers, captures decoded by scapy and tshark | `make interop-images && make interop` ([INTEROPERABILITY.md](../INTEROPERABILITY.md)) |
| Soak | every CI job (unit with and without the race detector on the oldest and newest Go, coverage, e2e and chaos, every native platform, real Layer 2 on Linux and macOS) and the interop suite, each run N times in parallel (default 25), run N with chaos seed 1000+N; required to be all green before a release | the Soak workflow: Actions tab, or a push to a `soak/…` branch |
| Benchmarks | codecs, cyclic path, mux, vnet | `make bench` ([BENCHMARKS.md](BENCHMARKS.md)) |
| Hardware | commercial PLCs and devices | not done: no hardware available |

Coverage is gated at 90 % of the statements of the library packages
([COVERAGE.md](COVERAGE.md)).

## The virtual network

`vnet` is an in-memory Ethernet switch: endpoints are `l2.Port`s, frames are
delivered to the destination (or flooded), and each endpoint can inject
faults with a seeded random source:

```go
n := vnet.New(42)
ep, _ := n.NewEndpoint(vnet.EndpointConfig{MAC: mac})
ep.SetFaults(vnet.Faults{Loss: 0.05, Duplicate: 0.01, Corrupt: 0.01, Reorder: 0.02, Delay: time.Millisecond, Jitter: 2 * time.Millisecond,
	Match: func(f []byte) bool { return true }})
ep.SetLinkUp(false)
n.AddTap(func(from *vnet.Endpoint, frame []byte) { /* observe */ })
```

`netstack` gives an endpoint ARP, IPv4 and UDP, so that DCE/RPC runs in
Ethernet frames on the same switch, and a test exercises the whole stack in
one process without privileges.

## Writing tests

- Prefer a test against a go-profinet peer on vnet over a mock: the e2e and
  the controller and device harnesses show how.
- A protocol fact learned from a reference implementation gets an interop
  check and a comment naming the implementation.
- Tests that depend on timing wait with deadlines (`eventually`), not
  sleeps, and stay correct under the race detector on a loaded machine.
