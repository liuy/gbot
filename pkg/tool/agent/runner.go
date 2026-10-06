package agent

// SubagentDeps bundles the shared dependencies that any tool needing
// sub-agent execution (AgentTool, SkillTool, ...) must have wired up.
// Injected by bootstrap after engine construction.
type SubagentDeps struct {
	Engine        SubagentEngine      // sub-agent execution engine (engine.Engine)
	ResolveTierFn func(string) string // model tier resolver
	McpConnect    McpConnectFunc      // agent-specific MCP server connector
	SysPromptFn   func() string       // parent engine's rendered system prompt (for true fork)
}
