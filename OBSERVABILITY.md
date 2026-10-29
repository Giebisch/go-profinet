# Observability

go-profinet reports what it does through a logger, a metrics callback
interface and counters on every component. It has no metrics dependency:
export the callbacks and counters with the library of your choice
(Prometheus, OpenTelemetry, expvar).

## Logging

Every component takes an optional `profinet.Logger`:

```go
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}
```

`profinet.NewSlogLogger(handler)` adapts a `log/slog` handler,
`profinet.NewStdLogger(*log.Logger)` the standard logger and
`profinet.NopLogger()` discards. A logger that also implements
`profinet.FieldLogger` (`With`, `DebugKV`, …) or `profinet.ContextLogger`
receives structured fields; `NewSlogFieldLogger` provides one.

| Level | What is logged |
|-------|----------------|
| Debug | state transitions of controller lifecycles and device ARs, dropped or undecodable DCP frames, alarm transport details, release and abort details |
| Info | an AR running (controller) or established, released, aborted or idle-released (device), an IP address assigned with DCP |
| Warn | failures the component recovers from: a lifecycle failure and its retry with the backoff, a rejected Connect, a CM activity timeout, an expired data hold timer, a failed ApplicationReady, a DCP response queue overflow, a failing socket read |

Errors are returned to the caller, not logged at an Error level.

Logging is never on a cyclic hot path: the provider and consumer count,
they do not log per frame.

## Metrics

`controller.Config.Metrics` and `device.Config.Metrics` take a
`profinet.Metrics`; embed `profinet.NopMetrics` and implement the callbacks
you need. They run synchronously on the protocol path and must not block.

| Callback | Controller | Device |
|----------|------------|--------|
| `OnStateChange(station, from, to, err)` | every lifecycle transition of a device (`Offline` … `Running`, `Stopped`), with the error of a failure | every AR transition (`Parameterizing` … `Data`, `Closed`) |
| `OnConnect(station, d, err)` | a Connect answered or failed, with its round trip | a Connect from a controller answered (err: the refusal) |
| `OnAbort(station, err)` | an AR that ended without a release (watchdog, abort by the device, lost AR) | an AR aborted (data hold, CM activity timeout, alarm transport) |
| `OnRecord(station, op, index, d, err)` | `ReadRecord`, `WriteRecord`, `WriteMultiple` (index 0xE040), `ReadIM0`, `ReadDiagnosis` | every Read, ReadImplicit and Write it served |
| `OnAlarm(station, type, high)` | every alarm received | every alarm sent |
| `OnRetry(station, attempt, wait, err)` | the next connection attempt after a failure and its backoff | — |

`station` is the peer: the device's name on a controller, the controller's
on a device. Classify `err` with the sentinels ([ERRORS.md](ERRORS.md)) to
label a counter by result. [examples/observability](examples/observability/main.go)
keeps Prometheus-style counters this way.

## Counters

| Component | Method | Counters |
|-----------|--------|----------|
| `l2.Mux` | `Stats()` | `Received`, `Malformed`, `Unclaimed` (no subscription matched), `Overflow` (a subscription's queue was full), `Sent` |
| `dcp.Client` | `Malformed()` | DCP frames that did not decode |
| `dcp.Responder` | `Stats()` | `Identify`, `Get`, `Set`, `Malformed`, `Ignored` (not for this station, or the 3 s lock of another requester) |
| `lldp.Agent` | `Sent()`, `Malformed()` | LLDP frames sent, frames that did not decode |
| `rpc.Server` | `Stats()` | `Requests`, `Retransmits` (answered from the response cache), `Malformed`, `Rejected`, `Busy` |
| `alarm.Manager` | `Malformed()`, `Unknown()` | RTA PDUs that did not decode, PDUs for no endpoint |
| `alarm.Endpoint` | `Stats()` | `Sent`, `Received`, `Retransmits`, `Duplicates`, `OutOfSequence`, `Acks`, `Nacks`, `Busy` |
| `rt.Provider` | `Stats()` | `Sent`, `SendErrors` |
| `rt.Consumer` | `Stats()` | `Received`, `Stale` (cycle counter did not advance), `Malformed`, `WrongSource`, `Expired` (watchdog), `LastCounter` |
| `rt.Task` | `Stats()` | `Cycles`, `Missed`, `MaxLateness`, `MeanLateness`, `MinInterval`, `MaxInterval`: the scheduling jitter of a provider or watchdog |
| `rt.Dispatcher` | `Unknown()` | cyclic frames for no consumer |
| `controller.Device` | `Stats()` | `Connects`, `Aborts`, `Alarms`, and the `Provider`, `Consumer` and `Cycle` stats of the current AR |
| `netstack.Stack` | `Stats()` | `ReceivedIP`, `SentIP`, `BadChecksum`, `Fragments`, `NoSocket`, `Overflow`, `ARPRequests`, `ARPReplys` |
| `l2/bridge.Bridge` | `Stats(port)` | `Received`, `Sent`, `Local` (delivered to the device), `Forwarded`, `Dropped` (blocked, boundary, full queue), `SendErrs` |
| `mrp.Entity` | `Status()` | role and acting role, state, ring open, blocked ports, `Transitions`, `TestsSent`, `TestsReceived`, `TopologyChanges`, `LinkChanges`, `Forwarded`, `Malformed` (`device.Device.MRPStatus()` on a device) |
| `snmp.Agent` | `Stats()` | the snmp group counters (`InPkts`, `OutPkts`, `InBadVersions`, `InBadCommunityNames`, `InASNParseErrs`, requests by type, `OutTooBigs`, `OutNoSuchNames`, `OutGetResponses`) and `SilentDrops`, `UnsupportedPDUs` |
| `vnet.Endpoint` | `Stats()` | `Sent`, `Received`, `Dropped`, `Duplicated`, `Corrupted`, `Reordered` (injected faults), `Overflow`, `LinkDrops` |

## Events

- `dcp.Policy.Audit` receives an event for every DCP Set block a device
  applied, refused or rate-limited (its requester, block and outcome).
- `controller.DeviceConfig.OnState(from, to, err)` reports every transition
  of a device's lifecycle with the error of a failure;
  `OnAlarm(AlarmEvent)` every alarm (after the controller acknowledged it).
- `device.Handler.StateChanged(ar, from, to)` reports every AR transition on
  the device, `AlarmAck` every acknowledgement, `NameChanged`, `IPChanged`,
  `Signal` and `ResetToFactory` the DCP requests.
- `controller.Device.AR()` and `device.Device.ARs()` describe the current
  ARs; `controller.Device.ModuleDiff()` the differences the device reported.

## Captures

`pcap.NewWriter` writes frames in pcap format; wrap a `l2.Port` to record
what a component sends and receives (the interop suite and profinetctl's
`--pcap` do), and open the file with Wireshark, whose PROFINET dissectors
decode DCP, RT, alarms and the PNIO blocks.
