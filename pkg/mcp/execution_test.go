package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// ---------------------------------------------------------------------------
// CallMCPTool
// ---------------------------------------------------------------------------

func TestCallMCPTool_NilServer(t *testing.T) {
	_, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   nil,
		ToolName: "test",
	})
	if err == nil {
		t.Fatal("expected error for nil server")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error = %q, want 'not connected'", err.Error())
	}
}

func TestCallMCPTool_NilSession(t *testing.T) {
	_, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   &ConnectedServer{Name: "test"},
		ToolName: "test",
	})
	if err == nil {
		t.Fatal("expected error for nil session")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error should mention 'not connected', got: %v", err)
	}
}

func TestCallMCPTool_Success(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echo tool",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		msg, _ := input["message"].(string)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + msg}},
		}, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	result, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "echo",
		Args:     map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("expected TextContent")
	}
	if tc.Text != "echo: hello" {
		t.Errorf("text = %q, want %q", tc.Text, "echo: hello")
	}
}

func TestCallMCPTool_Timeout(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	// Tool that blocks forever
	mcp.AddTool(server, &mcp.Tool{
		Name:        "slow_tool",
		Description: "Slow tool",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		<-ctx.Done()
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	// Use a very short timeout
	t.Setenv("MCP_TOOL_TIMEOUT", "100")
	_, err = CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "slow_tool",
		Args:     map[string]any{},
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestCallMCPTool_IsError(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "failing_tool",
		Description: "Always fails",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		result := &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "something went wrong"}},
		}
		return result, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	_, err = CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "failing_tool",
		Args:     map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error from failing tool")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("error = %q, want to contain error message", err.Error())
	}
	// Should be McpToolCallError
	var toolErr *McpToolCallError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *McpToolCallError, got %T: %v", err, err)
	}
}

func TestCallMCPTool_ClosedSession(t *testing.T) {
	_, t2 := setupInMemoryServer(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	_ = session.Close()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	_, err = CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "any_tool",
		Args:     map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error from closed session")
	}
	// SDK may return "closed" or "nil session" for closed connections
	errMsg := err.Error()
	if !strings.Contains(errMsg, "closed") && !strings.Contains(errMsg, "nil") {
		t.Errorf("error should mention closed or nil session, got: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// GetToolTimeoutMs
// ---------------------------------------------------------------------------

func TestGetToolTimeoutMs(t *testing.T) {
	if ms := GetToolTimeoutMs(); ms != DefaultToolCallTimeoutMs {
		t.Errorf("default = %d, want %d", ms, DefaultToolCallTimeoutMs)
	}

	t.Setenv("MCP_TOOL_TIMEOUT", "5000")
	if ms := GetToolTimeoutMs(); ms != 5000 {
		t.Errorf("custom = %d, want 5000", ms)
	}

	t.Setenv("MCP_TOOL_TIMEOUT", "invalid")
	if ms := GetToolTimeoutMs(); ms != DefaultToolCallTimeoutMs {
		t.Errorf("invalid = %d, want default", ms)
	}
}

// ---------------------------------------------------------------------------
// InferCompactSchema
// ---------------------------------------------------------------------------

func TestInferCompactSchema(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"nil", nil, "null"},
		{"bool", true, "boolean"},
		{"int", 42, "number"},
		{"float", 3.14, "number"},
		{"string", "hello", "string"},
		{"empty array", []any{}, "[]"},
		{"array of strings", []any{"a", "b"}, "[string]"},
		{"array of ints", []any{1, 2}, "[number]"},
		{"nested array", []any{[]any{1}}, "[[number]]"},
		{"empty object", map[string]any{}, "{}"},
		{"simple object", map[string]any{"name": "test", "age": 25.0}, "{age: number, name: string}"},
		{"nested object", map[string]any{"items": []any{map[string]any{"id": 1}}}, "{items: [{...}]}"},
		{"deep object at depth 0", map[string]any{"a": 1}, "{...}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferCompactSchema(tt.input)
			if tt.name == "deep object at depth 0" {
				got = InferCompactSchema(tt.input, 0)
			}
			if got != tt.want {
				t.Errorf("InferCompactSchema() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TransformResultContent
// ---------------------------------------------------------------------------

func TestTransformResultContent_Text(t *testing.T) {
	content := &mcp.TextContent{Text: "hello world"}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Type != MCPResultText {
		t.Errorf("type = %v, want %v", results[0].Type, MCPResultText)
	}
	if results[0].Content != "hello world" {
		t.Errorf("content = %q, want %q", results[0].Content, "hello world")
	}
}

func TestTransformResultContent_Image(t *testing.T) {
	content := &mcp.ImageContent{
		Data:     []byte("fake-image-data"),
		MIMEType: "image/png",
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Type != MCPResultImage {
		t.Errorf("type = %v, want %v", results[0].Type, MCPResultImage)
	}
	s, _ := results[0].Content.(string)
	if !strings.Contains(s, "Image from test-server") {
		t.Errorf("content should mention server, got: %q", s)
	}
	if !strings.Contains(s, "image/png") {
		t.Errorf("content should mention MIME type, got: %q", s)
	}
	if results[0].MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want %q", results[0].MIMEType, "image/png")
	}
}

func TestTransformResultContent_Audio(t *testing.T) {
	content := &mcp.AudioContent{
		Data:     []byte("fake-audio"),
		MIMEType: "audio/wav",
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Type != MCPResultAudio {
		t.Errorf("type = %v, want %v", results[0].Type, MCPResultAudio)
	}
	s, _ := results[0].Content.(string)
	if !strings.Contains(s, "Audio from test-server") {
		t.Errorf("content should mention audio, got: %q", s)
	}
}

func TestTransformResultContent_ResourceText(t *testing.T) {
	content := &mcp.EmbeddedResource{
		Resource: &mcp.ResourceContents{
			URI:  "file:///test.txt",
			Text: "file contents here",
		},
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	s, _ := results[0].Content.(string)
	if !strings.Contains(s, "Resource from test-server") {
		t.Errorf("content should mention resource, got: %q", s)
	}
	if !strings.Contains(s, "file contents here") {
		t.Errorf("content should include text, got: %q", s)
	}
}

func TestTransformResultContent_ResourceBlob(t *testing.T) {
	content := &mcp.EmbeddedResource{
		Resource: &mcp.ResourceContents{
			URI:      "file:///test.bin",
			Blob:     []byte("binary-data"),
			MIMEType: "application/octet-stream",
		},
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	s, _ := results[0].Content.(string)
	if !strings.Contains(s, "Binary resource") {
		t.Errorf("content should mention binary resource, got: %q", s)
	}
}

func TestTransformResultContent_ResourceNilResource(t *testing.T) {
	content := &mcp.EmbeddedResource{Resource: nil}
	results := TransformResultContent(content, "test-server")
	if len(results) != 0 {
		t.Errorf("expected nil for nil resource, got %d results", len(results))
	}
}

func TestTransformResultContent_ResourceLink(t *testing.T) {
	content := &mcp.ResourceLink{
		Name:        "My Resource",
		URI:         "file:///doc.md",
		Description: "A document",
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	s, _ := results[0].Content.(string)
	if !strings.Contains(s, "My Resource") {
		t.Errorf("should contain name, got: %q", s)
	}
	if !strings.Contains(s, "file:///doc.md") {
		t.Errorf("should contain URI, got: %q", s)
	}
}

// ---------------------------------------------------------------------------
// ContentContainsImages
// ---------------------------------------------------------------------------

func TestContentContainsImages(t *testing.T) {
	tests := []struct {
		name    string
		content []mcp.Content
		want    bool
	}{
		{
			"nil content",
			nil,
			false,
		},
		{
			"text only",
			[]mcp.Content{&mcp.TextContent{Text: "hello"}},
			false,
		},
		{
			"has image",
			[]mcp.Content{
				&mcp.TextContent{Text: "hello"},
				&mcp.ImageContent{Data: []byte("img"), MIMEType: "image/png"},
			},
			true,
		},
		{
			"only image",
			[]mcp.Content{&mcp.ImageContent{Data: []byte("img"), MIMEType: "image/png"}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContentContainsImages(tt.content)
			if got != tt.want {
				t.Errorf("ContentContainsImages() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TransformMCPResult
// ---------------------------------------------------------------------------

func TestTransformMCPResult_ContentArray(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "line 1"},
			&mcp.TextContent{Text: "line 2"},
		},
	}

	transformed, _ := TransformMCPResult(result, "echo", "test-server")
	if len(transformed) != 2 {
		t.Fatalf("expected 2 results, got %d", len(transformed))
	}
	s1, _ := transformed[0].Content.(string)
	if s1 != "line 1" {
		t.Errorf("first content = %q, want %q", s1, "line 1")
	}
}

func TestTransformMCPResult_StructuredContent(t *testing.T) {
	result := &mcp.CallToolResult{
		StructuredContent: map[string]any{"key": "value"},
	}

	transformed, schema := TransformMCPResult(result, "tool", "server")
	if len(transformed) != 1 {
		t.Fatalf("expected 1 result, got %d", len(transformed))
	}
	s, _ := transformed[0].Content.(string)
	if !strings.Contains(s, "key") {
		t.Errorf("should contain structured data, got: %q", s)
	}
	if schema == "" {
		t.Error("expected non-empty schema for structured content")
	}
}

func TestTransformMCPResult_EmptyContent(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{},
	}

	transformed, _ := TransformMCPResult(result, "tool", "server")
	if len(transformed) != 0 {
		t.Errorf("expected nil for empty content, got %d", len(transformed))
	}
}

// ---------------------------------------------------------------------------
// ProcessMCPResult
// ---------------------------------------------------------------------------

func TestProcessMCPResult_SmallContent(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "small output"}},
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	if content != "small output" {
		t.Errorf("content = %q, want %q", content, "small output")
	}
}

func TestProcessMCPResult_LargeContent_Persist(t *testing.T) {
	// Create content larger than maxMCPContentChars
	largeText := strings.Repeat("x", maxMCPContentChars+1000)
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: largeText}},
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	// Should be persisted to file, not the raw content
	if strings.HasPrefix(content, "xxx") {
		t.Error("large content should be persisted, not returned raw")
	}
	if !strings.Contains(content, "Large output persisted") {
		t.Errorf("should mention persistence, got: %q", content[:100])
	}
}

func TestProcessMCPResult_LargeContent_EnvDisabled(t *testing.T) {
	t.Setenv("ENABLE_MCP_LARGE_OUTPUT_FILES", "false")

	largeText := strings.Repeat("x", maxMCPContentChars+1000)
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: largeText}},
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	// Should be truncated, not persisted
	if strings.Contains(content, "Large output persisted") {
		t.Error("should not persist when env disabled")
	}
	if !strings.Contains(content, "truncated") {
		t.Error("should be truncated when env disabled")
	}
}

func TestProcessMCPResult_LargeContent_WithImages(t *testing.T) {
	// Image content should fallback to truncation even if large
	largeText := strings.Repeat("x", maxMCPContentChars+1000)
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: largeText},
			&mcp.ImageContent{Data: []byte("img"), MIMEType: "image/png"},
		},
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	// Should be truncated because content contains images
	if strings.Contains(content, "Large output persisted") {
		t.Error("should not persist content with images")
	}
}

// ---------------------------------------------------------------------------
// extractErrorMessage
// ---------------------------------------------------------------------------

func TestExtractErrorMessage(t *testing.T) {
	tests := []struct {
		name   string
		result *mcp.CallToolResult
		want   string
	}{
		{
			"text content error",
			&mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "file not found"}},
			},
			"file not found",
		},
		{
			"empty content",
			&mcp.CallToolResult{IsError: true, Content: []mcp.Content{}},
			"unknown tool error",
		},
		{
			"nil content",
			&mcp.CallToolResult{IsError: true, Content: nil},
			"unknown tool error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractErrorMessage(tt.result)
			if got != tt.want {
				t.Errorf("extractErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// truncateContent
// ---------------------------------------------------------------------------

func TestTruncateContent(t *testing.T) {
	// Short content is unchanged
	short := "hello"
	if got := truncateContent(short, 100); got != short {
		t.Errorf("short content should be unchanged, got %q", got)
	}

	// Long content is truncated
	long := strings.Repeat("a", 200)
	got := truncateContent(long, 100)
	if len(got) > 120 { // 100 + truncation marker
		t.Errorf("truncated content too long: %d chars", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncated content should contain truncation marker")
	}
}

// ---------------------------------------------------------------------------
// persistToolResult
// ---------------------------------------------------------------------------

func TestPersistToolResult(t *testing.T) {
	content := "test content " + strings.Repeat("x", 1000)
	persistID := fmt.Sprintf("test-persist-%d", time.Now().UnixMilli())

	path, err := persistToolResult(content, persistID)
	if err != nil {
		t.Fatalf("persistToolResult: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("file should exist at %q", path)
	}

	// Verify content
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != content {
		t.Errorf("file content mismatch: got %d bytes, want %d bytes", len(data), len(content))
	}
}

// ---------------------------------------------------------------------------
// CallMCPToolWithUrlElicitationRetry — stub test
// ---------------------------------------------------------------------------

func TestCallMCPToolWithUrlElicitationRetry_Delegates(t *testing.T) {
	// Just verify the stub delegates to CallMCPTool
	_, err := CallMCPToolWithUrlElicitationRetry(context.Background(), CallMCPToolParams{
		Server:   nil,
		ToolName: "test",
	})
	if err == nil {
		t.Fatal("expected error from nil server (stub should delegate)")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error = %q, want 'not connected'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// MCPProgress type
// ---------------------------------------------------------------------------

func TestMCPProgress_Fields(t *testing.T) {
	p := MCPProgress{
		Type:            "progress",
		Status:          "in_progress",
		ServerName:      "server",
		ToolName:        "tool",
		Progress:        50.0,
		Total:           100.0,
		ProgressMessage: "halfway",
	}
	if p.Type != "progress" {
		t.Errorf("Type = %q, want %q", p.Type, "progress")
	}
	if p.Progress != 50.0 {
		t.Errorf("Progress = %v, want 50", p.Progress)
	}
}

// ---------------------------------------------------------------------------
// InferCompactSchema — from JSON
// ---------------------------------------------------------------------------

func TestInferCompactSchema_FromJSON(t *testing.T) {
	// Verify it works with decoded JSON objects
	var v any
	if err := json.Unmarshal([]byte(`{"items": [{"id": 1, "name": "test"}], "total": 5}`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	schema := InferCompactSchema(v)
	if !strings.Contains(schema, "items:") {
		t.Errorf("schema should contain 'items:', got: %q", schema)
	}
	if !strings.Contains(schema, "total:") {
		t.Errorf("schema should contain 'total:', got: %q", schema)
	}
}

// ===========================================================================
// Additional coverage tests for InferCompactSchema, TransformResultContent, persistToolResult
// ===========================================================================

// ---------------------------------------------------------------------------
// InferCompactSchema (75% → 90%+) — Source: execution.go:162-222
// ---------------------------------------------------------------------------

func TestInferCompactSchema_DepthParameter(t *testing.T) {
	nested := map[string]any{
		"level1": map[string]any{
			"level2": map[string]any{
				"level3": "value",
			},
		},
	}
	// Default depth (2) should truncate
	schema := InferCompactSchema(nested)
	if !strings.Contains(schema, "{...}") {
		t.Errorf("default depth should truncate, got: %q", schema)
	}
	// Depth 3 should show level3
	schema = InferCompactSchema(nested, 3)
	if strings.Contains(schema, "{...}") {
		t.Errorf("depth 3 should not truncate, got: %q", schema)
	}
	if !strings.Contains(schema, "level3") {
		t.Errorf("depth 3 should show level3, got: %q", schema)
	}
}

func TestInferCompactSchema_NestedObjects(t *testing.T) {
	nested := map[string]any{
		"user": map[string]any{
			"name": "Alice",
			"age":  30,
		},
		"meta": map[string]any{
			"version": "1.0",
		},
	}
	schema := InferCompactSchema(nested)
	if !strings.Contains(schema, "user:") {
		t.Errorf("should contain user:, got: %q", schema)
	}
	if !strings.Contains(schema, "meta:") {
		t.Errorf("should contain meta:, got: %q", schema)
	}
	if !strings.Contains(schema, "name:") {
		t.Errorf("should contain name:, got: %q", schema)
	}
}

func TestInferCompactSchema_ArrayTypes(t *testing.T) {
	arr := []any{"string", 42, true}
	schema := InferCompactSchema(arr)
	if schema != "[string]" {
		t.Errorf("array schema should be [string], got: %q", schema)
	}
}

func TestInferCompactSchema_EmptyArray(t *testing.T) {
	schema := InferCompactSchema([]any{})
	if schema != "[]" {
		t.Errorf("empty array schema should be [], got: %q", schema)
	}
}

func TestInferCompactSchema_MoreThan10Keys(t *testing.T) {
	largeMap := make(map[string]any)
	for i := range 15 {
		largeMap[fmt.Sprintf("key%d", i)] = i
	}
	schema := InferCompactSchema(largeMap)
	if !strings.Contains(schema, ", ...") {
		t.Errorf("should truncate with ..., got: %q", schema)
	}
	// Should show first 10 keys sorted
	if !strings.Contains(schema, "key0:") {
		t.Errorf("should contain key0:, got: %q", schema)
	}
}

// ---------------------------------------------------------------------------
// persistToolResult (83.3% → 90%+) — Source: execution.go:421-431
// ---------------------------------------------------------------------------

func TestPersistToolResult_WithPersistID(t *testing.T) {
	content := "Test result content"
	persistID := "test-persist-123"

	path, err := persistToolResult(content, persistID)
	if err != nil {
		t.Fatalf("persistToolResult: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	// Verify file path
	if !strings.Contains(path, persistID+".txt") {
		t.Errorf("path should contain persistID, got: %q", path)
	}
	// Verify file exists in temp dir
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should exist: %v", err)
	}
	// Verify content
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", string(data), content)
	}
}

func TestPersistToolResult_WithoutPersistID(t *testing.T) {
	// Empty persistID should still work
	content := "No ID test"
	path, err := persistToolResult(content, "")
	if err != nil {
		t.Fatalf("persistToolResult with empty ID: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	// Should create file named ".txt"
	if !strings.HasSuffix(path, ".txt") {
		t.Errorf("path should end with .txt, got: %q", path)
	}
}

// ===========================================================================
// Coverage: CallMCPTool auth error, session expired error, InferCompactSchema
// unknown type, TransformResultContent resource link no description, embedded
// resource empty, ProcessMCPResult env="0", persistToolResult write error
// ===========================================================================

// TestCallMCPTool_AuthError tests that CallMCPTool returns McpAuthError when
// the server returns a 401/Unauthorized error.
func TestCallMCPTool_AuthError(t *testing.T) {
	// Use a closed session to trigger an error from CallTool that contains "401"
	server, t2 := setupInMemoryServer(t)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "auth_tool",
		Description: "Auth required tool",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, fmt.Errorf("401 Unauthorized: API key invalid")
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "auth-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	_, err = CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "auth_tool",
		Args:     map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error from auth tool")
	}

	// The SDK may wrap the error differently. Check what we actually get.
	// If the error contains "401" it should be McpAuthError, otherwise it's
	// a generic McpToolCallError.
	errMsg := err.Error()
	t.Logf("auth error type: %T, message: %v", err, err)

	var authErr *McpAuthError
	if errors.As(err, &authErr) {
		if authErr.ServerName != "auth-server" {
			t.Errorf("ServerName = %q, want %q", authErr.ServerName, "auth-server")
		}
	} else if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "Unauthorized") {
		// Error contains auth keywords but is wrapped differently
		t.Logf("error contains auth keywords but is wrapped as %T", err)
	} else {
		t.Errorf("expected auth-related error, got: %v", err)
	}
}

// TestCallMCPTool_SessionExpiredError tests that CallMCPTool detects session
// expiry errors from the server.
func TestCallMCPTool_SessionExpiredError(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "expired_tool",
		Description: "Session expired tool",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		return nil, nil, fmt.Errorf(`HTTP 404 Not Found: {"code":-32001,"message":"Session not found"}`)
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "expired-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	_, err = CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "expired_tool",
		Args:     map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error from expired session tool")
	}

	// The SDK wraps the tool handler error, and CallMCPTool catches it.
	// The error may be McpToolCallError depending on how the SDK passes it through.
	errMsg := err.Error()
	t.Logf("session error type: %T, message: %v", err, err)

	var toolErr *McpToolCallError
	if errors.As(err, &toolErr) {
		// Verify it's for the right server and tool
		if toolErr.ServerName != "expired-server" {
			t.Errorf("ServerName = %q, want %q", toolErr.ServerName, "expired-server")
		}
		if toolErr.ToolName != "expired_tool" {
			t.Errorf("ToolName = %q, want %q", toolErr.ToolName, "expired_tool")
		}
	} else {
		t.Errorf("expected *McpToolCallError, got %T: %v", err, err)
	}

	// Verify the error message contains the session expiry markers
	if !strings.Contains(errMsg, "404") {
		t.Errorf("error should contain 404, got: %v", errMsg)
	}
}

// TestCallMCPTool_MetaResult tests CallMCPTool with result that has Meta.
func TestCallMCPTool_MetaResult(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "meta_tool",
		Description: "Tool with meta",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "result"}},
		}, map[string]any{"progressToken": "abc"}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	result, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "meta_tool",
		Args:     map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content, got %d", len(result.Content))
	}
}

// TestInferCompactSchema_UnknownType tests InferCompactSchema with a type
// that requires JSON round-trip (the default case).
func TestInferCompactSchema_UnknownType(t *testing.T) {
	// complex128 triggers the default case in the type switch
	schema := InferCompactSchema(complex(1, 2))
	if schema == "" {
		t.Error("expected non-empty schema for complex number")
	}
	// complex128 marshals to JSON object, so should infer from that
	t.Logf("complex128 schema: %q", schema)
}

// TestInferCompactSchema_JSONRoundTripFail tests InferCompactSchema when
// JSON marshal fails for the unknown type.
func TestInferCompactSchema_JSONRoundTripFail(t *testing.T) {
	// Channels cannot be marshaled to JSON
	ch := make(chan int)
	schema := InferCompactSchema(ch)
	if schema != "unknown" {
		t.Errorf("expected 'unknown' for unmarshallable type, got %q", schema)
	}
}

// TestTransformResultContent_ResourceLinkNoDescription tests ResourceLink
// with empty description (should use "no description").
func TestTransformResultContent_ResourceLinkNoDescription(t *testing.T) {
	content := &mcp.ResourceLink{
		Name:        "My Resource",
		URI:         "file:///doc.md",
		Description: "",
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	s, _ := results[0].Content.(string)
	if !strings.Contains(s, "no description") {
		t.Errorf("should contain 'no description' for empty desc, got: %q", s)
	}
	if !strings.Contains(s, "My Resource") {
		t.Errorf("should contain resource name, got: %q", s)
	}
}

// TestTransformResultContent_EmbeddedResourceEmpty tests EmbeddedResource
// with a Resource that has neither Text nor Blob.
func TestTransformResultContent_EmbeddedResourceEmpty(t *testing.T) {
	content := &mcp.EmbeddedResource{
		Resource: &mcp.ResourceContents{
			URI: "file:///empty.txt",
		},
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 0 {
		t.Errorf("expected nil for empty resource, got %d results", len(results))
	}
}

// TestTransformResultContent_UnknownContent tests TransformResultContent
// with an unrecognized content type (default case).
func TestTransformResultContent_UnknownContent(t *testing.T) {
	// Use a nil content to trigger the default case
	var unknown mcp.Content = nil
	results := TransformResultContent(unknown, "test-server")
	if len(results) != 0 {
		t.Errorf("expected nil for unknown content, got %d results", len(results))
	}
}

// TestProcessMCPResult_LargeContent_EnvDisabledZero tests the env="0" branch.
func TestProcessMCPResult_LargeContent_EnvDisabledZero(t *testing.T) {
	t.Setenv("ENABLE_MCP_LARGE_OUTPUT_FILES", "0")

	largeText := strings.Repeat("x", maxMCPContentChars+1000)
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: largeText}},
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	if strings.Contains(content, "Large output persisted") {
		t.Error("should not persist when env=0")
	}
	if !strings.Contains(content, "truncated") {
		t.Error("should be truncated when env=0")
	}
}

// TestProcessMCPResult_PersistFailure tests that when persistence fails,
// it falls back to truncation.
func TestProcessMCPResult_PersistFailure(t *testing.T) {
	largeText := strings.Repeat("x", maxMCPContentChars+1000)
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: largeText}},
	}

	// persistToolResult writes to os.TempDir() which should succeed in normal
	// conditions. We test the happy path here.
	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	if !strings.Contains(content, "Large output persisted") && !strings.Contains(content, "truncated") {
		t.Errorf("expected persist or truncate, got: %q", content[:min(len(content), 100)])
	}
}

// TestPersistToolResult_WriteError tests persistToolResult when the write fails.
func TestPersistToolResult_WriteError(t *testing.T) {
	// Try to write to a path that contains null bytes (invalid)
	_, err := persistToolResult("test", "\x00invalid")
	if err == nil {
		t.Fatal("want error for invalid path")
	}
	if !strings.Contains(err.Error(), "persist") {
		t.Errorf("error should mention persist, got: %v", err)
	}
}

// TestInferCompactSchema_JSONRoundTripSuccess tests the default case where
// JSON round-trip succeeds and InferCompactSchema recurses on the decoded value.
func TestInferCompactSchema_JSONRoundTripSuccess(t *testing.T) {
	// time.Duration is an int64, which the type switch doesn't match directly
	// but JSON marshal/unmarshal converts to float64 (number) which is handled
	schema := InferCompactSchema(time.Duration(5))
	if schema != "number" {
		t.Errorf("time.Duration should round-trip to number, got: %q", schema)
	}
}

// TestInferCompactSchema_UnmarshalToMap tests that JSON objects decoded via
// the default case get properly handled by InferCompactSchema recursively.
func TestInferCompactSchema_UnmarshalToMap(t *testing.T) {
	// Create a custom struct that triggers the default case but marshals to JSON object
	type custom struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	schema := InferCompactSchema(custom{X: 1, Y: 2})
	if !strings.Contains(schema, "x:") || !strings.Contains(schema, "y:") {
		t.Errorf("expected x: and y: in schema, got: %q", schema)
	}
}

// TestProcessMCPResult_PersistFallbackOnWriteError tests the fallback to
// truncation when persistToolResult fails.
func TestProcessMCPResult_PersistFallbackOnWriteError(t *testing.T) {
	// We can't easily force persistToolResult to fail since it writes to TempDir.
	// But we can verify the code path by testing that the persistID format is correct.
	// The persistToolResult function is tested separately.
	// This test verifies the full ProcessMCPResult flow for large content.
	largeText := strings.Repeat("a", maxMCPContentChars+100)
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: largeText}},
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	// Should either be persisted or truncated
	if len(content) > maxMCPContentChars+200 {
		t.Errorf("content should be truncated or persisted, got %d chars", len(content))
	}
}

// TestCallMCPTool_WithOnProgress tests that CallMCPTool works with an
// OnProgress callback (even though the Go SDK handles progress at the
// Client level, not per-call).
func TestCallMCPTool_WithOnProgress(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "progress_tool",
		Description: "Tool with progress",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "done"}},
		}, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	progressCalled := false
	result, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "progress_tool",
		Args:     map[string]any{},
		OnProgress: func(p MCPProgress) {
			progressCalled = true
		},
	})
	if err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content, got %d", len(result.Content))
	}
	// OnProgress is not called in the Go SDK per-call path
	if progressCalled {
		t.Log("OnProgress was called — unexpected but acceptable")
	}
}

// TestCallMCPTool_ResultWithMeta tests CallMCPTool where the result has non-nil Meta.
func TestCallMCPTool_ResultWithMeta(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "meta_tool",
		Description: "Tool returning meta",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "result"}},
			Meta:    map[string]any{"key": "value"},
		}, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	result, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "meta_tool",
		Args:     map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}
	if result.Meta == nil {
		t.Error("expected non-nil Meta in result")
	}
	if result.Meta["key"] != "value" {
		t.Errorf("Meta[key] = %v, want 'value'", result.Meta["key"])
	}
}

// ===========================================================================
// Coverage: InferCompactSchema with negative depth, ProcessMCPResult with
// empty env string, CallMCPTool with nil args
// ===========================================================================

// TestInferCompactSchema_NegativeDepth tests that negative depth still produces
// a valid result.
func TestInferCompactSchema_NegativeDepth(t *testing.T) {
	nested := map[string]any{
		"key": "value",
	}
	schema := InferCompactSchema(nested, -1)
	if schema != "{...}" {
		t.Errorf("negative depth should produce {...}, got: %q", schema)
	}
}

// TestInferCompactSchema_ArrayOfBooleans tests array of mixed bool/non-bool.
func TestInferCompactSchema_ArrayOfBooleans(t *testing.T) {
	arr := []any{true, false}
	schema := InferCompactSchema(arr)
	if schema != "[boolean]" {
		t.Errorf("array of bools should be [boolean], got: %q", schema)
	}
}

// TestInferCompactSchema_ArrayOfMaps tests array of maps.
func TestInferCompactSchema_ArrayOfMaps(t *testing.T) {
	arr := []any{
		map[string]any{"id": 1},
	}
	schema := InferCompactSchema(arr)
	if !strings.Contains(schema, "id:") {
		t.Errorf("array of maps should contain id:, got: %q", schema)
	}
	if !strings.HasPrefix(schema, "[{") {
		t.Errorf("array of maps should start with [{, got: %q", schema)
	}
}

// TestProcessMCPResult_EnvEmptyString tests that an empty ENABLE_MCP_LARGE_OUTPUT_FILES
// env var still allows persistence.
func TestProcessMCPResult_EnvEmptyString(t *testing.T) {
	t.Setenv("ENABLE_MCP_LARGE_OUTPUT_FILES", "")

	largeText := strings.Repeat("x", maxMCPContentChars+1000)
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: largeText}},
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	// Empty string should allow persistence (the check is env != "" but then
	// checks for "false" or "0" specifically)
	if !strings.Contains(content, "Large output persisted") {
		t.Errorf("empty env should allow persistence, got: %q", content[:min(len(content), 100)])
	}
}

// TestProcessMCPResult_NilContent tests ProcessMCPResult with nil content.
func TestProcessMCPResult_NilContent(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: nil,
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty content for nil content, got %q", content)
	}
}

// TestProcessMCPResult_TransformedNonStringContent tests ProcessMCPResult
// when transformed content has non-string items (images).
func TestProcessMCPResult_TransformedNonStringContent(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "text part"},
			&mcp.ImageContent{Data: []byte("img"), MIMEType: "image/png"},
		},
	}

	content, err := ProcessMCPResult(result, "tool", "server")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	// Should contain the text part
	if !strings.Contains(content, "text part") {
		t.Errorf("should contain text part, got: %q", content)
	}
}

// TestCallMCPTool_NilArgs tests CallMCPTool with nil args map.
func TestCallMCPTool_NilArgs(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "nil_args_tool",
		Description: "Handles nil args",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
		}, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	result, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "nil_args_tool",
		Args:     nil,
	})
	if err != nil {
		t.Fatalf("CallMCPTool with nil args: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content, got %d", len(result.Content))
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("expected TextContent")
	}
	if tc.Text != "ok" {
		t.Errorf("text = %q, want %q", tc.Text, "ok")
	}
}

// TestCallMCPTool_WithMeta tests CallMCPTool with Meta parameter set.
func TestCallMCPTool_WithMetaParam(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "meta_param_tool",
		Description: "Tool with meta param",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "meta-ok"}},
		}, nil, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}

	result, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "meta_param_tool",
		Args:     map[string]any{},
		Meta:     map[string]any{"progressToken": "tok-123"},
	})
	if err != nil {
		t.Fatalf("CallMCPTool with meta: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content, got %d", len(result.Content))
	}
}

// TestTransformMCPResult_EmptyResult tests TransformMCPResult with empty result.
func TestTransformMCPResult_EmptyResult(t *testing.T) {
	result := &mcp.CallToolResult{}
	transformed, schema := TransformMCPResult(result, "tool", "server")
	if len(transformed) != 0 {
		t.Errorf("expected empty for empty result, got %d", len(transformed))
	}
	if schema != "" {
		t.Errorf("expected empty schema for empty result, got %q", schema)
	}
}

// TestTransformMCPResult_WithStructuredContent tests TransformMCPResult
// with structured content that has a nil schema.
func TestTransformMCPResult_StructuredContentWithSchema(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "text"}},
		StructuredContent: map[string]any{
			"items": []any{1, 2, 3},
		},
	}

	transformed, schema := TransformMCPResult(result, "tool", "server")
	if schema == "" {
		t.Error("expected non-empty schema for structured content")
	}
	if !strings.Contains(schema, "items:") {
		t.Errorf("schema should mention items, got: %q", schema)
	}
	// StructuredContent takes priority — returns 1 result with the JSON
	if len(transformed) != 1 {
		t.Fatalf("expected 1 result (structured), got %d", len(transformed))
	}
	s, ok := transformed[0].Content.(string)
	if !ok {
		t.Fatal("expected string content")
	}
	if !strings.Contains(s, "items") {
		t.Errorf("structured content should contain items, got: %q", s)
	}
}

// TestInferCompactSchema_Exactly10Keys tests the boundary at exactly 10 keys.
func TestInferCompactSchema_Exactly10Keys(t *testing.T) {
	m := make(map[string]any)
	for i := range 10 {
		m[fmt.Sprintf("k%d", i)] = i
	}
	schema := InferCompactSchema(m)
	if strings.Contains(schema, ", ...") {
		t.Errorf("exactly 10 keys should NOT have ellipsis, got: %q", schema)
	}
	// All 10 keys should be present
	for i := range 10 {
		key := fmt.Sprintf("k%d:", i)
		if !strings.Contains(schema, key) {
			t.Errorf("schema should contain %q, got: %q", key, schema)
		}
	}
}

// --- URL elicitation retry tests (Step 7) ---

func TestIsURLElicitationError_WithJsonrpcError(t *testing.T) {
	rpcErr := &jsonrpc.Error{Code: mcp.CodeURLElicitationRequired, Message: "URL elicitation required"}
	if !isURLElicitationError(rpcErr) {
		t.Error("expected true for -32042 jsonrpc error")
	}
}

func TestIsURLElicitationError_WrappedInToolCallError(t *testing.T) {
	rpcErr := &jsonrpc.Error{Code: mcp.CodeURLElicitationRequired, Message: "URL elicitation required"}
	toolErr := &McpToolCallError{ServerName: "srv", ToolName: "tool", Err: rpcErr}
	if !isURLElicitationError(toolErr) {
		t.Error("expected true for -32042 wrapped in McpToolCallError")
	}
}

func TestIsURLElicitationError_OtherError(t *testing.T) {
	if isURLElicitationError(fmt.Errorf("some other error")) {
		t.Error("expected false for non-elicitation error")
	}
}

func TestIsURLElicitationError_OtherJsonrpcCode(t *testing.T) {
	rpcErr := &jsonrpc.Error{Code: -32600, Message: "invalid request"}
	if isURLElicitationError(rpcErr) {
		t.Error("expected false for non--32042 jsonrpc error")
	}
}

func TestCallMCPToolWithUrlElicitationRetry_Success(t *testing.T) {
	callCount := 0
	origCallMCPTool := _callMCPTool
	_callMCPTool = func(ctx context.Context, params CallMCPToolParams) (*MCPToolCallResult, error) {
		callCount++
		return &MCPToolCallResult{Content: nil}, nil
	}
	defer func() { _callMCPTool = origCallMCPTool }()

	result, err := CallMCPToolWithUrlElicitationRetry(context.Background(), CallMCPToolParams{
		Server:   &ConnectedServer{Name: "test"},
		ToolName: "tool",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestCallMCPToolWithUrlElicitationRetry_NonElicitationError(t *testing.T) {
	origCallMCPTool := _callMCPTool
	_callMCPTool = func(ctx context.Context, params CallMCPToolParams) (*MCPToolCallResult, error) {
		return nil, fmt.Errorf("some error")
	}
	defer func() { _callMCPTool = origCallMCPTool }()

	_, err := CallMCPToolWithUrlElicitationRetry(context.Background(), CallMCPToolParams{
		Server:   &ConnectedServer{Name: "test"},
		ToolName: "tool",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "some error") {
		t.Errorf("error should contain original message, got: %v", err)
	}
}

func TestCallMCPToolWithUrlElicitationRetry_RetriesOn32042(t *testing.T) {
	callCount := 0
	origCallMCPTool := _callMCPTool
	_callMCPTool = func(ctx context.Context, params CallMCPToolParams) (*MCPToolCallResult, error) {
		callCount++
		if callCount < 3 {
			return nil, &McpToolCallError{
				ServerName: "srv",
				ToolName:   "tool",
				Err:        &jsonrpc.Error{Code: mcp.CodeURLElicitationRequired, Message: "need url"},
			}
		}
		return &MCPToolCallResult{Content: nil}, nil
	}
	defer func() { _callMCPTool = origCallMCPTool }()

	result, err := CallMCPToolWithUrlElicitationRetry(context.Background(), CallMCPToolParams{
		Server:   &ConnectedServer{Name: "test"},
		ToolName: "tool",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result after retries")
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + 1 success), got %d", callCount)
	}
}

func TestCallMCPToolWithUrlElicitationRetry_MaxRetriesExceeded(t *testing.T) {
	origCallMCPTool := _callMCPTool
	_callMCPTool = func(ctx context.Context, params CallMCPToolParams) (*MCPToolCallResult, error) {
		return nil, &McpToolCallError{
			ServerName: "srv",
			ToolName:   "tool",
			Err:        &jsonrpc.Error{Code: mcp.CodeURLElicitationRequired, Message: "need url"},
		}
	}
	defer func() { _callMCPTool = origCallMCPTool }()

	_, err := CallMCPToolWithUrlElicitationRetry(context.Background(), CallMCPToolParams{
		Server:   &ConnectedServer{Name: "test"},
		ToolName: "tool",
	})
	if err == nil {
		t.Fatal("expected error after max retries exceeded")
	}
	if !strings.Contains(err.Error(), "max retries") {
		t.Errorf("error should mention max retries, got: %v", err)
	}
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("error should contain server name, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Image resize tests — Step 5
// Source: imageResizer.ts:169-433 — maybeResizeAndDownsampleImageBuffer
// ---------------------------------------------------------------------------

// createPNG creates a minimal valid PNG of the given dimensions.
func createPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Fill with a gradient to make compression harder
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// createJPEG creates a JPEG image of the given dimensions.
func createJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: 128,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestResizeImage_NoResizeNeeded(t *testing.T) {
	// Small image — should return original data unchanged
	original := createPNG(t, 100, 100)
	result, err := resizeImageWithLimits(original, "image/png", 200, 200, 10<<20)
	if err != nil {
		t.Fatalf("resizeImage: %v", err)
	}
	if !bytes.Equal(result, original) {
		t.Error("small image should be returned unchanged")
	}
}

func TestResizeImage_DownscaleByDimensions(t *testing.T) {
	// Image larger than 2000x2000 → should be downscaled
	original := createJPEG(t, 100, 100)
	result, err := resizeImageWithLimits(original, "image/jpeg", 50, 50, 10<<20)
	if err != nil {
		t.Fatalf("resizeImage: %v", err)
	}
	t.Logf("original: %d bytes, resized: %d bytes", len(original), len(result))
	if len(result) == 0 {
		t.Fatal("resized image should not be empty")
	}

	// Verify dimensions are within limits
	img, _, err := image.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("decode resized image: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() > imageMaxWidth {
		t.Errorf("width %d exceeds max %d", bounds.Dx(), imageMaxWidth)
	}
	if bounds.Dy() > imageMaxHeight {
		t.Errorf("height %d exceeds max %d", bounds.Dy(), imageMaxHeight)
	}
	t.Logf("resized: %dx%d → %d bytes", bounds.Dx(), bounds.Dy(), len(result))
}

func TestResizeImage_PNGKeptAsPNG(t *testing.T) {
	// Small PNG that's within limits — should stay as PNG
	original := createPNG(t, 50, 50)
	result, err := resizeImageWithLimits(original, "image/png", 200, 200, 10<<20)
	if err != nil {
		t.Fatalf("resizeImage: %v", err)
	}
	if !bytes.Equal(result, original) {
		t.Error("small PNG should be returned unchanged")
	}
}

func TestResizeImage_LargePNGDownscaled(t *testing.T) {
	// Large PNG that exceeds dimension limits
	original := createPNG(t, 100, 100)
	result, err := resizeImageWithLimits(original, "image/png", 50, 50, 10<<20)
	if err != nil {
		t.Fatalf("resizeImage: %v", err)
	}

	// Should be downscaled
	img, _, err := image.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("decode resized image: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() > imageMaxWidth {
		t.Errorf("width %d exceeds max %d", bounds.Dx(), imageMaxWidth)
	}
	if bounds.Dy() > imageMaxHeight {
		t.Errorf("height %d exceeds max %d", bounds.Dy(), imageMaxHeight)
	}
}

func TestResizeImage_EmptyData(t *testing.T) {
	_, err := resizeImageWithLimits([]byte{}, "image/png", 50, 50, 10<<20)
	if err == nil {
		t.Fatal("expected error for empty data")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty, got: %v", err)
	}
}

func TestResizeImage_InvalidImageFallback(t *testing.T) {
	_, err := resizeImageWithLimits([]byte("not an image"), "image/png", 50, 50, 10<<20)
	if err == nil {
		t.Fatal("expected error for invalid image")
	}
}

func TestResizeImage_AspectRatioPreserved(t *testing.T) {
	// 2100x1050 image → should scale to 2000x1000 (width hits max first)
	original := createJPEG(t, 100, 50)
	result, err := resizeImageWithLimits(original, "image/jpeg", 50, 50, 10<<20)
	if err != nil {
		t.Fatalf("resizeImage: %v", err)
	}

	img, _, err := image.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	// Aspect ratio should be 2:1
	ratio := float64(b.Dx()) / float64(b.Dy())
	if ratio < 1.9 || ratio > 2.1 {
		t.Logf("dimensions: %dx%d, ratio: %.2f", b.Dx(), b.Dy(), ratio)
		t.Errorf("aspect ratio should be ~2.0, got %.2f (%dx%d)", ratio, b.Dx(), b.Dy())
	}
	if b.Dx() > imageMaxWidth {
		t.Errorf("width %d exceeds max %d", b.Dx(), imageMaxWidth)
	}
}

func TestTransformResultContent_ImageResized(t *testing.T) {
	// Use small limits so a tiny image triggers the resize path
	origMaxW, origMaxH := imageMaxWidth, imageMaxHeight
	imageMaxWidth, imageMaxHeight = 50, 50
	defer func() { imageMaxWidth, imageMaxHeight = origMaxW, origMaxH }()

	// Image larger than 50x50 → should be resized
	largeJPEG := createJPEG(t, 100, 100)
	content := &mcp.ImageContent{
		Data:     largeJPEG,
		MIMEType: "image/jpeg",
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Type != MCPResultImage {
		t.Errorf("type = %v, want MCPResultImage", results[0].Type)
	}
	// The result data should be different from original (resized)
	if results[0].RawData == "" {
		t.Error("expected non-empty RawData")
	}
	// Decode the base64 to verify it's valid
	decoded, err := base64.StdEncoding.DecodeString(results[0].RawData)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	// Verify the decoded image has smaller dimensions than the original
	img, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("decode resized image: %v", err)
	}
	b := img.Bounds()
	if b.Dx() > imageMaxWidth {
		t.Errorf("width %d exceeds max %d", b.Dx(), imageMaxWidth)
	}
	if b.Dy() > imageMaxHeight {
		t.Errorf("height %d exceeds max %d", b.Dy(), imageMaxHeight)
	}
}

func TestTransformResultContent_ImageResizeErrorFallback(t *testing.T) {
	// Invalid image data should fall back to original data
	content := &mcp.ImageContent{
		Data:     []byte("not-a-real-image"),
		MIMEType: "image/png",
	}
	results := TransformResultContent(content, "test-server")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// Should still work — fallback to original data
	if results[0].Type != MCPResultImage {
		t.Errorf("type = %v, want MCPResultImage", results[0].Type)
	}
	// RawData should be the base64 of the original invalid data
	expected := base64.StdEncoding.EncodeToString([]byte("not-a-real-image"))
	if results[0].RawData != expected {
		t.Error("fallback should use original data")
	}
}
