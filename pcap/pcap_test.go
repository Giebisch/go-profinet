// SPDX-License-Identifier: MIT

package pcap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-profinet"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	h := buf.Bytes()
	if len(h) != 24 || binary.LittleEndian.Uint32(h) != magicMicro ||
		binary.LittleEndian.Uint16(h[4:]) != 2 || binary.LittleEndian.Uint16(h[6:]) != 4 ||
		binary.LittleEndian.Uint32(h[16:]) != SnapLen || binary.LittleEndian.Uint32(h[20:]) != linkEthernet {
		t.Fatalf("header % x", h)
	}
	t0 := time.Unix(1700000000, 123456789)
	frames := [][]byte{
		bytes.Repeat([]byte{1}, 60),
		{},
		bytes.Repeat([]byte{2}, 1518),
	}
	for i, f := range frames {
		if err := w.WriteFrame(t0.Add(time.Duration(i)*time.Second), f); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range frames {
		ts, got, err := r.Next()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		// Microsecond resolution.
		want := t0.Add(time.Duration(i) * time.Second).Truncate(time.Microsecond)
		if !ts.Equal(want) {
			t.Fatalf("frame %d: ts %v, want %v", i, ts, want)
		}
		if !bytes.Equal(got, f) {
			t.Fatalf("frame %d differs", i)
		}
	}
	if _, _, err := r.Next(); err != io.EOF {
		t.Fatalf("err %v, want io.EOF", err)
	}
}

func TestSnapLen(t *testing.T) {
	var buf bytes.Buffer
	w, _ := NewWriter(&buf)
	big := make([]byte, SnapLen+10)
	big[SnapLen-1] = 0xAB
	if err := w.WriteFrame(time.Unix(1, 0), big); err != nil {
		t.Fatal(err)
	}
	rec := buf.Bytes()[24:]
	if incl, orig := binary.LittleEndian.Uint32(rec[8:]), binary.LittleEndian.Uint32(rec[12:]); incl != SnapLen || orig != SnapLen+10 {
		t.Fatalf("incl %d orig %d", incl, orig)
	}
	r, _ := NewReader(bytes.NewReader(buf.Bytes()))
	_, got, err := r.Next()
	if err != nil || len(got) != SnapLen || got[SnapLen-1] != 0xAB {
		t.Fatalf("len %d err %v", len(got), err)
	}
}

type failWriter struct{ after int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("disk full")
	}
	f.after--
	return len(p), nil
}

func TestWriterErrors(t *testing.T) {
	if _, err := NewWriter(&failWriter{}); err == nil {
		t.Fatal("header write error not reported")
	}
	w, err := NewWriter(&failWriter{after: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(time.Now(), []byte{1}); err == nil {
		t.Fatal("record write error not reported")
	}
}

func TestWriterConcurrent(t *testing.T) {
	var buf bytes.Buffer
	w, _ := NewWriter(&buf)
	var wg sync.WaitGroup
	const n, per = 8, 50
	for g := 0; g < n; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			f := bytes.Repeat([]byte{byte(g)}, 60+g)
			for i := 0; i < per; i++ {
				if err := w.WriteFrame(time.Now(), f); err != nil {
					t.Error(err)
				}
			}
		}(g)
	}
	wg.Wait()
	r, _ := NewReader(&buf)
	count := 0
	for {
		_, f, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// Records are not interleaved: each holds one goroutine's bytes.
		g := int(f[0])
		if len(f) != 60+g || !bytes.Equal(f, bytes.Repeat([]byte{f[0]}, len(f))) {
			t.Fatalf("record %d corrupt", count)
		}
		count++
	}
	if count != n*per {
		t.Fatalf("read %d records", count)
	}
}

// file builds a pcap file with the given byte order and magic.
func file(order binary.ByteOrder, magic uint32, link uint32, recs ...[]byte) []byte {
	h := make([]byte, 24)
	order.PutUint32(h[0:], magic)
	order.PutUint16(h[4:], 2)
	order.PutUint16(h[6:], 4)
	order.PutUint32(h[16:], SnapLen)
	order.PutUint32(h[20:], link)
	for _, r := range recs {
		h = append(h, r...)
	}
	return h
}

func record(order binary.ByteOrder, sec, sub uint32, data []byte) []byte {
	r := make([]byte, 16)
	order.PutUint32(r[0:], sec)
	order.PutUint32(r[4:], sub)
	order.PutUint32(r[8:], uint32(len(data)))
	order.PutUint32(r[12:], uint32(len(data)))
	return append(r, data...)
}

func TestReaderVariants(t *testing.T) {
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	cases := []struct {
		name  string
		order binary.ByteOrder
		magic uint32
		sub   uint32
		want  time.Time
	}{
		{"le-micro", binary.LittleEndian, magicMicro, 250000, time.Unix(100, 250000000)},
		{"be-micro", binary.BigEndian, magicMicro, 250000, time.Unix(100, 250000000)},
		{"le-nano", binary.LittleEndian, 0xA1B23C4D, 123456789, time.Unix(100, 123456789)},
		{"be-nano", binary.BigEndian, 0xA1B23C4D, 123456789, time.Unix(100, 123456789)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := file(c.order, c.magic, linkEthernet, record(c.order, 100, c.sub, data))
			r, err := NewReader(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			if r.order != c.order {
				t.Fatalf("order %v", r.order)
			}
			ts, got, err := r.Next()
			if err != nil || !ts.Equal(c.want) || !bytes.Equal(got, data) {
				t.Fatalf("ts %v data % x err %v", ts, got, err)
			}
		})
	}
}

func TestReaderErrors(t *testing.T) {
	le := binary.LittleEndian
	cases := []struct {
		name string
		b    []byte
		want error
	}{
		{"empty", nil, profinet.ErrMalformed},
		{"short header", make([]byte, 23), profinet.ErrMalformed},
		{"bad magic", file(le, 0x12345678, linkEthernet), profinet.ErrMalformed},
		{"link type", file(le, magicMicro, 101), profinet.ErrUnsupported},
		{"link type be", file(binary.BigEndian, magicMicro, 0), profinet.ErrUnsupported},
	}
	for _, c := range cases {
		if _, err := NewReader(bytes.NewReader(c.b)); !errors.Is(err, c.want) {
			t.Errorf("%s: err %v, want %v", c.name, err, c.want)
		}
	}

	rec := record(le, 1, 0, []byte{1, 2, 3, 4})
	big := record(le, 1, 0, nil)
	le.PutUint32(big[8:], SnapLen+1)
	next := []struct {
		name string
		b    []byte
	}{
		{"truncated record header", file(le, magicMicro, linkEthernet, rec[:10])},
		{"truncated record", file(le, magicMicro, linkEthernet, rec[:18])},
		{"truncated empty record", file(le, magicMicro, linkEthernet, rec[:16])},
		{"oversized record", file(le, magicMicro, linkEthernet, big)},
	}
	for _, c := range next {
		r, err := NewReader(bytes.NewReader(c.b))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, _, err := r.Next(); !errors.Is(err, profinet.ErrMalformed) {
			t.Errorf("%s: err %v", c.name, err)
		}
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestReaderPassesIOError(t *testing.T) {
	boom := errors.New("boom")
	r, err := NewReader(io.MultiReader(bytes.NewReader(file(binary.LittleEndian, magicMicro, linkEthernet)), errReader{boom}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Next(); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	var buf bytes.Buffer
	w, _ := NewWriter(&buf)
	_ = w.WriteFrame(time.Unix(1, 1000), bytes.Repeat([]byte{7}, 60))
	_ = w.WriteFrame(time.Unix(2, 0), nil)
	f.Add(buf.Bytes())
	f.Add(file(binary.BigEndian, 0xA1B23C4D, linkEthernet, record(binary.BigEndian, 5, 6, []byte{1, 2})))
	f.Add(file(binary.LittleEndian, magicMicro, 2))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := NewReader(bytes.NewReader(b))
		if err != nil {
			if !errors.Is(err, profinet.ErrMalformed) && !errors.Is(err, profinet.ErrUnsupported) {
				t.Fatalf("unexpected header error %v", err)
			}
			return
		}
		// Re-encode every record and check the result reads back the same.
		var out bytes.Buffer
		ww, err := NewWriter(&out)
		if err != nil {
			t.Fatal(err)
		}
		var frames [][]byte
		var times []time.Time
		for {
			ts, fr, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				if !errors.Is(err, profinet.ErrMalformed) {
					t.Fatalf("unexpected record error %v", err)
				}
				break
			}
			if len(fr) > SnapLen {
				t.Fatalf("record of %d octets", len(fr))
			}
			frames = append(frames, fr)
			times = append(times, ts)
			if err := ww.WriteFrame(ts, fr); err != nil {
				t.Fatal(err)
			}
		}
		rr, err := NewReader(&out)
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range frames {
			ts, fr, err := rr.Next()
			if err != nil {
				t.Fatalf("re-read %d: %v", i, err)
			}
			if !bytes.Equal(fr, want) {
				t.Fatalf("re-read %d differs", i)
			}
			// The writer stores 32-bit seconds and microseconds.
			if wantTS := time.Unix(int64(uint32(times[i].Unix())), int64(times[i].Nanosecond()/1000*1000)); !ts.Equal(wantTS) {
				t.Fatalf("re-read %d: ts %v, want %v", i, ts, wantTS)
			}
		}
		if _, _, err := rr.Next(); err != io.EOF {
			t.Fatalf("trailing data: %v", err)
		}
	})
}
