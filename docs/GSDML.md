# GSDML

GSDML (General Station Description Markup Language) is the XML description
of a PROFINET IO device: its identity, its device access points (DAPs), the
modules and submodules it accepts with their IO data and parameter records,
its diagnosis texts and its capabilities. Engineering tools build a
controller's configuration from it.

## Reading

```go
f, _ := os.Open("GSDML-V2.35-Vendor-Device-20240101.xml")
doc, err := gsdml.Parse(f)          // UTF-8 or ISO-8859-1, at most 64 MiB
if err := gsdml.Validate(doc); err != nil {
	var ve *gsdml.ValidationError
	errors.As(err, &ve)             // ve.Issues: path and message of each
}
m, err := gsdml.ToModel(doc, "DAP 1") // "" for the first device access point
```

`ToModel` converts one DAP into a `model.Device`:

- the identity (vendor and device ID, vendor name, family, order number,
  hardware and software release, ObjectUUID_LocalIndex as instance) and the
  capabilities (MinDeviceInterval, conformance class, LLDP, ports);
- the DAP's submodules: the virtual submodules, the interface and the ports
  (system-defined submodules);
- the modules that the DAP fixes or uses in slots (`FixedInSlots`,
  `UsedInSlots`) as the plugged `Slots`, and the other usable modules as the
  `Catalog`;
- per submodule: the IO data lengths (`DataItemSize` of each DataItem) and
  the parameter records with their default values (Const and Ref).

A controller connects with `controller.DeviceConfig{Model: m}`; plug further
modules of the catalog into `m.Slots` first (profinetctl's `--plug`).

## Writing

```go
doc, err := gsdml.FromModel(m, gsdml.GenerateOptions{SupervisorAR: true})
b, err := gsdml.Marshal(doc)
name := gsdml.FileName(m, "", time.Now()) // GSDML-V2.35-<vendor>-<family>-<date>.xml
```

`FromModel` writes the identity, one DAP with the interface and port
submodules, every module type with its submodules, IO data (as
`OctetString` items of the submodule's lengths), parameter records, the
certification info and the external texts. profinet-py parses go-profinet's
generated files and connects with them (the interop suite).

## Validation

`Validate` checks what go-profinet's catalog defines
([CODEGEN.md](CODEGEN.md)): required elements and attributes, the lexical
types of attributes (hexadecimal and decimal unsigned values, value lists,
enumerations, IDs), unique IDs, and that every module, submodule, value
item, graphic, category and text reference resolves. It is not PI's GSDML
checker: it does not read the licensed schemas and does not check the
semantic rules of the GSDML specification. Content outside the catalog is
kept, not validated.

## Unknown content

Elements and attributes the catalog does not define are kept in the `Attrs`
and `Unknown` fields of the element they appear in and written back after
the known ones. Comments and the original order of unknown elements among
known ones are not preserved.

## Not supported

GSDX (the packaged GSDML with graphics) and MRCD: their formats are not
public. See [research/GSDML.md](research/GSDML.md).
