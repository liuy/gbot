import { describe, it, expect, vi } from 'vitest'
import { createEventBlockStore } from './event_blocks'
import type { QueryEvent } from '../types'
import type { Block } from '../model'

// Reducer assertions ported from jobs_store.test.ts: after the applyStreamEvent
// move, these pin the shared reducer's behavior independently of the jobs
// routing layer, so any drift between the two consumers is caught here.

function firstBlock(blocks: Block[]): Block {
  const b = blocks[0]
  if (!b) throw new Error('expected a block, got none')
  return b
}

function toolStart(toolID: string, name: string, fields: Partial<QueryEvent> = {}): QueryEvent {
  return { type: 'tool_start', tool_use: { id: toolID, name, input: {} }, ...fields }
}

describe('event block store — reducer (moved from jobs_store)', () => {
  it('merges consecutive text deltas into one text block', () => {
    const store = createEventBlockStore()
    expect(store.apply({ type: 'text_delta', text: 'he' })).toBe(true)
    expect(store.apply({ type: 'text_delta', text: 'llo' })).toBe(true)
    const blocks = store.blocks()
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ kind: 'text', text: 'hello' })
  })

  it('tool_start builds a running tool block with the wire flags', () => {
    const store = createEventBlockStore()
    store.apply(toolStart('t1', 'Grep'))
    store.apply(toolStart('t2', 'Web'))
    const blocks = store.blocks()
    expect(blocks[0]).toMatchObject({
      kind: 'tool', id: 't1', name: 'Grep', state: 'running',
      summary: '', isSearch: false, isWeb: false, children: [],
    })
    // isWeb keys off the tool name, not a wire flag.
    expect(blocks[1]).toMatchObject({ kind: 'tool', id: 't2', name: 'Web', isWeb: true })
  })

  it('tool_start without tool_use is dropped', () => {
    const store = createEventBlockStore()
    expect(store.apply({ type: 'tool_start' })).toBe(false)
    expect(store.blocks()).toHaveLength(0)
  })

  it('tool_param_delta replaces the tool block summary and flags', () => {
    const store = createEventBlockStore()
    store.apply(toolStart('t1', 'Grep'))
    store.apply({
      type: 'tool_param_delta',
      partial_input: { id: 't1', name: 'Grep', delta: 'x', summary: 'pattern in src/', is_search: true },
    })
    const b = firstBlock(store.blocks())
    if (b.kind !== 'tool') throw new Error('expected tool block')
    expect(b.summary).toBe('pattern in src/')
    expect(b.isSearch).toBe(true)
    expect(b.state).toBe('running')
  })

  it('tool_param_delta for an unknown tool id is dropped', () => {
    const store = createEventBlockStore()
    expect(store.apply({
      type: 'tool_param_delta',
      partial_input: { id: 'ghost', name: 'Grep', delta: 'x', summary: 's' },
    })).toBe(false)
    expect(store.blocks()).toHaveLength(0)
  })

  it('tool_output_delta updates the tool block display output', () => {
    const store = createEventBlockStore()
    store.apply(toolStart('t1', 'Bash'))
    store.apply({
      type: 'tool_output_delta',
      tool_result: { tool_use_id: 't1', output: '', display_output: '3 matches' },
    })
    const b = firstBlock(store.blocks())
    if (b.kind !== 'tool') throw new Error('expected tool block')
    expect(b.displayOutput).toBe('3 matches')
  })

  it('tool_end marks the block done or error and stamps the elapsed duration', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
      const store = createEventBlockStore()
      store.apply(toolStart('t1', 'Bash'))
      vi.setSystemTime(new Date('2026-01-01T00:00:02.5Z'))
      store.apply({
        type: 'tool_end',
        tool_result: { tool_use_id: 't1', output: 'ok', display_output: 'ok' },
      })
      const done = firstBlock(store.blocks())
      if (done.kind !== 'tool') throw new Error('expected tool block')
      expect(done.state).toBe('done')
      expect(done.displayOutput).toBe('ok')
      expect(done.timingNs).toBe(2_500_000_000)

      store.apply(toolStart('t2', 'Bash'))
      store.apply({
        type: 'tool_end',
        tool_result: { tool_use_id: 't2', output: '', display_output: 'boom', is_error: true },
      })
      const blocks = store.blocks()
      const failed = blocks[1]
      if (!failed || failed.kind !== 'tool') throw new Error('expected second tool block')
      expect(failed.state).toBe('error')
    } finally {
      vi.useRealTimers()
    }
  })

  it('tool_end for one tool leaves sibling blocks untouched; unknown ids are no-ops', () => {
    // Children-clearing on tool_end needs nested routing (jobs_store) — this
    // store appends flat, so the sibling semantics are what is pinned here.
    const store = createEventBlockStore()
    store.apply(toolStart('t1', 'Agent'))
    store.apply(toolStart('t2', 'Bash'))
    store.apply({
      type: 'tool_end',
      tool_result: { tool_use_id: 't2', output: '', display_output: 'boom', is_error: true },
    })
    const blocks = store.blocks()
    expect(blocks[0]).toMatchObject({ kind: 'tool', id: 't1', state: 'running' })
    expect(blocks[1]).toMatchObject({ kind: 'tool', id: 't2', state: 'error' })

    const before = store.version()
    expect(store.apply({
      type: 'tool_end',
      tool_result: { tool_use_id: 'ghost', output: '' },
    })).toBe(false)
    expect(store.version()).toBe(before)
  })

  it('thinking_start/delta/end accumulate into one inactive thinking block', () => {
    const store = createEventBlockStore()
    store.apply({ type: 'thinking_start' })
    store.apply({ type: 'thinking_delta', thinking: { text: '先复现' } })
    store.apply({ type: 'thinking_end', thinking: { duration: 1200 } })
    const blocks = store.blocks()
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ kind: 'thinking', text: '先复现', active: false, durationNs: 1200 })
  })

  it('thinking_delta without an active thinking block is dropped', () => {
    const store = createEventBlockStore()
    expect(store.apply({ type: 'thinking_delta', thinking: { text: 'orphan' } })).toBe(false)
    expect(store.blocks()).toHaveLength(0)
  })

  it('attachment events append a user echo block, prompt preferred over content envelope', () => {
    const store = createEventBlockStore()
    store.apply({
      type: 'attachment',
      message: {
        attachment: { prompt: '跑一下 make check', mode: 'prompt' },
        content: [{ type: 'text', text: '[queued reminder envelope]' }],
      },
    })
    store.apply({
      type: 'attachment',
      message: { content: [{ type: 'text', text: 'part1 ' }, { type: 'text', text: 'part2' }] },
    })
    const blocks = store.blocks()
    expect(blocks).toHaveLength(2)
    expect(blocks[0]).toMatchObject({ kind: 'user', id: '', text: '跑一下 make check' })
    expect(blocks[1]).toMatchObject({ kind: 'user', id: '', text: 'part1 part2' })
  })

  it('attachment events with no message or empty text are dropped', () => {
    const store = createEventBlockStore()
    expect(store.apply({ type: 'attachment' })).toBe(false)
    expect(store.apply({
      type: 'attachment',
      message: { attachment: { prompt: '', mode: 'prompt' } },
    })).toBe(false)
    expect(store.apply({
      type: 'attachment',
      message: { content: [{ type: 'image', source: { media_type: 'image/png', data: 'zz' } }] },
    })).toBe(false)
    expect(store.blocks()).toHaveLength(0)
  })

  it('unknown event types change nothing', () => {
    const store = createEventBlockStore()
    expect(store.apply({ type: 'usage_event' })).toBe(false)
    expect(store.apply({ type: 'query_start' })).toBe(false)
    expect(store.version()).toBe(0)
  })
})

describe('event block store — change feed / version / reset', () => {
  it('emits one event change per content change carrying the wire event; silent otherwise', () => {
    const store = createEventBlockStore()
    const seen: string[] = []
    store.onChange((c) => seen.push(c.kind + ':' + (c.kind === 'event' ? c.e.type : 'reset')))

    store.apply(toolStart('t1', 'Bash'))
    // A param_delta whose fields are all absent still counts as changed (the
    // reducer returns true once the block is found) — pinned here so the jobs
    // feed's notify-on-every-param-delta behavior survives the move.
    store.apply({ type: 'tool_param_delta', partial_input: { id: 't1', name: 'Bash', delta: 'x' } })
    store.apply({ type: 'usage_event' })

    expect(seen).toEqual(['event:tool_start', 'event:tool_param_delta'])
  })

  it('version is monotonic and bumps only on change', () => {
    const store = createEventBlockStore()
    expect(store.version()).toBe(0)
    store.apply({ type: 'text_delta', text: 'a' })
    const v = store.version()
    expect(v).toBe(1)
    store.apply({ type: 'usage_event' })
    expect(store.version()).toBe(v)
    store.apply({ type: 'text_delta', text: 'b' })
    expect(store.version()).toBe(v + 1)
  })

  it('onChange unsubscribes cleanly', () => {
    const store = createEventBlockStore()
    let n = 0
    const off = store.onChange(() => n++)
    store.apply({ type: 'text_delta', text: 'a' })
    off()
    store.apply({ type: 'text_delta', text: 'b' })
    expect(n).toBe(1)
  })

  it('reset clears blocks, bumps the version and emits reset', () => {
    const store = createEventBlockStore()
    store.apply({ type: 'text_delta', text: 'gone' })
    const v = store.version()
    const seen: string[] = []
    store.onChange((c) => seen.push(c.kind))

    store.reset()

    expect(store.blocks()).toEqual([])
    expect(store.version()).toBe(v + 1)
    expect(seen).toEqual(['reset'])
  })
})
