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

// jobsWatcherInterval and jobsPendingTTL are package-level, test-overridable
// cadence knobs. Atomic-backed because watcher goroutines read them on every
// beat while tests rewrite them around construction/teardown — a plain var
// would race under -race when a not-yet-exited watcher ticks during a test's
// cleanup restore.
var (
	jobsWatcherIntervalNS = atomic.Int64{}
	jobsPendingTTLNS      = atomic.Int64{}
)

func init() {
	jobsWatcherIntervalNS.Store(int64(500 * time.Millisecond))
	jobsPendingTTLNS.Store(int64(5 * time.Second))
}

func jobsWatcherInterval() time.Duration { return time.Duration(jobsWatcherIntervalNS.Load()) }
func jobsPendingTTL() time.Duration      { return time.Duration(jobsPendingTTLNS.Load()) }

// View/feed bounds. Over-cap actions log: they indicate either a runaway
// spawn loop or a stuck pending parent id.
const (
	jobsViewCap      = 200
	jobFeedMaxBlocks = 400
)

// ---------------------------------------------------------------------------
// Wire shapes
// ---------------------------------------------------------------------------

// jobWireItem is one background job row on the wire.
type jobWireItem struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`   // "bash" | "agent"
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
	tail     string // last JobTail result (fetched outside jobsMu, written back under it)
	tailSent string // job_output baseline already pushed to the client
}

// jobFeedState is the server-side block cache for one background agent's
// event stream. blocks follow the same tree semantics as the live
// streamState (updateStreamState is reused verbatim).
type jobFeedState struct {
	streamState streamState
}

// pendingAgentEvents stashes sub-agent events whose parent job the watcher
// has not discovered yet (job spawned moments ago; jobs list lands on the
// next tick). firstSeen gives the TTL sweep its criterion.
type pendingAgentEvents struct {
	events    []hub.Event
	firstSeen time.Time
}

// ---------------------------------------------------------------------------
// Watcher loop
// ---------------------------------------------------------------------------

// startJobsWatcher launches the per-slot jobs watcher. One goroutine per
// slot; it exits when the connector's done channel closes (Stop).
func (c *WUIConnector) startJobsWatcher(slot *engineSlot) {
	go c.watchJobsLoop(slot)
}

func (c *WUIConnector) watchJobsLoop(slot *engineSlot) {
	ticker := time.NewTicker(jobsWatcherInterval())
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.jobTick(slot)
		}
	}
}

// jobKind maps JobInfo.Type onto the wire vocabulary.
func jobKind(t string) string {
	switch t {
	case "local_bash":
		return "bash"
	case "local_agent":
		return "agent"
	}
	return t
}

func isTerminalJobStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "killed"
}

// jobTick is one watcher beat. Lock discipline (deadlock-critical):
//   - engineClient methods (JobSummaries, JobTail) are called holding NO
//     connector lock. StreamingOutput.Write holds s.mu across its synchronous
//     onProgress → hub.Dispatch → onEngineEvent chain, which takes ssMu; both
//     sendMetadata and captureJobEvent take jobsMu under ssMu. Calling JobTail
//     (which takes s.mu) while holding jobsMu would close the cycle
//     jobsMu → s.mu → ssMu → jobsMu.
//   - the jobsMu critical sections do pure local work only (merge, cache
//     write-back, marshal, string diff) — never engineClient calls.
func (c *WUIConnector) jobTick(slot *engineSlot) {
	// Slots built directly by tests bypass registerEngine and have no jobs
	// maps; a nil map write would panic. Production slots always have them.
	if slot.jobView == nil {
		return
	}

	summaries := slot.engine.JobSummaries()

	slot.jobsMu.Lock()
	tailIDs := c.mergeJobSummariesLocked(slot, summaries)
	slot.jobsMu.Unlock()

	// Tail fetch, lock-free: results are staged locally and written back in
	// the next critical section. Activity does not gate this — caches stay
	// fresh while disconnected so reconnect metadata is current.
	tails := make(map[string]string, len(tailIDs))
	for _, id := range tailIDs {
		if tail, ok := slot.engine.JobTail(id); ok {
			tails[id] = tail
		}
	}

	slot.jobsMu.Lock()
	for id, tail := range tails {
		if entry, ok := slot.jobView[id]; ok {
			entry.tail = tail
		}
	}
	payloads := c.collectJobFramesLocked(slot)
	slot.jobsMu.Unlock()

	for _, p := range payloads {
		c.sendWS([]byte(p))
	}
}

// mergeJobSummariesLocked folds a registry enumeration into jobView and
// returns the bash ids whose tails should be fetched this beat (running and
// not-yet-evicted terminal — the bash registry evicts terminal jobs ~3s
// after completion, and the final tail must be captured before then).
// Caller holds jobsMu.
func (c *WUIConnector) mergeJobSummariesLocked(slot *engineSlot, summaries []*job.JobInfo) []string {
	seen := make(map[string]bool, len(summaries))
	for _, j := range summaries {
		if j == nil || !j.Backgrounded {
			continue // foreground-registered bash never enters the panel
		}
		seen[j.ID] = true
		if entry, ok := slot.jobView[j.ID]; ok {
			entry.item.Status = j.Status
			entry.item.ExitCode = j.ExitCode
			continue
		}
		item := jobWireItem{
			ID:              j.ID,
			Kind:            jobKind(j.Type),
			Status:          j.Status,
			Command:         j.Command,
			Description:     j.Description,
			AgentType:       j.AgentType,
			ParentToolUseID: j.ParentToolUseID,
			ExitCode:        j.ExitCode,
			StartedMs:       j.StartedMs,
		}
		slot.jobView[j.ID] = &jobViewEntry{item: item}
		if item.Kind == "agent" && item.ParentToolUseID != "" {
			slot.jobFeeds[item.ParentToolUseID] = &jobFeedState{}
			// Events that arrived before the watcher saw the job were parked
			// in pendingAgentEvents; replay them now that the feed exists.
			if p := slot.pendingAgentEvents[item.ParentToolUseID]; p != nil {
				for _, ev := range p.events {
					c.applyAgentEventLocked(slot, item.ParentToolUseID, ev)
				}
				delete(slot.pendingAgentEvents, item.ParentToolUseID)
			}
		}
	}
	// A running job that vanished from the registry is gone for good. Terminal
	// entries are deliberately kept frozen (display-until-cleared).
	for id, entry := range slot.jobView {
		if !seen[id] && entry.item.Status == "running" {
			slog.Info("wui:jobs: running job missing from registry, dropping", "id", id)
			c.deleteJobEntryLocked(slot, id)
		}
	}
	// Cap: evict the oldest TERMINAL entries until at cap. Running jobs are
	// never evicted — the panel must never lose a live job's controls.
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
	// TTL sweep: pending events whose job never materialized.
	now := time.Now()
	ttl := jobsPendingTTL()
	for key, p := range slot.pendingAgentEvents {
		if now.Sub(p.firstSeen) > ttl {
			slog.Info("wui:jobs: sweeping stale pending agent events", "parent", key, "count", len(p.events))
			delete(slot.pendingAgentEvents, key)
		}
	}

	var tailIDs []string
	for _, j := range summaries {
		if j == nil || !j.Backgrounded || jobKind(j.Type) != "bash" {
			continue
		}
		if _, ok := slot.jobView[j.ID]; ok {
			tailIDs = append(tailIDs, j.ID)
		}
	}
	return tailIDs
}

// collectJobFramesLocked marshals the current view and diffs it against the
// pushed baselines, returning payloads to send and advancing the baselines.
// Gated on an active client: inactive slots keep caches fresh (tails are
// still written back in jobTick) but push nothing. Caller holds jobsMu;
// sendWS happens outside.
func (c *WUIConnector) collectJobFramesLocked(slot *engineSlot) []string {
	if !(slot.active.Load() && c.activeWS.Load() != nil) {
		return nil
	}
	var payloads []string
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
	// An always-empty view never pushes: a client that never had jobs has
	// nothing to clear, and silence keeps watcher noise off every WS session.
	// A transition to empty (last job gone) still diffs against a non-empty
	// baseline and pushes the clearing frame.
	if len(items) > 0 || slot.lastJobsPush != "" {
		if payload, err := json.Marshal(jobListOutbound{Type: "job_list", Jobs: items}); err == nil {
			if string(payload) != slot.lastJobsPush {
				slot.lastJobsPush = string(payload)
				payloads = append(payloads, string(payload))
			}
		}
	}
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
		if payload, err := json.Marshal(jobOutputOutbound{Type: "job_output", JobID: id, Tail: entry.tail}); err == nil {
			entry.tailSent = entry.tail
			payloads = append(payloads, string(payload))
		}
	}
	return payloads
}

// pushJobsSnapshot force-pushes the current job_list (job_stop / jobs_clear
// acks). Clears the diff baseline instead of re-fetching tails: the client
// already holds each job's tail; a changed tail arrives on the next beat.
func (c *WUIConnector) pushJobsSnapshot(slot *engineSlot) {
	if slot.jobView == nil {
		return
	}
	slot.jobsMu.Lock()
	// Reset to a sentinel, not "": collect's guard suppresses pushes for an
	// empty view with an empty baseline, and jobs_clear of an all-terminal
	// view hits exactly that — the clearing frame would never reach the
	// client. "\x00" can never equal a marshaled payload, so the frame is
	// forced out and the baseline settles on the real JSON.
	slot.lastJobsPush = "\x00"
	payloads := c.collectJobFramesLocked(slot)
	slot.jobsMu.Unlock()
	for _, p := range payloads {
		c.sendWS([]byte(p))
	}
}

// buildJobsMetadata renders the metadata frame's embedded jobs snapshot.
// Called with slot.ssMu held (sendMetadata); takes jobsMu itself — lock order
// ssMu → jobsMu, same as captureJobEvent. It must NOT call JobTail: that is
// the middle edge of the deadlock cycle documented on jobTick. Tails come
// from the entry cache, which the watcher refreshes outside jobsMu.
func (c *WUIConnector) buildJobsMetadata(slot *engineSlot) json.RawMessage {
	slot.jobsMu.Lock()
	defer slot.jobsMu.Unlock()
	if slot.jobView == nil || len(slot.jobView) == 0 {
		return nil
	}
	items := make([]jobWireItemHydrated, 0, len(slot.jobView))
	for _, entry := range slot.jobView {
		h := jobWireItemHydrated{jobWireItem: entry.item}
		switch entry.item.Kind {
		case "agent":
			if feed, ok := slot.jobFeeds[entry.item.ParentToolUseID]; ok {
				h.Blocks = feed.streamState.blocks
			}
		case "bash":
			h.Tail = entry.tail
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
// drops the entry's feed and every innerToolToFeed route pointing into it.
// Bypassing it orphans feed/route keys and routes later events into a dead
// feed that is never reclaimed. Caller holds jobsMu.
func (c *WUIConnector) deleteJobEntryLocked(slot *engineSlot, id string) {
	entry, ok := slot.jobView[id]
	if !ok {
		return
	}
	delete(slot.jobView, id)
	if entry.item.Kind != "agent" || entry.item.ParentToolUseID == "" {
		return
	}
	key := entry.item.ParentToolUseID
	delete(slot.jobFeeds, key)
	for toolID, feedKey := range slot.innerToolToFeed {
		if feedKey == key {
			delete(slot.innerToolToFeed, toolID)
		}
	}
}

// clearTerminalJobsLocked drops all terminal entries — session switch/new
// semantics (the previous session's finished jobs must not leak into the new
// session's panel) and the jobs_clear handler. Running entries belong to the
// engine, not the session, and stay. Caller holds jobsMu.
func (c *WUIConnector) clearTerminalJobsLocked(slot *engineSlot) {
	for id, entry := range slot.jobView {
		if isTerminalJobStatus(entry.item.Status) {
			c.deleteJobEntryLocked(slot, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Sub-agent event capture (feed cache)
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

// captureJobEvent mirrors a sub-agent event into the per-job feed cache for
// metadata hydration. Called from onEngineEvent INSIDE the slot's ssMu
// critical section; takes jobsMu itself — lock order ssMu → jobsMu is
// mandatory (buildJobsMetadata pairs it; the reverse order deadlocks against
// sendMetadata).
//
// Masking: the job's own events (parent == the job's parentToolUseID) are
// masked Agent=nil so they land at the feed root. Events of agents nested
// INSIDE the feed (parent == a tool id registered in innerToolToFeed) keep
// their Agent meta — targetList then nests them under that tool block.
// Unroutable parents park in pendingAgentEvents until the watcher discovers
// the job (or the TTL sweeps them).
func (c *WUIConnector) captureJobEvent(slot *engineSlot, event hub.Event) {
	if slot.jobView == nil {
		return
	}
	if event.Agent == nil || event.Agent.ParentToolUseID == "" {
		return
	}
	if !isJobFeedEventType(event.Type) {
		return
	}
	parent := event.Agent.ParentToolUseID

	slot.jobsMu.Lock()
	defer slot.jobsMu.Unlock()

	if feedKey, ok := c.routeAgentEventLocked(slot, parent); ok {
		c.applyAgentEventLocked(slot, feedKey, event)
		return
	}
	p := slot.pendingAgentEvents[parent]
	if p == nil {
		p = &pendingAgentEvents{firstSeen: time.Now()}
		slot.pendingAgentEvents[parent] = p
	}
	p.events = append(p.events, event)
}

// routeAgentEventLocked resolves a parent id to its feed key: jobFeeds holds
// the job's own parent id, innerToolToFeed maps tool ids opened inside a
// feed back to that feed (transitive — any nesting depth).
func (c *WUIConnector) routeAgentEventLocked(slot *engineSlot, parent string) (string, bool) {
	if _, ok := slot.jobFeeds[parent]; ok {
		return parent, true
	}
	if key, ok := slot.innerToolToFeed[parent]; ok {
		return key, true
	}
	return "", false
}

// applyAgentEventLocked folds one event into the feed, registers inner tool
// routes, and enforces the block cap (head dropped — the tail is the
// interesting end). Caller holds jobsMu.
func (c *WUIConnector) applyAgentEventLocked(slot *engineSlot, feedKey string, event hub.Event) {
	feed, ok := slot.jobFeeds[feedKey]
	if !ok {
		return
	}
	if event.Agent != nil && event.Agent.ParentToolUseID == feedKey {
		masked := event
		masked.Agent = nil
		updateStreamState(&feed.streamState, masked)
	} else {
		updateStreamState(&feed.streamState, event)
	}
	if event.Type == types.EventToolStart && event.ToolUse != nil {
		slot.innerToolToFeed[event.ToolUse.ID] = feedKey
	}
	if n := len(feed.streamState.blocks); n > jobFeedMaxBlocks {
		feed.streamState.blocks = feed.streamState.blocks[n-jobFeedMaxBlocks:]
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

// handleJobsClear drops terminal entries from the active slot's view. The
// jobsMu section must end before pushJobsSnapshot — it takes the same lock.
func (c *WUIConnector) handleJobsClear() {
	slot := c.activeSlot()
	if slot == nil || slot.jobView == nil {
		return
	}
	slot.jobsMu.Lock()
	c.clearTerminalJobsLocked(slot)
	slot.jobsMu.Unlock()
	c.pushJobsSnapshot(slot)
}
