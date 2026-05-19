// Package main is the CLI entrypoint for gbot.
//
// Source reference: main.tsx
// Bootstraps config, LLM provider, tools, engine, and launches the TUI.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"os"
	"path/filepath"
	"time"
	"sync"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/dream"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/bash"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	skills "github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/tool/grep"
	"github.com/liuy/gbot/pkg/tool/repl"
	"github.com/liuy/gbot/pkg/tool/job"
	tasklist "github.com/liuy/gbot/pkg/tool/tasks"
	"github.com/liuy/gbot/pkg/tui"
)

func main() {
	// Debug logging: write info-level events to log file.
	// This provides comprehensive observability for diagnosing token stats,
	// event ordering, and rendering issues.
	var logPath string
	if home, err := os.UserHomeDir(); err == nil {
		logDir := filepath.Join(home, ".gbot")
		_ = os.MkdirAll(logDir, 0755)
		logPath = filepath.Join(logDir, "gbot.log")
	} else {
		logPath = filepath.Join(os.TempDir(), "gbot.log")
	}
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	// 1. Load config from ~/.gbot/settings.json, ~/.claude/settings.minimax.json, or env vars
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// 2. Create LLM providers from config
	providerMap := createAllProviders(cfg)
	if len(providerMap) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no API key configured.")
		fmt.Fprintln(os.Stderr, "Set providers[].keys in ~/.gbot/settings.json")
		os.Exit(1)
	}

	// Primary provider is the first configured one
	defaultProvider, defaultTier, err := cfg.ParseModel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	var provider llm.Provider
	var model string
	var primaryProviderCfg *config.Provider
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if prov, ok := providerMap[p.Name]; ok {
			if defaultProvider != "" && p.Name != defaultProvider {
				continue
			}
			provider = prov
			model = p.Models[defaultTier]
			if model == "" {
				model = p.Models[config.TierPro]
			}
			primaryProviderCfg = p
			break
		}
	}
	if provider == nil || model == "" {
		fmt.Fprintln(os.Stderr, "Error: could not resolve primary provider/model")
		os.Exit(1)
	}

	// 3. Resolve working directory early
	workingDir, _ := os.Getwd()

	// Skill registry (shared across all engines)
	skillReg := skills.NewRegistry(workingDir)
	if err := skillReg.Load(); err != nil {
		slog.Warn("main: skill registry load failed", "error", err)
	}

	// Agent definitions — loaded once globally
	agenttool.InitLoader(workingDir)

	// Plugin system — discover and load plugins before MCP/hooks/skills
	loadedPlugins, pluginErr := plugins.LoadAndInitialize(context.Background(), workingDir)
	if pluginErr != nil {
		slog.Warn("main: plugin loading failed", "error", pluginErr)
	}

	taskList := tasklist.NewList("")

	// Create engine
	logger := slog.Default()
	if cfg.Verbose {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	h := hub.NewHub()

	// MCP registry
	var pluginServers map[string]mcp.ScopedMcpServerConfig
	if loadedPlugins != nil {
		pluginServers = loadedPlugins.McpServers
	}
	mcpRegistry, err := mcp.LoadAndConnectMCP(context.Background(), workingDir, mcp.TransportFactory{}, pluginServers)
	if err != nil {
		slog.Warn("main: MCP initialization failed", "error", err)
	}

	contextWindow, maxTokens := primaryProviderCfg.ResolveCapabilities(model)

	// Hooks system
	var hooksConfig hooks.HooksConfig
	if len(cfg.Hooks) > 0 {
		if err := json.Unmarshal(cfg.Hooks, &hooksConfig); err != nil {
			slog.Warn("main: failed to parse hooks config", "error", err)
		}
	}
	hookExecutor := &hooks.CommandExecutor{
		Env: []string{
			hooks.FormatEnvVar("GBOT_PROJECT_DIR", workingDir),
		},
	}
	hookSystem := hooks.NewHooks(hooksConfig, hookExecutor)

	// Merge plugin hooks, skills, and agents
	if loadedPlugins != nil {
		if len(loadedPlugins.Hooks) > 0 {
			hooksConfig = plugins.MergeHooks(hooksConfig, loadedPlugins.Hooks)
			hookSystem.ReloadConfig(hooksConfig)
		}
		// Plugin env vars are injected per-hook via extraEnv in dispatch(),
		// not via shared hookExecutor.Env, to avoid cross-plugin pollution.
		_ = loadedPlugins.EnvVars
		skillReg.RegisterPluginSkills(loadedPlugins.Skills)
		agenttool.GlobalLoader().RegisterPluginAgents(loadedPlugins.Agents)

		// Register plugin skills for TUI slash command completion.
		// Skills appear in the / dropdown but fall through to engine as user messages.
		if skillCmds := skillReg.GetSkillToolSkills(); len(skillCmds) > 0 {
			slashCmds := make(map[string]tui.CommandDef, len(skillCmds))
			for _, sc := range skillCmds {
				slashCmds[sc.Name] = tui.CommandDef{
					Description: sc.Description,
					HasArgs:     true,
				}
			}
			tui.RegisterSlashCommands(slashCmds)
		}
	}

	// Permission rules
	configDir, _ := config.ConfigDir()
	permRules := permission.LoadConfig(configDir, workingDir)
	var permChecker *permission.Checker
	if len(permRules) > 0 {
		permChecker = permission.NewChecker(permRules)
		slog.Info("main: permission rules loaded", "count", len(permRules))
	}

	// Guard: only pass permChecker if non-nil to avoid Go nil interface trap.
	// nil *Checker assigned to PermissionChecker interface is non-nil as interface
	// but panics on method calls.
	var permCheckerIface permission.PermissionChecker
	if permChecker != nil {
		permCheckerIface = permChecker
	}

	// Construct shared dependencies (immutable across all engines)
	gitStatus := ctxbuild.LoadGitStatus(workingDir)
	deps := toolDeps{
		workingDir: workingDir,
		gitStatus:  gitStatus,
		skillReg:   skillReg,
		taskList:   taskList,
		mcpReg:     mcpRegistry,
		hooks:      hookSystem,
	}

	// Create per-engine tool instances for the main engine
	mainRefs := createTools(deps)

	eng := engine.New(&engine.Params{
		Provider:          provider,
		ToolsProvider:     mainRefs.reg.ToolMapFn(),
		Model:             model,
		MaxTokens:         maxTokens,
		TokenBudget:       contextWindow,
		Logger:            logger,
		Dispatcher:        h,
		MCPRegistry:       mcpRegistry,
		Hooks:             hookSystem,
		PermissionChecker: permCheckerIface,
		WorkingDir:        workingDir,
	})

	eng.SetOnClose(func(sessionID string) {
		mainRefs.repl.CleanSession(sessionID)
	})

	wireEngine(eng, mainRefs, deps)

	// 5. Build system prompt using context builder
	systemPrompt := buildSystemPrompt(workingDir, mainRefs.reg, skillReg, contextWindow)

	// Store system prompt on engine for fork agent access
	eng.SetSystemPrompt(systemPrompt)
	// 6. Initialize short-term memory store
	configDir, _ = config.ConfigDir()
	var store *short.Store
	if configDir != "" {
		dbPath := filepath.Join(configDir, "memory", "short-term.db")
		s, err := short.NewStore(dbPath)
		if err != nil {
			slog.Warn("main: failed to open short-term store, persistence disabled", "error", err)
		} else {
			store = s
		}
	}

	// 7. Auto-resume: restore last session or create new
	var sessionID string
	if store != nil {
		eng.SetStore(store, workingDir)
		id, err := eng.ResumeOrInitSession(workingDir, model)
		if err != nil {
			slog.Warn("main: session init failed", "error", err)
		} else {
			sessionID = id
			if err := tui.WriteWorkspaceMeta(workingDir, sessionID); err != nil {
				slog.Warn("main: write workspace meta failed", "error", err)
			}
		}
	}

		// Initialize task list storage
		if sessionID != "" {
			if dir, err := tasklist.TasksDir(sessionID); err == nil {
				if err := taskList.SetDir(dir); err != nil {
					slog.Warn("main: tasks init failed", "error", err)
				}
			} else {
				slog.Warn("main: tasks dir resolve failed", "error", err)
			}
		}

		// Wire auto-compact
		if store != nil && sessionID != "" {
			compactor := engine.NewAutoCompactor(store, sessionID, model, provider, contextWindow)
			eng.SetCompactor(compactor, engine.AutoCompactConfig{
				ContextWindow:          contextWindow,
				MaxConsecutiveFailures: 3,
			})
		}

		// Wire session memory extraction
		// TS source: services/SessionMemory/sessionMemory.ts
		if store != nil && sessionID != "" && contextWindow > 0 {
			smCfg := session.DefaultConfig()
			extractFn := func(ctx context.Context, prompt string, notesPath string, messages []types.Message, systemPrompt json.RawMessage) error {
				editTool := fileedit.New()
				subEng := eng.NewSubEngine(engine.SubEngineOptions{
					Tools:     map[string]tool.Tool{"Edit": editTool},
					AgentType: "session_memory",
				})
				defer subEng.Close()

				// Build fork-style messages: parent conversation + extraction prompt as user message.
				// TS: runForkedAgent passes forkContextMessages + promptMessages.
				extractionUserMsg := types.Message{
					ID:      uuid.New().String(),
					Role:    types.RoleUser,
					Content: []types.ContentBlock{types.NewTextBlock(prompt)},
				}
				forkMessages := append(slices.Clone(messages), extractionUserMsg)

				// Use parent's system prompt for cache sharing (TS: cacheSafeParams).
				result := subEng.RunForkedQuery(ctx, forkMessages, systemPrompt)
				return result.Error
			}
			sm := session.New(smCfg, workingDir, extractFn, slog.Default())
			sm.SetSystemPromptFn(eng.SystemPrompt)
			eng.SetSessionMemory(sm)
		}

		// Wire dream mode (auto memory consolidation)
		// TS source: services/autoDream/autoDream.ts
		if dream.IsEnabled() && store != nil && sessionID != "" && contextWindow > 0 {
			dreamCfg := dream.DefaultConfig()
			dreamRunFn := func(ctx context.Context, prompt string) error {
				// 5min timeout to prevent runaway dream sub-agents
				ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				dreamTools := map[string]tool.Tool{
					"Read":  fileread.New(),
					"Edit":  fileedit.New(),
					"Write": filewrite.New(),
					"Grep":  grep.New(),
					"Glob":  glob.New(),
				}
				subEng := eng.NewSubEngine(engine.SubEngineOptions{
					Tools:     dreamTools,
					AgentType: "auto_dream",
					MaxTurns:  30,
				})
				defer subEng.Close()
				sysPrompt, _ := json.Marshal(prompt)
				result := subEng.QuerySync(ctx, "", sysPrompt)
				return result.Error
			}

			memoryDir := long.GetMemoryPath(workingDir)
			dreamMgr := dream.NewManager(dreamCfg, memoryDir, workingDir, sessionID,
				store, dreamRunFn, eng.Dispatcher(), slog.Default())
			eng.RegisterPostTurnHook(dreamMgr.RunPostTurn)
		}

			// Fire SessionStart hook
			if sessionID != "" {
				hookSystem.SessionStart(context.Background(), &hooks.HookInput{
					HookEventName: string(hooks.HookSessionStart),
					SessionID:     sessionID,
					Cwd:           workingDir,
					Source:        "startup",
				})
			}
		// 8. Create TUI App
		app := tui.NewApp(eng, systemPrompt, h)
		app.SetProviders(providerMap, cfg)
		app.SetStore(store, sessionID, workingDir)

		// Estimate initial context usage
		// CJK-aware estimation. Corrected after first API response.
		initialTokens := types.EstimateTokens(string(systemPrompt))
		for _, t := range mainRefs.reg.EnabledTools() {
			if b, err := json.Marshal(t.InputSchema()); err == nil {
				initialTokens += types.EstimateTokens(string(b))
			}
		}
		app.SetInitialContext(initialTokens, contextWindow)

	// Wire task list panel reader
	app.SetAutoCleanupFn(func() bool {
		// Clean up terminal jobs from bash and fork agent registries.
		mainRefs.jobReg.CleanupCompleted()
		// Clean up completed tasks if 5s has elapsed (or session resume).
		if taskList.ShouldCleanupCompleted(5 * time.Second) {
			_ = taskList.CleanupCompleted()
			return true
		}
		return false
	})
	app.SetTaskListFn(func() []tui.TaskSummary {
		if taskList.Dir() == "" {
			return nil
		}
		allTasks, err := taskList.ListTasks()
		if err != nil {
			return nil
		}
		// Build ID→subject lookup and completed-ID set.
		completedIDs := make(map[string]bool)
		subjectByID := make(map[string]string)
		for _, t := range allTasks {
			if t.Status == tasklist.StatusCompleted {
				completedIDs[t.ID] = true
			}
			subjectByID[t.ID] = t.Subject
		}

		var result []tui.TaskSummary
		for _, t := range allTasks {
			if t.Metadata != nil && t.Metadata["_internal"] != nil {
				continue
			}
			// Filter blockedBy to only uncompleted, resolve to subjects.
			activeBlockedBy := make([]string, 0, len(t.BlockedBy))
			for _, id := range t.BlockedBy {
				if !completedIDs[id] {
					activeBlockedBy = append(activeBlockedBy, subjectByID[id])
				}
			}
			result = append(result, tui.TaskSummary{
				ID:        t.ID,
				Subject:   t.Subject,
				Status:    string(t.Status),
				Owner:     t.Owner,
				BlockedBy: activeBlockedBy,
			})
		}
		return result
	})
	app.SetKillAllFn(func() {
		for _, t := range mainRefs.jobReg.List() {
			if t.Status == "running" {
				_ = mainRefs.jobReg.Kill(t.ID)
			}
		}
	})

	// 9. Run bubbletea program
	p := tea.NewProgram(app, tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		eng.Close()
		os.Exit(1)
	}

	// Clean shutdown: close REPL sessions and MCP connections
	mainRefs.repl.Close()
	eng.Close()
}

// ProviderMap maps provider names to their llm.Provider instances.
type ProviderMap map[string]llm.Provider

// createAllProviders creates llm.Provider instances for all configured providers.
// Providers without a TierPro model are skipped with a warning.
func createAllProviders(cfg *config.Config) ProviderMap {
	m := make(ProviderMap)
	_, tier, err := cfg.ParseModel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	for _, p := range cfg.Providers {
		apiKey := p.ResolveKey()
		if apiKey == "" {
			continue
		}
		// Validate: at least TierPro must have a model
		if p.Models[config.TierPro] == "" {
			fmt.Fprintf(os.Stderr, "warning: provider %q has no pro model defined, skipping\n", p.Name)
			continue
		}
		model := p.Models[tier]
		if model == "" {
			model = p.Models[config.TierPro]
		}
		switch p.ProviderType() {
		case config.ProviderTypeOpenAI:
			m[p.Name] = llm.NewOpenAIProvider(&llm.OpenAIConfig{
				APIKey:  apiKey,
				BaseURL: p.URL,
				Model:   model,
			})
		default: // anthropic
			url := p.URL
			if url == "" {
				url = "https://api.anthropic.com"
			}
			m[p.Name] = llm.NewAnthropicProvider(&llm.AnthropicConfig{
				APIKey:  apiKey,
				BaseURL: url,
				Model:   model,
			})
		}
	}
	return m
}

// loadConfig reads configuration from gbot's own settings files and env vars.
func loadConfig() (*config.Config, error) {
	return config.Load()
}

// toolDeps holds shared dependencies that don't vary per engine.
type toolDeps struct {
	workingDir string
	gitStatus  *ctxbuild.GitStatusInfo
	skillReg   *skills.Registry
	taskList   *tasklist.List
	mcpReg     *mcp.Registry
	hooks      *hooks.Hooks
}

// toolRefs holds one engine's independent tool instances.
type toolRefs struct {
	reg     *tool.Registry
	bashReg *bash.BackgroundJobRegistry
	agent   *agenttool.AgentTool
	repl    *repl.REPLTool
	jobReg  *job.MultiRegistry
}

// createTools creates a fresh, complete set of tool instances for one engine.
// Notification wiring (OnNotify, SetFactory) is done separately by wireEngine.
func createTools(deps toolDeps) toolRefs {
	bashReg := bash.NewBackgroundJobRegistry()

	reg := tool.NewRegistry()
	reg.MustRegister(bash.New(bashReg))
	reg.MustRegister(fileread.New())
	reg.MustRegister(fileedit.New())
	reg.MustRegister(filewrite.New())
	reg.MustRegister(glob.New())
	reg.MustRegister(grep.New())

	at := agenttool.New()
	at.SetWorkingDir(deps.workingDir)
	at.SetGitStatus(deps.gitStatus)
	at.SetSkillRegistry(deps.skillReg)
	// Stub SetNotifyFn — must be called before JobAdapter() so forkReg is initialized.
	at.SetNotifyFn(func(string) {}, func() json.RawMessage { return nil })
	reg.MustRegister(at)

	jobReg := job.NewMultiRegistry(bash.NewJobInfoAdapter(bashReg), at.JobAdapter())
	reg.MustRegister(job.NewJobOutput(jobReg))
	reg.MustRegister(job.NewJobStop(jobReg))

	reg.MustRegister(tasklist.NewTaskCreate(deps.taskList))
	reg.MustRegister(tasklist.NewTaskGet(deps.taskList))
	reg.MustRegister(tasklist.NewTaskList(deps.taskList))
	reg.MustRegister(tasklist.NewTaskUpdate(deps.taskList))

	reg.MustRegister(skilltool.New(deps.skillReg))

	replTool := repl.New()
	reg.MustRegister(replTool)

	return toolRefs{reg: reg, bashReg: bashReg, agent: at, repl: replTool, jobReg: jobReg}
}

// wireEngine recursively wires notification callbacks and the agent factory
// for an engine and its tool set. Each sub-engine created through the factory
// gets its own fresh tool instances via createTools.
func wireEngine(eng *engine.Engine, refs toolRefs, deps toolDeps) {
	// 1. Bash background job notifications → this engine.
	refs.bashReg.OnNotify = func(n bash.JobNotification) {
		eng.EnqueueAttachment(types.QueuedItem{
			Value:     n.FormatXML(),
			Mode:      types.ItemModeJob,
			Timestamp: time.Now(),
		})
	}

	// 2. Fork agent notifications → this engine (overrides stub from createTools).
	refs.agent.SetNotifyFn(
		func(xml string) {
			eng.EnqueueAttachment(types.QueuedItem{
				Value:     xml,
				Mode:      types.ItemModeJob,
				Timestamp: time.Now(),
			})
		},
		func() json.RawMessage { return eng.SystemPrompt() },
	)

	// 3. Agent factory — creates fresh tool set per sub-engine (recursive).
	refs.agent.SetFactory(
		func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
			startTime := time.Now()
			subRefs := createTools(deps)

			// Merge MCP tools into sub-registry (equivalent to main engine's refreshTools).
			if deps.mcpReg != nil {
				for _, dt := range deps.mcpReg.GetTools() {
					subRefs.reg.MustRegister(engine.NewMCPTool(dt, deps.mcpReg))
				}
			}

			// agent.go's ResolveAgentTools already decided which tool names are allowed.
			// Map those names to fresh instances from the new registry.
			subTools := subRefs.reg.ToolMap()
			if len(opts.Tools) > 0 {
				filtered := make(map[string]tool.Tool, len(opts.Tools))
				for name := range opts.Tools {
					if t, ok := subTools[name]; ok {
						filtered[name] = t
					}
				}
				subTools = filtered
			}

			subEng := eng.NewSubEngine(engine.SubEngineOptions{
				Tools:           subTools,
				SystemPrompt:    string(opts.SystemPrompt),
				MaxTurns:        opts.MaxTurns,
				Model:           opts.Model,
				ParentToolUseID: opts.ParentToolUseID,
				AgentType:       opts.AgentType,
			})

			wireEngine(subEng, subRefs, deps)

			// Fire SubagentStart hook — collect additional context from plugins.
			hookInput := &hooks.HookInput{
				HookEventName: "SubagentStart",
				AgentID:       subEng.SessionID(),
				AgentType:     opts.AgentType,
			}
			for _, r := range deps.hooks.SubagentStart(ctx, hookInput) {
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
			var result engine.QueryResult
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
		refs.reg.ToolMap,
	)

	// 4. MCP connect — agent-specific MCP servers.
	if deps.mcpReg != nil {
		refs.agent.SetMcpConnect(func(ctx context.Context, agentID string, rawSpecs []json.RawMessage) (*agenttool.McpConnectResult, error) {
			handle, err := deps.mcpReg.ConnectAgentServers(ctx, agentID, rawSpecs)
			if err != nil || handle == nil {
				return nil, err
			}
			mcpTools := make(map[string]tool.Tool)
			for name, dt := range handle.Tools() {
				mcpTools[name] = engine.NewMCPTool(dt, deps.mcpReg)
			}
			return &agenttool.McpConnectResult{
				Tools:   mcpTools,
				Cleanup: handle.Cleanup,
			}, nil
		})
	}

	// 5. REPL tool executor → this engine.
	{
		var replAskMu sync.Mutex
		replSessionAllowed := make(map[string]bool)
		refs.repl.SetToolExecutor(func(toolCtx context.Context, name string, args json.RawMessage) (string, error) {
			return eng.ExecuteTool(toolCtx, name, args, replSessionAllowed, &replAskMu)
		})
	}
}

// buildSystemPrompt builds the system prompt using the context builder.
func buildSystemPrompt(workingDir string, reg *tool.Registry, skillReg *skills.Registry, contextWindow int) json.RawMessage {
	builder := ctxbuild.NewBuilder(workingDir)

	// Load git status
	builder.GitStatus = ctxbuild.LoadGitStatus(workingDir)

	// Load memory files
	builder.MemoryFiles = ctxbuild.LoadMemoryFiles(workingDir)

	// Collect tool prompt contributions
	for _, t := range reg.EnabledTools() {
		if p := t.Prompt(); p != "" {
			builder.ToolPrompts = append(builder.ToolPrompts, p)
		}
	}

	// Build skill listing within context window budget
	builder.SkillListing = skilltool.BuildSkillListing(skillReg.GetSkillToolSkills(), contextWindow)

	prompt, err := builder.Build()
	if err != nil {
		// Fallback to a minimal system prompt
		fallback := json.RawMessage(`"You are gbot, an interactive AI coding assistant. Use tools to accomplish tasks."`)
		return fallback
	}
	return prompt
}
