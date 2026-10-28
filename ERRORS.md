# Errors

Every error go-profinet returns can be classified with `errors.Is` against
the sentinels of the root package. Typed errors carry the protocol's codes
and match the sentinels too, so that code can branch on the class and still
report the exact status.

## Sentinels

| Sentinel | Meaning |
|----------|---------|
| `profinet.ErrClosed` | the controller, device, client, port or mux was closed by the application |
| `profinet.ErrTimeout` | the peer did not answer in time: a DCP request, an RPC call, an alarm acknowledgement, a consumer watchdog |
| `profinet.ErrMalformed` | input that cannot be decoded: a truncated or inconsistent frame, block or document |
| `profinet.ErrUnsupported` | a service, option, block type or feature that is not provided (here or by the peer) |
| `profinet.ErrInvalidConfig` | a configuration value a component cannot be built or used with |
| `profinet.ErrInvalidState` | an operation the current state does not allow (a record write without an established AR, an alarm without an AR in Data) |
| `profinet.ErrRejected` | the peer answered with an error: a PNIO status other than OK, a DCP block error, an RPC fault or reject |
| `profinet.ErrAborted` | an AR ended without a release: watchdog, abort by the peer, CM activity timeout, link down |
| `profinet.ErrNotFound` | no device answered for a station name or MAC address, or a slot, subslot, index or AR does not exist |
| `profinet.ErrResourceExhausted` | a bounded resource is used up: ARs, queues, reassembly buffers, concurrent requests |
| `profinet.ErrLinkDown` | the Ethernet link of a port is down |

Errors returned by a context (`context.Canceled`, `context.DeadlineExceeded`)
are wrapped as they are; a call bounded by its own default timeout returns an
error that matches both `ErrTimeout` and `context.DeadlineExceeded`.

## Retries and backoff

What the library retries by itself, and what it leaves to the application:

| Where | Retry | Configured by |
|-------|-------|---------------|
| DCP Get and Set | retransmitted with the same transaction ID (default 3 times) | `dcp.ClientOptions.Retries` |
| DCE/RPC calls | retransmitted every second until the call's deadline (a fragment that is missing is asked for again); a device executes a call at most once and answers a retransmission from its response cache | `rpc.ClientOptions.Retransmit`, `controller.DeviceConfig.RPCTimeout`, the context |
| Alarms (RTA) | retransmitted until acknowledged, then the AR is aborted | `RTATimeoutFactor`, `RTARetries` |
| Controller lifecycle | a failed AR or connection attempt is retried for ever with an exponential backoff and optional jitter; a DCP Hello of the device skips the wait | `ReconnectMin`, `ReconnectMax`, `ReconnectJitter`; `Metrics.OnRetry` |
| Record calls (`ReadRecord`, …) | not retried: a write must not be repeated behind the application's back | the application |

## Typed errors

| Type | Carries | Matches |
|------|---------|---------|
| `*pnio.StatusError` | `Op` and the PNIOStatus (`Code`, `Decode`, `Code1`, `Code2`), as a peer answered it or a decoder produced it | `ErrRejected`; by class `ErrNotFound` (invalid index, slot or area; unknown AR UUID), `ErrUnsupported` (feature not supported, unknown blocks), `ErrInvalidState` (state conflicts), `ErrResourceExhausted` (out of ARs, busy), `ErrAborted` (RTA aborts), `ErrMalformed` (a faulty block field in a Connect or Control) |
| `*dcp.SetError` | the MAC, the block and the `BlockError` of a DCP Set or Get the device refused | `ErrRejected`; `ErrUnsupported` for option and suboption unsupported |
| `*rpc.Fault` | a DCE/RPC FAULT or REJECT and its status | `ErrRejected`; `ErrUnsupported` (unknown interface, operation out of range, unsupported type), `ErrResourceExhausted` (server too busy) |
| `*gsdml.ValidationError` | the `Issues` of a document (path and message) | `ErrMalformed` |
| `dcp.ErrInvalidName` | a station name that is not a valid NameOfStation | `ErrInvalidConfig` |

```go
_, err := d.ReadRecord(ctx, controller.Record{Slot: 1, Subslot: 1, Index: 0x10}, 0)
var se *pnio.StatusError
switch {
case errors.As(err, &se) && errors.Is(err, profinet.ErrNotFound):
	log.Printf("no such record: %s", se.Status) // DE 80 B0 00 (read: access: invalid index)
case errors.Is(err, profinet.ErrTimeout):
	log.Print("no answer")
case errors.Is(err, profinet.ErrInvalidState):
	log.Print("the AR is not established")
}
```

## PNIO status

A `pnio.Status` prints as its four octets and their meaning, for example
`DE 80 B0 00 (read: access: invalid index)`. `Status.OK()` is true for
`00 00 00 00`. `pnio.RWStatus`, `pnio.ServiceStatus` and `pnio.AbortStatus`
build statuses; an IO-Device application answers a record request with
`device.RecordError(code1, code2)`.

The device answers a faulty Connect with the status of the faulty field:
ErrorCode 0xDB (connect), ErrorDecode 0x81 (PNIO), ErrorCode1 the block
(0x01 ARBlockReq, 0x02 IOCRBlockReq, 0x03 ExpectedSubmoduleBlockReq,
0x04 AlarmCRBlockReq, …) and ErrorCode2 the field, as IEC 61158-6-10 lists
them; a CMRPC error (ErrorCode1 0x40) for request-level faults (missing
IOCR, out of ARs, unknown AR UUID, state conflict).

## Network input

Malformed frames and datagrams never return an error to the application and
never panic: they are dropped and counted (`Malformed` in the stats of the
mux, the DCP client and responder, the LLDP agent, the alarm manager, the RPC
server, the RT consumers). A decoder used directly returns an error matching
`ErrMalformed`.

## Lifecycle errors

`controller.Device.State` returns the error of the last failure of the
lifecycle (resolve, Connect, parameterization, ApplicationReady, abort) next
to the state; `OnState(from, to, err)` reports it as it happens. The
controller retries after the backoff; the error is not fatal to the
controller.
