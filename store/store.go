// SPDX-License-Identifier: MIT

// Package store is the optional persistence of go-profinet: the values a
// device must keep across a restart (its permanent station name and IP
// suite, its I&M records) are saved in a [Store] under a key.
//
// Two backends come with it: [Memory], for tests and for applications that
// persist elsewhere, and [Dir], a directory of files whose updates are
// atomic and checked on load. A library user needs neither a filesystem
// nor this package: a device without a store keeps its state in memory,
// as before.
//
// A Store holds opaque values; their format is the caller's. Keys are
// short names of lowercase letters, digits, '.', '_' and '-' ([ValidKey]).
package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/otfabric/go-profinet"
)

// Errors of a Store. ErrNotFound and ErrCorrupt match
// profinet.ErrNotFound and profinet.ErrMalformed with errors.Is.
var (
	// ErrNotFound reports a key with no value.
	ErrNotFound = fmt.Errorf("store: %w", profinet.ErrNotFound)
	// ErrCorrupt reports a value that failed its integrity check and has
	// no intact previous version to fall back on.
	ErrCorrupt = fmt.Errorf("store: corrupt value: %w", profinet.ErrMalformed)
)

// MaxKeyLen bounds a key.
const MaxKeyLen = 64

// Store keeps values under keys. Its methods are safe for concurrent use.
type Store interface {
	// Load returns the value of key, ErrNotFound when it has none.
	Load(ctx context.Context, key string) ([]byte, error)
	// Save replaces the value of key atomically: after a failure or a
	// crash, Load returns the old value or the new one, never a mix.
	Save(ctx context.Context, key string, value []byte) error
	// Delete removes key; a key without a value is not an error.
	Delete(ctx context.Context, key string) error
}

// ValidKey reports whether key can name a value: 1 to MaxKeyLen
// characters of a-z, 0-9, '.', '_' and '-', not starting with '.'.
func ValidKey(key string) error {
	if key == "" || len(key) > MaxKeyLen || key[0] == '.' {
		return fmt.Errorf("store: %w: key %q", profinet.ErrInvalidConfig, key)
	}
	for _, c := range []byte(key) {
		lower, digit := c >= 'a' && c <= 'z', c >= '0' && c <= '9'
		if !lower && !digit && c != '.' && c != '_' && c != '-' {
			return fmt.Errorf("store: %w: key %q", profinet.ErrInvalidConfig, key)
		}
	}
	return nil
}

// Memory is a Store in memory: values live as long as the Memory.
type Memory struct {
	mu     sync.Mutex
	values map[string][]byte
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory { return &Memory{values: map[string][]byte{}} }

// Load implements Store.
func (m *Memory) Load(ctx context.Context, key string) ([]byte, error) {
	if err := check(ctx, key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	if !ok {
		return nil, ErrNotFound
	}
	return slices.Clone(v), nil
}

// Save implements Store.
func (m *Memory) Save(ctx context.Context, key string, value []byte) error {
	if err := check(ctx, key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string][]byte{}
	}
	m.values[key] = slices.Clone(value)
	return nil
}

// Delete implements Store.
func (m *Memory) Delete(ctx context.Context, key string) error {
	if err := check(ctx, key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
	return nil
}

// Keys returns the keys that have a value, sorted.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.values))
	for k := range m.values {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func check(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ValidKey(key)
}

// isNotFound reports a missing value.
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
