import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createVoicePage } from './voice_page'
import { createEventBlockStore } from './components/event_blocks'

// The draw loop and aura lifecycle need frame-level control that jsdom's
// timer-driven rAF cannot give deterministically, so this file stubs rAF
// with a hand-pumped queue and mocks the aura visual to a spy pair.

const frameSpy = vi.fn()
const auraDestroySpy = vi.fn()
vi.mock('./voice_aura', () => ({
  createAuraVisual: () => ({ frame: frameSpy, destroy: auraDestroySpy }),
}))

interface RafEntry { id: number; cb: (now: number) => void }
let queue: RafEntry[] = []
let nextId = 0
let rafSpy: ReturnType<typeof vi.fn>
let cancelSpy: ReturnType<typeof vi.fn>

function tick(now: number): void {
  const current = queue
  queue = []
  for (const f of current) f.cb(now)
}

function setup() {
  const store = createEventBlockStore()
  const page = createVoicePage({
    engineName: 'e',
    level: () => 0.5,
    blocks: store,
    onHold: vi.fn(),
    onClose: vi.fn(),
  })
  document.body.appendChild(page.root)
  return { page, store }
}

beforeEach(() => {
  queue = []
  nextId = 0
  frameSpy.mockClear()
  auraDestroySpy.mockClear()
  rafSpy = vi.fn((cb: (now: number) => void) => {
    queue.push({ id: ++nextId, cb })
    return nextId
  })
  cancelSpy = vi.fn((id: number) => {
    const i = queue.findIndex((f) => f.id === id)
    if (i >= 0) queue.splice(i, 1)
  })
  vi.stubGlobal('requestAnimationFrame', rafSpy)
  vi.stubGlobal('cancelAnimationFrame', cancelSpy)
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('aura draw loop', () => {
  it('frames pass (now, state, live level) to the aura visual', () => {
    const { page } = setup()
    tick(100)
    expect(frameSpy).toHaveBeenCalledTimes(1)
    expect(frameSpy).toHaveBeenCalledWith(100, 'idle', 0.5)
    page.setState('think')
    tick(200)
    expect(frameSpy).toHaveBeenLastCalledWith(200, 'think', 0.5)
    page.destroy()
  })

  it('re-schedules itself every frame while connected', () => {
    const { page } = setup()
    const initial = rafSpy.mock.calls.length
    tick(100)
    expect(rafSpy.mock.calls.length).toBe(initial + 1)
    tick(131)
    expect(rafSpy.mock.calls.length).toBe(initial + 2)
    page.destroy()
  })

  it('throttles to ~33fps: no frame before 30ms, frame at exactly 30ms', () => {
    const { page } = setup()
    // First frame at t=100 arms lastR=100.
    tick(100)
    expect(frameSpy).toHaveBeenCalledTimes(1)
    // +20ms: below the throttle window — no frame.
    tick(120)
    expect(frameSpy).toHaveBeenCalledTimes(1)
    // Exactly +30ms: the window has elapsed.
    tick(130)
    expect(frameSpy).toHaveBeenCalledTimes(2)
    page.destroy()
  })

  it('a detached root ends the chain: no further schedule, no frame', () => {
    const { page } = setup()
    tick(100)
    expect(frameSpy).toHaveBeenCalledTimes(1)
    page.root.remove()
    const scheduled = rafSpy.mock.calls.length
    tick(200)
    // The guard returned before re-scheduling and before framing.
    expect(rafSpy.mock.calls.length).toBe(scheduled)
    expect(frameSpy).toHaveBeenCalledTimes(1)
    page.destroy()
  })
})

describe('destroy', () => {
  it('cancels the pending frame, destroys the aura, and leaves no timers', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    // Fake timers replace globals at install time, so the rAF stubs must be
    // (re)applied after, not in beforeEach order.
    vi.stubGlobal('requestAnimationFrame', rafSpy)
    vi.stubGlobal('cancelAnimationFrame', cancelSpy)
    try {
      const { page, store } = setup()
      // A running tool starts the sink's 200ms live ticker.
      store.apply({ type: 'tool_start', tool_use: { id: 't1', name: 'web', input: {}, summary: '' } })
      expect(queue.length).toBe(1)
      page.destroy()
      expect(cancelSpy).toHaveBeenCalled()
      expect(auraDestroySpy).toHaveBeenCalledTimes(1)
      // rAF cancelled + sink ticker disposed: nothing may keep ticking.
      expect(vi.getTimerCount()).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })
})
