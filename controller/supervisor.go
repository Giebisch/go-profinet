// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
)

// SupervisorConfig configures a device access AR.
type SupervisorConfig struct {
	// InitiatorMAC is the tool's MAC address; required.
	InitiatorMAC ethernet.MAC
	// InitiatorObject is the tool's PNIO object UUID (pnio.ObjectUUID);
	// required. Nothing calls it: a device access AR has no
	// ApplicationReady.
	InitiatorObject rpc.UUID
	// StationName is the tool's name in the AR. Empty means
	// "go-profinet-supervisor".
	StationName string
	// ActivityTimeout is the CM initiator activity timeout after which
	// the device closes an AR the tool left behind; zero means 10 s.
	ActivityTimeout time.Duration
}

// SupervisorAR is a device access AR: an IOSAR with the DeviceAccess
// property, through which an engineering tool reads and writes records
// of a device without IO data, alarms or parameterization, and without
// the expected configuration of an IOCAR. Devices that do not support it
// reject the Connect.
type SupervisorAR struct {
	rc       *RecordClient
	uuid     rpc.UUID
	key      uint16
	released atomic.Bool
}

// OpenSupervisor establishes a device access AR with the device of r.
func (r *RecordClient) OpenSupervisor(ctx context.Context, cfg SupervisorConfig) (*SupervisorAR, error) {
	if !pnio.IsObjectUUID(cfg.InitiatorObject) {
		return nil, fmt.Errorf("controller: %w: supervisor AR without a PNIO object UUID", profinet.ErrInvalidConfig)
	}
	if cfg.StationName == "" {
		cfg.StationName = "go-profinet-supervisor"
	}
	if cfg.ActivityTimeout == 0 {
		cfg.ActivityTimeout = 10 * time.Second
	}
	s := &SupervisorAR{rc: r, uuid: rpc.NewRandomUUID(), key: uint16(sessionKeys.Add(1))}
	req := &pnio.ConnectRequest{AR: pnio.ARBlockReq{
		Type: pnio.ARTypeIOSAR, ARUUID: s.uuid, SessionKey: s.key,
		InitiatorMAC: cfg.InitiatorMAC, InitiatorObject: cfg.InitiatorObject,
		Properties:            pnio.ARPropStateActive | pnio.ARPropDeviceAccess,
		ActivityTimeoutFactor: uint16(min(max(cfg.ActivityTimeout/(100*time.Millisecond), 1), 1000)),
		UDPRTPort:             pnio.LT, StationName: cfg.StationName,
	}}
	resp, err := r.Connect(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.AR.ARUUID != s.uuid {
		return nil, fmt.Errorf("controller: supervisor connect: %w: response for another AR", profinet.ErrMalformed)
	}
	return s, nil
}

// UUID returns the AR UUID.
func (s *SupervisorAR) UUID() rpc.UUID { return s.uuid }

// SessionKey returns the session key of the AR.
func (s *SupervisorAR) SessionKey() uint16 { return s.key }

// Read reads a record within the AR.
func (s *SupervisorAR) Read(ctx context.Context, rec Record, maxLen uint32) ([]byte, error) {
	return s.rc.Read(ctx, s.uuid, rec, maxLen)
}

// Write writes a record within the AR.
//
// Write changes the configuration of an industrial device. Only call it on
// an explicit request of the user.
func (s *SupervisorAR) Write(ctx context.Context, rec Record, data []byte) error {
	return s.rc.Write(ctx, s.uuid, rec, data)
}

// WriteMultiple writes several records in one call.
func (s *SupervisorAR) WriteMultiple(ctx context.Context, items []WriteItem) ([]pnio.Status, error) {
	return s.rc.WriteMultiple(ctx, s.uuid, items)
}

// Release releases the AR. Releasing it again does nothing.
func (s *SupervisorAR) Release(ctx context.Context) error {
	if s.released.Swap(true) {
		return nil
	}
	return s.rc.Control(ctx, pnio.ControlBlock{Type: pnio.BlockReleaseReq, ARUUID: s.uuid, SessionKey: s.key, Command: pnio.CmdRelease})
}
