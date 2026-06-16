package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileToURI(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"/tmp/x.go", "file:///tmp/x.go"},
		{"/home/user/foo bar.go", "file:///home/user/foo%20bar.go"},
		{"/x#hash.go", "file:///x%23hash.go"},
	}
	for _, tc := range cases {
		got := FileToURI(tc.path)
		if got != tc.want {
			t.Errorf("FileToURI(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestURItoPath(t *testing.T) {
	got := URItoPath("file:///tmp/x.go")
	if got != "/tmp/x.go" {
		t.Errorf("URItoPath = %q, want /tmp/x.go", got)
	}
}

func TestURItoRelativePath(t *testing.T) {
	wd := "/home/user/project"
	absPath := wd + "/sub/file.go"
	uri := FileToURI(absPath)

	got := URItoRelativePath(uri, wd)
	if got != "sub/file.go" {
		t.Errorf("URItoRelativePath relative = %q, want sub/file.go", got)
	}

	// Outside cwd: returns the absolute path (no .. prefix).
	otherURI := FileToURI("/etc/passwd")
	got = URItoRelativePath(otherURI, wd)
	if got != "/etc/passwd" {
		t.Errorf("URItoRelativePath outside = %q, want /etc/passwd", got)
	}
}

func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"x.go", "go"},
		{"x.ts", "typescript"},
		{"x.tsx", "typescript"},
		{"x.js", "javascript"},
		{"x.mjs", "javascript"},
		{"x.cjs", "javascript"},
		{"x.py", "python"},
		{"x.rs", "rust"},
		{"x.rb", "ruby"},
		{"x.java", "java"},
		{"x.c", "c"},
		{"x.h", "c"},
		{"x.cpp", "cpp"},
		{"x.cc", "cpp"},
		{"x.cxx", "cpp"},
		{"x.hpp", "cpp"},
		{"x.hh", "cpp"},
		{"x.cs", "csharp"},
		{"x.swift", "swift"},
		{"x.kt", "kotlin"},
		{"x.kts", "kotlin"},
		{"x.scala", "scala"},
		{"x.php", "php"},
		{"x.css", "css"},
		{"x.scss", "css"},
		{"x.less", "css"},
		{"x.html", "html"},
		{"x.htm", "html"},
		{"x.json", "json"},
		{"x.xml", "xml"},
		{"x.yaml", "yaml"},
		{"x.yml", "yaml"},
		{"x.md", "markdown"},
		{"x.sql", "sql"},
		{"x.sh", "shellscript"},
		{"x.bash", "shellscript"},
		{"x.toml", "toml"},
		{"x.unknownext", ""},
		{"noext", ""},
	}
	for _, tc := range cases {
		got := DetectLanguage(tc.path)
		if got != tc.want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestFormatWorkspaceEdit_LegacyChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.go")
	uri := FileToURI(path)
	edit := &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			uri: {
				{Range: Range{}, NewText: "x"},
				{Range: Range{}, NewText: "y"},
			},
		},
	}
	out := FormatWorkspaceEdit(edit, dir)
	if len(out) != 1 {
		t.Fatalf("FormatWorkspaceEdit = %v, want 1 line", out)
	}
	if !strings.Contains(out[0], "foo.go") || !strings.Contains(out[0], "2 edits") {
		t.Errorf("FormatWorkspaceEdit[0] = %q, want foo.go + 2 edits", out[0])
	}
}

func TestFormatWorkspaceEdit_DocumentChanges(t *testing.T) {
	dir := t.TempDir()
	uri := FileToURI(filepath.Join(dir, "foo.go"))

	dc := map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": 1},
		"edits": []map[string]any{
			{"range": map[string]any{}, "newText": "x"},
		},
	}
	createURI := FileToURI(filepath.Join(dir, "new.go"))
	createOp := map[string]any{"kind": "create", "uri": createURI}
	renameOp := map[string]any{
		"kind":   "rename",
		"oldUri": FileToURI(filepath.Join(dir, "old.go")),
		"newUri": FileToURI(filepath.Join(dir, "new.go")),
	}
	deleteOp := map[string]any{"kind": "delete", "uri": createURI}

	edit := &WorkspaceEdit{
		DocumentChanges: []map[string]any{dc, createOp, renameOp, deleteOp},
	}
	out := FormatWorkspaceEdit(edit, dir)
	if len(out) != 4 {
		t.Fatalf("FormatWorkspaceEdit = %v, want 4 lines", out)
	}
	if !strings.Contains(out[1], "CREATE") || !strings.Contains(out[1], "new.go") {
		t.Errorf("create line = %q", out[1])
	}
	if !strings.Contains(out[2], "RENAME") {
		t.Errorf("rename line = %q", out[2])
	}
	if !strings.Contains(out[3], "DELETE") {
		t.Errorf("delete line = %q", out[3])
	}
}

func TestFormatWorkspaceEdit_Nil(t *testing.T) {
	if out := FormatWorkspaceEdit(nil, "/tmp"); out != nil {
		t.Errorf("FormatWorkspaceEdit(nil) = %v, want nil", out)
	}
}

func TestFormatLocation(t *testing.T) {
	loc := Location{
		URI:   FileToURI("/repo/foo.go"),
		Range: Range{Start: Position{Line: 4, Character: 2}, End: Position{Line: 4, Character: 5}},
	}
	got := FormatLocation(loc, "/repo")
	if !strings.Contains(got, "foo.go:5:3") {
		t.Errorf("FormatLocation = %q, want foo.go:5:3", got)
	}
}

func TestReadLocationContext_FileNotFound(t *testing.T) {
	out := ReadLocationContext("/nonexistent/file.go", 1, 3)
	if out != nil {
		t.Errorf("ReadLocationContext on missing file = %v, want nil", out)
	}
}

func TestReadLocationContext_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	content := "line0\nline1\nline2\nline3\nline4\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	out := ReadLocationContext(path, 3, 1)
	if len(out) == 0 {
		t.Fatal("expected non-empty context")
	}
	// line 3 (1-based), n=1: should include lines 2,3,4.
	if len(out) != 3 {
		t.Errorf("got %d context lines, want 3", len(out))
	}
	if !strings.Contains(out[0], "line1") {
		t.Errorf("first context line = %q", out[0])
	}
	if !strings.Contains(out[1], "line2") {
		t.Errorf("middle context line = %q", out[1])
	}
	if !strings.Contains(out[2], "line3") {
		t.Errorf("last context line = %q", out[2])
	}
}

func TestFormatLocationWithContext_NoContext(t *testing.T) {
	loc := Location{
		URI:   FileToURI("/tmp/x.go"),
		Range: Range{Start: Position{Line: 0, Character: 0}},
	}
	got := FormatLocationWithContext(loc, "/tmp", 0)
	// contextLines <= 0 falls back to FormatLocation.
	if !strings.HasSuffix(got, ":1:1") {
		t.Errorf("FormatLocationWithContext(0) = %q", got)
	}
}

func TestFormatLocationWithContext_FileMissing(t *testing.T) {
	loc := Location{
		URI:   FileToURI("/nonexistent/x.go"),
		Range: Range{Start: Position{Line: 0, Character: 0}},
	}
	got := FormatLocationWithContext(loc, "/tmp", 2)
	if !strings.Contains(got, "x.go:1:1") {
		t.Errorf("FormatLocationWithContext missing file = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("FormatLocationWithContext with missing file should have no context lines: %q", got)
	}
}

func TestFormatLocationWithContext_WithFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	content := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	loc := Location{
		URI:   FileToURI(path),
		Range: Range{Start: Position{Line: 2, Character: 0}},
	}
	got := FormatLocationWithContext(loc, dir, 1)
	if !strings.Contains(got, "x.go:3:1") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "beta") || !strings.Contains(got, "gamma") || !strings.Contains(got, "delta") {
		t.Errorf("missing context lines in: %q", got)
	}
}

func TestFormatLocationsWithContext_Empty(t *testing.T) {
	out := FormatLocationsWithContext(nil, "/tmp", 1)
	if len(out) != 0 {
		t.Errorf("FormatLocationsWithContext(nil) = %v, want empty", out)
	}
}

func TestFormatLocationsWithContext_NegativeContext(t *testing.T) {
	locs := []Location{
		{URI: FileToURI("/tmp/x.go"), Range: Range{Start: Position{Line: 0, Character: 0}}},
	}
	out := FormatLocationsWithContext(locs, "/tmp", -1)
	if len(out) != 1 {
		t.Fatalf("got %d lines, want 1", len(out))
	}
	if !strings.HasSuffix(out[0], "x.go:1:1") {
		t.Errorf("out[0] = %q", out[0])
	}
}

func TestFormatLocationsWithContext_Grouped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	content := "a\nb\nc\nd\ne\nf\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	uri := FileToURI(path)
	locs := []Location{
		{URI: uri, Range: Range{Start: Position{Line: 1, Character: 0}}},
		{URI: uri, Range: Range{Start: Position{Line: 3, Character: 0}}},
	}
	out := FormatLocationsWithContext(locs, dir, 1)
	if len(out) != 2 {
		t.Fatalf("got %d outputs, want 2", len(out))
	}
	if !strings.Contains(out[0], "x.go:2:1") || !strings.Contains(out[0], "a") || !strings.Contains(out[0], "b") {
		t.Errorf("out[0] = %q", out[0])
	}
	if !strings.Contains(out[1], "x.go:4:1") || !strings.Contains(out[1], "c") || !strings.Contains(out[1], "d") {
		t.Errorf("out[1] = %q", out[1])
	}
}
