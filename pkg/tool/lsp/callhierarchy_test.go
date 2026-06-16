package lsptool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

func testCtx() context.Context { return context.Background() }

func TestSymbolKindStr(t *testing.T) {
	tests := []struct {
		kind int
		want string
	}{
		{12, "Function"},
		{6, "Method"},
		{5, "Class"},
		{23, "Struct"},
		{11, "Interface"},
		{999, "Kind(999)"},
	}
	for _, tt := range tests {
		if got := symbolKindStr(tt.kind); got != tt.want {
			t.Errorf("symbolKindStr(%d) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestFormatRanges(t *testing.T) {
	tests := []struct {
		name string
		in   []lsp.Range
		want string
	}{
		{"empty", nil, ""},
		{"single", []lsp.Range{{Start: lsp.Position{Line: 4, Character: 9}}}, "5:10"},
		{"multiple", []lsp.Range{
			{Start: lsp.Position{Line: 4, Character: 9}},
			{Start: lsp.Position{Line: 9, Character: 0}},
		}, "5:10, 10:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatRanges(tt.in); got != tt.want {
				t.Errorf("formatRanges() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]int{"zebra": 3, "apple": 1, "mango": 2}
	got := sortedKeys(m)
	want := []string{"apple", "mango", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("sortedKeys: got %d keys, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sortedKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGroupCallersByFile(t *testing.T) {
	calls := []callerEntry{
		{From: callHierarchyItem{URI: "file:///a.go", Name: "funcA"}},
		{From: callHierarchyItem{URI: "file:///b.go", Name: "funcB"}},
		{From: callHierarchyItem{URI: "file:///a.go", Name: "funcC"}},
	}
	got := groupCallersByFile(calls, "/wd")
	if len(got) != 2 {
		t.Fatalf("groupCallersByFile: got %d files, want 2", len(got))
	}
}

func TestGroupCalleesByFile(t *testing.T) {
	calls := []calleeEntry{
		{To: callHierarchyItem{URI: "file:///x.go", Name: "funcX"}},
		{To: callHierarchyItem{URI: "file:///x.go", Name: "funcY"}},
	}
	got := groupCalleesByFile(calls, "/wd")
	if len(got) != 1 {
		t.Fatalf("groupCalleesByFile: got %d files, want 1", len(got))
	}
	total := 0
	for _, calls := range got {
		total += len(calls)
	}
	if total != 2 {
		t.Errorf("groupCalleesByFile: got %d total calls, want 2", total)
	}
}

func TestFilterGitIgnoredCallers_AllKept(t *testing.T) {
	calls := []callerEntry{
		{From: callHierarchyItem{URI: "file:///tmp/nonrepo/a.go"}},
		{From: callHierarchyItem{URI: "file:///tmp/nonrepo/b.go"}},
	}
	got := filterGitIgnoredCallers(testCtx(), calls, "/tmp/nonrepo")
	if len(got) != 2 {
		t.Errorf("filterGitIgnoredCallers in non-git dir: got %d, want 2", len(got))
	}
}

func TestFilterGitIgnoredCallees_AllKept(t *testing.T) {
	calls := []calleeEntry{
		{To: callHierarchyItem{URI: "file:///tmp/nonrepo/x.go"}},
	}
	got := filterGitIgnoredCallees(testCtx(), calls, "/tmp/nonrepo")
	if len(got) != 1 {
		t.Errorf("filterGitIgnoredCallees in non-git dir: got %d, want 1", len(got))
	}
}

// --- "no results" integration error paths ---

func TestIntegration_CallHierarchy_NoResults(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "callers", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No call hierarchy") {
		t.Errorf("expected 'No call hierarchy': %q", got)
	}
}

func TestIntegration_Callees_NoResults(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "callees", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No call hierarchy") {
		t.Errorf("expected 'No call hierarchy': %q", got)
	}
}
