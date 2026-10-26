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
| Hello | ✅ (receive) | — (no fast startup) | |
| Control/Start and Control/Stop around a Set | ✅ | ✅ | |
| DHCP options | — | — | |
| Requester lock of another requester (3 s) | — | ✅ | p-net (as the locking side) |

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
| PrmBegin (re-parameterization in Data) | ◐ (`RecordClient.Control`; not in the lifecycle) | ✅ | |
| Release, abort, CM initiator activity timeout | ✅ | ✅ | p-net, pnio-rs |
| ARRPCBlockReq (controller RPC port) | ✅ | ✅ | |
| Several ARs on one device (`MaxARs`), one controller AR per submodule | — | ✅ | profinet-py (IOSAR + IOCAR) |
| Shared device, IOCARSR (system redundancy), MCR, IRInfo, PrmServer, AR vendor blocks | — | — (rejected) | |
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
| RecordInputData (0x8028), LogBook (0xF830, without entries) | ✅ | n/a | |
| PDPortDataCheck, PDPortDataAdjust, PDInterfaceDataReal, PDExpectedData, PDIRData, MRP records | — | — | |

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
| GSDX, MRCD | — (formats not public) | |

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
| rt-labs p-net | `ea5e554` | IO-Controller, DCP requester, LLDP agent | 45 | 0 | 0 | 0 |
| pnio-rs | v0.1.0 | IO-Controller, DCP requester | 12 | 0 | 2 | 0 |
| profinet-py | 0.6.6 | IO-Device, DCP responder | 22 | 0 | 0 | 2 |
| pnio-dcp | 1.2.0 | DCP responder | 8 | 0 | 0 | 0 |
| scapy | 2.6.1 | (decoder of every capture) | 4 | 0 | 0 | 0 |
| tshark | 4.0.17 | (decoder of every capture) | 4 | 0 | 0 | 0 |

The full matrix, feature by feature, with the evidence of each check, is
[profinet-interop's MATRIX.md](https://github.com/otfabric/profinet-interop/blob/v0.1.0/docs/MATRIX.md);
the notes on each peer are in its
[INTEROPERABILITY.md](https://github.com/otfabric/profinet-interop/blob/v0.1.0/docs/INTEROPERABILITY.md).
The two "n/a" are pnio-rs limitations (no diagnosis records, no DCP Set); the
two deviations are profinet-py's I&M1 write with BlockLength 58.

### Run it

```bash
make interop-images   # clones otfabric/profinet-interop (or uses ../profinet-interop) and builds its images
make interop          # runs the suite; results in interop-results/
make interop-matrix   # regenerates profinet-interop's matrix from them
```

The **Interop** workflow does the same on GitHub Actions for pull requests,
for pushes to `main` and weekly.

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
controller). No commercial PLC or device has been tested; that is the
hardware tier of [docs/TESTING.md](docs/TESTING.md).
