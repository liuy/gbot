package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/types"
)

// PluginPieces holds one plugin's parsed four-piece content (plus repls),
// for reload reporting at per-plugin granularity.
type PluginPieces struct {
	Name     string
	RootPath string
	// Manifest is kept (alongside the parsed pieces) so report rows can
	// tell "surface declared but empty" from "surface not declared".
	Manifest    *PluginManifest
	McpServers  map[string]mcp.ScopedMcpServerConfig
	Hooks       hooks.HooksConfig
	Skills      []types.SkillCommand
	Agents      []types.AgentDefinition
	ReplScripts []ReplScript
	EnvVars     []string
}

// FileError names the plugin file a strict parse failed on; the reload
// report uses its Path for the failed row.
type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }
func (e *FileError) Unwrap() error { return e.Err }

// LoadPluginPieces discovers plugins (pluginsDir "" = default ~/.gbot/plugins)
// and parses each strictly: unlike LoadAndInitialize, an unreadable or
// JSON-invalid mcp.json / hooks.json is an ERROR naming the file — the manual
// reload's two-phase contract requires failures, not warn-and-skip. Skills,
// agents and repls keep their tolerant per-item skip semantics (same as
// startup). cfg gates plugins exactly like LoadAndInitialize.
//
// The mcp/hooks parsing below deliberately DUPLICATES the read+substitute+
// unmarshal path inside loadMcpServers/loadHooks: those internals are
// warn-and-skip (slog.Warn + zero value), so they structurally cannot report
// failure, and changing their signatures would silently alter
// LoadAndInitialize's tolerant startup contract.
func LoadPluginPieces(pluginsDir string, cfg *config.Config) ([]*PluginPieces, error) {
	var pluginList []*ResolvedPlugin
	var err error
	if pluginsDir == "" {
		pluginList, err = DiscoverPlugins()
	} else {
		pluginList, err = discoverPluginsFromDir(pluginsDir)
	}
	if err != nil {
		return nil, fmt.Errorf("plugins: discover: %w", err)
	}

	var pieces []*PluginPieces
	for _, plugin := range pluginList {
		if cfg != nil && !cfg.IsPluginEnabled(plugin.Name) {
			continue
		}
		p, err := loadPiecesOne(plugin)
		if err != nil {
			return nil, err
		}
		pieces = append(pieces, p)
	}
	return pieces, nil
}

func loadPiecesOne(plugin *ResolvedPlugin) (*PluginPieces, error) {
	p := &PluginPieces{
		Name:        plugin.Name,
		RootPath:    plugin.RootPath,
		Manifest:    plugin.Manifest,
		McpServers:  make(map[string]mcp.ScopedMcpServerConfig),
		EnvVars:     pluginEnvVars(plugin.RootPath, plugin.Name),
		Skills:      loadSkills(plugin),
		Agents:      loadAgents(plugin),
		ReplScripts: loadReplScripts(plugin),
	}

	// Strict mcp.json: the tolerant path (loadMcpServers) warns and skips;
	// the reload contract requires a hard error naming the file.
	if plugin.Manifest != nil && plugin.Manifest.McpServers != "" {
		mcpPath := resolvePluginPath(plugin.RootPath, plugin.Manifest.McpServers)
		data, err := os.ReadFile(mcpPath)
		if err != nil {
			return nil, &FileError{Path: mcpPath, Err: fmt.Errorf("read mcp.json: %w", err)}
		}
		pluginData := PluginDataDir(plugin.Name)
		expanded := substitutePluginVars(string(data), plugin.RootPath, pluginData, CacheDir())
		var cfgWrap mcp.McpJsonConfig
		if err := json.Unmarshal([]byte(expanded), &cfgWrap); err != nil {
			return nil, &FileError{Path: mcpPath, Err: fmt.Errorf("invalid mcp.json: %w", err)}
		}
		for serverName, rawMsg := range cfgWrap.McpServers {
			srvCfg, err := mcp.UnmarshalServerConfig(rawMsg)
			if err != nil {
				return nil, &FileError{Path: mcpPath, Err: fmt.Errorf("invalid server %q: %w", serverName, err)}
			}
			injectPluginEnv(srvCfg, plugin.RootPath, pluginData)
			scopedName := fmt.Sprintf("plugin:%s:%s", plugin.Name, serverName)
			p.McpServers[scopedName] = mcp.ScopedMcpServerConfig{
				Config:       srvCfg,
				Scope:        mcp.ScopeDynamic,
				PluginSource: scopedName,
			}
		}
	}

	// Strict hooks.json: unreadable-but-present or unparseable content is an
	// error (the tolerant path warns and returns nil). A missing file is the
	// normal no-hooks state, not an error.
	hooksPath := filepath.Join(plugin.RootPath, "hooks", "hooks.json")
	if data, err := os.ReadFile(hooksPath); err == nil {
		var wrapper pluginHookFile
		if jsonErr := json.Unmarshal(data, &wrapper); jsonErr == nil && wrapper.Hooks != nil {
			p.Hooks = attachPluginContext(wrapper.Hooks, plugin.RootPath)
		} else {
			var bare hooks.HooksConfig
			if bareErr := json.Unmarshal(data, &bare); bareErr != nil {
				return nil, &FileError{Path: hooksPath, Err: fmt.Errorf("invalid hooks.json: %w", bareErr)}
			}
			p.Hooks = attachPluginContext(bare, plugin.RootPath)
		}
	} else if !os.IsNotExist(err) {
		return nil, &FileError{Path: hooksPath, Err: fmt.Errorf("read hooks.json: %w", err)}
	}

	return p, nil
}
