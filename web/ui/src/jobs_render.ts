import type { JobWireItem } from './types'
import type { JobFeedState, JobsStoreHandle } from './jobs_store'
import { createElement } from './dom'
import { t } from './i18n'
import { renderStreamBlock, wireCopyButtons } from './components/stream_blocks'
import { renderToolOutput } from './tool_render'
import { createFloatButton } from './buttons'
import { toolBody } from './styles/recipes'

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
// whose content is fully re-rendered on every store change, and the button
// is the chat-scrollBtn-style follow affordance.
interface FeedView {
  wrap: HTMLElement
  feed: HTMLElement
  btn: HTMLButtonElement
}

export function createJobsSection(): JobsSectionHandles {
  // Feed view per expanded job. Entries live exactly as long as the job
  // stays expanded and present — collapse or removal drops (and therefore
  // scroll-resets) the feed, which is the intended fresh-follow behavior.
  const feedEls = new Map<string, FeedView>()
  let curStore: JobsStoreHandle | null = null
  let unsub: (() => void) | null = null
  let rafId = 0

  const renderFeedContent = (view: FeedView, feed: JobFeedState): void => {
    const el = view.feed
    if (el.dataset.kind === 'bash') {
      // Same pipeline as a chat Bash tool card's output body; the row label
      // already carries the command so the feed shows output only.
      const body = createElement('div', toolBody())
      body.classList.remove('ml-6', 'hidden')
      body.innerHTML = renderToolOutput(feed.tail)
      wireCopyButtons(body)
      el.replaceChildren(body)
    } else {
      const frag = document.createDocumentFragment()
      for (const b of feed.blocks) renderStreamBlock(frag as unknown as HTMLElement, b, null, null)
      el.replaceChildren(frag)
    }
    // Pin when following; when the user scrolled up, leave scrollTop alone —
    // re-rendered content must not yank the viewport (that is the whole
    // reason feeds are carried over instead of rebuilt).
    if (feed.follow) el.scrollTop = el.scrollHeight
    view.btn.classList.toggle('opacity-0', feed.follow)
    view.btn.classList.toggle('pointer-events-none', feed.follow)
  }

  // Renders every tracked feed, connected or not: the section host is built
  // before the popover is attached, and a carried-over feed that went stale
  // while the popover was closed must still refresh on the next rebuild.
  const renderAllFeeds = () => {
    if (!curStore) return
    for (const [id, view] of feedEls) {
      const feed = curStore.feed(id)
      if (feed) renderFeedContent(view, feed)
    }
  }

  const scheduleRender = () => {
    if (rafId) return
    rafId = requestAnimationFrame(() => {
      rafId = 0
      renderAllFeeds()
    })
  }

  const createFeedView = (job: JobWireItem): FeedView => {
    const wrap = createElement('div', 'relative mt-1.5')
    const el = createElement('div', 'jobs-feed')
    el.dataset.kind = job.kind
    el.dataset.jobId = job.id
    const btn = createFloatButton({
      position: 'center',
      innerIcon: 'scroll-to-bottom',
      onClick: () => {
        const feed = curStore?.feed(job.id)
        if (feed) feed.follow = true
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
      const feed = curStore?.feed(job.id)
      if (!feed) return
      feed.follow = nearBottom(el.scrollHeight, el.scrollTop, el.clientHeight)
      btn.classList.toggle('opacity-0', feed.follow)
      btn.classList.toggle('pointer-events-none', feed.follow)
    })
    wrap.append(el, btn)
    return { wrap, feed: el, btn }
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
      curStore = store
      unsub = store.onChange(scheduleRender)
    }

    const jobs = store.jobs()
    const live = new Set(jobs.map((j) => j.id))
    // Jobs removed from the store (cleared / engine switch) must not keep
    // phantom expansion state alive.
    for (const id of [...opts.expanded]) {
      if (!live.has(id)) opts.expanded.delete(id)
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
      if (!opts.expanded.has(id) || !live.has(id)) feedEls.delete(id)
    }
    renderAllFeeds()
  }

  return {
    rebuild,
    scheduleRender,
  }
}
