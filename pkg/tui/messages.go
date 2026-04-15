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

// streamStartMsg signals that the engine has started a new streaming response.
// Source: useStreaming hook onStreamStart callback.
type streamStartMsg struct{}

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

// toolInputMsg carries incremental input updates for a pending tool.
// The TUI uses this to update the display name once input is available.
type toolInputMsg struct {
	ID      string // tool use ID
	Delta   string // partial JSON delta
	Summary string // pre-computed summary from engine
}

// toolDeltaMsg carries streaming output lines from a tool in progress.
// Source: BashTool streaming via ExecuteStream onProgress callback.
type toolDeltaMsg struct {
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
