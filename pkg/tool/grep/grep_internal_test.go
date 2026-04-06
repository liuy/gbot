package grep

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// goGrep — the fallback when rg is not available
// ---------------------------------------------------------------------------

func TestGoGrep_SingleFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	content := "hello world\nfoo bar\nhello again\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "hello", fp, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Data type = %T, want *Output", result.Data)
	}
	if output.Count != 2 {
		t.Errorf("Count = %d, want 2", output.Count)
	}
	for _, m := range output.Matches {
		if !strings.Contains(m.Content, "hello") {
			t.Errorf("Match content = %q, should contain 'hello'", m.Content)
		}
	}
}

func TestGoGrep_Directory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("findme alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("findme beta\nno match\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "findme", dir, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.Count != 2 {
		t.Errorf("Count = %d, want 2", output.Count)
	}
}

func TestGoGrep_NoMatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(fp, []byte("nothing to see here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "UNIQUE_PATTERN_XYZ", fp, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.Count != 0 {
		t.Errorf("Count = %d, want 0", output.Count)
	}
	if output.Matches == nil {
		t.Error("Matches should be non-nil empty slice")
	}
}

func TestGoGrep_NonexistentPath(t *testing.T) {
	t.Parallel()

	_, err := goGrep(context.Background(), "pattern", "/nonexistent/path/xyz", "")
	if err == nil {
		t.Fatal("goGrep() should return error for nonexistent path")
	}
}

func TestGoGrep_DirectoryReadError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a file (not directory) to test that the directory read path fails
	fp := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(fp, []byte("test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Pass a valid directory path
	result, err := goGrep(context.Background(), "test", dir, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}
	output := result.Data.(*Output)
	if output.Count != 1 {
		t.Errorf("Count = %d, want 1", output.Count)
	}
}

func TestGoGrep_SkipsSubdirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "top.txt"), []byte("searchme\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// File inside subdir — should be skipped by goGrep (non-recursive)
	if err := os.WriteFile(filepath.Join(subdir, "deep.txt"), []byte("searchme\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "searchme", dir, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.Count != 1 {
		t.Errorf("Count = %d, want 1 (subdirectory files should be skipped)", output.Count)
	}
}

// ---------------------------------------------------------------------------
// grepFile
// ---------------------------------------------------------------------------

func TestGrepFile_Matches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	content := "first line\nsecond match line\nthird line\nmatch again\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	matches, err := grepFile(fp, "match")
	if err != nil {
		t.Fatalf("grepFile() error: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("len(matches) = %d, want 2", len(matches))
	}
	if matches[0].Line != 2 {
		t.Errorf("matches[0].Line = %d, want 2", matches[0].Line)
	}
	if matches[1].Line != 4 {
		t.Errorf("matches[1].Line = %d, want 4", matches[1].Line)
	}
	if matches[0].File != fp {
		t.Errorf("matches[0].File = %q, want %q", matches[0].File, fp)
	}
}

func TestGrepFile_NoMatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte("nothing here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	matches, err := grepFile(fp, "UNIQUE_PATTERN")
	if err != nil {
		t.Fatalf("grepFile() error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("len(matches) = %d, want 0", len(matches))
	}
}

func TestGrepFile_NonexistentFile(t *testing.T) {
	t.Parallel()

	_, err := grepFile("/nonexistent/file.txt", "pattern")
	if err == nil {
		t.Fatal("grepFile() should return error for nonexistent file")
	}
}

func TestGrepFile_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	matches, err := grepFile(fp, "anything")
	if err != nil {
		t.Fatalf("grepFile() error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("len(matches) = %d, want 0 for empty file", len(matches))
	}
}

// ---------------------------------------------------------------------------
// Execute with ToolUseContext providing WorkingDir
// ---------------------------------------------------------------------------

func TestExecute_WorkingDirFromToolUseContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "ctx_test.txt")
	if err := os.WriteFile(fp, []byte("marker_token_12345\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tctx := &types.ToolUseContext{WorkingDir: dir}
	input := json.RawMessage(`{"pattern":"marker_token_12345"}`)
	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.Count < 1 {
		t.Errorf("Count = %d, want >= 1 (WorkingDir=%s)", output.Count, dir)
	}
}

func TestExecute_NilToolUseContext_FallsBackToGetwd(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"pattern":"xyzQwertyNoMatch123456"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output == nil {
		t.Error("Output is nil")
	}
}

func TestExecute_ToolUseContextEmptyWorkingDir(t *testing.T) {
	t.Parallel()

	tctx := &types.ToolUseContext{WorkingDir: ""}
	input := json.RawMessage(`{"pattern":"xyzQwertyNoMatch123456"}`)
	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output == nil {
		t.Error("Output is nil")
	}
}

// ---------------------------------------------------------------------------
// parseRGOutput — additional coverage
// ---------------------------------------------------------------------------

func TestParseRGOutput_MultipleFiles(t *testing.T) {
	t.Parallel()

	output := "a.go:10:hello\nb.go:20:world\na.go:30:foo\n"
	matches := parseRGOutput(output)
	if len(matches) != 3 {
		t.Fatalf("len(matches) = %d, want 3", len(matches))
	}
	if matches[0].File != "a.go" || matches[0].Line != 10 {
		t.Errorf("matches[0] = {File:%q, Line:%d}, want {a.go, 10}", matches[0].File, matches[0].Line)
	}
	if matches[1].File != "b.go" || matches[1].Line != 20 {
		t.Errorf("matches[1] = {File:%q, Line:%d}, want {b.go, 20}", matches[1].File, matches[1].Line)
	}
}

func TestParseRGOutput_ColonInContent(t *testing.T) {
	t.Parallel()

	output := "main.go:5:time.Duration(10 * time.Second)\n"
	matches := parseRGOutput(output)
	if len(matches) != 1 {
		t.Fatalf("len(matches) = %d, want 1", len(matches))
	}
	if matches[0].Content != "time.Duration(10 * time.Second)" {
		t.Errorf("Content = %q, want full content after second colon", matches[0].Content)
	}
}

func TestParseRGOutput_BlankLines(t *testing.T) {
	t.Parallel()

	output := "file.go:1:test\n\nfile.go:3:test2\n\n"
	matches := parseRGOutput(output)
	if len(matches) != 2 {
		t.Errorf("len(matches) = %d, want 2 (blank lines skipped)", len(matches))
	}
}

// ---------------------------------------------------------------------------
// parseRGOutput benchmarks
// ---------------------------------------------------------------------------

// buildRGOutput creates synthetic ripgrep output with numLines match lines.
func buildRGOutput(numLines int) string {
	var buf strings.Builder
	for i := range numLines {
		buf.WriteString("pkg/engine/engine.go:")
		// Format line number as zero-padded 3 digits
		buf.WriteByte(byte('0' + (i/100)%10))
		buf.WriteByte(byte('0' + (i/10)%10))
		buf.WriteByte(byte('0' + i%10))
		buf.WriteByte(':')
		buf.WriteString("func processRequest(ctx context.Context, input json.RawMessage) (*types.Response, error) {")
		buf.WriteByte('\n')
	}
	return buf.String()
}

func BenchmarkParseRGOutput_Small(b *testing.B) {
	output := buildRGOutput(10)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = parseRGOutput(output)
	}
}

func BenchmarkParseRGOutput_Medium(b *testing.B) {
	output := buildRGOutput(100)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = parseRGOutput(output)
	}
}

func BenchmarkParseRGOutput_Large(b *testing.B) {
	output := buildRGOutput(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = parseRGOutput(output)
	}
}

func BenchmarkParseRGOutput_Empty(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = parseRGOutput("")
	}
}
