// SPDX-License-Identifier: MIT

//go:build !linux

package main

import "errors"

// runMRP needs Linux: veth ring ports and the signals that drive probes.
func runMRP([]string) error { return errors.New("otfnode mrp runs on Linux only") }
