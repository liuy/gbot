package bash

import (
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Background task registry — manages background shell commands
// Source: LocalShellTask.tsx:180-252, AppState.tasks, TaskStateBase
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

// BackgroundTask holds the state of a background shell command.
// Source: LocalShellTask.tsx:11-32 — LocalShellTaskState
// Source: Task.ts:45-57 — TaskStateBase
type BackgroundTask struct {
	mu        sync.Mutex
	ID        string
	Command   string
	PID       int
	StartTime time.Time
	Status    TaskStatus
	ExitCode  int
	OutputPath string
	Output    *StreamingOutput
	cancelStall func()
	done      chan struct{}
	// Additional context
	CWD        string
	Description string
	ToolUseID  string
}

// BackgroundTaskRegistry manages background shell tasks.
// Source: AppState.tasks map in AppStateStore.ts:160
type BackgroundTaskRegistry struct {
	mu    sync.Mutex
	tasks map[string]*BackgroundTask
	nextID int
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

// Spawn creates a new background task entry and returns it.
// The caller is responsible for starting the actual command.
//
// Source: LocalShellTask.tsx:180-252 — spawnShellTask()
func (r *BackgroundTaskRegistry) Spawn(command string, pid int, output *StreamingOutput) *BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := fmt.Sprintf("bg-%d", r.nextID)

	task := &BackgroundTask{
		ID:        id,
		Command:   command,
		PID:       pid,
		StartTime: time.Now(),
		Status:    TaskRunning,
		Output:    output,
		done:      make(chan struct{}),
	}

	r.tasks[id] = task
	return task
}

// SetStallCancel sets the stall watchdog cancel function for a task.
func (t *BackgroundTask) SetStallCancel(cancel func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelStall = cancel
}

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
	defer task.mu.Unlock()

	if task.Status != TaskRunning && task.Status != TaskPending {
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
	close(task.done)
	return nil
}

// Complete marks a task as completed with the given exit code.
func (t *BackgroundTask) Complete(exitCode int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cancelStall != nil {
		t.cancelStall()
		t.cancelStall = nil
	}

	if exitCode == 0 {
		t.Status = TaskCompleted
	} else {
		t.Status = TaskFailed
	}
	t.ExitCode = exitCode
	close(t.done)
}

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
