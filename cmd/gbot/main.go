// Package main is the CLI entrypoint for gbot.
//
// Source reference: main.tsx
// Bootstraps config, LLM provider, tools, engine, and launches the TUI.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
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
	// 1. Load config from ~/.claude/settings.minimax.json or env vars
	fmt.Fprintf(os.Stderr, "main() STARTING\n")
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
		Provider:    provider,
		Tools:       reg.EnabledTools(),
		Model:       cfg.Model,
		MaxTokens:   16000,
		TokenBudget: 200000,
		Logger:      logger,
		Dispatcher:  h,
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

	// 5. Build system prompt using context builder
	workingDir, _ := os.Getwd()
	systemPrompt := buildSystemPrompt(workingDir, reg)

	// 6. Create TUI App
	app := tui.NewApp(eng, systemPrompt, h)


	// 8. Run bubbletea program
	p := tea.NewProgram(app)
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
				// Still allow env vars to override
				return applyEnvOverrides(cfg), nil
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

// applyEnvOverrides lets environment variables override file config.
// NOTE: Only ANTHROPIC_API_KEY overrides the API key — ANTHROPIC_AUTH_TOKEN
// is NOT used to override because it may belong to a different provider
// (e.g., the system Claude session's token).
func applyEnvOverrides(cfg *config.Config) *config.Config {
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	// Do NOT read ANTHROPIC_AUTH_TOKEN — it may be a different provider's key
	if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("ANTHROPIC_MODEL"); v != "" {
		cfg.Model = v
	}
	return cfg
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

	// Background task management tools
	taskReg := bash.NewTaskInfoAdapter(bash.DefaultRegistry())
	reg.MustRegister(task.NewTaskOutput(taskReg))
	reg.MustRegister(task.NewTaskStop(taskReg))

	return reg
}

// buildSystemPrompt builds the system prompt using the context builder.
func buildSystemPrompt(workingDir string, reg *tool.Registry) json.RawMessage {
	builder := context.NewBuilder(workingDir)

	// Load git status
	builder.GitStatus = context.LoadGitStatus(workingDir)

	// Load GBOT.md instructions
	builder.GBOTMDContent = context.LoadGBOTMD(workingDir)

	// Load memory files
	builder.MemoryFiles = context.LoadMemoryFiles(workingDir)

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
