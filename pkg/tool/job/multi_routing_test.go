package job

import (
	"fmt"
	"strings"
	"testing"
)

// prefixStub wraps stubRegistry and declares a prefix.
type prefixStub struct {
	*stubRegistry
	prefix string
}

func (p *prefixStub) Prefix() string { return p.prefix }

// trackCallRegistry wraps a registry and tracks method calls.
type trackCallRegistry struct {
	Registry
	getCalls  []string
	killCalls []string
}

func (t *trackCallRegistry) Get(id string) (*JobInfo, bool) {
	t.getCalls = append(t.getCalls, id)
	return t.Registry.Get(id)
}

func (t *trackCallRegistry) Kill(id string) error {
	t.killCalls = append(t.killCalls, id)
	return t.Registry.Kill(id)
}

// Prefix delegates to inner registry if it implements Prefixer.
func (t *trackCallRegistry) Prefix() string {
	if p, ok := t.Registry.(Prefixer); ok {
		return p.Prefix()
	}
	return ""
}

func (t *trackCallRegistry) String() string {
	return fmt.Sprintf("trackCallRegistry{getCalls: %v, killCalls: %v}", t.getCalls, t.killCalls)
}

// ---------------------------------------------------------------------------
// Prefix auto-discovery tests
// ---------------------------------------------------------------------------

func TestMulti_PrefixAutoDiscovery(t *testing.T) {
	t.Parallel()
	bash := &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "bg-1", Type: "local_bash", Status: "running"}), prefix: "bg-"}
	fork := &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "fork-1", Type: "local_agent", Status: "running"}), prefix: "fork-"}

	m := NewMultiRegistry(bash, fork)
	// No manual RegisterPrefix needed — auto-discovered from Prefixer interface

	info, ok := m.Get("fork-1")
	if !ok {
		t.Fatal("Get fork-1 returned false")
	}
	if info.Type != "local_agent" {
		t.Errorf("Type = %q, want local_agent", info.Type)
	}
}

func TestMulti_PrefixRouting_Get(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "bg-1", Type: "local_bash", Status: "running"}), prefix: "bg-"}}
	fork := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "fork-1", Type: "local_agent", Status: "running"}), prefix: "fork-"}}

	m := NewMultiRegistry(bash, fork)

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
	bash := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"}), prefix: "bg-"}}
	fork := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "fork-1", Status: "running"}), prefix: "fork-"}}

	m := NewMultiRegistry(bash, fork)

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
	bash := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "bg-1", Status: "completed", ExitCode: 0}), prefix: "bg-"}}
	fork := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "fork-1", Status: "completed", ExitCode: 1}), prefix: "fork-"}}

	m := NewMultiRegistry(bash, fork)

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
	r1 := newStubRegistry(&JobInfo{ID: "custom-1", Type: "other", Status: "running"})

	m := NewMultiRegistry(r1)
	// No Prefixer — all lookups via linear scan

	info, ok := m.Get("custom-1")
	if !ok {
		t.Fatal("Get custom-1 should succeed via fallback")
	}
	if info.Type != "other" {
		t.Errorf("Type = %q, want other", info.Type)
	}
}

func TestMulti_PrefixRouting_KillNotFound(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"}), prefix: "bg-"}}
	fork := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "fork-1", Status: "running"}), prefix: "fork-"}}

	m := NewMultiRegistry(bash, fork)

	err := m.Kill("fork-99")
	if err == nil {
		t.Error("Kill should return error for nonexistent fork-99")
	}
	if len(bash.killCalls) != 0 {
		t.Errorf("bash.Kill should not be called for fork-99, got: %v", bash.killCalls)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want not found", err.Error())
	}
}

func TestMulti_PrefixRouting_GetNotFound(t *testing.T) {
	t.Parallel()
	bash := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"}), prefix: "bg-"}}
	fork := &trackCallRegistry{Registry: &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "fork-1", Status: "running"}), prefix: "fork-"}}

	m := NewMultiRegistry(bash, fork)

	_, ok := m.Get("fork-99")
	if ok {
		t.Error("Get fork-99 should return false")
	}
	if len(bash.getCalls) != 0 {
		t.Errorf("bash.Get should not be called for fork-99, got: %v", bash.getCalls)
	}
}

func TestMulti_NilRegistryFiltered(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	m := NewMultiRegistry(r1, nil)

	info, ok := m.Get("bg-1")
	if !ok {
		t.Fatal("Get bg-1 should succeed")
	}
	if info.ID != "bg-1" {
		t.Errorf("ID = %q, want bg-1", info.ID)
	}
}

func TestMulti_EmptyRegistries(t *testing.T) {
	t.Parallel()
	m := NewMultiRegistry()

	_, ok := m.Get("any")
	if ok {
		t.Error("Get should return false with no registries")
	}

	err := m.Kill("any")
	if err == nil {
		t.Error("Kill should return error with no registries")
	}

	_, err = m.Wait("any")
	if err == nil {
		t.Error("Wait should return error with no registries")
	}
}
