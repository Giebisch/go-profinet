# PROFINET implementation progress

Durable state of the go-profinet / profinet-interop / profinetctl build.
Resume from here after an interruption. Newest entries at the top of each
section.

## Decisions

- D1 Module root `profinet` is a leaf: sentinel errors, Logger, Metrics.
  Every package imports it; typed errors (PNIO status, DCP block error, RPC
  fault) match the sentinels with `Is`.
- D2 Transport split: `ethernet` (frame codec) · `l2` (Port interface + Mux
  with bounded per-subscription queues) · `vnet` (in-memory switch with
  seeded faults) · `netstack` (minimal ARP/IPv4/UDP over l2 so that RPC on a
  virtual network is also Ethernet frames) · `rpc` (CL DCE/RPC over any
  net.PacketConn) · OS backends `l2/afpacket` (Linux), `l2/bpf` (darwin),
  `l2/npcap` (Windows).
- D3 GSDML XSDs are PI members-only and not redistributable (research:
  docs/research/GSDML.md). The committed source of generation is an
  independently written schema catalog (`gsdml/schema/catalog.json`); the
  generator can additionally read a licensed local XSD copy
  (`GSDML_XSD_DIR`) to check the catalog. GSDX/MRCD formats are not public:
  not implemented, documented as blocked.
- D4 Consumer assigns FrameID: controller assigns the input CR's FrameID in
  the request, the device assigns the output CR's in the response.

## Status

| Area | State |
|------|-------|
| root errors/logger | done |
| ethernet codec | done (tests pending) |
| l2 Mux | done (tests pending) |
| vnet | done (tests pending) |
| netstack | done, tested |
| dcp codec/client/responder | done, tested |
| rpc CL client/server | done, tested |
| lldp | todo |
| pnio blocks | todo |
| rt cyclic | todo |
| alarm RTA | todo |
| device / controller | todo |
| gsdml | todo |
| OS L2 backends | todo |
| interop repo | todo |
| profinetctl | todo |

## Blockers

- B1 GSDML XSD / GSDML spec / GSDX / MRCD: PI membership + licence needed
  (see D3). Human decision: does OT Fabric hold PI membership?

## Next actions

1. pnio blocks, rt, alarm, lldp.
2. device + controller + e2e on vnet.
