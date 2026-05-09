package dream

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/memory/long"
)

func TestBuildConsolidationPrompt_AllPhases(t *testing.T) {
	result := BuildConsolidationPrompt("/mem", "/project", "")
	for _, phase := range []string{"Phase 1 — Orient", "Phase 2 — Gather", "Phase 3 — Consolidate", "Phase 4 — Prune"} {
		if !strings.Contains(result, phase) {
			t.Errorf("prompt missing phase header %q", phase)
		}
	}
}

func TestBuildConsolidationPrompt_Paths(t *testing.T) {
	result := BuildConsolidationPrompt("/mem/root", "/project/dir", "")
	if !strings.Contains(result, "/mem/root") {
		t.Error("prompt missing memoryRoot path")
	}
	if !strings.Contains(result, "/project/dir") {
		t.Error("prompt missing projectDir path")
	}
}

func TestBuildConsolidationPrompt_EntrypointConstraints(t *testing.T) {
	result := BuildConsolidationPrompt("/mem", "/project", "")
	if !strings.Contains(result, long.EntrypointName) {
		t.Error("prompt missing MEMORY.md reference")
	}
	// Should mention the line cap
	if !strings.Contains(result, "200") {
		t.Error("prompt missing line cap reference")
	}
	// Should mention 25KB
	if !strings.Contains(result, "25KB") {
		t.Error("prompt missing size cap reference")
	}
}

func TestBuildConsolidationPrompt_ExtraContext(t *testing.T) {
	extra := "Sessions since last consolidation (3):\n- sess1\n- sess2\n- sess3"
	result := BuildConsolidationPrompt("/mem", "/project", extra)
	if !strings.Contains(result, "Additional context") {
		t.Error("prompt missing 'Additional context' section")
	}
	if !strings.Contains(result, "sess1") {
		t.Error("prompt missing session IDs from extra")
	}
}

func TestBuildConsolidationPrompt_NoExtra(t *testing.T) {
	result := BuildConsolidationPrompt("/mem", "/project", "")
	if strings.Contains(result, "Additional context") {
		t.Error("prompt should not contain 'Additional context' when extra is empty")
	}
}
