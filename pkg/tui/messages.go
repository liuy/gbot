// Package tui implements the bubbletea TUI for gbot.
//
// Source reference: App.tsx, components/*.tsx
// messages.go defines the internal tea.Msg types used by the bubbletea Model.
package tui

import "time"

// ---------------------------------------------------------------------------
// tea.Msg types — source: React state dispatch → bubbletea messages
// ---------------------------------------------------------------------------

// streamChunkMsg delivers a chunk of streaming text from the engine.
// Source: useStreaming hook onTextDelta callback.
type streamChunkMsg struct {
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

// streamToolUseMsg signals that the LLM has started a tool invocation.
// Source: useStreaming hook onToolUseStart callback.
type streamToolUseMsg struct {
	ID      string
	Name    string
	Summary string // context-aware display name (e.g., "Listing 1 directory")
	Input   string // pretty-printed JSON
}

// streamToolDeltaMsg carries incremental input updates for a pending tool.
// The TUI uses this to update the display name once input is available.
type streamToolDeltaMsg struct {
	ID      string // tool use ID
	Delta   string // partial JSON delta
	Summary string // pre-computed summary from engine
}

// streamToolResultMsg delivers a tool execution result.
// Source: useStreaming hook onToolResult callback.
type streamToolResultMsg struct {
	ToolUseID string
	Output    string        // pretty-printed JSON
	IsError   bool
	Timing    time.Duration // elapsed time
}

// streamCompleteMsg signals that the engine has finished processing.
// Source: useStreaming hook onComplete callback.
type streamCompleteMsg struct {
	Err error // nil on success
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
