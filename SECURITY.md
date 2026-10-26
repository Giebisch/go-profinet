# Security Policy

## Reporting a Vulnerability

Please report vulnerabilities privately: email **security@otfabric.io**, or
open a private advisory on GitHub (the
[Security](https://github.com/otfabric/go-profinet/security) tab →
**Advisories** → **Report a vulnerability**), with a description, steps to
reproduce, the affected version and the impact you identified. Do not open a
public issue.

We acknowledge reports within 48 hours and aim to fix or mitigate critical
issues within 7 days.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x | Yes |

## Security considerations

### PROFINET has no security of its own

PROFINET IO without the security classes (which go-profinet does not
implement) has no authentication, integrity protection or encryption.
Anyone with Layer 2 access to a device can rename it, readdress it, reset it,
write its records and take control of its outputs; anyone on the path can
read and alter the cyclic data. go-profinet does not change that.

- Run PROFINET on isolated networks; separate them from office networks and
  the Internet with the zones and conduits of IEC 62443.
- Treat every host that runs go-profinet with raw Ethernet access as part of
  the control network.

### Privileges

Raw Ethernet needs CAP_NET_RAW (Linux), access to `/dev/bpf*` (macOS) or
Npcap (Windows). Grant the capability to the program, not root to the user;
do not grant it on shared hosts. The virtual network (`vnet`) needs none.

### Hardening of the implementation

- Every decoder of network input bounds counts and lengths before it
  allocates, rejects trailing or inconsistent data and is fuzzed
  (`make fuzz`); malformed input is dropped and counted, never a panic.
- Resources are bounded: RPC messages (64 KiB, 256 fragments), RPC
  activities and handler workers, ARs per device, subscription queues, alarm
  windows, GSDML documents (64 MiB; `encoding/xml` processes no DTDs or
  external entities).
- Concurrency is race-tested (`go test -race`) and chaos-tested with seeded
  loss, duplication, corruption, reordering and delay.
- The DCP responder ignores other requesters for 3 s after a Get or Set, as
  devices do, and validates station names.

### Changing devices

`dcp.Client.Set`, `SetName`, `SetIP`, `ResetToFactory`, `RecordClient.Write`
and the outputs of a controller change real equipment; their documentation
says so. Only call them on an explicit request of the user.

### Not certified

go-profinet is not certified by PROFIBUS & PROFINET International and is not
a safety component.

### Reference implementations

The interop suite runs GPL-licensed reference implementations in containers.
The p-net tree used is older than its v1.0.2 security fixes: run the interop
suite only on isolated Docker networks.
