// SPDX-License-Identifier: MIT

package device

import (
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rt"
)

// System redundancy S2 (IOCARSR): EXPERIMENTAL and off by default.
//
// The wire elements are those Wireshark dissects (ARType 0x0020, the
// ARUUID's AR set selector, the SRInfoBlock, the redundancy meanings of
// DataStatus, abort reasons 38 to 43; docs/research/EVIDENCE-AR-SR-MCR-UDP.md,
// B). The behaviour is what follows from them, not what IEC 61158 or PI's
// guidelines specify, whose texts are not public, and no open
// implementation exists to test it against: it is not interoperable with
// PLCs until it is.
//
// With Config.SystemRedundancy the device accepts the two IOCARSRs of an
// AR set (ARUUIDs that differ only in the AR number, 1 or 2): both own the
// submodules they expect. The outputs are taken from the AR whose output
// frames carry DataStatus "Primary Request"; an AR that sends "Backup
// Request" is a backup. The inputs go to both ARs: "Primary Acknowledge"
// on the primary; on a backup "Backup Acknowledge" with or without data, as
// the SRInfoBlock's InputValidOnBackupAR says, and the Redundancy bit when
// no AR is primary. Without a primary the outputs keep their last values
// for the redundancy data hold time (RedundancyDataHoldFactor × 1 ms, the
// unit p-net's comment gives; not public) and the set's ARs are then
// aborted with reason 38 "IOCARSR RDHT expired". Writes through a backup
// are refused ("AR is not in state Primary") and alarms go to the primary
// only.

// SRConfig enables system redundancy (experimental).
type SRConfig struct {
	// MinDataHoldFactor is the smallest RedundancyDataHoldFactor accepted;
	// zero means 3 (0-2 are reserved).
	MinDataHoldFactor uint16
}

// srSet is an AR set: the IOCARSRs of a redundant controller pair.
type srSet struct {
	key   [14]byte // discriminator and ConfigID
	info  pnio.SRInfoBlock
	mu    sync.Mutex
	ars   map[uint8]*ar // by AR number
	prim  *ar
	rdht  *time.Timer
	since time.Time // when the set lost its primary
}

// srFields are the system redundancy fields of an AR.
type srFields struct {
	set    *srSet
	number uint8
}

// srJoin checks an IOCARSR Connect and puts its AR in its set; the error
// is the Connect's status.
func (d *Device) srJoin(a *ar, req *pnio.ConnectRequest) error {
	fail := func(c1, c2 uint8) error {
		return &pnio.StatusError{Op: "connect", Status: pnio.ServiceStatus(pnio.CodeConnect, c1, c2)}
	}
	var info *pnio.SRInfoBlock
	for _, blk := range req.Other {
		if blk.Header.Type != pnio.BlockSRInfo {
			continue
		}
		v, err := pnio.DecodeSRInfoBlock(blk)
		if err != nil {
			return fail(pnio.Code1SRInfo, 4)
		}
		info = &v
	}
	if info == nil {
		return fail(pnio.Code1CMRPC, pnio.CMRPCUnknownBlocks)
	}
	minF := d.cfg.SystemRedundancy.MinDataHoldFactor
	if minF < 3 {
		minF = 3
	}
	if info.RedundancyDataHoldFactor < minF {
		return fail(pnio.Code1SRInfo, 4)
	}
	if info.Properties&^(pnio.SRPropInputValidOnBackupAR|pnio.SRPropMode) != 0 {
		return fail(pnio.Code1SRInfo, 5)
	}
	id := pnio.SplitARUUID(a.uuid)
	if id.ARNumber < 1 || id.ARNumber > 2 {
		// S2: two ARs per set.
		return fail(pnio.Code1ARBlock, 5)
	}
	var k [14]byte
	copy(k[:6], id.Discriminator[:])
	copy(k[6:], id.ConfigID[:])
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.srSets[k]
	if s == nil {
		s = &srSet{key: k, info: *info, ars: map[uint8]*ar{}}
		d.srSets[k] = s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.ars[id.ARNumber]; o != nil && o != a {
		return fail(pnio.Code1ARBlock, 5)
	}
	if s.info != *info {
		// The ARs of a set agree on the SRInfoBlock.
		return fail(pnio.Code1SRInfo, 5)
	}
	s.ars[id.ARNumber] = a
	a.sr = &srFields{set: s, number: id.ARNumber}
	return nil
}

// sameSet reports whether two ARs belong to one AR set.
func sameSet(a, b *ar) bool {
	return a != nil && b != nil && a.sr != nil && b.sr != nil && a.sr.set == b.sr.set
}

// srOther returns the other live AR of a's set, nil if none.
func (d *Device) srOther(a *ar) *ar {
	if a.sr == nil {
		return nil
	}
	s := a.sr.set
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.ars {
		if o != a {
			return o
		}
	}
	return nil
}

// srOutput follows the redundancy requests in an AR's output frames and
// reports whether its outputs are to be applied: it is the primary.
func (d *Device) srOutput(a *ar, st rt.DataStatus) bool {
	s := a.sr.set
	req := st & rt.RedundancyMask
	s.mu.Lock()
	changed := false
	switch {
	case req == rt.PrimaryRequest && s.prim != a:
		old := s.prim
		s.prim, changed = a, true
		if s.rdht != nil {
			s.rdht.Stop()
			s.rdht = nil
		}
		if old != nil {
			d.log.Infof("device: AR set: AR %d primary instead of AR %d", a.sr.number, old.sr.number)
		} else {
			d.log.Infof("device: AR set: AR %d primary", a.sr.number)
		}
	case req != rt.PrimaryRequest && s.prim == a:
		s.prim, changed = nil, true
		d.srNoPrimaryLocked(s)
	}
	primary := s.prim == a
	s.mu.Unlock()
	if changed {
		d.srStatus(s)
	}
	return primary
}

// srNoPrimaryLocked starts the redundancy data hold time: s.mu held.
func (d *Device) srNoPrimaryLocked(s *srSet) {
	s.since = time.Now()
	if s.rdht != nil {
		s.rdht.Stop()
	}
	hold := time.Duration(s.info.RedundancyDataHoldFactor) * time.Millisecond
	s.rdht = time.AfterFunc(hold, func() { d.srExpire(s) })
}

// srExpire aborts the ARs of a set that found no primary within RDHT.
func (d *Device) srExpire(s *srSet) {
	s.mu.Lock()
	if s.prim != nil {
		s.mu.Unlock()
		return
	}
	var ars []*ar
	for _, a := range s.ars {
		ars = append(ars, a)
	}
	s.mu.Unlock()
	d.log.Warnf("device: AR set: no primary AR for the redundancy data hold time: aborting %d AR(s)", len(ars))
	for _, a := range ars {
		go d.abort(a, pnio.AbortSRRDHTExpired, true)
	}
}

// srStatus sets the input DataStatus of the set's ARs: primary or backup
// acknowledge.
func (d *Device) srStatus(s *srSet) {
	s.mu.Lock()
	prim := s.prim
	valid := s.info.Properties&pnio.SRPropInputValidOnBackupAR != 0
	var ars []*ar
	for _, a := range s.ars {
		ars = append(ars, a)
	}
	s.mu.Unlock()
	for _, a := range ars {
		if a.provider == nil || a.currentState() != ARStateData {
			continue
		}
		st := rt.ProviderRun | rt.StationOK
		switch {
		case a == prim:
			st |= rt.PrimaryAck
		case valid:
			st |= rt.BackupAckWithData
		default:
			st |= rt.BackupAckWithoutData
		}
		if a != prim && prim == nil {
			// No primary AR of the set is present.
			st |= rt.Redundancy
		}
		a.provider.SetDataStatus(st)
	}
}

// srLeave takes a closing AR out of its set: a primary that leaves starts
// the redundancy data hold time; an empty set is forgotten.
func (d *Device) srLeave(a *ar) {
	if a.sr == nil {
		return
	}
	s := a.sr.set
	d.mu.Lock()
	s.mu.Lock()
	if s.ars[a.sr.number] == a {
		delete(s.ars, a.sr.number)
	}
	wasPrim := s.prim == a
	if wasPrim {
		s.prim = nil
	}
	empty := len(s.ars) == 0
	if empty {
		if s.rdht != nil {
			s.rdht.Stop()
		}
		delete(d.srSets, s.key)
	} else if wasPrim {
		d.srNoPrimaryLocked(s)
	}
	s.mu.Unlock()
	d.mu.Unlock()
	if wasPrim && !empty {
		d.log.Infof("device: AR set: primary AR %d gone", a.sr.number)
		d.srStatus(s)
	}
}

// srPrimary reports whether a may act for its set (writes, alarms): it is
// not an IOCARSR, or it is its set's primary.
func srPrimary(a *ar) bool {
	if a.sr == nil {
		return true
	}
	s := a.sr.set
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prim == a
}

// errNotPrimary refuses a write through a backup AR: Wireshark's
// "AR is not in state Primary" (ErrorCode1 204, ErrorCode2 1).
var errNotPrimary = &pnio.StatusError{Op: "write", Status: pnio.Status{Decode: pnio.DecodePNIO, Code1: 0xCC, Code2: 1}}

// SRState describes an AR set (experimental): its ARs by number and the
// primary's number, 0 when none.
type SRState struct {
	ARs     []uint8
	Primary uint8
	// NoPrimaryFor is how long the set has had no primary.
	NoPrimaryFor time.Duration
}

// SystemRedundancy returns the AR sets (experimental).
func (d *Device) SystemRedundancy() []SRState {
	d.mu.Lock()
	sets := make([]*srSet, 0, len(d.srSets))
	for _, s := range d.srSets {
		sets = append(sets, s)
	}
	d.mu.Unlock()
	var out []SRState
	for _, s := range sets {
		s.mu.Lock()
		st := SRState{}
		for n := range s.ars {
			st.ARs = append(st.ARs, n)
		}
		if s.prim != nil {
			st.Primary = s.prim.sr.number
		} else if !s.since.IsZero() {
			st.NoPrimaryFor = time.Since(s.since)
		}
		s.mu.Unlock()
		out = append(out, st)
	}
	return out
}

func (s SRState) String() string {
	return fmt.Sprintf("ARs %v primary %d", s.ARs, s.Primary)
}

// srPrimaryOf returns the primary of a's set, nil when it has none.
func (d *Device) srPrimaryOf(a *ar) *ar {
	s := a.sr.set
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prim
}
