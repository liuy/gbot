package lsptool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func TestFilterGitIgnored_RealRepo(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("vendor/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	vendorDir := filepath.Join(dir, "vendor")
	if err := os.MkdirAll(vendorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendorDir, "lib.go"), []byte("package vendor"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	mainURI := lsp.FileToURI(filepath.Join(dir, "main.go"))
	vendorURI := lsp.FileToURI(filepath.Join(vendorDir, "lib.go"))

	locs := []lsp.Location{
		{URI: mainURI},
		{URI: vendorURI},
	}
	filtered := filterGitIgnored(context.Background(), locs, dir)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 location after filter, got %d", len(filtered))
	}
	if filtered[0].URI != mainURI {
		t.Errorf("expected main.go to survive, got %s", filtered[0].URI)
	}
}

func TestFilterGitIgnored_NotInRepo(t *testing.T) {
	dir := t.TempDir()
	locs := []lsp.Location{
		{URI: lsp.FileToURI(filepath.Join(dir, "a.go"))},
	}
	filtered := filterGitIgnored(context.Background(), locs, dir)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 location (not a repo), got %d", len(filtered))
	}
}

func TestFilterGitIgnored_EmptyInput(t *testing.T) {
	filtered := filterGitIgnored(context.Background(), nil, "/test")
	if filtered != nil {
		t.Errorf("expected nil for empty input, got %v", filtered)
	}
}

func TestCheckGitIgnore_NoRepo(t *testing.T) {
	dir := t.TempDir()
	ignored := checkGitIgnore(context.Background(), []string{filepath.Join(dir, "x.go")}, dir)
	if len(ignored) != 0 {
		t.Errorf("expected 0 ignored in non-repo, got %d", len(ignored))
	}
}

func TestCheckGitIgnore_Batch(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	var paths []string
	for i := range 55 {
		p := filepath.Join(dir, "file"+string(rune('a'+i%26))+string(rune('0'+i/26))+".go")
		paths = append(paths, p)
	}
	ignored := checkGitIgnore(context.Background(), paths, dir)
	if len(ignored) != 0 {
		t.Errorf("expected 0 ignored, got %d", len(ignored))
	}
}

func TestFilterGitIgnoredCallers_RealRepo(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("vendor/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	vendorDir := filepath.Join(dir, "vendor")
	if err := os.MkdirAll(vendorDir, 0755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.go")
	vendorPath := filepath.Join(vendorDir, "lib.go")
	if err := os.WriteFile(mainPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vendorPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	calls := []callerEntry{
		{From: callHierarchyItem{URI: lsp.FileToURI(mainPath)}},
		{From: callHierarchyItem{URI: lsp.FileToURI(vendorPath)}},
	}
	filtered := filterGitIgnoredCallers(context.Background(), calls, dir)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 caller after git-ignore, got %d", len(filtered))
	}
}

func TestFilterGitIgnoredCallees_RealRepo(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("vendor/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	vendorDir := filepath.Join(dir, "vendor")
	if err := os.MkdirAll(vendorDir, 0755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.go")
	vendorPath := filepath.Join(vendorDir, "lib.go")
	if err := os.WriteFile(mainPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vendorPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	calls := []calleeEntry{
		{To: callHierarchyItem{URI: lsp.FileToURI(mainPath)}},
		{To: callHierarchyItem{URI: lsp.FileToURI(vendorPath)}},
	}
	filtered := filterGitIgnoredCallees(context.Background(), calls, dir)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 callee after git-ignore, got %d", len(filtered))
	}
}

func TestFilterGitIgnoredCallers_NoFilter(t *testing.T) {
	dir := t.TempDir()
	calls := []callerEntry{
		{From: callHierarchyItem{URI: "file:///a.go"}},
	}
	filtered := filterGitIgnoredCallers(context.Background(), calls, dir)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 caller (no filter), got %d", len(filtered))
	}
}

func TestFilterGitIgnoredCallees_NoFilter(t *testing.T) {
	dir := t.TempDir()
	calls := []calleeEntry{
		{To: callHierarchyItem{URI: "file:///a.go"}},
	}
	filtered := filterGitIgnoredCallees(context.Background(), calls, dir)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 callee (no filter), got %d", len(filtered))
	}
}
