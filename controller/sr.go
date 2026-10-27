// SPDX-License-Identifier: MIT

package controller

import (
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rt"
)

// SRSetting makes the device's AR an IOCARSR of an S2 AR set:
// EXPERIMENTAL and not interoperable (see the device package's sr.go).
// The two controllers of a redundant pair give the device the same
// Discriminator and ConfigID and AR numbers 1 and 2; each says with its
// outputs whether its AR is to be primary (DataStatus "Primary Request")
// or backup ("Backup Request"), and Device.SetPrimary switches it.
type SRSetting struct {
	Discriminator [6]byte `json:"discriminator"`
	ConfigID      [8]byte `json:"configId"`
	// ARNumber is 1 or 2.
	ARNumber uint8 `json:"arNumber"`
	// Primary is the initial request.
	Primary bool `json:"primary"`
	// RedundancyDataHoldFactor is the SRInfoBlock's (the device's wait
	// for a primary, × 1 ms on go-profinet devices); zero means 200.
	RedundancyDataHoldFactor uint16 `json:"redundancyDataHoldFactor,omitempty"`
	// InputValidOnBackup asks for valid inputs on the backup AR.
	InputValidOnBackup bool `json:"inputValidOnBackup,omitempty"`
}

func (s *SRSetting) validate() error {
	if s.ARNumber < 1 || s.ARNumber > 2 {
		return fmt.Errorf("controller: %w: AR number %d of an S2 AR set (1 or 2)", profinet.ErrInvalidConfig, s.ARNumber)
	}
	if s.RedundancyDataHoldFactor != 0 && s.RedundancyDataHoldFactor < 3 {
		return fmt.Errorf("controller: %w: RedundancyDataHoldFactor %d (0-2 are reserved)", profinet.ErrInvalidConfig, s.RedundancyDataHoldFactor)
	}
	return nil
}

// srBlocks returns the ARUUID and the SRInfoBlock of an IOCARSR.
func (s *SRSetting) srBlocks() (pnio.ARSetID, pnio.RawBlock, error) {
	id := pnio.ARSetID{Discriminator: s.Discriminator, ConfigID: s.ConfigID, ARNumber: s.ARNumber, Resource: 2}
	f := s.RedundancyDataHoldFactor
	if f == 0 {
		f = 200
	}
	info := pnio.SRInfoBlock{RedundancyDataHoldFactor: f}
	if s.InputValidOnBackup {
		info.Properties |= pnio.SRPropInputValidOnBackupAR
	}
	blk, _, err := pnio.ReadBlock(info.Append(nil))
	return id, blk, err
}

// srDataStatus is the output DataStatus of a primary or backup request.
func srDataStatus(primary bool) rt.DataStatus {
	if primary {
		return rt.PrimaryRequest | rt.ProviderRun | rt.StationOK
	}
	return rt.BackupRequest | rt.ProviderRun | rt.StationOK
}

// SetPrimary makes the device's IOCARSR request to be primary or backup
// (experimental); it fails for a device without SystemRedundancy.
func (d *Device) SetPrimary(primary bool) error {
	if d.cfg.SystemRedundancy == nil {
		return fmt.Errorf("controller: %s: %w: not an IOCARSR", d.cfg.StationName, profinet.ErrInvalidState)
	}
	d.mu.Lock()
	d.srPrimary = primary
	a := d.ar
	d.mu.Unlock()
	if a != nil && a.provider != nil {
		a.provider.SetDataStatus(srDataStatus(primary))
	}
	return nil
}

// RedundancyAck returns the redundancy acknowledge of the device's last
// input frame (DataStatus & rt.RedundancyMask, with the Redundancy bit):
// rt.PrimaryAck on the primary AR, rt.BackupAckWithData or
// rt.BackupAckWithoutData on a backup. ok is false before any frame.
func (d *Device) RedundancyAck() (rt.DataStatus, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.srAck, d.srAckSeen
}
