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
  return inputBarRoot.querySelectorAll('.queued-bubble')
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('cancel_result: queued message restore', () => {
  it('3-queue partial drain + cancel restores only what the server returned', () => {
    mount()
    const stamps = startStreamAndEnqueue(['msg1', 'msg2', 'msg3'])
    expect(queuedBubbles().length).toBe(3)

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))

    // Server popped only msg2/msg3 (msg1 already drained as a turn).
    dispatch({
      type: 'cancel_result',
      removed: [stamps['msg2'], stamps['msg3']],
      restored: [{ text: 'msg2' }, { text: 'msg3' }],
    })

    expect(ta.value).toBe('msg2\nmsg3')
    const cancelReq = sent.find((m) => m.type === 'cancel_queued') as { uuids: string[] }
    expect(cancelReq).toBeTruthy()
    expect(cancelReq.uuids.sort()).toEqual(
      [stamps['msg1'], stamps['msg2'], stamps['msg3']].sort(),
    )
  })

  it('partial cancel_result where some UUIDs already drained', () => {
    mount()
    const stamps = startStreamAndEnqueue(['a', 'b', 'c'])

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    dispatch({
      type: 'cancel_result',
      removed: [stamps['a']],
      restored: [{ text: 'a' }],
    })
    expect(ta.value).toBe('a')
  })

  it('Up key sends cancel_queued; restore waits for the server payload', () => {
    mount()
    const stamps = startStreamAndEnqueue(['one', 'two'])

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    // Nothing restored yet — the server owns the pop now.
    expect(ta.value).toBe('')
    const cancelReq = sent.find((m) => m.type === 'cancel_queued') as { uuids: string[] }
    expect(cancelReq.uuids.sort()).toEqual([stamps['one'], stamps['two']].sort())

    dispatch({
      type: 'cancel_result',
      removed: [stamps['one'], stamps['two']],
      restored: [{ text: 'one' }, { text: 'two' }],
    })
    // InputBar.appendQueuedText prefixes new text before existing.
    expect(ta.value).toBe('one\ntwo')
  })

  it('optimistic cancel (no server uuid) routes through the server pop-all', () => {
    mount()
    setTextarea('initial')
    pressEnter()
    events([{ type: 'query_start' }])
    setTextarea('optimistic msg')
    pressEnter()
    // No 'queued' event dispatched — uuids all empty.

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))

    // Empty uuid list = pop-all: the server-side item must not leak into
    // the next turn, and the restore rides on cancel_result.
    const cancelReq = sent.find((m) => m.type === 'cancel_queued') as { uuids: string[] }
    expect(cancelReq).toBeTruthy()
    expect(cancelReq.uuids).toEqual([])
    expect(ta.value).toBe('')

    dispatch({
      type: 'cancel_result',
      removed: ['server-side-uuid'],
      restored: [{ text: 'optimistic msg' }],
    })
    expect(ta.value).toBe('optimistic msg')
  })

  it('restored attachments rebuild chips that re-send without re-upload', () => {
    mount()
    setTextarea('initial')
    pressEnter()
    events([{ type: 'query_start' }])
    setTextarea('with pic')
    pressEnter()
    dispatch({ type: 'queued', uuid: 'u1' })

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    dispatch({
      type: 'cancel_result',
      removed: ['u1'],
      restored: [
        {
          text: 'with pic',
          attachments: [{ id: 'fresh-id', mime: 'image/png', size: 1234 }],
        },
      ],
    })
    expect(ta.value).toBe('with pic')

    // Chip rebuilt (image chip renders an img with alt = synthesized name).
    const chipImg = document.querySelector(
      '.absolute.bottom-0 img[alt^="image."]',
    ) as HTMLImageElement | null
    if (!chipImg) throw new Error('restored image chip not rendered')

    // Re-send while idle: the frame references the fresh id and the REAL
    // size (the stub File is zero-byte).
    events([{ type: 'query_end' }])
    setTextarea('again')
    pressEnter()
    const frame = sent
      .filter((m) => (m as { type?: string }).type === 'message')
      .pop() as { attachments?: { id: string; size: number }[] }
    if (!frame || !frame.attachments) {
      throw new Error('re-send message frame with attachments never sent')
    }
    expect(frame.attachments).toEqual([{ id: 'fresh-id', name: 'image.png', mime: 'image/png', size: 1234 }])
  })
})

describe('multi-queue: InputBar renders one bubble per queued', () => {
  it('multiple bubbles render; FIFO uuid stamping', () => {
    mount()
    const stamps = startStreamAndEnqueue(['first', 'second', 'third'])
    const bubbles = queuedBubbles()
    expect(bubbles.length).toBe(3)
    expect(bubbles[0].textContent).toContain('Tap to CANCEL all')
    expect(stamps['first']).toBe('uuid-first')
  })
})
