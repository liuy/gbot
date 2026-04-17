package task

import (
	"errors"
	"fmt"
)

// Compile-time interface check.
var _ Registry = (*MultiRegistry)(nil)

// MultiRegistry composes multiple Registry instances, dispatching by ID order.
// nil entries are filtered at construction time.
type MultiRegistry struct {
	registries []Registry
}

// NewMultiRegistry creates a composite registry from multiple sources.
// nil entries are silently filtered out.
func NewMultiRegistry(registries ...Registry) *MultiRegistry {
	filtered := make([]Registry, 0, len(registries))
	for _, r := range registries {
		if r != nil {
			filtered = append(filtered, r)
		}
	}
	return &MultiRegistry{registries: filtered}
}

// Get returns task info by ID, searching registries in order.
func (m *MultiRegistry) Get(id string) (*TaskInfo, bool) {
	for _, reg := range m.registries {
		if info, ok := reg.Get(id); ok {
			return info, true
		}
	}
	return nil, false
}

// Kill terminates a running task by ID, trying registries in order.
// Non-ErrNotFound errors are returned immediately.
func (m *MultiRegistry) Kill(id string) error {
	for _, reg := range m.registries {
		if err := reg.Kill(id); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return fmt.Errorf("kill %q: %w", id, ErrNotFound)
}

// List returns tasks from all registries concatenated.
func (m *MultiRegistry) List() []*TaskInfo {
	var result []*TaskInfo
	for _, reg := range m.registries {
		result = append(result, reg.List()...)
	}
	return result
}

// Wait blocks until the task finishes. Uses Get-first-then-Wait to avoid
// blocking on a registry that doesn't own the task.
// Task ID namespaces are disjoint (bash: "bg-*", fork: "fork-*").
func (m *MultiRegistry) Wait(id string) (int, error) {
	for _, reg := range m.registries {
		if _, found := reg.Get(id); found {
			return reg.Wait(id)
		}
	}
	return -1, fmt.Errorf("wait %q: %w", id, ErrNotFound)
}
