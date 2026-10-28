# Architecture

How go-profinet is put together: the packages and their dependencies, the
goroutines each component runs, the protocol state machines and how a frame
travels from the wire to the application.

## Layers

```text
            ┌───────────────────────────── application ─────────────────────────────┐
            │  controller (IO-Controller)            device (IO-Device)             │
            │  model (device model) ◄──────────────► gsdml (GSDML ⇄ model)          │
            └──────┬───────────┬──────────┬──────────────┬─────────────┬────────────┘
                   │           │          │              │             │
                  dcp        lldp      alarm (RTA)     rt (cyclic)    rpc + pnio (acyclic)
                   │           │          │              │             │
                   └───────────┴────┬─────┴──────────────┘        net.PacketConn (UDP 34964)
                                    │                              ├── OS UDP stack
                                 l2.Mux  (one reader, subscriptions by EtherType/FrameID)
                                    │                              └── netstack (ARP/IPv4/UDP over l2.Mux)
                                 l2.Port
                ┌──────────────┬────┴────────┬──────────────┐
            l2/afpacket      l2/bpf       l2/npcap         vnet (virtual switch, faults)
             (Linux)      (macOS/BSD)    (Windows)          (tests, examples)
```

Beside these layers: **`l2/bridge`**, the learning switch of a multiport
device (its ports are `l2.Port`s, the device sits on its local port);
**`mrp`**, the ring entity on two of its ports; **`snmp`**, the read-only
agent on UDP 161; **`store`**, where a device keeps its permanent state.

- **`profinet`** (the root package) is a leaf: the sentinel errors and the
  logger interfaces. Every package imports it; nothing else is shared.
- **`ethernet`** encodes and decodes Ethernet II frames with an optional
  802.1Q tag. **`l2`** defines `Port` (read and write whole frames) and `Mux`,
  the one reader of a port that hands frames to subscriptions by a matcher
  (EtherType, FrameID range) through bounded queues. Nothing above `l2` reads
  a port.
- **Layer 2 protocols** (`dcp`, `lldp`, `alarm`, `rt`) each subscribe to the
  mux. **Acyclic services** run over DCE/RPC (`rpc`) with the PNIO block
  codecs of `pnio`, over any `net.PacketConn`: the OS's UDP stack on a real
  network, or `netstack`, which carries UDP in Ethernet frames on the same mux,
  so that a whole network of controllers and devices runs in one process on
  `vnet`.
- **`controller`** and **`device`** compose these into the two roles;
  **`model`** describes a device once for both, and **`gsdml`** converts
  between models and GSDML files.

## Goroutines

Every component starts its goroutines in its constructor and stops them in
`Close`, which waits for them. Queues are bounded; a full queue drops and
counts.

| Component | Goroutines |
|-----------|------------|
| `l2.Mux` | one reader of the port |
| `dcp.Client` | one reader of its subscription (Identify responses, Get/Set responses by transaction ID, Hellos) |
| `dcp.Responder` | one reader; Identify responses are delayed by the ResponseDelayFactor on timers |
| `lldp.Agent` | one reader (neighbor table, TTL expiry) and one transmit ticker |
| `rpc.Client` | one reader of its socket; calls wait on a bounded pool of activities (or on their own `rpc.Activity`) |
| `rpc.Server` | one reader; complete requests run on at most `Workers` handler goroutines; a response cache answers retransmissions |
| `alarm.Manager` | one reader that routes RTA PDUs to endpoints by reference and priority; each `Endpoint` retransmits on a timer |
| `rt.Dispatcher` | one reader that routes cyclic frames to consumers by FrameID |
| `rt.Scheduler` | one goroutine that runs every periodic task (provider transmissions, consumer watchdogs) on deadlines `start + n × period` |
| `netstack.Stack` | one reader (ARP, IPv4, UDP to sockets) |
| `l2/bridge.Bridge` | one reader per physical port; the CPU port and each port's local port are queues the device's muxes read |
| `mrp.Entity` | one reader per ring port, one loop (link polling, the manager's test rounds) and a short-lived sender per burst of link change frames |
| `snmp.Agent` | one reader of its socket (opt-in) |
| `controller.Controller` | its DCP client, RPC client and server, alarm manager, dispatcher and scheduler, and **one lifecycle goroutine per device** |
| `device.Device` | its DCP responder, an LLDP agent per port, RPC client and server, alarm manager, dispatcher and scheduler, a link watcher and the PDev evaluator, the MRP entity and SNMP agent when configured; per AR, a watchdog and the ApplicationReady call; per shared-device handover, the plug sequence |

Application callbacks (`controller.DeviceConfig.OnState`, `OnAlarm`, the
`device.Handler` methods) run on these goroutines and must return promptly.

## The controller

A `controller.Device` runs this lifecycle on its own goroutine; a failure of
one device does not affect the others.

```text
Offline ──► Resolving ──► Connecting ──► Parameterizing ──► WaitApplicationReady ──► Running
   ▲            │              │                │                     │                 │
   └────────────┴──────────────┴────────────────┴─────────────────────┴─────────────────┘
                 any failure (DCP not found, Connect refused, watchdog, abort, ...): Offline,
                 then a retry after a backoff (ReconnectMin doubling to ReconnectMax)
Close: ──► Stopped (the AR is released)
```

1. **Resolving**: DCP Identify by station name; with `AssignIP`, DCP Set of
   the IP suite.
2. **Connecting**: a Connect with the ARBlockReq, the input and output
   IOCRBlockReqs (the controller assigns the input FrameID, the device the
   output one), the AlarmCRBlockReq and the ExpectedSubmoduleBlockReqs of the
   model. The ModuleDiffBlock of the response is kept. Cyclic transmission
   and the alarm endpoints start.
3. **Parameterizing**: the `Parameters` are written, then PrmEnd (Control).
4. **WaitApplicationReady**: the device calls the controller's RPC server
   with ApplicationReady; the controller acknowledges it. The consumer
   watchdog is armed now (p-net starts its provider only after PrmEnd).
5. **Running**: cyclic data both ways, alarms acknowledged
   (`OnAlarm`), records through `ReadRecord`/`WriteRecord`.

Every call of one AR uses the RPC activity of its Connect (`rpc.Activity`):
p-net ties the AR to it. PNIO calls are idempotent and are not acknowledged
with ACK packets, which p-net does not process.

## The device

Each application relationship has an explicit state machine
(`device.ARState`, `device.CanTransition`):

```text
IOCAR:  Connect ──► Parameterizing ──PrmEnd──► AppReadyPending ──ApplicationReady confirmed──► Data
                          ▲                                                                     │
                          └────────────────────────── PrmBegin ─────────────────────────────────┘
IOSAR (device access):  Connect ──► Data
every state ──► Closed   (Release, abort by either side, watchdog, CM activity timeout, DCP reset)
```

The device validates a Connect block by block and answers a fault with the
PNIO status of the faulty field (ErrorCode1/ErrorCode2 per IEC 61158-6-10).
It compares the expected submodules with its model and reports differences
in the ModuleDiffBlock; IO data of a wrong submodule is sent with IOPS bad.
Records of the model are validated and stored by the device; other user
records go to the `Handler`. A device serves several ARs (`MaxARs`), one
controller AR per submodule.

## Cyclic data

An IOCR's C_SDU is laid out by `rt.ComputeLayout` (controller) and checked
by `rt.LayoutFromIOCR` (device): the data objects of the submodules in
order, each followed by its IOPS, then the IOCS octets of the other
direction. A provider task encodes the current values into a frame every
`SendClockFactor × 31.25 µs × ReductionRatio` and sends it with 802.1Q
priority 6; the consumer accepts a frame from the expected source whose
cycle counter advanced, stores the data and resets the watchdog
(`WatchdogFactor` cycles; `DataHoldFactor` for the data). An expired
watchdog aborts the AR. See [docs/CYCLIC.md](docs/CYCLIC.md) for the
measured timing.

## Alarms

Per AR and priority, an `alarm.Endpoint` is the sender (APMS) and receiver
(APMR) of the RTA protocol: DATA PDUs numbered from 0xFFFF with a 15-bit
wrap, acknowledged by ACK PDUs (or by the acknowledgement number of the next
PDU), retransmitted after `RTATimeoutFactor × 100 ms` up to `RTARetries`
times, with a window of one. An ERR PDU aborts the AR. Above the transport,
an alarm is an AlarmNotification block and is confirmed by an AlarmAck
block.

## Errors, limits and input validation

- Every decoder bounds every count and length before it allocates; network
  input that does not decode is dropped and counted (`Malformed` counters),
  and every decoder has a fuzz target.
- Errors wrap the sentinels of the root package; typed errors carry the
  protocol's codes (`*pnio.StatusError`, `*dcp.SetError`, `*rpc.Fault`).
  See [ERRORS.md](ERRORS.md).
- Limits: RPC messages (64 KiB, 256 fragments), activities and workers of
  the RPC server, ARs of a device, subscriptions' queues, GSDML documents
  (64 MiB), record lengths (65535).

## Code generation

The GSDML document types (`gsdml/zz_generated.go`) and `gsdml/SCHEMA.md` are
generated by `internal/cmd/gengsdml` from the independently written catalog
`gsdml/schema/catalog.json`; `make generate-check` fails when they are out of
date. See [docs/CODEGEN.md](docs/CODEGEN.md).
