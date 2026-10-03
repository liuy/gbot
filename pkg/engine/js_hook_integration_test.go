package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/repl"
)

// TestIntegration_Stop_JsHook_ExecutesAndPersistsState drives the full js
// hook chain: engine Stop → hooks dispatch (type js) → REPL runner → goja.
// Observable side effect: the hook session's globalThis state, read back
// through a second RunHook (the hooks session is not reachable via Call —
// the session_id is reserved for hook evaluation).
func TestIntegration_Stop_JsHook_ExecutesAndPersistsState(t *testing.T) {
	t.Parallel()

	replTool := repl.New()
	t.Cleanup(replTool.Close)

	code := `async (input) => {
		globalThis.__e2eStopCount = (globalThis.__e2eStopCount || 0) + 1;
		globalThis.__e2eStopEvent = input.hook_event_name;
		return { seen: input.hook_event_name };
	}`
	hookConfig := hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{
			{Matcher: "", Hooks: []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: code}}},
		},
	}
	hookSystem := hooks.NewHooks(hookConfig, &integrationHookRecorder{})
	hookSystem.SetJsHookRunner(replTool)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "all done"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := eng.QuerySync(ctx, "Do something", "")
	if result.Error != nil {
		t.Fatalf("QuerySync: %v", result.Error)
	}

	state, err := replTool.RunHook(ctx, `() => [globalThis.__e2eStopCount, globalThis.__e2eStopEvent]`, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("readback RunHook: %v", err)
	}
	if state != `[1,"Stop"]` {
		t.Errorf("hook session state = %q, want %q", state, `[1,"Stop"]`)
	}
}

// TestIntegration_Stop_JsHook_CallsMcpNamedTool mirrors production wiring
// (bootstrap: executor=eng.ExecuteTool, lister=eng.AllTools) and asserts a
// Stop hook can call tools.mcp__* — the live daemon failed this with
// "Object has no member 'mcp__plugin_browser_playwright__browser_tabs'".
func TestIntegration_Stop_JsHook_CallsMcpNamedTool(t *testing.T) {
	t.Parallel()

	tabsTool := tool.BuildTool(tool.ToolDef{
		Name_: "mcp__plugin_browser_playwright__browser_tabs",
		InputSchema_: func() json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"}}}`)
		},
		Description_: func(json.RawMessage) (string, error) {
			return "List browser tabs", nil
		},
		Call_: func(ctx context.Context, args json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "- 0: [](about:blank)\n- 1: [](example.com)"}, nil
		},
	})

	replTool := repl.New()
	t.Cleanup(replTool.Close)

	code := `async (input) => {
		const out = await tools.mcp__plugin_browser_playwright__browser_tabs({ action: "list" });
		globalThis.__hookTabs = out;
		return "ok";
	}`
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{
			{Matcher: "", Hooks: []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: code}}},
		},
	}, &integrationHookRecorder{})
	hookSystem.SetJsHookRunner(replTool)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"mcp__plugin_browser_playwright__browser_tabs": tabsTool}
		},
	})
	t.Cleanup(func() { eng.Close() })

	// Production wiring, same as bootstrap.go:190/199.
	var replAskMu sync.Mutex
	replSessionAllowed := make(map[string]bool)
	replTool.SetToolExecutor(func(toolCtx context.Context, name string, args json.RawMessage) (string, error) {
		return eng.ExecuteTool(toolCtx, name, args, replSessionAllowed, &replAskMu)
	})
	replTool.SetToolLister(func() []repl.ToolMeta {
		all := eng.AllTools()
		names := make([]string, 0, len(all))
		for name := range all {
			names = append(names, name)
		}
		slices.Sort(names)
		metas := make([]repl.ToolMeta, 0, len(names))
		for _, name := range names {
			desc, err := all[name].Description(nil)
			if err != nil {
				desc = ""
			}
			metas = append(metas, repl.ToolMeta{Name: name, Description: desc})
		}
		return metas
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := eng.QuerySync(ctx, "Do something", "")
	if result.Error != nil {
		t.Fatalf("QuerySync: %v", result.Error)
	}

	state, err := replTool.RunHook(ctx, `() => String(globalThis.__hookTabs)`, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("readback RunHook: %v", err)
	}
	if !strings.Contains(state, "example.com") {
		t.Errorf("hook saw tabs output %q, want the fake tabs list — tools.mcp__* was missing from the hook session", state)
	}
}
