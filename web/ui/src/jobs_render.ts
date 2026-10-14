import type { JobWireItem } from './types'
import type { JobsStoreHandle, FeedChange } from './jobs_store'
import { createElement } from './dom'
import { t } from './i18n'
import { wireCopyButtons } from './components/stream_blocks'
import { createFeedSink, type FeedSinkHandle } from './components/stream_sink'
import { renderToolOutput } from './tool_render'
import { createFloatButton } from './buttons'
import { toolBody } from './styles/recipes'
import type { Block } from './model'

// Follow threshold shared by bash tail feeds and agent block feeds: within
// 40px of the bottom means "pinned". Parameterized (not read off an element)
// so tests can pin the boundary exactly — jsdom has no layout.
export function nearBottom(
  scrollHeight: number,
  scrollTop: number,
  clientHeight: number,
  threshold = 40,
): boolean {
  return scrollHeight - scrollTop - clientHeight < threshold
}

export interface JobsSectionHandles {
  // Rebuilds the jobs rows (plus the expansion box of every expanded job)
  // inside sectionHost. Feed containers of expanded jobs are carried over
  // across rebuilds — re-attached, never recreated — so an update arriving
  // mid-read neither collapses the row nor jumps its scroll position.
  rebuild(
    sectionHost: HTMLElement,
    store: JobsStoreHandle,
    opts: { expanded: Set<string>; onStop: (jobID: string) => void },
  ): void
  // rAF-coalesced refresh of every live feed's content (blocks / tail).
  scheduleRender(): void
}

// Feed element trio per expanded job: the wrap is the positioning context
// for the back-to-bottom overlay button (a sibling of the scroll container,
// not a child, so it never scrolls away), the feed is the scroll container
// whose content is updated incrementally by the per-feed sink, and the
// button is the chat-scrollBtn-style follow affordance.
interface FeedView {
  wrap: HTMLElement
  feed: HTMLElement
  btn: HTMLButtonElement
  // null for bash views: no blocks ever route to a bash feed (tails arrive
  // via applyOutput only), so the tail path is their entire rendering and a
  // sink/hydration would be dead weight.
  sink: FeedSinkHandle | null
  // Bash tail string the DOM last rendered — string-guarded skip. Must init
  // at view creation: the first flush must not rebuild a body that already
  // matches the store.
  lastTail: string
  // "this view's DOM has fully absorbed the store through that version".
  // Advances only in flush's consuming branches (1 and 3) and at
  // rehydrate initiation; a branch-2 deferral leaves it behind so the next
  // rebuild's mismatch check re-renders the deferred deltas.
  lastVersion: number
}

// Per-feed deferred work, filled from granular store changes and consumed by
// one flush pass. Records for never-expanded (detached) or removed feeds are
// purged by rebuild so the queue cannot grow unboundedly.
interface DirtyRecord {
  tools: Set<string>
  containers: Set<string | null>
  dropped: Block[]
  tail: boolean
  reset: boolean
}

export function createJobsSection(): JobsSectionHandles {
  // Feed view per expanded job. Entries live exactly as long as the job
  // stays expanded and present — collapse or removal drops (and therefore
  // scroll-resets) the feed, which is the intended fresh-follow behavior.
  const feedEls = new Map<string, FeedView>()
  const dirty = new Map<string, DirtyRecord>()
  let curStore: JobsStoreHandle | null = null
  let unsub: (() => void) | null = null
  let feedUnsub: (() => void) | null = null
  let rafId = 0

  const ensureDirty = (jobID: string): DirtyRecord => {
    let rec = dirty.get(jobID)
    if (!rec) {
      rec = { tools: new Set(), containers: new Set(), dropped: [], tail: false, reset: false }
      dirty.set(jobID, rec)
    }
    return rec
  }

  // Granular change intake: only the affected job's dirty record grows, so
  // a delta touches only that feed's DOM on the next flush.
  const onFeedChange = (c: FeedChange) => {
    const rec = ensureDirty(c.jobID)
    switch (c.kind) {
      case 'event': {
        // Per-event tool-id extraction mirrors the store's own findBlock
        // lookups: tool events address the tool, everything else addresses
        // the container the event routed to.
        if (c.e.type === 'tool_param_delta') {
          if (c.e.partial_input?.id) rec.tools.add(c.e.partial_input.id)
        } else if (c.e.type === 'tool_output_delta' || c.e.type === 'tool_end') {
          if (c.e.tool_result?.tool_use_id) rec.tools.add(c.e.tool_result.tool_use_id)
        } else {
          rec.containers.add(c.parentID)
        }
        break
      }
      case 'truncated':
        rec.dropped.push(...c.dropped)
        break
      case 'tail':
        rec.tail = true
        break
      case 'reset':
        rec.reset = true
        break
    }
    scheduleRender()
  }

  const createFeedView = (job: JobWireItem): FeedView => {
    const wrap = createElement('div', 'relative mt-1.5')
    const el = createElement('div', 'jobs-feed')
    el.dataset.kind = job.kind
    el.dataset.jobId = job.id
    const feed = curStore?.feed(job.id)
    const view: FeedView = {
      wrap,
      feed: el,
      btn: null as unknown as HTMLButtonElement,
      sink:
        job.kind === 'bash'
          ? null
          : createFeedSink({
              feedEl: el,
              blocks: () => curStore?.feed(job.id)?.blocks ?? [],
              follow: () => curStore?.feed(job.id)?.follow ?? false,
            }),
      lastTail: feed?.tail ?? '',
      lastVersion: curStore?.feedVersion(job.id) ?? 0,
    }
    const btn = createFloatButton({
      position: 'center',
      innerIcon: 'scroll-to-bottom',
      onClick: () => {
        const f = curStore?.feed(job.id)
        if (f) f.follow = true
        el.scrollTop = el.scrollHeight
        btn.classList.add('opacity-0', 'pointer-events-none')
      },
    }).root
    btn.setAttribute('aria-label', t('backToBottom'))
    // Inline bottom overrides the recipe's bottom-24 (chat's main-area
    // offset): the button must hug a 180px feed, not the viewport chrome.
    // Inline style wins over the class, so no CSS surgery is needed.
    btn.style.bottom = '4px'
    el.addEventListener('scroll', () => {
      const f = curStore?.feed(job.id)
      if (!f) return
      f.follow = nearBottom(el.scrollHeight, el.scrollTop, el.clientHeight)
      btn.classList.toggle('opacity-0', f.follow)
      btn.classList.toggle('pointer-events-none', f.follow)
    })
    wrap.append(el, btn)
    view.btn = btn
    return view
  }

  // Bash tail body: the chat Bash tool-card output pipeline, shared by the
  // flush tail path and a fresh view's initial paint.
  const renderTailBody = (view: FeedView, tail: string): void => {
    const body = createElement('div', toolBody())
    body.classList.remove('ml-6', 'hidden')
    body.innerHTML = renderToolOutput(tail)
    wireCopyButtons(body)
    view.feed.replaceChildren(body)
    view.lastTail = tail
  }

  const flush = () => {
    if (!curStore) return
    for (const [id, view] of feedEls) {
      const rec = dirty.get(id)
      const feed = curStore.feed(id)
      if (!rec || !feed) continue
      // Popover closed → skip; state stays truth, the rebuild's version
      // catch-up reconciles on reopen.
      if (!view.feed.isConnected) continue
      if (rec.reset || (rec.dropped.length > 0 && view.sink !== null && view.sink.pending())) {
        // Truncation/reset is ALWAYS a wholesale rehydrate from the fresh
        // store list: the pending chunks hold pre-truncation blocks, and a
        // dropLeading into a pending snapshot could remove the hydration's
        // insertBefore anchor (NotFoundError). The clear is load-bearing,
        // not bookkeeping: a surviving dirty.reset would re-enter this
        // branch on every subsequent flush AND on every hydration onDone —
        // a self-sustaining rehydrate loop.
        if (view.sink) {
          view.sink.dispose()
          view.lastVersion = curStore.feedVersion(id)
          view.sink.hydrate(scheduleRender)
        } else {
          view.lastVersion = curStore.feedVersion(id)
        }
        dirty.delete(id)
      } else if (view.sink !== null && view.sink.pending()) {
        // Deltas are deferred; they reconcile in one pass when the
        // hydration's onDone fires scheduleRender.
        continue
      } else {
        view.sink?.dropLeading(rec.dropped)
        for (const key of rec.containers) view.sink?.syncTail(key)
        for (const toolID of rec.tools) view.sink?.patchTool(toolID)
        view.lastVersion = curStore.feedVersion(id)
        dirty.delete(id)
      }
      // Bash tail path (runs in every consuming branch, string-guarded —
      // not on the cleared flag).
      if (view.sink === null && feed.tail !== view.lastTail) {
        renderTailBody(view, feed.tail)
      }
      // Pin when following; when the user scrolled up, leave scrollTop
      // alone — re-rendered content must not yank the viewport.
      if (feed.follow) view.feed.scrollTop = view.feed.scrollHeight
      view.btn.classList.toggle('opacity-0', feed.follow)
      view.btn.classList.toggle('pointer-events-none', feed.follow)
    }
  }

  const scheduleRender = () => {
    if (rafId) return
    rafId = requestAnimationFrame(() => {
      rafId = 0
      flush()
    })
  }

  // Stop affordance per design spec: bare 28px red icon button, solid
  // rounded square (rect 3,3,18,18 rx5), brighter on hover, pressed scale.
  const buildStopButton = (j: JobWireItem, onStop: (jobID: string) => void): HTMLButtonElement => {
    const stop = createElement(
      'button',
      'jobs-stop w-7 h-7 flex items-center justify-center text-red hover:text-[#FF7B7B] active:scale-[.88] transition-[color,transform] duration-100 cursor-pointer',
    ) as HTMLButtonElement
    stop.type = 'button'
    stop.title = t('stopJob')
    stop.setAttribute('aria-label', t('stopJob'))
    stop.innerHTML =
      '<svg width="14" height="14" viewBox="0 0 24 24"><rect x="3" y="3" width="18" height="18" rx="5" fill="currentColor"/></svg>'
    stop.addEventListener('click', (e) => {
      // Same guard as the design's stop handler: the click must stop the
      // job and never leak upward into panel-level handlers.
      e.stopPropagation()
      onStop(j.id)
    })
    return stop
  }

  const buildExpansion = (
    j: JobWireItem,
    onStop: (jobID: string) => void,
  ): HTMLElement => {
    const box = createElement('div', 'jobs-exp pl-[34px] pr-2 pt-0.5 pb-2.5')
    box.dataset.jobId = j.id
    let view = feedEls.get(j.id)
    if (!view) {
      view = createFeedView(j)
      feedEls.set(j.id, view)
      // A fresh feed always starts pinned: re-expanding a row must not
      // inherit the released-follow state of the element it replaced.
      const feed = curStore?.feed(j.id)
      if (feed) feed.follow = true
      // First render from current store truth; the fresh render supersedes
      // any record accumulated while no view existed (deltas while
      // collapsed are absorbed by this snapshot).
      view.sink?.hydrate(scheduleRender)
      // Bash counterpart of the hydrate call: the connector resends a tail
      // only when it changed (jobs_panel.go diffs tailSent), so a completed
      // job's output must paint here or never — flush's tail path is
      // string-guarded against lastTail, which view creation already
      // matched to the store.
      if (view.sink === null && feed && feed.tail !== '') renderTailBody(view, feed.tail)
      dirty.delete(j.id)
    }
    box.appendChild(view.wrap)
    if (j.status === 'running') {
      const actions = createElement('div', 'row-actions flex gap-2 mt-1.5 items-center justify-end')
      actions.appendChild(buildStopButton(j, onStop))
      box.appendChild(actions)
    }
    return box
  }

  // Chips are English mono by design (Running/Done — same language as the
  // existing task rows); the surrounding UI text is i18n'd.
  const renderJobChip = (j: JobWireItem): HTMLElement => {
    const chip = createElement('span', 'mono text-[10px] flex-shrink-0')
    if (j.status === 'running') {
      chip.className += ' text-blue pulse'
      chip.textContent = 'Running'
    } else if (j.status === 'completed') {
      chip.className += ' text-green'
      chip.textContent = 'Done'
    } else if (j.status === 'failed') {
      chip.className += ' text-red'
      chip.textContent = j.exitCode ? `Exit ${j.exitCode}` : 'Failed'
    } else {
      chip.className += ' text-red'
      // Same Exit N format as the failed branch: a killed bash job carries
      // the SIGKILL exit code (137) and it is the more specific fact.
      chip.textContent = j.exitCode ? `Stopped · Exit ${j.exitCode}` : 'Stopped'
    }
    return chip
  }

  const renderJobRow = (j: JobWireItem, open: boolean, onToggle: (id: string) => void): HTMLElement => {
    const row = createElement(
      'div',
      'jobs-row flex items-center gap-2 px-2 py-1.5 rounded-lg text-[13px] cursor-pointer select-none hover:bg-white/[0.04]',
    )
    row.dataset.jobId = j.id
    if (j.status !== 'running') row.className += ' opacity-60 hover:opacity-100'
    if (open) row.classList.add('exp')

    const icon = createElement('span', 'flex-shrink-0 w-4 h-4 rounded-full flex items-center justify-center')
    if (j.status === 'running') {
      const ring = createElement('span', 'w-3.5 h-3.5 rounded-full border-2 border-blue/25 border-t-blue spin')
      icon.replaceChildren(ring)
    } else if (j.status === 'completed') {
      icon.className += ' bg-green/20 text-green'
      icon.innerHTML = '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><path d="M20 6L9 17l-5-5"/></svg>'
    } else {
      icon.className += ' bg-red/20 text-red'
      icon.innerHTML = '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><path d="M18 6L6 18M6 6l12 12"/></svg>'
    }

    const name = createElement('span', 'flex-1 min-w-0 truncate')
    if (j.kind === 'bash') {
      name.className += ' mono text-[12px]'
      name.textContent = j.command ?? ''
    } else {
      const label = j.agentType === 'fork' ? t('jobKindFork') : (j.agentType ?? '')
      name.textContent = j.description ? `${label} · ${j.description}` : label
    }

    const chev = createElement(
      'span',
      'jobs-chev flex-shrink-0 text-t3 transition-transform duration-200' + (open ? ' rotate-90' : ''),
    )
    chev.textContent = '›'

    row.append(icon, name, renderJobChip(j), chev)
    row.addEventListener('click', () => onToggle(j.id))
    return row
  }

  const rebuild = (
    sectionHost: HTMLElement,
    store: JobsStoreHandle,
    opts: { expanded: Set<string>; onStop: (jobID: string) => void },
  ): void => {
    // Feed refresh subscribes once per store (task_panel owns the store for
    // the panel's lifetime, but tests mount fresh sections per case).
    if (curStore !== store) {
      unsub?.()
      feedUnsub?.()
      curStore = store
      unsub = store.onChange(scheduleRender)
      feedUnsub = store.onFeedChange(onFeedChange)
    }

    const jobs = store.jobs()
    const live = new Set(jobs.map((j) => j.id))
    // Jobs removed from the store (cleared / engine switch) must not keep
    // phantom expansion state alive, and their dirty records must not
    // outlive the store's own truncation (dropped refs of removed feeds
    // would grow the queue unboundedly).
    for (const id of [...opts.expanded]) {
      if (!live.has(id)) opts.expanded.delete(id)
    }
    for (const id of [...dirty.keys()]) {
      if (!live.has(id)) dirty.delete(id)
    }

    // The toggle closure binds this rebuild's opts, so a click always acts
    // on the freshest expansion state even after multiple rebuilds.
    const toggle = (id: string) => {
      if (opts.expanded.has(id)) {
        opts.expanded.delete(id)
      } else {
        // Mutual exclusion, same as the design's toggleRow: one row open at
        // a time.
        opts.expanded.clear()
        opts.expanded.add(id)
      }
      rebuild(sectionHost, store, opts)
    }

    const frag = document.createDocumentFragment()
    for (const j of jobs) {
      frag.appendChild(renderJobRow(j, opts.expanded.has(j.id), toggle))
      if (opts.expanded.has(j.id)) frag.appendChild(buildExpansion(j, opts.onStop))
    }
    sectionHost.replaceChildren(frag)

    for (const id of [...feedEls.keys()]) {
      if (!opts.expanded.has(id) || !live.has(id)) {
        // dispose() cancels any pending hydration rAF chain: a view collapsed
        // mid-hydration must not keep rendering backfill into detached nodes.
        feedEls.get(id)?.sink?.dispose()
        feedEls.delete(id)
      }
    }

    // Version catch-up for carried views: stale content accumulated while
    // the popover was closed re-renders here. Assigned at initiation so
    // deltas landing during the new hydration bump the store version past
    // the watermark and the NEXT rebuild re-renders them.
    for (const j of jobs) {
      if (!opts.expanded.has(j.id) || j.kind === 'bash') continue
      const view = feedEls.get(j.id)
      if (!view?.sink) continue
      if (view.lastVersion !== store.feedVersion(j.id)) {
        view.lastVersion = store.feedVersion(j.id)
        view.sink.dispose()
        view.sink.hydrate(scheduleRender)
        dirty.delete(j.id)
      }
    }
  }

  return {
    rebuild,
    scheduleRender,
  }
}
