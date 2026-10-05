package task

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/tool"
)

// hookGate records every hook dispatch the Tasks tool makes and replays
// canned results, so the tests can assert both the model-visible outcome and
// the state left on disk.
type hookGate struct {
	mu      sync.Mutex
	results []hooks.HookResult
	calls   []hooks.HookInput
	index   int
	onRun   func()
}

func (g *hookGate) ExecuteHook(ctx context.Context, command string, input *hooks.HookInput, timeout time.Duration, extraEnv []string) hooks.HookResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, *input)
	if g.onRun != nil {
		g.onRun()
	}
	if g.index < len(g.results) {
		result := g.results[g.index]
		g.index++
		return result
	}
	return hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, HookName: command}
}

func (g *hookGate) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *hookGate) callsFor(event string) []hooks.HookInput {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []hooks.HookInput
	for _, c := range g.calls {
		if c.HookEventName == event {
			out = append(out, c)
		}
	}
	return out
}

func blockingGate(stderr string) *hookGate {
	return &hookGate{results: []hooks.HookResult{{
		Outcome:  hooks.HookOutcomeBlocking,
		Stderr:   stderr,
		HookName: "gate",
	}}}
}

func gateWithHooks(gate *hookGate, events ...string) *hooks.Hooks {
	cfg := hooks.HooksConfig{}
	for _, ev := range events {
		cfg[ev] = []hooks.HookMatcher{{
			Matcher: "",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "gate"}},
		}}
	}
	return hooks.NewHooks(cfg, gate)
}

func callWithHooks(t *testing.T, list *List, hk *hooks.Hooks, input string) *TasksOutput {
	t.Helper()
	result, err := New(list, hk).Call(context.Background(), json.RawMessage(input), &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	out, ok := result.Data.(*TasksOutput)
	if !ok {
		t.Fatalf("unexpected output type: %T", result.Data)
	}
	return out
}

func callWithTctx(t *testing.T, list *List, hk *hooks.Hooks, input string, tctx *tool.ToolUseContext) *TasksOutput {
	t.Helper()
	result, err := New(list, hk).Call(context.Background(), json.RawMessage(input), tctx)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	out, ok := result.Data.(*TasksOutput)
	if !ok {
		t.Fatalf("unexpected output type: %T", result.Data)
	}
	return out
}

// ---------------------------------------------------------------------------
// TaskCreated gate
// ---------------------------------------------------------------------------

func TestTaskHook_BlocksCreate_RollsBackTask(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := blockingGate("no new tasks allowed")
	hk := gateWithHooks(gate, "TaskCreated")

	out := callWithHooks(t, list, hk, `{"creates":[{"subject":"Fix auth","description":"Fix the auth bug"}]}`)

	if len(out.Created) != 1 {
		t.Fatalf("len(Created) = %d, want 1", len(out.Created))
	}
	if out.Created[0].ID != "" {
		t.Errorf("ID = %q, want empty — a blocked create must not report an id", out.Created[0].ID)
	}
	wantErr := "TaskCreated hook feedback:\n[gate]: no new tasks allowed"
	if out.Created[0].Error != wantErr {
		t.Errorf("Error = %q, want %q", out.Created[0].Error, wantErr)
	}

	task, err := list.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if task != nil {
		t.Errorf("task #1 still on disk after rollback: %+v", task)
	}

	calls := gate.callsFor("TaskCreated")
	if len(calls) != 1 {
		t.Fatalf("TaskCreated calls = %d, want 1", len(calls))
	}
	if calls[0].TaskID != "1" {
		t.Errorf("hook saw TaskID = %q, want 1 — the task must exist before the hook runs", calls[0].TaskID)
	}
	if calls[0].TaskSubject != "Fix auth" {
		t.Errorf("hook saw TaskSubject = %q, want Fix auth", calls[0].TaskSubject)
	}
	if calls[0].TaskDescription != "Fix the auth bug" {
		t.Errorf("hook saw TaskDescription = %q, want Fix the auth bug", calls[0].TaskDescription)
	}

	wantWire := `Failed to create task "Fix auth": TaskCreated hook feedback:` + "\n" + `[gate]: no new tasks allowed`
	if got := wireText(out); got != wantWire {
		t.Errorf("wire text = %q, want %q", got, wantWire)
	}
}

func TestTaskHook_NonEmptyMatcherStillBlocksCreate(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := blockingGate("no new tasks allowed")
	hk := hooks.NewHooks(hooks.HooksConfig{
		"TaskCreated": []hooks.HookMatcher{{
			Matcher: "Bash",
			Hooks:   []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "gate"}},
		}},
	}, gate)

	out := callWithHooks(t, list, hk, `{"creates":[{"subject":"Fix auth","description":"desc"}]}`)

	if out.Created[0].Error != "TaskCreated hook feedback:\n[gate]: no new tasks allowed" {
		t.Errorf("Error = %q, want the hook feedback — a tool-shaped matcher must not filter the event out", out.Created[0].Error)
	}
	task, err := list.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if task != nil {
		t.Errorf("task #1 still on disk after rollback: %+v", task)
	}
}

func TestTaskHook_NonBlockingHookKeepsTask(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := &hookGate{}
	hk := gateWithHooks(gate, "TaskCreated")

	out := callWithHooks(t, list, hk, `{"creates":[{"subject":"Fix auth","description":"desc"}]}`)

	if out.Created[0].ID != "1" {
		t.Errorf("ID = %q, want 1", out.Created[0].ID)
	}
	if out.Created[0].Error != "" {
		t.Errorf("Error = %q, want empty", out.Created[0].Error)
	}
	if gate.callCount() != 1 {
		t.Errorf("hook calls = %d, want 1", gate.callCount())
	}
	task, err := list.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("task #1 should exist when no hook blocks")
	}
}

func TestTaskHook_CreateGateStillRunsSiblingCreates(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := blockingGate("first one rejected")
	hk := gateWithHooks(gate, "TaskCreated")

	out := callWithHooks(t, list, hk, `{"creates":[{"subject":"A","description":"a"},{"subject":"B","description":"b"}]}`)

	if len(out.Created) != 2 {
		t.Fatalf("len(Created) = %d, want 2", len(out.Created))
	}
	if out.Created[0].Error != "TaskCreated hook feedback:\n[gate]: first one rejected" {
		t.Errorf("Created[0].Error = %q, want the hook feedback", out.Created[0].Error)
	}
	if out.Created[1].ID != "2" || out.Created[1].Error != "" {
		t.Errorf("Created[1] = %+v, want an unaffected successful create", out.Created[1])
	}
}

// The hook runs between the create and the rollback, so locking the task
// directory from inside the hook is what makes the rollback itself fail.
func TestTaskHook_BlocksCreate_RollbackFailureSurfacesDeleteError(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	t.Cleanup(func() {
		if err := os.Chmod(list.Dir(), 0o700); err != nil {
			t.Errorf("restore task dir mode: %v", err)
		}
	})

	gate := blockingGate("no new tasks allowed")
	gate.onRun = func() {
		if err := os.Chmod(list.Dir(), 0o500); err != nil {
			t.Errorf("lock task dir: %v", err)
		}
	}
	hk := gateWithHooks(gate, "TaskCreated")

	out := callWithHooks(t, list, hk, `{"creates":[{"subject":"Fix auth","description":"desc"}]}`)

	if len(out.Created) != 1 {
		t.Fatalf("len(Created) = %d, want 1", len(out.Created))
	}
	errText := out.Created[0].Error
	if !strings.HasPrefix(errText, "delete task 1: ") {
		t.Errorf("Error = %q, want it to report the failed rollback", errText)
	}
	if !strings.Contains(errText, "permission denied") {
		t.Errorf("Error = %q, want a permission-denied cause", errText)
	}
	if out.Created[0].ID != "" {
		t.Errorf("ID = %q, want empty", out.Created[0].ID)
	}

	task, err := list.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Error("task #1 should still exist when the rollback failed")
	}
}

// ---------------------------------------------------------------------------
// TaskCompleted gate
// ---------------------------------------------------------------------------

func TestTaskHook_BlocksCompletion_StatusUnchanged(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Fix auth", "Fix the auth bug")
	gate := blockingGate("tests still failing")
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","status":"completed"}]}`)

	if len(out.Updated) != 1 {
		t.Fatalf("len(Updated) = %d, want 1", len(out.Updated))
	}
	if out.Updated[0].Success {
		t.Error("Success = true, want false when a TaskCompleted hook blocks")
	}
	if out.Updated[0].TaskID != id {
		t.Errorf("TaskID = %q, want %q", out.Updated[0].TaskID, id)
	}
	if len(out.Updated[0].UpdatedFields) != 0 {
		t.Errorf("UpdatedFields = %v, want empty", out.Updated[0].UpdatedFields)
	}
	wantErr := "TaskCompleted hook feedback:\n[gate]: tests still failing"
	if out.Updated[0].Error != wantErr {
		t.Errorf("Error = %q, want %q", out.Updated[0].Error, wantErr)
	}
	if out.Updated[0].StatusChange != nil {
		t.Errorf("StatusChange = %+v, want nil", out.Updated[0].StatusChange)
	}

	task, err := list.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("task must survive a blocked completion")
	}
	if task.Status != StatusPending {
		t.Errorf("Status = %q, want %q", task.Status, StatusPending)
	}

	calls := gate.callsFor("TaskCompleted")
	if len(calls) != 1 {
		t.Fatalf("TaskCompleted calls = %d, want 1", len(calls))
	}
	if calls[0].TaskID != id || calls[0].TaskSubject != "Fix auth" {
		t.Errorf("hook saw %q/%q, want %q/Fix auth", calls[0].TaskID, calls[0].TaskSubject, id)
	}

	if got := wireText(out); got != wantErr {
		t.Errorf("wire text = %q, want %q", got, wantErr)
	}
}

// TS aborts the whole update call on a blocking hook, not just the status
// field: the return happens before updateTask (TaskUpdateTool.ts:255-273).
func TestTaskHook_BlocksCompletion_AbortsWholeUpdate(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Old subject", "old description")
	gate := blockingGate("not verified")
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","subject":"New subject","status":"completed"}]}`)

	if out.Updated[0].Success {
		t.Error("Success = true, want false")
	}
	task, err := list.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("task must survive an aborted update")
	}
	if task.Subject != "Old subject" {
		t.Errorf("Subject = %q, want the pre-update %q — the whole update is aborted", task.Subject, "Old subject")
	}
	if task.Status != StatusPending {
		t.Errorf("Status = %q, want %q", task.Status, StatusPending)
	}
}

func TestTaskHook_BlocksCompletion_DecisionBlockReason(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Fix auth", "desc")
	gate := &hookGate{results: []hooks.HookResult{{
		Outcome:  hooks.HookOutcomeBlocking,
		HookName: "gate",
		Output:   &hooks.HookOutput{Decision: "block", Reason: "no evidence it works"},
	}}}
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","status":"completed"}]}`)

	wantErr := "TaskCompleted hook feedback:\nno evidence it works"
	if out.Updated[0].Error != wantErr {
		t.Errorf("Error = %q, want %q", out.Updated[0].Error, wantErr)
	}
	task, err := list.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusPending {
		t.Errorf("Status = %q, want %q", task.Status, StatusPending)
	}
}

func TestTaskHook_DoesNotFireWhenStatusUnchanged(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Fix auth", "desc")
	done := StatusCompleted
	if _, _, err := list.UpdateTask(id, TaskUpdates{Status: &done}); err != nil {
		t.Fatal(err)
	}
	gate := blockingGate("should never run")
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","status":"completed"}]}`)

	if gate.callCount() != 0 {
		t.Errorf("hook calls = %d, want 0 — no transition means no TaskCompleted event", gate.callCount())
	}
	if !out.Updated[0].Success {
		t.Errorf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}
	if out.Updated[0].StatusChange != nil {
		t.Errorf("StatusChange = %+v, want nil", out.Updated[0].StatusChange)
	}
}

func TestTaskHook_DoesNotFireForNonCompletedStatus(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Fix auth", "desc")
	gate := blockingGate("should never run")
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","status":"in_progress"}]}`)

	if gate.callCount() != 0 {
		t.Errorf("hook calls = %d, want 0 — only a transition into completed fires the event", gate.callCount())
	}
	if !out.Updated[0].Success {
		t.Errorf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}
	task, err := list.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusInProgress {
		t.Errorf("Status = %q, want %q", task.Status, StatusInProgress)
	}
}

func TestTaskHook_DoesNotFireOnDelete(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Fix auth", "desc")
	gate := blockingGate("should never run")
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","status":"deleted"}]}`)

	if gate.callCount() != 0 {
		t.Errorf("hook calls = %d, want 0 — deleting is not completing", gate.callCount())
	}
	if !out.Updated[0].Success {
		t.Errorf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}
	task, err := list.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task != nil {
		t.Errorf("task still on disk after delete: %+v", task)
	}
}

func TestTaskHook_NonBlockingCompletionAppliesStatus(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Fix auth", "desc")
	gate := &hookGate{}
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","status":"completed"}]}`)

	if !out.Updated[0].Success {
		t.Fatalf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}
	if out.Updated[0].StatusChange == nil {
		t.Fatal("StatusChange = nil, want pending->completed")
	}
	if out.Updated[0].StatusChange.From != "pending" || out.Updated[0].StatusChange.To != "completed" {
		t.Errorf("StatusChange = %+v, want pending->completed", out.Updated[0].StatusChange)
	}
	if gate.callCount() != 1 {
		t.Errorf("hook calls = %d, want 1", gate.callCount())
	}
	task, err := list.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", task.Status, StatusCompleted)
	}
}

// ---------------------------------------------------------------------------
// No hooks configured — behavior must be identical to the unhooked tool
// ---------------------------------------------------------------------------

func TestTaskHook_NilHooksBehavesAsBefore(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)

	out := callWithHooks(t, list, nil, `{"creates":[{"subject":"Fix auth","description":"desc"}]}`)
	if out.Created[0].ID != "1" || out.Created[0].Error != "" {
		t.Fatalf("Created[0] = %+v, want a successful create", out.Created[0])
	}

	out = callWithHooks(t, list, nil, `{"updates":[{"taskId":"1","status":"completed"}]}`)
	if !out.Updated[0].Success {
		t.Fatalf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}
	if len(out.Updated[0].UpdatedFields) != 1 || out.Updated[0].UpdatedFields[0] != "status" {
		t.Errorf("UpdatedFields = %v, want [status]", out.Updated[0].UpdatedFields)
	}
	task, err := list.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", task.Status, StatusCompleted)
	}
}

func TestTaskHook_EmptyConfigDispatchesNothing(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := &hookGate{}
	hk := hooks.NewHooks(hooks.HooksConfig{}, gate)

	out := callWithHooks(t, list, hk, `{"creates":[{"subject":"Fix auth","description":"desc"}]}`)
	if out.Created[0].ID != "1" {
		t.Fatalf("Created[0] = %+v, want a successful create", out.Created[0])
	}

	out = callWithHooks(t, list, hk, `{"updates":[{"taskId":"1","status":"completed"}]}`)
	if !out.Updated[0].Success {
		t.Fatalf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}
	if gate.callCount() != 0 {
		t.Errorf("hook calls = %d, want 0 with an empty config", gate.callCount())
	}
}

// ---------------------------------------------------------------------------
// Base fields — the call context is the only carrier of session id and cwd
// ---------------------------------------------------------------------------

func TestTaskHook_InputCarriesSessionIDAndCwd(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := &hookGate{}
	hk := gateWithHooks(gate, "TaskCreated", "TaskCompleted")
	tctx := &tool.ToolUseContext{
		Options:            tool.ToolUseOptions{SessionID: "sess-42"},
		WorkingDir:         "/work/live",
		OriginalWorkingDir: "/work/original",
	}

	out := callWithTctx(t, list, hk, `{"creates":[{"subject":"Fix auth","description":"desc"}]}`, tctx)
	if out.Created[0].ID != "1" {
		t.Fatalf("Created[0] = %+v, want a successful create", out.Created[0])
	}
	out = callWithTctx(t, list, hk, `{"updates":[{"taskId":"1","status":"completed"}]}`, tctx)
	if !out.Updated[0].Success {
		t.Fatalf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}

	created := gate.callsFor("TaskCreated")
	completed := gate.callsFor("TaskCompleted")
	if len(created) != 1 || len(completed) != 1 {
		t.Fatalf("calls = %d TaskCreated / %d TaskCompleted, want 1/1", len(created), len(completed))
	}
	for _, c := range [][]hooks.HookInput{created, completed} {
		for _, in := range c {
			if in.SessionID != "sess-42" {
				t.Errorf("%s SessionID = %q, want sess-42", in.HookEventName, in.SessionID)
			}
			if in.Cwd != "/work/live" {
				t.Errorf("%s Cwd = %q, want /work/live", in.HookEventName, in.Cwd)
			}
		}
	}
}

func TestTaskHook_CwdFallsBackToOriginalWorkingDir(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := &hookGate{}
	hk := gateWithHooks(gate, "TaskCreated")
	tctx := &tool.ToolUseContext{
		Options:            tool.ToolUseOptions{SessionID: "sess-7"},
		OriginalWorkingDir: "/work/original",
	}

	out := callWithTctx(t, list, hk, `{"creates":[{"subject":"A","description":"a"}]}`, tctx)
	if out.Created[0].ID != "1" {
		t.Fatalf("Created[0] = %+v, want a successful create", out.Created[0])
	}
	calls := gate.callsFor("TaskCreated")
	if len(calls) != 1 {
		t.Fatalf("TaskCreated calls = %d, want 1", len(calls))
	}
	if calls[0].Cwd != "/work/original" {
		t.Errorf("Cwd = %q, want %q — the session-start dir is the fallback when WorkingDir is empty", calls[0].Cwd, "/work/original")
	}
	if calls[0].SessionID != "sess-7" {
		t.Errorf("SessionID = %q, want sess-7", calls[0].SessionID)
	}
}

func TestTaskHook_NilCallContextLeavesBaseFieldsEmpty(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := &hookGate{}
	hk := gateWithHooks(gate, "TaskCreated")

	out := callWithTctx(t, list, hk, `{"creates":[{"subject":"A","description":"a"}]}`, nil)
	if out.Created[0].ID != "1" {
		t.Fatalf("Created[0] = %+v, want a successful create with no call context", out.Created[0])
	}
	calls := gate.callsFor("TaskCreated")
	if len(calls) != 1 {
		t.Fatalf("TaskCreated calls = %d, want 1", len(calls))
	}
	if calls[0].SessionID != "" || calls[0].Cwd != "" {
		t.Errorf("SessionID/Cwd = %q/%q, want empty/empty when the caller passes no context", calls[0].SessionID, calls[0].Cwd)
	}
}

// ---------------------------------------------------------------------------
// Each gate is independent of the other event's configuration
// ---------------------------------------------------------------------------

func TestTaskHook_OnlyTaskCreatedHooksDoNotGateCompletion(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Fix auth", "desc")
	gate := blockingGate("should never run")
	hk := gateWithHooks(gate, "TaskCreated")

	out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","status":"completed"}]}`)

	if gate.callCount() != 0 {
		t.Fatalf("hook calls = %d, want 0 — a TaskCreated-only config must not gate completions", gate.callCount())
	}
	if !out.Updated[0].Success {
		t.Errorf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}
	task, err := list.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("task vanished on a completion the config cannot gate")
	}
	if task.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", task.Status, StatusCompleted)
	}
}

func TestTaskHook_OnlyTaskCompletedHooksDoNotGateCreate(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	gate := blockingGate("should never run")
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithHooks(t, list, hk, `{"creates":[{"subject":"Fix auth","description":"desc"}]}`)

	if gate.callCount() != 0 {
		t.Fatalf("hook calls = %d, want 0 — a TaskCompleted-only config must not gate creates", gate.callCount())
	}
	if out.Created[0].ID != "1" || out.Created[0].Error != "" {
		t.Fatalf("Created[0] = %+v, want a successful create", out.Created[0])
	}
	task, err := list.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("task #1 must survive a create the config cannot gate")
	}
	if task.Status != StatusPending {
		t.Errorf("Status = %q, want %q", task.Status, StatusPending)
	}
}

// The hook runs before updateTask, so an update that renames the task in the
// same call still reports the subject/description the completion is about.
func TestTaskHook_CompletedInputCarriesPreUpdateFields(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "Old subject", "old description")
	gate := &hookGate{}
	hk := gateWithHooks(gate, "TaskCompleted")

	out := callWithTctx(t, list, hk, `{"updates":[{"taskId":"`+id+`","subject":"New subject","description":"new description","status":"completed"}]}`, &tool.ToolUseContext{Options: tool.ToolUseOptions{SessionID: "sess-1"}})

	if !out.Updated[0].Success {
		t.Fatalf("Success = false, want true; Error = %q", out.Updated[0].Error)
	}
	calls := gate.callsFor("TaskCompleted")
	if len(calls) != 1 {
		t.Fatalf("TaskCompleted calls = %d, want 1", len(calls))
	}
	if calls[0].TaskSubject != "Old subject" {
		t.Errorf("TaskSubject = %q, want the pre-update %q", calls[0].TaskSubject, "Old subject")
	}
	if calls[0].TaskDescription != "old description" {
		t.Errorf("TaskDescription = %q, want the pre-update %q", calls[0].TaskDescription, "old description")
	}

	task, err := list.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("task vanished on an unblocked completion")
	}
	if task.Subject != "New subject" || task.Description != "new description" {
		t.Errorf("stored %q/%q, want New subject/new description — the hook sees the old values, the update writes the new ones", task.Subject, task.Description)
	}
	if task.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", task.Status, StatusCompleted)
	}
}

// ---------------------------------------------------------------------------
// Status validation — the completion gate must not be bypassable by a
// near-miss status value
// ---------------------------------------------------------------------------

func TestTaskUpdate_RejectsInvalidStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"Complete", "done", "completed ", "Cancelled"} {
		list := newTestListForTool(t)
		id := mustCreateForTool(t, list, "Fix auth", "desc")
		gate := blockingGate("should never run")
		hk := gateWithHooks(gate, "TaskCompleted")

		out := callWithHooks(t, list, hk, `{"updates":[{"taskId":"`+id+`","status":"`+status+`"}]}`)

		if len(out.Updated) != 1 {
			t.Fatalf("status %q: len(Updated) = %d, want 1", status, len(out.Updated))
		}
		wantErr := `invalid status "` + status + `": must be one of pending, in_progress, completed, deleted`
		if out.Updated[0].Error != wantErr {
			t.Errorf("status %q: Error = %q, want %q", status, out.Updated[0].Error, wantErr)
		}
		if out.Updated[0].Success {
			t.Errorf("status %q: Success = true, want false", status)
		}
		if out.Updated[0].TaskID != id {
			t.Errorf("status %q: TaskID = %q, want %q", status, out.Updated[0].TaskID, id)
		}
		if len(out.Updated[0].UpdatedFields) != 0 {
			t.Errorf("status %q: UpdatedFields = %v, want empty", status, out.Updated[0].UpdatedFields)
		}
		if gate.callCount() != 0 {
			t.Errorf("status %q: hook calls = %d, want 0 — an unrecognised status must not reach the completion gate", status, gate.callCount())
		}
		task, err := list.GetTask(id)
		if err != nil {
			t.Fatal(err)
		}
		if task == nil {
			t.Fatalf("status %q: task vanished on a rejected update", status)
		}
		if task.Status != StatusPending {
			t.Errorf("status %q: stored Status = %q, want %q — a bogus status must not be persisted", status, task.Status, StatusPending)
		}
	}
}
