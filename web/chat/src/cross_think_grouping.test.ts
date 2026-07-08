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
}

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

function dispatchEvents(events: unknown[]) {
  for (const e of events) dispatch({ type: 'event', event: e })
}

function send(text: string) {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = text
  ta.dispatchEvent(new Event('input', { bubbles: true }))
  ta.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
}

function blockTypes(container: Element | null): string[] {
  if (!container) return []
  return Array.from(container.children).map(c => {
    const el = c as HTMLElement
    if (el.dataset.thinking) return 'thinking'
    if (el.dataset.toolGroup) return 'group'
    if (el.dataset.toolRoot) return 'tool'
    if (el.classList.contains('md-body')) return 'text'
    if (el.dataset.progress || el.querySelector('.heartbeat') || el.className.includes('progress')) return 'progress'
    return 'other'
  }).filter(t => t !== 'progress')
}

describe('cross-thinking grouping (no rebuild)', () => {
  beforeEach(() => mount())

  it('[think web web think web web] → [group(4 tools, 2 thinking absorbed)]', () => {
    send('test')
    dispatchEvents([
      { type: 'query_start' },
      { type: 'thinking_start' },
      { type: 'thinking_end', thinking: { duration: 1000000 } },
      { type: 'tool_start', tool_use: { id: 'w1', name: 'Web' } },
      { type: 'tool_end', tool_result: { tool_use_id: 'w1', is_error: false } },
      { type: 'tool_start', tool_use: { id: 'w2', name: 'Web' } },
      { type: 'tool_end', tool_result: { tool_use_id: 'w2', is_error: false } },
      { type: 'thinking_start' },
      { type: 'thinking_end', thinking: { duration: 500000 } },
      { type: 'tool_start', tool_use: { id: 'w3', name: 'Web' } },
      { type: 'tool_end', tool_result: { tool_use_id: 'w3', is_error: false } },
      { type: 'tool_start', tool_use: { id: 'w4', name: 'Web' } },
      { type: 'tool_end', tool_result: { tool_use_id: 'w4', is_error: false } },
      { type: 'query_end' },
    ])

    const container = document.querySelector('.space-y-3')
    const groups = container?.querySelectorAll('[data-tool-group]')
    expect(groups!.length).toBe(1)
    expect(groups![0].querySelectorAll('[data-tool-root]').length).toBe(4)
    expect(groups![0].querySelectorAll('[data-thinking]').length).toBe(2)
    expect(blockTypes(container)).toEqual(['group'])
  })
})
