package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
	defer registry.Close()

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
	defer registry.Close()

	// A config could be created but is intentionally not registered —
	// the test verifies that an unregistered server name produces
	// "server not found" via GetConnection.
	_ = mcp.ScopedMcpServerConfig{
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
	defer registry.Close()

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

// ---------------------------------------------------------------------------
// refreshTools wireup — RED test for MCP tool merging
// ---------------------------------------------------------------------------

// TestEngine_RefreshTools_MergesMCPTools verifies that refreshTools() includes
// MCP tools from the registry in the engine's tool map.
// This is the wireup that connects Registry → Engine → callLLM tool list.
//
// BUG: Currently refreshTools() only rebuilds from toolsProvider (built-in tools)
// and ignores mcpRegistry. This test should FAIL until that is fixed.
func TestEngine_RefreshTools_MergesMCPTools(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()

	// Simulate a built-in tool via ToolsProvider
	builtIn := map[string]tool.Tool{
		"Bash": &stubTool{name: "Bash"},
	}

	eng := New(&Params{
		ToolsProvider: func() map[string]tool.Tool { return builtIn },
		MCPRegistry:   registry,
	})
	defer eng.Close()

	// Pre-populate registry with a discovered tool (simulates ConnectAll result)
	// We directly inject into the registry's tool list — this is the same state
	// that would exist after a successful ConnectAll + discovery.
	echoTool := mcp.DiscoveredTool{
		Name:         "mcp__echo-srv__echo",
		OriginalName: "echo",
		ServerName:   "echo-srv",
		Description:  "Echo tool",
	}
	registry.SetToolsForTest([]mcp.DiscoveredTool{echoTool})

	// refreshTools rebuilds e.tools from toolsProvider
	eng.refreshTools()

	// Verify built-in tool is present
	if _, ok := eng.tools["Bash"]; !ok {
		t.Error("built-in tool 'Bash' missing from engine.tools after refreshTools")
	}

	// RED: MCP tool should be present but currently refreshTools ignores registry
	if _, ok := eng.tools["mcp__echo-srv__echo"]; !ok {
		t.Error("MCP tool 'mcp__echo-srv__echo' missing from engine.tools after refreshTools — " +
			"refreshTools() does not merge MCP tools from registry")
	}

	// Verify toolOrder includes MCP tool
	found := slices.Contains(eng.toolOrder, "mcp__echo-srv__echo")
	if !found {
		t.Error("toolOrder does not include MCP tool 'mcp__echo-srv__echo'")
	}
}

// stubTool is a minimal tool.Tool implementation for testing.
type stubTool struct {
	name string
}

func (s *stubTool) Name() string                                { return s.name }
func (s *stubTool) Aliases() []string                           { return nil }
func (s *stubTool) InputSchema() json.RawMessage                { return nil }
func (s *stubTool) Description(json.RawMessage) (string, error) { return "", nil }
func (s *stubTool) IsEnabled() bool                             { return true }
func (s *stubTool) InterruptBehavior() tool.InterruptBehavior   { return tool.InterruptCancel }
func (s *stubTool) MaxResultSize() int                          { return 0 }
func (s *stubTool) Prompt() string                              { return "" }
func (s *stubTool) RenderResult(any) string                     { return "" }
func (s *stubTool) Call(context.Context, json.RawMessage, *types.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (s *stubTool) CheckPermissions(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (s *stubTool) IsReadOnly(json.RawMessage) bool        { return false }
func (s *stubTool) IsDestructive(json.RawMessage) bool     { return false }
func (s *stubTool) IsConcurrencySafe(json.RawMessage) bool { return false }

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

func TestEngine_AllTools_WithMCPTools(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()

	// Inject MCP tool via SetToolsForTest
	registry.SetToolsForTest([]mcp.DiscoveredTool{
		{Name: "mcp__test__echo", OriginalName: "echo", ServerName: "test"},
	})

	eng := New(&Params{
		MCPRegistry: registry,
	})
	defer eng.Close()

	all := eng.AllTools()
	if len(all) != 1 {
		t.Errorf("expected 1 tool, got %d", len(all))
	}
	if _, ok := all["mcp__test__echo"]; !ok {
		t.Error("expected mcp__test__echo in AllTools")
	}
}

// ---------------------------------------------------------------------------
// MCPTool.Call — in-memory MCP server end-to-end test
// ---------------------------------------------------------------------------

// setupInMemoryEchoServer creates an in-memory MCP server with an echo tool,
// connects a client, and returns the ConnectedServer ready for use.
func setupInMemoryEchoServer(t *testing.T) *mcp.ConnectedServer {
	t.Helper()
	t1, t2 := mcpsdk.NewInMemoryTransports()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name: "test-server", Version: "1.0.0",
	}, nil)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "echo", Description: "Echoes input",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args map[string]any) (*mcpsdk.CallToolResult, any, error) {
		text, _ := args["text"].(string)
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
		}, nil, nil
	})

	go func() { _, _ = server.Connect(context.Background(), t1, nil) }()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	return &mcp.ConnectedServer{
		Name:       "test-srv",
		ServerInfo: &mcp.ServerInfo{Name: "test-server", Version: "1.0.0"},
		Session:    session,
	}
}

func TestMCPTool_Call_InMemoryServer_Success(t *testing.T) {
	cs := setupInMemoryEchoServer(t)

	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__echo",
		OriginalName: "echo",
		ServerName:   "test-srv",
	}, registry)

	result, err := tl.Call(context.Background(), json.RawMessage(`{"text":"Hello MCP!"}`), nil)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if result.Data == nil {
		t.Fatal("expected non-nil result data")
	}
	text, ok := result.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %T", result.Data)
	}
	if text != "Hello MCP!" {
		t.Errorf("echo result = %q, want %q", text, "Hello MCP!")
	}
}

func TestMCPTool_Call_InMemoryServer_InvalidInput(t *testing.T) {
	cs := setupInMemoryEchoServer(t)

	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__echo",
		OriginalName: "echo",
		ServerName:   "test-srv",
	}, registry)

	_, err := tl.Call(context.Background(), json.RawMessage(`{invalid json`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("error should mention invalid input, got: %v", err)
	}
}

func TestMCPTool_Call_InMemoryServer_EmptyInput(t *testing.T) {
	cs := setupInMemoryEchoServer(t)

	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__echo",
		OriginalName: "echo",
		ServerName:   "test-srv",
	}, registry)

	// Empty input should still work (args will be nil)
	result, err := tl.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Call with nil input failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestMCPTool_Call_NotConnectedServer(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()

	// Inject a FailedServer (not *ConnectedServer) to test the type assertion path
	registry.SetConnectionForTest("bad-srv", &mcp.FailedServer{Name: "bad-srv", Error: "connection failed"})

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__bad-srv__echo",
		OriginalName: "echo",
		ServerName:   "bad-srv",
	}, registry)

	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error for non-connected server")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error should mention not connected, got: %v", err)
	}
}
