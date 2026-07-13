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
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/connector/wui"
	"github.com/liuy/gbot/pkg/connector/wechat"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/dream"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/project"
	skills "github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/computer"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/tool/grep"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tui"
	"github.com/liuy/gbot/pkg/types"
)

func main() {
	var mediaStores []*media.Store

	// Parse -d/--daemon and -p/--port flags before anything else.
	var daemonMode bool
	wsPort := "8765"
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--daemon":
			daemonMode = true
		case "-p", "--port":
			if i+1 < len(args) {
				wsPort = args[i+1]
				i++
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	workingDir, _ := os.Getwd()
	projectDir := project.Dir(workingDir)
	if daemonMode {
		// Chdir to a fixed location so daemon state (PID, memory, session)
		// is isolated from project-specific gbot instances.
		daemonDir := filepath.Join(home, ".gbot", "daemon")
		_ = os.MkdirAll(daemonDir, 0755)
		if err := os.Chdir(daemonDir); err == nil {
			workingDir = daemonDir
			projectDir = project.Dir(workingDir)
		}
	}

	// PID-based single-instance guard
	pidCleanup, err := acquirePID(projectDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer pidCleanup()

	// Clean up PID file on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		pidCleanup()
		os.Exit(0)
	}()

	// Debug logging: write info-level events to log file.
	// This provides comprehensive observability for diagnosing token stats,
	// event ordering, and rendering issues.
	logPath := filepath.Join(projectDir, "gbot.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	// WeChat login subcommand: `gbot wechat login` or `gbot -d wechat login`.
	// The -d flag (parsed above) determines which project the login binds to.
	loginIdx := slices.IndexFunc(os.Args[1:], func(a string) bool { return a == "wechat" })
	loginOk := loginIdx >= 0 && loginIdx+1 < len(os.Args[1:]) && os.Args[1:][loginIdx+1] == "login"
	if loginOk {
		cfg, err := loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
		client := cfg.ProxyHTTPClient()
		accountID, err := wechat.Login(context.Background(), client, projectDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WeChat login failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("WeChat login successful. Account: %s\n", accountID)
		os.Exit(0)
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

	// Daemon mode: start the inbound WebSocket listener before any engine
	// is built so an inbound device conn can register before the model calls
	// connect. TUI mode leaves wsRegistry nil — the Computer tool then
	// surfaces "daemon not running" on connect, same inert behavior as today.
	// wsMux is hoisted to the outer scope so the wui connector can mount
	// its routes on the same mux later (after app/engine exist). The HTTP
	// server starts listening immediately; chat routes are added in Step B.
	// WebSocket server: start when daemon mode (-d) OR when an explicit
	// port is given (-p). The -p flag alone starts WS without daemon mode,
	// useful for running TUI + wui on the same machine.
	needWS := daemonMode || wsPort != "8765" || os.Getenv("GBOT_WS_ADDR") != ""
	var wsRegistry *computer.ConnectionRegistry
	var wsMux *http.ServeMux
	if needWS {
		wsRegistry = computer.NewConnectionRegistry()
		wsAddr := ":" + wsPort
		if env := os.Getenv("GBOT_WS_ADDR"); env != "" {
			wsAddr = env
		}
		wsMux = http.NewServeMux()
		if _, err := computer.StartWSServer(wsRegistry, wsAddr, wsMux); err != nil {
			fmt.Fprintf(os.Stderr, "ws server: %v\n", err)
			os.Exit(1)
		}
		slog.Info("ws:listen", "addr", wsAddr)
	}

	deps := engine.SharedDeps{
		WorkingDir: workingDir,
		GitStatus:  gitStatus,
		SkillReg:   skillReg,
		McpReg:     mcpRegistry,
		Hooks:      hookSystem,
		Cfg:        cfg,
		LSPReg:     lspReg,
		WSRegistry: wsRegistry,
	}

	// mainRefs is only used for tool enumeration and closures (JobReg, REPL),
	// not for a real engine. Give it its own throwaway list so CreateTools
	// succeeds and the Task tool schema is included in system-prompt token
	// estimation.
	mainTaskList := task.NewList("")
	mainRefs := engine.CreateTools(deps, mainTaskList)

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

	// 5. Build system prompt using context builder
	skillListing := skilltool.BuildSkillListing(skillReg.GetSkillToolSkills(), contextWindow)
	var toolPrompts []string
	for _, t := range mainRefs.Reg.EnabledTools() {
		if p := t.Prompt(); p != "" {
			toolPrompts = append(toolPrompts, p)
		}
	}
	systemPrompt := ctxbuild.BuildSystemPrompt(workingDir, projectDir, toolPrompts, skillListing, lspReg, "")

	// 6. Initialize short-term memory store
	var store *short.Store
	{
		dbPath := filepath.Join(projectDir, "memory", "memory.db")
		s, err := short.NewStore(dbPath)
		if err != nil {
			slog.Warn("main: failed to open short-term store, persistence disabled", "error", err)
		} else {
			store = s
		}
	}

	// 7. Engine factory: builds a fresh engine with its own Hub + TUIHandler.
	// Used both for /engine new (runtime) and for bootstrap restoration
	// (every engine in meta.json is rebuilt via this path on startup, so
	// main is no longer special — it's just whichever engine meta marks
	// active).
	engineFactory := func(id, name, providerName, modelArg string) (*engine.Engine, *tui.TUIHandler, error) {
		engineProvider := provider
		if providerName != "" {
			if p, found := providerMap[providerName]; found {
				engineProvider = p
			} else {
				slog.Warn("restore: provider not found, falling back to default",
					"provider", providerName, "model", modelArg, "engine_id", id)
			}
		} else {
			slog.Warn("restore: no provider prefix in model, falling back to default",
				"model", modelArg, "engine_id", id)
		}
		engineHub, handler := tui.NewEngineHubWithHandler(id, nil)
		engTaskList := task.NewList("")
		refs := engine.CreateTools(deps, engTaskList)
		// Resolve the provider config and recompute context window.
		// The outer contextWindow/maxTokens are from the primary provider
		// and may be wrong after /model switch.
		var providerCfg *config.Provider
		if providerName == "" {
			providerCfg = primaryProviderCfg
		} else {
			for i := range cfg.Providers {
				if cfg.Providers[i].Name == providerName {
					providerCfg = &cfg.Providers[i]
					break
				}
			}
			if providerCfg == nil {
				providerCfg = primaryProviderCfg
			}
		}
		engCtxWindow := providerCfg.ResolveContext(modelArg)
		engMaxTokens := providerCfg.ResolveMaxTokens(modelArg)
		engInputs := providerCfg.ResolveInput(modelArg)
		newEng := engine.New(&engine.Params{
			Provider:      engineProvider,
			ToolsProvider: refs.Reg.ToolMapFn(),
			Model:         modelArg,
			MaxTokens:     engMaxTokens,
			MaxTurns:      0, // unlimited
			TokenBudget:   engCtxWindow,
			AutoCompact: engine.AutoCompactConfig{
				ContextWindow:          engCtxWindow,
				MaxConsecutiveFailures: 3,
			},
			Compactor:         nil, // set via SetCompactor below when store+session exist
			Logger:            logger,
			Dispatcher:        engineHub,
			MCPRegistry:       mcpRegistry,
			Hooks:             hookSystem,
			PermissionChecker: permCheckerIface,
			WorkingDir:        workingDir,
			TaskList:          engTaskList,
			ModelThinking:     modelThinking,
			EngineID:          id,
			InputModalities:   engInputs,
		})
		newEng.SetOnClose(func(sessionID string) {
			refs.REPL.CleanSession(sessionID)
		})
		engine.WireEngine(newEng, refs, deps)
		newEng.SetSystemPrompt(systemPrompt)
		newEng.SetSkillListing(skillListing)
		newEng.SetAgentDefs(agenttool.ListAgentDefinitions())
		newEng.SetSharedDeps(&deps)
		newEng.SetSkillRegistry(skillReg)

		// Per-engine wiring: autocompactor, session memory, dream mode.
		// Each engine gets its own because they reference newEng.Dispatcher()
		// and newEng.NewSubEngine — closures capture newEng, not a shared ptr.
		// SM and dream wires here unconditionally; model/provider are read
		// live from engine at extract time (NewSubEngine copies parent state).
		// Dream needs SessionID() for NewManager — empty on fresh engines,
		// filled after SwitchSession on restore.
		if !cfg.SessionNotes.Disabled && store != nil && contextWindow > 0 {
			smCfg := session.DefaultConfig()
			smExtractFn := func(ctx context.Context, prompt string, notesPath string, messages []types.Message, sysPrompt string) error {
				editTool := fileedit.New()
				readTool := fileread.New()
				subEng := newEng.NewSubEngine(engine.SubEngineOptions{
					SystemPrompt:    sysPrompt,
					Tools:           map[string]tool.Tool{"Edit": editTool, "Read": readTool},
					MaxTurns:        0, // unlimited
					Model:           "",
					ParentToolUseID: "",
					AgentType:       "session_memory",
				})
				defer subEng.Close()

				// Resolve session notes model: "provider/model" or "model" (fuzzy).
				// Empty = inherit parent engine's provider+model.
				if smName := cfg.SessionNotes.Model; smName != "" {
					if smProv, smModel, err := cfg.ResolveModelByName(smName); err != nil {
						slog.Warn("session memory: resolve model failed, inheriting parent", "model", smName, "error", err)
					} else if smProv != nil {
						if prov, ok := providerMap[smProv.Name]; ok {
							subEng.SetProvider(prov)
							subEng.SetModel(smModel)
						} else {
							slog.Warn("session memory: provider not in providerMap, inheriting parent", "provider", smProv.Name)
						}
					}
				}
				extractionUserMsg := types.Message{
					ID:      uuid.New().String(),
					Role:    types.RoleUser,
					Content: []types.ContentBlock{types.NewTextBlock(prompt)},
				}
				forkMessages := append(slices.Clone(messages), extractionUserMsg)
				result := subEng.RunForkedQuery(ctx, forkMessages, sysPrompt)
				if err := session.SanitizeNotes(notesPath); err != nil {
					slog.Warn("session memory: sanitize failed", "error", err)
				}
				return result.Error
			}
			sm := session.New(smCfg, projectDir, id, smExtractFn, slog.Default())
			sm.SetSystemPromptFn(newEng.SystemPrompt)
			newEng.SetSessionMemory(sm)
			slog.Info("session memory: wired", "engine_id", id)
		}

		if dream.IsEnabled() && store != nil && contextWindow > 0 {
			dreamCfg := dream.DefaultConfig()
			dreamRunFn := func(ctx context.Context, prompt string) error {
				ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				dreamTools := map[string]tool.Tool{
					"Read":  fileread.New(),
					"Edit":  fileedit.New(),
					"Write": filewrite.New(),
					"Grep":  grep.New(),
					"Glob":  glob.New(),
				}
				subEng := newEng.NewSubEngine(engine.SubEngineOptions{
					SystemPrompt:    "",
					Tools:           dreamTools,
					Model:           "", // inherit from parent
					ParentToolUseID: "",
					AgentType:       "auto_dream",
					MaxTurns:        30,
				})
				defer subEng.Close()
				result := subEng.QuerySync(ctx, "", prompt)
				return result.Error
			}
			memoryDir := long.GetMemoryPath(workingDir)
			dreamMgr := dream.NewManager(dreamCfg, memoryDir, projectDir, newEng,
				store, dreamRunFn, newEng.Dispatcher(), slog.Default())
			newEng.RegisterPostTurnHook(dreamMgr.RunPostTurn)
			slog.Info("dream: wired", "engine_id", id)
		}

		newEng.SetToolRefs(refs)
		return newEng, handler, nil
	}

	// 8. Restore engines from meta.json. On first run (no meta), synthesize
	// a single main engine. Every engine — including main — goes through
	// engineFactory, so all engines have identical wiring.
	engineMgr := engine.NewEngineManager()
	var sessionID string
	if store != nil {
		sessionID = restoreEngines(restoreEnginesDeps{
			mgr:        engineMgr,
			factory:    engineFactory,
			store:      store,
			projectDir: projectDir,
			model:      model,
		})
	}

	states, _ := wechat.LoadAllStates(projectDir)
	for _, state := range states {
		if err := startWeChatConnector(startWeChatDeps{
			state:              state,
			engineMgr:          engineMgr,
			provider:           provider,
			model:              model,
			maxTokens:          maxTokens,
			contextWindow:      contextWindow,
			logger:             logger,
			mcpRegistry:        mcpRegistry,
			hookSystem:         hookSystem,
			modelThinking:      modelThinking,
			workingDir:         workingDir,
			projectDir:         projectDir,
			store:              store,
			deps:               deps,
			primaryProviderCfg: primaryProviderCfg,
			mediaStores:        &mediaStores,
			daemonMode:         daemonMode,
			toolPrompts:        toolPrompts,
			skillListing:       skillListing,
			lspReg:             lspReg,
		}); err != nil {
			slog.Warn("wechat: start connector failed", "account_id", state.AccountID, "error", err)
			continue
		}
	}
	if len(states) > 0 {
		if err := engineMgr.PersistMeta(projectDir); err != nil {
			slog.Warn("wechat: persist meta failed", "error", err)
		}
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
	app := tui.NewAppWithManager(engineMgr, systemPrompt, h)
	app.SetProviders(providerMap, cfg)
	if daemonMode {
		app.SetDisableFileHistory(true)
	}
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
	app.SetStore(store, sessionID, projectDir)
	app.SetEngineFactory(engineFactory)

	// Webchat wiring: the connector owns the EngineManager and subscribes to
	// every engine's hub. engine_new is wired via a closure that captures
	// engineFactory + engineMgr + store. Routes are added to the same
	// *http.ServeMux the already-running *http.Server uses (Go's ServeMux is
	// safe for concurrent register+read).
	if needWS && wsMux != nil {
		wc := wui.New(engineMgr, providerMap, buildProviderConfigMap(cfg))
		wc.SetCreateEngineFn(func(name string) (string, error) {
			currentProvider, currentModel := "", model
			if vs := engineMgr.Active(); vs != nil {
				if vs.Engine != nil && vs.Engine.Provider() != nil {
					currentProvider = vs.Engine.Provider().Name()
				}
				if vs.Engine != nil && vs.Engine.Model() != "" {
					currentModel = vs.Engine.Model()
				}
			}
			return createEngineForWebchat(name, engineMgr, engineFactory, store, projectDir, wc, currentProvider, currentModel)
		})
		wui.RegisterStaticRoutes(wsMux)
		wui.RegisterChatWS(wsMux, wc)
		slog.Info("wui: mounted on ws mux", "engines", engineMgr.Count())
	}

	// Estimate initial context usage
	initialTokens := types.EstimateTokens(systemPrompt)
	for _, t := range mainRefs.Reg.EnabledTools() {
		if b, err := json.Marshal(t.InputSchema()); err == nil {
			initialTokens += types.EstimateTokens(string(b))
		}
	}
	if ct := app.Engine().GetContextTokens(); ct > 0 {
		initialTokens = ct
	} else {
		initialTokens += engine.EstimateMessagesTokensForProvider(app.Engine().Messages(), app.CurrentProvider())
	}
	app.SetContextUsed(initialTokens)

	// Wire task list panel reader — closures read from the active engine
	// dynamically so switching engines updates the panel immediately.
	app.SetAutoCleanupFn(func() bool {
		// Clean up terminal jobs from bash and fork agent registries.
		mainRefs.JobReg.CleanupCompleted()
		if a := app.ActiveEngine(); a != nil {
			if tl := a.TaskList(); tl != nil && tl.ShouldCleanupCompleted(5*time.Second) {
				_ = tl.CleanupCompleted()
				return true
			}
		}
		return false
	})
	app.SetTaskListFn(func() []tui.TaskSummary {
		a := app.ActiveEngine()
		if a == nil {
			return nil
		}
		tl := a.TaskList()
		if tl == nil || tl.Dir() == "" {
			return nil
		}
		allTasks, err := tl.ListTasks()
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
		for _, vs := range engineMgr.List() {
			if vs.Engine != nil {
				vs.Engine.Close()
			}
		}
		os.Exit(1)
	}

	// Clean shutdown: close REPL sessions, all engines (including lazily-built
	// background ones), and MCP connections. Iterate the manager so any engine
	// created via /engine new during the session is also torn down.
	mainRefs.REPL.Close()
	for _, vs := range engineMgr.List() {
		if vs.Engine != nil {
			vs.Engine.Close()
		}
	}
	for _, ms := range mediaStores {
		if ms != nil {
			ms.Close()
		}
	}
}

// startWeChatDeps bundles the shared dependencies captured from main() plus the
// per-account state for one WeChat connector. Explicit struct beats a long
// parameter list and lets the caller stay a readable loop.
type startWeChatDeps struct {
	state              *wechat.State
	engineMgr          *engine.EngineManager
	provider           llm.Provider
	model              string
	maxTokens          int
	contextWindow      int
	logger             *slog.Logger
	mcpRegistry        *mcp.Registry
	hookSystem         *hooks.Hooks
	modelThinking      map[string]llm.ThinkingMode
	workingDir         string
	projectDir         string
	store              *short.Store
	deps               engine.SharedDeps
	primaryProviderCfg *config.Provider
	mediaStores        *[]*media.Store
	daemonMode         bool
	toolPrompts        []string
	skillListing       string
	lspReg             *lsp.Registry
}

// startWeChatConnector wires one WeChat account: builds (or adopts a restored)
// engine, subscribes the connector to the engine's hub, and starts the poll
// loop. Returns an error only when the engine cannot be resolved at all;
// softer failures (fresh session, start) are logged and swallowed so one
// account does not block the others.
func startWeChatConnector(d startWeChatDeps) error {
	state := d.state
	engineID := "wechat-" + state.AccountID
	engineName := "WeChat " + state.AccountID

	// The shared Hub the TUI handler and connector both subscribe to.
	// Built once per process for this engine; the handler is stored on
	// the EngineViewState so switchEngine can flip drain roles.
	var wcHub *hub.Hub
	var wcHandler *tui.TUIHandler

	// Check if engine already exists (restored from meta.json)
	if d.engineMgr.Get(engineID) == nil {
		wcTaskList := task.NewList("")
		refs := engine.CreateTools(d.deps, wcTaskList)
		wcHub, wcHandler = tui.NewEngineHubWithHandler(engineID, nil)
		wcEng := engine.New(&engine.Params{
			Provider:          d.provider,
			Model:             d.model,
			InputModalities:   d.primaryProviderCfg.ResolveInput(d.model),
			MaxTokens:         d.maxTokens,
			TokenBudget:       d.contextWindow,
			MaxTurns:          0,
			Logger:            d.logger,
			Compactor:         nil,
			AutoCompact:       engine.AutoCompactConfig{},
			MCPRegistry:       d.mcpRegistry,
			Hooks:             d.hookSystem,
			PermissionChecker: nil,
			WorkingDir:        d.workingDir,
			TaskList:          wcTaskList,
			ModelThinking:     d.modelThinking,
			EngineID:          engineID,
			Dispatcher:        wcHub,
			ToolsProvider:     refs.Reg.ToolMapFn(),
		})
		engine.WireEngine(wcEng, refs, d.deps)
		wcEng.SetToolRefs(refs)
		wcEng.SetStore(d.store, d.projectDir)

		// Create fresh session — ResumeOrInitSession would resume
		// meta.CurrentSessionID which is shared with other engines.
		if err := wcEng.NewSession(d.projectDir, "WeChat"); err != nil {
			slog.Warn("wechat: new session failed", "error", err)
		}

		d.engineMgr.Add(&engine.EngineViewState{
			Engine:          wcEng,
			Repl:            tui.NewReplSnapshot(),
			Handler:         wcHandler,
			ReadOnly:        true,
			History:         nil,
			ID:              engineID,
			Name:            engineName,
			ActiveSessionID: wcEng.SessionID(),
			Model:           d.primaryProviderCfg.Name + "/" + d.model,
			CreatedAt:       time.Now(),
			LastActiveAt:    time.Now(),
		})
	} else {
		// Restored from meta.json via restoreEngines, which builds the
		// engine through engineFactory with ReadOnly: false and never
		// wires the connector's hub. Two things must be restored here:
		//   1. ReadOnly: true — the TUI input must stay disabled because
		//      this engine is driven by the WeChat connector, not the TUI.
		//   2. The connector hub — recovered below from wcEng.Dispatcher()
		//      so the connector and TUI share the same Hub the factory
		//      built (engineFactory sets Dispatcher: engineHub).
		if vs := d.engineMgr.Get(engineID); vs != nil {
			vs.ReadOnly = true
			if h, ok := vs.Handler.(*tui.TUIHandler); ok && h != nil {
				wcHandler = h
			}
		}
		if wcHandler == nil {
			// Defensive: view state has no handler (shouldn't happen since
			// engineFactory always builds one). Build a fresh hub so the
			// connector still works; the TUI just won't render for this
			// engine.
			wcHub = hub.NewHub()
		}
	}

	wcEng := d.engineMgr.Get(engineID).Engine
	if wcEng == nil {
		return fmt.Errorf("engine not found after creation: %s", engineID)
	}
	// The connector needs the same *hub.Hub the engine dispatches to
	// so it sees the engine's events. In the fresh-build branch wcHub
	// is set directly. In the restore branch the hub only exists as
	// the engine's Dispatcher (an interface) — recover it by type
	// assertion so the connector subscribes to the real hub the TUI
	// is already on, instead of a detached hub that sees no events.
	connectorHub := wcHub
	if connectorHub == nil {
		if h, ok := wcEng.Dispatcher().(*hub.Hub); ok && h != nil {
			connectorHub = h
		}
	}
	if connectorHub == nil {
		// Engine's dispatcher is not a *hub.Hub (shouldn't happen —
		// engineFactory always sets a *hub.Hub). Fall back to a fresh
		// hub so the connector doesn't crash; it just won't see TUI.
		slog.Warn("wechat: engine dispatcher is not a *hub.Hub, using isolated hub", "id", engineID)
		connectorHub = hub.NewHub()
	}
	wc := wechat.New(wcEng, connectorHub)
	// Register the WeChat-only Send tool on the engine's mutable registry.
	// Both branches (fresh build + restore) stash their ToolRefs on wcEng, so
	// this single call covers both. The TUI engine never reaches this path.
	wc.RegisterSendTool(wcEng.ToolRefs().Reg)
	if d.daemonMode {
		memDir := filepath.Join(d.projectDir, "memory", engineID)
		wcEng.SetMemoryDir(memDir)
		wcEng.SetSystemPrompt(ctxbuild.BuildSystemPrompt(d.workingDir, d.projectDir, d.toolPrompts, d.skillListing, d.lspReg, memDir))
	}
	// Capture the media cache for shutdown teardown (the cache owns its
	// cleanup goroutine; main must Close() it to stop that goroutine).
	*d.mediaStores = append(*d.mediaStores, wc.MediaCache())
	go func() {
		if err := wc.Start(context.Background(), state, d.projectDir); err != nil {
			slog.Warn("wechat: start failed", "error", err)
		}
	}()
	return nil
}

// restoreEnginesDeps bundles everything restoreEngines needs. Explicit struct
// beats a long parameter list and makes the function signature stable when
// new wiring is added.
type restoreEnginesDeps struct {
	mgr        *engine.EngineManager
	factory    tui.EngineFactoryFn
	store      *short.Store
	projectDir string
	model      string
}

// restoreEngines reads meta.json, migrates legacy format if needed, then
// rebuilds every engine listed there via the factory. Returns the active
// engine's session ID (empty when no store was configured).
//
// First run (no meta) synthesizes a single main engine. Missing sessions
// fall back to a fresh session via ResumeOrInitSession. Every engine —
// including main — goes through the factory, so wiring stays uniform.
func restoreEngines(d restoreEnginesDeps) string {
	meta, err := short.ReadWorkspaceMeta(d.projectDir)
	if err != nil {
		slog.Warn("restore: read workspace meta failed, will synthesize default", "error", err)
	}
	enginesToRestore, activeID := planRestore(meta, d.model)

	for _, em := range enginesToRestore {
		// em.Model is "provider/model" (e.g. "zhipu/glm-5.2"). The engine's
		// own model field needs the bare registration name (e.g. "glm-5.2")
		// — that's what the provider API accepts and what status bar shows.
		// Split on the FIRST "/" only so providers whose model name itself
		// contains a slash (e.g. openrouter's "openrouter/owl-alpha") keep
		// their internal prefix intact.
		engineModel := em.Model
		engineProviderName := ""
		if before, after, ok := strings.Cut(em.Model, "/"); ok {
			engineProviderName = before
			engineModel = after
		}
		eng, handler, err := d.factory(em.ID, em.Name, engineProviderName, engineModel)
		if err != nil {
			slog.Error("restore: build engine failed", "id", em.ID, "error", err)
			continue
		}
		eng.SetStore(d.store, d.projectDir)

		// Resume the engine's last session; fall back to a new session if
		// the recorded one is missing or unresumable (user deleted DB row,
		// partial write, schema drift, etc.).
		resumeID := em.ActiveSessionID
		if resumeID != "" {
			if _, err := eng.SwitchSession(resumeID); err != nil {
				slog.Warn("restore: switch session failed, creating new", "id", em.ID, "error", err)
				resumeID = ""
			}
		}
		if resumeID == "" {
			id, err := eng.ResumeOrInitSession(d.projectDir, engineModel)
			if err != nil {
				slog.Warn("restore: session init failed", "id", em.ID, "error", err)
			} else {
				resumeID = id
			}
		}

		d.mgr.Add(&engine.EngineViewState{
			Engine:          eng,
			Repl:            nil, // set by tui on first switch
			Handler:         handler,
			History:         nil, // set by tui.NewAppWithManager
			ID:              em.ID,
			Name:            em.Name,
			ActiveSessionID: resumeID,
			Model:           em.Model,
			CreatedAt:       time.Now(),
			LastActiveAt:    time.Now(),
			ReadOnly:        false,
		})
	}

	if err := d.mgr.SetActive(activeID); err != nil {
		slog.Warn("restore: set active engine failed", "id", activeID, "error", err)
	}
	if err := d.mgr.PersistMeta(d.projectDir); err != nil {
		slog.Warn("restore: write workspace meta failed", "error", err)
	}
	if vs := d.mgr.Active(); vs != nil {
		return vs.ActiveSessionID
	}
	return ""
}

// createEngineForWebchat builds a new engine via engineFactory, registers it
// in the manager, subscribes the wui connector to its hub, and wires
// OnStreamDone. Returns the new engine ID. Called by the connector's
// engine_new handler via the closure injected by SetCreateEngineFn.
func createEngineForWebchat(
	name string,
	mgr *engine.EngineManager,
	factory tui.EngineFactoryFn,
	store *short.Store,
	projectDir string,
	connector *wui.WUIConnector,
	currentProvider string,
	currentModel string,
) (string, error) {
	if name == "" {
		name = mgr.NewEngineName()
	}
	id := mgr.NewEngineID()
	eng, handler, err := factory(id, name, currentProvider, currentModel)
	if err != nil {
		return "", fmt.Errorf("factory: %w", err)
	}
	if store != nil {
		eng.SetStore(store, projectDir)
	}
	if err := eng.NewSession(projectDir, ""); err != nil {
		eng.Close()
		return "", fmt.Errorf("new session: %w", err)
	}
	vs := &engine.EngineViewState{
		Engine:          eng,
		Handler:         handler,
		ID:              id,
		Name:            name,
		ActiveSessionID: eng.SessionID(),
		Model:           eng.Model(),
		CreatedAt:       time.Now(),
		LastActiveAt:    time.Now(),
		Repl:            nil,
		History:         nil,
		ReadOnly:        false,
	}
	mgr.Add(vs)
	connector.RegisterEngine(vs)
	if err := mgr.PersistMeta(projectDir); err != nil {
		slog.Warn("wui: persist meta after engine new", "error", err)
	}
	return id, nil
}

// planRestore decides which engines to rebuild on startup and which one is
// active. Pure function over meta.json contents so it can be tested without
// spinning up engines or a store.
//
// Two cases:
//   - nil meta (first run or read error): synthesize a single main engine.
//   - non-empty meta: restore every engine in Engines array, honor ActiveEngineID.
func planRestore(meta *short.WorkspaceMeta, defaultModel string) ([]short.EngineMeta, string) {
	if meta == nil || len(meta.Engines) == 0 {
		return []short.EngineMeta{{
			ID:    "main",
			Name:  "main",
			Model: defaultModel,
		}}, "main"
	}
	activeID := "main"
	if meta.ActiveEngineID != "" {
		activeID = meta.ActiveEngineID
	}
	return meta.Engines, activeID
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

// buildProviderConfigMap converts cfg.Providers ([]Provider) into the
// map[string]*Provider shape used by TUI and wui for model listing +
// capability resolution. Mirrors tui.App.SetProviders.
func buildProviderConfigMap(cfg *config.Config) map[string]*config.Provider {
	m := make(map[string]*config.Provider, len(cfg.Providers))
	for i := range cfg.Providers {
		m[cfg.Providers[i].Name] = &cfg.Providers[i]
	}
	return m
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
