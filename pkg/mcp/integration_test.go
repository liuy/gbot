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
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
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
	registry, err := LoadAndConnectMCP(context.Background(), tmpDir, provider, nil)
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

	// 6. Verify config watcher was started (hot-reload enabled)
	if registry.configWatcher == nil {
		t.Error("expected configWatcher to be started after LoadAndConnectMCP")
	} else {
		defer registry.configWatcher.Stop()
	}

	t.Logf("✓ wireup: .mcp.json → config → Registry → %d tools discovered, config watcher started", len(tools))
}

// TestIntegration_LoadAndConnectMCP_NoConfig verifies nil return when no .mcp.json exists.
func TestIntegration_LoadAndConnectMCP_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	registry, err := LoadAndConnectMCP(context.Background(), tmpDir, TransportFactory{}, nil)
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

	registry, err := LoadAndConnectMCP(context.Background(), tmpDir, TransportFactory{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if registry != nil {
		t.Error("expected nil registry when .mcp.json has no servers")
	}
}

// ---------------------------------------------------------------------------
// ListRoots integration tests
// Source: client.ts:1009-1018 — server calls ListRoots, client returns file://${workDir}
// ---------------------------------------------------------------------------

func TestIntegration_ListRoots_WithWorkDir(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()

	// Server side — capture ServerSession to call ListRoots from server→client
	server := mcp.NewServer(&mcp.Implementation{Name: "roots-server", Version: "1.0"}, nil)
	serverSessionCh := make(chan *mcp.ServerSession, 1)
	go func() {
		ss, _ := server.Connect(context.Background(), t1, nil)
		if ss != nil {
			serverSessionCh <- ss
		}
		close(serverSessionCh)
	}()

	// Client side — ClientManager with workDir set
	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["roots-srv"] = t2
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	mgr.SetWorkDir("/tmp/test-project")

	conn, err := mgr.ConnectToServer(context.Background(), "roots-srv", ScopedMcpServerConfig{
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
	defer func() { _ = cs.Close() }()

	// Server calls ListRoots on the client — this is the real MCP flow
	serverSession := <-serverSessionCh
	if serverSession == nil {
		t.Fatal("server session not established")
	}
	roots, err := serverSession.ListRoots(context.Background(), &mcp.ListRootsParams{})
	if err != nil {
		t.Fatalf("ListRoots: %v", err)
	}

	if len(roots.Roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(roots.Roots))
	}
	want := "file:///tmp/test-project"
	if roots.Roots[0].URI != want {
		t.Errorf("root URI = %q, want %q", roots.Roots[0].URI, want)
	}
}

func TestIntegration_ListRoots_EmptyWorkDir(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{Name: "roots-server", Version: "1.0"}, nil)
	serverSessionCh := make(chan *mcp.ServerSession, 1)
	go func() {
		ss, _ := server.Connect(context.Background(), t1, nil)
		if ss != nil {
			serverSessionCh <- ss
		}
		close(serverSessionCh)
	}()

	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["roots-srv"] = t2
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	// Don't set workDir — should result in empty roots

	conn, err := mgr.ConnectToServer(context.Background(), "roots-srv", ScopedMcpServerConfig{
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
	defer func() { _ = cs.Close() }()

	serverSession := <-serverSessionCh
	if serverSession == nil {
		t.Fatal("server session not established")
	}
	roots, err := serverSession.ListRoots(context.Background(), &mcp.ListRootsParams{})
	if err != nil {
		t.Fatalf("ListRoots: %v", err)
	}

	if len(roots.Roots) != 0 {
		t.Errorf("expected 0 roots with empty workDir, got %d", len(roots.Roots))
	}
}

// ---------------------------------------------------------------------------
// Sanitization integration tests — in-memory server with invisible chars
// ---------------------------------------------------------------------------

func TestIntegration_SanitizesToolNames(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "sanitize-test-server",
		Version: "1.0.0",
	}, nil)

	// Register a tool with invisible Unicode characters in name and description
	mcp.AddTool(server, &mcp.Tool{
		Name:        "tool\u200Bname",           // zero-width space in name
		Description: "a\uFEFFdescription\uE000", // BOM + private use in description
	}, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
		}, nil, nil
	})

	go func() {
		_, _ = server.Connect(context.Background(), t1, nil)
	}()

	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["sanitize-srv"] = t2
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	conn, err := mgr.ConnectToServer(context.Background(), "sanitize-srv", ScopedMcpServerConfig{
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
	defer func() { _ = cs.Close() }()

	tools, err := FetchToolsForServer(context.Background(), cs, NewLRUCache[string, []DiscoveredTool](fetchCacheCapacity))
	if err != nil {
		t.Fatalf("FetchToolsForServer: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	// Name should be sanitized: zero-width space removed
	if tools[0].OriginalName != "toolname" {
		t.Errorf("OriginalName = %q, want %q (invisible chars removed)", tools[0].OriginalName, "toolname")
	}
	if !strings.Contains(tools[0].Name, "toolname") {
		t.Errorf("Name = %q, should contain %q", tools[0].Name, "toolname")
	}

	// Description should be sanitized: BOM and private use removed
	if tools[0].Description != "adescription" {
		t.Errorf("Description = %q, want %q", tools[0].Description, "adescription")
	}
}

func TestIntegration_SanitizesResources(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "sanitize-resource-server",
		Version: "1.0.0",
	}, nil)

	// Register a resource with invisible chars
	server.AddResource(&mcp.Resource{
		URI:         "file:///path\u200B/to/file",
		Name:        "resource\uFEFFname",
		Description: "a\uE000desc",
		MIMEType:    "text/plain",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	})

	go func() {
		_, _ = server.Connect(context.Background(), t1, nil)
	}()

	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["sanitize-res-srv"] = t2
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	conn, err := mgr.ConnectToServer(context.Background(), "sanitize-res-srv", ScopedMcpServerConfig{
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
	defer func() { _ = cs.Close() }()

	resources, err := FetchResourcesForServer(context.Background(), cs, NewLRUCache[string, []ServerResource](fetchCacheCapacity))
	if err != nil {
		t.Fatalf("FetchResourcesForServer: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}

	if resources[0].URI != "file:///path/to/file" {
		t.Errorf("URI = %q, want %q", resources[0].URI, "file:///path/to/file")
	}
	if resources[0].Name != "resourcename" {
		t.Errorf("Name = %q, want %q", resources[0].Name, "resourcename")
	}
	if resources[0].Description != "adesc" {
		t.Errorf("Description = %q, want %q", resources[0].Description, "adesc")
	}
}

func TestIntegration_SanitizesCommands(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "sanitize-cmd-server",
		Version: "1.0.0",
	}, nil)

	// Register a prompt with invisible chars in name, description, and arg names
	server.AddPrompt(&mcp.Prompt{
		Name:        "cmd\u200Bname",
		Description: "a\uFEFFdesc",
		Arguments: []*mcp.PromptArgument{
			{Name: "arg\u200Bone", Description: "arg\uE000desc", Required: true},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{}, nil
	})

	go func() {
		_, _ = server.Connect(context.Background(), t1, nil)
	}()

	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["sanitize-cmd-srv"] = t2
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	conn, err := mgr.ConnectToServer(context.Background(), "sanitize-cmd-srv", ScopedMcpServerConfig{
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
	defer func() { _ = cs.Close() }()

	commands, err := FetchCommandsForServer(context.Background(), cs, NewLRUCache[string, []MCPCommand](fetchCacheCapacity))
	if err != nil {
		t.Fatalf("FetchCommandsForServer: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}

	if !strings.Contains(commands[0].Name, "cmdname") {
		t.Errorf("Name = %q, should contain %q", commands[0].Name, "cmdname")
	}
	if commands[0].Description != "adesc" {
		t.Errorf("Description = %q, want %q", commands[0].Description, "adesc")
	}
	if len(commands[0].Arguments) != 1 {
		t.Fatalf("expected 1 argument, got %d", len(commands[0].Arguments))
	}
	if commands[0].Arguments[0].Name != "argone" {
		t.Errorf("Arg Name = %q, want %q", commands[0].Arguments[0].Name, "argone")
	}
	if commands[0].Arguments[0].Description != "argdesc" {
		t.Errorf("Arg Description = %q, want %q", commands[0].Arguments[0].Description, "argdesc")
	}
}

// ---------------------------------------------------------------------------
// Elicitation chain test — full SDK transport round-trip
// ---------------------------------------------------------------------------

func TestIntegration_Elicitation_WithUI(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()

	// Server side
	server := mcp.NewServer(&mcp.Implementation{Name: "elicit-server", Version: "1.0"}, nil)
	serverSessionCh := make(chan *mcp.ServerSession, 1)
	go func() {
		ss, _ := server.Connect(context.Background(), t1, nil)
		if ss != nil {
			serverSessionCh <- ss
		}
		close(serverSessionCh)
	}()

	// Client side — with mock ElicitationUI
	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["elicit-srv"] = t2
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	mgr.SetElicitationUI(&mockElicitationUI{
		fn: func(ctx context.Context, serverName string, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			if serverName != "elicit-srv" {
				t.Errorf("serverName = %q, want %q", serverName, "elicit-srv")
			}
			if params.Message != "Please confirm" {
				t.Errorf("message = %q, want %q", params.Message, "Please confirm")
			}
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"key": "value"}}, nil
		},
	})

	conn, err := mgr.ConnectToServer(context.Background(), "elicit-srv", ScopedMcpServerConfig{
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
	defer func() { _ = cs.Close() }()

	// Server sends elicitation request → through real SDK transport → client handler → back
	serverSession := <-serverSessionCh
	if serverSession == nil {
		t.Fatal("server session not established")
	}
	result, err := serverSession.Elicit(context.Background(), &mcp.ElicitParams{
		Message: "Please confirm",
		Mode:    "form",
	})
	if err != nil {
		t.Fatalf("Elicit: %v", err)
	}
	if result.Action != "accept" {
		t.Errorf("action = %q, want %q", result.Action, "accept")
	}
}

// ---------------------------------------------------------------------------
// Sampling chain test — full SDK transport round-trip
// ---------------------------------------------------------------------------

func TestIntegration_Sampling_WithProvider(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()

	// Server side
	server := mcp.NewServer(&mcp.Implementation{Name: "sampling-server", Version: "1.0"}, nil)
	serverSessionCh := make(chan *mcp.ServerSession, 1)
	go func() {
		ss, _ := server.Connect(context.Background(), t1, nil)
		if ss != nil {
			serverSessionCh <- ss
		}
		close(serverSessionCh)
	}()

	// Client side — with mock SamplingProvider
	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["sampling-srv"] = t2
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	mgr.samplingModel = "test-model"
	mgr.SetSamplingProvider(&mockSamplingProvider{
		resp: &llm.Response{
			Content:    []types.ContentBlock{types.NewTextBlock("Sampled response")},
			Model:      "test-model",
			StopReason: "end_turn",
		},
	})

	conn, err := mgr.ConnectToServer(context.Background(), "sampling-srv", ScopedMcpServerConfig{
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
	defer func() { _ = cs.Close() }()

	// Server sends CreateMessage → through real SDK transport → client handler → mock provider → back
	serverSession := <-serverSessionCh
	if serverSession == nil {
		t.Fatal("server session not established")
	}
	result, err := serverSession.CreateMessage(context.Background(), &mcp.CreateMessageParams{
		MaxTokens: 100,
		Messages: []*mcp.SamplingMessage{
			{Role: "user", Content: &mcp.TextContent{Text: "Hello"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if result.Model != "test-model" {
		t.Errorf("model = %q, want %q", result.Model, "test-model")
	}
	if result.Role != "assistant" {
		t.Errorf("role = %q, want %q", result.Role, "assistant")
	}
	tc, ok := result.Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content)
	}
	if tc.Text != "Sampled response" {
		t.Errorf("text = %q, want %q", tc.Text, "Sampled response")
	}
}

// ---------------------------------------------------------------------------
// Config hot-reload chain test — config change → registry reloads → callbacks fire
// ---------------------------------------------------------------------------

func TestIntegration_ConfigReload_AddServer(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up first MCP server on in-memory transport
	t1a, t2a := mcp.NewInMemoryTransports()
	server1 := mcp.NewServer(&mcp.Implementation{Name: "srv1", Version: "1.0"}, nil)
	mcp.AddTool(server1, &mcp.Tool{Name: "tool1", Description: "Tool one"}, echoHandler)
	go func() { _, _ = server1.Connect(context.Background(), t1a, nil) }()

	// Create registry with in-memory provider
	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["srv1"] = t2a
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	registry := NewRegistry(mgr, ChangeCallbacks{})
	registry.configDir = tmpDir
	defer registry.Close()

	// Write initial config with only srv1
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"srv1":{"command":"echo","args":["hello"]}}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Load initial config
	configs, _ := GetProjectMcpConfigsFromCwd(tmpDir)
	registry.ConnectAll(context.Background(), configs)

	// Verify srv1 connected
	registry.mu.RLock()
	_, hasSrv1 := registry.connections["srv1"]
	registry.mu.RUnlock()
	if !hasSrv1 {
		t.Fatal("srv1 should be connected after initial load")
	}

	// Set up second MCP server
	t1b, t2b := mcp.NewInMemoryTransports()
	server2 := mcp.NewServer(&mcp.Implementation{Name: "srv2", Version: "1.0"}, nil)
	mcp.AddTool(server2, &mcp.Tool{Name: "tool2", Description: "Tool two"}, echoHandler)
	go func() { _, _ = server2.Connect(context.Background(), t1b, nil) }()

	// Register srv2 transport with provider
	provider.mu.Lock()
	provider.transports["srv2"] = t2b
	provider.mu.Unlock()

	// Modify config — add srv2
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"srv1":{"command":"echo","args":["hello"]},"srv2":{"command":"echo","args":["world"]}}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Trigger reload
	registry.handleConfigReload()

	// Verify srv2 was added and srv1 still exists
	registry.mu.RLock()
	_, hasSrv1After := registry.connections["srv1"]
	_, hasSrv2 := registry.connections["srv2"]
	registry.mu.RUnlock()
	if !hasSrv1After {
		t.Error("srv1 should still be connected after reload")
	}
	if !hasSrv2 {
		t.Error("srv2 should be connected after config reload")
	}
}

func TestIntegration_ConfigReload_RemoveServer(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up two MCP servers on in-memory transports
	t1a, t2a := mcp.NewInMemoryTransports()
	server1 := mcp.NewServer(&mcp.Implementation{Name: "srv1", Version: "1.0"}, nil)
	mcp.AddTool(server1, &mcp.Tool{Name: "tool1", Description: "Tool one"}, echoHandler)
	go func() { _, _ = server1.Connect(context.Background(), t1a, nil) }()

	t1b, t2b := mcp.NewInMemoryTransports()
	server2 := mcp.NewServer(&mcp.Implementation{Name: "srv2", Version: "1.0"}, nil)
	mcp.AddTool(server2, &mcp.Tool{Name: "tool2", Description: "Tool two"}, echoHandler)
	go func() { _, _ = server2.Connect(context.Background(), t1b, nil) }()

	// Create registry with both transports pre-registered
	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["srv1"] = t2a
	provider.transports["srv2"] = t2b
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	registry := NewRegistry(mgr, ChangeCallbacks{})
	registry.configDir = tmpDir
	defer registry.Close()

	// Write initial config with both servers
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"srv1":{"command":"echo","args":["hello"]},"srv2":{"command":"echo","args":["world"]}}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Load initial config
	configs, _ := GetProjectMcpConfigsFromCwd(tmpDir)
	registry.ConnectAll(context.Background(), configs)

	// Verify both connected
	registry.mu.RLock()
	_, hasSrv1 := registry.connections["srv1"]
	_, hasSrv2 := registry.connections["srv2"]
	registry.mu.RUnlock()
	if !hasSrv1 || !hasSrv2 {
		t.Fatal("both servers should be connected initially")
	}

	// Remove srv2 from config
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"srv1":{"command":"echo","args":["hello"]}}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Trigger reload
	registry.handleConfigReload()

	// Verify srv2 was removed but srv1 still exists
	registry.mu.RLock()
	_, hasSrv1After := registry.connections["srv1"]
	_, hasSrv2After := registry.connections["srv2"]
	registry.mu.RUnlock()
	if !hasSrv1After {
		t.Error("srv1 should still be connected")
	}
	if hasSrv2After {
		t.Error("srv2 should be disconnected after removal from config")
	}
}

// ---------------------------------------------------------------------------
// Config reload: plugin servers must survive reload (only project .mcp.json is re-read)
// ---------------------------------------------------------------------------

func TestIntegration_ConfigReload_PreservesPluginServers(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up plugin MCP server on in-memory transport
	t1a, t2a := mcp.NewInMemoryTransports()
	pluginSrv := mcp.NewServer(&mcp.Implementation{Name: "plugin-srv", Version: "1.0"}, nil)
	mcp.AddTool(pluginSrv, &mcp.Tool{Name: "plugin_tool", Description: "Plugin tool"}, echoHandler)
	go func() { _, _ = pluginSrv.Connect(context.Background(), t1a, nil) }()

	// Set up project MCP server on in-memory transport
	t1b, t2b := mcp.NewInMemoryTransports()
	projectSrv := mcp.NewServer(&mcp.Implementation{Name: "project-srv", Version: "1.0"}, nil)
	mcp.AddTool(projectSrv, &mcp.Tool{Name: "project_tool", Description: "Project tool"}, echoHandler)
	go func() { _, _ = projectSrv.Connect(context.Background(), t1b, nil) }()

	provider := newInMemoryProvider()
	provider.mu.Lock()
	provider.transports["plugin:some-plugin:srv"] = t2a
	provider.transports["project-srv"] = t2b
	provider.mu.Unlock()

	mgr := NewClientManager(provider, true, "")
	registry := NewRegistry(mgr, ChangeCallbacks{})
	registry.configDir = tmpDir
	defer registry.Close()

	// Write project config with only the project server
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"project-srv":{"command":"echo"}}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Initial load: project config + plugin server
	projectConfigs, _ := GetProjectMcpConfigsFromCwd(tmpDir)
	allConfigs := make(map[string]ScopedMcpServerConfig)
	maps.Copy(allConfigs, projectConfigs)
	// Add plugin server (simulating what main.go does via LoadAndConnectMCP)
	allConfigs["plugin:some-plugin:srv"] = ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		Scope:        ScopeDynamic,
		PluginSource: "plugin:some-plugin:srv",
	}
	registry.ConnectAll(context.Background(), allConfigs)

	// Verify both connected
	registry.mu.RLock()
	_, hasPlugin := registry.connections["plugin:some-plugin:srv"]
	_, hasProject := registry.connections["project-srv"]
	registry.mu.RUnlock()
	if !hasPlugin {
		t.Fatal("plugin server should be connected after initial load")
	}
	if !hasProject {
		t.Fatal("project server should be connected after initial load")
	}

	// Trigger config reload (project .mcp.json unchanged, but reload fires)
	registry.handleConfigReload()

	// Verify plugin server is STILL connected (not removed by reload)
	registry.mu.RLock()
	_, hasPluginAfter := registry.connections["plugin:some-plugin:srv"]
	_, hasProjectAfter := registry.connections["project-srv"]
	registry.mu.RUnlock()
	if !hasPluginAfter {
		t.Error("plugin server should survive config reload — it must be preserved")
	}
	if !hasProjectAfter {
		t.Error("project server should still be connected after reload")
	}
}

// ---------------------------------------------------------------------------
// StartConfigWatch: should not watch when no .mcp.json exists
// ---------------------------------------------------------------------------

func TestStartConfigWatch_NoConfigFile_NoWatcher(t *testing.T) {
	tmpDir := t.TempDir()
	// No .mcp.json created — watcher should not start

	registry := NewRegistry(nil, ChangeCallbacks{})
	registry.configDir = tmpDir
	defer registry.Close()

	err := registry.StartConfigWatch()
	if err != nil {
		t.Fatalf("StartConfigWatch should not error: %v", err)
	}

	if registry.configWatcher != nil {
		t.Error("configWatcher should be nil when no .mcp.json exists — no file to watch")
	}
}

func TestStartConfigWatch_WithConfigFile_StartsWatcher(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	registry := NewRegistry(nil, ChangeCallbacks{})
	registry.configDir = tmpDir
	defer registry.Close()

	err := registry.StartConfigWatch()
	if err != nil {
		t.Fatalf("StartConfigWatch should not error: %v", err)
	}

	if registry.configWatcher == nil {
		t.Error("configWatcher should be created when .mcp.json exists")
	}
}
