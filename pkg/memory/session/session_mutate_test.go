package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The shipped defaults are the tuning contract with the SM agent.
func TestDefaultConfig_AllFields(t *testing.T) {
	t.Parallel()
	c := DefaultConfig()
	if c.MinTokensToInit != 10000 {
		t.Errorf("MinTokensToInit = %d, want 10000", c.MinTokensToInit)
	}
	if c.MinTokensBetweenUpdate != 50000 {
		t.Errorf("MinTokensBetweenUpdate = %d, want 50000", c.MinTokensBetweenUpdate)
	}
	if c.ToolCallsBetweenUpdates != 50 {
		t.Errorf("ToolCallsBetweenUpdates = %d, want 50", c.ToolCallsBetweenUpdates)
	}
	if c.MaxSectionTokens != 2000 {
		t.Errorf("MaxSectionTokens = %d, want 2000", c.MaxSectionTokens)
	}
	if c.MaxTotalTokens != 12000 {
		t.Errorf("MaxTotalTokens = %d, want 12000", c.MaxTotalTokens)
	}
	if c.ExtractionTimeoutMs != 15000 {
		t.Errorf("ExtractionTimeoutMs = %d, want 15000", c.ExtractionTimeoutMs)
	}
	if c.ExtractionStaleMs != 60000 {
		t.Errorf("ExtractionStaleMs = %d, want 60000", c.ExtractionStaleMs)
	}
	if c.MinIntervalMs != 600000 {
		t.Errorf("MinIntervalMs = %d, want 600000", c.MinIntervalMs)
	}
}

func TestAnalyzeSectionSizes_Shapes(t *testing.T) {
	t.Parallel()
	// Empty content still yields one (unnamed, empty) section.
	got := analyzeSectionSizes("")
	if len(got) != 1 || got[0].Header != "" || got[0].Tokens != 0 {
		t.Errorf("analyzeSectionSizes(\"\") = %+v, want one empty unnamed section", got)
	}
	// A trailing header with no body still yields its section.
	got = analyzeSectionSizes("## Alpha\n")
	if len(got) != 1 || got[0].Header != "Alpha" || got[0].Tokens != 0 {
		t.Errorf("trailing header = %+v, want one Alpha section with 0 tokens", got)
	}
	// Preamble before the first header becomes its own unnamed section.
	got = analyzeSectionSizes("preamble line\n## Alpha\nbody text here")
	if len(got) != 2 {
		t.Fatalf("preamble sections = %+v, want 2 (unnamed preamble + Alpha)", got)
	}
	if got[0].Header != "" {
		t.Errorf("first section header = %q, want the unnamed preamble", got[0].Header)
	}
	if got[0].Tokens == 0 {
		t.Error("preamble section must carry its token count")
	}
	if got[1].Header != "Alpha" || got[1].Tokens == 0 {
		t.Errorf("second section = %+v, want Alpha with tokens", got[1])
	}
	// Two headers, last one empty-bodied.
	got = analyzeSectionSizes("## A\none two three\n## B\n")
	if len(got) != 2 || got[0].Header != "A" || got[1].Header != "B" || got[1].Tokens != 0 {
		t.Errorf("two sections = %+v, want A (tokens) and B (0 tokens)", got)
	}
}

// TruncateForCompact budgets 4 characters per section token and cuts at the
// last newline before the budget.
func TestTruncateForCompact_Exact(t *testing.T) {
	t.Parallel()
	const marker = "\n[... section truncated for length ...]"

	// maxChars = 10 tokens * 4 = 40. Body of 45 chars with a newline at 30.
	body := strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 14)
	got := TruncateForCompact("## Sec\n"+body, 10)
	want := "## Sec\n" + strings.Repeat("a", 30) + marker
	if got != want {
		t.Errorf("truncate at newline = %q, want %q", got, want)
	}

	// No newline inside the budget: hard cut at maxChars.
	body = strings.Repeat("c", 45)
	got = TruncateForCompact("## Sec\n"+body, 10)
	want = "## Sec\n" + strings.Repeat("c", 40) + marker
	if got != want {
		t.Errorf("hard cut = %q, want %q", got, want)
	}

	// A leading newline puts the cut at index 0, which must fall back to the
	// hard cut rather than emitting an empty section.
	body = "\n" + strings.Repeat("d", 44)
	got = TruncateForCompact("## Sec\n"+body, 10)
	want = "## Sec\n" + "\n" + strings.Repeat("d", 39) + marker
	if got != want {
		t.Errorf("newline at index 0 = %q, want %q", got, want)
	}

	// Exactly at the budget: untouched.
	body = strings.Repeat("e", 40)
	got = TruncateForCompact("## Sec\n"+body, 10)
	if got != "## Sec\n"+body {
		t.Errorf("at-budget section = %q, want it untouched", got)
	}
}

// SanitizeNotes normalizes aliases, merges duplicates, drops strays, and
// rewrites even when the first header starts the file.
func TestSanitizeNotes_Rewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SESSION_NOTES.md")
	content := "# Session Notes\n\n## Current state\nfirst body\n\n## Current State\nsecond body\n\n## learnings\nlearned thing\n\n## reference\nshould be dropped\n\n## Empty Section\n\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SanitizeNotes(path); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Session Notes\n" +
		"\n## Current State\n\nfirst body\n\nsecond body\n" +
		"\n## Learnings\n\nlearned thing\n"
	if string(data) != want {
		t.Errorf("sanitized file =\n%q\nwant\n%q", data, want)
	}
}

func TestSanitizeNotes_LeadingHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SESSION_NOTES.md")
	// The first "\n## " occurs at index 0: the file still gets normalized.
	content := "\n## current state\nbody text\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SanitizeNotes(path); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "\n\n## Current State\n\nbody text\n" {
		t.Errorf("leading-header file = %q, want the canonicalized header", data)
	}
}

func TestSanitizeNotes_NoHeadersNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SESSION_NOTES.md")
	content := "just prose, no headers at all\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SanitizeNotes(path); err != nil {
		t.Fatalf("SanitizeNotes: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("headerless file rewritten: %q", data)
	}
}

// The minimum-interval gate only applies when configured and an extraction
// has already happened.
func TestShouldExtract_MinIntervalGate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinTokensToInit = 0
	cfg.MinTokensBetweenUpdate = 10
	cfg.ToolCallsBetweenUpdates = 0
	sm := New(cfg, t.TempDir(), "engine", nil, nil)

	// No prior extraction: the interval gate is skipped.
	if !sm.ShouldExtract(1000, nil) {
		t.Fatal("first extraction past the token threshold must be allowed")
	}

	// Recent extraction inside the interval: blocked.
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	sm.lastExtractionTime = time.Now()
	if sm.ShouldExtract(5000, nil) {
		t.Error("extraction inside MinIntervalMs must be blocked")
	}

	// Old enough: allowed again.
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	sm.lastExtractionTime = time.Now().Add(-time.Duration(cfg.MinIntervalMs)*time.Millisecond - time.Second)
	if !sm.ShouldExtract(9000, nil) {
		t.Error("extraction past MinIntervalMs must be allowed")
	}

	// Gate disabled: a recent extraction no longer blocks.
	sm2 := New(Config{MinTokensBetweenUpdate: 10, ExtractionStaleMs: 60000}, t.TempDir(), "engine", nil, nil)
	sm2.initialized = true
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	sm2.lastExtractionTime = time.Now()
	if !sm2.ShouldExtract(5000, nil) {
		t.Error("MinIntervalMs = 0 must disable the interval gate")
	}
}
