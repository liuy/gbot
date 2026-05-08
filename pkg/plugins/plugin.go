// Package plugins implements plugin discovery and loading for gbot.
//
// Source reference:
//   - src/services/mcp/mcpPluginIntegration.ts — TS plugin integration
//   - src/schemas/plugins.ts — Plugin manifest schemas
//
// Plugin directory: ~/.gbot/plugins/{name}/.gbot-plugin/plugin.json
// Discovery: simple directory scan, no installed_plugins.json.
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Types — plugin manifest and resolved plugin
// ---------------------------------------------------------------------------

// PluginManifest is the plugin definition read from .gbot-plugin/plugin.json.
// Source: schemas/plugins.ts — PluginSchema.
type PluginManifest struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Skills     string `json:"skills,omitempty"`     // relative path, e.g. "./skills/"
	McpServers string `json:"mcpServers,omitempty"` // relative path, e.g. "./.mcp.json"
}

// ResolvedPlugin is a fully discovered plugin with loaded manifest.
type ResolvedPlugin struct {
	Name     string          // directory name, e.g. "oh-my-claudecode"
	RootPath string          // absolute path, e.g. ~/.gbot/plugins/oh-my-claudecode/
	Manifest *PluginManifest // loaded manifest (nil if missing)
}

// ---------------------------------------------------------------------------
// Variable substitution — ${GBOT_PLUGIN_ROOT} and ${GBOT_PLUGIN_DATA}
//
// Two-phase strategy:
//   - Phase 1: substitutePluginVars() replaces ${GBOT_PLUGIN_ROOT}/${GBOT_PLUGIN_DATA}
//     in MCP config values (JSON strings) at load time.
//   - Phase 2: mcp.ExpandConfigEnv() handles remaining ${VAR} references via os.ExpandEnv.
//
// Hook commands use $GBOT_PLUGIN_ROOT (no braces) — expanded by bash at runtime
// via env injection, not string replacement.
// ---------------------------------------------------------------------------

const (
	// EnvVarPluginRoot is the environment variable for the plugin install directory.
	// Source: TS CLAUDE_PLUGIN_ROOT → GBOT_PLUGIN_ROOT for gbot.
	EnvVarPluginRoot = "GBOT_PLUGIN_ROOT"

	// EnvVarPluginData is the environment variable for the plugin data directory.
	EnvVarPluginData = "GBOT_PLUGIN_DATA"
)

// substitutePluginVars replaces ${GBOT_PLUGIN_ROOT} and ${GBOT_PLUGIN_DATA} in a string.
func substitutePluginVars(s, pluginRoot, pluginData string) string {
	s = strings.ReplaceAll(s, "${GBOT_PLUGIN_ROOT}", pluginRoot)
	s = strings.ReplaceAll(s, "${GBOT_PLUGIN_DATA}", pluginData)
	return s
}

// pluginEnvVars returns environment variable entries for a plugin.
func pluginEnvVars(pluginRoot, pluginName string) []string {
	dataDir := PluginDataDir(pluginName)
	return []string{
		EnvVarPluginRoot + "=" + pluginRoot,
		EnvVarPluginData + "=" + dataDir,
	}
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

// pluginsDirOverride allows tests to override the plugins directory.
// When non-empty, PluginsDir() returns this value instead of computing from home.
var pluginsDirOverride string

// PluginsDir returns the plugin installation directory (~/.gbot/plugins/).
func PluginsDir() (string, error) {
	if pluginsDirOverride != "" {
		return pluginsDirOverride, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("plugins: home dir: %w", err)
	}
	return filepath.Join(home, ".gbot", "plugins"), nil
}

// PluginDataDir returns the plugin data directory (~/.gbot/plugins/data/{name}/).
func PluginDataDir(pluginName string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gbot", "plugins", "data", pluginName)
}

// ---------------------------------------------------------------------------
// Discovery — directory scan of ~/.gbot/plugins/
// ---------------------------------------------------------------------------

// DiscoverPlugins scans the plugin directory for valid plugins.
// A valid plugin has a .gbot-plugin/plugin.json file.
func DiscoverPlugins() ([]*ResolvedPlugin, error) {
	dir, err := PluginsDir()
	if err != nil {
		return nil, err
	}
	return discoverPluginsFromDir(dir)
}

// discoverPluginsFromDir scans a directory for valid plugins.
func discoverPluginsFromDir(dir string) ([]*ResolvedPlugin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no plugins directory → empty list
		}
		return nil, fmt.Errorf("plugins: read dir %s: %w", dir, err)
	}

	var plugins []*ResolvedPlugin
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginRoot := filepath.Join(dir, entry.Name())

		plugin, err := LoadPlugin(pluginRoot)
		if err != nil {
			// Fail open: log and skip broken plugins.
			fmt.Fprintf(os.Stderr, "plugins: skip %s: %v\n", entry.Name(), err)
			continue
		}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}

// ---------------------------------------------------------------------------
// Loading — manifest parsing
// ---------------------------------------------------------------------------

// LoadPlugin loads a single plugin from its root directory.
func LoadPlugin(rootPath string) (*ResolvedPlugin, error) {
	name := filepath.Base(rootPath)
	manifest, err := LoadManifest(rootPath)
	if err != nil {
		return nil, fmt.Errorf("load plugin %s: %w", name, err)
	}
	return &ResolvedPlugin{
		Name:     name,
		RootPath: rootPath,
		Manifest: manifest,
	}, nil
}

// LoadManifest reads and parses the plugin manifest.
func LoadManifest(pluginRoot string) (*PluginManifest, error) {
	path := filepath.Join(pluginRoot, ".gbot-plugin", "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &manifest, nil
}
