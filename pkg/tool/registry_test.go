package tool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// mockTool implements tool.Tool for registry tests.
type mockTool struct {
	name    string
	aliases []string
	enabled bool
}

func (m *mockTool) Name() string                                { return m.name }
func (m *mockTool) Aliases() []string                           { return m.aliases }
func (m *mockTool) Description(json.RawMessage) (string, error) { return "", nil }
func (m *mockTool) InputSchema() json.RawMessage                { return nil }
func (m *mockTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *mockTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *mockTool) IsReadOnly(json.RawMessage) bool           { return false }
func (m *mockTool) IsDestructive(json.RawMessage) bool        { return false }
func (m *mockTool) IsConcurrencySafe(json.RawMessage) bool    { return false }
func (m *mockTool) IsEnabled() bool                           { return m.enabled }
func (m *mockTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (m *mockTool) Prompt() string                            { return "" }
func (m *mockTool) RenderResult(any) string                   { return "" }

func (*mockTool) MaxResultSize() int { return 50000 }

func TestNewRegistry(t *testing.T) {
	r := tool.NewRegistry()
	if r == nil {
		t.Fatal("expected non-nil registry")
	}
	if r.Size() != 0 {
		t.Errorf("expected 0 tools, got %d", r.Size())
	}
}

func TestRegistry_Register(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", enabled: true})

	tm, ok := r.Lookup("bash")
	if !ok {
		t.Fatal("expected to find bash")
	}
	if tm.Name() != "bash" {
		t.Errorf("expected bash, got %s", tm.Name())
	}
}

func TestRegistry_RegisterWithAlias(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", aliases: []string{"sh", "shell"}, enabled: true})

	if _, ok := r.Lookup("sh"); !ok {
		t.Error("expected to find alias 'sh'")
	}
	if _, ok := r.Lookup("shell"); !ok {
		t.Error("expected to find alias 'shell'")
	}
}

func TestRegistry_DuplicateName(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash"})

	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	r.Register(&mockTool{name: "bash"})
}

func TestRegistry_DuplicateAlias(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", aliases: []string{"sh"}})

	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic on duplicate alias")
		}
	}()
	r.Register(&mockTool{name: "other", aliases: []string{"sh"}})
}

func TestRegistry_LookupNotFound(t *testing.T) {
	r := tool.NewRegistry()
	_, ok := r.Lookup("nonexistent")
	if ok {
		t.Error("expected not found")
	}
}

func TestRegistry_List(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", enabled: true})
	r.Register(&mockTool{name: "read", aliases: []string{"r"}, enabled: true})

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 unique tools, got %d", len(list))
	}
	// Should be sorted
	if list[0].Name() != "bash" || list[1].Name() != "read" {
		t.Errorf("unexpected order: %s, %s", list[0].Name(), list[1].Name())
	}
}

func TestRegistry_EnabledTools(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", enabled: true})
	r.Register(&mockTool{name: "disabled_tool", enabled: false})
	r.Register(&mockTool{name: "read", enabled: true})

	enabled := r.EnabledTools()
	if len(enabled) != 2 {
		t.Fatalf("expected 2 enabled tools, got %d", len(enabled))
	}
	for _, tm := range enabled {
		if tm.Name() == "disabled_tool" {
			t.Error("disabled tool should not appear")
		}
	}
}

func TestRegistry_ToolMap(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", aliases: []string{"sh"}, enabled: true})
	r.Register(&mockTool{name: "disabled", enabled: false})

	m := r.ToolMap()
	if len(m) != 1 {
		t.Fatalf("expected 1 tool in map, got %d", len(m))
	}
	if _, ok := m["bash"]; !ok {
		t.Error("expected bash in map")
	}
	// Aliases should not appear as keys
	if _, ok := m["sh"]; ok {
		t.Error("alias should not be a key in ToolMap")
	}
}

func TestRegistry_Names(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "grep", aliases: []string{"search"}, enabled: true})
	r.Register(&mockTool{name: "bash", enabled: true})

	names := r.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names (aliases deduplicated), got %d", len(names))
	}
	if names[0] != "bash" || names[1] != "grep" {
		t.Errorf("expected sorted names, got %v", names)
	}
}

func TestRegistry_Size(t *testing.T) {
	r := tool.NewRegistry()
	if r.Size() != 0 {
		t.Errorf("expected 0, got %d", r.Size())
	}

	r.Register(&mockTool{name: "bash", aliases: []string{"sh"}, enabled: true})
	if r.Size() != 1 {
		t.Errorf("expected 1 (aliases not counted), got %d", r.Size())
	}

	r.Register(&mockTool{name: "read", enabled: true})
	if r.Size() != 2 {
		t.Errorf("expected 2, got %d", r.Size())
	}
}

func TestRegistry_MustRegister(t *testing.T) {
	r := tool.NewRegistry()
	r.MustRegister(&mockTool{name: "bash"})
	if r.Size() != 1 {
		t.Errorf("expected 1, got %d", r.Size())
	}
}

func TestRegistry_RegisterEmptyAlias(t *testing.T) {
	r := tool.NewRegistry()
	// An empty-string alias should be silently skipped (the `continue` branch)
	r.Register(&mockTool{name: "bash", aliases: []string{"", "sh"}, enabled: true})

	// "bash" registered
	tm, ok := r.Lookup("bash")
	if !ok || tm.Name() != "bash" {
		t.Error("expected to find bash")
	}
	// "sh" alias registered
	if _, ok := r.Lookup("sh"); !ok {
		t.Error("expected to find alias 'sh'")
	}
	// empty string should NOT be a key
	if _, ok := r.Lookup(""); ok {
		t.Error("empty alias should not be registered")
	}
}

func TestRegistry_RegisterOnlyEmptyAliases(t *testing.T) {
	r := tool.NewRegistry()
	// Only empty aliases — no alias gets registered, only the primary name
	r.Register(&mockTool{name: "read", aliases: []string{""}, enabled: true})
	if r.Size() != 1 {
		t.Errorf("expected 1 tool, got %d", r.Size())
	}
}

func TestRegistry_ToolMapFn(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", enabled: true})
	r.Register(&mockTool{name: "disabled", enabled: false})

	fn := r.ToolMapFn()
	m := fn()
	if len(m) != 1 {
		t.Fatalf("expected 1 enabled tool, got %d", len(m))
	}
	if _, ok := m["bash"]; !ok {
		t.Error("expected bash in map from ToolMapFn()")
	}
	if _, ok := m["disabled"]; ok {
		t.Error("disabled tool should not appear in ToolMapFn() result")
	}
}

func TestRegistry_NamesEmpty(t *testing.T) {
	r := tool.NewRegistry()
	names := r.Names()
	if len(names) != 0 {
		t.Errorf("expected 0 names, got %d", len(names))
	}
}

// Map iteration order is random per range, so dedup/sort/skip assertions that
// a mutation can satisfy by luck are repeated until a fluke would require a
// 1-in-several-e100 map ordering streak.

func TestRegistry_ListDedupCompletes(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", aliases: []string{"sh"}, enabled: true})
	r.Register(&mockTool{name: "read", enabled: true})
	for range 200 {
		if list := r.List(); len(list) != 2 {
			t.Fatalf("List() = %d tools, want 2 (alias duplicate leaked or iteration truncated)", len(list))
		}
	}
}

func TestRegistry_ListSortedDeterministically(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "zeta", enabled: true})
	r.Register(&mockTool{name: "alpha", enabled: true})
	r.Register(&mockTool{name: "mid", enabled: true})
	for range 200 {
		list := r.List()
		if len(list) != 3 || list[0].Name() != "alpha" || list[1].Name() != "mid" || list[2].Name() != "zeta" {
			t.Fatalf("List() order = %v, want [alpha mid zeta]", toolNames(list))
		}
	}
}

func TestRegistry_EnabledToolsDedupAndFilter(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "bash", aliases: []string{"sh"}, enabled: true})
	r.Register(&mockTool{name: "dead", enabled: false})

	enabled := r.EnabledTools()
	if len(enabled) != 1 {
		t.Fatalf("EnabledTools() = %d tools, want 1 (alias dup or disabled tool leaked)", len(enabled))
	}
	if enabled[0].Name() != "bash" {
		t.Errorf("EnabledTools()[0] = %s, want bash", enabled[0].Name())
	}
}

func TestRegistry_EnabledToolsSortedDeterministically(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "zeta", enabled: true})
	r.Register(&mockTool{name: "alpha", enabled: true})
	for range 200 {
		enabled := r.EnabledTools()
		if len(enabled) != 2 || enabled[0].Name() != "alpha" || enabled[1].Name() != "zeta" {
			t.Fatalf("EnabledTools() order = %v, want [alpha zeta]", toolNames(enabled))
		}
	}
}

func TestRegistry_ToolMapKeepsToolsAfterDisabled(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "aaa", enabled: true})
	r.Register(&mockTool{name: "mmm", enabled: false})
	r.Register(&mockTool{name: "zzz", enabled: true})
	for range 200 {
		m := r.ToolMap()
		if len(m) != 2 {
			t.Fatalf("ToolMap() = %d tools, want 2 (disabled tool or truncated iteration)", len(m))
		}
		if _, ok := m["aaa"]; !ok {
			t.Fatal("ToolMap() lost aaa after disabled tool")
		}
		if _, ok := m["zzz"]; !ok {
			t.Fatal("ToolMap() lost zzz after disabled tool")
		}
	}
}

// TestRegistry_ReadersExcludeConcurrentWrites exercises the bulk read methods
// against a concurrent registrar. Each of these methods touches the tools map
// across a whole iteration, so releasing the read lock early exposes an
// unsynchronized map range that the Go runtime reports as a fatal concurrent
// map access (or the race detector flags under -race).
func TestRegistry_ReadersExcludeConcurrentWrites(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "seed", enabled: true})

	const writers = 2
	const inserts = 300
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				r.List()
				r.EnabledTools()
				r.ToolMap()
				r.Names()
				r.Size()
			}
		})
	}
	for w := range writers {
		for i := range inserts {
			r.Register(&mockTool{name: fmt.Sprintf("w%d-tool-%04d", w, i), enabled: i%2 == 0})
		}
	}
	close(stop)
	wg.Wait()

	if s := r.Size(); s != writers*inserts+1 {
		t.Fatalf("Size() = %d, want %d", s, writers*inserts+1)
	}
}

// TestRegistry_LookupExcludesConcurrentWrites targets Lookup specifically:
// its unprotected window under a dropped RUnlock is a single map access, so
// the bulk-method hammer above rarely overlaps it. Lookup spins in a tight
// loop instead, and every miss of an existing key is itself a failure.
func TestRegistry_LookupExcludesConcurrentWrites(t *testing.T) {
	r := tool.NewRegistry()
	r.Register(&mockTool{name: "seed", enabled: true})

	const writers = 4
	const inserts = 1000
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, ok := r.Lookup("seed"); !ok {
					t.Error("Lookup(seed) missed while registrars were running")
					return
				}
			}
		})
	}
	var writersWG sync.WaitGroup
	for w := range writers {
		writersWG.Add(1)
		go func(w int) {
			defer writersWG.Done()
			for i := range inserts {
				r.Register(&mockTool{name: fmt.Sprintf("w%d-tool-%05d", w, i), enabled: true})
			}
		}(w)
	}
	writersWG.Wait()
	close(stop)
	readers.Wait()

	if s := r.Size(); s != writers*inserts+1 {
		t.Fatalf("Size() = %d, want %d", s, writers*inserts+1)
	}
}

func toolNames(ts []tool.Tool) []string {
	names := make([]string, len(ts))
	for i, tm := range ts {
		names[i] = tm.Name()
	}
	return names
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := tool.NewRegistry()
	const n = 100
	done := make(chan struct{}, n*2)

	// Concurrent writes
	for i := range n {
		go func(idx int) {
			r.Register(&mockTool{name: fmt.Sprintf("tool-%03d", idx), enabled: true})
			done <- struct{}{}
		}(i)
	}
	// Wait for all writes
	for range n {
		<-done
	}

	// Concurrent reads with assertions
	for i := range n {
		go func(idx int) {
			name := fmt.Sprintf("tool-%03d", idx)
			if _, ok := r.Lookup(name); !ok {
				t.Errorf("tool-%03d not found", idx)
			}
			if list := r.List(); len(list) != n {
				t.Errorf("List() = %d tools, want %d", len(list), n)
			}
			if names := r.Names(); len(names) != n {
				t.Errorf("Names() = %d, want %d", len(names), n)
			}
			if enabled := r.EnabledTools(); len(enabled) != n {
				t.Errorf("EnabledTools() = %d, want %d", len(enabled), n)
			}
			if m := r.ToolMap(); len(m) != n {
				t.Errorf("ToolMap() = %d, want %d", len(m), n)
			}
			if s := r.Size(); s != n {
				t.Errorf("Size() = %d, want %d", s, n)
			}
			done <- struct{}{}
		}(i)
	}
	for range n {
		<-done
	}

	if r.Size() != n {
		t.Errorf("expected %d tools, got %d", n, r.Size())
	}
}
