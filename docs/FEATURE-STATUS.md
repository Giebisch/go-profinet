# Support, feature and interop matrix

What go-profinet supports, on what, how each feature is tested, and against
what. Status: go-profinet v0.6.0, profinet-interop v0.6.0 and profinetctl
v0.6.0, released together on 2026-10-29. How go-profinet compares with other PROFINET stacks is
in the [README](../README.md#go-profinet-vs-p-net-pnio-rs-profinet-py-and-the-pi-community-stack);
what is out of scope in [LIMITATIONS.md](../LIMITATIONS.md).

None of this is a conformance claim: no PI test has been run, nothing is
certified, and no measured time is a real-time guarantee
([PI-MEMBERSHIP.md](PI-MEMBERSHIP.md)).

## 1. Versions

| go-profinet | Go | profinet-interop (contract) | profinetctl | Supported |
|---|---|---|---|---|
| **v0.6.0** | 1.23 or later (CI: 1.23–1.26) | v0.6.0 (1.2) | v0.6.0 | **yes** |
| v0.5.1 | 1.23 or later | v0.4.1 (1.2) | v0.3.2 | no: v0.6.0 only adds API (apidiff: compatible) |
| v0.5.0 | 1.23 or later | v0.4.0 (1.2) | v0.3.1 | no |
| v0.4.0 | 1.23 or later | v0.3.0 (1.1) | v0.3.0 | no |
| < v0.4.0 | 1.23 or later | ≤ v0.2.1 | ≤ v0.2.x | no |

From v0.6.0 the three repositories are released together with the same
version number: profinetctl v0.6.0 is built on go-profinet v0.6.0, whose
interop suite runs profinet-interop v0.6.0. A patch release of one of them
does not move the others.

Security fixes go into the latest minor version only ([SECURITY.md](../SECURITY.md)).
go-profinet is pre-1.0: a minor version may change the API, and its notes
say so (apidiff against the previous release, [RELEASE.md](../RELEASE.md)).

## 2. Platforms

| Platform | Raw Ethernet backend | Tested in CI | Notes |
|---|---|---|---|
| Linux amd64, arm64 | AF_PACKET (`l2/afpacket`), allocation-free | the full test suite with the race detector; real veth pairs (`make l2-test`); Interop; Kernel MRP in micro-VMs | `CAP_NET_RAW` (and `CAP_NET_ADMIN` to set an IP) |
| macOS amd64, arm64 | BPF (`l2/bpf`) | the full test suite; a real feth pair | root or access to `/dev/bpf*` |
| FreeBSD and other BSDs | BPF (`l2/bpf`) | cross-compiled only | |
| Windows amd64, arm64 | Npcap (`l2/npcap`) | the full test suite (in-memory network) | not run against Npcap (the hosted runners have none) |
| any (tests, simulation) | in-memory network (`vnet`) | everything | no privileges, seeded faults |

Every library package is pure Go with the standard library only.

## 3. Feature support

✅ supported · ◐ partly (see the notes) · 🧪 experimental (off by default,
not interoperable) · ⚙ codec only (encode and decode, no behaviour) · —
not implemented · n/a not applicable. **E2E**: a go-profinet controller
and device (or several devices) exercise it on the in-memory network, with
seeded faults where it takes network input. **Independent**: software that
is not go-profinet exchanged it with go-profinet in profinet-interop's
matrix (section 5).

| Feature | Device | Controller | profinetctl | E2E | Independent |
|---|:---:|:---:|:---:|:---:|---|
| DCP Identify, Get, Set (name, IP; temporary, permanent), Signal, ResetToFactory | ✅ responder | ✅ requester | ✅ | ✅ | p-net, pnio-rs, profinet-py, pnio-dcp |
| DCP Hello, DeviceInitiative | ✅ | ✅ | ✅ `hello listen` | ✅ | p-net |
| DCP DHCP blocks | ◐ codec; Set through a handler | ◐ codec | — | ✅ | decoders only |
| DCP read-only mode, rate limits, audit | ✅ | n/a | n/a | ✅ | n/a (local policy) |
| LLDP with the PNO TLVs, neighbor table | ✅ | ✅ reads PDRealData | ✅ `lldp neighbors`, `topology` | ✅ | p-net; Net-SNMP (LLDP-MIB) |
| Connect: IOCARSingle, legacy (RT_CLASS_1) and advanced (RT_CLASS_2) startup | ✅ | ✅ | ✅ `connect`, `ar` | ✅ | p-net, pnio-rs, profinet-py |
| Device access AR (IOSAR) | ✅ | ✅ | ✅ record and I&M commands | ✅ | profinet-py |
| Records: user, I&M0–4, WriteMultiple, ReadImplicit, ModuleDiff, identification, diagnosis | ✅ | ✅ | ✅ | ✅ | p-net, pnio-rs, profinet-py |
| Alarms: both priorities, process and diagnosis, acknowledgement | ✅ | ✅ | ✅ `alarms` | ✅ chaos | p-net, profinet-py |
| Cyclic data RT_CLASS_1/2, IOPS/IOCS, DataStatus, watchdog | ✅ | ✅ | ✅ `io` | ✅ | p-net (1 ms), pnio-rs, profinet-py |
| Shared device (several IOCARs, one owner per submodule) | ✅ | ✅ | ◐ `connect --slot` | ✅ | p-net |
| Re-parameterization in Data (PrmBegin … PrmEnd) | ✅ | ✅ | ✅ `connect --reparam` | ✅ | profinet-py (p-net, pnio-rs refuse PrmBegin) |
| Port checks (CheckPeers), boundaries, topology check | ✅ | ✅ | ✅ `--peer-check`, `topology check` | ✅ | p-net |
| Multiport device (software bridge) | ✅ | n/a | n/a | ✅ | umrp, microchip-ung/mrp forward through it |
| MRP client and manager, PROFINET MRP records | ✅ | ✅ writes the role | ✅ `--mrp-role`, `mrp status` | ✅ | umrp; microchip-ung/mrp (kernel) |
| MRP automatic manager (MRA) | ✅ configuration only | n/a | ◐ `mrp status` shows the acting role | ✅ | umrp; microchip-ung/mrp (kernel) |
| SNMP agent v1/v2c (MIB-II, LLDP-MIB) | ✅ | n/a | — | ✅ | Net-SNMP |
| Persistence of name, IP, DHCP options, I&M | ✅ | n/a | n/a | ✅ restart, corruption | n/a (local) |
| GSDML: parse, validate, check, model, generate, GSDX inspection | ✅ library | ✅ library | ✅ `gsdml` | ✅ fixtures | pnio-rs's GSDML; profinet-py parses go-profinet's |
| System redundancy S2 | 🧪 | 🧪 | — | ✅ | none exists |
| RT_CLASS_UDP, MCR, PTCP delay frames | ⚙ | ⚙ | — | refusal | decoders only |
| Observability: `profinet.Metrics` callbacks, slog structured logging, per-component counters | ✅ | ✅ | ◐ `--debug` logs, `io statistics` | ✅ | n/a (local) |
| Reconnection with exponential backoff and jitter; DCP, RPC and alarm retransmission | n/a (RPC response cache) | ✅ | ✅ (library defaults) | ✅ chaos | n/a (local behaviour) |
| I&M5–I&M7, LogBook entries | — | — | — | — | — |
| IRT, PTCP synchronisation, isochronous mode, MRPD, MRP interconnection, TSN, PROFIsafe, security classes, SXP, SNMPv3 | — | — | — | — | — |

## 4. API and engineering

How the library is built, the same way as the other OT Fabric Go
libraries (go-modbus, go-ethernetip, go-canbus, …):

| Area | go-profinet | Where |
|---|---|---|
| Dependencies | the standard library only; no cgo | `go.mod` |
| Errors | eleven sentinels in the root package (`ErrTimeout`, `ErrNotFound`, `ErrRejected`, …); typed errors that carry the protocol's codes (`*pnio.StatusError`, `*dcp.SetError`, `*rpc.Fault`, `*gsdml.ValidationError`) and match the sentinels with `errors.Is` | [ERRORS.md](../ERRORS.md) |
| Logging | `profinet.Logger`, `FieldLogger` (structured), `ContextLogger`; adapters for `log/slog` and `log`; silent by default, never per cyclic frame | [OBSERVABILITY.md](../OBSERVABILITY.md) |
| Metrics | `profinet.Metrics` callbacks on controllers and devices (state changes, connects, aborts, records with durations, alarms, retries) plus pull counters on every component | [OBSERVABILITY.md](../OBSERVABILITY.md#metrics), [examples/observability](../examples/observability/main.go) |
| Retries and backoff | DCP retransmission, RPC retransmission with at-most-once execution, RTA retransmission, the controller's reconnection with `profinet.Backoff` (exponential, jitter); record calls are not retried behind the application's back | [ERRORS.md](../ERRORS.md#retries-and-backoff) |
| Context and concurrency | every blocking call takes a `context.Context`; components are safe for concurrent use; `Close` waits for their goroutines | [API.md](../API.md#conventions) |
| Types | typed identifiers and enums (`pnio.Index`, `pnio.RTClass`, `controller.State`, `device.ARState`, `mrp.Role`, …) with `String` methods; codecs with `Append`/`Decode` pairs | [API.md](../API.md) |
| Tests | 97.1 % statement coverage; race detector on every package; 26 fuzz targets; seeded chaos and chaos-monkey suites; a 25-run soak before every release; benchmarks | [TESTING.md](TESTING.md), [COVERAGE.md](COVERAGE.md) |
| Interop | independent stacks in containers, pinned by version and digest; every capture decoded by tshark and scapy | section 5, [INTEROPERABILITY.md](../INTEROPERABILITY.md) |
| Examples | 12 self-contained examples that CI runs, with their real output, and a device and a controller for a real interface | [examples/](../examples/README.md) |

## 5. Interop matrix

Each peer runs in a container of
[profinet-interop](https://github.com/otfabric/profinet-interop) v0.6.0
against go-profinet; every exchange is captured and decoded by tshark and
scapy. Full evidence, check by check:
[MATRIX.md](https://github.com/otfabric/profinet-interop/blob/v0.6.0/docs/MATRIX.md).

| Peer | go-profinet as | DCP | LLDP | AR, startup | Records, I&M | Alarms | Cyclic | Shared device | Port checks | Re-param. | MRP | MRA | SNMP | pass | fail | n/a | dev. |
|---|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|---:|---:|---:|---:|
| rt-labs p-net `ea5e554` (device) | controller | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ 1 ms | ✅ | ✅ | n/a¹ | | | | 56 | 0 | 1 | 0 |
| pnio-rs v0.1.0 (device) | controller | ◐² | | ✅ | ✅³ | | ✅ | | | n/a¹ | | | | 13 | 0 | 3 | 0 |
| profinet-py 0.6.6 (controller) | device | ✅ | | ✅ | ✅⁴ | ✅ | ✅ | | | ✅ | | | | 32 | 0 | 1 | 3 |
| pnio-dcp 1.2.0 (DCP tool) | device | ✅ | | | | | | | | | | | | 8 | 0 | 0 | 0 |
| umrp v4.0 (MRP node) | MRP node | | | | | | | | | | ✅ | ✅⁵ | | 32 | 0 | 0 | 2 |
| microchip-ung/mrp `177717e` on the Linux bridge (MRP node) | MRP node | | | | | | | | | | ✅⁶ | ✅ | | 31 | 0 | 1 | 0 |
| Net-SNMP 5.9.3 (SNMP manager) | device | | ✅ LLDP-MIB | | | | | | | | | | ✅ | 29 | 0 | 0 | 0 |
| tshark 4.0.17, scapy 2.6.1 (decoders) | every role | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | | | ✅ | ✅⁷ | ✅⁷ | ✅ | 47 | 0 | 0 | 9 |
| **total** | | | | | | | | | | | | | | **248** | **0** | **6** | **14** |

✅ tested and passing · ◐ partly · empty: the peer does not have the
feature. **n/a** and **dev.** are profinet-interop's outcomes: the peer
lacks a feature the test needs, or deviates in an attributed way:

1. p-net and pnio-rs refuse PrmBegin in Data; the AR keeps running.
2. pnio-rs answers Identify only: no DCP Get, Set, Signal or Reset.
3. pnio-rs has no diagnosis records.
4. profinet-py writes I&M1 with BlockLength 58 and leaves stray octets
   after its PNIO-CM blocks (tshark), three deviations; it ignores a GSDML
   fixture's UsedInSlots (n/a).
5. umrp's MRA, as a client, keeps reclaiming the manager role.
6. The kernel flushes its FDB where the adapter cannot observe it. Runs in
   go-profinet's Kernel MRP workflow: micro-VMs with a Linux 6.12 built
   without switchdev, because the hosted runners' kernels refuse MRP on a
   software bridge.
7. tshark decodes every MRP frame; scapy has no MRP dissector (and no DCP
   DHCP block class), and Wireshark decodes the data of DHCP options 61
   and 255 only: decoder limits, recorded as deviations.

No independent IO-Controller runs in CI for go-profinet's device in
RT_CLASS_1/2 besides profinet-py; CODESYS Control for Linux SL is a manual
recipe ([INDEPENDENT-CONTROLLER.md](research/INDEPENDENT-CONTROLLER.md)).
No engineering tool has imported the GSDML fixtures yet
([GSDML-TOOLS.md](research/GSDML-TOOLS.md)).

## 6. Evidence and limits by feature

**Normative source** says where the behaviour comes from when the IEC/PI
text is not available to the project.

| Feature | Normative source | Known limitations |
|---|---|---|
| DCP | IEC 61158-6-10 (not read); Wireshark's dissector; the reference stacks | p-net ignores the AllSelector; no DHCP client; rest of Fast Startup not implemented |
| DCP policy (read-only, rate limits, audit) | local design | not a security class; source MACs can be forged |
| LLDP | IEEE 802.1AB; Wireshark | line delay not measured |
| Connect, PrmEnd, ApplicationReady, Release, abort, CM activity timeout | Wireshark; reference stacks | — |
| Re-parameterization | Wireshark; p-net's CMPBE | no independent device accepts PrmBegin |
| Records, I&M0–4, diagnosis | Wireshark; reference stacks | I&M5 (Wireshark and profinet-py disagree), I&M6/7 (not public), LogBook entries (time stamp layouts disagree) deferred |
| Alarms | Wireshark; reference stacks | — |
| Cyclic data | Wireshark; reference stacks | timing of a general-purpose OS ([CYCLIC.md](CYCLIC.md), [BENCHMARKS.md](BENCHMARKS.md)) |
| Shared device | Wireshark; p-net | shared input not implemented (not public) |
| Port checks, boundaries | Wireshark; p-net | MAU, link state and line delay checks refused |
| Multiport device | IEEE 802.1D behaviour (learning bridge) | software forwarding, not deterministic |
| MRP client and manager | microchip-ung/mrp, umrp and the Linux bridge (IEC 62439-2 not read) | 200/500 ms profiles only; no interconnection, MRPD, diagnoses |
| MRA | microchip-ung/mrp and umrp (NAck/Propagate, priorities); Wireshark (option layout) | a role of the device configuration only (PROFINET's MRP_Role has no value for it); sends the OUI 08-00-06 Wireshark decodes, where the open implementations send 00-00-00 (both parse either) |
| SNMP agent | RFC 1157/3416/3418/1213, IEEE 802.1AB LLDP-MIB, RFC 2021 TimeFilter | no v3, no traps, no PNO LLDP extension MIB |
| Persistence | temporary vs permanent as p-net reads the CMINA state table | port, interface and MRP adjustments not stored |
| System redundancy S2 | Wireshark's elements; p-net's comment for the RDHT unit | not interoperable; R1/R2, MTOT, backup re-parameterization not implemented |
| RT_CLASS_UDP, MCR, PTCP | Wireshark | no behaviour: no open implementation to check against |
| GSDML | an independent catalog of the schema (the XSDs are members-only) | not PI's checker; GSDML 2.46/2.50 content unvalidated |
| AF_PACKET transport | Linux packet(7) | no rings (TPACKET_V3), no qdisc bypass: not measured to help |
| Out of scope (IRT, PTCP sync, isochronous mode, MRPD, TSN, PROFIsafe, security classes, SXP, CC-E, certification) | members-only or hardware-bound | [research/NEXT-PROGRAM-REPORT.md](research/NEXT-PROGRAM-REPORT.md) |
