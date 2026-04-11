// Package types defines shared types for gbot.
//
// Source reference: /home/yliu/claude-code-source-code/src/
// These types are ported 1:1 from the TypeScript source to ensure
// behavioral parity with the original Claude Code implementation.
package types

import (
	"context"
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Message types — source: types/logs.ts, Tool.ts
// ---------------------------------------------------------------------------

// Role represents the role of a message sender.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// Message represents a single message in the conversation.
// Source: Anthropic API Message type + internal message extensions.
type Message struct {
	ID         string          `json:"id,omitempty"`
	Role       Role            `json:"role"`
	Content    []ContentBlock  `json:"content"`
	Model      string          `json:"model,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	Timestamp  time.Time       `json:"timestamp,omitempty"`
}

// Usage tracks token consumption.
// Source: Anthropic API Usage type.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// ---------------------------------------------------------------------------
// ContentBlock types — source: Anthropic API ContentBlock variants
// ---------------------------------------------------------------------------

// ContentType identifies the type of a content block.
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
	ContentTypeThinking   ContentType = "thinking"
)

// ContentBlock is a discriminated union for message content.
// Source: Anthropic API content block types.
type ContentBlock struct {
	Type ContentType `json:"type"`

	// Text content (type == "text" or "thinking")
	Text string `json:"text,omitempty"`

	// Tool use fields (type == "tool_use")
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// Tool result fields (type == "tool_result")
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// NewTextBlock creates a text content block.
func NewTextBlock(text string) ContentBlock {
	return ContentBlock{Type: ContentTypeText, Text: text}
}

// NewToolUseBlock creates a tool_use content block.
func NewToolUseBlock(id, name string, input json.RawMessage) ContentBlock {
	return ContentBlock{Type: ContentTypeToolUse, ID: id, Name: name, Input: input}
}

// NewToolResultBlock creates a tool_result content block.
func NewToolResultBlock(toolUseID string, content json.RawMessage, isError bool) ContentBlock {
	return ContentBlock{
		Type:      ContentTypeToolResult,
		ToolUseID: toolUseID,
		Content:   content,
		IsError:   isError,
	}
}

// ---------------------------------------------------------------------------
// Permission types — source: types/permissions.ts
// ---------------------------------------------------------------------------

// PermissionMode represents the current permission mode.
// Source: types/permissions.ts:16-22
type PermissionMode string

const (
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"
	PermissionModeBypass      PermissionMode = "bypassPermissions"
	PermissionModeDefault     PermissionMode = "default"
	PermissionModeDontAsk     PermissionMode = "dontAsk"
	PermissionModePlan        PermissionMode = "plan"
	PermissionModeAuto        PermissionMode = "auto"
)

// PermissionBehavior represents the outcome of a permission check.
type PermissionBehavior string

const (
	BehaviorAllow       PermissionBehavior = "allow"
	BehaviorDeny        PermissionBehavior = "deny"
	BehaviorAsk         PermissionBehavior = "ask"
	BehaviorPassthrough PermissionBehavior = "passthrough"
)

// PermissionResult is the sum type for permission decisions.
// Source: types/permissions.ts:251-266
type PermissionResult interface {
	permissionResultMarker()
	Behavior() PermissionBehavior
}

// PermissionAllowDecision — source: types/permissions.ts:174-184
type PermissionAllowDecision struct{}

func (PermissionAllowDecision) permissionResultMarker() {}
func (PermissionAllowDecision) Behavior() PermissionBehavior { return BehaviorAllow }

// PermissionAskDecision — source: types/permissions.ts:199-226
type PermissionAskDecision struct {
	Message string `json:"message"`
}

func (PermissionAskDecision) permissionResultMarker() {}
func (PermissionAskDecision) Behavior() PermissionBehavior { return BehaviorAsk }

// PermissionDenyDecision — source: types/permissions.ts:231-236
type PermissionDenyDecision struct {
	Message string `json:"message"`
}

func (PermissionDenyDecision) permissionResultMarker() {}
func (PermissionDenyDecision) Behavior() PermissionBehavior { return BehaviorDeny }

// ---------------------------------------------------------------------------
// ToolUseContext — source: Tool.ts:158-300
// ---------------------------------------------------------------------------

// FileState records what a file looked like when last read.
// Used for deduplication (read same range again → file_unchanged stub)
// and staleness detection (file changed on disk since read).
// Source: FileReadTool.ts — readFileState map entry.
type FileState struct {
	Content       string // file content at read time
	Timestamp     int64  // file mtime in milliseconds at read time
	Offset        int    // offset used in this read (0 = no offset)
	Limit         int    // limit used in this read (0 = no limit)
	IsPartialView bool   // true if read was with offset/limit (partial)
}

// ToolUseContext carries the execution context for each tool call.
type ToolUseContext struct {
	Ctx           context.Context
	Options       ToolUseOptions
	Messages      []Message
	ToolUseID     string
	WorkingDir    string
	ReadFileState map[string]FileState // keyed by absolute file path
}

// ToolUseOptions holds the execution options.
// Source: Tool.ts:159-179
type ToolUseOptions struct {
	Debug         bool
	MainLoopModel string
	Verbose       bool
}

// ---------------------------------------------------------------------------
// Engine types — source: query.ts, QueryEngine.ts
// ---------------------------------------------------------------------------

// QueryEventType identifies the type of query event.
type QueryEventType string

const (
	EventStreamStart   QueryEventType = "stream_start"
	EventTextDelta     QueryEventType = "text_delta"
	EventToolUseStart  QueryEventType = "tool_use_start"
	EventToolUseDelta  QueryEventType = "tool_use_delta"
	EventToolResult    QueryEventType = "tool_result"
	EventMessage       QueryEventType = "message"
	EventUsage         QueryEventType = "usage"
	EventThinkingStart QueryEventType = "thinking_start"
	EventThinkingEnd   QueryEventType = "thinking_end"
	EventError         QueryEventType = "error"
	EventComplete      QueryEventType = "complete"
)

// QueryEvent represents an event emitted during the query loop.
// In TS this is yielded by an async generator; in Go sent on a channel.
type QueryEvent struct {
	Type         QueryEventType     `json:"type"`
	Text         string             `json:"text,omitempty"`
	ToolUse      *ToolUseEvent      `json:"tool_use,omitempty"`
	ToolResult   *ToolResultEvent   `json:"tool_result,omitempty"`
	Message      *Message           `json:"message,omitempty"`
	PartialInput *PartialInputEvent `json:"partial_input,omitempty"`
	Usage        *UsageEvent        `json:"usage_event,omitempty"`
	Thinking     *ThinkingEvent     `json:"thinking,omitempty"`
	Error        error              `json:"-"`
}

// UsageEvent carries token usage from the LLM provider during streaming.
type UsageEvent struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ThinkingEvent carries thinking state information.
type ThinkingEvent struct {
	Duration time.Duration `json:"duration,omitempty"` // time spent thinking (set on ThinkingEnd)
}

// PartialInputEvent carries incremental input for a pending tool call.
type PartialInputEvent struct {
	ID      string `json:"id"`              // tool use ID
	Delta   string `json:"delta"`           // partial JSON string
	Summary string `json:"summary,omitempty"` // pre-computed summary from engine
}

// ToolUseEvent represents a tool invocation event.
type ToolUseEvent struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Summary string          `json:"summary,omitempty"` // pre-computed summary from engine
}

// ToolResultEvent represents a tool execution result event.
type ToolResultEvent struct {
	ToolUseID     string          `json:"tool_use_id"`
	Output        json.RawMessage `json:"output"`
	DisplayOutput string          `json:"display_output,omitempty"` // human-readable result for TUI
	IsError       bool            `json:"is_error,omitempty"`
	Timing        time.Duration   `json:"timing,omitempty"`
}

// TerminalReason indicates why the query loop exited.
// Source: query.ts transition decision tree
type TerminalReason string

const (
	TerminalCompleted        TerminalReason = "completed"
	TerminalAbortedStreaming TerminalReason = "aborted_streaming"
	TerminalAbortedTools     TerminalReason = "aborted_tools"
	TerminalModelError       TerminalReason = "model_error"
	TerminalBlockingLimit    TerminalReason = "blocking_limit"
	TerminalPromptTooLong    TerminalReason = "prompt_too_long"
)

// ContinueReason indicates why the loop continues to another iteration.
type ContinueReason string

const (
	ContinueNextTurn       ContinueReason = "next_turn"
	ContinueMaxTokensRetry ContinueReason = "max_tokens_retry"
)

// LoopAction is the result of the transition decision tree.
type LoopAction struct {
	Continue bool
	Reason   ContinueReason
	Terminal TerminalReason
}
