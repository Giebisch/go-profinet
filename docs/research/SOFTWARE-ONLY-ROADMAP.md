# Software-only feature roadmap

Status: 2026-10-27, baseline go-profinet v0.1.2, profinet-interop v0.1.1,
profinetctl v0.1.1.

This roadmap covers the PROFINET functionality that can be built and
verified **without** PI membership, PI tooling or licensed PI
specifications, commercial PLCs, PROFINET hardware or test rigs: with the
in-process virtual Ethernet (`vnet`), Linux namespaces, veth and bridges,
Docker and the hosted CI runners. It separates four things that are easy
to confuse:

- **implemented**: the code exists and our own controller and device
  exercise it;
- **independently verified**: an implementation we did not write (p-net,
  pnio-rs, profinet-py, pnio-dcp) or an independent decoder (Wireshark
  tshark, Scapy) agrees;
- **timing**: what a general-purpose OS achieves, measured, never
  guaranteed;
- **conformance**: what PI's certification tests decide. go-profinet claims
  none. Agreement between implementations is evidence, not proof of
  conformance: two implementations can share a misreading.

## Evidence

The protocol facts behind each feature, with their sources (file and line
at pinned revisions, or document and section), are in:

- [EVIDENCE-DCP-PDEV.md](EVIDENCE-DCP-PDEV.md): DCP Hello and Fast Startup,
  PDev port and interface records, conformance class B;
- [EVIDENCE-AR-SR-MCR-UDP.md](EVIDENCE-AR-SR-MCR-UDP.md): shared device and
  several IOCARs, system redundancy (IOCARSR), MCR, RT_CLASS_UDP;
- [EVIDENCE-MRP-PTCP-MGMT.md](EVIDENCE-MRP-PTCP-MGMT.md): MRP, PTCP, SNMP and
  CC-B management, DHCP, GSDML 2.46/2.50, GSDX and MRCD;
- [STACKS.md](STACKS.md) and [GSDML.md](GSDML.md): the earlier research on the
  reference stacks and GSDML.

Every fact there carries one of four labels, used below as well:
**VERIFIED** (stated by a public specification excerpt, a public PI or
vendor document, or a field definition in Wireshark's dissector),
**OBSERVED** (what an open implementation does; behaviour, not a norm),
**INFERRED** (our engineering conclusion from the above) and
**UNVERIFIED** (an assumption nothing public confirms). Code was read for
facts only; nothing from a GPL implementation, a licensed specification or
an XSD is copied into this repository.

Sources used: rt-labs p-net (GPL-3.0, tree `ea5e554`), Wireshark's
PROFINET dissectors (GPL-2.0), profinet-py, pnio-rs, pnio-dcp, the Linux
kernel bridge MRP code and uapi header, microchip-ung/mrp and umrp (GPL-2.0),
PI's public Conformance Classes guideline (V1.1), PI's public GSDML
change overview, vendor application notes, Ecma-376 Part 2.

## Baseline (v0.1.2)

From an audit of the code (file:line references in the PRs that change
them):

| Area | State |
|------|-------|
| DCP Hello | codec and requester-side reception (`dcp.Client.Hellos`); the device sends none; the controller resolves by Identify only |
| PDev records | PDPortDataReal (0x802A) and PDRealData (0xF841) read-only; every PD write refused as "invalid index"; LLDP neighbor table with lazy aging and no change events; one port |
| GSDML | catalog-generated types for schema versions 2.25–2.45, unknown content preserved, structural validation with no severities and no semantic rules |
| Several ARs | one IOCAR plus IOSARs up to MaxARs; a second IOCAR is refused (CMRPC "out of ARs"); ownership is implicit in the single IOCAR; alarms go to every IOCAR in Data without an ownership check |
| AR types and blocks | IOCARSingle and IOSAR accepted; IOCARSR, RT_CLASS_3, RT_CLASS_UDP, multicast IOCRs refused; MCR, SRInfo, IRInfo, ARFSU, ARVendor blocks parsed as raw and silently ignored |
| Ports | one Ethernet port per device; port data hard-codes port 1 up, MAU 100BASE-TX FD |
| RT | Ethernet only, unicast FrameIDs, one consumer per FrameID |
| netstack | ARP, IPv4, UDP; no DHCP, IGMP or IP multicast |
| vnet | one flat segment (VLANs, multicast filtering, link state, seeded faults); no multi-segment or switch topology |
| MRP, PTCP, SNMP, DHCP, MCR | absent (constants only) |

## Conformance classes: features versus certification

PI's public Conformance Classes guideline (V1.1) assigns features to
classes: LLDP and the PDev port records belong to A, B and C; class B adds
SNMP with MIB-II and the LLDP-MIB (network diagnostics); Fast Startup and
MRP are optional features of B. It also states that certification against a
class checks the minimum properties and the optional functions a device
declares. Supporting the features of a class is therefore not the same as
being certified for it. go-profinet describes which **features** it covers;
it does not claim conformance to any class. Its SNMP agent (package
`snmp`, read-only v1/v2c with MIB-II and the LLDP-MIB) covers the SNMP
feature of class B as far as public texts describe it; the PNO's LLDP
extension MIB is not public and not implemented.

GSDML 2.50 changes how security is described: it removes SecurityClass 2
and 3 and describes the secure application classes SecureAccess and
SecureRealtime instead (VERIFIED, PI's public GSDML change overview). An
older GSDML attribute `SecurityClass` and the newer application classes are
not interchangeable; go-profinet implements neither.

## Features

Priority: **P1** implement now, **P2** implement after P1, **P3** codecs
and experiments only, **—** deferred.

### 1A. DCP Hello and Fast Startup — P1

- **Status**: Hello codec and reception exist; no device-side Hello; the
  controller ignores Hellos.
- **Versions**: DCP is unchanged across PROFINET versions for this feature.
- **Evidence**: frame layout VERIFIED (FrameID 0xFEFC, multicast
  01-0E-CF-00-00-01, ServiceID 6 request, blocks with BlockInfo, padding to
  an even length; Wireshark and p-net agree). DeviceInitiative (option 6,
  suboption 1: 0 = no Hello after power-on, 1 = Hello) VERIFIED.
  Repetition: p-net sends 3 Hellos 3 s apart at startup when a name and an
  IP are stored, stops on any DCP Set and on Connect (OBSERVED); vendor
  documents give a default interval of 30 ms and 1–15 retries (VERIFIED as
  vendor statements). FSHelloBlock (0x0600: Mode 0 off / 1 on LinkUp / 2 on
  LinkUp after a delay, Interval, Retry, Delay) in record 0x8090
  PDInterfaceFSUDataAdjust: layout VERIFIED, semantics from vendor
  documents.
- **Device**: send Hello sequences on power-on and link-up while waiting
  for a Connect; stop on a Set, a Connect and Close; never overlap
  sequences; no Hello without a name and an IP.
- **Controller**: subscribe to Hellos; a Hello naming a configured device
  that is not connected wakes it and supplies the MAC and IP, so Connect
  can skip Identify (INFERRED from the Hello content and vendor
  statements).
- **Full Fast Startup** is more than Hello: a fixed MAU type with
  autonegotiation off, stored parameters (FSParameterBlock, a parameter
  UUID in non-volatile memory) and the ARFSUBlock in Connect. A
  general-purpose OS controls none of the link settings and go-profinet
  has no persistence; these parts stay out of scope. The FSHello record
  can be supported as configuration.
- **Testability**: vnet (loss, duplication, delay, link flaps, restarts);
  tshark decodes Hello; p-net is configured with `send_hello` and can be a
  Hello source for our controller.
- **Acceptance**: golden Hello frame equal to the dissector-derived vector;
  tshark decodes our Hellos with no expert warning; a device started after
  its controller is connected without an Identify request; Hellos stop on
  Set/Connect/Close; no more than Count Hellos per sequence and never two
  closer than Interval; loss of every Hello falls back to Identify; startup
  times with and without Hello measured and reported, not promised.

### 1B. Port and interface parameterization, topology — P1

- **Status**: PDPortDataReal and PDRealData read-only; writes refused.
- **Evidence**: indexes and block types VERIFIED by Wireshark and p-net
  together: PDPortDataCheck 0x802B (block 0x0200) with CheckPeers 0x020A,
  CheckMAUType 0x020C, CheckLinkState 0x021C, CheckLineDelay 0x020B;
  PDPortDataAdjust 0x802F (0x0202) with AdjustPeerToPeerBoundary 0x0224,
  AdjustDCPBoundary 0x0225, AdjustMAUType 0x020E, AdjustLinkState 0x021B,
  AdjustDomainBoundary 0x0209, AdjustMulticastBoundary 0x0210;
  PDInterfaceAdjust 0x8071 (0x0250); PDInterfaceDataReal (block 0x0240,
  read through PDRealData); PDExpectedData 0xF842. Device behaviour in
  p-net (OBSERVED): it accepts Check/Adjust writes, evaluates checks after
  the write and on every LLDP change, timeout and link-down, and reports a
  failed peer check as channel diagnosis 0x8001 "remote mismatch" with
  extended types 0x8000 (peer name of station), 0x8001 (peer port name),
  0x8005 (no peer detected). The PNIORW code for an unsupported sub-block
  is UNVERIFIED (p-net answers "invalid index" class codes).
- **Device**: accept and store PDPortDataCheck; evaluate CheckPeers,
  CheckMAUType and CheckLinkState against the LLDP table and the port
  state, raise and clear "remote mismatch" diagnoses with alarms; accept
  only adjustments the device can honour (PeerToPeerBoundary bit 0: stop
  sending LLDP; DCPBoundary: drop DCP frames on the port; PDInterfaceAdjust
  MultipleInterfaceMode) and refuse the others (MAU type, link state,
  domain boundaries) with a PNIORW error instead of pretending; serve
  PDInterfaceDataReal inside PDRealData; serve PDExpectedData from the
  stored checks; never report an adjustment as applied that is not.
- **Controller**: write PD checks from the model (expected neighbors per
  port) as parameters before PrmEnd; a topology verification API that
  compares PDRealData of every device with the expected topology.
- **Testability**: vnet with LLDP; LLDP timeouts and contradictory
  neighbors are reproducible; p-net is an independent device for writes
  of Check/Adjust and its diagnoses.
- **Acceptance**: golden blocks; fuzzed decoders; correct topology gives
  no diagnosis; wrong peer, missing peer, neighbor timeout and two
  contradictory neighbors give the documented diagnoses and alarms, and
  clear when fixed; unsupported adjustments refused; reparameterization in
  a running AR (PrmBegin/PrmEnd) re-evaluates; p-net accepts our writes and
  reports the same mismatches.

### 1C. GSDML validation — P1

- **Status**: structural validation against the independent catalog
  (2.25–2.45); no severities, no semantic rules.
- **Evidence**: GSDML 2.46 adds I&M6/I&M7; 2.50 adds the secure
  application classes and conformance class E (VERIFIED, PI's public change
  overview). The XSDs and the specification are members-only: the catalog
  is not extended with 2.46/2.50 element definitions.
- **Plan**: issues gain a severity (error, warning, unvalidated); unknown
  content of newer versions is reported as unvalidated, not as an error;
  semantic rules only where public evidence substantiates them, others
  labelled as local heuristics (for example IO data length totals against
  the 1440-octet frame limit, which follows from the RT frame size, or
  references from UseableModules to undefined modules); synthetic MIT
  fixtures for each rule.
- **Acceptance**: every rule has a positive and a negative fixture; no
  existing valid fixture changes verdict; documents claim no official
  validation.

### 2. Shared device: several IOCARs — P1

- **Status**: one IOCAR per device.
- **Evidence**: per-submodule ownership, first come first served, in p-net
  (OBSERVED): a Connect for a submodule another AR owns is accepted; its
  ModuleDiff reports the submodule with SubmoduleState ARInfo 3 "locked by
  IO controller"; only the owner's IOCR carries the submodule's data, the
  others carry IOxS bad; on release the next AR that expects the
  submodule becomes owner and receives a "Released" alarm (0x0009).
  SubmoduleState bit fields VERIFIED (Wireshark). Shared **input**
  (SubmoduleProperties bit 2) is a per-submodule flag; p-net does not
  implement it; the data a non-owner then receives is UNVERIFIED.
- **Device**: ownership table keyed by (API, slot, subslot); ModuleDiff
  marks submodules owned by another AR; outputs only from the owner;
  inputs to the owner (and to shared-input readers once that is
  substantiated); alarms and diagnosis routed to owners; record writes
  restricted to the owner and to IOSARs; release, abort and timeout hand
  ownership over; deterministic winner for concurrent Connects (the first
  to take the device lock).
- **Controller**: connect with a subset of the model's submodules;
  report locked submodules from the ModuleDiff.
- **Independent check**: the p-net image (built with two ARs) with two of
  our controllers, and profinet-py as a second controller where its
  capabilities allow (separate network namespaces: it proposes the same
  input FrameID). A test of our controller against our own device alone
  is not counted as shared-device interoperability.
- **Acceptance**: the ten end-to-end scenarios of the program (two owners,
  concurrent data, one disconnects, a third rejected or locked, a race for
  one submodule, recovery after timeout, shared input, mixed I/O, alarm
  routing, concurrent parameterization without deadlocks or races) pass
  under the race detector and the soak; the p-net run shows the same
  ownership behaviour.

### 3. System redundancy S2 (IOCARSR) — P3

- **Evidence**: wire elements VERIFIED in Wireshark: ARType 0x0020, the
  ARUUID split into discriminator, ConfigID and selector (AR number 1–4),
  SRInfoBlock 0x010A (RedundancyDataHoldFactor, SRProperties with
  InputValidOnBackupAR and Mode), DataStatus bits 0 (primary/backup) and 1
  (redundancy), primary and backup request and acknowledge codes, abort
  reasons 38–43. Missing: the units of RedundancyDataHoldFactor, the
  switchover sequence, MTOT and every state machine. **No open
  implementation** exists as an oracle (p-net, pnio-rs and profinet-py
  support ARTypes 1 and 6 only; the PI Community Stack is members-only).
- **Plan**: codecs for SRInfoBlock, the ARUUID selector and the DataStatus
  redundancy bits, checked against Wireshark; a gated, experimental device
  mode (off by default) implementing the primitives that follow from the
  wire elements (two ARs of one AR set, outputs from the primary only,
  inputs valid on the backup per InputValidOnBackupAR, switchover on a
  primary request); a vnet scenario that measures detection and failover.
  Documented as experimental and not interoperable until an independent
  S2 implementation or normative text is available.
- **Status (2026-10-27)**: the gated experiment is done
  (`device.Config.SystemRedundancy`, `controller.DeviceConfig.SystemRedundancy`,
  `Device.SetPrimary`). e2e on vnet: both IOCARSRs of a set run, the
  outputs come from the primary and the inputs reach both (primary/backup
  acknowledge), the backup cannot write, a switchover hands the outputs
  over in about 5 ms, a failed primary is noticed when its data hold
  expires and the backup takes over before RDHT runs out, and without a
  primary the set is aborted with reason 38. Not interoperable: no
  independent S2 implementation exists.

### 4A. Multiport devices — P2

- **Plan**: a device with several logical ports (one `l2.Port` each),
  per-port MAC, LLDP agent, link state, port subslot and port records; the
  existing one-port configuration stays the default. vnet gains a
  multi-segment topology (a virtual bridge between segments) so that a
  two-port device can sit in a line or a ring.
- **Acceptance**: PDRealData and LLDP report each port's own neighbor;
  link events per port; existing single-port behaviour unchanged.
- **Status (2026-10-27)**: done. `l2/bridge` (learning switch, CPU port,
  per-port local ports for LLDP and MRP, forwarding/blocked states, DCP
  boundaries, bounded FDB) and `device.Config.Bridge`; vnet segments carry
  lines and rings. e2e `TestMultiportLine`: B reached through A's bridge,
  each port's own neighbor in PDRealData, a peer check per port,
  `CheckTopology`, a pulled cable takes B offline and A runs on.

### 4B. MRP — P2

- **Evidence**: frame format VERIFIED (EtherType 0x88E3, destinations
  01-15-4E-00-00-01..04, TLVs Common, Test, TopologyChange, LinkDown,
  LinkUp, Option, End and the interconnection TLVs; Wireshark, the Linux
  uapi header and microchip-ung/mrp agree except for the padding after
  LinkDown/LinkUp and InLink's LinkInfo field). MRM and MRC states and the
  default timers of the 500/200/30/10 ms profiles are OBSERVED in
  microchip-ung/mrp and umrp, not checked against IEC 62439-2. PROFINET MRP
  records (indexes 0x8050–0x8054, blocks 0x0211–0x0219) VERIFIED in
  Wireshark; the order of 0x8055–0x8057 differs between Wireshark and
  p-net.
- **Plan**: codecs with fuzzing; MRC first, then MRM, on the multiport
  device; the PROFINET MRP records and diagnoses; rings on vnet and on
  Linux veth/bridges. The Linux kernel generates Test frames itself only
  with a userspace daemon; microchip-ung/mrp is the candidate independent
  peer, but configuring a ring role on veth may fail on kernels with
  switchdev (INFERRED from the code, to be tested).
- **Claims**: no industrial reconvergence time is claimed from virtual
  rings; recovery times are measured. MRPD is deferred.
- **Status (2026-10-27)**: MRC and MRM done (`mrp.New`, on `l2/bridge`),
  with the PROFINET MRP records 0x8050–0x8054 (version 1.0 blocks, decoded
  field by field by tshark) and `controller.DeviceConfig.MRP`. Rings of
  three bridges and of three devices on vnet: closed with the manager's
  secondary port blocked, no loop (one broadcast is delivered once), a
  cable break opens the ring and the far device is reached the other way
  round (about 20–35 ms on vnet with the 200 ms profile), the cable back
  closes it. Not done: an independent MRP peer (microchip-ung/mrp needs a
  kernel with CONFIG_BRIDGE_MRP and a container network that carries a
  ring), MRA, interconnection, MRP diagnoses.

### 5. Optional extensions

| Feature | Priority | Evidence | Plan / blocker |
|---------|----------|----------|----------------|
| RT_CLASS_UDP | P3 | RTClass 4, RT payload on UDP 34962, UDP ports in the AR block VERIFIED; p-net validates the fields but has no data path | **done (codecs)**: `rt` UDP payload codec, `pnio` RTClass/LT/AlarmCR transport fields; the device refuses with DB 81 02 07 (IOCRProperties), the controller refuses to build one; tshark and scapy decode our frames. Behaviour unverifiable: no peer implements it |
| MCR (multicast CRs) | P3 | MCRBlockReq 0x0106 layout VERIFIED in Wireshark; AddressResolutionProperties bits, multicast MAC allocation and the resolution procedure UNVERIFIED; p-net parses but does not implement | codec only until the procedure is substantiated |
| PTCP | P3 | Delay PDUs and TLVs VERIFIED in Wireshark; the meaning of 0xFF41–0xFF43 and the destination MAC need a capture | decoder; line delay stays "not measured" (p-net does the same) |
| DHCP | P3 | DCP option 3 suboptions are DHCP option codes (61 client ID, 255 control) and IP BlockInfo bit 1 "set by DHCP" VERIFIED; no PI guidance on DHCP use found; p-net has none | **done**: `dcp` DHCP block codec (`DHCPParameter`, client ID, control), `Identity.DHCP` and `IPSetByDHCP`, responder Get from `device.Config.DHCP`, Set only through `Config.SetDHCP`; tshark decodes them, scapy has no DCP DHCP block class; addressing stays with the operating system (`ApplyIP`), no DHCP client in the library |
| SNMP (CC-B features) | P2 | CC-B needs SNMP with MIB-II and the LLDP-MIB (VERIFIED, PI guideline); p-net uses net-snmp; the PNO LLDP extension MIB's text is not public (only its OIDs) | **implemented** (package `snmp`, opt-in `device.Config.SNMP`): a read-only SNMPv1/v2c agent with the MIB-II system, interfaces and snmp groups and the LLDP-MIB local data and lldpRemTable, standard library only; **independently verified**: tshark and Scapy decode a v1/v2c walk without a warning (`interop/snmp_test.go`). No SNMPv3, no traps, no PNO extension MIB |
| DCP restricted modes, hardening | P2 | DCPBoundary and the device's own policies | **done**: `dcp.Policy` / `device.Config.DCPPolicy`: read-only DCP (Set refused with "SET not possible by local reasons", Identify/Get answered), per-source rate limits for Identify responses and Set requests in a fixed-size table, audit events (callback and log fields); off by default; e2e and flood tests; not a PROFINET security class |
| GSDX/MRCD | P3 | GSDX is an Open Packaging Conventions container (ISO/IEC 29500-2, Ecma-376 Part 2, public); its PI rules, signing and MRCD are members-only | a generic container inspector (parts, content types, relationships, signature metadata), unknown parts preserved; no PI-specific validation. **Status: GSDX inspector done** (`gsdml/gsdx`): signatures reported, not verified; MRCD not implemented |
| GSDML 2.46/2.50 | — | XSDs members-only | unknown elements preserved and reported as unvalidated |

### Deferred

IRT (RT_CLASS_3), PTCP synchronisation, isochronous mode, MRPD, TSN,
PROFIsafe, PROFINET security classes and secure application classes, PI
signing, official XSD validation, MRCD issuance and PI certification stay
deferred until normative access and the hardware to test them exist.

## Order of work

1. This roadmap and the documentation corrections (one PR).
2. 1A DCP Hello (one PR).
3. 1B PDev checks and adjustments, topology verification (one or two PRs).
4. 1C GSDML severities and semantic rules (one PR).
5. 2 shared device (one or two PRs, with the p-net two-controller interop).
6. 4A multiport and vnet topology, then 4B MRP codecs and MRC.
7. 3 and 5: codecs and gated experiments.

Each PR states its acceptance criteria and evidence, keeps the coverage
gate, adds fuzz targets for every new decoder, and is soaked (25 parallel
runs of every CI job and the interop suite) before a release.
