import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'

class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return []
  }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

type Listener = (msg: unknown) => void
const listeners: Set<Listener> = new Set()
const sent: unknown[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: unknown) => sent.push(p),
    connected: true,
  }),
}))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}
function events(es: unknown[]) {
  for (const e of es) dispatch({ type: 'event', event: e })
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  dispatch({ type: 'connect_status', connected: true })
  return chat
}

function setTextarea(value: string) {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = value
  ta.dispatchEvent(new Event('input', { bubbles: true }))
}
function pressEnter() {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
}
function clickStop() {
  const stopBtn = Array.from(document.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('STOP'),
  ) as HTMLButtonElement | undefined
  stopBtn?.click()
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('streaming DOM isolation', () => {
  it('text_delta reaches DOM', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'hello' },
    ])
    expect(
      (document.querySelector('.md-body') as HTMLElement).textContent,
    ).toContain('hello')
  })

  it('text_delta survives subsequent tool_start', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'persist' },
      { type: 'tool_start', tool_use: { id: 't-1', name: 'Bash' } },
    ])
    expect(
      (document.querySelector('.md-body') as HTMLElement).textContent,
    ).toContain('persist')
  })

  it('thinking_delta writes to thinking sink', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: 'reasoning' } },
    ])
    expect(document.body.textContent).toContain('reasoning')
  })

  it('structural events mount sinks (text_start)', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }, { type: 'text_start' }])
    expect(document.querySelector('.md-body')).toBeTruthy()
  })

  it('abort mid-stream keeps partial + interrupt marker', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'partial' },
    ])
    clickStop()
    events([
      { type: 'text_end' },
      { type: 'text_delta', text: '[interrupted]' },
      { type: 'query_end', aborted: true },
    ])
    expect(document.body.textContent).toContain('partial')
    expect(document.body.textContent).toContain('[interrupted]')
  })

  it('empty text_delta is a no-op', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '' },
    ])
    const mdBodies = document.querySelectorAll('.md-body')
    expect(mdBodies.length).toBe(1)
  })

  it('multiple text blocks per turn each get fresh sinks', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'a' },
      { type: 'text_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'b' },
      { type: 'text_end' },
      { type: 'query_end' },
    ])
    expect(document.querySelectorAll('.md-body').length).toBe(2)
  })

  it('interleaved text+thinking both writable', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: 'think' } },
      { type: 'text_start' },
      { type: 'text_delta', text: 'say' },
    ])
    expect(document.body.textContent).toContain('think')
    expect(document.body.textContent).toContain('say')
  })

  it('markdown renders inline during text_delta (**bold** → <strong>)', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '**bold**' },
    ])
    const mdBody = document.querySelector('.md-body') as HTMLElement
    expect(mdBody.querySelector('strong')?.textContent).toBe('bold')
  })

  it('InputBar textarea value is unchanged across text_delta', () => {
    mount()
    setTextarea('q')
    pressEnter()
    setTextarea('preserved')
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'streaming content' },
    ])
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('preserved')
  })

  it('reconnect mid-turn-2: no query_start in replay, must not duplicate (real-world)', () => {
    // Disconnect happens during turn 2. Turn 1 completed normally
    // (turn_end cleared buffer). On reconnect, replay has turn 2 events
    // but NO query_start. Without query_start calling cleanupStreamingRefs(),
    // old streaming state persists → duplication.
    mount()

    // Prior conversation history.
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'old-user', role: 'user',
          blocks: [{ kind: 'text', text: '之前的问题' }],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 1000,
        },
        {
          id: 'old-assistant', role: 'assistant',
          blocks: [{ kind: 'text', text: '之前的回答' }],
          usage: { inputTokens: 100, outputTokens: 50, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 1001,
        },
      ],
      nextCursor: '', hasMore: false,
    })

    // ── Phase 1: new query, turn 1 completes normally ──
    setTextarea('世界杯预测')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: '搜索比赛' } },
      { type: 'thinking_end' },
      { type: 'tool_start', tool_use: { id: 'tu1', name: 'Web' } },
      { type: 'tool_run', tool_use_id: 'tu1' },
      { type: 'tool_end', tool_result: { tool_use_id: 'tu1', content: 'result1' } },
      { type: 'turn_end' },
    ])

    // ── Phase 2: turn 2 starts, streams partially ──
    events([
      { type: 'turn_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: '分析结果' } },
      { type: 'thinking_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: '巴西输了' },
    ])

    // Client rendered: turn 1 (1 think + 1 tool) + turn 2 (1 think + partial text)
    expect(document.querySelectorAll('[data-thinking]').length).toBe(2)
    expect(document.querySelectorAll('[data-tool-root]').length).toBe(1)

    // ── Phase 3: disconnect + reconnect ──
    // Replay has turn 2 events ONLY (turn_end cleared turn 1 from buffer).
    // CRITICAL: replay has NO query_start — it was before turn 1.
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'old-user', role: 'user',
          blocks: [{ kind: 'text', text: '之前的问题' }],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 1000,
        },
        {
          id: 'old-assistant', role: 'assistant',
          blocks: [{ kind: 'text', text: '之前的回答' }],
          usage: { inputTokens: 100, outputTokens: 50, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 1001,
        },
        {
          id: 'new-user', role: 'user',
          blocks: [{ kind: 'text', text: '世界杯预测' }],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 2000,
        },
        {
          id: 'turn1-assistant', role: 'assistant',
          blocks: [{ kind: 'tool', tool: { name: 'Web', summary: '搜索', output: 'result1' } }],
          usage: { inputTokens: 200, outputTokens: 10, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 2001,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    // Replay: turn 2 events ONLY — NO query_start!
    events([
      { type: 'turn_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: '分析结果' } },
      { type: 'thinking_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: '巴西输了' },
      { type: 'text_delta', text: '，挪威赢了' },
      { type: 'text_end' },
      { type: 'turn_end' },
      { type: 'query_end' },
    ])

    // ── Assertions ──
    const body = document.body.textContent || ''

    // 1. Turn 2's thinking text appears exactly once (not duplicated by replay).
    const thinkMatches = body.match(/分析结果/g) || []
    expect(thinkMatches.length).toBe(1)

    // 2. Turn 2's text appears exactly once.
    const textMatches = body.match(/巴西输了/g) || []
    expect(textMatches.length).toBe(1)

    // 3. Turn 1's thinking was NOT committed to history (engine only
    //    commits tool/text blocks at turn_end, not thinking). So "搜索比赛"
    //    should NOT appear at all post-reconnect. Assert 0 to verify
    //    thinking isn't leaking from pre-disconnect streaming state.
    const turn1Think = body.match(/搜索比赛/g) || []
    expect(turn1Think.length).toBe(0)

    // 4. Total thinking blocks: turn 2 only (turn 1 thinking not committed
    //    to history). Must be exactly 1.
    const allThinking = document.querySelectorAll('[data-thinking]').length
    expect(allThinking).toBe(1)

    // 5. Total tool blocks: turn 1's tool from history = 1.
    //    (turn 2 has no tools in this scenario)
    const allTools = document.querySelectorAll('[data-tool-root]').length
    expect(allTools).toBe(1)

    // 6. No stuck heartbeat (all tools finalized).
    const heartbeats = document.querySelectorAll('.heartbeat')
    expect(heartbeats.length).toBe(0)

    // 7. Turn 2's full text is visible (replay + live merged correctly).
    expect(body).toContain('挪威赢了')

    // 8. Prior history preserved.
    expect(body).toContain('之前的回答')
  })
})
