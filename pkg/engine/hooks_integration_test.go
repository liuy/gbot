package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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

func (r *integrationHookRecorder) ExecuteHook(ctx context.Context, command string, input *hooks.HookInput, timeout time.Duration, extraEnv []string) hooks.HookResult {
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
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			toolCalled = true
			return &tool.ToolResult{Data: "should not reach here"}, nil
		},
	}

	// First response: LLM calls the tool. Second response: LLM says "ok" after block.
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Tool was blocked"), nil)

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

	result := eng.QuerySync(ctx, "Use the tool", "")

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
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			toolCalled = true
			return &tool.ToolResult{Data: "success"}, nil
		},
	}

	// First: tool use. Second: text response.
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

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

	result := eng.QuerySync(ctx, "Use the tool", "")

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
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return &tool.ToolResult{Data: "result"}, nil
		},
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "Done"), nil)

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

	result := eng.QuerySync(ctx, "Use the tool", "")

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
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, errors.New("something went wrong")
		},
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-1", "my_tool", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "I see the error"), nil)

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

	result := eng.QuerySync(ctx, "Use the tool", "")

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
		t.Fatalf("unexpected error: %v", result.Error)
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
// Stop hook: the blocking feedback text handed to the model
// ---------------------------------------------------------------------------

// stopHookFeedback runs a query whose Stop hook blocks once, and returns the
// user message the engine injected as feedback together with the text of that
// same message in the API-bound copy. Exactly one such message must exist —
// the engine either injects the feedback or it doesn't.
//
// The API-bound text is a separate observable: injectTimestamp rewrites the
// copy it sends to the model, so assertions on the stored message alone cannot
// see what the model was actually given.
func stopHookFeedback(t *testing.T, blockResult hooks.HookResult) stopHookFeedbackResult {
	t.Helper()

	rec := &integrationHookRecorder{
		results: []hooks.HookResult{
			blockResult,
			{Outcome: hooks.HookOutcomeSuccess, HookName: blockResult.HookName},
		},
	}
	hookSystem := hooks.NewHooks(hooks.HooksConfig{
		"Stop": []hooks.HookMatcher{
			{Matcher: "", Hooks: []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: blockResult.HookName}}},
		},
	}, rec)

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "I'm done"), nil)
	mp.addResponse(textStreamEvents("test-model", "more work done"), nil)

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

	var feedbacks []types.Message
	var others []string
	for _, m := range result.Messages {
		if m.Role != types.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type != types.ContentTypeText {
				continue
			}
			if strings.HasPrefix(b.Text, "Stop hook feedback:") {
				feedbacks = append(feedbacks, m)
			} else {
				others = append(others, b.Text)
			}
		}
	}
	if len(feedbacks) != 1 {
		t.Fatalf("Stop feedback text blocks = %d, want exactly 1; other user text: %q", len(feedbacks), others)
	}
	msg := feedbacks[0]
	return stopHookFeedbackResult{
		message: msg,
		apiText: apiTextForMessage(t, eng, msg.ID),
	}
}

type stopHookFeedbackResult struct {
	message types.Message
	apiText string
}

// apiTextForMessage returns the first text block of msgID as marshalMessages
// hands it to the model — the copy injectTimestamp has already rewritten.
func apiTextForMessage(t *testing.T, eng *Engine, msgID string) (text string) {
	t.Helper()
	for _, m := range eng.marshalMessages() {
		if m.ID != msgID {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText {
				return b.Text
			}
		}
	}
	t.Fatalf("message %s has no text block in the API-bound copy", msgID)
	return
}

// assertStopHookFeedbackMeta checks the two halves of TS's isMeta marking
// (stopHooks.ts:258-261): the flag on the stored message, and the model-facing
// copy staying byte-identical to it instead of gaining a timestamp prefix.
func assertStopHookFeedbackMeta(t *testing.T, r stopHookFeedbackResult) {
	t.Helper()
	if r.message.Flags&types.FlagMeta == 0 {
		t.Errorf("feedback Flags = %d, want the FlagMeta bit %d set", r.message.Flags, types.FlagMeta)
	}
	stored := r.message.Content[0].Text
	if r.apiText != stored {
		t.Errorf("model-facing text = %q, want %q verbatim (injectTimestamp prefixed a message it must skip)", r.apiText, stored)
	}
}

// A hook that blocks through JSON output carries its reason in stdout and
// leaves stderr empty, so a formatter keyed on stderr discards the reason and
// hands the model a bare header.
func TestIntegration_Stop_BlockingJsonDecisionSurfacesReason(t *testing.T) {
	t.Parallel()

	r := stopHookFeedback(t, hooks.HookResult{
		Outcome:  hooks.HookOutcomeBlocking,
		HookName: "json-stop",
		Output:   &hooks.HookOutput{Decision: "block", Reason: "run the tests before finishing"},
	})
	want := "Stop hook feedback:\nrun the tests before finishing"
	if r.message.Content[0].Text != want {
		t.Errorf("Stop feedback = %q, want %q", r.message.Content[0].Text, want)
	}
	assertStopHookFeedbackMeta(t, r)
}

// The exit-2 path names the hook and quotes its stderr.
func TestIntegration_Stop_BlockingExit2QuotesHookAndStderr(t *testing.T) {
	t.Parallel()

	r := stopHookFeedback(t, hooks.HookResult{
		Outcome:  hooks.HookOutcomeBlocking,
		HookName: "stop-hook",
		Stderr:   "keep working",
	})
	want := "Stop hook feedback:\n[stop-hook]: keep working"
	if r.message.Content[0].Text != want {
		t.Errorf("Stop feedback = %q, want %q", r.message.Content[0].Text, want)
	}
	assertStopHookFeedbackMeta(t, r)
}

// The stored message and the model-facing copy diverge on their own: an
// unmarked user message gains a wall-clock prefix here, and no assertion on
// the stored text can catch it.
func TestIntegration_Stop_HookFeedbackReachesModelWithoutTimestamp(t *testing.T) {
	t.Parallel()

	r := stopHookFeedback(t, hooks.HookResult{
		Outcome:  hooks.HookOutcomeBlocking,
		HookName: "stop-hook",
		Stderr:   "keep working",
	})
	want := "Stop hook feedback:\n[stop-hook]: keep working"
	if r.apiText != want {
		t.Errorf("model-facing Stop feedback = %q, want %q with no prefix", r.apiText, want)
	}
}

// ---------------------------------------------------------------------------
// No hooks: engine works normally
// ---------------------------------------------------------------------------

func TestIntegration_NoHooks_EngineWorks(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "Hello!"), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
		// No Hooks
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "Say hello", "")

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
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

	eng := New(&Params{
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
