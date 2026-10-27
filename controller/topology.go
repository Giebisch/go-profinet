// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/dcp"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

// PeerCheck is an expected neighbor of one of a device's ports. The
// controller writes it to the port (PDPortDataCheck with CheckPeers)
// before PrmEnd; the device then reports a "remote mismatch" diagnosis,
// with an alarm, whenever the neighbor its LLDP sees is another one or
// none.
type PeerCheck struct {
	// Subslot is the port submodule; zero means the first port, 0x8001.
	Subslot uint16 `json:"subslot,omitempty"`
	// Station is the expected neighbor's name of station and Port its
	// port, "port-001" for the first.
	Station string `json:"station"`
	Port    string `json:"port"`
}

// peerCheckParameters returns the parameters that write the peer checks.
func peerCheckParameters(m *model.Device, checks []PeerCheck) ([]Parameter, error) {
	var out []Parameter
	seen := map[uint16]bool{}
	for _, c := range checks {
		sub := c.Subslot
		if sub == 0 {
			sub = model.SubslotPort1
		}
		if _, ok := m.Submodule(0, sub); !ok || sub < model.SubslotPort1 || sub > model.SubslotPort1+0xFE {
			return nil, fmt.Errorf("controller: %w: peer check of subslot %#x: not a port of the model", profinet.ErrInvalidConfig, sub)
		}
		if seen[sub] {
			return nil, fmt.Errorf("controller: %w: two peer checks of port %#x", profinet.ErrInvalidConfig, sub)
		}
		if c.Station == "" || c.Port == "" {
			return nil, fmt.Errorf("controller: %w: peer check of port %#x without station or port", profinet.ErrInvalidConfig, sub)
		}
		seen[sub] = true
		rec := pnio.PDPortDataCheck{Subslot: sub, Peers: []pnio.CheckPeer{{PortID: c.Port, ChassisID: c.Station}}}
		out = append(out, Parameter{Slot: 0, Subslot: sub, Index: pnio.IndexPDPortDataCheck, Data: rec.Append(nil)})
	}
	return out, nil
}

// PortRef is a port of a station: its name of station and the port's
// name, "port-001" for the first.
type PortRef struct {
	Station string `json:"station"`
	Port    string `json:"port"`
}

func (p PortRef) String() string { return p.Station + "/" + p.Port }

// Link is a cable between two ports.
type Link struct {
	A PortRef `json:"a"`
	B PortRef `json:"b"`
}

func (l Link) String() string { return l.A.String() + " - " + l.B.String() }

// Link verdicts of a TopologyReport.
const (
	// LinkOK: both ends report each other as their neighbor (or the one
	// end that could be read does).
	LinkOK = "ok"
	// LinkMissing: an end reports no neighbor on the port.
	LinkMissing = "missing"
	// LinkWrongPeer: an end reports another neighbor on the port.
	LinkWrongPeer = "wrong peer"
	// LinkUnknownPort: an end has no port of that name.
	LinkUnknownPort = "unknown port"
	// LinkUnverified: neither end could be read.
	LinkUnverified = "unverified"
)

// LinkResult is the verdict on one expected link.
type LinkResult struct {
	Link    Link   `json:"link"`
	Verdict string `json:"verdict"`
	// Detail says what the ends report when the verdict is not LinkOK.
	Detail string `json:"detail,omitempty"`
}

// TopologyReport compares an expected topology with the one the devices
// report.
type TopologyReport struct {
	Links []LinkResult `json:"links"`
	// Unexpected are links between the stations of the expected topology
	// that the devices report but that are not expected.
	Unexpected []Link `json:"unexpected,omitempty"`
	// Unreachable lists the stations whose port data could not be read,
	// with the reason.
	Unreachable map[string]string `json:"unreachable,omitempty"`
}

// OK reports whether every expected link was verified and nothing else
// was found.
func (r *TopologyReport) OK() bool {
	for _, l := range r.Links {
		if l.Verdict != LinkOK {
			return false
		}
	}
	return len(r.Unexpected) == 0 && len(r.Unreachable) == 0
}

// observed are the neighbors the stations report, by port.
type observed map[PortRef][]PortRef

// normalizePort returns the port name of an LLDP port ID: a device in the
// "standard" mode sends "port-001.<its name>".
func normalizePort(portID, chassis string) string {
	return strings.TrimSuffix(portID, "."+chassis)
}

// CheckTopology finds the stations of the expected topology with DCP,
// reads the port data of each (PDRealData, with an implicit read: no AR
// needed) and compares the neighbors their LLDP reports with the expected
// links. It needs nothing written to the devices beforehand; PeerCheck is
// the way to have the devices watch their neighbors themselves.
func (c *Controller) CheckTopology(ctx context.Context, expected []Link) (TopologyReport, error) {
	rep := TopologyReport{Unreachable: map[string]string{}}
	stations := map[string]bool{}
	for _, l := range expected {
		stations[l.A.Station], stations[l.B.Station] = true, true
	}
	ids, err := c.dcp.Identify(ctx, dcp.FilterAll(), dcp.IdentifyOptions{Wait: time.Second})
	if err != nil {
		return rep, err
	}
	addr := map[string]dcp.Identity{}
	for _, id := range ids {
		addr[id.NameOfStation] = id
	}
	obs := observed{}
	read := map[string]bool{}
	for st := range stations {
		id, ok := addr[st]
		if !ok || id.IP.IsZero() {
			rep.Unreachable[st] = "not found with DCP or without an IP address"
			continue
		}
		rc := NewRecordClient(c.rpcCli, &net.UDPAddr{IP: id.IP.Addr.AsSlice(), Port: rpc.Port}, pnio.ObjectUUID(1, id.DeviceID, id.VendorID))
		b, err := rc.Read(ctx, rpc.NilUUID, Record{Slot: 0, Subslot: model.SubslotDAP, Index: pnio.IndexPDRealData}, 0)
		if err != nil {
			rep.Unreachable[st] = err.Error()
			continue
		}
		rd, err := pnio.DecodePDRealDataRecord(b)
		if err != nil {
			rep.Unreachable[st] = err.Error()
			continue
		}
		read[st] = true
		for _, p := range rd.Ports {
			own := PortRef{Station: st, Port: normalizePort(p.OwnPortID, st)}
			obs[own] = []PortRef{}
			for _, peer := range p.Peers {
				obs[own] = append(obs[own], PortRef{Station: peer.ChassisID, Port: normalizePort(peer.PortID, peer.ChassisID)})
			}
		}
	}
	// The expected neighbors of each port (a port of a shared segment
	// may have several).
	expectedPeer := map[PortRef]map[PortRef]bool{}
	expect := func(own, peer PortRef) {
		if expectedPeer[own] == nil {
			expectedPeer[own] = map[PortRef]bool{}
		}
		expectedPeer[own][peer] = true
	}
	for _, l := range expected {
		expect(l.A, l.B)
		expect(l.B, l.A)
		rep.Links = append(rep.Links, judge(l, obs, read))
	}
	// Links the devices report between expected stations that are not
	// expected, each once.
	seen := map[Link]bool{}
	for own, peers := range obs {
		for _, p := range peers {
			if !stations[p.Station] || expectedPeer[own][p] {
				continue
			}
			l := Link{A: own, B: p}
			if p.String() < own.String() {
				l = Link{A: p, B: own}
			}
			if !seen[l] {
				seen[l] = true
				rep.Unexpected = append(rep.Unexpected, l)
			}
		}
	}
	sort.Slice(rep.Unexpected, func(i, j int) bool { return rep.Unexpected[i].String() < rep.Unexpected[j].String() })
	if len(rep.Unreachable) == 0 {
		rep.Unreachable = nil
	}
	return rep, nil
}

var severity = map[string]int{LinkOK: 0, "": 0, LinkMissing: 1, LinkWrongPeer: 2, LinkUnknownPort: 3}

// judge returns the verdict on one expected link from what its ends
// report.
func judge(l Link, obs observed, read map[string]bool) LinkResult {
	res := LinkResult{Link: l, Verdict: LinkOK}
	checked := 0
	var details []string
	for _, end := range [][2]PortRef{{l.A, l.B}, {l.B, l.A}} {
		own, want := end[0], end[1]
		if !read[own.Station] {
			continue
		}
		checked++
		peers, ok := obs[own]
		verdict := ""
		switch {
		case !ok:
			verdict = LinkUnknownPort
			details = append(details, fmt.Sprintf("%s has no port %s", own.Station, own.Port))
		case len(peers) == 0:
			verdict = LinkMissing
			details = append(details, fmt.Sprintf("%s reports no neighbor", own))
		default:
			found := false
			var names []string
			for _, p := range peers {
				found = found || p == want
				names = append(names, p.String())
			}
			if !found {
				verdict = LinkWrongPeer
				details = append(details, fmt.Sprintf("%s reports %s", own, strings.Join(names, ", ")))
			}
		}
		// The more fundamental problem wins: a port that does not exist,
		// then another neighbor, then none.
		if severity[verdict] > severity[res.Verdict] {
			res.Verdict = verdict
		}
	}
	if checked == 0 {
		res.Verdict = LinkUnverified
		details = append(details, "neither end could be read")
	}
	res.Detail = strings.Join(details, "; ")
	return res
}
