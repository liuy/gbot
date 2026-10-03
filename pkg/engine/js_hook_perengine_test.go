package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/repl"
	"github.com/liuy/gbot/pkg/tool/task"
)

// ---------------------------------------------------------------------------
// Per-engine js hook sessions — dispatching engine's REPL is the runner
//
// Engine-side dispatch sites attach their own REPL via hooks.WithJsRunner,
// so each engine's js hooks evaluate in an isolated hook session whose
// tools.* resolve against that engine's executor (WireEngine wiring).
// ---------------------------------------------------------------------------

// echoTool builds a tool named Echo returning a fixed string, so two engines
// can register same-named tools with distinguishable outputs.
func echoTool(output string) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_: "Echo",
		InputSchema_: func() json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{}}`)
		},
		Description_: func(json.RawMessage) (string, error) { return "echo the engine tag", nil },
		Call_: func(context.Context, json.RawMessage, *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: output}, nil
		},
	})
}

// newPerEngineHarness builds one engine through the production path
// (CreateTools + WireEngine + SetToolRefs) sharing the given hooks system,
// and registers an Echo tool returning tag. Returns the engine and its own
// REPL instance. Optional responses replace the default single text reply.
func newPerEngineHarness(t *testing.T, hookSystem *hooks.Hooks, tag string, responses ...mockResponse) (*Engine, *repl.REPLTool) {
	t.Helper()
	tl := task.NewList(t.TempDir())
	deps := SharedDeps{WorkingDir: t.TempDir(), Hooks: hookSystem}
	refs := CreateTools(deps, tl)
	refs.Reg.MustRegister(echoTool(tag))
	t.Cleanup(func() { refs.REPL.Close() })

	mp := &mockProvider{}
	if responses == nil {
		responses = []mockResponse{{events: textStreamEvents("test-model", "done")}}
	}
	for _, r := range responses {
		mp.addResponse(r.events, r.err)
	}
	eng := New(&Params{
		Provider:      mp,
		ToolsProvider: refs.Reg.ToolMapFn(),
		Model:         "test-model",
		Logger:        slog.Default(),
		Hooks:         hookSystem,
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetSharedDeps(&deps)
	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)
	return eng, refs.REPL
}

func runQuery(t *testing.T, eng *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result := eng.QuerySync(ctx, "Do something", ""); result.Error != nil {
		t.Fatalf("QuerySync: %v", result.Error)
	}
}

// readHookState evaluates probe in the engine's own REPL hook session.
func readHookState(t *testing.T, replTool *repl.REPLTool, probe string) string {
	t.Helper()
	out, err := replTool.RunHook(context.Background(), probe, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("readback RunHook(%s): %v", probe, err)
	}
	return out
}

// TestIntegration_PerEngine_StopHookBindsToDispatchingEngine drives two
// engines over one shared hooks system and asserts each Stop hook's tools.*
// resolve against the dispatching engine: A's hook reads "engineA", B's
// reads "engineB", and neither session sees the other's state.
func TestIntegration_PerEngine_StopHookBindsToDispatchingEngine(t *testing.T) {
	t.Parallel()

	code := `async (input) => { globalThis.__echo = await tools.Echo({}); return "ok" }`
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: code, Timeout: 5}}},
		},
	}, &integrationHookRecorder{})

	engA, replA := newPerEngineHarness(t, hookSystem, "engineA")
	engB, replB := newPerEngineHarness(t, hookSystem, "engineB")

	runQuery(t, engA)
	if got := readHookState(t, replA, `() => globalThis.__echo`); got != `"engineA"` {
		t.Errorf("engine A hook echo = %s, want \"engineA\" (A's Stop must run in A's REPL against A's executor)", got)
	}
	if got := readHookState(t, replB, `() => typeof globalThis.__echo`); got != `"undefined"` {
		t.Errorf("engine B session saw A's state: typeof __echo = %s, want \"undefined\"", got)
	}

	runQuery(t, engB)
	if got := readHookState(t, replB, `() => globalThis.__echo`); got != `"engineB"` {
		t.Errorf("engine B hook echo = %s, want \"engineB\" (B's Stop must run in B's REPL against B's executor)", got)
	}
	if got := readHookState(t, replA, `() => globalThis.__echo`); got != `"engineA"` {
		t.Errorf("engine A hook echo after B = %s, want \"engineA\" (B's dispatch must not touch A's session)", got)
	}
}

// TestIntegration_PerEngine_HookSessionStateIsolated asserts the hook
// session's globalThis is one per REPLTool instance (per engine): a counter
// incremented by each engine's Stop hook ends at 1 in both sessions — a
// shared session would leave one of them at 2.
func TestIntegration_PerEngine_HookSessionStateIsolated(t *testing.T) {
	t.Parallel()

	code := `async (input) => { globalThis.__n = (globalThis.__n || 0) + 1; return globalThis.__n }`
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: code, Timeout: 5}}},
		},
	}, &integrationHookRecorder{})

	engA, replA := newPerEngineHarness(t, hookSystem, "engineA")
	engB, replB := newPerEngineHarness(t, hookSystem, "engineB")

	runQuery(t, engA)
	runQuery(t, engB)

	for name, replTool := range map[string]*repl.REPLTool{"A": replA, "B": replB} {
		if got := readHookState(t, replTool, `() => globalThis.__n`); got != "1" {
			t.Errorf("engine %s hook-session __n = %s, want 1 (per-engine sessions; 2 would mean one shared session)", name, got)
		}
	}
}

// TestIntegration_PerEngine_ReentrantToolCallSkipsInnerJsHook is the
// per-engine deadlock defense: a js hook holds its session mutex for the
// whole run, so a tools.* call that fans out a PreToolUse dispatch on the
// same engine must skip the inner js hook (hook-origin ctx guard) instead of
// re-entering the same REPL session. Fanout mirrors the real fan-out shape
// (a tool whose execution dispatches tool hooks on the ctx it received —
// what the Agent tool does through a sub-engine's executor).
func TestIntegration_PerEngine_ReentrantToolCallSkipsInnerJsHook(t *testing.T) {
	t.Parallel()

	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{
				Type:    hooks.HookTypeJS,
				Code:    `async (input) => { globalThis.__outer = await tools.Fanout({}); return "outer-done" }`,
				Timeout: 3,
			}}},
		},
		"PreToolUse": []hooks.HookMatcher{
			{Matcher: "Fanout", Hooks: []hooks.HookConfig{{
				Type: hooks.HookTypeJS,
				Code: `async (input) => { globalThis.__inner = "ran"; return "ok" }`,
			}}},
		},
	}, &integrationHookRecorder{})

	tl := task.NewList(t.TempDir())
	deps := SharedDeps{WorkingDir: t.TempDir(), Hooks: hookSystem}
	refs := CreateTools(deps, tl)
	t.Cleanup(func() { refs.REPL.Close() })
	refs.Reg.MustRegister(tool.BuildTool(tool.ToolDef{
		Name_: "Fanout",
		InputSchema_: func() json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{}}`)
		},
		Description_: func(json.RawMessage) (string, error) { return "dispatch PreToolUse like a fan-out tool", nil },
		Call_: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			if _, results := hookSystem.PreToolUse(ctx, &hooks.HookInput{
				HookEventName: string(hooks.HookPreToolUse),
				ToolName:      "Fanout",
			}); len(results) != 0 {
				return nil, fmt.Errorf("inner js hook re-entered dispatch: %d results", len(results))
			}
			return &tool.ToolResult{Data: "fanout-ok"}, nil
		},
	}))

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "done"), nil)
	eng := New(&Params{
		Provider:      mp,
		ToolsProvider: refs.Reg.ToolMapFn(),
		Model:         "test-model",
		Logger:        slog.Default(),
		Hooks:         hookSystem,
	})
	t.Cleanup(func() { eng.Close() })
	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	runQuery(t, eng)

	got := readHookState(t, refs.REPL, `() => [globalThis.__outer, globalThis.__inner]`)
	if want := `["fanout-ok",null]`; got != want {
		t.Errorf("hook session state = %s, want %s (outer must complete; inner js hook must be skipped)", got, want)
	}
}

// TestIntegration_PerEngine_SubagentStartLandsInParentRepl: RunAgent fires
// SubagentStart on the parent engine, so the dispatch must pin the parent's
// REPL — the hook session state appears in the parent engine's REPL, not in
// the process-global default runner.
func TestIntegration_PerEngine_SubagentStartLandsInParentRepl(t *testing.T) {
	t.Parallel()

	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"SubagentStart": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{
				Type:    hooks.HookTypeJS,
				Code:    `async (input) => { globalThis.__subStart = input.agent_type; return "ok" }`,
				Timeout: 5,
			}}},
		},
	}, &integrationHookRecorder{})

	eng, replTool := newPerEngineHarness(t, hookSystem, "engineA")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.RunAgent(ctx, agent.AgentOpts{Prompt: "sub task", AgentType: "General"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if got := readHookState(t, replTool, `() => globalThis.__subStart`); got != `"General"` {
		t.Errorf("parent hook session __subStart = %s, want \"General\" (SubagentStart must dispatch with the parent engine's REPL pinned)", got)
	}
}

// TestIntegration_PerEngine_SubagentStopLandsInParentRepl: a sub-agent query
// fires SubagentStop from the sub-engine, whose toolRefs are the parent's, so
// the js hook must land in the parent engine's REPL hook session.
func TestIntegration_PerEngine_SubagentStopLandsInParentRepl(t *testing.T) {
	t.Parallel()

	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"SubagentStop": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{
				Type:    hooks.HookTypeJS,
				Code:    `async (input) => { globalThis.__subStop = true; return "ok" }`,
				Timeout: 5,
			}}},
		},
	}, &integrationHookRecorder{})

	eng, replTool := newPerEngineHarness(t, hookSystem, "engineA")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.RunAgent(ctx, agent.AgentOpts{Prompt: "sub task", AgentType: "General"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if got := readHookState(t, replTool, `() => globalThis.__subStop`); got != "true" {
		t.Errorf("parent hook session __subStop = %s, want true (sub-engine dispatch must inherit the parent's REPL)", got)
	}
}

// TestIntegration_PerEngine_ToolExecutorDispatchUsesEngineRepl: the streaming
// executor (engine.go SetJsRunner wiring) routes tool-lifecycle js dispatches
// to the owning engine's REPL — the PreToolUse hook state lands in that
// engine's hook session while the process-global default runner (the
// countingJsRunner from stop_matcher_integration_test.go) stays untouched at
// count 0. Removing the SetJsRunner wiring falls back to the global default:
// the marker would be missing and the count nonzero.
func TestIntegration_PerEngine_ToolExecutorDispatchUsesEngineRepl(t *testing.T) {
	t.Parallel()

	globalRunner := &countingJsRunner{}
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{
				Type: hooks.HookTypeJS,
				Code: `async (input) => { globalThis.__preTool = input.tool_name; return "ok" }`,
			}}},
		},
	}, &integrationHookRecorder{})
	hookSystem.SetJsHookRunner(globalRunner)

	eng, replTool := newPerEngineHarness(t, hookSystem, "engineA",
		mockResponse{events: toolUseStreamEvents("test-model", "tu_pre_1", "Echo", "{}")},
		mockResponse{events: textStreamEvents("test-model", "done")},
	)

	runQuery(t, eng)
	if got := readHookState(t, replTool, `() => globalThis.__preTool`); got != `"Echo"` {
		t.Errorf("engine hook session __preTool = %s, want \"Echo\" (executor dispatch must pin the engine's own REPL)", got)
	}
	if n := globalRunner.count(); n != 0 {
		t.Errorf("global default runner dispatched %d times, want 0 (engine-side dispatch must shadow the global default)", n)
	}
}

// askToolPermChecker asks only for the probe tool so the Agent tool call
// that carries the sub-engine through stays allowed.
type askToolPermChecker struct{}

func (askToolPermChecker) Check(name string, _ json.RawMessage) permission.Decision {
	if name == "Bash" {
		return permission.Decision{Action: permission.ActionAsk, Message: "ask probe"}
	}
	return permission.Decision{Action: permission.ActionAllow}
}

func (askToolPermChecker) HasRules() bool { return true }

// TestIntegration_PerEngine_PermissionRequestFromHookToolNoDeadlock: a js
// hook holds its session mutex for the whole run; when it calls tools.Agent,
// the sub-engine inherits the same REPL as its js runner. If the
// sub-engine's PermissionRequest dispatch loses the hook-origin marker
// (context.Background()), the inner js hook blocks on that mutex forever —
// the outer hook waits for the agent, the agent waits for the hook:
// deadlock. With the marker preserved (dispatch on siblingCtx), the query
// returns in bounded time: the inner js hook is skipped, the sub-agent's
// unanswered ask is interrupted when the outer hook's own deadline expires.
//
// No engine/REPL Close cleanup here on purpose: in the deadlock form the
// leaked goroutine holds the hook session mutex forever, and Close would
// block the test binary instead of just failing the test.
func TestIntegration_PerEngine_PermissionRequestFromHookToolNoDeadlock(t *testing.T) {
	t.Parallel()

	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{
				Type: hooks.HookTypeJS,
				Code: `async (input) => {
					globalThis.__outerStart = true
					await tools.Agent({description: "probe", prompt: "run the ask tool"})
					return "ok"
				}`,
				Timeout: 2,
			}}},
		},
		"PermissionRequest": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{
				Type: hooks.HookTypeJS,
				Code: `async (input) => { globalThis.__permRan = input.tool_name; return {decision: "allow"} }`,
			}}},
		},
	}, &integrationHookRecorder{})

	tl := task.NewList(t.TempDir())
	deps := SharedDeps{WorkingDir: t.TempDir(), Hooks: hookSystem}
	refs := CreateTools(deps, tl)
	refs.Reg.MustRegister(echoTool("engineA"))

	// The sub-engine keeps only fresh CreateTools instances (RunAgent
	// filters by name), so the ask probe must be a real built-in tool.
	mp := &mockProvider{}
	// Parent query ends after one text turn; the Stop hook then drives
	// everything. The shared provider serves the sub-engine next.
	mp.addResponse(textStreamEvents("test-model", "done"), nil)
	mp.addResponse(toolUseStreamEvents("test-model", "tu_ask_1", "Bash", `{"command":"true"}`), nil)
	mp.addResponse(textStreamEvents("test-model", "sub done"), nil)
	eng := New(&Params{
		Provider:          mp,
		ToolsProvider:     refs.Reg.ToolMapFn(),
		Model:             "test-model",
		Logger:            slog.Default(),
		Hooks:             hookSystem,
		PermissionChecker: askToolPermChecker{},
	})
	eng.SetSharedDeps(&deps)
	WireEngine(eng, refs, deps)
	eng.SetToolRefs(refs)

	done := make(chan QueryResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done <- eng.QuerySync(ctx, "start", "")
	}()

	select {
	case result := <-done:
		if result.Error != nil {
			t.Fatalf("QuerySync: %v", result.Error)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("deadlock: query never completed — PermissionRequest dispatch lost the hook-origin marker and re-entered the hook session mutex")
	}

	if got := readHookState(t, refs.REPL, `() => globalThis.__outerStart`); got != "true" {
		t.Errorf("hook session __outerStart = %s, want true (outer hook must run in the engine's REPL)", got)
	}
	if got := readHookState(t, refs.REPL, `() => globalThis.__permRan`); got != "null" {
		t.Errorf("hook session __permRan = %s, want null (inner PermissionRequest js hook must be skipped by the hook-origin marker)", got)
	}
}
