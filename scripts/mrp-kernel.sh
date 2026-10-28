#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# mrp-kernel.sh <version> <dir>: build a mainline Linux kernel (a tag of torvalds/linux, e.g. v6.12)
# for a virtme-ng microVM with the bridge's MRP support and without
# switchdev, plus what Docker needs inside the guest. With
# CONFIG_NET_SWITCHDEV built in, the kernel refuses an MRP instance on a
# software bridge (br_mrp_switchdev_add returns -EOPNOTSUPP when no driver
# offloads it), and every distribution kernel builds it in; hence our own.
# Prints the path of the kernel image.
set -euo pipefail
v=$1 dir=$2
src="$dir/linux-$v"
if [ ! -f "$src/Makefile" ]; then
    git clone --quiet --depth 1 --branch "$v" https://github.com/torvalds/linux.git "$src"
fi
cd "$src"
# virtme-ng's minimal configuration, then: the bridge with MRP; veth;
# nftables with the bridge family (the adapters count probe frames with
# it); AF_PACKET (otfnode, tcpdump); and Docker's needs (namespaces,
# cgroups v2 controllers, seccomp, overlayfs).
items=(
    BRIDGE BRIDGE_MRP VETH PACKET
    NETFILTER NETFILTER_ADVANCED NF_TABLES NF_TABLES_INET NF_TABLES_BRIDGE NFT_COUNTER
    NAMESPACES NET_NS PID_NS IPC_NS UTS_NS USER_NS
    CGROUPS CGROUP_CPUACCT CGROUP_DEVICE CGROUP_FREEZER CGROUP_SCHED CGROUP_PIDS CPUSETS MEMCG CGROUP_BPF
    BPF_SYSCALL KEYS POSIX_MQUEUE SECCOMP SECCOMP_FILTER OVERLAY_FS TMPFS TMPFS_POSIX_ACL
)
args=()
for i in "${items[@]}"; do args+=(--configitem "CONFIG_$i=y"); done
vng --kconfig "${args[@]}" >/dev/null
scripts/config --disable NET_SWITCHDEV
make olddefconfig >/dev/null
for i in BRIDGE BRIDGE_MRP VETH NF_TABLES_BRIDGE OVERLAY_FS; do
    grep -q "^CONFIG_$i=y" .config || { echo "CONFIG_$i is not set" >&2; exit 1; }
done
if grep -q '^CONFIG_NET_SWITCHDEV=y' .config; then
    echo "CONFIG_NET_SWITCHDEV is selected by:" >&2
    grep -rl 'select NET_SWITCHDEV' --include=Kconfig . >&2
    exit 1
fi
make -j"$(nproc)" >/dev/null
echo "$src/arch/x86/boot/bzImage"
