package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/connector/wechat"
	"github.com/liuy/gbot/pkg/project"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/task"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool/repl"
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
	StartPprofServer(addr, nil)

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
	StartPprofServer("off", nil)
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
	StartPprofServer(cfgAddr, nil)

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
	StartPprofServer(addr, nil)
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
	streamCalls   int // Stream calls (main query)
	completeCalls int // Complete calls (compact - uses Complete path)
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

func (s *spyProvider) Name() string { return "spy" }

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
func TestNewAppWithManager_SyncsEffortToStatusBar(t *testing.T) {
	// Startup pushes the engine's resolved effort into the status bar — a
	// migrated per-model baseline must be visible without a /think round-trip.
	mgr := engine.NewEngineManager()
	hub, _ := tui.NewEngineHubWithHandler("main", nil)
	eng := engine.New(&engine.Params{
		Provider:    &spyProvider{},
		Logger:      slog.Default(),
		Model:       "zhipu/glm-5.3",
		EngineID:    "main",
		Dispatcher:  hub,
		TokenBudget: 5000,
	})
	if err := eng.SetThinking(llm.EffortNone); err != nil {
		t.Fatalf("SetThinking: %v", err)
	}
	mgr.Add(&engine.EngineViewState{
		Engine: eng,
		ID:     "main",
		Name:   "main",
		Model:  "zhipu/glm-5.3",
	})
	a := tui.NewAppWithManager(mgr, "", hub)
	if got := a.StatusEffort(); got != llm.EffortNone {
		t.Errorf("status effort = %q, want none (startup sync)", got)
	}
}

func TestRestoreEngines_ThinkingOverrideRoundTrip(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.CreateSessionWithEngine(projectDir, "glm-5", "main")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// A persisted sticky effort must come back as the engine's override —
	// restart must not silently drop the user's manual choice.
	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: sess.SessionID, Thinking: "none"},
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
		projectDir: projectDir,
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
				AutoCompact: engine.AutoCompactConfig{ContextWindow: 5000},
			})
			return eng, handler, nil
		},
	}
	restoreEngines(deps)
	vs := mgr.Get("main")
	if vs == nil || vs.Engine == nil {
		t.Fatal("main engine not restored")
	}
	if got := vs.Engine.Thinking(); got != llm.EffortNone {
		t.Errorf("restored effort = %q, want none", got)
	}
	// A garbage value from a hand-edited meta.json must neither reach the
	// engine nor ride the view-state back into the next PersistMeta.
	seed.Engines = append(seed.Engines, short.EngineMeta{
		ID: "ghost", Name: "ghost", Model: "zhipu/glm-5", Thinking: "bogus",
	})
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("re-WriteWorkspaceMeta: %v", err)
	}
	mgr2 := engine.NewEngineManager()
	deps.mgr = mgr2
	deps.factory = func(id, name, prov, model string) (*engine.Engine, *tui.TUIHandler, error) {
		h2, handler := tui.NewEngineHubWithHandler(id, nil)
		e2 := engine.New(&engine.Params{
			Provider: spy, Logger: slog.Default(), Model: model, EngineID: id,
			Dispatcher: h2, TokenBudget: 5000,
			AutoCompact: engine.AutoCompactConfig{ContextWindow: 5000},
		})
		return e2, handler, nil
	}
	restoreEngines(deps)
	ghost := mgr2.Get("ghost")
	if ghost == nil || ghost.Engine == nil {
		t.Fatal("ghost engine not restored")
	}
	if got := ghost.Engine.Thinking(); got != llm.EffortAuto {
		t.Errorf("bogus value leaked to engine: Thinking() = %q, want auto", got)
	}
	if got := ghost.Thinking; got != "" {
		t.Errorf("bogus value rides view-state: %q, want empty (no round-trip)", got)
	}
}

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
		projectDir: projectDir,
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

// TestStartSessionCleanup verifies the shared-startup cleanup loop deletes
// expired sessions while preserving fresh active ones.
func TestStartSessionCleanup(t *testing.T) {
	projectDir := t.TempDir()

	store, err := short.NewStore(filepath.Join(projectDir, "memory.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Old session: updated 31 days ago (past the 30d cutoff), carries a message
	// so only CleanupOldSessions — not PruneEmptySessions — may remove it.
	oldSes, err := store.CreateSessionWithEngine(projectDir, "test-model", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine(old): %v", err)
	}
	oldMsg := &short.TranscriptMessage{
		UUID:      "old-uuid-1",
		Type:      "user",
		Content:   `{"type":"text","text":"old"}`,
		CreatedAt: time.Now(), // REAL-TIME: message timestamps share the wall clock CleanupOldSessions cuts against
	}
	if err := store.AppendMessage(oldSes.SessionID, oldMsg); err != nil {
		t.Fatalf("AppendMessage(old): %v", err)
	}
	// Backdate AFTER AppendMessage, which resets updated_at via the FTS update.
	if _, err := store.DB().Exec(
		"UPDATE sessions SET updated_at = datetime('now', '-31 days') WHERE session_id = ?",
		oldSes.SessionID,
	); err != nil {
		t.Fatalf("backdate old session: %v", err)
	}

	// Recent session with a message: must survive the cleanup pass untouched.
	recentSes, err := store.CreateSessionWithEngine(projectDir, "test-model", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine(recent): %v", err)
	}
	recentMsg := &short.TranscriptMessage{
		UUID:      "recent-uuid-1",
		Type:      "user",
		Content:   `{"type":"text","text":"recent"}`,
		CreatedAt: time.Now(), // REAL-TIME: keeps updated_at fresh against the real cutoff
	}
	if err := store.AppendMessage(recentSes.SessionID, recentMsg); err != nil {
		t.Fatalf("AppendMessage(recent): %v", err)
	}

	// Empty session: /clear orphan with no messages and no title → pruned.
	// Fresh empty session: created just now with no messages and no title —
	// the exact shape of a startup-created active session. The loop must NOT
	// prune it (empty-session pruning stays on-demand in the TUI picker); if
	// it did, the user's first message would fail the sessions FK.
	emptySes, err := store.CreateSessionWithEngine(projectDir, "test-model", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine(empty): %v", err)
	}

	startSessionCleanup(store, 10*time.Millisecond, 24*time.Hour)

	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: bounded wait for the goroutine's real firstDelay
	for {
		_, oldErr := store.GetSession(oldSes.SessionID)
		if oldErr != nil {
			break
		}
		if time.Now().After(deadline) { // REAL-TIME: timeout makes the poll a hard assertion, not a flake
			t.Fatalf("cleanup loop did not delete targets within 2s: oldErr=%v", oldErr)
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: re-check loop for the goroutine's wall-clock sleep
	}

	if _, err := store.GetSession(oldSes.SessionID); err == nil {
		t.Errorf("old session %s (updated_at 31d ago) survived CleanupOldSessions", oldSes.SessionID)
	}
	if _, err := store.GetSession(emptySes.SessionID); err != nil {
		t.Errorf("fresh empty session %s was pruned, want kept (active sessions must survive until first message)", emptySes.SessionID)
	}
	recent, err := store.GetSession(recentSes.SessionID)
	if err != nil {
		t.Fatalf("recent session %s was deleted, want kept: %v", recentSes.SessionID, err)
	}
	if recent.SessionID != recentSes.SessionID {
		t.Errorf("GetSession returned session %s, want %s", recent.SessionID, recentSes.SessionID)
	}
	exists, err := store.MessageExists(recentSes.SessionID, "recent-uuid-1")
	if err != nil {
		t.Fatalf("MessageExists(recent-uuid-1): %v", err)
	}
	if !exists {
		t.Errorf("recent session's message recent-uuid-1 missing after cleanup")
	}
}

// TestBuildModelThinking pins the config-side migration: legacy values map
// onto the effort axis, empty is skipped, unknown values are dropped (the
// model falls back to auto).
func TestBuildModelThinking(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{
				Name: "p1",
				Models: config.NewModelsFromMap(map[string]config.ModelConfig{
					"model-a": {Thinking: "adaptive"},
					"model-b": {Thinking: "disabled"},
					"model-c": {Thinking: "high"},
					"model-d": {},
					"model-e": {Thinking: "bogus"},
				}),
			},
		},
	}

	got := buildModelThinking(cfg)
	if len(got) != 3 {
		t.Fatalf("len(buildModelThinking) = %d, want 3 (d skipped empty, e skipped unknown): %v", len(got), got)
	}
	want := map[string]llm.Effort{
		"model-a": llm.EffortAuto,
		"model-b": llm.EffortNone,
		"model-c": llm.EffortHigh,
	}
	for name, effort := range want {
		if got[name] != effort {
			t.Errorf("buildModelThinking[%q] = %q, want %q", name, got[name], effort)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestRestoreEngines_UsesModelContextWindow verifies that the engine factory
// uses the context window matching the RESTORED engine's model config, not
// the default model's context window.
//
// Setup: providers have zhipu/glm-5.2 (1M), deepseek/deepseek-v4-flash (500K).
// Factory looks up providerCfg and calls ResolveContext, same as production.
func TestRestoreEngines_UsesModelContextWindow(t *testing.T) {
	projectDir := t.TempDir()

	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "deepseek/deepseek-v4-flash"},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}

	// Replicate what main() does: load providers, build factory that
	// resolves context from the engine's actual provider+model.
	providers := []config.Provider{
		{
			Name: "zhipu",
			Models: config.NewModelsFromMap(map[string]config.ModelConfig{
				"glm-5.2": {Context: 1048576},
			}),
		},
		{
			Name: "deepseek",
			Models: config.NewModelsFromMap(map[string]config.ModelConfig{
				"deepseek-v4-flash": {Context: 512000},
			}),
		},
	}

	mgr := engine.NewEngineManager()
	deps := restoreEnginesDeps{
		mgr:        mgr,
		projectDir: projectDir,
		model:      "zhipu/glm-5.2",
		factory: func(id, name, providerName, modelArg string) (*engine.Engine, *tui.TUIHandler, error) {
			var providerCfg *config.Provider
			if providerName == "" {
				providerCfg = &providers[0] // fallback to zhipu
			} else {
				for i := range providers {
					if providers[i].Name == providerName {
						providerCfg = &providers[i]
						break
					}
				}
				if providerCfg == nil {
					providerCfg = &providers[0]
				}
			}
			// SAME logic as production engineFactory
			engCtxWindow := providerCfg.ResolveContext(modelArg)
			t.Logf("provider=%q model=%q resolved context=%d", providerName, modelArg, engCtxWindow)

			hub, handler := tui.NewEngineHubWithHandler(id, nil)
			eng := engine.New(&engine.Params{
				Logger:      slog.Default(),
				Model:       modelArg,
				EngineID:    id,
				Dispatcher:  hub,
				TokenBudget: engCtxWindow,
				MaxTokens:   4096,
				AutoCompact: engine.AutoCompactConfig{ContextWindow: engCtxWindow},
			})
			return eng, handler, nil
		},
	}

	_ = restoreEngines(deps)
	eng := mgr.Get("main").Engine
	if got := eng.TokenBudget(); got != 512000 {
		t.Errorf("TokenBudget = %d, want 512000 (500K for deepseek-v4-flash)", got)
	}
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
		projectDir: projectDir,
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
		projectDir: projectDir,
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
		projectDir: projectDir,
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

// TestStartCleanupWired pins the wiring: the cleanup loop must be invoked
// from Start() — the shared startup path. It previously lived under the TUI
// only, so wui-only memory.db never got pruned; a plain text scan is enough
// to catch the call being dropped or moved back.
func TestStartCleanupWired(t *testing.T) {
	src, err := os.ReadFile("start.go")
	if err != nil {
		t.Fatalf("read start.go: %v", err)
	}
	if !strings.Contains(string(src), "startSessionCleanup(store,") {
		t.Error("Start() no longer wires startSessionCleanup — wui/daemon users lose 30-day session cleanup")
	}
}

// TestStartSharedIDMinterWired pins the SharedDeps/dreamEngineDeps literals:
// every IDMinter field must carry the one shared idMinter. A dropped or nil
// field hands that engine's background table a private counter, so two
// engines mint the same agent-N id and message/job addressing collides across
// engines. Start() cannot run in tests, so this follows TestStartCleanupWired's
// source-scan pattern. WireEngine is not in the count: it takes the MCP
// registry alone, so no call site there can carry a divergent minter.
func TestStartSharedIDMinterWired(t *testing.T) {
	src, err := os.ReadFile("start.go")
	if err != nil {
		t.Fatalf("read start.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	count := 0
	for i, line := range lines {
		if !strings.Contains(line, "IDMinter:") {
			continue
		}
		count++
		rhs := strings.TrimSuffix(strings.TrimSpace(line[strings.Index(line, ":")+1:]), ",")
		if rhs != "idMinter" && rhs != "d.IDMinter" {
			t.Errorf("start.go:%d: IDMinter rhs = %q, want the shared idMinter (nil mints a colliding private counter)", i+1, rhs)
		}
	}
	if count != 4 {
		t.Errorf("start.go has %d IDMinter literal fields, want 4 (main, factory, dream call site, dream CreateTools)", count)
	}
}

// Restore must recover ENGINE-scoped from bad recorded sessions and rewrite
// meta.json in the same startup. Seeds both real incidents: a VALID row
// owned by another engine (agent-3 adopted main's chat after a buggy
// fallback persisted it) and a pure ghost id with no DB row.
func TestRestoreEngines_GhostSessionSelfHeals(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const ghost = "7469e5fe-0000-0000-0000-000000000000"
	// Mirror the real incident: main is the active engine with its own live
	// session (meta.CurrentSessionID dual-writes it), e3 carries a ghost id.
	mainSess, err := store.CreateSessionWithEngine(projectDir, "glm-5", "main")
	if err != nil {
		t.Fatalf("CreateSession main: %v", err)
	}
	// main's session has real history — that's what makes it "resumable"
	// for the global ResumeOrInitSession fallback.
	mainMsgs := short.EngineMessagesToStore([]types.Message{{
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.NewTextBlock("hello from main")},
		Timestamp: time.Now(), // REAL-TIME: fresh fixture timestamp, recency not under test
	}})
	if err := store.AppendMessages(mainSess.SessionID, mainMsgs); err != nil {
		t.Fatalf("seed main messages: %v", err)
	}
	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: mainSess.SessionID},
			// The state after one buggy restart: e3's entry now holds MAIN's
			// session id — a VALID row owned by a DIFFERENT engine. This is
			// stuck: the ghost check alone can't catch it.
			{ID: "e3", Name: "agent-3", Model: "zhipu/glm-5", ActiveSessionID: mainSess.SessionID},
			// Incident #1: a pure ghost id — no DB row at all.
			{ID: "e5", Name: "agent-5", Model: "zhipu/glm-5", ActiveSessionID: ghost},
		},
		CurrentSessionID: mainSess.SessionID,
		ActiveEngineID:   "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}

	spy := &spyProvider{}
	mgr := engine.NewEngineManager()
	deps := restoreEnginesDeps{
		mgr:        mgr,
		projectDir: projectDir,
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
				AutoCompact: engine.AutoCompactConfig{ContextWindow: 5000},
			})
			return eng, handler, nil
		},
	}
	restoreEngines(deps)

	vs := mgr.Get("e3")
	if vs == nil || vs.Engine == nil {
		t.Fatal("e3 engine not restored")
	}
	sid := vs.Engine.SessionID()
	// The fallback must be ENGINE-scoped: ResumeOrInitSession resumes
	// meta.CurrentSessionID — main's session — so e3 silently adopted
	// main's chat. Both the wrong-owner id and the pure ghost id must
	// resolve to fresh sessions.
	if sid == "" || sid == ghost || sid == mainSess.SessionID {
		t.Fatalf("active session = %q, want a fresh id (ghost %q or main %q rejected)", sid, ghost, mainSess.SessionID)
	}
	// The fresh session row must exist — persistence depends on it.
	if _, err := store.GetSession(sid); err != nil {
		t.Fatalf("fresh session row missing: %v", err)
	}
	// The pure-ghost engine recovers the same way.
	vs5 := mgr.Get("e5")
	if vs5 == nil || vs5.Engine == nil {
		t.Fatal("e5 engine not restored")
	}
	if sid5 := vs5.Engine.SessionID(); sid5 == "" || sid5 == ghost {
		t.Fatalf("e5 active session = %q, want a fresh id (ghost %q rejected)", sid5, ghost)
	}
	// meta.json rewritten in the same startup — no second restart needed.
	meta, err := short.ReadWorkspaceMeta(projectDir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta: %v", err)
	}
	for _, em := range meta.Engines {
		if em.ID == "e3" && em.ActiveSessionID != sid {
			t.Fatalf("meta.json still points at %q, want %q", em.ActiveSessionID, sid)
		}
	}
}

// TestBootSessionStartCtx_RoutesToOwningEngine pins the boot SessionStart
// routing: the dispatch ctx carries the session-owning engine's REPL, so
// the js hook evaluates in that engine's hook session rather than the
// process-global default; a session with no engine keeps the ctx bare and
// the default runner serves it.
func TestBootSessionStartCtx_RoutesToOwningEngine(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	newBootEngine := func(id string) (*engine.Engine, *repl.REPLTool) {
		t.Helper()
		eng := engine.New(&engine.Params{EngineID: id, Logger: slog.Default()})
		t.Cleanup(func() { eng.Close() })
		eng.SetStore(store, dir)
		if err := eng.NewSession(dir, ""); err != nil {
			t.Fatalf("%s NewSession: %v", id, err)
		}
		replTool := repl.New()
		t.Cleanup(replTool.Close)
		eng.SetToolRefs(engine.ToolRefs{REPL: replTool})
		mgr.Add(&engine.EngineViewState{Engine: eng, ID: id, Name: id})
		return eng, replTool
	}
	engA, replA := newBootEngine("main")
	_, replB := newBootEngine("e2")

	defaultRepl := repl.New()
	t.Cleanup(defaultRepl.Close)
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"SessionStart": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: `async () => { globalThis.__boot = "ran"; return "ok" }`}}},
		},
	}, &hooks.CommandExecutor{})
	hookSystem.SetJsHookRunner(defaultRepl)

	sessionID := engA.SessionID()
	hookSystem.SessionStart(bootSessionStartCtx(context.Background(), mgr, sessionID),
		&hooks.HookInput{HookEventName: string(hooks.HookSessionStart), SessionID: sessionID, Source: "startup"})

	bootState := func(rt *repl.REPLTool) string {
		t.Helper()
		out, err := rt.RunHook(context.Background(), `() => globalThis.__boot`, nil, 5*time.Second)
		if err != nil {
			t.Fatalf("readback RunHook: %v", err)
		}
		return out
	}
	if got := bootState(replA); got != `"ran"` {
		t.Errorf("engine A boot marker = %s, want \"ran\" (dispatch must use A's REPL)", got)
	}
	if got := bootState(replB); got != "null" {
		t.Errorf("engine B boot marker = %s, want null (B's session untouched)", got)
	}
	if got := bootState(defaultRepl); got != "null" {
		t.Errorf("default runner boot marker = %s, want null (ctx runner must shadow the default)", got)
	}

	hookSystem.SessionStart(bootSessionStartCtx(context.Background(), mgr, "ghost-session"),
		&hooks.HookInput{HookEventName: string(hooks.HookSessionStart), SessionID: "ghost-session", Source: "startup"})
	if got := bootState(defaultRepl); got != `"ran"` {
		t.Errorf("default runner marker after ghost dispatch = %s, want \"ran\" (no engine → global default)", got)
	}
}

// Pin tests for start.go mutation sweep; assertions are exact so a single
// changed byte flips the test. Mutant line:mutator pairs are in the sweep
// ledger.

const pinSettings = `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`

// pinHome is minimalHome with caller-provided settings JSON.
func pinHome(t *testing.T, settings string) string {
	t.Helper()
	pinSharedCaches(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir .gbot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return home
}

// pinBoot runs Start with an isolated HOME, an offline provider, pprof off
// and the WS listener on a free port; opts override the defaults.
func pinBoot(t *testing.T, settings string, opts Options) (*Instance, error) {
	return pinBootFiles(t, settings, opts, nil)
}

// pinBootFiles additionally writes files (name → content) into the temp
// project before Start (e.g. a .mcp.json).
func pinBootFiles(t *testing.T, settings string, opts Options, files map[string]string) (*Instance, error) {
	t.Helper()
	t.Setenv("HOME", pinHome(t, settings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	proj := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("pinBootFiles: getwd before chdir: %v", err)
	}
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldCwd); err != nil {
			t.Logf("pinBootFiles: restore chdir(%q): %v", oldCwd, err)
		}
	})
	inst, err := Start(opts)
	if err == nil {
		shutdownInstance(t, inst)
	}
	return inst, err
}

func pinLog(t *testing.T, inst *Instance) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(inst.ProjectDir, "gbot.log"))
	if err != nil {
		t.Fatalf("read gbot.log: %v", err)
	}
	return string(data)
}

// UserHomeDir failure must surface verbatim, not be masked by a later
// loadConfig error.
func TestPinStart_NoHomeDirFailsFast(t *testing.T) {
	pinSharedCaches(t)
	t.Setenv("HOME", "")
	t.Setenv("GBOT_PPROF_ADDR", "off")
	startInTempProject(t)
	_, err := Start(Options{NoTUI: true})
	if err == nil {
		t.Fatal("Start with empty HOME succeeded, want error")
	}
	if !strings.Contains(err.Error(), "$HOME") {
		t.Errorf("err = %v, want the UserHomeDir failure ($HOME...)", err)
	}
}

// Daemon mode isolates state under ~/.gbot/daemon (chdir + both dir vars).
func TestPinStart_DaemonChdir(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)
	home, _ := os.UserHomeDir()
	daemonDir := filepath.Join(home, ".gbot", "daemon")
	defer func() { _ = os.RemoveAll(daemonDir) }()

	inst, err := Start(Options{DaemonMode: true, NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	if inst.WorkingDir != daemonDir {
		t.Errorf("WorkingDir = %q, want %q", inst.WorkingDir, daemonDir)
	}
	if want := project.Dir(daemonDir); inst.ProjectDir != want {
		t.Errorf("ProjectDir = %q, want %q", inst.ProjectDir, want)
	}
	fi, err := os.Stat(daemonDir)
	if err != nil {
		t.Fatalf("stat daemonDir: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("daemonDir mode = %o, want 755", got)
	}
}

// Tier 3: an unparseable settings.json must boot degraded (nullProvider,
// empty provider map, empty model) instead of exiting — the Windows-desktop
// brick path. The broken file must also be preserved byte-for-byte: boot
// never rewrites settings.json, only a user save does.
func TestPinStart_BrokenSettingsBootsDegraded(t *testing.T) {
	home := pinHome(t, `{"providers":`)
	t.Setenv("HOME", home)
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)
	settingsPath := filepath.Join(home, ".gbot", "settings.json")
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings before boot: %v", err)
	}

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start with broken settings.json: %v, want degraded boot", err)
	}
	shutdownInstance(t, inst)
	if inst.Provider == nil || inst.Provider.Name() != "none" {
		t.Fatalf("Provider = %v, want the null provider (none)", inst.Provider)
	}
	if len(inst.ProviderMap) != 0 {
		t.Fatalf("ProviderMap = %v, want empty", inst.ProviderMap)
	}
	if inst.Model != "" {
		t.Fatalf("Model = %q, want empty", inst.Model)
	}
	log := pinLog(t, inst)
	if !strings.Contains(log, "settings load failed") {
		t.Errorf("gbot.log lacks the settings-load-failure WARN, got:\n%s", log)
	}
	// The tier-3 detail must reach the env holder: the wui banner reads it
	// through the holder-reading closure.
	env := inst.EnvHolder.Load()
	if !strings.Contains(env.ConfigParseWarn, "settings.json") {
		t.Errorf("Env.ConfigParseWarn = %q, want the parse-failure detail", env.ConfigParseWarn)
	}
	if env.Degraded == nil {
		t.Error("Env.Degraded = nil, want the tier-2 marker on a provider-less boot")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings after boot: %v", err)
	}
	if string(before) != `{"providers":` {
		t.Fatalf("fixture mutated before boot: %q", before)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("settings.json rewritten by boot:\nbefore %q\nafter  %q", before, after)
	}
}

// Tier 2: every provider key dead — same degraded shape as tier 3.
func TestPinStart_AllKeysBrokenBootsDegraded(t *testing.T) {
	inst, err := pinBoot(t, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["$GBOT_PIN_NO_SUCH_KEY"],"models":{"m":{}}}],"model":{"default":"t/m"}}`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start with all keys broken: %v, want degraded boot", err)
	}
	if inst.Provider == nil || inst.Provider.Name() != "none" {
		t.Fatalf("Provider = %v, want the null provider (none)", inst.Provider)
	}
	if len(inst.ProviderMap) != 0 {
		t.Fatalf("ProviderMap = %v, want empty", inst.ProviderMap)
	}
	if inst.Model != "" {
		t.Fatalf("Model = %q, want empty", inst.Model)
	}
}

// Tier 1b: the resolved provider was skipped for its broken key while a
// sibling keeps the map non-empty — boot falls back to the sibling's first
// model and logs which provider it abandoned.
func TestPinStart_KeylessResolvedProviderFallsBackToSibling(t *testing.T) {
	inst, err := pinBoot(t, `{"providers":[{"name":"p1","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m1":{}}},{"name":"p2","type":"openai","url":"http://127.0.0.1:2","keys":["$GBOT_PIN_UNSET_KEY"],"models":{"m2":{}}}],"model":{"default":"p2/m2"}}`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start with a keyless resolved provider: %v, want sibling fallback", err)
	}
	if inst.Provider == nil || inst.Provider.Name() != "p1" {
		t.Fatalf("Provider = %v, want p1", inst.Provider)
	}
	if inst.Model != "m1" {
		t.Fatalf("Model = %q, want m1", inst.Model)
	}
	if inst.PrimaryProviderCfg == nil || inst.PrimaryProviderCfg.Name != "p1" {
		t.Fatalf("PrimaryProviderCfg = %+v, want p1", inst.PrimaryProviderCfg)
	}
	log := pinLog(t, inst)
	if !strings.Contains(log, "p2") {
		t.Errorf("gbot.log does not mention the abandoned provider p2:\n%s", log)
	}
}

// Tier 1a/1b: an unresolvable default model falls back to the first usable
// model instead of failing boot.
func TestPinStart_UnresolvableDefaultModelFallsBackToFirstModel(t *testing.T) {
	inst, err := pinBoot(t, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"nope/m"}}`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start with unresolvable default model: %v, want first-model fallback", err)
	}
	if inst.Provider == nil || inst.Provider.Name() != "t" {
		t.Fatalf("Provider = %v, want t", inst.Provider)
	}
	if inst.Model != "m" {
		t.Fatalf("Model = %q, want m", inst.Model)
	}
	log := pinLog(t, inst)
	if !strings.Contains(log, "nope") {
		t.Errorf("gbot.log does not mention the unresolvable default:\n%s", log)
	}
}

// The degraded-mode user-visible contract: every query returns the canonical
// ErrNoProvider so the chat explains the one repair channel (Settings).
func TestPinStart_Degraded_QueryErrors(t *testing.T) {
	inst, err := pinBoot(t, `{"providers":`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start degraded: %v", err)
	}
	eng, _, err := inst.EngineFactory("q", "q", "", "")
	if err != nil {
		t.Fatalf("EngineFactory: %v", err)
	}
	result := eng.QuerySync(context.Background(), "hi", eng.SystemPrompt())
	if result.Error == nil {
		t.Fatal("degraded query succeeded, want ErrNoProvider")
	}
	if !errors.Is(result.Error, llm.ErrNoProvider) && !strings.Contains(result.Error.Error(), "no provider configured") {
		t.Fatalf("query error = %v, want the canonical no-provider error", result.Error)
	}
}

// Degraded boots must not spin up the background LLM subsystems: dream and
// session-notes would only produce a doomed query (plus error-log noise) on
// every tick.
func TestPinStart_Degraded_NoDreamNoSessionNotes(t *testing.T) {
	inst, err := pinBoot(t, `{"providers":`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start degraded: %v", err)
	}
	if inst.Store == nil {
		t.Fatal("Store = nil, want the short store (gates would be closed for the wrong reason)")
	}
	log := pinLog(t, inst)
	if strings.Contains(log, "dream: persistent engine started") {
		t.Errorf("dream engine started on a degraded boot:\n%s", log)
	}
	if strings.Contains(log, "session memory: wired") {
		t.Errorf("session memory wired on a degraded boot:\n%s", log)
	}
}

// The factory's empty-model fallback: a restored engine whose persisted
// Model was "" (degraded boot) adopts the boot default once settings are
// healthy again.
func TestPinStart_FactoryEmptyModelAdoptsDefault(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, _, err := inst.EngineFactory("restore", "Restore", "", "")
	if err != nil {
		t.Fatalf("EngineFactory: %v", err)
	}
	if got := eng.Model(); got != "m" {
		t.Fatalf("restored engine Model = %q, want the boot default m", got)
	}
	if eng.Provider() != inst.Provider {
		t.Fatalf("restored engine provider = %v, want the boot provider", eng.Provider())
	}
}

// The 2026-11-13 Windows incident: default model "glm-5.2" absent from the
// zhipu provider definition. Boot must succeed and fall back to the first
// declared model. Fixture rule: no declared model may contain the subsequence
// "glm52" once normalized — "glm-4.7-airx"→"glm47airx" has no 5, so the
// fuzzy fallback cannot rescue the default and the degrade path is real.
func TestPinStart_WindowsIncident(t *testing.T) {
	inst, err := pinBoot(t, `{"providers":[{"name":"zhipu","type":"openai","url":"https://open.bigmodel.cn/api/paas/v4","keys":["k"],"models":{"glm-4.7-airx":{},"glm-4.7-flash":{}}}],"model":{"default":"zhipu/glm-5.2"}}`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start with missing default model (Windows incident): %v, want fallback boot", err)
	}
	if inst.Provider == nil || inst.Provider.Name() != "zhipu" {
		t.Fatalf("Provider = %v, want zhipu", inst.Provider)
	}
	if inst.Model != "glm-4.7-airx" {
		t.Fatalf("Model = %q, want first declared model glm-4.7-airx", inst.Model)
	}
	log := pinLog(t, inst)
	if !strings.Contains(log, "glm-5.2") || !strings.Contains(log, "glm-4.7-airx") {
		t.Errorf("gbot.log WARN must name both the dead default and the fallback:\n%s", log)
	}
}

// The verbose flag must reach the installed log handler's level, and the
// Android cold-cycle exit reason must land in the log verbatim.
func TestPinStart_VerboseLevelAndPrevExitLog(t *testing.T) {
	prevLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prevLogger) })
	t.Setenv("GBOT_PREV_EXIT", "oom-pin")
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true, Verbose: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Error("default logger does not accept Debug after a Verbose boot, want debug level")
	}
	log := pinLog(t, inst)
	if !strings.Contains(log, "app: previous instance exit") || !strings.Contains(log, "oom-pin") {
		t.Errorf("gbot.log lacks the prev-exit line, got:\n%s", log)
	}
}

// A quiet boot must leave the handler at info level.
func TestPinStart_QuietBootStaysInfoLevel(t *testing.T) {
	prevLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prevLogger) })
	if _, err := pinBoot(t, pinSettings, Options{NoTUI: true, Verbose: false}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Error("default logger accepts Debug after a quiet boot, want info level")
	}
}

func TestPinStart_InstanceFields(t *testing.T) {
	inst, err := pinBootFiles(t, pinSettings, Options{NoTUI: true}, map[string]string{
		".mcp.json": `{"mcpServers":{"pinmcp":{"command":"/bin/false"}}}`,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst.EngineMgr == nil || inst.EngineMgr.Count() == 0 {
		t.Errorf("EngineMgr = %v count=%d, want non-empty", inst.EngineMgr, inst.EngineMgr.Count())
	}
	if inst.EngineFactory == nil {
		t.Error("EngineFactory nil")
	}
	if inst.Store == nil {
		t.Error("Store nil, want the short store (temp project dir)")
	}
	if inst.Cfg == nil || inst.ProviderMap == nil || len(inst.ProviderMap) != 1 {
		t.Errorf("Cfg/ProviderMap = %v/%v", inst.Cfg, inst.ProviderMap)
	}
	if inst.Provider == nil || inst.Provider.Name() != "t" {
		t.Errorf("Provider = %v, want t", inst.Provider)
	}
	if inst.Model != "m" {
		t.Errorf("Model = %q, want m", inst.Model)
	}
	if inst.PrimaryProviderCfg == nil || inst.PrimaryProviderCfg.Name != "t" {
		t.Errorf("PrimaryProviderCfg = %+v", inst.PrimaryProviderCfg)
	}
	if inst.SystemPrompt == "" {
		t.Error("SystemPrompt empty")
	}
	if !strings.Contains(inst.SystemPrompt, "[voice]") {
		t.Errorf("SystemPrompt lacks the voice block marker")
	}
	if inst.SkillListing == "" {
		t.Error("SkillListing empty")
	}
	if len(inst.ToolPrompts) == 0 {
		t.Error("ToolPrompts empty, want the enabled tools' prompts")
	}
	if inst.MainRefs == nil || inst.MainRefs.Reg == nil {
		t.Error("MainRefs/Reg nil")
	}
	if inst.HookSystem == nil {
		t.Error("HookSystem nil")
	}
	if inst.NoTUI != true {
		t.Errorf("NoTUI = %v", inst.NoTUI)
	}
	if inst.Hub == nil {
		t.Error("Hub nil")
	}
	if len(inst.MediaStores) == 0 {
		t.Error("MediaStores empty, want the wui upload cache (WS mounted)")
	}
	if inst.LSPReg == nil {
		t.Error("LSPReg nil")
	}
	if inst.Logger == nil {
		t.Error("Logger nil")
	}
	if inst.PIDCleanup == nil {
		t.Fatal("PIDCleanup nil")
	}
	pidPath := project.PIDFile(inst.ProjectDir)
	inst.PIDCleanup()
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("PID file %s survived PIDCleanup", pidPath)
	}
	if inst.Reloader == nil {
		t.Error("Reloader (orchestrator) nil")
	}
	if inst.EnvHolder == nil {
		t.Fatal("EnvHolder nil")
	}
	env := inst.EnvHolder.Load()
	if env.Cfg != inst.Cfg {
		t.Error("holder Cfg differs from inst.Cfg")
	}
	if env.ProviderMap == nil {
		t.Error("holder ProviderMap nil")
	}
	if env.Provider != inst.Provider {
		t.Error("holder Provider differs from inst.Provider")
	}
	if env.Model != "m" {
		t.Errorf("holder Model = %q, want m", env.Model)
	}
	if env.PrimaryProviderCfg != inst.PrimaryProviderCfg {
		t.Error("holder PrimaryProviderCfg differs from inst.PrimaryProviderCfg")
	}
	if env.ToolPrompts == nil {
		t.Error("holder ToolPrompts nil")
	}
	if env.SkillListing != inst.SkillListing {
		t.Error("holder SkillListing differs from inst.SkillListing")
	}
	if env.McpReg == nil {
		t.Error("holder McpReg nil")
	}
	if env.ModelThinking == nil {
		t.Error("holder ModelThinking nil")
	}
	if inst.SessionID == "" {
		t.Error("SessionID empty, want the boot session (store present)")
	}
	if inst.WorkingDir == "" || inst.ProjectDir == "" {
		t.Errorf("WorkingDir/ProjectDir = %q/%q", inst.WorkingDir, inst.ProjectDir)
	}
}

// Dream is enabled by default: the boot must register the system engine
// with the exact view-state fields the wui renders.
func TestPinStart_DreamEngineViewState(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	vs := inst.EngineMgr.Get("dream")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("dream engine missing, manager holds %v", engineIDs(inst.EngineMgr))
	}
	if vs.Name != "Dream" {
		t.Errorf("dream Name = %q, want Dream", vs.Name)
	}
	if vs.System != true {
		t.Error("dream System = false, want true")
	}
	if vs.ReadOnly != false {
		t.Error("dream ReadOnly = true, want false")
	}
	if vs.Model != "t/m" {
		t.Errorf("dream Model = %q, want t/m (display form)", vs.Model)
	}
	if vs.ActiveSessionID == "" || vs.ActiveSessionID != vs.Engine.SessionID() {
		t.Errorf("dream ActiveSessionID = %q, engine session %q", vs.ActiveSessionID, vs.Engine.SessionID())
	}
	if vs.Repl == nil {
		t.Error("dream Repl snapshot nil, want a fresh TUI repl snapshot")
	}
	if vs.CreatedAt.IsZero() {
		t.Error("dream CreatedAt zero, want boot time")
	}
	if vs.LastActiveAt.IsZero() {
		t.Error("dream LastActiveAt zero, want boot time")
	}
	if strings.TrimSpace(vs.Engine.SystemPrompt()) == "" {
		t.Error("dream engine system prompt empty, want dream.SystemPrompt")
	}
	if inst.DreamDone == nil {
		t.Fatal("DreamDone nil with dream enabled")
	}
	select {
	case <-inst.DreamDone:
		t.Error("DreamDone already closed: the dream timer goroutine exited at boot")
	default:
	}
}

// dream.disabled must keep the timer engine off the manager.
func TestPinStart_DreamDisabled(t *testing.T) {
	inst, err := pinBoot(t, `{"dream":{"disabled":true},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst.EngineMgr.Get("dream") != nil {
		t.Errorf("dream engine present after dream.disabled, manager holds %v", engineIDs(inst.EngineMgr))
	}
	if inst.DreamDone != nil {
		t.Error("DreamDone non-nil with dream disabled")
	}
}

// A broken duration string disables dream with the warn in the log.
func TestPinStart_DreamBadConfigDisables(t *testing.T) {
	inst, err := pinBoot(t, `{"dream":{"idle_threshold":"notaduration"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst.EngineMgr.Get("dream") != nil {
		t.Errorf("dream engine present despite config error, manager holds %v", engineIDs(inst.EngineMgr))
	}
	if log := pinLog(t, inst); !strings.Contains(log, "dream: config error, dream disabled") {
		t.Errorf("gbot.log lacks the dream config warn, got:\n%s", log)
	}
}

// A dream model on a second provider must win the provider/config/model
// resolution, observable through the display model and context window.
func TestPinStart_DreamModelOnSecondProvider(t *testing.T) {
	settings := `{"providers":[` +
		`{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}},` +
		`{"name":"b","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"dm":{"context":9999,"max_tokens":777}}}],` +
		`"model":{"default":"t/m"},"dream":{"model":"b/dm"}}`
	inst, err := pinBoot(t, settings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	vs := inst.EngineMgr.Get("dream")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("dream engine missing: %v", engineIDs(inst.EngineMgr))
	}
	if vs.Model != "b/dm" {
		t.Errorf("dream Model = %q, want b/dm", vs.Model)
	}
	if got := vs.Engine.Provider().Name(); got != "b" {
		t.Errorf("dream provider = %q, want b", got)
	}
	if got := vs.Engine.Model(); got != "dm" {
		t.Errorf("dream engine model = %q, want dm", got)
	}
	if got := vs.Engine.TokenBudget(); got != 9999 {
		t.Errorf("dream TokenBudget = %d, want 9999 (b/dm context_window)", got)
	}
	if got := vs.Engine.MaxTokens(); got != 777 {
		t.Errorf("dream MaxTokens = %d, want 777 (b/dm max_tokens)", got)
	}
}

// A dream model string that resolves to nothing keeps the default provider.
func TestPinStart_DreamModelResolveFailureKeepsDefault(t *testing.T) {
	settings := `{"dream":{"model":"nope/m"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`
	inst, err := pinBoot(t, settings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	vs := inst.EngineMgr.Get("dream")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("dream engine missing: %v", engineIDs(inst.EngineMgr))
	}
	if got := vs.Engine.Provider().Name(); got != "t" {
		t.Errorf("dream provider = %q, want t (default after resolve failure)", got)
	}
	if log := pinLog(t, inst); !strings.Contains(log, "dream: resolve model failed") {
		t.Errorf("gbot.log lacks the dream resolve warn, got:\n%s", log)
	}
}

// A `memory` FILE at the store path disables persistence (warn in log,
// Store nil, no session, no dream despite dream enabled, no cleanup loop).
func TestPinStart_StoreOpenFailureGates(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	projectDir := project.Dir(proj)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(projectDir, "memory", "memory.db")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "memory", "memory.db"), []byte("not a db"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	if inst.Store != nil {
		t.Error("Store non-nil despite a corrupt memory.db, want persistence disabled")
	}
	if inst.SessionID != "" {
		t.Errorf("SessionID = %q, want empty without a store", inst.SessionID)
	}
	if log := pinLog(t, inst); !strings.Contains(log, "failed to open short-term store") {
		t.Errorf("gbot.log lacks the store warn, got:\n%s", log)
	}
	// Dream is enabled by default: with NO store the timer must stay off
	// (the gate reads store != nil at CALL time).
	if inst.EngineMgr.Get("dream") != nil {
		t.Errorf("dream engine present with a nil store, manager holds %v", engineIDs(inst.EngineMgr))
	}
}

// The instance's engine factory: model/provider resolution, the voice-block
// split (wechat- engines are text channels), and session-memory wiring.
func TestPinStart_EngineFactoryAttributes(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, handler, ferr := inst.EngineFactory("pineng", "Pin", "t", "m")
	if ferr != nil {
		t.Fatalf("EngineFactory: %v", ferr)
	}
	if handler != nil {
		t.Errorf("handler = %v, want nil under NoTUI", handler)
	}
	if eng.Model() != "m" {
		t.Errorf("engine Model = %q, want m", eng.Model())
	}
	if eng.Provider().Name() != "t" {
		t.Errorf("engine provider = %q, want t", eng.Provider().Name())
	}
	if !strings.Contains(eng.SystemPrompt(), "[voice]") {
		t.Error("regular engine system prompt lacks the voice block")
	}
	if eng.SessionMemory() == nil {
		t.Error("SessionMemory nil, want wired (sessionNotes default enabled, store present)")
	}
	if eng.EngineID() != "pineng" {
		t.Errorf("engine EngineID = %q, want pineng", eng.EngineID())
	}
	if eng.TaskList() == nil {
		t.Error("engine TaskList nil, want a fresh list per engine")
	}
	if got, want := eng.TokenBudget(), inst.PrimaryProviderCfg.ResolveContext("m"); got != want {
		t.Errorf("engine TokenBudget = %d, want %d", got, want)
	}
	eng.Close()

	wcEng, _, ferr := inst.EngineFactory("wechat-pin", "WeChat", "t", "m")
	if ferr != nil {
		t.Fatalf("EngineFactory(wechat-): %v", ferr)
	}
	defer wcEng.Close()
	if strings.Contains(wcEng.SystemPrompt(), "[voice]") {
		t.Error("wechat- engine system prompt carries the voice block, want text-only")
	}
}

// A factory call with an unknown provider prefix falls back to the default
// provider with a warn (never a panic, never a nil provider).
func TestPinStart_EngineFactoryUnknownProvider(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, _, ferr := inst.EngineFactory("pineng2", "Pin", "ghost", "m")
	if ferr != nil {
		t.Fatalf("EngineFactory(ghost): %v", ferr)
	}
	defer eng.Close()
	if eng.Provider().Name() != "t" {
		t.Errorf("fallback provider = %q, want t", eng.Provider().Name())
	}
	if log := pinLog(t, inst); !strings.Contains(log, "restore: provider not found") {
		t.Errorf("gbot.log lacks the provider-not-found warn, got:\n%s", log)
	}
}

// sessionNotes.disabled removes the session memory entirely.
func TestPinStart_SessionNotesDisabled(t *testing.T) {
	settings := `{"session_notes":{"disabled":true},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`
	inst, err := pinBoot(t, settings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, _, ferr := inst.EngineFactory("pineng3", "Pin", "", "m")
	if ferr != nil {
		t.Fatalf("EngineFactory: %v", ferr)
	}
	defer eng.Close()
	if eng.SessionMemory() != nil {
		t.Error("SessionMemory wired despite sessionNotes.disabled")
	}
}

// Hooks parse failure warns but keeps booting; the command-hook env and the
// boot SessionStart hook input (stdin JSON) are exact contracts.
func TestPinStart_HooksParseWarn(t *testing.T) {
	settings := `{"hooks":"this-is-not-json","providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`
	inst, err := pinBoot(t, settings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if log := pinLog(t, inst); !strings.Contains(log, "failed to parse hooks config") {
		t.Errorf("gbot.log lacks the hooks parse warn, got:\n%s", log)
	}
}

func TestPinStart_SessionStartHookInputAndEnv(t *testing.T) {
	dir := t.TempDir()
	inFile := filepath.Join(dir, "stdin.json")
	envFile := filepath.Join(dir, "projectdir.env")
	settings := fmt.Sprintf(`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"cat > %s; printenv GBOT_PROJECT_DIR > %s"}]}]},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`,
		strings.ReplaceAll(inFile, `"`, `\"`), strings.ReplaceAll(envFile, `"`, `\"`))
	inst, err := pinBoot(t, settings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, err := os.ReadFile(inFile)
	if err != nil {
		t.Fatalf("SessionStart hook never ran (no stdin capture): %v", err)
	}
	var input hooks.HookInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("hook stdin is not HookInput JSON: %v\n%s", err, raw)
	}
	if input.HookEventName != "SessionStart" {
		t.Errorf("hook_event_name = %q, want SessionStart", input.HookEventName)
	}
	if input.SessionID != inst.SessionID {
		t.Errorf("session_id = %q, want %q", input.SessionID, inst.SessionID)
	}
	if input.Cwd != inst.WorkingDir {
		t.Errorf("cwd = %q, want %q", input.Cwd, inst.WorkingDir)
	}
	if input.Source != "startup" {
		t.Errorf("source = %q, want startup", input.Source)
	}
	pdir, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("hook env capture missing: %v", err)
	}
	if got := strings.TrimSpace(string(pdir)); got != inst.WorkingDir {
		t.Errorf("GBOT_PROJECT_DIR = %q, want %q", got, inst.WorkingDir)
	}
}

// Permission rules from user settings reach the holder's checker.
func TestPinStart_PermissionRulesLoaded(t *testing.T) {
	settings := `{"permissions":{"ask":["Bash(git push *)"]},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`
	inst, err := pinBoot(t, settings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst.EnvHolder.Load().PermissionChecker == nil {
		t.Fatal("PermissionChecker nil despite permission rules in settings")
	}
	if log := pinLog(t, inst); !strings.Contains(log, "permission rules loaded") {
		t.Errorf("gbot.log lacks the permission rules line, got:\n%s", log)
	}
}

func TestPinStart_NoPermissionRulesNoChecker(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pc := inst.EnvHolder.Load().PermissionChecker; pc != nil {
		t.Errorf("PermissionChecker = %v, want nil without rules", pc)
	}
}

// Two persisted wechat states boot two connectors; both engines land on the
// manager and meta.json is persisted.
func TestPinStart_WeChatStatesBoot(t *testing.T) {
	t.Setenv("HOME", pinHome(t, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"context":5555,"max_tokens":777}}}],"model":{"default":"t/m"}}`))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	projectDir := project.Dir(proj)
	if err := os.MkdirAll(filepath.Join(projectDir, "wechat"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, acct := range []string{"pina", "pinb"} {
		s := &wechat.State{AccountID: acct, Token: "tok", BaseURL: "http://127.0.0.1:1"}
		if err := wechat.SaveState(s, projectDir); err != nil {
			t.Fatalf("SaveState(%s): %v", acct, err)
		}
	}
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	for _, id := range []string{"wechat-pina", "wechat-pinb"} {
		vs := inst.EngineMgr.Get(id)
		if vs == nil || vs.Engine == nil {
			t.Fatalf("engine %s missing, manager holds %v", id, engineIDs(inst.EngineMgr))
		}
		if vs.Engine.Model() != "m" {
			t.Errorf("%s Model = %q, want m", id, vs.Engine.Model())
		}
		if vs.Engine.Provider() != inst.Provider {
			t.Errorf("%s provider is not the instance provider", id)
		}
		if got, want := vs.Engine.MaxTokens(), 777; got != want {
			t.Errorf("%s MaxTokens = %d, want %d", id, got, want)
		}
		if got, want := vs.Engine.TokenBudget(), 5555; got != want {
			t.Errorf("%s TokenBudget = %d, want %d", id, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(projectDir, "meta.json")); err != nil {
		t.Errorf("meta.json not persisted after wechat boot: %v", err)
	}
}

// The port preflight fails fast BEFORE the wui block: its failure leaves no
// "wui: mounted" line behind. The wrap chain must survive for the API user.
func TestPinStart_PreflightFailsBeforeMount(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	defer ln.Close()

	_, err = Start(Options{NoTUI: true})
	if err == nil {
		t.Fatal("Start on occupied port succeeded, want error")
	}
	if !strings.Contains(err.Error(), "ws server") {
		t.Errorf("err = %v, want 'ws server'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("err = %v, want a wrapped chain", err)
	}
	cwd, _ := os.Getwd()
	pd := project.Dir(cwd)
	if data, rerr := os.ReadFile(filepath.Join(pd, "gbot.log")); rerr == nil && strings.Contains(string(data), "wui: mounted") {
		t.Error("gbot.log shows 'wui: mounted': the boot failed AFTER mounting, want the preflight failure")
	}
}

// Supervised mode skips the preflight (SO_REUSEPORT overlap is the point),
// so the failure moves to the late listener AFTER "wui: mounted".
func TestPinStart_SupervisedLateListenFailure(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_SUPERVISED", "1")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	defer ln.Close()

	_, err = Start(Options{NoTUI: true})
	if err == nil {
		t.Fatal("supervised Start on a plainly-held port succeeded, want late-listen error")
	}
	if !strings.Contains(err.Error(), "ws server") {
		t.Errorf("err = %v, want 'ws server'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("err = %v, want a wrapped chain", err)
	}
	cwd, _ := os.Getwd()
	data, rerr := os.ReadFile(filepath.Join(project.Dir(cwd), "gbot.log"))
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if !strings.Contains(string(data), "wui: mounted") {
		t.Error("gbot.log lacks 'wui: mounted': supervised mode must reach the late listener, not fail at the preflight")
	}
}

// GET /api/admin/restart pins the admin surface: a TUI-process boot is NOT
// upgradable (upg nil in-process) and carries NO refusal code; the real PID
// and build identity ride along.
func TestPinStart_AdminSurface(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "")
	startInTempProject(t)
	inst, err := Start(Options{WSPort: "8766"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	addr := "127.0.0.1:8766"
	resp, err := http.Get("http://" + addr + "/api/admin/restart")
	if err != nil {
		t.Fatalf("GET /api/admin/restart: %v", err)
	}
	defer resp.Body.Close()
	var payload struct {
		Upgradable bool   `json:"upgradable"`
		Reason     string `json:"reason"`
		Pid        int    `json:"pid"`
		Build      string `json:"build"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode admin payload: %v", err)
	}
	if payload.Upgradable {
		t.Error("upgradable = true, want false (no tableflip upgrader in-process)")
	}
	if payload.Reason != "" {
		t.Errorf("reason = %q, want empty (refusal is TUI-upgrade-only)", payload.Reason)
	}
	if payload.Pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", payload.Pid, os.Getpid())
	}
	if payload.Build == "" {
		t.Error("build empty, want the version info string")
	}
}

// createDreamEngine pins: tool whitelist, provider/model resolution through
// a second provider, context window/max tokens from that provider's config,
// and the NoTUI handler split.
func TestPinCreateDreamEngine_Attributes(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "t", Type: "openai", URL: "http://127.0.0.1:1", Keys: []string{"k"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"m": {}})},
			{Name: "b", Type: "openai", URL: "http://127.0.0.1:1", Keys: []string{"k"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"dm": {Context: config.IntOrHuman(9999), MaxTokens: config.IntOrHuman(777)}})},
		},
	}
	cfg.Model = config.ModelSpec{"default": "t/m"}
	providerMap := map[string]llm.Provider{"t": &pinNamedProvider{name: "t"}, "b": &pinNamedProvider{name: "b"}}
	tProv := providerMap["t"]
	d := dreamEngineDeps{
		Cfg:                cfg,
		ProviderMap:        providerMap,
		Provider:           tProv,
		PrimaryProviderCfg: &cfg.Providers[0],
		Model:              "m",
		WorkingDir:         t.TempDir(),
		Store:              pinShortStore(t),
		Logger:             slog.Default(),
		NoTUI:              true,
		EngineMgr:          engine.NewEngineManager(),
		IDMinter:           agent.NewIDMinter(),
	}
	d.Cfg.Dream.Model = "b/dm"
	eng, handler, display, ctxWindow := createDreamEngine(d)
	if eng == nil {
		t.Fatal("dream engine nil")
	}
	if eng.Provider() != providerMap["b"] {
		t.Error("dream provider is not the b/dm provider instance")
	}
	if handler != nil {
		t.Errorf("handler = %v, want nil under NoTUI", handler)
	}
	if display != "b/dm" {
		t.Errorf("display model = %q, want b/dm", display)
	}
	if ctxWindow != 9999 {
		t.Errorf("ctxWindow = %d, want 9999", ctxWindow)
	}
	if eng.Model() != "dm" {
		t.Errorf("dream Model = %q, want dm", eng.Model())
	}
	if eng.Provider().Name() != "b" {
		t.Errorf("dream provider = %q, want b", eng.Provider().Name())
	}
	if eng.TokenBudget() != 9999 {
		t.Errorf("dream TokenBudget = %d, want 9999", eng.TokenBudget())
	}
	if eng.MaxTokens() != 777 {
		t.Errorf("dream MaxTokens = %d, want 777", eng.MaxTokens())
	}
	if eng.EngineID() != "dream" {
		t.Errorf("dream EngineID = %q, want dream", eng.EngineID())
	}
	toolMap := eng.Tools()
	if len(toolMap) != 7 {
		t.Errorf("whitelist tool count = %d, want 7 (Read Write Edit Bash Grep Glob Recall): %v", len(toolMap), toolNames(toolMap))
	}
	for _, want := range []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob", "Recall"} {
		if _, ok := toolMap[want]; !ok {
			t.Errorf("whitelist lacks %s: %v", want, toolNames(toolMap))
		}
	}
	// The TUI leg produces a handler.
	d.NoTUI = false
	_, tuiHandler, _, _ := createDreamEngine(d)
	if tuiHandler == nil {
		t.Error("TUI-mode dream handler nil, want the engine hub handler")
	}
}

func toolNames(m map[string]tool.Tool) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func pinShortStore(t *testing.T) *short.Store {
	t.Helper()
	store, err := short.NewStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func pinBareEngine(t *testing.T, id string) *engine.Engine {
	t.Helper()
	return engine.New(&engine.Params{EngineID: id, Provider: &spyProvider{}, ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{} }})
}

// pinNamedProvider is a spyProvider whose Name matches its map key, so
// createDreamEngine's provider-lookup-by-name resolves like the real boot.
type pinNamedProvider struct {
	spyProvider
	name string
}

func (p *pinNamedProvider) Name() string { return p.name }

// pinFailProvider always errors, so query results carry a non-nil Error.
type pinFailProvider struct{ spyProvider }

func (p *pinFailProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, errors.New("pin: provider down")
}

// dream model resolution failure keeps the default provider and warns.
func TestPinCreateDreamEngine_ModelResolveFailure(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "t", Type: "openai", URL: "http://127.0.0.1:1", Keys: []string{"k"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"m": {}})},
		},
	}
	cfg.Model = config.ModelSpec{"default": "t/m"}
	cfg.Dream.Model = "nope/m"
	p := &pinNamedProvider{name: "t"}
	d := dreamEngineDeps{
		Cfg:                cfg,
		ProviderMap:        map[string]llm.Provider{"t": p},
		Provider:           p,
		PrimaryProviderCfg: &cfg.Providers[0],
		Model:              "m",
		WorkingDir:         t.TempDir(),
		Store:              pinShortStore(t),
		Logger:             slog.Default(),
		NoTUI:              true,
		EngineMgr:          engine.NewEngineManager(),
		IDMinter:           agent.NewIDMinter(),
	}
	eng, _, display, _ := createDreamEngine(d)
	if eng.Provider() != p {
		t.Error("provider is not the default t provider after resolve failure")
	}
	if display != "t/m" {
		t.Errorf("display = %q, want t/m", display)
	}
}

// resumeDreamSession: an existing dream session is resumed verbatim; with no
// session a fresh Dream session is created.
func TestPinResumeDreamSession(t *testing.T) {
	projectDir := t.TempDir()
	store := pinShortStore(t)
	ses, err := store.CreateSessionWithEngine(projectDir, "m", "dream")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	eng := pinBareEngine(t, "dream")
	defer eng.Close()
	eng.SetStore(store, projectDir)
	resumeDreamSession(eng, store, projectDir)
	if eng.SessionID() != ses.SessionID {
		t.Errorf("resumed session = %q, want %q", eng.SessionID(), ses.SessionID)
	}

	freshStore := pinShortStore(t)
	fresh := pinBareEngine(t, "dream")
	defer fresh.Close()
	freshDir := t.TempDir()
	fresh.SetStore(freshStore, freshDir)
	resumeDreamSession(fresh, freshStore, freshDir)
	if fresh.SessionID() == "" {
		t.Error("fresh dream session not created when none existed")
	}
}

// The adapter forwards engine state and query errors verbatim.
func TestPinDreamAdapter(t *testing.T) {
	eng := engine.New(&engine.Params{
		EngineID:      "dream",
		Provider:      &pinFailProvider{},
		ToolsProvider: func() map[string]tool.Tool { return map[string]tool.Tool{} },
	})
	defer eng.Close()
	eng.SetStore(pinShortStore(t), t.TempDir())
	if err := eng.NewSession(t.TempDir(), "Dream"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	a := &dreamEngineAdapter{eng: eng}
	if a.IsBusy() {
		t.Error("idle engine reports busy")
	}
	if a.SessionID() != eng.SessionID() {
		t.Errorf("adapter SessionID = %q, engine %q", a.SessionID(), eng.SessionID())
	}
	err := a.Query(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "pin: provider down") {
		t.Errorf("adapter Query err = %v, want the provider failure verbatim", err)
	}
}

// bootSessionStartCtx: an unknown session keeps the ctx bare.
func TestPinBootSessionStartCtx_UnknownSession(t *testing.T) {
	mgr := engine.NewEngineManager()
	ctx := bootSessionStartCtx(context.Background(), mgr, "no-such-session")
	if hooks.JsRunnerFrom(ctx) != nil {
		t.Error("ctx carries a js runner for an unknown session, want bare")
	}
}

// startSessionCleanup logs both the failure and the counted deletion.
func TestPinStartSessionCleanup_Logs(t *testing.T) {
	var buf pinLogBuffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	projectDir := t.TempDir()
	store := pinShortStore(t)
	ses, err := store.CreateSessionWithEngine(projectDir, "m", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	msg := &short.TranscriptMessage{UUID: "u1", Type: "user", Content: `{"type":"text","text":"old"}`}
	if err := store.AppendMessage(ses.SessionID, msg); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE sessions SET updated_at = datetime('now', '-40 days') WHERE session_id = ?", ses.SessionID); err != nil {
		t.Fatal(err)
	}
	startSessionCleanup(store, 10*time.Millisecond, time.Hour)
	deadline := time.Now().Add(3 * time.Second) // REAL-TIME: bounded wait for the real firstDelay
	for !strings.Contains(buf.String(), "cleanup:old_sessions") {
		if time.Now().After(deadline) { // REAL-TIME: bounded wait for the real firstDelay
			t.Fatalf("cleanup pass never logged a deletion; log so far:\n%s", buf.String())
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: startSessionCleanup runs on a real timer; no channel to select on
	}
	if !strings.Contains(buf.String(), "count=1") {
		t.Errorf("cleanup count mismatch, want count=1 for one old session:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "cleanup:old_sessions_failed") {
		t.Errorf("cleanup reported a failure against a healthy store:\n%s", buf.String())
	}

	// A closed store surfaces the failure warn, not a panic.
	closed := pinShortStore(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	startSessionCleanup(closed, 10*time.Millisecond, time.Hour)
	failDeadline := time.Now().Add(3 * time.Second) // REAL-TIME: bounded wait for the first pass
	for !strings.Contains(buf.String(), "cleanup:old_sessions_failed") {
		if time.Now().After(failDeadline) { // REAL-TIME: bounded wait for the first pass
			t.Fatalf("closed-store cleanup never warned; log:\n%s", buf.String())
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: startSessionCleanup runs on a real timer; no channel to select on
	}
}

type pinLogBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *pinLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *pinLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *pinLogBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// Every wui route registration must actually mount: a dropped Register* call
// turns into a 404 on an otherwise-200/non-404 surface.
func TestPinStart_WuiRoutesMounted(t *testing.T) {
	if _, err := pinBoot(t, pinSettings, Options{NoTUI: true}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	base := "http://" + os.Getenv("GBOT_WS_ADDR")
	// path → method (probe expects != 404; the handlers' own error shapes vary)
	probes := []struct{ method, path string }{
		{"GET", "/api/settings/providers"},     // RegisterSettingsRoutes
		{"PUT", "/api/avatar/agent"},           // RegisterAvatarRoutes (GET of unknown who 404s by design)
		{"GET", "/api/settings/remotedesktop"}, // RegisterRemoteDesktopRoutes
		{"GET", "/wui/vnc/pinvnc"},             // RegisterVNCProxyRoutes (device registered below)
		{"GET", "/ws/stt"},                     // RegisterSTTRoutes (upgrade failure != 404)
		{"POST", "/api/tts"},                   // RegisterTTSRoutes
		{"GET", "/api/logs"},                   // RegisterLogRoutes
		{"POST", "/api/admin/reload"},          // RegisterReloadRoute
		{"GET", "/api/artifacts"},              // RegisterArtifactRoutes
	}
	// A configured remote device makes the VNC proxy path distinguishable
	// from a missing route (unknown devices 404 inside the handler).
	devReq, err := http.NewRequest(http.MethodPut, base+"/api/settings/remotedesktop",
		strings.NewReader(`[{"name":"pinvnc","addr":"ws://127.0.0.1:1"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(devReq); err != nil {
		t.Fatalf("PUT remotedesktop: %v", err)
	} else {
		_ = resp.Body.Close()
	}
	for _, p := range probes {
		req, err := http.NewRequest(p.method, base+p.path, nil)
		if err != nil {
			t.Fatalf("build %s %s: %v", p.method, p.path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("%s %s: %v", p.method, p.path, err)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("%s %s: 404 — route not mounted", p.method, p.path)
		}
	}
}

// The wui's own STT/TTS defaults and the admin surface ride on the Instance
// boot: WSPort passthrough + no refusal in a non-TUI-upgrade process.
func TestPinStart_WSPortAndAdminRefusal(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "")
	startInTempProject(t)
	inst, err := Start(Options{WSPort: "8791"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	if inst.WSPort != "8791" {
		t.Errorf("Instance.WSPort = %q, want 8791", inst.WSPort)
	}
	resp, err := http.Get("http://127.0.0.1:8791/api/admin/restart")
	if err != nil {
		t.Fatalf("GET /api/admin/restart: %v", err)
	}
	defer resp.Body.Close()
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Reason != "" {
		t.Errorf("reason = %q, want empty (no tableflip parent in-process)", payload.Reason)
	}
}

// A no-hooks boot must NOT log a parse failure (len guards), and a 1-byte
// hooks value must reach the parser and warn.
func TestPinStart_HooksLenGuards(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if log := pinLog(t, inst); strings.Contains(log, "failed to parse hooks config") {
		t.Errorf("no-hooks boot logged a parse failure:\n%s", log)
	}

	inst2, err := pinBoot(t, `{"hooks":0,"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start(hooks:0): %v", err)
	}
	if log := pinLog(t, inst2); !strings.Contains(log, "failed to parse hooks config") {
		t.Errorf("gbot.log lacks the hooks parse warn for the 1-byte value, got:\n%s", log)
	}
}

// Route probes distinguish the SPA catch-all (text/html) from the mounted
// JSON surfaces.
func TestPinStart_WuiRoutesServeJSON(t *testing.T) {
	if _, err := pinBoot(t, pinSettings, Options{NoTUI: true}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	base := "http://" + os.Getenv("GBOT_WS_ADDR")
	devReq, err := http.NewRequest(http.MethodPut, base+"/api/settings/remotedesktop",
		strings.NewReader(`[{"name":"pinvnc","addr":"ws://127.0.0.1:1"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(devReq); err != nil {
		t.Fatalf("PUT remotedesktop: %v", err)
	} else {
		_ = resp.Body.Close()
	}
	probes := []struct{ method, path, route string }{
		{"GET", "/api/plugins", "RegisterPluginRoutes"},
		{"GET", "/api/artifacts", "RegisterArtifactRoutes"},
		{"GET", "/api/settings/providers", "RegisterSettingsRoutes"},
		{"PUT", "/api/avatar/agent", "RegisterAvatarRoutes"},
		{"GET", "/api/logs", "RegisterLogRoutes"},
		{"POST", "/api/tts", "RegisterTTSRoutes"},
	}
	for _, p := range probes {
		req, err := http.NewRequest(p.method, base+p.path, nil)
		if err != nil {
			t.Fatalf("build %s %s: %v", p.method, p.path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("%s %s: %v", p.method, p.path, err)
			continue
		}
		_ = resp.Body.Close()
		// /api/logs serves the tailed gbot.log as text/plain — the SPA
		// catch-all would answer text/html (its JS bundle contains "level="
		// strings, so only the content type is a reliable discriminator).
		if p.path == "/api/logs" {
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
				t.Errorf("%s: Content-Type = %q, want text/plain (tailed gbot.log)", p.path, ct)
			}
			continue
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("%s %s: Content-Type = %q status=%d, want the %s JSON surface", p.method, p.path, ct, resp.StatusCode, p.route)
		}
	}
	// The VNC proxy upgrades registered devices: a plain GET must fail with
	// an upgrade error, not the SPA index.
	vnc, err := http.Get(base + "/wui/vnc/pinvnc")
	if err != nil {
		t.Fatalf("GET /wui/vnc/pinvnc: %v", err)
	}
	_ = vnc.Body.Close()
	if vnc.StatusCode != http.StatusBadRequest || strings.Contains(vnc.Header.Get("Content-Type"), "text/html") {
		t.Errorf("VNC probe: status=%d ct=%q, want 400 non-HTML (registered device upgrade)", vnc.StatusCode, vnc.Header.Get("Content-Type"))
	}
}

// The chat metadata frame must advertise tts:true when a TTS default is
// wired (mirrors the stt:true contract).
func TestPinStart_TTSSurfaceInMetadata(t *testing.T) {
	t.Setenv("HOME", pinHome(t, `{"providers":[
		{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}},
		{"name":"audiocpp","type":"audio","url":"http://127.0.0.1:1","models":{"tts-x":{}}}],
		"model":{"default":"t/m"},"tts":{"default":"audiocpp/tts-x"}}`))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	waitForHTTP(t, "http://127.0.0.1:"+port, 5*time.Second)

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+port+"/ws/chat", nil)
	if err != nil {
		t.Fatalf("dial /ws/chat: %v", err)
	}
	defer ws.Close()
	deadline := time.Now().Add(10 * time.Second) // REAL-TIME
	for {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read metadata: %v", err)
		}
		if !strings.Contains(string(data), `"connect"`) {
			continue
		}
		if !strings.Contains(string(data), `"tts":true`) {
			t.Fatalf("metadata config lacks tts:true: %s", data)
		}
		return
	}
}

// engine_new must be wired: the wui creates engines through the boot's
// factory closure (createEngine nil surfaces as a configuration error).
func TestPinStart_EngineNewWired(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := inst.EngineMgr.Count()
	base := "http://" + os.Getenv("GBOT_WS_ADDR")
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+base[len("http://"):]+"/ws/chat", nil)
	if err != nil {
		t.Fatalf("dial /ws/chat: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteMessage(1, []byte(`{"type":"engine_new","name":"pinnew"}`)); err != nil {
		t.Fatalf("send engine_new: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second) // REAL-TIME
	for {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read after engine_new: %v", err)
		}
		s := string(data)
		if strings.Contains(s, "engine creation not configured") {
			t.Fatalf("engine_new rejected: %s", s)
		}
		if strings.Contains(s, `"pinnew"`) {
			if got := inst.EngineMgr.Count(); got != before+1 {
				t.Errorf("engine count after engine_new = %d, want %d", got, before+1)
			}
			return
		}
	}
}

// The agent definition loader must be initialized with the boot working dir.
func TestPinStart_AgentLoaderInitialized(t *testing.T) {
	if _, err := pinBoot(t, pinSettings, Options{NoTUI: true}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if agent.GlobalLoader() == nil {
		t.Fatal("agent.GlobalLoader() nil after boot, want InitLoader(workingDir)")
	}
}

// GBOT_PPROF_ADDR pins a live pprof listener when requested.
func TestPinStart_PprofAddrBinds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pprofPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "127.0.0.1:"+fmt.Sprint(pprofPort))
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: pprof binds async
	for {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", pprofPort))
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) { // REAL-TIME: pprof binds async
			t.Fatalf("pprof listener never came up on %d: %v", pprofPort, err)
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: polls a real listener over HTTP, not a channel
	}
}

// The signal goroutine must run the ordered exit on SIGINT through the
// osExit seam. Earlier boots in this process hold their own signal
// registrations, so one signal fans out to every live goroutine: the seam
// parks, so no goroutine slips past it onto a real os.Exit.
func TestPinStart_SIGINTRunsExitActions(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)

	exited := make(chan struct{})
	var once sync.Once
	onExit(t, func(code int) {
		if code != 0 {
			t.Errorf("signal exit code = %d, want 0", code)
		}
		once.Do(func() { close(exited) })
		select {} // park: the fan-out goroutines must never reach a real exit
	})
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("kill -INT: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second): // REAL-TIME
		t.Fatal("SIGINT did not reach the osExit seam within 5s — the signal goroutine never wired")
	}
}

// engine_new inherits the ACTIVE engine's provider/model, not the boot
// default: a restored engine on a second provider must propagate to engines
// the wui creates later.
func TestPinStart_EngineNewInheritsActiveModel(t *testing.T) {
	t.Setenv("HOME", pinHome(t, `{"providers":[
		{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}},
		{"name":"b","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"bm":{}}}],
		"model":{"default":"t/m"}}`))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	projectDir := project.Dir(proj)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(projectDir, "meta.json")), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"engines":[{"id":"main","name":"main","model":"b/bm","activeSessionID":""}],"activeEngineID":"main"}`
	if err := os.WriteFile(filepath.Join(projectDir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	if vs := inst.EngineMgr.Active(); vs == nil || vs.Engine == nil || vs.Engine.Model() != "bm" {
		t.Fatalf("active engine = %+v, want the restored b/bm engine", vs)
	}

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+port+"/ws/chat", nil)
	if err != nil {
		t.Fatalf("dial /ws/chat: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteMessage(1, []byte(`{"type":"engine_new","name":"pinclone"}`)); err != nil {
		t.Fatalf("send engine_new: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second) // REAL-TIME
	for {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read after engine_new: %v", err)
		}
		s := string(data)
		if !strings.Contains(s, `"pinclone"`) {
			continue
		}
		if !strings.Contains(s, `"model":"b/bm"`) {
			t.Fatalf("created engine did not inherit the active model b/bm: %s", s)
		}
		return
	}
}

// A blocked project dir (file where ~/.gbot/projects/<slug> belongs) fails
// Start through acquirePID's mkdir error.
func TestPinStart_BlockedProjectDirFails(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+freeTCPPort(t))
	startInTempProject(t)
	cwd, _ := os.Getwd()
	blocked := project.Dir(cwd)
	if err := os.MkdirAll(filepath.Dir(blocked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Start(Options{NoTUI: true})
	if err == nil {
		t.Fatal("Start with a file at the project dir succeeded, want error")
	}
	if !strings.Contains(err.Error(), "create project dir") {
		t.Errorf("err = %v, want 'create project dir'", err)
	}
}

// An audio provider as the default model is excluded from the provider map
// (STT-only): the degrade ladder treats it like any dead default provider
// and boots on the first live chat sibling instead of failing.
func TestPinStart_AudioDefaultModelFallsBackToSibling(t *testing.T) {
	inst, err := pinBoot(t, `{"providers":[
		{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}},
		{"name":"audiocpp","type":"audio","url":"http://127.0.0.1:1","models":{"stt-x":{}}}],
		"model":{"default":"audiocpp/stt-x"}}`, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start with an audio default model: %v, want sibling fallback", err)
	}
	if inst.Provider == nil || inst.Provider.Name() != "t" {
		t.Fatalf("Provider = %v, want t", inst.Provider)
	}
	if inst.Model != "m" {
		t.Fatalf("Model = %q, want m", inst.Model)
	}
}

// A fresh boot (no meta.json) synthesizes the main engine with the DEFAULT
// model from the boot's model resolution.
func TestPinStart_FreshMainEngineUsesDefaultModel(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	vs := inst.EngineMgr.Get("main")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("main engine missing: %v", engineIDs(inst.EngineMgr))
	}
	if vs.Engine.Model() != "m" {
		t.Errorf("fresh main engine Model = %q, want m (boot default)", vs.Engine.Model())
	}
	if vs.Engine.Provider() != inst.Provider {
		t.Error("fresh main engine provider is not the boot provider")
	}
}

// resumeDreamSession's fresh-session failure path warns (engine already has
// an active session → NewSession errors).
func TestPinResumeDreamSession_NewSessionFailureWarns(t *testing.T) {
	var buf pinLogBuffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	eng := pinBareEngine(t, "dream")
	defer eng.Close()
	store := pinShortStore(t)
	dir := t.TempDir()
	eng.SetStore(store, dir)
	// A closed store makes both the listing and the fresh NewSession fail,
	// surfacing the create-session warn.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	resumeDreamSession(eng, store, dir)
	if !strings.Contains(buf.String(), "dream: create session failed") {
		t.Errorf("log lacks the create-session warn; session=%q log:\n%s", eng.SessionID(), buf.String())
	}
}

// The adapter's IsBusy must forward the ENGINE state (blocking provider →
// busy mid-query).
func TestPinDreamAdapter_IsBusyForwardsEngine(t *testing.T) {
	block := make(chan struct{})
	eng := engine.New(&engine.Params{
		EngineID: "dream",
		Provider: &pinBlockProvider{release: block},
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{}
		},
	})
	defer eng.Close()
	eng.SetStore(pinShortStore(t), t.TempDir())
	if err := eng.NewSession(t.TempDir(), "Dream"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	a := &dreamEngineAdapter{eng: eng}
	done := make(chan error, 1)
	go func() { done <- a.Query(context.Background(), "hi") }()
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: wait for the query to be in flight
	for !a.IsBusy() {
		if time.Now().After(deadline) { // REAL-TIME: bounded wait for the query to be in flight
			close(block)
			t.Fatal("engine never reported busy with a query in flight")
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: IsBusy flips inside the engine goroutine; no channel exposes it
	}
	close(block)
	if err := <-done; err == nil {
		t.Log("query completed without error")
	}
}

// pinBlockProvider holds every Stream until release closes.
type pinBlockProvider struct {
	spyProvider
	release chan struct{}
}

func (p *pinBlockProvider) Stream(ctx context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	<-p.release
	return p.spyProvider.Stream(ctx, &llm.Request{})
}

// needWS pinning across the three operand combinations that NoTUI boots
// short-circuit: TUI process + default port + env set mounts on the env
// address; TUI process + default port + no env mounts nothing; NoTUI with
// the default port still mounts.
func TestPinStart_NeedWSOperandMatrix(t *testing.T) {
	// (a) NoTUI=true, WSPort default, env unset → mounts (kills the
	// NoTUI-short-circuit removal).
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "")
	startInTempProject(t)
	inst, err := Start(Options{NoTUI: true, WSPort: "8765"})
	if err != nil {
		t.Fatalf("Start(NoTUI, 8765): %v", err)
	}
	shutdownInstance(t, inst)
	inst.PIDCleanup()
	if len(inst.MediaStores) == 0 {
		t.Error("MediaStores empty for NoTUI boot with default port, want the wui upload cache")
	}

	// (b) TUI process, default port, env unset → nothing mounts.
	t.Setenv("HOME", pinHome(t, pinSettings))
	inst2, err := Start(Options{WSPort: "8765"})
	if err != nil {
		t.Fatalf("Start(TUI, 8765, no env): %v", err)
	}
	shutdownInstance(t, inst2)
	inst2.PIDCleanup()
	if len(inst2.MediaStores) != 0 {
		t.Error("MediaStores non-empty for a TUI boot on the default port without env, want no wui")
	}

	// (c) TUI process, default port, env SET → mounts on the env address.
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	inst3, err := Start(Options{WSPort: "8765"})
	if err != nil {
		t.Fatalf("Start(TUI, 8765, env): %v", err)
	}
	shutdownInstance(t, inst3)
	if len(inst3.MediaStores) == 0 {
		t.Error("MediaStores empty for a TUI boot with GBOT_WS_ADDR set, want the wui mounted on the env address")
	}
	waitForHTTP(t, "http://127.0.0.1:"+port, 5*time.Second)
}

// The session-cleanup goroutine's Start-level schedule: the first pass comes
// at 5s (a session that old must survive 1s and be gone by 7s).
func TestPinStart_SessionCleanupSchedule(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	projectDir := project.Dir(proj)
	if err := os.MkdirAll(filepath.Join(projectDir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed, err := short.NewStore(filepath.Join(projectDir, "memory", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	ses, err := seed.CreateSessionWithEngine(projectDir, "m", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.AppendMessage(ses.SessionID, &short.TranscriptMessage{UUID: "u-old", Type: "user", Content: `{"type":"text","text":"old"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.DB().Exec("UPDATE sessions SET updated_at = datetime('now', '-40 days') WHERE session_id = ?", ses.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)

	time.Sleep(1 * time.Second) // REAL-TIME: before the 5s first pass
	probe, err := short.NewStore(filepath.Join(projectDir, "memory", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if _, err := probe.GetSession(ses.SessionID); err != nil {
		t.Fatalf("old session deleted before the 5s first pass (firstDelay too eager): %v", err)
	}
	deadline := time.Now().Add(8 * time.Second) // REAL-TIME: bounded wait past the 5s pass
	for {
		if _, err := probe.GetSession(ses.SessionID); err != nil {
			return // deleted by the scheduled pass
		}
		if time.Now().After(deadline) { // REAL-TIME: bounded wait for the scheduled cleanup pass
			t.Fatal("old session survived 9s of boot — the cleanup pass never ran")
		}
		time.Sleep(200 * time.Millisecond) // REAL-TIME: the pass is driven by a real timer inside boot
	}
}

// The factory resolves a named second provider (branch/else swap must not
// fall back to the default), and a model without a provider prefix warns.
func TestPinStart_EngineFactorySecondProviderAndPrefixWarn(t *testing.T) {
	settings := `{"providers":[
		{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}},
		{"name":"b","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"bm":{}}}],
		"model":{"default":"t/m"}}`
	inst, err := pinBoot(t, settings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng, _, ferr := inst.EngineFactory("pinengb", "PinB", "b", "bm")
	if ferr != nil {
		t.Fatalf("EngineFactory(b/bm): %v", ferr)
	}
	defer eng.Close()
	if eng.Provider().Name() != "b" {
		t.Errorf("engine provider = %q, want b (found provider must win)", eng.Provider().Name())
	}
	if eng.Model() != "bm" {
		t.Errorf("engine Model = %q, want bm", eng.Model())
	}
	noPrefix, _, ferr := inst.EngineFactory("pinengc", "PinC", "", "m")
	if ferr != nil {
		t.Fatalf("EngineFactory(no prefix): %v", ferr)
	}
	defer noPrefix.Close()
	if noPrefix.Provider().Name() != "t" {
		t.Errorf("no-prefix engine provider = %q, want t (default)", noPrefix.Provider().Name())
	}
	if log := pinLog(t, inst); !strings.Contains(log, "no provider prefix in model") {
		t.Errorf("gbot.log lacks the no-prefix warn, got:\n%s", log)
	}
}

// bootSessionStartCtx pins the REPL of the session-owning engine.
func TestPinBootSessionStartCtx_OwningEngineGetsRunner(t *testing.T) {
	mgr := engine.NewEngineManager()
	eng := pinBareEngine(t, "main")
	defer eng.Close()
	refs := engine.CreateTools(engine.SharedDeps{WorkingDir: t.TempDir()}, task.NewList(""))
	eng.SetToolRefs(refs)
	eng.SetStore(pinShortStore(t), t.TempDir())
	if err := eng.NewSession(t.TempDir(), "Main"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	mgr.Add(&engine.EngineViewState{Engine: eng, ID: "main", ActiveSessionID: eng.SessionID()})
	ctx := bootSessionStartCtx(context.Background(), mgr, eng.SessionID())
	if hooks.JsRunnerFrom(ctx) == nil {
		t.Error("owning engine's session ctx carries no js runner, want the engine REPL")
	}
}

// The dream view state in a NoTUI boot carries no TUI handler.
func TestPinStart_DreamNoTUIHandlerNil(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	vs := inst.EngineMgr.Get("dream")
	if vs == nil {
		t.Fatal("dream engine missing")
	}
	// The field is `any`: a NoTUI boot stores a TYPED-NIL handler; the
	// pointer itself must be nil (a TUI boot would store a live handler).
	if h, ok := vs.Handler.(*tui.TUIHandler); !ok || h != nil {
		t.Errorf("dream Handler = %#v, want a nil *tui.TUIHandler under NoTUI", vs.Handler)
	}
}

// A plugin dir that is a FILE fails discovery (warn); a good plugin's
// SessionStart hook merges into the boot dispatch (file written at boot).
func TestPinStart_PluginLoadFailureAndHookMerge(t *testing.T) {
	t.Setenv("HOME", pinHome(t, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0o755); err != nil {
		t.Fatal(err)
	}
	// ~/.gbot/plugins as a file → ReadDir fails → LoadAndInitialize errors.
	if err := os.WriteFile(filepath.Join(home, ".gbot", "plugins"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	startInTempProject(t)
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	if log := pinLog(t, inst); !strings.Contains(log, "plugin loading failed") {
		t.Errorf("gbot.log lacks the plugin failure warn, got:\n%s", log)
	}
}

func TestPinStart_PluginHooksMergeIntoBootDispatch(t *testing.T) {
	t.Setenv("HOME", pinHome(t, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	home, _ := os.UserHomeDir()
	hookOut := filepath.Join(home, "pin-hook-ran")
	plugRoot := filepath.Join(home, ".gbot", "plugins", "pinplug")
	if err := os.MkdirAll(filepath.Join(plugRoot, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"pinplug","version":"1.0"}`
	if err := os.WriteFile(filepath.Join(plugRoot, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	hooksJSON := fmt.Sprintf(`{"description":"pin","hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"echo ran > %s"}]}]}}`, hookOut)
	if err := os.WriteFile(filepath.Join(plugRoot, "hooks", "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	startInTempProject(t)
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	if _, err := os.Stat(hookOut); err != nil {
		t.Errorf("plugin SessionStart hook never ran (no marker file): %v", err)
	}
}

// A plugin's repls dir must land in repl's process-wide harness list under the
// "plugin:file" name, otherwise plugin harness JS silently never loads.
func TestPinStart_PluginReplScriptsWired(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	home, _ := os.UserHomeDir()
	plugRoot := filepath.Join(home, ".gbot", "plugins", "pinrepl")
	if err := os.MkdirAll(filepath.Join(plugRoot, "repl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugRoot, "plugin.json"), []byte(`{"name":"pinrepl","version":"1.0","repls":"./repl/"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const src = "globalThis.pinHarness = 1"
	if err := os.WriteFile(filepath.Join(plugRoot, "repl", "harness.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := repl.ReplScripts()
	t.Cleanup(func() { repl.SetReplScripts(prev) })
	repl.SetReplScripts(nil)

	startInTempProject(t)
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)

	scripts := repl.ReplScripts()
	if len(scripts) != 1 {
		t.Fatalf("repl scripts = %d (%v), want 1 from the plugin", len(scripts), scripts)
	}
	if scripts[0].Name != "pinrepl:harness.js" {
		t.Errorf("script name = %q, want pinrepl:harness.js", scripts[0].Name)
	}
	if scripts[0].Source != src {
		t.Errorf("script source = %q, want the plugin file verbatim", scripts[0].Source)
	}
}

// bootSessionStartCtx with an owning engine that has NO REPL (lazy view
// state) still returns the bare ctx — never nil.
func TestPinBootSessionStartCtx_NilREPLOutReturnsCtx(t *testing.T) {
	mgr := engine.NewEngineManager()
	eng := pinBareEngine(t, "lazy")
	defer eng.Close()
	eng.SetStore(pinShortStore(t), t.TempDir())
	if err := eng.NewSession(t.TempDir(), "Lazy"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	mgr.Add(&engine.EngineViewState{Engine: eng, ID: "lazy", ActiveSessionID: eng.SessionID()})
	ctx := bootSessionStartCtx(context.Background(), mgr, eng.SessionID())
	if ctx == nil {
		t.Fatal("bootSessionStartCtx returned nil for a nil-REPL engine, want the bare ctx")
	}
	if hooks.JsRunnerFrom(ctx) != nil {
		t.Error("ctx carries a js runner for a nil-REPL engine, want bare")
	}
}

// frameEngineIDs extracts engine ids from either an engine_list frame or a
// metadata frame's embedded engines object. Empty means "no list here".
func frameEngineIDs(data []byte) []string {
	type listShape struct {
		Engines []struct {
			ID string `json:"id"`
		} `json:"engines"`
	}
	var outer struct {
		Engines json.RawMessage `json:"engines"`
	}
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil
	}
	for _, raw := range []json.RawMessage{data, outer.Engines} {
		var l listShape
		if err := json.Unmarshal(raw, &l); err == nil && len(l.Engines) > 0 {
			ids := make([]string, 0, len(l.Engines))
			for _, e := range l.Engines {
				ids = append(ids, e.ID)
			}
			return ids
		}
	}
	return nil
}

// engine_delete's session cleanup must be wired through the boot store: the
// end-to-end chain (engine_new → engine_delete) leaves no session rows for
// the deleted engine in memory/memory.db.
func TestPinStart_EngineDeleteRemovesSessions(t *testing.T) {
	inst, err := pinBoot(t, pinSettings, Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	base := "http://" + os.Getenv("GBOT_WS_ADDR")
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+base[len("http://"):]+"/ws/chat", nil)
	if err != nil {
		t.Fatalf("dial /ws/chat: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteMessage(1, []byte(`{"type":"engine_new","name":"pindel"}`)); err != nil {
		t.Fatalf("send engine_new: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second) // REAL-TIME
	newID := ""
	for newID == "" {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read after engine_new: %v", err)
		}
		var list struct {
			Engines []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"engines"`
		}
		if err := json.Unmarshal(data, &list); err != nil || len(list.Engines) == 0 {
			continue
		}
		for _, e := range list.Engines {
			if e.Name == "pindel" {
				newID = e.ID
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projDir := project.Dir(cwd)
	store, err := short.NewStore(filepath.Join(projDir, "memory", "memory.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if seeds, err := store.ListSessionsByEngine(projDir, newID, 10); err != nil {
		t.Fatalf("list sessions before delete: %v", err)
	} else if len(seeds) == 0 {
		t.Fatal("engine_new seeded no session for the new engine")
	}
	if err := ws.WriteMessage(1, []byte(`{"type":"engine_delete","engineID":"`+newID+`"}`)); err != nil {
		t.Fatalf("send engine_delete: %v", err)
	}
	for {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read after engine_delete: %v", err)
		}
		if strings.Contains(string(data), `"scope":"engine_delete"`) {
			t.Fatalf("engine_delete rejected: %s", data)
		}
		// The list rides either a top-level engine_list or the metadata
		// frame's embedded engines object (active-engine delete).
		ids := frameEngineIDs(data)
		if len(ids) == 0 {
			continue
		}
		if slices.Contains(ids, newID) {
			continue
		}
		break
	}
	left, err := store.ListSessionsByEngine(projDir, newID, 10)
	if err != nil {
		t.Fatalf("list sessions after delete: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("deleted engine session rows = %d, want 0", len(left))
	}
}
