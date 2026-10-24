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

// Reading a missing file must surface the error, not silently rewrite.
func TestSanitizeNotes_ReadErrorPropagates(t *testing.T) {
	t.Parallel()
	err := SanitizeNotes(filepath.Join(t.TempDir(), "SESSION_NOTES.md"))
	if err == nil {
		t.Fatal("missing notes file must fail the sanitize pass")
	}
	if !os.IsNotExist(err) {
		t.Errorf("error = %v, want a not-exist error", err)
	}
}

// Every stray-header alias must land in its canonical section with its body;
// a dropped alias silently loses that section's content.
func TestSanitizeNotes_HeaderAliases(t *testing.T) {
	t.Parallel()
	aliases := map[string]string{
		"task specification":                "Task specification",
		"files and functions":               "Files and Functions",
		"files & functions":                 "Files and Functions",
		"key files & functions":             "Files and Functions",
		"errors":                            "Errors & Corrections",
		"errors and corrections":            "Errors & Corrections",
		"codebase":                          "Codebase and System Documentation",
		"codebase documentation":            "Codebase and System Documentation",
		"codebase and system documentation": "Codebase and System Documentation",
		"learnings":                         "Learnings",
		"key learnings":                     "Learnings",
		"key learnings (this session)":      "Learnings",
		"side discoveries":                  "Side Discoveries",
		"workflow":                          "Workflow",
		"worklog":                           "Worklog",
		"worklog (recent)":                  "Worklog",
		"worklog (2026-06-19, recent)":      "Worklog",
		"worklog (recent only — full history in git log)": "Worklog",
		"session title":                     "Session Title",
		"key results":                       "Key results",
		"sub-agent event propagation chain": "Learnings",
	}
	for alias, canonical := range aliases {
		t.Run(alias, func(t *testing.T) {
			t.Parallel()
			notesPath := filepath.Join(t.TempDir(), "SESSION_NOTES.md")
			body := "body for " + alias
			content := "# Session Notes\n\n## " + alias + "\n" + body + "\n"
			if err := os.WriteFile(notesPath, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := SanitizeNotes(notesPath); err != nil {
				t.Fatalf("SanitizeNotes: %v", err)
			}
			result, err := os.ReadFile(notesPath)
			if err != nil {
				t.Fatal(err)
			}
			s := string(result)
			if !contains(s, "## "+canonical+"\n") {
				t.Errorf("alias %q did not resolve to canonical section %q:\n%s", alias, canonical, s)
			}
			if !contains(s, body) {
				t.Errorf("body under alias %q was lost:\n%s", alias, s)
			}
		})
	}
}

// Date suffixes, parenthetical notes, and trailing whitespace are stripped
// from headers before resolution; without stripping the section is dropped.
func TestSanitizeNotes_StripsHeaderSuffixes(t *testing.T) {
	t.Parallel()
	cases := []struct{ header, canonical string }{
		{"Worklog — 2026-06-19", "Worklog"},
		{"Learnings (today)", "Learnings"},
		{"Learnings（今天）", "Learnings"},
		{"Worklog  ", "Worklog"},
	}
	for _, c := range cases {
		t.Run(c.header, func(t *testing.T) {
			t.Parallel()
			notesPath := filepath.Join(t.TempDir(), "SESSION_NOTES.md")
			content := "# Session Notes\n\n## " + c.header + "\nsuffix body\n"
			if err := os.WriteFile(notesPath, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := SanitizeNotes(notesPath); err != nil {
				t.Fatalf("SanitizeNotes: %v", err)
			}
			result, err := os.ReadFile(notesPath)
			if err != nil {
				t.Fatal(err)
			}
			s := string(result)
			if !contains(s, "## "+c.canonical+"\n") {
				t.Errorf("header %q did not resolve to %q:\n%s", c.header, c.canonical, s)
			}
			if !contains(s, "suffix body") {
				t.Errorf("body under header %q was lost:\n%s", c.header, s)
			}
		})
	}
}

// A section with an empty body is dropped, and scanning continues with the
// sections after it.
func TestSanitizeNotes_DropsEmptyBodySections(t *testing.T) {
	t.Parallel()
	notesPath := filepath.Join(t.TempDir(), "SESSION_NOTES.md")
	content := "# Session Notes\n\n## Worklog\n\n## Learnings\nreal lesson\n"
	if err := os.WriteFile(notesPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SanitizeNotes(notesPath); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}
	result, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(result)
	if contains(s, "## Worklog") {
		t.Errorf("empty-body section should be dropped:\n%s", s)
	}
	if !contains(s, "## Learnings") || !contains(s, "real lesson") {
		t.Errorf("section after the empty one was lost:\n%s", s)
	}
}
