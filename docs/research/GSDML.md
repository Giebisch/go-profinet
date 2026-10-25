# PROFINET machine-readable device description sources — research report

Prepared for otfabric/go-profinet (MIT). Retrieved 2026-10-25. I did not log in anywhere and did not accept any click-through licence.
Local artefacts:
- `/private/tmp/claude-501/-Users-bartvanbos-Git-otfabric/c31fe2d1-4c84-460b-b10c-4ecd3712f9ec/scratchpad/gsdml-xsd/` (README + unofficial v2.35 mirror, analysis only)
- `/private/tmp/claude-501/-Users-bartvanbos-Git-otfabric/c31fe2d1-4c84-460b-b10c-4ecd3712f9ec/scratchpad/gsdml-corpus/` (PROVENANCE.md, `commit-ok/`, `reference-only/`)

---

## 0. Verdicts

| Question | Verdict |
|---|---|
| Can the official XSDs be downloaded without membership? | **No.** They ship only inside the GSDML specification package (PI order no. 2.352). That package is "members only", and you must accept the PNO Disclaimer / License Agreement before downloading. |
| Can the XSDs be committed to an MIT repo (as testdata or generator input)? | **No.** The PNO specification licence allows use "exclusively for developing and supporting products compliant with the SPECIFICATION". It forbids redistribution to third parties, except for "Have Made" purposes, and says each copy "must be obtained on an individual basis, directly from www.profibus.com". The XSDs carry no copyright header of their own (checked in v2.35), but that does not grant any rights. |
| Can they be used as generator input? | **Yes, locally, by a licensee.** A developer who holds a legitimately obtained copy (PI membership, or the copy TIA Portal installs, e.g. `…\Portal V21\Data\Hwcn\Custom\Interpreter\GSD\XSD\GSDML-DeviceProfile-v2.4.xsd`) may run the generator. The generated Go code should be **hand-normalised structs** (facts: element and attribute names, enumerations). It must not contain verbatim XSD text or documentation strings. Keep the XSDs out of git (`.gitignore`, env var `GSDML_XSD_DIR`). Only CI jobs with a licensed copy should run XSD validation. |
| Which versions exist? | XSD/spec versions seen: 1.0, 2.0, 2.1, 2.2, 2.25, 2.3, 2.31, 2.32, 2.33, 2.34, 2.35, 2.4, 2.41, 2.42, 2.43, 2.44, 2.45, 2.46 and **2.50 (latest)**. The 2.50 file is `GSDML-Spec_2352_V250_May26.zip`, 3 MB, listed on profibus.com 11/06/2026. |
| Corpus files that can be committed | **Only `pnio-rs_sample-16real-32bool.xml`** (MIT OR Apache-2.0, author-generated). Everything else found is vendor-copyrighted or GPL: keep it as fetch-on-demand testdata (URL + sha256 manifest), not in git. The best option is to author our own synthetic GSDML files under MIT, using VendorID 0xF000 or 0xFFFF. |

---

## 1. GSDML XSD schemas

### 1.1 Official publication
- PI download page: https://www.profibus.com/download/gsdml-gsdx-specification-for-profinet ("GSDML Specification for PROFINET", order no. 2.352, English).
  - Current file: `GSDML-Spec_2352_V250_May26.zip`, ZIP, 3 MB, listed 11/06/2026.
  - Access: "This area is for members only. Please login…" and "Please accept the Disclaimer / License Agreement before downloading."
  - The page does not list the zip contents. Two facts suggest the zip carries the schema and examples. First, the V2.50 change log says the spec is "restructured to better reflect the XSD schema structure". Second, PI's GSD pages say the spec package and the checker contain example GSD files. Third-party installations agree (TIA Portal ships `GSDML-DeviceProfile-v2.4.xsd` + `GSDML-Primitives-v2.4.xsd`, per core-engineering/pnio-rs `docs/gsdml.md`).
- Related members-only tooling:
  - PROFINET GSD Editor (formerly "GSD Checker"), https://www.profibus.com/download/profinet-gsd-editor. V2.46 is `PROFINET_GSD_Editor_V2.46.zip`. V2.50 is `PROFINET_GSD_Editor_V2.50.zip` (07/09/2026) with a `.checksum` file. Copyright PNO 2025–2026, free for members. From 2027-01-01 it is limited to PI Technology Access (PTA) or RPA holders.
  - The GSDML Upgrade Tool (`UpgradeGSDML_2.45.zip`) has been retired, and its function moved into GSD Editor 2.50.
- profinet.com mirrors the same PI download system, and I found no separate free schema download.

### 1.2 Licence terms (PNO standard download licence, quoted from profibus.com download pages)
- Licensor: PROFIBUS Nutzerorganisation e.V., Karlsruhe.
- Grant: "the right to use the SPECIFICATION exclusively for developing and supporting products compliant with the SPECIFICATION. Licensee may copy the SPECIFICATION for this purpose and for data backup purposes."
- Redistribution: "Licensee is not entitled to copy or redistribute the SPECIFICATION to any third party … except … 'Have Made' purposes." Copies must be obtained individually from www.profibus.com.
- Other clauses: no modification or reverse engineering, notices must be kept intact, German law applies.
- **Consequence:** the XSDs count as part of the "SPECIFICATION" deliverable. Committing them to a public MIT repo is redistribution to third parties, so it is **not permitted**.

### 1.3 Copies in public repos (none confer rights)

| Repo | Files | Repo licence | Assessment |
|---|---|---|---|
| TINF17C/GSD2AML-Converter (`src/Gsd2Aml.Lib/Models/xsd/`) | GSDML-DeviceProfile-v2.35.xsd, GSDML-Primitives-v2.35.xsd, xml.xsd, xmldsig-core-schema.xsd | AGPL-3.0 | PI copyright; the AGPL cannot relicense PI material. Used here **only** for structural analysis. |
| DaltonManning/lastCBOpenIF (`ControlBuilderMProfessional/Parsers/Profinet/XSD/`) | DeviceProfile and Primitives V1.0, V2.0, V2.1, v2.2 | none | Unlicensed (appears to be leaked ABB Control Builder content). Avoid. |
| rtlabs-com/p-net | none (the sample GSDML references `..\xsd\GSDML-DeviceProfile-V2.4.xsd` but does not ship it) | GPLv3/commercial | — |
| core-engineering/pnio-rs | deliberately **not vendored** ("its licence has not been checked") | MIT/Apache-2.0 | Good precedent for us to follow. |
| Wireshark | does not ship XSDs. It parses user-supplied GSDML with libxml2 (`pnio_gsd` path preference in `plugins/epan/profinet/packet-dcerpc-pn-io.c`). | GPL-2.0+ | — |

A GitHub code search for `filename:GSDML-DeviceProfile` returned only the two repos above. I found **no public copy of any 2.4x XSD.**

`xml.xsd` (W3C, http://www.w3.org/2001/xml.xsd) and `xmldsig-core-schema.xsd` (W3C/IETF, "Copyright 2001 The Internet Society and W3C", W3C Software licence) are **independently redistributable**. Fetch them from w3.org, not from PI bundles.

### 1.4 Versions
- Versions are evidenced by `xsi:schemaLocation` values in the vendor files I found: V2.0, 2.1, 2.2, 2.25, 2.3, 2.31, 2.32, 2.33, 2.34, 2.35, 2.4, 2.41, 2.42, 2.43, 2.44, 2.45. Example files: Keyence 2.25, SMC 2.3, UR 2.31, Python-profinet 2.32, Zebra 2.33, Aventics 2.34, Beckhoff 2.35/2.41/2.43, KUKA 2.4, Festo 2.41, UR 2.42, ABB OmniCore 2.43, Cognex 2.44, KUKA 2.45 (20260414).
- The PI change log adds 2.46 and 2.50.
- Schema `version` attribute and dates:
  - v2.35 has `version="2.35"` and `<schemainfo versiondate="20180423"/>`.
  - I could not verify the dates of the other versions without the members-only package.
  - The latest listed version is **V2.50 (zip named "May26", listed 11/06/2026)**.
- Change log highlights (public, from the PI page):

| Version | Changes |
|---|---|
| 2.35 | Channel process alarms, MRP interconnection |
| 2.4 | TSN, RSI, SFP diagnosis, PROFIsafe PIR |
| 2.41 | GSDML signing, ProfileProcessAutomation |
| 2.42 | Stream RT class, 802.1AS, Shared R1 |
| 2.43 | APL, CIM, version-independent records, **GSDX container**, SecurityClass 1/2/3 |
| 2.44 | F_BaseID record, readable submodule record metadata, ApplicationClass Drive/Encoder |
| 2.45 | Non-contiguous channel data, Ed448 optional, PROFIsafeFSCP_TestMode_Supported, examples moved to VendorID 0xF000 |
| 2.46 | I&M6 OMLOCLocationPoint, I&M7 IdentificationLink, 10Base-T1L without APL |
| 2.50 | Spec restructured to follow the XSD; SXP transport; ConformanceClass "E" added and "D" removed; PoweredDevices; configurable LinkSpeed; protocol policies; ReadableEvents; SecureAccess/SecureRealtime ApplicationClasses; SecurityClass 2/3 removed |

- The PI GSD Editor 2.50 mentions supporting "GSDML files under Amendment 1".
- Note: PI writes the version as both "V2.4.1" and "V2.41". Filenames use `V2.41`.

### 1.5 Schema structure (from the v2.35 analysis copy)
- **Files.** `GSDML-DeviceProfile-vX.Y.xsd` (93 KB) has targetNamespace `http://www.profibus.com/GSDML/2003/11/DeviceProfile` (prefix `gsdml`). It imports:
  - `http://www.profibus.com/GSDML/2003/11/Primitives` (`GSDML-Primitives-vX.Y.xsd`, prefix `base`)
  - `http://www.w3.org/XML/1998/namespace` (`xml.xsd`, for `xml:lang` on `Language`)
  - `http://www.w3.org/2000/09/xmldsig#` (`xmldsig-core-schema.xsd`, `ds:Signature`, optional at the end of `ISO15745Profile`)
- **Form defaults.** `elementFormDefault="qualified"`, `attributeFormDefault="unqualified"`.
- **No `xs:include`.** The files use only `xs:import` across 4 namespaces.
- **Single global element:** `ISO15745Profile` = `ProfileHeader` (ISO 15745-1 boilerplate) + `ProfileBody` + optional `ds:Signature`. Primitives also declares the global `ExternalTextDocument`.
- **ProfileBodyT:** `DeviceIdentity` (VendorID/DeviceID hex, InfoText, VendorName), `DeviceFunction` (`Family` MainFamily/ProductFamily), `ApplicationProcess`.
- **ApplicationProcessT children:**
  - `DeviceAccessPointList/DeviceAccessPointItem`
  - `ModuleList/ModuleItem`
  - `SubmoduleList/(SubmoduleItem|PortSubmoduleItem)`
  - `ValueList/ValueItem`
  - `ChannelDiagList/(ChannelDiagItem|SystemDefinedChannelDiagItem|ProfileChannelDiagItem)`
  - `ChannelProcessAlarmList/…` (2.35+)
  - `UnitDiagTypeList/(UnitDiagTypeItem|ProfileUnitDiagTypeItem)`
  - `LogBookEntryList/LogBookEntryItem`
  - `GraphicsList/GraphicItem`
  - `CategoryList/CategoryItem`
  - `ExternalTextList/(PrimaryLanguage|Language)/Text`
- **XSD feature inventory** (DeviceProfile v2.35 counts, plus Primitives):
  - complexType: 65 named, 78 anonymous. simpleType: **86, all anonymous** in DeviceProfile (named ones live in Primitives, 42).
  - `complexContent/extension` (19, mostly `base:ObjectT` with an `ID` attribute), `complexContent/restriction` (1, `xsd:anyType` for SVG).
  - `sequence` 81, `choice` 11 (several `maxOccurs="unbounded"`), `xs:group` (1 named model group, `RecordDataBaseG`, used twice).
  - Attributes: 346, many with `default`/`fixed` (87 `xsd:boolean`, mostly with defaults). Defaults matter semantically, e.g. `NumberOfAR=1`, `SupportedRT_Class=Class1`, `AddressAssignment=DCP`, `MaxSupportedRecordSize=4068`, `RemoteApplicationTimeout=300`.
  - Restrictions: enumeration (208 in Primitives), pattern (22), min/maxInclusive (105), maxExclusive, length.
  - `xs:union`: 4 (e.g. `IASInterfaceEnumT` = enumeration ∪ 4-character string; `ErrorType` = 0..31 ∪ 32768..36863).
  - Identity constraints: `xs:key` 7, `xs:keyref` 13, `xs:unique` 61. Examples: TextId → ExternalText, ValueItemTarget, ModuleItemTarget, GraphicItemTarget, CategoryRef.
  - `xs:any namespace="##any" processContents="lax"` + `mixed="true"` (GraphicItem/Embedded SVG).
  - **Not used:** `xs:include`, `xs:list` (lists are encoded as pattern-restricted strings), `substitutionGroup`, `abstract`, `attributeGroup`, `redefine`, `nillable`.
  - Primitive bases: string, token, normalizedString, boolean, float, integer, positiveInteger, unsignedByte, unsignedShort, unsignedInt, date, anyURI.
- **Micro-syntaxes encoded in patterns.** The generator must turn these into custom Go types with parsers:
  - `ValueListT` `((\d+\.\.\d+)|(\d+))(( \d+\.\.\d+)|( \d+))*`, e.g. `"1..4 6"`, used by PhysicalSlots, AllowedInSlots, Writeable_IM_Records, ResetToFactoryModes
  - `SignedOrFloatValueListT`
  - `Unsigned16hexT` / `Unsigned32hexT` `0x[0-9a-fA-F]{1,8}` (plus a "non-zero" variant)
  - `TokenListT` / `TokenList1T` / `TokenList1exT` (`;`-separated)
  - `VersionStringT` `V\d+\.\d+`
  - `"0xNN,0xNN"` octet lists
  - space-separated flag sets (`"SIL1 SIL2 SIL3 NoSIL"`, `"Check NoCheck"`)
  - DNS name pattern, http(s) URL pattern

### 1.6 What an XSD→Go generator must support (minimum)
1. Multiple namespaces via `xs:import` with schemaLocation resolution. Map the W3C `xml:lang` and `ds:Signature` to hand-written types: keep the raw `ds:Signature` as `[]byte`/innerxml. Do not model XML-DSig.
2. Named and **anonymous** complex and simple types. Synthesise names from the element path, e.g. `DeviceAccessPointItem_CertificationInfo`.
3. `complexContent/extension` (embed the base struct). One `restriction` of `anyType` maps to innerxml.
4. Sequence, choice (unbounded, mixed children), and named model groups (`xs:group ref`). For unbounded choices, prefer one slice per element name. For ordering-sensitive cases use an ordered `[]any`.
5. Attributes with `use=required`, `default` and `fixed`. Emit default-application helpers, because Go `encoding/xml` has no defaults.
6. simpleType restrictions: enumeration → string-typed constants; pattern/range → validation funcs; union → string + validator; the pattern-encoded list micro-types → custom `UnmarshalXMLAttr`/`MarshalXMLAttr` types.
7. `xs:any lax` + `mixed` → `,innerxml`.
8. Identity constraints (key/keyref/unique), as a post-parse validation pass. The keys use restricted XPath (`.//gsdml:X|.//gsdml:Y`, `@Attr`), so either implement a tiny XPath subset or hand-code the ~80 constraints.
9. Version handling: one namespace for every version (it never changes, `2003/11`). Version is signalled only by the schemaLocation filename and `PNIO_Version`/`RequiredSchemaVersion` attributes. Generate a **superset model** (union of 2.25…2.4x) with version annotations, not one package per version.
10. Encoding and size: real files are UTF-8 (with or without BOM) **or ISO-8859-1** (rt-labs, Siemens SIMOCODE), so a `CharsetReader` is mandatory. Files reach 15 MB (Beckhoff EK9300), so use streaming decode.
11. Out of scope for generation: the GSD Checker's extra semantic rules (TIA rule codes `0x00020020_*`, per pnio-rs). The XSD is "necessary but not sufficient".

---

## 2. GSDML specification document (order no. 2.352)
- **Not public.** Members-only plus licence click-through (see §1.1). I could not read the text. The facts below come from public secondary sources (the PI page change log, rt-labs p-net docs, HMS KB, the XSD itself).
- **File naming:** spec §5.1 (per rt-labs `doc/creating_gsdml_files.rst`, p-net v0.2.0). The pattern is `GSDML-V<schema version>-<vendor>-<device family>-<yyyymmdd>[-<hhmmss>].xml`, e.g. `GSDML-V2.35-Vendor-Device-20171231.xml`. HMS KB notes that TIA rejects misnamed files ("Checker couldn't check Gsd file"). Bitmaps are referenced as `GSDML-<VendorID>-<DeviceID>-<name>.bmp` by convention (e.g. `GSDML-0120-23C1-EK9300.bmp`).
- **DataItem `DataType` values** (`DataItemTypeEnumT`, v2.35):
  - Integer8/16/32/64, Unsigned8/16/32/64, Float32, Float64, Boolean
  - Date, TimeOfDay with/without date indication, TimeDifference with/without date indication, NetworkTime, NetworkTimeDifference, TimeStamp, TimeStampDifference, TimeStampDifferenceShort
  - VisibleString, OctetString, UnicodeString8, 61131_STRING, 61131_WSTRING
  - Float32+Status8, Float32+Unsigned8, Unsigned8+Unsigned8, OctetString2+Unsigned8
  - F_MessageTrailer4Byte, F_MessageTrailer5Byte
  - Unsigned8_S, Unsigned16_S, Integer16_S, OctetString_S
  - Drive/PROFIdrive types N2, N4, V2, L2, R2, T2, T4, D2, E2, C4, X2, X4, Unipolar2.16
- **`Ref` DataType values** (`RecordDataRefTypeEnumT`, used in ParameterRecordDataItem/Ref): Bit and BitArea, plus most of the types above. It excludes Float32+Status8 and the F_MessageTrailer types.
- **ParameterRecordDataItem** has Index (required), Length (required), TransferSequence (default 0) and ChangeableWithBump. Its children follow `RecordDataBaseG`: Name, Const (`Data="0x..,0x.."`, ByteOffset), Ref, MenuList.
- **Ref** has DataType, ByteOffset, BitOffset, BitLength, DefaultValue (required), AllowedValues (`SignedOrFloatValueListT`), Changeable, Visible, TextId and ValueItemTarget. `ValueItem` provides `Assign` (Content ↔ TextId) tables and Help.
- **I&M:** `Writeable_IM_Records` (ValueList, e.g. `"1 2 3"`) and `IM5_Supported` on DAP, interface and port submodules. PI's change log adds I&M6/I&M7 in 2.46. rt-labs says I&M0 and I&M5 handling is defined per submodule.
- **Capability attributes:**
  - DAP: PNIO_Version, PhysicalSlots, MinDeviceInterval (×31.25 µs, Unsigned16), DNS_CompatibleName, FixedInSlots, ObjectUUID_LocalIndex, RequiredSchemaVersion, MultipleWriteSupported, IOXS_Required, AddressAssignment (default `DCP`), RemoteApplicationTimeout, MaxSupportedRecordSize, PowerOnToCommReady, NameOfStationNotTransferable, SharedDeviceSupported, DeviceAccessSupported, NumberOfImplicitAR, CIR_Supported, LLDP_NoD_Supported, ResetToFactoryModes, CheckDeviceID_Allowed, PROFIenergyASE_Supported, …
  - CertificationInfo: ConformanceClass `A|B|C` (2.35; 2.50 adds E and drops D), ApplicationClass, NetloadClass `I|II|III`.
  - IOConfigData: MaxInputLength, MaxOutputLength, MaxDataLength.
  - InterfaceSubmoduleItem: SupportedRT_Class(es) (`RT_CLASS_1`, `RT_CLASS_2`, `RT_CLASS_3`, `RT_CLASS_STREAM_*`), IsochroneModeSupported, SupportedProtocols (required: SNMP, LLDP), DCP_HelloSupported, PTP_BoundarySupported, DCP_BoundarySupported, MulticastBoundarySupported, DelayMeasurementSupported, PDEV_CombinedObjectSupported, MRP attributes, and `ApplicationRelations` with `NumberOfAR` (default 1), StartupMode and timing.
  - PortSubmoduleItem: MAUType(s), MaxPortRx/TxDelay, LinkStateDiagnosisCapability, …
  - These are facts readable from the XSD and public docs. Semantic rules (e.g. `PNIO_Version ≥ V2.31` requires LLDP_NoD_Supported, ResetToFactoryModes="2", PTP/DCP boundary) are checker rules from the spec; pnio-rs documents them publicly.

---

## 3. GSDX
- **What it is:** a signed, packaged form of GSDML. It is a **ZIP-based OPC container** (ISO/IEC 29500-2:2021 Open Packaging Conventions) holding the GSDML XML, its referenced resources (bitmaps, etc.) and a digital signature object. The file extension is `.gsdx`.
- **Signature:** XML-DSig (W3C / IETF RFC 3275) with **XAdES B-LT** (ETSI EN 319 132-1). Manufacturer certificates come from the PNO CA and are valid for 3 years, so validation uses the PNO-issued public key certificate. Sources: PI press release "Signed GSDML files for greater security" (11/2022), "Signing service established for GSD files" (2023-04-14), and https://www.profibus.com/products/gsd-files/gsd-signing.
- **Specification:** "GSDX Container Specification", PI order **2.752**, https://www.profibus.com/download/gsdx-container-specification.
  - **V1.2** was published. **V1.3** is now listed (`GSDX-Spec_2752_V13_May26.zip`, 2 MB, 09/06/2026); a V1.3 draft was out for review with comments due 2026-02-14.
  - Stated goals: package files and resources, validate integrity and authenticity, link origin to a vendor or publisher, and support engineering-tool integration.
  - **Members-only plus licence click-through.** The 2023 press release calls the container format "standardized and publicly available", meaning the OPC basis (ISO/IEC 29500-2) is public. **PI's GSDX profile itself (part names, relationship types, content types, manifest and metadata layout, signature-origin part) is not publicly specified.** I found no public description of the internal part layout.
- **Version links:** the GSDX container was introduced in GSDML 2.43, and SecurityClass1 is tied to it. In 2.46 the import marker "should be adapted to GSDX spec". Signing is linked to 2.41.
- **Signing service:** members only, 900 € base fee + 190 € per signing (PNO signing service).
- **Tooling:** GSD Editor ≥ 2.45 opens GSDX and validates signatures (saving the file drops the signature). TIA Portal imports GSDX; STEP 7 Classic does not (Siemens forum).
- **Public samples:** none found on GitHub (`extension:gsdx` → 0 hits).
- **Implementation note:** a Go reader can open a `.gsdx` with `archive/zip` and locate the GSDML part via `[Content_Types].xml` and `_rels/.rels` (standard OPC, public). Signature verification needs the PI-profiled XAdES rules, which are not public. Treat the signature as opaque or optional.
- **It is not the GSD Checker format.** The GSD Checker/Editor is a separate tool.

---

## 4. MRCD — Machine Readable Conformance Declaration
- "MRCD Specification – Machine Readable Conformance Declaration", PI order **2.772**, **V1.0**, `MRCD-Spec_2772_V10_Apr26.pdf` (1 MB), listed 16/06/2026. Page: https://www.profibus.com/download/mrcd-specification-machine-readable-conformance-declaration. A draft was out for review earlier (comments due 2026-02-14).
- Public description: a file format "issued by PI as the certificate authority for PROFINET". It "shall be embedded in the GSDX container for every certified GSDML file" and describes the device's **certified capabilities**, so engineering systems can check validity.
- **Members-only plus click-through.** The public page does not say whether the format is XML, whether an XSD exists, or how it is signed. Being embedded in an XAdES-signed OPC container suggests an XML part, but **this is unverified**.
- I found no public schema or sample, and no link to the ART tester output format. Verdict: **not publicly specified; do not plan code generation from it.**

---

## 5. GSDML sample corpus

| Source | URL | GSDML ver. | Terms | Commit to MIT repo? |
|---|---|---|---|---|
| **core-engineering/pnio-rs** `crates/pnio/testdata/gsdml/sample-16real-32bool.xml` | raw @276202284f38… | schema 2.4, PNIO_Version V2.3 | MIT OR Apache-2.0, generated by the project's own code (VendorID 0xFFFF dev ID) | **Yes** (keep the MIT notice). Downloaded to `commit-ok/`. |
| rt-labs p-net sample `GSDML-V2.4-RT-Labs-P-Net-Sample-App-20220324.xml` | raw @ tag v0.2.0 `sample_app/` | 2.4 / V2.4 | No file header; repo dual **GPLv3**/commercial. Removed from the current public branch. | **No** (GPL). Downloaded to `reference-only/`. |
| Apache PLC4X test resources (`plc4j/drivers/profinet-ng/src/test/resources/`): Advantech ADAM6100 V2.2, Siemens SIMOCODE pro V PN V2.3 and V2.43, WAGO 750/753 V2.33, `gsdml.xml` (which is the rt-labs p-net sample) | raw @0afc5c5a… | 2.2–2.43 | ASF Apache-2.0 header **prepended to vendor and GPL files**; this does not establish vendor permission. | **No.** WAGO and SIMOCODE 2.43 downloaded to `reference-only/`. |
| Inxton/AXOpen, Inxton/TcOpen | many vendor GSDML (ABB, Cognex 2.44, KUKA 2.45, Festo, Keyence, UR, Balluff, SMC, …) | 2.2–2.45 | MIT repo, but the files are vendor-copyrighted | **No.** Useful as a fetch-on-demand list across versions. |
| siamect/proview, ProviewRDev | ABB FENA 2.25/2.31, Siemens ET200M/S 2.1 | old | no licence or GPL | No |
| DaCookie4u/python-profinet | `GSDML-V2.32-#Python-Python_Profinet_IO-20181019.xml` | 2.32 | GPL-3.0 (author-made) | No (GPL) |
| work2it/profinet-linux | `GSDML-V2.3-PN-VIRTUAL-20251230.xml` | 2.3 | no licence | No |
| Beckhoff EK9300 | https://download.beckhoff.com/download/configuration-files/io/ethercat/ek9300/ (open directory: 2.33…2.43 zips) | 2.43 (20250128), XML 15.8 MB + BMP | No licence in zip; Beckhoff copyright; no login or terms gate | **No**, fetch-on-demand. Downloaded to `reference-only/`. |
| Siemens ET200SP (SIOS), WAGO 750-375 (wago.com product downloads), Phoenix Contact, Schneider, ABB, Hilscher netX | vendor portals | various | Vendor site terms; some require login; no redistribution grant | Not fetched; same verdict: fetch-on-demand at most |

**Recommendation:**
- Commit `commit-ok/` only.
- Author synthetic MIT GSDML fixtures for edge cases: ValueList micro-syntax, unions, ISO-8859-1 encoding, BOM, GraphicItem SVG `Embedded`, ChannelDiag with ExtChannelDiag, ParameterRecordDataItem with Ref/Const/ValueItem, and multiple languages. Use VendorID 0xF000, which PI itself uses for examples from 2.45.
- Keep a `testdata/external.json` manifest (URL + sha256) and a `make fetch-corpus` target for the vendor files above, run in CI but not committed.

---

## 6. Other machine-readable sources
- **Wireshark** `plugins/epan/profinet/packet-dcerpc-pn-io.c` (25 k lines, 187 `value_string` tables, SPDX GPL-2.0-or-later) and `packet-pn-dcp.c`, `packet-pn-rt.c`, `packet-pn-ptcp.c`, `packet-pn-mrp.c`, `packet-pn-rsi.c`, `packet-pn-sxp.c`, `packet-pn.c`. The tables cover block types, error codes, ChannelErrorType/ExtChannelErrorType, record indices and alarm types.
  - Licence implication: numeric code ↔ name mappings are facts and not copyrightable as such. Copying the C tables, or mechanically transforming the source file (including comments, ordering and selection), risks creating a derivative work.
  - Safe practice: re-key the values from the normative spec or from observation, use Wireshark only for cross-checking, and do not run code generators over Wireshark source.
  - Note: Wireshark can load GSDML files to name submodules (libxml2, `pnio_gsd` preference). This is useful for interop tests.
- **Scapy** `scapy/contrib/pnio.py`, `pnio_rpc.py`, `pnio_dcp.py` (SPDX GPL-2.0-or-later). Same facts-versus-code caution applies.
- **rt-labs p-net** (`src/`, GPLv3): C enums for error codes, block types and I&M. Same caution applies.
- **PI documents:**
  - The "Diagnosis for PROFINET" guideline and the "Profile Guidelines Part 1: I&M functions" are PI downloads. Most are members-only, a few are public. Check each page individually, because the PNO licence above typically applies.
  - The ChannelErrorType/ExtChannelErrorType normative lists live in IEC 61158-6-10.
  - The GSDML XSD `SystemDefinedChannelDiagItem`/`ProfileChannelDiagItem` mechanism lets vendors' GSDML carry vendor-specific diagnostic texts. Parse those at runtime instead of generating them.
- **IEC 61158-6-10:2023** (Ed. 5.0, published 2023-03-24, Type 10 = PROFINET application layer protocol): purchase only, https://webstore.iec.ch/publication/83418. Resellers quote about USD 779 single-user (Nimonik) and NZD 693 (Standards NZ). It is also available as EN IEC 61158-6-10:2023 and BS EN. IEC 61158-5-10 (services) and IEC 61784-2 (CPF3 profiles) are likewise paid.
  - Licence: the purchaser may implement it, and the numeric values (facts) may appear in MIT code. Reproducing tables or text verbatim is not allowed.

---

## Sources
- https://www.profibus.com/download/gsdml-gsdx-specification-for-profinet
- https://www.profibus.com/download/gsdx-container-specification
- https://www.profibus.com/download/mrcd-specification-machine-readable-conformance-declaration
- https://www.profibus.com/download/profinet-gsd-editor
- https://profibus.com/download/profinet-gsdml-upgrade-tool
- https://www.profibus.com/newsroom/press-news/signed-gsdml-files-for-greater-security
- https://www.profibus.com/newsroom/press-news/signing-service-established-for-gsd-files
- https://www.profibus.com/products/gsd-files/gsd-signing
- PNO download licence text: e.g. https://www.profibus.com/download/profinet-conformance-class-a-cabling, https://cl.profibus.com/downloads/fdt-interface-specification
- https://github.com/TINF17C/GSD2AML-Converter (commit d1a35b7c0d0db2385634cfb6befc208ebd1e960c)
- https://github.com/DaltonManning/lastCBOpenIF
- https://github.com/rtlabs-com/p-net (tag v0.2.0: `doc/creating_gsdml_files.rst`, `sample_app/`)
- https://github.com/core-engineering/pnio-rs (`docs/gsdml.md`, testdata)
- https://github.com/apache/plc4x (develop @0afc5c5a63a93b163846ca69644cc2e332324a83)
- https://github.com/Inxton/AXOpen, https://github.com/Inxton/TcOpen
- https://download.beckhoff.com/download/configuration-files/io/ethercat/ek9300/
- https://www.hms-networks.com/support/tech-support/kb-articles/9912450786578
- https://github.com/wireshark/wireshark/tree/master/plugins/epan/profinet
- https://github.com/secdev/scapy/tree/master/scapy/contrib
- https://webstore.iec.ch/publication/83418
