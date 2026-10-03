package repl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/liuy/gbot/pkg/hooks"
)

// ---------------------------------------------------------------------------
// REPLTool.RunHook — js hook evaluation entry (hooks.JsHookRunner impl)
// ---------------------------------------------------------------------------

func TestRunHook_FunctionFormReturnsJSON(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	out, err := tl.RunHook(context.Background(), "async (input) => ({ echo: input.name })", []byte(`{"name":"gbot"}`), 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != `{"echo":"gbot"}` {
		t.Errorf("result = %q, want %q", out, `{"echo":"gbot"}`)
	}
}

func TestRunHook_InputPassedAsArgument(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	code := `async (input) => {
		if (input.hook_event_name !== "Stop") {
			throw new Error("unexpected event: " + input.hook_event_name);
		}
		return input.hook_event_name;
	}`
	out, err := tl.RunHook(context.Background(), code, []byte(`{"hook_event_name":"Stop"}`), 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != `"Stop"` {
		t.Errorf("result = %q, want %q", out, `"Stop"`)
	}
}

func TestRunHook_AwaitSettles(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	code := `async (input) => {
		await new Promise((resolve) => setTimeout(resolve, 20));
		return input.ok;
	}`
	out, err := tl.RunHook(context.Background(), code, []byte(`{"ok":true}`), 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != "true" {
		t.Errorf("result = %q, want %q", out, "true")
	}
}

func TestRunHook_StatePersistsAcrossInvocations(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	code := `async () => { globalThis.n = (globalThis.n || 0) + 1; return globalThis.n; }`
	for want := 1; want <= 2; want++ {
		out, err := tl.RunHook(context.Background(), code, nil, time.Second)
		if err != nil {
			t.Fatalf("RunHook run %d: %v", want, err)
		}
		if out != strconv.Itoa(want) {
			t.Errorf("run %d: result = %q, want %q", want, out, strconv.Itoa(want))
		}
	}
}

func TestRunHook_NilInputBecomesNull(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	out, err := tl.RunHook(context.Background(), "async (input) => input === null", nil, time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != "true" {
		t.Errorf("result = %q, want %q", out, "true")
	}
}

func TestRunHook_UndefinedReturnEncodesNull(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	out, err := tl.RunHook(context.Background(), "async () => {}", nil, time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != "null" {
		t.Errorf("result = %q, want %q", out, "null")
	}
}

func TestRunHook_TimeoutKillsInfiniteLoop(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	_, err := tl.RunHook(context.Background(), "async () => { while (true) {} }", nil, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error from infinite-loop hook")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want timeout message", err.Error())
	}
}

func TestRunHook_TimerLongerThanTimeoutReturnsAtDeadline(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	start := time.Now() // REAL-TIME: deadline-bounded return assertion
	out, err := tl.RunHook(context.Background(), `async () => { setTimeout(() => {}, 3000); return "done"; }`, nil, 200*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != `"done"` {
		t.Errorf("result = %q, want %q", out, `"done"`)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("RunHook returned after %v, want ~200ms (stranded setTimeout kept the loop alive)", elapsed)
	}
}

func TestRunHook_IntervalHookDoesNotHang(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	type runResult struct {
		out string
		err error
	}
	ch := make(chan runResult, 1)
	go func() {
		out, err := tl.RunHook(context.Background(), `async () => { setInterval(() => {}, 50); return "done"; }`, nil, 200*time.Millisecond)
		ch <- runResult{out, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("RunHook: %v", r.err)
		}
		if r.out != `"done"` {
			t.Errorf("result = %q, want %q", r.out, `"done"`)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunHook hung: leftover setInterval keeps the event loop alive past the deadline")
	}
}

func TestRunHook_NonPositiveTimeoutUsesFullDefault(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	out, err := tl.RunHook(context.Background(), `async () => { await new Promise((r) => setTimeout(r, 30)); return "late"; }`, nil, 0)
	if err != nil {
		t.Fatalf("RunHook with timeout=0: %v", err)
	}
	if out != `"late"` {
		t.Errorf("result = %q, want %q", out, `"late"`)
	}
}

func TestRunHook_PanicThenNextHookRecovers(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	if _, err := tl.RunHook(context.Background(), "async () => 1", nil, time.Second); err != nil {
		t.Fatalf("seed RunHook: %v", err)
	}
	sv, ok := tl.sessions.Load(hookSessionID)
	if !ok {
		t.Fatal("hook session missing after seed RunHook")
	}
	sess := sv.(*Session)
	sess.loop.Run(func(vm *goja.Runtime) {
		if err := vm.Set("__boom", func() { panic("go-level boom") }); err != nil {
			t.Errorf("set __boom: %v", err)
		}
	})

	_, err := tl.RunHook(context.Background(), "() => __boom()", nil, time.Second)
	if err == nil {
		t.Fatal("expected fatal error from panicking hook")
	}
	if !strings.Contains(err.Error(), "fatal") {
		t.Errorf("error = %q, want fatal marker", err.Error())
	}

	out, err := tl.RunHook(context.Background(), "async () => 42", nil, time.Second)
	if err != nil {
		t.Fatalf("RunHook after panic: %v (session must self-heal)", err)
	}
	if out != "42" {
		t.Errorf("result = %q, want %q", out, "42")
	}
}

func TestRunHook_ClosedSessionHealsOnNextHook(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	closed, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	closed.Close()
	tl.sessions.Store(hookSessionID, closed)

	out, err := tl.RunHook(context.Background(), "async () => 1", nil, time.Second)
	if err != nil {
		t.Fatalf("RunHook on closed session: %v (hooks session must be rebuilt)", err)
	}
	if out != "1" {
		t.Errorf("result = %q, want %q", out, "1")
	}
}

func TestSession_RunHook_ClosedSessionSentinel(t *testing.T) {
	closed, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	closed.Close()

	_, err = closed.RunHook(context.Background(), "async () => 1", nil, time.Second, nil, nil)
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("error = %v, want ErrNoSession sentinel", err)
	}
}

func TestCall_RejectsReservedHooksSession(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	ctx := context.Background()
	for _, in := range []string{
		`{"code":"1","session_id":"hooks"}`,
		`{"reset":true,"session_id":"hooks"}`,
	} {
		_, err := tl.Call(ctx, json.RawMessage(in), nil)
		if err == nil {
			t.Errorf("Call(%s) = nil error, want reserved-session rejection", in)
			continue
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("Call(%s) error = %q, want reserved message", in, err.Error())
		}
	}
	if _, ok := tl.sessions.Load(hookSessionID); ok {
		t.Error("hook session must not be created by rejected Call attempts")
	}
}

func TestRunHook_ConcurrentHooksSerializeBothComplete(t *testing.T) {
	// Serial semantics (plain Lock, like Execute): concurrent hooks queue on
	// the session mutex and every one runs to completion — no busy rejection,
	// no deadlock.
	tl := New()
	t.Cleanup(tl.Close)

	const n = 2
	type runResult struct {
		out string
		err error
	}
	results := make(chan runResult, n)
	for i := range n {
		go func(idx int) {
			out, err := tl.RunHook(context.Background(),
				fmt.Sprintf(`async () => { await new Promise((r) => setTimeout(r, 30)); return %d; }`, idx),
				nil, 2*time.Second)
			results <- runResult{out, err}
		}(i)
	}
	seen := map[string]bool{}
	for range n {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("concurrent RunHook: %v (serialization must run every hook, not reject)", r.err)
			}
			seen[r.out] = true
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent RunHook hung (deadlock on session mutex)")
		}
	}
	if !seen["0"] || !seen["1"] {
		t.Errorf("results = %v, want both hooks completed with their own return values", seen)
	}
	out, err := tl.RunHook(context.Background(), "async () => 7", nil, time.Second)
	if err != nil {
		t.Fatalf("RunHook after concurrent pair: %v", err)
	}
	if out != "7" {
		t.Errorf("result = %q, want %q", out, "7")
	}
}

func TestRunHook_SyncThrowPropagates(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	_, err := tl.RunHook(context.Background(), `() => { throw new Error("sync boom") }`, nil, time.Second)
	if err == nil {
		t.Fatal("expected error from synchronously throwing hook")
	}
	if !strings.Contains(err.Error(), "sync boom") {
		t.Errorf("error = %q, want thrown message", err.Error())
	}
}

func TestRunHook_AsyncRejectionPropagates(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	_, err := tl.RunHook(context.Background(), `async () => { throw new Error("async boom") }`, nil, time.Second)
	if err == nil {
		t.Fatal("expected error from rejecting async hook")
	}
	if !strings.Contains(err.Error(), "async boom") {
		t.Errorf("error = %q, want rejection reason", err.Error())
	}
}

func TestRunHook_NonFunctionSourceRejected(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	_, err := tl.RunHook(context.Background(), "42", nil, time.Second)
	if err == nil {
		t.Fatal("expected error for non-function source")
	}
	if !strings.Contains(err.Error(), "function expression") {
		t.Errorf("error = %q, want function-expression guidance", err.Error())
	}
}

func TestRunHook_SyntaxErrorReported(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	_, err := tl.RunHook(context.Background(), "async ( => {}", nil, time.Second)
	if err == nil {
		t.Fatal("expected error for syntactically invalid source")
	}
	if !strings.Contains(err.Error(), "evaluate") {
		t.Errorf("error = %q, want evaluate failure", err.Error())
	}
}

func TestRunHook_LazilyCreatesHookSession(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	if _, ok := tl.sessions.Load(hookSessionID); ok {
		t.Fatal("hook session must not exist before first RunHook")
	}
	out, err := tl.RunHook(context.Background(), "async () => 7", nil, time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != "7" {
		t.Errorf("result = %q, want %q", out, "7")
	}
	if _, ok := tl.sessions.Load(hookSessionID); !ok {
		t.Fatal("hook session missing after RunHook")
	}
}

func TestRunHook_ToolCallRunsInjectedExecutorAndReturnsResult(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	var mu sync.Mutex
	var calls []string
	tl.SetToolExecutor(func(_ context.Context, name string, args json.RawMessage) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name+"|"+string(args))
		return `{"ok":true}`, nil
	})
	tl.SetToolLister(func() []ToolMeta {
		return []ToolMeta{{Name: "Browser_tabs", Description: "list tabs"}}
	})

	code := `async () => ({ r: await tools.Browser_tabs({ action: "list" }) })`
	out, err := tl.RunHook(context.Background(), code, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != `{"r":{"ok":true}}` {
		t.Errorf("result = %q, want %q", out, `{"r":{"ok":true}}`)
	}
	mu.Lock()
	wantCalls := []string{`Browser_tabs|{"action":"list"}`}
	if len(calls) != 1 || calls[0] != wantCalls[0] {
		t.Errorf("executor calls = %v, want %v", calls, wantCalls)
	}
	mu.Unlock()
}

// nopHookExecutor satisfies hooks.HookExecutor for dispatch simulations inside
// the repl package: command hooks must keep firing on hook-origin ctxs.
type nopHookExecutor struct{}

func (nopHookExecutor) ExecuteHook(context.Context, string, *hooks.HookInput, time.Duration, []string) hooks.HookResult {
	return hooks.HookResult{Outcome: hooks.HookOutcomeSuccess}
}

// countingJsRunner counts js hook executions that escaped the hook-origin skip.
type countingJsRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *countingJsRunner) RunHook(context.Context, string, json.RawMessage, time.Duration) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return `"inner-ran"`, nil
}

func (r *countingJsRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestRunHook_ToolCallSkipsReentrantJsHook(t *testing.T) {
	tl := New()
	t.Cleanup(tl.Close)

	inner := &countingJsRunner{}
	h := hooks.NewHooks(hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{
			{Hooks: []hooks.HookConfig{{Type: hooks.HookTypeJS, Code: "async () => 1"}}},
		},
	}, nopHookExecutor{})
	h.SetJsHookRunner(inner)

	var markerSeen bool
	tl.SetToolExecutor(func(toolCtx context.Context, name string, _ json.RawMessage) (string, error) {
		markerSeen = hooks.FromHookOrigin(toolCtx)
		// Simulate the engine's PreToolUse dispatch with the ctx the tool
		// call carried: the js hook must be skipped (0 results), otherwise
		// the real runner would re-enter the hook session mutex.
		if _, results := h.PreToolUse(toolCtx, &hooks.HookInput{HookEventName: "PreToolUse", ToolName: name}); len(results) != 0 {
			return "", fmt.Errorf("inner js hook re-entered dispatch: %d results", len(results))
		}
		return "tool-done", nil
	})
	tl.SetToolLister(func() []ToolMeta {
		return []ToolMeta{{Name: "TabCloser", Description: "close tabs"}}
	})

	code := `async () => "outer:" + await tools.TabCloser({})`
	out, err := tl.RunHook(context.Background(), code, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v (outer hook must complete after the re-entrant dispatch is skipped)", err)
	}
	if out != `"outer:tool-done"` {
		t.Errorf("result = %q, want %q", out, `"outer:tool-done"`)
	}
	if !markerSeen {
		t.Error("tool call ctx missing hook-origin marker (marker must ride the toolFn ctx)")
	}
	if n := inner.Calls(); n != 0 {
		t.Errorf("inner js hook calls = %d, want 0 (hook-origin ctx must skip js hooks)", n)
	}
}
