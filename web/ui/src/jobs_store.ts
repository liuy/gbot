import type { Block } from './model'
import type { JobWireItem, QueryEvent } from './types'

export interface JobFeedState {
  blocks: Block[]
  tail: string
  follow: boolean
}

export interface JobsStoreHandle {
  setJobs(items: JobWireItem[], hydrated?: Map<string, { blocks?: Block[]; tail?: string }>): void
  applyOutput(jobID: string, tail: string): void
  applyEvent(e: QueryEvent): void
  jobs(): JobWireItem[]
  feed(jobID: string): JobFeedState | undefined
  hasRunning(): boolean
  hasAny(): boolean
  onChange(cb: () => void): () => void
}

type ToolBlock = Extract<Block, { kind: 'tool' }>

// Mirror of the connector's jobFeedMaxBlocks (pkg/connector/wui/jobs_panel.go).
// The server caps its feed cache per event, so the live client feed must cap
// identically — otherwise the live view and a reconnect hydration (served from
// the capped cache) would diverge, and a hours-long agent would grow the
// client feed without bound.
const JOB_FEED_MAX_BLOCKS = 400

// findBlock mirrors the connector's findBlock: depth-first search of the
// block tree for a tool block by id. Sub-agent events address their parent
// tool by id, wherever in the tree it lives.
function findBlock(blocks: Block[], id: string): ToolBlock | null {
  for (const b of blocks) {
    if (b.kind === 'tool') {
      if (b.id === id) return b
      const nested = findBlock(b.children, id)
      if (nested) return nested
    }
  }
  return null
}

export function createJobsStore(): JobsStoreHandle {
  let items = new Map<string, JobWireItem>()
  const feeds = new Map<string, JobFeedState>()
  // parent → job resolution at two depths, mirroring the connector's
  // jobFeeds + innerToolToFeed pair: a job's own parentToolUseID routes to
  // the feed root, tool ids opened inside a feed route back to that feed.
  // toolID → jobID here (connector keys feeds by parent id; jobs are keyed
  // by job id in this store).
  const innerToolToJob = new Map<string, string>()
  const changeCbs = new Set<() => void>()

  const notify = () => {
    for (const cb of changeCbs) cb()
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

  // applyStreamEvent mirrors the connector's updateStreamState for the event
  // types that build blocks. target is the list to append to (null = the
  // parent block vanished; the event is dropped, same as Go's nil targetList).
  // Returns whether the feed content changed (drives onChange).
  const applyStreamEvent = (feed: JobFeedState, target: Block[] | null, e: QueryEvent): boolean => {
    switch (e.type) {
      case 'text_delta': {
        if (!target) return false
        const last = target[target.length - 1]
        if (last && last.kind === 'text') {
          last.text += e.text ?? ''
        } else {
          target.push({ kind: 'text', id: '', text: e.text ?? '' })
        }
        return true
      }
      case 'tool_start': {
        if (!target || !e.tool_use) return false
        const tu = e.tool_use
        target.push({
          kind: 'tool',
          id: tu.id,
          name: tu.name,
          summary: tu.summary ?? '',
          isSearch: !!tu.is_search,
          isRead: !!tu.is_read,
          isList: !!tu.is_list,
          isLsp: !!tu.is_lsp,
          isTask: !!tu.is_task,
          isWeb: tu.name === 'Web',
          state: 'running',
          timingNs: 0,
          displayOutput: '',
          startedAt: Date.now(),
          children: [],
        })
        return true
      }
      case 'tool_param_delta': {
        if (!e.partial_input) return false
        const b = findBlock(feed.blocks, e.partial_input.id)
        if (!b) return false
        if (e.partial_input.summary) b.summary = e.partial_input.summary
        if (e.partial_input.is_search) b.isSearch = true
        if (e.partial_input.is_read) b.isRead = true
        if (e.partial_input.is_list) b.isList = true
        if (e.partial_input.is_lsp) b.isLsp = true
        if (e.partial_input.is_task) b.isTask = true
        return true
      }
      case 'tool_output_delta': {
        if (!e.tool_result) return false
        const b = findBlock(feed.blocks, e.tool_result.tool_use_id)
        if (!b) return false
        b.displayOutput = e.tool_result.display_output ?? ''
        return true
      }
      case 'tool_end': {
        if (!e.tool_result) return false
        const b = findBlock(feed.blocks, e.tool_result.tool_use_id)
        if (!b) return false
        b.state = e.tool_result.is_error ? 'error' : 'done'
        b.displayOutput = e.tool_result.display_output ?? ''
        if (e.tool_result.is_search) b.isSearch = true
        if (e.tool_result.is_read) b.isRead = true
        if (e.tool_result.is_list) b.isList = true
        if (e.tool_result.is_lsp) b.isLsp = true
        if (e.tool_result.is_task) b.isTask = true
        b.timingNs = (Date.now() - b.startedAt) * 1e6
        // Agent tool: streamed children are superseded by the final result —
        // the server cache clears them on tool_end, so the live path must
        // too or a reconnect hydration would render differently.
        b.children = []
        return true
      }
      case 'thinking_start': {
        if (!target) return false
        target.push({ kind: 'thinking', id: '', text: '', durationNs: 0, active: true, startedAt: Date.now() })
        return true
      }
      case 'thinking_delta': {
        if (!target || !e.thinking) return false
        const last = target[target.length - 1]
        if (!last || last.kind !== 'thinking') return false
        last.text += e.thinking.text ?? ''
        return true
      }
      case 'thinking_end': {
        if (!target || !e.thinking) return false
        const last = target[target.length - 1]
        if (!last || last.kind !== 'thinking') return false
        last.active = false
        last.durationNs = e.thinking.duration ?? 0
        return true
      }
      case 'attachment': {
        if (!target || !e.message) return false
        // Mirrors Go updateStreamState: prompt-mode attachments carry the
        // original user text in prompt; queued-message events carry only the
        // reminder envelope in content — prefer the prompt to keep the echo
        // unwrapped.
        let text: string = ''
        const att = e.message.attachment
        if (att && att.mode === 'prompt' && att.prompt) {
          text = att.prompt
        } else {
          for (const cb of e.message.content ?? []) {
            if (cb.type === 'text' && cb.text) text += cb.text
          }
        }
        if (!text) return false
        target.push({ kind: 'user', id: '', text })
        return true
      }
    }
    return false
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
      // Agent feeds exist from discovery (Go creates jobFeeds when the
      // watcher sees the job), so applyEvent never has to create one on the
      // hot path and feed() is defined for every agent row. The agent family
      // is everything that is not a bash tail — background agents only:
      // engines never appear in this feed, the Go MultiRegistry exposes the
      // bash and background-agent adapters.
      for (const j of items.values()) {
        if (j.kind !== 'bash' && j.parentToolUseID) ensureFeed(j.id)
      }
      if (hydrated) {
        for (const [id, h] of hydrated) {
          const f = ensureFeed(id)
          if (!f) continue
          if (h.blocks !== undefined) f.blocks = h.blocks
          if (h.tail !== undefined) f.tail = h.tail
        }
      }
      notify()
    },
    applyOutput: (jobID, tail) => {
      const f = ensureFeed(jobID)
      if (!f) return
      f.tail = tail
      notify()
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
      const changed = applyStreamEvent(feed, target, e)
      // Mirror of Go's per-event cap in applyAgentEventLocked: drop the head,
      // the tail is the interesting end.
      if (feed.blocks.length > JOB_FEED_MAX_BLOCKS) {
        feed.blocks = feed.blocks.slice(-JOB_FEED_MAX_BLOCKS)
      }
      // Registration hangs off the ROUTING hit, not off toRoot: a depth ≥3
      // tool_start has an inner tool id as parent and must still register,
      // or its grandchildren would stop routing.
      if (e.type === 'tool_start' && e.tool_use) {
        innerToolToJob.set(e.tool_use.id, jobID)
      }
      if (changed) notify()
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
  }
}
