// Package tui implements the bubbletea TUI for gbot.
//
// Source reference: App.tsx, components/*.tsx
// messages.go defines the internal tea.Msg types used by the bubbletea Model.
package tui

import "time"

// ---------------------------------------------------------------------------
// tea.Msg types — source: React state dispatch → bubbletea messages
// ---------------------------------------------------------------------------

// textDeltaMsg delivers a chunk of streaming text from the engine.
// Source: useStreaming hook onTextDelta callback.
type textDeltaMsg struct {
	Text string
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
}

// toolParamDeltaMsg carries incremental input updates for a pending tool.
// The TUI uses this to update the display name once input is available.
type toolParamDeltaMsg struct {
	ID      string // tool use ID
	Delta   string // partial JSON delta
	Summary string // pre-computed summary from engine
}

// toolOutputDeltaMsg carries streaming output lines from a tool in progress.
// Source: BashTool streaming via ExecuteStream onProgress callback.
type toolOutputDeltaMsg struct {
	ToolUseID     string        // tool use ID
	DisplayOutput string        // accumulated output lines
	Timing        time.Duration // elapsed time since tool start
}

// toolEndMsg delivers a tool execution result.
// Source: useStreaming hook onToolResult callback.
type toolEndMsg struct {
	ToolUseID string
	Output    string        // pretty-printed JSON
	IsError   bool
	Timing    time.Duration // elapsed time
}

// queryEndMsg signals that the engine has finished processing.
// Source: useStreaming hook onComplete callback.
type queryEndMsg struct {
	Err error // nil on success
}

// usageMsg carries token usage from the LLM provider during streaming.
type usageMsg struct {
	InputTokens  int
	OutputTokens int
}

// thinkingStartMsg signals that the model has started extended thinking.
type thinkingStartMsg struct{}

// thinkingEndMsg signals that the model has finished extended thinking.
type thinkingEndMsg struct {
	Duration time.Duration
}

// thinkingDeltaMsg carries a chunk of thinking text from the engine.
type thinkingDeltaMsg struct {
	Text string
}

// agentToolMsg carries a sub-agent tool event for the grouped_tool_use display.
// When the engine emits events tagged with AgentMeta (from a sub-engine),
// the TUI handler converts them to this message type instead of the regular
// toolStartMsg/toolEndMsg so the parent Agent tool call can show live progress.
type agentToolMsg struct {
	ParentToolUseID string // parent Agent tool call ID
	AgentType       string // "Explore", "general-purpose", "Plan"
	Depth           int    // nesting depth (0 = direct child)
	SubType         string // "tool_start" or "tool_end"
	ToolName        string // sub-agent's tool name (e.g. "Read", "Grep")
	Summary         string // tool summary
	IsError         bool   // true on tool_end with error
}

// agentUsageMsg carries sub-agent token usage for both global and per-agent stats.
type agentUsageMsg struct {
	ParentToolUseID string
	InputTokens     int
	OutputTokens    int
}

// notificationPendingMsg signals that a background notification is available
// in the engine's queue. TUI auto-triggers ProcessNotifications (Path B).
type notificationPendingMsg struct{}

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
type spinnerTickMsg struct{}
