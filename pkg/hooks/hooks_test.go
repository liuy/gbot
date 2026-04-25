package hooks

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// HookRecorder — test double for HookExecutor
// ---------------------------------------------------------------------------

// HookRecorder records hook invocations for test assertions.
type HookRecorder struct {
	mu      sync.Mutex
	calls   []recorderCall
	results []HookResult // results to return, one per call
	index   int
}

type recorderCall struct {
	command string
	input   *HookInput
	timeout time.Duration
}

func (r *HookRecorder) ExecuteHook(ctx context.Context, command string, input *HookInput, timeout time.Duration) HookResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recorderCall{command, input, timeout})
	if r.index < len(r.results) {
		result := r.results[r.index]
		r.index++
		return result
	}
	return HookResult{Outcome: HookOutcomeSuccess, HookName: command}
}

func (r *HookRecorder) Calls() []recorderCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorderCall(nil), r.calls...)
}

func (r *HookRecorder) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// ---------------------------------------------------------------------------
// NewHooks
// ---------------------------------------------------------------------------

func TestNewHooks_NilConfig(t *testing.T) {
	h := NewHooks(nil, &HookRecorder{})
	if h == nil {
		t.Fatal("expected non-nil Hooks")
	}
	if h.config == nil {
		t.Error("expected non-nil config when nil passed")
	}
}

func TestNewHooks_EmptyConfig(t *testing.T) {
	config := make(HooksConfig)
	h := NewHooks(config, &HookRecorder{})
	if h == nil {
		t.Fatal("expected non-nil Hooks")
	}
}

func TestNewHooks_NilExecutor(t *testing.T) {
	h := NewHooks(make(HooksConfig), nil)
	if h == nil {
		t.Fatal("expected non-nil Hooks even with nil executor")
	}
}

// ---------------------------------------------------------------------------
// PreToolUse — returns HookDecision + results
// Source: toolHooks.ts:435 — runPreToolUseHooks
// ---------------------------------------------------------------------------

func TestPreToolUse_NoHooks(t *testing.T) {
	h := NewHooks(make(HooksConfig), &HookRecorder{})
	decision, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if decision != HookDecisionPassthrough {
		t.Errorf("decision = %v, want Passthrough", decision)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestPreToolUse_Success(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "echo ok"}},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "echo ok"},
			}},
		},
	}
	h := NewHooks(config, rec)

	decision, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if decision != HookDecisionPassthrough {
		t.Errorf("decision = %v, want Passthrough for success", decision)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != HookOutcomeSuccess {
		t.Errorf("outcome = %v, want Success", results[0].Outcome)
	}
}

func TestPreToolUse_Block(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeBlocking, Stderr: "blocked", HookName: "exit 2"}},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "exit 2"},
			}},
		},
	}
	h := NewHooks(config, rec)

	decision, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if decision != HookDecisionBlock {
		t.Errorf("decision = %v, want Block", decision)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestPreToolUse_Approve(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{
			{Outcome: HookOutcomeSuccess, Output: &HookOutput{Decision: "approve"}, HookName: "approve-hook"},
		},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "approve-hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	decision, _ := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if decision != HookDecisionApprove {
		t.Errorf("decision = %v, want Approve", decision)
	}
}

func TestPreToolUse_BlockOverridesApprove(t *testing.T) {
	// First hook approves, second hook blocks → Block wins
	rec := &HookRecorder{
		results: []HookResult{
			{Outcome: HookOutcomeSuccess, Output: &HookOutput{Decision: "approve"}, HookName: "hook-1"},
			{Outcome: HookOutcomeBlocking, HookName: "hook-2"},
		},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "hook-1"},
				{Type: HookTypeCommand, Command: "hook-2"},
			}},
		},
	}
	h := NewHooks(config, rec)

	decision, _ := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if decision != HookDecisionBlock {
		t.Errorf("decision = %v, want Block (blocking overrides approve)", decision)
	}
}

func TestPreToolUse_MatcherFilter(t *testing.T) {
	// Hook for Bash, input tool is Read → no match
	rec := &HookRecorder{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "echo bash-only"},
			}},
		},
	}
	h := NewHooks(config, rec)

	decision, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Read"})
	if decision != HookDecisionPassthrough {
		t.Errorf("decision = %v, want Passthrough (no matcher match)", decision)
	}
	if results != nil {
		t.Errorf("expected nil results when matcher doesn't match, got %v", results)
	}
	if rec.CallCount() != 0 {
		t.Errorf("expected 0 calls, got %d", rec.CallCount())
	}
}

// ---------------------------------------------------------------------------
// PostToolUse
// ---------------------------------------------------------------------------

func TestPostToolUse_NoHooks(t *testing.T) {
	h := NewHooks(make(HooksConfig), &HookRecorder{})
	results := h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestPostToolUse_Executes(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "post-hook"}},
	}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "post-hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	results := h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != HookOutcomeSuccess {
		t.Errorf("outcome = %v, want Success", results[0].Outcome)
	}
}

// ---------------------------------------------------------------------------
// PostToolUseFailure
// ---------------------------------------------------------------------------

func TestPostToolUseFailure_NoHooks(t *testing.T) {
	h := NewHooks(make(HooksConfig), &HookRecorder{})
	results := h.PostToolUseFailure(context.Background(), &HookInput{ToolName: "Bash"})
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// Stop — returns *HookResult, non-nil if blocking
// Source: stopHooks.ts — handleStopHooks
// ---------------------------------------------------------------------------

func TestStop_NoHooks(t *testing.T) {
	h := NewHooks(make(HooksConfig), &HookRecorder{})
	result := h.Stop(context.Background(), &HookInput{})
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
}

func TestStop_Success(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "stop-hook"}},
	}
	config := HooksConfig{
		"Stop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "stop-hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	result := h.Stop(context.Background(), &HookInput{})
	if result != nil {
		t.Errorf("expected nil result for non-blocking stop, got %+v", result)
	}
}

func TestStop_Blocking(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeBlocking, Stderr: "continue working", HookName: "stop-block"}},
	}
	config := HooksConfig{
		"Stop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "stop-block"},
			}},
		},
	}
	h := NewHooks(config, rec)

	result := h.Stop(context.Background(), &HookInput{})
	if result == nil {
		t.Fatal("expected non-nil result for blocking stop")
	}
	if result.Outcome != HookOutcomeBlocking {
		t.Errorf("outcome = %v, want Blocking", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// SubagentStop
// ---------------------------------------------------------------------------

func TestSubagentStop_Blocking(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeBlocking, HookName: "sub-stop"}},
	}
	config := HooksConfig{
		"SubagentStop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "sub-stop"},
			}},
		},
	}
	h := NewHooks(config, rec)

	result := h.SubagentStop(context.Background(), &HookInput{})
	if result == nil {
		t.Fatal("expected non-nil result for blocking subagent stop")
	}
}

func TestSubagentStop_NonBlocking(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "sub-stop"}},
	}
	config := HooksConfig{
		"SubagentStop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "sub-stop"},
			}},
		},
	}
	h := NewHooks(config, rec)

	result := h.SubagentStop(context.Background(), &HookInput{})
	if result != nil {
		t.Errorf("expected nil for non-blocking subagent stop, got %+v", result)
	}
}

// ---------------------------------------------------------------------------
// Other event methods (smoke tests)
// ---------------------------------------------------------------------------

func TestStopFailure(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "failure-hook"}},
	}
	config := HooksConfig{
		"StopFailure": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "failure-hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	results := h.StopFailure(context.Background(), &HookInput{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestUserPromptSubmit(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "prompt-hook"}},
	}
	config := HooksConfig{
		"UserPromptSubmit": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "prompt-hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	results := h.UserPromptSubmit(context.Background(), &HookInput{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSessionStart(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "session-start"}},
	}
	config := HooksConfig{
		"SessionStart": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "session-start"},
			}},
		},
	}
	h := NewHooks(config, rec)

	results := h.SessionStart(context.Background(), &HookInput{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSessionEnd(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "session-end"}},
	}
	config := HooksConfig{
		"SessionEnd": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "session-end"},
			}},
		},
	}
	h := NewHooks(config, rec)

	results := h.SessionEnd(context.Background(), &HookInput{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestPreCompact(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "pre-compact"}},
	}
	config := HooksConfig{
		"PreCompact": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "pre-compact"},
			}},
		},
	}
	h := NewHooks(config, rec)

	results := h.PreCompact(context.Background(), &HookInput{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestPostCompact(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "post-compact"}},
	}
	config := HooksConfig{
		"PostCompact": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "post-compact"},
			}},
		},
	}
	h := NewHooks(config, rec)

	results := h.PostCompact(context.Background(), &HookInput{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Trust check — 修正 5
// Source: hooks.ts:286-296 — shouldSkipHookDueToTrust
// ---------------------------------------------------------------------------

func TestTrust_Untrusted_SkipsAll(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess}},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "should-not-run"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetTrust(false)

	decision, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if decision != HookDecisionPassthrough {
		t.Errorf("decision = %v, want Passthrough (untrusted)", decision)
	}
	if results != nil {
		t.Errorf("expected nil results (untrusted), got %v", results)
	}
	if rec.CallCount() != 0 {
		t.Errorf("expected 0 calls (untrusted), got %d", rec.CallCount())
	}
}

func TestTrust_Trusted_Executes(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "hook"}},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "hook"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetTrust(true)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result (trusted), got %d", len(results))
	}
}

func TestTrust_DefaultTrusted(t *testing.T) {
	// NewHooks defaults to trusted
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "hook"}},
	}
	config := HooksConfig{
		"Stop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	results := h.Stop(context.Background(), &HookInput{})
	// Default trusted → hook should execute (result is success, so Stop returns nil)
	if rec.CallCount() != 1 {
		t.Errorf("expected 1 call (default trusted), got %d", rec.CallCount())
	}
	if results != nil {
		t.Errorf("Stop() = %+v, want nil when no blocking result", results)
	}
}

// ---------------------------------------------------------------------------
// Once tracking — 修正 6
// Source: hooks.ts:1733+ — dedup key
// ---------------------------------------------------------------------------

func TestOnce_FiresOnlyOnce(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{
			{Outcome: HookOutcomeSuccess, HookName: "once-hook"},
			{Outcome: HookOutcomeSuccess, HookName: "once-hook"}, // second result (should not be used)
		},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "once-hook", Once: true},
			}},
		},
	}
	h := NewHooks(config, rec)

	// First call → fires
	_, results1 := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results1) != 1 {
		t.Fatalf("first call: expected 1 result, got %d", len(results1))
	}

	// Second call → skipped (once already fired)
	_, results2 := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if results2 != nil {
		t.Errorf("second call: expected nil results (once fired), got %v", results2)
	}
	if rec.CallCount() != 1 {
		t.Errorf("expected 1 call total (once), got %d", rec.CallCount())
	}
}

func TestOnce_DifferentMatchers_Independent(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{
			{Outcome: HookOutcomeSuccess, HookName: "hook-a"},
			{Outcome: HookOutcomeSuccess, HookName: "hook-b"},
		},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "hook-a", Once: true},
			}},
			{Matcher: "Read", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "hook-b", Once: true},
			}},
		},
	}
	h := NewHooks(config, rec)

	// Bash hook fires
	_, r1 := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(r1) != 1 {
		t.Fatalf("Bash: expected 1 result, got %d", len(r1))
	}

	// Read hook fires independently
	_, r2 := h.PreToolUse(context.Background(), &HookInput{ToolName: "Read"})
	if len(r2) != 1 {
		t.Fatalf("Read: expected 1 result, got %d", len(r2))
	}
}

func TestOnce_ReloadResets(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{
			{Outcome: HookOutcomeSuccess, HookName: "once-hook"},
			{Outcome: HookOutcomeSuccess, HookName: "once-hook"},
		},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "once-hook", Once: true},
			}},
		},
	}
	h := NewHooks(config, rec)

	// First call fires
	h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})

	// Reload config → resets once tracking
	h.ReloadConfig(config)

	// Should fire again after reload
	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("after reload: expected 1 result, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Block short-circuit
// ---------------------------------------------------------------------------

func TestBlockShortCircuit(t *testing.T) {
	// Multiple hooks: first blocks, second should not execute
	rec := &HookRecorder{
		results: []HookResult{
			{Outcome: HookOutcomeBlocking, HookName: "blocker"},
			{Outcome: HookOutcomeSuccess, HookName: "should-not-run"},
		},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "blocker"},
				{Type: HookTypeCommand, Command: "should-not-run"},
			}},
		},
	}
	h := NewHooks(config, rec)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result (short-circuited), got %d", len(results))
	}
	if rec.CallCount() != 1 {
		t.Errorf("expected 1 call (short-circuited after block), got %d", rec.CallCount())
	}
}

// ---------------------------------------------------------------------------
// Multiple matchers
// ---------------------------------------------------------------------------

func TestMultipleMatchers(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{
			{Outcome: HookOutcomeSuccess, HookName: "bash-hook"},
			{Outcome: HookOutcomeSuccess, HookName: "wildcard-hook"},
		},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "bash-hook"},
			}},
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "wildcard-hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 2 {
		t.Fatalf("expected 2 results (both matchers), got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// HookName tracking
// ---------------------------------------------------------------------------

func TestHookName_Command(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess}},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "my-custom-hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].HookName != "my-custom-hook" {
		t.Errorf("HookName = %q, want %q", results[0].HookName, "my-custom-hook")
	}
}

func TestHookName_Prompt(t *testing.T) {
	rec := &HookRecorder{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "cmd-hook"},
				{Type: HookTypePrompt, Prompt: "review this code", Command: "cmd-hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	// Command hook runs, prompt hook is skipped (no PromptExecutor set)
	// HookName for command hook should be the command
	if len(results) != 1 {
		t.Fatalf("expected 1 result (command only, prompt skipped), got %d", len(results))
	}
	if results[0].HookName != "cmd-hook" {
		t.Errorf("HookName = %q, want %q", results[0].HookName, "cmd-hook")
	}
}

// ---------------------------------------------------------------------------
// Prompt/Agent hook — skipped when no executor set
// ---------------------------------------------------------------------------

func TestPromptHook_SkippedWithoutExecutor(t *testing.T) {
	rec := &HookRecorder{}
	config := HooksConfig{
		"Stop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "review"},
			}},
		},
	}
	h := NewHooks(config, rec) // no PromptExecutor set

	result := h.Stop(context.Background(), &HookInput{})
	if result != nil {
		t.Errorf("expected nil (prompt hook skipped), got %+v", result)
	}
}

func TestAgentHook_SkippedWithoutExecutor(t *testing.T) {
	rec := &HookRecorder{}
	config := HooksConfig{
		"Stop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeAgent, Prompt: "analyze"},
			}},
		},
	}
	h := NewHooks(config, rec) // no AgentExecutor set

	result := h.Stop(context.Background(), &HookInput{})
	if result != nil {
		t.Errorf("expected nil (agent hook skipped), got %+v", result)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestConcurrentDispatch(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "hook"}},
	}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "hook"},
			}},
		},
	}
	h := NewHooks(config, rec)

	var wg sync.WaitGroup
	var callCount int64
	for range 10 {
		wg.Go(func() {
			_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
			if len(results) != 1 {
				t.Errorf("expected 1 result, got %d", len(results))
			}
			atomic.AddInt64(&callCount, 1)
		})
	}
	wg.Wait()

	if atomic.LoadInt64(&callCount) != 10 {
		t.Errorf("expected 10 concurrent calls, got %d", atomic.LoadInt64(&callCount))
	}
}

// ---------------------------------------------------------------------------
// onceKey — internal helper
// ---------------------------------------------------------------------------

func TestOnceKey_DifferentCommands(t *testing.T) {
	cfg1 := HookConfig{Type: HookTypeCommand, Command: "hook-a"}
	cfg2 := HookConfig{Type: HookTypeCommand, Command: "hook-b"}
	key1 := onceKey(HookPreToolUse, "Bash", cfg1)
	key2 := onceKey(HookPreToolUse, "Bash", cfg2)
	if key1 == key2 {
		t.Error("different commands should produce different keys")
	}
}

func TestOnceKey_DifferentEvents(t *testing.T) {
	cfg := HookConfig{Type: HookTypeCommand, Command: "hook"}
	key1 := onceKey(HookPreToolUse, "Bash", cfg)
	key2 := onceKey(HookPostToolUse, "Bash", cfg)
	if key1 == key2 {
		t.Error("different events should produce different keys")
	}
}

func TestOnceKey_DifferentMatchers(t *testing.T) {
	cfg := HookConfig{Type: HookTypeCommand, Command: "hook"}
	key1 := onceKey(HookPreToolUse, "Bash", cfg)
	key2 := onceKey(HookPreToolUse, "Read", cfg)
	if key1 == key2 {
		t.Error("different matchers should produce different keys")
	}
}

func TestOnceKey_WithIf(t *testing.T) {
	cfg1 := HookConfig{Type: HookTypeCommand, Command: "hook"}
	cfg2 := HookConfig{Type: HookTypeCommand, Command: "hook", If: "Bash(git *)"}
	key1 := onceKey(HookPreToolUse, "Bash", cfg1)
	key2 := onceKey(HookPreToolUse, "Bash", cfg2)
	if key1 == key2 {
		t.Error("different 'if' conditions should produce different keys")
	}
}

func TestOnceKey_WithPrompt(t *testing.T) {
	cfg1 := HookConfig{Type: HookTypePrompt, Prompt: "review"}
	cfg2 := HookConfig{Type: HookTypePrompt, Prompt: "check"}
	key1 := onceKey(HookPreToolUse, "Bash", cfg1)
	key2 := onceKey(HookPreToolUse, "Bash", cfg2)
	if key1 == key2 {
		t.Error("different prompts should produce different keys")
	}
}

// ---------------------------------------------------------------------------
// findBlockingResult — internal helper
// ---------------------------------------------------------------------------

func TestFindBlockingResult_None(t *testing.T) {
	results := []HookResult{
		{Outcome: HookOutcomeSuccess},
		{Outcome: HookOutcomeNonBlockingError},
	}
	if r := findBlockingResult(results); r != nil {
		t.Errorf("expected nil, got %+v", r)
	}
}

func TestFindBlockingResult_Found(t *testing.T) {
	results := []HookResult{
		{Outcome: HookOutcomeSuccess},
		{Outcome: HookOutcomeBlocking, Stderr: "blocked"},
	}
	r := findBlockingResult(results)
	if r == nil {
		t.Fatal("expected non-nil")
	}
	if r.Outcome != HookOutcomeBlocking {
		t.Errorf("outcome = %v, want Blocking", r.Outcome)
	}
}

func TestFindBlockingResult_Empty(t *testing.T) {
	if r := findBlockingResult(nil); r != nil {
		t.Errorf("expected nil for empty slice, got %+v", r)
	}
}

func TestFindBlockingResult_First(t *testing.T) {
	results := []HookResult{
		{Outcome: HookOutcomeBlocking, Stderr: "first"},
		{Outcome: HookOutcomeBlocking, Stderr: "second"},
	}
	r := findBlockingResult(results)
	if r == nil {
		t.Fatal("expected non-nil")
	}
	if r.Stderr != "first" {
		t.Errorf("expected first blocking result, got %q", r.Stderr)
	}
}

// ---------------------------------------------------------------------------
// PromptExecutor injection
// ---------------------------------------------------------------------------

type mockPromptExecutor struct {
	called atomic.Bool
	ok     bool
	reason string
	err    error
}

func (m *mockPromptExecutor) ExecutePromptHook(ctx context.Context, prompt string, model string, timeout time.Duration) (bool, string, error) {
	m.called.Store(true)
	return m.ok, m.reason, m.err
}

func TestSetPromptExecutor(t *testing.T) {
	rec := &HookRecorder{}
	pe := &mockPromptExecutor{ok: true}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "review this"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetPromptExecutor(pe)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if !pe.called.Load() {
		t.Error("expected PromptExecutor to be called")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != HookOutcomeSuccess {
		t.Errorf("outcome = %v, want Success", results[0].Outcome)
	}
}

func TestPromptExecutor_Blocking(t *testing.T) {
	rec := &HookRecorder{}
	pe := &mockPromptExecutor{ok: false, reason: "unsafe operation"}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "review this"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetPromptExecutor(pe)

	decision, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if decision != HookDecisionBlock {
		t.Errorf("decision = %v, want Block", decision)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != HookOutcomeBlocking {
		t.Errorf("outcome = %v, want Blocking", results[0].Outcome)
	}
}

func TestPromptExecutor_Error(t *testing.T) {
	rec := &HookRecorder{}
	pe := &mockPromptExecutor{err: context.DeadlineExceeded}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "review this"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetPromptExecutor(pe)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != HookOutcomeNonBlockingError {
		t.Errorf("outcome = %v, want NonBlockingError", results[0].Outcome)
	}
}

// ---------------------------------------------------------------------------
// AgentExecutor injection
// ---------------------------------------------------------------------------

type mockAgentExecutor struct {
	called atomic.Bool
	ok     bool
	reason string
	err    error
}

func (m *mockAgentExecutor) ExecuteAgentHook(ctx context.Context, prompt string, model string, tools []string, maxTurns int, timeout time.Duration) (bool, string, error) {
	m.called.Store(true)
	return m.ok, m.reason, m.err
}

func TestSetAgentExecutor(t *testing.T) {
	rec := &HookRecorder{}
	ae := &mockAgentExecutor{ok: true}
	config := HooksConfig{
		"Stop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeAgent, Prompt: "analyze"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetAgentExecutor(ae)

	result := h.Stop(context.Background(), &HookInput{})
	// Agent hook returns ok=true → success → Stop returns nil
	if !ae.called.Load() {
		t.Error("expected AgentExecutor to be called")
	}
	if result != nil {
		t.Errorf("expected nil for non-blocking agent stop, got %+v", result)
	}
}

func TestAgentExecutor_Blocking(t *testing.T) {
	rec := &HookRecorder{}
	ae := &mockAgentExecutor{ok: false, reason: "needs more work"}
	config := HooksConfig{
		"Stop": []HookMatcher{
			{Matcher: "", Hooks: []HookConfig{
				{Type: HookTypeAgent, Prompt: "analyze"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetAgentExecutor(ae)

	result := h.Stop(context.Background(), &HookInput{})
	if result == nil {
		t.Fatal("expected non-nil for blocking agent stop")
	}
	if result.Outcome != HookOutcomeBlocking {
		t.Errorf("outcome = %v, want Blocking", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// ReloadConfig
// ---------------------------------------------------------------------------

func TestReloadConfig(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "new-hook"}},
	}

	// Start with no hooks
	h := NewHooks(make(HooksConfig), rec)

	// Reload with new config
	newConfig := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "*", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "new-hook"},
			}},
		},
	}
	h.ReloadConfig(newConfig)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result after reload, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Default model for prompt/agent hooks
// ---------------------------------------------------------------------------

type modelTrackingPromptExecutor struct {
	model string
}

func (m *modelTrackingPromptExecutor) ExecutePromptHook(ctx context.Context, prompt string, model string, timeout time.Duration) (bool, string, error) {
	m.model = model
	return true, "", nil
}

func TestPromptHook_DefaultModel(t *testing.T) {
	rec := &HookRecorder{}
	pe := &modelTrackingPromptExecutor{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "review"}, // no Model specified
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetPromptExecutor(pe)

	h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if pe.model != "haiku" {
		t.Errorf("model = %q, want %q (default)", pe.model, "haiku")
	}
}

func TestPromptHook_CustomModel(t *testing.T) {
	rec := &HookRecorder{}
	pe := &modelTrackingPromptExecutor{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "review", Model: "opus"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetPromptExecutor(pe)

	h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if pe.model != "opus" {
		t.Errorf("model = %q, want %q", pe.model, "opus")
	}
}

// ---------------------------------------------------------------------------
// $ARGUMENTS substitution in prompt hooks
// ---------------------------------------------------------------------------

type argsTrackingPromptExecutor struct {
	prompt string
}

func (a *argsTrackingPromptExecutor) ExecutePromptHook(ctx context.Context, prompt string, model string, timeout time.Duration) (bool, string, error) {
	a.prompt = prompt
	return true, "", nil
}

func TestPromptHook_ArgumentsSubstitution(t *testing.T) {
	rec := &HookRecorder{}
	pe := &argsTrackingPromptExecutor{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "Review this: $ARGUMENTS"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetPromptExecutor(pe)

	input := &HookInput{
		ToolName:  "Bash",
		ToolInput: []byte(`{"command":"rm -rf /"}`),
	}
	h.PreToolUse(context.Background(), input)
	if pe.prompt != `Review this: {"command":"rm -rf /"}` {
		t.Errorf("prompt = %q, want $ARGUMENTS substituted", pe.prompt)
	}
}

// ---------------------------------------------------------------------------
// HookName for prompt hooks
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Async hooks — runAsyncHook, OnRewake
// Source: hooks.ts:995-1030 — async/asyncRewake path
// ---------------------------------------------------------------------------

func TestAsyncHook_Command_RunsInBackground(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess, HookName: "async-cmd"}},
	}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "async-cmd", Async: true},
			}},
		},
	}
	h := NewHooks(config, rec)

	// dispatch returns immediately with no results (async hooks don't block)
	results := h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if results != nil {
		t.Errorf("expected nil results (async doesn't block dispatch), got %v", results)
	}

	// Wait for background goroutine to execute
	deadline := time.After(2 * time.Second)
	for rec.CallCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for async hook to execute")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if rec.CallCount() != 1 {
		t.Errorf("expected 1 async call, got %d", rec.CallCount())
	}
}

func TestAsyncHookAsyncRewake_Blocking_TriggersCallback(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeBlocking, Stderr: "needs review"}},
	}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "rewake-hook", AsyncRewake: true},
			}},
		},
	}

	var rewakeReason string
	var rewakeCalled atomic.Bool
	h := NewHooks(config, rec)
	h.OnRewake = func(reason string) {
		rewakeReason = reason
		rewakeCalled.Store(true)
	}

	results := h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if results != nil {
		t.Errorf("expected nil results (async doesn't block), got %v", results)
	}

	// Wait for OnRewake callback
	deadline := time.After(2 * time.Second)
	for !rewakeCalled.Load() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for OnRewake callback")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if rewakeReason != "needs review" {
		t.Errorf("rewake reason = %q, want %q", rewakeReason, "needs review")
	}
}

func TestAsyncHookAsyncRewake_Blocking_NoStderr_UsesDefault(t *testing.T) {
	// Blocking result with empty Stderr → OnRewake gets "async hook blocked"
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeBlocking}},
	}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "rewake-hook", AsyncRewake: true},
			}},
		},
	}

	var rewakeReason string
	var rewakeCalled atomic.Bool
	h := NewHooks(config, rec)
	h.OnRewake = func(reason string) {
		rewakeReason = reason
		rewakeCalled.Store(true)
	}

	h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})

	deadline := time.After(2 * time.Second)
	for !rewakeCalled.Load() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for OnRewake")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if rewakeReason != "async hook blocked" {
		t.Errorf("rewake reason = %q, want %q", rewakeReason, "async hook blocked")
	}
}

func TestAsyncHook_NonBlocking_NoRewake(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeSuccess}},
	}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "async-ok", AsyncRewake: true},
			}},
		},
	}

	h := NewHooks(config, rec)
	h.OnRewake = func(reason string) {
		t.Errorf("OnRewake should not be called for non-blocking result, got: %s", reason)
	}

	h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})

	// Give goroutine time to run
	time.Sleep(100 * time.Millisecond)
	// OnRewake not called → test passes
}

func TestAsyncHook_NilOnRewake(t *testing.T) {
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeBlocking}},
	}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "async-nil", AsyncRewake: true},
			}},
		},
	}
	h := NewHooks(config, rec)
	// OnRewake is nil — should not panic

	h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	time.Sleep(100 * time.Millisecond)
	// No panic → pass
}

func TestAsyncHook_NilExecutor_SkipsExecution(t *testing.T) {
	// Async hook with nil executor for the hook type — should not panic
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "async-cmd", Async: true},
			}},
		},
	}
	h := NewHooks(config, nil) // nil executor

	// Should not panic
	results := h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
	time.Sleep(100 * time.Millisecond)
}

func TestAsyncHook_PromptType(t *testing.T) {
	rec := &HookRecorder{}
	pe := &mockPromptExecutor{ok: true}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "review code", Async: true},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetPromptExecutor(pe)

	h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})

	deadline := time.After(2 * time.Second)
	for !pe.called.Load() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for async prompt hook")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAsyncHook_AgentType(t *testing.T) {
	rec := &HookRecorder{}
	ae := &mockAgentExecutor{ok: true}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeAgent, Prompt: "analyze", Async: true},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetAgentExecutor(ae)

	h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})

	deadline := time.After(2 * time.Second)
	for !ae.called.Load() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for async agent hook")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func TestAsyncHook_HookName_Command(t *testing.T) {
	// Verify async hook actually executes in background
	var executed atomic.Bool
	rec := &recordingExecutor{fn: func(ctx context.Context, command string, input *HookInput, timeout time.Duration) HookResult {
		if command != "my-async-cmd" {
			t.Errorf("command = %q, want %q", command, "my-async-cmd")
		}
		executed.Store(true)
		return HookResult{Outcome: HookOutcomeSuccess}
	}}
	config := HooksConfig{
		"PostToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeCommand, Command: "my-async-cmd", Async: true},
			}},
		},
	}
	h := NewHooks(config, rec)

	h.PostToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	time.Sleep(200 * time.Millisecond)

	if !executed.Load() {
		t.Error("async hook did not execute")
	}
}

// recordingExecutor is a flexible test executor that records calls via a callback.
type recordingExecutor struct {
	fn func(ctx context.Context, command string, input *HookInput, timeout time.Duration) HookResult
}

func (r *recordingExecutor) ExecuteHook(ctx context.Context, command string, input *HookInput, timeout time.Duration) HookResult {
	return r.fn(ctx, command, input, timeout)
}

// ---------------------------------------------------------------------------
// String methods — HookOutcome.String, HookDecision.String
// ---------------------------------------------------------------------------

func TestHookOutcome_String(t *testing.T) {
	tests := []struct {
		outcome HookOutcome
		want    string
	}{
		{HookOutcomeSuccess, "success"},
		{HookOutcomeBlocking, "blocking"},
		{HookOutcomeNonBlockingError, "non_blocking_error"},
		{HookOutcomeTimeout, "timeout"},
		{HookOutcomeCancelled, "cancelled"},
		{HookOutcome(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.outcome.String()
		if got != tt.want {
			t.Errorf("HookOutcome(%d).String() = %q, want %q", tt.outcome, got, tt.want)
		}
	}
}

func TestHookDecision_String(t *testing.T) {
	tests := []struct {
		decision HookDecision
		want     string
	}{
		{HookDecisionPassthrough, "passthrough"},
		{HookDecisionApprove, "approve"},
		{HookDecisionBlock, "block"},
		{HookDecision(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.decision.String()
		if got != tt.want {
			t.Errorf("HookDecision(%d).String() = %q, want %q", tt.decision, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Agent hook $ARGUMENTS substitution
// ---------------------------------------------------------------------------

type argsTrackingAgentExecutor struct {
	prompt string
}

func (a *argsTrackingAgentExecutor) ExecuteAgentHook(ctx context.Context, prompt string, model string, tools []string, maxTurns int, timeout time.Duration) (bool, string, error) {
	a.prompt = prompt
	return true, "", nil
}

func TestAgentHook_ArgumentsSubstitution(t *testing.T) {
	rec := &HookRecorder{}
	ae := &argsTrackingAgentExecutor{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeAgent, Prompt: "Analyze: $ARGUMENTS"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetAgentExecutor(ae)

	input := &HookInput{
		ToolName:  "Bash",
		ToolInput: []byte(`{"command":"ls"}`),
	}
	h.PreToolUse(context.Background(), input)
	if ae.prompt != `Analyze: {"command":"ls"}` {
		t.Errorf("prompt = %q, want $ARGUMENTS substituted", ae.prompt)
	}
}

func TestAgentHook_Error(t *testing.T) {
	rec := &HookRecorder{}
	ae := &mockAgentExecutor{err: context.DeadlineExceeded}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeAgent, Prompt: "analyze"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetAgentExecutor(ae)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != HookOutcomeNonBlockingError {
		t.Errorf("outcome = %v, want NonBlockingError", results[0].Outcome)
	}
}

// ---------------------------------------------------------------------------
// Agent hook custom model
// ---------------------------------------------------------------------------

type modelTrackingAgentExecutor struct {
	model string
}

func (m *modelTrackingAgentExecutor) ExecuteAgentHook(ctx context.Context, prompt string, model string, tools []string, maxTurns int, timeout time.Duration) (bool, string, error) {
	m.model = model
	return true, "", nil
}

func TestAgentHook_DefaultModel(t *testing.T) {
	rec := &HookRecorder{}
	ae := &modelTrackingAgentExecutor{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeAgent, Prompt: "analyze"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetAgentExecutor(ae)

	h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if ae.model != "haiku" {
		t.Errorf("model = %q, want %q (default)", ae.model, "haiku")
	}
}

func TestAgentHook_CustomModel(t *testing.T) {
	rec := &HookRecorder{}
	ae := &modelTrackingAgentExecutor{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypeAgent, Prompt: "analyze", Model: "sonnet"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetAgentExecutor(ae)

	h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if ae.model != "sonnet" {
		t.Errorf("model = %q, want %q", ae.model, "sonnet")
	}
}

func TestHookName_PromptType(t *testing.T) {
	rec := &HookRecorder{}
	pe := &mockPromptExecutor{ok: true}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{
				{Type: HookTypePrompt, Prompt: "review this code"},
			}},
		},
	}
	h := NewHooks(config, rec)
	h.SetPromptExecutor(pe)

	_, results := h.PreToolUse(context.Background(), &HookInput{ToolName: "Bash"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// HookName should be set to the prompt text
	if results[0].HookName != "review this code" {
		t.Errorf("HookName = %q, want %q", results[0].HookName, "review this code")
	}
}

func TestOnceKey_WithModel(t *testing.T) {
	cfg1 := HookConfig{Type: HookTypePrompt, Prompt: "review"}
	cfg2 := HookConfig{Type: HookTypePrompt, Prompt: "review", Model: "opus"}
	key1 := onceKey(HookPreToolUse, "Bash", cfg1)
	key2 := onceKey(HookPreToolUse, "Bash", cfg2)
	if key1 == key2 {
		t.Error("different models should produce different keys")
	}
}
