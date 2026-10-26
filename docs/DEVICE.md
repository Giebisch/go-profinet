# IO-Device

`device.Device` turns a Go program into a PROFINET IO-Device that PLCs and
engineering tools find, configure and exchange data with.

## Describing the device

```go
m := &model.Device{
	Identity: model.Identity{VendorID: 0xF000, DeviceID: 0x0001, VendorName: "ACME", TypeOfStation: "ACME IO",
		OrderID: "ACME-IO-1", SerialNumber: "0001", HardwareRevision: 1,
		SoftwareRevision: pnio.SoftwareRevision{Prefix: 'V', FunctionalEnhance: 1}},
	Capabilities: model.Capabilities{MaxARs: 2, MinDeviceInterval: 32, ConformanceClass: "B", Ports: 1, LLDP: true},
	DAP:          model.StandardDAP("ACME DAP", 0x00000100, 1), // identity, interface and one port
	Slots: []model.Slot{
		{Number: 1, Module: model.Module{Name: "8 in", IdentNumber: 0x10,
			Submodules: []model.Submodule{{Name: "Inputs", IdentNumber: 0x11, Subslot: 1, InputLen: 8}}}},
		{Number: 2, Module: model.Module{Name: "4 out", IdentNumber: 0x20,
			Submodules: []model.Submodule{{Name: "Outputs", IdentNumber: 0x21, Subslot: 1, OutputLen: 4,
				Records: []model.Record{{Index: 0x10, Name: "Mode", Length: 2, Default: []byte{0, 1}}}}}}},
	},
}
```

The same model generates the device's GSDML file (`gsdml.FromModel`) for the
engineering tool, and is what a go-profinet controller expects.

## Starting

```go
dev, err := device.New(device.Config{
	Model: m, Mux: mux, StationName: "acme-io-1", IP: suite,
	ApplyIP: func(s dcp.IPSuite, permanent bool) error { return configureOS(s) },
	Handler: &app{},             // embed device.NopHandler
	IM:      device.IMData{IM1: pnio.IM1{TagFunction: "pump", TagLocation: "hall 1"}},
})
defer dev.Close()
```

On a real interface the device uses the OS's UDP stack; a DCP Set IP is
passed to `ApplyIP`, which must configure the interface (go-profinet does
not change the OS's addresses).

## Process data, records and events

```go
dev.SetInput(1, 1, data, pnio.IOxSGood)  // every AR's next frames carry it
out, iops, ok := dev.Output(2, 1)        // what the controller last sent
```

The `Handler` decides what the device leaves to the application:

| Method | Called for |
|--------|------------|
| `ReadRecord`, `WriteRecord` | user records without a `model.Record`, unknown indices; return `device.RecordError(code1, code2)` to refuse. Writes of model records are validated and stored by the device and then reported |
| `PrmEnd` | the end of parameterization; return nil when the application is ready (the device then sends ApplicationReady), an error to abort |
| `StateChanged`, `AlarmAck` | AR transitions, alarm acknowledgements |
| `Signal`, `ResetToFactory`, `NameChanged`, `IPChanged` | DCP requests; persist the name and address here if they must survive a restart |

## Alarms and diagnosis

```go
err := dev.SendAlarm(ctx, device.Alarm{Type: pnio.AlarmProcess, Slot: 1, Subslot: 1, HasItem: true, USI: 0x10, Data: []byte{1}})
err = dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: 1, ErrorType: pnio.ChannelErrLineBreak})
```

See [DIAGNOSTICS.md](DIAGNOSTICS.md).

## ARs

The device accepts IOCARs (one per controller, one owner per submodule) and
IOSARs (device access, unless `RejectSupervisorAR`), up to `MaxARs`. It
validates a Connect block by block and answers faults with the PNIO status of
the faulty field; it reports differences from its model in the
ModuleDiffBlock and sends their IO data with IOPS bad. `dev.ARs()` describes
the ARs. A watchdog or data hold expiry, a Release, an abort by the
controller, a CM activity timeout or a DCP reset closes an AR.
