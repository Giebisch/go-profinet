// SPDX-License-Identifier: MIT

package rt

import (
	"fmt"
	"sort"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/pnio"
)

// SubmoduleIO is the IO data shape of one submodule.
type SubmoduleIO struct {
	API     uint32
	Slot    uint16
	Subslot uint16
	Type    pnio.SubmoduleType
	// InputLen and OutputLen are the data lengths without IOxS.
	InputLen  uint16
	OutputLen uint16
}

// Object places the data of one submodule and its provider status (IOPS,
// the octet after the data) in a C_SDU.
type Object struct {
	API           uint32
	Slot, Subslot uint16
	Offset        uint16
	Length        uint16
}

// IOPSOffset returns the offset of the object's provider status.
func (o Object) IOPSOffset() uint16 { return o.Offset + o.Length }

// Status places one consumer status octet (IOCS) in a C_SDU.
type Status struct {
	API           uint32
	Slot, Subslot uint16
	Offset        uint16
}

// Layout is the arrangement of the C_SDU of one IOCR.
type Layout struct {
	DataLength uint16
	Objects    []Object
	IOCS       []Status
}

// ProvidesData reports whether a submodule of type t has a data object in
// an IOCR of kind k: input (and NO_IO) submodules in the input CR, output
// submodules in the output CR.
func ProvidesData(k pnio.IOCRType, t pnio.SubmoduleType) bool {
	if k == pnio.IOCRInput {
		return t == pnio.SubmoduleNoIO || t.HasInput()
	}
	return t.HasOutput()
}

// ConsumesStatus reports whether a submodule of type t has an IOCS in an
// IOCR of kind k: the IOCS of output data travels in the input CR and the
// IOCS of input (and NO_IO) data in the output CR.
func ConsumesStatus(k pnio.IOCRType, t pnio.SubmoduleType) bool {
	if k == pnio.IOCRInput {
		return t.HasOutput()
	}
	return t == pnio.SubmoduleNoIO || t.HasInput()
}

// ComputeLayout arranges the C_SDU of an IOCR of kind k (IOCRInput or
// IOCROutput): the data objects in submodule order, each followed by its
// IOPS, then the IOCS octets. DataLength is at least MinDataLength.
func ComputeLayout(k pnio.IOCRType, subs []SubmoduleIO) (Layout, error) {
	var l Layout
	off := 0
	for _, s := range subs {
		if !ProvidesData(k, s.Type) {
			continue
		}
		n := s.OutputLen
		if k == pnio.IOCRInput {
			n = s.InputLen
		}
		l.Objects = append(l.Objects, Object{API: s.API, Slot: s.Slot, Subslot: s.Subslot, Offset: uint16(off), Length: n})
		off += int(n) + 1
		if off > MaxDataLength {
			return l, fmt.Errorf("rt: %w: IO data of %d octets exceeds the C_SDU maximum %d", profinet.ErrInvalidConfig, off, MaxDataLength)
		}
	}
	for _, s := range subs {
		if !ConsumesStatus(k, s.Type) {
			continue
		}
		l.IOCS = append(l.IOCS, Status{API: s.API, Slot: s.Slot, Subslot: s.Subslot, Offset: uint16(off)})
		off++
		if off > MaxDataLength {
			return l, fmt.Errorf("rt: %w: IO data of %d octets exceeds the C_SDU maximum %d", profinet.ErrInvalidConfig, off, MaxDataLength)
		}
	}
	l.DataLength = uint16(max(off, MinDataLength))
	return l, nil
}

// IOCRAPIs returns the API lists of an IOCRBlockReq for the layout.
func (l *Layout) IOCRAPIs() []pnio.IOCRAPI {
	idx := map[uint32]int{}
	var out []pnio.IOCRAPI
	get := func(api uint32) *pnio.IOCRAPI {
		i, ok := idx[api]
		if !ok {
			i = len(out)
			idx[api] = i
			out = append(out, pnio.IOCRAPI{API: api})
		}
		return &out[i]
	}
	for _, o := range l.Objects {
		a := get(o.API)
		a.IODataObjects = append(a.IODataObjects, pnio.IODataObject{Slot: o.Slot, Subslot: o.Subslot, FrameOffset: o.Offset})
	}
	for _, s := range l.IOCS {
		a := get(s.API)
		a.IOCS = append(a.IOCS, pnio.IODataObject{Slot: s.Slot, Subslot: s.Subslot, FrameOffset: s.Offset})
	}
	return out
}

type subKey struct {
	api           uint32
	slot, subslot uint16
}

// LayoutFromIOCR validates the offsets a controller put in an IOCR
// against the submodule shapes and returns the layout. Every data object
// and IOCS must lie inside DataLength and no two may overlap; a
// submodule that the IOCR does not list is left out of the layout.
func LayoutFromIOCR(req *pnio.IOCRBlockReq, subs []SubmoduleIO) (Layout, error) {
	shapes := map[subKey]SubmoduleIO{}
	for _, s := range subs {
		shapes[subKey{s.API, s.Slot, s.Subslot}] = s
	}
	l := Layout{DataLength: req.DataLength}
	if req.DataLength < MinDataLength || req.DataLength > MaxDataLength {
		return l, fmt.Errorf("rt: %w: DataLength %d", profinet.ErrInvalidConfig, req.DataLength)
	}
	type span struct{ lo, hi int }
	var spans []span
	add := func(lo, n int) error {
		if lo+n > int(req.DataLength) {
			return fmt.Errorf("rt: %w: IO object at %d+%d beyond DataLength %d", profinet.ErrInvalidConfig, lo, n, req.DataLength)
		}
		spans = append(spans, span{lo, lo + n})
		return nil
	}
	for _, a := range req.APIs {
		for _, o := range a.IODataObjects {
			s, ok := shapes[subKey{a.API, o.Slot, o.Subslot}]
			if !ok {
				return l, fmt.Errorf("rt: %w: IO data object for unknown submodule %d/%d/%d", profinet.ErrInvalidConfig, a.API, o.Slot, o.Subslot)
			}
			n := s.OutputLen
			if req.Type == pnio.IOCRInput {
				n = s.InputLen
			}
			if err := add(int(o.FrameOffset), int(n)+1); err != nil {
				return l, err
			}
			l.Objects = append(l.Objects, Object{API: a.API, Slot: o.Slot, Subslot: o.Subslot, Offset: o.FrameOffset, Length: n})
		}
		for _, o := range a.IOCS {
			if _, ok := shapes[subKey{a.API, o.Slot, o.Subslot}]; !ok {
				return l, fmt.Errorf("rt: %w: IOCS for unknown submodule %d/%d/%d", profinet.ErrInvalidConfig, a.API, o.Slot, o.Subslot)
			}
			if err := add(int(o.FrameOffset), 1); err != nil {
				return l, err
			}
			l.IOCS = append(l.IOCS, Status{API: a.API, Slot: o.Slot, Subslot: o.Subslot, Offset: o.FrameOffset})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].lo < spans[j].lo })
	for i := 1; i < len(spans); i++ {
		if spans[i].lo < spans[i-1].hi {
			return l, fmt.Errorf("rt: %w: overlapping IO objects at offset %d", profinet.ErrInvalidConfig, spans[i].lo)
		}
	}
	return l, nil
}

// Find returns the data object of a submodule.
func (l *Layout) Find(api uint32, slot, subslot uint16) (Object, bool) {
	for _, o := range l.Objects {
		if o.API == api && o.Slot == slot && o.Subslot == subslot {
			return o, true
		}
	}
	return Object{}, false
}

// FindIOCS returns the IOCS of a submodule.
func (l *Layout) FindIOCS(api uint32, slot, subslot uint16) (Status, bool) {
	for _, s := range l.IOCS {
		if s.API == api && s.Slot == slot && s.Subslot == subslot {
			return s, true
		}
	}
	return Status{}, false
}
