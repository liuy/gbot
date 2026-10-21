package wui

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// Red-anchor tests for the unified accumulator: slot.streams is one stream
// partitioned by lifetime region — key "" (query world: the chat timeline,
// born at query start, reset at query_end) and one key per background job id
// (session world: the job card, born at job_started). These were written
// before the slot.streams field existed; they must fail to compile first.

// TestStreams_JobFeedAddressing pins single-delivery routing across regions:
// a JobID-tagged process event lands only in that job's region, a no-JobID
// event lands only in the chat root, and a process event for a job with no
// region (no lifecycle event, no discovery) is dropped without a panic.
func TestStreams_JobFeedAddressing(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)

	// job_started creates the region before any process event of the job.
	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running", ParentToolUseID: "tu_job", StartedMs: 1,
	}})
	// Job process event: belongs to the job region only.
	h.Dispatch(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"},
		ToolUse: &types.ToolUseEvent{ID: "tu_inner", Name: "Bash", Summary: "inner"}})
	// No-JobID event (root engine / sync sub-agent): chat region only.
	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "chat"})
	// JobID event for a job the panel never saw started: dropped, no panic,
	// and it must not conjure a region either.
	rec := mutDCaptureSlog(t)
	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_x", JobID: "agent-404"}, Text: "stray"})
	if rec.count(slog.LevelDebug, "process event for unknown job region") != 1 {
		t.Fatalf("dropped process event must log the wui:jobs debug line exactly once")
	}

	slot := c.activeSlotTest(t)
	slot.ssMu.Lock()
	jobStream := slot.streams["agent-1"]
	var jobBlocks []streamBlock
	if jobStream != nil {
		jobBlocks = append([]streamBlock(nil), jobStream.blocks...)
	}
	_, has404 := slot.streams["agent-404"]
	root := slot.streams[""]
	var rootBlocks []streamBlock
	if root != nil {
		rootBlocks = append([]streamBlock(nil), root.blocks...)
	}
	slot.ssMu.Unlock()

	if jobStream == nil {
		t.Fatal("streams[\"agent-1\"] missing — job_started must create the region")
	}
	if jobStream.frozen {
		t.Error("a running job's region must not be frozen (freezing arrives with job_terminated in phase C)")
	}
	if len(jobBlocks) != 1 || jobBlocks[0].Kind != "tool" || jobBlocks[0].ID != "tu_inner" {
		t.Fatalf("job region blocks = %+v, want exactly [tool tu_inner]", jobBlocks)
	}
	if root == nil {
		t.Fatal("streams[\"\"] (chat root) missing — the slot is constructed with it")
	}
	if len(rootBlocks) != 1 || rootBlocks[0].Kind != "text" || rootBlocks[0].Text != "chat" {
		t.Fatalf("root blocks = %+v, want exactly the chat text block — the job event must not leak into the chat region", rootBlocks)
	}
	if has404 {
		t.Error("streams[\"agent-404\"] created — a process event must never create a region")
	}
}

// TestStreams_AnchorFromLifecycleEvent pins the masking anchor's provenance:
// the started shell's ParentToolUseID becomes feedAnchor. The job's own event
// (parent == anchor) masks to the region root even when it is not the first
// event folded, and an event whose parent is neither the anchor nor an
// existing block is dropped rather than adopted as the anchor by first-event
// inference.
func TestStreams_AnchorFromLifecycleEvent(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)

	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running", ParentToolUseID: "tu_job", StartedMs: 1,
	}})
	// First folded event already sits BELOW the job's own level (parent = an
	// inner tool id that has no block yet): with the anchor fixed by the
	// started shell this event is dropped, not anchored on.
	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_deep", JobID: "agent-1"}, Text: "deep-first"})
	// The job's own events (parent == anchor) mask to the region root.
	h.Dispatch(types.QueryEvent{Type: types.EventToolStart,
		Agent:   &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"},
		ToolUse: &types.ToolUseEvent{ID: "tu_outer", Name: "Bash", Summary: "outer"}})
	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"}, Text: "own text"})

	slot := c.activeSlotTest(t)
	slot.ssMu.Lock()
	stream := slot.streams["agent-1"]
	anchor := ""
	var blocks []streamBlock
	if stream != nil {
		anchor = stream.feedAnchor
		blocks = append([]streamBlock(nil), stream.blocks...)
	}
	slot.ssMu.Unlock()

	if stream == nil {
		t.Fatal("streams[\"agent-1\"] missing")
	}
	if anchor != "tu_job" {
		t.Fatalf("feedAnchor = %q, want tu_job (from the started shell, not first-event inference)", anchor)
	}
	if len(blocks) != 2 {
		t.Fatalf("region blocks = %+v, want [tool tu_outer, text] with the deep-first event dropped", blocks)
	}
	if blocks[0].Kind != "tool" || blocks[0].ID != "tu_outer" {
		t.Fatalf("blocks[0] = %+v, want tool tu_outer at the region root (anchor masking)", blocks[0])
	}
	if blocks[1].Kind != "text" || blocks[1].Text != "own text" {
		t.Fatalf("blocks[1] = %+v, want text %q at the region root", blocks[1], "own text")
	}
}

// TestStreams_RootResetOnQueryEnd pins the reset boundary: query_end clears
// only the "" region (the chat timeline dies with the query); a job region
// and its blocks survive — the job's lifetime outlives any one query.
func TestStreams_RootResetOnQueryEnd(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)

	h.Dispatch(types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running", ParentToolUseID: "tu_job", StartedMs: 1,
	}})
	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta,
		Agent: &types.AgentMeta{ParentToolUseID: "tu_job", JobID: "agent-1"}, Text: "job text"})
	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "chat text"})
	h.Dispatch(types.QueryEvent{Type: types.EventQueryEnd})

	slot := c.activeSlotTest(t)
	slot.ssMu.Lock()
	jobStream := slot.streams["agent-1"]
	var jobBlocks []streamBlock
	if jobStream != nil {
		jobBlocks = append([]streamBlock(nil), jobStream.blocks...)
	}
	root := slot.streams[""]
	rootLen := 0
	if root != nil {
		rootLen = len(root.blocks)
	}
	slot.ssMu.Unlock()

	if jobStream == nil || len(jobBlocks) != 1 || jobBlocks[0].Text != "job text" {
		t.Fatalf("job region after query_end = %+v, want the job's text block to survive", jobBlocks)
	}
	if rootLen != 0 {
		t.Fatalf("root blocks after query_end = %d, want 0 (only the \"\" region resets)", rootLen)
	}
}

// TestApplyJobLifecycleEvent_RegionCreationGate pins who may create a stream
// region: only job_started, only the agent family, only a real job id, and
// only on a slot whose streams map exists. Every other lifecycle shape must
// leave the region set untouched.
func TestApplyJobLifecycleEvent_RegionCreationGate(t *testing.T) {
	c := newTestConnector(t)

	// bash started: a row, but never a region.
	slot := mutDSlot()
	c.applyJobLifecycleEvent(slot, types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "bg-1", Kind: "bash", Status: "running", Command: "sleep 1", StartedMs: 1,
	}})
	slot.ssMu.Lock()
	bashRegion := slot.streams["bg-1"]
	n := len(slot.streams)
	slot.ssMu.Unlock()
	if bashRegion != nil || n != 1 {
		t.Fatalf("bash started created a region (bg-1=%v, streams=%d keys) — regions are agent-family only", bashRegion != nil, n)
	}

	// terminated for an unknown agent row: the row is created, the region is
	// not — only started owns creation.
	slot2 := mutDSlot()
	c.applyJobLifecycleEvent(slot2, types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "agent-9", Kind: "agent", Status: "failed", ExitCode: 3, ParentToolUseID: "tu_m", StartedMs: 2,
	}})
	slot2.ssMu.Lock()
	termRegion := slot2.streams["agent-9"]
	n = len(slot2.streams)
	slot2.ssMu.Unlock()
	if termRegion != nil || n != 1 {
		t.Fatalf("terminated created a region (agent-9=%v, streams=%d keys) — only job_started creates regions", termRegion != nil, n)
	}
	if _, ok := slot2.jobView["agent-9"]; !ok {
		t.Error("terminated for an unknown row must still create the row")
	}

	// started with an empty id: no region, and the chat root must survive
	// ("" is the root's reserved key — an id-less started must not clobber it).
	slot3 := mutDSlot()
	slot3.streams[""] = &streamState{blocks: []streamBlock{{Kind: "text", Text: "root"}}}
	c.applyJobLifecycleEvent(slot3, types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "", Kind: "agent", Status: "running", ParentToolUseID: "tu_p", StartedMs: 3,
	}})
	slot3.ssMu.Lock()
	root := slot3.streams[""]
	n = len(slot3.streams)
	slot3.ssMu.Unlock()
	if n != 1 || root == nil || len(root.blocks) != 1 || root.blocks[0].Text != "root" {
		t.Fatalf("empty-id started touched the region set (streams=%d keys, root blocks=%v) — the root must survive untouched", n, root)
	}

	// jobView present but streams nil (test-built slot shape): started must
	// be a silent skip, not a nil-map write.
	slot4 := &engineSlot{jobView: map[string]*jobViewEntry{}, streams: nil}
	c.applyJobLifecycleEvent(slot4, types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "agent-1", Kind: "agent", Status: "running", ParentToolUseID: "tu_p", StartedMs: 4,
	}})
	if slot4.streams != nil {
		t.Error("started must not conjure a streams map on a map-less slot")
	}

	// Empty-id started on a root-less map: must not conjure the root either —
	// region creation is keyed by a real job id only.
	slot5 := &engineSlot{jobView: map[string]*jobViewEntry{}, streams: map[string]*streamState{}}
	c.applyJobLifecycleEvent(slot5, types.QueryEvent{Type: types.EventJobStarted, Job: &types.JobEvent{
		ID: "", Kind: "agent", Status: "running", ParentToolUseID: "tu_p", StartedMs: 5,
	}})
	slot5.ssMu.Lock()
	n5 := len(slot5.streams)
	slot5.ssMu.Unlock()
	if n5 != 0 {
		t.Fatalf("empty-id started created %d region(s) — the \"\" key belongs to the chat root, never a job", n5)
	}

	// Empty-id terminated must not touch the root region either: "" is the
	// chat root, so truncating/freezing it would kill the whole timeline.
	slot6 := &engineSlot{jobView: map[string]*jobViewEntry{}, streams: map[string]*streamState{"": {}}}
	for i := 0; i < jobTerminalLastN+1; i++ {
		slot6.streams[""].blocks = append(slot6.streams[""].blocks, streamBlock{Text: fmt.Sprintf("root %d", i)})
	}
	c.applyJobLifecycleEvent(slot6, types.QueryEvent{Type: types.EventJobTerminated, Job: &types.JobEvent{
		ID: "", Kind: "agent", Status: "failed", ExitCode: 1, StartedMs: 6,
	}})
	slot6.ssMu.Lock()
	root6 := slot6.streams[""]
	rootBlocks := len(root6.blocks)
	rootFrozen := root6.frozen
	slot6.ssMu.Unlock()
	if rootBlocks != jobTerminalLastN+1 || rootFrozen {
		t.Fatalf("empty-id terminated touched the root (blocks=%d frozen=%v), want %d blocks unfrozen",
			rootBlocks, rootFrozen, jobTerminalLastN+1)
	}
}
