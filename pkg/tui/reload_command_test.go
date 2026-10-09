package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/reload"
	"github.com/liuy/gbot/pkg/types"
)

func fixtureReloadReport() *reload.Report {
	rep := &reload.Report{
		Applied: true,
		Phase:   4,
		Files: []reload.FileEntry{
			{Path: "~/.gbot/settings.json", Status: reload.StatusReloaded, Note: "max_tokens: 32768 → 16384"},
			{Path: "MEMORY.md · CLAUDE.md", Status: reload.StatusUnchanged},
		},
		EnginesRefreshed: 1,
	}
	rep.Counts.Reloaded, rep.Counts.Unchanged, rep.Counts.Failed = 1, 1, 0
	return rep
}

func fixtureEnvWithProvider(name string) *reload.Env {
	return &reload.Env{
		ProviderMap: config.ProviderMap{name: &mockLLMProvider{name: name}},
		Cfg:         &config.Config{Model: config.ModelSpec{"default": name + "/m"}},
	}
}

// newReloadTestApp builds a minimal App wired like the production one.
func newReloadTestApp(t *testing.T) *App {
	t.Helper()
	h := hub.NewHub()
	app := NewApp(nil, "test system prompt", h)
	return app
}

func TestReloadCommand_InBuiltinRegistry(t *testing.T) {
	def, ok := builtinCommandDefs["reload"]
	if !ok {
		t.Fatal(`builtinCommandDefs must contain "reload"`)
	}
	if def.Description == "" {
		t.Error(`"reload" command must carry a Description`)
	}
	if def.HasArgs {
		t.Error(`"reload" must not take args (HasArgs=false)`)
	}
	r := NewCommandRegistry()
	cmds := r.AllCommands()
	if len(cmds) != 9 {
		t.Errorf("AllCommands() returned %d commands, want 9", len(cmds))
	}
	found := false
	for _, c := range cmds {
		if c == "reload" {
			found = true
		}
	}
	if !found {
		t.Error(`AllCommands() missing "reload"`)
	}
}

// runReloadCmd executes a tea.Cmd (unwrapping tea.BatchMsg like the runtime)
// and feeds every produced message through Update.
func runReloadCmd(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, c := range m {
			runReloadCmd(t, app, c)
		}
	default:
		model, _ := app.Update(msg)
		if model == nil {
			t.Fatalf("Update returned nil model for %T", msg)
		}
	}
}

func TestReloadCommand_ShowsReportInInfoOverlay(t *testing.T) {
	app := newReloadTestApp(t)
	app.SetReloadFn(func(ctx context.Context) (*reload.Report, *reload.Env) {
		return fixtureReloadReport(), nil
	})

	cmd := app.handleReload(nil)
	if cmd == nil {
		t.Fatal("handleReload must return a tea.Cmd")
	}
	runReloadCmd(t, app, cmd)

	if !strings.Contains(app.infoOverlay, "~/.gbot/settings.json") {
		t.Errorf("info overlay must show the report rows; got: %.300s", app.infoOverlay)
	}
	if !strings.Contains(app.infoOverlay, "1 reloaded") {
		t.Errorf("info overlay must show the counts line; got: %.300s", app.infoOverlay)
	}
}

func TestReloadCommand_UnavailableWithoutFn(t *testing.T) {
	app := newReloadTestApp(t)
	if app.reloadFn != nil {
		t.Fatal("fixture: reloadFn must be nil here")
	}
	runReloadCmd(t, app, app.handleSlashCommand(SlashCommand{Name: "reload"}, nil))
	if !strings.Contains(app.infoOverlay, "not available") {
		t.Errorf("expected the unavailable notice, got: %.200s", app.infoOverlay)
	}
}

func TestReloadCommand_RefreshesProvidersOnSettingsChange(t *testing.T) {
	app := newReloadTestApp(t)
	app.SetReloadFn(func(ctx context.Context) (*reload.Report, *reload.Env) {
		rep := fixtureReloadReport()
		rep.SettingsChanged = true
		return rep, fixtureEnvWithProvider("fresh-prov")
	})

	runReloadCmd(t, app, app.handleReload(nil))

	if _, ok := app.providers["fresh-prov"]; !ok {
		t.Error("providers must contain the reloaded provider key after a settings reload")
	}
	if app.cfg == nil || app.cfg.Model["default"] != "fresh-prov/m" {
		t.Errorf("cfg must be the reloaded cfg, got %+v", app.cfg)
	}
}

func TestReloadCommand_RefreshesSkillCommandsOnSkillsChange(t *testing.T) {
	app := newReloadTestApp(t)
	app.SetReloadFn(func(ctx context.Context) (*reload.Report, *reload.Env) {
		rep := fixtureReloadReport()
		rep.Files = append(rep.Files, reload.FileEntry{
			Path:   "~/.gbot/plugins/p/skills/",
			Status: reload.StatusReloaded,
		})
		rep.Counts.Reloaded++
		return rep, &reload.Env{
			SkillCmds: []types.SkillCommand{{Name: "plug:good", Description: "does good"}},
		}
	})

	runReloadCmd(t, app, app.handleReload(nil))

	if _, ok := app.commands.getCommandDef("plug:good"); !ok {
		t.Error("skill slash command map must be rebuilt from env.SkillCmds on a skills reload")
	}
}

// TestReloadCommand_NoSkillRefreshForMyskillsPluginRows pins the exact
// skills-row matching: a plugin literally named "myskills" whose
// agents/mcp.json rows reloaded must NOT look like a skills change.
func TestReloadCommand_NoSkillRefreshForMyskillsPluginRows(t *testing.T) {
	app := newReloadTestApp(t)
	app.SetReloadFn(func(ctx context.Context) (*reload.Report, *reload.Env) {
		rep := fixtureReloadReport()
		rep.Files = append(rep.Files,
			reload.FileEntry{Path: "~/.gbot/plugins/myskills/mcp.json", Status: reload.StatusReloaded},
			reload.FileEntry{Path: "~/.gbot/plugins/myskills/agents/", Status: reload.StatusReloaded},
		)
		rep.Counts.Reloaded += 2
		return rep, &reload.Env{
			SkillCmds: []types.SkillCommand{{Name: "myskill:trap", Description: "must not register"}},
		}
	})

	runReloadCmd(t, app, app.handleReload(nil))

	if _, ok := app.commands.getCommandDef("myskill:trap"); ok {
		t.Error("non-skills rows of a plugin named myskills must not trigger the slash-command rebuild")
	}
}

// TestReloadCommand_RefreshesSkillCommandsOnUserSkillsEdit: user-level
// ~/.gbot/skills edits emit no dedicated report row (the skill listing is
// part of the system prompt), so PromptChanged is their only signal.
func TestReloadCommand_RefreshesSkillCommandsOnUserSkillsEdit(t *testing.T) {
	app := newReloadTestApp(t)
	app.SetReloadFn(func(ctx context.Context) (*reload.Report, *reload.Env) {
		rep := fixtureReloadReport()
		rep.PromptChanged = true
		return rep, &reload.Env{
			SkillCmds: []types.SkillCommand{{Name: "user:good", Description: "user-level skill"}},
		}
	})

	runReloadCmd(t, app, app.handleReload(nil))

	if _, ok := app.commands.getCommandDef("user:good"); !ok {
		t.Error("user-level skills edits (prompt flip) must rebuild the skill slash commands")
	}
}

// TestReloadCommand_NoProviderRefreshWithoutSettingsChange pins that the
// provider/skill rebuilds are conditional — an unrelated reload must not
// clobber the live provider table with a partial env.
func TestReloadCommand_NoProviderRefreshWithoutSettingsChange(t *testing.T) {
	app := newReloadTestApp(t)
	app.SetReloadFn(func(ctx context.Context) (*reload.Report, *reload.Env) {
		return fixtureReloadReport(), fixtureEnvWithProvider("unrelated-prov")
	})

	runReloadCmd(t, app, app.handleReload(nil))

	if _, ok := app.providers["unrelated-prov"]; ok {
		t.Error("providers must NOT be replaced when SettingsChanged is false")
	}
}
