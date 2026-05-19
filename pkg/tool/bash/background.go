package bash

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"syscall"
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
// JobStatus — source: Task.ts:15-21
// ---------------------------------------------------------------------------

// JobStatus represents the lifecycle state of a background job.
// Source: Task.ts:15-21 — JobStatus union
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobKilled    JobStatus = "killed"
)

// IsTerminalJobStatus returns true for terminal states that will not transition further.
// Source: Task.ts:27-29 — isTerminalJobStatus
func IsTerminalJobStatus(s JobStatus) bool {
	return s == JobCompleted || s == JobFailed || s == JobKilled
}

// ---------------------------------------------------------------------------
// JobNotification — source: LocalShellTask.tsx:80-88 (stall), 160-165 (completion)
// ---------------------------------------------------------------------------

// JobNotification represents a notification about a background job state change.
// These are formatted as XML and injected into the LLM conversation so the model
// can react to background job completions, failures, and stalls.
//
// Source: LocalShellTask.tsx:105-172 — enqueueShellNotification + startStallWatchdog
type JobNotification struct {
	JobID      string
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
func (n JobNotification) FormatXML() string {
	var sb strings.Builder
	sb.WriteString("<job-notification>\n")
	fmt.Fprintf(&sb, "<job-id>%s</job-id>\n", escapeXML(n.JobID))
	if n.ToolUseID != "" {
		fmt.Fprintf(&sb, "<tool-use-id>%s</tool-use-id>\n", escapeXML(n.ToolUseID))
	}
	if n.OutputFile != "" {
		fmt.Fprintf(&sb, "<output-file>%s</output-file>\n", escapeXML(n.OutputFile))
	}
	// Source: LocalShellTask.tsx:78-79 — stall notifications have no <status> tag.
	// No <status> tag means print.ts treats it as a progress ping, not terminal.
	if n.Status != "" && !n.IsStall {
		fmt.Fprintf(&sb, "<status>%s</status>\n", escapeXML(n.Status))
	}
	fmt.Fprintf(&sb, "<summary>%s</summary>\n", escapeXML(n.Summary))
	sb.WriteString("</job-notification>")

	// Source: LocalShellTask.tsx:85-88 — stall includes tail and instructions
	if n.IsStall && n.Tail != "" {
		fmt.Fprintf(&sb,
			"\nLast output:\n%s\n\nThe command is likely blocked on an interactive prompt. Kill this job and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists.",
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
// BackgroundJob — source: LocalShellTask.tsx:11-32 + Task.ts:45-57
// ---------------------------------------------------------------------------

// BackgroundJob holds the state of a background shell command.
//
// Source: LocalShellTask.tsx:11-32 — LocalShellTaskState
// Source: Task.ts:45-57 — TaskStateBase
type BackgroundJob struct {
	mu          sync.Mutex
	ID          string
	Command     string
	PID         int
	StartTime   time.Time
	EndTime     time.Time // Source: Task.ts:51 — endTime
	Status      JobStatus
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
	onNotify func(JobNotification)
	// evictAfter is set when the job enters a terminal state.
	// CleanupCompleted() removes tasks whose evictAfter has passed.
	// Source: utils/job/framework.ts:213-249 — applyTaskOffsetsAndEvictions
	evictAfter time.Time
}

// ---------------------------------------------------------------------------
// BackgroundJobRegistry — source: AppState.jobs map in AppStateStore.ts:160
// ---------------------------------------------------------------------------

// BackgroundJobRegistry manages background shell tasks.
// Source: AppState.jobs map in AppStateStore.ts:160
type BackgroundJobRegistry struct {
	mu     sync.Mutex
	jobs  map[string]*BackgroundJob
	nextID int
	// OnNotify is called when a job completes or stalls.
	// Set by the caller (e.g., engine integration) to route notifications
	// into the LLM conversation. Source: LocalShellTask.tsx:89-94 + 166-171
	OnNotify func(JobNotification)
}

// NewBackgroundJobRegistry creates a new registry.
func NewBackgroundJobRegistry() *BackgroundJobRegistry {
	return &BackgroundJobRegistry{
		jobs: make(map[string]*BackgroundJob),
	}
}

// defaultRegistry is the global background job registry.
var defaultRegistry = NewBackgroundJobRegistry()

// DefaultRegistry returns the global background job registry.
func DefaultRegistry() *BackgroundJobRegistry {
	return defaultRegistry
}

// ---------------------------------------------------------------------------
// Spawn — source: LocalShellTask.tsx:180-252 (spawnShellTask)
// ---------------------------------------------------------------------------

// Spawn creates a new background job entry and returns it.
// The caller is responsible for starting the actual command.
//
// Source: LocalShellTask.tsx:180-252 — spawnShellTask()
// TS sets isBackgrounded=true at line 212.
func (r *BackgroundJobRegistry) Spawn(command string, pid int, output *StreamingOutput) *BackgroundJob {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := fmt.Sprintf("bg-%d", r.nextID)

	job := &BackgroundJob{
		ID:             id,
		Command:        command,
		PID:            pid,
		StartTime:      time.Now(),
		Status:         JobRunning,
		Output:         output,
		done:           make(chan struct{}),
		IsBackgrounded: true,
		Kind:           "bash",
		onNotify:       r.OnNotify,
	}

	r.jobs[id] = job
	return job
}

// ---------------------------------------------------------------------------
// RegisterForeground — source: LocalShellTask.tsx:259-287
// ---------------------------------------------------------------------------

// RegisterForeground registers a job as foreground (isBackgrounded=false).
// Called when a bash command has been running long enough to show the background hint.
// No stall watchdog is started — that happens when the job is transitioned to background.
//
// Source: LocalShellTask.tsx:259-287 — registerForeground
func (r *BackgroundJobRegistry) RegisterForeground(command, description string, output *StreamingOutput) *BackgroundJob {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := fmt.Sprintf("bg-%d", r.nextID)

	job := &BackgroundJob{
		ID:             id,
		Command:        command,
		StartTime:      time.Now(),
		Status:         JobRunning,
		Output:         output,
		done:           make(chan struct{}),
		Description:    description,
		IsBackgrounded: false, // foreground — not yet backgrounded
		Kind:           "bash",
		onNotify:       r.OnNotify,
	}

	r.jobs[id] = job
	return job
}

// ---------------------------------------------------------------------------
// Background — source: LocalShellTask.tsx:293-368
// ---------------------------------------------------------------------------

// Background transitions a foreground job to background state.
// Starts the stall watchdog and sets up for completion notification.
// Returns true if the transition was successful.
//
// Source: LocalShellTask.tsx:293-368 — backgroundTask
func (r *BackgroundJobRegistry) Background(id string) bool {
	r.mu.Lock()
	job, ok := r.jobs[id]
	r.mu.Unlock()

	if !ok {
		return false
	}

	job.mu.Lock()

	// Source: LocalShellTask.tsx:297 — guard: must be foreground shell job
	if job.IsBackgrounded || IsTerminalJobStatus(job.Status) {
		job.mu.Unlock()
		return false
	}

	job.IsBackgrounded = true
	job.mu.Unlock()

	// Source: LocalShellTask.tsx:328 — start stall watchdog after backgrounding
	job.startStallWatchdog()

	return true
}

// ---------------------------------------------------------------------------
// BackgroundAll — source: LocalShellTask.tsx:390-410
// ---------------------------------------------------------------------------

// BackgroundAll transitions all foreground tasks to background state.
// Returns the IDs of tasks that were successfully transitioned.
//
// Source: LocalShellTask.tsx:390-410 — backgroundAll
func (r *BackgroundJobRegistry) BackgroundAll() []string {
	r.mu.Lock()
	var foregroundIDs []string
	for id, job := range r.jobs {
		job.mu.Lock()
		if !job.IsBackgrounded && !IsTerminalJobStatus(job.Status) {
			foregroundIDs = append(foregroundIDs, id)
		}
		job.mu.Unlock()
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

// BackgroundExistingForegroundTask transitions a specific foreground job to background.
// Unlike Background(), this does NOT re-register the job — it flips isBackgrounded
// on the existing registration.
//
// Source: LocalShellTask.tsx:420-474 — backgroundExistingForegroundTask
func (r *BackgroundJobRegistry) BackgroundExistingForegroundTask(id string) bool {
	return r.Background(id)
}

// ---------------------------------------------------------------------------
// HasForegroundTasks — source: LocalShellTask.tsx:378-389
// ---------------------------------------------------------------------------

// HasForegroundTasks returns true if there are foreground (non-backgrounded) running tasks.
// Source: LocalShellTask.tsx:378-389 — hasForegroundTasks
func (r *BackgroundJobRegistry) HasForegroundTasks() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, job := range r.jobs {
		job.mu.Lock()
		fg := !job.IsBackgrounded && !IsTerminalJobStatus(job.Status)
		job.mu.Unlock()
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
// carries the full output, so the job_notification would be redundant.
// Returns true if it was newly marked (was not already notified).
//
// Source: LocalShellTask.tsx:481-486 — markTaskNotified
func (r *BackgroundJobRegistry) MarkNotified(id string) bool {
	r.mu.Lock()
	job, ok := r.jobs[id]
	r.mu.Unlock()

	if !ok {
		return false
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	if job.Notified {
		return false
	}
	job.Notified = true
	return true
}

// ---------------------------------------------------------------------------
// UnregisterForeground — source: LocalShellTask.tsx:491-514
// ---------------------------------------------------------------------------

// UnregisterForeground removes a foreground job that completed without being backgrounded.
// Only removes tasks that are NOT backgrounded.
//
// Source: LocalShellTask.tsx:491-514 — unregisterForeground
func (r *BackgroundJobRegistry) UnregisterForeground(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, ok := r.jobs[id]
	if !ok {
		return
	}

	job.mu.Lock()
	isBg := job.IsBackgrounded
	job.mu.Unlock()

	// Source: LocalShellTask.tsx:496 — only remove if foreground
	if isBg {
		return
	}

	delete(r.jobs, id)
}

// ---------------------------------------------------------------------------
// Kill — source: killShellTasks.ts:16-46
// ---------------------------------------------------------------------------

// Kill terminates a background job by sending SIGKILL to its process tree.
// Source: killShellTasks.ts:16-46 — killTask()
func (r *BackgroundJobRegistry) Kill(id string) error {
	r.mu.Lock()
	job, ok := r.jobs[id]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("job %q not found", id)
	}

	job.mu.Lock()

	if job.Status != JobRunning && job.Status != JobPending {
		job.mu.Unlock()
		return fmt.Errorf("job %q is not running (status: %s)", id, job.Status)
	}

	// Stop stall watchdog
	if job.cancelStall != nil {
		job.cancelStall()
		job.cancelStall = nil
	}

	// Kill process tree
	if job.PID > 0 {
		_ = killProcessTree(job.PID)
	}

	job.Status = JobKilled
	job.EndTime = time.Now()
	job.ExitCode = 128 + int(syscall.SIGKILL)
	job.Interrupted = true
	job.evictAfter = time.Now().Add(3 * time.Second) // Source: framework.ts:25 — STOPPED_DISPLAY_MS

	// Send killed notification
	notify := job.buildNotificationLocked("killed")
	job.Notified = true
	close(job.done)
	job.mu.Unlock()

	// Send outside lock
	r.sendNotification(notify)

	return nil
}

// ---------------------------------------------------------------------------
// Complete — source: LocalShellTask.tsx:226-244 (result handler)
// ---------------------------------------------------------------------------

// Complete marks a job as completed with the given exit code.
// The interrupted flag indicates whether the command was killed/timed out.
// Source: LocalShellTask.tsx:226-244 — result handler in spawnShellTask
func (t *BackgroundJob) Complete(exitCode int, interrupted bool) {
	t.mu.Lock()

	if IsTerminalJobStatus(t.Status) {
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
		t.Status = JobCompleted
	} else {
		t.Status = JobFailed
	}
	t.evictAfter = time.Now().Add(3 * time.Second) // Source: framework.ts:25 — STOPPED_DISPLAY_MS

	// Flush output
	// Source: LocalShellTask.tsx:224 — flushAndCleanup(shellCommand)
	if t.Output != nil {
		t.Output.FinalUpdate()
	}

	// Build notification (if not already notified)
	var notify *JobNotification
	if !t.Notified {
		t.Notified = true
		status := "completed"
		if t.Status == JobFailed {
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

// buildNotification creates a JobNotification from the job's current state.
// Must be called with job.mu held.
// Source: LocalShellTask.tsx:146-156 — status-specific summary format
func (t *BackgroundJob) buildNotificationLocked(status string) *JobNotification {
	desc := t.Description
	if desc == "" {
		desc = t.Command
	}
	var summary string
	switch status {
	case "killed":
		summary = fmt.Sprintf("%s\"%s\" was stopped (exit code %d)", BackgroundBashSummaryPrefix, desc, t.ExitCode)
	case "completed":
		// Source: LocalShellTask.tsx:148 — always show exit code
		summary = fmt.Sprintf("%s\"%s\" completed (exit code %d)", BackgroundBashSummaryPrefix, desc, t.ExitCode)
	case "failed":
		// Source: LocalShellTask.tsx:150 — always show exit code
		summary = fmt.Sprintf("%s\"%s\" failed with exit code %d", BackgroundBashSummaryPrefix, desc, t.ExitCode)
	default:
		summary = fmt.Sprintf("%s\"%s\" %s", BackgroundBashSummaryPrefix, desc, status)
	}
	return &JobNotification{
		JobID:     t.ID,
		ToolUseID:  t.ToolUseID,
		Status:     status,
		Summary:    summary,
		OutputFile: t.OutputPath,
	}
}

// buildNotification creates a JobNotification for the registry-level methods.
func (r *BackgroundJobRegistry) buildNotification(job *BackgroundJob, status string) *JobNotification {
	job.mu.Lock()
	defer job.mu.Unlock()
	return job.buildNotificationLocked(status)
}

// sendNotification sends a notification via the registry's OnNotify callback.
func (r *BackgroundJobRegistry) sendNotification(notify *JobNotification) {
	if notify == nil || r.OnNotify == nil {
		return
	}
	r.OnNotify(*notify)
}

// ---------------------------------------------------------------------------
// startStallWatchdog — shared stall watchdog setup
// ---------------------------------------------------------------------------

// startStallWatchdog starts the stall watchdog for a background job.
// Must be called with job.mu NOT held (watchForStallStream spawns a goroutine).
// Source: LocalShellTask.tsx:221, 328, 442 — startStallWatchdog calls
func (t *BackgroundJob) startStallWatchdog() {
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
			t.onNotify(JobNotification{
				JobID:     t.ID,
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

// SetStallCancel sets the stall watchdog cancel function for a job.
func (t *BackgroundJob) SetStallCancel(cancel func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelStall = cancel
}

// ---------------------------------------------------------------------------
// List / Wait / Get / Remove
// ---------------------------------------------------------------------------

// List returns all background jobs.
// Source: framework.ts:149-152 — getRunningTasks()
func (r *BackgroundJobRegistry) List() []*BackgroundJob {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]*BackgroundJob, 0, len(r.jobs))
	for _, job := range r.jobs {
		result = append(result, job)
	}
	return result
}

// Wait blocks until the job completes, is killed, or fails.
// Returns the job's exit code.
// Source: ShellCommand.result Promise pattern
func (r *BackgroundJobRegistry) Wait(id string) (int, error) {
	r.mu.Lock()
	job, ok := r.jobs[id]
	r.mu.Unlock()

	if !ok {
		return -1, fmt.Errorf("job %q not found", id)
	}

	<-job.done

	job.mu.Lock()
	defer job.mu.Unlock()
	return job.ExitCode, nil
}

// Get returns a specific job by ID.
func (r *BackgroundJobRegistry) Get(id string) (*BackgroundJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, ok := r.jobs[id]
	return job, ok
}

// Remove removes a completed/killed/failed job from the registry.
func (r *BackgroundJobRegistry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, id)
}

// CleanupCompleted removes terminal tasks whose evictAfter deadline has passed.
// Called lazily from the job adapter's List() method.
// Source: utils/job/framework.ts:213-249 — applyTaskOffsetsAndEvictions
func (r *BackgroundJobRegistry) CleanupCompleted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var evicted []string
	for id, t := range r.jobs {
		if !t.evictAfter.IsZero() && !now.Before(t.evictAfter) {
			delete(r.jobs, id)
			evicted = append(evicted, id)
		}
	}
	if len(evicted) > 0 {
		slog.Info("bash: cleaned up completed jobs", "count", len(evicted), "ids", evicted)
	}
}
