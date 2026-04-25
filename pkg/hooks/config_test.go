package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"strings"
)

// ---------------------------------------------------------------------------
// LoadHooks — scope merge semantics
// Source: hooks.ts:1506-1513 — all scopes merged (concatenated)
// ---------------------------------------------------------------------------

func TestLoadHooks_EmptyDirs(t *testing.T) {
	// No settings files exist → empty config
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	config := LoadHooks(tmpDir, projectDir)
	// nil or empty config both acceptable when no files exist
	if len(config) != 0 {
		t.Errorf("expected nil or empty config, got %d events", len(config))
	}
}

func TestLoadHooks_UserScope(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	// Write user settings
	userSettings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Bash",
					"hooks": []map[string]any{
						{"type": "command", "command": "echo user-hook"},
					},
				},
			},
		},
	}
	writeSettingsFile(t, filepath.Join(tmpDir, "settings.json"), userSettings)

	config := LoadHooks(tmpDir, projectDir)
	if len(config["PreToolUse"]) != 1 {
		t.Fatalf("expected 1 matcher for PreToolUse, got %d", len(config["PreToolUse"]))
	}
	m := config["PreToolUse"][0]
	if m.Matcher != "Bash" {
		t.Errorf("matcher = %q, want %q", m.Matcher, "Bash")
	}
	if len(m.Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(m.Hooks))
	}
	if m.Hooks[0].Command != "echo user-hook" {
		t.Errorf("command = %q, want %q", m.Hooks[0].Command, "echo user-hook")
	}
}

func TestLoadHooks_ProjectScope(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	// Write project settings
	projectSettings := map[string]any{
		"hooks": map[string]any{
			"PostToolUse": []map[string]any{
				{
					"matcher": "Write|Edit",
					"hooks": []map[string]any{
						{"type": "command", "command": "prettier --write"},
					},
				},
			},
		},
	}
		if err := os.MkdirAll(filepath.Join(projectDir, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSettingsFile(t, filepath.Join(projectDir, ".gbot", "settings.json"), projectSettings)

	config := LoadHooks(tmpDir, projectDir)
	if len(config["PostToolUse"]) != 1 {
		t.Fatalf("expected 1 matcher for PostToolUse, got %d", len(config["PostToolUse"]))
	}
	m := config["PostToolUse"][0]
	if m.Matcher != "Write|Edit" {
		t.Errorf("matcher = %q, want %q", m.Matcher, "Write|Edit")
	}
}

func TestLoadHooks_LocalScope(t *testing.T) {
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	// Write local project settings
	localSettings := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []map[string]any{
				{
					"matcher": "",
					"hooks": []map[string]any{
						{"type": "command", "command": "echo session-started"},
					},
				},
			},
		},
	}
		if err := os.MkdirAll(filepath.Join(projectDir, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSettingsFile(t, filepath.Join(projectDir, ".gbot", "settings.local.json"), localSettings)

	config := LoadHooks(tmpDir, projectDir)
	if len(config["SessionStart"]) != 1 {
		t.Fatalf("expected 1 matcher for SessionStart, got %d", len(config["SessionStart"]))
	}
}

func TestLoadHooks_MergeAcrossScopes(t *testing.T) {
	// Source: hooks.ts:1506-1513 — all scopes merged (appended, not replaced)
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	// User scope: PreToolUse for Bash
	userSettings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Bash",
					"hooks": []map[string]any{
						{"type": "command", "command": "user-bash-hook"},
					},
				},
			},
		},
	}
	writeSettingsFile(t, filepath.Join(tmpDir, "settings.json"), userSettings)

	// Project scope: PreToolUse for Write|Edit
		if err := os.MkdirAll(filepath.Join(projectDir, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	projectSettings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Write|Edit",
					"hooks": []map[string]any{
						{"type": "command", "command": "project-write-hook"},
					},
				},
			},
		},
	}
	writeSettingsFile(t, filepath.Join(projectDir, ".gbot", "settings.json"), projectSettings)

	config := LoadHooks(tmpDir, projectDir)

	// Both matchers should be present (merged, not replaced)
	if len(config["PreToolUse"]) != 2 {
		t.Fatalf("expected 2 matchers for PreToolUse (merged), got %d", len(config["PreToolUse"]))
	}

	// Verify first matcher (from user scope)
	if config["PreToolUse"][0].Matcher != "Bash" {
		t.Errorf("first matcher = %q, want %q", config["PreToolUse"][0].Matcher, "Bash")
	}
	// Verify second matcher (from project scope)
	if config["PreToolUse"][1].Matcher != "Write|Edit" {
		t.Errorf("second matcher = %q, want %q", config["PreToolUse"][1].Matcher, "Write|Edit")
	}
}

func TestLoadHooks_SameEventSameScopeMerged(t *testing.T) {
	// Multiple files contributing to the same event → all matchers appended
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	userSettings := map[string]any{
		"hooks": map[string]any{
			"Stop": []map[string]any{
				{
					"matcher": "",
					"hooks": []map[string]any{
						{"type": "command", "command": "hook-1"},
					},
				},
			},
		},
	}
	writeSettingsFile(t, filepath.Join(tmpDir, "settings.json"), userSettings)

		if err := os.MkdirAll(filepath.Join(projectDir, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	projectSettings := map[string]any{
		"hooks": map[string]any{
			"Stop": []map[string]any{
				{
					"matcher": "",
					"hooks": []map[string]any{
						{"type": "command", "command": "hook-2"},
					},
				},
			},
		},
	}
	writeSettingsFile(t, filepath.Join(projectDir, ".gbot", "settings.json"), projectSettings)

	config := LoadHooks(tmpDir, projectDir)
	if len(config["Stop"]) != 2 {
		t.Fatalf("expected 2 matchers for Stop (merged across scopes), got %d", len(config["Stop"]))
	}
	if config["Stop"][0].Hooks[0].Command != "hook-1" {
		t.Errorf("first hook command = %q, want %q", config["Stop"][0].Hooks[0].Command, "hook-1")
	}
	if config["Stop"][1].Hooks[0].Command != "hook-2" {
		t.Errorf("second hook command = %q, want %q", config["Stop"][1].Hooks[0].Command, "hook-2")
	}
}

func TestLoadHooks_NoHooksKey(t *testing.T) {
	// Settings file exists but has no "hooks" key → no error
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	settings := map[string]any{
		"theme": "dark",
	}
	writeSettingsFile(t, filepath.Join(tmpDir, "settings.json"), settings)

	config := LoadHooks(tmpDir, projectDir)
	if len(config) != 0 {
		t.Errorf("expected empty config when no hooks key, got %d events", len(config))
	}
}

func TestLoadHooks_MalformedJSON(t *testing.T) {
	// Malformed JSON → silently skipped (TS behavior)
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	path := filepath.Join(tmpDir, "settings.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	config := LoadHooks(tmpDir, projectDir)
	if len(config) != 0 {
		t.Errorf("expected empty config for malformed JSON, got %d events", len(config))
	}
}

func TestLoadHooks_PartialFiles(t *testing.T) {
	// One scope has valid hooks, another has malformed JSON
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	userSettings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Bash",
					"hooks": []map[string]any{
						{"type": "command", "command": "valid-hook"},
					},
				},
			},
		},
	}
	writeSettingsFile(t, filepath.Join(tmpDir, "settings.json"), userSettings)

	// Malformed project settings
		if err := os.MkdirAll(filepath.Join(projectDir, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".gbot", "settings.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("write malformed file: %v", err)
	}

	config := LoadHooks(tmpDir, projectDir)
	// User scope should still work
	if len(config["PreToolUse"]) != 1 {
		t.Fatalf("expected 1 matcher from user scope, got %d", len(config["PreToolUse"]))
	}
	if config["PreToolUse"][0].Hooks[0].Command != "valid-hook" {
		t.Errorf("command = %q, want %q", config["PreToolUse"][0].Hooks[0].Command, "valid-hook")
	}
}

// ---------------------------------------------------------------------------
// LoadHooksPolicy
// ---------------------------------------------------------------------------

func TestLoadHooksPolicy_DisableAllHooks(t *testing.T) {
	tmpDir := t.TempDir()
	settings := map[string]any{
		"hooksPolicy": map[string]any{
			"disableAllHooks": true,
		},
	}
	writeSettingsFile(t, filepath.Join(tmpDir, "settings.json"), settings)

	policy := LoadHooksPolicy(tmpDir)
	if !policy.DisableAllHooks {
		t.Error("expected DisableAllHooks = true")
	}
}

func TestLoadHooksPolicy_AllowManagedOnly(t *testing.T) {
	tmpDir := t.TempDir()
	settings := map[string]any{
		"hooksPolicy": map[string]any{
			"allowManagedHooksOnly": true,
		},
	}
	writeSettingsFile(t, filepath.Join(tmpDir, "settings.json"), settings)

	policy := LoadHooksPolicy(tmpDir)
	if !policy.AllowManagedHooksOnly {
		t.Error("expected AllowManagedHooksOnly = true")
	}
}

func TestLoadHooksPolicy_NoPolicyKey(t *testing.T) {
	tmpDir := t.TempDir()
	settings := map[string]any{
		"theme": "dark",
	}
	writeSettingsFile(t, filepath.Join(tmpDir, "settings.json"), settings)

	policy := LoadHooksPolicy(tmpDir)
	if policy.DisableAllHooks {
		t.Error("expected DisableAllHooks = false when no policy key")
	}
	if policy.AllowManagedHooksOnly {
		t.Error("expected AllowManagedHooksOnly = false when no policy key")
	}
}

func TestLoadHooksPolicy_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	policy := LoadHooksPolicy(tmpDir)
	if policy.DisableAllHooks {
		t.Error("expected DisableAllHooks = false when no file")
	}
}

// ---------------------------------------------------------------------------
// ShouldSkipHooks
// ---------------------------------------------------------------------------

func TestShouldSkipHooks_Disabled(t *testing.T) {
	if !ShouldSkipHooks(PolicySettings{DisableAllHooks: true}) {
		t.Error("expected true when DisableAllHooks is true")
	}
}

func TestShouldSkipHooks_Enabled(t *testing.T) {
	if ShouldSkipHooks(PolicySettings{}) {
		t.Error("expected false when DisableAllHooks is false")
	}
}

func TestShouldSkipHooks_ManagedOnly(t *testing.T) {
	// allowManagedHooksOnly alone does NOT skip all hooks
	if ShouldSkipHooks(PolicySettings{AllowManagedHooksOnly: true}) {
		t.Error("AllowManagedHooksOnly alone should not skip all hooks")
	}
}

// ---------------------------------------------------------------------------
// readJSONFile — internal helper
// ---------------------------------------------------------------------------

func TestReadJSONFile_Nonexistent(t *testing.T) {
	var target map[string]any
	err := readJSONFile("/nonexistent/path/settings.json", &target)
	if err == nil {
	if !strings.Contains(err.Error(), "nonexistent") && !strings.Contains(err.Error(), "no such") {
		t.Errorf("error should mention missing file, got: %v", err)
	}
	}
}

func TestReadJSONFile_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.json")
	content := map[string]string{"key": "value"}
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var target map[string]string
	err = readJSONFile(path, &target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target["key"] != "value" {
		t.Errorf("key = %q, want %q", target["key"], "value")
	}
}

// ---------------------------------------------------------------------------
// mergeHooksFromFile — internal helper
// ---------------------------------------------------------------------------

func TestMergeHooksFromFile_AppendNotReplace(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")

	// Base config with one matcher
	base := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{{Type: HookTypeCommand, Command: "hook-1"}}},
		},
	}

	// File with another matcher for the same event
	fileContent := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Write",
					"hooks": []map[string]any{
						{"type": "command", "command": "hook-2"},
					},
				},
			},
		},
	}
	writeSettingsFile(t, path, fileContent)

	result := mergeHooksFromFile(base, path)
	if len(result["PreToolUse"]) != 2 {
		t.Fatalf("expected 2 matchers (appended), got %d", len(result["PreToolUse"]))
	}
	if result["PreToolUse"][0].Matcher != "Bash" {
		t.Errorf("first matcher = %q, want %q", result["PreToolUse"][0].Matcher, "Bash")
	}
	if result["PreToolUse"][1].Matcher != "Write" {
		t.Errorf("second matcher = %q, want %q", result["PreToolUse"][1].Matcher, "Write")
	}
}

func TestMergeHooksFromFile_NilBase(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")

	fileContent := map[string]any{
		"hooks": map[string]any{
			"Stop": []map[string]any{
				{
					"matcher": "",
					"hooks": []map[string]any{
						{"type": "command", "command": "stop-hook"},
					},
				},
			},
		},
	}
	writeSettingsFile(t, path, fileContent)

	result := mergeHooksFromFile(nil, path)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result["Stop"]) != 1 {
		t.Fatalf("expected 1 matcher, got %d", len(result["Stop"]))
	}
}

func TestMergeHooksFromFile_NilHooksInFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")

	// File with no hooks key
	fileContent := map[string]any{
		"theme": "dark",
	}
	writeSettingsFile(t, path, fileContent)

	base := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{{Type: HookTypeCommand, Command: "hook-1"}}},
		},
	}

	result := mergeHooksFromFile(base, path)
	if len(result["PreToolUse"]) != 1 {
		t.Fatalf("expected base unchanged, got %d matchers", len(result["PreToolUse"]))
	}
}

func TestMergeHooksFromFile_NonexistentFile(t *testing.T) {
	base := HooksConfig{
		"PreToolUse": []HookMatcher{
			{Matcher: "Bash", Hooks: []HookConfig{{Type: HookTypeCommand, Command: "hook-1"}}},
		},
	}

	result := mergeHooksFromFile(base, "/nonexistent/file.json")
	if len(result["PreToolUse"]) != 1 {
		t.Fatalf("expected base unchanged for nonexistent file, got %d", len(result["PreToolUse"]))
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

func writeSettingsFile(t *testing.T, path string, content map[string]any) {
	t.Helper()
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
