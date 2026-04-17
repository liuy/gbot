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
	ContentTypeRedacted     ContentType = "redacted_thinking"
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

	// Redacted thinking data (type == "redacted_thinking")
	// Must be preserved verbatim and replayed to the API.
	Data string `json:"data,omitempty"`
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
	// Query lifecycle
	EventQueryStart    QueryEventType = "query_start"
	EventQueryEnd      QueryEventType = "query_end"

	// Per-round LLM turn
	EventTurnStart     QueryEventType = "turn_start"
	EventTurnEnd       QueryEventType = "turn_end"

	// Thinking
	EventThinkingStart QueryEventType = "thinking_start"
	EventThinkingEnd   QueryEventType = "thinking_end"
	EventThinkingDelta QueryEventType = "thinking_delta"

	// Tool call lifecycle: start → param_delta → output_delta → end
	EventToolStart     QueryEventType = "tool_start"
	EventToolParamDelta     QueryEventType = "tool_param_delta"
	EventToolOutputDelta     QueryEventType = "tool_output_delta"
	EventToolEnd       QueryEventType = "tool_end"

	// Text and usage
	EventTextDelta     QueryEventType = "text_delta"
	EventUsage         QueryEventType = "usage"
	EventError                QueryEventType = "error"
	EventNotificationPending  QueryEventType = "notification_pending"
)

// AgentMeta tags events originating from a sub-agent.
// When non-nil on a QueryEvent, the event comes from a child engine
// spawned by the Agent tool, not from the main engine.
type AgentMeta struct {
	ParentToolUseID string // parent Agent tool call ID (e.g. "call_abc123")
	AgentType       string // "general-purpose", "Explore", "Plan"
	Depth           int    // nesting depth: 0 = direct child, 1 = grandchild
}

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
	Agent        *AgentMeta         // non-nil = sub-agent event
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
	Text     string        `json:"text,omitempty"`     // thinking text delta (set on ThinkingDelta)
}

// PartialInputEvent carries incremental input for a pending tool call.
type PartialInputEvent struct {
	ID      string `json:"id"`      // tool use ID
	Name    string `json:"name"`    // tool name (e.g. "Read", "Bash")
	Delta   string `json:"delta"`   // partial JSON string
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

// ---------------------------------------------------------------------------
// Agent types — source: tools/AgentTool/builtInAgents.ts, AgentTool.tsx
// ---------------------------------------------------------------------------

// AgentDefinition describes a built-in agent type.
// Source: tools/AgentTool/builtInAgents.ts — BaseAgentDefinition
type AgentDefinition struct {
	AgentType       string
	WhenToUse       string
	SystemPrompt    func() string // lazily generated system prompt
	Tools           []string      // nil or ["*"] = all tools
	DisallowedTools []string      // blacklist
	Model           string        // "inherit", "haiku", "sonnet", "opus"
	OmitClaudeMd    bool
	MaxTurns        int // 0 = no limit
}

// AgentInput is the input parameters for the Agent tool.
// Source: AgentTool.tsx:82-138 — AgentToolInput
type AgentInput struct {
	Description      string `json:"description"`
	Prompt           string `json:"prompt"`
	SubagentType     string `json:"subagent_type,omitempty"`
	Name             string `json:"name,omitempty"`
	Model            string `json:"model,omitempty"`
	RunInBackground  bool   `json:"run_in_background,omitempty"`
}

// SubQueryResult is the result returned by a sub-agent after execution.
// Source: agentToolUtils.ts:348-357 — AgentToolResult
type SubQueryResult struct {
	AgentID           string
	AgentType         string
	Content           string
	TotalDurationMs   int64
	TotalTokens       int
	TotalToolUseCount int
	AsyncLaunched     bool // true = fork agent launched in background
}

// ForkBoilerplateTag is the marker string used in fork agent directive messages
// to detect recursive forking. Source: forkSubagent.ts — FORK_BOILERPLATE_TAG
const ForkBoilerplateTag = "fork-boilerplate"
