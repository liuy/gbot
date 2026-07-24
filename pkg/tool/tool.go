// Package tool defines the tool system for gbot.
//
// Source reference: Tool.ts:362-695
// The Tool interface captures behavioral methods from the TS source.
// React JSX render methods are excluded — replaced by TUI ToolRenderer.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// FileState — source: FileReadTool.ts — readFileState map entry
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

// ---------------------------------------------------------------------------
// ToolUseContext — source: Tool.ts:158-300
// ---------------------------------------------------------------------------

// ToolUseContext carries the execution context for each tool call.
type ToolUseContext struct {
	Ctx              context.Context
	Options          ToolUseOptions
	Messages         []types.Message
	ToolUseID        string
	WorkingDir       string
	AssistantContent []types.ContentBlock                                                        // current assistant message's content blocks (mid-stream)
	ReadFileState    map[string]FileState                                                        // keyed by absolute file path
	OnProgress       func(ProgressUpdate)                                                        // optional — engine sets this for streaming progress
	UncappedOutput   bool                                                                        // bypass internal output capping (set for REPL sub-tool calls)
	OnAskInput       func(prompt string, masked bool, deadline time.Time) chan types.AskResponse // optional — interactive PTY input
}

// MaxUncappedOutput is the safety limit for uncapped tool output reads (64MB).
// Used when UncappedOutput is true — prevents OOM while allowing full intermediate results.
const MaxUncappedOutput = 64 * 1024 * 1024

// ToolUseOptions holds the execution options.
// Source: Tool.ts:159-179
type ToolUseOptions struct {
	Debug             bool
	MainLoopModel     string
	Verbose           bool
	Tools             map[string]Tool // all tools map, aligned with TS options.tools
	PendingMCPServers []string        // MCP pending server names
	SessionID         string          // engine session ID for tool isolation (e.g., REPLTool sessions)
}

// ---------------------------------------------------------------------------
// InterruptBehavior — source: Tool.ts:416
// ---------------------------------------------------------------------------

// InterruptBehavior determines how a tool responds to interrupts.
type InterruptBehavior int

const (
	InterruptCancel InterruptBehavior = iota // 'cancel' — stop tool, discard result
	InterruptBlock                           // 'block' — keep running, new message waits
)

// ---------------------------------------------------------------------------
// SearchReadKind — source: Tool.ts:429-433
// ---------------------------------------------------------------------------

// SearchReadKind classifies a tool call as search/read/list.
// Source: Tool.ts:429-433 — isSearchOrReadCommand return type.
type SearchReadKind struct {
	IsSearch bool
	IsRead   bool
	IsList   bool
	IsLsp    bool
}

// IsCollapsible returns true if the tool call should be collapsed in the TUI.
func (s SearchReadKind) IsCollapsible() bool {
	return s.IsSearch || s.IsRead || s.IsList || s.IsLsp
}

// ---------------------------------------------------------------------------
// Tool interface — source: Tool.ts:362-695
// ---------------------------------------------------------------------------

// Tool is the complete tool interface used by the engine.
// Source: Tool.ts:362-695
//
// Deliberately excluded (React JSX → TUI ToolRenderer):
//   - renderToolUseProgressMessage (Tool.ts:625)
//   - renderToolUseRejectedMessage (Tool.ts:641)
//   - renderToolUseErrorMessage (Tool.ts:659)
//   - renderGroupedToolUse (Tool.ts:678)
//
// Ported to plain string:
//   - renderToolResultMessage (Tool.ts:566) → RenderResult
//   - renderToolUseMessage (Tool.ts:605) → Description
type Tool interface {
	// ── Identity ──────────────────────────────────────────
	Name() string
	Aliases() []string

	// ── Description ───────────────────────────────────────
	Description(input json.RawMessage) (string, error)
	// ── Result Rendering ──────────────────────────────────
	// Source: Tool.ts:566 — renderToolResultMessage
	// Renders tool result data as a human-readable string for TUI display.
	RenderResult(data any) string

	// ── Schema ────────────────────────────────────────────
	InputSchema() json.RawMessage

	// ── Execution ─────────────────────────────────────────
	// Source: Tool.ts:379-385 — call(args, context, canUseTool, parentMessage, onProgress?)
	Call(ctx context.Context, input json.RawMessage, tctx *ToolUseContext) (*ToolResult, error)

	// ── Permission ────────────────────────────────────────
	// Source: Tool.ts:500-503
	CheckPermissions(input json.RawMessage, tctx *ToolUseContext) types.PermissionResult

	// ── Behavioral Properties ─────────────────────────────
	// Source: Tool.ts:404-434
	IsReadOnly(input json.RawMessage) bool
	IsDestructive(input json.RawMessage) bool
	IsConcurrencySafe(input json.RawMessage) bool
	IsEnabled() bool

	// Interrupt behavior: source values are 'cancel' | 'block' (Tool.ts:416)
	InterruptBehavior() InterruptBehavior

	// Max result size in characters before truncation.
	// Source: Tool.ts:707 — maxResultSizeChars
	MaxResultSize() int

	// ── Prompt (system prompt text contributed by this tool) ──
	Prompt() string
}

// ---------------------------------------------------------------------------
// ToolResult — source: Tool.ts:321-336
// ---------------------------------------------------------------------------

// ToolResult is the output of a tool execution.
// Source: Tool.ts:321-336 — generic typed output + context modifier.
//
// CRITICAL: ContextModifier is only honored for non-concurrent (serial) tools.
// Source: StreamingToolExecutor.ts:388-395
type ToolResult struct {
	// Data is the generic typed output. NOT always string!
	Data any `json:"data"`

	// Tools can inject follow-up messages into the conversation.
	NewMessages []types.Message `json:"new_messages,omitempty"`

	// ContextModifier modifies the execution context for subsequent tools.
	// Only honored for tools that aren't concurrency-safe.
	// Source: StreamingToolExecutor.ts:388-395
	ContextModifier func(*ToolUseContext) *ToolUseContext `json:"-"`

	// MCPMeta carries MCP protocol passthrough metadata.
	MCPMeta *MCPMeta `json:"mcp_meta,omitempty"`
}

// MCPMeta carries MCP protocol passthrough metadata.
type MCPMeta struct {
	Meta              map[string]any `json:"_meta,omitempty"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
}

// ApplyContextModifier enforces the concurrency restriction.
// Source: StreamingToolExecutor.ts:388-395
// Concurrent-safe tools' context modifiers are silently ignored.
func ApplyContextModifier(result *ToolResult, tctx *ToolUseContext, isConcurrencySafe bool) *ToolUseContext {
	if result.ContextModifier == nil {
		return tctx
	}
	if isConcurrencySafe {
		// Concurrent tool: silently ignore context modifier
		return tctx
	}
	// Serial tool: apply the modifier
	return result.ContextModifier(tctx)
}

// ---------------------------------------------------------------------------
// ProgressUpdate — streaming progress data
// Source: BashTool.tsx:826 — runShellCommand() yields progress events.
// ---------------------------------------------------------------------------

// ProgressUpdate is sent during tool execution via OnProgress in ToolUseContext.
// Engine emits it as EventToolOutputDelta for TUI display.
type ProgressUpdate struct {
	Lines      []string `json:"lines"`
	TotalLines int      `json:"total_lines"`
	TotalBytes int64    `json:"total_bytes"`
}

// ToolWithWireBlocks is an optional interface for tools that produce wire-format
// content blocks for tool_result.content sent to the LLM. Tools implementing this
// interface return an array of ContentBlock (text, image, ...). Tools that don't
// implement it get a default single-text-block wrapping of JSON-encoded Data.
type ToolWithWireBlocks interface {
	Tool
	FormatWireBlocks(data any) []types.ContentBlock
}

// ToolWithSummary is an optional interface for tools that provide custom
// TUI header summaries. Tools implementing this interface return a short
// string (e.g., a URL, file path, or command) for display in the tool card.
// Source: Tool.ts:539 — getToolUseSummary?(input) string | null
type ToolWithSummary interface {
	Tool
	Summary(input json.RawMessage) string
}

// ToolWithSearchOrRead is an optional interface for tools that classify as
// search/read operations. Collapsed TUI rendering skips output lines for
// these tools, showing only the header + ctrl+o hint.
// Source: Tool.ts:429 — isSearchOrReadCommand?(input)
type ToolWithSearchOrRead interface {
	Tool
	IsSearchOrRead(input json.RawMessage) SearchReadKind
}

// ToolWithDecodeResult lets a tool recover its concrete result type from
// persisted JSON, so RenderResult only ever sees concrete types.
type ToolWithDecodeResult interface {
	Tool
	DecodeResult(raw json.RawMessage) (any, error)
}

// IsDeferredTool is an optional interface for tools that should be deferred
// from the initial tool set and loaded on-demand via ToolSearch.
// Source: TS prompt.ts:62-108 — isDeferredTool()
type IsDeferredTool interface {
	Tool
	IsDeferred() bool
}

// ToolWithSearchHint is an optional interface for tools that provide a short
// search hint for ToolSearch scoring.
type ToolWithSearchHint interface {
	Tool
	SearchHint() string
}

// IsDeferred returns whether a tool should be deferred from the initial tool set.
// Source: TS prompt.ts:62-108 — isDeferredTool()
func IsDeferred(t Tool) bool {
	d, ok := t.(IsDeferredTool)
	return ok && d.IsDeferred()
}

// SearchHint returns the search hint for a tool, or empty string if none.
func SearchHint(t Tool) string {
	h, ok := t.(ToolWithSearchHint)
	if !ok {
		return ""
	}
	return h.SearchHint()
}

// ---------------------------------------------------------------------------
// Wire-array helpers — shared unwrap/wrap for DecodeResult implementations
// ---------------------------------------------------------------------------

// UnmarshalSingleBlock unwraps the standard [{type:"text",text:"..."}] wire
// form and returns the inner text for callers to unmarshal. Returns an error
// if the input is not array-form, is empty, or has no non-empty text block.
// Multi-block payloads (e.g. Computer's text+image) need their own unwrap
// and must not use this helper.
func UnmarshalSingleBlock(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || raw[0] != '[' {
		preview := string(raw)
		if runes := []rune(preview); len(runes) > 80 {
			preview = string(runes[:80])
		}
		return "", fmt.Errorf("tool: UnmarshalSingleBlock expects array-form content, got %q", preview)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text, nil
		}
	}
	return "", fmt.Errorf("tool: UnmarshalSingleBlock found no text block in array")
}

// WrapSingleBlock is the inverse of UnmarshalSingleBlock: it wraps a text
// payload in the standard wire array form. Used by renderToolOutput to feed
// persisted-file content (which is bare inner text) into DecodeResult.
func WrapSingleBlock(text string) json.RawMessage {
	textBytes, _ := json.Marshal(text)
	return json.RawMessage(`[{"type":"text","text":` + string(textBytes) + `}]`)
}

// ---------------------------------------------------------------------------
// ToolDef — source: Tool.ts:707-792
// ---------------------------------------------------------------------------

// ToolDef is a partial definition with optional fields filled by BuildTool.
// Source: Tool.ts:707-726
type ToolDef struct {
	// Required fields (no defaults)
	Name_        string
	Call_        func(ctx context.Context, input json.RawMessage, tctx *ToolUseContext) (*ToolResult, error)
	InputSchema_ func() json.RawMessage
	Description_ func(input json.RawMessage) (string, error)

	// Optional fields (defaults provided by BuildTool)
	Aliases_           []string
	IsReadOnly_        func(input json.RawMessage) bool // default: false
	IsDestructive_     func(input json.RawMessage) bool // default: false
	IsConcurrencySafe_ func(input json.RawMessage) bool // default: false
	IsEnabled_         func() bool                      // default: true
	InterruptBehavior_ InterruptBehavior                // default: InterruptBlock
	Prompt_            string                           // default: ""
	MaxResultSizeChars int                              // default: 50000

	// Permission checking
	CheckPermissions_ func(input json.RawMessage, tctx *ToolUseContext) types.PermissionResult

	// Result rendering
	RenderResult_ func(data any) string // default: json.Marshal

	// DecodeResult_ recovers a concrete result type from persisted JSON so
	// RenderResult only sees concrete types (never json.RawMessage).
	// Default: generic json.Unmarshal to any (string/map/slice/float64).
	DecodeResult_ func(raw json.RawMessage) (any, error)

	// FormatWireBlocks_ overrides the default wire-blocks output (single text
	// block of JSON-encoded data). Used by image-producing tools (fileread,
	// computer) to emit image blocks, and by tools that need custom text
	// (agent, skill, mcp). If set, BuildTool returns a tool that implements
	// ToolWithWireBlocks.
	FormatWireBlocks_ func(data any) []types.ContentBlock

	// Search/read classification for TUI collapse behavior.
	// If set, the built tool implements ToolWithSearchOrRead.
	IsSearchOrRead_ func(input json.RawMessage) SearchReadKind

	// Deferred loading
	ShouldDefer_ bool   // mark tool as deferred for ToolSearch
	SearchHint_  string // short description for search scoring
}

// builtTool wraps a ToolDef with defaults applied.
type builtTool struct {
	def ToolDef
}

// builtWireBlocksTool wraps a ToolDef and implements ToolWithWireBlocks.
type builtWireBlocksTool struct {
	builtTool
}

func (t *builtWireBlocksTool) FormatWireBlocks(data any) []types.ContentBlock {
	return t.def.FormatWireBlocks_(data)
}

// builtSearchReadTool wraps a ToolDef and implements ToolWithSearchOrRead.
type builtSearchReadTool struct {
	builtTool
}

func (t *builtSearchReadTool) IsSearchOrRead(input json.RawMessage) SearchReadKind {
	return t.def.IsSearchOrRead_(input)
}

// builtFullTool wraps a ToolDef and implements both ToolWithWireBlocks and
// ToolWithSearchOrRead.
type builtFullTool struct {
	builtTool
}

func (t *builtFullTool) FormatWireBlocks(data any) []types.ContentBlock {
	return t.def.FormatWireBlocks_(data)
}

func (t *builtFullTool) IsSearchOrRead(input json.RawMessage) SearchReadKind {
	return t.def.IsSearchOrRead_(input)
}

// BuildTool fills defaults and returns a Tool interface.
// Source: Tool.ts:783-792 — buildTool()
func BuildTool(def ToolDef) Tool {
	// Apply defaults
	if def.IsReadOnly_ == nil {
		def.IsReadOnly_ = func(json.RawMessage) bool { return false }
	}
	if def.IsDestructive_ == nil {
		def.IsDestructive_ = func(json.RawMessage) bool { return false }
	}
	if def.IsConcurrencySafe_ == nil {
		def.IsConcurrencySafe_ = func(json.RawMessage) bool { return false }
	}
	if def.IsEnabled_ == nil {
		def.IsEnabled_ = func() bool { return true }
	}
	if def.CheckPermissions_ == nil {
		def.CheckPermissions_ = func(json.RawMessage, *ToolUseContext) types.PermissionResult {
			return types.PermissionAllowDecision{}
		}
	}
	if def.MaxResultSizeChars == 0 {
		def.MaxResultSizeChars = 50000
	}
	if def.RenderResult_ == nil {
		def.RenderResult_ = func(data any) string {
			b, _ := json.Marshal(data)
			return string(b)
		}
	}
	if def.DecodeResult_ == nil {
		def.DecodeResult_ = func(raw json.RawMessage) (any, error) {
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return raw, nil
			}
			return v, nil
		}
	}
	if def.FormatWireBlocks_ != nil && def.IsSearchOrRead_ != nil {
		return &builtFullTool{builtTool{def: def}}
	}
	if def.FormatWireBlocks_ != nil {
		return &builtWireBlocksTool{builtTool{def: def}}
	}
	if def.IsSearchOrRead_ != nil {
		return &builtSearchReadTool{builtTool{def: def}}
	}
	return &builtTool{def: def}
}

func (t *builtTool) Name() string      { return t.def.Name_ }
func (t *builtTool) Aliases() []string { return t.def.Aliases_ }
func (t *builtTool) Description(input json.RawMessage) (string, error) {
	return t.def.Description_(input)
}
func (t *builtTool) InputSchema() json.RawMessage { return t.def.InputSchema_() }
func (t *builtTool) Call(ctx context.Context, input json.RawMessage, tctx *ToolUseContext) (*ToolResult, error) {
	return t.def.Call_(ctx, input, tctx)
}
func (t *builtTool) CheckPermissions(input json.RawMessage, tctx *ToolUseContext) types.PermissionResult {
	return t.def.CheckPermissions_(input, tctx)
}
func (t *builtTool) IsReadOnly(input json.RawMessage) bool    { return t.def.IsReadOnly_(input) }
func (t *builtTool) IsDestructive(input json.RawMessage) bool { return t.def.IsDestructive_(input) }
func (t *builtTool) IsConcurrencySafe(input json.RawMessage) bool {
	return t.def.IsConcurrencySafe_(input)
}
func (t *builtTool) IsEnabled() bool                      { return t.def.IsEnabled_() }
func (t *builtTool) InterruptBehavior() InterruptBehavior { return t.def.InterruptBehavior_ }
func (t *builtTool) MaxResultSize() int                   { return t.def.MaxResultSizeChars }

func (t *builtTool) Prompt() string               { return t.def.Prompt_ }
func (t *builtTool) RenderResult(data any) string { return t.def.RenderResult_(data) }
func (t *builtTool) DecodeResult(raw json.RawMessage) (any, error) {
	return t.def.DecodeResult_(raw)
}
func (t *builtTool) IsDeferred() bool   { return t.def.ShouldDefer_ }
func (t *builtTool) SearchHint() string { return t.def.SearchHint_ }
