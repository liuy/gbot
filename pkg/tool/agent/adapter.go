package agent

import (
	"fmt"

	"github.com/liuy/gbot/pkg/tool/job"
)

// Compile-time interface check.
var _ job.Registry = (*ForkAgentJobAdapter)(nil)

// ForkAgentJobAdapter adapts ForkAgentRegistry to the job.Registry interface.
// This enables TaskOutput/TaskStop to manage fork agent tasks alongside bash tasks.
type ForkAgentJobAdapter struct {
	reg *ForkAgentRegistry
}

// NewForkAgentJobAdapter creates an adapter wrapping a ForkAgentRegistry.
// If reg is nil, all methods return not-found.
func NewForkAgentJobAdapter(reg *ForkAgentRegistry) *ForkAgentJobAdapter {
	return &ForkAgentJobAdapter{reg: reg}
}

// Get returns task info by ID.
func (a *ForkAgentJobAdapter) Get(id string) (*job.JobInfo, bool) {
	if a.reg == nil {
		return nil, false
	}
	state, ok := a.reg.Get(id)
	if !ok {
		return nil, false
	}
	return convertState(state), true
}

// Kill cancels a running fork agent by ID.
func (a *ForkAgentJobAdapter) Kill(id string) error {
	if a.reg == nil {
		return fmt.Errorf("kill %q: %w", id, job.ErrNotFound)
	}
	if !a.reg.Cancel(id) {
		return fmt.Errorf("kill %q: %w", id, job.ErrNotFound)
	}
	return nil
}

// List returns all fork agent tasks as TaskInfo snapshots.
// Triggers lazy cleanup for any terminal-state agents.
func (a *ForkAgentJobAdapter) List() []*job.JobInfo {
	if a.reg == nil {
		return nil
	}
	states := a.reg.List()
	result := make([]*job.JobInfo, len(states))
	hasTerminal := false
	for i, s := range states {
		result[i] = convertState(s)
		if s.Status != ForkRunning {
			hasTerminal = true
		}
	}
	if hasTerminal {
		a.reg.CleanupCompleted()
	}
	return result
}

// Wait blocks until the fork agent completes, returning an exit code.
// ForkAgentRegistry.Wait blocks on <-state.done which is closed after the
// goroutine updates Status and Result, so the copy reflects the final state.
func (a *ForkAgentJobAdapter) Wait(id string) (int, error) {
	if a.reg == nil {
		return -1, fmt.Errorf("wait %q: %w", id, job.ErrNotFound)
	}
	state, ok := a.reg.Wait(id)
	if !ok {
		return -1, fmt.Errorf("wait %q: %w", id, job.ErrNotFound)
	}
	info := convertState(state)
	return info.ExitCode, nil
}

// convertState maps a ForkAgentState snapshot to a job.JobInfo.
// ForkCancelled maps to "killed" to match TS TaskOutputTool's status vocabulary.
func convertState(s *ForkAgentState) *job.JobInfo {
	info := &job.JobInfo{
		ID:          s.ID,
		Type:        "local_agent",
		Description: s.Description,
	}
	switch s.Status {
	case ForkCompleted:
		info.Status = "completed"
		info.ExitCode = 0
	case ForkFailed:
		info.Status = "failed"
		info.ExitCode = 1
	case ForkCancelled:
		info.Status = "killed"
		info.ExitCode = -1
	case ForkRunning:
		info.Status = "running"
	}
	if s.Result != nil {
		info.Output = s.Result.Content
		info.AgentType = s.Result.AgentType
		info.Tokens = s.Result.TotalTokens
		info.DurationMs = s.Result.TotalDurationMs
	} else if s.Status == ForkFailed {
		// Failed but no Result — provide a generic error message.
		info.Output = "agent failed with no result"
	}
	return info
}

// Prefix returns the ID prefix for fork agent tasks.
func (a *ForkAgentJobAdapter) Prefix() string { return "fork-" }
