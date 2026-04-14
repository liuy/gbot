package bash

import (
	"github.com/liuy/gbot/pkg/tool/task"
)

// TaskInfoAdapter adapts BackgroundTaskRegistry to the task.Registry interface.
// This is the bridge between the bash package's internal BackgroundTask type
// and the task package's public TaskInfo type.
type TaskInfoAdapter struct {
	reg *BackgroundTaskRegistry
}

// NewTaskInfoAdapter creates an adapter wrapping a BackgroundTaskRegistry.
func NewTaskInfoAdapter(reg *BackgroundTaskRegistry) *TaskInfoAdapter {
	return &TaskInfoAdapter{reg: reg}
}

// Get returns task info by ID.
func (a *TaskInfoAdapter) Get(id string) (*task.TaskInfo, bool) {
	bt, ok := a.reg.Get(id)
	if !ok {
		return nil, false
	}
	return backgroundTaskToInfo(bt), true
}

// Kill terminates a running task by ID.
func (a *TaskInfoAdapter) Kill(id string) error {
	return a.reg.Kill(id)
}

// List returns all tasks.
func (a *TaskInfoAdapter) List() []*task.TaskInfo {
	tasks := a.reg.List()
	result := make([]*task.TaskInfo, len(tasks))
	for i, bt := range tasks {
		result[i] = backgroundTaskToInfo(bt)
	}
	return result
}

// Wait blocks until the task finishes, returning the exit code.
func (a *TaskInfoAdapter) Wait(id string) (int, error) {
	return a.reg.Wait(id)
}

// backgroundTaskToInfo converts a BackgroundTask to a TaskInfo snapshot.
func backgroundTaskToInfo(bt *BackgroundTask) *task.TaskInfo {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	info := &task.TaskInfo{
		ID:          bt.ID,
		Type:        "local_bash",
		Status:      string(bt.Status),
		Command:     bt.Command,
		Description: bt.Description,
		ExitCode:    bt.ExitCode,
	}

	if bt.Output != nil {
		info.Output = bt.Output.String()
	}

	return info
}
