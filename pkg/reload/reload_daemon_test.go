package reload

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/tool/computer"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	"github.com/liuy/gbot/pkg/tool/task"
)

// This file mirrors the REAL daemon construction (app.Start) for every input
// the reload baseline depends on. The simplified fixtures in reload_test.go
// build the initial env without plugin skills registered into the live
// registry, which is exactly why they stayed green while the false-positive
// row flip shipped.

// buildDaemonStartup mirrors app.Start's construction order: settings load,
// primary resolution, skills Load + plugin skill registration into the LIVE
// registry, a full CreateTools ToolPrompts harvest, and a skill listing built
// from the live registry with the startup context window. It returns the
// orchestrator (daemon passes PluginsDir "") and the startup prompt bytes.
func buildDaemonStartup(t *testing.T, env *reloadTestEnv) (*Orchestrator, string) {
	t.Helper()

	// Real daemon plugin layout: discovered from ~/.gbot/plugins.
	writeFileT(t, filepath.Join(env.home, ".gbot", "plugins", "plug", "plugin.json"),
		`{"name":"plug","version":"1.0.0","skills":"./skills/"}`)
	writeFileT(t, filepath.Join(env.home, ".gbot", "plugins", "plug", "skills", "demo", "SKILL.md"),
		"---\nname: demo\ndescription: demo plugin skill\n---\nbody\n")

	cfg := env.cfg
	pm, err := config.CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	prov, model, pcfg, err := cfg.ResolvePrimary(pm)
	if err != nil {
		t.Fatalf("ResolvePrimary: %v", err)
	}

	loadedPlugins, err := plugins.LoadAndInitialize(context.Background(), env.work, cfg)
	if err != nil {
		t.Fatalf("LoadAndInitialize: %v", err)
	}
	if len(loadedPlugins.Skills) != 1 {
		t.Fatalf("fixture must register exactly 1 plugin skill, got %d", len(loadedPlugins.Skills))
	}
	env.skillReg.RegisterPluginSkills(loadedPlugins.Skills)

	contextWindow := pcfg.ResolveContext(model)
	if contextWindow <= 0 {
		t.Fatalf("fixture context window must resolve positive, got %d", contextWindow)
	}

	deps := engine.SharedDeps{
		WorkingDir: env.work,
		SkillReg:   env.skillReg,
		McpReg:     nil,
		Hooks:      env.hookSys,
		Cfg:        cfg,
		LSPReg:     lsp.NewRegistry(env.work),
		WSRegistry: computer.NewConnectionRegistry(),
		ShortStore: nil,
	}
	mainRefs := engine.CreateTools(deps, task.NewList(""))
	var toolPrompts []string
	for _, tl := range mainRefs.Reg.EnabledTools() {
		if p := tl.Prompt(); p != "" {
			toolPrompts = append(toolPrompts, p)
		}
	}
	skillListing := skilltool.BuildSkillListing(env.skillReg.GetSkillToolSkills(), contextWindow)

	env.holder = NewEnvHolder(&Env{
		Cfg:                cfg,
		ProviderMap:        pm,
		Provider:           prov,
		Model:              model,
		PrimaryProviderCfg: pcfg,
		ModelThinking:      cfg.BuildModelThinking(),
		ToolPrompts:        toolPrompts,
		SkillListing:       skillListing,
		SkillCmds:          env.skillReg.GetSkillToolSkills(),
	})

	o := NewOrchestrator(Deps{
		WorkingDir: env.work,
		ProjectDir: env.work,
		PluginsDir: "",
		EngineMgr:  env.engineMgr,
		Hooks:      env.hookSys,
		SkillReg:   env.skillReg,
		Env:        env.holder,
		LSPReg:     deps.LSPReg,
	})
	prompt := ctxbuild.BuildSystemPrompt(env.work, env.work, toolPrompts, skillListing, "")
	if !strings.Contains(prompt, "plug:demo") {
		t.Fatal("startup prompt must list the plugin skill plug:demo; a regression filtered plugin skills out of the listing entirely")
	}
	return o, prompt
}

// promptRow returns the "MEMORY.md · CLAUDE.md" report row.
func promptRow(t *testing.T, rep *Report) FileEntry {
	t.Helper()
	for _, row := range rep.Files {
		if row.Path == "MEMORY.md · CLAUDE.md" {
			return row
		}
	}
	t.Fatalf("no prompt row in report: %+v", rep.Files)
	return FileEntry{}
}

// TestReload_DaemonConstruction_UnchangedFiles_ByteIdenticalPrompt is the
// invariant: with nothing changed on disk, a reload after a daemon-faithful
// startup must keep the assembled system prompt byte-identical and report the
// prompt row as unchanged.
func TestReload_DaemonConstruction_UnchangedFiles_ByteIdenticalPrompt(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o, startupPrompt := buildDaemonStartup(t, env)

	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply, got err=%q files=%+v", rep.Err, rep.Files)
	}

	if rep.PromptChanged {
		t.Error("PromptChanged must be false when nothing changed on disk")
	}
	row := promptRow(t, rep)
	if row.Status != StatusUnchanged {
		t.Errorf("prompt row status = %q (note %q), want unchanged on a no-op reload", row.Status, row.Note)
	}

	newPrompt := ctxbuild.BuildSystemPrompt(env.work, env.work, o.Env().ToolPrompts, o.Env().SkillListing, "")
	if newPrompt != startupPrompt {
		t.Errorf("assembled prompt diverged with unchanged files (len new=%d old=%d)", len(newPrompt), len(startupPrompt))
	}
}

// TestReload_BudgetInputChange_LeavesPromptRowUnchanged is the guardrail:
// when the context files are untouched, a legitimate change to a skill
// budget INPUT (settings provider context window) may flip its own settings
// row but must not flip the prompt row as long as the listing bytes it
// produces are identical (the fixture listing is far below any budget).
func TestReload_BudgetInputChange_LeavesPromptRowUnchanged(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o, _ := buildDaemonStartup(t, env)

	// Legitimate budget-input change: context 128k → 256k.
	writeSettingsFile(t, env.home, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"32k","context":"256k"}}}],"model":{"default":"t/m"}}`)

	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply, got err=%q files=%+v", rep.Err, rep.Files)
	}
	if rep.PromptChanged {
		t.Error("PromptChanged must stay false: the context files are unchanged and the listing bytes did not change")
	}
	if row := promptRow(t, rep); row.Status != StatusUnchanged {
		t.Errorf("prompt row status = %q (note %q), want unchanged when only the budget input changed", row.Status, row.Note)
	}
}

// TestReload_DaemonConstruction_MultiCycle_Idempotent checks that repeated
// reloads with unchanged files converge: both cycles report the prompt row as
// unchanged, and the listing and assembled prompt after cycle 2 are
// byte-identical to after cycle 1.
func TestReload_DaemonConstruction_MultiCycle_Idempotent(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o, startupPrompt := buildDaemonStartup(t, env)

	rep1 := o.Reload(context.Background())
	if !rep1.Applied {
		t.Fatalf("cycle 1 must apply, got err=%q files=%+v", rep1.Err, rep1.Files)
	}
	listing1 := o.Env().SkillListing
	prompt1 := ctxbuild.BuildSystemPrompt(env.work, env.work, o.Env().ToolPrompts, listing1, "")

	rep2 := o.Reload(context.Background())
	if !rep2.Applied {
		t.Fatalf("cycle 2 must apply, got err=%q files=%+v", rep2.Err, rep2.Files)
	}
	for i, rep := range []*Report{rep1, rep2} {
		if rep.PromptChanged {
			t.Errorf("cycle %d: PromptChanged must be false when nothing changed on disk", i+1)
		}
		if row := promptRow(t, rep); row.Status != StatusUnchanged {
			t.Errorf("cycle %d: prompt row status = %q (note %q), want unchanged", i+1, row.Status, row.Note)
		}
	}

	if o.Env().SkillListing != listing1 {
		t.Error("skill listing diverged between cycle 1 and cycle 2 with unchanged files")
	}
	prompt2 := ctxbuild.BuildSystemPrompt(env.work, env.work, o.Env().ToolPrompts, o.Env().SkillListing, "")
	if prompt2 != prompt1 {
		t.Errorf("assembled prompt diverged between cycles (len c1=%d c2=%d)", len(prompt1), len(prompt2))
	}
	if prompt1 != startupPrompt {
		t.Errorf("cycle-1 prompt diverged from startup prompt (len c1=%d startup=%d)", len(prompt1), len(startupPrompt))
	}
}
