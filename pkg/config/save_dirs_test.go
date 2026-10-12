package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/config"
)

// Every settings.json writer creates $HOME/.gbot with 0755 when it is
// missing. The mode is observable, so pin it exactly — a mutated constant
// (0700/0754/0777) changes who can read a directory full of API keys.
func TestSaveWriters_CreateConfigDirMode0755(t *testing.T) {
	cases := []struct {
		name string
		seed bool // whether settings.json must exist for the call to succeed
		call func() error
	}{
		{
			name: "Save",
			call: func() error {
				cfg := &config.Config{Model: config.ModelSpec{"default": "p/m"}}
				return cfg.Save()
			},
		},
		{
			name: "SaveProviders",
			call: func() error {
				return config.SaveProviders([]config.Provider{{Name: "p", URL: "https://p", Keys: []string{"k"}}})
			},
		},
		{
			name: "SaveRemoteDevices",
			call: func() error {
				return config.SaveRemoteDevices([]config.RemoteDevice{{Name: "d", Addr: "ws://127.0.0.1:6080"}})
			},
		},
		{
			name: "SaveDefaultModel",
			seed: true, // validates the provider/model against the existing file
			call: func() error {
				return config.SaveDefaultModel("p", "m")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Chdir(t.TempDir()) // keep project-local .gbot out of the picture
			if tc.seed {
				seed := `{"providers":[{"name":"p","url":"https://p","keys":["k"],"models":{"m":{}}}]}`
				if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0755); err != nil {
					t.Fatalf("seed mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(seed), 0600); err != nil {
					t.Fatalf("seed write: %v", err)
				}
			}
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			info, err := os.Stat(filepath.Join(home, ".gbot"))
			if err != nil {
				t.Fatalf("stat .gbot: %v", err)
			}
			if got := info.Mode().Perm(); got != 0755 {
				t.Errorf("%s created .gbot with mode %04o, want 0755", tc.name, got)
			}
		})
	}
}

// SaveDefaultModel's two sentinels are distinct error values mapped to
// distinct 400s by the HTTP handler — pinning which one fires also pins the
// provider-loop's model check and the known flag.
func TestSaveDefaultModel_SentinelShape(t *testing.T) {
	seed := `{"providers":[{"name":"a","url":"https://a","keys":["k"],"models":{"m1":{}}},{"name":"b","url":"https://b","keys":["k"],"models":{"m2":{}}}]}`

	t.Run("unknown model in known provider", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0600)

		if err := config.SaveDefaultModel("b", "nope"); !errors.Is(err, config.ErrUnknownModel) {
			t.Fatalf("SaveDefaultModel(b, nope) = %v, want ErrUnknownModel", err)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0600)

		if err := config.SaveDefaultModel("zzz", "m1"); !errors.Is(err, config.ErrUnknownProvider) {
			t.Fatalf("SaveDefaultModel(zzz, m1) = %v, want ErrUnknownProvider", err)
		}
	})

	// The match must be found by name, not position: "b" is second.
	t.Run("second provider succeeds", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		seedSettingsFile(t, seed, 0600)

		if err := config.SaveDefaultModel("b", "m2"); err != nil {
			t.Fatalf("SaveDefaultModel(b, m2): %v", err)
		}
		raw, err := os.ReadFile(settingsPath(t))
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}
		var doc struct {
			Model map[string]string `json:"model"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal settings: %v", err)
		}
		if doc.Model["default"] != "b/m2" {
			t.Errorf("model.default = %q, want b/m2", doc.Model["default"])
		}
	})
}
