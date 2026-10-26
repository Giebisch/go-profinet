# Interop fixtures

Written for go-profinet (MIT). They describe the configurations of the
reference stacks as facts (identifiers and data lengths), not copies of
their files:

- `pnet-model.json`: the p-net sample application (`pn_dev`) with the IO
  module 0x32 in slot 1 and the echo module 0x40 in slot 2, as its
  `app_gsdml.h` defines them (vendor 0x0493, device 0x0002).
- The pnio-rs sample device is described by its own GSDML file, which is
  MIT licensed and kept in `gsdml/testdata/pnio-rs_sample-16real-32bool.xml`.
