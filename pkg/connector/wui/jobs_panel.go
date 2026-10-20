package wui

import (
	"encoding/json"
	"log/slog"
	"math"
	"sort"
	"sync/atomic"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// bashTailInterval is a package-level, test-overridable cadence knob for the
// bash tail sampling loop. Atomic-backed because the loop goroutine reads it
// on every beat while tests rewrite it around construction/teardown — a plain
// var would race under -race when a not-yet-exited loop ticks during a test's
// cleanup restore.
var bashTailIntervalNS = atomic.Int64{}

func init() {
	bashTailIntervalNS.Store(int64(500 * time.Millisecond))
}

func bashTailInterval() time.Duration { return time.Duration(bashTailIntervalNS.Load()) }

// View/feed bounds. Over-cap actions log: they indicate either a runaway
// spawn loop or a job id that never resolves to a registry row.
// jobFeedMaxBlocks is mirrored client-side by JOB_FEED_MAX_BLOCKS in
// web/ui/src/jobs_store.ts — the live feed must stay byte-identical to this
// cache or hydration diverges.
const (
	jobsViewCap      = 200
	jobFeedMaxBlocks = 400
	// jobTerminalLastN is the terminal-region payload policy: job_terminated
	// truncates the agent-family region to the last N blocks and freezes it.
	// Terminal takeover snapshots embed these; live snapshots embed the full
	// tree (≤ jobFeedMaxBlocks).
	jobTerminalLastN = 20
)

// ---------------------------------------------------------------------------
// Wire shapes
// ---------------------------------------------------------------------------

// jobWireItem is one background job row on the wire.
type jobWireItem struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`   // "bash" | "agent" | "fork"
	Status          string `json:"status"` // running|completed|failed|killed
	Command         string `json:"command,omitempty"`
	Description     string `json:"description,omitempty"`
	AgentType       string `json:"agentType,omitempty"`
	ParentToolUseID string `json:"parentToolUseID,omitempty"`
	ExitCode        int    `json:"exitCode,omitempty"`
	StartedMs       int64  `json:"startedMs"`
}

// jobListOutbound is the periodic full-snapshot frame (pushed on diff only).
type jobListOutbound struct {
	Type string        `json:"type"` // "job_list"
	Jobs []jobWireItem `json:"jobs"`
}

// jobOutputOutbound is the bash tail frame (pushed on diff only).
type jobOutputOutbound struct {
	Type  string `json:"type"` // "job_output"
	JobID string `json:"jobID"`
	Tail  string `json:"tail"`
}

// jobWireItemHydrated is a job_list entry as embedded in the metadata frame:
// agent entries carry the server-cached feed blocks, bash entries carry the
// current tail — so a reconnecting client restores feeds without replay.
type jobWireItemHydrated struct {
	jobWireItem
	Blocks []streamBlock `json:"blocks,omitempty"`
	Tail   string        `json:"tail,omitempty"`
}

// jobsMetadataOutbound is the metadata frame's embedded jobs field.
type jobsMetadataOutbound struct {
	Jobs []jobWireItemHydrated `json:"jobs"`
}

// jobViewEntry is the connector's display snapshot for one job. The tail
// cache and the diff baseline live on the entry so they die with it — an
// independent tail map would have no closed cleanup point (see
// deleteJobEntryLocked). After the registry evicts the job, tail simply stops
// updating: its last value is the frozen terminal tail.
type jobViewEntry struct {
	item     jobWireItem
	tail     string // last JobTail result (fetched outside ssMu, written back under it)
	tailSent string // job_output baseline already pushed to the client
}

// ---------------------------------------------------------------------------
// Bash tail sampling loop (the only remaining poller)
// ---------------------------------------------------------------------------

// startBashTailLoop launches the per-slot bash tail sampler. One goroutine per
// slot; it exits when the connector's done channel closes (Stop). Rows
// themselves are event-driven (job lifecycle events); this loop only samples
// bash output tails.
func (c *WUIConnector) startBashTailLoop(slot *engineSlot) {
	go c.bashTailLoop(slot)
}

func (c *WUIConnector) bashTailLoop(slot *engineSlot) {
	ticker := time.NewTicker(bashTailInterval())
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.bashTailTick(slot)
		}
	}
}

// jobHasFeed reports whether a wire kind belongs to the agent family
// (agent-N registry jobs, forks included) — everything the panel streams a
// feed for, i.e. everything that is not a bash tail.
func jobHasFeed(kind string) bool { return kind != "bash" }

func isTerminalJobStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "killed"
}

// bashTailTick is one tail beat. Lock discipline (deadlock-critical):
//   - engineClient methods (JobTail) are called holding NO connector lock.
//     StreamingOutput.Write holds s.mu across its synchronous onProgress →
//     hub.Dispatch → onEngineEvent chain, which takes ssMu. Calling JobTail
//     (which takes s.mu) while holding ssMu would close the cycle
//     ssMu → s.mu → ssMu.
//   - the ssMu critical sections do pure local work only (cache write-back,
//     snapshot capture) — never engineClient calls; frame marshal/diff runs
//     outside via renderJobFrames.
func (c *WUIConnector) bashTailTick(slot *engineSlot) {
	// Slots built directly by tests bypass registerEngine and have no jobs
	// maps; a nil map write would panic. Production slots always have them.
	if slot.jobView == nil {
		return
	}

	slot.ssMu.Lock()
	tailIDs := make([]string, 0, len(slot.jobView))
	for id, entry := range slot.jobView {
		if entry.item.Kind == "bash" {
			tailIDs = append(tailIDs, id)
		}
	}
	slot.ssMu.Unlock()

	// Tail fetch, lock-free: results are staged locally and written back in
	// the next critical section. Activity does not gate this — caches stay
	// fresh while disconnected so reconnect metadata is current. Terminal
	// bash rows stay listed until cleared; the registry answers not-found
	// for evicted ids and the row simply keeps its frozen tail.
	tails := make(map[string]string, len(tailIDs))
	for _, id := range tailIDs {
		if tail, ok := slot.engine.JobTail(id); ok {
			tails[id] = tail
		}
	}

	slot.ssMu.Lock()
	for id, tail := range tails {
		if entry, ok := slot.jobView[id]; ok {
			entry.tail = tail
		}
	}
	snap := c.captureJobFramesLocked(slot)
	slot.ssMu.Unlock()

	for _, p := range c.renderJobFrames(slot, snap) {
		c.sendWS([]byte(p))
	}
}

// evictTerminalOverCapLocked drops the oldest terminal rows until the view
// is at cap. Running rows are never evicted — the panel must never lose a
// live job's controls. Caller holds ssMu.
func (c *WUIConnector) evictTerminalOverCapLocked(slot *engineSlot) {
	for len(slot.jobView) > jobsViewCap {
		victimID := ""
		victimStarted := int64(math.MaxInt64)
		for id, entry := range slot.jobView {
			if isTerminalJobStatus(entry.item.Status) && entry.item.StartedMs < victimStarted {
				victimStarted = entry.item.StartedMs
				victimID = id
			}
		}
		if victimID == "" {
			break
		}
		slog.Info("wui:jobs: view over cap, evicting oldest terminal", "id", victimID, "startedMs", victimStarted)
		c.deleteJobEntryLocked(slot, victimID)
	}
}

// ---------------------------------------------------------------------------
// Takeover reconcile
// ---------------------------------------------------------------------------

// reconcileJobsFromRegistry folds one registry enumeration into the job view
// at takeover (sendMetadata). Rows are event-driven; this is the repair pass
// for events the panel never saw:
//   - a registry job with no row gets one (shell from JobInfo; nil and
//     foreground-registered entries are skipped — foreground bash never
//     enters the panel). Agent-family rows also get their stream region,
//     born frozen when the registry already reports them terminal (their
//     process events were never seen and never will be).
//   - a RUNNING row the registry no longer lists is gone for good — dropped,
//     but only when its StartedMs strictly precedes the enumeration instant
//     (now): a row minted at or after now was created by a job_started event
//     that landed after the snapshot was taken, exactly the miss this
//     reconcile repairs; it belongs to the next pass. Terminal entries are
//     deliberately kept frozen (display-until-cleared).
//   - cap: evict the oldest TERMINAL entries until at cap. Running rows are
//     never evicted — the panel must never lose a live job's controls.
//
// Caller holds ssMu; summaries/now were gathered outside it (sendMetadata).
func (c *WUIConnector) reconcileJobsFromRegistry(slot *engineSlot, summaries []*job.JobInfo, now time.Time) {
	if slot.jobView == nil {
		return // test-built slot: nothing to reconcile into
	}
	seen := make(map[string]bool, len(summaries))
	for _, j := range summaries {
		if j == nil || !j.Backgrounded {
			continue // foreground-registered bash never enters the panel
		}
		seen[j.ID] = true
		if _, ok := slot.jobView[j.ID]; ok {
			continue // events own row updates; reconcile only fills gaps
		}
		item := jobWireItem{
			ID:              j.ID,
			Kind:            j.Type,
			Status:          j.Status,
			Command:         j.Command,
			Description:     j.Description,
			AgentType:       j.AgentType,
			ParentToolUseID: j.ParentToolUseID,
			ExitCode:        j.ExitCode,
			StartedMs:       j.StartedMs,
		}
		slot.jobView[j.ID] = &jobViewEntry{item: item}
		if jobHasFeed(item.Kind) && slot.streams != nil && slot.streams[j.ID] == nil {
			// Never overwrite: blocks folded into an existing region must
			// survive the reconcile's first sighting.
			slot.streams[j.ID] = &streamState{
				feedAnchor: j.ParentToolUseID,
				frozen:     isTerminalJobStatus(j.Status),
			}
		}
	}
	for id, entry := range slot.jobView {
		if !seen[id] && entry.item.Status == "running" && entry.item.StartedMs < now.UnixMilli() {
			slog.Info("wui:jobs: running job missing from registry, dropping", "id", id)
			c.deleteJobEntryLocked(slot, id)
		}
	}
	c.evictTerminalOverCapLocked(slot)
}

// ---------------------------------------------------------------------------
// Lifecycle event fast path (event-driven rows)
// ---------------------------------------------------------------------------

// isJobLifecycleEventType reports whether the event is a background-job
// lifecycle transition (row shell in the Job payload).
func isJobLifecycleEventType(t types.QueryEventType) bool {
	switch t {
	case types.EventJobStarted, types.EventJobStatus, types.EventJobTerminated:
		return true
	}
	return false
}

// jobEventToWireItem converts a lifecycle event's payload to the panel row.
// The event IS the row shell — every field maps 1:1.
func jobEventToWireItem(ev *types.JobEvent) jobWireItem {
	return jobWireItem{
		ID:              ev.ID,
		Kind:            ev.Kind,
		Status:          ev.Status,
		Command:         ev.Command,
		Description:     ev.Description,
		AgentType:       ev.AgentType,
		ParentToolUseID: ev.ParentToolUseID,
		ExitCode:        ev.ExitCode,
		StartedMs:       ev.StartedMs,
	}
}

// applyJobLifecycleEvent folds one lifecycle event into the job view: upsert
// the row (existing rows take status/exitCode, new rows take the full shell),
// enforce the view cap, and return the diff-gated job_list payloads for the
// caller to push OUTSIDE the lock (outQueue pushes may block; never hold ssMu
// across one). Runs entirely inside one ssMu section — the unified lock
// domain. job_started owns the region-creation right for the agent family:
// Spawn emits it before the job's first process event (structural
// precedence), so the region — with its masking anchor from the lifecycle
// shell — always exists by the time process events arrive.
func (c *WUIConnector) applyJobLifecycleEvent(slot *engineSlot, event hub.Event) []string {
	slot.ssMu.Lock()
	if slot.jobView != nil {
		ev := event.Job
		if entry, ok := slot.jobView[ev.ID]; ok {
			entry.item.Status = ev.Status
			entry.item.ExitCode = ev.ExitCode
		} else {
			slot.jobView[ev.ID] = &jobViewEntry{item: jobEventToWireItem(ev)}
		}
		// ev.ID can never be "": job ids are registry-minted, and "" is the
		// reserved chat-root key of slot.streams.
		if event.Type == types.EventJobStarted && ev.ID != "" && jobHasFeed(ev.Kind) && slot.streams != nil {
			// Keep an already-present region (reconcile fallback or a
			// repeated started): blocks folded before must survive.
			if slot.streams[ev.ID] == nil {
				slot.streams[ev.ID] = &streamState{feedAnchor: ev.ParentToolUseID}
			}
		}
		// Terminal payload policy: job_terminated truncates the agent-family
		// region to the last jobTerminalLastN blocks and freezes it. Terminal
		// events for rows without a region (missed started) create no region —
		// there is nothing to truncate.
		if event.Type == types.EventJobTerminated && ev.ID != "" && jobHasFeed(ev.Kind) && slot.streams != nil {
			if stream := slot.streams[ev.ID]; stream != nil {
				if n := len(stream.blocks); n > jobTerminalLastN {
					stream.blocks = stream.blocks[n-jobTerminalLastN:]
				}
				stream.frozen = true
			}
		}
		c.evictTerminalOverCapLocked(slot)
	}
	snap := c.captureJobFramesLocked(slot)
	slot.ssMu.Unlock()
	return c.renderJobFrames(slot, snap)
}

// jobsFrameCapture is the snapshot copied out of the ssMu critical section;
// renderJobFrames marshals and diffs it outside the lock so frame
// serialization never sits on the ssMu hot path.
type jobsFrameCapture struct {
	items    []jobWireItem       // sorted row shells
	prevList string              // lastJobsPush baseline at capture time
	tails    []jobOutputOutbound // bash rows with tail != tailSent (tailSent advanced at capture)
}

// captureJobFramesLocked snapshots the view and advances the tail baselines.
// Gated on an active client: inactive slots keep caches fresh (tails are
// still written back in bashTailTick) but push nothing. Caller holds ssMu;
// the payloads come back from renderJobFrames, outside the lock.
//
// An always-empty view captures nothing: a client that never had jobs has
// nothing to clear, and silence keeps loop noise off every WS session. A
// transition to empty (last job gone) still diffs against a non-empty
// baseline in render and pushes the clearing frame.
func (c *WUIConnector) captureJobFramesLocked(slot *engineSlot) *jobsFrameCapture {
	if !slot.active.Load() || c.activeWS.Load() == nil {
		return nil
	}
	items := make([]jobWireItem, 0, len(slot.jobView))
	for _, entry := range slot.jobView {
		items = append(items, entry.item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartedMs != items[j].StartedMs {
			return items[i].StartedMs < items[j].StartedMs
		}
		return items[i].ID < items[j].ID
	})
	if len(items) == 0 && slot.lastJobsPush == "" {
		return nil
	}
	snap := &jobsFrameCapture{items: items, prevList: slot.lastJobsPush}
	bashIDs := make([]string, 0, len(slot.jobView))
	for id, entry := range slot.jobView {
		if entry.item.Kind == "bash" {
			bashIDs = append(bashIDs, id)
		}
	}
	sort.Strings(bashIDs)
	for _, id := range bashIDs {
		entry := slot.jobView[id]
		if entry.tail == entry.tailSent {
			continue
		}
		snap.tails = append(snap.tails, jobOutputOutbound{Type: "job_output", JobID: id, Tail: entry.tail})
		entry.tailSent = entry.tail // advanced inside the capture section: no duplicate job_output window
	}
	return snap
}

// renderJobFrames marshals the capture and diffs it against the captured
// baseline. Enters with NO lock held; a changed list takes ssMu briefly to
// settle the baseline. Accepted benign race (by design): a concurrent
// capture/render pair can push the same job_list frame twice — the frame is
// a full-snapshot replacement, idempotent for the client, and the baseline
// converges in the write-back. job_output has no such window (tailSent
// advances inside the capture section; the next beat is silent).
func (c *WUIConnector) renderJobFrames(slot *engineSlot, snap *jobsFrameCapture) []string {
	if snap == nil {
		return nil
	}
	var payloads []string
	if payload, err := json.Marshal(jobListOutbound{Type: "job_list", Jobs: snap.items}); err == nil {
		if string(payload) != snap.prevList {
			slot.ssMu.Lock()
			slot.lastJobsPush = string(payload)
			slot.ssMu.Unlock()
			payloads = append(payloads, string(payload))
		}
	}
	for _, out := range snap.tails {
		if payload, err := json.Marshal(out); err == nil {
			payloads = append(payloads, string(payload))
		}
	}
	return payloads
}

// pushJobsSnapshot force-pushes the current job_list (job_stop ack). Clears
// the diff baseline instead of re-fetching tails: the client already holds
// each job's tail; a changed tail arrives on the next beat.
func (c *WUIConnector) pushJobsSnapshot(slot *engineSlot) {
	if slot.jobView == nil {
		return
	}
	slot.ssMu.Lock()
	// Reset to a sentinel, not "": at stop time the cached view has usually
	// not caught up with the kill yet, so the marshaled payload equals the
	// baseline and the diff would swallow the ack. "\x00" can never equal a
	// marshaled payload, so the frame is forced out and the baseline settles
	// on the real JSON.
	slot.lastJobsPush = "\x00"
	snap := c.captureJobFramesLocked(slot)
	slot.ssMu.Unlock()
	for _, p := range c.renderJobFrames(slot, snap) {
		c.sendWS([]byte(p))
	}
}

// buildJobsMetadata renders the metadata frame's embedded jobs snapshot.
// Caller holds ssMu (sendMetadata) — the unified lock domain; the marshal
// stays inside the ssMu section on purpose: the handshake is a one-shot
// assembly, not a per-event hot path. It must NOT call JobTail: that is the
// middle edge of the deadlock cycle documented on bashTailTick. Tails come
// from the entry cache, which the tail loop refreshes outside ssMu.
func (c *WUIConnector) buildJobsMetadata(slot *engineSlot) json.RawMessage {
	if len(slot.jobView) == 0 {
		return nil
	}
	items := make([]jobWireItemHydrated, 0, len(slot.jobView))
	for _, entry := range slot.jobView {
		h := jobWireItemHydrated{jobWireItem: entry.item}
		switch entry.item.Kind {
		case "bash":
			h.Tail = entry.tail
		default:
			if stream, ok := slot.streams[entry.item.ID]; ok {
				h.Blocks = stream.blocks
			}
		}
		items = append(items, h)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartedMs != items[j].StartedMs {
			return items[i].StartedMs < items[j].StartedMs
		}
		return items[i].ID < items[j].ID
	})
	raw, err := json.Marshal(jobsMetadataOutbound{Jobs: items})
	if err != nil {
		return nil
	}
	return raw
}

// deleteJobEntryLocked is the ONLY removal path for a jobView entry: it also
// drops the entry's stream region. Bypassing it orphans the region and later
// events would land in a dead region that is never reclaimed. Caller holds
// ssMu.
func (c *WUIConnector) deleteJobEntryLocked(slot *engineSlot, id string) {
	entry, ok := slot.jobView[id]
	if !ok {
		return
	}
	delete(slot.jobView, id)
	if !jobHasFeed(entry.item.Kind) {
		return
	}
	delete(slot.streams, entry.item.ID)
}

// clearTerminalJobsLocked drops all terminal entries — session switch/new
// semantics (the previous session's finished jobs must not leak into the new
// session's panel). Running entries belong to the engine, not the session,
// and stay. Caller holds ssMu.
func (c *WUIConnector) clearTerminalJobsLocked(slot *engineSlot) {
	for id, entry := range slot.jobView {
		if isTerminalJobStatus(entry.item.Status) {
			c.deleteJobEntryLocked(slot, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Job stream regions (process events)
// ---------------------------------------------------------------------------

// isJobFeedEventType filters to the event types updateStreamState understands
// for block building. Everything else (usage, turn lifecycle, query frames)
// contributes no blocks.
func isJobFeedEventType(t types.QueryEventType) bool {
	switch t {
	case types.EventTextDelta, types.EventToolStart, types.EventToolParamDelta,
		types.EventToolEnd, types.EventToolOutputDelta, types.EventAttachment,
		types.EventThinkingStart, types.EventThinkingDelta, types.EventThinkingEnd:
		return true
	}
	return false
}

// applyJobStreamEvent folds a background-job event into that job's stream
// region. Routing is single-purpose: Agent.JobID addresses the region
// directly (key = job id), and the region must already exist — job_started
// created it before the job's first process event, so a missing region means
// a job the panel never saw; the event is dropped. Events without a JobID
// (root engine, sync sub-agents) belong to the chat region only. Called from
// onEngineEvent INSIDE the slot's ssMu critical section — the caller holds
// ssMu (sync.Mutex is not reentrant, so this function must not take it).
//
// Masking: the job's own events (parent == the region's feedAnchor, set from
// the job_started shell) are masked Agent=nil so they land at the region
// root; deeper parents nest by the block tree (targetList/findBlock),
// untouched.
func (c *WUIConnector) applyJobStreamEvent(slot *engineSlot, event hub.Event) {
	if slot.jobView == nil {
		return
	}
	if event.Agent == nil || event.Agent.JobID == "" {
		return // root/sync events: the chat region only, never a job region
	}
	if !isJobFeedEventType(event.Type) {
		return
	}
	jobID := event.Agent.JobID

	stream := slot.streams[jobID]
	if stream == nil {
		slog.Debug("wui:jobs: process event for unknown job region, dropping", "jobID", jobID, "type", event.Type)
		return
	}
	if stream.frozen {
		// Reachable, not defensive: the idle-kick path (a message delivered to
		// a dead job's idle sub-engine kicks a fresh query whose tagged events
		// surface after job_terminated). The card is frozen; the query itself
		// keeps running, only its events are dropped from the panel.
		slog.Debug("wui:jobs: process event for terminated job region, dropping", "jobID", jobID, "type", event.Type)
		return
	}
	if event.Agent.ParentToolUseID == stream.feedAnchor {
		masked := event
		masked.Agent = nil
		updateStreamState(stream, masked)
	} else {
		updateStreamState(stream, event)
	}
	// Rolling cap, head dropped — the tail is the interesting end.
	if n := len(stream.blocks); n > jobFeedMaxBlocks {
		stream.blocks = stream.blocks[n-jobFeedMaxBlocks:]
	}
}

// ---------------------------------------------------------------------------
// Inbound handlers
// ---------------------------------------------------------------------------

// handleJobStop kills a background job on the active engine. KillJob runs
// even on test-built slots without a jobs view; only the snapshot push is
// skipped there (pushJobsSnapshot's nil guard).
func (c *WUIConnector) handleJobStop(jobID string) {
	slot := c.activeSlot()
	if slot == nil || slot.engine == nil {
		return
	}
	if err := slot.engine.KillJob(jobID); err != nil {
		c.sendWS(buildError(err))
		return
	}
	c.pushJobsSnapshot(slot)
}
