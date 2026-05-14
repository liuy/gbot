package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// substitutePluginVars
// ---------------------------------------------------------------------------

func TestSubstitutePluginVars(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		pluginRoot string
		pluginData string
		want       string
	}{
		{
			name:       "replace both vars",
			input:      "${GBOT_PLUGIN_ROOT}/bridge/mcp-server.cjs",
			pluginRoot: "/home/user/.gbot/plugins/omc",
			pluginData: "/home/user/.gbot/plugins/data/omc",
			want:       "/home/user/.gbot/plugins/omc/bridge/mcp-server.cjs",
		},
		{
			name:       "replace plugin data",
			input:      "${GBOT_PLUGIN_DATA}/state.json",
			pluginRoot: "/root",
			pluginData: "/data",
			want:       "/data/state.json",
		},
		{
			name:       "no vars",
			input:      "static/path",
			pluginRoot: "/root",
			pluginData: "/data",
			want:       "static/path",
		},
		{
			name:       "empty string",
			input:      "",
			pluginRoot: "/root",
			pluginData: "/data",
			want:       "",
		},
		{
			name:       "multiple occurrences",
			input:      "${GBOT_PLUGIN_ROOT}/a:${GBOT_PLUGIN_ROOT}/b",
			pluginRoot: "/p",
			pluginData: "/d",
			want:       "/p/a:/p/b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := substitutePluginVars(tt.input, tt.pluginRoot, tt.pluginData)
			if got != tt.want {
				t.Errorf("substitutePluginVars(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// pluginEnvVars
// ---------------------------------------------------------------------------

func TestPluginEnvVars(t *testing.T) {
	vars := pluginEnvVars("/home/user/.gbot/plugins/omc", "omc")
	if len(vars) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(vars))
	}
	if vars[0] != "GBOT_PLUGIN_ROOT=/home/user/.gbot/plugins/omc" {
		t.Errorf("vars[0] = %q, want GBOT_PLUGIN_ROOT=...", vars[0])
	}
	if vars[1] != "GBOT_PLUGIN_DATA="+PluginDataDir("omc") {
		t.Errorf("vars[1] = %q, want GBOT_PLUGIN_DATA=...", vars[1])
	}
}

// ---------------------------------------------------------------------------
// LoadManifest
// ---------------------------------------------------------------------------

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	manifestDir := filepath.Join(dir, ".gbot-plugin")
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifest := PluginManifest{
		Name:       "test-plugin",
		Version:    "1.0.0",
		Skills:     "./skills/",
		McpServers: "./.mcp.json",
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest() error: %v", err)
	}
	if got.Name != "test-plugin" {
		t.Errorf("Name = %q, want %q", got.Name, "test-plugin")
	}
	if got.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", got.Version, "1.0.0")
	}
	if got.Skills != "./skills/" {
		t.Errorf("Skills = %q, want %q", got.Skills, "./skills/")
	}
	if got.McpServers != "./.mcp.json" {
		t.Errorf("McpServers = %q, want %q", got.McpServers, "./.mcp.json")
	}
}

func TestLoadManifest_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("LoadManifest should return error when .gbot-plugin/plugin.json does not exist")
	}
}

func TestLoadManifest_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	manifestDir := filepath.Join(dir, ".gbot-plugin")
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte("{bad}"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("LoadManifest should return error for malformed JSON in plugin.json")
	}
}

// ---------------------------------------------------------------------------
// LoadPlugin
// ---------------------------------------------------------------------------

func TestLoadPlugin(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	manifestDir := filepath.Join(pluginDir, ".gbot-plugin")
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifest := PluginManifest{Name: "my-plugin", Version: "2.0.0"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	plugin, err := LoadPlugin(pluginDir)
	if err != nil {
		t.Fatalf("LoadPlugin() error: %v", err)
	}
	if plugin.Name != "my-plugin" {
		t.Errorf("Name = %q, want %q", plugin.Name, "my-plugin")
	}
	if plugin.RootPath != pluginDir {
		t.Errorf("RootPath = %q, want %q", plugin.RootPath, pluginDir)
	}
	if plugin.Manifest.Version != "2.0.0" {
		t.Errorf("Manifest.Version = %q, want %q", plugin.Manifest.Version, "2.0.0")
	}
}

// ---------------------------------------------------------------------------
// DiscoverPlugins
// ---------------------------------------------------------------------------

func TestDiscoverPlugins_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	// Override PluginsDir by testing via direct directory scan logic.
	// Since DiscoverPlugins uses PluginsDir() which reads ~/.gbot/plugins/,
	// we test with a temp dir by using the internal logic directly.

	// Create a mock plugins structure in temp dir
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// No plugins → empty result from actual discovery logic
	plugins, err := discoverPluginsFromDir(pluginsDir)
	if err != nil {
		t.Fatalf("discoverPluginsFromDir() error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins from empty dir, got %d", len(plugins))
	}
}

func TestDiscoverPlugins_WithValidPlugin(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")

	// Create plugin structure
	pluginDir := filepath.Join(pluginsDir, "test-plugin")
	manifestDir := filepath.Join(pluginDir, ".gbot-plugin")
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifest := PluginManifest{Name: "test-plugin", Version: "1.0.0"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Also create a non-plugin directory (no manifest)
	if err := os.MkdirAll(filepath.Join(pluginsDir, "not-a-plugin"), 0755); err != nil {
		t.Fatal(err)
	}

	// Load plugin from the specific directory
	plugin, err := LoadPlugin(pluginDir)
	if err != nil {
		t.Fatalf("LoadPlugin() error: %v", err)
	}
	if plugin.Name != "test-plugin" {
		t.Errorf("Name = %q, want %q", plugin.Name, "test-plugin")
	}
}

// ---------------------------------------------------------------------------
// PluginsDir
// ---------------------------------------------------------------------------

func TestPluginsDir(t *testing.T) {
	dir, err := PluginsDir()
	if err != nil {
		t.Fatalf("PluginsDir() error: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".gbot", "plugins")
	if dir != want {
		t.Errorf("PluginsDir() = %q, want %q", dir, want)
	}
}

// ---------------------------------------------------------------------------
// discoverPluginsFromDir paths
// ---------------------------------------------------------------------------

func TestDiscoverPluginsFromDir_NonExistent(t *testing.T) {
	plugins, err := discoverPluginsFromDir("/nonexistent/path")
	if err != nil {
		t.Errorf("non-existent dir should return nil, nil, got err=%v", err)
	}
	if plugins != nil {
		t.Errorf("non-existent dir should return nil plugins, got %v", plugins)
	}
}

func TestPluginsDir_EmptyHome(t *testing.T) {
	orig := pluginsDirOverride
	pluginsDirOverride = ""
	defer func() { pluginsDirOverride = orig }()

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", "")
	defer func() { _ = os.Setenv("HOME", origHome) }()

	_, err := PluginsDir()
	if err == nil {
		t.Fatal("expected error when HOME is empty")
	}
	if !strings.Contains(err.Error(), "home dir") {
		t.Errorf("error should mention 'home dir', got: %v", err)
	}
}

func TestDiscoverPlugins_EmptyHome(t *testing.T) {
	orig := pluginsDirOverride
	pluginsDirOverride = ""
	defer func() { pluginsDirOverride = orig }()

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", "")
	defer func() { _ = os.Setenv("HOME", origHome) }()

	_, err := DiscoverPlugins()
	if err == nil {
		t.Fatal("expected error when PluginsDir fails")
	}
	if !strings.Contains(err.Error(), "home dir") {
		t.Errorf("error should propagate from PluginsDir, got: %v", err)
	}
}

func TestDiscoverPluginsFromDir_ReadDirError(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file (not directory) so os.ReadDir fails with ENOTDIR, not ENOENT
	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := discoverPluginsFromDir(filePath)
	if err == nil {
		t.Fatal("expected error when reading a file as directory")
	}
	if !strings.Contains(err.Error(), "read dir") {
		t.Errorf("error should mention 'read dir', got: %v", err)
	}
}

func TestDiscoverPluginsFromDir_WithPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "my-plugin", ".gbot-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":    "my-plugin",
		"version": "1.0.0",
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	plugins, err := discoverPluginsFromDir(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].Name != "my-plugin" {
		t.Errorf("plugin name = %q, want 'my-plugin'", plugins[0].Name)
	}
}
