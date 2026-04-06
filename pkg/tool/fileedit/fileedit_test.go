package fileedit_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/tool/fileedit"
)

// ---------------------------------------------------------------------------
// New — tool metadata
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Parallel()

	tt := fileedit.New()

	if tt.Name() != "FileEdit" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "FileEdit")
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
		{"with path", `{"file_path":"/tmp/test.go"}`, "Edit file: /tmp/test.go"},
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

// ---------------------------------------------------------------------------
// Execute — happy paths
// ---------------------------------------------------------------------------

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
	if !output.Success {
		t.Error("Success = false, want true")
	}
	if output.Path != fp {
		t.Errorf("Path = %q, want %q", output.Path, fp)
	}
	if output.Replacements != 1 {
		t.Errorf("Replacements = %d, want 1", output.Replacements)
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
	if output.Replacements != 3 {
		t.Errorf("Replacements = %d, want 3", output.Replacements)
	}

	data, _ := os.ReadFile(fp)
	if strings.Contains(string(data), "foo") {
		t.Errorf("File still contains 'foo': %q", string(data))
	}
}

func TestExecute_SameOldAndNew(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "same.txt")
	content := "hello\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"hello"}`)
	result, err := fileedit.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*fileedit.Output)
	if !output.Success {
		t.Error("Success = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Execute — error paths
// ---------------------------------------------------------------------------

func TestExecute_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := fileedit.Execute(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
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

func TestExecute_EmptyOldString(t *testing.T) {
	t.Parallel()

	_, err := fileedit.Execute(context.Background(), json.RawMessage(`{"file_path":"/tmp/test.txt","old_string":"","new_string":"b"}`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "old_string is required") {
		t.Errorf("Error = %q, want 'old_string is required'", err.Error())
	}
}

func TestExecute_FileNotFound(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"file_path":"/nonexistent/file.txt","old_string":"foo","new_string":"bar"}`)
	_, err := fileedit.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing file")
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
	if !strings.Contains(err.Error(), "not unique") {
		t.Errorf("Error = %q, want 'not unique'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Output JSON
// ---------------------------------------------------------------------------

func TestOutputJSON(t *testing.T) {
	t.Parallel()

	output := fileedit.Output{
		Success:      true,
		Path:         "/tmp/test.txt",
		Replacements: 2,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got fileedit.Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Success != true {
		t.Error("Success = false, want true")
	}
	if got.Path != "/tmp/test.txt" {
		t.Errorf("Path = %q, want /tmp/test.txt", got.Path)
	}
	if got.Replacements != 2 {
		t.Errorf("Replacements = %d, want 2", got.Replacements)
	}
}
