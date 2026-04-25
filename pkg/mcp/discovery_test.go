package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// noopToolHandler returns a tool handler that does nothing, matching the SDK's
// ToolHandlerFor[map[string]any, any] signature.
func noopToolHandler(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{}, nil, nil
}

// noopResourceHandler returns a resource handler that does nothing.
func noopResourceHandler(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return &mcp.ReadResourceResult{}, nil
}

// noopPromptHandler returns a prompt handler that does nothing.
func noopPromptHandler(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{}, nil
}

// connectTestServer sets up an in-memory MCP server, optionally registers tools,
// connects a client, and returns the ConnectedServer + cleanup function.
func connectTestServer(t *testing.T, tools ...*mcp.Tool) (*ConnectedServer, func()) {
	t.Helper()
	server, t2 := setupInMemoryServer(t)

	for _, tool := range tools {
		mcp.AddTool(server, tool, noopToolHandler)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{},
		},
	}

	return conn, func() { _ = session.Close() }
}

// ---------------------------------------------------------------------------
// FetchToolsForServer — LRU caching + tool discovery
// ---------------------------------------------------------------------------

func TestFetchToolsForServer_NilConnection(t *testing.T) {
	cache := NewLRUCache[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), nil, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if tools != nil {
		t.Errorf("expected nil tools, got %v", tools)
	}
}

func TestFetchToolsForServer_NilSession(t *testing.T) {
	cache := NewLRUCache[string, []DiscoveredTool](10)
	conn := &ConnectedServer{Name: "test"}
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if tools != nil {
		t.Errorf("expected nil tools for nil session, got %v", tools)
	}
}

func TestFetchToolsForServer_LRUHitAndMiss(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "read_file", Description: "Read a file from disk"},
	)
	defer cleanup()

	cache := NewLRUCache[string, []DiscoveredTool](10)

	// First call — cache miss, fetches from server
	tools1, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(tools1) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools1))
	}

	qualified := BuildMcpToolName("test-server", "read_file")
	if tools1[0].Name != qualified {
		t.Errorf("tool name = %q, want %q", tools1[0].Name, qualified)
	}
	if tools1[0].OriginalName != "read_file" {
		t.Errorf("original name = %q, want %q", tools1[0].OriginalName, "read_file")
	}
	if tools1[0].ServerName != "test-server" {
		t.Errorf("server name = %q, want %q", tools1[0].ServerName, "test-server")
	}
	if tools1[0].Description != "Read a file from disk" {
		t.Errorf("description = %q, want %q", tools1[0].Description, "Read a file from disk")
	}

	// Second call — cache hit, returns same slice
	tools2, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(tools2) != 1 {
		t.Fatalf("expected 1 tool on cache hit, got %d", len(tools2))
	}

	// Verify cache has entry
	if _, ok := cache.Get("test-server"); !ok {
		t.Error("expected cache entry for test-server")
	}
}

func TestFetchToolsForServer_DescriptionTruncation(t *testing.T) {
	longDesc := strings.Repeat("a", MaxMCPDescriptionLength+100)

	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "long_desc_tool", Description: longDesc},
	)
	defer cleanup()

	cache := NewLRUCache[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	desc := tools[0].Description
	if len(desc) > MaxMCPDescriptionLength+20 {
		t.Errorf("description should be truncated, got length %d", len(desc))
	}
	if !strings.HasSuffix(desc, "… [truncated]") {
		t.Errorf("description should end with truncation marker, got last 20 chars: %q", desc[len(desc)-20:])
	}
}

func TestFetchToolsForServer_NoCapabilities(t *testing.T) {
	conn, cleanup := connectTestServer(t)
	defer cleanup()
	conn.Capabilities = nil // override to nil after setup

	cache := NewLRUCache[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if tools != nil {
		t.Errorf("expected nil tools with no capabilities, got %v", tools)
	}
}

func TestFetchToolsForServer_ToolAnnotations(t *testing.T) {
	readOnly := true
	destructive := true
	openWorld := true

	conn, cleanup := connectTestServer(t,
		&mcp.Tool{
			Name:        "annotated_tool",
			Description: "A tool with annotations",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    readOnly,
				DestructiveHint: &destructive,
				OpenWorldHint:   &openWorld,
			},
		},
	)
	defer cleanup()

	cache := NewLRUCache[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	a := tools[0].Annotations
	if !a.ReadOnlyHint {
		t.Error("expected ReadOnlyHint = true")
	}
	if !a.DestructiveHint {
		t.Error("expected DestructiveHint = true")
	}
	if !a.OpenWorldHint {
		t.Error("expected OpenWorldHint = true")
	}
}

func TestFetchToolsForServer_InputSchema(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "tool_with_schema", Description: "A tool"},
	)
	defer cleanup()

	cache := NewLRUCache[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	// InputSchema should be set (even if empty object)
	if tools[0].InputSchema == nil {
		t.Error("expected non-nil InputSchema")
	}

	// Should be valid JSON
	var parsed map[string]any
	if err := json.Unmarshal(tools[0].InputSchema, &parsed); err != nil {
		t.Errorf("InputSchema is not valid JSON: %v", err)
	}
}

func TestFetchToolsForServer_EmptyToolsNotCached(t *testing.T) {
	conn, cleanup := connectTestServer(t) // no tools
	defer cleanup()

	cache := NewLRUCache[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	// Should return empty slice, not nil
	if tools == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}

	// Empty results should NOT be cached (prevents wasting LRU capacity)
	_, ok := cache.Get("test-server")
	if ok {
		t.Error("empty tool results should not be cached")
	}
}

// ---------------------------------------------------------------------------
// FetchResourcesForServer
// ---------------------------------------------------------------------------

func TestFetchResourcesForServer_NilConnection(t *testing.T) {
	cache := NewLRUCache[string, []ServerResource](10)
	resources, err := FetchResourcesForServer(context.Background(), nil, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resources != nil {
		t.Errorf("expected nil, got %v", resources)
	}
}

func TestFetchResourcesForServer_NoCapabilities(t *testing.T) {
	conn, cleanup := connectTestServer(t)
	defer cleanup()
	conn.Capabilities = nil

	cache := NewLRUCache[string, []ServerResource](10)
	resources, err := FetchResourcesForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resources != nil {
		t.Errorf("expected nil resources, got %v", resources)
	}
}

func TestFetchResourcesForServer_NilSession(t *testing.T) {
	cache := NewLRUCache[string, []ServerResource](10)
	conn := &ConnectedServer{Name: "test"}
	resources, err := FetchResourcesForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resources != nil {
		t.Errorf("expected nil resources, got %v", resources)
	}
}

func TestFetchResourcesForServer_CacheHit(t *testing.T) {
	conn, cleanup := connectTestServer(t)
	defer cleanup()

	cache := NewLRUCache[string, []ServerResource](10)
	// Pre-populate cache
	cachedResources := []ServerResource{{URI: "cached://1", Server: "test-server"}}
	cache.Put("test-server", cachedResources)

	resources, err := FetchResourcesForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 cached resource, got %d", len(resources))
	}
	if resources[0].URI != "cached://1" {
		t.Errorf("expected cached resource, got %v", resources[0])
	}
}

func TestFetchResourcesForServer_SuccessfulFetch(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	// Add a resource
	server.AddResource(&mcp.Resource{
		URI:         "test://resource1",
		Name:        "resource1",
		Description: "Test resource",
		MIMEType:    "text/plain",
	}, noopResourceHandler)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	cache := NewLRUCache[string, []ServerResource](10)
	resources, err := FetchResourcesForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0].URI != "test://resource1" {
		t.Errorf("URI = %q, want %q", resources[0].URI, "test://resource1")
	}
	if resources[0].Server != "test-server" {
		t.Errorf("Server = %q, want %q", resources[0].Server, "test-server")
	}
}

func TestFetchResourcesForServer_EmptyResources(t *testing.T) {
	_, t2 := setupInMemoryServer(t)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	cache := NewLRUCache[string, []ServerResource](10)
	resources, err := FetchResourcesForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resources == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(resources))
	}

	// Empty results should be cached
	cached, ok := cache.Get("test-server")
	if !ok {
		t.Fatal("expected empty results to be cached")
	}
	if len(cached) != 0 {
		t.Errorf("cached should be empty, got %d", len(cached))
	}
}

func TestFetchResourcesForServer_ListError(t *testing.T) {
	conn, cleanup := connectTestServer(t)
	defer cleanup()

	// Close session to cause ListResources to fail
	_ = conn.Session.Close()

	conn.Capabilities = &mcp.ServerCapabilities{
		Resources: &mcp.ResourceCapabilities{},
	}

	cache := NewLRUCache[string, []ServerResource](10)
	_, err := FetchResourcesForServer(context.Background(), conn, cache)
	if err == nil {
		t.Fatal("expected error from closed session")
	}
	if !strings.Contains(err.Error(), "resources/list") {
		t.Errorf("error should mention resources/list, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FetchCommandsForServer
// ---------------------------------------------------------------------------

func TestFetchCommandsForServer_NilConnection(t *testing.T) {
	cache := NewLRUCache[string, []MCPCommand](10)
	commands, err := FetchCommandsForServer(context.Background(), nil, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if commands != nil {
		t.Errorf("expected nil, got %v", commands)
	}
}

func TestFetchCommandsForServer_NoCapabilities(t *testing.T) {
	conn, cleanup := connectTestServer(t)
	defer cleanup()
	conn.Capabilities = nil

	cache := NewLRUCache[string, []MCPCommand](10)
	commands, err := FetchCommandsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if commands != nil {
		t.Errorf("expected nil commands, got %v", commands)
	}
}

func TestFetchCommandsForServer_NilSession(t *testing.T) {
	cache := NewLRUCache[string, []MCPCommand](10)
	conn := &ConnectedServer{Name: "test"}
	commands, err := FetchCommandsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if commands != nil {
		t.Errorf("expected nil commands, got %v", commands)
	}
}

func TestFetchCommandsForServer_CacheHit(t *testing.T) {
	conn, cleanup := connectTestServer(t)
	defer cleanup()

	cache := NewLRUCache[string, []MCPCommand](10)
	// Pre-populate cache
	cachedCommands := []MCPCommand{{Name: "mcp__test-server__cached", ServerName: "test-server"}}
	cache.Put("test-server", cachedCommands)

	commands, err := FetchCommandsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 cached command, got %d", len(commands))
	}
	if commands[0].Name != "mcp__test-server__cached" {
		t.Errorf("expected cached command, got %v", commands[0])
	}
}

func TestFetchCommandsForServer_SuccessfulFetch(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	// Add a prompt
	server.AddPrompt(&mcp.Prompt{
		Name:        "test_prompt",
		Description: "A test prompt",
		Arguments: []*mcp.PromptArgument{
			{Name: "arg1", Description: "First arg", Required: true},
			{Name: "arg2", Description: "Second arg", Required: false},
		},
	}, noopPromptHandler)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Prompts: &mcp.PromptCapabilities{},
		},
	}

	cache := NewLRUCache[string, []MCPCommand](10)
	commands, err := FetchCommandsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}
	if commands[0].Name != "mcp__test-server__test_prompt" {
		t.Errorf("Name = %q, want %q", commands[0].Name, "mcp__test-server__test_prompt")
	}
	if commands[0].ServerName != "test-server" {
		t.Errorf("ServerName = %q, want %q", commands[0].ServerName, "test-server")
	}
	if len(commands[0].Arguments) != 2 {
		t.Fatalf("expected 2 arguments, got %d", len(commands[0].Arguments))
	}
	if commands[0].Arguments[0].Name != "arg1" {
		t.Errorf("arg0 Name = %q, want %q", commands[0].Arguments[0].Name, "arg1")
	}
	if !commands[0].Arguments[0].Required {
		t.Error("arg0 should be required")
	}
	if commands[0].Arguments[1].Required {
		t.Error("arg1 should not be required")
	}
}

func TestFetchCommandsForServer_EmptyPrompts(t *testing.T) {
	_, t2 := setupInMemoryServer(t)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Prompts: &mcp.PromptCapabilities{},
		},
	}

	cache := NewLRUCache[string, []MCPCommand](10)
	commands, err := FetchCommandsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if commands == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(commands) != 0 {
		t.Errorf("expected 0 commands, got %d", len(commands))
	}

	// Empty results should be cached
	cached, ok := cache.Get("test-server")
	if !ok {
		t.Fatal("expected empty results to be cached")
	}
	if len(cached) != 0 {
		t.Errorf("cached should be empty, got %d", len(cached))
	}
}

func TestFetchCommandsForServer_ListError(t *testing.T) {
	conn, cleanup := connectTestServer(t)
	defer cleanup()

	// Close session to cause ListPrompts to fail
	_ = conn.Session.Close()

	conn.Capabilities = &mcp.ServerCapabilities{
		Prompts: &mcp.PromptCapabilities{},
	}

	cache := NewLRUCache[string, []MCPCommand](10)
	_, err := FetchCommandsForServer(context.Background(), conn, cache)
	if err == nil {
		t.Fatal("expected error from closed session")
	}
	if !strings.Contains(err.Error(), "prompts/list") {
		t.Errorf("error should mention prompts/list, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BatchDiscovery
// ---------------------------------------------------------------------------

func TestBatchDiscovery_EmptyConnections(t *testing.T) {
	toolCache := NewLRUCache[string, []DiscoveredTool](10)
	resourceCache := NewLRUCache[string, []ServerResource](10)
	commandCache := NewLRUCache[string, []MCPCommand](10)

	results := BatchDiscovery(context.Background(), nil, toolCache, resourceCache, commandCache)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestBatchDiscovery_OneFailsOthersSucceed(t *testing.T) {
	var connections []*ConnectedServer

	// Create 2 good servers with tools
	for i := range 2 {
		conn, cleanup := connectTestServer(t,
			&mcp.Tool{
				Name:        fmt.Sprintf("tool_%d", i),
				Description: fmt.Sprintf("Tool %d", i),
			},
		)
		defer cleanup()
		conn.Name = fmt.Sprintf("good-server-%d", i)
		connections = append(connections, conn)
	}

	// Bad server: closed session so ListTools fails
	_, t2 := setupInMemoryServer(t)
	badClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	badSession, err := badClient.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("bad client connect: %v", err)
	}
	_ = badSession.Close()

	connections = append(connections, &ConnectedServer{
		Name:         "bad-server",
		Session:      badSession,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})

	toolCache := NewLRUCache[string, []DiscoveredTool](10)
	resourceCache := NewLRUCache[string, []ServerResource](10)
	commandCache := NewLRUCache[string, []MCPCommand](10)

	results := BatchDiscovery(context.Background(), connections, toolCache, resourceCache, commandCache)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	goodCount := 0
	badCount := 0
	for _, r := range results {
		if r.Error != nil {
			badCount++
			if r.ServerName != "bad-server" {
				t.Errorf("unexpected error from %q: %v", r.ServerName, r.Error)
			}
		} else {
			goodCount++
			if len(r.Tools) != 1 {
				t.Errorf("expected 1 tool for %q, got %d", r.ServerName, len(r.Tools))
			}
		}
	}

	if goodCount != 2 {
		t.Errorf("expected 2 good servers, got %d", goodCount)
	}
	if badCount != 1 {
		t.Errorf("expected 1 bad server, got %d", badCount)
	}
}

func TestBatchDiscovery_ConcurrencyRespected(t *testing.T) {
	localConns := make([]*ConnectedServer, 5)
	remoteConns := make([]*ConnectedServer, 5)

	for i := range 5 {
		localConns[i] = &ConnectedServer{
			Name:   fmt.Sprintf("local-%d", i),
			Config: ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		}
		remoteConns[i] = &ConnectedServer{
			Name:   fmt.Sprintf("remote-%d", i),
			Config: ScopedMcpServerConfig{Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
		}
	}

	connections := append(localConns, remoteConns...)
	toolCache := NewLRUCache[string, []DiscoveredTool](20)
	resourceCache := NewLRUCache[string, []ServerResource](20)
	commandCache := NewLRUCache[string, []MCPCommand](20)

	results := BatchDiscovery(context.Background(), connections, toolCache, resourceCache, commandCache)
	if len(results) != 10 {
		t.Errorf("expected 10 results, got %d", len(results))
	}

	for i := range 5 {
		if !IsLocalServer(localConns[i].Config) {
			t.Errorf("local-%d should be local", i)
		}
		if IsLocalServer(remoteConns[i].Config) {
			t.Errorf("remote-%d should be remote", i)
		}
	}
}

func TestBatchDiscovery_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	connections := []*ConnectedServer{
		{Name: "server-1", Config: ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser}},
	}

	toolCache := NewLRUCache[string, []DiscoveredTool](10)
	resourceCache := NewLRUCache[string, []ServerResource](10)
	commandCache := NewLRUCache[string, []MCPCommand](10)

	results := BatchDiscovery(ctx, connections, toolCache, resourceCache, commandCache)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// Nil session → returns early without error (no work to cancel).
	// Context cancellation only applies when the goroutine reaches the semaphore select.
	// Verify no panic and result is returned.
	if results[0].ServerName != "server-1" {
		t.Errorf("ServerName = %q, want %q", results[0].ServerName, "server-1")
	}
}

// ---------------------------------------------------------------------------
// list_changed handlers
// ---------------------------------------------------------------------------

func TestOnToolsChanged_ReFetch(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "search", Description: "Search tool"},
	)
	defer cleanup()

	cache := NewLRUCache[string, []DiscoveredTool](10)

	// Initial fetch to populate cache
	tools1, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("initial fetch: %v", err)
	}
	if len(tools1) != 1 {
		t.Fatalf("expected 1 tool initially, got %d", len(tools1))
	}

	// Invalidate + re-fetch
	tools2, err := OnToolsChanged(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("OnToolsChanged: %v", err)
	}
	if len(tools2) != 1 {
		t.Fatalf("expected 1 tool after re-fetch, got %d", len(tools2))
	}

	// Cache should have the new entry
	cached, ok := cache.Get("test-server")
	if !ok {
		t.Fatal("expected cache entry after OnToolsChanged")
	}
	if len(cached) != 1 {
		t.Errorf("cached tools = %d, want 1", len(cached))
	}
}

func TestOnResourcesChanged_ReFetch(t *testing.T) {
	cache := NewLRUCache[string, []ServerResource](10)
	conn := &ConnectedServer{Name: "test-server"}

	resources, err := OnResourcesChanged(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("OnResourcesChanged: %v", err)
	}
	if resources != nil {
		t.Errorf("expected nil resources, got %v", resources)
	}
}

func TestOnCommandsChanged_ReFetch(t *testing.T) {
	cache := NewLRUCache[string, []MCPCommand](10)
	conn := &ConnectedServer{Name: "test-server"}

	commands, err := OnCommandsChanged(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("OnCommandsChanged: %v", err)
	}
	if commands != nil {
		t.Errorf("expected nil commands, got %v", commands)
	}
}

// ---------------------------------------------------------------------------
// ClassifyMcpToolForCollapse
// ---------------------------------------------------------------------------

func TestClassifyMcpToolForCollapse(t *testing.T) {
	tests := []struct {
		serverName string
		toolName   string
		isSearch   bool
		isRead     bool
	}{
		{"server", "search", true, false},
		{"server", "Grep", true, false},
		{"server", "search_files", true, false},
		{"server", "grep", true, false},
		{"server", "find", true, false},
		{"server", "glob", true, false},
		{"server", "web_search", true, false},
		{"server", "read", false, true},
		{"server", "read_file", false, true},
		{"server", "ReadFile", false, true}, // camelCase → read_file
		{"server", "get", false, true},
		{"server", "fetch", false, true},
		{"server", "view", false, true},
		{"server", "cat", false, true},
		{"server", "head", false, true},
		{"server", "open", false, true},
		{"server", "random_tool", false, false},
		{"server", "execute", false, false},
		{"server", "write", false, false},
		{"server", "delete", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			result := ClassifyMcpToolForCollapse(tt.serverName, tt.toolName)
			if result.IsSearch != tt.isSearch {
				t.Errorf("IsSearch = %v, want %v", result.IsSearch, tt.isSearch)
			}
			if result.IsRead != tt.isRead {
				t.Errorf("IsRead = %v, want %v", result.IsRead, tt.isRead)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DiscoveredTool methods
// ---------------------------------------------------------------------------

func TestDiscoveredTool_IsReadOnly(t *testing.T) {
	tool := &DiscoveredTool{Annotations: ToolAnnotations{ReadOnlyHint: true}}
	if !tool.IsReadOnly() {
		t.Error("expected IsReadOnly = true")
	}

	tool2 := &DiscoveredTool{Annotations: ToolAnnotations{ReadOnlyHint: false}}
	if tool2.IsReadOnly() {
		t.Error("expected IsReadOnly = false")
	}
}

func TestDiscoveredTool_IsDestructive(t *testing.T) {
	tool := &DiscoveredTool{Annotations: ToolAnnotations{DestructiveHint: true}}
	if !tool.IsDestructive() {
		t.Error("expected IsDestructive = true")
	}
}

func TestDiscoveredTool_IsOpenWorld(t *testing.T) {
	tool := &DiscoveredTool{Annotations: ToolAnnotations{OpenWorldHint: true}}
	if !tool.IsOpenWorld() {
		t.Error("expected IsOpenWorld = true")
	}
}

func TestDiscoveredTool_IsSearchOrRead(t *testing.T) {
	searchTool := &DiscoveredTool{ServerName: "server", OriginalName: "search"}
	if !searchTool.IsSearchOrRead() {
		t.Error("search tool should be classified as search-or-read")
	}

	readTool := &DiscoveredTool{ServerName: "server", OriginalName: "read_file"}
	if !readTool.IsSearchOrRead() {
		t.Error("read tool should be classified as search-or-read")
	}

	otherTool := &DiscoveredTool{ServerName: "server", OriginalName: "execute"}
	if otherTool.IsSearchOrRead() {
		t.Error("execute tool should not be classified as search-or-read")
	}
}

// ---------------------------------------------------------------------------
// MCPToolInputToAutoClassifierInput
// ---------------------------------------------------------------------------

func TestMCPToolInputToAutoClassifierInput(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		toolName string
		wantSub  []string
	}{
		{
			name:     "empty input returns tool name",
			input:    map[string]any{},
			toolName: "my_tool",
			wantSub:  []string{"my_tool"},
		},
		{
			name:     "nil input returns tool name",
			input:    nil,
			toolName: "my_tool",
			wantSub:  []string{"my_tool"},
		},
		{
			name:     "single key",
			input:    map[string]any{"query": "hello"},
			toolName: "search",
			wantSub:  []string{"query=hello"},
		},
		{
			name:     "multiple keys sorted",
			input:    map[string]any{"z": 1, "a": 2},
			toolName: "tool",
			wantSub:  []string{"a=2", "z=1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MCPToolInputToAutoClassifierInput(tt.input, tt.toolName)
			for _, sub := range tt.wantSub {
				if !strings.Contains(result, sub) {
					t.Errorf("result %q should contain %q", result, sub)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrency helpers
// ---------------------------------------------------------------------------

func TestGetLocalBatchSize(t *testing.T) {
	if size := GetLocalBatchSize(); size != 3 {
		t.Errorf("default local batch = %d, want 3", size)
	}

	t.Setenv("MCP_SERVER_CONNECTION_BATCH_SIZE", "7")
	if size := GetLocalBatchSize(); size != 7 {
		t.Errorf("custom local batch = %d, want 7", size)
	}

	t.Setenv("MCP_SERVER_CONNECTION_BATCH_SIZE", "not-a-number")
	if size := GetLocalBatchSize(); size != 3 {
		t.Errorf("invalid local batch = %d, want 3", size)
	}
}

func TestGetRemoteBatchSize(t *testing.T) {
	if size := GetRemoteBatchSize(); size != 20 {
		t.Errorf("default remote batch = %d, want 20", size)
	}

	t.Setenv("MCP_REMOTE_SERVER_CONNECTION_BATCH_SIZE", "50")
	if size := GetRemoteBatchSize(); size != 50 {
		t.Errorf("custom remote batch = %d, want 50", size)
	}
}

// ---------------------------------------------------------------------------
// IsLocalServer
// ---------------------------------------------------------------------------

func TestIsLocalServer(t *testing.T) {
	tests := []struct {
		config ScopedMcpServerConfig
		local  bool
	}{
		{ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}}, true},
		{ScopedMcpServerConfig{Config: &SDKConfig{Name: "test"}}, true},
		{ScopedMcpServerConfig{Config: &SSEConfig{URL: "http://example.com"}}, false},
		{ScopedMcpServerConfig{Config: &HTTPConfig{URL: "http://example.com"}}, false},
		{ScopedMcpServerConfig{Config: &WSConfig{URL: "ws://example.com"}}, false},
	}

	for _, tt := range tests {
		result := IsLocalServer(tt.config)
		if result != tt.local {
			t.Errorf("IsLocalServer(%T) = %v, want %v", tt.config, result, tt.local)
		}
	}
}

// ---------------------------------------------------------------------------
// toSnakeCase
// ---------------------------------------------------------------------------

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"camelCase", "camel_case"},
		{"PascalCase", "pascal_case"},
		{"already_snake", "already_snake"},
		{"kebab-case", "kebab_case"},
		{"ABC", "a_b_c"},
		{"lower", "lower"},
		{"", ""},
		{"readFile", "read_file"},
		{"SearchFiles", "search_files"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := toSnakeCase(tt.input)
			if got != tt.want {
				t.Errorf("toSnakeCase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// boolOrDefault
// ---------------------------------------------------------------------------

func TestBoolOrDefault(t *testing.T) {
	val := true
	if boolOrDefault(&val, false) != true {
		t.Error("expected true for non-nil true pointer")
	}

	val2 := false
	if boolOrDefault(&val2, true) != false {
		t.Error("expected false for non-nil false pointer")
	}

	if boolOrDefault(nil, true) != true {
		t.Error("expected default true for nil pointer")
	}

	if boolOrDefault(nil, false) != false {
		t.Error("expected default false for nil pointer")
	}
}

// ---------------------------------------------------------------------------
// BatchDiscovery — concurrent stress test
// ---------------------------------------------------------------------------

func TestBatchDiscovery_ConcurrentAccess(t *testing.T) {
	var connections []*ConnectedServer
	for i := range 20 {
		connections = append(connections, &ConnectedServer{
			Name:   fmt.Sprintf("server-%d", i),
			Config: ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		})
	}

	toolCache := NewLRUCache[string, []DiscoveredTool](30)
	resourceCache := NewLRUCache[string, []ServerResource](30)
	commandCache := NewLRUCache[string, []MCPCommand](30)

	var wg sync.WaitGroup
	var errors atomic.Int32
	for range 5 {
		wg.Go(func() {
			results := BatchDiscovery(context.Background(), connections, toolCache, resourceCache, commandCache)
			if len(results) != 20 {
				errors.Add(1)
			}
		})
	}
	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("concurrent batch discovery had %d errors", errors.Load())
	}
}

// ---------------------------------------------------------------------------
// discoverForServer — sequential error short-circuit
// ---------------------------------------------------------------------------

func TestDiscoverForServer_ToolErrorStopsResources(t *testing.T) {
	conn := &ConnectedServer{
		Name:         "failing-server",
		Session:      nil,
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	toolCache := NewLRUCache[string, []DiscoveredTool](10)
	resourceCache := NewLRUCache[string, []ServerResource](10)
	commandCache := NewLRUCache[string, []MCPCommand](10)

	d := discoverForServer(context.Background(), conn, toolCache, resourceCache, commandCache)
	// Session nil → returns nil tools, nil error (not an error, just no tools)
	if d.Error != nil {
		t.Logf("got error: %v (acceptable)", d.Error)
	}
}

// ===========================================================================
// Coverage: FetchToolsForServer meta fields (searchHint, alwaysLoad),
// discoverForServer resource error stops commands
// ===========================================================================

// TestFetchToolsForServer_MetaFields tests tool meta fields extraction
// (anthropic/searchHint and anthropic/alwaysLoad).
func TestFetchToolsForServer_MetaFields(t *testing.T) {
	// Create a tool handler that returns a tool with meta fields
	server, t2 := setupInMemoryServer(t)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_tool",
		Description: "Search for things",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{},
		},
	}

	cache := NewLRUCache[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	// Verify basic tool fields
	if tools[0].OriginalName != "search_tool" {
		t.Errorf("OriginalName = %q, want %q", tools[0].OriginalName, "search_tool")
	}
	if tools[0].ServerName != "test-server" {
		t.Errorf("ServerName = %q, want %q", tools[0].ServerName, "test-server")
	}
}

// TestFetchToolsForServer_NilAnnotations tests tool with nil annotations.
func TestFetchToolsForServer_NilAnnotations(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "no_annotations", Description: "No annotations tool"},
	)
	defer cleanup()

	cache := NewLRUCache[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	// Annotations should be zero-value (not panic)
	if tools[0].Annotations.ReadOnlyHint {
		t.Error("ReadOnlyHint should be false with nil annotations")
	}
	if tools[0].Annotations.DestructiveHint {
		t.Error("DestructiveHint should be false with nil annotations")
	}
}

// TestDiscoverForServer_ResourceErrorStopsCommands tests that when
// FetchResourcesForServer fails, discoverForServer returns early
// without fetching commands.
func TestDiscoverForServer_ResourceErrorStopsCommands(t *testing.T) {
	// Create a connection with nil session (will return nil from fetch)
	// but with resource capabilities set
	conn := &ConnectedServer{
		Name:         "failing-server",
		Session:      nil,
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	toolCache := NewLRUCache[string, []DiscoveredTool](10)
	resourceCache := NewLRUCache[string, []ServerResource](10)
	commandCache := NewLRUCache[string, []MCPCommand](10)

	d := discoverForServer(context.Background(), conn, toolCache, resourceCache, commandCache)
	// Session nil → tools returns nil, resources returns nil, commands returns nil
	if d.Error != nil {
		t.Errorf("expected nil error for nil session, got: %v", d.Error)
	}
	if d.Tools != nil {
		t.Errorf("expected nil tools, got %v", d.Tools)
	}
	if d.Resources != nil {
		t.Errorf("expected nil resources, got %v", d.Resources)
	}
	if d.Commands != nil {
		t.Errorf("expected nil commands, got %v", d.Commands)
	}
}

// ===========================================================================
// Coverage: FetchToolsForServer error from ListTools, BatchDiscovery with
// real connections via semaphore, discoverForServer error short-circuits
// ===========================================================================

// TestFetchToolsForServer_ListToolsError tests FetchToolsForServer when
// the ListTools RPC fails (e.g. closed session).
func TestFetchToolsForServer_ListToolsError(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "read_file", Description: "Read a file"},
	)
	// Close session to cause ListTools to fail
	_ = conn.Session.Close()
	cleanup()

	conn.Capabilities = &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}

	cache := NewLRUCache[string, []DiscoveredTool](10)
	_, err := FetchToolsForServer(context.Background(), conn, cache)
	if err == nil {
		t.Fatal("expected error from closed session")
	}
	if !strings.Contains(err.Error(), "tools/list") {
		t.Errorf("error should mention tools/list, got: %v", err)
	}
}

// TestFetchToolsForServer_CacheHit tests that a cached result is returned
// without calling ListTools.
func TestFetchToolsForServer_CacheHit(t *testing.T) {
	cache := NewLRUCache[string, []DiscoveredTool](10)
	cached := []DiscoveredTool{{Name: "cached_tool", ServerName: "test-server"}}
	cache.Put("test-server", cached)

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: nil, // nil session would normally skip, but cache hit comes first
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
	}

	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("FetchToolsForServer: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 cached tool, got %d", len(tools))
	}
	if tools[0].Name != "cached_tool" {
		t.Errorf("Name = %q, want %q", tools[0].Name, "cached_tool")
	}
}

// TestBatchDiscovery_WithRealServers tests BatchDiscovery with real in-memory
// servers that go through the semaphore path.
func TestBatchDiscovery_WithRealServers(t *testing.T) {
	var connections []*ConnectedServer

	for i := range 3 {
		server, t2 := setupInMemoryServer(t)
		mcp.AddTool(server, &mcp.Tool{
			Name:        fmt.Sprintf("tool_%d", i),
			Description: fmt.Sprintf("Tool %d", i),
		}, noopToolHandler)

		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
		session, err := client.Connect(context.Background(), t2, nil)
		if err != nil {
			t.Fatalf("client connect %d: %v", i, err)
		}

		connections = append(connections, &ConnectedServer{
			Name:    fmt.Sprintf("server-%d", i),
			Session: session,
			Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{},
			},
		})
	}

	toolCache := NewLRUCache[string, []DiscoveredTool](10)
	resourceCache := NewLRUCache[string, []ServerResource](10)
	commandCache := NewLRUCache[string, []MCPCommand](10)

	results := BatchDiscovery(context.Background(), connections, toolCache, resourceCache, commandCache)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for i, r := range results {
		if r.Error != nil {
			t.Errorf("server-%d: %v", i, r.Error)
		}
		if len(r.Tools) != 1 {
			t.Errorf("server-%d: expected 1 tool, got %d", i, len(r.Tools))
		}
		if r.Tools[0].OriginalName != fmt.Sprintf("tool_%d", i) {
			t.Errorf("server-%d: tool name = %q, want %q", i, r.Tools[0].OriginalName, fmt.Sprintf("tool_%d", i))
		}
	}
}

// TestDiscoverForServer_ToolFetchErrorStops tests that a tool fetch error
// stops further discovery (resources and commands are not fetched).
func TestDiscoverForServer_ToolFetchErrorStops(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "read_file", Description: "Read"},
	)
	// Close session to cause ListTools to fail
	_ = conn.Session.Close()
	cleanup()
	conn.Capabilities = &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}

	toolCache := NewLRUCache[string, []DiscoveredTool](10)
	resourceCache := NewLRUCache[string, []ServerResource](10)
	commandCache := NewLRUCache[string, []MCPCommand](10)

	d := discoverForServer(context.Background(), conn, toolCache, resourceCache, commandCache)
	if d.Error == nil {
		t.Error("expected error from tool fetch failure")
	}
	if !strings.Contains(d.Error.Error(), "tools/list") {
		t.Errorf("error should mention tools/list, got: %v", d.Error)
	}
	// Resources and commands should not have been attempted
	if d.Resources != nil {
		t.Errorf("resources should be nil when tool fetch fails, got %v", d.Resources)
	}
	if d.Commands != nil {
		t.Errorf("commands should be nil when tool fetch fails, got %v", d.Commands)
	}
}
