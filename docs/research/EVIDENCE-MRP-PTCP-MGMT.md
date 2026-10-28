# MRP / PTCP / CC-B management / DHCP / GSDML 2.46-2.50 / GSDX research

Date: 2026-10-27. Purpose: facts for MIT pure-Go `go-profinet`. No code copied; facts only.

## Labels
- **VERIFIED**: read in the cited source this session (URL + file + line).
- **OBSERVED-IN-IMPLEMENTATION (X)**: an implementation does it; not necessarily normative.
- **INFERRED**: deduced from verified facts.
- **UNVERIFIED**: from memory or secondary summary; needs a check.

## Sources and pinned revisions (read at these revisions, never executed)
| Short | Source | Revision | Licence |
|---|---|---|---|
| K-uapi | github.com/torvalds/linux `include/uapi/linux/mrp_bridge.h`, `if_bridge.h` | master @ d135ab453a7400a34930bdda1ef4bdc8acfac80e | GPL-2.0+ WITH Linux-syscall-note |
| K-mrp | linux `net/bridge/br_mrp.c`, `br_private_mrp.h`, `br_mrp_switchdev.c`, `net/switchdev/switchdev.c` | same | GPL-2.0 |
| WS-MRP | github.com/wireshark/wireshark `plugins/epan/profinet/packet-pn-mrp.c` | master @ 452e1fa203141634221b38b9e931dee66686198b | GPL-2.0+ |
| WS-PTCP | same repo `plugins/epan/profinet/packet-pn-ptcp.c` | same | GPL-2.0+ |
| WS-DCP | same repo `plugins/epan/profinet/packet-pn-dcp.c` | same | GPL-2.0+ |
| WS-PNIO | same repo `plugins/epan/profinet/packet-dcerpc-pn-io.c` | same | GPL-2.0+ |
| MC | github.com/microchip-ung/mrp (`pdu.h`, `state_machine.c/.h`, `utils.h`, `mrp.c`, `README.adoc`) | HEAD @ 177717ee893d5ccc6e31147b1b8a720c9b798b81 | **GPL-2.0** (README: "may require licenses from owners of applicable rights") |
| UM | github.com/giometti/umrp (fork of MC; `state_machine.c`, `ifdriver_*.c`, README) | HEAD @ fb193eac1a2df51fe42e19d4ea36bb0157bda09f | **GPL-2.0** |
| PN | github.com/rtlabs-com/p-net | ea5e5540b2be55c4dfadffda39b948632c104daa | **GPLv3 / commercial dual** |
| iproute2 | github.com/iproute2/iproute2 `bridge/` directory listing | HEAD (2026-10) | GPL-2.0 |

Note: the Wireshark dissectors live under `plugins/epan/profinet/`, not `epan/dissectors/`.

---

## A. MRP (IEC 62439-2)

### A.1 Frame format

**Ethernet layer**
- EtherType 0x88E3: VERIFIED. K-mrp uses `ETH_P_MRP` (br_mrp.c:138,145). WS-MRP registers on `ETHERTYPE_MRP` (packet-pn-mrp.c:874). Value 0x88E3 comes from `include/uapi/linux/if_ether.h` (not re-fetched), so the number itself is UNVERIFIED-this-session but well known.
- Destination MACs: VERIFIED.
  - `01-15-4E-00-00-01`: MRP_Test (K-mrp br_mrp.c:6; MC state_machine.c:20).
  - `01-15-4E-00-00-02`: "control" (TopologyChange, LinkDown/LinkUp) (MC state_machine.c:21; used for topo at ~466 and link at ~535).
  - `01-15-4E-00-00-03`: MRP_InTest (K-mrp br_mrp.c:7).
  - `01-15-4E-00-00-04`: interconnection control (InTopologyChange/InLink*) (MC state_machine.c:22).
- Source MAC: the kernel puts the egress port's MAC in the Ethernet SA but the bridge MAC in the MRP_SA field (br_mrp.c:185,193). OBSERVED-IN-IMPLEMENTATION (K-mrp).
- Priority: `MRP_FRAME_PRIO 7` is used as skb priority (K-uapi mrp_bridge.h:13; br_mrp.c:139). Whether the frames carry a VLAN priority tag on the wire is UNVERIFIED. The kernel sends untagged.
- Minimum length: MC pads the payload to 60 bytes before the FCS (state_machine.c:414-419). OBSERVED (MC).

**PDU = MRP_Version (u16 BE) followed by a TLV sequence**
- `MRP_VERSION 1` (K-uapi mrp_bridge.h:12; br_mrp.c:147-148). WS-MRP reads the version as u16 (packet-pn-mrp.c:605). VERIFIED.
- TLV header is `type u8, length u8` (MC pdu.h:9-12; WS-MRP :619-622). VERIFIED.
- Alignment: WS-MRP restarts offsets after MRP_Version ("4byte alignment regarding to the beginning of the next TLV block", :607-609) and applies `align4` after most TLVs. VERIFIED (dissector behaviour).
- TLV types (K-uapi mrp_bridge.h:53-66; WS-MRP :72-86): VERIFIED.

| Type | Name |
|---|---|
| 0x00 | MRP_End (length 0) |
| 0x01 | MRP_Common |
| 0x02 | MRP_Test |
| 0x03 | MRP_TopologyChange |
| 0x04 | MRP_LinkDown |
| 0x05 | MRP_LinkUp |
| 0x06 | MRP_InTest |
| 0x07 | MRP_InTopologyChange |
| 0x08 | MRP_InLinkDown |
| 0x09 | MRP_InLinkUp |
| 0x0A | MRP_InLinkStatusPoll |
| 0x7F | MRP_Option (organisationally specific) |

- Sub-TLV types inside MRP_Option (K-uapi :68-72; WS-MRP :135-155): 0x01 TestMgrNAck, 0x02 TestPropagate, 0x03 AutoMgr, 0xF1-0xFF manufacturer. VERIFIED.
  - Discrepancy: WS-MRP's comment at :389-391 labels 0x02 as "MRP_AutoMgr", while its value table and K-uapi say 0x02 = TestPropagate and 0x03 = AutoMgr. Trust the tables.

**Field layouts** (all big-endian; sizes VERIFIED from MC pdu.h and the WS-MRP dissect order)

| TLV | Fields (bytes) | Value length | Source |
|---|---|---|---|
| Common (0x01) | SequenceID u16, DomainUUID 16 | 18 | MC pdu.h:23-26; WS :186-198 |
| Test (0x02) | Prio u16, SA 6, PortRole u16, RingState u16, Transition u16, TimeStamp u32 | 18 | MC pdu.h:28-35; WS :276-306 |
| TopologyChange (0x03) | Prio u16, SA 6, Interval u16 | 10 | MC pdu.h:37-41; WS :313-340 |
| LinkDown/LinkUp (0x04/0x05) | SA 6, PortRole u16, Interval u16, Blocked u16 | 12 | MC pdu.h:43-48; WS :200-243 |
| InTest (0x06) | InID u16, SA 6, PortRole u16, InState u16, Transition u16, TimeStamp u32 | 18 | MC pdu.h:73-80; WS :474-499 |
| InTopologyChange (0x07) | SA 6, InID u16, Interval u16 | 10 | MC pdu.h:82-86; WS :502-525 |
| InLinkDown/Up (0x08/0x09) | SA 6, PortRole u16, InID u16, Interval u16, **[LinkInfo u16 in WS only]** | 12 (MC/K), 14 (WS) | MC pdu.h:88-93; WS :528-575 |
| InLinkStatusPoll (0x0A) | SA 6, PortRole u16, InID u16 | 10 | MC pdu.h:95-99; WS :577-591 |
| Option (0x7F) | OUI 3, then OUI-specific. K-mrp MRA form: OUI 00-00-00, sub-option byte(s), sub-TLV AutoMgr, 2 bytes zero padding | var | br_mrp.c:204-224, br_private_mrp.h:9 (`MRP_OPT_PADDING 2`) |
| TestMgrNAck / TestPropagate sub-TLV | Prio u16, SA 6, OtherPrio u16, OtherSA 6 (+2 padding per WS comment) | 16 | MC pdu.h:55-67; WS :370-411 |

**Field values**
- PortRole: 0 = primary, 1 = secondary, 2 = interconnection (K-uapi :47-51; WS :89-93). VERIFIED.
- RingState: 0 = open, 1 = closed (K-uapi :30-33; WS :110-113). VERIFIED.
- InState: the same open/closed mapping. VERIFIED.
- Prio: default MRM priority 0x8000 (K-uapi :10). VERIFIED.
  - WS-MRP's ranges (:245-266): 0x0000 highest MRM; 0x1000-0x7000 high; 0x8001-0x8FFF low MRM; 0x9000-0x9FFF high MRA; 0xA000 default MRA; 0xA001-0xF000 low MRA; 0xFFFF lowest. VERIFIED (dissector).
  - Lower value wins; ties are broken by the lower MAC as a u64 (K-mrp br_mrp.c:935-947; MC :885-896). OBSERVED (K, MC).
- Interval: in ms. "Interval for next topology change event (in ms)". Values < 0x07D1 are "Mandatory", otherwise "Optional" (WS :218-226). VERIFIED (dissector text).
  - MC sends `interval/1000` because its internal unit is µs (state_machine.c:~459, ~525).
- Blocked: 0 means the MRC cannot receive/forward on a blocked port, 1 means it can, others reserved (WS :229-239). VERIFIED.
- TimeStamp: the kernel puts `jiffies_to_msecs(jiffies)` there, i.e. ms (br_mrp.c:197). OBSERVED (K).
- DomainUUID: both K and MC default to all 0xFF (br_mrp.c:172; MC :2377). OBSERVED. INFERRED: the all-FF UUID is the "default domain".
- SequenceID: both K and MC increment it per frame per instance (br_mrp.c:120-124). OBSERVED.
- LinkInfo (WS only) bitmask (WS :21-25, :159-166):
  - 0x0001 primary/secondary.
  - 0x0F00 link-change reason: 1 undefined, 2 RDI, 3 CFM timeout, 4 PHY.
  - 0x1000 additional check.
  - VERIFIED (dissector). Not present in K/MC structs, so they are probably pre-Ed.3 (INFERRED).
- MRP_Role values on the PNIO side are a different field: 0 disabled, 1 client, 2 manager (WS-PNIO :3608-3614). The MRP frame-side role table adds 3 = manager (auto) (WS-MRP :99-104). VERIFIED.

**Padding discrepancy (important)**
- WS-MRP applies `align4` after Link (:242), Test (:304), InTest (:497), InLink (:571) and Option (:469). It does not apply it after TopologyChange (commented out, :338-339).
- With offsets counted from the first TLV:
  - Test: 2+18 = 20, already aligned.
  - Common: 20 more, giving 40.
  - Link: 2+12 = 14. **Wireshark expects 2 padding bytes after MRP_Blocked.**
- MC builds Link with no padding (pdu.h:43-48; :519-532) and appends Common directly.
- Which one is normative is UNVERIFIED. Recommendation:
  - Emit padding to 4-byte alignment after each TLV value (it matches Wireshark and, INFERRED, the IEC layout).
  - Be tolerant on receive: look at the next type/length and accept both.
  - Test against MC as the interop peer.

### A.2 State machines and timers

**States** (MC utils.h:31-71). VERIFIED in MC; names match IEC terminology.
- MRM: AC_STAT1 = 0, PRM_UP = 1, CHK_RO = 2, CHK_RC = 3.
- MRC: AC_STAT1 = 0, DE_IDLE = 1, PT = 2, DE = 3, PT_IDLE = 4.
- MIM: AC_STAT1, CHK_IO, CHK_IC.
- MIC: AC_STAT1, PT, IP_IDLE.

**Main transitions** (OBSERVED-IN-IMPLEMENTATION MC state_machine.c)
- MRM receives its own Test (`mrp_mrm_recv_ring_test`, :844-883):
  - In PRM_UP: restart the test timer at default interval and go to CHK_RC.
  - In CHK_RO: block the secondary port, restart the test timer, send TopologyChange (interval 0 if react_on_link_change, else TOPchgT), then go to CHK_RC.
  - In CHK_RC: reset the miss counter.
- Test timer expiry with misses ≥ max: `mrp_ring_open` sets the secondary port to forwarding, ring_transitions++, sends TopologyChange and goes to CHK_RO. INFERRED from `mrp_port_ring_open` (:2284-2313) together with the K-mrp notify logic (br_mrp.c:270-338).
- MRM receives LinkDown/LinkUp: the logic is in `mrp_recv_ring_link` (:1023-1136) and depends on `blocked`, `add_test` and `react_on_link_change`. For example, in CHK_RC with LinkDown and react_on_link_change it sets the secondary port to forwarding, transitions++, sends TopologyChange(0) and goes to CHK_RO.
- MRC port link change (`mrp_mrc_port_link`, :1722-1842):
  - AC_STAT1 → DE_IDLE when the first port comes up (it becomes primary, forwarding).
  - DE_IDLE → PT when the second port comes up: start LinkUp frames, repeating LNKNRmax times at LNKupT.
  - PT/PT_IDLE/DE handle port-down by swapping primary and secondary, blocking the failed port, sending LinkDown and going to DE.
- MRC receives TopologyChange (`mrp_mrc_recv_ring_topo`, :970-1007):
  - Flush the FDB after `interval` ms.
  - In PT: set the secondary port to forwarding and go to PT_IDLE.
  - In DE: go to DE_IDLE.
- MRC default port states: the secondary port stays BLOCKED until the MRM's TopologyChange arrives (PT → PT_IDLE). OBSERVED (MC).

**Timer defaults per recovery profile**
- Sources: MC `mrp_update_recovery` (state_machine.c:2114-2210, internal unit µs) and UM (state_machine.c:2471-2554, `__ms()/__us()` macros).
- Both are OBSERVED-IN-IMPLEMENTATION. The values are believed to mirror the IEC 62439-2 tables but are UNVERIFIED against the standard.
- IEC names map to MC fields as follows:
  - TOPchgT = ring_topo_conf_interval; TOPNRmax = ring_topo_conf_max.
  - TSTshortT = ring_test_conf_short; TSTdefaultT = ring_test_conf_interval; TSTNRmax = ring_test_conf_max.
  - LNKdownT = LNKupT = ring_link_conf_interval; LNKNRmax = ring_link_conf_max.

| Profile | TOPchgT | TOPNRmax | TSTshortT | TSTdefaultT | TSTNRmax | TSTExtNRmax | LNKdown/upT | LNKNRmax |
|---|---|---|---|---|---|---|---|---|
| 500 ms | 20 ms | 3 | 30 ms | 50 ms | 5 | 15 | 100 ms | 4 |
| 200 ms | 10 ms | 3 | 10 ms | 20 ms | 3 | 15 | 20 ms | 4 |
| 30 ms | 0.5 ms | 3 | 1 ms | 3.5 ms | 3 | 15 | MC: `1` (=1 µs, likely bug); UM: 1 ms | 4 |
| 10 ms | 0.5 ms | 3 | 0.5 ms | 1 ms | 3 | 15 | MC: `1` µs; UM: 1 ms | 4 |

**Interconnection profiles (MC :2182-2210)**
- 500 ms: InTOPchgT 20 ms, InTOPNRmax 3, InTSTdefaultT 50 ms, InTSTNRmax 8, InLNKT 20 ms, InLNKNRmax 4, InLinkStatusT 20 ms, InLinkStatusNRmax 8.
- 200 ms: InTOPchgT 10 ms, InTSTdefaultT 20 ms; the rest is the same.

**Other defaults and differences between implementations**
- MC test period is 10 s (`ring_test_conf_period = 10000*1000` µs). The kernel uses it as `test_end`: the userspace must re-arm it.
- Defaults at instance creation (MC `mrp_create` :2355-2393): role MRC, prio 0x8000, recovery 500 ms, `blocked=1`, `react_on_link_change=1`.
- 30 ms / 10 ms LNK interval: MC writes `1` with a µs unit, UM writes `__ms(1)`. UM is probably correct (INFERRED).
- Where the work happens:
  - K-mrp runs only Test/InTest generation and miss detection in-kernel; everything else is in userspace.
  - UM runs the state machine fully in userspace (README); it still uses netlink to set role and port state (UM ifdriver_netlink.c:92-160).
- MRA (MC `mrp_mra_recv_ring_test` :898-917): an MRM that sees a worse-priority Test sends TestMgrNAck. An MRC resets its miss counter only on better-priority Tests.

**PNIO-side field names for the same timers** (WS-PNIO, VERIFIED)
- MrpManagerParams (0x0216, :10498-10539): Prio, TOPchgT, TOPNRmax, TSTshortT, TSTdefaultT, TSTNRmax, each u16, then pad to 4.
- MrpClientParams (0x0217, :10639-10666): LNKdownT, LNKupT, LNKNRmax, each u16.
- Units in the PNIO block are UNVERIFIED. INFERRED: ms, or 1 ms / 10 ms ticks? Check against a real capture before relying on them.

### A.3 Linux kernel bridge MRP

**Kernel version and config**
- MRP was introduced in v5.8 (MC README "at least v5.8"). Kernel git log shows the "bridge: mrp:" series in 2020-04/05. VERIFIED (README + commit dates).
- Interconnect (MIM/MIC, IN_* netlink attributes) came later. The exact version is UNVERIFIED (INFERRED ≈ v5.11).
- Needs `CONFIG_BRIDGE_MRP` (MC README).

**Configuration**
- **iproute2 has no `bridge mrp` command.** The `bridge/` directory has bridge.c, fdb.c, link.c, mdb.c, monitor.c, mst.c, vlan.c and vni.c, and no mrp.c. VERIFIED (iproute2 listing).
- Configuration goes via RTM_SETLINK on the bridge with `IFLA_AF_SPEC` → `IFLA_BRIDGE_MRP` → nested attributes (K-uapi if_bridge.h:123,164-282):
  - INSTANCE (RING_ID, P_IFINDEX, S_IFINDEX, PRIO)
  - PORT_STATE, PORT_ROLE
  - RING_STATE (RING_ID, STATE)
  - RING_ROLE (RING_ID, ROLE)
  - START_TEST (RING_ID, INTERVAL µs, MAX_MISS, PERIOD µs, MONITOR)
  - INFO (dump)
  - IN_ROLE, IN_STATE, START_IN_TEST
  - VERIFIED.

**What the kernel does**
- It generates MRP_Test (MRM, or MRA acting as MRM) and MRP_InTest (MIM) frames periodically in software (br_mrp.c:270-338, :343-413).
- It counts misses. When the count passes max_miss it notifies userspace "ring open" via `br_mrp_ring_port_open` (netlink event).
- It forwards MRP frames between ring ports according to role (br_mrp_rcv :1065-1235). The MRM does not forward ring frames.
- It processes only Test/InTest itself: "All the other MRP frames are processed by userspace application" (br_mrp.c:905-907).
- Port state and FDB flush are set by userspace.
- In short: the kernel offers a half-stack; the state machine lives in a daemon (MC/UM). VERIFIED.

**Problem on veth (INFERRED from code; not tested)**
- With `CONFIG_NET_SWITCHDEV=y` (common in distro kernels) and ports without an MRP switchdev driver (e.g. veth), `br_mrp_switchdev_set_ring_role` does two things:
  1. It first gets -EOPNOTSUPP, which maps to BR_MRP_SW.
  2. It then retries with `sw_backup=true`. That again returns -EOPNOTSUPP because `switchdev_port_obj_notify` returns EOPNOTSUPP when not handled (switchdev.c:223-245). The result is BR_MRP_NONE, so `br_mrp_set_ring_role` returns -EOPNOTSUPP (br_mrp_switchdev.c:64-98; br_mrp.c:662-686).
- Pure software MRP via the kernel therefore likely works only when `CONFIG_NET_SWITCHDEV` is not set (IS_ENABLED false → BR_MRP_SW at :78-79).
- **Must be confirmed empirically** before relying on the kernel or MC as an oracle on veth/Docker. (Confirmed 2026-10-28: on the hosted runners' kernel, switchdev built in, the MRP instance is refused with EOPNOTSUPP; a Linux 6.12 without `CONFIG_NET_SWITCHDEV` runs it on veth in Docker: go-profinet's Kernel MRP workflow.)

**Namespaces and containers**
- Bridge netlink needs CAP_NET_ADMIN in the netns (INFERRED from the rtnetlink setlink permission check, not fetched).
- Docker's default capabilities exclude NET_ADMIN, so the container needs `--cap-add NET_ADMIN`.
- Kernel features come from the host kernel. Whether Docker Desktop's LinuxKit kernel has BRIDGE_MRP or NET_SWITCHDEV is UNVERIFIED.

**MC as an oracle**
- Licence GPL-2.0. Running it as a separate process / container peer does not affect our MIT code (INFERRED; standard aggregation reading).
- Build dependencies: libnl, libev, libmnl, libcfm (microchip-ung/cfm).
- Usage: `mrp_server` daemon plus `mrp addmrp bridge br0 ring_nr 1 pport eth0 sport eth1 ring_role mrm [ring_recv 500|200|30|10] [prio …]` (MC README; mrp.c:417-437, :583).
- Status of an MC peer in a container: **implementable with caveats** (it depends on the switchdev issue above).
- Best alternative oracle: UM (GPL-2.0, fully userspace test frames). It still needs the kernel MRP role netlink unless patched with a custom ifdriver.
- Wireshark is always available as a passive decoder oracle.

### A.4 PROFINET integration (WS-PNIO, VERIFIED unless noted)

**Block types** (:1423-1431, 1454-1459)

| Block | Name |
|---|---|
| 0x0211 | PDInterfaceMrpDataAdjust |
| 0x0212 | PDInterfaceMrpDataReal |
| 0x0213 | PDInterfaceMrpDataCheck |
| 0x0214 | PDPortMrpDataAdjust |
| 0x0215 | PDPortMrpDataReal |
| 0x0216 | MrpManagerParams |
| 0x0217 | MrpClientParams |
| 0x0218 | MrpRTModeManagerData |
| 0x0219 | MrpRingStateData |
| 0x021A | MrpRTStateData |
| 0x021D | MrpRTModeClientData |
| 0x0231 | MrpInstanceDataAdjust |
| 0x0232 | MrpInstanceDataReal |
| 0x0233 | MrpInstanceDataCheck |
| 0x0234 | PDPortMrpIcDataAdjust |
| 0x0235 | PDPortMrpIcDataCheck |
| 0x0236 | PDPortMrpIcDataReal |

**Record indices** (:2213-2220; also PN pf_types.h:629-636)
- 0x8050 PDInterfaceMrpDataReal
- 0x8051 PDInterfaceMrpDataCheck
- 0x8052 PDInterfaceMrpDataAdjust
- 0x8053 PDPortMrpDataAdjust
- 0x8054 PDPortMrpDataReal
- 0x8055 / 0x8056 / 0x8057: PDPortMrpIc Adjust / Check / Real.
  - Mismatch: WS orders them Adjust 0x8055, Check 0x8056, Real 0x8057. PN orders them REAL 0x8055, CHECK 0x8056, ADJUST 0x8057. One of them is wrong; UNVERIFIED which.

**Layouts**
- PDInterfaceMrpDataAdjust (:10247-10330):
  - v1.0: pad to 4, DomainUUID(16), MRP_Role u16, pad4, LengthDomainName u8, DomainName, pad4, then sub-blocks (MrpManagerParams / MrpClientParams).
  - v1.1: 1 byte padding, NumberOfMrpInstances u8 (≤ 0x0F), then N × MrpInstanceDataAdjust blocks.
- PDInterfaceMrpDataReal (:10332-10407):
  - v1.0: pad4, UUID, Role, LengthDomainName, DomainName, **MRP_Version u16**, pad4, sub-blocks (e.g. MrpRingStateData).
  - v1.1: MRP_Version comes before LengthDomainName.
  - v1.2: padding, NumberOfMrpInstances, N × MrpInstanceDataReal.
- PDInterfaceMrpDataCheck (:10410-10464):
  - v1.0: pad4, UUID, MRP_Check u32. Bit 0 = check MRM; bit 1 = check DomainUUID (hf masks 0x01/0x02 at :23175-23183).
  - v1.1: multi-instance form.
- PDPortMrpDataAdjust/Real (:10467-10495):
  - v1.0: pad4, DomainUUID.
  - v1.1: 1 pad, MrpInstance u8, DomainUUID.
- MrpInstanceDataAdjust (:11296-11339): 1 pad, MrpInstance u8, UUID, Role u16, pad4, LengthDomainName u8, DomainName, pad4, sub-blocks.
- MrpInstanceDataReal (:11342-11386): 1 pad, MrpInstance u8, UUID, Role u16, Version u16, LengthDomainName u8, DomainName, pad4, sub-blocks.
- MrpInstanceDataCheck (:11389-11420): 1 pad, MrpInstance u8, UUID, MRP_Check u32.
- MrpRingStateData (0x0219): RingState u16. MrpRTStateData: RTState u16.
- MrpRTMode (u32): bit 0 = RTClass1_2, bit 1 = RTClass3 (:23200-23208).
- DomainName encoding: 1-240 octets with NameOfStation-like syntax (WS comment citing IEC 61158-6-10 §5.2.17.2.4).

**Diagnosis** (VERIFIED, WS-PNIO :2515-2533 range of `pn_io_channel_error_type`)
- ChannelErrorType 0x8000 "Data transmission impossible". ExtChannelErrorType (:2913-2923): 0x8000 link down, 0x8001 MAUType mismatch, 0x8002 Line Delay mismatch.
- ChannelErrorType 0x8001 "Remote mismatch". Ext (:2926-2944): 0x8004 "Peer MRP domain mismatch", 0x8005 no peer, 0x8007 peer line-delay mismatch, 0x800B peer MRP interconnection domain mismatch.
- ChannelErrorType **0x8002 "Media redundancy mismatch - Ring"**. Ext (:2947-3000): 0x80n0 manager role fail, 0x80n1 ring open, 0x80n3 multiple manager, where n = instance-1 (0..F).
- Others: 0x800B "Media redundancy with planned duplication mismatch" (MRPD); 0x8700-0x8FFF MRPD duplication void.
- The question's guesses (0x8000 / 0x8001 / 0x8002) are confirmed.
- Alarm type 0x0014 MRPD problem (PN pf_types.h:900).

**GSDML MediaRedundancy**
- Element `MediaRedundancy` with attributes `SupportedRole` (e.g. "Client", "Manager", "Manager (Auto)"), `MaxMRP_Instances`, `AdditionalProtocolsSupported`, plus the `RT_MediaRedundancySupported` attribute. **UNVERIFIED** (memory only; no public XSD reachable; PI XSD is member-only, see E).
- Verify against a publicly downloadable vendor GSDML file (vendors publish GSDMLs openly).

---

## B. PTCP (WS-PTCP, VERIFIED unless noted)

**Transport**
- PTCP runs over PROFINET RT: EtherType 0x8892 + FrameID.

**FrameID ranges** (:773-779, :792-858)

| FrameID | Meaning |
|---|---|
| 0x0020 / 0x0021 | RTSync with follow-up (clock / time) |
| 0x0080 / 0x0081 | RTSync without follow-up |
| 0xFF00 / 0xFF01 | Announce |
| 0xFF20 / 0xFF21 | FollowUp |
| 0xFF40 | DelayReq |
| 0xFF41 | DelayRes |
| 0xFF42 | DelayFuRes (Wireshark label) |
| 0xFF43 | DelayRes (follow-up) |
| 0xFF44-0xFF5F | reserved |

- Ranges: 0x0000-0x007F RTSync with FU, 0x0080-0x00FF RTSync without FU, 0xFF00-0xFF1F Announce, 0xFF20-0xFF3F FollowUp, 0xFF40-0xFF5F Delay.
- The question's assumption "0xFF41 with follow-up 0xFF42, 0xFF43 Delay.fu" does not exactly match Wireshark's labels. INFERRED semantics:
  - 0xFF41 = response without follow-up.
  - 0xFF42 = response to be followed by a follow-up.
  - 0xFF43 = the follow-up.
  - UNVERIFIED; confirm with a capture.

**Delay PDU** (:722-757)
- Header: 12 bytes padding, SequenceID u16, pad to 4 (2 bytes), Delay1ns (u32 ns; "Delay1ns_FUP"), then TLV blocks.

**TLV header** (:170-189)
- A u16 where type = bits 15..9 (`>>9`) and length = bits 8..0 (`&0x1FF`).

**Block types** (:88-100)
- 0x00 End, 0x01 Subdomain, 0x02 Time, 0x03 TimeExtension, 0x04 Master, 0x05 PortParameter, 0x06 DelayParameter, 0x07 PortTime, 0x7F Organisationally Specific.

**Block layouts**
- DelayParameter (:371-391): PortMACAddress 6, pad to 4.
- PortParameter (:342-367): pad4, T2PortRxDelay u32 ns, T3PortTxDelay u32 ns.
- PortTime (:395-413): pad4, T2TimeStamp u32 ns.
- PeerToPeerBoundary is not a PTCP TLV. It is a PNIO PDPort adjust sub-block (AdjustPeerToPeerBoundary) and is not covered by WS-PTCP. INFERRED.

**Destination MAC**
- 01-80-C2-00-00-0E for Delay PDUs: **UNVERIFIED** (not in WS-PTCP; widely documented as the LLDP / nearest-bridge address).
- That same MAC is VERIFIED as the LLDP multicast in PN pf_lldp.c:114.

**What a device minimally needs**
- p-net (certified-class CC-A/B stack, README:18-19) has an **empty** PTCP module: pf_ptcp.c is 20 lines with no code.
- In LLDP it reports line delay "not measured" (all zeros), pf_lldp.c:866-877 ("Line delay measurement is not supported").
- The LLDP PNO "measured delay values" TLV (subtype 1, OUI 00-0E-CF, length 24: RxDelayLocal, RxDelayRemote, TxDelayLocal, TxDelayRemote, CableDelayLocal) is parsed at pf_lldp.c:1451-1475.
- INFERRED: answering Delay.req is not required for CC-A/B RT_CLASS_1 certification in practice. Line delay = 0 means "not measured".
- LineDelay encoding in PDPortDataReal/CheckLineDelay (WS-PNIO :10048-10077, :22555-22573): bit 31 = FormatIndicator (1 = CableDelay, 0 = LineDelay), bits 0-30 = value in ns.
- RTSync (0x0080..) is used only for IRT (RT_CLASS_3) synchronisation. INFERRED from the frame ranges plus p-net's RT_CLASS_1-only scope.

---

## C. CC-B management: SNMP and LLDP-MIB

**PI requirement (secondary)**
- From PI public doc "PNIO-CCs_7042_V11_Mar11.pdf" (se.profibus.com, public, no login), via a search excerpt. The PDF is encrypted, so local text extraction failed: UNVERIFIED-direct.
- The CC-B table lists "Netzwerkdiagnose über IP (SNMP)" and "Topologieinformation (LLDP) mit LLDP-MIB".
- Vendors (Cognex, Allied Telesis, NXP) list MIB-II, LLDP-MIB and LLDP-EXT-DOT3-MIB for CC-B. Secondary sources.

**What p-net implements** (VERIFIED, pf_snmp.h:14-60, pnal_snmp.c:44-68)
- No built-in agent. On Linux it is a net-snmp AgentX sub-agent (net-snmp is BSD licensed, README:130-131).
- MIBs: MIB-II system group (RFC 3418), LLDP-MIB (IEEE 802.1AB-2005), LLDP-EXT-DOT3-MIB, LLDP-EXT-PNO-MIB.
- Writable objects: sysName, sysContact, sysLocation.
- Read-only objects: lldpLoc*/lldpRem* objects, lldpXPno{Loc,Rem}{LPDValue, PortTxDValue, PortRxDValue, PortNoS}, and lldpXdot3 autoneg / MAU type.
- PNO MIB OID base: 1.0.8802.1.1.2.1.5.3791 (3791 = 0x000ECF, the PNO OUI). Local table at …3791.1.2.1 (lldpXPnoLocTable.c:51). VERIFIED.
- An Observium mirror shows the LLDP-EXT-PNO-MIB text, "Copyright PROFIBUS Nutzerorganisation e.V. 2005". Its redistribution status is UNVERIFIED. Do not vendor the file; re-type the OIDs as facts.

**MIB availability**

| MIB | Availability |
|---|---|
| RFC 1213, 3418, 2863 | IETF, public, free to implement |
| IEEE LLDP-MIB (2005) | historically published at `ieee802.org/1/files/public/MIBs/LLDP-MIB-200505060000Z.txt` (referenced by NVIDIA docs). Exists, UNVERIFIED-direct |
| LLDP-V2-MIB (802.1AB-2009/2016) | likely in the same directory. UNVERIFIED |
| LLDP-EXT-DOT3-MIB | IEEE 802.1AB-2005 Annex / 802.3.1. Public via the IEEE 802.3.1 public mib_modules area. UNVERIFIED |
| LLDP-EXT-PNO-MIB | in IEC 61158-6-10 (paid). The OIDs are visible in p-net and mirrors as facts |

**Feasibility of a minimal read-only (plus 3 writable) SNMPv1/v2c agent in pure Go**
- Feasibility only; KNOWN-from-public-standards, not re-fetched.
- BER needs only a small subset of X.690:
  - INTEGER, OCTET STRING, NULL, OBJECT IDENTIFIER, SEQUENCE.
  - Application types: IpAddress 0x40, Counter32 0x41, Gauge32 0x42, TimeTicks 0x43, Counter64 0x46.
  - PDUs: GetRequest 0xA0, GetNext 0xA1, Response 0xA2, Set 0xA3, GetBulk 0xA5 (v2c).
  - v2c exceptions: noSuchObject 0x80, noSuchInstance 0x81, endOfMibView 0x82.
- Community-string check, UDP/161.
- Sorted OID tree with lexicographic GetNext.
- Estimate: about 1-1.5 kLOC plus tests. INFERRED.
- Interop oracle: net-snmp `snmpwalk` (BSD), which is fine to run.
- PI SecurityClass1 / GSDML 2.46 note: "SecurityClass1 for CC-A devices without SNMP v1/2" (see E). This implies that newer security profiles constrain SNMPv1/v2c. INFERRED.

---

## D. DHCP in PROFINET (WS-DCP, PN)

**Option and suboptions**
- DCP option 0x03 = DHCP (WS-DCP :230, 243).
- Suboptions are **DHCP option codes** (:349-361): 12 Host name, 43 Vendor specific, 54 Server identifier, 55 Parameter request list, 60 Class identifier, 61 Client identifier, 81 FQDN, 97 UUID client, 255 "Control DHCP for address resolution". VERIFIED.
  - So "option 3 suboption 12" is Host name, not a generic "DHCP" flag.
- Same codes in PN `pf_dcp.h:69-77`, with 255 commented "Defined as END in the DHCP spec". VERIFIED.

**Suboption 61 Client-ID** (WS-DCP :1340-1368)
- Content: DHCP option code byte, parameter length byte, data.
- Length 1 with data 0x01 = use the MAC address. Length 1 with any other value = use the NameOfStation.
- Length > 1 = arbitrary client-ID (1 type byte + octet string). WS comment cites IEC 61158-6-10 §4.3.1.4.21.5.

**Suboption 255 Control**
- Parameter data: 0x00 "Don't use DHCP (Default)", 0x01 "Don't use DHCP, reset DHCPOptions to factory", 0x02 "Use DHCP with the given set of DHCPOptions" (:363-367).

**IP suboption BlockInfo** (:267-276)
- 0x0000 IP not set, 0x0001 IP set, **0x0002 IP set by DHCP**, 0x0080/0x0081/0x0082 the same with "address conflict detected".
- So bit 1 = DHCP and bit 7 = conflict. VERIFIED.
- BlockQualifier on Set (:206-211): 0 = temporary, 1 = permanent.

**p-net**
- "No support for DHCP" (README:82).
- The DCP IP BlockInfo sets only bit 0. ToDo comments for bit 1 (DHCP) and bit 7 (conflict) at pf_dcp.c:405-441.
- DHCP suboptions are accepted but unimplemented (pf_dcp.c:799-812).
- `dhcp_enable` exists in config but is "Not yet supported" (pnet_api.h:1279).

**Public PI guidance on DHCP**
- Not found this session. UNVERIFIED.

---

## E. GSDML 2.46 and 2.50 (profibus.com public description page; VERIFIED page text, fetched 2026-10-27)

- Page: https://www.profibus.com/gsdml-gsdx-specification-for-profinet. Order No. 2.352, current V2.50, file `GSDML-Spec_2352_V250_May26.zip`.
- **Download is members-only** ("This area is for members only" + licence agreement).
- No statement that the XSDs are publicly downloadable. The GSD Editor 2.46 (which replaced the GSD Checker) is also members-only.
- Conclusion: the **XSDs are not publicly downloadable without login**. Do not use mirrors.

**Per-version changes (page text)**

| Version | Changes |
|---|---|
| V2.43 | additional CIM interface; APL; version-independent records; **GSDX container**; SecurityClass1 aligned with GSDX security; **SecurityClass 2 and 3 introduced** |
| V2.44 | F_BaseID record (PROFIsafe 2.6MU2); readable-record structure/metadata; ApplicationClass "Drive" and "Encoder"; SecurityClass 1-3 updates |
| V2.45 | non-contiguous channel data; SC2/3 depend on SC1; **Ed448 optional**; F_Par_CRC correction; PROFIsafeFSCP_TestMode_Supported; examples moved to VendorID 0xF000 |
| V2.46 | **I&M6 (OMLOCLocationPoint) and I&M7 (IdentificationLink)**; 10Base-T1L without APL MAUTypeExtension; import marker aligned to GSDX spec; **SecurityClass1 clarified for CC-A devices without SNMP v1/2** |
| V2.50 | restructured to mirror the XSD; **SXP transport protocol**; **ConformanceClass "E" added, "D" removed**; PoweredDevices; APL_PowerClass; configurable LinkSpeed; protocol policies; ReadableEvents; new CIM records; **ApplicationClasses "SecureAccess" and "SecureRealtime" added; SecurityClasses 2 and 3 removed** |

**Secure application classes**
- Public statement: in V2.50, security levels beyond SC1 are expressed as ApplicationClass values (SecureAccess, SecureRealtime) rather than the SecurityClass 2/3 attribute values.
- Exact attribute syntax is UNVERIFIED (spec is member-only).
- Background paper: "PROFINET Security: A Look on Selected Concepts…", doi 10.25968/opus-2934. It describes SC1 as basic hardening including GSDML signing, and SC2/3 as cryptographic protection of communication.
- No public TSN- or SNMP-element detail beyond the above was found.

---

## F. GSDX and MRCD containers

**GSDX**
- PI page "GSDX container specification": Order No. 2.752, V1.3, `GSDX-Spec_2752_V13_May26.zip`, members-only. It says GSDX is "the GSDX packaging container file format according to ISO/IEC 29500-2:2021", i.e. Open Packaging Conventions.
- Stated goals: bundle files and their referenced resources, validate integrity and authenticity, and bind each file to a vendor or publisher. VERIFIED (page text).
- XMLDSig specifically: not stated on the page. INFERRED, since OPC Part 2 defines package digital signatures based on W3C XML-DSig.
- The PI page mentions a signing tool/portal. Siemens publishes signed GSDX packages for ET 200SP (secondary).

**ECMA-376 Part 2**
- 5th edition, December 2021 (= ISO/IEC 29500-2:2021). The ecma-international.org page offers a free "Download part 2". VERIFIED (page).
- It covers core properties, thumbnail and digital signatures (Wikipedia/OPC summary; secondary).

**What a safe, generic inspector can do** (no PI-specific rules; INFERRED from public OPC)
- Use archive/zip only.
- Reject: zip64 abuse, absolute paths or `..`, excessive ratio/size, duplicate part names (case-insensitive), and interleaved pieces.
- Parse `[Content_Types].xml` (Default/Override).
- Parse `_rels/.rels` and `*/_rels/*.rels` relationships, and flag External targets.
- List parts with size, CRC and content type.
- Locate the digital-signature origin part (relationship type `…/package/2006/relationships/digital-signature/origin`) and the signature parts.
- Parse XMLDSig metadata (SignatureMethod, DigestMethod, References / Manifest with part URIs and transforms, KeyInfo X509Certificate subject/issuer/validity, SignatureTime) without validating trust.
- Optionally recompute reference digests (C14N needed for XML parts). This is the hard part in stdlib Go: exclusive/inclusive C14N must be hand-rolled, so treat it as a caveat.
- Identify GSDML parts by content type / `.xml` root element `ISO15745Profile`.

**MRCD**
- PI page "MRCD specification – Machine Readable Conformance Declaration": V1.0, Order No. 2.772, April 2026 (draft 1.0 Dec 2025 not for development), members-only.
- PI issues it as certificate authority for certified devices. It is **embedded in the GSDX container** of every certified GSDML so engineering tools can check validity. VERIFIED (search excerpt of the PI page; page not opened directly).
- Format details: UNVERIFIED / blocked (member-only).

---

## Licences of potential interop peers
- microchip-ung/mrp: GPL-2.0. giometti/umrp: GPL-2.0. Linux kernel: GPL-2.0. Wireshark: GPL-2.0+. p-net: GPLv3 or commercial. net-snmp: BSD-style.
- Running any of these as **separate processes / containers** for interop testing is fine. Reading them for facts is fine. **No code copied.**
