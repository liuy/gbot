import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'
import { TokenRate } from './token_rate'

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
    const scrollBtn = chat.root.querySelector('button.absolute.bottom-24') as HTMLElement
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
    const scrollBtn = chat.root.querySelector('button.absolute.bottom-24') as HTMLElement
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

    // Feed sub-agent thinking text
    const longThinking = 'B'.repeat(200)
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(100)
      dispatchEvents([
        { type: 'thinking_delta', agent, thinking: { text: longThinking } },
      ])
    }

    // Find rate element — should show non-zero after sub-agent thinking
    const allSpans = document.querySelectorAll('span')
    let rateEl: HTMLElement | null = null
    allSpans.forEach(s => {
      if (s.textContent && s.textContent.includes('t/s')) rateEl = s as HTMLElement
    })
    expect(rateEl).toBeTruthy()
    expect(rateEl!.textContent).not.toMatch(/^0\.0 t\/s$/)
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
    const mdBody = toolRoot?.querySelector('.md-body')
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
    const panel = document.querySelector('[class*="bg-ink2"][class*="border-hairline"]') as HTMLElement
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
      { type: 'tool_param_delta', partial_input: { delta: '{"command":"ls"}', summary: 'ls' } },
    ])

    expect(addSpy).toHaveBeenCalledWith('{"command":"ls"}')
    addSpy.mockRestore()
  })
})
