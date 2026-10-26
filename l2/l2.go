// SPDX-License-Identifier: MIT

// Package l2 defines the Layer 2 port abstraction PROFINET runs on and a
// demultiplexer that shares one port between the protocol engines (DCP,
// LLDP, cyclic RT, alarms, the IP stack of a virtual network).
//
// A [Port] sends and receives complete Ethernet frames without FCS. The
// implementations are:
//
//   - the virtual Ethernet network of package vnet, which needs no
//     privileges and is what the unit and end-to-end tests use;
//   - raw packet sockets: AF_PACKET on Linux (package l2/afpacket), BPF
//     devices on macOS and the BSDs (package l2/bpf) and Npcap on Windows
//     (package l2/npcap), which need the privileges documented in each.
//
// Protocol code depends only on [Port], never on an operating system API.
package l2

import (
	"context"

	"github.com/otfabric/go-profinet/ethernet"
)

// Port is an Ethernet port that sends and receives whole frames.
//
// ReadFrame and WriteFrame may be called concurrently with each other;
// a Port must allow one reader and any number of writers at the same
// time. Close unblocks a pending ReadFrame, which then returns an error
// matching profinet.ErrClosed.
type Port interface {
	// ReadFrame reads the next frame into p and returns its length. A
	// frame longer than p is truncated to len(p). It returns ctx.Err()
	// when ctx is done first.
	ReadFrame(ctx context.Context, p []byte) (int, error)

	// WriteFrame sends one frame. Frames shorter than
	// ethernet.MinFrameLen are padded by the port. The port does not
	// retain p after WriteFrame returns.
	WriteFrame(ctx context.Context, p []byte) error

	// HardwareAddr returns the port's MAC address, the source address of
	// the frames the protocol engines send.
	HardwareAddr() ethernet.MAC

	// MTU returns the largest payload the port can send in one frame.
	MTU() int

	// Close releases the port.
	Close() error
}

// MulticastPort is implemented by ports that filter multicast frames and
// must be told which groups to receive.
type MulticastPort interface {
	Port
	JoinMulticast(group ethernet.MAC) error
	LeaveMulticast(group ethernet.MAC) error
}

// LinkStater is implemented by ports that report the state of their link.
type LinkStater interface {
	// LinkUp reports whether the link is up.
	LinkUp() bool
}

// JoinMulticast joins group when p filters multicast frames, and does
// nothing otherwise.
func JoinMulticast(p Port, group ethernet.MAC) error {
	if m, ok := p.(MulticastPort); ok {
		return m.JoinMulticast(group)
	}
	return nil
}
