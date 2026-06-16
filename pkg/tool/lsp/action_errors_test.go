package lsptool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// TestSymbolsAction_NoExtension covers the early-return error when file
// has no extension.
func TestSymbolsAction_NoExtension(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	noext := filepath.Join(dir, "README")
	if err := os.WriteFile(noext, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "symbols", File: noext,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil || !strings.Contains(err.Error(), "no extension") {
		t.Fatalf("expected 'no extension' error, got: %v", err)
	}
}

// TestSymbolsAction_UnknownLanguage covers the langID="" branch.
func TestSymbolsAction_UnknownLanguage(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	// .foo extension is not in the language map.
	unknown := filepath.Join(dir, "weird.foo")
	if err := os.WriteFile(unknown, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "symbols", File: unknown,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil {
		t.Fatalf("expected unknown language error, got nil")
	}
}

// TestResolveAndOpen_NoServer covers the ForFile failure when no LSP server
// matches the file extension.
func TestResolveAndOpen_NoServer(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	src := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(src, []byte("package main\nfunc foo(){}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Use a .ts file — fakels only handles .go, so ForFile should fail.
	tsFile := filepath.Join(dir, "x.ts")
	if err := os.WriteFile(tsFile, []byte("function foo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "definition", Symbol: "foo", File: tsFile,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil {
		t.Fatalf("expected error for .ts with only-.go server, got nil")
	}
}

// TestSymbolsAction_NotFound covers the ForFile error when no server matches.
func TestSymbolsAction_NotFound(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	// .ts is not handled by fakels (only .go).
	tsFile := filepath.Join(dir, "x.ts")
	if err := os.WriteFile(tsFile, []byte("function foo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "symbols", File: tsFile,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil {
		t.Fatal("expected ForFile error for .ts with only-.go server")
	}
}

// TestSourceAction_NoSymbol covers the missing-symbol validation.
func TestSourceAction_NoSymbol(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	src := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(src, []byte("package main\nfunc foo(){}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: src, // no Symbol
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil || !strings.Contains(err.Error(), "symbol") {
		t.Fatalf("expected 'symbol' error, got: %v", err)
	}
}

// TestRequest_MissingQuery covers the empty-query/payload validation.
func TestRequest_MissingQuery(t *testing.T) {
	reg, _, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "request", // no Query, no Payload
	}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("expected 'query' error for empty request, got: %v", err)
	}
}

// TestResolvePath_Relative covers the relative-path join branch.
func TestResolvePath_Relative(t *testing.T) {
	abs := resolvePath("foo/bar.go", "/work")
	if abs != "/work/foo/bar.go" {
		t.Errorf("relative join: got %q, want /work/foo/bar.go", abs)
	}
}

// TestResolvePath_Absolute covers the already-absolute passthrough branch.
func TestResolvePath_Absolute(t *testing.T) {
	abs := resolvePath("/abs/path.go", "/work")
	if abs != "/abs/path.go" {
		t.Errorf("absolute passthrough: got %q, want /abs/path.go", abs)
	}
}

// TestEnumerateRenamePairs_Exceeded covers the >1000 files cap.
func TestEnumerateRenamePairs_Exceeded(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	// Create 1001 files — should hit the cap.
	for i := range maxRenamePairs + 1 {
		name := filepath.Join(src, "f"+padNum(i)+".go")
		if err := os.WriteFile(name, []byte("package x\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pairs, _, exceeded, err := enumerateRenamePairs(src, filepath.Join(dir, "dst"))
	if err != nil {
		t.Fatalf("enumerateRenamePairs: %v", err)
	}
	if !exceeded {
		t.Error("expected exceeded=true for >1000 files")
	}
	if len(pairs) != maxRenamePairs {
		t.Errorf("pairs = %d, want %d", len(pairs), maxRenamePairs)
	}
}

// padNum zero-pads an integer to a 5-digit string for filename uniqueness.
func padNum(n int) string {
	const width = 5
	s := ""
	for range width {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// TestEnumerateRenamePairs_EmptyDirectory covers the no-pairs outcome.
func TestEnumerateRenamePairs_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	pairs, isDir, exceeded, err := enumerateRenamePairs(src, filepath.Join(dir, "dst"))
	if err != nil {
		t.Fatalf("enumerateRenamePairs: %v", err)
	}
	if !isDir {
		t.Error("expected isDir=true")
	}
	if exceeded {
		t.Error("expected exceeded=false for empty dir")
	}
	if len(pairs) != 0 {
		t.Errorf("pairs = %d, want 0", len(pairs))
	}
}

// TestCollectRelevantRenameServers_NoMatch covers the no-server case.
func TestCollectRelevantRenameServers_NoMatch(t *testing.T) {
	specs := []lsp.ServerSpec{{Name: "gopls", FileExts: []string{".go"}}}
	pairs := []fileRenamePair{
		{oldURI: "file:///a.md", newURI: "file:///b.md"},
	}
	got := collectRelevantRenameServers(specs, "/a.md", "/b.md", pairs)
	if len(got) != 0 {
		t.Errorf("expected 0 relevant servers for .md, got %d", len(got))
	}
}

// TestCollectRelevantRenameServers_DestExtOnly covers the case where only
// the destination extension matches a server (e.g. cross-extension rename).
func TestCollectRelevantRenameServers_DestExtOnly(t *testing.T) {
	specs := []lsp.ServerSpec{
		{Name: "gopls", FileExts: []string{".go"}},
		{Name: "tsserver", FileExts: []string{".ts"}},
	}
	// Source is .md, dest is .ts — tsserver should match on dest.
	pairs := []fileRenamePair{
		{oldURI: "file:///a.md", newURI: "file:///b.ts"},
	}
	got := collectRelevantRenameServers(specs, "/a.md", "/b.ts", pairs)
	if len(got) != 1 {
		t.Fatalf("expected 1 server (tsserver), got %d", len(got))
	}
	if got[0].Name != "tsserver" {
		t.Errorf("got[0].Name = %q, want tsserver", got[0].Name)
	}
}
