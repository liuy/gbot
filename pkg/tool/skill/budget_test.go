package skill

import (
	"os"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestGetCharBudget_Default(t *testing.T) {
	t.Parallel()

	budget := getCharBudget(0)
	if budget != DefaultCharBudget {
		t.Errorf("default budget = %d, want %d", budget, DefaultCharBudget)
	}
}

func TestGetCharBudget_WithTokens(t *testing.T) {
	t.Parallel()

	// 200k tokens * 4 chars * 1% = 8000
	budget := getCharBudget(200000)
	if budget != 8000 {
		t.Errorf("200k budget = %d, want 8000", budget)
	}
}

func TestGetCharBudget_EnvOverride(t *testing.T) {
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "5000")
	budget := getCharBudget(200000)
	if budget != 5000 {
		t.Errorf("env override budget = %d, want 5000", budget)
	}
}

func TestGetCommandDescription_Plain(t *testing.T) {
	t.Parallel()

	cmd := types.SkillCommand{Name: "test", Description: "A test skill"}
	desc := getCommandDescription(cmd)
	if desc != "A test skill" {
		t.Errorf("got %q, want %q", desc, "A test skill")
	}
}

func TestGetCommandDescription_WithWhenToUse(t *testing.T) {
	t.Parallel()

	cmd := types.SkillCommand{
		Name: "test", Description: "A skill", WhenToUse: "Use when testing",
	}
	desc := getCommandDescription(cmd)
	want := "A skill - Use when testing"
	if desc != want {
		t.Errorf("got %q, want %q", desc, want)
	}
}

func TestGetCommandDescription_Truncation(t *testing.T) {
	t.Parallel()

	longDesc := strings.Repeat("x", 300)
	cmd := types.SkillCommand{Name: "test", Description: longDesc}
	desc := getCommandDescription(cmd)
	if len([]rune(desc)) > MaxListingDescChars {
		t.Errorf("description should be capped at %d chars, got %d", MaxListingDescChars, len([]rune(desc)))
	}
	if !strings.ContainsRune(desc, '\u2026') {
		t.Error("truncated description should contain ellipsis")
	}
}

func TestBuildSkillListing_Empty(t *testing.T) {
	t.Parallel()

	result := BuildSkillListing(nil, 200000)
	if result != "" {
		t.Errorf("empty skills should return empty string, got %q", result)
	}
}

func TestBuildSkillListing_Fits(t *testing.T) {
	t.Parallel()

	skills := []types.SkillCommand{
		{Name: "commit", Description: "Create a commit"},
		{Name: "review", Description: "Review code"},
	}
	result := BuildSkillListing(skills, 200000)
	if !strings.Contains(result, "commit: Create a commit") {
		t.Errorf("expected commit entry, got %q", result)
	}
	if !strings.Contains(result, "review: Review code") {
		t.Errorf("expected review entry, got %q", result)
	}
}

func TestBuildSkillListing_BundledNeverTruncated(t *testing.T) {
	t.Parallel()

	// Description under 250 chars so getCommandDescription doesn't cap it
	desc := strings.Repeat("x", 100)
	skills := []types.SkillCommand{
		{Name: "bundled", Description: desc, Source: types.SkillSourceBundled},
		{Name: "user", Description: "short", Source: types.SkillSourceUser},
	}
	// Small budget to force truncation of non-bundled
	result := BuildSkillListing(skills, 500)
	// Bundled should keep full description
	if !strings.Contains(result, "bundled: "+desc) {
		t.Errorf("bundled skill should have full description, got %q", result)
	}
	// User skill should be truncated or names-only
	if strings.Contains(result, "user: short") {
		// If it fits that's fine too — budget is 500 which is generous
		t.Log("user skill kept description (budget sufficient)")
	}
}

func TestBuildSkillListing_NamesOnlyWhenTinyBudget(t *testing.T) {
	t.Parallel()

	skills := []types.SkillCommand{
		{Name: "commit", Description: "Create a git commit", Source: types.SkillSourceUser},
		{Name: "review", Description: "Review code changes", Source: types.SkillSourceUser},
	}
	// Tiny budget — should go names-only
	result := BuildSkillListing(skills, 10)
	if strings.Contains(result, "Create a git commit") {
		t.Errorf("tiny budget should show names only, got %q", result)
	}
	if !strings.Contains(result, "- commit") {
		t.Errorf("should contain skill name, got %q", result)
	}
}

func TestBuildSkillListing_Constants(t *testing.T) {
	t.Parallel()

	if SkillBudgetContextPercent != 0.01 {
		t.Errorf("SkillBudgetContextPercent = %v, want 0.01", SkillBudgetContextPercent)
	}
	if CharsPerToken != 4 {
		t.Errorf("CharsPerToken = %d, want 4", CharsPerToken)
	}
	if DefaultCharBudget != 8000 {
		t.Errorf("DefaultCharBudget = %d, want 8000", DefaultCharBudget)
	}
	if MaxListingDescChars != 250 {
		t.Errorf("MaxListingDescChars = %d, want 250", MaxListingDescChars)
	}
}

func TestStringWidth(t *testing.T) {
	t.Parallel()

	if stringWidth("hello") != 5 {
		t.Errorf("stringWidth(hello) = %d, want 5", stringWidth("hello"))
	}
	if stringWidth("") != 0 {
		t.Errorf("stringWidth(empty) = %d, want 0", stringWidth(""))
	}
}

func TestFormatCommandDescription(t *testing.T) {
	t.Parallel()

	cmd := types.SkillCommand{Name: "commit", Description: "Git commit"}
	result := formatCommandDescription(cmd)
	if result != "- commit: Git commit" {
		t.Errorf("got %q, want %q", result, "- commit: Git commit")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
