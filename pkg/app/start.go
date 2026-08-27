package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/connector/wechat"
	"github.com/liuy/gbot/pkg/connector/wui"
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
	"github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/computer"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/tool/fileread"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tui"
	"github.com/liuy/gbot/pkg/types"
)

// Start performs full initialization: config loading, provider setup,
// engine restore, WeChat connectors, and WUI wiring. It returns an
// Instance holding everything RunTUI() or the Wails GUI needs.
// The caller handles the WeChat login subcommand before calling Start.
func Start(opts Options) (*Instance, error) {
	var mediaStores []*media.Store

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	workingDir, _ := os.Getwd()
	projectDir := project.Dir(workingDir)
	if opts.DaemonMode {
		// Chdir to a fixed location so daemon state (PID, memory, session)
		// is isolated from project-specific gbot instances.
		daemonDir := filepath.Join(home, ".gbot", "daemon")
		_ = os.MkdirAll(daemonDir, 0755)
		if err := os.Chdir(daemonDir); err == nil {
			workingDir = daemonDir
			projectDir = project.Dir(workingDir)
		}
	}

	pidCleanup, err := acquirePID(projectDir)
	if err != nil {
		return nil, err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	var dreamCancel context.CancelFunc
	go func() {
		<-sigCh
		if dreamCancel != nil {
			dreamCancel()
		}
		pidCleanup()
		os.Exit(0)
	}()

	logLevel := slog.LevelInfo
	if opts.Verbose {
		logLevel = slog.LevelDebug
	}
	logPath := filepath.Join(projectDir, "gbot.log")
	f := setupLogFile(logPath, logLevel)
	_ = f

	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	startPprofServer(cfg.PprofAddr)

	providerMap, err := config.CreateAllProviders(cfg)
	if err != nil {
		return nil, err
	}
	if len(providerMap) == 0 {
		return nil, fmt.Errorf("no API key configured. Set providers[].keys in ~/.gbot/settings.json")
	}

	provider, model, primaryProviderCfg, err := resolvePrimaryProvider(cfg, providerMap)
	if err != nil {
		return nil, err
	}

	skillReg := skills.NewRegistry(workingDir)
	if err := skillReg.Load(); err != nil {
		slog.Warn("main: skill registry load failed", "error", err)
	}

	agent.InitLoader(workingDir)

	loadedPlugins, pluginErr := plugins.LoadAndInitialize(context.Background(), workingDir, cfg)
	if pluginErr != nil {
		slog.Warn("main: plugin loading failed", "error", pluginErr)
	}

	logger := slog.Default()

	h := hub.NewHub()

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

	if loadedPlugins != nil {
		if len(loadedPlugins.Hooks) > 0 {
			hooksConfig = plugins.MergeHooks(hooksConfig, loadedPlugins.Hooks)
			hookSystem.ReloadConfig(hooksConfig)
		}
		_ = loadedPlugins.EnvVars
		skillReg.RegisterPluginSkills(loadedPlugins.Skills)
		agent.GlobalLoader().RegisterPluginAgents(loadedPlugins.Agents)
	}

	skillCmdsForTUI := skillReg.GetSkillToolSkills()

	configDir, _ := config.ConfigDir()
	permRules := permission.LoadConfig(configDir, workingDir)
	var permChecker *permission.Checker
	if len(permRules) > 0 {
		permChecker = permission.NewChecker(permRules)
		slog.Info("main: permission rules loaded", "count", len(permRules))
	}

	var permCheckerIface permission.PermissionChecker
	if permChecker != nil {
		permCheckerIface = permChecker
	}

	lspReg := lsp.NewRegistry(workingDir)
	lspReg.Scan(lsp.DefaultServers)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		lspReg.Start(ctx, lsp.DefaultServers)
		slog.Info("lsp:startup", "servers", lspReg.LSPString())
	}()

	gitStatus := ctxbuild.LoadGitStatus(workingDir)

	needWS := opts.DaemonMode || opts.WSPort != "8765" || os.Getenv("GBOT_WS_ADDR") != ""
	var wsRegistry *computer.ConnectionRegistry
	var wsMux *http.ServeMux
	if needWS {
		wsRegistry = computer.NewConnectionRegistry()
		wsAddr := ":" + opts.WSPort
		if env := os.Getenv("GBOT_WS_ADDR"); env != "" {
			wsAddr = env
		}
		wsMux = http.NewServeMux()
		if _, err := computer.StartWSServer(wsRegistry, wsAddr, wsMux); err != nil {
			return nil, fmt.Errorf("ws server: %w", err)
		}
		slog.Info("ws:listen", "addr", wsAddr)
	}

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

	deps := engine.SharedDeps{
		WorkingDir: workingDir,
		GitStatus:  gitStatus,
		SkillReg:   skillReg,
		McpReg:     mcpRegistry,
		Hooks:      hookSystem,
		Cfg:        cfg,
		LSPReg:     lspReg,
		WSRegistry: wsRegistry,
		ShortStore: store,
	}

	mainTaskList := task.NewList("")
	mainRefs := engine.CreateTools(deps, mainTaskList)

	modelThinking := map[string]llm.ThinkingMode{}
	for i := range cfg.Providers {
		for _, name := range cfg.Providers[i].Models.Ordered() {
			mc, _ := cfg.Providers[i].Models.Get(name)
			if mc.Thinking != "" {
				modelThinking[name] = mc.Thinking
			}
		}
	}

	skillListing := skilltool.BuildSkillListing(skillReg.GetSkillToolSkills(), contextWindow)
	var toolPrompts []string
	for _, t := range mainRefs.Reg.EnabledTools() {
		if p := t.Prompt(); p != "" {
			toolPrompts = append(toolPrompts, p)
		}
	}
	systemPrompt := ctxbuild.BuildSystemPrompt(workingDir, projectDir, toolPrompts, skillListing, lspReg, "")

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
		var engineHub *hub.Hub
		var handler *tui.TUIHandler
		if opts.DaemonMode {
			// No TUI in daemon mode — skip TUIHandler entirely. Its appCh
			// has no readEvents consumer without a bubbletea loop, so any
			// event would block the engine goroutine on appCh <- msg.
			engineHub = hub.NewHub()
		} else {
			engineHub, handler = tui.NewEngineHubWithHandler(id, nil)
		}
		engTaskList := task.NewList("")
		refs := engine.CreateTools(deps, engTaskList)
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
			MaxTurns:      0,
			TokenBudget:   engCtxWindow,
			AutoCompact: engine.AutoCompactConfig{
				ContextWindow:          engCtxWindow,
				MaxConsecutiveFailures: 3,
			},
			Compactor:         nil,
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
		newEng.SetAgentDefs(agent.ListAgentDefinitions())
		newEng.SetSharedDeps(&deps)
		newEng.SetSkillRegistry(skillReg)

		if !cfg.SessionNotes.Disabled && store != nil && contextWindow > 0 {
			smCfg := session.DefaultConfig()
			smExtractFn := func(ctx context.Context, prompt string, notesPath string, messages []types.Message, sysPrompt string) error {
				editTool := fileedit.New()
				readTool := fileread.New()
				subEng := newEng.NewSubEngine(engine.SubEngineOptions{
					SystemPrompt:    sysPrompt,
					Tools:           map[string]tool.Tool{"Edit": editTool, "Read": readTool},
					MaxTurns:        0,
					Model:           "",
					ParentToolUseID: "",
					AgentType:       "session_memory",
				})
				defer subEng.Close()

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
				extractionUserMsg := types.NewUserMessage(
					[]types.ContentBlock{types.NewTextBlock(prompt)},
				)
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

		newEng.SetToolRefs(refs)
		return newEng, handler, nil
	}

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

	dreamEnabled, _, _, dreamCfgErr := cfg.Dream.Defaults()
	if dreamCfgErr != nil {
		slog.Warn("dream: config error, dream disabled", "error", dreamCfgErr)
		dreamEnabled = false
	}
	if dreamEnabled && store != nil && contextWindow > 0 {
		memoryDir := long.GetMemoryPath(workingDir)
		dreamEng, dreamHandler, dreamModel, dreamCtxWindow := createDreamEngine(dreamEngineDeps{
			Cfg:                cfg,
			ProviderMap:        providerMap,
			Provider:           provider,
			PrimaryProviderCfg: primaryProviderCfg,
			Model:              model,
			WorkingDir:         workingDir,
			Store:              store,
			Logger:             logger,
			DaemonMode:         opts.DaemonMode,
		})
		dreamEng.SetStore(store, projectDir)
		resumeDreamSession(dreamEng, store, projectDir)

		engineMgr.Add(&engine.EngineViewState{
			Engine:          dreamEng,
			Handler:         dreamHandler,
			Repl:            tui.NewReplSnapshot(),
			History:         nil,
			ID:              "dream",
			Name:            "Dream",
			ActiveSessionID: dreamEng.SessionID(),
			Model:           dreamModel,
			CreatedAt:       time.Now(),
			LastActiveAt:    time.Now(),
			ReadOnly:        false,
			System:          true,
		})

		dreamCtx, dreamCancelFn := context.WithCancel(context.Background())
		dreamCancel = dreamCancelFn
		_, idle, cooldown, _ := cfg.Dream.Defaults()
		go dream.RunDreamTimer(dreamCtx, dream.TimerParams{
			Engine:        &dreamEngineAdapter{eng: dreamEng},
			Store:         store,
			IdleQuerier:   store,
			MemoryDir:     memoryDir,
			IdleThreshold: idle,
			Cooldown:      cooldown,
			TickInterval:  10 * time.Minute,
			Logger:        logger,
		})
		slog.Info("dream: persistent engine started",
			"model", dreamModel, "context_window", dreamCtxWindow)
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
			daemonMode:         opts.DaemonMode,
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

	if sessionID != "" {
		hookSystem.SessionStart(context.Background(), &hooks.HookInput{
			HookEventName: string(hooks.HookSessionStart),
			SessionID:     sessionID,
			Cwd:           workingDir,
			Source:        "startup",
		})
	}

	if needWS && wsMux != nil {
		wc := wui.New(engineMgr, providerMap, buildProviderConfigMap(cfg))
		// Register the Send tool + "wui" FileSender on every engine restored
		// at boot. Engines created later via engine_new are wired inside
		// createEngineForWUI.
		for _, vs := range engineMgr.List() {
			if vs.System {
				continue // dream engine has no mutable registry (no CreateTools)
			}
			registerWUISendTool(vs.Engine, wc)
		}
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
			return createEngineForWUI(name, engineMgr, engineFactory, store, projectDir, wc, currentProvider, currentModel)
		})
		// Dedicated media cache for WS chunked uploads — do NOT reuse the
		// wechat cache (it is only constructed when wechat is enabled). The
		// cleanup goroutine is stopped via the shared mediaStores slice at
		// shutdown (this slice is later assigned to Instance.MediaStores in
		// the return literal below — at this point in the function `inst`
		// does not exist yet, so we must append to the local slice).
		uploadCache, err := media.New()
		if err != nil {
			slog.Warn("wui: media cache init failed, WS uploads disabled", "error", err)
		} else {
			wc.SetMediaCache(uploadCache)
			mediaStores = append(mediaStores, uploadCache)
		}
		wui.RegisterStaticRoutes(wsMux)
		wui.RegisterChatWS(wsMux, wc)
		wui.RegisterArtifactRoutes(wsMux, filepath.Join(projectDir, tool.ArtifactDirName))
		slog.Info("wui: mounted on ws mux", "engines", engineMgr.Count())
	}

	return &Instance{
		EngineMgr:          engineMgr,
		EngineFactory:      engineFactory,
		Store:              store,
		SessionID:          sessionID,
		Cfg:                cfg,
		ProviderMap:        providerMap,
		Provider:           provider,
		Model:              model,
		PrimaryProviderCfg: primaryProviderCfg,
		SystemPrompt:       systemPrompt,
		SkillListing:       skillListing,
		ToolPrompts:        toolPrompts,
		MainRefs:           &mainRefs,
		SkillCmdsForTUI:    skillCmdsForTUI,
		HookSystem:         hookSystem,
		WorkingDir:         workingDir,
		ProjectDir:         projectDir,
		DaemonMode:         opts.DaemonMode,
		WSPort:             opts.WSPort,
		Hub:                h,
		MediaStores:        mediaStores,
		LSPReg:             lspReg,
		Logger:             logger,
		PIDCleanup:         pidCleanup,
	}, nil
}

// dreamEngineDeps holds the dependencies for building the persistent dream engine.
type dreamEngineDeps struct {
	Cfg                *config.Config
	ProviderMap        map[string]llm.Provider
	Provider           llm.Provider
	PrimaryProviderCfg *config.Provider
	Model              string
	WorkingDir         string
	Store              *short.Store
	Logger             *slog.Logger
	DaemonMode         bool
}

// createDreamEngine builds a top-level engine for dream memory consolidation.
// It reuses the standard CreateTools path (so it gets a proper ToolRefs/Reg),
// then applies a whitelist filter to keep only the tools dream needs.
func createDreamEngine(d dreamEngineDeps) (*engine.Engine, *tui.TUIHandler, string, int) {
	// Build the full tool set via the standard path.
	dreamRefs := engine.CreateTools(engine.SharedDeps{
		WorkingDir: d.WorkingDir,
		GitStatus:  nil,
		SkillReg:   nil,
		McpReg:     nil,
		Hooks:      nil,
		Cfg:        d.Cfg,
		LSPReg:     nil,
		WSRegistry: nil,
		ShortStore: d.Store,
	}, task.NewList(""))

	// Whitelist: only tools dream needs.
	dreamWhitelist := []string{
		"Read", "Write", "Edit", "Bash", "Grep", "Glob",
		"Recall",
	}
	dreamDef := types.AgentDefinition{Tools: dreamWhitelist}
	dreamTools := agent.ResolveAgentTools(dreamRefs.Reg.ToolMapFn()(), &dreamDef)

	dreamProv := d.Provider
	dreamModel := d.Model
	if d.Cfg.Dream.Model != "" {
		if dp, dm, err := d.Cfg.ResolveModelByName(d.Cfg.Dream.Model); err != nil {
			slog.Warn("dream: resolve model failed, using default", "model", d.Cfg.Dream.Model, "error", err)
		} else if dp != nil {
			if p, ok := d.ProviderMap[dp.Name]; ok {
				dreamProv = p
				dreamModel = dm
			}
		}
	}

	dreamProviderCfg := d.PrimaryProviderCfg
	if dreamProv != d.Provider {
		for i := range d.Cfg.Providers {
			if d.Cfg.Providers[i].Name == dreamProv.Name() {
				dreamProviderCfg = &d.Cfg.Providers[i]
				break
			}
		}
	}
	dreamCtxWindow := dreamProviderCfg.ResolveContext(dreamModel)
	dreamMaxTokens := dreamProviderCfg.ResolveMaxTokens(dreamModel)

	var dreamHub types.EventDispatcher
	var dreamHandler *tui.TUIHandler
	if d.DaemonMode {
		dreamHub = hub.NewHub()
	} else {
		h, handler := tui.NewEngineHubWithHandler("dream", nil)
		dreamHub = h
		dreamHandler = handler
	}

	dreamEng := engine.New(&engine.Params{
		Provider:      dreamProv,
		ToolsProvider: func() map[string]tool.Tool { return dreamTools },
		Model:         dreamModel,
		MaxTokens:     dreamMaxTokens,
		MaxTurns:      0,
		TokenBudget:   dreamCtxWindow,
		AutoCompact: engine.AutoCompactConfig{
			ContextWindow:          dreamCtxWindow,
			MaxConsecutiveFailures: 3,
		},
		Compactor:         nil,
		Logger:            d.Logger,
		Dispatcher:        dreamHub,
		MCPRegistry:       nil,
		Hooks:             nil,
		PermissionChecker: nil,
		WorkingDir:        d.WorkingDir,
		TaskList:          task.NewList(""),
		ModelThinking:     nil,
		EngineID:          "dream",
		InputModalities:   nil,
	})
	dreamEng.SetToolRefs(dreamRefs)
	engine.WireEngine(dreamEng, dreamRefs, engine.SharedDeps{
		WorkingDir: d.WorkingDir,
		GitStatus:  nil,
		SkillReg:   nil,
		McpReg:     nil,
		Hooks:      nil,
		Cfg:        d.Cfg,
		LSPReg:     nil,
		WSRegistry: nil,
		ShortStore: d.Store,
	})
	dreamEng.SetSystemPrompt(dream.SystemPrompt)
	dreamEng.SetOnClose(func(sessionID string) {
		dreamRefs.BashReg.CleanupCompleted()
	})
	dreamDisplayModel := dreamProv.Name() + "/" + dreamModel
	return dreamEng, dreamHandler, dreamDisplayModel, dreamCtxWindow
}

// dreamEngineAdapter bridges *engine.Engine to the dream.DreamEngine interface.
type dreamEngineAdapter struct {
	eng *engine.Engine
}

func (a *dreamEngineAdapter) IsBusy() bool {
	return a.eng.IsBusy()
}

func (a *dreamEngineAdapter) SessionID() string {
	return a.eng.SessionID()
}

func (a *dreamEngineAdapter) Query(ctx context.Context, prompt string) error {
	result := a.eng.QuerySync(ctx, prompt, a.eng.SystemPrompt())
	return result.Error
}

// resumeDreamSession restores the dream engine's previous session if one
// exists, otherwise creates a fresh one.
func resumeDreamSession(eng *engine.Engine, store *short.Store, projectDir string) {
	sessions, err := store.ListSessionsByEngine(projectDir, "dream", 1)
	if err == nil && len(sessions) > 0 {
		if _, err := eng.SwitchSession(sessions[0].SessionID); err == nil {
			slog.Info("dream: resumed session", "sessionID", sessions[0].SessionID[:8])
			return
		}
	}
	if err := eng.NewSession(projectDir, "Dream"); err != nil {
		slog.Warn("dream: create session failed", "error", err)
	}
}
