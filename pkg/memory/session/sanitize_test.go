package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeNotes_DropsStrayHeaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notesPath := filepath.Join(dir, "SESSION_NOTES.md")

	content := `# Session Notes

## Session Title
Test session

## Current State
Working

## LATEST
Should be dropped

## Reference
Should be dropped

## Worklog
- 2026-06-19 10:00 — did stuff
`
	if err := os.WriteFile(notesPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SanitizeNotes(notesPath); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}

	result, _ := os.ReadFile(notesPath)
	s := string(result)

	if !contains(s, "## Session Title") {
		t.Error("should keep Session Title")
	}
	if !contains(s, "## Current State") {
		t.Error("should keep Current State")
	}
	if !contains(s, "## Worklog") {
		t.Error("should keep Worklog")
	}
	if contains(s, "## LATEST") {
		t.Error("should drop LATEST")
	}
	if contains(s, "## Reference") {
		t.Error("should drop Reference")
	}
}

func TestSanitizeNotes_MergesDuplicateHeaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notesPath := filepath.Join(dir, "SESSION_NOTES.md")

	content := `# Session Notes

## Worklog
- first entry

## Errors
old errors

## Worklog
- second entry
`
	if err := os.WriteFile(notesPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SanitizeNotes(notesPath); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}

	result, _ := os.ReadFile(notesPath)
	s := string(result)

	// Only one ## Worklog section.
	count := countSubstring(s, "## Worklog")
	if count != 1 {
		t.Errorf("expected 1 Worklog section, got %d", count)
	}
	// Both entries preserved.
	if !contains(s, "first entry") {
		t.Error("should preserve first entry")
	}
	if !contains(s, "second entry") {
		t.Error("should preserve second entry")
	}
	// Errors (alias) merged into Errors & Corrections.
	if !contains(s, "## Errors & Corrections") {
		t.Error("should have canonical Errors & Corrections")
	}
	if !contains(s, "old errors") {
		t.Error("should preserve errors content")
	}
}

func TestSanitizeNotes_NoChangeOnCleanFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notesPath := filepath.Join(dir, "SESSION_NOTES.md")

	content := `# Session Notes

## Session Title

Test

## Current State

Working
`
	if err := os.WriteFile(notesPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SanitizeNotes(notesPath); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}

	result, _ := os.ReadFile(notesPath)
	// Compare ignoring trailing whitespace differences.
	if trimTrailingNewlines(string(result)) != trimTrailingNewlines(content) {
		t.Errorf("clean file should not change\ngot:\n%q\nwant:\n%q", string(result), content)
	}
}

func trimTrailingNewlines(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func countSubstring(s, substr string) int {
	count := 0
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			count++
		}
	}
	return count
}
