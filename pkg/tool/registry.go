package tool

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
)

// Registry manages tool discovery and lookup.
// Source: Tool.ts tool map pattern + REPL.tsx tool registration.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry. Panics on duplicate name.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := t.Name()
	if _, exists := r.tools[name]; exists {
		panic(fmt.Sprintf("tool already registered: %s", name))
	}
	r.tools[name] = t

	// Register aliases
	for _, alias := range t.Aliases() {
		if alias == "" {
			continue
		}
		if _, exists := r.tools[alias]; exists {
			panic(fmt.Sprintf("tool alias already registered: %s -> %s", alias, name))
		}
		r.tools[alias] = t
	}
}

// MustRegister registers a tool, panicking on error.
func (r *Registry) MustRegister(t Tool) {
	r.Register(t)
}

// Lookup finds a tool by name or alias.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools, deduplicated by primary name.
// Tools appear once even if they have aliases.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var result []Tool
	for _, t := range r.tools {
		name := t.Name()
		if seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, t)
	}

	// Deterministic order
	slices.SortFunc(result, func(a, b Tool) int { return cmp.Compare(a.Name(), b.Name()) })

	return result
}

// EnabledTools returns only enabled tools, deduplicated.
func (r *Registry) EnabledTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var result []Tool
	for _, t := range r.tools {
		name := t.Name()
		if seen[name] || !t.IsEnabled() {
			continue
		}
		seen[name] = true
		result = append(result, t)
	}

	slices.SortFunc(result, func(a, b Tool) int { return cmp.Compare(a.Name(), b.Name()) })

	return result
}

// ToolMap returns a name->Tool map suitable for the engine.
// Only includes enabled tools, keyed by primary name (no aliases).
func (r *Registry) ToolMap() map[string]Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]Tool)
	for _, t := range r.tools {
		if !t.IsEnabled() {
			continue
		}
		result[t.Name()] = t
	}
	return result
}

// ToolMapFn returns a closure that calls ToolMap().
// The closure is safe to store and call later — it captures the Registry pointer.
func (r *Registry) ToolMapFn() func() map[string]Tool {
	return r.ToolMap
}

// Names returns all registered tool names (primary names only).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var names []string
	for _, t := range r.tools {
		name := t.Name()
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}

	slices.Sort(names)
	return names
}

// Size returns the number of unique tools (by primary name).
func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	for _, t := range r.tools {
		seen[t.Name()] = true
	}
	return len(seen)
}
