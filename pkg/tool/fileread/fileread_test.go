package fileread_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/tool/fileread"
)

// ---------------------------------------------------------------------------
// New — tool metadata
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Parallel()

	tt := fileread.New()

	if tt.Name() != "FileRead" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "FileRead")
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

	tt := fileread.New()
	schema := tt.InputSchema()
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()

	tt := fileread.New()

	tests := []struct {
		name   string
		input  string
		want   string
	}{
		{"with path", `{"file_path":"/tmp/test.go"}`, "Read file: /tmp/test.go"},
		{"invalid json", `{invalid`, "Read a file from the filesystem"},
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

func TestExecute_ReadWholeFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(*fileread.Output)
	if !ok {
		t.Fatalf("Data type = %T, want *fileread.Output", result.Data)
	}
	if output.Content != content {
		t.Errorf("Content = %q, want %q", output.Content, content)
	}
	if output.Path != fp {
		t.Errorf("Path = %q, want %q", output.Path, fp)
	}
	if output.Lines != 3 {
		t.Errorf("Lines = %d, want 3", output.Lines)
	}
}

func TestExecute_ReadFileNoTrailingNewline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "noeol.txt")
	content := "line1\nline2\nline3"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileread.Output)
	if output.Lines != 3 {
		t.Errorf("Lines = %d, want 3", output.Lines)
	}
}

func TestExecute_ReadEmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileread.Output)
	if output.Content != "" {
		t.Errorf("Content = %q, want empty", output.Content)
	}
	if output.Lines != 0 {
		t.Errorf("Lines = %d, want 0", output.Lines)
	}
}

func TestExecute_ReadWithOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "offset.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","offset":3}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileread.Output)
	if output.Lines != 3 {
		t.Errorf("Lines = %d, want 3", output.Lines)
	}
	if !strings.Contains(output.Content, "line3") {
		t.Errorf("Content = %q, should contain 'line3'", output.Content)
	}
	if !strings.Contains(output.Content, "line5") {
		t.Errorf("Content = %q, should contain 'line5'", output.Content)
	}
	if strings.Contains(output.Content, "line1") {
		t.Errorf("Content = %q, should NOT contain 'line1'", output.Content)
	}
}

func TestExecute_ReadWithLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "limit.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","offset":2,"limit":2}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileread.Output)
	if output.Lines != 2 {
		t.Errorf("Lines = %d, want 2", output.Lines)
	}
	if !strings.Contains(output.Content, "line2") {
		t.Errorf("Content should contain 'line2'")
	}
	if !strings.Contains(output.Content, "line3") {
		t.Errorf("Content should contain 'line3'")
	}
	if strings.Contains(output.Content, "line1") {
		t.Errorf("Content should NOT contain 'line1'")
	}
	if strings.Contains(output.Content, "line4") {
		t.Errorf("Content should NOT contain 'line4'")
	}
}

func TestExecute_ReadWithZeroOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "zerooffset.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// offset=0 with limit set should treat offset as 1
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":0,"limit":2}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileread.Output)
	if output.Lines != 2 {
		t.Errorf("Lines = %d, want 2", output.Lines)
	}
}

// ---------------------------------------------------------------------------
// Execute — error paths
// ---------------------------------------------------------------------------

func TestExecute_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := fileread.Execute(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for invalid JSON")
	}
}

func TestExecute_EmptyFilePath(t *testing.T) {
	t.Parallel()

	_, err := fileread.Execute(context.Background(), json.RawMessage(`{"file_path":""}`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for empty file_path")
	}
	if !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("Error = %q, want 'file_path is required'", err.Error())
	}
}

func TestExecute_FileNotFound(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"file_path":"/nonexistent/file.txt"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing file")
	}
	if !strings.Contains(err.Error(), "file does not exist") {
		t.Errorf("Error = %q, want 'file does not exist'", err.Error())
	}
}

func TestExecute_Directory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	input := json.RawMessage(`{"file_path":"` + dir + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("Error = %q, want 'directory'", err.Error())
	}
}

func TestExecute_StatPermissionDenied(t *testing.T) {
	t.Parallel()
	// Create a directory without execute permission to trigger non-IsNotExist stat error
	dir := t.TempDir()
	restricted := filepath.Join(dir, "restricted")
	if err := os.MkdirAll(restricted, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(restricted, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	// Remove execute permission from parent directory
	if err := os.Chmod(restricted, 0000); err != nil {
		t.Skip("chmod not supported")
	}
	defer func() { _ = os.Chmod(restricted, 0755) }() // restore for cleanup

	input := json.RawMessage(`{"file_path":"` + target + `"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for permission denied")
	}
}

// ---------------------------------------------------------------------------
// Output JSON
// ---------------------------------------------------------------------------

func TestOutputJSON(t *testing.T) {
	t.Parallel()

	output := fileread.Output{
		Content: "hello\nworld",
		Path:    "/tmp/test.txt",
		Lines:   2,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got fileread.Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Content != output.Content {
		t.Errorf("Content = %q, want %q", got.Content, output.Content)
	}
	if got.Path != output.Path {
		t.Errorf("Path = %q, want %q", got.Path, output.Path)
	}
	if got.Lines != output.Lines {
		t.Errorf("Lines = %d, want %d", got.Lines, output.Lines)
	}
}
