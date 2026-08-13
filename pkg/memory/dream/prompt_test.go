package dream

import (
	"strings"
	"testing"
	"time"
)

func TestSystemPrompt_PhaseStructure(t *testing.T) {
	for _, phase := range []string{"Phase 1 — Orient", "Phase 2 — Gather", "Phase 3 — Consolidate", "Phase 4 — Prune"} {
		if !strings.Contains(SystemPrompt, phase) {
			t.Errorf("system prompt missing phase header %q", phase)
		}
	}
	for _, step := range []string{"Step 1", "Step 2"} {
		if strings.Contains(SystemPrompt, step) {
			t.Errorf("system prompt should not contain old step header %q", step)
		}
	}
}

func TestSystemPrompt_RecallInstructions(t *testing.T) {
	if !strings.Contains(SystemPrompt, "Recall") {
		t.Error("system prompt should instruct the agent to use Recall tool")
	}
	if !strings.Contains(SystemPrompt, `Recall(query=`) {
		t.Error("system prompt should show Recall call syntax")
	}
}

func TestSystemPrompt_NoGrepOrJsonl(t *testing.T) {
	if strings.Contains(SystemPrompt, "grep") {
		t.Error("system prompt should not contain grep instructions (uses Recall instead)")
	}
	if strings.Contains(SystemPrompt, ".jsonl") {
		t.Error("system prompt should not reference .jsonl files")
	}
}

func TestSystemPrompt_NoRememberForget(t *testing.T) {
	if strings.Contains(SystemPrompt, "Remember") {
		t.Error("system prompt should not contain Remember instructions")
	}
	if strings.Contains(SystemPrompt, "Forget") {
		t.Error("system prompt should not contain Forget instructions")
	}
}

func TestSystemPrompt_PruneGuidance(t *testing.T) {
	if !strings.Contains(SystemPrompt, "MEMORY.md") {
		t.Error("system prompt missing MEMORY.md reference")
	}
	if !strings.Contains(SystemPrompt, "~50 lines") {
		t.Error("system prompt should mention ~50 line index cap")
	}
	if !strings.Contains(SystemPrompt, "~150 characters") {
		t.Error("system prompt should mention ~150 character per-line cap")
	}
}

func TestTriggerMessage_LastDreamNever(t *testing.T) {
	result := TriggerMessage("/mem", time.Time{})
	if !strings.Contains(result, "Last consolidation: never") {
		t.Error("cold-start trigger should say 'Last consolidation: never'")
	}
	if !strings.Contains(result, "/mem") {
		t.Error("trigger should contain memory dir path")
	}
}

func TestTriggerMessage_LastDreamFormatted(t *testing.T) {
	lastDream := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	result := TriggerMessage("/mem", lastDream)
	expected := "Last consolidation: 2026-03-15 10:30"
	if !strings.Contains(result, expected) {
		t.Errorf("trigger should contain %q, got: %s", expected, result)
	}
}
