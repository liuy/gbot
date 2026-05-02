package task

import (
	"errors"
	"fmt"
	"strings"
)

// Compile-time interface check.
var _ Registry = (*MultiRegistry)(nil)

// MultiRegistry composes multiple Registry instances with prefix-based routing.
// nil entries are filtered at construction time.
//
// Registries implementing the Prefixer interface are automatically routed:
// IDs matching the declared prefix go directly to that registry.
// Unknown prefixes fall back to linear scan.
type MultiRegistry struct {
	registries []Registry
	prefixMap  []prefixEntry // prefix → registry for direct routing
}

type prefixEntry struct {
	prefix string
	reg    Registry
}

// NewMultiRegistry creates a composite registry from multiple sources.
// nil entries are silently filtered out.
// Registries implementing Prefixer are auto-registered for prefix routing.
func NewMultiRegistry(registries ...Registry) *MultiRegistry {
	filtered := make([]Registry, 0, len(registries))
	var prefixes []prefixEntry
	for _, r := range registries {
		if r == nil {
			continue
		}
		filtered = append(filtered, r)
		if p, ok := r.(Prefixer); ok {
			prefixes = append(prefixes, prefixEntry{prefix: p.Prefix(), reg: r})
		}
	}
	return &MultiRegistry{registries: filtered, prefixMap: prefixes}
}

// routeByPrefix returns the registry for the given ID if a prefix matches.
func (m *MultiRegistry) routeByPrefix(id string) Registry {
	for _, pe := range m.prefixMap {
		if strings.HasPrefix(id, pe.prefix) {
			return pe.reg
		}
	}
	return nil
}

// Get returns task info by ID.
// Prefix-matched IDs go directly to the owning registry.
// Others fall back to linear scan.
func (m *MultiRegistry) Get(id string) (*TaskInfo, bool) {
	if reg := m.routeByPrefix(id); reg != nil {
		return reg.Get(id)
	}
	for _, reg := range m.registries {
		if info, ok := reg.Get(id); ok {
			return info, true
		}
	}
	return nil, false
}

// Kill terminates a running task by ID.
// Prefix-matched IDs go directly to the owning registry.
// Others fall back to linear scan with ErrNotFound skip logic.
func (m *MultiRegistry) Kill(id string) error {
	if reg := m.routeByPrefix(id); reg != nil {
		if err := reg.Kill(id); err != nil {
			return fmt.Errorf("kill %q: %w", id, err)
		}
		return nil
	}
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

// Wait blocks until the task finishes.
// Prefix-matched IDs go directly to the owning registry.
// Others fall back to Get-first-then-Wait to avoid blocking on wrong registry.
func (m *MultiRegistry) Wait(id string) (int, error) {
	if reg := m.routeByPrefix(id); reg != nil {
		return reg.Wait(id)
	}
	for _, reg := range m.registries {
		if _, found := reg.Get(id); found {
			return reg.Wait(id)
		}
	}
	return -1, fmt.Errorf("wait %q: %w", id, ErrNotFound)
}
