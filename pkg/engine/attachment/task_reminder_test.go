package attachment

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// isTaskToolUse
// ---------------------------------------------------------------------------

func TestIsTaskToolUse_TaskTool(t *testing.T) {
	content := []types.ContentBlock{
		types.NewToolUseBlock("id-1", "Task", json.RawMessage(`{"action":"create"}`)),
	}
	if !isTaskToolUse(content) {
		t.Error("isTaskToolUse should return true for Task tool_use")
	}
}

func TestIsTaskToolUse_OtherTool(t *testing.T) {
	content := []types.ContentBlock{
		types.NewToolUseBlock("id-1", "Bash", json.RawMessage(`{}`)),
	}
	if isTaskToolUse(content) {
		t.Error("isTaskToolUse should return false for non-Task tool_use")
	}
}

func TestIsTaskToolUse_NoToolUse(t *testing.T) {
	content := []types.ContentBlock{
		types.NewTextBlock("hello"),
	}
	if isTaskToolUse(content) {
		t.Error("isTaskToolUse should return false for text-only content")
	}
}

// ---------------------------------------------------------------------------
// isTaskReminderMessage
// ---------------------------------------------------------------------------

func TestIsTaskReminderMessage_Valid(t *testing.T) {
	msg := types.Message{
		Role:    types.RoleUser,
		Flags:   types.FlagMeta,
		Content: []types.ContentBlock{types.NewTextBlock("The task tools haven't been used recently. Do stuff.")},
	}
	if !isTaskReminderMessage(msg) {
		t.Error("isTaskReminderMessage should return true for meta user message with marker text")
	}
}

func TestIsTaskReminderMessage_NotMeta(t *testing.T) {
	msg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("The task tools haven't been used recently.")},
	}
	if isTaskReminderMessage(msg) {
		t.Error("isTaskReminderMessage should return false without FlagMeta")
	}
}

func TestIsTaskReminderMessage_WrongRole(t *testing.T) {
	msg := types.Message{
		Role:    types.RoleAssistant,
		Flags:   types.FlagMeta,
		Content: []types.ContentBlock{types.NewTextBlock("The task tools haven't been used recently.")},
	}
	if isTaskReminderMessage(msg) {
		t.Error("isTaskReminderMessage should return false for assistant role")
	}
}

func TestIsTaskReminderMessage_WrongText(t *testing.T) {
	msg := types.Message{
		Role:    types.RoleUser,
		Flags:   types.FlagMeta,
		Content: []types.ContentBlock{types.NewTextBlock("Some other text")},
	}
	if isTaskReminderMessage(msg) {
		t.Error("isTaskReminderMessage should return false without marker text")
	}
}

// ---------------------------------------------------------------------------
// CountAssistantTurnsSinceLastEvent
// ---------------------------------------------------------------------------

func makeAssistantMsg(content ...types.ContentBlock) types.Message {
	return types.Message{Role: types.RoleAssistant, Content: content}
}

func makeUserMsg(text string) types.Message {
	return types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(text)}}
}

func TestCountAssistantTurnsSinceLastEvent_Empty(t *testing.T) {
	turns, found := CountAssistantTurnsSinceLastEvent(
		nil,
		isTaskToolUse,
		isTaskReminderMessage,
	)
	if turns != 0 {
		t.Errorf("turns = %d, want 0", turns)
	}
	if found {
		t.Error("found = true, want false for empty messages")
	}
}

func TestCountAssistantTurnsSinceLastEvent_TaskToolRecent(t *testing.T) {
	msgs := []types.Message{
		makeUserMsg("hi"),
		makeAssistantMsg(types.NewTextBlock("hello")),
		makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
	}
	turns, found := CountAssistantTurnsSinceLastEvent(
		msgs,
		isTaskToolUse,
		isTaskReminderMessage,
	)
	if turns != 0 {
		t.Errorf("turns = %d, want 0 (Task tool is most recent assistant)", turns)
	}
	if !found {
		t.Error("found = false, want true")
	}
}

func TestCountAssistantTurnsSinceLastEvent_TaskToolOld(t *testing.T) {
	msgs := []types.Message{
		makeUserMsg("hi"),
		makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
		makeUserMsg("result"),
		makeAssistantMsg(types.NewTextBlock("turn 1")),
		makeAssistantMsg(types.NewTextBlock("turn 2")),
	}
	turns, found := CountAssistantTurnsSinceLastEvent(
		msgs,
		isTaskToolUse,
		isTaskReminderMessage,
	)
	if turns != 2 {
		t.Errorf("turns = %d, want 2 (2 assistant turns after Task tool)", turns)
	}
	if !found {
		t.Error("found = false, want true")
	}
}

func TestCountAssistantTurnsSinceLastEvent_ReminderResets(t *testing.T) {
	reminderMsg := types.Message{
		Role:    types.RoleUser,
		Flags:   types.FlagMeta,
		Content: []types.ContentBlock{types.NewTextBlock("The task tools haven't been used recently.")},
	}
	msgs := []types.Message{
		makeAssistantMsg(types.NewTextBlock("turn 1")),
		makeAssistantMsg(types.NewTextBlock("turn 2")),
		reminderMsg,
		makeAssistantMsg(types.NewTextBlock("turn 3")),
	}
	turns, found := CountAssistantTurnsSinceLastEvent(
		msgs,
		func(content []types.ContentBlock) bool { return false },
		isTaskReminderMessage,
	)
	if turns != 1 {
		t.Errorf("turns = %d, want 1 (1 assistant turn after reminder)", turns)
	}
	if !found {
		t.Error("found = false, want true")
	}
}

// ---------------------------------------------------------------------------
// TaskReminderProvider — ShouldFire
// ---------------------------------------------------------------------------

func pendingTasks() []TaskItem {
	return []TaskItem{
		{ID: "1", Status: "in_progress", Subject: "Fix bug"},
	}
}

// The TS defaults are the shipped tuning; a silent change to either number
// changes how often the reminder interrupts the model.
func TestDefaultTaskReminderConfig(t *testing.T) {
	want := TaskReminderConfig{TurnsSinceTaskAction: 10, TurnsSinceLastReminder: 10}
	if DefaultTaskReminderConfig != want {
		t.Errorf("DefaultTaskReminderConfig = %+v, want %+v", DefaultTaskReminderConfig, want)
	}
	if got := NewTaskReminderProvider().Config; got != want {
		t.Errorf("NewTaskReminderProvider().Config = %+v, want %+v", got, want)
	}
}

// firingCtx clears both turn thresholds for a provider configured {1,1}, so
// the guard under test is the only thing that can suppress the reminder.
func firingCtx(tasks []TaskItem, err error) ReminderContext {
	return ReminderContext{
		Messages: []types.Message{
			makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
			makeAssistantMsg(types.NewTextBlock("turn 1")),
		},
		TaskList: &mockTaskList{tasks: tasks, err: err},
	}
}

func TestTaskReminder_ShouldFire_GuardBaselineFires(t *testing.T) {
	p := &TaskReminderProvider{Config: TaskReminderConfig{TurnsSinceTaskAction: 1, TurnsSinceLastReminder: 1}}
	if !p.ShouldFire(firingCtx(pendingTasks(), nil)) {
		t.Fatal("baseline context with thresholds {1,1} must fire — the guard tests below depend on it")
	}
}

func TestTaskReminder_ShouldFire_SubagentBeatsEverythingElse(t *testing.T) {
	p := &TaskReminderProvider{Config: TaskReminderConfig{TurnsSinceTaskAction: 1, TurnsSinceLastReminder: 1}}
	ctx := firingCtx(pendingTasks(), nil)
	ctx.IsSubagent = true
	if p.ShouldFire(ctx) {
		t.Error("ShouldFire = true for a subagent, want false regardless of turn counts")
	}
}

func TestTaskReminder_ShouldFire_ErrorSuppressesReturnedTasks(t *testing.T) {
	p := &TaskReminderProvider{Config: TaskReminderConfig{TurnsSinceTaskAction: 1, TurnsSinceLastReminder: 1}}
	// (tasks, err) together: the error alone must suppress the reminder.
	if p.ShouldFire(firingCtx(pendingTasks(), errors.New("db error"))) {
		t.Error("ShouldFire = true when ListPending errors, want false even if it returned tasks")
	}
}

func TestTaskReminder_ShouldFire_EmptyTaskSlice(t *testing.T) {
	p := &TaskReminderProvider{Config: TaskReminderConfig{TurnsSinceTaskAction: 1, TurnsSinceLastReminder: 1}}
	if p.ShouldFire(firingCtx(nil, nil)) {
		t.Error("ShouldFire = true with an empty pending list, want false")
	}
}

// Threshold boundaries: the comparison is `<`, so a count exactly equal to the
// configured minimum passes.
func TestTaskReminder_ShouldFire_ExactlyAtTaskActionThreshold(t *testing.T) {
	p := &TaskReminderProvider{Config: TaskReminderConfig{TurnsSinceTaskAction: 3, TurnsSinceLastReminder: 3}}
	ctx := ReminderContext{
		Messages: []types.Message{
			makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
			makeAssistantMsg(types.NewTextBlock("turn 1")),
			makeAssistantMsg(types.NewTextBlock("turn 2")),
			makeAssistantMsg(types.NewTextBlock("turn 3")),
		},
		TaskList: &mockTaskList{tasks: pendingTasks()},
	}
	if !p.ShouldFire(ctx) {
		t.Error("3 turns since the last Task action with a threshold of 3 must fire")
	}
}

func TestTaskReminder_ShouldFire_ExactlyAtReminderThreshold(t *testing.T) {
	ctx := ReminderContext{
		Messages: []types.Message{
			makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
			makeAssistantMsg(types.NewTextBlock("turn 1")),
			makeAssistantMsg(types.NewTextBlock("turn 2")),
			makeAssistantMsg(types.NewTextBlock("turn 3")),
		},
		TaskList: &mockTaskList{tasks: pendingTasks()},
	}
	// 4 assistant turns since the last reminder: threshold 4 passes, 5 blocks.
	p := &TaskReminderProvider{Config: TaskReminderConfig{TurnsSinceTaskAction: 3, TurnsSinceLastReminder: 4}}
	if !p.ShouldFire(ctx) {
		t.Error("4 turns since the last reminder with a threshold of 4 must fire")
	}
	p.Config.TurnsSinceLastReminder = 5
	if p.ShouldFire(ctx) {
		t.Error("4 turns since the last reminder with a threshold of 5 must not fire")
	}
}

func TestTaskReminder_ShouldFire_OneTurnBelowTaskActionThreshold(t *testing.T) {
	ctx := ReminderContext{
		Messages: []types.Message{
			makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
			makeAssistantMsg(types.NewTextBlock("turn 1")),
			makeAssistantMsg(types.NewTextBlock("turn 2")),
			makeAssistantMsg(types.NewTextBlock("turn 3")),
		},
		TaskList: &mockTaskList{tasks: pendingTasks()},
	}
	p := &TaskReminderProvider{Config: TaskReminderConfig{TurnsSinceTaskAction: 4, TurnsSinceLastReminder: 3}}
	if p.ShouldFire(ctx) {
		t.Error("3 turns since the last Task action with a threshold of 4 must not fire")
	}
}

func TestTaskReminder_ShouldFire_Subagent(t *testing.T) {
	p := NewTaskReminderProvider()
	ctx := ReminderContext{
		IsSubagent: true,
		TaskList:   &mockTaskList{tasks: pendingTasks()},
	}
	if p.ShouldFire(ctx) {
		t.Error("ShouldFire should return false for subagents")
	}
}

func TestTaskReminder_ShouldFire_NilTaskList(t *testing.T) {
	p := NewTaskReminderProvider()
	ctx := ReminderContext{TaskList: nil}
	if p.ShouldFire(ctx) {
		t.Error("ShouldFire should return false with nil TaskList")
	}
}

func TestTaskReminder_ShouldFire_NoPendingTasks(t *testing.T) {
	p := NewTaskReminderProvider()
	ctx := ReminderContext{
		TaskList: &mockTaskList{tasks: nil},
	}
	if p.ShouldFire(ctx) {
		t.Error("ShouldFire should return false with no pending tasks")
	}
}

func TestTaskReminder_ShouldFire_TaskListError(t *testing.T) {
	p := NewTaskReminderProvider()
	ctx := ReminderContext{
		TaskList: &mockTaskList{err: errors.New("db error")},
	}
	if p.ShouldFire(ctx) {
		t.Error("ShouldFire should return false on TaskList error")
	}
}

func TestTaskReminder_ShouldFire_TooSoon(t *testing.T) {
	p := NewTaskReminderProvider()
	p.Config.TurnsSinceTaskAction = 10
	p.Config.TurnsSinceLastReminder = 10
	// Only 3 assistant turns since last Task tool use — too soon.
	msgs := []types.Message{
		makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
		makeAssistantMsg(types.NewTextBlock("turn 1")),
		makeAssistantMsg(types.NewTextBlock("turn 2")),
		makeAssistantMsg(types.NewTextBlock("turn 3")),
	}
	ctx := ReminderContext{
		Messages: msgs,
		TaskList: &mockTaskList{tasks: pendingTasks()},
	}
	if p.ShouldFire(ctx) {
		t.Error("ShouldFire should return false when too few turns since last Task tool use")
	}
}

func TestTaskReminder_ShouldFire_EnoughTurns(t *testing.T) {
	p := NewTaskReminderProvider()
	p.Config.TurnsSinceTaskAction = 3
	p.Config.TurnsSinceLastReminder = 3
	// 5 assistant turns since last Task tool use.
	msgs := []types.Message{
		makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
		makeAssistantMsg(types.NewTextBlock("turn 1")),
		makeAssistantMsg(types.NewTextBlock("turn 2")),
		makeAssistantMsg(types.NewTextBlock("turn 3")),
		makeAssistantMsg(types.NewTextBlock("turn 4")),
		makeAssistantMsg(types.NewTextBlock("turn 5")),
	}
	ctx := ReminderContext{
		Messages: msgs,
		TaskList: &mockTaskList{tasks: pendingTasks()},
	}
	if !p.ShouldFire(ctx) {
		t.Error("ShouldFire should return true when enough turns have passed")
	}
}

func TestTaskReminder_ShouldFire_RecentReminderBlocks(t *testing.T) {
	p := NewTaskReminderProvider()
	p.Config.TurnsSinceTaskAction = 3
	p.Config.TurnsSinceLastReminder = 5
	reminderMsg := types.Message{
		Role:    types.RoleUser,
		Flags:   types.FlagMeta,
		Content: []types.ContentBlock{types.NewTextBlock("The task tools haven't been used recently.")},
	}
	msgs := []types.Message{
		makeAssistantMsg(types.NewToolUseBlock("id", "Task", json.RawMessage(`{}`))),
		makeAssistantMsg(types.NewTextBlock("turn 1")),
		makeAssistantMsg(types.NewTextBlock("turn 2")),
		reminderMsg,
		// Only 3 turns since reminder — need 5
		makeAssistantMsg(types.NewTextBlock("turn 3")),
		makeAssistantMsg(types.NewTextBlock("turn 4")),
		makeAssistantMsg(types.NewTextBlock("turn 5")),
	}
	ctx := ReminderContext{
		Messages: msgs,
		TaskList: &mockTaskList{tasks: pendingTasks()},
	}
	if p.ShouldFire(ctx) {
		t.Error("ShouldFire should return false when not enough turns since last reminder")
	}
}

// ---------------------------------------------------------------------------
// TaskReminderProvider — Render
// ---------------------------------------------------------------------------

// The TS-aligned reminder sentence, asserted verbatim by every Render test.
const wantReminderBody = "The task tools haven't been used recently. If you're working on tasks that would benefit from tracking progress, consider using Task to add new tasks and Task to update task status (set to in_progress when starting, completed when done). Consider cleaning up the task list if it has become stale. Only use these if relevant to the current work. Make sure that you NEVER mention this reminder to the user"

func TestTaskReminder_Render_WrappedInSystemReminder(t *testing.T) {
	p := NewTaskReminderProvider()
	ctx := ReminderContext{
		TaskList: &mockTaskList{tasks: pendingTasks()},
	}
	msgs := p.Render(ctx)
	if len(msgs) != 1 {
		t.Fatalf("Render = %d messages, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", msg.Role, types.RoleUser)
	}
	if msg.Flags&types.FlagMeta == 0 {
		t.Error("FlagMeta not set")
	}
	// The whole rendered body, exactly: the wrapper, the TS-aligned sentence,
	// the task-list separator, and the formatted entry with its newline.
	want := "<system-reminder>\n" +
		wantReminderBody +
		"\n\nHere are the existing tasks:\n\n" +
		"#1. [in_progress] Fix bug\n" +
		"\n</system-reminder>"
	if got := msg.Content[0].Text; got != want {
		t.Errorf("Render text =\n%q\nwant:\n%q", got, want)
	}
}

func TestTaskReminder_Render_NoTasks(t *testing.T) {
	p := NewTaskReminderProvider()
	ctx := ReminderContext{
		TaskList: &mockTaskList{tasks: nil},
	}
	msgs := p.Render(ctx)
	if len(msgs) != 0 {
		t.Errorf("Render with no tasks = %d messages, want 0", len(msgs))
	}
}

func TestTaskReminder_Render_Error(t *testing.T) {
	p := NewTaskReminderProvider()
	ctx := ReminderContext{
		TaskList: &mockTaskList{err: errors.New("fail")},
	}
	msgs := p.Render(ctx)
	if len(msgs) != 0 {
		t.Errorf("Render with error = %d messages, want 0", len(msgs))
	}
}

func TestTaskReminder_Render_TaskWithDescription(t *testing.T) {
	p := NewTaskReminderProvider()
	tasks := []TaskItem{
		{ID: "5", Status: "pending", Subject: "Add tests", Description: "unit + integration"},
	}
	ctx := ReminderContext{
		TaskList: &mockTaskList{tasks: tasks},
	}
	msgs := p.Render(ctx)
	if len(msgs) != 1 {
		t.Fatalf("Render = %d messages, want 1", len(msgs))
	}
	want := "<system-reminder>\n" +
		wantReminderBody +
		"\n\nHere are the existing tasks:\n\n" +
		"#5. [pending] Add tests: unit + integration\n" +
		"\n</system-reminder>"
	if got := msgs[0].Content[0].Text; got != want {
		t.Errorf("Render text =\n%q\nwant:\n%q", got, want)
	}
}

// ---------------------------------------------------------------------------
// TaskReminderProvider — Key
// ---------------------------------------------------------------------------

func TestTaskReminder_Key(t *testing.T) {
	p := NewTaskReminderProvider()
	if p.Key() != "task_reminder" {
		t.Errorf("Key() = %q, want %q", p.Key(), "task_reminder")
	}
}
