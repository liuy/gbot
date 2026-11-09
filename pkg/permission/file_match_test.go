package permission

import (
	"errors"
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

func TestMatchFilePath_PatternsAndErrors(t *testing.T) {
	root := t.TempDir()
	rule := func(pattern string) Rule {
		p := pattern
		return Rule{Value: RuleValue{ToolName: "Read", RuleContent: &p}, Action: ActionDeny, ConfigRoot: root}
	}
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"src/**", "src/a/b.go", true},
		{"src/**", "other/a.go", false},
		{"*.go", "a.go", true},
		{"*.go", "src/a.go", false},
		{"secret.txt", "secret.txt", true},
		{"./secret.txt", "secret.txt", true},
	} {
		got, err := MatchFilePath(rule(tc.pattern), tc.path)
		if err != nil {
			t.Errorf("MatchFilePath(%q,%q): %v", tc.pattern, tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("MatchFilePath(%q,%q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
	if _, err := MatchFilePath(rule("src/**"), "../outside"); err == nil ||
		!strings.Contains(err.Error(), "..") {
		t.Errorf("traversal path error = %v, want a traversal rejection", err)
	}
	if ok, err := MatchFilePath(rule("["), "a.go"); err == nil ||
		!strings.HasPrefix(err.Error(), "pattern match error: ") {
		t.Errorf("bad pattern error = %v (matched=%v), want the pattern-match envelope", err, ok)
	}
}

func TestIsDangerousFilePath_Table(t *testing.T) {
	for _, p := range []string{".bashrc", "/home/u/.bashrc", ".git/config", "/x/.git/hooks/pre-push", ".mcp.json", ".vscode/settings.json", ".git"} {
		if !IsDangerousFilePath(p) {
			t.Errorf("IsDangerousFilePath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"main.go", "src/.gitignore", "README.md"} {
		if IsDangerousFilePath(p) {
			t.Errorf("IsDangerousFilePath(%q) = true, want false", p)
		}
	}
}

func TestValidateFilePattern_BadPattern(t *testing.T) {
	if err := ValidateFilePattern("src/**"); err != nil {
		t.Errorf("ValidateFilePattern(src/**) = %v, want nil", err)
	}
	err := ValidateFilePattern("[")
	if err == nil || !strings.HasPrefix(err.Error(), `invalid file pattern "[": `) {
		t.Errorf("ValidateFilePattern([) = %v, want the invalid-pattern envelope", err)
	}
	if !errors.Is(err, err) {
		t.Error("errors.Is broken for the pattern error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("pattern error %q does not wrap the matcher error", err.Error())
	}
}

func TestNormalizePathAndSegments(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"./a/b", "a/b"},
		{"a//b", "a/b"},
		{"/a/b", "/a/b"},
		{"", "."},
		// Clean must run before the "./" strip: otherwise a ".." or "."
		// segment survives and the matcher compares a different path.
		{"./a/../b", "b"},
		{"a/./b", "a/b"},
	} {
		if got := normalizePath(tc.in); got != tc.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := splitPathSegments(""); len(got) != 0 {
		t.Errorf("splitPathSegments(\"\") = %q, want empty", got)
	}
	if got := splitPathSegments("a/b"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitPathSegments(a/b) = %q, want [a b]", got)
	}
	// Backslash is a separator too (Windows-authored rules), and the leading
	// "/" must be dropped or every absolute path gains an empty first segment.
	if got := splitPathSegments(`/a\b`); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf(`splitPathSegments(\a\b) = %q, want [a b]`, got)
	}
	if got := splitPathSegments("/a/b"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitPathSegments(/a/b) = %q, want [a b] (no empty leading segment)", got)
	}
	if !containsPathTraversal("a/../b") || containsPathTraversal("a/b") {
		t.Error("containsPathTraversal misclassified")
	}
}

func TestIsDangerousFilePath_RelativeAndEmpty(t *testing.T) {
	for _, p := range []string{"./.bashrc", "./.git/config", ".//.bashrc"} {
		if !IsDangerousFilePath(p) {
			t.Errorf("IsDangerousFilePath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"", "/", "//"} {
		if IsDangerousFilePath(p) {
			t.Errorf("IsDangerousFilePath(%q) = true, want false", p)
		}
	}
}

func TestMatchFilePath_ResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(realDir, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	p := "real/*"
	rule := Rule{Value: RuleValue{ToolName: "Read", RuleContent: &p}, Action: ActionDeny, ConfigRoot: root}
	ok, err := MatchFilePath(rule, "link/a.go")
	if err != nil {
		t.Fatalf("MatchFilePath via symlink: %v", err)
	}
	if !ok {
		t.Error("a symlinked path did not resolve to the denied pattern")
	}
	// A path whose ancestors do not exist must still be matchable (fail-secure).
	ok, err = MatchFilePath(rule, "real/new-file.go")
	if err != nil {
		t.Fatalf("MatchFilePath for a new file: %v", err)
	}
	if !ok {
		t.Error("a not-yet-existing path under a denied pattern did not match")
	}
}

func TestMatchFilePath_PatternErrorWraps(t *testing.T) {
	p := "["
	rule := Rule{Value: RuleValue{ToolName: "Read", RuleContent: &p}, Action: ActionDeny, ConfigRoot: t.TempDir()}
	_, err := MatchFilePath(rule, "a.go")
	if err == nil {
		t.Fatal("a malformed pattern returned no error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("pattern error %q does not wrap the matcher error", err.Error())
	}
}
