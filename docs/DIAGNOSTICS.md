# Diagnosis, identification and topology records

## Diagnosis

A device records diagnoses per submodule and channel and reports each with a
diagnosis alarm:

```go
err := dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{
	ChannelNumber: 3, ChannelProperties: pnio.ChanDirInput | pnio.ChanMaintRequired,
	ErrorType: pnio.ChannelErrLineBreak,
})
err = dev.RemoveDiagnosis(ctx, 1, 1, 3, pnio.ChannelErrLineBreak) // "disappears" alarm
```

`AddDiagnosis` records the diagnosis even when no AR can receive the alarm
(it then returns an error matching `profinet.ErrInvalidState`). A controller
reads the diagnosis with `Device.ReadDiagnosis` (index 0xF80C of the device)
or `RecordClient.ReadDiagnosis` for one submodule (0x800C), and receives the
alarms in `OnAlarm`.

| ChannelProperties | Values |
|-------------------|--------|
| Direction (bits 13–14) | `ChanDirManufacturer`, `ChanDirInput`, `ChanDirOutput`, `ChanDirInOut` |
| Maintenance (bits 9–10) | fault (0), `ChanMaintRequired`, `ChanMaintDemanded`, `ChanMaintQualified` |
| Specifier (bits 11–12) | appears, disappears (set by the device), all disappear, other remain |
| Accumulative (bit 8) | `ChanAccumulative` |

Error types 0x0001–0x0009 and 0x8000/0x8001 are the standard channel errors
(`pnio.ChannelErr*`); 0x0100–0x7FFF are manufacturer specific.
The device serves every diagnosis index of the standard by scope (subslot,
slot, AR, API, device) and kind (channel, all, maintenance and qualified).

## Identification and maintenance (I&M)

| Record | Index | Contents |
|--------|-------|----------|
| I&M0 | 0xAFF0 | vendor, order ID, serial number, hardware and software revision, revision counter, profile, supported I&M records (from the model) |
| I&M1 | 0xAFF1 | tag function (32), tag location (22) |
| I&M2 | 0xAFF2 | installation date (`YYYY-MM-DD HH:MM`) |
| I&M3 | 0xAFF3 | descriptor (54) |
| I&M4 | 0xAFF4 | signature (54 octets) |
| I&M0FilterData | 0xF840 | the submodules that carry I&M data |

I&M1–4 of the DAP are writable; the device keeps them in memory
(`device.Config.IM` gives initial values). `controller.Device.ReadIM0` and
the `pnio.IM*` codecs read them.

## Identification of the configuration

RealIdentificationData (0xF000 for the API, 0xC001 per slot, 0x8001 per
subslot) lists the modules and submodules the device has;
ExpectedIdentificationData (0xE000…) what an AR expects; ModuleDiff (0xE002)
their differences.

## Topology

PDPortDataReal (0x802A of a port submodule) reports a port's own port ID,
its neighbors (port ID, chassis ID = station name, line delay, MAC), MAU
type, link state and media type; PDRealData (0xF841) the PDPortDataReal of
every port in MultipleBlockHeaders. go-profinet decodes both
(`pnio.DecodePDPortDataReal`, `pnio.DecodePDRealData`, verified against
p-net) and the IO-Device serves them from its LLDP agent, with its
interface's PDInterfaceDataReal (name of station, MAC, IP suite) in
PDRealData. profinetctl's `topology` reads PDRealData of every device and
derives the links; `Controller.CheckTopology` compares it with an expected
topology (links between station ports) and reports each link as ok,
missing, wrong peer, unknown port or unverified, plus the unexpected
links and the unreachable stations.

## Port checks and adjustments (PDev)

A controller writes what a port should see and how it should behave; the
device watches it.

| Record (index) | Device | Controller |
|----------------|--------|------------|
| PDPortDataCheck (0x802B) with CheckPeers | evaluated against the LLDP neighbor heard last, after the write and whenever the neighbors or the link change | `DeviceConfig.PeerChecks`, written before PrmEnd |
| PDPortDataAdjust (0x802F): PeerToPeerBoundary bit 0, DCPBoundary bit 1 | LLDP transmission off; DCP Hello off | encode with `pnio.PDPortDataAdjust` |
| PDInterfaceAdjust (0x8071) | MultipleInterfaceMode bit 0: LLDP port ID "port-001.<name>" (the default) or "port-001" | |
| PDExpectedData (0xF842) | the written checks and adjustments | |

A port whose neighbor differs from its CheckPeers reports channel diagnosis
0x8001 "remote mismatch" on channel 0x8000 of the port submodule, with the
extended type 0x8000 (another name of station), 0x8001 (another port) or
0x8005 (no neighbor), the codes p-net reports; each appears and disappears
with an alarm. A neighbor in the standard LLDP mode sends "port-001.<its
name>", which matches the expected port "port-001". Records the device does
not implement are refused, never stored: MAU type, link state and line
delay checks, domain, multicast, MAU, link-state and preamble adjustments,
with PNIORW 0xA9 "feature not supported" (p-net answers 0xB4; the code PI
specifies is not public). The checks and adjustments last as long as the
device runs (no persistence; a controller writes them at every Connect).
Evidence: [research/EVIDENCE-DCP-PDEV.md](research/EVIDENCE-DCP-PDEV.md).
