import { describe, it, expect, beforeEach, vi } from 'vitest'

// Mock WS at the path chat.ts imports. The mock captures every listener
// registered via subscribe() so tests can drive inbound messages through
// dispatch(); sent[] records every outbound payload (text + binary) for
// assertion.
type Listener = (msg: unknown) => void
const listeners = new Set<Listener>()
const sent: unknown[] = []
const sentBinary: ArrayBuffer[] = []

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
    sendBinary: (d: ArrayBuffer) => {
      sentBinary.push(d)
    },
    connected: true,
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
  // test sends. No uploadToken in the new wire shape.
  dispatch({ type: 'connect_status', connected: true })
  sent.length = 0
  sentBinary.length = 0
  return chat
}

function attachFile(file: File) {
  const fileInput = document.querySelector<HTMLInputElement>('input[type="file"]')!
  Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
  fileInput.dispatchEvent(new Event('change'))
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

// messagesContainer is the .space-y-7 div that holds rendered user/assistant
// messages. Inputs and chips live elsewhere (inputBar wrapper) so scoping
// img/span queries here avoids picking up chip thumbnails.
function messagesContainer(): HTMLElement {
  return document.querySelector('.space-y-7') as HTMLElement
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  sentBinary.length = 0
  document.body.innerHTML = ''
})

describe('chat attachment rendering', () => {
  it('UserMessageWithImage_SendsUserMessageFirstThenStreams', async () => {
    mount()
    setTextarea('look at this')
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    attachFile(file)
    clickSend()

    // onSend awaits sendAttachmentViaWS (which slices the file into chunks
    // and writes binary frames). Wait for the rendered <img> to appear in
    // the messages container before asserting.
    await vi.waitFor(() => {
      const imgs = messagesContainer().querySelectorAll('img')
      expect(imgs.length).toBe(1)
    })

    // First outbound frame MUST be the user_message — it carries the
    // attachments metadata so the server enters waiting state before any
    // bytes arrive.
    expect(sent.length).toBeGreaterThanOrEqual(1)
    const first = sent[0] as { type: string; text: string; attachments: unknown[] }
    expect(first.type).toBe('message')
    expect(first.text).toBe('look at this')
    expect(first.attachments.length).toBe(1)
    const att = first.attachments[0] as {
      id: string
      name: string
      mime: string
      size: number
    }
    expect(att.name).toBe('photo.png')
    expect(att.mime).toBe('image/png')
    expect(att.size).toBe(file.size)
    expect(typeof att.id).toBe('string')
    expect(att.id.length).toBeGreaterThan(0)

    // Subsequent text frames: attachment_start + attachment_end. Binary
    // frames go through sendBinary (counted in sentBinary, not sent).
    const subsequent = sent.slice(1) as Array<{ type?: string }>
    expect(subsequent.length).toBe(2)
    expect(subsequent[0].type).toBe('attachment_start')
    expect(subsequent[1].type).toBe('attachment_end')
    expect(sentBinary.length).toBe(1)
    expect(sentBinary[0].byteLength).toBe(file.size)

    // Rendered <img> uses the blob previewURL.
    const img = messagesContainer().querySelector('img') as HTMLImageElement
    expect(img.src.startsWith('blob:')).toBe(true)
    expect(img.alt).toBe('photo.png')
  })

  it('HistoryReplayImageBlock_RendersImgTag', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm1',
          role: 'user',
          // mapHistoryToChatMessages skips user rows whose text is empty,
          // so the row must carry non-empty text even when the payload is
          // an image. text='look' mirrors what the backend stores when a
          // user sends text+image together (text block concatenated).
          text: 'look',
          blocks: [{ kind: 'image', src: 'data:image/png;base64,abc' }],
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: Date.now(),
        },
      ],
      nextCursor: '',
      hasMore: false,
    })

    const imgs = messagesContainer().querySelectorAll('img')
    expect(imgs.length).toBe(1)
    expect(imgs[0].getAttribute('src')).toBe('data:image/png;base64,abc')
  })

  it('HistoryReplayDocumentPrefix_RendersChip', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm1',
          role: 'user',
          // The [Document: name saved at path]\n prefix is emitted by the
          // backend (parseDocument) and parsed client-side into a chip + a
          // trailing text span. body text after the prefix must render as a
          // separate node so the chip is a visual marker, not the message.
          text: '[Document: foo.pdf saved at /cache/documents/foo.pdf]\nbody text',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: Date.now(),
        },
      ],
      nextCursor: '',
      hasMore: false,
    })

    // Chip is a span whose entire text is `[foo.pdf]`.
    const chips = Array.from(messagesContainer().querySelectorAll('span')).filter(
      (s) => s.textContent === '[foo.pdf]',
    )
    expect(chips.length).toBe(1)

    // Remainder renders as a separate span (whitespace-pre-wrap) whose text
    // equals the post-prefix body. A concatenated chip+body text node would
    // fail this assertion.
    const rest = Array.from(messagesContainer().querySelectorAll('span')).filter(
      (s) => s.textContent === 'body text',
    )
    expect(rest.length).toBe(1)
  })

  it('TextOnlyMessage_SendsMessageWithNoAttachments', async () => {
    mount()
    setTextarea('hello there')
    clickSend()
    // One outbound frame: just the user_message.
    await vi.waitFor(() => {
      expect(sent.length).toBe(1)
    })
    const m = sent[0] as { type: string; text: string; attachments?: unknown }
    expect(m.type).toBe('message')
    expect(m.text).toBe('hello there')
    expect(m.attachments).toBeUndefined()
    expect(sentBinary.length).toBe(0)
  })

  it('SendDuringStreaming_QueuesInsteadOfRendering', async () => {
    mount()
    // Drive chat into streaming state — query_start triggers initStreaming.
    dispatch({
      type: 'event',
      event: { type: 'query_start' },
    })
    setTextarea('mid-stream message')
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    attachFile(file)
    clickSend()

    await vi.waitFor(() => {
      // user_message + attachment_start + attachment_end = 3 text frames.
      // (The single binary chunk is captured in sentBinary, not sent.)
      expect(sent.length).toBe(3)
      expect(sentBinary.length).toBe(1)
    })

    // No new user-message node should appear in the transcript —
    // query_start for the queued turn will render it when the engine picks
    // the message up.
    const userImgs = messagesContainer().querySelectorAll('img')
    expect(userImgs.length).toBe(0)

    // First frame is still user_message with attachments.
    const first = sent[0] as { type: string; attachments: unknown[] }
    expect(first.type).toBe('message')
    expect(first.attachments.length).toBe(1)
  })
})
