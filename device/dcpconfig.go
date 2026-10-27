// SPDX-License-Identifier: MIT

package device

import (
	"fmt"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/dcp"
)

// validateDHCP checks the configured DHCP options: each encodable, no code
// twice.
func validateDHCP(opts []dcp.DHCPParameter) error {
	seen := map[uint8]bool{}
	for _, p := range opts {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("device: %w", err)
		}
		if seen[p.Code] {
			return fmt.Errorf("device: %w: DHCP option %d configured twice", profinet.ErrInvalidConfig, p.Code)
		}
		seen[p.Code] = true
	}
	return nil
}

// cloneDHCP copies DHCP options, data included.
func cloneDHCP(opts []dcp.DHCPParameter) []dcp.DHCPParameter {
	if len(opts) == 0 {
		return nil
	}
	out := make([]dcp.DHCPParameter, len(opts))
	for i, p := range opts {
		out[i] = dcp.DHCPParameter{Code: p.Code, Data: append([]byte(nil), p.Data...)}
	}
	return out
}

// SetDHCP passes a DCP Set of a DHCP option to Config.SetDHCP and reports
// the new value from then on. Without Config.SetDHCP the Set is refused.
func (s station) SetDHCP(p dcp.DHCPParameter, permanent bool) dcp.BlockError {
	d := s.d
	if d.cfg.SetDHCP == nil {
		return dcp.BlockErrorOptionUnsupported
	}
	if permanent {
		if err := d.persist(func(st *persisted) { st.DHCP = setDHCP(st.DHCP, p) }); err != nil {
			return dcp.BlockErrorResource
		}
	}
	if code := d.cfg.SetDHCP(p, permanent); code != dcp.BlockErrorNone {
		return code
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dhcp = setDHCP(d.dhcp, p)
	return dcp.BlockErrorNone
}

// DHCP returns the DHCP options the device reports in DCP.
func (d *Device) DHCP() []dcp.DHCPParameter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cloneDHCP(d.dhcp)
}
