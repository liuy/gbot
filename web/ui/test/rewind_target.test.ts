import { describe, it, expect, beforeEach, vi } from 'vitest'

// Mock WS at the path chat.ts imports. The mock captures every listener
// registered via subscribe() so tests can drive inbound messages through
// dispatch(); sent[] records every outbound payload for assertion.
// Harness copied from chat_attach.test.ts (binary upload paths stripped —
// these tests exercise text prompts only).
type Listener = (msg: unknown) => void
const listeners = new Set<Listener>()
const sent: unknown[] = []

vi.mock('../src/ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => {
        listeners.delete(fn)
      }
    },
    send: (p: unknown) => {
      sent.push(p)
    },
    sendBinary: () => {},
    get connected() {
      return true
    },
  }),
}))

import { createChat } from '../src/chat'

// chat.ts constructs an IntersectionObserver for the scroll button.
class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return []
  }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  // connect_status resets chat state. The session_list_request it emits is
  // not relevant to these tests — drop it so sent[] reflects only what each
  // test sends.
  dispatch({ type: 'connect_status', connected: true })
  sent.length = 0
  return chat
}

// historyMsg builds one entry of the history wire frame, matching the exact
// shapes exercised by chat_attach.test.ts.
function historyMsg(id: string, role: 'user' | 'assistant', text: string) {
  return {
    id,
    role,
    text,
    thinking: [],
    tools: [],
    usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error: '',
    status: 'done' as const,
    startedAt: Date.now(),
  }
}

function seedHistory(msgs: ReturnType<typeof historyMsg>[]) {
  dispatch({ type: 'history', messages: msgs, nextCursor: '', hasMore: false })
}

function setTextarea(value: string) {
  const ta = document.querySelector<HTMLTextAreaElement>('textarea')!
  ta.value = value
  ta.dispatchEvent(new Event('input', { bubbles: true }))
}

function clickSend() {
  const sendBtn = document.querySelector<HTMLButtonElement>(
    'button[aria-label="Send"]',
  )!
  sendBtn.click()
}

function queryStart() {
  dispatch({ type: 'event', event: { type: 'query_start' } })
}

// abortEnd dispatches the aborted query_end frame. rewoundText undefined
// omits the field entirely (legacy daemon wire shape).
function abortEnd(rewoundText?: string) {
  const event: Record<string, unknown> = { type: 'query_end', aborted: true, rewound: true }
  if (rewoundText !== undefined) event.rewound_text = rewoundText
  dispatch({ type: 'event', event })
}

// messagesContainer is the .space-y-7 div that holds rendered user/assistant
// messages. Inputs and queue bubbles live elsewhere (inputBar wrapper).
function messagesContainer(): HTMLElement {
  return document.querySelector('.space-y-7') as HTMLElement
}

function transcript(): string {
  return messagesContainer().textContent ?? ''
}

function textarea(): HTMLTextAreaElement {
  return document.querySelector<HTMLTextAreaElement>('textarea')!
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('abort rewind target', () => {
  it('PrefillAbortAfterQueueMisroute_KeepsOldMessage_RestoresServerText', () => {
    // M1 misroute: a send lands in the queue branch on a stale streaming
    // flag, but the (idle) server runs it directly. The misrouted prompt
    // exists NOWHERE in this client's messages, so the later abort rewind
    // must touch no history and restore the server-reported text only.
    mount()
    seedHistory([historyMsg('m1', 'user', 'old question'), historyMsg('m2', 'assistant', 'old answer')])
    setTextarea('previous question')
    clickSend() // optimistic render (not streaming yet)
    queryStart()
    setTextarea('next question')
    clickSend() // streaming=true -> queue bubble only, no messages entry
    abortEnd('previous question') // first rewind: pops the optimistic render
    queryStart() // the misrouted direct query: assistant shell only
    abortEnd('next question') // server rewound a prompt this client never rendered

    const text = transcript()
    expect(text.includes('old question')).toBe(true)
    expect(text.includes('old answer')).toBe(true)
    expect(text.includes('previous question')).toBe(false)
    expect(textarea().value).toBe('next question')
  })

  it('ObserverClient_AbortRewind_KeepsOldMessage', () => {
    // M2 observer: another client's query — direct sends emit no echo, so
    // this client's messages never contain the in-flight prompt.
    mount()
    seedHistory([historyMsg('m1', 'user', 'old question'), historyMsg('m2', 'assistant', 'old answer')])
    queryStart()
    abortEnd('typed elsewhere')

    expect(transcript().includes('old question')).toBe(true)
    expect(textarea().value).toBe('typed elsewhere')
  })

  it('HappyPath_RewindsExactPrompt', () => {
    mount()
    seedHistory([historyMsg('m1', 'user', 'old question'), historyMsg('m2', 'assistant', 'old answer')])
    setTextarea('fresh question')
    clickSend()
    queryStart()
    abortEnd('fresh question')

    const text = transcript()
    expect(text.includes('fresh question')).toBe(false)
    expect(text.includes('old question')).toBe(true)
    expect(textarea().value).toBe('fresh question')
  })

  it('LegacyFrame_WithoutRewoundText_PopsLastUser', () => {
    // Old daemons send no rewound_text: keep today's behavior — pop the
    // last user message and restore its text.
    mount()
    seedHistory([historyMsg('m1', 'user', 'old question'), historyMsg('m2', 'assistant', 'old answer')])
    setTextarea('fresh question')
    clickSend()
    queryStart()
    abortEnd() // legacy frame: aborted + rewound, no rewound_text

    const text = transcript()
    expect(text.includes('fresh question')).toBe(false)
    expect(text.includes('old question')).toBe(true)
    expect(textarea().value).toBe('fresh question')
  })

  it('DuplicateText_RewindsNewestOccurrence', () => {
    // The backward scan must hit the NEWEST matching message (the just-sent
    // optimistic bubble), never the older identical-text history entry.
    mount()
    seedHistory([historyMsg('m1', 'user', 'dup'), historyMsg('m2', 'assistant', 'a1')])
    setTextarea('dup')
    clickSend()
    queryStart()
    abortEnd('dup')

    const occurrences = transcript().split('dup').length - 1
    expect(occurrences).toBe(1)
  })
})
