package lsp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeModuleTree writes the same three-file layout into both trees so a
// location leak from one to the other is detectable by path alone, not by count.
func writeModuleTree(t *testing.T, root string) {
	t.Helper()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write("go.mod", "module example.com/proj\n\ngo 1.27\n")
	write("a/pkg.go", "package a\n\nfunc Target() {}\n")
	write("a/use.go", "package a\n\nfunc UseA() {\n\tTarget()\n\tTarget()\n}\n")
	write("c/use.go", "package c\n\nimport \"example.com/proj/a\"\n\nfunc UseC() {\n\ta.Target()\n}\n")
}

func TestIntegration_Gopls_WorktreeModule(t *testing.T) {
	base := t.TempDir()
	mainDir := filepath.Join(base, "main")
	wtDir := filepath.Join(base, "wt")
	for _, dir := range []string{mainDir, wtDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		writeModuleTree(t, dir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	reg := NewRegistry(mainDir)
	defer reg.Shutdown(ctx)
	reg.Scan([]ServerSpec{{
		Name:     "gopls",
		Command:  "gopls",
		Args:     []string{"-rpc.trace"},
		FileExts: []string{".go"},
		Language: "Go",
	}})
	if reg.NumServers() != 1 {
		t.Skip("gopls not on PATH — real-server integration test skipped")
	}

	wtUse := filepath.Join(wtDir, "a", "use.go")
	wtUseURI := FileToURI(wtUse)
	callPos := Position{Line: 3, Character: 1}

	cMain, err := reg.ForFile(ctx, filepath.Join(mainDir, "a", "use.go"))
	if err != nil {
		t.Fatalf("ForFile(main): %v", err)
	}
	if cMain == nil {
		t.Fatal("ForFile(main) returned a nil client")
	}

	cWt, err := reg.ForFile(ctx, wtUse)
	if err != nil {
		t.Fatalf("ForFile(worktree): %v", err)
	}
	if cWt == nil {
		t.Fatal("ForFile(worktree) returned a nil client")
	}
	if cWt == cMain {
		t.Fatal("the worktree reused the launch-workspace gopls: one process cannot index two trees")
	}

	data, err := os.ReadFile(wtUse)
	if err != nil {
		t.Fatalf("read %s: %v", wtUse, err)
	}
	if err := cWt.EnsureFileOpen(ctx, wtUseURI, DetectLanguage(wtUse), string(data)); err != nil {
		t.Fatalf("EnsureFileOpen: %v", err)
	}

	syms, err := WorkspaceSymbol(ctx, cWt, "Target")
	if err != nil {
		t.Fatalf("WorkspaceSymbol: %v", err)
	}
	var targetSyms []SymbolInformation
	for _, s := range syms {
		if s.Name == "Target" {
			targetSyms = append(targetSyms, s)
		}
	}
	if len(targetSyms) != 1 {
		t.Fatalf("symbols named Target = %d, want 1 (of %d returned)", len(targetSyms), len(syms))
	}
	if got := URItoPath(targetSyms[0].Location.URI); got != filepath.Join(wtDir, "a", "pkg.go") {
		t.Errorf("workspace/symbol resolved to %q, want the worktree's pkg.go", got)
	}

	locs, err := Definition(ctx, cWt, wtUseURI, callPos)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("len(definition locations) = %d, want 1", len(locs))
	}
	if got := URItoPath(locs[0].URI); got != filepath.Join(wtDir, "a", "pkg.go") {
		t.Errorf("definition = %q, want the worktree's pkg.go", got)
	}
	if locs[0].Range.Start != (Position{Line: 2, Character: 5}) {
		t.Errorf("definition position = %+v, want {Line:2 Character:5}", locs[0].Range.Start)
	}

	refs, err := References(ctx, cWt, wtUseURI, callPos)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) != 4 {
		t.Fatalf("len(references) = %d, want 4", len(refs))
	}
	gotPaths := make([]string, 0, len(refs))
	for _, loc := range refs {
		p := URItoPath(loc.URI)
		if !strings.HasPrefix(p, wtDir+"/") {
			t.Errorf("reference %q is outside the worktree root %q", p, wtDir)
		}
		gotPaths = append(gotPaths, p)
	}
	wantPaths := []string{
		filepath.Join(wtDir, "a", "pkg.go"),
		filepath.Join(wtDir, "a", "use.go"),
		filepath.Join(wtDir, "a", "use.go"),
		filepath.Join(wtDir, "c", "use.go"),
	}
	slices.Sort(gotPaths)
	slices.Sort(wantPaths)
	if !slices.Equal(gotPaths, wantPaths) {
		t.Errorf("references = %v, want %v", gotPaths, wantPaths)
	}

	live := reg.LiveServers()
	if len(live) != 2 {
		t.Fatalf("len(LiveServers) = %d, want 2", len(live))
	}
	if live[0].Root != mainDir || live[1].Root != wtDir {
		t.Errorf("LiveServers roots = [%q, %q], want [%q, %q] (default root first)",
			live[0].Root, live[1].Root, mainDir, wtDir)
	}
	for i, ls := range live {
		if ls.Spec.Name != "gopls" {
			t.Errorf("LiveServers()[%d].Spec.Name = %q, want gopls", i, ls.Spec.Name)
		}
	}
}
