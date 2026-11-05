import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import type { Block } from './model'
import { createFeedSink } from './components/stream_sink'

// rAF + performance on the fake clock (takeover_hydration.test.ts pattern):
// one advanceTimersByTime(16) = exactly one backfill frame. interval + Date
// belong to the same clock: the live ticker needs a fake setInterval and
// refreshToolDuration/refreshThinkingLabel read Date.now().
function useHydrationTimers() {
  vi.useFakeTimers({
    toFake: [
      'setTimeout', 'clearTimeout',
      'setInterval', 'clearInterval',
      'requestAnimationFrame', 'cancelAnimationFrame',
      'performance', 'Date',
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

function thinkingBlock(
  text: string,
  active: boolean,
  durationNs = 0,
  startedAt = 0,
): Block {
  return { kind: 'thinking', id: '', text, durationNs, active, startedAt }
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
    const src = [thinkingBlock('plan **deep** ', true)]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const wrap = feedEl.querySelector('[data-thinking]') as HTMLElement
    expect(wrap).not.toBeNull()
    const p = wrap.querySelector('p') as HTMLElement
    expect(p.querySelector('strong')?.textContent).toBe('deep')

    ;(src[0] as { text: string }).text += 'more'
    sink.syncTail(null)
    expect(textOf(wrap)).toContain('plan deep more')

    const t = src[0] as { active: boolean; durationNs: number }
    t.active = false
    t.durationNs = 5_000_000_000
    sink.syncTail(null)

    expect(textOf(wrap)).toContain('Thought for 5s')
    expect(wrap.querySelector('p')!.classList.contains('hidden')).toBe(true)
  })

  it('seam thinking freezes past the limit and finalize converges to markdown', () => {
    useHydrationTimers()
    const base = '**deep** ' + 'A'.repeat(16384 - 9)
    const src = [thinkingBlock(base, true)]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const wrap = feedEl.querySelector('[data-thinking]') as HTMLElement
    const p = wrap.querySelector('p') as HTMLElement
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    const frozenChild = p.children[0]

    ;(src[0] as { text: string }).text += ' tail'
    sink.syncTail(null)
    const span = p.querySelector('[data-think-tail]') as HTMLSpanElement
    expect(span.textContent).toBe(' tail')
    expect(p.children[0]).toBe(frozenChild)

    const t = src[0] as { active: boolean; durationNs: number }
    t.active = false
    t.durationNs = 4_000_000_000
    sink.syncTail(null)
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    expect(p.querySelector('strong')?.textContent).toBe('deep')
    expect(textOf(wrap)).toContain('Thought for 4s')
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

describe('feed sink — live duration ticker', () => {
  it('running tool duration ticks with the live clock', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash', { startedAt: Date.now() })]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const row = feedEl.querySelector('[data-tool-root]') as HTMLElement
    expect(textOf(row)).toContain('0s') // frozen baseline before any tick

    vi.advanceTimersByTime(500)
    // Ticks land on the 200ms grid: t=400 → 0.4s.
    expect(textOf(row)).toContain('0.4s')

    vi.advanceTimersByTime(700)
    // t=1200 → 1.2s floors to '1s'; the mid-flight value was overwritten.
    expect(textOf(row)).toContain('1s')
    expect(textOf(row)).not.toContain('0.4s')
  })

  it('active thinking label ticks while active', () => {
    useHydrationTimers()
    const src = [thinkingBlock('推理', true, 0, Date.now())]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const wrap = feedEl.querySelector('[data-thinking]') as HTMLElement
    expect(textOf(wrap)).toContain('Thinking (0s)')

    vi.advanceTimersByTime(2300)
    // Last tick at t=2200 → 2.2s floors to 2.
    expect(textOf(wrap)).toContain('Thinking (2s)')
  })

  it('finished rows freeze and the ticker self-stops', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash', { startedAt: Date.now() })]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const row = feedEl.querySelector('[data-tool-root]') as HTMLElement

    const b = src[0] as ToolBlockT
    b.state = 'done'
    b.timingNs = 3_000_000_000
    sink.patchTool('t1')

    // Leading space: ' 3s' cannot match inside ' 13s'.
    expect(textOf(row)).toContain(' 3s')
    vi.advanceTimersByTime(10_000)
    expect(textOf(row)).toContain(' 3s')
    expect(textOf(row)).not.toContain('13s')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('dispose stops the ticker', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash', { startedAt: Date.now() })]
    const { sink } = makeSink(src)
    sink.hydrate()
    sink.dispose()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('detached feed self-stops', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash', { startedAt: Date.now() })]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    feedEl.remove()
    vi.advanceTimersByTime(200)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('re-registration restarts the ticker after a stop', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash', { startedAt: Date.now() })]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const b = src[0] as ToolBlockT
    b.state = 'done'
    b.timingNs = 1_000_000_000
    sink.patchTool('t1')
    vi.advanceTimersByTime(400) // past the self-stop tick
    expect(vi.getTimerCount()).toBe(0)

    src.push(toolBlock('t2', 'Bash', { startedAt: Date.now() }))
    sink.syncTail(null)
    vi.advanceTimersByTime(1200)
    const rows = feedEl.querySelectorAll('[data-tool-root]')
    expect(rows.length).toBe(2)
    expect(textOf(rows[1] as HTMLElement)).toContain('1s')
  })
})

describe('feed sink — live write follow pin', () => {
  it('syncTail pins to the stubbed bottom while following and holds when released', () => {
    useHydrationTimers()
    const src = [textBlock('a')]
    const { sink, feedEl, setFollow } = makeSink(src)
    stubScroll(feedEl)
    sink.hydrate() // single block: small path, no backfill → scrollTop stays 0

    src.push(textBlock('b'))
    sink.syncTail(null)
    // Appended content moved the bottom: following keeps the viewport pinned.
    expect(feedEl.scrollTop).toBe(feedEl.scrollHeight)

    setFollow(false)
    const held = feedEl.scrollTop
    src.push(textBlock('c'))
    sink.syncTail(null)
    expect(feedEl.scrollTop).toBe(held) // reading up: no yank back down
  })

  it('patchTool pins while following (running output growth)', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash')]
    const { sink, feedEl, setFollow } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement
    ;(root.querySelector('[data-tool-header]') as HTMLElement).click()
    // Text-length stub: patchTool only mutates inside the card, so
    // childElementCount never moves — the body text is the growth signal.
    Object.defineProperty(feedEl, 'scrollHeight', {
      configurable: true,
      get: () => 1000 + (feedEl.textContent?.length ?? 0),
    })
    Object.defineProperty(feedEl, 'clientHeight', { configurable: true, get: () => 500 })
    const before = feedEl.scrollTop

    const b = src[0] as ToolBlockT
    b.displayOutput = 'more output'
    sink.patchTool('t1')
    expect(feedEl.scrollTop).toBe(feedEl.scrollHeight)
    expect(feedEl.scrollTop).not.toBe(before)

    setFollow(false)
    const held = feedEl.scrollTop
    b.displayOutput += ' x'
    sink.patchTool('t1')
    expect(feedEl.scrollTop).toBe(held)
  })

  it('rerender (seam self-heal) also pins', () => {
    useHydrationTimers()
    const src = [textBlock('a'), textBlock('b')]
    const { sink, feedEl, setSrc } = makeSink(src)
    stubScroll(feedEl)
    sink.hydrate()

    // New refs everywhere: the tracked tail is gone → mismatch-at-0 → full
    // container re-render (childElementCount 2 → 3 under the stub).
    setSrc([textBlock('x'), textBlock('y'), textBlock('z')])
    sink.syncTail(null)

    expect(feedEl.scrollTop).toBe(feedEl.scrollHeight)
  })
})

describe('feed sink — initial state and ticker ownership', () => {
  it('a fresh sink is not pending and syncs without any prior hydrate', () => {
    useHydrationTimers()
    const src = [textBlock('a '), textBlock('b')]
    const { sink, feedEl } = makeSink(src)

    // hydrating means "a hydration is in flight" — before the first hydrate
    // there is none, so live syncs must not be silently swallowed.
    expect(sink.pending()).toBe(false)
    sink.syncTail(null)
    expect(feedEl.children.length).toBe(2)
    expect(textOf(feedEl)).toContain('a')
    expect(textOf(feedEl)).toContain('b')
  })

  it('two running tools share one ticker interval', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash'), toolBlock('t2', 'Read')]
    const { sink } = makeSink(src)
    sink.hydrate()
    // Every registration restart-checks: a second running tool must not leak
    // a second live interval.
    expect(vi.getTimerCount()).toBe(1)
  })

  it('a state flip without patchTool freezes the duration label on its last tick', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash', { startedAt: Date.now() })]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const row = feedEl.querySelector('[data-tool-root]') as HTMLElement
    vi.advanceTimersByTime(400)
    expect(textOf(row)).toContain(' 0.4s')

    // The store flips state in place; the flush that would purge the registry
    // has not run yet. The tick must skip the finished block, not keep
    // ticking its label past the freeze.
    ;(src[0] as ToolBlockT).state = 'done'
    vi.advanceTimersByTime(1000)
    expect(textOf(row)).toContain(' 0.4s')
    expect(textOf(row)).not.toContain(' 1s')
  })

  it('an active flip without syncTail freezes the thinking label on its last tick', () => {
    useHydrationTimers()
    const src = [thinkingBlock('why', true, 0, Date.now())]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const wrap = feedEl.querySelector('[data-thinking]') as HTMLElement
    vi.advanceTimersByTime(1200)
    expect(textOf(wrap)).toContain('Thinking (1s)')

    ;(src[0] as { active: boolean }).active = false
    vi.advanceTimersByTime(1200)
    expect(textOf(wrap)).toContain('Thinking (1s)')
    expect(textOf(wrap)).not.toContain('Thinking (2s)')
  })
})

describe('feed sink — registry purge completeness', () => {
  it('parent tool_end recursively purges the nested running child (ticker stops)', () => {
    useHydrationTimers()
    const src = [
      toolBlock('parent', 'Agent', { children: [toolBlock('child', 'Bash')] }),
    ]
    const { sink } = makeSink(src)
    sink.hydrate()

    const parent = src[0] as ToolBlockT
    parent.state = 'done'
    parent.timingNs = 1_000_000_000
    parent.children = []
    sink.patchTool('parent')

    // A leaked child registration keeps the interval alive forever.
    vi.advanceTimersByTime(400)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('a second child under the same parent joins the existing purge set', () => {
    useHydrationTimers()
    const src = [
      toolBlock('parent', 'Agent', { children: [toolBlock('c1', 'Bash')] }),
    ]
    const { sink } = makeSink(src)
    sink.hydrate()
    ;(src[0] as ToolBlockT).children.push(toolBlock('c2', 'Bash'))
    sink.syncTail('parent')

    const parent = src[0] as ToolBlockT
    parent.state = 'done'
    parent.timingNs = 1_000_000_000
    parent.children = []
    sink.patchTool('parent')

    // If the later registration REPLACED the child set instead of joining
    // it, c1 leaks past the parent's purge.
    vi.advanceTimersByTime(400)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('three-level nesting purges the grandchild', () => {
    useHydrationTimers()
    const src = [
      toolBlock('gp', 'Agent', {
        children: [
          toolBlock('mid', 'Agent', { children: [toolBlock('leaf', 'Bash')] }),
        ],
      }),
    ]
    const { sink } = makeSink(src)
    sink.hydrate()

    const gp = src[0] as ToolBlockT
    gp.state = 'done'
    gp.timingNs = 1_000_000_000
    gp.children = []
    sink.patchTool('gp')

    vi.advanceTimersByTime(400)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('purge clears a parent-scoped active thinking entry', () => {
    useHydrationTimers()
    const src = [
      toolBlock('parent', 'Agent', {
        children: [thinkingBlock('inner', true, 0, Date.now())],
      }),
    ]
    const { sink } = makeSink(src)
    sink.hydrate()

    const parent = src[0] as ToolBlockT
    parent.state = 'done'
    parent.timingNs = 1_000_000_000
    parent.children = []
    sink.patchTool('parent')

    vi.advanceTimersByTime(400)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('re-hydrate clears a stale running-tool registry (ticker stops once quiet)', () => {
    useHydrationTimers()
    const ghost = toolBlock('ghost', 'Bash')
    const { sink, setSrc } = makeSink([ghost])
    sink.hydrate()
    // The new snapshot drops the old tool wholesale; its registry entry must
    // not survive the hydrate reset and keep the ticker alive after the new
    // tool also finishes.
    const live = toolBlock('live', 'Bash')
    setSrc([live])
    sink.hydrate()

    live.state = 'done'
    live.timingNs = 1_000_000_000
    sink.patchTool('live')

    vi.advanceTimersByTime(400)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('re-hydrate clears a stale parent-scoped thinking registry', () => {
    useHydrationTimers()
    const first = toolBlock('p1', 'Agent', {
      children: [thinkingBlock('ghost-think', true, 0, Date.now())],
    })
    const { sink, setSrc } = makeSink([first])
    sink.hydrate()
    // p2 carries no thinking; the stale p1-scoped entry is the only thing
    // that could keep the ticker alive after p2 finishes.
    const second = toolBlock('p2', 'Agent', { children: [textBlock('q')] })
    setSrc([second])
    sink.hydrate()

    second.state = 'done'
    second.timingNs = 1_000_000_000
    second.children = []
    sink.patchTool('p2')

    vi.advanceTimersByTime(400)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('reset drops the rendered registry: dropLeading after reset is a no-op', () => {
    useHydrationTimers()
    const src = [textBlock('a'), textBlock('b'), textBlock('c')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    expect(feedEl.children.length).toBe(3)

    sink.reset()
    // reset clears tracking but leaves DOM; with no registry the sequence
    // guard has nothing to match, so the old head refs must not still drive
    // DOM removal.
    sink.dropLeading([src[0]])
    expect(feedEl.children.length).toBe(3)
  })
})

describe('feed sink — seam search self-heal', () => {
  it('older blocks appearing below the tracked window force a re-render', () => {
    useHydrationTimers()
    const x = textBlock('x-old')
    const y = textBlock('y-old')
    const { sink, feedEl, setSrc } = makeSink([x, y])
    sink.hydrate()
    expect(feedEl.children.length).toBe(2)

    // History growing BELOW the tracked window (gap before the seam): the
    // alignment window itself still matches, so only the gap check catches
    // it — skipping the re-render leaves the older blocks unrendered.
    setSrc([textBlock('a-new'), textBlock('b-new'), x, y])
    sink.syncTail(null)

    expect(feedEl.children.length).toBe(4)
    expect(textOf(feedEl)).toContain('a-new')
    expect(textOf(feedEl)).toContain('b-new')
  })

  it('a replaced head block inside the tracked window forces a re-render', () => {
    useHydrationTimers()
    const x = textBlock('x-old')
    const y = textBlock('y-old')
    const { sink, feedEl, setSrc } = makeSink([x, y])
    sink.hydrate()
    expect(feedEl.children.length).toBe(2)

    // Tracked tail (y) still present, tracked head (x) replaced: the walk
    // finds the seam, no gap exists — only per-entry alignment detects it.
    setSrc([textBlock('a-new'), y])
    sink.syncTail(null)

    expect(feedEl.children.length).toBe(2)
    expect(textOf(feedEl)).toContain('a-new')
    expect(textOf(feedEl)).not.toContain('x-old')
  })

  it('an emptied registry (all refs dropped) re-seeds on the next sync', () => {
    useHydrationTimers()
    const src = [textBlock('a'), textBlock('b')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const dropped = [src[0], src[1]]
    src.splice(0, 2)
    sink.dropLeading(dropped)
    expect(feedEl.children.length).toBe(0)

    // A registry that exists but is empty must take the re-seed path like a
    // missing one; treating it as a live seam indexes before the array.
    src.push(textBlock('c-only'))
    sink.syncTail(null)
    expect(feedEl.children.length).toBe(1)
    expect(textOf(feedEl)).toContain('c-only')
  })
})

describe('feed sink — seam patch edges', () => {
  it('empty seam text stays a no-render block while a suffix appends', () => {
    useHydrationTimers()
    const src = [textBlock('alpha'), textBlock('')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    expect(feedEl.children.length).toBe(1)

    src.push(toolBlock('t-9', 'Bash'))
    sink.syncTail(null)
    // The empty seam rendered nothing before and still renders nothing now —
    // no placeholder div may materialize ahead of the tool card.
    expect(feedEl.children.length).toBe(2)
    expect(feedEl.children[1].hasAttribute('data-tool-root')).toBe(true)
  })

  it('a delta adding a fenced block re-wires copy buttons after the morph', () => {
    useHydrationTimers()
    const src = [textBlock('```\nabc\n```')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    expect(feedEl.querySelectorAll('.copy-btn')).toHaveLength(1)

    ;(src[0] as { text: string }).text += '\n```\ndef\n```'
    sink.syncTail(null)
    // innerHTML morph preserves the OLD wrapper's injected button, but the
    // NEW wrapper only gets its button from the post-morph wire pass.
    expect(feedEl.querySelectorAll('.copy-btn')).toHaveLength(2)
  })

  it('a materialized seam div gets its copy button wired', () => {
    useHydrationTimers()
    const src = [textBlock('alpha '), textBlock('')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()

    ;(src[1] as { text: string }).text = '```\ncode\n```'
    sink.syncTail(null)
    expect(feedEl.querySelectorAll('.copy-btn')).toHaveLength(1)
  })

  it('empty thinking finalize stamps no think-length marker', () => {
    useHydrationTimers()
    const src = [thinkingBlock('', true, 0, Date.now())]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const p = feedEl.querySelector('[data-thinking] p') as HTMLElement

    const t = src[0] as { active: boolean; durationNs: number }
    t.active = false
    t.durationNs = 3_000_000_000
    sink.syncTail(null)

    expect(textOf(feedEl)).toContain('Thought for 3s')
    // An empty finalize must stay write-free: a zero-length stamp on the
    // paragraph is a hidden write the next same-length sync would skip on.
    expect(p.dataset.thinkLen).toBeUndefined()
  })

  it('finishing the seam thinking deletes its own entry (ticker stops)', () => {
    useHydrationTimers()
    const src = [thinkingBlock('plan', true, 0, Date.now())]
    const { sink } = makeSink(src)
    sink.hydrate()

    const t = src[0] as { active: boolean; durationNs: number }
    t.active = false
    t.durationNs = 2_000_000_000
    sink.syncTail(null)

    // A finished seam whose registry entry lingers keeps the interval alive.
    vi.advanceTimersByTime(400)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('a suffix active thinking keeps ticking after the seam finished', () => {
    useHydrationTimers()
    const src = [thinkingBlock('one ', true, 0, Date.now())]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()

    const t1 = src[0] as { active: boolean; durationNs: number }
    t1.active = false
    t1.durationNs = 1_000_000_000
    src.push(thinkingBlock('two', true, 0, Date.now()))
    sink.syncTail(null)

    // The suffix registration replaced the key mid-sync; finishing the seam
    // must not drop the NEW live entry — its label keeps ticking.
    vi.advanceTimersByTime(1200)
    const wraps = feedEl.querySelectorAll('[data-thinking]')
    expect(wraps.length).toBe(2)
    expect(textOf(wraps[1] as HTMLElement)).toContain('Thinking (1s)')
  })
})

describe('feed sink — registry el bookkeeping', () => {
  it('a rerendered empty-text block keeps a null el (later morph, not hijack)', () => {
    useHydrationTimers()
    const src = [textBlock('alpha '), textBlock('')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    sink.reset() // forces the next sync through the full re-seed path

    sink.syncTail(null) // re-seed: alpha div, empty text renders nothing
    ;(src[1] as { text: string }).text = 'late'
    src.push(toolBlock('t-1', 'Bash'))
    sink.syncTail(null) // suffix appends; seam materializes before the card

    const divs = Array.from(feedEl.querySelectorAll(':scope > .md-body'))
    expect(divs).toHaveLength(2)
    expect(textOf(divs[0])).toContain('alpha')
    expect(textOf(divs[1])).toContain('late')
  })

  it('a suffix block that renders nothing keeps a null el', () => {
    useHydrationTimers()
    const src = [textBlock('alpha ')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    src.push(textBlock(''))
    sink.syncTail(null)
    ;(src[1] as { text: string }).text = 'late'
    sink.syncTail(null)

    const divs = Array.from(feedEl.querySelectorAll(':scope > .md-body'))
    expect(divs).toHaveLength(2)
    expect(textOf(divs[0])).toContain('alpha')
    expect(textOf(divs[1])).toContain('late')
  })

  it('the seam materializes before the FIRST appended suffix element', () => {
    useHydrationTimers()
    const src = [textBlock('')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    expect(feedEl.children.length).toBe(0)

    ;(src[0] as { text: string }).text = 'late'
    src.push(toolBlock('t-a', 'Bash'))
    src.push(textBlock('')) // trailing suffix renders nothing
    sink.syncTail(null)

    // The materializing seam is older than every suffix element; a null el
    // at the END of the suffix would drop it below the tool card.
    const first = feedEl.children[0] as HTMLElement
    expect(textOf(first)).toContain('late')
    expect(feedEl.children.length).toBe(2)
    expect(feedEl.children[1].hasAttribute('data-tool-root')).toBe(true)
  })

  it('firstAppended is not overwritten by later suffix elements', () => {
    useHydrationTimers()
    const src = [textBlock('')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()

    ;(src[0] as { text: string }).text = 'late'
    src.push(toolBlock('t-1', 'Bash'))
    src.push(toolBlock('t-2', 'Bash'))
    sink.syncTail(null)

    const first = feedEl.children[0] as HTMLElement
    expect(textOf(first)).toContain('late')
    expect(feedEl.children[1].hasAttribute('data-tool-root')).toBe(true)
    expect(feedEl.children[2].hasAttribute('data-tool-root')).toBe(true)
  })
})

describe('feed sink — nested sync and running patch', () => {
  it('a running parent re-opens a user-collapsed children box on nested sync', () => {
    useHydrationTimers()
    const src = [
      toolBlock('parent', 'Agent', { children: [textBlock('c1')] }),
    ]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement
    const header = root.querySelector('[data-tool-header]') as HTMLElement
    header.click() // expand (running bodies start folded)
    header.click() // collapse again — the user's fold
    const box = feedEl.querySelector('[data-tool-children]') as HTMLElement
    expect(box.classList.contains('hidden')).toBe(true)

    ;(src[0] as ToolBlockT).children.push(textBlock('c2'))
    sink.syncTail('parent')

    // While the parent streams, its children box is open — the sync-driven
    // expand overrides the user's fold.
    expect(box.classList.contains('hidden')).toBe(false)
    expect(textOf(box)).toContain('c2')
  })

  it('a finished parent never re-opens a user-collapsed children box', () => {
    useHydrationTimers()
    const src = [
      toolBlock('parent', 'Agent', { children: [textBlock('c1')] }),
    ]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement
    const header = root.querySelector('[data-tool-header]') as HTMLElement
    header.click() // expand
    header.click() // the user's fold
    const box = feedEl.querySelector('[data-tool-children]') as HTMLElement
    expect(box.classList.contains('hidden')).toBe(true)

    // State flipped in place (end landed, patchTool not flushed yet): the
    // expand decision reads the live state, not the stale registration.
    ;(src[0] as ToolBlockT).state = 'done'
    ;(src[0] as ToolBlockT).children.push(textBlock('c2'))
    sink.syncTail('parent')

    expect(box.classList.contains('hidden')).toBe(true)
    expect(textOf(box)).toContain('c2')
  })

  it('patchTool while hydration is pending defers the write', () => {
    useHydrationTimers()
    const src: Block[] = Array.from({ length: 39 }, (_, i) =>
      textBlock(`blk-${i} ` + 'x'.repeat(40)),
    )
    src.push(toolBlock('t9', 'Bash'))
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    expect(sink.pending()).toBe(true)
    const row = feedEl.querySelector('[data-tool-root]') as HTMLElement

    // t9 sits in the tail window, so its handles exist while pending — the
    // defer must come from the hydrating guard, not a missing registration.
    ;(src[39] as ToolBlockT).summary = 'mid-sum'
    sink.patchTool('t9')
    expect(textOf(row)).not.toContain('mid-sum')
  })

  it('a running patch with empty output keeps the rendered body', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement
    ;(root.querySelector('[data-tool-header]') as HTMLElement).click()
    const body = root.children[1] as HTMLElement

    const b = src[0] as ToolBlockT
    b.displayOutput = 'halfway'
    sink.patchTool('t1')
    expect(textOf(body)).toContain('halfway')

    // An empty replace-window must not wipe the body the previous frame
    // rendered.
    b.displayOutput = ''
    sink.patchTool('t1')
    expect(textOf(body)).toContain('halfway')
  })

  it('a running patch never marks a non-collapsible tool collapsible', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement

    ;(src[0] as ToolBlockT).summary = 'echo hi'
    sink.patchTool('t1')

    expect(root.dataset.collapsible).toBeUndefined()
  })

  it('a running patch with no children keeps the children box folded', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement
    const box = root.querySelector('[data-tool-children]') as HTMLElement

    ;(src[0] as ToolBlockT).summary = 'x'
    sink.patchTool('t1')

    // No children to show: expanding the (always-mounted) box would surface
    // an empty bordered container.
    expect(box.classList.contains('hidden')).toBe(true)
  })

  it('an error tool finalizes red with FAIL', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement

    const b = src[0] as ToolBlockT
    b.state = 'error'
    b.timingNs = 2_000_000_000
    sink.patchTool('t1')

    expect(root.querySelector('.text-red')).not.toBeNull()
    expect(root.querySelector('.text-green')).toBeNull()
    expect(textOf(root)).toContain('FAIL')
  })

  it('patchTool end path pins while following', () => {
    useHydrationTimers()
    const src = [toolBlock('t1', 'Bash')]
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    const root = feedEl.querySelector('[data-tool-root]') as HTMLElement
    ;(root.querySelector('[data-tool-header]') as HTMLElement).click()
    Object.defineProperty(feedEl, 'scrollHeight', {
      configurable: true,
      get: () => 1000 + (feedEl.textContent?.length ?? 0),
    })
    Object.defineProperty(feedEl, 'clientHeight', { configurable: true, get: () => 500 })

    const b = src[0] as ToolBlockT
    b.state = 'done'
    b.displayOutput = 'final output body'
    b.timingNs = 3_000_000_000
    sink.patchTool('t1')

    // The terminal output grows the feed past the viewport; the finish-time
    // pin is the last chance to keep following the stream.
    expect(feedEl.scrollTop).toBe(feedEl.scrollHeight)
  })
})

describe('feed sink — dropLeading and backfill scroll', () => {
  it('dropLeading is a no-op while hydration is pending, even for a matching head ref', () => {
    useHydrationTimers()
    const src = Array.from({ length: 40 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    vi.advanceTimersByTime(16) // one backfill chunk: registry head is now src[24]
    expect(sink.pending()).toBe(true)
    expect(feedEl.children.length).toBe(16)

    // The guard exists for exactly this ref: a post-chunk head that DOES
    // identity-match. Deferral must still win over the shift.
    sink.dropLeading([src[24]])
    expect(feedEl.children.length).toBe(16)
  })

  it('dropLeading pins while following', () => {
    useHydrationTimers()
    const src = [textBlock('a'), textBlock('b'), textBlock('c')]
    const { sink, feedEl } = makeSink(src, true)
    stubScroll(feedEl)
    sink.hydrate()

    const dropped = [src[0]]
    src.splice(0, 1)
    sink.dropLeading(dropped)
    expect(feedEl.children.length).toBe(2)
    // Removing head content moves the bottom up; following re-pins to it.
    expect(feedEl.scrollTop).toBe(1100)
  })

  it('backfill restore with follow off compensates, not bottom-pins', () => {
    useHydrationTimers()
    const src = Array.from({ length: 40 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl } = makeSink(src, false)
    stubScroll(feedEl)
    sink.hydrate()

    vi.advanceTimersByTime(16)
    // Reading up while history backfills: content lands ABOVE the viewport
    // and only the height delta is compensated (stub: tail 8 = 1400px, one
    // chunk of 8 grows it to 1800 → top moves by exactly 400).
    expect(feedEl.scrollTop).toBe(400)
    expect(feedEl.scrollTop).not.toBe(feedEl.scrollHeight)
  })
})

describe('feed sink — hydrate reset and snapshot isolation', () => {
  it('hydrate resets registries: dropLeading after re-hydrate works on the new registry', () => {
    useHydrationTimers()
    const { sink, feedEl, setSrc } = makeSink([textBlock('a-old'), textBlock('b-old')])
    sink.hydrate()

    const cRef = textBlock('c-new')
    const dRef = textBlock('d-new')
    setSrc([cRef, dRef])
    sink.hydrate()
    expect(feedEl.children.length).toBe(2)

    sink.dropLeading([cRef])
    expect(feedEl.children.length).toBe(1)
    expect(textOf(feedEl)).toContain('d-new')
    expect(textOf(feedEl)).not.toContain('a-old')
  })

  it('hydrate snapshots the array: an in-place head drop mid-flight renders the captured list', () => {
    useHydrationTimers()
    const src = Array.from({ length: 40 }, (_, i) => textBlock(`blk-${i} ` + 'x'.repeat(40)))
    const { sink, feedEl } = makeSink(src)
    sink.hydrate()
    vi.advanceTimersByTime(16 * 2) // two chunks in, next=16

    // The store's cap can also splice the SAME array in place; backfill
    // indexes into the captured snapshot, so indexes must not shift.
    src.splice(0, 5)
    vi.advanceTimersByTime(16 * 20)
    expect(sink.pending()).toBe(false)
    expect(feedEl.children.length).toBe(40)
    const text = textOf(feedEl)
    for (let i = 0; i < 40; i++) {
      expect(text.split(`blk-${i} `).length - 1).toBe(1)
    }
  })
})
