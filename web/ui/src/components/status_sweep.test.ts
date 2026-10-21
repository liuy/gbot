import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  writeSweepChars,
  startStatusSweep,
  stopStatusSweep,
} from './status_sweep'

// All segment text goes through the real writeSweepChars — the test targets
// the public contract, not hand-built spans.
function seg(text: string, cls?: string): HTMLSpanElement {
  const s = document.createElement('span')
  if (cls) s.className = cls
  writeSweepChars(s, text)
  return s
}

function rootWith(...segments: HTMLSpanElement[]): HTMLElement {
  const root = document.createElement('div')
  root.className = 'status-line'
  for (const s of segments) root.appendChild(s)
  // Must be connected: the engine self-stops on detached roots.
  document.body.appendChild(root)
  return root
}

function sweepClassCount(scope: ParentNode = document): number {
  return scope.querySelectorAll('.p1, .t1, .t2').length
}

describe('status_sweep', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    // Module state outlives each it — a live 110ms chain from one test would
    // leak real timers into the rest of the file.
    stopStatusSweep()
    document.body.replaceChildren()
    vi.useRealTimers()
  })

  // First it in the file: the module singleton starts null, so the first
  // stop() call below exercises the never-started null path.
  it('stopStatusSweep is null-safe before start and on repeat stop', () => {
    expect(() => stopStatusSweep()).not.toThrow()

    const root = rootWith(seg('AB'))
    startStatusSweep(root)
    vi.advanceTimersByTime(110)
    expect(document.querySelectorAll('.p1').length).toBe(1)

    stopStatusSweep()
    // This is the real per-query sequence: finalize stops once, then
    // cleanupStreamingRefs and the next query_start pre-clean stop again on
    // an already-null singleton.
    expect(() => stopStatusSweep()).not.toThrow()
    expect(document.querySelectorAll('.p1').length).toBe(0)
  })

  it('writeSweepChars splits text into .ch spans per codepoint', () => {
    const el = document.createElement('span')
    writeSweepChars(el, '↑0')
    expect(el.children.length).toBe(2)
    expect(el.children[0].classList.contains('ch')).toBe(true)
    expect(el.children[1].classList.contains('ch')).toBe(true)
    expect(el.textContent).toBe('↑0')

    writeSweepChars(el, '')
    expect(el.children.length).toBe(0)
    expect(el.textContent).toBe('')

    writeSweepChars(el, '↑中')
    expect(el.querySelectorAll('.ch').length).toBe(2)
    expect(el.textContent).toBe('↑中')
  })

  it('writeSweepChars is a no-op for identical text', () => {
    const el = document.createElement('span')
    writeSweepChars(el, 'ab')
    el.children[0].classList.add('x')
    // Rebuilding on same-text writes would drop live sweep classes (flicker).
    writeSweepChars(el, 'ab')
    expect(el.children[0].classList.contains('x')).toBe(true)
  })

  it('comet sequence skips hidden segments and follows 110ms steps', () => {
    const s1 = seg('AB')
    const hiddenSeg = seg('XY', 'hidden')
    const s2 = seg('C')
    const root = rootWith(s1, hiddenSeg, s2)
    const A = s1.children[0] as HTMLElement
    const B = s1.children[1] as HTMLElement
    const C = s2.children[0] as HTMLElement

    startStatusSweep(root)
    expect(A.classList.contains('p1')).toBe(true)

    vi.advanceTimersByTime(110)
    expect(B.classList.contains('p1')).toBe(true)
    expect(A.classList.contains('t1')).toBe(true)

    vi.advanceTimersByTime(110)
    expect(C.classList.contains('p1')).toBe(true)
    expect(B.classList.contains('t1')).toBe(true)
    expect(A.classList.contains('t2')).toBe(true)

    vi.advanceTimersByTime(110)
    expect(sweepClassCount(root)).toBe(0)

    vi.advanceTimersByTime(900)
    expect(A.classList.contains('p1')).toBe(true)

    const X = hiddenSeg.children[0] as HTMLElement
    const Y = hiddenSeg.children[1] as HTMLElement
    expect(X.classList.contains('p1')).toBe(false)
    expect(X.classList.contains('t1')).toBe(false)
    expect(X.classList.contains('t2')).toBe(false)
    expect(Y.classList.contains('p1')).toBe(false)
    expect(Y.classList.contains('t1')).toBe(false)
    expect(Y.classList.contains('t2')).toBe(false)
  })

  it('sweep timing is exactly 110ms per char and 900ms pause', () => {
    const root = rootWith(seg('ABC'))
    startStatusSweep(root)
    expect(root.querySelectorAll('.p1').length).toBe(1)

    vi.advanceTimersByTime(3 * 110 - 1)
    expect(sweepClassCount(root)).toBe(3)
    vi.advanceTimersByTime(1)
    expect(sweepClassCount(root)).toBe(0)

    vi.advanceTimersByTime(900 - 1)
    expect(sweepClassCount(root)).toBe(0)
    vi.advanceTimersByTime(1)
    expect(root.querySelectorAll('.p1').length).toBe(1)
  })

  it('stopStatusSweep clears highlights and kills the timer chain', () => {
    const root = rootWith(seg('ABCDE'))
    startStatusSweep(root)
    vi.advanceTimersByTime(220)
    expect(sweepClassCount(root)).toBe(3)

    stopStatusSweep()
    expect(sweepClassCount()).toBe(0)
    // If the chain survived, a highlight would reappear within 5s.
    vi.advanceTimersByTime(5000)
    expect(sweepClassCount()).toBe(0)
  })

  it('starting a new root clears the old root and restarts', () => {
    const r1 = rootWith(seg('AB'))
    const r2 = rootWith(seg('XY'))
    startStatusSweep(r1)
    vi.advanceTimersByTime(110)
    startStatusSweep(r2)
    expect(sweepClassCount(r1)).toBe(0)
    expect(r2.querySelectorAll('.p1').length).toBe(1)
  })

  it('repeated start on the same root is idempotent', () => {
    const root = rootWith(seg('ABC'))
    startStatusSweep(root)
    // Double start must not spawn a second timer chain: two chains share one
    // cursor and desync the sequence (class counts go 0/2).
    startStatusSweep(root)
    vi.advanceTimersByTime(110)
    expect(root.querySelectorAll('.p1').length).toBe(1)
    expect(root.querySelectorAll('.t1').length).toBe(1)

    // Mid-sweep restart must also be a no-op: the cursor keeps its position
    // instead of jumping back to the first char.
    vi.advanceTimersByTime(110)
    const peak = root.querySelector('.p1') as HTMLElement
    expect(peak.textContent).toBe('C')
    startStatusSweep(root)
    expect(root.querySelector('.p1')).toBe(peak)
  })

  it('detached root self-stops on the next step', () => {
    const root = rootWith(seg('AB'))
    startStatusSweep(root)
    expect(root.querySelectorAll('.p1').length).toBe(1)
    root.remove()
    vi.advanceTimersByTime(110)
    // Scope is the detached root: a zombie chain would keep highlighting it.
    expect(sweepClassCount(root)).toBe(0)
    vi.advanceTimersByTime(5000)
    expect(sweepClassCount(root)).toBe(0)
  })

  it('shrinking text lands the cursor in the pause branch', () => {
    const s = seg('ABCDE')
    const root = rootWith(s)
    startStatusSweep(root)
    vi.advanceTimersByTime(4 * 110)
    expect((s.children[4] as HTMLElement).classList.contains('p1')).toBe(true)

    writeSweepChars(s, 'ABC')
    vi.advanceTimersByTime(110)
    expect(sweepClassCount(root)).toBe(0)
    vi.advanceTimersByTime(900)
    expect((s.children[0] as HTMLElement).classList.contains('p1')).toBe(true)
  })

  it('growing text continues the cursor by index', () => {
    const s = seg('AB')
    const root = rootWith(s)
    startStatusSweep(root)
    vi.advanceTimersByTime(110)
    expect((s.children[1] as HTMLElement).classList.contains('p1')).toBe(true)

    writeSweepChars(s, 'ABCD')
    vi.advanceTimersByTime(110)
    expect((s.children[2] as HTMLElement).classList.contains('p1')).toBe(true)
    expect((s.children[1] as HTMLElement).classList.contains('t1')).toBe(true)
  })
})
