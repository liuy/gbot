//go:build windows

// Package bash — Windows placeholder.
//
// The full bash implementation depends on Unix-specific primitives (PTY,
// process groups, signals). Windows support is tracked separately.
//
// In the meantime, this stub keeps the package compiling so the rest of gbot
// builds on Windows. Bash tool calls return errNotSupported.
package bash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// Input mirrors the non-Windows Input struct so JSON callers compile.
type Input struct {
	Command         string `json:"command" validate:"required"`
	Timeout         int    `json:"timeout,omitempty"`
	CWD             string `json:"cwd,omitempty"`
	Description     string `json:"description,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

// Output mirrors the non-Windows Output struct so callers compile.
type Output struct {
	Stdout          string `json:"output"`
	Stderr          string `json:"stderr,omitempty"`
	ExitCode        int    `json:"exitCode"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	BackgroundJobID string `json:"backgroundTaskId,omitempty"`
	CWD             string `json:"cwd,omitempty"`
}

// IsSearchOrRead always returns zero value (no classification) on Windows.
func IsSearchOrRead(json.RawMessage) tool.SearchReadKind {
	return tool.SearchReadKind{}
}

// JobNotification — stub matching the Unix struct shape used by bootstrap.go.
type JobNotification struct {
	JobID       string
	Description string
	Event       string // "stall" | "complete"
	ExitCode    int
}

// FormatXML renders the notification for display.
func (n JobNotification) FormatXML() string { return "" }

// BackgroundJobRegistry is a placeholder so engine/bootstrap.go compiles.
type BackgroundJobRegistry struct {
	OnNotify func(JobNotification)
}

// NewBackgroundJobRegistry returns an empty registry on Windows.
func NewBackgroundJobRegistry() *BackgroundJobRegistry { return &BackgroundJobRegistry{} }

// JobInfoAdapter satisfies the job.Registry interface on Windows with empty data.
type JobInfoAdapter struct{}

// NewJobInfoAdapter returns a stub adapter on Windows.
func NewJobInfoAdapter(_ *BackgroundJobRegistry) *JobInfoAdapter { return &JobInfoAdapter{} }

// Get satisfies job.Registry — no jobs on Windows.
func (*JobInfoAdapter) Get(string) (*job.JobInfo, bool) { return nil, false }

// List satisfies job.Registry — no jobs on Windows.
func (*JobInfoAdapter) List() []*job.JobInfo { return nil }

// Kill satisfies job.Registry — no-op on Windows.
func (*JobInfoAdapter) Kill(string) error { return errNotSupported }

// Wait satisfies job.Registry — no-op on Windows.
func (*JobInfoAdapter) Wait(string) (int, error) { return -1, errNotSupported }

// errNotSupported is returned by all entry points on Windows.
var errNotSupported = errors.New("bash tool: not supported on Windows yet — see project roadmap")

// Execute is the tool entry point. Always returns errNotSupported on Windows.
func Execute(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	return nil, errNotSupported
}

// New returns a tool.Tool whose Call always fails on Windows.
func New(_ *BackgroundJobRegistry) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["command"],
		"properties": {
			"command": {"type": "string"}
		}
	}`)
	return tool.BuildTool(tool.ToolDef{
		Name_:              "Bash",
		Aliases_:           []string{"bash", "shell", "sh"},
		InputSchema_:       func() json.RawMessage { return schema },
		Description_:       func(json.RawMessage) (string, error) { return "Bash (disabled on Windows)", nil },
		InterruptBehavior_: tool.InterruptCancel,
		Call_: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			return nil, errNotSupported
		},
		IsReadOnly_:        func(json.RawMessage) bool { return false },
		IsDestructive_:     func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return false },
		IsEnabled_:         func() bool { return false },
		RenderResult_:      func(data any) string { return fmt.Sprintf("%v", data) },
		CheckPermissions_: func(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
			return types.PermissionAllowDecision{}
		},
	})
}
