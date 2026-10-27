# Coverage

`make coverage-check` measures the statement coverage of the library
packages across all their tests (the controller and device tests and the
end-to-end suite exercise each other's packages: `-coverpkg` is the list of
library packages) and fails below 90 %. The raw Ethernet backends need
privileges and are measured by the real Layer 2 jobs; the examples, the
generator command, the e2e package and the interop tool are not library
code.

Measured on 2026-10-26 (`make coverage-check`, race detector):

| Package | Coverage |
|---------|----------|
| profinet (root) | 100.0% |
| alarm | 100.0% |
| controller | 98.6% |
| dcp | 99.4% |
| device | 99.0% |
| ethernet | 100.0% |
| gsdml | 91.9% |
| internal/xsd | 90.7% |
| l2 | 100.0% |
| l2/osport | 93.8% |
| lldp | 100.0% |
| model | 100.0% |
| netstack | 99.7% |
| pcap | 100.0% |
| pnio | 99.5% |
| rpc | 99.5% |
| rt | 98.8% |
| vnet | 100.0% |
| **total** | **97.8%** |

Targets: 90 % overall, 95 % for the codec packages (`ethernet`, `dcp`,
`lldp`, `rpc`, `pnio`, `rt`, `alarm`); every codec package is above 98 %.

What is not covered is mostly defensive: the generated GSDML validators'
nil-receiver guards, error paths of the operating system (`l2/osport` on
interfaces it cannot open), and errors the XSD reader of `internal/xsd`
only meets in malformed schemas.

Fuzzing complements coverage for the decoders: 21 fuzz targets run in CI
(`make fuzz FUZZTIME=10s`) and locally for longer.
