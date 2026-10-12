package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
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
	if t == nil {
		// Models a registry that reports found=true with a nil snapshot,
		// which executePoll's ticker loop guards against.
		return nil, true
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
		Type:       "agent",
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
}

func TestJobInfo_TypeJSONKey(t *testing.T) {
	info := &JobInfo{
		ID:     "bg-1",
		Type:   "bash",
		Status: "completed",
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "task_type") || strings.Contains(string(data), "job_type") {
		t.Errorf("JSON must use the unified type key, got: %s", data)
	}
	if !strings.Contains(string(data), `"type":"bash"`) {
		t.Errorf("JSON should contain \"type\":\"bash\", got: %s", data)
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
		Type:     "bash",
		Status:   "completed",
		Command:  "echo hello",
		Output:   "hello\n",
		ExitCode: 0,
	})

	tl := NewJob(reg)
	input := json.RawMessage(`{"action":"poll","job_id":"bg-1"}`)
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
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"poll","job_id":"nonexistent"}`), nil)
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
		Type:    "bash",
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
	input, _ := json.Marshal(JobInput{Action: "poll", JobID: "bg-2", Block: new(true), Timeout: 5000})
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
		Type:   "bash",
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
	input, _ := json.Marshal(JobInput{Action: "poll", JobID: "bg-3", Block: new(true), Timeout: 1})
	result, err := tl.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	if out.Poll.RetrievalStatus != "timeout" {
		t.Fatalf("RetrievalStatus = %q, want timeout", out.Poll.RetrievalStatus)
	}
	if out.Poll.Task == nil {
		t.Fatal("timeout result must still carry the job snapshot")
	}
	if out.Poll.Task.ID != "bg-3" || out.Poll.Task.Status != "running" {
		t.Errorf("timeout task = (%q, %q), want (bg-3, running)", out.Poll.Task.ID, out.Poll.Task.Status)
	}
}

func TestJob_PollNotReady(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:     "bg-4",
		Type:   "bash",
		Status: "running",
		Output: "partial output",
	})

	tl := NewJob(reg)
	input, _ := json.Marshal(JobInput{Action: "poll", JobID: "bg-4", Block: new(false)})
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
		Type:    "bash",
		Status:  "running",
		Command: "sleep 60",
	})

	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"stop","job_id":"bg-10"}`), nil)
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
	if out.Stop.Type != "bash" {
		t.Errorf("Type = %q, want bash", out.Stop.Type)
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
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"stop","job_id":"nonexistent"}`), nil)
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
		Type:     "bash",
		Status:   "completed",
		ExitCode: 0,
	})

	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"stop","job_id":"bg-11"}`), nil)
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
		Type:    "bash",
		Status:  "running",
		Command: "sleep 60",
	})
	reg := &killErrorRegistry{mockRegistry: base}

	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"stop","job_id":"bg-20"}`), nil)
	if err == nil {
		t.Fatal("expected error when Kill fails")
	}
	if !strings.Contains(err.Error(), "kill failed") {
		t.Errorf("error = %q, want containing 'kill failed'", err.Error())
	}
}

func TestJob_PollThenStopSequentialCalls(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:       "bg-1",
		Type:     "bash",
		Status:   "completed",
		Command:  "echo hello",
		Output:   "hello\n",
		ExitCode: 0,
	})
	reg.add(&JobInfo{
		ID:      "bg-10",
		Type:    "bash",
		Status:  "running",
		Command: "sleep 60",
	})

	tl := NewJob(reg)
	pollResult, err := tl.Call(context.Background(), json.RawMessage(`{"action":"poll","job_id":"bg-1"}`), nil)
	if err != nil {
		t.Fatalf("poll Call() error: %v", err)
	}
	stopResult, err := tl.Call(context.Background(), json.RawMessage(`{"action":"stop","job_id":"bg-10"}`), nil)
	if err != nil {
		t.Fatalf("stop Call() error: %v", err)
	}

	poll := pollResult.Data.(*JobOutput)
	if poll.Poll == nil {
		t.Error("Poll result should not be nil")
	} else if poll.Poll.RetrievalStatus != "success" {
		t.Errorf("Poll RetrievalStatus = %q, want success", poll.Poll.RetrievalStatus)
	}
	if poll.Stop != nil {
		t.Error("Poll-only call should not populate Stop")
	}
	if poll.List != nil {
		t.Error("Poll-only call should not populate List")
	}

	stop := stopResult.Data.(*JobOutput)
	if stop.Stop == nil {
		t.Error("Stop result should not be nil")
	} else if stop.Stop.Status != "killed" {
		t.Errorf("Stop Status = %q, want killed", stop.Stop.Status)
	}
	if stop.Poll != nil {
		t.Error("Stop-only call should not populate Poll")
	}
	if stop.List != nil {
		t.Error("Stop-only call should not populate List")
	}
}

func TestJob_MissingAction(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error when action is missing")
	}
	// Exact text: the empty-action branch must not degrade into the
	// "unknown action" branch, which would echo an empty %q.
	want := `action is required — valid actions are "list", "poll", "stop" (run {"action":"list"} to see jobs)`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestJob_UnknownAction(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"restart"}`), nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error = %q, want containing 'unknown action'", err.Error())
	}
	for _, want := range []string{"list", "poll", "stop"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want containing valid action %q", err.Error(), want)
		}
	}
}

func TestJob_PollMissingJobID(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"poll"}`), nil)
	if err == nil {
		t.Fatal("expected error when poll has no job_id")
	}
	if !strings.Contains(err.Error(), "job_id") {
		t.Errorf("error = %q, want containing 'job_id'", err.Error())
	}
	if !strings.Contains(err.Error(), `{"action":"list"}`) {
		t.Errorf("error = %q, want guidance to run list", err.Error())
	}
}

func TestJob_StopMissingJobID(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"stop"}`), nil)
	if err == nil {
		t.Fatal("expected error when stop has no job_id")
	}
	if !strings.Contains(err.Error(), "job_id") {
		t.Errorf("error = %q, want containing 'job_id'", err.Error())
	}
	if !strings.Contains(err.Error(), `{"action":"list"}`) {
		t.Errorf("error = %q, want guidance to run list", err.Error())
	}
}

// A job_id that looks like a boolean literal is not special-cased: it goes
// through the normal not-found path, which echoes the ID back.
func TestJob_PollWeirdJobIDNotSpecialCased(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"poll","job_id":"true"}`), nil)
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
	if !strings.Contains(err.Error(), "no job found with ID: true") {
		t.Errorf("error = %q, want containing 'no job found with ID: true'", err.Error())
	}
}

func TestJob_InvalidJSON(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.Call(context.Background(), json.RawMessage(`invalid`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if err.Error() != "parse input: invalid character 'i' looking for beginning of value" {
		t.Errorf("error = %q, want %q", err.Error(), "parse input: invalid character 'i' looking for beginning of value")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error %v must wrap *json.SyntaxError", err)
	}
}

func TestJob_ContextCancelled(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:     "bg-30",
		Type:   "bash",
		Status: "running",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tl := NewJob(reg)
	input, _ := json.Marshal(JobInput{Action: "poll", JobID: "bg-30", Block: new(true), Timeout: 5000})
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
	desc, err := tl.Description(json.RawMessage(`{"action":"poll","job_id":"bg-1"}`))
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
	desc, err := tl.Description(json.RawMessage(`{"action":"stop","job_id":"bg-2"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Stop bg-2" {
		t.Errorf("Description = %q, want 'Stop bg-2'", desc)
	}
}

func TestJob_DescriptionListAction(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	desc, err := tl.Description(json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "List" {
		t.Errorf("Description = %q, want 'List'", desc)
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
	for _, key := range []string{"action", "job_id", "block", "timeout"} {
		if _, exists := props[key]; !exists {
			t.Errorf("InputSchema properties missing '%s'", key)
		}
	}
	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing 'action' property object")
	}
	enum, ok := action["enum"].([]any)
	if !ok {
		t.Fatal("InputSchema 'action' missing enum")
	}
	got := make(map[string]bool, len(enum))
	for _, v := range enum {
		s, _ := v.(string)
		got[s] = true
	}
	for _, want := range []string{"list", "poll", "stop"} {
		if !got[want] {
			t.Errorf("InputSchema action enum missing %q, got: %v", want, enum)
		}
	}
	required, ok := obj["required"].([]any)
	if !ok {
		t.Fatal("InputSchema missing 'required' array")
	}
	if len(required) != 1 || required[0] != "action" {
		t.Errorf("InputSchema required = %v, want [action]", required)
	}
}

func TestJob_ReadOnlyPollOnly(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if !tl.IsReadOnly(json.RawMessage(`{"action":"poll","job_id":"bg-1"}`)) {
		t.Error("Poll-only should be read-only")
	}
}

func TestJob_NotReadOnlyWithStop(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if tl.IsReadOnly(json.RawMessage(`{"action":"stop","job_id":"bg-1"}`)) {
		t.Error("With stop should not be read-only")
	}
}

func TestJob_NotReadOnlyUnknownAction(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	if tl.IsReadOnly(json.RawMessage(`{"action":"restart"}`)) {
		t.Error("Unknown action should not be read-only")
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
			Type:    "bash",
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
			Type:    "bash",
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
		Type:        "bash",
		Status:      "running",
		Command:     "",
		Description: "my custom task",
	})

	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"stop","job_id":"bg-21"}`), nil)
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
		Type:        "bash",
		Status:      "running",
		Command:     "",
		Description: "",
	})

	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"stop","job_id":"bg-22"}`), nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	out := result.Data.(*JobOutput)
	rendered := tl.RenderResult(&JobOutput{
		Stop: &StopResult{
			JobID:   out.Stop.JobID,
			Status:  "killed",
			Type:    "bash",
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
	reg.add(&JobInfo{ID: "bg-1", Type: "bash", Status: "running", Command: "sleep 10"})
	reg.add(&JobInfo{ID: "bg-2", Type: "bash", Status: "completed", Command: "echo hi", ExitCode: 0})

	tl := NewJob(reg)
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil)
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
	result, err := tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil)
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
	if !tl.IsReadOnly(json.RawMessage(`{"action":"list"}`)) {
		t.Error("List-only should be read-only")
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
	if !strings.Contains(result, `job_id=bg-1 status=running command="sleep 10"`) {
		t.Errorf("RenderResult = %q, want containing new format", result)
	}
	if !strings.Contains(result, `job_id=bg-2 status=completed command="echo hi"`) {
		t.Errorf("RenderResult = %q, want containing new format", result)
	}
}

func TestRenderList_IncludesType(t *testing.T) {
	t.Parallel()
	got := renderListResult(&ListResult{Jobs: []*JobInfo{
		{ID: "agent-1", Type: "agent", Status: "running", Command: "research"},
		{ID: "bg-2", Type: "bash", Status: "completed", Command: "echo hi"},
	}})
	want := "job_id=agent-1 type=agent status=running command=\"research\"\n" +
		"job_id=bg-2 type=bash status=completed command=\"echo hi\""
	if got != want {
		t.Errorf("renderListResult = %q, want %q", got, want)
	}
}

// JobInfo built without Type (e.g. hand-constructed callers) keeps the old
// line format — the type token appears only when Type is set.
func TestJobListWire_EmptyTypeOmitsType(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{List: &ListResult{Jobs: []*JobInfo{
		{ID: "bg-1", Status: "running", Command: "go test ./..."},
	}}})
	want := "job_id=bg-1 status=running command=\"go test ./...\""
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
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
	if !strings.Contains(result, `job_id=bg-5 status=running command="my agent task"`) {
		t.Errorf("RenderResult = %q, want containing new format", result)
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
	if !strings.Contains(result, `job_id=bg-6 status=running command="bg-6"`) {
		t.Errorf("RenderResult = %q, want containing new format", result)
	}
}

func TestJob_RenderResult_JSONRawMessage(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	tl := NewJob(reg)
	inner := `{"poll":{"task":{"job_id":"bg-1","status":"completed","exit_code":0},"retrieval_status":"success","output":"hello world"}}`
	textBytes, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textBytes) + `}]`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult failed: %v", err)
	}
	got := tl.RenderResult(v)
	if strings.Contains(got, "[123") || strings.Contains(got, "34 117") {
		t.Errorf("RenderResult should not show raw bytes, got: %q", got)
	}
	if !strings.Contains(got, "bg-1") {
		t.Errorf("RenderResult should contain job ID, got: %q", got)
	}
}

func TestJob_DecodeResult_RejectsBareStruct(t *testing.T) {
	t.Parallel()

	reg := newMockRegistry()
	tl := NewJob(reg)
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`{"poll":{"task":{"job_id":"bg-1"}}}`))
	if err == nil {
		t.Fatal("DecodeResult must reject bare struct form")
	}
	// The block-extractor error, not the later JSON error: dropping the
	// extractor's guard would surface "unexpected end of JSON input" instead.
	want := `tool: UnmarshalSingleBlock expects array-form content, got "{\"poll\":{\"task\":{\"job_id\":\"bg-1\"}}}"`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestJob_IsTerminalStatuses(t *testing.T) {
	t.Parallel()
	// Each status pins one disjunct of isTerminal: dropping or negating any
	// comparison flips exactly one row.
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{"completed", true},
		{"failed", true},
		{"killed", true},
		{"running", false},
		{"pending", false},
		{"cancelled", false},
		{"", false},
		{"Completed", false},
	} {
		if got := isTerminal(tc.status); got != tc.want {
			t.Errorf("isTerminal(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestJob_PollTimeoutDefaultAndTickerInterval(t *testing.T) {
	// NOT parallel: swaps the package-level timeAfter/timeNewTicker vars.
	type tc struct{ in, wantMs int }
	for _, c := range []tc{{0, 30000}, {-1, 30000}, {1, 1}, {250, 250}} {
		reg := newMockRegistry()
		reg.add(&JobInfo{ID: "bg-td", Type: "bash", Status: "running"})

		var gotMs int
		var gotInterval time.Duration
		savedAfter, savedTicker := timeAfter, timeNewTicker
		timeAfter = func(ms int) <-chan time.Time {
			gotMs = ms
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		}
		timeNewTicker = func(d time.Duration) *time.Ticker {
			gotInterval = d
			return &time.Ticker{C: make(chan time.Time)}
		}

		res, err := executePoll(context.Background(), reg, "bg-td", nil, c.in)
		timeAfter, timeNewTicker = savedAfter, savedTicker

		if err != nil {
			t.Fatalf("executePoll(timeout=%d): %v", c.in, err)
		}
		if res.RetrievalStatus != "timeout" {
			t.Errorf("timeout=%d RetrievalStatus = %q, want timeout", c.in, res.RetrievalStatus)
		}
		if gotMs != c.wantMs {
			t.Errorf("executePoll(timeout=%d) armed deadline with %dms, want %dms", c.in, gotMs, c.wantMs)
		}
		if gotInterval != 100*time.Millisecond {
			t.Errorf("poll ticker interval = %v, want 100ms", gotInterval)
		}
	}
}

func TestJob_PollTickerDetectsTerminal(t *testing.T) {
	// NOT parallel: swaps the package-level timeAfter/timeNewTicker vars.
	reg := newMockRegistry()
	reg.add(&JobInfo{ID: "bg-tk", Type: "bash", Status: "running", Command: "sleep 30"})

	ticks := make(chan time.Time, 4)
	savedAfter, savedTicker := timeAfter, timeNewTicker
	timeNewTicker = func(time.Duration) *time.Ticker { return &time.Ticker{C: ticks} }
	timeAfter = func(int) <-chan time.Time { return time.After(300 * time.Millisecond) }
	defer func() { timeAfter, timeNewTicker = savedAfter, savedTicker }()

	// First tick lands while the job is still running: the loop must keep
	// waiting instead of reporting success for a running job.
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	ticks <- time.Now()
	go func() {
		// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
		time.Sleep(30 * time.Millisecond)
		reg.mu.Lock()
		reg.tasks["bg-tk"].Status = "completed"
		reg.tasks["bg-tk"].ExitCode = 0
		reg.mu.Unlock()
		// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
		ticks <- time.Now()
	}()

	res, err := executePoll(context.Background(), reg, "bg-tk", nil, 5000)
	if err != nil {
		t.Fatalf("executePoll: %v", err)
	}
	if res.RetrievalStatus != "success" {
		t.Fatalf("RetrievalStatus = %q, want success — a terminal job seen by the ticker must not fall through to the deadline", res.RetrievalStatus)
	}
	if res.Task.Status != "completed" {
		t.Errorf("Status = %q, want completed — the running-job tick must not have returned", res.Task.Status)
	}
	if res.Task.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.Task.ExitCode)
	}
}

// A terminal job answers immediately, even with block=false: the terminal
// check precedes the not_ready shortcut, so "not_ready" here is a bug.
func TestJob_PollTerminalBeatsNotReady(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{ID: "bg-t2", Type: "bash", Status: "killed", ExitCode: 137})

	res, err := executePoll(context.Background(), reg, "bg-t2", ptrFalse(), 60000)
	if err != nil {
		t.Fatalf("executePoll: %v", err)
	}
	if res.RetrievalStatus != "success" {
		t.Fatalf("RetrievalStatus = %q, want success", res.RetrievalStatus)
	}
	if res.Task == nil || res.Task.ID != "bg-t2" || res.Task.Status != "killed" {
		t.Errorf("task = %+v, want bg-t2/killed", res.Task)
	}
}

func ptrFalse() *bool { b := false; return &b }

// The deadline branch re-reads the job so the snapshot it returns is current.
func TestJob_PollDeadlineRefetchesInfo(t *testing.T) {
	// NOT parallel: swaps the package-level timeAfter/timeNewTicker vars.
	reg := newMockRegistry()
	reg.add(&JobInfo{ID: "bg-dr", Type: "bash", Status: "running"})

	savedAfter, savedTicker := timeAfter, timeNewTicker
	timeAfter = func(int) <-chan time.Time { return time.After(100 * time.Millisecond) }
	timeNewTicker = func(time.Duration) *time.Ticker { return &time.Ticker{C: make(chan time.Time)} }
	defer func() { timeAfter, timeNewTicker = savedAfter, savedTicker }()

	go func() {
		// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
		time.Sleep(30 * time.Millisecond)
		reg.mu.Lock()
		reg.tasks["bg-dr"].Status = "completed"
		reg.mu.Unlock()
	}()

	res, err := executePoll(context.Background(), reg, "bg-dr", nil, 5000)
	if err != nil {
		t.Fatalf("executePoll: %v", err)
	}
	if res.RetrievalStatus != "timeout" {
		t.Fatalf("RetrievalStatus = %q, want timeout", res.RetrievalStatus)
	}
	if res.Task == nil || res.Task.Status != "completed" {
		t.Errorf("deadline snapshot = %+v, want the refreshed completed job", res.Task)
	}
}

// A registry that answers with a nil snapshot must not be dereferenced by the
// ticker: the poll keeps waiting and ends on the deadline.
func TestJob_PollTickerNilSnapshotKeepsWaiting(t *testing.T) {
	// NOT parallel: swaps the package-level timeAfter/timeNewTicker vars.
	reg := newMockRegistry()
	reg.add(&JobInfo{ID: "bg-nl", Type: "bash", Status: "running"})

	ticks := make(chan time.Time, 4)
	savedAfter, savedTicker := timeAfter, timeNewTicker
	timeNewTicker = func(time.Duration) *time.Ticker { return &time.Ticker{C: ticks} }
	timeAfter = func(int) <-chan time.Time { return time.After(300 * time.Millisecond) }
	defer func() { timeAfter, timeNewTicker = savedAfter, savedTicker }()

	go func() {
		// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
		ticks <- time.Now() // first tick: still running
		// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
		time.Sleep(30 * time.Millisecond)
		reg.mu.Lock()
		reg.tasks["bg-nl"] = nil // registry now hands back a nil snapshot
		reg.mu.Unlock()
		// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
		ticks <- time.Now() // second tick: must not dereference nil
	}()

	res, err := executePoll(context.Background(), reg, "bg-nl", nil, 5000)
	if err != nil {
		t.Fatalf("executePoll: %v", err)
	}
	if res.RetrievalStatus != "timeout" {
		t.Errorf("RetrievalStatus = %q, want timeout", res.RetrievalStatus)
	}
	if res.Task != nil {
		t.Errorf("Task = %+v, want nil for the vanished job", res.Task)
	}
}

// The default timeAfter arms a millisecond-scale deadline: a unit mix-up would
// either return instantly or hang the poll.
func TestJob_PollRealDeadlineIsMillisecondScale(t *testing.T) {
	// NOT parallel: swaps the package-level timeNewTicker var.
	reg := newMockRegistry()
	reg.add(&JobInfo{ID: "bg-ms", Type: "bash", Status: "running"})

	savedTicker := timeNewTicker
	timeNewTicker = func(time.Duration) *time.Ticker { return &time.Ticker{C: make(chan time.Time)} }
	defer func() { timeNewTicker = savedTicker }()

	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	start := time.Now()
	res, err := executePoll(context.Background(), reg, "bg-ms", nil, 150)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("executePoll: %v", err)
	}
	if res.RetrievalStatus != "timeout" {
		t.Fatalf("RetrievalStatus = %q, want timeout", res.RetrievalStatus)
	}
	if elapsed < 100*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("150ms timeout took %v — deadline is not millisecond-scale", elapsed)
	}
}

func TestJob_ToolContractFields(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	if got := tl.InterruptBehavior(); got != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %v, want InterruptCancel", got)
	}
	if got := tool.SearchHint(tl); got != "manage background jobs -- poll output or stop a running job" {
		t.Errorf("SearchHint() = %q, want the job search hint", got)
	}
	if got := tl.Prompt(); got != jobPrompt() {
		t.Errorf("Prompt() = %q, want jobPrompt()", got)
	}
	if got := tool.IsDeferred(tl); got != false {
		t.Errorf("IsDeferred() = %v, want false", got)
	}
}

func TestJob_PromptText(t *testing.T) {
	t.Parallel()
	want := `Manages background jobs (background shell, agent, or remote session).

Actions (one per call, set via the "action" parameter):
- list: list all background jobs and their statuses
- poll: retrieve output from a running or completed job (requires job_id)
  - block=true (default): wait for job completion before returning
  - block=false: non-blocking check of current status
  - timeout: max wait time in ms (default 30000)
- stop: terminate a running job by its ID (requires job_id)

job_id values come from Job list output (e.g. "bg-1"); do not guess them.

Returns a retrieval_status for poll: success, timeout, or not_ready
Returns a status for stop: killed (with the original command/description)
Returns one line per job for list: job_id=... type=... status=... command=...

Works with all job types: background shells, async agents, and remote sessions`
	if got := jobPrompt(); got != want {
		t.Errorf("jobPrompt() = %q, want the pinned prompt", got)
	}
}

type wrappedKillRegistry struct {
	*mockRegistry
	cause error
}

func (w *wrappedKillRegistry) Kill(id string) error {
	return fmt.Errorf("registry refused: %w", w.cause)
}

func TestJob_StopKillErrorWrapsCause(t *testing.T) {
	t.Parallel()
	base := newMockRegistry()
	base.add(&JobInfo{ID: "bg-22", Type: "bash", Status: "running"})
	cause := errors.New("eperm")
	reg := &wrappedKillRegistry{mockRegistry: base, cause: cause}

	_, err := executeStop(reg, "bg-22")
	if err == nil {
		t.Fatal("executeStop must fail when Kill fails")
	}
	if !errors.Is(err, cause) {
		t.Errorf("error %v must keep the Kill cause reachable via errors.Is", err)
	}
}

func TestJob_MaxResultSize(t *testing.T) {
	t.Parallel()
	if got := NewJob(newMockRegistry()).MaxResultSize(); got != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", got)
	}
}

func TestJob_RenderListSeparatorCount(t *testing.T) {
	t.Parallel()
	// Exact strings: an off-by-one in the "i > 0" guard adds or drops a
	// newline, and removing the separator write collapses the rows.
	two := renderListResult(&ListResult{Jobs: []*JobInfo{
		{ID: "a", Status: "running", Command: "echo a"},
		{ID: "b", Status: "completed", Command: "echo b"},
	}})
	wantTwo := "job_id=a status=running command=\"echo a\"\njob_id=b status=completed command=\"echo b\""
	if two != wantTwo {
		t.Errorf("renderListResult(2 jobs) = %q, want %q", two, wantTwo)
	}
	three := renderListResult(&ListResult{Jobs: []*JobInfo{
		{ID: "a", Status: "running", Command: "a"},
		{ID: "b", Status: "running", Command: "b"},
		{ID: "c", Status: "running", Command: "c"},
	}})
	if n := strings.Count(three, "\n"); n != 2 {
		t.Errorf("renderListResult(3 jobs) has %d newlines, want 2: %q", n, three)
	}
	if strings.HasPrefix(three, "\n") {
		t.Errorf("renderListResult(3 jobs) starts with a separator: %q", three)
	}
}

func TestJob_RenderStopFallbackExact(t *testing.T) {
	t.Parallel()
	if got := renderStopResult(&StopResult{JobID: "bg-9", Command: "sleep 60"}); got != "Stop bg-9: killed (was: sleep 60)" {
		t.Errorf("renderStopResult = %q, want %q", got, "Stop bg-9: killed (was: sleep 60)")
	}
	if got := renderStopResult(&StopResult{JobID: "bg-9", Command: ""}); got != "Stop bg-9: killed (was: bg-9)" {
		t.Errorf("renderStopResult(empty command) = %q, want %q", got, "Stop bg-9: killed (was: bg-9)")
	}
}

func TestJob_RenderPollExactShapes(t *testing.T) {
	t.Parallel()
	running := renderPollResult(&PollResult{RetrievalStatus: "not_ready", Task: &JobInfo{ID: "bg-1", Status: "running", Output: "line1\nline2"}})
	if running != "Poll bg-1: not_ready\nline1\nline2" {
		t.Errorf("renderPollResult(running) = %q, want %q", running, "Poll bg-1: not_ready\nline1\nline2")
	}
	terminal := renderPollResult(&PollResult{RetrievalStatus: "success", Task: &JobInfo{ID: "bg-1", Status: "failed", ExitCode: 2}})
	if terminal != "Poll bg-1: success (exit: 2)" {
		t.Errorf("renderPollResult(terminal) = %q, want %q", terminal, "Poll bg-1: success (exit: 2)")
	}
}

func TestJob_StopKillErrorExact(t *testing.T) {
	t.Parallel()
	base := newMockRegistry()
	base.add(&JobInfo{ID: "bg-21", Type: "bash", Status: "running", Command: "sleep 60"})
	reg := &killErrorRegistry{mockRegistry: base}

	_, err := executeStop(reg, "bg-21")
	if err == nil {
		t.Fatal("executeStop must fail when Kill fails")
	}
	if err.Error() != "failed to stop job bg-21: kill failed: permission denied" {
		t.Errorf("error = %q, want %q", err.Error(), "failed to stop job bg-21: kill failed: permission denied")
	}
}

func TestJob_LegacyInputShapeRejected(t *testing.T) {
	t.Parallel()
	reg := newMockRegistry()
	reg.add(&JobInfo{
		ID:       "bg-1",
		Type:     "bash",
		Status:   "completed",
		Command:  "echo hello",
		Output:   "hello\n",
		ExitCode: 0,
	})
	tl := NewJob(reg)
	for _, legacy := range []string{`{"poll":"true"}`, `{"stop":"bg-1"}`, `{"list":true}`} {
		_, err := tl.Call(context.Background(), json.RawMessage(legacy), nil)
		if err == nil {
			t.Errorf("legacy input %s accepted, want rejection", legacy)
			continue
		}
		if !strings.Contains(err.Error(), `"list", "poll", "stop"`) {
			t.Errorf("legacy input %s error = %q, want valid-actions list", legacy, err)
		}
	}
}
