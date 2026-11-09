package reload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	ctxbuild "github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/tool/agent"
	skilltool "github.com/liuy/gbot/pkg/tool/skill"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const settingsA = `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"32k","context":"128k"}}}],"model":{"default":"t/m"}}`

const settingsB = `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"16k","context":"128k"}}}],"model":{"default":"t/m"}}`

const settingsWithHooks = `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"32k","context":"128k"}}}],"model":{"default":"t/m"},"hooks":{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"touch %s"}]}]}}`

// reloadTestEnv bundles one test's isolated world.
type reloadTestEnv struct {
	home      string
	work      string
	plugins   string
	cfg       *config.Config
	skillReg  *skills.Registry
	hookSys   *hooks.Hooks
	holder    *EnvHolder
	engineMgr *engine.EngineManager
}

func writeSettingsFile(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".gbot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .gbot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newReloadTestEnv isolates HOME + cwd, writes settings, builds the live
// objects (skill registry, hooks system, holder) the way Start does.
func newReloadTestEnv(t *testing.T, settings string) *reloadTestEnv {
	t.Helper()
	env := &reloadTestEnv{
		home:      t.TempDir(),
		work:      t.TempDir(),
		plugins:   filepath.Join(t.TempDir(), "plugins"),
		engineMgr: engine.NewEngineManager(),
	}
	t.Setenv("HOME", env.home)
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(env.work); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	writeSettingsFile(t, env.home, settings)

	env.cfg, err = config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	env.skillReg = skills.NewRegistry(env.work)
	if err := env.skillReg.Load(); err != nil {
		t.Fatalf("skillReg.Load: %v", err)
	}
	agent.InitLoader(env.work)

	var settingsHooks hooks.HooksConfig
	if env.cfg.Hooks != nil {
		if err := json.Unmarshal(env.cfg.Hooks, &settingsHooks); err != nil {
			t.Fatalf("parse settings hooks: %v", err)
		}
	}
	env.hookSys = hooks.NewHooks(settingsHooks, &hooks.CommandExecutor{})

	env.holder = NewEnvHolder(buildInitialEnv(t, env))
	return env
}

func buildInitialEnv(t *testing.T, env *reloadTestEnv) *Env {
	t.Helper()
	pm, err := config.CreateAllProviders(env.cfg)
	if err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	prov, model, pcfg, err := env.cfg.ResolvePrimary(pm)
	if err != nil {
		t.Fatalf("ResolvePrimary: %v", err)
	}
	cmds := env.skillReg.GetSkillToolSkills()
	return &Env{
		Cfg:                env.cfg,
		ProviderMap:        pm,
		Provider:           prov,
		Model:              model,
		PrimaryProviderCfg: pcfg,
		ModelThinking:      map[string]llm.Effort{},
		SkillListing:       skilltool.BuildSkillListing(cmds, pcfg.ResolveContext(model)),
		SkillCmds:          cmds,
	}
}

func (e *reloadTestEnv) orchestrator() *Orchestrator {
	return NewOrchestrator(Deps{
		WorkingDir: e.work,
		ProjectDir: e.work,
		PluginsDir: e.plugins,
		EngineMgr:  e.engineMgr,
		Hooks:      e.hookSys,
		SkillReg:   e.skillReg,
		Env:        e.holder,
	})
}

// writePlugin lays out plugin fixture files under the test plugins dir.
func (e *reloadTestEnv) writePlugin(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(e.plugins, name)
	for rel, content := range files {
		writeFileT(t, filepath.Join(root, rel), content)
	}
	return root
}

func dispatchSessionStart(t *testing.T, h *hooks.Hooks, work string) {
	t.Helper()
	h.SessionStart(context.Background(), &hooks.HookInput{
		HookEventName: string(hooks.HookSessionStart),
		SessionID:     "test-session",
		Cwd:           work,
		Source:        "test",
	})
}

// ---------------------------------------------------------------------------
// Abort paths
// ---------------------------------------------------------------------------

func TestReload_AbortsOnBadSettings(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker-old")
	env := newReloadTestEnv(t, fmt.Sprintf(settingsWithHooks, marker))
	initialCfg := env.holder.Load().Cfg
	o := env.orchestrator()

	writeSettingsFile(t, env.home, `{"providers": not json`)
	rep := o.Reload(context.Background())

	if rep.Applied {
		t.Fatal("reload must abort on corrupt settings.json")
	}
	if rep.Phase != 0 {
		t.Errorf("Phase = %d, want 0 (settings)", rep.Phase)
	}
	var failedRow *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Status == StatusFailed {
			failedRow = &rep.Files[i]
		}
	}
	if failedRow == nil {
		t.Fatalf("no failed row in report: %+v", rep.Files)
	}
	if !strings.Contains(failedRow.Path, "settings.json") {
		t.Errorf("failed row Path = %q, want it to name settings.json", failedRow.Path)
	}
	if failedRow.ErrLine == "" {
		t.Error("failed row ErrLine must carry the parse error")
	}
	if o.Env().Cfg != initialCfg {
		t.Error("env cfg must be untouched (pointer identity) after an aborted reload")
	}
	// The live hooks registry must still dispatch the ORIGINAL settings hooks.
	dispatchSessionStart(t, env.hookSys, env.work)
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("original settings hook did not fire after abort: %v", err)
	}
}

func TestReload_AbortsOnInvalidPluginMcp(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	initialCfg := env.holder.Load().Cfg
	o := env.orchestrator()

	env.writePlugin(t, "broken", map[string]string{
		"plugin.json": `{"name":"broken","version":"1.0.0","mcpServers":{"s":"not-an-object"}}`,
	})
	rep := o.Reload(context.Background())

	if rep.Applied {
		t.Fatal("reload must abort on an invalid inline mcpServers entry")
	}
	if rep.Phase != 1 {
		t.Errorf("Phase = %d, want 1 (plugins)", rep.Phase)
	}
	found := false
	for _, row := range rep.Files {
		if row.Status == StatusFailed && strings.Contains(row.Path, filepath.Join("plugins", "broken", "plugin.json")) {
			found = true
			if row.ErrLine == "" {
				t.Error("plugin mcp failed row must carry ErrLine")
			}
		}
	}
	if !found {
		t.Fatalf("no failed row naming plugins/broken/plugin.json: %+v", rep.Files)
	}
	if o.Env().Cfg != initialCfg {
		t.Error("env cfg must be untouched after an aborted reload")
	}
}

func TestReload_AbortsOnDroppedMcpServer(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	writeFileT(t, filepath.Join(env.work, ".mcp.json"), `{"mcpServers":{"s":"not-an-object"}}`)
	rep := o.Reload(context.Background())

	if rep.Applied {
		t.Fatal("reload must abort when a project .mcp.json server entry is dropped")
	}
	if rep.Phase != 1 {
		t.Errorf("Phase = %d, want 1", rep.Phase)
	}
}

func TestReload_MidFlightChangeGuard(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()
	o.afterValidate = func() {
		// Simulate a settings save landing between validation and apply.
		writeSettingsFile(t, env.home, settingsB)
	}

	rep := o.Reload(context.Background())
	if rep.Applied {
		t.Fatal("reload must abort when settings.json changes mid-flight")
	}
	if !strings.Contains(rep.Err, "changed during reload") {
		t.Errorf("Err = %q, want it to mention the mid-flight change", rep.Err)
	}
	if len(rep.Files) != 1 {
		t.Fatalf("abort report must carry exactly one row, got %+v", rep.Files)
	}
	row := rep.Files[0]
	if !strings.HasSuffix(row.Path, "settings.json") {
		t.Errorf("row Path = %q, want it to name settings.json", row.Path)
	}
	if row.Status != StatusFailed {
		t.Errorf("row Status = %q, want failed", row.Status)
	}
	if row.Note != "mid-flight settings change detected; reload aborted" {
		t.Errorf("row Note = %q, want the mid-flight abort reason", row.Note)
	}
	if rep.Counts.Failed != 1 {
		t.Errorf("Counts.Failed = %d, want 1", rep.Counts.Failed)
	}
	if got := rep.Render(); !strings.Contains(got, "mid-flight settings change detected; reload aborted") {
		t.Errorf("Render() must surface the abort reason; got:\n%s", got)
	}
}

func TestReload_SingleFlight(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()
	o.mu.Lock()
	defer o.mu.Unlock()

	rep := o.Reload(context.Background())
	if rep.Applied {
		t.Fatal("a second concurrent reload must not run")
	}
	if !strings.Contains(rep.Err, "already in progress") {
		t.Errorf("Err = %q, want 'already in progress'", rep.Err)
	}
}

// ---------------------------------------------------------------------------
// Successful apply
// ---------------------------------------------------------------------------

func TestReload_SwapsEnvAndAppliesAll(t *testing.T) {
	marker1 := filepath.Join(t.TempDir(), "marker-settings")
	marker2 := filepath.Join(t.TempDir(), "marker-plugin")
	env := newReloadTestEnv(t, fmt.Sprintf(settingsWithHooks, marker1))
	oldCfg := env.holder.Load().Cfg
	o := env.orchestrator()

	// Plugin with hooks + skills + agents (no mcp, no repl).
	env.writePlugin(t, "plug", map[string]string{
		"plugin.json":          `{"name":"plug","version":"1.0.0","skills":"./skills/"}`,
		"hooks/hooks.json":     `{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"touch ` + marker2 + `"}]}]}`,
		"skills/good/SKILL.md": "---\nname: good\ndescription: does good\n---\nbody\n",
		"agents/planner.md":    "---\nname: planner\ndescription: plans\n---\nbody\n",
	})
	// settings edit after baseline: max_tokens 32k → 16k
	writeSettingsFile(t, env.home, fmt.Sprintf(settingsWithHooksB, marker1))

	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply, got err=%q files=%+v", rep.Err, rep.Files)
	}
	if rep.Phase != 4 {
		t.Errorf("Phase = %d, want 4", rep.Phase)
	}
	if rep.SettingsChanged != true {
		t.Error("SettingsChanged must be true after a settings edit")
	}

	if o.Env().Cfg == oldCfg {
		t.Error("env cfg must be swapped (new pointer) after a successful reload")
	}
	if _, ok := o.Env().ProviderMap["t"]; !ok {
		t.Errorf("ProviderMap must carry the fixture provider t, got %v", o.Env().ProviderMap)
	}

	// Plugin hooks are live: dispatch runs the NEW plugin matcher too.
	dispatchSessionStart(t, env.hookSys, env.work)
	if _, err := os.Stat(marker2); err != nil {
		t.Errorf("plugin hook did not fire after reload: %v", err)
	}

	if !env.skillReg.HasSkill("plug:good") {
		t.Error("plugin skill plug:good not registered after reload")
	}
	foundAgent := false
	for _, def := range agent.ListAgentDefinitions() {
		if def.AgentType == "plug:planner" {
			foundAgent = true
		}
	}
	if !foundAgent {
		t.Error("plugin agent plug:planner not in ListAgentDefinitions after reload")
	}

	// Counts: settings + plugin hooks + plugin skills + plugin agents + prompt
	// (skill listing changed) all reloaded.
	if rep.Counts.Reloaded != 5 || rep.Counts.Unchanged != 0 || rep.Counts.Failed != 0 {
		t.Errorf("counts = %+v, want 5 reloaded / 0 unchanged / 0 failed", rep.Counts)
	}
	var settingsRow *FileEntry
	for i := range rep.Files {
		if strings.HasSuffix(rep.Files[i].Path, "settings.json") && rep.Files[i].Status == StatusReloaded {
			settingsRow = &rep.Files[i]
		}
	}
	if settingsRow == nil {
		t.Fatalf("no reloaded settings row: %+v", rep.Files)
	}
	if !strings.Contains(settingsRow.Note, "max_tokens") {
		t.Errorf("settings note must carry the max_tokens diff, got: %q", settingsRow.Note)
	}
}

const settingsWithHooksB = `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"16k","context":"128k"}}}],"model":{"default":"t/m"},"hooks":{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"touch %s"}]}]}}`

func TestReload_UnchangedRun(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	rep1 := o.Reload(context.Background())
	if !rep1.Applied {
		t.Fatalf("first reload must apply: %q", rep1.Err)
	}
	rep2 := o.Reload(context.Background())
	if !rep2.Applied {
		t.Fatalf("second reload must apply: %q", rep2.Err)
	}
	if rep2.PromptChanged {
		t.Error("PromptChanged must be false when no files changed")
	}
	for _, row := range rep2.Files {
		if row.Status != StatusUnchanged {
			t.Errorf("row %q status = %q, want unchanged on a no-op reload", row.Path, row.Status)
		}
	}
	if len(rep2.Files) != 2 {
		t.Errorf("no-op reload rows = %d (%+v), want 2 (settings + prompt)", len(rep2.Files), rep2.Files)
	}
}

// TestReload_RebuiltPromptCarriesVoiceBlock pins the 10-31 ruling at the
// reload pipeline: every prompt the orchestrator assembles is the primary
// engine's prompt mirror, so it must carry the voice block exactly like the
// engine-side refreshers do. A bare baseline would not only misreport what
// engines hold — the next diff compares the (wrapped) refreshed reality
// against it, so the two halves must be built the same way.
func TestReload_RebuiltPromptCarriesVoiceBlock(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	// Startup baseline (captureBaseline) mirrors the primary engine's prompt.
	if got := o.baseline.prompt; !strings.Contains(got, "# Voice Interaction") {
		t.Errorf("startup baseline prompt lacks the voice block; got: %.200s", got)
	}

	writeSettingsFile(t, env.home, settingsB)
	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply: %q", rep.Err)
	}
	if got := o.baseline.prompt; !strings.Contains(got, "# Voice Interaction") {
		t.Errorf("post-reload baseline prompt lacks the voice block; got: %.200s", got)
	}

	// The diff prompt must equal the post-reload baseline, or every later
	// no-op reload reports a spurious prompt change.
	rep2 := o.Reload(context.Background())
	if !rep2.Applied {
		t.Fatalf("second reload must apply: %q", rep2.Err)
	}
	if rep2.PromptChanged {
		t.Error("unchanged second reload reported PromptChanged — the diff prompt diverged from the stored baseline")
	}
}

func TestReload_SkillsLoadFailureKeepsOldListing(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	sentinelListing := "SENTINEL-SKILL-LISTING"
	sentinelCmds := []types.SkillCommand{{Name: "sentinel-cmd"}}
	init := env.holder.Load()
	init.SkillListing = sentinelListing
	init.SkillCmds = sentinelCmds
	env.holder.Store(init)

	o := env.orchestrator()
	o.loadSkillsFn = func() error { return fmt.Errorf("skills scan failed") }

	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("skills failure must not abort phase 2: %q %+v", rep.Err, rep.Files)
	}
	var skillsRow *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Status == StatusFailed && strings.Contains(rep.Files[i].Path, "skills") {
			skillsRow = &rep.Files[i]
		}
	}
	if skillsRow == nil {
		t.Fatalf("no failed skills row: %+v", rep.Files)
	}
	if skillsRow.ErrLine != "skills scan failed" {
		t.Errorf("skills row ErrLine = %q, want exact injected error", skillsRow.ErrLine)
	}
	if o.Env().SkillListing != sentinelListing {
		t.Errorf("SkillListing = %q, want the sentinel carried over", o.Env().SkillListing)
	}
	if len(o.Env().SkillCmds) != 1 || o.Env().SkillCmds[0].Name != "sentinel-cmd" {
		t.Errorf("SkillCmds = %+v, want the sentinel carried over", o.Env().SkillCmds)
	}
}

func TestReload_McpBusy_SkipsRowAndCompletes(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	mgr := mcp.NewClientManager(mcp.TransportFactory{}, true, env.work)
	reg := mcp.NewRegistry(mgr, mcp.ChangeCallbacks{})
	reg.SetConfigsForTest(map[string]mcp.ScopedMcpServerConfig{
		"s": {Config: &mcp.StdioConfig{Command: "echo"}, Scope: mcp.ScopeUser},
	})
	init := env.holder.Load()
	init.McpReg = reg
	env.holder.Store(init)

	// One engine with a refresher so EnginesRefreshed is observable.
	eng := newReloadEngine(t, env)
	env.engineMgr.Add(&engine.EngineViewState{Engine: eng, ID: "main", Name: "Main"})

	o := env.orchestrator()
	unlock := reg.LockReloadForTest()
	defer unlock()

	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("busy MCP reconcile must not abort the reload: %q", rep.Err)
	}
	if rep.Phase != 4 {
		t.Errorf("Phase = %d, want 4", rep.Phase)
	}
	mcpRows := 0
	for _, row := range rep.Files {
		if row.Path == "MCP" {
			mcpRows++
			if row.Status != StatusUnchanged {
				t.Errorf("MCP row status = %q, want unchanged", row.Status)
			}
			if row.Note != "MCP reconcile skipped; another reload in progress" {
				t.Errorf("MCP row Note = %q, want the skip disclosure", row.Note)
			}
		}
	}
	if mcpRows != 1 {
		t.Errorf("MCP rows = %d, want exactly 1", mcpRows)
	}
	if rep.EnginesRefreshed != 1 {
		t.Errorf("EnginesRefreshed = %d, want 1", rep.EnginesRefreshed)
	}
}

// newReloadEngine builds a minimal engine whose context refresher reads from
// the holder, mirroring the app wiring.
func newReloadEngine(t *testing.T, env *reloadTestEnv) *engine.Engine {
	t.Helper()
	eng := engine.New(&engine.Params{
		Provider:   &stubProvider{},
		Model:      "m",
		Logger:     nil,
		WorkingDir: env.work,
	})
	holder := env.holder
	eng.SetContextRefresher(func() (string, map[string]string) {
		e := holder.Load()
		return ctxbuild.BuildSystemPrompt(env.work, env.work, e.ToolPrompts, e.SkillListing, ""),
			ctxbuild.LoadContextFiles(env.work)
	})
	return eng
}

type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, fmt.Errorf("not implemented")
}
func (stubProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestReload_RefreshesEngineContext(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	const sentinelA = "RELOAD-CTX-SENTINEL-A-91ae"
	writeFileT(t, filepath.Join(env.work, "CLAUDE.md"), "# rules\n"+sentinelA)
	eng := newReloadEngine(t, env)
	env.engineMgr.Add(&engine.EngineViewState{Engine: eng, ID: "main", Name: "Main"})

	o := env.orchestrator()
	const sentinelB = "RELOAD-CTX-SENTINEL-B-91af"
	writeFileT(t, filepath.Join(env.work, "CLAUDE.md"), "# rules\n"+sentinelB)

	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply: %q", rep.Err)
	}
	if rep.PromptChanged != true {
		t.Error("PromptChanged must be true after a CLAUDE.md edit")
	}
	if rep.EnginesRefreshed != 1 {
		t.Errorf("EnginesRefreshed = %d, want 1", rep.EnginesRefreshed)
	}
	got := eng.DumpAPIRequest()
	var ctxText strings.Builder
	for _, m := range got.Messages {
		for _, cb := range m.Content {
			ctxText.WriteString(cb.Text)
		}
	}
	if !strings.Contains(ctxText.String(), sentinelB) {
		t.Errorf("engine context after reload lacks sentinel %s; got %.300s", sentinelB, ctxText.String())
	}
	if strings.Contains(ctxText.String(), sentinelA) {
		t.Errorf("engine context after reload must drop pre-reload sentinel %s; got %.300s", sentinelA, ctxText.String())
	}
}

func TestReload_KeepsSubEngineRefresherNilByDesign(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	eng := newReloadEngine(t, env)
	sub := eng.NewSubEngine(engine.SubEngineOptions{AgentType: "General"})
	if sub.RefreshContext() {
		t.Error("sub-engines must keep a nil refresher (design: stable instructions for the whole task)")
	}
}

func TestReload_ToolPromptsMatchFreshToolSet(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply: %q", rep.Err)
	}

	refs := engine.CreateTools(engine.SharedDeps{
		WorkingDir: env.work,
		SkillReg:   env.skillReg,
		Hooks:      env.hookSys,
		Cfg:        o.Env().Cfg,
	}, task.NewList(""))
	var want []string
	for _, tl := range refs.Reg.EnabledTools() {
		if p := tl.Prompt(); p != "" {
			want = append(want, p)
		}
	}
	if len(want) == 0 {
		t.Fatal("fixture harvest produced no prompts; test is vacuous")
	}
	got := o.Env().ToolPrompts
	if len(got) != len(want) {
		t.Fatalf("ToolPrompts len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ToolPrompts[%d] differs from a fresh harvest:\n got: %.120s\nwant: %.120s", i, got[i], want[i])
		}
	}
}

func TestReload_ReportsPluginMcpRow(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	env.writePlugin(t, "mcpd", map[string]string{
		"plugin.json": `{"name":"mcpd","version":"1.0.0","mcpServers":{"s":{"command":"node","args":["x.js"]}}}`,
	})
	rep := o.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("valid plugin reload must apply: %+v", rep)
	}
	var found *FileEntry
	for i := range rep.Files {
		if strings.Contains(rep.Files[i].Path, filepath.Join("plugins", "mcpd", "plugin.json")) {
			found = &rep.Files[i]
		}
	}
	if found == nil {
		t.Fatalf("no plugin.json row for the new plugin: %+v", rep.Files)
	}
	if found.Status != StatusReloaded {
		t.Errorf("row status = %v, want %v", found.Status, StatusReloaded)
	}
	if found.Note != "1 added · 0 removed · 0 changed" {
		t.Errorf("row note = %q, want the 1-added summary", found.Note)
	}
}

// Pin tests for reload.go mutation sweep; assertions are exact so a single
// changed byte flips the test. Mutant line:mutator pairs are in the sweep
// ledger. Fixtures reuse the reloadTestEnv harness from reload_test.go.

const pinSettings = `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`

// Phase-0 hooks parse abort: exact envelope fields.
func TestPinReload_AbortsOnBadHooksJSON(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	writeSettingsFile(t, env.home, `{"hooks":"@@@not-json@@@","providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`)
	rep := env.orchestrator().Reload(context.Background())
	if rep.Applied {
		t.Fatal("reload applied despite unparseable hooks")
	}
	if rep.Phase != 0 {
		t.Errorf("Phase = %d, want 0", rep.Phase)
	}
	if !strings.HasPrefix(rep.Err, "settings hooks:") {
		t.Errorf("Err = %q, want 'settings hooks:' prefix", rep.Err)
	}
	if len(rep.Files) != 1 {
		t.Fatalf("Files = %+v, want one row", rep.Files)
	}
	row := rep.Files[0]
	if row.Status != StatusFailed {
		t.Errorf("row.Status = %q, want failed", row.Status)
	}
	if row.Path != displaySettingsPath() {
		t.Errorf("row.Path = %q, want %q", row.Path, displaySettingsPath())
	}
	if row.ErrLine == "" {
		t.Error("row.ErrLine empty, want the parse error")
	}
	if rep.Counts.Failed != 1 {
		t.Errorf("Counts.Failed = %d, want 1", rep.Counts.Failed)
	}
	if rep.LastReloadAt.IsZero() {
		t.Error("LastReloadAt zero on abort, want a timestamp")
	}
}

// CreateAllProviders and ResolvePrimary aborts carry the same envelope.
func TestPinReload_AbortsOnBadProviders(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	writeSettingsFile(t, env.home, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["$PIN_NO_SUCH_KEY"],"models":{"m":{}}}],"model":{"default":"t/m"}}`)
	rep := env.orchestrator().Reload(context.Background())
	if rep.Applied || rep.Phase != 0 {
		t.Fatalf("Applied=%v Phase=%d, want abort at phase 0", rep.Applied, rep.Phase)
	}
	if !strings.Contains(rep.Err, "no providers could be created") {
		t.Errorf("Err = %q, want the providers error", rep.Err)
	}
	if len(rep.Files) != 1 || rep.Files[0].Status != StatusFailed || rep.Files[0].Path != displaySettingsPath() {
		t.Errorf("Files = %+v, want one failed settings row", rep.Files)
	}

	writeSettingsFile(t, env.home, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"ghost/m"}}`)
	rep2 := env.orchestrator().Reload(context.Background())
	if rep2.Applied || rep2.Phase != 0 {
		t.Fatalf("Applied=%v Phase=%d, want abort at phase 0 for a bad default model", rep2.Applied, rep2.Phase)
	}
	if !strings.Contains(rep2.Err, "ghost") {
		t.Errorf("Err = %q, want the resolve error naming ghost", rep2.Err)
	}
	if len(rep2.Files) != 1 || rep2.Files[0].Status != StatusFailed || rep2.Files[0].Path != displaySettingsPath() || rep2.Files[0].ErrLine == "" {
		t.Errorf("Files = %+v, want one failed settings row with ErrLine", rep2.Files)
	}
}

// Single-flight refusal: exact envelope with a live LastReloadAt.
func TestPinReload_SingleFlightEnvelope(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	orch.mu.Lock()
	defer orch.mu.Unlock()
	rep := orch.Reload(context.Background())
	if rep.Applied {
		t.Fatal("concurrent reload applied")
	}
	if rep.Phase != 0 {
		t.Errorf("Phase = %d, want 0", rep.Phase)
	}
	if rep.Err != "reload already in progress" {
		t.Errorf("Err = %q, want the single-flight refusal", rep.Err)
	}
	if rep.LastReloadAt.IsZero() {
		t.Error("LastReloadAt zero on refusal, want a timestamp")
	}
}

// A successful reload stamps LastReloadAt, phase 4, applied, and swaps every
// env field the holder exposes.
func TestPinReload_SuccessEnvelopeAndEnvSwap(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	oldEnv := env.holder.Load()
	orch := env.orchestrator()
	rep := orch.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload failed: %+v", rep)
	}
	if rep.Phase != 4 {
		t.Errorf("Phase = %d, want 4", rep.Phase)
	}
	if rep.LastReloadAt.IsZero() {
		t.Error("LastReloadAt zero on success, want a timestamp")
	}
	if rep.Err != "" {
		t.Errorf("Err = %q, want empty on success", rep.Err)
	}
	newEnv := env.holder.Load()
	if newEnv == oldEnv {
		t.Fatal("holder still holds the pre-reload env")
	}
	if newEnv.Cfg == nil || newEnv.ProviderMap == nil || newEnv.Provider == nil ||
		newEnv.Model != "m" || newEnv.PrimaryProviderCfg == nil || newEnv.ModelThinking == nil {
		t.Errorf("swapped env incomplete: %+v", newEnv)
	}
	if newEnv.McpReg != oldEnv.McpReg {
		t.Error("swapped env lost the live McpReg (in-place reconcile)")
	}
	if orch.Env() != newEnv {
		t.Error("Orchestrator.Env() does not reflect the swap")
	}
}

// Mid-flight settings change guard aborts at phase 1 with the note.
func TestPinReload_MidFlightGuardEnvelope(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	orch.afterValidate = func() {
		writeSettingsFile(t, env.home, pinSettings+"\n")
	}
	rep := orch.Reload(context.Background())
	if rep.Applied || rep.Phase != 1 {
		t.Fatalf("Applied=%v Phase=%d, want abort at phase 1", rep.Applied, rep.Phase)
	}
	if !strings.Contains(rep.Err, "changed during reload") {
		t.Errorf("Err = %q, want the mid-flight refusal", rep.Err)
	}
	if len(rep.Files) != 1 || rep.Files[0].Status != StatusFailed || rep.Files[0].Note == "" {
		t.Errorf("Files = %+v, want one failed row with a note", rep.Files)
	}
}

// The skills-failure path appends the exact failed row and keeps counts.
func TestPinReload_SkillsFailureRow(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	orch.loadSkillsFn = func() error { return errors.New("pin: skills down") }
	rep := orch.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("skills failure aborted the reload: %+v", rep)
	}
	var row *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Status == StatusFailed {
			row = &rep.Files[i]
		}
	}
	if row == nil {
		t.Fatalf("no failed row for the skills failure: %+v", rep.Files)
	}
	if !strings.HasSuffix(row.Path, filepath.Join(".gbot", "skills")) {
		t.Errorf("row.Path = %q, want the .gbot/skills home path", row.Path)
	}
	if row.ErrLine != "pin: skills down" {
		t.Errorf("row.ErrLine = %q, want pin: skills down", row.ErrLine)
	}
	if rep.Counts.Failed != 1 {
		t.Errorf("Counts.Failed = %d, want 1", rep.Counts.Failed)
	}
}

// Permission rules in user settings build a checker on the swapped env.
func TestPinReload_PermissionCheckerBuilt(t *testing.T) {
	env := newReloadTestEnv(t, `{"permissions":{"ask":["Bash(git push *)"]},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/m"}}`)
	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload failed: %+v", rep)
	}
	if env.holder.Load().PermissionChecker == nil {
		t.Fatal("PermissionChecker nil after reload despite rules in settings")
	}
}

func TestPinReload_NoRulesNoChecker(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload failed: %+v", rep)
	}
	if pc := env.holder.Load().PermissionChecker; pc != nil {
		t.Errorf("PermissionChecker = %v, want nil without rules", pc)
	}
}

// First MCP server after an MCP-less start: the swapped env gains a registry
// and the MCP row reports the add.
func TestPinReload_FirstServerBuildsRegistry(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	// .mcp.json in the project dir gives allServers one entry while the
	// startup env has no registry.
	writeFileT(t, filepath.Join(env.work, ".mcp.json"), `{"mcpServers":{"pinsrv":{"command":"/bin/true"}}}`)
	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload failed: %+v", rep)
	}
	if env.holder.Load().McpReg == nil {
		t.Fatal("swapped env has no McpReg despite a first server")
	}
	if rep.Counts.Reloaded < 1 {
		t.Errorf("Counts.Reloaded = %d, want >=1 (settings + MCP rows)", rep.Counts.Reloaded)
	}
	var mcpRow *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Path == "MCP" {
			mcpRow = &rep.Files[i]
		}
	}
	if mcpRow == nil {
		t.Fatalf("no MCP row: %+v", rep.Files)
	}
	if mcpRow.Status != StatusReloaded {
		t.Errorf("MCP row.Status = %q, want reloaded", mcpRow.Status)
	}
	if !strings.Contains(mcpRow.Note, "1 added") {
		t.Errorf("MCP row.Note = %q, want '1 added'", mcpRow.Note)
	}
	if !strings.Contains(mcpRow.Note, "0 removed") {
		t.Errorf("MCP row.Note = %q, want '0 removed'", mcpRow.Note)
	}
}

// The second reload with no changes reports no MCP row and unchanged counts.
func TestPinReload_SecondCycleUnchanged(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	if rep := orch.Reload(context.Background()); !rep.Applied {
		t.Fatalf("first reload failed: %+v", rep)
	}
	rep := orch.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("second reload failed: %+v", rep)
	}
	for _, f := range rep.Files {
		if f.Path == "MCP" {
			t.Errorf("unchanged cycle emitted an MCP row: %+v", f)
		}
		if f.Status == StatusReloaded {
			t.Errorf("unchanged cycle reported reloaded row: %+v", f)
		}
	}
	if rep.Counts.Reloaded != 0 {
		t.Errorf("Counts.Reloaded = %d, want 0", rep.Counts.Reloaded)
	}
}

// Phase 4 refreshes every top-level engine and stamps the counter.
func TestPinReload_RefreshesEnginesCounter(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	eng := newReloadEngine(t, env)
	env.engineMgr.Add(&engine.EngineViewState{Engine: eng, ID: "main", Name: "Main"})
	before := len(env.engineMgr.List())
	if before != 1 {
		t.Fatalf("test env engines = %d, want 1", before)
	}
	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload failed: %+v", rep)
	}
	if rep.EnginesRefreshed != before {
		t.Errorf("EnginesRefreshed = %d, want %d", rep.EnginesRefreshed, before)
	}
}

// display helpers are the wui/TUI surface contract.
func TestPinDisplayPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := displaySettingsPath(); !strings.HasSuffix(got, filepath.Join(".gbot", "settings.json")) {
		t.Errorf("displaySettingsPath = %q", got)
	}
	if got := displayHomePath(filepath.Join("a", "b")); got != filepath.Join("~", "a", "b") {
		t.Errorf("displayHomePath rel = %q, want ~/a/b", got)
	}
	if got := displayPath(filepath.Join(home, "x", "y")); got != filepath.Join("~", "x", "y") {
		t.Errorf("displayPath under home = %q, want ~/x/y", got)
	}
	if got := displayPath("/abs/elsewhere"); got != "/abs/elsewhere" {
		t.Errorf("displayPath abs = %q", got)
	}
	if got := displayPluginPath(filepath.Join(home, "plugins", "p"), "skills"); !strings.HasSuffix(got, filepath.Join("~", "plugins", "p", "skills")) {
		t.Errorf("displayPluginPath = %q", got)
	}
}

// A plugin whose manifest carries hooks/skills/agents/repls produces rows on
// first reload and none on the second.
func TestPinReload_PluginRowsLifecycle(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	env.writePlugin(t, "pinplug", map[string]string{
		"plugin.json":         `{"name":"pinplug","version":"1","skills":"./skills/","repls":"./repl/"}`,
		"hooks/hooks.json":    `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"true"}]}]}}`,
		"skills/pin/SKILL.md": `---\nname: pin\n---\nbody\n`,
		"agents/pin.md":       `---\nname: pin\n---\nbody\n`,
		"repl/pin.js":         "var x = 1",
	})
	rep := orch.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("first reload failed: %+v", rep)
	}
	var sawHooks, sawSkills, sawAgents, sawRepl, sawMcp bool
	for _, f := range rep.Files {
		switch {
		case strings.HasSuffix(f.Path, filepath.Join("pinplug", "hooks", "hooks.json")):
			sawHooks = true
			if f.Status != StatusReloaded || f.Note == "" {
				t.Errorf("hooks row = %+v, want reloaded with note", f)
			}
		case strings.HasSuffix(f.Path, filepath.Join("pinplug", "skills")+string(os.PathSeparator)):
			sawSkills = true
		case strings.HasSuffix(f.Path, filepath.Join("pinplug", "agents")+string(os.PathSeparator)):
			sawAgents = true
		case strings.HasSuffix(f.Path, filepath.Join("pinplug", "repl")+string(os.PathSeparator)):
			sawRepl = true
		case f.Path == "MCP":
			sawMcp = true
		}
	}
	if !sawHooks || !sawSkills || !sawAgents || !sawRepl {
		t.Errorf("plugin rows incomplete: hooks=%v skills=%v agents=%v repl=%v files=%+v", sawHooks, sawSkills, sawAgents, sawRepl, rep.Files)
	}
	if sawMcp {
		t.Errorf("MCP row emitted for a plugin without servers: %+v", rep.Files)
	}

	rep2 := orch.Reload(context.Background())
	if !rep2.Applied {
		t.Fatalf("second reload failed: %+v", rep2)
	}
	pluginRows := 0
	for _, f := range rep2.Files {
		if !strings.Contains(f.Path, "pinplug") {
			continue
		}
		pluginRows++
		if f.Status != StatusUnchanged {
			t.Errorf("unchanged plugin row not marked unchanged: %+v", f)
		}
	}
	if pluginRows == 0 {
		t.Errorf("second cycle dropped all plugin rows: %+v", rep2.Files)
	}
}

// The settings diff note carries joined leaf changes.
func TestPinReload_SettingsDiffNote(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	if rep := orch.Reload(context.Background()); !rep.Applied {
		t.Fatalf("first reload failed: %+v", rep)
	}
	writeSettingsFile(t, env.home, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k2"],"models":{"m":{}}}],"model":{"default":"t/m"}}`)
	rep := orch.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("second reload failed: %+v", rep)
	}
	var row *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Path == displaySettingsPath() {
			row = &rep.Files[i]
		}
	}
	if row == nil {
		t.Fatalf("no settings row: %+v", rep.Files)
	}
	if row.Status != StatusReloaded {
		t.Errorf("settings row.Status = %q, want reloaded", row.Status)
	}
	if !strings.Contains(row.Note, "keys") {
		t.Errorf("settings row.Note = %q, want the keys leaf change", row.Note)
	}
	if !rep.SettingsChanged {
		t.Error("SettingsChanged false despite a settings diff")
	}
}

// A dropped .mcp.json server aborts phase 1 with the exact row.
func TestPinReload_DroppedMcpRowExact(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	o := env.orchestrator()
	mcpPath := filepath.Join(env.work, ".mcp.json")
	writeFileT(t, mcpPath, `{"mcpServers":{"s":"not-an-object"}}`)
	rep := o.Reload(context.Background())
	if rep.Applied {
		t.Fatal("reload applied despite a dropped server")
	}
	if rep.Phase != 1 {
		t.Fatalf("Phase = %d, want 1", rep.Phase)
	}
	if len(rep.Files) != 1 {
		t.Fatalf("Files = %+v, want exactly the dropped-server row", rep.Files)
	}
	row := rep.Files[0]
	if row.Path != "mcpServers.s" {
		t.Errorf("row.Path = %q, want the mcpServers.s validation pointer", row.Path)
	}
	if row.Status != StatusFailed {
		t.Errorf("row.Status = %q, want failed", row.Status)
	}
	if row.ErrLine == "" {
		t.Error("row.ErrLine empty, want the validation message")
	}
	if rep.Counts.Failed != 1 {
		t.Errorf("Counts.Failed = %d, want 1", rep.Counts.Failed)
	}
}

// A plugin-pieces load failure aborts with the generic "plugins" row.
func TestPinReload_PluginScanFailureRow(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	blocker := filepath.Join(env.plugins, "blocker")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{deps: Deps{WorkingDir: env.work, ProjectDir: env.work, PluginsDir: blocker, Hooks: env.hookSys, SkillReg: env.skillReg, Env: env.holder}}
	o.loadSkillsFn = func() error { return env.skillReg.Load() }
	rep := o.Reload(context.Background())
	if rep.Applied {
		t.Fatal("reload applied despite a plugin scan failure")
	}
	if rep.Phase != 1 {
		t.Fatalf("Phase = %d, want 1", rep.Phase)
	}
	if len(rep.Files) != 1 || rep.Files[0].Path != "plugins" || rep.Files[0].Status != StatusFailed {
		t.Errorf("Files = %+v, want the generic plugins row", rep.Files)
	}
}

// Phase 4 skips nil-engine view states without breaking the counter.
func TestPinReload_NilEngineViewStateSkipped(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	eng := newReloadEngine(t, env)
	env.engineMgr.Add(&engine.EngineViewState{ID: "ghost"}) // nil Engine
	env.engineMgr.Add(&engine.EngineViewState{Engine: eng, ID: "main", Name: "Main"})
	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload failed: %+v", rep)
	}
	if rep.EnginesRefreshed != 1 {
		t.Errorf("EnginesRefreshed = %d, want 1 (nil view state skipped, real engine refreshed)", rep.EnginesRefreshed)
	}
}

// Changing BOTH settings scopes produces the note from the changed scope
// (user first, project fallback) and the changed fallback note.
func TestPinReload_ProjectSettingsDiffNote(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	if rep := orch.Reload(context.Background()); !rep.Applied {
		t.Fatalf("first reload failed: %+v", rep)
	}
	// Project-only change: note comes from the project diff.
	writeFileT(t, filepath.Join(env.work, ".gbot", "settings.json"), `{"permissions":{"ask":["Read(*)"]}}`)
	rep := orch.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("project reload failed: %+v", rep)
	}
	var row *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Path == displaySettingsPath() {
			row = &rep.Files[i]
		}
	}
	if row == nil {
		t.Fatalf("no settings row: %+v", rep.Files)
	}
	if row.Status != StatusReloaded {
		t.Errorf("row.Status = %q, want reloaded", row.Status)
	}
	if !strings.Contains(row.Note, "permissions") {
		t.Errorf("row.Note = %q, want the permissions leaf", row.Note)
	}
	if row.NoteKey != "newSessionsOnly" {
		t.Errorf("row.NoteKey = %q, want newSessionsOnly", row.NoteKey)
	}

	// A byte change with no JSON leaf diff falls back to "changed".
	writeFileT(t, filepath.Join(env.work, ".gbot", "settings.json"), "{\n\"permissions\":{\"ask\":[\"Read(*)\"]}\n}")
	rep2 := orch.Reload(context.Background())
	if !rep2.Applied {
		t.Fatalf("formatting reload failed: %+v", rep2)
	}
	for _, f := range rep2.Files {
		if f.Path == displaySettingsPath() {
			if f.Status != StatusReloaded || f.Note != "changed" {
				t.Errorf("formatting-only row = %+v, want reloaded/changed", f)
			}
		}
	}
}

// Unchanged cycles count every plugin row as unchanged.
func TestPinReload_SecondCycleCountsUnchanged(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	env.writePlugin(t, "pinplug", map[string]string{
		"plugin.json": `{"name":"pinplug","version":"1","repls":"./repl/"}`,
		"repl/p.js":   "var x = 1",
	})
	if rep := orch.Reload(context.Background()); !rep.Applied {
		t.Fatalf("first reload failed: %+v", rep)
	}
	rep := orch.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("second reload failed: %+v", rep)
	}
	if rep.Counts.Reloaded != 0 {
		t.Errorf("Counts.Reloaded = %d, want 0 on the unchanged cycle", rep.Counts.Reloaded)
	}
	// settings + prompt + skills + repl rows ride every cycle as unchanged.
	if rep.Counts.Unchanged != 4 {
		t.Errorf("Counts.Unchanged = %d, want 4 (settings, prompt, skills, repl): %+v", rep.Counts.Unchanged, rep.Files)
	}
}

// MCP rows across a server lifecycle: add → no-row unchanged → change note.
func TestPinReload_McpRowLifecycle(t *testing.T) {
	env := newReloadTestEnv(t, pinSettings)
	orch := env.orchestrator()
	mcpPath := filepath.Join(env.work, ".mcp.json")

	writeFileT(t, mcpPath, `{"mcpServers":{"pinsrv":{"command":"/bin/sleep","args":["1"]}}}`)
	rep := orch.Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("add reload failed: %+v", rep)
	}
	var note string
	for _, f := range rep.Files {
		if f.Path == "MCP" {
			note = f.Note
		}
	}
	if !strings.Contains(note, "1 added") || !strings.Contains(note, "0 removed") || !strings.Contains(note, "0 changed") {
		t.Fatalf("add note = %q, want '1 added · 0 removed · 0 changed'", note)
	}

	// Unchanged cycle: NO MCP row at all.
	rep2 := orch.Reload(context.Background())
	if !rep2.Applied {
		t.Fatalf("unchanged reload failed: %+v", rep2)
	}
	for _, f := range rep2.Files {
		if f.Path == "MCP" {
			t.Fatalf("unchanged cycle emitted an MCP row: %+v", f)
		}
	}

	// Changed server: note reports the change.
	writeFileT(t, mcpPath, `{"mcpServers":{"pinsrv":{"command":"/bin/sleep","args":["2"]}}}`)
	rep3 := orch.Reload(context.Background())
	if !rep3.Applied {
		t.Fatalf("change reload failed: %+v", rep3)
	}
	note3 := ""
	for _, f := range rep3.Files {
		if f.Path == "MCP" {
			note3 = f.Note
		}
	}
	if !strings.Contains(note3, "1 changed") {
		t.Fatalf("change note = %q, want '1 changed'", note3)
	}
}
