// Package tool defines the tool system for gbot.
//
// Source reference: Tool.ts:362-695
// The Tool interface captures behavioral methods from the TS source.
// React JSX render methods are excluded — replaced by TUI ToolRenderer.
package tool

import (
	"context"
	"encoding/json"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// InterruptBehavior — source: Tool.ts:416
// ---------------------------------------------------------------------------

// InterruptBehavior determines how a tool responds to interrupts.
type InterruptBehavior int

const (
	InterruptCancel InterruptBehavior = iota // 'cancel' — stop tool, discard result
	InterruptBlock                            // 'block' — keep running, new message waits
)

// ---------------------------------------------------------------------------
// SearchReadKind — source: Tool.ts:429-433
// ---------------------------------------------------------------------------

// SearchReadKind classifies a tool call as search/read/list.
type SearchReadKind struct {
	IsSearch bool
	IsRead   bool
	IsList   bool
}

// ---------------------------------------------------------------------------
// Tool interface — source: Tool.ts:362-695
// ---------------------------------------------------------------------------

// Tool is the complete tool interface used by the engine.
// Source: Tool.ts:362-695 — 30+ methods. Phase 1 uses a subset.
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
	Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*ToolResult, error)

	// ── Permission ────────────────────────────────────────
	// Source: Tool.ts:500-503
	CheckPermissions(input json.RawMessage, tctx *types.ToolUseContext) types.PermissionResult

	// ── Behavioral Properties ─────────────────────────────
	// Source: Tool.ts:404-434
	IsReadOnly(input json.RawMessage) bool
	IsDestructive(input json.RawMessage) bool
	IsConcurrencySafe(input json.RawMessage) bool
	IsEnabled() bool

	// Interrupt behavior: source values are 'cancel' | 'block' (Tool.ts:416)
	InterruptBehavior() InterruptBehavior

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
	ContextModifier func(*types.ToolUseContext) *types.ToolUseContext `json:"-"`

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
func ApplyContextModifier(result *ToolResult, tctx *types.ToolUseContext, isConcurrencySafe bool) *types.ToolUseContext {
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
// ToolDef — source: Tool.ts:707-792
// ---------------------------------------------------------------------------

// ToolDef is a partial definition with optional fields filled by BuildTool.
// Source: Tool.ts:707-726 — Omit<Tool, DefaultableToolKeys> & Partial<Pick<Tool, DefaultableToolKeys>>
type ToolDef struct {
	// Required fields (no defaults)
	Name_       string
	Call_       func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*ToolResult, error)
	InputSchema_ func() json.RawMessage
	Description_ func(input json.RawMessage) (string, error)

	// Optional fields (defaults provided by BuildTool)
	Aliases_           []string
	IsReadOnly_        func(input json.RawMessage) bool // default: false
	IsDestructive_     func(input json.RawMessage) bool // default: false
	IsConcurrencySafe_ func(input json.RawMessage) bool // default: false
	IsEnabled_         func() bool                       // default: true
	InterruptBehavior_ InterruptBehavior                 // default: InterruptBlock
	Prompt_            string                            // default: ""
	MaxResultSizeChars int                               // default: 50000

	// Permission checking
	CheckPermissions_ func(input json.RawMessage, tctx *types.ToolUseContext) types.PermissionResult

	// Result rendering
	RenderResult_ func(data any) string // default: json.Marshal

}

// builtTool wraps a ToolDef with defaults applied.
type builtTool struct {
	def ToolDef
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
		def.CheckPermissions_ = func(json.RawMessage, *types.ToolUseContext) types.PermissionResult {
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
	return &builtTool{def: def}
}

func (t *builtTool) Name() string                                          { return t.def.Name_ }
func (t *builtTool) Aliases() []string                                     { return t.def.Aliases_ }
func (t *builtTool) Description(input json.RawMessage) (string, error)     { return t.def.Description_(input) }
func (t *builtTool) InputSchema() json.RawMessage                          { return t.def.InputSchema_() }
func (t *builtTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*ToolResult, error) {
	return t.def.Call_(ctx, input, tctx)
}
func (t *builtTool) CheckPermissions(input json.RawMessage, tctx *types.ToolUseContext) types.PermissionResult {
	return t.def.CheckPermissions_(input, tctx)
}
func (t *builtTool) IsReadOnly(input json.RawMessage) bool      { return t.def.IsReadOnly_(input) }
func (t *builtTool) IsDestructive(input json.RawMessage) bool   { return t.def.IsDestructive_(input) }
func (t *builtTool) IsConcurrencySafe(input json.RawMessage) bool { return t.def.IsConcurrencySafe_(input) }
func (t *builtTool) IsEnabled() bool                            { return t.def.IsEnabled_() }
func (t *builtTool) InterruptBehavior() InterruptBehavior       { return t.def.InterruptBehavior_ }
func (t *builtTool) Prompt() string                             { return t.def.Prompt_ }
func (t *builtTool) RenderResult(data any) string               { return t.def.RenderResult_(data) }
