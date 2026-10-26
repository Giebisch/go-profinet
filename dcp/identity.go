// SPDX-License-Identifier: MIT

package dcp

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

// Identity is what a device reports about itself in an Identify or Get
// response: the content of its blocks in typed form.
type Identity struct {
	// MAC is the source address of the response.
	MAC ethernet.MAC `json:"mac"`
	// NameOfStation is the station name, empty when none is set.
	NameOfStation string `json:"nameOfStation"`
	// TypeOfStation is the vendor's name of the device type
	// (Device/Vendor block).
	TypeOfStation string `json:"typeOfStation,omitempty"`
	// VendorID and DeviceID identify the device type.
	VendorID uint16 `json:"vendorId"`
	DeviceID uint16 `json:"deviceId"`
	// Role is the bitmap of Role* values.
	Role uint8 `json:"role"`
	// Instance is the DeviceInstance (high octet first); HasInstance
	// reports whether the device sent one.
	Instance    uint16 `json:"instance,omitempty"`
	HasInstance bool   `json:"-"`
	// OEMVendorID and OEMDeviceID are set when HasOEM is true.
	OEMVendorID uint16 `json:"oemVendorId,omitempty"`
	OEMDeviceID uint16 `json:"oemDeviceId,omitempty"`
	HasOEM      bool   `json:"-"`
	// IP is the IP suite and IPInfo its BlockInfo (IPInfo* bits).
	IP     IPSuite `json:"ip"`
	IPInfo uint16  `json:"ipInfo"`
	// Options lists the blocks the device supports (Device/Options).
	Options []BlockID `json:"options,omitempty"`
	// RSIProperties is the Device/RSIProperties value when HasRSI.
	RSIProperties uint16 `json:"rsiProperties,omitempty"`
	HasRSI        bool   `json:"-"`
	// Alias is the AliasName a device derives from its LLDP neighbor;
	// it is only used to match Identify filters.
	Alias string `json:"-"`
}

// String returns a one-line summary of the identity.
func (id *Identity) String() string {
	return fmt.Sprintf("%s %q vendor 0x%04X device 0x%04X %s", id.MAC, id.NameOfStation, id.VendorID, id.DeviceID, id.IP)
}

// ParseIdentity extracts the identity from the blocks of a response. The
// strings and slices it returns do not alias f.
func ParseIdentity(mac ethernet.MAC, blocks []Block) (Identity, error) {
	id := Identity{MAC: mac}
	for _, b := range blocks {
		d := b.Data
		switch b.ID {
		case DeviceName:
			id.NameOfStation = string(d)
		case DeviceVendor:
			id.TypeOfStation = string(d)
		case DeviceID:
			if len(d) < 4 {
				return id, fmt.Errorf("dcp: %w: DeviceID block of %d octets", profinet.ErrMalformed, len(d))
			}
			id.VendorID = binary.BigEndian.Uint16(d)
			id.DeviceID = binary.BigEndian.Uint16(d[2:])
		case DeviceRole:
			if len(d) < 1 {
				return id, fmt.Errorf("dcp: %w: empty DeviceRole block", profinet.ErrMalformed)
			}
			id.Role = d[0]
		case DeviceInst:
			if len(d) < 2 {
				return id, fmt.Errorf("dcp: %w: DeviceInstance block of %d octets", profinet.ErrMalformed, len(d))
			}
			id.Instance = binary.BigEndian.Uint16(d)
			id.HasInstance = true
		case DeviceOEMID:
			if len(d) < 4 {
				return id, fmt.Errorf("dcp: %w: OEMDeviceID block of %d octets", profinet.ErrMalformed, len(d))
			}
			id.OEMVendorID = binary.BigEndian.Uint16(d)
			id.OEMDeviceID = binary.BigEndian.Uint16(d[2:])
			id.HasOEM = true
		case DeviceOpts:
			id.Options = make([]BlockID, 0, len(d)/2)
			for i := 0; i+1 < len(d); i += 2 {
				id.Options = append(id.Options, BlockID(binary.BigEndian.Uint16(d[i:])))
			}
		case DeviceRSI:
			if len(d) >= 2 {
				id.RSIProperties = binary.BigEndian.Uint16(d)
				id.HasRSI = true
			}
		case IPParameter, IPFullSuite:
			s, err := DecodeIPSuite(d)
			if err != nil {
				return id, err
			}
			id.IP = s
			id.IPInfo = b.Info
		case DeviceAlias:
			id.Alias = string(d)
		default:
		}
	}
	return id, nil
}

// Blocks returns the response blocks that describe the identity, in the
// order devices usually send them. Options is derived from the blocks
// when it is empty.
func (id *Identity) Blocks() []Block {
	var bs []Block
	add := func(b BlockID, info uint16, data []byte) {
		bs = append(bs, Block{ID: b, HasInfo: true, Info: info, Data: data})
	}
	add(DeviceVendor, 0, []byte(id.TypeOfStation))
	add(DeviceName, 0, []byte(id.NameOfStation))
	add(DeviceID, 0, binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, id.VendorID), id.DeviceID))
	add(DeviceRole, 0, []byte{id.Role, 0})
	if id.HasInstance {
		add(DeviceInst, 0, binary.BigEndian.AppendUint16(nil, id.Instance))
	}
	if id.HasOEM {
		add(DeviceOEMID, 0, binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, id.OEMVendorID), id.OEMDeviceID))
	}
	if id.HasRSI {
		add(DeviceRSI, 0, binary.BigEndian.AppendUint16(nil, id.RSIProperties))
	}
	add(IPParameter, id.IPInfo, AppendIPSuite(nil, id.IP))
	opts := id.Options
	if len(opts) == 0 {
		opts = []BlockID{IPMAC, IPParameter}
		for _, b := range bs {
			if b.ID.Option() == OptionDeviceProperties {
				opts = append(opts, b.ID)
			}
		}
		opts = append(opts, CtrlStart, CtrlStop, CtrlSignal, CtrlResponse, CtrlReset)
	}
	var od []byte
	for _, o := range opts {
		od = binary.BigEndian.AppendUint16(od, uint16(o))
	}
	// Device/Options goes before the IP block, as most devices order it.
	opt := Block{ID: DeviceOpts, HasInfo: true, Data: od}
	out := make([]Block, 0, len(bs)+1)
	for _, b := range bs {
		if b.ID == IPParameter {
			out = append(out, opt)
		}
		out = append(out, b)
	}
	return out
}

// Block returns the response block for one block ID, and false when the
// identity has no such block.
func (id *Identity) Block(b BlockID) (Block, bool) {
	if b == IPMAC {
		return Block{ID: IPMAC, HasInfo: true, Data: append([]byte(nil), id.MAC[:]...)}, true
	}
	for _, blk := range id.Blocks() {
		if blk.ID == b {
			return blk, true
		}
	}
	return Block{}, false
}

// ErrInvalidName reports a station name that does not follow the rules of
// IEC 61158-6-10 for NameOfStation.
var ErrInvalidName = fmt.Errorf("dcp: %w: invalid NameOfStation", profinet.ErrInvalidConfig)

// ValidateStationName checks a NameOfStation: 1 to 240 octets of labels
// separated by '.', each label 1 to 63 octets of a-z, 0-9 and '-', not
// beginning or ending with '-'; the name is not of the form n.n.n.n and
// does not begin with "port-xyz" or "port-xyz-abcde" (x..e digits), and
// labels do not begin with "xn-".
func ValidateStationName(name string) error {
	if len(name) == 0 || len(name) > 240 {
		return fmt.Errorf("%w: length %d (1..240)", ErrInvalidName, len(name))
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 {
			return fmt.Errorf("%w: label %q of length %d (1..63)", ErrInvalidName, l, len(l))
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("%w: label %q begins or ends with '-'", ErrInvalidName, l)
		}
		if strings.HasPrefix(l, "xn-") {
			return fmt.Errorf("%w: label %q begins with \"xn-\"", ErrInvalidName, l)
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return fmt.Errorf("%w: character %q in label %q", ErrInvalidName, c, l)
			}
		}
	}
	if len(labels) == 4 {
		numeric := true
		for _, l := range labels {
			if _, err := strconv.Atoi(l); err != nil {
				numeric = false
				break
			}
		}
		if numeric {
			return fmt.Errorf("%w: %q has the form of an IP address", ErrInvalidName, name)
		}
	}
	if isPortName(labels[0]) {
		return fmt.Errorf("%w: %q begins with a port name", ErrInvalidName, name)
	}
	return nil
}

// isPortName reports whether l is "port-xyz" or "port-xyz-abcde".
func isPortName(l string) bool {
	digits := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
	if !strings.HasPrefix(l, "port-") || len(l) < 8 || !digits(l[5:8]) {
		return false
	}
	switch {
	case len(l) == 8:
		return true
	case len(l) == 14 && l[8] == '-' && digits(l[9:14]):
		return true
	default:
		return false
	}
}
