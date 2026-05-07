package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	gbotmcp "github.com/liuy/gbot/pkg/mcp"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// ListMcpResources adapter tests
// ---------------------------------------------------------------------------

func TestListMcpResourcesAdapter_Call(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	reg.SetConnectionForTest("server-a", &gbotmcp.ConnectedServer{
		Name:    "server-a",
		Session: nil,
		Config:  gbotmcp.ScopedMcpServerConfig{Config: &gbotmcp.StdioConfig{Command: "test"}, Scope: gbotmcp.ScopeUser},
	})
	reg.SetConnectionForTest("server-b", &gbotmcp.ConnectedServer{
		Name:    "server-b",
		Session: nil,
		Config:  gbotmcp.ScopedMcpServerConfig{Config: &gbotmcp.StdioConfig{Command: "test"}, Scope: gbotmcp.ScopeUser},
	})

	tt := NewListMcpResources(reg)

	if tt.Name() != "ListMcpResources" {
		t.Fatalf("Name() = %q, want %q", tt.Name(), "ListMcpResources")
	}
	if !tt.IsReadOnly(nil) {
		t.Error("expected IsReadOnly = true")
	}
	if tt.IsDestructive(nil) {
		t.Error("expected IsDestructive = false")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("expected IsConcurrencySafe = true")
	}
	if !tt.IsEnabled() {
		t.Error("expected IsEnabled = true")
	}

	// Call with empty input — returns all resources (pre-populated in cache)
	result, err := tt.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	// Empty cache → empty resources
	rendered := tt.RenderResult(result.Data)
	if !strings.Contains(rendered, "No resources found") {
		t.Errorf("empty result should contain friendly message, got: %q", rendered)
	}
}

func TestListMcpResourcesAdapter_FilterByServer(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	reg.SetConnectionForTest("server-a", &gbotmcp.ConnectedServer{
		Name:    "server-a",
		Session: nil,
		Config:  gbotmcp.ScopedMcpServerConfig{Config: &gbotmcp.StdioConfig{Command: "test"}, Scope: gbotmcp.ScopeUser},
	})

	tt := NewListMcpResources(reg)

	// Filter to a specific server
	input := json.RawMessage(`{"server": "server-a"}`)
	result, err := tt.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	// Empty resources from nil session → renders friendly message
	rendered := tt.RenderResult(result.Data)
	if !strings.Contains(rendered, "No resources found") {
		t.Errorf("empty filtered result should contain friendly message, got: %q", rendered)
	}
}

func TestListMcpResourcesAdapter_ServerNotFound(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})

	tt := NewListMcpResources(reg)
	input := json.RawMessage(`{"server": "nonexistent"}`)
	_, err := tt.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent server")
	}
	if !strings.Contains(err.Error(), `server "nonexistent" not found`) {
		t.Errorf("error = %q, want server not found", err.Error())
	}
}

func TestListMcpResourcesAdapter_InvalidJSON(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	_, err := tt.Call(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("error = %q, want 'invalid input'", err.Error())
	}
}

func TestListMcpResourcesAdapter_EmptyInput(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	result, err := tt.Call(context.Background(), json.RawMessage(``), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty input → returns all resources (empty)
	rendered := tt.RenderResult(result.Data)
	if !strings.Contains(rendered, "No resources found") {
		t.Errorf("empty input should work, got: %q", rendered)
	}
}

func TestListMcpResourcesAdapter_WithCachedResources(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	reg.SetConnectionForTest("s1", &gbotmcp.ConnectedServer{
		Name:    "s1",
		Session: nil,
		Config:  gbotmcp.ScopedMcpServerConfig{Config: &gbotmcp.StdioConfig{Command: "test"}, Scope: gbotmcp.ScopeUser},
	})

	// Pre-populate cache
	reg.PutResourceCacheForTest("s1", []gbotmcp.ServerResource{
		{URI: "test://1", Name: "res1", Server: "s1"},
	})

	tt := NewListMcpResources(reg)
	result, err := tt.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	rendered := tt.RenderResult(result.Data)
	if !strings.Contains(rendered, "test://1") {
		t.Errorf("should contain resource URI, got: %q", rendered)
	}
	if !strings.Contains(rendered, "s1") {
		t.Errorf("should contain server name, got: %q", rendered)
	}
}

func TestListMcpResourcesAdapter_DescriptionAndPrompt(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	desc, err := tt.Description(nil)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "List available resources from configured MCP servers" {
		t.Errorf("Description with nil input should return generic description, got: %q", desc)
	}

	schema := tt.InputSchema()
	if !strings.Contains(string(schema), "server") {
		t.Errorf("InputSchema should contain 'server' field, got: %s", schema)
	}
}

// ---------------------------------------------------------------------------
// ReadMcpResource adapter tests
// ---------------------------------------------------------------------------

func TestReadMcpResourceAdapter_Call(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	reg.SetConnectionForTest("s1", &gbotmcp.ConnectedServer{
		Name:         "s1",
		Session:      nil, // no real session — will error on missing server
		Config:       gbotmcp.ScopedMcpServerConfig{Config: &gbotmcp.StdioConfig{Command: "test"}, Scope: gbotmcp.ScopeUser},
		Capabilities: nil,
	})

	tt := NewReadMcpResource(reg)

	if tt.Name() != "ReadMcpResource" {
		t.Fatalf("Name() = %q, want %q", tt.Name(), "ReadMcpResource")
	}
	if !tt.IsReadOnly(nil) {
		t.Error("expected IsReadOnly = true")
	}
	if tt.IsDestructive(nil) {
		t.Error("expected IsDestructive = false")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("expected IsConcurrencySafe = true")
	}
	if !tt.IsEnabled() {
		t.Error("expected IsEnabled = true")
	}
}

func TestReadMcpResourceAdapter_MissingRequiredParams(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	// Missing server
	_, err := tt.Call(context.Background(), json.RawMessage(`{"uri": "test://x"}`), nil)
	if err == nil {
		t.Fatal("expected error for missing server")
	}
	if !strings.Contains(err.Error(), "server is required") {
		t.Errorf("error = %q, want 'server is required'", err.Error())
	}

	// Missing uri
	_, err = tt.Call(context.Background(), json.RawMessage(`{"server": "s1"}`), nil)
	if err == nil {
		t.Fatal("expected error for missing uri")
	}
	if !strings.Contains(err.Error(), "uri is required") {
		t.Errorf("error = %q, want 'uri is required'", err.Error())
	}

	// Empty input (len(input) == 0 branch)
	_, err = tt.Call(context.Background(), json.RawMessage(``), nil)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if !strings.Contains(err.Error(), "server is required") {
		t.Errorf("error = %q, want 'server is required'", err.Error())
	}

	// Invalid JSON input (json.Unmarshal error branch)
	_, err = tt.Call(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("error = %q, want 'invalid input'", err.Error())
	}
}

func TestReadMcpResourceAdapter_ServerNotFound(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	input := json.RawMessage(`{"server": "nonexistent", "uri": "test://x"}`)
	_, err := tt.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent server")
	}
	if !strings.Contains(err.Error(), `server "nonexistent" not found`) {
		t.Errorf("error = %q, want server not found", err.Error())
	}
}

func TestReadMcpResourceAdapter_DescriptionAndSchema(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	desc, err := tt.Description(nil)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Read a resource from an MCP server" {
		t.Errorf("Description with nil input should return generic description, got: %q", desc)
	}

	schema := tt.InputSchema()
	var schemaMap map[string]any
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	props, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema should have 'properties'")
	}
	if _, ok := props["server"]; !ok {
		t.Error("InputSchema should have 'server' property")
	}
	if _, ok := props["uri"]; !ok {
		t.Error("InputSchema should have 'uri' property")
	}

	// Verify required fields
	required, ok := schemaMap["required"].([]any)
	if !ok {
		t.Fatal("InputSchema should have 'required' array")
	}
	requiredStrs := make([]string, 0, len(required))
	for _, r := range required {
		requiredStrs = append(requiredStrs, r.(string))
	}
	if !slices.Contains(requiredStrs, "server") || !slices.Contains(requiredStrs, "uri") {
		t.Errorf("required should contain 'server' and 'uri', got: %v", requiredStrs)
	}
}

// ---------------------------------------------------------------------------
// renderResourceResult tests (via Tool interface)
// ---------------------------------------------------------------------------

func TestRenderResourceResult_EmptyListMessage(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	// Call with no connections → empty resources
	result, err := tt.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	rendered := tt.RenderResult(result.Data)
	expected := "No resources found. MCP servers may still provide tools even if they have no resources."
	if rendered != expected {
		t.Errorf("RenderResult(empty) = %q, want %q", rendered, expected)
	}
}

func TestRenderResourceResult_NonEmptyResources(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	reg.SetConnectionForTest("s1", &gbotmcp.ConnectedServer{
		Name:    "s1",
		Session: nil,
		Config:  gbotmcp.ScopedMcpServerConfig{Config: &gbotmcp.StdioConfig{Command: "test"}, Scope: gbotmcp.ScopeUser},
	})
	reg.PutResourceCacheForTest("s1", []gbotmcp.ServerResource{
		{URI: "test://1", Name: "res1", Server: "s1"},
	})

	tt := NewListMcpResources(reg)
	result, err := tt.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	rendered := tt.RenderResult(result.Data)
	// Should be human-readable format with server name and URI
	if !strings.Contains(rendered, "test://1") {
		t.Errorf("should contain resource URI, got: %q", rendered)
	}
	if !strings.Contains(rendered, "s1") {
		t.Errorf("should contain server name, got: %q", rendered)
	}
	if !strings.Contains(rendered, "1 resources") {
		t.Errorf("should contain resource count, got: %q", rendered)
	}
}

// ---------------------------------------------------------------------------
// renderResourceResult branch coverage
// ---------------------------------------------------------------------------

func TestRenderResourceResult_NilData_ReadMcpResource(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	// RenderResult with nil data — exercises !isEmptyFriendly nil branch
	result := tt.RenderResult(nil)
	if result != "" {
		t.Errorf("RenderResult(nil) = %q, want empty string", result)
	}
}

func TestRenderResourceResult_EmptyResourceContentSlice(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	// RenderResult with empty []ResourceContent — exercises "[]" branch
	result := tt.RenderResult([]gbotmcp.ResourceContent{})
	if result != "[]" {
		t.Errorf("RenderResult(empty ResourceContent) = %q, want %q", result, "[]")
	}
}

func TestRenderResourceResult_ResourceContentWithData(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	contents := []gbotmcp.ResourceContent{
		{URI: "test://1", MimeType: "text/plain", Text: "hello"},
	}
	result := tt.RenderResult(contents)
	if !strings.Contains(result, "test://1") {
		t.Errorf("should contain URI, got: %q", result)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("should contain text, got: %q", result)
	}
}

func TestListMcpResources_FormatWireResult(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	wf, ok := tt.(tool.ToolWithWireFormat)
	if !ok {
		t.Fatal("ListMcpResources should implement ToolWithWireFormat")
	}

	// nil data with isEmptyFriendly → friendly message
	result := wf.FormatWireResult(nil)
	expected := "No resources found. MCP servers may still provide tools even if they have no resources."
	if result != expected {
		t.Errorf("FormatWireResult(nil) = %q, want %q", result, expected)
	}

	// empty slice with isEmptyFriendly → friendly message
	result = wf.FormatWireResult([]gbotmcp.ServerResource{})
	if result != expected {
		t.Errorf("FormatWireResult(empty) = %q, want %q", result, expected)
	}

	// non-empty slice → JSON
	result = wf.FormatWireResult([]gbotmcp.ServerResource{
		{URI: "test://1", Name: "res1", Server: "s1"},
	})
	if !strings.Contains(result, "test://1") {
		t.Errorf("should contain URI, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// ReadMcpResource successful call through adapter (covers success path)
// ---------------------------------------------------------------------------

func TestReadMcpResourceAdapter_SuccessfulCall(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	go func() {
		_, _ = server.Connect(context.Background(), t1, nil)
	}()

	server.AddResource(&mcp.Resource{
		URI:      "test://hello",
		Name:     "hello",
		MIMEType: "text/plain",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://hello", MIMEType: "text/plain", Text: "Hello, World!"},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", &gbotmcp.ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  gbotmcp.ScopedMcpServerConfig{Config: &gbotmcp.StdioConfig{Command: "test"}, Scope: gbotmcp.ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	})

	tt := NewReadMcpResource(reg)
	input := json.RawMessage(`{"server": "test-server", "uri": "test://hello"}`)
	result, err := tt.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	rendered := tt.RenderResult(result.Data)
	if !strings.Contains(rendered, "Hello, World!") {
		t.Errorf("should contain text content, got: %q", rendered)
	}
	if !strings.Contains(rendered, "test://hello") {
		t.Errorf("should contain URI, got: %q", rendered)
	}
}

// ---------------------------------------------------------------------------
// renderResourceResult: compact JSON fallback (line 198-200)
// ---------------------------------------------------------------------------

func TestRenderResourceResult_CompactJSONFallback(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	result := tt.RenderResult(make(chan int))
	if result != "" {
		t.Errorf("expected empty string for non-marshalable value, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Additional property coverage for both tools
// ---------------------------------------------------------------------------

func TestReadMcpResource_AllProperties(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	// Exercise all property lambdas
	desc, err := tt.Description(nil)
	if err != nil {
		t.Fatalf("Description error: %v", err)
	}
	if desc != "Read a resource from an MCP server" {
		t.Errorf("unexpected description: %q", desc)
	}

	// CheckPermissions
	perm := tt.CheckPermissions(nil, nil)
	if perm == nil {
		t.Error("CheckPermissions returned nil")
	}

	schema := tt.InputSchema()
	if !strings.Contains(string(schema), "server") {
		t.Errorf("schema should contain 'server': %s", schema)
	}

	// InterruptBehavior
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior = %v, want InterruptCancel", tt.InterruptBehavior())
	}

	// MaxResultSize
	if tt.MaxResultSize() != 100_000 {
		t.Errorf("MaxResultSize = %d, want 100000", tt.MaxResultSize())
	}

	// Prompt
	if tt.Prompt() == "" {
		t.Error("Prompt should not be empty")
	}

	// Aliases
	if len(tt.Aliases()) != 0 {
		t.Errorf("Aliases = %v, want empty", tt.Aliases())
	}
}

func TestListMcpResources_AllProperties(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	// Exercise all property lambdas
	desc, err := tt.Description(nil)
	if err != nil {
		t.Fatalf("Description error: %v", err)
	}
	if desc != "List available resources from configured MCP servers" {
		t.Errorf("unexpected description: %q", desc)
	}

	perm := tt.CheckPermissions(nil, nil)
	if perm == nil {
		t.Error("CheckPermissions returned nil")
	}

	// InterruptBehavior
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior = %v, want InterruptCancel", tt.InterruptBehavior())
	}

	// MaxResultSize
	if tt.MaxResultSize() != 100_000 {
		t.Errorf("MaxResultSize = %d, want 100000", tt.MaxResultSize())
	}

	// Prompt
	if tt.Prompt() == "" {
		t.Error("Prompt should not be empty")
	}

	// Aliases
	if len(tt.Aliases()) != 0 {
		t.Errorf("Aliases = %v, want empty", tt.Aliases())
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

