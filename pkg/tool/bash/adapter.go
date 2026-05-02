package bash

import (
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool/job"
)

// JobInfoAdapter adapts BackgroundTaskRegistry to the job.Registry interface.
// This is the bridge between the bash package's internal BackgroundTask type
// and the task package's public TaskInfo type.
type JobInfoAdapter struct {
	reg *BackgroundTaskRegistry
}

// NewJobInfoAdapter creates an adapter wrapping a BackgroundTaskRegistry.
func NewJobInfoAdapter(reg *BackgroundTaskRegistry) *JobInfoAdapter {
	return &JobInfoAdapter{reg: reg}
}

// Get returns task info by ID.
func (a *JobInfoAdapter) Get(id string) (*job.JobInfo, bool) {
	bt, ok := a.reg.Get(id)
	if !ok {
		return nil, false
	}
	return backgroundTaskToInfo(bt), true
}

// Kill terminates a running task by ID.
// Translates bash-specific "not found" errors into job.ErrNotFound
// so that MultiRegistry can properly skip to the next registry.
func (a *JobInfoAdapter) Kill(id string) error {
	err := a.reg.Kill(id)
	if err != nil && isBashNotFound(err) {
		return fmt.Errorf("kill %q: %w", id, job.ErrNotFound)
	}
	return err
}

// List returns all tasks.
func (a *JobInfoAdapter) List() []*job.JobInfo {
	tasks := a.reg.List()
	result := make([]*job.JobInfo, len(tasks))
	for i, bt := range tasks {
		result[i] = backgroundTaskToInfo(bt)
	}
	return result
}

// Wait blocks until the task finishes, returning the exit code.
// Translates bash-specific "not found" errors into job.ErrNotFound.
func (a *JobInfoAdapter) Wait(id string) (int, error) {
	code, err := a.reg.Wait(id)
	if err != nil && isBashNotFound(err) {
		return code, fmt.Errorf("wait %q: %w", id, job.ErrNotFound)
	}
	return code, err
}

// isBashNotFound detects the bash registry's ad-hoc "task X not found" error.
// BackgroundTaskRegistry doesn't import the task package, so it uses
// fmt.Errorf("task %q not found") instead of wrapping job.ErrNotFound.
func isBashNotFound(err error) bool {
	return strings.Contains(err.Error(), "not found")
}

// backgroundTaskToInfo converts a BackgroundTask to a TaskInfo snapshot.
func backgroundTaskToInfo(bt *BackgroundTask) *job.JobInfo {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	info := &job.JobInfo{
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

// Prefix returns the ID prefix for bash background tasks.
func (a *JobInfoAdapter) Prefix() string { return "bg-" }
