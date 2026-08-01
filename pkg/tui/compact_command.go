package tui

import (
	"context"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/types"
)

// handleCompact implements the /compact command. ManualCompact runs in a
// background goroutine and emits a full query lifecycle (EventQueryStart →
// tool events → EventQueryEnd) through the hub; readEvents drains those into
// TUI messages, so the tool block appears and the stream finalizes without
// manual UI simulation here.
//
//	/compact               → compact conversation with default summarization
//	/compact [instructions] → compact with custom summarization instructions
func (a *App) handleCompact(args string, commitCmd tea.Cmd) tea.Cmd {
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot compact while streaming")
	}
	if a.engine == nil {
		return a.showInfo("No engine available")
	}

	displayInput := "/compact"
	if args != "" {
		displayInput += " " + args
	}
	a.repl.AddUserMessage(displayInput)
	a.repl.StartQuery()
	a.status.SetStreaming(true)
	a.status.SetUsage(types.Usage{})
	a.spinner.Start()
	a.markViewportDirty()

	userMsg := types.Message{
		ID:        uuid.New().String(),
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.NewTextBlock(displayInput)},
		Timestamp: time.Now(),
	}
	customInstructions := args
	eng := a.engine
	ctx := context.Background()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("compact goroutine panic", "err", r)
			}
		}()
		if _, err := eng.ManualCompact(ctx, userMsg, customInstructions); err != nil {
			slog.Warn("manual compact failed", "err", err)
		}
	}()

	return tea.Batch(
		a.readEvents(),
		tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
			return spinnerTickMsg{}
		}),
	)
}
