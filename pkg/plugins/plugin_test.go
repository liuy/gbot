package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
		cacheDir   string
		want       string
	}{
		{
			name:       "replace both vars",
			input:      "${GBOT_PLUGIN_ROOT}/bridge/mcp-server.cjs",
			pluginRoot: "/home/user/.gbot/plugins/omc",
			pluginData: "/home/user/.gbot/plugins/data/omc",
			cacheDir:   "/home/user/.gbot/cache",
			want:       "/home/user/.gbot/plugins/omc/bridge/mcp-server.cjs",
		},
		{
			name:       "replace plugin data",
			input:      "${GBOT_PLUGIN_DATA}/state.json",
			pluginRoot: "/root",
			pluginData: "/data",
			cacheDir:   "/cache",
			want:       "/data/state.json",
		},
		{
			name:       "replace cache dir",
			input:      "${GBOT_CACHE_DIR}/traces/browser",
			pluginRoot: "/root",
			pluginData: "/data",
			cacheDir:   "/home/user/.gbot/cache",
			want:       "/home/user/.gbot/cache/traces/browser",
		},
		{
			name:       "replace all three vars",
			input:      "${GBOT_PLUGIN_ROOT}:${GBOT_PLUGIN_DATA}:${GBOT_CACHE_DIR}",
			pluginRoot: "/p",
			pluginData: "/d",
			cacheDir:   "/c",
			want:       "/p:/d:/c",
		},
		{
			name:       "no vars",
			input:      "static/path",
			pluginRoot: "/root",
			pluginData: "/data",
			cacheDir:   "/cache",
			want:       "static/path",
		},
		{
			name:       "empty string",
			input:      "",
			pluginRoot: "/root",
			pluginData: "/data",
			cacheDir:   "/cache",
			want:       "",
		},
		{
			name:       "multiple occurrences",
			input:      "${GBOT_PLUGIN_ROOT}/a:${GBOT_PLUGIN_ROOT}/b",
			pluginRoot: "/p",
			pluginData: "/d",
			cacheDir:   "/c",
			want:       "/p/a:/p/b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := substitutePluginVars(tt.input, tt.pluginRoot, tt.pluginData, tt.cacheDir)
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
	if len(vars) != 3 {
		t.Fatalf("expected 3 env vars, got %d", len(vars))
	}
	if vars[0] != "GBOT_PLUGIN_ROOT=/home/user/.gbot/plugins/omc" {
		t.Errorf("vars[0] = %q, want GBOT_PLUGIN_ROOT=...", vars[0])
	}
	if vars[1] != "GBOT_PLUGIN_DATA="+PluginDataDir("omc") {
		t.Errorf("vars[1] = %q, want GBOT_PLUGIN_DATA=...", vars[1])
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if vars[2] != "GBOT_CACHE_DIR="+filepath.Join(home, ".gbot", "cache") {
		t.Errorf("vars[2] = %q, want GBOT_CACHE_DIR=<home>/.gbot/cache", vars[2])
	}
}

func TestCacheDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	want := filepath.Join(home, ".gbot", "cache")
	if got := CacheDir(); got != want {
		t.Errorf("CacheDir() = %q, want %q", got, want)
	}
	if EnvVarCacheDir != "GBOT_CACHE_DIR" {
		t.Errorf("EnvVarCacheDir = %q, want GBOT_CACHE_DIR", EnvVarCacheDir)
	}
}

// ---------------------------------------------------------------------------
// LoadManifest
// ---------------------------------------------------------------------------

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()

	manifest := PluginManifest{
		Name:       "test-plugin",
		Version:    "1.0.0",
		Skills:     "./skills/",
		McpServers: "./mcp.json",
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0644); err != nil {
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
	if got.McpServers != "./mcp.json" {
		t.Errorf("McpServers = %q, want %q", got.McpServers, "./mcp.json")
	}
}

func TestLoadManifest_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("LoadManifest should return error when plugin.json does not exist")
	}
	if !strings.Contains(err.Error(), "read manifest") {
		t.Errorf("error should mention 'read manifest', got: %v", err)
	}
}

func TestLoadManifest_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte("{bad}"), 0644); err != nil {
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
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifest := PluginManifest{Name: "my-plugin", Version: "2.0.0"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644); err != nil {
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
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifest := PluginManifest{Name: "test-plugin", Version: "1.0.0"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644); err != nil {
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
	if runtime.GOOS == "android" {
		t.Skip("Termux HOME resolution differs from standard Linux; os.UserHomeDir fallback makes empty HOME non-fatal")
	}
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
	if runtime.GOOS == "android" {
		t.Skip("Termux HOME resolution differs from standard Linux; os.UserHomeDir fallback makes empty HOME non-fatal")
	}
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
	pluginDir := filepath.Join(tmpDir, "my-plugin")
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
	if plugins[0].RootPath != pluginDir {
		t.Errorf("RootPath = %q, want %q", plugins[0].RootPath, pluginDir)
	}
	if plugins[0].Manifest == nil || plugins[0].Manifest.Version != "1.0.0" {
		t.Errorf("Manifest.Version = %+v, want 1.0.0", plugins[0].Manifest)
	}
}

func TestDiscoverPluginsFromDir_NoManifestSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "no-manifest")
	if err := os.MkdirAll(bareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An unrelated file must not satisfy the manifest requirement.
	if err := os.WriteFile(filepath.Join(bareDir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	plugins, err := discoverPluginsFromDir(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plugins) != 0 {
		t.Fatalf("expected 0 plugins for dir without plugin.json, got %d", len(plugins))
	}
}

func TestLoadReplScripts(t *testing.T) {
	dir := t.TempDir()
	replDir := filepath.Join(dir, "repl")
	if err := os.MkdirAll(replDir, 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"b-harness.js": "globalThis.browser = {};",
		"a-setup.js":   "globalThis.__first = true;",
		"notes.txt":    "not javascript",
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(replDir, name), []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
	}
	plugin := &ResolvedPlugin{Name: "p", RootPath: dir, Manifest: &PluginManifest{Repls: "./repl/"}}
	got := loadReplScripts(plugin)
	if len(got) != 2 {
		t.Fatalf("loadReplScripts() = %d scripts, want 2 (only .js)", len(got))
	}
	if got[0].Name != "a-setup.js" || got[1].Name != "b-harness.js" {
		t.Errorf("scripts not sorted: %q, %q", got[0].Name, got[1].Name)
	}
	if got[0].Plugin != "p" || got[0].Source != "globalThis.__first = true;" {
		t.Errorf("script fields wrong: %+v", got[0])
	}
}

func TestLoadReplScripts_Missing(t *testing.T) {
	// No repl dir and no manifest pointer: both silently yield nothing.
	plugin := &ResolvedPlugin{Name: "p", RootPath: t.TempDir(), Manifest: &PluginManifest{Repls: "./repl/"}}
	if got := loadReplScripts(plugin); len(got) != 0 {
		t.Errorf("missing dir: got %d scripts, want 0", len(got))
	}
	noField := &ResolvedPlugin{Name: "p", RootPath: t.TempDir(), Manifest: &PluginManifest{}}
	if got := loadReplScripts(noField); len(got) != 0 {
		t.Errorf("no manifest pointer: got %d scripts, want 0", len(got))
	}
}
