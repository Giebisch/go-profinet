// SPDX-License-Identifier: MIT

package device

import (
	"fmt"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/lldp"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

// The ports of the device. A device without Config.Bridge has one port,
// its Mux: the interface and the port are the same. A device with a
// bridge (a multiport device) has one port per bridge port, each with its
// own MAC address, LLDP agent and link state, and its interface on the
// bridge's CPU port; port n is the port submodule 0x8000+n of the DAP.

// port is an Ethernet port of the device.
type port struct {
	n       int    // 1-based
	subslot uint16 // model.SubslotPort1 + n - 1
	mux     *l2.Mux
	ownMux  bool // the device built mux (on a bridge port) and closes it
	lldpA   *lldp.Agent
	link    l2.LinkStater // nil: always up
}

// linkUp reports the port's link state; a port that does not report it
// counts as up.
func (p *port) linkUp() bool {
	return p.link == nil || p.link.LinkUp()
}

// name is the port's LLDP port name.
func (p *port) name() string { return fmt.Sprintf("port-%03d", p.n) }

// openPorts builds the device's ports: the bridge's, or the Mux alone.
func (d *Device) openPorts() error {
	br := d.cfg.Bridge
	if br == nil {
		p := &port{n: 1, subslot: model.SubslotPort1, mux: d.mux}
		if ls, ok := d.mux.Port().(l2.LinkStater); ok {
			p.link = ls
		}
		d.ports = []*port{p}
		return nil
	}
	if d.mux.Port() != br.CPU() {
		return fmt.Errorf("device: Mux is not on the bridge's CPU port: %w", profinet.ErrInvalidConfig)
	}
	for i := 0; i < br.NumPorts(); i++ {
		sub := model.SubslotPort1 + uint16(i)
		if _, ok := d.model.Submodule(0, sub); !ok {
			return fmt.Errorf("device: bridge port %d has no port submodule %#x in the model: %w", i+1, sub, profinet.ErrInvalidConfig)
		}
		lp := br.Port(i)
		p := &port{n: i + 1, subslot: sub, mux: l2.NewMux(lp), ownMux: true}
		if ls, ok := lp.(l2.LinkStater); ok {
			p.link = ls
		}
		d.ports = append(d.ports, p)
	}
	return nil
}

// startLLDP starts an LLDP agent on every port.
func (d *Device) startLLDP() error {
	for _, p := range d.ports {
		a, err := lldp.NewAgent(p.mux, lldp.AgentOptions{
			Interval: d.cfg.LLDPInterval,
			Local:    func() lldp.LocalInfo { return d.lldpInfo(p) },
			OnChange: d.pokePDev,
			Logger:   d.log,
		})
		if err != nil {
			return err
		}
		p.lldpA = a
	}
	return nil
}

// closePorts stops the LLDP agents and the muxes the device built.
func (d *Device) closePorts() {
	for _, p := range d.ports {
		if p.lldpA != nil {
			p.lldpA.Close()
		}
		if p.ownMux {
			_ = p.mux.Close()
		}
	}
}

// portBySubslot returns the port of a port submodule, nil when the
// device has no such port (the model may declare more ports than the
// device has).
func (d *Device) portBySubslot(subslot uint16) *port {
	for _, p := range d.ports {
		if p.subslot == subslot {
			return p
		}
	}
	return nil
}

// anyLinkUp reports whether any port's link is up.
func (d *Device) anyLinkUp() bool {
	for _, p := range d.ports {
		if p.linkUp() {
			return true
		}
	}
	return false
}

// alias returns the alias name the LLDP neighbors give the device: that
// of the first port that has a neighbor.
func (d *Device) alias() string {
	for _, p := range d.ports {
		if p.lldpA == nil {
			continue
		}
		if a := p.lldpA.Alias(); a != "" {
			return a
		}
	}
	return ""
}

// hasLinkState reports whether any port reports its link state.
func (d *Device) hasLinkState() bool {
	for _, p := range d.ports {
		if p.link != nil {
			return true
		}
	}
	return false
}

// watchLinks follows the links of the ports: when a port's link goes down
// its LLDP neighbors are gone; when the first link comes up a Hello
// sequence starts (if configured); the port checks are evaluated again
// either way.
func (d *Device) watchLinks() {
	defer d.wg.Done()
	t := time.NewTicker(helloLinkPoll)
	defer t.Stop()
	up := make([]bool, len(d.ports))
	for i, p := range d.ports {
		up[i] = p.linkUp()
	}
	anyUp := d.anyLinkUp()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
		}
		changed := false
		for i, p := range d.ports {
			now := p.linkUp()
			if now == up[i] {
				continue
			}
			changed = true
			up[i] = now
			if now {
				d.log.Debugf("device: port %d link up", p.n)
				continue
			}
			d.log.Debugf("device: port %d link down", p.n)
			if p.lldpA != nil {
				p.lldpA.Clear()
			}
		}
		if !changed {
			continue
		}
		d.pokePDev()
		now := d.anyLinkUp()
		if now && !anyUp && d.cfg.Hello != 0 {
			d.startHello()
		}
		anyUp = now
	}
}

// dcpGroups are the multicast groups of a DCPBoundary.
func dcpGroups(boundary uint32) []ethernet.MAC {
	var g []ethernet.MAC
	if boundary&pnio.DCPBoundaryIdentify != 0 {
		g = append(g, ethernet.DCPIdentifyMulticast)
	}
	if boundary&pnio.DCPBoundaryHello != 0 {
		g = append(g, ethernet.DCPHelloMulticast)
	}
	return g
}
