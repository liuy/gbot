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
    subscribeBinary: () => () => {},
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

function messagesContainer(): HTMLElement {
  return document.getElementsByClassName('space-y-7')[0] as HTMLElement
}

function dividerElements(): HTMLElement[] {
  const all = document.querySelectorAll('.text-blue.text-\\[10px\\]')
  return Array.from(all).filter((el) => {
    return el.textContent === 'Compact'
  }) as HTMLElement[]
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('compact boundary divider (client)', () => {
  it('intermediate page without compactBoundary emits NO divider', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('p1-0', 'x'), userMsg('p1-1', 'y')],
      nextCursor: 'precompact:30',
      hasMore: true,
    })
    expect(dividerElements().length).toBe(0)
  })

  it('in-page compactBoundary marker renders divider at marker position', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        userMsg('pre-1', 'a'),
        { id: 'b1', role: 'system', compactBoundary: true, text: '', thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, error: '', status: 'done', startedAt: 0 },
        userMsg('pre-0', 'b'),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const dividers = dividerElements()
    expect(dividers.length).toBe(1)
    const container = messagesContainer()
    const dividerContainer = dividers[0].parentElement as HTMLElement
    const children = Array.from(container.children)
    const dividerIdx = children.indexOf(dividerContainer)
    // Container layout: [timeDiv(anchor), userMsg('pre-1'), compactDiv, userMsg('pre-0')].
    expect(dividerIdx).toBe(2)
  })

  it('multiple in-page markers each render their own divider', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        userMsg('m1', 'a'),
        { id: 'b1', role: 'system', compactBoundary: true, text: '', thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, error: '', status: 'done', startedAt: 0 },
        userMsg('m2', 'b'),
        { id: 'b2', role: 'system', compactBoundary: true, text: '', thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, error: '', status: 'done', startedAt: 0 },
        userMsg('m3', 'c'),
        { id: 'b3', role: 'system', compactBoundary: true, text: '', thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, error: '', status: 'done', startedAt: 0 },
        userMsg('m4', 'd'),
      ],
      nextCursor: '',
      hasMore: false,
    })
    expect(dividerElements().length).toBe(3)
  })

  it('in-page marker at start of page renders divider as first child', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        { id: 'b1', role: 'system', compactBoundary: true, text: '', thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, error: '', status: 'done', startedAt: 0 },
        userMsg('pre-0', 'a'),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const dividers = dividerElements()
    expect(dividers.length).toBe(1)
    const container = messagesContainer()
    const dividerContainer = dividers[0].parentElement as HTMLElement
    expect(Array.from(container.children).indexOf(dividerContainer)).toBe(0)
  })

  it('in-page marker at end of page renders divider as last child', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        userMsg('pre-0', 'a'),
        { id: 'b1', role: 'system', compactBoundary: true, text: '', thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '',
      hasMore: false,
    })
    const dividers = dividerElements()
    expect(dividers.length).toBe(1)
    const container = messagesContainer()
    const dividerContainer = dividers[0].parentElement as HTMLElement
    const children = Array.from(container.children)
    expect(children.indexOf(dividerContainer)).toBe(children.length - 1)
  })
})
