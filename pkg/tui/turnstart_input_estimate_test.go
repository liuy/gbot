package tui

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ↑input rides the engine's per-turn estimate until the turn's usage lands,
// then the billed total replaces it. Input is billed per turn over the whole
// context, so the target is the query's confirmed usage plus the in-flight
// turn's estimate.
func TestTurnStartMsg_SetsInputTokenTarget(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.status.SetUsage(types.Usage{})

	if _, cmd := app.updateRepl(turnStartMsg{InputEstimate: 4321}); cmd == nil {
		t.Fatal("expected a command from the first turn_start while streaming")
	}
	if got := app.repl.inputTokenTarget; got != 4321 {
		t.Errorf("inputTokenTarget = %d, want 4321 (the turn estimate alone)", got)
	}

	// Turn 1 answers: 1600 billed (1000 new + 500 cache read + 100 cache write).
	app.updateRepl(usageMsg{InputTokens: 1000, CacheReadInputTokens: 500, CacheCreationInputTokens: 100, OutputTokens: 20})
	if got := app.repl.inputTokenTarget; got != 1600 {
		t.Errorf("inputTokenTarget = %d, want 1600 after usage replaces the estimate", got)
	}

	// Turn 2's estimate rides on top of the confirmed total.
	app.updateRepl(turnStartMsg{InputEstimate: 2000})
	if got := app.repl.inputTokenTarget; got != 3600 {
		t.Errorf("inputTokenTarget = %d, want 3600 (1600 billed + 2000 estimate)", got)
	}

	// Turn 2's usage replaces it outright — the estimate must not linger. Its
	// billed delta is 1900 + 500 + 100 on top of turn 1's 1600.
	app.updateRepl(usageMsg{InputTokens: 1900, CacheReadInputTokens: 500, CacheCreationInputTokens: 100, OutputTokens: 30})
	if got := app.repl.inputTokenTarget; got != 4100 {
		t.Errorf("inputTokenTarget = %d, want 4100 (1600 + 2500 billed, estimate dropped)", got)
	}
	if got := app.repl.displayedInputTokens; got != 4100 {
		t.Errorf("displayedInputTokens = %d, want 4100 (usage snaps the display)", got)
	}
}

// A background job bills into the same token totals while the main engine's
// turn is still in flight. Its usage event covers the job's request, not the
// main turn's, so it must leave that turn's estimate standing.
func TestUsageMsg_JobTaggedKeepsInFlightEstimate(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.status.SetUsage(types.Usage{})

	app.updateRepl(turnStartMsg{InputEstimate: 300})
	app.updateRepl(usageMsg{InputTokens: 300, OutputTokens: 20})
	// Turn 2 of the main engine: 300 billed + 400 estimate.
	app.updateRepl(turnStartMsg{InputEstimate: 400})
	if got := app.repl.inputTokenTarget; got != 700 {
		t.Fatalf("inputTokenTarget = %d, want 700 before the job's usage lands", got)
	}

	job := &types.AgentMeta{ParentToolUseID: "tu-1", AgentType: "Explore", Depth: 1, JobID: "agent-9"}
	app.updateRepl(usageMsg{InputTokens: 111, OutputTokens: 5, Agent: job})
	if got := app.repl.inputTokenTarget; got != 811 {
		t.Errorf("inputTokenTarget = %d, want 811 (411 billed + the 400 estimate still in flight)", got)
	}
	if got := app.repl.displayedInputTokens; got != 811 {
		t.Errorf("displayedInputTokens = %d, want 811 (the snap must keep the estimate)", got)
	}

	// The spinner tick animates toward max(billed, target): a target that
	// dropped to billed-only would drag the display back down.
	app.updateRepl(spinnerTickMsg{})
	if got := app.repl.displayedInputTokens; got != 811 {
		t.Errorf("displayedInputTokens = %d, want 811 after the tick (no drop to 411)", got)
	}

	// The main engine's own usage covers turn 2: the estimate retires and the
	// billed total takes over — 411 + 1200, with no 400 left riding on top.
	app.updateRepl(usageMsg{InputTokens: 1200, OutputTokens: 10})
	if got := app.repl.inputTokenTarget; got != 1611 {
		t.Errorf("inputTokenTarget = %d, want 1611 (estimate retired)", got)
	}
	if got := app.repl.displayedInputTokens; got != 1611 {
		t.Errorf("displayedInputTokens = %d, want 1611", got)
	}
	app.updateRepl(spinnerTickMsg{})
	if got := app.repl.displayedInputTokens; got != 1611 {
		t.Errorf("displayedInputTokens = %d, want 1611 after the tick (estimate must not reappear)", got)
	}
}

// An estimate that never got its usage (the query was interrupted) must not
// ride into the next query, where it would inflate a job's billed total.
func TestStartQuery_DropsStaleTurnEstimate(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.status.SetUsage(types.Usage{})

	app.updateRepl(turnStartMsg{InputEstimate: 4000})
	if got := app.repl.inputTokenTarget; got != 4000 {
		t.Fatalf("inputTokenTarget = %d, want 4000 (query 1's in-flight estimate)", got)
	}

	// Query 2 opens: StartQuery zeroes the per-query account, submit reseeds.
	app.repl.StartQuery()
	app.status.SetUsage(types.Usage{})
	app.repl.SetInputTokenTarget(900)

	job := &types.AgentMeta{ParentToolUseID: "tu-1", AgentType: "Explore", Depth: 1, JobID: "agent-9"}
	app.updateRepl(usageMsg{InputTokens: 111, OutputTokens: 5, Agent: job})
	if got := app.repl.inputTokenTarget; got != 111 {
		t.Errorf("inputTokenTarget = %d, want 111 (query 1's 4000 estimate must not carry over)", got)
	}
	if got := app.repl.displayedInputTokens; got != 111 {
		t.Errorf("displayedInputTokens = %d, want 111", got)
	}
}

// The submit path seeds a cruder estimate for the same first turn; the engine's
// turn_start must replace it, not stack on top of it.
func TestTurnStartMsg_ReplacesSubmitEstimate(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.status.SetUsage(types.Usage{})
	app.repl.SetInputTokenTarget(777)

	app.updateRepl(turnStartMsg{InputEstimate: 1500})
	if got := app.repl.inputTokenTarget; got != 1500 {
		t.Errorf("inputTokenTarget = %d, want 1500 (engine estimate replaces the submit seed)", got)
	}
}

// A turn_start that starts the streaming region must still land its estimate
// after StartQuery zeroes the counters.
func TestTurnStartMsg_IdleStartAppliesEstimate(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.status.SetUsage(types.Usage{InputTokens: 9000})

	app.updateRepl(turnStartMsg{InputEstimate: 1200})
	if got := app.repl.inputTokenTarget; got != 1200 {
		t.Errorf("inputTokenTarget = %d, want 1200 (usage reset on the fresh query)", got)
	}
}

// Sub-agent turns belong to their parent tool card, not the main progress line.
func TestTurnStartMsg_SubAgentLeavesTargetAlone(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.SetInputTokenTarget(321)

	app.updateRepl(turnStartMsg{
		Agent:         &types.AgentMeta{ParentToolUseID: "tu-1", AgentType: "Explore"},
		InputEstimate: 4321,
	})
	if got := app.repl.inputTokenTarget; got != 321 {
		t.Errorf("inputTokenTarget = %d, want 321 (sub-agent turn_start must not touch it)", got)
	}
}

// Zero estimate keeps whatever the submit path seeded; one token of estimate
// is still an estimate.
func TestTurnStartMsg_ZeroEstimateKeepsTarget(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.status.SetUsage(types.Usage{})
	app.repl.SetInputTokenTarget(654)

	app.updateRepl(turnStartMsg{})
	if got := app.repl.inputTokenTarget; got != 654 {
		t.Errorf("inputTokenTarget = %d, want 654 (absent estimate must not clobber the seed)", got)
	}

	app.updateRepl(turnStartMsg{InputEstimate: 1})
	if got := app.repl.inputTokenTarget; got != 1 {
		t.Errorf("inputTokenTarget = %d, want 1 (the smallest estimate must still apply)", got)
	}
}

// The wire field must survive the event→msg conversion.
func TestConvertEventToMsg_TurnStartCarriesEstimate(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()

	msg := h.convertEventToMsg(types.QueryEvent{Type: types.EventTurnStart, InputEstimate: 2468})
	got, ok := msg.(turnStartMsg)
	if !ok {
		t.Fatalf("converted msg = %T, want turnStartMsg", msg)
	}
	if got.InputEstimate != 2468 {
		t.Errorf("turnStartMsg.InputEstimate = %d, want 2468", got.InputEstimate)
	}
	if got.Agent != nil {
		t.Errorf("turnStartMsg.Agent = %+v, want nil for a main-engine turn", got.Agent)
	}

	sub := &types.AgentMeta{ParentToolUseID: "tu-9", AgentType: "Explore"}
	msg = h.convertEventToMsg(types.QueryEvent{Type: types.EventTurnStart, InputEstimate: 42, Agent: sub})
	gotSub, ok := msg.(turnStartMsg)
	if !ok {
		t.Fatalf("converted msg = %T, want turnStartMsg", msg)
	}
	if gotSub.Agent != sub {
		t.Errorf("turnStartMsg.Agent = %+v, want the sub-agent meta %+v", gotSub.Agent, sub)
	}
	if gotSub.InputEstimate != 42 {
		t.Errorf("turnStartMsg.InputEstimate = %d, want 42", gotSub.InputEstimate)
	}
}
