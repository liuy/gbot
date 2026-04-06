package tool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
)

// mockTool implements tool.Tool for registry tests.
type mockTool struct {
	name    string
	aliases []string
	enabled bool
}

func (m *mockTool) Name() string                                                { return m.name }
func (m *mockTool) Aliases() []string                                           { return m.aliases }
func (m *mockTool) Description(json.RawMessage) (string, error)                 { return "", nil }
func (m *mockTool) InputSchema() json.RawMessage                                { return nil }
func (m *mockTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (m *mockTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (m *mockTool) IsReadOnly(json.RawMessage) bool            { return false }
func (m *mockTool) IsDestructive(json.RawMessage) bool         { return false }
func (m *mockTool) IsConcurrencySafe(json.RawMessage) bool     { return false }
func (m *mockTool) IsEnabled() bool                            { return m.enabled }
func (m *mockTool) InterruptBehavior() tool.InterruptBehavior  { return tool.InterruptCancel }
func (m *mockTool) Prompt() string                             { return "" }

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

func TestRegistry_NamesEmpty(t *testing.T) {
	r := tool.NewRegistry()
	names := r.Names()
	if len(names) != 0 {
		t.Errorf("expected 0 names, got %d", len(names))
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := tool.NewRegistry()
	const n = 100
	done := make(chan struct{}, n*2)

	// Concurrent writes
	for i := 0; i < n; i++ {
		go func(idx int) {
			r.Register(&mockTool{name: fmt.Sprintf("tool-%03d", idx), enabled: true})
			done <- struct{}{}
		}(i)
	}
	// Wait for all writes
	for i := 0; i < n; i++ {
		<-done
	}

	// Concurrent reads
	for i := 0; i < n; i++ {
		go func(idx int) {
			name := fmt.Sprintf("tool-%03d", idx)
			if _, ok := r.Lookup(name); !ok {
				t.Errorf("tool-%03d not found", idx)
			}
			_ = r.List()
			_ = r.Names()
			_ = r.EnabledTools()
			_ = r.ToolMap()
			_ = r.Size()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}

	if r.Size() != n {
		t.Errorf("expected %d tools, got %d", n, r.Size())
	}
}
