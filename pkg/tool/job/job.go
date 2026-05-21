// Package job provides tools for managing background jobs.
//
// TS divergence: the TypeScript source keeps JobOutput and JobStop as separate tools.
// gbot merges them into a single unified Job tool following the same pattern as the Task tool,
// allowing both poll and stop in a single invocation. This is a user-approved change.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

// ErrNotFound is returned by Registry methods when a job ID does not exist
// in any backing registry. Callers should use errors.Is(err, ErrNotFound)
// to check for this condition rather than string matching.
var ErrNotFound = errors.New("job not found")

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// JobInfo is a snapshot of a background job's state.
type JobInfo struct {
	ID          string `json:"job_id"`
	Type        string `json:"job_type"`               // "local_bash"
	Status      string `json:"status"`                  // running, completed, failed, killed
	Command     string `json:"command,omitempty"`
	Description string `json:"description,omitempty"`
	Output      string `json:"output,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`

	// Agent-specific fields (populated by ForkAgentTaskAdapter).
	AgentType  string `json:"agent_type,omitempty"`
	Tokens     int    `json:"tokens,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// Registry is the interface for querying and managing background jobs.
// bash.BackgroundJobRegistry satisfies this interface via JobInfoAdapter.
type Registry interface {
	// Get returns job info by ID. Returns (nil, false) if not found.
	Get(id string) (*JobInfo, bool)
	// Kill terminates a running job by ID.
	Kill(id string) error
	// List returns all jobs.
	List() []*JobInfo
	// Wait blocks until the job finishes, returning the exit code.
	Wait(id string) (int, error)
}

// Prefixer is an optional interface that Registries can implement to declare
// their ID prefix. MultiRegistry uses this for direct routing without
// cross-registry iteration.
type Prefixer interface {
	Prefix() string
}

// JobInput is the unified input schema for the Job tool.
// Poll, Stop, and List can be combined in a single call.
type JobInput struct {
	Poll    string `json:"poll,omitempty"`    // job ID to poll output from
	Stop    string `json:"stop,omitempty"`    // job ID to stop
	List    bool   `json:"list,omitempty"`    // list all jobs
	Block   *bool  `json:"block,omitempty"`   // default true (only meaningful with poll)
	Timeout int    `json:"timeout,omitempty"` // default 30000ms (only meaningful with poll)
}

// JobOutput is the unified output schema for the Job tool.
// Only the sections that were requested are populated.
type JobOutput struct {
	Poll *PollResult `json:"poll,omitempty"`
	Stop *StopResult `json:"stop,omitempty"`
	List *ListResult `json:"list,omitempty"`
}

// PollResult holds the result of a poll operation.
type PollResult struct {
	RetrievalStatus string   `json:"retrieval_status"` // success, timeout, not_ready
	Task            *JobInfo `json:"task,omitempty"`
}

// StopResult holds the result of a stop operation.
type StopResult struct {
	Status  string `json:"status"`   // "killed"
	JobID   string `json:"job_id"`
	JobType string `json:"job_type"` // "local_bash", "local_agent"
	Command string `json:"command"`  // the command/description of the killed job
}

// ListResult holds the result of a list operation.
type ListResult struct {
	Jobs []*JobInfo `json:"jobs"`
}

// ---------------------------------------------------------------------------
// Tool construction
// ---------------------------------------------------------------------------

var jobToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "poll": {
      "type": "string",
      "description": "The job ID to get output from"
    },
    "stop": {
      "type": "string",
      "description": "The job ID to stop"
    },
    "list": {
      "type": "boolean",
      "description": "List all background jobs"
    },
    "block": {
      "type": "boolean",
      "default": true,
      "description": "Whether to wait for completion (only used with poll)"
    },
    "timeout": {
      "type": "number",
      "default": 30000,
      "description": "Max wait time in ms (only used with poll)"
    }
  }
}`)

// NewJob creates the unified Job tool.
func NewJob(reg Registry) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "Job",
		Aliases_:     []string{"KillShell"},
		InputSchema_: func() json.RawMessage { return jobToolSchema },
		Description_: func(input json.RawMessage) (string, error) {
			var in JobInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Manage job", nil
			}
			parts := make([]string, 0, 3)
			if in.List {
				parts = append(parts, "List jobs")
			}
			if in.Poll != "" {
				parts = append(parts, "Poll "+in.Poll)
			}
			if in.Stop != "" {
				parts = append(parts, "Stop "+in.Stop)
			}
			if len(parts) == 0 {
				return "Manage job", nil
			}
			return strings.Join(parts, ", "), nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return executeJobCall(ctx, reg, input)
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			var in JobInput
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			return in.Stop == ""
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		ShouldDefer_:        false,
		SearchHint_:         "manage background jobs -- poll output or stop a running job",
		MaxResultSizeChars:  100000,
		Prompt_:             jobPrompt(),
		RenderResult_:       jobRenderResult,
	})
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// executeJobCall routes to poll and/or stop based on input.
// Execution order: Poll first, Stop second.
// If Poll fails, Stop is not executed.
func executeJobCall(ctx context.Context, reg Registry, input json.RawMessage) (*tool.ToolResult, error) {
	var in JobInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Poll == "" && in.Stop == "" && !in.List {
		return nil, fmt.Errorf("at least one of 'poll', 'stop', or 'list' is required")
	}

	var out JobOutput

	if in.List {
		out.List = executeList(reg)
	}

	if in.Poll != "" {
		result, err := executePoll(ctx, reg, in.Poll, in.Block, in.Timeout)
		if err != nil {
			return nil, err
		}
		out.Poll = result
	}

	if in.Stop != "" {
		result, err := executeStop(reg, in.Stop)
		if err != nil {
			return nil, err
		}
		out.Stop = result
	}

	return &tool.ToolResult{Data: &out}, nil
}

// executePoll polls a job for output.
func executePoll(ctx context.Context, reg Registry, jobID string, block *bool, timeout int) (*PollResult, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required")
	}

	doBlock := true
	if block != nil {
		doBlock = *block
	}

	if timeout <= 0 {
		timeout = 30000
	}

	info, found := reg.Get(jobID)
	if !found {
		return nil, fmt.Errorf("no job found with ID: %s", jobID)
	}

	if isTerminal(info.Status) {
		return &PollResult{RetrievalStatus: "success", Task: info}, nil
	}

	if !doBlock {
		return &PollResult{RetrievalStatus: "not_ready", Task: info}, nil
	}

	// block=true: poll until terminal or timeout
	deadline := timeAfter(timeout)
	ticker := timeNewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			info, _ = reg.Get(jobID)
			return &PollResult{RetrievalStatus: "timeout", Task: info}, nil
		case <-ticker.C:
			info, _ = reg.Get(jobID)
			if info != nil && isTerminal(info.Status) {
				return &PollResult{RetrievalStatus: "success", Task: info}, nil
			}
		}
	}
}

// executeStop stops a running job.
func executeStop(reg Registry, jobID string) (*StopResult, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required")
	}

	info, found := reg.Get(jobID)
	if !found {
		return nil, fmt.Errorf("no job found with ID: %s", jobID)
	}

	if isTerminal(info.Status) {
		return nil, fmt.Errorf("job %s is not running (status: %s)", jobID, info.Status)
	}

	if err := reg.Kill(jobID); err != nil {
		return nil, fmt.Errorf("failed to stop job %s: %w", jobID, err)
	}

	cmd := info.Command
	if cmd == "" {
		cmd = info.Description
	}

	return &StopResult{
		Status:  "killed",
		JobID:   jobID,
		JobType: info.Type,
		Command: cmd,
	}, nil
}

// executeList returns all registered jobs.
func executeList(reg Registry) *ListResult {
	return &ListResult{Jobs: reg.List()}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isTerminal returns true for terminal job statuses.
func isTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "killed"
}

// timeAfter and timeNewTicker are vars for testability.
var (
	timeAfter     = func(ms int) <-chan time.Time { return time.After(time.Duration(ms) * time.Millisecond) }
	timeNewTicker = time.NewTicker
)

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// jobRenderResult renders the unified JobOutput for TUI display.
func jobRenderResult(data any) string {
	out, ok := data.(*JobOutput)
	if !ok {
		return fmt.Sprintf("%v", data)
	}

	var sections []string

	if out.List != nil {
		sections = append(sections, renderListResult(out.List))
	}
	if out.Poll != nil {
		sections = append(sections, renderPollResult(out.Poll))
	}
	if out.Stop != nil {
		sections = append(sections, renderStopResult(out.Stop))
	}

	return strings.Join(sections, "\n\n")
}

func renderPollResult(p *PollResult) string {
	if p.Task == nil {
		return fmt.Sprintf("Poll: %s", p.RetrievalStatus)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Poll %s: %s", p.Task.ID, p.RetrievalStatus)
	if isTerminal(p.Task.Status) {
		fmt.Fprintf(&sb, " (exit: %d)", p.Task.ExitCode)
	}
	if p.Task.Output != "" {
		sb.WriteByte('\n')
		sb.WriteString(p.Task.Output)
	}
	return sb.String()
}

func renderStopResult(s *StopResult) string {
	cmd := s.Command
	if cmd == "" {
		cmd = s.JobID
	}
	return fmt.Sprintf("Stop %s: killed (was: %s)", s.JobID, cmd)
}

func renderListResult(l *ListResult) string {
	if len(l.Jobs) == 0 {
		return "No jobs"
	}
	var sb strings.Builder
	for i, j := range l.Jobs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		cmd := j.Command
		if cmd == "" {
			cmd = j.Description
		}
		if cmd == "" {
			cmd = j.ID
		}
		fmt.Fprintf(&sb, "%s [%s] %s", j.ID, j.Status, cmd)
	}
	return sb.String()
}
