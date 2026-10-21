import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import type { Block } from './model'
import { createFeedSink } from './components/stream_sink'

// rAF + performance on the fake clock (takeover_hydration.test.ts pattern):
// one advanceTimersByTime(16) = exactly one backfill frame.
function useHydrationTimers() {
  vi.useFakeTimers({
    toFake: [
      'setTimeout', 'clearTimeout',
      'requestAnimationFrame', 'cancelAnimationFrame',
      'performance',
    ],
  })
}

type ToolBlockT = Extract<Block, { kind: 'tool' }>

function toolBlock(id: string, name: string, over: Partial<ToolBlockT> = {}): Block {
  return {
    kind: 'tool',
    id,
    name,
    summary: '',
    isSearch: false,
    isRead: false,
    isList: false,
    isLsp: false,
    isWeb: false,
    isTask: false,
    state: 'running',
    timingNs: 0,
    displayOutput: '',
    startedAt: 0,
    children: [],
    ...over,
  }
}

function textBlock(text: string): Block {
  return { kind: 'text', id: '', text }
}

function thinkingBlock(text: string, active: boolean, durationNs = 0): Block {
  return { kind: 'thinking', id: '', text, durationNs, active, startedAt: 0 }
}

interface Ctx {
  sink: ReturnType<typeof createFeedSink>
  feedEl: HTMLElement
  setSrc(next: Block[]): void
  setFollow(v: boolean): void
}

function makeSink(initial: Block[] = [], follow = true): Ctx {
  document.body.innerHTML = ''
  let src: Block[] = initial
  let fol = follow
  const feedEl = document.createElement('div')
  document.body.appendChild(feedEl)
  const sink = createFeedSink({
    feedEl,
    blocks: () => src,
    follow: () => fol,
  })
  return {
    sink,
    feedEl,
    setSrc: (next) => { src = next },
    setFollow: (v) => { fol = v },
  }
}

// Layout-neutral scroll metrics that grow with rendered content so the
// follow-pin / scroll-compensation math is exact in jsdom.
function stubScroll(el: HTMLElement) {
  Object.defineProperty(el, 'scrollHeight', {
    configurable: true,
    get: () => 1000 + el.childElementCount * 50,
  })
  Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => 500 })
}

function textOf(el: HTMLElement): string {
  return el.textContent ?? ''
}

beforeEach(() => {
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.useRealTimers()
})

describe('feed sink — append + identity', () => {
  it('syncTail appends new blocks and preserves existing node identity', () => {
    useHydrationTimers()
    const src = [textBlock('alpha '), textBlock('beta')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    expect(sink.pending()).toBe(false)
    const before = Array.from(feedEl.children)

    src.push(textBlock('gamma'))
    sink.syncTail(null)

    expect(feedEl.children.length).toBe(3)
    expect(feedEl.children[0]).toBe(before[0])
    expect(feedEl.children[1]).toBe(before[1])
    expect(textOf(feedEl.lastElementChild as HTMLElement)).toContain('gamma')
  })

  it('consecutive text deltas merge into one div; text after a tool opens a new div', () => {
    useHydrationTimers()
    const src = [textBlock('first ')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()

    // text_delta merged into the existing (seam) text block
    ;(src[0] as { text: string }).text += 'part'
    sink.syncTail(null)
    // :scope — direct children only (the tool body inside a card is .md-body too)
    const textDivs = () => Array.from(feedEl.querySelectorAll(':scope > .md-body'))
    expect(textDivs()).toHaveLength(1)
    expect(textOf(textDivs()[0])).toContain('first part')
    const firstDiv = textDivs()[0]

    // tool_start then text: the text block after a tool is a NEW div
    src.push(toolBlock('t1', 'Bash'))
    src.push(textBlock('after tool'))
    sink.syncTail(null)

    expect(feedEl.querySelector('[data-tool-root]')).not.toBeNull()
    expect(textDivs()).toHaveLength(2)
    expect(textDivs()[0]).toBe(firstDiv)
    expect(textOf(textDivs()[1])).toContain('after tool')
    // Order: text div, tool card, new text div.
    expect(feedEl.children[1].hasAttribute('data-tool-root')).toBe(true)
  })
})

describe('feed sink — thinking', () => {
  it('active thinking seam is written in place and finished on end', () => {
    useHydrationTimers()
    const src = [thinkingBlock('plan ', true)]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const wrap = feedEl.querySelector('[data-thinking]') as HTMLElement
    expect(wrap).not.toBeNull()

    ;(src[0] as { text: string }).text += 'more'
    sink.syncTail(null)
    expect(textOf(wrap)).toContain('plan more')

    const t = src[0] as { active: boolean; durationNs: number }
    t.active = false
    t.durationNs = 5_000_000_000
    sink.syncTail(null)

    expect(textOf(wrap)).toContain('Thought for 5s')
    expect(wrap.querySelector('p')!.classList.contains('hidden')).toBe(true)
  })
})

describe('feed sink — tool patch', () => {
  it('patchTool refreshes summary/output while running and finalizes on end, then purges registries', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement
    // Running feed bodies never auto-expand; the user watches by expanding,
    // after which live patches render into the visible body.
    ;(root.querySelector('[data-tool-header]') as HTMLElement).click()

    const b = src[0] as ToolBlockT
    b.summary = 'make check'
    b.displayOutput = 'halfway'
    sink.patchTool('t1')
    expect(textOf(root)).toContain('(make check)')
    expect(textOf(root)).toContain('halfway')

    b.state = 'done'
    b.displayOutput = 'final output'
    b.timingNs = 3_000_000_000
    sink.patchTool('t1')
    expect(root.querySelector('.text-green')).not.toBeNull()
    expect(textOf(root)).toContain('final output')
    expect(textOf(root)).not.toContain('halfway')

    // Registry purge: post-end patches are skipped (no handles). A mutated
    // output after the purge must not rewrite the finalized card.
    b.displayOutput = 'should not appear'
    sink.patchTool('t1')
    expect(textOf(root)).toContain('final output')
    expect(textOf(root)).not.toContain('should not appear')
  })

  it('folded feed tool defers patches; finish settles folded and expand renders the final output', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement
    const body = root.children[1] as HTMLElement
    expect(body.classList.contains('hidden')).toBe(true)

    // Running patches while folded write no DOM (render-on-visible).
    const b = src[0] as ToolBlockT
    b.displayOutput = 'halfway'
    sink.patchTool('t1')
    expect(textOf(body)).toBe('')

    b.state = 'done'
    b.displayOutput = 'final output'
    b.timingNs = 3_000_000_000
    sink.patchTool('t1')
    // Untouched body stays folded with the terminal output deferred.
    expect(body.classList.contains('hidden')).toBe(true)
    expect(textOf(body)).toBe('')

    ;(root.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(body.classList.contains('hidden')).toBe(false)
    expect(textOf(body)).toContain('final output')
    expect(textOf(body)).not.toContain('halfway')
  })

  it('tool_end collapses the children container and purges nested registries', () => {
    useHydrationTimers()
    const src = [
      toolBlock('parent', 'Agent', {
        children: [toolBlock('child', 'Bash', { displayOutput: 'child out' })],
      }),
    ]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const childBox = feedEl.querySelector('[data-tool-children]') as HTMLElement
    expect(childBox.children.length).toBe(1)

    const parent = src[0] as ToolBlockT
    parent.state = 'done'
    parent.displayOutput = 'report'
    parent.timingNs = 1_000_000_000
    parent.children = []
    sink.patchTool('parent')

    expect(childBox.children.length).toBe(0)
    expect(childBox.classList.contains('hidden')).toBe(true)
  })
})

describe('feed sink — dropLeading', () => {
  it('removes head DOM for sequence-identity-matched refs and keeps survivors identical', () => {
    useHydrationTimers()
    const src = [textBlock('b0'), textBlock('b1'), textBlock('b2'), textBlock('b3')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const before = Array.from(feedEl.children)

    const dropped = [src[0], src[1]]
    src.splice(0, 2)
    sink.dropLeading(dropped)

    expect(feedEl.children.length).toBe(2)
    expect(textOf(feedEl.children[0] as HTMLElement)).toContain('b2')
    expect(feedEl.children[0]).toBe(before[2])
    expect(feedEl.children[1]).toBe(before[3])
  })

  it('stops at the first unmatched ref — unmatched refs are a no-op', () => {
    useHydrationTimers()
    const src = [textBlock('b0'), textBlock('b1')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const before = Array.from(feedEl.children)

    // Ref does not identity-match the head: nothing happens.
    sink.dropLeading([textBlock('b1')])
    expect(feedEl.children.length).toBe(2)
    expect(Array.from(feedEl.children)).toEqual(before)
  })
})

describe('feed sink — hydration', () => {
  it('hydrates tail-first, shards backfill, and pins scroll while following', () => {
    useHydrationTimers()
    const src = Array.from({ length: 60 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl } = makeSink(src, true)
    stubScroll(feedEl)

    sink.hydrate()
    expect(sink.pending()).toBe(true)
    expect(feedEl.children.length).toBe(8)
    expect(textOf(feedEl)).toContain('blk-59')
    expect(textOf(feedEl)).not.toContain('blk-0')

    vi.advanceTimersByTime(16)
    expect(feedEl.children.length).toBe(16)
    // follow=true: every backfill insert re-pins to the bottom.
    expect(feedEl.scrollTop).toBe(feedEl.scrollHeight)

    vi.advanceTimersByTime(16 * 20)
    expect(feedEl.children.length).toBe(60)
    expect(sink.pending()).toBe(false)
    expect(textOf(feedEl)).toContain('blk-0')
  })

  it('compensates scrollTop during backfill when follow is off', () => {
    useHydrationTimers()
    const src = Array.from({ length: 40 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl } = makeSink(src, false)
    stubScroll(feedEl)
    sink.hydrate()
    vi.advanceTimersByTime(16)

    const prevTop = feedEl.scrollTop
    const prevH = feedEl.scrollHeight
    vi.advanceTimersByTime(16)
    // Inserted content lands ABOVE the viewport: top grows by the height delta.
    expect(feedEl.scrollTop).toBe(prevTop + (feedEl.scrollHeight - prevH))
  })

  it('registry stays list-aligned after hydration: post-onDone syncTail appends exactly the new suffix', () => {
    useHydrationTimers()
    const src = Array.from({ length: 40 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    vi.advanceTimersByTime(16 * 20)
    expect(sink.pending()).toBe(false)
    const tailNode = feedEl.lastElementChild as HTMLElement

    src.push(textBlock('appended-new'))
    sink.syncTail(null)

    // Exactly-once: every fixture marker appears once (a self-heal re-render
    // would also be content-correct, so node identity is the discriminator).
    expect(feedEl.children.length).toBe(41)
    expect(tailNode).toBe(feedEl.children[39])
    expect(textOf(feedEl.lastElementChild as HTMLElement)).toContain('appended-new')
    const text = textOf(feedEl)
    expect(text.split('blk-7 ').length - 1).toBe(1)
  })
})

describe('feed sink — nested children', () => {
  it('first sync re-seeds the children container exactly (I1: no duplication)', () => {
    useHydrationTimers()
    const parent = toolBlock('parent', 'Agent', {
      children: [textBlock('child-a '), textBlock('child-b')],
    })
    const src = [parent]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const box = feedEl.querySelector('[data-tool-children]') as HTMLElement
    expect(box.children.length).toBe(2)

    ;(src[0] as ToolBlockT).children.push(textBlock('child-c'))
    sink.syncTail('parent')

    expect(box.children.length).toBe(3)
    const text = textOf(box)
    expect(text.split('child-a').length - 1).toBe(1)
    expect(text.split('child-b').length - 1).toBe(1)
    expect(text).toContain('child-c')

    // Second sync: registry now exists — pure append, survivors identical.
    const before = Array.from(box.children)
    ;(src[0] as ToolBlockT).children.push(textBlock('child-d'))
    sink.syncTail('parent')
    expect(box.children.length).toBe(4)
    expect(Array.from(box.children).slice(0, 3)).toEqual(before)
  })

  it('seam mismatch (tracked tail removed) self-heals with an exact re-render', () => {
    useHydrationTimers()
    const parent = toolBlock('parent', 'Agent', {
      children: [textBlock('child-a'), toolBlock('child-t', 'Bash')],
    })
    const src = [parent]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    sink.syncTail('parent') // seed the nested registry
    const box = feedEl.querySelector('[data-tool-children]') as HTMLElement
    expect(box.children.length).toBe(2)

    // The tracked tail (the tool block) is gone from the list.
    ;(src[0] as ToolBlockT).children = [textBlock('child-a')]
    sink.syncTail('parent')

    expect(box.children.length).toBe(1)
    expect(textOf(box)).not.toContain('child-t')
    expect(textOf(box)).toContain('child-a')
  })
})

describe('feed sink — empty-el seam entry', () => {
  it('materializes the text div before a newer suffix, writes el back, and morphs on later syncs (no duplicate)', () => {
    useHydrationTimers()
    // before-param pin: the div materializes BEFORE the tool card appended
    // in the same sync, not at the container end.
    const src = [textBlock('filled '), textBlock('')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    expect(feedEl.children.length).toBe(1) // the empty block rendered nothing

    ;(src[1] as { text: string }).text = 'late text'
    src.push(toolBlock('t-9', 'Bash'))
    sink.syncTail(null)

    const toolCard = feedEl.querySelector('[data-tool-root]') as HTMLElement
    const firstPass = Array.from(feedEl.querySelectorAll(':scope > .md-body')).filter(
      (el) => textOf(el).includes('late text'),
    )
    expect(firstPass).toHaveLength(1)
    expect(
      firstPass[0].compareDocumentPosition(toolCard) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()

    // el write-back pin: a second seam patch of the same entry (still the
    // container's current-last block when its next delta lands, then a tool
    // appended in the same sync) must morph in place — a surviving null el
    // would append a duplicate div before the tool card.
    const src2 = [textBlock('filled '), textBlock('')]
    const mk = makeSink(src2)
    mk.sink.hydrate()
    ;(src2[1] as { text: string }).text = 'late text'
    mk.sink.syncTail(null) // materialize (empty suffix → container end)
    ;(src2[1] as { text: string }).text += ' extended'
    src2.push(toolBlock('t-9b', 'Bash'))
    mk.sink.syncTail(null) // seam patch again, now with a suffix

    const secondPass = Array.from(mk.feedEl.querySelectorAll(':scope > .md-body')).filter(
      (el) => textOf(el).includes('late text'),
    )
    expect(secondPass).toHaveLength(1)
    expect(textOf(secondPass[0])).toContain('late text extended')
    const toolCard2 = mk.feedEl.querySelector('[data-tool-root]') as HTMLElement
    expect(
      secondPass[0].compareDocumentPosition(toolCard2) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })
})

describe('feed sink — snapshot isolation and deferral', () => {
  it('renders the captured snapshot when the source array is replaced mid-hydration; a follow-up hydrate renders the new list', () => {
    useHydrationTimers()
    const src = Array.from({ length: 40 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl, setSrc } = makeSink(src)
    sink.hydrate()
    vi.advanceTimersByTime(16 * 2) // some chunks landed, more pending

    // The store's cap REPLACES the array; the new head drops blk-0.
    setSrc(src.slice(1))
    vi.advanceTimersByTime(16 * 20)

    // Pending chunks rendered the captured snapshot — no index-shift garbage.
    expect(feedEl.children.length).toBe(40)
    expect(textOf(feedEl)).toContain('blk-0')
    expect(textOf(feedEl)).toContain('blk-39')

    // A fresh hydrate renders the new list exactly.
    sink.hydrate()
    expect(sink.pending()).toBe(true)
    expect(feedEl.children.length).toBe(8)
    vi.advanceTimersByTime(16 * 20)
    expect(feedEl.children.length).toBe(39)
    expect(textOf(feedEl)).not.toContain('blk-0')
    expect(textOf(feedEl)).toContain('blk-39')
  })

  it('syncTail/patchTool/dropLeading are no-ops while pending; one post-onDone sync reconciles', () => {
    useHydrationTimers()
    const src = Array.from({ length: 40 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    vi.advanceTimersByTime(16) // pending
    expect(sink.pending()).toBe(true)
    const countAtDefer = feedEl.children.length

    // Deltas land while the hydration is in flight.
    const last = src[39] as { text: string }
    last.text += ' PLUS-DELTA'
    src.push(toolBlock('t-new', 'Bash'))
    sink.syncTail(null)   // must no-op
    sink.patchTool('t-new') // must no-op
    sink.dropLeading([src[0]]) // must no-op
    expect(feedEl.children.length).toBe(countAtDefer)

    vi.advanceTimersByTime(16 * 20)
    expect(sink.pending()).toBe(false)

    // The deferred reconcile (jobs_render calls syncTail again post-onDone).
    sink.syncTail(null)
    expect(feedEl.children.length).toBe(41)
    // The seam picked up the in-place tail mutation: exactly one blk-39
    // div and it carries the delta.
    const lastDiv = Array.from(feedEl.querySelectorAll('.md-body')).find(
      (el) => textOf(el).includes('blk-39'),
    )
    expect(lastDiv).toBeDefined()
    expect(textOf(lastDiv!)).toContain('PLUS-DELTA')
    expect(feedEl.lastElementChild!.hasAttribute('data-tool-root')).toBe(true)
  })

  it('reset clears registries but keeps DOM; the next sync self-heals with a full re-render', () => {
    useHydrationTimers()
    const src = [textBlock('aaa'), textBlock('bbb')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    expect(feedEl.children.length).toBe(2)

    sink.reset()
    expect(feedEl.children.length).toBe(2) // DOM untouched

    src.push(textBlock('ccc'))
    sink.syncTail(null)
    expect(feedEl.children.length).toBe(3)
    const text = textOf(feedEl)
    expect(text.split('aaa').length - 1).toBe(1)
    expect(text.split('bbb').length - 1).toBe(1)
    expect(text).toContain('ccc')
  })

  it('dispose cancels in-flight hydration and clears tracking', () => {
    useHydrationTimers()
    const src = Array.from({ length: 40 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    vi.advanceTimersByTime(16 * 2)
    expect(sink.pending()).toBe(true)
    const midCount = feedEl.children.length

    sink.dispose()
    expect(sink.pending()).toBe(false)

    vi.advanceTimersByTime(16 * 20)
    expect(feedEl.children.length).toBe(midCount)
  })
})
