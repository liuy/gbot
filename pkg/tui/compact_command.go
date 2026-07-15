package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/tool"
)

// handleCompact implements the /compact command via the virtual tool pattern
// (mirrors bash_shortcut.go and the pre-turn auto-compact in engine.go).
//
//	/compact               → compact conversation with default summarization
//	/compact [instructions] → compact with custom summarization instructions
//
// The tool block is created synchronously so the user sees it immediately;
// ManualCompact runs async and returns a toolEndMsg that fills the result.
func (a *App) handleCompact(args string, commitCmd tea.Cmd) tea.Cmd {
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot compact while streaming")
	}
	if a.engine == nil {
		return a.showInfo("No engine available")
	}

	toolID := "compact-manual-" + uuid.New().String()[:8]
	summary := "Compacting conversation..."
	if args != "" {
		summary = fmt.Sprintf("Compacting conversation (%s)", args)
	}

	// Add user message + create the tool block synchronously (mirrors
	// bash_shortcut.go) so the card appears immediately. PendingToolStarted
	// attaches to the last message, so AddUserMessage must come first.
	displayInput := "/compact"
	if args != "" {
		displayInput += " " + args
	}
	a.repl.AddUserMessage(displayInput)
	a.repl.StartQuery()
	a.repl.PendingToolStarted(toolID, "Compact", summary, args, tool.SearchReadKind{})
	a.status.SetStreaming(true)
	a.spinner.Start()
	a.markViewportDirty()

	customInstructions := args
	eng := a.engine
	ctx := context.Background()

	asyncCmd := func() tea.Msg {
		result, err := eng.ManualCompact(ctx, customInstructions)

		if err != nil {
			return toolEndMsg{
				ToolUseID: toolID,
				Output:    fmt.Sprintf("Compact failed: %v", err),
				IsError:   true,
			}
		}
		return tea.Batch(
			func() tea.Msg {
				return toolParamDeltaMsg{
					ID:      toolID,
					Summary: engine.CompactSummaryLine(result),
				}
			},
			func() tea.Msg {
				return toolEndMsg{
					ToolUseID: toolID,
					Output:    engine.FormatCompactOutput(result),
				}
			},
		)()
	}

	// spinnerTickMsg is self-perpetuating only after the first tick fires.
	// /compact doesn't go through the engine's turnStartMsg (which normally
	// emits the initial tick), so we must batch it here like handleSubmitRepl
	// does for bash shortcuts. Without it the spinner never animates.
	return tea.Batch(
		asyncCmd,
		tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
			return spinnerTickMsg{}
		}),
	)
}
