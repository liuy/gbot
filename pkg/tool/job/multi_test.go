package job

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Basic MultiRegistry tests (prefix routing tested in multi_routing_test.go)
// ---------------------------------------------------------------------------

func TestMulti_GetFirst(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Type: "local_bash", Status: "running"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Type: "local_agent", Status: "running"})
	m := NewMultiRegistry(r1, r2)

	info, ok := m.Get("bg-1")
	if !ok {
		t.Fatal("Get bg-1 returned false")
	}
	if info.Type != "local_bash" {
		t.Errorf("Type = %q, want local_bash", info.Type)
	}
}

func TestMulti_GetSecond(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Type: "local_bash", Status: "running"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Type: "local_agent", Status: "running"})
	m := NewMultiRegistry(r1, r2)

	info, ok := m.Get("fork-1")
	if !ok {
		t.Fatal("Get fork-1 returned false")
	}
	if info.Type != "local_agent" {
		t.Errorf("Type = %q, want local_agent", info.Type)
	}
}

func TestMulti_GetNotFound(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Status: "running"})
	m := NewMultiRegistry(r1, r2)

	_, ok := m.Get("nonexistent")
	if ok {
		t.Error("Get should return false for nonexistent ID")
	}
}

func TestMulti_List(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Type: "local_bash"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Type: "local_agent"})
	m := NewMultiRegistry(r1, r2)

	list := m.List()
	if len(list) != 2 {
		t.Fatalf("List returned %d, want 2", len(list))
	}
	types := map[string]bool{}
	for _, info := range list {
		types[info.Type] = true
	}
	if !types["local_bash"] || !types["local_agent"] {
		t.Errorf("List should contain both types, got: %v", types)
	}
}

func TestMulti_KillFirst(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Status: "running"})
	m := NewMultiRegistry(r1, r2)

	if err := m.Kill("bg-1"); err != nil {
		t.Errorf("Kill bg-1 error: %v", err)
	}
}

func TestMulti_KillSecond(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Status: "running"})
	m := NewMultiRegistry(r1, r2)

	if err := m.Kill("fork-1"); err != nil {
		t.Errorf("Kill fork-1 error: %v", err)
	}
}

func TestMulti_KillNotFound(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	m := NewMultiRegistry(r1)

	err := m.Kill("nonexistent")
	if err == nil {
		t.Error("Kill should return error for nonexistent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want not found", err.Error())
	}
}

func TestMulti_WaitFirst(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "completed", ExitCode: 0})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Status: "completed", ExitCode: 0})
	m := NewMultiRegistry(r1, r2)

	code, err := m.Wait("bg-1")
	if err != nil {
		t.Fatalf("Wait bg-1 error: %v", err)
	}
	if code != 0 {
		t.Errorf("ExitCode = %d, want 0", code)
	}
}

func TestMulti_WaitSecond(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Status: "completed", ExitCode: 0})
	m := NewMultiRegistry(r1, r2)

	code, err := m.Wait("fork-1")
	if err != nil {
		t.Fatalf("Wait fork-1 error: %v", err)
	}
	if code != 0 {
		t.Errorf("ExitCode = %d, want 0", code)
	}
}

func TestMulti_WaitNotFound(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	m := NewMultiRegistry(r1)

	start := time.Now()
	_, err := m.Wait("nonexistent")
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Wait should return error for nonexistent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want not found", err.Error())
	}
	// Must not block — Get-first should fail fast
	if elapsed > 500*time.Millisecond {
		t.Errorf("Wait took %v for nonexistent ID — should be fast (Get-first)", elapsed)
	}
}
