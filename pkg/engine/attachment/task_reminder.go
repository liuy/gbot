package attachment

import (
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// TaskReminderProvider
// ---------------------------------------------------------------------------

// TaskReminderConfig controls when task reminders fire.
// TS reference: utils/attachments.ts:254 — TODO_REMINDER_CONFIG.
type TaskReminderConfig struct {
	TurnsSinceTaskAction  int // min assistant turns since last TaskCreate/TaskUpdate
	TurnsSinceLastReminder int // min assistant turns since last task_reminder injection
}

// DefaultTaskReminderConfig matches TS defaults.
var DefaultTaskReminderConfig = TaskReminderConfig{
	TurnsSinceTaskAction:   10,
	TurnsSinceLastReminder: 10,
}

// TaskReminderProvider injects a <system-reminder> listing pending tasks
// when the LLM hasn't used task tools recently.
// TS reference: utils/attachments.ts:3375 — getTaskReminderAttachments.
type TaskReminderProvider struct {
	Config TaskReminderConfig
}

// NewTaskReminderProvider creates a provider with default config.
func NewTaskReminderProvider() *TaskReminderProvider {
	return &TaskReminderProvider{Config: DefaultTaskReminderConfig}
}

// Key implements ReminderProvider.
func (p *TaskReminderProvider) Key() string { return "task_reminder" }

// ShouldFire implements ReminderProvider.
// TS reference: utils/attachments.ts:3406-3421.
func (p *TaskReminderProvider) ShouldFire(ctx ReminderContext) bool {
	if ctx.IsSubagent {
		return false
	}
	if ctx.TaskList == nil {
		return false
	}
	tasks, err := ctx.TaskList.ListPending()
	if err != nil || len(tasks) == 0 {
		return false
	}
	turnsSinceTaskAction, _ := CountAssistantTurnsSinceLastEvent(
		ctx.Messages,
		isTaskToolUse,
		isTaskReminderMessage,
	)
	turnsSinceReminder, _ := CountAssistantTurnsSinceLastEvent(
		ctx.Messages,
		func(content []types.ContentBlock) bool { return false },
		isTaskReminderMessage,
	)
	if turnsSinceTaskAction < p.Config.TurnsSinceTaskAction {
		return false
	}
	if turnsSinceReminder < p.Config.TurnsSinceLastReminder {
		return false
	}
	return true
}

// Render implements ReminderProvider.
// TS reference: utils/messages.ts:3680 — task_reminder case.
func (p *TaskReminderProvider) Render(ctx ReminderContext) []types.Message {
	tasks, err := ctx.TaskList.ListPending()
	if err != nil || len(tasks) == 0 {
		return nil
	}
	taskItems := FormatTaskList(tasks)
	var b strings.Builder
	b.WriteString("The task tools haven't been used recently. If you're working on tasks that would benefit from tracking progress, consider using Task to add new tasks and Task to update task status (set to in_progress when starting, completed when done). Consider cleaning up the task list if it has become stale. Only use these if relevant to the current work. Make sure that you NEVER mention this reminder to the user")
	if taskItems != "" {
		b.WriteString("\n\nHere are the existing tasks:\n\n")
		b.WriteString(taskItems)
	}
	content := WrapInSystemReminder(b.String())
	return []types.Message{NewMetaUserMessage(content)}
}

// ---------------------------------------------------------------------------
// Predicates for CountAssistantTurnsSinceLastEvent
// ---------------------------------------------------------------------------

// isTaskToolUse returns true if the content blocks contain a tool_use for
// the Task tool. gbot uses a unified "Task" tool (vs TS's separate
// TaskCreate/TaskUpdate).
func isTaskToolUse(content []types.ContentBlock) bool {
	for _, block := range content {
		if block.Type == types.ContentTypeToolUse {
			if block.Name == "Task" {
				return true
			}
		}
	}
	return false
}

// isTaskReminderMessage returns true if the message is a task reminder
// (meta user message containing the task reminder marker text).
// TS reference: utils/attachments.ts:3356 — checks attachment.type === 'task_reminder'.
func isTaskReminderMessage(msg types.Message) bool {
	if msg.Role != types.RoleUser || msg.Flags&types.FlagMeta == 0 {
		return false
	}
	for _, block := range msg.Content {
		if block.Type == types.ContentTypeText {
			if strings.Contains(block.Text, "The task tools haven't been used recently") {
				return true
			}
		}
	}
	return false
}

// TaskReminderText is the marker string used to identify task reminder
// messages in the conversation history.
const TaskReminderText = "The task tools haven't been used recently"

// Ensure TaskReminderProvider implements ReminderProvider.
var _ ReminderProvider = (*TaskReminderProvider)(nil)

// FormatTaskList renders a slice of TaskItems into the TS-compatible
// reminder text format.
// TS reference: utils/messages.ts:3680 — task_reminder case.
func FormatTaskList(items []TaskItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range items {
		fmt.Fprintf(&b, "#%s. [%s] %s", t.ID, t.Status, t.Subject)
		if t.Description != "" {
			fmt.Fprintf(&b, ": %s", t.Description)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
