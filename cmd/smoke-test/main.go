// Package main is a non-interactive smoke test for gbot's e2e flow.
// It sends a single message, streams the response, and exits.
// This verifies: config loading → context building → API call → SSE streaming → tool dispatch.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/user/gbot/pkg/config"
	ctxbuilder "github.com/user/gbot/pkg/context"
	"github.com/user/gbot/pkg/engine"
	"github.com/user/gbot/pkg/llm"
	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/tool/bash"
	"github.com/user/gbot/pkg/tool/fileread"
	"github.com/user/gbot/pkg/tool/fileedit"
	"github.com/user/gbot/pkg/tool/filewrite"
	"github.com/user/gbot/pkg/tool/glob"
	"github.com/user/gbot/pkg/tool/grep"
	"github.com/user/gbot/pkg/types"
)

func main() {
	// 1. Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Config: model=%s base_url=%s timeout=%dms\n", cfg.Model, cfg.BaseURL, cfg.APITimeoutMS)

	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "No API key found")
		os.Exit(1)
	}

	// Debug: verify key loaded
	fmt.Printf("API key length: %d (prefix: %s...)\n", len(cfg.APIKey), cfg.APIKey[:min(20, len(cfg.APIKey))])

	// 2. Create provider
	provider := llm.NewAnthropicProvider(&llm.AnthropicConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		Timeout: time.Duration(cfg.APITimeoutMS) * time.Millisecond,
	})

	// 3. Create tools
	tools := []tool.Tool{
		bash.New(),
		fileread.New(),
		fileedit.New(),
		filewrite.New(),
		glob.New(),
		grep.New(),
	}
	fmt.Printf("Tools: %d registered\n", len(tools))

	// 4. Build system prompt
	workingDir, _ := os.Getwd()
	builder := ctxbuilder.NewBuilder(workingDir)
	builder.GitStatus = ctxbuilder.LoadGitStatus(workingDir)
	builder.GBOTMDContent = ctxbuilder.LoadGBOTMD(workingDir)
	for _, t := range tools {
		if p := t.Prompt(); p != "" {
			builder.ToolPrompts = append(builder.ToolPrompts, p)
		}
	}
	systemPrompt, err := builder.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Context build error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("System prompt: %d bytes\n", len(systemPrompt))

	// 5. Create engine
	eng := engine.New(&engine.Config{
		Provider:    provider,
		Tools:       tools,
		Model:       cfg.Model,
		MaxTokens:   16000,
		TokenBudget: 200000,
	})

	// 6. Run a simple query
	testMessage := "Say hello in one sentence. Do not use any tools."
	fmt.Printf("\nSending: %q\n\n", testMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	eventCh, resultCh := eng.Query(ctx, testMessage, systemPrompt)

	// Read events
	for {
		select {
		case evt, ok := <-eventCh:
			if !ok {
				continue
			}
			switch evt.Type {
			case types.EventTextDelta:
				fmt.Print(evt.Text)
			case types.EventToolUseStart:
				if evt.ToolUse != nil {
					fmt.Printf("\n[Tool: %s]\n", evt.ToolUse.Name)
				}
			case types.EventToolResult:
				if evt.ToolResult != nil {
					fmt.Printf("[Tool result: %s (%.0fms)]\n", evt.ToolResult.ToolUseID, evt.ToolResult.Timing.Seconds()*1000)
				}
			case types.EventStreamStart:
				fmt.Print("--- stream start ---\n")
			case types.EventComplete:
				fmt.Print("\n--- complete ---\n")
			case types.EventError:
				if evt.Error != nil {
					fmt.Fprintf(os.Stderr, "\nError: %v\n", evt.Error)
				}
			}

		case result, ok := <-resultCh:
			if !ok {
				fmt.Fprintln(os.Stderr, "Result channel closed unexpectedly")
				os.Exit(1)
			}
			fmt.Printf("\nResult: terminal=%s turns=%d\n", result.Terminal, result.TurnCount)
			if result.Error != nil {
				fmt.Fprintf(os.Stderr, "Query error: %v\n", result.Error)
				os.Exit(1)
			}
			if result.TotalUsage.InputTokens > 0 || result.TotalUsage.OutputTokens > 0 {
				fmt.Printf("Usage: in=%d out=%d tokens\n", result.TotalUsage.InputTokens, result.TotalUsage.OutputTokens)
			}
			os.Exit(0)

		case <-time.After(60 * time.Second):
			fmt.Fprintln(os.Stderr, "\nTimeout waiting for response")
			os.Exit(1)
		}
	}
}

func loadConfig() (*config.Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return config.DefaultConfig(), nil
	}
	minimaxPath := filepath.Join(homeDir, ".claude", "settings.minimax.json")
	if _, err := os.Stat(minimaxPath); err == nil {
		cfg, err := config.LoadFromSettingsFile(minimaxPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", minimaxPath, err)
		} else {
			// Do NOT let env vars override the minimax settings file key.
			// ANTHROPIC_AUTH_TOKEN in env may be a different provider's key.
			return cfg, nil
		}
	}
	return config.Load()
}
