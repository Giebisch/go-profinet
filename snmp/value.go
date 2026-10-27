// SPDX-License-Identifier: MIT

package snmp

import (
	"fmt"
	"net/netip"

	"github.com/otfabric/go-profinet"
)

// Type is the BER tag of a value (RFC 2578 section 7.1, RFC 3416 section
// 3).
type Type byte

// The value types. Integer, OctetString, Null and ObjectIdentifier are
// universal types; IPAddress to Counter64 are the application types of
// SNMPv2-SMI (RFC 2578 section 7.1.5 to 7.1.10, tags [APPLICATION 0] to
// [APPLICATION 6]); NoSuchObject, NoSuchInstance and EndOfMibView are the
// exceptions of a v2c response, implicitly tagged NULLs [0], [1], [2]
// (RFC 3416 section 3).
const (
	TypeInteger          Type = 0x02
	TypeOctetString      Type = 0x04
	TypeNull             Type = 0x05
	TypeObjectIdentifier Type = 0x06
	TypeIPAddress        Type = 0x40
	TypeCounter32        Type = 0x41
	TypeGauge32          Type = 0x42
	TypeTimeTicks        Type = 0x43
	TypeCounter64        Type = 0x46
	TypeNoSuchObject     Type = 0x80
	TypeNoSuchInstance   Type = 0x81
	TypeEndOfMibView     Type = 0x82
)

func (t Type) String() string {
	switch t {
	case TypeInteger:
		return "INTEGER"
	case TypeOctetString:
		return "OCTET STRING"
	case TypeNull:
		return "NULL"
	case TypeObjectIdentifier:
		return "OBJECT IDENTIFIER"
	case TypeIPAddress:
		return "IpAddress"
	case TypeCounter32:
		return "Counter32"
	case TypeGauge32:
		return "Gauge32"
	case TypeTimeTicks:
		return "TimeTicks"
	case TypeCounter64:
		return "Counter64"
	case TypeNoSuchObject:
		return "noSuchObject"
	case TypeNoSuchInstance:
		return "noSuchInstance"
	case TypeEndOfMibView:
		return "endOfMibView"
	default:
		return fmt.Sprintf("Type(0x%02x)", byte(t))
	}
}

// IsException reports whether t is one of the v2c exceptions.
func (t Type) IsException() bool {
	return t == TypeNoSuchObject || t == TypeNoSuchInstance || t == TypeEndOfMibView
}

// Value is the value of a variable binding. The zero Value (Type 0) is
// "no value": a provider returns it for an instance that does not exist.
type Value struct {
	Type Type
	// Int holds an INTEGER.
	Int int64
	// Uint holds Counter32, Gauge32, TimeTicks and Counter64.
	Uint uint64
	// Bytes holds an OCTET STRING and the four octets of an IpAddress.
	Bytes []byte
	// OID holds an OBJECT IDENTIFIER.
	OID OID
}

// Integer returns an INTEGER (Integer32) value.
func Integer(v int32) Value { return Value{Type: TypeInteger, Int: int64(v)} }

// OctetString returns an OCTET STRING value.
func OctetString(b []byte) Value { return Value{Type: TypeOctetString, Bytes: b} }

// String returns an OCTET STRING value of s (DisplayString,
// SnmpAdminString).
func String(s string) Value { return Value{Type: TypeOctetString, Bytes: []byte(s)} }

// Null returns a NULL value, the value of the variable bindings of a
// request.
func Null() Value { return Value{Type: TypeNull} }

// ObjectIdentifier returns an OBJECT IDENTIFIER value.
func ObjectIdentifier(o OID) Value { return Value{Type: TypeObjectIdentifier, OID: o} }

// IPAddress returns an IpAddress value; a is an IPv4 address.
func IPAddress(a netip.Addr) Value {
	b := a.Unmap().As4()
	return Value{Type: TypeIPAddress, Bytes: b[:]}
}

// Counter32 returns a Counter32 value.
func Counter32(v uint32) Value { return Value{Type: TypeCounter32, Uint: uint64(v)} }

// Gauge32 returns a Gauge32 value.
func Gauge32(v uint32) Value { return Value{Type: TypeGauge32, Uint: uint64(v)} }

// TimeTicks returns a TimeTicks value in hundredths of a second.
func TimeTicks(v uint32) Value { return Value{Type: TypeTimeTicks, Uint: uint64(v)} }

// Counter64 returns a Counter64 value.
func Counter64(v uint64) Value { return Value{Type: TypeCounter64, Uint: v} }

func (v Value) String() string {
	switch v.Type {
	case TypeInteger:
		return fmt.Sprintf("INTEGER: %d", v.Int)
	case TypeOctetString:
		return fmt.Sprintf("OCTET STRING: %q", v.Bytes)
	case TypeObjectIdentifier:
		return "OID: " + v.OID.String()
	case TypeIPAddress:
		if a, ok := netip.AddrFromSlice(v.Bytes); ok {
			return "IpAddress: " + a.String()
		}
		return "IpAddress: invalid"
	case TypeCounter32, TypeGauge32, TypeTimeTicks, TypeCounter64:
		return fmt.Sprintf("%s: %d", v.Type, v.Uint)
	default:
		return v.Type.String()
	}
}

// valid checks that v can be encoded.
func (v Value) valid() error {
	switch v.Type {
	case TypeInteger:
		if v.Int < -1<<31 || v.Int > 1<<31-1 {
			return fmt.Errorf("snmp: %w: INTEGER %d exceeds Integer32", profinet.ErrInvalidConfig, v.Int)
		}
	case TypeOctetString, TypeNull, TypeCounter64, TypeNoSuchObject, TypeNoSuchInstance, TypeEndOfMibView:
	case TypeObjectIdentifier:
		return v.OID.validate()
	case TypeIPAddress:
		if len(v.Bytes) != 4 {
			return fmt.Errorf("snmp: %w: IpAddress of %d octets", profinet.ErrInvalidConfig, len(v.Bytes))
		}
	case TypeCounter32, TypeGauge32, TypeTimeTicks:
		if v.Uint > 0xFFFFFFFF {
			return fmt.Errorf("snmp: %w: %s %d exceeds 32 bits", profinet.ErrInvalidConfig, v.Type, v.Uint)
		}
	default:
		return fmt.Errorf("snmp: %w: value type %s", profinet.ErrInvalidConfig, v.Type)
	}
	return nil
}

// appendValue appends the TLV of a valid v.
func appendValue(b []byte, v Value) []byte {
	var tmp [9]byte
	switch v.Type {
	case TypeInteger:
		return appendTLV(b, byte(v.Type), appendInt(tmp[:0], v.Int))
	case TypeOctetString, TypeIPAddress:
		return appendTLV(b, byte(v.Type), v.Bytes)
	case TypeObjectIdentifier:
		b = appendLength(append(b, byte(v.Type)), oidContentLen(v.OID))
		return appendOIDContent(b, v.OID)
	case TypeCounter32, TypeGauge32, TypeTimeTicks, TypeCounter64:
		return appendTLV(b, byte(v.Type), appendUint(tmp[:0], v.Uint))
	default:
		// NULL and the exceptions.
		return append(b, byte(v.Type), 0)
	}
}

// valueLen is the size of the TLV of a valid v.
func valueLen(v Value) int {
	var tmp [9]byte
	switch v.Type {
	case TypeInteger:
		return tlvLen(len(appendInt(tmp[:0], v.Int)))
	case TypeOctetString, TypeIPAddress:
		return tlvLen(len(v.Bytes))
	case TypeObjectIdentifier:
		return tlvLen(oidContentLen(v.OID))
	case TypeCounter32, TypeGauge32, TypeTimeTicks, TypeCounter64:
		return tlvLen(len(appendUint(tmp[:0], v.Uint)))
	default:
		return 2
	}
}

// decodeValue decodes the value of a variable binding. Content octets of
// OCTET STRINGs are copied: a decoded message does not keep the input
// alive.
func decodeValue(tag byte, c []byte) (Value, error) {
	v := Value{Type: Type(tag)}
	switch v.Type {
	case TypeInteger:
		i, err := decodeInt32(c)
		v.Int = int64(i)
		return v, err
	case TypeOctetString:
		v.Bytes = append([]byte{}, c...)
	case TypeNull, TypeNoSuchObject, TypeNoSuchInstance, TypeEndOfMibView:
		if len(c) != 0 {
			return Value{}, malformed("%s with %d content octets", v.Type, len(c))
		}
	case TypeObjectIdentifier:
		o, err := decodeOID(c)
		v.OID = o
		return v, err
	case TypeIPAddress:
		if len(c) != 4 {
			return Value{}, malformed("IpAddress of %d octets", len(c))
		}
		v.Bytes = append([]byte{}, c...)
	case TypeCounter32, TypeGauge32, TypeTimeTicks:
		u, err := decodeUint(c, 32)
		v.Uint = u
		return v, err
	case TypeCounter64:
		u, err := decodeUint(c, 64)
		v.Uint = u
		return v, err
	default:
		return Value{}, malformed("value type 0x%02x", tag)
	}
	return v, nil
}
