// SPDX-License-Identifier: MIT

// Package vnetenv wires endpoints of a virtual network for the examples:
// each node is a vnet endpoint with a Layer 2 mux and the module's IP
// stack, which is all a controller or a device needs.
package vnetenv

import (
	"net/netip"

	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/netstack"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/vnet"
)

// Node is one endpoint with its IP stack.
type Node struct {
	Endpoint *vnet.Endpoint
	Mux      *l2.Mux
	Stack    *netstack.Stack
}

// Close releases the node.
func (n *Node) Close() {
	n.Stack.Close()
	n.Mux.Close()
	n.Endpoint.Close()
}

// NewNode attaches a node with the given last MAC octet and IPv4 address
// (empty for none) to the network.
func NewNode(net *vnet.Network, last byte, ip string) (*Node, error) {
	ep, err := net.NewEndpoint(vnet.EndpointConfig{MAC: ethernet.MAC{0x02, 0, 0, 0, 0, last}})
	if err != nil {
		return nil, err
	}
	mux := l2.NewMux(ep)
	cfg := netstack.Config{}
	if ip != "" {
		cfg.Addr, cfg.Mask = netip.MustParseAddr(ip), netip.MustParseAddr("255.255.255.0")
	}
	st, err := netstack.New(mux, cfg)
	if err != nil {
		mux.Close()
		ep.Close()
		return nil, err
	}
	return &Node{Endpoint: ep, Mux: mux, Stack: st}, nil
}

// Suite returns the IP suite of a /24 address without a gateway.
func Suite(ip string) dcp.IPSuite {
	return dcp.IPSuite{Addr: netip.MustParseAddr(ip), Mask: netip.MustParseAddr("255.255.255.0"), Gateway: netip.MustParseAddr("0.0.0.0")}
}

// Model returns the example device: an 8-octet input module in slot 1 and
// a 4-octet output module with a parameter record in slot 2.
func Model() *model.Device {
	return &model.Device{
		Identity: model.Identity{
			VendorID: 0xF000, DeviceID: 0x0001, VendorName: "OT Fabric", TypeOfStation: "go-profinet example device",
			Family: "Examples", OrderID: "OTF-EXAMPLE-1", SerialNumber: "0001", HardwareRevision: 1,
			SoftwareRevision: pnio.SoftwareRevision{Prefix: 'V', FunctionalEnhance: 1},
		},
		Capabilities: model.Capabilities{MaxARs: 2, MinDeviceInterval: 32, ConformanceClass: "A", Ports: 1, LLDP: true},
		DAP:          model.StandardDAP("go-profinet example DAP", 0x00000100, 1),
		Slots: []model.Slot{
			{Number: 1, Module: model.Module{Name: "8 octets in", IdentNumber: 0x10, Submodules: []model.Submodule{{Name: "Inputs", IdentNumber: 0x11, Subslot: 1, InputLen: 8}}}},
			{Number: 2, Module: model.Module{Name: "4 octets out", IdentNumber: 0x20, Submodules: []model.Submodule{{Name: "Outputs", IdentNumber: 0x21, Subslot: 1, OutputLen: 4,
				Records: []model.Record{{Index: 0x0010, Name: "Mode", Length: 2, Default: []byte{0, 1}}}}}}},
		},
	}
}
