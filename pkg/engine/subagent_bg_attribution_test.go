package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Helpers (script-slot pinning + request-content scans)
// ---------------------------------------------------------------------------

// requestTextContains reports whether some text block of the request contains
// s anywhere. Unlike requestHasTextBlock (suffix match), this catches blocks
// the engine wraps — timestamp prefixes and reminder/notification envelopes
// around the original prompt text.
func requestTextContains(req *llm.Request, s string) bool {
	for _, msg := range req.Messages {
		for _, b := range msg.Content {
			if b.Type == types.ContentTypeText && strings.Contains(b.Text, s) {
				return true
			}
		}
	}
	return false
}

// requestCountWhere counts recorded Stream calls whose request satisfies pred.
// Holds m.mu: the log is appended from whichever goroutine calls the provider.
func requestCountWhere(m *mockProvider, pred func(*llm.Request) bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, req := range m.allRequests {
		if req != nil && pred(req) {
			n++
		}
	}
	return n
}

// addMatchedResponse appends a slot served only to requests satisfying match.
// Same-package spelling of mockResponse's match semantics (see bg_task_isolation_test.go).
func addMatchedResponse(m *mockProvider, events []llm.StreamEvent, match func(*llm.Request) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, mockResponse{events: events, match: match})
}

// messagesContain reports whether some text block of some message contains
// every given substring within that single block.
func messagesContain(msgs []types.Message, subs ...string) bool {
	for _, msg := range msgs {
		for _, b := range msg.Content {
			if b.Type != types.ContentTypeText {
				continue
			}
			all := true
			for _, s := range subs {
				if !strings.Contains(b.Text, s) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}

// pollUntil runs fn every 10ms until it returns true or the deadline passes.
func pollUntil(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout) // REAL-TIME: bounded poll for async work
	for !fn() {
		if time.Now().After(deadline) { // REAL-TIME: bounded poll for async work
			t.Fatalf("%s not observed within %s", what, timeout)
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
	}
}

// ---------------------------------------------------------------------------
// Red tests (plan 2026-10-19-subagent-bg-jobs-panel step 1)
// ---------------------------------------------------------------------------

// TestSubAgentBgBash_LandsInRootJobRegistry: a sync sub-agent's background
// bash must land in the session root's job registry — the panel's real data
// source (root JobReg.ListSummaries) — and stay stoppable/killed from there,
// while an unrelated slot stays blind.
func TestSubAgentBgBash_LandsInRootJobRegistry(t *testing.T) {
	deps, _, _ := msgDeps(t)

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test", "call_bash", "Bash",
		`{"command":"sleep 30","description":"sidechain sleeper","run_in_background":true}`), nil)
	mp.addResponse(textStreamEvents("test", "sub done"), nil)

	parent, parentRefs := msgWiredParent(t, mp, "engine-root", deps)
	_, refsB := msgWiredParent(t, &mockProvider{}, "engine-b", deps)

	res, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "spawn sidechain sleeper",
		AgentType: "General",
		MaxTurns:  2,
	})
	if err != nil {
		t.Fatalf("parent RunAgent: %v", err)
	}
	if res == nil {
		t.Fatal("parent RunAgent returned nil result")
	}

	// The panel's real data source API: the root engine's JobReg summaries.
	// Exactly one backgrounded bash row; no other row exists in the script.
	var jobID string
	pollUntil(t, 2*time.Second, "root job registry holding exactly one backgrounded bash row", func() bool {
		summaries := parentRefs.JobReg.ListSummaries()
		bgRows := 0
		for _, s := range summaries {
			if s.Backgrounded {
				bgRows++
			}
		}
		if bgRows != 1 {
			return false
		}
		for _, s := range summaries {
			if s.Backgrounded && s.Type == "bash" && s.Status == "running" && s.Command == "sleep 30" {
				jobID = s.ID
				return true
			}
		}
		return false
	})

	// Terminal recheck: the poll succeeds on a snapshot, so re-count once after
	// it returns — a second backgrounded row appearing in between is a bug.
	bgRows := 0
	for _, s := range parentRefs.JobReg.ListSummaries() {
		if s.Backgrounded {
			bgRows++
		}
	}
	if bgRows != 1 {
		t.Errorf("root job registry holds %d backgrounded rows after the poll, want exactly 1", bgRows)
	}

	// Cross-slot isolation: engine B's registries never see the row.
	for _, info := range refsB.JobReg.List() {
		if info.ID == jobID {
			t.Fatalf("engine B's job list leaks the parent's bg bash %q", jobID)
		}
	}

	// The panel's stop button path: root JobReg.Kill must reach the job.
	var killOnce sync.Once
	t.Cleanup(func() { killOnce.Do(func() { _ = parentRefs.JobReg.Kill(jobID) }) })
	killOnce.Do(func() {
		if err := parentRefs.JobReg.Kill(jobID); err != nil {
			t.Fatalf("root Kill(%q): %v", jobID, err)
		}
	})
	pollUntil(t, 2*time.Second, "bg bash reaching killed after root kill", func() bool {
		info, ok := parentRefs.JobReg.Get(jobID)
		return ok && info.Status == "killed"
	})
}

// ---------------------------------------------------------------------------
// Contract pinning: notification routing (2026-10-19 final ruling, by-design)
//
// Notification routing = whoever spawned the job gets the notification: a live
// engine receives it directly, a returned sync sub engine self-drives one turn
// to read it.
// Root binding (above) is for panel visibility/stop only — it must never
// reroute notifications. These tests pin that: rerouting to the root,
// dropping, or sealing the sub would each leave the sub's wake round
// unobserved (timeout) or leak bytes into the root's history.
// ---------------------------------------------------------------------------

// TestSyncSub_LateBgBashNotification_SelfDrivenBySubEngine: a sync sub-agent
// returns before its background bash completes. The late completion
// notification must wake the SUB engine itself — one self-driven turn whose
// request carries both the sub's prompt context and the notification bytes —
// and must never reach the root's history.
func TestSyncSub_LateBgBashNotification_SelfDrivenBySubEngine(t *testing.T) {
	deps, _, _ := msgDeps(t)

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test", "call_bash", "Bash",
		`{"command":"sleep 2","description":"sidechain quick","run_in_background":true}`), nil)
	mp.addResponse(textStreamEvents("test", "sub done"), nil)
	// Wake-round slot. sleep 2 completes strictly after the sub's
	// millisecond-scale two-turn script, so this slot survives until the
	// notification-driven round claims it.
	mp.addResponse(textStreamEvents("test", "sub wake noted"), nil)

	parent, _ := msgWiredParent(t, mp, "engine-root", deps)
	// The root CAN self-drive (system prompt set): if the notification were
	// rerouted to the root, IT would consume the wake slot and the sub's wake
	// round below would never happen — both assertions go red together.
	parent.SetSystemPrompt("root prompt")

	res, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "start sidechain quick",
		AgentType: "General",
		// Turn budget persists across queries on the engine: 2 script turns
		// consume 2, so MaxTurns 3 leaves exactly one spare turn for the
		// self-driven wake round this test observes.
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("parent RunAgent: %v", err)
	}
	if res == nil {
		t.Fatal("parent RunAgent returned nil result")
	}
	subCount := requestCountWhere(mp, func(req *llm.Request) bool {
		return requestTextContains(req, "start sidechain quick")
	})

	// The sub engine reads its own mail: exactly one request carries both the
	// sub's prompt context and the completion envelope.
	pollUntil(t, 5*time.Second, "sub engine waking on its own bg bash completion", func() bool {
		return requestCountWhere(mp, func(req *llm.Request) bool {
			return requestTextContains(req, "start sidechain quick") &&
				requestTextContains(req, "<job-notification>") &&
				requestTextContains(req, "completed (exit code 0)")
		}) == 1
	})

	// Exactly one self-driven round — no runaway loop after the job ends.
	if n := requestCountWhere(mp, func(req *llm.Request) bool {
		return requestTextContains(req, "start sidechain quick")
	}); n != subCount+1 {
		t.Errorf("requests containing the sub prompt = %d after the wake round (was %d at RunAgent return), want exactly one more", n, subCount)
	}
	// The root's history never receives the sub's notification.
	if messagesContain(parent.Messages(), "<job-notification>") {
		t.Error("root history contains a job notification — the sub's late notification was rerouted to the root")
	}
}

// TestSyncSub_LateBgAgentNotification_SelfDrivenBySubEngine: same contract for
// a background GRANDCHILD agent spawned by a sync sub-agent: its completion
// notification wakes the sub (mid-run boundary or self-driven round), never
// the root.
func TestSyncSub_LateBgAgentNotification_SelfDrivenBySubEngine(t *testing.T) {
	deps, _, _ := msgDeps(t)

	mp := &mockProvider{}
	// Sub turn 1: spawn a background grandchild agent. Unmatched slot — the
	// sub's first request is the only contender.
	mp.addResponse(toolUseStreamEvents("test", "call_gc", "Agent",
		`{"description":"gc","prompt":"gc-work","run_in_background":true}`), nil)
	// Grandchild's own turn.
	addMatchedResponse(mp, textStreamEvents("test", "gc done"), func(req *llm.Request) bool {
		return requestHasTextBlock(req, "gc-work")
	})
	// Sub turn 2 and the wake round share the "start grandchild" predicate;
	// match-slot FIFO guarantees turn 2 takes "sub done" first and the wake
	// round (whenever it runs) takes the next one.
	addMatchedResponse(mp, textStreamEvents("test", "sub done"), func(req *llm.Request) bool {
		return requestHasTextBlock(req, "start grandchild")
	})
	addMatchedResponse(mp, textStreamEvents("test", "sub wake noted"), func(req *llm.Request) bool {
		return requestHasTextBlock(req, "start grandchild")
	})

	parent, _ := msgWiredParent(t, mp, "engine-root", deps)
	parent.SetSystemPrompt("root prompt")

	res, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "start grandchild",
		AgentType: "General",
		// MaxTurns 3 leaves one spare turn: under the late interleaving the
		// wake round needs budget to issue its read request.
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("parent RunAgent: %v", err)
	}
	if res == nil {
		t.Fatal("parent RunAgent returned nil result")
	}

	// Exactly one request carries both the sub's prompt and the grandchild
	// completion bytes. The grandchild's finish may land before sub turn 2
	// (mid-run boundary read) or after RunAgent returns (self-driven wake) —
	// the count is 1 under both interleavings.
	pollUntil(t, 5*time.Second, "sub engine reading its grandchild's completion", func() bool {
		return requestCountWhere(mp, func(req *llm.Request) bool {
			return requestTextContains(req, "start grandchild") &&
				requestTextContains(req, `Background agent "gc" completed`)
		}) == 1
	})

	if messagesContain(parent.Messages(), `Background agent "gc" completed`) {
		t.Error("root history contains the grandchild completion notification — it was rerouted to the root")
	}
}

// ---------------------------------------------------------------------------
// Guard tests (plan step 4: delivery-time routing invariants)
// ---------------------------------------------------------------------------

// A live background parent agent must be woken by its OWN background job's
// completion — tool-level priority must survive the root binding (guard
// against an A1 "always notify root" regression). The parent engine never
// runs a query (no system prompt), so the wake turn is attributable to the
// bg parent alone.
func TestBgParentAgent_WokenByOwnBgJobCompletion(t *testing.T) {
	deps, _, _ := msgDeps(t)

	mp := &mockProvider{}
	addMatchedResponse(mp, toolUseStreamEvents("test", "call_bash", "Bash",
		`{"command":"sleep 1","description":"waker job","run_in_background":true}`), func(req *llm.Request) bool {
		return requestHasTextBlock(req, "bgparent-go")
	})
	addMatchedResponse(mp, textStreamEvents("test", "bg parent idle"), func(req *llm.Request) bool {
		return requestHasTextBlock(req, "bgparent-go")
	})
	addMatchedResponse(mp, textStreamEvents("test", "woke: saw job notification"), func(req *llm.Request) bool {
		return requestHasTextBlock(req, "bgparent-go")
	})

	_, parentRefs := msgWiredParent(t, mp, "waker-root", deps)

	res, err := parentRefs.Agent.Call(context.Background(),
		[]byte(`{"description":"bg parent","prompt":"bgparent-go","run_in_background":true}`),
		&tool.ToolUseContext{ToolUseID: "t-bg"})
	if err != nil {
		t.Fatalf("Agent.Call(background): %v", err)
	}
	sub, ok := res.Data.(*types.SubQueryResult)
	if !ok {
		t.Fatalf("Call data type = %T, want *types.SubQueryResult", res.Data)
	}
	agentID := sub.AgentID
	if agentID == "" {
		t.Fatal("spawn returned empty AgentID")
	}
	t.Cleanup(func() {
		parentRefs.Agent.Registry().Cancel(agentID)
		parentRefs.Agent.Registry().Wait(agentID)
	})

	var subEng *Engine
	pollUntil(t, 2*time.Second, "bg parent sink attachment", func() bool {
		st, ok := parentRefs.Agent.Registry().Get(agentID)
		if !ok || st.Sink == nil {
			return false
		}
		subEng, ok = st.Sink.(*Engine)
		return ok
	})

	pollUntil(t, 5*time.Second, "bg parent receiving its own job completion notification and waking", func() bool {
		msgs := subEng.Messages()
		return messagesContain(msgs, "<job-notification>", "completed (exit code 0)") &&
			lastAssistantText(msgs) == "woke: saw job notification"
	})
}

// A sync sub-agent mid-run must receive its OWN background job's notification
// at a turn boundary (guard: root binding must not intercept bash
// notifications at table level and reroute them to the root engine).
func TestSyncSub_MidRunReceivesOwnBgJobNotification(t *testing.T) {
	deps, _, _ := msgDeps(t)

	mp := &mockProvider{}
	matchSub := func(req *llm.Request) bool { return requestHasTextBlock(req, "midrunsub-go") }
	addMatchedResponse(mp, toolUseStreamEvents("test", "call_bg", "Bash",
		`{"command":"sleep 1","description":"midrun job","run_in_background":true}`), matchSub)
	// Foreground sleep 2 blocks the turn; the bg job completes (~1s) while the
	// sub is mid-run, so the notification lands at the turn-2→3 boundary.
	addMatchedResponse(mp, toolUseStreamEvents("test", "call_block", "Bash",
		`{"command":"sleep 2","description":"blocking turn"}`), matchSub)
	addMatchedResponse(mp, textStreamEvents("test", "sub saw notification"), matchSub)

	parent, _ := msgWiredParent(t, mp, "engine-root", deps)

	res, err := parent.RunAgent(context.Background(), agenttool.AgentOpts{
		Prompt:    "midrunsub-go",
		AgentType: "General",
		MaxTurns:  3,
	})
	if err != nil {
		t.Fatalf("parent RunAgent: %v", err)
	}
	if res == nil || res.Content != "sub saw notification" {
		t.Fatalf("RunAgent result = %+v, want Content %q", res, "sub saw notification")
	}

	// Exactly one request carries BOTH the sub's own prompt and the
	// notification envelope: the sub's post-notification turn. Rerouted
	// notifications would leave the sub's requests without the envelope.
	if n := requestCountWhere(mp, func(req *llm.Request) bool {
		return requestTextContains(req, "midrunsub-go") && requestTextContains(req, "<job-notification>")
	}); n != 1 {
		t.Errorf("sub requests carrying own prompt + job notification = %d, want 1 — mid-run delivery was rerouted or lost", n)
	}
}
