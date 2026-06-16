package agent

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// AgentRunner is the neutral sub-agent execution engine.
// Mirrors TS runAgent.ts — a standalone function that needs environment context
// (workingDir, gitStatus, tools) to run a sub-agent. Both AgentTool and SkillTool
// (and any future caller) hold a *AgentRunner and call RunAgent.
type AgentRunner struct {
	Factory       SubEngineFactory
	ParentTools   func() map[string]tool.Tool
	WorkingDir    string
	GitStatus     *ctxbuild.GitStatusInfo
	SkillReg      SkillRegistry
	McpConnect    McpConnectFunc
	ResolveTierFn func(string) string

	skillsOnce  sync.Once
	skillsCache []types.SkillCommand
}

// RunAgentOpts configures a sub-agent execution.
// Mirrors TS runAgent() parameters that matter to gbot.
type RunAgentOpts struct {
	Prompt       string   // user prompt for the sub-agent
	AgentType    string   // agent type ("" = General)
	Model        string   // model override ("" = inherit)
	AllowedTools []string // further restrict tools (nil = use agent def)
}

// RunAgent mirrors TS runAgent(): resolves agent definition, filters tools,
// builds system prompt + user context, then calls the factory.
// Source: tools/AgentTool/runAgent.ts:248-860
func (r *AgentRunner) RunAgent(ctx context.Context, opts RunAgentOpts, tctx *tool.ToolUseContext) (*types.SubQueryResult, error) {
	if r.Factory == nil {
		return nil, fmt.Errorf("agent runner not initialized: factory not set")
	}

	// Resolve agent type (default: General)
	agentType := opts.AgentType
	if agentType == "" {
		agentType = "General"
	}

	agentDef, err := GetAgentDefinition(agentType)
	if err != nil {
		return nil, fmt.Errorf("unknown agent type %q: %w", agentType, err)
	}

	// Filter tools for this agent
	filteredTools := ResolveAgentTools(r.ParentTools(), agentDef)
	filteredTools = FilterMCPToolsForAgent(filteredTools, agentDef.RequiredMcpServers)

	if len(agentDef.McpServersRaw) > 0 && r.McpConnect != nil {
		mcpResult, err := r.McpConnect(ctx, "agent-"+agentType, agentDef.McpServersRaw)
		if err != nil {
			slog.Warn("agent MCP connect failed", "agent", agentType, "error", err)
		}
		if mcpResult != nil {
			if mcpResult.Cleanup != nil {
				defer func() {
					if cerr := mcpResult.Cleanup(); cerr != nil {
						slog.Warn("agent MCP cleanup failed", "agent", agentType, "error", cerr)
					}
				}()
			}
			maps.Copy(filteredTools, mcpResult.Tools)
		}
	}

	// Skill allowedTools override
	if len(opts.AllowedTools) > 0 {
		filteredTools = filterByAllowedTools(filteredTools, opts.AllowedTools)
	}

	// Resolve model
	model := opts.Model
	if model == "" {
		model = agentDef.Model
	}
	if model == "inherit" {
		model = ""
	}
	if model != "" && r.ResolveTierFn != nil {
		if resolved := r.ResolveTierFn(model); resolved != "" {
			model = resolved
		}
	}

	// Build system prompt
	basePrompt := agentDef.SystemPrompt()
	isGit := r.GitStatus != nil && r.GitStatus.IsGit
	systemPromptStr := enhanceSystemPrompt(basePrompt, filteredTools, r.WorkingDir, isGit, model)

	if r.GitStatus != nil && agentDef.AgentType != "Explore" && agentDef.AgentType != "Plan" {
		section := formatGitStatusForSystemPrompt(r.GitStatus)
		if section != "" {
			systemPromptStr += section
		}
	}

	// Build user context messages
	var userCtxMsgs []types.Message
	ctxMap := ctxbuild.LoadContextFiles(r.WorkingDir)
	ctxMap[ctxbuild.KeyCurrentDate] = fmt.Sprintf("Today's date is %s.", time.Now().Format("2006/01/02"))
	if agentDef.OmitClaudeMd {
		delete(ctxMap, ctxbuild.KeyClaudeMd)
		delete(ctxMap, ctxbuild.KeyProjectClaudeMd)
	}
	ctxText := ctxbuild.BuildPrependUserContext(ctxMap)
	if ctxText != "" {
		userCtxMsgs = append(userCtxMsgs, types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(ctxText)},
			Flags:   types.FlagMeta,
		})
	}

	// Skill preloading
	if len(agentDef.Skills) > 0 && r.SkillReg != nil {
		r.skillsOnce.Do(func() {
			r.skillsCache = r.SkillReg.GetAllSkills()
		})
		allSkills := r.skillsCache
		resolved := ResolveSkillNames(agentDef.Skills, allSkills, agentType)
		skillMsgs := BuildSkillMessages(resolved)
		userCtxMsgs = append(userCtxMsgs, skillMsgs...)
	}

	// Execute
	var parentToolUseID string
	if tctx != nil {
		parentToolUseID = tctx.ToolUseID
	}
	return r.Factory(ctx, AgentOpts{
		Prompt:              opts.Prompt,
		SystemPrompt:        systemPromptStr,
		Tools:               filteredTools,
		MaxTurns:            agentDef.MaxTurns,
		Model:               model,
		AgentType:           agentType,
		ParentToolUseID:     parentToolUseID,
		UserContextMessages: userCtxMsgs,
	})
}

func filterByAllowedTools(tools map[string]tool.Tool, allowed []string) map[string]tool.Tool {
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	result := make(map[string]tool.Tool, len(allowedSet))
	for name, t := range tools {
		if allowedSet[name] {
			result[name] = t
		}
	}
	return result
}
