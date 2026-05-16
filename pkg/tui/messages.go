// Package tui implements the bubbletea TUI for gbot.
//
// Source reference: App.tsx, components/*.tsx
// messages.go defines the internal tea.Msg types used by the bubbletea Model.
package tui

import (
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// tea.Msg types — source: React state dispatch → bubbletea messages
// ---------------------------------------------------------------------------

// textDeltaMsg delivers a chunk of streaming text from the engine.
// Source: useStreaming hook onTextDelta callback.
type textDeltaMsg struct {
	Text  string
	Agent *types.AgentMeta // non-nil when from a sub-agent
}

// turnStartMsg signals that the engine has started a new agentic turn.
// Source: useStreaming hook onStreamStart callback.
type turnStartMsg struct{}

// streamMessageMsg delivers a full message added to conversation history.
// Source: useStreaming hook onMessage callback.
type streamMessageMsg struct {
	Role string
}

// toolStartMsg signals that the LLM has started a tool invocation.
// Source: useStreaming hook onToolUseStart callback.
type toolStartMsg struct {
	ID      string
	Name    string
	Summary string // context-aware display name (e.g., "Listing 1 directory")
	Input   string // pretty-printed JSON
	Agent   *types.AgentMeta // non-nil when from a sub-agent
}

// toolParamDeltaMsg carries incremental input updates for a pending tool.
// The TUI uses this to update the display name once input is available.
type toolParamDeltaMsg struct {
	ID      string // tool use ID
	Delta   string // partial JSON delta
	Summary string // pre-computed summary from engine
	Agent   *types.AgentMeta // non-nil when from a sub-agent
}

// toolOutputDeltaMsg carries streaming output lines from a tool in progress.
// Source: BashTool streaming via OnProgress callback in ToolUseContext.
type toolOutputDeltaMsg struct {
	ToolUseID     string           // tool use ID
	DisplayOutput string           // accumulated output lines
	Timing        time.Duration    // elapsed time since tool start
	Agent         *types.AgentMeta // non-nil when from a sub-agent
}

// toolEndMsg delivers a tool execution result.
// Source: useStreaming hook onToolResult callback.
type toolEndMsg struct {
	ToolUseID    string
	Output       string        // pretty-printed JSON
	IsError      bool
	Timing       time.Duration // elapsed time
	Agent        *types.AgentMeta // non-nil when from a sub-agent
	IsBackground bool            // true = fork agent launched async, keep in running state
}

// queryEndMsg signals that the engine has finished processing.
// Source: useStreaming hook onComplete callback.
type queryEndMsg struct {
	Err        error                 // nil on success
	TotalUsage types.Usage           // engine's accumulated usage across all turns
	Agent      *types.AgentMeta      // non-nil when from a sub-agent
}

// usageMsg carries token usage from the LLM provider during streaming.
type usageMsg struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	Agent                    *types.AgentMeta // non-nil when from a sub-agent
}

// thinkingStartMsg signals that the model has started extended thinking.
type thinkingStartMsg struct {
	Agent *types.AgentMeta // non-nil when from a sub-agent
}

// thinkingEndMsg signals that the model has finished extended thinking.
type thinkingEndMsg struct {
	Duration time.Duration
	Agent    *types.AgentMeta // non-nil when from a sub-agent
}

// thinkingDeltaMsg carries a chunk of thinking text from the engine.
type thinkingDeltaMsg struct {
	Text  string
	Agent *types.AgentMeta // non-nil when from a sub-agent
}

// attachmentMsg signals that a background attachment was drained and injected.
// Carries notification info for TUI rendering. Empty fields = first dispatch
// from EnqueueAttachment (no content yet, TUI ignores).
type attachmentMsg struct {
	JobID   string // background job id
	Preview string // summary text
	Failed  bool   // true for "failed" or "killed"
}

// idleAbortedMsg is returned when an idle readEvents is cancelled
// because the user started a new query. No-op for Update.
type idleAbortedMsg struct{}

// submitMsg is sent when the user presses Enter to submit input.
type submitMsg struct {
	Text string
}

// errMsg wraps an error for display in the TUI.
type errMsg struct {
	Err error
}

// spinnerTickMsg is an internal message to animate the spinner.
// infoMsg displays a transient info message in the status bar.
type infoMsg string

type spinnerTickMsg struct{}

// textStartMsg signals that a text content block has started streaming.
type textStartMsg struct {
	Agent *types.AgentMeta // non-nil when from a sub-agent
}

// textEndMsg signals that a text content block has finished streaming.
type textEndMsg struct {
	Agent *types.AgentMeta // non-nil when from a sub-agent
}

// toolRunMsg signals that a tool's input is complete and execution is starting.
type toolRunMsg struct {
	ID    string
	Name  string
	Agent *types.AgentMeta // non-nil when from a sub-agent
}

// permissionAskMsg carries a permission confirmation request from the engine.
// The TUI creates a PermissionDialog overlay; user response is written to
// the event's ResponseCh, unblocking the waiting engine goroutine.
type permissionAskMsg struct {
	event *types.AskEvent
}

// inputAskMsg carries an interactive input request from the engine.
// The TUI creates an InputDialog overlay; user response is written to
// the event's ResponseCh, unblocking the waiting Drain goroutine.
type inputAskMsg struct {
	event *types.AskEvent
}

// retryAttemptMsg carries retry information when the engine retries a failed stream.
// Source: TS withRetry.ts — SystemAPIErrorMessage countdown display.
type retryAttemptMsg struct {
	Attempt    int
	MaxRetries int
	RetryInMs  int64
	ErrorType  string
	Error      string
}
