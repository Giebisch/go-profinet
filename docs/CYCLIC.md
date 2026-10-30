# Cyclic data

How go-profinet exchanges cyclic IO data (RT_CLASS_1 and unsynchronized
RT_CLASS_2), how to configure the timing, and what timing it achieves.

## The frame

A cyclic frame is an Ethernet frame with EtherType 0x8892, an 802.1Q tag of
priority 6, and:

| Field | Octets | Meaning |
|-------|-------:|---------|
| FrameID | 2 | identifies the IOCR: 0xC000–0xF7FF (RT_CLASS_1), 0x8000–0xBBFF (RT_CLASS_2) |
| C_SDU | 40–1440 | the IO data objects, each followed by its IOPS, and the IOCS octets |
| CycleCounter | 2 | advances by SendClockFactor × ReductionRatio per frame (units of 31.25 µs) |
| DataStatus | 1 | primary, data valid, provider run, station OK (`rt.DataStatus`) |
| TransferStatus | 1 | 0 |

The controller lays out both IOCRs (`rt.ComputeLayout`): the data objects
in submodule order (inputs and NO_IO submodules in the input CR, outputs in
the output CR), each followed by its IOPS, then the IOCS of the other
direction; the device checks the offsets it receives (`rt.LayoutFromIOCR`).
`controller.Device.AR()` and profinetctl's `iocr` show the layout.

## Timing parameters

| Parameter | `controller.DeviceConfig` | Default | Meaning |
|-----------|---------------------------|---------|---------|
| SendClockFactor | `SendClockFactor` | 32 | the send clock in units of 31.25 µs (32 = 1 ms); 1–128, a power of two |
| ReductionRatio | `ReductionRatio` | 4 | the cycle is SendClockFactor × 31.25 µs × ReductionRatio (default 4 ms); 1–512, a power of two; the cycle is at least the device's MinDeviceInterval |
| WatchdogFactor | `WatchdogFactor` | 10 | the consumer aborts the AR after this many cycles without a frame |
| DataHoldFactor | `DataHoldFactor` | 10 | the consumer's data are held valid for this many cycles |

The provider sends on deadlines `start + n × cycle` of the monotonic clock
(`rt.Scheduler`), so lateness does not accumulate as drift; a run later than
a whole cycle skips the missed deadlines and counts them (`TaskStats.Missed`).
The consumer accepts a frame from the expected MAC whose cycle counter
advanced (`rt.CounterNewer`), and counts stale, malformed and foreign frames.

## Why the default hold is 10 cycles

PLCs commonly use a watchdog and data hold factor of 3. go-profinet runs on
Go timers on a general-purpose operating system, which stalls a process for
10 to 20 ms now and then. Measured on 2026-10-26:

- A plain `time.Sleep(time.Millisecond)` loop on an Apple M1 Max (macOS)
  sees gaps of 4 to 11 ms about every 2 seconds, with no PROFINET involved.
- A controller and a device on `vnet` in one process, for 20 s per setting
  (`ARs lost` counts aborts by the watchdog or data hold on either side):

| Host | Cycle | Factor 3: ARs lost | Factor 10: ARs lost | Mean / max lateness (factor 10) | Longest interval (factor 10) |
|------|------:|-------------------:|--------------------:|----------------------------------|-----------------------------:|
| macOS, M1 Max, native | 1 ms | 23 | 7 | 75 µs / 544 µs | 9.4 ms |
| macOS, M1 Max, native | 2 ms | 11 | 1 | 164 µs / 1.1 ms | 8.6 ms |
| macOS, M1 Max, native | 4 ms | 3 | **0** | 393 µs / 4.0 ms | 21.8 ms |
| Linux arm64 (Docker Desktop VM) | 1 ms | 27 | 6 | 476 µs / 997 µs | 1.9 ms |
| Linux arm64 (Docker Desktop VM) | 2 ms | 10 | 2 | 518 µs / 2.0 ms | 10.9 ms |
| Linux arm64 (Docker Desktop VM) | 4 ms | 5 | **0** | 812 µs / 3.9 ms | 17.3 ms |

Over a real veth pair with the AF_PACKET backend (scripts/l2-bench.sh,
2026-10-27, Docker Desktop, linuxkit 7.0.14 arm64), a 1 ms cycle for 30 s:
the scheduler missed about 6000 of 30 000 deadlines (it skips a deadline it
is a whole cycle late for), the frames left at a median interval of 1.3 ms,
the 99.9th percentile was 1.7 ms and the longest 4 to 34 ms depending on
the run; no AR was lost with a hold of 1000 cycles. The hypervisor causes
most of it: a bare `time.Sleep(1 ms)` loop in the same container wakes
0.7 ms late at the median. See [BENCHMARKS.md](BENCHMARKS.md).

Against rt-labs p-net over AF_PACKET in Docker (the interop suite,
`TestPnetStartupModes`), a 1 ms cycle with a factor of 10 ran without an
abort for its 3 s: 2946 cycles, 56 missed, mean lateness 748 µs.

So, on a general-purpose OS:

- **Use cycles of 4 ms or more with factors of 10 or more** (the defaults).
  1 ms and 2 ms cycles work, but lose the AR now and then even with a factor
  of 10; raise the factors (30 cycles at 1 ms) if you need them.
- The vnet figures are pessimistic: both stacks share one process, and
  every frame passes three goroutines of the virtual switch.
- A real-time kernel (PREEMPT_RT), an isolated CPU and `GOMAXPROCS` tuning
  reduce the stalls; go-profinet does not lock threads or set priorities.
- Functional correctness on a virtual network does not establish real-time
  behaviour on hardware. Measure with `controller.Device.Stats().Cycle`
  (or profinetctl `io statistics`) on the target.

## Process data API

```go
// Controller
d.SetOutput(2, 1, []byte{1, 2, 3, 4}, pnio.IOxSGood) // next frames carry it
in, iops, ok := d.Input(1, 1)                       // last frame's data
iocs, ok := d.ConsumerStatus(2, 1)                  // the device's consumer status of our outputs

// Device
dev.SetInput(1, 1, data, pnio.IOxSGood)
out, iops, ok := dev.Output(2, 1)
```

Outputs not set are sent as zeros with IOPS bad, which tells the device to
keep them in their safe state. Data is copied in and out; the cyclic
goroutines never call the application.
