package tasks

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestTaskCreate_Basic(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskCreate(list)
	input := `{"subject": "Fix auth bug", "description": "The login flow is broken"}`

	result, err := tool.Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out, ok := result.Data.(*CreateOutput)
	if !ok {
		t.Fatalf("expected *CreateOutput, got %T", result.Data)
	}
	if out.Task.ID != "1" {
		t.Errorf("ID = %q, want %q", out.Task.ID, "1")
	}
	if out.Task.Subject != "Fix auth bug" {
		t.Errorf("Subject = %q, want %q", out.Task.Subject, "Fix auth bug")
	}

	// Verify task was persisted to disk
	task, err := list.GetTask("1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task == nil {
		t.Fatal("task should exist on disk")
	}
	if task.Subject != "Fix auth bug" {
		t.Errorf("persisted subject = %q, want %q", task.Subject, "Fix auth bug")
	}
	if task.Description != "The login flow is broken" {
		t.Errorf("persisted description = %q, want %q", task.Description, "The login flow is broken")
	}
	if task.Status != StatusPending {
		t.Errorf("persisted status = %q, want %q", task.Status, StatusPending)
	}
}

func TestTaskCreate_WithAllFields(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	input := `{
		"subject": "Write tests",
		"description": "Add unit tests for auth module",
		"activeForm": "Writing tests",
		"metadata": {"priority": "high", "team": "backend"}
	}`

	result, err := NewTaskCreate(list).Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out := result.Data.(*CreateOutput)
	if out.Task.ID != "1" {
		t.Errorf("ID = %q, want %q", out.Task.ID, "1")
	}

	task, _ := list.GetTask("1")
	if task.ActiveForm != "Writing tests" {
		t.Errorf("activeForm = %q, want %q", task.ActiveForm, "Writing tests")
	}
	if task.Metadata["priority"] != "high" {
		t.Errorf("metadata priority = %v, want %q", task.Metadata["priority"], "high")
	}
	if task.Metadata["team"] != "backend" {
		t.Errorf("metadata team = %v, want %q", task.Metadata["team"], "backend")
	}
}

func TestTaskCreate_EmptySubject(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := NewTaskCreate(list).Call(context.TODO(), json.RawMessage(`{"subject":"","description":"test"}`), nil)
	if err == nil {
		t.Fatal("expected error for empty subject")
	}
	if !strings.Contains(err.Error(), "subject is required") {
		t.Errorf("error = %q, want 'subject is required'", err.Error())
	}
}

func TestTaskCreate_EmptyDescription(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := NewTaskCreate(list).Call(context.TODO(), json.RawMessage(`{"subject":"test","description":""}`), nil)
	if err == nil {
		t.Fatal("expected error for empty description")
	}
	if !strings.Contains(err.Error(), "description is required") {
		t.Errorf("error = %q, want 'description is required'", err.Error())
	}
}

func TestTaskCreate_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := NewTaskCreate(list).Call(context.TODO(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want 'parse input'", err.Error())
	}
}

func TestTaskCreate_MonotonicIDs(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskCreate(list)
	for i := 1; i <= 3; i++ {
		input := `{"subject":"task","description":"test"}`
		result, err := tool.Call(context.TODO(), json.RawMessage(input), nil)
		if err != nil {
			t.Fatalf("Call %d: %v", i, err)
		}
		out := result.Data.(*CreateOutput)
		want := strconv.Itoa(i)
		if out.Task.ID != want {
			t.Errorf("task %d: ID = %q, want %q", i, out.Task.ID, want)
		}
	}
}

func TestTaskCreate_RenderResult(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskCreate(list)
	_, _ = tool.Call(context.TODO(), json.RawMessage(`{"subject":"Fix auth","description":"test"}`), nil)

	result, _ := tool.Call(context.TODO(), json.RawMessage(`{"subject":"Write docs","description":"test"}`), nil)
	out := result.Data.(*CreateOutput)

	rendered := tool.RenderResult(out)
	want := "Task #2 created successfully: Write docs"
	if rendered != want {
		t.Errorf("RenderResult = %q, want %q", rendered, want)
	}
}

func TestTaskCreate_Description(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	tool := NewTaskCreate(list)

	// Valid input → dynamic description
	desc, err := tool.Description(json.RawMessage(`{"subject":"Fix auth","description":"test"}`))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if desc != "Fix auth" {
		t.Errorf("desc = %q, want %q", desc, "Fix auth")
	}

	// Invalid input → fallback description
	desc, err = tool.Description(json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("Description fallback: %v", err)
	}
	if desc != "Create a task in the task list" {
		t.Errorf("fallback desc = %q, want %q", desc, "Create a task in the task list")
	}
}

func TestTaskCreate_MetaDataTypes(t *testing.T) {
	dir := t.TempDir()
	list := NewList(dir)
	if err := list.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	input := `{
		"subject": "Complex task",
		"description": "Test various metadata types",
		"metadata": {
			"string_val": "hello",
			"number_val": 42,
			"bool_val": true,
			"nested": {"key": "value"}
		}
	}`

	result, err := NewTaskCreate(list).Call(context.TODO(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	out := result.Data.(*CreateOutput)
	task, _ := list.GetTask(out.Task.ID)
	if task.Metadata["string_val"] != "hello" {
		t.Errorf("string metadata = %v, want %q", task.Metadata["string_val"], "hello")
	}
	// JSON numbers unmarshal to float64
	if num, ok := task.Metadata["number_val"].(float64); !ok || num != 42 {
		t.Errorf("number metadata = %v, want 42", task.Metadata["number_val"])
	}
	if b, ok := task.Metadata["bool_val"].(bool); !ok || !b {
		t.Errorf("bool metadata = %v, want true", task.Metadata["bool_val"])
	}
}

func TestTaskCreate_RenderResult_WrongType(t *testing.T) {
	tool := NewTaskCreate(NewList(""))
	rendered := tool.RenderResult("not a CreateOutput")
	if rendered != "not a CreateOutput" {
		t.Errorf("fallback render = %q, want original value", rendered)
	}
}

func TestTaskCreate_CreateError(t *testing.T) {
	dir := t.TempDir()
	l := NewList(dir)
	if err := l.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	_ = os.Chmod(dir, 0o555)
	defer func() { _ = os.Chmod(dir, 0o755) }()

	tool := NewTaskCreate(l)
	_, err := tool.Call(context.TODO(), json.RawMessage(`{"subject":"X","description":"D"}`), nil)
	if err == nil {
		t.Fatal("expected error writing to read-only dir")
	}
		if !strings.Contains(err.Error(), "write") {
			t.Errorf("error = %v, want write", err)
		}
}

func TestTaskCreate_AllMethods(t *testing.T) {
	tool := NewTaskCreate(NewList(""))
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
