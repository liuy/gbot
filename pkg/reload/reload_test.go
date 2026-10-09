package reload

import (
	"context"
	"encoding/json"
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
		"plugin.json": `{"name":"broken","version":"1.0.0","mcpServers":"./mcp.json"}`,
		"mcp.json":    `{"mcpServers":{"s":{"command":}}}`,
	})
	rep := o.Reload(context.Background())

	if rep.Applied {
		t.Fatal("reload must abort on an invalid plugin mcp.json")
	}
	if rep.Phase != 1 {
		t.Errorf("Phase = %d, want 1 (plugins)", rep.Phase)
	}
	found := false
	for _, row := range rep.Files {
		if row.Status == StatusFailed && strings.Contains(row.Path, filepath.Join("plugins", "broken", "mcp.json")) {
			found = true
			if row.ErrLine == "" {
				t.Error("plugin mcp failed row must carry ErrLine")
			}
		}
	}
	if !found {
		t.Fatalf("no failed row naming plugins/broken/mcp.json: %+v", rep.Files)
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
