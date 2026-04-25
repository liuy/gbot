package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// LoadHooks — source: hooks.ts:1506-1513 (config merge)
//
// Follows MCP config pattern: pkg/mcp/config.go.
// Config sources are MERGED (concatenated), not replaced.
// ---------------------------------------------------------------------------

// PolicySettings controls hook execution policy.
// Source: TS settings — disableAllHooks, allowManagedHooksOnly.
type PolicySettings struct {
	DisableAllHooks      bool `json:"disableAllHooks,omitempty"`
	AllowManagedHooksOnly bool `json:"allowManagedHooksOnly,omitempty"`
}

// LoadHooks reads hooks configuration from settings.json files.
// Source: hooks.ts:1506-1513 — all scopes merged (concatenated).
//
// Config sources (in priority order, all merged):
//  1. User: ~/.gbot/settings.json → "hooks" key
//  2. Project: .gbot/settings.json → "hooks" key
//  3. Local: .gbot/settings.local.json → "hooks" key
//
// Merge semantics: same event from different scopes → append matchers (not replace).
func LoadHooks(userSettingsDir, projectDir string) HooksConfig {
	var result HooksConfig

	// Scope 1: User settings
	userPath := filepath.Join(userSettingsDir, "settings.json")
	result = mergeHooksFromFile(result, userPath)

	// Scope 2: Project settings
	projectPath := filepath.Join(projectDir, ".gbot", "settings.json")
	result = mergeHooksFromFile(result, projectPath)

	// Scope 3: Local project settings
	localPath := filepath.Join(projectDir, ".gbot", "settings.local.json")
	result = mergeHooksFromFile(result, localPath)

	return result
}

// LoadHooksPolicy reads hook policy settings from user settings.
func LoadHooksPolicy(userSettingsDir string) PolicySettings {
	var policy struct {
		Policy PolicySettings `json:"hooksPolicy"`
	}
	path := filepath.Join(userSettingsDir, "settings.json")
	_ = readJSONFile(path, &policy)
	return policy.Policy
}

// mergeHooksFromFile reads hooks from a single settings file and merges
// into the existing config. Source: hooks.ts:1506-1513 — concatenate matchers.
func mergeHooksFromFile(base HooksConfig, path string) HooksConfig {
	var raw struct {
		Hooks HooksConfig `json:"hooks"`
	}
	if err := readJSONFile(path, &raw); err != nil {
		return base
	}
	if raw.Hooks == nil {
		return base
	}
	if base == nil {
		base = make(HooksConfig)
	}
	// Merge: same event → append matchers (not replace)
	for event, matchers := range raw.Hooks {
		base[event] = append(base[event], matchers...)
	}
	return base
}

// readJSONFile reads and unmarshals a JSON file. Returns nil error if file
// doesn't exist or is unreadable (silently skipped, matching TS behavior).
func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err // includes IsNotExist
	}
	return json.Unmarshal(data, target)
}

// ShouldSkipHooks checks if hooks should be skipped based on policy.
// Source: hooks.ts:286-296 — shouldSkipHookDueToTrust (trust check is on Hooks struct).
func ShouldSkipHooks(policy PolicySettings) bool {
	return policy.DisableAllHooks
}
