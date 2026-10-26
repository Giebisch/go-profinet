// SPDX-License-Identifier: MIT

//go:build windows

package osport

import (
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/npcap"
)

const backend = "npcap"

func open(name string, opts Options) (l2.Port, error) {
	return npcap.Open(name, npcap.Options{Promiscuous: opts.Promiscuous})
}
