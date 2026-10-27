// SPDX-License-Identifier: MIT

package snmp

import (
	"fmt"

	"github.com/otfabric/go-profinet"
)

// Version is the version field of a community-based message.
type Version int32

// The versions this package encodes and decodes: SNMPv1 (RFC 1157
// section 4: version-1(0)) and SNMPv2c (RFC 1901 section 3: version(1)).
const (
	Version1  Version = 0
	Version2c Version = 1
)

func (v Version) String() string {
	switch v {
	case Version1:
		return "v1"
	case Version2c:
		return "v2c"
	default:
		return fmt.Sprintf("Version(%d)", int32(v))
	}
}

// VersionError reports a message whose version is neither v1 nor v2c
// (SNMPv3 is 3). It matches profinet.ErrUnsupported.
type VersionError struct{ Version int32 }

func (e *VersionError) Error() string { return fmt.Sprintf("snmp: unsupported version %d", e.Version) }

// Unwrap returns profinet.ErrUnsupported.
func (e *VersionError) Unwrap() error { return profinet.ErrUnsupported }

// PDUType is the context-specific tag of a PDU.
type PDUType byte

// The PDU types (RFC 1157 section 4.1; RFC 3416 section 3).
const (
	PDUGetRequest     PDUType = 0xA0
	PDUGetNextRequest PDUType = 0xA1
	PDUResponse       PDUType = 0xA2 // GetResponse in SNMPv1
	PDUSetRequest     PDUType = 0xA3
	PDUTrapV1         PDUType = 0xA4
	PDUGetBulkRequest PDUType = 0xA5
	PDUInformRequest  PDUType = 0xA6
	PDUTrapV2         PDUType = 0xA7
	PDUReport         PDUType = 0xA8
)

func (t PDUType) String() string {
	switch t {
	case PDUGetRequest:
		return "GetRequest"
	case PDUGetNextRequest:
		return "GetNextRequest"
	case PDUResponse:
		return "Response"
	case PDUSetRequest:
		return "SetRequest"
	case PDUTrapV1:
		return "Trap"
	case PDUGetBulkRequest:
		return "GetBulkRequest"
	case PDUInformRequest:
		return "InformRequest"
	case PDUTrapV2:
		return "SNMPv2-Trap"
	case PDUReport:
		return "Report"
	default:
		return fmt.Sprintf("PDUType(0x%02x)", byte(t))
	}
}

// ErrorStatus is the error-status of a response.
type ErrorStatus int32

// The error-status values: noError to genErr are SNMPv1's (RFC 1157
// section 4.1.1); the others were added by SNMPv2 (RFC 3416 section 3).
const (
	NoError             ErrorStatus = 0
	TooBig              ErrorStatus = 1
	NoSuchName          ErrorStatus = 2
	BadValue            ErrorStatus = 3
	ReadOnly            ErrorStatus = 4
	GenErr              ErrorStatus = 5
	NoAccess            ErrorStatus = 6
	WrongType           ErrorStatus = 7
	WrongLength         ErrorStatus = 8
	WrongEncoding       ErrorStatus = 9
	WrongValue          ErrorStatus = 10
	NoCreation          ErrorStatus = 11
	InconsistentValue   ErrorStatus = 12
	ResourceUnavailable ErrorStatus = 13
	CommitFailed        ErrorStatus = 14
	UndoFailed          ErrorStatus = 15
	AuthorizationError  ErrorStatus = 16
	NotWritable         ErrorStatus = 17
	InconsistentName    ErrorStatus = 18
)

var errorStatusNames = [...]string{"noError", "tooBig", "noSuchName", "badValue", "readOnly", "genErr", "noAccess",
	"wrongType", "wrongLength", "wrongEncoding", "wrongValue", "noCreation", "inconsistentValue",
	"resourceUnavailable", "commitFailed", "undoFailed", "authorizationError", "notWritable", "inconsistentName"}

func (s ErrorStatus) String() string {
	if s >= 0 && int(s) < len(errorStatusNames) {
		return errorStatusNames[s]
	}
	return fmt.Sprintf("ErrorStatus(%d)", int32(s))
}

// VarBind is a variable binding: a name and its value.
type VarBind struct {
	Name  OID
	Value Value
}

// PDU is a GetRequest, GetNextRequest, Response, SetRequest,
// GetBulkRequest, InformRequest, SNMPv2-Trap or Report PDU; they share
// one layout (RFC 3416 section 3). A GetBulkRequest carries
// non-repeaters and max-repetitions where the others carry error-status
// and error-index.
type PDU struct {
	Type      PDUType
	RequestID int32
	// ErrorStatus and ErrorIndex for every type but GetBulkRequest.
	ErrorStatus ErrorStatus
	ErrorIndex  int32
	// NonRepeaters and MaxRepetitions for GetBulkRequest.
	NonRepeaters   int32
	MaxRepetitions int32
	VarBinds       []VarBind
}

// Message is a community-based SNMP message (RFC 1157 section 4, RFC 1901
// section 3).
type Message struct {
	Version   Version
	Community []byte
	PDU       PDU
}

// MaxVarBinds bounds the variable bindings Decode accepts in one PDU.
const MaxVarBinds = 512

// MaxMessageSize is the largest message Decode accepts: the largest UDP
// payload of IPv4.
const MaxMessageSize = 65507

// fields returns the two INTEGER fields after request-id.
func (p *PDU) fields() (int32, int32) {
	if p.Type == PDUGetBulkRequest {
		return p.NonRepeaters, p.MaxRepetitions
	}
	return int32(p.ErrorStatus), p.ErrorIndex
}

// varBindLen is the size of the TLV of a variable binding.
func varBindLen(vb *VarBind) int {
	return tlvLen(tlvLen(oidContentLen(vb.Name)) + valueLen(vb.Value))
}

// appendVarBind appends the TLV of a variable binding.
func appendVarBind(b []byte, vb *VarBind) []byte {
	n := oidContentLen(vb.Name)
	b = appendLength(append(b, tagSequence), tlvLen(n)+valueLen(vb.Value))
	b = appendOIDContent(appendLength(append(b, tagOID), n), vb.Name)
	return appendValue(b, vb.Value)
}

// messageLen is the size of a message with the header of m and variable
// bindings whose TLVs take vbLen octets.
func (m *Message) messageLen(vbLen int) int {
	f1, f2 := m.PDU.fields()
	pdu := tlvLen(intTLVLen(int64(m.PDU.RequestID)) + intTLVLen(int64(f1)) + intTLVLen(int64(f2)) + tlvLen(vbLen))
	return tlvLen(intTLVLen(int64(m.Version)) + tlvLen(len(m.Community)) + pdu)
}

// check validates the message before it is encoded.
func (m *Message) check() error {
	if m.Version != Version1 && m.Version != Version2c {
		return fmt.Errorf("snmp: %w: version %d", profinet.ErrInvalidConfig, m.Version)
	}
	switch m.PDU.Type {
	case PDUGetRequest, PDUGetNextRequest, PDUResponse, PDUSetRequest, PDUGetBulkRequest, PDUInformRequest, PDUTrapV2, PDUReport:
	default:
		return fmt.Errorf("snmp: %w: PDU type %s cannot be encoded", profinet.ErrInvalidConfig, m.PDU.Type)
	}
	for i := range m.PDU.VarBinds {
		vb := &m.PDU.VarBinds[i]
		if err := vb.Name.validate(); err != nil {
			return err
		}
		if err := vb.Value.valid(); err != nil {
			return err
		}
	}
	return nil
}

// AppendTo appends the BER encoding of m to dst.
func (m *Message) AppendTo(dst []byte) ([]byte, error) {
	if err := m.check(); err != nil {
		return dst, err
	}
	return m.appendTo(dst, m.PDU.VarBinds), nil
}

// appendTo appends m with the variable bindings vbs; m and vbs are valid.
func (m *Message) appendTo(dst []byte, vbs []VarBind) []byte {
	vbLen := 0
	for i := range vbs {
		vbLen += varBindLen(&vbs[i])
	}
	f1, f2 := m.PDU.fields()
	pduLen := intTLVLen(int64(m.PDU.RequestID)) + intTLVLen(int64(f1)) + intTLVLen(int64(f2)) + tlvLen(vbLen)
	msgLen := intTLVLen(int64(m.Version)) + tlvLen(len(m.Community)) + tlvLen(pduLen)
	b := appendLength(append(dst, tagSequence), msgLen)
	b = appendIntTLV(b, int64(m.Version))
	b = appendTLV(b, tagOctetString, m.Community)
	b = appendLength(append(b, byte(m.PDU.Type)), pduLen)
	b = appendIntTLV(b, int64(m.PDU.RequestID))
	b = appendIntTLV(b, int64(f1))
	b = appendIntTLV(b, int64(f2))
	b = appendLength(append(b, tagSequence), vbLen)
	for i := range vbs {
		b = appendVarBind(b, &vbs[i])
	}
	return b
}

// Decode decodes a message. A message whose version is not v1 or v2c is
// reported with a *VersionError (and its version); one that does not
// decode with an error that matches profinet.ErrMalformed. A v1 Trap PDU,
// whose layout differs (RFC 1157 section 4.1.6), is returned with its
// type only. Decode accepts no octets after the message.
func Decode(b []byte) (Message, error) {
	var m Message
	if len(b) > MaxMessageSize {
		return m, malformed("message of %d octets", len(b))
	}
	body, rest, err := expect(b, tagSequence, "message")
	if err != nil {
		return m, err
	}
	if len(rest) != 0 {
		return m, malformed("%d octets after the message", len(rest))
	}
	ver, body, err := readInt32(body, "version")
	if err != nil {
		return m, err
	}
	m.Version = Version(ver)
	if m.Version != Version1 && m.Version != Version2c {
		return m, &VersionError{Version: ver}
	}
	community, body, err := expect(body, tagOctetString, "community")
	if err != nil {
		return m, err
	}
	m.Community = append([]byte{}, community...)
	tag, pdu, rest, err := readTLV(body)
	if err != nil {
		return m, err
	}
	if len(rest) != 0 {
		return m, malformed("%d octets after the PDU", len(rest))
	}
	m.PDU.Type = PDUType(tag)
	switch m.PDU.Type {
	case PDUGetRequest, PDUGetNextRequest, PDUResponse, PDUSetRequest:
	case PDUGetBulkRequest, PDUInformRequest, PDUTrapV2, PDUReport:
		// "SNMPv2 PDUs" (RFC 3416 section 3): not in the v1 grammar of
		// RFC 1157 section 4.
		if m.Version == Version1 {
			return m, malformed("%s in an SNMPv1 message", m.PDU.Type)
		}
	case PDUTrapV1:
		if m.Version != Version1 {
			return m, malformed("%s in an SNMPv2c message", m.PDU.Type)
		}
		return m, nil
	default:
		return m, malformed("PDU tag 0x%02x", tag)
	}
	return m, decodePDU(&m.PDU, pdu)
}

func decodePDU(p *PDU, b []byte) error {
	id, b, err := readInt32(b, "request-id")
	if err != nil {
		return err
	}
	f1, b, err := readInt32(b, "error-status")
	if err != nil {
		return err
	}
	f2, b, err := readInt32(b, "error-index")
	if err != nil {
		return err
	}
	p.RequestID = id
	if p.Type == PDUGetBulkRequest {
		p.NonRepeaters, p.MaxRepetitions = f1, f2
	} else {
		p.ErrorStatus, p.ErrorIndex = ErrorStatus(f1), f2
	}
	list, rest, err := expect(b, tagSequence, "variable-bindings")
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return malformed("%d octets after the variable bindings", len(rest))
	}
	// Count first: the allocation is bounded by the input and MaxVarBinds.
	n := 0
	for r := list; len(r) > 0; n++ {
		if n == MaxVarBinds {
			return malformed("more than %d variable bindings", MaxVarBinds)
		}
		if _, _, r, err = readTLV(r); err != nil {
			return err
		}
	}
	if n == 0 {
		return nil
	}
	p.VarBinds = make([]VarBind, 0, n)
	for len(list) > 0 {
		var vb []byte
		if vb, list, err = expect(list, tagSequence, "variable binding"); err != nil {
			return err
		}
		name, vb, err := expect(vb, tagOID, "name")
		if err != nil {
			return err
		}
		o, err := decodeOID(name)
		if err != nil {
			return err
		}
		tag, c, rest, err := readTLV(vb)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return malformed("%d octets after a value", len(rest))
		}
		v, err := decodeValue(tag, c)
		if err != nil {
			return err
		}
		p.VarBinds = append(p.VarBinds, VarBind{Name: o, Value: v})
	}
	return nil
}
