# The container go-profinet's otfnode runs in during the interop tests:
# Debian with iproute2 (a DCP Set IP is applied to the interface) and
# tcpdump (capture sidecars).
FROM debian:bookworm-20251020-slim
RUN apt-get update && apt-get install -y --no-install-recommends iproute2 tcpdump && rm -rf /var/lib/apt/lists/*
