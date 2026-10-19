package plugins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/types"
)

// An inline server entry whose JSON value has the wrong shape: the FileError
// must name the manifest and keep the JSON error in the chain.
func TestLoadPluginPieces_InvalidMcpJSONShape(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json": `{"name":"plug","version":"1.0.0","mcpServers":{"s":"not-a-server-object"}}`,
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
	if !strings.HasPrefix(fe.Err.Error(), `invalid server "s": `) {
		t.Errorf("FileError.Err = %q, want it to start with %q", fe.Err.Error(), `invalid server "s": `)
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error %v must keep the JSON type error reachable via errors.As", err)
	}
}

// Unparseable hooks.json (neither wrapper nor bare form).
func TestLoadPluginPieces_InvalidHooksJSONShape(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "plug", map[string]string{
		"plugin.json":      `{"name":"plug","version":"1.0.0"}`,
		"hooks/hooks.json": `{"hooks": {`,
	})
	hooksPath := filepath.Join(dir, "plug", "hooks", "hooks.json")

	err := LoadPluginPiecesOnly(dir)
	fe := fileErrorOf(t, err)
	if fe.Path != hooksPath {
		t.Errorf("FileError.Path = %q, want %q", fe.Path, hooksPath)
	}
	if fe.Err == nil {
		t.Fatal("FileError.Err must carry the parse failure")
	}
	if !strings.HasPrefix(fe.Err.Error(), "invalid hooks.json: ") {
		t.Errorf("FileError.Err = %q, want it to start with %q", fe.Err.Error(), "invalid hooks.json: ")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error %v must keep the JSON syntax error reachable via errors.As", err)
	}
}

// Manifest read/parse failures must stay attributable through the wrap chain.
func TestLoadManifest_ErrorChain(t *testing.T) {
	t.Parallel()
	missing := t.TempDir()
	_, err := LoadManifest(missing)
	if err == nil {
		t.Fatal("LoadManifest must fail when plugin.json is absent")
	}
	if err.Error() != "read manifest: open "+filepath.Join(missing, "plugin.json")+": no such file or directory" {
		t.Errorf("error = %q, want the read-manifest message", err.Error())
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v must keep os.ErrNotExist reachable via errors.Is", err)
	}

	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "plugin.json"), []byte(`{"name":`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	_, err = LoadManifest(broken)
	if err == nil {
		t.Fatal("LoadManifest must fail on malformed JSON")
	}
	if !strings.HasPrefix(err.Error(), "parse manifest: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "parse manifest: ")
	}
	var syn *json.SyntaxError
	if !errors.As(err, &syn) {
		t.Errorf("error %v must keep the JSON syntax error reachable via errors.As", err)
	}

	// LoadPlugin adds the plugin-name context on top.
	_, err = LoadPlugin(broken)
	if err == nil {
		t.Fatal("LoadPlugin must fail on a malformed manifest")
	}
	if !strings.HasPrefix(err.Error(), "load plugin "+filepath.Base(broken)+": ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "load plugin "+filepath.Base(broken)+": ")
	}
	if !errors.As(err, &syn) {
		t.Errorf("error %v must keep the JSON syntax error reachable via errors.As", err)
	}
}

// Tolerant scans must keep going after a bad entry: an unreadable .md, a
// frontmatter-less .md and a non-markdown file all precede the good agent in
// sorted order, so any early exit from the loop drops it.
func TestLoadAgents_SkipsBadEntriesAndKeepsGoing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePluginFixture(t, root, "plug", map[string]string{
		"agents/b.md":       "plain text, no frontmatter at all",
		"agents/notes.txt":  "---\nname: txtagent\ndescription: from txt\n---\nbody\n",
		"agents/planner.md": "---\nname: planner\ndescription: plans\nmodel: zhipu/glm-5.3-flash\n---\nbody text\n",
	})
	// A dangling .md symlink sorts first and fails ReadFile: the scan must
	// skip it and keep going.
	dangling := filepath.Join(root, "plug", "agents", "a.md")
	if err := os.Symlink(filepath.Join(root, "missing-target"), dangling); err != nil {
		t.Fatalf("symlink %s: %v", dangling, err)
	}

	plugin := &ResolvedPlugin{
		Name:     "plug",
		RootPath: filepath.Join(root, "plug"),
		Manifest: &PluginManifest{Name: "plug"},
	}
	agents := loadAgents(plugin)
	if len(agents) != 1 {
		t.Fatalf("agents = %d (%+v), want exactly 1 after three skippable entries", len(agents), agents)
	}
	a := agents[0]
	if a.AgentType != "plug:planner" || a.Filename != "planner" {
		t.Errorf("agent = (%q, %q), want (plug:planner, planner)", a.AgentType, a.Filename)
	}
	if a.Source != types.AgentSourcePlugin {
		t.Errorf("Source = %q, want %q", a.Source, types.AgentSourcePlugin)
	}
	if a.BaseDir != filepath.Join(root, "plug", "agents") {
		t.Errorf("BaseDir = %q, want %q", a.BaseDir, filepath.Join(root, "plug", "agents"))
	}
	if a.WhenToUse != "plans" {
		t.Errorf("WhenToUse = %q, want plans", a.WhenToUse)
	}
	if a.Model != "zhipu/glm-5.3-flash" {
		t.Errorf("Model = %q, want zhipu/glm-5.3-flash", a.Model)
	}
	if a.SystemPrompt == nil {
		t.Fatal("SystemPrompt must be set")
	}
	if got := a.SystemPrompt(); got != "body text" {
		t.Errorf("SystemPrompt() = %q, want %q", got, "body text")
	}
}

// Skills: a plain file inside the skills dir precedes the skill directory in
// sorted order and must be skipped without ending the scan.
func TestLoadSkills_SkipsFileEntryAndKeepsGoing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePluginFixture(t, root, "plug", map[string]string{
		"skills/adir/README.md": "a skill dir without SKILL.md",
		"skills/afile.txt":      "not a skill dir",
		"skills/good/SKILL.md":  "---\nname: skill-a\ndescription: does things\n---\nbody\n",
	})
	plugin := &ResolvedPlugin{
		Name:     "plug",
		RootPath: filepath.Join(root, "plug"),
		Manifest: &PluginManifest{Name: "plug", Skills: "./skills/"},
	}
	skills := loadSkills(plugin)
	if len(skills) != 1 {
		t.Fatalf("skills = %d (%+v), want exactly 1", len(skills), skills)
	}
	if skills[0].Name != "plug:good" {
		t.Errorf("skill name = %q, want plug:good", skills[0].Name)
	}
	if skills[0].PluginInfo == nil || skills[0].PluginInfo.PluginName != "plug" {
		t.Errorf("PluginInfo = %+v, want plugin plug", skills[0].PluginInfo)
	}
}

// Repls: a non-.js file first in sorted order must be skipped without ending
// the scan, and only .js files are read.
func TestLoadReplScripts_SkipsNonJsAndKeepsGoing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePluginFixture(t, root, "plug", map[string]string{
		"repl/a.txt":     "not js",
		"repl/b.js":      "globalThis.b = 2",
		"repl/helper.js": "globalThis.h = 1",
	})
	// A dangling .js symlink sorts first and fails ReadFile: the scan must
	// skip it and keep going.
	dangling := filepath.Join(root, "plug", "repl", "a.js")
	if err := os.Symlink(filepath.Join(root, "missing-target"), dangling); err != nil {
		t.Fatalf("symlink %s: %v", dangling, err)
	}
	plugin := &ResolvedPlugin{
		Name:     "plug",
		RootPath: filepath.Join(root, "plug"),
		Manifest: &PluginManifest{Name: "plug", Repls: "./repl/"},
	}
	scripts := loadReplScripts(plugin)
	if len(scripts) != 2 {
		t.Fatalf("scripts = %d (%+v), want 2", len(scripts), scripts)
	}
	if scripts[0].Name != "b.js" || scripts[1].Name != "helper.js" {
		t.Errorf("order = [%s %s], want sorted [b.js helper.js]", scripts[0].Name, scripts[1].Name)
	}
	if scripts[0].Plugin != "plug" {
		t.Errorf("Plugin = %q, want plug", scripts[0].Plugin)
	}
	if scripts[0].Source != "globalThis.b = 2" {
		t.Errorf("Source = %q, want the file contents", scripts[0].Source)
	}
}

// An empty pluginsDir means "the default discovery path", not "nothing".
func TestLoadPluginPieces_DefaultDir(t *testing.T) {
	dir := t.TempDir()
	writePluginFixture(t, dir, "homeplug", map[string]string{
		"plugin.json":       `{"name":"homeplug","version":"1.0.0","skills":"./skills/"}`,
		"skills/s/SKILL.md": "---\nname: s\ndescription: d\n---\nbody\n",
	})
	pluginsDirOverride = dir
	defer func() { pluginsDirOverride = "" }()

	pieces, err := LoadPluginPieces("", &config.Config{})
	if err != nil {
		t.Fatalf("LoadPluginPieces(\"\"): %v", err)
	}
	if len(pieces) != 1 {
		t.Fatalf("pieces = %d (%+v), want the one plugin from the default dir", len(pieces), pieces)
	}
	if pieces[0].Name != "homeplug" {
		t.Errorf("Name = %q, want homeplug", pieces[0].Name)
	}
	if len(pieces[0].Skills) != 1 || pieces[0].Skills[0].Name != "homeplug:s" {
		t.Errorf("Skills = %+v, want one homeplug:s skill", pieces[0].Skills)
	}
}

// A disabled plugin is skipped before any piece is read, so a broken strict
// file inside it must not fail the load.
func TestLoadPluginPieces_DisabledPluginWithBrokenMcp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePluginFixture(t, dir, "off", map[string]string{
		"plugin.json": `{"name":"off","version":"1.0.0","mcpServers":{"s":"not-a-server-object"}}`,
	})
	cfg := &config.Config{Plugins: map[string]bool{"off": false}}
	pieces, err := LoadPluginPieces(dir, cfg)
	if err != nil {
		t.Fatalf("disabled plugin must be skipped before parsing, got %v", err)
	}
	if len(pieces) != 0 {
		t.Errorf("pieces = %d, want 0", len(pieces))
	}
}
