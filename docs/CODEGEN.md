# Code generation: the GSDML types

The Go types of GSDML documents (`gsdml/zz_generated.go`, about 2600 lines
for 83 elements) and their reference (`gsdml/SCHEMA.md`) are generated from
one input, `gsdml/schema/catalog.json`, by `internal/cmd/gengsdml`.

## Why a catalog, not the XSDs

The GSDML XSDs are published by PROFIBUS & PROFINET International to its
members under a licence that does not allow redistributing them
([research/GSDML.md](research/GSDML.md)). The catalog is written
independently from public descriptions of GSDML and contains no text of the
schemas. It describes the elements and attributes go-profinet models; a
document's other content is preserved verbatim (`Attrs`, `Unknown`), so
reading and writing a file loses nothing.

## The catalog

```json
{"name": "SubslotItem", "attrs": ["SubslotNumber:Unsigned16!", "TextId:IDRef!"]},
{"name": "ApplicationProcess", "children": ["DeviceAccessPointList", "ModuleList?", ..., "ExternalTextList"]},
```

- `simpleTypes` define the attribute types: a pattern, an enumeration, an
  unsigned width (decimal or `0x` hexadecimal), and a doc string.
- `elements` list each element with its doc, its attributes
  (`name:type`, `!` required, `~` always written but may be empty) and its
  children (`?` optional, `*` repeated, `+` one or more) or its text type.
- `versions` are the schema versions the catalog was written for (2.25 to
  2.45).

## The generator

```bash
go generate ./gsdml                         # or: make generate
go run ./internal/cmd/gengsdml -check       # make generate-check: fail when out of date
go run ./internal/cmd/gengsdml -xsd DIR     # compare the catalog with licensed XSDs in DIR
```

For each element it generates a struct with `encoding/xml` tags (attributes
as strings, so that a document round-trips exactly as written, children as
pointers or slices), a `validate` method (required attributes and children,
lexical types, ID uniqueness, ID and text references) and its section in
SCHEMA.md. `gsdml.CatalogSHA256` records the hash of the catalog the code was
generated from; CI fails when they differ (`make generate-check`).

`-xsd DIR` reads the XSDs of a licensed local copy with the small XSD reader
of `internal/xsd` and reports elements and attributes the catalog lacks, has
in excess, or types and requiredness that differ. Against GSDML v2.35 it
reports no difference for the modelled elements.

## Changing the catalog

1. Edit `gsdml/schema/catalog.json` (keep it independent: describe, do not
   copy schema text).
2. `make generate`; review the diff of `zz_generated.go` and `SCHEMA.md`.
3. Add or adjust the conversion in `gsdml/model.go` if the model uses the
   new content, and a test with a fixture.
