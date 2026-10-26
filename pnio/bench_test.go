// SPDX-License-Identifier: MIT

package pnio

import "testing"

func BenchmarkConnectRequestAppend(b *testing.B) {
	c := testConnectRequest()
	buf := make([]byte, 0, 1024)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf = c.Append(buf[:0])
	}
	b.SetBytes(int64(len(buf)))
}

func BenchmarkDecodeConnectRequest(b *testing.B) {
	c := testConnectRequest()
	buf := c.Append(nil)
	b.ReportAllocs()
	b.SetBytes(int64(len(buf)))
	for i := 0; i < b.N; i++ {
		if _, err := DecodeConnectRequest(buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeReadReq(b *testing.B) {
	q := testReadReq()
	buf := q.Append(nil)
	b.ReportAllocs()
	b.SetBytes(int64(len(buf)))
	for i := 0; i < b.N; i++ {
		blk, _, err := ReadBlock(buf)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := DecodeReadReq(blk); err != nil {
			b.Fatal(err)
		}
	}
}
