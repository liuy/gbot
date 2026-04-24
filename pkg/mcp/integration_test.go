// Package mcp — integration tests with real MCP tool handlers.
// Tests the full stack: Connect → Discover → CallTool → TransformResult.
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
