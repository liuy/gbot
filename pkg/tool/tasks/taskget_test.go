package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskGet_Existing(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Create a task first
	id, err := list.CreateTask("Fix auth bug", "The login flow is broken", "Fixing auth", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := NewTaskGet(list)
	input := `{"taskId": "` + id + `"}`

	result, err := tool.Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out, ok := result.Data.(*GetOutput)
	if !ok {
		t.Fatalf("expected *GetOutput, got %T", result.Data)
	}
	if out.Task == nil {
		t.Fatal("task should not be nil")
	}
	if out.Task.ID != id {
		t.Errorf("ID = %q, want %q", out.Task.ID, id)
	}
	if out.Task.Subject != "Fix auth bug" {
		t.Errorf("Subject = %q, want %q", out.Task.Subject, "Fix auth bug")
	}
	if out.Task.Description != "The login flow is broken" {
		t.Errorf("Description = %q, want %q", out.Task.Description, "The login flow is broken")
	}
	if out.Task.Status != StatusPending {
		t.Errorf("Status = %q, want %q", out.Task.Status, StatusPending)
	}
	if out.Task.Blocks == nil {
		t.Error("Blocks should be non-nil empty slice")
	}
	if out.Task.BlockedBy == nil {
		t.Error("BlockedBy should be non-nil empty slice")
	}
}

func TestTaskGet_NotFound(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskGet(list)
	result, err := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"999"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out := result.Data.(*GetOutput)
	if out.Task != nil {
		t.Errorf("expected nil task for not found, got %+v", out.Task)
	}
}

func TestTaskGet_WithBlocks(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id1, _ := list.CreateTask("Task 1", "desc", "", nil)
	id2, _ := list.CreateTask("Task 2", "desc", "", nil)

	// Establish block: task 1 blocks task 2
	list.BlockTask(id1, id2)

	tool := NewTaskGet(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"`+id2+`"}`), nil)
	out := result.Data.(*GetOutput)

	if len(out.Task.BlockedBy) != 1 || out.Task.BlockedBy[0] != id1 {
		t.Errorf("BlockedBy = %v, want [%s]", out.Task.BlockedBy, id1)
	}

	// Check task 1 has blocks
	result1, _ := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"`+id1+`"}`), nil)
	out1 := result1.Data.(*GetOutput)
	if len(out1.Task.Blocks) != 1 || out1.Task.Blocks[0] != id2 {
		t.Errorf("Blocks = %v, want [%s]", out1.Task.Blocks, id2)
	}
}

func TestTaskGet_EmptyID(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := NewTaskGet(list).Call(context.TODO(), json.RawMessage(`{"taskId":""}`), nil)
	if err == nil {
		t.Fatal("expected error for empty taskId")
	}
	if !strings.Contains(err.Error(), "taskId is required") {
		t.Errorf("error = %q, want 'taskId is required'", err.Error())
	}
}

func TestTaskGet_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := NewTaskGet(list).Call(context.TODO(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want 'parse input'", err.Error())
	}
}

func TestTaskGet_RenderResult_Found(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id1, _ := list.CreateTask("Fix auth", "Login is broken", "", nil)
	id2, _ := list.CreateTask("Write tests", "Need unit tests", "", nil)
	list.BlockTask(id1, id2)

	tool := NewTaskGet(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"`+id2+`"}`), nil)

	rendered := tool.RenderResult(result.Data)

	// Source: TaskGetTool.ts:109-119 — wire format lines
	if !strings.Contains(rendered, "Task #2: Write tests") {
		t.Errorf("render should contain task header, got: %q", rendered)
	}
	if !strings.Contains(rendered, "Status: pending") {
		t.Errorf("render should contain status, got: %q", rendered)
	}
	if !strings.Contains(rendered, "Description: Need unit tests") {
		t.Errorf("render should contain description, got: %q", rendered)
	}
	if !strings.Contains(rendered, "Blocked by: #1") {
		t.Errorf("render should contain blocked by, got: %q", rendered)
	}
	if strings.Contains(rendered, "Blocks:") {
		t.Errorf("render should not contain Blocks (empty), got: %q", rendered)
	}
}

func TestTaskGet_RenderResult_NotFound(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskGet(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"999"}`), nil)

	rendered := tool.RenderResult(result.Data)
	// Source: TaskGetTool.ts:101-106 — "Task not found"
	if rendered != "Task not found" {
		t.Errorf("RenderResult = %q, want %q", rendered, "Task not found")
	}
}

func TestTaskGet_RenderResult_WithBlocks(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id1, _ := list.CreateTask("Task 1", "desc", "", nil)
	id2, _ := list.CreateTask("Task 2", "desc", "", nil)
	id3, _ := list.CreateTask("Task 3", "desc", "", nil)
	list.BlockTask(id1, id3)
	list.BlockTask(id2, id3)

	tool := NewTaskGet(list)
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"`+id3+`"}`), nil)

	rendered := tool.RenderResult(result.Data)
	// Should show "Blocked by: #1, #2" (order depends on append order)
	if !strings.Contains(rendered, "Blocked by: #1, #2") && !strings.Contains(rendered, "Blocked by: #2, #1") {
		t.Errorf("render should contain blocked by IDs, got: %q", rendered)
	}
}

func TestTaskGet_Description(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskGet(list)

	// Valid input → dynamic description
	desc, err := tool.Description(json.RawMessage(`{"taskId":"42"}`))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if desc != "#42" {
		t.Errorf("desc = %q, want %q", desc, "#42")
	}

	// Invalid input → fallback
	desc, err = tool.Description(json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("Description fallback: %v", err)
	}
	if desc != "Retrieve a task by ID" {
		t.Errorf("fallback desc = %q, want %q", desc, "Retrieve a task by ID")
	}
}

func TestTaskGet_IsReadOnly(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskGet(list)
	if !tool.IsReadOnly(nil) {
		t.Error("TaskGet should be read-only")
	}
}

func TestTaskGet_RenderResult_WrongType(t *testing.T) {
	tool := NewTaskGet(NewList(""))
	rendered := tool.RenderResult(42)
	if rendered != "42" {
		t.Errorf("fallback render = %q, want %q", rendered, "42")
	}
}

func TestTaskGet_GetError(t *testing.T) {
	dir := t.TempDir()
	l := NewList(dir)
	if err := l.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "1.json"), 0o755)

	tool := NewTaskGet(l)
	_, err := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"1"}`), nil)
	if err == nil {
		t.Fatal("expected error reading directory as file")
	}
		if !strings.Contains(err.Error(), "read") {
			t.Errorf("error = %v, want read", err)
		}
}

func TestTaskGet_AllMethods(t *testing.T) {
	tool := NewTaskGet(NewList(""))
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

func TestTaskGet_RenderResult_WithBlocksOutput(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	id1, _ := list.CreateTask("Blocker", "blocks others", "", nil)
	id2, _ := list.CreateTask("Blocked", "is blocked", "", nil)
	list.BlockTask(id1, id2)

	tool := NewTaskGet(list)
	// Get task 1 which has Blocks
	result, _ := tool.Call(context.TODO(), json.RawMessage(`{"taskId":"`+id1+`"}`), nil)

	rendered := tool.RenderResult(result.Data)
	if !strings.Contains(rendered, "Blocks: #"+id2) {
		t.Errorf("render should show Blocks, got: %q", rendered)
	}
}

func TestFormatIDList(t *testing.T) {
	tests := []struct {
		ids  []string
		want string
	}{
		{[]string{"1"}, "#1"},
		{[]string{"1", "2"}, "#1, #2"},
		{[]string{"1", "2", "3"}, "#1, #2, #3"},
		{[]string{}, ""},
		{nil, ""},
	}
	for _, tt := range tests {
		got := formatIDList(tt.ids)
		if got != tt.want {
			t.Errorf("formatIDList(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}
