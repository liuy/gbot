package skills

import (
	"strings"
	"testing"
)

func TestJoinSections(t *testing.T) {
	t.Parallel()
	if got := joinSections(nil); got != "" {
		t.Errorf("empty = %q, want empty string", got)
	}
	if got := joinSections([]string{"a"}); got != "a" {
		t.Errorf("single = %q, want a", got)
	}
	if got := joinSections([]string{"a", "b", "c"}); got != "a\n\nb\n\nc" {
		t.Errorf("three = %q, want a\\n\\nb\\n\\nc", got)
	}
}

func TestIsOnlyDigits(t *testing.T) {
	t.Parallel()
	if isOnlyDigits("") {
		t.Error("empty string must not count as digits")
	}
	if !isOnlyDigits("12") || !isOnlyDigits("0") {
		t.Error("digit strings must report true")
	}
	for _, s := range []string{"1a", "a1", " 1", "1 ", "-1"} {
		if isOnlyDigits(s) {
			t.Errorf("isOnlyDigits(%q) = true, want false", s)
		}
	}
}

// Indexed shorthand is only replaced when the next byte is not a word char.
func TestSubstituteArguments_WordCharBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct{ content, want string }{
		{"$1z", "$1z"},
		{"$1Z", "$1Z"},
		{"$19", "$19"},
		{"$1_", "$1_"},
		{"$1a", "$1a"},
		{"$1A", "$1A"},
		{"$1-0", "B-0"},
		{"$1", "B"},
		{"$0", "A"},
		{"$0.$1", "A.B"},
		{"$1$", "B$"},
		{"$2", "$2"}, // out of range keeps the original
	}
	for _, c := range cases {
		if got := SubstituteArguments(c.content, "A B", nil, false); got != c.want {
			t.Errorf("SubstituteArguments(%q) = %q, want %q", c.content, got, c.want)
		}
	}
}

// The skill attachment honours the per-skill and total character budgets.
func TestCreateSkillAttachment_Budgets(t *testing.T) {
	r := NewRegistry(t.TempDir())
	for _, name := range []string{"s1", "s2", "s3"} {
		r.AddInvokedSkill(name, "/skills/"+name, "aaaa", "agent-1")
	}
	// Each section is 17 chars; a 36-char budget fits exactly two.
	got := r.CreateSkillAttachment("agent-1", 10, 9)
	want := "## Previously invoked skills\n\n### Skill: s1\naaaa\n\n### Skill: s2\naaaa"
	if got != want {
		t.Errorf("budgeted attachment =\n%q\nwant\n%q", got, want)
	}
	// A generous budget keeps all three.
	got = r.CreateSkillAttachment("agent-1", 10, 20)
	if strings.Count(got, "### Skill:") != 3 {
		t.Errorf("all-skills attachment = %q, want three sections", got)
	}
	// Per-skill truncation marker.
	r2 := NewRegistry(t.TempDir())
	r2.AddInvokedSkill("big", "/skills/big", strings.Repeat("y", 50), "agent-2")
	got = r2.CreateSkillAttachment("agent-2", 5, 100) // 20 chars per skill
	if !strings.HasSuffix(got, strings.Repeat("y", 20)+"\n[...truncated]") {
		t.Errorf("truncated attachment = %q, want the 20-char cut plus marker", got)
	}
	// No invoked skills → no attachment.
	if got := r.CreateSkillAttachment("nobody", 10, 100); got != "" {
		t.Errorf("empty agent attachment = %q, want empty string", got)
	}
}
