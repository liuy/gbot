package job

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Basic MultiRegistry tests (prefix routing tested in multi_routing_test.go)
// ---------------------------------------------------------------------------

func TestMulti_GetFirst(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Type: "bash", Status: "running"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Type: "agent", Status: "running"})
	m := NewMultiRegistry(r1, r2)

	info, ok := m.Get("bg-1")
	if !ok {
		t.Fatal("Get bg-1 returned false")
	}
	if info.Type != "bash" {
		t.Errorf("Type = %q, want bash", info.Type)
	}
}

func TestMulti_GetSecond(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Type: "bash", Status: "running"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Type: "agent", Status: "running"})
	m := NewMultiRegistry(r1, r2)

	info, ok := m.Get("fork-1")
	if !ok {
		t.Fatal("Get fork-1 returned false")
	}
	if info.Type != "agent" {
		t.Errorf("Type = %q, want agent", info.Type)
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
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Type: "bash"})
	r2 := newStubRegistry(&JobInfo{ID: "fork-1", Type: "agent"})
	m := NewMultiRegistry(r1, r2)

	list := m.List()
	if len(list) != 2 {
		t.Fatalf("List returned %d, want 2", len(list))
	}
	types := map[string]bool{}
	for _, info := range list {
		types[info.Type] = true
	}
	if !types["bash"] || !types["agent"] {
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
		t.Fatal("Kill should return error for nonexistent")
	}
	if err.Error() != `kill "nonexistent": job not found` {
		t.Errorf("error = %q, want %q", err.Error(), `kill "nonexistent": job not found`)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error %v must wrap ErrNotFound", err)
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

	start := time.Now() // REAL-TIME: unique ID generation
	code, err := m.Wait("nonexistent")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Wait should return error for nonexistent")
	}
	if code != -1 {
		t.Errorf("Wait exit code = %d, want -1", code)
	}
	if err.Error() != `wait "nonexistent": job not found` {
		t.Errorf("error = %q, want %q", err.Error(), `wait "nonexistent": job not found`)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error %v must wrap ErrNotFound", err)
	}
	// Must not block — Get-first should fail fast
	if elapsed > 500*time.Millisecond {
		t.Errorf("Wait took %v for nonexistent ID — should be fast (Get-first)", elapsed)
	}
}

func TestMulti_KillNonErrNotFound(t *testing.T) {
	t.Parallel()
	r1 := newErrorKillRegistry("permission denied")
	m := NewMultiRegistry(r1)

	err := m.Kill("any-id")
	if err == nil {
		t.Fatal("Kill should return error")
	}
	// A non-ErrNotFound error short-circuits the scan and is returned verbatim,
	// not wrapped and not swallowed into ErrNotFound.
	if err.Error() != "permission denied" {
		t.Errorf("error = %q, want %q", err.Error(), "permission denied")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("error %v must not be reported as ErrNotFound", err)
	}
}

func TestMulti_KillPrefixWrap(t *testing.T) {
	t.Parallel()
	prefixed := &prefixStub{stubRegistry: newStubRegistry(&JobInfo{ID: "bg-7", Status: "running"}), prefix: "bg-"}
	m := NewMultiRegistry(prefixed)

	// "bg-9" routes to the prefix registry, which has no such job: the raw
	// registry error must be wrapped with the id.
	err := m.Kill("bg-9")
	if err == nil {
		t.Fatal("Kill bg-9 should fail")
	}
	if err.Error() != `kill "bg-9": kill "bg-9": job not found` {
		t.Errorf("error = %q, want %q", err.Error(), `kill "bg-9": kill "bg-9": job not found`)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error %v must wrap ErrNotFound", err)
	}
}

func TestMulti_NilRegistriesFiltered(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Type: "bash", Status: "running"})
	m := NewMultiRegistry(nil, r1, nil)

	if got := len(m.List()); got != 1 {
		t.Errorf("List() after nil-filter = %d jobs, want 1", got)
	}
	if _, ok := m.Get("bg-1"); !ok {
		t.Error("Get bg-1 must still route to the surviving registry")
	}

	empty := NewMultiRegistry(nil, nil)
	if got := len(empty.List()); got != 0 {
		t.Errorf("List() of all-nil registry = %d jobs, want 0", got)
	}
	if _, ok := empty.Get("bg-1"); ok {
		t.Error("Get on all-nil registry must return false")
	}
	if err := empty.Kill("bg-1"); err == nil || err.Error() != `kill "bg-1": job not found` {
		t.Errorf("Kill on all-nil registry = %v, want %q", err, `kill "bg-1": job not found`)
	}
	code, err := empty.Wait("bg-1")
	if code != -1 || err == nil || err.Error() != `wait "bg-1": job not found` {
		t.Errorf("Wait on all-nil registry = (%d, %v), want (-1, %q)", code, err, `wait "bg-1": job not found`)
	}
}

func TestMulti_CleanupCompleted_WithCleanupAware(t *testing.T) {
	t.Parallel()
	r1 := newCleanupStubRegistry()
	r2 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	m := NewMultiRegistry(r1, r2)

	m.CleanupCompleted()

	if !r1.cleaned {
		t.Error("CleanupCompleted should have been called on cleanupAware registry")
	}
}

func TestMulti_CleanupCompleted_NoCleanupAware(t *testing.T) {
	t.Parallel()
	r1 := newStubRegistry(&JobInfo{ID: "bg-1", Status: "running"})
	m := NewMultiRegistry(r1)

	// stubRegistry does not implement cleanupAware, so the call must be a no-op
	// rather than a panic — and it must not drop anything from the registry.
	m.CleanupCompleted()

	if _, ok := m.Get("bg-1"); !ok {
		t.Error("bg-1 must still be reachable after a no-op cleanup")
	}
	got := m.List()
	if len(got) != 1 {
		t.Fatalf("List() = %d jobs, want 1 (nothing is cleanup-eligible)", len(got))
	}
	if got[0].ID != "bg-1" || got[0].Status != "running" {
		t.Errorf("List()[0] = %+v, want {ID:bg-1 Status:running}", got[0])
	}
	if len(r1.tasks) != 1 {
		t.Errorf("underlying registry holds %d jobs, want 1", len(r1.tasks))
	}
}

// summaryOnlyRegistry implements SummaryLister. Its List and ListSummaries
// return disjoint entries so the result IDs reveal which path MultiRegistry
// took — the fallback also clears Output, which alone cannot distinguish the
// two paths.
type summaryOnlyRegistry struct {
	stubRegistry
	summaryJobs []*JobInfo
}

func (s *summaryOnlyRegistry) ListSummaries() []*JobInfo { return s.summaryJobs }

func TestMulti_ListSummaries(t *testing.T) {
	t.Parallel()
	sumReg := &summaryOnlyRegistry{
		stubRegistry: *newStubRegistry(&JobInfo{ID: "sum-via-list", Status: "completed", Output: "full output"}),
		summaryJobs:  []*JobInfo{{ID: "sum-1", Status: "completed"}},
	}
	fallReg := newStubRegistry(
		&JobInfo{ID: "f-1", Status: "completed", Output: "blob one"},
		&JobInfo{ID: "f-2", Status: "running", Output: "blob two"},
	)
	m := NewMultiRegistry(sumReg, fallReg)

	got := m.ListSummaries()
	if len(got) != 3 {
		t.Fatalf("ListSummaries() len = %d, want 3: %+v", len(got), got)
	}
	ids := map[string]bool{}
	for _, info := range got {
		ids[info.ID] = true
		if info.Output != "" {
			t.Errorf("%s Output = %q, want empty (SummaryLister contract)", info.ID, info.Output)
		}
	}
	for _, want := range []string{"sum-1", "f-1", "f-2"} {
		if !ids[want] {
			t.Errorf("%s missing from ListSummaries(): %v", want, ids)
		}
	}
	if ids["sum-via-list"] {
		t.Error("summary-capable registry fell back to List(): sum-via-list must not appear")
	}
}
