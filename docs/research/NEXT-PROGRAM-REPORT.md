# Independent interoperability and production readiness: report

Status: 2026-10-28. go-profinet v0.4.0 (before: v0.3.0), profinet-interop
v0.3.0 (before: v0.2.1), profinetctl v0.3.0 (before: v0.2.0).

This program turned implemented but unverified capabilities into
independently tested ones, added what deployment needs (persistence,
re-parameterization, a faster transport), and recorded, with evidence, what
could not be done. The per-feature status is [FEATURE-STATUS.md](../FEATURE-STATUS.md).
Nothing here is a conformance claim or a real-time guarantee.

## What changed, milestone by milestone

### A. Independent interoperability

| Item | Outcome | Evidence |
|---|---|---|
| A1 MRP | **Done with umrp; microchip-ung/mrp blocked by the kernel.** profinet-interop's `umrp` adapter runs giometti/umrp (the MRP state machine in user space) on a Linux bridge, with a driver of ours (GPL-2.0, in the adapter only) that applies its port states through nftables, so it runs on any kernel. Rings of veth cables: go-profinet's MRM with two umrp MRCs, and go-profinet's MRC next to a umrp MRM: the ring closes with one port blocked, test frames pass foreign nodes, a cable break opens it, the clients flush their FDB on the manager's TopologyChange, it closes again; counted probe frames show no loop and no hole after every change, including seeded fault campaigns. Measured outages 0–8 ms with link change frames (measurements, not claims). | `TestMRPOtfManager`, `TestMRPOtfClient`, `TestMRPRingFaults`; matrix rows "umrp" |
| A1 blocker | microchip-ung/mrp needs the kernel's bridge MRP. Docker Desktop's kernel has none; the GitHub runners' (Ubuntu 6.17-azure) has `CONFIG_BRIDGE_MRP=y` and `CONFIG_NET_SWITCHDEV=y`, and with switchdev built in `br_mrp_add` returns `br_mrp_switchdev_add`'s `-EOPNOTSUPP` when no switchdev driver offloads the instance (net/bridge/br_mrp_switchdev.c, v6.17; traced with strace on the runner). The adapter reports `unsupported`; the tests are not applicable. A micro-VM with a kernel built with `BRIDGE_MRP=y` and `NET_SWITCHDEV` unset would run it. | profinet-interop docs/INTEROPERABILITY.md |
| A2 SNMP | **Done.** profinet-interop's `netsnmp` adapter (Net-SNMP's command-line manager) reads go-profinet's agent: Get, GetNext, GetBulk, full walks (GetNext, GetBulk, v1 and v2c agree), errors and exceptions, 128-sub-identifier names, GetBulk truncation, tooBig, a wrong community, 205 malformed datagrams counted in the agent's own counters, a flood bounded by the rate limit, and lldpRemTable while neighbors come and go. The agent's lldpRemIndex was unstable (the position in a sorted list); it is now assigned once per neighbor, and lldpRemTimeMark is a TimeFilter (RFC 2021). | `TestNetSNMP*`; `TestSNMPLLDPIndexStable` |
| A3 controller | **Researched; manual recipe.** The only software IO-Controller usable without membership or hardware is CODESYS Control for Linux SL in its demo mode, and only by hand (Windows engineering tool, runtime not released for containers or VMs, no redistribution right). PLCSIM Advanced does not simulate PROFINET IO; the others need hardware, payment or are not independent of profinet-py. Not run yet. | [INDEPENDENT-CONTROLLER.md](INDEPENDENT-CONTROLLER.md) |

### B. Production correctness

| Item | Outcome |
|---|---|
| Persistence | `store` (Memory; Dir: CRC-32C-checked files, atomic replace, previous version kept and used after a crash or corruption) and `device.Config.Store`: the permanent name and IP suite, DHCP options, I&M records and resets survive a restart; a temporary DCP Set clears the stored value (p-net's reading of the CMINA state table); the device saves before it applies and refuses the change when the store fails; `StoreError` reports an unusable state. Restart, failure and corruption tests on the virtual network. |
| LogBook | **Not completed: evidence conflicts.** Wireshark dissects the time stamps as 8-octet integers, p-net writes 12 octets (status, seconds, nanoseconds), and EntryDetail is stack-specific (p-net's own comment). The record stays empty. |
| SNMP table indexing | Repaired (A2). |
| Controller-side PrmBegin | `controller.Device.Reparameterize`: PrmBegin, writes, PrmEnd, the device's ApplicationReady; cyclic data goes on; the parameters are kept for reconnects; a PrmBegin refused while the device completes its startup is retried. profinet-py (an independent controller) re-parameterizes go-profinet's device; p-net (it accepts no DControl but PrmEnd) and pnio-rs refuse PrmBegin, recorded. |
| Restart and recovery tests | Restart with a store (e2e), and the reconnect paths exercised by the new tests. |

### C. Linux transport

`internal/cmd/l2bench` measures the AF_PACKET backend on a veth pair (flood,
ping-pong, a 1 ms cyclic exchange whose intervals are taken with kernel
receive timestamps on separate sockets). The backend is now allocation-free
on its read, write and link-state paths. Median of three alternating runs
(Docker Desktop, arm64):

| | before | after |
|---|---|---|
| flood frames per second | 80 738 | 927 041 |
| flood frames lost of 200 000 | 14 162 | 0 |
| CPU per frame | 3.67 µs | 2.17 µs |
| allocations per frame | 15.1 | 0 |
| 1 ms cyclic: allocations per second | 31 085 | 4 739 |
| 1 ms cyclic: garbage collections in 30 s | 35 | 1 |
| 1 ms cyclic: interval p50 / p99.9 | 1297 / 1751 µs | 1297 / 1705 µs |

The intervals did not change: in a virtual machine the hypervisor decides
them (the scheduler misses about a fifth of 1 ms deadlines either way).
`PACKET_QDISC_BYPASS` (no measurable effect on a veth), TPACKET_V3 rings (no
measured need) and a busy-wait scheduler (a core for a gain only on bare
metal) were measured or assessed and not adopted. Full table:
[BENCHMARKS.md](../BENCHMARKS.md).

### D. GSDML engineering compatibility

Two synthetic MIT fixtures (a modular device with a one-port and a two-port,
shared-device access point; a compact device) pass Check without an issue,
become the expected models, run as go-profinet devices and controllers (the
two-port DAP shared by two controllers), and are parsed by profinet-py,
which connects to a device built from the same file. profinet-py plugs only
FixedInSlots modules (it ignores UsedInSlots), a recorded difference in
interpretation. Engineering-tool imports (CODESYS, TIA Portal) have a
structured evidence file and procedure; none has been run. No validation
rule was added: nothing substantiates one yet. [GSDML-TOOLS.md](GSDML-TOOLS.md)

### E. Advanced features, gated by evidence

| Feature | Decision | Why |
|---|---|---|
| S2 interoperability and failover | stays an experiment | no open S2 implementation; CODESYS has S2 (with Redundancy SL), untried, manual only |
| MRP advanced roles (MRA, MIM/MIC) | not now; evidence available | umrp implements MRA, MIM and MIC: a peer exists for a next step |
| Shared input | no | what a non-owner receives is not public |
| RT_CLASS_UDP, MCR | codecs only | no open implementation of the behaviour |
| PTCP line delay | no | needs hardware timestamps for meaningful values; on veth it measures nothing |
| I&M5 | no | Wireshark (a list of I&M5Data blocks) and profinet-py (one 64-octet annotation) disagree |
| I&M6, I&M7 | no | layouts not public (GSDML 2.46+, members-only) |
| PROFINET 2.5, CC-E, SXP, security classes | no | members-only specifications; not attempted from guesses |

## Bugs found in released code

- `device`: a Connect's reservation lasted until its handler returned while
  its AR was already in the device's table, so the AR counted twice: a
  second IO controller connecting in that window was refused "out of AR
  resources" (seen with two S2 controllers starting together). Fixed with a
  test that fails before the fix.
- `l2/bridge`: a delivered frame was counted after it could be read, so
  statistics lagged (a flaky test).
- `snmp`: lldpRemIndex renumbered neighbors when one came or went.
- profinet-interop's scapy oracle counted MRP frames as decoded although
  scapy has no MRP dissector; they are now a recorded decoder limit, and
  the earlier "scapy decodes MRP" claims are withdrawn.

## Interoperability matrix

profinet-interop v0.3.0, regenerated from the full suite: 33 test runs, 193
pass, 0 fail, 8 not applicable (microchip-ung/mrp's kernel, and the
re-parameterization p-net and pnio-rs refuse), 8 attributed deviations
(profinet-py's padded I&M1, decoder limits of scapy and Wireshark). New
peers: Net-SNMP (29 pass), umrp (20 pass). The fixture tests added after the
release (profinet-py with the GSDML fixtures) pass in go-profinet's suite
and enter the matrix at its next regeneration.

## Unresolved constraints

Normative:

- IEC 61158-6-10 / 61784-2 and IEC 62439-2 were not read; behaviour follows
  Wireshark's dissectors and the reference stacks.
- PI's GSDML XSDs, GSDX, MRCD and test specifications are members-only.
- The LogBook time stamp encoding and I&M5 layout conflict in the public
  sources; I&M6/7, shared input, MCR address resolution, the S2 switchover
  and RDHT unit are not public.

Platform:

- Kernel MRP on a software bridge needs a kernel without switchdev; hosted
  runners and Docker Desktop do not qualify.
- In a virtual machine a 1 ms cycle misses deadlines (about 20 % in the
  measured VM); real-time behaviour needs bare metal, a real-time kernel and
  CPU isolation, which the library does not configure.
- No second independent IO-Controller can run in public CI; CODESYS needs
  a Windows engineering host and runs by hand.
- No engineering tool has imported the fixtures (Windows, licences).

## Roadmap, by evidence

1. **Run the CODESYS recipe** on a Linux host with a NIC and a Windows PC;
   record the GSDML import and the AR in the evidence files. This is the
   only path to a second independent controller and to S2 evidence.
2. **Done (2026-10-28): a micro-VM ring for microchip-ung/mrp**: the
   Kernel MRP workflow builds Linux 6.12 without switchdev and runs the
   MRP and MRA tests in one micro-VM boot each: all pass. The plan was: a kernel with `BRIDGE_MRP=y`
   and `NET_SWITCHDEV` unset, booted with KVM on an amd64 hosted runner
   (arm64 runners have no `/dev/kvm`), to add the kernel implementation as
   a second MRP peer. go-canbus already boots virtme-ng micro-VMs in CI
   (its Kernels workflow, `scripts/microvm-test.sh`), but with stock Ubuntu
   and kernel.ubuntu.com mainline kernels; whether those enable switchdev
   was checked only for the runner's Azure kernel, so expect to build the
   kernel (`vng --build` with a minimal config plus `BRIDGE_MRP`). The ring
   then runs in network namespaces inside the guest rather than in Docker
   containers.
3. **Done (2026-10-28): MRA** (`mrp.RoleAuto`) against umrp and the kernel.
4. **Engineering-tool imports** of the fixtures (CODESYS free, TIA Portal
   trial) into `evidence.json`.
5. Measurements of the transport on bare metal and with PREEMPT_RT; only
   then reconsider rings or a busy-wait scheduler.

## Release recommendations

- **profinet-interop v0.3.0**: released (new peers umrp, mrp, Net-SNMP;
  contract 1.1; scapy reports what it cannot dissect).
- **go-profinet v0.4.0**: new APIs (`store`, `Config.Store`,
  `Reparameterize`, `lldp.Neighbor.Index/Changed`), the allocation-free
  transport, fixes of released bugs, and one behaviour change to call out:
  `snmp.LLDPRemote.TimeMark` is now the time of the row's last change
  (walks list rows at filter 0). Release after the 25-run soak is green.
- **profinetctl v0.3.0**: require go-profinet v0.4.0; correct its
  limitations (it does have MRP roles and shared-device subsets); expose
  re-parameterization.
- Do not describe MRP as "verified against Linux kernel MRP", S2 as
  interoperable, or the GSDML fixtures as accepted by engineering tools:
  none of that has been shown.

## Pull requests

go-profinet: #22 (lldpRemIndex), #23 (MRP interop), #24 (Net-SNMP interop),
#25 (persistence), #26 (bridge counters), #27 (independent controller),
#28 (re-parameterization), #29 (AR reservation), #30 (transport), #31 (rpc
test), #32 (GSDML fixtures), and this documentation. profinet-interop: #7
(v0.3.0).
