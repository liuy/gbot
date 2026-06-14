package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

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
	"github.com/liuy/gbot/pkg/tool/grep"
	"github.com/liuy/gbot/pkg/tool/job"
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
// Notification wiring (OnNotify, SetFactory) is done separately by WireEngine.
func CreateTools(deps SharedDeps) ToolRefs {
	bashReg := bash.NewBackgroundJobRegistry()

	reg := tool.NewRegistry()
	reg.MustRegister(bash.New(bashReg))
	reg.MustRegister(fileread.New(deps.LSPReg))
	reg.MustRegister(fileedit.New(deps.LSPReg))
	reg.MustRegister(filewrite.New(deps.LSPReg))
	reg.MustRegister(grep.New(deps.LSPReg))

	at := agenttool.New()
	at.SetWorkingDir(deps.WorkingDir)
	at.SetGitStatus(deps.GitStatus)
	at.SetSkillRegistry(deps.SkillReg)
	// Stub SetNotifyFn — must be called before JobAdapter() so forkReg is initialized.
	at.SetNotifyFn(func(string) {}, func() string { return "" })
	reg.MustRegister(at)

	jobReg := job.NewMultiRegistry(bash.NewJobInfoAdapter(bashReg), at.JobAdapter())
	reg.MustRegister(job.NewJob(jobReg))

	reg.MustRegister(task.New(deps.TaskList))

	reg.MustRegister(skilltool.New(deps.SkillReg))

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

	return ToolRefs{Reg: reg, BashReg: bashReg, Agent: at, REPL: replTool, JobReg: jobReg}
}

// WireEngine recursively wires notification callbacks and the agent factory
// for an engine and its tool set. Each sub-engine created through the factory
// gets its own fresh tool instances via CreateTools.
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

	// Agent factory — creates fresh tool set per sub-engine (recursive).
	refs.Agent.SetFactory(
		func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
			startTime := time.Now()
			subRefs := CreateTools(deps)

			// Merge MCP tools into sub-registry.
			if deps.McpReg != nil {
				for _, dt := range deps.McpReg.GetTools() {
					subRefs.Reg.MustRegister(NewMCPTool(dt, deps.McpReg))
				}
			}

			// agent.go's ResolveAgentTools already decided which tool names are allowed.
			// Map those names to fresh instances from the new registry.
			subTools := subRefs.Reg.ToolMap()
			if len(opts.Tools) > 0 {
				filtered := make(map[string]tool.Tool, len(opts.Tools))
				for name := range opts.Tools {
					if t, ok := subTools[name]; ok {
						filtered[name] = t
					}
				}
				subTools = filtered
			}

			subEng := eng.NewSubEngine(SubEngineOptions{
				Tools:           subTools,
				SystemPrompt:    string(opts.SystemPrompt),
				MaxTurns:        opts.MaxTurns,
				Model:           opts.Model,
				ParentToolUseID: opts.ParentToolUseID,
				AgentType:       opts.AgentType,
			})

			WireEngine(subEng, subRefs, deps)

			// Fire SubagentStart hook — collect additional context from plugins.
			hookInput := &hooks.HookInput{
				HookEventName: "SubagentStart",
				AgentID:       subEng.SessionID(),
				AgentType:     opts.AgentType,
			}
			for _, r := range deps.Hooks.SubagentStart(ctx, hookInput) {
				if r.AdditionalContext != "" {
					opts.UserContextMessages = append(opts.UserContextMessages, types.Message{
						Role:    types.RoleUser,
						Content: []types.ContentBlock{types.NewTextBlock(r.AdditionalContext)},
					})
				}
			}

			messages := opts.UserContextMessages
			if opts.Prompt != "" {
				messages = append(messages, types.Message{
					ID:      uuid.New().String(),
					Role:    types.RoleUser,
					Content: []types.ContentBlock{types.NewTextBlock(opts.Prompt)},
				})
			}
			if len(opts.ForkMessages) > 0 {
				messages = append(opts.ForkMessages, messages...)
			}
			var result QueryResult
			if len(opts.ForkMessages) > 0 || len(opts.UserContextMessages) > 0 {
				result = subEng.RunForkedQuery(ctx, messages, opts.SystemPrompt)
			} else {
				result = subEng.QuerySync(ctx, opts.Prompt, opts.SystemPrompt)
			}
			if result.Error != nil {
				if ctx.Err() != nil {
					r := agenttool.FinalizeResult(result.Messages, opts.AgentType, startTime, result.TotalUsage, 0)
					return r, nil
				}
				return nil, result.Error
			}
			toolUseCount := agenttool.CountToolUses(result.Messages)
			return agenttool.FinalizeResult(result.Messages, opts.AgentType, startTime, result.TotalUsage, toolUseCount), nil
		},
		refs.Reg.ToolMap,
	)

	// MCP connect — agent-specific MCP servers.
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
