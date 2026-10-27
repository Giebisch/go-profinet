# PROFINET DCP Hello / FSU and PDev records: research notes

For: github.com/otfabric/go-profinet (MIT). Date: 2026-10-27.
All facts were read from the sources below and recorded as **facts only**. No code was copied, and the byte vectors are synthetic, built from the documented layouts.

## Sources (pinned)

| Tag | Source | Licence | Pin |
|---|---|---|---|
| PNET | rt-labs p-net, https://github.com/rtlabs-com/p-net | GPLv3 | tree `ea5e5540b2be55c4dfadffda39b948632c104daa` |
| WS-DCP | Wireshark `plugins/epan/profinet/packet-pn-dcp.c` | GPLv2+ | commit `452e1fa203141634221b38b9e931dee66686198b` (master, 2026-10-27) |
| WS-IO | Wireshark `plugins/epan/profinet/packet-dcerpc-pn-io.c` | GPLv2+ | same commit |
| WS-PN | Wireshark `plugins/epan/profinet/packet-pn.c` (`dissect_pn_align4`) | GPLv2+ | same commit |
| WS-RT | Wireshark `plugins/epan/profinet/packet-pn-rt.c` | GPLv2+ | same commit |
| PNPY | profinet-py, https://github.com/f0rw4rd/profinet-py, `profinet/dcp.py`, `profinet/indices.py` | GitHub reports licence `NOASSERTION`, so treat it as reference only | `ef09fb7788cfd86c2e6ee1b9bcf5165f5aac03c4` |
| PI-CC | PI "PROFINET IO Conformance Classes Guideline V1.1, Mar 2011" (order 7.042), public download https://se.profibus.com/fileadmin/media/downloadsection/supplementary_document/PNIO-CCs_7042_V11_Mar11.pdf | PI public document | — |
| PLCN | Phoenix Contact PLCnext Engineer help, "Profinet FSU", https://engineer.plcnext.help/latest/Profinet_FSU.htm | vendor doc | — |
| HIL | Hilscher wiki, "FSU - Profinet FastStartUp", https://hilscher.atlassian.net/wiki/spaces/PNS3V5/pages/123208193 | vendor doc | — |

Note: the Wireshark dissectors now live in `plugins/epan/profinet/`, including `packet-pn-dcp.c`. They are no longer in `epan/dissectors/`.

Sources were read at the pinned revisions above and never executed.

Labels: **VERIFIED** (stated directly by the cited source), **OBSERVED-IN-IMPL** (behaviour of one implementation, not necessarily normative), **INFERRED** (my derivation), **UNVERIFIED**.

---

## A. DCP Hello / Fast Startup

### A.1 Hello.req frame

| Item | Value | Label / source |
|---|---|---|
| EtherType | 0x8892 | VERIFIED PNET pf_dcp.c `pf_dcp_hello_req` (uses `PNAL_ETHTYPE_PROFINET`) |
| FrameID | **0xFEFC** | VERIFIED PNET pf_dcp.c:64 `PF_DCP_HELLO_FRAME_ID 0xfefc`. WS-RT ~l.950 labels FRAME_ID_DCP_HELLO "DCP ... hello". PNPY dcp.py:87. The others are 0xFEFD Get/Set, 0xFEFE Identify req and 0xFEFF Identify resp (PNET pf_dcp.c:65-67) |
| Destination MAC | **01:0E:CF:00:00:01** | VERIFIED PNET pf_dcp.c:110-111 `dcp_mc_addr_hello`. PNPY dcp.py:81. WS-IO DCPBoundary bit1 text "Block an outgoing DCP_Hello frame ... with the multicast MAC address 01-0E-CF-00-00-01" (l.3330) |
| Source MAC | device interface MAC | VERIFIED PNET `pf_dcp_hello_req` |
| ServiceID | **0x06** Hello | VERIFIED WS-DCP l.156 `PNDCP_SERVICE_ID_HELLO 0x06`. PNET pf_dcp.c:53 |
| ServiceType | **0x00** (request) | VERIFIED PNET `p_dcphdr->service_type = PF_DCP_SERVICE_TYPE_REQUEST` |
| Xid | p-net sends a constant **1** | OBSERVED-IN-IMPL PNET `htonl(1)`. The spec value is UNVERIFIED |
| Header word at offset 6 | 0x0000 | VERIFIED. WS-DCP `dissect_PNDCP_PDU` treats only Identify-request as a multicast header with ResponseDelay; Hello uses a "reserved16" field. PNET writes 0 |
| DCPDataLength | sum of the blocks, including padding | VERIFIED PNET (`dst_pos - dst_start_pos`) |
| Blocks carry BlockInfo | **yes**: Option, Suboption, DCPBlockLength, **BlockInfo(u16)**, Value | VERIFIED WS-DCP. Each suboption dissector reads BlockInfo when `(service==HELLO && !is_response)` (l.513, 546, 592, 694, 1089, 1316, 1511), the same as Identify-response and Get-response. PNET `pf_dcp_put_block(..., with_block_info=true, 0, ...)`. DCPBlockLength includes the 2 BlockInfo bytes |
| Block padding | pad each block to an even length with 0x00. The padding is **not** counted in DCPBlockLength | VERIFIED PNET `pf_dcp_put_block` ("Add padding to align on uint16_t"). WS-DCP adds one padding byte when the block is odd |
| Response | **none**. Hello is an unconfirmed multicast | INFERRED. No response path exists in PNET or WS. PNET `pf_dcp_hello_ind` only checks the destination MAC and discards the frame, with the comment "Device-device communication (MC) is not yet implemented" |

**Block grammar given in p-net's comment** (PNET pf_dcp.c `pf_dcp_hello_ind`, about l.1163-1205, quoting "the standard"):
`DCP-MC-Header, NameOfStationBlockRes, IPParameterBlockRes, DeviceIDBlockRes, [DeviceOptionsBlockRes ^ DeviceVendorBlockRes], DeviceRoleBlockRes, DeviceInitiativeBlockRes`. This is VERIFIED as p-net's citation. The meaning of `[a ^ b]` (optional, either one) is INFERRED.

**What p-net actually sends** (OBSERVED-IN-IMPL, `pf_dcp_hello_req`, in this order): NameOfStation (2/2), IPParameter (1/2, 12 bytes, **BlockInfo hard-coded 0** even when an IP is set), DeviceID (2/3), OEMDeviceID (2/8) if configured, DeviceInitiative (6/1). p-net **omits DeviceRole**, which deviates from its own grammar comment. Recommendation for go-profinet: follow the grammar order (Name, IP, DeviceID, DeviceRole, DeviceInitiative) and set IP BlockInfo the way the Identify response does (bit0 = "IP set", PNET `pf_dcp_get_req`). This is INFERRED.

PNPY `send_hello` (dcp.py ~l.1369) uses the order Name, IP, DeviceID, DeviceRole, DeviceInitiative, but writes the blocks **without BlockInfo**. That disagrees with WS-DCP and PNET, and Wireshark would mis-dissect it. Do not follow PNPY here.

### A.2 DeviceInitiative (option 0x06, suboption 0x01)

- Value is a u16. 0x0000 means "Device does not issue a DCP-Hello-ReqPDU after power on". 0x0001 means "Device does issue a DCP-Hello-ReqPDU after power on". VERIFIED WS-DCP l.398-401 and PNPY dcp.py:379-387.
- Option 0x06 is "Device Initiative". VERIFIED WS-DCP l.233/246.
- In a Hello request, Identify response or Get response it carries BlockInfo. In a Set request it carries BlockQualifier. VERIFIED WS-DCP `dissect_PNDCP_Suboption_DeviceInitiative` l.1490-1530.
- **Read-only in p-net.** A DCP Set of option 6 returns BlockError 0x01 "Option unsupp.". OBSERVED-IN-IMPL PNET pf_dcp.c about l.872 (`case PF_DCP_OPT_DEVICE_INITIATIVE: default: /* Can't set this */`). PNET pf_dcp.h:92 marks the suboption "Read".
- The value comes from device configuration (`pnet_cfg_t.send_hello`, pnet_api.h:1374-1375 → `device_initiative`, pf_cmina.c:183). The controller does **not** enable Hello through DCP. The controller-side enable is FSU parameterisation (FSHelloBlock in the PDInterfaceFSUDataAdjust / ARFSUDataAdjust records, see A.3). That last point is INFERRED from WS-IO block names together with PLCN.
- **Identify response:** p-net has the DeviceInitiative entry in its Identify/Get option list **commented out** (pf_dcp.c:157), so p-net does not send it in Identify. OBSERVED-IN-IMPL. Wireshark is able to dissect it in an Identify response (VERIFIED WS-DCP l.1510). PNPY parses it optionally from Identify responses (dcp.py:650-658). Whether the spec requires it in Identify is UNVERIFIED. Recommendation: include it in Identify/Get responses when Hello is supported. This is INFERRED and harmless.

### A.3 Hello repetition and FSU parameters

**p-net behaviour** (OBSERVED-IN-IMPL):
- `PF_CMINA_FS_HELLO_RETRY 3` and `PF_CMINA_FS_HELLO_INTERVAL (3 * 1000) /* milliseconds. Default is 30 ms */` (pf_types.h:1062-1064). p-net therefore uses 3 s, while its own comment states that the default is **30 ms**.
- Trigger: only in `pf_cmina_init` (startup), and only if a NameOfStation **and** a non-zero IP are stored and `device_initiative == 1` (pf_cmina.c about l.520-560). The first Hello is sent immediately (delay 0). There are 3 Hellos in total, at t = 0, 3 s and 6 s. Each send happens only while `cmina_state == W_CONNECT` (pf_cmina.c:59-80).
- Stop conditions: any incoming DCP Set (`pf_cmina_dcp_set_ind` → `pf_scheduler_remove_if_running(... hello_timeout)`, pf_cmina.c about l.646), or leaving the W_CONNECT state (for example after a Connect).
- p-net does **not** re-send Hello on link-up or after a name or IP is set over DCP.

**FSU parameter blocks** (VERIFIED WS-IO):
- Block types: 0x0600 FSHelloBlock, 0x0601 FSParameterBlock, 0x0602 FastStartUpBlock, 0x0608 PDInterfaceFSUDataAdjust, 0x0609 ARFSUDataAdjust, and 0x010B ARFSUBlock (inside Connect). Source: l.1390 and l.1506-1510. p-net has `PF_BT_AR_FSU_BLOCK_REQ = 0x010B` (pf_types.h:548).
- Record indexes: **0x8090 PDInterfaceFSUDataAdjust** (WS-IO l.2233; PNET `PF_IDX_SUB_PDINTF_FSU_ADJUST = 0x8090`) and **0xE050 ARFSUDataAdjust** (WS-IO l.2381; PNET `PF_IDX_AR_FSU_DATA_ADJUST`). Note: 0x8070 is **PDNCDataCheck** and 0x8071 is PDInterfaceAdjust. Neither is an FSU record.
- FSHelloBlock v1.0 layout (`dissect_FSHello_block`, WS-IO l.13799-13835), after the 6-byte block header:
  - align4 padding (2 bytes)
  - FSHelloMode u32: 0 = OFF, 1 = "Send req on LinkUp", 2 = "Send req on LinkUp after HelloDelay" (l.3702-3707)
  - FSHelloInterval u32, in ms. The field description reads "ms before conveying a second DCP_Hello.req" (l.23288)
  - FSHelloRetry u32
  - FSHelloDelay u32, in ms
- FSParameterBlock v1.0: align4, FSParameterMode u32 (0 = OFF, 1 = ON, 2-3 reserved), FSParameterUUID (16 bytes). The UUID is in DCE/RPC `drep` byte order. VERIFIED WS-IO l.13839-13866 and l.3709-3715.
- PDInterfaceFSUDataAdjust and ARFSUDataAdjust v1.0: 2 bytes of padding, then nested sub-blocks (FSHello, FSParameter). VERIFIED WS-IO l.13870-13925.
- Defaults from vendor docs (PLCN, VERIFIED as vendor statements): default Hello interval 30 ms; retransmissions 1..15, default 3; Hello mode OFF by default; delayed mode waits a configurable time after LinkUp.ind. FSU needs a fixed MAU type on FSU ports ("100 Mbit/s full duplex", autonegotiation and auto-crossover off). The target is "less than 500 ms" power-on to data exchange, "defined by the PI organization".
- Stored parameters (HIL, VERIFIED vendor statement): for *parameterization speedup*, the device stores the FSParameterUUID and all submodule write-record data in NVM. If the UUID in the next Connect matches, the device can use the stored parameters. FSU (Hello plus fixed link) and ParameterizationSpeedup are separate GSDML features: `DCP_HelloSupported` and `ParameterizationSpeedupSupported`. PNET doc/compliancetest.rst also lists `PowerOnToCommReady`.
- **Public PI wording:** PI-CC p.12 lists "Fast start-up after voltage recovery for switching operations — FSU — Optional — B". Hello alone (DeviceInitiative = 1 with the p-net cadence) is a plain DCP feature. The full FSU feature is Hello timing via FSHelloBlock, a fixed MAU, and stored parameters. This split is INFERRED from the sources above.

### A.4 How a controller uses Hello

- PLCN (vendor, VERIFIED statement): "When the controller receives the frame, it establishes the connection". If no connection follows, the Hello is retransmitted.
- HIL: the device "already has its IP address set" when it sends Hello, so the controller can skip the DCP Identify and Set steps.
- Matching the NameOfStation in the Hello to the configured device, then going straight to RPC Connect with the IP from the Hello block, is INFERRED. It follows from the Hello content (name, IP, DeviceID) and the vendor statements. There is no public normative text.
- No DCP response to Hello exists. INFERRED (A.1).
- A controller-side receiver exists in PNPY `receive_hello` (dcp.py about l.1476). It filters ServiceID 6 / request and parses the frame like an Identify response.

### A.5 Wireshark dissection and golden vectors

- Wireshark dispatches DCP for FrameIDs 0xFEFC..0xFEFF (`dissect_PNDCP_Data_heur`, WS-DCP about l.1769). The Info column shows "Hello Req, Xid:0x..". The NameOfStation, IP, DeviceID, DeviceRole and DeviceInitiative blocks each show BlockInfo.
- **Public captures:** the SampleCaptures page at wiki.wireshark.org has **no PROFINET captures**. The discussion section only contains a request for one. wiki.wireshark.org/PROFINET lists none. The Wireshark repo has no `test/captures/*pn*` files. There is therefore **no public, licence-clear Hello capture** to use as a golden vector. Use synthetic vectors and check them with `tshark -V` locally.

**Synthetic Hello.req** (INFERRED; layout per WS-DCP and PNET). Station "dut", IP 192.168.0.50/24, gateway 192.168.0.1, Vendor 0x0493, Device 0x0002, Role IO-Device, Initiative 1:
```
0000  01 0e cf 00 00 01 02 00 00 00 00 01 88 92 fe fc   dst, src, 8892, FrameID FEFC
0010  06 00 00 00 00 01 00 00 00 36                      SID 6, Type 0, Xid 1, reserved 0, DataLength 0x36
001a  02 02 00 05 00 00 64 75 74 00                      NameOfStation, len 5 = BlockInfo + "dut", pad
0024  01 02 00 0e 00 01 c0 a8 00 32 ff ff ff 00 c0 a8 00 01   IPParameter, BlockInfo=1 (IP set)
0036  02 03 00 06 00 00 04 93 00 02                      DeviceID
0040  02 04 00 04 00 00 01 00                            DeviceRole 0x01 IO-Device + reserved byte
0048  06 01 00 04 00 00 00 01                            DeviceInitiative = 1
```
The total is 80 bytes, which is above the 60-byte minimum. DeviceRole bit meanings (VERIFIED WS-DCP l.874-880): 0x01 IO-Device, 0x02 IO-Controller, 0x04 IO-Multidevice, 0x08 PN-Supervisor. A p-net Hello would differ: IP BlockInfo 0x0000 and no DeviceRole block.

---

## B. PDev records

### B.1 Indexes and block types (cross-checked WS-IO and PNET pf_types.h)

**Record indexes** (VERIFIED WS-IO `pn_io_index[]` l.2149ff and PNET pf_types.h:602-747; both agree unless noted):

| Index | Record |
|---|---|
| 0x8027 | PDPortDataRealExtended (**not 0x8062**) |
| 0x802A | PDPortDataReal |
| 0x802B | PDPortDataCheck |
| 0x802C | PDIRData |
| 0x802D | PDSyncData (SyncID 0) |
| 0x802F | PDPortDataAdjust |
| 0x8050-0x8054 | PDInterfaceMrpDataReal/Check/Adjust, PDPortMrpDataAdjust/Real |
| 0x8055-0x8057 | PDPortMrpIc: **WS: 0x8055 Adjust, 0x8056 Check, 0x8057 Real. PNET: 0x8055 REAL, 0x8057 ADJUST.** The two sources conflict; prefer WS. UNVERIFIED |
| 0x8060-0x8063 | PDPortFODataReal / Check / Adjust (0x8062) / PDPortSFPDataCheck |
| 0x8070 | PDNCDataCheck |
| 0x8071 | PDInterfaceAdjust |
| 0x8072 | PDPortStatistic |
| 0x8080 | PDInterfaceDataReal |
| 0x8090 | PDInterfaceFSUDataAdjust |
| 0xE050 | ARFSUDataAdjust |
| 0xF831 | PDevData |
| 0xF841 | PDRealData |
| 0xF842 | PDExpectedData |

**Block types** (VERIFIED WS-IO `pn_io_block_type[]` l.1337ff; PNET pf_types.h:562-569 agrees for 0x0200, 0x020A, 0x020F, 0x0202, 0x0224, 0x0240, 0x0250, 0x0251):

| Type | Block |
|---|---|
| 0x0200 | PDPortDataCheck |
| 0x0201 | PDevData |
| 0x0202 | PDPortDataAdjust |
| 0x0209 | AdjustDomainBoundary |
| 0x020A | CheckPeers |
| 0x020B | CheckLineDelay |
| 0x020C | CheckMAUType |
| 0x020E | AdjustMAUType |
| 0x020F | PDPortDataReal |
| 0x0210 | AdjustMulticastBoundary |
| 0x021B | AdjustLinkState (WS function `dissect_AdjustPortState_block`) |
| 0x021C | CheckLinkState |
| 0x021E | CheckSyncDifference |
| 0x021F | CheckMAUTypeDifference |
| 0x0224 | AdjustPeerToPeerBoundary |
| 0x0225 | AdjustDCPBoundary |
| 0x0226 | AdjustPreambleLength |
| 0x0227 | CheckMAUType-Extension |
| 0x0229 | AdjustMAUType-Extension |
| 0x022C | PDPortDataRealExtended |
| 0x0230 | PDNCDataCheck |
| 0x0240 | PDInterfaceDataReal |
| 0x0250 | PDInterfaceAdjust |
| 0x0251 | PDPortStatistic |

**Common block header:** BlockType u16, BlockLength u16 (bytes after this field, so it includes the 2 version bytes), BlockVersionHigh u8, BlockVersionLow u8. All values are big-endian. VERIFIED WS-IO `dissect_block` (body = BlockLength − 2) and PNET writer (`block_len = pos − (block_pos + 4)`). The next block starts at start + 4 + BlockLength (WS `dissect_blocks` loop). Trailing padding inside a block is counted in BlockLength.

**Alignment rule:** WS uses `dissect_pn_align4`, which pads to a multiple of 4 relative to the start of the tvb (WS-PN l.1508-1519). For sub-blocks the tvb is a new subset starting at the first sub-block, so alignment is effectively relative to the block start when blocks begin 4-aligned. INFERRED: "pad with zeros so the next field is 32-bit aligned relative to the BlockType of the outermost block".

**Field layouts** (offsets from the start of the BlockType; all v1.0 unless noted; VERIFIED WS-IO dissector names and lines):

- **PDPortDataCheck 0x0200** (l.10012): hdr(6), pad 2, SlotNumber u16, SubslotNumber u16, then one or more sub-blocks (CheckPeers, CheckLineDelay, CheckMAUType, CheckLinkState, CheckSyncDifference, CheckMAUTypeDifference, ...).
- **PDPortDataAdjust 0x0202** (l.9971): hdr(6), pad 2, Slot u16, Subslot u16, then sub-blocks (AdjustDomainBoundary, AdjustMulticastBoundary, AdjustMAUType, AdjustLinkState, AdjustPeerToPeerBoundary, AdjustDCPBoundary, AdjustPreambleLength, ...).
- **CheckPeers 0x020A** (l.10921): hdr(6), **no padding**, NumberOfPeers u8, then for each peer: LengthPeerPortID u8, PeerPortID (ASCII), LengthPeerChassisID u8, PeerChassisID (ASCII). There is no trailing padding inside the block. p-net pads the *outer* PDPortDataCheck to 4 instead (`pf_put_padding_align(block_pos,4)`). Note the order: **port first, then chassis** (the station name).
- **CheckLineDelay 0x020B** (l.10896): hdr, align4, LineDelay u32.
- **CheckMAUType 0x020C** (l.10871): hdr, MAUType u16 (no padding).
- **CheckLinkState 0x021C** (l.10997, "CheckPortState"): hdr, PortState u16. Values: 1 up, 2 down, 3 testing, 4 unknown (l.3527).
- **CheckMAUTypeDifference 0x021F** (l.10723): hdr, MAUTypeMode u16 (0 OFF, 1 ON).
- **AdjustDomainBoundary 0x0209** (l.10746). v1.0: align4, DomainBoundary u32, AdjustProperties u16, align4. v1.1: align4, DomainBoundaryIngress u32, DomainBoundaryEgress u32, AdjustProperties u16, align4.
- **AdjustMulticastBoundary 0x0210** (l.10808): align4, MulticastBoundary u32, AdjustProperties u16.
- **AdjustMAUType 0x020E** (l.10839): align4, MAUType u16, AdjustProperties u16.
- **AdjustLinkState 0x021B** (l.10965): align4, PortState u16, AdjustProperties u16.
- **AdjustPeerToPeerBoundary 0x0224** (l.11216): align4, PeerToPeerBoundary u32, AdjustProperties u16.
  - bit0: do not send LLDP
  - bit1: do not send PTCP_DELAY requests
  - bit2: do not send PATH_DELAY requests (l.3334-3350)
  - p-net's writer adds 2 trailing pad bytes (`pf_put_peer_to_peer_boundary`), giving BlockLength 12. WS's minimal reading gives 10. Whether a trailing pad is required is UNVERIFIED, so a parser must tolerate both.
- **AdjustDCPBoundary 0x0225** (l.11258): align4, DCPBoundary u32, AdjustProperties u16.
  - bit0: block egress DCP_Identify to 01-0E-CF-00-00-00
  - bit1: block egress DCP_Hello to 01-0E-CF-00-00-01 (l.3322-3331)
- **AdjustPreambleLength 0x0226** (l.9923): align4, PreambleLength u16, AdjustProperties u16.
- **PDPortDataReal 0x020F** (l.~10100):
  - hdr, align4, Slot u16, Subslot u16
  - LengthOwnPortID u8, OwnPortID
  - NumberOfPeers u8, align4
  - per peer: LengthPeerPortID u8, PeerPortID, LengthPeerChassisID u8, PeerChassisID, align4, LineDelay u32, PeerMACAddress (6 bytes), align4
  - MAUType u16, align4
  - DomainBoundary u32, MulticastBoundary u32
  - LinkState.Port u8, LinkState.Link u8, align4
  - MediaType u32
  - OBSERVED-IN-IMPL bug in p-net (`pf_put_pdport_data_real`): it writes a *fixed* 2-byte pad after NumberOfPeers instead of align4. That is only correct when the OwnPortID length is 8 (for example "port-001"). LinkState.Link values: 1 up, 2 down, 3 testing, 4 unknown, 5 dormant, 6 notpresent, 7 lowerlayerdown. Port value 0 = unknown (PNET pf_types.h:240-254).
- **PDInterfaceDataReal 0x0240** (l.~12650):
  - hdr, **no padding**: LengthOwnChassisID u8, then OwnChassisID ("OwnStationName" in WS)
  - align4, MACAddress (6 bytes), align4
  - IPAddress u32, Subnetmask u32, StandardGateway u32
  - Same as PNET `pf_put_pdinterface_data_real`. p-net puts the NameOfStation in the chassis field, with the comment "maybe mac string shall be used instead" when it is empty.
- **PDInterfaceAdjust 0x0250** (l.11427): align4, MultipleInterfaceMode u32. bit0 NameOfDevice: 0 = "PortID of LLDP contains name of port (Default)", 1 = "PortID of LLDP contains name of port and NameOfStation" (l.1987-1991). The other bits are reserved.

**Synthetic vectors** (INFERRED from the layouts above):
```
PDPortDataCheck (subslot 0x8001, CheckPeers port-001 / plc), 32 bytes:
02 00 00 1c 01 00 00 00 00 00 80 01 02 0a 00 10 01 00 01 08 70 6f 72 74 2d 30 30 31 03 70 6c 63
PDPortDataAdjust with AdjustPeerToPeerBoundary (no LLDP tx), minimal form, 26 bytes:
02 02 00 16 01 00 00 00 00 00 80 01 02 24 00 0a 01 00 00 00 00 00 00 01 00 00
PDInterfaceAdjust (NameOfDevice=1):   02 50 00 08 01 00 00 00 00 00 00 01
AdjustDCPBoundary (block both):       02 25 00 0a 01 00 00 00 00 00 00 03 00 00
AdjustMAUType (0x0010 100BASETXFD):   02 0e 00 08 01 00 00 00 00 10 00 00
CheckMAUType 0x0010:                  02 0c 00 04 01 00 00 10
CheckLinkState up:                    02 1c 00 04 01 00 00 01
```
In an IODWriteReq the record data starts with this block header. p-net's `pf_cmrpc` strips the outer header and then parses pad, slot, subslot and the first sub-block header (`pf_get_port_data_check` / `pf_get_port_data_adjust`, pf_block_reader.c:956-1043).

### B.2 Device semantics in p-net (all OBSERVED-IN-IMPL, PNET pf_pdport.c / pf_cmwrr.c / pf_cmrpc.c)

- **Writable indexes:** only 0x802B, 0x802F (port subslots) and 0x8071 (interface subslot) are routed to pdport (pf_cmwrr.c:261-264).
- **Only the first sub-block is parsed.**
  - PDPortDataCheck: only **CheckPeers** is accepted. It requires NumberOfPeers ≥ 1 and keeps only peer[0]. Any other first sub-block type logs "Unsupported port data check block type" and fails.
  - PDPortDataAdjust: only **AdjustPeerToPeerBoundary** is accepted, and only bit0 (LLDP tx disable) has an effect. Other types fail.
  - Further sub-blocks in the same record are silently ignored.
- **Error on unsupported or failed writes:** pdport returns −1 without setting a status. `pf_cmrpc` then sets ErrorCode 0xDF (WRITE), ErrorDecode 0x80 (PNIORW), ErrorCode1 0xB4 (`ACC_INVALID_AREA_API`), ErrorCode2 2 (pf_cmrpc.c about l.3280-3290; pnet_api.h:126, 137, 159). The PI-correct code for "unsupported block" is UNVERIFIED. Candidates are 0xB0 invalid index and 0xB8 invalid parameter / 0xB9 invalid type. A write from a non-owner AR gives 0xB6 access denied (pf_cmwrr.c:230). The wrong cmwrr state gives 0xB5 state conflict, and a backup AR gives 0xBA.
- **Check evaluation:** it runs immediately after the write (`pf_pdport_run_peer_check`), and again from `pf_pdport_periodic` whenever `lldp_peer_info_updated` is set. That flag is set on new LLDP peer info, on LLDP RX timeout and on link-down (pf_lldp.c about l.505 and 1957; pf_pdport.c `handle_link_down`). The link monitor polls every 100 ms (`PF_LINK_MONITOR_INTERVAL 100000` µs). The checks run in this order: no-peer, port name, station name.
- **Diagnoses** (standard-format diag, slot 0, subslot = port subslot, channel 0x8000 whole submodule, Fault severity):
  - ChannelErrorType **0x8001 "Remote mismatch"**, with ExtChannelErrorType **0x8000** peer NameOfStation mismatch, **0x8001** peer port name mismatch, or **0x8005** no peer detected (pf_types.h:320-330).
  - The diagnosis is removed when the condition clears.
  - Caveat: the station-name compare uses `strncmp` with the *received* length (a prefix match), which is an implementation weakness.
- **Persistence:** after a successful write, a background job saves the per-port pdport data (check plus adjust) to NVM (`PF_BGJOB_SAVE_PDPORT_NVM_DATA`). At init `pf_pdport_load` restores it, re-arms the peer check, and re-applies LLDP tx enable/disable. `pf_pdport_reset_all` exists for factory reset. PDInterfaceAdjust (`name_of_device_mode`) is held in RAM and is **not** saved by pdport.
- **Read-back:**
  - 0x802B returns the stored PDPortDataCheck (CheckPeers) only if one is active, otherwise empty data with ret 0. p-net has an observed bug: the whole-module loop in that path never advances the port iterator.
  - 0x802F returns the stored adjust if active.
  - 0x8071 returns PDInterfaceAdjust if written.
  - 0x802A, 0xF841 PDRealData (a multiblock of interface + statistics and port + statistics) and 0x8072 are supported.
  - 0xF842 PDExpectedData is "not yet implemented" and returns empty data. The p-net comment lists the expected PDExpectedData content: MultipleBlockHeader-wrapped [PDPortDataCheck], [PDPortDataAdjust], MRP, FO, PDNCDataCheck, PDInterfaceFSUDataAdjust, PDInterfaceAdjust, ..., "each submodule's data ... need its own MultipleBlockHeader".

### B.3 PDInterfaceAdjust / PDInterfaceDataReal content

- PDInterfaceAdjust contains only MultipleInterfaceMode (u32, bit0 NameOfDevice) in v1.0 (VERIFIED WS-IO).
- p-net semantics (OBSERVED-IN-IMPL pf_lldp.c about l.589-680; PNET doc/profinet_details.rst about l.560-582):
  - mode 0 ("legacy"): LLDP PortID = "port-001", ChassisID = NameOfStation, or the MAC string if no name is set.
  - mode 1 ("standard", PN ≥ v2.3): PortID = "port-001.<NameOfStation>", ChassisID = a system-description string.
  - LLDP transmission restarts on write.
- Wireshark labels mode 0 as "(Default)". p-net initialises it as not-active, which is treated as legacy.
- PDInterfaceDataReal content: ChassisID (= NameOfStation in p-net), interface MAC, IP, mask and gateway (B.1).

### B.4 What a controller writes at startup

- PI-CC p.12 lists "Port-related network statuses via PROFINET PDEV — Standard — A, B, C". It also says "In parallel to SNMP, all diagnostic and topology information can also be queried from the Physical Device Object (PDEV) using acyclic PROFINET services" (p.8). VERIFIED.
- Subslot numbering: interface 0x8000, port N 0x8000+N (VERIFIED PNET pnet_api.h:77-78; `PNET_SUBSLOT_DAP_INTERFACE_1_PORT_1_IDENT 0x8001`).
- Writes happen in the parameterisation phase between Connect and PrmEnd (the cmwrr STARTUP state). In the DATA state, writes are also accepted (pf_cmwrr.c:300-360, OBSERVED-IN-IMPL).
- The typical sequence is UNVERIFIED (no public commissioning text was found in this pass): PDInterfaceAdjust (0x8071) to subslot 0x8000; PDPortDataCheck (0x802B) with CheckPeers per port when the topology is configured; PDPortDataAdjust (0x802F) for boundaries or MAU; PDInterfaceFSUDataAdjust (0x8090) when FSU is configured. These may be sent as individual writes or as a MultipleWrite (0xE040). A device that only supports a subset should reject the rest with a PNIORW error. A real controller (TIA) aborts the AR on a write error to a PDev record, which is UNVERIFIED and should be tested against a real PLC or Codesys.

---

## C. Conformance class B (brief)

From PI-CC (VERIFIED, page refs to V1.1 Mar 2011):
- p.6: "CC-B extends the concept to include network diagnostics via IT mechanisms as well as topology information". The table gives CC-A "Topology information (LLDP)" and CC-B "Network diagnostics via IP (SNMP)" plus "Topology information (LLDP) with LLDP-MIB".
- p.8, §3.3: CC-B means "SNMP ... Portions of the Management Information Base 2 (MIB2) and the ... LLDP-EXT MIB are integrated in the devices", and PDEV gives the same data over acyclic services.
- p.12: LLDP and PDEV are Standard for A, B and C. SNMP is Standard for B and C. FSU is Optional, B. MRP is Optional, B. "Name assignment via DCP, PDEV" (automatic addressing after replacement) is Optional, B and C. Topology display (SNMP, LLDP, PDEV) is Standard, B and C.
- Features vs certification: p.4 says "The certification of a device according to a CC is done to ensure that selected devices will have the defined minimum properties". p.11 says optional functions "are also tested during certification tests". So "supports CC-B features" is not the same as "CC-B certified", which requires PI certification (the p.6 table "Controller/devices with certificate"). The distinction is INFERRED from that wording.
- PNET doc/compliancetest.rst about l.515-519 (OBSERVED, vendor doc): for CC-B the GSDML uses `ConformanceClass="B"` and `SupportedProtocols="SNMP;LLDP"`. The tests listed are topology discovery, port-to-port and PDEV_RECORDS (multi-port).
- Consequence for go-profinet (INFERRED): PDPortDataReal, PDInterfaceDataReal, PDRealData, PDPortDataCheck (CheckPeers + diagnosis) and PDInterfaceAdjust are needed already for CC-A behaviour. CC-B additionally needs an SNMP agent with MIB-II and LLDP-MIB (+ PNO LLDP extension).

## Open items / UNVERIFIED

1. The spec value of Hello Xid, and whether Hello must be re-sent on link-up when FSHelloMode = 1. The WS enum text implies yes.
2. Whether a trailing pad is required inside the Adjust* sub-blocks. PNET writes one; WS does not need one.
3. The PDPortMrpIc index assignment (0x8055 vs 0x8057): WS and PNET disagree.
4. The exact PNIORW code for an unsupported PDev sub-block.
5. Whether DeviceInitiative is mandatory in Identify / Get responses.
6. No licence-clear public capture of Hello or PDev writes was found. Generate captures locally (p-net plus Codesys, or our own stack) and verify with tshark.
