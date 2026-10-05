package hooks

import (
	"context"
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// TaskCreated / TaskCompleted dispatch
// Source: hooks.ts:3745-3817 — executeTaskCreatedHooks / executeTaskCompletedHooks
// ---------------------------------------------------------------------------

func taskHookConfig(matcher string, commands ...string) HooksConfig {
	hooks := make([]HookConfig, 0, len(commands))
	for _, c := range commands {
		hooks = append(hooks, HookConfig{Type: HookTypeCommand, Command: c})
	}
	return HooksConfig{
		"TaskCreated":   []HookMatcher{{Matcher: matcher, Hooks: hooks}},
		"TaskCompleted": []HookMatcher{{Matcher: matcher, Hooks: hooks}},
	}
}

func TestTaskCreated_DispatchesTaskFields(t *testing.T) {
	t.Parallel()
	rec := &HookRecorder{}
	h := NewHooks(taskHookConfig("", "gate"), rec)

	results := h.TaskCreated(context.Background(), &HookInput{
		HookEventName:   string(HookTaskCreated),
		TaskID:          "7",
		TaskSubject:     "Ship it",
		TaskDescription: "release the thing",
	})

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("CallCount = %d, want 1", len(calls))
	}
	if calls[0].command != "gate" {
		t.Errorf("command = %q, want %q", calls[0].command, "gate")
	}
	got := calls[0].input
	if got.HookEventName != "TaskCreated" {
		t.Errorf("HookEventName = %q, want TaskCreated", got.HookEventName)
	}
	if got.TaskID != "7" {
		t.Errorf("TaskID = %q, want 7", got.TaskID)
	}
	if got.TaskSubject != "Ship it" {
		t.Errorf("TaskSubject = %q, want Ship it", got.TaskSubject)
	}
	if got.TaskDescription != "release the thing" {
		t.Errorf("TaskDescription = %q, want release the thing", got.TaskDescription)
	}
}

func TestTaskCompleted_DispatchesTaskFields(t *testing.T) {
	t.Parallel()
	rec := &HookRecorder{}
	h := NewHooks(taskHookConfig("", "gate"), rec)

	results := h.TaskCompleted(context.Background(), &HookInput{
		HookEventName:   string(HookTaskCompleted),
		TaskID:          "3",
		TaskSubject:     "Land it",
		TaskDescription: "merge the thing",
	})

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("CallCount = %d, want 1", len(calls))
	}
	if calls[0].input.HookEventName != "TaskCompleted" {
		t.Errorf("HookEventName = %q, want TaskCompleted", calls[0].input.HookEventName)
	}
	if calls[0].input.TaskID != "3" {
		t.Errorf("TaskID = %q, want 3", calls[0].input.TaskID)
	}
	if calls[0].input.TaskSubject != "Land it" {
		t.Errorf("TaskSubject = %q, want Land it", calls[0].input.TaskSubject)
	}
	if calls[0].input.TaskDescription != "merge the thing" {
		t.Errorf("TaskDescription = %q, want merge the thing", calls[0].input.TaskDescription)
	}
}

func TestTaskCreated_NoHooks(t *testing.T) {
	t.Parallel()
	h := NewHooks(make(HooksConfig), &HookRecorder{})
	results := h.TaskCreated(context.Background(), &HookInput{HookEventName: string(HookTaskCreated)})
	if results != nil {
		t.Errorf("expected nil results, got %d", len(results))
	}
}

func TestTaskCompleted_NoHooks(t *testing.T) {
	t.Parallel()
	h := NewHooks(make(HooksConfig), &HookRecorder{})
	results := h.TaskCompleted(context.Background(), &HookInput{HookEventName: string(HookTaskCompleted)})
	if results != nil {
		t.Errorf("expected nil results, got %d", len(results))
	}
}

// TS leaves matchQuery undefined for TaskCreated/TaskCompleted
// (hooks.ts:1649-1652), and with no query it skips matcher filtering entirely
// (hooks.ts:1681-1686): a matcher written for a tool name still runs.
func TestTaskCreated_NonEmptyMatcherStillRuns(t *testing.T) {
	t.Parallel()
	rec := &HookRecorder{}
	h := NewHooks(taskHookConfig("Bash", "gate"), rec)

	results := h.TaskCreated(context.Background(), &HookInput{HookEventName: string(HookTaskCreated)})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 — a non-empty matcher must not filter the event out", len(results))
	}
	if rec.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1", rec.CallCount())
	}
}

func TestTaskCompleted_PipeMatcherStillRuns(t *testing.T) {
	t.Parallel()
	rec := &HookRecorder{}
	h := NewHooks(taskHookConfig("Write|Edit", "gate"), rec)

	results := h.TaskCompleted(context.Background(), &HookInput{HookEventName: string(HookTaskCompleted)})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
}

func TestTaskCompleted_RegexMatcherStillRuns(t *testing.T) {
	t.Parallel()
	rec := &HookRecorder{}
	h := NewHooks(taskHookConfig("^Bash$", "gate"), rec)

	results := h.TaskCompleted(context.Background(), &HookInput{HookEventName: string(HookTaskCompleted)})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
}

// Guards the dispatch-side matcher bypass: it must apply to the two task
// events only, leaving tool events on TS's match-on-tool_name filtering.
func TestDispatch_MatcherFilterStillAppliesToToolEvents(t *testing.T) {
	t.Parallel()
	rec := &HookRecorder{}
	config := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{{Type: HookTypeCommand, Command: "gate"}}},
		},
	}
	h := NewHooks(config, rec)

	decision, results := h.PreToolUse(context.Background(), &HookInput{HookEventName: "PreToolUse", ToolName: "Read"})
	if decision != HookDecisionPassthrough {
		t.Errorf("decision = %v, want Passthrough for a non-matching tool name", decision)
	}
	if results != nil {
		t.Errorf("expected nil results for a non-matching tool name, got %d", len(results))
	}
	if rec.CallCount() != 0 {
		t.Errorf("CallCount = %d, want 0", rec.CallCount())
	}
}

func TestTaskCreated_BlockingShortCircuitsRemainingHooks(t *testing.T) {
	t.Parallel()
	rec := &HookRecorder{
		results: []HookResult{{Outcome: HookOutcomeBlocking, Stderr: "nope", HookName: "gate-1"}},
	}
	h := NewHooks(taskHookConfig("", "gate-1", "gate-2"), rec)

	results := h.TaskCreated(context.Background(), &HookInput{HookEventName: string(HookTaskCreated)})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if rec.CallCount() != 1 {
		t.Errorf("CallCount = %d, want 1 — dispatch must stop at the first blocking hook", rec.CallCount())
	}
}

// ---------------------------------------------------------------------------
// Hook feedback message formats
// Source: hooks.ts:1914-1929 — getTaskCreatedHookMessage / getTaskCompletedHookMessage
// ---------------------------------------------------------------------------

func TestTaskCreatedHookMessage_ExitTwoPath(t *testing.T) {
	t.Parallel()
	got := TaskCreatedHookMessage(HookResult{
		Outcome:  HookOutcomeBlocking,
		Stderr:   "no new tasks allowed",
		HookName: "gate",
	})
	want := "TaskCreated hook feedback:\n[gate]: no new tasks allowed"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTaskCreatedHookMessage_ExitTwoNoStderr(t *testing.T) {
	t.Parallel()
	got := TaskCreatedHookMessage(HookResult{Outcome: HookOutcomeBlocking, HookName: "gate"})
	want := "TaskCreated hook feedback:\n[gate]: No stderr output"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTaskCreatedHookMessage_DecisionBlockUsesReason(t *testing.T) {
	t.Parallel()
	got := TaskCreatedHookMessage(HookResult{
		Outcome:  HookOutcomeBlocking,
		HookName: "gate",
		Stderr:   "ignored because JSON wins",
		Output:   &HookOutput{Decision: "block", Reason: "subject is too vague"},
	})
	want := "TaskCreated hook feedback:\nsubject is too vague"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTaskCreatedHookMessage_DecisionBlockWithoutReason(t *testing.T) {
	t.Parallel()
	got := TaskCreatedHookMessage(HookResult{
		Outcome:  HookOutcomeBlocking,
		HookName: "gate",
		Output:   &HookOutput{Decision: "block"},
	})
	want := "TaskCreated hook feedback:\nBlocked by hook"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTaskCompletedHookMessage_ExitTwoPath(t *testing.T) {
	t.Parallel()
	got := TaskCompletedHookMessage(HookResult{
		Outcome:  HookOutcomeBlocking,
		Stderr:   "tests still failing",
		HookName: "gate",
	})
	want := "TaskCompleted hook feedback:\n[gate]: tests still failing"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTaskCompletedHookMessage_DecisionBlockUsesReason(t *testing.T) {
	t.Parallel()
	got := TaskCompletedHookMessage(HookResult{
		Outcome:  HookOutcomeBlocking,
		HookName: "gate",
		Output:   &HookOutput{Decision: "block", Reason: "not verified"},
	})
	want := "TaskCompleted hook feedback:\nnot verified"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// HookInput wire shape for the two events
// Source: coreSchemas.ts:601-625
// ---------------------------------------------------------------------------

// TestTaskHookInput_JSONShape locks the wire shape of a task hook input,
// including the base fields: session_id and cwd are always present (no
// omitempty), and a dispatch that carries them must send them populated.
// TestTaskHook_InputCarriesSessionIDAndCwd in pkg/tool/task proves the
// populated values come from the tool call context.
// Source: coreSchemas.ts:601-625
func TestTaskHookInput_JSONShape(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(HookInput{
		HookEventName:   string(HookTaskCreated),
		SessionID:       "sess-42",
		Cwd:             "/home/yliu/repos/gbot",
		TaskID:          "7",
		TaskSubject:     "Ship it",
		TaskDescription: "release the thing",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"hook_event_name":"TaskCreated","session_id":"sess-42","transcript_path":"","cwd":"/home/yliu/repos/gbot","task_id":"7","task_subject":"Ship it","task_description":"release the thing"}`
	if string(data) != want {
		t.Errorf("JSON = %s, want %s", data, want)
	}
}

func TestTaskHookInput_UnmarshalFromHookStdin(t *testing.T) {
	t.Parallel()
	var input HookInput
	err := json.Unmarshal([]byte(`{"hook_event_name":"TaskCompleted","task_id":"3","task_subject":"Land it","task_description":"merge"}`), &input)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.HookEventName != "TaskCompleted" {
		t.Errorf("HookEventName = %q, want TaskCompleted", input.HookEventName)
	}
	if input.TaskID != "3" || input.TaskSubject != "Land it" || input.TaskDescription != "merge" {
		t.Errorf("task fields = %q/%q/%q, want 3/Land it/merge", input.TaskID, input.TaskSubject, input.TaskDescription)
	}
}
