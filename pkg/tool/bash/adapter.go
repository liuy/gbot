package bash

import (
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool/job"
)

// JobInfoAdapter adapts BackgroundJobRegistry to the job.Registry interface.
// This is the bridge between the bash package's internal BackgroundJob type
// and the job package's public TaskInfo type.
type JobInfoAdapter struct {
	reg *BackgroundJobRegistry
}

// NewJobInfoAdapter creates an adapter wrapping a BackgroundJobRegistry.
func NewJobInfoAdapter(reg *BackgroundJobRegistry) *JobInfoAdapter {
	return &JobInfoAdapter{reg: reg}
}

// Get returns job info by ID.
func (a *JobInfoAdapter) Get(id string) (*job.JobInfo, bool) {
	bt, ok := a.reg.Get(id)
	if !ok {
		return nil, false
	}
	return backgroundJobToInfo(bt), true
}

// Kill terminates a running job by ID.
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
	a.reg.CleanupCompleted() // Lazy cleanup — matches ForkAgentRegistry pattern
	tasks := a.reg.List()
	result := make([]*job.JobInfo, len(tasks))
	for i, bt := range tasks {
		result[i] = backgroundJobToInfo(bt)
	}
	return result
}

// Wait blocks until the job finishes, returning the exit code.
// Translates bash-specific "not found" errors into job.ErrNotFound.
func (a *JobInfoAdapter) Wait(id string) (int, error) {
	code, err := a.reg.Wait(id)
	if err != nil && isBashNotFound(err) {
		return code, fmt.Errorf("wait %q: %w", id, job.ErrNotFound)
	}
	return code, err
}

// isBashNotFound detects the bash registry's ad-hoc "job X not found" error.
// BackgroundJobRegistry doesn't import the job package, so it uses
// fmt.Errorf("job %q not found") instead of wrapping job.ErrNotFound.
func isBashNotFound(err error) bool {
	return strings.Contains(err.Error(), "not found")
}

// backgroundJobToInfo converts a BackgroundJob to a TaskInfo snapshot.
func backgroundJobToInfo(bt *BackgroundJob) *job.JobInfo {
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

// Prefix returns the ID prefix for bash background jobs.
func (a *JobInfoAdapter) Prefix() string { return "bg-" }
