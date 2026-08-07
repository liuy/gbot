package dream

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/memory/long"
)

func TestBuildConsolidationPrompt_StepStructure(t *testing.T) {
	result := BuildConsolidationPrompt("/mem", "")
	for _, step := range []string{"Step 1 — Notes", "Step 2 — Facts"} {
		if !strings.Contains(result, step) {
			t.Errorf("prompt missing step header %q", step)
		}
	}
	// Should NOT contain old Phase numbering
	for _, phase := range []string{"Phase 1", "Phase 2", "Phase 3", "Phase 4"} {
		if strings.Contains(result, phase) {
			t.Errorf("prompt should not contain old phase header %q", phase)
		}
	}
}

func TestBuildConsolidationPrompt_NoGrepOrProjectDir(t *testing.T) {
	result := BuildConsolidationPrompt("/mem", "")
	if strings.Contains(result, "grep") {
		t.Error("prompt should not contain grep instructions")
	}
	if strings.Contains(result, ".jsonl") {
		t.Error("prompt should not reference .jsonl files")
	}
}

func TestBuildConsolidationPrompt_MemoryDir(t *testing.T) {
	result := BuildConsolidationPrompt("/mem/root", "")
	if !strings.Contains(result, "/mem/root") {
		t.Error("prompt missing memoryDir path")
	}
}

func TestBuildConsolidationPrompt_EntrypointConstraints(t *testing.T) {
	result := BuildConsolidationPrompt("/mem", "")
	if !strings.Contains(result, long.EntrypointName) {
		t.Error("prompt missing MEMORY.md reference")
	}
	// Should mention the line cap
	if !strings.Contains(result, "200") {
		t.Error("prompt missing line cap reference")
	}
}

func TestBuildConsolidationPrompt_ExtraContext(t *testing.T) {
	extra := "Recent conversations since last dream (chunk 1/1):\n\n[user 2026-01-01 12:00] hello\n"
	result := BuildConsolidationPrompt("/mem", extra)
	if !strings.Contains(result, "Recent conversations") {
		t.Error("prompt missing 'Recent conversations' section")
	}
	if !strings.Contains(result, "hello") {
		t.Error("prompt missing message text from extra")
	}
}

func TestBuildConsolidationPrompt_NoExtra(t *testing.T) {
	result := BuildConsolidationPrompt("/mem", "")
	if strings.Contains(result, "Recent conversations") {
		t.Error("prompt should not contain 'Recent conversations' section when extra is empty")
	}
}

func TestBuildConsolidationPrompt_AlwaysIncludesFactsSection(t *testing.T) {
	result := BuildConsolidationPrompt("/mem", "")
	for _, marker := range []string{"Recall", "Remember", "Forget"} {
		if !strings.Contains(result, marker) {
			t.Errorf("prompt should contain %q", marker)
		}
	}
	if !strings.Contains(result, "[Extract]") {
		t.Error("prompt should contain extraction criteria")
	}
	if !strings.Contains(result, "[Decision criteria]") {
		t.Error("prompt should contain judgment criteria")
	}
}
