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

  it('shows the full command block and streams tail lines into the feed', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    const row = rowFor(ctx.host, 'bg-1')
    row.click()
    // The toggle rebuilds the section: re-query, the clicked node is stale.
    expect(rowFor(ctx.host, 'bg-1').classList.contains('exp')).toBe(true)
    expect(rowFor(ctx.host, 'bg-1').querySelector('.jobs-chev')?.classList.contains('rotate-90')).toBe(true)
    const cmd = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-cmd') as HTMLElement
    expect(cmd.textContent).toBe('$ make check')

    ctx.store.applyOutput('bg-1', 'go vet ./...\nok  pkg/engine  1.2s\n')
    await nextFrame()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    const lines = Array.from(feed.querySelectorAll('.f-line')).map((l) => l.textContent)
    expect(lines).toEqual(['go vet ./...', 'ok  pkg/engine  1.2s'])
    // Following by default: no jump-to-bottom pill shown.
    expect(feed.classList.contains('up')).toBe(false)
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

  it('renders tool cards (classifyTool noun + summary + mark), thinking, and markdown text in order', () => {
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

    const tools = Array.from(feed.querySelectorAll('.f-tool'))
    expect(tools).toHaveLength(2)
    // classifyTool language: Grep renders as its chat noun "Search".
    expect(tools[0].querySelector('.tn')?.textContent).toBe('Search')
    expect(tools[0].querySelector('.tn + span')?.textContent).toBe('Grep summary')
    expect(tools[0].querySelector('.f-mark.ok')?.textContent).toBe('✓')
    expect(tools[1].querySelector('.f-mark.err')?.textContent).toBe('✗')

    const think = feed.querySelectorAll('.f-think')
    expect(think).toHaveLength(1)
    expect(think[0].textContent).toBe('先复现')

    const texts = Array.from(feed.querySelectorAll('.f-text'))
    expect(texts).toHaveLength(2)
    expect(texts[0].innerHTML).toContain('<strong>1335</strong>')
    // Nested sub-agent output flattens after its parent card (markdown-it
    // leaves a trailing newline inside <p>).
    expect(texts[1].textContent?.trim()).toBe('nested report')
  })

  it('live events grow the expanded feed via the rAF-coalesced render', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feed.querySelectorAll('.f-tool')).toHaveLength(0)

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
    expect(feed.querySelectorAll('.f-tool')).toHaveLength(1)
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
    // User blocks render as bare feed lines (f-line), not markdown f-text.
    const lines = Array.from(feed.querySelectorAll('.f-line')).map((l) => l.textContent)
    expect(lines).toEqual(['继续跑压测并记录 p99'])
    expect(feed.querySelectorAll('.f-text')).toHaveLength(0)
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

  it('rebuild carries the expanded feed element over instead of recreating it', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    const feedBefore = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement

    // Status flip forces a full row rebuild (task_panel signature change).
    ctx.store.setJobs([bashJob('bg-1', 'completed')])
    ctx.rerender()

    const rowAfter = rowFor(ctx.host, 'bg-1')
    expect(rowAfter.classList.contains('exp')).toBe(true)
    const feedAfter = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    // Same node re-attached: scrollTop and listeners survive, so a rebuild
    // can neither collapse the row nor jump its scroll.
    expect(feedAfter).toBe(feedBefore)
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
    expect(second.classList.contains('up')).toBe(false)
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

describe('jobs section — follow pill', () => {
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

  it('scrolling up releases follow and shows the pill; the pill re-pins', async () => {
    const { ctx, feed } = await expandedBashFeed()
    feed.scrollTop = 100 // 500 - 100 - 200 = 200 > 40
    feed.dispatchEvent(new Event('scroll'))
    expect(ctx.store.feed('bg-1')?.follow).toBe(false)
    expect(feed.classList.contains('up')).toBe(true)
    const pill = feed.querySelector('.f-pin button') as HTMLButtonElement
    expect(pill.textContent).toBe('↓ Back to bottom')

    pill.click()
    expect(ctx.store.feed('bg-1')?.follow).toBe(true)
    expect(feed.classList.contains('up')).toBe(false)
    expect(feed.scrollTop).toBe(500)
  })

  it('scrolling near the bottom keeps follow and never shows the pill', async () => {
    const { ctx, feed } = await expandedBashFeed()
    feed.scrollTop = 269 // 500 - 269 - 200 = 31 < 40
    feed.dispatchEvent(new Event('scroll'))
    expect(ctx.store.feed('bg-1')?.follow).toBe(true)
    expect(feed.classList.contains('up')).toBe(false)
  })

  it('re-render pins scrollTop to the stubbed bottom while following and holds it when scrolled up', async () => {
    const { ctx, feed } = await expandedBashFeed()
    // Following: new output scrolls the feed to the bottom.
    ctx.store.applyOutput('bg-1', 'd\n')
    await nextFrame()
    expect(feed.scrollTop).toBe(500)

    // Scrolled up (500 - 100 - 200 = 200 > 40): a re-render must not yank
    // the viewport back to the bottom.
    feed.scrollTop = 100
    feed.dispatchEvent(new Event('scroll'))
    expect(ctx.store.feed('bg-1')?.follow).toBe(false)
    ctx.store.applyOutput('bg-1', 'e\n')
    await nextFrame()
    expect(feed.scrollTop).toBe(100)
    expect(feed.classList.contains('up')).toBe(true)
  })
})
