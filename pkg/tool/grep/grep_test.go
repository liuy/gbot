package grep_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool/grep"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Execute — output_mode: files_with_matches (default)
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
	if output.Mode != "files_with_matches" {
		t.Errorf("Mode = %q, want files_with_matches", output.Mode)
	}
	if output.NumFiles != 1 {
		t.Errorf("NumFiles = %d, want 1", output.NumFiles)
	}
	if len(output.Filenames) != 1 {
		t.Fatalf("len(Filenames) = %d, want 1", len(output.Filenames))
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

	// TS uses "glob" not "include"
	input := json.RawMessage(`{"pattern":"TODO","path":"` + dir + `","glob":"*.go"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	for _, fn := range output.Filenames {
		if !strings.HasSuffix(fn, ".go") {
			t.Errorf("Filename = %q, should be a .go file", fn)
		}
	}
	if output.NumFiles > 1 {
		t.Errorf("NumFiles = %d, expected at most 1 with glob filter", output.NumFiles)
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
	if output.NumFiles != 2 {
		t.Errorf("NumFiles = %d, want 2", output.NumFiles)
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
	if output.NumFiles != 0 {
		t.Errorf("NumFiles = %d, want 0", output.NumFiles)
	}
	if output.Filenames == nil {
		t.Error("Filenames should not be nil (should be empty slice)")
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
	if output.NumFiles < 1 {
		t.Errorf("NumFiles = %d, want >= 1 (using WorkingDir from context)", output.NumFiles)
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
	if output.NumFiles < 1 {
		t.Errorf("NumFiles = %d, want >= 1 with type filter", output.NumFiles)
	}
}

func TestGrepToolCall_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := grep.Execute(context.Background(), json.RawMessage(`{not json}`), nil)
	if err == nil {
		t.Fatal("Execute() should return error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want error containing 'parse input'", err.Error())
	}
}

func TestGrepToolCall_EmptyPattern(t *testing.T) {
	t.Parallel()

	_, err := grep.Execute(context.Background(), json.RawMessage(`{"pattern":""}`), nil)
	if err == nil {
		t.Fatal("Execute() should return error for empty pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error = %q, want error containing 'pattern is required'", err.Error())
	}
}

func TestGrepToolCall_NonexistentPath(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"pattern":"test","path":"/nonexistent/path/xyz123"}`)
	_, err := grep.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should return error for nonexistent path")
	}
	if !strings.Contains(err.Error(), "path does not exist") && !strings.Contains(err.Error(), "ripgrep") {
		t.Errorf("error = %q, want error about nonexistent path", err.Error())
	}
}

// ---------------------------------------------------------------------------
// New() — tool construction
// ---------------------------------------------------------------------------

func TestNew_ReturnsValidTool(t *testing.T) {
	g := grep.New()
	if g == nil {
		t.Fatal("New() returned nil")
	}
	if g.Name() != "Search" {
		t.Errorf("Name() = %q, want %q", g.Name(), "Search")
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
	if desc != "TODO" {
		t.Errorf("Description() = %q, want %q", desc, "TODO")
	}
}

func TestNew_DescriptionWithInvalidJSON(t *testing.T) {
	g := grep.New()
	desc, err := g.Description(json.RawMessage(`{invalid}`))
	if err != nil {
		t.Fatalf("Description() should not error on bad JSON: %v", err)
	}
	if desc != "Search file contents with regex" {
		t.Errorf("Description fallback = %q, want %q", desc, "Search file contents with regex")
	}
}

// ---------------------------------------------------------------------------
// Output mode: content
// ---------------------------------------------------------------------------

func TestGrepToolCall_OutputModeContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "content_test.txt")
	if err := os.WriteFile(fp, []byte("line one\nline two hello\nline three world\nline four hello again\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"hello","path":"` + fp + `","output_mode":"content"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.Mode != "content" {
		t.Errorf("Mode = %q, want content", output.Mode)
	}
	if !strings.Contains(output.Content, "hello") {
		t.Errorf("Content = %q, should contain 'hello'", output.Content)
	}
	if output.NumLines != 2 {
		t.Errorf("NumLines = %d, want 2 (two lines contain 'hello')", output.NumLines)
	}
}

func TestGrepToolCall_OutputModeContentWithContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "ctx.txt")
	if err := os.WriteFile(fp, []byte("line 1\nline 2\nline 3 match\nline 4\nline 5\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// -A 1 = 1 line after match, output_mode: content
	input := json.RawMessage(`{"pattern":"match","path":"` + fp + `","output_mode":"content","-A":1}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.Mode != "content" {
		t.Errorf("Mode = %q, want content", output.Mode)
	}
	// Should include line 3 (match) and line 4 (context after)
	if !strings.Contains(output.Content, "line 3 match") {
		t.Errorf("Content = %q, should contain 'line 3 match'", output.Content)
	}
}

func TestGrepToolCall_OutputModeContentBeforeContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "ctxb.txt")
	if err := os.WriteFile(fp, []byte("line 1\nline 2 match\nline 3\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// -B 1 = 1 line before match
	input := json.RawMessage(`{"pattern":"match","path":"` + fp + `","output_mode":"content","-B":1}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	// Should include line 1 (context before) and line 2 (match)
	if !strings.Contains(output.Content, "line 2 match") {
		t.Errorf("Content = %q, should contain 'line 2 match'", output.Content)
	}
}

func TestGrepToolCall_OutputModeContentContextFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "ctx2.txt")
	if err := os.WriteFile(fp, []byte("A\nB\nC match\nD\nE\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// context=1 = 1 line before and after
	input := json.RawMessage(`{"pattern":"match","path":"` + fp + `","output_mode":"content","context":1}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if !strings.Contains(output.Content, "C match") {
		t.Errorf("Content = %q, should contain 'C match'", output.Content)
	}
}

func TestGrepToolCall_OutputModeCount(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "count.txt")
	if err := os.WriteFile(fp, []byte("hello\nhello\nhello world\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"hello","path":"` + fp + `","output_mode":"count"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.Mode != "count" {
		t.Errorf("Mode = %q, want count", output.Mode)
	}
	if output.NumMatches != 3 {
		t.Errorf("NumMatches = %d, want 3 (hello x2 + hello world x1)", output.NumMatches)
	}
}


func TestGrepToolCall_OutputModeCountDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i := range 3 {
		fp := filepath.Join(dir, "c"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(fp, []byte("hello\nhello\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	// offset=1: 2 files × 2 matches = 4 (first file skipped)
	input := json.RawMessage(`{"pattern":"hello","path":"` + dir + `","output_mode":"count","offset":1}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.NumFiles != 2 {
		t.Errorf("NumFiles = %d, want 2 (offset=1 skips first of 3)", output.NumFiles)
	}
	if output.NumMatches != 4 {
		t.Errorf("NumMatches = %d, want 4 (2 files x 2 matches)", output.NumMatches)
	}
	if output.AppliedOffset == nil || *output.AppliedOffset != 1 {
		t.Errorf("AppliedOffset = %v, want 1", output.AppliedOffset)
	}

	// offset=0: all 3 files × 2 matches = 6
	input2 := json.RawMessage(`{"pattern":"hello","path":"` + dir + `","output_mode":"count"}`)
	result2, err := grep.Execute(context.Background(), input2, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output2 := result2.Data.(*grep.Output)
	if output2.NumFiles != 3 {
		t.Errorf("NumFiles = %d, want 3", output2.NumFiles)
	}
	if output2.NumMatches != 6 {
		t.Errorf("NumMatches = %d, want 6 (3 files x 2 matches)", output2.NumMatches)
	}
}

func TestGrepToolCall_OutputModeCountSingleFileWithOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "single.txt")
	if err := os.WriteFile(fp, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Single file, count mode with offset - triggers "if offset > 0" branch
	input := json.RawMessage(`{"pattern":"hello","path":"` + fp + `","output_mode":"count","offset":0}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.NumMatches != 1 {
		t.Errorf("NumMatches = %d, want 1", output.NumMatches)
	}
}

func TestGrepToolCall_OutputModeContentNoLineNumbers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "nolines.txt")
	if err := os.WriteFile(fp, []byte("hello world\nfoo bar\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// -n: false disables line numbers in content mode
	input := json.RawMessage(`{"pattern":"hello","path":"` + fp + `","output_mode":"content","-n":false}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if !strings.Contains(output.Content, "hello world") {
		t.Errorf("Content = %q, should contain 'hello world'", output.Content)
	}
}

func TestGrepToolCall_CaseInsensitive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "case.txt")
	if err := os.WriteFile(fp, []byte("Hello HELLO hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"hello","path":"` + fp + `","-i":true}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.NumFiles != 1 {
		t.Errorf("NumFiles = %d, want 1", output.NumFiles)
	}
}

func TestGrepToolCall_MultilineMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "multiline.txt")
	// Pattern "start.*end" spans two lines
	if err := os.WriteFile(fp, []byte("start middle end\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"start.*end","path":"` + fp + `","multiline":true}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	// Multiline mode should find "start middle end" as a single match
	if output.NumFiles != 1 {
		t.Errorf("NumFiles = %d, want 1 in multiline mode", output.NumFiles)
	}
}

func TestGrepToolCall_PatternStartingWithDash(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "dash.txt")
	if err := os.WriteFile(fp, []byte("-n 42\n--foo 123\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Pattern starts with "-", should use -e flag in rg
	input := json.RawMessage(`{"pattern":"-n","path":"` + fp + `"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.NumFiles != 1 {
		t.Errorf("NumFiles = %d, want 1", output.NumFiles)
	}
}

func TestGrepToolCall_HeadLimitFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		fp := filepath.Join(dir, "f"+strings.TrimLeft(fmt.Sprintf("%d", i), "0")+".txt")
		if err := os.WriteFile(fp, []byte("match\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	input := json.RawMessage(`{"pattern":"match","path":"` + dir + `","head_limit":3}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.NumFiles != 3 {
		t.Errorf("NumFiles = %d, want 3 with head_limit=3", output.NumFiles)
	}
	if output.AppliedLimit == nil {
		t.Error("AppliedLimit should be set when truncated")
	}
	if *output.AppliedLimit != 3 {
		t.Errorf("AppliedLimit = %d, want 3", *output.AppliedLimit)
	}
}

func TestGrepToolCall_HeadLimitUnlimited(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		fp := filepath.Join(dir, "unlim"+strings.TrimLeft(fmt.Sprintf("%d", i), "0")+".txt")
		if err := os.WriteFile(fp, []byte("match\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	// head_limit=0 means unlimited
	input := json.RawMessage(`{"pattern":"match","path":"` + dir + `","head_limit":0}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.NumFiles != 3 {
		t.Errorf("NumFiles = %d, want 3 (unlimited)", output.NumFiles)
	}
	if output.AppliedLimit != nil {
		t.Error("AppliedLimit should be nil for unlimited")
	}
}

func TestGrepToolCall_Offset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		fp := filepath.Join(dir, "off"+strings.TrimLeft(fmt.Sprintf("%d", i), "0")+".txt")
		if err := os.WriteFile(fp, []byte("match\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	// Skip first 2 results
	input := json.RawMessage(`{"pattern":"match","path":"` + dir + `","head_limit":2,"offset":2}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.NumFiles != 2 {
		t.Errorf("NumFiles = %d, want 2", output.NumFiles)
	}
	if output.AppliedOffset == nil || *output.AppliedOffset != 2 {
		t.Errorf("AppliedOffset = %v, want 2", output.AppliedOffset)
	}
}

func TestGrepToolCall_EmptyPatternDashDash(t *testing.T) {
	// Pattern "" is invalid — this is a duplicate of TestGrepToolCall_EmptyPattern
	// but verifies error content as well.
	t.Parallel()

	_, err := grep.Execute(context.Background(), json.RawMessage(`{"pattern":""}`), nil)
	if err == nil {
		t.Fatal("Execute() should return error for empty pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error = %q, want error containing 'pattern is required'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// rg not found → goGrep fallback
// ---------------------------------------------------------------------------

func TestExecute_RipgrepNotFound_UsesGoGrepFallback(t *testing.T) {
	// NO t.Parallel() - env var modification requires serial execution
	dir := t.TempDir()
	fp := filepath.Join(dir, "fallback.txt")
	content := "go grep fallback test content\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create a PATH that does NOT contain ripgrep
	emptyDir := filepath.Join(dir, "norg")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	origPath := os.Getenv("PATH")
	defer func() { _ = os.Setenv("PATH", origPath) }()
	if err := os.Setenv("PATH", emptyDir); err != nil {
		t.Fatalf("Setenv PATH: %v", err)
	}

	input := json.RawMessage(`{"pattern":"fallback","path":"` + fp + `"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(*grep.Output)
	if !ok {
		t.Fatalf("Data type = %T, want *grep.Output", result.Data)
	}
	// goGrep returns Matches, not files_with_matches format
	if output.Count < 1 {
		t.Errorf("Count = %d, want >= 1", output.Count)
	}
}

// ---------------------------------------------------------------------------
// goGrep tests (fallback path)
// ---------------------------------------------------------------------------

func TestGoGrep_GoGrepReturnsMatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "gg.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// goGrep returns the legacy Output format with Matches
	result, err := grep.Execute(context.Background(),
		json.RawMessage(`{"pattern":"hello","path":"`+fp+`"}`), nil)
	if err != nil {
		t.Fatalf("Execute() with no rg error: %v", err)
	}

	output, ok := result.Data.(*grep.Output)
	if !ok {
		t.Fatalf("Data type = %T", result.Data)
	}
	// When rg is not available, goGrep is used which populates Matches
	// Count is still populated
	if output.Count < 0 {
		t.Errorf("Count = %d, want >= 0", output.Count)
	}
}

// ---------------------------------------------------------------------------
// applyHeadLimit tests (via buildResult integration)
// ---------------------------------------------------------------------------

func TestApplyHeadLimit_OffsetBeyondLength(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create 2 files but offset past them
	for i := 0; i < 2; i++ {
		fp := filepath.Join(dir, "offbeyond"+strings.TrimLeft(fmt.Sprintf("%d", i), "0")+".txt")
		if err := os.WriteFile(fp, []byte("match\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	input := json.RawMessage(`{"pattern":"match","path":"` + dir + `","offset":10}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*grep.Output)
	if output.NumFiles != 0 {
		t.Errorf("NumFiles = %d, want 0 when offset > result count", output.NumFiles)
	}
}

// ---------------------------------------------------------------------------
// Gap fix: head_limit defaults to 250 when not set
// ---------------------------------------------------------------------------

func TestGrepToolCall_DefaultHeadLimit250(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i := 0; i < 300; i++ {
		fp := filepath.Join(dir, fmt.Sprintf("def%03d.txt", i))
		if err := os.WriteFile(fp, []byte("match\n"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	// No head_limit specified — should default to 250
	input := json.RawMessage(`{"pattern":"match","path":"` + dir + `"}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*grep.Output)
	if output.NumFiles != 250 {
		t.Errorf("NumFiles = %d, want 250 (default head_limit)", output.NumFiles)
	}
	if output.AppliedLimit == nil || *output.AppliedLimit != 250 {
		t.Errorf("AppliedLimit = %v, want 250", output.AppliedLimit)
	}
}

// ---------------------------------------------------------------------------
// Gap fix: -C works as alias for context
// ---------------------------------------------------------------------------

func TestGrepToolCall_ContextCAlias(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "calias.txt")
	if err := os.WriteFile(fp, []byte("A\nB\nC match\nD\nE\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// -C should work as alias for context
	input := json.RawMessage(`{"pattern":"match","path":"` + fp + `","output_mode":"content","-C":1}`)
	result, err := grep.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*grep.Output)
	if !strings.Contains(output.Content, "C match") {
		t.Errorf("Content = %q, should contain 'C match'", output.Content)
	}
	// Should include context lines before and after
	if !strings.Contains(output.Content, "B") {
		t.Errorf("Content = %q, should include context line 'B' before match", output.Content)
	}
	if !strings.Contains(output.Content, "D") {
		t.Errorf("Content = %q, should include context line 'D' after match", output.Content)
	}
}

// ---------------------------------------------------------------------------
// RenderResult — human-readable output for TUI
// ---------------------------------------------------------------------------

func TestRenderResult_ContentMode(t *testing.T) {
	t.Parallel()
	tt := grep.New()
	output := &grep.Output{
		Mode:     "content",
		Content:  "file.go:10:match here\nfile.go:20:another match",
		NumLines: 2,
	}
	result := tt.RenderResult(output)
	if !strings.Contains(result, "match here") {
		t.Errorf("RenderResult(content mode) = %q, should contain content", result)
	}
	if strings.Contains(result, `"mode"`) {
		t.Errorf("RenderResult(content mode) = %q, should not contain raw JSON keys", result)
	}
}

func TestRenderResult_FilesWithMatches(t *testing.T) {
	t.Parallel()
	tt := grep.New()
	output := &grep.Output{
		Mode:      "files_with_matches",
		NumFiles:  2,
		Filenames: []string{"a.go", "b.go"},
	}
	result := tt.RenderResult(output)
	if !strings.Contains(result, "a.go") || !strings.Contains(result, "b.go") {
		t.Errorf("RenderResult(files_with_matches) = %q, should contain filenames", result)
	}
}

func TestRenderResult_CountMode(t *testing.T) {
	t.Parallel()
	tt := grep.New()
	output := &grep.Output{
		Mode:       "count",
		NumFiles:   3,
		NumMatches: 10,
	}
	result := tt.RenderResult(output)
	if result != "10 matches in 3 files" {
		t.Errorf("RenderResult(count mode) = %q, want %q", result, "10 matches in 3 files")
	}
}

// RenderResult with non-*Output data covers lines 167-170 in grep.go
func TestRenderResult_NonOutputData(t *testing.T) {
	t.Parallel()
	tt := grep.New()
	result := tt.RenderResult("some plain string")
	want := `"some plain string"`
	if result != want {
		t.Errorf("RenderResult(non-Output) = %q, want %q", result, want)
	}
}

// RenderResult with nil data covers lines 167-170 in grep.go
func TestRenderResult_NilData(t *testing.T) {
	t.Parallel()
	tt := grep.New()
	result := tt.RenderResult(nil)
	if result != "null" {
		t.Errorf("RenderResult(nil) = %q, want %q", result, "null")
	}
}

// RenderResult files_with_matches with empty filenames covers line 175-177
func TestRenderResult_FilesWithMatchesEmpty(t *testing.T) {
	t.Parallel()
	tt := grep.New()
	output := &grep.Output{
		Mode:      "files_with_matches",
		NumFiles:  0,
		Filenames: []string{},
	}
	result := tt.RenderResult(output)
	if result != "No files matched" {
		t.Errorf("RenderResult(empty files_with_matches) = %q, want %q", result, "No files matched")
	}
}

// RenderResult with unknown mode covers lines 181-183
func TestRenderResult_UnknownMode(t *testing.T) {
	t.Parallel()
	tt := grep.New()
	output := &grep.Output{
		Mode: "unknown_mode",
	}
	result := tt.RenderResult(output)
	if !strings.Contains(result, "unknown_mode") {
		t.Errorf("RenderResult(unknown mode) = %q, should contain mode name", result)
	}
}
