import type { Block } from './model'
import type { JobWireItem } from './types'
import type { JobFeedState, JobsStoreHandle } from './jobs_store'
import { createElement } from './dom'
import { nounFor } from './utils'
import { renderMarkdown } from './markdown'
import { t } from './i18n'

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

// Flatten the block tree in document order: the server cache keeps sub-agent
// events nested under their parent tool, but the feed displays one flat
// sequence (design spec: flat order, tree is cache-only).
function flattenFeedBlocks(blocks: Block[], out: Block[]): void {
  for (const b of blocks) {
    out.push(b)
    if (b.kind === 'tool' && b.children.length > 0) flattenFeedBlocks(b.children, out)
  }
}

// Bash tail: full replacement each render (the wire always carries the whole
// 20-line window, so there is nothing to append onto).
function bashTailLines(tail: string): string[] {
  const lines = tail.split('\n')
  // A trailing newline is the window's terminator, not a blank output line.
  while (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
  return lines
}

export function createJobsSection(): JobsSectionHandles {
  // Feed element per expanded job. Entries live exactly as long as the job
  // stays expanded and present — collapse or removal drops (and therefore
  // scroll-resets) the feed, which is the intended fresh-follow behavior.
  const feedEls = new Map<string, HTMLElement>()
  let curStore: JobsStoreHandle | null = null
  let unsub: (() => void) | null = null
  let rafId = 0

  const buildPin = (el: HTMLElement, feed: JobFeedState): HTMLElement => {
    const pin = createElement('div', 'f-pin')
    const btn = createElement('button') as HTMLButtonElement
    btn.type = 'button'
    btn.textContent = '↓ ' + t('backToBottom')
    btn.addEventListener('click', (e) => {
      e.stopPropagation()
      feed.follow = true
      el.scrollTop = el.scrollHeight
      el.classList.remove('up')
    })
    pin.appendChild(btn)
    return pin
  }

  const renderBashFeed = (el: HTMLElement, feed: JobFeedState): void => {
    const frag = document.createDocumentFragment()
    for (const line of bashTailLines(feed.tail)) {
      const d = createElement('div', 'f-line')
      d.textContent = line
      frag.appendChild(d)
    }
    frag.appendChild(buildPin(el, feed))
    el.replaceChildren(frag)
  }

  const renderAgentFeed = (el: HTMLElement, feed: JobFeedState): void => {
    const flat: Block[] = []
    flattenFeedBlocks(feed.blocks, flat)
    const frag = document.createDocumentFragment()
    for (const b of flat) {
      if (b.kind === 'tool') {
        const card = createElement('div', 'f-tool')
        // Shared chat tool language: classifyTool-derived noun (Grep →
        // "Search", ls → "List") instead of the raw wire name.
        const tn = createElement('span', 'tn')
        tn.textContent = nounFor(b.name, b.isList)
        const sum = createElement('span')
        sum.textContent = b.summary
        card.append(tn, sum)
        if (b.state === 'done' || b.state === 'error') {
          const mark = createElement('span', 'f-mark ' + (b.state === 'done' ? 'ok' : 'err'))
          mark.textContent = b.state === 'done' ? '✓' : '✗'
          card.appendChild(mark)
        }
        frag.appendChild(card)
      } else if (b.kind === 'thinking') {
        const th = createElement('div', 'f-think')
        th.textContent = b.text
        frag.appendChild(th)
      } else if (b.kind === 'text') {
        const tx = createElement('div', 'f-text')
        tx.innerHTML = renderMarkdown(b.text)
        frag.appendChild(tx)
      } else if (b.kind === 'user') {
        const ln = createElement('div', 'f-line')
        ln.textContent = b.text
        frag.appendChild(ln)
      }
    }
    frag.appendChild(buildPin(el, feed))
    el.replaceChildren(frag)
  }

  const renderFeedContent = (el: HTMLElement, feed: JobFeedState): void => {
    if (el.dataset.kind === 'bash') renderBashFeed(el, feed)
    else renderAgentFeed(el, feed)
    // Pin when following; when the user scrolled up, leave scrollTop alone —
    // re-rendered content must not yank the viewport (that is the whole
    // reason feeds are carried over instead of rebuilt).
    if (feed.follow) el.scrollTop = el.scrollHeight
    el.classList.toggle('up', !feed.follow)
  }

  // Renders every tracked feed, connected or not: the section host is built
  // before the popover is attached, and a carried-over feed that went stale
  // while the popover was closed must still refresh on the next rebuild.
  const renderAllFeeds = () => {
    if (!curStore) return
    for (const [id, el] of feedEls) {
      const feed = curStore.feed(id)
      if (feed) renderFeedContent(el, feed)
    }
  }

  const scheduleRender = () => {
    if (rafId) return
    rafId = requestAnimationFrame(() => {
      rafId = 0
      renderAllFeeds()
    })
  }

  const createFeedEl = (job: JobWireItem): HTMLElement => {
    const el = createElement('div', 'jobs-feed mt-1.5')
    el.dataset.kind = job.kind
    el.dataset.jobId = job.id
    el.addEventListener('scroll', () => {
      const feed = curStore?.feed(job.id)
      if (!feed) return
      feed.follow = nearBottom(el.scrollHeight, el.scrollTop, el.clientHeight)
      el.classList.toggle('up', !feed.follow)
    })
    return el
  }

  const buildCmdBlock = (j: JobWireItem): HTMLElement => {
    const cmd = createElement(
      'div',
      'jobs-cmd mono text-[12px] leading-relaxed text-t2 bg-black/30 border border-hairline rounded-lg px-2.5 py-2 whitespace-pre-wrap break-all',
    )
    const dollar = createElement('span', 'text-t3')
    dollar.textContent = '$ '
    const text = createElement('span')
    text.textContent = j.command ?? ''
    cmd.append(dollar, text)
    return cmd
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
    if (j.kind === 'bash') box.appendChild(buildCmdBlock(j))
    let feedEl = feedEls.get(j.id)
    if (!feedEl) {
      feedEl = createFeedEl(j)
      feedEls.set(j.id, feedEl)
      // A fresh feed always starts pinned: re-expanding a row must not
      // inherit the released-follow state of the element it replaced.
      const feed = curStore?.feed(j.id)
      if (feed) feed.follow = true
    }
    box.appendChild(feedEl)
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
