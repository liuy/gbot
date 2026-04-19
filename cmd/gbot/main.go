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
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/bash"
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
	// Debug logging: always write info-level events to /tmp/gbot.log
	// This provides comprehensive observability for diagnosing token stats,
	// event ordering, and rendering issues.
	if f, err := os.OpenFile("/tmp/gbot.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	// 1. Load config from ~/.claude/settings.minimax.json or env vars
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "Error: ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN not set.")
		fmt.Fprintln(os.Stderr, "Set it via environment variable or in ~/.claude/settings.minimax.json")
		os.Exit(1)
	}

	// 2. Create LLM provider (Anthropic)
	provider := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	})

	// 3. Create tools
	reg := createTools()

	// 4. Create engine
	logger := slog.Default()
	if cfg.Verbose {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	h := hub.NewHub()

	eng := engine.New(&engine.Params{
		Provider:      provider,
		ToolsProvider: reg.ToolMapFn(),
		Model:         cfg.Model,
		MaxTokens:     16000,
		TokenBudget:   200000,
		Logger:        logger,
		Dispatcher:    h,
	})

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

	// Register Agent tool (needs engine for sub-engine factory)
	agentTool := createAgentTool(eng)
	reg.MustRegister(agentTool)

	// 5. Build system prompt using context builder
	workingDir, _ := os.Getwd()

		// Initialize agent loader (lazy — discovers ~/.gbot/agents/ and .gbot/agents/)
		agenttool.InitLoader(workingDir)

		systemPrompt := buildSystemPrompt(workingDir, reg)

	// Store system prompt on engine for fork agent access
	eng.SetSystemPrompt(systemPrompt)

	// Wire fork agent notification callback — delivers fork results
	// into the parent conversation as user messages (same pattern as bash background tasks).
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

	// Register task management tools with unified registry (bash + fork agents).
	bashTaskReg := bash.NewTaskInfoAdapter(bash.DefaultRegistry())
	forkTaskReg := agentTool.TaskAdapter()
	compositeTaskReg := task.NewMultiRegistry(bashTaskReg, forkTaskReg)
	reg.MustRegister(task.NewTaskOutput(compositeTaskReg))
	reg.MustRegister(task.NewTaskStop(compositeTaskReg))

	// 6. Initialize short-term memory store
	configDir, _ := config.ConfigDir()
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
					storeMsgs := make([]short.Message, len(msgs))
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
			session, err := store.CreateSession(workingDir, cfg.Model)
			if err != nil {
				slog.Warn("main: failed to create session", "error", err)
			} else {
				sessionID = session.SessionID
				if err := tui.WriteWorkspaceMeta(sessionID); err != nil {
					slog.Warn("main: write workspace meta failed", "error", err)
				}
				slog.Info("main: created new session", "sessionID", sessionID)
			}
		}
	}

	// 8. Create TUI App
	app := tui.NewApp(eng, systemPrompt, h)
	app.SetStore(store, sessionID, workingDir, lastPersistedIdx)

	// 9. Run bubbletea program
	p := tea.NewProgram(app, tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}

// loadConfig reads configuration from the minimax settings file then env vars.
func loadConfig() (*config.Config, error) {
	// Try ~/.claude/settings.minimax.json first
	homeDir, err := os.UserHomeDir()
	if err == nil {
		minimaxPath := filepath.Join(homeDir, ".claude", "settings.minimax.json")
		if _, err := os.Stat(minimaxPath); err == nil {
			cfg, err := config.LoadFromSettingsFile(minimaxPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", minimaxPath, err)
			} else {
				// Settings file is authoritative — do NOT apply env overrides.
				// Env vars (ANTHROPIC_BASE_URL, ANTHROPIC_MODEL etc.) may belong
				// to a different provider (e.g. GLM) and would corrupt minimax config.
				return cfg, nil
			}
		}
	}

	// Fallback to standard config loading
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg, nil
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

// createAgentTool creates the Agent tool and wires the sub-engine factory.
// Called after engine construction to break the circular dependency:
// tools → engine → tools (Agent needs engine to create sub-engines).
func createAgentTool(eng *engine.Engine) *agenttool.AgentTool {
	at := agenttool.New()
	at.SetFactory(
		func(ctx context.Context, opts agenttool.SubEngineOpts) (*types.SubQueryResult, error) {
			startTime := time.Now()
			subEng := eng.NewSubEngine(engine.SubEngineOptions{
				SystemPrompt:    string(opts.SystemPrompt),
				Tools:           opts.Tools,
				MaxTurns:        opts.MaxTurns,
				Model:           opts.Model,
				ParentToolUseID: opts.ParentToolUseID,
				AgentType:       opts.AgentType,
			})

			var result engine.QueryResult
			if len(opts.ForkMessages) > 0 {
				result = subEng.QueryWithExistingMessages(ctx, opts.ForkMessages, opts.SystemPrompt)
			} else {
				result = subEng.QuerySync(ctx, opts.Prompt, opts.SystemPrompt)
			}
			if result.Error != nil {
				return nil, result.Error
			}
			return agenttool.FinalizeResult(result.Messages, opts.AgentType, startTime, result.TotalUsage, 0), nil
		},
		eng.Tools,
	)
	return at
}

// buildSystemPrompt builds the system prompt using the context builder.
func buildSystemPrompt(workingDir string, reg *tool.Registry) json.RawMessage {
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

	prompt, err := builder.Build()
	if err != nil {
		// Fallback to a minimal system prompt
		fallback := json.RawMessage(`"You are gbot, an interactive AI coding assistant. Use tools to accomplish tasks."`)
		return fallback
	}
	return prompt
}
