# Contributing to go-profinet

## Getting started

```bash
git clone https://github.com/otfabric/go-profinet.git
cd go-profinet
make test
```

The Makefile sets `GOWORK=off`. Go 1.23 or later; the Docker-based suites
(`make l2-test`, `make interop`) need Docker.

## Before you open a pull request

```bash
make check
```

runs `generate-check` (the generated GSDML types are up to date),
`coverpkg-check`, formatting, `staticcheck`, `golangci-lint`, `go vet`,
`govulncheck`, the tests with the race detector, the coverage gate (90 %)
and builds the examples. For changes to the transports or the protocol
machines also run `make test-e2e` and, when you can, `make interop`.

## Where things go

| Change | Package |
|--------|---------|
| A block, frame or record format | the codec package (`pnio`, `dcp`, `lldp`, `rt`, `alarm`, `rpc`, `ethernet`) with a golden vector and a fuzz target |
| Behaviour of a role | `controller` or `device`, with a test on `vnet` |
| GSDML elements or attributes | `gsdml/schema/catalog.json`, then `make generate` ([docs/CODEGEN.md](docs/CODEGEN.md)) |
| A raw Ethernet backend | `l2/<backend>`, wired into `l2/osport` |

## Conventions

- Standard library only. No cgo except where an OS backend cannot avoid it
  (none does today).
- Every blocking call takes a `context.Context`; every queue is bounded;
  every component has `Close`.
- Errors wrap the sentinels of the root package ([ERRORS.md](ERRORS.md)).
- A decoder never panics on input and has a fuzz target; a codec has golden
  vectors written from the standard, not only round trips.
- Protocol facts from a reference implementation are documented where they
  are used, with the implementation named (see
  [INTEROPERABILITY.md](INTEROPERABILITY.md)).
- Files start with `// SPDX-License-Identifier: MIT`. No code from GPL or
  unlicensed implementations: they are oracles in containers, not sources.
- Comments explain why; tests say what the standard requires.

## Commits and pull requests

Small, focused commits whose messages say why. CI runs `.github/workflows/ci.yml`
(and the Interop workflow) on every pull request.
