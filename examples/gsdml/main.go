// SPDX-License-Identifier: MIT

// Command gsdml writes the GSDML file of the example device to standard
// output: the file an engineering tool imports to configure the device.
// With -o DIR it writes it under its conventional name instead.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/gsdml"
)

func main() {
	dir := flag.String("o", "", "write the file to this directory")
	flag.Parse()
	m := vnetenv.Model()
	doc, err := gsdml.FromModel(m, gsdml.GenerateOptions{SupervisorAR: true})
	if err != nil {
		log.Fatal(err)
	}
	b, err := gsdml.Marshal(doc)
	if err != nil {
		log.Fatal(err)
	}
	if *dir == "" {
		_, _ = os.Stdout.Write(b)
		return
	}
	name := filepath.Join(*dir, gsdml.FileName(m, "", time.Now()))
	if err := os.WriteFile(name, b, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", name)
}
