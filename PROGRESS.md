# PROFINET implementation progress

Durable state of the go-profinet / profinet-interop / profinetctl build.
Resume from here after an interruption.

## Repositories and branches

| Repository | Branch | State |
|------------|--------|-------|
| go-profinet | `feat/core` | library, tests, interop suite, CI workflows; local commits, not pushed |
| profinet-interop | `feat/adapters` | six adapter images, contract, matrix; local commits, not pushed |
| profinetctl | `feat/cli` | CLI, tests, docs, CI; local commits, not pushed. go.mod replaces go-profinet with `../go-profinet` until go-profinet is tagged |

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
  GSDX/MRCD formats are not public: not implemented (blocker B1).
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

| Area | State |
|------|-------|
| DCP, LLDP, RPC, PNIO blocks, alarms, RT cyclic, I&M, diagnosis | done, tested (unit, fuzz, e2e, chaos) |
| PDPortDataReal / PDRealData (topology) | done; decoder verified against p-net |
| IO-Device, IO-Controller, supervisor (device access) AR, AR info | done, tested |
| GSDML parse/validate/encode/model conversion, codegen | done |
| OS L2 backends | AF_PACKET tested in Docker (`make l2-test`), BPF opened on macOS, Npcap cross-compiled |
| profinet-interop | p-net, pnio-rs, profinet-py, pnio-dcp, umrp, microchip-ung/mrp (n/a: kernel), Net-SNMP, scapy, tshark: 193 pass, 0 fail, 8 n/a, 8 attributed deviations (v0.3.0); CI, contract check, release and upstream-candidate workflows |
| go-profinet interop suite + `make interop` + Interop workflow | done; 10/10 tests pass |
| profinetctl | done: every command family, 93 % coverage, race-free, verified against p-net |
| Documentation | complete: reference documents in the root, guides in docs/ |
| Releases | go-profinet v0.1.0–v0.4.0 (v0.4.0, 2026-10-28: independent interoperability and production readiness, docs/research/NEXT-PROGRAM-REPORT.md; interop pinned to profinet-interop v0.3.0; soaked 25×), profinet-interop v0.1.0–v0.3.0, profinetctl v0.1.0–v0.3.0 |
| Verification (2026-10-26) | make check (lint, vuln, race tests, 97.8 % coverage), make test-e2e (38 passed incl. 21 chaos runs, chaos monkey, leak test), make fuzz (13 targets), make l2-test (AF_PACKET), make cross, interop 11/11, profinetctl make check-equivalents and pnet-smoke |

## Blockers

- B0 PI membership postponed by the owner (2026-10-26); impact in
  docs/PI-MEMBERSHIP.md (XSDs, GSDX, MRCD, test specifications).
- B1 Kernel MRP (microchip-ung/mrp) needs a kernel without switchdev: not
  on Docker Desktop or the hosted runners (NEXT-PROGRAM-REPORT.md).
- B2 No second independent IO-Controller in CI: CODESYS only by hand
  (research/INDEPENDENT-CONTROLLER.md).

## Next actions

The evidence-based roadmap of docs/research/NEXT-PROGRAM-REPORT.md: run the
CODESYS recipe, a micro-VM ring for kernel MRP, MRA against umrp,
engineering-tool imports of the GSDML fixtures, measurements on bare metal.
