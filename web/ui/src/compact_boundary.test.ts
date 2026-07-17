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
    return el.textContent === 'compact'
  }) as HTMLElement[]
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('compact boundary divider (client)', () => {
  it('renders divider as LAST child of prepended fragment on final page', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('pre-0', 'a'), userMsg('pre-1', 'b'), userMsg('pre-2', 'c')],
      nextCursor: '',
      hasMore: false,
      compactBoundary: true,
    })
    const dividers = dividerElements()
    expect(dividers.length).toBe(1)
    const divider = dividers[0]
    // Divider must sit AFTER every prepended message DOM root. The container's
    // first three children are the three pre-compact messages; the divider is
    // the fourth.
    const container = messagesContainer()
    const dividerContainer = divider.parentElement as HTMLElement
    expect(dividerContainer.parentElement).toBe(container)
    const containerChildren = Array.from(container.children)
    const dividerIdx = containerChildren.indexOf(dividerContainer)
    expect(dividerIdx).toBe(3)
  })

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

  it('multi-page flow: only the final flagged page renders one divider', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('p1-0', 'a')],
      nextCursor: 'precompact:30',
      hasMore: true,
    })
    expect(dividerElements().length).toBe(0)
    dispatch({
      type: 'history',
      messages: [userMsg('p2-0', 'b')],
      nextCursor: '',
      hasMore: false,
      compactBoundary: true,
    })
    expect(dividerElements().length).toBe(1)
  })

  it('resetAllState (connect_status) clears the divider', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('pre-0', 'a')],
      nextCursor: '',
      hasMore: false,
      compactBoundary: true,
    })
    expect(dividerElements().length).toBe(1)
    // Trigger resetAllState via a fresh connect_status with new engineID.
    dispatch({
      type: 'connect_status',
      connected: true,
      engineID: 'new-engine',
    })
    expect(dividerElements().length).toBe(0)
  })

  it('divider uses the exact design-token class names', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('pre-0', 'a')],
      nextCursor: '',
      hasMore: false,
      compactBoundary: true,
    })
    const container = messagesContainer()
    const dividerContainer = Array.from(container.children).find((c) => {
      const el = c as HTMLElement
      return el.className.includes('flex items-center gap-2 my-4 px-4')
    }) as HTMLElement
    expect(dividerContainer).toBeTruthy()
    expect(dividerContainer.className).toBe('flex items-center gap-2 my-4 px-4')
    const lines = dividerContainer.querySelectorAll('.flex-1.border-t.border-hairline')
    expect(lines.length).toBe(2)
    const label = dividerContainer.querySelector('.text-blue.text-\\[10px\\].shrink-0') as HTMLElement
    expect(label).toBeTruthy()
    expect(label.textContent).toBe('compact')
  })

  it('empty page with compactBoundary still renders exactly one divider', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [],
      nextCursor: '',
      hasMore: false,
      compactBoundary: true,
    })
    const dividers = dividerElements()
    expect(dividers.length).toBe(1)
    const container = messagesContainer()
    // The divider is the only child (no messages were prepended).
    expect(container.children.length).toBe(1)
  })

  it('two consecutive flagged pages render only ONE divider (guard)', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('a', 'a')],
      nextCursor: '',
      hasMore: false,
      compactBoundary: true,
    })
    dispatch({
      type: 'history',
      messages: [userMsg('b', 'b')],
      nextCursor: '',
      hasMore: false,
      compactBoundary: true,
    })
    expect(dividerElements().length).toBe(1)
  })
})
