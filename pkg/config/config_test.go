package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/types"
)

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()

	if cfg.PermissionMode != types.PermissionModeDefault {
		t.Errorf("expected PermissionModeDefault, got %s", cfg.PermissionMode)
	}
	if cfg.APITimeoutMS != 300000 {
		t.Errorf("expected APITimeoutMS 300000, got %d", cfg.APITimeoutMS)
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

func TestLoad_UserConfigFileError(t *testing.T) {
	// Test the error branch when user config file exists but cannot be read
	// (e.g., permission denied or malformed JSON in user config)
	dir := t.TempDir()

	// Set HOME to our temp dir
	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	// Create .gbot dir with an invalid settings.json (malformed JSON)
	userGbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(userGbotDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userGbotDir, "settings.json"), []byte("{{invalid json}}"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Change to a different dir so there's no project config to interfere
	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	// Clear env vars that might interfere
	for _, k := range []string{
		"API_TIMEOUT_MS",
	} {
		_ = os.Unsetenv(k)
		defer func(key string) { _ = os.Unsetenv(key) }(k)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when user config has malformed JSON")
	}
	if !strings.Contains(err.Error(), "user config") {
		t.Errorf("expected error to mention 'user config', got: %v", err)
	}
}

func TestLoad_ProjectConfigFileError(t *testing.T) {
	// Test the error branch when project config file exists but contains invalid JSON
	dir := t.TempDir()

	// Set HOME to temp dir with no user config
	homeDir := filepath.Join(dir, "home")
	_ = os.MkdirAll(homeDir, 0755)
	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Unsetenv("HOME") }()

	// Create .gbot dir in project (working dir) with malformed JSON
	projectGbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(projectGbotDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectGbotDir, "settings.json"), []byte("{{bad json}}"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	// Clear env vars
	for _, k := range []string{
		"API_TIMEOUT_MS",
	} {
		_ = os.Unsetenv(k)
		defer func(key string) { _ = os.Unsetenv(key) }(k)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when project config has malformed JSON")
	}
	if !strings.Contains(err.Error(), "project config") {
		t.Errorf("expected error to mention 'project config', got: %v", err)
	}
}

func TestLoadFromFile_PermissionError(t *testing.T) {
	// Test loadFromFile with a file that exists but cannot be read
	// (non-NotExist error path, line 144)
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	dir := t.TempDir()
	restrictedPath := filepath.Join(dir, "settings.json")

	// Write a file then remove read permissions
	if err := os.WriteFile(restrictedPath, []byte(`{"model":"test"}`), 0000); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	// On some systems this may not actually restrict if running as root, so skip above

	// loadFromFile is unexported, so we test it indirectly via Load
	// by setting up HOME to point to a dir with a restricted settings.json
	homeDir := filepath.Join(dir, "home")
	_ = os.MkdirAll(homeDir, 0755)
	userGbotDir := filepath.Join(homeDir, ".gbot")
	_ = os.MkdirAll(userGbotDir, 0755)

	restrictedSettings := filepath.Join(userGbotDir, "settings.json")
	_ = os.WriteFile(restrictedSettings, []byte(`{"model":"test"}`), 0000)

	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Unsetenv("HOME") }()

	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	// Clear env vars
	for _, k := range []string{
		"API_TIMEOUT_MS",
	} {
		_ = os.Unsetenv(k)
		defer func(key string) { _ = os.Unsetenv(key) }(k)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when settings file has no read permission")
	}
	if !strings.Contains(err.Error(), "user config") {
		t.Errorf("error should mention user config, got: %v", err)
	}
}

func TestLoadFromFile_FileNotFound(t *testing.T) {
	// Test that loadFromFile returns nil error for non-existent files
	// This is the os.IsNotExist branch (line 142-143)
	// We test it indirectly via Load with no config files present
	dir := t.TempDir()

	homeDir := filepath.Join(dir, "home")
	_ = os.MkdirAll(homeDir, 0755)
	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Unsetenv("HOME") }()

	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	// Clear env vars
	for _, k := range []string{
		"API_TIMEOUT_MS",
	} {
		_ = os.Unsetenv(k)
		defer func(key string) { _ = os.Unsetenv(key) }(k)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return defaults since no config files exist
	if cfg.Model != "pro" {
		t.Errorf("expected Model pro, got %s", cfg.Model)
	}
}

func TestLoad_MissingHomeDir(t *testing.T) {
	// Test Load when HOME is not set — os.UserHomeDir returns error,
	// so user config is skipped, but project config still loads
	dir := t.TempDir()

	_ = os.Unsetenv("HOME")
	// On Linux, UserHomeDir also checks $HOME; on some systems it may
	// fall back to passwd lookup. We force it to fail by unsetting HOME
	// and ensuring no fallback. This test is best-effort.

	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	// Clear env vars
	for _, k := range []string{
		"API_TIMEOUT_MS",
		"HOME",
	} {
		_ = os.Unsetenv(k)
	}

	cfg, err := config.Load()
	if err != nil {
		// HOME unset may cause UserHomeDir to fail â acceptable
		t.Logf("Load without HOME returned error: %v", err)
	} else if cfg.Model == "" {
		t.Error("expected non-empty model from defaults")
	}

	// Restore HOME
	_ = os.Setenv("HOME", originalDir)
}

func TestConfigDir_GBOTConfigDirEnvOverride(t *testing.T) {
	// Note: Current implementation doesn't check GBOT_CONFIG_DIR env var,
	// but this test exercises the ConfigDir function thoroughly
	dir := t.TempDir()
	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	configDir, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error: %v", err)
	}
	expected := filepath.Join(dir, ".gbot")
	if configDir != expected {
		t.Errorf("expected %s, got %s", expected, configDir)
	}
}

func TestConfigDir_HomeError(t *testing.T) {
	// Test ConfigDir when UserHomeDir fails
	// On most systems, setting HOME to empty causes UserHomeDir to fail
	originalHome := os.Getenv("HOME")
	_ = os.Unsetenv("HOME")
	defer func() {
		if originalHome != "" {
			_ = os.Setenv("HOME", originalHome)
		}
	}()

	// Also try to break passwd lookup by setting USERPROFILE (Windows) empty
	_ = os.Unsetenv("USERPROFILE")

	configDir, err := config.ConfigDir()
	if err == nil {
		// On some systems, passwd lookup succeeds even without HOME env
		if configDir == "" {
			t.Error("ConfigDir should return non-empty path even via fallback")
		}
	} else if !strings.Contains(err.Error(), "home") && !strings.Contains(err.Error(), "Home") && !strings.Contains(err.Error(), "HOME") && !strings.Contains(err.Error(), "directory") {
		t.Errorf("expected home-related error, got: %v", err)
	}
}

func TestLoad_NoEnvVarsSet(t *testing.T) {
	// Test Load with no env vars and no config files — pure defaults
	dir := t.TempDir()

	homeDir := filepath.Join(dir, "home")
	_ = os.MkdirAll(homeDir, 0755)
	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Unsetenv("HOME") }()

	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	// Clear all relevant env vars
	for _, k := range []string{
		"API_TIMEOUT_MS",
	} {
		_ = os.Unsetenv(k)
		defer func(key string) { _ = os.Unsetenv(key) }(k)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	defaults := config.DefaultConfig()
	if cfg.Model != "pro" {
		t.Errorf("expected Model pro, got %s", cfg.Model)
	}
	if cfg.APITimeoutMS != defaults.APITimeoutMS {
		t.Errorf("expected default APITimeoutMS, got %d", cfg.APITimeoutMS)
	}
}

func TestLoad_NegativeTimeoutEnv(t *testing.T) {
	// BUG: fmt.Sscanf with %d parses negative values like "-1" successfully.
	// A negative timeout is nonsensical and should be rejected or clamped.
	_ = os.Setenv("HOME", t.TempDir())
	defer func() { _ = os.Unsetenv("HOME") }()

	_ = os.Unsetenv("API_TIMEOUT_MS")
	defer func() { _ = os.Unsetenv("API_TIMEOUT_MS") }()

	_ = os.Setenv("API_TIMEOUT_MS", "-1")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Negative timeout should be rejected and default kept, but currently
	// the code accepts it — this test documents the actual behavior.
	if cfg.APITimeoutMS == -1 {
		t.Errorf("BUG: negative timeout accepted, got APITimeoutMS=%d, should reject or clamp to default 300000", cfg.APITimeoutMS)
	}
}

func TestLoad_ZeroTimeoutEnv(t *testing.T) {
	// Zero timeout means "no timeout" in most HTTP clients, which is
	// almost certainly a mistake. Should be rejected or clamped.
	_ = os.Setenv("HOME", t.TempDir())
	defer func() { _ = os.Unsetenv("HOME") }()

	_ = os.Unsetenv("API_TIMEOUT_MS")
	defer func() { _ = os.Unsetenv("API_TIMEOUT_MS") }()

	_ = os.Setenv("API_TIMEOUT_MS", "0")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.APITimeoutMS == 0 {
		t.Errorf("BUG: zero timeout accepted, got APITimeoutMS=%d, should reject or clamp to default 300000", cfg.APITimeoutMS)
	}
}

// ---------------------------------------------------------------------------
// Tier / Provider / Models tests
// ---------------------------------------------------------------------------

func TestTierConstants(t *testing.T) {
	t.Parallel()

	if config.TierLite != "lite" {
		t.Errorf("TierLite = %q, want %q", config.TierLite, "lite")
	}
	if config.TierPro != "pro" {
		t.Errorf("TierPro = %q, want %q", config.TierPro, "pro")
	}
	if config.TierMax != "max" {
		t.Errorf("TierMax = %q, want %q", config.TierMax, "max")
	}
}

func TestDefaultConfig_DefaultTier(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	if cfg.Model != "" {
		t.Errorf("Model = %q, want empty", cfg.Model)
	}
}

func TestProvider_ResolveKey_EnvVar(t *testing.T) {
	t.Parallel()

	_ = os.Setenv("TEST_GBOT_KEY_1", "resolved-key-123")
	defer func() { _ = os.Unsetenv("TEST_GBOT_KEY_1") }()

	p := config.Provider{
		Name: "test",
		Keys: []string{"$TEST_GBOT_KEY_1"},
	}

	key := p.ResolveKey()
	if key != "resolved-key-123" {
		t.Errorf("ResolveKey() = %q, want %q", key, "resolved-key-123")
	}
}

func TestProvider_ResolveKey_LiteralKey(t *testing.T) {
	t.Parallel()

	p := config.Provider{
		Name: "test",
		Keys: []string{"sk-literal-key"},
	}

	key := p.ResolveKey()
	if key != "sk-literal-key" {
		t.Errorf("ResolveKey() = %q, want %q", key, "sk-literal-key")
	}
}

func TestProvider_ResolveKey_MultipleEnvVars(t *testing.T) {
	t.Parallel()

	// First var unset, second set
	_ = os.Unsetenv("TEST_GBOT_MISSING_KEY")
	_ = os.Setenv("TEST_GBOT_FALLBACK_KEY", "fallback-456")
	defer func() { _ = os.Unsetenv("TEST_GBOT_FALLBACK_KEY") }()

	p := config.Provider{
		Name: "test",
		Keys: []string{"$TEST_GBOT_MISSING_KEY", "$TEST_GBOT_FALLBACK_KEY"},
	}

	key := p.ResolveKey()
	if key != "fallback-456" {
		t.Errorf("ResolveKey() = %q, want %q", key, "fallback-456")
	}
}

func TestProvider_ResolveKey_EmptyKeys(t *testing.T) {
	t.Parallel()

	p := config.Provider{
		Name: "test",
		Keys: []string{},
	}

	key := p.ResolveKey()
	if key != "" {
		t.Errorf("ResolveKey() = %q, want empty string", key)
	}
}

func TestProvider_ModelFor_SpecificTier(t *testing.T) {
	t.Parallel()

	p := config.Provider{
		Name: "test",
		Models: map[config.Tier]string{
			config.TierLite: "model-lite",
			config.TierPro:  "model-pro",
			config.TierMax:  "model-max",
		},
	}

	if m := p.ModelFor(config.TierLite); m != "model-lite" {
		t.Errorf("ModelFor(lite) = %q, want %q", m, "model-lite")
	}
	if m := p.ModelFor(config.TierPro); m != "model-pro" {
		t.Errorf("ModelFor(pro) = %q, want %q", m, "model-pro")
	}
	if m := p.ModelFor(config.TierMax); m != "model-max" {
		t.Errorf("ModelFor(max) = %q, want %q", m, "model-max")
	}
}

func TestProvider_ModelFor_FallbackToPro(t *testing.T) {
	t.Parallel()

	p := config.Provider{
		Name: "test",
		Models: map[config.Tier]string{
			config.TierPro: "model-pro",
		},
	}

	// Requesting lite but only pro defined → fallback to pro
	if m := p.ModelFor(config.TierLite); m != "model-pro" {
		t.Errorf("ModelFor(lite) = %q, want fallback %q", m, "model-pro")
	}
}

func TestProvider_ProviderType(t *testing.T) {
	tests := []struct {
		name string
		p    config.Provider
		want string
	}{
		{"explicit openai", config.Provider{Type: "openai", URL: "https://anything.com"}, "openai"},
		{"explicit anthropic", config.Provider{Type: "anthropic", URL: "https://anything.com"}, "anthropic"},
		{"auto with anthropic url", config.Provider{URL: "https://api.anthropic.com"}, "anthropic"},
		{"auto with openai url", config.Provider{URL: "https://api.openai.com"}, "openai"},
		{"auto with custom url", config.Provider{URL: "https://open.bigmodel.cn/api/paas"}, "openai"},
		{"empty type and url", config.Provider{}, "openai"},
		{"type auto", config.Provider{Type: "auto", URL: "https://api.anthropic.com"}, "anthropic"},
		{"auto with anthropic in path", config.Provider{URL: "https://api.minimaxi.com/anthropic"}, "anthropic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.ProviderType(); got != tc.want {
				t.Errorf("ProviderType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoad_ProviderConfig(t *testing.T) {
	dir := t.TempDir()

	// Set env vars for key resolution
	_ = os.Setenv("TEST_GBOT_PROVIDER_KEY", "test-api-key-789")
	defer func() { _ = os.Unsetenv("TEST_GBOT_PROVIDER_KEY") }()

	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	// Clear env vars
	for _, k := range []string{
		"API_TIMEOUT_MS",
	} {
		_ = os.Unsetenv(k)
		defer func(key string) { _ = os.Unsetenv(key) }(k)
	}

	// Create user config with provider structure
	userGbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(userGbotDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	settings := map[string]any{
		"model": "pro",
		"providers": []map[string]any{
			{
				"name": "openai",
				"url":  "https://api.openai.com/v1",
				"keys": []string{"$TEST_GBOT_PROVIDER_KEY"},
				"models": map[string]string{
					"lite": "gpt-4o-mini",
					"pro":  "gpt-4o",
					"max":  "gpt-4.1",
				},
			},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userGbotDir, "settings.json"), data, 0644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Model != "pro" {
		t.Errorf("Model = %q, want %q", cfg.Model, "pro")
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("len(Providers) = %d, want 1", len(cfg.Providers))
	}
	p := cfg.Providers[0]
	if p.Name != "openai" {
		t.Errorf("Provider.Name = %q, want %q", p.Name, "openai")
	}
	if p.URL != "https://api.openai.com/v1" {
		t.Errorf("Provider.URL = %q, want %q", p.URL, "https://api.openai.com/v1")
	}
	if key := p.ResolveKey(); key != "test-api-key-789" {
		t.Errorf("ResolveKey() = %q, want %q", key, "test-api-key-789")
	}
	if m := p.ModelFor(config.TierPro); m != "gpt-4o" {
		t.Errorf("ModelFor(pro) = %q, want %q", m, "gpt-4o")
	}
}

func TestLoad_DefaultTierDefaultsToPro(t *testing.T) {
	dir := t.TempDir()

	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	for _, k := range []string{
		"API_TIMEOUT_MS",
	} {
		_ = os.Unsetenv(k)
		defer func(key string) { _ = os.Unsetenv(key) }(k)
	}

	originalDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalDir) }()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Model != "pro" {
		t.Errorf("Model = %q, want %q when not set", cfg.Model, "pro")
	}
}

// ---------------------------------------------------------------------------
// ParseModel tests
// ---------------------------------------------------------------------------

func TestParseModel_ProviderTier(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Model: "zhipu/pro"}
	provider, tier, err := cfg.ParseModel()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider != "zhipu" {
		t.Errorf("provider = %q, want %q", provider, "zhipu")
	}
	if tier != config.TierPro {
		t.Errorf("tier = %q, want %q", tier, config.TierPro)
	}
}

func TestParseModel_TierOnly(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Model: "lite"}
	provider, tier, err := cfg.ParseModel()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider != "" {
		t.Errorf("provider = %q, want empty", provider)
	}
	if tier != config.TierLite {
		t.Errorf("tier = %q, want %q", tier, config.TierLite)
	}
}

func TestParseModel_Empty(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Model: ""}
	provider, tier, err := cfg.ParseModel()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider != "" {
		t.Errorf("provider = %q, want empty", provider)
	}
	if tier != config.TierPro {
		t.Errorf("tier = %q, want %q", tier, config.TierPro)
	}
}

func TestParseModel_AllTiers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input     string
		wantTier  config.Tier
		wantProvd string
	}{
		{"zhipu/lite", config.TierLite, "zhipu"},
		{"zhipu/pro", config.TierPro, "zhipu"},
		{"zhipu/max", config.TierMax, "zhipu"},
		{"lite", config.TierLite, ""},
		{"pro", config.TierPro, ""},
		{"max", config.TierMax, ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			cfg := &config.Config{Model: tc.input}
			provider, tier, err := cfg.ParseModel()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if provider != tc.wantProvd {
				t.Errorf("provider = %q, want %q", provider, tc.wantProvd)
			}
			if tier != tc.wantTier {
				t.Errorf("tier = %q, want %q", tier, tc.wantTier)
			}
		})
	}
}

func TestParseModel_InvalidTier(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		model string
	}{
		{"invalid tier in provider/tier", "zhipu/fast"},
		{"invalid tier alone", "ultra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Model: tc.model}
			_, _, err := cfg.ParseModel()
			if err == nil {
				t.Fatal("expected error for invalid tier")
			}
			if !strings.Contains(err.Error(), "invalid tier") {
				t.Errorf("error should mention 'invalid tier', got: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Save tests
// ---------------------------------------------------------------------------

func TestSave_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	cfg := &config.Config{Model: "zhipu/pro"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Read back and verify
	data, err := os.ReadFile(filepath.Join(dir, ".gbot", "settings.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(raw["model"]) != `"zhipu/pro"` {
		t.Errorf("model = %s, want %q", string(raw["model"]), `"zhipu/pro"`)
	}
}

func TestSave_FieldLevelUpdate(t *testing.T) {
	dir := t.TempDir()
	gbotDir := filepath.Join(dir, ".gbot")
	_ = os.MkdirAll(gbotDir, 0755)
	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	// Pre-existing settings with other fields
	existing := map[string]any{
		"api_timeout_ms": 60000,
		"theme":          "dark",
		"providers":      []string{"zhipu"},
	}
	existingData, _ := json.Marshal(existing)
	_ = os.WriteFile(filepath.Join(gbotDir, "settings.json"), existingData, 0644)

	cfg := &config.Config{Model: "zhipu/lite"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Read back and verify all fields preserved
	data, err := os.ReadFile(filepath.Join(gbotDir, "settings.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(raw["model"]) != `"zhipu/lite"` {
		t.Errorf("model = %s, want %q", string(raw["model"]), `"zhipu/lite"`)
	}
	if string(raw["theme"]) != `"dark"` {
		t.Errorf("theme should be preserved, got %s", string(raw["theme"]))
	}
	if string(raw["api_timeout_ms"]) != "60000" {
		t.Errorf("api_timeout_ms should be preserved, got %s", string(raw["api_timeout_ms"]))
	}
}

func TestSave_CorruptedFile(t *testing.T) {
	dir := t.TempDir()
	gbotDir := filepath.Join(dir, ".gbot")
	_ = os.MkdirAll(gbotDir, 0755)
	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	// Write corrupted JSON
	_ = os.WriteFile(filepath.Join(gbotDir, "settings.json"), []byte("{{broken}}"), 0644)

	cfg := &config.Config{Model: "pro"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Read back — should only have model (corrupted content replaced)
	data, err := os.ReadFile(filepath.Join(gbotDir, "settings.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(raw["model"]) != `"pro"` {
		t.Errorf("model = %s, want %q", string(raw["model"]), `"pro"`)
	}
	// Corrupted content should be gone — only model field
	if len(raw) != 1 {
		t.Errorf("expected 1 field, got %d", len(raw))
	}
}

func TestSave_NoTmpFileLeft(t *testing.T) {
	dir := t.TempDir()
	_ = os.Setenv("HOME", dir)
	defer func() { _ = os.Unsetenv("HOME") }()

	cfg := &config.Config{Model: "pro"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// No .tmp file should remain
	gbotDir := filepath.Join(dir, ".gbot")
	entries, _ := os.ReadDir(gbotDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file should not remain: %s", e.Name())
		}
	}
}
