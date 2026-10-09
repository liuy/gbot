package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/mcp"
)

// writePluginFixture lays out a plugin under dir/<name> with the pieces
// requested by files (map relPath → content).
func writePluginFixture(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	root := filepath.Join(dir, name)
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

const manifestAll = `{"name":"plug","version":"1.0.0","mcpServers":"./mcp.json","skills":"./skills/","repls":"./repl/"}`

func TestLoadPluginPieces_StrictMcpJSON(t *testing.T) {
	dir := t.TempDir()

	// Invalid mcp.json must be an error naming the plugin and the file path.
	writePluginFixture(t, dir, "broken", map[string]string{
		"plugin.json": `{"name":"broken","version":"1.0.0","mcpServers":"./mcp.json"}`,
		"mcp.json":    `{"mcpServers":{"s":{"command":}}}`,
	})
	_, err := LoadPluginPieces(dir, &config.Config{})
	if err == nil {
		t.Fatal("LoadPluginPieces must fail on an invalid plugin mcp.json")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error must name the plugin, got: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join("broken", "mcp.json")) {
		t.Errorf("error must name the offending file path, got: %v", err)
	}

	// Valid parse: scoped key present. (Separate dir: the strict loader scans
	// the whole plugins dir, so the broken fixture must not be revisited.)
	validDir := t.TempDir()
	writePluginFixture(t, validDir, "plug", map[string]string{
		"plugin.json": manifestAll,
		"mcp.json":    `{"mcpServers":{"srv":{"command":"echo","args":["${GBOT_PLUGIN_ROOT}/run.sh"]}}}`,
	})
	pieces, err := LoadPluginPieces(validDir, &config.Config{})
	if err != nil {
		t.Fatalf("LoadPluginPieces(valid): %v", err)
	}
	var plug *PluginPieces
	for _, p := range pieces {
		if p.Name == "plug" {
			plug = p
		}
	}
	if plug == nil {
		t.Fatalf("plugin 'plug' missing from pieces: %+v", pieces)
	}
	if _, ok := plug.McpServers["plugin:plug:srv"]; !ok {
		t.Fatalf("McpServers missing scoped key plugin:plug:srv, got %d servers", len(plug.McpServers))
	}
}

// stdioConfigType aliases the concrete server config type for assertions.
type stdioConfigType = *mcp.StdioConfig

// TestLoadPluginPieces_StrictMcpJSON_EnvInjection continues the valid-parse
// assertions that need the concrete StdioConfig type (kept separate so the
// import list of the first test stays minimal).
func TestLoadPluginPieces_StrictMcpJSON_EnvInjection(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json": manifestAll,
		"mcp.json":    `{"mcpServers":{"srv":{"command":"echo","args":["${GBOT_PLUGIN_ROOT}/run.sh"]}}}`,
	})
	pieces, err := LoadPluginPieces(dir, &config.Config{})
	if err != nil {
		t.Fatalf("LoadPluginPieces(valid): %v", err)
	}
	var plug *PluginPieces
	for _, p := range pieces {
		if p.Name == "plug" {
			plug = p
		}
	}
	if plug == nil {
		t.Fatal("plugin 'plug' missing from pieces")
	}
	cfg, ok := plug.McpServers["plugin:plug:srv"]
	if !ok {
		t.Fatalf("McpServers missing scoped key plugin:plug:srv (got %d servers)", len(plug.McpServers))
	}
	stdio, ok := cfg.Config.(stdioConfigType)
	if !ok {
		t.Fatalf("server config is %T, want StdioConfig", cfg.Config)
	}
	if stdio.Env["GBOT_PLUGIN_ROOT"] != filepath.Join(dir, "plug") {
		t.Errorf("GBOT_PLUGIN_ROOT = %q, want %q", stdio.Env["GBOT_PLUGIN_ROOT"], filepath.Join(dir, "plug"))
	}
	if strings.Join(stdio.Args, " ") != filepath.Join(dir, "plug")+"/run.sh" {
		t.Errorf("${GBOT_PLUGIN_ROOT} not substituted in args, got %v", stdio.Args)
	}
}

func TestLoadPluginPieces_StrictHooksJSON(t *testing.T) {
	dir := t.TempDir()

	writePluginFixture(t, dir, "badhooks", map[string]string{
		"plugin.json":      `{"name":"badhooks","version":"1.0.0"}`,
		"hooks/hooks.json": `{"hooks": {`,
	})
	_, err := LoadPluginPieces(dir, &config.Config{})
	if err == nil {
		t.Fatal("LoadPluginPieces must fail on an unparseable hooks.json")
	}
	if !strings.Contains(err.Error(), filepath.Join("badhooks", "hooks", "hooks.json")) {
		t.Errorf("error must name the hooks file path, got: %v", err)
	}

	// Wrapper format + bare format in a clean dir (the broken fixture must
	// not fail this pass).
	okDir := t.TempDir()
	writePluginFixture(t, okDir, "wrap", map[string]string{
		"plugin.json":      `{"name":"wrap","version":"1.0.0"}`,
		"hooks/hooks.json": `{"description":"d","hooks":{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"echo wrapped"}]}]}}`,
	})
	// Bare format.
	writePluginFixture(t, okDir, "bare", map[string]string{
		"plugin.json":      `{"name":"bare","version":"1.0.0"}`,
		"hooks/hooks.json": `{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"echo bare"}]}]}`,
	})
	pieces, err := LoadPluginPieces(okDir, &config.Config{})
	if err != nil {
		t.Fatalf("LoadPluginPieces(wrapper+bare): %v", err)
	}
	byName := map[string]*PluginPieces{}
	for _, p := range pieces {
		byName[p.Name] = p
	}
	if w := byName["wrap"]; w == nil || len(w.Hooks["SessionStart"]) != 1 {
		t.Errorf("wrapper-format hooks not parsed: %+v", byName["wrap"])
	}
	if b := byName["bare"]; b == nil || len(b.Hooks["SessionStart"]) != 1 {
		t.Errorf("bare-format hooks not parsed: %+v", byName["bare"])
	}
}

func TestLoadPluginPieces_TolerantSkillsAgents(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json": `{"name":"plug","version":"1.0.0","skills":"./skills/"}`,
		// A skill dir without SKILL.md → skipped per-item, no error.
		// (ParseSkill itself never rejects frontmatter-less content, so the
		// per-item skip surface is the missing file.)
		"skills/noskill/keep.txt": "not a skill",
		"skills/good/SKILL.md":    "---\nname: skill-a\ndescription: does things\n---\nbody\n",
		// Agent without a name → skipped, no error.
		"agents/nameless.md": "---\ndescription: no name\n---\nbody\n",
		"agents/planner.md":  "---\nname: planner\ndescription: plans\n---\nbody\n",
	})
	pieces, err := LoadPluginPieces(dir, &config.Config{})
	if err != nil {
		t.Fatalf("LoadPluginPieces: %v", err)
	}
	if len(pieces) != 1 {
		t.Fatalf("got %d pieces, want 1", len(pieces))
	}
	p := pieces[0]
	if len(p.Skills) != 1 {
		t.Errorf("Skills = %d, want 1 (dir without SKILL.md skipped)", len(p.Skills))
	}
	if p.Skills[0].Name != "plug:good" {
		t.Errorf("skill name = %q, want plug:good", p.Skills[0].Name)
	}
	foundPlanner := false
	for _, a := range p.Agents {
		if strings.HasSuffix(a.AgentType, "planner") {
			foundPlanner = true
		}
	}
	if !foundPlanner {
		t.Errorf("agents = %+v, want planner present", p.Agents)
	}
	if len(p.Agents) != 1 {
		t.Errorf("Agents = %d, want 1 (nameless agent skipped)", len(p.Agents))
	}
}

func TestLoadPluginPieces_DisabledPluginSkipped(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "off", map[string]string{
		"plugin.json":       `{"name":"off","version":"1.0.0","skills":"./skills/"}`,
		"skills/s/SKILL.md": "---\nname: s\ndescription: d\n---\nbody\n",
	})
	cfg := &config.Config{Plugins: map[string]bool{"off": false}}
	pieces, err := LoadPluginPieces(dir, cfg)
	if err != nil {
		t.Fatalf("LoadPluginPieces: %v", err)
	}
	if len(pieces) != 0 {
		t.Fatalf("got %d pieces for a disabled plugin, want 0", len(pieces))
	}
}

func TestLoadPluginPieces_ExplicitDir(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	writePluginFixture(t, dir, "here", map[string]string{
		"plugin.json":       `{"name":"here","version":"1.0.0","skills":"./skills/"}`,
		"skills/s/SKILL.md": "---\nname: s\ndescription: d\n---\nbody\n",
	})
	writePluginFixture(t, other, "elsewhere", map[string]string{
		"plugin.json":       `{"name":"elsewhere","version":"1.0.0","skills":"./skills/"}`,
		"skills/s/SKILL.md": "---\nname: s\ndescription: d\n---\nbody\n",
	})
	pieces, err := LoadPluginPieces(dir, &config.Config{})
	if err != nil {
		t.Fatalf("LoadPluginPieces: %v", err)
	}
	if len(pieces) != 1 || pieces[0].Name != "here" {
		t.Fatalf("pieces = %+v, want exactly the fixture in the explicit dir", pieces)
	}
}

func TestLoadPluginPieces_ReplScripts(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json":    `{"name":"plug","version":"1.0.0","repls":"./repl/"}`,
		"repl/helper.js": "globalThis.h = 1",
		"repl/b.js":      "globalThis.b = 2",
		"repl/skip.txt":  "not js",
	})
	pieces, err := LoadPluginPieces(dir, &config.Config{})
	if err != nil {
		t.Fatalf("LoadPluginPieces: %v", err)
	}
	if len(pieces) != 1 {
		t.Fatalf("got %d pieces, want 1", len(pieces))
	}
	rs := pieces[0].ReplScripts
	if len(rs) != 2 {
		t.Fatalf("ReplScripts = %d, want 2 (only .js, sorted)", len(rs))
	}
	if rs[0].Name != "b.js" || rs[1].Name != "helper.js" {
		t.Errorf("ReplScripts order = [%s %s], want sorted [b.js helper.js]", rs[0].Name, rs[1].Name)
	}
}
