package app

import (
	"encoding/json"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tui"
	"github.com/liuy/gbot/pkg/types"
)

// RunTUI creates and runs the bubbletea TUI program. Returns the error
// from p.Run() if the program fails. The caller should defer Cleanup()
// to tear down engines and stores.
func (inst *Instance) RunTUI() error {
	app := tui.NewAppWithManager(inst.EngineMgr, inst.SystemPrompt, inst.Hub)
	app.SetProviders(inst.ProviderMap, inst.Cfg)
	if inst.DaemonMode {
		app.SetDisableFileHistory(true)
	}
	if len(inst.SkillCmdsForTUI) > 0 {
		slashCmds := make(map[string]tui.CommandDef, len(inst.SkillCmdsForTUI))
		for _, sc := range inst.SkillCmdsForTUI {
			slashCmds[sc.Name] = tui.CommandDef{
				Description: sc.Description,
				HasArgs:     true,
			}
		}
		app.RegisterSkillCommands(slashCmds)
	}
	app.SetStore(inst.Store, inst.SessionID, inst.ProjectDir)
	app.SetEngineFactory(inst.EngineFactory)

	initialTokens := types.EstimateTokens(inst.SystemPrompt)
	for _, t := range inst.MainRefs.Reg.EnabledTools() {
		if b, err := json.Marshal(t.InputSchema()); err == nil {
			initialTokens += types.EstimateTokens(string(b))
		}
	}
	if ct := app.Engine().GetContextTokens(); ct > 0 {
		initialTokens = ct
	} else {
		initialTokens += engine.EstimateMessagesTokensForProvider(app.Engine().Messages(), app.CurrentProvider())
	}
	app.SetContextUsed(initialTokens)

	app.SetAutoCleanupFn(func() bool {
		inst.MainRefs.JobReg.CleanupCompleted()
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
		for _, t := range inst.MainRefs.JobReg.List() {
			if t.Status == "running" {
				_ = inst.MainRefs.JobReg.Kill(t.ID)
			}
		}
	})

	p := tea.NewProgram(app, tea.WithMouseCellMotion())
	_, err := p.Run()
	if err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}
	return nil
}
