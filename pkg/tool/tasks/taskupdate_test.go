package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Test helpers
//
//go:fix inline

func TestTaskUpdate_StatusFlow(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Test task", "desc", "", nil)
	tool := NewTaskUpdate(list)

	// pending → in_progress
	input := `{"taskId":"` + id + `", "status":"in_progress"}`
	result, err := tool.Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call in_progress: %v", err)
	}
	out := result.Data.(*UpdateOutput)
	if !out.Success {
		t.Errorf("success = false, want true")
	}
	if out.StatusChange == nil {
		t.Fatal("statusChange should not be nil")
	}
	if out.StatusChange.From != "pending" || out.StatusChange.To != "in_progress" {
		t.Errorf("statusChange = %+v, want pending→in_progress", out.StatusChange)
	}

	// in_progress → completed
	input = `{"taskId":"` + id + `", "status":"completed"}`
	result, _ = tool.Call(context.TODO(), json.RawMessage(input), nil)
	out = result.Data.(*UpdateOutput)
	if out.StatusChange.To != "completed" {
		t.Errorf("statusChange.To = %q, want %q", out.StatusChange.To, "completed")
	}
}

func TestTaskUpdate_StatusRegression(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Test task", "desc", "", nil)
	if _, _, err := list.UpdateTask(id, TaskUpdates{Status: new(StatusCompleted)}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// completed → in_progress (regression allowed, aligned with TS)
	input := `{"taskId":"` + id + `", "status":"in_progress"}`
	result, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out := result.Data.(*UpdateOutput)
	if !out.Success {
		t.Error("status regression should succeed")
	}
	if out.StatusChange == nil || out.StatusChange.To != "in_progress" {
		t.Errorf("statusChange should show completed→in_progress, got %+v", out.StatusChange)
	}
}

func TestTaskUpdate_Delete(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("To delete", "desc", "", nil)

	input := `{"taskId":"` + id + `", "status":"deleted"}`
	result, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out := result.Data.(*UpdateOutput)
	if !out.Success {
		t.Error("delete should succeed")
	}
	if len(out.UpdatedFields) != 1 || out.UpdatedFields[0] != "deleted" {
		t.Errorf("updatedFields = %v, want [deleted]", out.UpdatedFields)
	}
	if out.StatusChange == nil || out.StatusChange.To != "deleted" {
		t.Errorf("statusChange should show →deleted, got %+v", out.StatusChange)
	}

	// Verify task is gone from disk
	task, _ := list.GetTask(id)
	if task != nil {
		t.Error("task should be deleted from disk")
	}
}

func TestTaskUpdate_AddBlocks(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id1, _ := list.CreateTask("Task 1", "desc", "", nil)
	id2, _ := list.CreateTask("Task 2", "desc", "", nil)

	input := `{"taskId":"` + id1 + `", "addBlocks":["` + id2 + `"]}`
	result, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out := result.Data.(*UpdateOutput)
	if !out.Success {
		t.Error("addBlocks should succeed")
	}
	// Verify bidirectional relationship
	t1, _ := list.GetTask(id1)
	t2, _ := list.GetTask(id2)
	if len(t1.Blocks) != 1 || t1.Blocks[0] != id2 {
		t.Errorf("task1.Blocks = %v, want [%s]", t1.Blocks, id2)
	}
	if len(t2.BlockedBy) != 1 || t2.BlockedBy[0] != id1 {
		t.Errorf("task2.BlockedBy = %v, want [%s]", t2.BlockedBy, id1)
	}
}

func TestTaskUpdate_AddBlockedBy(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id1, _ := list.CreateTask("Task 1", "desc", "", nil)
	id2, _ := list.CreateTask("Task 2", "desc", "", nil)

	input := `{"taskId":"` + id2 + `", "addBlockedBy":["` + id1 + `"]}`
	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	out := result.Data.(*UpdateOutput)
	if !out.Success {
		t.Error("addBlockedBy should succeed")
	}

	t2, _ := list.GetTask(id2)
	if len(t2.BlockedBy) != 1 || t2.BlockedBy[0] != id1 {
		t.Errorf("task2.BlockedBy = %v, want [%s]", t2.BlockedBy, id1)
	}
}

func TestTaskUpdate_Subject(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Old subject", "desc", "", nil)
	input := `{"taskId":"` + id + `", "subject":"New subject"}`

	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	out := result.Data.(*UpdateOutput)
	if !out.Success {
		t.Error("update should succeed")
	}
	if !containsField(out.UpdatedFields, "subject") {
		t.Errorf("updatedFields = %v, should contain 'subject'", out.UpdatedFields)
	}

	task, _ := list.GetTask(id)
	if task.Subject != "New subject" {
		t.Errorf("subject = %q, want %q", task.Subject, "New subject")
	}
}

func TestTaskUpdate_Description(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Task", "old desc", "", nil)
	input := `{"taskId":"` + id + `", "description":"new desc"}`

	if _, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	task, _ := list.GetTask(id)
	if task.Description != "new desc" {
		t.Errorf("description = %q, want %q", task.Description, "new desc")
	}
}

func TestTaskUpdate_ActiveForm(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Task", "desc", "", nil)
	input := `{"taskId":"` + id + `", "activeForm":"Running tests"}`

	if _, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	task, _ := list.GetTask(id)
	if task.ActiveForm != "Running tests" {
		t.Errorf("activeForm = %q, want %q", task.ActiveForm, "Running tests")
	}
}

func TestTaskUpdate_MetadataMerge(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Task", "desc", "", map[string]any{"keep": "yes", "remove": "val"})

	// Merge: add new key, delete "remove" (null value)
	input := `{"taskId":"` + id + `", "metadata":{"add":"new","remove":null}}`
	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	out := result.Data.(*UpdateOutput)
	if !out.Success {
		t.Error("metadata merge should succeed")
	}

	task, _ := list.GetTask(id)
	if task.Metadata["keep"] != "yes" {
		t.Errorf("keep = %v, want %q", task.Metadata["keep"], "yes")
	}
	if task.Metadata["add"] != "new" {
		t.Errorf("add = %v, want %q", task.Metadata["add"], "new")
	}
	if _, exists := task.Metadata["remove"]; exists {
		t.Error("remove key should be deleted")
	}
}

func TestTaskUpdate_ClearOwner(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Task", "desc", "", nil)
	if _, _, err := list.UpdateTask(id, TaskUpdates{Owner: new("agent-1")}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Clear owner with empty string
	input := `{"taskId":"` + id + `", "owner":""}`
	if _, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil); err != nil {
		t.Fatalf("Call: %v", err)
	}

	task, _ := list.GetTask(id)
	if task.Owner != "" {
		t.Errorf("owner = %q, want empty string", task.Owner)
	}
}

func TestTaskUpdate_NotFound(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	input := `{"taskId":"999", "status":"in_progress"}`
	result, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out := result.Data.(*UpdateOutput)
	if out.Success {
		t.Error("should fail for non-existent task")
	}
	if out.Error != "Task not found" {
		t.Errorf("error = %q, want %q", out.Error, "Task not found")
	}
}

func TestTaskUpdate_EmptyID(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(`{"taskId":""}`), nil)
	if err == nil {
		t.Fatal("expected error for empty taskId")
	}
	if !strings.Contains(err.Error(), "taskId is required") {
		t.Errorf("error = %q, want 'taskId is required'", err.Error())
	}
}

func TestTaskUpdate_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want 'parse input'", err.Error())
	}
}

func TestTaskUpdate_NoChanges(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Task", "desc", "", nil)

	// Update with same values — no actual change
	input := `{"taskId":"` + id + `", "subject":"Task"}`
	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	out := result.Data.(*UpdateOutput)
	if !out.Success {
		t.Error("no-op update should still succeed")
	}
	// Store layer returns empty updatedFields when no fields changed
	// (AddBlocks/AddBlockedBy are not provided here either)
}

func TestTaskUpdate_RenderResult_Success(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Task", "desc", "", nil)

	input := `{"taskId":"` + id + `", "status":"in_progress"}`
	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)

	rendered := NewTaskUpdate(list).RenderResult(result.Data)
	// Source: TaskUpdateTool.ts:384 — "Updated task #N field1, field2"
	if !strings.Contains(rendered, "Updated task #"+id) {
		t.Errorf("render should contain 'Updated task #%s', got: %q", id, rendered)
	}
	if !strings.Contains(rendered, "status") {
		t.Errorf("render should contain 'status' in updatedFields, got: %q", rendered)
	}
}

func TestTaskUpdate_RenderResult_NotFound(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	input := `{"taskId":"999", "status":"in_progress"}`
	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)

	rendered := NewTaskUpdate(list).RenderResult(result.Data)
	if rendered != "Task not found" {
		t.Errorf("RenderResult = %q, want %q", rendered, "Task not found")
	}
}

func TestTaskUpdate_Description_Func(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskUpdate(list)

	desc, err := tool.Description(json.RawMessage(`{"taskId":"42"}`))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if desc != "#42" {
		t.Errorf("desc = %q, want %q", desc, "#42")
	}

	desc, err = tool.Description(json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("Description fallback: %v", err)
	}
	if desc != "Update a task" {
		t.Errorf("fallback desc = %q, want %q", desc, "Update a task")
	}
}

func TestTaskUpdate_MultipleFields(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, _ := list.CreateTask("Old", "old desc", "", nil)

	input := `{
		"taskId":"` + id + `",
		"subject":"New",
		"description":"new desc",
		"status":"in_progress",
		"owner":"agent-1"
	}`
	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	out := result.Data.(*UpdateOutput)

	if !out.Success {
		t.Error("multi-field update should succeed")
	}
	// Should have 4+ updated fields
	if len(out.UpdatedFields) < 4 {
		t.Errorf("updatedFields = %v, want at least 4 fields", out.UpdatedFields)
	}

	task, _ := list.GetTask(id)
	if task.Subject != "New" {
		t.Errorf("subject = %q, want %q", task.Subject, "New")
	}
	if task.Description != "new desc" {
		t.Errorf("description = %q, want %q", task.Description, "new desc")
	}
	if task.Status != StatusInProgress {
		t.Errorf("status = %q, want %q", task.Status, StatusInProgress)
	}
	if task.Owner != "agent-1" {
		t.Errorf("owner = %q, want %q", task.Owner, "agent-1")
	}
}

func containsField(fields []string, target string) bool {
	return slices.Contains(fields, target)
}

func TestTaskUpdate_RenderResult_WrongType(t *testing.T) {
	tool := NewTaskUpdate(NewList(""))
	rendered := tool.RenderResult(99)
	if rendered != "99" {
		t.Errorf("fallback render = %q, want %q", rendered, "99")
	}
}

func TestTaskUpdate_AllMethods(t *testing.T) {
	tool := NewTaskUpdate(NewList(""))
	schema := tool.InputSchema()
	if len(schema) == 0 {
		t.Error("InputSchema should not be empty")
	}
	if !tool.IsConcurrencySafe(nil) {
		t.Error("should be concurrency safe")
	}
	if tool.IsReadOnly(nil) {
		t.Error("should not be read-only")
	}
}

func TestTaskUpdate_RenderResult_NotFoundNoError(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Delete a non-existent task → returns success=false with empty error
	input := `{"taskId":"999", "status":"deleted"}`
	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	out := result.Data.(*UpdateOutput)
	if out.Success {
		t.Error("should fail")
	}
	// RenderResult should show "Task #999 not found" when Error is empty
	out.Error = ""
	rendered := NewTaskUpdate(list).RenderResult(out)
	if !strings.Contains(rendered, "Task #999 not found") {
		t.Errorf("expected 'Task #999 not found', got: %q", rendered)
	}
}

func TestTaskUpdate_DeleteFailed(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Delete non-existent task — GetTask returns nil first, so "Task not found"
	input := `{"taskId":"999", "status":"deleted"}`
	result, _ := NewTaskUpdate(list).Call(context.TODO(), json.RawMessage(input), nil)
	out := result.Data.(*UpdateOutput)
	if out.Success {
		t.Error("deleting non-existent task should fail")
	}
	if out.Error != "Task not found" {
		t.Errorf("error = %q, want %q", out.Error, "Task not found")
	}
}

func TestTaskUpdate_GetTaskError(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Create a directory where task file should be to cause GetTask error
	_ = os.MkdirAll(filepath.Join(dir, "1.json"), 0o755)
	tool := NewTaskUpdate(list)
	_, err := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"1","subject":"X"}`), nil)
	if err == nil {
		t.Fatal("expected error reading directory as file")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error = %v, want read", err)
	}
}

func TestTaskUpdate_DeleteError(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Create a task then replace file with non-empty dir to cause DeleteTask error
	id, _ := list.CreateTask("ToDel", "desc", "", nil)
	path := filepath.Join(dir, id+".json")
	_ = os.Remove(path)
	_ = os.MkdirAll(path, 0o755)
	_ = os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o600)

	tool := NewTaskUpdate(list)
	_, err := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"`+id+`","status":"deleted"}`), nil)
	if err == nil {
		t.Fatal("expected error when delete fails")
	}
	if !strings.Contains(err.Error(), "get task") {
		t.Errorf("error = %v, want delete", err)
	}
}

func TestTaskUpdate_UpdateError(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	id, _ := list.CreateTask("Task", "desc", "", nil)
	// Make dir read-only to cause UpdateTask write failure
	_ = os.Chmod(dir, 0o555)
	defer func() { _ = os.Chmod(dir, 0o755) }()

	tool := NewTaskUpdate(list)
	_, err := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"`+id+`","subject":"New"}`), nil)
	if err == nil {
		t.Fatal("expected error writing to read-only dir")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Errorf("error = %v, want write", err)
	}
}
