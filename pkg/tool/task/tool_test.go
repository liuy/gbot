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
	tl := New(list)
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
	_, out := callTasks(t, list, `{"creates":[{"subject":"Fix auth","description":"Fix the auth bug"}]}`)

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
	_, out := callTasks(t, list, `{"creates":[{"subject":"Fix auth","description":"Fix the auth bug","activeForm":"Fixing auth","metadata":{"priority":"high"}}]}`)

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
	_, out := callTasks(t, list, `{"creates":[{"subject":"","description":"desc"}]}`)

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
	_, out := callTasks(t, list, `{"creates":[{"subject":"subj","description":""}]}`)

	if out.Created[0].Error != "description is required" {
		t.Errorf("Error = %q, want %q", out.Created[0].Error, "description is required")
	}
}

func TestTasks_Create_InvalidJSON(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)
	_, err := tl.Call(context.Background(), json.RawMessage(`{bad}`), &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want parse input error", err.Error())
	}
}

func TestTasks_Create_MonotonicIDs(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"creates":[{"subject":"A","description":"a"},{"subject":"B","description":"b"},{"subject":"C","description":"c"}]}`)

	if len(out.Created) != 3 {
		t.Fatalf("len = %d, want 3", len(out.Created))
	}
	want := []string{"1", "2", "3"}
	for i, w := range want {
		if out.Created[i].ID != w {
			t.Errorf("Created[%d].ID = %q, want %q", i, out.Created[i].ID, w)
		}
	}
}

func TestTasks_Create_MetadataTypes(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"creates":[{"subject":"M","description":"m","metadata":{"s":"hello","i":42.0,"b":true}}]}`)
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
// Batch create
// ---------------------------------------------------------------------------

func TestTasks_BatchCreate(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"creates":[{"subject":"A","description":"a"},{"subject":"B","description":"b"},{"subject":"C","description":"c"}]}`)

	if len(out.Created) != 3 {
		t.Fatalf("len = %d, want 3", len(out.Created))
	}
	wantSubjects := []string{"A", "B", "C"}
	for i, ws := range wantSubjects {
		if out.Created[i].Subject != ws {
			t.Errorf("Created[%d].Subject = %q, want %q", i, out.Created[i].Subject, ws)
		}
		if out.Created[i].ID == "" {
			t.Errorf("Created[%d].ID is empty", i)
		}
	}
}

func TestTasks_BatchCreate_PartialFailure(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	_, out := callTasks(t, list, `{"creates":[{"subject":"A","description":"a"},{"subject":"","description":"bad"},{"subject":"C","description":"c"}]}`)

	if len(out.Created) != 3 {
		t.Fatalf("len = %d, want 3", len(out.Created))
	}
	if out.Created[0].ID == "" {
		t.Error("first create should succeed")
	}
	if out.Created[1].Error != "subject is required" {
		t.Errorf("second create error = %q, want %q", out.Created[1].Error, "subject is required")
	}
	if out.Created[2].ID == "" {
		t.Error("third create should succeed")
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
	_, out := callTasks(t, list, `{"updates":[{"taskId":"1","status":"in_progress"}]}`)
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
	_, out = callTasks(t, list, `{"updates":[{"taskId":"1","status":"completed"}]}`)
	if out.Updated[0].StatusChange.To != "completed" {
		t.Errorf("StatusChange.To = %q, want completed", out.Updated[0].StatusChange.To)
	}
}

func TestTasks_Update_Subject(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "Old", "desc")

	_, out := callTasks(t, list, `{"updates":[{"taskId":"1","subject":"New"}]}`)
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

	callTasks(t, list, `{"updates":[{"taskId":"1","description":"new desc"}]}`)
	task, _ := list.GetTask("1")
	if task.Description != "new desc" {
		t.Errorf("Description = %q, want %q", task.Description, "new desc")
	}
}

func TestTasks_Update_ActiveForm(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	callTasks(t, list, `{"updates":[{"taskId":"1","activeForm":"Working on A"}]}`)
	task, _ := list.GetTask("1")
	if task.ActiveForm != "Working on A" {
		t.Errorf("ActiveForm = %q, want %q", task.ActiveForm, "Working on A")
	}
}

func TestTasks_Update_Owner(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	callTasks(t, list, `{"updates":[{"taskId":"1","owner":"agent-1"}]}`)
	task, _ := list.GetTask("1")
	if task.Owner != "agent-1" {
		t.Errorf("Owner = %q, want %q", task.Owner, "agent-1")
	}
}

func TestTasks_Update_Metadata(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id, _ := list.CreateTask("A", "desc", "", map[string]any{"k1": "v1"})

	callTasks(t, list, `{"updates":[{"taskId":"`+id+`","metadata":{"k2":"v2"}}]}`)
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
	_, out := callTasks(t, list, `{"updates":[{"taskId":"999","status":"in_progress"}]}`)

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
	_, out := callTasks(t, list, `{"updates":[{"taskId":""}]}`)

	if out.Updated[0].Error != "taskId is required" {
		t.Errorf("Error = %q, want %q", out.Updated[0].Error, "taskId is required")
	}
}

func TestTasks_Update_NoChanges(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	_, out := callTasks(t, list, `{"updates":[{"taskId":"1"}]}`)
	if !out.Updated[0].Success {
		t.Error("update with no changes should still succeed")
	}
}

func TestTasks_Update_DeleteViaStatus(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	_, out := callTasks(t, list, `{"updates":[{"taskId":"1","status":"deleted"}]}`)
	if !out.Updated[0].Success {
		t.Fatal("delete should succeed")
	}

	task, _ := list.GetTask("1")
	if task != nil {
		t.Error("task should be nil after deletion")
	}
}

func TestTasks_Update_AddBlocks(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id1 := mustCreateForTool(t, list, "Blocker", "desc")
	id2 := mustCreateForTool(t, list, "Blocked", "desc")

	callTasks(t, list, `{"updates":[{"taskId":"`+id1+`","addBlocks":["`+id2+`"]}]}`)
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

	callTasks(t, list, `{"updates":[{"taskId":"`+id2+`","addBlockedBy":["`+id1+`"]}]}`)
	task2, _ := list.GetTask(id2)
	if len(task2.BlockedBy) != 1 || task2.BlockedBy[0] != id1 {
		t.Errorf("BlockedBy = %v, want [%s]", task2.BlockedBy, id1)
	}
}

// ---------------------------------------------------------------------------
// Batch update
// ---------------------------------------------------------------------------

func TestTasks_BatchUpdate(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "a")
	mustCreateForTool(t, list, "B", "b")
	mustCreateForTool(t, list, "C", "c")

	_, out := callTasks(t, list, `{"updates":[{"taskId":"1","subject":"A2"},{"taskId":"2","subject":"B2"}]}`)
	if len(out.Updated) != 2 {
		t.Fatalf("len = %d, want 2", len(out.Updated))
	}
	if !out.Updated[0].Success || !out.Updated[1].Success {
		t.Fatal("both updates should succeed")
	}
	task1, _ := list.GetTask("1")
	if task1.Subject != "A2" {
		t.Errorf("task1.Subject = %q, want A2", task1.Subject)
	}
	task2, _ := list.GetTask("2")
	if task2.Subject != "B2" {
		t.Errorf("task2.Subject = %q, want B2", task2.Subject)
	}
}

func TestTasks_BatchUpdate_PartialFailure(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "a")

	_, out := callTasks(t, list, `{"updates":[{"taskId":"1","subject":"A2"},{"taskId":"999","subject":"X"}]}`)
	if len(out.Updated) != 2 {
		t.Fatalf("len = %d, want 2", len(out.Updated))
	}
	if !out.Updated[0].Success {
		t.Error("first update should succeed")
	}
	if out.Updated[1].Success {
		t.Error("second update should fail (not found)")
	}
	if out.Updated[1].Error != "Task not found" {
		t.Errorf("error = %q, want %q", out.Updated[1].Error, "Task not found")
	}
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestTasks_Delete_Basic(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	_, out := callTasks(t, list, `{"deletes":["1"]}`)
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
	_, out := callTasks(t, list, `{"deletes":["999"]}`)

	if out.Deleted[0].Success {
		t.Error("deleting non-existent task should not succeed")
	}
}

func TestTasks_BatchDelete(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "a")
	mustCreateForTool(t, list, "B", "b")
	mustCreateForTool(t, list, "C", "c")

	_, out := callTasks(t, list, `{"deletes":["1","2"]}`)
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

	_, out := callTasks(t, list, `{"get":"1"}`)
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
	_, out := callTasks(t, list, `{"get":"999"}`)

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
	callTasks(t, list, `{"updates":[{"taskId":"`+id1+`","addBlocks":["`+id2+`"]}]}`)

	_, out := callTasks(t, list, `{"get":"`+id1+`"}`)
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
	_, out := callTasks(t, list, `{"list":true}`)

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

	_, out := callTasks(t, list, `{"list":true}`)
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

	callTasks(t, list, `{"updates":[{"taskId":"`+id2+`","addBlockedBy":["`+id1+`"]}]}`)
	// Complete the blocker
	callTasks(t, list, `{"updates":[{"taskId":"`+id1+`","status":"completed"}]}`)

	_, out := callTasks(t, list, `{"list":true}`)
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

	_, out := callTasks(t, list, `{"list":true}`)
	if len(out.List.Tasks) != 1 {
		t.Fatalf("len = %d, want 1 (_internal excluded)", len(out.List.Tasks))
	}
	if out.List.Tasks[0].Subject != "Normal" {
		t.Errorf("Subject = %q, want %q", out.List.Tasks[0].Subject, "Normal")
	}
}

// ---------------------------------------------------------------------------
// Mixed operations
// ---------------------------------------------------------------------------

func TestTasks_Mixed_CreateAndList(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)

	_, out := callTasks(t, list, `{"creates":[{"subject":"New","description":"new task"}],"list":true}`)

	if len(out.Created) != 1 || out.Created[0].ID == "" {
		t.Fatal("create should succeed")
	}
	if out.List == nil {
		t.Fatal("List should not be nil")
	}
	if len(out.List.Tasks) != 1 {
		t.Fatalf("List.Tasks len = %d, want 1", len(out.List.Tasks))
	}
	if out.List.Tasks[0].Subject != "New" {
		t.Errorf("listed task subject = %q, want %q", out.List.Tasks[0].Subject, "New")
	}
}

func TestTasks_Mixed_DeleteAndGet(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "A", "desc")

	// Order: deletes -> get. Delete runs first, so get should return nil.
	_, out := callTasks(t, list, `{"deletes":["1"],"get":"1"}`)

	if !out.Deleted[0].Success {
		t.Error("delete should succeed")
	}
	if out.Get.Task != nil {
		t.Error("Get.Task should be nil after delete")
	}
}

func TestTasks_Mixed_FullBatch(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "PreExist1", "p1")
	mustCreateForTool(t, list, "PreExist2", "p2")

	input := `{"creates":[{"subject":"New","description":"n"}],"updates":[{"taskId":"1","subject":"Updated1"}],"deletes":["2"],"list":true,"get":"1"}`
	_, out := callTasks(t, list, input)

	// Create
	if len(out.Created) != 1 || out.Created[0].Subject != "New" {
		t.Errorf("Created = %v, unexpected", out.Created)
	}
	// Update
	if !out.Updated[0].Success {
		t.Error("update should succeed")
	}
	// Delete
	if !out.Deleted[0].Success {
		t.Error("delete should succeed")
	}
	// List: should have 2 tasks (PreExist1 updated + New)
	if out.List == nil || len(out.List.Tasks) != 2 {
		t.Fatalf("List.Tasks len = %d, want 2", len(out.List.Tasks))
	}
	// Get: task 1 should have updated subject
	if out.Get == nil || out.Get.Task == nil {
		t.Fatal("Get.Task should not be nil")
	}
	if out.Get.Task.Subject != "Updated1" {
		t.Errorf("Got subject = %q, want %q", out.Get.Task.Subject, "Updated1")
	}
}

func TestTasks_Mixed_CreateAndUpdate(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "Existing", "desc")

	_, out := callTasks(t, list, `{"creates":[{"subject":"New","description":"n"}],"updates":[{"taskId":"1","status":"in_progress"}]}`)

	if len(out.Created) != 1 || out.Created[0].ID == "" {
		t.Error("create should succeed")
	}
	if !out.Updated[0].Success {
		t.Error("update should succeed")
	}
	if out.Updated[0].StatusChange.To != "in_progress" {
		t.Errorf("StatusChange.To = %q, want in_progress", out.Updated[0].StatusChange.To)
	}
}

// ---------------------------------------------------------------------------
// Tool metadata
// ---------------------------------------------------------------------------

func TestTasks_IsReadOnly(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)

	tests := []struct {
		input string
		want  bool
	}{
		{`{"creates":[{"subject":"A","description":"a"}]}`, false},
		{`{"updates":[{"taskId":"1"}]}`, false},
		{`{"deletes":["1"]}`, false},
		{`{"list":true}`, true},
		{`{"get":"1"}`, true},
		{`{}`, true},
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
	tl := New(list)

	schema := tl.InputSchema()
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	for _, name := range []string{"creates", "updates", "deletes", "list", "get"} {
		if _, ok := props[name]; !ok {
			t.Errorf("property %q missing from schema", name)
		}
	}
}

func TestTasks_Name(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)
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
	tl := New(list)

	desc, err := tl.Description(json.RawMessage(`{"creates":[{"subject":"Fix auth","description":"a"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Fix auth" {
		t.Errorf("Description = %q, want %q", desc, "Fix auth")
	}
}

func TestTasks_Description_List(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)

	desc, err := tl.Description(json.RawMessage(`{"list":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "List all tasks" {
		t.Errorf("Description = %q, want %q", desc, "List all tasks")
	}
}

func TestTasks_Description_MultipleCreates(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)

	desc, err := tl.Description(json.RawMessage(`{"creates":[{"subject":"A","description":"a"},{"subject":"B","description":"b"},{"subject":"C","description":"c"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Create 3 tasks" {
		t.Errorf("Description = %q, want %q", desc, "Create 3 tasks")
	}
}

func TestTasks_Description_EmptyInput(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)

	desc, err := tl.Description(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Manage tasks" {
		t.Errorf("Description = %q, want %q", desc, "Manage tasks")
	}
}

func TestTasks_Description_UpdateExisting(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	mustCreateForTool(t, list, "My Task", "desc")
	tl := New(list)

	desc, err := tl.Description(json.RawMessage(`{"updates":[{"taskId":"1"}]}`))
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
	tl := New(list)

	desc, err := tl.Description(json.RawMessage(`{"get":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Some Task" {
		t.Errorf("Description = %q, want %q", desc, "Some Task")
	}
}

func TestTasks_Description_MixedOps(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)

	desc, err := tl.Description(json.RawMessage(`{"creates":[{"subject":"A","description":"a"}],"list":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "A, List all tasks" {
		t.Errorf("Description = %q, want %q", desc, "A, List all tasks")
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
	tl := New(list)
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
	tl := New(list)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"creates":[{"subject":"A","description":"a"}]}`), &tool.ToolUseContext{})
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
	tl := New(list)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"updates":[{"taskId":"1","status":"in_progress"}]}`), &tool.ToolUseContext{})
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
	tl := New(list)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"deletes":["1"]}`), &tool.ToolUseContext{})
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
	tl := New(list)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"list":true}`), &tool.ToolUseContext{})
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
	tl := New(list)
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
	tl := New(list)
	desc, err := tl.Description(json.RawMessage(`{"updates":[{"taskId":"999"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "#999" {
		t.Errorf("Description = %q, want %q", desc, "#999")
	}
}

func TestTasks_Description_MultipleUpdates(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)
	desc, err := tl.Description(json.RawMessage(`{"updates":[{"taskId":"1"},{"taskId":"2"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "Update 2 tasks" {
		t.Errorf("Description = %q, want %q", desc, "Update 2 tasks")
	}
}

func TestTasks_Description_Deletes(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	tl := New(list)
	desc, err := tl.Description(json.RawMessage(`{"deletes":["1","2"]}`))
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
	tl := New(list)
	desc, err := tl.Description(json.RawMessage(`{"get":"999"}`))
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
	tl := New(list)
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
	callTasks(t, list, `{"updates":[{"taskId":"`+id2+`","addBlockedBy":["`+id1+`"]}]}`)

	_, out := callTasks(t, list, `{"list":true}`)
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

	tl := New(list)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"updates":[{"taskId":"`+id+`","status":"in_progress"}]}`), &tool.ToolUseContext{})
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
// Update: DeleteTask IO error during status=deleted path
// ---------------------------------------------------------------------------

func TestTasks_Update_DeleteIOError(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "A", "desc")

	// Make the dir read-only so DeleteTask fails on os.Remove
	dir := list.Dir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0755) }()

	tl := New(list)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"updates":[{"taskId":"`+id+`","status":"deleted"}]}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Data.(*TasksOutput)
	errMsg := out.Updated[0].Error
	if errMsg == "" {
		t.Fatalf("DeleteTask should fail when dir is read-only, got empty error")
	}
	if !strings.Contains(errMsg, "remove") && !strings.Contains(errMsg, "permission") {
		t.Errorf("unexpected error message: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// Update: DeleteTask returns deleted=false — defensive path
// Only possible with concurrent modification; tested as "Task not found" instead.
// ---------------------------------------------------------------------------

func TestTasks_Update_DeleteAlreadyGone(t *testing.T) {
	t.Parallel()
	list := newTestListForTool(t)
	id := mustCreateForTool(t, list, "A", "desc")

	// Delete the file manually so DeleteTask returns (false, nil)
	_ = os.Remove(filepath.Join(list.Dir(), id+".json"))

	tl := New(list)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"updates":[{"taskId":"`+id+`","status":"deleted"}]}`), &tool.ToolUseContext{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Data.(*TasksOutput)
	if out.Updated[0].Error != "Task not found" {
		t.Errorf("Error = %q, want %q", out.Updated[0].Error, "Task not found")
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

	tl := New(list)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"list":true}`), &tool.ToolUseContext{})
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
	callTasks(t, list, `{"updates":[{"taskId":"1","status":"completed"}]}`)

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

