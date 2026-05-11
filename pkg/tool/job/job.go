// Package job provides tools for managing background jobs.
//
// Source reference: tools/TaskOutputTool/, tools/TaskStopTool/
package job

import "errors"

// ErrNotFound is returned by Registry methods when a job ID does not exist
// in any backing registry. Callers should use errors.Is(err, ErrNotFound)
// to check for this condition rather than string matching.
var ErrNotFound = errors.New("job not found")

// JobInfo is a snapshot of a background task's state.
// Source: TaskOutputTool.tsx — TaskOutput type
type JobInfo struct {
	ID          string `json:"job_id"`
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
// bash.BackgroundTaskRegistry satisfies this interface via JobInfoAdapter.
type Registry interface {
	// Get returns task info by ID. Returns (nil, false) if not found.
	Get(id string) (*JobInfo, bool)
	// Kill terminates a running task by ID.
	Kill(id string) error
	// List returns all tasks.
	List() []*JobInfo
	// Wait blocks until the task finishes, returning the exit code.
	Wait(id string) (int, error)
}

// Prefixer is an optional interface that Registries can implement to declare
// their ID prefix. MultiRegistry uses this for direct routing without
// cross-registry iteration.
type Prefixer interface {
	Prefix() string
}
