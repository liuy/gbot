import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createChat } from './chat'

// jsdom lacks IntersectionObserver — stub it (chat.ts header/prefetch paths).
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
    subscribeBinary: () => () => {},
    send: (p: unknown) => {
      sent.push(p)
    },
    connected: true,
    onStateChange: () => {},
    reconnect: () => {},
  }),
}))

// Full-chain integration: only the rasterizer is mocked — the markdown
// pipeline, the offscreen card, the bridge probe and the menu all run real.
const domToPngMock = vi.hoisted(() => vi.fn())
vi.mock('modern-screenshot', () => ({ domToPng: domToPngMock }))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

function historyMsg(overrides: Record<string, unknown> = {}) {
  return {
    id: 'm-1',
    role: 'assistant',
    text: 'hello **world**',
    thinking: [],
    tools: [],
    usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error: '',
    status: 'done',
    startedAt: new Date(2026, 9, 15, 10, 50).getTime(),
    ...overrides,
  }
}

function messageRow(chat: ReturnType<typeof mount>): HTMLElement | null {
  const container = chat.root.querySelector('.space-y-7')
  if (!container) return null
  return (Array.from(container.children).find((c) => c.querySelector('.md-body')) ??
    null) as HTMLElement | null
}

const bridgeShare = vi.fn()

beforeEach(() => {
  vi.useFakeTimers()
  listeners.clear()
  sent.length = 0
  domToPngMock.mockReset()
  domToPngMock.mockResolvedValue('data:image/png;base64,QUJD')
  bridgeShare.mockClear()
  ;(window as unknown as Record<string, unknown>).GBotShare = { shareImage: bridgeShare }
})

afterEach(() => {
  delete (window as unknown as Record<string, unknown>).GBotShare
  document.body.innerHTML = ''
  vi.useRealTimers()
})

describe('share integration: entry → menu → capture → bridge', () => {
  it('long-press a done assistant message, share item hands a PNG to the bridge', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [historyMsg()],
      nextCursor: '',
      hasMore: false,
    })
    const row = messageRow(chat)
    expect(row?.textContent).toContain('hello')

    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    // The browser's long-press moment: contextmenu is the primary open path.
    row!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    const menu = chat.root.querySelector('.share-menu')
    expect(menu).not.toBeNull()
    const items = chat.root.querySelectorAll('button.share-menu-item')
    expect(items).toHaveLength(2)

    // rAF is faked with the timers: advance two frames so the offscreen
    // capture's double-rAF gate opens, then let microtasks settle.
    items[0].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await vi.advanceTimersByTimeAsync(16)
    await vi.advanceTimersByTimeAsync(16)
    await Promise.resolve()
    await Promise.resolve()

    expect(bridgeShare).toHaveBeenCalledTimes(1)
    expect(bridgeShare.mock.calls[0][0]).toBe('data:image/png;base64,QUJD')
    expect(bridgeShare.mock.calls[0][1]).toMatch(/^gbot-share-\d{4}-\d{2}-\d{2}-\d{6}\.png$/)
    expect(domToPngMock).toHaveBeenCalledTimes(1)
    chat.cleanup()
  })

  it('lift after expiry opens the menu (non-text fallback path)', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [historyMsg()], nextCursor: '', hasMore: false })
    const row = messageRow(chat)
    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    row!.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).not.toBeNull()
    chat.cleanup()
  })

  it('streaming messages cannot be long-pressed into a menu', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [historyMsg({ status: 'streaming' })],
      nextCursor: '',
      hasMore: false,
    })
    const row = messageRow(chat)
    expect(row).not.toBeNull()
    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    row!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    row!.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    chat.cleanup()
  })
})
