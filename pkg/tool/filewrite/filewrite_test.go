package filewrite_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/tool/filewrite"
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
// Execute — happy paths
// ---------------------------------------------------------------------------

func TestExecute_WriteNewFile(t *testing.T) {
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
	if !output.Success {
		t.Error("Success = false, want true")
	}
	if output.Path != fp {
		t.Errorf("Path = %q, want %q", output.Path, fp)
	}
	if output.BytesWritten != len("hello world") {
		t.Errorf("BytesWritten = %d, want %d", output.BytesWritten, len("hello world"))
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
	if !output.Success {
		t.Error("Success = false, want true")
	}

	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "deep content" {
		t.Errorf("File content = %q, want %q", string(data), "deep content")
	}
}

func TestExecute_OverwriteExisting(t *testing.T) {
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
	if !output.Success {
		t.Error("Success = false, want true")
	}

	data, _ := os.ReadFile(fp)
	if string(data) != "new content" {
		t.Errorf("File content = %q, want %q", string(data), "new content")
	}
}

func TestExecute_WriteEmptyContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")

	input := json.RawMessage(`{"file_path":"` + fp + `","content":""}`)
	result, err := filewrite.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*filewrite.Output)
	if !output.Success {
		t.Error("Success = false, want true")
	}
	if output.BytesWritten != 0 {
		t.Errorf("BytesWritten = %d, want 0", output.BytesWritten)
	}

	data, _ := os.ReadFile(fp)
	if string(data) != "" {
		t.Errorf("File content = %q, want empty", string(data))
	}
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
// Output JSON
// ---------------------------------------------------------------------------

func TestOutputJSON(t *testing.T) {
	t.Parallel()

	output := filewrite.Output{
		Success:      true,
		Path:         "/tmp/out.txt",
		BytesWritten: 42,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got filewrite.Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Success != true {
		t.Error("Success = false, want true")
	}
	if got.Path != "/tmp/out.txt" {
		t.Errorf("Path = %q, want /tmp/out.txt", got.Path)
	}
	if got.BytesWritten != 42 {
		t.Errorf("BytesWritten = %d, want 42", got.BytesWritten)
	}
}
