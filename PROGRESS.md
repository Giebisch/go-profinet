# PROFINET implementation progress

Durable state of the go-profinet / profinet-interop / profinetctl build.
Resume from here after an interruption.

## Repositories and releases

| Repository | Release | State |
|------------|---------|-------|
| go-profinet | v0.6.0 (2026-10-29) | main; CI, Interop, Kernel MRP, Soak (25×) and Release workflows |
| profinet-interop | v0.6.0 (contract 1.2) | main; nine adapters, contract, matrix |
| profinetctl | v0.6.0 (requires go-profinet v0.6.0); the three are released together with one version number | main |

## Decisions

- D1 Module root `profinet` is a leaf: sentinel errors and Logger. Typed
  errors (PNIO status, DCP block error, RPC fault) match the sentinels.
- D2 Transport split: `ethernet` · `l2` (Port + Mux with bounded queues) ·
  `vnet` (in-memory switch, seeded faults) · `netstack` (ARP/IPv4/UDP over
  l2) · `rpc` (CL DCE/RPC over any net.PacketConn) · OS backends
  `l2/afpacket`, `l2/bpf`, `l2/npcap`, `l2/osport`.
- D3 GSDML XSDs are PI members-only: generation from an independently
  written catalog (`gsdml/schema/catalog.json`), optional comparison with a
  licensed local XSD (`gengsdml -xsd`, 0 differences against v2.35).
  GSDX containers are inspected (`gsdml/gsdx`); MRCD is not public: not
  implemented.
- D4 Controller assigns the input CR's FrameID; the device the output CR's.
- D5 Interop facts adopted from the reference stacks: per-AR RPC activity
  (p-net), consumer watchdog armed at establishment, idempotent PNIO calls
  without ACK packets, first alarm DATA numbered 0xFFFF, DCP Get by named
  blocks, 3 DCP retries (p-net's 3 s requester lock).
- D6 profinetctl follows dnp3ctl: cobra, text/table/JSON (snake_case),
  typed errors with stable exit codes, `PROFINETCTL_*` environment. Tests run
  in process against go-profinet IO-Devices on vnet (a `Backend`
  abstraction), plus `make pnet-smoke` against p-net in Docker.

## Status

The supported features, how each is tested and the interop matrix are in
[docs/FEATURE-STATUS.md](docs/FEATURE-STATUS.md); what is out of scope in
[LIMITATIONS.md](LIMITATIONS.md).

| Area | State |
|------|-------|
| DCP, LLDP, RPC, PNIO blocks, alarms, RT cyclic, I&M, diagnosis | done, tested (unit, fuzz, e2e, chaos, interop) |
| IO-Device, IO-Controller, device access AR, shared device, re-parameterization | done, tested |
| Multiport device, MRP client, manager and automatic manager, SNMP agent, persistence | done, tested (interop: umrp, microchip-ung/mrp on the kernel, Net-SNMP) |
| System redundancy S2 | experimental (no independent peer) |
| GSDML parse/validate/encode/model conversion, codegen, synthetic fixtures | done |
| OS L2 backends | AF_PACKET tested on veth (`make l2-test`), allocation-free; BPF on a macOS feth pair (CI); Npcap compiled for Windows, not exercised on a real interface |
| profinet-interop matrix (v0.6.0) | 248 pass, 0 fail, 6 n/a, 14 attributed peer deviations |
| Verification (2026-10-29, v0.6.0) | make check (lint, vuln, race tests, 97.1 % coverage), e2e and chaos suites, 26 fuzz targets, l2-test, cross builds, Interop and Kernel MRP workflows, 25× soak of every job green |

## Blockers

- B0 PI membership postponed by the owner (2026-10-26); impact in
  docs/PI-MEMBERSHIP.md (XSDs, GSDX, MRCD, test specifications).
- B1 Kernel MRP (microchip-ung/mrp) needs a kernel without switchdev: not
  on Docker Desktop or the hosted runners; resolved by the Kernel MRP
  workflow (own Linux 6.12 in virtme-ng micro-VMs, one boot per test).
- B2 No second independent IO-Controller in CI: CODESYS only by hand
  (research/INDEPENDENT-CONTROLLER.md).

## Next actions

The evidence-based roadmap of docs/research/NEXT-PROGRAM-REPORT.md: run the
CODESYS recipe, engineering-tool imports of the GSDML fixtures,
measurements on bare metal (the micro-VM ring for kernel MRP and MRA are
done: Kernel MRP workflow, `mrp.RoleAuto`).
