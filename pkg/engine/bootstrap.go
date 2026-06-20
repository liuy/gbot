package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/config"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/tool/grep"
	"github.com/liuy/gbot/pkg/tool/job"
	lsptool "github.com/liuy/gbot/pkg/tool/lsp"
	"github.com/liuy/gbot/pkg/tool/repl"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	"github.com/liuy/gbot/pkg/tool/task"
	webtool "github.com/liuy/gbot/pkg/tool/web"
	"github.com/liuy/gbot/pkg/types"
)

// SharedDeps holds dependencies that don't vary per engine (main + sub-agents).
type SharedDeps struct {
	WorkingDir string
	GitStatus  *ctxbuild.GitStatusInfo
	SkillReg   *skills.Registry
	TaskList   *task.List
	McpReg     *mcp.Registry
	Hooks      *hooks.Hooks
	Cfg        *config.Config
	LSPReg     *lsp.Registry
}

// ToolRefs holds one engine's independent tool instances.
type ToolRefs struct {
	Reg     *tool.Registry
	BashReg *bash.BackgroundJobRegistry
	Agent   *agenttool.AgentTool
	REPL    *repl.REPLTool
	JobReg  *job.MultiRegistry
}

// CreateTools creates a fresh, complete set of tool instances for one engine.
// Engine wiring (SetEngine, OnNotify) is done separately by WireEngine.
func CreateTools(deps SharedDeps) ToolRefs {
	bashReg := bash.NewBackgroundJobRegistry()

	reg := tool.NewRegistry()
	reg.MustRegister(bash.New(bashReg))
	reg.MustRegister(fileread.New())
	reg.MustRegister(fileedit.New())
	reg.MustRegister(filewrite.New())
	reg.MustRegister(glob.New())
	reg.MustRegister(grep.New())

	at := agenttool.New()
	at.SetWorkingDir(deps.WorkingDir)
	at.SetGitStatus(deps.GitStatus)
	at.SetSkillRegistry(deps.SkillReg)
	// Inject tier resolver so agent model: "max" resolves to the configured model.
	if deps.Cfg != nil {
		at.SetResolveTierFn(deps.Cfg.ResolveTier)
	}
	// Stub SetNotifyFn — must be called before JobAdapter() so forkReg is initialized.
	at.SetNotifyFn(func(string) {}, func() string { return "" })
	reg.MustRegister(at)

	jobReg := job.NewMultiRegistry(bash.NewJobInfoAdapter(bashReg), at.JobAdapter())
	reg.MustRegister(job.NewJob(jobReg))

	reg.MustRegister(task.New(deps.TaskList))

	reg.MustRegister(skilltool.New(deps.SkillReg, at))

	replTool := repl.New()
	reg.MustRegister(replTool)

	// Web tool: zhipu API key and proxy come from config; the tool itself
	// wires up its own search providers and scraper registry.
	var proxyClient *http.Client
	var webKeys map[string]string
	if deps.Cfg != nil {
		proxyClient = deps.Cfg.ProxyHTTPClient()
		webKeys = deps.Cfg.Web
	}
	var webOpts []webtool.Option
	if len(webKeys) > 0 {
		webOpts = append(webOpts, webtool.WithAPIKeys(webKeys))
	}
	reg.MustRegister(webtool.New(proxyClient, webOpts...))

	reg.MustRegister(lsptool.New(deps.LSPReg))

	return ToolRefs{Reg: reg, BashReg: bashReg, Agent: at, REPL: replTool, JobReg: jobReg}
}

// WireEngine wires notification callbacks and injects the engine into tools.
// Each sub-engine created through RunAgent gets its own fresh tool instances
// via CreateTools.
func WireEngine(eng *Engine, refs ToolRefs, deps SharedDeps) {
	// Bash background job notifications → engine's attachment queue.
	refs.BashReg.OnNotify = func(n bash.JobNotification) {
		eng.EnqueueAttachment(types.QueuedItem{
			Value:     n.FormatXML(),
			Mode:      types.ItemModeJob,
			Timestamp: time.Now(),
		})
	}

	// Fork agent notifications → engine's attachment queue (overrides stub from CreateTools).
	refs.Agent.SetNotifyFn(
		func(xml string) {
			eng.EnqueueAttachment(types.QueuedItem{
				Value:     xml,
				Mode:      types.ItemModeJob,
				Timestamp: time.Now(),
			})
		},
		func() string { return eng.SystemPrompt() },
	)

	// Inject the engine into AgentTool so it can spawn sub-agents directly.
	// Engine.RunAgent implements SubagentEngine and handles all business
	// logic (agentDef resolution, system prompt building, tool filtering).
	refs.Agent.SetEngine(eng)

	// MCP connect — agent-specific MCP servers (used by ForkAgent + SkillTool).
	if deps.McpReg != nil {
		refs.Agent.SetMcpConnect(func(ctx context.Context, agentID string, rawSpecs []json.RawMessage) (*agenttool.McpConnectResult, error) {
			handle, err := deps.McpReg.ConnectAgentServers(ctx, agentID, rawSpecs)
			if err != nil || handle == nil {
				return nil, err
			}
			mcpTools := make(map[string]tool.Tool)
			for name, dt := range handle.Tools() {
				mcpTools[name] = NewMCPTool(dt, deps.McpReg)
			}
			return &agenttool.McpConnectResult{
				Tools:   mcpTools,
				Cleanup: handle.Cleanup,
			}, nil
		})
	}

	// REPL tool executor → this engine.
	{
		var replAskMu sync.Mutex
		replSessionAllowed := make(map[string]bool)
		refs.REPL.SetToolExecutor(func(toolCtx context.Context, name string, args json.RawMessage) (string, error) {
			return eng.ExecuteTool(toolCtx, name, args, replSessionAllowed, &replAskMu)
		})
	}
}
