// SPDX-License-Identifier: MIT

package device

import (
	"fmt"
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
	err = mib.AddSystem(func() snmp.System {
		return snmp.System{Descr: descr, ObjectID: c.ObjectID, UpTime: time.Since(start), Contact: c.Contact, Name: d.StationName(), Location: c.Location}
	})
	if err == nil {
		err = mib.AddInterfaces(d.snmpInterfaces)
	}
	if err == nil {
		err = mib.AddSNMPGroup(a.Stats)
	}
	if err == nil && d.lldpA != nil {
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

// snmpInterfaces is the ifTable: the device's one Ethernet interface.
func (d *Device) snmpInterfaces() []snmp.Interface {
	mac := d.mux.HardwareAddr()
	oper := snmp.IfStatusUp
	if !d.linkUp() {
		oper = snmp.IfStatusDown
	}
	return []snmp.Interface{{
		Index: 1, Descr: d.lldpInfo().PortName, Type: snmp.IfTypeEthernetCsmacd, MTU: 1500, Speed: ifSpeed,
		PhysAddress: mac[:], AdminStatus: snmp.IfStatusUp, OperStatus: oper,
	}}
}

// snmpLLDPLocal is the local system data of the LLDP-MIB: what the LLDP
// agent sends.
func (d *Device) snmpLLDPLocal() snmp.LLDPLocal {
	f := d.lldpInfo().Build(d.mux.HardwareAddr(), 0)
	return snmp.LLDPLocal{
		ChassisIDSubtype: int32(f.ChassisSubtype), ChassisID: f.ChassisID, SysName: f.SystemName, SysDesc: f.SystemDescription,
		Ports: []snmp.LLDPLocalPort{{Num: 1, IDSubtype: int32(f.PortSubtype), ID: f.PortID, Desc: f.PortDescription}},
	}
}

// snmpLLDPRemote is lldpRemTable: the LLDP neighbors of the port, numbered
// in the order of their chassis and port IDs.
func (d *Device) snmpLLDPRemote() []snmp.LLDPRemote {
	ns := d.lldpA.Neighbors()
	out := make([]snmp.LLDPRemote, 0, len(ns))
	for i, n := range ns {
		f := &n.Frame
		out = append(out, snmp.LLDPRemote{
			LocalPortNum: 1, Index: int32(i + 1),
			ChassisIDSubtype: int32(f.ChassisSubtype), ChassisID: f.ChassisID, PortIDSubtype: int32(f.PortSubtype), PortID: f.PortID,
			PortDesc: f.PortDescription, SysName: f.SystemName, SysDesc: f.SystemDescription,
		})
	}
	return out
}
