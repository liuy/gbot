// Test that prefix-based routing avoids unnecessary cross-registry lookups.
// TDD: write failing tests first, then implement.

package task

import (
	"fmt"
	"strings"
	"testing"
)

// trackCallRegistry wraps a stubRegistry and tracks which methods were called.
type trackCallRegistry struct {
	*stubRegistry
	getCalls  []string
	killCalls []string
}

func (t *trackCallRegistry) Get(id string) (*TaskInfo, bool) {
	t.getCalls = append(t.getCalls, id)
	return t.stubRegistry.Get(id)
}

func (t *trackCallRegistry) Kill(id string) error {
	t.killCalls = append(t.killCalls, id)
	return t.stubRegistry.Kill(id)
}

func TestMulti_PrefixRouting_Get(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "bg-1", Type: "local_bash", Status: "running"})}
	fork := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "fork-1", Type: "local_agent", Status: "running"})}

	m := NewMultiRegistry(bash, fork)
	m.RegisterPrefix("bg-", bash)
	m.RegisterPrefix("fork-", fork)

	// Get fork-1 should ONLY query fork registry
	info, ok := m.Get("fork-1")
	if !ok {
		t.Fatal("Get fork-1 returned false")
	}
	if info.Type != "local_agent" {
		t.Errorf("Type = %q, want local_agent", info.Type)
	}
	if len(bash.getCalls) != 0 {
		t.Errorf("bash.Get should not be called for fork-1, got calls: %v", bash.getCalls)
	}
	if len(fork.getCalls) != 1 || fork.getCalls[0] != "fork-1" {
		t.Errorf("fork.Get should be called once with fork-1, got: %v", fork.getCalls)
	}
}

func TestMulti_PrefixRouting_Kill(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "bg-1", Status: "running"})}
	fork := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "fork-1", Status: "running"})}

	m := NewMultiRegistry(bash, fork)
	m.RegisterPrefix("bg-", bash)
	m.RegisterPrefix("fork-", fork)

	// Kill bg-1 should ONLY query bash registry
	if err := m.Kill("bg-1"); err != nil {
		t.Errorf("Kill bg-1 error: %v", err)
	}
	if len(fork.killCalls) != 0 {
		t.Errorf("fork.Kill should not be called for bg-1, got calls: %v", fork.killCalls)
	}
	if len(bash.killCalls) != 1 || bash.killCalls[0] != "bg-1" {
		t.Errorf("bash.Kill should be called once with bg-1, got: %v", bash.killCalls)
	}
}

func TestMulti_PrefixRouting_Wait(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "bg-1", Status: "completed", ExitCode: 0})}
	fork := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "fork-1", Status: "completed", ExitCode: 1})}

	m := NewMultiRegistry(bash, fork)
	m.RegisterPrefix("bg-", bash)
	m.RegisterPrefix("fork-", fork)

	// Wait fork-1 should ONLY query fork registry
	code, err := m.Wait("fork-1")
	if err != nil {
		t.Fatalf("Wait fork-1 error: %v", err)
	}
	if code != 1 {
		t.Errorf("ExitCode = %d, want 1", code)
	}
	if len(bash.getCalls) != 0 {
		t.Errorf("bash.Get should not be called for fork-1, got calls: %v", bash.getCalls)
	}
}

func TestMulti_PrefixRouting_Fallback(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&TaskInfo{ID: "custom-1", Type: "other", Status: "running"})

	m := NewMultiRegistry(r1)
	m.RegisterPrefix("bg-", r1) // only bg- prefix registered

	// custom-1 has no registered prefix → fallback to linear scan
	info, ok := m.Get("custom-1")
	if !ok {
		t.Fatal("Get custom-1 should succeed via fallback")
	}
	if info.Type != "other" {
		t.Errorf("Type = %q, want other", info.Type)
	}
}

func TestMulti_PrefixRouting_KillFallback(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "bg-1", Status: "running"})}

	m := NewMultiRegistry(bash)
	m.RegisterPrefix("fork-", bash) // only fork prefix registered, not bg-

	// Kill bg-1 should fall back to linear scan
	if err := m.Kill("bg-1"); err != nil {
		t.Errorf("Kill bg-1 via fallback error: %v", err)
	}
}

func TestMulti_PrefixRouting_KillNotFound(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "bg-1", Status: "running"})}
	fork := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "fork-1", Status: "running"})}

	m := NewMultiRegistry(bash, fork)
	m.RegisterPrefix("bg-", bash)
	m.RegisterPrefix("fork-", fork)

	err := m.Kill("fork-99")
	if err == nil {
		t.Error("Kill should return error for nonexistent fork-99")
	}
	// Should only query fork registry, not bash
	if len(bash.killCalls) != 0 {
		t.Errorf("bash.Kill should not be called for fork-99, got: %v", bash.killCalls)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want not found", err.Error())
	}
}

func TestMulti_PrefixRouting_GetNotFound(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "bg-1", Status: "running"})}
	fork := &trackCallRegistry{stubRegistry: newStubRegistry(&TaskInfo{ID: "fork-1", Status: "running"})}

	m := NewMultiRegistry(bash, fork)
	m.RegisterPrefix("bg-", bash)
	m.RegisterPrefix("fork-", fork)

	_, ok := m.Get("fork-99")
	if ok {
		t.Error("Get fork-99 should return false")
	}
	if len(bash.getCalls) != 0 {
		t.Errorf("bash.Get should not be called for fork-99, got: %v", bash.getCalls)
	}
}

func TestMulti_PrefixRouting_NoPrefix(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&TaskInfo{ID: "bg-1", Status: "running"})
	r2 := newStubRegistry(&TaskInfo{ID: "fork-1", Status: "running"})

	// No RegisterPrefix calls → should behave like old linear scan
	m := NewMultiRegistry(r1, r2)

	info, ok := m.Get("fork-1")
	if !ok {
		t.Fatal("Get fork-1 should succeed via linear scan")
	}
	if info.ID != "fork-1" {
		t.Errorf("ID = %q, want fork-1", info.ID)
	}
}

func TestMulti_RegisterPrefixReturnsSelf(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&TaskInfo{ID: "bg-1", Status: "running"})

	// RegisterPrefix should return *MultiRegistry for chaining
	m := NewMultiRegistry(r1)
	result := m.RegisterPrefix("bg-", r1)
	if result != m {
		t.Error("RegisterPrefix should return *MultiRegistry for chaining")
	}
}

// Verify the stubRegistry Kill returns ErrNotFound properly.
func TestStubRegistry_KillErrNotFound(t *testing.T) {
	r := newStubRegistry()
	err := r.Kill("nonexistent")
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want not found", err.Error())
	}
}

// Verify trackCallRegistry with fmt.Stringer for debug
var _ fmt.Stringer = (*trackCallRegistry)(nil)

func (t *trackCallRegistry) String() string {
	return fmt.Sprintf("trackCallRegistry{getCalls: %v, killCalls: %v}", t.getCalls, t.killCalls)
}
