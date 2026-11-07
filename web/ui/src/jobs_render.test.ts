import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createJobsSection, nearBottom } from './jobs_render'
import { createJobsStore } from './jobs_store'
import type { JobWireItem } from './types'
import type { Block } from './model'

// jsdom fires rAF as a timer; one awaited frame flushes scheduleRender.
const nextFrame = () => new Promise<void>((r) => requestAnimationFrame(() => r()))

// Fake-timer rAF for hydration tests (takeover_hydration.test.ts pattern):
// advanceTimersByTime(16) fires exactly one backfill frame.
function useFakeFrames() {
  vi.useFakeTimers({
    toFake: ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame', 'performance'],
  })
}

function agentEvent(type: string, fields: Record<string, unknown> = {}) {
  return {
    type,
    agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 },
    ...fields,
  }
}

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
  it('releases at exactly the threshold — the boundary gap is not pinned', () => {
    // 100 - 40 - 20 = 40: exactly at the 40px threshold. The chat follow
    // contract treats the boundary as released; a <= would pin it.
    expect(nearBottom(100, 40, 20)).toBe(false)
    expect(nearBottom(100, 55, 20, 25)).toBe(false)
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

  it('failed without an exit code reads "Failed"', () => {
    const ctx = mount()
    ctx.store.setJobs([jw({ id: 'bg-fail0', kind: 'bash', status: 'failed', command: 'x' })])
    ctx.rerender()
    expect(rowFor(ctx.host, 'bg-fail0').querySelector('.mono.text-red')?.textContent).toBe('Failed')
  })

  it('row anatomy: chip state colors, icon tint, name base class, chevron glyph', () => {
    const ctx = mount()
    ctx.store.setJobs([
      bashJob('bg-run', 'running'),
      bashJob('bg-done', 'completed'),
      jw({ id: 'bg-fail', kind: 'bash', status: 'failed', command: 'x', exitCode: 1 }),
    ])
    ctx.rerender()
    // The color IS the state signal for chips and terminal icons — the
    // panel has no other status affordance on these spans. (A bash row's
    // name cell is also .mono, so chips are selected via the state color.)
    expect(rowFor(ctx.host, 'bg-run').querySelector('.mono.text-blue')?.className).toContain('pulse')
    expect(rowFor(ctx.host, 'bg-done').querySelector('.mono.text-green')?.textContent).toBe('Done')
    expect(rowFor(ctx.host, 'bg-fail').querySelector('.mono.text-red')?.textContent).toBe('Exit 1')

    // Row children are fixed-order: [icon, name, chip, chev].
    const doneRow = rowFor(ctx.host, 'bg-done')
    const failRow = rowFor(ctx.host, 'bg-fail')
    const runRow = rowFor(ctx.host, 'bg-run')
    expect(runRow.children[0].className).toContain('flex-shrink-0 w-4 h-4 rounded-full')
    expect(doneRow.children[0].className).toContain('flex-shrink-0 w-4 h-4 rounded-full')
    expect(doneRow.children[0].className).toContain('bg-green/20 text-green')
    expect(failRow.children[0].className).toContain('bg-red/20 text-red')
    // The name cell must keep its flex/truncate layout even for a bash row
    // that appends mono sizing classes on top.
    expect(doneRow.children[1].className).toContain('flex-1 min-w-0 truncate')

    const chev = doneRow.children[3]
    expect(chev.className).toBe('jobs-chev flex-shrink-0 text-t3 transition-transform duration-200')
    expect(chev.textContent).toBe('›')
  })

  it('a bash row without a command and an agent row without an agentType render empty labels', () => {
    const ctx = mount()
    ctx.store.setJobs([
      jw({ id: 'bg-nocmd', kind: 'bash', status: 'running' }),
      jw({ id: 'ag-notype', kind: 'agent', status: 'running', parentToolUseID: 'px' }),
    ])
    ctx.rerender()
    // Absent metadata must read as an empty label, never a placeholder.
    expect(rowFor(ctx.host, 'bg-nocmd').children[1].textContent).toBe('')
    expect(rowFor(ctx.host, 'ag-notype').children[1].textContent).toBe('')
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

  it('the stop button is the solid rounded-square glyph in a right-aligned actions row', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    const stop = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-stop') as HTMLButtonElement
    // The affordance is the glyph itself: a filled square, not text or an
    // outline. Design spec: rect 3,3,18,18 rx5 in a 24x24 viewBox.
    const svg = stop.querySelector('svg')
    expect(svg?.getAttribute('viewBox')).toBe('0 0 24 24')
    const rect = svg?.querySelector('rect')
    expect(rect?.getAttribute('x')).toBe('3')
    expect(rect?.getAttribute('width')).toBe('18')
    expect(rect?.getAttribute('rx')).toBe('5')
    expect(rect?.getAttribute('fill')).toBe('currentColor')
    // The actions row keeps the stop button flush right under the feed.
    expect(stop.parentElement?.className).toBe('row-actions flex gap-2 mt-1.5 items-center justify-end')
  })

  it('a stop click never bubbles to panel-level handlers', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    // The expansion box is a SIBLING of the row, so the row toggle can never
    // see the click; the containment boundary that matters is the panel host
    // (task_panel closes popovers on outside clicks).
    const panel = document.createElement('div')
    panel.appendChild(ctx.host)
    document.body.appendChild(panel)
    const panelClick = vi.fn()
    panel.addEventListener('click', panelClick)

    const stop = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-stop') as HTMLButtonElement
    stop.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(ctx.onStop).toHaveBeenCalledWith('bg-1')
    expect(panelClick).not.toHaveBeenCalled()
  })

  it('terminal rows render no stop button', () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'completed')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    expect(expFor(ctx.host, 'bg-1')).not.toBeNull()
    expect(ctx.host.querySelector('.jobs-stop')).toBeNull()
  })

  it('re-expanding a bash row paints the carried tail (connector resends only changes)', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'completed')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    ctx.store.applyOutput('bg-1', 'carried tail line\n')
    await nextFrame()
    rowFor(ctx.host, 'bg-1').click() // collapse drops the view
    rowFor(ctx.host, 'bg-1').click() // re-expand: tail unchanged server-side, never resent
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    const body = feed.querySelector('.md-body') as HTMLElement
    expect(body.textContent).toContain('carried tail line')
    // The fresh paint arms the string guard: an unchanged-tail resend keeps
    // the node (no phantom rebuild).
    ctx.store.applyOutput('bg-1', 'carried tail line\n')
    await nextFrame()
    expect(feed.querySelector('.md-body')).toBe(body)
  })

  it('first expansion of a completed bash job paints the hydrated tail', async () => {
    const ctx = mount()
    ctx.store.setJobs(
      [bashJob('bg-1', 'completed')],
      new Map([['bg-1', { tail: 'snapshot tail line\n' }]]),
    )
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    expect((feed.querySelector('.md-body') as HTMLElement).textContent).toContain('snapshot tail line')
    // A later unchanged-tail resend must not clobber the painted body.
    ctx.store.applyOutput('bg-1', 'snapshot tail line\n')
    await nextFrame()
    expect(feed.querySelector('.md-body')?.textContent).toContain('snapshot tail line')
  })

  it('bash tail code blocks get wired copy buttons', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    ctx.store.applyOutput('bg-1', '```go\nfmt.Println("hi")\n```\n')
    await nextFrame()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    // The tail body rides the chat Bash-card pipeline, copy affordance
    // included — a long paste must be copyable straight from the feed.
    const header = feed.querySelector('.code-header') as HTMLElement
    expect(header.querySelector('.copy-btn')).not.toBeNull()
  })

  it('a first tail paints verbatim — the fresh-view guard must not match any real tail', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'completed')])
    ctx.rerender()
    // Guard armed while the feed already carries output: whatever string the
    // connector has, the first paint must happen.
    ctx.store.applyOutput('bg-1', 'Stryker was here!')
    rowFor(ctx.host, 'bg-1').click()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feed.querySelector('.md-body')?.textContent).toContain('Stryker was here!')
  })

  it('a view opened before any output still renders the first tail', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    // Expansion with no feed yet: the string guard seeds empty, so the very
    // first output — whatever it says — is by definition unpainted.
    rowFor(ctx.host, 'bg-1').click()
    ctx.store.applyOutput('bg-1', 'Stryker was here!')
    await nextFrame()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feed.querySelector('.md-body')?.textContent).toContain('Stryker was here!')
  })

  it('re-expanding a bash view whose tail is empty paints no body', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'completed')])
    ctx.rerender()
    ctx.store.applyOutput('bg-1', 'x\n')
    rowFor(ctx.host, 'bg-1').click()
    await nextFrame()
    rowFor(ctx.host, 'bg-1').click() // collapse drops the view
    // Tail reset to empty (e.g. a cleared region): the fresh view must stay
    // unpainted — an empty body is not a render.
    ctx.store.applyOutput('bg-1', '')
    rowFor(ctx.host, 'bg-1').click() // re-expand: feed exists, tail === ''
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feed.querySelectorAll('.md-body')).toHaveLength(0)
  })

  it('a tail queued in the same tick as a status-flip rebuild still paints after it', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    ctx.store.applyOutput('bg-1', 'first\n')
    await nextFrame()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feed.textContent).toContain('first')

    // Same tick: a new tail delta is queued (rAF not fired yet) when the
    // running→completed signature change rebuilds the rows — exactly what a
    // terminal batch (final output + job_list) delivers. The rebuild's dirty
    // sweep must spare live ids: flush's !rec continue sits ahead of the
    // string-guarded tail path, and the rebuild catch-up skips sink-less
    // (bash) views, so a live bash view whose dirty record was purged has no
    // path left to paint
    // the queued tail — and a terminal job's connector never resends it.
    ctx.store.applyOutput('bg-1', 'second-final\n')
    ctx.store.setJobs([bashJob('bg-1', 'completed')])
    ctx.rerender()
    await nextFrame()
    expect(feed.textContent).toContain('second-final')
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

  it('tool_param_delta patches the running card summary', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 't-1', name: 'Grep', input: {} } }))
    await nextFrame()
    const card = expFor(ctx.host, 'agent-1')?.querySelector('[data-tool-root]') as HTMLElement
    expect(card.querySelector('span.font-light')?.textContent).toBe('')
    ctx.store.applyEvent(
      agentEvent('tool_param_delta', { partial_input: { id: 't-1', name: 'Grep', delta: '', summary: 'main.go#40' } }),
    )
    await nextFrame()
    // The streamed summary must reach the card while the tool still runs —
    // waiting for tool_end would hide progress for minutes-long tools.
    expect(card.querySelector('span.font-light')?.textContent).toBe(' (main.go#40)')
  })

  it('tool_output_delta streams output into the opened card body', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 't-1', name: 'Bash', input: {} } }))
    await nextFrame()
    const card = expFor(ctx.host, 'agent-1')?.querySelector('[data-tool-root]') as HTMLElement
    // Folded bodies are render-on-visible: open it so the stream is
    // observable in the DOM.
    ;(card.querySelector('[data-tool-header]') as HTMLElement).click()
    ctx.store.applyEvent(
      agentEvent('tool_output_delta', { tool_result: { tool_use_id: 't-1', output: '', display_output: 'stream-out line' } }),
    )
    await nextFrame()
    expect(card.querySelector('.md-body')?.textContent).toContain('stream-out line')
  })

  it('tool_end finishes the card: FAIL duration, stopped heartbeat', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 't-1', name: 'Bash', input: {} } }))
    await nextFrame()
    const card = expFor(ctx.host, 'agent-1')?.querySelector('[data-tool-root]') as HTMLElement
    expect(card.querySelector('.heartbeat')).not.toBeNull()
    ctx.store.applyEvent(
      agentEvent('tool_end', { tool_result: { tool_use_id: 't-1', output: '', display_output: 'boom', is_error: true } }),
    )
    await nextFrame()
    // The live card must converge to the finished anatomy the snapshot path
    // renders; a card stuck in running state is a stale feed.
    expect(card.querySelector('.heartbeat')).toBeNull()
    const durEl = card.querySelector('.font-mono.text-xs') as HTMLElement
    expect(durEl.textContent).toContain('FAIL')
    expect(durEl.className).toContain('text-red')
  })

  it('expanded agent feed: running tool row ticks', async () => {
    // useFakeFrames superset: the sink's live ticker needs a fake interval and
    // the duration math reads Date.now().
    vi.useFakeTimers({
      toFake: [
        'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval',
        'requestAnimationFrame', 'cancelAnimationFrame', 'performance', 'Date',
      ],
    })
    try {
      const ctx = mount()
      ctx.store.setJobs(
        [agentJob('agent-1', 'running')],
        new Map([
          ['agent-1', { blocks: [toolBlock('t1', 'Bash', { state: 'running', startedAt: Date.now() })] }],
        ]),
      )
      ctx.rerender()
      rowFor(ctx.host, 'agent-1').click()
      // One frame lets the hydrate onDone fire.
      await vi.advanceTimersByTimeAsync(16)
      const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
      const row = feed.querySelector('[data-tool-root]') as HTMLElement
      expect(row.textContent).toContain('0s')

      vi.advanceTimersByTime(1200)
      expect(row.textContent).toContain('1s')
      expect(row.textContent).not.toContain('0s')
    } finally {
      vi.useRealTimers()
    }
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
    // The envelope lives in the folded row body — rendered on expand
    // (render-on-visible), never as a bare gray echo span.
    ;(rows[0].querySelector('[data-tool-header]') as HTMLElement).click()
    expect(rows[0].querySelector('.md-body')?.textContent).toContain('</job-notification>')
  })

  it('renders bare notification envelopes as Reminder rows with display-text bodies', async () => {
    const ctx = mount()
    const bare =
      '<job-notification>\n<job-id>bg-1</job-id>\n<status>completed</status>\n' +
      '<summary>Background command &quot;make check&quot; completed (exit code 0)</summary>\n</job-notification>'
    const blocks: Block[] = [{ kind: 'user', id: '', text: bare, startedAt: 1760358185000 }]
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    const rows = Array.from(feed.querySelectorAll('[data-reminder-row]'))
    expect(rows).toHaveLength(1)
    expect(rows[0].dataset.toolName).toBe('Reminder')
    const summary = rows[0].querySelector('span.font-light')
    expect(summary?.textContent).toBe(' (command make check · exit 0)')
    // Display text in the folded body — rendered on expand (render-on-visible)
    // — never the raw XML, never a gray echo of the envelope.
    ;(rows[0].querySelector('[data-tool-header]') as HTMLElement).click()
    const bodyText = rows[0].querySelector('.md-body')?.textContent ?? ''
    expect(bodyText).toContain('Background command "make check" completed (exit code 0)')
    expect(bodyText).not.toContain('<job-notification>')
    const echo = Array.from(feed.querySelectorAll('span')).filter(
      (s) => s.className.includes('text-t3') && s.textContent?.includes('<job-notification>'),
    )
    expect(echo).toHaveLength(0)
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

  it('the wrap is the positioning context and the button hugs the feed', async () => {
    const { feed } = await expandedBashFeed()
    const wrap = feed.parentElement as HTMLElement
    // The overlay button is a sibling of the scroll container inside a
    // relative wrap — that geometry is what keeps it on screen.
    expect(wrap.className).toBe('relative mt-1.5')
    const btn = wrap.querySelector('button') as HTMLButtonElement
    // Inline bottom beats the recipe's bottom-24: the button must hug a
    // 180px feed, not the viewport chrome.
    expect(btn.style.bottom).toBe('4px')
  })
})

// ── Reset / truncation / cross-kind writes ─────────────────────────────────

describe('jobs section — reset and cross-kind writes', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('a setJobs hydration reset re-renders the expanded feed from the new snapshot', async () => {
    const ctx = mount()
    ctx.store.setJobs(
      [agentJob('agent-1', 'running')],
      new Map([['agent-1', { blocks: [toolBlock('t-old', 'Read')] }]]),
    )
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feed.querySelector('[data-tool-root]')?.dataset.toolName).toBe('Read')

    // Reconnect-style hydration replaces the whole snapshot: the live view
    // must follow or it keeps rendering blocks the store no longer has.
    ctx.store.setJobs(
      [agentJob('agent-1', 'running')],
      new Map([['agent-1', { blocks: [toolBlock('t-new', 'Grep')] }]]),
    )
    await nextFrame()
    const cards = feed.querySelectorAll('[data-tool-root]')
    expect(cards).toHaveLength(1)
    expect(cards[0].dataset.toolName).toBe('Grep')
  })

  it('a consumed reset leaves no residue: later quiet flushes keep the same nodes', async () => {
    const ctx = mount()
    const blocks = makeFeedBlocks(3, 'r')
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    await nextFrame()
    ctx.store.setJobs(
      [agentJob('agent-1', 'running')],
      new Map([['agent-1', { blocks: makeFeedBlocks(3, 's') }]]),
    )
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    const node = feed.children[0]
    // A notify-only setJobs (no hydration) must not re-render anything: the
    // reset was consumed. A surviving reset flag would rehydrate forever.
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    await nextFrame()
    expect(feed.children[0]).toBe(node)
  })

  it('truncation arriving mid-hydration rehydrates: the old tail node is replaced', () => {
    useFakeFrames()
    const ctx = mount()
    const blocks = makeFeedBlocks(420)
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    vi.advanceTimersByTime(16 * 2) // backfill in flight, still pending
    const oldTail = feed.lastElementChild as HTMLElement // blk-419's div
    expect(oldTail.textContent).toContain('blk-419')

    // One event past the 400 cap: the reset branch must swap the whole feed
    // (the pending chunks hold pre-truncation blocks), not defer them.
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 't-401', name: 'Bash', input: {} } }))
    vi.advanceTimersByTime(16)

    const after = Array.from(feed.querySelectorAll('.md-body')).find((el) =>
      (el.textContent ?? '').includes('blk-419'),
    )
    // Wholesale rehydrate: fresh nodes even for content that was already
    // on screen.
    expect(after).not.toBe(oldTail)
    vi.useRealTimers()
  })

  it('a bash-tail write on an agent view never touches the block feed', async () => {
    const ctx = mount()
    ctx.store.setJobs(
      [agentJob('agent-1', 'running')],
      new Map([['agent-1', { blocks: [toolBlock('t-1', 'Read')] }]]),
    )
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    const card = feed.querySelector('[data-tool-root]') as HTMLElement
    // The tail path and the reset path are bash-only contracts; a stray
    // tail write on a sink view must be a no-op (no wipe, no rehydrate
    // churn — node identity pins both).
    ctx.store.applyOutput('agent-1', 'stray tail\n')
    await nextFrame()
    expect(feed.querySelector('[data-tool-root]')).toBe(card)
    expect(feed.textContent).not.toContain('stray tail')
  })

  it('a fresh agent view with a stray tail hydrates blocks, never a tail body', async () => {
    const ctx = mount()
    ctx.store.setJobs(
      [agentJob('agent-1', 'running')],
      new Map([['agent-1', { blocks: [toolBlock('t-1', 'Grep')] }]]),
    )
    ctx.store.applyOutput('agent-1', 'stray\n')
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    // Creation paints the sink snapshot; the tail paint is bash-only.
    expect(feed.querySelector('[data-tool-root]')?.dataset.toolName).toBe('Grep')
    expect(feed.textContent).not.toContain('stray')
  })
})

// ── DOM sink behavior (incremental rendering) ─────────────────────────────

// Distinct marker per block; each text block renders exactly one .md-body.
function makeFeedBlocks(n: number, prefix = 'blk'): Block[] {
  return Array.from({ length: n }, (_, i) => ({
    kind: 'text' as const,
    id: '',
    text: `${prefix}-${i} lorem ipsum dolor sit amet consectetur adipiscing elit **md**`,
  }))
}

function expectedMdBodies(blocks: Block[]): number {
  return blocks.length
}

describe('jobs section — incremental DOM sink', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('marker survives 100 coalesced text deltas (no full rebuild)', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 't-1', name: 'Read', input: {} } }))
    await nextFrame()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    const card = feed.querySelector('[data-tool-root]') as HTMLElement
    card.dataset.marker = 'keep'
    const cardNode = card

    for (let i = 0; i < 100; i++) {
      ctx.store.applyEvent(agentEvent('text_delta', { text: `d${i} ` }))
    }
    await nextFrame()

    const after = feed.querySelector('[data-tool-root]') as HTMLElement
    expect(after).toBe(cardNode)
    expect(after.dataset.marker).toBe('keep')
    expect(feed.querySelectorAll('[data-tool-root]')).toHaveLength(1)
    expect(feed.textContent).toContain('d99')
  })

  it('sibling isolation: flooding job B leaves expanded job A untouched', async () => {
    const ctx = mount()
    ctx.store.setJobs([
      agentJob('agent-A', 'running'),
      jw({ id: 'agent-B', kind: 'agent', status: 'running', agentType: 'Executor', description: 'b', parentToolUseID: 'pB' }),
    ])
    ctx.rerender()
    rowFor(ctx.host, 'agent-A').click()
    const feedA = expFor(ctx.host, 'agent-A')?.querySelector('.jobs-feed') as HTMLElement
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 'ta-1', name: 'Read', input: {} } }))
    await nextFrame()
    const card = feedA.querySelector('[data-tool-root]') as HTMLElement
    card.dataset.marker = 'A'
    const countBefore = feedA.children.length

    for (let i = 0; i < 50; i++) {
      ctx.store.applyEvent({
        type: 'tool_start',
        tool_use: { id: `tb-${i}`, name: 'Bash', input: {} },
        agent: { parent_tool_use_id: 'pB', agent_type: 'Executor', depth: 1 },
      })
    }
    await nextFrame()

    expect(feedA.children.length).toBe(countBefore)
    const cardAfter = feedA.querySelector('[data-tool-root]') as HTMLElement
    expect(cardAfter).toBe(card)
    expect(cardAfter.dataset.marker).toBe('A')
  })

  it('expand of a 420-block feed shows the tail synchronously and backfills across frames', () => {
    useFakeFrames()
    const ctx = mount()
    const blocks = makeFeedBlocks(420)
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement

    // Tail-first: newest visible now, oldest absent, partial DOM.
    expect(feed.textContent).toContain('blk-419')
    expect(feed.textContent).not.toContain('blk-0')
    const syncCount = feed.querySelectorAll('.md-body').length
    expect(syncCount).toBeGreaterThan(0)
    expect(syncCount).toBeLessThan(expectedMdBodies(blocks))

    vi.advanceTimersByTime(16 * 60)

    expect(feed.querySelectorAll('.md-body')).toHaveLength(expectedMdBodies(blocks))
    expect(feed.textContent).toContain('blk-0')
    const text = feed.textContent ?? ''
    expect(text.indexOf('blk-0')).toBeLessThan(text.indexOf('blk-419'))
    vi.useRealTimers()
  })

  it('C1: deltas streamed mid-hydration reconcile exactly once with in-place seam patch', () => {
    useFakeFrames()
    const ctx = mount()
    const blocks = makeFeedBlocks(60)
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    const tailDiv = feed.lastElementChild as HTMLElement // blk-59's div (8-tail sync)
    expect(tailDiv.textContent).toContain('blk-59')

    vi.advanceTimersByTime(16) // one backfill chunk landed; hydration pending

    // Deltas while pending: two finalize deltas onto the seam block plus a
    // new suffix (tool + text) — all coalesced into the same flush frame,
    // so the seam mutates after its last render.
    ctx.store.applyEvent(agentEvent('text_delta', { text: ' PLUS-ONE' }))
    ctx.store.applyEvent(agentEvent('text_delta', { text: ' PLUS-FINAL' }))
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 't-live', name: 'Bash', input: {} } }))
    ctx.store.applyEvent(agentEvent('text_delta', { text: 'after-tool' }))

    vi.advanceTimersByTime(16 * 40)

    // Exactly once: 60 fixture + the tool card's (hidden) output body + the
    // after-tool text block.
    expect(feed.querySelectorAll('.md-body')).toHaveLength(62)
    // Seam patched in place: the tail div is the SAME node, holding every
    // delta streamed onto it (content pin on the unconditional patch).
    const seam = Array.from(feed.querySelectorAll('.md-body')).find(
      (el) => (el.textContent ?? '').includes('blk-59'),
    )
    expect(seam).toBe(tailDiv)
    expect(seam!.textContent).toContain('PLUS-ONE')
    // Both finalize deltas coalesced before the tool_start landed on the
    // seam. (**md** renders as <strong>md</strong>, so textContent has 'md'.)
    expect(seam!.textContent).toContain('md PLUS-ONE PLUS-FINAL')
    // Order intact: fixture head still above the tail.
    const text = feed.textContent ?? ''
    expect(text.indexOf('blk-0')).toBeGreaterThanOrEqual(0)
    expect(text.indexOf('blk-0')).toBeLessThan(text.indexOf('blk-59'))
    expect(text).toContain('after-tool')
    vi.useRealTimers()
  })

  it('C2: truncation arriving mid-hydration rehydrates from the capped list, never NotFoundError', () => {
    useFakeFrames()
    const ctx = mount()
    const blocks = makeFeedBlocks(420)
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    vi.advanceTimersByTime(16) // backfill pending

    // One event pushes the store past the 400 cap: the array is replaced and
    // 21 head blocks are dropped.
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 't-401', name: 'Bash', input: {} } }))

    vi.advanceTimersByTime(16 * 60)

    expect(feed.querySelectorAll('.md-body')).toHaveLength(400)
    const text = feed.textContent ?? ''
    expect(text).toContain('blk-21') // post-truncation head
    expect(text).not.toContain('blk-0')
    expect(text).toContain('blk-419')
    expect(text).toContain('Bash')
    vi.useRealTimers()
  })

  it('collapse mid-hydration disposes the pending rAF chain: nothing appends later', () => {
    useFakeFrames()
    const ctx = mount()
    const blocks = makeFeedBlocks(420)
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    vi.advanceTimersByTime(16)
    const midCount = feed.querySelectorAll('.md-body').length

    rowFor(ctx.host, 'agent-1').click() // collapse
    vi.advanceTimersByTime(16 * 60)

    expect(feed.querySelectorAll('.md-body').length).toBe(midCount)
    expect(expFor(ctx.host, 'agent-1')).toBeNull()
    vi.useRealTimers()
  })

  it('quiescent truncation drops the rendered head node (dropLeading)', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    for (let i = 0; i < 5; i++) {
      ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: `t-${String(i).padStart(3, '0')}`, name: 'Bash', input: {} } }))
    }
    await nextFrame()
    const headNode = feed.children[0]

    for (let i = 5; i < 401; i++) {
      ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: `t-${String(i).padStart(3, '0')}`, name: 'Bash', input: {} } }))
    }
    await nextFrame()

    expect(feed.children.length).toBe(400)
    expect(headNode.isConnected).toBe(false)
    expect(feed.children[0]).not.toBe(headNode)
  })

  it('bash tails skip unchanged strings (node identity) and re-render on change', async () => {
    const ctx = mount()
    ctx.store.setJobs([bashJob('bg-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'bg-1').click()
    const feed = expFor(ctx.host, 'bg-1')?.querySelector('.jobs-feed') as HTMLElement

    ctx.store.applyOutput('bg-1', 'line-one\n')
    await nextFrame()
    const body = feed.children[0] as HTMLElement
    expect(body.textContent).toContain('line-one')

    ctx.store.applyOutput('bg-1', 'line-one\n') // same string
    await nextFrame()
    expect(feed.children[0]).toBe(body)

    ctx.store.applyOutput('bg-1', 'line-one\nline-two\n')
    await nextFrame()
    const after = feed.children[0] as HTMLElement
    expect(after).not.toBe(body)
    expect(after.textContent).toContain('line-two')
  })

  it('quiet-feed rebuild stability: an unrelated status flip preserves a current view', async () => {
    const ctx = mount()
    const blocks = makeFeedBlocks(3, 'q')
    ctx.store.setJobs(
      [agentJob('agent-1', 'completed'), bashJob('bg-9', 'running')],
      new Map([['agent-1', { blocks }]]),
    )
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    const childrenBefore = Array.from(feed.children)
    expect(childrenBefore.length).toBe(3)

    // Unrelated job status flip forces a full row rebuild (jobsSig change).
    ctx.store.setJobs([agentJob('agent-1', 'completed'), bashJob('bg-9', 'completed')])
    ctx.rerender()

    const feedAfter = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feedAfter).toBe(feed)
    // Node identity, not structural equality: an uninitialized lastVersion
    // forces dispose+rehydrate on every rebuild — permanent churn.
    expect(feedAfter.children.length).toBe(childrenBefore.length)
    for (let i = 0; i < childrenBefore.length; i++) {
      expect(feedAfter.children[i]).toBe(childrenBefore[i])
    }
  })

  it('stale catch-up: deltas while the popover is closed render on reopen', async () => {
    const ctx = mount()
    ctx.store.setJobs([agentJob('agent-1', 'running')])
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    await nextFrame()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(feed.children.length).toBe(0)

    // Popover closed: the section host detaches; flushes skip the feed.
    ctx.host.remove()
    ctx.store.applyEvent(agentEvent('tool_start', { tool_use: { id: 't-late', name: 'Read', input: {} } }))
    ctx.store.applyEvent(agentEvent('text_delta', { text: 'streamed while closed' }))
    await nextFrame()
    expect(feed.children.length).toBe(0)

    // Reopen: rebuild catches the version gap and rehydrates.
    document.body.appendChild(ctx.host)
    ctx.rerender()
    await nextFrame()
    const reopened = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    expect(reopened.textContent).toContain('streamed while closed')
    expect(reopened.querySelectorAll('[data-tool-root]')).toHaveLength(1)
  })

  it('backfill after the job left the store never pins a dead feed', () => {
    useFakeFrames()
    const ctx = mount()
    const blocks = makeFeedBlocks(420)
    ctx.store.setJobs([agentJob('agent-1', 'running')], new Map([['agent-1', { blocks }]]))
    ctx.rerender()
    rowFor(ctx.host, 'agent-1').click()
    const feed = expFor(ctx.host, 'agent-1')?.querySelector('.jobs-feed') as HTMLElement
    Object.defineProperty(feed, 'scrollHeight', { value: 500 })
    vi.advanceTimersByTime(16 * 2) // chunks land; live feed pins to 500

    // Job removed with no rebuild yet: the view stays mounted while the
    // remaining backfill chunks still insert. follow() now reads a missing
    // feed — the safe default must be "not following" (no yank to bottom).
    ctx.store.setJobs([])
    feed.scrollTop = 0
    vi.advanceTimersByTime(16 * 60)
    expect(feed.scrollTop).toBe(0)
    vi.useRealTimers()
  })
})
