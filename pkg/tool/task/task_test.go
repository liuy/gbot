package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockRegistry implements Registry for testing.
type mockRegistry struct {
	mu    sync.Mutex
	tasks map[string]*TaskInfo
}

func newMockRegistry() *mockRegistry {
	return &mockRegistry{tasks: make(map[string]*TaskInfo)}
}

func (m *mockRegistry) Get(id string) (*TaskInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, false
	}
	// Return a copy
	cp := *t
	return &cp, true
}

func (m *mockRegistry) Kill(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("not found: %s", id)
	}
	t.Status = "killed"
	t.ExitCode = 137
	return nil
}

func (m *mockRegistry) List() []*TaskInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*TaskInfo
	for _, t := range m.tasks {
		cp := *t
		result = append(result, &cp)
	}
	return result
}

func (m *mockRegistry) Wait(id string) (int, error) {
	// Poll until terminal
	for i := 0; i < 50; i++ {
		m.mu.Lock()
		t, ok := m.tasks[id]
		if !ok {
			m.mu.Unlock()
			return -1, fmt.Errorf("not found: %s", id)
		}
		if isTerminal(t.Status) {
			code := t.ExitCode
			m.mu.Unlock()
			return code, nil
		}
		m.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	return -1, fmt.Errorf("timeout waiting for task %s", id)
}

func (m *mockRegistry) add(info *TaskInfo) {
	m.mu.Lock()
	m.tasks[info.ID] = info
	m.mu.Unlock()
}

// ---------------------------------------------------------------------------
// TaskOutput tests
// ---------------------------------------------------------------------------

func TestTaskOutput_CompletedTask(t *testing.T) {
	reg := newMockRegistry()
	reg.add(&TaskInfo{
		ID:       "bg-1",
		Type:     "local_bash",
		Status:   "completed",
		Command:  "echo hello",
		Output:   "hello\n",
		ExitCode: 0,
	})

	tl := NewTaskOutput(reg)
	input := json.RawMessage(`{"task_id":"bg-1"}`)
	result, err := tl.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out, ok := result.Data.(*OutputOutput)
	if !ok {
		t.Fatalf("result.Data type = %T, want *OutputOutput", result.Data)
	}
	if out.RetrievalStatus != "success" {
		t.Errorf("RetrievalStatus = %q, want success", out.RetrievalStatus)
	}
	if out.Task.Output != "hello\n" {
		t.Errorf("Output = %q, want hello\\n", out.Task.Output)
	}
	if out.Task.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.Task.ExitCode)
	}
}

func TestTaskOutput_NotFound(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	input := json.RawMessage(`{"task_id":"nonexistent"}`)
	_, err := tl.Call(context.Background(), input, nil)
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}

func TestTaskOutput_BlockWait(t *testing.T) {
	reg := newMockRegistry()
	reg.add(&TaskInfo{
		ID:       "bg-2",
		Type:     "local_bash",
		Status:   "running",
		Command:  "sleep 1",
	})

	// Complete the task after a short delay
	go func() {
		time.Sleep(200 * time.Millisecond)
		reg.mu.Lock()
		reg.tasks["bg-2"].Status = "completed"
		reg.tasks["bg-2"].Output = "done\n"
		reg.tasks["bg-2"].ExitCode = 0
		reg.mu.Unlock()
	}()

	tl := NewTaskOutput(reg)
	block := true
	input, _ := json.Marshal(OutputInput{TaskID: "bg-2", Block: &block, Timeout: 5000})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*OutputOutput)
	if out.RetrievalStatus != "success" {
		t.Errorf("RetrievalStatus = %q, want success", out.RetrievalStatus)
	}
	if out.Task.Status != "completed" {
		t.Errorf("Status = %q, want completed", out.Task.Status)
	}
}

func TestTaskOutput_BlockTimeout(t *testing.T) {
	reg := newMockRegistry()
	reg.add(&TaskInfo{
		ID:     "bg-3",
		Type:   "local_bash",
		Status: "running",
	})

	tl := NewTaskOutput(reg)
	block := true
	input, _ := json.Marshal(OutputInput{TaskID: "bg-3", Block: &block, Timeout: 200})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*OutputOutput)
	if out.RetrievalStatus != "timeout" {
		t.Errorf("RetrievalStatus = %q, want timeout", out.RetrievalStatus)
	}
}

func TestTaskOutput_NotReady(t *testing.T) {
	reg := newMockRegistry()
	reg.add(&TaskInfo{
		ID:     "bg-4",
		Type:   "local_bash",
		Status: "running",
		Output: "partial output",
	})

	tl := NewTaskOutput(reg)
	block := false
	input, _ := json.Marshal(OutputInput{TaskID: "bg-4", Block: &block})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*OutputOutput)
	if out.RetrievalStatus != "not_ready" {
		t.Errorf("RetrievalStatus = %q, want not_ready", out.RetrievalStatus)
	}
	if out.Task.Output != "partial output" {
		t.Errorf("Output = %q, want partial output", out.Task.Output)
	}
}

func TestTaskOutput_EmptyTaskID(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"task_id":""}`), nil)
	if err == nil {
		t.Error("expected error for empty task_id")
	}
}

func TestTaskOutput_InvalidJSON(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`invalid`), nil)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// TaskStop tests
// ---------------------------------------------------------------------------

func TestTaskStop_Success(t *testing.T) {
	reg := newMockRegistry()
	reg.add(&TaskInfo{
		ID:     "bg-10",
		Type:   "local_bash",
		Status: "running",
		Command: "sleep 60",
	})

	tl := NewTaskStop(reg)
	input := json.RawMessage(`{"task_id":"bg-10"}`)
	result, err := tl.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out, ok := result.Data.(*StopOutput)
	if !ok {
		t.Fatalf("result.Data type = %T, want *StopOutput", result.Data)
	}
	if out.TaskID != "bg-10" {
		t.Errorf("TaskID = %q, want bg-10", out.TaskID)
	}
	if out.TaskType != "local_bash" {
		t.Errorf("TaskType = %q, want local_bash", out.TaskType)
	}
}

func TestTaskStop_NotFound(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"task_id":"nonexistent"}`), nil)
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}

func TestTaskStop_NotRunning(t *testing.T) {
	reg := newMockRegistry()
	reg.add(&TaskInfo{
		ID:       "bg-11",
		Type:     "local_bash",
		Status:   "completed",
		ExitCode: 0,
	})

	tl := NewTaskStop(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"task_id":"bg-11"}`), nil)
	if err == nil {
		t.Error("expected error for non-running task")
	}
}

func TestTaskStop_EmptyTaskID(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Error("expected error for empty task_id")
	}
}

func TestTaskStop_InvalidJSON(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`invalid`), nil)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// Tool interface compliance
// ---------------------------------------------------------------------------

func TestTaskOutput_ImplementsTool(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	if tl.Name() != "TaskOutput" {
		t.Errorf("Name = %q, want TaskOutput", tl.Name())
	}
	if !tl.IsReadOnly(nil) {
		t.Error("TaskOutput should be read-only")
	}
	if !tl.IsConcurrencySafe(nil) {
		t.Error("TaskOutput should be concurrency-safe")
	}
}

func TestTaskStop_ImplementsTool(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	if tl.Name() != "TaskStop" {
		t.Errorf("Name = %q, want TaskStop", tl.Name())
	}
	aliases := tl.Aliases()
	found := false
	for _, a := range aliases {
		if a == "KillShell" {
			found = true
		}
	}
	if !found {
		t.Error("TaskStop should have KillShell alias")
	}
}

func TestTaskStop_Description(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	desc, err := tl.Description(json.RawMessage(`{"task_id":"bg-1"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestTaskOutput_Description(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	desc, err := tl.Description(json.RawMessage(`{"task_id":"bg-1"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestTaskOutput_RenderResult(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)

	// Test rendering with output
	result := tl.RenderResult(&OutputOutput{
		RetrievalStatus: "success",
		Task: &TaskInfo{
			ID:     "bg-1",
			Status: "completed",
			Output: "hello world",
		},
	})
	if result == "" {
		t.Error("RenderResult should not be empty")
	}
}

func TestTaskStop_RenderResult(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)

	result := tl.RenderResult(&StopOutput{
		Message: "Successfully stopped task: bg-1 (sleep 60)",
		TaskID:  "bg-1",
	})
	if result != "Successfully stopped task: bg-1 (sleep 60)" {
		t.Errorf("RenderResult = %q, want success message", result)
	}
}

// ---------------------------------------------------------------------------
// Coverage: description fallback + render fallback
// ---------------------------------------------------------------------------

func TestTaskOutput_DescriptionInvalidJSON(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	desc, err := tl.Description(json.RawMessage(`invalid`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Get task output" {
		t.Errorf("Description fallback = %q, want Get task output", desc)
	}
}

func TestTaskOutput_DescriptionEmptyCommand(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	desc, err := tl.Description(json.RawMessage(`{"task_id":""}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "TaskOutput()" {
		t.Errorf("Description = %q, want TaskOutput()", desc)
	}
}

func TestTaskStop_DescriptionInvalidJSON(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	desc, err := tl.Description(json.RawMessage(`invalid`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Stop a running background task" {
		t.Errorf("Description fallback = %q, want Stop a running background task", desc)
	}
}

func TestTaskOutput_RenderResultFallback(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	// Pass wrong type to hit the fallback path
	result := tl.RenderResult("not an OutputOutput")
	if result != "not an OutputOutput" {
		t.Errorf("RenderResult fallback = %q, want not an OutputOutput", result)
	}
}

func TestTaskOutput_RenderResultNilTask(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	result := tl.RenderResult(&OutputOutput{
		RetrievalStatus: "timeout",
		Task:            nil,
	})
	if result != "Status: timeout" {
		t.Errorf("RenderResult nil task = %q, want Status: timeout", result)
	}
}

func TestTaskOutput_RenderResultNoOutput(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	result := tl.RenderResult(&OutputOutput{
		RetrievalStatus: "success",
		Task: &TaskInfo{
			ID:     "bg-1",
			Status: "completed",
		},
	})
	if result == "" {
		t.Error("RenderResult should not be empty")
	}
}

func TestTaskStop_RenderResultFallback(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	result := tl.RenderResult("not a StopOutput")
	if result != "not a StopOutput" {
		t.Errorf("RenderResult fallback = %q, want not a StopOutput", result)
	}
}

func TestTaskStop_KillError(t *testing.T) {
	reg := newMockRegistry()
	// Don't add task — Kill will fail via Get first
	// Actually we need a task that exists but Kill fails.
	// Use a custom mock for this.
	tl := NewTaskStop(reg)
	// Task doesn't exist — tests the "not found" before Kill
	_, err := tl.Call(context.Background(), json.RawMessage(`{"task_id":"missing"}`), nil)
	if err == nil {
		t.Error("expected error for missing task")
	}
}

// killErrorRegistry returns an error from Kill.
type killErrorRegistry struct {
	*mockRegistry
}

func (k *killErrorRegistry) Kill(id string) error {
	return fmt.Errorf("kill failed: permission denied")
}

func TestTaskStop_KillReturnsError(t *testing.T) {
	base := newMockRegistry()
	base.add(&TaskInfo{
		ID:      "bg-20",
		Type:    "local_bash",
		Status:  "running",
		Command: "sleep 60",
	})
	reg := &killErrorRegistry{mockRegistry: base}

	tl := NewTaskStop(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"task_id":"bg-20"}`), nil)
	if err == nil {
		t.Fatal("expected error when Kill fails")
	}
	if !strings.Contains(err.Error(), "kill failed") {
		t.Errorf("error = %q, want kill failed message", err.Error())
	}
}

func TestTaskStop_EmptyCommandUsesDescription(t *testing.T) {
	reg := newMockRegistry()
	reg.add(&TaskInfo{
		ID:          "bg-21",
		Type:        "local_bash",
		Status:      "running",
		Command:     "",
		Description: "my custom task",
	})

	tl := NewTaskStop(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"task_id":"bg-21"}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*StopOutput)
	if out.Command != "my custom task" {
		t.Errorf("Command = %q, want my custom task", out.Command)
	}
	if !strings.Contains(out.Message, "my custom task") {
		t.Errorf("Message = %q, want description in message", out.Message)
	}
}

func TestTaskOutput_ContextCancelled(t *testing.T) {
	reg := newMockRegistry()
	reg.add(&TaskInfo{
		ID:     "bg-30",
		Type:   "local_bash",
		Status: "running",
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel context immediately so the polling loop hits ctx.Done()
	cancel()

	tl := NewTaskOutput(reg)
	block := true
	input, _ := json.Marshal(OutputInput{TaskID: "bg-30", Block: &block, Timeout: 5000})
	_, err := tl.Call(ctx, json.RawMessage(input), nil)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestTaskOutput_InputSchema(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskOutput(reg)
	schema := tl.InputSchema()
	if len(schema) == 0 {
		t.Error("InputSchema should not be empty")
	}
}

func TestTaskStop_InputSchema(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	schema := tl.InputSchema()
	if len(schema) == 0 {
		t.Error("InputSchema should not be empty")
	}
}

func TestTaskStop_IsConcurrencySafe(t *testing.T) {
	reg := newMockRegistry()
	tl := NewTaskStop(reg)
	if !tl.IsConcurrencySafe(nil) {
		t.Error("TaskStop should be concurrency-safe")
	}
}
