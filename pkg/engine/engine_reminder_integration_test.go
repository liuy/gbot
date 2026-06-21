package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Reminder integration tests — full call chain through the turn loop.
//
// These tests verify that the reminder injection code in engine.go's turn loop
// (after tool results, before turnCount++) works correctly end-to-end:
//   - Mock LLM produces multi-turn conversations
//   - Real task file storage (task.NewList with t.TempDir())
//   - Reminders are appended to message history tail
//   - Reminders are NOT prepended to the prefix (prompt cache preserved)
// ---------------------------------------------------------------------------

// findReminderMessages returns all meta user messages containing <system-reminder>
// from the given message slice. These are the injected reminder messages.
func findReminderMessages(msgs []types.Message) []types.Message {
	var reminders []types.Message
	for _, m := range msgs {
		if m.Role != types.RoleUser {
			continue
		}
		if m.Flags&types.FlagMeta == 0 {
			continue
		}
		for _, b := range m.Content {
			if b.Type == types.ContentTypeText && strings.Contains(b.Text, "<system-reminder>") {
				reminders = append(reminders, m)
			}
		}
	}
	return reminders
}

// TestReminder_FullChain_TurnLoopInjection verifies the complete call chain:
// user input → LLM response → tool execution → reminder check → reminder appended.
//
// Setup: 12 turns of tool_use (non-Task) + 1 end_turn. After the 12th turn,
// the reminder should fire and appear in the message history.
func TestReminder_FullChain_TurnLoopInjection(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()
	tl := task.NewList(taskDir)
	id, err := tl.CreateTask("Fix auth bug", "OAuth token refresh fails", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := tl.UpdateTask(id, task.TaskUpdates{
		Status: &[]task.TaskStatus{task.StatusInProgress}[0],
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	mp := &mockProvider{}
	// 12 turns of tool_use for "dummy_tool" (not "Task") + 1 end_turn.
	for i := range 12 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "All done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "do 12 rounds", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	reminders := findReminderMessages(result.Messages)
	if len(reminders) == 0 {
		t.Fatal("expected at least one reminder message after 12 turns, found none")
	}

	text := reminders[0].Content[0].Text
	if !strings.Contains(text, "Fix auth bug") {
		t.Errorf("reminder should contain task subject 'Fix auth bug', got:\n%s", text)
	}
	if !strings.Contains(text, "in_progress") {
		t.Errorf("reminder should contain task status 'in_progress', got:\n%s", text)
	}
	if !strings.Contains(text, "<system-reminder>") {
		t.Errorf("reminder should be wrapped in <system-reminder>, got:\n%s", text)
	}
	if !strings.Contains(text, "The task tools haven't been used recently") {
		t.Errorf("reminder should contain TS-aligned prompt text, got:\n%s", text)
	}
}

// TestReminder_NotPrepended_PreservesCache verifies that reminders are appended
// to the message history tail, NOT prepended to the prefix. This is critical
// for prompt cache preservation.
func TestReminder_NotPrepended_PreservesCache(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()
	tl := task.NewList(taskDir)
	id, err := tl.CreateTask("Important task", "", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := tl.UpdateTask(id, task.TaskUpdates{
		Status: &[]task.TaskStatus{task.StatusPending}[0],
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	mp := &mockProvider{}
	for i := range 12 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test message", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if len(result.Messages) == 0 {
		t.Fatal("no messages")
	}

	// First message must be the original user message, not a reminder.
	first := result.Messages[0]
	if first.Role != types.RoleUser {
		t.Fatalf("first message role = %s, want User", first.Role)
	}
	if first.Flags&types.FlagMeta != 0 {
		t.Fatal("first message should NOT be a meta (system-injected) message")
	}
	firstText := first.Content[0].Text
	if strings.Contains(firstText, "<system-reminder>") {
		t.Fatalf("first message should be user input, not a reminder. Got:\n%s", firstText)
	}
	// The original user message should contain the input we sent.
	if !strings.Contains(firstText, "test message") {
		t.Errorf("first message should contain 'test message', got:\n%s", firstText)
	}
}

// TestReminder_Subagent_NeverFires verifies that subagent engines never inject
// reminders, regardless of turn count.
func TestReminder_Subagent_NeverFires(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()
	tl := task.NewList(taskDir)
	_, err := tl.CreateTask("Subagent task", "", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	mp := &mockProvider{}
	for i := range 15 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}

	// Use NewSubEngine to create a subagent engine.
	parent := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { parent.Close() })
	sub := parent.NewSubEngine(SubEngineOptions{
		AgentType: "General",
		Tools:     map[string]tool.Tool{"dummy_tool": mt},
	})
	sub.taskList = tl

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := sub.QuerySync(ctx, "subagent work", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	reminders := findReminderMessages(result.Messages)
	if len(reminders) != 0 {
		t.Fatalf("subagent should never inject reminders, found %d; first: %s",
			len(reminders), reminders[0].Content[0].Text[:min(80, len(reminders[0].Content[0].Text))])
	}
}

// TestReminder_RecoveryAfterRestart verifies that a new engine instance
// (simulating a process restart) with the same task directory correctly
// fires reminders for pre-existing tasks.
//
// Cold start → hot path → recovery: engine A creates tasks, engine B reads them.
func TestReminder_RecoveryAfterRestart(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()

	// Engine A: creates tasks (no turns, just sets up the file store).
	tlA := task.NewList(taskDir)
	_, err := tlA.CreateTask("Migrate DB", "Add migration 0042", "", nil)
	if err != nil {
		t.Fatalf("CreateTask A: %v", err)
	}
	_, err = tlA.CreateTask("Write tests", "Cover edge cases", "", nil)
	if err != nil {
		t.Fatalf("CreateTask A: %v", err)
	}

	// Engine B: new instance, same task dir (simulates restart).
	tlB := task.NewList(taskDir)
	mp := &mockProvider{}
	for i := range 12 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tlB,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "continue after restart", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	reminders := findReminderMessages(result.Messages)
	if len(reminders) == 0 {
		t.Fatal("restarted engine should fire reminder for pre-existing tasks")
	}
	text := reminders[0].Content[0].Text
	if !strings.Contains(text, "Migrate DB") {
		t.Errorf("reminder should contain 'Migrate DB', got:\n%s", text)
	}
	if !strings.Contains(text, "Write tests") {
		t.Errorf("reminder should contain 'Write tests', got:\n%s", text)
	}
}

// TestReminder_TaskToolUse_ResetsCounter verifies that using the Task tool
// resets the turn counter, preventing reminders for another 10 turns.
func TestReminder_TaskToolUse_ResetsCounter(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()
	tl := task.NewList(taskDir)
	id, err := tl.CreateTask("Main task", "", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := tl.UpdateTask(id, task.TaskUpdates{
		Status: &[]task.TaskStatus{task.StatusInProgress}[0],
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	mp := &mockProvider{}

	// 5 turns of dummy_tool (not enough to trigger reminder).
	for i := range 5 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("pre%d", i), "dummy_tool", `{}`), nil)
	}
	// 1 turn using "Task" tool (resets the counter).
	mp.addResponse(toolUseStreamEvents("test-model",
		"task_turn", "Task", `{"action":"update"}`), nil)
	// 5 more turns of dummy_tool (still not enough: only 5 since Task use).
	for i := range 5 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("post%d", i), "dummy_tool", `{}`), nil)
	}
	// End turn.
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	// Need both dummy_tool and Task tool registered.
	dummyTool := &mockTool{name: "dummy_tool", enabled: true}
	taskTool := &mockTool{name: "Task", enabled: true}

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{
				dummyTool.Name(): dummyTool,
				taskTool.Name():  taskTool,
			}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "work on tasks", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// After 5+1+5 = 11 turns, but only 5 since Task use → no reminder.
	reminders := findReminderMessages(result.Messages)
	if len(reminders) != 0 {
		t.Fatalf("expected no reminder (only 5 turns since Task use), found %d; first: %s",
			len(reminders), reminders[0].Content[0].Text[:min(80, len(reminders[0].Content[0].Text))])
	}
}

// TestReminder_FiresAgain_AfterSufficientTurns verifies that:
// 1) reminder fires after 10 turns (no Task use)
// 2) reminder fires AGAIN after 10 more turns
// This tests the full lifecycle: trigger → cool-down → re-trigger.
func TestReminder_FiresAgain_AfterSufficientTurns(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()
	tl := task.NewList(taskDir)
	id, err := tl.CreateTask("Recurring task", "", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := tl.UpdateTask(id, task.TaskUpdates{
		Status: &[]task.TaskStatus{task.StatusInProgress}[0],
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	mp := &mockProvider{}
	// 25 turns of dummy_tool: reminder fires at turn 12, then again at turn 22+
	for i := range 25 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "long conversation", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	reminders := findReminderMessages(result.Messages)
	if len(reminders) < 2 {
		t.Fatalf("expected at least 2 reminders over 25 turns, got %d", len(reminders))
	}

	// Verify both reminders contain the task.
	for i, r := range reminders {
		text := r.Content[0].Text
		if !strings.Contains(text, "Recurring task") {
			t.Errorf("reminder %d should contain 'Recurring task', got:\n%s", i, text)
		}
	}
}

// TestReminder_NoReminder_WhenNoTasks verifies that no reminder fires when
// there are no pending tasks (empty task list).
func TestReminder_NoReminder_WhenNoTasks(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()
	tl := task.NewList(taskDir) // empty task list

	mp := &mockProvider{}
	for i := range 12 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "no tasks", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	reminders := findReminderMessages(result.Messages)
	if len(reminders) != 0 {
		t.Fatalf("expected no reminders when no tasks exist, found %d; first: %s",
			len(reminders), reminders[0].Content[0].Text[:min(80, len(reminders[0].Content[0].Text))])
	}
}

// TestReminder_NoReminder_WhenAllTasksCompleted verifies that completed tasks
// do not trigger reminders.
func TestReminder_NoReminder_WhenAllTasksCompleted(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()
	tl := task.NewList(taskDir)
	id, err := tl.CreateTask("Done task", "", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Mark as completed.
	if _, _, err := tl.UpdateTask(id, task.TaskUpdates{
		Status: &[]task.TaskStatus{task.StatusCompleted}[0],
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	mp := &mockProvider{}
	for i := range 12 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "all done", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	reminders := findReminderMessages(result.Messages)
	if len(reminders) != 0 {
		t.Fatalf("expected no reminders for completed tasks, found %d; first: %s",
			len(reminders), reminders[0].Content[0].Text[:min(80, len(reminders[0].Content[0].Text))])
	}
}

// TestReminder_NilTaskList_NoReminder verifies that a nil TaskList
// (e.g. engines without task support) never triggers reminders.
func TestReminder_NilTaskList_NoReminder(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{}
	for i := range 12 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:  "test-model",
		Logger: slog.Default(),
		// TaskList is nil (default)
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "no task list", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	reminders := findReminderMessages(result.Messages)
	if len(reminders) != 0 {
		t.Fatalf("expected no reminders with nil TaskList, found %d; first: %s",
			len(reminders), reminders[0].Content[0].Text[:min(80, len(reminders[0].Content[0].Text))])
	}
}

// TestReminder_MessageHasFlagMeta verifies that injected reminder messages
// have the FlagMeta flag set (hidden from UI/rewind).
func TestReminder_MessageHasFlagMeta(t *testing.T) {
	t.Parallel()

	taskDir := t.TempDir()
	tl := task.NewList(taskDir)
	id, err := tl.CreateTask("Flag test", "", "", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := tl.UpdateTask(id, task.TaskUpdates{
		Status: &[]task.TaskStatus{task.StatusPending}[0],
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	mp := &mockProvider{}
	for i := range 12 {
		mp.addResponse(toolUseStreamEvents("test-model",
			fmt.Sprintf("t%d", i), "dummy_tool", `{}`), nil)
	}
	mp.addResponse(textStreamEvents("test-model", "Done."), nil)

	mt := &mockTool{name: "dummy_tool", enabled: true}
	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{mt.Name(): mt}
		},
		Model:    "test-model",
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := eng.QuerySync(ctx, "test flag", "")
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	reminders := findReminderMessages(result.Messages)
	if len(reminders) == 0 {
		t.Fatal("expected at least one reminder")
	}

	r := reminders[0]
	if r.Flags&types.FlagMeta == 0 {
		t.Error("reminder message should have FlagMeta set")
	}
	if r.Role != types.RoleUser {
		t.Errorf("reminder role = %s, want User", r.Role)
	}
}

// ---------------------------------------------------------------------------
// Verify that mockProvider and helpers are accessible.
// These compile-time assertions ensure the test file can use engine internals.
// ---------------------------------------------------------------------------

var _ llm.Provider = (*mockProvider)(nil)
var _ tool.Tool = (*mockTool)(nil)
