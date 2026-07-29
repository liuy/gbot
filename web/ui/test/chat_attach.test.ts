import { describe, it, expect, beforeEach, vi, type Mock } from 'vitest'

// Mock WS at the path chat.ts imports. The mock captures every listener
// registered via subscribe() so tests can drive inbound messages through
// dispatch(); sent[] records every outbound payload (text + binary) for
// assertion.
//
// connState + binaryCount let a test flip `connected` to false after the
// Nth binary write — mirroring a real WS going down mid-upload so that
// sendAttachmentViaWS's between-chunks re-check throws. getConnection()
// returns an object whose `connected` is a getter over connState so the
// re-check sees the live value after sendBinary flips it.
type Listener = (msg: unknown) => void
const listeners = new Set<Listener>()
const sent: unknown[] = []
const sentBinary: ArrayBuffer[] = []

let connState: { connected: boolean; disconnectAfterBinary?: number }
let binaryCount: number

function installConn(opts: { connected?: boolean; disconnectAfterBinary?: number } = {}) {
  connState = {
    connected: opts.connected ?? true,
    disconnectAfterBinary: opts.disconnectAfterBinary,
  }
  binaryCount = 0
}

vi.mock('../src/ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => {
        listeners.delete(fn)
      }
    },
    subscribeBinary: () => () => {},
    send: (p: unknown) => {
      sent.push(p)
    },
    sendBinary: (d: ArrayBuffer) => {
      sentBinary.push(d)
      binaryCount++
      if (
        connState.disconnectAfterBinary !== undefined &&
        binaryCount >= connState.disconnectAfterBinary
      ) {
        connState.connected = false
      }
    },
    get connected() {
      return connState.connected
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
  sentBinary.length = 0
  return chat
}

function attachFile(file: File, kind: 'image' | 'document' = 'image') {
  // Three file inputs exist (camera/image/doc). Select by kind to avoid
  // ambiguity from DOM-order-dependent querySelector('input[type="file"]').
  const selector =
    kind === 'image'
      ? 'input[accept="image/*"][multiple]' // imageInput (multiple, no capture)
      : 'input[accept]:not([accept*="image/*"])' // docInput (no image/*)
  const fileInput = document.querySelector<HTMLInputElement>(selector)!
  Object.defineProperty(fileInput, 'files', { value: [file], configurable: true, writable: true })
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

// dispatchPaste: simulates Android WebView paste via beforeinput +
// insertFromPaste (production code uses beforeinput, not paste, because
// Android IMEs often bypass paste events). Returns { evt, spy } for
// preventDefault assertions.
function dispatchPaste(textarea: HTMLTextAreaElement, text: string): { evt: InputEvent; spy: Mock } {
  const evt = new InputEvent('beforeinput', { bubbles: true, cancelable: true })
  Object.defineProperty(evt, 'inputType', {
    value: 'insertFromPaste',
    writable: false, configurable: true,
  })
  Object.defineProperty(evt, 'data', {
    value: text,
    writable: false, configurable: true,
  })
  Object.defineProperty(evt, 'dataTransfer', {
    value: { getData: (t: string) => t === 'text/plain' ? text : '' },
    writable: false, configurable: true,
  })
  const spy = vi.spyOn(evt as unknown as { preventDefault: () => void }, 'preventDefault')
  textarea.dispatchEvent(evt)
  return { evt, spy }
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
  installConn()
})

describe('chat attachment rendering', () => {
  it('UserMessageWithImage_UploadsFirstThenCommits', async () => {
    mount()
    setTextarea('look at this')
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    attachFile(file)
    clickSend()

    // onSend awaits sendAttachmentViaWS before sending the user_message.
    // Wait for the rendered <img> to appear in the messages container
    // (renderUserMessage runs only after the commit frame is sent).
    await vi.waitFor(() => {
      const imgs = messagesContainer().querySelectorAll('img')
      expect(imgs.length).toBe(1)
    })

    // Two-phase commit order: attachment_start + attachment_end (upload),
    // then a single user_message with attachments metadata (commit).
    expect(sent.length).toBe(3)
    const start = sent[0] as { type: string; id: string; name: string; mime: string; size: number }
    expect(start.type).toBe('attachment_start')
    expect(start.name).toBe('photo.png')
    expect(start.mime).toBe('image/png')
    expect(start.size).toBe(file.size)
    expect(typeof start.id).toBe('string')
    expect(start.id.length).toBeGreaterThan(0)

    const end = sent[1] as { type: string; id: string }
    expect(end.type).toBe('attachment_end')
    expect(end.id).toBe(start.id)

    const msg = sent[2] as {
      type: string
      text: string
      attachments: Array<{ id: string; name: string; mime: string; size: number }>
    }
    expect(msg.type).toBe('message')
    expect(msg.text).toBe('look at this')
    expect(msg.attachments.length).toBe(1)
    expect(msg.attachments[0].id).toBe(start.id)
    expect(msg.attachments[0].name).toBe('photo.png')

    // One binary chunk (small file) — captured in sentBinary, not sent.
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

    const chips = Array.from(messagesContainer().querySelectorAll('span')).filter(
      (s) => s.textContent === '[foo.pdf]',
    )
    expect(chips.length).toBe(1)

    const rest = Array.from(messagesContainer().querySelectorAll('span')).filter(
      (s) => s.textContent === 'body text',
    )
    expect(rest.length).toBe(1)
  })

  it('TextOnlyMessage_SendsMessageWithNoAttachments', async () => {
    mount()
    setTextarea('hello there')
    clickSend()
    // One outbound frame: just the user_message (no upload phase).
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
      // attachment_start + attachment_end + user_message = 3 text frames.
      // (The single binary chunk is captured in sentBinary, not sent.)
      expect(sent.length).toBe(3)
      expect(sentBinary.length).toBe(1)
    })

    // No new user-message node should appear in the transcript —
    // query_start for the queued turn will render it when the engine picks
    // the message up.
    const userImgs = messagesContainer().querySelectorAll('img')
    expect(userImgs.length).toBe(0)

    // Two-phase commit: the user_message is the LAST frame (commit), not
    // the first.
    const start = sent[0] as { type: string }
    const end = sent[1] as { type: string }
    const msg = sent[2] as { type: string; attachments: unknown[] }
    expect(start.type).toBe('attachment_start')
    expect(end.type).toBe('attachment_end')
    expect(msg.type).toBe('message')
    expect(msg.attachments.length).toBe(1)
  })

  it('PartialFailure_MarksChipRed_DoesNotCommit', async () => {
    // First file uploads successfully (1 binary); second file's binary
    // write flips connected=false, so sendAttachmentViaWS throws on its
    // between-chunks re-check and the loop breaks.
    installConn({ disconnectAfterBinary: 2 })
    mount()
    setTextarea('two files')
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const f1 = new File([blob], 'a.png', { type: 'image/png' })
    const f2 = new File([blob], 'b.png', { type: 'image/png' })
    attachFile(f1)
    attachFile(f2)
    clickSend()

    // Wait for the failed chip to render with the red border class.
    await vi.waitFor(() => {
      const failed = document.querySelectorAll('.border-red-500')
      expect(failed.length).toBe(1)
    })

    // Commit frame must NOT be sent — onSend returns before conn.send when
    // anyFailed is true.
    const hasMessage = sent.some((m) => (m as { type?: string }).type === 'message')
    expect(hasMessage).toBe(false)

    // Draft restored so the user can retry without retyping.
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('two files')
  })

  it('StripCleared_AfterSuccessfulSend_NewFileReuploads', async () => {
    // After a successful commit, removeAttachments clears the chip strip so
    // the refs (and their uploadedIDs) do NOT leak into the next send. A
    // fresh attachment on the next send is a new ref with uploadedID=
    // undefined and must re-upload from scratch.
    installConn()
    mount()
    setTextarea('first send')
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const f1 = new File([blob], 'a.png', { type: 'image/png' })
    attachFile(f1)
    clickSend()

    // First send: 1 attachment_start + 1 binary + 1 attachment_end + 1 message.
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })
    expect(sentBinary.length).toBe(1)
    expect(document.querySelectorAll('.border-red-500').length).toBe(0)

    // Second send: new file (new ref with uploadedID=undefined) must
    // re-upload — strip was cleared by removeAttachments after the first
    // successful commit.
    sent.length = 0
    sentBinary.length = 0
    setTextarea('second send')
    const f2 = new File([blob], 'b.png', { type: 'image/png' })
    attachFile(f2)
    clickSend()
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })
    expect(sentBinary.length).toBe(1)
  })

  it('RetryThenResend_SkipsAlreadyUploaded_UploadsOnlyFailed', async () => {
    // After a partial failure (f1 uploaded, f2 failed) the chips stay in the
    // strip: f1's ref retains its uploadedID, f2's does not. A resend must
    // exercise the `if (ref.uploadedID) continue` branch in onSend — skip
    // f1 (bytes already staged server-side) and upload ONLY f2.
    //
    // Mutation guard: flipping the condition to `if (!ref.uploadedID)
    // continue` re-uploads f1 (fresh id != original) and skips f2 (commit id
    // is undefined). Both the id-match and the string-id assertions fail.
    installConn({ disconnectAfterBinary: 2 })
    mount()
    setTextarea('two files')
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const f1 = new File([blob], 'a.png', { type: 'image/png' })
    const f2 = new File([blob], 'b.png', { type: 'image/png' })
    attachFile(f1)
    attachFile(f2)
    clickSend()

    // First send: f1 uploads (1 binary), f2's binary write flips connected
    // -> f2 marked failed, loop breaks, NO commit frame sent.
    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(1)
    })
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)

    // Capture f1's uploadedID from its attachment_start frame BEFORE reset
    // (it equals the id onSend assigned and stored on the ref).
    const f1Start = sent.find(
      (m) =>
        (m as { type?: string; name?: string }).type === 'attachment_start' &&
        (m as { name?: string }).name === 'a.png',
    ) as { id: string } | undefined
    if (!f1Start) throw new Error('first send did not emit attachment_start for a.png')
    const f1OriginalID = f1Start.id

    // Fresh connection + cleared frames for the resend.
    installConn()
    sent.length = 0
    sentBinary.length = 0

    // Draft was restored after the failed send — clickSend re-fires onSend.
    clickSend()

    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })

    // KEY ASSERTION: only f2's bytes hit the wire. If the skip branch is
    // removed entirely, both files upload -> length 2.
    expect(sentBinary.length).toBe(1)

    const msg = sent.find((m) => (m as { type?: string }).type === 'message') as {
      attachments: Array<{ id: string; name: string }>
    }
    expect(msg.attachments.length).toBe(2)

    // Every commit id must be a non-empty string. If the skip condition is
    // flipped (!uploadedID), f2 is skipped and its commit id is undefined.
    for (const att of msg.attachments) {
      expect(typeof att.id).toBe('string')
      expect(att.id.length).toBeGreaterThan(0)
    }

    // f1's commit id must equal its ORIGINAL uploadedID — the skip branch
    // reused the staged id instead of re-uploading. If the condition is
    // flipped, f1 is re-uploaded and gets a fresh id != f1OriginalID.
    const f1Commit = msg.attachments.find((a) => a.name === 'a.png')
    if (!f1Commit) throw new Error('commit message missing a.png attachment')
    expect(f1Commit.id).toBe(f1OriginalID)
  })
})

describe('chat paste integration', () => {
  it('PasteOnly_SendsAsMessageText_NoAttachmentFrames', async () => {
    mount()
    setTextarea('explain')
    const paste = 'code\nline2\nline3\nline4'
    const ta = document.querySelector<HTMLTextAreaElement>('textarea')!
    dispatchPaste(ta, paste)
    clickSend()

    await vi.waitFor(() => {
      expect(sent.length).toBe(1)
    })

    const m = sent[0] as { type: string; text: string; attachments?: unknown }
    expect(m.type).toBe('message')
    expect(m.text).toBe('explain\n\n' + paste)
    expect(m.attachments).toBeUndefined()
    expect(sentBinary.length).toBe(0)

    const text = messagesContainer().textContent ?? ''
    expect(text.includes('explain')).toBe(true)
    expect(text.includes('code')).toBe(true)
  })

  it('PastePlusImage_UploadsImageOnly_InlinesPaste', async () => {
    mount()
    setTextarea('see this')
    const paste = 'first line\nsecond line\nthird line\nfourth line'
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    attachFile(file)
    const ta = document.querySelector<HTMLTextAreaElement>('textarea')!
    dispatchPaste(ta, paste)
    clickSend()

    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })

    expect(sent.length).toBe(3)
    expect((sent[0] as { type: string }).type).toBe('attachment_start')
    expect((sent[1] as { type: string }).type).toBe('attachment_end')
    const msg = sent[2] as {
      type: string
      text: string
      attachments: Array<{ id: string; name: string }>
    }
    expect(msg.type).toBe('message')
    expect(msg.text).toBe('see this\n\n' + paste)
    expect(msg.attachments.length).toBe(1)
    expect(msg.attachments[0].name).toBe('photo.png')
    expect(sentBinary.length).toBe(1)
  })

  it('FailedImageUpload_KeepsPasteChip_RestoresTypedText', async () => {
    installConn({ disconnectAfterBinary: 1 })
    mount()
    setTextarea('hi')
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    attachFile(file)
    const ta = document.querySelector<HTMLTextAreaElement>('textarea')!
    dispatchPaste(ta, 'pasted\nline2\nline3\nline4')
    clickSend()

    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(1)
    })

    // Typed text restored — NOT the paste-inlined fullText.
    const taAfter = document.querySelector<HTMLTextAreaElement>('textarea')!
    expect(taAfter.value).toBe('hi')
    // Paste chip stays in the strip for retry.
    expect(document.querySelector('[data-paste-chip]')).not.toBeNull()
    // No commit frame sent.
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)
  })
})
