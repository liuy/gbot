// Package agent implements the Agent tool for spawning sub-agents.
//
// Source reference: tools/AgentTool/AgentTool.tsx:239-1261 (call method)
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// SubEngineFactory — avoids circular dependency on engine package
// ---------------------------------------------------------------------------

// SubEngineFactory creates a sub-engine and synchronously executes a query.
// Injected by main.go after engine construction to avoid agent → engine import cycle.
type SubEngineFactory func(ctx context.Context, opts SubEngineOpts) (*types.SubQueryResult, error)

// SubEngineOpts passes parameters to the sub-engine factory.
// Uses only types from shared packages (no engine dependency).
type SubEngineOpts struct {
	Prompt             string               // actual user prompt for the sub-agent
	SystemPrompt       json.RawMessage      // sub-agent's system prompt
	Tools              map[string]tool.Tool // filtered tool set
	MaxTurns           int                  // 0 = default 50
	Model              string               // "" = inherit from parent
	AgentType          string               // resolved agent type (e.g. "general-purpose", "Explore")
	ParentToolUseID    string               // parent Agent tool call ID for TUI progress display
	ForkMessages       []types.Message      // non-nil: use pre-built fork messages instead of Prompt
	ParentSystemPrompt json.RawMessage      // fork: parent engine's rendered system prompt bytes
}

// ---------------------------------------------------------------------------
// AgentTool — source: tools/AgentTool/AgentTool.tsx:239-1261
// ---------------------------------------------------------------------------

// AgentTool is the tool that allows the LLM to spawn sub-agents.
// Source: AgentTool.tsx:239-1261 — call() sync path
type AgentTool struct {
	factory     SubEngineFactory            // injected via SetFactory
	parentTools func() map[string]tool.Tool // lazy accessor for parent engine tools
	forkReg     *ForkAgentRegistry          // nil = fork disabled
	notifyFn    func(xml string)            // injects notification into parent conversation
	sysPromptFn func() json.RawMessage      // returns parent engine's rendered system prompt
}

// New creates a new AgentTool with no dependencies.
func New() *AgentTool {
	return &AgentTool{}
}

// SetFactory injects the sub-engine factory and parent tools accessor.
// Called after engine construction in main.go to break the circular dependency.
func (t *AgentTool) SetFactory(factory SubEngineFactory, toolsFn func() map[string]tool.Tool) {
	t.factory = factory
	t.parentTools = toolsFn
}

// SetNotifyFn enables fork agent support. Injects the notification callback
// and system prompt accessor for fork agent lifecycle management.
func (t *AgentTool) SetNotifyFn(notifyFn func(xml string), sysPromptFn func() json.RawMessage) {
	t.notifyFn = notifyFn
	t.sysPromptFn = sysPromptFn
	t.forkReg = NewForkAgentRegistry()
}

// TaskAdapter returns a task.Registry wrapping the fork agent registry.
// Returns nil if fork is not enabled (SetNotifyFn not called).
func (t *AgentTool) TaskAdapter() task.Registry {
	if t.forkReg == nil {
		return nil
	}
	return NewForkAgentTaskAdapter(t.forkReg)
}

// Name returns the tool name.
// Source: tools/AgentTool/constants.ts — AGENT_TOOL_NAME = "Agent"
func (t *AgentTool) Name() string { return "Agent" }

// Aliases returns no aliases.
func (t *AgentTool) Aliases() []string { return nil }

// Description returns the tool description for the given input.
// Source: AgentTool.tsx — description pre-computed from input.
func (t *AgentTool) Description(input json.RawMessage) (string, error) {
	var parsed types.AgentInput
	if err := json.Unmarshal(input, &parsed); err != nil {
		return "Execute a sub-agent task", nil
	}
	if parsed.Description != "" {
		return parsed.Description, nil
	}
	if parsed.Prompt != "" {
		if len(parsed.Prompt) > 80 {
			return parsed.Prompt[:80] + "...", nil
		}
		return parsed.Prompt, nil
	}
	return "Execute a sub-agent task", nil
}

// InputSchema returns the JSON schema for Agent tool input.
// Source: AgentTool.tsx:82-138 — AgentToolInput
func (t *AgentTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {"type": "string", "description": "A short (3-5 word) description of the task"},
    "prompt": {"type": "string", "description": "The task for the agent to perform"},
    "subagent_type": {"type": "string", "description": "Agent type to use"},
    "name": {"type": "string", "description": "Name for the spawned agent. Makes it addressable via SendMessage while running."},
    "model": {"type": "string", "enum": ["sonnet","opus","haiku"]},
    "run_in_background": {"type": "boolean", "description": "Set to true to run this agent in the background"}
  },
  "required": ["description","prompt"]
}`)
}

// Call executes the sub-agent synchronously (or spawns fork agent in background).
// Source: AgentTool.tsx:239-1261 — call() sync path
func (t *AgentTool) Call(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	if t.factory == nil {
		return nil, fmt.Errorf("agent tool not initialized: sub-engine factory not set")
	}

	// Step 1: Parse input
	var agentInput types.AgentInput
	if err := json.Unmarshal(input, &agentInput); err != nil {
		return nil, fmt.Errorf("invalid Agent input: %w", err)
	}

	// Step 1.5: Recursive fork guard — applies to all paths
	if tctx != nil && IsInForkChild(tctx.Messages) {
		return nil, fmt.Errorf("cannot spawn agents from within a fork agent")
	}

	// Step 1.6: Fork routing — background agent path
	if agentInput.RunInBackground && t.forkReg != nil {
		return t.callFork(ctx, agentInput, tctx)
	}

	// Step 2: Resolve agent type (default: general-purpose)
	agentType := agentInput.SubagentType
	if agentType == "" {
		agentType = "General"
	}

	// Step 3: Look up agent definition
	agentDef, err := GetAgentDefinition(agentType)
	if err != nil {
		return nil, fmt.Errorf("unknown agent type %q: %w", agentType, err)
	}

	// Step 4: Filter tools for this agent
	parentTools := t.parentTools()
	filteredTools := ResolveAgentTools(parentTools, agentDef)

	// Step 5: Build system prompt (JSON string, matching context.Builder.Build() format)
	systemPromptStr := agentDef.SystemPrompt()
	encoded, _ := json.Marshal(systemPromptStr)
	systemPrompt := json.RawMessage(encoded)

	// Step 6: Resolve model
	// Source: AgentTool.tsx:579-583 — model resolution
	model := agentInput.Model
	if model == "" {
		model = agentDef.Model
	}
	// "inherit" means use the parent engine's model.
	// Normalize to "" so NewSubEngine inherits via e.model (engine.go:740-743).
	if model == "inherit" {
		model = ""
	}

	// Step 7: Call factory to create sub-engine and execute
	var parentToolUseID string
	if tctx != nil {
		parentToolUseID = tctx.ToolUseID
	}
	opts := SubEngineOpts{
		Prompt:          agentInput.Prompt,
		SystemPrompt:    systemPrompt,
		Tools:           filteredTools,
		MaxTurns:        agentDef.MaxTurns,
		Model:           model,
		AgentType:       agentType,
		ParentToolUseID: parentToolUseID,
	}

	result, err := t.factory(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("sub-agent execution failed: %w", err)
	}

	// Step 8: Return result
	return &tool.ToolResult{
		Data: result,
	}, nil
}

// CheckPermissions always allows — the engine handles permission checks
// for the sub-agent's own tool calls.
func (t *AgentTool) CheckPermissions(input json.RawMessage, tctx *types.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}

// IsReadOnly returns true — the Agent tool itself doesn't modify files.
// Sub-agent tool calls have their own permission checks.
func (t *AgentTool) IsReadOnly(input json.RawMessage) bool { return false }

// IsDestructive returns false — the Agent tool itself isn't destructive.
func (t *AgentTool) IsDestructive(input json.RawMessage) bool { return false }

// IsConcurrencySafe returns false — sub-agents must run serially.
func (t *AgentTool) IsConcurrencySafe(input json.RawMessage) bool { return false }

// IsEnabled returns true.
func (t *AgentTool) IsEnabled() bool { return true }

// InterruptBehavior returns InterruptBlock — let the sub-agent finish.
func (t *AgentTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptBlock }

// MaxResultSize returns the maximum result size for the agent tool.
func (t *AgentTool) MaxResultSize() int { return 100000 }

// Prompt returns the system prompt contribution from the Agent tool.
func (t *AgentTool) Prompt() string { return agentPrompt() }

// RenderResult renders the sub-query result for TUI display.
func (t *AgentTool) RenderResult(data any) string {
	result, ok := data.(*types.SubQueryResult)
	if !ok {
		b, _ := json.Marshal(data)
		return string(b)
	}
	if result.AsyncLaunched {
		return fmt.Sprintf("Fork agent %s running in background...", result.AgentID)
	}
	return result.Content
}

// ---------------------------------------------------------------------------
// Fork agent support
// ---------------------------------------------------------------------------

// forkMaxTurns is the maximum turns for a fork agent.
// Source: forkSubagent.ts — FORK_AGENT.maxTurns = 200
const forkMaxTurns = 200

// callFork spawns a background fork agent and returns immediately.
// Source: AgentTool.tsx — fork path in call()
func (t *AgentTool) callFork(ctx context.Context, input types.AgentInput, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	parentTools := t.parentTools()

	// Split messages: find the triggering assistant message and context history
	var triggerAssistant *types.Message
	var contextHistory []types.Message
	if tctx != nil && len(tctx.Messages) > 0 {
		for i := len(tctx.Messages) - 1; i >= 0; i-- {
			if tctx.Messages[i].Role == types.RoleAssistant {
				msg := tctx.Messages[i]
				triggerAssistant = &msg
				contextHistory = tctx.Messages[:i]
				break
			}
		}
	}

	// Build fork messages (contextHistory is filtered for incomplete tool calls)
	forkMessages := BuildForkMessages(triggerAssistant, contextHistory, input.Prompt)

	// Get parent system prompt (rendered bytes, not recomputed)
	var systemPrompt json.RawMessage
	if t.sysPromptFn != nil {
		systemPrompt = t.sysPromptFn()
	}

	var parentToolUseID string
	if tctx != nil {
		parentToolUseID = tctx.ToolUseID
	}

	// Resolve model
	model := input.Model
	if model == "inherit" || model == "" {
		model = ""
	}

	// Build the runFn closure
	runFn := func(runCtx context.Context) (*types.SubQueryResult, error) {
		opts := SubEngineOpts{
			ForkMessages:       forkMessages,
			SystemPrompt:       systemPrompt,
			Tools:              parentTools,
			MaxTurns:           forkMaxTurns,
			Model:              model,
			AgentType: "fork",
			ParentToolUseID:     parentToolUseID,
			ParentSystemPrompt: systemPrompt,
		}
		if input.SubagentType != "" {
			opts.AgentType = input.SubagentType
		}
		result, err := t.factory(runCtx, opts)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	// Build the notifyFn closure
	forkNotifyFn := func(agentID, toolUseID string, result *types.SubQueryResult, err error) {
		xml := buildForkNotificationXML(agentID, toolUseID, result, err, input.Description, input.Name)
		if t.notifyFn != nil {
			t.notifyFn(xml)
		}
		// CleanupCompleted is NOT called here — the adapter handles lazy cleanup
		// to avoid deleting agents before TaskOutput can query them.
	}

	// Detached context — fork agents must survive parent query lifecycle.
	// Source: TS forkSubagent.ts — fork agents have their own AbortController.
	// The parent query's context is cancelled by ReplState.FinishStream on
	// normal completion; if we derived from it, the fork agent would be killed.
	// Explicit cancellation is handled via ForkAgentRegistry.Cancel().
	detachedCtx := context.Background()

	state, err := t.forkReg.Spawn(detachedCtx, runFn, forkNotifyFn, input.Description, parentToolUseID)
	if err != nil {
		return nil, fmt.Errorf("failed to spawn fork agent: %w", err)
	}

	return &tool.ToolResult{
		Data: &types.SubQueryResult{
			AgentID:       state.ID,
			AgentType:     "fork",
			Content:       fmt.Sprintf("Fork agent %q launched in background", state.ID),
			AsyncLaunched: true,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// FinalizeResult extracts the final text from a sub-agent's QueryResult.
// Source: agentToolUtils.ts:276-357 — finalizeAgentTool
func FinalizeResult(messages []types.Message, agentType string, startTime time.Time, totalUsage types.Usage, toolUseCount int) *types.SubQueryResult {
	// Backward walk: find the last assistant message with text content.
	// Source: agentToolUtils.ts:301-317
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != types.RoleAssistant {
			continue
		}
		var textParts []string
		for _, blk := range msg.Content {
			if blk.Type == types.ContentTypeText {
				textParts = append(textParts, blk.Text)
			}
		}
		if len(textParts) > 0 {
			content := strings.Join(textParts, "\n")
			return &types.SubQueryResult{
				AgentType:         agentType,
				Content:           content,
				TotalDurationMs:   time.Since(startTime).Milliseconds(),
				TotalTokens:       totalUsage.InputTokens + totalUsage.OutputTokens,
				TotalToolUseCount: toolUseCount,
			}
		}
		// This assistant message has no text (pure tool_use) — continue backward
	}

	// Fallback: no text found in any assistant message
	return &types.SubQueryResult{
		AgentType:       agentType,
		Content:         "(agent completed with no text output)",
		TotalDurationMs: time.Since(startTime).Milliseconds(),
		TotalTokens:     totalUsage.InputTokens + totalUsage.OutputTokens,
	}
}
