import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createChat } from './chat'

class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return [] }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

type Listener = (msg: unknown) => void
const listeners: Set<Listener> = new Set()
const sent: unknown[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => { listeners.add(fn); return () => { listeners.delete(fn) } },
    send: (p: unknown) => { sent.push(p) },
    connected: true,
  }),
}))

function mount() {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

function send(text: string) {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = text
  ta.dispatchEvent(new Event('input', { bubbles: true }))
  ta.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
}

describe('thinking_delta scroll', () => {
  beforeEach(() => {
    mount()
  })

  it('auto-scrolls during thinking_delta when near bottom', async () => {
    send('test')
    const chat = mount()
    const scrollEl = chat.scrollEl
    Object.defineProperty(scrollEl, 'scrollHeight', { configurable: true, get: () => 500 })
    Object.defineProperty(scrollEl, 'clientHeight', { configurable: true, get: () => 400 })
    // Position near bottom so isNearBottom is true after the scroll listener fires.
    scrollEl.scrollTop = 500 - 400

    dispatch({ type: 'event', event: { type: 'query_start' } })
    dispatch({ type: 'event', event: { type: 'thinking_start' } })
    dispatch({ type: 'event', event: { type: 'thinking_delta', thinking: { text: 'A'.repeat(2000) } } })

    await new Promise((resolve) => requestAnimationFrame(resolve))
    expect(scrollEl.scrollTop).toBe(scrollEl.scrollHeight)
  })
})
