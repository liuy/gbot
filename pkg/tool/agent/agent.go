// Package agent implements the Agent tool for spawning sub-agents.
//
// Source reference: tools/AgentTool/AgentTool.tsx:239-1261 (call method)
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils/proc"
)

// ---------------------------------------------------------------------------
// SubagentEngine — avoids circular dependency on engine package
// ---------------------------------------------------------------------------

// SubagentEngine is the interface sub-agent execution engines must implement.
// Implemented by engine.Engine; injected via AgentTool.SetEngine after wiring.
type SubagentEngine interface {
	RunAgent(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error)
}

// McpConnectResult holds the result of connecting agent-specific MCP servers.
type McpConnectResult struct {
	Tools   map[string]tool.Tool // discovered MCP tools
	Cleanup func() error         // must be deferred; nil if no cleanup needed
}

// McpConnectFunc connects agent-specific MCP servers.
// Injected by main.go to avoid agent → mcp direct dependency.
// Source: runAgent.ts:95-218 — initializeAgentMcpServers
type McpConnectFunc func(ctx context.Context, agentID string, rawSpecs []json.RawMessage) (*McpConnectResult, error)

// AgentOpts passes parameters to SubagentEngine.RunAgent.
// Uses only types from shared packages (no engine dependency).
type AgentOpts struct {
	Prompt              string               // actual user prompt for the sub-agent
	SystemPrompt        string               // sub-agent's system prompt (pre-built; empty = build from agent def)
	Tools               map[string]tool.Tool // filtered tool set
	MaxTurns            int                  // 0 = no limit
	Model               string               // "", "inherit", or "provider/model" (resolved by the engine; anything else hard-fails)
	AgentType           string               // resolved agent type (e.g. "General", "Explore")
	ParentToolUseID     string               // parent Agent tool call ID for TUI progress display
	ForkMessages        []types.Message      // non-nil: use pre-built fork messages instead of Prompt
	UserContextMessages []types.Message      // caller-supplied extras before userPrompt; spawnBackground appends the spawn identity line, RunAgent adds skill messages after it
	McpConnect          McpConnectFunc       // agent-specific MCP server connector (nil = skip)
	AllowedTools        []string             // further restrict tools to this list (nil = use agent def)
	Cwd                 string               // absolute cwd override for the sub-agent ("" = inherit parent)
	// AgentID is the background agent's registry id ("agent-N"), set
	// by spawnBackground before the goroutine reads opts. Empty for sync
	// spawns — RunAgent falls back to the parent engine id for those.
	AgentID string
	// Registry is the registry this background spawn lives in, set by
	// spawnBackground next to AgentID before the goroutine reads opts.
	// RunAgent consumes it to attach the sub-engine as the agent's message
	// sink — the spawning engine's own table, not a shared one.
	Registry *AgentRegistry
}

// ---------------------------------------------------------------------------
// AgentTool — source: tools/AgentTool/AgentTool.tsx:239-1261
// ---------------------------------------------------------------------------

// AgentTool is the tool that allows the LLM to spawn sub-agents.
// Source: AgentTool.tsx:239-1261 — call() sync path
type AgentTool struct {
	engine      SubagentEngine // shared sub-agent engine (engine.Engine)
	bgReg       *AgentRegistry // nil = background agents disabled
	notifyFn    func(xml string)
	sysPromptFn func() string
	mcpConnect  McpConnectFunc
	// engineID is this tool's engine's messaging identity (engine id, or the
	// sub-agent's own agent id after RunAgent re-injects). Used only for the
	// spawn identity line.
	engineID string
}

// New creates a new AgentTool with no dependencies.
func New() *AgentTool {
	return &AgentTool{}
}

// SetEngine injects the sub-agent execution engine. Called by WireEngine.
func (t *AgentTool) SetEngine(eng SubagentEngine) { t.engine = eng }

// SubagentDeps returns a SubagentDeps backed by this AgentTool's engine.
// Used by SkillTool and other tools that need sub-agent execution.
// Safe on nil receiver — returns nil, which downstream callers translate
// to "no sub-agent engine" error.
func (t *AgentTool) SubagentDeps() *SubagentDeps {
	if t == nil {
		return nil
	}
	return &SubagentDeps{
		Engine:      t.engine,
		McpConnect:  t.mcpConnect,
		SysPromptFn: t.sysPromptFn,
	}
}

// SetNotifyFn sets the background-agent notification callbacks. The registry
// itself comes only from SetRegistry — Call rejects background/fork spawns
// while bgReg is nil, so a tool used without a registry stays spawn-safe.
func (t *AgentTool) SetNotifyFn(notifyFn func(xml string), sysPromptFn func() string) {
	t.notifyFn = notifyFn
	t.sysPromptFn = sysPromptFn
}

// SetEngineID injects the messaging identity used in the spawn identity line.
// WireEngine passes eng.EngineID(); RunAgent re-injects the sub-agent's own id.
func (t *AgentTool) SetEngineID(id string) { t.engineID = id }

// EngineID returns the id injected by SetEngineID. Exported because
// cross-package wiring tests (package engine) cannot read the unexported field.
func (t *AgentTool) EngineID() string { return t.engineID }

// SetRegistry injects this engine's background-agent registry. CreateTools
// gives every engine a table; RunAgent then re-points sub tools at the root's
// table, so sub-engine spawns land there instead of in their own engine's
// (permanently empty) table. The shared IDMinter keeps agent-N ids unique
// across engines.
func (t *AgentTool) SetRegistry(reg *AgentRegistry) { t.bgReg = reg }

// Registry returns the injected registry. RunAgent reads it to route
// sub-engine spawns to the root's table, and the EngineManager discovers it
// through the engine's toolRefs; nil when SetRegistry was never called.
func (t *AgentTool) Registry() *AgentRegistry { return t.bgReg }

// SetMcpConnect sets the MCP connector for agent-specific MCP server connections.
func (t *AgentTool) SetMcpConnect(fn McpConnectFunc) { t.mcpConnect = fn }

// McpConnectFn returns the MCP connector (for testing).
func (t *AgentTool) McpConnectFn() McpConnectFunc { return t.mcpConnect }

// JobAdapter returns a job.Registry wrapping the background agent registry.
// Returns nil if SetRegistry was never called.
func (t *AgentTool) JobAdapter() job.Registry {
	if t.bgReg == nil {
		return nil
	}
	return NewBackgroundAgentJobAdapter(t.bgReg)
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
    "name": {"type": "string", "description": "Name for the spawned agent (display label in the TUI and in its completion notification)."},
    "run_in_background": {"type": "boolean", "description": "Set to true to run this agent in the background. Returns immediately with an agent ID; the result arrives as a job notification when the agent finishes. Applies to both fork and subagent_type agents."},
    "fork": {"type": "boolean", "description": "Set to true to inherit parent agent's conversation context"},
    "cwd": {"type": "string", "description": "Absolute path to run the agent in. Overrides the working directory for all filesystem and shell operations within this agent."}
  },
  "required": ["description","prompt"]
}`)
}

// Call executes the sub-agent synchronously (or spawns fork agent in background).
// Source: AgentTool.tsx:239-1261 — call() sync path
func (t *AgentTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("agent tool not initialized: sub-agent engine not set")
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

	// Step 1.6: Fork routing — inherit parent context
	if agentInput.Fork {
		if t.bgReg == nil {
			return nil, fmt.Errorf("fork mode is not available: fork agent registry not initialized")
		}
		return t.callFork(ctx, agentInput, tctx)
	}

	// ParentToolUseID lets NewSubEngine wire a taggedDispatcher so the
	// parent TUI's Agent tool card receives the sub-agent's streaming
	// events (text_delta/tool_start/tool_end). Without it, every event
	// dispatched inside the sub-engine is dropped.
	var parentToolUseID string
	if tctx != nil {
		parentToolUseID = tctx.ToolUseID
	}

	// One opts literal serves both paths, guaranteeing the background spawn
	// is field-identical to the sync call (MaxTurns 0 lets RunAgent apply
	// the agent-def default).
	opts := AgentOpts{
		Prompt:          agentInput.Prompt,
		AgentType:       agentInput.SubagentType,
		ParentToolUseID: parentToolUseID,
		Cwd:             agentInput.Cwd,
	}

	if agentInput.RunInBackground {
		if t.bgReg == nil {
			return nil, fmt.Errorf("background mode is not available: agent registry not initialized")
		}
		displayType := agentInput.SubagentType
		if displayType == "" {
			// Mirrors Engine.RunAgent's default for the launch message and
			// notification; opts.AgentType stays raw so the engine still
			// owns type resolution.
			displayType = "General"
		}
		return t.spawnBackground(opts, agentInput, displayType, parentToolUseID)
	}

	result, err := t.engine.RunAgent(ctx, opts)

	if err != nil {
		return nil, fmt.Errorf("sub-agent execution failed: %w", err)
	}
	return &tool.ToolResult{Data: result}, nil
}

// CheckPermissions always allows — the engine handles permission checks
// for the sub-agent's own tool calls.
func (t *AgentTool) CheckPermissions(input json.RawMessage, tctx *tool.ToolUseContext) types.PermissionResult {
	return types.PermissionAllowDecision{}
}

// IsReadOnly returns true — the Agent tool itself doesn't modify files.
// Sub-agent tool calls have their own permission checks.
func (t *AgentTool) IsReadOnly(input json.RawMessage) bool { return false }

// IsDestructive returns false — the Agent tool itself isn't destructive.
func (t *AgentTool) IsDestructive(input json.RawMessage) bool { return false }

// IsConcurrencySafe returns true — multiple agent calls can run in parallel.
// Each sub-engine has independent state; internal tool execution follows its
// own concurrency rules. Source: TS AgentTool.tsx:1273 isConcurrencySafe().
func (t *AgentTool) IsConcurrencySafe(input json.RawMessage) bool { return true }

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
	switch v := data.(type) {
	case *types.SubQueryResult:
		if v.AsyncLaunched {
			// Fork launch results hard-code AgentType "fork", so the
			// capitalized fork string stays reachable only from fork spawns.
			if v.AgentType == "fork" {
				return fmt.Sprintf("Fork agent %s running in background...", v.AgentID)
			}
			return fmt.Sprintf("%s agent %s running in background...", v.AgentType, v.AgentID)
		}
		return v.Content
	case string:
		return v
	default:
		b, _ := json.Marshal(data)
		return string(b)
	}
}

func (t *AgentTool) DecodeResult(raw json.RawMessage) (any, error) {
	text, err := tool.UnmarshalSingleBlock(raw)
	if err != nil {
		return nil, err
	}
	var r types.SubQueryResult
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// FormatWireBlocks formats the tool result for the LLM wire format as an array
// of ContentBlock (single text block in this case).
// Source: AgentTool.tsx:1340-1374
// Note: TS sends array-of-blocks; Go wraps the formatted string in a single
// text block to match the array-of-blocks wire shape.
// NOTE: When worktree support added, add !worktreeInfoText guard (TS line 1356).
func (t *AgentTool) FormatWireBlocks(data any) []types.ContentBlock {
	result, ok := data.(*types.SubQueryResult)
	if !ok {
		b, _ := json.Marshal(data)
		return []types.ContentBlock{types.NewTextBlock(string(b))}
	}
	// One-shot: skip trailer (TS: ONE_SHOT_BUILTIN_AGENT_TYPES + !worktreeInfoText)
	// Also skip if async-launched (fork launch message already has agentId)
	if IsOneShotAgent(result.AgentType) && result.AgentID == "" && !result.AsyncLaunched {
		return []types.ContentBlock{types.NewTextBlock(result.Content)}
	}
	// Async-launched fork: just the launch message, no trailer
	if result.AsyncLaunched {
		return []types.ContentBlock{types.NewTextBlock(result.Content)}
	}
	var sb strings.Builder
	sb.WriteString(result.Content)
	if result.AgentID != "" {
		fmt.Fprintf(&sb, "\n\nagentId: %s (addressable via the message tool with to: '%s' while running)", result.AgentID, result.AgentID)
	}
	fmt.Fprintf(&sb, "\n<usage>total_tokens: %d\ntool_uses: %d\nduration_ms: %d</usage>", result.TotalTokens, result.TotalToolUseCount, result.TotalDurationMs)
	return []types.ContentBlock{types.NewTextBlock(sb.String())}
}

// ---------------------------------------------------------------------------
// Fork agent support
// ---------------------------------------------------------------------------

// forkMaxTurns is the maximum turns for a fork agent.
// Source: forkSubagent.ts — FORK_AGENT.maxTurns = 200
const forkMaxTurns = 200

// callFork handles the fork path — inherits parent conversation context.
// If run_in_background is true, spawns asynchronously via Spawn.
// Otherwise, runs synchronously via engine.RunAgent (RunForkedQuery).
func (t *AgentTool) callFork(ctx context.Context, input types.AgentInput, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	// Source: runAgent.ts:370-373 — [...filterIncompleteToolCalls(forkContextMessages), ...promptMessages]
	// Source: AgentTool.tsx:239 — assistantMessage parameter passed to call()
	var contextHistory []types.Message
	var triggerAssistantMsg *types.Message
	if tctx != nil {
		contextHistory = tctx.Messages
		if len(tctx.AssistantContent) > 0 {
			triggerAssistantMsg = &types.Message{
				Role:    types.RoleAssistant,
				Content: tctx.AssistantContent,
			}
		}
	}
	forkMessages := BuildForkMessages(triggerAssistantMsg, contextHistory, input.Prompt)

	// Get parent system prompt (rendered bytes, not recomputed)
	var systemPrompt string
	if t.sysPromptFn != nil {
		systemPrompt = t.sysPromptFn()
	}

	var parentToolUseID string
	if tctx != nil {
		parentToolUseID = tctx.ToolUseID
	}

	agentType := "fork"
	if input.SubagentType != "" {
		agentType = input.SubagentType
	}

	opts := AgentOpts{
		// Prompt intentionally left empty — it's already embedded in
		// forkMessages via buildForkDirective. See: forkSubagent.ts:163.
		ForkMessages:    forkMessages,
		SystemPrompt:    systemPrompt,
		MaxTurns:        forkMaxTurns,
		AgentType:       agentType,
		ParentToolUseID: parentToolUseID,
		Cwd:             input.Cwd,
	}

	// Sync path: run fork in-process and return result directly
	if !input.RunInBackground {
		result, err := t.engine.RunAgent(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("fork agent execution failed: %w", err)
		}
		return &tool.ToolResult{Data: result}, nil
	}

	return t.spawnBackground(opts, input, "fork", parentToolUseID)
}

// spawnBackground launches a background agent in the shared registry and
// returns the async-launched tool result. displayType is the launch result's
// AgentType and the notification label when input.Name is unset ("fork" for
// forks, the resolved subagent type otherwise); the registry mints the
// agent-N id from the process-global counter.
func (t *AgentTool) spawnBackground(opts AgentOpts, input types.AgentInput, displayType, parentToolUseID string) (*tool.ToolResult, error) {
	runFn := func(runCtx context.Context, agentID string) (*types.SubQueryResult, error) {
		// Identity must be complete before the goroutine below reads opts —
		// mutating after Spawn returns would race the registry snapshot.
		opts.AgentID = agentID
		opts.Registry = t.bgReg
		opts.UserContextMessages = append(opts.UserContextMessages, types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock(spawnIdentityMessage(agentID, input.Name, t.engineID)),
			},
		})
		return t.engine.RunAgent(runCtx, opts)
	}

	// Fork notifications label the agent name-else-"fork" (the XML builder's
	// historical default); subagents label name-else-resolved-type.
	agentLabel := input.Name
	if agentLabel == "" {
		agentLabel = displayType
	}
	notifyFn := func(agentID, toolUseID string, result *types.SubQueryResult, err error) {
		xml := buildAgentNotificationXML(agentID, toolUseID, result, err, input.Description, agentLabel)
		if t.notifyFn != nil {
			t.notifyFn(xml)
		}
	}

	// Detached context — background agents must survive parent query lifecycle.
	// CleanupCompleted is NOT called here — the adapter handles lazy cleanup
	// to avoid deleting agents before TaskOutput can query them.
	detachedCtx := context.Background()

	state, err := t.bgReg.Spawn(detachedCtx, runFn, notifyFn, input.Description, parentToolUseID, displayType)
	if err != nil {
		return nil, fmt.Errorf("failed to spawn background agent: %w", err)
	}

	var content string
	if displayType == "fork" {
		content = fmt.Sprintf("Fork agent %q launched in background", state.ID)
	} else {
		content = fmt.Sprintf("%s agent %q launched in background", displayType, state.ID)
	}
	return &tool.ToolResult{
		Data: &types.SubQueryResult{
			AgentID:       state.ID,
			AgentType:     displayType,
			Content:       content,
			AsyncLaunched: true,
		},
	}, nil
}

// spawnIdentityMessage tells a spawned agent who it is (registry id), what it
// is called, and which engine spawned it — all three are resolvable message
// addresses. The name part drops entirely for unnamed agents. It opens a plain
// sub-agent's conversation, but not a background fork's: RunAgent prepends the
// inherited ForkMessages, so the line lands after that context.
func spawnIdentityMessage(agentID, name, engineID string) string {
	if name != "" {
		return fmt.Sprintf("你是后台 %s（%s），由 engine:%s 派生", agentID, name, engineID)
	}
	return fmt.Sprintf("你是后台 %s，由 engine:%s 派生", agentID, engineID)
}

// ---------------------------------------------------------------------------
// System prompt enhancement for sub-agents
// Source: prompts.ts:606-791 — computeEnvInfo + enhanceSystemPromptWithEnvDetails
// ---------------------------------------------------------------------------

// defaultAgentPrompt is the fallback when an agent's SystemPrompt() returns "".
// Source: prompts.ts:758 — DEFAULT_AGENT_PROMPT
const defaultAgentPrompt = `You are an agent for gbot, an interactive AI coding assistant. Given the user's message, you should use the tools available to complete the task. Complete the task fully—don't gold-plate, but don't leave it half-done. When you complete the task, respond with a concise report covering what was done and any key findings — the caller will relay this to the user, so it only needs the essentials.`

// agentNotes are appended to every agent's system prompt.
// Source: prompts.ts:766-770 — notes in enhanceSystemPromptWithEnvDetails
const agentNotes = `Notes:
- In your final response, share file paths (always absolute, never relative) that are relevant to the task. Include code snippets only when the exact text is load-bearing (e.g., a bug you found, a function signature the caller asked for) — do not recap code you merely read.
- For clear communication with the user the assistant MUST avoid using emojis.
- Do not use a colon before tool calls. Text like "Let me read the file:" followed by a read tool call should just be "Let me read the file." with a period.`

// EnhanceSystemPrompt appends environment details and tool names to the agent's
// base system prompt, aligning with TS enhanceSystemPromptWithEnvDetails().
//
// Source: runAgent.ts:906 — getAgentSystemPrompt()
// Source: prompts.ts:760-791 — enhanceSystemPromptWithEnvDetails()
func EnhanceSystemPrompt(basePrompt string, tools map[string]tool.Tool, workingDir string, model string) string {
	var parts []string

	// Base prompt (or fallback to DEFAULT_AGENT_PROMPT)
	// Source: runAgent.ts:914-931 — try/catch with DEFAULT_AGENT_PROMPT fallback
	if basePrompt == "" {
		basePrompt = defaultAgentPrompt
	}
	parts = append(parts, basePrompt)

	// Notes — Source: prompts.ts:766-770
	parts = append(parts, agentNotes)

	// Enabled tool names
	toolList := formatToolNamesList(tools)
	if toolList != "" {
		parts = append(parts, "\nEnabled tools:\n"+toolList)
	}

	// Environment info — Source: prompts.ts:606-649 — computeEnvInfo
	parts = append(parts, buildEnvBlock(workingDir, model))

	return strings.Join(parts, "\n\n")
}

// buildEnvBlock generates the <env> block for the agent system prompt.
// Source: prompts.ts:606-649 — computeEnvInfo
func buildEnvBlock(workingDir string, model string) string {
	var buf strings.Builder
	buf.WriteString("Here is useful information about the environment you are running in:\n<env>")
	fmt.Fprintf(&buf, "\nWorking directory: %s", workingDir)
	if isGitRepo(workingDir) {
		buf.WriteString("\nIs directory a git repo: Yes")
	} else {
		buf.WriteString("\nIs directory a git repo: No")
	}
	fmt.Fprintf(&buf, "\nPlatform: %s", runtime.GOOS)
	if shell := os.Getenv("SHELL"); shell != "" {
		fmt.Fprintf(&buf, "\nShell: %s", shell)
	} else {
		buf.WriteString("\nShell: /bin/bash")
	}
	if osVersion := getOSVersion(); osVersion != "" {
		fmt.Fprintf(&buf, "\nOS Version: %s", osVersion)
	}
	buf.WriteString("\n</env>")
	if model != "" {
		fmt.Fprintf(&buf, "\nYou are powered by the model %s.", model)
	}
	return buf.String()
}

// isGitRepo reports whether dir, or any parent of dir, contains a .git entry.
//
// The TS reference injects branch, default branch and dirty state alongside
// this flag (Source: context.ts). gbot deliberately does not: those are
// computed once at startup and go stale the moment the user runs git checkout
// or edits a file, so an agent that needs live state runs git itself.
func isGitRepo(dir string) bool {
	// A path that doesn't exist can't be inside a repo, even if its parents are.
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	for d := dir; ; {
		// A .git file counts as much as a directory — worktrees and submodules
		// use a file that points at the real gitdir.
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

// formatToolNamesList formats the tool names as a sorted bullet list.
func formatToolNamesList(tools map[string]tool.Tool) string {
	if len(tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf strings.Builder
	for _, name := range names {
		fmt.Fprintf(&buf, "- %s\n", name)
	}
	return buf.String()
}

// getOSVersion returns the OS version string.
// Source: prompts.ts:610 — getUnameSR() returns "Linux 6.6.4" etc.
// Cached with sync.OnceValue since OS version never changes during process lifetime.
var getOSVersion = sync.OnceValue(func() string {
	cmd := exec.Command("uname", "-sr")
	proc.HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

// ---------------------------------------------------------------------------
// Partial result extraction on cancellation
// Source: agentToolUtils.ts:488-500 — extractPartialResult
// ---------------------------------------------------------------------------

// ExtractPartialResult walks messages backward to find the last assistant
// message with non-empty text content. Returns joined text or empty string.
// Only called on cancellation (context.Canceled / user kill), not general errors.
//
// Source: agentToolUtils.ts:488-500 — extractPartialResult.
// Called on user_cancel_background (AgentTool.tsx:1006) and user_kill_async
// (agentToolUtils.ts:658) to preserve what the agent accomplished before being killed.
func ExtractPartialResult(messages []types.Message) string {
	for _, msg := range slices.Backward(messages) {

		if msg.Role != types.RoleAssistant {
			continue
		}
		// Extract text content blocks, joining with newline.
		// Source: agentToolUtils.ts:494 — extractTextContent(content, '\n')
		var textParts []string
		for _, blk := range msg.Content {
			if blk.Type == types.ContentTypeText && blk.Text != "" {
				textParts = append(textParts, blk.Text)
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n")
		}
		// This assistant message has no text (pure tool_use) — continue backward.
	}
	return ""
}

// ---------------------------------------------------------------------------
// Progress tracking helpers
// Source: agentToolUtils.ts:262-274 — countToolUses, getLastToolUseName
// ---------------------------------------------------------------------------

// CountToolUses counts tool_use blocks across all assistant messages.
//
// Source: agentToolUtils.ts:262-274 — countToolUses.
// Iterates forward through all messages, counting each tool_use block
// in assistant messages. Used to report tool use count in FinalizeResult.
func CountToolUses(messages []types.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, blk := range msg.Content {
			if blk.Type == types.ContentTypeToolUse {
				count++
			}
		}
	}
	return count
}

// GetLastToolUseName returns the name of the last tool_use block in a single
// assistant message. Returns empty string for non-assistant or no tool_use.
//
// Source: agentToolUtils.ts:363-367 — getLastToolUseName.
// Takes a SINGLE message (not array). Called per-message during streaming
// to emit task progress (AgentTool.tsx:946,1070).
func GetLastToolUseName(msg types.Message) string {
	if msg.Role != types.RoleAssistant {
		return ""
	}
	// Walk backward to find the last tool_use block.
	// Source: TS uses Array.findLast() — Go equivalent is reverse iteration.
	for _, v := range slices.Backward(msg.Content) {
		if v.Type == types.ContentTypeToolUse {
			return v.Name
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// FinalizeResult extracts the final text from a sub-agent's QueryResult.
// Source: agentToolUtils.ts:276-357 — finalizeAgentTool
func FinalizeResult(messages []types.Message, agentType string, startTime time.Time, totalUsage types.Usage, toolUseCount int) *types.SubQueryResult {
	// Backward walk: find the last assistant message with text content.
	// Source: agentToolUtils.ts:301-317
	for _, msg := range slices.Backward(messages) {

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

	// Before fallback, check for interrupt marker on any message.
	// When the sub-agent is cancelled, appendInlineInterruptMessage adds
	// [Request interrupted by user] to the last message (user or assistant).
	for _, msg := range messages {
		for _, blk := range msg.Content {
			if blk.Type == types.ContentTypeText && strings.Contains(blk.Text, types.InterruptMessage) {
				return &types.SubQueryResult{
					AgentType:         agentType,
					Content:           "(agent interrupted by user)",
					TotalDurationMs:   time.Since(startTime).Milliseconds(),
					TotalTokens:       totalUsage.InputTokens + totalUsage.OutputTokens,
					TotalToolUseCount: toolUseCount,
				}
			}
		}
	}

	// Fallback: no text found in any assistant message
	return &types.SubQueryResult{
		AgentType:         agentType,
		Content:           "(agent completed with no text output)",
		TotalDurationMs:   time.Since(startTime).Milliseconds(),
		TotalTokens:       totalUsage.InputTokens + totalUsage.OutputTokens,
		TotalToolUseCount: toolUseCount,
	}
}
