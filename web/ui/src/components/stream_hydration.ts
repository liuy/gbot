// Snapshot hydration state machine extracted verbatim from chat.ts: render
// the last N blocks synchronously (live content visible immediately), then
// rAF-shard the older blocks in chunks inserted above the tail so a long
// snapshot never freezes the main task. Chat and the jobs feed sink share
// this module; consumer-specific wiring (container sourcing, scroll
// compensation, registry bookkeeping) arrives as callbacks.

import { countStreamBlocks, streamTextChars } from '../utils'
import { createFragment } from '../dom'
import type { Block } from '../model'
import { renderStreamBlock, type LiveStreamSink } from './stream_blocks'

export interface HydrationScrollSnapshot {
  top: number
  height: number
}

export interface StreamHydrationOptions {
  // null → renderSnapshot is a no-op (chat before initStreaming).
  container(): HTMLElement | null
  // before-anchor for the sync tail + small-path renders (chat's progress
  // bar). null = append at the end.
  anchor(): Node | null
  // passed through to every renderStreamBlock call.
  sink?: LiveStreamSink
  // measured after fragment build, immediately before insert — exactly where
  // chat measured. The container arg is the hydration container; consumers
  // whose scroll element differs (chat) ignore it and close over their own.
  captureScroll?(c: HTMLElement): HydrationScrollSnapshot
  restoreScroll?(prev: HydrationScrollSnapshot, c: HTMLElement): void
  // el null = the block rendered nothing (empty text). backfill = rendered
  // via insertBackfillChunk — strictly older than everything already tracked,
  // so registries splice it at the head instead of appending.
  onBlockRendered?(block: Block, el: HTMLElement | null, backfill: boolean): void
  syncBlocks?: number
  syncChars?: number
  tailBlocks?: number
  chunkBlocks?: number
}

export interface StreamHydrationHandle {
  renderSnapshot(blocks: Block[], onDone?: () => void): void
  // synchronous remainder + onDone once (query_end keeps the DOM: the
  // backfill must land now, not across frames)
  flush(): void
  // drop in-flight backfill, no onDone (the DOM is about to be replaced)
  cancel(): void
}

export function createStreamHydration(opts: StreamHydrationOptions): StreamHydrationHandle {
  const syncBlocks = opts.syncBlocks ?? 24
  const syncChars = opts.syncChars ?? 50_000
  const tailBlocks = opts.tailBlocks ?? 8
  const chunkBlocks = opts.chunkBlocks ?? 8

  interface StreamHydration {
    raf: number
    next: number              // lowest not-yet-rendered backfill index
    firstRendered: Node | null
    container: HTMLElement
    blocks: Block[]
    onDone: (() => void) | undefined
    finished: boolean
  }
  let hydration: StreamHydration | null = null

  // Renders blocks[from..to) in forward order before the anchor. The sync
  // path, the hydration tail, and the flush all produce identical DOM here.
  function renderRange(container: HTMLElement, blocks: Block[], from: number, to: number) {
    const anchor = opts.anchor()
    for (let i = from; i < to; i++) {
      const before = container.childElementCount
      renderStreamBlock(container, blocks[i], anchor, null, opts.sink)
      report(container, anchor, before, blocks[i], false)
    }
  }

  // The new element sits immediately before the anchor when one is given,
  // at the end otherwise. A block absorbed into a tool group (child count
  // unchanged) reports null like a no-render block — tool registries key off
  // the live sink, not this el.
  function report(
    container: HTMLElement,
    anchor: Node | null,
    before: number,
    block: Block,
    backfill: boolean,
  ) {
    if (!opts.onBlockRendered) return
    if (container.childElementCount <= before) {
      opts.onBlockRendered(block, null, backfill)
      return
    }
    const el = anchor
      ? (anchor as HTMLElement).previousElementSibling
      : container.lastElementChild
    opts.onBlockRendered(block, el as HTMLElement | null, backfill)
  }

  // Invalidates an in-flight hydration without flushing. Rendering into
  // doomed nodes would be wasted work.
  function cancel() {
    if (!hydration) return
    if (hydration.raf) cancelAnimationFrame(hydration.raf)
    hydration = null
  }

  function complete(h: StreamHydration) {
    if (h.finished) return
    h.finished = true
    h.onDone?.()
  }

  // Renders blocks[end..h.next) forward into a fragment, inserts it before
  // the oldest rendered node, and compensates scrollTop via the consumer
  // hooks (same compensation loadHistory uses).
  function insertBackfillChunk(h: StreamHydration, end: number) {
    const frag = createFragment()
    const anchor = null as Node | null
    for (let i = end; i < h.next; i++) {
      const before = frag.childElementCount
      renderStreamBlock(frag as unknown as HTMLElement, h.blocks[i], anchor, null, opts.sink)
      if (opts.onBlockRendered) {
        const grew = frag.childElementCount > before
        // Chunk blocks are strictly older than everything already tracked,
        // on both the step-wise and the flush path — always a head splice.
        opts.onBlockRendered(
          h.blocks[i],
          grew ? (frag.lastElementChild as HTMLElement | null) : null,
          true,
        )
      }
    }
    const chunkFirst = frag.firstChild
    const prev = opts.captureScroll?.(h.container)
    h.container.insertBefore(frag, h.firstRendered)
    if (prev && opts.restoreScroll) opts.restoreScroll(prev, h.container)
    // The oldest rendered node advances to this chunk's first node — the
    // next (older) chunk must land ABOVE it, not between it and the tail.
    // Skipped blocks (empty text) render nothing, keeping the old ref.
    if (chunkFirst) h.firstRendered = chunkFirst
    h.next = end
  }

  function step(h: StreamHydration) {
    if (hydration !== h) return
    const end = Math.max(0, h.next - chunkBlocks)
    if (end < h.next) insertBackfillChunk(h, end)
    if (h.next > 0) {
      h.raf = requestAnimationFrame(() => step(h))
    } else {
      hydration = null
      complete(h)
    }
  }

  function startHydration(container: HTMLElement, blocks: Block[], onDone: (() => void) | undefined) {
    const prevFirst = container.firstChild
    // The tail renders above the current bottom anchor, so it starts at the
    // index prevFirst occupied. childNodes[prevIndex] after the render is
    // the oldest node this hydration owns (container.firstChild when it was
    // empty) — prevFirst.nextSibling would be null here because the anchor
    // ends up BELOW the tail, not above it.
    const prevIndex = prevFirst
      ? Array.prototype.indexOf.call(container.childNodes, prevFirst)
      : 0
    const tailFrom = Math.max(0, blocks.length - tailBlocks)
    renderRange(container, blocks, tailFrom, blocks.length)
    const firstRendered = (container.childNodes[prevIndex] as Node | undefined) ?? null
    const h: StreamHydration = {
      raf: 0,
      next: tailFrom,
      firstRendered,
      container,
      blocks,
      onDone,
      finished: false,
    }
    hydration = h
    h.raf = requestAnimationFrame(() => step(h))
  }

  return {
    renderSnapshot(blocks, onDone) {
      const container = opts.container()
      if (!container) return
      // Cancel FIRST on both paths: a small snapshot arriving mid-backfill
      // must discard the pending chunks or they keep inserting stale blocks
      // above the fresh content. The displaced hydration's onDone never
      // fires (cancel semantics). (chat's original code canceled only on
      // the large path; this is the extraction's one deliberate change.)
      cancel()
      const large =
        countStreamBlocks(blocks) > syncBlocks ||
        streamTextChars(blocks) > syncChars
      if (!large) {
        renderRange(container, blocks, 0, blocks.length)
        onDone?.()
        return
      }
      startHydration(container, blocks, onDone)
    },
    flush() {
      const h = hydration
      if (!h) return
      if (h.raf) cancelAnimationFrame(h.raf)
      hydration = null
      if (h.next > 0 && h.container.isConnected) {
        insertBackfillChunk(h, 0)
      }
      complete(h)
    },
    cancel,
  }
}
