// Package mcp — integration tests with real MCP tool handlers.
// Tests the full stack: Connect → Discover → CallTool → TransformResult.
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Tool handlers — real implementations, not mocks
// ---------------------------------------------------------------------------

// echoHandler returns the input text as-is.
func echoHandler(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
	text, _ := args["text"].(string)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, nil, nil
}

// generateImageHandler creates a gradient PNG image of given size.
func generateImageHandler(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
	width := 100
	height := 100
	if w, ok := args["width"].(float64); ok {
		width = int(w)
	}
	if h, ok := args["height"].(float64); ok {
		height = int(h)
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r := uint8(x * 255 / width)
			g := uint8(y * 255 / height)
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: 128, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, nil, fmt.Errorf("encode: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.ImageContent{
			Data:     buf.Bytes(),
			MIMEType: "image/png",
		}},
	}, nil, nil
}

// addHandler adds two numbers.
func addHandler(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
	a, _ := args["a"].(float64)
	b, _ := args["b"].(float64)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%.0f", a+b)}},
	}, nil, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupServerWithTools creates an in-memory MCP server with real tool handlers.
func setupServerWithTools(t *testing.T) (*mcp.Server, mcp.Transport) {
	t.Helper()
	t1, t2 := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "integration-test-server",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echoes back the input text",
	}, echoHandler)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "generate_image",
		Description: "Generates a gradient PNG image of specified dimensions",
	}, generateImageHandler)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add",
		Description: "Adds two numbers",
	}, addHandler)

	go func() {
		_, _ = server.Connect(context.Background(), t1, nil)
	}()

	return server, t2
}

// connectTestServerWithTools wires up a full client connection to the tool server.
func connectTestServerWithTools(t *testing.T, name string) (*ConnectedServer, func()) {
	t.Helper()
	_, transport := setupServerWithTools(t)

	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports[name] = transport
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	ctx := context.Background()

	conn, err := mgr.ConnectToServer(ctx, name, ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "test"},
		Scope:  ScopeUser,
	})
	if err != nil {
		t.Fatalf("ConnectToServer: %v", err)
	}
	cs, ok := conn.(*ConnectedServer)
	if !ok {
		t.Fatal("expected *ConnectedServer")
	}
	return cs, func() { _ = cs.Close() }
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestIntegration_EchoTool(t *testing.T) {
	cs, cleanup := connectTestServerWithTools(t, "echo-srv")
	defer cleanup()
	ctx := context.Background()

	// Discover tools
	tools, err := FetchToolsForServer(ctx, cs, NewLRUCache[string, []DiscoveredTool](fetchCacheCapacity))
	if err != nil {
		t.Fatalf("FetchTools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	// Call echo tool
	result, err := CallMCPTool(ctx, CallMCPToolParams{
		Server:   cs,
		ToolName: "echo",
		Args:     map[string]any{"text": "Hello MCP!"},
	})
	if err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}

	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if tc.Text != "Hello MCP!" {
		t.Errorf("echo = %q, want %q", tc.Text, "Hello MCP!")
	}
	t.Logf("✓ echo tool: %q", tc.Text)
}

func TestIntegration_ImageGeneration(t *testing.T) {
	origMaxW, origMaxH := imageMaxWidth, imageMaxHeight
	imageMaxWidth, imageMaxHeight = 50, 50
	defer func() { imageMaxWidth, imageMaxHeight = origMaxW, origMaxH }()

	cs, cleanup := connectTestServerWithTools(t, "img-srv")
	defer cleanup()
	ctx := context.Background()

	// Generate image larger than limits → triggers resize
	result, err := CallMCPTool(ctx, CallMCPToolParams{
		Server:   cs,
		ToolName: "generate_image",
		Args:     map[string]any{"width": float64(100), "height": float64(100)},
	})
	if err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}

	imgContent, ok := result.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("expected ImageContent, got %T", result.Content[0])
	}
	t.Logf("✓ generated image: %d bytes, mime=%s", len(imgContent.Data), imgContent.MIMEType)

	// Transform result — should resize the large image
	transformed := TransformResultContent(imgContent, "img-srv")
	if len(transformed) != 1 {
		t.Fatalf("expected 1 transformed result, got %d", len(transformed))
	}
	if transformed[0].Type != MCPResultImage {
		t.Errorf("type = %v, want MCPResultImage", transformed[0].Type)
	}

	// Decode base64 and verify dimensions are within limits
	decoded, err := base64.StdEncoding.DecodeString(transformed[0].RawData)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("decode resized image: %v", err)
	}
	b := img.Bounds()
	t.Logf("✓ image resized: 3000x3000 → %dx%d (%d bytes → %d bytes)",
		b.Dx(), b.Dy(), len(imgContent.Data), len(decoded))

	if b.Dx() > imageMaxWidth || b.Dy() > imageMaxHeight {
		t.Errorf("resized image %dx%d exceeds limits %dx%d", b.Dx(), b.Dy(), imageMaxWidth, imageMaxHeight)
	}
}

func TestIntegration_MultiToolWorkflow(t *testing.T) {
	cs, cleanup := connectTestServerWithTools(t, "workflow-srv")
	defer cleanup()
	ctx := context.Background()

	// Discover tools
	tools, err := FetchToolsForServer(ctx, cs, NewLRUCache[string, []DiscoveredTool](fetchCacheCapacity))
	if err != nil {
		t.Fatalf("FetchTools: %v", err)
	}
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.OriginalName] = true
	}
	for _, name := range []string{"echo", "generate_image", "add"} {
		if !toolNames[name] {
			t.Fatalf("tool %q not found in discovered tools", name)
		}
	}

	// Step 1: Add 42 + 58
	addResult, err := CallMCPTool(ctx, CallMCPToolParams{
		Server:   cs,
		ToolName: "add",
		Args:     map[string]any{"a": float64(42), "b": float64(58)},
	})
	if err != nil {
		t.Fatalf("CallMCPTool add: %v", err)
	}
	sumText := addResult.Content[0].(*mcp.TextContent).Text
	if sumText != "100" {
		t.Errorf("add(42,58) = %q, want %q", sumText, "100")
	}
	t.Logf("✓ step 1: 42 + 58 = %s", sumText)

	// Step 2: Echo the result
	echoResult, err := CallMCPTool(ctx, CallMCPToolParams{
		Server:   cs,
		ToolName: "echo",
		Args:     map[string]any{"text": fmt.Sprintf("The answer is %s!", sumText)},
	})
	if err != nil {
		t.Fatalf("CallMCPTool echo: %v", err)
	}
	echoText := echoResult.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(echoText, "100") {
		t.Errorf("echo result = %q, should contain '100'", echoText)
	}
	t.Logf("✓ step 2: echo = %q", echoText)

	// Step 3: Generate a celebration image
	imgResult, err := CallMCPTool(ctx, CallMCPToolParams{
		Server:   cs,
		ToolName: "generate_image",
		Args:     map[string]any{"width": float64(50), "height": float64(50)},
	})
	if err != nil {
		t.Fatalf("CallMCPTool generate_image: %v", err)
	}
	imgData := imgResult.Content[0].(*mcp.ImageContent).Data
	if len(imgData) == 0 {
		t.Error("generated image should not be empty")
	}
	t.Logf("✓ step 3: celebration image (%d bytes)", len(imgData))
}

// ---------------------------------------------------------------------------
// Wireup integration test: .mcp.json → config → Registry → tools
// Tests the full startup path that main.go should follow.
// RED: calls LoadAndConnectMCP which doesn't exist yet.
// ---------------------------------------------------------------------------

// TestIntegration_LoadAndConnectMCP verifies the full startup wireup:
// write .mcp.json → load configs → create Registry → connect → discover tools.
//
// This tests the function that main.go should call. Currently RED because
// LoadAndConnectMCP doesn't exist.
func TestIntegration_LoadAndConnectMCP(t *testing.T) {
	// 1. Write a .mcp.json to a temp directory
	tmpDir := t.TempDir()
	mcpJSON := map[string]any{
		"mcpServers": map[string]any{
			"echo-srv": map[string]any{
				"command": "echo",
				"args":    []string{"test"},
			},
		},
	}
	data, err := json.Marshal(mcpJSON)
	if err != nil {
		t.Fatalf("marshal .mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	// 2. Create an in-memory MCP server with a hello tool
	t1, t2 := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "wireup-test-server",
		Version: "1.0.0",
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hello",
		Description: "Says hello",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Hello from MCP!"}},
		}, nil, nil
	})
	go func() {
		_, _ = server.Connect(context.Background(), t1, nil)
	}()

	// 3. Create a provider that returns our in-memory transport for "echo-srv"
	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["echo-srv"] = t2
	provider.mu.Unlock()

	// 4. Call the wireup function (doesn't exist yet → compile error = RED)
	registry, err := LoadAndConnectMCP(context.Background(), tmpDir, provider)
	if err != nil {
		t.Fatalf("LoadAndConnectMCP: %v", err)
	}
	if registry == nil {
		t.Fatal("expected non-nil registry")
	}
	defer registry.Close()

	// 5. Verify tools were discovered
	tools := registry.GetTools()
	if len(tools) == 0 {
		t.Fatal("expected tools to be discovered from connected server")
	}

	foundHello := false
	for _, dt := range tools {
		if dt.OriginalName == "hello" {
			foundHello = true
			if dt.ServerName != "echo-srv" {
				t.Errorf("server name = %q, want %q", dt.ServerName, "echo-srv")
			}
			break
		}
	}
	if !foundHello {
		t.Error("expected 'hello' tool to be discovered")
	}

	t.Logf("✓ wireup: .mcp.json → config → Registry → %d tools discovered", len(tools))
}

// TestIntegration_LoadAndConnectMCP_NoConfig verifies nil return when no .mcp.json exists.
func TestIntegration_LoadAndConnectMCP_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	registry, err := LoadAndConnectMCP(context.Background(), tmpDir, TransportFactory{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if registry != nil {
		t.Error("expected nil registry when no .mcp.json exists")
	}
}

// TestIntegration_LoadAndConnectMCP_EmptyConfig verifies nil return when .mcp.json has no servers.
func TestIntegration_LoadAndConnectMCP_EmptyConfig(t *testing.T) {
	tmpDir := t.TempDir()
	data, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{},
	})
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	registry, err := LoadAndConnectMCP(context.Background(), tmpDir, TransportFactory{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if registry != nil {
		t.Error("expected nil registry when .mcp.json has no servers")
	}
}
