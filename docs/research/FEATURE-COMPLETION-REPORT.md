# Software-only feature completion: report

Status: 2026-10-27, go-profinet v0.3.0 (before: v0.1.2), profinet-interop
v0.2.1, profinetctl v0.2.0. The plan, its evidence and its order are in
[SOFTWARE-ONLY-ROADMAP.md](SOFTWARE-ONLY-ROADMAP.md).

Every feature below was built without PI membership and without special
hardware, under the MIT licence with the Go standard library only; GPL
stacks run only in profinet-interop's containers. Nothing here is an
official conformance claim or a PI certification, and nothing claims
deterministic real-time behaviour: times are measured on the virtual
network (vnet) and reported as measurements.

**Kinds.** *Completed*: implemented, tested end to end and, where an open
implementation exists, against it. *Codec*: encoded and decoded (golden
vectors, fuzzing, tshark and scapy) where no open implementation of the
behaviour exists. *Experiment*: behaviour built from the public wire
elements, off by default, not interoperable. *Deferred*: not built.

Every completed item and experiment has unit tests with golden vectors,
fuzz targets for its decoders (25 in all), race-free tests, chaos-style
or flood tests where it takes network input, and keeps the coverage gate
(97.1 % of statements); the release commit was soaked: every CI job and
the interop suite 25 times in parallel.

| Feature | Kind | Before (v0.1.2) | After (v0.3.0) | Controller | Device | E2E (vnet) | Independent interop | Remaining limitations |
|---|---|---|---|---|---|---|---|---|
| DCP Hello / Fast Startup (1A) | Completed | none | Hello sequences (bounded, rate-limited across restarts, ended by a DCP Set), DeviceInitiative | connects through a Hello without Identify | sends Hello at start and on link up | `TestHello*` | p-net's Hellos drive our controller (`TestPnetHello`); tshark and scapy decode our Hellos (`TestOtfHelloDecoders`) | the rest of Fast Startup (stored AR parameters, link speed shortcuts) is not implemented |
| Port checks and adjustments, topology (1B) | Completed | PDRealData read only | PDPortDataCheck (CheckPeers), PDPortDataAdjust (P2P and DCP boundaries), PDInterfaceAdjust, PDInterfaceDataReal, PDExpectedData; remote mismatch diagnoses with alarms | `PeerChecks`, `CheckTopology` | evaluates the checks, applies the adjustments, refuses the rest (0xA9) | `TestPeerCheck`, `TestMultiportLine` | p-net decodes our record and reports the same diagnoses (`TestPnetPortCheck`) | MAU type, link state and line delay checks, domain/multicast boundaries: refused (not knowable or not public) |
| GSDML validation (1C) | Completed | structural validation | `gsdml.Check`: errors, warnings, unvalidated content, each with a rule and basis; unknown elements preserved | profinetctl `gsdml check` | — | generator round trips | N/A: PI's checker and XSDs are members-only | not PI's checker; GSDML 2.46/2.50 content is reported as unvalidated |
| Shared device (2) | Completed | one IOCAR | several IOCARs, one owner per submodule, locks in the ModuleDiff, owner-only data/alarms/writes, handover with Released / PrmEnd (plug) / ApplicationReady (plug) | subset models (`WithSlots`), takes released submodules | yes | `TestSharedDevice*`, `TestSecondControllerLocked` | p-net with two go-profinet controllers (`TestPnetSharedDevice`) | shared input: not public |
| Multiport devices (4A) | Completed | one port | `l2/bridge` (learning switch, per-port local ports, blocked/forwarding, DCP boundaries, bounded FDB); per-port MAC, LLDP, link state, port data | verifies topologies | `Config.Bridge` | `TestMultiportLine` (line, reach through a bridge, per-port neighbors, cable pull) | N/A: no open multiport PROFINET device; LLDP of each port decoded as before | software forwarding, not deterministic; ports must be promiscuous |
| MRP (4B) | Completed (no independent peer) | codec only | MRC and MRM (`mrp.New`) on a bridge: test frames, ring open/closed, blocked secondary, TopologyChange and FDB flush, link change frames; PROFINET MRP records 0x8050–0x8054 | `DeviceConfig.MRP` writes the records | `Config.MRP`, records, `MRPStatus` | `TestRing`, `TestMRPRing` (closed without loops, cable break opens the ring, ~20–35 ms on vnet with the 200 ms profile, closes again) | tshark and scapy decode our frames and every record field; no behavioural peer (microchip-ung/mrp needs CONFIG_BRIDGE_MRP and a ring of containers) | state machines and timers follow microchip-ung/mrp and the Linux bridge, not IEC 62439-2; 200/500 ms profiles only; no MRA, interconnection, MRPD, MRP diagnoses; PNIO timer fields not applied |
| System redundancy S2 (3) | Experiment | codec only | AR sets of two IOCARSRs: primary by DataStatus request, primary/backup acknowledge, outputs held for RDHT, abort reason 38, writes/alarms through the primary | `SystemRedundancy`, `SetPrimary`, `RedundancyAck` | `Config.SystemRedundancy` (off by default) | `TestSystemRedundancy*` (switchover ~5 ms, failover after the data hold, RDHT abort) | none exists (p-net and pnio-rs refuse IOCARSR; the Community Stack is members-only) | not interoperable; RDHT unit (1 ms) from p-net's comment; R1/R2, MTOT, backup re-parameterization, diagnoses not implemented |
| RT_CLASS_UDP (5) | Codec | none | RT payload on UDP 34962, the RTClass/LT/AlarmCR fields; Connect refused with DB 81 02 07 / DB 81 04 06 | refuses to build one | refuses it | refusal tested | tshark and scapy decode our UDP-RT frame and Connect (`TestUDPRTDecoders`) | no peer implements the data path: behaviour unverifiable |
| MCR (5) | Codec | none | `MCRBlockReq` | — | refused | — | tshark and scapy decode it (`TestCodecDecoders`) | address resolution procedure not public |
| PTCP (5) | Codec | none | delay request/response frames | — | — | — | tshark and scapy (`TestCodecDecoders`) | line delay not measured (as p-net); synchronisation deferred |
| DHCP blocks of DCP (5) | Completed | none | option 3 suboptions (61 client ID, 255 control, others raw), "set by DHCP" | `Client.SetDHCP`, identity fields | answers Get, Set through a handler (refused without) | DCP tests | tshark decodes 61 and 255 (`TestDCPDHCPDecoders`); scapy 2.6.1 has no DCP DHCP class (recorded deviation) | no DHCP client: addressing stays with the OS |
| SNMP for CC-B (5) | Completed | none | read-only SNMPv1/v2c agent: MIB-II system, interfaces (one row per port), snmp group, LLDP-MIB | — | `Config.SNMP` (opt-in) | `e2e/snmp_test.go` (GetNext and GetBulk walks agree, neighbors shown) | tshark and scapy decode all 178 datagrams of a walk (`TestSNMPDecoders`); no net-snmp client run | no v3/USM, no traps, no PNO LLDP extension MIB (text not public); class B also needs more than SNMP: no CC-B claim |
| DCP restricted modes, hardening (5) | Completed | none | read-only DCP, per-source rate limits in a fixed table, audit events | — | `Config.DCPPolicy` (opt-in) | `TestDCPReadOnly`, `TestDCPFlood` (22,000 spoofed frames, bounded responses, flat heap) | N/A (local policy) | not a PROFINET security class; source MACs can be forged |
| GSDX / MRCD (5) | Completed (inspector) | none | `gsdml/gsdx`: safe OPC reader (limits, hostile names), content types, relationships, core properties, signature metadata, GSDML parts, lossless round trip | profinetctl `gsdml gsdx` | — | — | N/A: PI's GSDX rules are members-only | signatures not verified; PI rules and MRCD not implemented |
| IRT, PTCP sync, isochronous mode, MRPD, TSN, PROFIsafe, security classes, PI signing, official XSD validation, MRCD issuance, certification | Deferred | — | — | — | — | — | — | need normative access, hardware or PI membership (docs/PI-MEMBERSHIP.md) |

## Bugs of earlier releases found on the way

Soaking every new feature (every CI job and the interop suite, 25 runs in
parallel, repeated until green) also found bugs in released code:

- `controller`: an AR's teardown read the consumer's statistics while
  holding the device lock, and a frame being handled at that moment holds
  the consumer's lock and waits for the device lock: a deadlock, present
  since v0.1.0.
- `device`: an AR that a Connect was replacing (a rerun) was counted twice
  against MaxARs, so another controller could be refused "out of ARs".
- `device` (shared device, v0.2.0): a submodule handed over to an AR that
  was completing its startup in the same instant was never announced; the
  new owner did not take it over.
- `gsdml` (v0.2.0): `Severity` marshalled to text but did not unmarshal, so
  a check report did not decode from its own JSON.

The new code had its own share before release (MRP loops when a cable came
back while link change frames were still repeating, stale test frames, a
data race in the S2 status), each found by the soak and fixed with a test.

## Verification

- `make check`: lint (0 issues), vet on four OSes, govulncheck, race tests,
  coverage 97.1 % (gate 90 %), examples.
- Soak: every CI job and the interop suite, 25 runs in parallel, green on
  the release commit.
- `make interop`: all tests pass against p-net, pnio-rs, profinet-py,
  pnio-dcp, scapy and tshark; the matrix is profinet-interop's
  docs/MATRIX.md.
