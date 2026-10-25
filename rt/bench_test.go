// SPDX-License-Identifier: MIT

package rt

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/otfabric/go-profinet/pnio"
)

func BenchmarkProviderTick(b *testing.B) {
	p := NewProvider(ProviderConfig{FrameID: 0xC000, DataLength: 256, Dst: macB, Src: macA, CounterStep: 32,
		Send: func(context.Context, []byte) error { return nil }, Fill: func(d []byte) { d[0]++ }})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p.Tick(ctx)
	}
}

func BenchmarkConsumerHandle(b *testing.B) {
	c := NewConsumer(ConsumerConfig{FrameID: 0xC000, DataLength: 256, Src: macA, Watchdog: time.Second})
	f := rxFrame(b, macA, 0xC000, make([]byte, 256), 0, DefaultDataStatus)
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Advance the cycle counter so that every frame is accepted.
		binary.BigEndian.PutUint16(f.Payload[2+256:], uint16(i+1))
		c.Handle(f, now)
	}
	if s := c.Stats(); s.Stale != 0 || s.Malformed != 0 {
		b.Fatalf("stats %+v", s)
	}
}

func BenchmarkComputeLayout(b *testing.B) {
	subs := []SubmoduleIO{{Slot: 0, Subslot: 1, Type: pnio.SubmoduleNoIO}}
	for i := 1; i <= 32; i++ {
		subs = append(subs, SubmoduleIO{Slot: uint16(i), Subslot: 1, Type: pnio.SubmoduleType(i % 4), InputLen: 4, OutputLen: 2})
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ComputeLayout(pnio.IOCRInput, subs); err != nil {
			b.Fatal(err)
		}
	}
}
