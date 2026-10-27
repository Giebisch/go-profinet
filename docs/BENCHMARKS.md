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

## The Linux transport (AF_PACKET)

`scripts/l2-bench.sh` runs `internal/cmd/l2bench` on a veth pair in a
Linux container (CAP_NET_RAW, CAP_NET_ADMIN) and measures `l2/afpacket`
three ways:

- **flood**: 200 000 frames from one end, read at the other: frames per
  second, losses, CPU time and heap allocations per frame;
- **ping-pong**: 20 000 round trips (write, echo, read) on raw ports;
- **cyclic**: an IO-Controller and an IO-Device at a 1 ms cycle (SendClock
  32, ReductionRatio 1) for 30 s. Separate raw sockets with kernel receive
  timestamps (SO_TIMESTAMPNS) record every cyclic frame, so the intervals
  are measured outside the code under test; the cycle counter shows missing
  frames, the controller's statistics reconnects and watchdog expiries,
  `getrusage` and `runtime.MemStats` CPU, allocations and garbage
  collections.

Before and after making the backend allocation-free (go-profinet v0.4.0:
ReadFrame reuses its buffers and receives with a raw recvmsg, WriteFrame
writes on the bound socket, LinkUp reads the flags with an ioctl), on
2026-10-27, Docker Desktop on an Apple M1 Max (linuxkit 7.0.14, arm64,
10 CPUs, Go 1.23): median of three alternating runs, with the range.

| Measurement | before | after |
|---|---|---|
| flood: frames per second | 80 738 (48 175–83 386) | 927 041 (913 410–939 874) |
| flood: frames lost of 200 000 | 14 162 (8 974–81 952) | 0 (0–0) |
| flood: CPU per frame | 3.67 µs (3.54–3.78) | 2.17 µs (2.13–2.20) |
| flood: allocations per frame | 15.1 (10.7–15.4) | 0 |
| ping-pong round trip p50 | 6.1 µs (6.0–14.0) | 6.3 µs (6.2–6.3) |
| ping-pong p99 | 37 µs (33–142) | 50 µs (50–75) |
| ping-pong p99.9 | 141 µs (139–1562) | 78 µs (74–169) |
| ping-pong max | 1223 µs (400–16 684) | 254 µs (244–562) |
| cyclic: interval p50 | 1297 µs (1295–1333) | 1297 µs (1250–1306) |
| cyclic: interval p99 | 1652 µs (1632–2434) | 1629 µs (1606–3810) |
| cyclic: interval p99.9 | 1751 µs (1729–7027) | 1705 µs (1676–10 546) |
| cyclic: longest interval | 5.1 ms (2.9–22.8) | 9.6 ms (4.0–34.2) |
| cyclic: frames sent in 30 s (30 000 deadlines) | 23 672 (23 308–24 430) | 23 429 (23 212–23 497) |
| cyclic: deadlines missed by the scheduler | 6073 (3885–6145) | 6048 (6017–6093) |
| cyclic: CPU of the process | 13.8 % (13.0–15.6) | 12.6 % (12.0–14.1) |
| cyclic: allocations per second | 31 085 (30 808–32 950) | 4 739 (4 725–4 751) |
| cyclic: allocated per second | 5.5 MB (5.5–5.8) | 0.30 MB (0.30–0.30) |
| cyclic: garbage collections in 30 s | 35 (35–37) | 1 (1–1) |
| cyclic: GC pauses, total | 4970 µs (4168–8136) | 169 µs (51–197) |
| reconnects, watchdog expiries | 0 | 0 |

What changed reproducibly: throughput, losses, CPU and allocations of the
transport, and the garbage collection of a cyclic exchange (35 collections
in 30 s down to 1). What did not: the intervals of the cyclic frames. In
this virtual machine the hypervisor, not the transport, decides them: the
scheduler misses a fifth of the 1 ms deadlines before and after (a plain
`time.Sleep` loop in the same container is late by 0.7 ms at the median,
and a sleep followed by a busy wait still by 0.2 ms), and the longest
interval varies from run to run by a factor of five. The round-trip p99
rose by about 13 µs in all three runs; its p99.9 and maximum fell.

Investigated and not adopted:

- `PACKET_QDISC_BYPASS`: on a veth (whose queueing discipline is
  `noqueue`) three runs each gave overlapping ranges (flood 851 540–931 568
  frames/s without, 834 633–974 606 with); nothing to keep without a
  physical NIC to measure on.
- `PACKET_RX_RING`/`PACKET_TX_RING` (TPACKET_V3): with the allocation-free
  path the flood runs at 0.93 M frames/s and a 1 ms exchange of a few
  devices needs a few thousand frames per second; a memory-mapped ring
  would add unsafe shared memory and a block timeout (latency) for no
  measured need.
- A busy-wait scheduler (sleep, then spin to the deadline): on macOS it
  cuts missed 1 ms deadlines from 20 to 3 in 5000 with a 100 µs spin, at
  the cost of a spinning core; in the VM it still misses 135 in 5000 with
  a 600 µs spin. Not added: it trades a core for a gain only on bare
  metal, which an application can get from a real-time kernel and CPU
  isolation instead.
- Kernel receive timestamps (SO_TIMESTAMPNS) are what l2bench measures
  with; the library does not use them (PTCP delay measurement is not
  implemented).

Run `scripts/l2-bench.sh -label NAME -json /src/out.json` on the target
machine; compare runs of one machine only.
