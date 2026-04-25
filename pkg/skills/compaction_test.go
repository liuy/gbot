package skills

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Compaction protection tests
// Source: src/services/compact/compact.ts:129-130, 1494-1534
// ---------------------------------------------------------------------------

func TestAddInvokedSkill(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("commit", "project:commit", "commit content", "agent-1")

	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if len(reg.invokedSkills) != 1 {
		t.Fatalf("expected 1 invoked skill, got %d", len(reg.invokedSkills))
	}
	info, ok := reg.invokedSkills["agent-1:commit"]
	if !ok {
		t.Fatal("expected key 'agent-1:commit'")
	}
	if info.SkillName != "commit" {
		t.Errorf("SkillName = %q, want %q", info.SkillName, "commit")
	}
	if info.SkillPath != "project:commit" {
		t.Errorf("SkillPath = %q, want %q", info.SkillPath, "project:commit")
	}
	if info.Content != "commit content" {
		t.Errorf("Content = %q, want %q", info.Content, "commit content")
	}
	if info.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", info.AgentID, "agent-1")
	}
	if info.InvokedAt.IsZero() {
		t.Error("InvokedAt should be set")
	}
}

func TestAddInvokedSkill_ScopedPerAgent(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("commit", "project:commit", "content", "agent-1")
	reg.AddInvokedSkill("commit", "project:commit", "content", "agent-2")

	reg.mu.RLock()
	count := len(reg.invokedSkills)
	reg.mu.RUnlock()

	if count != 2 {
		t.Errorf("expected 2 entries (different agents), got %d", count)
	}
}

func TestGetInvokedSkillsForAgent(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("commit", "p:commit", "c1", "agent-1")
	time.Sleep(time.Millisecond) // ensure ordering
	reg.AddInvokedSkill("review", "p:review", "c2", "agent-1")
	reg.AddInvokedSkill("test", "p:test", "c3", "agent-2")

	skills := reg.GetInvokedSkillsForAgent("agent-1")
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills for agent-1, got %d", len(skills))
	}
	// Should be sorted by invocation time
	if skills[0].SkillName != "commit" {
		t.Errorf("first skill should be 'commit' (earlier), got %q", skills[0].SkillName)
	}
	if skills[1].SkillName != "review" {
		t.Errorf("second skill should be 'review' (later), got %q", skills[1].SkillName)
	}

	skills2 := reg.GetInvokedSkillsForAgent("agent-2")
	if len(skills2) != 1 {
		t.Fatalf("expected 1 skill for agent-2, got %d", len(skills2))
	}
	if skills2[0].SkillName != "test" {
		t.Errorf("skill for agent-2 = %q, want %q", skills2[0].SkillName, "test")
	}
}

func TestGetInvokedSkillsForAgent_Empty(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	skills := reg.GetInvokedSkillsForAgent("nonexistent")
	if len(skills) != 0 {
		t.Errorf("expected empty for nonexistent agent, got %d", len(skills))
	}
}

func TestClearInvokedSkillsForAgent(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("commit", "p:commit", "c1", "agent-1")
	reg.AddInvokedSkill("review", "p:review", "c2", "agent-1")
	reg.AddInvokedSkill("test", "p:test", "c3", "agent-2")

	reg.ClearInvokedSkillsForAgent("agent-1")

	reg.mu.RLock()
	count := len(reg.invokedSkills)
	reg.mu.RUnlock()

	if count != 1 {
		t.Errorf("expected 1 remaining skill (agent-2), got %d", count)
	}

	remaining := reg.GetInvokedSkillsForAgent("agent-2")
	if len(remaining) != 1 {
		t.Errorf("agent-2 should still have 1 skill, got %d", len(remaining))
	}
}

func TestCreateSkillAttachment_Empty(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	result := reg.CreateSkillAttachment("agent-1", PostCompactMaxTokensPerSkill, PostCompactSkillsTokenBudget)
	if result != "" {
		t.Errorf("expected empty string for no invoked skills, got %q", result)
	}
}

func TestCreateSkillAttachment_Single(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("commit", "p:commit", "Create a git commit.", "agent-1")

	result := reg.CreateSkillAttachment("agent-1", PostCompactMaxTokensPerSkill, PostCompactSkillsTokenBudget)
	if !strings.Contains(result, "Previously invoked skills") {
		t.Errorf("expected header, got %q", result)
	}
	if !strings.Contains(result, "### Skill: commit") {
		t.Errorf("expected skill header, got %q", result)
	}
	if !strings.Contains(result, "Create a git commit.") {
		t.Errorf("expected skill content, got %q", result)
	}
}

func TestCreateSkillAttachment_PerSkillTruncation(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	// Create content larger than per-skill budget (5000 tokens * 4 chars = 20000 chars)
	longContent := strings.Repeat("x", 25000)
	reg.AddInvokedSkill("big", "p:big", longContent, "agent-1")

	result := reg.CreateSkillAttachment("agent-1", 5000, 25000)
	if !strings.Contains(result, "[...truncated]") {
		t.Error("expected truncation marker for oversized skill content")
	}
	// Content should be capped at 20000 chars + marker
	skillSection := result[strings.Index(result, "### Skill: big"):]
	if len(skillSection) > 20200 { // 20000 + marker + header with some margin
		t.Errorf("skill section too large: %d chars", len(skillSection))
	}
}

func TestCreateSkillAttachment_TotalBudgetEnforcement(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	// Add multiple skills that together exceed total budget
	for i := range 10 {
		content := strings.Repeat("a", 10000) // 10K chars each
		reg.AddInvokedSkill("skill-"+strings.Repeat("a", 1+i), "p:s"+string(rune('0'+i)), content, "agent-1")
		time.Sleep(time.Millisecond) // ensure ordering
	}

	// Total budget: 25000 tokens * 4 = 100000 chars
	// Each skill: 5000 tokens * 4 = 20000 chars (but content is only 10000)
	// Should fit ~10 skills at 10000 each = 100000, but overhead means fewer
	result := reg.CreateSkillAttachment("agent-1", 5000, 25000)
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Count how many skill sections are included
	skillCount := strings.Count(result, "### Skill:")
	if skillCount < 1 {
		t.Error("expected at least one skill in attachment")
	}
	if skillCount > 10 {
		t.Errorf("expected at most 10 skills, got %d", skillCount)
	}
}

func TestCreateSkillAttachment_MultipleAgentsIsolation(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("commit", "p:commit", "commit content", "agent-1")
	reg.AddInvokedSkill("review", "p:review", "review content", "agent-2")

	result := reg.CreateSkillAttachment("agent-1", PostCompactMaxTokensPerSkill, PostCompactSkillsTokenBudget)
	if strings.Contains(result, "review") {
		t.Error("agent-1 attachment should not contain agent-2 skills")
	}
	if !strings.Contains(result, "commit") {
		t.Error("agent-1 attachment should contain its own skill")
	}
}

func TestCleanupActivatedSkills(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("commit", "p:commit", "c1", "agent-1")

	// Immediately clean with 0 max age — should remove
	reg.CleanupActivatedSkills(0)

	reg.mu.RLock()
	count := len(reg.invokedSkills)
	reg.mu.RUnlock()

	if count != 0 {
		t.Errorf("expected 0 after cleanup with 0 age, got %d", count)
	}
}

func TestCleanupActivatedSkills_KeepsRecent(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("commit", "p:commit", "c1", "agent-1")

	// Clean with 1 hour max age — should keep (just invoked)
	reg.CleanupActivatedSkills(1 * time.Hour)

	reg.mu.RLock()
	count := len(reg.invokedSkills)
	reg.mu.RUnlock()

	if count != 1 {
		t.Errorf("expected 1 after cleanup (skill is recent), got %d", count)
	}
}

func TestCleanupActivatedSkills_ClearsActivatedNames(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.activatedNames["test-skill"] = true
	reg.mu.Unlock()

	reg.CleanupActivatedSkills(0)

	reg.mu.RLock()
	names := reg.activatedNames
	reg.mu.RUnlock()

	if len(names) != 0 {
		t.Errorf("activatedNames should be cleared, got %d entries", len(names))
	}
}

func TestCreateSkillAttachment_ZeroBudget(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.AddInvokedSkill("big", "p:big", "content", "agent-1")

	// Zero total budget — sections should be empty
	result := reg.CreateSkillAttachment("agent-1", 5000, 0)
	if result != "" {
		t.Errorf("zero budget should return empty, got %q", result)
	}
}

func TestCompactionConstants(t *testing.T) {
	t.Parallel()

	if PostCompactMaxTokensPerSkill != 5000 {
		t.Errorf("PostCompactMaxTokensPerSkill = %d, want 5000 (TS: compact.ts:129)", PostCompactMaxTokensPerSkill)
	}
	if PostCompactSkillsTokenBudget != 25000 {
		t.Errorf("PostCompactSkillsTokenBudget = %d, want 25000 (TS: compact.ts:130)", PostCompactSkillsTokenBudget)
	}
}
