# Releases

go-profinet follows semantic versioning from v1.0.0; before that, minor
versions may change the API. Releases are tagged by the Release workflow
(`.github/workflows/release.yml`).

The notes of a version are the body of its GitHub release: the Release
workflow (the shared `go-package-release.yml`) takes everything after the
heading `## vX.Y.Z` up to the next line that is exactly `---`. So: one
section per version, newest first, the heading exactly `## vX.Y.Z`, each
section ending with a `---` line and no `---` line inside a section.

## Release checklist

1. `make check` and `make test-e2e` pass; the Interop workflow is green; a
   Soak run of the release commit (25 runs) is entirely green.
2. `INTEROP_REF` (Makefile and `.github/workflows/interop.yml`) names the
   profinet-interop release the matrix in INTEROPERABILITY.md came from.
3. This file lists the changes under the new version, in a `## vX.Y.Z`
   section ending with `---` (`make release-notes-check`); API changes are
   called out.
4. Run the Release workflow with the version bump.
5. Update profinetctl: `go get github.com/otfabric/go-profinet@<tag>`.

---

## v0.5.0

**Date:** 2026-10-28
**Previous release:** v0.4.0

The MRP automatic manager (MRA), and MRP verified against the Linux
kernel's bridge MRP: microchip-ung/mrp runs as a peer in micro-VMs booted
with a kernel built without switchdev. Feature coverage is not
conformance: go-profinet is not certified, and no measured time is a
real-time claim.

No incompatible API change (apidiff against v0.4.0: additions only).

- `mrp.RoleAuto` (MRA): starts as a manager; the better of two managers
  (lower priority, then lower address: `mrp.Better`) answers the other's
  tests with MRP_TestMgrNAck, the other gives way (MRP_TestPropagate) and
  monitors the better managers' tests, managing again after TSTNRmax
  intervals without one. `Status.Acting`, `Manager`, `ManagerPrio`;
  `ManagerOption`, `NewManagerOption` (Siemens OUI, which Wireshark
  decodes; any OUI read). `device.MRPConfig.Role` may be `mrp.RoleAuto`
  (configuration only: PROFINET's MRP_Role has no value for it);
  PDInterfaceMrpDataReal reports the acting role.
- `mrp.Decode` accepts the Linux bridge's MRA test frames, whose last
  MRP_Option claims five octets more than the frame has.
- Verified against umrp and microchip-ung/mrp (kernel): manager, client,
  seeded cable faults, MRA with go-profinet and with the peer the best
  (failure and return of the manager): no loop or hole after any change;
  measured outages 10–20 ms on the kernel ring, 200 ms profile. umrp's MRA
  as a client keeps reclaiming the manager role (its monitor ignores the
  manager it gave way to): recorded as its deviation.
- Kernel MRP workflow: `scripts/mrp-kernel.sh` builds Linux 6.12 with
  `BRIDGE_MRP` and without `NET_SWITCHDEV`; `scripts/microvm-interop.sh`
  runs each interop test in its own virtme-ng micro-VM with a Docker daemon
  inside (amd64, KVM). Interop harness: results written before teardown,
  ring nodes stopped before removal, bounded removal.
- Interop: profinet-interop v0.4.0, contract 1.2 (248 pass, 0 fail, 6 n/a,
  14 attributed deviations).

---

## v0.4.0

**Date:** 2026-10-28
**Previous release:** v0.3.0

Independent interoperability and production readiness: MRP and the SNMP
agent tested against independent implementations, persistence,
re-parameterization of a running AR, an allocation-free Linux transport, and
synthetic GSDML fixtures. What each feature covers, how it is tested and
what limits it is in [docs/FEATURE-STATUS.md](docs/FEATURE-STATUS.md); the
program's outcome, blockers and roadmap in
[docs/research/NEXT-PROGRAM-REPORT.md](docs/research/NEXT-PROGRAM-REPORT.md).
Feature coverage is not conformance: go-profinet is not certified, and no
measured time is a real-time claim.

No incompatible API change (apidiff against v0.3.0). One behaviour change:
`snmp.LLDPRemote.TimeMark` is the time of a row's last change; walks list
every row at TimeFilter 0 and a Get answers any filter value up to it
(before, a row appeared at its TimeMark only).

- Persistence: package `store` (`Store`; `Memory`; `Dir`: CRC-32C-checked
  files replaced atomically, the previous version kept and used after a
  crash or corruption) and `device.Config.Store`/`StoreKey`: the permanent
  station name and IP suite, DHCP options, I&M1–4 and resets survive a
  restart; a temporary DCP Set clears the stored value; a failing store
  refuses the change; `Device.StoreError`.
- `controller.Device.Reparameterize`: PrmBegin, writes, PrmEnd and the
  device's ApplicationReady on a running AR; the parameters are kept for
  reconnects. Verified with profinet-py re-parameterizing go-profinet's
  device; p-net and pnio-rs refuse PrmBegin in Data.
- SNMP: lldpRemIndex is kept per neighbor (`lldp.Neighbor.Index`,
  `Changed`); lldpRemTimeMark is a TimeFilter. Verified with Net-SNMP:
  walks, errors, limits, malformed datagrams, a flood, changing neighbors.
- MRP verified against umrp as manager and as client (profinet-interop
  v0.3.0); microchip-ung/mrp could not run (the kernels available refuse
  MRP on a software bridge); the interop suite builds MRP rings of veth
  cables (`otfnode mrp`).
- `l2/afpacket`: reads, writes and `LinkUp` allocate nothing; flood
  throughput ×11, losses gone, 35 → 1 garbage collection per 30 s of a 1 ms
  exchange (docs/BENCHMARKS.md, `scripts/l2-bench.sh`); the cyclic intervals
  in a VM are unchanged.
- GSDML: synthetic MIT fixtures (`gsdml/testdata/fixtures`: two device
  access points with one and two ports, shared device, a compact device),
  run as devices and controllers and parsed by profinet-py; an evidence file
  for engineering-tool imports (none run yet).
- Fixes: `device`: a Connect counted twice against MaxARs while its AR was
  being established, so a second IO controller connecting at that moment was
  refused "out of AR resources"; `l2/bridge`: statistics counted a frame
  after it could be read.
- Docs: `docs/research/INDEPENDENT-CONTROLLER.md` (CODESYS manual recipe),
  `docs/research/GSDML-TOOLS.md`; a correction of v0.3.0's report: scapy has
  no MRP dissector.
- Interop: profinet-interop v0.3.0 (193 pass, 0 fail, 8 n/a, 8 attributed
  deviations).

---

## v0.3.0

**Date:** 2026-10-27
**Previous release:** v0.2.0

The rest of the software-only roadmap
([docs/research/SOFTWARE-ONLY-ROADMAP.md](docs/research/SOFTWARE-ONLY-ROADMAP.md)):
multiport devices, MRP, system redundancy as an experiment, SNMP, DCP
hardening, DHCP blocks, RT_CLASS_UDP codecs and a GSDX inspector. What each
feature covers, how it was verified and what it does not do is in
[docs/research/FEATURE-COMPLETION-REPORT.md](docs/research/FEATURE-COMPLETION-REPORT.md).
Feature coverage is not conformance: go-profinet is not certified, and no
time measured on the virtual network is a real-time claim.

One incompatible API change (apidiff against v0.2.0): `dcp.ResponderOptions`
is no longer comparable with `==`, since its `Policy` holds an audit
callback. Behaviour changes: a
Connect that has both a bad LT and a bad RT class is refused naming the RT
class (IOCRProperties) instead of the LT; an AlarmCR over UDP is refused
naming its properties.

- Multiport devices (roadmap 4A): `l2/bridge`, the learning switch of a
  multiport device (CPU port, per-port local ports for LLDP and MRP,
  forwarding/blocked states, DCP boundaries, a bounded FDB), and
  `device.Config.Bridge`: each port has its own MAC address, LLDP agent,
  link state, port data and peer check.
- MRP (roadmap 4B): `mrp.New` runs a ring client or manager on a bridge
  (200 ms and 500 ms profiles); the PROFINET MRP records 0x8050–0x8054
  (`pnio`), `device.Config.MRP`, `Device.MRPStatus`,
  `controller.DeviceConfig.MRP`.
- System redundancy S2 (roadmap 3): an experiment, off by default and not
  interoperable: `device.Config.SystemRedundancy`,
  `controller.DeviceConfig.SystemRedundancy`, `Device.SetPrimary`,
  `Device.RedundancyAck`; abort reasons 38–43 and the SRInfoBlock error
  code in `pnio`.
- SNMP (roadmap 5, for CC-B features): `snmp`, a read-only SNMPv1/v2c agent
  with MIB-II and the LLDP-MIB; `device.Config.SNMP` (opt-in).
- DCP: DHCP blocks of option 3 (`dcp.DHCPParameter`, `Identity.DHCP`,
  `device.Config.DHCP`, `SetDHCP`); an opt-in policy (`dcp.Policy`,
  `device.Config.DCPPolicy`): read-only DCP, per-source rate limits, audit
  events. Never called a security class.
- RT_CLASS_UDP: codecs (`rt.AppendUDPPayload`, `rt.DecodeUDP`, the UDP
  fields of `pnio`); the device and the controller refuse it.
- GSDX: `gsdml/gsdx`, a safe inspector of the OPC container (no PI rules,
  signatures not verified); `gsdml.Severity` decodes from its JSON.
- Fixes, found by soaking: `controller`: no deadlock between an AR's
  teardown and a frame being handled (since v0.1.0); `device`: an AR being
  replaced (a rerun) counts once against MaxARs; a shared-device handover to
  an AR completing its startup is announced once it runs (v0.2.0);
  `gsdml.Severity` decodes from its JSON (v0.2.0).
- Interop: pinned to profinet-interop v0.2.1; new decoder checks for SNMP,
  UDP-RT, the DCP DHCP blocks and the MRP records.
- 25 fuzz targets (was 17); coverage 97.1 %.

---

## v0.2.0

**Date:** 2026-10-27
**Previous release:** v0.1.2

The first features of the software-only roadmap
([docs/research/SOFTWARE-ONLY-ROADMAP.md](docs/research/SOFTWARE-ONLY-ROADMAP.md)):
DCP Hello, port checks and adjustments, GSDML validation, shared device,
and codecs of MRP, PTCP and system redundancy. No incompatible API change
(apidiff against v0.1.2); two behaviour changes: the IO-Device now
accepts the IOCARs of several IO controllers (shared device) instead of
refusing a second one, and generated GSDML no longer declares SNMP.
Feature coverage is not conformance: go-profinet is not certified.

- DCP Hello (roadmap 1A): `dcp.HelloFrame`, `Responder.StartHello` and
  `StopHello` (bounded sequences, rate-limited across restarts, ended by a
  DCP Set); the IO-Device sends Hello at start and on link up while no IO
  controller is connected (`device.Config.Hello`, `HelloOptions`) and
  reports DeviceInitiative; the IO-Controller connects through a Hello
  without Identify (`controller.Config.IgnoreHello`, `DeviceStats.Hellos`).
  `model.Capabilities.DCPHello` and GSDML `DCP_HelloSupported`.
- Port checks and adjustments (roadmap 1B): `pnio` codecs of
  PDPortDataCheck, PDPortDataAdjust, PDInterfaceAdjust, PDInterfaceDataReal
  and the PDRealData record with the interface (`DecodePDRealDataRecord`);
  the IO-Device evaluates CheckPeers against its LLDP neighbor (remote
  mismatch diagnoses with alarms), applies PeerToPeerBoundary,
  DCPBoundary and PDInterfaceAdjust, serves PDExpectedData and
  PDInterfaceDataReal, reports the real link state, and refuses the
  checks and adjustments it cannot honour; the IO-Controller writes
  `DeviceConfig.PeerChecks` and verifies a topology (`CheckTopology`).
  The LLDP agent reports changes (`AgentOptions.OnChange`), can stop
  sending (`SetTransmit`) and forget its neighbors (`Clear`).
- Shared device (roadmap 2): the IO-Device accepts IOCARs of several IO
  controllers; each submodule belongs to the first AR that expects it,
  the others see it locked (ModuleDiff ARInfo 3), get no data of it,
  cannot write it; alarms go to the owner; when the owner leaves, the
  submodule is handed over with a Released alarm, PrmEnd (plug) and
  ApplicationReady (plug). The IO-Controller leaves out locked submodules,
  takes over released ones, and `model.Device.WithSlots` builds the
  model of a controller that owns only some modules. A closed AR now
  invalidates only its own outputs.
- Codecs of features without an open implementation to test behaviour
  against (roadmap 3, 4B, 5): `mrp` (MRP frames), `ptcp` (PTCP delay
  frames), `pnio.SRInfoBlock`, `pnio.ARSetID`, `pnio.MCRBlockReq`, the
  IOCARSR DataStatus codes in `rt`; golden vectors, fuzz targets, tshark
  and scapy decode them without a warning.
- Generated GSDML no longer declares SNMP in `SupportedProtocols`: the
  device has no SNMP agent.
- `docs/research/SOFTWARE-ONLY-ROADMAP.md` and its evidence files.
- GSDML validation (roadmap 1C): `gsdml.Check` grades every issue as
  error, warning or unvalidated and names its rule; semantic rules for IO
  data that does not fit an RT frame (error), slots against PhysicalSlots
  and AllowedInSlots, record indexes, module ident numbers, interface and
  port subslots and reduction ratios (warnings, each with its basis);
  elements and attributes outside the catalog and newer schema versions
  are reported as unvalidated. `Validate` still fails on errors only.
- `controller`: `Device.WaitState` returns only after the `OnState`
  callback of the state it waited for has run, as documented; before, it
  could return in between.
- `device`: a controller that connects anew before the device saw its old
  AR end replaces that AR, which hands its submodules to the new one
  (before, they were locked until the old AR timed out, or went to
  another controller); `SendAlarm` no longer reads the alarm endpoint of
  an AR that is still being set up (data race).
- Tests: a failed e2e test prints the logs of its devices and
  controllers; tests that are not about the watchdog ride out CI runner
  stalls of seconds. Soaked: every CI job 25 times in parallel, green.
- Interop: pinned to profinet-interop v0.1.3, whose matrix covers the new
  features (p-net 55 checks pass, 0 fail).

---

## v0.1.2

**Date:** 2026-10-26
**Previous release:** v0.1.1

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

---

## v0.1.1

**Date:** 2026-10-26
**Previous release:** v0.1.0

No code change.

- The interop suite (`make interop-images`, the Interop workflow) builds the
  reference images from profinet-interop v0.1.0 instead of its `main`
  branch; the Interop workflow's `interop_ref` input still overrides it.
- Documentation names the released versions (SECURITY.md, this file) and
  links profinet-interop's matrix at v0.1.0.

---

## v0.1.0

**Date:** 2026-10-26

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

---
