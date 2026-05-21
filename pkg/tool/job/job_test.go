package job

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
	mu       sync.Mutex
	tasks    map[string]*JobInfo
	notifyCh chan string // signals when a task becomes terminal
}

func newMockRegistry() *mockRegistry {
	return &mockRegistry{
		tasks:    make(map[string]*JobInfo),
		notifyCh: make(chan string, 16),
	}
}

func (m *mockRegistry) Get(id string) (*JobInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, false
	}
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

func (m *mockRegistry) List() []*JobInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*JobInfo
	for _, t := range m.tasks {
		cp := *t
		result = append(result, &cp)
	}
	return result
}

func (m *mockRegistry) Wait(id string) (int, error) {
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

	for {
		select {
		case notifiedID := <-m.notifyCh:
			if notifiedID != id {
				continue
			}
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
		case <-time.After(5 * time.Second):
			return -1, fmt.Errorf("timeout waiting for task %s", id)
		}
	}
}

func (m *mockRegistry) add(info *JobInfo) {
	m.mu.Lock()
	m.tasks[info.ID] = info
	m.mu.Unlock()
}

// ---------------------------------------------------------------------------
// JobInfo tests
// ---------------------------------------------------------------------------

func TestJobInfo_AgentFields(t *testing.T) {
	info := &JobInfo{
		ID:         "fork-1",
		Type:       "local_agent",
		Status:     "completed",
		ExitCode:   0,
		AgentType:  "fork",
		Tokens:     5000,
		DurationMs: 1234,
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"agent_type":"fork"`) {
		t.Errorf("JSON should contain agent_type, got: %s", data)
	}
	if !strings.Contains(string(data), `"tokens":5000`) {
		t.Errorf("JSON should contain tokens, got: %s", data)
	}
	if !strings.Contains(string(data), `"duration_ms":1234`) {
		t.Errorf("JSON should contain duration_ms, got: %s", data)
	}

	var parsed JobInfo
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed.AgentType != "fork" {
		t.Errorf("AgentType = %q, want fork", parsed.AgentType)
	}
	if parsed.Tokens != 5000 {
		t.Errorf("Tokens = %d, want 5000", parsed.Tokens)
	}
	if parsed.DurationMs != 1234 {
		t.Errorf("DurationMs = %d, want 1234", parsed.DurationMs)
	}

	// Old JSON without agent fields should unmarshal with zero values
	oldJSON := `{"job_id":"bg-1","job_type":"local_bash","status":"completed"}`
	var old JobInfo
	if err := json.Unmarshal([]byte(oldJSON), &old); err != nil {
		t.Fatalf("Unmarshal old JSON: %v", err)
	}
	if old.AgentType != "" {
		t.Errorf("old AgentType = %q, want empty", old.AgentType)
	}
	if old.Tokens != 0 {
		t.Errorf("old Tokens = %d, want 0", old.Tokens)
	}
}

func TestJobInfo_TaskTypeRenamedToJobType(t *testing.T) {
	info := &JobInfo{
		ID:     "bg-1",
		Type:   "local_bash",
		Status: "completed",
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "task_type") {
		t.Errorf("JSON should not contain task_type, got: %s", data)
	}
	if !strings.Contains(string(data), "job_type") {
		t.Errorf("JSON should contain job_type, got: %s", data)
	}
}

// ---------------------------------------------------------------------------
// Unified Job tool tests
// ---------------------------------------------------------------------------

// killErrorRegistry returns an error from Kill.
type killErrorRegistry struct {
	*mockRegistry
}

func (k *killErrorRegistry) Kill(id string) error {
	return fmt.Errorf("kill failed: permission denied")
}

func TestJob_PollCompleted(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:       "bg-1",
		Type:     "local_bash",
		Status:   "completed",
		Command:  "echo hello",
		Output:   "hello\n",
		ExitCode: 0,
	})

	tl := NewJob(reg)
	input := json.RawMessage(`{"poll":"bg-1"}`)
	result, err := tl.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out, ok := result.Data.(*JobOutput)
	if !ok {
		t.Fatalf("result.Data type = %T, want *JobOutput", result.Data)
	}
	if out.Poll == nil {
		t.Fatal("Poll result is nil")
	}
	if out.Poll.RetrievalStatus != "success" {
		t.Errorf("RetrievalStatus = %q, want success", out.Poll.RetrievalStatus)
	}
	if out.Poll.Task.Output != "hello\n" {
		t.Errorf("Output = %q, want hello\\n", out.Poll.Task.Output)
	}
	if out.Poll.Task.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", out.Poll.Task.ExitCode)
	}
	if out.Stop != nil {
		t.Error("Stop result should be nil when only poll requested")
	}
}

func TestJob_PollNotFound(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"poll":"nonexistent"}`), nil)
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
	if !strings.Contains(err.Error(), "no job found") {
		t.Errorf("error = %q, want containing 'no job found'", err.Error())
	}
}

func TestJob_PollBlockWait(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:      "bg-2",
		Type:    "local_bash",
		Status:  "running",
		Command: "sleep 1",
	})

	go func() {
		reg.mu.Lock()
		reg.tasks["bg-2"].Status = "completed"
		reg.tasks["bg-2"].Output = "done\n"
		reg.tasks["bg-2"].ExitCode = 0
		reg.mu.Unlock()
		reg.notifyCh <- "bg-2"
	}()

	tl := NewJob(reg)
	input, _ := json.Marshal(JobInput{Poll: "bg-2", Block: new(true), Timeout: 5000})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.Poll.RetrievalStatus != "success" {
		t.Errorf("RetrievalStatus = %q, want success", out.Poll.RetrievalStatus)
	}
	if out.Poll.Task.Status != "completed" {
		t.Errorf("Status = %q, want completed", out.Poll.Task.Status)
	}
}

func TestJob_PollBlockTimeout(t *testing.T) {
	// NOT parallel: modifies global timeAfter variable
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:     "bg-3",
		Type:   "local_bash",
		Status: "running",
	})

	// Override timeAfter to return immediately so timeout fires right away
	saved := timeAfter
	timeAfter = func(ms int) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	defer func() { timeAfter = saved }()

	tl := NewJob(reg)
	input, _ := json.Marshal(JobInput{Poll: "bg-3", Block: new(true), Timeout: 1})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.Poll.RetrievalStatus != "timeout" {
		t.Errorf("RetrievalStatus = %q, want timeout", out.Poll.RetrievalStatus)
	}
}

func TestJob_PollNotReady(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:     "bg-4",
		Type:   "local_bash",
		Status: "running",
		Output: "partial output",
	})

	tl := NewJob(reg)
	input, _ := json.Marshal(JobInput{Poll: "bg-4", Block: new(false)})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.Poll.RetrievalStatus != "not_ready" {
		t.Errorf("RetrievalStatus = %q, want not_ready", out.Poll.RetrievalStatus)
	}
	if out.Poll.Task.Output != "partial output" {
		t.Errorf("Output = %q, want partial output", out.Poll.Task.Output)
	}
}

func TestJob_StopSuccess(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:      "bg-10",
		Type:    "local_bash",
		Status:  "running",
		Command: "sleep 60",
	})

	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"stop":"bg-10"}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.Stop == nil {
		t.Fatal("Stop result is nil")
	}
	if out.Stop.Status != "killed" {
		t.Errorf("Status = %q, want killed", out.Stop.Status)
	}
	if out.Stop.JobID != "bg-10" {
		t.Errorf("JobID = %q, want bg-10", out.Stop.JobID)
	}
	if out.Stop.JobType != "local_bash" {
		t.Errorf("JobType = %q, want local_bash", out.Stop.JobType)
	}
	if out.Stop.Command != "sleep 60" {
		t.Errorf("Command = %q, want sleep 60", out.Stop.Command)
	}
	if out.Poll != nil {
		t.Error("Poll result should be nil when only stop requested")
	}
}

func TestJob_StopNotFound(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"stop":"nonexistent"}`), nil)
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
	if !strings.Contains(err.Error(), "no job found") {
		t.Errorf("error = %q, want containing 'no job found'", err.Error())
	}
}

func TestJob_StopNotRunning(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:       "bg-11",
		Type:     "local_bash",
		Status:   "completed",
		ExitCode: 0,
	})

	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"stop":"bg-11"}`), nil)
	if err == nil {
		t.Error("expected error for non-running job")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want containing 'not running'", err.Error())
	}
}

func TestJob_StopKillError(t *testing.T) {
	t.Parallel()
	base := newMockRegistry()
	base.add(&JobInfo{
		ID:      "bg-20",
		Type:    "local_bash",
		Status:  "running",
		Command: "sleep 60",
	})
	reg := &killErrorRegistry{mockRegistry: base}

	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"stop":"bg-20"}`), nil)
	if err == nil {
		t.Fatal("expected error when Kill fails")
	}
	if !strings.Contains(err.Error(), "kill failed") {
		t.Errorf("error = %q, want containing 'kill failed'", err.Error())
	}
}

func TestJob_CombinedPollStop(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:       "bg-1",
		Type:     "local_bash",
		Status:   "completed",
		Command:  "echo hello",
		Output:   "hello\n",
		ExitCode: 0,
	})
	reg.add(&JobInfo{
		ID:      "bg-10",
		Type:    "local_bash",
		Status:  "running",
		Command: "sleep 60",
	})

	tl := NewJob(reg)
	input, _ := json.Marshal(JobInput{Poll: "bg-1", Stop: "bg-10"})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.Poll == nil {
		t.Error("Poll result should not be nil")
	} else if out.Poll.RetrievalStatus != "success" {
		t.Errorf("Poll RetrievalStatus = %q, want success", out.Poll.RetrievalStatus)
	}
	if out.Stop == nil {
		t.Error("Stop result should not be nil")
	} else if out.Stop.Status != "killed" {
		t.Errorf("Stop Status = %q, want killed", out.Stop.Status)
	}
}

func TestJob_CombinedPollFailsStopNotExecuted(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:      "bg-10",
		Type:    "local_bash",
		Status:  "running",
		Command: "sleep 60",
	})

	// Poll a nonexistent job should fail before stop executes
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"poll":"nonexistent","stop":"bg-10"}`), nil)
	if err == nil {
		t.Error("expected error from failed poll")
	}
	if !strings.Contains(err.Error(), "no job found") {
		t.Errorf("error = %q, want containing 'no job found'", err.Error())
	}

	// Verify stop was NOT executed (bg-10 should still be running)
	info, _ := reg.Get("bg-10")
	if info.Status != "running" {
		t.Errorf("Stop was executed despite poll failure, status = %q", info.Status)
	}
}

func TestJob_NeitherPollNorStop(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Error("expected error when neither poll nor stop provided")
	}
	if !strings.Contains(err.Error(), "at least one of 'poll', 'stop', or 'list'") {
		t.Errorf("error = %q, want containing 'at least one'", err.Error())
	}
}

func TestJob_PollEmptyJobID(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"poll":""}`), nil)
	if err == nil {
		t.Error("expected error for empty poll")
	}
	if !strings.Contains(err.Error(), "at least one of 'poll', 'stop', or 'list'") {
		t.Errorf("error = %q, want containing 'at least one'", err.Error())
	}
}

func TestJob_StopEmptyJobID(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"stop":""}`), nil)
	if err == nil {
		t.Error("expected error for empty stop")
	}
	if !strings.Contains(err.Error(), "at least one of 'poll', 'stop', or 'list'") {
		t.Errorf("error = %q, want containing 'at least one'", err.Error())
	}
}

func TestJob_InvalidJSON(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`invalid`), nil)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want containing 'parse input'", err.Error())
	}
}

func TestJob_ContextCancelled(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:     "bg-30",
		Type:   "local_bash",
		Status: "running",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tl := NewJob(reg)
	input, _ := json.Marshal(JobInput{Poll: "bg-30", Block: new(true), Timeout: 5000})
	_, err := tl.Call(ctx, json.RawMessage(input), nil)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("error = %q, want containing 'context'", err.Error())
	}
}

func TestJob_Name(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if tl.Name() != "Job" {
		t.Errorf("Name = %q, want Job", tl.Name())
	}
}

func TestJob_Aliases(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	aliases := tl.Aliases()
	hasKillShell := false
	for _, a := range aliases {
		if a == "KillShell" {
			hasKillShell = true
		}
	}
	if !hasKillShell {
		t.Error("Aliases should include KillShell")
	}
}

func TestJob_DescriptionPoll(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`{"poll":"bg-1"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Poll bg-1" {
		t.Errorf("Description = %q, want 'Poll bg-1'", desc)
	}
}

func TestJob_DescriptionStop(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`{"stop":"bg-2"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Stop bg-2" {
		t.Errorf("Description = %q, want 'Stop bg-2'", desc)
	}
}

func TestJob_DescriptionBoth(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`{"poll":"bg-1","stop":"bg-2"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Poll bg-1, Stop bg-2" {
		t.Errorf("Description = %q, want 'Poll bg-1, Stop bg-2'", desc)
	}
}

func TestJob_DescriptionInvalidJSON(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`invalid`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Manage job" {
		t.Errorf("Description fallback = %q, want 'Manage job'", desc)
	}
}

func TestJob_DescriptionEmpty(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Manage job" {
		t.Errorf("Description = %q, want 'Manage job'", desc)
	}
}

func TestJob_InputSchema(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	schema := tl.InputSchema()
	if len(schema) == 0 {
		t.Fatal("InputSchema should not be empty")
	}
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	props, ok := obj["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing 'properties' object")
	}
	for _, key := range []string{"poll", "stop", "block", "timeout"} {
		if _, exists := props[key]; !exists {
			t.Errorf("InputSchema properties missing '%s'", key)
		}
	}
}

func TestJob_ReadOnlyPollOnly(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if !tl.IsReadOnly(json.RawMessage(`{"poll":"bg-1"}`)) {
		t.Error("Poll-only should be read-only")
	}
}

func TestJob_NotReadOnlyWithStop(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if tl.IsReadOnly(json.RawMessage(`{"stop":"bg-1"}`)) {
		t.Error("With stop should not be read-only")
	}
}

func TestJob_NotReadOnlyBoth(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if tl.IsReadOnly(json.RawMessage(`{"poll":"bg-1","stop":"bg-10"}`)) {
		t.Error("Combined poll+stop should not be read-only")
	}
}

func TestJob_IsConcurrencySafe(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if !tl.IsConcurrencySafe(nil) {
		t.Error("Job should be concurrency-safe")
	}
}

func TestJob_RenderResultPoll(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		Poll: &PollResult{
			RetrievalStatus: "success",
			Task: &JobInfo{
				ID:       "bg-1",
				Status:   "completed",
				Output:   "hello world",
				ExitCode: 0,
			},
		},
	})
	if !strings.Contains(result, "Poll bg-1: success (exit: 0)") {
		t.Errorf("RenderResult = %q, want containing 'Poll bg-1: success (exit: 0)'", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("RenderResult = %q, want containing 'hello world'", result)
	}
}

func TestJob_RenderResultStop(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		Stop: &StopResult{
			JobID:   "bg-10",
			Status:  "killed",
			JobType: "local_bash",
			Command: "sleep 60",
		},
	})
	want := "Stop bg-10: killed (was: sleep 60)"
	if result != want {
		t.Errorf("RenderResult = %q, want %q", result, want)
	}
}

func TestJob_RenderResultCombined(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		Poll: &PollResult{
			RetrievalStatus: "success",
			Task: &JobInfo{
				ID:       "bg-1",
				Status:   "completed",
				Output:   "hello world",
				ExitCode: 0,
			},
		},
		Stop: &StopResult{
			JobID:   "bg-10",
			Status:  "killed",
			JobType: "local_bash",
			Command: "sleep 60",
		},
	})
	if !strings.Contains(result, "Poll bg-1: success (exit: 0)") {
		t.Errorf("RenderResult missing poll section, got: %q", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("RenderResult missing output, got: %q", result)
	}
	if !strings.Contains(result, "Stop bg-10: killed (was: sleep 60)") {
		t.Errorf("RenderResult missing stop section, got: %q", result)
	}
}

func TestJob_RenderResultFallback(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult("not a JobOutput")
	if result != "not a JobOutput" {
		t.Errorf("RenderResult fallback = %q, want 'not a JobOutput'", result)
	}
}

func TestJob_RenderResultPollTimeout(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		Poll: &PollResult{
			RetrievalStatus: "timeout",
			Task: &JobInfo{
				ID:     "bg-3",
				Status: "running",
			},
		},
	})
	want := "Poll bg-3: timeout"
	if result != want {
		t.Errorf("RenderResult = %q, want %q", result, want)
	}
}

func TestJob_RenderResultPollNilTask(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		Poll: &PollResult{
			RetrievalStatus: "success",
			Task:            nil,
		},
	})
	if result != "Poll: success" {
		t.Errorf("RenderResult nil task = %q, want 'Poll: success'", result)
	}
}

func TestJob_StopEmptyCommandUsesDescription(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:          "bg-21",
		Type:        "local_bash",
		Status:      "running",
		Command:     "",
		Description: "my custom task",
	})

	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"stop":"bg-21"}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.Stop.Command != "my custom task" {
		t.Errorf("Command = %q, want 'my custom task'", out.Stop.Command)
	}
}

func TestJob_StopEmptyJobAndEmptyCommandUsesJobID(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:          "bg-22",
		Type:        "local_bash",
		Status:      "running",
		Command:     "",
		Description: "",
	})

	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"stop":"bg-22"}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	rendered := tl.RenderResult(&JobOutput{
		Stop: &StopResult{
			JobID:   out.Stop.JobID,
			Status:  "killed",
			JobType: "local_bash",
			Command: out.Stop.Command,
		},
	})
	if !strings.Contains(rendered, "bg-22") {
		t.Errorf("RenderResult = %q, want containing job ID", rendered)
	}
}

func TestJob_RenderResultPollNoOutput(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		Poll: &PollResult{
			RetrievalStatus: "success",
			Task: &JobInfo{
				ID:       "bg-1",
				Status:   "completed",
				ExitCode: 0,
			},
		},
	})
	want := "Poll bg-1: success (exit: 0)"
	if result != want {
		t.Errorf("RenderResult = %q, want %q", result, want)
	}
}

func TestJob_IsReadOnlyInvalidJSON(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if tl.IsReadOnly(json.RawMessage(`invalid`)) {
		t.Error("IsReadOnly with invalid JSON should return false")
	}
}

func TestJob_ExecutePollEmptyJobID(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	_, err := executePoll(context.Background(), reg, "", nil, 0)
	if err == nil {
		t.Error("expected error for empty job_id")
	}
	if !strings.Contains(err.Error(), "job_id is required") {
		t.Errorf("error = %q, want containing 'job_id is required'", err.Error())
	}
}

func TestJob_ExecuteStopEmptyJobID(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	_, err := executeStop(reg, "")
	if err == nil {
		t.Error("expected error for empty job_id")
	}
	if !strings.Contains(err.Error(), "job_id is required") {
		t.Errorf("error = %q, want containing 'job_id is required'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

func TestJob_ListSuccess(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{ID: "bg-1", Type: "local_bash", Status: "running", Command: "sleep 10"})
	reg.add(&JobInfo{ID: "bg-2", Type: "local_bash", Status: "completed", Command: "echo hi", ExitCode: 0})

	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"list":true}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.List == nil {
		t.Fatal("List result is nil")
	}
	if len(out.List.Jobs) != 2 {
		t.Fatalf("List.Jobs count = %d, want 2", len(out.List.Jobs))
	}

	ids := make(map[string]bool)
	for _, j := range out.List.Jobs {
		ids[j.ID] = true
	}
	if !ids["bg-1"] || !ids["bg-2"] {
		t.Errorf("List.Jobs missing expected IDs, got: %v", ids)
	}
}

func TestJob_ListEmpty(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"list":true}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.List == nil {
		t.Fatal("List result should not be nil even when empty")
	}
	if len(out.List.Jobs) != 0 {
		t.Errorf("List.Jobs count = %d, want 0", len(out.List.Jobs))
	}
}

func TestJob_ListIsReadOnly(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if !tl.IsReadOnly(json.RawMessage(`{"list":true}`)) {
		t.Error("List-only should be read-only")
	}
}

func TestJob_ListWithStopIsNotReadOnly(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if tl.IsReadOnly(json.RawMessage(`{"list":true,"stop":"bg-1"}`)) {
		t.Error("List+Stop should not be read-only")
	}
}

func TestJob_ListWithPollAndStop(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{ID: "bg-1", Type: "local_bash", Status: "completed", Command: "echo hi", Output: "hi\n", ExitCode: 0})
	reg.add(&JobInfo{ID: "bg-2", Type: "local_bash", Status: "running", Command: "sleep 60"})

	tl := NewJob(reg)
	input, _ := json.Marshal(JobInput{List: true, Poll: "bg-1", Stop: "bg-2"})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.List == nil {
		t.Error("List result should not be nil")
	}
	if out.Poll == nil {
		t.Error("Poll result should not be nil")
	}
	if out.Stop == nil {
		t.Error("Stop result should not be nil")
	}
	if len(out.List.Jobs) != 2 {
		t.Errorf("List.Jobs count = %d, want 2", len(out.List.Jobs))
	}
}

func TestJob_DescriptionList(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`{"list":true}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "List jobs" {
		t.Errorf("Description = %q, want 'List jobs'", desc)
	}
}

func TestJob_DescriptionListWithPoll(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`{"list":true,"poll":"bg-1"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "List jobs, Poll bg-1" {
		t.Errorf("Description = %q, want 'List jobs, Poll bg-1'", desc)
	}
}

func TestJob_DescriptionListWithStop(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`{"list":true,"stop":"bg-2"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "List jobs, Stop bg-2" {
		t.Errorf("Description = %q, want 'List jobs, Stop bg-2'", desc)
	}
}

func TestJob_InputSchemaIncludesList(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	schema := tl.InputSchema()
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	props, ok := obj["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing 'properties' object")
	}
	for _, key := range []string{"poll", "stop", "list", "block", "timeout"} {
		if _, exists := props[key]; !exists {
			t.Errorf("InputSchema properties missing '%s'", key)
		}
	}
}

func TestJob_RenderResultListEmpty(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		List: &ListResult{Jobs: nil},
	})
	if result != "No jobs" {
		t.Errorf("RenderResult empty list = %q, want 'No jobs'", result)
	}
}

func TestJob_RenderResultListWithJobs(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		List: &ListResult{
			Jobs: []*JobInfo{
				{ID: "bg-1", Status: "running", Command: "sleep 10"},
				{ID: "bg-2", Status: "completed", Command: "echo hi"},
			},
		},
	})
	if !strings.Contains(result, "bg-1 [running] sleep 10") {
		t.Errorf("RenderResult = %q, want containing 'bg-1 [running] sleep 10'", result)
	}
	if !strings.Contains(result, "bg-2 [completed] echo hi") {
		t.Errorf("RenderResult = %q, want containing 'bg-2 [completed] echo hi'", result)
	}
}

func TestJob_RenderResultListFallsBackToDescription(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		List: &ListResult{
			Jobs: []*JobInfo{
				{ID: "bg-5", Status: "running", Description: "my agent task"},
			},
		},
	})
	if !strings.Contains(result, "bg-5 [running] my agent task") {
		t.Errorf("RenderResult = %q, want containing 'bg-5 [running] my agent task'", result)
	}
}

func TestJob_RenderResultListFallsBackToID(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	result := tl.RenderResult(&JobOutput{
		List: &ListResult{
			Jobs: []*JobInfo{
				{ID: "bg-6", Status: "running"},
			},
		},
	})
	if !strings.Contains(result, "bg-6 [running] bg-6") {
		t.Errorf("RenderResult = %q, want containing 'bg-6 [running] bg-6'", result)
	}
}
