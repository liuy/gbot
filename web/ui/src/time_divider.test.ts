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
    // All dividers (including anchor and intra-page) carry a date prefix
    // — historical times can't be mistaken for today.
    const dateTimeCount = timeDividerElements().filter((d) =>
      /\d{1,2}:\d{2}/.test(d.textContent || ''),
    ).length
    expect(dateTimeCount).toBe(before + 2)
  })

  it('6 queries over 48min + 1 late query produces 5 dividers', () => {
    // Real-world scenario: a productive morning of frequent queries.
    // 6 queries at 8min intervals span 40min (q1@0..q6@40), then q7 at
    // +48 (8min after q6) extends span to 48min. Then q8 16min after q7.
    // Expected dividers fire at: q1 (anchor), q3 (+16), q5 (+32), q7 (+48),
    // q8 (+64) — every 15min wall-clock from the previous divider.
    vi.useFakeTimers()
    const t0 = new Date(2026, 6, 19, 10, 0).getTime()
    vi.setSystemTime(t0)
    mount()
    const times = [0, 8, 16, 24, 32, 40, 48, 64].map((m) => t0 + m * MIN)
    times.forEach((at, i) => {
      vi.setSystemTime(at)
      dispatch({
        type: 'history',
        messages: [userMsgAt(`u${i}`, `q${i}`, at)],
        nextCursor: '',
        hasMore: false,
      })
    })
    expect(timeDividerElements().length).toBe(5)
  })

  it('inserts divider when wall-clock from last divider crosses 15min', () => {
    // Rule A: at least one divider every 15min wall-clock. baseline at
    // now-20min anchors, m1 at now-10 (10min from anchor → no), m2 at now
    // (20min from anchor divider → yes, despite only 10min from m1).
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
    expect(timeDividerElements().length).toBe(2)
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

  it('no divider when subsequent page has sub-15min gap from last divider', () => {
    // page-boundary: second dispatch arrives 5min after the first anchor —
    // the gap from lastDivAt is 5min, so no divider fires. (The old
    // compactBoundary envelope suppression is gone; envelope markers no
    // longer affect the divider rule.)
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
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
    expect(timeDividerElements().length).toBe(1)
    expect(dividerElements().length).toBe(0)
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
    // New divider carries date prefix (Today/Yesterday/...) + time.
    const newDividers = timeDividerElements().filter((d) =>
      /^Today\b/.test(d.textContent || ''),
    )
    expect(newDividers.length).toBeGreaterThanOrEqual(1)
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

  it('query_start (assistant stream) never inserts a divider', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 12, 0).getTime()
    vi.setSystemTime(now)
    mountWithBaseline(now - 2 * HOUR)
    const before = timeDividerElements().length
    dispatchEvent({ type: 'query_start' })
    expect(timeDividerElements().length).toBe(before)
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

  // query_end / appendError no longer have dedicated divider tests:
  // assistant completion time is irrelevant under the single-cursor rule
  // (only user-to-user wall-clock matters).

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
