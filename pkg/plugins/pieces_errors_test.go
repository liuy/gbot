package plugins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/liuy/gbot/pkg/config"
)

// fileErrorOf asserts the strict loader reports a FileError and returns it.
func fileErrorOf(t *testing.T, err error) *FileError {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var fe *FileError
	if !errors.As(err, &fe) {
		t.Fatalf("error %v (%T) must wrap *FileError", err, err)
	}
	return fe
}

// The manifest itself unparseable (mcpServers of the wrong shape) fails at
// discovery; the fail-open scan skips the plugin and the strict pieces path
// sees nothing — pin that the malformed plugin never produces pieces.
func TestLoadPluginPieces_McpJSONUnreadable(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json": `{"name":"plug","version":"1.0.0","mcpServers":"./mcp.json"}`,
	})
	pieces, err := LoadPluginPieces(dir, &config.Config{})
	if err != nil {
		t.Fatalf("string-form mcpServers is a discovery skip, not a pieces error: %v", err)
	}
	for _, p := range pieces {
		if p.Name == "plug" {
			t.Fatal("malformed plugin must be skipped, not loaded as pieces")
		}
	}
}

// A server entry that fails UnmarshalServerConfig must name the server.
func TestLoadPluginPieces_InvalidServerConfig(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json": `{"name":"plug","version":"1.0.0","mcpServers":{"bad":"notanobject"}}`,
	})
	manifestPath := filepath.Join(dir, "plug", "plugin.json")

	err := LoadPluginPiecesOnly(dir)
	fe := fileErrorOf(t, err)
	if fe.Path != manifestPath {
		t.Errorf("FileError.Path = %q, want %q", fe.Path, manifestPath)
	}
	if fe.Err == nil {
		t.Fatal("FileError.Err must carry the parse failure")
	}
	if !strings.HasPrefix(fe.Err.Error(), `invalid server "bad": `) {
		t.Errorf("FileError.Err = %q, want it to start with %q", fe.Err.Error(), `invalid server "bad": `)
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error %v must keep the JSON type error reachable via errors.As", err)
	}
}

// hooks.json present but unreadable is an error; a missing file is not.
func TestLoadPluginPieces_HooksJSONUnreadable(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json": `{"name":"plug","version":"1.0.0"}`,
	})
	hooksPath := filepath.Join(dir, "plug", "hooks", "hooks.json")
	if err := os.MkdirAll(hooksPath, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", hooksPath, err)
	}

	err := LoadPluginPiecesOnly(dir)
	fe := fileErrorOf(t, err)
	if fe.Path != hooksPath {
		t.Errorf("FileError.Path = %q, want %q", fe.Path, hooksPath)
	}
	if fe.Err == nil || !strings.HasPrefix(fe.Err.Error(), "read hooks.json: ") {
		t.Errorf("FileError.Err = %v, want it to start with %q", fe.Err, "read hooks.json: ")
	}
	if !errors.Is(err, syscall.EISDIR) {
		t.Errorf("the OS error must stay reachable through the wrap chain")
	}
}

// Discovery failure must be reported wrapped, not swallowed or unwrapped.
func TestLoadPluginPieces_DiscoverError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", blocker, err)
	}
	pieces, err := LoadPluginPieces(blocker, &config.Config{})
	if err == nil {
		t.Fatalf("LoadPluginPieces(%q) = %v, want a discovery error", blocker, pieces)
	}
	if !strings.HasPrefix(err.Error(), "plugins: discover: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "plugins: discover: ")
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("error %v must keep the OS error reachable via errors.Is", err)
	}
	if pieces != nil {
		t.Errorf("pieces = %+v, want nil on discovery failure", pieces)
	}
}

func LoadPluginPiecesOnly(dir string) error {
	_, err := LoadPluginPieces(dir, &config.Config{})
	return err
}

// A nil manifest must not be dereferenced by the per-surface guards.
func TestPieceGuards_NilManifest(t *testing.T) {
	t.Parallel()
	plugin := &ResolvedPlugin{Name: "nilman", RootPath: t.TempDir(), Manifest: nil}
	if got := loadMcpServers(plugin); len(got) != 0 {
		t.Errorf("loadMcpServers(nil manifest) = %d servers, want 0", len(got))
	}
	if got := loadSkills(plugin); len(got) != 0 {
		t.Errorf("loadSkills(nil manifest) = %d skills, want 0", len(got))
	}
	if got := loadReplScripts(plugin); len(got) != 0 {
		t.Errorf("loadReplScripts(nil manifest) = %d scripts, want 0", len(got))
	}
}

// With the manifest field empty, the surface is off: files that would match
// the default layout must not be picked up from the plugin root.
func TestPieceGuards_EmptyFieldIgnoresRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePluginFixture(t, root, "plug", map[string]string{
		"mcp.json":      `{"mcpServers":{"srv":{"command":"echo"}}}`,
		"good/SKILL.md": "---\nname: s\ndescription: d\n---\nbody\n",
		"preload.js":    "globalThis.x = 1",
	})
	plugin := &ResolvedPlugin{
		Name:     "plug",
		RootPath: filepath.Join(root, "plug"),
		Manifest: &PluginManifest{Name: "plug"},
	}
	if got := loadMcpServers(plugin); len(got) != 0 {
		t.Errorf("loadMcpServers(empty field) = %v, want no servers", got)
	}
	if got := loadSkills(plugin); len(got) != 0 {
		t.Errorf("loadSkills(empty field) = %v, want no skills", got)
	}
	if got := loadReplScripts(plugin); len(got) != 0 {
		t.Errorf("loadReplScripts(empty field) = %v, want no scripts", got)
	}
}
