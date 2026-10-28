# Without PROFIBUS & PROFINET International membership: what is missing

OT Fabric is not (yet) a member of PROFIBUS & PROFINET International (PI).
Several PROFINET artefacts are published by PI to members only, under a
licence that forbids redistribution. go-profinet is built from public
sources instead: the public parts of IEC 61158/61784, public descriptions of
GSDML, packet dissectors (Wireshark, scapy) and the behaviour of four
independent implementations (p-net, pnio-rs, profinet-py, pnio-dcp). This
page lists what that costs, so that nobody mistakes a gap for a feature.

Status words: **Missing** (not implemented, cannot be without the
artefact) · **Degraded** (implemented from public sources; may disagree
with the official artefact) · **Unaffected**.

## Impact by feature

| Area | Members-only artefact | Impact | Status | What we have instead |
|------|----------------------|--------|--------|----------------------|
| GSDML validation | GSDML XSDs (in the GSDML specification, PI order 2.352) | `gsdml.Validate` checks our own catalog of the schema, not the official XSDs. A file it accepts may fail an engineering tool's import, and a file it rejects may be valid | **Degraded** | an independent catalog (83 elements, versions 2.25–2.45); 0 differences against v2.35 for those elements (checked once with a copy we could read, not redistributed) |
| GSDML coverage | the XSDs and the specification | elements outside the catalog are kept verbatim when a file is read and written, but are not modelled, validated or converted: `ChannelProcessAlarmList`, `UnitDiagTypeList`, `LogBookEntryList`, `SystemDefinedChannelDiagItem`, `ProfileChannelDiagItem`, profile-specific items, the XML signature, and everything added in GSDML 2.46 and 2.50 (I&M6/I&M7, 10BASE-T1L, ConformanceClass E, SXP, protocol policies, …) | **Degraded** | content is preserved, so round trips are lossless |
| GSDML semantic rules | the GSDML specification and PI's GSD Editor (checker) | the rules beyond the schema (for example "PNIO_Version ≥ V2.31 requires LLDP_NoD_Supported") are not checked; generated files have not been run through PI's checker | **Missing** | generated files and the synthetic fixtures (gsdml/testdata/fixtures) are parsed by profinet-py and connect; no engineering tool (CODESYS, TIA Portal) has imported them yet ([research/GSDML-TOOLS.md](research/GSDML-TOOLS.md)) |
| GSDML file generation for products | the GSDML specification | `gsdml.FromModel` writes what our catalog knows; a product shipping a go-profinet-generated GSDML should have it checked with PI's GSD Editor | **Degraded** | |
| GSDX container | GSDX Container Specification (PI order 2.752) | PI's part layout and rules are not applied; signatures are not verified; no `.gsdx` is produced | **Degraded** | `gsdml/gsdx` inspects the public OPC layer (ISO/IEC 29500-2): parts, content types, relationships, signature metadata, GSDML parts; round trips are lossless |
| GSDML signing, Security Class 1 | GSDX specification, PI signing service | no signed GSDML; no Security Class 1 | **Missing** | |
| MRCD | MRCD specification (PI order 2.772) | conformance declarations cannot be read or produced | **Missing** | |
| PROFINET Security (Security Classes 2/3) | PROFINET Security specifications | no authenticated or encrypted PROFINET | **Missing** | see [SECURITY.md](../SECURITY.md) |
| Protocol details | the PROFINET IO specification documents of PI | where the public standard is silent, go-profinet follows what p-net, pnio-rs and profinet-py agree on; rare edge cases may still differ from the specification (examples found so far: alarm sequence start, RPC activity per AR, DCP AllSelector) | **Degraded** | interop matrix (profinet-interop v0.3.0): 193 checks pass, 0 fail ([INTEROPERABILITY.md](../INTEROPERABILITY.md)) |
| Conformance testing | the PI test specifications, the ART tester, certification test bundles | no conformance test has been run; nothing is certified | **Missing** | e2e, chaos, fuzz and interop tests ([TESTING.md](TESTING.md)) |
| Certification | PI certification at an accredited test lab | go-profinet and products built with it cannot be certified as they stand | **Missing** | |
| Vendor ID | PI vendor ID assignment | examples and tests use 0xF000, the range PI uses for examples; a product needs its own vendor ID from PI | **Missing** for products | |
| Controller, device, DCP, LLDP, RPC, RT, alarms, records, diagnosis, I&M, topology | — | none of these need a member artefact to work | **Unaffected** | verified against independent implementations |
| profinetctl | — | its `gsdml validate` inherits the validation gap above and says so in its output (`checked: … not PI's GSDML checker`) | **Degraded** (GSDML only) | |

## What would change with membership

1. **Generate from the official XSDs.** Store PI's download location and
   the sha256 of the specification package in a manifest; the code
   generation step downloads it with a member's credentials (a CI secret)
   or takes a local copy, checks the hash and generates. Only the generated
   code is committed. The catalog remains a fallback.
2. **Validate against the official XSDs** in CI, and run generated files
   through PI's GSD Editor.
3. **Implement GSDX** (read, write, verify) and **MRCD** from their
   specifications.
4. **Check the protocol behaviour** against the PI specifications where we
   followed the reference stacks.
5. **Prepare for certification**: PI's test specifications, then a test lab.

Buying IEC 61158-5-10/6-10 and IEC 61784-2 from IEC does not need PI
membership and would remove part of the "protocol details" degradation; it
does not give access to the XSDs, GSDX, MRCD or the test specifications.

## Where this is flagged

- [LIMITATIONS.md](../LIMITATIONS.md) and the README of go-profinet
- [GSDML.md](GSDML.md) and [CODEGEN.md](CODEGEN.md)
- profinetctl: README and the output of `gsdml validate`
- profinet-interop: the "Evaluated and not used" table (PI Community Stack)
