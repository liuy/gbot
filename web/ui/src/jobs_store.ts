import type { Block } from './model'
import type { JobWireItem, QueryEvent } from './types'
import { applyStreamEvent, findBlock } from './components/event_blocks'

export interface JobFeedState {
  blocks: Block[]
  tail: string
  follow: boolean
}

// Granular per-feed change notification. The DOM sink consumes these to
// touch only the affected job's feed element per store delta.
//   event     — e routed to jobID; parentID = the parent tool id the event
//               addressed (null = feed root)
//   truncated — refs removed from the head by the 400-block cap
//   tail      — applyOutput wrote the bash tail
//   reset     — setJobs hydration replaced blocks/tail
export type FeedChange =
  | { kind: 'event'; jobID: string; parentID: string | null; e: QueryEvent }
  | { kind: 'truncated'; jobID: string; dropped: Block[] }
  | { kind: 'tail'; jobID: string }
  | { kind: 'reset'; jobID: string }

export interface JobsStoreHandle {
  setJobs(items: JobWireItem[], hydrated?: Map<string, { blocks?: Block[]; tail?: string }>): void
  applyOutput(jobID: string, tail: string): void
  applyEvent(e: QueryEvent): void
  jobs(): JobWireItem[]
  feed(jobID: string): JobFeedState | undefined
  hasRunning(): boolean
  hasAny(): boolean
  onChange(cb: (jobID?: string) => void): () => void
  onFeedChange(cb: (c: FeedChange) => void): () => void
  // Monotonic per jobID, never reset — the renderer's stale-view watermark.
  feedVersion(jobID: string): number
}

// Mirror of the connector's jobFeedMaxBlocks (pkg/wui/jobs_panel.go).
// The 400 cap still governs LIVE jobs: the server keeps the live region at
// ≤400 blocks and the client must cap identically or the live view diverges
// from a reconnect hydration. TERMINAL jobs are the deliberate exception: the
// server truncates their region to the last jobTerminalLastN (20) blocks at
// job_terminated and freezes it, so a post-terminal reconnect hydrates with
// ≤20 blocks by design — the client renders whatever the snapshot carries.
const JOB_FEED_MAX_BLOCKS = 400

export function createJobsStore(): JobsStoreHandle {
  let items = new Map<string, JobWireItem>()
  const feeds = new Map<string, JobFeedState>()
  // parent → job resolution at two depths: a job's own parentToolUseID
  // routes to the feed root, tool ids opened inside a feed route back to
  // that feed. The connector routes server-side by job id (single delivery);
  // this store keeps the parent-id routing because job_list frames now
  // arrive on job lifecycle transitions (no polling cadence), and the
  // "belongs to no job → drop" filter must keep working for early events.
  const innerToolToJob = new Map<string, string>()
  const changeCbs = new Set<(jobID?: string) => void>()
  const feedCbs = new Set<(c: FeedChange) => void>()
  const versions = new Map<string, number>()

  const notify = (jobID?: string) => {
    for (const cb of changeCbs) cb(jobID)
  }

  const emit = (c: FeedChange) => {
    for (const cb of feedCbs) cb(c)
  }

  const bumpVersion = (jobID: string) => {
    versions.set(jobID, (versions.get(jobID) ?? 0) + 1)
  }

  const ensureFeed = (jobID: string): JobFeedState | undefined => {
    if (!items.has(jobID)) return undefined
    let f = feeds.get(jobID)
    if (!f) {
      f = { blocks: [], tail: '', follow: true }
      feeds.set(jobID, f)
    }
    return f
  }

  // dropRoutesFor removes route entries pointing at jobs that no longer
  // exist — otherwise stale inner ids would keep appending into feeds the
  // panel no longer shows.
  const dropRoutesForGoneJobs = () => {
    for (const [toolID, jobID] of innerToolToJob) {
      if (!items.has(jobID)) innerToolToJob.delete(toolID)
    }
  }

  return {
    setJobs: (list, hydrated) => {
      const next = new Map<string, JobWireItem>()
      for (const it of list) next.set(it.id, it)
      for (const id of feeds.keys()) {
        if (!next.has(id)) feeds.delete(id)
      }
      items = next
      dropRoutesForGoneJobs()
      // Agent feeds exist from discovery (Go creates the region when the
      // job_started lifecycle event lands), so applyEvent never has to create
      // one on the hot path and feed() is defined for every agent row. The
      // agent family is everything that is not a bash tail — background
      // agents only: engines never appear in this feed, the Go
      // MultiRegistry exposes the bash and background-agent adapters.
      for (const j of items.values()) {
        if (j.kind !== 'bash' && j.parentToolUseID) ensureFeed(j.id)
      }
      if (hydrated) {
        for (const [id, h] of hydrated) {
          // chat.ts maps every wire job into the hydration map; a job with
          // no cached feed arrives as {undefined, undefined} and must not
          // emit a spurious reset (it would rehydrate an expanded view).
          if (h.blocks === undefined && h.tail === undefined) continue
          const f = ensureFeed(id)
          if (!f) continue
          if (h.blocks !== undefined) f.blocks = h.blocks
          if (h.tail !== undefined) f.tail = h.tail
          emit({ kind: 'reset', jobID: id })
          bumpVersion(id)
        }
      }
      notify()
    },
    applyOutput: (jobID, tail) => {
      const f = ensureFeed(jobID)
      if (!f) return
      const changed = f.tail !== tail
      f.tail = tail
      emit({ kind: 'tail', jobID })
      if (changed) bumpVersion(jobID)
      notify(jobID)
    },
    applyEvent: (e) => {
      const parent = e.agent?.parent_tool_use_id
      if (!parent) return
      let jobID: string | null = null
      let toRoot = false
      for (const j of items.values()) {
        if (j.parentToolUseID === parent) {
          jobID = j.id
          toRoot = true
          break
        }
      }
      if (jobID === null) jobID = innerToolToJob.get(parent) ?? null
      if (jobID === null) return
      const feed = ensureFeed(jobID)
      if (!feed) return
      // The job's own events land at the feed root (the connector masks
      // Agent=nil for them); inner-agent events nest under their parent
      // tool's children.
      const target = toRoot
        ? feed.blocks
        : findBlock(feed.blocks, parent)?.children ?? null
      const changed = applyStreamEvent(feed.blocks, target, e)
      // Mirror of Go's per-event cap in applyAgentEventLocked: drop the head,
      // the tail is the interesting end.
      let dropped: Block[] | null = null
      if (feed.blocks.length > JOB_FEED_MAX_BLOCKS) {
        const over = feed.blocks.length - JOB_FEED_MAX_BLOCKS
        dropped = feed.blocks.slice(0, over)
        feed.blocks = feed.blocks.slice(-JOB_FEED_MAX_BLOCKS)
      }
      // Registration hangs off the ROUTING hit, not off toRoot: a depth ≥3
      // tool_start has an inner tool id as parent and must still register,
      // or its grandchildren would stop routing.
      if (e.type === 'tool_start' && e.tool_use) {
        innerToolToJob.set(e.tool_use.id, jobID)
      }
      if (changed) {
        emit({ kind: 'event', jobID, parentID: toRoot ? null : parent, e })
        if (dropped) emit({ kind: 'truncated', jobID, dropped })
        bumpVersion(jobID)
        notify(jobID)
      }
    },
    jobs: () => [...items.values()],
    feed: (jobID) => feeds.get(jobID),
    hasRunning: () => {
      for (const j of items.values()) {
        if (j.status === 'running') return true
      }
      return false
    },
    hasAny: () => items.size > 0,
    onChange: (cb) => {
      changeCbs.add(cb)
      return () => changeCbs.delete(cb)
    },
    onFeedChange: (cb) => {
      feedCbs.add(cb)
      return () => feedCbs.delete(cb)
    },
    feedVersion: (jobID) => versions.get(jobID) ?? 0,
  }
}
