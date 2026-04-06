// Package tui implements the bubbletea TUI for gbot.
//
// Source reference: App.tsx, components/*.tsx
// messages.go defines the internal tea.Msg types used by the bubbletea Model.
package tui

// ---------------------------------------------------------------------------
// tea.Msg types — source: React state dispatch → bubbletea messages
// ---------------------------------------------------------------------------

// streamChunkMsg delivers a chunk of streaming text from the engine.
// Source: useStreaming hook onTextDelta callback.
type streamChunkMsg struct {
	Text string
}

// streamToolUseMsg signals that the LLM has started a tool invocation.
// Source: useStreaming hook onToolUseStart callback.
type streamToolUseMsg struct {
	ID    string
	Name  string
	Input string // pretty-printed JSON
}

// streamToolResultMsg delivers a tool execution result.
// Source: useStreaming hook onToolResult callback.
type streamToolResultMsg struct {
	ToolUseID string
	Output    string // pretty-printed JSON
	IsError   bool
	Timing    string // human-readable duration
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
