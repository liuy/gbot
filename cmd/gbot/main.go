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
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/permission"
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
	"github.com/liuy/gbot/pkg/tool/task"
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


	// 3. Create tools
	reg := createTools()

	// Resolve working directory early (needed for skill registry, MCP, and system prompt)
	workingDir, _ := os.Getwd()

	// 3.1 Initialize skill registry
	skillReg := skills.NewRegistry(workingDir)
	if err := skillReg.Load(); err != nil {
		slog.Warn("main: skill registry load failed", "error", err)
	}
	reg.MustRegister(skilltool.New(skillReg))

	// 3.2 Register Agent tool early (factory wired after engine creation).
	// SetNotifyFn with stubs creates forkReg so TaskAdapter() works.
	agentTool := agenttool.New()
	agentTool.SetWorkingDir(workingDir)
	agentTool.SetGBOTMDContent(ctxbuild.LoadGBOTMD(workingDir))
	agentTool.SetGitStatus(ctxbuild.LoadGitStatus(workingDir))
	agentTool.SetSkillRegistry(skillReg)
	agentTool.SetNotifyFn(
		func(xml string) {},                   // stub — replaced after engine creation
		func() json.RawMessage { return nil }, // stub — replaced after engine creation
	)
	reg.MustRegister(agentTool)

	// 3.3 Register task management tools (needs agentTool.TaskAdapter from SetNotifyFn).
	agenttool.InitLoader(workingDir)
	bashTaskReg := bash.NewTaskInfoAdapter(bash.DefaultRegistry())
	forkTaskReg := agentTool.TaskAdapter()
	compositeTaskReg := task.NewMultiRegistry(bashTaskReg, forkTaskReg)
	reg.MustRegister(task.NewTaskOutput(compositeTaskReg))
	reg.MustRegister(task.NewTaskStop(compositeTaskReg))

	// 4. Create engine — all 10 tools registered, ToolsProvider captures full set.
	logger := slog.Default()
	if cfg.Verbose {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	h := hub.NewHub()

	// 4.1 Initialize MCP registry from .mcp.json
	mcpRegistry, err := mcp.LoadAndConnectMCP(context.Background(), workingDir, mcp.TransportFactory{})
	if err != nil {
		slog.Warn("main: MCP initialization failed", "error", err)
	}

	contextWindow, maxTokens := primaryProviderCfg.ResolveCapabilities(model)

	// 4.2 Initialize hooks system
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

	// 4.2b Load permission rules from user/project/local settings
	configDir, _ := config.ConfigDir()
	permRules := permission.LoadConfig(configDir, workingDir)
	var permChecker *permission.Checker
	if len(permRules) > 0 {
		permChecker = permission.NewChecker(permRules)
		slog.Info("main: permission rules loaded", "count", len(permRules))
	}

	eng := engine.New(&engine.Params{
		Provider:         provider,
		ToolsProvider:    reg.ToolMapFn(),
		Model:            model,
		MaxTokens:        maxTokens,
		TokenBudget:      contextWindow,
		Logger:           logger,
		Dispatcher:       h,
		MCPRegistry:      mcpRegistry,
		Hooks:            hookSystem,
		PermissionChecker: permChecker,
	})

	// 4.3 Wire Agent factory (breaks circular dependency: tools → engine → tools).
	agentTool.SetFactory(
		func(ctx context.Context, opts agenttool.AgentOpts) (*types.SubQueryResult, error) {
			startTime := time.Now()
			subEng := eng.NewSubEngine(engine.SubEngineOptions{
				SystemPrompt:    string(opts.SystemPrompt),
				Tools:           opts.Tools,
				MaxTurns:        opts.MaxTurns,
				Model:           opts.Model,
				ParentToolUseID: opts.ParentToolUseID,
				AgentType:       opts.AgentType,
			})
			messages := opts.UserContextMessages
			messages = append(messages, types.Message{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock(opts.Prompt)},
			})
			if len(opts.ForkMessages) > 0 {
				messages = append(opts.ForkMessages, messages...)
			}
			var result engine.QueryResult
			if len(opts.ForkMessages) > 0 || len(opts.UserContextMessages) > 0 {
				result = subEng.QueryWithExistingMessages(ctx, messages, opts.SystemPrompt)
			} else {
				result = subEng.QuerySync(ctx, opts.Prompt, opts.SystemPrompt)
			}
			if result.Error != nil {
				if ctx.Err() != nil && len(result.Messages) > 0 {
					return agenttool.FinalizeResult(result.Messages, opts.AgentType, startTime, result.TotalUsage, 0), nil
				}
				return nil, result.Error
			}
			toolUseCount := agenttool.CountToolUses(result.Messages)
			return agenttool.FinalizeResult(result.Messages, opts.AgentType, startTime, result.TotalUsage, toolUseCount), nil
		},
		eng.Tools,
	)

	// 4.4 Replace stub SetNotifyFn with real callbacks.
	agentTool.SetNotifyFn(
		func(xml string) {
			eng.EnqueueNotification(types.Message{
				Role:      types.RoleUser,
				Content:   []types.ContentBlock{types.NewTextBlock(xml)},
				Timestamp: time.Now(),
			})
		},
		func() json.RawMessage { return eng.SystemPrompt() },
	)

	// Wire background task notifications into the engine's notification queue.
	registry := bash.DefaultRegistry()
	registry.OnNotify = func(n bash.TaskNotification) {
		xml := n.FormatXML()
		eng.EnqueueNotification(types.Message{
			Role:      types.RoleUser,
			Content:   []types.ContentBlock{types.NewTextBlock(xml)},
			Timestamp: time.Now(),
		})
	}

	// 5. Build system prompt using context builder
	systemPrompt := buildSystemPrompt(workingDir, reg, skillReg, contextWindow)

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

	// 7. Auto-resume: restore last session if workspace metadata exists
	var sessionID string
	var lastPersistedIdx int
	if store != nil {
		meta, _ := short.ReadWorkspaceMeta(workingDir)
		if meta != nil && meta.CurrentSessionID != "" {
			resumable, err := store.IsSessionResumable(meta.CurrentSessionID)
			if err == nil && resumable {
				_, msgs, err := store.ResumeSession(meta.CurrentSessionID)
				if err == nil && len(msgs) > 0 {
					storeMsgs := make([]short.TranscriptMessage, len(msgs))
					for i, m := range msgs {
						storeMsgs[i] = *m
					}
					engineMsgs, err := tui.StoreMessagesToEngine(storeMsgs)
					if err == nil {
						eng.SetMessages(engineMsgs)
						sessionID = meta.CurrentSessionID
						lastPersistedIdx = len(engineMsgs)
						eng.SetSessionID(sessionID)
						slog.Info("main: resumed session", "sessionID", sessionID, "messages", len(engineMsgs))
					} else {
						slog.Warn("main: failed to convert resumed messages", "error", err)
					}
				}
			}
		}
		// No resumable session — create a new one
		if sessionID == "" {
			session, err := store.CreateSession(workingDir, model)
			if err != nil {
				slog.Warn("main: failed to create session", "error", err)
			} else {
				sessionID = session.SessionID
				if err := tui.WriteWorkspaceMeta(workingDir, sessionID); err != nil {
					slog.Warn("main: write workspace meta failed", "error", err)
				}
				slog.Info("main: created new session", "sessionID", sessionID)
			}
		}
	}

		// 7.5 Wire auto-compact
		if store != nil && sessionID != "" {
			compactor := engine.NewAutoCompactor(store, sessionID, model, provider)
			eng.SetCompactor(compactor, engine.AutoCompactConfig{
				ContextWindow:          contextWindow,
				MaxConsecutiveFailures: 3,
			})
		}

			// 7.6 Fire SessionStart hook
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
		app.SetStore(store, sessionID, workingDir, lastPersistedIdx)

		// 8.5 Estimate initial context usage from system prompt + tools.
		// Rough heuristic: ~4 chars per token. Corrected after first API response.
		initialTokens := len(systemPrompt) / 4
		for _, t := range reg.EnabledTools() {
			if b, err := json.Marshal(t.InputSchema()); err == nil {
				initialTokens += len(b) / 4
			}
		}
		app.SetInitialContext(initialTokens, contextWindow)

	// 9. Run bubbletea program
	p := tea.NewProgram(app, tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		eng.Close()
		os.Exit(1)
	}

	// Clean shutdown: close MCP connections
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



// createTools instantiates all core tools and registers them.
func createTools() *tool.Registry {
	reg := tool.NewRegistry()
	reg.MustRegister(bash.New(bash.DefaultRegistry()))
	reg.MustRegister(fileread.New())
	reg.MustRegister(fileedit.New())
	reg.MustRegister(filewrite.New())
	reg.MustRegister(glob.New())
	reg.MustRegister(grep.New())

	// Background task management tools are registered in main() after
	// the fork agent adapter is available (need MultiRegistry for both
	// bash and fork agent tasks).

	return reg
}


// buildSystemPrompt builds the system prompt using the context builder.
func buildSystemPrompt(workingDir string, reg *tool.Registry, skillReg *skills.Registry, contextWindow int) json.RawMessage {
	builder := ctxbuild.NewBuilder(workingDir)

	// Load git status
	builder.GitStatus = ctxbuild.LoadGitStatus(workingDir)

	// Load GBOT.md instructions
	builder.GBOTMDContent = ctxbuild.LoadGBOTMD(workingDir)

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
