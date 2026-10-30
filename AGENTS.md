# AGENTS.md — go-profinet

## What this is

Pure Go PROFINET IO library: controller, device, DCP, LLDP, GSDML, MRP. No cgo, no dependencies outside the standard library. Profile: **protocol-library**. Nothing here is certified by PI; claim levels per feature are in docs/FEATURE-STATUS.md.

## Organisation standards

This repository follows **otfabric/engineering policy 1.0**, profile **protocol-library** (`.otfabric/policy.yaml`, mode `baseline`). Where this file and the organisation standards disagree, this file wins here — say so in your report.

## Where the truth lives

| Question | Source |
|---|---|
| What is supported and how well it is verified | docs/FEATURE-STATUS.md, LIMITATIONS.md |
| What PI membership would change | docs/PI-MEMBERSHIP.md |
| Evidence against other stacks | INTEROPERABILITY.md |
| How to test | docs/TESTING.md, docs/COVERAGE.md |
| GSDML types | `gsdml/schema/catalog.json` (docs/CODEGEN.md) |
| Errors | ERRORS.md |
| Release history | RELEASE.md (format stated at its top) |

## Hard rules

1. Never lower `COVER_MIN` (90), add lint suppressions, or skip or delete tests to make CI pass.
2. Never invent protocol semantics. A fact taken from a reference implementation is documented where it is used, with the implementation named.
3. Never claim conformance or interoperability beyond the evidence in docs/FEATURE-STATUS.md.
4. Standard library only; no cgo.
5. No code from GPL or unlicensed implementations: they are oracles in containers, not sources.
6. Every blocking call takes a `context.Context`; every queue is bounded; every component has `Close`.
7. Generated GSDML types change through `gsdml/schema/catalog.json` and `make generate`.

## Commands

| When you change… | Run |
|---|---|
| anything | `make check` |
| transports or protocol machines | `make test-e2e` |
| a raw Ethernet backend | `make l2-test` (Docker) |
| a codec | `make fuzz` (a new decoder needs a fuzz target and golden vectors) |
| interop-relevant behaviour | `make interop-images interop` (Docker; images are built locally as `:dev` from profinet-interop) |

## Definition of Done

`make check` green; tests for new behaviour including error paths; docs/FEATURE-STATUS.md, LIMITATIONS.md and ERRORS.md updated; RELEASE.md section for user-visible changes; failing or skipped steps reported.

## Git and releases

Branches and PRs. Do not push to `main`, tag or run the Release workflow unless the maintainer asks in the session.
