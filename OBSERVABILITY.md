# Observability

go-profinet reports what it does through a logger and through counters on
every component. It has no metrics dependency: read the counters and export
them with the library of your choice.

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
