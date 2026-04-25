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
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// SubEngineFactory — avoids circular dependency on engine package
// ---------------------------------------------------------------------------

// SubEngineFactory creates a sub-engine and synchronously executes a query.
// Injected by main.go after engine construction to avoid agent → engine import cycle.
type SubEngineFactory func(ctx context.Context, opts AgentOpts) (*types.SubQueryResult, error)

// SkillRegistry provides skill lookup for agent skill preloading.
// Interface avoids circular import (agent → skills → types).
type SkillRegistry interface {
	GetAllSkills() []types.SkillCommand
}

// AgentOpts passes parameters to the sub-engine factory.
// Uses only types from shared packages (no engine dependency).
type AgentOpts struct {
	Prompt             string               // actual user prompt for the sub-agent
	SystemPrompt       json.RawMessage      // sub-agent's system prompt
	Tools              map[string]tool.Tool // filtered tool set
	MaxTurns           int                  // 0 = default 50
	Model              string               // "" = inherit from parent
	AgentType          string               // resolved agent type (e.g. "general-purpose", "Explore")
	ParentToolUseID    string               // parent Agent tool call ID for TUI progress display
	ForkMessages       []types.Message      // non-nil: use pre-built fork messages instead of Prompt
	ParentSystemPrompt json.RawMessage      // fork: parent engine's rendered system prompt bytes
	UserContextMessages []types.Message     // [currentDate, claudeMd?, skill?...] injected before userPrompt
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

	// Sub-agent environment context — loaded once at startup, read-only during execution
	workingDir    string
	gbotMdContent string
	gitStatus     *ctxbuild.GitStatusInfo

	// Skill registry — injected from main.go, provides all loaded skills.
	skillReg    SkillRegistry
	skillsOnce  sync.Once
	skillsCache []types.SkillCommand
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

// SetWorkingDir sets the working directory for sub-agent system prompt enhancement.
func (t *AgentTool) SetWorkingDir(dir string) { t.workingDir = dir }

// SetGBOTMDContent sets the GBOT.md content for sub-agent injection.
func (t *AgentTool) SetGBOTMDContent(content string) { t.gbotMdContent = content }

// SetGitStatus sets the git status for sub-agent system prompt injection.
func (t *AgentTool) SetGitStatus(gs *ctxbuild.GitStatusInfo) { t.gitStatus = gs }

// SetSkillRegistry sets the skill registry for agent skill preloading.
func (t *AgentTool) SetSkillRegistry(reg SkillRegistry) { t.skillReg = reg }

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

	// Step 4.5: Filter MCP tools by RequiredMcpServers
	// Source: runAgent.ts:95-218 — initializeAgentMcpServers
	filteredTools = FilterMCPToolsForAgent(filteredTools, agentDef.RequiredMcpServers)

	// Step 5.5: Resolve model (needed for enhanceSystemPrompt)
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

	// Step 5: Build enhanced system prompt
	// Source: runAgent.ts:906-932 — getAgentSystemPrompt()
	basePrompt := agentDef.SystemPrompt()
	isGit := t.gitStatus != nil && t.gitStatus.IsGit
	systemPromptStr := enhanceSystemPrompt(basePrompt, filteredTools, t.workingDir, isGit, model)

	// Append gitStatus to system prompt for non-Explore/Plan agents.
	// Source: runAgent.ts:403-410 — appendSystemContext()
	if t.gitStatus != nil && agentDef.AgentType != "Explore" && agentDef.AgentType != "Plan" {
		section := formatGitStatusForSystemPrompt(t.gitStatus)
		if section != "" {
			systemPromptStr += section
		}
	}

	encoded, _ := json.Marshal(systemPromptStr)
	systemPrompt := json.RawMessage(encoded)

	// Step 6: Build user context messages
	// Source: runAgent.ts:380-398 — getUserContext() + prependUserContext()
	var userCtxMsgs []types.Message
	// currentDate — Source: context.ts getUserContext().currentDate
	userCtxMsgs = append(userCtxMsgs, types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("Today's date is %s.", time.Now().Format("2006/01/02")))},
	})
	// claudeMd — Source: context.ts getUserContext().claudeMd, omitted for Explore/Plan
	if !agentDef.OmitClaudeMd && t.gbotMdContent != "" {
		userCtxMsgs = append(userCtxMsgs, types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(t.gbotMdContent)},
		})
	}

	// Step 6.5: Skill preloading
	// Source: runAgent.ts:578-646 â resolveSkillName + load + inject
	if len(agentDef.Skills) > 0 && t.skillReg != nil {
		t.skillsOnce.Do(func() {
			t.skillsCache = t.skillReg.GetAllSkills()
		})
	allSkills := t.skillsCache
		resolved := ResolveSkillNames(agentDef.Skills, allSkills, agentType)
		skillMsgs := BuildSkillMessages(resolved)
		userCtxMsgs = append(userCtxMsgs, skillMsgs...)
	}

		// Step 7: Call factory to create sub-engine and execute
	var parentToolUseID string
	if tctx != nil {
		parentToolUseID = tctx.ToolUseID
	}
	opts := AgentOpts{
		Prompt:             agentInput.Prompt,
		SystemPrompt:       systemPrompt,
		Tools:              filteredTools,
		MaxTurns:           agentDef.MaxTurns,
		Model:              model,
		AgentType:          agentType,
		ParentToolUseID:    parentToolUseID,
		UserContextMessages: userCtxMsgs,
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
		opts := AgentOpts{
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
		return t.factory(runCtx, opts)
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

// formatGitStatusForSystemPrompt formats git status for the agent system prompt.
// Mirrors Builder.GitStatusSection() but works without a Builder instance.
// Source: runAgent.ts:403-410 — appendSystemContext()
func formatGitStatusForSystemPrompt(gs *ctxbuild.GitStatusInfo) string {
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

// enhanceSystemPrompt appends environment details and tool names to the agent's
// base system prompt, aligning with TS enhanceSystemPromptWithEnvDetails().
//
// Source: runAgent.ts:906 — getAgentSystemPrompt()
// Source: prompts.ts:760-791 — enhanceSystemPromptWithEnvDetails()
func enhanceSystemPrompt(basePrompt string, tools map[string]tool.Tool, workingDir string, isGit bool, model string) string {
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

	// Fallback: no text found in any assistant message
	return &types.SubQueryResult{
		AgentType:       agentType,
		Content:         "(agent completed with no text output)",
		TotalDurationMs: time.Since(startTime).Milliseconds(),
		TotalTokens:     totalUsage.InputTokens + totalUsage.OutputTokens,
	}
}
