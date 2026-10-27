// SPDX-License-Identifier: MIT

//go:build linux && 386

package afpacket

import "syscall"

// recvmsg receives one frame with syscall.Recvmsg: linux/386 multiplexes
// the socket calls through socketcall, which package syscall does not
// export, so this path allocates the source address.
func recvmsg(fd uintptr, p *Port, buf []byte) (n, oobn int, pkttype uint8, err error) {
	n, oobn, _, from, err := syscall.Recvmsg(int(fd), buf, p.oob[:], 0)
	if err != nil {
		return 0, 0, 0, err
	}
	if ll, ok := from.(*syscall.SockaddrLinklayer); ok {
		pkttype = ll.Pkttype
	}
	return n, oobn, pkttype, nil
}
