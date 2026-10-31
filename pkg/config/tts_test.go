package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
)

func ttsTestProviders() []config.Provider {
	return []config.Provider{
		{Name: "zhipu", URL: "https://open.bigmodel.cn/api/paas/v4", Keys: []string{"k1"}, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"glm-5.2": {}})},
		{Name: "audiocpp", URL: "http://127.0.0.1:8082/v1", Keys: []string{"k2"}, Type: config.ProviderTypeAudio, Models: config.NewModelsFromMap(map[string]config.ModelConfig{"qwen3-asr-0.6b": {}, "qwen3-tts-1.7b": {}})},
	}
}

func TestResolveTTS(t *testing.T) {
	t.Run("explicit provider and exact model", func(t *testing.T) {
		cfg := &config.Config{Providers: ttsTestProviders(), TTS: config.TTSConfig{Default: "audiocpp/qwen3-tts-1.7b"}}
		p, m, err := cfg.ResolveTTS()
		if err != nil {
			t.Fatalf("ResolveTTS: %v", err)
		}
		if p.Name != "audiocpp" || m != "qwen3-tts-1.7b" {
			t.Errorf("resolved %q/%q, want audiocpp/qwen3-tts-1.7b", p.Name, m)
		}
	})

	t.Run("model-only fuzzy match across audio providers", func(t *testing.T) {
		cfg := &config.Config{Providers: ttsTestProviders(), TTS: config.TTSConfig{Default: "qwen3-tts"}}
		p, m, err := cfg.ResolveTTS()
		if err != nil {
			t.Fatalf("ResolveTTS: %v", err)
		}
		if p.Name != "audiocpp" || m != "qwen3-tts-1.7b" {
			t.Errorf("resolved %q/%q, want audiocpp/qwen3-tts-1.7b", p.Name, m)
		}
	})

	t.Run("non-audio provider rejected", func(t *testing.T) {
		cfg := &config.Config{Providers: ttsTestProviders(), TTS: config.TTSConfig{Default: "zhipu/glm-5.2"}}
		if _, _, err := cfg.ResolveTTS(); !errors.Is(err, config.ErrNotAudioProvider) {
			t.Fatalf("ResolveTTS(zhipu) = %v, want ErrNotAudioProvider", err)
		}
	})

	t.Run("unknown provider rejected", func(t *testing.T) {
		cfg := &config.Config{Providers: ttsTestProviders(), TTS: config.TTSConfig{Default: "nope/qwen3-tts-1.7b"}}
		if _, _, err := cfg.ResolveTTS(); err == nil || !strings.Contains(err.Error(), "tts: provider") {
			t.Fatalf("ResolveTTS(nope) = %v, want provider-not-found error", err)
		}
	})

	t.Run("explicit audio provider with unknown model rejected", func(t *testing.T) {
		cfg := &config.Config{Providers: ttsTestProviders(), TTS: config.TTSConfig{Default: "audiocpp/no-such-tts"}}
		_, _, err := cfg.ResolveTTS()
		if err == nil || !strings.Contains(err.Error(), `model "no-such-tts" not found in provider "audiocpp"`) {
			t.Fatalf("ResolveTTS(audiocpp/no-such-tts) = %v, want the model-not-found wording", err)
		}
	})

	t.Run("empty default errors", func(t *testing.T) {
		cfg := &config.Config{Providers: ttsTestProviders()}
		_, _, err := cfg.ResolveTTS()
		if err == nil || !strings.Contains(err.Error(), "no default configured") {
			t.Fatalf("ResolveTTS() = %v, want the no-default message", err)
		}
	})
}

func TestSaveTTSDefault(t *testing.T) {
	seed := `{"model":{"default":"zhipu/glm-5.2"},"providers":[{"name":"zhipu","url":"https://z","keys":["k"],"models":{"glm-5.2":{}}},{"name":"audiocpp","url":"http://127.0.0.1:8082/v1","type":"audio","keys":["k"],"models":{"qwen3-asr-0.6b":{},"qwen3-tts-1.7b":{}}}],"stt":{"default":"audiocpp/qwen3-asr-0.6b"}}`

	newEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0600)
	}

	t.Run("success preserves siblings", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveTTSDefault("audiocpp", "qwen3-tts-1.7b", ""); err != nil {
			t.Fatalf("SaveTTSDefault: %v", err)
		}
		raw, err := os.ReadFile(settingsPath(t))
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}
		var doc struct {
			Model map[string]string `json:"model"`
			STT   map[string]string `json:"stt"`
			TTS   map[string]string `json:"tts"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal settings: %v", err)
		}
		if doc.TTS["default"] != "audiocpp/qwen3-tts-1.7b" {
			t.Errorf("tts.default = %q, want audiocpp/qwen3-tts-1.7b", doc.TTS["default"])
		}
		if doc.STT["default"] != "audiocpp/qwen3-asr-0.6b" {
			t.Errorf("stt.default = %q, want preserved audiocpp/qwen3-asr-0.6b", doc.STT["default"])
		}
		if doc.Model["default"] != "zhipu/glm-5.2" {
			t.Errorf("model.default disturbed: %+v", doc.Model)
		}
	})

	t.Run("non-audio provider rejected", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveTTSDefault("zhipu", "glm-5.2", ""); !errors.Is(err, config.ErrNotAudioProvider) {
			t.Fatalf("SaveTTSDefault(zhipu) = %v, want ErrNotAudioProvider", err)
		}
	})

	t.Run("unknown model rejected", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveTTSDefault("audiocpp", "nope", ""); !errors.Is(err, config.ErrUnknownModel) {
			t.Fatalf("SaveTTSDefault(audiocpp, nope) = %v, want ErrUnknownModel", err)
		}
	})

	t.Run("unknown provider rejected", func(t *testing.T) {
		newEnv(t)
		if err := config.SaveTTSDefault("zzz", "qwen3-tts-1.7b", ""); !errors.Is(err, config.ErrUnknownProvider) {
			t.Fatalf("SaveTTSDefault(zzz) = %v, want ErrUnknownProvider", err)
		}
	})
}

// File-mechanics mirror of TestSaveSTTDefault_FileMechanics: the write path
// is the same safety-critical surface (backup, mode preservation, corrupt
// JSON resilience).
func TestSaveTTSDefault_FileMechanics(t *testing.T) {
	seed := `{"providers":[{"name":"audiocpp","url":"http://127.0.0.1:8082/v1","type":"audio","models":{"qwen3-tts-1.7b":{}}}]}`

	t.Run("preserves existing file mode", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0o640)
		if err := config.SaveTTSDefault("audiocpp", "qwen3-tts-1.7b", ""); err != nil {
			t.Fatalf("SaveTTSDefault: %v", err)
		}
		info, err := os.Stat(filepath.Join(home, ".gbot", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Errorf("settings.json mode = %o, want preserved 640", got)
		}
	})

	t.Run("backup exists and is the pre-write copy", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0o600)
		if err := config.SaveTTSDefault("audiocpp", "qwen3-tts-1.7b", ""); err != nil {
			t.Fatalf("SaveTTSDefault: %v", err)
		}
		bak, err := os.ReadFile(filepath.Join(home, ".gbot", "settings.json.bak"))
		if err != nil {
			t.Fatalf("read .bak: %v", err)
		}
		if string(bak) != seed {
			t.Errorf(".bak content differs from the seeded file (must be a pre-write copy)")
		}
	})

	t.Run("corrupt JSON file yields unknown-provider, file untouched", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, `{corrupt`, 0o600)
		if err := config.SaveTTSDefault("audiocpp", "qwen3-tts-1.7b", ""); !errors.Is(err, config.ErrUnknownProvider) {
			t.Fatalf("SaveTTSDefault over corrupt file = %v, want ErrUnknownProvider", err)
		}
		raw, err := os.ReadFile(filepath.Join(home, ".gbot", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "{corrupt" {
			t.Errorf("corrupt file was rewritten: %s", raw)
		}
	})

	t.Run("creates .gbot with 0755 even when validation then fails", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		if err := config.SaveTTSDefault("audiocpp", "qwen3-tts-1.7b", ""); !errors.Is(err, config.ErrUnknownProvider) {
			t.Fatalf("SaveTTSDefault on empty HOME = %v, want ErrUnknownProvider", err)
		}
		info, err := os.Stat(filepath.Join(home, ".gbot"))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf(".gbot mode = %o, want 755", got)
		}
	})

	t.Run("preserves sibling keys inside the tts object", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, `{"providers":[{"name":"audiocpp","url":"http://127.0.0.1:8082/v1","type":"audio","models":{"qwen3-tts-1.7b":{}}}],"tts":{"extra":"keep-me"}}`, 0o600)
		if err := config.SaveTTSDefault("audiocpp", "qwen3-tts-1.7b", ""); err != nil {
			t.Fatalf("SaveTTSDefault: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(home, ".gbot", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			TTS map[string]string `json:"tts"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.TTS["extra"] != "keep-me" {
			t.Errorf("tts.extra = %q, want keep-me (sibling keys preserved)", doc.TTS["extra"])
		}
	})
}

func TestSaveTTSDefaultVoice(t *testing.T) {
	seed := `{"providers":[{"name":"audiocpp","url":"http://127.0.0.1:8082/v1","type":"audio","keys":["k"],"models":{"qwen3-tts-1.7b":{}}}],"tts":{"default":"audiocpp/qwen3-tts-1.7b"}}`
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	seedSettingsFile(t, seed, 0600)
	if err := config.SaveTTSDefault("audiocpp", "qwen3-tts-1.7b", "Serena"); err != nil {
		t.Fatalf("SaveTTSDefault: %v", err)
	}
	var doc struct {
		TTS map[string]string `json:"tts"`
	}
	raw, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.TTS["voice"] != "Serena" {
		t.Errorf("tts.voice = %q, want Serena", doc.TTS["voice"])
	}
	if doc.TTS["default"] != "audiocpp/qwen3-tts-1.7b" {
		t.Errorf("tts.default = %q, want preserved", doc.TTS["default"])
	}
	// Empty voice is the engine-default state: the key must not linger with
	// a stale name.
	if err := config.SaveTTSDefault("audiocpp", "qwen3-tts-1.7b", ""); err != nil {
		t.Fatalf("SaveTTSDefault clear: %v", err)
	}
	raw, _ = os.ReadFile(settingsPath(t))
	doc.TTS = nil
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if v, ok := doc.TTS["voice"]; ok && v != "" {
		t.Errorf("tts.voice = %q after clear, want absent/empty", v)
	}
}
