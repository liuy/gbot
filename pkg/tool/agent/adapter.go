package agent

import (
	"fmt"

	"github.com/liuy/gbot/pkg/tool/job"
)

// Compile-time interface check.
var _ job.Registry = (*BackgroundAgentJobAdapter)(nil)

// BackgroundAgentJobAdapter adapts BackgroundAgentRegistry to the job.Registry interface.
// This enables TaskOutput/TaskStop to manage background agent tasks alongside bash tasks.
type BackgroundAgentJobAdapter struct {
	reg *BackgroundAgentRegistry
}

// NewBackgroundAgentJobAdapter creates an adapter wrapping a BackgroundAgentRegistry.
// If reg is nil, all methods return not-found.
func NewBackgroundAgentJobAdapter(reg *BackgroundAgentRegistry) *BackgroundAgentJobAdapter {
	return &BackgroundAgentJobAdapter{reg: reg}
}

// Get returns task info by ID.
func (a *BackgroundAgentJobAdapter) Get(id string) (*job.JobInfo, bool) {
	if a.reg == nil {
		return nil, false
	}
	state, ok := a.reg.Get(id)
	if !ok {
		return nil, false
	}
	return convertState(state), true
}

// Kill cancels a running background agent by ID.
func (a *BackgroundAgentJobAdapter) Kill(id string) error {
	if a.reg == nil {
		return fmt.Errorf("kill %q: %w", id, job.ErrNotFound)
	}
	if !a.reg.Cancel(id) {
		return fmt.Errorf("kill %q: %w", id, job.ErrNotFound)
	}
	return nil
}

// List returns all background agent tasks as TaskInfo snapshots.
// Triggers lazy cleanup for any terminal-state agents.
func (a *BackgroundAgentJobAdapter) List() []*job.JobInfo {
	if a.reg == nil {
		return nil
	}
	states := a.reg.List()
	result := make([]*job.JobInfo, len(states))
	hasTerminal := false
	for i, s := range states {
		result[i] = convertState(s)
		if s.Status != AgentRunning {
			hasTerminal = true
		}
	}
	if hasTerminal {
		a.reg.CleanupCompleted()
	}
	return result
}

// Wait blocks until the background agent completes, returning an exit code.
// BackgroundAgentRegistry.Wait blocks on <-state.done which is closed after the
// goroutine updates Status and Result, so the copy reflects the final state.
func (a *BackgroundAgentJobAdapter) Wait(id string) (int, error) {
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

// convertState maps a BackgroundAgentState snapshot to a job.JobInfo.
// AgentCancelled maps to "killed" to match TS TaskOutputTool's status vocabulary.
func convertState(s *BackgroundAgentState) *job.JobInfo {
	info := &job.JobInfo{
		ID:          s.ID,
		Type:        "local_agent",
		Description: s.Description,
	}
	switch s.Status {
	case AgentCompleted:
		info.Status = "completed"
		info.ExitCode = 0
	case AgentFailed:
		info.Status = "failed"
		info.ExitCode = 1
	case AgentCancelled:
		info.Status = "killed"
		info.ExitCode = -1
	case AgentRunning:
		info.Status = "running"
	}
	if s.Result != nil {
		info.Output = s.Result.Content
		info.AgentType = s.Result.AgentType
		info.Tokens = s.Result.TotalTokens
		info.DurationMs = s.Result.TotalDurationMs
	} else if s.Status == AgentFailed {
		// Failed but no Result — provide a generic error message.
		info.Output = "agent failed with no result"
	}
	return info
}

// Prefix returns the ID prefix the adapter routes directly (fork agents).
// Background subagent IDs ("agent-N") miss this prefix and fall through to
// MultiRegistry's linear scan.
func (a *BackgroundAgentJobAdapter) Prefix() string { return "fork-" }
