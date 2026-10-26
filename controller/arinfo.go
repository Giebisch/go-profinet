// SPDX-License-Identifier: MIT

package controller

import (
	"time"

	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/rt"
)

// ARInfo describes the established AR of a device: what Connect
// negotiated.
type ARInfo struct {
	UUID       rpc.UUID     `json:"arUUID"`
	SessionKey uint16       `json:"sessionKey"`
	Type       pnio.ARType  `json:"arType"`
	RTClass    pnio.RTClass `json:"rtClass"`
	// Advanced reports the advanced startup mode.
	Advanced bool `json:"advanced"`
	// DeviceAddr is the device's RPC endpoint.
	DeviceAddr string `json:"deviceAddr"`
	// LocalAlarmRef and RemoteAlarmRef are the alarm references of the
	// controller and of the device.
	LocalAlarmRef  uint16 `json:"localAlarmRef"`
	RemoteAlarmRef uint16 `json:"remoteAlarmRef"`
	// Input and Output are the two IOCRs.
	Input  IOCRInfo `json:"input"`
	Output IOCRInfo `json:"output"`
}

// IOCRInfo describes one IOCR of an AR.
type IOCRInfo struct {
	Type    pnio.IOCRType `json:"type"`
	FrameID uint16        `json:"frameId"`
	// DataLength is the length of the C_SDU.
	DataLength      uint16 `json:"dataLength"`
	SendClockFactor uint16 `json:"sendClockFactor"`
	ReductionRatio  uint16 `json:"reductionRatio"`
	WatchdogFactor  uint16 `json:"watchdogFactor"`
	DataHoldFactor  uint16 `json:"dataHoldFactor"`
	// Cycle is SendClockFactor × 31.25 µs × ReductionRatio.
	Cycle time.Duration `json:"cycle"`
	// Objects are the data objects of the C_SDU and IOCS the consumer
	// statuses it carries, at their offsets.
	Objects []rt.Object `json:"objects"`
	IOCS    []rt.Status `json:"iocs"`
}

// AR returns the description of the device's current AR; false when none
// is established.
func (d *Device) AR() (ARInfo, bool) {
	d.mu.Lock()
	a := d.ar
	d.mu.Unlock()
	if a == nil {
		return ARInfo{}, false
	}
	cfg := &d.cfg
	cr := func(t pnio.IOCRType, id uint16, l rt.Layout) IOCRInfo {
		return IOCRInfo{
			Type: t, FrameID: id, DataLength: l.DataLength,
			SendClockFactor: cfg.SendClockFactor, ReductionRatio: cfg.ReductionRatio,
			WatchdogFactor: cfg.WatchdogFactor, DataHoldFactor: cfg.DataHoldFactor,
			Cycle:   time.Duration(cfg.SendClockFactor) * time.Duration(cfg.ReductionRatio) * 31250 * time.Nanosecond,
			Objects: append([]rt.Object(nil), l.Objects...), IOCS: append([]rt.Status(nil), l.IOCS...),
		}
	}
	addr := ""
	if a.addr != nil {
		addr = a.addr.String()
	}
	return ARInfo{
		UUID: a.uuid, SessionKey: a.sessionKey, Type: pnio.ARTypeIOCARSingle, RTClass: a.class, Advanced: cfg.Advanced,
		DeviceAddr: addr, LocalAlarmRef: a.localRef, RemoteAlarmRef: a.remoteRef,
		Input:  cr(pnio.IOCRInput, a.inFrameID, a.inLayout),
		Output: cr(pnio.IOCROutput, a.outFrameID, a.outLayout),
	}, true
}
