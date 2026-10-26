// SPDX-License-Identifier: MIT

// Package osport opens an l2.Port on a network interface with the backend
// of the operating system: AF_PACKET on Linux (package l2/afpacket), BPF
// on macOS and the BSDs (package l2/bpf) and Npcap on Windows (package
// l2/npcap). See each backend for the privileges it needs.
package osport

import (
	"net"

	"github.com/otfabric/go-profinet/l2"
)

// Options configure a port.
type Options struct {
	// Promiscuous receives unicast frames for other addresses too.
	Promiscuous bool
}

// Open opens a port on the named interface.
func Open(name string, opts Options) (l2.Port, error) { return open(name, opts) }

// Interface describes a network interface a port can be opened on.
type Interface struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac"`
	MTU   int      `json:"mtu"`
	Up    bool     `json:"up"`
	Addrs []string `json:"addresses,omitempty"`
}

// Interfaces lists the Ethernet interfaces of the host.
func Interfaces() ([]Interface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Interface
	for _, i := range ifs {
		if len(i.HardwareAddr) != 6 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		it := Interface{Name: i.Name, MAC: i.HardwareAddr.String(), MTU: i.MTU, Up: i.Flags&net.FlagUp != 0}
		if addrs, err := i.Addrs(); err == nil {
			for _, a := range addrs {
				it.Addrs = append(it.Addrs, a.String())
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// Backend names the backend Open uses on this platform.
func Backend() string { return backend }
