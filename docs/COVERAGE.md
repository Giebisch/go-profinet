# Coverage

`make coverage-check` measures the statement coverage of the library
packages across all their tests (the controller and device tests and the
end-to-end suite exercise each other's packages: `-coverpkg` is the list of
library packages) and fails below 90 %. The raw Ethernet backends need
privileges and are measured by the real Layer 2 jobs; the examples, the
generator command, the e2e package and the interop tool are not library
code.

Measured on 2026-10-28 at v0.5.0 (`make coverage`, race detector;
`scripts/coverage-by-package.sh coverage.out` prints the table):

| Package | Coverage |
|---------|----------|
| profinet (root) | 100.0% |
| alarm | 100.0% |
| controller | 96.9% |
| dcp | 99.3% |
| device | 96.4% |
| ethernet | 100.0% |
| gsdml | 93.0% |
| gsdml/gsdx | 98.1% |
| internal/xsd | 90.7% |
| l2 | 100.0% |
| l2/bridge | 93.2% |
| l2/osport | 93.8% |
| lldp | 98.9% |
| model | 100.0% |
| mrp | 91.5% |
| netstack | 99.7% |
| pcap | 100.0% |
| pnio | 98.5% |
| ptcp | 100.0% |
| rpc | 99.3% |
| rt | 98.9% |
| snmp | 99.9% |
| store | 97.3% |
| vnet | 100.0% |
| **total** | **97.1%** |

Targets: 90 % overall, 95 % for the codec packages (`ethernet`, `dcp`,
`lldp`, `rpc`, `pnio`, `rt`, `alarm`); every codec package is above 98 %.

What is not covered is mostly defensive (in `mrp` and `l2/bridge` also
timer races that the ring tests reach only some of the time): the generated GSDML validators'
nil-receiver guards, error paths of the operating system (`l2/osport` on
interfaces it cannot open), and errors the XSD reader of `internal/xsd`
only meets in malformed schemas.

Fuzzing complements coverage for the decoders: 26 fuzz targets run in CI
(`make fuzz FUZZTIME=10s`) and locally for longer.
