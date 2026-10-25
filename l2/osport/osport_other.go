// SPDX-License-Identifier: MIT

//go:build !linux && !darwin && !freebsd && !netbsd && !dragonfly && !windows

package osport

import (
	"fmt"
	"runtime"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/l2"
)

const backend = "none"

func open(string, Options) (l2.Port, error) {
	return nil, fmt.Errorf("osport: %w: no raw Ethernet backend for %s", profinet.ErrUnsupported, runtime.GOOS)
}
