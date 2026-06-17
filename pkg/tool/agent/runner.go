package agent

import (
	"context"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// AgentRunner holds a reference to the sub-agent execution engine.
// Both AgentTool and SkillTool use this to run sub-agents.
// The actual business logic (agentDef resolution, system prompt building,
// tool filtering) is handled by engine.Engine.RunAgent.
type AgentRunner struct {
	Engine SubagentEngine // sub-agent execution engine (engine.Engine)

	// Provider fields that cannot travel through AgentOpts:
	McpConnect McpConnectFunc // agent-specific MCP server connector
}

// RunAgentOpts configures a sub-agent execution for caller convenience.
type RunAgentOpts struct {
	Prompt       string   // user prompt for the sub-agent
	AgentType    string   // agent type ("" = General)
	Model        string   // model override ("" = inherit)
	AllowedTools []string // further restrict tools (nil = use agent def)
}

// RunAgent delegates to the SubagentEngine with translated options.
// The engine handles all business logic: agentDef resolution, system prompt
// building, tool filtering, context assembly, and execution.
func (r *AgentRunner) RunAgent(ctx context.Context, opts RunAgentOpts, tctx *tool.ToolUseContext) (*types.SubQueryResult, error) {
	if r.Engine == nil {
		return nil, fmt.Errorf("agent runner not initialized: engine not set")
	}

	var parentToolUseID string
	if tctx != nil {
		parentToolUseID = tctx.ToolUseID
	}

	return r.Engine.RunAgent(ctx, AgentOpts{
		Prompt:          opts.Prompt,
		AgentType:       opts.AgentType,
		Model:           opts.Model,
		AllowedTools:    opts.AllowedTools,
		ParentToolUseID: parentToolUseID,
		McpConnect:      r.McpConnect,
	})
}
