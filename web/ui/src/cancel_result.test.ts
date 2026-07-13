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

// Enter streaming turn, then enqueue messages (each gets stamped on 'queued').
function startStreamAndEnqueue(texts: string[]): Record<string, string> {
  // First send a real user msg + start streaming.
  setTextarea('initial')
  pressEnter()
  events([{ type: 'query_start' }])
  const uuids: Record<string, string> = {}
  for (const t of texts) {
    setTextarea(t)
    pressEnter()
    const stamp = `uuid-${t}`
    dispatch({ type: 'queued', uuid: stamp })
    uuids[t] = stamp
  }
  return uuids
}

// Select queued bubbles inside the InputBar (not Header dropdown panels,
// which also use the modal-enter class). The InputBar's root is .absolute.bottom-0.
function queuedBubbles(): NodeListOf<HTMLElement> {
  const inputBarRoot = document.querySelector('.absolute.bottom-0') as HTMLElement
  return inputBarRoot.querySelectorAll('.modal-enter')
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('cancel_result: queued message restore', () => {
  it('3-queue partial drain + cancel restores drained msgs only', () => {
    mount()
    const stamps = startStreamAndEnqueue(['msg1', 'msg2', 'msg3'])

    // 3 bubbles visible while streaming.
    expect(queuedBubbles().length).toBe(3)

    // Up key triggers cancel_queued for all stamped.
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }),
    )

    // Server reports only msg1 drained (msg2/msg3 still queued, removed).
    dispatch({
      type: 'cancel_result',
      removed: [stamps['msg2'], stamps['msg3']],
    })

    // Input restored with msg2\nmsg3 (NOT msg1 — drained).
    expect(ta.value).toBe('msg2\nmsg3')

    // cancel_queued sent with all three stamped UUIDs.
    const cancelReq = sent.find((m) => m.type === 'cancel_queued')
    expect(cancelReq).toBeTruthy()
    expect(cancelReq.uuids.sort()).toEqual(
      [stamps['msg1'], stamps['msg2'], stamps['msg3']].sort(),
    )
  })

  it('partial cancel_result where some UUIDs already drained', () => {
    mount()
    const stamps = startStreamAndEnqueue(['a', 'b', 'c'])

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }),
    )
    // Only 'a' was successfully removed (b/c drained by engine).
    dispatch({ type: 'cancel_result', removed: [stamps['a']] })
    expect(ta.value).toBe('a')
  })
})

describe('multi-queue: InputBar renders one bubble per queued', () => {
  it('multiple bubbles render; FIFO uuid stamping', () => {
    mount()
    const stamps = startStreamAndEnqueue(['first', 'second', 'third'])
    const bubbles = queuedBubbles()
    expect(bubbles.length).toBe(3)
    // First bubble shows "Tap to CANCEL all" (multi-queue mode).
    expect(bubbles[0].textContent).toContain('Tap to CANCEL all')
    // FIFO: first stamp went to 'first'.
    expect(stamps['first']).toBe('uuid-first')
  })

  it('Up key pops all queued back to input', () => {
    mount()
    const stamps = startStreamAndEnqueue(['one', 'two'])

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }),
    )
    dispatch({
      type: 'cancel_result',
      removed: [stamps['one'], stamps['two']],
    })
    // InputBar.appendQueuedText prefixes new text before existing.
    expect(ta.value).toBe('one\ntwo')
  })

  it('optimistic cancel (no server uuid) restores input immediately', () => {
    mount()
    // Send while streaming — queue without server-stamped uuid.
    setTextarea('initial')
    pressEnter()
    events([{ type: 'query_start' }])
    setTextarea('optimistic msg')
    pressEnter()
    // No dispatch of 'queued' event — uuid stays empty string.
    // ArrowUp triggers onCancelQueued.
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }),
    )
    // Text restored immediately, no cancel_queued sent.
    expect(ta.value).toBe('optimistic msg')
  })
})
