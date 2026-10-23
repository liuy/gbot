package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/long"
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
	"github.com/liuy/gbot/pkg/tool/message"
	"github.com/liuy/gbot/pkg/tool/repl"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	"github.com/liuy/gbot/pkg/tool/task"
	webtool "github.com/liuy/gbot/pkg/tool/web"
	"github.com/liuy/gbot/pkg/types"
)

// SharedDeps holds dependencies that don't vary per engine (main + sub-agents).
type SharedDeps struct {
	WorkingDir string
	SkillReg   *skills.Registry
	McpReg     *mcp.Registry
	Hooks      *hooks.Hooks
	Cfg        *config.Config
	// Providers maps provider names to live llm.Provider instances. RunAgent
	// uses it to switch a sub-agent's provider when its model is
	// "provider/model"; nil = no switching (model override then hard-fails).
	Providers config.ProviderMap
	LSPReg    *lsp.Registry
	// WSRegistry is the daemon's inbound-device connection registry. nil in
	// TUI mode / sub-agent-only contexts — regDial then returns "daemon not
	// running". Set only by the daemon entrypoint (cmd/gbot).
	WSRegistry *computer.ConnectionRegistry

	// ShortStore enables the recall tool for the main agent. When non-nil,
	// recall searches conversation history.
	// nil = recall unavailable.
	ShortStore *short.Store

	// EngineMgr and IDMinter split the background-agent substrate into its
	// global half and local half. EngineMgr is the global identity layer:
	// every live engine sits there, and its BgAgent lookup walks the live
	// engines' background tables via the manager, so agent-N ids stay
	// addressable from any engine. IDMinter is the global id source — one
	// counter for engines,
	// sub-agents and forks, so every producer mints collision-free agent-N
	// ids. Each engine's background table itself is per-engine (minted in
	// CreateTools from IDMinter): engine A's spawns are invisible and
	// unstoppable from engine B's job view. nil EngineMgr (reload's harvest
	// toolset) = resolver degraded, tool still registered for ToolPrompts
	// collection; nil IDMinter = each registry falls back to a private
	// counter.
	EngineMgr *EngineManager
	IDMinter  *agenttool.IDMinter
}

// ToolRefs holds one engine's independent tool instances.
type ToolRefs struct {
	Reg     *tool.Registry
	Bash    *bash.Bash
	BashReg *bash.BashRegistry
	Agent   *agenttool.AgentTool
	REPL    *repl.REPLTool
	JobReg  *job.MultiRegistry
	Message *message.MessageTool
}

// createToolsOpts carries CreateTools option state.
type createToolsOpts struct {
	root *ToolRefs
}

// CreateToolsOption customizes CreateTools.
type CreateToolsOption func(*createToolsOpts)

// WithSessionRootRefs binds the created toolset's Bash and Job tools to a
// session root's registries. A sidechain engine's own registries die with it,
// taking their panel rows with them; the session root lives as long as the
// slot, keeping every descendant's background job visible and stoppable.
// Notifications are unaffected — they follow the spawner (per-job hook), not
// the table. A root missing either registry degrades to no binding.
func WithSessionRootRefs(root *ToolRefs) CreateToolsOption {
	return func(o *createToolsOpts) { o.root = root }
}

// CreateTools creates a fresh, complete set of tool instances for one engine.
// Engine wiring (SetEngine, OnNotify) is done separately by WireEngine.
// taskList is passed explicitly rather than via SharedDeps because SharedDeps is shared across all engines and sub-engines.
func CreateTools(deps SharedDeps, taskList *task.List, opts ...CreateToolsOption) ToolRefs {
	var ctOpts createToolsOpts
	for _, opt := range opts {
		opt(&ctOpts)
	}
	root := ctOpts.root
	if root != nil && (root.BashReg == nil || root.JobReg == nil) {
		root = nil
	}

	bashReg := bash.NewBashRegistry()
	// Root binding swaps only the registries the TOOLS see; the returned
	// ToolRefs keeps the private ones because WireEngine writes its own
	// engine's sink into refs.BashReg.OnNotify — returning the shared root
	// table here would steal the root's notifications for this engine.
	toolBashReg := bashReg
	if root != nil {
		toolBashReg = root.BashReg
	}

	reg := tool.NewRegistry()
	bt := bash.New(toolBashReg)
	reg.MustRegister(bt)
	reg.MustRegister(fileread.New())
	reg.MustRegister(fileedit.New())
	reg.MustRegister(filewrite.New())
	reg.MustRegister(glob.New())
	reg.MustRegister(grep.New())

	at := agenttool.New()
	// Per-engine background table, minted from the shared id minter so
	// agent-N ids stay globally unique. Injection is unconditional: a nil
	// IDMinter makes the registry fall back to a private counter (harvest
	// toolsets, tests).
	bgReg := agenttool.NewAgentRegistryWithMinter(deps.IDMinter)
	at.SetRegistry(bgReg)
	// Stub notification callbacks — WireEngine overrides them with the real
	// engine attachment path. Harmless on toolsets that never get wired.
	at.SetNotifyFn(func(string) {}, func() string { return "" })
	reg.MustRegister(at)

	jobReg := job.NewMultiRegistry(bash.NewJobInfoAdapter(bashReg), at.JobAdapter())
	toolJobReg := jobReg
	if root != nil {
		toolJobReg = root.JobReg
	}
	reg.MustRegister(job.NewJob(toolJobReg))

	// message: registered unconditionally — the ToolPrompts harvest (reload,
	// boot) runs CreateTools with nil resolver deps and still needs the tool
	// text; delivery degrades to "unknown target" until real deps arrive.
	// The resolver consults mgr.BgAgent, which walks every engine's table.
	msgTool := message.New(&EngineMessageResolver{mgr: deps.EngineMgr})
	reg.MustRegister(msgTool)

	reg.MustRegister(task.New(taskList, deps.Hooks))

	reg.MustRegister(skilltool.New(deps.SkillReg, at))

	replTool := repl.New()
	reg.MustRegister(replTool)

	// Web tool: search provider API keys (anysearch/jina/tavily/zhipu) and
	// proxy come from config; the tool itself wires up its own search
	// providers and scraper registry.
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

	// recall: conversation history search. ShortStore is nil-guarded upstream.
	// MemoryDir points the archive scan at the projectspace memory dir.
	if deps.ShortStore != nil {
		reg.MustRegister(recall.New(deps.ShortStore, long.GetMemoryPath(deps.WorkingDir)))
	}

	return ToolRefs{Reg: reg, Bash: bt, BashReg: bashReg, Agent: at, REPL: replTool, JobReg: jobReg, Message: msgTool}
}

// WireEngine wires notification callbacks and injects the engine into tools.
// Each sub-engine created through RunAgent gets its own fresh tool instances
// via CreateTools.
// JobEventEmitter converts the job-event line into QueryEvents on this
// engine's dispatcher. Exported so multi-engine tests can wire a registry
// with the exact production conversion without a full WireEngine.
func JobEventEmitter(eng *Engine) func(types.QueryEventType, types.JobEvent) {
	return func(et types.QueryEventType, ev types.JobEvent) {
		if et == types.EventToolOutputDelta {
			// Bash job output snapshots ride this line with ToolResult
			// payload semantics: ToolUseID = job id (coalesce key + panel
			// routing), DisplayOutput = the rolling-window snapshot. The
			// Agent tag keeps the delta out of the chat-root stream in
			// hub consumers.
			eng.emitEvent(types.QueryEvent{
				Type:  types.EventToolOutputDelta,
				Agent: &types.AgentMeta{JobID: ev.ID},
				ToolResult: &types.ToolResultEvent{
					ToolUseID:     ev.ID,
					DisplayOutput: ev.Tail,
				},
			})
			return
		}
		eng.emitEvent(types.QueryEvent{Type: et, Job: &ev})
	}
}

func WireEngine(eng *Engine, refs ToolRefs, deps SharedDeps) {
	// One closure, two hook points. Registry-level OnNotify serves the jobs
	// the registry notifies as its owner (after root binding, the session-root
	// engine — e.g. Kill's killed notifications); the per-spawner Bash hook
	// restores tool-level priority so jobs this engine spawns notify THIS
	// engine even though they register in a shared root table. Same bytes,
	// same recipient contract.
	bashNotify := func(n bash.JobNotification) {
		eng.EnqueueAttachment(types.QueuedItem{
			Value:     n.FormatXML(),
			Mode:      types.ItemModeNotification,
			Origin:    &types.MessageOrigin{Kind: types.OriginJobCommand},
			Timestamp: time.Now(),
		})
	}
	refs.BashReg.OnNotify = bashNotify
	refs.Bash.SetJobNotifyFn(bashNotify)

	// Background agent notifications → engine's attachment queue (overrides stub from CreateTools).
	refs.Agent.SetNotifyFn(
		func(xml string) {
			eng.EnqueueAttachment(types.QueuedItem{
				Value:     xml,
				Mode:      types.ItemModeNotification,
				Origin:    &types.MessageOrigin{Kind: types.OriginJob},
				Timestamp: time.Now(),
			})
		},
		func() string { return eng.SystemPrompt() },
	)

	// Inject the engine into AgentTool so it can spawn sub-agents directly.
	// Engine.RunAgent implements SubagentEngine and handles all business
	// logic (agentDef resolution, system prompt building, tool filtering).
	refs.Agent.SetEngine(eng)

	// Messaging identity for this engine's tools: the message tool's
	// from=/self-reject id and the Agent tool's spawn-identity attribution.
	// RunAgent re-injects both on sub-engines (a sub-engine's EngineID() is
	// empty — NewSubEngine never sets engineID). Guarded: the wiring above
	// tolerates a nil eng (lazy closures), this read is eager.
	if eng != nil {
		refs.Agent.SetEngineID(eng.EngineID())
		refs.Message.SetSelfID(eng.EngineID())

		// Job lifecycle emitters → this engine's dispatcher. Root wiring is
		// the session-root panel feed; a sub-engine's wiring lands on its own
		// permanently-empty private registries (root-bound tools never spawn
		// there), so it stays inert. Sub-engines created by RunAgent are
		// wired by their own WireEngine call with the same shape.
		emitJob := JobEventEmitter(eng)
		refs.BashReg.OnJobEvent = emitJob
		refs.Agent.Registry().SetOnJobEvent(emitJob)
	}

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

	// REPL tool lister → fresh tool inventory for the JS tools object.
	// Called on every Repl Execute so MCP tools that connect mid-session
	// are discovered without restarting the JS VM.
	// AllTools() is live while ExecuteTool resolves against e.tools (refreshed per callLLM), so a tool connecting mid-turn shows up in tools.* but fails until the next turn.
	refs.REPL.SetToolLister(func() []repl.ToolMeta {
		all := eng.AllTools()
		names := make([]string, 0, len(all))
		for name := range all {
			names = append(names, name)
		}
		// Sorted for a stable ALL_TOOLS order across Executes.
		slices.Sort(names)
		metas := make([]repl.ToolMeta, 0, len(names))
		for _, name := range names {
			desc, err := all[name].Description(nil)
			if err != nil {
				desc = ""
			}
			metas = append(metas, repl.ToolMeta{Name: name, Description: desc})
		}
		return metas
	})
}
