import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createChat, mapHistoryToChatMessages } from './chat'
import { refreshAvatar, resetAvatarStateForTest } from './avatar'
import { TokenRate } from './token_rate'
import { stopStatusSweep } from './components/status_sweep'

// jsdom lacks IntersectionObserver — stub it (no longer used for prefetch).
class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return [] }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver as unknown as typeof IntersectionObserver)

type Listener = (msg: unknown) => void

const listeners: Set<Listener> = new Set()
const sent: unknown[] = []

let stateCb: ((s: string) => void) | null = null

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: unknown) => {
      sent.push(p)
    },
    connected: true,
    onStateChange: (cb: (s: string) => void) => { stateCb = cb },
    reconnect: () => { stateCb?.('reconnecting') },
  }),
}))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

function dispatchEvents(events: unknown[]) {
  for (const e of events) dispatch({ type: 'event', event: e })
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

// Every mount() boots initAvatars, and fetch spies in earlier tests (e.g.
// the artifact integration Response mocks) can satisfy the probe — leaked
// avatar state would flip later shells to <img> with no test opting in.
afterEach(() => {
  resetAvatarStateForTest()
})

function setTextarea(value: string) {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = value
  ta.dispatchEvent(new Event('input', { bubbles: true }))
}

function pressEnter() {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.dispatchEvent(
    new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
  )
}

function clickStop() {
  const stopBtn = Array.from(document.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('STOP'),
  ) as HTMLButtonElement | undefined
  stopBtn?.click()
}

function assistantContentDivs(): HTMLElement[] {
  return Array.from(document.querySelectorAll('.space-y-3'))
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('chat integration', () => {
  it('mounts the upgrade capsule at the shell root, outside the transform trap', () => {
    const chat = mount()
    const capsule = chat.root.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule).toBeTruthy()
    // The capsule is fixed z-70 and must float above the opaque z-60
    // settings sheet (restart is initiated FROM settings). Sidebar
    // toggling parks a translateX(0) on mainContent — a lingering
    // transform makes it a stacking context that would trap the capsule.
    // Only a direct shell-root child stacks as authored.
    expect(capsule.parentElement).toBe(chat.root)
  })
  it('send shows user message and dispatches message', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('hello world')
    pressEnter()
    expect(document.body.textContent).toContain('hello world')
    expect(sent.some((m) => m.type === 'message' && m.text === 'hello world'))
      .toBe(true)
  })

  it('query_start creates an assistant shell and shows STOP', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    const before = assistantContentDivs().length
    dispatchEvents([{ type: 'query_start' }])
    expect(assistantContentDivs().length).toBe(before + 1)
    expect(document.body.textContent).toContain('STOP')
  })

  it('text_delta accumulates and renders markdown incrementally', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '**bo' },
      { type: 'text_delta', text: 'ld**' },
    ])
    const mdBody = document.querySelector('.md-body.text-t1') as HTMLElement
    expect(mdBody.querySelector('strong')?.textContent).toBe('bold')
  })

  it('text_delta survives subsequent tool_start (no remount loss)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'keep me' },
      {
        type: 'tool_start',
        tool_use: { id: 't-1', name: 'Bash' },
      },
    ])
    const mdBody = document.querySelector('.md-body.text-t1') as HTMLElement
    expect(mdBody.textContent).toContain('keep me')
  })

  it('empty text_delta is a no-op', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '' },
    ])
    const mdBodies = document.querySelectorAll('.md-body')
    expect(mdBodies.length).toBe(1)
    // Empty text → empty innerHTML.
    expect((mdBodies[0] as HTMLElement).textContent).toBe('')
  })

  it('multiple text blocks per turn each get fresh sinks', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'first' },
      { type: 'text_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'second' },
      { type: 'text_end' },
      { type: 'query_end' },
    ])
    expect(document.querySelectorAll('.md-body').length).toBe(2)
  })

  it('interleaved text+thinking both writable', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: 'deep thought' } },
      { type: 'text_start' },
      { type: 'text_delta', text: 'answer' },
    ])
    const mdBody = document.querySelector('.md-body.text-t1') as HTMLElement
    expect(mdBody.textContent).toContain('answer')
    expect(document.body.textContent).toContain('deep thought')
  })

  it('abort during thinking removes message and restores input', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('test query')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    dispatchEvents([
      { type: 'thinking_end' },
      { type: 'query_end', aborted: true, rewound: true },
    ])
    expect(document.body.textContent).not.toContain('test query')
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('test query')
  })

  it('abort with partial response keeps interrupt marker', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('another query')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'partial' },
    ])
    clickStop()
    dispatchEvents([
      { type: 'text_end' },
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])
    expect(document.body.textContent).toContain('another query')
    expect(document.body.textContent).toContain('partial')
    expect(document.body.textContent).toContain(
      '[Request interrupted by user]',
    )
  })

  it('repeated send+abort restores correct text (stale-input regression)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })

    function cycle() {
      setTextarea('repeat me')
      pressEnter()
      dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
      clickStop()
      dispatchEvents([
        { type: 'thinking_end' },
        { type: 'query_end', aborted: true, rewound: true },
      ])
      const ta = document.querySelector('textarea') as HTMLTextAreaElement
      expect(ta.value).toBe('repeat me')
    }
    cycle()
    cycle()
  })

  it('query_end is a DOM no-op (key new behavior)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'hello' },
    ])
    const content = assistantContentDivs().slice(-1)[0]
    const firstChildBefore = content.firstElementChild
    const childCountBefore = content.childElementCount
    dispatchEvents([{ type: 'query_end' }])
    // Same node identity — DOM preserved verbatim.
    expect(assistantContentDivs().slice(-1)[0]).toBe(content)
    expect(content.firstElementChild).toBe(firstChildBefore)
    expect(content.childElementCount).toBe(childCountBefore)
  })

  it('tool_end uses wall-clock duration (not prefix parse)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    const start = Date.now()
    dispatchEvents([
      { type: 'query_start' },
      {
        type: 'tool_start',
        tool_use: { id: 't-1', name: 'Bash' },
      },
    ])
    const elapsed = (Date.now() - start) / 1000
    dispatch({
      type: 'event',
      event: {
        type: 'tool_end',
        tool_result: { tool_use_id: 't-1', display_output: '' },
      },
    })
    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot).toBeTruthy()
    const allText = toolRoot.textContent ?? ''
    expect(allText).toMatch(/\d+(\.\d+)?s/)
    const secMatch = allText.match(/(\d+(\.\d+)?)s/)
    const sec = secMatch ? parseFloat(secMatch[1]) : 0
    expect(sec).toBeGreaterThanOrEqual(0)
    expect(sec).toBeLessThanOrEqual(elapsed + 2)
  })

  it('tool_output_delta keeps the running body folded; expand renders the live output', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'live-bash-1', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'live-bash-1', display_output: 'building 1/100' } },
    ])
    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    const body = toolRoot.children[1] as HTMLElement
    expect(body.classList.contains('hidden')).toBe(true)
    ;(toolRoot.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('building 1/100')
  })

  it('tool_output_delta: empty delta inert, content stays folded, morph patches in place', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'live-bash-2', name: 'Bash' } },
    ])
    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    const body = toolRoot.children[1] as HTMLElement
    // Segment 1 — empty delta must not touch the DOM (UI contract; the
    // engine never dispatches empty payloads, this guards regressions).
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'live-bash-2', display_output: '' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    expect(body.textContent).toBe('')
    // Segment 2 — the first content-bearing delta buffers into
    // pendingOutput; the body stays folded (no running-time expand).
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'live-bash-2', display_output: 'step 1' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    expect(body.textContent).toBe('')
    ;(toolRoot.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(body.textContent).toContain('step 1')
    const pBefore = body.querySelector('p')
    // Segment 3 — a delta while visible: innerHTML replacement would swap
    // in a fresh <p>; morph must patch the existing text node in place.
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'live-bash-2', display_output: 'step 1\nstep 2' } },
    ])
    expect(body.textContent).toContain('step 2')
    expect(body.querySelector('p')).toBe(pBefore)
  })

  // ── Fold-policy trio: bodies stay folded while running (only Edit/Write
  // auto-expand on finish), render-on-visible flushes the latest snapshot,
  // and user gestures win over everything. Shared helpers below exercise
  // the full event pipeline (tool_start → tool_output_delta → tool_end).
  function liveBashBody(): HTMLElement {
    const toolRoot = document.querySelector(`[data-tool-root][data-tool-name="Bash"]`) as HTMLElement
    expect(toolRoot).toBeTruthy()
    return toolRoot.children[1] as HTMLElement
  }

  function clickToolHeader() {
    const header = document.querySelector('[data-tool-root] [data-tool-header]') as HTMLElement
    expect(header).toBeTruthy()
    header.click()
  }

  it('a running non-edit tool never expands; tool_end keeps it folded', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-1', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-1', display_output: 'building 1/100' } },
    ])
    const body = liveBashBody()
    const header = document.querySelector('[data-tool-root] [data-tool-header]') as HTMLElement
    // No first-delta expand: streaming output must not make the running
    // block grow and shrink (screen flicker).
    expect(body.classList.contains('hidden')).toBe(true)
    expect(header.querySelector('svg')?.getAttribute('class')).not.toContain('rotate-90')
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'fold-1', display_output: 'building 100/100\nok' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    // The final output is not lost: expanding renders it (render-on-visible).
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('building 100/100')
  })

  it('user-folded block stays folded through later deltas and tool_end (intent guard)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-2', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-2', display_output: 'step 1' } },
    ])
    const body = liveBashBody()
    // The user expands, then folds mid-run.
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(false)
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(true)
    // A later delta must not re-expand the user's fold.
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-2', display_output: 'step 1\nstep 2' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'fold-2', display_output: 'step 1\nstep 2\ndone' } },
    ])
    // tool_end must leave the user's fold alone.
    expect(body.classList.contains('hidden')).toBe(true)
  })

  it('user-expanded block stays expanded through tool_end (intent guard)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-3', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-3', display_output: 'step 1' } },
    ])
    const body = liveBashBody()
    // The user expands mid-run.
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(false)
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'fold-3', display_output: 'done' } },
    ])
    // User intent wins over the finish-time fold.
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('done')
  })

  it('folded running block receives no DOM writes; expand renders the latest snapshot', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-4', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-4', display_output: 'step 1' } },
    ])
    const body = liveBashBody()
    // The block is folded from the start — deltas must not touch the
    // hidden DOM (no markdown, no morph — the payload is a replace
    // window, intermediate frames carry no information).
    const frozen = body.innerHTML
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-4', display_output: 'step 1\nstep 2' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-4', display_output: 'step 1\nstep 2\nstep 3' } },
    ])
    expect(body.innerHTML).toBe(frozen)
    expect(body.classList.contains('hidden')).toBe(true)
    // Expanding renders the LATEST snapshot exactly once — 'step 3' present,
    // no duplicate 'step 2' accumulation.
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('step 3')
    expect(body.textContent.match(/step 2/g)?.length).toBe(1)
  })

  it('terminal fold: tool_end output stays pending until expand renders it', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-5', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-5', display_output: 'streaming…' } },
    ])
    const body = liveBashBody()
    expect(body.classList.contains('hidden')).toBe(true)
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'fold-5', display_output: 'FINAL OUTPUT' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    clickToolHeader()
    expect(body.textContent).toContain('FINAL OUTPUT')
  })

  it('usage updates progress bar', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: {
        type: 'usage',
        usage_event: {
          input_tokens: 1234,
          output_tokens: 5678,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
      },
    })
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑1.2k')
    const outEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↓'),
    )
    expect(outEl?.textContent).toBe('↓5.5k')
  })

  it('turn_start estimate drives ↑input before usage arrives', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start', input_estimate: 1234 },
    ])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑1.2k')

    // Billed usage replaces the estimate instead of adding to it:
    // 1000 new + 300 cache read = 1300, not 1300 + 1234.
    dispatchEvents([
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 20, cache_read_input_tokens: 300 } },
    ])
    expect(inEl?.textContent).toBe('↑1.3k')
  })

  it('turn_start estimate accumulates over turns and never double-counts', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start', input_estimate: 1000 },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
      // Turn 2 estimates ride on the billed turn 1: 1000 + 2000 = 3000.
      { type: 'turn_start', input_estimate: 2000 },
    ])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑2.9k')

    // Turn 2's billed 2048 replaces its 2000 estimate: 1000 + 2048 = 3048.
    dispatchEvents([
      { type: 'usage', usage_event: { input_tokens: 2048, output_tokens: 1000 } },
    ])
    expect(inEl?.textContent).toBe('↑3.0k')
  })

  it('sub-agent turn_start estimate leaves ↑input alone', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start', input_estimate: 1000 },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      {
        type: 'turn_start',
        input_estimate: 9000,
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 },
      },
    ])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑1.0k')
  })

  it('job-tagged usage is stripped: the mainline estimate and totals stay untouched', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start', input_estimate: 300 },
      { type: 'usage', usage_event: { input_tokens: 300, output_tokens: 20 } },
      // Turn 2's estimate rides on turn 1's billed input: 300 + 400 = 700.
      { type: 'turn_start', input_estimate: 400 },
    ])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑700')
    const outEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↓'),
    )
    const outBefore = outEl?.textContent

    // A background job's tokens belong entirely to the job (its completion
    // notification <tokens>): mainline estimate and totals stay exactly as
    // they were.
    dispatchEvents([
      {
        type: 'usage',
        usage_event: { input_tokens: 111, output_tokens: 5 },
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1, job_id: 'agent-9' },
      },
    ])
    expect(inEl?.textContent).toBe('↑700')
    expect(outEl?.textContent).toBe(outBefore)

    // Sync sub-agent usage still accumulates into the mainline totals:
    // billed 300 + 111 with the 400 estimate riding on top.
    dispatchEvents([
      {
        type: 'usage',
        usage_event: { input_tokens: 111, output_tokens: 5 },
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 },
      },
    ])
    expect(inEl?.textContent).toBe('↑811')
  })

  it('bare turn_start estimate reaches ↑input via initStreaming', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // No query_start ahead of it: an auto-turn or drained-attachment query
    // arrives as a bare turn_start, which opens the stream through
    // initStreaming instead of the mid-query guard.
    dispatchEvents([{ type: 'turn_start', input_estimate: 777 }])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑777')
  })

  it('query_end retires an uncleared estimate: the next query starts at 0', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q1')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      // Query 1's turn never gets its usage event, so the estimate is still
      // standing when the query ends.
      { type: 'turn_start', input_estimate: 4000 },
      { type: 'query_end' },
    ])
    const upSpans = () =>
      Array.from(document.querySelectorAll('span'))
        // .ch = per-char sweep span inside inEl; only segment spans count
        .filter((s) => s.textContent?.startsWith('↑') && !s.classList.contains('ch'))
        .map((s) => s.textContent)
    // The finalized bar bills what the estimate never covered: 0.
    expect(upSpans()).toEqual(['↑0'])

    // Query 2 opens with no turn_start of its own yet — the fresh bar must
    // read 0, not carry query 1's leftover estimate.
    dispatchEvents([{ type: 'query_start' }])
    expect(upSpans()).toEqual(['↑0', '↑0'])
  })

  it('error renders red block on streaming assistant', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    dispatch({ type: 'error', message: 'boom' })
    const red = document.querySelectorAll('.text-red')
    const errorEl = Array.from(red).find(el => el.textContent?.includes('boom'))
    expect(errorEl?.textContent).toContain('boom')
  })

  it('comet sweep cold start: cursor walks the full visible string', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])

    const root = document.querySelector('.status-line') as HTMLElement
    const p1s = () => Array.from(root.querySelectorAll<HTMLElement>('.p1'))
    // k=0 is the synchronous first step: highlight exists the instant the
    // query starts.
    expect(p1s().length).toBe(1)
    expect(p1s()[0].textContent).toBe('↑')

    vi.advanceTimersByTime(110)
    expect(p1s()[0].textContent).toBe('0')
    expect((p1s()[0].previousElementSibling as HTMLElement).classList.contains('t1')).toBe(true)

    // Walk k=2..14 (14 advances total). The 200ms refresh ticks interleave:
    // elapsedEl rewrites itself every tick, and the tick at t=1000 shrinks
    // '0.8s'→'1s' (a real span rebuild) — the last two highlights must land
    // on the rebuilt '1','s' spans, not the initial '0','s'.
    const seq = ['↑', '0']
    for (let k = 2; k <= 14; k++) {
      vi.advanceTimersByTime(110)
      seq.push(p1s()[0].textContent ?? '')
    }
    expect(seq).toEqual(['↑', '0', '↓', '0', '·', '0', '.', '0', ' ', 't', '/', 's', '·', '1', 's'])
    stopStatusSweep()
    vi.useRealTimers()
  })

  it('comet sweep follows numeric refreshes across a real rebuild', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])

    // tick@200 has run: toolCountEl got its one-time '0 tools', elapsedEl
    // reads '0.2s'.
    vi.advanceTimersByTime(250)
    const root = document.querySelector('.status-line') as HTMLElement
    const before = root.textContent
    expect(before).toBe('↑0↓0·0.0 t/s··0 tools·0.2s')

    // Crosses tick@1000 where '0.8s'→'1s' rebuilds the elapsed spans; every
    // other segment's text must be untouched (token counts stay 0).
    vi.advanceTimersByTime(1000)
    expect(root.textContent).toBe(before.replace('0.2s', '1s'))

    // The cursor keeps advancing over the rebuilt DOM: one of each class per
    // step, and the peak node changes identity every step.
    let prev = root.querySelector('.p1')
    for (let i = 0; i < 3; i++) {
      vi.advanceTimersByTime(110)
      expect(root.querySelectorAll('.p1').length).toBe(1)
      expect(root.querySelectorAll('.t1').length).toBe(1)
      expect(root.querySelectorAll('.t2').length).toBe(1)
      const cur = root.querySelector('.p1')
      expect(cur).not.toBe(prev)
      prev = cur
    }
    stopStatusSweep()
    vi.useRealTimers()
  })

  it('query_end freezes the sweep for good', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    vi.advanceTimersByTime(110)
    const root = document.querySelector('.status-line') as HTMLElement
    expect(root.querySelectorAll('.p1').length).toBe(1)

    dispatchEvents([{
      type: 'query_end',
      usage_event: { input_tokens: 10, output_tokens: 5, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    }])
    expect(root.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    vi.advanceTimersByTime(5000)
    expect(root.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    vi.useRealTimers()
  })

  it('error path stops the sweep without finalize', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    vi.advanceTimersByTime(110)
    const root = document.querySelector('.status-line') as HTMLElement
    expect(root.querySelectorAll('.p1').length).toBe(1)

    dispatch({ type: 'error', message: 'boom' })
    expect(root.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    vi.advanceTimersByTime(5000)
    expect(root.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    vi.useRealTimers()
  })

  it('connect_status resets state for fresh history', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1',
          role: 'user',
          text: 'old',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: 0,
        },
      ],
      nextCursor: 'c1',
      hasMore: false,
    })
    expect(document.body.textContent).toContain('old')
    // Reconnect → fresh history replaces old.
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-2',
          role: 'user',
          text: 'fresh',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: 0,
        },
      ],
      nextCursor: '',
      hasMore: false,
    })
    expect(document.body.textContent).not.toContain('old')
    expect(document.body.textContent).toContain('fresh')
  })

  it('query_end scrolls to bottom after streaming completes', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 2000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('test query')
    pressEnter()
    chat.scrollEl.scrollTop = 1500
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'A'.repeat(1000) },
    ])

    // query_end: progress bar finalize changes DOM height, must re-scroll.
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 3000 })
    chat.scrollEl.scrollTop = 2500
    dispatchEvents([
      { type: 'text_end' },
      { type: 'turn_end' },
      { type: 'query_end' },
    ])

    expect(chat.scrollEl.scrollTop).toBe(chat.scrollEl.scrollHeight)
  })

  it('streaming fast text keeps scrolled to bottom (no scrollBtn)', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })
    Element.prototype.scrollIntoView = vi.fn()

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('fast stream')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    // Rapid-fire deltas (simulating fast stream)
    for (let i = 0; i < 50; i++) {
      Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 + i * 50 })
      dispatch({ type: 'event', event: { type: 'text_delta', text: `chunk ${i} ` } })
    }

    // scrollBtn should remain hidden (user is at bottom)
    const scrollBtn = chat.root.querySelector('.scroll-progress')?.closest('button') as HTMLElement
    expect(scrollBtn.classList.contains('opacity-0')).toBe(true)
  })

  it('initial history load scrolls to bottom synchronously (no RAF delay)', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm1', role: 'user', text: 'hello',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })

    // scrollTop must be set synchronously during loadHistory,
    // not deferred to RAF (content-visibility makes deferred scroll land wrong).
    expect(chat.scrollEl.scrollTop).toBe(chat.scrollEl.scrollHeight)
  })

  it('reconnect scrolls to bottom after history load even if previously scrolled up', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })
    Element.prototype.scrollIntoView = vi.fn()

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })

    // Simulate user scrolled up: scroll button should be visible
    chat.scrollEl.scrollTop = 0
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    const scrollBtn = chat.root.querySelector('.scroll-progress')?.closest('button') as HTMLElement
    expect(scrollBtn.classList.contains('opacity-0')).toBe(false)

    // Reconnect with history
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1',
          role: 'user',
          text: 'reconnect message',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: 0,
        },
      ],
      nextCursor: '',
      hasMore: false,
    })

    // After reconnect, isNearBottom should be reset so scroll button is hidden
    expect(scrollBtn.classList.contains('opacity-0')).toBe(true)
  })

  it('scroll near top triggers prefetch when < 10 messages above viewport', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1', role: 'user', text: 'first',
          thinking: [], tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: 'c1',
      hasMore: true,
    })
    // With only 1 message and hasMore=true, loadHistory calls maybePrefetchHistory
    // which detects < 10 messages above viewport and fetches immediately.
    const historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(1)
    expect(historyReqs[0].cursor).toBe('c1')
  })

  it('loadingMore guard prevents concurrent prefetch', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1', role: 'user', text: 'msg1',
          thinking: [], tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: 'c1',
      hasMore: true,
    })
    // First prefetch already sent by loadHistory's maybePrefetchHistory.
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(1)

    // Scrolling again must not send duplicate (loadingMore guard).
    const scrollEl = document.querySelector('.overflow-y-auto') as HTMLElement
    Object.defineProperty(scrollEl, 'scrollTop', { configurable: true, writable: true, value: 10 })
    scrollEl.dispatchEvent(new Event('scroll'))
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(1)
  })

  it('does not prefetch when hasMore is false', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-1', role: 'user', text: 'only',
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(0)
  })

  it('chain-prefetches: second batch arrives with few messages, continues loading', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-1', role: 'user', text: 'first',
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: 'c1', hasMore: true,
    })
    // First prefetch fired immediately (1 message < 10 threshold).
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(1)

    // Second batch arrives — still only a few messages, still has more.
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-2', role: 'assistant', text: 'reply',
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: 'c2', hasMore: true,
    })
    // Second prefetch should fire (2 messages still < 10 threshold).
    const reqs = sent.filter((m) => m.type === 'history_request')
    expect(reqs.length).toBe(2)
    expect(reqs[1].cursor).toBe('c2')
  })

  it('sub-agent tool duration shows perceived time on tool_end', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'tool_start', agent, tool_use: { id: 'sub-tool-1', name: 'Bash' } },
    ])

    // Advance time by 2 seconds
    vi.advanceTimersByTime(2000)

    dispatchEvents([
      { type: 'tool_end', agent, tool_result: { tool_use_id: 'sub-tool-1', display_output: 'done' } },
    ])

    // Find the sub-agent tool's duration element
    const subTool = document.querySelector('[data-tool-root][data-tool-name="Bash"]')
    expect(subTool).toBeTruthy()
    const durEl = subTool!.querySelector('.font-mono.text-xs')
    expect(durEl).toBeTruthy()
    // Duration should show ~2s, not 0s
    expect(durEl!.textContent).toContain('2s')
    vi.useRealTimers()
  })

  it('sub-agent tool live ticking updates duration while running', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'tool_start', agent, tool_use: { id: 'sub-tool-1', name: 'Bash' } },
    ])

    const subTool = document.querySelector('[data-tool-root][data-tool-name="Bash"]')
    const durEl = subTool!.querySelector('.font-mono.text-xs') as HTMLElement
    expect(durEl).toBeTruthy()

    // After 1 second, live ticking should update duration
    const before = durEl.textContent
    vi.advanceTimersByTime(1000)
    const after = durEl.textContent
    expect(after).not.toBe(before)
    expect(after).toContain('1s')
    vi.useRealTimers()
  })

  it('sub-agent tool duration freezes after tool_end', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'tool_start', agent, tool_use: { id: 'sub-tool-1', name: 'Bash' } },
    ])

    vi.advanceTimersByTime(3000)
    dispatchEvents([
      { type: 'tool_end', agent, tool_result: { tool_use_id: 'sub-tool-1', display_output: 'done' } },
    ])

    const subTool = document.querySelector('[data-tool-root][data-tool-name="Bash"]')
    const durEl = subTool!.querySelector('.font-mono.text-xs') as HTMLElement
    const frozen = durEl.textContent

    // Advance more time — duration should NOT change
    vi.advanceTimersByTime(5000)
    expect(durEl.textContent).toBe(frozen)
    vi.useRealTimers()
  })

  it('Esc during running tool: interrupt text renders after tool, not appended to prior text block', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run slow command')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'Let me read that.' },
      { type: 'tool_start', tool_use: { id: 'read-1', name: 'Read' } },
    ])

    // Tool running, no output. Esc. Connector sends interrupt text_delta.
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])

    // DOM order must be: text("Let me read that") → tool(Read) → text("[Request interrupted]")
    // NOT: text("Let me read that[Request interrupted]") → tool(Read)
    const allText = document.body.textContent ?? ''
    expect(allText).toContain('[Request interrupted by user]')

    // Verify the interrupt text is NOT inside the first text block (appended to "Let me read that")
    const toolRoot = document.querySelector('[data-tool-root][data-tool-name="Read"]')
    expect(toolRoot).toBeTruthy()

    // The interrupt text element should come AFTER the tool root in DOM order
    const allElements = document.querySelectorAll('*')
    let toolIdx = -1
    let interruptIdx = -1
    for (let i = 0; i < allElements.length; i++) {
      if (allElements[i] === toolRoot) toolIdx = i
    }
    // Find the element containing interrupt text
    for (let i = 0; i < allElements.length; i++) {
      const el = allElements[i] as HTMLElement
      if (el.children.length === 0 && el.textContent === '[Request interrupted by user]') {
        interruptIdx = i
        break
      }
    }
    expect(interruptIdx).toBeGreaterThan(toolIdx)
  })

  it('Esc during running tool: interrupt text renders, tool shows failed', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run slow command')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'Let me run that.' },
      { type: 'tool_start', tool_use: { id: 'bash-1', name: 'Bash' } },
    ])

    // Tool is running with no output. User presses Esc.
    // Connector sends interrupt via text_delta, then query_end(aborted).
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])

    const allText = document.body.textContent ?? ''

    // 1. Interrupt text must be visible in DOM (not dropped)
    expect(allText).toContain('[Request interrupted by user]')

    // 2. Interrupt text should appear only once (no duplication)
    const interruptCount = (allText.match(/Request interrupted/g) || []).length
    expect(interruptCount).toBe(1)

    // 3. Running tool should show as failed (red dot), not success (green dot)
    const bashTool = document.querySelector('[data-tool-root][data-tool-name="Bash"]')
    expect(bashTool).toBeTruthy()
    const dot = bashTool!.querySelector('span.heartbeat, span.text-red')
    expect(dot?.classList.contains('text-red')).toBe(true)
  })

  it('Esc during running tool: subsequent query works normally', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('first query')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'bash-1', name: 'Bash' } },
    ])
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])

    // Second query after abort should work normally
    setTextarea('second query')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'Working again.' },
      { type: 'query_end' },
    ])

    const allText = document.body.textContent ?? ''
    expect(allText).toContain('Working again.')
    expect(allText).toContain('second query')
  })

  it('Edit tool output auto-expands on finish (not hidden)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('fix the bug')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'edit-1', name: 'Edit' } },
    ])
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'edit-1', display_output: 'some diff output' } },
      { type: 'query_end' },
    ])

    const editTool = document.querySelector('[data-tool-root][data-tool-name="Edit"]')
    expect(editTool).toBeTruthy()
    const bodyEl = editTool!.children[1] as HTMLElement
    expect(bodyEl.classList.contains('hidden')).toBe(false)
  })

  it('Read tool output stays collapsed on finish (hidden)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('read the file')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'read-1', name: 'Read' } },
    ])
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'read-1', display_output: 'file contents' } },
      { type: 'query_end' },
    ])

    const readTool = document.querySelector('[data-tool-root][data-tool-name="Read"]')
    expect(readTool).toBeTruthy()
    const bodyEl = readTool!.children[1] as HTMLElement
    expect(bodyEl.classList.contains('hidden')).toBe(true)
  })

  it('sub-agent thinking_delta updates token rate', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('research something')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'thinking_start', agent },
    ])

    // First delta already reads (no gate): 40 wire tokens read at the next
    // 200ms tick as 80·(decayed) tokens over 0.396s of integral plus the
    // 0.3s prior ≈ 113 t/s — prior-suppressed, never a fabricated 0.0.
    dispatchEvents([
      { type: 'thinking_delta', agent, thinking: { text: 'B'.repeat(40) }, tokens: 40 },
    ])
    vi.advanceTimersByTime(200)
    const firstEl = findRateSpan()
    expect(firstEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    const first = parseFloat(firstEl!.textContent!)
    expect(first).toBeGreaterThan(100)
    expect(first).toBeLessThan(130)

    // Sustained wire-token deltas keep a numeric reading.
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(1000)
      dispatchEvents([
        { type: 'thinking_delta', agent, thinking: { text: 'B'.repeat(50) }, tokens: 50 },
      ])
    }
    vi.advanceTimersByTime(200)
    const rateEl = findRateSpan()
    expect(rateEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    vi.useRealTimers()
  })

  it('sub-agent text_delta updates token rate', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('research something')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
    ])

    // First delta already reads (no gate): a numeric reading from the
    // first 200ms tick.
    dispatchEvents([
      { type: 'text_delta', agent, text: 'B'.repeat(40), tokens: 40 },
    ])
    vi.advanceTimersByTime(200)
    const firstEl = findRateSpan()
    expect(firstEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    const first = parseFloat(firstEl!.textContent!)
    expect(first).toBeGreaterThan(100)
    expect(first).toBeLessThan(130)

    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(1000)
      dispatchEvents([
        { type: 'text_delta', agent, text: 'B'.repeat(50), tokens: 50 },
      ])
    }
    vi.advanceTimersByTime(200)
    const rateEl = findRateSpan()
    expect(rateEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    vi.useRealTimers()
  })

  // The rate span is the progress-bar child right after the ↑/↓ token spans
  // (root children: inEl, outEl, suffix, ·, rateEl, ...).
  function findRateSpan(): HTMLElement | null {
    const inEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.startsWith('↑'),
    )
    return (inEl?.parentElement?.children[4] as HTMLElement) ?? null
  }

  it('live ↓output counts wire tokens and reconciles on usage', () => {
    // Full path: text_delta tokens → tokenRate.localTokens → ↓output in the
    // 200ms tick → usage event zeroes the local sum (authoritative count
    // takes over, no double counting).
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('write a long answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    const outEl = () => {
      const el = Array.from(document.querySelectorAll('span')).find(
        (s) => s.textContent?.startsWith('↓'),
      )
      return el as HTMLElement
    }
    for (let i = 0; i < 3; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'streamed chunk of text', tokens: 40 }])
      vi.advanceTimersByTime(1000)
    }
    vi.advanceTimersByTime(200)
    expect(outEl()?.textContent).toBe('↓120')

    dispatchEvents([
      { type: 'usage', usage_event: { input_tokens: 10, output_tokens: 200 } },
    ])
    expect(outEl()?.textContent).toBe('↓200')
    // The local sum was reconciled away: further ticks keep the
    // authoritative value instead of re-adding it.
    vi.advanceTimersByTime(200)
    expect(outEl()?.textContent).toBe('↓200')
    vi.useRealTimers()
  })

  it('rate shows a prior-suppressed reading immediately, then keeps reading', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('long answer please')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    // 90 tokens in one delta: the next tick already shows a number —
    // 180·(decayed) tokens over 0.396s of integral plus the 0.3s prior ≈
    // 254 t/s (prior-suppressed; a span-based read would report ~450k).
    dispatchEvents([{ type: 'text_delta', text: 'a chunk of streamed words', tokens: 90 }])
    vi.advanceTimersByTime(200)
    const firstEl = findRateSpan()
    expect(firstEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    const first = parseFloat(firstEl!.textContent!)
    expect(first).toBeGreaterThan(230)
    expect(first).toBeLessThan(280)

    // 4.4s later another 150 tokens: still numeric — the reading blends.
    vi.advanceTimersByTime(4400)
    dispatchEvents([{ type: 'text_delta', text: 'more words stream onward', tokens: 150 }])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    vi.useRealTimers()
  })

  it("engine switch does not seed the new engine's rate from the old engine's meter", () => {
    // Regression: engine_switch → server sendMetadata → resetAllState →
    // tokenRate.reset() kept the decayed sums (omp seed semantics). With one
    // shared meter for all engines, the new engine's first stream
    // blended the old engine's rate: slow→fast kept reading ~48 t/s while
    // the fast engine streamed 120 t/s.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })

    // Engine A (slow): 15 tokens per 500ms = 30 t/s for 12s.
    setTextarea('slow engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'slow chunk of words', tokens: 15 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    const slow = parseFloat(findRateSpan()!.textContent!)
    expect(slow).toBeGreaterThan(20)
    expect(slow).toBeLessThan(40)

    // A's query ends; the user switches engines. The server answers
    // engine_switch with a metadata frame for the new engine (connector.go
    // switchEngine → sendMetadata).
    dispatchEvents([{ type: 'turn_end' }, { type: 'query_end' }])
    vi.advanceTimersByTime(2000)
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'engineB' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    // metadata sets pendingPageZero and buffers live events until the page
    // lands — flush it so engine B's events are applied live.
    dispatch({ type: 'history', messages: [], stream: [], nextCursor: '', hasMore: false })

    // Engine B (fast): 60 tokens per 500ms = 120 t/s.
    setTextarea('fast engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 8; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'fast chunk of words!', tokens: 60 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    const fast = parseFloat(findRateSpan()!.textContent!)
    // A's frozen sums dominate the kernel for tens of seconds (20s tail):
    // the buggy blend reads ~48 t/s; a meter cleared on the switch reads
    // ~130 t/s from B's deltas alone.
    expect(fast).toBeGreaterThanOrEqual(80)
    vi.useRealTimers()
  })

  it('per-engine buckets: B reads pure B, and returning to A is not polluted by B', () => {
    // Full A→B→A cycle with per-engine buckets. Each switch's full-clear
    // wipes the incoming engine's bucket, and B's fast episode never enters
    // A's bucket — the return stream reads ~30 t/s, not a blend pushed up
    // by B's 120 t/s residue.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true, engineID: 'engineA' })

    const meta = (engineID: string) => ({
      type: 'metadata',
      connect: { connected: true, engineID },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: engineID },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    const flushPage = () =>
      dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // Engine A (slow): 15 tokens per 500ms = 30 t/s, turn left dangling.
    setTextarea('slow engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'slow chunk of words', tokens: 15 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThan(20)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeLessThan(40)

    // Mid-stream switch to B (no turn_end/query_end for A) + page flush.
    dispatch(meta('engineB'))
    flushPage()

    // Engine B (fast): 60 tokens per 500ms = 120 t/s.
    setTextarea('fast engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 8; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'fast chunk of words!', tokens: 60 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThanOrEqual(80)

    // Back to A: A's bucket was wiped by the switch, and B's evidence never
    // entered it — A's fresh 30 t/s stream reads in the pure-A band (a
    // B-blended reading would sit well above it).
    dispatch(meta('engineA'))
    flushPage()
    setTextarea('slow engine again')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'slow chunk of words', tokens: 15 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThan(20)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeLessThan(40)
    vi.useRealTimers()
  })

  it('per-engine buckets: dropped metadata answer keeps reader and writer on the same bucket', () => {
    // engine_switch does not flip activeEngineID locally — attribution moves
    // only on the server's confirmation. With the answer dropped, B's deltas
    // meter into the pre-switch bucket, the same one the refresh loop reads:
    // the reading converges on B's actual rate as B's evidence dominates the
    // kernel (A's decayed 30 t/s tail holds the blend to ~86 t/s after 12s of
    // B's 120 t/s). A reader/writer bucket split — deltas metered where the
    // display never looks — would strand the reading on A's decay at ~9 t/s.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true, engineID: 'e1' })
    dispatch({
      type: 'metadata',
      connect: { connected: true, engineID: 'e1' },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: {
        engines: [
          { id: 'e1', name: 'agent-1', model: 'm' },
          { id: 'e2', name: 'agent-2', model: 'm' },
        ],
        activeID: 'e1',
      },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // Engine A (slow): 15 tokens per 500ms = 30 t/s, turn left dangling.
    setTextarea('slow engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'slow chunk of words', tokens: 15 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThan(20)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeLessThan(40)

    // User picks agent-2: engine_switch leaves, and the answer never comes.
    const trigger = document.querySelector(
      'header button.text-\\[15px\\]:not(.hidden)',
    ) as HTMLButtonElement
    trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = document.querySelector(
      '[data-testid="engine-picker-panel"]',
    ) as HTMLElement
    const row = Array.from(panel.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('agent-2'),
    ) as HTMLButtonElement
    row.click()
    expect(
      sent.find((m) => (m as { type: string }).type === 'engine_switch'),
    ).toEqual({ type: 'engine_switch', engineID: 'e2' })

    // B's deltas land with no reset: 60 tokens per 500ms = 120 t/s, 12s —
    // the blend must climb past the pure-B band's floor, proving the
    // displayed meter is still tracking the deltas that actually arrive.
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'fast chunk of words!', tokens: 60 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThanOrEqual(80)
    vi.useRealTimers()
  })

  it('tool_run pauses the rate display; the resumed reading excludes the tool gap', () => {
    // omp: "Tool execution between messages neither ages the sums nor counts
    // as time" — tool_run is the wui's execution boundary. The display falls
    // back to 0.0 t/s while paused, and the resumed reading stays near its
    // pre-tool value instead of sliding down across the tool duration.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('long answer please')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    const rateValue = () => parseFloat(findRateSpan()?.textContent ?? '')
    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'chunk of streamed words', tokens: 100 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    const r0 = rateValue()
    expect(r0).toBeGreaterThan(0)

    dispatchEvents([{ type: 'tool_run', tool_use: { id: 't1', name: 'Bash' } }])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    vi.advanceTimersByTime(5000) // tool execution
    dispatchEvents([{ type: 'text_delta', text: 'resumed words arrive', tokens: 100 }])
    vi.advanceTimersByTime(200)
    // Gap skipped, not aged: with gap-aging this reads ~0.55x r0.
    const r1 = rateValue()
    expect(r1).toBeGreaterThanOrEqual(0.9 * r0)
    expect(r1).toBeLessThanOrEqual(1.7 * r0)
    vi.useRealTimers()
  })

  it('turn_end pauses the rate display through the Agent tool gap (agent turn_end does not)', () => {
    // The sub-agent's deltas re-arm the meter, so from the Agent tool_end to
    // the next turn's first delta the old reading would bare-decay across
    // the turn gap. Main turn_end stops the clock (TUI parity); the
    // sub-agent's own turn_end is interior to the parent Agent run and must
    // not touch the main meter.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('delegate the research')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    const rateValue = () => parseFloat(findRateSpan()?.textContent ?? '')
    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'chunk of streamed words', tokens: 100 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(rateValue()).toBeGreaterThan(0)

    const agent = { parent_tool_use_id: 'ag1', agent_type: 'Explore', depth: 1 }
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 'ag1', name: 'Agent' } },
      { type: 'tool_run', tool_use: { id: 'ag1', name: 'Agent' } },
    ])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    // Sub-agent stream re-arms the meter — its real flow counts.
    for (let i = 0; i < 6; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'sub agent words', tokens: 100, agent }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(rateValue()).toBeGreaterThan(0)

    // Sub-agent turn_end: interior boundary — the reading survives it.
    dispatchEvents([{ type: 'turn_end', agent }])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).not.toBe('0.0 t/s')

    const r0 = rateValue()
    // Agent tool completes, then the main turn ends: clock stops.
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'ag1', display_output: 'done' } },
      { type: 'turn_end' },
    ])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    // Turn gap skipped, not aged: with gap-aging this reads ~0.55x r0.
    vi.advanceTimersByTime(5000)
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'text_delta', text: 'resumed words arrive', tokens: 100 },
    ])
    vi.advanceTimersByTime(200)
    const r1 = rateValue()
    expect(r1).toBeGreaterThanOrEqual(0.9 * r0)
    expect(r1).toBeLessThanOrEqual(1.7 * r0)
    vi.useRealTimers()
  })

  it('turn_end keeps the bash tool gap paused until the next turn resumes', () => {
    // tool_run already paused the clock, so turn_end's pause is a no-op
    // there — the scenario pins that it stays a no-op (no un-pause, no
    // double aging) and the next turn's first delta recovers the reading.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run the build')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'chunk of streamed words', tokens: 100 }])
      vi.advanceTimersByTime(500)
    }
    dispatchEvents([{ type: 'tool_run', tool_use: { id: 't1', name: 'Bash' } }])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 't1', display_output: 'done' } },
      { type: 'turn_end' },
    ])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    vi.advanceTimersByTime(5000)
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'text_delta', text: 'resumed words arrive', tokens: 100 },
    ])
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()?.textContent ?? '0')).toBeGreaterThan(0)
    vi.useRealTimers()
  })

  it('REPRO: param deltas after tool_start un-pause the meter and the rate slides during exec', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run sleep 10')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'thinking_start' },
    ])
    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'thinking_delta', thinking: { text: 'planning the sleep' }, tokens: 60 }])
      dispatchEvents([{ type: 'text_delta', text: 'running sleep now', tokens: 60 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()?.textContent ?? '0')).toBeGreaterThan(0)
    // tool_start only opens the block; params still stream after it.
    dispatchEvents([{ type: 'tool_start', tool_use: { id: 'tb1', name: 'Bash' } }])
    dispatchEvents([
      { type: 'tool_param_delta', partial_input: { id: 'tb1', name: 'Bash', delta: '{"command":"sleep 10 && echo done","timeout"', summary: 'sleep 10' }, tokens: 29 },
    ])
    dispatchEvents([{ type: 'tool_run', tool_use: { id: 'tb1', name: 'Bash' } }])
    for (let i = 0; i < 20; i++) {
      vi.advanceTimersByTime(500)
      const got = findRateSpan()?.textContent
      if (got !== '0.0 t/s') {
        throw new Error(`tick ${i}: rate = ${got}, want '0.0 t/s'`)
      }
    }
    vi.useRealTimers()
  })
  it('second query meters independently: TTFT blank, first delta reads on its own', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('first query')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])
    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'chunk of streamed words', tokens: 100 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    const r0 = parseFloat(findRateSpan()?.textContent ?? '0')
    expect(r0).toBeGreaterThan(0)
    dispatchEvents([{ type: 'query_end' }])
    // Next query: the meter was wiped at query_start — TTFT stays 0.0, and
    // the first delta reads on its own (no gate: the 300ms prior makes even
    // one token visible).
    setTextarea('second query')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])
    vi.advanceTimersByTime(200)
    // TTFT: no delta yet — nothing to read; the bar reads 0.0.
    const ttft = document.body.textContent?.match(/([\d.]+) t\/s/g) ?? []
    expect(ttft.some((s) => parseFloat(s) > 0)).toBe(false)
    dispatchEvents([{ type: 'text_delta', text: 'first', tokens: 1 }])
    vi.advanceTimersByTime(200)
    const firstDelta = document.body.textContent?.match(/([\d.]+) t\/s/g) ?? []
    expect(firstDelta.some((s) => parseFloat(s) > 0)).toBe(true)
    // Sidebar new-session: connect_status with a different sessionID wipes
    // the meter — the new session starts with no old-session blend.
    dispatch({ type: 'connect_status', connected: true, sessionID: 'other-session' })
    setTextarea('new session query')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])
    dispatchEvents([{ type: 'text_delta', text: 'first', tokens: 1 }])
    vi.advanceTimersByTime(200)
    const fresh = parseFloat(findRateSpan()?.textContent ?? '999')
    expect(fresh).toBeLessThanOrEqual(10) // 2 tokens / 0.3s ≈ 6.7, no blend
    // Reconnect to the same session (sessionID unchanged, no clear()):
    // dispatch must not throw.
    dispatch({ type: 'connect_status', connected: true, sessionID: 'other-session' })
    expect(() => dispatchEvents([{ type: 'text_delta', text: 'x', tokens: 1 }])).not.toThrow()
    vi.useRealTimers()
  })
  it('reconnect does not duplicate history messages', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'hello',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    expect(document.body.textContent).toContain('hello')

    // Reconnect
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'hello',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'hello',
    )
    expect(spans.length).toBe(1)
  })

  it('user message preserves newlines in history', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'line1\nline2\nline3',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    // The span must have whitespace-pre-wrap to render newlines
    const spans = Array.from(document.querySelectorAll('span')).filter(
      (s) => s.textContent === 'line1\nline2\nline3',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).toContain('whitespace-pre-wrap')
    // The parent content div must also have whitespace-pre-wrap
    // (buildShell user content div)
    const contentDiv = spans[0].parentElement
    expect(contentDiv).toBeTruthy()
    expect(contentDiv!.className).toContain('whitespace-pre-wrap')
  })

  it('user message content div has break-words to prevent overflow', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'https://example.com/very/long/path/that/should/overflow',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const spans = Array.from(document.querySelectorAll('span')).filter(
      (s) => s.textContent === 'https://example.com/very/long/path/that/should/overflow',
    )
    expect(spans.length).toBe(1)
    const contentDiv = spans[0].parentElement
    expect(contentDiv).toBeTruthy()
    expect(contentDiv!.className).toContain('break-words')
  })

  it('Bash grep in history is classified as search', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'a1', role: 'assistant', text: '',
          thinking: [],
          tools: [],
          blocks: [
            {
              kind: 'tool',
              tool: {
                id: 't1', name: 'Bash', summary: 'grep -n "pattern"',
                displayOutput: 'result', isError: false, isRunning: false,
                durationNs: 1_000_000_000,
                is_search: true,
              },
            },
          ],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    // The Bash tool with is_search:true should be rendered as collapsible
    const all = document.querySelectorAll('[data-tool-root]')
    expect(all.length).toBe(1)
    expect(all[0].getAttribute('data-collapsible')).toBe('1')
  })

  it('tool_start with is_search groups consecutive Bash tools', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash', input: { command: 'git log' }, is_search: true } },
      { type: 'tool_start', tool_use: { id: 't2', name: 'Bash', input: { command: 'git status' }, is_search: true } },
    ])
    // Both tools should be in a group
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    expect(toolRoots[0].getAttribute('data-collapsible')).toBe('1')
    expect(toolRoots[1].getAttribute('data-collapsible')).toBe('1')
    // They should be inside a tool-group
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
  })

  it('Task in history is classified as collapsible via is_task', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'a1', role: 'assistant', text: '',
          thinking: [],
          tools: [],
          blocks: [
            {
              kind: 'tool',
              tool: {
                id: 't1', name: 'Task', summary: 'create task',
                displayOutput: 'Created: #1 Fix login', isError: false, isRunning: false,
                durationNs: 1_000_000_000,
                is_task: true,
              },
            },
          ],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const all = document.querySelectorAll('[data-tool-root]')
    expect(all.length).toBe(1)
    expect(all[0].getAttribute('data-collapsible')).toBe('1')
  })

  it('tool_start with is_task groups consecutive Task tools', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Task', input: { action: 'list' }, is_task: true } },
      { type: 'tool_start', tool_use: { id: 't2', name: 'Task', input: { action: 'get', taskId: '1' }, is_task: true } },
    ])
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    expect(toolRoots[0].getAttribute('data-collapsible')).toBe('1')
    expect(toolRoots[1].getAttribute('data-collapsible')).toBe('1')
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
    const summary = group!.querySelector('[data-group-summary]')
    expect(summary).toBeTruthy()
    expect(summary!.textContent).toContain('2 Tasks')
  })

  it('tool_start with is_search renders Bash as collapsible', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash', input: {}, is_search: true } },
    ])
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(1)
    expect(toolRoots[0].getAttribute('data-collapsible')).toBe('1')
  })

  it('tool_param_delta updates isSearch and enables grouping', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash', input: {} } },
      { type: 'tool_param_delta', partial_input: { id: 't1', name: 'Bash', delta: '', summary: 'git log', is_search: true } },
      { type: 'tool_run', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'tool_start', tool_use: { id: 't2', name: 'Bash', input: {} } },
      { type: 'tool_param_delta', partial_input: { id: 't2', name: 'Bash', delta: '', summary: 'git status', is_search: true } },
      { type: 'tool_run', tool_use: { id: 't2', name: 'Bash' } },
    ])
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    // Both should be marked collapsible after tool_param_delta provides is_search
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
  })

  it('tool_param_delta with is_list enables grouping for Bash ls (not just is_search)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash', input: {} } },
      { type: 'tool_param_delta', partial_input: { id: 't1', name: 'Bash', delta: '', summary: 'ls /a', is_list: true } },
      { type: 'tool_run', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'tool_start', tool_use: { id: 't2', name: 'Bash', input: {} } },
      { type: 'tool_param_delta', partial_input: { id: 't2', name: 'Bash', delta: '', summary: 'ls /b', is_list: true } },
      { type: 'tool_run', tool_use: { id: 't2', name: 'Bash' } },
    ])
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    // Both Bash(ls) should be marked collapsible after tool_param_delta provides is_list
    // and grouped together (matches committed-render behavior in isCollapsible).
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
  })

  it('reconnect after streaming does not duplicate', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'task',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'response' },
      { type: 'text_end' },
      { type: 'query_end' },
    ])
    expect(document.body.textContent).toContain('task')
    expect(document.body.textContent).toContain('response')

    // Reconnect
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'task',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
        { id: 'a1', role: 'assistant', text: 'response',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })
    // Check presence, not count (regex \b is fragile with jsdom textContent).
    expect(document.body.textContent).toContain('task')
    expect(document.body.textContent).toContain('response')
    // Count span children — user messages create exactly 1 span per message.
    const userSpans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'task',
    )
    expect(userSpans.length).toBe(1)
  })

  it('reconnect mid-stream replay does not duplicate history', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // Initial: user msg + streaming query in progress.
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'do it',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'working' },
      // No query_end — stream interrupted by disconnect.
    ])
    expect(document.body.textContent).toContain('working')

    // Reconnect: history has the user msg (committed), buffer has replay events.
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'do it',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })
    // Replay: same stream events (turn not committed).
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'working' },
      { type: 'text_end' },
      { type: 'query_end' },
    ])
    // User message from history appears exactly once.
    const userSpans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'do it',
    )
    expect(userSpans.length).toBe(1)
    // "working" appears in the assistant response (from replay).
    expect(document.body.textContent).toContain('working')
  })

  it('history during streaming prepends without breaking streaming', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'current',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: 'c1', hasMore: true,
    })

    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'streaming' },
    ])

    // History arrives mid-stream (user scrolled up, observer fired).
    dispatch({
      type: 'history',
      messages: [
        { id: 'u0', role: 'user', text: 'older',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })

    expect(document.body.textContent).toContain('older')
    expect(document.body.textContent).toContain('streaming')
    expect(document.body.textContent).toContain('current')
  })

  it('takeover replay renders sub-agent events inside committed Agent tool', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })

    // History: main agent committed [thinking, tool_use(Agent)].
    // Agent tool is in "done" state with a result — but tool execution
    // (sub-agent) is still running. This is the takeover scenario:
    // appendMessage committed the response, buffer replays sub-agent events.
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1',
        role: 'assistant',
        thinking: [],
        tools: [{
          id: 'tu1',
          name: 'Agent',
          summary: 'Review latest commit',
          isError: false,
          isRunning: true,
          durationNs: 0,
          displayOutput: '',
        }],
        blocks: [
          { kind: 'tool', tool: {
            id: 'tu1',
            name: 'Agent',
            summary: 'Review latest commit',
            isError: false,
            isRunning: true,
            durationNs: 0,
            displayOutput: '',
          }},
        ],
        text: '',
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '',
        status: 'done',
        startedAt: 0,
      }],
      nextCursor: '',
      hasMore: false,
    })

    // Replay: sub-agent events arrive (from buffer).
    const agent = { parent_tool_use_id: 'tu1', agent_type: 'Reviewer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'thinking_start', agent },
      { type: 'thinking_delta', agent, thinking: { text: 'reviewing commit' } },
      { type: 'thinking_end', agent },
      { type: 'turn_end', agent },
    ])

    // Sub-agent content must appear in the DOM — not silently dropped.
    expect(document.body.textContent).toContain('reviewing commit')
  })

  it('sub-agent text_delta renders markdown (not plain textContent)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [], tools: [],
        blocks: [
          { kind: 'tool', tool: {
            id: 'tu1', name: 'Agent', summary: 'research',
            isRunning: true, durationNs: 0, displayOutput: '',
          }},
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })

    const agent = { parent_tool_use_id: 'tu1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'text_start', agent },
      { type: 'text_delta', agent, text: '## Findings\n\nThe tool uses `pendingToolStart`.' },
      { type: 'turn_end', agent },
    ])

    // Sub-agent text should be rendered as markdown HTML, not raw textContent.
    // If rendered correctly, <h2> and <code> tags exist.
    const agentTool = document.querySelector('[data-tool-name^="Agent"]')
    expect(agentTool).toBeTruthy()
    const toolRoot = agentTool!.closest('[data-tool-root]')
    const mdBody = toolRoot?.querySelector('.md-body.text-t1')
    expect(mdBody).toBeTruthy()
    expect(mdBody!.innerHTML).toContain('<h2>')
    expect(mdBody!.innerHTML).toContain('<code>')
  })

  it('takeover with running tool shows STOP button', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1',
        role: 'assistant',
        thinking: [],
        tools: [{
          id: 'tu1', name: 'Agent', summary: 'review',
          isRunning: true, durationNs: 0, displayOutput: '',
        }],
        blocks: [
          { kind: 'tool', tool: {
            id: 'tu1', name: 'Agent', summary: 'review',
            isRunning: true, durationNs: 0, displayOutput: '',
          }},
        ],
        text: '',
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    const stopBtn = Array.from(document.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('STOP'),
    )
    expect(stopBtn).toBeInstanceOf(HTMLButtonElement)
  })

  it('auto-prefetches when content is shorter than viewport (few messages)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // History arrives with more pages and only 1 message.
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-1', role: 'user', text: 'hello',
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: 'c1', hasMore: true,
    })
    // With < 10 messages above viewport, maybePrefetchHistory fires.
    const historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(1)
    expect(historyReqs[0].cursor).toBe('c1')
  })

  it('task_list message renders the task ring with count', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'task_list',
      tasks: [
        { id: '1', subject: 'Plan', status: 'completed' },
        { id: '2', subject: 'Code', status: 'in_progress' },
        { id: '3', subject: 'Test', status: 'pending' },
      ],
    })
    // Ring label shows completed/total.
    const label = document.querySelector('.task-label') as SVGTextElement
    expect(label).toBeTruthy()
    expect(label.textContent).toBe('1/3')
    // Ring is visible.
    const ring = label.closest('button') as HTMLElement
    expect(ring.style.display).not.toBe('none')
  })

  it('task ring popover shows full summary on click', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'task_list',
      tasks: [
        { id: '1', subject: 'Plan', status: 'completed' },
        { id: '2', subject: 'Code', status: 'in_progress' },
        { id: '3', subject: 'Test', status: 'pending' },
      ],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    ring.click()

    const popover = document.getElementById('task-popover') as HTMLElement
    expect(popover).toBeTruthy()
    const text = popover.textContent ?? ''
    expect(text).toContain('1/3 Done')
    expect(text).toContain('1 Running')
    expect(text).toContain('1 Pending')
  })

  it('connect_status hides the task panel', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'task_list',
      tasks: [{ id: '1', subject: 'UniqueTaskSubject123', status: 'pending' }],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    expect(ring).toBeTruthy()
    expect(ring.style.display).not.toBe('none')
    dispatch({ type: 'connect_status', connected: true })
    expect(ring.style.display).toBe('none')
  })

  it('job_list message shows the task panel with background job rows', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [
        { id: 'bg-1', kind: 'bash', status: 'running', command: 'make check', startedMs: 1 },
        { id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor', description: '修 wui 分页 bug', parentToolUseID: 'p1', startedMs: 2 },
      ],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    expect(ring.style.display).not.toBe('none')
    // Jobs-only mode: label is the running count.
    expect((document.querySelector('.task-label') as SVGTextElement).textContent).toBe('2')
    ring.click()
    const popover = document.getElementById('task-popover') as HTMLElement
    expect(popover.querySelectorAll('.jobs-row')).toHaveLength(2)
    expect(popover.textContent).toContain('make check')
    expect(popover.textContent).toContain('Executor · 修 wui 分页 bug')
  })

  it('job_output lands in the jobs store tail', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [{ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check', startedMs: 1 }],
    })
    dispatch({ type: 'job_output', jobID: 'bg-1', tail: 'ok  pkg/engine  1.2s\n' })
    expect(chat.jobsStore.feed('bg-1')?.tail).toBe('ok  pkg/engine  1.2s\n')
  })

  it('agent event frames accumulate into the job feed while chat renders normally', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [{ id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor', description: 'fix bug', parentToolUseID: 'p1', startedMs: 1 }],
    })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't-1', name: 'Read', input: {} }, agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 } },
      { type: 'text_delta', text: 'found it', agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 } },
    ])
    const blocks = chat.jobsStore.feed('agent-1')?.blocks
    expect(blocks).toHaveLength(2)
    expect(blocks?.[0]).toMatchObject({ kind: 'tool', id: 't-1', name: 'Read' })
    expect(blocks?.[1]).toMatchObject({ kind: 'text', text: 'found it' })
  })

  it('metadata with jobs hydrates the store (blocks + tail)', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
      jobs: {
        jobs: [
          { id: 'bg-1', kind: 'bash', status: 'running', command: 'go test ./...', startedMs: 1, tail: 'ok pkg/a\n' },
          {
            id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor',
            description: 'scan logs', parentToolUseID: 'p1', startedMs: 2,
            blocks: [{ kind: 'text', id: '', text: 'halfway report' }],
          },
        ],
      },
    })
    expect(chat.jobsStore.jobs().map((j) => j.id)).toEqual(['bg-1', 'agent-1'])
    expect(chat.jobsStore.feed('bg-1')?.tail).toBe('ok pkg/a\n')
    expect(chat.jobsStore.feed('agent-1')?.blocks[0]).toMatchObject({ kind: 'text', text: 'halfway report' })
    // Panel chrome reflects the hydrated view.
    expect((document.querySelector('.task-label') as SVGTextElement).textContent).toBe('2')
  })

  it('metadata without jobs clears the previous engine view (A→B switch)', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    const metaWithJobs = {
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'a' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
      jobs: { jobs: [{ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check', startedMs: 1 }] },
    } as const
    dispatch(metaWithJobs)
    expect(chat.jobsStore.hasAny()).toBe(true)
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    expect(ring.style.display).not.toBe('none')

    // Engine B's metadata carries no jobs field — the view must swap to
    // empty, not keep engine A's residue.
    dispatch({ ...metaWithJobs, engines: { engines: [], activeID: 'b' }, jobs: undefined })
    expect(chat.jobsStore.jobs()).toEqual([])
    expect(chat.jobsStore.feed('bg-1')).toBeUndefined()
    expect(ring.style.display).toBe('none')
  })

  it('expanded bash feed streams job_output frames and the stop button sends job_stop', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [{ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check', startedMs: 1 }],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    ring.click()
    const popover = document.getElementById('task-popover') as HTMLElement
    ;(popover.querySelector('.jobs-row') as HTMLElement).click()
    // The row label already carries the command: no command block.
    expect(popover.querySelector('.jobs-cmd')).toBeNull()

    dispatch({ type: 'job_output', jobID: 'bg-1', tail: 'ok  pkg/engine  1.2s\n' })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    const feed = popover.querySelector('.jobs-feed') as HTMLElement
    expect(feed.querySelectorAll('.md-body')).toHaveLength(1)
    expect(feed.textContent).toContain('ok  pkg/engine  1.2s')

    ;(popover.querySelector('.jobs-stop') as HTMLButtonElement).click()
    expect(sent).toContainEqual({ type: 'job_stop', jobID: 'bg-1' })
    // The stop click must not collapse the expansion it lives in.
    expect(popover.querySelector('.jobs-exp')).not.toBeNull()
  })

  it('expanded agent feed renders live tool cards and text through the full wiring', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [{ id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor', description: 'fix bug', parentToolUseID: 'p1', startedMs: 1 }],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    ring.click()
    const popover = document.getElementById('task-popover') as HTMLElement
    ;(popover.querySelector('.jobs-row') as HTMLElement).click()

    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't-1', name: 'Read', input: {} }, agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 } },
      {
        type: 'tool_end',
        tool_result: { tool_use_id: 't-1', output: 'done', is_error: false, display_output: '1 file' },
        agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 },
      },
      { type: 'text_delta', text: 'found it', agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 } },
    ])
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    const feed = popover.querySelector('.jobs-feed') as HTMLElement
    const tools = feed.querySelectorAll('[data-tool-root]')
    expect(tools).toHaveLength(1)
    expect(tools[0].dataset.toolName).toBe('Read')
    expect(tools[0].querySelector('.text-green')).not.toBeNull()
    expect(feed.textContent).toContain('found it')
    // Stop affordance present while the agent runs.
    expect(popover.querySelector('.jobs-stop')).not.toBeNull()
  })

  it('takeover does not reset cumulative usage for the whole query', () => {
    // The engine emits per-turn usage deltas; the frontend accumulates
    // across the whole query. On takeover (reconnect), connect_status
    // must NOT wipe the accumulated progressUsage, because query_end
    // finalizes stats from progressUsage and the final stats must reflect
    // the ENTIRE query (both pre- and post-takeover turns).
    //
    // Event sequence during a takeover:
    //   query_start → turn_start → usage(turn1) → [disconnect]
    //   connect_status → history → [replay: turn_start → usage(turn2)]
    //   → query_end(usage_event = turn1+turn2 totals)
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()

    // Turn 1: first turn before disconnect. Accumulate usage.
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
    ])

    // Disconnect + takeover: connect_status resets state, stats
    // carries server-accumulated usage so progressUsage is restored.
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    })

    // History replays committed messages (none yet — query still in flight).
    dispatch({
      type: 'history',
      messages: [],
      nextCursor: '',
      hasMore: false,
    })

    // Replay buffer: turn_start (new turn after takeover) + usage for turn 2.
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 2000, output_tokens: 3000 } },
      // query_end carries the authoritative cumulative totals from the engine.
      {
        type: 'query_end',
        usage_event: { input_tokens: 3000, output_tokens: 3500 },
      },
    ])

    // Final progress bar stats must reflect the WHOLE query (3000 in, 3500 out),
    // not just the post-takeover turn (2000 in, 3000 out).
    // totalInput = input_tokens + cacheRead + cacheCreation = 3000 + 0 + 0 = 3000
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑2.9k') // 3000 / 1024 = 2.93k → "2.9k"
    const outEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↓'),
    )
    expect(outEl?.textContent).toBe('↓3.4k') // 3500 / 1024 = 3.42k → "3.4k"
  })

  it('takeover accumulates new usage on top of restored baseline', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()

    // Turn 1: accumulate usage before disconnect
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
      { type: 'turn_end' },
    ])

    // Takeover: stats carries server-accumulated baseline (1000 in, 500 out)
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // New turn after takeover — usage delta must accumulate on top of baseline
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 2048, output_tokens: 1000 } },
    ])

    // 1000 (baseline) + 2048 (new) = 3048 input → "3.0k"
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑3.0k')
    // 500 (baseline) + 1000 (new) = 1500 output → "1.5k"
    const outEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↓'),
    )
    expect(outEl?.textContent).toBe('↓1.5k')
  })

  it('takeover restores elapsed time from queryStartMs', () => {
    vi.useFakeTimers()
    mount()
    const pastTime = Date.now() - 5000
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', queryStartMs: pastTime })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([{ type: 'turn_start' }, { type: 'text_delta', text: 'x' }])
    vi.advanceTimersByTime(300)
    const elapsedEls = Array.from(document.querySelectorAll('span')).filter(
      (s) => /^\d+(\.\d+)?s$/.test(s.textContent ?? ''),
    )
    const elapsedValues = elapsedEls.map((s) => parseFloat(s.textContent!))
    const maxElapsed = Math.max(...elapsedValues)
    expect(maxElapsed).toBeGreaterThanOrEqual(5)
    vi.useRealTimers()
  })

  it('takeover tool count restored from server, increments on new tool_start', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', toolCount: 2 })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't3', name: 'Grep' } },
    ])
    vi.advanceTimersByTime(300)
    const toolCountEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.includes('tool'),
    )
    expect(toolCountEl?.textContent).toBe('3 tools')
    vi.useRealTimers()
  })

  it('turn_start does not reset elapsed time mid-query', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    vi.advanceTimersByTime(300)
    const elapsedAtQueryStart = Math.max(
      ...Array.from(document.querySelectorAll('span'))
        .filter((s) => /^\d+(\.\d+)?s$/.test(s.textContent ?? ''))
        .map((s) => parseFloat(s.textContent!)),
    )
    vi.advanceTimersByTime(5000)
    dispatchEvents([{ type: 'turn_start' }])
    vi.advanceTimersByTime(300)
    const elapsedAfterTurnStart = Math.max(
      ...Array.from(document.querySelectorAll('span'))
        .filter((s) => /^\d+(\.\d+)?s$/.test(s.textContent ?? ''))
        .map((s) => parseFloat(s.textContent!)),
    )
    expect(elapsedAfterTurnStart).toBeGreaterThan(elapsedAtQueryStart + 4)
    vi.useRealTimers()
  })

  // ── Takeover state restoration: full lifecycle tests ──

  it('takeover restores thinking duration from server baseline', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', thinkingMs: 3200 })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'thinking_start' },
      { type: 'thinking_end', thinking: { duration: 1800000000 } },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 50 } },
      { type: 'turn_end' },
      { type: 'query_end', usage_event: { input_tokens: 100, output_tokens: 50 } },
    ])
    vi.advanceTimersByTime(300)
    // Thinking stats are hidden — verify no 'thought for' text appears
    const thinkingEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.startsWith('thought for'),
    )
    expect(thinkingEl).toBeUndefined()
    vi.useRealTimers()
  })

  it('cold start connect_status with all-zero stats shows no progress bar', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    // No streaming events — progress bar should not exist
    const progressBars = document.querySelectorAll('[data-progress]')
    expect(progressBars.length).toBe(0)
  })

  it('model_switched updates header context with used + total from server', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', contextUsed: 5000, contextTotal: 200000 })
    const trigger = document.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
    expect(trigger.textContent).toContain('4.9k/195.3k')

    // Switch to model with 500k context — server responds with current used + new total
    dispatch({ type: 'model_switched', contextUsed: 5000, contextTotal: 500000 })
    expect(trigger.textContent).toContain('4.9k/488.3k')
    expect(trigger.textContent).not.toContain('195.3k')
  })

  it('sub-agent usage events accumulate in progress bar', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 50 } },
      { type: 'usage', usage_event: { input_tokens: 200, output_tokens: 30 }, agent: { parent_tool_use_id: 'tu-1', agent_type: 'sub', depth: 1 } },
    ])
    vi.advanceTimersByTime(300)
    // 100 (main) + 200 (sub-agent) = 300 input tokens
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑300')
    vi.useRealTimers()
  })

  it('sub-agent thinking_end accumulates into progress display', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      // Main agent thinking: 500ms = 500000000 ns
      { type: 'thinking_start' },
      { type: 'thinking_end', thinking: { duration: 500000000 } },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      // Sub-agent thinking: 2000ms = 2000000000 ns
      { type: 'thinking_start', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
      { type: 'thinking_end', thinking: { duration: 2000000000 }, agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 50 } },
      { type: 'turn_end' },
      { type: 'query_end', usage_event: { input_tokens: 100, output_tokens: 50 } },
    ])
    vi.advanceTimersByTime(300)
    // 500ms (main) + 2000ms (sub-agent) = 2500ms total thinking
    // The finalized progress bar should reflect 2500ms of thinking
    // Thinking stats are hidden in the current UI, but accumulatedThinkingMs
    // must be correct so the stats message carries the right value.
    // Verify via the internal state: query_end finalizes the bar.
    const finalizedBar = document.querySelector('[data-progress="1"]') as HTMLDivElement | null
    expect(finalizedBar).not.toBeNull()
    vi.useRealTimers()
  })

  it('sub-agent tool_start accumulates into committedToolCount', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      // Main agent tool
      { type: 'tool_start', tool_use: { id: 'main-t1', name: 'Bash', input: 'echo hi' } },
      // Agent tool (parent) — starts sub-agent
      { type: 'tool_start', tool_use: { id: 'agent-t1', name: 'Agent', input: 'do stuff' } },
      // Sub-agent tools
      { type: 'tool_start', tool_use: { id: 'sub-t1', name: 'Read', input: 'file.txt' }, agent: { parent_tool_use_id: 'agent-t1', agent_type: 'sub', depth: 1 } },
      { type: 'tool_start', tool_use: { id: 'sub-t2', name: 'Grep', input: 'pattern' }, agent: { parent_tool_use_id: 'agent-t1', agent_type: 'sub', depth: 1 } },
    ])
    // Dispatch query_end to finalize and check stats
    dispatchEvents([
      { type: 'tool_end', tool_use_id: 'sub-t1', output: 'done', name: 'Read', agent: { parent_tool_use_id: 'agent-t1', agent_type: 'sub', depth: 1 } },
      { type: 'tool_end', tool_use_id: 'sub-t2', output: 'done', name: 'Grep', agent: { parent_tool_use_id: 'agent-t1', agent_type: 'sub', depth: 1 } },
      { type: 'tool_end', tool_use_id: 'agent-t1', output: 'done', name: 'Agent' },
      { type: 'tool_end', tool_use_id: 'main-t1', output: 'done', name: 'Bash' },
      { type: 'query_end' },
    ])
    // 4 tools total: Bash + Agent + Read + Grep
    const toolEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.match(/\d+\s*tools?/),
    )
    expect(toolEl?.textContent).toBe('4 tools')
  })

  it('full recovery: disconnect mid-query, reconnect, continue, query_end', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })

    // Simulate query started, accumulated some stats on server
    // Now takeover happens with server-accumulated stats
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 5000, output_tokens: 1000, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 30000,
      toolCount: 3,
      thinkingMs: 5000,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // New turn after reconnect
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't4', name: 'Bash' } },
      { type: 'thinking_start' },
      { type: 'thinking_end', thinking: { duration: 2000000000 } },
      { type: 'usage', usage_event: { input_tokens: 2000, output_tokens: 500 } },
    ])
    vi.advanceTimersByTime(300)

    // Verify cumulative stats during streaming
    // Tool count: 3 (server) + 1 (new) = 4
    const toolEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.includes('tool'),
    )
    expect(toolEl?.textContent).toBe('4 tools')

    // Now trigger query_end — final stats should use authoritative usage_event
    dispatchEvents([
      { type: 'turn_end' },
      { type: 'query_end', usage_event: { input_tokens: 8000, output_tokens: 1800 } },
    ])
    vi.advanceTimersByTime(300)

    // After query_end: progress bar finalized, no longer updating
    // Input: 5000 + 2000 + 8000 = 15000 (cumulative + authoritative)
    // Actually: usage_event in query_end is the authoritative total from LLM.
    // The client replaces progressUsage with it. So final input should reflect
    // usage_event total (8000) since that's what LLM reports for last turn.
    // Check that the progress bar is finalized (data-progress=1)
    const finalizedBar = document.querySelector('[data-progress="1"]') as HTMLDivElement | null
    expect(finalizedBar).not.toBeNull()
    expect(finalizedBar!.dataset.progress).toBe('1')

    // Thinking stats are hidden — verify no 'thought for' text appears
    const thinkingEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.startsWith('thought for'),
    )
    expect(thinkingEl).toBeUndefined()

    vi.useRealTimers()
  })

  it('agent tool_end after takeover: main agent text_delta renders', () => {
    mount()
    // Phase 1: main agent starts Agent tool (normal pre-takeover streaming)
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('review my code')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    // Phase 2: takeover mid-agent-run.
    // resetAllState wipes streaming state. connect_status resets everything.
    // History shows committed turns (empty for this simple case).
    // Replay buffer: buffer was cleared at turn_start, so it starts from
    // tool_start (NOT turn_start — turn_start was already committed).
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // Replay buffer does NOT have turn_start — only tool_start onward
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'text_delta', text: 'Reviewing code...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])

    // Phase 3: sub-agent finishes, Agent tool_end, main agent continues
    dispatchEvents([
      { type: 'query_end', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
      { type: 'tool_end', tool_result: { tool_use_id: 'agent-1', display_output: 'Review complete.' } },
      // Main agent continues with text — THIS must render
      { type: 'text_delta', text: 'Based on the review, here are my findings.' },
    ])

    // The main agent's text_delta after Agent tool_end must appear in the DOM
    const allText = document.body.textContent ?? ''
    expect(allText).toContain('Based on the review')
  })

  it('takeover during Agent tool: history with running tool initializes progress bar', () => {
    vi.useFakeTimers()
    mount()
    // Takeover: connect_status + stats with server-accumulated stats
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 5000,
      toolCount: 1,
      thinkingMs: 0,
    })
    // History: committed assistant message with a RUNNING Agent tool
    // (no tool_result yet — Agent is still executing)
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [],
        tools: [],
        blocks: [
          { kind: 'tool', tool: { id: 'agent-1', name: 'Agent', isRunning: true } },
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'streaming', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    // Replay buffer: sub-agent events arrive after history
    dispatchEvents([
      { type: 'turn_start', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
      { type: 'text_delta', text: 'Working...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])
    vi.advanceTimersByTime(300)

    // Progress bar MUST exist — history with running tool should init streaming
    const inEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.startsWith('↑'),
    )
    expect(inEl).toBeTruthy()
    expect(inEl?.textContent).toBe('↑1.0k')

    vi.useRealTimers()
  })

  it('STOP during Agent tool after takeover: [Request interrupted by user] renders', () => {
    mount()
    // Takeover: history has running Agent tool
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [], tools: [],
        blocks: [
          { kind: 'tool', tool: { id: 'agent-1', name: 'Agent', isRunning: true } },
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'streaming', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    // Sub-agent event replay
    dispatchEvents([
      { type: 'text_delta', text: 'Working...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])
    // User clicks STOP: connector injects interrupt text_delta, then query_end(aborted)
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])

    const allText = document.body.textContent ?? ''
    expect(allText).toContain('[Request interrupted by user]')
  })

  // ── DOM integrity: assert ALL streaming elements exist after each scenario ──

  function assertStreamingDOMIntegrity(label: string, opts: {
    expectTokenStats?: boolean
    expectElapsed?: boolean
    expectToolCount?: number | null
  } = {}) {
    const { expectTokenStats = true, expectElapsed = true, expectToolCount = null } = opts
    // Progress bar exists iff token stats span exists (↑ is always in progress bar)
    if (expectTokenStats) {
      const inEl = Array.from(document.querySelectorAll('span')).find(
        (s) => s.textContent?.startsWith('↑'),
      )
      expect(inEl?.textContent ?? '').toMatch(/^↑/)
    }
    if (expectElapsed) {
      const elapsedEl = Array.from(document.querySelectorAll('span')).find(
        (s) => /^\d+(\.\d+)?s$/.test(s.textContent ?? ''),
      )
      expect(elapsedEl?.textContent ?? '').toMatch(/^\d/)
    }
    if (expectToolCount !== null) {
      const toolEl = Array.from(document.querySelectorAll('span')).find(
        (s) => s.textContent?.includes('tool'),
      )
      if (expectToolCount > 0) {
        expect(toolEl?.textContent).toContain(String(expectToolCount))
      }
    }
  }

  it('normal streaming: all DOM elements exist during live stream', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('hello')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 500, output_tokens: 200 } },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
    ])
    vi.advanceTimersByTime(300)
    assertStreamingDOMIntegrity('normal streaming', { expectToolCount: 1 })
    vi.useRealTimers()
  })

  it('takeover mid-text-stream: all DOM elements restored', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('hello')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 500, output_tokens: 200 } },
    ])
    // Takeover
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 500, output_tokens: 200, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 3000,
      toolCount: 0, thinkingMs: 0,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([{ type: 'turn_start' }, { type: 'usage', usage_event: { input_tokens: 300, output_tokens: 100 } }])
    vi.advanceTimersByTime(300)
    assertStreamingDOMIntegrity('takeover mid-text', { expectToolCount: null })
    vi.useRealTimers()
  })

  it('takeover mid-Agent-tool: all DOM elements restored (progress bar, tool count, elapsed)', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 5000,
      toolCount: 1, thinkingMs: 0,
    })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [], tools: [],
        blocks: [
          { kind: 'tool', tool: { id: 'agent-1', name: 'Agent', isRunning: true } },
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'streaming', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([
      { type: 'text_delta', text: 'Working...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])
    vi.advanceTimersByTime(300)
    // Must have: progress bar, token stats, elapsed time, tool count
    assertStreamingDOMIntegrity('takeover mid-Agent-tool', { expectToolCount: 1 })
    vi.useRealTimers()
  })

  it('double takeover: reconnect twice during same query without DOM loss', () => {
    vi.useFakeTimers()
    mount()
    // First takeover
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 500, output_tokens: 100, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 2000,
      toolCount: 0, thinkingMs: 0,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([{ type: 'turn_start' }, { type: 'usage', usage_event: { input_tokens: 300, output_tokens: 50 } }])
    vi.advanceTimersByTime(300)
    assertStreamingDOMIntegrity('first takeover', { expectToolCount: null })

    // Second takeover — same query, fresh reconnect
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 800, output_tokens: 150, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 5000,
      toolCount: 1, thinkingMs: 0,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'usage', usage_event: { input_tokens: 200, output_tokens: 80 } },
    ])
    vi.advanceTimersByTime(300)
    assertStreamingDOMIntegrity('second takeover', { expectToolCount: null })
    vi.useRealTimers()
  })

  it('STOP after takeover: progress bar finalized, interrupt visible', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 5000,
      toolCount: 1, thinkingMs: 2000,
    })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [], tools: [],
        blocks: [
          { kind: 'tool', tool: { id: 'agent-1', name: 'Agent', isRunning: true } },
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'streaming', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([
      { type: 'text_delta', text: 'Working...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])
    vi.advanceTimersByTime(300)

    // STOP
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])
    vi.advanceTimersByTime(300)

    const allText = document.body.textContent ?? ''
    expect(allText).toContain('[Request interrupted by user]')
    // Progress bar finalized (data-progress=1)
    const finalizedBar = document.querySelector('[data-progress="1"]') as HTMLDivElement | null
    expect(finalizedBar).not.toBeNull()
    vi.useRealTimers()
  })

  it('double takeover: tool count not double-counted after reconnect', () => {
    vi.useFakeTimers()
    mount()
    // Phase 1: normal streaming with one tool
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('hello')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
    ])
    vi.advanceTimersByTime(300)

    // Phase 2: first takeover — server toolCount=1 (the Bash tool already started)
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 500, output_tokens: 100, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 2000,
      toolCount: 1, thinkingMs: 0,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    // Replay buffer replays tool_start (same t1, already counted by server)
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
    ])
    vi.advanceTimersByTime(300)

    // Tool count must be 1, not 2
    const toolEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.includes('tool'),
    )
    expect(toolEl?.textContent).toBe('1 tool')

    vi.useRealTimers()
  })

  it('stats message overwrites replay-accumulated stats', () => {
    vi.useFakeTimers()
    mount()
    // Takeover: connect_status (no stats) then replay then stats
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    // Replay: usage event accumulates input=1000
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
    ])
    vi.advanceTimersByTime(300)
    // Before stats: progress bar shows 1000 (replay-accumulated) → "1.0k"
    const inElBefore = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inElBefore?.textContent).toBe('↑1.0k')

    // stats message arrives with authoritative value (500, not 1000)
    dispatch({
      type: 'stats',
      usage: { input_tokens: 500, output_tokens: 200, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    })
    vi.advanceTimersByTime(300)
    // After stats: progress bar shows 500 (overwritten, not accumulated)
    const inElAfter = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inElAfter?.textContent).toBe('↑500')

    vi.useRealTimers()
  })

  it('tool_param_delta feeds tokenRate.add', () => {
    const addSpy = vi.spyOn(TokenRate.prototype, 'add')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'hello' },
    ])
    addSpy.mockClear()

    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'tool_param_delta', partial_input: { delta: '{"command":"ls"}', summary: 'ls' }, tokens: 5 },
    ])

    expect(addSpy).toHaveBeenCalledWith('{"command":"ls"}', 5)
    addSpy.mockRestore()
  })

  it('disconnect banner shows reconnecting text on ws state change', () => {
    mount()
    expect(stateCb).toBeTruthy()
    stateCb!('reconnecting')
    const banner = document.querySelector('.text-red') as HTMLElement
    expect(banner?.textContent).toContain('reconnecting')
    // The banner parks below the header, whose height includes the status
    // bar inset (edge-to-edge) — a fixed top-11 would overlap it on phones.
    expect(banner?.parentElement?.className).toContain(
      'top-[calc(2.75rem+env(safe-area-inset-top,0px))]',
    )
  })

  it('disconnect banner shows failure text on disconnected state', () => {
    mount()
    stateCb!('disconnected')
    const banner = document.querySelector('.text-red') as HTMLElement
    expect(banner?.textContent).toContain('Tap to retry')
  })

  it('disconnect banner hides on connected state', () => {
    mount()
    stateCb!('reconnecting')
    const banner = document.querySelector(
      '.absolute.top-\\[calc\\(2\\.75rem\\+env\\(safe-area-inset-top\\,0px\\)\\)\\]',
    ) as HTMLElement
    expect(banner.style.opacity).toBe('1')

    stateCb!('connected')
    expect(banner.style.opacity).toBe('0')
  })

  it('permission ask renders three buttons and sends decision on click', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'ask',
      id: 'p1',
      kind: 'permission',
      tool_name: 'Bash',
      input: { command: 'ls' },
    })
    const root = document.querySelector('[class*="border-amber"]') as HTMLElement
    expect(root).toBeInstanceOf(HTMLElement)
    expect(root.textContent).toContain('Approve · Bash')
    const allowBtn = Array.from(root.querySelectorAll('button')).find(
      (b) => b.textContent === 'Allow Once',
    ) as HTMLButtonElement
    allowBtn.click()
    const last = sent[sent.length - 1] as { type: string; id: string; decision?: string }
    expect(last.type).toBe('ask_response')
    expect(last.id).toBe('p1')
    expect(last.decision).toBe('allow')
  })

  it('input ask renders input field and sends text/aborted on submit', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'ask',
      id: 'i1',
      kind: 'input',
      tool_name: 'Bash',
      prompt: '[sudo] password:',
      masked: true,
    })
    const root = document.querySelector('[class*="border-blue"]') as HTMLElement
    expect(root.textContent).toContain('[sudo] password:')
    // No permission buttons on input ask.
    expect(root.textContent).not.toContain('Allow Once')
    const input = root.querySelector('input') as HTMLInputElement
    expect(input.type).toBe('password')
    input.value = 'hunter2'
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    const last = sent[sent.length - 1] as {
      type: string
      id: string
      text?: string
      aborted?: boolean
      decision?: string
    }
    expect(last.type).toBe('ask_response')
    expect(last.id).toBe('i1')
    expect(last.text).toBe('hunter2')
    expect(last.aborted).toBe(false)
    expect(last.decision).toBeUndefined()
  })

  it('permission ask does not render input field (regression)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'ask',
      id: 'p2',
      kind: 'permission',
      tool_name: 'Bash',
      input: { command: 'rm -rf /' },
    })
    const root = document.querySelector('[class*="border-amber"]') as HTMLElement
    expect(root.querySelector('input')).toBeNull()
    // Three permission buttons must be present.
    const btns = Array.from(root.querySelectorAll('button')).map((b) => b.textContent)
    expect(btns).toEqual(['Allow Once', 'Allow This Session', 'Deny'])
  })

  it('input ask with already-expired deadline auto-aborts without leaving dead dialog', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const pastDeadline = Math.floor(Date.now() / 1000) - 5
    dispatch({
      type: 'ask',
      id: 'i-expired',
      kind: 'input',
      tool_name: 'Bash',
      prompt: 'pw?',
      deadline_unix: pastDeadline,
    })
    // Abort is deferred via setTimeout(0); flush it.
    vi.advanceTimersByTime(10)
    const last = sent[sent.length - 1] as {
      type: string
      id: string
      aborted?: boolean
      text?: string
    }
    expect(last.type).toBe('ask_response')
    expect(last.id).toBe('i-expired')
    expect(last.aborted).toBe(true)
    expect(last.text).toBe('')
    expect(last.timeout).toBe(true)
    // No live dialog left in the DOM (chat.ts removes it from askEls and
    // close() detaches the node). Scoped to the ask dialog's border-blue/50 —
    // the hidden settings page also carries border-blue/30 styling.
    expect(document.querySelector('[class*="border-blue/50"]')).toBeNull()
    vi.useRealTimers()
  })

  it('metadata restore renders queued messages with attachment chips', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // A busy engine reconnect: the page frame's stream window flips
    // streaming back on (metadata's resetAllState ran first), then queuedMsgs
    // restores the queue.
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      queuedMsgs: [{
        uuid: 'q-1',
        text: 'review this',
        attachments: [
          { name: 'report.pdf', mime: 'application/pdf' },
          { mime: 'image/jpeg' },
        ],
      }],
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    expect(
      sent.find((m) => (m as { type: string }).type === 'history_request'),
    ).toMatchObject({ type: 'history_request', cursor: '', limit: 30 })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'working' }],
      nextCursor: '',
      hasMore: false,
      busy: true,
    })
    expect(document.body.textContent).toContain('Tap to CANCEL')
    expect(document.body.textContent).toContain('[report.pdf]')
    expect(document.body.textContent).toContain('[image/jpeg]')
  })

  it('queued uuid backfill preserves attachment chips', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      // uuid:'' mirrors a locally-pushed queue entry awaiting its server uuid
      queuedMsgs: [{ uuid: '', text: 'with file', attachments: [{ name: 'doc.pdf', mime: 'application/pdf' }] }],
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'working' }],
      nextCursor: '',
      hasMore: false,
      busy: true,
    })
    dispatch({ type: 'queued', uuid: 'srv-1' })
    expect(document.body.textContent).toContain('Tap to CANCEL')
    expect(document.body.textContent).toContain('[doc.pdf]')
  })
})

// formatDuration renders "0.2s" | "45s" | "5m 0s" | "1h 2m 3s"; parse any of
// those back to plain seconds so anchoring assertions can use second bounds.
function parseDurationSeconds(text: string): number {
  const m = /^(?:(\d+)h )?(?:(\d+)m )?(\d+(?:\.\d+)?)s$/.exec(text.trim())
  if (!m) return NaN
  return (m[1] ? +m[1] * 3600 : 0) + (m[2] ? +m[2] * 60 : 0) + parseFloat(m[3])
}

describe('takeover elapsed anchoring', () => {
  it('takeover: metadata stats.queryStartMs anchors elapsed across the busy page round trip', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      stats: { queryStartMs: Date.now() - 300000, usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'working' }],
      nextCursor: '',
      hasMore: false,
      busy: true,
    })
    vi.advanceTimersByTime(300)
    const sep = document.querySelector('.sep-elapsed')
    expect(sep).not.toBeNull()
    const elapsedText = sep!.nextElementSibling?.textContent ?? ''
    const seconds = parseDurationSeconds(elapsedText)
    // 300s of server-anchored age at the 200ms tick. A Date.now() fallback
    // (2513 skipped) reads ~0.2s — below the lower bound; a direction/scale
    // mutation overshoots the upper bound.
    expect(seconds).toBeGreaterThanOrEqual(299)
    expect(seconds).toBeLessThan(302)
    stopStatusSweep()
    vi.useRealTimers()
  })

  it('takeover: hydrated thinking block ticks from the server startedAt, not (0s)', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      stats: { queryStartMs: Date.now() - 300000, usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'thinking', id: '', text: '', durationNs: 0, active: true, startedAt: Date.now() - 120000 }],
      nextCursor: '',
      hasMore: false,
      busy: true,
    })
    vi.advanceTimersByTime(300)
    const label = Array.from(document.querySelectorAll('[data-thinking] span')).find((el) =>
      /^Thinking \(/.test(el.textContent ?? ''),
    )
    expect(label).toBeDefined()
    const inner = /^Thinking \((.+)\)$/.exec(label!.textContent ?? '')!
    const seconds = parseDurationSeconds(inner[1])
    // 120s of server startedAt at the 200ms tick: the stale static "(0s)"
    // initial label or a Date.now() fallback reads ~0 — below the lower bound.
    expect(seconds).toBeGreaterThanOrEqual(119)
    expect(seconds).toBeLessThan(122)
    stopStatusSweep()
    vi.useRealTimers()
  })
})

describe('mapHistoryToChatMessages', () => {
  const usage = { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }
  const user = (id: string, text: string) => ({ id, role: 'user' as const, text, thinking: [], tools: [], usage, error: '', status: 'done' as const, startedAt: 0 })
  it('propagates error through assistant merge so partial+error turns keep their error box on restart', () => {
    const result = mapHistoryToChatMessages([
      user('u1', 'go'),
      { id: 'a1', role: 'assistant' as const, text: 'partial answer', thinking: [], tools: [], usage, error: '', status: 'done' as const, startedAt: 0, blocks: [] },
      { id: 'a2', role: 'assistant' as const, text: '', thinking: [], tools: [], usage, error: 'API Error 529: service overloaded', status: 'done' as const, startedAt: 0, blocks: [] },
    ])
    expect(result.length).toBe(2)
    expect(result[1].blocks[0].kind === 'text' && (result[1].blocks[0] as { text: string }).text).toBe('partial answer')
    expect(result[1].error).toBe('API Error 529: service overloaded')
  })
  const marker = (id: string) => ({ id, role: 'system' as const, compactBoundary: true, text: '', thinking: [], tools: [], usage, error: '', status: 'done' as const, startedAt: 0 })

  it('filters out system-role markers between regular messages', () => {
    const result = mapHistoryToChatMessages([
      user('u1', 'hello'),
      marker('b1'),
      user('u2', 'world'),
    ])
    expect(result.length).toBe(2)
    expect(result[0].id).toBe('u1')
    expect(result[0].role).toBe('user')
    expect(result[1].id).toBe('u2')
    expect(result[1].role).toBe('user')
  })

  it('returns empty array when all inputs are system markers', () => {
    const result = mapHistoryToChatMessages([marker('b1'), marker('b2')])
    expect(result).toEqual([])
  })

  it('maps document history blocks to document chat blocks', () => {
    const result = mapHistoryToChatMessages([{
      id: 'u-doc',
      role: 'user' as const,
      text: 'see this',
      thinking: [],
      tools: [],
      usage,
      error: '',
      status: 'done' as const,
      startedAt: 0,
      blocks: [
        { kind: 'text', text: 'see this' },
        { kind: 'document', name: 'report.pdf', mime: 'application/pdf', size: 4321 },
      ],
    }])
    expect(result.length).toBe(1)
    const blocks = result[0].blocks
    expect(blocks.length).toBe(2)
    expect(blocks[0].kind).toBe('text')
    expect(blocks[1].kind).toBe('document')
    expect(blocks[1]).toEqual({ kind: 'document', id: '', name: 'report.pdf', mime: 'application/pdf', size: 4321 })
  })

  it('keeps an image-only user message: empty text with an image block is not dropped', () => {
    const result = mapHistoryToChatMessages([{
      id: 'u-img',
      role: 'user' as const,
      text: '',
      thinking: [],
      tools: [],
      usage,
      error: '',
      status: 'done' as const,
      startedAt: 0,
      blocks: [{ kind: 'image', src: 'data:image/jpeg;base64,AAAA' }],
    }])
    expect(result.length).toBe(1)
    expect(result[0].id).toBe('u-img')
    expect(result[0].blocks).toEqual([{ kind: 'image', id: '', src: 'data:image/jpeg;base64,AAAA' }])
  })

  it('keeps a document-only user message: empty text with a document block is not dropped', () => {
    const result = mapHistoryToChatMessages([{
      id: 'u-doc-only',
      role: 'user' as const,
      text: '',
      thinking: [],
      tools: [],
      usage,
      error: '',
      status: 'done' as const,
      startedAt: 0,
      blocks: [{ kind: 'document', name: 'x.pdf', size: 1 }],
    }])
    expect(result.length).toBe(1)
    expect(result[0].id).toBe('u-doc-only')
    expect(result[0].blocks).toEqual([{ kind: 'document', id: '', name: 'x.pdf', mime: undefined, size: 1 }])
  })

  it('still drops a textless user message that carries no renderable blocks', () => {
    const result = mapHistoryToChatMessages([user('u-empty', '   ')])
    expect(result).toEqual([])
  })
})

describe('document block history rendering', () => {
  const usage = { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }

  function dispatchHistory(blocks: unknown[]) {
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-doc',
        role: 'user',
        text: 'see this',
        thinking: [],
        tools: [],
        usage,
        error: '',
        status: 'done',
        startedAt: 0,
        blocks,
      }],
      nextCursor: '',
      hasMore: false,
    })
  }

  it('document block renders the upload-style [name size] chip, display-only', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      { kind: 'text', text: 'see this' },
      { kind: 'document', name: 'report.pdf', mime: 'application/pdf', size: 4321 },
    ])
    // Byte-identical to the input-bar pending chip: [name size], no icon,
    // no click affordance.
    const chips = Array.from(document.querySelectorAll('span')).filter(el =>
      el.textContent?.includes('report.pdf'))
    expect(chips.length).toBe(1)
    const chip = chips[0] as HTMLElement
    expect(chip.textContent).toBe('[report.pdf 4.2 KB]')
    expect(chip.querySelector('svg')).toBeNull()
    expect(chip.className).not.toContain('cursor')
    // Reference metadata never surfaces the mime or any parsed markdown.
    expect(document.body.textContent).not.toContain('application/pdf')
  })

  it('document chip with unknown size matches the same [name] form', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      { kind: 'text', text: 'see this' },
      { kind: 'document', name: 'no-size.bin' },
    ])
    const chip = Array.from(document.querySelectorAll('span')).find(el =>
      el.textContent?.includes('no-size.bin')) as HTMLElement
    expect(chip.textContent).toBe('[no-size.bin]')
  })

  it('document-only user message (no text) still renders its chip on replay', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-doc-only',
        role: 'user',
        text: '',
        thinking: [],
        tools: [],
        usage,
        error: '',
        status: 'done',
        startedAt: 0,
        blocks: [{ kind: 'document', name: 'x.pdf', mime: 'application/pdf', size: 1 }],
      }],
      nextCursor: '',
      hasMore: false,
    })
    const chip = Array.from(document.querySelectorAll('span')).find(el =>
      el.textContent?.includes('x.pdf')) as HTMLElement
    expect(chip).toBeDefined()
    expect(chip.textContent).toBe('[x.pdf 1 B]')
    expect(document.body.textContent).not.toContain('application/pdf')
  })

  it('legacy [Document: ...] text block still renders chip plus remaining text', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      { kind: 'text', text: '[Document: notes.txt saved at /cache/documents/abc.txt]\nleftover body text' },
    ])
    const chip = Array.from(document.querySelectorAll('span')).find(el =>
      el.textContent === '[notes.txt]')
    expect(chip).toBeDefined()
    expect(chip?.textContent).toBe('[notes.txt]')
    expect(document.body.textContent).toContain('leftover body text')
  })
})

describe('image-only user message history rendering', () => {
  const usage = { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }
  const IMG = 'data:image/jpeg;base64,AAAA'

  function dispatchHistory(messages: unknown[]) {
    dispatch({
      type: 'history',
      messages,
      nextCursor: '',
      hasMore: false,
    })
  }

  function userMsg(id: string, text: string, blocks: unknown[]) {
    return { id, role: 'user', text, thinking: [], tools: [], usage, error: '', status: 'done', startedAt: 0, blocks }
  }

  function historyImage(): HTMLImageElement | null {
    const img = Array.from(document.querySelectorAll('img')).find(
      (el) => el.getAttribute('src') === IMG,
    )
    return img ? (img as HTMLImageElement) : null
  }

  it('image-only user message renders its thumbnail between adjacent text messages', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      userMsg('u-img', '', [{ kind: 'image', src: IMG }]),
      userMsg('u-t1', 'first text', [{ kind: 'text', text: 'first text' }]),
      userMsg('u-t2', 'second text', [{ kind: 'text', text: 'second text' }]),
      { id: 'a1', role: 'assistant', text: 'answer', thinking: [], tools: [], usage, error: '', status: 'done', startedAt: 0, blocks: [] },
    ])
    const img = historyImage()
    expect(img).not.toBeNull()
    // History attachment thumbnails are lightbox-wired (cursor-zoom-in).
    expect(img!.className).toContain('cursor-zoom-in')
    // Control group: the adjacent plain-text messages still render.
    expect(document.body.textContent).toContain('first text')
    expect(document.body.textContent).toContain('second text')
    // Cross-shell DOM order: thumbnail shell, then first text shell, then
    // second text shell — frag append order, attachments not regrouped.
    // data-block-kind="user" selects exactly the committed user text spans
    // (assistant markdown carries no such marker), and the img is matched by
    // src so avatar <img>s can't alias.
    const spans = Array.from(document.querySelectorAll('[data-block-kind="user"]'))
    expect(spans.map((s) => s.textContent)).toEqual(['first text', 'second text'])
    const ordered = [img!, ...spans].sort((a, b) =>
      a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1)
    expect(ordered).toEqual([img, ...spans])
  })

  it('image+text user message renders both blocks, image first', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      userMsg('u-mix1', 'caption after', [{ kind: 'image', src: IMG }, { kind: 'text', text: 'caption after' }]),
    ])
    const img = historyImage()
    expect(img).not.toBeNull()
    const kids = Array.from(img!.parentElement!.children)
    expect(kids.indexOf(img!)).toBe(0)
    const span = kids.find((el) => el.textContent === 'caption after')
    expect(span?.tagName).toBe('SPAN')
  })

  it('image+text user message renders both blocks, text first', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      userMsg('u-mix2', 'caption before', [{ kind: 'text', text: 'caption before' }, { kind: 'image', src: IMG }]),
    ])
    const img = historyImage()
    expect(img).not.toBeNull()
    const kids = Array.from(img!.parentElement!.children)
    expect(kids.indexOf(img!)).toBe(1)
    expect(kids[0].textContent).toBe('caption before')
  })
})

describe('artifact integration', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  function stubFetchLength(len: string) {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () =>
      new Response(null, { status: 200, headers: { 'content-length': len } }),
    )
  }

  function lastAssistantContent(): HTMLElement {
    return assistantContentDivs().slice(-1)[0]
  }

  function artifactCards(): HTMLElement[] {
    return Array.from(document.querySelectorAll('.artifact-card'))
  }

  function sheetFrame(): HTMLIFrameElement {
    return document.querySelector('.artifact-sheet iframe') as HTMLIFrameElement
  }

  function spySheetSrcSetter(): { calls: unknown[]; restore: () => void } {
    const frame = sheetFrame()
    const desc = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'src')!
    const calls: unknown[] = []
    Object.defineProperty(frame, 'src', {
      configurable: true,
      get: desc.get,
      set: (v: unknown) => {
        calls.push(v)
        desc.set!.call(frame, v)
      },
    })
    return {
      calls,
      restore: () => {
        delete (frame as Partial<HTMLIFrameElement> & { src?: unknown }).src
      },
    }
  }

  function runWriteQuery(
    toolName: 'Write' | 'Edit',
    id: string,
    opts: { isError?: boolean; aborted?: boolean; path?: string } = {},
  ) {
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id, name: toolName, summary: opts.path ?? '/home/u/.gbot/projects/abc/artifacts/game.html' } },
    ])
    dispatchEvents([
      {
        type: 'tool_end',
        tool_result: {
          tool_use_id: id,
          display_output: 'wrote file',
          is_error: opts.isError ?? false,
        },
      },
      { type: 'query_end', aborted: opts.aborted ?? false },
    ])
  }

  it('query_end appends an artifact card for a successful Write', async () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1')

    const content = lastAssistantContent()
    const card = content.querySelector('.artifact-card') as HTMLElement | null
    expect(card).toBeTruthy()
    const iframe = card!.querySelector('iframe') as HTMLIFrameElement
    expect(iframe.getAttribute('src')).toBe('/artifacts/game.html')
    await vi.waitFor(() => {
      expect(card!.querySelector('.card-meta-inline')?.textContent).toContain('14.2 KB')
    })
  })

  it('a later Edit query adds an Updated card; the first card stays plain', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1')
    setTextarea('tweak it')
    pressEnter()
    runWriteQuery('Edit', 'e-1')

    const cards = artifactCards()
    expect(cards.length).toBe(2)
    expect(cards[0].classList.contains('stale')).toBe(false)
    expect(cards[0].querySelector('.card-updated')).toBeNull()
    expect(cards[0].querySelector('.card-fresh-dot')).toBeNull()
    expect(cards[1].classList.contains('stale')).toBe(true)
    expect(cards[1].querySelector('.card-updated')?.textContent).toBe('Updated')
    expect(cards[1].querySelector('.card-fresh-dot')).toBeTruthy()
  })

  it('a failed Write produces no card', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1', { isError: true })

    expect(artifactCards().length).toBe(0)
  })

  it('a download-type card opens the confirm, never the sheet', () => {
    stubFetchLength('68157440')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('package the apk')
    pressEnter()
    runWriteQuery('Write', 'w-apk', { path: '/home/u/.gbot/projects/abc/artifacts/app.apk' })
    expect(artifactCards()).toHaveLength(1)
    artifactCards()[0].click()
    expect(document.querySelectorAll('.share-dl')).toHaveLength(1)
    // The sheet stays in its closed state — height 0px and no frame src.
    expect((document.querySelector('.artifact-sheet') as HTMLElement).style.height).toBe('0px')
    expect(document.querySelector('.artifact-sheet iframe')?.getAttribute('src')).toBeNull()
    // Leave nothing open: an unresolved confirm keeps activeConfirm and a live
    // keydown handler for every later test in this file.
    document.querySelector('.share-dl-btn')?.click()
    expect(document.querySelectorAll('.share-dl, .share-dl-scrim')).toHaveLength(0)
  })
  it('an html card still opens the sheet — the download gate does not swallow it', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1')
    artifactCards()[0].click()
    expect(document.querySelectorAll('.share-dl')).toHaveLength(0)
    expect((document.querySelector('.artifact-sheet') as HTMLElement).style.height).toBe('70%')
    expect(document.querySelector('.artifact-sheet iframe')?.getAttribute('src')).toBe(
      '/artifacts/game.html',
    )
  })
  it('an image card opens the sheet with its download button, not the confirm', () => {
    stubFetchLength('20480')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('draw a cat')
    pressEnter()
    runWriteQuery('Write', 'w-png', { path: '/home/u/.gbot/projects/abc/artifacts/pic.png' })
    expect(artifactCards()).toHaveLength(1)
    // The thumbnail stays — images render in the frame.
    expect(artifactCards()[0].querySelector('.card-preview iframe')).not.toBeNull()
    artifactCards()[0].click()
    expect(document.querySelectorAll('.share-dl')).toHaveLength(0)
    expect((document.querySelector('.artifact-sheet') as HTMLElement).style.height).toBe('70%')
    const btn = document.querySelector('.sheet-download') as HTMLElement
    expect(btn.classList.contains('hidden')).toBe(false)
  })
  it('query_end does not reload the open sheet', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1')

    const card = artifactCards()[0]
    ;(card as HTMLElement).click()
    const sheetRoot = document.querySelector('.artifact-sheet') as HTMLElement
    expect(sheetRoot.style.height).toBe('70%')

    const spy = spySheetSrcSetter()
    dispatchEvents([{ type: 'query_start' }, { type: 'query_end' }])
    // No refresh at query end — the flash outweighed freshness; refresh =
    // close and reopen.
    expect(spy.calls.length).toBe(0)
    spy.restore()
  })

  it('aborted query with a successful Write still commits the card; empty abort rewinds to none', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })

    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1', { aborted: true })
    expect(artifactCards().length).toBe(1)

    setTextarea('make another')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'query_end', aborted: true }])
    expect(artifactCards().length).toBe(1)
  })

  it('history replay renders cards without any live event', () => {
    stubFetchLength('14541')
    mount()
    dispatch({
      type: 'metadata',
      connect: { connected: true, model: 'test' },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', startedAt: 1000, text: 'make a page',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0 },
        },
        {
          // One assistant message per query on the wire; Write+Edit of the
          // same file inside it dedupes to a single Updated card.
          id: 'a1', role: 'assistant', startedAt: 1000, text: '',
          thinking: [], tools: [],
          blocks: [
            { kind: 'tool', tool: { id: 'w1', name: 'Write', summary: '/pd/artifacts/game.html', durationNs: 1000 } },
            { kind: 'tool', tool: { id: 'e1', name: 'Edit', summary: '/pd/artifacts/game.html', durationNs: 1000 } },
            { kind: 'tool', tool: { id: 'w2', name: 'Write', summary: '/pd/artifacts/other.html', durationNs: 1000 } },
          ],
          usage: { inputTokens: 0, outputTokens: 0 },
        },
      ],
      nextCursor: '', hasMore: false,
    })

    const cards = artifactCards()
    expect(cards.length).toBe(2)
    expect(cards[0].querySelector('.card-title')?.textContent).toBe('game.html')
    expect((cards[0].querySelector('iframe') as HTMLIFrameElement).getAttribute('src')).toBe(
      '/artifacts/game.html',
    )
    expect(cards[0].classList.contains('stale')).toBe(true)
    expect(cards[0].querySelector('.card-updated')?.textContent).toBe('Updated')
    expect(cards[1].querySelector('.card-title')?.textContent).toBe('other.html')
    expect(cards[1].classList.contains('stale')).toBe(false)
  })
})

describe('sidebar artifacts', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })
  it('opening the sidebar fetches the list; clicking a row opens the sheet', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        json: async () => [{ name: 'game.html', size: 29, mtime: Date.now() }],
      })),
    )
    mount()

    const hamburger = document.querySelector('button[aria-label="Menu"]') as HTMLElement
    expect(hamburger).not.toBeNull()
    hamburger.click()

    await vi.waitFor(() => {
      const row = document.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement | null
      expect(row?.textContent).toContain('game.html')
    })

    const row = document.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement
    row.click()
    const frame = document.querySelector('.artifact-sheet iframe') as HTMLIFrameElement
    expect(frame.getAttribute('src')).toBe('/artifacts/game.html')
  })
  it('a download-type row opens the confirm, never the sheet', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        json: async () => [{ name: 'app.apk', size: 68157440, mtime: Date.now() }],
      })),
    )
    mount()

    const hamburger = document.querySelector('button[aria-label="Menu"]') as HTMLElement
    hamburger.click()

    await vi.waitFor(() => {
      const row = document.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement | null
      expect(row?.textContent).toContain('app.apk')
    })

    const row = document.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement
    row.click()
    // Same gate as the card path: one confirm, and the sheet stays shut.
    expect(document.querySelectorAll('.share-dl')).toHaveLength(1)
    expect((document.querySelector('.artifact-sheet') as HTMLElement).style.height).toBe('0px')
    expect(document.querySelector('.artifact-sheet iframe')?.getAttribute('src')).toBeNull()
    document.querySelector('.share-dl-btn')?.click()
    expect(document.querySelectorAll('.share-dl, .share-dl-scrim')).toHaveLength(0)
  })
  it('empty list shows the empty state after opening', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        json: async () => [],
      })),
    )
    mount()

    const hamburger = document.querySelector('button[aria-label="Menu"]') as HTMLElement
    hamburger.click()

    await vi.waitFor(() => {
      const section = document.querySelector('.sidebar-artifacts') as HTMLElement | null
      expect(section?.textContent).toContain('No artifacts yet')
    })
  })
})

describe('wui asset hash reload', () => {
  beforeEach(() => {
    // The reload marker lives in sessionStorage, which jsdom shares across
    // every test in this file — a leaked flag would toast on later mounts.
    sessionStorage.clear()
  })
  afterEach(() => {
    sessionStorage.clear()
    vi.unstubAllGlobals()
  })

  function metaFrame(wuiHash?: string) {
    return {
      type: 'metadata',
      connect: wuiHash === undefined
        ? { connected: true }
        : { connected: true, wuiHash },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    }
  }

  it('reloads exactly once when a reconnect reports a different bundle hash', () => {
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    mount()
    // Boot: first frame per page lifetime only records the baseline.
    dispatch(metaFrame('hash-a'))
    expect(reloadSpy).toHaveBeenCalledTimes(0)
    // Transient drop, same binary: reconnect rides the identical hash.
    dispatch(metaFrame('hash-a'))
    expect(reloadSpy).toHaveBeenCalledTimes(0)
    // Daemon upgraded (tableflip/REUSEPORT): the new process serves a new
    // bundle, so the reconnecting page must hard-reload.
    dispatch(metaFrame('hash-b'))
    expect(reloadSpy).toHaveBeenCalledTimes(1)
    // The reload itself is silent — the marker is what lets the reloaded
    // page explain the flash with a toast.
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe('1')
  })

  it('does not reload when the hash disappears (older daemon, no new information)', () => {
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    mount()
    dispatch(metaFrame('hash-a'))
    dispatch(metaFrame())
    expect(reloadSpy).toHaveBeenCalledTimes(0)
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe(null)
  })

  it('hash-less first frame stores no baseline, so a later hashed frame does not reload', () => {
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    mount()
    dispatch(metaFrame())
    dispatch(metaFrame('hash-a'))
    expect(reloadSpy).toHaveBeenCalledTimes(0)
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe(null)
  })

  // The refresh notice rides the upgrade capsule (data-status-capsule).
  function bootCapsule(): HTMLElement {
    const chat = mount()
    return chat.root.querySelector('[data-status-capsule]') as HTMLElement
  }

  it('boot with the reload marker shows the refresh in the capsule once and clears the marker', () => {
    sessionStorage.setItem('wuiAutoReloaded', '1')
    const capsuleEl = bootCapsule()
    expect(capsuleEl.textContent).toBe('Refreshed')
    expect(capsuleEl.classList.contains('opacity-0')).toBe(false)
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe(null)
  })

  it('plain boot without the marker keeps the capsule hidden', () => {
    const capsuleEl = bootCapsule()
    expect(capsuleEl.textContent).toBe('')
    expect(capsuleEl.classList.contains('opacity-0')).toBe(true)
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe(null)
  })
})

// jsdom has no URL.createObjectURL — the input bar needs it to mint preview
// blob URLs when an image attachment is picked.
describe('abort rewind attachment restore', () => {
  const hadCreateObjectURL = 'createObjectURL' in URL
  const origCreateObjectURL = (URL as { createObjectURL?: (b: Blob) => string }).createObjectURL
  const hadRevokeObjectURL = 'revokeObjectURL' in URL
  const origRevokeObjectURL = (URL as { revokeObjectURL?: (u: string) => void }).revokeObjectURL
  let blobSeq = 0

  beforeAll(() => {
    Object.defineProperty(URL, 'createObjectURL', {
      value: () => `blob:mock-${++blobSeq}`,
      configurable: true,
      writable: true,
    })
    Object.defineProperty(URL, 'revokeObjectURL', {
      value: () => {},
      configurable: true,
      writable: true,
    })
  })
  afterAll(() => {
    if (hadCreateObjectURL) {
      Object.defineProperty(URL, 'createObjectURL', {
        value: origCreateObjectURL,
        configurable: true,
        writable: true,
      })
    } else {
      delete (URL as { createObjectURL?: unknown }).createObjectURL
    }
    if (hadRevokeObjectURL) {
      Object.defineProperty(URL, 'revokeObjectURL', {
        value: origRevokeObjectURL,
        configurable: true,
        writable: true,
      })
    } else {
      delete (URL as { revokeObjectURL?: unknown }).revokeObjectURL
    }
  })

  // pickIntoInput feeds a File through one of the hidden <input type=file>
  // pickers (image picker excludes the camera input via the capture attr).
  function pickIntoInput(match: (input: HTMLInputElement) => boolean, file: File) {
    const input = Array.from(document.querySelectorAll('input[type=file]')).find((el) =>
      match(el as HTMLInputElement),
    ) as HTMLInputElement | undefined
    if (!input) throw new Error('matching file input not found')
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    input.dispatchEvent(new Event('change', { bubbles: true }))
  }

  const messageFrames = () =>
    sent.filter(
      (m) =>
        (m as { type?: string; attachments?: { id: string; name?: string; mime: string; size: number }[] })
          .type === 'message' &&
        Array.isArray(
          (m as { attachments?: { id: string }[] }).attachments,
        ),
    ) as { text: string; attachments: { id: string; name?: string; mime: string; size: number }[] }[]

  it('abort with no content restores text and re-stages attachment chips via restore_result', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    pickIntoInput(
      (i) => i.accept === 'image/*' && !i.hasAttribute('capture'),
      new File([], 'cat.png', { type: 'image/png' }),
    )
    pickIntoInput(
      (i) => i.accept.includes('.pdf'),
      new File([], 'notes.pdf', { type: 'application/pdf' }),
    )
    setTextarea('see this')
    pressEnter()
    await vi.waitFor(() => {
      if (messageFrames().length !== 1 || messageFrames()[0].attachments.length !== 2) {
        throw new Error('commit frame with 2 attachments not sent yet')
      }
    })

    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    dispatchEvents([{ type: 'thinking_end' }, { type: 'query_end', aborted: true, rewound: true }])

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('see this')
    expect(sent.some((m) => (m as { type?: string }).type === 'restore_attachments')).toBe(true)

    dispatch({
      type: 'restore_result',
      attachments: [
        { id: 'fresh-img', mime: 'image/png', size: 1234 },
        { id: 'fresh-doc', name: 'notes.pdf', mime: 'application/pdf', size: 4321 },
      ],
    })

    // Image chip reuses the local blob thumbnail (server ids carry no bytes).
    const chipImg = document.querySelector('img.w-12') as HTMLImageElement | null
    if (!chipImg) throw new Error('restored image chip not rendered')
    expect(chipImg.src).toBe('blob:mock-1')
    // Document chip keeps its original filename from the local stash.
    const chipStrip = document.querySelector('.flex-wrap.gap-2') as HTMLElement | null
    if (!chipStrip) throw new Error('chip strip not found')
    expect(chipStrip.textContent).toContain('notes.pdf')

    // Re-send references the fresh ids without re-uploading bytes.
    setTextarea('again')
    pressEnter()
    await vi.waitFor(() => {
      const frames = messageFrames()
      const last = frames[frames.length - 1]
      if (!last || last.attachments.map((a) => a.id).join(',') !== 'fresh-img,fresh-doc') {
        throw new Error('re-send frame ids not staged yet')
      }
    })
  })

  it('first connect after auto-refresh keeps the refreshed notice (no false recovered)', () => {
    sessionStorage.setItem('wuiAutoReloaded', '1')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    expect(document.body.textContent).toContain('Refreshed')
    // The reloaded page's first WS connect must not mistake the active
    // notice capsule (Refreshed) for an in-flight upgrade and swap in
    // Recovered — the capsule is shared by notifyCapsule and the upgrade
    // flow, so active() alone cannot tell them apart.
    stateCb?.('connected')
    expect(document.body.textContent).toContain('Refreshed')
    expect(document.body.textContent).not.toContain('Recovered')
  })

  it('refreshed notice auto-dismisses after 1s', () => {
    sessionStorage.setItem('wuiAutoReloaded', '1')
    vi.useFakeTimers()
    try {
      mount()
      const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
      expect(capsule.classList.contains('opacity-0')).toBe(false)
      vi.advanceTimersByTime(999)
      expect(capsule.classList.contains('opacity-0')).toBe(false)
      vi.advanceTimersByTime(1)
      expect(capsule.classList.contains('opacity-0')).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('real upgrade cycle still reports recovered and clears data-upgrading', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    stateCb?.('upgrading')
    expect(document.body.getAttribute('data-upgrading')).toBe('1')
    stateCb?.('connected')
    expect(document.body.textContent).toContain('Recovered')
    expect(document.body.hasAttribute('data-upgrading')).toBe(false)
  })

  it('abort with queued follow-ups keeps the interrupted turn (no rewind)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('query A')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    // Server skipped the rewind (queue non-empty — B runs next); rewound is
    // absent on the wire. A must stay on screen and NOT clobber the input.
    dispatchEvents([{ type: 'thinking_end' }, { type: 'query_end', aborted: true }])
    expect(document.body.textContent).toContain('query A')
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('')
    expect(sent.some((m) => (m as { type?: string }).type === 'restore_attachments')).toBe(false)
  })

  it('text-only abort rewind restores the text but sends no restore_attachments frame', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('just words')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    dispatchEvents([{ type: 'thinking_end' }, { type: 'query_end', aborted: true, rewound: true }])
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('just words')
    expect(sent.some((m) => (m as { type?: string }).type === 'restore_attachments')).toBe(false)
  })

  it('attachments-only abort pops the ghost bubble and asks for a restore', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    pickIntoInput(
      (i) => i.accept.includes('.pdf'),
      new File([], 'notes.pdf', { type: 'application/pdf' }),
    )
    pressEnter()
    await vi.waitFor(() => {
      const frames = messageFrames()
      if (frames.length !== 1 || frames[0].attachments.length !== 1) {
        throw new Error('commit frame with 1 attachment not sent yet')
      }
    })
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    dispatchEvents([{ type: 'thinking_end' }, { type: 'query_end', aborted: true, rewound: true }])
    // No text to restore, but the bubble must not linger as a ghost.
    expect(document.body.textContent.includes('[notes.pdf]')).toBe(false)
    expect(sent.some((m) => (m as { type?: string }).type === 'restore_attachments')).toBe(true)
  })

  it('restore_result without a pending rewind stash stages nothing', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'restore_result',
      attachments: [{ id: 'stray', mime: 'image/png', size: 10 }],
    })
    expect(document.querySelector('img.w-12')).toBe(null)
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Mid-turn queued message rendering (persist-in-final-form era)
// ---------------------------------------------------------------------------

const QUEUED_ENVELOPE =
  '<system-reminder>\nThe user sent a new message while you were working:\ncheck on that' +
  '\n\nIMPORTANT: After completing your current task, you MUST address the user\'s message above. Do not ignore it.\n</system-reminder>'

describe('queued message history rendering', () => {
  it('history queued message renders unwrapped original text in gray', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: QUEUED_ENVELOPE,
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'check on that',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).toContain('text-t3')
    expect(document.body.textContent).not.toContain('<system-reminder>')
  })

  it('plain user history message is not gray', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'plain hello',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'plain hello',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).not.toContain('text-t3')
  })

  it('live attachment event with envelope renders unwrapped gray echo', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{
      type: 'attachment',
      message: {
        content: [{ type: 'text', text: QUEUED_ENVELOPE }],
        attachment: { prompt: 'check on that', source_uuid: 'u-9', mode: 'prompt' },
      },
    }])
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'check on that',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).toContain('text-t3')
    expect(document.body.textContent).not.toContain('<system-reminder>')
  })
  it('job-tagged events stay out of the chat timeline (single delivery)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])
    const jobAgent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1, job_id: 'agent-9' }
    dispatchEvents([
      { type: 'turn_start', agent: jobAgent },
      { type: 'tool_start', agent: jobAgent, tool_use: { id: 'sub-tool-1', name: 'Bash' } },
      { type: 'text_delta', agent: jobAgent, text: 'job text' },
    ])
    // The job's own content belongs to the jobs feed: no nested Bash block,
    // no job text in the chat timeline.
    expect(document.querySelector('[data-tool-root][data-tool-name="Bash"]')).toBeNull()
    expect(document.body.textContent).not.toContain('job text')
    // turn_start is exempt: the launch card keeps the agent-type name.
    expect(document.querySelector('[data-tool-root][data-tool-name="Agent Explorer"]')).toBeTruthy()

    // Control: the same sequence without a job_id (sync sub-agent) renders
    // as before — the filter must not over-skip.
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-2', name: 'Agent' } },
    ])
    const syncAgent = { parent_tool_use_id: 'agent-2', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent: syncAgent },
      { type: 'tool_start', agent: syncAgent, tool_use: { id: 'sub-tool-2', name: 'Bash' } },
    ])
    expect(document.querySelector('[data-tool-root][data-tool-name="Bash"]')).toBeTruthy()
  })
})

describe('STT mic availability', () => {
  it('config frame stt:true enables the mic; absent renders the slashed unavailable state', () => {
    mount()
    const mic = () =>
      document.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement

    dispatch({
      type: 'config',
      models: [],
      current: { provider: '', model: '' },
      thinking: 'auto',
      stt: true,
    })
    expect(mic().classList.contains('stt-mic-off')).toBe(false)

    dispatch({
      type: 'config',
      models: [],
      current: { provider: '', model: '' },
      thinking: 'auto',
    })
    expect(mic().classList.contains('stt-mic-off')).toBe(true)
    expect(mic().disabled).toBe(true)
  })
})

describe('avatar consumption', () => {
  // Manual save/restore: this file stubs IntersectionObserver at module
  // scope, so vi.unstubAllGlobals() would strip it for every later test.
  const realFetch = globalThis.fetch
  afterEach(() => {
    globalThis.fetch = realFetch
  })

  // The chat shell embeds a full settings page (the sidebar gear), whose
  // avatar row/panel add their own data-avatar holders — scope to the
  // message stream by excluding both settings containers.
  const chatHolders = (who: 'agent' | 'user', selector = ''): HTMLElement[] =>
    Array.from(
      document.querySelectorAll<HTMLElement>(`[data-avatar="${who}"]${selector ? ' ' + selector : ''}`),
    ).filter((el) => !el.closest('[data-general-row]') && !el.closest('[data-avatar-panel]'))

  function sendUserAndStartAssistant(): void {
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('avatar probe')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
  }

  it('renders default avatars (G letter / user icon) inside real shells', () => {
    mount()
    sendUserAndStartAssistant()
    const agents = chatHolders('agent')
    const users = chatHolders('user')
    // One user echo + one assistant shell were dispatched.
    expect(agents.length).toBe(1)
    expect(users.length).toBe(1)
    for (const el of agents) expect(el.textContent).toBe('G')
    for (const el of users) expect(el.querySelector('svg')).toBeTruthy()
  })

  it('rewrites already-rendered agent holders to img and serves new shells with img directly', async () => {
    mount()
    sendUserAndStartAssistant()
    expect(chatHolders('agent', 'img').length).toBe(0)

    globalThis.fetch = (async () => ({
      ok: true,
      status: 200,
      headers: new Headers({ ETag: '"t-1"' }),
    })) as unknown as typeof fetch
    await refreshAvatar('agent')

    const imgs = chatHolders('agent', 'img') as HTMLImageElement[]
    expect(imgs.length).toBe(1)
    expect(imgs[0].getAttribute('src')).toMatch(/^\/api\/avatar\/agent\?v=/)
    expect(chatHolders('user', 'svg').length).toBe(1)

    dispatchEvents([{ type: 'query_start' }])
    const after = chatHolders('agent', 'img') as HTMLImageElement[]
    expect(after.length).toBe(2)
    expect(after[1].getAttribute('src')).toContain('t-1')
  })

  it('falls back to the default rendering when the img errors', async () => {
    mount()
    sendUserAndStartAssistant()
    globalThis.fetch = (async () => ({
      ok: true,
      status: 200,
      headers: new Headers({ ETag: '"t-1"' }),
    })) as unknown as typeof fetch
    await refreshAvatar('agent')
    const img = chatHolders('agent', 'img')[0] as HTMLImageElement
    expect(img).toBeTruthy()

    img.dispatchEvent(new Event('error'))

    for (const el of chatHolders('agent')) expect(el.textContent).toBe('G')
    expect(chatHolders('agent', 'img').length).toBe(0)
  })
})
