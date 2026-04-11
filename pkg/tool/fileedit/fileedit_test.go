package fileedit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/types"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tt := fileedit.New()

	if tt.Name() != "Edit" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "Edit")
	}
	if tt.IsReadOnly(nil) {
		t.Error("IsReadOnly() = true, want false")
	}
	if tt.IsDestructive(nil) {
		t.Error("IsDestructive() = true, want false")
	}
	if tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() = true, want false")
	}
	if !tt.IsEnabled() {
		t.Error("IsEnabled() = false, want true")
	}
	if tt.Prompt() == "" {
		t.Error("Prompt() is empty")
	}
}

func TestNewInputSchema(t *testing.T) {
	t.Parallel()

	tt := fileedit.New()
	schema := tt.InputSchema()
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()

	tt := fileedit.New()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with path", `{"file_path":"/tmp/test.go"}`, "/tmp/test.go"},
		{"invalid json", `{invalid`, "Edit a file with string replacement"},
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

func TestExecute_SingleReplacement(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	content := "hello world\nfoo bar\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello world","new_string":"hello gbot"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(*fileedit.Output)
	if !ok {
		t.Fatalf("Data type = %T, want *fileedit.Output", result.Data)
	}
	if output.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", output.FilePath, fp)
	}
	if output.OldString != "hello world" {
		t.Errorf("OldString = %q, want %q", output.OldString, "hello world")
	}
	if output.NewString != "hello gbot" {
		t.Errorf("NewString = %q, want %q", output.NewString, "hello gbot")
	}
	if output.ReplaceAll {
		t.Error("ReplaceAll = true, want false")
	}

	// Verify file was actually modified
	data, _ := os.ReadFile(fp)
	if !strings.Contains(string(data), "hello gbot") {
		t.Errorf("File content = %q, should contain 'hello gbot'", string(data))
	}
	if strings.Contains(string(data), "hello world") {
		t.Errorf("File content should NOT contain 'hello world'")
	}
}

func TestExecute_ReplaceAll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "multi.txt")
	content := "foo bar foo baz foo\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"foo","new_string":"qux","replace_all":true}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileedit.Output)
	if !output.ReplaceAll {
		t.Error("ReplaceAll = false, want true")
	}

	data, _ := os.ReadFile(fp)
	if strings.Contains(string(data), "foo") {
		t.Errorf("File still contains 'foo': %q", string(data))
	}
	got := string(data)
	want := "qux bar qux baz qux\n"
	if got != want {
		t.Errorf("File content = %q, want %q", got, want)
	}
}

func TestExecute_NewFileCreation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "newfile.txt")

	// old_string="" on nonexistent file → creates new file
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"","new_string":"hello new file\n"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileedit.Output)
	if output.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", output.FilePath, fp)
	}

	data, _ := os.ReadFile(fp)
	if string(data) != "hello new file\n" {
		t.Errorf("File content = %q, want %q", string(data), "hello new file\n")
	}

	// Verify file is readable (perm 0644, not 0000)
	info, statErr := os.Stat(fp)
	if statErr != nil {
		t.Fatalf("Stat: %v", statErr)
	}
	perm := info.Mode().Perm()
	if perm&0o444 == 0 {
		t.Errorf("File permissions = %o, should be readable", perm)
	}
}

func TestExecute_EmptyFileEdit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// old_string="" on empty existing file → valid
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"","new_string":"now has content"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(fp)
	if string(data) != "now has content" {
		t.Errorf("File content = %q, want %q", string(data), "now has content")
	}
	_ = result
}

func TestExecute_CurlyQuoteMatching(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "quotes.txt")
	// File has curly quotes
	content := "\u201CHello World\u201D\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Search with straight quotes — should match via normalization
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"\"Hello World\"","new_string":"\"Goodbye World\""}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileedit.Output)
	// oldString should be the actual curly-quoted string from the file
	if output.OldString != "\u201CHello World\u201D" {
		t.Errorf("OldString = %q, want curly-quoted version", output.OldString)
	}
	// newString should have curly quotes preserved
	if output.NewString != "\u201CGoodbye World\u201D" {
		t.Errorf("NewString = %q, want curly-quoted version", output.NewString)
	}

	data, _ := os.ReadFile(fp)
	// File should have curly quotes
	if !strings.Contains(string(data), "\u201CGoodbye World\u201D") {
		t.Errorf("File content = %q, should have curly quotes", string(data))
	}
}

func TestExecute_CRLFNormalization(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "crlf.txt")
	// File with CRLF line endings
	content := "line1\r\nline2\r\nline3\r\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Search with LF — should match after CRLF normalization
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"line2","new_string":"replaced"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(fp)
	got := string(data)
	// CRLF should be preserved in output
	if !strings.Contains(got, "line1\r\n") {
		t.Errorf("CRLF should be preserved in output, got: %q", got)
	}
	if !strings.Contains(got, "replaced\r\n") {
		t.Errorf("replaced line should have CRLF, got: %q", got)
	}
	_ = result
}

func TestExecute_UTF16LEWithBOM(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "bom.txt")

	// Write UTF-16 LE with BOM
	bom := []byte{0xFF, 0xFE}
	content := "hello world"
	encoded := make([]byte, len(bom)+len(content)*2)
	copy(encoded, bom)
	for i, r := range content {
		v := uint16(r)
		encoded[len(bom)+i*2] = byte(v)
		encoded[len(bom)+i*2+1] = byte(v >> 8)
	}
	if err := os.WriteFile(fp, encoded, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Edit the file — should detect BOM and decode/encode correctly
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// Read back and verify it's still UTF-16 LE with BOM
	data, _ := os.ReadFile(fp)
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xFE {
		t.Fatal("File should start with UTF-16 LE BOM")
	}
	// Decode the content after BOM
	decoded := make([]uint16, (len(data)-2)/2)
	for i := range decoded {
		decoded[i] = uint16(data[2+i*2]) | uint16(data[2+i*2+1])<<8
	}
	text := strings.ToLower(string(rune(decoded[0])))
	if text != "g" {
		t.Errorf("First decoded char = %q, want 'g' (from 'goodbye')", text)
	}
	_ = result
}

func TestExecute_DeleteWithTrailingNewline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "delete.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Delete "line2" with empty new_string — should strip trailing newline too
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"line2","new_string":""}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(fp)
	got := string(data)
	// Should be "line1\nline3\n" — the trailing newline after "line2" was stripped
	if got != "line1\nline3\n" {
		t.Errorf("File content = %q, want %q", got, "line1\nline3\n")
	}
	_ = result
}

func TestExecute_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := fileedit.Execute(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("Error = %q, want 'parse input'", err.Error())
	}
}

func TestExecute_EmptyFilePath(t *testing.T) {
	t.Parallel()

	_, err := fileedit.Execute(context.Background(), json.RawMessage(`{"file_path":"","old_string":"a","new_string":"b"}`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("Error = %q, want 'file_path is required'", err.Error())
	}
}

func TestExecute_SameOldAndNewString(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "same.txt")
	content := "hello\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"hello"}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for same old/new string")
	}
	if !strings.Contains(err.Error(), "no changes to make") {
		t.Errorf("Error = %q, want 'no changes to make'", err.Error())
	}
}

func TestExecute_FileNotFound(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"file_path":"/nonexistent/file.txt","old_string":"foo","new_string":"bar"}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing file")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Error = %q, want 'does not exist'", err.Error())
	}
}

func TestExecute_OldStringNotFound(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(fp, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"notfound","new_string":"bar"}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error when old_string not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Error = %q, want 'not found'", err.Error())
	}
}

func TestExecute_NonUniqueOldString(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "dup.txt")
	content := "foo bar foo\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"foo","new_string":"baz"}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for non-unique old_string")
	}
	if !strings.Contains(err.Error(), "replace_all is false") {
		t.Errorf("Error = %q, want 'replace_all is false'", err.Error())
	}
	if !strings.Contains(err.Error(), "found 2 matches") {
		t.Errorf("Error = %q, want 'found 2 matches'", err.Error())
	}
}

func TestExecute_ExistingFileWithEmptyOldString(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(fp, []byte("has content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Empty old_string on existing non-empty file → error
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"","new_string":"new content"}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for existing non-empty file with empty old_string")
	}
	if !strings.Contains(err.Error(), "file already exists") {
		t.Errorf("Error = %q, want 'file already exists'", err.Error())
	}
}

func TestOutputJSON(t *testing.T) {
	t.Parallel()

	output := fileedit.Output{
		FilePath:   "/tmp/test.txt",
		OldString:  "old",
		NewString:  "new",
		ReplaceAll: true,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got fileedit.Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.FilePath != "/tmp/test.txt" {
		t.Errorf("FilePath = %q, want /tmp/test.txt", got.FilePath)
	}
	if got.OldString != "old" {
		t.Errorf("OldString = %q, want 'old'", got.OldString)
	}
	if got.NewString != "new" {
		t.Errorf("NewString = %q, want 'new'", got.NewString)
	}
	if !got.ReplaceAll {
		t.Error("ReplaceAll = false, want true")
	}
}

func TestExecute_PreservesPermissions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "perm.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	info, statErr := os.Stat(fp)
	if statErr != nil {
		t.Fatalf("Stat: %v", statErr)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("Permissions = %o, want 0600", perm)
	}
}


// ---------------------------------------------------------------------------
// Task #20: Must-read-first + staleness rejection
// ---------------------------------------------------------------------------

func TestExecute_MustReadFirst_RejectsUnread(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "unread.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tctx := &types.ToolUseContext{
		ReadFileState: make(map[string]types.FileState),
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	_, err := fileedit.Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() should reject edit to unread file")
	}
	if !strings.Contains(err.Error(), "not been read") && !strings.Contains(err.Error(), "read it first") {
		t.Errorf("Error = %q, want 'not been read' or 'read it first'", err.Error())
	}
}

func TestExecute_MustReadFirst_RejectsPartialRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "partial.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fp)
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			fp: {
				Content:       "hello world\n",
				Timestamp:     info.ModTime().UnixMilli(),
				IsPartialView: true,
			},
		},
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	_, err := fileedit.Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() should reject edit to partially-read file")
	}
	if !strings.Contains(err.Error(), "not been read") && !strings.Contains(err.Error(), "read it first") {
		t.Errorf("Error = %q, want 'not been read' or 'read it first'", err.Error())
	}
}

func TestExecute_Staleness_RejectsStaleRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(fp, []byte("old content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fp)
	oldMtime := info.ModTime().UnixMilli()
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			fp: {
				Content:       "old content\n",
				Timestamp:     oldMtime,
				IsPartialView: false,
			},
		},
	}
	// Modify file after recording read state
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(fp, []byte("modified by others\n"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"old content","new_string":"new content"}`)
	_, err := fileedit.Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() should reject edit to stale file")
	}
	if !strings.Contains(err.Error(), "modified since read") && !strings.Contains(err.Error(), "read it again") {
		t.Errorf("Error = %q, want 'modified since read' or 'read it again'", err.Error())
	}
}

func TestExecute_MustReadFirst_AllowsNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "new.txt")
	// File doesn't exist — no read required
	tctx := &types.ToolUseContext{
		ReadFileState: make(map[string]types.FileState),
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"","new_string":"hello new file\n"}`)
	result, err := fileedit.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*fileedit.Output)
	if output.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", output.FilePath, fp)
	}
}

func TestExecute_MustReadFirst_AllowsFullRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "fullread.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fp)
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			fp: {
				Content:       "hello world\n",
				Timestamp:     info.ModTime().UnixMilli(),
				IsPartialView: false,
			},
		},
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	result, err := fileedit.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*fileedit.Output)
	if output.NewString != "goodbye" {
		t.Errorf("NewString = %q, want 'goodbye'", output.NewString)
	}
}

// ---------------------------------------------------------------------------
// Task #20: Desanitize — sanitized tags should match their real counterparts
// ---------------------------------------------------------------------------

func TestExecute_DesanitizeMatchesFunctionResults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "desanitize.txt")
	content := "before\n<function_results>data here</function_results>\nafter\n"
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	// Model sends sanitized <fnr> but file has <function_results>
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"<fnr>data here</fnr>","new_string":"<fnr>replaced</fnr>"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v (desanitize should handle <fnr>)", err)
	}
	output := result.Data.(*fileedit.Output)
	// The actual old/new strings should be the desanitized versions
	if !strings.Contains(output.OldString, "<function_results>") {
		t.Errorf("OldString = %q, should contain '<function_results>'", output.OldString)
	}
	if !strings.Contains(output.NewString, "<function_results>") {
		t.Errorf("NewString = %q, should contain '<function_results>'", output.NewString)
	}
	data, _ := os.ReadFile(fp)
	if strings.Contains(string(data), "<fnr>") {
		t.Errorf("File should not contain sanitized '<fnr>', got: %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// Task #20: Structured patch output
// ---------------------------------------------------------------------------


func TestExecute_StructuredPatchOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "patch.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"line2","new_string":"replaced"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*fileedit.Output)
	if output.OriginalFile == nil {
		t.Error("OriginalFile = nil, want original content")
	} else if *output.OriginalFile != "line1\nline2\nline3\n" {
		t.Errorf("OriginalFile = %q, want original content", *output.OriginalFile)
	}
	if len(output.StructuredPatch) == 0 {
		t.Error("StructuredPatch is empty, want at least one hunk")
	}
}

// ---------------------------------------------------------------------------
// Cover fileedit.go:109-111 — relative path with WorkingDir
// ---------------------------------------------------------------------------

func TestExecute_RelativePathWithWorkingDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "rel.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Use a relative path "rel.txt" with WorkingDir set to dir
	info, _ := os.Stat(fp)
	tctx := &types.ToolUseContext{
		WorkingDir: dir,
		ReadFileState: map[string]types.FileState{
			fp: {
				Content:   "hello world\n",
				Timestamp: info.ModTime().UnixMilli(),
			},
		},
	}
	input := json.RawMessage(`{"file_path":"rel.txt","old_string":"hello","new_string":"goodbye"}`)
	result, err := fileedit.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*fileedit.Output)
	if output.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", output.FilePath, fp)
	}
	data, _ := os.ReadFile(fp)
	if !strings.Contains(string(data), "goodbye") {
		t.Errorf("File content = %q, should contain 'goodbye'", string(data))
	}
}

// ---------------------------------------------------------------------------
// Cover fileedit.go:291-297 — expandPath with ~/ prefix
// ---------------------------------------------------------------------------

func TestExecute_TildePathExpansion(t *testing.T) {
	t.Parallel()
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}

	// Create a temp file directly under HOME so ~/ expansion resolves to it
	fp := filepath.Join(home, ".gbot_test_tilde_expand.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Remove(fp) }()

	tildePath := "~/.gbot_test_tilde_expand.txt"

	input := json.RawMessage(`{"file_path":"` + tildePath + `","old_string":"hello","new_string":"expanded"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*fileedit.Output)
	if output.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", output.FilePath, fp)
	}
	data, _ := os.ReadFile(fp)
	if !strings.Contains(string(data), "expanded") {
		t.Errorf("File content = %q, should contain 'expanded'", string(data))
	}
}

// ---------------------------------------------------------------------------
// Cover fileedit.go:296-297 — expandPath with ~/ but HOME empty
// ---------------------------------------------------------------------------

func TestExecute_TildePathWithNoHome(t *testing.T) {
	// Save and clear HOME (no t.Parallel — env var is process-global)
	origHome := os.Getenv("HOME")
	_ = os.Unsetenv("HOME")
	defer func() { _ = os.Setenv("HOME", origHome) }()

	dir := t.TempDir()
	fp := filepath.Join(dir, "nohome.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// With HOME unset, ~/foo falls back to filepath.Abs which resolves
	// relative to CWD. We need to give an absolute path disguised with ~
	// but since HOME is unset, expandPath will call filepath.Abs("~/nohome.txt")
	// which won't resolve correctly. The test just needs to exercise the fallback
	// path. We can't make it succeed (the file won't be found), but we can
	// verify the error path is hit.
	input := json.RawMessage(`{"file_path":"~/nonexistent_no_home.txt","old_string":"hello","new_string":"world"}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() should fail when ~ expansion fails")
	}
}

// ---------------------------------------------------------------------------
// RenderResult
// ---------------------------------------------------------------------------

func TestRenderResult_WithPatch(t *testing.T) {
	t.Parallel()
	tt := fileedit.New()
	original := "line1\nline2\nline3\n"
	out := &fileedit.Output{
		FilePath:   "/tmp/test.txt",
		OldString:  "line2",
		NewString:  "replaced",
		ReplaceAll: false,
		OriginalFile: &original,
		StructuredPatch: []fileedit.PatchHunk{
			{
				OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 3,
				Lines: []string{" line1", "-line2", "+replaced", " line3"},
			},
		},
	}
	got := tt.RenderResult(out)
	if !strings.Contains(got, "Added") || !strings.Contains(got, "removed") {
		t.Errorf("expected summary with Added/removed, got: %q", got)
	}
}

func TestRenderResult_AdditionsOnly(t *testing.T) {
	t.Parallel()
	tt := fileedit.New()
	original := "line1\n"
	out := &fileedit.Output{
		FilePath:   "/tmp/test.txt",
		OldString:  "line1",
		NewString:  "line1\nline2",
		ReplaceAll: false,
		OriginalFile: &original,
		StructuredPatch: []fileedit.PatchHunk{
			{
				OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 2,
				Lines: []string{" line1", "+line2"},
			},
		},
	}
	got := tt.RenderResult(out)
	if !strings.Contains(got, "Added") {
		t.Errorf("expected 'Added', got: %q", got)
	}
	if strings.Contains(got, "removed") {
		t.Errorf("should not contain 'removed' for additions-only, got: %q", got)
	}
}

func TestRenderResult_RemovalsOnly(t *testing.T) {
	t.Parallel()
	tt := fileedit.New()
	original := "line1\nline2\n"
	out := &fileedit.Output{
		FilePath:   "/tmp/test.txt",
		OldString:  "line2",
		NewString:  "",
		ReplaceAll: false,
		OriginalFile: &original,
		StructuredPatch: []fileedit.PatchHunk{
			{
				OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 1,
				Lines: []string{" line1", "-line2"},
			},
		},
	}
	got := tt.RenderResult(out)
	if !strings.Contains(got, "Removed") {
		t.Errorf("expected 'Removed', got: %q", got)
	}
	if strings.Contains(got, "Added") {
		t.Errorf("should not contain 'Added' for removals-only, got: %q", got)
	}
}

func TestRenderResult_NoPatch(t *testing.T) {
	t.Parallel()
	tt := fileedit.New()
	out := &fileedit.Output{
		FilePath:        "/tmp/test.txt",
		OldString:       "old",
		NewString:       "new",
		ReplaceAll:      false,
		StructuredPatch: nil,
	}
	got := tt.RenderResult(out)
	if got != "" {
		t.Errorf("expected empty for no patch, got: %q", got)
	}
}

func TestRenderResult_NonOutputData(t *testing.T) {
	t.Parallel()
	tt := fileedit.New()
	got := tt.RenderResult(42)
	if !strings.Contains(got, "42") {
		t.Errorf("expected fallback string representation, got: %q", got)
	}
}

// ---------------------------------------------------------------------------
// getStructuredPatch fallback (diffmatchpatch) — requires oldLines*newLines > 10M
// ---------------------------------------------------------------------------

func TestExecute_LargeFileFallbackPath(t *testing.T) {
	// Generate large content where len(oldLines)*len(newLines) > 10M
	// 4000 lines each → 16M entries → triggers ComputePatch → nil → diffmatchpatch fallback.
	// Each line ~50 bytes → total ~200KB, very fast in memory.
	// Lines are globally unique (numbered) to avoid "non-unique old_string" error.
	lines := make([]string, 4000)
	for i := range lines {
		lines[i] = fmt.Sprintf("%05d unique content line for large file test\n", i)
	}
	oldContent := strings.Join(lines, "")

	// Replace line 2000 uniquely
	oldLine := lines[2000]
	newLine := fmt.Sprintf("%05d modified content line for large file test\n", 2000)
	newContent := strings.Replace(oldContent, oldLine, newLine, 1)

	// Verify trigger condition
	oldLineCount := len(strings.Split(strings.TrimSuffix(oldContent, "\n"), "\n"))
	newLineCount := len(strings.Split(strings.TrimSuffix(newContent, "\n"), "\n"))
	if oldLineCount*newLineCount <= 10_000_000 {
		t.Fatalf("test content too small to trigger fallback: %d*%d <= 10M", oldLineCount, newLineCount)
	}

	dir := t.TempDir()
	fp := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(fp, []byte(oldContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Edit one line — triggers fallback path in getStructuredPatch
	inp := fileedit.Input{FilePath: fp, OldString: strings.TrimSpace(oldLine), NewString: strings.TrimSpace(newLine)}
	inputBytes, _ := json.Marshal(inp)
	input := json.RawMessage(inputBytes)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*fileedit.Output)
	if len(output.StructuredPatch) == 0 {
		t.Error("StructuredPatch should have at least one hunk")
	}
	added, removed := 0, 0
	for _, hunk := range output.StructuredPatch {
		for _, l := range hunk.Lines {
			if len(l) > 0 {
				switch l[0] {
				case '+':
					added++
				case '-':
					removed++
				}
			}
		}
	}
	if added != 1 || removed != 1 {
		t.Errorf("expected 1 added, 1 removed; got %d added, %d removed", added, removed)
	}
}

func TestExecute_LargeFileInsert(t *testing.T) {
	// Test fallback with an insertion: add a line at the end
	lines := make([]string, 5000)
	for i := range lines {
		lines[i] = fmt.Sprintf("%05d insert test line\n", i)
	}
	oldContent := strings.Join(lines, "")
	dir := t.TempDir()
	fp := filepath.Join(dir, "insert.txt")
	if err := os.WriteFile(fp, []byte(oldContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Replace last line: append a new line after it
	lastLine := fmt.Sprintf("%05d insert test line", 4999)
	newLine := fmt.Sprintf("%05d insert test line\n%05d appended new line", 4999, 5000)
	inp := fileedit.Input{FilePath: fp, OldString: lastLine, NewString: newLine}
	inputBytes, _ := json.Marshal(inp)
	input := json.RawMessage(inputBytes)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestExecute_LargeFileDelete(t *testing.T) {
	// Test fallback with a deletion
	lines := make([]string, 5000)
	for i := range lines {
		lines[i] = fmt.Sprintf("%05d delete test line\n", i)
	}
	oldContent := strings.Join(lines, "")
	dir := t.TempDir()
	fp := filepath.Join(dir, "delete.txt")
	if err := os.WriteFile(fp, []byte(oldContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	firstLine := fmt.Sprintf("%05d delete test line", 0)
	input := json.RawMessage(`{"file_path":"`+fp+`","old_string":"`+firstLine+`","new_string":""}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}
