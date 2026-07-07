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

type Listener = (msg: any) => void

const listeners: Set<Listener> = new Set()
const sent: any[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: any) => {
      sent.push(p)
    },
    connected: true,
  }),
}))

function dispatch(msg: any) {
  listeners.forEach((fn) => fn(msg))
}

function dispatchEvents(events: any[]) {
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
    // No prefetch on initial load — IntersectionObserver handles lazy loading.
    let historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(0)

    // User scrolls to top — observer triggers prefetch.
    triggerTopObserver()
    historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(1)
    expect(historyReqs[0].cursor).toBe('c1')
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
    const stopBtn = document.querySelector('button') as HTMLButtonElement
    expect(stopBtn?.textContent).toContain('STOP')
  })
})
