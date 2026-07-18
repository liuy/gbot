import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
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

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  dispatch({ type: 'connect_status', connected: true })
  return chat
}

function userMsg(id: string, text: string) {
  return {
    id,
    role: 'user' as const,
    text,
    thinking: [],
    tools: [],
    usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error: '',
    status: 'done' as const,
    startedAt: 0,
  }
}

function userMsgAt(id: string, text: string, startedAt: number) {
  return { ...userMsg(id, text), startedAt }
}

// Compact-only selector (mirrors compact_boundary.test.ts's dividerElements).
// The `.shrink-0` qualifier excludes the streaming progress-bar heartbeat dot,
// which also carries `text-blue text-[10px]` classes.
function dividerElements(): HTMLElement[] {
  const all = document.querySelectorAll('.text-blue.text-\\[10px\\].shrink-0')
  return Array.from(all).filter((el) => el.textContent === 'Compact') as HTMLElement[]
}

// Non-Compact selector: anchor + gap time dividers.
function timeDividerElements(): HTMLElement[] {
  const all = document.querySelectorAll('.text-blue.text-\\[10px\\].shrink-0')
  return Array.from(all).filter((el) => el.textContent !== 'Compact') as HTMLElement[]
}

function mountWithBaseline(baselineAt: number): void {
  mount()
  dispatch({
    type: 'history',
    messages: [userMsgAt('baseline', 'baseline', baselineAt)],
    nextCursor: '',
    hasMore: false,
  })
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

const HOUR = 3600 * 1000
const MIN = 60 * 1000

describe('time divider (loadHistory)', () => {
  it('first message on history load gets an anchor divider', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mount()
    dispatch({
      type: 'history',
      messages: [userMsgAt('m1', 'hello', now)],
      nextCursor: '',
      hasMore: false,
    })
    expect(timeDividerElements().length).toBe(1)
    expect(timeDividerElements()[0].textContent).toMatch(/^Today\b/)
  })

  it('renders a divider for each >=15min gap within a page', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 3 * HOUR)
    const before = timeDividerElements().length
    dispatch({
      type: 'history',
      messages: [
        userMsgAt('m1', 'one', now - 2 * HOUR),
        userMsgAt('m2', 'two', now),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // baseline anchor + m1 (gap 1h) + m2 (gap 2h).
    expect(timeDividerElements().length).toBe(before + 2)
    // The two NEW dividers must be time-format labels (the baseline anchor
    // is a date label and could sit anywhere in DOM order due to prepend).
    const timeFmtCount = timeDividerElements().filter((d) =>
      /^\d{1,2}:\d{2}\s*(AM|PM)?$/.test(d.textContent || ''),
    ).length
    expect(timeFmtCount).toBe(2)
  })

  it('does not render a divider for sub-15min gaps', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 20 * MIN)
    dispatch({
      type: 'history',
      messages: [
        userMsgAt('m1', 'one', now - 10 * MIN),
        userMsgAt('m2', 'two', now),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // Only the baseline anchor; no new dividers.
    expect(timeDividerElements().length).toBe(1)
  })

  it('renders Today divider when a page crosses day boundary', () => {
    vi.useFakeTimers()
    const todayNoon = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(todayNoon)
    // Baseline yesterday noon → anchor label is weekday (Yesterday would be
    // 1 day ago; yesterday noon is 0..1 day depending on direction).
    mountWithBaseline(new Date(2026, 6, 17, 12, 0).getTime())
    dispatch({
      type: 'history',
      messages: [
        userMsgAt('m1', 'late', new Date(2026, 6, 17, 23, 0).getTime()),
        userMsgAt('m2', 'morning', new Date(2026, 6, 18, 9, 0).getTime()),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // m1 prev=baseline (yesterday noon → yesterday 23:00, gap 11h, same day) → time divider.
    // m2 prev=m1 (yesterday 23:00 → today 09:00) → cross-day → "Today" label.
    // Plus baseline anchor → total 3.
    expect(timeDividerElements().length).toBe(3)
    expect(timeDividerElements().some((d) => d.textContent?.startsWith('Today') ?? false)).toBe(true)
  })

  it('skips compact markers when comparing time gaps', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - MIN)
    dispatch({
      type: 'history',
      messages: [
        userMsgAt('m1', 'one', now - 30 * 1000),
        {
          id: 'b1',
          role: 'system' as const,
          compactBoundary: true,
          text: '',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done' as const,
          startedAt: 0,
        },
        userMsgAt('m2', 'two', now),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // Marker skipped; both gaps (baseline→m1, m1→m2) are sub-30s. Only anchor.
    expect(timeDividerElements().length).toBe(1)
    // The Compact divider is still rendered.
    expect(dividerElements().length).toBe(1)
  })

  it('page-boundary divider accounts for negative-time-diff when prepending older pages', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mount()
    // First dispatch establishes existing message at now-2h. Anchor fires
    // because prev=null (wasEmpty). 1 anchor divider.
    dispatch({
      type: 'history',
      messages: [userMsgAt('existing', 'existing', now - 2 * HOUR)],
      nextCursor: '',
      hasMore: false,
    })
    expect(timeDividerElements().length).toBe(1)
    // Second dispatch prepends a page with a single older message at now-3h.
    // Batch-internal: prev=existing(now-2h), curr=newMsg(now-3h) → -1h diff →
    // same day, no batch-internal divider.
    // Page-boundary: prev=newMsg(now-3h), curr=existing(now-2h) → +1h → divider.
    // Total: 1 anchor + 0 batch + 1 page-boundary = 2.
    dispatch({
      type: 'history',
      messages: [userMsgAt('newMsg', 'older', now - 3 * HOUR)],
      nextCursor: '',
      hasMore: false,
    })
    expect(timeDividerElements().length).toBe(2)
  })

  it('page-boundary: no time divider when gap < 15min OR envelope carries compactBoundary', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)

    // (a) small positive gap on page-boundary → no time divider.
    mount()
    dispatch({
      type: 'history',
      messages: [userMsgAt('existing', 'existing', now - 5 * MIN)],
      nextCursor: '',
      hasMore: false,
    })
    dispatch({
      type: 'history',
      messages: [userMsgAt('newMsg', 'older', now - 10 * MIN)],
      nextCursor: '',
      hasMore: false,
    })
    // Only the anchor from the first dispatch.
    expect(timeDividerElements().length).toBe(1)
    expect(dividerElements().length).toBe(0)

    // (b) envelope compactBoundary suppresses time divider even when gap
    // would qualify.
    document.body.innerHTML = ''
    listeners.clear()
    mount()
    dispatch({
      type: 'history',
      messages: [userMsgAt('existing2', 'existing', now - 2 * HOUR)],
      nextCursor: '',
      hasMore: false,
    })
    expect(timeDividerElements().length).toBe(1)
    dispatch({
      type: 'history',
      messages: [userMsgAt('newMsg2', 'older', now - 3 * HOUR)],
      nextCursor: '',
      hasMore: false,
      compactBoundary: true,
    })
    // anchor + 0 time page-boundary (suppressed) + 1 compact.
    expect(timeDividerElements().length).toBe(1)
    expect(dividerElements().length).toBe(1)
  })

  it('renders multiple dividers when several gaps within one page qualify', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 4 * HOUR)
    dispatch({
      type: 'history',
      messages: [
        userMsgAt('m1', 'one', now - 3 * HOUR),
        userMsgAt('m2', 'two', now - 2 * HOUR),
        userMsgAt('m3', 'three', now - 2 * HOUR + 5 * MIN),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // baseline anchor + m1 (1h gap) + m2 (1h gap); m3 5min after m2 → no.
    expect(timeDividerElements().length).toBe(3)
  })

  it('resetAllState (connect_status with new engineID) clears all dividers', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mount()
    dispatch({
      type: 'history',
      messages: [userMsgAt('m1', 'hello', now)],
      nextCursor: '',
      hasMore: false,
    })
    expect(timeDividerElements().length).toBe(1)
    dispatch({ type: 'connect_status', connected: true, engineID: 'new-engine' })
    expect(timeDividerElements().length).toBe(0)
  })
})

// Input helpers — textarea keydown listener at input_bar.ts dispatches send
// on Enter when navigator.maxTouchPoints === 0 (jsdom default).
function setTextarea(value: string) {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = value
  ta.dispatchEvent(new Event('input', { bubbles: true }))
}
function pressEnter() {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
}
function dispatchEvent(e: unknown) {
  dispatch({ type: 'event', event: e })
}

describe('time divider (streaming append)', () => {
  it('user send triggers divider after long gap', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 2 * HOUR)
    const before = timeDividerElements().length
    setTextarea('hello')
    pressEnter()
    expect(timeDividerElements().length).toBe(before + 1)
    const newDividers = timeDividerElements().filter((d) =>
      /^\d{1,2}:\d{2}\s*(AM|PM)?$/.test(d.textContent || ''),
    )
    expect(newDividers.length).toBe(1)
  })

  it('user send does not trigger divider after short gap', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 5 * MIN)
    const before = timeDividerElements().length
    setTextarea('hi')
    pressEnter()
    expect(timeDividerElements().length).toBe(before)
  })

  it('first-send-of-session creates anchor divider', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mount()
    setTextarea('hello')
    pressEnter()
    expect(timeDividerElements().length).toBe(1)
    expect(timeDividerElements()[0].textContent).toMatch(/^Today\b/)
  })

  it('query_start triggers divider after long gap', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 2 * HOUR)
    const before = timeDividerElements().length
    dispatchEvent({ type: 'query_start' })
    expect(timeDividerElements().length).toBe(before + 1)
  })

  it('query_start does not trigger divider after short gap', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 5 * MIN)
    const before = timeDividerElements().length
    dispatchEvent({ type: 'query_start' })
    expect(timeDividerElements().length).toBe(before)
  })

  it('query_end updates lastActivityAt so a 30s-later send does not insert a divider', () => {
    vi.useFakeTimers()
    const t0 = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(t0)
    // baseline at t0-1000ms.
    mountWithBaseline(t0 - 1000)
    // Dispatch user msg at t0 via history (loads with prev=baseline, gap=1s → no).
    dispatch({
      type: 'history',
      messages: [userMsgAt('u1', 'q', t0)],
      nextCursor: '',
      hasMore: false,
    })
    // query_start at t0+1s — assistant shell pushed, gap to user msg = 1s → no.
    vi.setSystemTime(t0 + 1000)
    dispatchEvent({ type: 'query_start' })
    const afterQueryStart = timeDividerElements().length
    // query_end at t0+35min — should update assistant.lastActivityAt.
    vi.setSystemTime(t0 + 1000 + 35 * MIN)
    dispatchEvent({ type: 'query_end' })
    // User sends 30s after query_end — gap = 30s, no divider.
    vi.setSystemTime(t0 + 1000 + 35 * MIN + 30 * 1000)
    setTextarea('next')
    pressEnter()
    expect(timeDividerElements().length).toBe(afterQueryStart)
  })

  it('appendError first-branch updates lastActivityAt so a 10min-later send does not insert a divider', () => {
    vi.useFakeTimers()
    const t0 = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(t0)
    mountWithBaseline(t0 - 1000)
    dispatch({
      type: 'history',
      messages: [userMsgAt('u1', 'q', t0)],
      nextCursor: '',
      hasMore: false,
    })
    vi.setSystemTime(t0 + 1000)
    dispatchEvent({ type: 'query_start' })
    const afterQueryStart = timeDividerElements().length
    // Error 25min into the stream — appendError first-branch updates
    // lastActivityAt to t0+25min+1s.
    vi.setSystemTime(t0 + 1000 + 25 * MIN)
    dispatch({ type: 'error', message: 'boom' })
    expect(document.body.textContent).toContain('boom')
    // User sends 10min later — gap = 10min → no divider.
    vi.setSystemTime(t0 + 1000 + 25 * MIN + 10 * MIN)
    setTextarea('next')
    pressEnter()
    expect(timeDividerElements().length).toBe(afterQueryStart)
  })

  it('attachment non-streaming branch creates anchor and later gap divider', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mount()
    dispatchEvent({
      type: 'attachment',
      message: { attachment: { prompt: 'foo', source_uuid: '' } },
    })
    expect(timeDividerElements().length).toBe(1)
    const before = timeDividerElements().length
    // 2h later, another attachment fires the non-streaming branch.
    vi.setSystemTime(now + 2 * HOUR)
    dispatchEvent({
      type: 'attachment',
      message: { attachment: { prompt: 'bar', source_uuid: '' } },
    })
    expect(timeDividerElements().length).toBe(before + 1)
  })

  it('rapid deltas do not insert multiple dividers', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 2 * HOUR)
    const before = timeDividerElements().length
    setTextarea('hi')
    pressEnter()
    // onSend gap=2h → 1 divider.
    expect(timeDividerElements().length).toBe(before + 1)
    const afterSend = timeDividerElements().length
    // query_start immediately after — gap=0s → no divider.
    dispatchEvent({ type: 'query_start' })
    // 10 rapid text deltas — no message creation, no divider.
    for (let i = 0; i < 10; i++) {
      dispatchEvent({ type: 'text_delta', text: 'chunk ' })
    }
    expect(timeDividerElements().length).toBe(afterSend)
  })

  it('locale follows navigator.language', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 14, 30).getTime()
    vi.setSystemTime(now)
    vi.spyOn(navigator, 'language', 'get').mockReturnValue('zh-CN')
    mountWithBaseline(now - 2 * HOUR)
    dispatch({
      type: 'history',
      messages: [userMsgAt('m1', 'one', now)],
      nextCursor: '',
      hasMore: false,
    })
    // The new divider (m1's gap=2h, at 14:30) should localize to zh-CN.
    // Anchor divider is at 12:30 (also matches /30/), so filter by hour 14
    // or 下午2 to isolate m1's divider from the anchor.
    const zhTimeDividers = timeDividerElements().filter((d) =>
      /(14:30|下午2:30)/.test(d.textContent || ''),
    )
    expect(zhTimeDividers.length).toBe(1)
  })
})
