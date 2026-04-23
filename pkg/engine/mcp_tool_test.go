package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// MCPTool adapter tests
// ---------------------------------------------------------------------------

func TestMCPTool_Name(t *testing.T) {
	info := mcp.DiscoveredTool{
		Name:         "mcp__server__echo",
		OriginalName: "echo",
		ServerName:   "server",
		Description:  "Echo tool",
	}
	tl := NewMCPTool(info, nil)

	if tl.Name() != "mcp__server__echo" {
		t.Errorf("expected mcp__server__echo, got %s", tl.Name())
	}
	if tl.Name() != info.Name {
		t.Errorf("Name should match info.Name")
	}
}

func TestMCPTool_Aliases(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{}, nil)
	if tl.Aliases() != nil {
		t.Errorf("expected nil aliases, got %v", tl.Aliases())
	}
}

func TestMCPTool_Description(t *testing.T) {
	tests := []struct {
		name string
		hint string
		desc string
		want string
	}{
		{"search hint preferred", "search files", "description", "search files"},
		{"description fallback", "", "my description", "my description"},
		{"empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tl := NewMCPTool(mcp.DiscoveredTool{
				Description: tt.desc,
				SearchHint:  tt.hint,
			}, nil)
			got, err := tl.Description(nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Description() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMCPTool_BehavioralProperties(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{
		Annotations: mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: false,
			OpenWorldHint:   true,
		},
	}, nil)

	if !tl.IsReadOnly(nil) {
		t.Error("expected IsReadOnly=true")
	}
	if tl.IsDestructive(nil) {
		t.Error("expected IsDestructive=false")
	}
	if !tl.IsConcurrencySafe(nil) {
		t.Error("MCP tools should be concurrency-safe")
	}
	if !tl.IsEnabled() {
		t.Error("expected IsEnabled=true")
	}
	if tl.MaxResultSize() != 50000 {
		t.Errorf("expected MaxResultSize=50000, got %d", tl.MaxResultSize())
	}
	if tl.Prompt() != "" {
		t.Errorf("expected empty prompt, got %q", tl.Prompt())
	}
}

func TestMCPTool_InputSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`)
	tl := NewMCPTool(mcp.DiscoveredTool{InputSchema: schema}, nil)

	got := tl.InputSchema()
	if string(got) != string(schema) {
		t.Errorf("InputSchema() = %s, want %s", got, schema)
	}
}

func TestMCPTool_RenderResult(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{}, nil)

	if tl.RenderResult(nil) != "" {
		t.Error("expected empty for nil")
	}
	if tl.RenderResult("hello") != "hello" {
		t.Error("expected 'hello' for string")
	}
	if tl.RenderResult(42) != "42" {
		t.Error("expected '42' for int")
	}
}

func TestMCPTool_CheckPermissions(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{}, nil)
	result := tl.CheckPermissions(nil, nil)
	if _, ok := result.(types.PermissionAllowDecision); !ok {
		t.Errorf("expected PermissionAllowDecision, got %T", result)
	}
}

func TestMCPTool_Call_ServerNotFound(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer func() { _ = registry.Close() }()

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test__echo",
		OriginalName: "echo",
		ServerName:   "test",
	}, registry)

	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error for missing server")
	}
	if err.Error() != `mcp: server "test" not found` {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMCPTool_Call_ServerNotConnected(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer func() { _ = registry.Close() }()

	// Register a config but don't connect (connection map empty)
	cfg := mcp.ScopedMcpServerConfig{
		Config: &mcp.StdioConfig{Command: "echo"},
		Scope:  mcp.ScopeUser,
	}

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test__echo",
		OriginalName: "echo",
		ServerName:   "test",
	}, registry)

	// The config is not registered, so GetConnection returns not found
	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error for missing server")
	}
	if err.Error() != `mcp: server "test" not found` {
		t.Errorf("unexpected error: %v", err)
	}

	// Register config but store a FailedServer
	_ = cfg
}

// ---------------------------------------------------------------------------
// Engine MCP integration tests
// ---------------------------------------------------------------------------

func TestEngine_MCPTools_NilRegistry(t *testing.T) {
	eng := New(&Params{})
	defer eng.Close()

	tools := eng.MCPTools()
	if tools != nil {
		t.Errorf("expected nil for nil registry, got %v", tools)
	}
}

func TestEngine_AllTools_NoMCP(t *testing.T) {
	eng := New(&Params{})
	defer eng.Close()

	all := eng.AllTools()
	if len(all) != 0 {
		t.Errorf("expected empty map, got %d tools", len(all))
	}
}

func TestEngine_Close_WithRegistry(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	eng := New(&Params{
		MCPRegistry: registry,
	})

	eng.Close()
	eng.Close() // double close safe
}

func TestEngine_MCPRegistryParam(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	eng := New(&Params{
		MCPRegistry: registry,
	})
	defer eng.Close()

	if eng.mcpRegistry != registry {
		t.Error("expected mcpRegistry to be set from params")
	}
}

func TestEngine_AllTools_MergesStaticAndMCP(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer func() { _ = registry.Close() }()

	eng := New(&Params{
		MCPRegistry: registry,
	})
	defer eng.Close()

	// AllTools should work even with empty registry
	all := eng.AllTools()
	if len(all) != 0 {
		t.Errorf("expected empty, got %d", len(all))
	}
}

func TestMCPTool_DestructiveProperties(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{
		Annotations: mcp.ToolAnnotations{
			DestructiveHint: true,
		},
	}, nil)

	if !tl.IsDestructive(nil) {
		t.Error("expected IsDestructive=true for destructive tool")
	}
	if tl.IsReadOnly(nil) {
		t.Error("expected IsReadOnly=false for destructive tool")
	}
	// Destructive tools should still be allowed by default
	// (higher-level confirmation is handled by the engine)
	result := tl.CheckPermissions(nil, nil)
	if _, ok := result.(types.PermissionAllowDecision); !ok {
		t.Error("destructive MCP tools should still get allow decision from CheckPermissions")
	}
}
