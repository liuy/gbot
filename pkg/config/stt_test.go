package config_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
)

func sttTestProviders() []config.Provider {
	return []config.Provider{
		{Name: "zhipu", URL: "https://open.bigmodel.cn/api/paas/v4", Keys: []string{"k1"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"glm-5.2": {}})},
		{Name: "audiocpp", URL: "http://127.0.0.1:8082/v1", Keys: []string{"k2"}, Type: config.ProviderTypeAudio, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"stt-stream": {}, "stt": {}})},
		{Name: "minimax", URL: "https://api.minimax.chat/v1", Keys: []string{"k3"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"MiniMax-Text": {}})},
	}
}

// Explicit type wins; URL inference must never mint "audio" (no URL shape
// implies STT — only an explicit "type":"audio" entry is one).
func TestProviderType_Audio(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "a", URL: "http://127.0.0.1:8082/v1", Type: config.ProviderTypeAudio}
	if got := p.ProviderType(); got != config.ProviderTypeAudio {
		t.Errorf("ProviderType() = %q, want audio", got)
	}
	// URL inference does not cross into audio for type-less providers.
	q := &config.Provider{Name: "b", URL: "http://127.0.0.1:8082/v1"}
	if got := q.ProviderType(); got == config.ProviderTypeAudio {
		t.Errorf("ProviderType() without explicit type = audio, want URL-inferred/openai")
	}
}

func TestResolveSTT(t *testing.T) {
	t.Run("exact provider/model", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: sttTestProviders(), STT: config.STTConfig{Default: "audiocpp/stt-stream"}}
		p, m, err := cfg.ResolveSTT()
		if err != nil {
			t.Fatalf("ResolveSTT: %v", err)
		}
		if p.Name != "audiocpp" || m != "stt-stream" {
			t.Errorf("ResolveSTT = %q/%q, want audiocpp/stt-stream", p.Name, m)
		}
	})
	t.Run("bare model crosses only audio providers", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: sttTestProviders(), STT: config.STTConfig{Default: "stt"}}
		p, m, err := cfg.ResolveSTT()
		if err != nil {
			t.Fatalf("ResolveSTT: %v", err)
		}
		if p.Name != "audiocpp" || m != "stt" {
			t.Errorf("ResolveSTT = %q/%q, want audiocpp/stt", p.Name, m)
		}
	})
	t.Run("chat provider rejected", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: sttTestProviders(), STT: config.STTConfig{Default: "zhipu/glm-5.2"}}
		_, _, err := cfg.ResolveSTT()
		if !errors.Is(err, config.ErrNotAudioProvider) {
			t.Fatalf("ResolveSTT(zhipu/glm-5.2) = %v, want ErrNotAudioProvider", err)
		}
		if !strings.Contains(err.Error(), "audio") {
			t.Errorf("error message %q must mention audio", err.Error())
		}
	})
	t.Run("bare chat model rejected", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: sttTestProviders(), STT: config.STTConfig{Default: "MiniMax-Text"}}
		_, _, err := cfg.ResolveSTT()
		if !errors.Is(err, config.ErrNotAudioProvider) {
			t.Fatalf("ResolveSTT(MiniMax-Text) = %v, want ErrNotAudioProvider", err)
		}
	})
	t.Run("empty default", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: sttTestProviders()}
		_, _, err := cfg.ResolveSTT()
		if err == nil || !strings.Contains(err.Error(), "stt") {
			t.Fatalf("ResolveSTT() = %v, want stt error", err)
		}
	})
	t.Run("unknown provider", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: sttTestProviders(), STT: config.STTConfig{Default: "nope/stt"}}
		_, _, err := cfg.ResolveSTT()
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("ResolveSTT(nope/stt) = %v, want provider-not-found", err)
		}
	})
	t.Run("unknown model in audio provider", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: sttTestProviders(), STT: config.STTConfig{Default: "audiocpp/nope"}}
		_, _, err := cfg.ResolveSTT()
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("ResolveSTT(audiocpp/nope) = %v, want model-not-found", err)
		}
	})
}

// Audio providers must be invisible to chat-model resolution in all three
// lookup paths (empty-model first-provider, explicit provider, cross-provider
// fuzzy).
func TestResolveModel_SkipsAudio(t *testing.T) {
	audioOnly := []config.Provider{
		{Name: "audiocpp", URL: "http://127.0.0.1:8082/v1", Type: config.ProviderTypeAudio, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"stt-stream": {}})},
	}
	t.Run("empty model ignores audio as first provider", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: audioOnly}
		if _, _, err := cfg.ResolveModel(); err == nil {
			t.Fatal("ResolveModel with audio-only providers should error")
		}
	})
	t.Run("explicit audio provider errors", func(t *testing.T) {
		t.Parallel()
		providers := append(sttTestProviders()[:1:1], audioOnly...)
		cfg := &config.Config{Providers: providers}
		_, _, err := cfg.ResolveModelByName("audiocpp/stt-stream")
		if err == nil || !strings.Contains(err.Error(), "audio") {
			t.Fatalf("ResolveModelByName(audiocpp/stt-stream) = %v, want audio-rejection", err)
		}
	})
	t.Run("cross-provider fuzzy skips audio", func(t *testing.T) {
		t.Parallel()
		providers := append(sttTestProviders()[:1:1], audioOnly...)
		cfg := &config.Config{Providers: providers}
		_, _, err := cfg.ResolveModelByName("stt-stream")
		if err == nil {
			t.Fatal("ResolveModelByName(stt-stream) should not resolve into an audio provider")
		}
	})
	t.Run("empty model with chat provider ahead of audio", func(t *testing.T) {
		t.Parallel()
		providers := append(sttTestProviders()[:1:1], audioOnly...)
		cfg := &config.Config{Providers: providers}
		p, m, err := cfg.ResolveModel()
		if err != nil {
			t.Fatalf("ResolveModel: %v", err)
		}
		if p.Name != "zhipu" || m != "glm-5.2" {
			t.Errorf("ResolveModel = %q/%q, want zhipu/glm-5.2", p.Name, m)
		}
	})
}

func TestCreateAllProviders_AudioExcluded(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Providers: sttTestProviders()}
	// Audio provider is second; without the skip its fallthrough would mint
	// an anthropic entry named audiocpp.
	cfg.Model = config.ModelSpec{"default": "zhipu/glm-5.2"}
	m, err := config.CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	if _, ok := m["audiocpp"]; ok {
		t.Error("audio provider leaked into llm provider map")
	}
	if _, ok := m["zhipu"]; !ok {
		t.Error("chat provider missing from llm provider map")
	}
	if len(m) != 2 {
		t.Errorf("provider map size = %d, want 2 (zhipu, minimax)", len(m))
	}
}

func TestSaveSTTDefault(t *testing.T) {
	seed := `{"model":{"default":"zhipu/glm-5.2","lite":"zhipu/glm-5-air"},"providers":[{"name":"zhipu","url":"https://z","keys":["k"],"models":{"glm-5.2":{}}},{"name":"audiocpp","url":"http://127.0.0.1:8082/v1","type":"audio","keys":["k"],"models":{"stt-stream":{},"stt":{}}}],"stt":{"default":"audiocpp/stt","extra":"keep-me"}}`

	newEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0600)
	}

	t.Run("success preserves siblings and stt keys", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveSTTDefault("audiocpp", "stt-stream"); err != nil {
			t.Fatalf("SaveSTTDefault: %v", err)
		}
		raw, err := os.ReadFile(settingsPath(t))
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}
		var doc struct {
			Model     map[string]string `json:"model"`
			STT       map[string]string `json:"stt"`
			Providers []config.Provider `json:"providers"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal settings: %v", err)
		}
		if doc.STT["default"] != "audiocpp/stt-stream" {
			t.Errorf("stt.default = %q, want audiocpp/stt-stream", doc.STT["default"])
		}
		if doc.STT["extra"] != "keep-me" {
			t.Errorf("stt.extra = %q, want keep-me (sibling keys preserved)", doc.STT["extra"])
		}
		if doc.Model["default"] != "zhipu/glm-5.2" || doc.Model["lite"] != "zhipu/glm-5-air" {
			t.Errorf("model tiers disturbed: %+v", doc.Model)
		}
		if len(doc.Providers) != 2 {
			t.Errorf("providers = %d entries, want 2", len(doc.Providers))
		}
	})

	t.Run("non-audio provider rejected", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveSTTDefault("zhipu", "glm-5.2"); !errors.Is(err, config.ErrNotAudioProvider) {
			t.Fatalf("SaveSTTDefault(zhipu, glm-5.2) = %v, want ErrNotAudioProvider", err)
		}
	})

	t.Run("unknown model rejected", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveSTTDefault("audiocpp", "nope"); !errors.Is(err, config.ErrUnknownModel) {
			t.Fatalf("SaveSTTDefault(audiocpp, nope) = %v, want ErrUnknownModel", err)
		}
	})

	t.Run("unknown provider rejected", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveSTTDefault("zzz", "stt-stream"); !errors.Is(err, config.ErrUnknownProvider) {
			t.Fatalf("SaveSTTDefault(zzz, stt-stream) = %v, want ErrUnknownProvider", err)
		}
	})

	t.Run("backup exists", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveSTTDefault("audiocpp", "stt-stream"); err != nil {
			t.Fatalf("SaveSTTDefault: %v", err)
		}
		bak, err := os.ReadFile(settingsPath(t) + ".bak")
		if err != nil {
			t.Fatalf("read settings.json.bak: %v", err)
		}
		if string(bak) != seed {
			t.Errorf(".bak content differs from the seeded file (must be a pre-write copy)")
		}
	})
}

func TestResolveSTT_EmptyDefaultMessage(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Providers: sttTestProviders()}
	_, _, err := cfg.ResolveSTT()
	if err == nil || !strings.Contains(err.Error(), "no default configured") {
		t.Fatalf("ResolveSTT() = %v, want the no-default message", err)
	}
}

// The audio-skip loops must SKIP, not break: an audio provider sorted ahead
// of the chat target must not end the search.
func TestResolveModel_AudioAheadOfChatProvider(t *testing.T) {
	audioFirst := []config.Provider{
		{Name: "audiocpp", URL: "http://127.0.0.1:8082/v1", Type: config.ProviderTypeAudio, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"stt-stream": {}})},
		{Name: "zhipu", URL: "https://z", Keys: []string{"k"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"glm-5.2": {}})},
	}
	t.Run("cross-provider search continues past audio", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: audioFirst}
		p, m, err := cfg.ResolveModelByName("glm-5.2")
		if err != nil {
			t.Fatalf("ResolveModelByName: %v (audio-first ordering must not break the search)", err)
		}
		if p.Name != "zhipu" || m != "glm-5.2" {
			t.Errorf("resolved %q/%q, want zhipu/glm-5.2", p.Name, m)
		}
	})
	t.Run("empty model takes first non-audio provider", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Providers: audioFirst}
		p, m, err := cfg.ResolveModel()
		if err != nil {
			t.Fatalf("ResolveModel: %v", err)
		}
		if p.Name != "zhipu" || m != "glm-5.2" {
			t.Errorf("resolved %q/%q, want zhipu/glm-5.2", p.Name, m)
		}
	})
}

// SaveSTTDefault file-mechanics pins: created dir mode, fresh-file mode,
// corrupt-JSON resilience, backup failure, and unwritable dir.
func TestSaveSTTDefault_FileMechanics(t *testing.T) {
	seed := `{"providers":[{"name":"audiocpp","url":"http://127.0.0.1:8082/v1","type":"audio","models":{"stt-stream":{}}}]}`

	t.Run("creates .gbot with 0755 even when validation then fails", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		// No settings.json → no providers → ErrUnknownProvider, but the
		// config dir is created (and mode-observable) before validation.
		if err := config.SaveSTTDefault("audiocpp", "stt-stream"); !errors.Is(err, config.ErrUnknownProvider) {
			t.Fatalf("SaveSTTDefault on empty HOME = %v, want ErrUnknownProvider", err)
		}
		dirInfo, err := os.Stat(filepath.Join(home, ".gbot"))
		if err != nil {
			t.Fatal(err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o755 {
			t.Errorf(".gbot mode = %o, want 755", got)
		}
	})

	t.Run("preserves existing file mode", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0o640)
		if err := config.SaveSTTDefault("audiocpp", "stt-stream"); err != nil {
			t.Fatalf("SaveSTTDefault: %v", err)
		}
		fileInfo, err := os.Stat(filepath.Join(home, ".gbot", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got := fileInfo.Mode().Perm(); got != 0o640 {
			t.Errorf("settings.json mode = %o, want preserved 640", got)
		}
	})

	t.Run("corrupt JSON file yields unknown-provider, file untouched", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, `{corrupt`, 0o600)
		if err := config.SaveSTTDefault("audiocpp", "stt-stream"); !errors.Is(err, config.ErrUnknownProvider) {
			t.Fatalf("SaveSTTDefault over corrupt file = %v, want ErrUnknownProvider (nothing to validate against)", err)
		}
		raw, err := os.ReadFile(filepath.Join(home, ".gbot", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "{corrupt" {
			t.Errorf("corrupt file was rewritten: %s", raw)
		}
	})

	t.Run("backup failure aborts the save", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0o600)
		path := filepath.Join(home, ".gbot", "settings.json")
		// A directory at the .bak path makes the backup write fail; the
		// atomic-save contract then requires an error, not a clobber.
		if err := os.Mkdir(path+".bak", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := config.SaveSTTDefault("audiocpp", "stt-stream"); err == nil {
			t.Fatal("SaveSTTDefault must fail when the backup cannot be written")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != seed {
			t.Error("original settings.json was modified despite backup failure")
		}
	})

	t.Run("unwritable config dir fails", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		gbot := filepath.Join(home, ".gbot")
		if err := os.Mkdir(gbot, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := config.SaveSTTDefault("audiocpp", "stt-stream"); err == nil {
			t.Fatal("SaveSTTDefault must fail when the config dir rejects writes")
		}
	})
}

// Audio providers are invisible to the free-model mirror even when sorted
// ahead of a fetching provider — the skip must not end the loop.
func TestCreateAllProviders_FreeFetchSkipsAudio(t *testing.T) {
	t.Parallel()
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"free-a","context_length":8192,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}]}`))
	}))
	t.Cleanup(free.Close)

	audioFirst := []config.Provider{
		{Name: "audiocpp", URL: "http://127.0.0.1:8082/v1", Type: config.ProviderTypeAudio, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"stt-stream": {}})},
		{Name: "freeprov", URL: free.URL, Keys: []string{"k"}, FreeFetched: []string{"stale-free"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"chat-x": {}})},
	}
	cfg := &config.Config{Providers: audioFirst}
	cfg.Model = config.ModelSpec{"default": "freeprov/chat-x"}
	if _, err := config.CreateAllProviders(cfg); err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	p := cfg.Providers[1]
	if !p.Models.Has("free-a") {
		t.Errorf("free fetch after the audio provider did not run: models = %v (a break-at-audio skip would drop it)", p.Models.Ordered())
	}
	if p.Models.Has("stale-free") {
		t.Error("stale fetched id must be replaced")
	}
}

// An audio provider WITH a free-fetch ledger must be skipped, not fetched:
// the mirror is an LLM concern.
func TestCreateAllProviders_FreeFetchSkipsAudioWithLedger(t *testing.T) {
	t.Parallel()
	fetched := make(chan string, 4)
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case fetched <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"free-a","context_length":8192,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}]}`))
	}))
	t.Cleanup(free.Close)

	providers := []config.Provider{
		{Name: "audiocpp", URL: free.URL, Type: config.ProviderTypeAudio, FreeFetched: []string{"old"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"stt-stream": {}})},
		{Name: "t", URL: "http://127.0.0.1:1", Keys: []string{"k"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"m": {}})},
	}
	cfg := &config.Config{Providers: providers}
	cfg.Model = config.ModelSpec{"default": "t/m"}
	if _, err := config.CreateAllProviders(cfg); err != nil {
		t.Fatalf("CreateAllProviders: %v", err)
	}
	if got := cfg.Providers[0].Models.Ordered(); len(got) != 1 || got[0] != "stt-stream" {
		t.Errorf("audio provider models = %v, must stay untouched (no fetch, no replace)", got)
	}
}
