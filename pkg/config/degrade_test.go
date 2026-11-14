package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// degProvider builds a chat provider fixture whose URL never gets dialed:
// buildProviderMap only constructs client objects, and fixtures never set
// FreeFetched so refreshFreeModels skips them.
func degProvider(name, key string, models ...string) Provider {
	return Provider{
		Name:   name,
		URL:    "http://127.0.0.1:1",
		Keys:   []string{key},
		Type:   ProviderTypeOpenAI,
		Models: newModelSet(models...),
	}
}

// assertTier2 pins the full tier-2 shape: no live providers, NullProvider,
// empty model, zero-value primary cfg, and the canonical reason.
func assertTier2(t *testing.T, boot BootResult) {
	t.Helper()
	if len(boot.Providers) != 0 {
		t.Fatalf("Providers = %v, want empty", boot.Providers)
	}
	if _, ok := boot.Provider.(*llm.NullProvider); !ok {
		t.Fatalf("Provider = %T, want *llm.NullProvider", boot.Provider)
	}
	if boot.Model != "" {
		t.Fatalf("Model = %q, want empty", boot.Model)
	}
	if boot.Primary == nil {
		t.Fatal("Primary = nil, want zero-value &Provider{}")
	}
	if boot.Primary.Name != "" {
		t.Fatalf("Primary.Name = %q, want empty", boot.Primary.Name)
	}
	if boot.Degraded == nil || !strings.Contains(boot.Degraded.Reason, "no providers") {
		t.Fatalf("Degraded = %+v, want reason containing %q", boot.Degraded, "no providers")
	}
}

func TestResolveBoot_Healthy(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model:     ModelSpec{"default": "t/m2"},
		Providers: []Provider{degProvider("t", "k", "m1", "m2")},
	}
	boot := ResolveBoot(cfg)
	if boot.Degraded != nil {
		t.Fatalf("Degraded = %+v, want nil on healthy boot", boot.Degraded)
	}
	if boot.Provider == nil || boot.Provider.Name() != "t" {
		t.Fatalf("Provider = %v, want t", boot.Provider)
	}
	if boot.Model != "m2" {
		t.Fatalf("Model = %q, want m2", boot.Model)
	}
	if len(boot.Notes) != 0 {
		t.Fatalf("Notes = %v, want none", boot.Notes)
	}
	if len(boot.Providers) != 1 {
		t.Fatalf("Providers = %v, want exactly t", boot.Providers)
	}
	if _, ok := boot.Providers["t"]; !ok {
		t.Fatalf("Providers = %v, want t present", boot.Providers)
	}
	if boot.Primary == nil || boot.Primary.Name != "t" {
		t.Fatalf("Primary = %+v, want t", boot.Primary)
	}
}

// Tier 1a: the default model name misses even the fuzzy fallback inside a
// live provider — fall back to that provider's first declared model.
// Fixture self-check: "m1"/"m2" contain no subsequence of "nope", so
// FindClosestMatch cannot rescue the default and the tier is truly 1a.
func TestResolveBoot_Tier1a_FallsBackToFirstModel(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model:     ModelSpec{"default": "t/nope"},
		Providers: []Provider{degProvider("t", "k", "m1", "m2")},
	}
	boot := ResolveBoot(cfg)
	if boot.Degraded != nil {
		t.Fatalf("Degraded = %+v, want nil (provider is alive)", boot.Degraded)
	}
	if boot.Provider == nil || boot.Provider.Name() != "t" {
		t.Fatalf("Provider = %v, want to stay on t", boot.Provider)
	}
	if boot.Model != "m1" {
		t.Fatalf("Model = %q, want first declared model m1", boot.Model)
	}
	if len(boot.Notes) != 1 {
		t.Fatalf("Notes = %v, want exactly one warning", boot.Notes)
	}
	if !strings.Contains(boot.Notes[0], "nope") || !strings.Contains(boot.Notes[0], "m1") {
		t.Fatalf("Notes[0] = %q, want it to name both the dead default and the fallback", boot.Notes[0])
	}
}

// Tier 1b, entry one: the default names a provider that does not exist in
// cfg — fall back to the first provider alive in the provider map.
func TestResolveBoot_Tier1b_UnknownProvider(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model:     ModelSpec{"default": "nope/m"},
		Providers: []Provider{degProvider("t", "k", "m1", "m2")},
	}
	boot := ResolveBoot(cfg)
	if boot.Degraded != nil {
		t.Fatalf("Degraded = %+v, want nil (a sibling is alive)", boot.Degraded)
	}
	if boot.Provider == nil || boot.Provider.Name() != "t" {
		t.Fatalf("Provider = %v, want t", boot.Provider)
	}
	if boot.Model != "m1" {
		t.Fatalf("Model = %q, want m1", boot.Model)
	}
	if len(boot.Notes) != 1 {
		t.Fatalf("Notes = %v, want exactly one warning", boot.Notes)
	}
	// The named-provider wording pins the provName derivation: a lost
	// ParseModel result would degrade this to the bare-model wording.
	if !strings.Contains(boot.Notes[0], `provider "nope" not available`) {
		t.Fatalf("Notes[0] = %q, want the named-provider wording", boot.Notes[0])
	}
}

// Tier 1b, entry two: a bare default model that no provider fuzzy-matches.
// "zzz" shares no subsequence with "m1"/"m2", so resolution errors with an
// empty provider name.
func TestResolveBoot_Tier1b_BareModelUnmatched(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model:     ModelSpec{"default": "zzz"},
		Providers: []Provider{degProvider("t", "k", "m1", "m2")},
	}
	boot := ResolveBoot(cfg)
	if boot.Degraded != nil {
		t.Fatalf("Degraded = %+v, want nil", boot.Degraded)
	}
	if boot.Provider == nil || boot.Provider.Name() != "t" {
		t.Fatalf("Provider = %v, want t", boot.Provider)
	}
	if boot.Model != "m1" {
		t.Fatalf("Model = %q, want m1", boot.Model)
	}
	if len(boot.Notes) != 1 || !strings.Contains(boot.Notes[0], "zzz") {
		t.Fatalf("Notes = %v, want one warning naming the dead default", boot.Notes)
	}
	// The bare-model wording: provName is empty, so the note must say the
	// default matched no provider at all.
	if !strings.Contains(boot.Notes[0], "not found in any provider") {
		t.Fatalf("Notes[0] = %q, want the bare-model wording", boot.Notes[0])
	}
}

// Tier 1b via a dead-key default provider: ResolveModel succeeds against
// cfg (the provider is declared) but the provider map never contains it, so
// the boot falls back to the first surviving sibling — mirroring
// CreateAllProviders' own skip-and-degrade treatment of broken keys.
func TestResolveBoot_Tier1b_DeadKeyProviderFallsBackToSibling(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model: ModelSpec{"default": "p2/m2"},
		Providers: []Provider{
			degProvider("p1", "k1", "m1"),
			degProvider("p2", "$GBOT_PIN_UNSET_KEY", "m2"),
		},
	}
	boot := ResolveBoot(cfg)
	if boot.Degraded != nil {
		t.Fatalf("Degraded = %+v, want nil (p1 is alive)", boot.Degraded)
	}
	if boot.Provider == nil || boot.Provider.Name() != "p1" {
		t.Fatalf("Provider = %v, want p1", boot.Provider)
	}
	if boot.Model != "m1" {
		t.Fatalf("Model = %q, want m1", boot.Model)
	}
	if len(boot.Notes) != 1 || !strings.Contains(boot.Notes[0], "p2") {
		t.Fatalf("Notes = %v, want one warning naming p2", boot.Notes)
	}
	if boot.Primary == nil || boot.Primary.Name != "p1" {
		t.Fatalf("Primary = %+v, want p1", boot.Primary)
	}
}

// Tier 1a must STAY on the default's provider even when an earlier-declared
// sibling is alive: falling through to the tier-1b scan would pick that
// sibling instead. This fixture makes the two tiers' outcomes differ, so a
// gutted 1a branch cannot hide behind 1b's fallback.
func TestResolveBoot_Tier1a_StaysOnProviderWithSiblingPresent(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model: ModelSpec{"default": "t/nope"},
		Providers: []Provider{
			degProvider("s", "ks", "s1"),
			degProvider("t", "kt", "m1", "m2"),
		},
	}
	boot := ResolveBoot(cfg)
	if boot.Provider == nil || boot.Provider.Name() != "t" {
		t.Fatalf("Provider = %v, want t (tier 1a keeps the user's provider)", boot.Provider)
	}
	if boot.Model != "m1" {
		t.Fatalf("Model = %q, want t's first model m1, not sibling s/s1", boot.Model)
	}
	if len(boot.Providers) != 2 {
		t.Fatalf("Providers = %v, want both live providers in the map", boot.Providers)
	}
	if boot.Primary == nil || boot.Primary.Name != "t" {
		t.Fatalf("Primary = %+v, want t", boot.Primary)
	}
}

// Tier 1b scans in declaration order PAST dead providers: a dead-key
// provider listed first must not stop the scan (a continue-turned-break
// would strand the boot on the unreachable tier-2 fallback).
func TestResolveBoot_Tier1b_DeadProviderListedFirst(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model: ModelSpec{"default": "p2/m2"},
		Providers: []Provider{
			degProvider("p2", "$GBOT_PIN_UNSET_KEY", "m2"),
			degProvider("p1", "k1", "m1"),
		},
	}
	boot := ResolveBoot(cfg)
	if boot.Provider == nil || boot.Provider.Name() != "p1" {
		t.Fatalf("Provider = %v, want p1 (scan must skip past the dead p2)", boot.Provider)
	}
	if boot.Model != "m1" {
		t.Fatalf("Model = %q, want m1", boot.Model)
	}
	if len(boot.Providers) != 1 {
		t.Fatalf("Providers = %v, want only the live p1 in the map", boot.Providers)
	}
}

func TestResolveBoot_Tier2_AllKeysBroken(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model: ModelSpec{"default": "p1/m1"},
		Providers: []Provider{
			degProvider("p1", "$GBOT_PIN_UNSET_KEY", "m1"),
		},
	}
	assertTier2(t, ResolveBoot(cfg))
}

func TestResolveBoot_Tier2_EmptyProviderList(t *testing.T) {
	t.Parallel()
	assertTier2(t, ResolveBoot(&Config{Model: ModelSpec{"default": "t/m1"}}))
}

// Audio providers are STT-only and never enter the provider map: an
// audio-only config degrades exactly like an empty one.
func TestResolveBoot_Tier2_AudioOnly(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model: ModelSpec{"default": "a/m1"},
		Providers: []Provider{{
			Name:   "a",
			URL:    "http://127.0.0.1:1",
			Keys:   []string{"k"},
			Type:   ProviderTypeAudio,
			Models: newModelSet("m1"),
		}},
	}
	assertTier2(t, ResolveBoot(cfg))
}

func writeDegSettings(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// degLoadEnv isolates HOME (user settings) and CWD (project settings).
func degLoadEnv(t *testing.T) (userPath, projectPath string) {
	t.Helper()
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(project)
	return filepath.Join(home, ".gbot", "settings.json"), filepath.Join(project, ".gbot", "settings.json")
}

func TestLoadForgiving_BrokenUserFile(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, `{"providers":`)

	cfg, degraded := LoadForgiving()
	if cfg == nil {
		t.Fatal("cfg = nil, want a usable default config")
	}
	if len(cfg.Providers) != 0 {
		t.Fatalf("Providers = %v, want none from a broken file", cfg.Providers)
	}
	if degraded == nil {
		t.Fatal("Degraded = nil, want parse failure report")
	}
	if !strings.Contains(degraded.Reason, userPath) {
		t.Fatalf("Reason = %q, want it to contain the offending path %s", degraded.Reason, userPath)
	}
	if !strings.Contains(degraded.Reason, "line") {
		t.Fatalf("Reason = %q, want a line number", degraded.Reason)
	}
}

func TestLoadForgiving_BrokenProjectFilePartialRescue(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, `{"providers":[{"name":"u","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"um":{}}}]}`)
	writeDegSettings(t, filepath.Join(".gbot", "settings.json"), `{"providers":`)

	cfg, degraded := LoadForgiving()
	if degraded == nil {
		t.Fatal("Degraded = nil, want the broken project file reported")
	}
	// The project file is addressed the same way Load() addresses it —
	// relative to CWD — so the report carries that exact path.
	if !strings.Contains(degraded.Reason, ".gbot/settings.json") {
		t.Fatalf("Reason = %q, want it to contain the project settings path", degraded.Reason)
	}
	// Only the broken file is reported; the good user file must stay unmentioned.
	if strings.Contains(degraded.Reason, userPath) {
		t.Fatalf("Reason = %q, want only the broken project file reported", degraded.Reason)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "u" {
		t.Fatalf("Providers = %v, want the good user file's provider u rescued", cfg.Providers)
	}
}

// The half-decode pollution path: a file whose first provider element is
// complete but whose second element is truncated. json.Unmarshal has already
// written the first element into the destination when the error fires, so a
// naive single-pass merge would leak provider "t" from a file that must be
// skipped wholesale.
func TestLoadForgiving_HalfDecodedElementDoesNotLeak(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, `{"providers":[{"name":"u","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"um":{}}}]}`)
	writeDegSettings(t, filepath.Join(".gbot", "settings.json"), `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m1":{}}},{"name":`)

	cfg, degraded := LoadForgiving()
	if degraded == nil {
		t.Fatal("Degraded = nil, want the truncated project file reported")
	}
	if !strings.Contains(degraded.Reason, ".gbot/settings.json") {
		t.Fatalf("Reason = %q, want it to contain the project settings path", degraded.Reason)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "u" {
		t.Fatalf("Providers = %v, want only u — the truncated file's partial element t must not leak", cfg.Providers)
	}
}

func TestLoadForgiving_AllFilesGood(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, `{"theme":"dark"}`)

	cfg, degraded := LoadForgiving()
	if degraded != nil {
		t.Fatalf("Degraded = %+v, want nil", degraded)
	}
	if cfg.Theme != "dark" {
		t.Fatalf("Theme = %q, want dark (file must still merge)", cfg.Theme)
	}
}

func TestLoadForgiving_SettingsPathIsDirectory(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	if err := os.MkdirAll(userPath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg, degraded := LoadForgiving()
	if cfg == nil {
		t.Fatal("cfg = nil, want a usable default config")
	}
	if degraded == nil {
		t.Fatal("Degraded = nil, want the EISDIR read error reported")
	}
	if !strings.Contains(degraded.Reason, userPath) {
		t.Fatalf("Reason = %q, want it to contain the path %s", degraded.Reason, userPath)
	}
}

func TestLoadForgiving_ReasonCarriesLineNumber(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, "{\n\"providers\": tru}")

	_, degraded := LoadForgiving()
	if degraded == nil {
		t.Fatal("Degraded = nil, want a syntax error report")
	}
	if !strings.Contains(degraded.Reason, "(line 2)") {
		t.Fatalf("Reason = %q, want %q for an error on the second line", degraded.Reason, "(line 2)")
	}
}

// ResolveBoot runs the same free-model refresh CreateAllProviders does: a
// provider with a fetch ledger must see its stale ids dropped before the
// provider map is built.
func TestResolveBoot_RefreshesFreeModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id":                   "fresh:free",
				"context_length":       32768,
				"pricing":              map[string]string{"prompt": "0", "completion": "0"},
				"supported_parameters": []string{"tools"},
			}},
		})
	}))
	defer srv.Close()

	var m Models
	_ = json.Unmarshal([]byte(`{"stale:free":{}}`), &m)
	cfg := &Config{
		Model: ModelSpec{"default": "free/stale:free"},
		Providers: []Provider{{
			Name:        "free",
			URL:         srv.URL,
			Type:        ProviderTypeOpenAI,
			Keys:        []string{"k"},
			FreeFetched: []string{"stale:free"},
			Models:      m,
		}},
	}
	boot := ResolveBoot(cfg)
	if boot.Model != "fresh:free" {
		t.Fatalf("Model = %q, want the refreshed fresh:free", boot.Model)
	}
	if _, ok := cfg.Providers[0].Models.Get("stale:free"); ok {
		t.Error("stale:free must be dropped by the refresh inside ResolveBoot")
	}
}

// The winning provider must EMBED the resolved model (wire-level fidelity of
// the original CreateAllProviders loop): without the rebuild-with-override,
// every provider would embed its first model and requests would silently go
// to the wrong model.
func TestResolveBoot_OverridesEmbeddedModel(t *testing.T) {
	var lastModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lastModel = body.Model
		_, _ = w.Write([]byte(`{"id":"c","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	chat := func(name string) Provider {
		return Provider{Name: name, URL: srv.URL, Keys: []string{"k"}, Type: ProviderTypeOpenAI,
			Models: newModelSet("m1", "m2")}
	}
	cfg := &Config{
		Model:     ModelSpec{"default": "t/m2"},
		Providers: []Provider{chat("t"), chat("s")},
	}
	boot := ResolveBoot(cfg)
	if _, err := boot.Providers["t"].Complete(t.Context(), &llm.Request{
		MaxTokens: 8,
		Messages:  []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if lastModel != "m2" {
		t.Fatalf("embedded model on the wire = %q, want the resolved m2", lastModel)
	}
	// The override must land on t only: s keeps its own first model.
	if _, err := boot.Providers["s"].Complete(t.Context(), &llm.Request{
		MaxTokens: 8,
		Messages:  []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}}},
	}); err != nil {
		t.Fatalf("Complete s: %v", err)
	}
	if lastModel != "m1" {
		t.Fatalf("sibling embedded model = %q, want its own first model m1", lastModel)
	}
}

// The legacy remote_desktop key backfills on the success path, exactly like
// loadFromFile does for the strict Load.
func TestLoadForgiving_LegacyRemoteDesktopBackfill(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, `{"remote_desktop":[{"name":"r","addr":"ws://127.0.0.1:5901"}]}`)

	cfg, degraded := LoadForgiving()
	if degraded != nil {
		t.Fatalf("Degraded = %+v, want nil", degraded)
	}
	if len(cfg.Desktops) != 1 || cfg.Desktops[0].Name != "r" {
		t.Fatalf("Desktops = %+v, want the legacy entry r", cfg.Desktops)
	}
}

// The API_TIMEOUT_MS env override mirrors Load(): valid values apply,
// non-positive values keep the default.
func TestLoadForgiving_APITimeoutEnv(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, `{"theme":"dark"}`)

	t.Setenv("API_TIMEOUT_MS", "12345")
	cfg, degraded := LoadForgiving()
	if degraded != nil {
		t.Fatalf("Degraded = %+v, want nil", degraded)
	}
	if cfg.APITimeoutMS != 12345 {
		t.Fatalf("APITimeoutMS = %d, want 12345 from the env override", cfg.APITimeoutMS)
	}

	t.Setenv("API_TIMEOUT_MS", "0")
	cfg, degraded = LoadForgiving()
	if degraded != nil {
		t.Fatalf("Degraded = %+v, want nil", degraded)
	}
	if cfg.APITimeoutMS != 300000 {
		t.Fatalf("APITimeoutMS = %d, want the default 300000 (non-positive env ignored)", cfg.APITimeoutMS)
	}

	// The boundary value 1 applies (the guard is strictly > 0).
	t.Setenv("API_TIMEOUT_MS", "1")
	cfg, _ = LoadForgiving()
	if cfg.APITimeoutMS != 1 {
		t.Fatalf("APITimeoutMS = %d, want 1", cfg.APITimeoutMS)
	}
}

// An empty file's syntax error carries Offset 0: the line-number suffix must
// be suppressed rather than claim "(line 1)".
func TestLoadForgiving_EmptyFileHasNoLineNumber(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, "")

	_, degraded := LoadForgiving()
	if degraded == nil {
		t.Fatal("Degraded = nil, want the empty-file parse failure")
	}
	if !strings.Contains(degraded.Reason, userPath) {
		t.Fatalf("Reason = %q, want the offending path", degraded.Reason)
	}
	if !strings.HasSuffix(degraded.Reason, "unexpected end of JSON input") {
		t.Fatalf("Reason = %q, want the bare parse error with no (line N) suffix", degraded.Reason)
	}
}

// A bad byte on the very first line reports "(line 1)": the offset boundary
// is inclusive of offset 1 (the first byte).
func TestLoadForgiving_FirstByteErrorReportsLineOne(t *testing.T) {
	userPath, _ := degLoadEnv(t)
	writeDegSettings(t, userPath, "x")

	_, degraded := LoadForgiving()
	if degraded == nil {
		t.Fatal("Degraded = nil, want the parse failure")
	}
	if !strings.Contains(degraded.Reason, "(line 1)") {
		t.Fatalf("Reason = %q, want (line 1) for an error at the first byte", degraded.Reason)
	}
}
