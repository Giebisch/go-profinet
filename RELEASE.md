# Releases

go-profinet follows semantic versioning from v1.0.0; before that, minor
versions may change the API. Releases are tagged by the Release workflow
(`.github/workflows/release.yml`).

## v0.1.1 (2026-10-26)

No code change.

- The interop suite (`make interop-images`, the Interop workflow) builds the
  reference images from profinet-interop v0.1.0 instead of its `main`
  branch; the Interop workflow's `interop_ref` input still overrides it.
- Documentation names the released versions (SECURITY.md, this file) and
  links profinet-interop's matrix at v0.1.0.

## v0.1.0 (2026-10-26)

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
2. `INTEROP_REF` (Makefile and `.github/workflows/interop.yml`) names the
   profinet-interop release the matrix in INTEROPERABILITY.md came from.
3. This file lists the changes under the new version; API changes are
   called out.
4. Run the Release workflow with the version bump.
5. Update profinetctl: `go get github.com/otfabric/go-profinet@<tag>`.
