// SPDX-License-Identifier: MIT

//go:build darwin || freebsd || netbsd || dragonfly

package osport

import (
	"github.com/otfabric/go-profinet/l2"
	"github.com/otfabric/go-profinet/l2/bpf"
)

const backend = "bpf"

func open(name string, opts Options) (l2.Port, error) {
	return bpf.Open(name, bpf.Options{Promiscuous: opts.Promiscuous})
}
