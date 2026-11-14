package reload

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
)

// degradedForTest shapes a tier-2 marker for hand-built degraded fixtures.
var degradedForTest = config.Degraded{Reason: "fixture: degraded for test"}

// T16 — tier 3 on reload: a broken settings.json aborts the reload (strict
// Load) and keeps the daemon on the old config. Abort never kills the daemon.
func TestReload_BrokenSettingsKeepsOldConfig(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	writeSettingsFile(t, env.home, `{"providers": not json`)
	rep := o.Reload(context.Background())

	if rep.Applied {
		t.Fatal("reload applied despite unparseable settings.json, want abort")
	}
	if rep.Phase != 0 {
		t.Fatalf("Phase = %d, want 0", rep.Phase)
	}
	if got := o.Env().Provider.Name(); got != "t" {
		t.Fatalf("live provider after abort = %q, want the old provider t (daemon keeps serving)", got)
	}
	if o.Env().Degraded != nil {
		t.Fatalf("Degraded = %+v, want nil (old env untouched)", o.Env().Degraded)
	}
}

// T17 — tier 1 on reload: a default model that matches nothing falls back to
// the provider's first model; the settings row note carries the fallback.
func TestReload_Tier1FallbackApplies(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	writeSettingsFile(t, env.home, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/nope"}}`)
	rep := o.Reload(context.Background())

	if !rep.Applied {
		t.Fatalf("tier-1 reload must apply, got err=%q files=%+v", rep.Err, rep.Files)
	}
	if got := o.Env().Model; got != "m" {
		t.Fatalf("Env().Model = %q, want the first-model fallback m", got)
	}
	if o.Env().Degraded != nil {
		t.Fatalf("Degraded = %+v, want nil (provider alive, only the model fell back)", o.Env().Degraded)
	}
	var settingsRow *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Path == displaySettingsPath() {
			settingsRow = &rep.Files[i]
		}
	}
	if settingsRow == nil {
		t.Fatalf("no settings row: %+v", rep.Files)
	}
	// Both halves must survive the join: the JSON diff leaf AND the tier-1
	// disclosure, separated by "; " (a repair that clobbers either half or
	// drops the separator loses information the settings page shows).
	if !strings.Contains(settingsRow.Note, `model.default`) || !strings.Contains(settingsRow.Note, "falling back") {
		t.Fatalf("settings row Note = %q, want the diff leaf plus the tier-1 fallback note", settingsRow.Note)
	}
	if !strings.Contains(settingsRow.Note, "; default model") {
		t.Fatalf("settings row Note = %q, want the tier-1 note joined after the diff note", settingsRow.Note)
	}
}

// Tier 1b on reload: the default names a provider the settings never
// declare (a ghost) — the reload applies with the first live provider's
// first model and the ghost called out in the settings-row note.
func TestReload_Tier1bGhostDefaultProviderApplies(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	writeSettingsFile(t, env.home, `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"ghost/m"}}`)
	rep := o.Reload(context.Background())

	if !rep.Applied {
		t.Fatalf("tier-1b reload must apply, got err=%q files=%+v", rep.Err, rep.Files)
	}
	if o.Env().Provider == nil || o.Env().Provider.Name() != "t" {
		t.Fatalf("Env().Provider = %v, want the first live provider t", o.Env().Provider)
	}
	if got := o.Env().Model; got != "m" {
		t.Fatalf("Env().Model = %q, want the ghost-provider fallback m", got)
	}
	if o.Env().Degraded != nil {
		t.Fatalf("Degraded = %+v, want nil (a live provider took over)", o.Env().Degraded)
	}
	var settingsRow *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Path == displaySettingsPath() {
			settingsRow = &rep.Files[i]
		}
	}
	if settingsRow == nil {
		t.Fatalf("no settings row: %+v", rep.Files)
	}
	if !strings.Contains(settingsRow.Note, "ghost") || !strings.Contains(settingsRow.Note, "falling back") {
		t.Fatalf("settings row Note = %q, want the ghost named with the fallback", settingsRow.Note)
	}
}

// T18 — tier 2 on reload: no providers at all swaps the env into the
// degraded shape (nullProvider) instead of aborting, and the report carries
// a failed providers row so the settings page shows why.
func TestReload_Tier2DegradedSwap(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()
	// A null-provider engine from the degraded start: the tier-2 reload must
	// not touch it (the recovery gate requires the NEW env to be healthy).
	nullEng := engine.New(&engine.Params{Provider: llm.NewNullProvider(), Model: "", WorkingDir: env.work})
	env.engineMgr.Add(&engine.EngineViewState{Engine: nullEng, ID: "main", Name: "Main", Model: ""})

	writeSettingsFile(t, env.home, `{"providers":[],"model":{"default":"t/m"}}`)
	rep := o.Reload(context.Background())

	if !rep.Applied {
		t.Fatalf("tier-2 reload must apply (degraded), got err=%q files=%+v", rep.Err, rep.Files)
	}
	if _, ok := o.Env().Provider.(*llm.NullProvider); !ok {
		t.Fatalf("Env().Provider = %T, want *llm.NullProvider", o.Env().Provider)
	}
	if o.Env().Degraded == nil || !strings.Contains(o.Env().Degraded.Reason, "no providers") {
		t.Fatalf("Env().Degraded = %+v, want the tier-2 reason", o.Env().Degraded)
	}
	if vs := env.engineMgr.Get("main"); vs == nil || vs.Model != "" {
		t.Fatalf("null engine view state Model = %+v, want untouched empty (tier-2 swap runs no surgery)", vs)
	}
	var provRow *FileEntry
	for i := range rep.Files {
		if rep.Files[i].Path == "providers" {
			provRow = &rep.Files[i]
		}
	}
	if provRow == nil {
		t.Fatalf("no providers row: %+v", rep.Files)
	}
	if provRow.Status != StatusFailed {
		t.Fatalf("providers row Status = %q, want failed", provRow.Status)
	}
	if !strings.Contains(provRow.ErrLine, "no providers") {
		t.Fatalf("providers row ErrLine = %q, want the tier-2 reason", provRow.ErrLine)
	}
	if rep.Counts.Failed != 1 {
		t.Fatalf("Counts.Failed = %d, want exactly 1 (the providers row)", rep.Counts.Failed)
	}
}

// The tier-1 fallback note rides EVERY cycle while the default stays broken:
// on a no-diff reload the settings row is Unchanged but still discloses the
// standing fallback (without the append, the note would be empty).
func TestReload_Tier1NoteOnUnchangedCycle(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	o := env.orchestrator()

	broken := `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}],"model":{"default":"t/nope"}}`
	writeSettingsFile(t, env.home, broken)
	if rep := o.Reload(context.Background()); !rep.Applied {
		t.Fatalf("first reload must apply: %q", rep.Err)
	}
	// Second reload: nothing changed on disk.
	rep2 := o.Reload(context.Background())
	if !rep2.Applied {
		t.Fatalf("second reload must apply: %q", rep2.Err)
	}
	var settingsRow *FileEntry
	for i := range rep2.Files {
		if rep2.Files[i].Path == displaySettingsPath() {
			settingsRow = &rep2.Files[i]
		}
	}
	if settingsRow == nil {
		t.Fatalf("no settings row: %+v", rep2.Files)
	}
	if settingsRow.Status != StatusUnchanged {
		t.Fatalf("settings row Status = %q, want unchanged on the second cycle", settingsRow.Status)
	}
	if !strings.Contains(settingsRow.Note, "falling back") {
		t.Fatalf("settings row Note = %q, want the standing tier-1 fallback disclosure", settingsRow.Note)
	}
}

// T19 — recovery surgery: a degraded env that reloads back to health gets
// every null-provider engine swapped to the new provider/model/capabilities,
// and its view state model updated. The reverse direction (healthy→degraded)
// leaves live engines untouched by design.
func TestReload_RecoverySwapsNullEngines(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	// Degraded initial env, hand-built: holder holds the null provider and
	// the tier-2 marker (what a degraded Start would have produced).
	init := env.holder.Load()
	init.Provider = llm.NewNullProvider()
	init.Model = ""
	init.Degraded = &degradedForTest
	// Empty provider map completes the tier-2 shape a degraded Start builds.
	init.ProviderMap = map[string]llm.Provider{}
	env.holder.Store(init)

	nullEng := engine.New(&engine.Params{Provider: llm.NewNullProvider(), Model: "", WorkingDir: env.work})
	// Engine order pins the scan's continue handling: a nil-engine view
	// state and a non-null live engine must not stop the loop before it
	// reaches the null engine.
	env.engineMgr.Add(&engine.EngineViewState{ID: "ghost"})
	liveEng := newReloadEngine(t, env)
	env.engineMgr.Add(&engine.EngineViewState{Engine: liveEng, ID: "aux", Name: "Aux", Model: "stub/m"})
	env.engineMgr.Add(&engine.EngineViewState{Engine: nullEng, ID: "main", Name: "Main", Model: ""})

	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("recovery reload must apply, got err=%q files=%+v", rep.Err, rep.Files)
	}
	if got := nullEng.Provider().Name(); got != "t" {
		t.Fatalf("null engine provider after recovery = %q, want t", got)
	}
	if got := nullEng.Model(); got != "m" {
		t.Fatalf("null engine model after recovery = %q, want m", got)
	}
	if got := nullEng.MaxTokens(); got != 32768 {
		t.Fatalf("null engine max tokens after recovery = %d, want settingsA's 32k (32768)", got)
	}
	// The capability sync rides along: settingsA's context is 128k.
	if got := nullEng.ContextWindow(); got != 131072 {
		t.Fatalf("null engine context window after recovery = %d, want 128k (131072)", got)
	}
	// Modalities re-resolve from the recovered model's config: default
	// input is text-only for settingsA's plain m.
	if !nullEng.SupportsModality("text") || nullEng.SupportsModality("image") {
		t.Fatal("recovered engine modalities = want text-only from settingsA's m")
	}
	vs := env.engineMgr.Get("main")
	if vs == nil || vs.Model != "t/m" {
		t.Fatalf("view state Model = %+v, want t/m", vs)
	}
	// The recovery persists the repaired view states.
	meta, err := short.ReadWorkspaceMeta(env.work)
	if err != nil {
		t.Fatalf("read meta after recovery: %v", err)
	}
	found := false
	for _, em := range meta.Engines {
		if em.ID == "main" && em.Model == "t/m" {
			found = true
		}
	}
	if !found {
		t.Fatalf("meta.json engines = %+v, want main carrying t/m", meta.Engines)
	}
	// The already-healthy engine keeps its construction-time provider.
	if got := liveEng.Provider().Name(); got != "stub" {
		t.Fatalf("live engine provider was touched by recovery: %q, want stub", got)
	}
	if env.holder.Load().Degraded != nil {
		t.Fatal("recovered env still carries Degraded, want nil")
	}
}

// A healthy→healthy reload must not run recovery surgery: the gate requires
// the OLD env to be degraded, so an engine a user deliberately parked on the
// null provider keeps it across ordinary reloads.
func TestReload_HealthyReloadLeavesNullEngineAlone(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	nullEng := engine.New(&engine.Params{Provider: llm.NewNullProvider(), Model: "", WorkingDir: env.work})
	env.engineMgr.Add(&engine.EngineViewState{Engine: nullEng, ID: "parked", Name: "Parked", Model: ""})

	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply, got err=%q", rep.Err)
	}
	if _, ok := nullEng.Provider().(*llm.NullProvider); !ok {
		t.Fatalf("parked engine provider = %T, want to stay NullProvider on a healthy reload", nullEng.Provider())
	}
	if nullEng.Model() != "" {
		t.Fatalf("parked engine model = %q, want untouched empty", nullEng.Model())
	}
	// No recovery happened, so no recovery persist either: meta.json must
	// not appear on a plain healthy reload.
	if _, err := os.Stat(filepath.Join(env.work, "meta.json")); !os.IsNotExist(err) {
		t.Fatalf("meta.json written by a healthy reload without recovery (err=%v)", err)
	}
}

// Healthy→degraded reload: live engines keep their providers (their queries
// fail naturally at the old provider, the user's choice is respected).
func TestReload_DegradeKeepsLiveEngines(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	liveEng := newReloadEngine(t, env)
	env.engineMgr.Add(&engine.EngineViewState{Engine: liveEng, ID: "main", Name: "Main", Model: "t/m"})

	writeSettingsFile(t, env.home, `{"providers":[],"model":{"default":"t/m"}}`)
	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("degrade reload must apply, got err=%q", rep.Err)
	}
	if got := liveEng.Provider().Name(); got != "stub" {
		t.Fatalf("live engine provider = %q, want stub (untouched on degrade)", got)
	}
	if _, ok := env.holder.Load().Provider.(*llm.NullProvider); !ok {
		t.Fatalf("new env provider = %T, want NullProvider", env.holder.Load().Provider)
	}
}

// Degraded→degraded reload: the recovery gate needs the NEW env healthy, so
// a still-degraded reload must not run surgery even on null-provider engines
// (the swap would point their view state at "none/").
func TestReload_DegradedToDegradedLeavesNullEngines(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	init := env.holder.Load()
	init.Provider = llm.NewNullProvider()
	init.Model = ""
	init.Degraded = &degradedForTest
	init.ProviderMap = map[string]llm.Provider{}
	env.holder.Store(init)

	nullEng := engine.New(&engine.Params{Provider: llm.NewNullProvider(), Model: "", WorkingDir: env.work})
	env.engineMgr.Add(&engine.EngineViewState{Engine: nullEng, ID: "main", Name: "Main", Model: ""})

	writeSettingsFile(t, env.home, `{"providers":[],"model":{"default":"t/m"}}`)
	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("degraded reload must apply, got err=%q", rep.Err)
	}
	if vs := env.engineMgr.Get("main"); vs == nil || vs.Model != "" {
		t.Fatalf("null engine view state Model = %+v, want untouched empty", vs)
	}
	if _, ok := nullEng.Provider().(*llm.NullProvider); !ok {
		t.Fatalf("null engine provider = %T, want to stay NullProvider", nullEng.Provider())
	}
}

// Recovery persists only when it actually swapped something: a
// degraded→healthy reload with no null-provider engines writes no meta.json.
func TestReload_RecoveryWithoutNullEnginesDoesNotPersist(t *testing.T) {
	env := newReloadTestEnv(t, settingsA)
	init := env.holder.Load()
	init.Provider = llm.NewNullProvider()
	init.Model = ""
	init.Degraded = &degradedForTest
	init.ProviderMap = map[string]llm.Provider{}
	env.holder.Store(init)

	// Only non-null engines exist: recovery finds nothing to swap.
	env.engineMgr.Add(&engine.EngineViewState{Engine: newReloadEngine(t, env), ID: "aux", Name: "Aux", Model: "stub/m"})

	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply, got err=%q", rep.Err)
	}
	if env.holder.Load().Degraded != nil {
		t.Fatal("recovered env still carries Degraded, want nil")
	}
	if _, err := os.Stat(filepath.Join(env.work, "meta.json")); !os.IsNotExist(err) {
		t.Fatalf("meta.json written despite no recovered engines (err=%v)", err)
	}
}

// The recovery's modality sync is observable on a model that declares image
// input: the null engine adopts the recovered model's modalities, not the
// conservative text-only default engine.New installs.
func TestReload_RecoverySyncsModalities(t *testing.T) {
	// settingsA's m is text-only; this variant declares image input.
	withImage := `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"32k","context":"128k","input":["text","image"]}}}],"model":{"default":"t/m"}}`
	env := newReloadTestEnv(t, withImage)
	init := env.holder.Load()
	init.Degraded = &degradedForTest
	env.holder.Store(init)

	nullEng := engine.New(&engine.Params{Provider: llm.NewNullProvider(), Model: "", WorkingDir: env.work})
	env.engineMgr.Add(&engine.EngineViewState{Engine: nullEng, ID: "main", Name: "Main", Model: ""})

	rep := env.orchestrator().Reload(context.Background())
	if !rep.Applied {
		t.Fatalf("reload must apply, got err=%q", rep.Err)
	}
	if !nullEng.SupportsModality("image") {
		t.Fatal("recovered engine lacks image modality from the settings model")
	}
}
