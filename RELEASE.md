# Releases

go-profinet follows semantic versioning from v1.0.0; before that, minor
versions may change the API. Releases are tagged by the Release workflow
(`.github/workflows/release.yml`).

## Unreleased

First version.

- IO-Controller (`controller`): DCP resolution and IP assignment, legacy
  (RT_CLASS_1) and advanced (RT_CLASS_2) startup, parameterization, cyclic
  data, alarms, records, reconnect with backoff; `RecordClient` with
  ReadImplicit; device access ARs (`OpenSupervisor`); `Device.AR`.
- IO-Device (`device`): DCP responder, LLDP, Connect/Read/Write/Control/
  Release, IOCAR and IOSAR, several ARs, ModuleDiff, cyclic data with
  watchdogs, alarms and diagnosis, I&M0–4, RealIdentificationData,
  PDPortDataReal and PDRealData.
- DCP, LLDP, CL DCE/RPC, PNIO blocks, RT and RTA codecs and machines.
- GSDML: generated types from an independent catalog, parse, validate,
  encode, model conversion.
- Raw Ethernet backends for Linux, macOS/BSD and Windows; virtual Ethernet
  with fault injection; ARP/IPv4/UDP stack for it.
- Verified against p-net, pnio-rs, profinet-py and pnio-dcp
  ([INTEROPERABILITY.md](INTEROPERABILITY.md)).

## Release checklist

1. `make check` and `make test-e2e` pass; the Interop workflow is green.
2. This file lists the changes; API changes are called out.
3. Run the Release workflow with the version bump.
4. Update profinetctl: require the tag, drop its `replace`.
