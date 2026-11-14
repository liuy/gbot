package config_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/config"
)

// seedSettingsFile writes content to $HOME/.gbot/settings.json, creating the
// directory, failing the test on setup errors.
func seedSettingsFile(t *testing.T, content string, mode os.FileMode) {
	t.Helper()
	gbotDir, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if err := os.MkdirAll(gbotDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", gbotDir, err)
	}
	path := filepath.Join(gbotDir, "settings.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile applies the process umask (Termux runs with 0077), so the
	// seeded file would not actually carry `mode` — and tests that assert
	// "save preserves the existing mode" would then compare against the
	// umask-masked value. Chmod is not umask-filtered.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func settingsPath(t *testing.T) string {
	t.Helper()
	dir, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	return filepath.Join(dir, "settings.json")
}

// readProviders reads settings.json back and decodes its "providers" key,
// failing the test if the file or key is missing/invalid.
func readProviders(t *testing.T) []config.Provider {
	t.Helper()
	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\n%s", err, data)
	}
	var providers []config.Provider
	if err := json.Unmarshal(raw["providers"], &providers); err != nil {
		t.Fatalf("providers key invalid: %v", err)
	}
	return providers
}

func TestSaveProviders_CreatesFileWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveProviders([]config.Provider{{
		Name:   "zhipu",
		URL:    "https://open.bigmodel.cn/api/coding/paas/v4",
		Keys:   []string{"sk-test"},
		Models: mustModels(t, `{"glm-5.3":{"context":"500k","input":["text"]}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	if _, err := os.Stat(settingsPath(t)); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("new file does not unmarshal: %v", err)
	}
	if _, exists := raw["model"]; exists {
		t.Errorf("model key must be absent, got %s", raw["model"])
	}
	var got []config.Provider
	if err := json.Unmarshal(raw["providers"], &got); err != nil {
		t.Fatalf("providers invalid: %v", err)
	}
	if len(got) != 1 || got[0].Name != "zhipu" {
		t.Errorf("providers = %+v, want single zhipu entry", got)
	}
	if got[0].FirstModelName() != "glm-5.3" {
		t.Errorf("first model = %q, want glm-5.3", got[0].FirstModelName())
	}
}

func TestSaveProviders_PreservesOtherKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// The default points at the provider being installed, so the dangling-
	// default guard leaves the model bytes alone and every key stays
	// verbatim (the test's actual subject).
	seed := `{
  "model": {"default": "minimax/abab6.5s-chat"},
  "permission_mode": "default",
  "theme": "dark",
  "web": {"anysearch": "as_sk_x"},
  "hooks": {"pre_tool": [{"command": "echo"}]}
}`
	seedSettingsFile(t, seed, 0600)

	if err := config.SaveProviders([]config.Provider{{
		Name: "minimax", URL: "https://api.minimax.chat/v1", Keys: []string{"k"},
		Models: mustModels(t, `{"abab6.5s-chat":{}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal([]byte(seed), &want); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	// MarshalIndent re-indents nested values, so byte equality holds only
	// after compacting both sides — what is asserted is data equality.
	for _, key := range []string{"model", "permission_mode", "theme", "web", "hooks"} {
		if gotCompact, wantCompact := compactJSON(t, got[key]), compactJSON(t, want[key]); gotCompact != wantCompact {
			t.Errorf("key %q not preserved verbatim:\n got %s\nwant %s", key, gotCompact, wantCompact)
		}
	}
	if p := readProviders(t); len(p) != 1 || p[0].Name != "minimax" {
		t.Errorf("providers = %+v, want minimax only", p)
	}
}

func TestSaveProviders_BackupCreatedBeforeWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := `{"model":{"default":"zhipu/glm-5.2"},"providers":[{"name":"old","url":"https://a","keys":["k"],"models":{"m":{}}}]}`
	seedSettingsFile(t, old, 0600)

	if err := config.SaveProviders([]config.Provider{{
		Name: "new", URL: "https://b", Keys: []string{"k"},
		Models: mustModels(t, `{"n":{}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	bak, err := os.ReadFile(settingsPath(t) + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bak) != old {
		t.Errorf(".bak must hold the OLD content verbatim:\n got %s\nwant %s", bak, old)
	}
	if got := readProviders(t); len(got) != 1 || got[0].Name != "new" {
		t.Errorf("new providers not written: %+v", got)
	}
}

func TestSaveProviders_NoBackupWithoutPriorFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveProviders([]config.Provider{{
		Name: "zhipu", URL: "https://a", Keys: []string{"k"},
		Models: mustModels(t, `{"m":{}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	if _, err := os.Stat(settingsPath(t) + ".bak"); !os.IsNotExist(err) {
		t.Errorf("no prior file existed, .bak must not be created (err=%v)", err)
	}
}

func TestSaveProviders_NewFileMode0600(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveProviders([]config.Provider{{
		Name: "zhipu", URL: "https://a", Keys: []string{"k"},
		Models: mustModels(t, `{"m":{}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	info, err := os.Stat(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("new file mode = %o, want 600 (carries API keys)", got)
	}
}

func TestSaveProviders_PreservesExistingMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"providers":[{"name":"a","url":"https://a","keys":[],"models":{"m":{}}}]}`, 0600)

	if err := config.SaveProviders([]config.Provider{{
		Name: "b", URL: "https://b", Keys: []string{"k"},
		Models: mustModels(t, `{"n":{}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	info, err := os.Stat(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("mode = %o, want preserved 600", got)
	}
}

func TestSaveProviders_MalformedExistingIsReplaced(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, "{garbage", 0600)

	if err := config.SaveProviders([]config.Provider{{
		Name: "zhipu", URL: "https://a", Keys: []string{"k"},
		Models: mustModels(t, `{"m":{}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders should heal a garbage file: %v", err)
	}

	bak, err := os.ReadFile(settingsPath(t) + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bak) != "{garbage" {
		t.Errorf(".bak = %q, want the garbage bytes preserved", bak)
	}
	if data, err := os.ReadFile(settingsPath(t)); err != nil {
		t.Fatal(err)
	} else if !json.Valid(data) {
		t.Errorf("new file not valid JSON: %s", data)
	}
}

func TestSaveProviders_ModelsKeyOrderPreserved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveProviders([]config.Provider{{
		Name: "zhipu", URL: "https://a", Keys: []string{"k"},
		Models: mustModels(t, `{"glm-a":{},"glm-b":{},"glm-c":{}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	providers := readProviders(t)
	got := providers[0].ModelNames()
	want := []string{"glm-a", "glm-b", "glm-c"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("models[%d] = %q, want %q (order must survive round-trip)", i, got[i], want[i])
		}
	}
}

func TestSaveProviders_NoTmpLeftBehind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"providers":[{"name":"a","url":"https://a","keys":[],"models":{"m":{}}}]}`, 0600)

	if err := config.SaveProviders([]config.Provider{{
		Name: "b", URL: "https://b", Keys: []string{"k"},
		Models: mustModels(t, `{"n":{}}`),
	}}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	if _, err := os.Stat(settingsPath(t) + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("settings.json.tmp must not survive the rename (err=%v)", err)
	}
}

func TestSaveProviders_ConcurrentSavesValidJson(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"providers":[{"name":"seed","url":"https://a","keys":[],"models":{"m":{}}}]}`, 0600)

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 5 {
				p := config.Provider{
					Name: "g" + string(rune('A'+g)) + "-" + string(rune('0'+i)),
					URL:  "https://x",
					Keys: []string{"k"},
				}
				p.Models.Set("m", config.ModelConfig{})
				if err := config.SaveProviders([]config.Provider{p}); err != nil {
					t.Errorf("goroutine %d save %d: %v", g, i, err)
				}
			}
		}(g)
	}
	wg.Wait()

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("file corrupted after concurrent saves:\n%s", data)
	}
	bak, err := os.ReadFile(settingsPath(t) + ".bak")
	if err != nil {
		t.Fatalf(".bak missing: %v", err)
	}
	if !json.Valid(bak) {
		t.Errorf(".bak corrupted:\n%s", bak)
	}
	if _, err := os.Stat(settingsPath(t) + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp left behind (err=%v)", err)
	}
}

// mustModels decodes a JSON model object, failing the test on bad input.
func mustModels(t *testing.T, raw string) config.Models {
	t.Helper()
	var m config.Models
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad models fixture %q: %v", raw, err)
	}
	return m
}

// compactJSON returns raw with insignificant whitespace removed, for value
// equality between differently-indented JSON bytes.
func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buf.String()
}

// T22 — deleting the default model from its provider repairs model.default
// to the surviving first model, so the next boot/reload skips tier 1.
func TestSaveProviders_DanglingDefaultRepairedToSiblingModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{"default":"t/old"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"old":{},"new":{}}}]}`, 0600)

	p := config.Provider{Name: "t", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	p.Models.Set("new", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{p}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Model["default"]; got != "t/new" {
		t.Fatalf("model.default = %q, want repaired t/new", got)
	}
}

// T23 — deleting the entire default provider repoints default at the first
// surviving provider's first model.
func TestSaveProviders_DefaultProviderDeletedFallsBackToSibling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{"default":"p2/m2"},"providers":[{"name":"p1","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m1":{}}},{"name":"p2","type":"openai","url":"http://127.0.0.1:2","keys":["k"],"models":{"m2":{}}}]}`, 0600)

	p := config.Provider{Name: "p1", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	p.Models.Set("m1", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{p}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Model["default"]; got != "p1/m1" {
		t.Fatalf("model.default = %q, want repaired p1/m1", got)
	}
}

// T24 — no salvageable provider in the new list: default is removed
// outright, while other tiers survive the repair.
func TestSaveProviders_NoLiveProviderDropsDefaultKeepsTiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{"default":"t/m","pro":"t/m"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}]}`, 0600)

	p := config.Provider{Name: "dead", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"$TEST_GBOT_PIN_UNSET_KEY"}}
	p.Models.Set("x", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{p}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Model map[string]string `json:"model"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw.Model["default"]; ok {
		t.Fatalf("model.default = %q, want it removed (no provider to point at)", raw.Model["default"])
	}
	if raw.Model["pro"] != "t/m" {
		t.Fatalf("model.pro = %q, want preserved (guard only repairs default)", raw.Model["pro"])
	}
}

// T25 — a still-valid default leaves the model bytes untouched (no-op
// branch is byte-stable; the repair branch only asserts values because Go
// map serialization alphabetizes tier keys).
func TestSaveProviders_ValidDefaultBytesUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seed := `{"model":{"default":"t/m"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}]}`
	seedSettingsFile(t, seed, 0600)

	p := config.Provider{Name: "t", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	p.Models.Set("m", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{p}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var seedRaw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(seed), &seedRaw); err != nil {
		t.Fatal(err)
	}
	if compactJSON(t, raw["model"]) != compactJSON(t, seedRaw["model"]) {
		t.Fatalf("model bytes changed on a valid default:\n got %s\nwant %s", raw["model"], seedRaw["model"])
	}
}

func TestSaveDefaultModel_SetsDefaultKeepsTiers(t *testing.T) {
	// Load() reads .gbot/settings.json relative to CWD.
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0755); err != nil {
		t.Fatal(err)
	}
	seed := `{"model":{"default":"old/a","pro":"zhipu/glm-5.3"},"providers":[{"name":"zhipu","url":"https://z","type":"openai","keys":["k"],"models":{"glm-5.3":{"context":"1M"}}}]}`
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}

	if err := config.SaveDefaultModel("zhipu", "glm-5.3"); err != nil {
		t.Fatalf("SaveDefaultModel: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model["default"] != "zhipu/glm-5.3" {
		t.Errorf("default = %q, want zhipu/glm-5.3", cfg.Model["default"])
	}
	if cfg.Model["pro"] != "zhipu/glm-5.3" {
		t.Errorf("pro tier lost: %v", cfg.Model)
	}
	// backup created from the pre-save content
	bak, err := os.ReadFile(filepath.Join(home, ".gbot", "settings.json.bak"))
	if err != nil || !strings.Contains(string(bak), "old/a") {
		t.Errorf("backup missing or wrong: err=%v", err)
	}
}

// An explicitly empty default ("model": {}) is a no-op: the guard must not
// invent a default for a spec that never declared one.
func TestSaveProviders_EmptyDefaultIsNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}]}`, 0600)

	p := config.Provider{Name: "t", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	p.Models.Set("m", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{p}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := compactJSON(t, raw["model"]); got != "{}" {
		t.Fatalf("model = %s, want the empty spec untouched", got)
	}
}

// A valid default must keep the model bytes untouched even when the default
// provider is NOT the first entry — the validity scan may not stop early,
// and the no-op branch must not rewrite tier order.
func TestSaveProviders_ValidDefaultNotFirstBytesUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Tier order is deliberately non-alphabetical: a repair-rewrite would
	// re-marshal alphabetically and expose itself.
	seedSettingsFile(t, `{"model":{"zz":"t/m","default":"t/m"},"providers":[{"name":"s","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"sm":{}}},{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}]}`, 0600)

	sp := config.Provider{Name: "s", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	sp.Models.Set("sm", config.ModelConfig{})
	tp := config.Provider{Name: "t", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	tp.Models.Set("m", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{sp, tp}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := compactJSON(t, raw["model"]), `{"zz":"t/m","default":"t/m"}`; got != want {
		t.Fatalf("model = %s, want the valid spec byte-untouched (%s)", got, want)
	}
}

// The validity check pairs provider name AND model: a same-named model on a
// DIFFERENT provider must not satisfy it (the default would still dangle).
func TestSaveProviders_OtherProvidersModelDoesNotSatisfyDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{"default":"t/m"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"other":{}}}]}`, 0600)

	sp := config.Provider{Name: "s", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	sp.Models.Set("m", config.ModelConfig{})
	tp := config.Provider{Name: "t", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	tp.Models.Set("other", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{sp, tp}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Model["default"]; got != "s/m" {
		t.Fatalf("model.default = %q, want repaired s/m (t lost its m)", got)
	}
}

// The repair picks the first VIABLE provider: audio-only, keyless, and
// model-less entries are all skipped in declaration order.
func TestSaveProviders_RepairSkipsUnviableProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{"default":"t/m"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}]}`, 0600)

	audio := config.Provider{Name: "aud", URL: "http://127.0.0.1:1", Keys: []string{"k"}, Type: config.ProviderTypeAudio}
	audio.Models.Set("stt", config.ModelConfig{})
	keyless := config.Provider{Name: "keyless", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"$TEST_GBOT_PIN_UNSET_KEY2"}}
	keyless.Models.Set("km", config.ModelConfig{})
	modelless := config.Provider{Name: "modelless", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	viable := config.Provider{Name: "good", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	viable.Models.Set("m3", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{audio, keyless, modelless, viable}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Model["default"]; got != "good/m3" {
		t.Fatalf("model.default = %q, want the first viable provider good/m3", got)
	}
}

// A bare (slash-less) default that the new list still resolves — a
// hand-edited "m2" while provider t keeps defining m2 — is valid: an
// unrelated provider's save must leave the model bytes untouched, not
// silently rewrite it to provider/first-model.
func TestSaveProviders_BareValidDefaultBytesUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seed := `{"model":{"default":"m2"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m1":{},"m2":{}}}]}`
	seedSettingsFile(t, seed, 0600)

	sp := config.Provider{Name: "s", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	sp.Models.Set("sm", config.ModelConfig{})
	tp := config.Provider{Name: "t", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	tp.Models.Set("m1", config.ModelConfig{})
	tp.Models.Set("m2", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{sp, tp}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := compactJSON(t, raw["model"]), `{"default":"m2"}`; got != want {
		t.Fatalf("model = %s, want the bare-but-valid default byte-untouched (%s)", got, want)
	}
}

// A bare default that no provider defines is dangling and gets the standard
// repair: first viable provider's first model.
func TestSaveProviders_BareDanglingDefaultRepaired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{"default":"zzz"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m1":{}}}]}`, 0600)

	sp := config.Provider{Name: "s", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	sp.Models.Set("sm", config.ModelConfig{})
	tp := config.Provider{Name: "t", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"k"}}
	tp.Models.Set("m1", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{sp, tp}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Model["default"]; got != "s/sm" {
		t.Fatalf("model.default = %q, want repaired s/sm", got)
	}
}

// When the repair removes the default and no other tier remains, the whole
// model key is dropped from the file.
func TestSaveProviders_EmptySpecDropsModelKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedSettingsFile(t, `{"model":{"default":"t/m"},"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{}}}]}`, 0600)

	p := config.Provider{Name: "dead", URL: "http://127.0.0.1:1", Type: "openai", Keys: []string{"$TEST_GBOT_PIN_UNSET_KEY3"}}
	p.Models.Set("x", config.ModelConfig{})
	if err := config.SaveProviders([]config.Provider{p}); err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	data, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["model"]; ok {
		t.Fatalf("model key = %s, want it removed once the spec is empty", raw["model"])
	}
}
