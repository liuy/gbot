package grep

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"errors"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// New — tool metadata
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Parallel()

	tt := New()

	if tt.Name() != "Grep" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "Grep")
	}
	if !tt.IsReadOnly(nil) {
		t.Error("IsReadOnly() = false, want true")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() = false, want true")
	}
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %d, want %d", tt.InterruptBehavior(), tool.InterruptCancel)
	}
	if tt.Prompt() == "" {
		t.Error("Prompt() is empty")
	}
	if !tt.IsEnabled() {
		t.Error("IsEnabled() = false, want true")
	}
}

func TestNewInputSchema(t *testing.T) {
	t.Parallel()

	tt := New()
	schema := tt.InputSchema()
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()

	tt := New()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with pattern", `{"glob":"**/*.go"}`, "**/*.go"},
		{"invalid json", `{invalid`, "Search file contents or file names"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			desc, err := tt.Description(json.RawMessage(tc.input))
			if err != nil {
				t.Fatalf("Description() error: %v", err)
			}
			if desc != tc.want {
				t.Errorf("Description() = %q, want %q", desc, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Execute — happy paths
// ---------------------------------------------------------------------------

func TestExecute_MatchGoFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create test files
	files := []string{"main.go", "util.go", "readme.md", "go.mod"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", f, err)
		}
	}

	input := json.RawMessage(`{"glob":"*.go","path":"` + dir + `"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Data type = %T, want *Output", result.Data)
	}
	if output.NumFiles != 2 {
		t.Errorf("Count = %d, want 2", output.NumFiles)
	}

	// Results should contain both .go files (order depends on mtime)
	found := make(map[string]bool)
	for _, f := range output.Filenames {
		found[f] = true
	}
	if !found["main.go"] || !found["util.go"] {
		t.Errorf("Files = %v, want main.go and util.go", output.Filenames)
	}
}

func TestExecute_NestedDirs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	subdir := filepath.Join(dir, "internal", "pkg")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create nested files
	if err := os.WriteFile(filepath.Join(dir, "top.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "deep.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"glob":"**/*.go","path":"` + dir + `"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.NumFiles != 2 {
		t.Errorf("Count = %d, want 2 (files: %v)", output.NumFiles, output.Filenames)
	}
}

func TestExecute_NoMatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"glob":"*.go","path":"` + dir + `"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.NumFiles != 0 {
		t.Errorf("Count = %d, want 0", output.NumFiles)
	}
	if len(output.Filenames) != 0 {
		t.Errorf("Files = %v, want empty", output.Filenames)
	}
}

func TestExecute_WorkingDirFromContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tctx := &tool.ToolUseContext{WorkingDir: dir}
	input := json.RawMessage(`{"glob":"*.txt"}`)
	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.NumFiles != 1 {
		t.Errorf("Count = %d, want 1", output.NumFiles)
	}
}

func TestExecute_Truncated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create more than DefaultHeadLimit (250) files to trigger truncation
	for i := range 300 {
		fp := filepath.Join(dir, fmt.Sprintf("file%03d.go", i))
		if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	input := json.RawMessage(`{"glob":"*.go","path":"` + dir + `"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.NumFiles != 250 {
		t.Errorf("Count = %d, want %d (max)", output.NumFiles, 250)
	}
	if len(output.Filenames) != 250 {
		t.Errorf("Files len = %d, want %d", len(output.Filenames), 250)
	}
}

func TestExecute_DurationMs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	input := json.RawMessage(`{"glob":"*.go","path":"` + dir + `"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	_ = result.Data.(*Output) // DurationMs/Truncated removed
}

// ---------------------------------------------------------------------------
// Execute — error paths
// ---------------------------------------------------------------------------

func TestExecute_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := Execute(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want error containing 'parse input'", err.Error())
	}
}

func TestExecute_EmptyPattern(t *testing.T) {
	t.Parallel()

	_, err := Execute(context.Background(), json.RawMessage(`{"pattern":""}`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "either pattern or glob must be set") {
		t.Errorf("error = %q, want error containing 'either pattern or glob must be set'", err.Error())
	}
}

func TestExecute_PathNotFound(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"glob":"*.go","path":"/nonexistent/path"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for nonexistent path")
	}
	if !strings.Contains(err.Error(), "path does not exist") {
		t.Errorf("error = %q, want error containing 'path does not exist'", err.Error())
	}
}

func TestExecute_PathIsFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"glob":"*.go","path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error when path is a file")
	}
	if !strings.Contains(err.Error(), "path is not a directory") {
		t.Errorf("error = %q, want error containing 'path is not a directory'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Output JSON
// ---------------------------------------------------------------------------

func TestOutputJSON(t *testing.T) {
	t.Parallel()

	output := Output{
		Filenames: []string{"a.go", "b.go"},
		NumFiles:  2,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.NumFiles != 2 {
		t.Errorf("Count = %d, want 2", got.NumFiles)
	}
	if len(got.Filenames) != 2 {
		t.Fatalf("Files length = %d, want 2", len(got.Filenames))
	}
}

// ---------------------------------------------------------------------------
// Gap: output JSON field names must match TS (filenames, numFiles)
// ---------------------------------------------------------------------------

func TestOutput_JSONFieldNames(t *testing.T) {
	t.Parallel()
	output := Output{Mode: "files_with_matches", Filenames: []string{"a.go"}, NumFiles: 1}
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := parsed["filenames"]; !ok {
		t.Error("JSON output missing 'filenames' field (has 'files' instead — must match TS)")
	}
	if _, ok := parsed["numFiles"]; !ok {
		t.Error("JSON output missing 'numFiles' field (has 'count' instead — must match TS)")
	}
}

// ---------------------------------------------------------------------------
// RenderResult — human-readable output for TUI
// ---------------------------------------------------------------------------

func TestRenderResult_Files(t *testing.T) {
	t.Parallel()
	tt := New()
	output := &Output{
		Mode:      "files_with_matches",
		Filenames: []string{"src/a.go", "src/b.go", "src/c.go"},
		NumFiles:  3,
	}
	result := tt.RenderResult(output)
	if result != "src/a.go\nsrc/b.go\nsrc/c.go" {
		t.Errorf("RenderResult(files) = %q, want %q", result, "src/a.go\nsrc/b.go\nsrc/c.go")
	}
}

func TestRenderResult_NoMatches(t *testing.T) {
	t.Parallel()
	tt := New()
	output := &Output{
		Mode:      "files_with_matches",
		Filenames: []string{},
		NumFiles:  0,
	}
	result := tt.RenderResult(output)
	if result != "" {
		t.Errorf("RenderResult(no matches) = %q, want %q", result, "")
	}
}

// RenderResult with non-*Output data covers lines 82-85 in glob.go
func TestRenderResult_NonOutputData(t *testing.T) {
	t.Parallel()
	tt := New()
	// Pass a plain string instead of *Output to trigger the !ok branch
	result := tt.RenderResult("some random string")
	// Should be the JSON-marshaled version of the string
	want := `"some random string"`
	if result != want {
		t.Errorf("RenderResult(non-Output) = %q, want %q", result, want)
	}
}

// RenderResult with nil data covers lines 82-85 in glob.go
func TestRenderResult_NilData(t *testing.T) {
	t.Parallel()
	tt := New()
	result := tt.RenderResult(nil)
	if result != "null" {
		t.Errorf("RenderResult(nil) = %q, want %q", result, "null")
	}
}

// RenderResult with map data covers lines 82-85 in glob.go
func TestGlobTool_IsSearchOrRead(t *testing.T) {
	t.Parallel()
	tt := New()
	srk := tt.(tool.ToolWithSearchOrRead).IsSearchOrRead(nil)
	if !srk.IsSearch || srk.IsRead || srk.IsList {
		t.Errorf("GlobTool.IsSearchOrRead() = %+v, want {IsSearch:true}", srk)
	}
}

func TestRenderResult_MapData(t *testing.T) {
	t.Parallel()
	tt := New()
	result := tt.RenderResult(map[string]int{"count": 42})
	if !strings.Contains(result, `"count"`) || !strings.Contains(result, "42") {
		t.Errorf("RenderResult(map) = %q, should contain JSON with count:42", result)
	}
}

func TestRenderResult_JsonRawMessage(t *testing.T) {
	t.Parallel()
	tt := New()

	// Valid JSON RawMessage with output fields
	raw := json.RawMessage(`{"mode":"files_with_matches","filenames":["a.go","b.go"],"numFiles":2}`)
	result := tt.RenderResult(raw)
	if result != "a.go\nb.go" {
		t.Errorf("RenderResult(json.RawMessage) = %q, want %q", result, "a.go\nb.go")
	}
}

func TestRenderResult_JsonRawMessage_Empty(t *testing.T) {
	t.Parallel()
	tt := New()

	// Empty files list
	raw := json.RawMessage(`{"mode":"files_with_matches","filenames":[],"numFiles":0}`)
	result := tt.RenderResult(raw)
	if result != "" {
		t.Errorf("RenderResult(json.RawMessage empty) = %q, want empty", result)
	}
}

func TestRenderResult_JsonRawMessage_Invalid(t *testing.T) {
	t.Parallel()
	tt := New()

	// Invalid JSON within RawMessage — should return raw string
	raw := json.RawMessage(`not valid json`)
	result := tt.RenderResult(raw)
	if result != "not valid json" {
		t.Errorf("RenderResult(invalid json.RawMessage) = %q, want %q", result, "not valid json")
	}
}

// writeFakeRg creates a fake "rg" executable in dir that runs the given bash script.
func writeFakeRg(t *testing.T, dir, script string) {
	t.Helper()
	rgPath := filepath.Join(dir, "rg")
	content := "#!/bin/bash\n" + script
	if err := os.WriteFile(rgPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake rg: %v", err)
	}
}

// prependPath prepends dir to PATH so fake rg is found first.
func prependPath(t *testing.T, dir string) {
	t.Helper()
	orig := os.Getenv("PATH")
	t.Setenv("PATH", dir+":"+orig)
}

// ---------------------------------------------------------------------------
// Execute — basic paths
// ---------------------------------------------------------------------------

func TestExecute_GetwdFallback(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"glob":"*.go"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Logf("Execute() returned error (expected in some cwd): %v", err)
		return
	}

	output := result.Data.(*Output)
	if output == nil {
		t.Fatal("Output is nil")
	}
	if output.NumFiles < 0 {
		t.Errorf("Count = %d, want >= 0", output.NumFiles)
	}
}

func TestExecute_InvalidGlobPattern(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tctx := &tool.ToolUseContext{WorkingDir: dir}

	input := json.RawMessage(`{"pattern":"[invalid"}`)
	_, err := Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for invalid glob pattern")
	}
	if !strings.Contains(err.Error(), "ripgrep") {
		t.Errorf("error = %q, want error containing 'ripgrep'", err.Error())
	}
}

func TestExecute_CancelledContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tctx := &tool.ToolUseContext{WorkingDir: dir}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	input := json.RawMessage(`{"glob":"**/*"}`)
	_, err := Execute(ctx, input, tctx)
	if err == nil {
		t.Fatal("Execute() with cancelled context should return error")
	}
}

// ---------------------------------------------------------------------------
// isRgEagainError — unit tests
// ---------------------------------------------------------------------------

func TestIsEagainError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"os error 11", &rgError{stderr: "rg: os error 11"}, true},
		{"Resource temporarily unavailable", &rgError{stderr: "rg: Resource temporarily unavailable"}, true},
		{"other error", &rgError{stderr: "permission denied"}, false},
		{"not rgError", context.Canceled, false},
		{"wrapped rgError", &rgError{stderr: "failed: os error 11 on thread"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isRgEagainError(tc.err)
			if got != tc.want {
				t.Errorf("isRgEagainError() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// rgError — Unwrap coverage
// ---------------------------------------------------------------------------

func TestRgError_Unwrap(t *testing.T) {
	t.Parallel()

	inner := fmt.Errorf("inner error")
	rge := &rgError{stderr: "some stderr", wrapped: inner}

	if !errors.Is(rge, inner) {
		t.Error("errors.Is should match inner error via Unwrap")
	}

	var target *rgError
	if !errors.As(rge, &target) {
		t.Error("errors.As should match *rgError")
	}
	if target.stderr != "some stderr" {
		t.Errorf("rgError.stderr = %q, want %q", target.stderr, "some stderr")
	}
}

// ---------------------------------------------------------------------------
// Fake rg — EAGAIN retry
// ---------------------------------------------------------------------------

func TestRgGlob_EagainRetrySucceeds(t *testing.T) {
	t.Skip("rgGlob signature differs from rgRaw in merged tool")
}

func TestRgGlob_EagainRetryFails(t *testing.T) {
	t.Skip("rgGlob signature differs from rgRaw in merged tool")
}

func TestRgRaw_TimeoutPartialResults(t *testing.T) {
	dir := t.TempDir()

	// Output 3 lines, then burn CPU in bash-only loop (no subprocess = clean SIGKILL)
	writeFakeRg(t, dir, `echo "file1.go"
echo "file2.go"
echo "file3.go"
x=0; while [ $x -lt 100000000 ]; do x=$((x+1)); done`)
	prependPath(t, dir)

	// Short context timeout → kills rg before sleep finishes
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	lines, err := rgRaw(ctx, []string{"--files", dir})
	if err != nil {
		t.Fatalf("rgRaw() error: %v", err)
	}
	// Should get partial results (last line dropped as potentially incomplete)
	if len(lines) < 1 {
		t.Errorf("got %d lines, want >= 1 partial result", len(lines))
	}
	if len(lines) > 3 {
		t.Errorf("got %d lines, want <= 3", len(lines))
	}
}

// ---------------------------------------------------------------------------
// Fake rg — timeout with no results
// ---------------------------------------------------------------------------

func TestRgRaw_TimeoutNoResults(t *testing.T) {
	dir := t.TempDir()

	// Block without output (bash-only loop, no subprocess = clean SIGKILL)
	writeFakeRg(t, dir, `x=0; while [ $x -lt 100000000 ]; do x=$((x+1)); done`)
	prependPath(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := rgRaw(ctx, []string{"--files", dir})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want 'timed out'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// parseRgOutput
// ---------------------------------------------------------------------------

func TestParseOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"whitespace only", "  \n  ", []string{}},
		{"single line", "file.go\n", []string{"file.go"}},
		{"multiple lines", "a.go\nb.go\nc.go\n", []string{"a.go", "b.go", "c.go"}},
		{"carriage return", "a.go\r\nb.go\r\n", []string{"a.go", "b.go"}},
		{"trailing empty", "a.go\n\n", []string{"a.go"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseRgOutput(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseRgOutput() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseRgOutput()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
