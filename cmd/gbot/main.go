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
	"strings"
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
	"github.com/liuy/gbot/pkg/tool/glob"
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
		McpReg:     mcpRegistry,
		Hooks:      hookSystem,
		Cfg:        cfg,
		LSPReg:     lspReg,
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
	systemPrompt := ctxbuild.BuildSystemPrompt(workingDir, toolPrompts, skillListing, lspReg)

	// 6. Initialize short-term memory store
	configDir, _ = config.ConfigDir()
	var store *short.Store
	if configDir != "" {
		dbPath := filepath.Join(configDir, "memory", "memory.db")
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
		newEng := engine.New(&engine.Params{
			Provider:      engineProvider,
			ToolsProvider: refs.Reg.ToolMapFn(),
			Model:         modelArg,
			MaxTokens:     maxTokens,
			MaxTurns:      0, // unlimited
			TokenBudget:   contextWindow,
			AutoCompact: engine.AutoCompactConfig{
				ContextWindow:          contextWindow,
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
		if cfg.SessionNotes != "off" && store != nil && contextWindow > 0 {
			smCfg := session.DefaultConfig()
			smExtractFn := func(ctx context.Context, prompt string, notesPath string, messages []types.Message, sysPrompt string) error {
				editTool := fileedit.New()
				readTool := fileread.New()
				subEng := newEng.NewSubEngine(engine.SubEngineOptions{
					SystemPrompt:    sysPrompt,
					Tools:           map[string]tool.Tool{"Edit": editTool, "Read": readTool},
					MaxTurns:        0,  // unlimited
					Model:           "", // inherit from parent
					ParentToolUseID: "",
					AgentType:       "session_memory",
				})
				defer subEng.Close()
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
			sm := session.New(smCfg, workingDir, id, smExtractFn, slog.Default())
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
			dreamMgr := dream.NewManager(dreamCfg, memoryDir, workingDir, newEng,
				store, dreamRunFn, newEng.Dispatcher(), slog.Default())
			newEng.RegisterPostTurnHook(dreamMgr.RunPostTurn)
			slog.Info("dream: wired", "engine_id", id)
		}

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
			workingDir: workingDir,
			model:      model,
		})
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
	app.SetEngineFactory(engineFactory)

	// Estimate initial context usage
	// CJK-aware estimation. Corrected after first API response.
	initialTokens := types.EstimateTokens(systemPrompt)
	for _, t := range mainRefs.Reg.EnabledTools() {
		if b, err := json.Marshal(t.InputSchema()); err == nil {
			initialTokens += types.EstimateTokens(string(b))
		}
	}
	app.SetInitialContext(initialTokens, contextWindow)

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
}

// restoreEnginesDeps bundles everything restoreEngines needs. Explicit struct
// beats a long parameter list and makes the function signature stable when
// new wiring is added.
type restoreEnginesDeps struct {
	mgr        *engine.EngineManager
	factory    tui.EngineFactoryFn
	store      *short.Store
	workingDir string
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
	meta, err := short.ReadWorkspaceMeta(d.workingDir)
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
		eng.SetStore(d.store, d.workingDir)

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
			id, err := eng.ResumeOrInitSession(d.workingDir, engineModel)
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
		})
	}

	if err := d.mgr.SetActive(activeID); err != nil {
		slog.Warn("restore: set active engine failed", "id", activeID, "error", err)
	}
	if err := d.mgr.PersistMeta(d.workingDir); err != nil {
		slog.Warn("restore: write workspace meta failed", "error", err)
	}
	if vs := d.mgr.Active(); vs != nil {
		return vs.ActiveSessionID
	}
	return ""
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
