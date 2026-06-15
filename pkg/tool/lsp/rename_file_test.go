package lsptool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

// --- Unit tests for pure functions ---

func TestPluralS(t *testing.T) {
	if got := pluralS(1); got != "" {
		t.Errorf("pluralS(1) = %q, want empty", got)
	}
	if got := pluralS(0); got != "s" {
		t.Errorf("pluralS(0) = %q, want \"s\"", got)
	}
	if got := pluralS(2); got != "s" {
		t.Errorf("pluralS(2) = %q, want \"s\"", got)
	}
}

func TestIsMethodNotFoundError(t *testing.T) {
	cases := map[string]bool{
		"method not found":                      true,
		"Server returned: unhandled method foo": true,
		"not supported on this server":          true,
		"jsonrpc -32601 went away":              true,
		"network timeout":                       false,
		"":                                      false,
	}
	for msg, want := range cases {
		err := &simpleError{msg}
		if got := isMethodNotFoundError(err); got != want {
			t.Errorf("isMethodNotFoundError(%q) = %v, want %v", msg, got, want)
		}
	}
}

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

func TestEnumerateRenamePairs_SingleFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "bar.go")

	pairs, isDir, exceeded, err := enumerateRenamePairs(src, dst)
	if err != nil {
		t.Fatalf("enumerateRenamePairs: %v", err)
	}
	if isDir {
		t.Error("isDir = true, want false for single file")
	}
	if exceeded {
		t.Error("exceeded = true")
	}
	if len(pairs) != 1 {
		t.Fatalf("len(pairs) = %d, want 1", len(pairs))
	}
	wantOld := lsp.FileToURI(src)
	wantNew := lsp.FileToURI(dst)
	if pairs[0].oldURI != wantOld {
		t.Errorf("oldURI = %q, want %q", pairs[0].oldURI, wantOld)
	}
	if pairs[0].newURI != wantNew {
		t.Errorf("newURI = %q, want %q", pairs[0].newURI, wantNew)
	}
}

func TestEnumerateRenamePairs_Directory(t *testing.T) {
	dir := t.TempDir()
	// Create a nested structure: src/a.go, src/sub/b.go
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.go"), []byte("package src\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.go"), []byte("package sub\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")

	pairs, isDir, exceeded, err := enumerateRenamePairs(src, dst)
	if err != nil {
		t.Fatalf("enumerateRenamePairs: %v", err)
	}
	if !isDir {
		t.Error("isDir = false, want true for directory")
	}
	if exceeded {
		t.Error("exceeded = true")
	}
	if len(pairs) != 2 {
		t.Fatalf("len(pairs) = %d, want 2", len(pairs))
	}
	// Each new URI should be anchored at dst.
	for _, p := range pairs {
		if !strings.HasPrefix(lsp.URItoPath(p.newURI), dst) {
			t.Errorf("newURI %q doesn't start with dst %q", p.newURI, dst)
		}
	}
}

func TestEnumerateRenamePairs_NonExistent(t *testing.T) {
	dir := t.TempDir()
	_, _, _, err := enumerateRenamePairs(filepath.Join(dir, "nope"), filepath.Join(dir, "dest"))
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("enumerateRenamePairs err=%v, want mention of 'nope'", err)
	}
}

func TestCollectRelevantRenameServers(t *testing.T) {
	specs := []lsp.ServerSpec{
		{Name: "gopls", FileExts: []string{".go"}},
		{Name: "tsserver", FileExts: []string{".ts", ".tsx"}},
		{Name: "rust-analyzer", FileExts: []string{".rs"}},
	}
	// Rename a .go file: only gopls should be relevant.
	pairs := []fileRenamePair{
		{oldURI: "file:///src/a.go", newURI: "file:///dst/b.go"},
	}
	got := collectRelevantRenameServers(specs, "/src/a.go", "/dst/b.go", pairs)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Name != "gopls" {
		t.Errorf("got[0].Name = %q, want gopls", got[0].Name)
	}
}

func TestPairMaps(t *testing.T) {
	pairs := []fileRenamePair{
		{oldURI: "file:///a", newURI: "file:///b"},
		{oldURI: "file:///c", newURI: "file:///d"},
	}
	got := pairMaps(pairs)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0]["oldUri"] != "file:///a" || got[0]["newUri"] != "file:///b" {
		t.Errorf("got[0] = %v", got[0])
	}
	if got[1]["oldUri"] != "file:///c" || got[1]["newUri"] != "file:///d" {
		t.Errorf("got[1] = %v", got[1])
	}
}

// --- Execute-level failure path tests ---

func TestRenameFile_MissingParams(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})

	// Missing file → error.
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", NewName: "/tmp/dest.go",
	}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "requires both") {
		t.Fatalf("expected requires-both error, got %v", err)
	}

	// Missing new_name → error.
	_, err = New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: "/tmp/src.go",
	}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "requires both") {
		t.Fatalf("expected requires-both error, got %v", err)
	}
}

func TestRenameFile_SourceEqualsDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})

	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: src,
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("expected identical error, got %v", err)
	}
}

func TestRenameFile_SourceMissing(t *testing.T) {
	dir := t.TempDir()
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})

	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: filepath.Join(dir, "nope.go"), NewName: filepath.Join(dir, "dest.go"),
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected does-not-exist error, got %v", err)
	}
}

func TestRenameFile_DestExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.go")
	dst := filepath.Join(dir, "b.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})

	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}

func TestRenameFile_NoServer_PhysicalRename(t *testing.T) {
	// When no LSP server matches the file extension, rename_file should still
	// physically rename the file and report "(no LSP server for these files...)".
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.md")
	dst := filepath.Join(dir, "renamed.md")
	if err := os.WriteFile(src, []byte("# hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Configure a server that only handles .go — .md should fall through.
	reg := lsp.NewRegistry(dir)
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})

	result, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	got := result.Data.(string)
	if !strings.Contains(got, "physical rename only") {
		t.Errorf("result = %q, want 'physical rename only'", got)
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("source still exists after rename")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Error("destination missing after rename")
	}
}
