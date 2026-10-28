// Per-feed incremental DOM sink for the wui task panel's job feeds: consume
// granular store changes and touch only the affected feed element (per-delta
// single-block append/patch) instead of re-rendering the whole feed. The
// snapshot path rides the shared stream_hydration module (tail-first sync +
// rAF-sharded backfill); the live path mirrors chat.ts's delta DOM-write
// semantics but reads post-mutation block state — the store owns truth.

import type { Block } from '../model'
import { findBlock } from '../jobs_store'
import { isCollapsibleBlock } from '../utils'
import { renderMarkdown } from '../markdown'
import { morphHtml } from '../morph'
import {
  renderStreamBlock,
  wireCopyButtons,
  type LiveStreamSink,
  type ThinkingEntryData,
} from './stream_blocks'
import {
  type ToolDomHandles,
  appendTextBlock,
  appendToolChildrenContainer,
  setToolSummary,
  setToolOutput,
  writeThinkingText,
  writeThinkingStream,
  finishThinking,
  finishTool,
  expandToolChildrenForRunning,
  collapseToolChildrenOnDone,
  markToolCollapsible,
} from './stream_dom'
import { createStreamHydration } from './stream_hydration'

export interface FeedSinkOptions {
  // the agent feed scroll container
  feedEl: HTMLElement
  // fresh root blocks from the store
  blocks(): Block[]
  // store follow flag, read at flush time
  follow(): boolean
}

export interface FeedSinkHandle {
  // seed registries during any renderStreamBlock call
  live: LiveStreamSink
  // forward to hydration onBlockRendered (root level)
  noteRendered(block: Block, el: HTMLElement | null, backfill: boolean): void
  // clear DOM + snapshot render via shared hydration; onDone fires when the
  // render settles (synchronously for small feeds)
  hydrate(onDone?: () => void): void
  // hydration in flight = the deferred-sync window
  pending(): boolean
  // append new blocks + patch mutated tail block of one container
  syncTail(parentID: string | null): void
  // refresh one tool card from final block state
  patchTool(toolID: string): void
  // remove head DOM for cap-dropped blocks (no-op while pending, or when the
  // registry head doesn't identity-match the dropped refs)
  dropLeading(dropped: Block[]): void
  // clear registries/tracking (DOM untouched)
  reset(): void
  // cancel hydration + reset
  dispose(): void
}

interface RenderedEntry {
  block: Block
  el: HTMLElement | null
}

export function createFeedSink(opts: FeedSinkOptions): FeedSinkHandle {
  const { feedEl, blocks, follow } = opts

  // Per container key (null = feed root, toolID = nested children box),
  // entries IN LIST ORDER. Alignment is maintained by three writers: hydration
  // reports (tail/small = append at end — the newest; backfill = splice at the
  // head — each chunk is strictly older than everything already tracked),
  // syncTail suffix appends, and dropLeading head shifts on identity match.
  const rendered = new Map<string | null, RenderedEntry[]>()
  const toolHandles = new Map<string, ToolDomHandles>()
  // parent → registered nested tool ids, for registry cleanup on tool_end
  // whose children the store already cleared.
  const childTools = new Map<string, Set<string>>()
  const activeThinking = new Map<string | null, ThinkingEntryData>()
  // Set by hydrate(), cleared by the wrapped onDone and by dispose(). While
  // true, syncTail/patchTool/dropLeading are no-ops and the renderer keeps
  // the feed's dirty record for one post-onDone reconcile pass.
  let hydrating = false

  const live: LiveStreamSink = {
    onToolRunning: (handles, block, parentID) => {
      toolHandles.set(block.id, handles)
      if (parentID !== null) {
        let s = childTools.get(parentID)
        if (!s) {
          s = new Set()
          childTools.set(parentID, s)
        }
        s.add(block.id)
      }
    },
    onThinkingActive: (entry, parentID) => {
      activeThinking.set(parentID, entry)
    },
  }

  let backfillCursor = 0
  const noteRendered = (block: Block, el: HTMLElement | null, backfill: boolean) => {
    const entry: RenderedEntry = { block, el }
    let arr = rendered.get(null)
    if (!arr) {
      arr = []
      rendered.set(null, arr)
    }
    if (!backfill) {
      // Tail/small-path renders are the newest blocks — append at the end.
      arr.push(entry)
      return
    }
    // Backfill chunks splice at the HEAD as a unit (each chunk is strictly
    // older than everything already tracked), but reports arrive per block
    // in forward order — a per-entry unshift would reverse the chunk. The
    // shared hydration pings captureScroll once per chunk, after that
    // chunk's per-block reports but before the DOM insert: the reset takes
    // effect for the NEXT chunk's splices.
    arr.splice(backfillCursor++, 0, entry)
  }

  const hydration = createStreamHydration({
    container: () => feedEl,
    anchor: () => null,
    sink: live,
    // Here the hydration container and the scroll element are the same node.
    // Doubles as the backfill chunk-boundary ping for noteRendered (above).
    captureScroll: (c) => {
      backfillCursor = 0
      return { top: c.scrollTop, height: c.scrollHeight }
    },
    restoreScroll: (prev, c) => {
      if (follow()) {
        c.scrollTop = c.scrollHeight
      } else {
        c.scrollTop = prev.top + (c.scrollHeight - prev.height)
      }
    },
    onBlockRendered: noteRendered,
  })

  const reset = () => {
    rendered.clear()
    toolHandles.clear()
    childTools.clear()
    activeThinking.clear()
    backfillCursor = 0
  }

  // Recursive registry purge on tool_end: the store cleared the block's
  // children, so every descendant registry entry is dead weight.
  const purgeTool = (id: string) => {
    const kids = childTools.get(id)
    if (kids) {
      for (const cid of kids) purgeTool(cid)
      childTools.delete(id)
    }
    toolHandles.delete(id)
    activeThinking.delete(id)
    rendered.delete(id)
  }

  // Bounded by the list size; runs once per container lifetime (unknown key,
  // empty registry) and on every seam failure. Never produces wrong DOM.
  const rerenderContainer = (container: HTMLElement, list: Block[], parentID: string | null) => {
    container.replaceChildren()
    const arr: RenderedEntry[] = []
    for (const b of list) {
      const before = container.childElementCount
      renderStreamBlock(container, b, null, parentID, live)
      const el =
        container.childElementCount > before
          ? (container.lastElementChild as HTMLElement)
          : null
      arr.push({ block: b, el })
    }
    rendered.set(parentID, arr)
  }

  // Patches the seam entry (the LAST tracked block) in place by kind, reading
  // final state. Unconditional on every syncTail: a finalize delta for block
  // N plus a tool_start (N+1) coalesced into one frame leaves the seam at N
  // with N mutated after its last render — gating the patch on "nothing new
  // appended" would permanently drop N's final delta.
  const patchSeam = (
    entry: RenderedEntry,
    container: HTMLElement,
    firstAppended: HTMLElement | null,
    seamThinking: ThinkingEntryData | undefined,
    key: string | null,
  ) => {
    const b = entry.block
    if (b.kind === 'text') {
      if (!b.text) return // still renders nothing (renderStreamBlock skips)
      if (entry.el) {
        morphHtml(entry.el, renderMarkdown(b.text))
        wireCopyButtons(entry.el)
      } else {
        // The block rendered empty earlier; now non-empty. The before param
        // is load-bearing: appending at the container end would place this
        // block AFTER the newer suffix blocks. With an empty suffix the entry
        // IS last and null (container end) is correct. Writing el back keeps
        // the next syncTail's patch from appending a duplicate div.
        const div = appendTextBlock(container, firstAppended)
        div.innerHTML = renderMarkdown(b.text)
        wireCopyButtons(div)
        entry.el = div
      }
    } else if (b.kind === 'thinking') {
      if (b.active) {
        if (seamThinking) writeThinkingStream(seamThinking.p, b.text)
      } else if (seamThinking) {
        // Frozen tails converge here; an already-rendered same-length text
        // skips inside writeThinkingText.
        if (b.text) writeThinkingText(seamThinking.p, b.text)
        finishThinking(seamThinking.p, seamThinking.labelEl, b.durationNs)
        // A suffix render may have re-registered the key with a NEW active
        // thinking — only clear the entry we actually finished.
        if (activeThinking.get(key) === seamThinking) activeThinking.delete(key)
      }
    }
    // tool/user/image/document: tools patch via patchTool; users never mutate.
  }

  const syncTail = (parentID: string | null) => {
    if (hydrating) return
    let container: HTMLElement
    let list: Block[]
    if (parentID === null) {
      container = feedEl
      list = blocks()
    } else {
      const parent = findBlock(blocks(), parentID)
      if (!parent) return
      const handles = toolHandles.get(parentID)
      // No handles: the parent finished (registries purged) or never rendered
      // running — chat drops nested DOM writes in the same situation.
      if (!handles) return
      // Mirror chat's maybeAutoExpandParent: a running parent's children box
      // is open while it streams.
      if (parent.state === 'running') expandToolChildrenForRunning(handles)
      container = appendToolChildrenContainer(handles)
      list = parent.children
    }

    const entries = rendered.get(parentID)
    if (!entries || entries.length === 0) {
      rerenderContainer(container, list, parentID)
      return
    }
    const m = entries.length
    const n = list.length
    // SUFFIX-ALIGNED seam search: walk j down from the end; the skipped
    // entries list[j+1..n-1] are blocks appended since the last sync. Never a
    // forward prefix walk — rendered[0] is a mid-list block right after a
    // tail-first hydration.
    let j = n - 1
    while (j >= 0 && list[j] !== entries[m - 1].block) j--
    // mismatch-at-0 (tracked tail gone: tool_end cleared the children, a
    // missed truncation) or mid-gap (older blocks below the tracked window —
    // impossible after a completed hydration): self-heal.
    if (j < 0 || j - (m - 1) > 0) {
      rerenderContainer(container, list, parentID)
      return
    }
    // Verify the seam by alignment across the full tracked window before
    // appending; any failure is a mismatch-at-0.
    for (let i = 0; i < m; i++) {
      if (entries[i].block !== list[j - (m - 1 - i)]) {
        rerenderContainer(container, list, parentID)
        return
      }
    }
    // Capture the thinking entry BEFORE suffix appends — a new active
    // thinking in the suffix would otherwise overwrite the key mid-patch.
    const seamThinking = activeThinking.get(parentID)
    let firstAppended: HTMLElement | null = null
    for (let k = j + 1; k < n; k++) {
      const before = container.childElementCount
      renderStreamBlock(container, list[k], null, parentID, live)
      const el =
        container.childElementCount > before
          ? (container.lastElementChild as HTMLElement)
          : null
      entries.push({ block: list[k], el })
      if (el && !firstAppended) firstAppended = el
    }
    // Suffix blocks rendered from final state this frame; the store only ever
    // mutates a container's current-last block in place, which since the
    // previous sync is either the seam or a just-appended suffix block.
    patchSeam(entries[m - 1], container, firstAppended, seamThinking, parentID)
  }

  const patchTool = (toolID: string) => {
    if (hydrating) return
    const handles = toolHandles.get(toolID)
    // Not rendered / pre-hydration → skip.
    if (!handles) return
    const b = findBlock(blocks(), toolID)
    if (!b) return
    if (b.state === 'running') {
      setToolSummary(handles, b.summary, b.name)
      if (b.displayOutput) setToolOutput(handles, b.displayOutput)
      if (isCollapsibleBlock(b)) markToolCollapsible(handles.root)
      if (b.children && b.children.length > 0) expandToolChildrenForRunning(handles)
      return
    }
    finishTool(handles, {
      isError: b.state === 'error',
      durationNs: b.timingNs,
      output: b.displayOutput,
    })
    collapseToolChildrenOnDone(handles)
    // Hours-long feeds must not leak finished-tool registries.
    purgeTool(toolID)
  }

  const dropLeading = (dropped: Block[]) => {
    if (hydrating) return
    const entries = rendered.get(null)
    if (!entries) return
    for (const ref of dropped) {
      const head = entries[0]
      // Sequence-identity guard: each dropped ref must identity-match the
      // current head in order (mirroring the store's head slice). The first
      // unmatched ref stops the shift — a truncated record deferred across a
      // rehydrate carries pre-hydration refs against a freshly seeded
      // registry, and unconditional shifting would eat live entries.
      if (!head || head.block !== ref) return
      entries.shift()
      head.el?.remove()
    }
  }

  return {
    live,
    noteRendered,
    hydrate(onDone) {
      reset()
      hydrating = true
      feedEl.replaceChildren()
      // The .slice() snapshot is load-bearing: the store's cap REPLACES the
      // blocks array and the hydration indexes backfill chunks by position —
      // a live array shrinking mid-walk shifts indexes and renders garbage.
      // Any truncation/reset landing mid-flight is discarded wholesale by
      // the renderer's dispose + rehydrate.
      hydration.renderSnapshot(blocks().slice(), () => {
        hydrating = false
        onDone?.()
      })
    },
    pending: () => hydrating,
    syncTail,
    patchTool,
    dropLeading,
    reset,
    dispose() {
      hydration.cancel()
      hydrating = false
      reset()
    },
  }
}
