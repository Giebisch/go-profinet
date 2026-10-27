// SPDX-License-Identifier: MIT

package device

import (
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/alarm"
	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/pnio"
)

// TestPlugHandover: when the owner's AR ends, the other AR is told about
// each submodule with a Released alarm, one at a time; PrmEnd (plug) with
// the alarm's sequence number is answered with ApplicationReady (plug); a
// controller that does not answer is waited for until its activity
// timeout, then the next submodule follows.
func TestPlugHandover(t *testing.T) {
	n := newNet(t)
	d, _ := n.device(nil)
	pa := n.peer(d, "192.168.0.2", true)
	reqA := pa.ioCAR()
	pa.establish(reqA)
	pb := n.peer(d, "192.168.0.3", true)
	reqB := pb.ioCAR()
	reqB.AR.ActivityTimeoutFactor = 5 // 500 ms
	respB, err := pb.connect(reqB)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan pnio.AlarmNotification, 16)
	pb.openAlarms(reqB, respB, func(high bool, nt pnio.AlarmNotification, ep *alarm.Endpoint) {
		if nt.Type == pnio.AlarmReleased {
			released <- nt
		}
		// Not on the endpoint's own goroutine, which delivers the
		// transport acknowledgement.
		ack := pnio.AckFor(&nt, pnio.Status{})
		go func() { _ = ep.Send(context.Background(), ack.Append(nil)) }()
	})
	readyPlug := make(chan uint16, 16)
	pb.setOnReady(func(cb pnio.ControlBlock) []byte {
		if cb.Type == pnio.BlockAppReadyPlugReq {
			readyPlug <- cb.AlarmSequence
		}
		return nil
	})
	if err := pb.prmEnd(reqB); err != nil {
		t.Fatal(err)
	}
	waitAR(t, d, reqB.AR.ARUUID, ARStateData)
	ctx := context.Background()
	prmEndPlug := func(seq uint16) error {
		return pb.rc.Control(ctx, pnio.ControlBlock{Type: pnio.BlockPrmEndPlugReq, ARUUID: reqB.AR.ARUUID, SessionKey: reqB.AR.SessionKey, Command: pnio.CmdPrmEnd, AlarmSequence: seq})
	}
	// Nothing handed over yet: PrmEnd (plug) is out of place.
	wantStatus(t, prmEndPlug(0), pnio.Code1CMDEV, 0)
	// B's ModuleDiff shows the submodules locked.
	b, err := pb.rc.Read(ctx, reqB.AR.ARUUID, controller.Record{Index: pnio.IndexModuleDiffAR}, 0)
	if err != nil {
		t.Fatal(err)
	}
	blk, _, _ := pnio.ReadBlock(b)
	diff, err := pnio.DecodeModuleDiffBlock(blk)
	if err != nil || len(diff.APIs) == 0 || diff.APIs[0].Modules[0].Submodules[0].State&(0xF<<7) != pnio.SubmStateARInfoCtrlLock {
		t.Fatalf("B's ModuleDiff %+v %v", diff, err)
	}

	if err := pa.release(reqA); err != nil {
		t.Fatal(err)
	}
	next := func() pnio.AlarmNotification {
		t.Helper()
		select {
		case nt := <-released:
			return nt
		case <-time.After(5 * time.Second):
			t.Fatal("no Released alarm")
			return pnio.AlarmNotification{}
		}
	}
	first := next()
	// A wrong sequence number is ignored; the right one is answered.
	if err := prmEndPlug(first.Sequence() + 5); err != nil {
		t.Fatal(err)
	}
	if err := prmEndPlug(first.Sequence()); err != nil {
		t.Fatal(err)
	}
	select {
	case seq := <-readyPlug:
		if seq != first.Sequence() {
			t.Fatalf("ApplicationReady (plug) for %d, want %d", seq, first.Sequence())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ApplicationReady (plug)")
	}
	// The next one is not answered: after the activity timeout the one
	// after it follows.
	second := next()
	start := time.Now()
	third := next()
	if third.Subslot == second.Subslot && third.Slot == second.Slot || time.Since(start) < 400*time.Millisecond {
		t.Fatalf("second %d/%d, third %d/%d after %s", second.Slot, second.Subslot, third.Slot, third.Subslot, time.Since(start))
	}
	if o := d.ownerOf(second.Slot, second.Subslot); o == nil || o.uuid != reqB.AR.ARUUID {
		t.Fatal("an unanswered hand-over did not keep the submodule with B")
	}
}
