// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/otfabric/go-profinet"
)

// The file of a value: a 16-octet header, then the value.
//
//	magic "OTFS" | version 1 | 3 reserved octets (0) | length (uint32, big endian) | CRC-32C of the value
const (
	headerLen   = 16
	fileVersion = 1
)

var (
	magic  = [4]byte{'O', 'T', 'F', 'S'}
	crcTab = crc32.MakeTable(crc32.Castagnoli)
)

// The file operations of Save, replaced by the tests to inject failures.
var (
	rename   = os.Rename
	syncFile = (*os.File).Sync
	write    = (*os.File).Write
)

// DefaultMaxValueSize bounds a value of a Dir.
const DefaultMaxValueSize = 1 << 20

// DirOptions configure a [Dir].
type DirOptions struct {
	// MaxValueSize bounds a value; zero means DefaultMaxValueSize.
	MaxValueSize int
	// NoSync skips the fsync calls (faster, for tests): an update is then
	// atomic against a crash of the program but not of the machine.
	NoSync bool
	// OnRecover is called when Load found a value corrupt or missing and
	// returned the previous version instead; cause says why. It must not
	// block.
	OnRecover func(key string, cause error)
}

// Dir is a Store in a directory: the value of key is the file key.v, its
// previous version key.v.prev. Save writes a temporary file, syncs it,
// renames the current file to the previous one and the temporary file to
// the current one, then syncs the directory; Load checks a file's header
// and CRC-32C and falls back on the previous version when the current one
// is corrupt or missing (a crash between the two renames). A Dir assumes
// it is the only writer of its directory.
type Dir struct {
	path string
	opts DirOptions
	mu   sync.Mutex
}

// OpenDir opens (and creates, with mode 0o700) the directory of a Dir. It
// removes the temporary files an interrupted Save left.
func OpenDir(path string, opts DirOptions) (*Dir, error) {
	if opts.MaxValueSize <= 0 {
		opts.MaxValueSize = DefaultMaxValueSize
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	tmps, err := filepath.Glob(filepath.Join(path, "*.tmp*"))
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	for _, t := range tmps {
		_ = os.Remove(t)
	}
	return &Dir{path: path, opts: opts}, nil
}

// Path returns the directory.
func (d *Dir) Path() string { return d.path }

func (d *Dir) file(key string) string { return filepath.Join(d.path, key+".v") }

// Load implements Store.
func (d *Dir) Load(ctx context.Context, key string) ([]byte, error) {
	if err := check(ctx, key); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.read(d.file(key))
	if err == nil {
		return v, nil
	}
	prev, perr := d.read(d.file(key) + ".prev")
	if perr != nil {
		if isNotFound(err) && isNotFound(perr) {
			return nil, ErrNotFound
		}
		if !isNotFound(err) {
			return nil, err
		}
		return nil, perr
	}
	if d.opts.OnRecover != nil {
		d.opts.OnRecover(key, err)
	}
	return prev, nil
}

// read reads and checks one file.
func (d *Dir) read(name string) ([]byte, error) {
	f, err := os.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer func() { _ = f.Close() }()
	var h [headerLen]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return nil, fmt.Errorf("%w: %s: short header", ErrCorrupt, filepath.Base(name))
	}
	return decodeFile(h, f, d.opts.MaxValueSize, filepath.Base(name))
}

// decodeFile checks a header and reads the value that follows it from r.
func decodeFile(h [headerLen]byte, r io.Reader, maxSize int, name string) ([]byte, error) {
	if [4]byte(h[:4]) != magic || h[4] != fileVersion || h[5] != 0 || h[6] != 0 || h[7] != 0 {
		return nil, fmt.Errorf("%w: %s: bad header", ErrCorrupt, name)
	}
	n := binary.BigEndian.Uint32(h[8:12])
	if uint64(n) > uint64(maxSize) {
		return nil, fmt.Errorf("%w: %s: length %d exceeds %d", ErrCorrupt, name, n, maxSize)
	}
	v := make([]byte, n)
	if _, err := io.ReadFull(r, v); err != nil {
		return nil, fmt.Errorf("%w: %s: short value", ErrCorrupt, name)
	}
	if extra, _ := r.Read(make([]byte, 1)); extra != 0 {
		return nil, fmt.Errorf("%w: %s: trailing data", ErrCorrupt, name)
	}
	if crc32.Checksum(v, crcTab) != binary.BigEndian.Uint32(h[12:16]) {
		return nil, fmt.Errorf("%w: %s: checksum", ErrCorrupt, name)
	}
	return v, nil
}

// encodeFile returns the file of a value.
func encodeFile(v []byte) []byte {
	b := make([]byte, headerLen, headerLen+len(v))
	copy(b, magic[:])
	b[4] = fileVersion
	binary.BigEndian.PutUint32(b[8:12], uint32(len(v)))
	binary.BigEndian.PutUint32(b[12:16], crc32.Checksum(v, crcTab))
	return append(b, v...)
}

// Save implements Store.
func (d *Dir) Save(ctx context.Context, key string, value []byte) error {
	if err := check(ctx, key); err != nil {
		return err
	}
	if len(value) > d.opts.MaxValueSize {
		return fmt.Errorf("store: %w: value of %d octets exceeds %d", profinet.ErrResourceExhausted, len(value), d.opts.MaxValueSize)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.file(key)
	tmp, err := os.CreateTemp(d.path, key+".tmp*")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	clean := func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }
	if _, err := write(tmp, encodeFile(value)); err != nil {
		clean()
		return fmt.Errorf("store: %w", err)
	}
	if !d.opts.NoSync {
		if err := syncFile(tmp); err != nil {
			clean()
			return fmt.Errorf("store: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("store: %w", err)
	}
	// The current version becomes the previous one, if it is intact: a
	// corrupt current file must not replace a good previous one.
	if _, err := d.read(cur); err == nil {
		if err := rename(cur, cur+".prev"); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("store: %w", err)
		}
	}
	if err := rename(tmp.Name(), cur); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("store: %w", err)
	}
	return d.syncDir()
}

// Delete implements Store: both versions go.
func (d *Dir) Delete(ctx context.Context, key string) error {
	if err := check(ctx, key); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range []string{d.file(key), d.file(key) + ".prev"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("store: %w", err)
		}
	}
	return d.syncDir()
}

// syncDir makes the renames durable. Windows cannot sync a directory; its
// renames are durable with the file system's journal.
func (d *Dir) syncDir() error {
	if d.opts.NoSync || runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(d.path)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := syncFile(f); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}
