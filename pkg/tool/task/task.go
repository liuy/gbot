// Package task provides tools for managing background tasks.
//
// Source reference: tools/TaskOutputTool/, tools/TaskStopTool/
package task

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
