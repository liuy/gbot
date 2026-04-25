// Package hooks implements the hooks system for gbot.
//
// Source reference:
//   - src/schemas/hooks.ts — Hook Zod schemas (discriminated union on type)
//   - src/entrypoints/sdk/coreTypes.ts:25-53 — 27 HOOK_EVENTS
//   - src/entrypoints/sdk/coreSchemas.ts:387-443 — HookInput JSON schema
//   - src/types/hooks.ts:50-166 — syncHookResponseSchema (HookOutput)
//   - src/utils/hooks.ts — main execution engine
//   - src/services/tools/toolHooks.ts — tool integration
package hooks

import (
	"context"
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// HookEventName — source: coreTypes.ts:25-53 — HOOK_EVENTS
// ---------------------------------------------------------------------------

// HookEventName identifies a hook lifecycle event.
// TS defines 27 events; gbot implements the 11 core lifecycle events.
type HookEventName string

const (
	HookPreToolUse         HookEventName = "PreToolUse"
	HookPostToolUse        HookEventName = "PostToolUse"
	HookPostToolUseFailure HookEventName = "PostToolUseFailure"
	HookStop               HookEventName = "Stop"
	HookSubagentStop       HookEventName = "SubagentStop"
	HookStopFailure        HookEventName = "StopFailure"
	HookUserPromptSubmit   HookEventName = "UserPromptSubmit"
	HookSessionStart       HookEventName = "SessionStart"
	HookSessionEnd         HookEventName = "SessionEnd"
	HookPreCompact         HookEventName = "PreCompact"
	HookPostCompact        HookEventName = "PostCompact"
)

// ---------------------------------------------------------------------------
// HookType — source: schemas/hooks.ts:31-171 — discriminated union on Type
// ---------------------------------------------------------------------------

// HookType identifies the hook execution strategy.
type HookType string

const (
	HookTypeCommand HookType = "command" // BashCommandHookSchema
	HookTypePrompt  HookType = "prompt"  // PromptHookSchema
	HookTypeAgent   HookType = "agent"   // AgentHookSchema
	// HookTypeHTTP is not yet implemented. TS: HttpHookSchema (schemas/hooks.ts:97-126)
)

// ---------------------------------------------------------------------------
// HookConfig — source: schemas/hooks.ts:32-163
//
// TS uses a discriminated union on `type`, but Go uses a flat struct.
// Fields are shared across types; only relevant fields are populated per type.
// ---------------------------------------------------------------------------

// HookConfig is a single hook definition.
// Source: schemas/hooks.ts:32-163 — discriminated union on Type field.
type HookConfig struct {
	Type          HookType `json:"type"`                        // discriminator: "command"|"prompt"|"agent"
	Command       string   `json:"command,omitempty"`            // command hook: shell command to execute
	Prompt        string   `json:"prompt,omitempty"`             // prompt/agent hook: $ARGUMENTS substitution
	Model         string   `json:"model,omitempty"`              // prompt/agent hook: model override (default: small fast model)
	If            string   `json:"if,omitempty"`                 // permission rule filter (暂不实现)
	Timeout       int      `json:"timeout,omitempty"`            // seconds, 0 → default (600s for command, 30s for prompt)
	StatusMessage string   `json:"statusMessage,omitempty"`      // TUI spinner text
	Once          bool     `json:"once,omitempty"`               // remove after first run
	Async         bool     `json:"async,omitempty"`              // non-blocking (command hook only)
	AsyncRewake   bool     `json:"asyncRewake,omitempty"`        // async + exit 2 wakes model (command hook only)
}

// ---------------------------------------------------------------------------
// HookMatcher — source: schemas/hooks.ts:194-209 — HookMatcherSchema
// ---------------------------------------------------------------------------

// HookMatcher groups hooks under a tool name pattern.
// Source: schemas/hooks.ts:194-209 — HookMatcherSchema.
type HookMatcher struct {
	Matcher string       `json:"matcher,omitempty"` // exact/pipe/regex pattern (empty = match all)
	Hooks   []HookConfig `json:"hooks"`
}

// ---------------------------------------------------------------------------
// HooksConfig — source: schemas/hooks.ts:211-222
// ---------------------------------------------------------------------------

// HooksConfig is the top-level hooks configuration.
// Key = HookEventName (string), Value = []HookMatcher.
// Source: schemas/hooks.ts:211-222 — partialRecord(HookEvent, HookMatcher[]).
type HooksConfig map[string][]HookMatcher

// ---------------------------------------------------------------------------
// HookInput — source: coreSchemas.ts:387-443
// ---------------------------------------------------------------------------

// HookInput is the JSON payload sent to hook commands on stdin.
// All field names use snake_case to match the TS JSON schema exactly.
// Source: coreSchemas.ts:387-443 — BaseHookInputSchema + event-specific extensions.
type HookInput struct {
	HookEventName  string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	PermissionMode string          `json:"permission_mode,omitempty"` // Source: coreSchemas.ts:392
	AgentID        string          `json:"agent_id,omitempty"`        // Source: coreSchemas.ts:393-401
	AgentType      string          `json:"agent_type,omitempty"`      // Source: coreSchemas.ts:402-409

	// Tool-specific fields (PreToolUse, PostToolUse, PostToolUseFailure)
	ToolName     string          `json:"tool_name,omitempty"`
	ToolInput    json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	ToolResponse json.RawMessage `json:"tool_response,omitempty"`

	// SessionStart-specific
	Source string `json:"source,omitempty"` // "startup" | "resume" | "clear"

	// StopFailure-specific
	Reason string `json:"reason,omitempty"`

	// PreCompact/PostCompact-specific
	Trigger string `json:"trigger,omitempty"` // "manual" | "auto"
}

// ---------------------------------------------------------------------------
// HookOutput — source: types/hooks.ts:50-166 — syncHookResponseSchema
// ---------------------------------------------------------------------------

// HookOutput is the optional JSON parsed from hook stdout.
// Source: types/hooks.ts:50-166 — syncHookResponseSchema.
// HookSpecificOutput fields are flattened into this struct.
type HookOutput struct {
	// Shared fields (all event types)
	Decision          string          `json:"decision,omitempty"`          // "approve"|"block"
	Reason            string          `json:"reason,omitempty"`
	Continue          *bool           `json:"continue,omitempty"`          // default: true
	StopReason        string          `json:"stopReason,omitempty"`
	SystemMessage     string          `json:"systemMessage,omitempty"`

}


// ---------------------------------------------------------------------------
// HookOutcome — source: hooks.ts exit code semantics
// ---------------------------------------------------------------------------

// HookOutcome classifies the hook result.
// Source: hooks.ts — exit code semantics + timeout/cancellation.
type HookOutcome int

const (
	HookOutcomeSuccess          HookOutcome = iota // exit 0
	HookOutcomeBlocking                             // exit 2 (TS: blocking error)
	HookOutcomeNonBlockingError                     // exit other non-zero
	HookOutcomeTimeout                              // exceeded timeout
	HookOutcomeCancelled                            // context cancelled
)

// String returns a human-readable name for the outcome.
func (o HookOutcome) String() string {
	switch o {
	case HookOutcomeSuccess:
		return "success"
	case HookOutcomeBlocking:
		return "blocking"
	case HookOutcomeNonBlockingError:
		return "non_blocking_error"
	case HookOutcomeTimeout:
		return "timeout"
	case HookOutcomeCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// HookResult — source: types/hooks.ts — HookResult
// ---------------------------------------------------------------------------

// HookResult is returned from hook execution.
// Source: types/hooks.ts — HookResult type.
type HookResult struct {
	Outcome             HookOutcome
	Stdout              string
	Stderr              string
	Output              *HookOutput // parsed JSON stdout, nil if not JSON
	HookName            string      // command/prompt that ran
	PreventContinuation bool        // Source: preventContinuation field
	StopReason          string      // Source: stopReason field
	SystemMessage       string      // Source: systemMessage to inject
}

// ---------------------------------------------------------------------------
// HookDecision — combines multiple hook results into a policy decision
// ---------------------------------------------------------------------------

// HookDecision combines multiple hook results into a policy decision.
type HookDecision int

const (
	HookDecisionPassthrough HookDecision = iota // no hooks matched, or all approved
	HookDecisionApprove                         // hook explicitly approved (decision:"approve")
	HookDecisionBlock                           // hook blocked (exit 2 or decision:"block")
)

// String returns a human-readable name for the decision.
func (d HookDecision) String() string {
	switch d {
	case HookDecisionPassthrough:
		return "passthrough"
	case HookDecisionApprove:
		return "approve"
	case HookDecisionBlock:
		return "block"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Executor interfaces — break circular imports
//
// pkg/hooks/ cannot import pkg/llm/ or pkg/engine/ because those packages
// import pkg/hooks/. The interfaces below are satisfied by implementations
// injected at startup (cmd/gbot/main.go or pkg/engine/).
// ---------------------------------------------------------------------------

// HookExecutor executes a hook command (shell, stdin/stdout, exit codes).
// Production: CommandExecutor. Tests: HookRecorder.
type HookExecutor interface {
	ExecuteHook(ctx context.Context, command string, input *HookInput, timeout time.Duration) HookResult
}

// PromptExecutor executes a single LLM call for prompt hook evaluation.
// Source: execPromptHook.ts:21-211.
// Implemented by engine/main, injected into Hooks.
type PromptExecutor interface {
	ExecutePromptHook(ctx context.Context, prompt string, model string, timeout time.Duration) (ok bool, reason string, err error)
}

// AgentExecutor executes an agent hook using a sub-engine with tool access.
// Source: execAgentHook.ts:36-339.
// Implemented by engine, injected into Hooks.
type AgentExecutor interface {
	ExecuteAgentHook(ctx context.Context, prompt string, model string, tools []string, maxTurns int, timeout time.Duration) (ok bool, reason string, err error)
}

// ---------------------------------------------------------------------------
// Timeout constants — source: hooks.ts:166-168
// ---------------------------------------------------------------------------

const (
	// DefaultHookTimeout is the default timeout for hook commands.
	// Source: hooks.ts:166 — TOOL_HOOK_EXECUTION_TIMEOUT_MS = 10 * 60 * 1000
	DefaultHookTimeout = 600 * time.Second

	// SessionEndTimeout is the timeout for SessionEnd hooks (must be fast).
	// Source: hooks.ts:168 — SESSION_END_HOOK_TIMEOUT_MS_DEFAULT = 1500
	SessionEndTimeout = 1500 * time.Millisecond

	// DefaultPromptHookTimeout is the default timeout for prompt hooks.
	// Source: execPromptHook.ts:54 — 30 seconds default.
	DefaultPromptHookTimeout = 30 * time.Second

	// DefaultAgentHookTimeout is the default timeout for agent hooks.
	// Source: execAgentHook.ts — 60 seconds default.
	DefaultAgentHookTimeout = 60 * time.Second

	// DefaultAgentMaxTurns is the maximum number of turns for agent hooks.
	// Source: execAgentHook.ts — 50 turns.
	DefaultAgentMaxTurns = 50
)

// ---------------------------------------------------------------------------
// Helper: timeout resolution
// ---------------------------------------------------------------------------

// TimeoutForHook returns the timeout for a hook, falling back to the default
// for the given event type.
func TimeoutForHook(hookTimeout int, event HookEventName) time.Duration {
	if hookTimeout > 0 {
		return time.Duration(hookTimeout) * time.Second
	}
	return timeoutForEvent(event)
}

// timeoutForEvent returns the default timeout for a hook event.
func timeoutForEvent(event HookEventName) time.Duration {
	if event == HookSessionEnd {
		return SessionEndTimeout
	}
	return DefaultHookTimeout
}
