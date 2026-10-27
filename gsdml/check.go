// SPDX-License-Identifier: MIT

package gsdml

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/otfabric/go-profinet"
)

// Severity grades an issue of a GSDML document.
type Severity int

const (
	// SeverityError: the document breaks a rule of the catalog (structure,
	// lexical form, references) or a physical limit; Validate fails.
	SeverityError Severity = iota
	// SeverityWarning: the document is valid for the catalog but probably
	// wrong; the rule says whether it is publicly substantiated or a local
	// heuristic of go-profinet.
	SeverityWarning
	// SeverityUnvalidated: content outside go-profinet's catalog (newer
	// schema versions, vendor extensions); kept as read, not checked.
	SeverityUnvalidated
)

// String returns the severity's name.
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityUnvalidated:
		return "unvalidated"
	default:
		return "Severity(" + strconv.Itoa(int(s)) + ")"
	}
}

// MarshalText makes a Severity a JSON string.
func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText reads the names MarshalText writes, so that a Report
// decodes from its own JSON.
func (s *Severity) UnmarshalText(b []byte) error {
	for _, v := range []Severity{SeverityError, SeverityWarning, SeverityUnvalidated} {
		if string(b) == v.String() {
			*s = v
			return nil
		}
	}
	return fmt.Errorf("gsdml: %w: severity %q", profinet.ErrMalformed, b)
}

// Rules of the issues. The catalog rules come from the independent
// catalog; the semantic rules each state their basis.
const (
	// RuleStructure: required elements and attributes, lexical forms,
	// unique IDs (the catalog).
	RuleStructure = "structure"
	// RuleReference: every ID and text reference resolves (the catalog).
	RuleReference = "reference"
	// RuleUnknown: an element or attribute outside the catalog.
	RuleUnknown = "unknown"
	// RuleSchemaVersion: the document declares a schema version newer
	// than the catalog's.
	RuleSchemaVersion = "schema-version"
	// RuleIODataLength: the input or the output data of a submodule and
	// its provider status do not fit the 1440 octets an RT frame carries
	// (a physical limit).
	RuleIODataLength = "io-data-length"
	// RuleSlots: a module is allowed, used or fixed in a slot the device
	// access point does not have (PhysicalSlots), used or fixed where it
	// is not allowed, or two modules are fixed in one slot (consistency of
	// the document's own declarations).
	RuleSlots = "slots"
	// RuleIdentNumber: two modules share a ModuleIdentNumber, so that a
	// controller cannot tell them apart (local heuristic).
	RuleIdentNumber = "ident-number"
	// RuleRecordIndex: a parameter record outside the user index range
	// 0x0000-0x7FFF (the range of record indexes a device defines).
	RuleRecordIndex = "record-index"
	// RuleSubslot: an interface or port submodule outside 0x8000-0x8FFF,
	// the subslots PROFINET reserves for them (as p-net and pnio-rs number
	// them; local heuristic).
	RuleSubslot = "subslot"
	// RuleReductionRatio: a reduction ratio that is not a power of two
	// from 1 to 512, the RT_CLASS_1 ratios (local heuristic).
	RuleReductionRatio = "reduction-ratio"
)

// Report is the result of Check: every issue, with its severity.
type Report struct {
	// SchemaVersion is the version the document declares (from
	// xsi:schemaLocation), empty when it declares none.
	SchemaVersion string  `json:"schemaVersion,omitempty"`
	Issues        []Issue `json:"issues"`
}

// Count returns the number of issues of a severity.
func (r *Report) Count(s Severity) int {
	n := 0
	for _, i := range r.Issues {
		if i.Severity == s {
			n++
		}
	}
	return n
}

// Err returns the errors of the report as a *ValidationError, or nil.
func (r *Report) Err() error {
	var errs []Issue
	for _, i := range r.Issues {
		if i.Severity == SeverityError {
			errs = append(errs, i)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Issues: errs}
}

// Check validates a document against the catalog (as Validate does) and
// the semantic rules, and lists the content it did not validate. Nothing
// it reports makes it PI's GSDML checker.
func Check(doc *Document) Report {
	v := &validator{ids: map[string]string{}, texts: map[string]bool{}}
	doc.validate(v)
	for _, r := range v.refs {
		if _, ok := v.ids[r.id]; !ok {
			v.issues = append(v.issues, Issue{Path: r.path, Message: fmt.Sprintf("attribute %s: no element with ID %q", r.kind, r.id), Rule: RuleReference})
		}
	}
	for _, r := range v.tids {
		if !v.texts[r.id] {
			v.issues = append(v.issues, Issue{Path: r.path, Message: fmt.Sprintf("attribute TextId: no text %q in the ExternalTextList", r.id), Rule: RuleReference})
		}
	}
	rep := Report{SchemaVersion: schemaVersion(doc)}
	if rep.SchemaVersion != "" && newerThanCatalog(rep.SchemaVersion) {
		v.issues = append(v.issues, Issue{Path: "ISO15745Profile", Severity: SeverityUnvalidated, Rule: RuleSchemaVersion,
			Message: fmt.Sprintf("schema version %s is newer than go-profinet's catalog (up to %s): what that version added is kept, not validated", rep.SchemaVersion, SchemaVersions[len(SchemaVersions)-1])})
	}
	semantic(doc, v)
	rep.Issues = v.issues
	return rep
}

var schemaLocation = regexp.MustCompile(`GSDML-DeviceProfile-[Vv](\d+\.\d+)\.xsd`)

// schemaVersion returns the version a document declares in
// xsi:schemaLocation ("2.35").
func schemaVersion(doc *Document) string {
	for _, a := range doc.Attrs {
		if a.Name.Local == "schemaLocation" || strings.HasSuffix(a.Name.Local, ":schemaLocation") {
			if m := schemaLocation.FindStringSubmatch(a.Value); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// versionNumber turns "2.4" into 240 and "2.45" into 245.
func versionNumber(v string) int {
	major, minor, ok := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(v, "V"), "v"), ".")
	if !ok {
		return 0
	}
	for len(minor) < 2 {
		minor += "0"
	}
	a, err1 := strconv.Atoi(major)
	b, err2 := strconv.Atoi(minor[:2])
	if err1 != nil || err2 != nil {
		return 0
	}
	return a*100 + b
}

func newerThanCatalog(v string) bool {
	n := versionNumber(v)
	return n > 0 && n > versionNumber(SchemaVersions[len(SchemaVersions)-1])
}

func (v *validator) unknown(nodes []*Node) {
	for _, n := range nodes {
		v.issues = append(v.issues, Issue{Path: strings.Join(v.path, "/"), Severity: SeverityUnvalidated, Rule: RuleUnknown,
			Message: fmt.Sprintf("element %s is not in the catalog: kept, not validated", n.XMLName.Local)})
	}
}

func (v *validator) unknownAttrs(attrs []xml.Attr) {
	for _, a := range attrs {
		// Namespace declarations and the XML Schema instance attributes
		// (the parser keeps their prefixes in the name) are not content.
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" || strings.HasPrefix(a.Name.Local, "xmlns:") ||
			a.Name.Space == "http://www.w3.org/2001/XMLSchema-instance" || a.Name.Space == "xsi" || strings.HasPrefix(a.Name.Local, "xsi:") {
			continue
		}
		v.issues = append(v.issues, Issue{Path: strings.Join(v.path, "/"), Severity: SeverityUnvalidated, Rule: RuleUnknown,
			Message: fmt.Sprintf("attribute %s is not in the catalog: kept, not validated", a.Name.Local)})
	}
}

func (v *validator) warn(path, rule, format string, a ...any) {
	v.issues = append(v.issues, Issue{Path: path, Severity: SeverityWarning, Rule: rule, Message: fmt.Sprintf(format, a...)})
}

// maxIOData is the most data of one submodule an RT frame carries: its
// 1440-octet C_SDU holds the data and the submodule's provider status.
const maxIOData = 1439

// dataLength adds up the sizes of data items; items of a type without a
// known size are left out.
func dataLength(items []*DataItem) int {
	total := 0
	for _, it := range items {
		if n, err := DataItemSize(it.DataType, it.Length); err == nil {
			total += n
		}
	}
	return total
}

func (v *validator) checkSubmodules(path string, items []*VirtualSubmoduleItem) {
	for _, sm := range items {
		p := path + "/VirtualSubmoduleItem[" + sm.ID + "]"
		if sm.IOData != nil {
			if sm.IOData.Input != nil {
				if n := dataLength(sm.IOData.Input.DataItems); n > maxIOData {
					v.issues = append(v.issues, Issue{Path: p, Rule: RuleIODataLength, Message: fmt.Sprintf("%d octets of input data: an RT frame carries at most %d with the provider status", n, maxIOData)})
				}
			}
			if sm.IOData.Output != nil {
				if n := dataLength(sm.IOData.Output.DataItems); n > maxIOData {
					v.issues = append(v.issues, Issue{Path: p, Rule: RuleIODataLength, Message: fmt.Sprintf("%d octets of output data: an RT frame carries at most %d with the provider status", n, maxIOData)})
				}
			}
		}
		if sm.RecordDataList != nil {
			for _, r := range sm.RecordDataList.ParameterRecordDataItems {
				if idx, err := parseUint(r.Index, 16); err == nil && idx > 0x7FFF {
					v.warn(p, RuleRecordIndex, "parameter record index %#x is outside the user range 0x0000-0x7FFF", idx)
				}
			}
		}
	}
}

func slotSet(s string) (map[uint32]bool, bool) {
	if strings.TrimSpace(s) == "" {
		return nil, false
	}
	vs, err := ParseValueList(s, 1<<16)
	if err != nil {
		return nil, false
	}
	m := map[uint32]bool{}
	for _, x := range vs {
		m[x] = true
	}
	return m, true
}

// semantic applies the semantic rules.
func semantic(doc *Document, v *validator) {
	if doc.ProfileBody == nil || doc.ProfileBody.ApplicationProcess == nil {
		return
	}
	ap := doc.ProfileBody.ApplicationProcess
	const root = "ISO15745Profile/ProfileBody/ApplicationProcess"
	if ml := ap.ModuleList; ml != nil {
		idents := map[string]string{}
		for _, m := range ml.ModuleItems {
			p := root + "/ModuleList/ModuleItem[" + m.ID + "]"
			if n, err := parseUint(m.ModuleIdentNumber, 32); err == nil {
				key := strconv.FormatUint(n, 16)
				if prev, dup := idents[key]; dup {
					v.warn(p, RuleIdentNumber, "ModuleIdentNumber %#x is also the one of module %s", n, prev)
				} else {
					idents[key] = m.ID
				}
			}
			if m.VirtualSubmoduleList != nil {
				v.checkSubmodules(p+"/VirtualSubmoduleList", m.VirtualSubmoduleList.VirtualSubmoduleItems)
			}
		}
	}
	if ap.DeviceAccessPointList == nil {
		return
	}
	for _, dap := range ap.DeviceAccessPointList.DeviceAccessPointItems {
		p := root + "/DeviceAccessPointList/DeviceAccessPointItem[" + dap.ID + "]"
		if dap.VirtualSubmoduleList != nil {
			v.checkSubmodules(p+"/VirtualSubmoduleList", dap.VirtualSubmoduleList.VirtualSubmoduleItems)
		}
		physical, havePhysical := slotSet(dap.PhysicalSlots)
		if um := dap.UseableModules; um != nil {
			fixedBy := map[uint32]string{}
			for _, ref := range um.ModuleItemRefs {
				rp := p + "/UseableModules/ModuleItemRef[" + ref.ModuleItemTarget + "]"
				allowed, haveAllowed := slotSet(ref.AllowedInSlots)
				for name, list := range map[string]string{"AllowedInSlots": ref.AllowedInSlots, "UsedInSlots": ref.UsedInSlots, "FixedInSlots": ref.FixedInSlots} {
					set, ok := slotSet(list)
					if !ok {
						continue
					}
					for s := range set {
						if havePhysical && !physical[s] {
							v.warn(rp, RuleSlots, "%s: slot %d is not among the PhysicalSlots %q", name, s, dap.PhysicalSlots)
						}
						if name != "AllowedInSlots" && haveAllowed && !allowed[s] {
							v.warn(rp, RuleSlots, "%s: slot %d is not in AllowedInSlots %q", name, s, ref.AllowedInSlots)
						}
						if name == "FixedInSlots" {
							if prev, dup := fixedBy[s]; dup && prev != ref.ModuleItemTarget {
								v.warn(rp, RuleSlots, "slot %d is also fixed by module %s", s, prev)
							}
							fixedBy[s] = ref.ModuleItemTarget
						}
					}
				}
			}
		}
		sd := dap.SystemDefinedSubmoduleList
		if sd == nil {
			continue
		}
		for _, it := range sd.InterfaceSubmoduleItems {
			ip := p + "/SystemDefinedSubmoduleList/InterfaceSubmoduleItem[" + it.ID + "]"
			if n, err := parseUint(it.SubslotNumber, 16); err == nil && (n < 0x8000 || n > 0x8FFF) {
				v.warn(ip, RuleSubslot, "interface submodule in subslot %#x, outside 0x8000-0x8FFF", n)
			}
			if ar := it.ApplicationRelations; ar != nil && ar.TimingProperties != nil && ar.TimingProperties.ReductionRatio != "" {
				if rrs, err := ParseValueList(ar.TimingProperties.ReductionRatio, 1<<16); err == nil {
					for _, rr := range rrs {
						if rr < 1 || rr > 512 || rr&(rr-1) != 0 {
							v.warn(ip, RuleReductionRatio, "reduction ratio %d is not a power of two from 1 to 512", rr)
						}
					}
				}
			}
		}
		for _, it := range sd.PortSubmoduleItems {
			if n, err := parseUint(it.SubslotNumber, 16); err == nil && (n < 0x8000 || n > 0x8FFF) {
				v.warn(p+"/SystemDefinedSubmoduleList/PortSubmoduleItem["+it.ID+"]", RuleSubslot, "port submodule in subslot %#x, outside 0x8000-0x8FFF", n)
			}
		}
	}
}
