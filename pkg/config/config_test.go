package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/user/gbot/pkg/config"
	"github.com/user/gbot/pkg/types"
)

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()

	if cfg.BaseURL != "https://api.anthropic.com" {
		t.Errorf("expected BaseURL 'https://api.anthropic.com', got %s", cfg.BaseURL)
	}
	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected Model 'claude-sonnet-4-20250514', got %s", cfg.Model)
	}
	if cfg.SmallModel != "claude-haiku-4-5-20251001" {
		t.Errorf("expected SmallModel 'claude-haiku-4-5-20251001', got %s", cfg.SmallModel)
	}
	if cfg.PermissionMode != types.PermissionModeDefault {
		t.Errorf("expected PermissionModeDefault, got %s", cfg.PermissionMode)
	}
	if cfg.APITimeoutMS != 300000 {
		t.Errorf("expected APITimeoutMS 300000, got %d", cfg.APITimeoutMS)
	}
}

func TestLoadFromSettingsFile_ValidFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	settings := map[string]any{
		"env": map[string]string{
			"ANTHROPIC_BASE_URL":       "https://custom.api.com/",
			"ANTHROPIC_AUTH_TOKEN":      "test-token-123",
			"ANTHROPIC_MODEL":           "claude-opus-4-20250101",
			"ANTHROPIC_SMALL_FAST_MODEL": "claude-haiku-4-5-20251001",
			"API_TIMEOUT_MS":            "60000",
		},
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("failed to marshal settings: %v", err)
	}
	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	cfg, err := config.LoadFromSettingsFile(settingsPath)
	if err != nil {
		t.Fatalf("LoadFromSettingsFile() error: %v", err)
	}

	if cfg.APIKey != "test-token-123" {
		t.Errorf("expected APIKey 'test-token-123', got %s", cfg.APIKey)
	}
	// BaseURL should be trimmed of trailing slash
	if cfg.BaseURL != "https://custom.api.com" {
		t.Errorf("expected BaseURL 'https://custom.api.com', got %s", cfg.BaseURL)
	}
	if cfg.Model != "claude-opus-4-20250101" {
		t.Errorf("expected Model 'claude-opus-4-20250101', got %s", cfg.Model)
	}
	if cfg.SmallModel != "claude-haiku-4-5-20251001" {
		t.Errorf("expected SmallModel 'claude-haiku-4-5-20251001', got %s", cfg.SmallModel)
	}
	if cfg.APITimeoutMS != 60000 {
		t.Errorf("expected APITimeoutMS 60000, got %d", cfg.APITimeoutMS)
	}
}

func TestLoadFromSettingsFile_NoEnvSection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	settings := map[string]any{
		"other_field": "value",
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("failed to marshal settings: %v", err)
	}
	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	cfg, err := config.LoadFromSettingsFile(settingsPath)
	if err != nil {
		t.Fatalf("LoadFromSettingsFile() error: %v", err)
	}

	// Should return defaults
	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected default model, got %s", cfg.Model)
	}
}

func TestLoadFromSettingsFile_PartialEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	settings := map[string]any{
		"env": map[string]string{
			"ANTHROPIC_AUTH_TOKEN": "partial-token",
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("failed to marshal settings: %v", err)
	}
	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	cfg, err := config.LoadFromSettingsFile(settingsPath)
	if err != nil {
		t.Fatalf("LoadFromSettingsFile() error: %v", err)
	}

	if cfg.APIKey != "partial-token" {
		t.Errorf("expected APIKey 'partial-token', got %s", cfg.APIKey)
	}
	// Other fields should retain defaults
	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected default model, got %s", cfg.Model)
	}
}

func TestLoadFromSettingsFile_FileNotFound(t *testing.T) {
	t.Parallel()

	_, err := config.LoadFromSettingsFile("/nonexistent/path/settings.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadFromSettingsFile_InvalidJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	if err := os.WriteFile(settingsPath, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	_, err := config.LoadFromSettingsFile(settingsPath)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadFromSettingsFile_InvalidTimeout(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	settings := map[string]any{
		"env": map[string]string{
			"API_TIMEOUT_MS": "not_a_number",
		},
	}
	data, _ := json.Marshal(settings)
	_ = os.WriteFile(settingsPath, data, 0644)

	cfg, err := config.LoadFromSettingsFile(settingsPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should keep default timeout since parsing failed
	if cfg.APITimeoutMS != 300000 {
		t.Errorf("expected default timeout 300000, got %d", cfg.APITimeoutMS)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	// Not parallel because we set env vars

	dir := t.TempDir()
	// Create user config dir (empty, so defaults come through)
	userGbotDir := filepath.Join(dir, "userhome", ".gbot")
	_ = os.MkdirAll(userGbotDir, 0755)

	_ = os.Setenv("HOME", filepath.Join(dir, "userhome"))
	defer func() { _ = os.Unsetenv("HOME") }()

	// Unset any existing env vars first
	for _, k := range []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL",
		"API_TIMEOUT_MS",
	} {
		_ = os.Unsetenv(k)
		defer func(key string) { _ = os.Unsetenv(key) }(k)
	}

	// Set our test env vars
	_ = os.Setenv("ANTHROPIC_API_KEY", "env-api-key")
	_ = os.Setenv("ANTHROPIC_BASE_URL", "https://env.api.com")
	_ = os.Setenv("ANTHROPIC_MODEL", "env-model")
	_ = os.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "env-small-model")
	_ = os.Setenv("API_TIMEOUT_MS", "120000")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.APIKey != "env-api-key" {
		t.Errorf("expected APIKey 'env-api-key', got %s", cfg.APIKey)
	}
	if cfg.BaseURL != "https://env.api.com" {
		t.Errorf("expected BaseURL 'https://env.api.com', got %s", cfg.BaseURL)
	}
	if cfg.Model != "env-model" {
		t.Errorf("expected Model 'env-model', got %s", cfg.Model)
	}
	if cfg.SmallModel != "env-small-model" {
		t.Errorf("expected SmallModel 'env-small-model', got %s", cfg.SmallModel)
	}
	if cfg.APITimeoutMS != 120000 {
		t.Errorf("expected APITimeoutMS 120000, got %d", cfg.APITimeoutMS)
	}
}

func TestLoad_AuthTokenEnv(t *testing.T) {
	_ = os.Setenv("HOME", t.TempDir())
	defer func() { _ = os.Unsetenv("HOME") }()

	_ = os.Unsetenv("ANTHROPIC_API_KEY")
	_ = os.Unsetenv("ANTHROPIC_AUTH_TOKEN")
	defer func() { _ = os.Unsetenv("ANTHROPIC_API_KEY") }()
	defer func() { _ = os.Unsetenv("ANTHROPIC_AUTH_TOKEN") }()

	_ = os.Setenv("ANTHROPIC_AUTH_TOKEN", "auth-token-value")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.APIKey != "auth-token-value" {
		t.Errorf("expected APIKey from ANTHROPIC_AUTH_TOKEN, got %s", cfg.APIKey)
	}
}

func TestLoad_InvalidTimeoutEnv(t *testing.T) {
	_ = os.Setenv("HOME", t.TempDir())
	defer func() { _ = os.Unsetenv("HOME") }()

	_ = os.Unsetenv("API_TIMEOUT_MS")
	defer func() { _ = os.Unsetenv("API_TIMEOUT_MS") }()

	_ = os.Setenv("API_TIMEOUT_MS", "abc")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Should keep default since parsing failed
	if cfg.APITimeoutMS != 300000 {
		t.Errorf("expected default timeout, got %d", cfg.APITimeoutMS)
	}
}

func TestConfigDir(t *testing.T) {
	// Not parallel because we modify HOME
	dir := t.TempDir()
	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	configDir, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error: %v", err)
	}
	if configDir == "" {
		t.Fatal("expected non-empty config dir")
	}
	if filepath.Base(configDir) != ".gbot" {
		t.Errorf("expected dir to end with .gbot, got %s", configDir)
	}
}

func TestLoad_ProjectConfig(t *testing.T) {
	// Create temp project dir with .gbot/settings.json
	dir := t.TempDir()

	// Set HOME to temp dir to avoid reading real config
	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	// Create .gbot dir in current working directory
	projectGbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(projectGbotDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	projectSettings := map[string]any{
		"model": "project-model",
	}
	data, _ := json.Marshal(projectSettings)
	_ = os.WriteFile(filepath.Join(projectGbotDir, "settings.json"), data, 0644)

	// Change to project dir
	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Model != "project-model" {
		t.Errorf("expected Model 'project-model', got %s", cfg.Model)
	}
}

func TestLoad_UserConfig(t *testing.T) {
	dir := t.TempDir()

	// Create user config
	userGbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(userGbotDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	userSettings := map[string]any{
		"model":  "user-model",
		"theme":  "dark",
		"debug":  true,
	}
	data, _ := json.Marshal(userSettings)
	_ = os.WriteFile(filepath.Join(userGbotDir, "settings.json"), data, 0644)

	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Model != "user-model" {
		t.Errorf("expected Model 'user-model', got %s", cfg.Model)
	}
	if cfg.Theme != "dark" {
		t.Errorf("expected Theme 'dark', got %s", cfg.Theme)
	}
	if !cfg.Debug {
		t.Error("expected Debug true")
	}
}

func TestLoadFromSettingsFile_EmptyEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	settings := map[string]any{
		"env": map[string]string{},
	}
	data, _ := json.Marshal(settings)
	_ = os.WriteFile(settingsPath, data, 0644)

	cfg, err := config.LoadFromSettingsFile(settingsPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return defaults
	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected default model, got %s", cfg.Model)
	}
}

func TestLoadFromSettingsFile_BaseURLNoTrailingSlash(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	settings := map[string]any{
		"env": map[string]string{
			"ANTHROPIC_BASE_URL": "https://api.example.com",
		},
	}
	data, _ := json.Marshal(settings)
	_ = os.WriteFile(settingsPath, data, 0644)

	cfg, err := config.LoadFromSettingsFile(settingsPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "https://api.example.com" {
		t.Errorf("expected 'https://api.example.com', got %s", cfg.BaseURL)
	}
}
