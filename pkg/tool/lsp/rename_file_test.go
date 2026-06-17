package lsptool

import (
	"context"
	"encoding/json"
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
