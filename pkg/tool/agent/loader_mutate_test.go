package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

// ---------------------------------------------------------------------------
// slog diagnostics — an invalid value must be reported, an unset one must not
// ---------------------------------------------------------------------------

type agentLogRec struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// agentLogHandler swaps the process-default logger so the parser's warning
// paths become observable. Not parallel-safe: the default handler is global.
type agentLogHandler struct {
	mu   sync.Mutex
	recs []agentLogRec
}

func (h *agentLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *agentLogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, agentLogRec{level: r.Level, msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *agentLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *agentLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *agentLogHandler) records() []agentLogRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.recs)
}

func captureAgentLogs(t *testing.T) *agentLogHandler {
	t.Helper()
	h := &agentLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func (h *agentLogHandler) lines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.recs))
	for _, r := range h.recs {
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s", r.level, r.msg)
		for _, k := range slices.Sorted(maps.Keys(r.attrs)) {
			fmt.Fprintf(&b, " %s=%v", k, r.attrs[k])
		}
		out = append(out, b.String())
	}
	return out
}

// wantLines compares level+message+attrs as one string per record, so a warning
// that fires with the wrong path or value is as much a failure as a missing one.
func wantLines(t *testing.T, h *agentLogHandler, want []string) {
	t.Helper()
	got := h.lines()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("log records =\n  %v\nwant:\n  %v", got, want)
	}
}

func TestApplyOptionalFields_InvalidValues_LogExactlyOneWarningEach(t *testing.T) {
	logs := captureAgentLogs(t)
	def := &types.AgentDefinition{}
	applyOptionalFields(def, map[string]any{
		"effort":         "superfast",
		"permissionMode": "yolo",
		"maxTurns":       "not-a-number",
		"background":     42,
		"memory":         "global",
		"isolation":      "docker",
	}, "worker.md")

	if def.Effort != "" || def.PermissionModeField != "" || def.MaxTurns != 0 || def.Background || def.Memory != "" || def.Isolation != "" {
		t.Errorf("invalid values leaked into the definition: %+v", def)
	}
	want := []string{
		"WARN agent: invalid effort value path=worker.md value=superfast",
		"WARN agent: invalid permissionMode path=worker.md value=yolo",
		"WARN agent: invalid maxTurns path=worker.md value=not-a-number",
		"WARN agent: invalid background value path=worker.md value=42",
		"WARN agent: invalid memory value path=worker.md value=global",
		"WARN agent: invalid isolation value path=worker.md value=docker",
	}
	wantLines(t, logs, want)
}

// An empty string means "not set", not "invalid": warning about it would bury
// real diagnostics, and the guard's short-circuit is what keeps it quiet.
func TestApplyOptionalFields_EmptyStringsLogNothing(t *testing.T) {
	logs := captureAgentLogs(t)
	def := &types.AgentDefinition{}
	applyOptionalFields(def, map[string]any{
		"permissionMode": "",
		"memory":         "",
		"isolation":      "",
		"color":          "",
	}, "worker.md")

	if def.PermissionModeField != "" || def.Memory != "" || def.Isolation != "" || def.Color != "" {
		t.Errorf("empty strings leaked into the definition: %+v", def)
	}
	wantLines(t, logs, []string{})
}

func TestParseAgentFromMarkdown_MissingDescriptionLogsPath(t *testing.T) {
	logs := captureAgentLogs(t)
	def := parseAgentFromMarkdown("/p/agent.md", "/p", map[string]any{"name": "NoDesc"}, "body", types.AgentSourceProjectSettings)
	if def != nil {
		t.Fatalf("parsed = %+v, want nil without a description", def)
	}
	wantLines(t, logs, []string{"WARN agent: missing required 'description' in frontmatter path=/p/agent.md"})
}

func TestGetActiveAgentsFromList_LogsEveryOverride(t *testing.T) {
	logs := captureAgentLogs(t)
	builtIn := &types.AgentDefinition{AgentType: "Executor", Source: types.AgentSourceBuiltIn}
	user := &types.AgentDefinition{AgentType: "Executor", Source: types.AgentSourceUserSettings}
	got := getActiveAgentsFromList([]*types.AgentDefinition{builtIn, user})
	if len(got) != 1 || got[0] != user {
		t.Fatalf("resolved = %+v, want the user-settings definition", got)
	}
	wantLines(t, logs, []string{"INFO agent: overridden by higher priority source existing=built-in new=userSettings type=Executor"})
}

// ---------------------------------------------------------------------------
// loadMarkdownFiles — a skipped file must not end the scan, and each skip is logged
// ---------------------------------------------------------------------------

func TestLoadMarkdownFiles_SkipsAreLoggedAndScanContinues(t *testing.T) {
	logs := captureAgentLogs(t)
	dir := t.TempDir()
	header := "---\nname: Over\ndescription: over the limit\n---\n"
	oversize := header + strings.Repeat("y", markdown.MaxFrontmatterFileSize-len(header)+1)
	if err := os.WriteFile(filepath.Join(dir, "a-over.md"), []byte(oversize), 0o600); err != nil {
		t.Fatalf("write oversize: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing-target.md"), filepath.Join(dir, "b-dangling.md")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "z-good.md"), []byte("---\nname: GoodAfter\ndescription: still loaded\n---\n"), 0o600); err != nil {
		t.Fatalf("write good: %v", err)
	}

	got := loadMarkdownFiles(dir, types.AgentSourceProjectSettings)
	if len(got) != 1 || got[0].filePath != filepath.Join(dir, "z-good.md") {
		t.Fatalf("loaded %v, want exactly z-good.md — neither skip may end the scan", entryNames(got))
	}

	lines := logs.lines()
	if len(lines) != 2 {
		t.Fatalf("log records = %v, want one too-large and one read-failure record", lines)
	}
	wantTooLarge := fmt.Sprintf("WARN agent: skipping file: too large path=%s size=%d",
		filepath.Join(dir, "a-over.md"), len(oversize))
	if lines[0] != wantTooLarge {
		t.Errorf("too-large record = %q, want %q", lines[0], wantTooLarge)
	}
	const readMsg = "agent: failed to read file"
	if !strings.HasPrefix(lines[1], "WARN "+readMsg) {
		t.Fatalf("read record = %q, want a %q warning", lines[1], readMsg)
	}
	rec := logs.records()[1]
	if rec.attrs["path"] != filepath.Join(dir, "b-dangling.md") {
		t.Errorf("read record path = %v, want the dangling symlink", rec.attrs["path"])
	}
	err, ok := rec.attrs["error"].(error)
	if !ok || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read record error = %v, want it to report fs.ErrNotExist", rec.attrs["error"])
	}
}

// ---------------------------------------------------------------------------
// deduplicateFiles — a duplicate must not end the scan
// ---------------------------------------------------------------------------

func TestDeduplicateFiles_DuplicateDoesNotEndScan(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.md")
	second := filepath.Join(dir, "second.md")
	third := filepath.Join(dir, "third.md")
	for _, p := range []string{first, second, third} {
		if err := os.WriteFile(p, []byte("---\nname: X\n---\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// first and second are the same physical file; third is distinct.
	if err := os.Remove(second); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Link(first, second); err != nil {
		t.Skipf("hard links not supported: %v", err)
	}

	got := deduplicateFiles([]markdownFileEntry{
		{filePath: first, source: types.AgentSourceProjectSettings},
		{filePath: second, source: types.AgentSourceProjectSettings},
		{filePath: third, source: types.AgentSourceProjectSettings},
	})
	if !reflect.DeepEqual(entryNames(got), []string{"first.md", "third.md"}) {
		t.Errorf("dedup result = %v, want [first.md third.md]", entryNames(got))
	}
}

// ---------------------------------------------------------------------------
// Loader.load — dedup runs over the whole file list, a parse failure ends one file only
// ---------------------------------------------------------------------------

func TestLoad_DeduplicatesByInodeBeforeParsing(t *testing.T) {
	l := isolatedLoader(t, nil)
	dir := filepath.Join(l.cwd, ".gbot", "agents")
	bad := "---\nname: BadTwin\n---\nno description\n"
	if err := os.WriteFile(filepath.Join(dir, "a-src.md"), []byte(bad), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Link(filepath.Join(dir, "a-src.md"), filepath.Join(dir, "b-twin.md")); err != nil {
		t.Skipf("hard links not supported: %v", err)
	}

	l.Reload()
	failed := l.FailedFiles()
	if len(failed) != 1 {
		t.Fatalf("failed files = %v, want one entry — the hard-link twin must be deduplicated before parsing", failed)
	}
	if failed[0].Path != filepath.Join(dir, "a-src.md") {
		t.Errorf("failed path = %q, want the first entry in sorted order", failed[0].Path)
	}
}

func TestLoad_ParseFailureDoesNotEndTheFileScan(t *testing.T) {
	l := isolatedLoader(t, map[string]string{
		"a-bad.md":  "---\nname: BadFirst\n---\nno description\n",
		"z-good.md": "---\nname: GoodAfter\ndescription: loaded despite the bad file\n---\n",
	})
	got := l.Get("GoodAfter")
	if got == nil {
		t.Fatalf("agents = %v, want GoodAfter despite the earlier unparseable file", agentTypes(l.ListAll()))
	}
	if got.WhenToUse != "loaded despite the bad file" {
		t.Errorf("WhenToUse = %q", got.WhenToUse)
	}
}

// ---------------------------------------------------------------------------
// Reload / lazy-load interaction
// ---------------------------------------------------------------------------

// Reload disarms the lazy guard, so the first access after a Reload re-reads the
// disk. Without the reset, ensureLoaded stays spent and the loader silently
// serves whatever was on disk before the next write.
func TestReload_ReArmsLazyLoading(t *testing.T) {
	l := isolatedLoader(t, nil)
	if l.Get("LateAgent") != nil {
		t.Fatal("LateAgent must be absent before it is written")
	}
	l.Reload()
	dir := filepath.Join(l.cwd, ".gbot", "agents")
	if err := os.WriteFile(filepath.Join(dir, "late.md"), []byte("---\nname: LateAgent\ndescription: written after Reload\n---\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := l.Get("LateAgent")
	if got == nil {
		t.Fatalf("agents = %v, want LateAgent — Reload must re-arm lazy loading", agentTypes(l.ListAll()))
	}
}

// The lazy load is memoised: a plain read never re-reads the disk, so a file
// written after the first access stays invisible until an explicit Reload.
func TestEnsureLoaded_MemoisesTheDiskRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".gbot", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "early.md"), []byte("---\nname: EarlyAgent\ndescription: there from the start\n---\n"), 0o600); err != nil {
		t.Fatalf("write early: %v", err)
	}

	l := NewLoader(cwd)
	if l.Get("EarlyAgent") == nil {
		t.Fatal("EarlyAgent missing — the first read must trigger the lazy load")
	}
	if err := os.WriteFile(filepath.Join(dir, "late.md"), []byte("---\nname: LateAgent\ndescription: written after the first read\n---\n"), 0o600); err != nil {
		t.Fatalf("write late: %v", err)
	}
	if l.Get("LateAgent") != nil {
		t.Error("LateAgent appeared without a Reload — the lazy load must be memoised")
	}
}

// An explicit Load spends the guard the same way: reads after it stay on the cache.
func TestLoad_SpendsTheLazyGuard(t *testing.T) {
	l := isolatedLoader(t, nil)
	dir := filepath.Join(l.cwd, ".gbot", "agents")
	if err := os.WriteFile(filepath.Join(dir, "late.md"), []byte("---\nname: LateAgent\ndescription: written after Load\n---\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if l.Get("LateAgent") != nil {
		t.Error("a read after Load re-read the disk; Load must mark the cache loaded")
	}
}

// Reload must populate the cache eagerly: RegisterPluginAgents appends to
// whatever is cached and never re-reads the disk for a non-nil cache.
func TestReload_CachesDiskStateForPluginRegistration(t *testing.T) {
	l := isolatedLoader(t, nil)
	dir := filepath.Join(l.cwd, ".gbot", "agents")
	if err := os.WriteFile(filepath.Join(dir, "late.md"), []byte("---\nname: LateAgent\ndescription: written after the first Load\n---\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	l.Reload()
	l.RegisterPluginAgents([]types.AgentDefinition{{AgentType: "PlugAgent", Source: types.AgentSourcePlugin}})

	got := agentTypes(l.ListAll())
	want := []string{"Critic", "Executor", "Explore", "General", "LateAgent", "Planner", "PlugAgent", "Reviewer"}
	if !slices.Equal(got, want) {
		t.Errorf("agents = %v, want %v — the post-Reload on-disk agent and the plugin agent both survive", got, want)
	}
}

func TestGet_ExactMatchBeatsCaseInsensitive(t *testing.T) {
	l := isolatedLoader(t, map[string]string{
		"upper.md": "---\nname: Dup\ndescription: exact-case definition\n---\n",
		"lower.md": "---\nname: dup\ndescription: lower-case definition\n---\n",
	})
	got := l.Get("dup")
	if got == nil {
		t.Fatal("Get(dup) = nil, want the lower-case definition")
	}
	if got.WhenToUse != "lower-case definition" {
		t.Errorf("Get(dup) resolved to %q, want the exact-case match ahead of the case-insensitive fallback", got.WhenToUse)
	}
}

func TestFailedFiles_TriggersLazyLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	agentsDir := filepath.Join(cwd, ".gbot", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "bad.md"), []byte("---\nname: LazyBad\n---\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	l := NewLoader(cwd)
	failed := l.FailedFiles()
	if len(failed) != 1 {
		t.Fatalf("failed files = %v, want the one bad file — FailedFiles must trigger the lazy load", failed)
	}
	if !strings.HasSuffix(failed[0].Path, "bad.md") {
		t.Errorf("failed path = %q, want bad.md", failed[0].Path)
	}
}

// ---------------------------------------------------------------------------
// Empty-but-present required fields
// ---------------------------------------------------------------------------

func TestParseAgentFromMarkdown_EmptyNameOrDescriptionIsNotAnAgent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		frontmatter map[string]any
	}{
		{"empty name", map[string]any{"name": "", "description": "has a description"}},
		{"empty description", map[string]any{"name": "HasName", "description": ""}},
	} {
		if got := parseAgentFromMarkdown("a.md", "", tc.frontmatter, "body", types.AgentSourceProjectSettings); got != nil {
			t.Errorf("%s: parsed = %+v, want nil", tc.name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// parseMcpServersRaw — an item that cannot be marshalled is skipped, not fatal
// ---------------------------------------------------------------------------

func TestParseMcpServersRaw_SkipsUnmarshalableItem(t *testing.T) {
	got := parseMcpServersRaw([]any{math.NaN(), "ref"})
	if len(got) != 1 {
		t.Fatalf("result = %q, want exactly one entry — NaN cannot be marshalled and must not end the scan", got)
	}
	if string(got[0]) != `"ref"` {
		t.Errorf("entry = %q, want the string reference verbatim", string(got[0]))
	}
}

// ---------------------------------------------------------------------------
// Blank tool lists mean "no tools" ([]), never "all tools" (nil)
// ---------------------------------------------------------------------------

func TestParseAgentToolsFromFrontmatter_BlankStringIsNoTools(t *testing.T) {
	got := parseAgentToolsFromFrontmatter(" ")
	if got == nil {
		t.Fatal(`parseAgentToolsFromFrontmatter(" ") = nil, want an empty slice (nil means all tools)`)
	}
	if len(got) != 0 {
		t.Errorf(`parseAgentToolsFromFrontmatter(" ") = %v, want an empty slice`, got)
	}
}

func TestPositiveBoundaries_AreOne(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"int one", 1},
		{"string one", "1"},
		{"float one", float64(1)},
	} {
		got := parsePositiveIntFromFrontmatter(tc.value)
		if got == nil || *got != 1 {
			t.Errorf("parsePositiveIntFromFrontmatter(%v) = %v, want 1", tc.value, got)
		}
	}
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"string one", "1", "1"},
		{"int one", 1, "1"},
		{"float one", float64(1), "1"},
	} {
		if got := parseEffortValue(tc.value); got != tc.want {
			t.Errorf("parseEffortValue(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// InitLoader on an existing global loader must re-read the disk
// ---------------------------------------------------------------------------

func TestInitLoader_ReinitialisationReloadsDisk(t *testing.T) {
	prev := globalLoader
	t.Cleanup(func() { globalLoader = prev })
	globalLoader = nil

	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	agentsDir := filepath.Join(cwd, ".gbot", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	InitLoader(cwd)
	if GlobalLoader().Get("LateAgent") != nil {
		t.Fatal("LateAgent must be absent before it is written")
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "late.md"), []byte("---\nname: LateAgent\ndescription: written after the first init\n---\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	InitLoader(cwd)
	if got := GlobalLoader().Get("LateAgent"); got == nil {
		t.Error("the second InitLoader kept the stale cache; it must reload the disk")
	}
}

func TestRegisterPluginAgents_SameTypeOverrides(t *testing.T) {
	l := isolatedLoader(t, nil)
	l.RegisterPluginAgents([]types.AgentDefinition{{
		AgentType: "Executor",
		WhenToUse: "plugin executor",
		Source:    types.AgentSourcePlugin,
	}})

	var matches []*types.AgentDefinition
	for _, def := range l.ListAll() {
		if def.AgentType == "Executor" {
			matches = append(matches, def)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("Executor entries = %d, want 1 — the plugin definition must replace the built-in one", len(matches))
	}
	if matches[0].Source != types.AgentSourcePlugin || matches[0].WhenToUse != "plugin executor" {
		t.Errorf("Executor = %+v, want the plugin definition to win", matches[0])
	}
}

// A failed git run is not a git root, even when git printed a path before
// failing (corrupt repo, dubious ownership): the error must win over stdout.
func TestFindGitRoot_FailedCommandIgnoresStdout(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho /printed/path\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := findGitRoot(t.TempDir()); got != "" {
		t.Errorf("findGitRoot = %q, want the empty string — git exited non-zero", got)
	}
}

// ---------------------------------------------------------------------------
// Lock discipline — the write lock must cover the whole load, not just its start
// ---------------------------------------------------------------------------
// findGitRoot shells out to git, which gives load() a rendezvous point: the fake
// git signals once it is running and parks there until the test writes the
// release file, so the loader is provably mid-load when the lock is probed.
// The park is bounded: a test that aborts before releasing must not leave a
// spinning shell behind.
func fakeGitHoldingLoad(t *testing.T) (signal, release string) {
	t.Helper()
	bin := t.TempDir()
	signal = filepath.Join(bin, "git-started")
	release = filepath.Join(bin, "git-release")
	script := "#!/bin/sh\n: > \"" + signal + "\"\ni=0\nwhile [ ! -f \"" + release + "\" ] && [ $i -lt 500 ]; do sleep 0.01; i=$((i+1)); done\necho /nonexistent\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return signal, release
}

func releaseFakeGit(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("release fake git: %v", err)
	}
}

func TestAllLoadPaths_HoldTheWriteLockForTheWholeLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	signal, release := fakeGitHoldingLoad(t)

	for _, tc := range []struct {
		name string
		run  func(*Loader)
	}{
		{name: "Load", run: func(l *Loader) { l.Load() }},
		{name: "Reload", run: func(l *Loader) { l.Reload() }},
		{name: "Get (lazy)", run: func(l *Loader) { l.Get("Executor") }},
	} {
		for _, f := range []string{signal, release} {
			if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("remove %s: %v", f, err)
			}
		}
		l := NewLoader(cwd)
		done := make(chan struct{})
		go func() {
			tc.run(l)
			close(done)
		}()
		waitForFile(t, signal, 10*time.Second)
		if l.mu.TryLock() {
			l.mu.Unlock()
			t.Errorf("%s released the write lock before load() finished", tc.name)
		}
		releaseFakeGit(t, release)
		<-done
	}
}

// RegisterPluginAgents falls back to load() for a loader that has never read
// the disk; that fallback runs inside the same write lock.
func TestRegisterPluginAgents_HoldTheWriteLockWhileLoading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	signal, release := fakeGitHoldingLoad(t)

	l := NewLoader(cwd)
	done := make(chan struct{})
	go func() {
		l.RegisterPluginAgents([]types.AgentDefinition{{AgentType: "PlugAgent", Source: types.AgentSourcePlugin}})
		close(done)
	}()
	waitForFile(t, signal, 10*time.Second)
	if l.mu.TryLock() {
		l.mu.Unlock()
		t.Error("RegisterPluginAgents released the write lock before its load() finished")
	}
	releaseFakeGit(t, release)
	<-done
}

// The write lock is held for the whole of load(), so these readers queue behind
// a load that is still running instead of sampling a half-populated cache. The
// trailing Reload is the rendezvous: it returns only once the load has finished
// and every reader has handed its read lock back.
func TestLoader_ReadersReleaseTheReadLockWhileLoadRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	signal, release := fakeGitHoldingLoad(t)

	l := NewLoader(cwd)
	done := make(chan struct{})
	go func() {
		l.Load()
		close(done)
	}()
	waitForFile(t, signal, 10*time.Second)

	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for range 50 {
				l.Get("Executor")
				l.ListAll()
				l.FailedFiles()
			}
		})
	}
	releaseFakeGit(t, release)
	<-done
	wg.Wait()

	// Every reader must have handed the lock back: a leaked RLock blocks here
	// forever instead.
	l.Reload()
	got := agentTypes(l.ListAll())
	want := []string{"Critic", "Executor", "Explore", "General", "Planner", "Reviewer"}
	if !slices.Equal(got, want) {
		t.Errorf("agents = %v, want %v after concurrent readers", got, want)
	}
}

// The lazy-load guard is re-armed by Reload under the write lock but sampled by
// every reader, so it has to live behind the same lock. A guard outside the lock
// is a data race between the re-arm and the readers' sample.
func TestLoader_ConcurrentReadersAndReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".gbot", "agents"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	l := NewLoader(cwd)

	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for range 200 {
				l.Get("Executor")
				l.ListAll()
				l.FailedFiles()
			}
		})
	}
	for range 10 {
		l.Reload()
	}
	wg.Wait()

	got := agentTypes(l.ListAll())
	want := []string{"Critic", "Executor", "Explore", "General", "Planner", "Reviewer"}
	if !slices.Equal(got, want) {
		t.Errorf("agents = %v, want %v after concurrent reloads", got, want)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout) // REAL-TIME: bounded poll for the fake git's start-up signal
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) { // REAL-TIME: the deadline turns the poll into a hard failure
			t.Fatalf("fake git never signalled at %s within %v", path, timeout)
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: re-check loop for an external process's signal file
	}
}
