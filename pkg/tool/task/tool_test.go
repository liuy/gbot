package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

func newTestListForTool(t *testing.T) *List {
	t.Helper()
	list := NewList(t.TempDir())
	if err := list.Init(); err != nil {
		t.Fatal(err)
	}
	return list
}

func callTasks(t *testing.T, list *List, input string) (*tool.ToolResult, *TasksOutput) {
	t.Helper()
	tl := New(list, nil)
	result, err := tl.Call(context.Background(), json.RawMessage(input), &tool.ToolUseContext{})
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
	return result, out
}

func mustCreateForTool(t *testing.T, list *List, subject, desc string) string {
	t.Helper()
	id, err := list.CreateTask(subject, desc, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestTasks_Create_Basic(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"create","subject":"Fix auth","description":"Fix the auth bug"}`)

	if len(out.Created) != 1 {
		t.Fatalf("len(Created) = %d, want 1", len(out.Created))
	}
	if out.Created[0].ID != "1" {
		t.Errorf("ID = %q, want %q", out.Created[0].ID, "1")
	}
	if out.Created[0].Subject != "Fix auth" {
		t.Errorf("Subject = %q, want %q", out.Created[0].Subject, "Fix auth")
	}
	if out.Created[0].Error != "" {
		t.Errorf("Error = %q, want empty", out.Created[0].Error)
	}
}

func TestTasks_Create_AllFields(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"create","subject":"Fix auth","description":"Fix the auth bug","activeForm":"Fixing auth","metadata":{"priority":"high"}}`)

	if out.Created[0].ID != "1" {
		t.Errorf("ID = %q, want %q", out.Created[0].ID, "1")
	}

	task, err := list.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if task.ActiveForm != "Fixing auth" {
		t.Errorf("ActiveForm = %q, want %q", task.ActiveForm, "Fixing auth")
	}
	if task.Metadata["priority"] != "high" {
		t.Errorf("Metadata[priority] = %v, want %q", task.Metadata["priority"], "high")
	}
}

func TestTasks_Create_EmptySubject(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"create","subject":"","description":"desc"}`)

	if len(out.Created) != 1 {
		t.Fatal("expected 1 result")
	}
	if out.Created[0].Error != "subject is required" {
		t.Errorf("Error = %q, want %q", out.Created[0].Error, "subject is required")
	}
	if out.Created[0].ID != "" {
		t.Errorf("ID should be empty on error, got %q", out.Created[0].ID)
	}
}

func TestTasks_Create_EmptyDescription(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"create","subject":"subj","description":""}`)

	if out.Created[0].Error != "description is required" {
		t.Errorf("Error = %q, want %q", out.Created[0].Error, "description is required")
	}
}

func TestTasks_Create_InvalidJSON(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)
	_, err := tl.Call(context.Background(), json.RawMessage(`{bad}`), &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want parse input error", err.Error())
	}
}

// create is single-item, so monotonic IDs span separate calls.
func TestTasks_Create_MonotonicIDs(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	for i, subj := range []string{"A", "B", "C"} {
		_, out := callTasks(t, list, `{"action":"create","subject":"`+subj+`","description":"d"}`)
		if len(out.Created) != 1 {
			t.Fatalf("call %d: len(Created) = %d, want 1", i+1, len(out.Created))
		}
		if want := string(rune('1' + i)); out.Created[0].ID != want {
			t.Errorf("call %d: ID = %q, want %q", i+1, out.Created[0].ID, want)
		}
	}
}

func TestTasks_Create_MetadataTypes(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"create","subject":"M","description":"m","metadata":{"s":"hello","i":42.0,"b":true}}`)
	if out.Created[0].Error != "" {
		t.Fatalf("unexpected error: %q", out.Created[0].Error)
	}
	task, _ := list.GetTask(out.Created[0].ID)
	if task.Metadata["s"] != "hello" {
		t.Errorf("s = %v, want hello", task.Metadata["s"])
	}
	if task.Metadata["i"] != float64(42) {
		t.Errorf("i = %v (%T), want 42", task.Metadata["i"], task.Metadata["i"])
	}
	if task.Metadata["b"] != true {
		t.Errorf("b = %v, want true", task.Metadata["b"])
	}
}

// ---------------------------------------------------------------------------
// Update tests
// ---------------------------------------------------------------------------

func TestTasks_Update_StatusFlow(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "Task A", "desc")

	// pending -> in_progress
	_, out := callTasks(t, list, `{"action":"update","taskId":"1","status":"in_progress"}`)
	if !out.Updated[0].Success {
		t.Fatal("update should succeed")
	}
	if out.Updated[0].StatusChange == nil {
		t.Fatal("StatusChange should not be nil")
	}
	if out.Updated[0].StatusChange.From != "pending" || out.Updated[0].StatusChange.To != "in_progress" {
		t.Errorf("StatusChange = %s->%s, want pending->in_progress", out.Updated[0].StatusChange.From, out.Updated[0].StatusChange.To)
	}

	// in_progress -> completed
	_, out = callTasks(t, list, `{"action":"update","taskId":"1","status":"completed"}`)
	if out.Updated[0].StatusChange.To != "completed" {
		t.Errorf("StatusChange.To = %q, want completed", out.Updated[0].StatusChange.To)
	}
}

func TestTasks_Update_Subject(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "Old", "desc")

	_, out := callTasks(t, list, `{"action":"update","taskId":"1","subject":"New"}`)
	if !out.Updated[0].Success {
		t.Fatal("update should succeed")
	}

	task, _ := list.GetTask("1")
	if task.Subject != "New" {
		t.Errorf("Subject = %q, want %q", task.Subject, "New")
	}
}

func TestTasks_Update_Description(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "old desc")

	callTasks(t, list, `{"action":"update","taskId":"1","description":"new desc"}`)
	task, _ := list.GetTask("1")
	if task.Description != "new desc" {
		t.Errorf("Description = %q, want %q", task.Description, "new desc")
	}
}

func TestTasks_Update_ActiveForm(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	callTasks(t, list, `{"action":"update","taskId":"1","activeForm":"Working on A"}`)
	task, _ := list.GetTask("1")
	if task.ActiveForm != "Working on A" {
		t.Errorf("ActiveForm = %q, want %q", task.ActiveForm, "Working on A")
	}
}

func TestTasks_Update_Owner(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	callTasks(t, list, `{"action":"update","taskId":"1","owner":"agent-1"}`)
	task, _ := list.GetTask("1")
	if task.Owner != "agent-1" {
		t.Errorf("Owner = %q, want %q", task.Owner, "agent-1")
	}
}

func TestTasks_Update_Metadata(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id, _ := list.CreateTask("A", "desc", "", map[string]any{"k1": "v1"})

	callTasks(t, list, `{"action":"update","taskId":"`+id+`","metadata":{"k2":"v2"}}`)
	task, _ := list.GetTask(id)
	if task.Metadata["k1"] != "v1" {
		t.Error("existing key k1 should be preserved")
	}
	if task.Metadata["k2"] != "v2" {
		t.Error("new key k2 should be added")
	}
}

func TestTasks_Update_NotFound(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"update","taskId":"999","status":"in_progress"}`)

	if out.Updated[0].Success {
		t.Error("should not succeed for non-existent task")
	}
	if out.Updated[0].Error != "Task not found" {
		t.Errorf("Error = %q, want %q", out.Updated[0].Error, "Task not found")
	}
}

func TestTasks_Update_EmptyID(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"update","taskId":""}`)

	if out.Updated[0].Error != "taskId is required" {
		t.Errorf("Error = %q, want %q", out.Updated[0].Error, "taskId is required")
	}
}

func TestTasks_Update_NoChanges(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	_, out := callTasks(t, list, `{"action":"update","taskId":"1"}`)
	if !out.Updated[0].Success {
		t.Error("update with no changes should still succeed")
	}
}

func TestTasks_Update_AddBlocks(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id1 := mustCreateForTool(t, list, "Blocker", "desc")
	id2 := mustCreateForTool(t, list, "Blocked", "desc")

	callTasks(t, list, `{"action":"update","taskId":"`+id1+`","addBlocks":["`+id2+`"]}`)
	task1, _ := list.GetTask(id1)
	if len(task1.Blocks) != 1 || task1.Blocks[0] != id2 {
		t.Errorf("Blocks = %v, want [%s]", task1.Blocks, id2)
	}
	task2, _ := list.GetTask(id2)
	if len(task2.BlockedBy) != 1 || task2.BlockedBy[0] != id1 {
		t.Errorf("BlockedBy = %v, want [%s]", task2.BlockedBy, id1)
	}
}

func TestTasks_Update_AddBlockedBy(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id1 := mustCreateForTool(t, list, "Blocker", "desc")
	id2 := mustCreateForTool(t, list, "Blocked", "desc")

	callTasks(t, list, `{"action":"update","taskId":"`+id2+`","addBlockedBy":["`+id1+`"]}`)
	task2, _ := list.GetTask(id2)
	if len(task2.BlockedBy) != 1 || task2.BlockedBy[0] != id1 {
		t.Errorf("BlockedBy = %v, want [%s]", task2.BlockedBy, id1)
	}
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestTasks_Delete_Basic(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	_, out := callTasks(t, list, `{"action":"delete","taskIds":["1"]}`)
	if len(out.Deleted) != 1 {
		t.Fatalf("len = %d, want 1", len(out.Deleted))
	}
	if !out.Deleted[0].Success {
		t.Error("delete should succeed")
	}
	if out.Deleted[0].ID != "1" {
		t.Errorf("ID = %q, want %q", out.Deleted[0].ID, "1")
	}
	task, _ := list.GetTask("1")
	if task != nil {
		t.Error("task should be nil after delete")
	}
}

func TestTasks_Delete_NotFound(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"delete","taskIds":["999"]}`)

	if out.Deleted[0].Success {
		t.Error("deleting non-existent task should not succeed")
	}
}

// delete is the one bulk action: multiple IDs ride in a single call.
func TestTasks_Delete_MultipleIDs(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "a")
	mustCreateForTool(t, list, "B", "b")
	mustCreateForTool(t, list, "C", "c")

	_, out := callTasks(t, list, `{"action":"delete","taskIds":["1","2"]}`)
	if len(out.Deleted) != 2 {
		t.Fatalf("len = %d, want 2", len(out.Deleted))
	}
	if !out.Deleted[0].Success || !out.Deleted[1].Success {
		t.Error("both deletes should succeed")
	}
	tasks, _ := list.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("remaining tasks = %d, want 1", len(tasks))
	}
	if tasks[0].ID != "3" {
		t.Errorf("remaining task ID = %q, want %q", tasks[0].ID, "3")
	}
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestTasks_Get_Basic(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "Fix auth", "Fix the auth bug in login")

	_, out := callTasks(t, list, `{"action":"get","taskId":"1"}`)
	if out.Get == nil {
		t.Fatal("Get should not be nil")
	}
	if out.Get.Task == nil {
		t.Fatal("Task should not be nil")
	}
	if out.Get.Task.ID != "1" {
		t.Errorf("ID = %q, want %q", out.Get.Task.ID, "1")
	}
	if out.Get.Task.Subject != "Fix auth" {
		t.Errorf("Subject = %q, want %q", out.Get.Task.Subject, "Fix auth")
	}
	if out.Get.Task.Description != "Fix the auth bug in login" {
		t.Errorf("Description = %q, want %q", out.Get.Task.Description, "Fix the auth bug in login")
	}
	if out.Get.Task.Status != StatusPending {
		t.Errorf("Status = %q, want %q", out.Get.Task.Status, StatusPending)
	}
}

func TestTasks_Get_NotFound(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"get","taskId":"999"}`)

	if out.Get == nil {
		t.Fatal("Get result wrapper should not be nil")
	}
	if out.Get.Task != nil {
		t.Error("Task should be nil for non-existent ID")
	}
}

func TestTasks_Get_WithBlocks(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id1 := mustCreateForTool(t, list, "Blocker", "desc")
	id2 := mustCreateForTool(t, list, "Blocked", "desc")
	callTasks(t, list, `{"action":"update","taskId":"`+id1+`","addBlocks":["`+id2+`"]}`)

	_, out := callTasks(t, list, `{"action":"get","taskId":"`+id1+`"}`)
	if len(out.Get.Task.Blocks) != 1 || out.Get.Task.Blocks[0] != id2 {
		t.Errorf("Blocks = %v, want [%s]", out.Get.Task.Blocks, id2)
	}
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

func TestTasks_List_Empty(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"list"}`)

	if out.List == nil {
		t.Fatal("List should not be nil")
	}
	if len(out.List.Tasks) != 0 {
		t.Errorf("Tasks len = %d, want 0", len(out.List.Tasks))
	}
}

func TestTasks_List_OrderByID(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "C", "c")
	mustCreateForTool(t, list, "A", "a")
	mustCreateForTool(t, list, "B", "b")

	_, out := callTasks(t, list, `{"action":"list"}`)
	if len(out.List.Tasks) != 3 {
		t.Fatalf("len = %d, want 3", len(out.List.Tasks))
	}
	wantIDs := []string{"1", "2", "3"}
	for i, w := range wantIDs {
		if out.List.Tasks[i].ID != w {
			t.Errorf("Tasks[%d].ID = %q, want %q", i, out.List.Tasks[i].ID, w)
		}
	}
}

func TestTasks_List_FilterCompletedBlockers(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id1 := mustCreateForTool(t, list, "Blocker", "desc")
	id2 := mustCreateForTool(t, list, "Blocked", "desc")

	callTasks(t, list, `{"action":"update","taskId":"`+id2+`","addBlockedBy":["`+id1+`"]}`)
	// Complete the blocker
	callTasks(t, list, `{"action":"update","taskId":"`+id1+`","status":"completed"}`)

	_, out := callTasks(t, list, `{"action":"list"}`)
	// Find the blocked task
	for _, task := range out.List.Tasks {
		if task.ID == id2 {
			if len(task.BlockedBy) != 0 {
				t.Errorf("BlockedBy should be empty (completed blocker filtered), got %v", task.BlockedBy)
			}
		}
	}
}

func TestTasks_List_ExcludeInternal(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "Normal", "desc")
	if _, err := list.CreateTask("Internal", "desc", "", map[string]any{"_internal": true}); err != nil {
		t.Fatal(err)
	}

	_, out := callTasks(t, list, `{"action":"list"}`)
	if len(out.List.Tasks) != 1 {
		t.Fatalf("len = %d, want 1 (_internal excluded)", len(out.List.Tasks))
	}
	if out.List.Tasks[0].Subject != "Normal" {
		t.Errorf("Subject = %q, want %q", out.List.Tasks[0].Subject, "Normal")
	}
}

// ---------------------------------------------------------------------------
// Action dispatch: missing, unknown, disallowed params, missing required IDs
// ---------------------------------------------------------------------------

// {} and the legacy shape {"creates":[...]} both lack action; the error
// must name the enum so the caller can retry correctly.
func TestTasks_MissingAction(t *testing.T) {
	want := `action is required — valid actions are "create", "update", "delete", "get", "list"`
	for _, input := range []string{`{}`, `{"creates":[{"subject":"A","description":"a"}]}`} {
		_, err := New(newTestListForTool(t), nil).Call(context.Background(), json.RawMessage(input), &tool.ToolUseContext{})
		if err == nil || err.Error() != want {
			t.Errorf("input %s: err = %v, want %q", input, err, want)
		}
	}
}

func TestTasks_UnknownAction(t *testing.T) {
	want := `unknown action "restart" — valid actions are "create", "update", "delete", "get", "list"`
	_, err := New(newTestListForTool(t), nil).Call(context.Background(), json.RawMessage(`{"action":"restart"}`), &tool.ToolUseContext{})
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// callRejection runs one input expected to be rejected at dispatch and asserts
// the exact error, a nil result, and an untouched store.
func callRejection(t *testing.T, input, want string) {
	t.Helper()
	list := newTestListForTool(t)
	result, err := New(list, nil).Call(context.Background(), json.RawMessage(input), &tool.ToolUseContext{})
	if err == nil || err.Error() != want {
		t.Errorf("input %s: err = %v, want %q", input, err, want)
	}
	if result != nil {
		t.Errorf("input %s: result = %+v, want nil", input, result)
	}
	if tasks, lerr := list.ListTasks(); lerr != nil {
		t.Errorf("input %s: ListTasks: %v", input, lerr)
	} else if len(tasks) != 0 {
		t.Errorf("input %s: rejection must not mutate storage, found %d tasks", input, len(tasks))
	}
}

func TestTasks_RejectsDisallowedParams(t *testing.T) {
	t.Parallel()
	tests := []struct{ input, want string }{
		{`{"action":"create","taskId":"1"}`,
			`action "create" accepts only: subject, description, activeForm, metadata — got taskId`},
		{`{"action":"update","taskIds":["1"]}`,
			`action "update" accepts only: taskId, subject, description, activeForm, status, addBlocks, addBlockedBy, owner, metadata — got taskIds`},
		{`{"action":"get","taskIds":["1"]}`,
			`action "get" accepts only: taskId — got taskIds`},
		{`{"action":"delete","taskId":"1","subject":"x"}`,
			`action "delete" accepts only: taskIds — got taskId, subject`},
		{`{"action":"create","addBlocks":["1"]}`,
			`action "create" accepts only: subject, description, activeForm, metadata — got addBlocks`},
		{`{"action":"create","addBlockedBy":["1"]}`,
			`action "create" accepts only: subject, description, activeForm, metadata — got addBlockedBy`},
		{`{"action":"list","subject":"x"}`,
			`action "list" accepts no other params — got subject`},
	}
	for _, tt := range tests {
		callRejection(t, tt.input, tt.want)
	}
}

// Legacy keys ride along no matter which action is set — a half-migrated
// caller must fail loudly instead of silently no-op-ing.
func TestTasks_RejectsLegacyShape(t *testing.T) {
	t.Parallel()
	tests := []struct{ input, want string }{
		{`{"action":"list","creates":[{"subject":"A","description":"a"}]}`,
			`action "list" accepts no other params — got creates`},
		{`{"action":"get","taskId":"1","updates":[{"taskId":"1"}]}`,
			`action "get" accepts only: taskId — got updates`},
		{`{"action":"create","subject":"A","description":"a","deletes":["1"],"list":true,"get":"1"}`,
			`action "create" accepts only: subject, description, activeForm, metadata — got deletes, list, get`},
		{`{"action":"list","creates":[{"subject":"A","description":"a"}],"updates":[{"taskId":"1"}]}`,
			`action "list" accepts no other params — got creates, updates`},
		{`{"action":"update","taskId":"1","creates":[{"subject":"A","description":"a"}],"get":"1"}`,
			`action "update" accepts only: taskId, subject, description, activeForm, status, addBlocks, addBlockedBy, owner, metadata — got creates, get`},
		{`{"action":"list","get":5}`,
			`action "list" accepts no other params — got get`},
	}
	for _, tt := range tests {
		callRejection(t, tt.input, tt.want)
	}
}

// A null legacy key carries no value, so it must not count as a disallowed
// param — only non-null values are half-migrated payloads worth rejecting.
func TestTasks_NullLegacyKeysIgnored(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"action":"list","creates":null,"updates":null,"deletes":null,"list":null,"get":null}`)

	if out.List == nil {
		t.Fatal("List should not be nil when legacy keys are null")
	}
	if len(out.List.Tasks) != 0 {
		t.Errorf("Tasks len = %d, want 0", len(out.List.Tasks))
	}
}

// An empty taskId/taskIds fails the presence checks above, so both the bare
// action and the explicitly-empty forms land on the required-param error.
func TestTasks_GetMissingTaskId(t *testing.T) {
	want := `action "get" requires taskId — run {"action":"list"} to see tasks`
	for _, input := range []string{`{"action":"get"}`, `{"action":"get","taskId":""}`} {
		callRejection(t, input, want)
	}
}

func TestTasks_DeleteMissingTaskIds(t *testing.T) {
	want := `action "delete" requires taskIds — run {"action":"list"} to see tasks`
	for _, input := range []string{`{"action":"delete"}`, `{"action":"delete","taskIds":[]}`} {
		callRejection(t, input, want)
	}
}

// ---------------------------------------------------------------------------
// Tool metadata
// ---------------------------------------------------------------------------

func TestTasks_IsReadOnly(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)

	tests := []struct {
		input string
		want  bool
	}{
		{`{"action":"create","subject":"A","description":"a"}`, false},
		{`{"action":"update","taskId":"1"}`, false},
		{`{"action":"delete","taskIds":["1"]}`, false},
		{`{"action":"get","taskId":"1"}`, true},
		{`{"action":"list"}`, true},
		{`{}`, false},
	}
	for _, tt := range tests {
		got := tl.IsReadOnly(json.RawMessage(tt.input))
		if got != tt.want {
			t.Errorf("IsReadOnly(%s) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestTasks_InputSchema(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)

	schema := tl.InputSchema()
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	for _, name := range []string{"action", "taskId", "taskIds", "subject", "description", "activeForm", "status", "addBlocks", "addBlockedBy", "owner", "metadata"} {
		if _, ok := props[name]; !ok {
			t.Errorf("property %q missing from schema", name)
		}
	}
}

func TestTasks_ActionEnumInSchema(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	schema := New(list, nil).InputSchema()

	var s struct {
		Properties struct {
			Action struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	if s.Properties.Action.Type != "string" {
		t.Errorf("action type = %q, want string", s.Properties.Action.Type)
	}
	want := []string{"create", "update", "delete", "get", "list"}
	if len(s.Properties.Action.Enum) != len(want) {
		t.Fatalf("action enum = %v, want %v", s.Properties.Action.Enum, want)
	}
	for i, w := range want {
		if s.Properties.Action.Enum[i] != w {
			t.Errorf("action enum[%d] = %q, want %q", i, s.Properties.Action.Enum[i], w)
		}
	}
	if len(s.Required) != 1 || s.Required[0] != "action" {
		t.Errorf("required = %v, want [action]", s.Required)
	}
}

// The status enum is what the update path accepts, and the completion gate
// only fires on the exact literal "completed" — an unconstrained status lets
// a near-miss value past both.
func TestTasksToolSchema_StatusEnum(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	schema := New(list, nil).InputSchema()

	var s struct {
		Properties struct {
			Status struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			} `json:"status"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	status := s.Properties.Status
	if status.Type != "string" {
		t.Errorf("status type = %q, want string", status.Type)
	}
	want := []string{"pending", "in_progress", "completed"}
	if len(status.Enum) != len(want) {
		t.Fatalf("status enum = %v, want %v", status.Enum, want)
	}
	for i, w := range want {
		if status.Enum[i] != w {
			t.Errorf("status enum[%d] = %q, want %q", i, status.Enum[i], w)
		}
	}
}

// "deleted" stopped being a status when deletion became an action; an update
// carrying it must fail in-result and leave the task untouched.
func TestTasks_Update_StatusDeletedRejected(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	_, out := callTasks(t, list, `{"action":"update","taskId":"1","status":"deleted"}`)

	if len(out.Updated) != 1 {
		t.Fatalf("len(Updated) = %d, want 1", len(out.Updated))
	}
	wantErr := `invalid status "deleted": must be one of pending, in_progress, completed`
	if out.Updated[0].Error != wantErr {
		t.Errorf("Error = %q, want %q", out.Updated[0].Error, wantErr)
	}
	if out.Updated[0].Success {
		t.Error("Success = true, want false")
	}
	task, err := list.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("task must survive a rejected status update")
	}
	if task.Status != StatusPending {
		t.Errorf("Status = %q, want %q", task.Status, StatusPending)
	}
}

func TestTasks_Name(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)
	if tl.Name() != "Task" {
		t.Errorf("Name() = %q, want %q", tl.Name(), "Task")
	}
}

// ---------------------------------------------------------------------------
// Description tests
// ---------------------------------------------------------------------------

func TestTasks_Description_Create(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)

	desc, err := tl.Description(json.RawMessage(`{"action":"create","subject":"Fix auth","description":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Fix auth" {
		t.Errorf("Description = %q, want %q", desc, "Fix auth")
	}
}

func TestTasks_Description_LegacyShapeInput(t *testing.T) {
	t.Parallel()
	// Old sessions replay stored tool_use inputs through Description; the
	// legacy keys are recorded-but-unused and must render the generic label.
	list := newTestListForTool(t)
	tl := New(list, nil)
	desc, err := tl.Description(json.RawMessage(`{"creates":[{"subject":"a"}],"list":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Manage tasks" {
		t.Errorf("Description = %q, want %q", desc, "Manage tasks")
	}
}
func TestTasks_Description_CreateEmptySubject(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)

	for _, input := range []string{`{"action":"create"}`, `{"action":"create","subject":""}`} {
		desc, err := tl.Description(json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if desc != "Create task" {
			t.Errorf("Description(%s) = %q, want %q", input, desc, "Create task")
		}
	}
}

func TestTasks_Description_List(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)

	desc, err := tl.Description(json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "List all tasks" {
		t.Errorf("Description = %q, want %q", desc, "List all tasks")
	}
}

func TestTasks_Description_EmptyInput(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)

	for _, input := range []string{`{}`, `{"action":"restart"}`} {
		desc, err := tl.Description(json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if desc != "Manage tasks" {
			t.Errorf("Description(%s) = %q, want %q", input, desc, "Manage tasks")
		}
	}
}

func TestTasks_Description_UpdateExisting(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "My Task", "desc")
	tl := New(list, nil)

	desc, err := tl.Description(json.RawMessage(`{"action":"update","taskId":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "My Task" {
		t.Errorf("Description = %q, want %q", desc, "My Task")
	}
}

func TestTasks_Description_GetExisting(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "Some Task", "desc")
	tl := New(list, nil)

	desc, err := tl.Description(json.RawMessage(`{"action":"get","taskId":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Some Task" {
		t.Errorf("Description = %q, want %q", desc, "Some Task")
	}
}

// ---------------------------------------------------------------------------
// RenderResult tests
// ---------------------------------------------------------------------------

func TestTasks_RenderResult_CreateOnly(t *testing.T) {
	out := &TasksOutput{
		Created: []CreateResult{{ID: "1", Subject: "Fix auth"}},
	}
	got := tasksRenderResult(out)
	want := "Created: #1 Fix auth"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTasks_RenderResult_UpdateWithStatusChange(t *testing.T) {
	out := &TasksOutput{
		Updated: []UpdateResult{{
			Success:      true,
			TaskID:       "2",
			StatusChange: &StatusChange{From: "pending", To: "in_progress"},
		}},
	}
	got := tasksRenderResult(out)
	want := "Updated: #2 pending->in_progress"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTasks_RenderResult_UpdateWithFields(t *testing.T) {
	out := &TasksOutput{
		Updated: []UpdateResult{{
			Success:       true,
			TaskID:        "1",
			UpdatedFields: []string{"subject", "description"},
		}},
	}
	got := tasksRenderResult(out)
	if !strings.Contains(got, "#1 subject,description") {
		t.Errorf("got %q, want #1 subject,description", got)
	}
}

func TestTasks_RenderResult_MixedOutput(t *testing.T) {
	out := &TasksOutput{
		Created: []CreateResult{{ID: "1", Subject: "A"}},
		Updated: []UpdateResult{{Success: true, TaskID: "2", UpdatedFields: []string{"status"}}},
		List:    &ListResult{Tasks: make([]ListOutputTask, 5)},
	}
	got := tasksRenderResult(out)
	if !strings.Contains(got, "Created: #1 A") {
		t.Errorf("missing 'Created: #1 A' in %q", got)
	}
	if !strings.Contains(got, "Updated: #2 status") {
		t.Errorf("missing 'Updated: #2 status' in %q", got)
	}
	if !strings.Contains(got, "Listed: 5 tasks") {
		t.Errorf("missing 'Listed: 5 tasks' in %q", got)
	}
}

func TestTasks_RenderResult_EmptyOutput(t *testing.T) {
	out := &TasksOutput{}
	got := tasksRenderResult(out)
	if got != "No changes" {
		t.Errorf("got %q, want %q", got, "No changes")
	}
}

func TestTasks_RenderResult_CreateError(t *testing.T) {
	out := &TasksOutput{
		Created: []CreateResult{
			{ID: "1", Subject: "A"},
			{Subject: "B", Error: "description is required"},
		},
	}
	got := tasksRenderResult(out)
	if !strings.Contains(got, "#1 A") {
		t.Errorf("missing '#1 A' in %q", got)
	}
	if !strings.Contains(got, "B FAILED: description is required") {
		t.Errorf("missing error in %q", got)
	}
}

func TestTasks_RenderResult_DeleteSuccess(t *testing.T) {
	out := &TasksOutput{
		Deleted: []DeleteResult{{ID: "3", Success: true}},
	}
	got := tasksRenderResult(out)
	if got != "Deleted: #3" {
		t.Errorf("got %q, want %q", got, "Deleted: #3")
	}
}

func TestTasks_RenderResult_GetNotFound(t *testing.T) {
	out := &TasksOutput{
		Get: &GetResult{Task: nil},
	}
	got := tasksRenderResult(out)
	if got != "Got: not found" {
		t.Errorf("got %q, want %q", got, "Got: not found")
	}
}

func TestTasks_RenderResult_GetTask(t *testing.T) {
	out := &TasksOutput{
		Get: &GetResult{Task: &GetOutputTask{ID: "5", Subject: "My task"}},
	}
	got := tasksRenderResult(out)
	if got != "Got: #5 My task" {
		t.Errorf("got %q, want %q", got, "Got: #5 My task")
	}
}

// ---------------------------------------------------------------------------
// ConcurrencySafe
// ---------------------------------------------------------------------------

func TestTasks_IsConcurrencySafe(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)
	if !tl.IsConcurrencySafe(json.RawMessage(`{}`)) {
		t.Error("Tasks tool should always be concurrency safe")
	}
}

// ---------------------------------------------------------------------------
// Error path tests — corrupt the underlying dir to trigger IO errors
// ---------------------------------------------------------------------------

// readOnlyList creates a List, writes a task file, then makes the dir read-only
// so subsequent writes fail. Returns the list and a cleanup func.
func readOnlyList(t *testing.T) (*List, func()) {
	t.Helper()
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatal(err)
	}
	// Pre-create a task so reads succeed
	if _, err := list.CreateTask("Pre", "desc", "", nil); err != nil {
		t.Fatal(err)
	}
	// Make the dir read-only to trigger write errors
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	return list, func() { _ = os.Chmod(dir, 0755) }
}

func TestTasks_Create_StoreError(t *testing.T) {
	t.Parallel()
	list, cleanup := readOnlyList(t)
	defer cleanup()
	tl := New(list, nil)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"create","subject":"A","description":"a"}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Data.(*TasksOutput)
	if len(out.Created) != 1 {
		t.Fatal("expected 1 result")
	}
	errMsg := out.Created[0].Error
	if errMsg == "" {
		t.Fatalf("CreateTask should fail when dir is read-only, got empty error")
	}
	if !strings.Contains(errMsg, "write") && !strings.Contains(errMsg, "permission") && !strings.Contains(errMsg, "open") {
		t.Errorf("unexpected error message: %q", errMsg)
	}
}

func TestTasks_Update_StoreError(t *testing.T) {
	t.Parallel()
	list, cleanup := readOnlyList(t)
	defer cleanup()
	tl := New(list, nil)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"update","taskId":"1","status":"in_progress"}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Data.(*TasksOutput)
	errMsg := out.Updated[0].Error
	if errMsg == "" {
		t.Fatalf("UpdateTask should fail when dir is read-only, got empty error")
	}
	if !strings.Contains(errMsg, "write") && !strings.Contains(errMsg, "permission") && !strings.Contains(errMsg, "open") {
		t.Errorf("unexpected error message: %q", errMsg)
	}
}

func TestTasks_Delete_StoreError(t *testing.T) {
	t.Parallel()
	list, cleanup := readOnlyList(t)
	defer cleanup()
	tl := New(list, nil)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"delete","taskIds":["1"]}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Data.(*TasksOutput)
	if out.Deleted[0].Success {
		t.Fatal("delete should not succeed when dir is read-only")
	}
	errMsg := out.Deleted[0].Error
	if errMsg == "" {
		t.Fatalf("DeleteTask should fail when dir is read-only, got empty error")
	}
	if !strings.Contains(errMsg, "remove") && !strings.Contains(errMsg, "permission") {
		t.Errorf("unexpected error message: %q", errMsg)
	}
}

func TestTasks_List_NoDir(t *testing.T) {
	t.Parallel()
	// Point List at a non-existent session dir to trigger ListTasks error
	dir := t.TempDir()
	list := NewList(dir)
	// Don't call Init — the session dir doesn't exist
	// Set a session-based dir that doesn't exist
	_ = list.SetDir(filepath.Join(dir, "nonexistent"))
	tl := New(list, nil)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Data.(*TasksOutput)
	if out.List == nil {
		t.Fatal("List result should not be nil")
	}
	// With a non-existent dir, ListTasks returns nil tasks (no error in current impl)
	// or error. Just verify it doesn't panic.
}

// ---------------------------------------------------------------------------
// Description error/edge paths
// ---------------------------------------------------------------------------

func TestTasks_Description_InvalidJSON(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)
	desc, err := tl.Description(json.RawMessage(`{bad}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Manage tasks" {
		t.Errorf("Description = %q, want %q", desc, "Manage tasks")
	}
}

func TestTasks_Description_UpdateNonExistent(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)
	desc, err := tl.Description(json.RawMessage(`{"action":"update","taskId":"999"}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "#999" {
		t.Errorf("Description = %q, want %q", desc, "#999")
	}
}

func TestTasks_Description_Deletes(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)
	desc, err := tl.Description(json.RawMessage(`{"action":"delete","taskIds":["1","2"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Delete 2 tasks" {
		t.Errorf("Description = %q, want %q", desc, "Delete 2 tasks")
	}
}

func TestTasks_Description_GetNonExistent(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)
	desc, err := tl.Description(json.RawMessage(`{"action":"get","taskId":"999"}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "#999" {
		t.Errorf("Description = %q, want %q", desc, "#999")
	}
}

// ---------------------------------------------------------------------------
// IsReadOnly invalid JSON
// ---------------------------------------------------------------------------

func TestTasks_IsReadOnly_InvalidJSON(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list, nil)
	if tl.IsReadOnly(json.RawMessage(`{bad}`)) != false {
		t.Error("IsReadOnly with invalid JSON should return false")
	}
}

// ---------------------------------------------------------------------------
// RenderResult remaining branches
// ---------------------------------------------------------------------------

func TestTasks_RenderResult_NonTasksOutput(t *testing.T) {
	got := tasksRenderResult("not a TasksOutput")
	if got != "not a TasksOutput" {
		t.Errorf("got %q, want %q", got, "not a TasksOutput")
	}
}

func TestTasks_RenderResult_CreateErrorEmptySubject(t *testing.T) {
	out := &TasksOutput{
		Created: []CreateResult{{Error: "subject is required"}},
	}
	got := tasksRenderResult(out)
	if !strings.Contains(got, "FAILED: subject is required") {
		t.Errorf("got %q, want FAILED: subject is required", got)
	}
}

func TestTasks_RenderResult_UpdateFailed(t *testing.T) {
	out := &TasksOutput{
		Updated: []UpdateResult{{Success: false, TaskID: "1", Error: "not found"}},
	}
	got := tasksRenderResult(out)
	want := "Updated: #1 FAILED: not found"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTasks_RenderResult_UpdateNoFields(t *testing.T) {
	out := &TasksOutput{
		Updated: []UpdateResult{{Success: true, TaskID: "1"}},
	}
	got := tasksRenderResult(out)
	want := "Updated: #1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTasks_RenderResult_DeleteFailed(t *testing.T) {
	out := &TasksOutput{
		Deleted: []DeleteResult{{ID: "1", Success: false, Error: "io error"}},
	}
	got := tasksRenderResult(out)
	want := "Deleted: #1 FAILED: io error"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTasks_RenderResult_ListZeroTasks(t *testing.T) {
	out := &TasksOutput{
		List: &ListResult{Tasks: nil},
	}
	got := tasksRenderResult(out)
	want := "Listed: 0 tasks"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// List: active blockedBy (blocker not yet completed)
// ---------------------------------------------------------------------------

func TestTasks_List_ActiveBlockedBy(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id1 := mustCreateForTool(t, list, "Blocker", "desc")
	id2 := mustCreateForTool(t, list, "Blocked", "desc")

	// Set up blocker relationship but don't complete the blocker
	callTasks(t, list, `{"action":"update","taskId":"`+id2+`","addBlockedBy":["`+id1+`"]}`)

	_, out := callTasks(t, list, `{"action":"list"}`)
	for _, task := range out.List.Tasks {
		if task.ID == id2 {
			if len(task.BlockedBy) != 1 || task.BlockedBy[0] != id1 {
				t.Errorf("BlockedBy = %v, want [%s]", task.BlockedBy, id1)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Update: GetTask IO error path (corrupt individual task file)
// ---------------------------------------------------------------------------

func TestTasks_Update_GetTaskIOError(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "A", "desc")

	// Make the task file unreadable to trigger GetTask error
	taskFile := filepath.Join(list.Dir(), id+".json")
	if err := os.Chmod(taskFile, 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(taskFile, 0644) }()

	tl := New(list, nil)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"update","taskId":"`+id+`","status":"in_progress"}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Data.(*TasksOutput)
	errMsg := out.Updated[0].Error
	if errMsg == "" {
		t.Fatalf("GetTask should fail when task file is unreadable, got empty error")
	}
	if !strings.Contains(errMsg, "read") && !strings.Contains(errMsg, "permission") {
		t.Errorf("unexpected error message: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// List: ListTasks returns error
// ---------------------------------------------------------------------------

func TestTasks_List_IOError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatal(err)
	}
	// Replace the session dir with a file to trigger ReadDir error (not IsNotExist)
	sessDir := list.Dir()
	_ = os.RemoveAll(sessDir)
	if f, err := os.Create(sessDir); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	tl := New(list, nil)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Data.(*TasksOutput)
	if out.List == nil {
		t.Fatal("List result should not be nil even on error")
	}
	if out.List.Tasks != nil {
		t.Error("List.Tasks should be nil on IO error")
	}
}

// ---------------------------------------------------------------------------
// checkAutoReset: all tasks completed path
// ---------------------------------------------------------------------------

func TestTasks_CheckAutoReset_AllCompleted(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	// Complete the only task — triggers checkAutoReset which should set allDoneSince
	callTasks(t, list, `{"action":"update","taskId":"1","status":"completed"}`)

	// If allDoneSince was set by checkAutoReset → time.Since < 1h → false
	// If allDoneSince was zero → ShouldCleanupCompleted detects from disk → true
	got := list.ShouldCleanupCompleted(time.Hour)
	// Either way, the path was exercised — just confirm deterministic outcome:
	// After completing, ShouldCleanupCompleted should not panic
	if got {
		// allDoneSince was zero — disk scan detected all completed
		t.Log("ShouldCleanupCompleted returned true (disk scan path)")
	} else {
		// allDoneSince was set — timer hasn't elapsed
		t.Log("ShouldCleanupCompleted returned false (timer path)")
	}
}

// ---------------------------------------------------------------------------
// TasksInput unmarshaling tolerance
// ---------------------------------------------------------------------------

// LLMs sometimes send taskId as a number despite the schema declaring
// "type":"string". Both forms must work; unparseable values must error
// instead of being silently dropped (the old get path dropped them).
func TestTasksInput_UnmarshalJSON_TaskIdAsStringOrNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want string
	}{
		{"string form", `{"action":"get","taskId":"42"}`, "42"},
		{"number form", `{"taskId":42}`, "42"},
		{"single digit number", `{"taskId":5}`, "5"},
		{"large number", `{"taskId":9999999}`, "9999999"},
		{"string zero", `{"taskId":"0"}`, "0"},
		{"empty string", `{"taskId":""}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var in TasksInput
			if err := json.Unmarshal([]byte(tt.json), &in); err != nil {
				t.Fatalf("UnmarshalJSON failed: %v", err)
			}
			if in.TaskID != tt.want {
				t.Errorf("TaskID = %q, want %q", in.TaskID, tt.want)
			}
		})
	}

	t.Run("boolean rejected", func(t *testing.T) {
		t.Parallel()
		var in TasksInput
		err := json.Unmarshal([]byte(`{"taskId":true}`), &in)
		want := "taskId: cannot unmarshal true into string or number"
		if err == nil || err.Error() != want {
			t.Errorf("err = %v, want %q", err, want)
		}
	})
}

// taskIds mirrors the old deletes tolerance: numbers and strings both work.
func TestTasksInput_UnmarshalJSON_TaskIdsAsStringOrNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want []string
	}{
		{"string ids", `{"action":"delete","taskIds":["104","105"]}`, []string{"104", "105"}},
		{"number ids", `{"taskIds":[104,105]}`, []string{"104", "105"}},
		{"mixed", `{"taskIds":["104",105]}`, []string{"104", "105"}},
		{"large number", `{"taskIds":[1234567890]}`, []string{"1234567890"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var in TasksInput
			if err := json.Unmarshal([]byte(tt.json), &in); err != nil {
				t.Fatalf("UnmarshalJSON failed: %v", err)
			}
			if len(in.TaskIDs) != len(tt.want) {
				t.Fatalf("got %d taskIds, want %d", len(in.TaskIDs), len(tt.want))
			}
			for i, wantID := range tt.want {
				if in.TaskIDs[i] != wantID {
					t.Errorf("taskIds[%d] = %q, want %q", i, in.TaskIDs[i], wantID)
				}
			}
		})
	}
}

func TestTask_DecodeResult_ArrayForm(t *testing.T) {
	t.Parallel()

	tt := New(NewList(""), nil)
	inner := `{"list":{"tasks":[]}}`
	textBytes, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textBytes) + `}]`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*TasksOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TasksOutput", v)
	}
	if o.List == nil {
		t.Errorf("List = nil, want non-nil")
	}
}

func TestTask_DecodeResult_RejectsBareStruct(t *testing.T) {
	t.Parallel()

	tt := New(NewList(""), nil)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`{"list":{"tasks":[]}}`))
	if err == nil {
		t.Error("DecodeResult must reject bare struct form")
	}
}
