# Interoperability

What go-profinet implements of PROFINET IO, service by service, and how it is
verified against independent implementations. go-profinet is not certified
by PROFIBUS & PROFINET International and claims no conformance: the tables
below say what is implemented and tested, not what a certification test
would find.

## Implemented services

✅ implemented and tested · ◐ partly (the note says what) · — not implemented.
"Interop" names the independent implementations a feature is verified
against (see [the matrix](#interop-matrix)).

### DCP (IEC 61158-6-10, Discovery and Configuration)

| Service | Requester (`dcp.Client`) | Responder (`device`) | Interop |
|---------|:---:|:---:|---------|
| Identify all, with ResponseDelayFactor | ✅ | ✅ (spreads its answer) | p-net, pnio-rs, profinet-py, pnio-dcp |
| Identify filtered by NameOfStation, AliasName, IP, DeviceID | ✅ | ✅ | p-net, pnio-rs |
| Get (named blocks; AllSelector) | ✅ (by name: p-net ignores AllSelector) | ✅ | p-net, pnio-dcp, profinet-py |
| Set NameOfStation, IP parameter, temporary or permanent | ✅ | ✅ | p-net, pnio-dcp, profinet-py |
| Set Control/Signal | ✅ | ✅ | p-net, pnio-dcp, profinet-py |
| Set Control/ResetToFactory (all modes), FactoryReset | ✅ | ✅ | p-net, pnio-dcp, profinet-py |
| Hello: the device announces itself; the controller connects without Identify | ✅ (connects through the Hello) | ✅ (sends; not the rest of Fast Startup) | p-net (its Hellos, our controller: `TestPnetHello`); tshark and scapy decode our Hellos (`TestOtfHelloDecoders`) |
| Control/Start and Control/Stop around a Set | ✅ | ✅ | |
| DHCP options (option 3: client ID 61, control 255, others raw), IP BlockInfo "set by DHCP" | ✅ (Get, `SetDHCP`, parsed in `Identity`) | ✅ (reports `Config.DHCP`; Set only through `Config.SetDHCP`, else "option not supported"; no DHCP client) | tshark decodes them (`TestDCPDHCPDecoders`; it shows options other than 61 and 255 as undecoded data); scapy 2.6.1 has no DCP DHCP block class and leaves them Raw |
| Requester lock of another requester (3 s) | — | ✅ | p-net (as the locking side) |
| Read-only DCP, per-source rate limits, audit events (`DCPPolicy`, opt-in) | — | ✅ | own e2e and flood tests only (local policy, no peer behaviour to compare) |

### LLDP (IEEE 802.1AB with the PROFINET TLVs)

| Feature | State | Interop |
|---------|:---:|---------|
| Chassis ID (station name), port ID (`port-001.<station>`), TTL, system description, management address | ✅ | p-net |
| PROFINET port status, chassis MAC, measured delay TLVs; IEEE 802.3 MAC/PHY | ✅ (decode; send port status and chassis MAC) | p-net |
| Neighbor table with TTL expiry, AliasName | ✅ | p-net |
| Delay measurement (PTCP) | — | |

### Context management (DCE/RPC, UDP 34964)

| Service | Controller | Device | Interop |
|---------|:---:|:---:|---------|
| Connect, IOCARSingle, legacy startup (RT_CLASS_1) | ✅ | ✅ | p-net, profinet-py |
| Connect, advanced startup (RT_CLASS_2) | ✅ | ✅ | p-net, pnio-rs |
| Connect, IOSAR (device access, supervisor) | ✅ | ✅ | profinet-py |
| ExpectedSubmoduleBlockReq, ModuleDiffBlock | ✅ | ✅ (wrong, substitute, missing; IOPS bad) | p-net, pnio-rs, profinet-py |
| PrmEnd, ApplicationReady | ✅ | ✅ | p-net, pnio-rs, profinet-py |
| PrmBegin (re-parameterization in Data): PrmBegin, writes, PrmEnd, ApplicationReady | ✅ (`Device.Reparameterize`) | ✅ | profinet-py re-parameterizes go-profinet's device (`TestProfinetPyReparameterize`); p-net (DD 81 14 08: it accepts no DControl but PrmEnd) and pnio-rs (DD 81 3D 03) refuse PrmBegin in Data, their ARs run on (`TestPnetReparameterize`, `TestPnioRsReparameterize`) |
| Release, abort, CM initiator activity timeout | ✅ | ✅ | p-net, pnio-rs |
| ARRPCBlockReq (controller RPC port) | ✅ | ✅ | |
| Several ARs on one device (`MaxARs`) | ✅ | ✅ | profinet-py (IOSAR + IOCAR) |
| Shared device: several IOCARs, one owner per submodule, "locked by IO controller" in the ModuleDiff, owner-only data, alarms and writes | ✅ (subset models, locked submodules) | ✅ | p-net with two go-profinet controllers (`TestPnetSharedDevice`) |
| Hand-over: Released alarm, PrmEnd (plug), ApplicationReady (plug), one submodule at a time | ✅ | ✅ | p-net (`TestPnetSharedDevice`) |
| Shared input | — | — | not public; p-net does not implement it |
| IOCARSR blocks: ARUUID selector, SRInfoBlock; MCRBlockReq (codecs only) | ✅ encode | ✅ decode (refused) | tshark and scapy decode a Connect with them (`TestCodecDecoders`); no open implementation of the behaviour |
| MRP: client and manager on a multiport device (test frames, ring open/closed, blocked secondary, TopologyChange, link change frames) | — | ✅ (`Config.MRP`) | umrp as manager and as clients in rings of veth cables with go-profinet: closed, opened, FDB flushed, no loop or hole, seeded faults (`TestMRPOtfManager`, `TestMRPOtfClient`, `TestMRPRingFaults`); microchip-ung/mrp on the kernel's bridge MRP, the same tests and `TestMRPAuto`, in the Kernel MRP workflow's micro-VMs (Linux 6.12 built without switchdev: the hosted runners' kernel refuses MRP on a software bridge); tshark decodes every frame, scapy has no MRP dissector |
| PTCP delay frames (codecs only) | ✅ | ✅ | tshark and scapy (`TestCodecDecoders`) |
| RT_CLASS_UDP: RT payload on UDP port 34962, RTClass 4 / LT 0x0800 / RTA_CLASS_UDP in Connect (codecs only) | ✅ encode, refused by `AddDevice` | ✅ decode, Connect refused (DB 81 02 07) | tshark and scapy decode both (`TestUDPRTDecoders`); no open implementation of the data path |
| IOCARSR (system redundancy S2): AR sets of two IOCARSRs, primary by DataStatus request, outputs held for RDHT, abort reason 38 (EXPERIMENT, off by default) | ✅ (`SystemRedundancy`, `SetPrimary`) | ✅ (`Config.SystemRedundancy`) | none: no open S2 implementation exists (p-net and pnio-rs refuse IOCARSR); own e2e only (`TestSystemRedundancy*`) |
| MCR, IRInfo, PrmServer, AR vendor blocks | — | — (rejected) | |
| DCE/RPC: fragmentation, retransmission, at-most-once, idempotent calls, ping/working, big- and little-endian NDR | ✅ | ✅ | all |

### Records

| Record | Read | Write | Interop |
|--------|:---:|:---:|---------|
| User records of the model (0x0000–0x7FFF), application records (Handler) | ✅ | ✅ | p-net, pnio-rs, profinet-py |
| ReadImplicit (no AR) | ✅ | n/a | profinet-py, p-net |
| WriteMultiple (0xE040) | ✅ | ✅ | p-net |
| I&M0 (0xAFF0), I&M1–4 (0xAFF1–0xAFF4), I&M0FilterData (0xF840) | ✅ | ✅ (I&M1–4) | p-net, pnio-rs, profinet-py |
| RealIdentificationData (0xF000, 0xC001, 0x8001), ExpectedIdentificationData | ✅ | n/a | p-net, profinet-py |
| ModuleDiff (0xE002) | ✅ | n/a | profinet-py |
| Diagnosis: channel, all, maintenance and qualified (0x800A–0x800C, 0xC00A–0xC00C, 0xE00A–0xE00C, 0xF00A–0xF00C, 0xF80C) | ✅ | n/a | p-net |
| PDPortDataReal (0x802A), PDRealData (0xF841) | ✅ | n/a | p-net (decoded) |
| RecordInputData (0x8028), LogBook (0xF830, without entries: Wireshark and p-net disagree on its time stamps) | ✅ | n/a | |
| PDPortDataCheck with CheckPeers (0x802B): written before PrmEnd and in Data; remote mismatch diagnoses 0x8001 with 0x8000 (name of station), 0x8001 (port), 0x8005 (no peer) | ✅ (`PeerChecks`) | ✅ | p-net (decodes our record, evaluates it, reports the same diagnoses: `TestPnetPortCheck`) |
| PDPortDataAdjust (0x802F): PeerToPeerBoundary, DCPBoundary | ✅ (encode) | ✅ (applies LLDP off, Hello off) | |
| PDInterfaceAdjust (0x8071), PDInterfaceDataReal (0x8080, in PDRealData), PDExpectedData (0xF842) | ✅ (decode) | ✅ | |
| Topology verification from PDRealData (`CheckTopology`) | ✅ | n/a | |
| MRP records: PDInterfaceMrpDataAdjust/Real (0x8052/0x8050), PDPortMrpDataAdjust/Real (0x8053/0x8054); PDInterfaceMrpDataCheck refused | ✅ (`DeviceConfig.MRP`) | ✅ (multiport devices) | tshark decodes every field we encode (`TestCodecDecoders`) |
| Multiport devices: port data, LLDP and peer checks per port (`l2/bridge`) | ✅ (`CheckTopology` over a line) | ✅ (`Config.Bridge`) | own e2e only (`TestMultiportLine`); no open multiport PROFINET device |
| SNMP (read-only v1/v2c agent: MIB-II system, interfaces, snmp group, LLDP-MIB with stable lldpRemIndex and TimeFilter time marks) | — | ✅ (`Config.SNMP`, opt-in) | Net-SNMP: walks (GetNext, GetBulk, v1, v2c agree), errors, limits, malformed datagrams, a flood, changing LLDP neighbors (`TestNetSNMP*`); tshark and scapy decode every datagram of a walk (`TestSNMPDecoders`) |
| Other checks and adjustments (MAU type, link state, line delay, domain/multicast boundaries), PDIRData | — | refused (0xA9) | |

### Alarms (RTA)

| Feature | Controller | Device | Interop |
|---------|:---:|:---:|---------|
| High and low priority (FrameID 0xFC01/0xFE01, 802.1Q priority 6/5) | ✅ | ✅ | p-net, profinet-py |
| Sequence numbers from 0xFFFF with 15-bit wrap, ACK, retransmission, window 1, ERR (abort) | ✅ | ✅ | p-net, pnio-rs, profinet-py |
| AlarmNotification and AlarmAck | ✅ (acknowledges) | ✅ (waits for the AlarmAck) | p-net, profinet-py |
| Diagnosis appears/disappears, process alarm, plug/pull/other types | ✅ (receive any) | ✅ (`AddDiagnosis`, `RemoveDiagnosis`, `SendAlarm`) | p-net, profinet-py |

### Cyclic data (RT)

| Feature | State | Interop |
|---------|:---:|---------|
| RT_CLASS_1 (FrameID 0xC000–0xF7FF) and RT_CLASS_2 unsynchronized (0x8000–0xBBFF) | ✅ | p-net, pnio-rs, profinet-py |
| SendClockFactor 1–128 (powers of two, from the device's MinDeviceInterval) × ReductionRatio 1–512 | ✅ | p-net (1 ms), profinet-py (32 ms) |
| IOPS, IOCS, DataStatus, cycle counter, watchdog and data hold factors | ✅ | all |
| 802.1Q priority 6 tag; untagged and priority-tagged reception | ✅ | all |
| RT_CLASS_3 (IRT), RT over UDP, multicast CRs (MCR), redundant frames | — | |

### GSDML

| Feature | State | Interop |
|---------|:---:|---------|
| Parse GSDML 2.25–2.45 (UTF-8, ISO-8859-1), keep unknown content | ✅ | pnio-rs's GSDML |
| Validate against the catalog (structure, types, references) | ✅ (not PI's checker; see [docs/GSDML.md](docs/GSDML.md)) | |
| Model from GSDML (DAP, modules, submodules, IO data, records) | ✅ | pnio-rs (its GSDML drives the controller) |
| GSDML from a model | ✅ | profinet-py (parses it and connects with it) |
| Check: errors, warnings and unvalidated content with their rules (`gsdml.Check`) | ✅ (not PI's checker) | |
| GSDX: the public OPC container (parts, relationships, signature metadata, GSDML parts), `gsdml/gsdx` | ✅ inspector (signatures not verified; PI's rules and MRCD are members only) | |

## Interop matrix

The suite in [interop/](interop/) runs go-profinet against the reference
implementations of
[otfabric/profinet-interop](https://github.com/otfabric/profinet-interop) on
Docker bridge networks, over the AF_PACKET backend and the kernel's UDP
stack. Every exchange is captured and decoded by scapy and tshark; a frame
either cannot decode, or that tshark warns about, fails the test unless it
is an attributed deviation of the peer.

| Peer | Version | go-profinet as | pass | fail | n/a | peer deviation |
|------|---------|----------------|-----:|-----:|----:|---------------:|
| rt-labs p-net | `ea5e554` | IO-Controller, DCP requester, LLDP agent | 56 | 0 | 1 | 0 |
| pnio-rs | v0.1.0 | IO-Controller, DCP requester | 13 | 0 | 3 | 0 |
| profinet-py | 0.6.6 | IO-Device, DCP responder | 32 | 0 | 1 | 3 |
| pnio-dcp | 1.2.0 | DCP responder | 8 | 0 | 0 | 0 |
| umrp | v4.0 | MRP manager, client and MRA | 32 | 0 | 0 | 2 |
| microchip-ung/mrp | `177717e` | MRP manager, client and MRA (kernel, micro-VM) | 31 | 0 | 1 | 0 |
| Net-SNMP | 5.9.3 | SNMP agent of the IO-Device | 29 | 0 | 0 | 0 |
| scapy | 2.6.1 | (decoder of every capture) | 20 | 0 | 0 | 7 |
| tshark | 4.0.17 | (decoder of every capture) | 19 | 0 | 0 | 0 |
| tshark and scapy | | (decoders of our Hello, SNMP, UDP-RT and DHCP frames) | 8 | 0 | 0 | 2 |

The full matrix, feature by feature, with the evidence of each check, is
[profinet-interop's MATRIX.md](https://github.com/otfabric/profinet-interop/blob/v0.4.1/docs/MATRIX.md)
(248 pass, 0 fail, 6 n/a, 14 deviations; the Interop workflow on amd64,
the mrp rows from the Kernel MRP workflow's micro-VMs); the notes on each
peer are in its
[INTEROPERABILITY.md](https://github.com/otfabric/profinet-interop/blob/v0.4.1/docs/INTEROPERABILITY.md).
The "n/a" are peer limits: pnio-rs has no diagnosis records and no DCP Set,
p-net and pnio-rs refuse PrmBegin in Data, profinet-py ignores the GSDML's
UsedInSlots, and the kernel flushes its FDB where the mrp adapter cannot
observe it. The deviations are profinet-py's I&M1 write with BlockLength
58, umrp's MRA reclaiming the manager role as a client, and decoder
limits: scapy has no MRP and no DCP DHCP block class, and Wireshark
decodes the data of DHCP options 61 and 255 only.

### Run it

```bash
make interop-images   # clones otfabric/profinet-interop (or uses ../profinet-interop) and builds its images
make interop          # runs the suite; results in interop-results/
make interop-matrix   # regenerates profinet-interop's matrix from them
```

The **Interop** workflow does the same on GitHub Actions for pull requests,
for pushes to `main` and weekly. The microchip-ung/mrp rows need a kernel
with bridge MRP and without switchdev; the **Kernel MRP** workflow (same
triggers, amd64 with KVM) builds one and runs those tests in micro-VMs:

```bash
make -C ../profinet-interop image-mrp image-umrp image-scapy image-tshark
scripts/microvm-interop.sh "$(scripts/mrp-kernel.sh v6.12 /tmp/k)" 'TestMRPOtfManager/^mrp$'   # needs virtme-ng and /dev/kvm
```

### What the tests found

The interop tests changed go-profinet where it disagreed with every
reference implementation (the standard is not public; the stacks agree with
each other):

- **p-net ties an AR to the RPC activity of its Connect.** A PrmEnd on
  another activity is acknowledged but never processed. Every call of an AR
  now uses the activity of its Connect.
- **p-net starts its input provider only after PrmEnd.** A consumer watchdog
  that runs from the Connect expires; it is now armed when the AR is
  established.
- **p-net does not process DCE/RPC ACK packets.** PNIO calls are now
  idempotent and send none.
- **The first alarm DATA PDU is numbered 0xFFFF** by p-net, pnio-rs and
  profinet-py; go-profinet expected 0.
- **p-net does not answer DCP Get with the AllSelector** and ignores another
  requester for 3 s after a Get or Set; the requester asks for the blocks by
  name and retransmits three times.
- **WriteMultiple answers start with the IODWriteResHeader of the
  WriteMultiple itself** (index 0xE040), then one per item. go-profinet's
  device omitted it and its controller counted it as an item; both now
  follow p-net (and Wireshark's dissector).
- **A Linux bridge with LLDP forwarding (Docker Desktop) returns LLDP
  frames to their sender**: p-net then lists itself as the neighbor of its
  port. go-profinet's LLDP agent now ignores frames of its own address.
- **pnio-rs accepts only RT_CLASS_2 FrameIDs**: a controller must use the
  advanced startup with it.

## Discrepancies of the peers

Recorded, not worked around in go-profinet:

| Peer | Discrepancy |
|------|-------------|
| p-net | plugs any module the controller expects (logged as "unknown") instead of reporting it in a ModuleDiffBlock, so a wrong module cannot be detected against p-net; answers an unknown record index with `DE 80 A0 00` (application read error) rather than invalid index; the veth speed check has to be patched out of its Connect; the tested tree predates its v1.0.2 security fixes |
| profinet-py | writes I&M1 with BlockLength 58 and two padding octets (the block is 56); `read_all_diagnosis` reads 0xF000 and decodes it as diagnosis |
| pnio-rs | answers diagnosis indices and I&M4 with invalid index; rejects a Connect without exact ModuleDiff agreement |

## Not covered

There is no second independent IO-Controller with an open licence:
go-profinet's IO-Device is tested by profinet-py (and by go-profinet's own
controller). CODESYS Control for Linux SL in its demo mode is the one
candidate an engineer can run by hand; the recipe, with what its licence
allows, is [docs/research/INDEPENDENT-CONTROLLER.md](docs/research/INDEPENDENT-CONTROLLER.md)
(not run yet). No commercial PLC or device and no engineering tool has
been tested; that is the hardware tier of [docs/TESTING.md](docs/TESTING.md).
