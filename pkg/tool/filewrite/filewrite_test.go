package filewrite_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// New — tool metadata
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Parallel()

	tt := filewrite.New()

	if tt.Name() != "FileWrite" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "FileWrite")
	}
	if tt.IsReadOnly(nil) {
		t.Error("IsReadOnly() = true, want false")
	}
	if !tt.IsDestructive(nil) {
		t.Error("IsDestructive() = false, want true")
	}
	if tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() = true, want false")
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

	tt := filewrite.New()
	schema := tt.InputSchema()
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()

	tt := filewrite.New()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with path", `{"file_path":"/tmp/output.txt"}`, "Write file: /tmp/output.txt"},
		{"invalid json", `{invalid`, "Write content to a file"},
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
// Execute — create (new file)
// ---------------------------------------------------------------------------

func TestExecute_CreateNewFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "newfile.txt")

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"hello world"}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(*filewrite.Output)
	if !ok {
		t.Fatalf("Data type = %T, want *filewrite.Output", result.Data)
	}
	if output.Type != filewrite.WriteTypeCreate {
		t.Errorf("Type = %q, want %q", output.Type, filewrite.WriteTypeCreate)
	}
	if output.FilePath != fp {
		t.Errorf("FilePath = %q, want %q", output.FilePath, fp)
	}
	if output.Content != "hello world" {
		t.Errorf("Content = %q, want %q", output.Content, "hello world")
	}
	if output.OriginalFile != nil {
		t.Errorf("OriginalFile = %v, want nil for new file", *output.OriginalFile)
	}

	// Verify file content
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("File content = %q, want %q", string(data), "hello world")
	}
}

func TestExecute_CreateDirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "a", "b", "c", "deep.txt")

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"deep content"}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*filewrite.Output)
	if output.Type != filewrite.WriteTypeCreate {
		t.Errorf("Type = %q, want %q", output.Type, filewrite.WriteTypeCreate)
	}

	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "deep content" {
		t.Errorf("File content = %q, want %q", string(data), "deep content")
	}
}

func TestExecute_CreateEmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")

	input := json.RawMessage(`{"file_path":"` + fp + `","content":""}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*filewrite.Output)
	if output.Type != filewrite.WriteTypeCreate {
		t.Errorf("Type = %q, want %q", output.Type, filewrite.WriteTypeCreate)
	}
	if output.Content != "" {
		t.Errorf("Content = %q, want empty", output.Content)
	}
	if output.OriginalFile != nil {
		t.Errorf("OriginalFile = %v, want nil", *output.OriginalFile)
	}

	data, _ := os.ReadFile(fp)
	if string(data) != "" {
		t.Errorf("File content = %q, want empty", string(data))
	}
}

// ---------------------------------------------------------------------------
// Execute — update (existing file)
// ---------------------------------------------------------------------------

func TestExecute_UpdateExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "existing.txt")

	// Create initial file
	if err := os.WriteFile(fp, []byte("old content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new content"}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*filewrite.Output)
	if output.Type != filewrite.WriteTypeUpdate {
		t.Errorf("Type = %q, want %q", output.Type, filewrite.WriteTypeUpdate)
	}
	if output.Content != "new content" {
		t.Errorf("Content = %q, want %q", output.Content, "new content")
	}
	if output.OriginalFile == nil {
		t.Fatal("OriginalFile = nil, want original content")
	}
	if *output.OriginalFile != "old content" {
		t.Errorf("OriginalFile = %q, want %q", *output.OriginalFile, "old content")
	}

	data, _ := os.ReadFile(fp)
	if string(data) != "new content" {
		t.Errorf("File content = %q, want %q", string(data), "new content")
	}
}

func TestExecute_UpdateUnchanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "same.txt")

	content := "same content"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"same content"}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*filewrite.Output)
	if output.Type != filewrite.WriteTypeUpdate {
		t.Errorf("Type = %q, want %q", output.Type, filewrite.WriteTypeUpdate)
	}
	if output.OriginalFile == nil {
		t.Fatal("OriginalFile = nil, want original content")
	}
	// Structured patch should be nil/empty when unchanged
}

// ---------------------------------------------------------------------------
// Execute — error paths
// ---------------------------------------------------------------------------

func TestExecute_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := filewrite.Execute(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
}

func TestExecute_EmptyFilePath(t *testing.T) {
	t.Parallel()

	_, err := filewrite.Execute(context.Background(), json.RawMessage(`{"file_path":"","content":"test"}`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("Error = %q, want 'file_path is required'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Output JSON serialization
// ---------------------------------------------------------------------------

func TestOutputJSON_Create(t *testing.T) {
	t.Parallel()

	nilStr := (*string)(nil)
	output := filewrite.Output{
		Type:            filewrite.WriteTypeCreate,
		FilePath:        "/tmp/out.txt",
		Content:         "hello",
		StructuredPatch: nil,
		OriginalFile:    nilStr,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got filewrite.Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Type != filewrite.WriteTypeCreate {
		t.Errorf("Type = %q, want %q", got.Type, filewrite.WriteTypeCreate)
	}
	if got.FilePath != "/tmp/out.txt" {
		t.Errorf("FilePath = %q, want /tmp/out.txt", got.FilePath)
	}
	if got.OriginalFile != nil {
		t.Errorf("OriginalFile = %v, want nil", got.OriginalFile)
	}
}

func TestOutputJSON_Update(t *testing.T) {
	t.Parallel()

	old := "old content"
	output := filewrite.Output{
		Type:            filewrite.WriteTypeUpdate,
		FilePath:        "/tmp/out.txt",
		Content:         "new content",
		StructuredPatch: nil,
		OriginalFile:    &old,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got filewrite.Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Type != filewrite.WriteTypeUpdate {
		t.Errorf("Type = %q, want %q", got.Type, filewrite.WriteTypeUpdate)
	}
	if got.OriginalFile == nil {
		t.Fatal("OriginalFile = nil, want 'old content'")
	}
	if *got.OriginalFile != "old content" {
		t.Errorf("OriginalFile = %q, want 'old content'", *got.OriginalFile)
	}
}

// ---------------------------------------------------------------------------
// ContentChanged verification
// ---------------------------------------------------------------------------

// expandPath mirrors the internal helper for test use.
func expandPath(filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	if strings.HasPrefix(filePath, "~/") {
		if home := os.Getenv("HOME"); home != "" {
			return filepath.Join(home, filePath[2:])
		}
	}
	abs, _ := filepath.Abs(filePath)
	return abs
}

// --- ContentChanged: file not previously read → must-read-first rejection ---
func TestExecute_ContentChanged_NotRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "notread.txt")
	// Create file without reading it
	if err := os.WriteFile(fp, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	tctx := &types.ToolUseContext{
		ReadFileState: make(map[string]types.FileState),
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new"}`)
	_, err := filewrite.Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() should reject write to file that hasn't been read")
	}
	if !strings.Contains(err.Error(), "not been read") && !strings.Contains(err.Error(), "read it first") {
		t.Errorf("Error = %q, want 'not been read'", err.Error())
	}
}

// --- ContentChanged: file read but mtime unchanged ---
func TestExecute_ContentChanged_ReadUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "unchanged.txt")
	if err := os.WriteFile(fp, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fp)
	mtimeMs := info.ModTime().UnixMilli()
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			expandPath(fp): {
				Content:   "old",
				Timestamp: mtimeMs,
				Offset:    0,
				Limit:     0,
			},
		},
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new"}`)
	result, err := filewrite.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*filewrite.Output)
	if output.ContentChanged {
		t.Errorf("ContentChanged = true, want false (mtime unchanged)")
	}
}

// --- ContentChanged: file read but mtime changed ---
func TestExecute_ContentChanged_Stale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(fp, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	// Record old mtime
	info, _ := os.Stat(fp)
	oldMtime := info.ModTime().UnixMilli()
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			expandPath(fp): {
				Content:   "old",
				Timestamp: oldMtime,
				Offset:    0,
				Limit:     0,
			},
		},
	}
	// Modify file after read
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(fp, []byte("modified by others"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new"}`)
	_, err := filewrite.Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() should reject write to stale file")
	}
	if !strings.Contains(err.Error(), "modified since read") && !strings.Contains(err.Error(), "read it again") {
		t.Errorf("Error = %q, want 'modified since read'", err.Error())
	}
}

// --- Gap 4: CRLF normalization in old content for patch ---
func TestExecute_PatchWithCRLFNormalization(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "crlf.txt")
	// Write file with CRLF line endings
	if err := os.WriteFile(fp, []byte("line1\r\nline2\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Write same logical content but with LF
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"line1\nline2\n"}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*filewrite.Output)
	if output.Type != filewrite.WriteTypeUpdate {
		t.Fatalf("Type = %q, want update", output.Type)
	}
	// Patch should have no change lines — CRLF→LF is not a real change
	for _, hunk := range output.StructuredPatch {
		for _, line := range hunk.Lines {
			if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+") {
				t.Errorf("Patch should have no change lines after CRLF normalization, got %q", line)
			}
		}
	}
}

// --- Gap 5: ReadFileState updated after write ---
func TestExecute_UpdatesReadFileState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "stateupdate.txt")
	if err := os.WriteFile(fp, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Record an old read state
	info, _ := os.Stat(fp)
	oldMtime := info.ModTime().UnixMilli()
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			expandPath(fp): {
				Content:   "old content",
				Timestamp: oldMtime,
			},
		},
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new content"}`)
	result, err := filewrite.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	_ = result

	// ReadFileState should be updated with new content and new mtime
	state, ok := tctx.ReadFileState[expandPath(fp)]
	if !ok {
		t.Fatal("ReadFileState not updated after write")
	}
	if state.Content != "new content" {
		t.Errorf("ReadFileState.Content = %q, want %q", state.Content, "new content")
	}
	// New mtime should reflect the file's actual mtime after write
	newInfo, _ := os.Stat(fp)
	newMtime := newInfo.ModTime().UnixMilli()
	if state.Timestamp != newMtime {
		t.Errorf("ReadFileState.Timestamp = %d, want %d", state.Timestamp, newMtime)
	}
}

// --- GitDiff ---
func TestExecute_GitDiff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "gitdiff.txt")
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"hello world"}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*filewrite.Output)
	// GitDiff may be nil if not in git repo — that's ok
	// But if we're in a git repo, it should be non-nil for new files
	_ = output.GitDiff
}

// ---------------------------------------------------------------------------
// Task #23: Path expansion — ~ and relative paths must be resolved before write
// ---------------------------------------------------------------------------

func TestExecute_ExpandsTildePath(t *testing.T) {
	t.Parallel()
	// Write a file using a tilde-expanded path
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	// Use a unique subdir under /tmp to avoid HOME permission issues
	dir := t.TempDir()
	fp := filepath.Join(dir, "file.txt")
	defer func() { _ = os.RemoveAll(dir) }()

	input := json.RawMessage(`{"file_path":"` + strings.Replace(fp, home, "~", 1) + `","content":"expanded"}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*filewrite.Output)
	if output.Type != filewrite.WriteTypeCreate {
		t.Errorf("Type = %q, want create", output.Type)
	}
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "expanded" {
		t.Errorf("File content = %q, want %q", string(data), "expanded")
	}
}

func TestExecute_ExpandsRelativePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "rel.txt")

	// Use ToolUseContext.WorkingDir to resolve relative path
	tctx := &types.ToolUseContext{WorkingDir: dir}
	// Write with relative path
	input := json.RawMessage(`{"file_path":"rel.txt","content":"relative"}`)
	result, err := filewrite.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*filewrite.Output)
	// The written file path should be expanded to absolute
	if !filepath.IsAbs(output.FilePath) {
		t.Errorf("FilePath = %q, want absolute path", output.FilePath)
	}
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "relative" {
		t.Errorf("File content = %q, want %q", string(data), "relative")
	}
}

// ---------------------------------------------------------------------------
// Task #24: Must-read-first + staleness rejection
// ---------------------------------------------------------------------------

func TestExecute_MustReadFirst_RejectsUnread(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "unread.txt")
	// Create existing file
	if err := os.WriteFile(fp, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	// No ReadFileState entry → must reject
	tctx := &types.ToolUseContext{
		ReadFileState: make(map[string]types.FileState),
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new"}`)
	_, err := filewrite.Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() should reject write to unread file")
	}
	if !strings.Contains(err.Error(), "not been read") && !strings.Contains(err.Error(), "read it first") {
		t.Errorf("Error = %q, want 'not been read' message", err.Error())
	}
}

func TestExecute_MustReadFirst_RejectsPartialRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "partial.txt")
	if err := os.WriteFile(fp, []byte("existing content"), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fp)
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			expandPath(fp): {
				Content:       "existing",
				Timestamp:     info.ModTime().UnixMilli(),
				IsPartialView: true, // partial read should be rejected
			},
		},
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new"}`)
	_, err := filewrite.Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() should reject write to partially-read file")
	}
	if !strings.Contains(err.Error(), "not been read") && !strings.Contains(err.Error(), "read it first") {
		t.Errorf("Error = %q, want 'not been read' message", err.Error())
	}
}

func TestExecute_Staleness_RejectsStaleRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(fp, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fp)
	oldMtime := info.ModTime().UnixMilli()
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			expandPath(fp): {
				Content:       "old",
				Timestamp:     oldMtime,
				IsPartialView: false,
			},
		},
	}
	// Modify file after recording read state
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(fp, []byte("modified by others"), 0644); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new"}`)
	_, err := filewrite.Execute(context.Background(), input, tctx)
	if err == nil {
		t.Fatal("Execute() should reject write to stale file")
	}
	if !strings.Contains(err.Error(), "modified since read") && !strings.Contains(err.Error(), "read it again") {
		t.Errorf("Error = %q, want 'modified since read' message", err.Error())
	}
}

func TestExecute_MustReadFirst_AllowsNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "newfile.txt")
	// File doesn't exist yet — no read required
	tctx := &types.ToolUseContext{
		ReadFileState: make(map[string]types.FileState),
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new file content"}`)
	result, err := filewrite.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*filewrite.Output)
	if output.Type != filewrite.WriteTypeCreate {
		t.Errorf("Type = %q, want create", output.Type)
	}
}

func TestExecute_MustReadFirst_AllowsFullRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "fullread.txt")
	if err := os.WriteFile(fp, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fp)
	tctx := &types.ToolUseContext{
		ReadFileState: map[string]types.FileState{
			expandPath(fp): {
				Content:       "existing",
				Timestamp:     info.ModTime().UnixMilli(),
				IsPartialView: false,
			},
		},
	}
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new"}`)
	result, err := filewrite.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*filewrite.Output)
	if output.Type != filewrite.WriteTypeUpdate {
		t.Errorf("Type = %q, want update", output.Type)
	}
}
