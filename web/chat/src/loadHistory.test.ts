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

type Listener = (msg: any) => void
const listeners: Set<Listener> = new Set()
const sent: any[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: any) => sent.push(p),
    connected: true,
  }),
}))

function dispatch(msg: any) {
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
    role: 'user',
    text,
    thinking: [],
    tools: [],
    usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error: '',
    status: 'done',
    startedAt: 0,
  }
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('loadHistory', () => {
  it('initial load replaces DOM children', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('m-1', 'one'), userMsg('m-2', 'two')],
      nextCursor: '',
      hasMore: false,
    })
    expect(document.body.textContent).toContain('one')
    expect(document.body.textContent).toContain('two')
  })

  it('pagination prepends older messages (dedupes by id)', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('m-2', 'two')],
      nextCursor: 'c1',
      hasMore: true,
    })
    // Server returns m-2 + m-1; m-2 already present.
    dispatch({
      type: 'history',
      messages: [userMsg('m-2', 'two-dupe'), userMsg('m-1', 'one')],
      nextCursor: '',
      hasMore: false,
    })
    expect(document.body.textContent).toContain('one')
    // m-2 deduped: original kept (no 'two-dupe').
    expect(document.body.textContent).not.toContain('two-dupe')
  })

  it('empty deduped page does not advance cursor', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('m-1', 'first')],
      nextCursor: 'c1',
      hasMore: true,
    })
    // Trigger prefetch first (initial load with hasMore → prefetch).
    const initialReqs = sent.filter((m) => m.type === 'history_request')
    expect(initialReqs.length).toBe(1)

    // Server returns only dupes.
    dispatch({
      type: 'history',
      messages: [userMsg('m-1', 'first-dupe')],
      nextCursor: 'c2',
      hasMore: true,
    })
    // No new DOM children.
    expect(document.body.textContent).not.toContain('first-dupe')
  })

  it('connect_status resets pagination (expectingInitial)', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('m-1', 'old')],
      nextCursor: 'c1',
      hasMore: false,
    })
    expect(document.body.textContent).toContain('old')
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [userMsg('m-2', 'fresh')],
      nextCursor: '',
      hasMore: false,
    })
    expect(document.body.textContent).not.toContain('old')
    expect(document.body.textContent).toContain('fresh')
  })

  it('prefetch second page after initial load', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('m-1', 'first')],
      nextCursor: 'c1',
      hasMore: true,
    })
    const reqs = sent.filter((m) => m.type === 'history_request')
    expect(reqs.length).toBe(1)
    expect(reqs[0].cursor).toBe('c1')
  })
})
