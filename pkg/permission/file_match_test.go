package permission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchFilePath(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		filePath string
		want     bool
		wantErr  bool
	}{
		{name: "simple glob", pattern: "*.json", filePath: "settings.json", want: true},
		{name: "glob no match", pattern: "*.json", filePath: "main.go", want: false},
		{name: "directory glob", pattern: "src/**/*.ts", filePath: "src/pkg/tool/bash.ts", want: true},
		{name: "dotfile pattern", pattern: ".gbot/*", filePath: ".gbot/settings.json", want: true},
		{name: "go files", pattern: "*.go", filePath: "tool.go", want: true},
		{name: "path traversal", pattern: "*.json", filePath: "../../etc/passwd", want: false, wantErr: true},
		{name: "absolute path with relative pattern", pattern: "*.json", filePath: "/absolute/path/evil.json", want: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := tt.pattern
			rule := Rule{
				Value:      RuleValue{ToolName: "Write", RuleContent: &content},
				ConfigRoot: "",
			}
			got, err := MatchFilePath(rule, tt.filePath)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("pattern %q should be rejected", tt.pattern)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchFilePathRootRelative(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	pattern := ".env"
	rule := Rule{
		Value:      RuleValue{ToolName: "Write", RuleContent: &pattern},
		ConfigRoot: subDir,
	}

	got, err := MatchFilePath(rule, ".env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected .env to match with ConfigRoot")
	}
}

func TestMatchFilePathSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	realFile := filepath.Join(tmpDir, "real.json")
	if err := os.WriteFile(realFile, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	linkFile := filepath.Join(tmpDir, "link.json")
	if err := os.Symlink(realFile, linkFile); err != nil {
		t.Fatal(err)
	}

	pattern := "*.json"
	rule := Rule{
		Value:      RuleValue{ToolName: "Write", RuleContent: &pattern},
		ConfigRoot: tmpDir,
	}

	got, err := MatchFilePath(rule, linkFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected symlink to match *.json pattern")
	}
}

func TestIsDangerousFilePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: ".bashrc", want: true},
		{path: ".gitconfig", want: true},
		{path: ".zshrc", want: true},
		{path: ".mcp.json", want: true},
		{path: ".BASHRC", want: true},
		{path: ".GitConfig", want: true},
		{path: ".git/HEAD", want: true},
		{path: ".vscode/settings.json", want: true},
		{path: ".claude/settings.json", want: true},
		{path: "main.go", want: false},
		{path: "settings.json", want: false},
		{path: "README.md", want: false},
		{path: ".env", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := IsDangerousFilePath(tt.path)
			if got != tt.want {
				t.Errorf("IsDangerousFilePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestValidateFilePattern(t *testing.T) {
	tests := []struct {
		pattern string
		wantErr bool
	}{
		{pattern: "*.json", wantErr: false},
		{pattern: "src/**/*.ts", wantErr: false},
		{pattern: ".env", wantErr: false},
		{pattern: "**", wantErr: false},
		{pattern: "[invalid", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			err := ValidateFilePattern(tt.pattern)
			if tt.wantErr && err == nil {
				t.Fatalf("pattern %q should be rejected", tt.pattern)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestContainsPathTraversal(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "../../etc/passwd", want: true},
		{path: "foo/../bar", want: true},
		{path: "normal/path", want: false},
		{path: "./relative", want: false},
		{path: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := containsPathTraversal(tt.path)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{input: "./foo/bar", want: "foo/bar"},
		{input: "foo/bar", want: "foo/bar"},
		{input: "", want: "."},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizePath(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchFilePathBareToolName(t *testing.T) {
	rule := Rule{
		Value: RuleValue{ToolName: "Write", RuleContent: nil},
	}
	got, err := MatchFilePath(rule, "anything.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("bare tool name (nil RuleContent) should match everything")
	}
}

func TestMatchFilePathNewFileParentWalk(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	pattern := "*.go"
	rule := Rule{
		Value:      RuleValue{ToolName: "Write", RuleContent: &pattern},
		ConfigRoot: subDir,
	}

	// Non-existent file triggers parent dir walk in resolvePath
	got, err := MatchFilePath(rule, "newfile.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected new file to match *.go pattern via parent walk")
	}
}

func TestMatchFilePathAbsolutePatternAbsolute(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.json")
	if err := os.WriteFile(testFile, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	pattern := filepath.Join(tmpDir, "*.json")
	rule := Rule{
		Value:      RuleValue{ToolName: "Write", RuleContent: &pattern},
		ConfigRoot: "",
	}

	got, err := MatchFilePath(rule, testFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("absolute pattern should match absolute path")
	}
}

func TestSplitPathSegmentsEmpty(t *testing.T) {
	got := splitPathSegments("")
	if len(got) != 0 {
		t.Errorf("expected empty segments for empty path, got %v", got)
	}
}

func TestMatchFilePathNoConfigRootPatternError(t *testing.T) {
	badContent := "[invalid"
	rule := Rule{
		Value:      RuleValue{ToolName: "Write", RuleContent: &badContent},
		ConfigRoot: "",
	}
	_, err := MatchFilePath(rule, "test.go")
	if err == nil {
		t.Fatal("expected error for invalid pattern without ConfigRoot")
	}
	if !strings.Contains(err.Error(), "pattern match error") {
		t.Errorf("expected pattern match error, got: %v", err)
	}
}
