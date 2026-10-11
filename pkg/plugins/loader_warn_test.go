package plugins

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureSlog swaps the default logger for a buffer. Not safe with
// t.Parallel: slog.Default is process-global.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// The tolerant startup paths warn-and-skip; the warning is the only visible
// half of that contract, so each one is asserted.
func TestTolerantPathsWarn(t *testing.T) {
	root := t.TempDir()

	plugin := func(name string, files map[string]string, m *PluginManifest) *ResolvedPlugin {
		t.Helper()
		writePluginFixture(t, root, name, files)
		return &ResolvedPlugin{Name: name, RootPath: filepath.Join(root, name), Manifest: m}
	}

	mcpMissing := plugin("mcpmissing", nil, &PluginManifest{Name: "mcpmissing", McpServers: "./nope.json"})
	mcpBad := plugin("mcpbad", map[string]string{"mcp.json": `{"mcpServers":`},
		&PluginManifest{Name: "mcpbad", McpServers: "./mcp.json"})
	mcpSrv := plugin("mcpsrv", map[string]string{"mcp.json": `{"mcpServers":{"bad":"notanobject"}}`},
		&PluginManifest{Name: "mcpsrv", McpServers: "./mcp.json"})

	hooksDirP := plugin("hooksdir", nil, &PluginManifest{Name: "hooksdir"})
	if err := os.MkdirAll(filepath.Join(hooksDirP.RootPath, "hooks", "hooks.json"), 0o755); err != nil {
		t.Fatalf("mkdir hooks.json dir: %v", err)
	}
	hooksBad := plugin("hooksbad", map[string]string{"hooks/hooks.json": `{"hooks": {`},
		&PluginManifest{Name: "hooksbad"})

	dirsBad := plugin("dirsbad", map[string]string{"skills": "not a dir", "repls": "not a dir"},
		&PluginManifest{Name: "dirsbad", Skills: "./skills", Repls: "./repls"})

	replBad := plugin("replbad", map[string]string{"repl/good.js": "globalThis.g = 1"},
		&PluginManifest{Name: "replbad", Repls: "./repl/"})
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(replBad.RootPath, "repl", "a.js")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	agentsBad := plugin("agentsbad", map[string]string{"agents": "not a dir"},
		&PluginManifest{Name: "agentsbad"})

	buf := captureSlog(t)

	if got := loadMcpServers(mcpMissing); len(got) != 0 {
		t.Errorf("mcpmissing servers = %d, want 0", len(got))
	}
	if got := loadMcpServers(mcpBad); len(got) != 0 {
		t.Errorf("mcpbad servers = %d, want 0", len(got))
	}
	if got := loadMcpServers(mcpSrv); len(got) != 0 {
		t.Errorf("mcpsrv servers = %d, want 0 (invalid server skipped)", len(got))
	}
	if got := loadHooks(hooksDirP); len(got) != 0 {
		t.Errorf("hooksdir hooks = %d, want 0", len(got))
	}
	if got := loadHooks(hooksBad); len(got) != 0 {
		t.Errorf("hooksbad hooks = %d, want 0", len(got))
	}
	if got := loadSkills(dirsBad); len(got) != 0 {
		t.Errorf("dirsbad skills = %d, want 0", len(got))
	}
	if got := loadReplScripts(dirsBad); len(got) != 0 {
		t.Errorf("dirsbad repls = %d, want 0", len(got))
	}
	if got := loadReplScripts(replBad); len(got) != 1 {
		t.Errorf("replbad scripts = %d, want 1 (unreadable script skipped)", len(got))
	}
	if got := loadAgents(agentsBad); len(got) != 0 {
		t.Errorf("agentsbad agents = %d, want 0", len(got))
	}

	logged := buf.String()
	for _, want := range []string{
		`msg="plugins: skip MCP config"`,
		`msg="plugins: invalid .mcp.json"`,
		`msg="plugins: invalid server config"`,
		`msg="plugins: read hooks"`,
		`msg="plugins: invalid hooks.json"`,
		`msg="plugins: read skills dir"`,
		`msg="plugins: read repl dir"`,
		`msg="plugins: read repl script"`,
		`msg="plugins: read agents dir"`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log missing %q; got:\n%s", want, logged)
		}
	}
}
