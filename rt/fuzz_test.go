// SPDX-License-Identifier: MIT

package rt

import (
	"bytes"
	"testing"
)

func FuzzDecode(f *testing.F) {
	f.Add(AppendPayload(nil, &Frame{FrameID: 0xC000, Data: make([]byte, MinDataLength), CycleCounter: 32, DataStatus: DefaultDataStatus}), 40)
	f.Add(AppendPayload(nil, &Frame{FrameID: 0x8001, Data: []byte{1, 2, 3}, CycleCounter: 0xFFFF, DataStatus: 0x25, TransferStatus: 1}), -1)
	f.Add([]byte{0xc0, 0x00, 0, 1, 0x35}, -1)
	f.Add([]byte{}, 0)
	f.Fuzz(func(t *testing.T, p []byte, n int) {
		fr, err := Decode(p, n)
		if err != nil {
			return
		}
		if n >= 0 && len(fr.Data) != n {
			t.Fatalf("C_SDU of %d octets for %d", len(fr.Data), n)
		}
		// Re-encoding reproduces the decoded octets (without padding).
		out := AppendPayload(nil, &fr)
		if !bytes.Equal(out, p[:len(out)]) {
			t.Fatalf("re-encoded %x from %x", out, p)
		}
		again, err := Decode(out, len(fr.Data))
		if err != nil || again.FrameID != fr.FrameID || again.CycleCounter != fr.CycleCounter ||
			again.DataStatus != fr.DataStatus || again.TransferStatus != fr.TransferStatus || !bytes.Equal(again.Data, fr.Data) {
			t.Fatalf("round trip %+v, %v", again, err)
		}
	})
}
