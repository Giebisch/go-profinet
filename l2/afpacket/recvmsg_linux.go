// SPDX-License-Identifier: MIT

//go:build linux && !386

package afpacket

import (
	"syscall"
	"unsafe"
)

// recvmsg receives one frame into the port's message (p.msg, prepared by
// recv), without allocating.
func recvmsg(fd uintptr, p *Port, _ []byte) (n, oobn int, pkttype uint8, err error) {
	r, _, e := syscall.Syscall(syscall.SYS_RECVMSG, fd, uintptr(unsafe.Pointer(&p.msg)), 0)
	if e != 0 {
		return 0, 0, 0, e
	}
	return int(r), int(p.msg.Controllen), p.from.Pkttype, nil
}
