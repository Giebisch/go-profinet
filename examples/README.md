# Examples

Each example is a small `main` package. The ones marked *self-contained*
run a controller and devices on the virtual Ethernet network (`vnet`) in one
process: they need no privileges and no hardware, and `make examples` runs
them. The outputs below are real, captured on 2026-10-26 (UUIDs, MAC
addresses and timings differ from run to run).

| Example | Shows | Runs |
|---------|-------|------|
| [quickstart](quickstart/main.go) | DCP, Connect, a parameter, cyclic data both ways, I&M0 | self-contained |
| [discovery](discovery/main.go) | DCP Identify, naming and addressing a new device, LLDP neighbors | self-contained |
| [hello](hello/main.go) | a device announcing itself with DCP Hello; the controller connecting through it without Identify | self-contained |
| [alarms](alarms/main.go) | a diagnosis alarm, reading the diagnosis, the alarm when it disappears | self-contained |
| [records](records/main.go) | ReadImplicit (I&M0, real configuration), a device access AR for writes, WriteMultiple and diagnosis in an AR, PNIO errors | self-contained |
| [topology](topology/main.go) | LLDP between devices, PDRealData read with ReadImplicit | self-contained |
| [chaos](chaos/main.go) | an AR under loss, duplication, reordering, corruption and jitter; a pulled cable; reconnection and counters | self-contained |
| [gsdml](gsdml/main.go) | generating the GSDML file of a device model | self-contained |
| [gsdmlimport](gsdmlimport/main.go) | reading and validating a GSDML file, converting it to the expected configuration | self-contained |
| [gsdx](gsdx/main.go) | inspecting an OPC (GSDX) container and checking the GSDML part in it | self-contained |
| [device](device/main.go) | an IO-Device on a real interface | needs raw Ethernet access |
| [controller](controller/main.go) | an IO-Controller on a real interface, connecting the example device | needs raw Ethernet access |

## quickstart

```console
$ go run ./examples/quickstart
AR established: 1365da5e-6aa5-4058-b5c8-1f7633c6c3e7
controller reads "PROFINET", device reads 01 02 03 04
I&M0: order OTF-EXAMPLE-1 serial 0001 software V1.0.0
stats: {Received:1 Stale:0 Malformed:0 WrongSource:0 Expired:0 LastCounter:128}
```

## discovery

```console
$ go run ./examples/discovery
found 02:00:00:00:00:0a "io-1" vendor 0xF000 device 0x0001 192.168.0.10/255.255.255.0 gw 0.0.0.0
found 02:00:00:00:00:0c "" vendor 0xF000 device 0x0001 0.0.0.0/0.0.0.0 gw 0.0.0.0
named 02:00:00:00:00:0c io-3 at 192.168.0.12
found 02:00:00:00:00:0b "io-2" vendor 0xF000 device 0x0001 192.168.0.11/255.255.255.0 gw 0.0.0.0
io-3 now: 02:00:00:00:00:0c "io-3" vendor 0xF000 device 0x0001 192.168.0.12/255.255.255.0 gw 0.0.0.0
LLDP neighbor io-1 port port-001.io-1 (02:00:00:00:00:0a)
LLDP neighbor io-2 port port-001.io-2 (02:00:00:00:00:0b)
LLDP neighbor io-3 port port-001.io-3 (02:00:00:00:00:0c)
```

## hello

```console
$ go run ./examples/hello
first attempt: controller: resolve io-1: dcp: station "io-1": profinet: not found
controller: next attempt in 30 s
device: started, sending DCP Hello
controller: Running 4ms after the device started (connections through a Hello: 1)
```

The time is that of the in-process virtual network; on a real network the
device's startup, the link and the RPC round trips add to it.

## alarms

```console
$ go run ./examples/alarms
alarm Diagnosis from slot 1 subslot 1 (sequence 0)
diagnosis slot 1 subslot 1 channel 2 error type 0x0006
alarm DiagnosisDisappears
```

## records

```console
$ go run ./examples/records
ReadImplicit I&M0: vendor 0xF000 order "OTF-EXAMPLE-1" serial "0001" software V1.0.0
real configuration: slot 0 module 0x00000100, 3 submodule(s)
real configuration: slot 1 module 0x00000010, 1 submodule(s)
real configuration: slot 2 module 0x00000020, 1 submodule(s)
device access AR: wrote I&M1 (now "conveyor" / "hall 2") and record 0x0010 = 00 05, released
WriteMultiple: item 1 OK, item 2 DF 80 B1 00 (write: access: write length error)
diagnosis: slot 1 subslot 1 channel 3 error type 0x0001
read of a missing slot: pnio: read 0/9/0x0001 index 0x0010: DE 80 B2 00 (read: access: invalid slot/subslot)
```

## topology

All devices see each other: the virtual network is one segment that floods
LLDP. Behind a switch each port reports its one neighbor.

```console
$ go run ./examples/topology
io-1 port-001 (link 1): io-2/port-001.io-2 io-3/port-001.io-3
io-3 port-001 (link 1): io-1/port-001.io-1 io-2/port-001.io-2
io-2 port-001 (link 1): io-1/port-001.io-1 io-3/port-001.io-3
```

## chaos

```console
$ go run ./examples/chaos
faults on both endpoints: 5% loss, 2% duplicates, 2% reordered, 1% corrupted, 2ms jitter
  Offline → Resolving
  Resolving → Connecting
  Connecting → Parameterizing
  Parameterizing → WaitApplicationReady
  WaitApplicationReady → Running
inputs through the faults: "CHAOS-OK"
cable pulled for 300 ms
  Running → Offline: controller: io-1: profinet: timeout: input data hold time expired
  Offline → Resolving
  Resolving → Offline: controller: resolve io-1: dcp: station "io-1": profinet: not found
  Offline → Resolving
  Resolving → Connecting
  Connecting → Parameterizing
  Parameterizing → WaitApplicationReady
  WaitApplicationReady → Running
after the cable: "CHAOS-OK"; 2 connects, 1 aborts
current AR: 125 frames sent, 120 received, 3 stale, 0 malformed
injected: 33 dropped, 21 duplicated, 17 reordered, 4 corrupted
  Running → Stopped
```

## gsdml and gsdmlimport

```console
$ go run ./examples/gsdml -o .
wrote GSDML-V2.35-OT_Fabric-Examples-20261026.xml
$ go run ./examples/gsdmlimport
valid (against go-profinet's catalog, not PI's checker)
device: vendor 0xFFFF device 0x0001 "pnio DAP", conformance class A, min device interval 32
DAP 0x00000001 with 3 submodule(s)
slot 1: "in1" (0x00000101) subslot 1: 64 octets in, 0 out, 0 record(s)
slot 2: "in2" (0x00000102) subslot 1: 4 octets in, 0 out, 0 record(s)
slot 3: "out3" (0x00000103) subslot 1: 0 octets in, 64 out, 0 record(s)
slot 4: "out4" (0x00000104) subslot 1: 0 octets in, 4 out, 0 record(s)
4 module type(s) usable
```

## gsdx

```console
$ go run ./examples/gsdx
part /_rels/.rels (application/vnd.openxmlformats-package.relationships+xml, 187 octets)
part /GSDML/GSDML-V2.35-Example.xml (application/xml, 7235 octets)
0 OPC issue(s), 0 signature(s)
GSDML /GSDML/GSDML-V2.35-Example.xml: 0 error(s), 0 warning(s) (go-profinet's catalog, not PI's checker)
```

## device and controller on a real network

These two talk over a real interface (AF_PACKET on Linux, BPF on macOS,
Npcap on Windows) and the operating system's UDP stack. Captured with both
in Linux containers on a Docker bridge network
(`--cap-add NET_RAW --cap-add NET_ADMIN`); the controller was stopped after
eight seconds, so the device's data hold timer ends the AR.

```console
$ sudo go run ./examples/device -i eth0 -name io-1      # on 10.142.3.10
2026/10/26 13:06:09 DEBUG device: AR 4de1c989-5cd2-46af-b18c-eb161a2e257d: None → Parameterizing
2026/10/26 13:06:09 INFO  device: AR 4de1c989-5cd2-46af-b18c-eb161a2e257d (IOCARSingle) established by go-profinet-controller at 10.142.3.2:60805
2026/10/26 13:06:09 DEBUG device: AR 4de1c989-5cd2-46af-b18c-eb161a2e257d: Parameterizing → ApplicationReadyPending
2026/10/26 13:06:09 DEBUG device: AR 4de1c989-5cd2-46af-b18c-eb161a2e257d: ApplicationReadyPending → Data
outputs 00 00 00 00
outputs 01 00 00 01
outputs 02 00 00 02
...
outputs 07 00 00 07
2026/10/26 13:06:17 WARN  device: AR 4de1c989-5cd2-46af-b18c-eb161a2e257d: output data hold time expired
2026/10/26 13:06:17 DEBUG device: AR 4de1c989-5cd2-46af-b18c-eb161a2e257d: Data → Closed
2026/10/26 13:06:17 INFO  device: AR 4de1c989-5cd2-46af-b18c-eb161a2e257d aborted: consumer data hold timer expired
```

```console
$ sudo go run ./examples/controller -i eth0 -device io-1   # on 10.142.3.2
2026/10/26 13:06:09 io-1: Offline → Resolving <nil>
2026/10/26 13:06:09 io-1: Resolving → Connecting <nil>
2026/10/26 13:06:09 io-1: Connecting → Parameterizing <nil>
2026/10/26 13:06:09 io-1: Parameterizing → WaitApplicationReady <nil>
2026/10/26 13:06:09 io-1: WaitApplicationReady → Running <nil>
2026/10/26 13:06:09 INFO  controller: io-1: AR 4de1c989-5cd2-46af-b18c-eb161a2e257d running
inputs 03 03 03 03 03 03 03 03
inputs 04 04 04 04 04 04 04 04
...
inputs 09 09 09 09 09 09 09 09
```

(The controller's debug log lines, which repeat the state changes, are left
out.)
