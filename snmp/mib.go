// SPDX-License-Identifier: MIT

package snmp

import (
	"fmt"
	"slices"
	"sync"

	"github.com/otfabric/go-profinet"
)

// MIB is the tree of objects an [Agent] serves: subtrees, each with a
// provider that returns the instances under its prefix when a request
// reads it. Subtrees do not overlap; within the MIB they are kept in
// lexicographic order, so that GetNext and GetBulk walk the instances in
// the order RFC 3416 section 4.2.2 defines. A MIB is safe for concurrent
// use; objects can be added while an agent serves it.
type MIB struct {
	mu sync.RWMutex
	// regs is sorted by prefix and replaced, not modified, by Add.
	regs []reg
}

type reg struct {
	prefix OID
	fn     func() []VarBind
}

// NewMIB returns an empty MIB.
func NewMIB() *MIB { return &MIB{} }

// AddScalar adds a scalar object: its single instance object.0 has the
// value fn returns. fn may return the zero Value when the object has no
// value at the moment.
func (m *MIB) AddScalar(object OID, fn func() Value) error {
	inst := object.Append(0)
	return m.AddSubtree(object, func() []VarBind {
		v := fn()
		if v.Type == 0 {
			return nil
		}
		return []VarBind{{Name: inst, Value: v}}
	})
}

// AddSubtree adds the instances fn returns, for example the rows of a
// table, under prefix. fn is called once for each request that reads the
// subtree; it returns the instances in any order, each with a name below
// prefix (other names, duplicates and values that cannot be encoded are
// ignored).
func (m *MIB) AddSubtree(prefix OID, fn func() []VarBind) error {
	if err := prefix.validate(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("snmp: %w: nil provider for %s", profinet.ErrInvalidConfig, prefix)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.regs {
		if r.prefix.HasPrefix(prefix) || prefix.HasPrefix(r.prefix) {
			return fmt.Errorf("snmp: %w: %s overlaps %s", profinet.ErrInvalidConfig, prefix, r.prefix)
		}
	}
	regs := append(slices.Clip(m.regs), reg{prefix: append(OID(nil), prefix...), fn: fn})
	slices.SortFunc(regs, func(a, b reg) int { return a.prefix.Compare(b.prefix) })
	m.regs = regs
	return nil
}

// view is the MIB as one request sees it: every subtree is read at most
// once, so that the variable bindings of a response are consistent.
type view struct {
	regs  []reg
	cache map[int][]VarBind
	// v1 hides Counter64 instances (RFC 3584 section 4.2.2.1).
	v1 bool
}

func (m *MIB) view(v1 bool) *view {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return &view{regs: m.regs, v1: v1}
}

// rows returns the instances of subtree i, sorted, without duplicates or
// names outside the subtree.
func (v *view) rows(i int) []VarBind {
	if rows, ok := v.cache[i]; ok {
		return rows
	}
	r := v.regs[i]
	var rows []VarBind
	for _, vb := range r.fn() {
		if len(vb.Name) <= len(r.prefix) || !vb.Name.HasPrefix(r.prefix) || len(vb.Name) > MaxOIDLen || vb.Value.valid() != nil {
			continue
		}
		if vb.Value.Type.IsException() || (v.v1 && vb.Value.Type == TypeCounter64) {
			continue
		}
		rows = append(rows, vb)
	}
	slices.SortStableFunc(rows, func(a, b VarBind) int { return a.Name.Compare(b.Name) })
	rows = slices.CompactFunc(rows, func(a, b VarBind) bool { return a.Name.Equal(b.Name) })
	if v.cache == nil {
		v.cache = map[int][]VarBind{}
	}
	v.cache[i] = rows
	return rows
}

// get returns the value of the instance name: a NoSuchInstance exception
// when name is below a subtree that has no such instance, NoSuchObject
// when it is below none (RFC 3416 section 4.2.1).
func (v *view) get(name OID) Value {
	for i, r := range v.regs {
		if !name.HasPrefix(r.prefix) {
			continue
		}
		rows := v.rows(i)
		j, found := slices.BinarySearchFunc(rows, name, func(vb VarBind, n OID) int { return vb.Name.Compare(n) })
		if found {
			return rows[j].Value
		}
		return Value{Type: TypeNoSuchInstance}
	}
	return Value{Type: TypeNoSuchObject}
}

// next returns the first instance after name in lexicographic order, and
// false at the end of the MIB view.
func (v *view) next(name OID) (VarBind, bool) {
	for i, r := range v.regs {
		under := name.HasPrefix(r.prefix)
		if !under && r.prefix.Compare(name) < 0 {
			// The whole subtree precedes name.
			continue
		}
		rows := v.rows(i)
		j := 0
		if under {
			j, _ = slices.BinarySearchFunc(rows, name, func(vb VarBind, n OID) int { return vb.Name.Compare(n) })
			if j < len(rows) && rows[j].Name.Equal(name) {
				j++
			}
		}
		if j < len(rows) {
			return rows[j], true
		}
	}
	return VarBind{}, false
}
