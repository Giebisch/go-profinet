# Shared device / multiple ARs, system redundancy (IOCARSR), MCR, RT_CLASS_UDP — public-source evidence

Research date 2026-10-27. Facts only; no code copied.

## Sources (pinned)

| Tag | Source | Version |
|---|---|---|
| WS-IO | wireshark `plugins/epan/profinet/packet-dcerpc-pn-io.c` (GPLv2) | master; last commit touching the file 5680d76136febe327fdccf7b399d182a96b34938 (2026-09-19) |
| WS-RT | wireshark `plugins/epan/profinet/packet-pn-rt.c` | master, same fetch |
| WS-PN | wireshark `plugins/epan/profinet/packet-pn.c` | master, same fetch |
| PNET | rtlabs-com/p-net (GPLv3) | ea5e5540b2be55c4dfadffda39b948632c104daa |
| PPY | f0rw4rd/profinet-py | main @ ef09fb7788cfd86c2e6ee1b9bcf5165f5aac03c4 |
| PNRS | core-engineering/pnio-rs | main @ 276202284f38a7a66878ec68f11381fb33b8c0f5 |
| PI-8.132 | PI "PROFINET Planning Guideline Redundancy", Order No. 8.132, V1.00, May 2023 (public PDF, se.profibus.com/fileadmin/media/downloadsection/installation_guide/PROFINET_Planning_Redundancy_8132_V100_May_23.pdf), §3.3 pp. 38-42 | V1.00 |
| PI-CS | https://www.profinet.com/profinet-explained/community-stack | fetched 2026-10-27 |

Line numbers refer to the pinned revisions listed above.

Labels: **VERIFIED** means the fact is read directly from the cited source location. **OBSERVED-IN-IMPLEMENTATION** means it is how one implementation behaves, which is not necessarily the norm. **INFERRED** means it is deduced from several facts. **UNVERIFIED** means it comes from memory or a secondary source with no public primary source found.

---

## A. Shared device / multiple IOCARs

### A.1 Ownership model

- **VERIFIED (WS-IO 1853-1955, 21294-21335).** SubmoduleState (u16) with FormatIndicator=1 has this layout:
  - bits 0-2 AddInfo: 0 None, 1 "Takeover not allowed".
  - bit 3 Advice.
  - bit 4 MaintenanceRequired.
  - bit 5 MaintenanceDemanded.
  - bit 6 Fault.
  - **bits 7-10 ARInfo**: 0 Own, 1 ApplicationReadyPending, 2 Superordinated Locked, 3 Locked By IO Controller, 4 Locked By IO Supervisor, 5-15 reserved.
  - bits 11-14 IdentInfo: 0 OK, 1 Substitute, 2 Wrong, 3 NoSubmodule.
  - bit 15 FormatIndicator.

  The legacy coding (FormatIndicator=0) uses Detail in bits 0-14: 0 no submodule, 1 wrong, 2 locked by IO controller, 4 application ready pending, 7 substitute.
- **VERIFIED (PNET pf_types.h 1338-1349, 1507-1517).** p-net has the same enum (`PF_AR_INFO_OWN`…`LOCKED_BY_IO_SUPERVISOR`) and the same bit positions. It always sets format_indicator=1.
- **OBSERVED-IN-IMPLEMENTATION (PNET doc/shared_device.rst 1-25; pf_cmdev.c 3746-3752).** Ownership is per submodule and first come, first served. On Connect, each expected submodule whose `ownsm_state==FREE` becomes owned by the new AR (state IOC). An already-owned submodule is **not** rejected: the Connect succeeds and the submodule is reported in the ModuleDiff.
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmdev.c 4374-4383 and 4549-4558).** When the ModuleDiff is built, a submodule whose real ident matches but is owned (IOC or IOS) by another AR gets IdentInfo=OK, ARInfo=3 (LockedByIOController) and is listed in the diff. p-net uses value 3 even when the owner is a supervisor; value 4 is never used. A wrong ident gives ARInfo=Own, IdentInfo=Wrong. A wrong module gives ModuleState=Substitute, with every submodule set to IdentInfo=Wrong (comment cites "Profinet 2.4 services, Table 214").
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_ppm.c 446-455, pf_cpm.c 343-350; pf_ppm.c 337).** Application data and IOPS writes (and output/IOCS reads) for a subslot go only to the **owner** AR's IOCR. The other AR's frame buffer for that subslot stays at its default: data zero and IOxS = 0x00 (BAD).
  - **INFERRED:** in a non-owner AR, a locked submodule's IOPS (inputs) and IOCS (for outputs) are BAD.
  - **UNVERIFIED:** whether the spec requires exactly BAD with a specific detection value, such as 0x00 or "by IO device" 0x40.
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmdev.c 951-1000; pf_plugsm.c 28-105; pnet_api.h 640-673).** On release or abort of AR X, each submodule owned by X is set FREE. It is then handed to the **first AR in connect order** (`cmrpc_ar_order`) that has it in its expected config. If the module and submodule idents match, a **"Released" alarm** (AlarmType 0x0009, WS-IO 1727) is queued to that AR. The application gets `pnet_sm_released_ind` and must refresh input data, IOPS and IOCS, then call `pnet_sm_released_cnf`.
  - **UNVERIFIED:** the spec flow after Released (the controller re-parameterises with Write, then the device sends ApplicationReady through the plug state machine). p-net routes it through PLUGSM state `WFPRMIND`, the same path as a plug alarm.
- **VERIFIED (WS-IO 1725-1731).** Related AlarmTypes: 0x0007 Redundancy, 0x0008 Controlled by supervisor, 0x0009 Released, 0x000D Multicast communication mismatch notification.
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmdev.c 4745-4775).** If the CMSU start fails (for example, PDEV already in use), Connect is rejected with ErrorCode1=0x40 (CMRPC), ErrorCode2=0x09 "PDEV already owned" (constant pnet_api.h 249).
- **UNVERIFIED:** the normative rule that only one AR may own the PDEV/interface submodules (0x8000…).

### A.2 Shared input

- **VERIFIED (WS-IO 21254-21285, 2041-2045).** ExpectedSubmoduleBlockReq SubmoduleProperties (u16):
  - bits 0-1 Type.
  - **bit 2 SharedInput** (0 "IO controller", 1 "IO controller shared").
  - bit 3 ReduceInputSubmoduleDataLength.
  - bit 4 ReduceOutputSubmoduleDataLength.
  - bit 5 DiscardIOXS.
  - bits 6-15 reserved.
- **INFERRED.** Shared input is signalled per submodule in the ExpectedSubmoduleBlockReq. No ARProperties or IOCRProperties bit for it exists in WS-IO. A second AR reads the same inputs through its own unicast Input CR. It is not an IOCR-level multicast (MCR is a separate mechanism, section C).
  - **UNVERIFIED:** the exact SubmoduleState/ARInfo reported for a shared-input submodule in the non-owner AR, and whether IOPS there is GOOD.
- **OBSERVED-IN-IMPLEMENTATION (PNET doc/shared_device.rst 25; pf_cmdev.c 3600-3609).** p-net "does not yet support" shared input. It only rejects SharedInput=1 on an output-type submodule (ErrorCode1=0x03 ExpectedSubmoduleBlockReq, ErrorCode2=12). The ownership code ignores the bit.

### A.3 ARProperties (u32, BE) — VERIFIED (WS-IO 1853-1955, 20426-20487, 21420-21425; dissect order 13312ff)

| Bits | Mask | Field | Values |
|---|---|---|---|
| 0-2 | 0x00000007 | State | 1 Active; others reserved |
| 3 | 0x00000008 | SupervisorTakeoverAllowed | 0 not allowed, 1 allowed |
| 4 | 0x00000010 | ParameterizationServer | 0 External PrmServer, 1 CM Initiator |
| 5-7 | 0x000000E0 | Reserved_1 (formerly DataRate, "REMOVED with 2.3") | |
| 8 | 0x00000100 | DeviceAccess | 0 only ExpectedSubmodule submodules, 1 access controlled by device application |
| 9-10 | 0x00000600 | CompanionAR | 0 Single, 1 first of pair, 2 companion, 3 reserved |
| 11 | 0x00000800 | AcknowledgeCompanionAR | |
| 12-26 | 0x07FFF000 | Reserved | |
| 27 | 0x08000000 | RejectDCPsetRequests | |
| 28 | 0x10000000 | TimeAwareSystem | |
| 29 | 0x20000000 | CombinedObjectContainer | legacy startup: must be 0; advanced: 1 = required |
| 30 | 0x40000000 | StartupMode | 0 Legacy, 1 Advanced |
| 31 | 0x80000000 | PullModuleAlarmAllowed | |

- **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmdev.c 1676-1800).** ARBlockReq checks, each failing with ErrorCode1=0x01 (FAULTY_AR_BLOCK_REQ) and the ErrorCode2 shown:

  | Check | ErrorCode2 |
  |---|---|
  | ARType not supported | 4 |
  | ARUUID all zero | 5 |
  | CMInitiatorMacAdd is multicast | 7 |
  | CMInitiatorObjectUUID wrong | 8 |
  | State≠1, PrmServer=External, DeviceAccess=1 with ARType≠IOSAR, or CompanionAR=3 | 9 |
  | ActivityTimeoutFactor outside 1..1000 | 10 |
  | StationName length/charset wrong | 12 |

- **OBSERVED-IN-IMPLEMENTATION (PPY rpc.py 1502-1545).** profinet-py sends ARProperties 0x00000011 (IOCAR) and 0x00000111 (IOSAR/DeviceAccess). Its comments reference the same bit layout.

### A.4 ARType, release/abort, same-controller reconnect

- **VERIFIED (WS-IO 1807-1821, 14682-14701).** ARType values:
  - 0x0001 IOCARSingle ("IO Controller AR").
  - 0x0003 IOCARCIR.
  - 0x0006 IOSAR: "IO Supervisor AR", or "DeviceAccess AR" when ARProperties.DeviceAccess=1.
  - 0x0010 IOCARSingle using RT_CLASS_3.
  - 0x0020 "IO Controller AR (sysred/CiR)" = IOCARSR.
  - All others reserved.
  - PNET pf_types.h 1218-1224 names them PF_ART_IOCAR_SINGLE, IOSAR, IOCAR_SINGLE_RTC_3, IOCAR_SR ("Set of AR").
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmrpc.c 1975-2025).** A Connect whose ARUUID equals an existing AR's ARUUID is rejected with ErrorCode1=0x3D (CMDEV), ErrorCode2=0x00 (state conflict). The existing AR is kept, and if the request came on a different RPC session, that session is killed.
  - p-net does **not** match a new AR to an old one by CMInitiatorObjectUUID, MAC or station name. A reconnecting controller with a fresh ARUUID gets a second AR slot, if one is free; otherwise ErrorCode2 0x04 "no AR resources" (CMRPC 0x40).
  - **UNVERIFIED:** the normative "same initiator replaces AR" rule. No public source was found.
- **OBSERVED-IN-IMPLEMENTATION (PPY rpc.py 1480-1483).** profinet-py makes a new random ARUUID, activity UUID and session key on each reconnect.

### A.5 Interop feasibility

- **VERIFIED (local repo).** `profinet-interop/adapters/pnet/Dockerfile:43` builds pn_dev with `-DPNET_MAX_AR=2`. The optional build arg ACCEPT_IOSAR=1 patches `pf_cmdev_check_ar_type` to also accept 0x0006. PNET CMakeLists.txt 72 documents ">1 supports shared device".
- **OBSERVED-IN-IMPLEMENTATION (PNET sampleapp_common.c 127-143, 304-323).** The sample app keeps per-AREP state (`main_api.ar[PNET_MAX_AR]`) and implements `sm_released_cb`. Some paths still use `ar[0]` only, such as the LED/abort demo at lines 1304, 1455 and 1473.
  - The sample GSDML has SharedDeviceSupported commented out (lines 28-64). That affects engineering tools only, not the wire.
- **INFERRED.** The image should accept two concurrent IOCARSingle ARs from two controllers. With ACCEPT_IOSAR=1, one IOCAR plus one IOSAR uses up both slots.
  - Constraint: p-net does not check that a controller-proposed **input** FrameID is unique across ARs. `pf_cmdev_verify_free_frame_id` (pf_cmdev.c 4601-4622) is used only to pick an output FrameID when the controller sends 0xFFFF (4640-4690).
- **OBSERVED-IN-IMPLEMENTATION (PPY rpc.py 473-540, 995, 1243-1257; cyclic.py 851-870).**
  - `IOCRSetup.slots` is a free list, so a subset of submodules works.
  - The input CR FrameID is proposed as `0xC000 + iocr_reference`, with the counter starting at 1. Two instances therefore both propose 0xC001.
  - The RX filter matches only device source MAC and FrameID, not destination MAC.
  - CMInitiatorObjectUUID is a constant (prefix + 00 01 76 54 32 10), and the station name is fixed to "tp".
- **INFERRED.** Run two profinet-py instances only in **separate network namespaces/containers with distinct MACs**, so each socket sees only frames unicast to its own MAC. In one namespace they would share a MAC, which looks like one controller, and would cross-receive each other's 0xC001 input frames.
  - profinet-py marks cyclic IO "EXPERIMENTAL, NOT TESTED with real devices" (rpc.py ~1495).
  - Not executed, so not proven.
- **OBSERVED-IN-IMPLEMENTATION (PNRS connect.rs ~240).** pnio-rs (device) accepts only ARType 1 and a single AR. It cannot serve as a shared-device oracle.

---

## B. System redundancy S2 (IOCARSR)

### B.1 Wire elements

- **VERIFIED (WS-IO 14752-14770, 20941-20970, 4182-4196).** For ARType 0x0020, Wireshark decodes the ARUUID as a structured value:
  - Discriminator: 6 bytes.
  - ConfigID: 8 bytes.
  - Selector u16:
    - ARnumber (mask 0x0007): 1-4 = "1st…4th AR of an ARset".
    - ARresource (mask 0x0018): 2 = "Communication endpoint shall allocate two ARs for the ARset".
    - ARreserved (0xFFE0).
  - **INFERRED:** the ARs of one ARset share Discriminator and ConfigID and differ in ARnumber.
- **VERIFIED (WS-IO 1389, 15655-15699, 20576-20614, 1993-2004, 4175-4180).** SRInfoBlock, BlockType **0x010A**, version 1.0, is sent in the Connect request. Body:

  | Offset | Size | Field |
  |---|---|---|
  | 0 | u16 | RedundancyDataHoldFactor (0-2 reserved; 3-0xC7 "Optional – expiration leads to AR termination"; ≥0xC8 "Mandatory – …") |
  | 2 | u32 | SRProperties |

  SRProperties bits:
  - bit 0 InputValidOnBackupAR.
    - With Mode=0: 1 = "device shall deliver valid input data", 0 = "IO controller shall not evaluate the input data".
    - With Mode=1: 0 = device marks the data invalid through DataStatus.DataValid.
  - bit 1 Reserved_1 ("Legacy mode" / must be 0).
  - bit 2 Mode.
  - bits 3-15 and 16-31 reserved.
  - Connect error: ErrorCode1=0x0A "Faulty SRInfoBlock" (WS-PN 306); ErrorCode2 4 = RedundancyDataHoldFactor, 5 = SRProperties (WS-PN 708-716).
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_types.h 1629-1640).** The p-net comment gives "res: 1 ms" for RedundancyDataHoldFactor, with ranges that **differ** from WS (1..0xC8 optional). Treat the units as UNVERIFIED; the WS ranges are the better-sourced ones.
- **VERIFIED (WS-IO 1354, 13724-13741, 20491-20500, 1945-1951).** SRLData, BlockType 0x001B, is a record: RedundancyInfo u16, where bits 0-1 EndPoint are 1 = left/below node and 2 = right/above node; then padding to 4. User structure 0x4003 is "RS_SRLObserver" (WS-IO 3939). Not needed for S2 device basics.
- **VERIFIED (WS-RT 118-145, 174-290, 1205-1280).** The APDU DataStatus byte (u8) carries the redundancy state:

  | Bit | Mask | Meaning |
  |---|---|---|
  | 0 | 0x01 | State: 1 Primary, 0 Backup |
  | 1 | 0x02 | Redundancy |
  | 2 | 0x04 | DataValid |
  | 3 | 0x08 | reserved |
  | 4 | 0x10 | ProviderState (Run) |
  | 5 | 0x20 | StationProblemIndicator (1=OK) |
  | 6 | 0x40 | reserved |
  | 7 | 0x80 | Ignore |

  Function/Meaning is decoded from mask 0x07 when the AR is IOCARSR:
  - **Output CR (controller→device):** 0x05 "Primary Request" and 0x04 "Backup Request". This is how the controller tells the device which AR is primary. Redundancy has no meaning for Output CRs and is 0.
  - **Input CR (device→controller):**
    - 0x05 Primary Acknowledge.
    - 0x04 Backup Acknowledge with actual data.
    - 0x00 Backup Acknowledge without actual data.
    - 0x06 / 0x02 Primary Missing, with or without actual data.
    - 0x07 Primary Fault.
  - Redundancy bit when the input State is Backup: 1 = "None primary AR of a given AR-set is present".
  - Redundancy bit when the input State is Primary: 1 = "The ARState from the IO device point of view is Backup".
  - A 2019 WS commit swapped these values to match the spec (lists.wireshark.org wireshark-commits 201911 msg00256).
- **VERIFIED (WS-PN 875-886, 1140-1160, 1225-1245/1290-1300).** Error and abort codes:
  - AlarmAck ErrorCode2 2 = "IOCARSR Backup – Alarm not executed".
  - Write ErrorCode2 1 = "AR is not in state Primary" (table pnio_204).
  - AR abort reasons (RTA error code 0xFD / pnio_253):
    - 38 IOCARSR RDHT expired.
    - 39 IOCARSR PDEV parameterization impossible.
    - 41 redundant interface lost or no access to the peripherals.
    - 42 IOCARSR MTOT expired.
    - 43 IOCARSR AR protocol violation.
- **VERIFIED (WS-IO 1401-1404, 21057-21090, 3682-3691).** Related blocks:
  - IOXControlReq "Ready for Companion" 0x0116.
  - PrmBeginReq 0x0118 (ControlCommand bit 6, 0x0040, "IO controller starts transmission of stored start-up parameter").
  - SubmoduleListBlock 0x0119.
  - **UNVERIFIED:** that PrmBegin/SubmoduleList are the S2 re-parameterisation path for a backup taking over.
- **UNVERIFIED (not found in WS):** an "ARSetBlock", or a separate "RedundancyInfo" field in the IOCRBlockReq. WS shows no S2-specific IOCR field.

### B.2 Device obligations

- **VERIFIED (PI-8.132 §3.3.2, pp. 38-40).**
  - S2: one interface module holds an AR to each controller of a redundant pair.
  - The active AR1a does the data exchange. The backup AR1b "is in a standby mode" until failure of AR1a is detected, followed by a "bumpless switchover" to the backup.
- **INFERRED from WS-RT function meanings plus SRProperties:**
  - The device applies outputs only from the AR whose Output CR carries State=Primary.
  - It answers each Input CR with Primary Ack or Backup Ack.
  - Input validity on the backup follows SRProperties.InputValidOnBackupAR and Mode.
  - If no AR is primary, the device keeps outputs for RedundancyDataHoldTime (RDHT, factor × unit) and then substitutes or aborts (abort reason 38).
  - Writes and alarms through a backup AR are refused (WS-PN error codes above).
  - Exact timer units, the order of the state machine, and MTOT semantics are **UNVERIFIED**.

### B.3 S1/S2/R1/R2

- **VERIFIED (PI-8.132 §3.3.1-3.3.4, pp. 38-42).** The letter gives the number of interface modules / networks (S = single, R = redundant). The digit gives the maximum number of ARs between the nodes.
  - S2: one interface module, two ARs (one per controller); a network failure is not compensated.
  - R1: two interface modules, each with one AR to a different controller, over two separate networks.
  - R2: two interface modules, each with ARs to both controllers (four ARs); on failure, switchover to any of AR1b, AR2a or AR2b.
- Public guideline PI-8.131 is the same document in German.

### B.4 Open oracles

- **OBSERVED-IN-IMPLEMENTATION.**
  - p-net accepts only ARType 0x0001 (pf_cmdev.c 1654-1664). It has no SRInfoBlock case in the Connect parser, so it falls to the default and returns CMRPC "unknown blocks" (pf_cmrpc.c 1645-1658). `pf_put_sr_info` is a stub ("Not yet implemented", pf_block_writer.c 894-903).
  - pnio-rs: ARType 1 only.
  - profinet-py: sends ARType 1 or 6 only.
- **VERIFIED (PI-CS).** The PROFINET Community Stack supports S2, but it is "provided free of charge to members of PI" under the PNO Community Source Code License. That is not usable under this project's constraints.
- **No public S2 pcap was found** (web search; the Wireshark wiki/bug tracker were not exhaustively checked; bug 13456 attachments may contain one, UNVERIFIED).
- **Result:** no usable independent oracle. Wireshark (a decoder only) is the sole cross-check.

---

## C. MCR (multicast communication relation)

- **VERIFIED (WS-IO 1385, 15440-15474, 21394-21408).** MCRBlockReq, BlockType **0x0106**, version 1.0. Body:

  | Offset | Size | Field |
  |---|---|---|
  | 0 | u16 | IOCRReference |
  | 2 | u32 | AddressResolutionProperties (not bit-decoded by WS) |
  | 6 | u16 | MCITimeoutFactor |
  | 8 | u16 | StationNameLength |
  | 10 | n | ProviderStationName (ASCII) |

  - ErrorCode1 0x06 = "Faulty MCRBlockReq" (WS-PN 302).
- **CONFLICT / OBSERVED-IN-IMPLEMENTATION (PNET pf_block_reader.c 655-678, pf_types.h 1251-1255, 1577-1590).** p-net reads AddressResolutionProperties as **two bytes**: u8 protocol (enum DNS=0, DCP=1) and u8 resolution_factor (1..0x64). WS reads a u32. The two disagree by 2 bytes.
  - **INFERRED:** WS is more likely right, since its offsets match its own block length.
  - **UNVERIFIED:** the bit layout. From memory: Protocol in the low bits (DNS/DCP), Factor in the high 16 bits.
- **VERIFIED (WS-IO 1822-1830).** IOCRType: 1 Input, 2 Output, 3 Multicast Provider, 4 Multicast Consumer.
- **VERIFIED (WS-IO 14995-14999).** IOCRBlockReq carries IOCRMulticastMACAdd, 6 bytes, after IOCRTagHeader.
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmdev.c 3296-3305).** For MC CRs the multicast MAC must have the group bit set (ErrorCode1=0x02, ErrorCode2=18).
- **FrameID ranges for multicast.**
  - **VERIFIED (WS-RT around 850-895):** 0xBC00-0xBFFF RT class-1 multicast; 0xF800-0xFBFF multicast cyclic (RT on Ethernet, or UDP multicast when the destination port is 0x8892).
  - **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmdev.c 3124-3140):** for MC CRs, RTClass 1(legacy)/UDP use 0xF800-0xFBFF, RTClass 2 uses 0xBC00-0xBFFF, RTClass 3 uses 0x0100-0x7FFF.
- **VERIFIED (WS-IO 2520, 3033-3040).** Diagnostics: ChannelErrorType 0x8005 "Multicast CR mismatch", with ExtChannelErrorType 0x8000 "Multicast Consumer CR timed out" and 0x8001 "Address resolution failed". AlarmType 0x000D (WS-IO 1731).
- **VERIFIED (WS-PN 1228, 1283).** Abort reason 28: "AR could not register multicast-mac address".
- **UNVERIFIED:** an RT multicast MAC range 01-0E-CF-00-xx-xx for MC CRs. WS only names 01-0E-CF-00-00-00 (DCP Identify) and 01-0E-CF-00-00-01 (DCP Hello) (WS-IO 3323-3330).
- **UNVERIFIED:** the address-resolution procedure, beyond the field names (the consumer resolves ProviderStationName through DCP or DNS within Factor × 100 ms; MCI timeout).
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmrpc.c 1534-1558, pf_cmdev.c 4225, pf_ppm.c 242).**
  - p-net parses MCR blocks, but its PPM always sends to `cm_initiator_mac_add`, never to IOCRMulticastMACAdd. **INFERRED:** MC provider is not functional in p-net.
  - No open implementation of a working MCR was found.

---

## D. RT_CLASS_UDP

- **VERIFIED (WS-IO 1957-1967, 20642-20664).** IOCRProperties (u32):
  - bits 0-3 RTClass:
    - 1 = "Reserved (legacy)" (RT_CLASS_1 legacy).
    - 2 = RT_CLASS_1/RT_CLASS_STREAM-RT.
    - 3 = RT_CLASS_3.
    - **4 = RT_CLASS_UDP**.
    - 5 = RT_CLASS_STREAM time-aware.
    - 6 = RT_CLASS_SXP.
  - bits 4-12 Reserved1, bits 13-23 Reserved2, bits 24-31 Reserved3.
  - PNET pf_types.h 1242-1249 agrees (UDP=4).
- **VERIFIED (WS-RT 32, 1443, 720, 865-890).** pn_rt is registered on **udp.port 0x8892 (34962)** with the same dissector as Ethertype 0x8892. It reads FrameID at offset 0 of the UDP payload.
  - **INFERRED from that:** the UDP payload = FrameID(2) + C_SDU + CycleCounter(2) + DataStatus(1) + TransferStatus(1), with no Ethernet padding.
  - WS labels 0xC000-0xF7FF "RT_CLASS_UDP unicast" and 0xF800-0xFBFF "UDP multicast".
- **VERIFIED (WS-IO 14826, 14893, 13522-13589).** The ARBlockReq carries CMInitiatorUDPRTPort (u16) and the ARBlockRes carries CMResponderUDPRTPort.
- **VERIFIED (WS-IO 2065-2069, 20836-20838).** AlarmCRProperties bit 1 Transport: 0 RTA_CLASS_1, 1 RTA_CLASS_UDP.
- **OBSERVED-IN-IMPLEMENTATION (PNET pf_cmdev.c 3020-3345; pf_types.h 63; pf_cmdev.c 4845).** Negotiation rules for RT_CLASS_UDP in p-net:
  - IOCR LT must be 0x0800 (IP) for RTClass UDP and 0x8892 otherwise.
  - AlarmCR LT is likewise 0x0800 when transport_udp (pf_cmdev.c 3999-4001).
  - C_SDU length 12..1440 (RTClass 1: 40..1440).
  - ReductionRatio 1..16384, with extra constraints at ≥8192/16384.
  - Data hold time ≤ 61.44 s (RTC1-3: ≤ 1.92 s).
  - **CMInitiatorMacAdd must be all-zero** (ErrorCode1=0x01, ErrorCode2=7).
  - CMInitiatorUDPRTPort must be > 0x03FF (ErrorCode2=11).
  - Responder port = 0x8892.
  - Input FrameID 0xC000-0xF7FF.
  - Comments cite "Profinet 2.4 Protocol table 984".
  - **INFERRED:** p-net validates UDP IOCRs but has no UDP send/receive path in PPM/CPM (no UDP code in pf_ppm.c / pf_cpm.c), so it is not a working oracle.
- **INFERRED:** IP addresses are not in any IOCR field. Endpoints come from the RPC/IP layer (the AR's IP addresses) plus the negotiated UDP RT ports.
- **OBSERVED-IN-IMPLEMENTATION.** profinet-py's protocol.py documents LT "0x8892 or 0x0800 for UDP", and rpc.py 1124 builds LT 0x0800 when a `transport` argument ≠ 0 (an alarm CR path). Its cyclic engine is Ethernet-only (cyclic.py 4-18). pnio-rs is Ethernet-only.
- **Status (UNVERIFIED).** No public PI statement on deprecation was found. A secondary source (profinetuniversity.com, "profinet real time classes") calls RT over UDP an optional, rarely implemented service.

---

## Implementability summary

| Feature | Public evidence | Oracle | Verdict |
|---|---|---|---|
| Shared device (multi IOCAR, per-submodule ownership, ARInfo=LockedByIOC, Released alarm) | Strong: WS field defs + p-net behaviour | p-net image, PNET_MAX_AR=2 | **Implementable**; interop-testable with 2 controllers in separate netns |
| Shared input | Bit only (SubmoduleProperties bit 2) | none (p-net lacks it) | Partially: encode/decode only; semantics UNVERIFIED |
| ARProperties / ARType decode/encode | Strong (WS) | WS, p-net | **Implementable** |
| S2 / IOCARSR | Field layouts (ARUUID selector, SRInfoBlock, DataStatus meanings, abort codes) in WS; topology in PI-8.132 | **none** (Community Stack is members-only) | Codec only; device/controller state machine **blocked** on timer units, switchover sequence, PrmBegin flow, no oracle |
| MCR | Block layout in WS (field disagreement with p-net); diag codes | none working | Codec only; **blocked** on AddressResolutionProperties bits, MC MAC allocation, resolution procedure |
| RT_CLASS_UDP | RTClass=4, port 0x8892, WS dissection, p-net validation rules | WS decode only | Frame codec + Connect param rules implementable; end-to-end interop **blocked** for lack of an implementation peer |
