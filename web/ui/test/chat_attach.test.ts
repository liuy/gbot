import { describe, it, expect, beforeEach, afterEach, vi, type Mock } from 'vitest'

// Mock WS at the path chat.ts imports. The mock captures every listener
// registered via subscribe() so tests can drive inbound messages through
// dispatch(); sent[] records every outbound payload for assertion.
// onStateChange is captured too so tests can push 'reconnecting' (chat.ts
// aborts in-flight uploads there).
type Listener = (msg: unknown) => void
type StateListener = (cs: string) => void
const listeners = new Set<Listener>()
const stateListeners = new Set<StateListener>()
const sent: unknown[] = []

let connState: { connected: boolean }

function installConn(opts: { connected?: boolean } = {}) {
  connState = { connected: opts.connected ?? true }
}

function pushState(cs: string) {
  stateListeners.forEach((fn) => fn(cs))
}

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
    onStateChange: (fn: StateListener) => {
      stateListeners.add(fn)
      return () => {
        stateListeners.delete(fn)
      }
    },
    get connected() {
      return connState.connected
    },
  }),
}))

import { createChat } from '../src/chat'

// XHR stub: the real uploadAttachment in chat.ts runs against this class
// (jsdom's XHR never fires upload.onprogress for send(FormData)).
class XHRStub {
  static instances: XHRStub[] = []
  method = ''
  url = ''
  body: unknown = null
  sent = false
  aborted = false
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  onabort: (() => void) | null = null
  status = 0
  responseText = ''

  constructor() { XHRStub.instances.push(this) }
  open(method: string, url: string) { this.method = method; this.url = url }
  setRequestHeader(_k: string, _v: string) {}
  send(body: unknown) {
    this.sent = true
    this.body = body
  }
  abort() { this.aborted = true }
}

// respondUpload completes one in-flight upload with a 200 id.
function respondUpload(x: XHRStub, id: string) {
  x.status = 200
  x.responseText = JSON.stringify({ id, mime: 'image/png', size: 6, kind: 'image' })
  x.onload!()
}

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

function clickRetry() {
  const retryBtn = document.querySelector<HTMLButtonElement>(
    'button[aria-label="Retry upload"]',
  )!
  retryBtn.click()
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

// dispatchFilePaste: jsdom 29 has no ClipboardEvent/DataTransfer (issue
// #1568), so build a synthetic paste Event with a stubbed clipboardData
// carrying kind=file items backed by real File objects. Returns the
// preventDefault spy so call sites can assert the text-suppression decision.
function dispatchFilePaste(ta: HTMLTextAreaElement, files: File[]): { spy: Mock } {
  const evt = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(evt, 'clipboardData', {
    value: {
      items: files.map((f) => ({ kind: 'file', type: f.type, getAsFile: () => f })),
      files,
      getData: (t: string) => (t === 'text/plain' ? '' : ''),
    },
    writable: false, configurable: true,
  })
  const spy = vi.spyOn(evt as unknown as { preventDefault: () => void }, 'preventDefault')
  ta.dispatchEvent(evt)
  return { spy }
}

beforeEach(() => {
  listeners.clear()
  stateListeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
  installConn()
  XHRStub.instances = []
  vi.stubGlobal('XMLHttpRequest', XHRStub)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function pngBlob(): Blob {
  return new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
}

describe('chat attachment rendering', () => {
  it('UserMessageWithImage_PostsOnceThenCommitsReturnedID', async () => {
    mount()
    setTextarea('look at this')
    const file = new File([pngBlob()], 'photo.png', { type: 'image/png' })
    attachFile(file)
    clickSend()

    // Exactly one POST to /api/upload, carrying photo.png in the file field.
    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    const x = XHRStub.instances[0]
    expect(x.method).toBe('POST')
    expect(x.url).toBe('/api/upload')
    const form = x.body as FormData
    expect(form.get('file')).toBe(file)

    respondUpload(x, 'i:0123456789abcdef.png')

    // The ONLY WS frame is the commit; the chunked protocol must not creep
    // back.
    await vi.waitFor(() => {
      expect(sent.length).toBe(1)
    })
    const msg = sent[0] as {
      type: string
      text: string
      attachments: Array<{ id: string; name: string; mime: string; size: number }>
    }
    expect(msg.type).toBe('message')
    expect(msg.text).toBe('look at this')
    expect(msg.attachments.length).toBe(1)
    expect(msg.attachments[0].id).toBe('i:0123456789abcdef.png')
    expect(msg.attachments[0].name).toBe('photo.png')

    // Rendered <img> uses the blob previewURL.
    await vi.waitFor(() => {
      expect(messagesContainer().querySelectorAll('img').length).toBe(1)
    })
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
    // One outbound frame: just the user_message (no upload).
    await vi.waitFor(() => {
      expect(sent.length).toBe(1)
    })
    const m = sent[0] as { type: string; text: string; attachments?: unknown }
    expect(m.type).toBe('message')
    expect(m.text).toBe('hello there')
    expect(m.attachments).toBeUndefined()
    expect(XHRStub.instances.length).toBe(0)
  })

  it('SendDuringStreaming_QueuesAfterUpload', async () => {
    mount()
    // Drive chat into streaming state — query_start triggers initStreaming.
    dispatch({
      type: 'event',
      event: { type: 'query_start' },
    })
    setTextarea('mid-stream message')
    const file = new File([pngBlob()], 'photo.png', { type: 'image/png' })
    attachFile(file)
    clickSend()

    // One POST first; the message frame follows only after it resolves.
    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    expect(sent.length).toBe(0)
    respondUpload(XHRStub.instances[0], 'i:queued')
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })

    // No new user-message node should appear in the transcript —
    // query_start for the queued turn will render it when the engine picks
    // the message up.
    const userImgs = messagesContainer().querySelectorAll('img')
    expect(userImgs.length).toBe(0)
  })

  it('UploadProgressPaintsChipBar', async () => {
    mount()
    setTextarea('watch the bar')
    const file = new File([pngBlob()], 'photo.png', { type: 'image/png' })
    attachFile(file)
    clickSend()

    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    const x = XHRStub.instances[0]
    x.upload.onprogress!({ lengthComputable: true, loaded: 512, total: 1024 })
    const fill = document.querySelector('.bg-blue') as HTMLElement
    if (!fill) throw new Error('progress fill not rendered')
    expect(fill.style.width).toBe('50%')

    respondUpload(x, 'i:progress')
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })
    // setAttachmentProgress(ref, 1) hides the bar (chip strip is cleared by
    // the successful send, and no fill element remains).
    expect(document.querySelector('.bg-blue')).toBeNull()
  })

  it('UploadFailure_MarksChipRed_NoCommit', async () => {
    mount()
    setTextarea('two files')
    const f1 = new File([pngBlob()], 'a.png', { type: 'image/png' })
    const f2 = new File([pngBlob()], 'b.png', { type: 'image/png' })
    attachFile(f1)
    attachFile(f2)
    clickSend()

    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    // First upload dies on the network: chip turns red, loop breaks before
    // the second file, no commit frame.
    XHRStub.instances[0].onerror!()

    await vi.waitFor(() => {
      const failed = document.querySelectorAll('.border-red-500')
      expect(failed.length).toBe(1)
    })
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)

    // Draft restored so the user can retry without retyping.
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('two files')
  })

  it('StripCleared_AfterSuccessfulSend_NewFileReuploads', async () => {
    mount()
    setTextarea('first send')
    const f1 = new File([pngBlob()], 'a.png', { type: 'image/png' })
    attachFile(f1)
    clickSend()

    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    respondUpload(XHRStub.instances[0], 'i:first')
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })
    expect(XHRStub.instances.length).toBe(1)
    expect(document.querySelectorAll('.border-red-500').length).toBe(0)

    // Second send: new file (new ref with uploadedID=undefined) must
    // re-upload — strip was cleared by removeAttachments after the first
    // successful commit.
    sent.length = 0
    XHRStub.instances = []
    setTextarea('second send')
    const f2 = new File([pngBlob()], 'b.png', { type: 'image/png' })
    attachFile(f2)
    clickSend()
    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    respondUpload(XHRStub.instances[0], 'i:second')
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })
  })

  it('RetryThenSend_PostsOnlyTheFailedFile', async () => {
    mount()
    setTextarea('two files')
    const f1 = new File([pngBlob()], 'a.png', { type: 'image/png' })
    const f2 = new File([pngBlob()], 'b.png', { type: 'image/png' })
    attachFile(f1)
    attachFile(f2)
    clickSend()

    // First send: f1 uploads fine, f2's POST dies → f2 marked failed, no
    // commit frame.
    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    respondUpload(XHRStub.instances[0], 'i:a1')
    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(2)
    })
    XHRStub.instances[1].onerror!()
    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(1)
    })
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)

    // Retry the failed chip: exactly one more POST, which succeeds.
    clickRetry()
    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(3)
    })
    respondUpload(XHRStub.instances[2], 'i:b2')
    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(0)
    })

    // Resend: both refs already carry ids — no further POSTs, and the commit
    // carries exactly the two ids the server minted.
    sent.length = 0
    clickSend()
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })
    expect(XHRStub.instances.length).toBe(3) // no 4th POST
    const msg = sent.find((m) => (m as { type?: string }).type === 'message') as {
      attachments: Array<{ id: string; name: string }>
    }
    expect(msg.attachments.length).toBe(2)
    expect(msg.attachments.find((a) => a.name === 'a.png')!.id).toBe('i:a1')
    expect(msg.attachments.find((a) => a.name === 'b.png')!.id).toBe('i:b2')
  })

  it('RetryRestoredChip_WithUploadedID_NoPost_ClearsFailure', async () => {
    mount()
    // A restore-shaped chip: zero-byte stub File + uploadedID set; the true
    // length rides restoredSize.
    dispatch({
      type: 'cancel_result',
      removed: [],
      restored: [{
        uuid: 'u1',
        text: 'restored prompt',
        attachments: [{ id: 'i:restored0000000f', name: 'photo.png', mime: 'image/png', size: 4321 }],
      }],
    })
    expect(document.querySelectorAll('.border-red-500').length).toBe(0)

    // The connection drops before the re-send: the pre-flight marks every
    // chip failed without any POST (the zero-byte stub must not be uploaded).
    connState.connected = false
    setTextarea('restored prompt')
    clickSend()
    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(1)
    })
    expect(XHRStub.instances.length).toBe(0)

    // Retry on a chip whose bytes already live server-side: success WITHOUT
    // a request — re-POSTing the stub would overwrite a good id with the id
    // of an empty document.
    clickRetry()
    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(0)
    })
    expect(XHRStub.instances.length).toBe(0)

    // The later Send commits the original id with restoredSize, not 0.
    connState.connected = true
    setTextarea('restored prompt')
    clickSend()
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })
    expect(XHRStub.instances.length).toBe(0)
    const msg = sent.find((m) => (m as { type?: string }).type === 'message') as {
      attachments: Array<{ id: string; size: number }>
    }
    expect(msg.attachments.length).toBe(1)
    expect(msg.attachments[0].id).toBe('i:restored0000000f')
    expect(msg.attachments[0].size).toBe(4321)
  })

  it('AbortOnDisconnect_CancelsInFlightUpload', async () => {
    mount()
    setTextarea('watch it die')
    const file = new File([pngBlob()], 'photo.png', { type: 'image/png' })
    attachFile(file)
    clickSend()

    // Upload hangs (no response fired) until the reconnect aborts it.
    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    pushState('reconnecting')
    expect(XHRStub.instances[0].aborted).toBe(true)
    XHRStub.instances[0].onabort!()

    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(1)
    })
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)
  })

  it('NoConnection_PreFlightMarksFailure_NoPost', async () => {
    installConn({ connected: false })
    mount()
    setTextarea('offline')
    const file = new File([pngBlob()], 'photo.png', { type: 'image/png' })
    attachFile(file)
    clickSend()

    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(1)
    })
    expect(XHRStub.instances.length).toBe(0)
    expect(sent.length).toBe(0)
    // Draft restored for the retry.
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('offline')
  })
})

describe('chat paste integration', () => {
  it('PasteOnly_SendsAsMessageText_NoUpload', async () => {
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
    expect(XHRStub.instances.length).toBe(0)

    const text = messagesContainer().textContent ?? ''
    expect(text.includes('explain')).toBe(true)
    expect(text.includes('code')).toBe(true)
  })

  it('PastePlusImage_UploadsImageOnly_InlinesPaste', async () => {
    mount()
    setTextarea('see this')
    const paste = 'first line\nsecond line\nthird line\nfourth line'
    const file = new File([pngBlob()], 'photo.png', { type: 'image/png' })
    attachFile(file)
    const ta = document.querySelector<HTMLTextAreaElement>('textarea')!
    dispatchPaste(ta, paste)
    clickSend()

    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    respondUpload(XHRStub.instances[0], 'i:paste1')
    await vi.waitFor(() => {
      expect(sent.filter((m) => (m as { type?: string }).type === 'message').length).toBe(1)
    })

    expect(sent.length).toBe(1)
    const msg = sent[0] as {
      type: string
      text: string
      attachments: Array<{ id: string; name: string }>
    }
    expect(msg.type).toBe('message')
    expect(msg.text).toBe('see this\n\n' + paste)
    expect(msg.attachments.length).toBe(1)
    expect(msg.attachments[0].name).toBe('photo.png')
    expect(msg.attachments[0].id).toBe('i:paste1')
  })

  it('FailedImageUpload_KeepsPasteChip_RestoresTypedText', async () => {
    mount()
    setTextarea('hi')
    const file = new File([pngBlob()], 'photo.png', { type: 'image/png' })
    attachFile(file)
    const ta = document.querySelector<HTMLTextAreaElement>('textarea')!
    dispatchPaste(ta, 'pasted\nline2\nline3\nline4')
    clickSend()

    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    XHRStub.instances[0].onerror!()

    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(1)
    })

    // Typed text restored — NOT the paste-inlined fullText.
    const taAfter = document.querySelector('textarea') as HTMLTextAreaElement
    expect(taAfter.value).toBe('hi')
    // Paste chip stays in the strip for retry.
    expect(document.querySelector('[data-paste-chip]')).not.toBeNull()
    // No commit frame sent.
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)
  })
})

describe('chat clipboard file paste', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('ClipboardPaste_UploadsStampedName', async () => {
    mount()
    const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: 'image/png' })
    const file = new File([blob], '', { type: 'image/png' })
    const ta = document.querySelector<HTMLTextAreaElement>('textarea')!
    // Freeze the clock around the paste so the stamp is deterministic;
    // real timers are restored before the async upload phase so vi.waitFor
    // polls with real intervals.
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 10, 14, 3, 5))
    const { spy } = dispatchFilePaste(ta, [file])
    vi.useRealTimers()
    expect(spy).toHaveBeenCalledTimes(1)
    setTextarea('look')
    clickSend()

    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    // The FormData entry carries the stamped filename.
    const form = XHRStub.instances[0].body as FormData
    const entry = form.get('file') as File
    expect(entry.name).toBe('pasted-140305.png')
    respondUpload(XHRStub.instances[0], 'i:stamped')

    await vi.waitFor(() => {
      expect(sent.length).toBe(1)
    })
    const msg = sent[0] as {
      type: string
      text: string
      attachments: Array<{ id: string; name: string; mime: string; size: number }>
    }
    expect(msg.type).toBe('message')
    expect(msg.text).toBe('look')
    expect(msg.attachments.length).toBe(1)
    expect(msg.attachments[0].name).toBe('pasted-140305.png')
    expect(msg.attachments[0].id).toBe('i:stamped')
    expect(msg.attachments[0].mime).toBe('image/png')
    expect(msg.attachments[0].size).toBe(blob.size)

    // Rendered user message shows the stamped name with a blob preview.
    const img = messagesContainer().querySelector('img') as HTMLImageElement
    if (!img) throw new Error('rendered user message img not found')
    expect(img.alt).toBe('pasted-140305.png')
    expect(img.src.startsWith('blob:')).toBe(true)
    // Textarea cleared after send.
    const taAfter = document.querySelector('textarea') as HTMLTextAreaElement
    expect(taAfter.value).toBe('')
  })

  it('PastedFile_UploadFailure_MarksChipRed_KeepsName', async () => {
    mount()
    const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: 'image/png' })
    const file = new File([blob], 'image.png', { type: 'image/png' })
    const ta = document.querySelector<HTMLTextAreaElement>('textarea')!
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 10, 14, 3, 5))
    dispatchFilePaste(ta, [file])
    vi.useRealTimers()
    clickSend()

    await vi.waitFor(() => {
      expect(XHRStub.instances.length).toBe(1)
    })
    XHRStub.instances[0].onerror!()

    await vi.waitFor(() => {
      expect(document.querySelectorAll('.border-red-500').length).toBe(1)
    })
    // No commit frame — the failure path stops before conn.send.
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)
    // The failed chip keeps the stamped name through the failure path.
    const failedChip = document.querySelector('.border-red-500') as HTMLImageElement
    if (!failedChip) throw new Error('failed chip not found')
    expect(failedChip.alt).toBe('pasted-140305.png')
  })
})
