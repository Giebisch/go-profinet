# Public API

An overview of what each package exports. The authoritative reference is the
Go documentation (`go doc github.com/otfabric/go-profinet/<package>`). Until
v1.0.0 the API may change between minor versions; changes are listed in
[RELEASE.md](RELEASE.md).

## Conventions

- Constructors (`New`, `NewClient`, `NewResponder`, `NewAgent`, ...) start a
  component; `Close` stops it, waits for its goroutines and is safe to call
  twice. Methods of the components are safe for concurrent use.
- Blocking calls take a `context.Context` and return when it is done.
  Defaults that bound a call (timeouts, retries) apply when the context has
  no deadline.
- Options structs use zero values for documented defaults.
- Errors match the sentinels of the root package with `errors.Is`
  ([ERRORS.md](ERRORS.md)). Network input never makes a decoder panic.
- `Append*` functions append an encoding to a byte slice and return it;
  `Decode*` functions decode one unit and report `profinet.ErrMalformed`.
- A `Logger` (`profinet.Logger`) is optional everywhere; nil logs nothing.

## controller — IO-Controller

| Symbol | Purpose |
|--------|---------|
| `New(Config) (*Controller, error)` | start a controller on a `*l2.Mux`; `Config.ListenPacket` opens its UDP sockets (nil: the OS), `RPCPort` its RPC server port (34964), `StationName`, `VendorID`/`DeviceID`/`Instance` (its object UUID), `DCP` options |
| `(*Controller).AddDevice(DeviceConfig) (*Device, error)` | add a device and start its lifecycle |
| `(*Controller).Device(name)`, `Devices()`, `DCP()`, `RPC()`, `ObjectUUID()`, `Close()` | access and shutdown |
| `DeviceConfig` | `StationName`, `Model` (required), `IP`/`AssignIP`/`PermanentIP`, `SendClockFactor`, `ReductionRatio`, `WatchdogFactor`, `DataHoldFactor`, `Advanced` (RT_CLASS_2 advanced startup), `RTClass` (only the startup mode's class; RT_CLASS_UDP and the others are refused with `ErrUnsupported`), `ActivityTimeout`, `RTATimeoutFactor`, `RTARetries`, `Parameters`, `PeerChecks` (the expected neighbor of each port, written before PrmEnd), `SystemRedundancy` (experimental IOCARSR of an S2 AR set; `Device.SetPrimary`, `RedundancyAck`), `RPCTimeout`, `ReconnectMin`/`ReconnectMax`, `OnAlarm`, `OnState` |
| `(*Device).WaitState(ctx, State)`, `State()` | the lifecycle: `StateOffline` … `StateRunning`, `StateStopped` |
| `(*Device).SetOutput(slot, subslot, data, iops)`, `Input(slot, subslot)`, `ConsumerStatus(slot, subslot)` | cyclic process data |
| `(*Device).ReadRecord`, `WriteRecord`, `WriteMultiple`, `ReadIM0`, `ReadDiagnosis` | acyclic services within the AR |
| `(*Device).AR() (ARInfo, bool)` | what Connect negotiated: AR UUID, session key, RT class, alarm references, both IOCRs with FrameID, length, cycle and C_SDU layout |
| `(*Device).ModuleDiff()`, `ARUUID()`, `MAC()`, `IP()`, `Stats()`, `Close()` | |
| `NewRecordClient(*rpc.Client, addr, object) *RecordClient` | the acyclic services of one device without a controller: `Read` (with a nil AR UUID: ReadImplicit), `Write`, `WriteMultiple`, `Control`, `Connect`, `ReadIM0`, `ReadDiagnosis` |
| `(*RecordClient).OpenSupervisor(ctx, SupervisorConfig) (*SupervisorAR, error)` | a device access AR (IOSAR) for an engineering tool: `Read`, `Write`, `WriteMultiple`, `Release` |
| `(*Controller).CheckTopology(ctx, []Link) (TopologyReport, error)` | compare an expected topology (`Link` between `PortRef`s) with the PDRealData of the devices: a verdict per link (`LinkOK`, `LinkMissing`, `LinkWrongPeer`, `LinkUnknownPort`, `LinkUnverified`), unexpected links, unreachable stations |
| `AlarmEvent`, `Record`, `WriteItem`, `Parameter`, `PeerCheck`, `DeviceStats` | |

## device — IO-Device

| Symbol | Purpose |
|--------|---------|
| `New(Config) (*Device, error)` | start a device: `Model` and `Mux` required; `StationName`, `IP`, `ListenPacket`, `ApplyIP` (apply a DCP Set IP to the IP stack), `RPCPort`, `Handler`, `IM` (I&M1–4), `DHCP`/`IPFromDHCP`/`SetDHCP` (the DHCP options DCP reports and a handler for their Set), `DCPPolicy` (read-only DCP, rate limits, audit events), `RejectSupervisorAR`, `DisableLLDP`, `LLDPInterval`, `ApplicationReadyTimeout`, `SNMP` (an opt-in read-only SNMP agent), `SystemRedundancy` (experimental S2 AR sets; `Device.SystemRedundancy` reports them); `Bridge` makes it a multiport device (one port submodule, MAC, LLDP agent and link state per bridge port; `Mux` on the bridge's CPU port) |
| `(*Device).SetInput(slot, subslot, data, iops)`, `Output(slot, subslot)` | process data |
| `(*Device).SendAlarm(ctx, Alarm)` | a process (or other) alarm to every AR that owns the submodule; waits for the AlarmAck |
| `(*Device).AddDiagnosis`, `RemoveDiagnosis`, `Diagnoses` | diagnosis records with diagnosis alarms (appears/disappears) |
| `(*Device).ARs() []ARInfo`, `StationName()`, `IP()`, `DHCP()`, `ObjectUUID()`, `RPCAddr()`, `Close()` | |
| `Handler`, `NopHandler` | what the application decides: user records (`ReadRecord`, `WriteRecord`), `PrmEnd`, `StateChanged`, `AlarmAck`, `Signal`, `ResetToFactory`, `NameChanged`, `IPChanged` |
| `RecordError(code1, code2) error` | a PNIORW error for a Handler to return |
| `ARState`, `CanTransition`, `RecordRequest`, `IMData`, `Alarm` | |

## model — device model

`Device` (`Identity`, `Capabilities`, `DAP`, `Slots`, `Catalog`; `WithSlots` for a shared device), `Module`,
`Submodule` (`InputLen`, `OutputLen`, `Records`), `Record`, `Slot`;
`StandardDAP(name, ident, ports)`; methods `Validate`, `Each`, `Module`,
`Submodule`, `ModuleTypes`, `Expected` (ExpectedSubmoduleBlockReqs),
`RealIdentification`. Models are JSON-serializable.

## gsdml — GSDML documents

| Symbol | Purpose |
|--------|---------|
| `Parse(io.Reader)`, `ParseBytes([]byte) (*Document, error)` | read a document (UTF-8 or ISO-8859-1, at most `MaxDocumentSize`) |
| `Validate(*Document) error` | the errors of `Check`: structural, lexical, reference and IO data length; a `*ValidationError` lists the `Issue`s |
| `Check(*Document) Report` | every issue with its `Severity` (`SeverityError`, `SeverityWarning`, `SeverityUnvalidated`) and `Rule` (`RuleStructure`, `RuleReference`, `RuleIODataLength`, `RuleSlots`, …), the declared `SchemaVersion`; `Report.Count`, `Report.Err` |
| `Encode(io.Writer, *Document)`, `Marshal(*Document)` | write; unknown elements and attributes are kept |
| `ToModel(doc, dapID)`, `FromModel(model, GenerateOptions)` | conversion to and from `model.Device` |
| `FileName(model, version, date)` | the conventional file name |
| `(*Document).Text(id)`, `DataItemSize`, `ParseUint16/32`, `ParseBool`, `ParseValueList`, `FormatValueList` | helpers |
| the generated element types (`ISO15745Profile`, `DeviceAccessPointItem`, `ModuleItem`, `VirtualSubmoduleItem`, …) | see [gsdml/SCHEMA.md](gsdml/SCHEMA.md) |

## gsdml/gsdx — GSDX (OPC) containers

A generic Open Packaging Conventions inspector; no PI-specific rule, no
signature verification ([LIMITATIONS.md](LIMITATIONS.md#gsdx-containers)).

| Symbol | Purpose |
|--------|---------|
| `Open(io.ReaderAt, size, Options)`, `OpenFile(path) (*Package, error)` | read the ZIP within `Options` limits (`MaxParts`, `MaxPartSize`, `MaxTotalSize`, `MaxRatio`); hostile or equivalent names are `ErrMalformed`, limits `ErrResourceExhausted` |
| `Package` | `Parts` (`Name`, resolved `ContentType`, sizes, `CRC32`, `Relationships`), `Defaults`/`Overrides`, package `Relationships` (`ResolvedTarget`), `CoreProperties`, `Signatures`, `Issues` (OPC rules broken) |
| `(*Package).Part`, `ReadPart`, `Unreachable` | look up a part, read its content, parts no relationship reaches |
| `Signature` | `SignedInfoReferences`, `PartReferences` (`Digest`: `DigestMatch`/`DigestMismatch`/`DigestNotChecked`), `SignatureTime`, `Certificates` (subject, issuer, validity, serial); `Verified` is always false, `Reason` says why |
| `(*Package).GSDMLParts`, `GSDML(name)`, `CheckGSDML(name)`, `IsGSDML` | find GSDML parts, read one, parse it and run `gsdml.Check` |
| `(*Package).WriteTo(io.Writer)` | write every entry back unchanged (name, method, time, content) |

## dcp — Discovery and Configuration Protocol

| Symbol | Purpose |
|--------|---------|
| `NewClient(*l2.Mux, ClientOptions) (*Client, error)` | a requester: `Identify(ctx, Filter, IdentifyOptions)`, `IdentifyName`, `Get`, `GetIdentity`, `Set`, `SetName`, `SetIP`, `Signal`, `ResetToFactory`, `Hellos` |
| `FilterAll`, `FilterName`, `FilterAlias`, `FilterIP`, `FilterDeviceID` | Identify filters |
| `NewResponder(*l2.Mux, Station, ResponderOptions) (*Responder, error)` | a responder for a `Station` (the device uses one); `StartHello(HelloOptions)`, `StopHello()` |
| `HelloFrame(*Identity, xid) *Frame` | the DCP Hello request of an identity; `HelloMode`, `HelloOptions` (`Normalize`) |
| `Identity`, `ParseIdentity`, `IPSuite`, `Block`, `BlockID`, `SetBlock`, `SetError`, `BlockError`, `ValidateStationName` | |
| `Policy` (`ReadOnly`, `IdentifyLimit`/`SetLimit` as `RateLimit`, `Sources`, `Audit`) in `ResponderOptions.Policy`; `AuditEvent`, `AuditOutcome`; `ResponderStats.RateLimited`/`Refused` | opt-in DCP restrictions and audit events |
| `DHCPParameter` (`AppendDHCPParameter`, `DecodeDHCPParameter`, `ClientID`), `DHCPClientIDFromMAC`, `DHCPClientIDArbitrary`, `DHCPControlParameter`, the `DHCPOpt*` codes, `Identity.DHCP`/`IPSetByDHCP`/`IPConflict`/`DHCPOption`, `Client.SetDHCP`, `DHCPSetter` | DCP option 3 (DHCP); no DHCP client |
| `Decode`, `Frame`, `AppendIPSuite`, `DecodeIPSuite`, `AppendControlResponse`, `DecodeControlResponse` | codec |

## lldp — Link Layer Discovery Protocol

`NewAgent(*l2.Mux, AgentOptions)` sends the local information (`LocalInfo`:
station name as chassis ID, port name, description, management address)
with the PROFINET TLVs every interval (`SendNow` at once; a negative interval
only listens) and keeps the neighbor table (`Neighbors`, `Alias`). `Decode`, `Frame.AppendTo`,
`TLV`, `Neighbor.Alias` (the AliasName for DCP).

## rpc — connectionless DCE/RPC

`NewClient(net.PacketConn, owned, ClientOptions)` with `Do(ctx, addr, Call)`
and `NewActivity`/`DoActivity` for calls that must share an activity;
`NewServer(net.PacketConn, owned, ServerOptions)` with `Register(iface,
major, Handler)`. `UUID`, `ParseUUID`, `NewRandomUUID`, `Header`, `Fault`,
`Errorf`, `Port` (34964).

## pnio — PNIO blocks

The block codecs (`ConnectRequest`, `ConnectResponse`, `ARBlockReq`,
`IOCRBlockReq`, `AlarmCRBlockReq`, `ExpectedSubmoduleBlockReq`,
`ModuleDiffBlock`, `ControlBlock`, `ReadReq`, `WriteReq`, `AlarmNotification`,
`AlarmAck`, `DiagnosisData`, `IdentificationData`, `IM0`–`IM4`,
`PDPortDataReal`, `PDPortDataCheck`, `PDPortDataAdjust`, `PDInterfaceAdjust`,
`PDInterfaceDataReal`, `PDRealData` with `AppendPDRealDataRecord` and
`DecodePDRealDataRecord`, `AppendMultipleBlockHeader`, …), `ReadBlock`, `SplitBlocks`, NDR (`AppendNDRRequest`,
`DecodeNDRResponse`), `Status` and `StatusError`, `ObjectUUID`, the
identifiers (`Index`, `ARType`, `AlarmType`, `BlockType`, `OpNum`, `RTClass`) and the
interface UUIDs. RT_CLASS_UDP fields (codecs only): `LTIP`, `UDPRTPort`,
`IOCRPropRTClassMask`, `AlarmCRPropTransportUDP`,
`AlarmCRBlockReq.TransportUDP`, `ConnectRequest.UsesUDP`.

## mrp and ptcp

`mrp`: `Frame` (`Test`, `TopologyChange`, `LinkChange`, `Common`, `Option`, other
TLVs), `AppendTo`, `Decode`, `EtherType`, the destination addresses, `PortRole`,
`RingState`, `DefaultDomain`; the ring entity `New(Config)` (`RoleClient`,
`RoleManager`, two ring ports of a `Switch` such as `l2/bridge`, `Profile200`,
`Profile500`), `Entity.Status`, `Close`. `device.Config.MRP` (`MRPConfig`) runs it on
a multiport device, `Device.MRPStatus` reports it, and the records
PDInterfaceMrpDataAdjust/Real/Check and PDPortMrpDataAdjust/Real (`pnio`) configure
and report it; `controller.DeviceConfig.MRP` (`MRPSetting`) writes them.
`ptcp`: `Delay` (`AppendTo`, `Decode`), the delay FrameIDs; no line delay
measurement.

## snmp — read-only SNMPv1/v2c agent

| Symbol | Purpose |
|--------|---------|
| `NewAgent(net.PacketConn, owned, Config) (*Agent, error)` | an agent on a socket the caller opens (UDP `Port` 161): `Community` (required, compared in constant time), `MIB`, `MaxMessageSize`, `MaxRepetitions`, `MaxBulkVarBinds`, `Rate`/`Burst` |
| `(*Agent).Serve(ctx)`, `Close()`, `Handle([]byte) []byte`, `Stats()`, `LocalAddr()` | serve until ctx is done or Close; answer one datagram; the counters of the snmp group |
| `NewMIB()`, `(*MIB).AddScalar`, `AddSubtree` | the tree of objects: non-overlapping subtrees whose providers return their instances per request |
| `(*MIB).AddSystem(func() System)`, `AddInterfaces(func() []Interface)`, `AddSNMPGroup(func() Stats)`, `AddLLDP(func() LLDPLocal, func() []LLDPRemote)` | MIB-II system, interfaces (ifNumber, ifTable) and snmp groups; the LLDP-MIB local data, lldpLocPortTable and lldpRemTable |
| `Message`, `PDU`, `VarBind`, `Value`, `OID`, `Decode`, `(*Message).AppendTo` | the BER codec of v1/v2c messages, for a manager as well (`Integer`, `String`, `Counter32`, `Counter64`, `TimeTicks`, `IPAddress`, … build values) |

`device.Config.SNMP` (`device.SNMPConfig`: `Community`, `Contact`,
`Location`, `Description`, `ObjectID`, `Port`, `Rate`) starts the agent in a
device; nil (the default) runs none.

## rt and alarm — real-time transports

`rt`: `Frame`, `AppendEthernet`, `Layout`/`ComputeLayout`/`LayoutFromIOCR`,
`Provider`, `Consumer`, `Dispatcher`, `Scheduler`/`Task`, `DataStatus`,
`CycleNanos`; the RT_CLASS_UDP payload codec `AppendUDPPayload`, `DecodeUDP`,
`IsUDPFrameID`, `UDPPort`, `MinDataLengthUDP` (no UDP data path). `alarm`: `PDU`, `Manager`, `Endpoint` (`Send`, `SendErr`, the `OnData` and
`OnErr` callbacks), `EndpointStats`. Applications rarely use them directly; the
controller and the device do.

## Transports

| Package | Symbol | Purpose |
|---------|--------|---------|
| `l2` | `Port`, `MulticastPort`, `LinkStater`, `NewMux`, `Mux.Subscribe`, `MatchEtherType`, `MatchFrameIDs`, `JoinMulticast` | the frame interface and demultiplexer |
| `l2/bridge` | `New(Config)`, `Bridge.CPU`, `Port(i)`, `Physical(i)`, `SetState(i, Forwarding\|Blocked)`, `SetBoundary`, `Flush`, `Learned`, `Stats`, `LinkLocal` | the learning switch of a multiport device: the interface on the CPU port, link-local and MRP frames on each port's local port |
| `l2/osport` | `Open(name, Options)`, `Interfaces()`, `Backend()` | the raw Ethernet backend of the platform |
| `l2/afpacket`, `l2/bpf`, `l2/npcap` | `Open`, `Port` | the backends themselves (Linux, macOS/BSD, Windows) |
| `vnet` | `New(seed)`, `Network.NewEndpoint`, `Endpoint.SetFaults(Faults)`, `SetLinkUp`, `Network.AddTap` | the virtual switch |
| `netstack` | `New(*l2.Mux, Config)`, `Stack.ListenPacket`, `ListenUDP`, `SetAddr` | ARP/IPv4/UDP for vnet |
| `ethernet` | `MAC`, `ParseMAC`, `Frame`, `Decode`, `Pad`, the EtherTypes and multicast addresses | |
| `pcap` | `NewWriter`, `NewReader` | capture files |
