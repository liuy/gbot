package engine

// End-to-end death-recovery through MCPTool.Call: after the server process
// dies, the next call must transparently re-establish the connection (lazy
// respawn), and a FailedServer entry must be treated like a missing
// connection instead of a permanent "not connected" error.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	// Re-exec'd helper mode: serve as a minimal MCP stdio server and exit
	// before any test selection.
	if os.Getenv("GBOT_TEST_MCP_HELPER") == "1" {
		mcpDeathHelper()
		return // unreachable: mcpDeathHelper exits the process
	}
	os.Exit(m.Run())
}

func mcpDeathHelper() {
	pidFile := os.Getenv("GBOT_TEST_MCP_PID_FILE")
	mode := os.Getenv("GBOT_TEST_MCP_MODE")
	if pidFile != "" {
		f, err := os.OpenFile(pidFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			if _, werr := fmt.Fprintf(f, "%d\n", os.Getpid()); werr != nil {
				os.Exit(1)
			}
			if cerr := f.Close(); cerr != nil {
				os.Exit(1)
			}
		}
	}
	if mode == "die-first" {
		if data, _ := os.ReadFile(pidFile); strings.Count(string(data), "\n") <= 1 {
			os.Exit(0)
		}
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "death-helper", Version: "v1"}, nil)
	srv.AddTool(&sdk.Tool{Name: "ping", Description: "reply pong:<pid>", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("pong:%d", os.Getpid())}},
			}, nil
		})
	ss, err := srv.Connect(context.Background(), &sdk.StdioTransport{}, nil)
	if err != nil {
		os.Exit(1)
	}
	if werr := ss.Wait(); werr != nil { // blocks until stdin closes (client gone)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestHelperNeverRuns(t *testing.T) {
	if os.Getenv("GBOT_TEST_MCP_HELPER") == "1" {
		t.Fatal("helper mode must exit in TestMain before running tests")
	}
}

func newDeathHelperRegistry(t *testing.T, pidFile, mode string) *mcp.Registry {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	tmpDir := t.TempDir()
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"helper": map[string]any{
			"command": exe,
			"args":    []string{"-test.run=TestHelperNeverRuns"},
			"env": map[string]string{
				"GBOT_TEST_MCP_HELPER":   "1",
				"GBOT_TEST_MCP_PID_FILE": pidFile,
				"GBOT_TEST_MCP_MODE":     mode,
			},
		},
	}})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	reg, err := mcp.LoadAndConnectMCP(context.Background(), tmpDir, mcp.TransportFactory{}, nil)
	if err != nil {
		t.Fatalf("LoadAndConnectMCP: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func waitDeathHelperPIDs(t *testing.T, pidFile string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second) // REAL-TIME: helper spawn + MCP handshake
	for {
		if data, err := os.ReadFile(pidFile); err == nil {
			var pids []int
			for line := range strings.FieldsSeq(string(data)) {
				var pid int
				if _, err := fmt.Sscanf(line, "%d", &pid); err == nil {
					pids = append(pids, pid)
				}
			}
			if len(pids) >= n {
				return pids
			}
		}
		if time.Now().After(deadline) { // REAL-TIME: bounded poll
			t.Fatalf("timeout waiting for %d helper spawn(s) in %s", n, pidFile)
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: poll interval
	}
}

func callPing(t *testing.T, tool *MCPTool) string {
	t.Helper()
	res, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("ping call: %v", err)
	}
	s, ok := res.Data.(string)
	if !ok {
		t.Fatalf("ping result should be string, got %T", res.Data)
	}
	return s
}

func TestStdioCallAfterServerDeathRecovers(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newDeathHelperRegistry(t, pidFile, "serve")
	pids := waitDeathHelperPIDs(t, pidFile, 1)

	tool := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__helper__ping",
		OriginalName: "ping",
		ServerName:   "helper",
	}, reg)

	first := callPing(t, tool)
	if first != fmt.Sprintf("pong:%d", pids[0]) {
		t.Fatalf("first ping should carry the helper pid, got %q (pid %d)", first, pids[0])
	}
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper: %v", err)
	}

	// The call after death must transparently recover against a NEW process.
	second, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("ping after server death must recover, got: %v", err)
	}
	secondText, ok := second.Data.(string)
	if !ok {
		t.Fatalf("recovered ping result should be string, got %T", second.Data)
	}
	if secondText == first || !strings.HasPrefix(secondText, "pong:") {
		t.Fatalf("recovered ping must hit a new helper process, got %q (first was %q)", secondText, first)
	}
	newPIDs := waitDeathHelperPIDs(t, pidFile, 2)
	if secondText != fmt.Sprintf("pong:%d", newPIDs[1]) {
		t.Fatalf("recovered ping should carry the respawned pid, got %q want %q", secondText, fmt.Sprintf("pong:%d", newPIDs[1]))
	}
}

func TestCallRecoversFromFailedServer(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	// die-first: the first spawn exits before serving, so startup stores a
	// FailedServer; the next call must treat it like a missing connection.
	reg := newDeathHelperRegistry(t, pidFile, "die-first")
	waitDeathHelperPIDs(t, pidFile, 1)

	conn, ok := reg.GetConnection("helper")
	if !ok {
		t.Fatal("FailedServer entry should be present after failed connect")
	}
	if conn.ConnType() != "failed" {
		t.Fatalf("expected failed state after die-first spawn, got %s", conn.ConnType())
	}

	tool := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__helper__ping",
		OriginalName: "ping",
		ServerName:   "helper",
	}, reg)

	// A FailedServer entry must not be permanent: one Reconnect spawns the
	// second helper generation and the call succeeds against it.
	got := callPing(t, tool)
	pids := waitDeathHelperPIDs(t, pidFile, 2)
	if got != fmt.Sprintf("pong:%d", pids[1]) {
		t.Fatalf("call must recover from FailedServer against the respawned helper, got %q want %q", got, fmt.Sprintf("pong:%d", pids[1]))
	}
}

func TestRemoteCallAfterServerDeathRecovers(t *testing.T) {
	// Remote (streamable HTTP) servers are request-driven: death is only
	// discovered on the next call. The classifier + evict + reconnect +
	// retry path must recover transparently once the server is back.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	serveGen := func(l net.Listener, gen int) *http.Server {
		sdkSrv := sdk.NewServer(&sdk.Implementation{Name: "remote-helper", Version: "v1"}, nil)
		sdkSrv.AddTool(&sdk.Tool{Name: "ping", Description: "pong with generation", InputSchema: map[string]any{"type": "object"}},
			func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("pong:%d", gen)}}}, nil
			})
		handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return sdkSrv }, nil)
		srv := &http.Server{Handler: handler}
		go func() { _ = srv.Serve(l) }()
		return srv
	}
	srv1 := serveGen(ln, 1)
	t.Cleanup(func() { _ = srv1.Close() })

	tmpDir := t.TempDir()
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"remote": map[string]any{"type": "http", "url": "http://" + addr},
	}})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	reg, err := mcp.LoadAndConnectMCP(context.Background(), tmpDir, mcp.TransportFactory{}, nil)
	if err != nil {
		t.Fatalf("LoadAndConnectMCP: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	tool := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__remote__ping",
		OriginalName: "ping",
		ServerName:   "remote",
	}, reg)
	first := callPing(t, tool)
	if first != "pong:1" {
		t.Fatalf("first remote ping should hit server generation 1, got %q", first)
	}
	if err := srv1.Close(); err != nil {
		t.Fatalf("close remote generation 1: %v", err)
	}
	// Restart on the SAME port (generation 2).
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("re-listen on %s: %v", addr, err)
	}
	srv2 := serveGen(ln2, 2)
	t.Cleanup(func() { _ = srv2.Close() })

	second, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("remote ping after server death must recover, got: %v", err)
	}
	s, ok := second.Data.(string)
	if !ok {
		t.Fatalf("recovered remote ping result should be string, got %T", second.Data)
	}
	if s != "pong:2" {
		t.Fatalf("recovered remote ping must hit the restarted server generation, got %q", s)
	}
}

func TestCallNonDeadErrorDoesNotRetry(t *testing.T) {
	// An ordinary tool error (unknown tool name) must NOT engage the
	// dead-connection reconnect path.
	cs := setupInMemoryEchoServer(t)
	registry := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	defer registry.Close()
	registry.SetConnectionForTest("echo-srv", cs)
	tl := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__echo-srv__nope",
		OriginalName: "no-such-tool",
		ServerName:   "echo-srv",
	}, registry)
	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("unknown tool must error")
	}
	if strings.Contains(err.Error(), "connection died") {
		t.Errorf("ordinary tool error must not be wrapped as connection-died: %v", err)
	}
}

func TestRemoteCallReconnectFailureSurfaces(t *testing.T) {
	// Remote server dies and stays down: the dead-connection retry must
	// surface both the original error and the reconnect failure.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	sdkSrv := sdk.NewServer(&sdk.Implementation{Name: "remote-down", Version: "v1"}, nil)
	sdkSrv.AddTool(&sdk.Tool{Name: "ping", Description: "pong", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return sdkSrv }, nil)
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	tmpDir := t.TempDir()
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"remote": map[string]any{"type": "http", "url": "http://" + addr},
	}})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	reg, err := mcp.LoadAndConnectMCP(context.Background(), tmpDir, mcp.TransportFactory{}, nil)
	if err != nil {
		t.Fatalf("LoadAndConnectMCP: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	tool := NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__remote__ping",
		OriginalName: "ping",
		ServerName:   "remote",
	}, reg)
	callPing(t, tool)
	if err := srv.Close(); err != nil {
		t.Fatalf("close remote: %v", err)
	}
	_, err = tool.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("call against permanently dead remote must fail")
	}
	if !strings.Contains(err.Error(), "connection died") || !strings.Contains(err.Error(), "reconnect returned state failed") {
		t.Errorf("error must surface dead connection + failed reconnect state, got: %v", err)
	}
}
