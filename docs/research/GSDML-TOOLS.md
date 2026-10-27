# GSDML fixtures and engineering tools

Status: 2026-10-27.

go-profinet's GSDML support is checked against its own catalog of the
schema (not PI's XSDs, which are members-only: [PI-MEMBERSHIP.md](../PI-MEMBERSHIP.md)).
What it can add without membership is evidence from other software that
reads GSDML: an independent parser and controller, and engineering tools
run by hand. This page describes the synthetic fixtures that serve as the
common input, how their imports are recorded, and what has been run.

## The fixtures

`gsdml/testdata/fixtures/` (MIT, written for go-profinet, vendor ID 0xF000,
PI's range for examples, no vendor data):

| File | Device access points | Covers |
|------|----------------------|--------|
| `GSDML-V2.43-OTFabric-ModularFixture-20261027.xml` | `DAP_1P`: one port, one AR. `DAP_2P`: two ports, shared device (`SharedDeviceSupported`), device access AR, conformance class B | a modular device (modules proposed with `UsedInSlots`, allowed in 1..8/1..16), input, output and combined submodules, three parameter records (bit, bit area, unsigned and signed values with ranges and enumerations, constant octets), process alarms, a channel diagnosis with extended diagnoses, categories |
| `GSDML-V2.35-OTFabric-CompactFixture-20261027.xml` | `DAP_C`: one port | a compact device (every module `FixedInSlots`), a combined submodule, a module with two submodules in fixed subslots, a parameter record with an enumeration, a channel diagnosis, legacy and advanced startup |

Every fixture is tested on each change:

- `gsdml.Check` reports no error, warning or unvalidated content; the file
  round-trips (`TestFixtures`).
- Each device access point becomes a model that validates, with the
  expected ports, slots, IO lengths and records; the records take the
  defaults of their `Ref`s at their offsets (`TestFixtureRecords`).
- A go-profinet IO-Device and IO-Controller configured from each access
  point connect on the virtual network, write parameters other than the
  defaults, exchange every input and output and read the records back;
  `DAP_2P` runs as a two-port device shared by two controllers, one on each
  port (e2e `TestFixtureDevices`).
- profinet-py, an independent GSDML parser and IO-Controller, reads each
  one-port fixture and runs a cyclic AR with a go-profinet device configured
  from the same file (interop `TestProfinetPyFixtures`).

## What independent software made of them

| Software | Fixture | Result |
|----------|---------|--------|
| profinet-py 0.6.6 (`load_gsdml`, `build_io_slots`, `start_cyclic`) | Compact, `DAP_C` | parsed; the configuration it built (slots 1 to 3, subslots 1 and 2 of slot 2, IO lengths 5/1, 2, 4, 6) equals go-profinet's; cyclic AR established, inputs read |
| profinet-py 0.6.6 | Modular, `DAP_1P` | parsed; profinet-py plugs only the modules a DAP fixes (`FixedInSlots`) and ignores the proposed configuration (`UsedInSlots`), so it connected with the DAP alone (recorded as not applicable). go-profinet's `ToModel` plugs the `UsedInSlots` modules too. Both readings are defensible: `UsedInSlots` is a default an engineering tool proposes, not a requirement |
| CODESYS Development System | all | not run (needs Windows; recipe: [INDEPENDENT-CONTROLLER.md](INDEPENDENT-CONTROLLER.md), step 5) |
| Siemens TIA Portal (trial) | all | not run (needs Windows and a trial licence) |

## Recording an engineering-tool import

`gsdml/testdata/fixtures/evidence.json` holds one entry per import: the
tool and its version, the date, the fixture (or `*`), the outcome
(`imported`, `imported-with-warnings`, `rejected` or `not-run`) and the
tool's messages verbatim. `TestFixtureEvidence` refuses an outcome without
a version and a date. To add one:

1. Install the tool (on the machine it supports) and record its exact
   version.
2. Import each fixture into the tool's device catalog (CODESYS: Tools →
   Device Repository → Install; TIA Portal: Options → Manage general station
   description files).
3. For each device access point, insert the device into a project with its
   proposed modules, and compile the hardware configuration.
4. Copy every message (errors, warnings, notes) verbatim into the entry;
   take screenshots only for your own records (the tools' user interfaces
   are not ours to publish).
5. Optionally connect the project's controller to a go-profinet device
   started with `otfnode device -model FILE -dap DAP` and record the result
   as interoperability evidence (INDEPENDENT-CONTROLLER.md).

Nothing in git comes from a tool: no XSD, catalog export or binary; only the
messages and outcomes.

## Validation rules

No GSDML rule was added in this round: the fixtures pass every catalog and
semantic rule, and no tool output is available yet to substantiate a new
one. A rule is added only with a public basis (the catalog, Wireshark's or a
reference stack's reading) or as a clearly labelled local heuristic
(docs/GSDML.md lists each rule with its basis).
