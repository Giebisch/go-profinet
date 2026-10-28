#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# microvm-interop.sh <kernel image> <go test -run pattern>: run interop
# tests in a virtme-ng micro-VM (QEMU/KVM) booted with the given kernel,
# with a Docker daemon of its own inside the guest. For peers that need a
# kernel feature the host does not have: microchip-ung/mrp needs bridge MRP
# without switchdev (scripts/mrp-kernel.sh). The guest shares the host's
# file system read-only; the images (make interop-images), otfnode and the
# test binary are prepared here and loaded there. Results go to
# PROFINET_INTEROP_RESULTS (default interop-results).
set -euo pipefail
kernel=$1 run=$2
results=$(realpath -m "${PROFINET_INTEROP_RESULTS:-interop-results}")
mkdir -p "$results"
out=$(mktemp -d)
export GOWORK=off
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$out/otfnode" ./interop/otfnode
go test -c -tags interop -o "$out/interop.test" ./interop
docker build -q -t go-profinet-interop-runner:dev interop/runner >/dev/null
docker save -o "$out/images.tar" go-profinet-interop-runner:dev \
    profinet-interop-mrp:dev profinet-interop-umrp:dev profinet-interop-scapy:dev profinet-interop-tshark:dev
cat > "$out/guest.sh" <<GUEST
set -e
echo "guest kernel \$(uname -r)"
# Diagnostics of a hang: the kernel reports tasks blocked for 20 s, and
# every minute the tasks in uninterruptible sleep are listed with their
# kernel stacks.
sysctl -qw kernel.hung_task_timeout_secs=20 kernel.hung_task_panic=0 2>/dev/null || true
( while sleep 60; do
    for p in \$(ps -eo pid=,stat= | awk '\$2 ~ /^D/ {print \$1}'); do
        echo "blocked \$p: \$(cat /proc/\$p/cmdline | tr '\\0' ' ')"; cat /proc/\$p/stack 2>/dev/null
    done
    dmesg -c 2>/dev/null | grep -E -A40 'blocked for more than|WARNING:|BUG:' || true
done ) &
mountpoint -q /sys/fs/cgroup || mount -t cgroup2 none /sys/fs/cgroup
d=\$(mktemp -d)
mount -t tmpfs -o size=3G tmpfs "\$d"
export DOCKER_HOST=unix://\$d/docker.sock DOCKER_CONFIG=\$d/cli
dockerd --data-root "\$d/data" --exec-root "\$d/exec" --pidfile "\$d/docker.pid" -H "\$DOCKER_HOST" \
    --iptables=false --ip6tables=false --bridge=none --storage-driver overlay2 >"\$d/dockerd.log" 2>&1 &
for i in \$(seq 60); do docker info >/dev/null 2>&1 && break; sleep 1; done
docker info >/dev/null || { tail -n 40 "\$d/dockerd.log"; exit 1; }
docker load -q -i $out/images.tar
docker run --rm --network none --cap-add NET_ADMIN --cap-add NET_RAW profinet-interop-mrp:dev kernel-mrp ||
    { echo "the guest kernel does not take an MRP instance"; exit 1; }
cd $PWD/interop
PROFINET_INTEROP_OTFNODE=$out/otfnode PROFINET_INTEROP_RESULTS=$results \
    $out/interop.test -test.count=1 -test.timeout=20m -test.v -test.run '$run' 2>&1 | tee $out/test.log
grep -q '^PASS\$' $out/test.log && echo MICROVM-OK
GUEST
start=$(date +%s)
vng --run "$kernel" --root / --user root --memory 4G --cpus "$(nproc)" \
    --rwdir "$out" --rwdir "$results" \
    --exec "bash $out/guest.sh" | tee "$out/log"
echo "micro-VM run took $(( $(date +%s) - start )) s"
grep -q MICROVM-OK "$out/log"
