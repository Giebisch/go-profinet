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

## Reset modes

| Constant | Resets |
|----------|--------|
| `ResetApplicationData` | the application's parameters |
| `ResetCommunication` | the communication parameters: name and IP address |
| `ResetEngineering` | the engineering data |
| `ResetAllData` | all stored data |
| `ResetDevice` | the device, keeping its firmware |
| `ResetAndRestoreDevice` | the device to its factory state |
