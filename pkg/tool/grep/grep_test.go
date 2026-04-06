package grep_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/gbot/pkg/tool/grep"
	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// GrepToolCall with actual file searching (uses rg when available)
// ---------------------------------------------------------------------------

func TestGrepToolCall_ActualFileSearch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "sample.txt")
	content := "line one hello\nline two world\nline three hello\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"hello","path":"` + fp + `"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(*grep.Output)
	if !ok {
		t.Fatalf("Data type = %T, want *grep.Output", result.Data)
	}
	if output.Count != 2 {
		t.Errorf("Count = %d, want 2", output.Count)
	}
	for _, m := range output.Matches {
		if !strings.Contains(m.Content, "hello") {
			t.Errorf("Match content = %q, should contain 'hello'", m.Content)
		}
		if m.File != fp {
			t.Errorf("Match file = %q, want %q", m.File, fp)
		}
		if m.Line == 0 {
			t.Error("Match line = 0, want non-zero")
		}
	}
}

func TestGrepToolCall_IncludeFilter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "code.go"), []byte("// TODO: fix this\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("// TODO: review later\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"TODO","path":"` + dir + `","include":"*.go"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	for _, m := range output.Matches {
		if !strings.HasSuffix(m.File, ".go") {
			t.Errorf("Match file = %q, should be a .go file", m.File)
		}
	}
	// Should only match .go file
	if output.Count > 1 {
		t.Errorf("Count = %d, expected at most 1 match with include filter", output.Count)
	}
}

func TestGrepToolCall_DirectorySearch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("findme in a\nno match\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("findme in b\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"findme","path":"` + dir + `"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.Count != 2 {
		t.Errorf("Count = %d, want 2", output.Count)
	}
}

func TestGrepToolCall_NoMatches_ExitCode1(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(fp, []byte("nothing relevant here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"UNIQUE_PATTERN_NOT_FOUND","path":"` + fp + `"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v, no-match should not be an error", err)
	}

	output := result.Data.(*grep.Output)
	if output.Count != 0 {
		t.Errorf("Count = %d, want 0", output.Count)
	}
	if output.Matches == nil {
		t.Error("Matches should not be nil (should be empty slice)")
	}
}

func TestGrepToolCall_WorkingDirFromContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(fp, []byte("unique_marker_xyz\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tctx := &types.ToolUseContext{WorkingDir: dir}
	input := json.RawMessage(`{"pattern":"unique_marker_xyz"}`)
	result, err := grep.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.Count < 1 {
		t.Errorf("Count = %d, want >= 1 (using WorkingDir from context)", output.Count)
	}
}

func TestGrepToolCall_TypeFilter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc doSearch() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"doSearch","path":"` + dir + `","type":"go"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.Count < 1 {
		t.Errorf("Count = %d, want >= 1 with type filter", output.Count)
	}
}

func TestGrepToolCall_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := grep.Execute(context.Background(), json.RawMessage(`{not json}`), nil)
	if err == nil {
		t.Fatal("Execute() should return error for invalid JSON")
	}
}

func TestGrepToolCall_EmptyPattern(t *testing.T) {
	t.Parallel()

	_, err := grep.Execute(context.Background(), json.RawMessage(`{"pattern":""}`), nil)
	if err == nil {
		t.Fatal("Execute() should return error for empty pattern")
	}
}

func TestGrepToolCall_NonexistentPath(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"pattern":"test","path":"/nonexistent/path/xyz123"}`)
	_, err := grep.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should return error for nonexistent path")
	}
}

// ---------------------------------------------------------------------------
// New() — covers tool construction (was 0% coverage)
// ---------------------------------------------------------------------------

func TestNew_ReturnsValidTool(t *testing.T) {
	g := grep.New()
	if g == nil {
		t.Fatal("New() returned nil")
	}
	if g.Name() != "Grep" {
		t.Errorf("Name() = %q, want %q", g.Name(), "Grep")
	}
	aliases := g.Aliases()
	if len(aliases) != 1 || aliases[0] != "grep" {
		t.Errorf("Aliases() = %v, want [grep]", aliases)
	}
	if !g.IsReadOnly(nil) {
		t.Error("IsReadOnly should be true")
	}
	if !g.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe should be true")
	}
	if !g.IsEnabled() {
		t.Error("IsEnabled should be true")
	}
	if g.Prompt() == "" {
		t.Error("Prompt should not be empty")
	}
	schema := g.InputSchema()
	if !json.Valid(schema) {
		t.Errorf("InputSchema returned invalid JSON: %s", string(schema))
	}
}

func TestNew_DescriptionWithPattern(t *testing.T) {
	g := grep.New()
	desc, err := g.Description(json.RawMessage(`{"pattern":"TODO"}`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Grep: TODO" {
		t.Errorf("Description() = %q, want %q", desc, "Grep: TODO")
	}
}

func TestNew_DescriptionWithInvalidJSON(t *testing.T) {
	g := grep.New()
	desc, err := g.Description(json.RawMessage(`{invalid}`))
	if err != nil {
		t.Fatalf("Description() should not error on bad JSON: %v", err)
	}
	// Falls back to default description
	if desc != "Search file contents with regex" {
		t.Errorf("Description fallback = %q, want %q", desc, "Search file contents with regex")
	}
}

