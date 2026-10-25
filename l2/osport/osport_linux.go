// SPDX-License-Identifier: MIT

//go:build linux

package osport

import (
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/afpacket"
)

const backend = "afpacket"

func open(name string, opts Options) (l2.Port, error) {
	return afpacket.Open(name, afpacket.Options{Promiscuous: opts.Promiscuous})
}
