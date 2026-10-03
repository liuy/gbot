package engine

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hooks"
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
