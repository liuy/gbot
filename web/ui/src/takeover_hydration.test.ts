import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import type { Block } from './model'
import { createChat } from './chat'

// jsdom lacks IntersectionObserver — stub it (no longer used for prefetch).
class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return [] }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver as unknown as typeof IntersectionObserver)

type Listener = (msg: unknown) => void
type StateListener = (s: string) => void

const listeners: Set<Listener> = new Set()
const stateCbs: Set<StateListener> = new Set()

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: () => {},
    connected: true,
    onStateChange: (cb: StateListener) => {
      stateCbs.add(cb)
      return () => stateCbs.delete(cb)
    },
    reconnect: () => {},
  }),
}))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

// Drives chat.ts's conn.onStateChange callback the way ws.ts does on a
// drop/reconnect cycle.
function changeState(cs: string) {
  stateCbs.forEach((fn) => fn(cs))
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

// The takeover trickle and the hydration backfill both drive off rAF +
// performance.now, so both must run on the fake clock. Each
// advanceTimersByTime(16) fires exactly one rAF tick.
function useHydrationTimers() {
  vi.useFakeTimers({
    toFake: [
      'setTimeout', 'clearTimeout',
      'requestAnimationFrame', 'cancelAnimationFrame',
      'performance',
    ],
  })
}

const BLOCK_TOTAL = 420

// 420 blocks, kind cycles text → tool → thinking, every kind embeds
// `block-${i}: ` so index-based marker assertions are kind-independent.
// Every rendered block contributes exactly one .md-body (text div / tool
// body / thinking p).
function makeSnapshotBlocks(n = BLOCK_TOTAL): Block[] {
  const kinds = ['text', 'tool', 'thinking'] as const
  const blocks: Block[] = []
  for (let i = 0; i < n; i++) {
    const marker = `block-${i}: `
    const kind = kinds[i % 3]
    if (kind === 'text') {
      let text = marker + 'lorem ipsum dolor sit amet '.repeat(24) + ' **md**'
      // One fenced code block so copy-button wiring is observable.
      if (i === 0) text += '\n```js\nconsole.log("fence-0")\n```'
      blocks.push({ kind: 'text', id: '', text })
    } else if (kind === 'tool') {
      const lines: string[] = []
      for (let k = 0; k < 40; k++) lines.push(`${marker}line-${k} of output`)
      blocks.push({
        kind: 'tool',
        id: `tool-${i}`,
        name: 'Read',
        summary: `file-${i}.txt`,
        isSearch: false,
        isRead: true,
        isList: false,
        isLsp: false,
        isWeb: false,
        isTask: false,
        state: 'done',
        timingNs: 1_000_000_000,
        displayOutput: lines.join('\n'),
        startedAt: 0,
        children: [],
      })
    } else {
      blocks.push({
        kind: 'thinking',
        id: `think-${i}`,
        text: `${marker}thinking 巴西输了`,
        durationNs: 3_000_000_000,
        active: false,
        startedAt: 0,
      })
    }
  }
  return blocks
}

// Derived from the fixture, never hardcoded: text divs, tool bodies, and
// thinking paragraphs each carry the md-body class.
function expectedMdBodies(blocks: Block[]): number {
  return (
    blocks.filter((b) => b.kind === 'text').length +
    blocks.filter((b) => b.kind === 'tool').length +
    blocks.filter((b) => b.kind === 'thinking').length
  )
}

function mdBodyCount(): number {
  return document.querySelectorAll('.md-body').length
}

function containerText(): string {
  const container = document.querySelector('.space-y-3') as HTMLElement
  return container ? (container.textContent ?? '') : ''
}

function fillOf(): HTMLElement {
  return document.querySelector('header [data-testid="takeover-bar-fill"]') as HTMLElement
}

function metadataWithSnapshot(blocks: Block[]) {
  return {
    type: 'metadata',
    connect: { connected: true },
    config: { models: [], current: { provider: 'p', model: 'm' } },
    engines: { engines: [{ id: 'e1', name: 'agent-1', model: 'm' }], activeID: 'e1' },
    history: { messages: [], nextCursor: '', hasMore: false },
    stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    snapshot: { blocks },
  }
}

const BOOT_METADATA = metadataWithSnapshot([])

// Opens a takeover window the reconnect way: the bar starts on
// 'reconnecting', and the snapshot metadata frame is what must close it
// once hydration completes.
function startTakeover() {
  dispatch({ type: 'connect_status', connected: true })
  dispatch(BOOT_METADATA)
  changeState('reconnecting')
  vi.advanceTimersByTime(100)
  changeState('connected')
}

// Layout-neutral scroll metrics that grow with rendered content, so
// scrollTop-compensation math is exact and deterministic in jsdom.
function stubScrollMetrics() {
  Object.defineProperty(Element.prototype, 'scrollHeight', {
    configurable: true,
    get() {
      return 1000 + document.querySelectorAll('.md-body').length * 50
    },
  })
  Object.defineProperty(Element.prototype, 'clientHeight', {
    configurable: true,
    get: () => 500,
  })
}

beforeEach(() => {
  listeners.clear()
  stateCbs.clear()
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.useRealTimers()
  // Restore layout-neutral metrics so stubs never leak across tests.
  Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, get: () => 0 })
  Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, get: () => 0 })
})

describe('takeover snapshot hydration', () => {
  it('renders the tail synchronously and backfills older blocks across rAF frames', () => {
    useHydrationTimers()
    mount()
    startTakeover()
    const blocks = makeSnapshotBlocks()
    const totalMd = expectedMdBodies(blocks)

    dispatch(metadataWithSnapshot(blocks))

    // Tail-first: the newest block is visible in this same task...
    expect(containerText()).toContain('block-419:')
    // ...while the oldest is not rendered yet.
    expect(containerText()).not.toContain('block-0:')
    // Partial DOM: more than nothing, fewer than the full snapshot.
    const syncMd = mdBodyCount()
    expect(syncMd).toBeGreaterThan(0)
    expect(syncMd).toBeLessThan(totalMd)
    // The bar is still working: hydration has not completed.
    const fill = fillOf()
    expect(parseFloat(fill.style.width)).toBeLessThan(100)

    // 412 backfill blocks at 8 per frame fit well inside 60 rAF ticks.
    vi.advanceTimersByTime(16 * 60)

    expect(mdBodyCount()).toBe(totalMd)
    expect(containerText()).toContain('block-0:')
    expect(fillOf().style.width).toBe('100%')
    vi.advanceTimersByTime(300)
    expect(fillOf().style.opacity).toBe('0')
  }, 15_000)

  it('backfill produces complete DOM in fixture order with parsed markdown and copy buttons', () => {
    useHydrationTimers()
    mount()
    startTakeover()
    const blocks = makeSnapshotBlocks()
    dispatch(metadataWithSnapshot(blocks))
    vi.advanceTimersByTime(16 * 60)

    expect(mdBodyCount()).toBe(expectedMdBodies(blocks))

    const text = containerText()
    // Ascending spot pairs across kind boundaries, including the last block.
    for (const [a, b] of [[0, 1], [139, 140], [209, 210], [300, 301], [418, 419]]) {
      const ia = text.indexOf(`block-${a}:`)
      const ib = text.indexOf(`block-${b}:`)
      expect(ia, `block-${a}: marker must be present`).toBeGreaterThanOrEqual(0)
      expect(ib, `block-${b}: must follow block-${a}: in DOM order`).toBeGreaterThan(ia)
    }

    const mdBodies = Array.from(document.querySelectorAll('.md-body'))
    // Text block markdown is actually parsed, not left as raw '**md**'.
    const sampleText = mdBodies.find((el) => (el.textContent ?? '').startsWith('block-3: '))
    expect(sampleText).toBeDefined()
    expect(sampleText!.innerHTML).toContain('<strong>md</strong>')

    // Tool output body carries its rendered marker lines.
    const sampleTool = mdBodies.find((el) => (el.textContent ?? '').includes('block-1: line-0 of output'))
    expect(sampleTool).toBeDefined()
    expect(sampleTool!.textContent).toContain('block-1: line-39 of output')

    // Copy-button wiring ran for the fenced code block.
    const fenced = mdBodies.find((el) => (el.textContent ?? '').includes('fence-0'))
    expect(fenced).toBeDefined()
    expect(fenced!.querySelector('.copy-btn')).not.toBeNull()

    expect(text).toContain('巴西输了')
  }, 15_000)

  it('compensates scrollTop on every backfill frame while the user is scrolled up', () => {
    useHydrationTimers()
    stubScrollMetrics()
    const chat = mount()
    startTakeover()
    const blocks = makeSnapshotBlocks()
    dispatch(metadataWithSnapshot(blocks))

    // User scrolled away from the bottom before backfill starts.
    chat.scrollEl.scrollTop = 0
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    const h = () => chat.scrollEl.scrollHeight

    for (let frame = 0; frame < 60; frame++) {
      const prevTop = chat.scrollEl.scrollTop
      const prevH = h()
      vi.advanceTimersByTime(16)
      const grew = h() - prevH
      expect(chat.scrollEl.scrollTop).toBe(prevTop + grew)
    }
    expect(mdBodyCount()).toBe(expectedMdBodies(blocks))
  }, 15_000)

  it('keeps the tail pinned to the bottom during backfill when the user is at the bottom', async () => {
    useHydrationTimers()
    stubScrollMetrics()
    const chat = mount()
    startTakeover()
    const blocks = makeSnapshotBlocks()
    dispatch(metadataWithSnapshot(blocks))

    // The empty-history load pinned scrollTop to scrollHeight with
    // isNearBottom true. Each backfill insert must re-pin through the
    // MutationObserver → scrollToBottom chain, which runs as a microtask —
    // async timer advancement flushes it between frames. One async advance
    // still interleaves microtasks after every timer it runs, and avoids
    // 60 await round-trips that push the test past the default timeout on
    // loaded CI workers.
    await vi.advanceTimersByTimeAsync(16 * 60)
    expect(chat.scrollEl.scrollTop).toBe(chat.scrollEl.scrollHeight)
    expect(mdBodyCount()).toBe(expectedMdBodies(blocks))
  }, 15_000)

  it('live text deltas during backfill append after the tail and survive completion', () => {
    useHydrationTimers()
    mount()
    startTakeover()
    const blocks = makeSnapshotBlocks()
    dispatch(metadataWithSnapshot(blocks))
    vi.advanceTimersByTime(16 * 2)

    dispatch({ type: 'event', event: { type: 'text_delta', text: 'live-tail-text' } })
    expect(document.body.textContent).toContain('live-tail-text')

    vi.advanceTimersByTime(16 * 60)

    const text = containerText()
    // block-9 is a text block: its marker appears exactly once. A tool
    // block's marker repeats on every output line, so occurrence counting
    // must use a text marker to detect double renders.
    const occurrences = text.split('block-9:').length - 1
    expect(occurrences).toBe(1)
    const toolLineOccurrences = text.split('block-7: line-0 of output').length - 1
    expect(toolLineOccurrences).toBe(1)
    expect(text).toContain('live-tail-text')
    expect(mdBodyCount()).toBe(expectedMdBodies(blocks) + 1)
    const first = text.indexOf('block-0:')
    expect(first).toBeGreaterThanOrEqual(0)
    expect(first).toBeLessThan(text.indexOf('live-tail-text'))
  }, 15_000)

  it('query_end mid-hydration flushes the remaining backfill synchronously', () => {
    useHydrationTimers()
    mount()
    startTakeover()
    const blocks = makeSnapshotBlocks()
    dispatch(metadataWithSnapshot(blocks))
    vi.advanceTimersByTime(16 * 2)

    dispatch({ type: 'event', event: { type: 'query_end' } })

    // No timer advance: the flush must complete within the query_end task.
    expect(mdBodyCount()).toBe(expectedMdBodies(blocks))
    expect(containerText()).toContain('block-0:')
    expect(fillOf().style.width).toBe('100%')
    expect(document.querySelectorAll('.heartbeat').length).toBe(0)
  }, 15_000)

  it('query_start mid-hydration finishes the stranded bar', () => {
    useHydrationTimers()
    mount()
    startTakeover()
    dispatch(metadataWithSnapshot(makeSnapshotBlocks()))
    vi.advanceTimersByTime(16 * 2)

    dispatch({ type: 'event', event: { type: 'query_start' } })

    expect(fillOf().style.width).toBe('100%')
    vi.advanceTimersByTime(300)
    expect(fillOf().style.opacity).toBe('0')
  }, 15_000)

  it('a second takeover mid-hydration discards the stale backfill', () => {
    useHydrationTimers()
    mount()
    startTakeover()
    dispatch(metadataWithSnapshot(makeSnapshotBlocks()))
    vi.advanceTimersByTime(16 * 2)

    dispatch(metadataWithSnapshot([{ kind: 'text', id: '', text: 'second-takeover' }]))

    expect(containerText()).toContain('second-takeover')
    expect(containerText()).not.toContain('block-0:')
    expect(mdBodyCount()).toBe(1)

    vi.advanceTimersByTime(16 * 60)

    expect(containerText()).not.toContain('block-0:')
    expect(mdBodyCount()).toBe(1)
  })

  it('small snapshots render synchronously and finish the bar in the same task', () => {
    useHydrationTimers()
    mount()
    startTakeover()
    dispatch(metadataWithSnapshot([{ kind: 'text', id: '', text: 'small-snapshot-text' }]))

    expect(containerText()).toContain('small-snapshot-text')
    expect(mdBodyCount()).toBe(1)
    expect(fillOf().style.width).toBe('100%')
  })

  it('standalone streamState chunks large snapshots without touching the takeover bar', () => {
    useHydrationTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const blocks = makeSnapshotBlocks()
    dispatch({ type: 'streamState', blocks })

    expect(containerText()).toContain('block-419:')
    expect(containerText()).not.toContain('block-0:')
    const syncMd = mdBodyCount()
    expect(syncMd).toBeGreaterThan(0)
    expect(syncMd).toBeLessThan(expectedMdBodies(blocks))
    // No switch window was opened: the bar stays idle throughout.
    const fill = fillOf()
    expect(fill.style.opacity).toBe('0')
    expect(fill.style.width).toBe('0%')

    vi.advanceTimersByTime(16 * 60)

    expect(mdBodyCount()).toBe(expectedMdBodies(blocks))
    expect(containerText()).toContain('block-0:')
    expect(fill.style.opacity).toBe('0')
  }, 15_000)
})
