// SPDX-License-Identifier: MIT

package rt

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/pnio"
)

// testSubs is a DAP (NO_IO submodules), an input, an output and an
// input/output submodule, one of them in another API.
func testSubs() []SubmoduleIO {
	return []SubmoduleIO{
		{API: 0, Slot: 0, Subslot: 1, Type: pnio.SubmoduleNoIO},
		{API: 0, Slot: 1, Subslot: 1, Type: pnio.SubmoduleInput, InputLen: 4},
		{API: 0, Slot: 2, Subslot: 1, Type: pnio.SubmoduleOutput, OutputLen: 2},
		{API: 0x3A00, Slot: 3, Subslot: 1, Type: pnio.SubmoduleInputOutput, InputLen: 3, OutputLen: 5},
	}
}

func TestProvidesConsumes(t *testing.T) {
	for _, c := range []struct {
		t                                pnio.SubmoduleType
		inData, inIOCS, outData, outIOCS bool
	}{
		{pnio.SubmoduleNoIO, true, false, false, true},
		{pnio.SubmoduleInput, true, false, false, true},
		{pnio.SubmoduleOutput, false, true, true, false},
		{pnio.SubmoduleInputOutput, true, true, true, true},
	} {
		if ProvidesData(pnio.IOCRInput, c.t) != c.inData || ConsumesStatus(pnio.IOCRInput, c.t) != c.inIOCS ||
			ProvidesData(pnio.IOCROutput, c.t) != c.outData || ConsumesStatus(pnio.IOCROutput, c.t) != c.outIOCS {
			t.Errorf("%s: wrong placement", c.t)
		}
	}
}

func TestComputeLayoutInput(t *testing.T) {
	l, err := ComputeLayout(pnio.IOCRInput, testSubs())
	if err != nil {
		t.Fatal(err)
	}
	// Data objects in submodule order, each followed by its IOPS: the
	// NO_IO submodule has an IOPS only.
	wantObjects := []Object{
		{API: 0, Slot: 0, Subslot: 1, Offset: 0, Length: 0},
		{API: 0, Slot: 1, Subslot: 1, Offset: 1, Length: 4},
		{API: 0x3A00, Slot: 3, Subslot: 1, Offset: 6, Length: 3},
	}
	// Then the IOCS of the output data.
	wantIOCS := []Status{
		{API: 0, Slot: 2, Subslot: 1, Offset: 10},
		{API: 0x3A00, Slot: 3, Subslot: 1, Offset: 11},
	}
	if !reflect.DeepEqual(l.Objects, wantObjects) || !reflect.DeepEqual(l.IOCS, wantIOCS) {
		t.Fatalf("layout\n objects %+v\n iocs %+v", l.Objects, l.IOCS)
	}
	// 12 octets of IO data, raised to the minimum.
	if l.DataLength != MinDataLength {
		t.Fatalf("DataLength %d", l.DataLength)
	}
	if l.Objects[1].IOPSOffset() != 5 || l.Objects[0].IOPSOffset() != 0 {
		t.Fatalf("IOPS offsets %d %d", l.Objects[0].IOPSOffset(), l.Objects[1].IOPSOffset())
	}
}

func TestComputeLayoutOutput(t *testing.T) {
	l, err := ComputeLayout(pnio.IOCROutput, testSubs())
	if err != nil {
		t.Fatal(err)
	}
	wantObjects := []Object{
		{API: 0, Slot: 2, Subslot: 1, Offset: 0, Length: 2},
		{API: 0x3A00, Slot: 3, Subslot: 1, Offset: 3, Length: 5},
	}
	wantIOCS := []Status{
		{API: 0, Slot: 0, Subslot: 1, Offset: 9},
		{API: 0, Slot: 1, Subslot: 1, Offset: 10},
		{API: 0x3A00, Slot: 3, Subslot: 1, Offset: 11},
	}
	if !reflect.DeepEqual(l.Objects, wantObjects) || !reflect.DeepEqual(l.IOCS, wantIOCS) {
		t.Fatalf("layout\n objects %+v\n iocs %+v", l.Objects, l.IOCS)
	}
	if l.DataLength != MinDataLength {
		t.Fatalf("DataLength %d", l.DataLength)
	}
}

func TestComputeLayoutSizes(t *testing.T) {
	// More than the minimum: DataLength is the sum.
	l, err := ComputeLayout(pnio.IOCRInput, []SubmoduleIO{{Type: pnio.SubmoduleInput, InputLen: 100}, {Subslot: 2, Type: pnio.SubmoduleOutput}})
	if err != nil || l.DataLength != 102 {
		t.Fatalf("DataLength %d, %v", l.DataLength, err)
	}
	// Exactly the maximum.
	l, err = ComputeLayout(pnio.IOCRInput, []SubmoduleIO{{Type: pnio.SubmoduleInput, InputLen: MaxDataLength - 1}})
	if err != nil || l.DataLength != MaxDataLength {
		t.Fatalf("maximum: %d, %v", l.DataLength, err)
	}
	// No submodules at all.
	l, err = ComputeLayout(pnio.IOCROutput, nil)
	if err != nil || l.DataLength != MinDataLength || l.Objects != nil || l.IOCS != nil {
		t.Fatalf("empty: %+v, %v", l, err)
	}

	// One octet too many in the data.
	_, err = ComputeLayout(pnio.IOCRInput, []SubmoduleIO{{Type: pnio.SubmoduleInput, InputLen: MaxDataLength}})
	if !errors.Is(err, profinet.ErrInvalidConfig) || !strings.Contains(err.Error(), "1441") {
		t.Fatalf("overflow: %v", err)
	}
	// One octet too many through an IOCS.
	_, err = ComputeLayout(pnio.IOCRInput, []SubmoduleIO{
		{Type: pnio.SubmoduleInput, InputLen: MaxDataLength - 1},
		{Subslot: 2, Type: pnio.SubmoduleOutput, OutputLen: 1},
	})
	if !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("IOCS overflow: %v", err)
	}
	// Overflow over many submodules (where a uint16 offset would wrap).
	var many []SubmoduleIO
	for i := 0; i < 100; i++ {
		many = append(many, SubmoduleIO{Slot: uint16(i), Type: pnio.SubmoduleInput, InputLen: 1000})
	}
	if _, err := ComputeLayout(pnio.IOCRInput, many); !errors.Is(err, profinet.ErrInvalidConfig) {
		t.Fatalf("many: %v", err)
	}
}

func TestIOCRAPIs(t *testing.T) {
	l, err := ComputeLayout(pnio.IOCRInput, testSubs())
	if err != nil {
		t.Fatal(err)
	}
	apis := l.IOCRAPIs()
	want := []pnio.IOCRAPI{
		{API: 0,
			IODataObjects: []pnio.IODataObject{{Slot: 0, Subslot: 1, FrameOffset: 0}, {Slot: 1, Subslot: 1, FrameOffset: 1}},
			IOCS:          []pnio.IODataObject{{Slot: 2, Subslot: 1, FrameOffset: 10}}},
		{API: 0x3A00,
			IODataObjects: []pnio.IODataObject{{Slot: 3, Subslot: 1, FrameOffset: 6}},
			IOCS:          []pnio.IODataObject{{Slot: 3, Subslot: 1, FrameOffset: 11}}},
	}
	if !reflect.DeepEqual(apis, want) {
		t.Fatalf("IOCRAPIs\n got %+v\nwant %+v", apis, want)
	}
	// An API that only has IOCS octets.
	l = Layout{IOCS: []Status{{API: 7, Slot: 1, Subslot: 1, Offset: 0}}}
	if apis := l.IOCRAPIs(); len(apis) != 1 || apis[0].API != 7 || apis[0].IODataObjects != nil || len(apis[0].IOCS) != 1 {
		t.Fatalf("IOCS only %+v", apis)
	}
	if apis := (&Layout{}).IOCRAPIs(); apis != nil {
		t.Fatalf("empty %+v", apis)
	}
}

func iocrFor(t *testing.T, k pnio.IOCRType, subs []SubmoduleIO) pnio.IOCRBlockReq {
	t.Helper()
	l, err := ComputeLayout(k, subs)
	if err != nil {
		t.Fatal(err)
	}
	return pnio.IOCRBlockReq{Type: k, DataLength: l.DataLength, APIs: l.IOCRAPIs()}
}

func TestLayoutFromIOCR(t *testing.T) {
	for _, k := range []pnio.IOCRType{pnio.IOCRInput, pnio.IOCROutput} {
		want, err := ComputeLayout(k, testSubs())
		if err != nil {
			t.Fatal(err)
		}
		req := iocrFor(t, k, testSubs())
		got, err := LayoutFromIOCR(&req, testSubs())
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: LayoutFromIOCR\n got %+v\nwant %+v", k, got, want)
		}
	}

	// A submodule the IOCR does not list is left out; extra shapes are
	// fine.
	req := pnio.IOCRBlockReq{Type: pnio.IOCRInput, DataLength: 40, APIs: []pnio.IOCRAPI{{API: 0, IODataObjects: []pnio.IODataObject{{Slot: 1, Subslot: 1, FrameOffset: 20}}}}}
	l, err := LayoutFromIOCR(&req, testSubs())
	if err != nil || len(l.Objects) != 1 || l.Objects[0].Offset != 20 || l.Objects[0].Length != 4 || l.IOCS != nil || l.DataLength != 40 {
		t.Fatalf("partial %+v, %v", l, err)
	}
	// Objects that touch but do not overlap, ending exactly at DataLength.
	req.APIs[0].IODataObjects = []pnio.IODataObject{{Slot: 1, Subslot: 1, FrameOffset: 35}}
	req.APIs[0].IOCS = []pnio.IODataObject{{Slot: 2, Subslot: 1, FrameOffset: 34}}
	if _, err := LayoutFromIOCR(&req, testSubs()); err != nil {
		t.Fatalf("adjacent: %v", err)
	}
}

func TestLayoutFromIOCRErrors(t *testing.T) {
	base := func() pnio.IOCRBlockReq { return iocrFor(t, pnio.IOCRInput, testSubs()) }
	for name, c := range map[string]struct {
		mod  func(*pnio.IOCRBlockReq)
		text string
	}{
		"DataLength below minimum": {func(r *pnio.IOCRBlockReq) { r.DataLength = MinDataLength - 1 }, "DataLength 39"},
		"DataLength above maximum": {func(r *pnio.IOCRBlockReq) { r.DataLength = MaxDataLength + 1 }, "DataLength 1441"},
		"unknown data submodule":   {func(r *pnio.IOCRBlockReq) { r.APIs[0].IODataObjects[1].Slot = 9 }, "IO data object for unknown submodule 0/9/1"},
		"unknown API":              {func(r *pnio.IOCRBlockReq) { r.APIs[1].API = 5 }, "unknown submodule 5/3/1"},
		"unknown IOCS submodule":   {func(r *pnio.IOCRBlockReq) { r.APIs[0].IOCS[0].Subslot = 9 }, "IOCS for unknown submodule 0/2/9"},
		"data beyond DataLength":   {func(r *pnio.IOCRBlockReq) { r.APIs[0].IODataObjects[1].FrameOffset = 36 }, "IO object at 36+5 beyond DataLength 40"},
		"IOCS beyond DataLength":   {func(r *pnio.IOCRBlockReq) { r.APIs[0].IOCS[0].FrameOffset = 40 }, "IO object at 40+1 beyond DataLength 40"},
		"data overlap":             {func(r *pnio.IOCRBlockReq) { r.APIs[0].IODataObjects[1].FrameOffset = 0 }, "overlapping IO objects at offset"},
		"IOCS overlaps data":       {func(r *pnio.IOCRBlockReq) { r.APIs[1].IOCS[0].FrameOffset = 3 }, "overlapping IO objects at offset 3"},
		"IOCS overlaps IOPS":       {func(r *pnio.IOCRBlockReq) { r.APIs[1].IOCS[0].FrameOffset = 5 }, "overlapping IO objects at offset 5"},
	} {
		t.Run(name, func(t *testing.T) {
			r := base()
			c.mod(&r)
			_, err := LayoutFromIOCR(&r, testSubs())
			if !errors.Is(err, profinet.ErrInvalidConfig) || !strings.Contains(err.Error(), c.text) {
				t.Fatalf("error %v, want %q", err, c.text)
			}
		})
	}
}

func TestFind(t *testing.T) {
	l, err := ComputeLayout(pnio.IOCRInput, testSubs())
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := l.Find(0x3A00, 3, 1); !ok || o.Offset != 6 || o.Length != 3 {
		t.Fatalf("Find %+v %v", o, ok)
	}
	for _, k := range [][3]uint32{{0, 3, 1}, {0x3A00, 3, 2}, {0, 2, 1}} {
		if _, ok := l.Find(k[0], uint16(k[1]), uint16(k[2])); ok {
			t.Errorf("Find(%v) found", k)
		}
	}
	if s, ok := l.FindIOCS(0, 2, 1); !ok || s.Offset != 10 {
		t.Fatalf("FindIOCS %+v %v", s, ok)
	}
	for _, k := range [][3]uint32{{0, 1, 1}, {1, 2, 1}, {0, 2, 2}} {
		if _, ok := l.FindIOCS(k[0], uint16(k[1]), uint16(k[2])); ok {
			t.Errorf("FindIOCS(%v) found", k)
		}
	}
}
