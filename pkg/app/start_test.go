package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/config"
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
	mainMsgs, err := short.EngineMessagesToStore([]types.Message{{
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.NewTextBlock("hello from main")},
		Timestamp: time.Now(),
	}})
	if err != nil {
		t.Fatalf("convert main message: %v", err)
	}
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
