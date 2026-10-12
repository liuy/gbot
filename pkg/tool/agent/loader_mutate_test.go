package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/markdown"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// applyOptionalFields: empty or invalid values must leave the field untouched
// ---------------------------------------------------------------------------

func TestApplyOptionalFields_EmptyAndInvalidStayZero(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		key   string
		value any
		check func(def *types.AgentDefinition) (got any, want any)
	}{
		{"color empty", "color", "", func(d *types.AgentDefinition) (any, any) { return d.Color, "" }},
		{"color non-string", "color", 9, func(d *types.AgentDefinition) (any, any) { return d.Color, "" }},
		{"permissionMode empty", "permissionMode", "", func(d *types.AgentDefinition) (any, any) { return d.PermissionModeField, types.PermissionMode("") }},
		{"permissionMode invalid", "permissionMode", "yolo", func(d *types.AgentDefinition) (any, any) { return d.PermissionModeField, types.PermissionMode("") }},
		{"memory empty", "memory", "", func(d *types.AgentDefinition) (any, any) { return d.Memory, "" }},
		{"memory invalid", "memory", "global", func(d *types.AgentDefinition) (any, any) { return d.Memory, "" }},
		{"isolation empty", "isolation", "", func(d *types.AgentDefinition) (any, any) { return d.Isolation, "" }},
		{"isolation invalid", "isolation", "remote", func(d *types.AgentDefinition) (any, any) { return d.Isolation, "" }},
		{"reminder empty", "criticalSystemReminder_EXPERIMENTAL", "", func(d *types.AgentDefinition) (any, any) { return d.CriticalSystemReminder, "" }},
		{"reminder non-string", "criticalSystemReminder_EXPERIMENTAL", 5, func(d *types.AgentDefinition) (any, any) { return d.CriticalSystemReminder, "" }},
		{"mcp servers empty array", "requiredMcpServers", []any{}, func(d *types.AgentDefinition) (any, any) { return d.RequiredMcpServers, []string(nil) }},
		{"mcp servers non-strings", "requiredMcpServers", []any{1, true}, func(d *types.AgentDefinition) (any, any) { return d.RequiredMcpServers, []string(nil) }},
		{"background false", "background", false, func(d *types.AgentDefinition) (any, any) { return d.Background, false }},
		{"background string false", "background", "false", func(d *types.AgentDefinition) (any, any) { return d.Background, false }},
		{"maxTurns zero", "maxTurns", "0", func(d *types.AgentDefinition) (any, any) { return d.MaxTurns, 0 }},
		{"maxTurns negative", "maxTurns", -4, func(d *types.AgentDefinition) (any, any) { return d.MaxTurns, 0 }},
		{"effort zero", "effort", "0", func(d *types.AgentDefinition) (any, any) { return d.Effort, "" }},
		{"initialPrompt blank", "initialPrompt", "   ", func(d *types.AgentDefinition) (any, any) { return d.InitialPrompt, "" }},
		{"model blank", "model", "  ", func(d *types.AgentDefinition) (any, any) { return d.Model, "" }},
	} {
		def := &types.AgentDefinition{}
		applyOptionalFields(def, map[string]any{tc.key: tc.value}, "worker.md")
		got, want := tc.check(def)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: field = %#v, want %#v", tc.name, got, want)
		}
	}
}

func TestApplyOptionalFields_ValidValues(t *testing.T) {
	t.Parallel()
	def := &types.AgentDefinition{}
	applyOptionalFields(def, map[string]any{
		"color":                               "red",
		"permissionMode":                      "plan",
		"memory":                              "project",
		"isolation":                           "worktree",
		"criticalSystemReminder_EXPERIMENTAL": "stay focused",
		"requiredMcpServers":                  []any{"github", 7, "postgres"},
		"background":                          "true",
		"maxTurns":                            "12",
		"effort":                              "MAX",
		"initialPrompt":                       "  kick off  ",
		"model":                               " Inherit ",
	}, "worker.md")

	if def.Color != "red" {
		t.Errorf("Color = %q, want %q", def.Color, "red")
	}
	if def.PermissionModeField != types.PermissionModePlan {
		t.Errorf("PermissionModeField = %q, want %q", def.PermissionModeField, types.PermissionModePlan)
	}
	if def.Memory != "project" {
		t.Errorf("Memory = %q, want %q", def.Memory, "project")
	}
	if def.Isolation != "worktree" {
		t.Errorf("Isolation = %q, want %q", def.Isolation, "worktree")
	}
	if def.CriticalSystemReminder != "stay focused" {
		t.Errorf("CriticalSystemReminder = %q", def.CriticalSystemReminder)
	}
	if len(def.RequiredMcpServers) != 2 || def.RequiredMcpServers[0] != "github" || def.RequiredMcpServers[1] != "postgres" {
		t.Errorf("RequiredMcpServers = %v, want [github postgres]", def.RequiredMcpServers)
	}
	if !def.Background {
		t.Error("Background should be true for the string \"true\"")
	}
	if def.MaxTurns != 12 {
		t.Errorf("MaxTurns = %d, want 12", def.MaxTurns)
	}
	if def.Effort != "max" {
		t.Errorf("Effort = %q, want %q", def.Effort, "max")
	}
	if def.InitialPrompt != "  kick off  " {
		t.Errorf("InitialPrompt = %q, want the raw (untrimmed) value", def.InitialPrompt)
	}
	if def.Model != "inherit" {
		t.Errorf("Model = %q, want %q (case-insensitive inherit, trimmed)", def.Model, "inherit")
	}
}

// ---------------------------------------------------------------------------
// Tool-list parsing — nil (all tools) vs empty (no tools) must stay distinct
// ---------------------------------------------------------------------------

func TestParseAgentToolsFromFrontmatter_NilVersusEmpty(t *testing.T) {
	t.Parallel()
	if got := parseAgentToolsFromFrontmatter(nil); got != nil {
		t.Errorf("nil input → %v, want nil (all tools)", got)
	}
	for _, empty := range []any{"", []any{}, []any{7}, 42} {
		got := parseAgentToolsFromFrontmatter(empty)
		if got == nil {
			t.Errorf("%#v → nil, want an empty non-nil slice (no tools)", empty)
			continue
		}
		if len(got) != 0 {
			t.Errorf("%#v → %v, want an empty slice", empty, got)
		}
	}
	if got := parseSlashCommandToolsFromFrontmatter(nil); got == nil || len(got) != 0 {
		t.Errorf("parseSlashCommandToolsFromFrontmatter(nil) = %v, want an empty non-nil slice", got)
	}
	if got := parseToolListString(nil); got != nil {
		t.Errorf("parseToolListString(nil) = %v, want nil", got)
	}
	if got := parseToolListString(""); got == nil || len(got) != 0 {
		t.Errorf("parseToolListString(\"\") = %v, want an empty non-nil slice", got)
	}
	if got := parseToolListString("Read, *, Write"); len(got) != 1 || got[0] != "*" {
		t.Errorf("parseToolListString(\"Read, *, Write\") = %v, want [\"*\"]", got)
	}
	if got := parseToolListString([]any{"Read", "*"}); len(got) != 1 || got[0] != "*" {
		t.Errorf("parseToolListString([Read *]) = %v, want [\"*\"]", got)
	}
}

func TestParseToolListFromCLI_Separators(t *testing.T) {
	t.Parallel()
	got := parseToolListFromCLI([]string{"A,,B", "", " C "})
	want := []string{"A", "B", "C"}
	if len(got) != len(want) {
		t.Fatalf("parseToolListFromCLI = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := parseToolListFromCLI([]string{"Bash(git commit, git push)"}); len(got) != 1 || got[0] != "Bash(git commit, git push)" {
		t.Errorf("parenthesised spec = %v, want one untouched token", got)
	}
}

// ---------------------------------------------------------------------------
// loadMarkdownFiles — directory, suffix and size guards
// ---------------------------------------------------------------------------

func TestLoadMarkdownFiles_SkipsDirsAndNonMarkdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := []byte("---\nname: Keep\ndescription: kept\n---\nbody\n")
	for _, name := range []string{"keep.md", "skip.txt", "UPPER.MD", "also.md.bak"} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested.md"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := loadMarkdownFiles(dir, types.AgentSourceProjectSettings)
	if len(got) != 1 {
		t.Fatalf("loaded %d entries (%v), want exactly 1", len(got), entryNames(got))
	}
	if got[0].filePath != filepath.Join(dir, "keep.md") {
		t.Errorf("filePath = %q, want %q", got[0].filePath, filepath.Join(dir, "keep.md"))
	}
	if got[0].baseDir != dir {
		t.Errorf("baseDir = %q, want %q", got[0].baseDir, dir)
	}
	if got[0].source != types.AgentSourceProjectSettings {
		t.Errorf("source = %q, want %q", got[0].source, types.AgentSourceProjectSettings)
	}
	if got[0].frontmatter["name"] != "Keep" {
		t.Errorf("frontmatter = %v, want name=Keep", got[0].frontmatter)
	}
}

func TestLoadMarkdownFiles_SizeBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	header := "---\nname: Exact\ndescription: at the limit\n---\n"
	exact := header + strings.Repeat("y", markdown.MaxFrontmatterFileSize-len(header))
	if len(exact) != markdown.MaxFrontmatterFileSize {
		t.Fatalf("fixture is %d bytes, want exactly %d", len(exact), markdown.MaxFrontmatterFileSize)
	}
	if err := os.WriteFile(filepath.Join(dir, "exact.md"), []byte(exact), 0o600); err != nil {
		t.Fatalf("write exact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "over.md"), append([]byte(exact), 'z'), 0o600); err != nil {
		t.Fatalf("write over: %v", err)
	}

	got := loadMarkdownFiles(dir, types.AgentSourceProjectSettings)
	if len(got) != 1 {
		t.Fatalf("loaded %d entries (%v), want 1 — only the over-limit file may be skipped", len(got), entryNames(got))
	}
	if got[0].filePath != filepath.Join(dir, "exact.md") {
		t.Errorf("kept %q, want exact.md", got[0].filePath)
	}
}

func entryNames(entries []markdownFileEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, filepath.Base(e.filePath))
	}
	return names
}

// ---------------------------------------------------------------------------
// Loader — ordering, reload, failed files, copies
// ---------------------------------------------------------------------------

// isolatedLoader points HOME and the project root at temp dirs so only the
// built-in, bundled and test-authored agents are visible.
func isolatedLoader(t *testing.T, projectAgents map[string]string) *Loader {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	agentsDir := filepath.Join(cwd, ".gbot", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	for name, body := range projectAgents {
		if err := os.WriteFile(filepath.Join(agentsDir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	l := NewLoader(cwd)
	l.Load()
	return l
}

func agentTypes(defs []*types.AgentDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.AgentType)
	}
	return out
}

func TestLoader_ListAll_SortedByAgentType(t *testing.T) {
	l := isolatedLoader(t, map[string]string{
		"zeta.md":  "---\nname: Zeta\ndescription: last alphabetically\n---\n",
		"alpha.md": "---\nname: Alpha\ndescription: first alphabetically\n---\n",
	})
	want := []string{"Alpha", "Critic", "Executor", "Explore", "General", "Planner", "Reviewer", "Zeta"}
	got := agentTypes(l.ListAll())
	if len(got) != len(want) {
		t.Fatalf("agents = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agents = %v, want %v (sorted by AgentType)", got, want)
		}
	}
}

func TestLoader_ReloadReReadsDiskAndClearsFailures(t *testing.T) {
	l := isolatedLoader(t, map[string]string{
		"bad.md": "---\nname: Bad\n---\nno description\n",
	})
	if l.Get("Fresh") != nil {
		t.Fatal("Fresh must be absent before it is written")
	}
	bad := l.FailedFiles()
	if len(bad) != 1 {
		t.Fatalf("failed files = %v, want exactly the one bad agent", bad)
	}

	dir := filepath.Join(l.cwd, ".gbot", "agents")
	if err := os.Remove(filepath.Join(dir, "bad.md")); err != nil {
		t.Fatalf("remove bad.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fresh.md"), []byte("---\nname: Fresh\ndescription: new agent\n---\nbody"), 0o600); err != nil {
		t.Fatalf("write fresh.md: %v", err)
	}

	l.Reload()
	if got := l.Get("Fresh"); got == nil {
		t.Fatal("Reload did not pick up fresh.md")
	} else if got.WhenToUse != "new agent" {
		t.Errorf("Fresh.WhenToUse = %q, want %q", got.WhenToUse, "new agent")
	}
	if failed := l.FailedFiles(); len(failed) != 0 {
		t.Errorf("failed files after reload = %v, want none", failed)
	}
}

func TestLoader_FailedFilesExactEntry(t *testing.T) {
	l := isolatedLoader(t, map[string]string{
		"bad-agent.md": "---\nname: bad-agent\n---\nBody.",
	})
	failed := l.FailedFiles()
	if len(failed) != 1 {
		t.Fatalf("failed files = %v, want exactly 1", failed)
	}
	wantPath := filepath.Join(l.cwd, ".gbot", "agents", "bad-agent.md")
	if failed[0].Path != wantPath {
		t.Errorf("Path = %q, want %q", failed[0].Path, wantPath)
	}
	wantErr := `Missing required "description" field in frontmatter for agent "bad-agent"`
	if failed[0].Error != wantErr {
		t.Errorf("Error = %q, want %q", failed[0].Error, wantErr)
	}
}

func TestLoader_ListAllReturnsCopy(t *testing.T) {
	// Isolated HOME + empty project agents dir leaves exactly the built-in and
	// bundled agent types, so the expected set is deterministic.
	l := isolatedLoader(t, nil)
	want := []string{"Critic", "Executor", "Explore", "General", "Planner", "Reviewer"}
	first := l.ListAll()
	if got := agentTypes(first); !slices.Equal(got, want) {
		t.Fatalf("ListAll = %v, want %v", got, want)
	}
	first[0] = nil
	second := l.ListAll()
	if second[0] == nil {
		t.Error("ListAll shares its backing slice with callers")
	}
}

func TestRegisterPluginAgentsOnFreshLoaderReadsDiskFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	agentsDir := filepath.Join(cwd, ".gbot", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "disk.md"), []byte("---\nname: DiskAgent\ndescription: from disk\n---\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	l := NewLoader(cwd)
	l.RegisterPluginAgents([]types.AgentDefinition{{AgentType: "PluginAgent", Source: types.AgentSourcePlugin}})

	got := agentTypes(l.ListAll())
	want := []string{"Critic", "DiskAgent", "Executor", "Explore", "General", "Planner", "PluginAgent", "Reviewer"}
	if !slices.Equal(got, want) {
		t.Errorf("agents = %v, want %v (the on-disk agent survives plugin registration, sorted by AgentType)", got, want)
	}
}
