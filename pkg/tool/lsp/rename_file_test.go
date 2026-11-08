package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	// nil error must return false without panicking.
	if isMethodNotFoundError(nil) {
		t.Error("isMethodNotFoundError(nil) = true, want false")
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

// TestRenameFile_NoServers_PhysicalRename verifies that when no LSP servers
// are configured at all, rename_file still physically moves the file.
func TestRenameFile_NoServers_PhysicalRename(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\nfunc foo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	reg := lsp.NewRegistry(dir)
	tt := New(reg)
	_, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination file not created: %v", err)
	}
}

// TestIntegration_RenameFile_WithEdits covers the willRenameFiles round-trip:
// server returns a WorkspaceEdit, renameFile applies it to disk, then performs
// the physical rename. Verifies the apply-mode happy path.
func TestIntegration_RenameFile_WithEdits(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "workspace/willRenameFiles":
				// In a real rename, gopls would update import paths in
				// referencing files. Simulate by editing another.go: replace
				// "oldname" with "newname".
				otherURI := lsp.FileToURI(filepath.Join(d, "other.go"))
				return map[string]any{
					"changes": map[string][]map[string]any{
						otherURI: {
							{
								"range": map[string]any{
									"start": map[string]any{"line": 0, "character": 0},
									"end":   map[string]any{"line": 0, "character": 7},
								},
								"newText": "newname",
							},
						},
					},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	// Create source file and another file referencing the old name.
	src := filepath.Join(dir, "oldname.go")
	dst := filepath.Join(dir, "newname.go")
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(src, []byte("package main\nfunc foo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// "oldname" occupies chars 0-7 on line 0; the willRenameFiles edit above
	// replaces those 7 chars with "newname".
	if err := os.WriteFile(other, []byte("oldname reference\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(true),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}

	got := result.Data.(string)
	// Verify summary mentions the applied edit and the rename.
	if !strings.Contains(got, "applied 1 edit") {
		t.Errorf("expected 'applied 1 edit' in summary, got: %s", got)
	}
	if !strings.Contains(got, "Renamed") {
		t.Errorf("expected 'Renamed' in summary, got: %s", got)
	}

	// Verify edit was applied to other.go.
	otherContent, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(otherContent), "oldname") {
		t.Errorf("other.go should have 'oldname' replaced with 'newname', got: %s", otherContent)
	}
	if !strings.Contains(string(otherContent), "newname") {
		t.Errorf("other.go should contain 'newname', got: %s", otherContent)
	}

	// Verify physical rename happened.
	if _, err := os.Stat(src); err == nil {
		t.Error("source file still exists")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Error("destination file missing")
	}
}

// TestIntegration_RenameFile_PreviewMode covers the preview path (apply=false):
// no disk writes, edits listed as what-would-happen.
func TestIntegration_RenameFile_PreviewMode(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "workspace/willRenameFiles":
				otherURI := lsp.FileToURI(filepath.Join(d, "caller.go"))
				return map[string]any{
					"changes": map[string][]map[string]any{
						otherURI: {{
							"range": map[string]any{
								"start": map[string]any{"line": 0, "character": 0},
								"end":   map[string]any{"line": 0, "character": 3},
							},
							"newText": "NEW",
						}},
					},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	caller := filepath.Join(dir, "caller.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	originalCaller := "ABC reference\n"
	if err := os.WriteFile(caller, []byte(originalCaller), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(false),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file preview: %v", err)
	}
	got := result.Data.(string)
	if !strings.Contains(got, "preview") {
		t.Errorf("preview output should mention 'preview', got: %s", got)
	}

	// Preview mode: source must still exist, caller must be unchanged.
	if _, err := os.Stat(src); err != nil {
		t.Error("source should still exist in preview mode")
	}
	gotCaller, _ := os.ReadFile(caller)
	if string(gotCaller) != originalCaller {
		t.Errorf("caller.go modified in preview mode: %q", gotCaller)
	}
}

// TestIntegration_RenameFile_MethodNotSupported covers the case where the
// server returns method-not-found for willRenameFiles — should skip the edit
// step and still do the physical rename.
func TestIntegration_RenameFile_MethodNotSupported(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				// Return an error response — handled as MethodNotFound.
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()

	src := filepath.Join(dir, "a.go")
	dst := filepath.Join(dir, "b.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	got := result.Data.(string)
	// Server returned null → no edits applied → just physical rename.
	if !strings.Contains(got, "Renamed") {
		t.Errorf("expected physical rename to succeed, got: %s", got)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Error("destination file missing")
	}
}

// ---------------------------------------------------------------------------
// Mutation-kill tests. Exact-output assertions and error-path coverage for
// renameFile and its helpers.
// ---------------------------------------------------------------------------

func editMap(uri string, line, start, end int, newText string) map[string]any {
	return map[string]any{"changes": map[string][]map[string]any{
		uri: {{"range": map[string]any{
			"start": map[string]any{"line": line, "character": start},
			"end":   map[string]any{"line": line, "character": end},
		}, "newText": newText}},
	}}
}

// recvNot waits for one recorded notification.
func recvNot(t *testing.T, ch chan string, what string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

// assertNoNot drains ch for a grace period and fails on any value.
func assertNoNot(t *testing.T, ch chan string, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s: %s", what, v)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestEnumerateRenamePairs_CapExact(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	for i := range 1001 {
		if err := os.WriteFile(filepath.Join(src, "f"+padNum(i)+".go"), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pairs, _, exceeded, err := enumerateRenamePairs(src, filepath.Join(dir, "dst"))
	if err != nil {
		t.Fatalf("enumerateRenamePairs: %v", err)
	}
	// Literals on purpose: maxRenamePairs mutations must show up here.
	if !exceeded {
		t.Error("exceeded = false, want true for 1001 files")
	}
	if len(pairs) != 1000 {
		t.Errorf("pairs = %d, want 1000", len(pairs))
	}
}

func TestEnumerateRenamePairs_WalkError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	locked := filepath.Join(src, "locked")
	if err := os.MkdirAll(locked, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(locked, 0o755) }() // let TempDir cleanup succeed
	_, _, _, err := enumerateRenamePairs(src, filepath.Join(dir, "dst"))
	if err == nil {
		t.Fatal("expected walk error for unreadable subdir, got nil")
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Errorf("error does not unwrap to *fs.PathError: %v", err)
	}
}

func TestEnumerateRenamePairs_DirExactURIs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.go"), []byte("package y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	pairs, _, _, err := enumerateRenamePairs(src, dst)
	if err != nil {
		t.Fatalf("enumerateRenamePairs: %v", err)
	}
	// WalkDir visits lexically: a.go before sub/b.go.
	if len(pairs) != 2 {
		t.Fatalf("pairs = %d, want 2", len(pairs))
	}
	if pairs[0].oldURI != lsp.FileToURI(filepath.Join(src, "a.go")) ||
		pairs[0].newURI != lsp.FileToURI(filepath.Join(dst, "a.go")) {
		t.Errorf("pair[0] = %+v", pairs[0])
	}
	if pairs[1].oldURI != lsp.FileToURI(filepath.Join(src, "sub", "b.go")) ||
		pairs[1].newURI != lsp.FileToURI(filepath.Join(dst, "sub", "b.go")) {
		t.Errorf("pair[1] = %+v", pairs[1])
	}
}

func TestRenameFile_RelativePaths(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return nil, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "old.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: "old.go", NewName: "new.go", Apply: new(true),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("relative-path rename_file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.go")); err != nil {
		t.Errorf("destination not created: %v", err)
	}
}

func TestRenameFile_Apply_ExactOutput(t *testing.T) {
	willCh := make(chan string, 4)
	didCloseCh := make(chan string, 4)
	didRenameCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "workspace/willRenameFiles":
				willCh <- string(params)
				return editMap(lsp.FileToURI(filepath.Join(d, "other.go")), 0, 0, 7, "newname"), true
			case "workspace/didRenameFiles":
				didRenameCh <- string(params)
				return nil, false
			case "textDocument/didClose":
				didCloseCh <- string(params)
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "oldname.go")
	dst := filepath.Join(dir, "nest", "newname.go")
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("oldname reference\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-open the source so the didClose path runs and its params can be
	// recorded.
	c := fakeClient(t, reg)
	if err := c.EnsureFileOpen(context.Background(), lsp.FileToURI(src), "go", "package main\n"); err != nil {
		t.Fatal(err)
	}

	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(true),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	want := "Renamed oldname.go → nest/newname.go\n" +
		"  fakels: applied 1 edit to other.go\n" +
		"  Renamed oldname.go → nest/newname.go\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("apply output =\n%q\nwant\n%q", got, want)
	}
	if info, err := os.Stat(filepath.Join(dir, "nest")); err != nil {
		t.Errorf("nest dir: %v", err)
	} else if info.Mode().Perm() != 0o755 {
		t.Errorf("nest dir perm = %o, want 755", info.Mode().Perm())
	}

	// willRenameFiles must carry the exact pair set.
	var wp struct {
		Files []struct {
			OldURI string `json:"oldUri"`
			NewURI string `json:"newUri"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(recvNot(t, willCh, "willRenameFiles")), &wp); err != nil {
		t.Fatal(err)
	}
	assertNoNot(t, willCh, "second willRenameFiles")
	if len(wp.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(wp.Files))
	}
	if wp.Files[0].OldURI != lsp.FileToURI(src) || wp.Files[0].NewURI != lsp.FileToURI(dst) {
		t.Errorf("willRenameFiles pair = %+v", wp.Files[0])
	}

	// didClose must name the source URI.
	closeParams := recvNot(t, didCloseCh, "didClose")
	var dc struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal([]byte(closeParams), &dc); err != nil {
		t.Fatal(err)
	}
	if dc.TextDocument.URI != lsp.FileToURI(src) {
		t.Errorf("didClose uri = %q, want %q", dc.TextDocument.URI, lsp.FileToURI(src))
	}

	// didRenameFiles must carry the exact pair set too.
	var rp struct {
		Files []struct {
			OldURI string `json:"oldUri"`
			NewURI string `json:"newUri"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(recvNot(t, didRenameCh, "didRenameFiles")), &rp); err != nil {
		t.Fatal(err)
	}
	if len(rp.Files) != 1 || rp.Files[0].OldURI != lsp.FileToURI(src) || rp.Files[0].NewURI != lsp.FileToURI(dst) {
		t.Errorf("didRenameFiles pair = %+v", rp.Files)
	}
	assertNoNot(t, didCloseCh, "second didClose")

	// The dest parent must exist with 0755 (mkdir-perm mutations).
	info, err := os.Stat(filepath.Join(dir, "nest"))
	if err != nil {
		t.Fatalf("nest dir: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("nest perm = %o, want 755", info.Mode().Perm())
	}
	// Edit applied + file moved.
	got, _ := os.ReadFile(other)
	if string(got) != "newname reference\n" {
		t.Errorf("other.go = %q", got)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("dest missing: %v", err)
	}
}

// TestRenameFile_DirApply_Label pins the "%d file(s) under %s" label, the
// willRenameFiles round-trip for a DIRECTORY source (the client is acquired
// per spec, not from the extension-less source path), and the per-pair
// didClose notifications.
func TestRenameFile_DirApply_Label(t *testing.T) {
	willCh := make(chan string, 4)
	didCloseCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "workspace/willRenameFiles":
				willCh <- string(params)
				return editMap(lsp.FileToURI(filepath.Join(d, "caller.go")), 0, 0, 3, "NEW"), true
			case "textDocument/didClose":
				didCloseCh <- string(params)
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	// caller.go sits OUTSIDE the renamed dir: it receives the willRename edit
	// and is not part of the rename pair set.
	if err := os.WriteFile(filepath.Join(dir, "caller.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := fakeClient(t, reg)
	for _, name := range []string{"a.go", "b.go"} {
		uri := lsp.FileToURI(filepath.Join(src, name))
		if err := c.EnsureFileOpen(context.Background(), uri, "go", "package x\n"); err != nil {
			t.Fatal(err)
		}
	}

	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: filepath.Join(dir, "dst"), Apply: new(true),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	want := "Renamed 2 files under src → dst\n" +
		"  fakels: applied 1 edit to caller.go\n" +
		"  Renamed src → dst\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("dir apply output =\n%q\nwant\n%q", got, want)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "caller.go")); string(got) != "NEWkage x\n" {
		t.Errorf("caller.go = %q, want the willRename edit applied", got)
	}
	// The directory source must reach the server as a willRenameFiles query.
	var wp struct {
		Files []struct {
			OldURI string `json:"oldUri"`
			NewURI string `json:"newUri"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(recvNot(t, willCh, "willRenameFiles")), &wp); err != nil {
		t.Fatal(err)
	}
	assertNoNot(t, willCh, "second willRenameFiles")
	if len(wp.Files) != 2 {
		t.Fatalf("willRenameFiles files = %d, want 2", len(wp.Files))
	}
	// Both source URIs get didClose.
	for range 2 {
		var dc struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal([]byte(recvNot(t, didCloseCh, "didClose")), &dc); err != nil {
			t.Fatal(err)
		}
		if dc.TextDocument.URI != lsp.FileToURI(filepath.Join(src, "a.go")) &&
			dc.TextDocument.URI != lsp.FileToURI(filepath.Join(src, "b.go")) {
			t.Errorf("didClose uri = %q", dc.TextDocument.URI)
		}
	}
	assertNoNot(t, didCloseCh, "third didClose")
}

func TestRenameFile_Preview_ExactOutput(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return editMap(lsp.FileToURI(filepath.Join(d, "caller.go")), 0, 0, 3, "NEW"), true
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(false),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file preview: %v", err)
	}
	want := "Rename preview: old.go → new.go\n  fakels:\n    caller.go: 1 edit\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("preview output =\n%q\nwant\n%q", got, want)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source must survive preview: %v", err)
	}
}

// TestRenameFile_Preview_Notes covers the willRenameFiles failure note path:
// a non-method-not-found RPC error records a server note instead of edits.
func TestRenameFile_Preview_Notes(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return rpcError{"boom"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(false),
	}), basicCtxWithDir(t, dir))
	if err == nil {
		t.Fatal("preview must abort on a willRename hard failure")
	}
	if !strings.Contains(err.Error(), "aborted rename") || !strings.Contains(err.Error(), "fakels") {
		t.Errorf("abort error = %v, want aborted rename with server named", err)
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Error("source must stay in place")
	}
}

// TestRenameFile_Preview_TwoSpecsBothFail pins the note-per-spec loop AND the
// per-spec client routing: each relevant spec must be queried exactly once on
// its own client (routing all queries to the ext-map winner would starve the
// other server and double-query the winner).
func TestRenameFile_Preview_TwoSpecsBothFail(t *testing.T) {
	fakelsCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				fakelsCh <- "q"
				return rpcError{"boom"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	fakels2Ch := make(chan string, 4)
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		if method == "workspace/willRenameFiles" {
			fakels2Ch <- "q"
			return rpcError{"boom"}, true
		}
		return nil, false
	})
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(false),
	}), basicCtxWithDir(t, dir))
	if err == nil {
		t.Fatal("two-spec hard failure must abort")
	}
	for _, name := range []string{"fakels", "fakels2"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("abort error missing %s: %v", name, err)
		}
	}
	recvNot(t, fakelsCh, "fakels willRenameFiles")
	recvNot(t, fakels2Ch, "fakels2 willRenameFiles")
	assertNoNot(t, fakelsCh, "second fakels willRenameFiles")
	assertNoNot(t, fakels2Ch, "second fakels2 willRenameFiles")
}

// TestRenameFile_Preview_MethodNotFoundContinue: spec1 answers
// method-not-found (silently skipped), spec2 returns real edits. Kills the
// isMethodNotFoundError branch and its continue.
func TestRenameFile_Preview_MethodNotFoundContinue(t *testing.T) {
	fakelsCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				fakelsCh <- "q"
				return rpcError{"method not found: workspace/willRenameFiles"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	callerURI := ""
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		if method == "workspace/willRenameFiles" {
			if callerURI == "" {
				callerURI = lsp.FileToURI(filepath.Join(dir, "caller.go"))
			}
			return editMap(callerURI, 0, 0, 3, "NEW"), true
		}
		return nil, false
	})
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(false),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file preview: %v", err)
	}
	want := "Rename preview: old.go → new.go\n  fakels2:\n    caller.go: 1 edit\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("method-not-found preview =\n%q\nwant\n%q", got, want)
	}
	// The method-not-found reply must come from spec1's own client.
	recvNot(t, fakelsCh, "fakels willRenameFiles")
	assertNoNot(t, fakelsCh, "second fakels willRenameFiles")
}

// TestRenameFile_Preview_SkipVariants: null / invalid-JSON / empty-edit
// responses must each skip that server without stopping later ones.
func TestRenameFile_Preview_SkipVariants(t *testing.T) {
	cases := []struct {
		name   string
		first  any
		wantFn func(d string) string
	}{
		{"null", nil, func(d string) string {
			return "Rename preview: old.go → new.go\n  fakels2:\n    caller.go: 1 edit\n"
		}},
		{"bad edit shape", json.RawMessage(`{"changes":5}`), func(d string) string {
			return "Rename preview: old.go → new.go\n  fakels2:\n    caller.go: 1 edit\n"
		}},
		{"empty edit", map[string]any{"changes": map[string][]map[string]any{}}, func(d string) string {
			return "Rename preview: old.go → new.go\n  fakels2:\n    caller.go: 1 edit\n"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
				return func(method string, _ json.RawMessage) (any, bool) {
					if method == "workspace/willRenameFiles" {
						return tc.first, true
					}
					return nil, false
				}
			})
			defer cleanup()
			callerURI := ""
			injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
				if method == "workspace/willRenameFiles" {
					if callerURI == "" {
						callerURI = lsp.FileToURI(filepath.Join(dir, "caller.go"))
					}
					return editMap(callerURI, 0, 0, 3, "NEW"), true
				}
				return nil, false
			})
			src := filepath.Join(dir, "old.go")
			dst := filepath.Join(dir, "new.go")
			if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := New(reg).Call(context.Background(), mustInput(t, Input{
				Action: "rename_file", File: src, NewName: dst, Apply: new(false),
			}), basicCtxWithDir(t, dir))
			if err != nil {
				t.Fatalf("rename_file preview: %v", err)
			}
			if got, _ := res.Data.(string); got != tc.wantFn(dir) {
				t.Errorf("skip-variant output =\n%q\nwant\n%q", got, tc.wantFn(dir))
			}
		})
	}
}

// TestRenameFile_Apply_Conflict: both relevant specs return the same
// overlapping edit; the first server wins and the overlap is discarded with a
// note.
func TestRenameFile_Apply_Conflict(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return editMap(lsp.FileToURI(filepath.Join(d, "other.go")), 0, 0, 7, "X"), true
			}
			return nil, false
		}
	})
	defer cleanup()
	otherURI := lsp.FileToURI(filepath.Join(dir, "other.go"))
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		if method == "workspace/willRenameFiles" {
			return editMap(otherURI, 0, 0, 7, "X"), true
		}
		return nil, false
	})
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	other := filepath.Join(dir, "other.go")
	for _, p := range []string{src, other} {
		if err := os.WriteFile(p, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(true),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	want := "Renamed old.go → new.go\n" +
		"  fakels: applied 1 edit to other.go\n" +
		"    note: discarded 1 overlapping edit from fakels2 (kept fakels)\n" +
		"  Renamed old.go → new.go\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("conflict output =\n%q\nwant\n%q", got, want)
	}
	got, _ := os.ReadFile(other)
	if string(got) != "X main\n" {
		t.Errorf("other.go = %q, want first server's edit applied once", got)
	}
}

// TestRenameFile_Apply_TwoServersDisjointEdits: non-overlapping edits from the
// second server are merged, "applied 2 edits".
func TestRenameFile_Apply_TwoServersDisjointEdits(t *testing.T) {
	otherURI := ""
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return editMap(otherURI, 0, 0, 7, "AAAAAAA"), true
			}
			return nil, false
		}
	})
	defer cleanup()
	otherURI = lsp.FileToURI(filepath.Join(dir, "other.go"))
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		if method == "workspace/willRenameFiles" {
			return editMap(otherURI, 0, 8, 12, "BBBB"), true
		}
		return nil, false
	})
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("package_maaaaain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(true),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	want := "Renamed old.go → new.go\n" +
		"  fakels: applied 2 edits to other.go\n" +
		"  Renamed old.go → new.go\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("disjoint output =\n%q\nwant\n%q", got, want)
	}
	got, _ := os.ReadFile(other)
	if string(got) != "AAAAAAA_BBBBaain\n" {
		t.Errorf("other.go = %q", got)
	}
}

// TestRenameFile_Apply_ApplyFail: edits aimed at a missing file report
// "failed to apply edits" per server; other files still apply.
func TestRenameFile_Apply_ApplyFail(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return map[string]any{"changes": map[string][]map[string]any{
					lsp.FileToURI(filepath.Join(d, "missing.go")): {{
						"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}},
						"newText": "X",
					}},
					lsp.FileToURI(filepath.Join(d, "other.go")): {{
						"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 7}},
						"newText": "edited",
					}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(true),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	got, _ := res.Data.(string)
	if !strings.Contains(got, "  fakels: applied 1 edit to other.go\n") {
		t.Errorf("missing applied line for other.go:\n%s", got)
	}
	if !strings.HasPrefix(got, "Renamed old.go → new.go\n") {
		t.Errorf("bad header:\n%s", got)
	}
	found := strings.Contains(got, "failed to apply edits")
	if !found {
		t.Fatalf("missing apply-fail note:\n%s", got)
	}
	if !strings.Contains(got, "  Renamed old.go → new.go\n") {
		t.Errorf("missing final Renamed line:\n%s", got)
	}
	gotOther, _ := os.ReadFile(other)
	if string(gotOther) != "edited main\n" {
		t.Errorf("other.go = %q", gotOther)
	}
}

// TestRenameFile_Apply_ServerNotes covers the apply-mode notes block.
func TestRenameFile_Apply_ServerNotes(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return rpcError{"boom"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(true),
	}), basicCtxWithDir(t, dir))
	if err == nil {
		t.Fatal("apply must abort on a willRename hard failure")
	}
	if !strings.Contains(err.Error(), "No files were moved") {
		t.Errorf("abort error = %v, want no-files-moved wording", err)
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Error("source must stay in place after the abort")
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("destination must not exist after the abort")
	}
}

func TestRenameFile_MkdirParentFail(t *testing.T) {
	for _, tc := range []struct {
		name, srcExt string
		apply        *bool
	}{
		{"with server", ".go", new(true)},
		{"no server", ".md", new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
				return func(method string, _ json.RawMessage) (any, bool) {
					if method == "workspace/willRenameFiles" {
						return nil, true
					}
					return nil, false
				}
			})
			defer cleanup()
			// A file squatting on the path where dest's parent must go.
			if err := os.WriteFile(filepath.Join(dir, "squatter"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(dir, "old"+tc.srcExt)
			if err := os.WriteFile(src, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := New(reg).Call(context.Background(), mustInput(t, Input{
				Action: "rename_file", File: src, NewName: filepath.Join(dir, "squatter", "dest"+tc.srcExt), Apply: tc.apply,
			}), basicCtxWithDir(t, dir))
			if err == nil || !strings.HasPrefix(err.Error(), "mkdir parent of dest:") {
				t.Fatalf("error = %v, want mkdir parent of dest:", err)
			}
			if _, ok := errors.AsType[*fs.PathError](err); !ok {
				t.Errorf("error does not unwrap to *fs.PathError: %v", err)
			}
		})
	}
}

func TestRenameFile_RenamePermFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	for _, tc := range []struct {
		name, srcExt string
	}{
		{"with server", ".go"},
		{"no server", ".md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
				return func(method string, _ json.RawMessage) (any, bool) {
					if method == "workspace/willRenameFiles" {
						return nil, true
					}
					return nil, false
				}
			})
			defer cleanup()
			sub := filepath.Join(dir, "sub")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(sub, "old"+tc.srcExt)
			if err := os.WriteFile(src, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(sub, 0o555); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chmod(sub, 0o755) }() // let TempDir cleanup succeed
			_, err := New(reg).Call(context.Background(), mustInput(t, Input{
				Action: "rename_file", File: src, NewName: filepath.Join(sub, "dest"+tc.srcExt),
			}), basicCtxWithDir(t, sub))
			if err == nil || !strings.HasPrefix(err.Error(), "rename:") {
				t.Fatalf("error = %v, want rename:", err)
			}
			if _, ok := errors.AsType[*os.LinkError](err); !ok {
				t.Errorf("error does not unwrap to *os.LinkError: %v", err)
			}
		})
	}
}

func TestRenameFile_EmptyDir(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	src := filepath.Join(dir, "empty")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: filepath.Join(dir, "dest"),
	}), basicCtxWithDir(t, dir))
	if err == nil || err.Error() != "no files to rename" {
		t.Fatalf("error = %v, want 'no files to rename'", err)
	}
}

func TestRenameFile_ExceededMessage(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 1001 {
		if err := os.WriteFile(filepath.Join(src, "f"+padNum(i)+".go"), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: filepath.Join(dir, "dst"),
	}), basicCtxWithDir(t, dir))
	want := "directory contains more than 1000 files; rename in smaller batches to keep LSP edits accurate"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestRenameFile_EnumerateErrorViaWalk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	src := filepath.Join(dir, "src")
	locked := filepath.Join(src, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(locked, 0o755) }()
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: filepath.Join(dir, "dst"),
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.HasPrefix(err.Error(), "enumerate rename pairs:") {
		t.Fatalf("error = %v, want prefix enumerate rename pairs:", err)
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Errorf("error does not unwrap to *fs.PathError: %v", err)
	}
}

// TestRenameFile_CancelledCtx: a pre-cancelled ctx must abort before any disk
// mutation.
func TestRenameFile_CancelledCtx(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return nil, true
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(reg).Call(ctx, mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source must survive cancelled rename: %v", err)
	}
}

// TestRenameFile_NotifyLoopCancel: cancelling during spec1's didClose must
// stop spec2's notifications.
func TestRenameFile_NotifyLoopCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	didCloseCh := make(chan string, 4)
	didRenameCh := make(chan string, 4)
	secondCloseCh := make(chan string, 4)
	secondRenameCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "workspace/willRenameFiles":
				return nil, true
			case "textDocument/didClose":
				// First notification cancels the ctx mid-notify-loop.
				cancel()
				didCloseCh <- string(params)
				return nil, false
			case "workspace/didRenameFiles":
				didRenameCh <- string(params)
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()
	injectSecondFake(t, reg, dir, "fakels2", func(method string, params json.RawMessage) (any, bool) {
		switch method {
		case "textDocument/didClose":
			secondCloseCh <- string(params)
			return nil, false
		case "workspace/didRenameFiles":
			secondRenameCh <- string(params)
			return nil, false
		}
		return nil, false
	})
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Open the source on fakels only, so spec1 runs didClose.
	c := fakeClient(t, reg)
	if err := c.EnsureFileOpen(ctx, lsp.FileToURI(src), "go", "package main\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(reg).Call(ctx, mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir)); err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	recvNot(t, didCloseCh, "fakels didClose")
	recvNot(t, didRenameCh, "fakels didRenameFiles")
	// fakels2 must have been skipped by the ctx check.
	assertNoNot(t, secondRenameCh, "fakels2 didRenameFiles")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("dest missing: %v", err)
	}
}

// TestRenameFile_NotifyLoopCancelThreeSpecs: cancelling during spec1's
// didClose must stop notifications for BOTH later specs (a break->continue
// mutation would reach spec3 after skipping spec2).
func TestRenameFile_NotifyLoopCancelThreeSpecs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	thirdRenameCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "workspace/willRenameFiles":
				return nil, true
			case "textDocument/didClose":
				cancel()
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		return nil, false
	})
	injectSecondFake(t, reg, dir, "fakels3", func(method string, params json.RawMessage) (any, bool) {
		if method == "workspace/didRenameFiles" {
			thirdRenameCh <- string(params)
			return nil, false
		}
		return nil, false
	})
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	if err := c.EnsureFileOpen(ctx, lsp.FileToURI(src), "go", "package main\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(reg).Call(ctx, mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir)); err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	assertNoNot(t, thirdRenameCh, "fakels3 didRenameFiles")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("dest missing: %v", err)
	}
}

func TestCollectRelevantRenameServers_ExactSources(t *testing.T) {
	specs := []lsp.ServerSpec{
		{Name: "go1", FileExts: []string{".go"}},
		{Name: "multi", FileExts: []string{".zzz", ".go"}},
		{Name: "rs", FileExts: []string{".rs"}},
	}
	// Source-ext only: pairs empty so .go can only come from source.
	got := collectRelevantRenameServers(specs, "/w/a.go", "/w/b.go", nil)
	if len(got) != 2 || got[0].Name != "go1" || got[1].Name != "multi" {
		t.Errorf("source-ext only: got %+v", got)
	}
	// Dest-ext only.
	got = collectRelevantRenameServers(specs, "/w/a.md", "/w/b.go", nil)
	if len(got) != 2 || got[0].Name != "go1" || got[1].Name != "multi" {
		t.Errorf("dest-ext only: got %+v", got)
	}
	// Pair old-ext only, and pair new-ext only.
	got = collectRelevantRenameServers(specs, "/w/a.md", "/w/b.md", []fileRenamePair{
		{oldURI: "file:///w/c.go", newURI: "file:///w/d.md"},
	})
	if len(got) != 2 || got[0].Name != "go1" || got[1].Name != "multi" {
		t.Errorf("pair old-ext only: got %+v", got)
	}
	got = collectRelevantRenameServers(specs, "/w/a.md", "/w/b.md", []fileRenamePair{
		{oldURI: "file:///w/c.md", newURI: "file:///w/d.go"},
	})
	if len(got) != 2 || got[0].Name != "go1" || got[1].Name != "multi" {
		t.Errorf("pair new-ext only: got %+v", got)
	}
	// A spec matching TWO queried extensions must be appended ONCE: the
	// inner loop breaks after the first matching ext.
	got = collectRelevantRenameServers(specs, "/w/a.zzz", "/w/b.go", nil)
	if len(got) != 2 || got[1].Name != "multi" {
		t.Errorf("multi-ext dedupe: got %+v, want [go1 multi] once each", got)
	}
	// The inner-ext loop must look past a non-matching first extension
	// ("multi" claims .go as its second entry).
	got = collectRelevantRenameServers(specs, "/w/a.go", "/w/b.go", nil)
	if len(got) != 2 {
		t.Fatalf("inner-ext loop: got %d specs", len(got))
	}
	if got[1].Name != "multi" {
		t.Errorf("inner-ext loop: got[1] = %q, want multi", got[1].Name)
	}
}

// TestRenameFile_SpawnFailureNote evicts the injected client so the loop must
// spawn (and fail: fake-lsp is not a real binary). Pins the note path only —
// the spawn dies at lookpath, before any ctx use, so the nil-ctx kill lives in
// TestRenameFile_PerSpecSpawnPath below.
func TestRenameFile_SpawnFailureNote(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply *bool
	}{
		{"preview", new(false)},
		{"apply", new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler { return nil })
			defer cleanup()
			src := filepath.Join(dir, "old.go")
			dst := filepath.Join(dir, "new.go")
			if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if !reg.KillAndEvict("fakels", dir) {
				t.Fatal("KillAndEvict: no live client")
			}
			res, err := New(reg).Call(context.Background(), mustInput(t, Input{
				Action: "rename_file", File: src, NewName: dst, Apply: tc.apply,
			}), basicCtxWithDir(t, dir))
			if err != nil {
				t.Fatalf("rename_file: %v", err)
			}
			got, _ := res.Data.(string)
			if !strings.Contains(got, "  Server notes:\n") {
				t.Fatalf("missing server notes:\n%s", got)
			}
			if !strings.Contains(got, "fakels: ") || !strings.Contains(got, "fake-lsp") {
				t.Errorf("spawn-failure note missing server/binary name:\n%s", got)
			}
			header := "Rename preview: old.go → new.go"
			if tc.apply == nil || *tc.apply {
				header = "Renamed old.go → new.go"
			}
			if !strings.Contains(got, header) {
				t.Errorf("missing header %q:\n%s", header, got)
			}
		})
	}
}

// TestRenameFile_PerSpecSpawnPath arms a spec whose client must be SPAWNED in
// both the willRename and didRename loops: spec1 stays live, spec2's client is
// evicted and its command resolves (the "false" binary), so each loop really
// executes the registry spawn path. A nil-ctx mutation on either loop's
// client-acquisition call panics in context.WithTimeout, which this test
// detects; a routing mutation that resolves every spec through one client
// mislabels or doubles the spawn-failure notes.
func TestRenameFile_PerSpecSpawnPath(t *testing.T) {
	didRenameCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "workspace/willRenameFiles":
				return nil, true
			case "workspace/didRenameFiles":
				didRenameCh <- string(params)
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()
	// Register spec2 with a spawnable command, then evict its live client so
	// both loops must spawn. The "false" binary starts fine but exits at once,
	// so the handshake fails fast at Initialize.
	clientConn, serverConn := net.Pipe()
	var wg sync.WaitGroup
	wg.Go(func() { serveFake(t, serverConn, nil, dir) })
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
		wg.Wait()
	})
	c2 := lsp.NewTestClient("fakels2", clientConn)
	reg.InjectClientInRoot("fakels2", dir, lsp.ServerSpec{
		Name:     "fakels2",
		Language: "Fake",
		FileExts: []string{".go"},
		Command:  "false",
	}, c2)
	if !reg.KillAndEvict("fakels2", dir) {
		t.Fatal("KillAndEvict: no live fakels2 client")
	}

	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	// Everything deterministic is pinned by the prefix; the initialize
	// failure suffix races between a broken-pipe write and the dead-server
	// select, so only the prefix is stable.
	want := "Renamed old.go → new.go\n" +
		"  Renamed old.go → new.go\n" +
		"  Server notes:\n" +
		"  fakels2: lsp fakels2: initialize: "
	got, _ := res.Data.(string)
	if !strings.HasPrefix(got, want) {
		t.Fatalf("spawn-path output =\n%q\nwant prefix\n%q", got, want)
	}
	if n := strings.Count(got, "\n"); n != 4 {
		t.Errorf("spawn-path output has %d lines, want exactly 4:\n%s", n, got)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("dest missing: %v", err)
	}
	// spec1's live client delivered didRenameFiles; spec2's spawn failure
	// silently skipped it.
	recvNot(t, didRenameCh, "fakels didRenameFiles")
	assertNoNot(t, didRenameCh, "second didRenameFiles")
}

// TestRenameFile_AcquisitionContinueAfterFailure: a spec whose client cannot
// be acquired must not starve the LATER specs — both the willRename and the
// didRename loops continue past the failure (break/continue mutants).
func TestRenameFile_AcquisitionContinueAfterFailure(t *testing.T) {
	didRenameCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler { return nil })
	defer cleanup()
	// fakels (first spec) evicted: fake-lsp is not a real binary, so every
	// acquisition fails at lookpath. fakels2 stays live and answers edits.
	if !reg.KillAndEvict("fakels", dir) {
		t.Fatal("KillAndEvict: no live fakels client")
	}
	otherURI := ""
	injectSecondFake(t, reg, dir, "fakels2", func(method string, params json.RawMessage) (any, bool) {
		switch method {
		case "workspace/willRenameFiles":
			return editMap(otherURI, 0, 0, 7, "X"), true
		case "workspace/didRenameFiles":
			didRenameCh <- string(params)
			return nil, false
		}
		return nil, false
	})
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	other := filepath.Join(dir, "other.go")
	for _, p := range []string{src, other} {
		if err := os.WriteFile(p, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	otherURI = lsp.FileToURI(other)

	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	got, _ := res.Data.(string)
	if !strings.Contains(got, "  fakels2: applied 1 edit to other.go\n") {
		t.Errorf("later spec starved by the earlier acquisition failure:\n%s", got)
	}
	if !strings.Contains(got, "  Server notes:\n  fakels: lsp fakels: lookpath:") {
		t.Errorf("missing fakels acquisition-failure note:\n%s", got)
	}
	if strings.Count(got, "  Server notes:\n") != 1 || strings.Count(got, "lookpath") != 1 {
		t.Errorf("note count wrong (one failure, later spec continues):\n%s", got)
	}
	recvNot(t, didRenameCh, "fakels2 didRenameFiles")
	assertNoNot(t, didRenameCh, "second didRenameFiles")
}

// TestRenameFile_DidCloseOpenSet: only URIs the server has open get didClose;
// an unopened pair must be skipped without stopping later pairs.
func TestRenameFile_DidCloseOpenSet(t *testing.T) {
	didCloseCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			if method == "textDocument/didClose" {
				didCloseCh <- string(params)
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Open ONLY b.go: the walk order puts unopened a.go first, so a break or
	// fall-through at a.go would drop or add a didClose.
	c := fakeClient(t, reg)
	if err := c.EnsureFileOpen(context.Background(), lsp.FileToURI(filepath.Join(src, "b.go")), "go", "package x\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: filepath.Join(dir, "dst"),
	}), basicCtxWithDir(t, dir)); err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	var dc struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal([]byte(recvNot(t, didCloseCh, "didClose")), &dc); err != nil {
		t.Fatal(err)
	}
	if dc.TextDocument.URI != lsp.FileToURI(filepath.Join(src, "b.go")) {
		t.Errorf("didClose uri = %q, want only the opened b.go", dc.TextDocument.URI)
	}
	assertNoNot(t, didCloseCh, "second didClose")
}

// TestRenameFile_NoServersDirLabel: with no relevant specs the dir label still
// reads "N files under X" (renameFileNoServers has its own label copy).
func TestRenameFile_NoServersDirLabel(t *testing.T) {
	dir := t.TempDir()
	reg := lsp.NewRegistry(dir)
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(dir, "nested", "dst")
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file: %v", err)
	}
	want := "Renamed 2 files under src → nested/dst\n  (no LSP server for these files; physical rename only)\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("no-servers dir output =\n%q\nwant\n%q", got, want)
	}
	if info, err := os.Stat(filepath.Join(dir, "nested")); err != nil {
		t.Errorf("nested dir: %v", err)
	} else if info.Mode().Perm() != 0o755 {
		t.Errorf("nested dir perm = %o, want 755", info.Mode().Perm())
	}
}

// TestRenameFile_PreviewUnformattableEdit: an edit accepted into perServer but
// formatting to zero lines (bogus documentChanges) must not stop the preview
// from printing LATER servers' sections.
func TestRenameFile_PreviewUnformattableEdit(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return map[string]any{"documentChanges": []map[string]any{{"bogus": 1}}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	callerURI := ""
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		if method == "workspace/willRenameFiles" {
			return editMap(callerURI, 0, 0, 3, "NEW"), true
		}
		return nil, false
	})
	src := filepath.Join(dir, "old.go")
	dst := filepath.Join(dir, "new.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	callerURI = lsp.FileToURI(filepath.Join(dir, "caller.go"))
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst, Apply: new(false),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("rename_file preview: %v", err)
	}
	want := "Rename preview: old.go → new.go\n  fakels2:\n    caller.go: 1 edit\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("unformattable-edit preview =\n%q\nwant\n%q", got, want)
	}
}

// A willRenameFiles hard failure (real server error, not method-not-found)
// must abort before any mutation — moving the path with the semantic edits
// dropped would leave references dangling (omp #8380 semantics).
func TestIntegration_RenameFile_WillRenameHardFailureAborts(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			if method == "workspace/willRenameFiles" {
				return rpcError{msg: "server exploded"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "a.go")
	dst := filepath.Join(dir, "b.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: src, NewName: dst,
	}), basicCtxWithDir(t, dir))
	if err == nil {
		t.Fatal("rename_file: expected abort error, got nil")
	}
	if !strings.Contains(err.Error(), "aborted rename") || !strings.Contains(err.Error(), "No files were moved") {
		t.Errorf("error = %v, want aborted-rename wording", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Error("source must stay in place after the abort")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("destination must not exist after the abort")
	}
}
