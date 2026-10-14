import { describe, it, expect } from 'vitest'
import { createJobsStore, type FeedChange } from './jobs_store'
import type { JobWireItem, QueryEvent } from './types'
import type { Block } from './model'

function bashJob(id: string, status: JobWireItem['status'], extra: Partial<JobWireItem> = {}): JobWireItem {
  return { id, kind: 'bash', status, command: 'make check', startedMs: 1000, ...extra }
}

function agentJob(id: string, status: JobWireItem['status'], parent: string): JobWireItem {
  return {
    id, kind: 'agent', status, parentToolUseID: parent,
    agentType: 'executor', description: '修 wui 分页 bug', startedMs: 2000,
  }
}
function forkJob(id: string, status: JobWireItem['status'], parent: string): JobWireItem {
  return {
    id, kind: 'fork', status, parentToolUseID: parent,
    agentType: 'fork', description: 'audit the parser', startedMs: 2000,
  }
}

function agentEvent(type: string, parent: string, fields: Partial<QueryEvent> = {}): QueryEvent {
  return {
    type,
    agent: { parent_tool_use_id: parent, agent_type: 'executor', depth: 1 },
    ...fields,
  }
}

function toolStart(toolID: string, parent: string, name: string): QueryEvent {
  return agentEvent('tool_start', parent, { tool_use: { id: toolID, name, input: {} } })
}

function firstToolBlock(blocks: Block[]) {
  const b = blocks[0]
  if (!b || b.kind !== 'tool') throw new Error('expected tool block, got ' + JSON.stringify(b))
  return b
}

describe('jobs store — setJobs merge', () => {
  it('updates status in place and keeps the feed for surviving ids', () => {
    const store = createJobsStore()
    store.setJobs([bashJob('bg-1', 'running')])
    store.applyOutput('bg-1', 'go vet ./...\n')
    store.setJobs([bashJob('bg-1', 'completed', { exitCode: 0 })])
    expect(store.jobs()).toHaveLength(1)
    expect(store.jobs()[0].status).toBe('completed')
    expect(store.jobs()[0].exitCode).toBe(0)
    expect(store.feed('bg-1')?.tail).toBe('go vet ./...\n')
  })

  it('removes job and its feed when the id disappears from the snapshot', () => {
    const store = createJobsStore()
    store.setJobs([bashJob('bg-1', 'running'), bashJob('bg-2', 'running')])
    store.applyOutput('bg-1', 'a\n')
    store.setJobs([bashJob('bg-2', 'running')])
    expect(store.jobs().map((j) => j.id)).toEqual(['bg-2'])
    expect(store.feed('bg-1')).toBeUndefined()
  })

  it('empty snapshot clears everything including feeds (metadata reset semantics)', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(toolStart('inner-1', 'parent-1', 'Read'))
    store.setJobs([])
    expect(store.jobs()).toEqual([])
    expect(store.feed('agent-1')).toBeUndefined()
    // Routes must be gone too: the same parent id no longer appends anywhere.
    store.applyEvent(agentEvent('text_delta', 'parent-1', { text: 'orphan' }))
    expect(store.feed('agent-1')).toBeUndefined()
    expect(store.hasAny()).toBe(false)
  })
})

describe('jobs store — applyEvent routing', () => {
  it("routes the job's own tool_start to the feed root and registers the inner tool id", () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(toolStart('inner-1', 'parent-1', 'Read'))
    const blocks = store.feed('agent-1')?.blocks
    expect(blocks).toHaveLength(1)
    expect(blocks![0]).toMatchObject({ kind: 'tool', id: 'inner-1', name: 'Read', state: 'running' })
  })

  it('fork rows are agent-family: discovery creates the feed before any event lands', () => {
    const store = createJobsStore()
    store.setJobs([forkJob('agent-1', 'running', 'parent-f')])
    expect(store.feed('agent-1')).toBeDefined()
    expect(store.feed('agent-1')!.blocks).toHaveLength(0)
    // Bash rows stay out of the feed set — their content arrives as tails.
    store.setJobs([forkJob('agent-1', 'running', 'parent-f'), bashJob('bg-1', 'running')])
    expect(store.feed('bg-1')).toBeUndefined()
  })

  it('nests events of an inner agent under the spawning tool block', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(toolStart('inner-1', 'parent-1', 'Agent'))
    store.applyEvent(toolStart('nested-1', 'inner-1', 'Bash'))
    const root = firstToolBlock(store.feed('agent-1')!.blocks)
    expect(root.children).toHaveLength(1)
    expect(root.children[0]).toMatchObject({ kind: 'tool', id: 'nested-1', name: 'Bash' })
  })

  it('routes depth-3 tool_starts through registered inner ids (transitive registration)', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(toolStart('inner-1', 'parent-1', 'Agent'))
    store.applyEvent(toolStart('nested-1', 'inner-1', 'Agent'))
    // nested-1 spawned inside inner-1's subtree; its id was registered on the
    // routing hit, so a grandchild agent (parent == nested-1) still lands in
    // the same feed, nested one level deeper.
    store.applyEvent(toolStart('deep-1', 'nested-1', 'Read'))
    const root = firstToolBlock(store.feed('agent-1')!.blocks)
    const nested = root.children[0]
    if (!nested || nested.kind !== 'tool') throw new Error('expected nested tool block')
    expect(nested.children).toHaveLength(1)
    expect(nested.children[0]).toMatchObject({ kind: 'tool', id: 'deep-1', name: 'Read' })
  })

  it('ignores events whose parent matches no job and no registered inner id', () => {
    const store = createJobsStore()
    let changes = 0
    store.onChange(() => changes++)
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    expect(changes).toBe(1)
    store.applyEvent(toolStart('x', 'unrelated-parent', 'Read'))
    expect(changes).toBe(1)
    expect(store.feed('agent-1')?.blocks).toHaveLength(0)
  })

  it('ignores events without agent metadata', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent({ type: 'text_delta', text: 'no agent' })
    expect(store.feed('agent-1')?.blocks).toHaveLength(0)
  })

  it('merges consecutive text deltas into one text block', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(agentEvent('text_delta', 'parent-1', { text: 'he' }))
    store.applyEvent(agentEvent('text_delta', 'parent-1', { text: 'llo' }))
    const blocks = store.feed('agent-1')?.blocks
    expect(blocks).toHaveLength(1)
    expect(blocks![0]).toMatchObject({ kind: 'text', text: 'hello' })
  })

  it('tool_param_delta replaces the tool block summary and flags', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(toolStart('inner-1', 'parent-1', 'Grep'))
    store.applyEvent(agentEvent('tool_param_delta', 'parent-1', {
      partial_input: { id: 'inner-1', name: 'Grep', delta: 'x', summary: 'pattern in src/', is_search: true },
    }))
    const b = firstToolBlock(store.feed('agent-1')!.blocks)
    expect(b.summary).toBe('pattern in src/')
    expect(b.isSearch).toBe(true)
  })

  it('tool_output_delta updates the tool block display output', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(toolStart('inner-1', 'parent-1', 'Bash'))
    store.applyEvent(agentEvent('tool_output_delta', 'parent-1', {
      tool_result: { tool_use_id: 'inner-1', output: '', display_output: '3 matches' },
    }))
    expect(firstToolBlock(store.feed('agent-1')!.blocks).displayOutput).toBe('3 matches')
  })

  it('tool_end marks the block done or error and clears streamed children', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(toolStart('inner-1', 'parent-1', 'Agent'))
    store.applyEvent(toolStart('nested-1', 'inner-1', 'Bash'))
    store.applyEvent(agentEvent('tool_end', 'parent-1', {
      tool_result: { tool_use_id: 'inner-1', output: 'final report', display_output: 'final report' },
    }))
    const b = firstToolBlock(store.feed('agent-1')!.blocks)
    expect(b.state).toBe('done')
    expect(b.displayOutput).toBe('final report')
    // Nested activity is superseded by the final result (Go updateStreamState
    // clears children on tool_end — the feed must match hydration truth).
    expect(b.children).toEqual([])

    store.applyEvent(agentEvent('tool_end', 'parent-1', {
      tool_result: { tool_use_id: 'nested-1', output: '', display_output: 'boom', is_error: true },
    }))
    // nested-1's block was cleared with inner-1's children — its tool_end
    // finds no target and must leave the feed untouched.
    const after = firstToolBlock(store.feed('agent-1')!.blocks)
    expect(store.feed('agent-1')?.blocks).toHaveLength(1)
    expect(after.state).toBe('done')
    expect(after.displayOutput).toBe('final report')
    expect(after.children).toEqual([])
  })

  it('thinking_start/delta/end accumulate into one inactive thinking block', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(agentEvent('thinking_start', 'parent-1'))
    store.applyEvent(agentEvent('thinking_delta', 'parent-1', { thinking: { text: '先复现' } }))
    store.applyEvent(agentEvent('thinking_end', 'parent-1', { thinking: { duration: 1200 } }))
    const blocks = store.feed('agent-1')?.blocks
    expect(blocks).toHaveLength(1)
    expect(blocks![0]).toMatchObject({ kind: 'thinking', text: '先复现', active: false, durationNs: 1200 })
  })

  it('attachment events append a user echo block, prompt preferred over content envelope', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(agentEvent('attachment', 'parent-1', {
      message: {
        attachment: { prompt: '跑一下 make check', mode: 'prompt' },
        content: [{ type: 'text', text: '[queued reminder envelope]' }],
      },
    }))
    store.applyEvent(agentEvent('attachment', 'parent-1', {
      message: { content: [{ type: 'text', text: 'part1 ' }, { type: 'text', text: 'part2' }] },
    }))
    const blocks = store.feed('agent-1')?.blocks
    expect(blocks).toHaveLength(2)
    expect(blocks![0]).toMatchObject({ kind: 'user', id: '', text: '跑一下 make check' })
    expect(blocks![1]).toMatchObject({ kind: 'user', id: '', text: 'part1 part2' })
  })

  it('attachment events with no message or empty text are dropped', () => {
    const store = createJobsStore()
    let changes = 0
    store.onChange(() => changes++)
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    expect(changes).toBe(1)
    store.applyEvent(agentEvent('attachment', 'parent-1'))
    store.applyEvent(agentEvent('attachment', 'parent-1', {
      message: { attachment: { prompt: '', mode: 'prompt' } },
    }))
    store.applyEvent(agentEvent('attachment', 'parent-1', {
      message: { content: [{ type: 'image', source: { media_type: 'image/png', data: 'zz' } }] },
    }))
    expect(store.feed('agent-1')?.blocks).toHaveLength(0)
    expect(changes).toBe(1)
  })

  it('caps the feed at 400 blocks, dropping the head (mirror of Go jobFeedMaxBlocks)', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    for (let i = 1; i <= 401; i++) {
      store.applyEvent(toolStart(`toolu_${String(i).padStart(3, '0')}`, 'parent-1', 'Bash'))
    }
    const blocks = store.feed('agent-1')?.blocks
    expect(blocks).toHaveLength(400)
    // First block dropped — the tail is the interesting end, same as the
    // server cache (TestCaptureJobEvent_FeedCapDropsHead).
    expect(blocks![0]).toMatchObject({ kind: 'tool', id: 'toolu_002' })
    expect(blocks![399]).toMatchObject({ kind: 'tool', id: 'toolu_401' })
  })

  it('live appends after a full hydration stay at the cap (live view matches reconnect)', () => {
    const store = createJobsStore()
    const blocks: Block[] = Array.from({ length: 400 }, (_, i) => ({
      kind: 'text' as const, id: '', text: `h${i}`,
    }))
    store.setJobs(
      [agentJob('agent-1', 'running', 'parent-1')],
      new Map([['agent-1', { blocks }]]),
    )
    store.applyEvent(toolStart('live-1', 'parent-1', 'Bash'))
    const live = store.feed('agent-1')?.blocks
    expect(live).toHaveLength(400)
    expect(live![0]).toMatchObject({ kind: 'text', text: 'h1' })
    expect(live![399]).toMatchObject({ kind: 'tool', id: 'live-1' })
  })
})

describe('jobs store — output / predicates / onChange', () => {
  it('applyOutput only touches the tail, never blocks', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(toolStart('inner-1', 'parent-1', 'Read'))
    store.applyOutput('agent-1', 'tail text')
    const feed = store.feed('agent-1')
    expect(feed?.tail).toBe('tail text')
    expect(feed?.blocks).toHaveLength(1)
  })

  it('applyOutput for an unknown job id is dropped', () => {
    const store = createJobsStore()
    let changes = 0
    store.onChange(() => changes++)
    store.setJobs([bashJob('bg-1', 'running')])
    expect(changes).toBe(1)
    store.applyOutput('unknown', 'x')
    expect(changes).toBe(1)
    expect(store.feed('unknown')).toBeUndefined()
  })

  it('hasRunning and hasAny reflect the current snapshot', () => {
    const store = createJobsStore()
    expect(store.hasAny()).toBe(false)
    expect(store.hasRunning()).toBe(false)
    store.setJobs([bashJob('bg-1', 'completed')])
    expect(store.hasAny()).toBe(true)
    expect(store.hasRunning()).toBe(false)
    store.setJobs([bashJob('bg-1', 'completed'), bashJob('bg-2', 'running')])
    expect(store.hasRunning()).toBe(true)
    store.setJobs([])
    expect(store.hasAny()).toBe(false)
  })

  it('onChange fires once per mutating call and unsubscribes cleanly', () => {
    const store = createJobsStore()
    let a = 0
    let b = 0
    const off = store.onChange(() => a++)
    store.onChange(() => b++)
    store.setJobs([bashJob('bg-1', 'running')]) // a=1 b=1
    store.applyEvent({ type: 'text_delta', text: 'no agent' }) // ignored
    store.applyOutput('bg-1', 't') // a=2 b=2
    store.applyOutput('unknown', 'x') // unknown job — no fire
    expect(a).toBe(2)
    expect(b).toBe(2)
    off()
    store.setJobs([]) // only b fires now
    expect(a).toBe(2)
    expect(b).toBe(3)
  })
})

describe('jobs store — metadata hydration', () => {
  it('hydrated blocks and tail land in the feed', () => {
    const store = createJobsStore()
    const blocks: Block[] = [
      { kind: 'thinking', id: '', text: 'plan', durationNs: 5, active: false, startedAt: 1 },
      { kind: 'text', id: '', text: 'server said ' },
    ]
    store.setJobs(
      [agentJob('agent-1', 'running', 'parent-1')],
      new Map([['agent-1', { blocks, tail: 't' }]]),
    )
    const feed = store.feed('agent-1')
    expect(feed?.blocks).toHaveLength(2)
    expect(feed?.blocks[1]).toMatchObject({ kind: 'text', text: 'server said ' })
    expect(feed?.tail).toBe('t')
  })

  it('live events continue appending after hydration (text merges into the hydrated tail block)', () => {
    const store = createJobsStore()
    const blocks: Block[] = [{ kind: 'text', id: '', text: 'server said ' }]
    store.setJobs(
      [agentJob('agent-1', 'running', 'parent-1')],
      new Map([['agent-1', { blocks }]]),
    )
    store.applyEvent(agentEvent('text_delta', 'parent-1', { text: 'more' }))
    const feed = store.feed('agent-1')
    expect(feed?.blocks).toHaveLength(1)
    expect(feed?.blocks[0]).toMatchObject({ kind: 'text', text: 'server said more' })
  })

  it('hydration for an id absent from the snapshot is ignored', () => {
    const store = createJobsStore()
    const blocks: Block[] = [{ kind: 'text', id: '', text: 'x' }]
    store.setJobs(
      [bashJob('bg-1', 'running')],
      new Map([['ghost', { blocks }]]),
    )
    expect(store.feed('ghost')).toBeUndefined()
  })
})

describe('jobs store — granular change feed', () => {
  it('emits event with parentID null for root-routed events and the inner tool id for nested ones', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    const seen: FeedChange[] = []
    store.onFeedChange((c) => seen.push(c))

    store.applyEvent(toolStart('inner-1', 'parent-1', 'Agent'))
    store.applyEvent(toolStart('nested-1', 'inner-1', 'Bash'))

    expect(seen).toHaveLength(2)
    expect(seen[0]).toMatchObject({ kind: 'event', jobID: 'agent-1', parentID: null })
    if (seen[0].kind !== 'event') throw new Error('expected event change')
    expect(seen[0].e.type).toBe('tool_start')
    expect(seen[1]).toMatchObject({ kind: 'event', jobID: 'agent-1', parentID: 'inner-1' })
    // The emitted event object is the routed wire event itself.
    if (seen[1].kind !== 'event') throw new Error('expected event change')
    expect(seen[1].e.tool_use?.id).toBe('nested-1')
  })

  it('emits truncated after the event when the 400 cap slices the head, with the dropped refs', () => {
    const store = createJobsStore()
    const blocks: Block[] = Array.from({ length: 400 }, (_, i) => ({
      kind: 'text' as const, id: '', text: `h${i}`,
    }))
    store.setJobs(
      [agentJob('agent-1', 'running', 'parent-1')],
      new Map([['agent-1', { blocks }]]),
    )
    const seen: FeedChange[] = []
    store.onFeedChange((c) => seen.push(c))
    const headRef = store.feed('agent-1')!.blocks[0]

    store.applyEvent(toolStart('live-1', 'parent-1', 'Bash'))

    expect(seen.map((c) => c.kind)).toEqual(['event', 'truncated'])
    const trunc = seen[1]
    if (trunc.kind !== 'truncated') throw new Error('expected truncated change')
    expect(trunc.jobID).toBe('agent-1')
    expect(trunc.dropped).toHaveLength(1)
    // Identity, not deep equality: the sink keys its registry off refs.
    expect(trunc.dropped[0]).toBe(headRef)
    expect(store.feed('agent-1')!.blocks).toHaveLength(400)
  })

  it('emits tail on every applyOutput but bumps the version only when the string changed', () => {
    const store = createJobsStore()
    store.setJobs([bashJob('bg-1', 'running')])
    const seen: FeedChange[] = []
    store.onFeedChange((c) => seen.push(c))
    const v0 = store.feedVersion('bg-1')

    store.applyOutput('bg-1', 'a\n')
    const v1 = store.feedVersion('bg-1')
    store.applyOutput('bg-1', 'a\n') // same string

    expect(seen.map((c) => c.kind + ':' + c.jobID)).toEqual(['tail:bg-1', 'tail:bg-1'])
    expect(v1).toBe(v0 + 1)
    expect(store.feedVersion('bg-1')).toBe(v1)
  })

  it('emits reset + version bump per hydrated id whose blocks/tail were written', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1'), agentJob('agent-2', 'running', 'parent-2')])
    const seen: FeedChange[] = []
    store.onFeedChange((c) => seen.push(c))
    const v1a = store.feedVersion('agent-1')

    store.setJobs(
      [agentJob('agent-1', 'running', 'parent-1'), agentJob('agent-2', 'running', 'parent-2')],
      new Map([['agent-1', { blocks: [{ kind: 'text', id: '', text: 'x' }] }]]),
    )

    expect(seen.map((c) => c.kind + ':' + c.jobID)).toEqual(['reset:agent-1'])
    expect(store.feedVersion('agent-1')).toBe(v1a + 1)
    expect(store.feedVersion('agent-2')).toBe(0)
  })
  it('empty hydration entries (no blocks, no tail) emit no reset and no bump', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1'), bashJob('bg-1', 'completed')])
    const seen: FeedChange[] = []
    store.onFeedChange((c) => seen.push(c))
    // chat.ts maps every wire job into the hydration map; a job with no
    // cached feed arrives as { blocks: undefined, tail: undefined }.
    store.setJobs(
      [agentJob('agent-1', 'running', 'parent-1'), bashJob('bg-1', 'completed')],
      new Map([
        ['agent-1', {}],
        ['bg-1', {}],
      ]),
    )
    expect(seen).toEqual([])
    expect(store.feedVersion('agent-1')).toBe(0)
    expect(store.feedVersion('bg-1')).toBe(0)
    // Agent feeds still exist from discovery; an empty entry creates no
    // bash feed (applyOutput will on the first tail).
    expect(store.feed('agent-1')?.blocks).toEqual([])
    expect(store.feed('bg-1')).toBeUndefined()
  })

  it('status-only setJobs emits nothing and does not bump the version', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    store.applyEvent(agentEvent('text_delta', 'parent-1', { text: 'hi' }))
    const v = store.feedVersion('agent-1')
    const seen: FeedChange[] = []
    store.onFeedChange((c) => seen.push(c))

    store.setJobs([agentJob('agent-1', 'completed', 'parent-1')])

    expect(seen).toHaveLength(0)
    expect(store.feedVersion('agent-1')).toBe(v)
  })

  it('no emission for unrouted events or content-free routed ones', () => {
    const store = createJobsStore()
    store.setJobs([agentJob('agent-1', 'running', 'parent-1')])
    const seen: FeedChange[] = []
    store.onFeedChange((c) => seen.push(c))
    let changes = 0
    store.onChange(() => changes++)

    // Parent resolves to no job and no registered inner id.
    store.applyEvent(toolStart('x', 'unrelated', 'Read'))
    // Routed, but the event carries no content change.
    store.applyEvent(agentEvent('tool_param_delta', 'parent-1', {}))
    store.applyEvent(agentEvent('attachment', 'parent-1'))

    expect(seen).toHaveLength(0)
    expect(changes).toBe(0)
    expect(store.feedVersion('agent-1')).toBe(0)
  })

  it('onChange callbacks receive the jobID of the mutated feed', () => {
    const store = createJobsStore()
    store.setJobs([bashJob('bg-1', 'running'), bashJob('bg-2', 'running')])
    const ids: Array<string | undefined> = []
    store.onChange((jobID) => ids.push(jobID))

    store.applyOutput('bg-2', 'x')
    store.setJobs([bashJob('bg-1', 'running'), bashJob('bg-2', 'running')])
    store.applyEvent(agentEvent('text_delta', 'nope', { text: 'orphan' }))

    expect(ids).toEqual(['bg-2', undefined])
  })

  it('feedVersion is monotonic per job and never resets (removal included)', () => {
    const store = createJobsStore()
    store.setJobs([bashJob('bg-1', 'running')])
    store.applyOutput('bg-1', 'a')
    const v = store.feedVersion('bg-1')
    expect(v).toBeGreaterThan(0)
    store.setJobs([]) // job gone; the version watermark survives
    store.setJobs([bashJob('bg-1', 'running')])
    store.applyOutput('bg-1', 'b')
    expect(store.feedVersion('bg-1')).toBe(v + 1)
    expect(store.feedVersion('never-seen')).toBe(0)
  })
})
