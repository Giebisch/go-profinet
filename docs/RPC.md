# DCE/RPC

PROFINET's acyclic services (Connect, Read, Write, Control, Release) are
calls of connectionless DCE/RPC (DCE 1.1, chapter 12) over UDP port 34964.
Package `rpc` implements the protocol; package `pnio` the PNIO interfaces,
NDR bodies and blocks; `controller.RecordClient` the services.

## Client

```go
cli := rpc.NewClient(conn, true, rpc.ClientOptions{Timeout: 5 * time.Second})
r, err := cli.Do(ctx, addr, rpc.Call{Object: obj, Iface: pnio.DeviceInterface, IfaceVersion: 1, OpNum: uint16(pnio.OpRead), Body: body, Idempotent: true})
```

- A call uses an **activity** (a UUID with a sequence number) from a pool of
  `Activities` (default 4); `NewActivity` and `DoActivity` let several calls
  share one, as the calls of a PROFINET AR must (p-net ties the AR to the
  activity of its Connect).
- An unanswered request is retransmitted every `Retransmit` (1 s) with the
  same activity and sequence number, until the call's deadline.
- Requests and responses larger than a packet are **fragmented** (1384
  octets of body per fragment, at most 256 fragments, `MaxMessage` 64 KiB) and
  reassembled; FACKs, WORKING, NOCALL and REJECT are handled.
- **Idempotent** calls are not acknowledged with an ACK packet (p-net does
  not process ACKs); non-idempotent ones are.

## Server

```go
srv := rpc.NewServer(conn, true, rpc.ServerOptions{})
srv.Register(pnio.DeviceInterface, 1, rpc.HandlerFunc(func(ctx context.Context, r *rpc.Request) ([]byte, error) { ... }))
```

- **At most once**: a retransmitted request of an executing call is answered
  WORKING, one of a completed call from the response cache (`ResponseTTL`
  30 s).
- `Workers` (8) handlers run at the same time; `MaxActivities` (64) bound
  the state the server keeps.
- A handler returns a body or a `*rpc.Fault` (FAULT or REJECT with a status).

## PNIO on top

`pnio.AppendNDRRequest`/`DecodeNDRResponse` frame the blocks with the NDR
header (args maximum, length, offset, actual count) and the PNIOStatus.
`controller.RecordClient` builds the IODReadReq/IODWriteReq headers, checks
the responses and returns PNIO errors as `*pnio.StatusError`. The PNIO
object UUID of a device is `pnio.ObjectUUID(instance, deviceID, vendorID)`.
