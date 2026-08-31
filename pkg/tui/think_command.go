package tui

import (
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/llm"
)

// handleThink implements /think:
//
//	/think          → show the current effort
//	/think <value>  → set the effort for subsequent requests
func (a *App) handleThink(args string, commitCmd tea.Cmd) tea.Cmd {
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot change thinking effort while streaming")
	}
	args = strings.ToLower(strings.TrimSpace(args))
	if args == "" {
		return a.showInfo(fmt.Sprintf("Thinking effort: %s (none|auto|low|medium|high|max)", a.engine.Thinking()))
	}
	effort, err := llm.ParseEffort(args)
	if err != nil {
		return a.showInfo(err.Error())
	}
	if err := a.engine.SetThinking(effort); err != nil {
		return a.showInfo(err.Error())
	}
	slog.Info("think: switched", "effort", effort)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Thinking effort: %s", effort)))
}
