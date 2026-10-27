# DCP

The Discovery and Configuration Protocol finds PROFINET devices and sets
their station names and IP addresses, on Layer 2 (EtherType 0x8892, FrameIDs
0xFEFC–0xFEFF).

## Requester

```go
c, err := dcp.NewClient(mux, dcp.ClientOptions{})       // or controller.DCP()
devices, err := c.Identify(ctx, dcp.FilterAll(), dcp.IdentifyOptions{Wait: 2 * time.Second, ResponseDelay: 10})
id, err := c.IdentifyName(ctx, "io-rack-1", time.Second) // profinet.ErrNotFound when none answers
id, err = c.GetIdentity(ctx, mac)                        // the identity blocks of one device
err = c.SetName(ctx, mac, "io-rack-1", true)              // permanent
err = c.SetIP(ctx, mac, dcp.IPSuite{Addr: ip, Mask: mask, Gateway: gw}, false)
err = c.Signal(ctx, mac)                                  // flash the LED
err = c.ResetToFactory(ctx, mac, dcp.ResetCommunication)
for hello := range c.Hellos(ctx, 16) { ... }
```

- **Identify** is multicast to 01:0E:CF:00:00:00. Every matching device
  answers after a random delay within `ResponseDelay × 10 ms`; the requester
  collects the answers for `Wait` and reports each device once. Filters:
  `FilterAll`, `FilterName`, `FilterAlias` (the AliasName a device derives
  from its LLDP neighbor, for devices without a name), `FilterIP`,
  `FilterDeviceID`.
- **Get** and **Set** are unicast, with a transaction ID, retransmitted
  `Retries` times (default 3) after `Timeout` (default 1 s). `GetIdentity`
  asks for the identity blocks by name: p-net does not answer the
  AllSelector for Get.
- A Set block the device refuses is returned as a `*dcp.SetError` with the
  block and its `BlockError`.
- Station names are validated first (`dcp.ValidateStationName`: at most 240
  octets of DNS labels of 1–63 lowercase letters, digits and hyphens, not
  beginning or ending with a hyphen nor with `xn-`; not of the form of an IP
  address; not beginning with a port name such as `port-001`).

Devices ignore other requesters for 3 s after a Get or Set (p-net does);
go-profinet's retries outlast that.

## Responder

`dcp.NewResponder(mux, station, opts)` answers for a `dcp.Station` (its
identity, `SetName`, `SetIP`, `Signal`, `ResetToFactory`). The IO-Device uses
it; an application rarely needs it directly. It spreads its Identify
answers over the requested delay, answers Get with the requested blocks,
applies Set blocks in order (with Control/Start and Control/Stop around
them), and refuses other requesters for 3 s after a Get or Set.

## Hello

A device can announce itself with a DCP Hello request (FrameID 0xFEFC,
multicast to 01:0E:CF:00:00:01, no response): NameOfStation, IP parameter,
DeviceID, DeviceRole and DeviceInitiative, each with its BlockInfo. A
controller that knows the name connects with the MAC and IP address from the
Hello, without an Identify.

```go
r.StartHello(dcp.HelloOptions{Count: 3, Interval: 30 * time.Millisecond}) // a Responder
r.StopHello()
frame := dcp.HelloFrame(&identity, xid) // the request itself
```

- A sequence is `Count` Hellos (1–15, default 3) `Interval` apart (at least
  10 ms, default 30 ms), the first after `Delay`. A new sequence replaces the
  running one, and two Hellos of a responder are never closer than the
  interval, whatever restarts sequences: a flapping link cannot cause a
  storm.
- No Hello goes out while the station has no name or no IP address.
- A DCP Set ends the sequence (a tool is configuring the device), as p-net
  does; the IO-Device also ends it on a Connect.
- The defaults (3 Hellos, 30 ms) are those vendor documents state; p-net
  sends 3 Hellos 3 s apart. The modes of `dcp.HelloMode` are the values of
  FSHelloMode: off, on link up, on link up after a delay.
- The device reports `DeviceInitiative` = 1 (Hello after power-on) in its
  Identify and Get responses when Hello is on.

This is DCP Hello, not the whole of Fast Startup: Fast Startup also fixes
the port's MAU type with autonegotiation off and stores parameters in
non-volatile memory (FSParameterBlock), which a library on a
general-purpose OS does not control. Startup times on the virtual network
are measured by the e2e tests (`TestHelloDiscovery` logs them); they are no
promise for a real network. Evidence and sources:
[research/EVIDENCE-DCP-PDEV.md](research/EVIDENCE-DCP-PDEV.md).

## Reset modes

| Constant | Resets |
|----------|--------|
| `ResetApplicationData` | the application's parameters |
| `ResetCommunication` | the communication parameters: name and IP address |
| `ResetEngineering` | the engineering data |
| `ResetAllData` | all stored data |
| `ResetDevice` | the device, keeping its firmware |
| `ResetAndRestoreDevice` | the device to its factory state |
