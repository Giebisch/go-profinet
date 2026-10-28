// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"syscall"

	"github.com/otfabric/go-profinet/dcp"
)

// setIP applies a DCP Set of the IP suite to the interface with iproute2,
// as a device on a real interface must (the container has CAP_NET_ADMIN).
func setIP(iface string, s dcp.IPSuite) error {
	if out, err := exec.Command("ip", "address", "flush", "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip address flush: %v: %s", err, out)
	}
	if s.IsZero() {
		return nil
	}
	ones, _ := net.IPMask(s.Mask.AsSlice()).Size()
	if out, err := exec.Command("ip", "address", "add", fmt.Sprintf("%s/%d", s.Addr, ones), "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip address add: %v: %s", err, out)
	}
	return nil
}

// setLinkUp sets an interface up.
func setLinkUp(iface string) error {
	if out, err := exec.Command("ip", "link", "set", "dev", iface, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up: %v: %s", err, out)
	}
	return nil
}

// listenPacket opens a UDP socket like net.ListenConfig, and when a
// wildcard address is taken it binds the interface's address instead:
// Docker's embedded DNS server listens in every container of a
// user-defined network on 127.0.0.11 at a random ephemeral port, which can
// be the PROFINET RPC port 34964 (seen once in a soak of a few thousand
// containers).
func listenPacket(iface string) func(ctx context.Context, network, address string) (net.PacketConn, error) {
	return func(ctx context.Context, network, address string) (net.PacketConn, error) {
		var lc net.ListenConfig
		c, err := lc.ListenPacket(ctx, network, address)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
			return c, err
		}
		host, port, serr := net.SplitHostPort(address)
		ip := ifaceSuite(iface).Addr
		if serr != nil || host != "" || !ip.IsValid() {
			return nil, err
		}
		emit(map[string]any{"event": "bind-fallback", "address": address, "error": err.Error(), "to": ip.String()})
		return lc.ListenPacket(ctx, network, net.JoinHostPort(ip.String(), port))
	}
}
