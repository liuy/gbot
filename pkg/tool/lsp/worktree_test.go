package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// projectRootFor walks up to "/", so the tests here whose premise is "this
// directory has no project" depend on /tmp and / carrying no go.mod,
// package.json or Cargo.toml: TestIntegration_LspTool_ForeignFileWithoutProject,
// _RenameRefusedOutsideWorkspace, _RenameFileRefusedOutsideWorkspace,
// _RenameFileGuardSeesTrimmedPath, _CodeActionsRefusedOutsideWorkspace and
// _UsesRootOfResolvedSymbol. A marker above a markerless temp dir routes the
// request to that marker instead of the default root and makes the write guard
// decide the file is safe. If one of these goes red without a code change, look
// for a stray marker in /tmp before looking at the code.

// countOccurrences exists because every routing claim in this file is an exact
// count: a wrong root produces the same shape of output with different paths,
// so "at least one" proves nothing.
func countOccurrences(s, sub string) int {
	return strings.Count(s, sub)
}

func callTool(t *testing.T, reg *lsp.Registry, wd string, in Input) (string, error) {
	t.Helper()
	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, in), &tool.ToolUseContext{WorkingDir: wd})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result.Data), nil
}

func writeGoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func rangeOf(line, startChar, endChar int) map[string]any {
	return map[string]any{
		"start": map[string]any{"line": line, "character": startChar},
		"end":   map[string]any{"line": line, "character": endChar},
	}
}

func locationOf(uri string, line, endChar int) map[string]any {
	return map[string]any{"uri": uri, "range": rangeOf(line, 0, endChar)}
}

func hierarchyItemOf(name, uri string, line, endChar int) map[string]any {
	return map[string]any{
		"name":  name,
		"kind":  12,
		"uri":   uri,
		"range": rangeOf(line, 0, endChar),
	}
}

// newTwoRootFakeEnv injects one fake client per root for the same spec name.
// wtSymbolPath is the file the worktree fake's workspace/symbol reports for the
// query "target"; "" means the default <wtDir>/a/pkg.go. The "wtonly" answer is
// fixed at <wtDir>/a/wtonly.go and is deliberately not configurable — it is the
// fixture's only root-exclusive symbol.
func newTwoRootFakeEnv(t *testing.T, mainDir, wtDir, wtSymbolPath string) (*lsp.Registry, func()) {
	t.Helper()

	if wtSymbolPath == "" {
		wtSymbolPath = filepath.Join(wtDir, "a", "pkg.go")
	}

	for _, dir := range []string{mainDir, wtDir} {
		writeGoFile(t, filepath.Join(dir, "go.mod"), "module example.com/proj\n\ngo 1.27\n")
		writeGoFile(t, filepath.Join(dir, "a", "pkg.go"), "package a\n\nfunc target() {}\n")
		writeGoFile(t, filepath.Join(dir, "a", "use.go"), "package a\n\nfunc use() { target() }\n")
		writeGoFile(t, filepath.Join(dir, "a", "caller.go"), "package a\n\nfunc caller() { target() }\n")
		writeGoFile(t, filepath.Join(dir, "a", "callee.go"), "package a\n\nfunc callee() {}\n")
	}
	// Written here rather than per test: a fake may only report a symbol whose
	// file exists, because resolveAndOpen stats and opens the resolved target.
	writeGoFile(t, filepath.Join(wtDir, "a", "wtonly.go"), "package a\n\nfunc wtonly() {}\n")
	if wtSymbolPath != filepath.Join(wtDir, "a", "pkg.go") {
		writeGoFile(t, wtSymbolPath, "package dep\n\nfunc target() {}\n")
	}

	reg := lsp.NewRegistry(mainDir)
	spec := lsp.ServerSpec{
		Name:     "fakels",
		Language: "Fake",
		FileExts: []string{".go"},
		Command:  "fake-lsp",
	}

	var wg sync.WaitGroup
	var conns []net.Conn

	inject := func(dir, root, targetFile string, answerWtonly bool) {
		t.Helper()
		clientConn, serverConn := net.Pipe()
		conns = append(conns, clientConn, serverConn)
		wg.Go(func() {
			serveFake(t, serverConn, twoRootHandler(dir, targetFile, answerWtonly), dir)
		})
		c := lsp.NewTestClient("fakels", clientConn)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.Initialize(ctx, lsp.FileToURI(dir)); err != nil {
			t.Fatalf("Initialize(%s): %v", root, err)
		}
		reg.InjectClientInRoot("fakels", root, spec, c)
	}

	inject(mainDir, mainDir, filepath.Join(mainDir, "a", "pkg.go"), false)
	inject(wtDir, wtDir, wtSymbolPath, true)

	cleanup := func() {
		for _, c := range conns {
			_ = c.Close()
		}
		wg.Wait()
	}
	return reg, cleanup
}

// twoRootHandler answers the methods the worktree tests need, fixed per root.
// The location handlers deliberately do not echo the requested URI: the point of
// the fixture is that one fake answers for a document that lives somewhere else.
func twoRootHandler(dir, targetFile string, answerWtonly bool) fakeHandler {
	pkgURI := lsp.FileToURI(filepath.Join(dir, "a", "pkg.go"))
	useURI := lsp.FileToURI(filepath.Join(dir, "a", "use.go"))
	callerURI := lsp.FileToURI(filepath.Join(dir, "a", "caller.go"))
	calleeURI := lsp.FileToURI(filepath.Join(dir, "a", "callee.go"))
	wtonlyURI := lsp.FileToURI(filepath.Join(dir, "a", "wtonly.go"))
	targetURI := lsp.FileToURI(targetFile)

	return func(method string, params json.RawMessage) (any, bool) {
		switch method {
		case "workspace/symbol":
			var p struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, false
			}
			switch p.Query {
			case "target":
				return []map[string]any{{
					"name":     "target",
					"kind":     12,
					"location": locationOf(targetURI, 0, 15),
				}}, true
			case "wtonly":
				if !answerWtonly {
					return nil, false
				}
				return []map[string]any{{
					"name":     "wtonly",
					"kind":     12,
					"location": locationOf(wtonlyURI, 2, 15),
				}}, true
			}
			return nil, false
		case "textDocument/definition":
			return []map[string]any{locationOf(pkgURI, 0, 15)}, true
		case "textDocument/references":
			return []map[string]any{locationOf(useURI, 0, 22), locationOf(useURI, 0, 22)}, true
		case "textDocument/prepareCallHierarchy":
			return []map[string]any{hierarchyItemOf("target", pkgURI, 0, 15)}, true
		case "callHierarchy/incomingCalls":
			return []map[string]any{{
				"from":       hierarchyItemOf("UseMain", callerURI, 3, 9),
				"fromRanges": []map[string]any{rangeOf(3, 1, 7)},
			}}, true
		case "callHierarchy/outgoingCalls":
			return []map[string]any{{
				"to":         hierarchyItemOf("HelperMain", calleeURI, 5, 14),
				"fromRanges": []map[string]any{rangeOf(3, 1, 7)},
			}}, true
		case "textDocument/codeAction":
			return []map[string]any{{
				"title": "Add import",
				"kind":  "quickfix",
				"edit": map[string]any{
					"changes": map[string]any{
						pkgURI: []map[string]any{{
							"range":   rangeOf(0, 0, 7),
							"newText": "package a",
						}},
					},
				},
			}}, true
		case "textDocument/rename":
			return map[string]any{
				"changes": map[string]any{
					pkgURI: []map[string]any{{
						"range":   rangeOf(0, 5, 11),
						"newText": "renamed",
					}},
				},
			}, true
		}
		return nil, false
	}
}

func TestIntegration_LspTool_ReferencesInWorktreeFile(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()

	got, err := callTool(t, reg, mainDir, Input{
		Action: "references", Symbol: "target", File: filepath.Join(wtDir, "a", "use.go"),
	})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if n := countOccurrences(got, "Found 2 reference(s):"); n != 1 {
		t.Errorf("count of 'Found 2 reference(s):' = %d, want 1\n%s", n, got)
	}
	if n := countOccurrences(got, wtDir+"/a/use.go"); n != 2 {
		t.Errorf("count of %q = %d, want 2: the worktree server must answer\n%s", wtDir+"/a/use.go", n, got)
	}
	if n := countOccurrences(got, mainDir); n != 0 {
		t.Errorf("count of %q = %d, want 0\n%s", mainDir, n, got)
	}
}

func TestIntegration_LspTool_DefinitionInWorktreeFile(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()

	got, err := callTool(t, reg, mainDir, Input{
		Action: "definition", Symbol: "target", File: filepath.Join(wtDir, "a", "use.go"),
	})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if n := countOccurrences(got, wtDir+"/a/pkg.go"); n != 1 {
		t.Errorf("count of %q = %d, want 1\n%s", wtDir+"/a/pkg.go", n, got)
	}
	if n := countOccurrences(got, mainDir); n != 0 {
		t.Errorf("count of %q = %d, want 0\n%s", mainDir, n, got)
	}
}

func TestIntegration_LspTool_SymbolOnlyUsesDefaultRoot(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()

	got, err := callTool(t, reg, mainDir, Input{Action: "definition", Symbol: "target"})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	// A location inside WorkingDir renders relative, so the only claim that can
	// discriminate here is that the worktree server was never consulted.
	if n := countOccurrences(got, "a/pkg.go"); n != 1 {
		t.Errorf("count of 'a/pkg.go' = %d, want 1\n%s", n, got)
	}
	if n := countOccurrences(got, wtDir); n != 0 {
		t.Errorf("count of %q = %d, want 0: a symbol-only query must not fan out to foreign roots\n%s", wtDir, n, got)
	}

	_, err = callTool(t, reg, mainDir, Input{Action: "definition", Symbol: "wtonly"})
	if err == nil {
		t.Fatal("definition of a worktree-only symbol without a file should fail")
	}
	want := fmt.Sprintf("symbol %q not found in %s (pass file=<a path inside the project you mean> to query a different project)", "wtonly", reg.DefaultRoot())
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestIntegration_LspTool_ForeignFileWithoutProject(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	markerlessDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()
	writeGoFile(t, filepath.Join(markerlessDir, "x.go"), "package x\n")

	got, err := callTool(t, reg, mainDir, Input{
		Action: "definition", Symbol: "target", File: filepath.Join(markerlessDir, "x.go"),
	})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if n := countOccurrences(got, "a/pkg.go"); n != 1 {
		t.Errorf("count of 'a/pkg.go' = %d, want 1: the default-root server must serve a markerless file\n%s", n, got)
	}
	if n := countOccurrences(got, markerlessDir); n != 0 {
		t.Errorf("count of %q = %d, want 0\n%s", markerlessDir, n, got)
	}
	if n := countOccurrences(got, wtDir); n != 0 {
		t.Errorf("count of %q = %d, want 0\n%s", wtDir, n, got)
	}
}

func TestIntegration_LspTool_StatusListsBothRoots(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()

	_, err := callTool(t, reg, mainDir, Input{
		Action: "references", Symbol: "target", File: filepath.Join(wtDir, "a", "use.go"),
	})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	got, err := callTool(t, reg, mainDir, Input{Action: "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	var readyLines []string
	var bulletLines []string
	for line := range strings.SplitSeq(got, "\n") {
		if strings.Contains(line, "fakels — ready") {
			readyLines = append(readyLines, line)
		}
		if strings.HasPrefix(line, "  • ") {
			bulletLines = append(bulletLines, line)
		}
	}
	if len(readyLines) != 2 {
		t.Fatalf("lines containing 'fakels — ready' = %d, want 2\n%s", len(readyLines), got)
	}
	if !strings.Contains(readyLines[0], mainDir) {
		t.Errorf("first ready line = %q, want it to name the default root %q", readyLines[0], mainDir)
	}
	if !strings.Contains(readyLines[1], wtDir) {
		t.Errorf("second ready line = %q, want it to name %q", readyLines[1], wtDir)
	}
	if len(bulletLines) != 2 {
		t.Errorf("lines beginning '  • ' = %d, want 2\n%s", len(bulletLines), got)
	}
	if n := countOccurrences(got, "— not started"); n != 0 {
		t.Errorf("count of '— not started' = %d, want 0\n%s", n, got)
	}
}

func TestIntegration_LspTool_ImpactInWorktreeFile(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()

	got, err := callTool(t, reg, mainDir, Input{
		Action: "impact", Symbol: "target", File: filepath.Join(wtDir, "a", "use.go"),
	})
	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	for _, header := range []string{"## References", "## Callers", "## Callees"} {
		if n := countOccurrences(got, header); n != 1 {
			t.Errorf("count of %q = %d, want 1\n%s", header, n, got)
		}
	}
	for _, claim := range []struct {
		sub  string
		want int
	}{
		{"Found 2 reference(s):", 1},
		{"Found 1 caller(s):", 1},
		{"Found 1 callee(s):", 1},
		{wtDir + "/a/use.go", 2},
		{wtDir + "/a/caller.go", 1},
		{wtDir + "/a/callee.go", 1},
	} {
		if n := countOccurrences(got, claim.sub); n != claim.want {
			t.Errorf("count of %q = %d, want %d\n%s", claim.sub, n, claim.want, got)
		}
	}
	if n := countOccurrences(got, mainDir); n != 0 {
		t.Errorf("count of %q = %d, want 0\n%s", mainDir, n, got)
	}
}

func TestIntegration_LspTool_RenameRefusedOutsideWorkspace(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	markerlessDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()
	writeGoFile(t, filepath.Join(markerlessDir, "x.go"), "package x\n")

	pkgPath := filepath.Join(mainDir, "a", "pkg.go")
	before, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pkgPath, err)
	}

	_, err = callTool(t, reg, mainDir, Input{
		Action: "rename", Symbol: "target", NewName: "renamed", File: filepath.Join(markerlessDir, "x.go"),
	})
	if err == nil {
		t.Fatal("rename of a markerless foreign file should be refused")
	}
	msg := err.Error()
	for _, want := range []string{"outside the workspace root", mainDir, markerlessDir + "/x.go"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}

	after, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("read %s after: %v", pkgPath, err)
	}
	if string(after) != string(before) {
		t.Errorf("%s was modified by the refused rename:\nbefore=%q\nafter=%q", pkgPath, before, after)
	}
}

func TestIntegration_LspTool_RenameFileRefusedOutsideWorkspace(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	markerlessDir := t.TempDir()
	source := filepath.Join(markerlessDir, "a.go")
	dest := filepath.Join(markerlessDir, "b.go")
	writeGoFile(t, source, "package a\n")

	tt := New(reg)
	_, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: source, NewName: dest,
	}), basicCtxWithDir(t, dir))
	if err == nil {
		t.Fatal("rename_file of a markerless foreign file should be refused")
	}
	msg := err.Error()
	for _, want := range []string{"outside the workspace root", dir, source} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}

	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("source %s should still exist: %v", source, err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("source %s is not a regular file", source)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("destination %s should not exist, got err = %v", dest, err)
	}
}

// The guard must judge the path the rename actually performs. rename_file trims
// `file` before resolving it, so a JSON argument padded with whitespace would be
// judged as <workingDir>/  <path>  — inside the workspace — while the rename ran
// on the real, foreign one. Stray whitespace is realistic LLM output.
func TestIntegration_LspTool_RenameFileGuardSeesTrimmedPath(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	markerlessDir := t.TempDir()
	source := filepath.Join(markerlessDir, "a.go")
	dest := filepath.Join(markerlessDir, "b.go")
	writeGoFile(t, source, "package a\n")

	tt := New(reg)
	_, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "rename_file", File: "  " + source + "  ", NewName: dest,
	}), basicCtxWithDir(t, dir))
	if err == nil {
		t.Fatalf("rename_file of %q should be refused: the rename runs on %s, which is outside %s", "  "+source+"  ", source, dir)
	}
	want := "rename_file: file " + source + " is outside the workspace root " + dir +
		" and its directory has no project marker for its file type (go.mod, package.json or Cargo.toml), so no language server" +
		" can be rooted at its directory; pass a file inside " + dir + " or a file whose directory has a marker for that extension"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}

	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("source %s should still exist: %v", source, err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("source %s is not a regular file", source)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("destination %s should not exist, got err = %v", dest, err)
	}
}

// rename previews edits without touching disk, so there is no write to guard.
// This is the counterpart of TestIntegration_LspTool_RenameRefusedOutsideWorkspace:
// it fails if the guard stops being gated on apply.
func TestIntegration_LspTool_RenamePreviewModeNotGuarded(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	markerlessDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()
	writeGoFile(t, filepath.Join(markerlessDir, "x.go"), "package x\n")

	pkgPath := filepath.Join(mainDir, "a", "pkg.go")
	before, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pkgPath, err)
	}

	got, err := callTool(t, reg, mainDir, Input{
		Action: "rename", Symbol: "target", NewName: "renamed",
		File: filepath.Join(markerlessDir, "x.go"), Apply: new(false),
	})
	if err != nil {
		t.Fatalf("rename preview should not be guarded: %v", err)
	}
	if n := countOccurrences(got, "outside the workspace root"); n != 0 {
		t.Errorf("count of 'outside the workspace root' = %d, want 0: a preview is not a write\n%s", n, got)
	}
	if n := countOccurrences(got, "Rename preview:"); n != 1 {
		t.Errorf("count of 'Rename preview:' = %d, want 1\n%s", n, got)
	}
	if n := countOccurrences(got, "a/pkg.go: 1 edit"); n != 1 {
		t.Errorf("count of 'a/pkg.go: 1 edit' = %d, want 1\n%s", n, got)
	}

	after, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("read %s after: %v", pkgPath, err)
	}
	if string(after) != string(before) {
		t.Errorf("%s was modified by a preview:\nbefore=%q\nafter=%q", pkgPath, before, after)
	}
}

// rename_file's preview mode is the same case in the other guarded action: the
// edits are listed, nothing is renamed, so the guard must not run.
func TestIntegration_LspTool_RenameFilePreviewModeNotGuarded(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	markerlessDir := t.TempDir()
	source := filepath.Join(markerlessDir, "a.go")
	dest := filepath.Join(markerlessDir, "b.go")
	writeGoFile(t, source, "package a\n")

	got, err := callTool(t, reg, dir, Input{
		Action: "rename_file", File: source, NewName: dest, Apply: new(false),
	})
	if err != nil {
		t.Fatalf("rename_file preview should not be guarded: %v", err)
	}
	if n := countOccurrences(got, "outside the workspace root"); n != 0 {
		t.Errorf("count of 'outside the workspace root' = %d, want 0: a preview is not a write\n%s", n, got)
	}
	if n := countOccurrences(got, "Rename preview:"); n != 1 {
		t.Errorf("count of 'Rename preview:' = %d, want 1\n%s", n, got)
	}
	if n := countOccurrences(got, "No LSP edits would be applied"); n != 1 {
		t.Errorf("count of 'No LSP edits would be applied' = %d, want 1\n%s", n, got)
	}

	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("source %s should still exist: %v", source, err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("source %s is not a regular file", source)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("destination %s should not exist, got err = %v", dest, err)
	}
}

func TestIntegration_LspTool_CodeActionsRefusedOutsideWorkspace(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	markerlessDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()
	writeGoFile(t, filepath.Join(markerlessDir, "x.go"), "package x\n")

	pkgPath := filepath.Join(mainDir, "a", "pkg.go")
	before, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("read %s: %v", pkgPath, err)
	}

	_, err = callTool(t, reg, mainDir, Input{
		Action: "code_actions", Symbol: "target", Query: "Add import",
		File: filepath.Join(markerlessDir, "x.go"), Apply: new(true),
	})
	if err == nil {
		t.Fatal("code_actions with apply=true on a markerless foreign file should be refused")
	}
	msg := err.Error()
	for _, want := range []string{"outside the workspace root", mainDir} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}

	after, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("read %s after: %v", pkgPath, err)
	}
	if string(after) != string(before) {
		t.Errorf("%s was modified by the refused code action:\nbefore=%q\nafter=%q", pkgPath, before, after)
	}
}

func TestIntegration_LspTool_CodeActionsListModeNotGuarded(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	markerlessDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()
	writeGoFile(t, filepath.Join(markerlessDir, "x.go"), "package x\n")

	got, err := callTool(t, reg, mainDir, Input{
		Action: "code_actions", Symbol: "target", File: filepath.Join(markerlessDir, "x.go"),
	})
	if err != nil {
		t.Fatalf("code_actions: %v", err)
	}
	if n := countOccurrences(got, "outside the workspace root"); n != 0 {
		t.Errorf("count of 'outside the workspace root' = %d, want 0: listing is not a write\n%s", n, got)
	}
	if n := countOccurrences(got, "1 code action(s):"); n != 1 {
		t.Errorf("count of '1 code action(s):' = %d, want 1\n%s", n, got)
	}
	if n := countOccurrences(got, "Add import"); n != 1 {
		t.Errorf("count of 'Add import' = %d, want 1\n%s", n, got)
	}
}

func TestIntegration_LspTool_UsesRootOfResolvedSymbol(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	thirdDir := t.TempDir()
	depPath := filepath.Join(thirdDir, "dep.go")
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, depPath)
	defer cleanup()

	got, err := callTool(t, reg, mainDir, Input{
		Action: "references", Symbol: "target", File: filepath.Join(wtDir, "a", "use.go"),
	})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if n := countOccurrences(got, "Found 2 reference(s):"); n != 1 {
		t.Errorf("count of 'Found 2 reference(s):' = %d, want 1\n%s", n, got)
	}
	if n := countOccurrences(got, wtDir+"/a/use.go"); n != 2 {
		t.Errorf("count of %q = %d, want 2: the client that resolved the symbol must serve the file\n%s", wtDir+"/a/use.go", n, got)
	}
	if n := countOccurrences(got, thirdDir); n != 0 {
		t.Errorf("count of %q = %d, want 0\n%s", thirdDir, n, got)
	}
	live := reg.LiveServers()
	if len(live) != 2 {
		t.Fatalf("len(LiveServers) = %d, want 2", len(live))
	}
	for _, ls := range live {
		if ls.Root == thirdDir {
			t.Errorf("a server was rooted at the markerless dir %q", thirdDir)
		}
	}
}

func TestIntegration_LspTool_SymbolResolvedOnlyInWorktreeRoot(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()
	file := filepath.Join(wtDir, "a", "use.go")

	got, err := callTool(t, reg, mainDir, Input{Action: "definition", Symbol: "wtonly", File: file})
	if err != nil {
		t.Fatalf("definition wtonly: %v", err)
	}
	if n := countOccurrences(got, wtDir+"/a/pkg.go"); n != 1 {
		t.Errorf("count of %q = %d, want 1: only the worktree server knows wtonly\n%s", wtDir+"/a/pkg.go", n, got)
	}
	if n := countOccurrences(got, mainDir); n != 0 {
		t.Errorf("count of %q = %d, want 0\n%s", mainDir, n, got)
	}

	got, err = callTool(t, reg, mainDir, Input{Action: "source", Symbol: "wtonly", File: file})
	if err != nil {
		t.Fatalf("source wtonly: %v", err)
	}
	if n := countOccurrences(got, wtDir+"/a/wtonly.go"); n != 1 {
		t.Errorf("count of %q = %d, want 1: sourceAction renders the resolved symbol's own file\n%s", wtDir+"/a/wtonly.go", n, got)
	}

	_, err = callTool(t, reg, mainDir, Input{Action: "definition", Symbol: "absent", File: file})
	if err == nil {
		t.Fatal("definition of an unresolvable symbol should fail")
	}
	want := fmt.Sprintf("symbol %q not found in %s (pass file=<a path inside the project you mean> to query a different project)", "absent", wtDir)
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestIntegration_LspTool_WorkspaceSymbolResolvedOnlyInWorktreeRoot(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()

	got, err := callTool(t, reg, mainDir, Input{
		Action: "workspace_symbol", Query: "wtonly", File: filepath.Join(wtDir, "a", "use.go"),
	})
	if err != nil {
		t.Fatalf("workspace_symbol: %v", err)
	}
	if n := countOccurrences(got, `Found 1 symbol(s) matching "wtonly":`); n != 1 {
		t.Errorf("count of the found-header = %d, want 1\n%s", n, got)
	}
	if n := countOccurrences(got, "No symbols matching"); n != 0 {
		t.Errorf("count of 'No symbols matching' = %d, want 0\n%s", n, got)
	}
}

func TestResolveInWorkspace_RootScopedBranches(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()
	ctx := context.Background()

	mainPkg := lsp.FileToURI(filepath.Join(mainDir, "a", "pkg.go"))
	wtPkg := lsp.FileToURI(filepath.Join(wtDir, "a", "pkg.go"))

	uri, _, err := resolveInWorkspace(ctx, reg, "target", 1, mainDir, wtDir, ".go")
	if err != nil {
		t.Fatalf("ext=.go: %v", err)
	}
	if uri != wtPkg {
		t.Errorf("uri = %q, want %q: a .go query with a foreign root must be answered by the worktree server", uri, wtPkg)
	}

	uri, _, err = resolveInWorkspace(ctx, reg, "target", 1, mainDir, wtDir, ".rs")
	if err != nil {
		t.Fatalf("ext=.rs: %v", err)
	}
	if uri != mainPkg {
		t.Errorf("uri = %q, want %q: no configured spec claims .rs, so the spec list must stay at the default root, never rooted at %s", uri, mainPkg, wtDir)
	}

	uri, _, err = resolveInWorkspace(ctx, reg, "target", 1, mainDir, wtDir, "")
	if err != nil {
		t.Fatalf(`ext="": %v`, err)
	}
	if uri != mainPkg {
		t.Errorf("uri = %q, want %q: with no extension the query must not fan out to the foreign root even when one is supplied", uri, mainPkg)
	}
}
