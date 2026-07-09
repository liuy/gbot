import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'

// jsdom lacks IntersectionObserver
let observerCallback: IntersectionObserverCallback | null = null
class MockIntersectionObserver {
  constructor(cb: IntersectionObserverCallback) { observerCallback = cb }
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return [] }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver as unknown as typeof IntersectionObserver)

/** Simulate the scroll-to-top sentinel becoming visible (triggers prefetch). */
function triggerTopObserver() {
  if (observerCallback) {
    observerCallback([{ isIntersecting: true } as IntersectionObserverEntry], null!)
  }
}

type Listener = (msg: unknown) => void

const listeners: Set<Listener> = new Set()
const sent: unknown[] = []

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
  observerCallback = null
  document.body.innerHTML = ''
})

describe('chat integration', () => {
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
    const mdBody = document.querySelector('.md-body') as HTMLElement
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
    const mdBody = document.querySelector('.md-body') as HTMLElement
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
    const mdBody = document.querySelector('.md-body') as HTMLElement
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
      { type: 'query_end', aborted: true },
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
        { type: 'query_end', aborted: true },
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

  it('error renders red block on streaming assistant', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    dispatch({ type: 'error', message: 'boom' })
    const red = document.querySelector('.text-red')
    expect(red?.textContent).toContain('boom')
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

  it('reconnect scrolls to bottom after history load even if previously scrolled up', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })
    Element.prototype.scrollIntoView = vi.fn()

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })

    // Simulate user scrolled up: scroll button should be visible
    chat.scrollEl.scrollTop = 0
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    const scrollBtn = chat.root.querySelector('button.absolute.bottom-24') as HTMLElement
    expect(scrollBtn.style.opacity).toBe('1')

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
    expect(scrollBtn.style.opacity).toBe('0')
  })

  it('IntersectionObserver triggers prefetch on scroll to top', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1',
          role: 'user',
          text: 'first',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: 0,
        },
      ],
      nextCursor: 'c1',
      hasMore: true,
    })
    // Auto-prefetch fires immediately when sentinel is visible (content short).
    let historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(1)
    expect(historyReqs[0].cursor).toBe('c1')

    // Observer already fired — loadingMore guard blocks duplicate.
    triggerTopObserver()
    historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(1)
  })

  it('loadingMore guard prevents concurrent observer prefetch', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1',
          role: 'user',
          text: 'msg1',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: 0,
        },
      ],
      nextCursor: 'c1',
      hasMore: true,
    })
    // First scroll triggers prefetch.
    triggerTopObserver()
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(1)

    // Second trigger before response must not send duplicate.
    triggerTopObserver()
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(1)
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

  it('auto-prefetches when content is shorter than viewport (observer missed)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // Observer fires immediately — sentinel visible, content empty.
    // At this point hasMore is false, so no request is sent.
    triggerTopObserver()
    // History arrives with more pages.
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
    // Why: the sentinel is already visible when hasMore is set, so the
    // IntersectionObserver won't re-fire. loadHistory detects this and
    // triggers the fetch directly.
    const historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(1)
    expect(historyReqs[0].cursor).toBe('c1')
  })

  it('task_list message renders the task panel with summary', () => {
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
    const body = document.body.textContent ?? ''
    expect(body).toContain('1/3 Done')
    expect(body).toContain('1 Running')
    expect(body).toContain('1 Pending')
  })

  it('connect_status hides the task panel', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'task_list',
      tasks: [{ id: '1', subject: 'UniqueTaskSubject123', status: 'pending' }],
    })
    // Panel root is the glass card; find it by its class.
    const panel = document.querySelector('.glass.border-hairline.rounded-xl') as HTMLElement
    expect(panel).toBeTruthy()
    expect(panel.style.display).toBe('')
    dispatch({ type: 'connect_status', connected: true })
    expect(panel.style.display).toBe('none')
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

    // Disconnect + takeover: connect_status resets state but carries
    // server-accumulated usage so progressUsage is restored.
    dispatch({
      type: 'connect_status',
      connected: true,
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

    // Takeover: connect_status carries server-accumulated baseline (1000 in, 500 out)
    dispatch({
      type: 'connect_status',
      connected: true,
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
})
