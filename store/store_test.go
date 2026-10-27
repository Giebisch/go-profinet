// SPDX-License-Identifier: MIT

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/otfabric/go-profinet"
)

var (
	_ Store = (*Memory)(nil)
	_ Store = (*Dir)(nil)
)

func TestValidKey(t *testing.T) {
	for _, k := range []string{"device", "a", "im-1", "x_y.z", "0", string(bytes.Repeat([]byte("a"), MaxKeyLen))} {
		if err := ValidKey(k); err != nil {
			t.Errorf("%q: %v", k, err)
		}
	}
	for _, k := range []string{"", ".hidden", "A", "a/b", `a\b`, "a b", "ä", "..", string(bytes.Repeat([]byte("a"), MaxKeyLen+1))} {
		if err := ValidKey(k); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Errorf("%q: %v", k, err)
		}
	}
}

// contract runs the behaviour every Store has.
func contract(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Load(ctx, "k"); !errors.Is(err, ErrNotFound) || !errors.Is(err, profinet.ErrNotFound) {
		t.Fatalf("empty Load: %v", err)
	}
	if err := s.Save(ctx, "k", []byte("one")); err != nil {
		t.Fatal(err)
	}
	v, err := s.Load(ctx, "k")
	if err != nil || string(v) != "one" {
		t.Fatalf("Load %q %v", v, err)
	}
	// The value is a copy.
	v[0] = 'X'
	if v, _ := s.Load(ctx, "k"); string(v) != "one" {
		t.Fatalf("Load returned the stored slice: %q", v)
	}
	if err := s.Save(ctx, "k", nil); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Load(ctx, "k"); err != nil || len(v) != 0 {
		t.Fatalf("empty value %q %v", v, err)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if _, err := s.Load(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after Delete: %v", err)
	}
	for _, f := range []func() error{
		func() error { _, err := s.Load(ctx, "Bad"); return err },
		func() error { return s.Save(ctx, "../x", nil) },
		func() error { return s.Delete(ctx, "") },
	} {
		if err := f(); !errors.Is(err, profinet.ErrInvalidConfig) {
			t.Fatalf("invalid key: %v", err)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Save(cctx, "k", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Save: %v", err)
	}
	// Concurrent use.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := fmt.Sprintf("c%d", i%3)
			for j := range 20 {
				if err := s.Save(ctx, k, []byte(fmt.Sprint(j))); err != nil {
					t.Error(err)
					return
				}
				if _, err := s.Load(ctx, k); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestMemory(t *testing.T) {
	m := NewMemory()
	contract(t, m)
	if k := m.Keys(); len(k) != 3 || k[0] != "c0" {
		t.Fatalf("keys %v", k)
	}
	var zero Memory
	if err := zero.Save(context.Background(), "z", []byte{1}); err != nil {
		t.Fatal(err)
	}
}

func openDir(t *testing.T, opts DirOptions) *Dir {
	t.Helper()
	d, err := OpenDir(filepath.Join(t.TempDir(), "state"), opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDir(t *testing.T) {
	contract(t, openDir(t, DirOptions{}))
	contract(t, openDir(t, DirOptions{NoSync: true}))
}

// TestDirRecovery: a corrupt or missing current version falls back on the
// previous one, which a corrupt current version never replaces.
func TestDirRecovery(t *testing.T) {
	ctx := context.Background()
	var recovered []string
	d := openDir(t, DirOptions{OnRecover: func(key string, cause error) { recovered = append(recovered, key+": "+cause.Error()) }})
	cur := filepath.Join(d.Path(), "dev.v")
	if err := d.Save(ctx, "dev", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(ctx, "dev", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	// A flipped bit in the current version: the previous one.
	b, _ := os.ReadFile(cur)
	b[len(b)-1] ^= 1
	if err := os.WriteFile(cur, b, 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := d.Load(ctx, "dev")
	if err != nil || string(v) != "v1" || len(recovered) != 1 {
		t.Fatalf("Load %q %v, recovered %v", v, err, recovered)
	}
	// Saving over the corrupt version keeps v1 as the previous one.
	if err := d.Save(ctx, "dev", []byte("v3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cur); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Load(ctx, "dev"); err != nil || string(v) != "v1" {
		t.Fatalf("after a crash between the renames: %q %v", v, err)
	}
	// Both corrupt: ErrCorrupt.
	if err := os.WriteFile(cur, []byte("garbage that is long enough"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur+".prev", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load(ctx, "dev"); !errors.Is(err, ErrCorrupt) || !errors.Is(err, profinet.ErrMalformed) {
		t.Fatalf("both corrupt: %v", err)
	}
	// Only a corrupt previous version: ErrCorrupt as well.
	if err := os.Remove(cur); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load(ctx, "dev"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt previous: %v", err)
	}
	// Delete removes both.
	if err := d.Delete(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load(ctx, "dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete: %v", err)
	}
}

// TestDirOpen: OpenDir removes the temporary files of an interrupted Save
// and keeps the values.
func TestDirOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s")
	d, err := OpenDir(path, DirOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Save(ctx, "a", []byte("kept")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "a.tmp123"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	d2, err := OpenDir(path, DirOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := d2.Load(ctx, "a"); err != nil || string(v) != "kept" {
		t.Fatalf("%q %v", v, err)
	}
	if left, _ := filepath.Glob(filepath.Join(path, "*.tmp*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	// A path that is a file cannot be a Dir.
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDir(f, DirOptions{}); err == nil {
		t.Fatal("OpenDir on a file")
	}
	if _, err := OpenDir(filepath.Join(f, "sub"), DirOptions{}); err == nil {
		t.Fatal("OpenDir below a file")
	}
}

func TestDirLimits(t *testing.T) {
	ctx := context.Background()
	d := openDir(t, DirOptions{MaxValueSize: 8})
	if err := d.Save(ctx, "k", make([]byte, 9)); !errors.Is(err, profinet.ErrResourceExhausted) {
		t.Fatalf("oversized Save: %v", err)
	}
	if err := d.Save(ctx, "k", make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	// A file that claims more than the limit is corrupt.
	big := encodeFile(make([]byte, 9))
	if err := os.WriteFile(filepath.Join(d.Path(), "k.v"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(d.Path(), "k.v.prev")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := d.Load(ctx, "k"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("oversized file: %v", err)
	}
}

// TestDirFailures: a directory that cannot be written fails Save and
// Delete without losing the value.
func TestDirFailures(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions are not enforced")
	}
	ctx := context.Background()
	d := openDir(t, DirOptions{})
	if err := d.Save(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d.Path(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d.Path(), 0o700) })
	if err := d.Save(ctx, "k", []byte("w")); err == nil {
		t.Fatal("Save in a read-only directory")
	}
	if err := d.Delete(ctx, "k"); err == nil {
		t.Fatal("Delete in a read-only directory")
	}
	if v, err := d.Load(ctx, "k"); err != nil || string(v) != "v" {
		t.Fatalf("%q %v", v, err)
	}
	// An unreadable file is an error, not a missing value.
	_ = os.Chmod(d.Path(), 0o700)
	if err := os.Chmod(filepath.Join(d.Path(), "k.v"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(d.Path(), "k.v.prev")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := d.Load(ctx, "k"); err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("unreadable file: %v", err)
	}
}

// TestDirInjected: every failing step of Save leaves the old value and no
// temporary file.
func TestDirInjected(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("injected")
	origRename, origSync, origWrite := rename, syncFile, write
	t.Cleanup(func() { rename, syncFile, write = origRename, origSync, origWrite })
	for name, inject := range map[string]func(){
		"write":  func() { write = func(*os.File, []byte) (int, error) { return 0, boom } },
		"sync":   func() { syncFile = func(*os.File) error { return boom } },
		"rename": func() { rename = func(string, string) error { return boom } },
		"rename to cur": func() {
			n := 0
			rename = func(a, b string) error {
				n++
				if n == 2 {
					return boom
				}
				return os.Rename(a, b)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			rename, syncFile, write = origRename, origSync, origWrite
			d := openDir(t, DirOptions{})
			if err := d.Save(ctx, "k", []byte("old")); err != nil {
				t.Fatal(err)
			}
			inject()
			if err := d.Save(ctx, "k", []byte("new")); !errors.Is(err, boom) {
				t.Fatalf("Save: %v", err)
			}
			rename, syncFile, write = origRename, origSync, origWrite
			if v, err := d.Load(ctx, "k"); err != nil || string(v) != "old" {
				t.Fatalf("after a failed Save: %q %v", v, err)
			}
			if left, _ := filepath.Glob(filepath.Join(d.Path(), "*.tmp*")); len(left) != 0 {
				t.Fatalf("temporary files left: %v", left)
			}
		})
	}
	// A failing directory sync fails Delete.
	d := openDir(t, DirOptions{})
	syncFile = func(*os.File) error { return boom }
	if err := d.Delete(ctx, "k"); !errors.Is(err, boom) && runtime.GOOS != "windows" {
		t.Fatalf("Delete: %v", err)
	}
}

// TestFileFormat: the golden encoding and every way a file is rejected.
func TestFileFormat(t *testing.T) {
	got := encodeFile([]byte("abc"))
	want := []byte{'O', 'T', 'F', 'S', 1, 0, 0, 0, 0, 0, 0, 3, 0x36, 0x4b, 0x3f, 0xb7, 'a', 'b', 'c'}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeFile = % x, want % x", got, want)
	}
	decode := func(b []byte) error {
		if len(b) < headerLen {
			return ErrCorrupt
		}
		_, err := decodeFile([headerLen]byte(b[:headerLen]), bytes.NewReader(b[headerLen:]), 100, "f")
		return err
	}
	if err := decode(got); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(b []byte) []byte{
		"magic":    func(b []byte) []byte { b[0] = 'X'; return b },
		"version":  func(b []byte) []byte { b[4] = 2; return b },
		"reserved": func(b []byte) []byte { b[6] = 1; return b },
		"length":   func(b []byte) []byte { b[11] = 4; return b },
		"too long": func(b []byte) []byte { b[8] = 1; return b },
		"checksum": func(b []byte) []byte { b[15] ^= 1; return b },
		"value":    func(b []byte) []byte { b[16] ^= 1; return b },
		"trailing": func(b []byte) []byte { return append(b, 0) },
	} {
		b := mutate(append([]byte(nil), got...))
		if err := decode(b); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func FuzzDecodeFile(f *testing.F) {
	f.Add(encodeFile([]byte("abc")))
	f.Add(encodeFile(nil))
	f.Add([]byte("OTFS"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) < headerLen {
			return
		}
		v, err := decodeFile([headerLen]byte(b[:headerLen]), bytes.NewReader(b[headerLen:]), 1<<16, "f")
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("error %v is not ErrCorrupt", err)
			}
			return
		}
		if !bytes.Equal(encodeFile(v), b) {
			t.Fatal("accepted a file that does not re-encode to itself")
		}
	})
}
