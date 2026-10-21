// Comet sweep cursor engine for the streaming status line (visual baseline:
// artifacts/status-sweep-modes.html plan A). Lives at the stream_dom layer
// because the status line is pure DOM — React never touches it mid-query.
//
// Module-level singleton: only one streaming status line exists at a time.
// Starting a new root must tear down the previous sweep or two timer chains
// would fight over one shared cursor.

import { createNode } from '../dom'

export const SWEEP_STEP_MS = 110
export const SWEEP_PAUSE_MS = 900

let sweepTimer: number | null = null
let sweepRoot: HTMLElement | null = null
let cursor = -1

// Split a segment's text into per-codepoint .ch spans. Same-text calls are a
// no-op: most 200ms refresh ticks don't change the text, and rebuilding the
// spans would drop the sweep's current p1/t1 highlight — a visible flicker.
// Array.from iterates code points, so multi-byte characters stay whole.
export function writeSweepChars(el: HTMLElement, text: string): void {
  if (el.textContent === text) return
  el.replaceChildren(...Array.from(text, c => createNode('span', { className: 'ch', text: c })))
}

// A char inside a `hidden` ancestor segment (sep-cache, empty cacheEl, hidden
// toolCount) must not consume a 110ms step — the sweep would appear to stall
// on invisible characters.
function hiddenAncestor(node: HTMLElement): boolean {
  for (let el: HTMLElement | null = node; el && el !== sweepRoot; el = el.parentElement) {
    if (el.classList.contains('hidden')) return true
  }
  return false
}

function visibleChars(): HTMLElement[] {
  const root = sweepRoot
  if (!root) return []
  return Array.from(root.querySelectorAll<HTMLElement>('.ch')).filter(c => !hiddenAncestor(c))
}

function clearSweepClasses(): void {
  const root = sweepRoot
  if (!root) return
  root.querySelectorAll('.p1, .t1, .t2').forEach(el => {
    el.classList.remove('p1', 't1', 't2')
  })
}

function hardStop(): void {
  // Null root is the COMMON path, not an exception: every query stops the
  // sweep at least twice more than it starts it (finalizeProgressBar, then
  // cleanupStreamingRefs, then the next query_start pre-clean), so without
  // this guard clearSweepClasses would dereference null on every query.
  if (!sweepRoot) return
  if (sweepTimer !== null) window.clearTimeout(sweepTimer)
  sweepTimer = null
  clearSweepClasses()
  sweepRoot = null
  cursor = -1
}

function step(): void {
  // Self-heal: if the whole streaming container was swapped out (query_end
  // morph, React takeover), the sweep must not keep mutating garbage nodes.
  if (!sweepRoot || !sweepRoot.isConnected) {
    hardStop()
    return
  }
  const chars = visibleChars()
  if (cursor >= 0) clearSweepClasses()
  cursor++
  if (cursor >= chars.length) {
    cursor = -1
    sweepTimer = window.setTimeout(step, SWEEP_PAUSE_MS)
    return
  }
  chars[cursor].classList.add('p1')
  chars[cursor - 1]?.classList.add('t1')
  chars[cursor - 2]?.classList.add('t2')
  sweepTimer = window.setTimeout(step, SWEEP_STEP_MS)
}

export function startStatusSweep(root: HTMLElement): void {
  if (sweepRoot === root && sweepTimer !== null) return
  hardStop()
  sweepRoot = root
  cursor = -1
  // Synchronous first step so the first char is highlighted the instant the
  // query starts (no 110ms dead frame).
  step()
}

export function stopStatusSweep(): void {
  hardStop()
}
