package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Integration tests for MCP resource tools
//
// Call chain: Engine.refreshTools → Registry.HasResourceSupport → tool registration
//             → tool.Call → Registry → MCP server → result → tool.RenderResult
//
// These tests verify the full assembly works, not just individual functions.
// ---------------------------------------------------------------------------

// setupResourceServer creates an in-memory MCP server with resources support,
// a connected client session, and a configured Registry.
func setupResourceServer(t *testing.T) (*mcpsdk.Server, *mcp.Registry) {
	t.Helper()

	t1, t2 := mcpsdk.NewInMemoryTransports()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "resource-server",
		Version: "1.0.0",
	}, nil)

	type serverResult struct {
		session *mcpsdk.ServerSession
		err     error
	}
	ch := make(chan serverResult, 1)
	go func() {
		s, err := server.Connect(context.Background(), t1, nil)
		ch <- serverResult{s, err}
	}()

	server.AddResource(&mcpsdk.Resource{
		URI:      "test://hello",
		Name:     "hello",
		MIMEType: "text/plain",
	}, func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return &mcpsdk.ReadResourceResult{
			Contents: []*mcpsdk.ResourceContents{
				{URI: "test://hello", MIMEType: "text/plain", Text: "Hello from MCP!"},
			},
		}, nil
	})

	server.AddResource(&mcpsdk.Resource{
		URI:      "test://data.bin",
		Name:     "data",
		MIMEType: "application/octet-stream",
	}, func(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return &mcpsdk.ReadResourceResult{
			Contents: []*mcpsdk.ResourceContents{
				{URI: "test://data.bin", MIMEType: "application/octet-stream", Blob: []byte("binary-data")},
			},
		}, nil
	})

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
	default:
		t.Fatal("server connect did not complete")
	}

	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Close()
	})

	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	reg.SetConnectionForTest("resource-server", &mcp.ConnectedServer{
		Name:    "resource-server",
		Session: session,
		Config:  mcp.ScopedMcpServerConfig{Config: &mcp.StdioConfig{Command: "test"}, Scope: mcp.ScopeUser},
		Capabilities: &mcpsdk.ServerCapabilities{
			Resources: &mcpsdk.ResourceCapabilities{},
		},
	})

	return server, reg
}

// newEngineWithRegistry creates an Engine with the given MCP registry.
func newEngineWithRegistry(reg *mcp.Registry) *Engine {
	return New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{}
		},
		MCPRegistry: reg,
	})
}

// ---------------------------------------------------------------------------
// Cold start: No MCP registry → resource tools not registered
// ---------------------------------------------------------------------------

func TestResourceTools_ColdStart_NoRegistry(t *testing.T) {
	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{}
		},
		MCPRegistry: nil, // no registry
	})
	t.Cleanup(func() { eng.Close() })

	eng.refreshTools()

	tools := eng.Tools()
	for name := range tools {
		if name == "ListMcpResources" || name == "ReadMcpResource" {
			t.Errorf("resource tools should NOT be registered without registry, found %q", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Cold start: Registry exists but no server supports resources
// ---------------------------------------------------------------------------

func TestResourceTools_ColdStart_NoResourceSupport(t *testing.T) {
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	reg.SetConnectionForTest("no-resources-server", &mcp.ConnectedServer{
		Name:         "no-resources-server",
		Session:      nil,
		Config:       mcp.ScopedMcpServerConfig{Config: &mcp.StdioConfig{Command: "test"}, Scope: mcp.ScopeUser},
		Capabilities: &mcpsdk.ServerCapabilities{}, // no Resources
	})

	eng := newEngineWithRegistry(reg)
	eng.refreshTools()

	tools := eng.Tools()
	for name := range tools {
		if name == "ListMcpResources" || name == "ReadMcpResource" {
			t.Errorf("resource tools should NOT be registered without resource support, found %q", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Hot path: Full chain — Engine → refreshTools → tool registered → Call → RenderResult
// ---------------------------------------------------------------------------

func TestResourceTools_HotPath_ListAndReadResources(t *testing.T) {
	_, reg := setupResourceServer(t)
	eng := newEngineWithRegistry(reg)
	eng.refreshTools()

	tools := eng.Tools()
	var listTool, readTool tool.Tool
	for name, t := range tools {
		if name == "ListMcpResources" {
			listTool = t
		}
		if name == "ReadMcpResource" {
			readTool = t
		}
	}
	if listTool == nil {
		t.Fatal("ListMcpResources not registered")
	}
	if readTool == nil {
		t.Fatal("ReadMcpResource not registered")
	}

	// Chain step 1: List resources with server filter → verify the real MCP server responds
	listResult, err := listTool.Call(context.Background(), json.RawMessage(`{"server": "resource-server"}`), nil)
	if err != nil {
		t.Fatalf("ListMcpResources.Call error: %v", err)
	}
	rendered := listTool.RenderResult(listResult.Data)
	if !strings.Contains(rendered, "test://hello") {
		t.Errorf("list should contain test://hello, got: %q", rendered)
	}
	if !strings.Contains(rendered, "test://data.bin") {
		t.Errorf("list should contain test://data.bin, got: %q", rendered)
	}

	// Chain step 2: Read a text resource → verify content comes through
	readInput := json.RawMessage(`{"server": "resource-server", "uri": "test://hello"}`)
	readResult, err := readTool.Call(context.Background(), readInput, nil)
	if err != nil {
		t.Fatalf("ReadMcpResource.Call error: %v", err)
	}
	readRendered := readTool.RenderResult(readResult.Data)
	if !strings.Contains(readRendered, "Hello from MCP!") {
		t.Errorf("read should contain text content, got: %q", readRendered)
	}
	if !strings.Contains(readRendered, "test://hello") {
		t.Errorf("read should contain URI, got: %q", readRendered)
	}
}

// ---------------------------------------------------------------------------
// Idempotency: List resources twice → same result
// ---------------------------------------------------------------------------

func TestResourceTools_IdempotentListResources(t *testing.T) {
	_, reg := setupResourceServer(t)
	eng := newEngineWithRegistry(reg)
	eng.refreshTools()

	var listTool tool.Tool
	for name, t := range eng.Tools() {
		if name == "ListMcpResources" {
			listTool = t
			break
		}
	}
	if listTool == nil {
		t.Fatal("ListMcpResources not found")
	}

	// First call — cache miss, fetches from server
	result1, err := listTool.Call(context.Background(), json.RawMessage(`{"server": "resource-server"}`), nil)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	rendered1 := listTool.RenderResult(result1.Data)

	// Second call — cache hit, returns same data
	result2, err := listTool.Call(context.Background(), json.RawMessage(`{"server": "resource-server"}`), nil)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	rendered2 := listTool.RenderResult(result2.Data)

	// Both should produce identical results
	if rendered1 != rendered2 {
		t.Errorf("idempotent calls should produce same result:\n  first:  %s\n  second: %s", rendered1, rendered2)
	}
}

// ---------------------------------------------------------------------------
// Recovery: refreshTools rebuilds after registry state change
// ---------------------------------------------------------------------------

func TestResourceTools_Recovery_RefreshRebuilds(t *testing.T) {
	reg := mcp.NewRegistry(mcp.NewClientManager(nil, false, ""), mcp.ChangeCallbacks{})
	eng := newEngineWithRegistry(reg)

	// Phase 1: No resource support → no tools
	eng.refreshTools()
	if toolExists(eng, "ListMcpResources") {
		t.Error("should not have ListMcpResources initially")
	}

	// Phase 2: Add a server with resource support
	t1, t2 := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "new-server", Version: "1.0"}, nil)

	type srvResult struct {
		s *mcpsdk.ServerSession
		e error
	}
	srvCh := make(chan srvResult, 1)
	go func() {
		s, err := server.Connect(context.Background(), t1, nil)
		srvCh <- srvResult{s, err}
	}()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	// Wait for server and clean up both sessions.
	select {
	case r := <-srvCh:
		if r.e != nil {
			t.Fatalf("server connect: %v", r.e)
		}
		t.Cleanup(func() {
			_ = session.Close()
			_ = r.s.Close()
		})
	default:
		t.Fatal("server connect did not complete")
	}

	reg.SetConnectionForTest("new-server", &mcp.ConnectedServer{
		Name:    "new-server",
		Session: session,
		Config:  mcp.ScopedMcpServerConfig{Config: &mcp.StdioConfig{Command: "test"}, Scope: mcp.ScopeUser},
		Capabilities: &mcpsdk.ServerCapabilities{
			Resources: &mcpsdk.ResourceCapabilities{},
		},
	})

	// Refresh → tools should appear
	eng.refreshTools()
	if !toolExists(eng, "ListMcpResources") {
		t.Error("should have ListMcpResources after adding resource server")
	}
	if !toolExists(eng, "ReadMcpResource") {
		t.Error("should have ReadMcpResource after adding resource server")
	}
}

// ---------------------------------------------------------------------------
// Name collision guard: MCP server provides same-named tool → not overwritten
// ---------------------------------------------------------------------------

func TestResourceTools_NameCollisionGuard(t *testing.T) {
	_, reg := setupResourceServer(t)

	// Pre-register a tool with the same name using BuildTool
	collisionTool := tool.BuildTool(tool.ToolDef{
		Name_:        "ListMcpResources",
		Call_:        func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) { return nil, nil },
		InputSchema_: func() json.RawMessage { return json.RawMessage(`{}`) },
		Description_: func(json.RawMessage) (string, error) { return "", nil },
	})

	eng := New(&Params{
		Provider: &testProvider{},
		Model:    "test",
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				"ListMcpResources": collisionTool,
			}
		},
		MCPRegistry: reg,
	})
	t.Cleanup(func() { eng.Close() })

	eng.refreshTools()

	// The original tool should NOT be overwritten
	found := eng.Tools()
	tt := findToolByName(found, "ListMcpResources")
	if tt == nil {
		t.Fatal("ListMcpResources should exist")
	}
	// Our collision tool has empty description, MCP resource tool has non-empty
	desc, _ := tt.Description(nil)
	if desc != "" {
		t.Error("collision tool should have empty description, but got non-empty — tool was overwritten!")
	}

	// ReadMcpResource should still be registered (no collision)
	if !toolExists(eng, "ReadMcpResource") {
		t.Error("ReadMcpResource should still be registered when only ListMcpResources collides")
	}
}

// ---------------------------------------------------------------------------
// Binary resource: Read → verify blob persisted and message rendered
// ---------------------------------------------------------------------------

func TestResourceTools_BinaryResourcePersisted(t *testing.T) {
	_, reg := setupResourceServer(t)
	eng := newEngineWithRegistry(reg)
	eng.refreshTools()

	var readTool tool.Tool
	for name, t := range eng.Tools() {
		if name == "ReadMcpResource" {
			readTool = t
			break
		}
	}
	if readTool == nil {
		t.Fatal("ReadMcpResource not found")
	}

	input := json.RawMessage(`{"server": "resource-server", "uri": "test://data.bin"}`)
	result, err := readTool.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}

	rendered := readTool.RenderResult(result.Data)
	// Binary blob should be persisted and message should indicate where
	if !strings.Contains(rendered, "Binary content") {
		t.Errorf("should indicate binary content was saved, got: %q", rendered)
	}
	if !strings.Contains(rendered, "saved to") {
		t.Errorf("should indicate save location, got: %q", rendered)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func toolExists(eng *Engine, name string) bool {
	for n := range eng.Tools() {
		if n == name {
			return true
		}
	}
	return false
}

func findToolByName(tools map[string]tool.Tool, name string) tool.Tool {
	for n, t := range tools {
		if n == name {
			return t
		}
	}
	return nil
}
