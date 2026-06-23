package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// replSnapshotAdapter implements engine.ReplSnapshot for *ReplState.
// Lives in package tui so the engine package doesn't import tui (which would
// be a cycle). Constructed once per EngineViewState; the underlying pointer
// never changes for the lifetime of that engine.
type replSnapshotAdapter struct {
	r *ReplState
}

func (a replSnapshotAdapter) IsStreaming() bool { return a.r.IsStreaming() }
func (a replSnapshotAdapter) CurrentToolName() string {
	return a.r.CurrentToolName()
}

// newReplAdapter wraps a *ReplState as an engine.ReplSnapshot.
func newReplAdapter(r *ReplState) engine.ReplSnapshot {
	return replSnapshotAdapter{r: r}
}

// Lazy materialization of engines read from meta.json is intentionally
// deferred (Assumption #1 of the multi-engine plan). Until that lands, every
// engine listed in meta.json must be constructed at boot — startup cost is
// O(N) in engine count. The future lazy path will construct on first switch
// via engine.BuildEngine + deps stored on App, gated on adding
// EngineBuildDeps to pkg/engine/factory.go.

// renderEngineStatusBar renders a single line summarizing all engines.
// Output format:
//
//	● main · ● engine-2 (Edit) · ○ engine-3 (idle)
//
// Indicators: ● active (bold white), ● streaming (faint, label changes with tool), ○ idle (faint).
// Hidden when only one engine is registered (no useful information to show).
func (a *App) renderEngineStatusBar() string {
	views, activeID := a.engineMgr.Snapshot()
	if len(views) <= 1 {
		return ""
	}
	parts := make([]string, 0, len(views))
	for _, vs := range views {
		var indicator, label string
		switch {
		case vs.ID == activeID:
			indicator = "●"
			label = vs.Name
		case vs.IsStreaming:
			if a.bgBlinkTick%2 == 0 {
				indicator = "●"
			} else {
				indicator = " "
			}
			if vs.CurrentToolName != "" {
				label = vs.Name + " (" + vs.CurrentToolName + ")"
			} else {
				label = vs.Name + " (streaming)"
			}
		default:
			indicator = "○"
			label = vs.Name + " (idle)"
		}
		var styled string
		switch vs.ID {
		case activeID:
			styled = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Render(indicator + " " + label)
		default:
			styled = lipgloss.NewStyle().Faint(true).Render(indicator + " " + label)
		}
		parts = append(parts, styled)
	}
	sep := lipgloss.NewStyle().Faint(true).Render(" · ")
	// Leading and trailing spaces match StatusBar.View() so the engine row
	// lines up with the model/token row beneath the separator.
	return " " + strings.Join(parts, sep) + " "
}

// buildBackgroundDrainFn returns a drain function that applies engine events
// to a background engine's ReplState without producing tea.Msgs. The TUIHandler
// for a background engine calls this for every Hub event.
//
// Mirrors what updateRepl does for the active engine minus: tea.Cmd production,
// status bar / token counter updates, viewport dirty flags (those belong to the
// active engine only). Background state accumulates and surfaces instantly when
// the user switches to that engine.
//
// Background engines cannot interactively prompt for permission or input —
// those events are auto-denied / aborted to avoid deadlock (the TUI's dialog
// overlay is for the active engine only).
func (a *App) buildBackgroundDrainFn(vs *engine.EngineViewState) func(tea.Msg) {
	// vs.Repl may be nil when the view state was registered by
	// restoreEngines (main.go) without a Repl attached. Return a no-op
	// drain so switchEngine can still flip the handler into background
	// mode; events for that engine are dropped until it gets a Repl
	// (e.g. on the next /engine switch that re-uses the view state).
	r, ok := vs.Repl.(replSnapshotAdapter)
	if !ok || r.r == nil {
		return func(tea.Msg) {}
	}
	repl := r.r
	return func(msg tea.Msg) {
		switch m := msg.(type) {
		case textDeltaMsg:
			if m.Agent == nil {
				repl.AppendChunk(m.Text)
			}
		case turnStartMsg:
			if m.Agent == nil && !repl.IsStreaming() {
				repl.StartQuery()
				repl.AppendTextItem()
			}
		case toolStartMsg:
			if m.Agent == nil {
				repl.PendingToolStarted(m.ID, m.Name, m.Summary, m.Input,
					tool.SearchReadKind{IsSearch: m.IsSearch, IsRead: m.IsRead, IsList: m.IsList, IsLsp: m.IsLsp})
			}
		case toolParamDeltaMsg:
			if m.Agent == nil {
				repl.PendingToolDelta(m.ID, m.Delta, m.Summary,
					tool.SearchReadKind{IsSearch: m.IsSearch, IsRead: m.IsRead, IsList: m.IsList, IsLsp: m.IsLsp})
			}
		case toolOutputDeltaMsg:
			if m.Agent == nil {
				repl.PendingToolOutput(m.ToolUseID, m.DisplayOutput, m.Timing)
			}
		case toolEndMsg:
			if m.Agent == nil {
				repl.PendingToolDone(m.ToolUseID, m.Output, m.IsError, m.Timing,
					tool.SearchReadKind{IsSearch: m.IsSearch, IsRead: m.IsRead, IsList: m.IsList, IsLsp: m.IsLsp})
			}
		case thinkingStartMsg:
			if m.Agent == nil {
				repl.PendingThinkingStarted()
			}
		case thinkingDeltaMsg:
			if m.Agent == nil {
				repl.PendingThinkingDelta(m.Text)
			}
		case thinkingEndMsg:
			if m.Agent == nil {
				repl.PendingThinkingDone(m.Duration)
			}
		case queryEndMsg:
			if m.Agent == nil {
				streamStart := repl.StreamingStart()
				repl.FinishStream(m.Err)
				repl.AppendStatsLine(streamStart, m.TotalUsage)
			}
		case attachmentMsg:
			// Background attachment: TUI-initiated only. Ignore.
		case permissionAskMsg:
			if m.event != nil && m.event.ResponseCh != nil {
				select {
				case m.event.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}:
				default:
				}
			}
		case inputAskMsg:
			if m.event != nil && m.event.ResponseCh != nil {
				select {
				case m.event.ResponseCh <- types.AskResponse{Aborted: true}:
				default:
				}
			}
		}
	}
}
