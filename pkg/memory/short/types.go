// Package short implements short-term session memory storage using SQLite.
// It ports the TS Claude Code session storage system (sessionStorage.ts, ~5900 lines)
// to Go with SQLite replacing JSONL files.
package short

import (
	"encoding/json"
	"time"
)

// Message represents a single message in a session transcript.
// Aligned with TS TranscriptMessage (types/logs.ts:221-231).
type Message struct {
	Seq       int64
	SessionID string
	UUID      string
	// ParentUUID is the chain link. Empty string for chain root or compact boundary.
	ParentUUID string
	// LogicalParentUUID preserves the logical predecessor when ParentUUID is reset
	// (e.g., at compact boundary). TS: messages.ts:4552-4553.
	LogicalParentUUID string
	// IsSidechain is 0 for main conversation, 1 for agent sub-chains. TS: logs.ts:224.
	IsSidechain int
	Type        string // user / assistant / system / attachment / progress
	Subtype     string // compact_boundary / informational / tool_result etc.
	Content     string // Complete JSON message body
	CreatedAt   time.Time
}

// Session represents a conversation session.
type Session struct {
	SessionID       string
	ProjectDir      string
	Model           string
	Title           string
	ParentSessionID string
	ForkPointSeq    int
	AgentType       string            // Current agent type (TS: separate metadata message)
	Mode            string            // Current mode (TS: separate metadata message)
	Settings        map[string]string // JSON: agent/mode settings (TS: agent-setting messages)
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ContentBlock represents a parsed block from a message's content JSON.
type ContentBlock struct {
	Type       string          `json:"type"`             // "text" | "tool_use" | "tool_result" | "thinking" | "redacted_thinking"
	Text       string          `json:"text,omitempty"`
	ID         string          `json:"id,omitempty"`             // tool_use block id
	Name       string          `json:"name,omitempty"`           // tool name for tool_use
	Input      json.RawMessage `json:"input,omitempty"`          // tool_use input parameters
	ToolUseID  string          `json:"tool_use_id,omitempty"`    // tool_result reference to tool_use.id
	IsError    bool            `json:"is_error,omitempty"`       // tool_result error flag
	Content    json.RawMessage `json:"content,omitempty"`        // tool_result content (string or base64 for images)
	Data       string          `json:"data,omitempty"`           // redacted_thinking data (must be preserved verbatim)
}

// CompactResult holds the output of a compact operation.
// Aligned with TS CompactionResult.
type CompactResult struct {
	BoundaryMarker   *Message   // system, subtype=compact_boundary
	SummaryMessages  []*Message // user, isCompactSummary=true
	MessagesToKeep   []*Message // partial compact preserved messages
	Attachments      []*Message // reinjected attachments
	PreCompactTokens  int
	PostCompactTokens int
}

// SearchOptions configures a full-text search query.
type SearchOptions struct {
	SessionID  string
	ProjectDir string
	Types      []string // Filter by message type
	Limit      int
	Offset     int
}

// SearchResult holds a matched message with its relevance score.
type SearchResult struct {
	Message *Message
	Score   float64
}

// PreBoundaryMetadata stores session state metadata before a compact boundary.
// TS: sessionStorage.ts:3157 scanPreBoundaryMetadata() (file byte scan).
// Go: queries sessions table columns directly (agent_type/mode/settings).
type PreBoundaryMetadata struct {
	AgentType string            // sessions.agent_type
	Mode      string            // sessions.mode
	Settings  map[string]string // JSON.parse(sessions.settings)
}

// SkillState holds restored skill invocation state.
type SkillState struct {
	InvokedSkills map[string]bool // skill_id → active
	CronTasks     []CronTask
}

// CronTask represents a restored cron task.
type CronTask struct {
	SkillName string
	CronExpr  string
	Durable   bool
}

// AgentState holds restored agent configuration.
type AgentState struct {
	AgentType  string            // "general-purpose", "Explore", "Plan", etc.
	Model      string            // "sonnet", "opus", "haiku"
	Setting    map[string]string // agent-specific settings
	ToolUseIDs map[string]string // tool_use_id → agent_id mapping
}

// TodoItem represents a restored TODO entry.
type TodoItem struct {
	ID          string
	Subject     string
	Status      string // "pending" | "in_progress" | "completed" | "deleted"
	Description string
}

// AttributionState holds attribution info for sub-agent sessions.
type AttributionState struct {
	IsSubAgent    bool
	ParentAgentID string
	ToolUseID     string
}

// AgentContext holds standalone agent context for session resume.
type AgentContext struct {
	AgentType    string
	SessionID    string
	Model        string
	SystemPrompt string
}

// ResumedState holds the aggregate result of ProcessResumedConversation.
type ResumedState struct {
	AgentState  *AgentState
	SkillState  *SkillState
	Todos       []*TodoItem
	Attribution *AttributionState
}
