// SPDX-License-Identifier: MIT

package gsdml

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/model"
	"github.com/otfabric/go-profinet/pnio"
)

// dataTypeSizes are the sizes in octets of the fixed-size data types;
// strings take their Length attribute.
var dataTypeSizes = map[DataType]int{
	DataTypeBoolean: 1, DataTypeInteger8: 1, DataTypeUnsigned8: 1, DataTypeUnsigned8S: 1,
	DataTypeInteger16: 2, DataTypeUnsigned16: 2, DataTypeUnsigned16S: 2, DataTypeInteger16S: 2,
	DataTypeInteger32: 4, DataTypeUnsigned32: 4, DataTypeFloat32: 4, DataTypeInteger32S: 4, DataTypeUnsigned32S: 4,
	DataTypeInteger64: 8, DataTypeUnsigned64: 8, DataTypeFloat64: 8,
	DataTypeDate: 7, DataTypeTimeOfDayWithDateIndication: 6, DataTypeTimeOfDayWithoutDateIndication: 4,
	DataTypeTimeDifferenceWithDateIndication: 6, DataTypeTimeDifferenceWithoutDateIndication: 4,
	DataTypeNetworkTime: 8, DataTypeNetworkTimeDifference: 8,
	DataTypeUnsigned8PlusUnsigned8: 2, DataTypeFloat32PlusUnsigned8: 5, DataTypeFloat32PlusStatus8: 5, DataTypeFloat64PlusStatus8: 9,
	DataTypeN2: 2, DataTypeN4: 4, DataTypeV2: 2, DataTypeL2: 2, DataTypeR2: 2, DataTypeT2: 2, DataTypeT4: 4,
	DataTypeD2: 2, DataTypeE2: 2, DataTypeC4: 4, DataTypeX2: 2, DataTypeX4: 4, DataTypeUnipolar216: 2,
	DataTypeOctetString2PlusUnsigned8: 3, DataTypeFMessageTrailer4Byte: 4, DataTypeFMessageTrailer5Byte: 5,
	DataTypeTimeStamp: 12, DataTypeTimeStampDifference: 12, DataTypeTimeStampDifferenceShort: 8,
}

// DataItemSize returns the size in octets of a data item.
func DataItemSize(dataType, length string) (int, error) {
	t := DataType(dataType)
	switch t {
	case DataTypeVisibleString, DataTypeOctetString, DataTypeUTF8String, DataTypeOctetStringS:
		n, err := ParseUint16(length)
		if err != nil || n == 0 {
			return 0, fmt.Errorf("gsdml: %w: %s without a valid Length", profinet.ErrMalformed, dataType)
		}
		return int(n), nil
	default:
	}
	n, ok := dataTypeSizes[t]
	if !ok {
		return 0, fmt.Errorf("gsdml: %w: data type %q has no fixed size", profinet.ErrUnsupported, dataType)
	}
	return n, nil
}

func ioLength(items []*DataItem) (uint16, error) {
	total := 0
	for _, it := range items {
		n, err := DataItemSize(it.DataType, it.Length)
		if err != nil {
			return 0, err
		}
		total += n
	}
	if total > 1439 {
		return 0, fmt.Errorf("gsdml: %w: IO data of %d octets", profinet.ErrMalformed, total)
	}
	return uint16(total), nil
}

// ToModel converts the device access point dapID (the first one when
// empty) of a document into a device model: the DAP, the modules that are
// fixed or used in slots, and the other usable modules as the catalog.
func ToModel(doc *Document, dapID string) (*model.Device, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("gsdml: %w: "+format, append([]any{profinet.ErrMalformed}, a...)...)
	}
	if doc.ProfileBody == nil || doc.ProfileBody.DeviceIdentity == nil || doc.ProfileBody.ApplicationProcess == nil {
		return nil, bad("no ProfileBody, DeviceIdentity or ApplicationProcess")
	}
	ap := doc.ProfileBody.ApplicationProcess
	if ap.DeviceAccessPointList == nil || len(ap.DeviceAccessPointList.DeviceAccessPointItems) == 0 {
		return nil, bad("no device access point")
	}
	var dap *DeviceAccessPointItem
	for _, d := range ap.DeviceAccessPointList.DeviceAccessPointItems {
		if dapID == "" || d.ID == dapID {
			dap = d
			break
		}
	}
	if dap == nil {
		return nil, fmt.Errorf("gsdml: device access point %q: %w", dapID, profinet.ErrNotFound)
	}
	vid, err := ParseUint16(doc.ProfileBody.DeviceIdentity.VendorID)
	if err != nil {
		return nil, bad("VendorID: %v", err)
	}
	did, err := ParseUint16(doc.ProfileBody.DeviceIdentity.DeviceID)
	if err != nil {
		return nil, bad("DeviceID: %v", err)
	}
	m := &model.Device{}
	m.Identity.VendorID, m.Identity.DeviceID = vid, did
	if vn := doc.ProfileBody.DeviceIdentity.VendorName; vn != nil {
		m.Identity.VendorName = vn.Value
	}
	if f := doc.ProfileBody.DeviceFunction; f != nil && f.Family != nil {
		m.Identity.Family = f.Family.ProductFamily
	}
	m.Identity.TypeOfStation = dap.DNSCompatibleName
	if mi := dap.ModuleInfo; mi != nil {
		if mi.Name != nil {
			m.Identity.TypeOfStation = doc.Text(mi.Name.TextID)
		}
		if mi.OrderNumber != nil {
			m.Identity.OrderID = mi.OrderNumber.Value
		}
		if mi.HardwareRelease != nil {
			if v, err := strconv.ParseUint(strings.TrimLeft(mi.HardwareRelease.Value, "Vv"), 10, 16); err == nil {
				m.Identity.HardwareRevision = uint16(v)
			}
		}
		if mi.SoftwareRelease != nil {
			m.Identity.SoftwareRevision = parseSoftwareRelease(mi.SoftwareRelease.Value)
		}
	}
	if idx, err := ParseUint16(dap.ObjectUUIDLocalIndex); err == nil {
		m.Identity.Instance = idx
	}
	if v, err := ParseUint16(dap.MinDeviceInterval); err == nil {
		m.Capabilities.MinDeviceInterval = v
	}
	m.Capabilities.MaxARs = 1
	m.Capabilities.ConformanceClass = "A"
	if ci := dap.CertificationInfo; ci != nil {
		m.Capabilities.ConformanceClass = ci.ConformanceClass
	}
	m.Capabilities.LLDP = ParseBool(dap.LLDPNoDSupported, false) || m.Capabilities.ConformanceClass != "A"

	dapIdent, err := ParseUint32(dap.ModuleIdentNumber)
	if err != nil {
		return nil, bad("DAP %s ModuleIdentNumber: %v", dap.ID, err)
	}
	m.DAP = model.Module{Name: textOf(doc, dap.ModuleInfo), IdentNumber: dapIdent}
	if dap.VirtualSubmoduleList != nil {
		for _, vs := range dap.VirtualSubmoduleList.VirtualSubmoduleItems {
			s, err := toSubmodule(doc, vs, 1)
			if err != nil {
				return nil, err
			}
			m.DAP.Submodules = append(m.DAP.Submodules, s)
		}
	}
	if sd := dap.SystemDefinedSubmoduleList; sd != nil {
		for _, it := range sd.InterfaceSubmoduleItems {
			s, err := systemSubmodule(doc, it.TextID, it.SubslotNumber, it.SubmoduleIdentNumber)
			if err != nil {
				return nil, err
			}
			m.DAP.Submodules = append(m.DAP.Submodules, s)
			m.Capabilities.SupportedRTClasses = strings.FieldsFunc(it.SupportedRTClasses, func(r rune) bool { return r == ' ' || r == ';' })
			m.Capabilities.DCPHello = ParseBool(it.DCPHelloSupported, false)
			if ar := it.ApplicationRelations; ar != nil {
				if n, err := ParseUint16(ar.NumberOfAR); err == nil && n > 0 {
					m.Capabilities.MaxARs = int(n)
				}
				if tp := ar.TimingProperties; tp != nil && tp.ReductionRatio != "" {
					if rr, err := ParseValueList(tp.ReductionRatio, 64); err == nil {
						for _, v := range rr {
							m.Capabilities.ReductionRatios = append(m.Capabilities.ReductionRatios, uint16(v))
						}
					}
				}
			}
		}
		for _, it := range sd.PortSubmoduleItems {
			s, err := systemSubmodule(doc, it.TextID, it.SubslotNumber, it.SubmoduleIdentNumber)
			if err != nil {
				return nil, err
			}
			m.DAP.Submodules = append(m.DAP.Submodules, s)
			m.Capabilities.Ports++
		}
	}

	modules := map[string]*ModuleItem{}
	if ap.ModuleList != nil {
		for _, mi := range ap.ModuleList.ModuleItems {
			modules[mi.ID] = mi
		}
	}
	if dap.UseableModules != nil {
		used := map[uint16]bool{}
		for _, r := range dap.UseableModules.ModuleItemRefs {
			mi := modules[r.ModuleItemTarget]
			if mi == nil {
				return nil, bad("ModuleItemRef %q: no such module", r.ModuleItemTarget)
			}
			mod, err := toModule(doc, mi)
			if err != nil {
				return nil, err
			}
			// The configuration: the slots the module is fixed in, else
			// the slots it is used in, else its only allowed slot.
			slots := r.FixedInSlots
			if slots == "" {
				slots = r.UsedInSlots
			}
			if slots == "" {
				if allowed, err := ParseValueList(r.AllowedInSlots, 2); err == nil && len(allowed) == 1 {
					slots = r.AllowedInSlots
				}
			}
			list, err := ParseValueList(slots, 4096)
			if err != nil {
				return nil, err
			}
			for _, s := range list {
				if s == 0 || s > 0x7FFF || used[uint16(s)] {
					continue
				}
				used[uint16(s)] = true
				m.Slots = append(m.Slots, model.Slot{Number: uint16(s), Module: mod})
			}
			m.Catalog = append(m.Catalog, mod)
		}
		sort.Slice(m.Slots, func(i, j int) bool { return m.Slots[i].Number < m.Slots[j].Number })
	}
	return m, nil
}

func textOf(doc *Document, mi *ModuleInfo) string {
	if mi == nil || mi.Name == nil {
		return ""
	}
	return doc.Text(mi.Name.TextID)
}

func parseSoftwareRelease(s string) pnio.SoftwareRevision {
	r := pnio.SoftwareRevision{Prefix: 'V'}
	if s == "" {
		return r
	}
	if c := s[0]; c == 'V' || c == 'R' || c == 'P' || c == 'U' || c == 'T' {
		r.Prefix = c
		s = s[1:]
	}
	parts := strings.Split(s, ".")
	vals := []*uint8{&r.FunctionalEnhance, &r.BugFix, &r.InternalChange}
	for i, p := range parts {
		if i >= len(vals) {
			break
		}
		if v, err := strconv.ParseUint(p, 10, 8); err == nil {
			*vals[i] = uint8(v)
		}
	}
	return r
}

func systemSubmodule(doc *Document, textID, subslot, ident string) (model.Submodule, error) {
	ss, err := ParseUint16(subslot)
	if err != nil {
		return model.Submodule{}, fmt.Errorf("gsdml: %w: SubslotNumber %q", profinet.ErrMalformed, subslot)
	}
	id, err := ParseUint32(ident)
	if err != nil {
		return model.Submodule{}, fmt.Errorf("gsdml: %w: SubmoduleIdentNumber %q", profinet.ErrMalformed, ident)
	}
	return model.Submodule{Name: doc.Text(textID), IdentNumber: id, Subslot: ss}, nil
}

func toModule(doc *Document, mi *ModuleItem) (model.Module, error) {
	id, err := ParseUint32(mi.ModuleIdentNumber)
	if err != nil {
		return model.Module{}, fmt.Errorf("gsdml: %w: module %s ModuleIdentNumber %q", profinet.ErrMalformed, mi.ID, mi.ModuleIdentNumber)
	}
	m := model.Module{Name: textOf(doc, mi.ModuleInfo), IdentNumber: id}
	if mi.VirtualSubmoduleList != nil {
		next := uint16(1)
		for _, vs := range mi.VirtualSubmoduleList.VirtualSubmoduleItems {
			s, err := toSubmodule(doc, vs, next)
			if err != nil {
				return m, err
			}
			next = s.Subslot + 1
			m.Submodules = append(m.Submodules, s)
		}
	}
	return m, nil
}

func toSubmodule(doc *Document, vs *VirtualSubmoduleItem, defSubslot uint16) (model.Submodule, error) {
	id, err := ParseUint32(vs.SubmoduleIdentNumber)
	if err != nil {
		return model.Submodule{}, fmt.Errorf("gsdml: %w: submodule %s SubmoduleIdentNumber %q", profinet.ErrMalformed, vs.ID, vs.SubmoduleIdentNumber)
	}
	s := model.Submodule{Name: textOf(doc, vs.ModuleInfo), IdentNumber: id, Subslot: defSubslot}
	if vs.FixedInSubslots != "" {
		if l, err := ParseValueList(vs.FixedInSubslots, 64); err == nil && len(l) > 0 {
			s.Subslot = uint16(l[0])
		}
	}
	if io := vs.IOData; io != nil {
		if io.Input != nil {
			if s.InputLen, err = ioLength(io.Input.DataItems); err != nil {
				return s, fmt.Errorf("submodule %s input: %w", vs.ID, err)
			}
		}
		if io.Output != nil {
			if s.OutputLen, err = ioLength(io.Output.DataItems); err != nil {
				return s, fmt.Errorf("submodule %s output: %w", vs.ID, err)
			}
		}
	}
	if rl := vs.RecordDataList; rl != nil {
		for _, r := range rl.ParameterRecordDataItems {
			rec, err := toRecord(doc, r)
			if err != nil {
				return s, fmt.Errorf("submodule %s: %w", vs.ID, err)
			}
			s.Records = append(s.Records, rec)
		}
	}
	return s, nil
}

// MaxRecordLength bounds the length of a parameter record ToModel builds.
const MaxRecordLength = 65535

func toRecord(doc *Document, r *ParameterRecordDataItem) (model.Record, error) {
	idx, err := ParseUint16(r.Index)
	if err != nil || idx > 0x7FFF {
		return model.Record{}, fmt.Errorf("gsdml: %w: record Index %q", profinet.ErrMalformed, r.Index)
	}
	n, err := ParseUint32(r.Length)
	if err != nil || n > MaxRecordLength {
		return model.Record{}, fmt.Errorf("gsdml: %w: record %d Length %q", profinet.ErrMalformed, idx, r.Length)
	}
	rec := model.Record{Index: pnio.Index(idx), Length: uint16(n), Default: make([]byte, n)}
	if r.Name != nil {
		rec.Name = doc.Text(r.Name.TextID)
	}
	for _, c := range r.Consts {
		off, _ := ParseUint32(c.ByteOffset)
		data, err := parseOctets(c.Data)
		if err != nil {
			return rec, err
		}
		if int(off)+len(data) > len(rec.Default) {
			return rec, fmt.Errorf("gsdml: %w: record %d: Const beyond the record", profinet.ErrMalformed, idx)
		}
		copy(rec.Default[off:], data)
	}
	for _, ref := range r.Refs {
		if err := applyRef(rec.Default, ref); err != nil {
			return rec, fmt.Errorf("record %d: %w", idx, err)
		}
	}
	return rec, nil
}

// parseOctets parses the Data of a Const: octets as "0x01,0x02".
func parseOctets(s string) ([]byte, error) {
	var out []byte
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		v, err := parseUint(p, 8)
		if err != nil {
			return nil, fmt.Errorf("gsdml: %w: Const data %q", profinet.ErrMalformed, s)
		}
		out = append(out, byte(v))
	}
	return out, nil
}

// applyRef writes the DefaultValue of a Ref into a record.
func applyRef(rec []byte, r *Ref) error {
	off, err := ParseUint32(r.ByteOffset)
	if err != nil {
		return fmt.Errorf("gsdml: %w: Ref ByteOffset %q", profinet.ErrMalformed, r.ByteOffset)
	}
	t := DataType(r.DataType)
	if t == DataTypeBit || t == DataTypeBitArea {
		bit, _ := ParseUint16(r.BitOffset)
		width := uint16(1)
		if t == DataTypeBitArea {
			width, _ = ParseUint16(r.BitLength)
		}
		v, err := strconv.ParseUint(strings.TrimSpace(r.DefaultValue), 0, 64)
		if err != nil || int(off) >= len(rec) || bit+width > 8 || width == 0 {
			return fmt.Errorf("gsdml: %w: bit Ref at %d.%d", profinet.ErrMalformed, off, bit)
		}
		mask := byte((1<<width)-1) << bit
		rec[off] = rec[off]&^mask | byte(v<<bit)&mask
		return nil
	}
	size, err := DataItemSize(r.DataType, r.Length)
	if err != nil {
		return err
	}
	if int(off)+size > len(rec) {
		return fmt.Errorf("gsdml: %w: Ref at %d of %d octets beyond the record", profinet.ErrMalformed, off, size)
	}
	dst := rec[off : int(off)+size]
	dv := strings.TrimSpace(r.DefaultValue)
	switch t {
	case DataTypeFloat32:
		f, err := strconv.ParseFloat(dv, 32)
		if err != nil {
			return fmt.Errorf("gsdml: %w: Float32 default %q", profinet.ErrMalformed, dv)
		}
		binary.BigEndian.PutUint32(dst, math.Float32bits(float32(f)))
	case DataTypeFloat64:
		f, err := strconv.ParseFloat(dv, 64)
		if err != nil {
			return fmt.Errorf("gsdml: %w: Float64 default %q", profinet.ErrMalformed, dv)
		}
		binary.BigEndian.PutUint64(dst, math.Float64bits(f))
	case DataTypeVisibleString, DataTypeUTF8String:
		copy(dst, dv)
	case DataTypeOctetString:
		b, err := parseOctets(dv)
		if err != nil {
			return err
		}
		copy(dst, b)
	case DataTypeInteger8, DataTypeInteger16, DataTypeInteger32, DataTypeInteger64:
		v, err := strconv.ParseInt(dv, 0, size*8)
		if err != nil {
			return fmt.Errorf("gsdml: %w: %s default %q", profinet.ErrMalformed, t, dv)
		}
		putUint(dst, uint64(v))
	default:
		v, err := strconv.ParseUint(dv, 0, min(size*8, 64))
		if err != nil {
			return fmt.Errorf("gsdml: %w: %s default %q", profinet.ErrMalformed, t, dv)
		}
		putUint(dst, v)
	}
	return nil
}

func putUint(dst []byte, v uint64) {
	for i := len(dst) - 1; i >= 0; i-- {
		dst[i] = byte(v)
		v >>= 8
	}
}

// GenerateOptions tune FromModel.
type GenerateOptions struct {
	// MainFamily is the Family/MainFamily; empty means "I/O".
	MainFamily string
	// DNSCompatibleName is the default station name; empty derives it
	// from the type of station.
	DNSCompatibleName string
	// NetloadClass is declared in the CertificationInfo; empty means "I".
	// It is a declaration the device vendor must back with a netload
	// test; this library does not establish it.
	NetloadClass string
	// PNIOVersion is the declared PNIO_Version; empty means "V2.35".
	PNIOVersion string
	// SupervisorAR declares DeviceAccessSupported.
	SupervisorAR bool
}

// FromModel generates a GSDML document that describes a device model:
// the identity, the DAP with its interface and ports, every module type
// with its submodules' IO data and parameter records, and the plugged
// modules as fixed in their slots. The document declares conformance
// class A and RT_CLASS_1, the scope this module implements. The result
// passes Validate.
func FromModel(m *model.Device, opts GenerateOptions) (*Document, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if opts.MainFamily == "" {
		opts.MainFamily = "I/O"
	}
	if opts.NetloadClass == "" {
		opts.NetloadClass = "I"
	}
	if opts.PNIOVersion == "" {
		opts.PNIOVersion = "V2.35"
	}
	dns := opts.DNSCompatibleName
	if dns == "" {
		dns = dnsName(m.Identity.TypeOfStation)
	}
	g := &gen{texts: map[string]string{}}
	id := m.Identity
	family := id.Family
	if family == "" {
		family = id.TypeOfStation
	}
	doc := &Document{
		// The schema location engineering tools expect; the schema itself
		// is PI's and not part of this module.
		Attrs: []xml.Attr{
			{Name: xml.Name{Local: "xmlns:xsi"}, Value: "http://www.w3.org/2001/XMLSchema-instance"},
			{Name: xml.Name{Local: "xsi:schemaLocation"}, Value: Namespace + " ..\\xsd\\GSDML-DeviceProfile-" + opts.PNIOVersion + ".xsd"},
		},
		ProfileHeader: &ProfileHeader{
			ProfileIdentification: &ProfileIdentification{Value: "PROFINET Device Profile"},
			ProfileRevision:       &ProfileRevision{Value: "1.00"},
			ProfileName:           &ProfileName{Value: "Device Profile for PROFINET Devices"},
			ProfileSource:         &ProfileSource{Value: "PROFIBUS Nutzerorganisation e. V. (PNO)"},
			ProfileClassID:        &ProfileClassID{Value: "Device"},
			ISO15745Reference: &ISO15745Reference{
				ISO15745Part: &ISO15745Part{Value: "4"}, ISO15745Edition: &ISO15745Edition{Value: "1"},
				ProfileTechnology: &ProfileTechnology{Value: "GSDML"},
			},
		},
		ProfileBody: &ProfileBody{
			DeviceIdentity: &DeviceIdentity{
				VendorID: hex16(id.VendorID), DeviceID: hex16(id.DeviceID),
				InfoText:   &InfoText{TextID: g.text("T_DeviceInfo", id.TypeOfStation)},
				VendorName: &VendorName{Value: nonEmpty(id.VendorName, "unknown")},
			},
			DeviceFunction: &DeviceFunction{Family: &Family{MainFamily: opts.MainFamily, ProductFamily: family}},
		},
	}
	ap := &ApplicationProcess{}
	doc.ProfileBody.ApplicationProcess = ap

	maxSlot := uint16(0)
	for _, s := range m.Slots {
		maxSlot = max(maxSlot, s.Number)
	}
	maxIn, maxOut := 0, 0
	m.Each(func(p model.PluggedSubmodule) {
		maxIn += int(p.Submodule.InputLen) + 1
		maxOut += int(p.Submodule.OutputLen) + 1
	})
	sw := id.SoftwareRevision
	if sw.Prefix == 0 {
		sw.Prefix = 'V'
	}
	dap := &DeviceAccessPointItem{
		ID: "DAP", PNIOVersion: opts.PNIOVersion, PhysicalSlots: fmt.Sprintf("0..%d", maxSlot),
		ModuleIdentNumber: hex32(m.DAP.IdentNumber), MinDeviceInterval: strconv.Itoa(int(max(m.Capabilities.MinDeviceInterval, 32))),
		DNSCompatibleName: dns, FixedInSlots: "0", ObjectUUIDLocalIndex: strconv.Itoa(int(max(id.Instance, 1))),
		MultipleWriteSupported: "true", DeviceAccessSupported: strconv.FormatBool(opts.SupervisorAR),
		CheckDeviceIDAllowed: "true", NameOfStationNotTransferable: "false", LLDPNoDSupported: strconv.FormatBool(m.Capabilities.LLDP),
		ResetToFactoryModes: "1..2",
		ModuleInfo: &ModuleInfo{
			Name: &Name{TextID: g.text("T_DAP_Name", nonEmpty(m.DAP.Name, id.TypeOfStation))}, InfoText: &InfoText{TextID: g.text("T_DAP_Info", id.TypeOfStation)},
			VendorName: &VendorName{Value: nonEmpty(id.VendorName, "unknown")}, OrderNumber: &OrderNumber{Value: nonEmpty(id.OrderID, "-")},
			HardwareRelease: &HardwareRelease{Value: strconv.Itoa(int(id.HardwareRevision))}, SoftwareRelease: &SoftwareRelease{Value: sw.String()},
		},
		CertificationInfo: &CertificationInfo{ConformanceClass: nonEmpty(m.Capabilities.ConformanceClass, "A"), NetloadClass: opts.NetloadClass},
		IOConfigData:      &IOConfigData{MaxInputLength: strconv.Itoa(min(maxIn, 1440)), MaxOutputLength: strconv.Itoa(min(maxOut, 1440))},
	}
	ap.DeviceAccessPointList = &DeviceAccessPointList{DeviceAccessPointItems: []*DeviceAccessPointItem{dap}}

	// The DAP's own submodules: virtual ones and the interface and ports.
	dap.VirtualSubmoduleList = &VirtualSubmoduleList{}
	sd := &SystemDefinedSubmoduleList{}
	for _, s := range m.DAP.Submodules {
		switch {
		case s.Subslot >= 0x8000 && s.Subslot&0x00FF == 0:
			rr := "1 2 4 8 16 32 64 128 256 512"
			if len(m.Capabilities.ReductionRatios) > 0 {
				var v []uint32
				for _, x := range m.Capabilities.ReductionRatios {
					v = append(v, uint32(x))
				}
				rr = FormatValueList(v)
			}
			sd.InterfaceSubmoduleItems = append(sd.InterfaceSubmoduleItems, &InterfaceSubmoduleItem{
				ID: fmt.Sprintf("IF%d", s.Subslot), SubslotNumber: strconv.Itoa(int(s.Subslot)), TextID: g.text(fmt.Sprintf("T_IF%d", s.Subslot), nonEmpty(s.Name, "Interface")),
				SubmoduleIdentNumber: hex32(s.IdentNumber), SupportedRTClasses: "RT_CLASS_1", SupportedProtocols: "LLDP", DCPHelloSupported: strconv.FormatBool(m.Capabilities.DCPHello),
				ApplicationRelations: &ApplicationRelations{StartupMode: "Legacy;Advanced", NumberOfAR: strconv.Itoa(max(m.Capabilities.MaxARs, 1)),
					TimingProperties: &TimingProperties{SendClock: "32", ReductionRatio: rr}},
			})
		case s.Subslot >= 0x8000:
			sd.PortSubmoduleItems = append(sd.PortSubmoduleItems, &PortSubmoduleItem{
				ID: fmt.Sprintf("Port%d", s.Subslot), SubslotNumber: strconv.Itoa(int(s.Subslot)), TextID: g.text(fmt.Sprintf("T_Port%d", s.Subslot), nonEmpty(s.Name, "Port")),
				SubmoduleIdentNumber: hex32(s.IdentNumber), MAUTypeList: &MAUTypeList{MAUTypeItems: []*MAUTypeItem{{Value: "16"}}},
			})
		default:
			vs := g.submodule(fmt.Sprintf("DAP_SM%d", s.Subslot), &s)
			if s.Subslot == model.SubslotDAP {
				vs.WriteableIMRecords = "1 2 3 4"
			}
			dap.VirtualSubmoduleList.VirtualSubmoduleItems = append(dap.VirtualSubmoduleList.VirtualSubmoduleItems, vs)
		}
	}
	if len(sd.InterfaceSubmoduleItems) > 0 {
		dap.SystemDefinedSubmoduleList = sd
	}

	// Module types, with the slots they are plugged in.
	slotsOf := map[uint32][]uint32{}
	for _, s := range m.Slots {
		slotsOf[s.Module.IdentNumber] = append(slotsOf[s.Module.IdentNumber], uint32(s.Number))
	}
	types := m.ModuleTypes()
	if len(types) > 0 {
		ap.ModuleList = &ModuleList{}
		dap.UseableModules = &UseableModules{}
	}
	all := make([]uint32, 0, maxSlot)
	for i := uint32(1); i <= uint32(maxSlot); i++ {
		all = append(all, i)
	}
	for i, mt := range types {
		mid := fmt.Sprintf("M%d", i+1)
		mi := &ModuleItem{ID: mid, ModuleIdentNumber: hex32(mt.IdentNumber),
			ModuleInfo: &ModuleInfo{Name: &Name{TextID: g.text(mid+"_Name", nonEmpty(mt.Name, mid))}, InfoText: &InfoText{TextID: g.text(mid+"_Info", nonEmpty(mt.Info, mt.Name))},
				OrderNumber: &OrderNumber{Value: nonEmpty(id.OrderID, "-")}},
			VirtualSubmoduleList: &VirtualSubmoduleList{}}
		for j := range mt.Submodules {
			s := mt.Submodules[j]
			vs := g.submodule(fmt.Sprintf("%s_SM%d", mid, s.Subslot), &s)
			vs.FixedInSubslots = strconv.Itoa(int(s.Subslot))
			mi.VirtualSubmoduleList.VirtualSubmoduleItems = append(mi.VirtualSubmoduleList.VirtualSubmoduleItems, vs)
		}
		ap.ModuleList.ModuleItems = append(ap.ModuleList.ModuleItems, mi)
		ref := &ModuleItemRef{ModuleItemTarget: mid}
		if fixed := slotsOf[mt.IdentNumber]; len(fixed) > 0 {
			ref.AllowedInSlots = FormatValueList(fixed)
			ref.FixedInSlots = FormatValueList(fixed)
		} else if len(all) > 0 {
			ref.AllowedInSlots = FormatValueList(all)
		}
		dap.UseableModules.ModuleItemRefs = append(dap.UseableModules.ModuleItemRefs, ref)
	}
	ap.ExternalTextList = &ExternalTextList{PrimaryLanguage: &PrimaryLanguage{}}
	keys := make([]string, 0, len(g.texts))
	for k := range g.texts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ap.ExternalTextList.PrimaryLanguage.Texts = append(ap.ExternalTextList.PrimaryLanguage.Texts, &Text{TextID: k, Value: g.texts[k]})
	}
	if err := Validate(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

type gen struct {
	texts map[string]string
}

func (g *gen) text(id, value string) string {
	g.texts[id] = nonEmpty(value, id)
	return id
}

func (g *gen) submodule(id string, s *model.Submodule) *VirtualSubmoduleItem {
	vs := &VirtualSubmoduleItem{ID: id, SubmoduleIdentNumber: hex32(s.IdentNumber), MayIssueProcessAlarm: "true", IOData: &IOData{},
		ModuleInfo: &ModuleInfo{Name: &Name{TextID: g.text(id+"_Name", nonEmpty(s.Name, id))}, InfoText: &InfoText{TextID: g.text(id+"_Info", nonEmpty(s.Info, s.Name))}}}
	if s.InputLen > 0 {
		vs.IOData.Input = &Input{Consistency: "All items consistency", DataItems: []*DataItem{{DataType: string(DataTypeOctetString), Length: strconv.Itoa(int(s.InputLen)), TextID: g.text(id+"_In", "Input data")}}}
	}
	if s.OutputLen > 0 {
		vs.IOData.Output = &Output{Consistency: "All items consistency", DataItems: []*DataItem{{DataType: string(DataTypeOctetString), Length: strconv.Itoa(int(s.OutputLen)), TextID: g.text(id+"_Out", "Output data")}}}
	}
	if len(s.Records) > 0 {
		vs.RecordDataList = &RecordDataList{}
		for _, r := range s.Records {
			pr := &ParameterRecordDataItem{Index: strconv.Itoa(int(r.Index)), Length: strconv.Itoa(int(r.Length)),
				Name: &Name{TextID: g.text(fmt.Sprintf("%s_R%d", id, r.Index), nonEmpty(r.Name, fmt.Sprintf("Record %d", r.Index)))}}
			if len(r.Default) > 0 {
				pr.Consts = []*Const{{ByteOffset: "0", Data: formatOctets(r.Default)}}
			}
			vs.RecordDataList.ParameterRecordDataItems = append(vs.RecordDataList.ParameterRecordDataItems, pr)
		}
	}
	return vs
}

func formatOctets(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("0x%02X", v)
	}
	return strings.Join(parts, ",")
}

func hex16(v uint16) string { return fmt.Sprintf("0x%04X", v) }
func hex32(v uint32) string { return fmt.Sprintf("0x%08X", v) }

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return strings.TrimSpace(s)
}

// dnsName derives a valid station name from a device name.
func dnsName(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	n := strings.TrimRight(b.String(), "-")
	if n == "" || strings.HasPrefix(n, "port-") || strings.HasPrefix(n, "xn-") {
		n = "device-" + n
	}
	if len(n) > 63 {
		n = strings.TrimRight(n[:63], "-")
	}
	return n
}

// FileName returns the conventional file name of a GSDML file:
// GSDML-V2.35-<vendor>-<family>-<yyyymmdd>.xml.
func FileName(m *model.Device, version string, date time.Time) string {
	if version == "" {
		version = "V2.35"
	}
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
				b.WriteRune(r)
			case r == ' ' || r == '-' || r == '_':
				b.WriteByte('_')
			}
		}
		return strings.Trim(b.String(), "_")
	}
	family := m.Identity.Family
	if family == "" {
		family = m.Identity.TypeOfStation
	}
	return fmt.Sprintf("GSDML-%s-%s-%s-%s.xml", version, nonEmpty(clean(m.Identity.VendorName), "Vendor"), nonEmpty(clean(family), "Device"), date.Format("20060102"))
}
