package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Test hook executor — records calls for assertions
// ---------------------------------------------------------------------------

type integrationHookRecorder struct {
	mu      sync.Mutex
	calls   []hookCall
	results []hooks.HookResult
	index   int
}

type hookCall struct {
	event   string
	command string
	input   *hooks.HookInput
}

func (r *integrationHookRecorder) ExecuteHook(ctx context.Context, command string, input *hooks.HookInput, timeout time.Duration) hooks.HookResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, hookCall{event: input.HookEventName, command: command, input: input})
	if r.index < len(r.results) {
		result := r.results[r.index]
		r.index++
		return result
	}
	return hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, HookName: command}
}

func (r *integrationHookRecorder) Calls() []hookCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hookCall(nil), r.calls...)
}

func (r *integrationHookRecorder) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *integrationHookRecorder) Events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var events []string
	for _, c := range r.calls {
		events = append(events, c.event)
	}
	return events
}

// ---------------------------------------------------------------------------
// PreToolUse: hook blocks tool execution
// ---------------------------------------------------------------------------

func TestIntegration_PreToolUse_BlockPreventsExecution(t *testing.T) {
	t.Parallel()

	// Hook recorder that blocks Bash tool calls
	rec := &integrationHookRecorder{
		results: []hooks.HookResult{
			{Outcome: hooks.HookOutcomeBlocking, Stderr: "Bash is forbidden", HookName: "block-bash"},
		},
	}
	hookConfig := hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{
			{Matcher: "my_tool", Hooks: []hooks.HookConfig{
				{Type: hooks.HookTypeCommand, Command: "block-bash"},
			}},
		},
	}
	hookSystem := hooks.NewHooks(hookConfig, rec)

	// Track if tool was actually called
	var toolCalled bool
	mt := &mockTool{
		name:    "my_tool",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			toolCalled = true
			return &tool.ToolResult{Data: "should not reach here"}, nil
		},
	}

	// First response: LLM calls the tool. Second response: LLM says "ok" after block.
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Tool was blocked"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "Use the tool", nil)
	for range eventCh {
	}
	result := <-resultCh

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Tool should NOT have been called
	if toolCalled {
		t.Error("tool should not have been called — PreToolUse hook blocked it")
	}

	// Hook should have been called once (PreToolUse)
	if rec.CallCount() < 1 {
		t.Errorf("expected at least 1 hook call, got %d", rec.CallCount())
	}
	events := rec.Events()
	if len(events) == 0 || events[0] != "PreToolUse" {
		t.Errorf("expected first hook event to be PreToolUse, got %v", events)
	}
}

// ---------------------------------------------------------------------------
// PreToolUse: hook approves — tool executes normally
// ---------------------------------------------------------------------------

func TestIntegration_PreToolUse_ApproveAllowsExecution(t *testing.T) {
	t.Parallel()

	rec := &integrationHookRecorder{
		results: []hooks.HookResult{
			{Outcome: hooks.HookOutcomeSuccess, Output: &hooks.HookOutput{Decision: "approve"}, HookName: "approve-hook"},
		},
	}
	hookConfig := hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{
			{Matcher: "*", Hooks: []hooks.HookConfig{
				{Type: hooks.HookTypeCommand, Command: "approve-hook"},
			}},
		},
	}
	hookSystem := hooks.NewHooks(hookConfig, rec)

	var toolCalled bool
	mt := &mockTool{
		name:    "my_tool",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			toolCalled = true
			return &tool.ToolResult{Data: "success"}, nil
		},
	}

	// First: tool use. Second: text response.
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "Use the tool", nil)
	for range eventCh {
	}
	result := <-resultCh

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !toolCalled {
		t.Error("tool should have been called — PreToolUse hook approved it")
	}

	// Verify PreToolUse hook fired
	calls := rec.Calls()
	if len(calls) == 0 {
		t.Fatal("expected at least 1 hook call")
	}
	if calls[0].event != "PreToolUse" {
		t.Errorf("expected PreToolUse, got %q", calls[0].event)
	}
}

// ---------------------------------------------------------------------------
// PostToolUse: fires after successful tool execution
// ---------------------------------------------------------------------------

func TestIntegration_PostToolUse_FiresAfterSuccess(t *testing.T) {
	t.Parallel()

	rec := &integrationHookRecorder{}
	hookConfig := hooks.HooksConfig{
		"PostToolUse": []hooks.HookMatcher{
			{Matcher: "my_tool", Hooks: []hooks.HookConfig{
				{Type: hooks.HookTypeCommand, Command: "post-hook"},
			}},
		},
	}
	hookSystem := hooks.NewHooks(hookConfig, rec)

	mt := &mockTool{
		name:    "my_tool",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "result"}, nil
		},
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "Use the tool", nil)
	for range eventCh {
	}
	result := <-resultCh

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// PostToolUse hook should have fired
	calls := rec.Calls()
	postFound := false
	for _, c := range calls {
		if c.event == "PostToolUse" {
			postFound = true
			if c.input.ToolName != "my_tool" {
				t.Errorf("PostToolUse tool_name = %q, want my_tool", c.input.ToolName)
			}
		}
	}
	if !postFound {
		t.Errorf("PostToolUse hook did not fire; events: %v", rec.Events())
	}
}

// ---------------------------------------------------------------------------
// PostToolUseFailure: fires after tool error
// ---------------------------------------------------------------------------

func TestIntegration_PostToolUseFailure_FiresOnError(t *testing.T) {
	t.Parallel()

	rec := &integrationHookRecorder{}
	hookConfig := hooks.HooksConfig{
		"PostToolUseFailure": []hooks.HookMatcher{
			{Matcher: "my_tool", Hooks: []hooks.HookConfig{
				{Type: hooks.HookTypeCommand, Command: "failure-hook"},
			}},
		},
	}
	hookSystem := hooks.NewHooks(hookConfig, rec)

	mt := &mockTool{
		name:    "my_tool",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
			return nil, errors.New("something went wrong")
		},
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "I see the error"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Tools:    []tool.Tool{mt},
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "Use the tool", nil)
	for range eventCh {
	}
	result := <-resultCh

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	calls := rec.Calls()
	failureFound := false
	for _, c := range calls {
		if c.event == "PostToolUseFailure" {
			failureFound = true
		}
	}
	if !failureFound {
		t.Errorf("PostToolUseFailure hook did not fire; events: %v", rec.Events())
	}
}

// ---------------------------------------------------------------------------
// Stop hook: blocking gives LLM another turn
// ---------------------------------------------------------------------------

func TestIntegration_Stop_BlockingGivesAnotherTurn(t *testing.T) {
	t.Parallel()

	rec := &integrationHookRecorder{
		results: []hooks.HookResult{
			{Outcome: hooks.HookOutcomeBlocking, Stderr: "keep working", HookName: "stop-hook"},
			// Second call (after rewake): allow stop
			{Outcome: hooks.HookOutcomeSuccess, HookName: "stop-hook"},
		},
	}
	hookConfig := hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{
			{Matcher: "", Hooks: []hooks.HookConfig{
				{Type: hooks.HookTypeCommand, Command: "stop-hook"},
			}},
		},
	}
	hookSystem := hooks.NewHooks(hookConfig, rec)

	mp := &mockProvider{}
	// First response: LLM says "I'm done" → stop hook blocks → LLM gets another turn
	mp.addResponse(textStreamEvents("test-model", "I'm done"), nil)
	// Second response: LLM does more work
	mp.addResponse(textStreamEvents("test-model", "OK, more work done"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "Do something", nil)
	for range eventCh {
	}
	result := <-resultCh

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %s", result.Terminal)
	}

	// Stop hook should have been called at least once
	calls := rec.Calls()
	stopCount := 0
	for _, c := range calls {
		if c.event == "Stop" {
			stopCount++
		}
	}
	if stopCount < 1 {
		t.Errorf("expected at least 1 Stop hook call, got %d; events: %v", stopCount, rec.Events())
	}

	// LLM should have been called twice (first turn blocked, second turn completed)
	if mp.index < 2 {
		t.Errorf("expected at least 2 LLM calls (stop hook rewake), got %d", mp.index)
	}
}

// ---------------------------------------------------------------------------
// No hooks: engine works normally
// ---------------------------------------------------------------------------

func TestIntegration_NoHooks_EngineWorks(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello!"), nil)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		// No Hooks
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, "Say hello", nil)
	for range eventCh {
	}
	result := <-resultCh

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Terminal != types.TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %s", result.Terminal)
	}
}

// ---------------------------------------------------------------------------
// SessionEnd: fires on engine Close()
// ---------------------------------------------------------------------------

func TestIntegration_SessionEnd_FiresOnClose(t *testing.T) {
	t.Parallel()

	rec := &integrationHookRecorder{}
	hookConfig := hooks.HooksConfig{
		"SessionEnd": []hooks.HookMatcher{
			{Matcher: "", Hooks: []hooks.HookConfig{
				{Type: hooks.HookTypeCommand, Command: "session-end-hook"},
			}},
		},
	}
	hookSystem := hooks.NewHooks(hookConfig, rec)

	eng := engine.New(&engine.Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		Hooks:    hookSystem,
	})

	eng.Close()

	calls := rec.Calls()
	found := false
	for _, c := range calls {
		if c.event == "SessionEnd" {
			found = true
		}
	}
	if !found {
		t.Errorf("SessionEnd hook did not fire on Close; events: %v", rec.Events())
	}
}

// mp is a minimal provider for tests that don't call Stream.
var mp = &mockProvider{}

func init() {
	mp.addResponse([]llm.StreamEvent{}, nil)
}
