// SPDX-License-Identifier: MIT

package device

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/snmp"
)

// SNMPConfig turns on the device's read-only SNMPv1/v2c agent (package
// snmp): the system, interfaces and snmp groups of MIB-II and the
// LLDP-MIB, the network management objects that PI's conformance class B
// asks for. The community string travels in clear text and is the only
// access control.
type SNMPConfig struct {
	// Community is the read community; required.
	Community string
	// Contact and Location are sysContact and sysLocation.
	Contact, Location string
	// Description is sysDescr; empty uses the model's TypeOfStation (what
	// LLDP sends as the system description).
	Description string
	// ObjectID is sysObjectID; nil serves zeroDotZero.
	ObjectID snmp.OID
	// Port is the agent's UDP port; zero means 161.
	Port int
	// Rate limits the requests per second (snmp.Config.Rate); zero
	// means snmp.DefaultRate, negative no limit.
	Rate float64
}

// ifSpeed is the speed the device reports for its port: 100 Mbit/s, as
// the MAU type of its LLDP MAC/PHY TLV (100BASE-TX full duplex) says.
const ifSpeed = 100_000_000

// startSNMP opens the SNMP socket and serves the device's MIB on it.
func (d *Device) startSNMP() error {
	c := d.cfg.SNMP
	if c.Community == "" {
		return fmt.Errorf("device: %w: SNMP needs a community", profinet.ErrInvalidConfig)
	}
	port := c.Port
	if port == 0 {
		port = snmp.Port
	}
	conn, err := d.cfg.ListenPacket(d.ctx, "udp4", ":"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("device: SNMP socket: %w", err)
	}
	mib := snmp.NewMIB()
	a, err := snmp.NewAgent(conn, true, snmp.Config{Community: c.Community, MIB: mib, Rate: c.Rate, Logger: d.log})
	if err != nil {
		_ = conn.Close()
		return err
	}
	d.snmpA = a
	descr := c.Description
	if descr == "" {
		descr = d.model.Identity.TypeOfStation
	}
	start := time.Now()
	d.snmpStart = start
	err = mib.AddSystem(func() snmp.System {
		return snmp.System{Descr: descr, ObjectID: c.ObjectID, UpTime: time.Since(start), Contact: c.Contact, Name: d.StationName(), Location: c.Location}
	})
	if err == nil {
		err = mib.AddInterfaces(d.snmpInterfaces)
	}
	if err == nil {
		err = mib.AddSNMPGroup(a.Stats)
	}
	if err == nil && len(d.ports) > 0 && d.ports[0].lldpA != nil {
		err = mib.AddLLDP(d.snmpLLDPLocal, d.snmpLLDPRemote)
	}
	if err != nil {
		return err
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if err := a.Serve(d.ctx); err != nil && d.ctx.Err() == nil {
			d.log.Warnf("device: SNMP: %v", err)
		}
	}()
	return nil
}

// snmpInterfaces is the ifTable: one row per Ethernet port.
func (d *Device) snmpInterfaces() []snmp.Interface {
	out := make([]snmp.Interface, 0, len(d.ports))
	for _, p := range d.ports {
		mac := p.mux.HardwareAddr()
		oper := snmp.IfStatusUp
		if !p.linkUp() {
			oper = snmp.IfStatusDown
		}
		out = append(out, snmp.Interface{
			Index: int32(p.n), Descr: p.name(), Type: snmp.IfTypeEthernetCsmacd, MTU: 1500, Speed: ifSpeed,
			PhysAddress: mac[:], AdminStatus: snmp.IfStatusUp, OperStatus: oper,
		})
	}
	return out
}

// snmpLLDPLocal is the local system data of the LLDP-MIB: what the LLDP
// agents of the ports send.
func (d *Device) snmpLLDPLocal() snmp.LLDPLocal {
	var l snmp.LLDPLocal
	for i, p := range d.ports {
		f := d.lldpInfo(p).Build(p.mux.HardwareAddr(), 0)
		if i == 0 {
			l = snmp.LLDPLocal{ChassisIDSubtype: int32(f.ChassisSubtype), ChassisID: f.ChassisID, SysName: f.SystemName, SysDesc: f.SystemDescription}
		}
		l.Ports = append(l.Ports, snmp.LLDPLocalPort{Num: int32(p.n), IDSubtype: int32(f.PortSubtype), ID: f.PortID, Desc: f.PortDescription})
	}
	return l
}

// snmpLLDPRemote is lldpRemTable: the LLDP neighbors of each port, each
// with the index its port's LLDP agent gave it when it appeared (kept
// while it stays), and its last change as a time mark in sysUpTime units
// (0 for a change before the agent started).
func (d *Device) snmpLLDPRemote() []snmp.LLDPRemote {
	var out []snmp.LLDPRemote
	for _, p := range d.ports {
		if p.lldpA == nil {
			continue
		}
		for _, n := range p.lldpA.Neighbors() {
			f := &n.Frame
			var mark uint32
			if since := n.Changed.Sub(d.snmpStart); since > 0 {
				mark = uint32(min(since/(10*time.Millisecond), math.MaxUint32))
			}
			out = append(out, snmp.LLDPRemote{
				TimeMark: mark, LocalPortNum: int32(p.n), Index: int32(n.Index),
				ChassisIDSubtype: int32(f.ChassisSubtype), ChassisID: f.ChassisID, PortIDSubtype: int32(f.PortSubtype), PortID: f.PortID,
				PortDesc: f.PortDescription, SysName: f.SystemName, SysDesc: f.SystemDescription,
			})
		}
	}
	return out
}
