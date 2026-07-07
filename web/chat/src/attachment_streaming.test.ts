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
function events(es: unknown[]) {
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

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('attachment streaming', () => {
  it('text_delta after attachment renders', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: 'see this', source_uuid: '' } },
      },
    })
    events([
      { type: 'text_start' },
      { type: 'text_delta', text: 'response' },
    ])
    expect(document.body.textContent).toContain('see this')
    const mdBody = document.querySelector('.md-body') as HTMLElement
    expect(mdBody.textContent).toContain('response')
  })

  it('attachment event renders user msg with prompt text', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: 'look here', source_uuid: '' } },
      },
    })
    expect(document.body.textContent).toContain('look here')
  })

  it('attachment after query_end renders as user msg then text_delta in new turn', () => {
    mount()
    setTextarea('q1')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'first' },
      { type: 'query_end' },
    ])
    // Attachment between queries: rendered as standalone user msg.
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: 'second prompt', source_uuid: '' } },
      },
    })
    expect(document.body.textContent).toContain('second prompt')
    // New turn streams text.
    events([
      { type: 'turn_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'second response' },
    ])
    expect(document.body.textContent).toContain('second response')
  })
})
