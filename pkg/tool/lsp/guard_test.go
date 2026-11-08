package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/lsp"
)

// TestEnsureFileOpenWithGuard_AlreadyOpen covers the early-return branch.
func TestEnsureFileOpenWithGuard_AlreadyOpen(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	src := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	specs := reg.Snapshot()
	c, err := reg.ForSpec(context.Background(), specs[0])
	if err != nil {
		t.Fatalf("ForSpec: %v", err)
	}
	uri := lsp.FileToURI(src)
	// First call opens the file.
	if err := ensureFileOpenWithGuard(context.Background(), c, uri, "go", src); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if !c.IsFileOpen(uri) {
		t.Fatal("file should be open after first call")
	}
	// Second call is a no-op (already open).
	if err := ensureFileOpenWithGuard(context.Background(), c, uri, "go", src); err != nil {
		t.Errorf("second open should be no-op, got: %v", err)
	}
}

// TestEnsureFileOpenWithGuard_StatError covers the os.Stat failure branch.
func TestEnsureFileOpenWithGuard_StatError(t *testing.T) {
	reg, _, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	specs := reg.Snapshot()
	c, err := reg.ForSpec(context.Background(), specs[0])
	if err != nil {
		t.Fatalf("ForSpec: %v", err)
	}
	missing := "/nonexistent/file.go"
	err = ensureFileOpenWithGuard(context.Background(), c, lsp.FileToURI(missing), "go", missing)
	if err == nil || !strings.Contains(err.Error(), "stat file") {
		t.Fatalf("expected 'stat file' error, got: %v", err)
	}
}

// TestEnsureFileOpenWithGuard_TooLarge covers the >10MB rejection branch.
func TestEnsureFileOpenWithGuard_TooLarge(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	big := filepath.Join(dir, "big.go")
	// Create a sparse 11MB file — seek past 10MB then write 1 byte.
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(11*1024*1024, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(" ")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	specs := reg.Snapshot()
	c, err := reg.ForSpec(context.Background(), specs[0])
	if err != nil {
		t.Fatalf("ForSpec: %v", err)
	}
	err = ensureFileOpenWithGuard(context.Background(), c, lsp.FileToURI(big), "go", big)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected 'too large' error, got: %v", err)
	}
}

// TestWaitForProjectLoaded_NormalExit covers the time.After branch —
// uncancelled ctx returns nil without error.
func TestWaitForProjectLoaded_NormalExit(t *testing.T) {
	done := make(chan struct{})
	go func() {
		err := waitForProjectLoaded(context.Background())
		if err != nil {
			t.Errorf("waitForProjectLoaded returned %v, want nil", err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForProjectLoaded did not return within 2s")
	}
}

// TestWaitForProjectLoaded_Cancelled covers the ctx.Done() branch.
func TestWaitForProjectLoaded_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	err := waitForProjectLoaded(ctx)
	if err == nil {
		t.Fatalf("waitForProjectLoaded on cancelled ctx returned nil, want non-nil error")
	}
}

// TestIsOnlyQueriedDeclaration_CoversAllBranches covers all 3 return paths.
func TestIsOnlyQueriedDeclaration_CoversAllBranches(t *testing.T) {
	uri := "file:///foo.go"
	pos := lsp.Position{Line: 1, Character: 5}
	rng := lsp.Range{Start: lsp.Position{Line: 1, Character: 4}, End: lsp.Position{Line: 1, Character: 8}}

	// len != 1 → false
	if isOnlyQueriedDeclaration(nil, uri, pos) {
		t.Error("nil slice should return false")
	}
	if isOnlyQueriedDeclaration([]lsp.Location{{}, {}}, uri, pos) {
		t.Error("2-element slice should return false")
	}

	// URI mismatch → false
	if isOnlyQueriedDeclaration([]lsp.Location{{URI: "file:///other.go", Range: rng}}, uri, pos) {
		t.Error("URI mismatch should return false")
	}

	// Same URI, position in range → true
	if !isOnlyQueriedDeclaration([]lsp.Location{{URI: uri, Range: rng}}, uri, pos) {
		t.Error("exact declaration match should return true")
	}

	// Same URI, position out of range → false
	outOfRange := lsp.Position{Line: 5, Character: 0}
	if isOnlyQueriedDeclaration([]lsp.Location{{URI: uri, Range: rng}}, uri, outOfRange) {
		t.Error("position out of range should return false")
	}
}

// TestRangeContainsPosition covers the boundary checks.
func TestRangeContainsPosition(t *testing.T) {
	rng := lsp.Range{
		Start: lsp.Position{Line: 1, Character: 5},
		End:   lsp.Position{Line: 3, Character: 10},
	}
	if !rangeContainsPosition(rng, lsp.Position{Line: 1, Character: 5}) {
		t.Error("start boundary should be in range")
	}
	if !rangeContainsPosition(rng, lsp.Position{Line: 3, Character: 10}) {
		t.Error("end boundary should be in range")
	}
	if !rangeContainsPosition(rng, lsp.Position{Line: 2, Character: 0}) {
		t.Error("midpoint should be in range")
	}
	if rangeContainsPosition(rng, lsp.Position{Line: 1, Character: 4}) {
		t.Error("before start should NOT be in range")
	}
	if rangeContainsPosition(rng, lsp.Position{Line: 3, Character: 11}) {
		t.Error("after end should NOT be in range")
	}
}

// TestIsProjectAwareLspServer covers both true/false outcomes.
func TestIsProjectAwareLspServer(t *testing.T) {
	if !isProjectAwareLspServer(lsp.ServerSpec{Name: "gopls"}) {
		t.Error("non-linter should be project-aware")
	}
	if isProjectAwareLspServer(lsp.ServerSpec{Name: "biome", IsLinter: true}) {
		t.Error("linter should NOT be project-aware")
	}
}

// TestParseSymbolOccurrence_Edges pins the #N splitting rules.
func TestParseSymbolOccurrence_Edges(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantOcc  int
	}{
		{"foo#2", "foo", 2},
		{"a#2", "a", 2},
		{"#2", "#2", 1},       // separator at index 0 is not an occurrence marker
		{"foo#0", "foo#0", 1}, // zero is not a valid occurrence
		{"foo#x", "foo#x", 1}, // non-numeric suffix is part of the name
		{"foo", "foo", 1},
	}
	for _, c := range cases {
		name, occ := parseSymbolOccurrence(c.in)
		if name != c.wantName || occ != c.wantOcc {
			t.Errorf("parseSymbolOccurrence(%q) = (%q, %d), want (%q, %d)",
				c.in, name, occ, c.wantName, c.wantOcc)
		}
	}
}

func TestSymbolNameMatches_Qualified(t *testing.T) {
	if !symbolNameMatches("Store.Load", "Load") {
		t.Error("qualified last component must match")
	}
	if !symbolNameMatches("(*mockEngine).Messages", "Messages") {
		t.Error("method-receiver form must match")
	}
	if symbolNameMatches("Store.Load", "Store") {
		t.Error("only the last component matches")
	}
	if !symbolNameMatches("Load", "Load") {
		t.Error("exact match must match")
	}
	if symbolNameMatches("Store.Load", "Other") {
		t.Error("different name must not match")
	}
}

// TestReferencesRetryCount pins the configured retry budget: two extra
// attempts are enough to ride out two declaration-only answers.
func TestReferencesRetryCount(t *testing.T) {
	// Pattern of server answers: declaration-only twice, then real refs.
	// With referencesRetryCount=2 (3 attempts) the real refs surface.
	calls := 0
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				calls++
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				if calls <= 2 {
					// Declaration-only answer: the range must actually contain
					// the queried position for the retry logic to classify it.
					return []any{map[string]any{
						"uri": a,
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 4},
							"end":   map[string]any{"line": 0, "character": 7},
						},
					}}, true
				}
				return []any{
					map[string]any{"uri": a, "range": map[string]any{
						"start": map[string]any{"line": 1, "character": 0},
						"end":   map[string]any{"line": 1, "character": 3},
					}},
					map[string]any{"uri": a, "range": map[string]any{
						"start": map[string]any{"line": 2, "character": 0},
						"end":   map[string]any{"line": 2, "character": 3},
					}},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\nfunc c() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels"})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	got := fmt.Sprintf("%v", res.Data)
	if !strings.HasPrefix(got, "Found 2 reference(s):") {
		t.Errorf("expected the 3rd attempt's real refs, got:\n%s", got)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

// TestReferencesRetryCountFourthAttempt pins the retry budget at exactly two
// extra attempts: three declaration-only answers exceed it.
func TestReferencesRetryCountFourthAttempt(t *testing.T) {
	calls := 0
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				calls++
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				if calls <= 3 {
					return []any{map[string]any{
						"uri": a,
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 4},
							"end":   map[string]any{"line": 0, "character": 7},
						},
					}}, true
				}
				return []any{
					map[string]any{"uri": a, "range": map[string]any{
						"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 3}}},
					map[string]any{"uri": a, "range": map[string]any{
						"start": map[string]any{"line": 2, "character": 0}, "end": map[string]any{"line": 2, "character": 3}}},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\nfunc c() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels"})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	got := fmt.Sprintf("%v", res.Data)
	// Budget exhausted at 3 attempts: the declaration-only answer stands.
	if !strings.HasPrefix(got, "Found 1 reference(s):") {
		t.Errorf("expected the declaration to stand, got:\n%s", got)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}
