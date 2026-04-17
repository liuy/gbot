// Package task provides tools for managing background tasks.
//
// Source reference: tools/TaskOutputTool/, tools/TaskStopTool/
package task

import "errors"

// ErrNotFound is returned by Registry methods when a task ID does not exist
// in any backing registry. Callers should use errors.Is(err, ErrNotFound)
// to check for this condition rather than string matching.
var ErrNotFound = errors.New("task not found")

// TaskInfo is a snapshot of a background task's state.
// Source: TaskOutputTool.tsx — TaskOutput type
type TaskInfo struct {
	ID          string `json:"task_id"`
	Type        string `json:"task_type"`              // "local_bash"
	Status      string `json:"status"`                 // running, completed, failed, killed
	Command     string `json:"command,omitempty"`
	Description string `json:"description,omitempty"`
	Output      string `json:"output,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`

	// Agent-specific fields (populated by ForkAgentTaskAdapter).
	AgentType  string `json:"agent_type,omitempty"`
	Tokens     int    `json:"tokens,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// Registry is the interface for querying and managing background tasks.
// bash.BackgroundTaskRegistry satisfies this interface via TaskInfoAdapter.
type Registry interface {
	// Get returns task info by ID. Returns (nil, false) if not found.
	Get(id string) (*TaskInfo, bool)
	// Kill terminates a running task by ID.
	Kill(id string) error
	// List returns all tasks.
	List() []*TaskInfo
	// Wait blocks until the task finishes, returning the exit code.
	Wait(id string) (int, error)
}
