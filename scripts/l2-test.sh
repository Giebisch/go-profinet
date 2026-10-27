#!/bin/sh
# Runs the real Layer 2 tests (Tier 4) on a veth pair inside a Linux
# container with CAP_NET_RAW and CAP_NET_ADMIN, so that the host needs no
# privileges. Usage: scripts/l2-test.sh [go test flags]
# Inside a Linux machine with the capabilities, run the same with
# PROFINET_L2_INSIDE=1.
set -eu
GO_IMAGE=${GO_IMAGE:-golang:1.23-bookworm}
if [ "${PROFINET_L2_INSIDE:-}" != 1 ]; then
	exec docker run --rm --cap-add NET_RAW --cap-add NET_ADMIN \
		-e PROFINET_L2_INSIDE=1 -e GOFLAGS="${GOFLAGS:-}" -e GOWORK=off \
		-v "$(cd "$(dirname "$0")/.." && pwd)":/src -w /src \
		-v profinet-gomod:/go/pkg/mod -v profinet-gocache:/root/.cache/go-build \
		"$GO_IMAGE" sh scripts/l2-test.sh "$@"
fi
if ! command -v ip >/dev/null 2>&1; then
	apt-get update -qq >/dev/null && apt-get install -y -qq iproute2 >/dev/null
fi
ip link add pnA type veth peer name pnB
ip link set pnA up
ip link set pnB up
# No IP addresses on the veths: the module's own IP stack answers ARP and
# UDP, so that the kernel stays out of the exchange.
sysctl -qw net.ipv6.conf.pnA.disable_ipv6=1 net.ipv6.conf.pnB.disable_ipv6=1 2>/dev/null || true
export PROFINET_L2_IFACE_A=pnA PROFINET_L2_IFACE_B=pnB
go test -race -count=1 -v "$@" ./l2/afpacket ./l2/osport
# Allocations are counted without the race detector, which allocates.
go test -count=1 -v -run 'TestNoAllocations' "$@" ./l2/afpacket
go test -race -count=1 -v -run 'TestRealL2' "$@" ./e2e
