// Package attachment manages the attachment queue and reminder system.
//
// Reminders are transient messages injected at turn boundaries (after tool
// results, before the next LLM call). They are appended to the message
// history tail — never prepended to the prefix — so prompt cache is
// preserved.
//
// TS reference: utils/attachments.ts — getAttachments() collects per-turn
// attachments; they are pushed into toolResults (appended, not prepended)
// in query.ts:1596.
package attachment

import (
	"fmt"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Reminder infrastructure
// ---------------------------------------------------------------------------

// ReminderProvider generates transient reminder messages injected at turn
// boundaries. Each provider is independent and decides for itself whether
// to fire based on the current turn state.
type ReminderProvider interface {
	// Key returns a unique identifier for this reminder type
	// (e.g. "task_reminder"). Used for logging and diagnostics.
	Key() string

	// ShouldFire decides whether to inject this reminder given the
	// current turn state. Called once per turn for each registered
	// provider.
	ShouldFire(ctx ReminderContext) bool

	// Render produces the message(s) to inject. Called only when
	// ShouldFire returns true. Returned messages are appended to the
	// message history tail (after tool results, before next LLM call).
	// TS reference: utils/messages.ts — each attachment type has a
	// rendering case that produces UserMessages wrapped in
	// <system-reminder> tags.
	Render(ctx ReminderContext) []types.Message
}

// ReminderContext carries the state providers need to make decisions.
// Fields will be added as new provider types are introduced.
type ReminderContext struct {
	// Messages is the current message history (read-only; providers
	// must not mutate).
	Messages []types.Message
	// TurnCount is the engine's turn counter (0-based).
	TurnCount int
	// IsSubagent is true for forked agents.
	IsSubagent bool
	// TaskList provides access to the task store. Nil if tasks are
	// unavailable (e.g. sub-agents that don't own a task list).
	TaskList TaskListReader
}

// TaskListReader is the read-only interface reminder providers use to
// access tasks. Extracted from the engine's concrete taskList to keep
// attachment/ free of engine internals.
type TaskListReader interface {
	// ListPending returns all tasks with status pending or in_progress.
	// Returns nil if no tasks exist or the list is unavailable.
	ListPending() ([]TaskItem, error)
}

// TaskItem is a minimal task representation for reminder rendering.
type TaskItem struct {
	ID          string
	Subject     string
	Status      string
	Description string
}

// ReminderEngine collects and renders reminders from registered providers.
type ReminderEngine struct {
	providers []ReminderProvider
}

// NewReminderEngine creates an engine with the given providers.
func NewReminderEngine(providers ...ReminderProvider) *ReminderEngine {
	return &ReminderEngine{providers: providers}
}

// Collect iterates all registered providers, calls ShouldFire, and
// returns the concatenated Render output. Messages are returned in
// provider registration order.
func (e *ReminderEngine) Collect(ctx ReminderContext) []types.Message {
	var msgs []types.Message
	for _, p := range e.providers {
		if p.ShouldFire(ctx) {
			msgs = append(msgs, p.Render(ctx)...)
		}
	}
	return msgs
}

// Providers returns the registered provider list (for testing/diagnostics).
func (e *ReminderEngine) Providers() []ReminderProvider {
	return e.providers
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

// WrapInSystemReminder wraps content in <system-reminder> tags.
// TS reference: utils/messages.ts:3097 — wrapInSystemReminder.
func WrapInSystemReminder(content string) string {
	return fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", content)
}

// NewMetaUserMessage creates a user message with FlagMeta set (hidden from
// rewind/brief, used for system-injected content).
// TS reference: utils/messages.ts — createUserMessage({ isMeta: true }).
func NewMetaUserMessage(content string) types.Message {
	msg := types.NewUserMessage([]types.ContentBlock{types.NewTextBlock(content)})
	msg.Flags = types.FlagMeta
	return msg
}

// CountAssistantTurnsSinceLastEvent scans messages backwards and counts
// assistant turns since the last occurrence of a matching event.
// Returns (turnsSinceEvent, foundEvent).
// TS reference: utils/attachments.ts:3319 — getTaskReminderTurnCounts.
func CountAssistantTurnsSinceLastEvent(
	messages []types.Message,
	isAssistantToolUse func(content []types.ContentBlock) bool,
	isReminder func(msg types.Message) bool,
) (turnsSince int, foundEvent bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		// Reminder check applies to any role (reminders are user messages).
		if isReminder(msg) {
			return turnsSince, true
		}
		if msg.Role == types.RoleAssistant {
			if isAssistantToolUse(msg.Content) {
				return turnsSince, true
			}
			turnsSince++
		}
	}
	return turnsSince, false
}
