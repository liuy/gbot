import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createTakeoverBar } from './takeover_bar'
import { createChat } from './chat'

type Listener = (msg: unknown) => void
type BinaryListener = (data: ArrayBuffer) => void

const listeners: Set<Listener> = new Set()
const binaryListeners: Set<BinaryListener> = new Set()
const stateCbs: Set<(s: string) => void> = new Set()
const sent: unknown[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    subscribeBinary: (fn: BinaryListener) => {
      binaryListeners.add(fn)
      return () => binaryListeners.delete(fn)
    },
    send: (p: unknown) => {
      sent.push(p)
    },
    connected: true,
    onStateChange: (cb: (s: string) => void) => {
      stateCbs.add(cb)
      return () => stateCbs.delete(cb)
    },
    reconnect: () => {},
  }),
}))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

// Drives chat.ts's conn.onStateChange callback the way ws.ts does on a
// drop/reconnect cycle.
function changeState(cs: string) {
  stateCbs.forEach((fn) => fn(cs))
}

// The trickle driver reads performance.now() and drives via
// requestAnimationFrame, so both must run on the fake clock alongside
// setTimeout — advancing timers then yields deterministic widths.
function useBarTimers() {
  vi.useFakeTimers({
    toFake: [
      'setTimeout', 'clearTimeout',
      'requestAnimationFrame', 'cancelAnimationFrame',
      'performance',
    ],
  })
}

function fillOf(root: ParentNode): HTMLElement {
  return root.querySelector('[data-testid="takeover-bar-fill"]') as HTMLElement
}

function mountBar(): ReturnType<typeof createTakeoverBar> {
  const bar = createTakeoverBar()
  document.body.appendChild(bar.root)
  return bar
}

function mountChat() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

// Starts a takeover window the production way: pick engine e2 in the
// header's engine picker (sends engine_switch, bar starts).
function clickEngineSwitch() {
  dispatch({ type: 'connect_status', connected: true })
  dispatch(METADATA)
  // Engine picker trigger is the first breadcrumb combo in the header.
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
}

const METADATA = {
  type: 'metadata',
  connect: { connected: true },
  config: { models: [], current: { provider: 'p', model: 'm' } },
  engines: {
    engines: [
      { id: 'e1', name: 'agent-1', model: 'm' },
      { id: 'e2', name: 'agent-2', model: 'm' },
    ],
    activeID: 'e1',
  },
  history: { messages: [], nextCursor: '', hasMore: false },
  stats: { usage: { input_tokens: 0, output_tokens: 0 } },
}

beforeEach(() => {
  listeners.clear()
  binaryListeners.clear()
  stateCbs.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.useRealTimers()
})

describe('takeover bar', () => {
  it('is invisible by default: width 0, opacity 0, 2px track reserved', () => {
    const bar = mountBar()
    const fill = fillOf(bar.root)
    expect(fill.style.width).toBe('0%')
    expect(fill.style.opacity).toBe('0')
    // The always-present 2px track is what makes appearing shift-free.
    expect(bar.root.style.height).toBe('2px')
  })

  it('start() makes the bar visible and the width creep forward', () => {
    useBarTimers()
    const bar = mountBar()
    bar.start()
    vi.advanceTimersByTime(300)
    const fill = fillOf(bar.root)
    expect(fill.style.opacity).toBe('1')
    const w1 = parseFloat(fill.style.width)
    expect(w1).toBeGreaterThan(0)
    expect(w1).toBeLessThan(90)
  })

  it('trickle decelerates: equal windows gain less width over time', () => {
    useBarTimers()
    const bar = mountBar()
    bar.start()
    vi.advanceTimersByTime(300)
    const w1 = parseFloat(fillOf(bar.root).style.width)
    vi.advanceTimersByTime(300)
    const w2 = parseFloat(fillOf(bar.root).style.width)
    // Ease-out: the second 300ms window must advance less than the first.
    expect(w2 - w1).toBeLessThan(w1)
  })

  it('holds at the 90% asymptote once the assumed window is exceeded', () => {
    useBarTimers()
    const bar = mountBar()
    bar.start()
    vi.advanceTimersByTime(5000)
    const fill = fillOf(bar.root)
    expect(fill.style.width).toBe('90%')
    // Still pinned at the asymptote later on — it never finishes alone.
    vi.advanceTimersByTime(5000)
    expect(fill.style.width).toBe('90%')
    expect(fill.style.opacity).toBe('1')
  })

  it('finish() fills to 100%, then fades out after 300ms', () => {
    useBarTimers()
    const bar = mountBar()
    bar.start()
    vi.advanceTimersByTime(300)
    bar.finish()
    const fill = fillOf(bar.root)
    expect(fill.style.width).toBe('100%')
    expect(fill.style.opacity).toBe('1')
    vi.advanceTimersByTime(299)
    expect(fill.style.opacity).toBe('1')
    vi.advanceTimersByTime(1)
    expect(fill.style.opacity).toBe('0')
  })

  it('fast path: finish() immediately after start() completes cleanly', () => {
    useBarTimers()
    const bar = mountBar()
    bar.start()
    bar.finish()
    const fill = fillOf(bar.root)
    expect(fill.style.width).toBe('100%')
    vi.advanceTimersByTime(300)
    expect(fill.style.opacity).toBe('0')
  })

  it('finish() while idle is a no-op so boot metadata never flashes the bar', () => {
    useBarTimers()
    const bar = mountBar()
    bar.finish()
    vi.advanceTimersByTime(1000)
    const fill = fillOf(bar.root)
    expect(fill.style.width).toBe('0%')
    expect(fill.style.opacity).toBe('0')
  })

  it('restart mid-fade resets to 0% before trickling again', () => {
    useBarTimers()
    const bar = mountBar()
    bar.start()
    vi.advanceTimersByTime(300)
    bar.finish()
    vi.advanceTimersByTime(400) // fully faded while sitting at 100%
    bar.start()
    const fill = fillOf(bar.root)
    expect(fill.style.width).toBe('0%')
    expect(fill.style.opacity).toBe('1')
    vi.advanceTimersByTime(300)
    expect(parseFloat(fill.style.width)).toBeGreaterThan(0)
    expect(parseFloat(fill.style.width)).toBeLessThan(90)
  })

  it('start() while already running is a no-op — the trickle continues', () => {
    useBarTimers()
    const bar = mountBar()
    bar.start()
    vi.advanceTimersByTime(500)
    const w1 = parseFloat(fillOf(bar.root).style.width)
    // ws.ts re-notifies its state on every reconnect retry (250ms cadence
    // while upgrading); a repeat start must not snap the fill back to 0.
    bar.start()
    const fill = fillOf(bar.root)
    expect(parseFloat(fill.style.width)).toBe(w1)
    vi.advanceTimersByTime(200)
    expect(parseFloat(fill.style.width)).toBeGreaterThan(w1)
  })

  it('uses the theme token only — no hardcoded colors anywhere', () => {
    const bar = mountBar()
    const fill = fillOf(bar.root)
    expect(fill.style.background).toBe('var(--color-blue)')
    // Settings-card convention: scan style and class strings for hex/rgb
    // literals that would defeat theme adaptivity.
    const walk = [bar.root as Element, ...bar.root.querySelectorAll('*')]
    for (const el of walk) {
      const style = el.getAttribute('style') ?? ''
      expect(style.includes('#')).toBe(false)
      expect(style.includes('rgb(')).toBe(false)
      const cls = el.getAttribute('class') ?? ''
      expect(cls.includes('[#')).toBe(false)
      expect(cls.includes('rgb(')).toBe(false)
    }
  })
})

describe('takeover bar wiring', () => {
  it('mounts inside the header and stays idle at boot', () => {
    useBarTimers()
    mountChat()
    dispatch({ type: 'connect_status', connected: true })
    dispatch(METADATA)
    const headerEl = document.querySelector('header') as HTMLElement
    expect(headerEl).toBeTruthy()
    const fill = fillOf(headerEl)
    expect(headerEl.contains(fill)).toBe(true)
    expect(fill.style.opacity).toBe('0')
    expect(fill.style.width).toBe('0%')
  })

  it('engine switch click starts the bar; the metadata frame completes it', () => {
    useBarTimers()
    mountChat()
    clickEngineSwitch()
    expect(
      sent.find((m) => (m as { type: string }).type === 'engine_switch'),
    ).toEqual({ type: 'engine_switch', engineID: 'e2' })
    vi.advanceTimersByTime(100)
    const fill = fillOf(document.querySelector('header') as HTMLElement)
    expect(fill.style.opacity).toBe('1')
    expect(parseFloat(fill.style.width)).toBeGreaterThan(0)
    // The metadata frame is what ends the loading window.
    dispatch(METADATA)
    expect(fill.style.width).toBe('100%')
    vi.advanceTimersByTime(300)
    expect(fill.style.opacity).toBe('0')
  })

  it('error frame finishes a stranded switch: 100% then fade to invisible', () => {
    useBarTimers()
    mountChat()
    clickEngineSwitch()
    vi.advanceTimersByTime(100)
    const fill = fillOf(document.querySelector('header') as HTMLElement)
    expect(fill.style.opacity).toBe('1')
    // Unknown engine: the server answers engine_switch with an error frame
    // (switchEngine → buildError), never a metadata frame — without a
    // finish here the bar waits at the asymptote forever.
    dispatch({ type: 'error', message: 'unknown engine: e3' })
    expect(fill.style.width).toBe('100%')
    expect(fill.style.opacity).toBe('1')
    vi.advanceTimersByTime(300)
    expect(fill.style.opacity).toBe('0')
  })

  it('session switch click starts the bar', () => {
    useBarTimers()
    mountChat()
    dispatch({ type: 'connect_status', connected: true })
    dispatch(METADATA)
    dispatch({
      type: 'session_list',
      sessions: [{ id: 's2', title: 'second session', updatedAt: 1 }],
    })
    const row = document.querySelector('[data-session-row]') as HTMLElement
    row.click()
    expect(
      sent.find((m) => (m as { type: string }).type === 'session_switch'),
    ).toEqual({ type: 'session_switch', sessionID: 's2' })
    vi.advanceTimersByTime(100)
    const fill = fillOf(document.querySelector('header') as HTMLElement)
    expect(fill.style.opacity).toBe('1')
    expect(parseFloat(fill.style.width)).toBeGreaterThan(0)
  })

  it('reconnect auto-takeover: reconnecting starts the bar, replayed metadata completes it', () => {
    useBarTimers()
    mountChat()
    dispatch({ type: 'connect_status', connected: true })
    dispatch(METADATA)
    // serveChatWS takes over on every (re)connect without any client-sent
    // switch, so the reconnect window itself is a takeover window.
    changeState('reconnecting')
    vi.advanceTimersByTime(100)
    const fill = fillOf(document.querySelector('header') as HTMLElement)
    expect(fill.style.opacity).toBe('1')
    expect(parseFloat(fill.style.width)).toBeGreaterThan(0)
    changeState('connected')
    dispatch(METADATA)
    expect(fill.style.width).toBe('100%')
    vi.advanceTimersByTime(300)
    expect(fill.style.opacity).toBe('0')
  })

  it('upgrade reconnect (1012 path) runs the bar until metadata replays', () => {
    useBarTimers()
    mountChat()
    dispatch({ type: 'connect_status', connected: true })
    dispatch(METADATA)
    changeState('upgrading')
    vi.advanceTimersByTime(100)
    const fill = fillOf(document.querySelector('header') as HTMLElement)
    expect(fill.style.opacity).toBe('1')
    expect(parseFloat(fill.style.width)).toBeGreaterThan(0)
    changeState('connected')
    dispatch(METADATA)
    expect(fill.style.width).toBe('100%')
    vi.advanceTimersByTime(300)
    expect(fill.style.opacity).toBe('0')
  })

  it('reconnect give-up finishes the bar instead of stranding it at the asymptote', () => {
    useBarTimers()
    mountChat()
    dispatch({ type: 'connect_status', connected: true })
    dispatch(METADATA)
    changeState('reconnecting')
    vi.advanceTimersByTime(100)
    // 5 failed retries later the connection machinery gives up; the retry
    // banner owns the failure messaging, the bar must close its window.
    changeState('disconnected')
    const fill = fillOf(document.querySelector('header') as HTMLElement)
    expect(fill.style.width).toBe('100%')
    vi.advanceTimersByTime(300)
    expect(fill.style.opacity).toBe('0')
  })
})
