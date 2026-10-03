package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// js hook dispatch — runner injection, timeout, skip paths
// ---------------------------------------------------------------------------

// jsRunnerRecorder records RunHook calls and returns canned results.
type jsRunnerRecorder struct {
	mu    sync.Mutex
	calls []jsRunnerCall
	ret   string
	err   error
}

type jsRunnerCall struct {
	ctx     context.Context
	source  string
	input   string
	timeout time.Duration
}

func (r *jsRunnerRecorder) RunHook(ctx context.Context, source string, input json.RawMessage, timeout time.Duration) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, jsRunnerCall{ctx: ctx, source: source, input: string(input), timeout: timeout})
	return r.ret, r.err
}

func (r *jsRunnerRecorder) Calls() []jsRunnerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]jsRunnerCall(nil), r.calls...)
}

func (r *jsRunnerRecorder) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestDispatch_JS_CallsRunnerAndRecordsResult(t *testing.T) {
	code := `async (input) => ({ blocked: false })`
	rec := &jsRunnerRecorder{ret: `{"blocked":false}`}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{{Type: HookTypeJS, Code: code}}},
		},
	}, &HookRecorder{})
	h.SetJsHookRunner(rec)

	type dispatchKey struct{}
	dispatchCtx := context.WithValue(context.Background(), dispatchKey{}, "stop-dispatch")
	results := h.dispatch(dispatchCtx, HookStop, &HookInput{HookEventName: "Stop", SessionID: "s1"})

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Outcome != HookOutcomeSuccess {
		t.Errorf("Outcome = %v, want success", results[0].Outcome)
	}
	if results[0].Stdout != `{"blocked":false}` {
		t.Errorf("Stdout = %q, want %q", results[0].Stdout, `{"blocked":false}`)
	}
	if results[0].HookName != code {
		t.Errorf("HookName = %q, want the hook code", results[0].HookName)
	}

	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(calls))
	}
	if calls[0].source != code {
		t.Errorf("source = %q, want the hook code", calls[0].source)
	}
	if calls[0].timeout != DefaultJsHookTimeout {
		t.Errorf("timeout = %v, want default %v", calls[0].timeout, DefaultJsHookTimeout)
	}
	if calls[0].ctx != dispatchCtx {
		t.Errorf("runner ctx = %v, want the dispatch ctx (cancellation must reach the hook)", calls[0].ctx)
	}
	var payload HookInput
	if err := json.Unmarshal([]byte(calls[0].input), &payload); err != nil {
		t.Fatalf("runner input is not valid HookInput JSON: %v", err)
	}
	if payload.HookEventName != "Stop" {
		t.Errorf("input hook_event_name = %q, want Stop", payload.HookEventName)
	}
	if payload.SessionID != "s1" {
		t.Errorf("input session_id = %q, want s1", payload.SessionID)
	}
}

func TestDispatch_JS_TimeoutOverride(t *testing.T) {
	rec := &jsRunnerRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{
			{Hooks: []HookConfig{{Type: HookTypeJS, Code: "async () => 1", Timeout: 2}}},
		},
	}, &HookRecorder{})
	h.SetJsHookRunner(rec)

	results := h.dispatch(context.Background(), HookStop, &HookInput{})

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(calls))
	}
	if calls[0].timeout != 2*time.Second {
		t.Errorf("timeout = %v, want 2s override", calls[0].timeout)
	}
}

func TestDispatch_JS_NilRunnerSkips(t *testing.T) {
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{
			{Hooks: []HookConfig{{Type: HookTypeJS, Code: "async () => 1"}}},
		},
	}, &HookRecorder{})

	results := h.dispatch(context.Background(), HookStop, &HookInput{})

	if len(results) != 0 {
		t.Fatalf("results = %d, want 0 (nil runner must skip)", len(results))
	}
}

func TestDispatch_JS_EmptyCodeSkips(t *testing.T) {
	rec := &jsRunnerRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{
			{Hooks: []HookConfig{{Type: HookTypeJS}}},
		},
	}, &HookRecorder{})
	h.SetJsHookRunner(rec)

	results := h.dispatch(context.Background(), HookStop, &HookInput{})

	if len(results) != 0 {
		t.Fatalf("results = %d, want 0 (empty code must skip)", len(results))
	}
	if rec.CallCount() != 0 {
		t.Errorf("runner calls = %d, want 0 (empty code must not reach runner)", rec.CallCount())
	}
}

func TestDispatch_JS_AsyncSkips(t *testing.T) {
	rec := &jsRunnerRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{
			{Hooks: []HookConfig{{Type: HookTypeJS, Code: "async () => 1", Async: true}}},
		},
	}, &HookRecorder{})
	h.SetJsHookRunner(rec)

	results := h.dispatch(context.Background(), HookStop, &HookInput{})

	if len(results) != 0 {
		t.Fatalf("results = %d, want 0 (async is not supported for js)", len(results))
	}
	if rec.CallCount() != 0 {
		t.Errorf("runner calls = %d, want 0 (async js must not reach runner)", rec.CallCount())
	}
}

func TestDispatch_JS_RunnerErrorIsNonBlocking(t *testing.T) {
	rec := &jsRunnerRecorder{err: errors.New("boom")}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{
			{Hooks: []HookConfig{{Type: HookTypeJS, Code: "async () => 1"}}},
		},
	}, &HookRecorder{})
	h.SetJsHookRunner(rec)

	results := h.dispatch(context.Background(), HookStop, &HookInput{})

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Outcome != HookOutcomeNonBlockingError {
		t.Errorf("Outcome = %v, want non_blocking_error", results[0].Outcome)
	}
	if results[0].Stderr != "boom" {
		t.Errorf("Stderr = %q, want %q", results[0].Stderr, "boom")
	}
}

func TestDispatch_JS_HookOriginCtxSkipsJsButNotCommandHooks(t *testing.T) {
	jsRec := &jsRunnerRecorder{ret: `"js"`}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{
			{Hooks: []HookConfig{
				{Type: HookTypeJS, Code: "async () => 1"},
				{Type: HookTypeCommand, Command: "echo ok"},
			}},
		},
	}, &HookRecorder{})
	h.SetJsHookRunner(jsRec)

	marked := WithHookOrigin(context.Background())
	results := h.dispatch(marked, HookStop, &HookInput{})

	if len(results) != 1 {
		t.Fatalf("marked-ctx results = %d, want 1 (only the command hook; the js hook must be skipped)", len(results))
	}
	if results[0].HookName != "echo ok" {
		t.Errorf("results[0].HookName = %q, want the command hook %q", results[0].HookName, "echo ok")
	}
	if jsRec.CallCount() != 0 {
		t.Errorf("js runner calls on marked ctx = %d, want 0", jsRec.CallCount())
	}

	plain := h.dispatch(context.Background(), HookStop, &HookInput{})
	if len(plain) != 2 {
		t.Fatalf("plain-ctx results = %d, want 2 (marker must not suppress ordinary dispatch)", len(plain))
	}
	if jsRec.CallCount() != 1 {
		t.Errorf("js runner calls after plain dispatch = %d, want 1", jsRec.CallCount())
	}
}

func TestDispatch_JS_OnceKeyDistinguishesCode(t *testing.T) {
	rec := &jsRunnerRecorder{}
	h := NewHooks(HooksConfig{
		"Stop": []HookMatcher{
			{Hooks: []HookConfig{
				{Type: HookTypeJS, Code: "async () => 1", Once: true},
				{Type: HookTypeJS, Code: "async () => 2", Once: true},
			}},
		},
	}, &HookRecorder{})
	h.SetJsHookRunner(rec)

	results := h.dispatch(context.Background(), HookStop, &HookInput{})

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (distinct code must not collapse once keys)", len(results))
	}
	if rec.CallCount() != 2 {
		t.Errorf("runner calls = %d, want 2", rec.CallCount())
	}
}
