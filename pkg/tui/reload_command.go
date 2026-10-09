package tui

import (
	"context"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/liuy/gbot/pkg/reload"
)

// reloadOutcome bundles the reload result with the swapped env.
type reloadOutcome struct {
	report *reload.Report
	env    *reload.Env
}

type reloadDoneMsg reloadOutcome

// SetReloadFn wires the /reload command to the orchestrator (nil = /reload
// reports unavailable — e.g. App instances not backed by a full Start).
func (a *App) SetReloadFn(fn func(ctx context.Context) (*reload.Report, *reload.Env)) {
	a.reloadFn = fn
}

// handleReload implements /reload: the reload runs in the Cmd goroutine
// (never blocking Update), the report lands as reloadDoneMsg. No streaming
// gate: the context refresh is lock-safe mid-query by design (the same
// mechanism compaction relies on).
func (a *App) handleReload(_ tea.Cmd) tea.Cmd {
	if a.reloadFn == nil {
		a.infoOverlay = "/reload not available in this mode"
		a.infoOverlayScroll = 0
		return nil
	}
	reloadFn := a.reloadFn
	return tea.Batch(
		a.showInfo("Reloading configuration…"),
		func() tea.Msg {
			report, env := reloadFn(context.Background())
			return reloadDoneMsg(reloadOutcome{report: report, env: env})
		},
	)
}

// handleReloadDone applies a finished reload: refresh the provider table and
// skill slash commands when their surfaces changed, then show the report in
// the scrollable info overlay (multi-line, like /context).
func (a *App) handleReloadDone(msg reloadDoneMsg) tea.Cmd {
	if msg.report == nil {
		return nil
	}
	if msg.report.Applied && msg.report.SettingsChanged && msg.env != nil {
		// Without this, /model would serve the stale provider table after a
		// settings reload.
		a.SetProviders(msg.env.ProviderMap, msg.env.Cfg)
	}
	if msg.env != nil && skillsRowsChanged(msg.report) {
		// Without this, /skill completion would serve the stale skill set.
		slashCmds := make(map[string]CommandDef, len(msg.env.SkillCmds))
		for _, sc := range msg.env.SkillCmds {
			slashCmds[sc.Name] = CommandDef{
				Description: sc.Description,
				HasArgs:     true,
			}
		}
		a.RegisterSkillCommands(slashCmds)
	}
	a.infoOverlay = msg.report.Render()
	a.infoOverlayScroll = 0
	return nil
}

// skillsRowsChanged reports whether the skill slash-command set may have
// changed. The orchestrator emits plugin skills rows whose Path ends in
// "/skills/"; user-level ~/.gbot/skills edits emit no dedicated row — the
// skill listing lives in the system prompt, so PromptChanged is their only
// signal (re-registering an identical command set is a harmless no-op).
// Exact suffix matching keeps a plugin named "myskills" from false-positive
// matches on its agents/mcp.json rows.
func skillsRowsChanged(rep *reload.Report) bool {
	suffix := string(os.PathSeparator) + "skills" + string(os.PathSeparator)
	for _, row := range rep.Files {
		if row.Status == reload.StatusReloaded && strings.HasSuffix(row.Path, suffix) {
			return true
		}
	}
	return rep.PromptChanged
}
