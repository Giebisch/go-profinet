# An independent IO-Controller for go-profinet's IO-Device

Status: 2026-10-27. Research only: nothing here was run against
go-profinet yet. Every claim cites its source; **[unverified]** marks what
the sources do not settle.

go-profinet's IO-Device is tested by one independent IO-Controller,
profinet-py, and by go-profinet's own controller. This page looks for a
second one that needs no PI membership, no paid PI tools, no commercial
PLC and no special NIC, and says how far each candidate can go: public
CI, a documented manual recipe, or not at all.

## Result

| Candidate | Roles | Runs on | Licence for this use | Verdict |
|---|---|---|---|---|
| **CODESYS Control for Linux SL** + PROFINET Controller SL, demo mode | IO-Controller (CC B), shared device, device access AR, S2 (with Redundancy SL) | Linux x86_64/ARM `.deb`, standard NIC; engineering on Windows only | free demo: "runs for two hours without a license"; no redistribution right; no clause on benchmarks or publishing results | **manual recipe** (below) |
| CODESYS Virtual Control SL | the same, in a container | Docker/Podman, kernel ≥ 6.1, a CODESYS LicenseServer | 2-hour demo per controller | manual at best; PROFINET from the container **[unverified]** |
| Beckhoff TwinCAT 3 + TF6271 | IO-Controller (CC B), shared device; S2 not mentioned | Windows (or TwinCAT/BSD, Beckhoff RT Linux) with an **Intel** NIC | 7-day trial, renewable | manual, second choice |
| Siemens S7-PLCSIM Advanced (TIA Portal trial) | **no PROFINET IO**: "does not simulate fieldbus systems (PROFINET IO, PROFIBUS DP)" | Windows | 21-day trial, "not permitted for production use" | not usable |
| Siemens S7-1500 Software Controller, S7-1500V, PN Driver | IO-Controller | SIMATIC IPC / Industrial Edge / paid source licence "for internal validation purposes" | paid | not usable |
| Phoenix Contact PLCnext Engineer simulation | PROFINET disabled in simulation | Windows | free basic | not usable |
| Virtual PLCnext Control | IO-Controller | OCI container | paid, no trial found | not usable |
| OpenPLC | no PROFINET found | | | not usable |
| p-net, PI Community Stack, pnio-rs, OpenPNIO | device stacks only (the Community Stack: PI members only) | | | not usable as a controller |
| profinet-rs (pasrom) | IO-Controller RT_CLASS_1 | libpcap | GPL-3.0, "a derivative work of f0rw4rd/profinet-py" | usable, but **not independent** of profinet-py |

So: no second independent IO-Controller can run in public CI. CODESYS's
demo is the one candidate an engineer can run by hand, on a Linux host
with a standard NIC, with engineering on a Windows PC.

### Sources

- CODESYS Control for Linux SL: datasheet (demo, fieldbuses in the
  runtime package, "Not released for use in containers or virtual
  machines"), store page (4.23.0.0, 22.10.2026; 4.21.0.0 added XDP for
  PROFINET):
  <https://store.codesys.com/en/codesys-control-linux-sl-1.html>,
  <https://store.codesys.com/media/n98_media_assets/files/2302000005-SW/4/CODESYS%20Control%20for%20Linux%20SL_en.pdf>
- CODESYS PROFINET Controller SL (Conformance Class B, shared device,
  device access AR, "System Redundancy S2", "operates on standard network
  interface cards", single port on a standard adapter, 2-hour demo):
  <https://store.codesys.com/media/n98_media_assets/files/2303000015-F/4/CODESYS%20PROFINET%20Controller%20SL_en.pdf>
- CODESYS PROFINET runtime settings (`[SysEthernet]`,
  `Linux.ProtocolFilter=3`, `Linux.PACKET_QDISC_BYPASS`):
  <https://content.helpme-codesys.com/en/CODESYS%20PROFINET/_pnio_runtime_configuration_controller.html>
- CODESYS S2 (two controllers with CODESYS Redundancy; "Field devices which
  support S2 system redundancy are automatically set to this mode"):
  <https://content.helpme-codesys.com/en/CODESYS%20PROFINET/_pnio_controller_redundancy.html>;
  Redundancy SL needs "Two SoftPLC systems of the same type in the same
  version": <https://store.codesys.com/media/n98_media_assets/files/2302000040-F/4/CODESYS%20Redundancy%20SL_en.pdf>
- CODESYS scripting without UI (`--runscript … --noUI`):
  <https://content.helpme-codesys.com/en/CODESYS%20Scripting/_cds_starting_script_via_command_line.html>
- CODESYS EULA (demo "free of charge but limited by time and/or
  functionality"; licences "simple and non-sublicensable"; no
  modification or decompilation):
  <https://store.codesys.com/media/n98_media_assets/files/000135/4/CODESYS_EULA_Engineering_en.pdf>
- CODESYS Virtual Control SL:
  <https://us.store.codesys.com/media/n98_media_assets/files/000138/2/CODESYS%20Virtual%20Control%20SL_en.pdf>
- S7-PLCSIM Advanced function manual (02/2024):
  <https://cache.industry.siemens.com/dl/files/578/109955578/att_1267815/v1/s7-plcsim_advanced_function_manual_en-US.pdf>
- Beckhoff TF6271 (Intel chipset required) and TwinCAT trial licences:
  <https://www.beckhoff.com/en-en/products/automation/twincat/tfxxxx-twincat-3-functions/tf6xxx-connectivity/tf6271.html>,
  <https://www.beckhoff.com/en-en/products/automation/twincat/twincat-3-licensing/>
- Siemens PN Driver licence: <https://cache.industry.siemens.com/dl/files/422/109802422/att_1080880/v1/AddOn_License_PN_Driver_outside_Germany_2018-06-06.pdf>
- PLCnext simulation: <https://www.plcnext-community.net/makersblog/painful-memories-of-plc-simulations/>
- profinet-rs: <https://github.com/pasrom/profinet-rs>

### What the sources do not settle

- Whether the PROFINET Controller in demo mode stops after 2 hours or
  after 30 minutes: vendor-integration guides for other CODESYS fieldbuses
  report 30-minute trials (TI, Bosch Rexroth). Keep sessions under 30
  minutes.
- Whether the demo needs a CodeMeter soft container or a store login.
- Whether the runtime talks PROFINET over veth, a Linux bridge or TAP, or
  inside a VM (CODESYS calls VMs and containers "not released").
- Whether a fully scripted (`--noUI`) PROFINET configuration works end to
  end.
- Whether CODESYS Redundancy runs in demo mode, and whether it accepts
  go-profinet's experimental S2 (`device.Config.SystemRedundancy`): our
  GSDML generator does not declare system redundancy.

Publishing results: the EULA neither grants nor forbids publishing
interoperability results; name the CODESYS versions, never redistribute
its packages, and do not put them in images or public CI caches.

## Manual recipe: CODESYS Control for Linux SL → go-profinet's IO-Device

Topology: a Windows PC with the CODESYS Development System; Linux host A
with CODESYS Control for Linux SL, PROFINET on interface `pn0`; Linux host
B with go-profinet's `otfnode device` on the same Ethernet segment (a
cable or a switch; virtual links are **[unverified]**).

1. **Engineering.** Install CODESYS Development System 3.5.22.40 or later
   on Windows 10/11 and, in it, the "CODESYS Control for Linux SL" package
   4.23.0.0 or later (earlier versions are affected by advisory AV26-983).
2. **Runtime.** Deploy it to host A from the IDE over SSH, or install the
   package's `codesyscontrol_linux_*.deb` and `codemeter-lite_*.deb` by
   hand. Whether raw Ethernet needs the PrivilegeProxy package is
   **[unverified]**.
3. **Runtime configuration.** In `CODESYSControl.cfg`, section
   `[SysEthernet]`: `Linux.ProtocolFilter=3` (without it RT frames are not
   received) and `Linux.PACKET_QDISC_BYPASS=1` (0 where the interface does
   not support it). Give `pn0` a static address (192.168.0.1/24). Restart
   the runtime: the demo period starts.
4. **The device.** On host B, build `otfnode` (`go build ./interop/otfnode`
   in go-profinet) and run, as root or with CAP_NET_RAW and CAP_NET_ADMIN:

   ```bash
   otfnode device -i eth0 -station otf-device -gsdml-out GSDML-otf.xml -pcap device.pcap -alarm-after 60s
   ```

   It writes its GSDML and keeps running: inputs count up, outputs are
   printed as JSON events, a diagnosis appears on slot 1 a minute after the
   AR reaches Data.
5. **GSDML import.** Tools → Device Repository → Install, select
   `GSDML-otf.xml`. Record whether CODESYS accepts it, and every message it
   prints: that is GSDML compatibility evidence (Milestone D).
6. **Project.** Standard project for "CODESYS Control for Linux SL"; scan
   for host A; add an Ethernet adapter on `pn0`, a PROFINET IO Controller
   under it, and the otfnode device with its modules (from the GSDML) under
   that. Exact dialog labels **[unverified]**.
7. **Name and address.** Log in; on the controller, Scan for Devices; set
   the name `otf-device` and the address 192.168.0.10 (DCP Set; otfnode
   applies it to `eth0`). The scan also reads I&M0.
8. **Startup.** Download and start the application. Expected: the device is
   green, `xRunning` of the device and `xDataValid` of the controller; the
   capture on host B shows Connect, PrmEnd, ApplicationReady and cyclic
   frames.
9. **Cyclic IO.** Map the module data to variables, write outputs online,
   watch otfnode's `output` events and the incrementing inputs.
10. **Records.** `RDREC`/`WRREC` on slot 0 subslot 1, index 0xAFF0 (I&M0)
    and 0xAFF1 (I&M1, writable); a user record of the model. Parameters of
    the function blocks **[unverified]**.
11. **Alarms and diagnosis.** After `-alarm-after`, the device's status
    page shows the channel diagnosis; `RALARM`/`DiagnosisBuffer` see the
    alarm. Whether CODESYS acknowledges it automatically **[unverified]**;
    otfnode prints `diagnosis-sent` with an error when no AlarmAck came.
12. **Reconnect.** Pull the cable (or `ip link set eth0 down` and up), then
    restart otfnode under the same name: CODESYS should report the error
    and re-establish the AR. Time it from the capture.
13. **S2 (optional, least certain).** A second Linux SL host of the same
    version with CODESYS Redundancy SL; a redundancy configuration with
    both controllers. go-profinet's device needs
    `Config.SystemRedundancy` (otfnode has no flag for it yet) and a GSDML
    that declares S2. Expect this to fail: go-profinet's S2 is an
    experiment built from public wire elements.
14. **Records of the run.** Keep each session under 30 minutes; note the
    versions of the IDE, the runtime and the PROFINET package; keep the
    captures and the CODESYS messages; publish results and captures only,
    never CODESYS packages.

Report each step as pass, fail (with the message) or not reached, in the
form of profinet-interop's matrix, so that a manual run can be added to the
interoperability evidence with its date and versions.
