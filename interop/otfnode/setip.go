// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"net"
	"os/exec"

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
