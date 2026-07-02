package session

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// --- IsSessionMemoryEmpty ---

func TestIsSessionMemoryEmpty_EmptyString(t *testing.T) {
	t.Parallel()
	if !IsSessionMemoryEmpty("") {
		t.Error("empty string should be considered empty")
	}
}

func TestIsSessionMemoryEmpty_TemplateOnly(t *testing.T) {
	t.Parallel()
	if !IsSessionMemoryEmpty(DefaultTemplate) {
		t.Error("default template should be considered empty")
	}
}

func TestIsSessionMemoryEmpty_PartialTemplate(t *testing.T) {
	t.Parallel()
	// Partial template content (not the full template) — TS uses strict equality,
	// so this is NOT considered empty even though it has only placeholders.
	content := `# Session Notes

## Session Title
[Brief, descriptive title]

## Current State
[What are we working on right now? What's the status?]

## Worklog
- [Timestamp] [Brief description of what was done]
`
	if IsSessionMemoryEmpty(content) {
		t.Error("partial template should NOT be considered empty (strict equality check)")
	}
}

func TestIsSessionMemoryEmpty_HasRealContent(t *testing.T) {
	t.Parallel()
	content := `# Session Notes

## Session Title
Implementing session memory extraction

## Current State
Writing tests for the template module
`
	if IsSessionMemoryEmpty(content) {
		t.Error("content with real data should NOT be empty")
	}
}

func TestIsSessionMemoryEmpty_MixedContent(t *testing.T) {
	t.Parallel()
	// One real line among placeholders
	content := `# Session Notes

## Session Title
[Brief, descriptive title]

## Current State
Working on the auth module

## Task specification
[What is the user trying to accomplish?]
`
	if IsSessionMemoryEmpty(content) {
		t.Error("content with at least one real line should NOT be empty")
	}
}

// --- TruncateForCompact ---

func TestTruncateForCompact_ShortContent(t *testing.T) {
	t.Parallel()
	content := `# Session Notes

## Session Title
Short title

## Current State
Short state
`
	truncated := TruncateForCompact(content, 2000)
	if truncated != content {
		t.Errorf("short content should not be truncated\ngot:  %q\nwant: %q", truncated, content)
	}
}

func TestTruncateForCompact_OversizeSection(t *testing.T) {
	t.Parallel()
	// Create a section that exceeds the budget
	longLine := strings.Repeat("x", 500) // 500 chars ≈ 125 tokens
	content := "## Files and Functions\n" + strings.Repeat(longLine+"\n", 20)

	maxSection := 200 // 200 tokens × 4 = 800 chars budget
	truncated := TruncateForCompact(content, maxSection)

	if !strings.Contains(truncated, "[... section truncated for length ...]") {
		t.Error("oversized section should contain truncation marker")
	}
	if !strings.Contains(truncated, "## Files and Functions") {
		t.Error("header should be preserved after truncation")
	}
}

func TestTruncateForCompact_MultipleSections(t *testing.T) {
	t.Parallel()
	shortSection := "## Session Title\nShort title\n"
	longSection := "## Files\n" + strings.Repeat("y", 3000) + "\n"
	content := shortSection + "\n" + longSection

	truncated := TruncateForCompact(content, 200) // 800 char budget per section

	// Short section should survive intact
	if !strings.Contains(truncated, "Short title") {
		t.Error("short section should be preserved")
	}
}

// --- BuildUpdatePrompt ---

func TestBuildUpdatePrompt_SubstitutesVariables(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	prompt := BuildUpdatePrompt("my current notes", "/path/to/notes.md", cfg)

	if !strings.Contains(prompt, "my current notes") {
		t.Error("prompt should contain the current notes")
	}
	if !strings.Contains(prompt, "/path/to/notes.md") {
		t.Error("prompt should contain the notes path")
	}
	if !strings.Contains(prompt, "You are a session memory manager") {
		t.Error("prompt should contain the system instruction")
	}
}

func TestBuildUpdatePrompt_SizeWarnings(t *testing.T) {
	t.Parallel()
	// Create oversized content to trigger warning
	bigSection := "## Files\n" + strings.Repeat("x", 10000) + "\n"
	cfg := Config{
		MaxSectionTokens: 100,
		MaxTotalTokens:   500,
	}

	prompt := BuildUpdatePrompt(bigSection, "/path/notes.md", cfg)

	if !strings.Contains(prompt, "Size Warnings") {
		t.Error("oversized content should trigger size warnings")
	}
}

// --- analyzeSectionSizes ---

func TestAnalyzeSectionSizes_BasicSections(t *testing.T) {
	t.Parallel()
	content := `## Session Title
My title

## Current State
Working on tests
`
	sizes := analyzeSectionSizes(content)
	if len(sizes) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sizes))
	}
	if sizes[0].Header != "Session Title" {
		t.Errorf("first header = %q, want %q", sizes[0].Header, "Session Title")
	}
	if sizes[1].Header != "Current State" {
		t.Errorf("second header = %q, want %q", sizes[1].Header, "Current State")
	}
}

func TestAnalyzeSectionSizes_EstimatesTokens(t *testing.T) {
	t.Parallel()
	content := "## Title\n" + strings.Repeat("a", 400) + "\n" // ~400 chars ≈ 100 tokens
	sizes := analyzeSectionSizes(content)
	if len(sizes) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sizes))
	}
	// Verify it called types.EstimateTokens (result should be ~100)
	if sizes[0].Tokens <= 0 {
		t.Errorf("expected positive token estimate, got %d", sizes[0].Tokens)
	}
}

// --- generateSectionReminders ---

func TestGenerateSectionReminders_NoWarnings(t *testing.T) {
	t.Parallel()
	sizes := []SectionSizes{
		{Header: "Title", Tokens: 50},
		{Header: "State", Tokens: 100},
	}
	reminders := generateSectionReminders(sizes, 200, 500)
	if reminders != "" {
		t.Errorf("no warnings expected, got: %s", reminders)
	}
}

func TestGenerateSectionReminders_OversizedSection(t *testing.T) {
	t.Parallel()
	sizes := []SectionSizes{
		{Header: "Files", Tokens: 300},
	}
	reminders := generateSectionReminders(sizes, 200, 1000)
	if !strings.Contains(reminders, "Files") {
		t.Error("should warn about oversized Files section")
	}
	if !strings.Contains(reminders, "300 tokens (budget: 200)") {
		t.Errorf("should show section size and budget, got: %s", reminders)
	}
}

func TestGenerateSectionReminders_TotalOverBudget(t *testing.T) {
	t.Parallel()
	sizes := []SectionSizes{
		{Header: "A", Tokens: 600},
		{Header: "B", Tokens: 500},
	}
	reminders := generateSectionReminders(sizes, 2000, 1000)
	if !strings.Contains(reminders, "CRITICAL") {
		t.Error("total over budget should generate CRITICAL warning")
	}
	if !strings.Contains(reminders, "1100 tokens") {
		t.Error("should show total token count")
	}
}

// --- substituteVariables ---

func TestSubstituteVariables_BasicSubstitution(t *testing.T) {
	t.Parallel()
	result := substituteVariables("Hello {{name}}!", map[string]string{"name": "world"})
	if result != "Hello world!" {
		t.Errorf("got %q, want %q", result, "Hello world!")
	}
}

func TestSubstituteVariables_MissingVariable(t *testing.T) {
	t.Parallel()
	result := substituteVariables("Hello {{unknown}}!", map[string]string{"name": "world"})
	if result != "Hello {{unknown}}!" {
		t.Errorf("missing variable should remain as-is, got %q", result)
	}
}

func TestSubstituteVariables_MultipleVariables(t *testing.T) {
	t.Parallel()
	result := substituteVariables("{{a}} and {{b}}", map[string]string{
		"a": "first",
		"b": "second",
	})
	if result != "first and second" {
		t.Errorf("got %q, want %q", result, "first and second")
	}
}

// --- Edge case: EstimateTokens used in template ---

func TestDefaultConfig_ReasonableDefaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if cfg.MinTokensToInit != 10000 {
		t.Errorf("MinTokensToInit = %d, want 10000", cfg.MinTokensToInit)
	}
	if cfg.MinTokensBetweenUpdate != 50000 {
		t.Errorf("MinTokensBetweenUpdate = %d, want 50000", cfg.MinTokensBetweenUpdate)
	}
	if cfg.ToolCallsBetweenUpdates != 50 {
		t.Errorf("ToolCallsBetweenUpdates = %d, want 50", cfg.ToolCallsBetweenUpdates)
	}
	if cfg.MaxSectionTokens != 2000 {
		t.Errorf("MaxSectionTokens = %d, want 2000", cfg.MaxSectionTokens)
	}
}

// EstimateTokens must be callable from this package (not importing engine).
func TestEstimateTokensAccessible(t *testing.T) {
	t.Parallel()
	// This tests that the package can call types.EstimateTokens without
	// importing engine (avoiding circular dependency).
	tokens := types.EstimateTokens("hello world")
	if tokens <= 0 {
		t.Errorf("EstimateTokens should return positive value, got %d", tokens)
	}
}
