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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
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

// SkillRegistry provides skill lookup for agent skill preloading.
// Interface avoids circular import (agent → skills → types).
type SkillRegistry interface {
	GetAllSkills() []types.SkillCommand
}

// AgentOpts passes parameters to SubagentEngine.RunAgent.
// Uses only types from shared packages (no engine dependency).
type AgentOpts struct {
	Prompt              string                  // actual user prompt for the sub-agent
	SystemPrompt        string                  // sub-agent's system prompt (pre-built; empty = build from agent def)
	Tools               map[string]tool.Tool    // filtered tool set
	MaxTurns            int                     // 0 = no limit
	Model               string                  // "" = inherit from parent
	AgentType           string                  // resolved agent type (e.g. "General", "Explore")
	ParentToolUseID     string                  // parent Agent tool call ID for TUI progress display
	ForkMessages        []types.Message         // non-nil: use pre-built fork messages instead of Prompt
	UserContextMessages []types.Message         // [currentDate, claudeMd?, skill?...] injected before userPrompt
	GitStatus           *ctxbuild.GitStatusInfo // git status for system prompt injection (nil = no git info)
	ResolveTierFn       func(string) string     // model tier resolver (nil = identity)
	McpConnect          McpConnectFunc          // agent-specific MCP server connector (nil = skip)
	AllowedTools        []string                // further restrict tools to this list (nil = use agent def)
}

// ---------------------------------------------------------------------------
// AgentTool — source: tools/AgentTool/AgentTool.tsx:239-1261
// ---------------------------------------------------------------------------

// AgentTool is the tool that allows the LLM to spawn sub-agents.
// Source: AgentTool.tsx:239-1261 — call() sync path
type AgentTool struct {
	engine      SubagentEngine     // shared sub-agent engine (engine.Engine)
	forkReg     *ForkAgentRegistry // nil = fork disabled
	notifyFn    func(xml string)
	sysPromptFn func() string
	workingDir  string
	gitStatus   *ctxbuild.GitStatusInfo
	skillReg    SkillRegistry
	mcpConnect  McpConnectFunc
	resolveTier func(string) string
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
		Engine:        t.engine,
		GitStatus:     t.gitStatus,
		ResolveTierFn: t.resolveTier,
		McpConnect:    t.mcpConnect,
		SysPromptFn:   t.sysPromptFn,
	}
}

// SetNotifyFn enables fork agent support.
func (t *AgentTool) SetNotifyFn(notifyFn func(xml string), sysPromptFn func() string) {
	t.notifyFn = notifyFn
	t.sysPromptFn = sysPromptFn
	if t.forkReg == nil {
		t.forkReg = NewForkAgentRegistry()
	}
}

// SetWorkingDir sets the working directory for sub-agent system prompt enhancement.
func (t *AgentTool) SetWorkingDir(dir string) { t.workingDir = dir }

// SetResolveTierFn injects a tier-name resolver for agent model selection.
func (t *AgentTool) SetResolveTierFn(fn func(tier string) string) { t.resolveTier = fn }

// SetGitStatus sets the git status for sub-agent system prompt injection.
func (t *AgentTool) SetGitStatus(gs *ctxbuild.GitStatusInfo) { t.gitStatus = gs }

// SetSkillRegistry sets the skill registry for agent skill preloading.
func (t *AgentTool) SetSkillRegistry(reg SkillRegistry) { t.skillReg = reg }

// SetMcpConnect sets the MCP connector for agent-specific MCP server connections.
func (t *AgentTool) SetMcpConnect(fn McpConnectFunc) { t.mcpConnect = fn }

// McpConnectFn returns the MCP connector (for testing).
func (t *AgentTool) McpConnectFn() McpConnectFunc { return t.mcpConnect }

// JobAdapter returns a job.Registry wrapping the fork agent registry.
// Returns nil if fork is not enabled (SetNotifyFn not called).
func (t *AgentTool) JobAdapter() job.Registry {
	if t.forkReg == nil {
		return nil
	}
	return NewForkAgentJobAdapter(t.forkReg)
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
    "run_in_background": {"type": "boolean", "description": "Set to true to run this agent in the background"},
    "fork": {"type": "boolean", "description": "Set to true to inherit parent agent's conversation context"}
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
		if t.forkReg == nil {
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

	result, err := t.engine.RunAgent(ctx, AgentOpts{
		Prompt:          agentInput.Prompt,
		AgentType:       agentInput.SubagentType,
		Model:           agentInput.Model,
		GitStatus:       t.gitStatus,
		ResolveTierFn:   t.resolveTier,
		ParentToolUseID: parentToolUseID,
	})

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
			return fmt.Sprintf("Fork agent %s running in background...", v.AgentID)
		}
		return v.Content
	case json.RawMessage:
		var result types.SubQueryResult
		if err := json.Unmarshal(v, &result); err != nil {
			return string(v)
		}
		if result.AsyncLaunched {
			return fmt.Sprintf("Fork agent %s running in background...", result.AgentID)
		}
		return result.Content
	default:
		b, _ := json.Marshal(data)
		return string(b)
	}
}

func (t *AgentTool) NewResultType() any { return &types.SubQueryResult{} }

// FormatWireResult formats the tool result for the LLM wire format.
// Source: AgentTool.tsx:1340-1374
// Note: TS sends array-of-blocks, Go sends joined string. Valid per API.
// NOTE: When worktree support added, add !worktreeInfoText guard (TS line 1356).
func (t *AgentTool) FormatWireResult(data any) string {
	result, ok := data.(*types.SubQueryResult)
	if !ok {
		b, _ := json.Marshal(data)
		return string(b)
	}
	// One-shot: skip trailer (TS: ONE_SHOT_BUILTIN_AGENT_TYPES + !worktreeInfoText)
	// Also skip if async-launched (fork launch message already has agentId)
	if IsOneShotAgent(result.AgentType) && result.AgentID == "" && !result.AsyncLaunched {
		return result.Content
	}
	// Async-launched fork: just the launch message, no trailer
	if result.AsyncLaunched {
		return result.Content
	}
	var sb strings.Builder
	sb.WriteString(result.Content)
	if result.AgentID != "" {
		fmt.Fprintf(&sb, "\n\nagentId: %s (use SendMessage with to: '%s' to continue this agent)", result.AgentID, result.AgentID)
	}
	fmt.Fprintf(&sb, "\n<usage>total_tokens: %d\ntool_uses: %d\nduration_ms: %d</usage>", result.TotalTokens, result.TotalToolUseCount, result.TotalDurationMs)
	return sb.String()
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

	// Resolve model
	model := input.Model
	if model == "inherit" || model == "" {
		model = ""
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
		Model:           model,
		AgentType:       agentType,
		ParentToolUseID: parentToolUseID,
		GitStatus:       t.gitStatus,
		ResolveTierFn:   t.resolveTier,
	}

	// Sync path: run fork in-process and return result directly
	if !input.RunInBackground {
		result, err := t.engine.RunAgent(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("fork agent execution failed: %w", err)
		}
		return &tool.ToolResult{Data: result}, nil
	}

	// Async path: spawn in background
	runFn := func(runCtx context.Context) (*types.SubQueryResult, error) {
		return t.engine.RunAgent(runCtx, opts)
	}

	// Build the notifyFn closure
	forkNotifyFn := func(agentID, toolUseID string, result *types.SubQueryResult, err error) {
		xml := buildForkNotificationXML(agentID, toolUseID, result, err, input.Description, input.Name)
		if t.notifyFn != nil {
			t.notifyFn(xml)
		}
	}

	// Detached context — fork agents must survive parent query lifecycle.
	// CleanupCompleted is NOT called here — the adapter handles lazy cleanup
	// to avoid deleting agents before TaskOutput can query them.
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

// formatGitStatusForSystemPrompt formats git status for the agent system prompt.
// Mirrors Builder.GitStatusSection() but works without a Builder instance.
// Source: runAgent.ts:403-410 — appendSystemContext()
func FormatGitStatusForSystemPrompt(gs *ctxbuild.GitStatusInfo) string {
	if !gs.IsGit {
		return ""
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "\n\nGit branch: %s", gs.Branch)
	if gs.DefaultBranch != "" {
		fmt.Fprintf(&buf, "\nDefault branch: %s", gs.DefaultBranch)
	}
	if gs.IsDirty {
		buf.WriteString("\nWorking tree: dirty (uncommitted changes)")
	} else {
		buf.WriteString("\nWorking tree: clean")
	}
	return buf.String()
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
- Agent threads always have their cwd reset between bash calls, as a result please only use absolute file paths.
- In your final response, share file paths (always absolute, never relative) that are relevant to the task. Include code snippets only when the exact text is load-bearing (e.g., a bug you found, a function signature the caller asked for) — do not recap code you merely read.
- For clear communication with the user the assistant MUST avoid using emojis.
- Do not use a colon before tool calls. Text like "Let me read the file:" followed by a read tool call should just be "Let me read the file." with a period.`

// EnhanceSystemPrompt appends environment details and tool names to the agent's
// base system prompt, aligning with TS enhanceSystemPromptWithEnvDetails().
//
// Source: runAgent.ts:906 — getAgentSystemPrompt()
// Source: prompts.ts:760-791 — enhanceSystemPromptWithEnvDetails()
func EnhanceSystemPrompt(basePrompt string, tools map[string]tool.Tool, workingDir string, isGit bool, model string) string {
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
	parts = append(parts, buildEnvBlock(workingDir, isGit, model))

	return strings.Join(parts, "\n\n")
}

// buildEnvBlock generates the <env> block for the agent system prompt.
// Source: prompts.ts:606-649 — computeEnvInfo
func buildEnvBlock(workingDir string, isGit bool, model string) string {
	var buf strings.Builder
	buf.WriteString("Here is useful information about the environment you are running in:\n<env>")
	fmt.Fprintf(&buf, "\nWorking directory: %s", workingDir)
	if isGit {
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
	out, err := exec.Command("uname", "-sr").Output()
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
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
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
	for i := len(msg.Content) - 1; i >= 0; i-- {
		if msg.Content[i].Type == types.ContentTypeToolUse {
			return msg.Content[i].Name
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

	// Before fallback, check for interrupt marker on any message.
	// When the sub-agent is cancelled, appendInlineInterruptMessage adds
	// [Request interrupted by user] to the last message (user or assistant).
	for _, msg := range messages {
		for _, blk := range msg.Content {
			if blk.Type == types.ContentTypeText && strings.Contains(blk.Text, types.InterruptMessage) {
				return &types.SubQueryResult{
					AgentType:       agentType,
					Content:         "(agent interrupted by user)",
					TotalDurationMs: time.Since(startTime).Milliseconds(),
					TotalTokens:     totalUsage.InputTokens + totalUsage.OutputTokens,
				}
			}
		}
	}

	// Fallback: no text found in any assistant message
	return &types.SubQueryResult{
		AgentType:       agentType,
		Content:         "(agent completed with no text output)",
		TotalDurationMs: time.Since(startTime).Milliseconds(),
		TotalTokens:     totalUsage.InputTokens + totalUsage.OutputTokens,
	}
}
