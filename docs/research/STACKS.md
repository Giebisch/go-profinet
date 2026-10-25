# PROFINET interop targets: stack research for otfabric/go-profinet

Research date: 2026-10-25. Everything below comes from reading source; nothing was built or run on the host. The clones are in `scratchpad/stacks/<name>`. File paths are relative to each clone.

## 0. Executive summary

| # | Target | Role (verified in source) | SPDX licence | Pin | Containerisable | Verdict |
|---|---|---|---|---|---|---|
| 1 | rtlabs-com/p-net **current `public` branch** | IO-Device (stack core only) | Dual: `GPL-3.0-only` plus commercial. `.github/README.md` also calls the tree an "evaluation version". | `ca4f37c` (2026-06-23), release `public/v1.0.2` | **No.** The tree has no CMakeLists, no ports and no sample app, so it cannot be built. | Usable only as a reference for wire constants and behaviour |
| 1a | rtlabs-com/p-net **last full GPL tree** `ea5e5540b2be55c4dfadffda39b948632c104daa` (2023-11-10). It can still be fetched by SHA from rtlabs-com/p-net and equals `kolonialno/profinet@master`. | IO-Device + `pn_dev` sample | `GPL-3.0-only` (or commercial) | That SHA, plus `cmake/tools@5f104ff`, plus osal `e960068` (FetchContent) | **Yes**, with 2 small patches for veth | **Primary device oracle** for our controller role |
| 2 | f0rw4rd/profinet-py v0.6.6 | IO-Controller (minimal) + DCP tool | `GPL-3.0-only` + commercial, CLA | `ef09fb7` / PyPI `profinet-py==0.6.6` | Yes, trivially | **Primary controller oracle** for our device role |
| 3 | core-engineering/pnio-rs v0.1.0 | IO-Device (single AR) | `MIT OR Apache-2.0` | tag `v0.1.0` = `798500c` / crates.io `pnio 0.1.0` | Yes | **Secondary device oracle**. It is permissively licensed and young, and it implements a lot less than p-net. |
| 4 | M256-Automation/profinet-python (fork of alfredkrohmer/profinet, 2015) | DCP tool + a toy RPC client | **None** (no licence file, so all rights reserved) | `306bfe2` (2021-06-08) | Technically yes | **Not recommended.** It has no licence, it is broken against compliant devices, and it is unmaintained. |
| 5 | PI PROFINET Community Stack | IO-Device | PNO Community Source Code License v1.0 (not OSI). PI members only. | n/a (PI GitLab) | Not for us | **Not usable.** It needs PI membership and a signed agreement, and the agreement forbids combining with copyleft code. |
| + | scapy `contrib.pnio*` (v2.8.0) | Packet codec only | `GPL-2.0-only` | PyPI `scapy==2.8.0` | Yes | Realistic **encoder/decoder cross-check**, for golden bytes and parse checks |
| + | Wireshark/tshark dissectors | Passive decoder | `GPL-2.0-or-later` | Debian package version | Yes | **Strongly recommended passive oracle.** Use expert info and malformed flags on every pcap. |
| + | pnio-dcp (PyPI 1.2.0, Codewerk) | DCP client only | MIT (per PyPI metadata) | `pnio-dcp==1.2.0` (2024-01-17) | Yes | A useful extra DCP-client peer: Identify, Set name/IP, Blink, Reset |

### Main surprises

1. **The public p-net repository can no longer be built.** Since 2024 the default branch `public` is an "evaluation version" (`.github/README.md`): `src/common` + `src/device` + `include` + `test` only, and no `CMakeLists.txt`, `src/ports/linux`, `sample_app/` or `samples/`.
   - Releases ship binaries instead: `p-net-1.0.2-Linux-x86_64.zip` (2.2 MB) and `p-net-1.0.2-samples.zip` on the GitHub release `public/v1.0.2`. I did not download them; downloading needs your approval.
   - `LICENSE.md` still says GPLv3 + commercial, while `.github/README.md` says "evaluation purposes only". Treat the current tree's terms as ambiguous.
   - The old full GPLv3 tree is still in the object store. `git fetch origin ea5e5540b2be55c4dfadffda39b948632c104daa` works on rtlabs-com/p-net (verified), and the tag `v0.2.0` (`3e65072`, 2022-04-19) is also complete.
   - `ea5e554` is the last upstream master commit that the public branch "caught up to" (commit `8a14ce4`).
2. **p-net fails on veth out of the box.** veth reports 10 Gbit/s, which has no MAU mapping, so `pf_pdport_is_a_fast_port_in_use` fails and Connect is rejected. profinet-py's integration Dockerfile patches this out (`tests/integration/docker/Dockerfile.profinet`), together with IOSAR acceptance and `-Werror`.
3. **p-net rejects IOCRProperties.RTClass=1 when ARProperties.StartupMode=Advanced (bit 30)**, and accepts only 2..5 there (`src/device/pf_cmdev.c:3048-3075`). Legacy startup accepts 1..5.
   - A Go controller that uses advanced startup must send RTClass=2 for unsynchronised RT, or use legacy startup.
   - pnio-rs does not check RTClass at all.
4. **ApplicationReady (CControl)**: p-net sends from local UDP **0xC001** to the controller's IP at **34964**, always in big-endian NDR. pnio-rs also sends to 34964. Both ignore the Connect source port and CMInitiatorUDPRTPort. Our controller must listen on UDP 34964.
5. **All three real stacks use RTA sequence init 0xFFFF/0xFFFE with a 15-bit wrap.** profinet-py shares one counter set across both priorities, which is a deviation from the spec.
6. **Accept-set mismatch:**
   - p-net accepts only ARType 0x0001 (IOCARSingle).
   - pnio-rs accepts only ARType 1, exactly 2 IOCRs, and a single AR.
   - profinet-py's acyclic API uses **IOSAR 0x0006** (DeviceAccess). Unpatched p-net and pnio-rs therefore reject profinet-py's read/write-only sessions; only its cyclic path (ARType 1) works against them.
7. **p-net never enforces CMInitiatorActivityTimeoutFactor.** It range-checks the value (1..1000) and ignores it.
8. **Linux bridge and LLDP:** a default Docker bridge does **not** forward 01:80:C2:00:00:0E (LLDP). Set `group_fwd_mask` to 0x4000 on the bridge, or use a direct veth pair or macvlan.
   - PROFINET multicasts (01:0E:CF:00:00:00 Identify, 01:0E:CF:00:00:01 Hello) and unicast 0x8892 frames pass a Linux bridge normally.
   - Priority-tagged (VID 0) frames are passed, but the kernel strips the 802.1Q header before an AF_PACKET socket sees it. The PCP/VID is then only available via PACKET_AUXDATA. Our Go raw-socket layer must handle both tagged and untagged reception.

---

## 1. rt-labs p-net (C, IO-Device)

### Identity, licence, maintenance

| Item | Value | Evidence |
|---|---|---|
| Default branch | `public` (GitHub default). Remote also has Gerrit-style `refs/for/master` and about 600 `refs/pull/*`. There is no `master` head. | `git ls-remote origin` |
| HEAD | `ca4f37c5d66b5378a8cf9349b9084df0612043da`, 2026-06-23, "chore: catchup to 4b522e9" | `git log` |
| History | 41 commits, all "catchup" squashes since 2024-04-11. The real development happens in rt-labs' private repo. | `git log` |
| Tags / releases | `public/v1.0.2` (`459e043`, 2025-04-24; GitHub release 2025-04-28, fixes 10 CVEs CVE-2025-32396..32405 in packet parsing; "PROFINET Test Bundle 2.45.0.2 PASS"). Also `public/v1.0.0` (2024-05-17) and `v0.2.0` (`3e65072`, 2022-04-19, full tree). | `git tag`, `gh release view` |
| Release assets | `p-net-1.0.2-Linux-x86_64.zip`, `-Linux-aarch64.zip`, `-LAN9662.zip`, `-samples.zip` (prebuilt binaries, not inspected) | `gh release view public/v1.0.2` |
| Licence | `LICENSE.md`: dual GPLv3 / commercial, with an added sentence that IPC use is "ordinary use". SPDX best fit: `GPL-3.0-only` (no "or later" wording; file headers say "dual-licensed under GPLv3 and a commercial license"). GitHub shows `NOASSERTION`. `.github/README.md` (current tree only): "evaluation version … does not contain any ports and cannot be built … can be used for evaluation purposes only". | `LICENSE.md`, `.github/README.md` |
| Submodule | `cmake/tools` → rtlabs-com/cmake-tools. It is pinned at `5f104ff` (2022-05-03) in `ea5e554` and at `45c751c` (2021-09-30) in v0.2.0. The current public tree keeps `.gitmodules` but **has no gitlink** (`git ls-tree public cmake/tools` is empty). | `.gitmodules`, `git ls-tree` |
| OSAL | Not a submodule. `cmake-tools/AddOsal.cmake` does a FetchContent of rtlabs-com/osal (BSD-3-Clause) at a short SHA: `e960068` in cmake-tools@5f104ff, `88784fc` in @45c751c, `5ec7b7e` at cmake-tools HEAD. Builds therefore need network access unless osal is vendored or `Osal_DIR` is set. | `stacks/cmake-tools/AddOsal.cmake` |
| Issues | 24 open, 603 stars, 232 forks | GitHub API |

**Which tree to use for the interop container:** `ea5e5540b2be55c4dfadffda39b948632c104daa` (2023-11-10, "Update vcap syntax"). It has `CMakeLists.txt`, `cmake/Linux.cmake`, `src/ports/linux/*`, `samples/pn_dev`, `samples/pn_shm_tool` and `samples/pn_dev_lan9662`. Licence: GPLv3 / commercial, unambiguous. It can be fetched from the upstream repo by SHA (verified in this clone, worktree at `stacks/p-net-master-ea5e554`), and it is identical to `kolonialno/profinet@master` (36 commits ahead of v0.2.0, 0 behind).

Running it as a separate process in a container is no licence issue for an MIT Go library: we only talk to it over the wire. We must not copy its code; the constants appendix below lists facts only. The v1.0.2 CVE fixes are **not** in `ea5e554`. That is acceptable for a lab oracle, but we should run our fuzz/negative tests knowing that the old stack has known OOB-write bugs.

### Build (Linux, Debian/Ubuntu), for `ea5e554`
- Requirements: cmake ≥ 3.14, gcc, make, git (for the FetchContent of osal), ca-certificates. SNMP is optional (`PNET_OPTION_SNMP` OFF by default; net-snmp is needed only when ON).
- Commands:
  ```
  git clone https://github.com/rtlabs-com/p-net && cd p-net
  git fetch origin ea5e5540b2be55c4dfadffda39b948632c104daa && git checkout ea5e5540b2be55c4dfadffda39b948632c104daa
  git submodule update --init      # cmake/tools @ 5f104ff
  cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_TESTING=OFF -DPNET_MAX_PHYSICAL_PORTS=1 [-DLOG_LEVEL=DEBUG -DPF_DCP_LOG=ON ...]
  cmake --build build --target pn_dev -j
  ```
- Known build friction: `-Werror` breaks on newer GCC, so strip it (profinet-py's Dockerfile does `sed -i 's/-Werror//g'` on all CMake files). The profinet-py recipe uses ubuntu:22.04. On Debian 12/13 expect the same `-Werror` issue. Gtest is fetched only when `BUILD_TESTING=ON`.
- Compile-time sizing: `PNET_MAX_AR` (default 2, but the comment says only 1 connection AR is supported), `PNET_MAX_SLOTS` (5), `PNET_MAX_SUBSLOTS` (3), `PNET_MAX_PHYSICAL_PORTS`, and per-module log switches `PF_*_LOG` (`CMakeLists.txt`).

### Roles and features (verified in source, current tree and ea5e554)
- **IO-Device only.** There is no controller code. The device acts as an RPC *client* only for CControl (ApplicationReady), in `src/device/pf_cmrpc.c`.

| Feature | Status | Where |
|---|---|---|
| DCP Identify (all + filters), Get, Set NameOfStation/IP/Signal/ResetToFactory/Start/Stop, Hello | Yes. Hello only if `cfg.send_hello`. | `src/common/pf_dcp.c`, `src/device/pf_cmina.c` |
| LLDP Tx + peer Rx (PNIO org TLVs), SNMP (optional) | Yes | `src/common/pf_lldp.c`, `pf_snmp.c` |
| RPC Connect / Release / Read / Write / WriteMultiple / DControl (PrmEnd) / CControl (ApplicationReady) / ReadImplicit / EPM lookup / Ping / Reject | Yes | `src/device/pf_cmrpc.c`, `pf_cmrpc_epm.c` |
| RT class 1 cyclic (PPM/CPM, software driver) | Yes. RTClass 1 in legacy mode, 2 in advanced mode. | `pf_ppm.c`, `pf_cpm.c` |
| Alarms (RTA high/low, ACK, ERR/abort), plug/pull, diagnosis alarms | Yes | `src/common/pf_alarm.c` |
| Diagnosis records (USI, channel, ext channel, qualified), logbook 0xF830 | Yes | `src/device/pf_diag.c`, `pf_cmrdr.c` |
| I&M0-4 | Yes, for the device (DAP) only, not per module (README "Limitations") | `pf_fspm.c` |
| Multiple ARs / shared device | README claims shared device. AR type gate = IOCARSingle only; build default `PNET_MAX_AR=2`. | `pf_cmdev.c:1654-1664` |
| PDPort records, PDInterfaceAdjust (LLDP mode), PTCP (delay measurement) | Partial | `pf_pdport.c`, `pf_ptcp.c` |
| Not supported | MRP, RT_CLASS_UDP, DHCP, fast startup, MC CR, shared inputs, DeviceAccess AR, iPar, time sync, ProfiSafe/ProfiDrive | README "Limitations" |
| GSDML | Static XML shipped (`samples/pn_dev/GSDML-V2.4-RT-Labs-P-Net-Sample-App-20220324.xml`). The stack does not parse GSDML. | |

### How it is driven: `pn_dev` sample (`src/ports/linux/sampleapp_main.c`)
- `pn_dev [-v…] [-i IF[,IF2…]] [-s STATION] [-p STORAGE_DIR] [-b FILE] [-d FILE] [-f] [-r] [-g]`
  - `-i`: interface(s), a comma list for multi-port
  - `-s`: station name, used only if none is stored yet
  - `-p`: absolute storage directory (default cwd)
  - `-b` / `-d`: files read as Button1 (cyclic input bit) and Button2 (cycles through alarm, diagnosis and logbook)
  - `-f`: factory reset and exit
  - `-r`: remove stored files and exit
  - `-g`: show stack state and exit
  - Verbosity is controlled with `-v` repeats (`sampleapp_main.c:60-205`).
- Identity is compile-time, in `samples/pn_dev/app_gsdml.h`:
  - VendorID 0x0493, DeviceID 0x0002, OEM 0xCAFE/0xEE02, default station "rt-labs-dev"
  - Order ID "12345 Abcdefghijk", product "P-Net Sample Application"
  - Modules: 0x30 (8 in), 0x31 (8 out), 0x32 (8 in/8 out), 0x40 (echo)
  - Submodules: 0x130, 0x131, 0x132
  - To change the identity, override the header (profinet-py copies its own `app_gsdml.h` over it).
- Observability: plain-text stdout logs (`APP_LOG_*`, plus stack `LOG_*` per module when `PF_*_LOG=ON`). There is no JSON output, so the adapter must parse stdout lines, or patch in a small JSON printer through the app callbacks (`app_*_ind` in `sampleapp_common.c`). Files written to the storage dir are `pnet_data_ip.bin`, `pnet_data_im.bin` and `pnet_data_diagnostics.bin`.
- External scripts executed by the stack (and located next to the binary or in PATH):
  - `set_profinet_leds` (signal LED, called on DCP Signal; a useful hook for observing Signal in the adapter)
  - `set_network_parameters IF IP MASK GW HOSTNAME PERMANENT` (called on DCP Set IP; uses `ip address flush/add`, so it needs NET_ADMIN)

### Networking requirements (Linux port, `src/ports/linux/pnal_eth.c`, `pnal.c`)
- AF_PACKET `SOCK_RAW` sockets per EtherType, bound to the ifindex. `SIOCSIFFLAGS` sets `IFF_MULTICAST|IFF_BROADCAST`, and `IFF_ALLMULTI` for ETH_P_ALL. It joins 01:0E:CF:00:00:00 via `PACKET_ADD_MEMBERSHIP`. It does **not** set promiscuous mode. Needs **CAP_NET_RAW + CAP_NET_ADMIN** (flag change, IP set script).
- UDP sockets on 34964 (server), 0xC003 (PNIO port in the EPM tower) and 0xC001 (CControl client).
- Reads IP, netmask and MAC via ioctls (`SIOCGIFADDR`, `SIOCGIFNETMASK`, `SIOCGIFHWADDR`), the gateway via `pnal_get_gateway`, and link speed via `SIOCETHTOOL`. The usage text says it "Assumes the default gateway is found on .1 on same subnet". **The interface should have an IP before start.** An IP of 0.0.0.0 works for DCP, but RPC needs an IP (set by DCP via the script or pre-configured).
- **veth:** `SIOCETHTOOL` reports 10000 Mb/s. The MAU type calculation has no 10G entry, so `pf_cmdev_check_pdev` / `pf_pdport_is_a_fast_port_in_use` fails and Connect is refused. Patch it as profinet-py does, or use macvlan on a real NIC.
- Works on a dedicated Docker bridge network (profinet-py proves this with `cap_add: [NET_RAW, NET_ADMIN]`, static IP 192.168.99.2). For LLDP visibility through the bridge, set the bridge's `group_fwd_mask`, or use a veth pair between two netns with no bridge.

### Wire-level choices
See Appendix A, Part 1 (all cited to `pf_cmdev.c`, `pf_cmrpc.c`, `pf_alarm.c`, `pf_dcp.c`, `pf_lldp.c`, `pf_ppm.c`). Key points:
- RTClass rule (legacy 1..5, advanced 2..5), C_SDU 40..1440, and FrameIDs: input CR in 0xC000-0xF7FF for RTC1; output CR assigned from 0xC000 when 0xFFFF is requested.
- CControl from :0xC001 → controller:34964, big-endian, retransmit every 2 s × 3. Responses are always big-endian (EPM is little-endian).
- RTA init 0xFFFF/0xFFFE, 15-bit wrap, VLAN PCP 5/6, WindowSize 1, TACK on DATA.
- DCP delay = (MAC[4..5] mod factor) × 10 ms. 3 s SAM lock. Signal 3 flashes at 1 Hz.
- LLDP:
  - every 5 s, TTL 20
  - ChassisID subtype 7 (system description in standard mode, station name in legacy mode)
  - PortID `port-001.<station>`
  - PNO PortStatus + ChassisMAC, IEEE 802.3 MAC/PHY, and Management Address IPv4 TLVs
  - no PNIO Delay TLV, no PortDescription
- CMInitiatorActivityTimeoutFactor: not enforced. The watchdog is DataHoldFactor-based (abort reason 0x05).

### Known bugs / limitations
- CVEs CVE-2025-32396..32405 (OOB writes, NULL derefs and a hang in the Connect/IOCR/AR-vendor/MCR/IR block parsers) are fixed only in v1.0.2 binaries. The buildable `ea5e554` tree is vulnerable, so malformed Connect requests from our fuzzer may crash it. Distinguish "oracle crashed" from "we are wrong".
- Only 1 connection AR in practice. I&M only on the DAP. There is no DeviceAccess AR, so profinet-py's IOSAR reads fail unless patched.
- Development is closed. The public repo receives only snapshot drops, and issues/PRs are rarely merged.

### Feasibility verdict
**Yes, with caveats.** Build `ea5e554` in a two-stage Debian/Ubuntu Dockerfile:
- pin cmake-tools 5f104ff and vendor osal e960068 (or let FetchContent run at build time)
- strip `-Werror`
- patch the fast-port check for veth
- optionally patch the AR-type gate to accept IOSAR, as a separate image variant, so that the stock behaviour stays testable

Run `pn_dev -i eth0 -s <name> -p /var/lib/pnet -vvv` with NET_RAW/NET_ADMIN.

The adapter is a small shell or Python sidecar that:
- tails stdout and maps known log lines (AR established, PrmEnd, ApplicationReady confirmed, alarm ack, DCP set) to JSON events
- reads the storage files and stub-script invocations (`set_profinet_leds` writes a marker file) to verify DCP Set and Signal

Alternative, pending your approval to download: the official `p-net-1.0.2-Linux-x86_64.zip` (2.2 MB) release binary, which has the CVE fixes. Its redistribution terms are unclear ("evaluation"), so keep it out of published images.

---
## 2. profinet-py (f0rw4rd/profinet-py), the Python IO-Controller

All paths below are relative to `stacks/profinet-py/`. Line numbers refer to commit `ef09fb7`.

### Identity, licence, maintenance

| Item | Value |
|---|---|
| Default branch / HEAD | `main` @ `ef09fb7788cfd86c2e6ee1b9bcf5165f5aac03c4`, 2026-10-07 22:08 +0200 ("fix(cyclic): skip zero-length IOCS-only objects on input") |
| Latest tag / release | `v0.6.6` (GitHub release 2026-10-07). Earlier releases: v0.6.5 and v0.6.4 (both 2026-09-15), v0.6.3 (2026-08-19), v0.6.2 (2026-04-02) |
| PyPI | `profinet-py` 0.6.6 (latest on PyPI matches the tag). License metadata on PyPI: `GPL-3.0-only` |
| Licence | **SPDX `GPL-3.0-only`** (`pyproject.toml` `license = {text = "GPL-3.0-only"}`; `LICENSE` is the GPLv3 text with a dual-licence note). A **commercial/OEM licence** is offered separately (`LICENSE-COMMERCIAL.md`, contact quellsec.dev). GitHub's licence detector reports NOASSERTION because of the preamble. A CLA is required for contributions (`CONTRIBUTING.md`, README badge). |
| Redistribution impact | Using it as an **external test peer** in a container is fine: we neither link nor ship it. Do not vendor its code into the MIT go-profinet repo. Install it in the image with `pip install profinet-py==0.6.6`, or from git at the pinned SHA. |
| History | One squashed "initial commit" on 2026-02-17, then 58 commits up to 2026-10. Active: 13 commits in each of Sep and Oct 2026. Pushed 2026-10-19. 19 stars. |
| Open issues | #26 (yearly vendor-table update) and #25 (System Redundancy support) are open. Closed: #15 "Cyclic IO startup sequence issue with a real PROFINET IO device" (v0.6.2) and #6 (commercial-licence inquiry). |
| Language / deps | Pure Python ≥3.10. Single runtime dependency: `construct>=2.10`. Entry point is `profinet = profinet.cli:main` (`pyproject.toml` `[project.scripts]`). About 20k LOC; the largest files are `profinet/rpc.py` (3397 lines) and `profinet/dcp.py` (1701 lines). |

### Roles actually implemented (checked in the source)

- **IO-Controller.** It is a real one, but minimal and partly experimental. `RPCCon` (`profinet/rpc.py:935`) builds ARBlockReq, IOCRBlockReq ×2, AlarmCRBlockReq and ExpectedSubmoduleBlockReq. It also sends PrmEnd, answers the device's ApplicationReady, runs RT_CLASS_1 cyclic TX/RX threads (`profinet/cyclic.py`) and an RTA alarm listener with acks (`profinet/alarm_listener.py`). The code labels IOCR/cyclic as "EXPERIMENTAL – NOT TESTED WITH REAL DEVICES" (`rpc.py:510`) and logs a warning at runtime (`rpc.py:1501`).
- **DCP tool.** It covers Identify (all and filtered), Get, Set (name, IP), Signal and ResetToFactory, plus sending and receiving Hello.
- **Supervisor / DeviceAccess AR.** For acyclic-only access it uses ARType **0x0006 IOSAR** with the DeviceAccess bit set (`rpc.py:1514-1529`).
- **No IO-Device role.** There is no DCP responder, no RPC server and no LLDP sender. The only "device-like" output is `dcp.send_hello()` (`dcp.py:1369`), which can **emit a device-style DCP Hello**. That is useful to test our controller's Hello handling.

### Features

| Feature | Status | Evidence |
|---|---|---|
| DCP Identify all | Yes | `send_discover` `dcp.py:1139` (FrameID 0xFEFE, multicast 01:0e:cf:00:00:00) |
| DCP Identify filtered (by name) | Yes, falls back to Identify-all and filters by name locally | `rpc.get_station_info` `rpc.py:883-920` |
| DCP Get | Yes (name, ip) | `get_param` `dcp.py:765` |
| DCP Set name / IP | Yes, permanent or temporary qualifier, and the SET response BlockError is checked | `set_param` `dcp.py:968`, `set_ip` `dcp.py:1057`, `_parse_set_response` `dcp.py:819` |
| DCP Signal | Yes. Option 5/3, BlockQualifier 0x0000, SignalValue 0x0100 | `signal_device` `dcp.py:1569` (data built ≈ `dcp.py:1595-1597`) |
| DCP Reset | Yes. Option 5/6 ResetToFactory with modes 0x0002…0x0040 | `reset_to_factory` `dcp.py:1634` |
| DCP Start/End transaction blocks (5/1, 5/2) | Constants only; not sent around SETs | `dcp.py:130-131` |
| DCP Hello | Send and receive. FrameID 0xFEFC, dst 01:0e:cf:00:00:01 | `dcp.py:81,87,1369,1476` |
| LLDP | **Not implemented.** Only LLDP peer data read from the PDPortDataReal record is parsed | `blocks.py:242`; comment `dcp.py:148` |
| RPC Connect | Yes | `connect` `rpc.py:1452` |
| RPC Release | Yes. Fire-and-forget, no wait for the response | `disconnect` `rpc.py:3341` |
| RPC Read / Write | Yes, inside an AR | `read` `rpc.py:1684`, `write` `rpc.py:1788` |
| WriteMultiple (0xE040) | Yes | `write_multiple` `rpc.py:1840` |
| ReadImplicit | Yes. opnum 5, nil AR UUID | `read_implicit` `rpc.py:1739` (nil UUID at `rpc.py:1761`) |
| Control PrmBegin / PrmEnd / RT_CLASS_3 ready | Yes, client side | `rpc.py:2928-3040`, `3322` |
| ApplicationReady (device to controller CControl) | Yes. Listens and replies DONE | `application_ready` `rpc.py:3113` |
| EPM lookup | Yes. A single ept_lookup call (max_ents 100, entry_handle 0, no continuation loop), little-endian drep | `epm_lookup` `rpc.py:657-800` |
| RT class 1 cyclic | Yes: TX/RX threads, cycle counter, DataStatus, IOPS/IOCS, watchdog | `cyclic.py`, `rt.py` |
| Alarms + AlarmAck | Yes. RTA DATA/ACK/NACK/ERR handling, TACK, retransmission, application-level AlarmAck (0x8001/0x8002); Layer 2 and UDP | `alarm_listener.py` |
| Diagnosis parsing | Channel, ExtChannel and QualifiedChannel diagnosis | `diagnosis.py`, `rpc.read_diagnosis` `rpc.py:2484` |
| I&M | Read I&M0–15, write I&M1–3, I&M0FilterData | `rpc.py:1942-2198`, `device.py:859-968` |
| Topology / identification | PDRealData, RealIdentificationData, ModuleDiff, logbook, ARInfo, index enumeration | `rpc.py:2257-2908` |
| GSDML | Parser for DAP, modules, submodules and IO data sizes. Builds `IOSlot` lists for cyclic | `gsdml.py` (`load_gsdml` :551, `build_io_slots` :179) |
| Multiple ARs | One AR per `RPCCon`. You can create several instances, but they all bind UDP 34964 for CControl (SO_REUSEADDR) | `rpc.py:1012-1016` |

### How it is driven

- **CLI** (`profinet/cli.py:590-793`): `profinet -i IFACE [-v|--debug] [-t SECS] <cmd>`. Commands are `discover`, `get-param MAC name|ip`, `set-param MAC name VALUE [--permanent?]`, `set-ip MAC ip mask gw [--permanent]`, `signal MAC`, `reset MAC [mode]`, `read NAME --slot --subslot --index`, `write NAME … HEX`, `read-inm0..3 NAME`, `read-inm0-filter NAME`, and `cyclic NAME --gsdml FILE [--cycle-ms 32] [--duration N] [--exclude-zero-io-submodules] [--submodule S:SS:ID]`. Output is human-readable text with no JSON mode, so an adapter should use the Python API.
- **Python API**: `profinet.scan(iface)` and `ProfinetDevice.discover(name, iface)` / `.from_ip(ip, iface)` (`device.py:95,348,416`). Device methods include `.read/.write/.write_multiple/.read_implicit/.read_im0..5/.read_diagnosis/.start_alarm_listener/.on_alarm/.start_cyclic(IOCRSetup)` (`device.py:1148`). Lower-level pieces are `profinet.dcp.*`, `profinet.rpc.RPCCon` and `CyclicController`. `IOCRSetup` fields are `slots`, `send_clock_factor`, `reduction_ratio`, `watchdog_factor`, `data_hold_factor` and `exclude_zero_io_submodules`. `IOSlot` fields are `api`, `slot`, `subslot`, `module_ident`, `submodule_ident`, `input_length` and `output_length`.
- **start_cyclic sequence** (`device.py:1215-1269`): release any DeviceAccess AR, then a fresh Connect (IOCARSingle + AlarmCR + ExpectedSubmodule). RT output starts **before** PrmEnd by default (`start_rt_before_prm_end=True`). PrmEnd is sent next, and `application_ready(timeout=30)` runs if `confirm_application_ready`. This ordering was introduced after issue #15.
- **Observability**: Python `logging` at DEBUG level dumps every RPC request/response in hex, block types and frame IDs. `CyclicController.stats` exposes frames sent/received, watchdog timeouts and cycle-counter tracking. Alarm callbacks receive `AlarmNotification` objects. An adapter can serialise all of these to JSON.

### Networking requirements

- Linux uses `AF_PACKET`/`SOCK_RAW` bound to the interface. The default protocol is `ETH_P_ALL`, so VLAN-tagged replies are seen (`util.py:807-834`). **Root or CAP_NET_RAW is required** for DCP, cyclic and alarms. No promiscuous mode is set and no scapy is used. Windows uses Npcap.
- RPC uses an ephemeral UDP socket for requests and a second UDP socket **bound to `0.0.0.0:34964`** for incoming CControl (`rpc.py:1009-1016`). It therefore conflicts with anything else on 34964 in the same network namespace, such as a device stack on the same host. Run it in its own container.
- EPM lookup and RPC need no root. The DCP step that learns vendor/device ID does (`examples/15_direct_rpc_no_root.py`).
- **Works on a plain Docker bridge.** The repo's own integration setup puts p-net in a container on a user-defined bridge (`192.168.99.0/24`, `cap_add: NET_RAW, NET_ADMIN`). The host-side controller talks via the `br-<id>` interface (`tests/integration/docker-compose.yml`). TX RT and alarm frames are 802.1Q priority-tagged (VID 0, PCP 6 for RT and high alarms, PCP 5 for low alarms; `rt.py:63`, `alarm_listener.py:67-68`). The default Linux bridge forwards these unchanged.

### Wire-level choices (load-bearing for interop)

| Aspect | profinet-py behaviour | Evidence |
|---|---|---|
| NDR drep (requests) | `00 00 00`, **big-endian**. Responses are parsed DREP-aware (LE `0x10` handled) | `rpc.py:1035`, `_parse_rpc_header` `rpc.py:3043`, `_send_receive` `rpc.py:1342-1400` |
| EPM drep | `10 00 00` (little-endian) | `rpc.py:725` |
| RPC header | version 4, flags1 0x20 (idempotent), InterfaceVersion 1, hints 0xFFFF, sequence number incremented per request, random activity UUID | `rpc.py:1025-1050` |
| Interface UUID | DEA00001-6C97-11D1-8271-00A02442DF7D (device). Controller …0002, supervisor …0003, param server …0004. Bytes are stored big-endian to match drep 0 | `protocol.py:192-196` |
| Object UUID | Prefix `DEA00000-6C97-11D1-8271-` + Instance 0x0001 + DeviceID + VendorID. The remote ID comes from DCP; the local one is fixed at `0001-7654-3210` | `rpc.py:973-978`, `protocol.py:198` |
| NDR ArgsMaximum | 4000 (or the payload length if larger) | `rpc.py:304,1061` |
| ARUUID / session key | `os.urandom(16)` (not RFC 4122 versioned); SessionKey random and non-zero; regenerated on reconnect | `rpc.py:969, 989, 1490-1494` |
| ARType / ARProperties | Cyclic: 0x0001 IOCARSingle, props **0x00000011** (State Active, ParametrizationServer=CM initiator). Acyclic: **0x0006 IOSAR**, props **0x00000111** (DeviceAccess). StartupMode=Legacy (bit 30 = 0) | `rpc.py:1510-1529` |
| CMInitiatorActivityTimeoutFactor | **100** (= 10 s) | `rpc.py:1539` |
| InitiatorUDPRTPort | 0x8892 | `rpc.py:1540` |
| CMInitiatorStationName | `"tp"` (hard-coded, 2 bytes) | `rpc.py:1542` |
| Block padding | Request blocks are concatenated with no inter-block padding | comment `rpc.py:1545-1548` |
| IOCRProperties | **0x00000001 → RTClass 1** (legacy RT_CLASS_1) | `rpc.py:1211` |
| FrameID | Input CR proposes **0xC000 + IOCRReference** (ref 1 → 0xC001). Output CR sends **0xFFFF** and expects the device to assign it in IOCRBlockRes. Constants: RTC1 range 0xC000–0xF7FF (`rt.py:56-57`). Docstrings and integration tests still say "0x8000–0xFBFF" (`rt.py:12,135`; `tests/integration/test_cyclic_integration.py:155-158`), which is inconsistent | `rpc.py:1247` |
| DataLength | `max(40, frame_end)`. Minimum C_SDU **40** | `rpc.py:1208`, `rt.py:149,579` |
| IOCR timing defaults | SendClockFactor 32 (1 ms), ReductionRatio 32 (→ **32 ms** cycle), Phase 1, Sequence 0, FrameSendOffset 0xFFFFFFFF, WatchdogFactor 6, DataHoldFactor 6, IOCRTagHeader 0xC000, multicast MAC 0 | `rpc.py:540-566, 1245-1258` |
| Frame layout | Per API, data objects (data + 1 IOPS) first, then IOCS (1 byte each). No-IO submodules: zero-length data + IOPS in the input CR, IOCS in the output CR | `rpc.py:1171-1205`, `rt.py:499-580` |
| Cycle counter (TX) | Starts at 0 and steps by SCF×RR (31.25 µs units) per frame, mod 2^16 | `cyclic.py:281,296,704` |
| DataStatus (TX) | 0x35 = State(primary) \| DataValid \| ProviderRun \| StationOK. Stop frames clear ProviderRun. TransferStatus 0 | `cyclic.py:709-747`, `rt.py:77-83` |
| AlarmCR request | Type 0x0001, LT 0x8892, AlarmCRProperties 0 (Layer 2, low prio bit 0), **RTATimeoutFactor 1** (100 ms), **RTARetries 3**, LocalAlarmReference 1, MaxAlarmDataLength **200**, TagHeaderHigh 0xC000, TagHeaderLow 0xA000. Block version 1.0, length 22 | `rpc.py:1091-1134`, `protocol.py:518-522` |
| RTA sequence numbers | SendSeqNum / ExpSeqNum start at **0xFFFF**; "_o" (last acked) at **0xFFFE**; increments are masked with **0x7FFF** (15-bit wrap). This is the same scheme as p-net (`p-net/src/common/pf_alarm.c:1012-1017,1121,1831`). A duplicate of the last accepted PDU is re-ACKed; an out-of-sequence PDU gets a NACK. The window is 1. **A single counter set is shared between high- and low-priority alarms**, whereas the spec keeps a separate APMS/APMR per priority | `alarm_listener.py:54-62,136-141,368-392` |
| RTA FrameIDs | High 0xFC01, low 0xFE01 | `rt.py:64-65` |
| AlarmAck | DATA PDU with TACK, carrying AlarmAck block 0x8001/0x8002 v1.0 with PNIOStatus 0. Retransmitted every RTATimeoutFactor×100 ms up to RTARetries | `alarm_listener.py:560-627,505-530` |
| ApplicationReady (CControl) | Listens on **UDP 0.0.0.0:34964** (`self._ccontrol_socket`). Accepts any REQUEST with opnum 4 and block 0x0112. It does **not** check ARUUID or source and does not deduplicate. It replies from the same socket to the sender's (ip, port) with block **0x8112**, ControlCommand **DONE 0x0008**, PNIOStatus 0, and the device's drep, object/interface/activity UUIDs and sequence number echoed. Flags1 is **0x0A** ("from the working capture"). Default wait is 30 s | `rpc.py:3113-3320` (flags at :3285) |
| PrmEnd / Release | Block 0x0110 cmd 0x0001 / block 0x0114 cmd 0x0004 (Release is fire-and-forget) | `rpc.py:3024-3041, 3341-3376` |
| DCP xid | Random 32-bit (`secrets.randbelow`). Mismatched xids are **accepted with a warning** by default; `strict_xid=True` rejects them | `dcp.py:505-507,1306-1316`; README |
| DCP ResponseDelay | Identify-all default **0x0080** (1.28 s); filtered identify also 0x0080. The reader waits for the whole timeout | `dcp.py:1139-1161,1202` |
| DCP Set qualifier | 0x0001 permanent, 0x0000 temporary | `dcp.py:1016,1098` |

### Known bugs and limitations

- Cyclic I/O is marked experimental (`rpc.py:510`). Python timing is practical only at ≥8 ms; the default is 32 ms.
- Issue #15: a real device rejected the connection or stopped RT depending on startup order. The fix was to start RT before PrmEnd and make ApplicationReady optional (now the `start_cyclic` flags). With a strict device, RT might stop after ApplicationReady.
- The **IOSAR (0x0006) DeviceAccess AR is rejected by upstream p-net**: the project patches p-net's `pf_cmdev.c` ar_type gate to accept it (`tests/integration/docker/Dockerfile.profinet`). Our device should decide whether to accept IOSAR. If it does not, profinet-py's acyclic API (`ProfinetDevice.connect`) will fail against us.
- The CControl handler does not check ARUUID or session key, and it binds 34964 globally.
- Release does not wait for a response. EPM does not page.
- No LLDP and no device role.
- One alarm sequence-counter set covers both priorities (see the table).
- The integration tests are not run in CI (`.github/workflows/ci.yml` runs unit tests only; integration tests are marked `-m integration` and need root and Docker).

### Useful by-product: a buildable p-net recipe

`tests/integration/docker/Dockerfile.profinet` builds p-net from the **kolonialno/profinet** fork. That fork of rtlabs-com/p-net was last pushed 2023-11-14 and still has CMakeLists and the Linux port; the Dockerfile clones it at `--depth 1` with no pinned commit. The build runs on ubuntu:22.04 with cmake. Three patches are applied:

1. Accept IOSAR.
2. Bypass the `pf_pdport_is_a_fast_port_in_use` speed check, because veth reports 10 Gbps and p-net has no MAU type for that.
3. Strip `-Werror`.

The configure flags are `-DPNET_MAX_AR=2 -DPNET_MAX_SLOTS=8 -DPNET_MAX_SUBSLOTS=4 -DPNET_MAX_PHYSICAL_PORTS=1`, plus DCP/LLDP/RPC/alarm logs. The binary is run as `pn_dev -i eth0 -s <name> -p /var/lib/pnet -v -v`, with stub `set_profinet_leds` and `set_network_parameters` scripts. This directly informs the p-net container recipe; note that it is relevant for target 1.

### Feasibility verdict

**Realistic, recommended controller-side peer** for testing go-profinet's **IO-Device** role. It covers DCP Identify/Get/Set/Signal/Reset, Connect/PrmEnd/ApplicationReady, Read/Write/ReadImplicit/WriteMultiple, I&M, diagnosis read, RT_CLASS_1 cyclic and alarm ack. It can also act as a DCP-Hello emitter. It cannot test our controller role, apart from Hello reception.

It containerises trivially and reproducibly: `python:3.12-slim` plus `pip install profinet-py==0.6.6`, or `pip install git+https://github.com/f0rw4rd/profinet-py@ef09fb7788cfd86c2e6ee1b9bcf5165f5aac03c4`. The only dependency is `construct`. Run with `--cap-add NET_RAW` on the same Docker bridge or veth as the device under test.

Adapter sketch: a small Python script, `adapter.py <scenario> --iface eth0 --station X`, that calls the API and prints one JSON object per step:

- `scan()` → list of {mac, name, ip, vendor/device id}
- `ProfinetDevice.discover(...)` → `read_im0()`, `read(…)`, `write(…)`, `read_implicit(…)`
- `start_cyclic(IOCRSetup(...))`: run N seconds, then dump `cyclic.stats` and input data
- `on_alarm`: collect alarms as JSON

Use `logging` at DEBUG level to a file for hex traces. Make sure nothing else in the container binds UDP 34964.

---

## 3. pnio-rs (core-engineering) — Rust IO-Device

All paths are relative to the clone at `stacks/pnio-rs/`. Unless a path says otherwise, it is under `crates/pnio/src/`.

### Identity, licence, maintenance

| Item | Value | Evidence |
|---|---|---|
| Default branch / HEAD | `main` @ `276202284f38a7a66878ec68f11381fb33b8c0f5`, 2026-10-10 (docs-only commit: the Plan 8 plan) | `git log` |
| Latest tag / release | `v0.1.0` (GitHub release 2026-08-31). The only commits after the tag are 3 documentation commits (badges, Plan 8 spec and plan). | `git log v0.1.0..HEAD` |
| crates.io | `pnio 0.1.0`, published 2026-08-31, licence field `MIT OR Apache-2.0`, rust_version 1.74, about 69 downloads | crates.io API |
| Licence (SPDX) | `MIT OR Apache-2.0`. GitHub reports `NOASSERTION` because there are two licence files, which is normal. No redistribution restrictions. | `Cargo.toml` (`[workspace.package] license`), `LICENSE-MIT`, `LICENSE-APACHE`, README "License" |
| Caveat | `captures/.gitignore` excludes the p-net sample GSDML ("GPLv3 repo, not ours to redistribute") and all pcaps | `captures/.gitignore` |
| Authorship | 203 commits, all by one author (Camille Martin). The project is about 4 months old (2026-06 to 2026-10). It is plan-driven (`docs/plans/`, `docs/design/`). Stars: 16. Open issues: 0. | `git log` |
| Clean-room claim | Written from IEC 61158/61784 and Wireshark captures. Golden frames come from an S7-1515-2 PN (TIA V21) and from **p-net v0.2.0 `pn_dev`** acting as the device. | README "Clean-room approach", `captures/README.md` |

### Language, build, dependencies
- Rust 2021, MSRV 1.74 (library only). The examples and tests need a recent stable toolchain because clap 4.6 uses edition 2024 (`.github/workflows/ci.yml`, msrv job comment).
- Runtime deps: `libc`, `nix 0.31` (net, socket, poll), `log`, `thiserror 2`, and optional `pcap-file 2` behind the default feature `capture`. Dev-deps: `clap`, `env_logger`, `roxmltree` (`crates/pnio/Cargo.toml`).
- Build: `cargo build --release --examples` (or `cargo build --example typed_bringup`). There is no C toolchain or system library dependency. A Debian `rust:1.9x` image is enough.
- Linux only. The examples print a note and exit 2 on other operating systems (`examples/typed_bringup.rs`).
- PREEMPT_RT is **not required**. `SCHED_FIFO`, CPU pinning and `mlockall` are optional, and a failure becomes a `RtEvent::SchedWarning`, not an error (`rt/runner.rs:336-355`). The timer is a `timerfd` (`rt/runner.rs:393`).

### Roles actually implemented (verified in source)
- **IO-Device only.** It has no controller code. It *calls* the controller only for ApplicationReady (`cm/mod.rs:444-500`).
- **Single AR, IOCAR only.** `ar_type != 1` is rejected (`cm/connect.rs:239`). It requires exactly 2 IOCRs (one input, one output) (`cm/connect.rs:248-255`) and `AlarmCRType == 1` (`cm/connect.rs:308`).
- A second Connect while an AR exists is refused with `connect_ar_already_exists` = `DB 81 3D 0E` (`cm/status.rs:73`). A Connect with the same ARUUID and a new SessionKey is treated as a takeover from any initiator (FOLLOWUPS.md, Plan 3 list).

### Feature matrix (source inspection)

| Feature | Status | Evidence |
|---|---|---|
| DCP Identify (All, NameOfStation filter) | Yes. It replies only to an exact name match or the AllSelector. **Any other filter block (alias, vendor/device ID, IP) gets no reply.** | `dcp/mod.rs:70-92`, `dcp/identify.rs:24` |
| Identify response blocks | NameOfStation (2/2), DeviceOptions (2/5), TypeOfStation (2/1), DeviceID (2/3), DeviceRole (2/4, value `0x0100`), DeviceInstance (2/7, value 1), IP (1/2, BlockInfo 1). The frame is untagged and ResponseDelay is 0. | `dcp/identify.rs:80-131`, `config.rs:347-361` |
| DCP Get | **No.** Get requests are logged at debug level and ignored (no response). | `dcp/mod.rs:95-102` |
| DCP Set IP | **Partial.** The interface is never changed. The reply is `Ok` only if ip, mask and gateway all equal the configured suite; otherwise `SetNotPossible (0x05)`. | `dcp/set.rs:95-118` |
| DCP Set NameOfStation / Signal / ResetToFactory | **No.** The reply is `SuboptionNotSupported (0x02)`. Planned for 0.2.0 (Plan 8). | `dcp/set.rs:114`, README "Limitations" |
| DCP Hello | FrameID `0xFEFC` is decoded only. It never sends Hello. | `dcp/frame.rs:18-29` |
| LLDP | **None.** The crate has no `lldp` module and no 0x88CC socket. Plan 8 designs one: chassis subtype 7 = NameOfStation, port subtype 7 `port-001[.name]`, TTL 20 s, PNO OUI 00-0E-CF subtypes 1/2/5, and 802.3 MAC/PHY. | file tree, `docs/design/2026-10-10-pnio-pdev-lldp-dcp-design.md:77-84` |
| RPC Connect / Write (incl. MultipleWrite 0xE040) / Control-PrmEnd / Release / Read / ReadImplicit | Yes (opnums 0-5) | `rpc/header.rs:74-87`, `cm/mod.rs:300-385` |
| ApplicationReady (device to controller) | Yes. Timeout 1 s, 3 attempts, then abort. | `cm/ar.rs:21-25,370-373,443-450` |
| EPM lookup | **No.** Any interface UUID other than the PNIO device UUID gives `RpcError::BadInterface`, and no reply is sent. | `cm/mod.rs:270-271` |
| RPC fragmentation | No (it is rejected explicitly) | FOLLOWUPS.md, Plan 3 |
| Record reads served | Only I&M0 (0xAFF0) on every known submodule and I&M1-3 (0xAFF1-3). **Everything else (0x800x diag, 0xF8xx, 0xF000 RealIdentificationData, 0xF840, 0xFBFF) is answered `DE 80 B0 00` "invalid index".** | `cm/records.rs:115-122`, `cm/status.rs:116` |
| RT class 1 cyclic | Yes: PPM/CPM, IOPS/IOCS, cycle counter, consumer watchdog, ProblemIndicator. HIL-tested at 1 ms and 500 µs. | `rt/engine.rs`, `rt/runner.rs` |
| Alarms (RTA) | Yes, but only the Low-priority channel is opened by the device. It runs one alarm in flight with retries, dedup, ACK-RTA, AlarmAck and ERR-RTA in both directions. It sends Diagnosis / DiagnosisDisappears only. **No process alarms, no plug/pull alarms.** | `device/mod.rs:648-664`, `alarm/channel.rs` |
| Diagnosis | Channel diagnosis from the application (`raise_diagnosis`/`clear_diagnosis`). USI 0x8000 (ChannelDiagnosis) and 0x8002 (ExtChannelDiagnosis). It cannot be read back through 0x800A/0x800C (invalid index). | `alarm/rta.rs:29-31`, README |
| I&M | I&M0 read-only. I&M1-3 read/write with an optional file store. **No I&M4.** `IM_Supported = 0x000E`. | `im.rs:15-35` |
| Multiple ARs / shared device | No | above |
| ModuleDiffBlock | **No.** Any ident or length mismatch rejects the whole Connect (`DB 81 <block> 04/06/07`). | `cm/connect.rs:262-302` |
| GSDML | It generates GSDML V2.4 from the typed `DeviceConfig` (`gsdml.rs`, example `gen_gsdml --out --station --vendor-id --device-id --interval`). | `examples/gen_gsdml.rs:8-25` |

### How it is driven
- The library has no daemon or config file. There are 5 examples (`crates/pnio/examples/`):
  - `typed_bringup --iface eth0 --ip A.B.C.D [--station pnio-dev] [--rt-priority N] [--cpu N] [--lock-memory] [--duration S] [--csv PATH] [--diag slot:ch:error-name]... [--im-store PATH] [--stats-every S]`. It uses a fixed sample model: inputs in slot 1 (16 REAL) and slot 2 (32 BOOL), outputs in slot 3 (16 REAL) and slot 4 (32 BOOL). The loop echoes outputs back to inputs and prints `VERDICT: PASS/FAIL` at exit (`examples/linux/typed_bringup.rs:13-62,98-115,483-488`).
  - `ar_bringup --iface --ip [--name rt-labs-dev]` **clones the p-net sample identity**: vendor 0x0493, device 0x0002, `DeviceModel::pnet_sample` with slots 0-4, module IDs 0x30/0x31/0x32/0x40 and submodule IDs 0x130 and up. Any controller config written for p-net `pn_dev` connects to it unchanged (`examples/linux/ar_bringup.rs:55-80`, `cm/model.rs:77-130`). It runs AR establishment only (`rt: None`, so no cyclic data).
  - `rt_bringup`, `latency_probe`, `gen_gsdml`.
- Station name, vendor/device ID and modules are set in Rust through `DeviceConfig::builder(name).identity(v,d).input(Slot, &[FieldType]).output(...)` (`config.rs:397-440`). The default identity is `0xFFFF/0x0001`. Generated idents are module `0x100+slot` and submodule `0x1` at subslot 1. The DAP is slot 0, module 0x1, with subslots 1/0x8000/0x8001 (`config.rs:313-334`). Only `min_device_interval` 16 or 32 is accepted (`config.rs:411-418`). **A configurable harness needs a small custom Rust binary** (around 100 lines on top of `IoDevice`) that reads a JSON model.
- Observability: `log` + `env_logger` (`RUST_LOG`), the `on_state_change` callback (`AR state: Connected/AppReadySent/Data/Idle (abort: reason)`), `IoDevice::{ar_state,last_abort,rt_stats,alarm_stats,ready}`, and per-interval CSV (`--csv`). All of these can be turned into JSON easily.

### Networking requirements
- Two `AF_PACKET SOCK_RAW` sockets bound to EtherType 0x8892 on the interface (one acyclic, one RT). Each joins multicast `01:0e:cf:00:00:00` and has a classic BPF filter: RT FrameID `0x8000-0xBFFF`, acyclic `0xFC00-0xFFFF` (`eth/afpacket.rs:47-120`, `eth/bpf.rs:88-96`). It does not use promiscuous mode or ALLMULTI.
- UDP socket `0.0.0.0:34964`, used both to serve requests and to send ApplicationReady (`api.rs:202`, `rpc/udp.rs`).
- MAC is read from `/sys/class/net/<iface>/address` (`api.rs:877`). **IP is not read from the interface.** It is passed as `--ip`, reported in DCP and used for the DCP Set comparison. The OS interface must already carry that IP to receive the RPC. Subnet is hard-coded to /24 and gateway equals the device's own IP (`config.rs:356-358`).
- Capabilities: `CAP_NET_RAW` (plus `CAP_NET_ADMIN`, as the README says). `CAP_SYS_NICE` and `CAP_IPC_LOCK` are needed only for the optional RT tuning.
- No hard-coded storage paths. The I&M store file is optional (`--im-store`).
- Docker: it should work on a user-defined bridge or veth pair, because it binds a named interface and frames can be unicast or the 01:0e:cf multicast. Cyclic and alarm frames are sent **802.1Q-tagged, VID 0** (`rt/frame.rs:17-18,176`, `alarm/rta.rs:20-23`). Linux bridges pass priority-tagged frames. Received tags are stripped by the kernel and the device accepts both tagged and untagged frames. Not tested here (no build or run).

### Wire-level choices (with citations)

| Topic | pnio-rs behaviour | Evidence |
|---|---|---|
| IOCRProperties / RTClass | **Parsed, not decoded or validated.** RTClass 1 and 2 are both accepted. | `cm/block.rs:276-277,336` |
| FrameID | Must be `0x8000-0xBBFF` for both CRs, otherwise reject `(IOCRBlockReq, 6)`. Output CR `0xFFFF` means the device picks the first ID in `0x8001-0xBBFF` that differs from the input ID. An input CR of `0xFFFF` is rejected. | `cm/connect.rs:183-195,256-261,313-318` |
| DataLength | No explicit 40-byte minimum on Connect. It checks that every IODataObject (`offset+len+1`) and IOCS fits, otherwise `(IOCR,5)`. On TX the C-SDU is zero-padded to `CSDU_MIN = 40`. | `cm/connect.rs:206-235`, `rt/frame.rs:20,104-160` |
| RT frame acceptance | The frame's source MAC must equal the CMInitiatorMAC and its FrameID must equal the output CR's | `rt/engine.rs:309` |
| Data status TX | `0x35` (Run, Primary, Valid, OK). Bit 5 is cleared while a diagnosis is active. (p-net sends `0x36`, as the test comment notes.) | `rt/frame.rs:56`, `rt/engine.rs:274,499` |
| Watchdog | `DataHoldFactor × SendClockFactor × ReductionRatio × 31.25 µs`. **WatchdogFactor is ignored** ("inert" per the docs commit). On expiry it sends ERR-RTA code2 `0x05` (DHT/WDT). | `rt/layout.rs:190-201`, `device/mod.rs:1320` |
| CMInitiatorActivityTimeoutFactor | × **100 ms**. The deadline is armed at Connect and re-armed on each Write while Connected, and it aborts if PrmEnd is not reached in time. (A factor of 0 would abort at once.) | `cm/ar.rs:26-29,265,342-346` |
| ApplicationReady CControl | Sent from the device's single UDP socket (src port 34964) to **controller IP : 34964**. The IP is learned from the Connect source address. **The controller's Connect source port is not used**, so the controller must listen on 34964. Request uses drep big-endian, interface `DEA00002-6C97-11D1-8271-00A02442DF7D`, object = the controller's CMInitiatorObjectUUID, activity UUID `14af198a-1234-1056-8079-<MAC>` (stable for the life of the process), seq_num increasing across ARs, opnum 4, flags1 idempotent. | `cm/mod.rs:315,444-500`, `config.rs:364-370` |
| RTA sequence numbers | SendSeqNum starts at **0xFFFF**, so the first DATA is 0xFFFF and then counts 0,1,2… with `& 0x7FFF` wrap. AckSeqNum starts at **0xFFFE** until a DATA is accepted. A duplicate peer seq is re-ACKed. AlarmSpecifier.sequence wraps at 0x7FF. PDU version nibble must be 1. | `alarm/rta.rs:23-27,89-96,107-109`, `alarm/channel.rs:163-171,298-322,363-372` |
| Alarm FrameIDs / VLAN | High `0xFC01` TCI `0xC000`, Low `0xFE01` TCI `0xA000`. ERR-RTA is always sent on Low. | `alarm/rta.rs:15-23,40-43` |
| AlarmCR | RTA timeout = factor × 100 ms (minimum 1), retries from the request. The device's LocalAlarmReference is **0**. MaxAlarmDataLength reply is 200, and alarms are capped at the controller's value. | `device/mod.rs:154,654-664`, `cm/connect.rs:334,378-385` |
| ERR-RTA PNIOStatus | `CF 81 FD <code2>`. Codes used: 1 DHT, 3 alarm send failed, 5 DHT/WDT expired, 11 alarm indication error, 17 AR removed. | `cm/status.rs:144-157` |
| Connect response | ARBlockRes (CMResponderUDPRTPort = 0x8892), IOCRBlockRes input then output, AlarmCRBlockRes, ARServerBlockRes (station name, padded to 4). **No ModuleDiffBlock.** | `cm/connect.rs:344-397` |
| UUIDs | Object `DEA00000-6C97-11D1-8271-{instance:2}{device:2}{vendor:2}`, device interface `DEA00001-…`, controller interface `DEA00002-…` | `rpc/uuid.rs:10-34` |
| NDR / drep | Parses headers in either drep (`Drep::from_byte`). **Responses and calls are always sent big-endian**, whatever drep the request used. Responses echo the request's object, interface and activity UUIDs. | `rpc/header.rs:171-183`, `cm/mod.rs:478,546-553` |
| DCP response delay | It ignores the request's ResponseDelay and **replies immediately**, with ResponseDelay 0 | `dcp/identify.rs:130`, `dcp/mod.rs:83-90` |
| DCP Set response | Option 5 / suboption 4 per block `[opt, sub, BlockError]`, untagged | `dcp/set.rs:120-151` |
| I&M0 layout | 60 bytes: BlockHeader(0x0020, len 56, v1.0), VendorID, OrderID (20, space-padded), Serial (16, default `PNIO-XXYYZZ` from the MAC), HW rev, SW rev (prefix char + 3 bytes), RevCounter, ProfileID, ProfileSpecificType, IM_Version **1.1**, IM_Supported 0x000E | `im.rs:170-191`, `config.rs:378-382` |
| Other PNIO status values | Write invalid index `DF 80 B0 00`. Write wrong AR `DF 81 3D 03`. Control wrong state `DD 81 3D 03`. Service unsupported `81 81 05 00`. Read wrong AR `DE 81 3D 03`. Write invalid param `DF 81 B0 02`. Resource `DF 80 C3 00`. The author marks these as "to re-verify". | `cm/status.rs:64-145` |

### Fixtures
- **pcaps are not in the repo** (`captures/.gitignore`). The table in `captures/README.md` lists what the author holds locally: an S7-1500 with p-net v0.2.0 covering AR connect, RTC1, alarm, release and device loss.
- **Hex golden frames in the repo** (usable as MIT-licensed test vectors). Provenance is documented in `docs/{dcp,cm,alarm}-golden-frames.md`.
  - `crates/pnio/testdata/cm/`: connect, write, prmend, appready, dcp_set, and `ident_ok_pnet` (all `_req`/`_res` pairs).
  - `testdata/alarm/`: about 20 RTA frames covering ACK, ERR, diag appear/disappear and I&M0 read.
  - `testdata/rt/`: RTC1 frames 0x8000 and 0x8001.
  - `testdata/gsdml/sample-16real-32bool.xml`.

### Known limitations and bugs
These come from the README, CHANGELOG and FOLLOWUPS, plus source inspection:
- No DCP name/Signal/Reset/Get, no LLDP, no EPM, and a single AR.
- No ModuleDiffBlock, no diagnosis record reads, no I&M4, no process alarms.
- No RPC fragmentation. The RPC response cache is never flushed on abort.
- The IP subnet is fixed at /24.
- The Write response's outer `record_data_length` is 0. This mirrors p-net and is kept for byte-exactness.
- The device always replies big-endian. A strict little-endian-only controller would need to accept BE responses, which DCE-RPC permits.
- Active, but this is a young single-maintainer project. Version 0.2.0 (Plan 8) is designed but not implemented.

### Feasibility verdict
**Good as a containerised device target for the DCP-Identify, Connect, Write, PrmEnd, AppReady, RTC1, alarm and I&M0-3 paths.**
- Pin the crate to tag `v0.1.0` = `798500c1bf3726a1bf015c43f39c6ae2a2d55b28` (2026-08-31), or to the crates.io `pnio =0.1.0` tarball. Commits after the tag are docs-only.
- Image: `rust:<stable>-bookworm` multi-stage build. Run with `--cap-add NET_RAW --cap-add NET_ADMIN` on a veth or bridge network, with the IP preassigned by Docker.
- Adapter: a small `pnio-harness` binary that takes a JSON device model, starts `IoDevice` (`rt: Some(RtOptions{cpu_pin:None, rt_priority:None, lock_memory:false})`) and logs JSON lines on state changes, RT stats and alarm stats. Alternatively run `ar_bringup` (p-net identity) or `typed_bringup` and parse their `env_logger` output and `VERDICT:` line.
- It is not useful for DCP Set-name/Get, LLDP, EPM or diagnosis-read tests. Those tests must target p-net.
- Two wire facts matter for go-profinet as controller:
  - The controller must accept ApplicationReady on UDP **34964**.
  - Responses are always big-endian NDR.

---

## 4. profinet-python (M256-Automation): Python DCP/RPC tooling

### Identity, licence, maintenance
| Item | Value | Evidence |
|---|---|---|
| Repo | GitHub fork of `alfredkrohmer/profinet` (originally `devkid/profinet`, Alfred Krohmer, 2015). The fork adds only an Italian-language README. | `gh api` (`fork: true`, parent alfredkrohmer/profinet); `git log` |
| HEAD | `306bfe2e60f57d4fea0b0e7bca8a8920f0daa335`, 2021-06-08 (README). The last code commit is `ffaae64`, 2015-07-17. | `git log` |
| Tags / releases | None | |
| Licence | **None**: no LICENSE file in the fork or its upstream (GitHub `license: null`). By default all rights are reserved, so it cannot legally be redistributed in our images. | repo tree |
| Size | 854 lines of Python (`dcp.py`, `rpc.py`, `protocol.py`, `util.py`, `main.py`, `server.py` with Flask web UI) | `wc -l` |

### Roles / features (source)
- **DCP client:** Identify-All (`dcp.py:60-66`). Identify-by-name (`send_request`, `dcp.py:69-75`). Get (`get_param`, FrameID 0xFEFD) and Set (`set_param`) of the options in the `params` table: name and IP only.
  - Bugs: Set pads the DCP length for odd values but never appends the pad byte (`dcp.py:40-57`). XID is fixed at 0x012345. Every request is sent with an 802.1Q tag of TCI 0.
- **RPC client (toy):**
  - Connect sends **only an ARBlockReq**: ARType 0x0006 (IOSAR), fixed ARUUID 00112233…eeff, activity UUID = ARUUID, ARProperties 0x131, timeout factor 100, InitiatorUDPRTPort 0x8892, station "tp". It sends no IOCR, AlarmCR or ExpectedSubmodule blocks and ignores the response (`rpc.py:62-90`).
  - Read and ReadImplicit (`rpc.py:92-124`), Write (`rpc.py:126-138`), I&M0 filter 0xF840 parse (`rpc.py:140-163`), I&M0/I&M1 structs (`protocol.py`).
  - drep `00 00 00` (big-endian); object UUID DEA00000-6C97-11D1-8271-0001<dev><vendor> (`rpc.py:23-26, 33-53`).
- No device role, LLDP, cyclic, alarms or EPM.
- Driven by `python3 main.py -i IF {discover|get-param|set-param|read|read-inm0|read-inm1|write-inm1|read-inm0-filter} [mac] [param] [value]…`. Output is human text; Flask UI via `server.py -i IF`.
- Networking: AF_PACKET `SOCK_RAW` bound with protocol 3 (ETH_P_ALL), so root/CAP_NET_RAW is needed. It reads the MAC via ioctl 0x8927 (SIOCGIFHWADDR).

### Verdict
**Not realistic as a target:**
- There is no licence.
- Connect is IOSAR-only and incomplete, so p-net and pnio-rs reject it. Only ReadImplicit would work against a device.
- It has not been maintained since 2015.

Its only value is as a legacy DCP Identify/Set client, and that role is better covered by `pnio-dcp` (MIT) or profinet-py.

---

## 5. PI PROFINET Community Stack (and other PI-provided stacks)

- **Access:** free for PI members only. You sign the "PNO Community Source Code License Agreement" (v1.0, 2021-08-19) and then get access to PI's GitLab (gitlab.profibus.com). Nothing is public on GitHub.
- **Licence:** non-exclusive, royalty-free, perpetual licence to use, modify and distribute.
  - Object code may ship in products. Source may be sublicensed only for porting to a specific hardware platform.
  - **Combining with copyleft code in a way that requires disclosing its source is not permitted.**
  - German law; no support. Not an OSI licence.
- **Roles:** primarily IO-Device (demo app on Raspberry Pi 4, Debian 13). PI says a "virtual IO-Device for Linux containers" is forthcoming. Some PI pages also mention controller functionality. Supports IRT with a user-provided driver. v3.0.0 adds SXP/RTAv3 (spec 2.5).
- **Verdict for otfabric:** **not usable** as a public CI target. It needs organisational PI membership and a signed agreement, the binaries/sources may not be redistributed in public images, and results could not be reproduced by outside contributors. If the user's organisation is a PI member, it could serve as a *private* manual oracle only. Siemens/Hilscher/Phoenix SDKs and CODESYS are excluded (proprietary).
- Sources: https://www.profinet.com/profinet-explained/community-stack, https://profibus.com/download/profinet-community-stack, https://www.profibus.com/newsroom/press-news/profinet-community-stack-1

---

## 6. Other open implementations considered

| Candidate | What it is | Licence | Realistic? | Notes |
|---|---|---|---|---|
| **Wireshark / tshark** | Dissectors: `plugins/epan/profinet/packet-pn-dcp.c`, `packet-dcerpc-pn-io.c` (Connect/Read/Write/Control/alarm blocks, records), `packet-pn-rt.c` (RT frames, DataStatus), `packet-pn-ptcp.c`, `packet-pn-mrp.c`, `packet-pn-rsi.c`, `packet-pn-sxp.c`, `packet-pn.c`, plus `epan/dissectors/packet-dcerpc.c`, `packet-dcerpc-epm.c` and `packet-lldp.c` (PNO TLVs) | `GPL-2.0-or-later` | **Yes, strongly recommended** | Passive oracle: run `tshark -r x.pcap -T json` (or `-T ek`) and fail on `_ws.malformed` / `_ws.expert` errors. Optional GSDML folder preference for module naming. Pin by Debian package version (Debian 13 ships 4.4.x). No licence issue because it is a separate process. |
| **scapy `scapy.contrib.pnio`, `pnio_dcp`, `pnio_rpc`, `lldp`** (v2.8.0, 2026-10-02) | Codec classes: ProfinetIO/RT cyclic PDU + IOxS; DCP blocks (IP, FullIP, Name, DeviceID, Role, Options, Alias, Instance, OEMID, Control, DeviceInitiative); RPC blocks (ARBlockReq/Res, IOCRBlockReq/Res, AlarmCRBlockReq/Res, ExpectedSubmoduleBlockReq, IODControlReq/Res, IODRead/Write/WriteMultiple, IM0-IM4, PDPortDataAdjust sub-blocks, AlarmNotification + RTA header, PROFIsafe); drep-aware NDR (`dce_rpc_endianness`) | `GPL-2.0-only` | **Yes, as a cross-codec** | No state machines, so it is not a peer. Use it in a Python sidecar to (a) decode our frames and (b) build independent golden frames. Note gaps: no Release block, ModuleDiffBlock or diagnosis records. |
| **pnio-dcp** (PyPI 1.2.0, gitlab.com/pyshacks/pnio_dcp, Codewerk GmbH) | DCP client: identify_all, identify(mac), set name/IP (permanent/temporary), get, blink, reset_to_factory. Raw sockets on Linux (root). | MIT (PyPI metadata, "MIT © 2020-2023 Codewerk GmbH") | **Yes** | A second independent DCP client for our device role. Last release 2024-01. Deps: psutil. Note: its constructor needs the host IP to select the interface. |
| freeneta (avbxl/freeneta) | Python DCP discovery/commissioning tool | none listed on GitHub | Maybe | Unlicensed, so the same problem as #4 |
| naresh97/OpenProfinet, DaCookie4u/python-profinet, st-one-io/pn-tools | Small GPL-3.0 tools / abandoned daemons (2018-2021) | GPL-3.0 | No | Stale and incomplete |
| fbarresi/ProfinetTools | C#/.NET DCP tool (Windows, WinPcap) | MIT | No | Windows-centric |
| langmo/profipp | C++ wrapper around p-net | GPL-3.0 | No | Adds nothing over p-net |
| Zeek: `amzn/zeek-plugin-profinet`, `cisagov/icsnpp-profinet-io-cm` | Passive parsers for DCP / CM RPC | BSD-3-Clause | Optional | A second passive oracle with structured logs. Coverage is narrower than Wireshark's. |
| f0rw4rd/oida | OT scanner/fuzzer CLI (same author as profinet-py) | unclear (`other`) | Optional | Possibly useful for robustness tests later. The licence needs checking. |
| "OpenPNIO" | No such maintained public project was found | n/a | No | |
| PI Community Stack, Siemens PN Driver, CODESYS, Hilscher, TwinCAT | Proprietary or membership-gated | n/a | No | |

---

## 7. Recommended interop matrix and harness notes

| go-profinet role under test | Peer(s) | What it exercises |
|---|---|---|
| **IO-Controller** (ours) | p-net `pn_dev` @ ea5e554 (patched for veth); pnio-rs `ar_bringup`/`typed_bringup` @ v0.1.0 | DCP Identify/Set name/IP/Signal (p-net only for Set/Signal/Reset), Connect → PrmEnd → ApplicationReady (we must listen on :34964), RT class 1 cyclic, alarms + ack (p-net Button2 file triggers alarm/diag), Read I&M0-4, diagnosis, EPM lookup (p-net only), LLDP reception (p-net only) |
| **IO-Device** (ours) | profinet-py 0.6.6 (controller, cyclic via ARType 1; acyclic via IOSAR, so we decide whether to support IOSAR); pnio-dcp 1.2.0 (DCP client) | DCP server incl. Hello reception, Connect parsing of Python-built blocks, CControl to its :34964, cyclic, alarm ack, record reads/writes |
| Both | tshark (pinned), scapy 2.8.0 codecs | Passive validation of every captured exchange, plus independent golden frames |

Harness networking: use a dedicated user-defined bridge per test, or better, a direct veth pair between two netns. Give each container NET_RAW + NET_ADMIN and a static IPv4 on the PROFINET interface. For LLDP over a bridge, set `/sys/class/net/<br>/bridge/group_fwd_mask` to 0x4000. Disable multicast snooping if it is relevant (it only affects IP multicast). Expect AF_PACKET reception to have the VLAN tag stripped (PACKET_AUXDATA).

---

# Appendix A: p-net interop behaviours and wire constants reference

Source: `rtlabs-com/p-net`, branch `public`, commit `ca4f37c5d66b5378a8cf9349b9084df0612043da` (2026-06-23), the "evaluation" tree with no ports. Paths are relative to the repo root. The tables list facts (names, values, meaning) taken from the source; no code is copied. Where a value comes from the PROFINET standard and p-net does not define it, the table says so.

---

### Part 1: p-net interop behaviours (what a fresh controller or device must match)

### 1.1 Connect / AR checks (`src/device/pf_cmdev.c`)

| Behaviour | Detail | Cite |
|---|---|---|
| Accepted ARType | **Only 0x0001 (IOCARSingle)**. Every other ARType is rejected. Supervisor AR 0x0006 is checked in one other path but is not accepted by `pf_cmdev_check_ar_type`. | pf_cmdev.c:1654-1664 |
| CMInitiatorObjectUUID check | Must start `DEA00000-6C97-11D1-8271-…`. The last 6 bytes are not checked. | pf_cmdev.c:1602-1615 |
| CMInitiatorActivityTimeoutFactor | Range-checked 1..1000 (unit 100 ms). **No CMI activity timer uses it anywhere in the tree**: it is only parsed, checked and logged. | pf_cmdev.c:1771-1772; pf_block_reader.c:382; pf_cmrpc.c:1286 |
| IOCR LT field | Must be 0x8892 for non-UDP RT classes (0x0800 for RT_CLASS_UDP). Otherwise ErrorCode1 0x02 / ErrorCode2 6. | pf_cmdev.c:3024-3047 |
| IOCRProperties.RTClass (bits 0-3) | **Legacy startup** (ARProperties bit 30 = 0): 1..5 accepted. **Advanced startup** (bit 30 = 1): 2..5 accepted, so RTClass=1 is rejected with ErrorCode1 0x02 / ErrorCode2 7. For RT class 1 traffic in advanced mode a controller must send RTClass=**2**. | pf_cmdev.c:3048-3075 |
| C_SDU (DataLength) | RTC1/2/3/STREAM: **40..1440**. UDP: 12..1440. Error ErrorCode2 8. | pf_cmdev.c:3094-3110 |
| Input-CR FrameID (device→controller, set by the controller) | RTC1 and UDP: 0xC000-0xF7FF. RTC2: 0x8000-0xBBFF. RTC3: 0x0100-0x7FFF. | pf_cmdev.c:3132-3141 |
| MC CR FrameID | RTC1/UDP 0xF800-0xFBFF, RTC2 0xBC00-0xBFFF, RTC3 0x0100-0x7FFF | pf_cmdev.c:3113-3122 |
| Output-CR FrameID (controller→device) | When the request carries **0xFFFF**, the device picks the first free FrameID from the class range (RTC1/UDP from 0xC000, RTC2 from 0x8000, RTC3 from 0x0100) and returns it in IOCRBlockRes. | pf_cmdev.c:4660-4712 |
| SendClockFactor | Must be one of 1, 2, 4, …, 128 | pf_cmdev.c:3150-3160 |
| ReductionRatio | RTC1-3: 1..512, with 256+ only when SCF ≤ 64 and 512 only when SCF ≤ 32. UDP: 1..16384. Phase must be 1..RR. | pf_cmdev.c:3175-3215 |
| DataHoldFactor | 1..0x1E00, plus a check on the total hold time | pf_cmdev.c:3236-3255 |
| AlarmCR | LT 0x8892 (UDP transport rejected by LT check). RTATimeoutFactor 1..100 (unit 100 ms). RTARetries 3..15. MaxAlarmDataLength 200..1432. | pf_cmdev.c:4024-4070 |
| AlarmCRBlockRes | `LocalAlarmReference` = endpoint allocated by the device. `MaxAlarmDataLength` = 200 (`PF_MAX_ALARM_DATA_LEN`). | pf_cmdev.c:4884-4887; pf_types.h:1691 |
| ARServerBlock | Contains the device's current station name | pf_cmdev.c:4889-4895 |
| AR limit | `PNET_MAX_AR` is a build option (pnet_options.h.in:76). In the v0.2.0 CMake default it is 2, and the comment says only 1 connection AR is supported. | pnet_options.h.in:76 |

### 1.2 DCE/RPC (`src/device/pf_cmrpc.c`)

| Behaviour | Detail | Cite |
|---|---|---|
| Server port | UDP 34964 (0x8894) on `cmrpc_rpcreq_socket`. Responses go back to the requester's source ip:port. | pf_types.h:62; pf_cmrpc.c:4396-4400 |
| Response drep for PNIO interface | After parsing the NDR header the session is forced big-endian. **PNIO responses are always encoded big-endian (drep 0x00), whatever the request's drep.** | pf_cmrpc.c:4643-4644, 4680 |
| Response drep for EPM | Little-endian (drep 0x10) | pf_cmrpc.c:4660-4664 |
| Response flags | PT=RESPONSE(2), `no_fack`=1, `idempotent`=1 (cleared for EPM). Fragmented responses set `fragment`=1 and `no_fack`=0. | pf_cmrpc.c:4669-4675, 4726, 4780-4781 |
| Max fragment body | 1464 − 80 = 1384 bytes (MustRecvFragSize) | pf_cmrpc.c:63-72 |
| Reject PDU | PT=REJECT(6) with NCA status codes (table 2.12) | pf_cmrpc.c:4535-4545; pf_types.h:472-490 |
| Ping | Answered with RESP_PING / "no call" (PT 5) | pf_cmrpc.c:4572-4594 |
| DControl rerun | Same SequenceNumber returns the cached result | pf_cmrpc.c:2722-2764 |
| **ApplicationReady (CControl)** | The device is the RPC client. It opens a **new UDP socket bound to local port 0xC001 (49153)** (`PF_RPC_CCONTROL_EPHEMERAL_PORT`) and sends to the controller's **IP at port 34964 (0x8894)**. It does **not** use the controller's source port from Connect, nor CMInitiatorUDPRTPort. Header: drep big-endian, PT=REQUEST, idempotent=1, ObjectUUID = CMInitiatorObjectUUID from the Connect, InterfaceUUID = DEA00002-6C97-11D1-8271-00A02442DF7D (controller interface), interface version 1, opnum 4 (Control), ihint/ahint 0xFFFF, server boot time 0. Body: IOXBlockReq 0x0112 (or 0x0113 for plug), ControlCommand = bit 1 (ApplicationReady), ControlBlockProperties 0, AlarmSequenceNumber 0 (or last plug-alarm seq for 0x0113). | pf_cmrpc.c:3759-3840, 4009-4012; pf_types.h:64 |
| CControl retransmit | Timeout 2000 ms (`PF_CCONTROL_TIMEOUT`), resend counter 3 | pf_types.h:79; pf_cmrpc.c:88, 1091-1100, 4011 |
| ReadImplicit | Opnum 5 is handled without an AR (implicit AR UUID all-zero) | pf_cmrpc.c:75, 2946-2996, 3732 |
| EPM lookup response | Object UUID `DEA00000-6C97-11D1-8271-0001<DeviceID hi lo><VendorID hi lo>` (interface 0x0001). Tower points to UDP port **0xC003** (`PF_RPC_PNIO_PORT`) at the device IP. Annotation string: product name padded to 25, order ID padded to 20, HW revision as `%5u`, then SW revision as prefix char plus 3×`%3u`. EPM session timeout 5 s. | pf_cmrpc_epm.c:218-243; pf_types.h:65; pf_block_writer.c:4608-4636; pf_cmrpc.c:73 |

### 1.3 RTA / alarms (`src/common/pf_alarm.c`)

| Behaviour | Detail | Cite |
|---|---|---|
| FrameIDs | High 0xFC01, low 0xFE01 | pf_alarm.c:75-76 |
| VLAN | Alarm frames are always sent 802.1Q-tagged, VID 0, PCP 5 (low) or 6 (high) | pf_alarm.c:78-79, 1296-1307 |
| SrcRef/DstRef | SrcRef = device LocalAlarmReference (from the result). DstRef = controller LocalAlarmReference (from the AlarmCRBlockReq). | pf_alarm.c:1008-1009 |
| Initial sequence numbers | SendSeqNum **0xFFFF**, previous **0xFFFE**. Expected seq **0xFFFF**, previous **0xFFFE**. | pf_alarm.c:1012-1017 |
| Wrap | Send/expected seq advance with `(n+1) & 0x7FFF` (15-bit): 0xFFFF goes to 0x0000, then 0..0x7FFF | pf_alarm.c:1121, 1831, 1866 |
| PDU type byte | Low nibble = type (1 DATA, 2 NACK, 3 ACK, 4 ERR), high nibble = version (1) | pf_types.h:872-882; pf_block_reader.c:1026-1027 |
| AddFlags | WindowSize (bits 0-3) always 1. TACK (bit 4) = 1 on DATA (alarm notification), 0 on ACK/ERR/ack-DATA. | pf_alarm.c:1516-1517, 1596-1597, 1704-1705, 1746-1747; pf_block_reader.c:1030-1031 |
| Retransmit | Timeout = RTATimeoutFactor × 100 ms. Resends = RTARetries. | pf_alarm.c:1043-1044, 1521 |
| AlarmSpecifier SequenceNumber | 11-bit, mod 0x800, starts at 0 | pf_alarm.c:384, 2318-2321, 3029-3031 |
| Close/abort | Sends ERR-RTA-PDU on the low-prio endpoint with PNIOStatus CF 81 FD `<reason>` (reason codes in table 2.6) | pf_alarm.c:1575-1615 |

### 1.4 DCP (`src/common/pf_dcp.c`, `src/device/pf_cmina.c`)

| Behaviour | Detail | Cite |
|---|---|---|
| Identify response delay | No delay when ResponseDelayFactor is 0 or > 6400. Otherwise delay = ((MAC[4]<<8 \| MAC[5]) mod factor) × 10 ms. | pf_dcp.c:1406-1421, 1930-1958 |
| ResponseDelay in responses | Always 0 | pf_dcp.c:1013, 1256, 1563 |
| SAM (single-client lock) | After a Get/Set, Get/Set frames from a **different source MAC** are silently dropped for 3 s | pf_dcp.c:74, 257-260, 976-979, 1058, 1083 |
| Set temporary vs permanent | BlockQualifier bit 0: 0 = temporary, 1 = permanent. Temporary values are not written to the non-volatile copy. | pf_cmina.c:653, 700-716 |
| IP change with AR active | A Set IP that changes the address while an AR exists is rejected (ACC_INVALID_PARAMETER) | pf_cmina.c:~1053-1080 |
| Name change with AR active | Accepted. Active ARs are aborted with RTA reason 0x1F (DCP_STATION_NAME_CHANGED). | pf_cmina.c:~1004-1045 |
| ResetToFactory | Reset mode = BlockQualifier >> 1. Mode 1: application data and I&M. Mode 2: communication (IP 0.0.0.0, name cleared, IP and diag files cleared). Mode ≥ 3: both. FactoryReset (suboption 5) = mode 99 (all). ARs are aborted with reason 0x20. | pf_cmina.c:159-345, 889, 902 |
| Start/Stop | Stop is accepted only with the same BlockQualifier as Start | pf_dcp.c:~823-832 |
| Signal | Flashes the LED 3 times with a 0.5 s half-period (about 3 s at 1 Hz) | pf_dcp.c:69-70, 646-660 |
| Hello | Only when `pnet_cfg.send_hello` is true: 3 Hellos at 3 s intervals to 01:0E:CF:00:00:01, FrameID 0xFEFC | pnet_api.h:1396-1397; pf_types.h:1067-1069; pf_cmina.c:59-75, 547; pf_dcp.c:110-111 |
| IP BlockInfo | Bit 0 set when an IP is configured (any of ip/mask/gw ≠ 0). DHCP and IP-conflict bits are not implemented. | pf_dcp.c:412-441 |
| DeviceRole | 0x01 (IO-Device) | pf_cmina.c:185 |
| Persistent files (in `file_directory`) | `pnet_data_ip.bin` (name + IP), `pnet_data_im.bin`, `pnet_data_diagnostics.bin`, and SNMP `pnet_data_syscontact.bin`, `pnet_data_sysname.bin`, `pnet_data_syslocation.bin` | src/common/pf_file.h:28-30; src/common/pf_snmp.h:113-115 |

### 1.5 LLDP (`src/common/pf_lldp.c`)

| Behaviour | Detail | Cite |
|---|---|---|
| Destination / interval / TTL | 01:80:C2:00:00:0E, every 5 s, TTL 20 s | pf_lldp.c:114; pf_types.h:1070-1071 |
| TLV order sent | ChassisID, PortID, TTL, PNIO PortStatus (subtype 2, RTC2=0, RTC3=0), PNIO ChassisMAC (subtype 5), IEEE 802.3 MAC/PHY (OUI 00-12-0F, subtype 1), Management Address (IPv4), End. **No Port Description and no PNIO Delay TLV are sent.** | pf_lldp.c:1033-1043 |
| ChassisID | No station name (or too long): subtype 4 (MAC) with the device MAC. Legacy mode: subtype 7 with the station name. **Standard mode (default)**: subtype 7 with the system description string. | pf_lldp.c:590-625; pf_port.c:67-68 |
| PortID | Subtype 7. Standard mode: `<portname>.<stationname>`, e.g. `port-001.dut`, falling back to the MAC string when there is no name. Legacy mode: `<portname>`. | pf_lldp.c:648-688; pf_types.h:2795-2815 |
| LLDP mode switch | The controller sets Legacy/Standard via the PDInterfaceAdjust record (0x8071) | pf_pdport.c:1350-1365 |
| PNO OUI | 00-0E-CF. Subtypes 1 MeasDelay, 2 PortStatus, 3 PortAlias, 4 MRP ring port status, 5 ChassisMAC, 6 PTCP status, 7 MAU type ext, 8 MRP interconnection status. | pf_lldp.c:96-110 |
| Parsed peer TLV lengths | MeasDelay 24, PortStatus 8, ChassisMAC 10, MAC/PHY 9 | pf_lldp.c:70-73 |

### 1.6 Cyclic (PPM)

| Behaviour | Detail | Cite |
|---|---|---|
| Initial DataStatus | PRIMARY (bit 0), DataValid (bit 2), StationProblemIndicator=normal (bit 5) = **0x25**. ProviderState RUN (bit 4) is set later by the application. | pf_ppm.c:273-276, 925-929 |
| Watchdog | Consumer DHT counter. Abort reason 0x05 (CONSUMER_DHT_EXPIRED) when DataHoldFactor cycles pass with no frames. | pf_cpm_driver_sw.c:65-81; pf_cpm.c:234 |

---

### Part 2: Wire constants reference

### 2.1 Ports, EtherTypes, MACs, FrameIDs

| Item | Value | Cite |
|---|---|---|
| EtherType PROFINET / LLDP / VLAN / IP / ARP | 0x8892 / 0x88CC / 0x8100 / 0x0800 / 0x0806 | include/pnal.h:115-121 |
| RPC server port | 34964 (0x8894) | pf_types.h:62 |
| UDP RT port (CMInitiatorUDPRTPort default) | 0x8892 | pf_types.h:63, 1416 |
| CControl local port | 0xC001 | pf_types.h:64 |
| EPM tower PNIO port | 0xC003 | pf_types.h:65 |
| DCP Hello FrameID / Get-Set / Identify req / Identify res | 0xFEFC / 0xFEFD / 0xFEFE / 0xFEFF | pf_dcp.c:64-67 |
| DCP Hello multicast | 01:0E:CF:00:00:01 | pf_dcp.c:110-111 |
| DCP Identify multicast (standard) | 01:0E:CF:00:00:00 (standard value, not a named constant in pf_dcp.c) | n/a |
| Alarm FrameID high / low | 0xFC01 / 0xFE01 | pf_alarm.c:75-76 |
| LLDP multicast | 01:80:C2:00:00:0E | pf_lldp.c:114 |
| RT FrameID ranges | RTC3 0x0100-0x7FFF. RTC2 0x8000-0xBBFF (MC 0xBC00-0xBFFF). RTC1/UDP 0xC000-0xF7FF (MC 0xF800-0xFBFF). | pf_cmdev.c:3113-3141 |

### 2.2 DCP

**ServiceID** (pf_dcp.c:50-53): Get 0x03, Set 0x04, Identify 0x05, Hello 0x06.
**ServiceType** (pf_dcp.c:57-59): Request 0x00, Response Success 0x01, Response "not supported" 0x05.
Header: 10 bytes (ServiceID, ServiceType, Xid u32, ResponseDelay u16, DCPDataLength u16). Block header: 4 bytes (Option, Suboption, BlockLength). Padding byte 0x00. (pf_dcp.c:55-62, 86-104)

| Option | Value | Suboptions (value: name) | Cite |
|---|---|---|---|
| IP | 0x01 | 1 MAC (R), 2 IPParameter (R/W), 3 FullIPSuite (R/optional W) | pf_dcp.h:47-52 |
| DeviceProperties | 0x02 | 1 Vendor (TypeOfStation), 2 NameOfStation (R/W), 3 DeviceID, 4 DeviceRole, 5 DeviceOptions, 6 AliasName (filter only), 7 DeviceInstance, 8 OEM DeviceID, 9 StandardGateway | pf_dcp.h:54-65 |
| DHCP | 0x03 | 12 Hostname, 43 VendorSpec, 54 ServerID, 55 ParReqList, 60 ClassID, 61 ClientID, 81 FQDN, 97 UUID ClientID, 255 Control (not advertised) | pf_dcp.h:67-78 |
| Reserved | 0x04 | n/a | pf_dcp.h:32 |
| Control | 0x05 | 1 Start, 2 Stop, 3 Signal, 4 Response, 5 FactoryReset, 6 ResetToFactory | pf_dcp.h:80-88 |
| DeviceInitiative | 0x06 | 1 DeviceInitiative support (not advertised) | pf_dcp.h:90-93 |
| Manufacturer specific | 0x80-0xFE | n/a | pf_dcp.h:35-38 |
| All | 0xFF | 0xFF All | pf_dcp.h:39-45 |

DeviceOptions advertised in the Identify response: table at pf_dcp.c:133-154.

**BlockError** (Control/Response block) (pf_dcp.h:95-103): 0 no error, 1 option not supported, 2 suboption not supported or no data set, 3 suboption not set, 4 resource error, 5 Set not possible (local reasons).

**BlockQualifier (Set)**: bit 0 = permanent (1) / temporary (0). For ResetToFactory, bits 1-15 = reset mode (1 application, 2 communication, 3+ all). (pf_cmina.c:653, 902)

### 2.3 DCE/RPC

| Item | Value | Cite |
|---|---|---|
| RPC version | 4 | pf_types.h:298; pf_cmrpc.c:3799 |
| Packet types | 0 Request, 1 Ping, 2 Response, 3 Fault, 4 Working, 5 NoCall (resp to ping), 6 Reject, 7 Ack, 8 CL_Cancel, 9 FragAck, 10 CancelAck | pf_types.h:183-197 |
| Flags1 bits | 1 LastFragment, 2 Fragment, 3 NoFack, 4 Maybe, 5 Idempotent, 6 Broadcast (0 and 7 reserved) | pf_types.h:199-209 |
| Flags2 bits | 1 CancelPending | pf_types.h:211-214 |
| drep | Byte 0 high nibble 0 = big-endian, 1 = little-endian | pf_block_reader.c:910 |
| Header size | 80 bytes. MustRecvFragSize 1464. | pf_cmrpc.c:69-70 |
| NDR header (PNIO) | Request: ArgsMaximum u32, ArgsLength u32, MaximumCount u32, Offset u32, ActualCount u32 (20 bytes). Response: PNIOStatus u32 in place of ArgsMaximum. | pf_types.h:782-799 |
| PNIO opnums | 0 Connect, 1 Release, 2 Read, 3 Write, 4 Control, 5 ReadImplicit | pf_types.h:216-224 |
| EPM opnums | 0 Insert, 1 Delete, 2 Lookup, 3 Map, 4 LookupHandleFree, 5 InqObject, 6 MgmtDelete | pf_types.h:227-236 |
| EPM inquiry types | 0 all registered interfaces, 1 all objects for 1 interface, 2 all interfaces incl. objects, 3 one interface + one object | pf_types.h:359-365 |
| EPM tower | 5 floors, referent ID 3. Floor protocol IDs: 0x0D UUID, 0x0A connectionless, 0x08 DOD UDP, 0x09 DOD IP. Floor versions EPMv4 = 3, NDR = 2, PNIO = 1, minor 0. Annotation 64 bytes. | pf_types.h:338-356 |
| EPM "not registered" | 0x16C9A0D6 | pf_types.h:368-372 |
| NCA reject status | 0x1C000008 version mismatch, …09 unspec reject, …0A bad actid, …0B who-are-you failed, …0C manager not entered, …1D unsupported authn level, …1F invalid checksum, …20 invalid CRC, 0x1C010002 op range error, …03 unknown interface, …06 wrong boot time, …09 you crashed, …0B proto error, …13 out args too big, …14 server too busy, …17 unsupported type | pf_types.h:472-490 |

**UUIDs**

| UUID | Value | Cite |
|---|---|---|
| PNIO IO-Device interface | DEA00001-6C97-11D1-8271-00A02442DF7D | pf_cmrpc.c:83-87 |
| PNIO IO-Controller interface (CControl target) | DEA00002-6C97-11D1-8271-00A02442DF7D | pf_cmrpc.c:3810-3822 |
| PNIO IO-Supervisor / ParameterServer interface | DEA00003-… / DEA00004-… (standard values, **not defined in p-net**) | n/a |
| PNIO object UUID | DEA00000-6C97-11D1-8271-`<InstanceHi><InstanceLo><DeviceIDHi><DeviceIDLo><VendorIDHi><VendorIDLo>`. p-net uses instance 0x0001. | pf_cmrpc_epm.c:51-57, 225-236 |
| EPM interface (EPMv4) | E1AF8308-5D1F-11C9-91A4-08002B14A0FA | pf_cmrpc.c:77-81 |
| NDR transfer syntax | 8A885D04-1CEB-11C9-9FE8-08002B104860 | pf_cmrpc_epm.c:59-65 |
| Implicit AR UUID | All zero | pf_cmrpc.c:75 |

### 2.4 Block types (pf_types.h:494-597)

Block header: BlockType u16, BlockLength u16 (excludes type and length), VersionHigh u8, VersionLow u8. Default version 1.0, some blocks use 1.1 (pnet_api.h:63-67; pf_types.h:769-775).

| Value | Name | Value | Name |
|---|---|---|---|
| 0x0001 | AlarmNotification High | 0x8001 | AlarmAck High |
| 0x0002 | AlarmNotification Low | 0x8002 | AlarmAck Low |
| 0x0008 | IODWriteReqHeader | 0x8008 | IODWriteResHeader |
| 0x0009 | IODReadReqHeader | 0x8009 | IODReadResHeader |
| 0x0010 | DiagnosisData | 0x0012 | ExpectedIdentificationData |
| 0x0013 | RealIdentificationData | 0x0014 | SubstituteValue |
| 0x0015 | RecordInputDataObjectElement | 0x0016 | RecordOutputDataObjectElement |
| 0x0018 | ARData | 0x0019 | LogBookData |
| 0x001A | APIData | 0x001B | SRLData |
| 0x0020-0x002F | I&M0 … I&M15 | 0x0030 / 0x0031 / 0x0032 | I&M0FilterData submodule / module / device |
| 0x0034 | I&M5Data | | |
| 0x0101 | ARBlockReq | 0x8101 | ARBlockRes |
| 0x0102 | IOCRBlockReq | 0x8102 | IOCRBlockRes |
| 0x0103 | AlarmCRBlockReq | 0x8103 | AlarmCRBlockRes |
| 0x0104 | ExpectedSubmoduleBlockReq | 0x8104 | ModuleDiffBlock |
| 0x0105 | PrmServerBlockReq | 0x8105 | PrmServerBlockRes |
| 0x0106 | MCRBlockReq | 0x8106 | ARServerBlock |
| 0x0107 | ARRPCBlockReq | 0x8107 | ARRPCBlockRes |
| 0x0108 | ARVendorBlockReq | 0x8108 | ARVendorBlockRes |
| 0x0109 | IRInfoBlock | 0x010A | SRInfoBlock |
| 0x010B | ARFSUBlock | 0x010C | RSInfoBlock |
| 0x0110 | IODControlReq (PrmEnd) | 0x8110 | IODControlRes (PrmEnd) |
| 0x0111 | IODControlReq (PrmEnd, plug alarm) | 0x8111 | its Res |
| 0x0112 | IOXControlReq (ApplicationReady) | 0x8112 | IOXControlRes (ApplicationReady) |
| 0x0113 | IOXControlReq (AppReady, plug alarm) | 0x8113 | its Res |
| 0x0114 | ReleaseBlockReq | 0x8114 | ReleaseBlockRes |
| 0x0116 | IOXControlReq ReadyForCompanion | 0x0117 | IOXControlReq ReadyForRTC3 |
| 0x0118 | IODControlReq PrmBegin | 0x0119 | SubmoduleListBlock (PrmBegin) |
| 0x0200 | PDPortDataCheck | 0x0202 | PDPortDataAdjust: AdjustBoundary (as named in p-net) |
| 0x020A | CheckPeers | 0x020F | PDPortDataReal |
| 0x0224 | AdjustPeerToPeerBoundary | 0x0240 | PDInterfaceDataReal |
| 0x0250 | PDInterfaceAdjust | 0x0251 | PDPortStatistic |
| 0x0400 | MultipleBlockHeader | 0x0F00 | MaintenanceItem |

### 2.5 PNIOStatus (pnet_api.h:116-385)

**ErrorCode** (byte 0): 0x00 no error, 0x81 PNIO (generic), 0xCF RTA error (ERR-RTA-PDU), 0xDA AlarmAck, 0xDB Connect, 0xDC Release, 0xDD Control, 0xDE Read, 0xDF Write.
**ErrorDecode** (byte 1): 0x00 no error, 0x80 PNIORW (Read/Write only), 0x81 PNIO, 0x82 manufacturer specific.

**ErrorCode1 with ErrorDecode = PNIORW (0x80)** (pnet_api.h:149-169). ErrorCode2 is user-specific here.

| Value | Meaning | Value | Meaning |
|---|---|---|---|
| 0xA0 | application: read error | 0xB5 | access: state conflict |
| 0xA1 | application: write error | 0xB6 | access: access denied |
| 0xA2 | application: module failure | 0xB7 | access: invalid range |
| 0xA7 | application: busy | 0xB8 | access: invalid parameter |
| 0xA8 | application: version conflict | 0xB9 | access: invalid type |
| 0xA9 | application: feature not supported | 0xBA | access: backup |
| 0xB0 | access: invalid index | 0xC0 | resource: read constraint conflict |
| 0xB1 | access: write length error | 0xC1 | resource: write constraint conflict |
| 0xB2 | access: invalid slot/subslot | 0xC2 | resource: busy |
| 0xB3 | access: type conflict | 0xC3 | resource: unavailable |
| 0xB4 | access: invalid area/API | | |

**ErrorCode1 with ErrorDecode = PNIO (0x81)** (pnet_api.h:174-231)

| Value | Meaning | Value | Meaning |
|---|---|---|---|
| 0x01 | Connect: faulty ARBlockReq | 0x3C | AlarmAck (ALARM_ACK) |
| 0x02 | Connect: faulty IOCRBlockReq | 0x3D | CMDEV |
| 0x03 | Connect: faulty ExpectedSubmoduleBlockReq | 0x3E | CMCTL |
| 0x04 | Connect: faulty AlarmCRBlockReq | 0x3F | CTLDINA |
| 0x05 | Connect: faulty PrmServerBlockReq | 0x40 | CMRPC |
| 0x06 | Connect: faulty MCRBlockReq | 0x41 | ALPMI |
| 0x07 | Connect: faulty ARRPCBlockReq | 0x42 | ALPMR |
| 0x08 | Read/Write: faulty record | 0x43 | LMPM |
| 0x09 | Connect: faulty IRInfoBlock | 0x44 | MAC |
| 0x0A | Connect: faulty SRInfoBlock | 0x45 | RPC |
| 0x0B | Connect: faulty ARFSUBlock | 0x46 | APMR |
| 0x0C | Connect: faulty ARVendorBlockReq | 0x47 | APMS |
| 0x0D | Connect: faulty RSInfoBlock | 0x48 | CPM |
| 0x14 | Control: faulty IODControlReq (PrmEnd) | 0x49 | PPM |
| 0x15 | Control: faulty IODControlReq (plug) | 0x4A-0x4D | DCPUCS / DCPUCR / DCPMCS / DCPMCR |
| 0x16 | Control: faulty IOXControlReq (AppReady) | 0x4E | FSPM |
| 0x17 | Control: faulty IOXControlReq (plug) | 0xC8 | CMSM |
| 0x18 | Control: faulty IODControlReq PrmBegin | 0xCA | CMRDR |
| 0x19 | Control: faulty SubmoduleListBlock | 0xCC | CMWRR |
| 0x28 | Release: faulty ReleaseBlock | 0xCD-0xD3 | CMIO, CMSU, (0xCF gap), CMINA 0xD0, CMPBE 0xD1, CMSRL 0xD2, CMDMC 0xD3 |
| 0xFD | RTA error class protocol (abort reasons) | 0xFF | user specific |

> Note: the request lists APMR/APMS as 0x3C/0x3D. In p-net 0x3C is ALARM_ACK and 0x3D is CMDEV, and APMR/APMS are **0x46/0x47**.

**ErrorCode2 for block-parsing ErrorCode1 0x01-0x0D** (field index in the block): p-net uses 0 = BlockType, 1 = BlockLength, 2 = VersionHigh, 3 = VersionLow, then the field ordinal (e.g. IOCR 6 = LT field, 7 = RTClass, 8 = DataLength, 9 = FrameID). Named examples: 0x08 DCTRL ControlCommand, IRInfo 0x05 wrong UUID / 0x07 wrong NumberOfIOCRs, MCR 0x07 and PrmServer 0x07 wrong name length (pnet_api.h:315-383; pf_cmdev.c:3024-3141).

**ErrorCode2 for other ErrorCode1** (pnet_api.h:236-312)

| ErrorCode1 | ErrorCode2 values |
|---|---|
| CMDEV 0x3D | 0 state conflict, 1 resource |
| CMRPC 0x40 | 0 ArgsLength invalid, 1 unknown blocks, 2 IOCR missing, 3 wrong AlarmCRBlock count, 4 out of AR resources, 5 AR UUID unknown, 6 state conflict, 8 out of memory, 9 PDEV already owned, 0x0A ARset state conflict, 0x0B ARset param conflict, 0x0C PDEV ports without interface |
| CTLDINA 0x3F | 7 ARP multiple IP addresses |
| ALPMI 0x41 | 0 invalid state, 1 wrong ACK-PDU, 2 invalid, 3 wrong state |
| ALPMR 0x42 | 0 invalid state, 1 wrong notification PDU, 2 invalid, 3 wrong state |
| APMR 0x46 / APMS 0x47 | 0 invalid state, 1 LMPM error, (APMS) 2 timeout |
| CPM 0x48 / PPM 0x49 | 0 invalid state, 1 invalid |
| CMSM/CMRDR/CMIO/CMINA/CMPBE/CMSRL/CMDMC | 0 invalid state, 1 signaled error |
| CMWRR 0xCC | 0 invalid state, 1 AR not primary, 2 signaled error |
| CMSU 0xCE | 0 invalid state, 1 AR add prov/cons failed, 2 alarm open failed, 3 alarm send, 4 alarm ack send, 5 alarm ind |

**RTA abort reasons: ErrorCode1 0xFD, sent as PNIOStatus CF 81 FD xx** (pnet_api.h:323-355)

| Code | Reason | Code | Reason |
|---|---|---|---|
| 0x01 | sequence error | 0x13 | name resolution error |
| 0x02 | instance closed | 0x14 | RPC bind error |
| 0x03 | AR out of memory | 0x15 | RPC connect error |
| 0x04 | add provider/consumer failed | 0x16 | RPC read error |
| 0x05 | consumer DHT/WDT expired | 0x17 | RPC write error |
| 0x06 | CMI timeout | 0x18 | RPC control error |
| 0x07 | alarm open failed | 0x19 | forbidden pull/plug |
| 0x08 | alarm send cnf(−) | 0x1A | AP removed |
| 0x09 | alarm ack send cnf(−) | 0x1B | link down |
| 0x0A | alarm data too long | 0x1C | MC register failed |
| 0x0B | alarm ind error | 0x1D | not synchronized |
| 0x0C | RPC client call cnf(−) | 0x1E | wrong topology |
| 0x0D | abort request (user) | 0x1F | DCP station name changed |
| 0x0E | re-run aborts existing AR | 0x20 | DCP reset to factory |
| 0x0F | release indication received | 0x24 | PDEV check failed |
| 0x10 | device deactivated | | |
| 0x11 | removed | | |
| 0x12 | protocol violation | | |

### 2.6 Record indices (pf_types.h:599-766)

| Index | Meaning | Index | Meaning |
|---|---|---|---|
| 0x0000-0x7FFF | user specific | 0xAFF0-0xAFF4 | I&M0-I&M4 (0xAFF5-0xAFFF I&M5-15) |
| 0x8000 / 0x8001 | Expected / RealIdentificationData (subslot) | 0xC000 / 0xC001 | Expected / Real IdentData (slot) |
| 0x800A / 0x800B / 0x800C | Diagnosis: channel / all / DMQS (subslot) | 0xC00A / 0xC00B / 0xC00C | Diagnosis: same, slot |
| 0x8010-0x8013 | MaintReq ch, MaintDem ch, MaintReq all, MaintDem all (subslot) | 0xC010-0xC013 | same, slot |
| 0x801E | SubstituteValue | 0xE000 / 0xE001 / 0xE002 | AR Expected / Real IdentData / ModuleDiffBlock |
| 0x8020 | PDIRSubframeData | 0xE00A / 0xE00B / 0xE00C | Diagnosis per AR |
| 0x8027 | PDPortDataRealExtended | 0xE010-0xE013 | Maintenance per AR |
| 0x8028 / 0x8029 | RecordInput / OutputDataObjectElement | 0xE030 / 0xE031 | PE EntityFilter / StatusData (AR) |
| 0x802A | PDPortDataReal | **0xE040** | **WriteMultiple** |
| 0x802B | PDPortDataCheck | 0xE041 | ApplicationReady record (as named in p-net) |
| 0x802C | PDIRData | 0xE050 | ARFSUDataAdjust |
| 0x802D | PDSyncData (SyncID 0) | 0xE060 / 0xE061 | RS GetEvent / AckEvent |
| 0x802F | PDPortDataAdjust | 0xF000 | RealIdentificationData (API) |
| 0x8030 | IsochronousModeData | 0xF00A / 0xF00B / 0xF00C | Diagnosis per API |
| 0x8031 | PDTimeData | 0xF010-0xF013 | Maintenance per API |
| 0x8050-0x8057 | MRP interface/port real/check/adjust, MRP-IC | 0xF020 | ARData (API) |
| 0x8060-0x8063 | FO port real/check/adjust, SFP check | **0xF80C** | Diagnosis DMQS (device) |
| 0x8070 | PDNCDataCheck | 0xF820 / 0xF821 | ARData / APIData (device) |
| 0x8071 | PDInterfaceAdjust (LLDP mode) | 0xF830 | LogBookData |
| 0x8072 | PDPortStatistic | 0xF831 | PDevData |
| 0x8080 | PDInterfaceDataReal | 0xF840 | I&M0FilterData |
| 0x8090 | PDInterfaceFSUDataAdjust | 0xF841 / 0xF842 | PDRealData / PDExpectedData |
| 0x80AF | PE EntityStatusData (subslot) | 0xF850 | AutoConfiguration |
| 0x80B0 | CombinedObjectContainer | 0xF860 | GSD upload |
| 0x80CF | RS AdjustObserver | 0xF870 / 0xF871 | PE EntityFilter / Status (device) |
| 0x80F0-0x80F5 | TSN records | 0xF880-0xF889 | AssetManagement 1-10 |
| 0x8200 | PDInterfaceSecurityAdjust | 0xF8F0-0xF8F3 | TSN stream add / PDRsiInstances / remove / renew |
| | | 0xFBFF | connection-monitor trigger (resets the timeout) |

Read handlers implemented (pf_cmrdr.c:161-790): I&M0-4 (via fspm, pf_fspm.c:714-830), I&M0Filter 0xF840, LogBook 0xF830, Exp/Real ident at sub/slot/AR/API level, APIData 0xF821, all diagnosis variants incl. 0xF80C, 0xFBFF, ARData 0xF020/0xF820, ModuleDiff 0xE002, PD records 0x802A/0x802B/0x802F/0x8072/0x8071/0xF841/0xF842 (pf_pdport.c:506-722), 0x8080. WriteMultiple 0xE040 is unpacked in pf_cmrpc.c:3464-3480.

### 2.7 ControlCommand bits (pf_types.h:801-810)

| Bit | Meaning |
|---|---|
| 0 | PrmEnd |
| 1 | ApplicationReady |
| 2 | Release |
| 3 | Done |
| 4 | ReadyForCompanion |
| 5 | ReadyForRT_CLASS_3 |
| 6 | PrmBegin |

IODControl/IOXControl block body: ARUUID, SessionKey u16, (reserved u16 not shown in the struct), ControlCommand u16, ControlBlockProperties u16, plus AlarmSequenceNumber for plug variants (pf_types.h:812-821).

### 2.8 ARType, ARProperties, IOCR, AlarmCR

**ARType** (pf_types.h:1223-1229): 0x0001 IOCARSingle, 0x0006 IOSAR, 0x0010 IOCARSingle RTC3, 0x0020 IOCAR_SR.

**ARProperties bits** (pf_block_reader.c:364-380): 0-2 State (1 = active), 3 SupervisorTakeoverAllowed, 4 ParameterizationServer (0 external, 1 CM initiator), 8 DeviceAccess, 9-10 CompanionAR, 11 AcknowledgeCompanionAR, 29 CombinedObjectContainer, 30 StartupMode (0 legacy, 1 advanced), 31 PullModuleAlarmAllowed.

**IOCRType** (pf_types.h:1231-1238): 1 Input, 2 Output, 3 MC provider, 4 MC consumer.
**IOCRProperties** (pf_block_reader.c:428-436): bits 0-3 RTClass (1 RT_CLASS_1 legacy, 2 RT_CLASS_2, 3 RT_CLASS_3, 4 RT_CLASS_UDP, 5 RT_CLASS_STREAM; pf_types.h:1247-1254), bits 4-31 reserved.
**IOCRTagHeader**: VLAN ID bits 0-11, user priority bits 13-15 (pf_block_reader.c:448-452).
**AlarmCRType** 0x0001 (pf_types.h:1318-1321). **AlarmCRProperties**: bit 0 priority (0 user, 1 fixed), bit 1 transport UDP (pf_block_reader.c:628-633). AlarmCRTagHeader High/Low: VID bits 0-11, prio bits 13-15 (pf_block_reader.c:639-650; pf_types.h:1681-1686).
**SubmoduleProperties** (pf_block_reader.c:298-304): bits 0-1 Type (0 NO_IO, 1 IN, 2 OUT, 3 IO), 2 SharedInput, 3 ReduceInputSubmoduleDataLength, 4 ReduceOutputSubmoduleDataLength, 5 DiscardIOXS.
**DataDescription**: LengthIOCS / LengthIOPS allowed = 1 (pf_types.h:1495-1501).

### 2.9 IOxS and DataStatus

**IOPS/IOCS** (pnet_api.h:413-417): 0x80 GOOD, 0x00 BAD (bits 5-6 = location, bit 7 = DataState).

**DataStatus bits** (pnet_api.h:464-488): 0 State (1 primary / 0 backup), 1 Redundancy, 2 DataValid, 3 reserved, 4 ProviderState (1 RUN / 0 STOP), 5 StationProblemIndicator (1 normal / 0 problem), 6 reserved, 7 Ignore. The cyclic frame trailer is CycleCounter u16, DataStatus u8, TransferStatus u8 (pf_ppm.c:199-205, 250-256).

### 2.10 Alarms

**AlarmType** (pf_types.h:884-914)

| Value | Type | Value | Type |
|---|---|---|---|
| 0x0001 | Diagnosis | 0x000C | Diagnosis disappears |
| 0x0002 | Process | 0x000D | MC communication mismatch |
| 0x0003 | Pull | 0x000E | Port data change notification |
| 0x0004 | Plug | 0x000F | Sync data change |
| 0x0005 | Status | 0x0010 | Isochronous mode problem |
| 0x0006 | Update | 0x0011 | Network component problem |
| 0x0007 | Media redundancy | 0x0012 | Time data change |
| 0x0008 | Controlled by supervisor | 0x0013 | Dynamic frame packing problem |
| 0x0009 | Released | 0x0014 | MRPD problem |
| 0x000A | Plug wrong submodule | 0x0016 | Multiple interface mismatch |
| 0x000B | Return of submodule | 0x001E / 0x001F | Upload & retrieval / Pull module |

0x0020-0x007F manufacturer specific, 0x0080-0x00FF profiles.

**AlarmSpecifier** (u16) (pf_block_reader.c:1052-1061; pf_block_writer.c:3538-3554): bits 0-10 SequenceNumber, 11 ChannelDiagnosis, 12 ManufacturerSpecificDiagnosis, 13 SubmoduleDiagnosisState, 14 reserved, 15 ARDiagnosisState.

**AlarmNotification body**: AlarmType u16, API u32, Slot u16, Subslot u16, ModuleIdent u32, SubmoduleIdent u32, AlarmSpecifier u16, then the optional USI u16 + data (pf_block_reader.c:1040-1061).

**RTA fixed header** (pf_types.h:928-941): AlarmDstEndpoint u16, AlarmSrcEndpoint u16, PDUType u8 (type/version), AddFlags u8 (window/TACK), SendSeqNum u16, AckSeqNum u16, then VarPartLen u16.

### 2.11 Diagnosis

**USI** (pf_types.h:2250-2274): 0x0000-0x7FFF manufacturer specific. 0x8000 ChannelDiagnosis, 0x8001 Multiple, 0x8002 ExtChannelDiagnosis, 0x8003 QualifiedChannelDiagnosis, 0x8100 Maintenance, 0x8200 Upload&Retrieval, 0x8201 iParameter, 0x8300-0x8303 RS low watermark / timeout / overflow / event, 0x8310 PE energy saving status, 0x8320 process alarm reason.

**ChannelNumber** 0x8000 = whole submodule (pnet_api.h:1907).

**ChannelProperties** (u16) (pf_types.h:111-180; pnet_api.h:1912-1951)

| Bits | Field | Values |
|---|---|---|
| 0-7 | Type (data width) | 0 unspecified, 1 = 1 bit, 2 = 2 bit, 3 = 4 bit, 4 = 8 bit, 5 = 16 bit, 6 = 32 bit, 7 = 64 bit |
| 8 | Accumulative | 0 individual channel, 1 channel group |
| 9-10 | Maintenance | 0 fault, 1 maintenance required, 2 maintenance demanded, 3 qualified |
| 11-12 | Specifier | 0 all disappear, 1 appears, 2 disappears, 3 disappears but others remain |
| 13-15 | Direction | 0 manufacturer specific, 1 input, 2 output, 3 in/out |

**QualifiedChannelQualifier masks** (pf_types.h:96-107): advice 0x00000078 (bits 3-6), maintenance required 0x0001FF80 (7-16), demanded 0x07FE0000 (17-26), fault 0xF8000000 (27-31).

**Diagnosis item (standard format)**: ChannelNumber u16, ChannelProperties u16, ChannelErrorType u16, ExtChannelErrorType u16, ExtChannelAddValue u32, QualifiedChannelQualifier u32 (pf_types.h:952-960).

**ChannelErrorType values defined in p-net**: 0x8001 Remote mismatch (`PF_WRT_ERROR_REMOTE_MISMATCH`), with ExtChannelErrorType 0x8000 peer station-name mismatch, 0x8001 peer port-name mismatch, 0x8005 no peer detected (pf_types.h:321-331). The common standard values are **not defined in p-net** (from IEC 61158-6-10): 0x0001 short circuit, 0x0002 undervoltage, 0x0003 overvoltage, 0x0004 overload, 0x0005 overtemperature, 0x0006 line break, 0x0007 upper limit exceeded, 0x0008 lower limit exceeded, 0x0009 error, 0x8000 data transmission impossible, 0x8002 media redundancy mismatch.

### 2.12 SubmoduleState / ModuleState / Ident

**SubmoduleState** (u16, FormatIndicator = 1) (pf_types.h:1512-1522)

| Bits | Field | Values |
|---|---|---|
| 0-2 | AddInfo | 0 none, 1 takeover not allowed |
| 3 | AdviceAvailable / QualifiedInfo | bool |
| 4 | MaintenanceRequired | bool |
| 5 | MaintenanceDemanded | bool |
| 6 | Fault (DiagInfo) | bool |
| 7-10 | ARInfo | 0 own, 1 ApplicationReadyPending, 2 superordinated locked, 3 locked by IO controller, 4 locked by IO supervisor |
| 11-14 | IdentInfo | 0 OK, 1 substitute, 2 wrong, 3 no submodule |
| 15 | FormatIndicator | always 1 |

**ModuleState** (ModuleDiffBlock) (pf_types.h:1326-1332): 0 no module, 1 wrong module, 2 proper module, 3 substitute.

### 2.13 I&M

**I&M0 layout** (block type 0x0020, BlockLength 56, version 1.0, 60 bytes on the wire) (pf_block_writer.c:2305-2370; pnet_api.h:1130-1160)

| Offset | Len | Field |
|---|---|---|
| 0 | 6 | BlockHeader |
| 6 | 2 | VendorID |
| 8 | 20 | OrderID (space-padded ASCII) |
| 28 | 16 | IM_Serial_Number (space-padded) |
| 44 | 2 | IM_Hardware_Revision |
| 46 | 1 | SW revision prefix ('V','R','P','U','T') |
| 47 | 1 | SW functional enhancement |
| 48 | 1 | SW bug fix |
| 49 | 1 | SW internal change |
| 50 | 2 | IM_Revision_Counter |
| 52 | 2 | IM_Profile_ID |
| 54 | 2 | IM_Profile_Specific_Type |
| 56 | 1 | IM_Version major (1) |
| 57 | 1 | IM_Version minor (1) |
| 58 | 2 | IM_Supported (bit 1 I&M1, 2 I&M2, 3 I&M3, 4 I&M4) |

String padding: `pf_put_str` pads with spaces to the field width (pf_block_writer.c:101-128).
I&M1: TagFunction 32 + TagLocation 22. I&M2: Date 16 ("YYYY-MM-DD HH:MM"). I&M3: Descriptor 54. I&M4: Signature 54 (binary, zero padded) (pnet_api.h:1165-1200). Per the README, I&M is supported for the device only, not per module.

### 2.14 DAP identifiers (pnet_api.h:74-89)

Slot 0. Subslot 1 = DAP identity (submodule ident 0x00000001). Subslot 0x8000 = interface 1 (ident 0x8000). Subslots 0x8001-0x8004 = ports 1-4. Module ident of the DAP is 0x00000001 in the API defaults (the sample app may override).

### 2.15 LLDP TLVs

| TLV | Type | Content | Cite |
|---|---|---|---|
| End | 0 | n/a | pf_lldp.c:52 |
| ChassisID | 1 | subtype 4 (MAC) or 7 (locally assigned) | pf_lldp.c:53; pf_lldp.h:31-32 |
| PortID | 2 | subtype 7 | pf_lldp.c:54, 686 |
| TTL | 3 | 20 s | pf_lldp.c:55; pf_types.h:1071 |
| PortDescription | 4 | parsed only | pf_lldp.c:56, 1657 |
| ManagementAddress | 8 | length 12: addr-len, subtype (IPv4 = 1), IPv4, if-subtype, if-number u32, OID len 0 | pf_lldp.c:57, 425-445 |
| Org-specific | 127 | PNO 00-0E-CF (subtypes in 1.5), IEEE 802.3 00-12-0F subtype 1 MAC/PHY: autoneg support/status, PMD capability, MAU type | pf_lldp.c:58-60, 96-109, 380-410 |
