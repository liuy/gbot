package agent

import (
	"fmt"

	"github.com/liuy/gbot/pkg/tool/task"
)

// Compile-time interface check.
var _ task.Registry = (*ForkAgentTaskAdapter)(nil)

// ForkAgentTaskAdapter adapts ForkAgentRegistry to the task.Registry interface.
// This enables TaskOutput/TaskStop to manage fork agent tasks alongside bash tasks.
type ForkAgentTaskAdapter struct {
	reg *ForkAgentRegistry
}

// NewForkAgentTaskAdapter creates an adapter wrapping a ForkAgentRegistry.
// If reg is nil, all methods return not-found.
func NewForkAgentTaskAdapter(reg *ForkAgentRegistry) *ForkAgentTaskAdapter {
	return &ForkAgentTaskAdapter{reg: reg}
}

// Get returns task info by ID. Triggers lazy cleanup for terminal-state agents.
func (a *ForkAgentTaskAdapter) Get(id string) (*task.TaskInfo, bool) {
	if a.reg == nil {
		return nil, false
	}
	state, ok := a.reg.Get(id)
	if !ok {
		return nil, false
	}
	info := convertState(state)
	// Lazy cleanup: after reading a terminal agent, purge all completed agents.
	if state.Status != ForkRunning {
		a.reg.CleanupCompleted()
	}
	return info, true
}

// Kill cancels a running fork agent by ID.
func (a *ForkAgentTaskAdapter) Kill(id string) error {
	if a.reg == nil {
		return fmt.Errorf("kill %q: %w", id, task.ErrNotFound)
	}
	if !a.reg.Cancel(id) {
		return fmt.Errorf("kill %q: %w", id, task.ErrNotFound)
	}
	return nil
}

// List returns all fork agent tasks as TaskInfo snapshots.
// Triggers lazy cleanup for any terminal-state agents.
func (a *ForkAgentTaskAdapter) List() []*task.TaskInfo {
	if a.reg == nil {
		return nil
	}
	states := a.reg.List()
	result := make([]*task.TaskInfo, len(states))
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
func (a *ForkAgentTaskAdapter) Wait(id string) (int, error) {
	if a.reg == nil {
		return -1, fmt.Errorf("wait %q: %w", id, task.ErrNotFound)
	}
	state, ok := a.reg.Wait(id)
	if !ok {
		return -1, fmt.Errorf("wait %q: %w", id, task.ErrNotFound)
	}
	info := convertState(state)
	return info.ExitCode, nil
}

// convertState maps a ForkAgentState snapshot to a task.TaskInfo.
// ForkCancelled maps to "killed" to match TS TaskOutputTool's status vocabulary.
func convertState(s *ForkAgentState) *task.TaskInfo {
	info := &task.TaskInfo{
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
