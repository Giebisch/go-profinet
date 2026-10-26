# Benchmarks

`make bench` runs the codec and cyclic benchmarks. Measured on 2026-10-26 on
an Apple M1 Max (darwin/arm64), Go 1.27, `-benchmem`, one run each:

| Package | Benchmark | Time | Throughput | Allocations |
|---------|-----------|-----:|-----------:|------------:|
| ethernet | Encode (into a buffer) | 6.4 ns | 9.4 GB/s | 0 |
| ethernet | Encode (allocating) | 139 ns | | 1 (1024 B) |
| ethernet | Decode | 9.4 ns | 6.4 GB/s | 0 |
| dcp | Decode (Identify response) | 132 ns | 924 MB/s | 4 (480 B) |
| dcp | AppendTo | 33 ns | | 0 |
| lldp | Decode | 208 ns | 760 MB/s | 13 (224 B) |
| lldp | AppendTo | 140 ns | | 7 (96 B) |
| rpc | Header append | 26 ns | | 0 |
| rpc | Header decode | 33 ns | | 0 |
| rpc | Loopback call (client and server over UDP on 127.0.0.1) | 42 µs | | 38 (4.3 KB) |
| pnio | ConnectRequest append | 72 ns | 4.9 GB/s | 0 |
| pnio | ConnectRequest decode | 1.0 µs | 349 MB/s | 31 (1.6 KB) |
| pnio | ReadReq decode | 28 ns | 2.3 GB/s | 0 |
| rt | Provider tick (encode and send a cyclic frame) | 24 ns | | 0 |
| rt | Consumer handle (check and store a cyclic frame) | 22 ns | | 0 |
| rt | ComputeLayout | 386 ns | | 11 (1.1 KB) |
| alarm | AppendPayload (an RTA PDU) | 3.5 ns | | 0 |
| alarm | Decode | 4.7 ns | | 0 |
| gsdml | Parse (pnio-rs's sample file) | 512 µs | 42 MB/s | 5839 (223 KB) |
| l2 | Mux dispatch | 719 ns | 1.4 M frames/s | 3 (176 B) |
| vnet | Delivery | 128 ns | 469 MB/s | 2 (128 B) |
| vnet | Broadcast | 713 ns | | 9 (576 B) |

What they mean for an application:

- The cyclic path (provider tick, consumer handle) costs tens of
  nanoseconds and allocates nothing: a controller with a hundred devices at
  1 ms spends well under 1 % of a core on encoding and decoding. The limit
  is the operating system's timer and network latency, not the codec (see
  [CYCLIC.md](CYCLIC.md)).
- Acyclic calls are dominated by the network round trip (42 µs on the
  loopback interface).
- GSDML parsing is a one-off per file.

The numbers vary by machine; run `make bench` on the target.
