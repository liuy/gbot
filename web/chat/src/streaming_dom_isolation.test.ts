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
function events(es: any[]) {
  for (const e of es) dispatch({ type: 'event', event: e })
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  dispatch({ type: 'connect_status', connected: true })
  return chat
}

function setTextarea(value: string) {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = value
  ta.dispatchEvent(new Event('input', { bubbles: true }))
}
function pressEnter() {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
}
function clickStop() {
  const stopBtn = Array.from(document.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('STOP'),
  ) as HTMLButtonElement | undefined
  stopBtn?.click()
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('streaming DOM isolation', () => {
  it('text_delta reaches DOM', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'hello' },
    ])
    expect(
      (document.querySelector('.md-body') as HTMLElement).textContent,
    ).toContain('hello')
  })

  it('text_delta survives subsequent tool_start', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'persist' },
      { type: 'tool_start', tool_use: { id: 't-1', name: 'Bash' } },
    ])
    expect(
      (document.querySelector('.md-body') as HTMLElement).textContent,
    ).toContain('persist')
  })

  it('thinking_delta writes to thinking sink', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: 'reasoning' } },
    ])
    expect(document.body.textContent).toContain('reasoning')
  })

  it('structural events mount sinks (text_start)', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }, { type: 'text_start' }])
    expect(document.querySelector('.md-body')).toBeTruthy()
  })

  it('abort mid-stream keeps partial + interrupt marker', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'partial' },
    ])
    clickStop()
    events([
      { type: 'text_end' },
      { type: 'text_delta', text: '[interrupted]' },
      { type: 'query_end', aborted: true },
    ])
    expect(document.body.textContent).toContain('partial')
    expect(document.body.textContent).toContain('[interrupted]')
  })

  it('empty text_delta is a no-op', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '' },
    ])
    const mdBodies = document.querySelectorAll('.md-body')
    expect(mdBodies.length).toBe(1)
  })

  it('multiple text blocks per turn each get fresh sinks', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'a' },
      { type: 'text_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'b' },
      { type: 'text_end' },
      { type: 'query_end' },
    ])
    expect(document.querySelectorAll('.md-body').length).toBe(2)
  })

  it('interleaved text+thinking both writable', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: 'think' } },
      { type: 'text_start' },
      { type: 'text_delta', text: 'say' },
    ])
    expect(document.body.textContent).toContain('think')
    expect(document.body.textContent).toContain('say')
  })

  it('markdown renders inline during text_delta (**bold** → <strong>)', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '**bold**' },
    ])
    const mdBody = document.querySelector('.md-body') as HTMLElement
    expect(mdBody.querySelector('strong')?.textContent).toBe('bold')
  })

  it('InputBar textarea value is unchanged across text_delta', () => {
    mount()
    setTextarea('q')
    pressEnter()
    setTextarea('preserved')
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'streaming content' },
    ])
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('preserved')
  })
})
