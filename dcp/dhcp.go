// SPDX-License-Identifier: MIT

package dcp

import (
	"fmt"

	"github.com/otfabric/go-profinet"
)

// DCP option 3 (DHCP). Its suboptions are DHCP option codes, and the value
// of each block is a DHCP option: the option code, the parameter length
// and the parameter data. Wireshark's DCP dissector and p-net agree on
// the codes; Wireshark decodes the data of the client identifier (61) and
// of the control suboption (255) and shows the others as undecoded
// (docs/research/EVIDENCE-MRP-PTCP-MGMT.md, D).
//
// go-profinet has no DHCP client: a device reports the DHCP options it is
// configured with, answers Get of them and passes a Set to the
// application, which decides whether and how the operating system uses
// DHCP. Addressing stays with the operating system.

// DHCP option codes used as suboptions of option 3.
const (
	DHCPOptHostName         uint8 = 12
	DHCPOptVendorSpecific   uint8 = 43
	DHCPOptServerID         uint8 = 54
	DHCPOptParamRequestList uint8 = 55
	DHCPOptClassID          uint8 = 60
	DHCPOptClientID         uint8 = 61
	DHCPOptFQDN             uint8 = 81
	DHCPOptUUIDClient       uint8 = 97
	// DHCPOptControl is "Control DHCP for address resolution" (the END
	// code of DHCP, reused).
	DHCPOptControl uint8 = 255
)

// Parameter data of the control suboption (255).
const (
	// DHCPControlOff: do not use DHCP (the default).
	DHCPControlOff uint8 = 0
	// DHCPControlOffReset: do not use DHCP, reset the DHCP options to
	// their factory values.
	DHCPControlOffReset uint8 = 1
	// DHCPControlOn: use DHCP with the given set of DHCP options.
	DHCPControlOn uint8 = 2
)

// DHCPClientIDKind says how a client identifier (suboption 61) is formed.
type DHCPClientIDKind uint8

// Client identifier kinds: with one octet of parameter data, 0x01 means
// the MAC address and any other value the NameOfStation; longer data is an
// arbitrary client identifier (a type octet and an octet string).
const (
	ClientIDMAC DHCPClientIDKind = iota + 1
	ClientIDNameOfStation
	ClientIDArbitrary
)

func (k DHCPClientIDKind) String() string {
	switch k {
	case ClientIDMAC:
		return "MAC address"
	case ClientIDNameOfStation:
		return "NameOfStation"
	case ClientIDArbitrary:
		return "arbitrary"
	default:
		return fmt.Sprintf("ClientIDKind(%d)", uint8(k))
	}
}

// DHCPParameter is the value of a DHCP block: a DHCP option. Code is the
// option code, which is also the block's suboption.
type DHCPParameter struct {
	Code uint8  `json:"code"`
	Data []byte `json:"data,omitempty"`
}

// BlockID returns the DCP block ID of the parameter (option 3, the code as
// suboption).
func (p DHCPParameter) BlockID() BlockID { return NewBlockID(OptionDHCP, p.Code) }

// Validate checks that the parameter can be encoded: at most 255 octets
// of data.
func (p DHCPParameter) Validate() error {
	if len(p.Data) > 0xFF {
		return fmt.Errorf("dcp: %w: DHCP option %d with %d octets of data (at most 255)", profinet.ErrInvalidConfig, p.Code, len(p.Data))
	}
	return nil
}

// String formats the parameter.
func (p DHCPParameter) String() string {
	switch {
	case p.Code == DHCPOptControl && len(p.Data) == 1:
		return fmt.Sprintf("DHCP control %d", p.Data[0])
	case p.Code == DHCPOptClientID:
		if k, id, ok := p.ClientID(); ok {
			if k == ClientIDArbitrary {
				return fmt.Sprintf("DHCP client ID %s %x", k, id)
			}
			return "DHCP client ID " + k.String()
		}
	}
	return fmt.Sprintf("DHCP option %d %x", p.Code, p.Data)
}

// AppendDHCPParameter appends the value of a DHCP block: code, length and
// data.
func AppendDHCPParameter(dst []byte, p DHCPParameter) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return dst, err
	}
	dst = append(dst, p.Code, byte(len(p.Data)))
	return append(dst, p.Data...), nil
}

// DecodeDHCPParameter decodes the value of a DHCP block. Data aliases b.
func DecodeDHCPParameter(b []byte) (DHCPParameter, error) {
	if len(b) < 2 {
		return DHCPParameter{}, fmt.Errorf("dcp: %w: DHCP block of %d octets", profinet.ErrMalformed, len(b))
	}
	if n := int(b[1]); n != len(b)-2 {
		return DHCPParameter{}, fmt.Errorf("dcp: %w: DHCP option %d of length %d in %d octets", profinet.ErrMalformed, b[0], n, len(b)-2)
	}
	return DHCPParameter{Code: b[0], Data: b[2:]}, nil
}

// DHCPClientIDFromMAC is the client identifier "use the MAC address".
func DHCPClientIDFromMAC() DHCPParameter {
	return DHCPParameter{Code: DHCPOptClientID, Data: []byte{0x01}}
}

// DHCPClientIDArbitrary is an arbitrary client identifier: a type octet
// and the identifier (at least one octet, at most 254).
func DHCPClientIDArbitrary(typ uint8, id []byte) (DHCPParameter, error) {
	if len(id) == 0 || len(id) > 0xFE {
		return DHCPParameter{}, fmt.Errorf("dcp: %w: arbitrary client ID of %d octets (1..254)", profinet.ErrInvalidConfig, len(id))
	}
	return DHCPParameter{Code: DHCPOptClientID, Data: append([]byte{typ}, id...)}, nil
}

// DHCPControlParameter is the control suboption with the given value
// (DHCPControlOff, DHCPControlOffReset, DHCPControlOn).
func DHCPControlParameter(v uint8) DHCPParameter {
	return DHCPParameter{Code: DHCPOptControl, Data: []byte{v}}
}

// ClientID interprets a client identifier parameter: its kind and, for an
// arbitrary one, the identifier after the type octet. ok is false for
// another code or no data.
func (p DHCPParameter) ClientID() (kind DHCPClientIDKind, id []byte, ok bool) {
	if p.Code != DHCPOptClientID || len(p.Data) == 0 {
		return 0, nil, false
	}
	switch {
	case len(p.Data) > 1:
		return ClientIDArbitrary, p.Data[1:], true
	case p.Data[0] == 0x01:
		return ClientIDMAC, nil, true
	default:
		return ClientIDNameOfStation, nil, true
	}
}
