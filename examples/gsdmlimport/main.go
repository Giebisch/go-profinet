// SPDX-License-Identifier: MIT

// Command gsdmlimport reads a GSDML file, validates it and converts its
// device access point into a device model, the expected configuration a
// controller connects with:
//
//	go run ./examples/gsdmlimport [FILE]
//
// Without FILE it reads pnio-rs's sample GSDML from gsdml/testdata.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/otfabric/go-profinet/gsdml"
)

func main() {
	path := "gsdml/testdata/pnio-rs_sample-16real-32bool.xml"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	doc, err := gsdml.Parse(f)
	if err != nil {
		log.Fatal(err)
	}
	if err := gsdml.Validate(doc); err != nil {
		var ve *gsdml.ValidationError
		if errors.As(err, &ve) {
			fmt.Printf("%d validation issue(s), first: %s\n", len(ve.Issues), ve.Issues[0])
		}
	} else {
		fmt.Println("valid (against go-profinet's catalog, not PI's checker)")
	}
	m, err := gsdml.ToModel(doc, "")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("device: vendor 0x%04X device 0x%04X %q, conformance class %s, min device interval %d\n",
		m.Identity.VendorID, m.Identity.DeviceID, m.Identity.TypeOfStation, m.Capabilities.ConformanceClass, m.Capabilities.MinDeviceInterval)
	fmt.Printf("DAP 0x%08X with %d submodule(s)\n", m.DAP.IdentNumber, len(m.DAP.Submodules))
	for _, s := range m.Slots {
		for _, sm := range s.Module.Submodules {
			fmt.Printf("slot %d: %q (0x%08X) subslot %d: %d octets in, %d out, %d record(s)\n",
				s.Number, s.Module.Name, s.Module.IdentNumber, sm.Subslot, sm.InputLen, sm.OutputLen, len(sm.Records))
		}
	}
	fmt.Printf("%d module type(s) usable\n", len(m.ModuleTypes()))
}
