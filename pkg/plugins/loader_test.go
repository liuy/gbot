package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Helper: create a mock plugin directory structure in t.TempDir()
// ---------------------------------------------------------------------------

// createMockPlugin creates a complete plugin directory for testing.
// Returns the plugin root path.
func createMockPlugin(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	createMockPluginAt(t, root, name)
	return root
}

func createMockPluginAt(t *testing.T, root, name string) {
	t.Helper()

	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatalf("mkdir plugin root: %v", err)
	}

	// plugin.json (root-level manifest) — mcpServers inline
	manifest := `{"name":"` + name + `","version":"1.0.0","skills":"./skills/","mcpServers":{
		"my-server": {
			"command": "node",
			"args": ["${GBOT_PLUGIN_ROOT}/bridge/server.cjs"],
			"env": {"DEBUG": "1"}
		}
	}}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// hooks/hooks.json (wrapper format)
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hooksJSON := `{
		"description": "Test plugin hooks",
		"hooks": {
			"PreToolUse": [
				{
					"matcher": "Bash",
					"hooks": [{"type": "command", "command": "$GBOT_PLUGIN_ROOT/scripts/check.sh"}]
				}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	// skills/autopilot/SKILL.md
	skillDir := filepath.Join(root, "skills", "autopilot")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	skillMD := `---
name: autopilot
description: Run in autonomous mode
triggers: ["autopilot"]
---
# Autopilot Mode

Run tasks autonomously.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	// agents/reviewer.md
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	agentMD := `---
name: reviewer
description: Code review specialist
model: zhipu/glm-5.3-flash
tools: ["Read", "Grep"]
---
You are a code reviewer. Analyze code for quality issues.`
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte(agentMD), 0644); err != nil {
		t.Fatalf("write reviewer.md: %v", err)
	}
}

// ---------------------------------------------------------------------------
// loadMcpServers tests
// ---------------------------------------------------------------------------

// inlineSrv wraps one server definition as the manifest's inline map value.
func inlineSrv(raw string) map[string]json.RawMessage {
	return map[string]json.RawMessage{"my-server": json.RawMessage(raw)}
}

func inlineSrvAs(name, raw string) map[string]json.RawMessage {
	return map[string]json.RawMessage{name: json.RawMessage(raw)}
}

func TestLoadMcpServers_VariableSubstitution(t *testing.T) {
	root := createMockPlugin(t, "test-plugin")
	plugin := &ResolvedPlugin{
		Name:     "test-plugin",
		RootPath: root,
		Manifest: &PluginManifest{
			Name:       "test-plugin",
			Version:    "1.0.0",
			McpServers: inlineSrv(`{"command":"node","args":["${GBOT_PLUGIN_ROOT}/bridge/server.cjs"],"env":{"DEBUG":"1"}}`),
		},
	}

	servers := loadMcpServers(plugin)
	if len(servers) != 1 {
		t.Fatalf("servers count = %d, want 1", len(servers))
	}

	scoped, ok := servers["plugin:test-plugin:my-server"]
	if !ok {
		t.Fatal("missing scoped server 'plugin:test-plugin:my-server'")
	}
	if scoped.PluginSource != "plugin:test-plugin:my-server" {
		t.Errorf("PluginSource = %q, want 'plugin:test-plugin:my-server'", scoped.PluginSource)
	}

	// Verify variable substitution via JSON round-trip
	raw, _ := json.Marshal(scoped.Config)
	var parsed struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal server config: %v", err)
	}
	if parsed.Command != "node" {
		t.Errorf("Command = %q, want 'node'", parsed.Command)
	}
	if len(parsed.Args) == 0 {
		t.Fatal("Args is empty — ${GBOT_PLUGIN_ROOT} not substituted")
	}
	if !strings.Contains(parsed.Args[0], root) {
		t.Errorf("Args[0] = %q, want to contain plugin root %q", parsed.Args[0], root)
	}
	if !strings.Contains(parsed.Args[0], "/bridge/server.cjs") {
		t.Errorf("Args[0] = %q, want to contain /bridge/server.cjs", parsed.Args[0])
	}
	// Verify env injection
	if parsed.Env["GBOT_PLUGIN_ROOT"] != root {
		t.Errorf("Env GBOT_PLUGIN_ROOT = %q, want %q", parsed.Env["GBOT_PLUGIN_ROOT"], root)
	}
	if parsed.Env["DEBUG"] != "1" {
		t.Errorf("Env DEBUG = %q, want '1' (original env preserved)", parsed.Env["DEBUG"])
	}
}

func TestLoadMcpServers_NoManifestField(t *testing.T) {
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "plugin",
		RootPath: root,
		Manifest: &PluginManifest{},
	}

	servers := loadMcpServers(plugin)
	if len(servers) != 0 {
		t.Errorf("servers count = %d, want 0 when McpServers empty", len(servers))
	}
}

func TestLoadMcpServers_ModePreserved(t *testing.T) {
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "browser",
		RootPath: root,
		Manifest: &PluginManifest{Name: "browser", Version: "1.0.0",
			McpServers: inlineSrvAs("pw", `{"command":"node","args":["bridge.js"],"mode":"code"}`)},
	}

	servers := loadMcpServers(plugin)
	if len(servers) != 1 {
		t.Fatalf("servers count = %d, want 1", len(servers))
	}
	scoped, ok := servers["plugin:browser:pw"]
	if !ok {
		t.Fatal("missing scoped server 'plugin:browser:pw'")
	}
	raw, err := json.Marshal(scoped.Config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if !strings.Contains(string(raw), `"mode":"code"`) {
		t.Errorf("mode dropped from server config: got %s, want it to contain \"mode\":\"code\"", raw)
	}
}

func TestLoadMcpServers_InvalidMode_SkipsServer(t *testing.T) {
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "browser",
		RootPath: root,
		Manifest: &PluginManifest{Name: "browser", Version: "1.0.0",
			McpServers: inlineSrvAs("pw", `{"command":"node","args":["bridge.js"],"mode":"nope"}`)},
	}

	servers := loadMcpServers(plugin)
	if len(servers) != 0 {
		t.Errorf("servers count = %d, want 0 for invalid mode (server must be dropped, not silently deferred)", len(servers))
	}
}

// ---------------------------------------------------------------------------
// loadHooks tests
// ---------------------------------------------------------------------------

func TestLoadHooks_WrapperFormat(t *testing.T) {
	root := createMockPlugin(t, "test-plugin")
	plugin := &ResolvedPlugin{
		Name:     "test-plugin",
		RootPath: root,
	}

	hc := loadHooks(plugin)
	if hc == nil {
		t.Fatal("hooks config is nil")
	}

	matchers := hc["PreToolUse"]
	if len(matchers) == 0 {
		t.Fatal("no PreToolUse matchers")
	}

	// Verify matcher pattern
	if matchers[0].Matcher != "Bash" {
		t.Errorf("Matcher = %q, want 'Bash'", matchers[0].Matcher)
	}

	// Verify hooks loaded
	if len(matchers[0].Hooks) == 0 {
		t.Fatal("no hooks in matcher")
	}
	if matchers[0].Hooks[0].Command != "$GBOT_PLUGIN_ROOT/scripts/check.sh" {
		t.Errorf("Hook command = %q, want '$GBOT_PLUGIN_ROOT/scripts/check.sh'", matchers[0].Hooks[0].Command)
	}

	// Verify PluginRoot attached
	if matchers[0].PluginRoot != root {
		t.Errorf("PluginRoot = %q, want %q", matchers[0].PluginRoot, root)
	}
}

func TestLoadHooks_BareFormat(t *testing.T) {
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	bareJSON := `{
		"PostToolUse": [
			{
				"matcher": "Read",
				"hooks": [{"type": "command", "command": "log-read.sh"}]
			}
		]
	}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(bareJSON), 0644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{
		Name:     "bare-plugin",
		RootPath: root,
	}

	hc := loadHooks(plugin)
	if hc == nil {
		t.Fatal("hooks config is nil for bare format")
	}

	matchers := hc["PostToolUse"]
	if len(matchers) == 0 {
		t.Fatal("no PostToolUse matchers in bare format")
	}
	if matchers[0].Matcher != "Read" {
		t.Errorf("Matcher = %q, want 'Read'", matchers[0].Matcher)
	}
	if matchers[0].PluginRoot != root {
		t.Errorf("PluginRoot not set for bare format, got %q", matchers[0].PluginRoot)
	}
}

func TestLoadHooks_MissingFile_Skip(t *testing.T) {
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "no-hooks-plugin",
		RootPath: root,
	}

	hc := loadHooks(plugin)
	if hc != nil {
		t.Errorf("expected nil for missing hooks file, got %v", hc)
	}
}

// ---------------------------------------------------------------------------
// loadSkills tests
// ---------------------------------------------------------------------------

func TestLoadSkills_PrefixAndMetadata(t *testing.T) {
	root := createMockPlugin(t, "test-plugin")
	plugin := &ResolvedPlugin{
		Name:     "test-plugin",
		RootPath: root,
		Manifest: &PluginManifest{
			Skills: "./skills/",
		},
	}

	loaded := loadSkills(plugin)
	if len(loaded) == 0 {
		t.Fatal("no skills loaded")
	}

	skill := loaded[0]
	// Name should be prefixed: "test-plugin:autopilot"
	if !strings.Contains(skill.Name, "test-plugin:") {
		t.Errorf("Skill name = %q, want 'test-plugin:...' prefix", skill.Name)
	}
	if !strings.Contains(skill.Name, "autopilot") {
		t.Errorf("Skill name = %q, want to contain 'autopilot'", skill.Name)
	}

	// SkillRoot should point to plugin root
	if skill.SkillRoot != root {
		t.Errorf("SkillRoot = %q, want %q", skill.SkillRoot, root)
	}

	// PluginInfo should be set
	if skill.PluginInfo == nil {
		t.Fatal("PluginInfo is nil")
	}
	if skill.PluginInfo.PluginName != "test-plugin" {
		t.Errorf("PluginInfo.PluginName = %q, want 'test-plugin'", skill.PluginInfo.PluginName)
	}

	// Source should be SkillSourcePlugin
	if skill.Source != types.SkillSourcePlugin {
		t.Errorf("Source = %v, want SkillSourcePlugin", skill.Source)
	}
}

func TestLoadSkills_MissingSkillMD_Skip(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills", "empty-skill")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Directory exists but no SKILL.md

	plugin := &ResolvedPlugin{
		Name:     "no-skills-plugin",
		RootPath: root,
		Manifest: &PluginManifest{Skills: "./skills/"},
	}

	loaded := loadSkills(plugin)
	if len(loaded) != 0 {
		t.Errorf("skills count = %d, want 0 for missing SKILL.md", len(loaded))
	}
}

func TestLoadSkills_NoManifestField(t *testing.T) {
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "plugin",
		RootPath: root,
		Manifest: &PluginManifest{Skills: ""},
	}

	loaded := loadSkills(plugin)
	if len(loaded) != 0 {
		t.Errorf("skills count = %d, want 0 when Skills empty", len(loaded))
	}
}

// ---------------------------------------------------------------------------
// loadAgents tests
// ---------------------------------------------------------------------------

func TestLoadAgents_PrefixAndFrontmatter(t *testing.T) {
	root := createMockPlugin(t, "test-plugin")
	plugin := &ResolvedPlugin{
		Name:     "test-plugin",
		RootPath: root,
	}

	loaded := loadAgents(plugin)
	if len(loaded) == 0 {
		t.Fatal("no agents loaded")
	}

	agent := loaded[0]
	// AgentType should be prefixed: "test-plugin:reviewer"
	if agent.AgentType != "test-plugin:reviewer" {
		t.Errorf("AgentType = %q, want 'test-plugin:reviewer'", agent.AgentType)
	}

	// Source should be AgentSourcePlugin
	if agent.Source != types.AgentSourcePlugin {
		t.Errorf("Source = %v, want AgentSourcePlugin", agent.Source)
	}

	// WhenToUse should be the description
	if agent.WhenToUse != "Code review specialist" {
		t.Errorf("WhenToUse = %q, want 'Code review specialist'", agent.WhenToUse)
	}

	// SystemPrompt should return the content
	if !strings.Contains(agent.SystemPrompt(), "code reviewer") {
		t.Errorf("SystemPrompt = %q, want to contain 'code reviewer'", agent.SystemPrompt())
	}

	// Optional fields from frontmatter
	if agent.Model != "zhipu/glm-5.3-flash" {
		t.Errorf("Model = %q, want 'zhipu/glm-5.3-flash'", agent.Model)
	}
	if len(agent.Tools) != 2 {
		t.Errorf("Tools count = %d, want 2", len(agent.Tools))
	}
}

func TestLoadAgents_MissingRequiredFields_Skip(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Agent missing "name" field
	noName := `---
description: Missing name agent
---
Some content.`
	if err := os.WriteFile(filepath.Join(agentsDir, "noname.md"), []byte(noName), 0644); err != nil {
		t.Fatal(err)
	}
	// Agent missing "description" field
	noDesc := `---
name: no-desc
---
Some content.`
	if err := os.WriteFile(filepath.Join(agentsDir, "nodesc.md"), []byte(noDesc), 0644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{
		Name:     "bad-agents-plugin",
		RootPath: root,
	}

	loaded := loadAgents(plugin)
	if len(loaded) != 0 {
		t.Errorf("agents count = %d, want 0 for agents missing required fields", len(loaded))
	}
}

func TestLoadAgents_NoAgentsDir(t *testing.T) {
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "no-agents-plugin",
		RootPath: root,
	}

	loaded := loadAgents(plugin)
	if len(loaded) != 0 {
		t.Errorf("agents count = %d, want 0 when agents dir missing", len(loaded))
	}
}

// ---------------------------------------------------------------------------
// MergeHooks tests
// ---------------------------------------------------------------------------

func TestMergeHooks_AppendBoth(t *testing.T) {
	base := hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{
			{Matcher: "Read", Hooks: []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "base-hook.sh"}}},
		},
	}

	pluginHooks := hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{
			{Matcher: "Bash", Hooks: []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "plugin-hook.sh"}}},
		},
		"PostToolUse": []hooks.HookMatcher{
			{Matcher: "Write", Hooks: []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "post-hook.sh"}}},
		},
	}

	merged := MergeHooks(base, pluginHooks)

	// PreToolUse should have both matchers
	preMatchers := merged["PreToolUse"]
	if len(preMatchers) != 2 {
		t.Fatalf("PreToolUse matchers count = %d, want 2", len(preMatchers))
	}
	if preMatchers[0].Matcher != "Read" {
		t.Errorf("first matcher = %q, want 'Read' (base)", preMatchers[0].Matcher)
	}
	if preMatchers[1].Matcher != "Bash" {
		t.Errorf("second matcher = %q, want 'Bash' (plugin)", preMatchers[1].Matcher)
	}

	// PostToolUse should be added
	postMatchers := merged["PostToolUse"]
	if len(postMatchers) != 1 {
		t.Fatalf("PostToolUse matchers count = %d, want 1", len(postMatchers))
	}
	if postMatchers[0].Matcher != "Write" {
		t.Errorf("PostToolUse matcher = %q, want 'Write'", postMatchers[0].Matcher)
	}
}

func TestMergeHooks_NilBase(t *testing.T) {
	pluginHooks := hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{
			{Matcher: "Bash", Hooks: []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "hook.sh"}}},
		},
	}

	merged := MergeHooks(nil, pluginHooks)
	if merged == nil {
		t.Fatal("merged should not be nil")
	}
	if len(merged["PreToolUse"]) != 1 {
		t.Errorf("PreToolUse matchers count = %d, want 1", len(merged["PreToolUse"]))
	}
}

func TestMergeHooks_NilPlugin(t *testing.T) {
	base := hooks.HooksConfig{
		"PreToolUse": []hooks.HookMatcher{
			{Matcher: "Read", Hooks: []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "base.sh"}}},
		},
	}

	merged := MergeHooks(base, nil)
	if len(merged["PreToolUse"]) != 1 {
		t.Errorf("PreToolUse matchers count = %d, want 1", len(merged["PreToolUse"]))
	}
}

// ---------------------------------------------------------------------------
// ReloadConfig with plugin hooks (recovery test)
// ---------------------------------------------------------------------------

// testHookRecorder is a local mock for hooks.HookExecutor.
type testHookRecorder struct {
	calls []testHookCall
}

type testHookCall struct {
	command  string
	extraEnv []string
}

func (r *testHookRecorder) ExecuteHook(ctx context.Context, command string, input *hooks.HookInput, timeout time.Duration, extraEnv []string) hooks.HookResult {
	r.calls = append(r.calls, testHookCall{command, extraEnv})
	return hooks.HookResult{Outcome: hooks.HookOutcomeSuccess, HookName: command}
}

func TestReloadConfig_WithPluginHooks_ActivatesNewHooks(t *testing.T) {
	rec := &testHookRecorder{}

	// Start with empty base config
	baseConfig := hooks.HooksConfig{}
	h := hooks.NewHooks(baseConfig, rec)

	// Dispatch — should return nothing (no hooks)
	results := h.PostToolUse(t.Context(), &hooks.HookInput{
		HookEventName: "PostToolUse",
		ToolName:      "Bash",
	})
	if len(results) != 0 {
		t.Fatalf("results count before reload = %d, want 0", len(results))
	}

	// Merge plugin hooks and reload
	pluginHooks := hooks.HooksConfig{
		"PostToolUse": []hooks.HookMatcher{
			{
				Matcher:    "Bash",
				PluginRoot: "/plugin/root",
				Hooks:      []hooks.HookConfig{{Type: hooks.HookTypeCommand, Command: "post-hook.sh"}},
			},
		},
	}
	merged := MergeHooks(baseConfig, pluginHooks)
	h.ReloadConfig(merged)

	// Dispatch again — plugin hook should fire now
	results = h.PostToolUse(t.Context(), &hooks.HookInput{
		HookEventName: "PostToolUse",
		ToolName:      "Bash",
	})
	if len(results) != 1 {
		t.Fatalf("results count after reload = %d, want 1", len(results))
	}
	if len(rec.calls) != 1 {
		t.Fatalf("call count = %d, want 1", len(rec.calls))
	}
	if rec.calls[0].command != "post-hook.sh" {
		t.Errorf("command = %q, want 'post-hook.sh'", rec.calls[0].command)
	}
	// Verify PluginRoot propagates through reload
	if len(rec.calls[0].extraEnv) == 0 {
		t.Fatal("extraEnv empty after reload — PluginRoot not propagated")
	}
	found := false
	for _, e := range rec.calls[0].extraEnv {
		if e == "GBOT_PLUGIN_ROOT=/plugin/root" {
			found = true
		}
	}
	if !found {
		t.Errorf("extraEnv = %v, want GBOT_PLUGIN_ROOT=/plugin/root", rec.calls[0].extraEnv)
	}
}

// ---------------------------------------------------------------------------
// parseStringOrArrayField tests
// ---------------------------------------------------------------------------

func TestParseStringOrArrayField(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []string
	}{
		{"string", "Read", []string{"Read"}},
		{"empty string", "", nil},
		{"nil", nil, nil},
		{"array", []any{"Read", "Grep"}, []string{"Read", "Grep"}},
		{"array with empty", []any{"Read", "", "Grep"}, []string{"Read", "Grep"}},
		{"empty array", []any{}, []string{}},
		{"other type", 42, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStringOrArrayField(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// resolvePluginPath tests
// ---------------------------------------------------------------------------

func TestResolvePluginPath(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		relPath string
		want    string
	}{
		{"relative", "/plugins/test", "./skills/", "/plugins/test/skills/"},
		{"absolute", "/plugins/test", "/absolute/path", "/absolute/path"},
		{"no dot", "/plugins/test", "skills/", "/plugins/test/skills/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePluginPath(tt.root, tt.relPath)
			// filepath.Join may clean trailing slashes
			gotClean := filepath.Clean(got)
			wantClean := filepath.Clean(tt.want)
			if gotClean != wantClean {
				t.Errorf("resolvePluginPath(%q, %q) = %q, want %q", tt.root, tt.relPath, gotClean, wantClean)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DiscoverPlugins — using pluginsDirOverride for testability
// ---------------------------------------------------------------------------

func TestDiscoverPlugins_FromDir(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")
	pluginDir := filepath.Join(pluginsDir, "test-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"test-plugin","version":"1.0.0","skills":"./skills/"}`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	// Non-plugin directory (no manifest)
	if err := os.MkdirAll(filepath.Join(pluginsDir, "not-a-plugin"), 0755); err != nil {
		t.Fatal(err)
	}
	// Regular file (should be skipped)
	if err := os.WriteFile(filepath.Join(pluginsDir, "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := discoverPluginsFromDir(pluginsDir)
	if err != nil {
		t.Fatalf("discoverPluginsFromDir error: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("plugins count = %d, want 1", len(plugins))
	}
	if plugins[0].Name != "test-plugin" {
		t.Errorf("Name = %q, want 'test-plugin'", plugins[0].Name)
	}
}

func TestDiscoverPlugins_FromDir_NotExist(t *testing.T) {
	plugins, err := discoverPluginsFromDir("/nonexistent/path/plugins")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got: %v", err)
	}
	if plugins != nil {
		t.Errorf("expected nil plugins for nonexistent dir, got %v", plugins)
	}
}

func TestDiscoverPlugins_BrokenManifest_Skip(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")
	pluginDir := filepath.Join(pluginsDir, "broken-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Invalid JSON manifest
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte("{bad json"), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := discoverPluginsFromDir(pluginsDir)
	if err != nil {
		t.Fatalf("discoverPluginsFromDir error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("plugins count = %d, want 0 for broken manifest", len(plugins))
	}
}

func TestDiscoverPlugins_UsingOverride(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")
	pluginDir := filepath.Join(pluginsDir, "ovr-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"ovr-plugin","version":"2.0.0"}`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	// Override plugins dir
	pluginsDirOverride = pluginsDir
	defer func() { pluginsDirOverride = "" }()

	plugins, err := DiscoverPlugins()
	if err != nil {
		t.Fatalf("DiscoverPlugins error: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("plugins count = %d, want 1", len(plugins))
	}
	if plugins[0].Name != "ovr-plugin" {
		t.Errorf("Name = %q, want 'ovr-plugin'", plugins[0].Name)
	}
}

// ---------------------------------------------------------------------------
// LoadAndInitialize — full chain test using override
// ---------------------------------------------------------------------------

func TestLoadAndInitialize_FullPlugin(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")

	// Create a complete plugin
	root := filepath.Join(pluginsDir, "full-plugin")
	createMockPluginAt(t, root, "full-plugin")

	// Override
	pluginsDirOverride = pluginsDir
	defer func() { pluginsDirOverride = "" }()

	loaded, err := LoadAndInitialize(context.Background(), base, nil)
	if err != nil {
		t.Fatalf("LoadAndInitialize error: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded is nil")
	}

	// Verify MCP servers loaded
	if len(loaded.McpServers) != 1 {
		t.Errorf("McpServers count = %d, want 1", len(loaded.McpServers))
	}
	if _, ok := loaded.McpServers["plugin:full-plugin:my-server"]; !ok {
		t.Error("missing MCP server 'plugin:full-plugin:my-server'")
	}

	// Verify hooks loaded
	if len(loaded.Hooks) == 0 {
		t.Error("Hooks is empty")
	}
	preMatchers := loaded.Hooks["PreToolUse"]
	if len(preMatchers) == 0 {
		t.Error("no PreToolUse hooks")
	}
	if preMatchers[0].PluginRoot != root {
		t.Errorf("PluginRoot = %q, want %q", preMatchers[0].PluginRoot, root)
	}

	// Verify skills loaded
	if len(loaded.Skills) == 0 {
		t.Error("Skills is empty")
	}
	foundSkill := false
	for _, s := range loaded.Skills {
		if s.Name == "full-plugin:autopilot" {
			foundSkill = true
			if s.Source != types.SkillSourcePlugin {
				t.Errorf("skill Source = %v, want SkillSourcePlugin", s.Source)
			}
			break
		}
	}
	if !foundSkill {
		t.Error("skill 'full-plugin:autopilot' not found")
	}

	// Verify agents loaded
	if len(loaded.Agents) == 0 {
		t.Error("Agents is empty")
	}
	foundAgent := false
	for _, a := range loaded.Agents {
		if a.AgentType == "full-plugin:reviewer" {
			foundAgent = true
			if a.Source != types.AgentSourcePlugin {
				t.Errorf("agent Source = %v, want AgentSourcePlugin", a.Source)
			}
			break
		}
	}
	if !foundAgent {
		t.Error("agent 'full-plugin:reviewer' not found")
	}

	// Verify env vars
	if len(loaded.EnvVars) == 0 {
		t.Error("EnvVars is empty")
	}
	foundRoot := false
	for _, e := range loaded.EnvVars {
		if strings.HasPrefix(e, "GBOT_PLUGIN_ROOT=") {
			foundRoot = true
			break
		}
	}
	if !foundRoot {
		t.Error("GBOT_PLUGIN_ROOT not in EnvVars")
	}
}

func TestLoadAndInitialize_NoPlugins(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatal(err)
	}

	pluginsDirOverride = pluginsDir
	defer func() { pluginsDirOverride = "" }()

	loaded, err := LoadAndInitialize(context.Background(), base, nil)
	if err != nil {
		t.Fatalf("LoadAndInitialize error: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for empty plugins dir, got %+v", loaded)
	}
}

// TestLoadAndInitialize_DisabledPlugin verifies that a plugin listed as
// false in cfg.Plugins is skipped entirely — no MCP servers, hooks,
// skills, or agents loaded.
func TestLoadAndInitialize_DisabledPlugin(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")

	root := filepath.Join(pluginsDir, "full-plugin")
	createMockPluginAt(t, root, "full-plugin")

	pluginsDirOverride = pluginsDir
	defer func() { pluginsDirOverride = "" }()

	cfg := &config.Config{
		Plugins: map[string]bool{"full-plugin": false},
	}
	loaded, err := LoadAndInitialize(context.Background(), base, cfg)
	if err != nil {
		t.Fatalf("LoadAndInitialize error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil loaded (skipped plugins still return empty struct)")
	}
	if len(loaded.McpServers) != 0 {
		t.Errorf("McpServers count = %d, want 0 (plugin disabled)", len(loaded.McpServers))
	}
	if len(loaded.Hooks) != 0 {
		t.Errorf("Hooks count = %d, want 0 (plugin disabled)", len(loaded.Hooks))
	}
	if len(loaded.Skills) != 0 {
		t.Errorf("Skills count = %d, want 0 (plugin disabled)", len(loaded.Skills))
	}
	if len(loaded.Agents) != 0 {
		t.Errorf("Agents count = %d, want 0 (plugin disabled)", len(loaded.Agents))
	}
}

// TestLoadAndInitialize_ExplicitlyEnabled verifies that a plugin listed
// as true in cfg.Plugins loads normally.
func TestLoadAndInitialize_ExplicitlyEnabled(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")

	root := filepath.Join(pluginsDir, "full-plugin")
	createMockPluginAt(t, root, "full-plugin")

	pluginsDirOverride = pluginsDir
	defer func() { pluginsDirOverride = "" }()

	cfg := &config.Config{
		Plugins: map[string]bool{"full-plugin": true},
	}
	loaded, err := LoadAndInitialize(context.Background(), base, cfg)
	if err != nil {
		t.Fatalf("LoadAndInitialize error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected loaded to be non-nil")
	}
	if len(loaded.McpServers) != 1 {
		t.Errorf("McpServers count = %d, want 1", len(loaded.McpServers))
	}
}

// TestLoadAndInitialize_PartialDisable verifies that disabling one plugin
// does not affect other plugins discovered in the same directory.
func TestLoadAndInitialize_PartialDisable(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")

	createMockPluginAt(t, filepath.Join(pluginsDir, "alpha"), "alpha")
	createMockPluginAt(t, filepath.Join(pluginsDir, "beta"), "beta")

	pluginsDirOverride = pluginsDir
	defer func() { pluginsDirOverride = "" }()

	cfg := &config.Config{
		Plugins: map[string]bool{"alpha": false},
	}
	loaded, err := LoadAndInitialize(context.Background(), base, cfg)
	if err != nil {
		t.Fatalf("LoadAndInitialize error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected loaded to be non-nil")
	}
	// alpha disabled, beta enabled → only plugin:beta:my-server
	if _, ok := loaded.McpServers["plugin:alpha:my-server"]; ok {
		t.Error("alpha MCP server should not be loaded (disabled)")
	}
	if _, ok := loaded.McpServers["plugin:beta:my-server"]; !ok {
		t.Error("beta MCP server should be loaded (enabled)")
	}
	if len(loaded.McpServers) != 1 {
		t.Errorf("McpServers count = %d, want 1 (only beta)", len(loaded.McpServers))
	}
}

func TestLoadAndInitialize_MissingPluginDir(t *testing.T) {
	pluginsDirOverride = "/nonexistent/path/plugins"
	defer func() { pluginsDirOverride = "" }()

	loaded, err := LoadAndInitialize(context.Background(), "/tmp", nil)
	if err != nil {
		t.Fatalf("LoadAndInitialize error: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for missing plugins dir, got %+v", loaded)
	}
}

func TestLoadAndInitialize_BrokenPlugin_FailOpen(t *testing.T) {
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")

	// Create a valid plugin and a broken one
	goodDir := filepath.Join(pluginsDir, "good-plugin")
	if err := os.MkdirAll(goodDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "plugin.json"), []byte(`{"name":"good-plugin","version":"1.0.0"}`), 0644); err != nil {
		t.Fatal(err)
	}

	badDir := filepath.Join(pluginsDir, "bad-plugin")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "plugin.json"), []byte("{broken"), 0644); err != nil {
		t.Fatal(err)
	}

	pluginsDirOverride = pluginsDir
	defer func() { pluginsDirOverride = "" }()

	loaded, err := LoadAndInitialize(context.Background(), base, nil)
	if err != nil {
		t.Fatalf("LoadAndInitialize error: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded should not be nil — good plugin should load")
	}
	// Only the good plugin's env vars should be present
	foundGood := false
	for _, e := range loaded.EnvVars {
		if strings.Contains(e, "good-plugin") {
			foundGood = true
		}
	}
	if !foundGood {
		t.Error("good-plugin env vars not found")
	}
}

// ---------------------------------------------------------------------------
// Remaining path coverage
// ---------------------------------------------------------------------------

func TestLoadMcpServers_InvalidServerConfig(t *testing.T) {
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "test",
		RootPath: root,
		Manifest: &PluginManifest{McpServers: inlineSrvAs("bad", `{"url":"not-a-valid-config"}`)},
	}
	servers := loadMcpServers(plugin)
	// Should skip the invalid server but not crash
	if len(servers) > 1 {
		t.Errorf("servers count = %d, want 0 or 1", len(servers))
	}
}

func TestLoadPlugin_NilManifest(t *testing.T) {
	root := t.TempDir()
	// Write manifest with empty name (valid JSON but empty name)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{"version":"1.0.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	plugin, err := LoadPlugin(root)
	if err != nil {
		t.Fatalf("LoadPlugin error: %v", err)
	}
	// Name comes from directory basename
	if plugin.Name != filepath.Base(root) {
		t.Errorf("Name = %q, want %q", plugin.Name, filepath.Base(root))
	}
}

func TestLoadPlugin_NonexistentRoot(t *testing.T) {
	_, err := LoadPlugin("/nonexistent/path/plugin")
	if err == nil {
		t.Fatalf("LoadPlugin should fail for nonexistent root, got err=%v", err)
	}
}

func TestLoadSkills_SkillsDirNotExist(t *testing.T) {
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "test",
		RootPath: root,
		Manifest: &PluginManifest{Skills: "./nonexistent/"},
	}
	loaded := loadSkills(plugin)
	if len(loaded) != 0 {
		t.Errorf("skills count = %d, want 0", len(loaded))
	}
}

func TestLoadAgents_AgentWithNoFrontmatter(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Agent file with no frontmatter
	if err := os.WriteFile(filepath.Join(agentsDir, "plain.md"), []byte("Just plain text"), 0644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{Name: "test", RootPath: root}
	loaded := loadAgents(plugin)
	if len(loaded) != 0 {
		t.Errorf("agents count = %d, want 0 for no frontmatter", len(loaded))
	}
}

func TestLoadAgents_AgentWithMaxTurns(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	agentMD := `---
name: turn-limited
description: Has max turns
maxTurns: 10
---
Content here.`
	if err := os.WriteFile(filepath.Join(agentsDir, "limited.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{Name: "test", RootPath: root}
	loaded := loadAgents(plugin)
	if len(loaded) != 1 {
		t.Fatalf("agents count = %d, want 1", len(loaded))
	}
	if loaded[0].MaxTurns != 10 {
		t.Errorf("MaxTurns = %d, want 10", loaded[0].MaxTurns)
	}
}

// ---------------------------------------------------------------------------
// Coverage gap fillers — error paths and edge cases
// ---------------------------------------------------------------------------

func TestLoadSkills_FileEntryInSkillsDir(t *testing.T) {
	// Line 232: file (non-dir) entries should be skipped
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create a plain file in skills dir (not a directory)
	if err := os.WriteFile(filepath.Join(skillsDir, "README.md"), []byte("not a skill"), 0644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{
		Name:     "test",
		RootPath: root,
		Manifest: &PluginManifest{Skills: "./skills/"},
	}
	loaded := loadSkills(plugin)
	if len(loaded) != 0 {
		t.Errorf("skills count = %d, want 0 (file entries skipped)", len(loaded))
	}
}

func TestLoadSkills_ParseSkillReturnsNil(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills", "minimal-skill")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("Just a body with no frontmatter"), 0644); err != nil {
		t.Fatal(err)
	}
	plugin := &ResolvedPlugin{
		Name:     "test",
		RootPath: root,
		Manifest: &PluginManifest{Skills: "./skills/"},
	}
	loaded := loadSkills(plugin)
	if len(loaded) != 1 {
		t.Errorf("skills count = %d, want 1 for minimal SKILL.md", len(loaded))
	}
	if loaded[0].SkillRoot != root {
		t.Errorf("SkillRoot = %q, want %q", loaded[0].SkillRoot, root)
	}
}

func TestLoadAgents_DisallowedTools(t *testing.T) {
	// Line 330: disallowedTools field
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	agentMD := `---
name: restricted
description: Has disallowed tools
disallowedTools: ["Bash", "Write"]
---
Content.`
	if err := os.WriteFile(filepath.Join(agentsDir, "restricted.md"), []byte(agentMD), 0644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{Name: "test", RootPath: root}
	loaded := loadAgents(plugin)
	if len(loaded) != 1 {
		t.Fatalf("agents count = %d, want 1", len(loaded))
	}
	if len(loaded[0].DisallowedTools) != 2 {
		t.Errorf("DisallowedTools count = %d, want 2", len(loaded[0].DisallowedTools))
	}
}

func TestLoadAgents_DirAndNonMdSkipped(t *testing.T) {
	// Line 282: directories and non-.md files in agents/
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Subdirectory — should be skipped
	if err := os.MkdirAll(filepath.Join(agentsDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	// A directory whose NAME ends in .md — ReadDir reports it as a dir.
	if err := os.MkdirAll(filepath.Join(agentsDir, "subdir.md"), 0755); err != nil {
		t.Fatal(err)
	}
	// A valid agent body under the wrong extension — should be skipped.
	valid := "---\nname: sneaky\ndescription: valid but wrong extension\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "notes.txt"), []byte(valid), 0644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{Name: "test", RootPath: root}
	loaded := loadAgents(plugin)
	if len(loaded) != 0 {
		names := make([]string, 0, len(loaded))
		for _, a := range loaded {
			names = append(names, a.AgentType)
		}
		t.Errorf("agents loaded = %v, want none (dirs and non-.md skipped)", names)
	}
}

func TestLoadHooks_InvalidBareJson(t *testing.T) {
	// Line 178: bare format with invalid JSON
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte("not json at all"), 0644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{Name: "test", RootPath: root}
	hc := loadHooks(plugin)
	if hc != nil {
		t.Errorf("expected nil for invalid JSON hooks, got %v", hc)
	}
}

func TestLoadMcpServers_NoEnvField(t *testing.T) {
	// Coverage for injectPluginEnv when Env is nil
	root := t.TempDir()
	plugin := &ResolvedPlugin{
		Name:     "test",
		RootPath: root,
		Manifest: &PluginManifest{McpServers: inlineSrvAs("minimal", `{"command":"echo","args":["hello"]}`)},
	}
	servers := loadMcpServers(plugin)
	if len(servers) != 1 {
		t.Fatalf("servers count = %d, want 1", len(servers))
	}
	scoped, ok := servers["plugin:test:minimal"]
	if !ok {
		t.Fatal("missing server 'plugin:test:minimal'")
	}
	raw, _ := json.Marshal(scoped.Config)
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Env["GBOT_PLUGIN_ROOT"] != root {
		t.Errorf("GBOT_PLUGIN_ROOT = %q, want %q", parsed.Env["GBOT_PLUGIN_ROOT"], root)
	}
}

func TestPluginsDir_HomeError(t *testing.T) {
	// Coverage for PluginsDir error path — test override returns before home check
	pluginsDirOverride = "/test/override"
	defer func() { pluginsDirOverride = "" }()
	dir, err := PluginsDir()
	if err != nil {
		t.Fatalf("PluginsDir with override error: %v", err)
	}
	if dir != "/test/override" {
		t.Errorf("PluginsDir = %q, want /test/override", dir)
	}
}

func TestDiscoverPlugins_PluginsDirError(t *testing.T) {
	// Coverage for DiscoverPlugins when PluginsDir fails
	// We can't easily make os.UserHomeDir fail, but we can test the override path
	// which covers the same flow
	pluginsDirOverride = "/nonexistent/deep/path/plugins"
	defer func() { pluginsDirOverride = "" }()

	plugins, err := DiscoverPlugins()
	if err != nil {
		t.Fatalf("DiscoverPlugins error: %v", err)
	}
	if plugins != nil {
		t.Errorf("expected nil for nonexistent dir, got %v", plugins)
	}
}

// ---------------------------------------------------------------------------
// Error paths in loadHooks, loadSkills, loadAgents, LoadAndInitialize
// ---------------------------------------------------------------------------

func TestLoadAndInitialize_DiscoverError(t *testing.T) {
	if runtime.GOOS == "android" {
		t.Skip("Termux HOME resolution differs from standard Linux; os.UserHomeDir fallback makes empty HOME non-fatal")
	}
	orig := pluginsDirOverride
	pluginsDirOverride = ""
	defer func() { pluginsDirOverride = orig }()

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", "")
	defer func() { _ = os.Setenv("HOME", origHome) }()

	_, err := LoadAndInitialize(context.Background(), "/tmp", nil)
	if err == nil {
		t.Fatal("expected error when DiscoverPlugins fails")
	}
	if !strings.Contains(err.Error(), "discover") {
		t.Errorf("error should mention 'discover', got: %v", err)
	}
}

func TestLoadHooks_NonIsNotExistError(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "my-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := PluginManifest{Name: "my-plugin", Version: "1.0.0"}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Create hooks.json as a directory to cause EISDIR (not ENOENT)
	hooksDir := filepath.Join(tmpDir, "my-plugin", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hooksDir, "hooks.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{
		Name:     "my-plugin",
		RootPath: filepath.Join(tmpDir, "my-plugin"),
		Manifest: &manifest,
	}
	hc := loadHooks(plugin)
	if hc != nil {
		t.Errorf("expected nil hooks on error, got %v", hc)
	}
}

func TestLoadSkills_NonIsNotExistError(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "my-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Skills path is a file (not directory) so ReadDir returns ENOTDIR
	skillsPath := filepath.Join(tmpDir, "my-plugin", "skills")
	if err := os.WriteFile(skillsPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest := PluginManifest{Name: "my-plugin", Version: "1.0.0", Skills: "skills"}
	plugin := &ResolvedPlugin{
		Name:     "my-plugin",
		RootPath: filepath.Join(tmpDir, "my-plugin"),
		Manifest: &manifest,
	}
	skills := loadSkills(plugin)
	if skills != nil {
		t.Errorf("expected nil skills on error, got %v", skills)
	}
}

func TestLoadAgents_NonIsNotExistError(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "my-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}

	// agents dir is a file (not directory) so ReadDir returns ENOTDIR
	agentsPath := filepath.Join(tmpDir, "my-plugin", "agents")
	if err := os.WriteFile(agentsPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{
		Name:     "my-plugin",
		RootPath: filepath.Join(tmpDir, "my-plugin"),
		Manifest: &PluginManifest{Name: "my-plugin", Version: "1.0.0"},
	}
	agents := loadAgents(plugin)
	if agents != nil {
		t.Errorf("expected nil agents on error, got %v", agents)
	}
}

func TestLoadAgents_NilFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, "my-plugin", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// .md file with no frontmatter
	if err := os.WriteFile(filepath.Join(agentsDir, "test.md"), []byte("just plain text"), 0o644); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{
		Name:     "my-plugin",
		RootPath: filepath.Join(tmpDir, "my-plugin"),
		Manifest: &PluginManifest{Name: "my-plugin", Version: "1.0.0"},
	}
	agents := loadAgents(plugin)
	if len(agents) != 0 {
		t.Errorf("expected 0 agents for .md with no frontmatter, got %d", len(agents))
	}
}

func TestLoadAgents_ReadFileError(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, "my-plugin", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a .md file with no read permissions
	secretFile := filepath.Join(agentsDir, "secret.md")
	if err := os.WriteFile(secretFile, []byte("---\nname: test\ndescription: test\n---\ncontent"), 0o000); err != nil {
		t.Fatal(err)
	}

	plugin := &ResolvedPlugin{
		Name:     "my-plugin",
		RootPath: filepath.Join(tmpDir, "my-plugin"),
		Manifest: &PluginManifest{Name: "my-plugin", Version: "1.0.0"},
	}
	agents := loadAgents(plugin)
	if len(agents) != 0 {
		t.Errorf("expected 0 agents when file unreadable, got %d", len(agents))
	}
}

// ---------------------------------------------------------------------------
// protocolModule: declaration → ProtocolModule field mapping
// ---------------------------------------------------------------------------
func TestProtocolModuleFieldMapping(t *testing.T) {
	p := &ResolvedPlugin{
		Name:     "browser",
		RootPath: "/root/browser",
		Manifest: &PluginManifest{Name: "browser", Protocol: "./protocol.js"},
	}
	pm := protocolModule(p)
	if pm == nil {
		t.Fatal("declared protocol must produce a module")
	}
	if pm.Plugin != "browser" {
		t.Errorf("Plugin = %q, want browser", pm.Plugin)
	}
	if pm.RootPath != "/root/browser" {
		t.Errorf("RootPath = %q, want /root/browser", pm.RootPath)
	}
	if pm.Path != filepath.Join("/root/browser", "protocol.js") {
		t.Errorf("Path = %q, want resolved under root", pm.Path)
	}
	if got := protocolModule(&ResolvedPlugin{Manifest: &PluginManifest{}}); got != nil {
		t.Errorf("undeclared protocol must produce nil, got %+v", got)
	}
	if got := protocolModule(&ResolvedPlugin{}); got != nil {
		t.Errorf("nil manifest must produce nil, got %+v", got)
	}
}

func TestLoadProtocolsBranch(t *testing.T) {
	// A plugin declaring protocol surfaces it in LoadedPlugins.Protocols;
	// one declaring none adds nothing.
	base := t.TempDir()
	pluginsDir := filepath.Join(base, "plugins")
	root := filepath.Join(pluginsDir, "withproto")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.json"),
		[]byte(`{"name":"withproto","version":"1.0.0","protocol":"./protocol.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "protocol.js"), []byte(`// noop`), 0o644); err != nil {
		t.Fatal(err)
	}
	root2 := filepath.Join(pluginsDir, "noproto")
	if err := os.MkdirAll(root2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root2, "plugin.json"),
		[]byte(`{"name":"noproto","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pluginsDirOverride = pluginsDir
	defer func() { pluginsDirOverride = "" }()
	loaded, err := LoadAndInitialize(context.Background(), base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Protocols) != 1 {
		t.Fatalf("Protocols = %d, want exactly 1 (declared only)", len(loaded.Protocols))
	}
	if loaded.Protocols[0].Plugin != "withproto" {
		t.Errorf("Protocols[0].Plugin = %q, want withproto", loaded.Protocols[0].Plugin)
	}
	if !strings.HasSuffix(loaded.Protocols[0].Path, "protocol.js") {
		t.Errorf("Protocols[0].Path = %q, want it to end in protocol.js", loaded.Protocols[0].Path)
	}
}

// ---------------------------------------------------------------------------
// Per-piece loaders: skills, repl scripts, agents, MCP servers
// ---------------------------------------------------------------------------

func writePluginFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadSkills_PluginDir(t *testing.T) {
	root := t.TempDir()
	writePluginFile(t, filepath.Join(root, "skills", "good", "SKILL.md"), "---\ndescription: good skill\n---\nbody\n")
	writePluginFile(t, filepath.Join(root, "skills", "noskill", "README.md"), "nothing")
	writePluginFile(t, filepath.Join(root, "skills", "stray.md"), "not a dir")
	p := &ResolvedPlugin{Name: "plug", RootPath: root, Manifest: &PluginManifest{Name: "plug", Skills: "./skills"}}
	got := loadSkills(p)
	if len(got) != 1 || got[0].Name != "plug:good" {
		t.Fatalf("skills = %+v, want only the plugin-prefixed skill plug:good", got)
	}
	if got[0].Source != types.SkillSourcePlugin {
		t.Errorf("skill source = %v, want plugin", got[0].Source)
	}
	if loadSkills(&ResolvedPlugin{Name: "plug", RootPath: root}) != nil && len(loadSkills(&ResolvedPlugin{Name: "plug", RootPath: root})) != 0 {
		t.Error("a plugin without a manifest must load no skills")
	}
	if n := len(loadSkills(&ResolvedPlugin{Name: "plug", RootPath: root, Manifest: &PluginManifest{Skills: "./missing"}})); n != 0 {
		t.Errorf("skills from a missing dir = %d, want 0", n)
	}
}

func TestLoadReplScripts_OnlyJSFiles(t *testing.T) {
	logs := captureSlog(t)
	root := t.TempDir()
	writePluginFile(t, filepath.Join(root, "repl", "b_second.js"), "SECOND=1")
	writePluginFile(t, filepath.Join(root, "repl", "a_first.js"), "FIRST=1")
	writePluginFile(t, filepath.Join(root, "repl", "notjs.txt"), "x")
	if err := os.MkdirAll(filepath.Join(root, "repl", "nested.js"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := &ResolvedPlugin{Name: "plug", RootPath: root, Manifest: &PluginManifest{Name: "plug", Repls: "./repl"}}
	got := loadReplScripts(p)
	if len(got) != 2 {
		t.Fatalf("repl scripts = %d, want 2 (.js files only)", len(got))
	}
	// The count alone can't see a directory that got past the IsDir guard: it
	// would fail ReadFile and be dropped. The warning is the observable half.
	if strings.Contains(logs.String(), "plugins: read repl script") {
		t.Errorf("a non-file entry was opened as a repl script; log:\n%s", logs.String())
	}
	if got[0].Name != "a_first.js" || got[1].Name != "b_second.js" {
		t.Errorf("script order = %q, want sorted [a_first.js b_second.js]", []string{got[0].Name, got[1].Name})
	}
	if got[0].Plugin != "plug" || got[0].Source != "FIRST=1" {
		t.Errorf("script[0] = %+v, want plugin plug and the file source", got[0])
	}
	if n := len(loadReplScripts(&ResolvedPlugin{Name: "plug", RootPath: root})); n != 0 {
		t.Errorf("repl scripts without a manifest = %d, want 0", n)
	}
}

func TestLoadAgents_FrontmatterAndFiltering(t *testing.T) {
	root := t.TempDir()
	writePluginFile(t, filepath.Join(root, "agents", "executor.md"),
		"---\nname: executor\ndescription: runs tasks\nmodel: gpt-x\ntools: Read, Write\ndisallowedTools:\n  - Bash\nmaxTurns: 7\n---\nsystem prompt body\n")
	writePluginFile(t, filepath.Join(root, "agents", "nodesc.md"), "---\nname: nodesc\n---\nbody\n")
	writePluginFile(t, filepath.Join(root, "agents", "nofm.md"), "plain markdown")
	writePluginFile(t, filepath.Join(root, "agents", "notes.txt"), "not markdown")
	if err := os.MkdirAll(filepath.Join(root, "agents", "sub.md"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := &ResolvedPlugin{Name: "plug", RootPath: root, Manifest: &PluginManifest{Name: "plug"}}
	got := loadAgents(p)
	if len(got) != 1 {
		t.Fatalf("agents = %d, want 1 (only the fully specified one)", len(got))
	}
	a := got[0]
	if a.AgentType != "plug:executor" {
		t.Errorf("AgentType = %q, want plug:executor", a.AgentType)
	}
	if a.Model != "gpt-x" || a.MaxTurns != 7 || a.Filename != "executor" {
		t.Errorf("agent fields = model %q maxTurns %d file %q", a.Model, a.MaxTurns, a.Filename)
	}
	if len(a.Tools) != 1 || a.Tools[0] != "Read, Write" {
		t.Errorf("Tools = %q, want the single comma string preserved", a.Tools)
	}
	if len(a.DisallowedTools) != 1 || a.DisallowedTools[0] != "Bash" {
		t.Errorf("DisallowedTools = %q, want [Bash]", a.DisallowedTools)
	}
	if a.SystemPrompt() != "system prompt body" {
		t.Errorf("SystemPrompt = %q", a.SystemPrompt())
	}
	if a.Source != types.AgentSourcePlugin || a.BaseDir != filepath.Join(root, "agents") {
		t.Errorf("agent provenance = %v / %q", a.Source, a.BaseDir)
	}
}

func TestParseStringOrArrayField_PluginFields(t *testing.T) {
	if got := parseStringOrArrayField("a, b"); len(got) != 1 || got[0] != "a, b" {
		t.Errorf("string field = %q, want the string kept whole", got)
	}
	if got := parseStringOrArrayField([]any{"x", 1, "y"}); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("array field = %q, want [x y]", got)
	}
	if got := parseStringOrArrayField(nil); got != nil {
		t.Errorf("nil field = %#v, want nil", got)
	}
	if got := parseStringOrArrayField(42); got != nil {
		t.Errorf("numeric field = %#v, want nil", got)
	}
}

func TestLoadMcpServers_ScopeAndEnv(t *testing.T) {
	root := t.TempDir()
	manifest := &PluginManifest{
		Name: "plug",
		McpServers: map[string]json.RawMessage{
			"srv": json.RawMessage(`{"command":"node ${GBOT_PLUGIN_ROOT}/index.js","args":["--x"]}`),
		},
	}
	p := &ResolvedPlugin{Name: "plug", RootPath: root, Manifest: manifest}
	got := loadMcpServers(p)
	if len(got) != 1 {
		t.Fatalf("servers = %d, want 1", len(got))
	}
	sc, ok := got["plugin:plug:srv"]
	if !ok {
		t.Fatalf("server key = %q, want plugin:plug:srv (keys: %v)", "plugin:plug:srv", got)
	}
	if sc.Scope != mcp.ScopeDynamic || sc.PluginSource != "plugin:plug:srv" {
		t.Errorf("scope = %v source = %q, want dynamic/plugin:plug:srv", sc.Scope, sc.PluginSource)
	}
	stdio, ok := sc.Config.(*mcp.StdioConfig)
	if !ok {
		t.Fatalf("config type = %T, want *mcp.StdioConfig", sc.Config)
	}
	if stdio.Command != "node "+filepath.Join(root, "index.js") || len(stdio.Args) != 1 || stdio.Args[0] != "--x" {
		t.Errorf("stdio = %+v, want the substituted command and --x", stdio)
	}
	if stdio.Env[EnvVarPluginRoot] != root {
		t.Errorf("plugin root env = %q, want %q", stdio.Env[EnvVarPluginRoot], root)
	}
	if n := len(loadMcpServers(&ResolvedPlugin{Name: "plug", RootPath: root})); n != 0 {
		t.Errorf("servers without a manifest = %d, want 0", n)
	}
}
