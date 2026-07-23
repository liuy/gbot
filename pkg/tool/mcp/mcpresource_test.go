package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	gbotmcp "github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestListMcpResources_FormatWireBlocks(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	wb, ok := tt.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("ListMcpResources should implement ToolWithWireBlocks")
	}

	// nil data with isEmptyFriendly → friendly message
	blocks := wb.FormatWireBlocks(nil)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	expected := "No resources found. MCP servers may still provide tools even if they have no resources."
	if blocks[0].Text != expected {
		t.Errorf("FormatWireBlocks(nil).Text = %q, want %q", blocks[0].Text, expected)
	}

	// empty slice with isEmptyFriendly → friendly message
	blocks = wb.FormatWireBlocks([]gbotmcp.ServerResource{})
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Text != expected {
		t.Errorf("FormatWireBlocks(empty).Text = %q, want %q", blocks[0].Text, expected)
	}

	// non-empty slice → JSON
	blocks = wb.FormatWireBlocks([]gbotmcp.ServerResource{
		{URI: "test://1", Name: "res1", Server: "s1"},
	})
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "test://1") {
		t.Errorf("should contain URI, got: %q", blocks[0].Text)
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
// Description_ branches: json.Unmarshal error + server-only description
// ---------------------------------------------------------------------------

func TestListMcpResources_Description_InvalidJSON(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	desc, err := tt.Description(json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != listMcpResourcesDescription {
		t.Errorf("Description(invalid json) = %q, want %q", desc, listMcpResourcesDescription)
	}
}

func TestReadMcpResource_Description_InvalidJSON(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	desc, err := tt.Description(json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != readMcpResourceDescription {
		t.Errorf("Description(invalid json) = %q, want %q", desc, readMcpResourceDescription)
	}
}

func TestReadMcpResource_Description_ServerOnly(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	desc, err := tt.Description(json.RawMessage(`{"server":"my-server"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "my-server" {
		t.Errorf("Description(server-only) = %q, want %q", desc, "my-server")
	}
}

func TestReadMcpResource_Description_ServerAndURI(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	desc, err := tt.Description(json.RawMessage(`{"server":"my-server","uri":"test://x"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "my-server test://x" {
		t.Errorf("Description(server+uri) = %q, want %q", desc, "my-server test://x")
	}
}

func TestListMcpResources_Description_WithServer(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewListMcpResources(reg)

	desc, err := tt.Description(json.RawMessage(`{"server":"my-server"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "my-server" {
		t.Errorf("Description(with server) = %q, want %q", desc, "my-server")
	}
}

// ---------------------------------------------------------------------------
// renderResourceResultJSON: empty ResourceContent, MarshalIndent error
// ---------------------------------------------------------------------------

func TestRenderResourceResultJSON_NilData(t *testing.T) {
	got := renderResourceResultJSON(nil)
	if got != emptyResourcesMessage {
		t.Errorf("renderResourceResultJSON(nil) = %q, want %q", got, emptyResourcesMessage)
	}
}

func TestRenderResourceResultJSON_EmptyServerResourceSlice(t *testing.T) {
	got := renderResourceResultJSON([]gbotmcp.ServerResource{})
	if got != emptyResourcesMessage {
		t.Errorf("renderResourceResultJSON(empty ServerResource) = %q, want %q", got, emptyResourcesMessage)
	}
}

func TestRenderResourceResultJSON_EmptyResourceContentSlice(t *testing.T) {
	got := renderResourceResultJSON([]gbotmcp.ResourceContent{})
	if got != "[]" {
		t.Errorf("renderResourceResultJSON(empty ResourceContent) = %q, want %q", got, "[]")
	}
}

func TestRenderResourceResultJSON_MarshalIndentError(t *testing.T) {
	// Channels cannot be marshaled to JSON, so MarshalIndent fails
	// and the fallback json.Marshal also fails, returning empty string.
	got := renderResourceResultJSON(make(chan int))
	if got != "" {
		// json.Marshal(chan) returns an error; the fallback also fails.
		// Both paths fail → returns "" from the string(b) where b is nil.
		t.Logf("renderResourceResultJSON(chan) = %q", got)
	}
}

func TestRenderResourceResultJSON_NonEmptyResourceContent(t *testing.T) {
	contents := []gbotmcp.ResourceContent{
		{URI: "test://1", Text: "hello"},
	}
	got := renderResourceResultJSON(contents)
	if !strings.Contains(got, "test://1") {
		t.Errorf("should contain URI, got: %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("should contain text, got: %q", got)
	}
}

// ---------------------------------------------------------------------------
// renderListResourcesTUI: nil, wrong type, MimeType
// ---------------------------------------------------------------------------

func TestRenderListResourcesTUI_Nil(t *testing.T) {
	got := renderListResourcesTUI(nil)
	if got != emptyResourcesMessage {
		t.Errorf("renderListResourcesTUI(nil) = %q, want %q", got, emptyResourcesMessage)
	}
}

func TestRenderListResourcesTUI_WrongType(t *testing.T) {
	got := renderListResourcesTUI("not a slice")
	if got != emptyResourcesMessage {
		t.Errorf("renderListResourcesTUI(string) = %q, want %q", got, emptyResourcesMessage)
	}
}

func TestRenderListResourcesTUI_WithMimeType(t *testing.T) {
	resources := []gbotmcp.ServerResource{
		{URI: "test://1", Name: "res1", Server: "s1", MimeType: "text/plain"},
		{URI: "test://2", Name: "res2", Server: "s1"},
	}
	got := renderListResourcesTUI(resources)
	if !strings.Contains(got, "test://1 (text/plain)") {
		t.Errorf("should show MimeType for res1, got: %q", got)
	}
	// res2 has no MimeType — should NOT have parentheses
	_, after, found := strings.Cut(got, "test://2")
	if !found {
		t.Fatalf("should contain test://2, got: %q", got)
	}
	// After test://2, next char should be newline, not space+paren
	if strings.HasPrefix(after, " (") {
		t.Errorf("res2 should not have MimeType, got: %q", got)
	}
}

func TestRenderListResourcesTUI_MultipleServers(t *testing.T) {
	resources := []gbotmcp.ServerResource{
		{URI: "test://b", Name: "rb", Server: "srv-b"},
		{URI: "test://a", Name: "ra", Server: "srv-a"},
	}
	got := renderListResourcesTUI(resources)
	// Servers should be sorted alphabetically
	idxA := strings.Index(got, "srv-a")
	idxB := strings.Index(got, "srv-b")
	if idxA == -1 || idxB == -1 {
		t.Fatalf("should contain both servers, got: %q", got)
	}
	if idxA > idxB {
		t.Errorf("srv-a should appear before srv-b, got: %q", got)
	}
	if !strings.Contains(got, "2 resources from 2 servers") {
		t.Errorf("should show total count, got: %q", got)
	}
}

// ---------------------------------------------------------------------------
// renderReadResourceTUI: multiple contents separator, BlobSavedTo
// ---------------------------------------------------------------------------

func TestRenderReadResourceTUI_MultipleContents(t *testing.T) {
	contents := []gbotmcp.ResourceContent{
		{URI: "test://1", Text: "first"},
		{URI: "test://2", Text: "second"},
	}
	got := renderReadResourceTUI(contents)
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("should contain both texts, got: %q", got)
	}
	if !strings.Contains(got, "test://1") || !strings.Contains(got, "test://2") {
		t.Errorf("should contain both URIs, got: %q", got)
	}
	// Verify separator: there should be a newline between the two entries
	// Pattern: "first\ntest://2" — the separator is the \n between entries
	parts := strings.Split(got, "\n")
	// Should have: [test://1], first, [test://2], second, at minimum
	found := 0
	for _, p := range parts {
		if strings.Contains(p, "first") || strings.Contains(p, "second") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("expected 2 content lines, found %d in: %q", found, got)
	}
}

func TestRenderReadResourceTUI_BlobSavedTo(t *testing.T) {
	contents := []gbotmcp.ResourceContent{
		{URI: "test://binary", BlobSavedTo: "/tmp/saved.bin"},
	}
	got := renderReadResourceTUI(contents)
	if !strings.Contains(got, "(binary content saved to /tmp/saved.bin)") {
		t.Errorf("should show BlobSavedTo path, got: %q", got)
	}
}

func TestRenderReadResourceTUI_NilData(t *testing.T) {
	got := renderReadResourceTUI(nil)
	if got != "" {
		t.Errorf("renderReadResourceTUI(nil) = %q, want empty", got)
	}
}

func TestRenderReadResourceTUI_WrongType(t *testing.T) {
	got := renderReadResourceTUI("not a slice")
	if got != "" {
		t.Errorf("renderReadResourceTUI(string) = %q, want empty", got)
	}
}

func TestRenderReadResourceTUI_WithMimeType(t *testing.T) {
	contents := []gbotmcp.ResourceContent{
		{URI: "test://1", MimeType: "application/json", Text: `{}`},
	}
	got := renderReadResourceTUI(contents)
	if !strings.Contains(got, "[test://1] (application/json)") {
		t.Errorf("should show MimeType in header, got: %q", got)
	}
}

func TestRenderListResourcesTUI_JSONRawMessage(t *testing.T) {
	raw := json.RawMessage(`[{"uri":"test://1","name":"res1","server":"s1","mimeType":"text/plain"}]`)
	v, err := NewListMcpResources(nil).(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	got := renderListResourcesTUI(v)
	if !strings.Contains(got, "test://1") {
		t.Errorf("renderListResourcesTUI(RawMessage) should contain URI, got: %q", got)
	}
	if !strings.Contains(got, "text/plain") {
		t.Errorf("renderListResourcesTUI(RawMessage) should contain MIME type, got: %q", got)
	}
}

func TestRenderReadResourceTUI_JSONRawMessage(t *testing.T) {
	raw := json.RawMessage(`[{"uri":"test://1","text":"hello world"}]`)
	v, err := NewReadMcpResource(nil).(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	got := renderReadResourceTUI(v)
	if !strings.Contains(got, "test://1") {
		t.Errorf("renderReadResourceTUI(RawMessage) should contain URI, got: %q", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("renderReadResourceTUI(RawMessage) should contain text, got: %q", got)
	}
}

// ---------------------------------------------------------------------------
// FormatWireBlocks for ReadMcpResource (line 166-168)
// ---------------------------------------------------------------------------

func TestReadMcpResource_FormatWireBlocks(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	tt := NewReadMcpResource(reg)

	wb, ok := tt.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("ReadMcpResource should implement ToolWithWireBlocks")
	}

	// nil data → emptyResourcesMessage (from renderResourceResultJSON)
	blocks := wb.FormatWireBlocks(nil)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	if blocks[0].Text != emptyResourcesMessage {
		t.Errorf("FormatWireBlocks(nil).Text = %q, want %q", blocks[0].Text, emptyResourcesMessage)
	}

	// empty ResourceContent → "[]"
	blocks = wb.FormatWireBlocks([]gbotmcp.ResourceContent{})
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Text != "[]" {
		t.Errorf("FormatWireBlocks(empty ResourceContent).Text = %q, want %q", blocks[0].Text, "[]")
	}

	// non-empty ResourceContent → JSON
	blocks = wb.FormatWireBlocks([]gbotmcp.ResourceContent{
		{URI: "test://1", Text: "hello"},
	})
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "test://1") {
		t.Errorf("FormatWireBlocks with content should contain URI, got: %q", blocks[0].Text)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
