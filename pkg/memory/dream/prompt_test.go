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

// TestSystemPrompt_QueriesPrimaryRecallDeepDive guards the standing manual:
// queries first, Recall deep-dive after — never Recall-first gathering.
// (Pagination/coverage teaching intentionally lives in the trigger runbook,
// not here; see TestTriggerMessage_PhaseFramingNotSteps.)
func TestSystemPrompt_QueriesPrimaryRecallDeepDive(t *testing.T) {
	if !strings.Contains(SystemPrompt, "ready-to-run Read queries") {
		t.Error("system prompt should describe the trigger's pre-built queries")
	}
	if !strings.Contains(SystemPrompt, "Recall") {
		t.Error("system prompt should still keep Recall as the deep-dive tool")
	}
	// Recall deep-dive guidance must be example-driven (trigger conditions,
	// not abstract advice) — abstract hints proved too weak to change behavior.
	if !strings.Contains(SystemPrompt, `Recall(query=`) {
		t.Error("system prompt should include a concrete Recall example")
	}
	// Ordering invariant: the pre-built queries come first, Recall deep-dive
	// after — never Recall-first gathering.
	if strings.Index(SystemPrompt, "ready-to-run Read queries") > strings.Index(SystemPrompt, `Recall(query=`) {
		t.Error("query-based gathering must be taught before Recall deep-dive")
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

func TestTriggerMessage_ColdStart(t *testing.T) {
	result := TriggerMessage("/mem", "/mem/memory.db", "dream-sid", time.Time{}, 42)
	if !strings.Contains(result, "Last consolidation: never") {
		t.Error("cold-start trigger should say 'Last consolidation: never'")
	}
	if !strings.Contains(result, "/mem") || !strings.Contains(result, "/mem/memory.db") {
		t.Error("trigger should contain memory dir and DB paths")
	}
	if !strings.Contains(result, "New main-thread messages since cutoff: 42") {
		t.Error("trigger should report the new-message count")
	}
	// Epoch cutoff covers the entire transcript on first dream.
	if !strings.Contains(result, "> '1970-01-01 00:00:00'") {
		t.Error("cold-start queries should use the epoch cutoff")
	}
}

func TestTriggerMessage_LastDreamFormatted(t *testing.T) {
	// 10:30 CST (+8) = 02:30 UTC — the human-readable line is local wall
	// clock while the query cutoff must be UTC.
	cst := time.FixedZone("CST", 8*3600)
	lastDream := time.Date(2026, 3, 15, 10, 30, 0, 0, cst)
	result := TriggerMessage("/mem", "/mem/memory.db", "dream-sid", lastDream, 7)
	if !strings.Contains(result, "Last consolidation: 2026-03-15 10:30") {
		t.Errorf("trigger should render local wall clock 2026-03-15 10:30, got: %s", result)
	}
	if !strings.Contains(result, "'2026-03-15 02:30:00'") {
		t.Error("query cutoff must be UTC 02:30, not local 10:30")
	}
	if strings.Contains(result, "'2026-03-15 10:30") {
		t.Error("local wall clock must not leak into the query cutoff")
	}
}

func TestTriggerMessage_Queries(t *testing.T) {
	lastDream := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	result := TriggerMessage("/mem", "/mem/memory.db", "dream-sid", lastDream, 7)

	// Both steps are copy-pasteable Read calls against the DB path.
	for _, want := range []string{
		`Read("/mem/memory.db?q=SELECT session_id, COUNT(*)`,
		`Read("/mem/memory.db?q=SELECT seq, type, substr(COALESCE(`,
		`json_extract(content,'$[0].text')`,
	} {
		if !strings.Contains(result, want) {
			t.Errorf("trigger missing query fragment %q", want)
		}
	}

	// Dream's own transcript is excluded in both queries.
	if got := strings.Count(result, "session_id != 'dream-sid'"); got != 2 {
		t.Errorf("both queries must exclude the dream session, found %d exclusions", got)
	}

	// Dialogue query keeps user asks and assistant conclusions, drops tool noise.
	if !strings.Contains(result, `type = 'user' AND content NOT LIKE '%tool_result%'`) {
		t.Error("dialogue query must exclude user tool_result rows")
	}
	if !strings.Contains(result, `type = 'assistant' AND content LIKE '%"type":"text"%'`) {
		t.Error("dialogue query must keep only assistant rows that have text blocks")
	}
	if !strings.Contains(result, "is_sidechain = 0") {
		t.Error("queries must exclude sidechain (sub-agent) messages")
	}
}

// TestTriggerMessage_PhaseFramingNotSteps guards the trigger's role: a
// per-window RUNBOOK aligned to the system prompt's phase names — never its
// own step enumeration. On 2026-09-22 the old "Step 1/Step 2 ... Begin"
// framing drowned the system prompt's workflow: the dream ran the two
// copy-pasteable queries and used Recall zero times. The runbook must carry
// the three operational musts the system prompt cannot hammer home alone:
// seq-cursor pagination to exhaustion, and Recall at each of its three
// intervention points (gather depth, conflict check before overwrite,
// staleness check before prune).
func TestTriggerMessage_PhaseFramingNotSteps(t *testing.T) {
	lastDream := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	result := TriggerMessage("/mem", "/mem/memory.db", "dream-sid", lastDream, 7)

	for _, step := range []string{"Step 1", "Step 2", "Step 3"} {
		if strings.Contains(result, step) {
			t.Errorf("trigger must not invent step numbering %q — phases live in the system prompt", step)
		}
	}
	if !strings.Contains(result, "AND seq > <last returned seq>") {
		t.Error("trigger must teach seq-cursor pagination to window exhaustion")
	}
	for _, phase := range []string{"Phase 2 (gather)", "Phase 3 (consolidate)", "Phase 4 (prune)"} {
		if !strings.Contains(result, phase) {
			t.Errorf("trigger runbook missing %s", phase)
		}
	}
	if strings.Count(result, "Recall") < 3 {
		t.Errorf("trigger must name Recall at all three intervention points, got %d mentions", strings.Count(result, "Recall"))
	}
	if !strings.Contains(result, "Begin.") {
		t.Error("trigger should end with the begin cue")
	}
}
