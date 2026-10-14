import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import type { Block } from '../model'
import { createStreamHydration } from './stream_hydration'

// Same timer discipline as takeover_hydration.test.ts: rAF + performance on
// the fake clock so one advanceTimersByTime(16) = exactly one backfill frame.
function useHydrationTimers() {
  vi.useFakeTimers({
    toFake: [
      'setTimeout', 'clearTimeout',
      'requestAnimationFrame', 'cancelAnimationFrame',
      'performance',
    ],
  })
}

// Distinct marker per block; text blocks render exactly one .md-body div.
function textBlocks(n: number, prefix = 'blk'): Block[] {
  return Array.from({ length: n }, (_, i) => ({
    kind: 'text' as const,
    id: '',
    text: `${prefix}-${i} lorem ipsum dolor sit amet consectetur adipiscing elit **md**`,
  }))
}

function mdBodies(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll('.md-body'))
}

function mountContainer(): HTMLElement {
  document.body.innerHTML = ''
  const c = document.createElement('div')
  document.body.appendChild(c)
  return c
}

describe('stream hydration', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('small snapshots render synchronously and fire onDone in the same task', () => {
    useHydrationTimers()
    const c = mountContainer()
    let done = 0
    const h = createStreamHydration({ container: () => c, anchor: () => null })
    h.renderSnapshot(textBlocks(3), () => done++)
    expect(done).toBe(1)
    expect(mdBodies(c)).toHaveLength(3)
    expect(c.textContent).toContain('blk-0')
    expect(c.textContent).toContain('blk-2')
    vi.advanceTimersByTime(16 * 10)
    expect(mdBodies(c)).toHaveLength(3)
  })

  it('large snapshots render the tail synchronously and backfill older blocks across rAF frames', () => {
    useHydrationTimers()
    const c = mountContainer()
    let done = 0
    const h = createStreamHydration({ container: () => c, anchor: () => null })
    const blocks = textBlocks(60)
    h.renderSnapshot(blocks, () => done++)

    // Tail (last 8) is synchronous; the oldest block is not rendered yet.
    expect(c.textContent).toContain('blk-59')
    expect(c.textContent).not.toContain('blk-0')
    const syncCount = mdBodies(c).length
    expect(syncCount).toBe(8)
    expect(done).toBe(0)

    vi.advanceTimersByTime(16 * 2)
    // 8 per frame from the front of the remaining range.
    expect(mdBodies(c).length).toBe(24)

    vi.advanceTimersByTime(16 * 20)
    expect(mdBodies(c)).toHaveLength(60)
    expect(c.textContent).toContain('blk-0')
    expect(done).toBe(1)
    // Completion is one-shot: further frames are inert.
    vi.advanceTimersByTime(16 * 5)
    expect(done).toBe(1)
    expect(mdBodies(c)).toHaveLength(60)
  })

  it('backfill chunks insert ABOVE the tail, preserving document order', () => {
    useHydrationTimers()
    const c = mountContainer()
    const h = createStreamHydration({ container: () => c, anchor: () => null })
    h.renderSnapshot(textBlocks(30))
    vi.advanceTimersByTime(16 * 20)
    const text = c.textContent ?? ''
    expect(text.indexOf('blk-0')).toBeLessThan(text.indexOf('blk-1'))
    expect(text.indexOf('blk-1')).toBeLessThan(text.indexOf('blk-8'))
    expect(text.indexOf('blk-8')).toBeLessThan(text.indexOf('blk-29'))
  })

  it('flush() completes the remaining backfill synchronously and fires onDone once', () => {
    useHydrationTimers()
    const c = mountContainer()
    let done = 0
    const h = createStreamHydration({ container: () => c, anchor: () => null })
    h.renderSnapshot(textBlocks(40), () => done++)
    vi.advanceTimersByTime(16 * 2)

    h.flush()

    expect(mdBodies(c)).toHaveLength(40)
    expect(done).toBe(1)
    vi.advanceTimersByTime(16 * 10)
    expect(done).toBe(1)
    expect(mdBodies(c)).toHaveLength(40)
    // flush with nothing in flight is a no-op.
    h.flush()
    expect(done).toBe(1)
  })

  it('cancel() drops the in-flight backfill without firing onDone', () => {
    useHydrationTimers()
    const c = mountContainer()
    let done = 0
    const h = createStreamHydration({ container: () => c, anchor: () => null })
    h.renderSnapshot(textBlocks(40), () => done++)
    vi.advanceTimersByTime(16 * 2)
    const midCount = mdBodies(c).length

    h.cancel()

    vi.advanceTimersByTime(16 * 20)
    expect(done).toBe(0)
    expect(mdBodies(c).length).toBe(midCount)
  })

  it('flush() reports the remaining backfill with backfill=true (head-splice semantics)', () => {
    useHydrationTimers()
    const c = mountContainer()
    const calls: Array<{ text: string; backfill: boolean }> = []
    const h = createStreamHydration({
      container: () => c,
      anchor: () => null,
      onBlockRendered: (block, _el, backfill) => {
        if (block.kind !== 'text') return
        calls.push({ text: block.text, backfill })
      },
    })
    h.renderSnapshot(textBlocks(40))
    vi.advanceTimersByTime(16 * 2) // tail + two chunks landed; 16 remain
    calls.length = 0

    h.flush()

    // The flushed remainder is still strictly older than everything already
    // tracked: a backfill=false report would append at a registry tail and
    // corrupt consumer order.
    expect(calls).toHaveLength(16)
    expect(calls[0].text).toContain('blk-0')
    for (const call of calls) expect(call.backfill).toBe(true)
  })

  it('flush() skips the render when the container is detached but still fires onDone', () => {    useHydrationTimers()
    const c = mountContainer()
    let done = 0
    const h = createStreamHydration({ container: () => c, anchor: () => null })
    h.renderSnapshot(textBlocks(40), () => done++)
    vi.advanceTimersByTime(16 * 1)
    c.remove()

    h.flush()

    expect(done).toBe(1)
    // Tail (8) + the one landed frame (8): the detached flush rendered none.
    expect(mdBodies(c).length).toBe(16)
  })

  it('a null container makes renderSnapshot a no-op', () => {
    useHydrationTimers()
    let done = 0
    const h = createStreamHydration({ container: () => null, anchor: () => null })
    h.renderSnapshot(textBlocks(60), () => done++)
    expect(done).toBe(0)
    vi.advanceTimersByTime(16 * 30)
    expect(done).toBe(0)
  })

  it('PIN: a small renderSnapshot arriving mid-backfill discards the pending chunks; the discarded hydration never fires onDone', () => {
    useHydrationTimers()
    const c = mountContainer()
    let largeDone = 0
    let smallDone = 0
    const h = createStreamHydration({ container: () => c, anchor: () => null })
    h.renderSnapshot(textBlocks(60, 'big'), () => largeDone++)
    vi.advanceTimersByTime(16) // one backfill frame landed; more pending

    // Caller-clears contract (chat's resetAllState clears the container).
    c.replaceChildren()
    h.renderSnapshot(textBlocks(2, 'small'), () => smallDone++)

    // The small render completed synchronously...
    expect(smallDone).toBe(1)
    expect(c.textContent).toContain('small-0')
    expect(c.textContent).toContain('small-1')
    // ...and the displaced large hydration is dead, onDone never fires.
    expect(largeDone).toBe(0)

    vi.advanceTimersByTime(16 * 30)
    expect(largeDone).toBe(0)
    // Nothing further appended: exactly the small snapshot's two bodies.
    expect(mdBodies(c)).toHaveLength(2)
    expect(c.textContent).not.toContain('big-0')
  })

  it('a second LARGE renderSnapshot also discards the pending backfill', () => {
    useHydrationTimers()
    const c = mountContainer()
    let firstDone = 0
    let secondDone = 0
    const h = createStreamHydration({ container: () => c, anchor: () => null })
    h.renderSnapshot(textBlocks(60, 'one'), () => firstDone++)
    vi.advanceTimersByTime(16 * 2)
    // Caller-clears contract: chat's resetAllState clears before re-render.
    c.replaceChildren()
    h.renderSnapshot(textBlocks(50, 'two'), () => secondDone++)
    vi.advanceTimersByTime(16 * 30)

    expect(firstDone).toBe(0)
    expect(secondDone).toBe(1)
    const text = c.textContent ?? ''
    expect(text).not.toContain('one-0')
    expect(text).toContain('two-0')
    expect(text).toContain('two-49')
    expect(mdBodies(c)).toHaveLength(50)
  })

  it('onBlockRendered reports el + backfill=false for the sync tail and backfill=true for backfilled blocks, null el for empty text', () => {
    useHydrationTimers()
    const c = mountContainer()
    const calls: Array<{ text: string; el: boolean; backfill: boolean }> = []
    const h = createStreamHydration({
      container: () => c,
      anchor: () => null,
      onBlockRendered: (block, el, backfill) => {
        if (block.kind !== 'text') return
        calls.push({ text: block.text, el: !!el, backfill })
        if (block.text) expect(el).toBeInstanceOf(HTMLElement)
      },
    })
    const blocks = textBlocks(20)
    blocks[0] = { kind: 'text', id: '', text: '' } // renders nothing
    h.renderSnapshot(blocks)
    // 20 ≤ 24: small path, all sync, backfill=false, order = list order.
    expect(calls).toHaveLength(20)
    expect(calls[0]).toEqual({ text: '', el: false, backfill: false })
    for (const call of calls.slice(1)) {
      expect(call.el).toBe(true)
      expect(call.backfill).toBe(false)
    }

    // Large path: tail reports backfill=false (newest), chunks report true.
    c.replaceChildren()
    calls.length = 0
    const big = textBlocks(60, 'big')
    big[3] = { kind: 'text', id: '', text: '' }
    h.renderSnapshot(big)
    expect(calls).toHaveLength(8) // tail only so far
    for (const call of calls) expect(call.backfill).toBe(false)
    vi.advanceTimersByTime(16 * 1)
    // First chunk (blocks 44..51) landed: backfill=true entries.
    expect(calls).toHaveLength(16)
    expect(calls[8].backfill).toBe(true)
    expect(calls[8].text).toContain('big-44')
    vi.advanceTimersByTime(16 * 30)
    // The empty block at index 3 renders nothing: null el, backfill=true.
    const empty = calls.find((x) => x.text === '')
    expect(empty).toEqual({ text: '', el: false, backfill: true })
    expect(calls).toHaveLength(60)
  })

  it('renders before the anchor node when one is provided (range renders)', () => {
    useHydrationTimers()
    const c = mountContainer()
    const anchor = document.createElement('div')
    anchor.textContent = 'ANCHOR'
    c.appendChild(anchor)
    const h = createStreamHydration({ container: () => c, anchor: () => anchor })
    h.renderSnapshot(textBlocks(3, 'a'))
    const text = c.textContent ?? ''
    // All three blocks precede the anchor.
    expect(text.indexOf('a-2')).toBeLessThan(text.indexOf('ANCHOR'))
    expect(text.startsWith('a-0')).toBe(true)
  })

  it('captureScroll/restoreScroll run around each backfill insert with the hydration container', () => {
    useHydrationTimers()
    const c = mountContainer()
    const seen: string[] = []
    let scrollSeq = 0
    const h = createStreamHydration({
      container: () => c,
      anchor: () => null,
      captureScroll: (el) => {
        seen.push('capture')
        expect(el).toBe(c)
        return { top: scrollSeq, height: 100 }
      },
      restoreScroll: (prev, el) => {
        seen.push('restore')
        expect(el).toBe(c)
        expect(prev.top).toBe(scrollSeq)
        scrollSeq++
      },
    })
    h.renderSnapshot(textBlocks(30))
    vi.advanceTimersByTime(16 * 20)
    // 22 backfill blocks / 8 per frame = 3 chunk inserts, plus the tail
    // range render never scrolls (sync path measures nothing today).
    expect(seen.filter((s) => s === 'capture')).toHaveLength(3)
    expect(seen.filter((s) => s === 'restore')).toHaveLength(3)
  })
})
