package mcp

// Death-recovery tests: when an MCP server process dies, the stale
// ConnectedServer must be evicted so the next call re-establishes it lazily.
// The tool table is config-driven: an evicted or failed server keeps its
// last-known tool list listed, so the recovery entry point (the tool call)
// stays reachable — the engine rebuilds its tool table from GetTools() per
// call. Concurrent recovery attempts must collapse into one spawn (no fork
// storm, no orphan holding profile locks).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// helperMain runs inside the re-exec'd test binary as a minimal MCP stdio
// server. Modes: "serve" (default) and "die-first" (exit before serving on
// the first spawn only — detected via the pid file holding exactly one line).
func helperMain() {
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
	if mode == "die-on-respawn" {
		// Any spawn after the first must die before serving, so a reconnect
		// attempt fails and stores a FailedServer while the initial spawn's
		// discovery caches stay populated.
		if data, _ := os.ReadFile(pidFile); strings.Count(string(data), "\n") >= 2 {
			os.Exit(0)
		}
	}
	if mode == "grow-tool" {
		// Respawns expose one extra tool, proving reconnect re-fetches fresh
		// discovery data instead of serving the stale cache.
		if data, _ := os.ReadFile(pidFile); strings.Count(string(data), "\n") >= 2 {
			if err := os.Setenv("GBOT_TEST_MCP_GROW", "1"); err != nil {
				fmt.Fprintln(os.Stderr, "setenv grow:", err)
			}
		}
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "death-helper", Version: "v1"}, nil)
	srv.AddTool(&sdk.Tool{Name: "ping", Description: "reply pong:<pid>", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("pong:%d", os.Getpid())}},
			}, nil
		})
	if os.Getenv("GBOT_TEST_MCP_GROW") == "1" {
		srv.AddTool(&sdk.Tool{Name: "pong2", Description: "grown on respawn", InputSchema: map[string]any{"type": "object"}},
			func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong2"}}}, nil
			})
	}
	// A prompt and a resource populate the command/resource discovery caches,
	// exercising the non-tool aggregate unions in rebuildAggregatesLocked.
	srv.AddPrompt(&sdk.Prompt{Name: "greet", Description: "greeting prompt"}, func(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
		return &sdk.GetPromptResult{}, nil
	})
	srv.AddResource(&sdk.Resource{URI: "helper://doc", Name: "doc", MIMEType: "text/plain"}, func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{}, nil
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

// TestHelperNeverRuns is the re-exec target; helper mode exits in TestMain
// before test selection, so this must never execute in helper processes.
func TestHelperNeverRuns(t *testing.T) {
	if os.Getenv("GBOT_TEST_MCP_HELPER") == "1" {
		t.Fatal("helper mode must exit in TestMain before running tests")
	}
}

func helperConfig(t *testing.T, pidFile, mode string) map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return map[string]any{
		"command": exe,
		"args":    []string{"-test.run=TestHelperNeverRuns"},
		"env": map[string]string{
			"GBOT_TEST_MCP_HELPER":   "1",
			"GBOT_TEST_MCP_PID_FILE": pidFile,
			"GBOT_TEST_MCP_MODE":     mode,
		},
	}
}

func newHelperRegistry(t *testing.T, servers map[string]any) *Registry {
	t.Helper()
	tmpDir := t.TempDir()
	data, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	reg, err := LoadAndConnectMCP(context.Background(), tmpDir, TransportFactory{}, nil)
	if err != nil {
		t.Fatalf("LoadAndConnectMCP: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func readHelperPIDs(t *testing.T, pidFile string) []int {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return nil
	}
	var pids []int
	for line := range strings.FieldsSeq(string(data)) {
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func waitForHelperPIDs(t *testing.T, pidFile string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second) // REAL-TIME: helper spawn + MCP handshake
	for {
		if pids := readHelperPIDs(t, pidFile); len(pids) >= n {
			return pids
		}
		if time.Now().After(deadline) { // REAL-TIME: bounded poll
			t.Fatalf("timeout waiting for %d helper spawn(s) in %s", n, pidFile)
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: poll interval
	}
}

func helperPidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestStdioServerDeathEvictsConnection(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newHelperRegistry(t, map[string]any{"helper": helperConfig(t, pidFile, "serve")})

	pids := waitForHelperPIDs(t, pidFile, 1)
	conn, ok := reg.GetConnection("helper")
	if !ok {
		t.Fatal("helper connection should be present after connect")
	}
	if _, ok := conn.(*ConnectedServer); !ok {
		t.Fatalf("expected ConnectedServer, got %s", conn.ConnType())
	}
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	// The call path evicts the stale entry when the next call classifies the
	// dead connection; simulate that eviction directly (same function the
	// call path uses).
	if !reg.EvictIfSame("helper", conn.(*ConnectedServer)) {
		t.Fatal("EvictIfSame must evict the killed helper's connection")
	}
	if _, ok := reg.GetConnection("helper"); ok {
		t.Fatal("stale connection was not evicted after server death")
	}
	// fork-storm discipline: stdio death must NOT schedule a timer reconnect.
	reg.mu.RLock()
	timer := reg.reconnectTimers["helper"]
	reg.mu.RUnlock()
	if timer != nil {
		t.Error("stdio server death must not schedule a reconnect timer")
	}
}

func toolsContainPing(t *testing.T, reg *Registry) {
	t.Helper()
	for _, dt := range reg.GetTools() {
		if dt.OriginalName == "ping" {
			return
		}
	}
	t.Fatal("tools list must still contain the helper's ping tool (config-driven tool table)")
}

// R1: after the call path evicts a dead connection, the tool table must keep
// the server's tools listed — GetTools() feeds the engine's live tool table,
// and the recovery entry point is precisely a call to that tool.
func TestToolsStayListedAfterEviction(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newHelperRegistry(t, map[string]any{"helper": helperConfig(t, pidFile, "serve")})
	pids := waitForHelperPIDs(t, pidFile, 1)
	toolsContainPing(t, reg)

	conn, _ := reg.GetConnection("helper")
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	if !reg.EvictIfSame("helper", conn.(*ConnectedServer)) {
		t.Fatal("EvictIfSame must evict the killed helper's connection")
	}
	toolsContainPing(t, reg)
}

// R2: kill -9 five times in a row; every next reconnect recovers against a
// fresh process and the tool table never loses the tool.
func TestKillFiveTimesToolsListedAndReconnectRecovers(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newHelperRegistry(t, map[string]any{"helper": helperConfig(t, pidFile, "serve")})
	pids := waitForHelperPIDs(t, pidFile, 1)

	for i := range 5 {
		if err := syscall.Kill(pids[len(pids)-1], syscall.SIGKILL); err != nil {
			t.Fatalf("kill #%d: %v", i+1, err)
		}
		conn, _ := reg.GetConnection("helper")
		if cs, ok := conn.(*ConnectedServer); ok {
			reg.EvictIfSame("helper", cs)
		}
		toolsContainPing(t, reg)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		newConn, err := reg.Reconnect(ctx, "helper")
		cancel()
		if err != nil {
			t.Fatalf("reconnect #%d must recover: %v", i+1, err)
		}
		if _, ok := newConn.(*ConnectedServer); !ok {
			t.Fatalf("reconnect #%d must yield a live connection, got %s", i+1, newConn.ConnType())
		}
		pids = waitForHelperPIDs(t, pidFile, len(pids)+1)
		toolsContainPing(t, reg)
	}
}

// R3: a failed reconnect stores a FailedServer; the tool table must keep the
// last-known tools listed so the next call retries the not-found path.
func TestToolsStayListedAfterFailedReconnect(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newHelperRegistry(t, map[string]any{"helper": helperConfig(t, pidFile, "die-on-respawn")})
	pids := waitForHelperPIDs(t, pidFile, 1)
	toolsContainPing(t, reg)

	conn, _ := reg.GetConnection("helper")
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	if !reg.EvictIfSame("helper", conn.(*ConnectedServer)) {
		t.Fatal("EvictIfSame must evict the killed helper's connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A spawn that dies before serving surfaces as a FailedServer connection
	// (connectInner maps spawn failures to FailedServer, nil-error), not as
	// a Go error from Reconnect itself.
	newConn, rcErr := reg.Reconnect(ctx, "helper")
	if rcErr == nil && newConn.ConnType() != "failed" {
		t.Fatalf("reconnect against die-on-respawn helper must end failed, got %s", newConn.ConnType())
	}
	stored, ok := reg.GetConnection("helper")
	if !ok || stored.ConnType() != "failed" {
		t.Fatalf("failed reconnect must store a FailedServer, got ok=%v type=%s", ok, stored.ConnType())
	}
	toolsContainPing(t, reg)
}

// Resource/command aggregates are config-driven too: an evicted server's
// last-known resources and commands must stay listed.
func TestResourcesAndCommandsStayListedAfterEviction(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newHelperRegistry(t, map[string]any{"helper": helperConfig(t, pidFile, "serve")})
	pids := waitForHelperPIDs(t, pidFile, 1)

	listed := func() (bool, bool) {
		var hasRes, hasCmd bool
		for _, r := range reg.GetResources() {
			if r.Name == "doc" {
				hasRes = true
			}
		}
		for _, c := range reg.GetCommands() {
			if c.Name == "mcp__helper__greet" {
				hasCmd = true
			}
		}
		return hasRes, hasCmd
	}
	hasRes, hasCmd := listed()
	if !hasRes || !hasCmd {
		t.Fatalf("helper should expose doc resource + greet command after connect, got res=%v cmd=%v", hasRes, hasCmd)
	}

	conn, _ := reg.GetConnection("helper")
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	if !reg.EvictIfSame("helper", conn.(*ConnectedServer)) {
		t.Fatal("EvictIfSame must evict the killed helper's connection")
	}
	hasRes, hasCmd = listed()
	if !hasRes || !hasCmd {
		t.Fatalf("resource/command lists must survive eviction (config-driven), got res=%v cmd=%v", hasRes, hasCmd)
	}
}

// Reconnect re-fetches fresh discovery on success: a respawned server with a
// changed tool list must be reflected (not the stale cache).
func TestReconnectRefreshesDiscovery(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newHelperRegistry(t, map[string]any{"helper": helperConfig(t, pidFile, "grow-tool")})
	pids := waitForHelperPIDs(t, pidFile, 1)
	toolsContainPing(t, reg)
	for _, dt := range reg.GetTools() {
		if dt.OriginalName == "pong2" {
			t.Fatal("pong2 must not exist on the first spawn")
		}
	}

	conn, _ := reg.GetConnection("helper")
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	if !reg.EvictIfSame("helper", conn.(*ConnectedServer)) {
		t.Fatal("EvictIfSame must evict the killed helper's connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := reg.Reconnect(ctx, "helper"); err != nil {
		t.Fatalf("reconnect must succeed: %v", err)
	}
	var hasPong2 bool
	for _, dt := range reg.GetTools() {
		if dt.OriginalName == "pong2" {
			hasPong2 = true
		}
	}
	if !hasPong2 {
		t.Fatal("reconnect must fetch fresh discovery: respawned pong2 must be listed")
	}
	toolsContainPing(t, reg)
}

func TestEvictIfSameIdentity(t *testing.T) {
	reg := NewRegistry(NewClientManager(TransportFactory{}, true, ""), ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	a := &ConnectedServer{Name: "s"}
	b := &ConnectedServer{Name: "s"}
	reg.SetConnectionForTest("s", a)
	if reg.EvictIfSame("s", b) {
		t.Error("EvictIfSame must not evict a different connection")
	}
	if _, ok := reg.GetConnection("s"); !ok {
		t.Fatal("connection must survive identity mismatch")
	}
	if !reg.EvictIfSame("s", a) {
		t.Error("EvictIfSame must evict the matching connection")
	}
	if _, ok := reg.GetConnection("s"); ok {
		t.Error("connection must be gone after identity match")
	}
}

func TestConcurrentReconnectAfterDeathSingleFlight(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	reg := newHelperRegistry(t, map[string]any{"helper": helperConfig(t, pidFile, "serve")})
	pids := waitForHelperPIDs(t, pidFile, 1)
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	const N = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	conns := make([]ServerConnection, N)
	errs := make([]error, N)
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			conns[i], errs[i] = reg.Reconnect(ctx, "helper")
		}(i)
	}
	close(start)
	wg.Wait()
	okCount := 0
	for i := range N {
		if errs[i] == nil && conns[i] != nil {
			okCount++
		}
	}
	if okCount != N {
		t.Errorf("all %d concurrent reconnects should succeed, got %d", N, okCount)
	}
	// Single flight: exactly one respawn — the pid file must hold exactly two
	// distinct pids (original + one respawn). A broken single flight spawns
	// duplicates that orphan profile locks.
	deadline := time.Now().Add(15 * time.Second) // REAL-TIME: respawn settle
	var uniq map[int]bool
	for {
		all := readHelperPIDs(t, pidFile)
		uniq = map[int]bool{}
		for _, p := range all {
			uniq[p] = true
		}
		if len(uniq) >= 2 || time.Now().After(deadline) { // REAL-TIME: bounded poll
			break
		}
		time.Sleep(50 * time.Millisecond) // REAL-TIME: respawn settle poll
	}
	if len(uniq) != 2 {
		t.Errorf("single-flight violated: expected exactly 2 distinct helper pids (original+respawn), got %d: %v", len(uniq), uniq)
	}
	// No orphans: after Close every spawned pid must die.
	_ = reg.Close()
	deadline = time.Now().Add(5 * time.Second) // REAL-TIME: cleanup escalation
	for {
		alive := 0
		for p := range uniq {
			if helperPidAlive(p) {
				alive++
			}
		}
		if alive == 0 || time.Now().After(deadline) { // REAL-TIME: bounded poll
			break
		}
		time.Sleep(50 * time.Millisecond) // REAL-TIME: process death poll
	}
	for p := range uniq {
		if helperPidAlive(p) {
			t.Errorf("orphan helper pid %d survived registry Close", p)
		}
	}
}

func TestRemoteCallErrorClassifiedAsDead(t *testing.T) {
	sdkSrv := sdk.NewServer(&sdk.Implementation{Name: "remote-helper", Version: "v1"}, nil)
	sdkSrv.AddTool(&sdk.Tool{Name: "ping", Description: "pong", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return sdkSrv }, nil)
	// Raw http.Server instead of httptest: Close() force-closes active
	// streams, which is exactly the remote-death event under test.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: handler}
	go func() { _ = httpSrv.Serve(ln) }()

	tmpDir := t.TempDir()
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"remote": map[string]any{"type": "http", "url": "http://" + ln.Addr().String()},
	}})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	reg, err := LoadAndConnectMCP(context.Background(), tmpDir, TransportFactory{}, nil)
	if err != nil {
		t.Fatalf("LoadAndConnectMCP: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	conn, ok := reg.GetConnection("remote")
	if !ok || conn.ConnType() != "connected" {
		t.Fatalf("remote server should be connected, got ok=%v", ok)
	}
	cs := conn.(*ConnectedServer)
	if err := httpSrv.Close(); err != nil {
		t.Fatalf("close remote server: %v", err)
	}
	// The SDK's streamable client is request-driven: remote death is only
	// discovered on the next call (Session.Wait does NOT fire — unlike stdio).
	// The classifier must recognize the resulting error so MCPTool.Call's
	// evict+reconnect+retry path engages.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, callErr := cs.Session.CallTool(ctx, &sdk.CallToolParams{Name: "ping"})
	if callErr == nil {
		t.Fatal("CallTool against a dead remote server must fail")
	}
	if !IsConnectionDeadError(callErr) {
		t.Fatalf("remote death error must be classified as connection-dead, got: %v", callErr)
	}
}

func TestIsConnectionDeadErrorTable(t *testing.T) {
	wrapped := func(err error) error {
		return fmt.Errorf("calling %q: %w", "tools/call", err)
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"ErrConnectionClosed wrapped", wrapped(sdk.ErrConnectionClosed), true},
		{"ErrSessionMissing wrapped", wrapped(sdk.ErrSessionMissing), true},
		{"io.EOF wrapped", wrapped(io.EOF), true},
		{"bare io.EOF", io.EOF, true},
		{"EPIPE wrapped", wrapped(syscall.EPIPE), true},
		{"ECONNREFUSED wrapped", wrapped(syscall.ECONNREFUSED), true},
		{"errors.Join EOF", errors.Join(errors.New("transport read"), io.EOF), true},
		{"nil", nil, false},
		{"tool-level error", errors.New("tool execution failed: element not found"), false},
		{"context deadline", context.DeadlineExceeded, false},
		{"EACCES not dead", syscall.EACCES, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsConnectionDeadError(tc.err); got != tc.want {
				t.Fatalf("IsConnectionDeadError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
