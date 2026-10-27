# Known limitations

What go-profinet does not do, or does with restrictions. Each item says why
and what would lift it.

## Protocol scope

| Not implemented | Notes |
|-----------------|-------|
| IRT (RT_CLASS_3), PTCP time synchronization and line delay measurement, isochronous mode | needs hardware timestamping and scheduling a general-purpose OS cannot give; out of scope for a pure Go library |
| MRP (media redundancy), MRPD | the `mrp` package encodes and decodes MRP frames (verified by tshark); there is no ring manager or client state machine and a device with two ports is not modelled (one Ethernet port) |
| System redundancy (IOCARSR, S2/R1/R2), shared input, MCR (multicast CRs) | rejected by the device; not requested by the controller. Their blocks are encoded and decoded (`pnio.SRInfoBlock`, `ARSetID`, `MCRBlockReq`, the `rt` redundancy DataStatus codes): no open implementation of their behaviour exists to test against, and the switchover sequence, MCR address resolution and the RedundancyDataHoldFactor unit are not public. Shared device (several controllers, one owner per submodule) is implemented; shared input is not: what a non-owner receives is not public |
| PROFINET security classes 1–3, PROFIsafe | no authentication, integrity protection or safety layer (see [SECURITY.md](SECURITY.md)) |
| Fast Startup beyond DCP Hello | the device sends DCP Hello and the controller connects through it; the rest of Fast Startup (fixed MAU type with autonegotiation off, stored parameters, ARFSUBlock) needs control over the link and persistence that a library on a general-purpose OS does not have |
| A DHCP client | DCP's DHCP blocks (option 3) are decoded and encoded, the device reports configured DHCP options and "IP set by DHCP", and passes a DCP Set of them to `Config.SetDHCP` (refused without it). Obtaining an address by DHCP stays with the operating system; no public PI guidance on DHCP use was found |
| Port checks and adjustments beyond peers and boundaries | the device evaluates CheckPeers and applies PeerToPeerBoundary, DCPBoundary and PDInterfaceAdjust; it refuses CheckMAUType, CheckLinkState, CheckLineDelay (the real MAU type is not known to a library, the line delay is not measured, the link-state diagnosis code is not public) and the domain, multicast, MAU, link-state and preamble adjustments (no frame forwarding with one port; the link belongs to the operating system), with PNIORW "feature not supported" (0xA9), a code PI may define differently |
| RT over UDP (RT_CLASS_UDP), RT_CLASS_STREAM (TSN) | RT_CLASS_UDP has codecs only (`rt.AppendUDPPayload`/`DecodeUDP` for the RT payload on UDP port 34962, the RTClass, LT and AlarmCR transport fields in `pnio`), checked by tshark and scapy; the device refuses an RT_CLASS_UDP Connect (PNIO status DB 81 02 07, IOCRProperties) and the controller refuses to build one. No open implementation has a UDP data path to test against (p-net validates the fields only) |
| GSDX rules and signing, MRCD | their formats are not public (PI members only): see [docs/research/GSDML.md](docs/research/GSDML.md). `gsdml/gsdx` inspects the public OPC layer of a GSDX (see below); MRCD is not implemented |

What of this list can be built without PI membership or PROFINET hardware,
with the protocol evidence for each feature and the order of work, is in
[docs/research/SOFTWARE-ONLY-ROADMAP.md](docs/research/SOFTWARE-ONLY-ROADMAP.md).

## Without PI membership

Several artefacts are published by PROFIBUS & PROFINET International to
members only: the GSDML XSDs, GSDX, MRCD, the PROFINET security and test
specifications. [docs/PI-MEMBERSHIP.md](docs/PI-MEMBERSHIP.md) lists, feature by
feature, what is missing or degraded as a result, and what membership would
change.

## GSDML

The official GSDML schemas and specification are licensed by PROFIBUS &
PROFINET International to its members and are not redistributable.
go-profinet generates its GSDML types from an independently written catalog
of the schema (`gsdml/schema/catalog.json`) and validates against it: the
structure, attribute types and references that a device model relies on.
`gengsdml -xsd DIR` compares the catalog with a licensed local copy of the
XSDs (0 differences against v2.35). `gsdml.Validate` is not PI's GSDML
checker, and a file it accepts may still fail PI's. Elements and attributes
outside the catalog are kept verbatim and reported as unvalidated by
`gsdml.Check`; its semantic rules are a small set, each with its basis
(docs/GSDML.md), several of them local heuristics. Schema versions after 2.45
(2.46, 2.50) are not in the catalog: their schemas are members-only.

## GSDX containers

`gsdml/gsdx` is a generic Open Packaging Conventions inspector (ISO/IEC
29500-2 / Ecma-376 Part 2), not a GSDX implementation: PI's container
specification, signing policy and MRCD are members-only. It applies no
PI-specific rule and never says that a file is a valid GSDX. Digital
signatures are reported (references, signature time, signer certificate),
never verified: there is no XML canonicalization, no SignatureValue check
and no trust decision, so `Signature.Verified` is always false. Only the
digest of a part reference without transforms is compared with the part.
Which parts hold GSDML is guessed from the content type or the
`GSDML-*.xml` file name. Interleaved part pieces are kept, not reassembled.
`WriteTo` keeps every entry's name, method, time and content but compresses
again, so the compressed octets may differ.

## Timing

- Cyclic data runs on Go timers on a general-purpose OS. On Linux and macOS
  a 1 ms cycle is held with a mean lateness well below the cycle and
  occasional outliers; the watchdog and data hold factors absorb them. See
  [docs/CYCLIC.md](docs/CYCLIC.md) and [docs/BENCHMARKS.md](docs/BENCHMARKS.md)
  for measurements. In Docker Desktop's VM under the race detector, cycles
  below 4 ms are not reliable.
- There is no real-time guarantee: go-profinet is not for applications that
  need IRT, motion control or safety reaction times.

## Platforms

| Backend | State |
|---------|-------|
| Linux AF_PACKET | tested on a veth pair in Docker (`make l2-test`) and by the interop suite; a CI job runs the same tests on a veth pair |
| macOS / BSD BPF | a CI job tests it on a feth pair as root; locally, opening and reading an interface was verified (profinetctl on macOS) |
| Windows Npcap | cross-compiled; its unit tests run without Npcap; not tested against Npcap (the hosted runners have none) |

VLAN-tagged reception: the kernel strips the 802.1Q header before AF_PACKET
delivers a frame; the backend restores the tag from the packet's auxiliary
data (PACKET_AUXDATA).

## Devices

- One owner AR per submodule (shared device); several ARs per device up to
  `MaxARs`.
- One Ethernet port by default; with `Config.Bridge` a multiport device
  (`l2/bridge`): each port has its own MAC, LLDP agent, link state and port
  data. The bridge forwards in software (a goroutine hop and a copy per
  frame, no cut-through, no hardware queues): it suits tests and software
  devices, not deterministic forwarding; its ports must receive every frame
  (promiscuous). Its forwarding database is bounded (1024 stations by
  default; beyond that frames are flooded).
- No persistence: I&M records, the station name and the IP address set with
  `--permanent` are kept in memory; an application that needs persistence
  stores them from `Handler.NameChanged`, `IPChanged` and `WriteRecord`.

## Verification

- Interoperability is verified against four open implementations (p-net,
  pnio-rs, profinet-py, pnio-dcp) and two decoders (scapy, Wireshark). There
  is no second open IO-Controller; no commercial PLC or device has been
  tested.
- No PI certification test has been run. go-profinet claims no conformance.
