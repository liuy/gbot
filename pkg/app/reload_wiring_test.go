package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/reload"
)

// reloadHome isolates HOME with two offline providers; the default model pins
// max_tokens + thinking explicitly, and t2/m2 carries a different max_tokens
// so provider-specific factory resolution is observable.
func reloadHome(t *testing.T, maxTokens string) string {
	t.Helper()
	home := t.TempDir()
	settings := `{"providers":[` +
		`{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"` + maxTokens + `","context":"128k","thinking":"high"}}},` +
		`{"name":"t2","type":"openai","url":"http://127.0.0.1:2","keys":["k2"],"models":{"m2":{"max_tokens":"8k","context":"128k"}}}` +
		`],"model":{"default":"t/m"}}`
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir .gbot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return home
}

// TestStart_ReloadSwapsEnvForNewEnginesOnly pins the env-holder wiring of
// the production engine factory: a reload swaps the holder, live engines
// keep their construction-time params, engines built afterwards pick up the
// new ones — and per-provider resolution + unknown-provider fallback follow
// the swapped env.
func TestStart_ReloadSwapsEnvForNewEnginesOnly(t *testing.T) {
	t.Setenv("HOME", reloadHome(t, "32k"))
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")
	startInTempProject(t)

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst.Reloader == nil || inst.EnvHolder == nil {
		t.Fatalf("Instance must expose Reloader and EnvHolder, got %+v %+v", inst.Reloader, inst.EnvHolder)
	}

	engA, _, err := inst.EngineFactory("eng-a", "A", "t", "m")
	if err != nil {
		t.Fatalf("engine A: %v", err)
	}
	defer engA.Close()
	if got := engA.MaxTokens(); got != 32768 {
		t.Fatalf("engine A initial MaxTokens = %d, want 32768", got)
	}
	if engA.Provider() == nil || engA.Provider().Name() != "t" {
		t.Fatalf("engine A provider = %v, want t", engA.Provider())
	}
	// Provider-specific resolution: t2/m2 must resolve through the swapped
	// env's Providers, not fall back to the primary.
	engC, _, err := inst.EngineFactory("eng-c", "C", "t2", "m2")
	if err != nil {
		t.Fatalf("engine C: %v", err)
	}
	defer engC.Close()
	if got := engC.MaxTokens(); got != 8192 {
		t.Errorf("engine C (t2/m2) MaxTokens = %d, want 8192 (per-provider model config)", got)
	}
	if engC.Provider() == nil || engC.Provider().Name() != "t2" {
		t.Errorf("engine C provider = %v, want t2 (resolved through env.ProviderMap)", engC.Provider())
	}
	if got := engA.Thinking(); got != llm.EffortHigh {
		t.Errorf("engine A Thinking = %q, want high (params carry env.ModelThinking)", got)
	}
	if _, hasRecall := engA.AllTools()["Recall"]; !hasRecall {
		t.Error("engine A must have the Recall tool (deps.ShortStore wired into the factory build)")
	}
	if engA.SessionMemory() == nil {
		t.Error("engine A must have session memory wired (store non-nil, session notes enabled, context window > 0)")
	}
	// Unknown provider falls back to the primary provider + its params.
	engD, _, err := inst.EngineFactory("eng-d", "D", "missing", "m")
	if err != nil {
		t.Fatalf("engine D: %v", err)
	}
	defer engD.Close()
	if got := engD.MaxTokens(); got != 32768 {
		t.Errorf("engine D (unknown provider) MaxTokens = %d, want 32768 (primary fallback)", got)
	}
	if engD.Provider() == nil || engD.Provider().Name() != "t" {
		t.Errorf("engine D provider = %v, want fallback to t", engD.Provider())
	}

	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gbot", "settings.json"),
		[]byte(`{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"16k","context":"128k","thinking":"high"}}},{"name":"t2","type":"openai","url":"http://127.0.0.1:2","keys":["k2"],"models":{"m2":{"max_tokens":"8k","context":"128k"}}}],"model":{"default":"t/m"}}`), 0o600); err != nil {
		t.Fatalf("rewrite settings: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	rep := inst.Reloader.Reload(context.Background())
	if rep == nil || !rep.Applied {
		t.Fatalf("reload must apply, got %+v", rep)
	}
	// The refreshed prompt must be built from the real working dir — a
	// mis-wired orchestrator baseline would silently rebuild against "".
	// Checked on a MANAGED engine (phase 4 refreshes those, not engA).
	managedRefreshed := 0
	for _, vs := range inst.EngineMgr.List() {
		if vs.Engine == nil {
			continue
		}
		if sp := vs.Engine.DumpAPIRequest().SystemPrompt; strings.Contains(sp, "workspace="+wd) {
			managedRefreshed++
		}
	}
	if managedRefreshed == 0 {
		t.Errorf("no managed engine carries workspace=%s after reload", wd)
	}
	if rep.EnginesRefreshed < 1 {
		t.Errorf("EnginesRefreshed = %d, want at least the restored engine refreshed", rep.EnginesRefreshed)
	}
	if got := engA.MaxTokens(); got != 32768 {
		t.Errorf("live engine A MaxTokens after reload = %d, want 32768 (live engines keep old params)", got)
	}

	engB, _, err := inst.EngineFactory("eng-b", "B", "t", "m")
	if err != nil {
		t.Fatalf("engine B: %v", err)
	}
	defer engB.Close()
	if got := engB.MaxTokens(); got != 16384 {
		t.Errorf("new engine B MaxTokens = %d, want 16384 (factory reads the swapped env)", got)
	}
	if _, hasRecall := engB.AllTools()["Recall"]; !hasRecall {
		t.Error("new engine B must have the Recall tool (per-engine deps carry the store)")
	}
	// The post-reload env's tool prompts must match the startup harvest —
	// the orchestrator's Deps.ShortStore/LSPReg wiring decides whether the
	// harvest sees the same tool set a fresh engine gets.
	if got := len(inst.EnvHolder.Load().ToolPrompts); got != len(inst.ToolPrompts) || got == 0 {
		t.Errorf("post-reload ToolPrompts = %d entries, want the startup parity (%d)", got, len(inst.ToolPrompts))
	}
}

// TestStart_EnvHolderMirrorsStartupState pins that the initial Env carries
// exactly what Start computed — the reload baseline and every post-reload
// factory build start from a faithful snapshot.
func TestStart_EnvHolderMirrorsStartupState(t *testing.T) {
	t.Setenv("HOME", reloadHome(t, "32k"))
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")
	startInTempProject(t)

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	env := inst.EnvHolder.Load()
	if inst.SystemPrompt == "" || !strings.Contains(inst.SystemPrompt, "workspace=") {
		t.Errorf("startup SystemPrompt must be the built prompt, got %.200s", inst.SystemPrompt)
	}
	if env.Cfg != inst.Cfg {
		t.Error("env.Cfg must be the startup cfg (pointer identity)")
	}
	if env.Provider == nil || env.Provider.Name() != "t" {
		t.Errorf("env.Provider = %v, want t", env.Provider)
	}
	for _, name := range []string{"t", "t2"} {
		if _, ok := env.ProviderMap[name]; !ok {
			t.Errorf("env.ProviderMap missing %q", name)
		}
	}
	if env.Model != "m" {
		t.Errorf("env.Model = %q, want m", env.Model)
	}
	if env.PrimaryProviderCfg == nil || env.PrimaryProviderCfg.Name != "t" {
		t.Errorf("env.PrimaryProviderCfg = %+v, want t", env.PrimaryProviderCfg)
	}
	if env.ModelThinking["m"] != llm.EffortHigh {
		t.Errorf("env.ModelThinking[m] = %q, want high", env.ModelThinking["m"])
	}
	if env.SkillListing != inst.SkillListing {
		t.Error("env.SkillListing must equal the startup listing")
	}
	if len(env.ToolPrompts) != len(inst.ToolPrompts) || len(env.ToolPrompts) == 0 {
		t.Errorf("env.ToolPrompts = %d entries, want the startup tool prompts (%d)", len(env.ToolPrompts), len(inst.ToolPrompts))
	}
	if !reflect.DeepEqual(env.SkillCmds, inst.SkillCmdsForTUI) {
		t.Errorf("env.SkillCmds = %d entries, want the startup SkillCmdsForTUI (%d)", len(env.SkillCmds), len(inst.SkillCmdsForTUI))
	}
	if env.McpReg != nil {
		t.Errorf("env.McpReg = %v, want nil (no servers configured)", env.McpReg)
	}
	if env.PermissionChecker != nil {
		t.Errorf("env.PermissionChecker = %v, want nil (no rules configured)", env.PermissionChecker)
	}
}

// TestStart_ReloadRouteRegistered proves the wui face end to end: the route
// is mounted on the live listener and answers with a reload report. The
// port-0 tests cannot see this (the addr is a pipe), so this binds a real
// port like TestStart_PortOpenImpliesRoutesReady.
func TestStart_ReloadRouteRegistered(t *testing.T) {
	t.Setenv("HOME", reloadHome(t, "32k"))
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)

	startErr := make(chan error, 1)
	go func() {
		_, err := Start(Options{NoTUI: true})
		startErr <- err
	}()
	base := "http://127.0.0.1:" + port
	timeout := time.After(5 * time.Second)
	for {
		resp, err := http.Post(base+"/api/settings/reload", "application/json", strings.NewReader("{}"))
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if closeErr != nil {
				t.Logf("close reload body: %v", closeErr)
			}
			if readErr != nil {
				t.Fatalf("read reload body: %v", readErr)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("POST /api/settings/reload status = %d, want 200", resp.StatusCode)
			}
			var rep reload.Report
			if err := json.Unmarshal(body, &rep); err != nil {
				t.Fatalf("decode reload report: %v", err)
			}
			if !rep.Applied {
				t.Fatalf("reload via route must apply, got %+v", rep)
			}
			// No file changed since Start: both invariant rows must read
			// unchanged — a broken orchestrator baseline (wrong dirs) would
			// flip them to reloaded.
			settingsUnchanged, promptUnchanged := false, false
			for _, row := range rep.Files {
				if strings.Contains(row.Path, "settings.json") && row.Status == reload.StatusUnchanged {
					settingsUnchanged = true
				}
				if row.Path == "MEMORY.md · CLAUDE.md" && row.Status == reload.StatusUnchanged {
					promptUnchanged = true
				}
			}
			if !settingsUnchanged || !promptUnchanged {
				t.Fatalf("no-op reload rows: settingsUnchanged=%v promptUnchanged=%v, files=%+v", settingsUnchanged, promptUnchanged, rep.Files)
			}
			break
		}
		select {
		case <-timeout:
			t.Fatalf("route never answered: %v", err)
		case serr := <-startErr:
			if serr != nil {
				t.Fatalf("Start failed: %v", serr)
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
}
