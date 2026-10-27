// SPDX-License-Identifier: MIT

package pnio

import "fmt"

// RT_CLASS_UDP: cyclic RT frames carried in UDP datagrams (port 34962)
// instead of Ethernet frames, with RTA alarms over UDP (RTA_CLASS_UDP).
// What is encoded here is what the public evidence verifies
// (docs/research/EVIDENCE-AR-SR-MCR-UDP.md, D): RTClass 4 in the
// IOCRProperties, AlarmCRProperties bit 1 (Transport), the
// CMInitiatorUDPRTPort of the ARBlockReq and the CMResponderUDPRTPort of
// the ARBlockRes (the UDPRTPort fields of [ARBlockReq] and [ARBlockRes]),
// all in Wireshark's PNIO dissector. No open implementation has a data
// path for RT_CLASS_UDP (p-net validates the fields and stops there): the
// IO-Device refuses such a Connect and the controller never requests one.

// IOCRPropRTClassMask selects the RTClass in IOCRProperties (bits 0-3).
const IOCRPropRTClassMask uint32 = 0x0000000F

// AlarmCRProperties bits.
const (
	// AlarmCRPropTransportUDP is bit 1, Transport: 0 RTA_CLASS_1 (alarms
	// on Ethernet), 1 RTA_CLASS_UDP (alarms in UDP datagrams).
	AlarmCRPropTransportUDP uint32 = 1 << 1
)

// LTIP is the LT of the IOCRs of RT_CLASS_UDP and of an AlarmCR of
// RTA_CLASS_UDP: the EtherType of IPv4 instead of [LT]. p-net requires it
// for RT_CLASS_UDP and profinet-py builds it for alarms over UDP
// (observed in implementations, docs/research/EVIDENCE-AR-SR-MCR-UDP.md, D).
const LTIP = 0x0800

// UDPRTPort is the UDP port of RT frames of RT_CLASS_UDP (34962, 0x8892):
// Wireshark dissects the payload of UDP port 0x8892 as PROFINET RT. The
// controller and the device of this module put it in the
// CMInitiatorUDPRTPort and CMResponderUDPRTPort fields although they use
// no UDP transport; p-net answers the same responder port.
const UDPRTPort = 0x8892

// String names the RT class. Wireshark calls value 1 the legacy encoding
// of RT_CLASS_1 and value 2 RT_CLASS_1 (or RT_CLASS_STREAM-RT).
func (c RTClass) String() string {
	switch c {
	case RTClass1:
		return "RT_CLASS_1(legacy)"
	case RTClass2:
		return "RT_CLASS_1"
	case RTClass3:
		return "RT_CLASS_3"
	case RTClassUDP:
		return "RT_CLASS_UDP"
	case RTClassStream:
		return "RT_CLASS_STREAM"
	default:
		return fmt.Sprintf("RTClass(%d)", uint8(c))
	}
}

// TransportUDP reports whether the AlarmCR asks for alarms over UDP
// (RTA_CLASS_UDP).
func (a *AlarmCRBlockReq) TransportUDP() bool { return a.Properties&AlarmCRPropTransportUDP != 0 }

// UsesUDP reports whether a Connect request asks for any transport over
// UDP: an IOCR of RT_CLASS_UDP or an AlarmCR of RTA_CLASS_UDP.
func (c *ConnectRequest) UsesUDP() bool {
	for i := range c.IOCRs {
		if c.IOCRs[i].RTClass() == RTClassUDP {
			return true
		}
	}
	return c.AlarmCR != nil && c.AlarmCR.TransportUDP()
}
