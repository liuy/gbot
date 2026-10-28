// Kill tests for pkg/mcp/registry.go mutation batch (e-registry, census
// 66.89% → 246 escaped). Every test here kills at least one census-escaped
// mutant instance; the per-instance mapping lives in
// /home/yliu/mutate-registry/ledger-registry.txt.
// LINE keys in that ledger bind to the pre-fix registry.go; post-fix remap
// and the binding md5 are recorded in the ledger header.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// selectivePanicProvider panics on NewTransport for registered names. connectInner
// turns every transport failure into (FailedServer, nil); a provider panic
// is the one channel that makes ClientManager.ConnectToServer return a real
// error (client.go's recover assigns the named return), which is what the
// registry's err != nil branches need to become reachable.
type selectivePanicProvider struct {
	panicNames map[string]bool
	calls      atomic.Int32
}

func (p *selectivePanicProvider) NewTransport(name string, cfg McpServerConfig, scope ConfigScope, trusted bool) (mcp.Transport, error) {
	p.calls.Add(1)
	if p.panicNames[name] {
		panic("mock: provider blew up for " + name)
	}
	return nil, fmt.Errorf("mock: no transport for %q", name)
}

// recordingHandler collects slog records so log-only mutations become
// assertable (message presence + exact attr values).
type recordingHandler struct {
	mu   sync.Mutex
	logs []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.logs = append(h.logs, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.logs {
		if r.Message == msg {
			n++
		}
	}
	return n
}

// intAttr returns the int value of key on the first record with msg.
func (h *recordingHandler) intAttr(msg, key string) (int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.logs {
		if r.Message != msg {
			continue
		}
		var out int64
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				switch v := a.Value.Any().(type) {
				case int:
					out, found = int64(v), true
				case int64:
					out, found = v, true
				}
			}
			return true
		})
		if found {
			return out, true
		}
	}
	return 0, false
}

// stringAttr returns the string value of key on the first record with msg.
func (h *recordingHandler) stringAttr(msg, key string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.logs {
		if r.Message != msg {
			continue
		}
		var out string
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && !found {
				out, found = a.Value.String(), true
			}
			return true
		})
		if found {
			return out, true
		}
	}
	return "", false
}

// captureSlog swaps the default logger for a recorder and restores it.
func captureSlog(t *testing.T) *recordingHandler {
	t.Helper()
	h := &recordingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return h
}

// regProvider stands up a fresh in-memory MCP server per connect (a fresh
// pipe each time — reconnects must not reuse the previous closed transport)
// and registers tools/resources/prompts per server name.
type regProvider struct {
	mu       sync.Mutex
	failConn map[string]bool
	tools    map[string][]string
	fullCaps map[string]bool // tool + resource + prompt
	sessions []*mcp.ServerSession
}

func newRegProvider() *regProvider {
	return &regProvider{
		failConn: make(map[string]bool),
		tools:    make(map[string][]string),
		fullCaps: make(map[string]bool),
	}
}

func (p *regProvider) fail(name string) {
	p.mu.Lock()
	p.failConn[name] = true
	p.mu.Unlock()
}

func (p *regProvider) withTools(name string, tools ...string) {
	p.mu.Lock()
	p.tools[name] = append(p.tools[name], tools...)
	p.mu.Unlock()
}

func (p *regProvider) withFullCaps(name string, tools ...string) {
	p.mu.Lock()
	p.fullCaps[name] = true
	p.tools[name] = append(p.tools[name], tools...)
	p.mu.Unlock()
}

func (p *regProvider) NewTransport(name string, _ McpServerConfig, _ ConfigScope, _ bool) (mcp.Transport, error) {
	p.mu.Lock()
	if p.failConn[name] {
		p.mu.Unlock()
		return nil, fmt.Errorf("mock: connection failed for %q", name)
	}
	toolNames := p.tools[name]
	full := p.fullCaps[name]
	p.mu.Unlock()

	t1, t2 := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "reg-" + name, Version: "1.0"}, nil)
	for _, tn := range toolNames {
		mcp.AddTool(server, &mcp.Tool{Name: tn, Description: "test"}, noopToolHandler)
	}
	if full {
		server.AddResource(&mcp.Resource{URI: "res://1", Name: "r1", MIMEType: "text/plain"}, noopResourceHandler)
		server.AddPrompt(&mcp.Prompt{Name: "p1"}, noopPromptHandler)
	}
	session, err := server.Connect(context.Background(), t1, nil)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.sessions = append(p.sessions, session)
	p.mu.Unlock()
	return t2, nil
}

func (p *regProvider) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sessions {
		_ = s.Close()
	}
}

// closableInMemoryServer is an in-memory server whose server-side session
// can be closed mid-test, making later List* calls fail deterministically.
func closableInMemoryServer(t *testing.T) (*mcp.Server, mcp.Transport, func()) {
	t.Helper()
	t1, t2 := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "closable", Version: "1.0"}, nil)
	type sessRes struct {
		s   *mcp.ServerSession
		err error
	}
	ch := make(chan sessRes, 1)
	go func() {
		s, err := server.Connect(context.Background(), t1, nil)
		ch <- sessRes{s, err}
	}()
	var sess *mcp.ServerSession
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("closable server connect: %v", r.err)
		}
		sess = r.s
	case <-time.After(5 * time.Second):
		t.Fatal("closable server connect timed out")
	}
	closed := false
	return server, t2, func() {
		if !closed {
			closed = true
			_ = sess.Close()
		}
	}
}

// lockFreesWithin asserts r.mu becomes free within timeout (a removed
// Unlock inside a failing path would hold it forever).
func lockFreesWithin(t *testing.T, r *Registry, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout) // REAL-TIME: TryLock poll deadline
	for {
		if r.mu.TryLock() {
			r.mu.Unlock()
			return
		}
		if !time.Now().Before(deadline) { // REAL-TIME: poll deadline
			t.Fatal("registry mutex still held after operation completed — Unlock removed?")
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: TryLock poll
	}
}

// ---------------------------------------------------------------------------
// Reconnection constants (registry.go:41, 575; discovery drain const 1238)
// ---------------------------------------------------------------------------

func TestRegistryDefaultConstantsExact(t *testing.T) {
	if shutdownGracePeriod != 5*time.Second {
		t.Errorf("shutdownGracePeriod = %v, want 5s", shutdownGracePeriod)
	}
	if agentServerConnectTimeout != 30*time.Second {
		t.Errorf("agentServerConnectTimeout = %v, want 30s", agentServerConnectTimeout)
	}
	if drainPollInterval != 25*time.Millisecond {
		t.Errorf("drainPollInterval = %v, want 25ms", drainPollInterval)
	}
}

// ---------------------------------------------------------------------------
// NewRegistry wiring (148-150) + SetConnectionForTest nil-map guard (431)
// ---------------------------------------------------------------------------

func TestNewRegistryWiresManagerCallbacks(t *testing.T) {
	mgr := NewClientManager(newInMemoryProvider(), true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	mgr.mu.Lock()
	resOK := mgr.onResourceChanged != nil
	toolOK := mgr.onToolChanged != nil
	cmdOK := mgr.onCommandChanged != nil
	mgr.mu.Unlock()
	if !resOK || !toolOK || !cmdOK {
		t.Fatalf("NewRegistry must wire all three manager callbacks, got resource=%v tool=%v command=%v", resOK, toolOK, cmdOK)
	}
}

func TestSetConnectionForTestInitializesNilMap(t *testing.T) {
	r := &Registry{}
	r.SetConnectionForTest("late", &ConnectedServer{Name: "late"})
	if _, ok := r.GetConnection("late"); !ok {
		t.Fatal("connection must be stored after nil-map initialization")
	}
}

// ---------------------------------------------------------------------------
// Accessor lock discipline: a reader must block while a writer holds r.mu
// ---------------------------------------------------------------------------

func TestRegistryAccessorsBlockWhileWriterHoldsRegistryLock(t *testing.T) {
	// Only accessors whose lock domain is r.mu itself: the map-iterating
	// readers (GetTools/HasResourceSupport/…) are pinned by the churn probe
	// instead, because a removed defer shows up as a runtime map guard, not
	// as a blocked call.
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"s": {Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser},
	})

	cases := []struct {
		name string
		call func()
	}{
		{"GetConfigs", func() { _ = r.GetConfigs() }},
		{"SetConfigsForTest", func() { r.SetConfigsForTest(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r.mu.Lock()
			done := make(chan struct{})
			go func() { tc.call(); close(done) }()
			select {
			case <-done:
				r.mu.Unlock()
				t.Fatalf("%s completed while a writer held r.mu", tc.name)
			case <-time.After(150 * time.Millisecond): // REAL-TIME: blocked-observation window
			}
			r.mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second): // REAL-TIME: must return soon after release
				t.Fatalf("%s never returned after lock release", tc.name)
			}
			if !r.mu.TryLock() {
				t.Fatalf("%s leaked the registry lock (Unlock removed)", tc.name)
			}
			r.mu.Unlock()
		})
	}
}

func TestAgentHandleCleanupHoldsHandleLock(t *testing.T) {
	// dr33 形状：cleanup 体可阻塞 → 停泊体内时 h.mu 必须仍被持有
	// （TryLock 必败）；defer 被删则锁在体前释放，TryLock 成功。
	parked := make(chan struct{})
	release := make(chan struct{})
	h := &AgentServerHandle{cleanup: func() error {
		close(parked)
		<-release // REAL-TIME: 把 cleanup 停在体内
		return nil
	}}

	done := make(chan error, 1)
	go func() { done <- h.Cleanup() }()
	<-parked
	if h.mu.TryLock() {
		h.mu.Unlock()
		close(release)
		<-done
		t.Fatal("h.mu was free while Cleanup was parked inside its body — defer removed?")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !h.mu.TryLock() {
		t.Fatal("Cleanup leaked the handle lock")
	}
	h.mu.Unlock()
}

// ---------------------------------------------------------------------------
// AgentServerHandle.Cleanup semantics (599, 601×2)
// ---------------------------------------------------------------------------

func TestAgentHandleCleanupRunsBodyExactlyOnce(t *testing.T) {
	var ran atomic.Int32
	h := &AgentServerHandle{cleanup: func() error { ran.Add(1); return nil }}
	if err := h.Cleanup(); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if err := h.Cleanup(); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if ran.Load() != 1 {
		t.Fatalf("cleanup body ran %d times, want exactly 1 (idempotence guard)", ran.Load())
	}
}

// ---------------------------------------------------------------------------
// ConnectAll (173, 195×2, 197, 222/223 family, 241/242)
// ---------------------------------------------------------------------------

func TestConnectAllRemovesStaleConnections(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["old"] = cfg
	r.connections["old"] = &ConnectedServer{Name: "old", Config: cfg, Cleanup: func() error { return nil }}
	r.mu.Unlock()

	r.ConnectAll(context.Background(), map[string]ScopedMcpServerConfig{})

	if _, ok := r.GetConnection("old"); ok {
		t.Error("stale connection must be removed when its config disappears")
	}
	if _, ok := r.GetConfigs()["old"]; ok {
		t.Error("stale config must be removed")
	}
}

func TestConnectAllDisabledResultCarriesNameAndConfig(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser}
	r.mu.Lock()
	r.disabled["test"] = true
	r.mu.Unlock()

	results := r.ConnectAll(context.Background(), map[string]ScopedMcpServerConfig{"test": cfg})

	conn, ok := results["test"]
	if !ok {
		t.Fatal("expected result entry for disabled server")
	}
	disabled, ok := conn.(*DisabledServer)
	if !ok {
		t.Fatalf("result type = %T, want *DisabledServer", conn)
	}
	if disabled.Name != "test" {
		t.Errorf("DisabledServer.Name = %q, want test", disabled.Name)
	}
	if disabled.Config.Config != cfg.Config {
		t.Error("DisabledServer must carry the server config")
	}
	stored, ok := r.GetConnection("test")
	if !ok {
		t.Fatal("disabled server must be stored in connections")
	}
	if stored.ConnType() != "disabled" {
		t.Errorf("stored conn type = %s, want disabled", stored.ConnType())
	}
}

func TestConnectAllRealConnectErrorYieldsFailedServer(t *testing.T) {
	mgr := NewClientManager(&selectivePanicProvider{panicNames: map[string]bool{"boom": true}}, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"boom": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})
	results := r.ConnectAll(context.Background(), r.GetConfigs())

	conn, ok := results["boom"]
	if !ok {
		t.Fatal("expected result entry for boom")
	}
	failed, ok := conn.(*FailedServer)
	if !ok {
		t.Fatalf("result type = %T, want *FailedServer", conn)
	}
	if failed.Name != "boom" {
		t.Errorf("FailedServer.Name = %q, want boom", failed.Name)
	}
	if failed.Config.Config == nil {
		t.Error("FailedServer must carry the server config")
	}
	if !strings.Contains(failed.Error, "panic") {
		t.Errorf("FailedServer.Error = %q, want panic trace", failed.Error)
	}
	stored, ok := r.GetConnection("boom")
	if !ok {
		t.Fatal("failed result must still be adopted into connections")
	}
	if stored.ConnType() != "failed" {
		t.Errorf("stored conn type = %s, want failed", stored.ConnType())
	}
}

// barrierProvider releases transports only once two connects are in
// flight — a serialized ConnectAll (goroutine removed from one group) can
// never satisfy the barrier.
type barrierProvider struct {
	inner   *regProvider
	mu      sync.Mutex
	cur     int
	release chan struct{}
}

func (p *barrierProvider) NewTransport(name string, cfg McpServerConfig, scope ConfigScope, trusted bool) (mcp.Transport, error) {
	p.mu.Lock()
	p.cur++
	if p.cur == 2 {
		close(p.release)
	}
	p.mu.Unlock()
	select {
	case <-p.release:
	case <-time.After(3 * time.Second): // REAL-TIME: 屏障守卫
		return nil, fmt.Errorf("mock: barrier not satisfied for %q", name)
	}
	return p.inner.NewTransport(name, cfg, scope, trusted)
}

// TestConnectAllOverlapsLocalAndRemoteGroups pins the two group goroutines:
// local and remote batches must run concurrently (rendezvous of 2).
func TestConnectAllOverlapsLocalAndRemoteGroups(t *testing.T) {
	inner := newRegProvider()
	t.Cleanup(inner.closeAll)
	inner.withTools("l", "tl")
	inner.withTools("rr", "tr")
	p := &barrierProvider{inner: inner, release: make(chan struct{})}
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	results := r.ConnectAll(context.Background(), map[string]ScopedMcpServerConfig{
		"l":  {Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser},
		"rr": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for name, conn := range results {
		if _, ok := conn.(*ConnectedServer); !ok {
			t.Errorf("%s: conn type = %T, want *ConnectedServer (groups serialized — barrier missed)", name, conn)
		}
	}
}

// TestConnectAllRunsLocalAndRemoteGroups pins the two processGroup
// goroutines: both batches must reach the provider.
func TestConnectAllRunsLocalAndRemoteGroups(t *testing.T) {
	p := newInMemoryProvider()
	for _, name := range []string{"l", "rr"} {
		srv, tr := setupInMemoryServer(t)
		mcp.AddTool(srv, &mcp.Tool{Name: "t_" + name}, noopToolHandler)
		p.mu.Lock()
		p.transports[name] = tr
		p.mu.Unlock()
	}
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	results := r.ConnectAll(context.Background(), map[string]ScopedMcpServerConfig{
		"l":  {Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser},
		"rr": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for name, conn := range results {
		if _, ok := conn.(*ConnectedServer); !ok {
			t.Errorf("%s: conn type = %T, want *ConnectedServer (its group goroutine never ran)", name, conn)
		}
	}
}

// ---------------------------------------------------------------------------
// Reconnect (294-297 family, 308/309)
// ---------------------------------------------------------------------------

func TestReconnectRealConnectErrorStoresFailedServerAndWraps(t *testing.T) {
	mgr := NewClientManager(&selectivePanicProvider{panicNames: map[string]bool{"boom": true}}, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"boom": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})

	conn, err := r.Reconnect(context.Background(), "boom")
	if err == nil {
		t.Fatal("Reconnect must return the wrapped error on real connect failure")
	}
	if !strings.Contains(err.Error(), "reconnect") || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("error = %v, want reconnect-wrapped panic error", err)
	}
	if errors.Unwrap(err) == nil {
		t.Error("error must wrap the underlying cause (%w)")
	}
	if conn != nil {
		t.Fatalf("conn = %v, want nil on failure", conn)
	}
	stored, ok := r.GetConnection("boom")
	if !ok {
		t.Fatal("failed reconnect must store a FailedServer entry")
	}
	failed, ok := stored.(*FailedServer)
	if !ok {
		t.Fatalf("stored conn type = %T, want *FailedServer", stored)
	}
	if failed.Name != "boom" {
		t.Errorf("stored Name = %q, want boom", failed.Name)
	}
	if failed.Config.Config == nil {
		t.Error("stored FailedServer must carry the config")
	}
	if !strings.Contains(failed.Error, "panic") {
		t.Errorf("stored Error = %q, want panic trace", failed.Error)
	}
	lockFreesWithin(t, r, 2*time.Second)
}

func TestReconnectInvalidatesDiscoveryCachesBeforeRefetch(t *testing.T) {
	var gotTools []DiscoveredTool
	p := newInMemoryProvider()
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{
		OnToolsChanged: func(name string, tools []DiscoveredTool) { gotTools = tools },
	})
	defer r.Close()

	srv, t2 := setupInMemoryServer(t)
	mcp.AddTool(srv, &mcp.Tool{Name: "fresh_tool"}, noopToolHandler)
	p.mu.Lock()
	p.transports["srv"] = t2
	p.mu.Unlock()

	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "test-cmd"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["srv"] = cfg
	r.toolCache.Add("srv", []DiscoveredTool{{Name: "stale_tool", ServerName: "srv"}})
	r.resourceCache.Add("srv", []ServerResource{{URI: "res://stale", Server: "srv"}})
	r.commandCache.Add("srv", []MCPCommand{{Name: "stale_cmd", ServerName: "srv"}})
	r.mu.Unlock()

	if _, err := r.Reconnect(context.Background(), "srv"); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if len(gotTools) != 1 || gotTools[0].OriginalName != "fresh_tool" || gotTools[0].Name != "mcp__srv__fresh_tool" {
		t.Fatalf("OnToolsChanged = %v, want exactly [mcp__srv__fresh_tool] (stale cache survived)", gotTools)
	}
	if tools := r.GetTools(); len(tools) != 1 || tools[0].OriginalName != "fresh_tool" {
		t.Fatalf("GetTools = %v, want exactly [mcp__srv__fresh_tool]", tools)
	}
	if res := r.GetResources(); len(res) != 0 {
		t.Fatalf("GetResources = %v, want empty (stale resource survived)", res)
	}
	if cmds := r.GetCommands(); len(cmds) != 0 {
		t.Fatalf("GetCommands = %v, want empty (stale command survived)", cmds)
	}
}

// ---------------------------------------------------------------------------
// Disconnect (350, 359, 364)
// ---------------------------------------------------------------------------

func TestDisconnectStopsPendingReconnectTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		origMin := reconnectMinBackoff
		reconnectMinBackoff = 40 * time.Millisecond
		defer func() { reconnectMinBackoff = origMin }()

		mgr := NewClientManager(&errorProvider{}, true, "")
		r := NewRegistry(mgr, ChangeCallbacks{})
		t.Cleanup(func() { _ = r.Close() })
		r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
			"remote": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
		})
		remoteCfg := ScopedMcpServerConfig{Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser}
		r.SetConnectionForTest("remote", &ConnectedServer{Name: "remote", Config: remoteCfg})

		r.ScheduleReconnect("remote", 0)
		if err := r.Disconnect("remote"); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}
		// 越过延迟窗：被 Stop 的 timer 不得再触发重连。
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()

		if _, ok := r.GetConnection("remote"); ok {
			t.Fatal("reconnect timer fired after Disconnect — Stop was removed")
		}
	})
}

func TestDisconnectRebuildsAggregatesAfterCacheClear(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["srv"] = cfg
	r.connections["srv"] = &ConnectedServer{Name: "srv", Config: cfg}
	r.toolCache.Add("srv", []DiscoveredTool{{Name: "t", ServerName: "srv"}})
	r.rebuildAggregatesLocked() // 聚合先有内容，rebuild 的删除才可观察
	r.mu.Unlock()
	if tools := r.GetTools(); len(tools) != 1 {
		t.Fatalf("fixture: GetTools = %d, want 1", len(tools))
	}

	if err := r.Disconnect("srv"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if tools := r.GetTools(); len(tools) != 0 {
		t.Fatalf("GetTools after Disconnect = %v, want empty (aggregates not rebuilt)", tools)
	}
}

func TestDisconnectInvalidatesManagerCache(t *testing.T) {
	// reconcileProvider 每次连接给新管道——复用已关管道会让第二次连接假失败。
	p := newReconcileProvider()
	t.Cleanup(p.closeAll)
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "cmd"}, Scope: ScopeUser}
	configs := map[string]ScopedMcpServerConfig{"srv": cfg}

	first := r.ConnectAll(context.Background(), configs)
	c1, ok := first["srv"].(*ConnectedServer)
	if !ok {
		t.Fatalf("first connect type = %T", first["srv"])
	}
	if err := r.Disconnect("srv"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	second := r.ConnectAll(context.Background(), configs)
	c2, ok := second["srv"].(*ConnectedServer)
	if !ok {
		t.Fatalf("second connect type = %T", second["srv"])
	}
	if c1 == c2 {
		t.Fatal("second connect returned the cached (disconnected) object — InvalidateCache was skipped")
	}
}

// ---------------------------------------------------------------------------
// ToggleServer (395, 396)
// ---------------------------------------------------------------------------

func TestToggleServerEnableReconnectsWorkingServer(t *testing.T) {
	p := newInMemoryProvider()
	srv, tr := setupInMemoryServer(t)
	mcp.AddTool(srv, &mcp.Tool{Name: "t"}, noopToolHandler)
	p.mu.Lock()
	p.transports["srv"] = tr
	p.mu.Unlock()

	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "cmd"}, Scope: ScopeUser}
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{"srv": cfg})
	r.mu.Lock()
	r.disabled["srv"] = true
	r.mu.Unlock()

	if err := r.ToggleServer(context.Background(), "srv"); err != nil {
		t.Fatalf("ToggleServer enable: %v", err)
	}
	conn, ok := r.GetConnection("srv")
	if !ok {
		t.Fatal("enabled server must be reconnected")
	}
	if conn.ConnType() != "connected" {
		t.Errorf("conn type = %s, want connected", conn.ConnType())
	}
}

func TestToggleServerEnableReturnsReconnectError(t *testing.T) {
	// 传输失败只产 FailedServer+nil err（enable 依设计返回 nil）；
	// 真错误只有 panic 通道——enable 必须把它递出去。
	mgr := NewClientManager(&selectivePanicProvider{panicNames: map[string]bool{"srv": true}}, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"srv": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})
	r.mu.Lock()
	r.disabled["srv"] = true
	r.mu.Unlock()

	err := r.ToggleServer(context.Background(), "srv")
	if err == nil {
		t.Fatal("ToggleServer enable must surface the reconnect failure")
	}
	if !strings.Contains(err.Error(), "reconnect") {
		t.Errorf("error = %v, want reconnect failure", err)
	}
}

// ---------------------------------------------------------------------------
// closeInner (798, 807/809, 818, 831/833/836/849, 853/857)
// ---------------------------------------------------------------------------

func TestCloseCancelsContextStopsTimersAndClearsState(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})

	cfg := ScopedMcpServerConfig{Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["srv"] = cfg
	r.connections["srv"] = &ConnectedServer{
		Name: "srv", Config: cfg, Cleanup: func() error { return nil },
	}
	r.reconnectTimers["srv"] = time.AfterFunc(time.Hour, func() {})
	r.mu.Unlock()

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if r.ctx.Err() == nil {
		t.Error("Close must cancel the registry context")
	}
	r.mu.RLock()
	timers := len(r.reconnectTimers)
	r.mu.RUnlock()
	if timers != 0 {
		t.Errorf("reconnectTimers after Close = %d entries, want 0", timers)
	}
	if _, ok := r.GetConnection("srv"); ok {
		t.Error("Close must remove server connections")
	}
}

func TestCloseWaitsForServerCleanupWithinPerServerCap(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})

	var cleaned atomic.Int32
	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "test-cmd"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["srv"] = cfg
	r.connections["srv"] = &ConnectedServer{
		Name:   "srv",
		Config: cfg,
		Cleanup: func() error {
			time.Sleep(200 * time.Millisecond) // REAL-TIME: slow Close, well inside the 5s cap
			cleaned.Add(1)
			return nil
		},
	}
	r.mu.Unlock()

	start := time.Now() // REAL-TIME: elapsed-window measurement
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)
	// 等残留的 cleanup goroutine 收尾，避免 goleak 误报。
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: drain wait bound
	for cleaned.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if cleaned.Load() != 1 {
		t.Fatalf("cleanup ran %d times, want 1", cleaned.Load())
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("Close returned after %v; per-server 5s close wait was skipped", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Close took %v with a 200ms cleanup — it waited the full per-server cap", elapsed)
	}
}

func TestClosePerServerCapDurationIsFiveSeconds(t *testing.T) {
	// 4.5s cleanup：cap=5s 时等 closeCh（elapsed≈4.5s）；cap 变 4s 则 4s 截断。
	t.Run("cap not shortened", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			origGrace := shutdownGracePeriod
			shutdownGracePeriod = 6 * time.Second
			defer func() { shutdownGracePeriod = origGrace }()

			r, _ := newTestRegistry(ChangeCallbacks{})
			cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "c"}, Scope: ScopeUser}
			r.mu.Lock()
			r.configs["srv"] = cfg
			r.connections["srv"] = &ConnectedServer{
				Name: "srv", Config: cfg,
				Cleanup: func() error { time.Sleep(4500 * time.Millisecond); return nil },
			}
			r.mu.Unlock()

			start := time.Now()
			if err := r.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if d := time.Since(start); d < 4400*time.Millisecond {
				t.Fatalf("Close returned after %v; the per-server cap fired before the 4.5s cleanup finished", d)
			}
		})
	})
	// 5.2s cleanup：cap=5s 时 5s 截断（elapsed≈5s）；cap 变 6s 则等满 5.2s。
	t.Run("cap not lengthened", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			origGrace := shutdownGracePeriod
			shutdownGracePeriod = 6 * time.Second
			defer func() { shutdownGracePeriod = origGrace }()

			r, _ := newTestRegistry(ChangeCallbacks{})
			cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "c"}, Scope: ScopeUser}
			r.mu.Lock()
			r.configs["srv"] = cfg
			r.connections["srv"] = &ConnectedServer{
				Name: "srv", Config: cfg,
				Cleanup: func() error { time.Sleep(5200 * time.Millisecond); return nil },
			}
			r.mu.Unlock()

			start := time.Now()
			if err := r.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if d := time.Since(start); d >= 5100*time.Millisecond {
				t.Fatalf("Close returned after %v; per-server cap did not cut off at 5s", d)
			}
			// 5s 截断后 cleanup goroutine 还在虚拟睡眠——排空再退出 bubble。
			time.Sleep(300 * time.Millisecond)
			synctest.Wait()
		})
	})
}

func TestClosePerServerCapBoundsWaitEvenWhenServerNeverCloses(t *testing.T) {
	// synctest 气泡：closeInner 的 per-server 5s cap 与 grace 全部走虚拟钟，
	// 零真实等待。cap 分支被删则唯一的 select 分支是 closeCh（cleanup 永不
	// 返回）→ 只能撞 grace 超时报错，err 非 nil 即为红。
	synctest.Test(t, func(t *testing.T) {
		origGrace := shutdownGracePeriod
		shutdownGracePeriod = 6 * time.Second // must exceed the per-server 5s cap
		defer func() { shutdownGracePeriod = origGrace }()

		r, _ := newTestRegistry(ChangeCallbacks{})
		release := make(chan struct{})
		cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "c"}, Scope: ScopeUser}
		r.mu.Lock()
		r.configs["srv"] = cfg
		r.connections["srv"] = &ConnectedServer{
			Name:   "srv",
			Config: cfg,
			Cleanup: func() error {
				<-release // server never closes on its own
				return nil
			},
		}
		r.mu.Unlock()

		start := time.Now()
		if err := r.Close(); err != nil {
			t.Fatalf("Close: %v — the per-server cap must let shutdown finish", err)
		}
		if d := time.Since(start); d < 4900*time.Millisecond {
			t.Fatalf("Close returned after %v; the per-server cap did not bound the hung server", d)
		}

		// 5s 截断后 cleanup goroutine 还停在 release 上——放行再退出 bubble。
		close(release)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
	})
}

func TestCloseTimesOutWithErrorWhenServerNeverCloses(t *testing.T) {
	origGrace := shutdownGracePeriod
	shutdownGracePeriod = 200 * time.Millisecond
	defer func() { shutdownGracePeriod = origGrace }()

	r, _ := newTestRegistry(ChangeCallbacks{})
	release := make(chan struct{})
	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "c"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["srv"] = cfg
	r.connections["srv"] = &ConnectedServer{
		Name:   "srv",
		Config: cfg,
		Cleanup: func() error {
			<-release // REAL-TIME
			return nil
		},
	}
	r.mu.Unlock()

	err := r.Close()
	close(release)
	time.Sleep(100 * time.Millisecond) // REAL-TIME: drain parked goroutines
	if err == nil {
		t.Fatal("Close must report an error when a server outlives the grace period")
	}
	if !strings.Contains(err.Error(), "did not close") {
		t.Errorf("error = %v, want 'did not close'", err)
	}
}

// ---------------------------------------------------------------------------
// ScheduleReconnect backoff window (896/898/899 family)
// ---------------------------------------------------------------------------

func TestScheduleReconnectBackoffJitterStaysWithinHalfBackoffWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		origMin := reconnectMinBackoff
		reconnectMinBackoff = 400 * time.Millisecond
		defer func() { reconnectMinBackoff = origMin }()

		p := newReconcileProvider()
		t.Cleanup(p.closeAll)
		var mu sync.Mutex
		scheduledAt := make(map[string]time.Time)
		delays := make(map[string]time.Duration)
		mgr := NewClientManager(p, true, "")
		r := NewRegistry(mgr, ChangeCallbacks{
			OnServerStatusChanged: func(name string, conn ServerConnection) {
				mu.Lock()
				t0 := scheduledAt[name]
				// 虚拟钟只被 timer 推进：回调时刻-排程时刻 = 精确 delay。
				delays[name] = time.Since(t0)
				mu.Unlock()
			},
		})
		t.Cleanup(func() { _ = r.Close() })

		type round struct {
			name               string
			attempt            int
			minDelay, maxDelay time.Duration
		}
		var rounds []round
		// 898 `/`→`*` 类实例每轮杀灭率 0.75；16 轮把漏杀压到 0.25^16≈2e-10。
		for i := range 16 {
			rounds = append(rounds, round{fmt.Sprintf("jit0-%d", i), 0, 200 * time.Millisecond, 400 * time.Millisecond})
		}
		// 896 `*`→`/`（1<<attempt 侧）只在 attempt≥1 可分辨。
		for i := range 4 {
			rounds = append(rounds, round{fmt.Sprintf("jit1-%d", i), 1, 400 * time.Millisecond, 800 * time.Millisecond})
		}
		// 898 `/2`→`/3` 把 jitter 窗压到 [0, b/3)：delay 上界从 b 缩到 5b/6。
		// 只有"够大的一次延迟"能分辨，每轮原版 ≥5b/6 的概率 1/3；40 轮
		// 漏杀率 (2/3)^40 ≈ 5e-8。
		for i := range 24 {
			rounds = append(rounds, round{fmt.Sprintf("jit0hi-%d", i), 0, 200 * time.Millisecond, 400 * time.Millisecond})
		}

		var attempt0Max time.Duration
		var attempt0Count int
		for _, rd := range rounds {
			cfg := ScopedMcpServerConfig{Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser}
			r.mu.Lock()
			r.configs[rd.name] = cfg
			r.mu.Unlock()
			mu.Lock()
			scheduledAt[rd.name] = time.Now()
			mu.Unlock()
			r.ScheduleReconnect(rd.name, rd.attempt)
			// 裸 synctest.Wait 不为无等待者的 AfterFunc 推钟（探针证实），
			// 先虚拟睡眠越过窗口上界放行 timer，再 Wait 收 Reconnect 回调链。
			time.Sleep(rd.maxDelay)
			synctest.Wait()
			mu.Lock()
			d, ok := delays[rd.name]
			mu.Unlock()
			if !ok {
				t.Fatalf("round %s: reconnect never fired (no status callback)", rd.name)
			}
			if d < rd.minDelay || d >= rd.maxDelay {
				t.Fatalf("round %s attempt %d: delay %v outside [%v, %v)",
					rd.name, rd.attempt, d, rd.minDelay, rd.maxDelay)
			}
			if rd.attempt == 0 && d > attempt0Max {
				attempt0Max = d
			}
			if rd.attempt == 0 {
				attempt0Count++
			}
		}
		// jitter 必须能吃满 [0, b/2)：上界 5b/6 之下的样本说明分母被改大。
		if floor := 5*400*time.Millisecond/6 + 1; attempt0Max < floor {
			t.Fatalf("largest attempt-0 delay over %d rounds = %v, want >= %v (jitter window too narrow)",
				attempt0Count, attempt0Max, floor)
		}
	})
}

func TestScheduleReconnectFailedAttemptReschedulesWithNextBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		origMin := reconnectMinBackoff
		reconnectMinBackoff = 10 * time.Millisecond
		defer func() { reconnectMinBackoff = origMin }()

		p := &selectivePanicProvider{panicNames: map[string]bool{"boom": true}}
		mgr := NewClientManager(p, true, "")
		r := NewRegistry(mgr, ChangeCallbacks{})
		t.Cleanup(func() { _ = r.Close() })
		r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
			"boom": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
		})

		r.ScheduleReconnect("boom", 0)
		// 阶段一：越过 attempt-0 窗（[5,10)ms），attempt-1 的重排计时器必须在。
		time.Sleep(15 * time.Millisecond)
		synctest.Wait()
		r.mu.RLock()
		_, has := r.reconnectTimers["boom"]
		attemptsSoFar := p.calls.Load()
		r.mu.RUnlock()
		if !has {
			t.Fatal("failed attempt must reschedule the next backoff timer")
		}
		if attemptsSoFar != 1 {
			t.Fatalf("attempts after first fire = %d, want 1", attemptsSoFar)
		}
		// 定时器不得被 Stop：链要继续跑，阶段二数全链次数。

		// 阶段二：跑完整链。全链累计退避（b=10ms）[5,10)+[10,20)+[20,40)+
		// [40,80)+[80,160)ms ≤310ms；裸 AfterFunc 不被 Wait 推进，须睡够。
		// 原版 5 次尝试（attempt 0..4）后停；attempt+1→attempt+2 则 3 次；
		// attempt+1→attempt+0 则无限重试。
		time.Sleep(400 * time.Millisecond)
		synctest.Wait()
		if got := p.calls.Load(); got != 5 {
			t.Fatalf("connect attempts after full retry chain = %d, want 5", got)
		}
	})
}

func TestScheduleReconnectTimerSelfCleansOnFire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		origMin := reconnectMinBackoff
		reconnectMinBackoff = 10 * time.Millisecond
		defer func() { reconnectMinBackoff = origMin }()

		p := newReconcileProvider()
		t.Cleanup(p.closeAll)
		mgr := NewClientManager(p, true, "")
		r := NewRegistry(mgr, ChangeCallbacks{})
		t.Cleanup(func() { _ = r.Close() })
		r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
			"remote": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
		})

		r.ScheduleReconnect("remote", 0)
		time.Sleep(100 * time.Millisecond) // 虚拟钟：越过延迟窗
		synctest.Wait()

		if _, ok := r.GetConnection("remote"); !ok {
			t.Fatal("fixture: reconnect should have stored a connection")
		}
		// 定时器体内 cancel 若被提到 Reconnect 之前，这次重连带的是已销毁的
		// ctx：连接退化成 FailedServer。
		conn, _ := r.GetConnection("remote")
		if conn.ConnType() != "connected" {
			t.Fatalf("connection after timer fire = %s, want connected (attempt context cancelled early)", conn.ConnType())
		}
		r.mu.RLock()
		timers := len(r.reconnectTimers)
		r.mu.RUnlock()
		if timers != 0 {
			t.Fatalf("reconnectTimers after successful fire = %d entries, want 0", timers)
		}
	})
}

func TestScheduleReconnectUnknownServerSchedulesNothing(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	r.ScheduleReconnect("ghost", 0)

	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.reconnectTimers) != 0 {
		t.Fatalf("reconnectTimers = %v, want empty for unknown server", r.reconnectTimers)
	}
}

// ---------------------------------------------------------------------------
// rebuildAggregatesLocked (937/938, 950/951/953/954)
// ---------------------------------------------------------------------------

func TestRebuildAggregatesResetsAccumulatorsEachRun(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	r.mu.Lock()
	r.resourceCache.Add("srv", []ServerResource{{URI: "res://1", Server: "srv"}})
	r.commandCache.Add("srv", []MCPCommand{{Name: "cmd1", ServerName: "srv"}})
	r.rebuildAggregatesLocked()
	r.rebuildAggregatesLocked() // 第二次：不得累加旧条目
	r.mu.Unlock()

	if res := r.GetResources(); len(res) != 1 {
		t.Fatalf("GetResources after two rebuilds = %v, want exactly 1 entry", res)
	}
	if cmds := r.GetCommands(); len(cmds) != 1 {
		t.Fatalf("GetCommands after two rebuilds = %v, want exactly 1 entry", cmds)
	}
}

func TestRebuildAggregatesIncludesResourceOnlyAndCommandOnlyServers(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	r.mu.Lock()
	r.resourceCache.Add("res-only", []ServerResource{{URI: "res://x", Server: "res-only"}})
	r.commandCache.Add("cmd-only", []MCPCommand{{Name: "c", ServerName: "cmd-only"}})
	r.rebuildAggregatesLocked()
	r.mu.Unlock()

	if res := r.GetResources(); len(res) != 1 || res[0].Server != "res-only" {
		t.Fatalf("GetResources = %v, want the resource-only server's entry", res)
	}
	if cmds := r.GetCommands(); len(cmds) != 1 || cmds[0].ServerName != "cmd-only" {
		t.Fatalf("GetCommands = %v, want the command-only server's entry", cmds)
	}
}

// ---------------------------------------------------------------------------
// Notification handlers (979-1042 family)
// ---------------------------------------------------------------------------

// registeredInMemoryTransport mirrors setupInMemoryServer but registers the
// handlers BEFORE the server session is established: the SDK snapshots server
// capabilities at connect, so a tool added afterwards can leave
// Capabilities.Tools nil and the discovery layer then legitimately reports
// "nothing to fetch" (seen as a -race-load flake in the notification tests).
func registeredInMemoryTransport(t *testing.T, register func(*mcp.Server)) mcp.Transport {
	t.Helper()
	t1, t2 := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "reg-fixture", Version: "1.0.0"}, nil)
	register(server)
	type sessionResult struct {
		session *mcp.ServerSession
		err     error
	}
	ch := make(chan sessionResult, 1)
	go func() {
		s, err := server.Connect(context.Background(), t1, nil)
		ch <- sessionResult{s, err}
	}()
	var session *mcp.ServerSession
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("fixture server connect: %v", res.err)
		}
		session = res.session
	case <-time.After(5 * time.Second): // REAL-TIME: connect guard
		t.Fatal("fixture server connect timed out")
	}
	t.Cleanup(func() { _ = session.Close() })
	return t2
}

// notificationFixture wires one connected server with a tool, a resource and
// a prompt. The connection is established with zero callbacks (Reconnect
// itself fires all three), so tests swap their observers in afterwards.
func notificationFixture(t *testing.T) (*Registry, string) {
	t.Helper()
	p := newInMemoryProvider()
	tr := registeredInMemoryTransport(t, func(srv *mcp.Server) {
		mcp.AddTool(srv, &mcp.Tool{Name: "read", Description: "r"}, noopToolHandler)
		srv.AddResource(&mcp.Resource{URI: "res://1", Name: "r1", MIMEType: "text/plain"}, noopResourceHandler)
		srv.AddPrompt(&mcp.Prompt{Name: "p1"}, noopPromptHandler)
	})
	p.mu.Lock()
	p.transports["srv"] = tr
	p.mu.Unlock()

	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	t.Cleanup(func() { _ = r.Close() })
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"srv": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})
	if _, err := r.Reconnect(context.Background(), "srv"); err != nil {
		t.Fatalf("fixture Reconnect: %v", err)
	}
	return r, "srv"
}

// swapCallbacks installs observers after the fixture's Reconnect ran.
func swapCallbacks(r *Registry, cb ChangeCallbacks) {
	r.mu.Lock()
	r.callbacks = cb
	r.mu.Unlock()
}

func TestHandleToolNotificationUpdatesAggregatesAndFiresCallback(t *testing.T) {
	var fired atomic.Int32
	var gotName string
	r, name := notificationFixture(t)
	swapCallbacks(r, ChangeCallbacks{
		OnToolsChanged: func(n string, tools []DiscoveredTool) {
			fired.Add(1)
			gotName = n
		},
	})
	if tools := r.GetTools(); len(tools) != 1 {
		t.Fatalf("fixture: GetTools = %d, want 1", len(tools))
	}
	// 清空聚合：rebuild 是否执行只能靠"聚合从空恢复"观察。
	r.mu.Lock()
	r.tools = nil
	r.mu.Unlock()
	r.handleToolNotification(name)
	if fired.Load() != 1 {
		t.Fatalf("OnToolsChanged fired %d times, want 1", fired.Load())
	}
	if gotName != name {
		t.Errorf("callback serverName = %q, want %q", gotName, name)
	}
	// rebuildAggregates：fetch 后聚合仍含该工具。
	if tools := r.GetTools(); len(tools) != 1 || tools[0].OriginalName != "read" {
		t.Fatalf("GetTools after notification = %v, want the refreshed tool", tools)
	}
}

func TestHandleResourceNotificationUpdatesAggregatesAndFiresCallback(t *testing.T) {
	var fired atomic.Int32
	r, name := notificationFixture(t)
	swapCallbacks(r, ChangeCallbacks{
		OnResourcesChanged: func(n string, res []ServerResource) { fired.Add(1) },
	})
	r.mu.Lock()
	r.resources = nil
	r.mu.Unlock()
	r.handleResourceNotification(name)
	if fired.Load() != 1 {
		t.Fatalf("OnResourcesChanged fired %d times, want 1", fired.Load())
	}
	if res := r.GetResources(); len(res) != 1 || res[0].URI != "res://1" {
		t.Fatalf("GetResources after notification = %v, want res://1", res)
	}
}

func TestHandleCommandNotificationUpdatesAggregatesAndFiresCallback(t *testing.T) {
	var fired atomic.Int32
	r, name := notificationFixture(t)
	swapCallbacks(r, ChangeCallbacks{
		OnCommandsChanged: func(n string, cmds []MCPCommand) { fired.Add(1) },
	})
	r.mu.Lock()
	r.commands = nil
	r.mu.Unlock()
	r.handleCommandNotification(name)
	if fired.Load() != 1 {
		t.Fatalf("OnCommandsChanged fired %d times, want 1", fired.Load())
	}
	if cmds := r.GetCommands(); len(cmds) != 1 {
		t.Fatalf("GetCommands after notification = %v, want the prompt entry", cmds)
	}
}

func TestHandleNotificationSilentWhenConnectionMissing(t *testing.T) {
	var toolFired, resFired, cmdFired atomic.Int32
	r, _ := notificationFixture(t)
	swapCallbacks(r, ChangeCallbacks{
		OnToolsChanged:     func(string, []DiscoveredTool) { toolFired.Add(1) },
		OnResourcesChanged: func(string, []ServerResource) { resFired.Add(1) },
		OnCommandsChanged:  func(string, []MCPCommand) { cmdFired.Add(1) },
	})
	r.handleToolNotification("ghost")
	r.handleResourceNotification("ghost")
	r.handleCommandNotification("ghost")
	if toolFired.Load()+resFired.Load()+cmdFired.Load() != 0 {
		t.Fatalf("callbacks fired for missing connection: tool=%d res=%d cmd=%d",
			toolFired.Load(), resFired.Load(), cmdFired.Load())
	}
}

func TestHandleNotificationFetchFailureSkipsCallback(t *testing.T) {
	// 会话关闭后 ListTools 确定性失败：原版必须静默返回（无回调）。
	var fired atomic.Int32
	p := newInMemoryProvider()
	srv, tr, closeSrv := closableInMemoryServer(t)
	mcp.AddTool(srv, &mcp.Tool{Name: "t"}, noopToolHandler)
	srv.AddResource(&mcp.Resource{URI: "res://1", Name: "r", MIMEType: "text/plain"}, noopResourceHandler)
	srv.AddPrompt(&mcp.Prompt{Name: "p"}, noopPromptHandler)
	p.mu.Lock()
	p.transports["srv"] = tr
	p.mu.Unlock()
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	t.Cleanup(func() { _ = r.Close() })
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"srv": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})
	if _, err := r.Reconnect(context.Background(), "srv"); err != nil {
		t.Fatalf("fixture Reconnect: %v", err)
	}
	swapCallbacks(r, ChangeCallbacks{
		OnToolsChanged:     func(string, []DiscoveredTool) { fired.Add(1) },
		OnResourcesChanged: func(string, []ServerResource) { fired.Add(1) },
		OnCommandsChanged:  func(string, []MCPCommand) { fired.Add(1) },
	})
	closeSrv()

	r.handleToolNotification("srv")
	r.handleResourceNotification("srv")
	r.handleCommandNotification("srv")
	if fired.Load() != 0 {
		t.Fatalf("callbacks fired %d times on failed fetch, want 0", fired.Load())
	}
}

// ---------------------------------------------------------------------------
// StartConfigWatch (1069, 1082)
// ---------------------------------------------------------------------------

func TestStartConfigWatchReloadsOnFileChange(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")
	writeCfg := func(content string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	srv, t2 := setupInMemoryServer(t)
	mcp.AddTool(srv, &mcp.Tool{Name: "added_tool"}, noopToolHandler)
	p := newInMemoryProvider()
	p.mu.Lock()
	p.transports["late"] = t2
	p.mu.Unlock()

	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	r.configDir = tmpDir
	defer r.Close()

	writeCfg(`{"mcpServers":{}}`)
	r.StartConfigWatch()
	if r.configWatcher == nil {
		t.Fatal("fixture: watcher not started")
	}

	writeCfg(`{"mcpServers":{"late":{"command":"echo"}}}`)
	// mtime 粒度可能粗于两次写入的间隔：显式拨到未来，保证 After(baseline) 成立。
	future := time.Now().Add(2 * time.Second) // REAL-TIME: mtime pin past the write
	if err := os.Chtimes(filepath.Join(tmpDir, ".mcp.json"), future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second) // REAL-TIME: 2s 轮询 ×2 + 余量
	for time.Now().Before(deadline) {
		if _, ok := r.GetConnection("late"); ok {
			return // reload 发生，watcher 活着
		}
		time.Sleep(50 * time.Millisecond) // REAL-TIME
	}
	t.Fatal("watcher never reloaded after config change — polling goroutine not started")
}

func TestStartConfigWatchLogsHotReloadEnabled(t *testing.T) {
	h := captureSlog(t)
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	r := NewRegistry(NewClientManager(newInMemoryProvider(), true, ""), ChangeCallbacks{})
	r.configDir = tmpDir
	defer r.Close()

	r.StartConfigWatch()
	if h.count("mcp: config hot-reload enabled") != 1 {
		t.Fatalf("hot-reload log count = %d, want 1", h.count("mcp: config hot-reload enabled"))
	}
}

// ---------------------------------------------------------------------------
// Config reload (handleConfigReload family)
// ---------------------------------------------------------------------------

// reloadFixture builds a registry whose configDir/.mcp.json drives
// handleConfigReload directly (no watcher timing involved).
type reloadFixture struct {
	r     *Registry
	p     *regProvider
	dir   string
	write func(string)
}

func newReloadFixture(t *testing.T, cb ChangeCallbacks) *reloadFixture {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".mcp.json")
	p := newRegProvider()
	t.Cleanup(p.closeAll)
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, cb)
	r.configDir = tmpDir
	t.Cleanup(func() { _ = r.Close() })
	f := &reloadFixture{r: r, p: p, dir: tmpDir}
	f.write = func(content string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	return f
}

// addServer registers tools (and, for the first tool name, full discovery
// capabilities) for name on the fixture provider.
func (f *reloadFixture) addServer(t *testing.T, name string, tools ...string) {
	t.Helper()
	f.p.withTools(name, tools...)
}

func TestConfigReloadChangedServerReconnects(t *testing.T) {
	var refreshes atomic.Int32
	f := newReloadFixture(t, ChangeCallbacks{
		OnToolsChanged: func(name string, tools []DiscoveredTool) { refreshes.Add(1) },
	})
	f.addServer(t, "srv", "read_file")
	f.write(`{"mcpServers":{"srv":{"command":"echo","args":["v1"]}}}`)

	configs, _ := GetProjectMcpConfigsFromCwd(f.dir)
	f.r.ConnectAll(context.Background(), configs)
	oldConn, ok := f.r.GetConnection("srv")
	if !ok {
		t.Fatal("fixture: srv did not connect")
	}
	refreshes.Store(0)

	f.write(`{"mcpServers":{"srv":{"command":"echo","args":["v2"]}}}`)
	f.r.handleConfigReload()

	newConn, ok := f.r.GetConnection("srv")
	if !ok {
		t.Fatal("changed server must have a connection after reload")
	}
	if newConn == oldConn {
		t.Fatal("changed server kept its old connection object — reload must reconnect it")
	}
	if refreshes.Load() != 1 {
		t.Fatalf("full-refresh callback fired %d times, want exactly 1", refreshes.Load())
	}
	if tools := f.r.GetTools(); len(tools) != 1 || tools[0].OriginalName != "read_file" {
		t.Fatalf("GetTools after changed reload = %v, want the server's tool", tools)
	}
	scoped, has := f.r.GetConfigs()["srv"]
	if !has {
		t.Fatal("configs must be updated by reload")
	}
	stdio, ok := scoped.Config.(*StdioConfig)
	if !ok || !slices.Equal(stdio.Args, []string{"v2"}) {
		t.Fatalf("refreshed config args = %v, want [v2] (configs not replaced)", stdio.Args)
	}
}

func TestConfigReloadUnchangedConfigLeavesEverythingUntouched(t *testing.T) {
	var refreshes atomic.Int32
	f := newReloadFixture(t, ChangeCallbacks{
		OnToolsChanged: func(name string, tools []DiscoveredTool) { refreshes.Add(1) },
	})
	f.addServer(t, "srv", "read_file")
	f.write(`{"mcpServers":{"srv":{"command":"echo"}}}`)

	configs, _ := GetProjectMcpConfigsFromCwd(f.dir)
	f.r.ConnectAll(context.Background(), configs)
	oldConn, ok := f.r.GetConnection("srv")
	if !ok {
		t.Fatal("fixture: srv did not connect")
	}
	refreshes.Store(0)

	f.r.handleConfigReload() // 文件未变

	if refreshes.Load() != 0 {
		t.Fatalf("no-change reload fired the full-refresh callback %d times, want 0", refreshes.Load())
	}
	if conn, ok := f.r.GetConnection("srv"); !ok || conn != oldConn {
		t.Fatal("no-change reload must not touch live connections")
	}
}

func TestConfigReloadBusyReloadIsSkippedEntirely(t *testing.T) {
	h := captureSlog(t)
	f := newReloadFixture(t, ChangeCallbacks{})
	f.addServer(t, "late")
	f.write(`{"mcpServers":{}}`)

	unlock := f.r.LockReloadForTest()
	f.write(`{"mcpServers":{"late":{"command":"echo"}}}`)
	f.r.handleConfigReload() // 忙：不得 reload
	unlock()

	if _, has := f.r.GetConfigs()["late"]; has {
		t.Fatal("busy reload must not adopt new configs (reloadMu was held)")
	}
	if _, ok := f.r.GetConnection("late"); ok {
		t.Fatal("busy reload must not connect servers")
	}
	if got := h.count("mcp: config reload already in progress, skipping"); got != 1 {
		t.Fatalf("busy-skip debug log count = %d, want 1", got)
	}
}

// gateProvider blocks the first NewTransport for gateName until release,
// parking handleConfigReload mid-run so reloadMu ownership is observable.
type gateProvider struct {
	inner   *regProvider
	gate    string
	release chan struct{}
}

func (p *gateProvider) NewTransport(name string, cfg McpServerConfig, scope ConfigScope, trusted bool) (mcp.Transport, error) {
	if name == p.gate {
		<-p.release
	}
	return p.inner.NewTransport(name, cfg, scope, trusted)
}

func TestConfigReloadHoldsReloadMuThroughConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := newRegProvider()
		t.Cleanup(inner.closeAll)
		release := make(chan struct{})
		p := &gateProvider{inner: inner, gate: "gated", release: release}
		mgr := NewClientManager(p, true, "")
		r := NewRegistry(mgr, ChangeCallbacks{})
		t.Cleanup(func() { _ = r.Close() })

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, ".mcp.json")
		if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"srv":{"command":"echo"}}}`), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		r.configDir = tmpDir
		r.SetConfigsForTest(map[string]ScopedMcpServerConfig{"srv": stdioCfg()})

		// 追加 gated 服务器 → reload 停泊在 connectSingle（持 reloadMu）。
		if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"srv":{"command":"echo"},"gated":{"command":"x"}}}`), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		reloadDone := make(chan struct{})
		go func() { r.handleConfigReload(); close(reloadDone) }()
		synctest.Wait()

		res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{"srv": stdioCfg()}, time.Second)
		if ok {
			t.Fatalf("Reconcile ran while handleConfigReload was mid-run: %+v — reloadMu released early", res)
		}
		if res != nil {
			t.Fatal("busy Reconcile must return nil result")
		}

		close(release)
		<-reloadDone
	})
}

func TestConfigReloadReleasesReloadMuAfterRun(t *testing.T) {
	f := newReloadFixture(t, ChangeCallbacks{})
	f.write(`{"mcpServers":{}}`)
	f.r.handleConfigReload()

	res, ok := f.r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{}, time.Second)
	if !ok {
		t.Fatal("reloadMu still held after handleConfigReload returned — defer removed?")
	}
	if res == nil {
		t.Fatal("Reconcile must return a result when not busy")
	}
}

func TestConfigReloadLogsSkipsAndActions(t *testing.T) {
	h := captureSlog(t)
	f := newReloadFixture(t, ChangeCallbacks{})
	f.addServer(t, "srv", "")
	f.write(`{"mcpServers":{"srv":{"command":"echo","args":["v1"]}}}`)
	configs, _ := GetProjectMcpConfigsFromCwd(f.dir)
	f.r.ConnectAll(context.Background(), configs)

	// 断配 → bail（保留 warn）
	f.write(`{"mcpServers":{"srv":{}}}`)
	f.r.handleConfigReload()
	if got := h.count("mcp: reload skipped, invalid server config"); got != 1 {
		t.Fatalf("bail warn count = %d, want 1", got)
	}
	h.mu.Lock()
	h.logs = nil
	h.mu.Unlock()

	// 正常变更 → reload 日志
	f.write(`{"mcpServers":{"srv":{"command":"echo","args":["v2"]}}}`)
	f.r.handleConfigReload()
	if got := h.count("mcp: config reload"); got != 1 {
		t.Fatalf("reload log count = %d, want 1", got)
	}
	if got := h.count("mcp: reconnected changed server"); got != 1 {
		t.Fatalf("reconnect log count = %d, want 1", got)
	}

	h.mu.Lock()
	h.logs = nil
	h.mu.Unlock()
	// 新增服务器 → connect 日志
	f.addServer(t, "fresh")
	f.write(`{"mcpServers":{"srv":{"command":"echo","args":["v2"]},"fresh":{"command":"echo"}}}`)
	f.r.handleConfigReload()
	if got := h.count("mcp: connected new server"); got != 1 {
		t.Fatalf("add log count = %d, want 1", got)
	}

	// 移除 → disconnect 日志
	h.mu.Lock()
	h.logs = nil
	h.mu.Unlock()
	f.write(`{"mcpServers":{"srv":{"command":"echo","args":["v2"]}}}`)
	f.r.handleConfigReload()
	if got := h.count("mcp: disconnected removed server"); got != 1 {
		t.Fatalf("remove log count = %d, want 1", got)
	}
}

func TestConfigReloadChangedServerUsesBackgroundContext(t *testing.T) {
	// 1169 的 nil ctx：带资源/prompt 能力的服务器让 FetchResources/Commands
	// 真正执行——nil context 会在 SDK 调用处崩溃。
	f := newReloadFixture(t, ChangeCallbacks{})
	f.p.withFullCaps("srv", "t")

	f.write(`{"mcpServers":{"srv":{"command":"echo","args":["v1"]}}}`)
	configs, _ := GetProjectMcpConfigsFromCwd(f.dir)
	f.r.ConnectAll(context.Background(), configs)
	f.write(`{"mcpServers":{"srv":{"command":"echo","args":["v2"]}}}`)
	f.r.handleConfigReload() // nil ctx 变异体会在此崩溃

	if _, ok := f.r.GetConnection("srv"); !ok {
		t.Fatal("changed server must reconnect")
	}
	if res := f.r.GetResources(); len(res) != 1 {
		t.Fatalf("GetResources after changed reload = %v, want the server's resource", res)
	}
	if cmds := f.r.GetCommands(); len(cmds) != 1 {
		t.Fatalf("GetCommands after changed reload = %v, want the server's prompt", cmds)
	}
}

// ---------------------------------------------------------------------------
// LoadAndConnectMCP logging + wiring (469-502)
// ---------------------------------------------------------------------------

func TestLoadAndConnectMCPMergesPluginServersAndLogs(t *testing.T) {
	h := captureSlog(t)
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"),
		[]byte(`{"mcpServers":{"bad":{},"ok":{"command":"echo"},"dead":{"command":"x"}}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	p := newRegProvider()
	t.Cleanup(p.closeAll)
	p.withTools("ok", "t_ok")
	p.withTools("plugin:p:x", "t_plug")
	p.fail("dead")

	plugin := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "plug"},
		Scope:        ScopeDynamic,
		PluginSource: "plugin:p:x",
	}
	registry, err := LoadAndConnectMCP(context.Background(), tmpDir, p, map[string]ScopedMcpServerConfig{
		"plugin:p:x": plugin,
	})
	if err != nil {
		t.Fatalf("LoadAndConnectMCP: %v", err)
	}
	defer registry.Close()

	if _, has := registry.GetConfigs()["plugin:p:x"]; !has {
		t.Fatal("plugin servers must be merged into the registry configs")
	}
	if got := h.count("mcp: skipping invalid server config"); got != 1 {
		t.Fatalf("skip-invalid warn count = %d, want 1 (bad entry)", got)
	}
	if got := h.count("mcp: connected"); got != 2 {
		t.Fatalf("connected log count = %d, want 2 (ok + plugin)", got)
	}
	// 只有 dead 一个服务器失败（bad 在配置阶段被剔除）。
	if got := h.count("mcp: failed to connect"); got != 1 {
		t.Fatalf("failed log count = %d, want 1 (dead)", got)
	}
	if v, ok := h.intAttr("mcp: startup complete", "connected"); !ok || v != 2 {
		t.Errorf("startup complete connected attr = %d (ok=%v), want 2", v, ok)
	}
	if v, ok := h.intAttr("mcp: startup complete", "failed"); !ok || v != 1 {
		t.Errorf("startup complete failed attr = %d (ok=%v), want 1", v, ok)
	}
	if got := h.count("mcp: startup complete"); got != 1 {
		t.Fatalf("startup complete log count = %d, want 1", got)
	}
	// StartConfigWatch 成功（tmpDir 有 .mcp.json）。
	if registry.configWatcher == nil {
		t.Fatal("LoadAndConnectMCP must start the config watcher when .mcp.json exists")
	}
	// 483：SetWorkDir 必须执行——manager 工作目录来自 cwd。
	registry.manager.mu.Lock()
	wd := registry.manager.workDir
	registry.manager.mu.Unlock()
	if wd != tmpDir {
		t.Errorf("manager workDir = %q, want %q", wd, tmpDir)
	}
}

// ---------------------------------------------------------------------------
// ConnectAgentServers (642, 648-681 family, 713-761 family)
// ---------------------------------------------------------------------------

func TestConnectAgentServersEmptyStringRefIsNotARef(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	// "" 不是合法引用：原版走配置解析 → 失败跳过 → handle nil。
	// `&&`→`||` / 去掉 ref!="" 的变异体会把 "" 当引用。
	r.mu.Lock()
	r.connections[""] = &ConnectedServer{Name: ""}
	r.tools = append(r.tools, DiscoveredTool{Name: "ghost_tool", ServerName: ""})
	r.mu.Unlock()

	handle := r.ConnectAgentServers(context.Background(), "a", []json.RawMessage{json.RawMessage(`""`)})
	if handle != nil {
		t.Fatalf("handle = %v with tools %v, want nil (empty string is not a server ref)", handle, handle.Tools())
	}
}

func TestConnectAgentServersProcessesEverySpec(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	// 两个合法引用：第一个不存在（跳过），第二个带工具。
	// loop/break（首迭代即断）会丢掉第二个。
	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "x"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["one"] = cfg
	r.connections["one"] = &ConnectedServer{Name: "one", Config: cfg}
	r.configs["two"] = cfg
	r.connections["two"] = &ConnectedServer{Name: "two", Config: cfg}
	r.tools = append(r.tools,
		DiscoveredTool{Name: "tool_one", ServerName: "one"},
		DiscoveredTool{Name: "tool_two", ServerName: "two"},
	)
	r.mu.Unlock()

	handle := r.ConnectAgentServers(context.Background(), "a", []json.RawMessage{
		json.RawMessage(`"one"`),
		json.RawMessage(`"two"`),
	})
	if handle == nil {
		t.Fatal("expected a handle carrying both servers' tools")
	}
	tools := handle.Tools()
	if _, ok := tools["tool_one"]; !ok {
		t.Error("tool_one missing — first spec was not processed")
	}
	if _, ok := tools["tool_two"]; !ok {
		t.Error("tool_two missing — second spec was dropped (loop broke early?)")
	}
	if n := len(tools); n != 2 {
		t.Fatalf("handle carries %d tools, want 2 (both refs must be validated)", n)
	}
}

func TestConnectAgentServersCollectsAllInlineResults(t *testing.T) {
	p := newInMemoryProvider()
	srv, tr := setupInMemoryServer(t)
	mcp.AddTool(srv, &mcp.Tool{Name: "second_tool"}, noopToolHandler)
	p.mu.Lock()
	p.transports["agent-collect-1"] = tr // 第二个 spec 成功；第一个 failConn
	p.failConn["agent-collect-0"] = true
	p.mu.Unlock()

	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	handle := r.ConnectAgentServers(context.Background(), "collect", []json.RawMessage{
		json.RawMessage(`{"command": "dead"}`),
		json.RawMessage(`{"command": "live"}`),
	})
	if handle == nil {
		t.Fatal("expected a handle carrying the surviving server's tools")
	}
	defer func() { _ = handle.Cleanup() }()

	if _, ok := handle.Tools()["mcp__agent-collect-1__second_tool"]; !ok {
		t.Fatalf("second inline spec's tool missing: %v", handle.Tools())
	}
	if _, ok := r.GetConnection("agent-collect-0"); ok {
		t.Error("failed spec left a residual connection")
	}

	// Cleanup 必须断连它记下的 agent 服务器（闭包捕获的是 namesCopy 快照）。
	conn, ok := r.GetConnection("agent-collect-1")
	if !ok {
		t.Fatal("fixture: second spec must be connected")
	}
	if conn.ConnType() != "connected" {
		t.Fatalf("adopted conn type = %s, want connected", conn.ConnType())
	}
	if err := handle.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, still := r.GetConnection("agent-collect-1"); still {
		t.Error("cleanup must disconnect the recorded agent connections")
	}
}

func TestConnectAgentServersInlineAdoptsScopedConfig(t *testing.T) {
	p := newInMemoryProvider()
	srv, tr := setupInMemoryServer(t)
	mcp.AddTool(srv, &mcp.Tool{Name: "t"}, noopToolHandler)
	p.mu.Lock()
	p.transports["agent-scope-0"] = tr
	p.mu.Unlock()

	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	handle := r.ConnectAgentServers(context.Background(), "scope", []json.RawMessage{
		json.RawMessage(`{"command": "c"}`),
	})
	if handle == nil {
		t.Fatal("expected a handle")
	}
	defer func() { _ = handle.Cleanup() }()

	conn, ok := r.GetConnection("agent-scope-0")
	if !ok {
		t.Fatal("inline server must be adopted into connections")
	}
	if conn.ConnType() != "connected" {
		t.Errorf("adopted conn type = %s, want connected", conn.ConnType())
	}
	cfgs := r.GetConfigs()
	scoped, ok := cfgs["agent-scope-0"]
	if !ok {
		t.Fatal("inline server must be recorded in configs")
	}
	if scoped.Scope != ScopeDynamic {
		t.Errorf("adopted config Scope = %q, want %q", scoped.Scope, ScopeDynamic)
	}
	if scoped.Config == nil {
		t.Error("adopted config must carry the McpServerConfig")
	}
}

func TestConnectAgentServersRealConnectErrorAdoptsNothing(t *testing.T) {
	h := captureSlog(t)
	mgr := NewClientManager(&selectivePanicProvider{panicNames: map[string]bool{"agent-x-0": true}}, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	handle := r.ConnectAgentServers(context.Background(), "x", []json.RawMessage{
		json.RawMessage(`{"command": "test-cmd"}`),
	})
	if handle != nil {
		t.Fatal("handle must be nil when the inline server fails with a real error")
	}
	r.mu.RLock()
	for name, conn := range r.connections {
		if strings.HasPrefix(name, "agent-") {
			t.Fatalf("agent connection %q=%v leaked after real connect failure", name, conn)
		}
	}
	r.mu.RUnlock()
	// 失败行的 server 属性取自 connResult.serverName：字段被清就变成空串。
	if got := h.count("agent MCP server connect failed"); got != 1 {
		t.Fatalf("connect-failed warn count = %d, want 1", got)
	}
	if srv, ok := h.stringAttr("agent MCP server connect failed", "server"); !ok || srv != "agent-x-0" {
		t.Fatalf("connect-failed warn server attr = %q (ok=%v), want agent-x-0", srv, ok)
	}
	if ag, ok := h.stringAttr("agent MCP server connect failed", "agent"); !ok || ag != "x" {
		t.Fatalf("connect-failed warn agent attr = %q (ok=%v), want x", ag, ok)
	}
}

func TestConnectAgentServersLogsFailures(t *testing.T) {
	h := captureSlog(t)
	p := newInMemoryProvider()
	p.mu.Lock()
	p.failConn["agent-logs-0"] = true
	p.mu.Unlock()
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	handle := r.ConnectAgentServers(context.Background(), "logs", []json.RawMessage{
		json.RawMessage(`{"command": "dead"}`),
	})
	if handle != nil {
		t.Fatal("all-failed specs must yield a nil handle")
	}
	if got := h.count("agent MCP server connect failed"); got != 1 {
		t.Fatalf("connect-failed warn count = %d, want 1", got)
	}
	if srv, ok := h.stringAttr("agent MCP server connect failed", "server"); !ok || srv != "agent-logs-0" {
		t.Fatalf("connect-failed warn server attr = %q (ok=%v), want agent-logs-0", srv, ok)
	}
}

func TestConnectAgentServersLogsInvalidRefParse(t *testing.T) {
	h := captureSlog(t)
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	// 引用不存在 → warn；配置解析失败 → warn。
	handle := r.ConnectAgentServers(context.Background(), "logs", []json.RawMessage{
		json.RawMessage(`"ghost"`),
		json.RawMessage(`123`),
	})
	if handle != nil {
		t.Fatal("expected nil handle")
	}
	if got := h.count("agent MCP server ref not found"); got != 1 {
		t.Fatalf("ref-not-found warn count = %d, want 1", got)
	}
	if got := h.count("agent MCP server config parse failed"); got != 1 {
		t.Fatalf("parse-failed warn count = %d, want 1", got)
	}
}

func TestConnectAgentServersLogsNotConnectedRef(t *testing.T) {
	h := captureSlog(t)
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "x"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["dead"] = cfg
	r.connections["dead"] = &FailedServer{Name: "dead", Config: cfg}
	r.mu.Unlock()

	handle := r.ConnectAgentServers(context.Background(), "logs", []json.RawMessage{
		json.RawMessage(`"dead"`),
	})
	if handle != nil {
		t.Fatal("expected nil handle")
	}
	if got := h.count("agent MCP server ref not connected"); got != 1 {
		t.Fatalf("ref-not-connected warn count = %d, want 1", got)
	}
	// 0 个有效引用时不得记 "refs resolved"。
	if got := h.count("agent MCP string refs resolved"); got != 0 {
		t.Fatalf("refs-resolved log count = %d, want 0 (no valid refs)", got)
	}
}

// ---------------------------------------------------------------------------
// Reconcile (1272, 1324/1326/1328 family, 1341, 1358, 1362/1363, 1387)
// ---------------------------------------------------------------------------

func TestReconcileHoldsReloadMuWhileDraining(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := newReconcileRegistry(t)
		old := stdioCfg("v1")
		r.SetConfigsForTest(map[string]ScopedMcpServerConfig{"a": old})
		r.connectSingle(context.Background(), "a", old)
		if _, ok := r.GetConnection("a"); !ok {
			t.Fatal("fixture: server a did not connect")
		}

		callDone := r.EnterCall("a")
		type outcome struct {
			res *ReconcileResult
			ok  bool
		}
		first := make(chan outcome, 1)
		go func() {
			res, ok := r.Reconcile(context.Background(),
				map[string]ScopedMcpServerConfig{"a": stdioCfg("v2")}, 10*time.Second)
			first <- outcome{res, ok}
		}()
		synctest.Wait() // 第一个 Reconcile 已停泊在 DrainServer

		second := make(chan outcome, 1)
		go func() {
			res, ok := r.Reconcile(context.Background(),
				map[string]ScopedMcpServerConfig{"a": stdioCfg("v3")}, time.Second)
			second <- outcome{res, ok}
		}()
		synctest.Wait()

		select {
		case got := <-second:
			if got.ok {
				t.Fatal("second Reconcile ran although the first still held reloadMu")
			}
			if got.res != nil {
				t.Fatal("busy path must return nil result")
			}
		default:
			t.Fatal("second Reconcile did not return busy — reloadMu released mid-run (defer removed?)")
		}

		callDone()
		got := <-first
		if !got.ok {
			t.Fatalf("first Reconcile reported busy: %+v", got)
		}
	})
}

func TestReconcileReportsAndSortsResultSets(t *testing.T) {
	h := captureSlog(t)
	r, _ := newReconcileRegistry(t)
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"b-keep": stdioCfg(),
		"z-old":  stdioCfg(),
	})
	// c-new 加入、b-keep 变更（args 不同）、z-old 移除：三处结果集都要钉。
	changedCfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "echo", Args: []string{"v2"}}, Scope: ScopeUser}
	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{
		"c-new":  stdioCfg(),
		"b-keep": changedCfg,
	}, time.Second)
	if !ok {
		t.Fatal("Reconcile returned ok=false; nothing else held reloadMu")
	}
	if res == nil {
		t.Fatal("Reconcile returned nil result")
	}
	if !slices.Equal(res.Added, []string{"c-new"}) {
		t.Errorf("Added = %v, want [c-new]", res.Added)
	}
	if !slices.Equal(res.Changed, []string{"b-keep"}) {
		t.Errorf("Changed = %v, want [b-keep]", res.Changed)
	}
	if !slices.Equal(res.Removed, []string{"z-old"}) {
		t.Errorf("Removed = %v, want [z-old]", res.Removed)
	}
	if got := h.count("mcp: manual reconcile"); got != 1 {
		t.Fatalf("manual-reconcile log count = %d, want 1", got)
	}
}

func TestReconcileSortsAddedRemovedAndFailedSets(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"gone-z": stdioCfg(),
		"gone-a": stdioCfg(),
	})
	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{
		"z-new": stdioCfg(),
		"a-new": stdioCfg(),
	}, time.Second)
	if !ok || res == nil {
		t.Fatalf("Reconcile ok=%v res=%v", ok, res)
	}
	if !slices.Equal(res.Added, []string{"a-new", "z-new"}) {
		t.Errorf("Added = %v, want sorted [a-new z-new]", res.Added)
	}
	if !slices.Equal(res.Removed, []string{"gone-a", "gone-z"}) {
		t.Errorf("Removed = %v, want sorted [gone-a gone-z]", res.Removed)
	}

	// 两个失败服务器验证 ReconnectFailed 排序。
	r2, p2 := newReconcileRegistry(t)
	p2.mu.Lock()
	p2.failConn["zf"] = true
	p2.failConn["af"] = true
	p2.mu.Unlock()
	res2, ok := r2.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{
		"zf": stdioCfg(),
		"af": stdioCfg(),
	}, time.Second)
	if !ok || res2 == nil {
		t.Fatalf("Reconcile ok=%v res=%v", ok, res2)
	}
	if !slices.Equal(res2.ReconnectFailed, []string{"af", "zf"}) {
		t.Errorf("ReconnectFailed = %v, want sorted [af zf]", res2.ReconnectFailed)
	}
}

func TestReconcileUnchangedSetFiresNoCallbackAndTouchesNothing(t *testing.T) {
	var refreshes atomic.Int32
	r, _ := newReconcileRegistryWithCallbacks(t, ChangeCallbacks{
		OnToolsChanged: func(string, []DiscoveredTool) { refreshes.Add(1) },
	})
	same := stdioCfg("v1")
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{"a": same})
	r.connectSingle(context.Background(), "a", same)
	oldConn, ok := r.GetConnection("a")
	if !ok {
		t.Fatal("fixture: a did not connect")
	}
	refreshes.Store(0)

	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{"a": same}, time.Second)
	if !ok || res == nil {
		t.Fatalf("Reconcile ok=%v res=%v", ok, res)
	}
	if refreshes.Load() != 0 {
		t.Fatalf("no-change reconcile fired the full-refresh callback %d times, want 0", refreshes.Load())
	}
	if conn, ok := r.GetConnection("a"); !ok || conn != oldConn {
		t.Fatal("no-change reconcile must not touch live connections")
	}
	if len(res.Added)+len(res.Removed)+len(res.Changed)+len(res.ReconnectFailed) != 0 {
		t.Fatalf("no-change reconcile reported changes: %+v", res)
	}
}

func TestReconcileDrainTargetsInSortedOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := newReconcileRegistry(t)
		old := map[string]ScopedMcpServerConfig{}
		for _, n := range []string{"m-2", "m-1", "m-5", "m-4", "m-3"} {
			old[n] = stdioCfg()
		}
		r.SetConfigsForTest(old)
		// 五个移除目标全部 in-flight → drain 全超时 → DrainedLate 必须按
		// drainTargets 的排序（sorted）产出；sort 被删则为 map 随机序
		// （侥幸有序概率 1/5!≈0.8%）。
		var dones []func()
		for n := range old {
			d := r.EnterCall(n)
			dones = append(dones, d)
		}
		res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{}, 100*time.Millisecond)
		if !ok {
			t.Fatal("Reconcile reported busy")
		}
		if !slices.Equal(res.DrainedLate, []string{"m-1", "m-2", "m-3", "m-4", "m-5"}) {
			t.Fatalf("DrainedLate = %v, want sorted [m-1 m-2 m-3 m-4 m-5]", res.DrainedLate)
		}
		for _, d := range dones {
			d()
		}
	})
}

func TestReconcileRecordsDrainedLateServers(t *testing.T) {
	h := captureSlog(t)
	synctest.Test(t, func(t *testing.T) {
		r, _ := newReconcileRegistry(t)
		old := stdioCfg("v1")
		r.SetConfigsForTest(map[string]ScopedMcpServerConfig{"a": old})
		r.connectSingle(context.Background(), "a", old)

		callDone := r.EnterCall("a")
		defer callDone()
		res, ok := r.Reconcile(context.Background(),
			map[string]ScopedMcpServerConfig{"a": stdioCfg("v2")}, 100*time.Millisecond)
		if !ok {
			t.Fatal("Reconcile reported busy")
		}
		if !slices.Contains(res.DrainedLate, "a") {
			t.Fatalf("DrainedLate = %v, must contain a (drain timed out with a call in flight)", res.DrainedLate)
		}
		if got := h.count("mcp: reconcile drain timed out, disconnecting anyway"); got != 1 {
			t.Fatalf("drain-timeout warn count = %d, want 1", got)
		}
	})
}

func TestReconcileRealConnectErrorAdoptsNothing(t *testing.T) {
	h := captureSlog(t)
	mgr := NewClientManager(&selectivePanicProvider{panicNames: map[string]bool{"boom": true}}, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{
		"boom": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	}, time.Second)
	if !ok || res == nil {
		t.Fatalf("Reconcile ok=%v res=%v", ok, res)
	}
	if _, has := r.GetConnection("boom"); has {
		t.Fatal("real connect failure must not adopt a connection entry")
	}
	if got := h.count("mcp: config reload connect failed"); got != 1 {
		t.Fatalf("connect-failed warn count = %d, want 1", got)
	}
}

func TestReconcileLogsBusySkip(t *testing.T) {
	h := captureSlog(t)
	r, _ := newReconcileRegistry(t)
	unlock := r.LockReloadForTest()
	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{}, time.Second)
	unlock()
	if ok || res != nil {
		t.Fatalf("Reconcile busy path = (%v, %v), want (nil, false)", res, ok)
	}
	if got := h.count("mcp: manual reconcile skipped, another reload in progress"); got != 1 {
		t.Fatalf("busy-skip debug log count = %d, want 1", got)
	}
}

func TestReconcileUpdatesConfigsAndAggregates(t *testing.T) {
	var refreshes atomic.Int32
	p := newRegProvider()
	t.Cleanup(p.closeAll)
	p.withTools("srv", "fresh_tool")
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{
		OnToolsChanged: func(name string, tools []DiscoveredTool) { refreshes.Add(1) },
	})
	t.Cleanup(func() { _ = r.Close() })

	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{
		"srv": stdioCfg(),
	}, time.Second)
	if !ok || res == nil {
		t.Fatalf("Reconcile ok=%v res=%v", ok, res)
	}
	if _, has := r.GetConfigs()["srv"]; !has {
		t.Fatal("Reconcile must record the new config set")
	}
	if tools := r.GetTools(); len(tools) != 1 || tools[0].OriginalName != "fresh_tool" {
		t.Fatalf("GetTools after reconcile = %v, want the server's tool (aggregates not rebuilt)", tools)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("full-refresh callback fired %d times, want exactly 1", refreshes.Load())
	}
}

func TestLockReloadForTestUnlockActuallyUnlocks(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	unlock := r.LockReloadForTest()
	unlock()
	unlock() // once 包裹：二次调用必须安全

	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{}, time.Second)
	if !ok {
		t.Fatal("reloadMu still held after unlock — once.Do(unlock) was removed?")
	}
	if res == nil {
		t.Fatal("Reconcile must return a result when not busy")
	}
}

// newReconcileRegistryWithCallbacks is newReconcileRegistry with callbacks.
func newReconcileRegistryWithCallbacks(t *testing.T, cb ChangeCallbacks) (*Registry, *reconcileProvider) {
	t.Helper()
	p := newReconcileProvider()
	t.Cleanup(p.closeAll)
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, cb)
	t.Cleanup(func() { _ = r.Close() })
	return r, p
}

// ---------------------------------------------------------------------------
// Disconnect early return (344)
// ---------------------------------------------------------------------------

func TestDisconnectUnknownServerLeavesAggregatesAndConnectionsAlone(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser}
	r.SetToolsForTest([]DiscoveredTool{{Name: "keep_me", ServerName: "srv"}})
	r.mu.Lock()
	r.connections["srv"] = &ConnectedServer{Name: "srv", Config: cfg}
	r.mu.Unlock()

	if err := r.Disconnect("ghost"); err != nil {
		t.Fatalf("Disconnect(unknown server) = %v, want nil", err)
	}
	// 早退被删时后半段照跑：rebuildAggregatesLocked 用发现缓存重置聚合，
	// SetToolsForTest 注入（无缓存）的工具就此蒸发。
	if tools := r.GetTools(); len(tools) != 1 || tools[0].Name != "keep_me" {
		t.Fatalf("GetTools after Disconnect(unknown) = %v, want the injected tool intact", tools)
	}
	if _, ok := r.GetConnection("srv"); !ok {
		t.Fatal("Disconnect(unknown) dropped an unrelated connection")
	}
	lockFreesWithin(t, r, 2*time.Second)
}

// ---------------------------------------------------------------------------
// Map-domain lock discipline (516/526/542): churn probe, dr33 形状
// ---------------------------------------------------------------------------

// TestRegistryConnectionMapReadersSerializeWithWriter keeps a lock-holding
// writer churning r.connections while every r.connections reader runs. With
// the deferred RUnlock in place the reader can never overlap a map write; if
// the defer is hoisted the read escapes the lock and the runtime map guard
// throws "concurrent map read and map write".
func TestRegistryConnectionMapReadersSerializeWithWriter(t *testing.T) {
	r, _ := newTestRegistry(ChangeCallbacks{})
	defer r.Close()

	stop := make(chan struct{})
	var writerRounds, reads atomic.Int64
	var writerDone sync.WaitGroup

	writerDone.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			r.mu.Lock()
			for k := range 64 {
				name := fmt.Sprintf("srv-%d-%d", k, i&3)
				r.connections[name] = &ConnectedServer{Name: name}
			}
			r.mu.Unlock()
			writerRounds.Add(1)
		}
	})

	readers := []struct {
		name string
		call func()
	}{
		{"GetConnection", func() { _, _ = r.GetConnection("srv-3-1") }},
		{"HasResourceSupport", func() { _ = r.HasResourceSupport() }},
		{"PendingServerNames", func() { _ = r.PendingServerNames() }},
		{"GetConfigs", func() { _ = r.GetConfigs() }},
	}
	var wg sync.WaitGroup
	deadline := time.Now().Add(500 * time.Millisecond) // REAL-TIME: churn probe window
	for _, rd := range readers {
		wg.Add(1)
		go func(call func()) {
			defer wg.Done()
			for time.Now().Before(deadline) { // REAL-TIME: churn window
				call()
				reads.Add(1)
			}
		}(rd.call)
	}
	wg.Wait()
	close(stop)
	writerDone.Wait()

	if reads.Load() == 0 || writerRounds.Load() == 0 {
		t.Fatalf("probe made no progress: reads=%d writerRounds=%d", reads.Load(), writerRounds.Load())
	}
	if !r.mu.TryLock() {
		t.Fatal("a reader leaked the registry lock")
	}
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Agent spec loops must continue past a bad entry (649/675/681)
// ---------------------------------------------------------------------------

func agentRefFixture(t *testing.T) *Registry {
	t.Helper()
	r, _ := newTestRegistry(ChangeCallbacks{})
	t.Cleanup(func() { _ = r.Close() })
	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "x"}, Scope: ScopeUser}
	r.mu.Lock()
	r.configs["dead-a"] = cfg
	r.connections["dead-a"] = &FailedServer{Name: "dead-a", Config: cfg}
	r.configs["dead-b"] = cfg
	r.connections["dead-b"] = &FailedServer{Name: "dead-b", Config: cfg}
	r.configs["two"] = cfg
	r.connections["two"] = &ConnectedServer{Name: "two", Config: cfg}
	r.tools = append(r.tools, DiscoveredTool{Name: "tool_two", ServerName: "two"})
	r.mu.Unlock()
	return r
}

func TestConnectAgentServersContinuesPastUnparsableSpec(t *testing.T) {
	h := captureSlog(t)
	r := agentRefFixture(t)

	handle := r.ConnectAgentServers(context.Background(), "cont", []json.RawMessage{
		json.RawMessage(`123`),
		json.RawMessage(`"two"`),
	})
	if handle == nil {
		t.Fatal("a valid ref behind an unparsable spec must still yield a handle")
	}
	if n := len(handle.Tools()); n != 1 {
		t.Fatalf("handle carries %d tools (%v), want exactly [tool_two]", n, handle.Tools())
	}
	if got := h.count("agent MCP server config parse failed"); got != 1 {
		t.Fatalf("parse-failed warn count = %d, want 1", got)
	}
}

func TestConnectAgentServersContinuesPastMissingRef(t *testing.T) {
	h := captureSlog(t)
	r := agentRefFixture(t)

	// 两个缺失引用 + 一个有效引用：refSet 迭代序随机，所以钉的是
	// "每个缺失引用都被记过一次" —— break 变异体只会留下一条。
	handle := r.ConnectAgentServers(context.Background(), "cont", []json.RawMessage{
		json.RawMessage(`"ghost-a"`),
		json.RawMessage(`"ghost-b"`),
		json.RawMessage(`"two"`),
	})
	if handle == nil {
		t.Fatal("valid refs behind a missing ref must still yield a handle")
	}
	if n := len(handle.Tools()); n != 1 {
		t.Fatalf("handle carries %d tools (%v), want exactly [tool_two]", n, handle.Tools())
	}
	if got := h.count("agent MCP server ref not found"); got != 2 {
		t.Fatalf("ref-not-found warn count = %d, want 2 (one per missing ref)", got)
	}
}

func TestConnectAgentServersContinuesPastUnconnectedRef(t *testing.T) {
	h := captureSlog(t)
	r := agentRefFixture(t)

	handle := r.ConnectAgentServers(context.Background(), "cont", []json.RawMessage{
		json.RawMessage(`"dead-a"`),
		json.RawMessage(`"dead-b"`),
		json.RawMessage(`"two"`),
	})
	if handle == nil {
		t.Fatal("valid refs behind an unconnected ref must still yield a handle")
	}
	if n := len(handle.Tools()); n != 1 {
		t.Fatalf("handle carries %d tools (%v), want exactly [tool_two]", n, handle.Tools())
	}
	if got := h.count("agent MCP server ref not connected"); got != 2 {
		t.Fatalf("ref-not-connected warn count = %d, want 2 (one per unconnected ref)", got)
	}
}

func TestConnectAgentServersLogsInlineConnectCount(t *testing.T) {
	h := captureSlog(t)
	p := newRegProvider()
	t.Cleanup(p.closeAll)
	p.withTools("agent-logcnt-0", "t0")
	p.withTools("agent-logcnt-1", "t1")

	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	handle := r.ConnectAgentServers(context.Background(), "logcnt", []json.RawMessage{
		json.RawMessage(`{"command": "c0"}`),
		json.RawMessage(`{"command": "c1"}`),
	})
	if handle == nil {
		t.Fatal("expected a handle for two inline servers")
	}
	defer func() { _ = handle.Cleanup() }()

	if got := h.count("agent MCP inline servers connected"); got != 1 {
		t.Fatalf("inline-connected info log count = %d, want 1", got)
	}
	if v, ok := h.intAttr("agent MCP inline servers connected", "count"); !ok || v != 2 {
		t.Fatalf("inline-connected count attr = %d (ok=%v), want 2", v, ok)
	}
}

// ---------------------------------------------------------------------------
// AgentServerHandle.cleanup closure (765/766/769) — the production closure,
// not a test-local copy of it.
// ---------------------------------------------------------------------------

// injectFailingCleanup swaps a connected server for an equivalent entry whose
// Cleanup fails, so Disconnect (and therefore the handle's cleanup closure)
// surfaces a real error.
func injectFailingCleanup(t *testing.T, r *Registry, name string, failure error) {
	t.Helper()
	conn, ok := r.GetConnection(name)
	if !ok {
		t.Fatalf("fixture: %q is not connected", name)
	}
	cs, ok := conn.(*ConnectedServer)
	if !ok {
		t.Fatalf("%q = %T, want *ConnectedServer", name, conn)
	}
	r.SetConnectionForTest(name, &ConnectedServer{
		Name:    cs.Name,
		Config:  cs.Config,
		Cleanup: func() error { return failure },
	})
}

func TestAgentHandleCleanupPropagatesRealDisconnectFailure(t *testing.T) {
	boom := errors.New("close refused")
	p := newRegProvider()
	t.Cleanup(p.closeAll)
	p.withTools("agent-cleanuperr-0", "t0")

	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	handle := r.ConnectAgentServers(context.Background(), "cleanuperr", []json.RawMessage{
		json.RawMessage(`{"command": "c"}`),
	})
	if handle == nil {
		t.Fatal("expected a handle for the inline server")
	}
	injectFailingCleanup(t, r, "agent-cleanuperr-0", boom)

	if err := handle.Cleanup(); !errors.Is(err, boom) {
		t.Fatalf("Cleanup error = %v, want %v (Disconnect error must reach the caller)", err, boom)
	}
	if _, ok := r.GetConnection("agent-cleanuperr-0"); ok {
		t.Fatal("a failing cleanup must still drop the connection entry")
	}
}

func TestAgentHandleCleanupKeepsFirstErrorAcrossServers(t *testing.T) {
	errA := errors.New("first server close refused")
	errB := errors.New("second server close refused")
	p := newRegProvider()
	t.Cleanup(p.closeAll)
	p.withTools("agent-twerr-0", "t0")
	p.withTools("agent-twerr-1", "t1")

	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	defer r.Close()

	handle := r.ConnectAgentServers(context.Background(), "twerr", []json.RawMessage{
		json.RawMessage(`{"command": "c0"}`),
		json.RawMessage(`{"command": "c1"}`),
	})
	if handle == nil {
		t.Fatal("expected a handle for two inline servers")
	}
	injectFailingCleanup(t, r, "agent-twerr-0", errA)
	injectFailingCleanup(t, r, "agent-twerr-1", errB)

	if err := handle.Cleanup(); !errors.Is(err, errA) {
		t.Fatalf("Cleanup error = %v, want the first failure %v", err, errA)
	}
}

// ---------------------------------------------------------------------------
// closeInner timer stop (808)
// ---------------------------------------------------------------------------

// refusedTransportProvider records every transport attempt: a reconnect timer
// that Close failed to stop shows up as a connect attempt after shutdown.
type refusedTransportProvider struct{ calls atomic.Int32 }

func (p *refusedTransportProvider) NewTransport(name string, cfg McpServerConfig, scope ConfigScope, trusted bool) (mcp.Transport, error) {
	p.calls.Add(1)
	return nil, fmt.Errorf("mock: no transport for %q", name)
}

func TestCloseStopsPendingReconnectTimersBeforeTheyFire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		origMin := reconnectMinBackoff
		reconnectMinBackoff = 20 * time.Millisecond
		defer func() { reconnectMinBackoff = origMin }()

		p := &refusedTransportProvider{}
		mgr := NewClientManager(p, true, "")
		r := NewRegistry(mgr, ChangeCallbacks{})
		t.Cleanup(func() { _ = r.Close() })
		r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
			"remote": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
		})

		r.ScheduleReconnect("remote", 0)
		r.mu.RLock()
		scheduled := len(r.reconnectTimers)
		r.mu.RUnlock()
		if scheduled != 1 {
			t.Fatalf("fixture: reconnectTimers = %d, want 1", scheduled)
		}

		if err := r.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		// 越过退避窗：被 Stop 的定时器绝不能补一次连接。
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()

		if calls := p.calls.Load(); calls != 0 {
			t.Fatalf("transport attempts after Close = %d, want 0 (timer not stopped)", calls)
		}
		if _, ok := r.GetConnection("remote"); ok {
			t.Fatal("a post-Close reconnect attempt re-registered a connection")
		}
	})
}

// ---------------------------------------------------------------------------
// Config set replacement (1181 / 1362)
// ---------------------------------------------------------------------------

func TestConfigReloadDropsRemovedServerFromConfigs(t *testing.T) {
	f := newReloadFixture(t, ChangeCallbacks{})
	f.addServer(t, "srv")
	f.addServer(t, "gone")
	f.write(`{"mcpServers":{"srv":{"command":"echo"},"gone":{"command":"echo"}}}`)

	configs, _ := GetProjectMcpConfigsFromCwd(f.dir)
	f.r.ConnectAll(context.Background(), configs)
	if _, has := f.r.GetConfigs()["gone"]; !has {
		t.Fatal("fixture: gone must be configured before the reload")
	}

	f.write(`{"mcpServers":{"srv":{"command":"echo"}}}`)
	f.r.handleConfigReload()

	if _, has := f.r.GetConfigs()["gone"]; has {
		t.Fatal("removed server must leave the config set after reload")
	}
	if _, has := f.r.GetConfigs()["srv"]; !has {
		t.Fatal("kept server must remain in the config set")
	}
}

func TestReconcileDropsRemovedServerFromConfigs(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"keep": stdioCfg(),
		"gone": stdioCfg(),
	})

	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{
		"keep": stdioCfg(),
	}, time.Second)
	if !ok || res == nil {
		t.Fatalf("Reconcile ok=%v res=%v", ok, res)
	}
	if _, has := r.GetConfigs()["gone"]; has {
		t.Fatal("Reconcile must drop the removed server from the config set")
	}
	if _, has := r.GetConfigs()["keep"]; !has {
		t.Fatal("Reconcile must keep the retained server in the config set")
	}
}
