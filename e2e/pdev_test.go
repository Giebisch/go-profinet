// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

// portMismatch returns the extended error types of the remote mismatch
// diagnoses the device reports for its first port, read by the
// controller.
func portMismatch(t *testing.T, cd *controller.Device) []uint16 {
	t.Helper()
	diag, err := cd.ReadDiagnosis(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []uint16
	for _, s := range diag {
		if s.Slot != 0 || s.Subslot != model.SubslotPort1 {
			continue
		}
		for _, it := range s.Items {
			if it.ErrorType == pnio.ChannelErrRemoteMismat {
				out = append(out, it.ExtErrorType)
			}
		}
	}
	return out
}

func drainAlarms(ch <-chan controller.AlarmEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// waitPortAlarm waits for a diagnosis alarm of the first port, appearing
// or disappearing, with the extended error type ext. On a timeout it lists
// what it saw instead.
func waitPortAlarm(t *testing.T, alarms <-chan controller.AlarmEvent, appears bool, ext uint16, cd *controller.Device) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	connects := cd.Stats().Connects
	var seen []string
	for {
		select {
		case a := <-alarms:
			n := a.Notification
			items, _ := pnio.DecodeDiagnosisItems(n.USI, n.Data)
			seen = append(seen, fmt.Sprintf("%s %d/%#x %+v", n.Type, n.Slot, n.Subslot, items))
			if n.Subslot != model.SubslotPort1 || !n.HasItem {
				continue
			}
			for _, it := range items {
				if it.ErrorType == pnio.ChannelErrRemoteMismat && it.ExtErrorType == ext && it.Appears() == appears {
					return
				}
			}
		case <-deadline:
			// On a starved runner the device may end the AR when an alarm
			// is not acknowledged in time; the controller connects again
			// and an alarm sent meanwhile went to the old AR. The diagnosis
			// must then be right in the new one.
			if st := cd.Stats(); st.Connects != connects {
				t.Logf("the AR was established again (%d connects, %d aborts): checking the diagnosis instead of the alarm", st.Connects, st.Aborts)
				eventually(t, 10*time.Second, "diagnosis after the reconnect", func() bool {
					got := portMismatch(t, cd)
					return appears == (len(got) == 1 && got[0] == ext)
				})
				return
			}
			t.Fatalf("no alarm of remote mismatch %#x (appears %v); alarms %v; diagnosis now %#x; stats %+v", ext, appears, seen, portMismatch(t, cd), cd.Stats())
		}
	}
}

// TestPeerCheck: the controller writes the expected neighbor of the
// device's port; the device reports, with alarms, when the neighbor is
// gone or another one, and when it is right again.
func TestPeerCheck(t *testing.T) {
	e := newEnv(t, 71)
	sw, swNode := e.device("sw-1", "192.168.0.11", testModel(), nil)
	alarms := make(chan controller.AlarmEvent, 64)
	_, _, cd := running(t, e, "io-1", "192.168.0.10", controller.DeviceConfig{
		PeerChecks: []controller.PeerCheck{{Station: "sw-1", Port: "port-001"}},
		OnAlarm:    func(a controller.AlarmEvent) { alarms <- a },
	})
	eventually(t, 5*time.Second, "right neighbor", func() bool { return len(portMismatch(t, cd)) == 0 })
	// The check was evaluated before the device heard its neighbor: drop
	// the alarms of that.
	drainAlarms(alarms)

	// The neighbor goes: its LLDP information times out.
	_ = sw.Close()
	_ = swNode.mux.Close()
	waitPortAlarm(t, alarms, true, 0x8005, cd)
	if got := portMismatch(t, cd); len(got) != 1 || got[0] != 0x8005 {
		t.Fatalf("diagnosis %#x", got)
	}
	// It comes back.
	drainAlarms(alarms)
	e.device("sw-1", "192.168.0.12", testModel(), nil)
	waitPortAlarm(t, alarms, false, 0x8005, cd)
	eventually(t, 5*time.Second, "cleared", func() bool { return len(portMismatch(t, cd)) == 0 })

	// Parameterization in the Data state: another expected neighbor.
	drainAlarms(alarms)
	c := pnio.PDPortDataCheck{Subslot: model.SubslotPort1, Peers: []pnio.CheckPeer{{PortID: "port-001", ChassisID: "sw-9"}}}
	if err := cd.WriteRecord(context.Background(), controller.Record{Subslot: model.SubslotPort1, Index: pnio.IndexPDPortDataCheck}, c.Append(nil)); err != nil {
		t.Fatal(err)
	}
	waitPortAlarm(t, alarms, true, 0x8000, cd)
	// The device refuses checks it does not implement.
	mau := pnio.PDPortDataCheck{Subslot: model.SubslotPort1, Other: []pnio.RawBlock{{Header: pnio.BlockHeader{Type: pnio.BlockCheckMAUType, VersionHigh: 1}, Body: []byte{0, 0x10}}}}
	err := cd.WriteRecord(context.Background(), controller.Record{Subslot: model.SubslotPort1, Index: pnio.IndexPDPortDataCheck}, mau.Append(nil))
	var se *pnio.StatusError
	if !errors.As(err, &se) || se.Status.Code1 != pnio.RWFeatureNotSupport {
		t.Fatalf("CheckMAUType: %v", err)
	}
}

func TestPeerCheckConfig(t *testing.T) {
	e := newEnv(t, 72)
	ctrl, _ := e.controller("192.168.0.1")
	for name, checks := range map[string][]controller.PeerCheck{
		"not a port":      {{Subslot: 0x8005, Station: "a", Port: "port-001"}},
		"the interface":   {{Subslot: model.SubslotInterface, Station: "a", Port: "port-001"}},
		"twice":           {{Station: "a", Port: "port-001"}, {Subslot: model.SubslotPort1, Station: "b", Port: "port-001"}},
		"without station": {{Port: "port-001"}},
	} {
		if _, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "x", Model: testModel(), PeerChecks: checks}); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestCheckTopology compares an expected topology with the one the
// devices report through LLDP and PDRealData.
func TestCheckTopology(t *testing.T) {
	e := newEnv(t, 73)
	ctrl, _ := e.controller("192.168.0.1")
	e.device("io-1", "192.168.0.10", testModel(), nil)
	e.device("io-2", "192.168.0.11", testModel(), nil)
	ctx := context.Background()
	link := func(a, ap, b, bp string) controller.Link {
		return controller.Link{A: controller.PortRef{Station: a, Port: ap}, B: controller.PortRef{Station: b, Port: bp}}
	}
	var rep controller.TopologyReport
	eventually(t, 10*time.Second, "topology verified", func() bool {
		var err error
		rep, err = ctrl.CheckTopology(ctx, []controller.Link{link("io-1", "port-001", "io-2", "port-001")})
		return err == nil && rep.OK()
	})
	// A station that is not there; a port that does not exist.
	rep, err := ctrl.CheckTopology(ctx, []controller.Link{link("io-1", "port-001", "ghost", "port-001")})
	if err != nil || rep.OK() || rep.Unreachable["ghost"] == "" || rep.Links[0].Verdict != controller.LinkWrongPeer {
		t.Fatalf("ghost: %+v %v", rep, err)
	}
	rep, _ = ctrl.CheckTopology(ctx, []controller.Link{link("io-1", "port-002", "io-2", "port-001")})
	if rep.Links[0].Verdict != controller.LinkUnknownPort {
		t.Fatalf("unknown port: %+v", rep)
	}
	rep, _ = ctrl.CheckTopology(ctx, []controller.Link{link("ghost-a", "port-001", "ghost-b", "port-001")})
	if rep.Links[0].Verdict != controller.LinkUnverified {
		t.Fatalf("unverified: %+v", rep)
	}
	// A third device on the segment: io-1 and io-2 report it, an
	// unexpected link between expected stations is listed.
	e.device("io-3", "192.168.0.12", testModel(), nil)
	eventually(t, 10*time.Second, "unexpected link", func() bool {
		rep, err = ctrl.CheckTopology(ctx, []controller.Link{link("io-1", "port-001", "io-2", "port-001"), link("io-2", "port-001", "io-3", "port-001")})
		return err == nil && len(rep.Unexpected) == 1 && rep.Unexpected[0] == link("io-1", "port-001", "io-3", "port-001")
	})
	for _, l := range rep.Links {
		if l.Verdict != controller.LinkOK {
			t.Fatalf("%+v", rep)
		}
	}
	if rep.OK() || rep.Links[0].Link.String() != "io-1/port-001 - io-2/port-001" {
		t.Fatalf("%+v", rep)
	}
	// A link whose end reports no neighbor.
	e2 := newEnv(t, 74)
	c2, _ := e2.controller("192.168.0.1")
	e2.device("lonely", "192.168.0.10", testModel(), nil)
	eventually(t, 10*time.Second, "missing", func() bool {
		rep, err = c2.CheckTopology(ctx, []controller.Link{link("lonely", "port-001", "ghost", "port-001")})
		return err == nil && rep.Links[0].Verdict == controller.LinkMissing
	})
}
