package wui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// newRealTaskList creates a task.List backed by a temp dir so tests can use
// the real disk-based implementation (CreateTask, UpdateTask, etc.).
func newRealTaskList(t *testing.T) *task.List {
	t.Helper()
	dir := t.TempDir()
	tl := task.NewList(dir)
	if err := tl.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return tl
}

// TestBuildTaskListMessage_NilTaskList verifies that a nil engine.TaskList
// (no session dir, sub-engine, or pre-Init state) produces nil — no panic,
// no wire frame. The frontend keeps the panel hidden.
func TestBuildTaskListMessage_NilTaskList(t *testing.T) {
	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return nil }

	if got := c.buildTaskList(c.activeSlotTest(t)); got != nil {
		t.Fatalf("expected nil for nil TaskList, got %s", string(got))
	}
}

// TestBuildTaskListMessage_EmptyDir verifies that a task.List whose Dir() is
// "" (engine with no session id) short-circuits to nil. This is the same
// guard the TUI uses at main.go:629.
func TestBuildTaskListMessage_EmptyDir(t *testing.T) {
	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return task.NewList("") }

	if got := c.buildTaskList(c.activeSlotTest(t)); got != nil {
		t.Fatalf("expected nil for empty Dir(), got %s", string(got))
	}
}

// TestBuildTaskListMessage_WithTasks verifies the happy path: tasks are
// serialized with the correct id/subject/status, and IDs come back as
// monotonically increasing strings (task package allocates them).
func TestBuildTaskListMessage_WithTasks(t *testing.T) {
	tl := newRealTaskList(t)
	id1, _ := tl.CreateTask("Fix bug", "desc", "", nil)
	id2, _ := tl.CreateTask("Write tests", "desc", "", nil)
	_, _, _ = tl.UpdateTask(id2, task.TaskUpdates{Status: new(task.StatusInProgress)})

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }

	payload := c.buildTaskList(c.activeSlotTest(t))
	if payload == nil {
		t.Fatal("expected non-nil payload")
	}
	var got taskListOutbound
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "task_list" {
		t.Errorf("Type = %q, want \"task_list\"", got.Type)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("len(Tasks) = %d, want 2", len(got.Tasks))
	}
	if got.Tasks[0].ID != id1 || got.Tasks[0].Subject != "Fix bug" || got.Tasks[0].Status != "pending" {
		t.Errorf("Tasks[0] = %+v, want id=%s subject=Fix bug status=pending", got.Tasks[0], id1)
	}
	if got.Tasks[1].ID != id2 || got.Tasks[1].Subject != "Write tests" || got.Tasks[1].Status != "in_progress" {
		t.Errorf("Tasks[1] = %+v, want id=%s subject=Write tests status=in_progress", got.Tasks[1], id2)
	}
}

// TestBuildTaskListMessage_FiltersInternalAndBlockedBy verifies two edge
// cases in the same flow (mirrors main.go:640-657):
//  1. Tasks with Metadata["_internal"] are excluded from the wire payload.
//  2. BlockedBy is filtered to only UNcompleted blockers and resolved from
//     task IDs to subjects.
func TestBuildTaskListMessage_FiltersInternalAndBlockedBy(t *testing.T) {
	tl := newRealTaskList(t)
	id1, _ := tl.CreateTask("Blocker", "desc", "", nil)
	id2, _ := tl.CreateTask("Dependent", "desc", "", nil)
	_, _ = tl.CreateTask("Hidden", "desc", "", map[string]any{"_internal": true})
	_ = tl.BlockTask(id1, id2)
	_, _, _ = tl.UpdateTask(id1, task.TaskUpdates{Status: new(task.StatusCompleted)})

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }

	payload := c.buildTaskList(c.activeSlotTest(t))
	if payload == nil {
		t.Fatal("expected non-nil payload")
	}
	var got taskListOutbound
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("len(Tasks) = %d, want 2 (internal filtered)", len(got.Tasks))
	}
	if got.Tasks[1].ID != id2 {
		t.Fatalf("Tasks[1].ID = %q, want %s", got.Tasks[1].ID, id2)
	}
	if len(got.Tasks[1].BlockedBy) != 0 {
		t.Errorf("Dependent.BlockedBy = %v, want empty (blocker completed)", got.Tasks[1].BlockedBy)
	}
}

// TestBuildTaskListMessage_BlockedByResolvesToSubjects verifies that an
// UNcompleted blocker's ID is resolved to its subject in the wire payload.
func TestBuildTaskListMessage_BlockedByResolvesToSubjects(t *testing.T) {
	tl := newRealTaskList(t)
	id1, _ := tl.CreateTask("Setup", "desc", "", nil)
	id2, _ := tl.CreateTask("Build", "desc", "", nil)
	_ = tl.BlockTask(id1, id2)

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }

	payload := c.buildTaskList(c.activeSlotTest(t))
	var got taskListOutbound
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Tasks[1].BlockedBy) != 1 || got.Tasks[1].BlockedBy[0] != "Setup" {
		t.Errorf("BlockedBy = %v, want [\"Setup\"]", got.Tasks[1].BlockedBy)
	}
}

// TestHandle_ToolEndTask_PushesTaskList verifies the load-bearing ordering:
// when a Task tool_end arrives, the client must receive the tool_end event
// frame FIRST, then the task_list frame. Asserts strict index ordering so a
// regression that pushes task_list before tool_end is caught.
func TestHandle_ToolEndTask_PushesTaskList(t *testing.T) {
	tl := newRealTaskList(t)
	_, _ = tl.CreateTask("Do thing", "desc", "", nil)

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }
	ws := dialAndStore(t, c)
	// dialAndStore now drains all takeover frames including task_list.

	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_task_1", Name: "Task"},
	})
	c.Handle(types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tu_task_1"},
	})

	var types_ []string
	toolEndIdx := -1
	taskListIdx := -1
	for i := range 3 {
		msg := readWSMessage(t, ws)
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(msg, &head); err != nil {
			t.Fatalf("msg %d unmarshal: %v", i, err)
		}
		types_ = append(types_, head.Type)
		if head.Type == "event" && strings.Contains(string(msg), `"tool_end"`) {
			toolEndIdx = i
		}
		if head.Type == "task_list" {
			taskListIdx = i
		}
	}
	if toolEndIdx < 0 {
		t.Fatalf("tool_end event frame not received; frames=%v", types_)
	}
	if taskListIdx < 0 {
		t.Fatalf("task_list frame not received; frames=%v", types_)
	}
	if taskListIdx <= toolEndIdx {
		t.Fatalf("task_list (idx %d) must come AFTER tool_end (idx %d); frames=%v", taskListIdx, toolEndIdx, types_)
	}
}

// TestHandle_ToolEndNonTask_NoPush verifies that tool_end for non-Task tools
// does NOT push a task_list frame. Only Task tool calls mutate the task list.
func TestHandle_ToolEndNonTask_NoPush(t *testing.T) {
	tl := newRealTaskList(t)
	_, _ = tl.CreateTask("Unrelated", "desc", "", nil)

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }
	ws := dialAndStore(t, c)
	// dialAndStore now drains all takeover frames including task_list.

	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_read_1", Name: "Read"},
	})
	c.Handle(types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tu_read_1"},
	})

	for range 2 {
		_ = readWSMessage(t, ws)
	}
	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // REAL-TIME
	if _, data, err := ws.ReadMessage(); err == nil {
		t.Fatalf("unexpected frame after non-Task tool_end: %s", string(data))
	}
}

// TestTakeover_PushesTaskList verifies that on reconnect (WS takeover), the
// client receives a task_list frame inside the metadata composite frame.
// This is the path that populates the panel on page refresh.
func TestTakeover_PushesTaskList(t *testing.T) {
	tl := newRealTaskList(t)
	_, _ = tl.CreateTask("Resume work", "desc", "", nil)

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")

	// The server sends a single metadata frame. task_list is nested inside.
	data := readWSMessage(t, ws)
	var meta struct {
		Type  string          `json:"type"`
		Tasks json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta.Type != "metadata" {
		t.Fatalf("type = %q, want \"metadata\"", meta.Type)
	}
	if len(meta.Tasks) == 0 {
		t.Fatal("metadata.tasks is empty, want non-nil task_list")
	}
	var tasks struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(meta.Tasks, &tasks); err != nil {
		t.Fatalf("unmarshal tasks: %v", err)
	}
	if tasks.Type != "task_list" {
		t.Fatalf("tasks.type = %q, want \"task_list\"", tasks.Type)
	}
}

// TestTakeover_NoTaskListWhenEmpty verifies that when the engine has no tasks,
// the takeover sequence does NOT send a task_list frame. The panel stays
// hidden on the client.
func TestTakeover_NoTaskListWhenEmpty(t *testing.T) {
	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return nil }

	ws := dialAndStore(t, c)
	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // REAL-TIME
	if _, data, err := ws.ReadMessage(); err == nil {
		t.Fatalf("unexpected frame when no tasks exist: %s", string(data))
	}
}

// TestHandle_SubAgentTaskNotTracked verifies that a Task tool call by a
// sub-agent (Agent != nil) does NOT trigger a task_list push — the panel
// reflects only the main engine's task list.
func TestHandle_SubAgentTaskNotTracked(t *testing.T) {
	tl := newRealTaskList(t)
	_, _ = tl.CreateTask("Main task", "desc", "", nil)

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }
	ws := dialAndStore(t, c)
	// dialAndStore now drains all takeover frames including task_list.

	agent := &types.AgentMeta{ParentToolUseID: "parent_tu", AgentType: "Executor"}
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		Agent:   agent,
		ToolUse: &types.ToolUseEvent{ID: "tu_sub_task", Name: "Task"},
	})
	c.Handle(types.QueryEvent{
		Type:       types.EventToolEnd,
		Agent:      agent,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tu_sub_task"},
	})

	// Read the two event frames, then assert no task_list.
	for range 2 {
		_ = readWSMessage(t, ws)
	}
	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // REAL-TIME
	if _, data, err := ws.ReadMessage(); err == nil {
		t.Fatalf("unexpected task_list from sub-agent Task call: %s", string(data))
	}
}

// TestHandle_TaskToolIDsClearedOnQueryEnd verifies that tracked Task
// tool_use IDs do NOT leak across turns. After query_end clears the buffer,
// a tool_end with the prior turn's tracked ID must NOT push task_list.
func TestHandle_TaskToolIDsClearedOnQueryEnd(t *testing.T) {
	tl := newRealTaskList(t)
	_, _ = tl.CreateTask("Persist", "desc", "", nil)

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }
	ws := dialAndStore(t, c)
	// dialAndStore now drains all takeover frames including task_list.

	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_leak", Name: "Task"},
	})
	// Engine commits the turn → query_end clears taskToolIDs.
	c.slotsMu.RLock()
	slot := c.slots["main"]
	c.slotsMu.RUnlock()
	if slot != nil {
		resetQueryStats(&slot.queryStats)
		slot.taskToolIDs = make(map[string]bool)
	}

	c.Handle(types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tu_leak"},
	})

	// Read the two event frames (tool_start + tool_end), then assert no
	// task_list arrives within a short window.
	for range 2 {
		_ = readWSMessage(t, ws)
	}
	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond)) // REAL-TIME
	if _, data, err := ws.ReadMessage(); err == nil {
		t.Fatalf("unexpected task_list after stream done cleared tracking: %s", string(data))
	}
}

// TestBuildTaskListMessage_HubDispatchEndToEnd verifies the full hub-routed
// path: hub.Dispatch → c.Handle → tool_end frame + task_list frame arrive at
// the WS client. Catches a regression where the connector's hub subscription
// skips the task_list push.
func TestBuildTaskListMessage_HubDispatchEndToEnd(t *testing.T) {
	tl := newRealTaskList(t)
	_, _ = tl.CreateTask("Hub task", "desc", "", nil)

	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mock().taskListFn = func() *task.List { return tl }

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws) // drains everything including task_list
	if s := c.activeSlot(); s != nil {
		s.snapshotSent.Store(true)
	}

	h.Dispatch(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu_hub", Name: "Task"},
	})
	h.Dispatch(types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tu_hub"},
	})

	toolEndSeen := false
	taskListSeen := false
	for range 3 {
		msg := readWSMessage(t, ws)
		if strings.Contains(string(msg), "tool_end") {
			toolEndSeen = true
		}
		if strings.Contains(string(msg), "task_list") {
			taskListSeen = true
		}
	}
	if !toolEndSeen {
		t.Error("tool_end frame not received via hub dispatch")
	}
	if !taskListSeen {
		t.Error("task_list frame not received via hub dispatch")
	}
}

func TestBuildTaskListMessage_CleansUpDiskWhenAllCompleted(t *testing.T) {
	tl := newRealTaskList(t)
	id1, _ := tl.CreateTask("Task A", "desc", "", nil)
	id2, _ := tl.CreateTask("Task B", "desc", "", nil)

	c := newTestConnector(t)
	c.mock().taskListFn = func() *task.List { return tl }

	_, _, _ = tl.UpdateTask(id1, task.TaskUpdates{Status: new(task.StatusCompleted)})
	_, _, _ = tl.UpdateTask(id2, task.TaskUpdates{Status: new(task.StatusCompleted)})

	payload := c.buildTaskList(c.activeSlotTest(t))
	if payload == nil {
		t.Fatal("expected non-nil payload with completed tasks")
	}
	var got taskListOutbound
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("len(Tasks) = %d, want 2", len(got.Tasks))
	}
	for _, item := range got.Tasks {
		if item.Status != string(task.StatusCompleted) {
			t.Errorf("task %s status = %s, want completed", item.ID, item.Status)
		}
	}

	remaining, _ := tl.ListTasks()
	if len(remaining) != 0 {
		t.Errorf("expected 0 tasks after cleanup, got %d", len(remaining))
	}

	payload2 := c.buildTaskList(c.activeSlotTest(t))
	if payload2 != nil {
		t.Errorf("expected nil on second call (disk empty), got %s", string(payload2))
	}
}
