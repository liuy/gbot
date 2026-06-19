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
	"net"
	"net/http"
	_ "net/http/pprof"

	"github.com/google/uuid"
	"os"
	"path/filepath"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/dream"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/plugins"
	skills "github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/tool/grep"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tui"
	"github.com/liuy/gbot/pkg/types"
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

	// pprof HTTP server: exposes /debug/pprof/* for runtime introspection
	// (heap, goroutine, CPU profiles). Always-on; bound to localhost only.
	// Override address with GBOT_PPROF_ADDR; set to "" to disable.
	startPprofServer(cfg.PprofAddr)

	// 2. Create LLM providers from config
	providerMap, err := config.CreateAllProviders(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(providerMap) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no API key configured.")
		fmt.Fprintln(os.Stderr, "Set providers[].keys in ~/.gbot/settings.json")
		os.Exit(1)
	}

	// Primary provider resolved from Config.Model
	provider, model, primaryProviderCfg, err := resolvePrimaryProvider(cfg, providerMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
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
	loadedPlugins, pluginErr := plugins.LoadAndInitialize(context.Background(), workingDir, cfg)
	if pluginErr != nil {
		slog.Warn("main: plugin loading failed", "error", pluginErr)
	}

	taskList := task.NewList("")

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

	contextWindow := primaryProviderCfg.ResolveContext(model)
	maxTokens := primaryProviderCfg.ResolveMaxTokens(model)

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
	}

	// Register ALL skills (user + plugin) — collected here for TUI dispatch
	// after App is constructed (RegisterSlashCommands needs *App).
	skillCmdsForTUI := skillReg.GetSkillToolSkills()

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

	// Construct LSP registry — InitFromPATH is instant (LookPath only),
	// then Start spawns+validates in background for warmup.
	lspReg := lsp.NewRegistry(workingDir)
	lspReg.Scan(lsp.DefaultServers)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		lspReg.Start(ctx, lsp.DefaultServers)
		slog.Info("lsp:startup", "servers", lspReg.LSPString())
	}()

	// Construct shared dependencies (immutable across all engines)
	gitStatus := ctxbuild.LoadGitStatus(workingDir)
	deps := engine.SharedDeps{
		WorkingDir: workingDir,
		GitStatus:  gitStatus,
		SkillReg:   skillReg,
		TaskList:   taskList,
		McpReg:     mcpRegistry,
		Hooks:      hookSystem,
		Cfg:        cfg,
		LSPReg:     lspReg,
	}

	// Create per-engine tool instances for the main engine
	mainRefs := engine.CreateTools(deps)

	// Collect per-model Anthropic `thinking` overrides from every provider's
	// Models config. Models not present in any provider omit the field entirely.
	modelThinking := map[string]llm.ThinkingMode{}
	for i := range cfg.Providers {
		for _, name := range cfg.Providers[i].Models.Ordered() {
			mc, _ := cfg.Providers[i].Models.Get(name)
			if mc.Thinking != "" {
				modelThinking[name] = mc.Thinking
			}
		}
	}

	eng := engine.New(&engine.Params{
		Provider:          provider,
		ToolsProvider:     mainRefs.Reg.ToolMapFn(),
		Model:             model,
		MaxTokens:         maxTokens,
		TokenBudget:       contextWindow,
		Logger:            logger,
		Dispatcher:        h,
		MCPRegistry:       mcpRegistry,
		Hooks:             hookSystem,
		PermissionChecker: permCheckerIface,
		WorkingDir:        workingDir,
		TaskList:          taskList,
		ModelThinking:     modelThinking,
	})

	eng.SetOnClose(func(sessionID string) {
		mainRefs.REPL.CleanSession(sessionID)
	})

	engine.WireEngine(eng, mainRefs, deps)

	// 5. Build system prompt using context builder
	skillListing := skilltool.BuildSkillListing(skillReg.GetSkillToolSkills(), contextWindow)
	var toolPrompts []string
	for _, t := range mainRefs.Reg.EnabledTools() {
		if p := t.Prompt(); p != "" {
			toolPrompts = append(toolPrompts, p)
		}
	}
	systemPrompt := ctxbuild.BuildSystemPrompt(workingDir, toolPrompts, skillListing, lspReg)

	// Store system prompt on engine for fork agent access
	eng.SetSystemPrompt(systemPrompt)
	eng.SetSkillListing(skillListing)
	eng.SetAgentDefs(agenttool.ListAgentDefinitions())
	eng.SetSharedDeps(&deps)
	eng.SetSkillRegistry(skillReg)
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
		if dir, err := task.TasksDir(sessionID); err == nil {
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
		extractFn := func(ctx context.Context, prompt string, notesPath string, messages []types.Message, systemPrompt string) error {
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
			}
			subEng := eng.NewSubEngine(engine.SubEngineOptions{
				Tools:     dreamTools,
				AgentType: "auto_dream",
				MaxTurns:  30,
			})
			defer subEng.Close()
			result := subEng.QuerySync(ctx, "", prompt)
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
	if len(skillCmdsForTUI) > 0 {
		slashCmds := make(map[string]tui.CommandDef, len(skillCmdsForTUI))
		for _, sc := range skillCmdsForTUI {
			slashCmds[sc.Name] = tui.CommandDef{
				Description: sc.Description,
				HasArgs:     true,
			}
		}
		app.RegisterSkillCommands(slashCmds)
	}
	app.SetStore(store, sessionID, workingDir)

	// Estimate initial context usage
	// CJK-aware estimation. Corrected after first API response.
	initialTokens := types.EstimateTokens(systemPrompt)
	for _, t := range mainRefs.Reg.EnabledTools() {
		if b, err := json.Marshal(t.InputSchema()); err == nil {
			initialTokens += types.EstimateTokens(string(b))
		}
	}
	app.SetInitialContext(initialTokens, contextWindow)

	// Wire task list panel reader
	app.SetAutoCleanupFn(func() bool {
		// Clean up terminal jobs from bash and fork agent registries.
		mainRefs.JobReg.CleanupCompleted()
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
			if t.Status == task.StatusCompleted {
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
		for _, t := range mainRefs.JobReg.List() {
			if t.Status == "running" {
				_ = mainRefs.JobReg.Kill(t.ID)
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
	mainRefs.REPL.Close()
	eng.Close()
}

// loadConfig reads configuration from gbot's own settings files and env vars.
func loadConfig() (*config.Config, error) {
	return config.Load()
}

// startPprofServer starts a localhost-bound pprof HTTP server.
// Priority: GBOT_PPROF_ADDR env > cfg.PprofAddr > "localhost:6060" default.
// "off" or "-" disables the server. Logs the listen address (or skip notice).
func startPprofServer(cfgAddr string) {
	addr := cfgAddr
	if env := os.Getenv("GBOT_PPROF_ADDR"); env != "" {
		addr = env
	}
	if addr == "" {
		addr = "localhost:6060"
	}
	if addr == "off" || addr == "-" {
		slog.Info("pprof:disabled")
		return
	}
	// net/http/pprof registers handlers on DefaultServeMux at import time.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("pprof:listen_failed", "addr", addr, "error", err)
		return
	}
	go func() {
		slog.Info("pprof:listen", "addr", ln.Addr().String())
		if err := http.Serve(ln, http.DefaultServeMux); err != nil {
			slog.Warn("pprof:server_failed", "error", err)
		}
	}()
}

// resolvePrimaryProvider resolves Config.Model into a concrete provider, model name,
// and Provider config using the new model resolution logic.
func resolvePrimaryProvider(cfg *config.Config, providerMap config.ProviderMap) (llm.Provider, string, *config.Provider, error) {
	p, modelName, err := cfg.ResolveModel()
	if err != nil {
		return nil, "", nil, err
	}
	if p == nil {
		return nil, "", nil, fmt.Errorf("no providers configured")
	}

	prov, ok := providerMap[p.Name]
	if !ok {
		return nil, "", nil, fmt.Errorf("provider %q has no API key configured", p.Name)
	}
	return prov, modelName, p, nil
}
