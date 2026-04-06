package glob_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/tool/glob"
	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// New — tool metadata
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Parallel()

	tt := glob.New()

	if tt.Name() != "Glob" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "Glob")
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

	tt := glob.New()
	schema := tt.InputSchema()
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}
}

func TestDescription(t *testing.T) {
	t.Parallel()

	tt := glob.New()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with pattern", `{"pattern":"**/*.go"}`, "Glob: **/*.go"},
		{"invalid json", `{invalid`, "Find files matching a glob pattern"},
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

	input := json.RawMessage(`{"pattern":"*.go","path":"` + dir + `"}`)
	result, err := glob.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(*glob.Output)
	if !ok {
		t.Fatalf("Data type = %T, want *glob.Output", result.Data)
	}
	if output.Count != 2 {
		t.Errorf("Count = %d, want 2", output.Count)
	}

	// Results should be sorted
	expected := []string{"main.go", "util.go"}
	sort.Strings(expected)
	for i, e := range expected {
		if output.Files[i] != e {
			t.Errorf("Files[%d] = %q, want %q", i, output.Files[i], e)
		}
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

	input := json.RawMessage(`{"pattern":"**/*.go","path":"` + dir + `"}`)
	result, err := glob.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*glob.Output)
	if output.Count != 2 {
		t.Errorf("Count = %d, want 2 (files: %v)", output.Count, output.Files)
	}
}

func TestExecute_NoMatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"*.go","path":"` + dir + `"}`)
	result, err := glob.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*glob.Output)
	if output.Count != 0 {
		t.Errorf("Count = %d, want 0", output.Count)
	}
	if len(output.Files) != 0 {
		t.Errorf("Files = %v, want empty", output.Files)
	}
}

func TestExecute_WorkingDirFromContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tctx := &types.ToolUseContext{WorkingDir: dir}
	input := json.RawMessage(`{"pattern":"*.txt"}`)
	result, err := glob.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*glob.Output)
	if output.Count != 1 {
		t.Errorf("Count = %d, want 1", output.Count)
	}
}

// ---------------------------------------------------------------------------
// Execute — error paths
// ---------------------------------------------------------------------------

func TestExecute_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := glob.Execute(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
}

func TestExecute_EmptyPattern(t *testing.T) {
	t.Parallel()

	_, err := glob.Execute(context.Background(), json.RawMessage(`{"pattern":""}`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
}

func TestExecute_PathNotFound(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"pattern":"*.go","path":"/nonexistent/path"}`)
	_, err := glob.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for nonexistent path")
	}
}

func TestExecute_PathIsFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"pattern":"*.go","path":"` + fp + `"}`)
	_, err := glob.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error when path is a file")
	}
}

// ---------------------------------------------------------------------------
// Output JSON
// ---------------------------------------------------------------------------

func TestOutputJSON(t *testing.T) {
	t.Parallel()

	output := glob.Output{
		Files: []string{"a.go", "b.go"},
		Count: 2,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got glob.Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Count != 2 {
		t.Errorf("Count = %d, want 2", got.Count)
	}
	if len(got.Files) != 2 {
		t.Fatalf("Files length = %d, want 2", len(got.Files))
	}
}
