# Known limitations

What go-profinet does not do, or does with restrictions. Each item says why
and what would lift it.

## Protocol scope

| Not implemented | Notes |
|-----------------|-------|
| IRT (RT_CLASS_3), PTCP time synchronization, isochronous mode | needs hardware timestamping and scheduling a general-purpose OS cannot give; out of scope for a pure Go library |
| MRP (media redundancy), MRPD | no ring support; a device with two ports is not modelled (one Ethernet port) |
| System redundancy (IOCARSR, S2/R1/R2), shared input, MCR (multicast CRs) | rejected by the device; not requested by the controller. Shared device (several controllers, one owner per submodule) is implemented; shared input is not: what a non-owner receives is not public |
| PROFINET security classes 1–3, PROFIsafe | no authentication, integrity protection or safety layer (see [SECURITY.md](SECURITY.md)) |
| Fast Startup beyond DCP Hello, DHCP | the device sends DCP Hello and the controller connects through it; the rest of Fast Startup (fixed MAU type with autonegotiation off, stored parameters, ARFSUBlock) needs control over the link and persistence that a library on a general-purpose OS does not have |
| Port checks and adjustments beyond peers and boundaries | the device evaluates CheckPeers and applies PeerToPeerBoundary, DCPBoundary and PDInterfaceAdjust; it refuses CheckMAUType, CheckLinkState, CheckLineDelay (the real MAU type is not known to a library, the line delay is not measured, the link-state diagnosis code is not public) and the domain, multicast, MAU, link-state and preamble adjustments (no frame forwarding with one port; the link belongs to the operating system), with PNIORW "feature not supported" (0xA9), a code PI may define differently |
| RT over UDP (RT_CLASS_UDP), RT_CLASS_STREAM (TSN) | |
| GSDX, MRCD | their formats are not public (PI members only): see [docs/research/GSDML.md](docs/research/GSDML.md) |

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
outside the catalog are kept verbatim, not validated.

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
- One Ethernet port: port data reports the LLDP neighbors of the interface
  as the peers of the first port.
- No persistence: I&M records, the station name and the IP address set with
  `--permanent` are kept in memory; an application that needs persistence
  stores them from `Handler.NameChanged`, `IPChanged` and `WriteRecord`.

## Verification

- Interoperability is verified against four open implementations (p-net,
  pnio-rs, profinet-py, pnio-dcp) and two decoders (scapy, Wireshark). There
  is no second open IO-Controller; no commercial PLC or device has been
  tested.
- No PI certification test has been run. go-profinet claims no conformance.
