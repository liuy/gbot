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
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/computer"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/tool/grep"
	"github.com/liuy/gbot/pkg/tool/job"
	lsptool "github.com/liuy/gbot/pkg/tool/lsp"
	"github.com/liuy/gbot/pkg/tool/memory/recall"
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
	McpReg     *mcp.Registry
	Hooks      *hooks.Hooks
	Cfg        *config.Config
	LSPReg     *lsp.Registry
	// WSRegistry is the daemon's inbound-device connection registry. nil in
	// TUI mode / sub-agent-only contexts — regDial then returns "daemon not
	// running". Set only by the daemon entrypoint (cmd/gbot).
	WSRegistry *computer.ConnectionRegistry

	// ShortStore enables the recall tool for the main agent. When non-nil,
	// recall searches both structured facts and message history.
	// nil = recall unavailable.
	ShortStore *short.Store
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
// taskList is passed explicitly rather than via SharedDeps because SharedDeps is shared across all engines and sub-engines.
func CreateTools(deps SharedDeps, taskList *task.List) ToolRefs {
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

	reg.MustRegister(task.New(taskList))

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

	// Computer tool (Android via gbot app). Always registered — inert
	// (returns "not connected; call connect first") until the model drives a
	// connect action. With a nil WSRegistry (TUI mode) connect surfaces the
	// "daemon not running" error; with a registry (daemon mode) it resolves
	// against the inbound device pool.
	reg.MustRegister(computer.New(computer.NewAndroidBackendWithRegistry(deps.WSRegistry)))

	// recall: read-only search of facts + messages. ShortStore satisfies both
	// the FactSearcher and MessageSearcher interfaces. nil guard lets the rest
	// of the toolset work when persistence is disabled.
	if deps.ShortStore != nil {
		reg.MustRegister(recall.New(deps.ShortStore, deps.ShortStore))
	}

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
