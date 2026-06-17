package agent

import (
	ctxbuild "github.com/liuy/gbot/pkg/context"
)

// SubagentDeps bundles the shared dependencies that any tool needing
// sub-agent execution (AgentTool, SkillTool, ...) must have wired up.
// Injected by bootstrap after engine construction.
type SubagentDeps struct {
	Engine        SubagentEngine          // sub-agent execution engine (engine.Engine)
	GitStatus     *ctxbuild.GitStatusInfo // git status for system prompt injection
	ResolveTierFn func(string) string     // model tier resolver
	McpConnect    McpConnectFunc          // agent-specific MCP server connector
}
