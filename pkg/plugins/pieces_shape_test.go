package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/mcp"
)

// Every PluginPieces field is consumed by the reload report; a zeroed field
// silently misreports the plugin.
func TestLoadPluginPieces_PieceShape(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "plug")
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json": `{"name":"plug","version":"1.2.3","mcpServers":{"srv":{"command":"echo"}}}`,
	})
	pieces, err := LoadPluginPieces(dir, &config.Config{})
	if err != nil {
		t.Fatalf("LoadPluginPieces: %v", err)
	}
	if len(pieces) != 1 {
		t.Fatalf("pieces = %d, want 1", len(pieces))
	}
	p := pieces[0]
	if p.Name != "plug" {
		t.Errorf("Name = %q, want plug", p.Name)
	}
	if p.RootPath != root {
		t.Errorf("RootPath = %q, want %q", p.RootPath, root)
	}
	if p.Manifest == nil || p.Manifest.Version != "1.2.3" {
		t.Errorf("Manifest = %+v, want version 1.2.3", p.Manifest)
	}
	wantEnv := []string{
		EnvVarPluginRoot + "=" + root,
		EnvVarPluginData + "=" + PluginDataDir("plug"),
		EnvVarCacheDir + "=" + CacheDir(),
	}
	if len(p.EnvVars) != len(wantEnv) {
		t.Fatalf("EnvVars = %v, want %v", p.EnvVars, wantEnv)
	}
	for i := range wantEnv {
		if p.EnvVars[i] != wantEnv[i] {
			t.Errorf("EnvVars[%d] = %q, want %q", i, p.EnvVars[i], wantEnv[i])
		}
	}
	scoped, ok := p.McpServers["plugin:plug:srv"]
	if !ok {
		t.Fatalf("McpServers = %v, want the plugin:plug:srv key", p.McpServers)
	}
	if scoped.Scope != mcp.ScopeDynamic {
		t.Errorf("Scope = %q, want %q", scoped.Scope, mcp.ScopeDynamic)
	}
	if scoped.PluginSource != "plugin:plug:srv" {
		t.Errorf("PluginSource = %q, want plugin:plug:srv", scoped.PluginSource)
	}
}

// cfg == nil means "no gating": every discovered plugin loads.
func TestLoadPluginPieces_NilConfigLoadsAll(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one", "two"} {
		writePluginFixture(t, dir, name, map[string]string{
			"plugin.json":       `{"name":"` + name + `","version":"1.0.0","skills":"./skills/"}`,
			"skills/s/SKILL.md": "---\nname: s\ndescription: d\n---\nbody\n",
		})
	}
	pieces, err := LoadPluginPieces(dir, nil)
	if err != nil {
		t.Fatalf("LoadPluginPieces(nil cfg): %v", err)
	}
	if len(pieces) != 2 {
		t.Errorf("pieces = %d (%v), want both plugins", len(pieces), pieceNames(pieces))
	}
}

// A disabled plugin is skipped, not a stop signal: the scan continues to the
// next plugin (sorted order puts the disabled one first).
func TestLoadPluginPieces_DisabledDoesNotStopScan(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a_off", "b_on"} {
		writePluginFixture(t, dir, name, map[string]string{
			"plugin.json":       `{"name":"` + name + `","version":"1.0.0","skills":"./skills/"}`,
			"skills/s/SKILL.md": "---\nname: s\ndescription: d\n---\nbody\n",
		})
	}
	cfg := &config.Config{Plugins: map[string]bool{"a_off": false}}
	pieces, err := LoadPluginPieces(dir, cfg)
	if err != nil {
		t.Fatalf("LoadPluginPieces: %v", err)
	}
	if len(pieces) != 1 || pieces[0].Name != "b_on" {
		t.Errorf("pieces = %v, want exactly [b_on]", pieceNames(pieces))
	}
}

func pieceNames(pieces []*PluginPieces) []string {
	var names []string
	for _, p := range pieces {
		names = append(names, p.Name)
	}
	return names
}

// The startup loader's discovery error must stay wrapped.
func TestLoadAndInitialize_DiscoverErrorWrapped(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", blocker, err)
	}
	pluginsDirOverride = blocker
	defer func() { pluginsDirOverride = "" }()

	_, err := LoadAndInitialize(context.Background(), t.TempDir(), &config.Config{})
	if err == nil {
		t.Fatal("LoadAndInitialize must report a discovery failure")
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("error %v must keep the OS error reachable via errors.Is", err)
	}
}

// PluginDataDir is the per-plugin writable path.
func TestPluginDataDirPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	want := filepath.Join(home, ".gbot", "plugins", "data", "myplug")
	if got := PluginDataDir("myplug"); got != want {
		t.Errorf("PluginDataDir = %q, want %q", got, want)
	}
}
