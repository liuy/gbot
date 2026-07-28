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

	"github.com/google/uuid"

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
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/tool/grep"
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
	go func() {
		<-sigCh
		pidCleanup()
		os.Exit(0)
	}()

	logLevel := slog.LevelInfo
	if opts.Verbose {
		logLevel = slog.LevelDebug
	}
	logPath := filepath.Join(projectDir, "gbot.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: logLevel})))
	}

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
					Model:           "",
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
