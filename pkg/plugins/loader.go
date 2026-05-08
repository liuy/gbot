package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/markdown"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/skills"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// LoadedPlugins — aggregate result from all plugins
// ---------------------------------------------------------------------------

// LoadedPlugins holds everything loaded from all discovered plugins.
type LoadedPlugins struct {
	McpServers map[string]mcp.ScopedMcpServerConfig
	Hooks      hooks.HooksConfig
	Skills     []types.SkillCommand
	Agents     []types.AgentDefinition
	EnvVars    []string
}

// ---------------------------------------------------------------------------
// LoadAndInitialize — top-level orchestrator
//
// Source: mcpPluginIntegration.ts — loadAndInitializePlugins
// Discovers all plugins, loads their MCP servers, hooks, skills, agents.
// ---------------------------------------------------------------------------

// LoadAndInitialize discovers and loads all plugins.
// Returns nil (not error) if no plugins found — caller should treat as no-op.
func LoadAndInitialize(ctx context.Context, cwd string) (*LoadedPlugins, error) {
	pluginList, err := DiscoverPlugins()
	if err != nil {
		return nil, fmt.Errorf("plugins: discover: %w", err)
	}
	if len(pluginList) == 0 {
		return nil, nil
	}

	result := &LoadedPlugins{
		McpServers: make(map[string]mcp.ScopedMcpServerConfig),
	}

	for _, plugin := range pluginList {
		slog.Info("plugins: loading", "name", plugin.Name, "version", plugin.Manifest.Version)

		// Collect env vars for all plugins
		result.EnvVars = append(result.EnvVars, pluginEnvVars(plugin.RootPath, plugin.Name)...)

		// Load MCP servers
		maps.Copy(result.McpServers, loadMcpServers(plugin))

		// Load hooks
		result.Hooks = MergeHooks(result.Hooks, loadHooks(plugin))

		// Load skills
		result.Skills = append(result.Skills, loadSkills(plugin)...)

		// Load agents
		result.Agents = append(result.Agents, loadAgents(plugin)...)
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// loadMcpServers — read .mcp.json, substitute vars, wrap as ScopedMcpServerConfig
//
// Source: mcpPluginIntegration.ts:281-350 — resolveMcpServerConfig
// ---------------------------------------------------------------------------

func loadMcpServers(plugin *ResolvedPlugin) map[string]mcp.ScopedMcpServerConfig {
	result := make(map[string]mcp.ScopedMcpServerConfig)

	if plugin.Manifest == nil || plugin.Manifest.McpServers == "" {
		return result
	}

	// Resolve relative path
	mcpPath := resolvePluginPath(plugin.RootPath, plugin.Manifest.McpServers)

	data, err := os.ReadFile(mcpPath)
	if err != nil {
		slog.Warn("plugins: skip MCP config", "plugin", plugin.Name, "error", err)
		return result
	}

	// Substitute ${GBOT_PLUGIN_ROOT} in the raw JSON before parsing
	pluginData := PluginDataDir(plugin.Name)
	expanded := substitutePluginVars(string(data), plugin.RootPath, pluginData)

	// Parse as McpJsonConfig
	var config mcp.McpJsonConfig
	if err := json.Unmarshal([]byte(expanded), &config); err != nil {
		slog.Warn("plugins: invalid .mcp.json", "plugin", plugin.Name, "error", err)
		return result
	}

	for serverName, rawMsg := range config.McpServers {
		cfg, err := mcp.UnmarshalServerConfig(rawMsg)
		if err != nil {
			slog.Warn("plugins: invalid server config", "plugin", plugin.Name, "server", serverName, "error", err)
			continue
		}

		// Inject GBOT_PLUGIN_ROOT into server env
		injectPluginEnv(cfg, plugin.RootPath, pluginData)

		// Scoped name: plugin:{pluginName}:{serverName}
		scopedName := fmt.Sprintf("plugin:%s:%s", plugin.Name, serverName)

		result[scopedName] = mcp.ScopedMcpServerConfig{
			Config:       cfg,
			Scope:        mcp.ScopeDynamic,
			PluginSource: scopedName,
		}
	}

	return result
}

// injectPluginEnv adds GBOT_PLUGIN_ROOT/DATA to the MCP server's environment.
func injectPluginEnv(cfg mcp.McpServerConfig, pluginRoot, pluginData string) {
	switch c := cfg.(type) {
	case *mcp.StdioConfig:
		if c.Env == nil {
			c.Env = make(map[string]string)
		}
		c.Env[EnvVarPluginRoot] = pluginRoot
		c.Env[EnvVarPluginData] = pluginData
	}
}

// ---------------------------------------------------------------------------
// loadHooks — read hooks/hooks.json, unwrap, attach plugin context
//
// Source: mcpPluginIntegration.ts:351-430 — resolveHooksFromPlugin
// TS PluginHooksSchema wraps hooks in {"description":"...","hooks":{...}}
// ---------------------------------------------------------------------------

// pluginHookFile matches the TS PluginHooksSchema wrapper format.
type pluginHookFile struct {
	Description string            `json:"description"`
	Hooks       hooks.HooksConfig `json:"hooks"`
}

func loadHooks(plugin *ResolvedPlugin) hooks.HooksConfig {
	hooksPath := filepath.Join(plugin.RootPath, "hooks", "hooks.json")

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("plugins: read hooks", "plugin", plugin.Name, "error", err)
		}
		return nil
	}

	// Try wrapper format first: {"description":"...","hooks":{...}}
	var wrapper pluginHookFile
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Hooks != nil {
		return attachPluginContext(wrapper.Hooks, plugin.RootPath)
	}

	// Fallback: bare hooks config
	var bare hooks.HooksConfig
	if err := json.Unmarshal(data, &bare); err != nil {
		slog.Warn("plugins: invalid hooks.json", "plugin", plugin.Name, "error", err)
		return nil
	}

	return attachPluginContext(bare, plugin.RootPath)
}

// attachPluginContext sets PluginRoot on every HookMatcher for env injection at runtime.
func attachPluginContext(hc hooks.HooksConfig, pluginRoot string) hooks.HooksConfig {
	for _, matchers := range hc {
		for i := range matchers {
			matchers[i].PluginRoot = pluginRoot
		}
	}
	return hc
}

// mergeHooks appends plugin hooks into base config.
// Same event from plugin → append matchers (same as multi-scope settings merge).
func MergeHooks(base, pluginHooks hooks.HooksConfig) hooks.HooksConfig {
	if base == nil {
		base = make(hooks.HooksConfig)
	}
	for event, matchers := range pluginHooks {
		base[event] = append(base[event], matchers...)
	}
	return base
}

// ---------------------------------------------------------------------------
// loadSkills — scan skills/*/SKILL.md, prefix name, register
//
// Source: mcpPluginIntegration.ts:220-280 — resolveSkillCommandsFromPlugin
// ---------------------------------------------------------------------------

func loadSkills(plugin *ResolvedPlugin) []types.SkillCommand {
	var result []types.SkillCommand

	if plugin.Manifest == nil || plugin.Manifest.Skills == "" {
		return result
	}

	skillsDir := resolvePluginPath(plugin.RootPath, plugin.Manifest.Skills)

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("plugins: read skills dir", "plugin", plugin.Name, "error", err)
		}
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(skillsDir, entry.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")

		content, err := os.ReadFile(skillFile)
		if err != nil {
			continue // skip missing SKILL.md
		}

		// Prefix skill name: "autopilot" → "oh-my-claudecode:autopilot"
		prefixedName := plugin.Name + ":" + entry.Name()

		skill := skills.ParseSkill(prefixedName, skillFile, string(content), types.SkillSourcePlugin)
		if skill == nil {
			continue
		}

		// Set plugin metadata
		skill.SkillRoot = plugin.RootPath
		skill.PluginInfo = &types.PluginSkillInfo{
			PluginName: plugin.Name,
		}

		result = append(result, *skill)
	}

	return result
}

// ---------------------------------------------------------------------------
// loadAgents — scan agents/*.md, parse frontmatter, prefix agentType
//
// Source: mcpPluginIntegration.ts:180-219 — resolveAgentDefinitionsFromPlugin
// ---------------------------------------------------------------------------

func loadAgents(plugin *ResolvedPlugin) []types.AgentDefinition {
	var result []types.AgentDefinition

	agentsDir := filepath.Join(plugin.RootPath, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("plugins: read agents dir", "plugin", plugin.Name, "error", err)
		}
		return result
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		filePath := filepath.Join(agentsDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		parsed := markdown.ParseFrontmatter(string(data), filePath)
		fm := parsed.Frontmatter
		if fm == nil {
			continue
		}

		// Validate required fields
		agentType, _ := fm["name"].(string)
		if agentType == "" {
			continue
		}

		description, _ := fm["description"].(string)
		if description == "" {
			continue
		}

		// Prefix agentType: "executor" → "oh-my-claudecode:executor"
		prefixedType := plugin.Name + ":" + agentType
		content := strings.TrimSpace(parsed.Content)
		filename := strings.TrimSuffix(entry.Name(), ".md")

		def := &types.AgentDefinition{
			AgentType:    prefixedType,
			WhenToUse:    strings.ReplaceAll(description, "\\n", "\n"),
			SystemPrompt: func() string { return content },
			Source:       types.AgentSourcePlugin,
			Filename:     filename,
			BaseDir:      agentsDir,
		}

		// Parse optional fields
		if model, ok := fm["model"].(string); ok && model != "" {
			def.Model = model
		}
		if tools, ok := fm["tools"]; ok {
			def.Tools = parseStringOrArrayField(tools)
		}
		if disallowed, ok := fm["disallowedTools"]; ok {
			def.DisallowedTools = parseStringOrArrayField(disallowed)
		}
		if maxTurns, ok := fm["maxTurns"]; ok {
			if n, ok := maxTurns.(int); ok && n > 0 {
				def.MaxTurns = n
			}
		}

		result = append(result, *def)
	}

	return result
}

// parseStringOrArrayField parses a frontmatter field that can be string or []any.
func parseStringOrArrayField(v any) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		if val == "" {
			return nil
		}
		return []string{val}
	case []any:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok && s != "" {
				result = append(result, s)
			}
		}
		return result
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// resolvePluginPath resolves a relative path from the manifest against plugin root.
func resolvePluginPath(pluginRoot, relPath string) string {
	if filepath.IsAbs(relPath) {
		return relPath
	}
	return filepath.Join(pluginRoot, relPath)
}
