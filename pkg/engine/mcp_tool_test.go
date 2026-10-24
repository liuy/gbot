package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestMCPTool_Call_ServerNotFound_TriesReconnect(t *testing.T) {
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
	// Should attempt reconnect before giving up
	if !strings.Contains(err.Error(), `server "test" not found`) {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "reconnect failed") {
		t.Errorf("error should mention reconnect attempt: %v", err)
	}
	// The reconnect failure must be wrapped, not flattened into the text —
	// callers walk the chain (errors.Is/As) to tell a dead registry entry
	// from an unreachable server.
	inner := errors.Unwrap(err)
	if inner == nil {
		t.Fatalf("reconnect error is not in the wrap chain (Unwrap = nil): %v", err)
	}
	if !strings.Contains(inner.Error(), "not found in registry") {
		t.Errorf("wrapped reconnect error = %q, want the registry's not-found error", inner.Error())
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

	// The config is not registered, so GetConnection returns not found.
	// Call should attempt reconnect (which also fails), then report.
	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error for missing server")
	}
	if !strings.Contains(err.Error(), `server "test" not found`) {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "reconnect failed") {
		t.Errorf("error should mention reconnect attempt: %v", err)
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

type fakeServerConn struct{}

func (fakeServerConn) ConnType() string { return "fake" }

// Engines borrow the daemon's registry; closing any engine — including the
// transient session-memory sub-engines spawned every turn — must leave the
// shared connections alive. Only the registry's owner tears it down.
func TestEngine_Close_WithRegistry(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConnectionForTest("srv", fakeServerConn{})

	eng := New(&Params{MCPRegistry: registry})
	eng.Close()
	eng.Close() // double close safe

	if _, ok := registry.GetConnection("srv"); !ok {
		t.Fatal("engine Close killed the shared registry's connection")
	}

	if err := registry.Close(); err != nil {
		t.Fatalf("owner registry close: %v", err)
	}
	if _, ok := registry.GetConnection("srv"); ok {
		t.Fatal("owner registry close left the connection in place")
	}
}

// The production trigger: a sub-engine (session-memory, sub-agents) closes
// while every other session still uses the same registry.
func TestEngineClose_SubEngineKeepsSharedRegistry(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConnectionForTest("srv", fakeServerConn{})

	parent := New(&Params{MCPRegistry: registry})
	t.Cleanup(parent.Close)
	sub := parent.NewSubEngine(SubEngineOptions{})
	sub.Close()

	if _, ok := registry.GetConnection("srv"); !ok {
		t.Fatal("sub-engine Close killed the shared registry's connection")
	}
}

func TestEngine_MCPRegistryParam(t *testing.T) {
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
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
// Currently refreshTools() only rebuilds from toolsProvider (built-in tools)
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
	name   string
	prompt string
}

func (s *stubTool) Name() string                                { return s.name }
func (s *stubTool) Aliases() []string                           { return nil }
func (s *stubTool) InputSchema() json.RawMessage                { return nil }
func (s *stubTool) Description(json.RawMessage) (string, error) { return "", nil }
func (s *stubTool) IsEnabled() bool                             { return true }
func (s *stubTool) InterruptBehavior() tool.InterruptBehavior   { return tool.InterruptCancel }
func (s *stubTool) MaxResultSize() int                          { return 0 }
func (s *stubTool) Prompt() string                              { return s.prompt }
func (s *stubTool) RenderResult(any) string                     { return "" }
func (s *stubTool) Call(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, nil
}
func (s *stubTool) CheckPermissions(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}
func (s *stubTool) IsReadOnly(json.RawMessage) bool        { return false }
func (s *stubTool) IsDestructive(json.RawMessage) bool     { return false }
func (s *stubTool) IsConcurrencySafe(json.RawMessage) bool { return false }

func TestMCPTool_Summary_WithSearchHint(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{
		SearchHint: "fetch web pages",
	}, nil)

	got := tl.Summary(nil)
	if got != "fetch web pages" {
		t.Errorf("Summary() = %q, want %q", got, "fetch web pages")
	}
}

func TestMCPTool_Summary_ExtractsURL(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{}, nil)

	got := tl.Summary(json.RawMessage(`{"url":"https://example.com/page"}`))
	if got != "https://example.com/page" {
		t.Errorf("Summary() = %q, want %q", got, "https://example.com/page")
	}
}

func TestMCPTool_Summary_ExtractsQuery(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{}, nil)

	got := tl.Summary(json.RawMessage(`{"query":"find files"}`))
	if got != "find files" {
		t.Errorf("Summary() = %q, want %q", got, "find files")
	}
}

func TestMCPTool_Summary_EmptyInput(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{}, nil)

	got := tl.Summary(nil)
	if got != "" {
		t.Errorf("Summary() = %q, want empty", got)
	}
}

func TestMCPTool_Summary_SearchHintOverridesParams(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{
		SearchHint: "search files",
	}, nil)

	got := tl.Summary(json.RawMessage(`{"url":"https://example.com"}`))
	if got != "search files" {
		t.Errorf("Summary() = %q, want SearchHint %q", got, "search files")
	}
}

func TestExtractCommonParams(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"url field", `{"url":"https://example.com"}`, "https://example.com"},
		{"query field", `{"query":"search term"}`, "search term"},
		{"file_path field", `{"file_path":"/tmp/test.go"}`, "/tmp/test.go"},
		{"pattern field", `{"pattern":"*.go"}`, "*.go"},
		{"command field", `{"command":"ls -la"}`, "ls -la"},
		{"path field", `{"path":"/home/user"}`, "/home/user"},
		{"url priority over query", `{"url":"https://x.com","query":"test"}`, "https://x.com"},
		{"no match", `{"something":"value"}`, ""},
		{"empty input", "", ""},
		{"60-char value passes the limit untouched", `{"url":"` + strings.Repeat("a", 60) + `"}`, strings.Repeat("a", 60)},
		{"61-char value truncates at the limit", `{"url":"` + strings.Repeat("b", 61) + `"}`, strings.Repeat("b", 60) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommonParams(tt.input)
			if got != tt.want {
				t.Errorf("extractCommonParams(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
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

	// echo-meta mirrors echo but tags the result with server-supplied _meta,
	// for assertions on the metadata the engine preserves.
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "echo-meta", Description: "Echoes input with result metadata",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args map[string]any) (*mcpsdk.CallToolResult, any, error) {
		text, _ := args["text"].(string)
		return &mcpsdk.CallToolResult{
			Meta:    mcpsdk.Meta{"trail": "mk-9"},
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
		}, nil, nil
	})

	// slow holds the call open so tests can observe state while a call is
	// in flight.
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "slow", Description: "Echoes input after a delay",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args map[string]any) (*mcpsdk.CallToolResult, any, error) {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		text, _ := args["text"].(string)
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
		}, nil, nil
	})

	type serverResult struct {
		session *mcpsdk.ServerSession
		err     error
	}
	ch := make(chan serverResult, 1)
	go func() {
		s, err := server.Connect(context.Background(), t1, nil)
		ch <- serverResult{s, err}
	}()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	// Wait for server to finish connecting so we can clean it up.
	var serverSession *mcpsdk.ServerSession
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("server connect: %v", r.err)
		}
		serverSession = r.session
	case <-time.After(5 * time.Second):
		t.Fatal("server connect timed out")
	}

	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Close()
	})

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
	// The JSON parse error must stay wrapped so callers can branch on the
	// concrete decode failure instead of parsing message text.
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("invalid-input error does not wrap *json.SyntaxError: %v", err)
	}
}

// A one-byte input still parses (and fails) — the empty-input fast path is
// reserved for the truly empty argument list, not for short bytes.
func TestMCPTool_Call_SingleByteInputStillParses(t *testing.T) {
	cs := setupInMemoryEchoServer(t)
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__echo",
		OriginalName: "echo",
		ServerName:   "test-srv",
	}, registry)

	_, err := tl.Call(context.Background(), json.RawMessage(`x`), nil)
	if err == nil {
		t.Fatal("single-byte non-JSON input must be rejected by the args parse")
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

	// A FailedServer with no stored config: the call attempts one Reconnect,
	// which fails because there is no config to reconnect from.
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
	if !strings.Contains(err.Error(), "reconnect failed") {
		t.Errorf("failed server must attempt one reconnect, got: %v", err)
	}

	// A DisabledServer (no reconnect attempt for non-failed states) still hits
	// the type-assertion path. The error must name the server and its state —
	// a generic "not connected" would hide which server and why.
	registry.SetConnectionForTest("off-srv", &mcp.DisabledServer{Name: "off-srv"})
	tl2 := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__off-srv__echo",
		OriginalName: "echo",
		ServerName:   "off-srv",
	}, registry)
	_, err = tl2.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error for disabled server")
	}
	if !strings.Contains(err.Error(), `server "off-srv" not connected`) || !strings.Contains(err.Error(), "state: disabled") {
		t.Errorf("disabled server error must carry the server name and state, got: %v", err)
	}
}

func TestMCPTool_Call_NilToolUseContext(t *testing.T) {
	cs := setupInMemoryEchoServer(t)

	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__echo",
		OriginalName: "echo",
		ServerName:   "test-srv",
	}, registry)

	// nil tctx should work — OnProgress is nil, no progress token registered
	result, err := tl.Call(context.Background(), json.RawMessage(`{"text":"hello"}`), nil)
	if err != nil {
		t.Fatalf("Call with nil tctx failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	text, ok := result.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %T", result.Data)
	}
	if text != "hello" {
		t.Errorf("result = %q, want %q", text, "hello")
	}
}

func TestMCPTool_Call_WithToolUseContext_NoProgress(t *testing.T) {
	cs := setupInMemoryEchoServer(t)

	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__echo",
		OriginalName: "echo",
		ServerName:   "test-srv",
	}, registry)

	// tctx with OnProgress set, but the echo tool doesn't send progress
	progressCalled := false
	tctx := &tool.ToolUseContext{
		OnProgress: func(u tool.ProgressUpdate) {
			progressCalled = true
		},
	}

	result, err := tl.Call(context.Background(), json.RawMessage(`{"text":"hello"}`), tctx)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	text, ok := result.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %T", result.Data)
	}
	if text != "hello" {
		t.Errorf("result = %q, want %q", text, "hello")
	}
	// Echo tool doesn't send progress, so OnProgress should not be called
	if progressCalled {
		t.Error("OnProgress should not be called for a tool that doesn't send progress")
	}
}

func TestMCPTool_IsSearchOrRead(t *testing.T) {
	tests := []struct {
		name      string
		info      mcp.DiscoveredTool
		wantIsSet bool
	}{
		{
			"search tool returns IsSearch",
			mcp.DiscoveredTool{
				Name: "mcp__ripgrep__search", OriginalName: "search", ServerName: "ripgrep",
				Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			true,
		},
		{
			"non-search tool returns zero",
			mcp.DiscoveredTool{
				Name: "mcp__server__run", OriginalName: "run", ServerName: "server",
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tl := NewMCPTool(tt.info, nil)
			srk := tl.IsSearchOrRead(nil)
			if srk.IsCollapsible() != tt.wantIsSet {
				t.Errorf("IsSearchOrRead() = %+v, want IsCollapsible=%v", srk, tt.wantIsSet)
			}
		})
	}
}

func TestMCPTool_DecodeResult_ArrayForm(t *testing.T) {
	t.Parallel()

	tl := NewMCPTool(mcp.DiscoveredTool{Name: "mcp__s__echo"}, nil)
	inner := `"hello from MCP"`
	textBytes, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textBytes) + `}]`)
	v, err := tl.DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want string", v)
	}
	if s != "hello from MCP" {
		t.Errorf("result = %q, want %q", s, "hello from MCP")
	}
}

func TestMCPTool_DecodeResult_RejectsBareStruct(t *testing.T) {
	t.Parallel()

	tl := NewMCPTool(mcp.DiscoveredTool{Name: "mcp__s__echo"}, nil)
	_, err := tl.DecodeResult(json.RawMessage(`"hello from MCP"`))
	if err == nil {
		t.Error("DecodeResult must reject non-array-form input")
	}
}

// Wire text that is not itself a JSON string literal must come back as the
// raw text — the unwrap layer is only for legacy double-encoded wires.
func TestMCPTool_DecodeResult_PlainTextPassesThrough(t *testing.T) {
	t.Parallel()

	tl := NewMCPTool(mcp.DiscoveredTool{Name: "mcp__s__echo"}, nil)
	v, err := tl.DecodeResult(json.RawMessage(`[{"type":"text","text":"plain 42 output"}]`))
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	if s, ok := v.(string); !ok || s != "plain 42 output" {
		t.Errorf("DecodeResult = %#v, want the raw wire text %q", v, "plain 42 output")
	}
}

// ---------------------------------------------------------------------------
// Mode matrix — server mode × per-tool alwaysLoad
// ---------------------------------------------------------------------------

func TestMCPTool_ModeMatrix(t *testing.T) {
	cases := []struct {
		name         string
		mode         mcp.ToolMode
		alwaysLoad   bool
		wantDeferred bool
		wantCodeOnly bool
	}{
		// Historical behavior pinned: no mode → deferred unless alwaysLoad.
		{"unset alwaysLoad=false", "", false, true, false},
		{"unset alwaysLoad=true", "", true, false, false},
		{"deferred alwaysLoad=false", mcp.ModeDeferred, false, true, false},
		{"deferred alwaysLoad=true", mcp.ModeDeferred, true, false, false},
		// direct enters the initial list regardless of alwaysLoad.
		{"direct alwaysLoad=false", mcp.ModeDirect, false, false, false},
		{"direct alwaysLoad=true", mcp.ModeDirect, true, false, false},
		// code overrides the server-side _meta alwaysLoad signal and is a
		// separate axis from deferred.
		{"code alwaysLoad=false", mcp.ModeCode, false, false, true},
		{"code alwaysLoad=true", mcp.ModeCode, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tl := NewMCPTool(mcp.DiscoveredTool{
				Name:       "mcp__s__t",
				ServerName: "s",
				AlwaysLoad: tc.alwaysLoad,
				Mode:       tc.mode,
			}, nil)
			if got := tl.IsDeferred(); got != tc.wantDeferred {
				t.Errorf("IsDeferred() = %v, want %v", got, tc.wantDeferred)
			}
			if got := tl.IsCodeOnly(); got != tc.wantCodeOnly {
				t.Errorf("IsCodeOnly() = %v, want %v", got, tc.wantCodeOnly)
			}
			if got := tool.IsDeferred(tl); got != tc.wantDeferred {
				t.Errorf("tool.IsDeferred = %v, want %v", got, tc.wantDeferred)
			}
			if got := tool.IsCodeOnly(tl); got != tc.wantCodeOnly {
				t.Errorf("tool.IsCodeOnly = %v, want %v", got, tc.wantCodeOnly)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MCPTool.Call — progress routing and metadata preservation
// ---------------------------------------------------------------------------

// Progress notifications from the server process must reach tctx.OnProgress
// as formatted snapshot lines — and an update that formats to empty (no
// message, no totals) must not surface at all. End-to-end over a real stdio
// helper because the client-level progress routing only exists on
// manager-built clients.
func TestMCPTool_Call_ProgressUpdatesReachToolContext(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newDeathHelperRegistry(t, pidFile, "serve")
	waitDeathHelperPIDs(t, pidFile, 1)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__helper__progress",
		OriginalName: "progress",
		ServerName:   "helper",
	}, reg)

	var mu sync.Mutex
	var updates []tool.ProgressUpdate
	tctx := &tool.ToolUseContext{
		OnProgress: func(u tool.ProgressUpdate) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, u)
		},
	}
	res, err := tl.Call(context.Background(), json.RawMessage(`{}`), tctx)
	if err != nil {
		t.Fatalf("progress tool call: %v", err)
	}
	if s, ok := res.Data.(string); !ok || s != "progress-done" {
		t.Fatalf("progress tool result = %#v, want text %q", res.Data, "progress-done")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 1 {
		t.Fatalf("progress updates delivered = %d, want exactly 1 (empty-formatted update must be dropped): %+v", len(updates), updates)
	}
	if !slices.Equal(updates[0].Lines, []string{"wired progress (1/2)"}) {
		t.Errorf("progress update Lines = %q, want [\"wired progress (1/2)\"]", updates[0].Lines)
	}
}

// A session that died mid-flight with no stored config: the death-retry
// must surface both the original failure and the reconnect failure in one
// error, so the caller can tell a dead server from an unreachable one.
func TestMCPTool_Call_DeadSessionReconnectFailureSurfaces(t *testing.T) {
	cs := setupInMemoryEchoServer(t)
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConnectionForTest("srv", cs)

	if err := cs.Session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__srv__echo",
		OriginalName: "echo",
		ServerName:   "srv",
	}, registry)
	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("call against a dead session with no reconnect config must fail")
	}
	if !strings.Contains(err.Error(), "connection died") || !strings.Contains(err.Error(), "reconnect failed") {
		t.Errorf("error must carry the dead connection and the failed reconnect, got: %v", err)
	}
	// The reconnect failure must stay wrapped — same chain contract as the
	// not-found path, checked here on the death-retry branch.
	inner := errors.Unwrap(err)
	if inner == nil {
		t.Fatalf("reconnect error is not in the wrap chain (Unwrap = nil): %v", err)
	}
	if !strings.Contains(inner.Error(), "not found in registry") {
		t.Errorf("wrapped reconnect error = %q, want the registry's not-found error", inner.Error())
	}
	// The dead entry must not survive the failed retry: eviction is what
	// routes every later call through reconnect instead of a dead session.
	if conn, ok := registry.GetConnection("srv"); ok {
		t.Errorf("registry still holds srv (state %q) after the dead-session retry — eviction never ran", conn.ConnType())
	}
}

// A death-retry whose reconnect "succeeds" into a non-connected state (a
// FailedServer from a broken spawn config) must surface the original death
// plus the state it landed in — not a bare nil.
func TestMCPTool_Call_DeadSessionReconnectReturnsFailedState(t *testing.T) {
	cs := setupInMemoryEchoServer(t)
	registry := mcp.NewRegistry(mcp.NewClientManager(mcp.TransportFactory{}, false, ""), mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConfigsForTest(map[string]mcp.ScopedMcpServerConfig{
		"srv": {Config: &mcp.StdioConfig{Command: "/nonexistent-binary-xyz"}, Scope: mcp.ScopeUser},
	})
	registry.SetConnectionForTest("srv", cs)
	if err := cs.Session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__srv__echo",
		OriginalName: "echo",
		ServerName:   "srv",
	}, registry)
	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("call against a dead session whose respawn config is broken must fail")
	}
	if !strings.Contains(err.Error(), "connection died") || !strings.Contains(err.Error(), "reconnect returned state failed") {
		t.Errorf("error must carry the death and the post-reconnect state, got: %v", err)
	}
}

// Server-supplied result metadata must survive into the tool result's
// MCPMeta — downstream consumers key resource hints off it.
func TestMCPTool_Call_PreservesResultMeta(t *testing.T) {
	cs := setupInMemoryEchoServer(t)

	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__echo-meta",
		OriginalName: "echo-meta",
		ServerName:   "test-srv",
	}, registry)

	res, err := tl.Call(context.Background(), json.RawMessage(`{"text":"hello"}`), nil)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if res.MCPMeta == nil {
		t.Fatal("result carries no MCPMeta — server _meta dropped on the floor")
	}
	if got, _ := res.MCPMeta.Meta["trail"].(string); got != "mk-9" {
		t.Errorf("MCPMeta[trail] = %q, want %q", got, "mk-9")
	}
}

// A tool-use context without an OnProgress sink must suppress the progress
// channel entirely: no token is registered, the server emits nothing, and
// the call completes through the plain path.
func TestMCPTool_Call_ToolContextWithoutProgressSink(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newDeathHelperRegistry(t, pidFile, "serve")
	waitDeathHelperPIDs(t, pidFile, 1)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__helper__progress",
		OriginalName: "progress",
		ServerName:   "helper",
	}, reg)

	res, err := tl.Call(context.Background(), json.RawMessage(`{}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("progress tool call without a sink failed: %v", err)
	}
	if s, ok := res.Data.(string); !ok || s != "progress-done" {
		t.Fatalf("progress tool result = %#v, want text %q", res.Data, "progress-done")
	}
}

// SearchHint hands ToolSearch the curated hint verbatim — an empty return
// would drop the tool from relevance scoring.
func TestMCPTool_SearchHint(t *testing.T) {
	tl := NewMCPTool(mcp.DiscoveredTool{SearchHint: "fetch web pages"}, nil)
	if got := tl.SearchHint(); got != "fetch web pages" {
		t.Errorf("SearchHint() = %q, want %q", got, "fetch web pages")
	}
}

// The in-flight counter must balance: after a call returns, the server's
// slot is free again, so a config reload's drain does not wait on a call
// that already finished.
func TestMCPTool_Call_InFlightCounterBalances(t *testing.T) {
	cs := setupInMemoryEchoServer(t)
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__echo",
		OriginalName: "echo",
		ServerName:   "test-srv",
	}, registry)

	if _, err := tl.Call(context.Background(), json.RawMessage(`{"text":"hi"}`), nil); err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	idle, remaining := registry.DrainServer("test-srv", 2*time.Second)
	if !idle || remaining != 0 {
		t.Errorf("DrainServer after a completed call = (idle=%v, remaining=%d), want (true, 0) — the in-flight slot leaked", idle, remaining)
	}
}

// While a call is running, the server's in-flight slot must be held — a
// reload that drains mid-call must see it and wait, not reap the server
// out from under the live call.
func TestMCPTool_Call_HoldsInFlightSlotDuringCall(t *testing.T) {
	cs := setupInMemoryEchoServer(t)
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	t.Cleanup(func() { _ = registry.Close() })
	registry.SetConnectionForTest("test-srv", cs)

	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__test-srv__slow",
		OriginalName: "slow",
		ServerName:   "test-srv",
	}, registry)

	done := make(chan error, 1)
	go func() {
		_, err := tl.Call(context.Background(), json.RawMessage(`{"text":"late"}`), nil)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // REAL-TIME: land inside the slow call's window
	idle, remaining := registry.DrainServer("test-srv", 50*time.Millisecond)
	if idle || remaining != 1 {
		t.Errorf("DrainServer during a running call = (idle=%v, remaining=%d), want (false, 1) — the in-flight slot is not held for the call's duration", idle, remaining)
	}
	if err := <-done; err != nil {
		t.Fatalf("slow call failed: %v", err)
	}
}
