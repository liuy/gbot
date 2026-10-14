import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createJobsSection, nearBottom } from './jobs_render'
import { createJobsStore } from './jobs_store'
import type { JobWireItem } from './types'
import type { Block } from './model'

// jsdom fires rAF as a timer; one awaited frame flushes scheduleRender.
const nextFrame = () => new Promise<void>((r) => requestAnimationFrame(() => r()))

function jw(partial: Partial<JobWireItem> & Pick<JobWireItem, 'id' | 'kind' | 'status'>): JobWireItem {
  return { startedMs: 1000, ...partial }
}

function bashJob(id: string, status: JobWireItem['status']): JobWireItem {
  return jw({ id, kind: 'bash', status, command: 'make check' })
}

function agentJob(id: string, status: JobWireItem['status']): JobWireItem {
  return jw({ id, kind: 'agent', status, agentType: 'Executor', description: '修 wui 分页 bug', parentToolUseID: 'p1' })
}

function toolBlock(id: string, name: string, over: Partial<Extract<Block, { kind: 'tool' }>> = {}): Block {
  return {
    kind: 'tool',
    id,
    name,
    summary: `${name} summary`,
    isSearch: false,
    isRead: false,
    isList: false,
    isLsp: false,
    isWeb: false,
    isTask: false,
    state: 'done',
    timingNs: 0,
    displayOutput: '',
    startedAt: 0,
    children: [],
    ...over,
  }
}

function mount() {
  document.body.innerHTML = ''
  const store = createJobsStore()
  const section = createJobsSection()
  const host = document.createElement('div')
  document.body.appendChild(host)
  const expanded = new Set<string>()
  const onStop = vi.fn()
  const opts = { expanded, onStop }
  section.rebuild(host, store, opts)
  // Store updates only refresh feed content; row rebuilds are driven by the
  // owning panel (task_panel rebuilds on job-signature change). Tests here
  // replay that by rebuilding after setJobs.
  const rerender = () => section.rebuild(host, store, opts)
  return { store, section, host, expanded, onStop, rerender }
}

function rowFor(host: HTMLElement, id: string): HTMLElement {
  return host.querySelector(`.jobs-row[data-job-id="${id}"]`) as HTMLElement
}

function expFor(host: HTMLElement, id: string): HTMLElement | null {
  return host.querySelector(`.jobs-exp[data-job-id="${id}"]`)
}

describe('nearBottom', () => {
  it('pins within 40px and releases beyond it', () => {
    // 100 - 41 - 20 = 39 gap → pinned.
    expect(nearBottom(100, 41, 20)).toBe(true)
    // 100 - 39 - 20 = 41 gap → released.
    expect(nearBottom(100, 39, 20)).toBe(false)
  })
  it('honors an explicit threshold', () => {
    expect(nearBottom(100, 60, 20, 25)).toBe(true)
    expect(nearBottom(100, 50, 20, 25)).toBe(false)
  })
})

describe('jobs section — rows', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('renders the three states: running ring, completed check, failed cross with exit code', () => {
    const ctx = mount()
    ctx.store.setJobs([
      bashJob('bg-run', 'running'),
      bashJob('bg-done', 'completed'),
      jw({ id: 'bg-fail', kind: 'bash', status: 'failed', command: 'x', exitCode: 130 }),
      jw({ id: 'bg-kill', kind: 'bash', status: 'killed', command: 'x' }),
      jw({ id: 'bg-kill137', kind: 'bash', status: 'killed', command: 'x', exitCode: 137 }),
    ])
    ctx.rerender()
    expect(rowFor(ctx.host, 'bg-run').querySelector('.spin')).not.toBeNull()
    const doneRow = rowFor(ctx.host, 'bg-done')
    expect(doneRow.querySelector('svg path')?.getAttribute('d')).toBe('M20 6L9 17l-5-5')
    const failRow = rowFor(ctx.host, 'bg-fail')
    expect(failRow.querySelector('svg path')?.getAttribute('d')).toBe('M18 6L6 18M6 6l12 12')
    expect(failRow.querySelector('.mono.text-red')?.textContent).toBe('Exit 130')
    expect(rowFor(ctx.host, 'bg-kill').querySelector('.mono.text-red')?.textContent).toBe('Stopped')
    expect(rowFor(ctx.host, 'bg-kill137').querySelector('.mono.text-red')?.textContent).toBe('Stopped · Exit 137')
  })
})

describe('jobs section — bash expansion', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('bash expansion shows only the output feed', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    const row = rowFor(ctx.host, 'bg-1')
    row.click()
    // The toggle rebuilds the section: re-query, the clicked node is stale.
    expect(rowFor(ctx.host, 'bg-1').classList.contains('exp')).toBe(true)
    expect(rowFor(ctx.host, 'bg-1').querySelector('.jobs-chev')?.classList.contains('rotate-90')).toBe(true)
    // The row label already carries the command: no command block.
    expect(expFor(ctx.host, 'bg-1')?.querySelector('.jobs-cmd')).toBeNull()

    // Empty tail: feed present, no exception, and the body is not created
    // yet — renderToolOutput('') defers until the first output arrives.
    await nextFrame()
    const emptyFeed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(emptyFeed).not.toBeNull()
    expect(emptyFeed.querySelectorAll('.md-body')).toHaveLength(0)
    ctx.store.applyOutput('bg-1', 'go vet ./...\nok  pkg/engine  1.2s\n')
    await nextFrame()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    // Output goes through the chat Bash-card pipeline: a single md-body.
    const bodies = feed.querySelectorAll('.md-body')
    expect(bodies).toHaveLength(1)
    expect(bodies[0].textContent).toContain('go vet ./...')
    expect(bodies[0].textContent).toContain('ok  pkg/engine  1.2s')
    // Following by default: the back-to-bottom button stays hidden.
    const btn = feed.parentElement?.querySelector('button') as HTMLButtonElement
    expect(btn.classList.contains('opacity-0')).toBe(true)
  })

  it('stop button calls onStop with the job id and does not toggle the row', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    const stop = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-stop') as HTMLButtonElement
    stop.click()
    expect(ctx.onStop).toHaveBeenCalledTimes(1)
    expect(ctx.onStop).toHaveBeenCalledWith('bg-1')
    // Behavioral contract, not a stopPropagation claim: the stop click must
    // stop the job without toggling the row — still expanded afterwards.
    expect(rowFor(ctx.host, 'bg-1').classList.contains('exp')).toBe(true)
    expect(expFor(ctx.host, 'bg-1')).not.toBeNull()
    // Control case: the row's own click does collapse it, so the assertions
    // above are live against a working toggle, not vacuously green.
    rowFor(ctx.host, 'bg-1').click()
    expect(ctx.host.querySelector('.jobs-exp')).toBeNull()
    expect(rowFor(ctx.host, 'bg-1').classList.contains('exp')).toBe(false)
  })

  it('terminal rows render no stop button', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'completed')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    expect(expFor(ctx.host, 'bg-1')).not.toBeNull()
    expect(ctx.host.querySelector('.jobs-stop')).toBeNull()
  })
})

describe('jobs section — agent feed', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('renders tool cards (chat stream semantics), thinking, and markdown text in order', () => {
    const ctx = mount()
    const blocks: Block[] = [
      toolBlock('t-1', 'Grep'),
      { kind: 'thinking', id: '', text: '先复现', durationNs: 0, active: false, startedAt: 0 },
      { kind: 'text', id: '', text: '测试通过 **1335**/1335' },
      toolBlock('t-2', 'Read', { state: 'error', children: [{ kind: 'text', id: '', text: 'nested report' }] }),
    ]
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement

    const tools = Array.from(feed.querySelectorAll('[data-tool-root]'))
    expect(tools).toHaveLength(2)
    // Raw wire tool name — chat stream rendering never applies classifyTool.
    expect(tools[0].dataset.toolName).toBe('Grep')
    expect(tools[0].querySelector('.text-green')).not.toBeNull()
    // toolName is text-sm; the duration label is the only font-mono text-xs.
    const durEl = tools[1].querySelector('.font-mono.text-xs') as HTMLElement
    expect(durEl.textContent).toBe(' FAIL · 0s')
    expect(tools[1].querySelector('.text-red')).not.toBeNull()

    const think = feed.querySelectorAll('[data-thinking]')
    expect(think).toHaveLength(1)
    expect(think[0].textContent).toContain('先复现')
    expect(think[0].textContent).toContain('Thought for 0s')

    // textBlock is .text-t1; thinkingText is .text-t2 — sizes apart.
    const texts = Array.from(feed.querySelectorAll('.md-body.text-t1'))
    expect(texts).toHaveLength(2)
    expect(texts[0].innerHTML).toContain('<strong>1335</strong>')
    // Chat nesting semantics: sub-agent output lives in the parent card's
    // children container (collapsed on done, but present in the DOM).
    const children = tools[1].querySelector('[data-tool-children]') as HTMLElement
    expect(children.classList.contains('hidden')).toBe(true)
    expect(children.textContent?.trim()).toBe('nested report')
  })

  it('live events grow the expanded feed via the rAF-coalesced render', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feed.querySelectorAll('[data-tool-root]')).toHaveLength(0)

    ctx.store.applyEvent({
      type: 'tool_start',
      tool_use: { id: 't-1', name: 'Read', input: {} },
      agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 },
    })
    ctx.store.applyEvent({
      type: 'text_delta',
      text: 'found it',
      agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 },
    })
    await nextFrame()
    expect(feed.querySelectorAll('[data-tool-root]')).toHaveLength(1)
    expect(feed.textContent).toContain('found it')
  })

  it('renders attachment events as user lines in the feed', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    ctx.store.applyEvent({
      type: 'attachment',
      message: { attachment: { mode: 'prompt', prompt: '继续跑压测并记录 p99' } },
      agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 },
    })
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    // 'user' blocks render as the chat queued-message echo (gray), not
    // markdown text.
    const spans = Array.from(feed.querySelectorAll('span')).filter(
      (s) => s.textContent === '继续跑压测并记录 p99',
    )
    expect(spans).toHaveLength(1)
    expect(spans[0].className).toContain('text-t3')
    expect(feed.querySelectorAll('.md-body.text-t1')).toHaveLength(0)
  })

  it('renders drained reminder envelopes as Reminder rows, never the raw envelope', async () => {
    const ctx = mount()
    const envelope =
      '<system-reminder>\nA background command completed:\n<job-notification>\n<job-id>bg-1</job-id>\n' +
      '<status>completed</status>\n<summary>Background command &quot;make check&quot; completed (exit code 0)</summary>\n' +
      '</job-notification>\n</system-reminder>'
    const blocks: Block[] = [{ kind: 'user', id: '', text: envelope, startedAt: 1760358185000 }]
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    // Same anatomy as the chat stream's reminder row: collapsed tool-block
    // row with the parsed summary, not a gray queued echo of the envelope.
    const rows = Array.from(feed.querySelectorAll('[data-reminder-row]'))
    expect(rows).toHaveLength(1)
    expect(rows[0].dataset.toolName).toBe('Reminder')
    const summary = rows[0].querySelector('span.font-light')
    expect(summary?.textContent).toBe(' (command make check · exit 0)')
    // The envelope only lives inside the collapsed row body — never as a
    // bare gray echo span.
    const echo = Array.from(feed.querySelectorAll('span')).filter(
      (s) => s.className.includes('text-t3') && s.textContent?.includes('<system-reminder>'),
    )
    expect(echo).toHaveLength(0)
    expect(rows[0].querySelector('.md-body')?.textContent).toContain('</job-notification>')
  })
})

describe('jobs section — expansion lifecycle', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('opening a second row collapses the first (mutual exclusion)', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running'), bashJob('bg-2', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    expect(expFor(ctx.host, 'bg-1')).not.toBeNull()
    rowFor(ctx.host, 'bg-2').click()
    expect(expFor(ctx.host, 'bg-1')).toBeNull()
    expect(expFor(ctx.host, 'bg-2')).not.toBeNull()
    expect(ctx.expanded).toEqual(new Set(['bg-2']))
    // Clicking the open row collapses it.
    rowFor(ctx.host, 'bg-2').click()
    expect(ctx.host.querySelector('.jobs-exp')).toBeNull()
    expect(ctx.expanded.size).toBe(0)
  })

  it('rebuild carries the expanded feed view over instead of recreating it', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    const before = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement

    // Status flip forces a full row rebuild (task_panel signature change).
    ctx.store.setJobs([bashJob('bg-1', 'completed')])
    ctx.rerender()

    const rowAfter = rowFor(ctx.host, 'bg-1')
    expect(rowAfter.classList.contains('exp')).toBe(true)
    const after = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    // Same nodes re-attached: scrollTop and listeners survive, so a rebuild
    // can neither collapse the row nor jump its scroll.
    expect(after).toBe(before)
    // The whole view trio (wrap positioning context + follow button) is
    // carried with the feed, not just the scroll container.
    const wrapBefore = before.parentElement as HTMLElement
    const wrapAfter = after.parentElement as HTMLElement
    expect(wrapAfter).toBe(wrapBefore)
    expect(wrapAfter.querySelector('button')).toBe(wrapBefore.querySelector('button'))
    // Terminal now: the stop affordance is gone from the rebuilt box.
    expect(expFor(ctx.host, 'bg-1')?.querySelector('.jobs-stop')).toBeNull()
  })

  it('collapse drops the feed element so re-expanding starts fresh at follow', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    ctx.store.applyOutput('bg-1', 'a\n')
    const first = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    // Release follow before collapsing, so the reset is observable.
    Object.defineProperty(first, 'scrollHeight', { value: 500 })
    Object.defineProperty(first, 'clientHeight', { value: 200 })
    first.scrollTop = 100
    first.dispatchEvent(new Event('scroll'))
    expect(ctx.store.feed('bg-1')?.follow).toBe(false)

    rowFor(ctx.host, 'bg-1').click()
    rowFor(ctx.host, 'bg-1').click()
    const second = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(second).not.toBe(first)
    expect(ctx.store.feed('bg-1')?.follow).toBe(true)
    // Fresh follow state: the re-expanded view's button starts hidden.
    const btn = second.parentElement?.querySelector('button') as HTMLButtonElement
    expect(btn.classList.contains('opacity-0')).toBe(true)
  })

  it('prunes expansion state for jobs that left the snapshot', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    ctx.store.setJobs([])
    ctx.rerender()
    expect(ctx.expanded.size).toBe(0)
    expect(ctx.host.querySelectorAll('.jobs-row')).toHaveLength(0)
  })
})

describe('jobs section — follow button', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  async function expandedBashFeed() {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    ctx.store.applyOutput('bg-1', 'a\nb\nc\n')
    await nextFrame()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    // jsdom has no layout: stub the metrics the scroll handler reads.
    Object.defineProperty(feed, 'scrollHeight', { value: 500 })
    Object.defineProperty(feed, 'clientHeight', { value: 200 })
    return { ctx, feed }
  }

  it('scrolling up releases follow and shows the back-to-bottom button; the button re-pins', async () => {
    const { ctx, feed } = await expandedBashFeed()
    feed.scrollTop = 100 // 500 - 100 - 200 = 200 > 40
    feed.dispatchEvent(new Event('scroll'))
    expect(ctx.store.feed('bg-1')?.follow).toBe(false)
    // The wrap holds exactly one button: the follow affordance.
    const btn = feed.parentElement?.querySelector('button') as HTMLButtonElement
    expect(btn.getAttribute('aria-label')).toBe('Back to bottom')
    expect(btn.classList.contains('opacity-0')).toBe(false)

    btn.click()
    expect(ctx.store.feed('bg-1')?.follow).toBe(true)
    expect(btn.classList.contains('opacity-0')).toBe(true)
    expect(feed.scrollTop).toBe(500)
  })

  it('scrolling near the bottom keeps follow and never shows the button', async () => {
    const { ctx, feed } = await expandedBashFeed()
    feed.scrollTop = 269 // 500 - 269 - 200 = 31 < 40
    feed.dispatchEvent(new Event('scroll'))
    expect(ctx.store.feed('bg-1')?.follow).toBe(true)
    expect((feed.parentElement?.querySelector('button') as HTMLButtonElement).classList.contains('opacity-0')).toBe(true)
  })

  it('re-render pins scrollTop to the stubbed bottom while following and holds it when scrolled up', async () => {
    const { ctx, feed } = await expandedBashFeed()
    const btn = feed.parentElement?.querySelector('button') as HTMLButtonElement
    // Following: new output scrolls the feed to the bottom.
    ctx.store.applyOutput('bg-1', 'd\n')
    await nextFrame()
    expect(feed.scrollTop).toBe(500)
    expect(btn.classList.contains('opacity-0')).toBe(true)

    // Scrolled up (500 - 100 - 200 = 200 > 40): a re-render must not yank
    // the viewport back to the bottom.
    feed.scrollTop = 100
    feed.dispatchEvent(new Event('scroll'))
    expect(ctx.store.feed('bg-1')?.follow).toBe(false)
    ctx.store.applyOutput('bg-1', 'e\n')
    await nextFrame()
    expect(feed.scrollTop).toBe(100)
    expect(btn.classList.contains('opacity-0')).toBe(false)
  })
})
