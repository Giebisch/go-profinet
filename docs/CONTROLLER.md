# IO-Controller

`controller.Controller` connects to IO-Devices, parameterizes them and
exchanges their cyclic data: what a PLC does, in conformance class A/B.

## Setup

```go
port, _ := osport.Open("eth0", osport.Options{})
mux := l2.NewMux(port)
ctrl, err := controller.New(controller.Config{
	Mux:         mux,
	StationName: "plc-1",       // CMInitiatorStationName
	VendorID: 0xF000, DeviceID: 1, Instance: 1, // the controller's object UUID
	// ListenPacket: nil uses the OS's UDP stack; RPCPort: 34964
})
defer ctrl.Close() // releases every AR
```

The controller listens on UDP 34964 for the devices' ApplicationReady
(`RPCPort`; another port is announced in the ARRPCBlockReq).

## Adding a device

```go
d, err := ctrl.AddDevice(controller.DeviceConfig{
	StationName: "io-rack-1",
	Model:       m,                         // from gsdml.ToModel or written by hand
	IP:          suite, AssignIP: true,     // set the address with DCP if it differs
	ReductionRatio: 4,                      // 4 ms (send clock 1 ms)
	Parameters: []controller.Parameter{{Slot: 2, Subslot: 1, Index: 0x10, Data: []byte{0, 2}}},
	OnState: func(from, to controller.State, err error) { log.Printf("%s → %s %v", from, to, err) },
	OnAlarm: func(a controller.AlarmEvent) { log.Printf("alarm %s slot %d", a.Notification.Type, a.Notification.Slot) },
})
err = d.WaitState(ctx, controller.StateRunning)
```

Each device runs its lifecycle on its own goroutine (see
[ARCHITECTURE.md](../ARCHITECTURE.md#the-controller)):
Resolving (DCP Identify by name, optional Set IP) → Connecting → Parameterizing
(the `Parameters`, PrmEnd) → WaitApplicationReady → Running. A failure in any
state returns to Offline and retries after a backoff (`ReconnectMin` 200 ms,
doubling to `ReconnectMax` 10 s). `d.Close()` releases the AR and removes the
device.

| `DeviceConfig` | Default | |
|----------------|---------|--|
| `SendClockFactor`, `ReductionRatio` | 32, 4 | the cycle: 4 ms |
| `WatchdogFactor`, `DataHoldFactor` | 10, 10 | see [CYCLIC.md](CYCLIC.md#why-the-default-hold-is-10-cycles) |
| `Advanced` | false | advanced startup with RT_CLASS_2 (pnio-rs needs it); legacy with RT_CLASS_1 otherwise |
| `ActivityTimeout` | 10 s | CM initiator activity timeout |
| `RTATimeoutFactor`, `RTARetries` | 5 (500 ms), 3 | alarm transport |
| `RPCTimeout` | 5 s | each RPC call |

## Running

```go
d.SetOutput(2, 1, out, pnio.IOxSGood)
in, iops, ok := d.Input(1, 1)
b, err := d.ReadRecord(ctx, controller.Record{Slot: 1, Subslot: 1, Index: 0x10}, 0)
err = d.WriteRecord(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 3})
im0, err := d.ReadIM0(ctx)
diags, err := d.ReadDiagnosis(ctx)
info, ok := d.AR()     // AR UUID, IOCRs, layout
diff := d.ModuleDiff() // what the device reported as different
st := d.Stats()        // connects, aborts, alarms, provider, consumer, cycle jitter
```

Alarms are acknowledged by the controller before `OnAlarm` is called.
A device whose real configuration differs still runs; the ModuleDiffBlock
says which submodules are wrong, and their IO data has IOPS bad.

## Without a lifecycle: engineering access

```go
rc := controller.NewRecordClient(ctrl.RPC(), &net.UDPAddr{IP: ip, Port: rpc.Port}, pnio.ObjectUUID(inst, devID, vendorID))
im0, err := rc.ReadIM0(ctx, rpc.NilUUID, 0, 1) // ReadImplicit: no AR
sar, err := rc.OpenSupervisor(ctx, controller.SupervisorConfig{InitiatorMAC: mux.HardwareAddr(), InitiatorObject: ctrl.ObjectUUID()})
err = sar.Write(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, data)
err = sar.Release(ctx)
```

A device access AR (IOSAR) gives read and write access without IO data;
devices that do not support it (p-net) reject it. All calls of one AR use
the RPC activity of its Connect.

## Discovery and addressing

`ctrl.DCP()` is the controller's DCP requester ([DCP.md](DCP.md)): Identify,
Set the name and the IP address, Signal, ResetToFactory.
