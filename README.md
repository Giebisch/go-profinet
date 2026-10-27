# go-profinet - PROFINET IO Controller, Device, DCP, LLDP and GSDML Library for Go

[![Go Version](https://img.shields.io/badge/Go-1.23%2B-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Go Reference](https://pkg.go.dev/badge/github.com/otfabric/go-profinet.svg)](https://pkg.go.dev/github.com/otfabric/go-profinet)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/go-profinet/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-profinet/actions/workflows/ci.yml)
[![Interop](https://github.com/otfabric/go-profinet/actions/workflows/interop.yml/badge.svg)](https://github.com/otfabric/go-profinet/actions/workflows/interop.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/go-profinet/graph/badge.svg)](https://codecov.io/gh/otfabric/go-profinet)
[![Latest Release](https://img.shields.io/github/v/release/otfabric/go-profinet?label=release)](https://github.com/otfabric/go-profinet/releases)

A pure Go implementation of PROFINET IO (IEC 61158-5-10/6-10, IEC 61784-2),
the industrial Ethernet of PLCs, remote IO, drives and field devices. No
cgo, no dependencies outside the standard library.

The library provides:

- An **IO-Controller** (`controller`): finds its devices by station name
  with DCP, optionally assigns their IP address, establishes an application
  relationship (AR) in legacy (RT_CLASS_1) or advanced (RT_CLASS_2)
  startup, writes the parameters, exchanges cyclic IO data, acknowledges
  alarms, reads and writes records and re-establishes the AR after a
  failure, one lifecycle per device; device access (supervisor) ARs and
  ReadImplicit for engineering tools
- An **IO-Device** (`device`): DCP responder, LLDP, the RPC server for
  Connect, Read, Write, Control and Release, the AR state machine (IOCAR
  and IOSAR, several ARs), cyclic input production and output consumption
  with watchdogs, process, diagnosis and plug alarms, diagnosis records,
  I&M0–4, RealIdentificationData, ModuleDiff, port data for the topology,
  port checks (CheckPeers) and adjustments (LLDP and DCP boundaries)
- **DCP** (`dcp`): Identify with filters and response delay, Get, Set
  (station name, IP suite, temporary or permanent), Signal, ResetToFactory,
  Hello (sent by the device, used by the controller to connect without
  Identify); requester and responder
- **LLDP** (`lldp`) with the PROFINET TLVs, an agent and a neighbor table
- **Connectionless DCE/RPC** (`rpc`) over any `net.PacketConn`: activities,
  fragmentation and reassembly, at-most-once execution, idempotent calls
- The **PNIO codecs** (`pnio`): every block of Connect, Control, Read,
  Write, alarms, diagnosis, identification, I&M, ModuleDiff and
  PDPortDataReal; NDR headers and PNIO status
- **RT cyclic data** (`rt`): frames, the C_SDU layout of IOCRs, provider,
  consumer with cycle counter and watchdog, a scheduler with jitter
  statistics; **alarms** (`alarm`): the RTA protocol with sequence numbers,
  acknowledgement, retransmission and window
- **GSDML** (`gsdml`): parsing, validation, encoding and conversion between
  GSDML files and device models, from a schema catalog by code generation
- **Layer 2 backends**: Linux AF_PACKET, macOS/BSD BPF, Windows Npcap
  (`l2/osport` picks the one of the platform) and an in-memory **virtual
  Ethernet** (`vnet`) with seeded faults, plus a small ARP/IPv4/UDP stack
  (`netstack`), so that a controller and devices run in one process without
  privileges
- **Verified against independent stacks**: rt-labs p-net, pnio-rs,
  profinet-py and pnio-dcp, with every capture decoded by scapy and
  Wireshark, in the containers of
  [otfabric/profinet-interop](https://github.com/otfabric/profinet-interop)
  (see [INTEROPERABILITY.md](INTEROPERABILITY.md))

A command-line tool on top of it is
[otfabric/profinetctl](https://github.com/otfabric/profinetctl).

> go-profinet is not certified by PROFIBUS & PROFINET International (PI)
> and claims no conformance. PROFINET has no security of its own: run it
> on isolated networks. See [SECURITY.md](SECURITY.md).

**Comparison:** [go-profinet vs other stacks](#go-profinet-vs-p-net-pnio-rs-profinet-py-and-the-pi-community-stack)

**Docs:** [API.md](API.md) (public API) · [ARCHITECTURE.md](ARCHITECTURE.md) (packages, goroutines, state machines) · [INTEROPERABILITY.md](INTEROPERABILITY.md) (implemented services, conformance and interop matrices) · [ERRORS.md](ERRORS.md) · [OBSERVABILITY.md](OBSERVABILITY.md) · [SECURITY.md](SECURITY.md) · [LIMITATIONS.md](LIMITATIONS.md) · [CONTRIBUTING.md](CONTRIBUTING.md) · [RELEASE.md](RELEASE.md) · guides in [docs/](docs/README.md) (controller, device, DCP, LLDP, RPC, cyclic data, diagnostics, GSDML, code generation, testing, coverage, benchmarks)

## go-profinet vs p-net, pnio-rs, profinet-py and the PI Community Stack

go-profinet is tested against three of these stacks (see
[INTEROPERABILITY.md](INTEROPERABILITY.md)). Each suits different needs;
the table shows where they differ, from their public sources, so that you
can pick the right one. The PI Community Stack is members-only; its column
is from PI's public pages.

| Capability | otfabric/go-profinet | rt-labs p-net | pnio-rs | profinet-py | PI Community Stack |
|---|:---:|:---:|:---:|:---:|:---:|
| Language | Go | C | Rust | Python | C |
| Licence | MIT | GPL-3.0-only or commercial | MIT OR Apache-2.0 | GPL-3.0-only or commercial | PNO Community Source Code License (members only; no combination with copyleft) |
| Status | pre-1.0 | maintained privately; public tree is an evaluation snapshot without ports (v1.0.2, 2025) | young, single author (v0.1.0, 2026) | active, one maintainer (0.6.6, 2026) | maintained by PI |
| IO-Controller | ✅ | — | — | ✅ minimal (cyclic marked experimental) | not documented publicly |
| IO-Device | ✅ | ✅ | ✅ | — | ✅ |
| DCP requester | ✅ Identify (all filters), Get, Set, Signal, Reset | — | — | ✅ | — |
| DCP responder | ✅ incl. filters, response delay, requester lock, Hello | ✅ incl. Hello | ◐ Identify by name or all; no Get, Set name, Signal or Reset | — | ✅ |
| LLDP | ✅ agent and neighbor table | ✅ | — | — (reads PDRealData only) | ✅ |
| RT_CLASS_1 / RT_CLASS_2 (unsynchronized) | ✅ / ✅ | ✅ / ✅ | ◐ RT_CLASS_2 FrameIDs only | ✅ / — | ✅ |
| IRT (RT_CLASS_3) | — | — | — | — | ✅ with a hardware driver |
| Alarms | ✅ both priorities, all types | ✅ | ◐ low priority, diagnosis only | ✅ receive and acknowledge | ✅ |
| Diagnosis records | ✅ serve and read | ✅ | — (invalid index) | ✅ read | ✅ |
| I&M | ✅ I&M0–4 | ✅ I&M0–4 (device only) | ◐ I&M0–3 | ✅ read I&M0–15, write I&M1–3 | ✅ |
| Several ARs on a device | ✅ IOCAR + IOSAR | ◐ 2 IOCARs | — single AR | n/a | ✅ |
| Device access AR (IOSAR) | ✅ both roles | — | — | ✅ | not documented |
| ModuleDiffBlock | ✅ both roles | ✅ (plugs expected modules) | — (mismatch rejects Connect) | ✅ read | ✅ |
| ReadImplicit, WriteMultiple | ✅ / ✅ | ✅ / ✅ | ✅ / ✅ | ✅ / ✅ | ✅ |
| PDPortDataReal / PDRealData (topology) | ✅ serve and decode | ✅ | — | ✅ read | ✅ |
| PDPortDataCheck (CheckPeers) / PDPortDataAdjust (boundaries) | ✅ device and controller | ◐ CheckPeers, PeerToPeerBoundary bit 0 | — | — | ✅ |
| GSDML | ✅ parse, validate (catalog), generate, model conversion | — (static file) | ◐ generate | ◐ parse for cyclic | — |
| Platforms | Linux, macOS/BSD, Windows (Npcap) | Linux, RTOS ports (not in the public tree) | Linux | Linux | Linux (Raspberry Pi demo) |
| Runs without privileges in tests | ✅ virtual Ethernet in process | — | — | — | — |
| Tested against other stacks in CI | ✅ p-net, pnio-rs, profinet-py, pnio-dcp; scapy and Wireshark decode every capture | not documented | golden frames from p-net and an S7-1500 | p-net (integration tests) | not documented |
| PI certification | — (not certified) | v1.0.2 release notes: "PROFINET Test Bundle 2.45.0.2 PASS" | — | — | basis for certified products |

✅ = available · ◐ = partly (the cell says what) · — = not available. For
p-net `ea5e554` (the last complete public tree; current release v1.0.2),
pnio-rs v0.1.0 and profinet-py 0.6.6, from their source and documentation
([docs/research/STACKS.md](docs/research/STACKS.md)) and the interop tests,
as of October 2026. If you spot an inaccuracy, please
[open an issue](https://github.com/otfabric/go-profinet/issues).

## Install

```sh
go get github.com/otfabric/go-profinet
```

Requires Go 1.23 or later.

## Project structure

| Package | Contents |
|---------|----------|
| `profinet` (root) | sentinel errors, logger interfaces |
| `controller` | IO-Controller, record client, supervisor AR |
| `device` | IO-Device |
| `model` | the device model: identity, DAP, slots, modules, submodules, records |
| `gsdml` | GSDML documents (generated types), validation, model conversion |
| `dcp` | DCP codec, requester (`Client`), responder |
| `lldp` | LLDP codec and agent |
| `rpc` | connectionless DCE/RPC client and server |
| `pnio` | PNIO blocks, NDR, status, identifiers |
| `rt` | cyclic frames, layout, provider, consumer, scheduler |
| `alarm` | RTA alarm transport |
| `ethernet` | Ethernet and VLAN frame codec, MAC addresses |
| `l2` | the `Port` interface and the frame demultiplexer `Mux` |
| `l2/afpacket`, `l2/bpf`, `l2/npcap`, `l2/osport` | the operating systems' raw Ethernet backends |
| `vnet` | virtual Ethernet switch with loss, duplication, corruption, reordering, delay and jitter |
| `netstack` | ARP, IPv4 and UDP over a `l2.Mux` |
| `pcap` | pcap reader and writer |
| `examples/` | quickstart, discovery, alarms, records, topology, chaos, GSDML generation and import, a device and a controller on a real interface ([outputs](examples/README.md)) |
| `e2e/` | end-to-end and chaos tests on vnet; real Layer 2 tests |
| `interop/` | the interop suite against profinet-interop (build tag `interop`) |

## Quickstart: controller and device in one process

```go
network := vnet.New(1)
dn, _ := vnetenv.NewNode(network, 10, "192.168.0.10") // an endpoint with an IP stack
dev, _ := device.New(device.Config{
	Model: model, Mux: dn.Mux, StationName: "io-1",
	IP: suite("192.168.0.10"), ListenPacket: dn.Stack.ListenPacket,
})
cn, _ := vnetenv.NewNode(network, 1, "192.168.0.1")
ctrl, _ := controller.New(controller.Config{Mux: cn.Mux, ListenPacket: cn.Stack.ListenPacket, StationName: "plc-1"})
io1, _ := ctrl.AddDevice(controller.DeviceConfig{StationName: "io-1", Model: model})
_ = io1.WaitState(ctx, controller.StateRunning)

dev.SetInput(1, 1, []byte("PROFINET"), pnio.IOxSGood)
io1.SetOutput(2, 1, []byte{1, 2, 3, 4}, pnio.IOxSGood)
in, iops, _ := io1.Input(1, 1)
```

The full program is [examples/quickstart](examples/quickstart/main.go) ([all examples with their output](examples/README.md)):
`go run ./examples/quickstart`. It needs no privileges.

## On a real network

```go
port, err := osport.Open("eth0", osport.Options{}) // AF_PACKET, BPF or Npcap
if err != nil {
	return err // needs CAP_NET_RAW, /dev/bpf* or Npcap
}
defer port.Close()
mux := l2.NewMux(port)
defer mux.Close()

ctrl, err := controller.New(controller.Config{Mux: mux, StationName: "plc-1"}) // the OS's UDP stack
...
dcpc := ctrl.DCP()
devices, err := dcpc.Identify(ctx, dcp.FilterAll(), dcp.IdentifyOptions{Wait: 2 * time.Second})
```

The expected configuration of a device usually comes from its GSDML file:

```go
doc, err := gsdml.Parse(f)
m, err := gsdml.ToModel(doc, "") // the first device access point
d, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "io-rack-1", Model: m})
```

and a device built with go-profinet describes itself with one:

```go
doc, err := gsdml.FromModel(m, gsdml.GenerateOptions{SupervisorAR: true})
b, err := gsdml.Marshal(doc)
os.WriteFile(gsdml.FileName(m, "", time.Now()), b, 0o644)
```

See the guides: [controller](docs/CONTROLLER.md), [device](docs/DEVICE.md),
[DCP](docs/DCP.md), [GSDML](docs/GSDML.md).

## Production features

- `context.Context` on every blocking call; bounded queues and
  concurrency everywhere; malformed network input is dropped and counted,
  never a panic (every decoder is fuzzed)
- One goroutine per device lifecycle on the controller; reconnect with
  backoff; watchdogs, data hold and CM activity timeouts as configured
- Typed errors that match the sentinels of the root package
  ([ERRORS.md](ERRORS.md)); PNIO and DCP status codes preserved
- Structured logging through a small `Logger` interface with `log/slog`
  and `log` adapters; counters on every component
  ([OBSERVABILITY.md](OBSERVABILITY.md))
- Raw Ethernet on Linux, macOS and Windows; cross-compiles for every
  `GOOS/GOARCH` of the release targets

## Development

```bash
make check        # generate-check, fmt, lint, vet, vuln, test (race), coverage ≥ 90 %, examples
make test-e2e     # end-to-end and chaos tests on vnet
make fuzz         # every fuzz target for FUZZTIME (30 s)
make bench        # codec and cyclic benchmarks
make l2-test      # real AF_PACKET tests on a veth pair in a privileged container (Docker)
make interop-images && make interop   # the interop suite (Docker), see INTEROPERABILITY.md
```

[docs/TESTING.md](docs/TESTING.md) describes the test strategy,
[docs/COVERAGE.md](docs/COVERAGE.md) the coverage per package and
[docs/BENCHMARKS.md](docs/BENCHMARKS.md) the measured performance.

## Limitations

The features of conformance class A as controller and device (RT_CLASS_1
and RT_CLASS_2 without synchronization); class B also requires SNMP, which
is missing. This is feature coverage, not conformance: go-profinet is not
certified for any class. No IRT, MRP, shared device, system redundancy, PROFIsafe or
PROFINET security classes. What can still be built without PI membership or
hardware, and in which order, is in
[docs/research/SOFTWARE-ONLY-ROADMAP.md](docs/research/SOFTWARE-ONLY-ROADMAP.md). OT Fabric is not a PI
member: what that costs (official XSD validation, GSDX, MRCD, signing,
certification) is listed in [docs/PI-MEMBERSHIP.md](docs/PI-MEMBERSHIP.md).
GSDML validation follows
an independent catalog, not PI's licensed schemas; GSDX and MRCD are not
implemented (their formats are not public). The full list is in
[LIMITATIONS.md](LIMITATIONS.md).

## License

MIT, see [LICENSE](LICENSE). The interop suite runs GPL-licensed reference
implementations in separate containers; none of their code is part of
go-profinet.
