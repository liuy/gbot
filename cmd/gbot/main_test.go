package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tui"
	"github.com/liuy/gbot/pkg/types"
)

// freePort returns a TCP port that is free at call time.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// dialReachable waits until the pprof HTTP server is accepting connections
// on addr, or fails the test after a short timeout. Returns the baseURL
// (with scheme) for convenience.
func dialReachable(t *testing.T, addr string) string {
	t.Helper()
	timeoutCh := time.After(2 * time.Second)
	for {
		select {
		case <-timeoutCh:
			t.Fatalf("pprof server at %s never became reachable", addr)
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return "http://" + addr
		}
	}
}

// TestStartPprofServer_Default listens on a free port and verifies
// the server responds with the pprof index page.
func TestStartPprofServer_Default(t *testing.T) {
	addr := freePort(t)
	startPprofServer(addr)

	base := dialReachable(t, addr)
	resp, err := http.Get(base + "/debug/pprof/")
	if err != nil {
		t.Fatalf("HTTP GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Types of profiles available") {
		t.Errorf("pprof index page missing expected text; got %q", truncate(string(body), 200))
	}
}

// TestStartPprofServer_Disabled verifies that addr "off" skips starting
// the server (no listener is created).
func TestStartPprofServer_Disabled(t *testing.T) {
	startPprofServer("off")
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6060", 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Skip("default :6060 already has a listener (perhaps from another test); cannot assert absence reliably")
	}
}

// TestStartPprofServer_EnvOverride verifies GBOT_PPROF_ADDR takes precedence
// over the cfg argument.
func TestStartPprofServer_EnvOverride(t *testing.T) {
	envAddr := freePort(t)
	t.Setenv("GBOT_PPROF_ADDR", envAddr)

	// Pass a different addr; env should win.
	cfgAddr := freePort(t)
	startPprofServer(cfgAddr)

	// envAddr must be reachable.
	dialReachable(t, envAddr)

	// cfgAddr should NOT be reachable (env won).
	conn, err := net.DialTimeout("tcp", cfgAddr, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Errorf("cfgAddr %s unexpectedly has a listener; env override should have won", cfgAddr)
	}
}

// TestStartPprofServer_HeapProfile verifies /debug/pprof/heap returns
// valid output — the main motivating use case for the server.
func TestStartPprofServer_HeapProfile(t *testing.T) {
	addr := freePort(t)
	startPprofServer(addr)
	base := dialReachable(t, addr)

	req, _ := http.NewRequestWithContext(context.Background(), "GET", base+"/debug/pprof/heap", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("heap profile GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heap status = %d, want 200", resp.StatusCode)
	}
	// pprof heap endpoint returns binary protobuf by default; just check non-empty.
	data, _ := io.ReadAll(resp.Body)
	if len(data) == 0 {
		t.Error("heap profile body is empty")
	}
}

// spyProvider captures the model name from LLM calls for assertion.
type spyProvider struct {
	mu            sync.Mutex
	models        []string
	streamCalls   int   // Stream calls (main query)
	completeCalls int   // Complete calls (compact - uses Complete path)
}

func (s *spyProvider) Stream(_ context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	s.mu.Lock()
	s.streamCalls++
	s.models = append(s.models, req.Model)
	s.mu.Unlock()
	ch := make(chan llm.StreamEvent, 10)
	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: ""}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "OK"}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", Usage: &types.Usage{InputTokens: 100, OutputTokens: 10}, DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}}
		ch <- llm.StreamEvent{Type: "message_stop"}
		close(ch)
	}()
	return ch, nil
}

func (s *spyProvider) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	s.models = append(s.models, req.Model)
	return &llm.Response{
		ID:         "spy-resp",
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    []types.ContentBlock{types.NewTextBlock("Summary of conversation.")},
		StopReason: "end_turn",
		Usage:      types.Usage{InputTokens: 100, OutputTokens: 10},
	}, nil
}

// TestRestoreEngines_CompactorUsesCorrectModel verifies that after restore,
// when a compaction is triggered, the compactor sends the correct model
// to the LLM provider — not the config default. This is the red light for
// the bug where compactor was nil after restart (factory didn't set it
// because SessionID was empty), so compaction never triggered.
func TestRestoreEngines_CompactorUsesCorrectModel(t *testing.T) {
	projectDir := t.TempDir()

	// Create store + session for the engine.
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sess, err := store.CreateSessionWithEngine(projectDir, "glm-5", "main")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Seed meta.json with provider/model format.
	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: sess.SessionID},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}

	spy := &spyProvider{}
	mgr := engine.NewEngineManager()
	deps := restoreEnginesDeps{
		mgr:        mgr,
		workingDir: projectDir,
		store:      store,
		model:      "zhipu/glm-5",
		factory: func(id, name, prov, model string) (*engine.Engine, *tui.TUIHandler, error) {
			hub, handler := tui.NewEngineHubWithHandler(id, nil)
			eng := engine.New(&engine.Params{
				Provider:    spy,
				Logger:      slog.Default(),
				Model:       model,
				EngineID:    id,
				Dispatcher:  hub,
				TokenBudget: 5000,
				AutoCompact: engine.AutoCompactConfig{
					ContextWindow: 5000,
				},
			})
			return eng, handler, nil
		},
	}

	_ = restoreEngines(deps)

	// Get the restored engine, seed messages, and verify compaction.
	eng := mgr.Get("main").Engine
	if eng == nil {
		t.Fatal("main engine is nil")
	}
	// Seed many messages to trigger compaction (20 messages × ~100 tokens each).
	msgs := make([]types.Message, 0, 20)
	for range 20 {
		msgs = append(msgs, types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("task content ", 100))},
		})
	}
	eng.SetMessages(msgs)

	// Verify compactor is set (not nil — this is the red light).
	// Without compactor, compaction never triggers → provider never called.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = eng.QuerySync(ctx, "continue", "")

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.completeCalls == 0 {
		t.Fatal("compactor never called Complete() — compactor was not set after restore " +
			"(factory created engine without compactor because SessionID was empty)")
	}
	// Verify correct model was used for compaction.
	if len(spy.models) == 0 || !slices.Contains(spy.models, "glm-5") {
		t.Errorf("compactor used models = %v, want to contain glm-5", spy.models)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestRestoreEngines_StripsProviderPrefix verifies that the engine factory
// receives the bare model registration name (e.g. "glm-5.2"), not the
// "provider/model" form stored in meta.json (e.g. "zhipu/glm-5.2").
//
// Without this strip, Engine.Model() returns the prefixed form, the status
// bar shows "zhipu/glm-5.2" instead of "glm-5.2", and the provider API may
// reject the prefixed model name.
func TestRestoreEngines_StripsProviderPrefix(t *testing.T) {
	projectDir := t.TempDir()

	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "zhipu/glm-5.2"},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}

	var gotModel string
	deps := restoreEnginesDeps{
		mgr:        engine.NewEngineManager(),
		workingDir: projectDir,
		factory: func(id, name, provider, model string) (*engine.Engine, *tui.TUIHandler, error) {
			gotModel = model
			hub, handler := tui.NewEngineHubWithHandler(id, nil)
			eng := engine.New(&engine.Params{
				Logger:     slog.Default(),
				Model:      model,
				EngineID:   id,
				Dispatcher: hub,
			})
			return eng, handler, nil
		},
	}

	_ = restoreEngines(deps)

	if gotModel != "glm-5.2" {
		t.Errorf("factory received model = %q, want %q (provider prefix stripped)",
			gotModel, "glm-5.2")
	}
}

// TestRestoreEngines_StripsOpenRouterNestedPrefix verifies the strip works
// for providers whose model name itself contains a slash. OpenRouter's
// models are registered as "openrouter/owl-alpha" — when stored in meta.json
// as "openrouter/openrouter/owl-alpha", the factory must receive
// "openrouter/owl-alpha" (the full registration name), not just "owl-alpha".
func TestRestoreEngines_StripsOpenRouterNestedPrefix(t *testing.T) {
	projectDir := t.TempDir()

	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "openrouter/openrouter/owl-alpha"},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}

	var gotModel string
	deps := restoreEnginesDeps{
		mgr:        engine.NewEngineManager(),
		workingDir: projectDir,
		factory: func(id, name, provider, model string) (*engine.Engine, *tui.TUIHandler, error) {
			gotModel = model
			hub, handler := tui.NewEngineHubWithHandler(id, nil)
			eng := engine.New(&engine.Params{
				Logger:     slog.Default(),
				Model:      model,
				EngineID:   id,
				Dispatcher: hub,
			})
			return eng, handler, nil
		},
	}

	_ = restoreEngines(deps)

	if gotModel != "openrouter/owl-alpha" {
		t.Errorf("factory received model = %q, want %q (only gbot provider prefix stripped, model's own prefix preserved)",
			gotModel, "openrouter/owl-alpha")
	}
}

// TestRestoreEngines_VSModelMatchesMetaJson verifies that the EngineViewState
// created by restoreEngines stores the full "provider/model" from meta.json
// (not the bare registration name from engine.Model()). Without this,
// switchEngine's persistWorkspaceMeta overwrites the correct "provider/model"
// with a bare name, breaking restart.
func TestRestoreEngines_VSModelMatchesMetaJson(t *testing.T) {
	projectDir := t.TempDir()
	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "openrouter/openrouter/owl-alpha"},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}

	mgr := engine.NewEngineManager()
	deps := restoreEnginesDeps{
		mgr:        mgr,
		workingDir: projectDir,
		factory: func(id, name, provider, model string) (*engine.Engine, *tui.TUIHandler, error) {
			hub, handler := tui.NewEngineHubWithHandler(id, nil)
			eng := engine.New(&engine.Params{
				Logger: slog.Default(), Model: model, EngineID: id, Dispatcher: hub,
			})
			return eng, handler, nil
		},
	}

	_ = restoreEngines(deps)

	vs := mgr.Get("main")
	if vs == nil {
		t.Fatal("main not registered")
	}
	if vs.Model != "openrouter/openrouter/owl-alpha" {
		t.Errorf("vs.Model = %q, want openrouter/openrouter/owl-alpha (full provider/model from meta.json, not bare %q)",
			vs.Model, vs.Engine.Model())
	}
}
