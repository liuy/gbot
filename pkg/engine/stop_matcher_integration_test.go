package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/repl"
	"github.com/liuy/gbot/pkg/types"
)

// countingJsRunner records every RunHook invocation's input JSON. Standing in
// for the repl runner, its call count is the observable for "Stop hook
// skipped": a gated-out hook never reaches the js runtime.
type countingJsRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *countingJsRunner) RunHook(ctx context.Context, source string, input json.RawMessage, timeout time.Duration) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, string(input))
	return "ok", nil
}

func (r *countingJsRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// multiToolUseStreamEvents builds one assistant turn containing several
// tool_use blocks (fired sequentially on the stream, executed concurrently
// because mockTool is concurrency-safe).
func multiToolUseStreamEvents(model string, calls [][2]string) []llm.StreamEvent {
	events := []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 20}}},
	}
	for i, c := range calls {
		events = append(events,
			llm.StreamEvent{Type: "content_block_start", Index: i, ContentBlock: &types.ContentBlock{Type: types.ContentTypeToolUse, ID: c[0], Name: c[1]}},
			llm.StreamEvent{Type: "content_block_delta", Index: i, Delta: &llm.StreamDelta{Type: "input_json_delta", PartialJSON: `{}`}},
			llm.StreamEvent{Type: "content_block_stop", Index: i},
		)
	}
	events = append(events,
		llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "tool_use"}, Usage: &types.Usage{OutputTokens: 10}},
		llm.StreamEvent{Type: "message_stop"},
	)
	return events
}

// TestIntegration_StopMatcher_ToolUsed_JsHookRuns drives the full chain:
// engine collects the tools executed in the query → runStopHook fills
// ToolNames → dispatch any-matches the matcher → js hook runs in the hook
// session and can observe input.tool_names.
func TestIntegration_StopMatcher_ToolUsed_JsHookRuns(t *testing.T) {
	t.Parallel()

	replTool := repl.New()
	t.Cleanup(replTool.Close)

	code := `async (input) => {
		globalThis.__stopCount = (globalThis.__stopCount || 0) + 1;
		globalThis.__stopToolNames = input.tool_names;
		return "ok";
	}`
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{{
			Matcher: "mcp__plugin_browser_playwright__.*",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: code}},
		}},
	}, &integrationHookRecorder{})
	hookSystem.SetJsHookRunner(replTool)

	tabsTool := &mockTool{name: "mcp__plugin_browser_playwright__browser_tabs", enabled: true}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "mcp__plugin_browser_playwright__browser_tabs", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{tabsTool.Name(): tabsTool}
		},
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := eng.QuerySync(ctx, "open a tab then finish", "")
	if result.Error != nil {
		t.Fatalf("QuerySync: %v", result.Error)
	}

	state, err := replTool.RunHook(ctx, `() => [globalThis.__stopCount, globalThis.__stopToolNames]`, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("readback RunHook: %v", err)
	}
	if state != `[1,["mcp__plugin_browser_playwright__browser_tabs"]]` {
		t.Errorf("hook session state = %q, want one Stop run seeing exactly the executed tool name", state)
	}
}

// TestIntegration_StopMatcher_NoMatchingTool_JsHookSkipped covers the skip
// path: a text-only query and a query that only used a non-matching tool
// never reach the js runner.
func TestIntegration_StopMatcher_NoMatchingTool_JsHookSkipped(t *testing.T) {
	t.Parallel()

	counter := &countingJsRunner{}
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{{
			Matcher: "mcp__plugin_browser_playwright__.*",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: `async () => "ok"`}},
		}},
	}, &integrationHookRecorder{})
	hookSystem.SetJsHookRunner(counter)

	plainTool := &mockTool{name: "task_tool", enabled: true}
	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "just text"), nil)
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "task_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{plainTool.Name(): plainTool}
		},
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if result := eng.QuerySync(ctx, "text only", ""); result.Error != nil {
		t.Fatalf("QuerySync(text): %v", result.Error)
	}
	if result := eng.QuerySync(ctx, "use plain tool", ""); result.Error != nil {
		t.Fatalf("QuerySync(tool): %v", result.Error)
	}

	got := counter.count()
	want := 0 // both queries missed the matcher: no run may reach the js runtime
	if got != want {
		t.Errorf("js runner calls = %d, want %d (no query used a matcher-matching tool)", got, want)
	}
}

// TestIntegration_StopMatcher_CollectionResetsPerQuery proves the collection
// is per-query: a browser-tool query fires the Stop hook once, and a
// following text-only query does not fire it again from stale state.
func TestIntegration_StopMatcher_CollectionResetsPerQuery(t *testing.T) {
	t.Parallel()

	counter := &countingJsRunner{}
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{{
			Matcher: "mcp__plugin_browser_playwright__.*",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: `async () => "ok"`}},
		}},
	}, &integrationHookRecorder{})
	hookSystem.SetJsHookRunner(counter)

	tabsTool := &mockTool{name: "mcp__plugin_browser_playwright__browser_tabs", enabled: true}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "mcp__plugin_browser_playwright__browser_tabs", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)
	mp.addResponse(textStreamEvents("test-model", "plain follow-up"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{tabsTool.Name(): tabsTool}
		},
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if result := eng.QuerySync(ctx, "use browser", ""); result.Error != nil {
		t.Fatalf("QuerySync(browser): %v", result.Error)
	}
	if result := eng.QuerySync(ctx, "text only follow-up", ""); result.Error != nil {
		t.Fatalf("QuerySync(text): %v", result.Error)
	}

	if got := counter.count(); got != 1 {
		t.Errorf("js runner calls = %d, want 1 (second query must not re-fire from the first query's collection)", got)
	}
}

// TestIntegration_StopMatcher_ParallelTools_CollectionDedup runs four
// concurrency-safe tools in parallel (two with the same name) and asserts
// the Stop hook sees the deduped set — concurrent recording loses nothing
// and duplicates collapse.
func TestIntegration_StopMatcher_ParallelTools_CollectionDedup(t *testing.T) {
	t.Parallel()

	replTool := repl.New()
	t.Cleanup(replTool.Close)

	code := `async (input) => {
		globalThis.__stopToolNames = input.tool_names;
		return "ok";
	}`
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{{
			Hooks: []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: code}},
		}},
	}, &integrationHookRecorder{})
	hookSystem.SetJsHookRunner(replTool)

	tabsTool := &mockTool{name: "mcp__plugin_browser_playwright__browser_tabs", enabled: true}
	otherMcp := &mockTool{name: "mcp__other_server__thing", enabled: true}
	plain := &mockTool{name: "task_tool", enabled: true}
	tools := map[string]tool.Tool{
		tabsTool.Name(): tabsTool,
		otherMcp.Name(): otherMcp,
		plain.Name():    plain,
	}

	mp := &mockProvider{}
	mp.addResponse(multiToolUseStreamEvents("test-model", [][2]string{
		{"t1", "mcp__plugin_browser_playwright__browser_tabs"},
		{"t2", "mcp__plugin_browser_playwright__browser_tabs"},
		{"t3", "mcp__other_server__thing"},
		{"t4", "task_tool"},
	}), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	eng := New(&Params{
		Provider:      mp,
		Model:         "test-model",
		Logger:        slog.Default(),
		Hooks:         hookSystem,
		ToolsProvider: func() map[string]tool.Tool { return tools },
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := eng.QuerySync(ctx, "parallel work", "")
	if result.Error != nil {
		t.Fatalf("QuerySync: %v", result.Error)
	}

	state, err := replTool.RunHook(ctx, `() => globalThis.__stopToolNames`, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("readback RunHook: %v", err)
	}
	want := `["mcp__other_server__thing","mcp__plugin_browser_playwright__browser_tabs","task_tool"]`
	if state != want {
		t.Errorf("deduped tool names = %q, want %q (both browser_tabs calls collapse to one entry)", state, want)
	}
}

// TestIntegration_SubagentStop_FillsOwnCollection verifies a sub-engine's
// SubagentStop sees only its own executed tools, not the parent's.
func TestIntegration_SubagentStop_FillsOwnCollection(t *testing.T) {
	t.Parallel()

	counter := &countingJsRunner{}
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"SubagentStop": []hooks.HookMatcher{{
			Matcher: "mcp__plugin_browser_playwright__.*",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: `async () => "ok"`}},
		}},
	}, &integrationHookRecorder{})
	hookSystem.SetJsHookRunner(counter)

	plainTool := &mockTool{name: "task_tool", enabled: true}
	tabsTool := &mockTool{name: "mcp__plugin_browser_playwright__browser_tabs", enabled: true}
	allTools := map[string]tool.Tool{
		plainTool.Name(): plainTool,
		tabsTool.Name():  tabsTool,
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", "mcp__plugin_browser_playwright__browser_tabs", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "sub done"), nil)

	parent := New(&Params{
		Provider:      mp,
		Model:         "test-model",
		Logger:        slog.Default(),
		Hooks:         hookSystem,
		ToolsProvider: func() map[string]tool.Tool { return allTools },
	})
	t.Cleanup(func() { parent.Close() })

	sub := parent.NewSubEngine(SubEngineOptions{Tools: allTools, AgentType: "General"})
	t.Cleanup(func() { sub.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := sub.QuerySync(ctx, "use browser in subagent", "")
	if result.Error != nil {
		t.Fatalf("QuerySync(sub): %v", result.Error)
	}

	if got := counter.count(); got != 1 {
		t.Errorf("js runner calls = %d, want 1 (sub-engine's own mcp tool use gates its SubagentStop)", got)
	}
}

// TestIntegration_StopMatcher_BlockedToolNotCollected pins that a tool
// vetoed by the PreToolUse gate never enters ToolNames — moving the record
// call above the gates would make the Stop hook fire for a tool that never
// ran.
func TestIntegration_StopMatcher_BlockedToolNotCollected(t *testing.T) {
	t.Parallel()
	counter := &countingJsRunner{}
	rec := &integrationHookRecorder{
		results: []hooks.HookResult{
			{Outcome: hooks.HookOutcomeBlocking, Stderr: "mcp tool forbidden", HookName: "block-mcp"},
		},
	}
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{{
			Matcher: "mcp__plugin_browser_playwright__.*",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "block-mcp"}},
		}},
		"Stop": []hooks.HookMatcher{{
			Matcher: "mcp__plugin_browser_playwright__.*",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: `async () => "ok"`}},
		}},
	}, rec)
	hookSystem.SetJsHookRunner(counter)

	var toolCalled bool
	mt := &mockTool{
		name:    "mcp__plugin_browser_playwright__browser_tabs",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			toolCalled = true
			return &tool.ToolResult{Data: "should not run"}, nil
		},
	}
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "t1", mt.Name(), `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "blocked, giving up"), nil)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
		Hooks:  hookSystem,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := eng.QuerySync(ctx, "use the tool", "")
	if result.Error != nil {
		t.Fatalf("QuerySync: %v", result.Error)
	}
	if toolCalled {
		t.Fatal("tool ran despite PreToolUse block")
	}
	if got, want := counter.count(), 0; got != want {
		t.Errorf("js runner calls = %d, want %d (blocked tool must not gate the Stop hook in)", got, want)
	}
}
