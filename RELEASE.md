# Releases

go-profinet follows semantic versioning from v1.0.0; before that, minor
versions may change the API. Releases are tagged by the Release workflow
(`.github/workflows/release.yml`).

## Unreleased

- DCP Hello (roadmap 1A): `dcp.HelloFrame`, `Responder.StartHello` and
  `StopHello` (bounded sequences, rate-limited across restarts, ended by a
  DCP Set); the IO-Device sends Hello at start and on link up while no IO
  controller is connected (`device.Config.Hello`, `HelloOptions`) and
  reports DeviceInitiative; the IO-Controller connects through a Hello
  without Identify (`controller.Config.IgnoreHello`, `DeviceStats.Hellos`).
  `model.Capabilities.DCPHello` and GSDML `DCP_HelloSupported`.
- Generated GSDML no longer declares SNMP in `SupportedProtocols`: the
  device has no SNMP agent.
- `docs/research/SOFTWARE-ONLY-ROADMAP.md` and its evidence files.

## v0.1.2 (2026-10-26)

Fixes found by soaking every CI job and the interop suite 25 times in
parallel. No API change.

- `device`: `Handler.StateChanged` reports the transitions of an AR in
  order. A transition caused while the previous one was being reported
  (PrmBegin right after Data) could be reported first.
- `rpc`: the client no longer drops response fragments when they arrive
  faster than the call takes them; a large fragmented response could time
  out on a loaded host because every retransmission lost the same
  fragments.
- `l2/bpf`: a closed port returns `ErrClosed` instead of frames left over
  from its last read.
- Tests: mux tests wait until a frame is fully handled; the real Layer 2
  test tolerates scheduler stalls of a CI runner; the AF_PACKET close test
  reads past frames the kernel sends.
- Interop: pinned to profinet-interop v0.1.1 (tshark decodes PNIO RPC on
  any ephemeral port); otfnode asks for a 32-cycle data hold; a failing
  test logs otfnode's report and keeps its captures.
- CI: the Soak workflow (every job N times in parallel, own chaos seeds);
  Intel macOS on `macos-15-intel` (GitHub retired `macos-13`).

## v0.1.1 (2026-10-26)

No code change.

- The interop suite (`make interop-images`, the Interop workflow) builds the
  reference images from profinet-interop v0.1.0 instead of its `main`
  branch; the Interop workflow's `interop_ref` input still overrides it.
- Documentation names the released versions (SECURITY.md, this file) and
  links profinet-interop's matrix at v0.1.0.

## v0.1.0 (2026-10-26)

First version.

- IO-Controller (`controller`): DCP resolution and IP assignment, legacy
  (RT_CLASS_1) and advanced (RT_CLASS_2) startup, parameterization, cyclic
  data, alarms, records, reconnect with backoff; `RecordClient` with
  ReadImplicit; device access ARs (`OpenSupervisor`); `Device.AR`.
- IO-Device (`device`): DCP responder, LLDP, Connect/Read/Write/Control/
  Release, IOCAR and IOSAR, several ARs, ModuleDiff, cyclic data with
  watchdogs, alarms and diagnosis, I&M0–4, RealIdentificationData,
  PDPortDataReal and PDRealData.
- DCP, LLDP, CL DCE/RPC, PNIO blocks, RT and RTA codecs and machines.
- GSDML: generated types from an independent catalog, parse, validate,
  encode, model conversion.
- Raw Ethernet backends for Linux, macOS/BSD and Windows; virtual Ethernet
  with fault injection; ARP/IPv4/UDP stack for it.
- Verified against p-net, pnio-rs, profinet-py and pnio-dcp
  ([INTEROPERABILITY.md](INTEROPERABILITY.md)).

## Release checklist

1. `make check` and `make test-e2e` pass; the Interop workflow is green; a
   Soak run of the release commit (25 runs) is entirely green.
2. `INTEROP_REF` (Makefile and `.github/workflows/interop.yml`) names the
   profinet-interop release the matrix in INTEROPERABILITY.md came from.
3. This file lists the changes under the new version; API changes are
   called out.
4. Run the Release workflow with the version bump.
5. Update profinetctl: `go get github.com/otfabric/go-profinet@<tag>`.
