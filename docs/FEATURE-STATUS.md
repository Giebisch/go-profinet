# Feature status

What go-profinet implements, how each feature is tested, and what limits
it. Status: go-profinet v0.4.0, profinet-interop v0.3.0, 2026-10-28.

The columns keep four things apart that are easily conflated:

- **Implemented**: the code exists and is unit tested (golden vectors,
  fuzzing of every decoder of network input, race tests).
- **E2E tested**: a go-profinet controller and device (or two devices)
  exercise it on the virtual network, with seeded faults where it takes
  network input.
- **Independently tested**: software that is not go-profinet exchanged it
  with go-profinet and the result was checked (profinet-interop's matrix;
  decoders count as independent only for the encoding).
- **Experimental**: off by default, not interoperable, built from public
  wire elements without an independent implementation to check against.

None of this is a conformance claim: no PI test has been run, nothing is
certified, and the **normative source** column says where the behaviour
comes from when the IEC/PI text is not available to the project
([docs/PI-MEMBERSHIP.md](PI-MEMBERSHIP.md)).

| Feature | Implemented | E2E tested | Independently tested | Experimental | Normative source | Known limitations |
|---|---|---|---|---|---|---|
| DCP: Identify, Get, Set (name, IP, temporary/permanent), Signal, ResetToFactory | yes | yes | p-net, pnio-rs, profinet-py, pnio-dcp | no | IEC 61158-6-10 (not read); Wireshark's dissector; the reference stacks | p-net ignores the AllSelector |
| DCP Hello, DeviceInitiative | yes | yes | p-net's Hellos drive the controller; tshark and scapy decode go-profinet's | no | as above | rest of Fast Startup not implemented |
| DCP DHCP blocks (option 3) | codec, Set through a handler | yes | tshark decodes (61, 255); scapy has no class | no | Wireshark | no DHCP client |
| DCP read-only mode, rate limits, audit | yes | yes (flood of 22 000 frames) | n/a (local policy) | no | local design | not a security class; source MACs can be forged |
| LLDP with the PNO TLVs, neighbor table | yes | yes | p-net; Net-SNMP through the LLDP-MIB | no | IEEE 802.1AB; Wireshark | line delay not measured |
| Connect: IOCARSingle legacy (RT_CLASS_1) and advanced (RT_CLASS_2) startup, IOSAR | yes | yes | p-net, pnio-rs, profinet-py | no | Wireshark; reference stacks | — |
| PrmEnd, ApplicationReady, Release, abort, CM activity timeout | yes | yes | p-net, pnio-rs, profinet-py | no | as above | — |
| Re-parameterization in Data: PrmBegin, writes, PrmEnd, ApplicationReady | controller (`Reparameterize`) and device | yes | profinet-py re-parameterizes go-profinet's device; p-net and pnio-rs refuse PrmBegin (their limits, recorded) | no | Wireshark; p-net's CMPBE | no independent device accepts PrmBegin |
| Records: user, I&M0–4, WriteMultiple, ReadImplicit, ModuleDiff, identification, diagnosis records | yes | yes | p-net, pnio-rs, profinet-py | no | Wireshark; reference stacks | — |
| I&M5–I&M7 | no | — | — | — | Wireshark and profinet-py disagree on I&M5; I&M6/7 not public | deferred |
| LogBook (0xF830) | read, without entries | — | — | no | Wireshark (8-octet time stamps) and p-net (12-octet) disagree; EntryDetail is stack-specific | entries deferred |
| Alarms: high/low priority, sequence, ACK, retransmission, diagnosis/process alarms | yes | yes (chaos) | p-net, pnio-rs, profinet-py | no | Wireshark; reference stacks | — |
| Cyclic data RT_CLASS_1/2, IOPS/IOCS, DataStatus, watchdog and data hold | yes | yes | p-net (1 ms), pnio-rs, profinet-py | no | Wireshark; reference stacks | timing of a general-purpose OS ([CYCLIC.md](CYCLIC.md)) |
| Shared device (several IOCARs, one owner per submodule, handover) | yes | yes | p-net with two go-profinet controllers | no | Wireshark; p-net | shared input not implemented (not public) |
| Port checks (CheckPeers), boundaries, PDInterfaceAdjust, topology check | yes | yes | p-net evaluates go-profinet's records | no | Wireshark; p-net | MAU, link state, line delay checks refused |
| Multiport device (`l2/bridge`) | yes | yes | n/a (no open multiport PROFINET device); umrp and microchip-ung/mrp nodes forward through it | no | IEEE 802.1D behaviour (learning bridge) | software forwarding, not deterministic |
| MRP client and manager (MRC, MRM), PROFINET MRP records | yes | yes | **umrp** (MRM against umrp MRCs, MRC next to a umrp MRM: ring closed, opened, flushed, no loop or hole, seeded faults); tshark decodes every frame; microchip-ung/mrp not applicable (kernel) | no | microchip-ung/mrp, umrp and the Linux bridge (IEC 62439-2 not read) | 200/500 ms profiles only; no MRA, interconnection, MRPD, diagnoses |
| SNMP agent (v1/v2c, MIB-II system/interfaces/snmp, LLDP-MIB) | yes | yes | **Net-SNMP** (walks, errors, limits, malformed datagrams, flood, changing LLDP neighbors); tshark, scapy | no | RFC 1157/3416/3418/1213, IEEE 802.1AB LLDP-MIB, RFC 2021 TimeFilter | no v3, no traps, no PNO LLDP extension MIB |
| Persistence (`store`, `Config.Store`): permanent name and IP, DHCP options, I&M, resets | yes | yes (restart, failure, corruption) | n/a (local) | no | temporary vs permanent as p-net reads the CMINA state table | port/interface/MRP adjustments not stored |
| System redundancy S2 (IOCARSR AR sets, primary/backup) | yes | yes | none exists (p-net and pnio-rs refuse IOCARSR; CODESYS has S2, manual and untried: [INDEPENDENT-CONTROLLER.md](research/INDEPENDENT-CONTROLLER.md)) | **yes** | Wireshark's elements; p-net's comment for the RDHT unit | not interoperable; R1/R2, MTOT, backup re-parameterization not implemented |
| RT_CLASS_UDP, MCR, PTCP delay frames | codec only | refusal tested | tshark and scapy decode (scapy: PTCP header only) | — | Wireshark | no behaviour: no open implementation |
| GSDML: parse, catalog validation, Check, model, generation, GSDX inspection | yes | yes (fixtures run as devices and controllers) | pnio-rs's GSDML drives the controller; profinet-py parses go-profinet's generated GSDML and the fixtures; CODESYS and TIA Portal not run | no | an independent catalog of the schema (XSDs are members-only) | not PI's checker; GSDML 2.46/2.50 content unvalidated |
| Linux AF_PACKET transport | yes, allocation-free | yes (real veth, `make l2-test`) | p-net and others over it (interop) | no | Linux packet(7) | no rings (TPACKET_V3), no qdisc bypass: not measured to help |
| IRT, PTCP synchronisation, isochronous mode, MRPD, TSN, PROFIsafe, security classes, SXP, CC-E, certification | no | — | — | — | members-only or hardware-bound | deferred ([research/NEXT-PROGRAM-REPORT.md](research/NEXT-PROGRAM-REPORT.md)) |

The evidence behind each "independently tested" entry is profinet-interop's
[MATRIX.md](https://github.com/otfabric/profinet-interop/blob/v0.3.0/docs/MATRIX.md).
