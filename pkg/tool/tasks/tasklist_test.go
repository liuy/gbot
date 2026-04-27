package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskList_Empty(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskList(list)
	result, err := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out := result.Data.(*ListOutput)
	if len(out.Tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(out.Tasks))
	}

	// Source: TaskListTool.ts:93-98 — "No tasks found" when empty
	rendered := tool.RenderResult(result.Data)
	if rendered != "No tasks found" {
		t.Errorf("RenderResult = %q, want %q", rendered, "No tasks found")
	}
}

func TestTaskList_OrderByID(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	if _, err := list.CreateTask("Third task", "desc", "", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := list.CreateTask("First task", "desc", "", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := list.CreateTask("Second task", "desc", "", nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	tool := NewTaskList(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)
	out := result.Data.(*ListOutput)

	if len(out.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(out.Tasks))
	}
	// Tasks should be ordered by numeric ID
	if out.Tasks[0].ID != "1" || out.Tasks[0].Subject != "Third task" {
		t.Errorf("task[0] = #%s %q, want #1 Third task", out.Tasks[0].ID, out.Tasks[0].Subject)
	}
	if out.Tasks[1].ID != "2" || out.Tasks[1].Subject != "First task" {
		t.Errorf("task[1] = #%s %q, want #2 First task", out.Tasks[1].ID, out.Tasks[1].Subject)
	}
	if out.Tasks[2].ID != "3" || out.Tasks[2].Subject != "Second task" {
		t.Errorf("task[2] = #%s %q, want #3 Second task", out.Tasks[2].ID, out.Tasks[2].Subject)
	}
}

func TestTaskList_FilterCompletedBlockers(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Task 1: completed, blocks task 3
	id1, _ := list.CreateTask("Task 1", "desc", "", nil)
	// Task 2: pending, blocks task 3
	id2, _ := list.CreateTask("Task 2", "desc", "", nil)
	// Task 3: blocked by 1 and 2
	id3, _ := list.CreateTask("Task 3", "desc", "", nil)

	list.BlockTask(id1, id3)
	list.BlockTask(id2, id3)

	// Mark task 1 as completed
	if _, _, err := list.UpdateTask(id1, TaskUpdates{Status: new(StatusCompleted)}); err != nil {
		t.Fatalf("update: %v", err)
	}

	tool := NewTaskList(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)
	out := result.Data.(*ListOutput)

	// Source: TaskListTool.ts:82 — blockedBy filters out completed IDs
	task3 := out.Tasks[2]
	if task3.ID != id3 {
		t.Fatalf("task3 ID = %q, want %q", task3.ID, id3)
	}
	if len(task3.BlockedBy) != 1 {
		t.Fatalf("task3 BlockedBy = %v (len %d), want len 1 (only pending blocker)", task3.BlockedBy, len(task3.BlockedBy))
	}
	if task3.BlockedBy[0] != id2 {
		t.Errorf("task3 BlockedBy[0] = %q, want %q (pending task)", task3.BlockedBy[0], id2)
	}
}

func TestTaskList_ExcludesInternal(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Regular task
	if _, err := list.CreateTask("Public task", "desc", "", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Internal task (metadata._internal set)
	if _, err := list.CreateTask("Internal task", "desc", "", map[string]any{"_internal": true}); err != nil {
		t.Fatalf("create: %v", err)
	}

	tool := NewTaskList(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)
	out := result.Data.(*ListOutput)

	// Source: TaskListTool.ts:68-69 — filter out metadata._internal
	if len(out.Tasks) != 1 {
		t.Fatalf("expected 1 task (internal filtered), got %d", len(out.Tasks))
	}
	if out.Tasks[0].Subject != "Public task" {
		t.Errorf("task subject = %q, want %q", out.Tasks[0].Subject, "Public task")
	}
}

func TestTaskList_WithOwner(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, err := list.CreateTask("Owned task", "desc", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := list.UpdateTask(id, TaskUpdates{Owner: new("agent-1")}); err != nil {
		t.Fatalf("update: %v", err)
	}

	tool := NewTaskList(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)
	out := result.Data.(*ListOutput)

	if len(out.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(out.Tasks))
	}
	if out.Tasks[0].Owner != "agent-1" {
		t.Errorf("Owner = %q, want %q", out.Tasks[0].Owner, "agent-1")
	}
}

func TestTaskList_RenderResult(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id1, _ := list.CreateTask("Task 1", "desc", "", nil)
	id2, _ := list.CreateTask("Task 2", "desc", "", nil)
	list.BlockTask(id1, id2)

	tool := NewTaskList(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)

	// Source: TaskListTool.ts:101-108 — wire format
	rendered := tool.RenderResult(result.Data)

	if !strings.Contains(rendered, "#1 [pending] Task 1") {
		t.Errorf("render should contain task 1, got: %q", rendered)
	}
	if !strings.Contains(rendered, "#2 [pending] Task 2") {
		t.Errorf("render should contain task 2, got: %q", rendered)
	}
	if !strings.Contains(rendered, "blocked by #1") {
		t.Errorf("render should show blocker, got: %q", rendered)
	}
}

func TestTaskList_RenderResult_WithOwner(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id, err := list.CreateTask("Owned task", "desc", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := list.UpdateTask(id, TaskUpdates{Owner: new("bot-1")}); err != nil {
		t.Fatalf("update: %v", err)
	}

	tool := NewTaskList(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)

	rendered := tool.RenderResult(result.Data)
	if !strings.Contains(rendered, "(bot-1)") {
		t.Errorf("render should contain owner, got: %q", rendered)
	}
}

func TestTaskList_IsReadOnly(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskList(list)
	if !tool.IsReadOnly(nil) {
		t.Error("TaskList should be read-only")
	}
}

func TestTaskList_Description(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskList(list)
	desc, err := tool.Description(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if desc != "List all tasks" {
		t.Errorf("desc = %q, want %q", desc, "List all tasks")
	}
}

func TestTaskList_RenderResult_WrongType(t *testing.T) {
	tool := NewTaskList(NewList(""))
	rendered := tool.RenderResult("bad")
	if rendered != "bad" {
		t.Errorf("fallback render = %q, want %q", rendered, "bad")
	}
}

func TestTaskList_AllMethods(t *testing.T) {
	tool := NewTaskList(NewList(""))
	schema := tool.InputSchema()
	if len(schema) == 0 {
		t.Error("InputSchema should not be empty")
	}
	if !tool.IsConcurrencySafe(nil) {
		t.Error("should be concurrency safe")
	}
	if !tool.IsReadOnly(nil) {
		t.Error("should be read-only")
	}
}

func TestTaskList_ListReadDirError(t *testing.T) {
	dir := t.TempDir()
	// Point to a file instead of directory — ReadDir fails with ENOTDIR
	fileAsDir := filepath.Join(dir, "not-a-dir")
	_ = os.WriteFile(fileAsDir, []byte("x"), 0o644)
	list := NewList(fileAsDir)

	tool := NewTaskList(list)
	_, err := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error when list dir is a file")
	}
	if !strings.Contains(err.Error(), "list tasks") {
		t.Errorf("error should mention 'list tasks', got: %v", err)
	}
}

func TestTaskList_ListError(t *testing.T) {
	// ListTasks handles missing dir gracefully (returns empty), so this tests
	// the empty-list rendering path instead.
	dir := t.TempDir()
	l := NewList(dir)
	if err := l.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	tool := NewTaskList(l)
	result, err := tool.Call(context.TODO(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	rendered := tool.RenderResult(result.Data)
	if rendered != "No tasks found" {
		t.Errorf("empty list = %q, want %q", rendered, "No tasks found")
	}
}
