package bash

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Constants
// Source: LocalShellTask.tsx:23 — BACKGROUND_BASH_SUMMARY_PREFIX
// ---------------------------------------------------------------------------

// BackgroundBashSummaryPrefix is the prefix for background command notifications.
// Source: LocalShellTask.tsx:23
const BackgroundBashSummaryPrefix = `Background command `

// ---------------------------------------------------------------------------
// TaskStatus — source: Task.ts:15-21
// ---------------------------------------------------------------------------

// TaskStatus represents the lifecycle state of a background task.
// Source: Task.ts:15-21 — TaskStatus union
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskKilled    TaskStatus = "killed"
)

// IsTerminalTaskStatus returns true for terminal states that will not transition further.
// Source: Task.ts:27-29 — isTerminalTaskStatus
func IsTerminalTaskStatus(s TaskStatus) bool {
	return s == TaskCompleted || s == TaskFailed || s == TaskKilled
}

// ---------------------------------------------------------------------------
// TaskNotification — source: LocalShellTask.tsx:80-88 (stall), 160-165 (completion)
// ---------------------------------------------------------------------------

// TaskNotification represents a notification about a background task state change.
// These are formatted as XML and injected into the LLM conversation so the model
// can react to background task completions, failures, and stalls.
//
// Source: LocalShellTask.tsx:105-172 — enqueueShellNotification + startStallWatchdog
type TaskNotification struct {
	TaskID     string
	ToolUseID  string
	Status     string // "completed", "failed", "killed", or "" (stall — no status tag)
	Summary    string
	OutputFile string
	IsStall    bool
	Tail       string // last output for stall notifications
}

// FormatXML returns the notification formatted as XML for LLM injection.
//
// Source: LocalShellTask.tsx:80-88 (stall notification format)
// Source: LocalShellTask.tsx:160-165 (completion notification format)
func (n TaskNotification) FormatXML() string {
	var sb strings.Builder
	sb.WriteString("<task-notification>\n")
	fmt.Fprintf(&sb, "<task-id>%s</task-id>\n", n.TaskID)
	if n.ToolUseID != "" {
		fmt.Fprintf(&sb, "<tool-use-id>%s</tool-use-id>\n", n.ToolUseID)
	}
	if n.OutputFile != "" {
		fmt.Fprintf(&sb, "<output-file>%s</output-file>\n", n.OutputFile)
	}
	// Source: LocalShellTask.tsx:78-79 — stall notifications have no <status> tag.
	// No <status> tag means print.ts treats it as a progress ping, not terminal.
	if n.Status != "" && !n.IsStall {
		fmt.Fprintf(&sb, "<status>%s</status>\n", n.Status)
	}
	fmt.Fprintf(&sb, "<summary>%s</summary>\n", escapeXML(n.Summary))
	sb.WriteString("</task-notification>")

	// Source: LocalShellTask.tsx:85-88 — stall includes tail and instructions
	if n.IsStall && n.Tail != "" {
		fmt.Fprintf(&sb,
			"\nLast output:\n%s\n\nThe command is likely blocked on an interactive prompt. Kill this task and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists.",
			strings.TrimSpace(n.Tail),
		)
	}

	return sb.String()
}

// escapeXML escapes special characters for XML content.
// Source: utils/xml.ts — escapeXml
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// ---------------------------------------------------------------------------
// BackgroundTask — source: LocalShellTask.tsx:11-32 + Task.ts:45-57
// ---------------------------------------------------------------------------

// BackgroundTask holds the state of a background shell command.
//
// Source: LocalShellTask.tsx:11-32 — LocalShellTaskState
// Source: Task.ts:45-57 — TaskStateBase
type BackgroundTask struct {
	mu          sync.Mutex
	ID          string
	Command     string
	PID         int
	StartTime   time.Time
	EndTime     time.Time // Source: Task.ts:51 — endTime
	Status      TaskStatus
	ExitCode    int
	Interrupted bool  // Source: guards.ts:15 — result.interrupted
	OutputPath  string
	Output      *StreamingOutput
	cancelStall func()
	done        chan struct{}
	// Context fields
	CWD         string
	Description string
	ToolUseID   string
	// Source: Task.ts:56 — notified
	Notified bool
	// Source: guards.ts:25 — isBackgrounded
	IsBackgrounded bool
	// Source: guards.ts:31 — kind ("bash" or "monitor")
	Kind string
	// Source: guards.ts:28 — agentId
	AgentID string
	// Notification callback — copied from registry at spawn time.
	onNotify func(TaskNotification)
}

// ---------------------------------------------------------------------------
// BackgroundTaskRegistry — source: AppState.tasks map in AppStateStore.ts:160
// ---------------------------------------------------------------------------

// BackgroundTaskRegistry manages background shell tasks.
// Source: AppState.tasks map in AppStateStore.ts:160
type BackgroundTaskRegistry struct {
	mu     sync.Mutex
	tasks  map[string]*BackgroundTask
	nextID int
	// OnNotify is called when a task completes or stalls.
	// Set by the caller (e.g., engine integration) to route notifications
	// into the LLM conversation. Source: LocalShellTask.tsx:89-94 + 166-171
	OnNotify func(TaskNotification)
}

// NewBackgroundTaskRegistry creates a new registry.
func NewBackgroundTaskRegistry() *BackgroundTaskRegistry {
	return &BackgroundTaskRegistry{
		tasks: make(map[string]*BackgroundTask),
	}
}

// defaultRegistry is the global background task registry.
var defaultRegistry = NewBackgroundTaskRegistry()

// DefaultRegistry returns the global background task registry.
func DefaultRegistry() *BackgroundTaskRegistry {
	return defaultRegistry
}

// ---------------------------------------------------------------------------
// Spawn — source: LocalShellTask.tsx:180-252 (spawnShellTask)
// ---------------------------------------------------------------------------

// Spawn creates a new background task entry and returns it.
// The caller is responsible for starting the actual command.
//
// Source: LocalShellTask.tsx:180-252 — spawnShellTask()
// TS sets isBackgrounded=true at line 212.
func (r *BackgroundTaskRegistry) Spawn(command string, pid int, output *StreamingOutput) *BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := fmt.Sprintf("bg-%d", r.nextID)

	task := &BackgroundTask{
		ID:             id,
		Command:        command,
		PID:            pid,
		StartTime:      time.Now(),
		Status:         TaskRunning,
		Output:         output,
		done:           make(chan struct{}),
		IsBackgrounded: true,
		Kind:           "bash",
		onNotify:       r.OnNotify,
	}

	r.tasks[id] = task
	return task
}

// ---------------------------------------------------------------------------
// RegisterForeground — source: LocalShellTask.tsx:259-287
// ---------------------------------------------------------------------------

// RegisterForeground registers a task as foreground (isBackgrounded=false).
// Called when a bash command has been running long enough to show the background hint.
// No stall watchdog is started — that happens when the task is transitioned to background.
//
// Source: LocalShellTask.tsx:259-287 — registerForeground
func (r *BackgroundTaskRegistry) RegisterForeground(command, description string, output *StreamingOutput) *BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := fmt.Sprintf("bg-%d", r.nextID)

	task := &BackgroundTask{
		ID:             id,
		Command:        command,
		StartTime:      time.Now(),
		Status:         TaskRunning,
		Output:         output,
		done:           make(chan struct{}),
		Description:    description,
		IsBackgrounded: false, // foreground — not yet backgrounded
		Kind:           "bash",
		onNotify:       r.OnNotify,
	}

	r.tasks[id] = task
	return task
}

// ---------------------------------------------------------------------------
// Background — source: LocalShellTask.tsx:293-368
// ---------------------------------------------------------------------------

// Background transitions a foreground task to background state.
// Starts the stall watchdog and sets up for completion notification.
// Returns true if the transition was successful.
//
// Source: LocalShellTask.tsx:293-368 — backgroundTask
func (r *BackgroundTaskRegistry) Background(id string) bool {
	r.mu.Lock()
	task, ok := r.tasks[id]
	r.mu.Unlock()

	if !ok {
		return false
	}

	task.mu.Lock()

	// Source: LocalShellTask.tsx:297 — guard: must be foreground shell task
	if task.IsBackgrounded || IsTerminalTaskStatus(task.Status) {
		task.mu.Unlock()
		return false
	}

	task.IsBackgrounded = true
	task.mu.Unlock()

	// Source: LocalShellTask.tsx:328 — start stall watchdog after backgrounding
	task.startStallWatchdog()

	return true
}

// ---------------------------------------------------------------------------
// BackgroundAll — source: LocalShellTask.tsx:390-410
// ---------------------------------------------------------------------------

// BackgroundAll transitions all foreground tasks to background state.
// Returns the IDs of tasks that were successfully transitioned.
//
// Source: LocalShellTask.tsx:390-410 — backgroundAll
func (r *BackgroundTaskRegistry) BackgroundAll() []string {
	r.mu.Lock()
	var foregroundIDs []string
	for id, task := range r.tasks {
		task.mu.Lock()
		if !task.IsBackgrounded && !IsTerminalTaskStatus(task.Status) {
			foregroundIDs = append(foregroundIDs, id)
		}
		task.mu.Unlock()
	}
	r.mu.Unlock()

	var transitioned []string
	for _, id := range foregroundIDs {
		if r.Background(id) {
			transitioned = append(transitioned, id)
		}
	}
	return transitioned
}

// ---------------------------------------------------------------------------
// BackgroundExistingForegroundTask — source: LocalShellTask.tsx:420-474
// ---------------------------------------------------------------------------

// BackgroundExistingForegroundTask transitions a specific foreground task to background.
// Unlike Background(), this does NOT re-register the task — it flips isBackgrounded
// on the existing registration.
//
// Source: LocalShellTask.tsx:420-474 — backgroundExistingForegroundTask
func (r *BackgroundTaskRegistry) BackgroundExistingForegroundTask(id string) bool {
	return r.Background(id)
}

// ---------------------------------------------------------------------------
// HasForegroundTasks — source: LocalShellTask.tsx:378-389
// ---------------------------------------------------------------------------

// HasForegroundTasks returns true if there are foreground (non-backgrounded) running tasks.
// Source: LocalShellTask.tsx:378-389 — hasForegroundTasks
func (r *BackgroundTaskRegistry) HasForegroundTasks() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, task := range r.tasks {
		task.mu.Lock()
		fg := !task.IsBackgrounded && !IsTerminalTaskStatus(task.Status)
		task.mu.Unlock()
		if fg {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// MarkNotified — source: LocalShellTask.tsx:481-486
// ---------------------------------------------------------------------------

// MarkNotified atomically sets the notified flag.
// Used when backgrounding raced with completion — the tool result already
// carries the full output, so the task_notification would be redundant.
// Returns true if it was newly marked (was not already notified).
//
// Source: LocalShellTask.tsx:481-486 — markTaskNotified
func (r *BackgroundTaskRegistry) MarkNotified(id string) bool {
	r.mu.Lock()
	task, ok := r.tasks[id]
	r.mu.Unlock()

	if !ok {
		return false
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	if task.Notified {
		return false
	}
	task.Notified = true
	return true
}

// ---------------------------------------------------------------------------
// UnregisterForeground — source: LocalShellTask.tsx:491-514
// ---------------------------------------------------------------------------

// UnregisterForeground removes a foreground task that completed without being backgrounded.
// Only removes tasks that are NOT backgrounded.
//
// Source: LocalShellTask.tsx:491-514 — unregisterForeground
func (r *BackgroundTaskRegistry) UnregisterForeground(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	task, ok := r.tasks[id]
	if !ok {
		return
	}

	task.mu.Lock()
	isBg := task.IsBackgrounded
	task.mu.Unlock()

	// Source: LocalShellTask.tsx:496 — only remove if foreground
	if isBg {
		return
	}

	delete(r.tasks, id)
}

// ---------------------------------------------------------------------------
// Kill — source: killShellTasks.ts:16-46
// ---------------------------------------------------------------------------

// Kill terminates a background task by sending SIGKILL to its process tree.
// Source: killShellTasks.ts:16-46 — killTask()
func (r *BackgroundTaskRegistry) Kill(id string) error {
	r.mu.Lock()
	task, ok := r.tasks[id]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("task %q not found", id)
	}

	task.mu.Lock()

	if task.Status != TaskRunning && task.Status != TaskPending {
		task.mu.Unlock()
		return fmt.Errorf("task %q is not running (status: %s)", id, task.Status)
	}

	// Stop stall watchdog
	if task.cancelStall != nil {
		task.cancelStall()
		task.cancelStall = nil
	}

	// Kill process tree
	if task.PID > 0 {
		_ = killProcessTree(task.PID)
	}

	task.Status = TaskKilled
	task.EndTime = time.Now()
	task.Interrupted = true

	// Send killed notification
	notify := task.buildNotificationLocked("killed")
	task.Notified = true
	close(task.done)
	task.mu.Unlock()

	// Send outside lock
	r.sendNotification(notify)

	return nil
}

// ---------------------------------------------------------------------------
// Complete — source: LocalShellTask.tsx:226-244 (result handler)
// ---------------------------------------------------------------------------

// Complete marks a task as completed with the given exit code.
// The interrupted flag indicates whether the command was killed/timed out.
// Source: LocalShellTask.tsx:226-244 — result handler in spawnShellTask
func (t *BackgroundTask) Complete(exitCode int, interrupted bool) {
	t.mu.Lock()

	if IsTerminalTaskStatus(t.Status) {
		t.mu.Unlock()
		return
	}

	// Stop stall watchdog
	if t.cancelStall != nil {
		t.cancelStall()
		t.cancelStall = nil
	}

	t.EndTime = time.Now()
	t.ExitCode = exitCode
	t.Interrupted = interrupted

	if exitCode == 0 {
		t.Status = TaskCompleted
	} else {
		t.Status = TaskFailed
	}

	// Flush output
	// Source: LocalShellTask.tsx:224 — flushAndCleanup(shellCommand)
	if t.Output != nil {
		t.Output.FinalUpdate()
	}

	// Build notification (if not already notified)
	var notify *TaskNotification
	if !t.Notified {
		t.Notified = true
		status := "completed"
		if t.Status == TaskFailed {
			status = "failed"
		}
		notify = t.buildNotificationLocked(status)
	}

	close(t.done)
	t.mu.Unlock()

	// Send notification outside lock
	if notify != nil && t.onNotify != nil {
		t.onNotify(*notify)
	}
}

// buildNotification creates a TaskNotification from the task's current state.
// Must be called with task.mu held.
// Source: LocalShellTask.tsx:146-156 — status-specific summary format
func (t *BackgroundTask) buildNotificationLocked(status string) *TaskNotification {
	desc := t.Description
	if desc == "" {
		desc = t.Command
	}
	var summary string
	switch status {
	case "killed":
		// Source: LocalShellTask.tsx:154 — "was stopped", no exit code
		summary = fmt.Sprintf("%s\"%s\" was stopped", BackgroundBashSummaryPrefix, desc)
	case "completed":
		// Source: LocalShellTask.tsx:148 — always show exit code
		summary = fmt.Sprintf("%s\"%s\" completed (exit code %d)", BackgroundBashSummaryPrefix, desc, t.ExitCode)
	case "failed":
		// Source: LocalShellTask.tsx:150 — always show exit code
		summary = fmt.Sprintf("%s\"%s\" failed with exit code %d", BackgroundBashSummaryPrefix, desc, t.ExitCode)
	default:
		summary = fmt.Sprintf("%s\"%s\" %s", BackgroundBashSummaryPrefix, desc, status)
	}
	return &TaskNotification{
		TaskID:     t.ID,
		ToolUseID:  t.ToolUseID,
		Status:     status,
		Summary:    summary,
		OutputFile: t.OutputPath,
	}
}

// buildNotification creates a TaskNotification for the registry-level methods.
func (r *BackgroundTaskRegistry) buildNotification(task *BackgroundTask, status string) *TaskNotification {
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.buildNotificationLocked(status)
}

// sendNotification sends a notification via the registry's OnNotify callback.
func (r *BackgroundTaskRegistry) sendNotification(notify *TaskNotification) {
	if notify == nil || r.OnNotify == nil {
		return
	}
	r.OnNotify(*notify)
}

// ---------------------------------------------------------------------------
// startStallWatchdog — shared stall watchdog setup
// ---------------------------------------------------------------------------

// startStallWatchdog starts the stall watchdog for a background task.
// Must be called with task.mu NOT held (watchForStallStream spawns a goroutine).
// Source: LocalShellTask.tsx:221, 328, 442 — startStallWatchdog calls
func (t *BackgroundTask) startStallWatchdog() {
	if t.Output == nil || t.Kind == "monitor" {
		return
	}

	t.mu.Lock()
	t.cancelStall = watchForStallStream(t, func(summary, tail string) {
		t.mu.Lock()
		if t.Notified {
			t.mu.Unlock()
			return
		}
		t.Notified = true
		t.mu.Unlock()

		desc := t.Description
		if desc == "" {
			desc = t.Command
		}
		stallSummary := fmt.Sprintf("%s\"%s\" %s", BackgroundBashSummaryPrefix, desc, summary)
		if t.onNotify != nil {
			t.onNotify(TaskNotification{
				TaskID:     t.ID,
				ToolUseID:  t.ToolUseID,
				Summary:    stallSummary,
				OutputFile: t.OutputPath,
				IsStall:    true,
				Tail:       tail,
			})
		}
	})
	t.mu.Unlock()
}

// ---------------------------------------------------------------------------
// SetStallCancel
// ---------------------------------------------------------------------------

// SetStallCancel sets the stall watchdog cancel function for a task.
func (t *BackgroundTask) SetStallCancel(cancel func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelStall = cancel
}

// ---------------------------------------------------------------------------
// List / Wait / Get / Remove
// ---------------------------------------------------------------------------

// List returns all background tasks.
// Source: framework.ts:149-152 — getRunningTasks()
func (r *BackgroundTaskRegistry) List() []*BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]*BackgroundTask, 0, len(r.tasks))
	for _, task := range r.tasks {
		result = append(result, task)
	}
	return result
}

// Wait blocks until the task completes, is killed, or fails.
// Returns the task's exit code.
// Source: ShellCommand.result Promise pattern
func (r *BackgroundTaskRegistry) Wait(id string) (int, error) {
	r.mu.Lock()
	task, ok := r.tasks[id]
	r.mu.Unlock()

	if !ok {
		return -1, fmt.Errorf("task %q not found", id)
	}

	<-task.done

	task.mu.Lock()
	defer task.mu.Unlock()
	return task.ExitCode, nil
}

// Get returns a specific task by ID.
func (r *BackgroundTaskRegistry) Get(id string) (*BackgroundTask, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	task, ok := r.tasks[id]
	return task, ok
}

// Remove removes a completed/killed/failed task from the registry.
func (r *BackgroundTaskRegistry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks, id)
}
