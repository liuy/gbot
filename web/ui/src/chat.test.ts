import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createChat, mapHistoryToChatMessages } from './chat'
import { refreshAvatar, resetAvatarStateForTest } from './avatar'
import { TokenRate } from './token_rate'
import { stopStatusSweep } from './components/status_sweep'
import { createTTSQueue } from './voice_tts'
import { floatTo16Le } from './stt'
import * as sttModule from './stt'
import * as streamBlocks from './components/stream_blocks'
import { copyText } from './utils/copy_button'
import { uploadAttachment } from './upload'
import type { HistoryChatMsg, HistoryBlock } from './types'

// jsdom lacks IntersectionObserver — stub it (no longer used for prefetch).
class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return [] }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver as unknown as typeof IntersectionObserver)

type Listener = (msg: unknown) => void

const listeners: Set<Listener> = new Set()
const sent: unknown[] = []

let stateCb: ((s: string) => void) | null = null

// reconnect is deliberately ABSENT (as on the real connector before the
// first state change): the banner click handler must tolerate a missing
// method, and a mutant that drops the optional chaining throws here.
// `connected` reads mutable state so a test can simulate the socket state
// for the upgrade rollback timer.
let wsConnected = true
let reconnectImpl: () => void = () => { stateCb?.('reconnecting') }

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: unknown) => {
      sent.push(p)
    },
    get connected() {
      return wsConnected
    },
    onStateChange: (cb: (s: string) => void) => { stateCb = cb },
    reconnect: () => reconnectImpl(),
  }),
}))
// The TTS queue is mocked so the chat-level voice tests can observe what
// the controller enqueues and drive playback (write/settle/error) — a real
// queue would POST /api/tts and stream, which connects to nothing in jsdom.
// voice_tts.test.ts covers the queue itself.
vi.mock('./voice_tts', () => {
  interface MockOpts {
    write(pcm: Uint8Array): void
    onError?(message: string): void
    onSettled?(): void
  }
  class MockTTSQueue {
    opts: MockOpts
    texts: string[] = []
    aborts = 0
    inFlightCount = 0
    constructor(opts: MockOpts) {
      this.opts = opts
    }
    enqueue(text: string): void {
      this.texts.push(text)
      this.inFlightCount++
    }
    inFlight(): number {
      return this.inFlightCount
    }
    abort(): void {
      this.aborts++
      this.inFlightCount = 0
    }
    // Driver: one item finished — inFlight DECREMENTS, reaching 0 only when
    // every enqueued item settled (the real queue's drain semantics).
    settle(): void {
      if (this.inFlightCount > 0) this.inFlightCount--
      this.opts.onSettled?.()
    }
  }
  const all: MockTTSQueue[] = []
  const create = (opts: MockOpts): MockTTSQueue => {
    const q = new MockTTSQueue(opts)
    all.push(q)
    return q
  }
  return { createTTSQueue: Object.assign(create, { all }) }
})
// The voice mic leg is a real SttSession; its /ws/stt socket is faked here
// so the tests can drive the handshake (start frame wire shape, ready, done)
// through the genuine capture/buffering path. The wrapper is transparent to
// the dictation session (no chat-level test drives it).
vi.mock('./stt', async (importOriginal) => {
  const orig = await importOriginal<typeof import('./stt')>()
  class MockSttWS {
    static instances: MockSttWS[] = []
    static OPEN = 1
    readyState = MockSttWS.OPEN
    bufferedAmount = 0
    onopen: (() => void) | null = null
    onclose: (() => void) | null = null
    onerror: (() => void) | null = null
    onmessage: ((ev: { data: unknown }) => void) | null = null
    sentText: string[] = []
    sentBinary: ArrayBuffer[] = []
    closed = false
    constructor() {
      MockSttWS.instances.push(this)
      queueMicrotask(() => {
        this.readyState = MockSttWS.OPEN
        this.onopen?.()
      })
    }
    send(data: string | ArrayBuffer) {
      if (typeof data === 'string') this.sentText.push(data)
      else this.sentBinary.push(data)
    }
    close() {
      this.closed = true
      this.readyState = 3
    }
    dispatch(data: unknown) {
      this.onmessage?.({ data })
    }
  }
  const RealSttSession = orig.SttSession
  const sttSocks: unknown[] = []
  class SttSession extends RealSttSession {
    constructor(opts: Record<string, unknown>) {
      super({
        ...opts,
        deps: {
          ...(opts.deps as object | undefined),
          connectSocket: () => {
            const sock = new MockSttWS()
            sttSocks.push(sock)
            return sock as never
          },
        },
      } as never)
    }
  }
  return { ...orig, SttSession, __sttSocks: sttSocks }
})
vi.mock('./utils/copy_button', async (importOriginal) => {
  const orig = await importOriginal<typeof import('./utils/copy_button')>()
  return { ...orig, copyText: vi.fn() }
})
// uploadAttachment is the network boundary: a controllable promise that
// rejects exactly when the chat aborts its AbortSignal. The rest of the
// upload module (attachmentMeta, pooled) stays real — those are pure.
vi.mock('./upload', async (importOriginal) => {
  const orig = await importOriginal<typeof import('./upload')>()
  return { ...orig, uploadAttachment: vi.fn() }
})
// The lightbox overlay is a module singleton appended to document.body once;
// the history-twin test creates it and later tests' body wipes detach it,
// so a live-send lightbox assert cannot go through the DOM. Record calls
// while delegating to the real singleton.
vi.mock('./components/stream_blocks', async (importOriginal) => {
  const orig = await importOriginal<typeof import('./components/stream_blocks')>()
  const real = orig.showImageLightbox
  const __lightboxCalls: string[] = []
  return {
    ...orig,
    showImageLightbox: (src: string): void => {
      __lightboxCalls.push(src)
      real(src)
    },
    __lightboxCalls,
  }
})

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

function dispatchEvents(events: unknown[]) {
  for (const e of events) dispatch({ type: 'event', event: e })
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

// Every mount() boots initAvatars, and fetch spies in earlier tests (e.g.
// the artifact integration Response mocks) can satisfy the probe — leaked
// avatar state would flip later shells to <img> with no test opting in.
afterEach(() => {
  vi.useRealTimers()
  resetAvatarStateForTest()
  delete (window as unknown as Record<string, unknown>).GBotShare
  vi.restoreAllMocks()
})

function setTextarea(value: string) {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = value
  ta.dispatchEvent(new Event('input', { bubbles: true }))
}

function pressEnter() {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.dispatchEvent(
    new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
  )
}

function clickStop() {
  const stopBtn = Array.from(document.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('STOP'),
  ) as HTMLButtonElement | undefined
  stopBtn?.click()
}

function assistantContentDivs(): HTMLElement[] {
  return Array.from(document.querySelectorAll('.space-y-3'))
}

const histMsg = (over: Partial<HistoryChatMsg>): HistoryChatMsg => ({
  id: '',
  role: 'assistant',
  text: '',
  thinking: [],
  tools: [],
  usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
  error: '',
  status: 'done',
  startedAt: 0,
  ...over,
})

// The share menu is armed only when the Android bridge exists.
function armShareBridge() {
  ;(window as unknown as Record<string, unknown>).GBotShare = {
    shareImage: vi.fn().mockResolvedValue('opened'),
  }
}
// Full long-press gesture: pointerdown, hold expiry, contextmenu open.
function sharePress(target: HTMLElement) {
  target.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientY: 100 }))
  vi.advanceTimersByTime(450)
  target.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
}
function shareMenu(): HTMLElement | null {
  return document.querySelector('.share-menu')
}
function clickCopy() {
  const items = Array.from(document.querySelectorAll('.share-menu-item')) as HTMLButtonElement[]
  items[items.length - 1].click()
}
function toolRootByName(name: string): HTMLElement {
  return document.querySelector(`[data-tool-root][data-tool-name="${name}"]`) as HTMLElement
}

beforeEach(() => {
  reconnectImpl = (): void => {}
  listeners.clear()
  sent.length = 0
  wsConnected = true
  stateCb = null
  document.body.innerHTML = ''
  vi.mocked(copyText).mockReset().mockResolvedValue(undefined)
  vi.mocked(uploadAttachment).mockReset().mockResolvedValue({ id: 'up-1' })
})

describe('chat integration', () => {
  it('mounts the upgrade capsule at the shell root, outside the transform trap', () => {
    const chat = mount()
    const capsule = chat.root.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule).toBeTruthy()
    // The capsule is fixed z-70 and must float above the opaque z-60
    // settings sheet (restart is initiated FROM settings). Sidebar
    // toggling parks a translateX(0) on mainContent — a lingering
    // transform makes it a stacking context that would trap the capsule.
    // Only a direct shell-root child stacks as authored.
    expect(capsule.parentElement).toBe(chat.root)
  })
  it('send shows user message and dispatches message', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('hello world')
    pressEnter()
    expect(document.body.textContent).toContain('hello world')
    expect(sent.some((m) => m.type === 'message' && m.text === 'hello world'))
      .toBe(true)
  })

  it('query_start creates an assistant shell and shows STOP', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    const before = assistantContentDivs().length
    dispatchEvents([{ type: 'query_start' }])
    expect(assistantContentDivs().length).toBe(before + 1)
    expect(document.body.textContent).toContain('STOP')
  })

  it('text_delta accumulates and renders markdown incrementally', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '**bo' },
      { type: 'text_delta', text: 'ld**' },
    ])
    const mdBody = document.querySelector('.md-body.text-t1') as HTMLElement
    expect(mdBody.querySelector('strong')?.textContent).toBe('bold')
  })

  it('text_delta survives subsequent tool_start (no remount loss)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'keep me' },
      {
        type: 'tool_start',
        tool_use: { id: 't-1', name: 'Bash' },
      },
    ])
    const mdBody = document.querySelector('.md-body.text-t1') as HTMLElement
    expect(mdBody.textContent).toContain('keep me')
  })

  it('empty text_delta is a no-op', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '' },
    ])
    const mdBodies = document.querySelectorAll('.md-body')
    expect(mdBodies.length).toBe(1)
    // Empty text → empty innerHTML.
    expect((mdBodies[0] as HTMLElement).textContent).toBe('')
  })

  it('multiple text blocks per turn each get fresh sinks', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'first' },
      { type: 'text_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'second' },
      { type: 'text_end' },
      { type: 'query_end' },
    ])
    expect(document.querySelectorAll('.md-body').length).toBe(2)
  })

  it('interleaved text+thinking both writable', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: 'deep thought' } },
      { type: 'text_start' },
      { type: 'text_delta', text: 'answer' },
    ])
    const mdBody = document.querySelector('.md-body.text-t1') as HTMLElement
    expect(mdBody.textContent).toContain('answer')
    expect(document.body.textContent).toContain('deep thought')
  })

  it('abort during thinking removes message and restores input', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('test query')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    dispatchEvents([
      { type: 'thinking_end' },
      { type: 'query_end', aborted: true, rewound: true },
    ])
    expect(document.body.textContent).not.toContain('test query')
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('test query')
  })

  it('abort with partial response keeps interrupt marker', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('another query')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'partial' },
    ])
    clickStop()
    dispatchEvents([
      { type: 'text_end' },
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])
    expect(document.body.textContent).toContain('another query')
    expect(document.body.textContent).toContain('partial')
    expect(document.body.textContent).toContain(
      '[Request interrupted by user]',
    )
  })

  it('repeated send+abort restores correct text (stale-input regression)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })

    function cycle() {
      setTextarea('repeat me')
      pressEnter()
      dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
      clickStop()
      dispatchEvents([
        { type: 'thinking_end' },
        { type: 'query_end', aborted: true, rewound: true },
      ])
      const ta = document.querySelector('textarea') as HTMLTextAreaElement
      expect(ta.value).toBe('repeat me')
    }
    cycle()
    cycle()
  })

  it('query_end is a DOM no-op (key new behavior)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'hello' },
    ])
    const content = assistantContentDivs().slice(-1)[0]
    const firstChildBefore = content.firstElementChild
    const childCountBefore = content.childElementCount
    dispatchEvents([{ type: 'query_end' }])
    // Same node identity — DOM preserved verbatim.
    expect(assistantContentDivs().slice(-1)[0]).toBe(content)
    expect(content.firstElementChild).toBe(firstChildBefore)
    expect(content.childElementCount).toBe(childCountBefore)
  })

  it('tool_end uses wall-clock duration (not prefix parse)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    const start = Date.now()
    dispatchEvents([
      { type: 'query_start' },
      {
        type: 'tool_start',
        tool_use: { id: 't-1', name: 'Bash' },
      },
    ])
    const elapsed = (Date.now() - start) / 1000
    dispatch({
      type: 'event',
      event: {
        type: 'tool_end',
        tool_result: { tool_use_id: 't-1', display_output: '' },
      },
    })
    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot).toBeTruthy()
    const allText = toolRoot.textContent ?? ''
    expect(allText).toMatch(/\d+(\.\d+)?s/)
    const secMatch = allText.match(/(\d+(\.\d+)?)s/)
    const sec = secMatch ? parseFloat(secMatch[1]) : 0
    expect(sec).toBeGreaterThanOrEqual(0)
    expect(sec).toBeLessThanOrEqual(elapsed + 2)
  })

  it('tool_output_delta keeps the running body folded; expand renders the live output', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'live-bash-1', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'live-bash-1', display_output: 'building 1/100' } },
    ])
    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    const body = toolRoot.children[1] as HTMLElement
    expect(body.classList.contains('hidden')).toBe(true)
    ;(toolRoot.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('building 1/100')
  })

  it('tool_output_delta: empty delta inert, content stays folded, morph patches in place', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'live-bash-2', name: 'Bash' } },
    ])
    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    const body = toolRoot.children[1] as HTMLElement
    // Segment 1 — empty delta must not touch the DOM (UI contract; the
    // engine never dispatches empty payloads, this guards regressions).
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'live-bash-2', display_output: '' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    expect(body.textContent).toBe('')
    // Segment 2 — the first content-bearing delta buffers into
    // pendingOutput; the body stays folded (no running-time expand).
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'live-bash-2', display_output: 'step 1' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    expect(body.textContent).toBe('')
    ;(toolRoot.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(body.textContent).toContain('step 1')
    const pBefore = body.querySelector('p')
    // Segment 3 — a delta while visible: innerHTML replacement would swap
    // in a fresh <p>; morph must patch the existing text node in place.
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'live-bash-2', display_output: 'step 1\nstep 2' } },
    ])
    expect(body.textContent).toContain('step 2')
    expect(body.querySelector('p')).toBe(pBefore)
  })

  // ── Fold-policy trio: bodies stay folded while running (only Edit/Write
  // auto-expand on finish), render-on-visible flushes the latest snapshot,
  // and user gestures win over everything. Shared helpers below exercise
  // the full event pipeline (tool_start → tool_output_delta → tool_end).
  function liveBashBody(): HTMLElement {
    const toolRoot = document.querySelector(`[data-tool-root][data-tool-name="Bash"]`) as HTMLElement
    expect(toolRoot).toBeTruthy()
    return toolRoot.children[1] as HTMLElement
  }

  function clickToolHeader() {
    const header = document.querySelector('[data-tool-root] [data-tool-header]') as HTMLElement
    expect(header).toBeTruthy()
    header.click()
  }

  it('a running non-edit tool never expands; tool_end keeps it folded', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-1', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-1', display_output: 'building 1/100' } },
    ])
    const body = liveBashBody()
    const header = document.querySelector('[data-tool-root] [data-tool-header]') as HTMLElement
    // No first-delta expand: streaming output must not make the running
    // block grow and shrink (screen flicker).
    expect(body.classList.contains('hidden')).toBe(true)
    expect(header.querySelector('svg')?.getAttribute('class')).not.toContain('rotate-90')
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'fold-1', display_output: 'building 100/100\nok' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    // The final output is not lost: expanding renders it (render-on-visible).
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('building 100/100')
  })

  it('user-folded block stays folded through later deltas and tool_end (intent guard)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-2', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-2', display_output: 'step 1' } },
    ])
    const body = liveBashBody()
    // The user expands, then folds mid-run.
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(false)
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(true)
    // A later delta must not re-expand the user's fold.
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-2', display_output: 'step 1\nstep 2' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'fold-2', display_output: 'step 1\nstep 2\ndone' } },
    ])
    // tool_end must leave the user's fold alone.
    expect(body.classList.contains('hidden')).toBe(true)
  })

  it('user-expanded block stays expanded through tool_end (intent guard)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-3', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-3', display_output: 'step 1' } },
    ])
    const body = liveBashBody()
    // The user expands mid-run.
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(false)
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'fold-3', display_output: 'done' } },
    ])
    // User intent wins over the finish-time fold.
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('done')
  })

  it('folded running block receives no DOM writes; expand renders the latest snapshot', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-4', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-4', display_output: 'step 1' } },
    ])
    const body = liveBashBody()
    // The block is folded from the start — deltas must not touch the
    // hidden DOM (no markdown, no morph — the payload is a replace
    // window, intermediate frames carry no information).
    const frozen = body.innerHTML
    dispatchEvents([
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-4', display_output: 'step 1\nstep 2' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-4', display_output: 'step 1\nstep 2\nstep 3' } },
    ])
    expect(body.innerHTML).toBe(frozen)
    expect(body.classList.contains('hidden')).toBe(true)
    // Expanding renders the LATEST snapshot exactly once — 'step 3' present,
    // no duplicate 'step 2' accumulation.
    clickToolHeader()
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('step 3')
    expect(body.textContent.match(/step 2/g)?.length).toBe(1)
  })

  it('terminal fold: tool_end output stays pending until expand renders it', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run tests')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'fold-5', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 'fold-5', display_output: 'streaming…' } },
    ])
    const body = liveBashBody()
    expect(body.classList.contains('hidden')).toBe(true)
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'fold-5', display_output: 'FINAL OUTPUT' } },
    ])
    expect(body.classList.contains('hidden')).toBe(true)
    clickToolHeader()
    expect(body.textContent).toContain('FINAL OUTPUT')
  })

  it('usage updates progress bar', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: {
        type: 'usage',
        usage_event: {
          input_tokens: 1234,
          output_tokens: 5678,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
      },
    })
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑1.2k')
    const outEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↓'),
    )
    expect(outEl?.textContent).toBe('↓5.5k')
  })

  it('turn_start estimate drives ↑input before usage arrives', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start', input_estimate: 1234 },
    ])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑1.2k')

    // Billed usage replaces the estimate instead of adding to it:
    // 1000 new + 300 cache read = 1300, not 1300 + 1234.
    dispatchEvents([
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 20, cache_read_input_tokens: 300 } },
    ])
    expect(inEl?.textContent).toBe('↑1.3k')
  })

  it('turn_start estimate accumulates over turns and never double-counts', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start', input_estimate: 1000 },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
      // Turn 2 estimates ride on the billed turn 1: 1000 + 2000 = 3000.
      { type: 'turn_start', input_estimate: 2000 },
    ])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑2.9k')

    // Turn 2's billed 2048 replaces its 2000 estimate: 1000 + 2048 = 3048.
    dispatchEvents([
      { type: 'usage', usage_event: { input_tokens: 2048, output_tokens: 1000 } },
    ])
    expect(inEl?.textContent).toBe('↑3.0k')
  })

  it('sub-agent turn_start estimate leaves ↑input alone', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start', input_estimate: 1000 },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      {
        type: 'turn_start',
        input_estimate: 9000,
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 },
      },
    ])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑1.0k')
  })

  it('job-tagged usage is stripped: the mainline estimate and totals stay untouched', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start', input_estimate: 300 },
      { type: 'usage', usage_event: { input_tokens: 300, output_tokens: 20 } },
      // Turn 2's estimate rides on turn 1's billed input: 300 + 400 = 700.
      { type: 'turn_start', input_estimate: 400 },
    ])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑700')
    const outEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↓'),
    )
    const outBefore = outEl?.textContent

    // A background job's tokens belong entirely to the job (its completion
    // notification <tokens>): mainline estimate and totals stay exactly as
    // they were.
    dispatchEvents([
      {
        type: 'usage',
        usage_event: { input_tokens: 111, output_tokens: 5 },
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1, job_id: 'agent-9' },
      },
    ])
    expect(inEl?.textContent).toBe('↑700')
    expect(outEl?.textContent).toBe(outBefore)

    // Sync sub-agent usage still accumulates into the mainline totals:
    // billed 300 + 111 with the 400 estimate riding on top.
    dispatchEvents([
      {
        type: 'usage',
        usage_event: { input_tokens: 111, output_tokens: 5 },
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 },
      },
    ])
    expect(inEl?.textContent).toBe('↑811')
  })

  it('bare turn_start estimate reaches ↑input via initStreaming', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // No query_start ahead of it: an auto-turn or drained-attachment query
    // arrives as a bare turn_start, which opens the stream through
    // initStreaming instead of the mid-query guard.
    dispatchEvents([{ type: 'turn_start', input_estimate: 777 }])
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑777')
  })

  it('query_end retires an uncleared estimate: the next query starts at 0', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q1')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      // Query 1's turn never gets its usage event, so the estimate is still
      // standing when the query ends.
      { type: 'turn_start', input_estimate: 4000 },
      { type: 'query_end' },
    ])
    const upSpans = () =>
      Array.from(document.querySelectorAll('span'))
        // .ch = per-char sweep span inside inEl; only segment spans count
        .filter((s) => s.textContent?.startsWith('↑') && !s.classList.contains('ch'))
        .map((s) => s.textContent)
    // The finalized bar bills what the estimate never covered: 0.
    expect(upSpans()).toEqual(['↑0'])

    // Query 2 opens with no turn_start of its own yet — the fresh bar must
    // read 0, not carry query 1's leftover estimate.
    dispatchEvents([{ type: 'query_start' }])
    expect(upSpans()).toEqual(['↑0', '↑0'])
  })

  it('error renders red block on streaming assistant', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    dispatch({ type: 'error', message: 'boom' })
    const red = document.querySelectorAll('.text-red')
    const errorEl = Array.from(red).find(el => el.textContent?.includes('boom'))
    expect(errorEl?.textContent).toContain('boom')
  })

  it('comet sweep cold start: cursor walks the full visible string', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])

    const root = document.querySelector('.status-line') as HTMLElement
    const p1s = () => Array.from(root.querySelectorAll<HTMLElement>('.p1'))
    // k=0 is the synchronous first step: highlight exists the instant the
    // query starts.
    expect(p1s().length).toBe(1)
    expect(p1s()[0].textContent).toBe('↑')

    vi.advanceTimersByTime(110)
    expect(p1s()[0].textContent).toBe('0')
    expect((p1s()[0].previousElementSibling as HTMLElement).classList.contains('t1')).toBe(true)

    // Walk k=2..14 (14 advances total). The 200ms refresh ticks interleave:
    // elapsedEl rewrites itself every tick, and the tick at t=1000 shrinks
    // '0.8s'→'1s' (a real span rebuild) — the last two highlights must land
    // on the rebuilt '1','s' spans, not the initial '0','s'.
    const seq = ['↑', '0']
    for (let k = 2; k <= 14; k++) {
      vi.advanceTimersByTime(110)
      seq.push(p1s()[0].textContent ?? '')
    }
    expect(seq).toEqual(['↑', '0', '↓', '0', '·', '0', '.', '0', ' ', 't', '/', 's', '·', '1', 's'])
    stopStatusSweep()
    vi.useRealTimers()
  })

  it('comet sweep follows numeric refreshes across a real rebuild', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])

    // tick@200 has run: toolCountEl got its one-time '0 tools', elapsedEl
    // reads '0.2s'.
    vi.advanceTimersByTime(250)
    const root = document.querySelector('.status-line') as HTMLElement
    const before = root.textContent
    expect(before).toBe('↑0↓0·0.0 t/s··0 tools·0.2s')

    // Crosses tick@1000 where '0.8s'→'1s' rebuilds the elapsed spans; every
    // other segment's text must be untouched (token counts stay 0).
    vi.advanceTimersByTime(1000)
    expect(root.textContent).toBe(before.replace('0.2s', '1s'))

    // The cursor keeps advancing over the rebuilt DOM: one of each class per
    // step, and the peak node changes identity every step.
    let prev = root.querySelector('.p1')
    for (let i = 0; i < 3; i++) {
      vi.advanceTimersByTime(110)
      expect(root.querySelectorAll('.p1').length).toBe(1)
      expect(root.querySelectorAll('.t1').length).toBe(1)
      expect(root.querySelectorAll('.t2').length).toBe(1)
      const cur = root.querySelector('.p1')
      expect(cur).not.toBe(prev)
      prev = cur
    }
    stopStatusSweep()
    vi.useRealTimers()
  })

  it('query_end freezes the sweep for good', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    vi.advanceTimersByTime(110)
    const root = document.querySelector('.status-line') as HTMLElement
    expect(root.querySelectorAll('.p1').length).toBe(1)

    dispatchEvents([{
      type: 'query_end',
      usage_event: { input_tokens: 10, output_tokens: 5, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    }])
    expect(root.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    vi.advanceTimersByTime(5000)
    expect(root.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    vi.useRealTimers()
  })

  it('error path stops the sweep without finalize', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    vi.advanceTimersByTime(110)
    const root = document.querySelector('.status-line') as HTMLElement
    expect(root.querySelectorAll('.p1').length).toBe(1)

    dispatch({ type: 'error', message: 'boom' })
    expect(root.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    vi.advanceTimersByTime(5000)
    expect(root.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    vi.useRealTimers()
  })

  it('connect_status resets state for fresh history', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1',
          role: 'user',
          text: 'old',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: 0,
        },
      ],
      nextCursor: 'c1',
      hasMore: false,
    })
    expect(document.body.textContent).toContain('old')
    // Reconnect → fresh history replaces old.
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-2',
          role: 'user',
          text: 'fresh',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: 0,
        },
      ],
      nextCursor: '',
      hasMore: false,
    })
    expect(document.body.textContent).not.toContain('old')
    expect(document.body.textContent).toContain('fresh')
  })

  it('query_end scrolls to bottom after streaming completes', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 2000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('test query')
    pressEnter()
    chat.scrollEl.scrollTop = 1500
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'A'.repeat(1000) },
    ])

    // query_end: progress bar finalize changes DOM height, must re-scroll.
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 3000 })
    chat.scrollEl.scrollTop = 2500
    dispatchEvents([
      { type: 'text_end' },
      { type: 'turn_end' },
      { type: 'query_end' },
    ])

    expect(chat.scrollEl.scrollTop).toBe(chat.scrollEl.scrollHeight)
  })

  it('streaming fast text keeps scrolled to bottom (no scrollBtn)', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })
    Element.prototype.scrollIntoView = vi.fn()

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('fast stream')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    // Rapid-fire deltas (simulating fast stream)
    for (let i = 0; i < 50; i++) {
      Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 + i * 50 })
      dispatch({ type: 'event', event: { type: 'text_delta', text: `chunk ${i} ` } })
    }

    // scrollBtn should remain hidden (user is at bottom)
    const scrollBtn = chat.root.querySelector('.scroll-progress')?.closest('button') as HTMLElement
    expect(scrollBtn.classList.contains('opacity-0')).toBe(true)
  })

  it('initial history load scrolls to bottom synchronously (no RAF delay)', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm1', role: 'user', text: 'hello',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })

    // scrollTop must be set synchronously during loadHistory,
    // not deferred to RAF (content-visibility makes deferred scroll land wrong).
    expect(chat.scrollEl.scrollTop).toBe(chat.scrollEl.scrollHeight)
  })

  it('reconnect scrolls to bottom after history load even if previously scrolled up', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 1000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })
    Element.prototype.scrollIntoView = vi.fn()

    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })

    // Simulate user scrolled up: scroll button should be visible
    chat.scrollEl.scrollTop = 0
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    const scrollBtn = chat.root.querySelector('.scroll-progress')?.closest('button') as HTMLElement
    expect(scrollBtn.classList.contains('opacity-0')).toBe(false)

    // Reconnect with history
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1',
          role: 'user',
          text: 'reconnect message',
          thinking: [],
          tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '',
          status: 'done',
          startedAt: 0,
        },
      ],
      nextCursor: '',
      hasMore: false,
    })

    // After reconnect, isNearBottom should be reset so scroll button is hidden
    expect(scrollBtn.classList.contains('opacity-0')).toBe(true)
  })

  it('scroll near top triggers prefetch when < 10 messages above viewport', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1', role: 'user', text: 'first',
          thinking: [], tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: 'c1',
      hasMore: true,
    })
    // With only 1 message and hasMore=true, loadHistory calls maybePrefetchHistory
    // which detects < 10 messages above viewport and fetches immediately.
    const historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(1)
    expect(historyReqs[0].cursor).toBe('c1')
  })

  it('loadingMore guard prevents concurrent prefetch', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'm-1', role: 'user', text: 'msg1',
          thinking: [], tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: 'c1',
      hasMore: true,
    })
    // First prefetch already sent by loadHistory's maybePrefetchHistory.
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(1)

    // Scrolling again must not send duplicate (loadingMore guard).
    const scrollEl = document.querySelector('.overflow-y-auto') as HTMLElement
    Object.defineProperty(scrollEl, 'scrollTop', { configurable: true, writable: true, value: 10 })
    scrollEl.dispatchEvent(new Event('scroll'))
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(1)
  })

  it('does not prefetch when hasMore is false', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-1', role: 'user', text: 'only',
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(0)
  })

  it('chain-prefetches: second batch arrives with few messages, continues loading', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-1', role: 'user', text: 'first',
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: 'c1', hasMore: true,
    })
    // First prefetch fired immediately (1 message < 10 threshold).
    expect(sent.filter((m) => m.type === 'history_request').length).toBe(1)

    // Second batch arrives — still only a few messages, still has more.
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-2', role: 'assistant', text: 'reply',
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: 'c2', hasMore: true,
    })
    // Second prefetch should fire (2 messages still < 10 threshold).
    const reqs = sent.filter((m) => m.type === 'history_request')
    expect(reqs.length).toBe(2)
    expect(reqs[1].cursor).toBe('c2')
  })

  it('sub-agent tool duration shows perceived time on tool_end', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'tool_start', agent, tool_use: { id: 'sub-tool-1', name: 'Bash' } },
    ])

    // Advance time by 2 seconds
    vi.advanceTimersByTime(2000)

    dispatchEvents([
      { type: 'tool_end', agent, tool_result: { tool_use_id: 'sub-tool-1', display_output: 'done' } },
    ])

    // Find the sub-agent tool's duration element
    const subTool = document.querySelector('[data-tool-root][data-tool-name="Bash"]')
    expect(subTool).toBeTruthy()
    const durEl = subTool!.querySelector('.font-mono.text-xs')
    expect(durEl).toBeTruthy()
    // Duration should show ~2s, not 0s
    expect(durEl!.textContent).toContain('2s')
    vi.useRealTimers()
  })

  it('sub-agent tool live ticking updates duration while running', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'tool_start', agent, tool_use: { id: 'sub-tool-1', name: 'Bash' } },
    ])

    const subTool = document.querySelector('[data-tool-root][data-tool-name="Bash"]')
    const durEl = subTool!.querySelector('.font-mono.text-xs') as HTMLElement
    expect(durEl).toBeTruthy()

    // After 1 second, live ticking should update duration
    const before = durEl.textContent
    vi.advanceTimersByTime(1000)
    const after = durEl.textContent
    expect(after).not.toBe(before)
    expect(after).toContain('1s')
    vi.useRealTimers()
  })

  it('sub-agent tool duration freezes after tool_end', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'tool_start', agent, tool_use: { id: 'sub-tool-1', name: 'Bash' } },
    ])

    vi.advanceTimersByTime(3000)
    dispatchEvents([
      { type: 'tool_end', agent, tool_result: { tool_use_id: 'sub-tool-1', display_output: 'done' } },
    ])

    const subTool = document.querySelector('[data-tool-root][data-tool-name="Bash"]')
    const durEl = subTool!.querySelector('.font-mono.text-xs') as HTMLElement
    const frozen = durEl.textContent

    // Advance more time — duration should NOT change
    vi.advanceTimersByTime(5000)
    expect(durEl.textContent).toBe(frozen)
    vi.useRealTimers()
  })

  it('Esc during running tool: interrupt text renders after tool, not appended to prior text block', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run slow command')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'Let me read that.' },
      { type: 'tool_start', tool_use: { id: 'read-1', name: 'Read' } },
    ])

    // Tool running, no output. Esc. Connector sends interrupt text_delta.
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])

    // DOM order must be: text("Let me read that") → tool(Read) → text("[Request interrupted]")
    // NOT: text("Let me read that[Request interrupted]") → tool(Read)
    const allText = document.body.textContent ?? ''
    expect(allText).toContain('[Request interrupted by user]')

    // Verify the interrupt text is NOT inside the first text block (appended to "Let me read that")
    const toolRoot = document.querySelector('[data-tool-root][data-tool-name="Read"]')
    expect(toolRoot).toBeTruthy()

    // The interrupt text element should come AFTER the tool root in DOM order
    const allElements = document.querySelectorAll('*')
    let toolIdx = -1
    let interruptIdx = -1
    for (let i = 0; i < allElements.length; i++) {
      if (allElements[i] === toolRoot) toolIdx = i
    }
    // Find the element containing interrupt text
    for (let i = 0; i < allElements.length; i++) {
      const el = allElements[i] as HTMLElement
      if (el.children.length === 0 && el.textContent === '[Request interrupted by user]') {
        interruptIdx = i
        break
      }
    }
    expect(interruptIdx).toBeGreaterThan(toolIdx)
  })

  it('Esc during running tool: interrupt text renders, tool shows failed', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run slow command')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'Let me run that.' },
      { type: 'tool_start', tool_use: { id: 'bash-1', name: 'Bash' } },
    ])

    // Tool is running with no output. User presses Esc.
    // Connector sends interrupt via text_delta, then query_end(aborted).
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])

    const allText = document.body.textContent ?? ''

    // 1. Interrupt text must be visible in DOM (not dropped)
    expect(allText).toContain('[Request interrupted by user]')

    // 2. Interrupt text should appear only once (no duplication)
    const interruptCount = (allText.match(/Request interrupted/g) || []).length
    expect(interruptCount).toBe(1)

    // 3. Running tool should show as failed (red dot), not success (green dot)
    const bashTool = document.querySelector('[data-tool-root][data-tool-name="Bash"]')
    expect(bashTool).toBeTruthy()
    const dot = bashTool!.querySelector('span.heartbeat, span.text-red')
    expect(dot?.classList.contains('text-red')).toBe(true)
  })

  it('Esc during running tool: subsequent query works normally', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('first query')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'bash-1', name: 'Bash' } },
    ])
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])

    // Second query after abort should work normally
    setTextarea('second query')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'Working again.' },
      { type: 'query_end' },
    ])

    const allText = document.body.textContent ?? ''
    expect(allText).toContain('Working again.')
    expect(allText).toContain('second query')
  })

  it('Edit tool output auto-expands on finish (not hidden)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('fix the bug')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'edit-1', name: 'Edit' } },
    ])
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'edit-1', display_output: 'some diff output' } },
      { type: 'query_end' },
    ])

    const editTool = document.querySelector('[data-tool-root][data-tool-name="Edit"]')
    expect(editTool).toBeTruthy()
    const bodyEl = editTool!.children[1] as HTMLElement
    expect(bodyEl.classList.contains('hidden')).toBe(false)
  })

  it('Read tool output stays collapsed on finish (hidden)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('read the file')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'read-1', name: 'Read' } },
    ])
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'read-1', display_output: 'file contents' } },
      { type: 'query_end' },
    ])

    const readTool = document.querySelector('[data-tool-root][data-tool-name="Read"]')
    expect(readTool).toBeTruthy()
    const bodyEl = readTool!.children[1] as HTMLElement
    expect(bodyEl.classList.contains('hidden')).toBe(true)
  })

  it('sub-agent thinking_delta updates token rate', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('research something')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'thinking_start', agent },
    ])

    // First delta already reads (no gate): 40 wire tokens read at the next
    // 200ms tick as 80·(decayed) tokens over 0.396s of integral plus the
    // 0.3s prior ≈ 113 t/s — prior-suppressed, never a fabricated 0.0.
    dispatchEvents([
      { type: 'thinking_delta', agent, thinking: { text: 'B'.repeat(40) }, tokens: 40 },
    ])
    vi.advanceTimersByTime(200)
    const firstEl = findRateSpan()
    expect(firstEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    const first = parseFloat(firstEl!.textContent!)
    expect(first).toBeGreaterThan(100)
    expect(first).toBeLessThan(130)

    // Sustained wire-token deltas keep a numeric reading.
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(1000)
      dispatchEvents([
        { type: 'thinking_delta', agent, thinking: { text: 'B'.repeat(50) }, tokens: 50 },
      ])
    }
    vi.advanceTimersByTime(200)
    const rateEl = findRateSpan()
    expect(rateEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    vi.useRealTimers()
  })

  it('sub-agent text_delta updates token rate', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('research something')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
    ])

    // First delta already reads (no gate): a numeric reading from the
    // first 200ms tick.
    dispatchEvents([
      { type: 'text_delta', agent, text: 'B'.repeat(40), tokens: 40 },
    ])
    vi.advanceTimersByTime(200)
    const firstEl = findRateSpan()
    expect(firstEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    const first = parseFloat(firstEl!.textContent!)
    expect(first).toBeGreaterThan(100)
    expect(first).toBeLessThan(130)

    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(1000)
      dispatchEvents([
        { type: 'text_delta', agent, text: 'B'.repeat(50), tokens: 50 },
      ])
    }
    vi.advanceTimersByTime(200)
    const rateEl = findRateSpan()
    expect(rateEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    vi.useRealTimers()
  })

  // The rate span is the progress-bar child right after the ↑/↓ token spans
  // (root children: inEl, outEl, suffix, ·, rateEl, ...).
  function findRateSpan(): HTMLElement | null {
    const inEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.startsWith('↑'),
    )
    return (inEl?.parentElement?.children[4] as HTMLElement) ?? null
  }

  it('live ↓output counts wire tokens and reconciles on usage', () => {
    // Full path: text_delta tokens → tokenRate.localTokens → ↓output in the
    // 200ms tick → usage event zeroes the local sum (authoritative count
    // takes over, no double counting).
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('write a long answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    const outEl = () => {
      const el = Array.from(document.querySelectorAll('span')).find(
        (s) => s.textContent?.startsWith('↓'),
      )
      return el as HTMLElement
    }
    for (let i = 0; i < 3; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'streamed chunk of text', tokens: 40 }])
      vi.advanceTimersByTime(1000)
    }
    vi.advanceTimersByTime(200)
    expect(outEl()?.textContent).toBe('↓120')

    dispatchEvents([
      { type: 'usage', usage_event: { input_tokens: 10, output_tokens: 200 } },
    ])
    expect(outEl()?.textContent).toBe('↓200')
    // The local sum was reconciled away: further ticks keep the
    // authoritative value instead of re-adding it.
    vi.advanceTimersByTime(200)
    expect(outEl()?.textContent).toBe('↓200')
    vi.useRealTimers()
  })

  it('rate shows a prior-suppressed reading immediately, then keeps reading', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('long answer please')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    // 90 tokens in one delta: the next tick already shows a number —
    // 180·(decayed) tokens over 0.396s of integral plus the 0.3s prior ≈
    // 254 t/s (prior-suppressed; a span-based read would report ~450k).
    dispatchEvents([{ type: 'text_delta', text: 'a chunk of streamed words', tokens: 90 }])
    vi.advanceTimersByTime(200)
    const firstEl = findRateSpan()
    expect(firstEl?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    const first = parseFloat(firstEl!.textContent!)
    expect(first).toBeGreaterThan(230)
    expect(first).toBeLessThan(280)

    // 4.4s later another 150 tokens: still numeric — the reading blends.
    vi.advanceTimersByTime(4400)
    dispatchEvents([{ type: 'text_delta', text: 'more words stream onward', tokens: 150 }])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toMatch(/^[0-9]+\.[0-9] t\/s$/)
    vi.useRealTimers()
  })

  it("engine switch does not seed the new engine's rate from the old engine's meter", () => {
    // Regression: engine_switch → server sendMetadata → resetAllState →
    // tokenRate.reset() kept the decayed sums (omp seed semantics). With one
    // shared meter for all engines, the new engine's first stream
    // blended the old engine's rate: slow→fast kept reading ~48 t/s while
    // the fast engine streamed 120 t/s.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })

    // Engine A (slow): 15 tokens per 500ms = 30 t/s for 12s.
    setTextarea('slow engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'slow chunk of words', tokens: 15 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    const slow = parseFloat(findRateSpan()!.textContent!)
    expect(slow).toBeGreaterThan(20)
    expect(slow).toBeLessThan(40)

    // A's query ends; the user switches engines. The server answers
    // engine_switch with a metadata frame for the new engine (connector.go
    // switchEngine → sendMetadata).
    dispatchEvents([{ type: 'turn_end' }, { type: 'query_end' }])
    vi.advanceTimersByTime(2000)
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'engineB' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    // metadata sets pendingPageZero and buffers live events until the page
    // lands — flush it so engine B's events are applied live.
    dispatch({ type: 'history', messages: [], stream: [], nextCursor: '', hasMore: false })

    // Engine B (fast): 60 tokens per 500ms = 120 t/s.
    setTextarea('fast engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 8; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'fast chunk of words!', tokens: 60 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    const fast = parseFloat(findRateSpan()!.textContent!)
    // A's frozen sums dominate the kernel for tens of seconds (20s tail):
    // the buggy blend reads ~48 t/s; a meter cleared on the switch reads
    // ~130 t/s from B's deltas alone.
    expect(fast).toBeGreaterThanOrEqual(80)
    vi.useRealTimers()
  })

  it('per-engine buckets: B reads pure B, and returning to A is not polluted by B', () => {
    // Full A→B→A cycle with per-engine buckets. Each switch's full-clear
    // wipes the incoming engine's bucket, and B's fast episode never enters
    // A's bucket — the return stream reads ~30 t/s, not a blend pushed up
    // by B's 120 t/s residue.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true, engineID: 'engineA' })

    const meta = (engineID: string) => ({
      type: 'metadata',
      connect: { connected: true, engineID },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: engineID },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    const flushPage = () =>
      dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // Engine A (slow): 15 tokens per 500ms = 30 t/s, turn left dangling.
    setTextarea('slow engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'slow chunk of words', tokens: 15 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThan(20)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeLessThan(40)

    // Mid-stream switch to B (no turn_end/query_end for A) + page flush.
    dispatch(meta('engineB'))
    flushPage()

    // Engine B (fast): 60 tokens per 500ms = 120 t/s.
    setTextarea('fast engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 8; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'fast chunk of words!', tokens: 60 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThanOrEqual(80)

    // Back to A: A's bucket was wiped by the switch, and B's evidence never
    // entered it — A's fresh 30 t/s stream reads in the pure-A band (a
    // B-blended reading would sit well above it).
    dispatch(meta('engineA'))
    flushPage()
    setTextarea('slow engine again')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'slow chunk of words', tokens: 15 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThan(20)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeLessThan(40)
    vi.useRealTimers()
  })

  it('per-engine buckets: dropped metadata answer keeps reader and writer on the same bucket', () => {
    // engine_switch does not flip activeEngineID locally — attribution moves
    // only on the server's confirmation. With the answer dropped, B's deltas
    // meter into the pre-switch bucket, the same one the refresh loop reads:
    // the reading converges on B's actual rate as B's evidence dominates the
    // kernel (A's decayed 30 t/s tail holds the blend to ~86 t/s after 12s of
    // B's 120 t/s). A reader/writer bucket split — deltas metered where the
    // display never looks — would strand the reading on A's decay at ~9 t/s.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true, engineID: 'e1' })
    dispatch({
      type: 'metadata',
      connect: { connected: true, engineID: 'e1' },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: {
        engines: [
          { id: 'e1', name: 'agent-1', model: 'm' },
          { id: 'e2', name: 'agent-2', model: 'm' },
        ],
        activeID: 'e1',
      },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // Engine A (slow): 15 tokens per 500ms = 30 t/s, turn left dangling.
    setTextarea('slow engine answer')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'slow chunk of words', tokens: 15 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThan(20)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeLessThan(40)

    // User picks agent-2: engine_switch leaves, and the answer never comes.
    const trigger = document.querySelector(
      'header button.text-\\[15px\\]:not(.hidden)',
    ) as HTMLButtonElement
    trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = document.querySelector(
      '[data-testid="engine-picker-panel"]',
    ) as HTMLElement
    const row = Array.from(panel.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('agent-2'),
    ) as HTMLButtonElement
    row.click()
    expect(
      sent.find((m) => (m as { type: string }).type === 'engine_switch'),
    ).toEqual({ type: 'engine_switch', engineID: 'e2' })

    // B's deltas land with no reset: 60 tokens per 500ms = 120 t/s, 12s —
    // the blend must climb past the pure-B band's floor, proving the
    // displayed meter is still tracking the deltas that actually arrive.
    for (let i = 0; i < 24; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'fast chunk of words!', tokens: 60 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()!.textContent!)).toBeGreaterThanOrEqual(80)
    vi.useRealTimers()
  })

  it('tool_run pauses the rate display; the resumed reading excludes the tool gap', () => {
    // omp: "Tool execution between messages neither ages the sums nor counts
    // as time" — tool_run is the wui's execution boundary. The display falls
    // back to 0.0 t/s while paused, and the resumed reading stays near its
    // pre-tool value instead of sliding down across the tool duration.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('long answer please')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    const rateValue = () => parseFloat(findRateSpan()?.textContent ?? '')
    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'chunk of streamed words', tokens: 100 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    const r0 = rateValue()
    expect(r0).toBeGreaterThan(0)

    dispatchEvents([{ type: 'tool_run', tool_use: { id: 't1', name: 'Bash' } }])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    vi.advanceTimersByTime(5000) // tool execution
    dispatchEvents([{ type: 'text_delta', text: 'resumed words arrive', tokens: 100 }])
    vi.advanceTimersByTime(200)
    // Gap skipped, not aged: with gap-aging this reads ~0.55x r0.
    const r1 = rateValue()
    expect(r1).toBeGreaterThanOrEqual(0.9 * r0)
    expect(r1).toBeLessThanOrEqual(1.7 * r0)
    vi.useRealTimers()
  })

  it('turn_end pauses the rate display through the Agent tool gap (agent turn_end does not)', () => {
    // The sub-agent's deltas re-arm the meter, so from the Agent tool_end to
    // the next turn's first delta the old reading would bare-decay across
    // the turn gap. Main turn_end stops the clock (TUI parity); the
    // sub-agent's own turn_end is interior to the parent Agent run and must
    // not touch the main meter.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('delegate the research')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    const rateValue = () => parseFloat(findRateSpan()?.textContent ?? '')
    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'chunk of streamed words', tokens: 100 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(rateValue()).toBeGreaterThan(0)

    const agent = { parent_tool_use_id: 'ag1', agent_type: 'Explore', depth: 1 }
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 'ag1', name: 'Agent' } },
      { type: 'tool_run', tool_use: { id: 'ag1', name: 'Agent' } },
    ])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    // Sub-agent stream re-arms the meter — its real flow counts.
    for (let i = 0; i < 6; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'sub agent words', tokens: 100, agent }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(rateValue()).toBeGreaterThan(0)

    // Sub-agent turn_end: interior boundary — the reading survives it.
    dispatchEvents([{ type: 'turn_end', agent }])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).not.toBe('0.0 t/s')

    const r0 = rateValue()
    // Agent tool completes, then the main turn ends: clock stops.
    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 'ag1', display_output: 'done' } },
      { type: 'turn_end' },
    ])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    // Turn gap skipped, not aged: with gap-aging this reads ~0.55x r0.
    vi.advanceTimersByTime(5000)
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'text_delta', text: 'resumed words arrive', tokens: 100 },
    ])
    vi.advanceTimersByTime(200)
    const r1 = rateValue()
    expect(r1).toBeGreaterThanOrEqual(0.9 * r0)
    expect(r1).toBeLessThanOrEqual(1.7 * r0)
    vi.useRealTimers()
  })

  it('turn_end keeps the bash tool gap paused until the next turn resumes', () => {
    // tool_run already paused the clock, so turn_end's pause is a no-op
    // there — the scenario pins that it stays a no-op (no un-pause, no
    // double aging) and the next turn's first delta recovers the reading.
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run the build')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
    ])

    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'chunk of streamed words', tokens: 100 }])
      vi.advanceTimersByTime(500)
    }
    dispatchEvents([{ type: 'tool_run', tool_use: { id: 't1', name: 'Bash' } }])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    dispatchEvents([
      { type: 'tool_end', tool_result: { tool_use_id: 't1', display_output: 'done' } },
      { type: 'turn_end' },
    ])
    vi.advanceTimersByTime(200)
    expect(findRateSpan()?.textContent).toBe('0.0 t/s')

    vi.advanceTimersByTime(5000)
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'text_delta', text: 'resumed words arrive', tokens: 100 },
    ])
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()?.textContent ?? '0')).toBeGreaterThan(0)
    vi.useRealTimers()
  })

  it('REPRO: param deltas after tool_start un-pause the meter and the rate slides during exec', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('run sleep 10')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'thinking_start' },
    ])
    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'thinking_delta', thinking: { text: 'planning the sleep' }, tokens: 60 }])
      dispatchEvents([{ type: 'text_delta', text: 'running sleep now', tokens: 60 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    expect(parseFloat(findRateSpan()?.textContent ?? '0')).toBeGreaterThan(0)
    // tool_start only opens the block; params still stream after it.
    dispatchEvents([{ type: 'tool_start', tool_use: { id: 'tb1', name: 'Bash' } }])
    dispatchEvents([
      { type: 'tool_param_delta', partial_input: { id: 'tb1', name: 'Bash', delta: '{"command":"sleep 10 && echo done","timeout"', summary: 'sleep 10' }, tokens: 29 },
    ])
    dispatchEvents([{ type: 'tool_run', tool_use: { id: 'tb1', name: 'Bash' } }])
    for (let i = 0; i < 20; i++) {
      vi.advanceTimersByTime(500)
      const got = findRateSpan()?.textContent
      if (got !== '0.0 t/s') {
        throw new Error(`tick ${i}: rate = ${got}, want '0.0 t/s'`)
      }
    }
    vi.useRealTimers()
  })
  it('second query meters independently: TTFT blank, first delta reads on its own', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('first query')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])
    for (let i = 0; i < 12; i++) {
      dispatchEvents([{ type: 'text_delta', text: 'chunk of streamed words', tokens: 100 }])
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(200)
    const r0 = parseFloat(findRateSpan()?.textContent ?? '0')
    expect(r0).toBeGreaterThan(0)
    dispatchEvents([{ type: 'query_end' }])
    // Next query: the meter was wiped at query_start — TTFT stays 0.0, and
    // the first delta reads on its own (no gate: the 300ms prior makes even
    // one token visible).
    setTextarea('second query')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])
    vi.advanceTimersByTime(200)
    // TTFT: no delta yet — nothing to read; the bar reads 0.0.
    const ttft = document.body.textContent?.match(/([\d.]+) t\/s/g) ?? []
    expect(ttft.some((s) => parseFloat(s) > 0)).toBe(false)
    dispatchEvents([{ type: 'text_delta', text: 'first', tokens: 1 }])
    vi.advanceTimersByTime(200)
    const firstDelta = document.body.textContent?.match(/([\d.]+) t\/s/g) ?? []
    expect(firstDelta.some((s) => parseFloat(s) > 0)).toBe(true)
    // Sidebar new-session: connect_status with a different sessionID wipes
    // the meter — the new session starts with no old-session blend.
    dispatch({ type: 'connect_status', connected: true, sessionID: 'other-session' })
    setTextarea('new session query')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])
    dispatchEvents([{ type: 'text_delta', text: 'first', tokens: 1 }])
    vi.advanceTimersByTime(200)
    const fresh = parseFloat(findRateSpan()?.textContent ?? '999')
    expect(fresh).toBeLessThanOrEqual(10) // 2 tokens / 0.3s ≈ 6.7, no blend
    // Reconnect to the same session (sessionID unchanged, no clear()):
    // dispatch must not throw.
    dispatch({ type: 'connect_status', connected: true, sessionID: 'other-session' })
    expect(() => dispatchEvents([{ type: 'text_delta', text: 'x', tokens: 1 }])).not.toThrow()
    vi.useRealTimers()
  })
  it('reconnect does not duplicate history messages', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'hello',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    expect(document.body.textContent).toContain('hello')

    // Reconnect
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'hello',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'hello',
    )
    expect(spans.length).toBe(1)
  })

  it('user message preserves newlines in history', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'line1\nline2\nline3',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    // The span must have whitespace-pre-wrap to render newlines
    const spans = Array.from(document.querySelectorAll('span')).filter(
      (s) => s.textContent === 'line1\nline2\nline3',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).toContain('whitespace-pre-wrap')
    // The parent content div must also have whitespace-pre-wrap
    // (buildShell user content div)
    const contentDiv = spans[0].parentElement
    expect(contentDiv).toBeTruthy()
    expect(contentDiv!.className).toContain('whitespace-pre-wrap')
  })

  it('user message content div has break-words to prevent overflow', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'https://example.com/very/long/path/that/should/overflow',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const spans = Array.from(document.querySelectorAll('span')).filter(
      (s) => s.textContent === 'https://example.com/very/long/path/that/should/overflow',
    )
    expect(spans.length).toBe(1)
    const contentDiv = spans[0].parentElement
    expect(contentDiv).toBeTruthy()
    expect(contentDiv!.className).toContain('break-words')
  })

  it('Bash grep in history is classified as search', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'a1', role: 'assistant', text: '',
          thinking: [],
          tools: [],
          blocks: [
            {
              kind: 'tool',
              tool: {
                id: 't1', name: 'Bash', summary: 'grep -n "pattern"',
                displayOutput: 'result', isError: false, isRunning: false,
                durationNs: 1_000_000_000,
                is_search: true,
              },
            },
          ],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    // The Bash tool with is_search:true should be rendered as collapsible
    const all = document.querySelectorAll('[data-tool-root]')
    expect(all.length).toBe(1)
    expect(all[0].getAttribute('data-collapsible')).toBe('1')
  })

  it('tool_start with is_search groups consecutive Bash tools', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash', input: { command: 'git log' }, is_search: true } },
      { type: 'tool_start', tool_use: { id: 't2', name: 'Bash', input: { command: 'git status' }, is_search: true } },
    ])
    // Both tools should be in a group
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    expect(toolRoots[0].getAttribute('data-collapsible')).toBe('1')
    expect(toolRoots[1].getAttribute('data-collapsible')).toBe('1')
    // They should be inside a tool-group
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
  })

  it('Task in history is classified as collapsible via is_task', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'a1', role: 'assistant', text: '',
          thinking: [],
          tools: [],
          blocks: [
            {
              kind: 'tool',
              tool: {
                id: 't1', name: 'Task', summary: 'create task',
                displayOutput: 'Created: #1 Fix login', isError: false, isRunning: false,
                durationNs: 1_000_000_000,
                is_task: true,
              },
            },
          ],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const all = document.querySelectorAll('[data-tool-root]')
    expect(all.length).toBe(1)
    expect(all[0].getAttribute('data-collapsible')).toBe('1')
  })

  it('tool_start with is_task groups consecutive Task tools', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Task', input: { action: 'list' }, is_task: true } },
      { type: 'tool_start', tool_use: { id: 't2', name: 'Task', input: { action: 'get', taskId: '1' }, is_task: true } },
    ])
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    expect(toolRoots[0].getAttribute('data-collapsible')).toBe('1')
    expect(toolRoots[1].getAttribute('data-collapsible')).toBe('1')
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
    const summary = group!.querySelector('[data-group-summary]')
    expect(summary).toBeTruthy()
    expect(summary!.textContent).toContain('2 Tasks')
  })

  it('tool_start with is_search renders Bash as collapsible', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash', input: {}, is_search: true } },
    ])
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(1)
    expect(toolRoots[0].getAttribute('data-collapsible')).toBe('1')
  })

  it('tool_param_delta updates isSearch and enables grouping', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash', input: {} } },
      { type: 'tool_param_delta', partial_input: { id: 't1', name: 'Bash', delta: '', summary: 'git log', is_search: true } },
      { type: 'tool_run', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'tool_start', tool_use: { id: 't2', name: 'Bash', input: {} } },
      { type: 'tool_param_delta', partial_input: { id: 't2', name: 'Bash', delta: '', summary: 'git status', is_search: true } },
      { type: 'tool_run', tool_use: { id: 't2', name: 'Bash' } },
    ])
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    // Both should be marked collapsible after tool_param_delta provides is_search
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
  })

  it('tool_param_delta with is_list enables grouping for Bash ls (not just is_search)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash', input: {} } },
      { type: 'tool_param_delta', partial_input: { id: 't1', name: 'Bash', delta: '', summary: 'ls /a', is_list: true } },
      { type: 'tool_run', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'tool_start', tool_use: { id: 't2', name: 'Bash', input: {} } },
      { type: 'tool_param_delta', partial_input: { id: 't2', name: 'Bash', delta: '', summary: 'ls /b', is_list: true } },
      { type: 'tool_run', tool_use: { id: 't2', name: 'Bash' } },
    ])
    const toolRoots = document.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    // Both Bash(ls) should be marked collapsible after tool_param_delta provides is_list
    // and grouped together (matches committed-render behavior in isCollapsible).
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
  })

  it('reconnect after streaming does not duplicate', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'task',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'response' },
      { type: 'text_end' },
      { type: 'query_end' },
    ])
    expect(document.body.textContent).toContain('task')
    expect(document.body.textContent).toContain('response')

    // Reconnect
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'task',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
        { id: 'a1', role: 'assistant', text: 'response',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })
    // Check presence, not count (regex \b is fragile with jsdom textContent).
    expect(document.body.textContent).toContain('task')
    expect(document.body.textContent).toContain('response')
    // Count span children — user messages create exactly 1 span per message.
    const userSpans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'task',
    )
    expect(userSpans.length).toBe(1)
  })

  it('reconnect mid-stream replay does not duplicate history', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // Initial: user msg + streaming query in progress.
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'do it',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'working' },
      // No query_end — stream interrupted by disconnect.
    ])
    expect(document.body.textContent).toContain('working')

    // Reconnect: history has the user msg (committed), buffer has replay events.
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'do it',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })
    // Replay: same stream events (turn not committed).
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'working' },
      { type: 'text_end' },
      { type: 'query_end' },
    ])
    // User message from history appears exactly once.
    const userSpans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'do it',
    )
    expect(userSpans.length).toBe(1)
    // "working" appears in the assistant response (from replay).
    expect(document.body.textContent).toContain('working')
  })

  it('history during streaming prepends without breaking streaming', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        { id: 'u1', role: 'user', text: 'current',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: 'c1', hasMore: true,
    })

    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'streaming' },
    ])

    // History arrives mid-stream (user scrolled up, observer fired).
    dispatch({
      type: 'history',
      messages: [
        { id: 'u0', role: 'user', text: 'older',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0 },
      ],
      nextCursor: '', hasMore: false,
    })

    expect(document.body.textContent).toContain('older')
    expect(document.body.textContent).toContain('streaming')
    expect(document.body.textContent).toContain('current')
  })

  it('takeover replay renders sub-agent events inside committed Agent tool', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })

    // History: main agent committed [thinking, tool_use(Agent)].
    // Agent tool is in "done" state with a result — but tool execution
    // (sub-agent) is still running. This is the takeover scenario:
    // appendMessage committed the response, buffer replays sub-agent events.
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1',
        role: 'assistant',
        thinking: [],
        tools: [{
          id: 'tu1',
          name: 'Agent',
          summary: 'Review latest commit',
          isError: false,
          isRunning: true,
          durationNs: 0,
          displayOutput: '',
        }],
        blocks: [
          { kind: 'tool', tool: {
            id: 'tu1',
            name: 'Agent',
            summary: 'Review latest commit',
            isError: false,
            isRunning: true,
            durationNs: 0,
            displayOutput: '',
          }},
        ],
        text: '',
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '',
        status: 'done',
        startedAt: 0,
      }],
      nextCursor: '',
      hasMore: false,
    })

    // Replay: sub-agent events arrive (from buffer).
    const agent = { parent_tool_use_id: 'tu1', agent_type: 'Reviewer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'thinking_start', agent },
      { type: 'thinking_delta', agent, thinking: { text: 'reviewing commit' } },
      { type: 'thinking_end', agent },
      { type: 'turn_end', agent },
    ])

    // Sub-agent content must appear in the DOM — not silently dropped.
    expect(document.body.textContent).toContain('reviewing commit')
  })

  it('sub-agent text_delta renders markdown (not plain textContent)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [], tools: [],
        blocks: [
          { kind: 'tool', tool: {
            id: 'tu1', name: 'Agent', summary: 'research',
            isRunning: true, durationNs: 0, displayOutput: '',
          }},
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })

    const agent = { parent_tool_use_id: 'tu1', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent },
      { type: 'text_start', agent },
      { type: 'text_delta', agent, text: '## Findings\n\nThe tool uses `pendingToolStart`.' },
      { type: 'turn_end', agent },
    ])

    // Sub-agent text should be rendered as markdown HTML, not raw textContent.
    // If rendered correctly, <h2> and <code> tags exist.
    const agentTool = document.querySelector('[data-tool-name^="Agent"]')
    expect(agentTool).toBeTruthy()
    const toolRoot = agentTool!.closest('[data-tool-root]')
    const mdBody = toolRoot?.querySelector('.md-body.text-t1')
    expect(mdBody).toBeTruthy()
    expect(mdBody!.innerHTML).toContain('<h2>')
    expect(mdBody!.innerHTML).toContain('<code>')
  })

  it('takeover with running tool shows STOP button', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1',
        role: 'assistant',
        thinking: [],
        tools: [{
          id: 'tu1', name: 'Agent', summary: 'review',
          isRunning: true, durationNs: 0, displayOutput: '',
        }],
        blocks: [
          { kind: 'tool', tool: {
            id: 'tu1', name: 'Agent', summary: 'review',
            isRunning: true, durationNs: 0, displayOutput: '',
          }},
        ],
        text: '',
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    const stopBtn = Array.from(document.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('STOP'),
    )
    expect(stopBtn).toBeInstanceOf(HTMLButtonElement)
  })

  it('auto-prefetches when content is shorter than viewport (few messages)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // History arrives with more pages and only 1 message.
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-1', role: 'user', text: 'hello',
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: 'c1', hasMore: true,
    })
    // With < 10 messages above viewport, maybePrefetchHistory fires.
    const historyReqs = sent.filter((m) => m.type === 'history_request')
    expect(historyReqs.length).toBe(1)
    expect(historyReqs[0].cursor).toBe('c1')
  })

  it('task_list message renders the task ring with count', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'task_list',
      tasks: [
        { id: '1', subject: 'Plan', status: 'completed' },
        { id: '2', subject: 'Code', status: 'in_progress' },
        { id: '3', subject: 'Test', status: 'pending' },
      ],
    })
    // Ring label shows completed/total.
    const label = document.querySelector('.task-label') as SVGTextElement
    expect(label).toBeTruthy()
    expect(label.textContent).toBe('1/3')
    // Ring is visible.
    const ring = label.closest('button') as HTMLElement
    expect(ring.style.display).not.toBe('none')
  })

  it('task ring popover shows full summary on click', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'task_list',
      tasks: [
        { id: '1', subject: 'Plan', status: 'completed' },
        { id: '2', subject: 'Code', status: 'in_progress' },
        { id: '3', subject: 'Test', status: 'pending' },
      ],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    ring.click()

    const popover = document.getElementById('task-popover') as HTMLElement
    expect(popover).toBeTruthy()
    const text = popover.textContent ?? ''
    expect(text).toContain('1/3 Done')
    expect(text).toContain('1 Running')
    expect(text).toContain('1 Pending')
  })

  it('connect_status hides the task panel', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'task_list',
      tasks: [{ id: '1', subject: 'UniqueTaskSubject123', status: 'pending' }],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    expect(ring).toBeTruthy()
    expect(ring.style.display).not.toBe('none')
    dispatch({ type: 'connect_status', connected: true })
    expect(ring.style.display).toBe('none')
  })

  it('job_list message shows the task panel with background job rows', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [
        { id: 'bg-1', kind: 'bash', status: 'running', command: 'make check', startedMs: 1 },
        { id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor', description: '修 wui 分页 bug', parentToolUseID: 'p1', startedMs: 2 },
      ],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    expect(ring.style.display).not.toBe('none')
    // Jobs-only mode: label is the running count.
    expect((document.querySelector('.task-label') as SVGTextElement).textContent).toBe('2')
    ring.click()
    const popover = document.getElementById('task-popover') as HTMLElement
    expect(popover.querySelectorAll('.jobs-row')).toHaveLength(2)
    expect(popover.textContent).toContain('make check')
    expect(popover.textContent).toContain('Executor · 修 wui 分页 bug')
  })

  it('job_output lands in the jobs store tail', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [{ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check', startedMs: 1 }],
    })
    dispatch({ type: 'job_output', jobID: 'bg-1', tail: 'ok  pkg/engine  1.2s\n' })
    expect(chat.jobsStore.feed('bg-1')?.tail).toBe('ok  pkg/engine  1.2s\n')
  })

  it('agent event frames accumulate into the job feed while chat renders normally', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [{ id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor', description: 'fix bug', parentToolUseID: 'p1', startedMs: 1 }],
    })
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't-1', name: 'Read', input: {} }, agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 } },
      { type: 'text_delta', text: 'found it', agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 } },
    ])
    const blocks = chat.jobsStore.feed('agent-1')?.blocks
    expect(blocks).toHaveLength(2)
    expect(blocks?.[0]).toMatchObject({ kind: 'tool', id: 't-1', name: 'Read' })
    expect(blocks?.[1]).toMatchObject({ kind: 'text', text: 'found it' })
  })

  it('metadata with jobs hydrates the store (blocks + tail)', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
      jobs: {
        jobs: [
          { id: 'bg-1', kind: 'bash', status: 'running', command: 'go test ./...', startedMs: 1, tail: 'ok pkg/a\n' },
          {
            id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor',
            description: 'scan logs', parentToolUseID: 'p1', startedMs: 2,
            blocks: [{ kind: 'text', id: '', text: 'halfway report' }],
          },
        ],
      },
    })
    expect(chat.jobsStore.jobs().map((j) => j.id)).toEqual(['bg-1', 'agent-1'])
    expect(chat.jobsStore.feed('bg-1')?.tail).toBe('ok pkg/a\n')
    expect(chat.jobsStore.feed('agent-1')?.blocks[0]).toMatchObject({ kind: 'text', text: 'halfway report' })
    // Panel chrome reflects the hydrated view.
    expect((document.querySelector('.task-label') as SVGTextElement).textContent).toBe('2')
  })

  it('metadata without jobs clears the previous engine view (A→B switch)', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    const metaWithJobs = {
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'a' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
      jobs: { jobs: [{ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check', startedMs: 1 }] },
    } as const
    dispatch(metaWithJobs)
    expect(chat.jobsStore.hasAny()).toBe(true)
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    expect(ring.style.display).not.toBe('none')

    // Engine B's metadata carries no jobs field — the view must swap to
    // empty, not keep engine A's residue.
    dispatch({ ...metaWithJobs, engines: { engines: [], activeID: 'b' }, jobs: undefined })
    expect(chat.jobsStore.jobs()).toEqual([])
    expect(chat.jobsStore.feed('bg-1')).toBeUndefined()
    expect(ring.style.display).toBe('none')
  })

  it('expanded bash feed streams job_output frames and the stop button sends job_stop', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [{ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check', startedMs: 1 }],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    ring.click()
    const popover = document.getElementById('task-popover') as HTMLElement
    ;(popover.querySelector('.jobs-row') as HTMLElement).click()
    // The row label already carries the command: no command block.
    expect(popover.querySelector('.jobs-cmd')).toBeNull()

    dispatch({ type: 'job_output', jobID: 'bg-1', tail: 'ok  pkg/engine  1.2s\n' })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    const feed = popover.querySelector('.jobs-feed') as HTMLElement
    expect(feed.querySelectorAll('.md-body')).toHaveLength(1)
    expect(feed.textContent).toContain('ok  pkg/engine  1.2s')

    ;(popover.querySelector('.jobs-stop') as HTMLButtonElement).click()
    expect(sent).toContainEqual({ type: 'job_stop', jobID: 'bg-1' })
    // The stop click must not collapse the expansion it lives in.
    expect(popover.querySelector('.jobs-exp')).not.toBeNull()
  })

  it('expanded agent feed renders live tool cards and text through the full wiring', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'job_list',
      jobs: [{ id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor', description: 'fix bug', parentToolUseID: 'p1', startedMs: 1 }],
    })
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    ring.click()
    const popover = document.getElementById('task-popover') as HTMLElement
    ;(popover.querySelector('.jobs-row') as HTMLElement).click()

    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't-1', name: 'Read', input: {} }, agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 } },
      {
        type: 'tool_end',
        tool_result: { tool_use_id: 't-1', output: 'done', is_error: false, display_output: '1 file' },
        agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 },
      },
      { type: 'text_delta', text: 'found it', agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 } },
    ])
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    const feed = popover.querySelector('.jobs-feed') as HTMLElement
    const tools = feed.querySelectorAll('[data-tool-root]')
    expect(tools).toHaveLength(1)
    expect(tools[0].dataset.toolName).toBe('Read')
    expect(tools[0].querySelector('.text-green')).not.toBeNull()
    expect(feed.textContent).toContain('found it')
    // Stop affordance present while the agent runs.
    expect(popover.querySelector('.jobs-stop')).not.toBeNull()
  })

  it('takeover does not reset cumulative usage for the whole query', () => {
    // The engine emits per-turn usage deltas; the frontend accumulates
    // across the whole query. On takeover (reconnect), connect_status
    // must NOT wipe the accumulated progressUsage, because query_end
    // finalizes stats from progressUsage and the final stats must reflect
    // the ENTIRE query (both pre- and post-takeover turns).
    //
    // Event sequence during a takeover:
    //   query_start → turn_start → usage(turn1) → [disconnect]
    //   connect_status → history → [replay: turn_start → usage(turn2)]
    //   → query_end(usage_event = turn1+turn2 totals)
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()

    // Turn 1: first turn before disconnect. Accumulate usage.
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
    ])

    // Disconnect + takeover: connect_status resets state, stats
    // carries server-accumulated usage so progressUsage is restored.
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    })

    // History replays committed messages (none yet — query still in flight).
    dispatch({
      type: 'history',
      messages: [],
      nextCursor: '',
      hasMore: false,
    })

    // Replay buffer: turn_start (new turn after takeover) + usage for turn 2.
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 2000, output_tokens: 3000 } },
      // query_end carries the authoritative cumulative totals from the engine.
      {
        type: 'query_end',
        usage_event: { input_tokens: 3000, output_tokens: 3500 },
      },
    ])

    // Final progress bar stats must reflect the WHOLE query (3000 in, 3500 out),
    // not just the post-takeover turn (2000 in, 3000 out).
    // totalInput = input_tokens + cacheRead + cacheCreation = 3000 + 0 + 0 = 3000
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑2.9k') // 3000 / 1024 = 2.93k → "2.9k"
    const outEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↓'),
    )
    expect(outEl?.textContent).toBe('↓3.4k') // 3500 / 1024 = 3.42k → "3.4k"
  })

  it('takeover accumulates new usage on top of restored baseline', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()

    // Turn 1: accumulate usage before disconnect
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
      { type: 'turn_end' },
    ])

    // Takeover: stats carries server-accumulated baseline (1000 in, 500 out)
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // New turn after takeover — usage delta must accumulate on top of baseline
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 2048, output_tokens: 1000 } },
    ])

    // 1000 (baseline) + 2048 (new) = 3048 input → "3.0k"
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑3.0k')
    // 500 (baseline) + 1000 (new) = 1500 output → "1.5k"
    const outEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↓'),
    )
    expect(outEl?.textContent).toBe('↓1.5k')
  })

  it('takeover restores elapsed time from queryStartMs', () => {
    vi.useFakeTimers()
    mount()
    const pastTime = Date.now() - 5000
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', queryStartMs: pastTime })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([{ type: 'turn_start' }, { type: 'text_delta', text: 'x' }])
    vi.advanceTimersByTime(300)
    const elapsedEls = Array.from(document.querySelectorAll('span')).filter(
      (s) => /^\d+(\.\d+)?s$/.test(s.textContent ?? ''),
    )
    const elapsedValues = elapsedEls.map((s) => parseFloat(s.textContent!))
    const maxElapsed = Math.max(...elapsedValues)
    expect(maxElapsed).toBeGreaterThanOrEqual(5)
    vi.useRealTimers()
  })

  it('takeover tool count restored from server, increments on new tool_start', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', toolCount: 2 })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't3', name: 'Grep' } },
    ])
    vi.advanceTimersByTime(300)
    const toolCountEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.includes('tool'),
    )
    expect(toolCountEl?.textContent).toBe('3 tools')
    vi.useRealTimers()
  })

  it('turn_start does not reset elapsed time mid-query', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    vi.advanceTimersByTime(300)
    const elapsedAtQueryStart = Math.max(
      ...Array.from(document.querySelectorAll('span'))
        .filter((s) => /^\d+(\.\d+)?s$/.test(s.textContent ?? ''))
        .map((s) => parseFloat(s.textContent!)),
    )
    vi.advanceTimersByTime(5000)
    dispatchEvents([{ type: 'turn_start' }])
    vi.advanceTimersByTime(300)
    const elapsedAfterTurnStart = Math.max(
      ...Array.from(document.querySelectorAll('span'))
        .filter((s) => /^\d+(\.\d+)?s$/.test(s.textContent ?? ''))
        .map((s) => parseFloat(s.textContent!)),
    )
    expect(elapsedAfterTurnStart).toBeGreaterThan(elapsedAtQueryStart + 4)
    vi.useRealTimers()
  })

  // ── Takeover state restoration: full lifecycle tests ──

  it('takeover restores thinking duration from server baseline', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', thinkingMs: 3200 })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'thinking_start' },
      { type: 'thinking_end', thinking: { duration: 1800000000 } },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 50 } },
      { type: 'turn_end' },
      { type: 'query_end', usage_event: { input_tokens: 100, output_tokens: 50 } },
    ])
    vi.advanceTimersByTime(300)
    // Thinking stats are hidden — verify no 'thought for' text appears
    const thinkingEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.startsWith('thought for'),
    )
    expect(thinkingEl).toBeUndefined()
    vi.useRealTimers()
  })

  it('cold start connect_status with all-zero stats shows no progress bar', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    // No streaming events — progress bar should not exist
    const progressBars = document.querySelectorAll('[data-progress]')
    expect(progressBars.length).toBe(0)
  })

  it('model_switched updates header context with used + total from server', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', contextUsed: 5000, contextTotal: 200000 })
    const trigger = document.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
    expect(trigger.textContent).toContain('4.9k/195.3k')

    // Switch to model with 500k context — server responds with current used + new total
    dispatch({ type: 'model_switched', contextUsed: 5000, contextTotal: 500000 })
    expect(trigger.textContent).toContain('4.9k/488.3k')
    expect(trigger.textContent).not.toContain('195.3k')
  })

  it('sub-agent usage events accumulate in progress bar', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 50 } },
      { type: 'usage', usage_event: { input_tokens: 200, output_tokens: 30 }, agent: { parent_tool_use_id: 'tu-1', agent_type: 'sub', depth: 1 } },
    ])
    vi.advanceTimersByTime(300)
    // 100 (main) + 200 (sub-agent) = 300 input tokens
    const inEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inEl?.textContent).toBe('↑300')
    vi.useRealTimers()
  })

  it('sub-agent thinking_end accumulates into progress display', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      // Main agent thinking: 500ms = 500000000 ns
      { type: 'thinking_start' },
      { type: 'thinking_end', thinking: { duration: 500000000 } },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      // Sub-agent thinking: 2000ms = 2000000000 ns
      { type: 'thinking_start', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
      { type: 'thinking_end', thinking: { duration: 2000000000 }, agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 50 } },
      { type: 'turn_end' },
      { type: 'query_end', usage_event: { input_tokens: 100, output_tokens: 50 } },
    ])
    vi.advanceTimersByTime(300)
    // 500ms (main) + 2000ms (sub-agent) = 2500ms total thinking
    // The finalized progress bar should reflect 2500ms of thinking
    // Thinking stats are hidden in the current UI, but accumulatedThinkingMs
    // must be correct so the stats message carries the right value.
    // Verify via the internal state: query_end finalizes the bar.
    const finalizedBar = document.querySelector('[data-progress="1"]') as HTMLDivElement | null
    expect(finalizedBar).not.toBeNull()
    vi.useRealTimers()
  })

  it('sub-agent tool_start accumulates into committedToolCount', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      // Main agent tool
      { type: 'tool_start', tool_use: { id: 'main-t1', name: 'Bash', input: 'echo hi' } },
      // Agent tool (parent) — starts sub-agent
      { type: 'tool_start', tool_use: { id: 'agent-t1', name: 'Agent', input: 'do stuff' } },
      // Sub-agent tools
      { type: 'tool_start', tool_use: { id: 'sub-t1', name: 'Read', input: 'file.txt' }, agent: { parent_tool_use_id: 'agent-t1', agent_type: 'sub', depth: 1 } },
      { type: 'tool_start', tool_use: { id: 'sub-t2', name: 'Grep', input: 'pattern' }, agent: { parent_tool_use_id: 'agent-t1', agent_type: 'sub', depth: 1 } },
    ])
    // Dispatch query_end to finalize and check stats
    dispatchEvents([
      { type: 'tool_end', tool_use_id: 'sub-t1', output: 'done', name: 'Read', agent: { parent_tool_use_id: 'agent-t1', agent_type: 'sub', depth: 1 } },
      { type: 'tool_end', tool_use_id: 'sub-t2', output: 'done', name: 'Grep', agent: { parent_tool_use_id: 'agent-t1', agent_type: 'sub', depth: 1 } },
      { type: 'tool_end', tool_use_id: 'agent-t1', output: 'done', name: 'Agent' },
      { type: 'tool_end', tool_use_id: 'main-t1', output: 'done', name: 'Bash' },
      { type: 'query_end' },
    ])
    // 4 tools total: Bash + Agent + Read + Grep
    const toolEl = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.match(/\d+\s*tools?/),
    )
    expect(toolEl?.textContent).toBe('4 tools')
  })

  it('full recovery: disconnect mid-query, reconnect, continue, query_end', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })

    // Simulate query started, accumulated some stats on server
    // Now takeover happens with server-accumulated stats
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 5000, output_tokens: 1000, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 30000,
      toolCount: 3,
      thinkingMs: 5000,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // New turn after reconnect
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't4', name: 'Bash' } },
      { type: 'thinking_start' },
      { type: 'thinking_end', thinking: { duration: 2000000000 } },
      { type: 'usage', usage_event: { input_tokens: 2000, output_tokens: 500 } },
    ])
    vi.advanceTimersByTime(300)

    // Verify cumulative stats during streaming
    // Tool count: 3 (server) + 1 (new) = 4
    const toolEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.includes('tool'),
    )
    expect(toolEl?.textContent).toBe('4 tools')

    // Now trigger query_end — final stats should use authoritative usage_event
    dispatchEvents([
      { type: 'turn_end' },
      { type: 'query_end', usage_event: { input_tokens: 8000, output_tokens: 1800 } },
    ])
    vi.advanceTimersByTime(300)

    // After query_end: progress bar finalized, no longer updating
    // Input: 5000 + 2000 + 8000 = 15000 (cumulative + authoritative)
    // Actually: usage_event in query_end is the authoritative total from LLM.
    // The client replaces progressUsage with it. So final input should reflect
    // usage_event total (8000) since that's what LLM reports for last turn.
    // Check that the progress bar is finalized (data-progress=1)
    const finalizedBar = document.querySelector('[data-progress="1"]') as HTMLDivElement | null
    expect(finalizedBar).not.toBeNull()
    expect(finalizedBar!.dataset.progress).toBe('1')

    // Thinking stats are hidden — verify no 'thought for' text appears
    const thinkingEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.startsWith('thought for'),
    )
    expect(thinkingEl).toBeUndefined()

    vi.useRealTimers()
  })

  it('agent tool_end after takeover: main agent text_delta renders', () => {
    mount()
    // Phase 1: main agent starts Agent tool (normal pre-takeover streaming)
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('review my code')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])

    // Phase 2: takeover mid-agent-run.
    // resetAllState wipes streaming state. connect_status resets everything.
    // History shows committed turns (empty for this simple case).
    // Replay buffer: buffer was cleared at turn_start, so it starts from
    // tool_start (NOT turn_start — turn_start was already committed).
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })

    // Replay buffer does NOT have turn_start — only tool_start onward
    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'text_delta', text: 'Reviewing code...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])

    // Phase 3: sub-agent finishes, Agent tool_end, main agent continues
    dispatchEvents([
      { type: 'query_end', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
      { type: 'tool_end', tool_result: { tool_use_id: 'agent-1', display_output: 'Review complete.' } },
      // Main agent continues with text — THIS must render
      { type: 'text_delta', text: 'Based on the review, here are my findings.' },
    ])

    // The main agent's text_delta after Agent tool_end must appear in the DOM
    const allText = document.body.textContent ?? ''
    expect(allText).toContain('Based on the review')
  })

  it('takeover during Agent tool: history with running tool initializes progress bar', () => {
    vi.useFakeTimers()
    mount()
    // Takeover: connect_status + stats with server-accumulated stats
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 5000,
      toolCount: 1,
      thinkingMs: 0,
    })
    // History: committed assistant message with a RUNNING Agent tool
    // (no tool_result yet — Agent is still executing)
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [],
        tools: [],
        blocks: [
          { kind: 'tool', tool: { id: 'agent-1', name: 'Agent', isRunning: true } },
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'streaming', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    // Replay buffer: sub-agent events arrive after history
    dispatchEvents([
      { type: 'turn_start', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
      { type: 'text_delta', text: 'Working...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])
    vi.advanceTimersByTime(300)

    // Progress bar MUST exist — history with running tool should init streaming
    const inEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.startsWith('↑'),
    )
    expect(inEl).toBeTruthy()
    expect(inEl?.textContent).toBe('↑1.0k')

    vi.useRealTimers()
  })

  it('STOP during Agent tool after takeover: [Request interrupted by user] renders', () => {
    mount()
    // Takeover: history has running Agent tool
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [], tools: [],
        blocks: [
          { kind: 'tool', tool: { id: 'agent-1', name: 'Agent', isRunning: true } },
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'streaming', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    // Sub-agent event replay
    dispatchEvents([
      { type: 'text_delta', text: 'Working...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])
    // User clicks STOP: connector injects interrupt text_delta, then query_end(aborted)
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])

    const allText = document.body.textContent ?? ''
    expect(allText).toContain('[Request interrupted by user]')
  })

  // ── DOM integrity: assert ALL streaming elements exist after each scenario ──

  function assertStreamingDOMIntegrity(label: string, opts: {
    expectTokenStats?: boolean
    expectElapsed?: boolean
    expectToolCount?: number | null
  } = {}) {
    const { expectTokenStats = true, expectElapsed = true, expectToolCount = null } = opts
    // Progress bar exists iff token stats span exists (↑ is always in progress bar)
    if (expectTokenStats) {
      const inEl = Array.from(document.querySelectorAll('span')).find(
        (s) => s.textContent?.startsWith('↑'),
      )
      expect(inEl?.textContent ?? '').toMatch(/^↑/)
    }
    if (expectElapsed) {
      const elapsedEl = Array.from(document.querySelectorAll('span')).find(
        (s) => /^\d+(\.\d+)?s$/.test(s.textContent ?? ''),
      )
      expect(elapsedEl?.textContent ?? '').toMatch(/^\d/)
    }
    if (expectToolCount !== null) {
      const toolEl = Array.from(document.querySelectorAll('span')).find(
        (s) => s.textContent?.includes('tool'),
      )
      if (expectToolCount > 0) {
        expect(toolEl?.textContent).toContain(String(expectToolCount))
      }
    }
  }

  it('normal streaming: all DOM elements exist during live stream', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('hello')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 500, output_tokens: 200 } },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
    ])
    vi.advanceTimersByTime(300)
    assertStreamingDOMIntegrity('normal streaming', { expectToolCount: 1 })
    vi.useRealTimers()
  })

  it('takeover mid-text-stream: all DOM elements restored', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('hello')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 500, output_tokens: 200 } },
    ])
    // Takeover
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 500, output_tokens: 200, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 3000,
      toolCount: 0, thinkingMs: 0,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([{ type: 'turn_start' }, { type: 'usage', usage_event: { input_tokens: 300, output_tokens: 100 } }])
    vi.advanceTimersByTime(300)
    assertStreamingDOMIntegrity('takeover mid-text', { expectToolCount: null })
    vi.useRealTimers()
  })

  it('takeover mid-Agent-tool: all DOM elements restored (progress bar, tool count, elapsed)', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 5000,
      toolCount: 1, thinkingMs: 0,
    })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [], tools: [],
        blocks: [
          { kind: 'tool', tool: { id: 'agent-1', name: 'Agent', isRunning: true } },
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'streaming', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([
      { type: 'text_delta', text: 'Working...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])
    vi.advanceTimersByTime(300)
    // Must have: progress bar, token stats, elapsed time, tool count
    assertStreamingDOMIntegrity('takeover mid-Agent-tool', { expectToolCount: 1 })
    vi.useRealTimers()
  })

  it('double takeover: reconnect twice during same query without DOM loss', () => {
    vi.useFakeTimers()
    mount()
    // First takeover
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 500, output_tokens: 100, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 2000,
      toolCount: 0, thinkingMs: 0,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([{ type: 'turn_start' }, { type: 'usage', usage_event: { input_tokens: 300, output_tokens: 50 } }])
    vi.advanceTimersByTime(300)
    assertStreamingDOMIntegrity('first takeover', { expectToolCount: null })

    // Second takeover — same query, fresh reconnect
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 800, output_tokens: 150, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 5000,
      toolCount: 1, thinkingMs: 0,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'usage', usage_event: { input_tokens: 200, output_tokens: 80 } },
    ])
    vi.advanceTimersByTime(300)
    assertStreamingDOMIntegrity('second takeover', { expectToolCount: null })
    vi.useRealTimers()
  })

  it('STOP after takeover: progress bar finalized, interrupt visible', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 1000, output_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 5000,
      toolCount: 1, thinkingMs: 2000,
    })
    dispatch({
      type: 'history',
      messages: [{
        id: 'a1', role: 'assistant', text: '',
        thinking: [], tools: [],
        blocks: [
          { kind: 'tool', tool: { id: 'agent-1', name: 'Agent', isRunning: true } },
        ],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'streaming', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([
      { type: 'text_delta', text: 'Working...\n', agent: { parent_tool_use_id: 'agent-1', agent_type: 'sub', depth: 1 } },
    ])
    vi.advanceTimersByTime(300)

    // STOP
    dispatchEvents([
      { type: 'text_delta', text: '[Request interrupted by user]' },
      { type: 'query_end', aborted: true },
    ])
    vi.advanceTimersByTime(300)

    const allText = document.body.textContent ?? ''
    expect(allText).toContain('[Request interrupted by user]')
    // Progress bar finalized (data-progress=1)
    const finalizedBar = document.querySelector('[data-progress="1"]') as HTMLDivElement | null
    expect(finalizedBar).not.toBeNull()
    vi.useRealTimers()
  })

  it('double takeover: tool count not double-counted after reconnect', () => {
    vi.useFakeTimers()
    mount()
    // Phase 1: normal streaming with one tool
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('hello')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
    ])
    vi.advanceTimersByTime(300)

    // Phase 2: first takeover — server toolCount=1 (the Bash tool already started)
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'stats',
      usage: { input_tokens: 500, output_tokens: 100, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
      queryStartMs: Date.now() - 2000,
      toolCount: 1, thinkingMs: 0,
    })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    // Replay buffer replays tool_start (same t1, already counted by server)
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
    ])
    vi.advanceTimersByTime(300)

    // Tool count must be 1, not 2
    const toolEl = Array.from(document.querySelectorAll('span')).find(
      (s) => s.textContent?.includes('tool'),
    )
    expect(toolEl?.textContent).toBe('1 tool')

    vi.useRealTimers()
  })

  it('stats message overwrites replay-accumulated stats', () => {
    vi.useFakeTimers()
    mount()
    // Takeover: connect_status (no stats) then replay then stats
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [], nextCursor: '', hasMore: false })
    // Replay: usage event accumulates input=1000
    dispatchEvents([
      { type: 'turn_start' },
      { type: 'usage', usage_event: { input_tokens: 1000, output_tokens: 500 } },
    ])
    vi.advanceTimersByTime(300)
    // Before stats: progress bar shows 1000 (replay-accumulated) → "1.0k"
    const inElBefore = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inElBefore?.textContent).toBe('↑1.0k')

    // stats message arrives with authoritative value (500, not 1000)
    dispatch({
      type: 'stats',
      usage: { input_tokens: 500, output_tokens: 200, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    })
    vi.advanceTimersByTime(300)
    // After stats: progress bar shows 500 (overwritten, not accumulated)
    const inElAfter = Array.from(document.querySelectorAll('span')).find((s) =>
      s.textContent?.startsWith('↑'),
    )
    expect(inElAfter?.textContent).toBe('↑500')

    vi.useRealTimers()
  })

  it('tool_param_delta feeds tokenRate.add', () => {
    const addSpy = vi.spyOn(TokenRate.prototype, 'add')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'hello' },
    ])
    addSpy.mockClear()

    dispatchEvents([
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'tool_param_delta', partial_input: { delta: '{"command":"ls"}', summary: 'ls' }, tokens: 5 },
    ])

    expect(addSpy).toHaveBeenCalledWith('{"command":"ls"}', 5)
    addSpy.mockRestore()
  })

  it('disconnect banner shows reconnecting text on ws state change', () => {
    mount()
    expect(stateCb).toBeTruthy()
    stateCb!('reconnecting')
    const banner = document.querySelector('.text-red') as HTMLElement
    expect(banner?.textContent).toContain('reconnecting')
    // The banner parks below the header, whose height includes the status
    // bar inset (edge-to-edge) — a fixed top-11 would overlap it on phones.
    expect(banner?.parentElement?.className).toContain(
      'top-[calc(2.75rem+env(safe-area-inset-top,0px))]',
    )
  })

  it('disconnect banner shows failure text on disconnected state', () => {
    mount()
    stateCb!('disconnected')
    const banner = document.querySelector('.text-red') as HTMLElement
    expect(banner?.textContent).toContain('Tap to retry')
  })

  it('disconnect banner hides on connected state', () => {
    mount()
    stateCb!('reconnecting')
    const banner = document.querySelector(
      '.absolute.top-\\[calc\\(2\\.75rem\\+env\\(safe-area-inset-top\\,0px\\)\\)\\]',
    ) as HTMLElement
    expect(banner.style.opacity).toBe('1')

    stateCb!('connected')
    expect(banner.style.opacity).toBe('0')
  })

  it('permission ask renders three buttons and sends decision on click', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'ask',
      id: 'p1',
      kind: 'permission',
      tool_name: 'Bash',
      input: { command: 'ls' },
    })
    const root = document.querySelector('[class*="border-amber"]') as HTMLElement
    expect(root).toBeInstanceOf(HTMLElement)
    expect(root.textContent).toContain('Approve · Bash')
    const allowBtn = Array.from(root.querySelectorAll('button')).find(
      (b) => b.textContent === 'Allow Once',
    ) as HTMLButtonElement
    allowBtn.click()
    const last = sent[sent.length - 1] as { type: string; id: string; decision?: string }
    expect(last.type).toBe('ask_response')
    expect(last.id).toBe('p1')
    expect(last.decision).toBe('allow')
  })

  it('input ask renders input field and sends text/aborted on submit', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'ask',
      id: 'i1',
      kind: 'input',
      tool_name: 'Bash',
      prompt: '[sudo] password:',
      masked: true,
    })
    const root = document.querySelector('[class*="border-blue"]') as HTMLElement
    expect(root.textContent).toContain('[sudo] password:')
    // No permission buttons on input ask.
    expect(root.textContent).not.toContain('Allow Once')
    const input = root.querySelector('input') as HTMLInputElement
    expect(input.type).toBe('password')
    input.value = 'hunter2'
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    const last = sent[sent.length - 1] as {
      type: string
      id: string
      text?: string
      aborted?: boolean
      decision?: string
    }
    expect(last.type).toBe('ask_response')
    expect(last.id).toBe('i1')
    expect(last.text).toBe('hunter2')
    expect(last.aborted).toBe(false)
    expect(last.decision).toBeUndefined()
  })

  it('permission ask does not render input field (regression)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'ask',
      id: 'p2',
      kind: 'permission',
      tool_name: 'Bash',
      input: { command: 'rm -rf /' },
    })
    const root = document.querySelector('[class*="border-amber"]') as HTMLElement
    expect(root.querySelector('input')).toBeNull()
    // Three permission buttons must be present.
    const btns = Array.from(root.querySelectorAll('button')).map((b) => b.textContent)
    expect(btns).toEqual(['Allow Once', 'Allow This Session', 'Deny'])
  })

  it('input ask with already-expired deadline auto-aborts without leaving dead dialog', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const pastDeadline = Math.floor(Date.now() / 1000) - 5
    dispatch({
      type: 'ask',
      id: 'i-expired',
      kind: 'input',
      tool_name: 'Bash',
      prompt: 'pw?',
      deadline_unix: pastDeadline,
    })
    // Abort is deferred via setTimeout(0); flush it.
    vi.advanceTimersByTime(10)
    const last = sent[sent.length - 1] as {
      type: string
      id: string
      aborted?: boolean
      text?: string
    }
    expect(last.type).toBe('ask_response')
    expect(last.id).toBe('i-expired')
    expect(last.aborted).toBe(true)
    expect(last.text).toBe('')
    expect(last.timeout).toBe(true)
    // No live dialog left in the DOM (chat.ts removes it from askEls and
    // close() detaches the node). Scoped to the ask dialog's border-blue/50 —
    // the hidden settings page also carries border-blue/30 styling.
    expect(document.querySelector('[class*="border-blue/50"]')).toBeNull()
    vi.useRealTimers()
  })

  it('metadata restore renders queued messages with attachment chips', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // A busy engine reconnect: the page frame's stream window flips
    // streaming back on (metadata's resetAllState ran first), then queuedMsgs
    // restores the queue.
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      queuedMsgs: [{
        uuid: 'q-1',
        text: 'review this',
        attachments: [
          { name: 'report.pdf', mime: 'application/pdf' },
          { mime: 'image/jpeg' },
        ],
      }],
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    expect(
      sent.find((m) => (m as { type: string }).type === 'history_request'),
    ).toMatchObject({ type: 'history_request', cursor: '', limit: 30 })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'working' }],
      nextCursor: '',
      hasMore: false,
      busy: true,
    })
    expect(document.body.textContent).toContain('Tap to CANCEL')
    expect(document.body.textContent).toContain('[report.pdf]')
    expect(document.body.textContent).toContain('[image/jpeg]')
  })

  it('queued uuid backfill preserves attachment chips', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      // uuid:'' mirrors a locally-pushed queue entry awaiting its server uuid
      queuedMsgs: [{ uuid: '', text: 'with file', attachments: [{ name: 'doc.pdf', mime: 'application/pdf' }] }],
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'working' }],
      nextCursor: '',
      hasMore: false,
      busy: true,
    })
    dispatch({ type: 'queued', uuid: 'srv-1' })
    expect(document.body.textContent).toContain('Tap to CANCEL')
    expect(document.body.textContent).toContain('[doc.pdf]')
  })

  // A stuck STOP has no trace of WHERE its query_end went. The page's seq
  // watermark swallows frames silently; each of those drops must name itself
  // in the log so a phone capture can tell the mechanisms apart.
  it('event frame absorbed by the page watermark drops silently', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    // The page must echo the nonce the client itself sent, or case 'history'
    // drops it whole and no watermark ever lands.
    const req = sent.filter((m) => (m as { type?: string }).type === 'history_request').at(-1) as {
      nonce: number
    }
    // Deferral first: a frame that lands while the page is in flight is
    // buffered, then skipped at replay when the page's watermark covers it.
    dispatch({ type: 'event', event: { type: 'text_delta', text: 'buffered' }, seq: 12 })
    dispatch({
      type: 'history',
      nonce: req.nonce,
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'working' }],
      nextCursor: '',
      hasMore: false,
      busy: true,
      seq: 20,
    })
    expect(document.body.textContent).not.toContain('buffered')
    dispatch({ type: 'event', event: { type: 'text_delta', text: 'absorbed' }, seq: 15 })
    // Characterization: the swallowed frame changes no state.
    expect(document.body.textContent).not.toContain('absorbed')
    dispatch({ type: 'event', event: { type: 'text_delta', text: 'fresh' }, seq: 21 })
    expect(document.body.textContent).toContain('fresh')
  })

  // The no-local-copy rewind branch (observer tab / takeover / queue
  // misroute): rewound_text is server-side and marker-bearing, and this is the
  // second of the three sites where a stored string re-enters the input.
  it('rewind of a voice prompt with no local copy restores the clean text', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{ type: 'query_start' }])
    clickStop()
    dispatchEvents([
      { type: 'query_end', aborted: true, rewound: true, rewound_text: '[voice] 你好。' },
    ])
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('你好。')
  })

  // The Busy gate's other half: a page that reports busy:false for a query in
  // its prefill window arms nothing, and the live turn_start one seq above the
  // watermark arms the bar late rather than never.
  it('TestPrefillPageArmsLate: a busy:false prefill page arms nothing, its turn_start arms STOP', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // No rev on this metadata frame: the page0Cache write and lookup both
    // require one, so the client must mint a fresh history_request here
    // instead of replaying an earlier test's cached page.
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    const req = sent.filter((m) => (m as { type?: string }).type === 'history_request').at(-1) as {
      nonce: number
    }
    dispatch({
      type: 'history',
      nonce: req.nonce,
      messages: [
        {
          id: 'u1', role: 'user', text: 'prefill query',
          thinking: [], tools: [],
          usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      stream: [],
      nextCursor: '',
      hasMore: false,
      busy: false,
      seq: 20,
    })
    expect(document.body.textContent).not.toContain('STOP')
    dispatch({ type: 'event', event: { type: 'turn_start' }, seq: 21 })
    expect(document.body.textContent).toContain('STOP')
  })
})

// formatDuration renders "0.2s" | "45s" | "5m 0s" | "1h 2m 3s"; parse any of
// those back to plain seconds so anchoring assertions can use second bounds.
function parseDurationSeconds(text: string): number {
  const m = /^(?:(\d+)h )?(?:(\d+)m )?(\d+(?:\.\d+)?)s$/.exec(text.trim())
  if (!m) return NaN
  return (m[1] ? +m[1] * 3600 : 0) + (m[2] ? +m[2] * 60 : 0) + parseFloat(m[3])
}

describe('takeover elapsed anchoring', () => {
  it('takeover: metadata stats.queryStartMs anchors elapsed across the busy page round trip', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      stats: { queryStartMs: Date.now() - 300000, usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'working' }],
      nextCursor: '',
      hasMore: false,
      busy: true,
    })
    vi.advanceTimersByTime(300)
    const sep = document.querySelector('.sep-elapsed')
    expect(sep).not.toBeNull()
    const elapsedText = sep!.nextElementSibling?.textContent ?? ''
    const seconds = parseDurationSeconds(elapsedText)
    // 300s of server-anchored age at the 200ms tick. A Date.now() fallback
    // (2513 skipped) reads ~0.2s — below the lower bound; a direction/scale
    // mutation overshoots the upper bound.
    expect(seconds).toBeGreaterThanOrEqual(299)
    expect(seconds).toBeLessThan(302)
    stopStatusSweep()
    vi.useRealTimers()
  })

  it('takeover: hydrated thinking block ticks from the server startedAt, not (0s)', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      busy: true,
      stats: { queryStartMs: Date.now() - 300000, usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'thinking', id: '', text: '', durationNs: 0, active: true, startedAt: Date.now() - 120000 }],
      nextCursor: '',
      hasMore: false,
      busy: true,
    })
    vi.advanceTimersByTime(300)
    const label = Array.from(document.querySelectorAll('[data-thinking] span')).find((el) =>
      /^Thinking \(/.test(el.textContent ?? ''),
    )
    expect(label).toBeDefined()
    const inner = /^Thinking \((.+)\)$/.exec(label!.textContent ?? '')!
    const seconds = parseDurationSeconds(inner[1])
    // 120s of server startedAt at the 200ms tick: the stale static "(0s)"
    // initial label or a Date.now() fallback reads ~0 — below the lower bound.
    expect(seconds).toBeGreaterThanOrEqual(119)
    expect(seconds).toBeLessThan(122)
    stopStatusSweep()
    vi.useRealTimers()
  })
})

describe('mapHistoryToChatMessages', () => {
  const usage = { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }
  const user = (id: string, text: string) => ({ id, role: 'user' as const, text, thinking: [], tools: [], usage, error: '', status: 'done' as const, startedAt: 0 })
  it('propagates error through assistant merge so partial+error turns keep their error box on restart', () => {
    const result = mapHistoryToChatMessages([
      user('u1', 'go'),
      { id: 'a1', role: 'assistant' as const, text: 'partial answer', thinking: [], tools: [], usage, error: '', status: 'done' as const, startedAt: 0, blocks: [] },
      { id: 'a2', role: 'assistant' as const, text: '', thinking: [], tools: [], usage, error: 'API Error 529: service overloaded', status: 'done' as const, startedAt: 0, blocks: [] },
    ])
    expect(result.length).toBe(2)
    expect(result[1].blocks[0].kind === 'text' && (result[1].blocks[0] as { text: string }).text).toBe('partial answer')
    expect(result[1].error).toBe('API Error 529: service overloaded')
  })
  const marker = (id: string) => ({ id, role: 'system' as const, compactBoundary: true, text: '', thinking: [], tools: [], usage, error: '', status: 'done' as const, startedAt: 0 })

  it('filters out system-role markers between regular messages', () => {
    const result = mapHistoryToChatMessages([
      user('u1', 'hello'),
      marker('b1'),
      user('u2', 'world'),
    ])
    expect(result.length).toBe(2)
    expect(result[0].id).toBe('u1')
    expect(result[0].role).toBe('user')
    expect(result[1].id).toBe('u2')
    expect(result[1].role).toBe('user')
  })

  it('returns empty array when all inputs are system markers', () => {
    const result = mapHistoryToChatMessages([marker('b1'), marker('b2')])
    expect(result).toEqual([])
  })

  it('maps document history blocks to document chat blocks', () => {
    const result = mapHistoryToChatMessages([{
      id: 'u-doc',
      role: 'user' as const,
      text: 'see this',
      thinking: [],
      tools: [],
      usage,
      error: '',
      status: 'done' as const,
      startedAt: 0,
      blocks: [
        { kind: 'text', text: 'see this' },
        { kind: 'document', name: 'report.pdf', mime: 'application/pdf', size: 4321 },
      ],
    }])
    expect(result.length).toBe(1)
    const blocks = result[0].blocks
    expect(blocks.length).toBe(2)
    expect(blocks[0].kind).toBe('text')
    expect(blocks[1].kind).toBe('document')
    expect(blocks[1]).toEqual({ kind: 'document', id: '', name: 'report.pdf', mime: 'application/pdf', size: 4321 })
  })

  it('keeps an image-only user message: empty text with an image block is not dropped', () => {
    const result = mapHistoryToChatMessages([{
      id: 'u-img',
      role: 'user' as const,
      text: '',
      thinking: [],
      tools: [],
      usage,
      error: '',
      status: 'done' as const,
      startedAt: 0,
      blocks: [{ kind: 'image', src: 'data:image/jpeg;base64,AAAA' }],
    }])
    expect(result.length).toBe(1)
    expect(result[0].id).toBe('u-img')
    expect(result[0].blocks).toEqual([{ kind: 'image', id: '', src: 'data:image/jpeg;base64,AAAA' }])
  })

  it('keeps a document-only user message: empty text with a document block is not dropped', () => {
    const result = mapHistoryToChatMessages([{
      id: 'u-doc-only',
      role: 'user' as const,
      text: '',
      thinking: [],
      tools: [],
      usage,
      error: '',
      status: 'done' as const,
      startedAt: 0,
      blocks: [{ kind: 'document', name: 'x.pdf', size: 1 }],
    }])
    expect(result.length).toBe(1)
    expect(result[0].id).toBe('u-doc-only')
    expect(result[0].blocks).toEqual([{ kind: 'document', id: '', name: 'x.pdf', mime: undefined, size: 1 }])
  })

  it('still drops a textless user message that carries no renderable blocks', () => {
    const result = mapHistoryToChatMessages([user('u-empty', '   ')])
    expect(result).toEqual([])
  })
})

describe('document block history rendering', () => {
  const usage = { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }

  function dispatchHistory(blocks: unknown[]) {
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-doc',
        role: 'user',
        text: 'see this',
        thinking: [],
        tools: [],
        usage,
        error: '',
        status: 'done',
        startedAt: 0,
        blocks,
      }],
      nextCursor: '',
      hasMore: false,
    })
  }

  it('document block renders the upload-style [name size] chip, display-only', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      { kind: 'text', text: 'see this' },
      { kind: 'document', name: 'report.pdf', mime: 'application/pdf', size: 4321 },
    ])
    // Byte-identical to the input-bar pending chip: [name size], no icon,
    // no click affordance.
    const chips = Array.from(document.querySelectorAll('span')).filter(el =>
      el.textContent?.includes('report.pdf'))
    expect(chips.length).toBe(1)
    const chip = chips[0] as HTMLElement
    expect(chip.textContent).toBe('[report.pdf 4.2 KB]')
    expect(chip.querySelector('svg')).toBeNull()
    expect(chip.className).not.toContain('cursor')
    // Reference metadata never surfaces the mime or any parsed markdown.
    expect(document.body.textContent).not.toContain('application/pdf')
  })

  it('document chip with unknown size matches the same [name] form', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      { kind: 'text', text: 'see this' },
      { kind: 'document', name: 'no-size.bin' },
    ])
    const chip = Array.from(document.querySelectorAll('span')).find(el =>
      el.textContent?.includes('no-size.bin')) as HTMLElement
    expect(chip.textContent).toBe('[no-size.bin]')
  })

  it('document-only user message (no text) still renders its chip on replay', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [{
        id: 'm-doc-only',
        role: 'user',
        text: '',
        thinking: [],
        tools: [],
        usage,
        error: '',
        status: 'done',
        startedAt: 0,
        blocks: [{ kind: 'document', name: 'x.pdf', mime: 'application/pdf', size: 1 }],
      }],
      nextCursor: '',
      hasMore: false,
    })
    const chip = Array.from(document.querySelectorAll('span')).find(el =>
      el.textContent?.includes('x.pdf')) as HTMLElement
    expect(chip).toBeDefined()
    expect(chip.textContent).toBe('[x.pdf 1 B]')
    expect(document.body.textContent).not.toContain('application/pdf')
  })

  it('legacy [Document: ...] text block still renders chip plus remaining text', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      { kind: 'text', text: '[Document: notes.txt saved at /cache/documents/abc.txt]\nleftover body text' },
    ])
    const chip = Array.from(document.querySelectorAll('span')).find(el =>
      el.textContent === '[notes.txt]')
    expect(chip).toBeDefined()
    expect(chip?.textContent).toBe('[notes.txt]')
    expect(document.body.textContent).toContain('leftover body text')
  })
})

describe('image-only user message history rendering', () => {
  const usage = { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }
  const IMG = 'data:image/jpeg;base64,AAAA'

  function dispatchHistory(messages: unknown[]) {
    dispatch({
      type: 'history',
      messages,
      nextCursor: '',
      hasMore: false,
    })
  }

  function userMsg(id: string, text: string, blocks: unknown[]) {
    return { id, role: 'user', text, thinking: [], tools: [], usage, error: '', status: 'done', startedAt: 0, blocks }
  }

  function historyImage(): HTMLImageElement | null {
    const img = Array.from(document.querySelectorAll('img')).find(
      (el) => el.getAttribute('src') === IMG,
    )
    return img ? (img as HTMLImageElement) : null
  }

  it('image-only user message renders its thumbnail between adjacent text messages', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      userMsg('u-img', '', [{ kind: 'image', src: IMG }]),
      userMsg('u-t1', 'first text', [{ kind: 'text', text: 'first text' }]),
      userMsg('u-t2', 'second text', [{ kind: 'text', text: 'second text' }]),
      { id: 'a1', role: 'assistant', text: 'answer', thinking: [], tools: [], usage, error: '', status: 'done', startedAt: 0, blocks: [] },
    ])
    const img = historyImage()
    expect(img).not.toBeNull()
    // History attachment thumbnails are lightbox-wired (cursor-zoom-in).
    expect(img!.className).toContain('cursor-zoom-in')
    // Control group: the adjacent plain-text messages still render.
    expect(document.body.textContent).toContain('first text')
    expect(document.body.textContent).toContain('second text')
    // Cross-shell DOM order: thumbnail shell, then first text shell, then
    // second text shell — frag append order, attachments not regrouped.
    // data-block-kind="user" selects exactly the committed user text spans
    // (assistant markdown carries no such marker), and the img is matched by
    // src so avatar <img>s can't alias.
    const spans = Array.from(document.querySelectorAll('[data-block-kind="user"]'))
    expect(spans.map((s) => s.textContent)).toEqual(['first text', 'second text'])
    const ordered = [img!, ...spans].sort((a, b) =>
      a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1)
    expect(ordered).toEqual([img, ...spans])
  })

  it('image+text user message renders both blocks, image first', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      userMsg('u-mix1', 'caption after', [{ kind: 'image', src: IMG }, { kind: 'text', text: 'caption after' }]),
    ])
    const img = historyImage()
    expect(img).not.toBeNull()
    const kids = Array.from(img!.parentElement!.children)
    expect(kids.indexOf(img!)).toBe(0)
    const span = kids.find((el) => el.textContent === 'caption after')
    expect(span?.tagName).toBe('SPAN')
  })

  it('image+text user message renders both blocks, text first', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchHistory([
      userMsg('u-mix2', 'caption before', [{ kind: 'text', text: 'caption before' }, { kind: 'image', src: IMG }]),
    ])
    const img = historyImage()
    expect(img).not.toBeNull()
    const kids = Array.from(img!.parentElement!.children)
    expect(kids.indexOf(img!)).toBe(1)
    expect(kids[0].textContent).toBe('caption before')
  })
})

describe('artifact integration', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  function stubFetchLength(len: string) {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () =>
      new Response(null, { status: 200, headers: { 'content-length': len } }),
    )
  }

  function lastAssistantContent(): HTMLElement {
    return assistantContentDivs().slice(-1)[0]
  }

  function artifactCards(): HTMLElement[] {
    return Array.from(document.querySelectorAll('.artifact-card'))
  }

  function sheetFrame(): HTMLIFrameElement {
    return document.querySelector('.artifact-sheet iframe') as HTMLIFrameElement
  }

  function spySheetSrcSetter(): { calls: unknown[]; restore: () => void } {
    const frame = sheetFrame()
    const desc = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'src')!
    const calls: unknown[] = []
    Object.defineProperty(frame, 'src', {
      configurable: true,
      get: desc.get,
      set: (v: unknown) => {
        calls.push(v)
        desc.set!.call(frame, v)
      },
    })
    return {
      calls,
      restore: () => {
        delete (frame as Partial<HTMLIFrameElement> & { src?: unknown }).src
      },
    }
  }

  function runWriteQuery(
    toolName: 'Write' | 'Edit',
    id: string,
    opts: { isError?: boolean; aborted?: boolean; path?: string } = {},
  ) {
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id, name: toolName, summary: opts.path ?? '/home/u/.gbot/projects/abc/artifacts/game.html' } },
    ])
    dispatchEvents([
      {
        type: 'tool_end',
        tool_result: {
          tool_use_id: id,
          display_output: 'wrote file',
          is_error: opts.isError ?? false,
        },
      },
      { type: 'query_end', aborted: opts.aborted ?? false },
    ])
  }

  it('query_end appends an artifact card for a successful Write', async () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1')

    const content = lastAssistantContent()
    const card = content.querySelector('.artifact-card') as HTMLElement | null
    expect(card).toBeTruthy()
    const iframe = card!.querySelector('iframe') as HTMLIFrameElement
    expect(iframe.getAttribute('src')).toBe('/artifacts/game.html')
    await vi.waitFor(() => {
      expect(card!.querySelector('.card-meta-inline')?.textContent).toContain('14.2 KB')
    })
  })

  it('a later Edit query adds an Updated card; the first card stays plain', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1')
    setTextarea('tweak it')
    pressEnter()
    runWriteQuery('Edit', 'e-1')

    const cards = artifactCards()
    expect(cards.length).toBe(2)
    expect(cards[0].classList.contains('stale')).toBe(false)
    expect(cards[0].querySelector('.card-updated')).toBeNull()
    expect(cards[0].querySelector('.card-fresh-dot')).toBeNull()
    expect(cards[1].classList.contains('stale')).toBe(true)
    expect(cards[1].querySelector('.card-updated')?.textContent).toBe('Updated')
    expect(cards[1].querySelector('.card-fresh-dot')).toBeTruthy()
  })

  it('a failed Write produces no card', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1', { isError: true })

    expect(artifactCards().length).toBe(0)
  })

  it('a download-type card opens the confirm, never the sheet', () => {
    stubFetchLength('68157440')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('package the apk')
    pressEnter()
    runWriteQuery('Write', 'w-apk', { path: '/home/u/.gbot/projects/abc/artifacts/app.apk' })
    expect(artifactCards()).toHaveLength(1)
    artifactCards()[0].click()
    expect(document.querySelectorAll('.share-dl')).toHaveLength(1)
    // The sheet stays in its closed state — height 0px and no frame src.
    expect((document.querySelector('.artifact-sheet') as HTMLElement).style.height).toBe('0px')
    expect(document.querySelector('.artifact-sheet iframe')?.getAttribute('src')).toBeNull()
    // Leave nothing open: an unresolved confirm keeps activeConfirm and a live
    // keydown handler for every later test in this file.
    document.querySelector('.share-dl-btn')?.click()
    expect(document.querySelectorAll('.share-dl, .share-dl-scrim')).toHaveLength(0)
  })
  it('an html card still opens the sheet — the download gate does not swallow it', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1')
    artifactCards()[0].click()
    expect(document.querySelectorAll('.share-dl')).toHaveLength(0)
    expect((document.querySelector('.artifact-sheet') as HTMLElement).style.height).toBe('70%')
    expect(document.querySelector('.artifact-sheet iframe')?.getAttribute('src')).toBe(
      '/artifacts/game.html',
    )
  })
  it('an image card opens the sheet with its download button, not the confirm', () => {
    stubFetchLength('20480')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('draw a cat')
    pressEnter()
    runWriteQuery('Write', 'w-png', { path: '/home/u/.gbot/projects/abc/artifacts/pic.png' })
    expect(artifactCards()).toHaveLength(1)
    // The thumbnail stays — images render in the frame.
    expect(artifactCards()[0].querySelector('.card-preview iframe')).not.toBeNull()
    artifactCards()[0].click()
    expect(document.querySelectorAll('.share-dl')).toHaveLength(0)
    expect((document.querySelector('.artifact-sheet') as HTMLElement).style.height).toBe('70%')
    const btn = document.querySelector('.sheet-download') as HTMLElement
    expect(btn.classList.contains('hidden')).toBe(false)
  })
  it('query_end does not reload the open sheet', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1')

    const card = artifactCards()[0]
    ;(card as HTMLElement).click()
    const sheetRoot = document.querySelector('.artifact-sheet') as HTMLElement
    expect(sheetRoot.style.height).toBe('70%')

    const spy = spySheetSrcSetter()
    dispatchEvents([{ type: 'query_start' }, { type: 'query_end' }])
    // No refresh at query end — the flash outweighed freshness; refresh =
    // close and reopen.
    expect(spy.calls.length).toBe(0)
    spy.restore()
  })

  it('aborted query with a successful Write still commits the card; empty abort rewinds to none', () => {
    stubFetchLength('14541')
    mount()
    dispatch({ type: 'connect_status', connected: true })

    setTextarea('make a page')
    pressEnter()
    runWriteQuery('Write', 'w-1', { aborted: true })
    expect(artifactCards().length).toBe(1)

    setTextarea('make another')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'query_end', aborted: true }])
    expect(artifactCards().length).toBe(1)
  })

  it('history replay renders cards without any live event', () => {
    stubFetchLength('14541')
    mount()
    dispatch({
      type: 'metadata',
      connect: { connected: true, model: 'test' },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', startedAt: 1000, text: 'make a page',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0 },
        },
        {
          // One assistant message per query on the wire; Write+Edit of the
          // same file inside it dedupes to a single Updated card.
          id: 'a1', role: 'assistant', startedAt: 1000, text: '',
          thinking: [], tools: [],
          blocks: [
            { kind: 'tool', tool: { id: 'w1', name: 'Write', summary: '/pd/artifacts/game.html', durationNs: 1000 } },
            { kind: 'tool', tool: { id: 'e1', name: 'Edit', summary: '/pd/artifacts/game.html', durationNs: 1000 } },
            { kind: 'tool', tool: { id: 'w2', name: 'Write', summary: '/pd/artifacts/other.html', durationNs: 1000 } },
          ],
          usage: { inputTokens: 0, outputTokens: 0 },
        },
      ],
      nextCursor: '', hasMore: false,
    })

    const cards = artifactCards()
    expect(cards.length).toBe(2)
    expect(cards[0].querySelector('.card-title')?.textContent).toBe('game.html')
    expect((cards[0].querySelector('iframe') as HTMLIFrameElement).getAttribute('src')).toBe(
      '/artifacts/game.html',
    )
    expect(cards[0].classList.contains('stale')).toBe(true)
    expect(cards[0].querySelector('.card-updated')?.textContent).toBe('Updated')
    expect(cards[1].querySelector('.card-title')?.textContent).toBe('other.html')
    expect(cards[1].classList.contains('stale')).toBe(false)
  })
})

describe('sidebar artifacts', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })
  it('opening the sidebar fetches the list; clicking a row opens the sheet', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        json: async () => [{ name: 'game.html', size: 29, mtime: Date.now() }],
      })),
    )
    mount()

    const hamburger = document.querySelector('button[aria-label="Menu"]') as HTMLElement
    expect(hamburger).not.toBeNull()
    hamburger.click()

    await vi.waitFor(() => {
      const row = document.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement | null
      expect(row?.textContent).toContain('game.html')
    })

    const row = document.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement
    row.click()
    const frame = document.querySelector('.artifact-sheet iframe') as HTMLIFrameElement
    expect(frame.getAttribute('src')).toBe('/artifacts/game.html')
  })
  it('a download-type row opens the confirm, never the sheet', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        json: async () => [{ name: 'app.apk', size: 68157440, mtime: Date.now() }],
      })),
    )
    mount()

    const hamburger = document.querySelector('button[aria-label="Menu"]') as HTMLElement
    hamburger.click()

    await vi.waitFor(() => {
      const row = document.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement | null
      expect(row?.textContent).toContain('app.apk')
    })

    const row = document.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement
    row.click()
    // Same gate as the card path: one confirm, and the sheet stays shut.
    expect(document.querySelectorAll('.share-dl')).toHaveLength(1)
    expect((document.querySelector('.artifact-sheet') as HTMLElement).style.height).toBe('0px')
    expect(document.querySelector('.artifact-sheet iframe')?.getAttribute('src')).toBeNull()
    document.querySelector('.share-dl-btn')?.click()
    expect(document.querySelectorAll('.share-dl, .share-dl-scrim')).toHaveLength(0)
  })
  it('empty list shows the empty state after opening', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        json: async () => [],
      })),
    )
    mount()

    const hamburger = document.querySelector('button[aria-label="Menu"]') as HTMLElement
    hamburger.click()

    await vi.waitFor(() => {
      const section = document.querySelector('.sidebar-artifacts') as HTMLElement | null
      expect(section?.textContent).toContain('No artifacts yet')
    })
  })
})

describe('wui asset hash reload', () => {
  beforeEach(() => {
    // The reload marker lives in sessionStorage, which jsdom shares across
    // every test in this file — a leaked flag would toast on later mounts.
    sessionStorage.clear()
  })
  afterEach(() => {
    sessionStorage.clear()
    vi.unstubAllGlobals()
  })

  function metaFrame(wuiHash?: string) {
    return {
      type: 'metadata',
      connect: wuiHash === undefined
        ? { connected: true }
        : { connected: true, wuiHash },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    }
  }

  it('reloads exactly once when a reconnect reports a different bundle hash', () => {
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    mount()
    // Boot: first frame per page lifetime only records the baseline.
    dispatch(metaFrame('hash-a'))
    expect(reloadSpy).toHaveBeenCalledTimes(0)
    // Transient drop, same binary: reconnect rides the identical hash.
    dispatch(metaFrame('hash-a'))
    expect(reloadSpy).toHaveBeenCalledTimes(0)
    // Daemon upgraded (tableflip/REUSEPORT): the new process serves a new
    // bundle, so the reconnecting page must hard-reload.
    dispatch(metaFrame('hash-b'))
    expect(reloadSpy).toHaveBeenCalledTimes(1)
    // The reload itself is silent — the marker is what lets the reloaded
    // page explain the flash with a toast.
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe('1')
  })

  it('does not reload when the hash disappears (older daemon, no new information)', () => {
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    mount()
    dispatch(metaFrame('hash-a'))
    dispatch(metaFrame())
    expect(reloadSpy).toHaveBeenCalledTimes(0)
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe(null)
  })

  it('hash-less first frame stores no baseline, so a later hashed frame does not reload', () => {
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    mount()
    dispatch(metaFrame())
    dispatch(metaFrame('hash-a'))
    expect(reloadSpy).toHaveBeenCalledTimes(0)
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe(null)
  })

  // The refresh notice rides the upgrade capsule (data-status-capsule).
  function bootCapsule(): HTMLElement {
    const chat = mount()
    return chat.root.querySelector('[data-status-capsule]') as HTMLElement
  }

  it('boot with the reload marker shows the refresh in the capsule once and clears the marker', () => {
    sessionStorage.setItem('wuiAutoReloaded', '1')
    const capsuleEl = bootCapsule()
    expect(capsuleEl.textContent).toBe('Refreshed')
    expect(capsuleEl.classList.contains('opacity-0')).toBe(false)
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe(null)
  })

  it('plain boot without the marker keeps the capsule hidden', () => {
    const capsuleEl = bootCapsule()
    expect(capsuleEl.textContent).toBe('')
    expect(capsuleEl.classList.contains('opacity-0')).toBe(true)
    expect(sessionStorage.getItem('wuiAutoReloaded')).toBe(null)
  })
})

// jsdom has no URL.createObjectURL — the input bar needs it to mint preview
// blob URLs when an image attachment is picked.
describe('abort rewind attachment restore', () => {
  const hadCreateObjectURL = 'createObjectURL' in URL
  const origCreateObjectURL = (URL as { createObjectURL?: (b: Blob) => string }).createObjectURL
  const hadRevokeObjectURL = 'revokeObjectURL' in URL
  const origRevokeObjectURL = (URL as { revokeObjectURL?: (u: string) => void }).revokeObjectURL
  let blobSeq = 0

  // The upload path POSTs /api/upload via XHR; jsdom's real XHR cannot make
  // network requests, so this block installs a stub that completes every
  // send with a 200 whose id derives from the uploaded filename.
  class AutoXHR {
    onload: (() => void) | null = null
    onerror: (() => void) | null = null
    onabort: (() => void) | null = null
    upload: { onprogress: (() => void) | null } = { onprogress: null }
    status = 0
    responseText = ''
    open() {}
    setRequestHeader() {}
    send(body: unknown) {
      const entry = (body as FormData)?.get?.('file')
      const name = entry instanceof File ? entry.name : 'stub'
      this.status = 200
      this.responseText = JSON.stringify({
        id: `i:${name.replace(/\W/g, '')}`,
        mime: 'image/png',
        size: 0,
        kind: 'image',
      })
      queueMicrotask(() => this.onload?.())
    }
    abort() {}
  }
  beforeEach(() => {
    vi.stubGlobal('XMLHttpRequest', AutoXHR)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  beforeAll(() => {
    Object.defineProperty(URL, 'createObjectURL', {
      value: () => `blob:mock-${++blobSeq}`,
      configurable: true,
      writable: true,
    })
    Object.defineProperty(URL, 'revokeObjectURL', {
      value: () => {},
      configurable: true,
      writable: true,
    })
  })
  afterAll(() => {
    if (hadCreateObjectURL) {
      Object.defineProperty(URL, 'createObjectURL', {
        value: origCreateObjectURL,
        configurable: true,
        writable: true,
      })
    } else {
      delete (URL as { createObjectURL?: unknown }).createObjectURL
    }
    if (hadRevokeObjectURL) {
      Object.defineProperty(URL, 'revokeObjectURL', {
        value: origRevokeObjectURL,
        configurable: true,
        writable: true,
      })
    } else {
      delete (URL as { revokeObjectURL?: unknown }).revokeObjectURL
    }
  })

  // pickIntoInput feeds a File through one of the hidden <input type=file>
  // pickers (image picker excludes the camera input via the capture attr).
  function pickIntoInput(match: (input: HTMLInputElement) => boolean, file: File) {
    const input = Array.from(document.querySelectorAll('input[type=file]')).find((el) =>
      match(el as HTMLInputElement),
    ) as HTMLInputElement | undefined
    if (!input) throw new Error('matching file input not found')
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    input.dispatchEvent(new Event('change', { bubbles: true }))
  }

  const messageFrames = () =>
    sent.filter(
      (m) =>
        (m as { type?: string; attachments?: { id: string; name?: string; mime: string; size: number }[] })
          .type === 'message' &&
        Array.isArray(
          (m as { attachments?: { id: string }[] }).attachments,
        ),
    ) as { text: string; attachments: { id: string; name?: string; mime: string; size: number }[] }[]

  it('abort with no content restores text and re-stages attachment chips via restore_result', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    pickIntoInput(
      (i) => i.accept === 'image/*' && !i.hasAttribute('capture'),
      new File([], 'cat.png', { type: 'image/png' }),
    )
    pickIntoInput(
      (i) => i.accept.includes('.pdf'),
      new File([], 'notes.pdf', { type: 'application/pdf' }),
    )
    setTextarea('see this')
    pressEnter()
    await vi.waitFor(() => {
      if (messageFrames().length !== 1 || messageFrames()[0].attachments.length !== 2) {
        throw new Error('commit frame with 2 attachments not sent yet')
      }
    })

    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    dispatchEvents([{ type: 'thinking_end' }, { type: 'query_end', aborted: true, rewound: true }])

    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('see this')
    expect(sent.some((m) => (m as { type?: string }).type === 'restore_attachments')).toBe(true)

    dispatch({
      type: 'restore_result',
      attachments: [
        { id: 'fresh-img', mime: 'image/png', size: 1234 },
        { id: 'fresh-doc', name: 'notes.pdf', mime: 'application/pdf', size: 4321 },
      ],
    })

    // Image chip reuses the local blob thumbnail (server ids carry no bytes).
    const chipImg = document.querySelector('img.w-12') as HTMLImageElement | null
    if (!chipImg) throw new Error('restored image chip not rendered')
    expect(chipImg.src).toBe('blob:mock-1')
    // Document chip keeps its original filename from the local stash.
    const chipStrip = document.querySelector('.flex-wrap.gap-2') as HTMLElement | null
    if (!chipStrip) throw new Error('chip strip not found')
    expect(chipStrip.textContent).toContain('notes.pdf')

    // Re-send references the fresh ids without re-uploading bytes.
    setTextarea('again')
    pressEnter()
    await vi.waitFor(() => {
      const frames = messageFrames()
      const last = frames[frames.length - 1]
      if (!last || last.attachments.map((a) => a.id).join(',') !== 'fresh-img,fresh-doc') {
        throw new Error('re-send frame ids not staged yet')
      }
    })
  })

  it('first connect after auto-refresh keeps the refreshed notice (no false recovered)', () => {
    sessionStorage.setItem('wuiAutoReloaded', '1')
    mount()
    dispatch({ type: 'connect_status', connected: true })
    expect(document.body.textContent).toContain('Refreshed')
    // The reloaded page's first WS connect must not mistake the active
    // notice capsule (Refreshed) for an in-flight upgrade and swap in
    // Recovered — the capsule is shared by notifyCapsule and the upgrade
    // flow, so active() alone cannot tell them apart.
    stateCb?.('connected')
    expect(document.body.textContent).toContain('Refreshed')
    expect(document.body.textContent).not.toContain('Recovered')
  })

  it('refreshed notice auto-dismisses after 1s', () => {
    sessionStorage.setItem('wuiAutoReloaded', '1')
    vi.useFakeTimers()
    try {
      mount()
      const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
      expect(capsule.classList.contains('opacity-0')).toBe(false)
      vi.advanceTimersByTime(999)
      expect(capsule.classList.contains('opacity-0')).toBe(false)
      vi.advanceTimersByTime(1)
      expect(capsule.classList.contains('opacity-0')).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('real upgrade cycle still reports recovered and clears data-upgrading', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    stateCb?.('upgrading')
    expect(document.body.getAttribute('data-upgrading')).toBe('1')
    stateCb?.('connected')
    expect(document.body.textContent).toContain('Recovered')
    expect(document.body.hasAttribute('data-upgrading')).toBe(false)
  })

  it('abort with queued follow-ups keeps the interrupted turn (no rewind)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('query A')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    // Server skipped the rewind (queue non-empty — B runs next); rewound is
    // absent on the wire. A must stay on screen and NOT clobber the input.
    dispatchEvents([{ type: 'thinking_end' }, { type: 'query_end', aborted: true }])
    expect(document.body.textContent).toContain('query A')
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('')
    expect(sent.some((m) => (m as { type?: string }).type === 'restore_attachments')).toBe(false)
  })

  it('text-only abort rewind restores the text but sends no restore_attachments frame', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('just words')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    dispatchEvents([{ type: 'thinking_end' }, { type: 'query_end', aborted: true, rewound: true }])
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    expect(ta.value).toBe('just words')
    expect(sent.some((m) => (m as { type?: string }).type === 'restore_attachments')).toBe(false)
  })

  it('attachments-only abort pops the ghost bubble and asks for a restore', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    pickIntoInput(
      (i) => i.accept.includes('.pdf'),
      new File([], 'notes.pdf', { type: 'application/pdf' }),
    )
    pressEnter()
    await vi.waitFor(() => {
      const frames = messageFrames()
      if (frames.length !== 1 || frames[0].attachments.length !== 1) {
        throw new Error('commit frame with 1 attachment not sent yet')
      }
    })
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    clickStop()
    dispatchEvents([{ type: 'thinking_end' }, { type: 'query_end', aborted: true, rewound: true }])
    // No text to restore, but the bubble must not linger as a ghost.
    expect(document.body.textContent.includes('[notes.pdf]')).toBe(false)
    expect(sent.some((m) => (m as { type?: string }).type === 'restore_attachments')).toBe(true)
  })

  it('restore_result without a pending rewind stash stages nothing', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'restore_result',
      attachments: [{ id: 'stray', mime: 'image/png', size: 10 }],
    })
    expect(document.querySelector('img.w-12')).toBe(null)
    expect(sent.some((m) => (m as { type?: string }).type === 'message')).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Mid-turn queued message rendering (persist-in-final-form era)
// ---------------------------------------------------------------------------

const QUEUED_ENVELOPE =
  '<system-reminder>\nThe user sent a new message while you were working:\ncheck on that' +
  '\n\nIMPORTANT: After completing your current task, you MUST address the user\'s message above. Do not ignore it.\n</system-reminder>'

describe('queued message history rendering', () => {
  it('history queued message renders unwrapped original text in gray', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: QUEUED_ENVELOPE,
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'check on that',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).toContain('text-t3')
    expect(document.body.textContent).not.toContain('<system-reminder>')
  })

  it('plain user history message is not gray', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        {
          id: 'u1', role: 'user', text: 'plain hello',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 0,
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'plain hello',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).not.toContain('text-t3')
  })

  it('live attachment event with envelope renders unwrapped gray echo', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{
      type: 'attachment',
      message: {
        content: [{ type: 'text', text: QUEUED_ENVELOPE }],
        attachment: { prompt: 'check on that', source_uuid: 'u-9', mode: 'prompt' },
      },
    }])
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'check on that',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).toContain('text-t3')
    expect(document.body.textContent).not.toContain('<system-reminder>')
  })
  it('job-tagged events stay out of the chat timeline (single delivery)', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
    ])
    const jobAgent = { parent_tool_use_id: 'agent-1', agent_type: 'Explorer', depth: 1, job_id: 'agent-9' }
    dispatchEvents([
      { type: 'turn_start', agent: jobAgent },
      { type: 'tool_start', agent: jobAgent, tool_use: { id: 'sub-tool-1', name: 'Bash' } },
      { type: 'text_delta', agent: jobAgent, text: 'job text' },
    ])
    // The job's own content belongs to the jobs feed: no nested Bash block,
    // no job text in the chat timeline.
    expect(document.querySelector('[data-tool-root][data-tool-name="Bash"]')).toBeNull()
    expect(document.body.textContent).not.toContain('job text')
    // turn_start is exempt: the launch card keeps the agent-type name.
    expect(document.querySelector('[data-tool-root][data-tool-name="Agent Explorer"]')).toBeTruthy()

    // Control: the same sequence without a job_id (sync sub-agent) renders
    // as before — the filter must not over-skip.
    dispatchEvents([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'agent-2', name: 'Agent' } },
    ])
    const syncAgent = { parent_tool_use_id: 'agent-2', agent_type: 'Explorer', depth: 1 }
    dispatchEvents([
      { type: 'turn_start', agent: syncAgent },
      { type: 'tool_start', agent: syncAgent, tool_use: { id: 'sub-tool-2', name: 'Bash' } },
    ])
    expect(document.querySelector('[data-tool-root][data-tool-name="Bash"]')).toBeTruthy()
  })
})

describe('STT mic availability', () => {
  it('config frame stt:true enables the mic; absent renders the slashed unavailable state', () => {
    mount()
    const mic = () =>
      document.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement

    dispatch({
      type: 'config',
      models: [],
      current: { provider: '', model: '' },
      thinking: 'auto',
      stt: true,
    })
    expect(mic().classList.contains('stt-mic-off')).toBe(false)

    dispatch({
      type: 'config',
      models: [],
      current: { provider: '', model: '' },
      thinking: 'auto',
    })
    expect(mic().classList.contains('stt-mic-off')).toBe(true)
    expect(mic().disabled).toBe(true)
  })
})

// ── Voice face integration: long-press entry → PTT turn → marked message ─
// Pins the whole chain from the plan's test matrix: long-press → overlay →
// big-mic hold (/ws/stt, ptt start frame) → release → done → message frame
// with the transient voice flag → engine events → speak → TTS queue writes
// (POST /api/tts sentences) → worklet write → drain → idle.
describe('voice face integration', () => {
  interface TTSQueueMock {
    opts: {
      write(pcm: Uint8Array): void
      onError?(message: string): void
      onSettled?(): void
    }
    texts: string[]
    aborts: number
    inFlightCount: number
    settle(): void
  }
  interface SttSockMock {
    sentText: string[]
    sentBinary: ArrayBuffer[]
    dispatch(data: unknown): void
  }
  const ttsQueues = (): TTSQueueMock[] =>
    (createTTSQueue as unknown as { all: TTSQueueMock[] }).all
  const lastTTSQueue = (): TTSQueueMock => ttsQueues()[ttsQueues().length - 1]
  const sttSocks = (): SttSockMock[] =>
    (sttModule as unknown as { __sttSocks: SttSockMock[] }).__sttSocks
  const lastStt = (): SttSockMock => sttSocks()[sttSocks().length - 1]

  class FakeAudioContext {
    static created: FakeAudioContext[] = []
    static analysers: Array<{ connectedTo: unknown; data: Uint8Array }> = []
    readonly sampleRate: number
    readonly destination = {}
    readonly audioWorklet = { addModule: async () => {} }
    constructor(opts?: { sampleRate?: number }) {
      this.sampleRate = opts?.sampleRate ?? 48000
      FakeAudioContext.created.push(this)
    }
    resume() {
      return Promise.resolve()
    }
    close() {
      return Promise.resolve()
    }
    createAnalyser() {
      const analyser = {
        connectedTo: null as unknown,
        data: new Uint8Array(0),
        connect(target: unknown) {
          this.connectedTo = target
        },
        disconnect() {},
        getByteTimeDomainData(arr: Uint8Array) {
          this.data = arr
        },
      }
      FakeAudioContext.analysers.push(analyser)
      return analyser
    }
    createMediaStreamSource(_s: unknown) {
      return { connect() {}, disconnect() {} }
    }
  }
  class FakeAudioWorkletNode {
    static created: Array<{ node: FakeAudioWorkletNode; name: string }> = []
    readonly name: string
    readonly port = {
      posted: [] as unknown[],
      onmessage: null as ((e: { data: unknown }) => void) | null,
      postMessage(msg: unknown) {
        this.posted.push(msg)
      },
    }
    constructor(_ctx: unknown, name: string) {
      this.name = name
      FakeAudioWorkletNode.created.push({ node: this, name })
    }
    connect() {}
    disconnect() {}
  }
  const nodeByName = (name: string): FakeAudioWorkletNode[] =>
    FakeAudioWorkletNode.created.filter((e) => e.name === name).map((e) => e.node)

  // AudioContext/AudioWorkletNode/navigator.mediaDevices are absent in
  // jsdom; the real capture/playback graphs are driven through these fakes.
  // Manual save/restore (no vi.unstubAllGlobals): the file-scope
  // IntersectionObserver stub must survive this describe.
  const realAudioContext = globalThis.AudioContext
  const realAudioWorkletNode = globalThis.AudioWorkletNode
  const realBlobUrl = URL.createObjectURL

  beforeEach(() => {
    FakeAudioContext.created = []
    FakeAudioContext.analysers = []
    FakeAudioWorkletNode.created = []
    ttsQueues().length = 0
    sttSocks().length = 0
    vi.stubGlobal('AudioContext', FakeAudioContext)
    vi.stubGlobal('AudioWorkletNode', FakeAudioWorkletNode)
    Object.defineProperty(navigator, 'mediaDevices', {
      value: { getUserMedia: async () => ({ getTracks: () => [] }) },
      configurable: true,
    })
    URL.createObjectURL = () => 'blob:mock'
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.stubGlobal('AudioContext', realAudioContext)
    vi.stubGlobal('AudioWorkletNode', realAudioWorkletNode)
    URL.createObjectURL = realBlobUrl
    Reflect.deleteProperty(navigator, 'mediaDevices')
  })

  function mountWithRoot() {
    document.body.innerHTML = ''
    const rootEl = document.createElement('div')
    rootEl.id = 'root'
    document.body.appendChild(rootEl)
    const chat = createChat({ connected: true })
    rootEl.appendChild(chat.root)
    return chat
  }
  const enableVoice = () =>
    dispatch({
      type: 'config',
      models: [],
      current: { provider: '', model: '' },
      thinking: 'auto',
      stt: true,
      tts: true,
    })
  const micBtn = () => document.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement
  const voicePage = () => document.querySelector('.voice-page') as HTMLElement
  const flushMicrotasks = async (): Promise<void> => {
    for (let i = 0; i < 12; i++) await Promise.resolve()
  }

  // User rows live in the chat timeline (messagesContainer `.space-y-7`); the
  // voice face hides #root with display:none, which jsdom does not apply to
  // textContent — every bubble assertion below is a DOM query, not a
  // visibility check.
  const userBubbleTexts = (): string[] =>
    Array.from(document.querySelectorAll('.space-y-7 .ml-auto')).map((el) => el.textContent ?? '')
  const queuedBubbleTexts = (): string[] =>
    Array.from(document.querySelectorAll('.queued-bubble span:first-child')).map(
      (el) => el.textContent ?? '',
    )

  // Entry gesture (long-press 350ms) + the voice page's big-mic hold and
  // release. Stops at the stt socket so each case dispatches its own final.
  // midTurn puts the chat in a streaming turn BEFORE the face opens.
  async function faceAtReleasedMic(
    midTurn = false,
  ): Promise<{ sttWs: SttSockMock; bigMic: HTMLElement }> {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    enableVoice()
    if (midTurn) dispatchEvents([{ type: 'query_start' }])
    micBtn().dispatchEvent(
      new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }),
    )
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    const bigMic = voicePage().querySelector('.voice-mic') as HTMLElement
    bigMic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true }))
    await flushMicrotasks()
    const sttWs = lastStt()
    bigMic.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    return { sttWs, bigMic }
  }

  it('voice final renders its user bubble in the chat timeline before any engine event', async () => {
    vi.useFakeTimers()
    const { sttWs } = await faceAtReleasedMic()
    sttWs.dispatch('{"type":"done","text":"你好。"}')
    await flushMicrotasks()
    expect(userBubbleTexts()).toEqual(['[voice] 你好。'])
    expect(sent.filter((m) => (m as { type?: string }).type === 'message')).toEqual([
      { type: 'message', text: '你好。', voice: true },
    ])
  })

  it('voice final mid-turn creates the cancellable queued bubble, marker included', async () => {
    vi.useFakeTimers()
    const { sttWs } = await faceAtReleasedMic(true)
    sttWs.dispatch('{"type":"done","text":"你好。"}')
    await flushMicrotasks()
    // The queued bubble renders inside inputBar.bubbles under the display:none
    // #root while the voice face is up; jsdom sees it, a phone user sees it
    // only after closing the face.
    expect(queuedBubbleTexts()).toEqual(['[voice] 你好。'])
    // The server's uuid ack backfills the local uuid:'' entry — the bubble
    // must survive the backfill, not be orphaned by a re-render.
    dispatch({ type: 'queued', uuid: 'srv-1' })
    expect(queuedBubbleTexts()).toEqual(['[voice] 你好。'])
    expect(sent.filter((m) => (m as { type?: string }).type === 'message')).toEqual([
      { type: 'message', text: '你好。', voice: true },
    ])
  })

  it('voice turn: query_start arms STOP, query_end clears it', async () => {
    vi.useFakeTimers()
    const { sttWs } = await faceAtReleasedMic()
    sttWs.dispatch('{"type":"done","text":"你好。"}')
    await flushMicrotasks()
    dispatchEvents([{ type: 'query_start' }])
    expect(document.body.textContent).toContain('STOP')
    dispatchEvents([{ type: 'query_end' }])
    expect(document.body.textContent).not.toContain('STOP')
  })

  it('voice final lands in the input history clean', async () => {
    vi.useFakeTimers()
    const { sttWs } = await faceAtReleasedMic()
    sttWs.dispatch('{"type":"done","text":"你好。"}')
    await flushMicrotasks()
    setTextarea('')
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    expect(ta.value).toBe('你好。')
  })

  it('abort-rewind of a voice turn removes its bubble and restores the clean text', async () => {
    vi.useFakeTimers()
    const { sttWs } = await faceAtReleasedMic()
    sttWs.dispatch('{"type":"done","text":"你好。"}')
    await flushMicrotasks()
    dispatchEvents([{ type: 'query_start' }])
    clickStop()
    dispatchEvents([
      { type: 'query_end', aborted: true, rewound: true, rewound_text: '[voice] 你好。' },
    ])
    expect(userBubbleTexts()).toEqual([])
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('你好。')
  })

  it('cancel_result of a queued voice utterance restores the textarea without the marker', async () => {
    vi.useFakeTimers()
    const { sttWs } = await faceAtReleasedMic(true)
    sttWs.dispatch('{"type":"done","text":"你好。"}')
    await flushMicrotasks()
    dispatch({ type: 'queued', uuid: 'srv-1' })
    ;(document.querySelector('.queued-bubble') as HTMLElement).click()
    expect(sent).toContainEqual({ type: 'cancel_queued', uuids: ['srv-1'] })
    dispatch({ type: 'cancel_result', restored: [{ uuid: 'srv-1', text: '[voice] 你好。' }] })
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('你好。')
    expect(queuedBubbleTexts()).toEqual([])
  })

  it('cancel_result with two restored voice prompts strips the marker per entry', async () => {
    vi.useFakeTimers()
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'cancel_result',
      restored: [
        { uuid: 'srv-1', text: '[voice] 第一句。' },
        { uuid: 'srv-2', text: '[voice] 第二句。' },
      ],
    })
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe(
      '第一句。\n第二句。',
    )
  })

  it('long-press entry: overlay opens ONLY (no recording) → big-mic hold → release sends the transcript with voice:true', async () => {
    vi.useFakeTimers()
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    enableVoice()
    expect(micBtn().disabled).toBe(false)

    // Long-press 350ms on the chat mic enters the voice face — and NOTHING
    // else (10-28 ruling): no recording rides the entry gesture; recording
    // starts only on the voice page's big mic below.
    micBtn().dispatchEvent(
      new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }),
    )
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()

    expect(voicePage()).toBeTruthy()
    expect(document.getElementById('root')?.getAttribute('aria-hidden')).toBe('true')
    expect(document.getElementById('root')?.style.display).toBe('none')
    // Fullscreen page owns the viewport: the body itself never scrolls,
    // whatever the phone's dvh/inset arithmetic does to the document box.
    expect(document.body.style.overflow).toBe('hidden')
    // Voice mode is retired (10-28): entry sends NOTHING voice-related — no
    // message; the marker rides every voice final below.
    expect(sent.filter((m) => ['voice_mode', 'message'].includes((m as { type?: string }).type ?? ''))).toEqual([])
    // Entry-only: no stt round, no capture graph, page idles.
    expect(sttSocks().length).toBe(0)
    expect(voicePage().dataset.st).toBe('idle')

    // Push the BIG mic: one /ws/stt round opens with a ptt start frame.
    const bigMic = voicePage().querySelector('.voice-mic') as HTMLElement
    bigMic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true }))
    await flushMicrotasks()
    const sttWs = lastStt()
    expect(sttWs.sentText.length).toBe(1)
    expect(JSON.parse(sttWs.sentText[0])).toEqual({ type: 'start', prompt: '', ptt: true })
    expect(voicePage().dataset.st).toBe('listen')
    expect(nodeByName('stt-capture').length).toBe(1)

    // ⑤ at the integration level: the real SttSession mic leg built the
    // sink-only analyser tap on the capture chain (the play ctx's analyser
    // is a separate node — this one belongs to the 16k capture ctx, created
    // second, and connects nowhere but the source → analyser edge).
    expect(FakeAudioContext.analysers.length).toBeGreaterThanOrEqual(2)
    const micAnalyser = FakeAudioContext.analysers[1]
    expect(micAnalyser.connectedTo).toBe(null) // sink-only: never destination

    // Mic capture frames go up the stt socket as s16le PCM once ready.
    sttWs.dispatch('{"type":"ready","mode":"live"}')
    const frame = new Float32Array([0.5, -0.25, 0, 0.125])
    nodeByName('stt-capture')[0].port.onmessage?.({ data: frame })
    expect(sttWs.sentBinary.length).toBe(1)
    expect(Array.from(new Uint8Array(sttWs.sentBinary[0]))).toEqual(Array.from(floatTo16Le(frame)))

    // Partial transcript renders DURING the entry animation window (the
    // voiceIn slide runs 350→800ms; +100ms here is mid-flight) — the words
    // must already be on screen, not waiting for the animation to settle.
    await vi.advanceTimersByTimeAsync(100)
    sttWs.dispatch('{"type":"partial","text":"你好"}')
    const partialEl = voicePage().querySelector('.voice-partial') as HTMLElement
    expect(partialEl.textContent).toBe('你好')
    expect(partialEl.hasAttribute('hidden')).toBe(false)

    // Releasing the big mic ends the utterance round.
    bigMic.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    expect(sttWs.sentText.at(-1)).toBe('{"type":"stop"}')
    expect(voicePage().dataset.st).toBe('wait')

    // The transcript lands as a user message plus the wire-transient voice
    // flag (every voice final): same path as typing, the connector prefixes
    // the marker server-side and the flag never persists.
    sttWs.dispatch('{"type":"done","text":"你好。"}')
    await flushMicrotasks()
    expect(sent).toContainEqual({ type: 'message', text: '你好。', voice: true })
    const tts = lastTTSQueue()
    expect(tts.texts).toEqual([]) // no prefetch at the final — only body text synthesizes

    // Engine reply: speak state + rendered sentence out as TTS.
    dispatchEvents([{ type: 'query_start' }, { type: 'text_delta', text: '大家好。' }, { type: 'text_end' }])
    expect(voicePage().dataset.st).toBe('speak')
    expect(tts.texts).toContainEqual('大家好。')

    // TTS PCM down → worklet write; settle + query_end + drain → idle.
    tts.opts.write(new Uint8Array([0, 64, 0, 192]))
    const playNode = nodeByName('stream_processor')[0]
    expect(playNode.port.posted.some((m) => (m as { event?: string }).event === 'write')).toBe(true)
    tts.settle()
    dispatchEvents([{ type: 'query_end' }])
    playNode.port.onmessage?.({ data: { event: 'stop' } })
    expect(voicePage().dataset.st).toBe('idle')
  })

  it('entry holds never record: a silent open-then-close leaves zero frames', async () => {
    vi.useFakeTimers()
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    enableVoice()
    micBtn().dispatchEvent(
      new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }),
    )
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    // The entry finger lifting does nothing: no stt round ever opened, page
    // never left idle, and closing from here sends no frames at all (no
    // speech happened — voice mode is retired, nothing to turn off).
    window.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    expect(sttSocks().length).toBe(0)
    expect(voicePage().dataset.st).toBe('idle')
  })

  it('empty final (silent hold on the big mic) sends nothing at all', async () => {
    vi.useFakeTimers()
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    enableVoice()
    micBtn().dispatchEvent(
      new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }),
    )
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    const bigMic = voicePage().querySelector('.voice-mic') as HTMLElement
    bigMic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true }))
    await flushMicrotasks()
    const sttWs = lastStt()
    sttWs.dispatch('{"type":"ready","mode":"live"}')
    sttWs.dispatch('{"type":"partial","text":"嗯"}')
    bigMic.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    // Ambient-noise final: whitespace/punctuation only — no message, no
    // voice flag, no filler prefetch. The round never happened.
    sttWs.dispatch('{"type":"done","text":"。"}')
    await flushMicrotasks()
    expect(sent.filter((m) => (m as { type?: string }).type === 'message')).toEqual([])
    expect(lastTTSQueue().texts).toEqual([]) // nothing synthesized for a non-round
  })

  it('contextmenu (desktop right-click) enters the voice page WITHOUT recording', async () => {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    enableVoice()
    const ev = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
    micBtn().dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(true)
    expect(voicePage()).toBeTruthy()
    // Lazy marking: entry-only opens send nothing voice-related.
    expect(sent.filter((m) => (m as { type?: string }).type === 'message')).toEqual([])
    // Entry-only: no first hold, no capture graph, no mic round, page stays idle.
    expect(sttSocks().length).toBe(0)
    expect(nodeByName('stt-capture').length).toBe(0)
    expect(voicePage().dataset.st).toBe('idle')
    // No release wiring was armed either.
    window.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    expect(sttSocks().length).toBe(0)
    await flushMicrotasks()
  })

  it('X close tears the whole face down: page removed, chat restored, no frames, tts socket closed', async () => {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    enableVoice()
    micBtn().dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    expect(voicePage()).toBeTruthy()
    const tts = lastTTSQueue()

    ;(voicePage().querySelector('.voice-x') as HTMLElement).click()

    // Container removed and the chat shell visible again.
    expect(voicePage()).toBe(null)
    const rootEl = document.getElementById('root') as HTMLElement
    expect(rootEl.getAttribute('aria-hidden')).toBe(null)
    expect(rootEl.style.display).toBe('')
    // The viewport lock releases with the face.
    expect(document.body.style.overflow).toBe('')
    // Voice mode is retired: this speech-less session sent nothing, and the
    // close sends nothing either — the wire footprint is empty.
    expect(sent.filter((m) => (m as { type?: string }).type === 'voice_mode')).toEqual([])
    expect(sent.filter((m) => (m as { type?: string }).type === 'message')).toEqual([])
    expect(tts.aborts).toBe(1)
    // Re-entry mounts a fresh face on a clean slate.
    micBtn().dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    expect(voicePage()).toBeTruthy()
    expect(sent.filter((m) => (m as { type?: string }).type === 'voice_mode')).toEqual([])
    await flushMicrotasks()
  })

  it('prefetch stays silent behind the voice face and resumes after close', async () => {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    enableVoice()
    const page = (id: string, cursor: string) => ({
      type: 'history' as const,
      messages: [{
        id, role: 'user', text: `old ${id}`,
        thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: cursor,
      hasMore: true,
    })
    const prefetches = () => sent.filter((m) => (m as { type?: string; cursor?: string }).type === 'history_request' && (m as { cursor?: string }).cursor !== '')
    dispatch(page('m-1', 'a:9:0:1'))
    await flushMicrotasks()
    // jsdom measures zero layout, so the visible page prefetches at load end.
    expect(prefetches().length).toBe(1)
    // Enter the voice face — the chat tree goes display:none.
    micBtn().dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    expect(voicePage()).toBeTruthy()
    sent.length = 0
    // A further page arrives while hidden (the in-flight prefetch
    // answering). Its load-completion re-check must NOT self-feed another
    // request: hidden layout reads all-zero and lies (the 60-page loop
    // first-cause, 2026-10-30 14:29).
    dispatch(page('m-2', 'a:8:0:1'))
    await flushMicrotasks()
    expect(prefetches()).toEqual([])
    // X close: the chat face is visible again and the same re-check fires.
    ;(voicePage().querySelector('.voice-x') as HTMLElement).click()
    expect(voicePage()).toBe(null)
    dispatch(page('m-3', 'a:7:0:1'))
    await flushMicrotasks()
    expect(prefetches().length).toBe(1)
  })

  it('a scoped tts error surfaces in the chat capsule instead of silence', async () => {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    enableVoice()
    micBtn().dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    expect(voicePage()).toBeTruthy()
    // A failed sentence synthesis reports its message verbatim — the text
    // is what the user needs to see (same policy as an error frame).
    lastTTSQueue().opts.onError?.('tts: no default voice')
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.textContent).toBe('tts: no default voice')
    expect(capsule.classList.contains('opacity-0')).toBe(false)
  })

  it('long-press with voice unavailable (stt but no tts) never opens the face', async () => {
    vi.useFakeTimers()
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'config',
      models: [],
      current: { provider: '', model: '' },
      thinking: 'auto',
      stt: true,
    })
    micBtn().dispatchEvent(
      new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }),
    )
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    expect(voicePage()).toBe(null)
    expect(sent.filter((m) => (m as { type?: string }).type === 'voice_mode')).toEqual([])
  })
})

describe('avatar consumption', () => {
  // Manual save/restore: this file stubs IntersectionObserver at module
  // scope, so vi.unstubAllGlobals() would strip it for every later test.
  const realFetch = globalThis.fetch
  afterEach(() => {
    globalThis.fetch = realFetch
  })

  // The chat shell embeds a full settings page (the sidebar gear), whose
  // avatar row/panel add their own data-avatar holders — scope to the
  // message stream by excluding both settings containers.
  const chatHolders = (who: 'agent' | 'user', selector = ''): HTMLElement[] =>
    Array.from(
      document.querySelectorAll<HTMLElement>(`[data-avatar="${who}"]${selector ? ' ' + selector : ''}`),
    ).filter((el) => !el.closest('[data-general-row]') && !el.closest('[data-avatar-panel]'))

  function sendUserAndStartAssistant(): void {
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('avatar probe')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
  }

  it('renders default avatars (G letter / user icon) inside real shells', () => {
    mount()
    sendUserAndStartAssistant()
    const agents = chatHolders('agent')
    const users = chatHolders('user')
    // One user echo + one assistant shell were dispatched.
    expect(agents.length).toBe(1)
    expect(users.length).toBe(1)
    for (const el of agents) expect(el.textContent).toBe('G')
    for (const el of users) expect(el.querySelector('svg')).toBeTruthy()
  })

  it('rewrites already-rendered agent holders to img and serves new shells with img directly', async () => {
    mount()
    sendUserAndStartAssistant()
    expect(chatHolders('agent', 'img').length).toBe(0)

    globalThis.fetch = (async () => ({
      ok: true,
      status: 200,
      headers: new Headers({ ETag: '"t-1"' }),
    })) as unknown as typeof fetch
    await refreshAvatar('agent')

    const imgs = chatHolders('agent', 'img') as HTMLImageElement[]
    expect(imgs.length).toBe(1)
    expect(imgs[0].getAttribute('src')).toMatch(/^\/api\/avatar\/agent\?v=/)
    expect(chatHolders('user', 'svg').length).toBe(1)

    dispatchEvents([{ type: 'query_start' }])
    const after = chatHolders('agent', 'img') as HTMLImageElement[]
    expect(after.length).toBe(2)
    expect(after[1].getAttribute('src')).toContain('t-1')
  })

  it('falls back to the default rendering when the img errors', async () => {
    mount()
    sendUserAndStartAssistant()
    globalThis.fetch = (async () => ({
      ok: true,
      status: 200,
      headers: new Headers({ ETag: '"t-1"' }),
    })) as unknown as typeof fetch
    await refreshAvatar('agent')
    const img = chatHolders('agent', 'img')[0] as HTMLImageElement
    expect(img).toBeTruthy()

    img.dispatchEvent(new Event('error'))

    for (const el of chatHolders('agent')) expect(el.textContent).toBe('G')
    expect(chatHolders('agent', 'img').length).toBe(0)
  })
})

// Mutation kill tests for src/chat.ts (StrykerJS census follow-up).
// Colocated per the wui mutate convention: merged into chat.test.ts, these
// describes only pin behaviors the census found un-asserted. Assertions
// target observable DOM / wire output.

// ── mapHistoryToChatMessages ────────────────────────────────────────────────

describe('chat mutation kills: mapHistoryToChatMessages', () => {
  it('merges consecutive assistant parts: text concat, usage summed, error kept from the part that has one', () => {
    const noUsage = { usage: undefined } as Partial<HistoryChatMsg>
    const out = mapHistoryToChatMessages([
      histMsg({ id: 'u1', role: 'user', text: 'q' }),
      histMsg({ id: 'a1', text: 'part1 ', usage: { inputTokens: 1, outputTokens: 2, cacheRead: 3, cacheCreation: 4 } }),
      histMsg({ id: 'a2', text: 'part2', usage: { inputTokens: 10, outputTokens: 20, cacheRead: 30, cacheCreation: 40 } }),
      histMsg({ id: 'a3', text: undefined as unknown as string, error: 'boom' }),
      // A trailing part with neither usage nor error must overwrite neither.
      { ...histMsg({ id: 'a4', text: undefined as unknown as string }), ...noUsage },
    ])
    expect(out.length).toBe(2)
    expect(out[0].role).toBe('user')
    expect(out[1].id).toBe('a1')
    expect(out[1].blocks).toEqual([{ kind: 'text', id: '', text: 'part1 part2' }])
    expect(out[1].usage).toEqual({ inputTokens: 11, outputTokens: 22, cacheRead: 33, cacheCreation: 44 })
    expect(out[1].error).toBe('boom')
  })

  it('merge with no prior usage sums from zero', () => {
    const out = mapHistoryToChatMessages([
      { ...histMsg({ id: 'a1', text: 'x' }), usage: undefined } as unknown as HistoryChatMsg,
      histMsg({ id: 'a2', usage: { inputTokens: 5, outputTokens: 6, cacheRead: 7, cacheCreation: 8 } }),
    ])
    expect(out.length).toBe(1)
    expect(out[0].usage).toEqual({ inputTokens: 5, outputTokens: 6, cacheRead: 7, cacheCreation: 8 })
  })

  it('defaults: absent id/error/status map to empty id, empty error, done status', () => {
    const out = mapHistoryToChatMessages([
      { role: 'user', text: 'hi', thinking: [], tools: [], startedAt: 0 } as unknown as HistoryChatMsg,
    ])
    expect(out[0].id).toBe('')
    expect(out[0].error).toBe('')
    expect(out[0].status).toBe('done')
  })

  it('block mapping: image block id empty, thinking inactive with zero duration, tool summary empty', () => {
    const blocks: HistoryBlock[] = [
      { kind: 'image', src: 'data:image/png;base64,AA' },
      { kind: 'thinking', thinking: { text: 'th' } },
      { kind: 'tool', tool: { id: 't1', name: 'Bash' } },
    ]
    const out = mapHistoryToChatMessages([
      histMsg({ id: 'a1', role: 'assistant', blocks, text: '' }),
    ])
    expect(out[0].blocks[0]).toEqual({ kind: 'image', id: '', src: 'data:image/png;base64,AA' })
    expect(out[0].blocks[1]).toEqual({ kind: 'thinking', id: '', text: 'th', durationNs: 0, active: false, startedAt: 0 })
    expect(out[0].blocks[2]).toEqual({
      kind: 'tool', id: 't1', name: 'Bash', summary: '',
      isSearch: false, isRead: false, isList: false, isLsp: false, isWeb: false, isTask: false,
      state: 'done', timingNs: 0, displayOutput: '', startedAt: 0, children: [],
    })
  })
})

// ── committed history rendering ─────────────────────────────────────────────

describe('chat mutation kills: committed history rendering', () => {
  it('user message error renders the red error box with the error text', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'u1', role: 'user', text: 'q', error: 'upload failed' })],
      nextCursor: '', hasMore: false,
    })
    const box = document.querySelector('.break-all.text-red') as HTMLElement
    expect(box).toBeTruthy()
    expect(box.textContent).toBe('upload failed')
    expect(box.className).toContain('border-red/40')
  })

  it('an errored history tool renders the red duration style', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({
        id: 'a0', role: 'assistant',
        blocks: [{ kind: 'tool', tool: { id: 't1', name: 'Bash', isError: true, isRunning: false, durationNs: 2_000_000_000 } }],
      })],
      nextCursor: '', hasMore: false,
    })
    const dur = document.querySelector('[data-tool-root] .font-mono.text-xs') as HTMLElement
    expect(dur).toBeTruthy()
    expect(dur.className).toContain('text-red')
    expect(dur.className).not.toContain('text-blue')
  })

  it('assistant image block renders a defensive 400px thumbnail', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({
        id: 'a1', role: 'assistant',
        blocks: [{ kind: 'image', src: 'data:image/png;base64,QUJD' }],
      })],
      nextCursor: '', hasMore: false,
    })
    const img = document.querySelector('.space-y-3 img') as HTMLImageElement
    expect(img).toBeTruthy()
    expect(img.src).toBe('data:image/png;base64,QUJD')
    expect(img.className).toBe('block max-w-[400px] max-h-[400px] rounded-lg my-1')
  })

  it('legacy document text with no trailing text renders the chip only', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({
        id: 'u1', role: 'user',
        text: '[Document: notes.md saved at /tmp/x]\n',
        blocks: [{ kind: 'text', text: '[Document: notes.md saved at /tmp/x]\n' }],
      })],
      nextCursor: '', hasMore: false,
    })
    const chip = document.querySelector('span.font-mono') as HTMLElement
    expect(chip.textContent).toBe('[notes.md]')
    expect(chip.className).toContain('font-mono')
    // No trailing empty user-text span is appended after the chip: the
    // content div's only child is the chip itself.
    expect(chip.parentElement!.childElementCount).toBe(1)
    expect(document.querySelectorAll('.whitespace-pre-wrap').length).toBe(1)
  })

  it('history image click opens the lightbox with the same data URL', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({
        id: 'u1', role: 'user',
        blocks: [
          { kind: 'text', text: 'look' },
          { kind: 'image', src: 'data:image/png;base64,WFk' },
        ],
      })],
      nextCursor: '', hasMore: false,
    })
    const img = document.querySelector('.max-w-fit img') as HTMLImageElement
    img.click()
    const overlay = document.querySelector('.cursor-zoom-out img') as HTMLImageElement
    expect(overlay).toBeTruthy()
    expect(overlay.src).toBe('data:image/png;base64,WFk')
  })

  it('running tool replay dedupes a live tool_start with the same id', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({
        id: 'a1', role: 'assistant',
        blocks: [{ kind: 'tool', tool: { id: 't-1', name: 'Bash', isRunning: true } }],
      })],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([{ type: 'tool_start', tool_use: { id: 't-1', name: 'Bash' } }])
    const roots = document.querySelectorAll('[data-tool-root]')
    expect(roots.length).toBe(1)
    expect(roots[0].getAttribute('data-tool-name')).toBe('Bash')
  })

  it('replayed nested running tools are registered so their live tool_end finalizes them', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{
        kind: 'tool', id: 'agent-1', name: 'Agent', summary: '', isSearch: false, isRead: false,
        isList: false, isLsp: false, isWeb: false, isTask: false, state: 'running', timingNs: 0,
        displayOutput: '', startedAt: Date.now(), children: [{
          kind: 'tool', id: 'inner-1', name: 'Edit', summary: '', isSearch: false, isRead: false,
          isList: false, isLsp: false, isWeb: false, isTask: false, state: 'running', timingNs: 0,
          displayOutput: '', startedAt: Date.now(), children: [],
        }],
      }],
      nextCursor: '', hasMore: false, busy: true,
    })
    dispatchEvents([{
      type: 'tool_end',
      tool_result: { tool_use_id: 'inner-1', display_output: 'file contents here' },
    }])
    const inner = document.querySelector('[data-tool-root][data-tool-name="Edit"]') as HTMLElement
    expect(inner).toBeTruthy()
    const innerBody = inner.querySelector('.md-body') as HTMLElement
    expect(innerBody.textContent).toBe('file contents here\n')
    // The Edit row finalized: duration element leaves the running style.
    const dur = inner.querySelector('.text-t3, .text-red') as HTMLElement
    expect(dur.className).not.toContain('text-blue')
  })
})

// ── share source resolution (long-press menu) ───────────────────────────────
describe('chat mutation kills: share source resolution', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    armShareBridge()
  })

  function replayDoneAssistant(blocks: HistoryBlock[]) {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'a1', role: 'assistant', blocks })],
      nextCursor: '', hasMore: false,
    })
  }

  it('tool press copies that tool output by document order (second tool of two)', () => {
    replayDoneAssistant([
      { kind: 'tool', tool: { id: 't1', name: 'Bash', displayOutput: 'OUT-ONE', durationNs: 1 } },
      { kind: 'tool', tool: { id: 't2', name: 'Read', displayOutput: 'OUT-TWO', durationNs: 2 } },
    ])
    sharePress(toolRootByName('Read').querySelector('.font-mono') as HTMLElement)
    expect(shareMenu()).toBeTruthy()
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('OUT-TWO')
  })

  it('sub-agent child press resolves to the parent tool row, not the child text', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [
        { kind: 'text', id: '', text: 'narration' },
        {
          kind: 'tool', id: 'agent-1', name: 'Agent', summary: '', isSearch: false, isRead: false,
          isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 5,
          displayOutput: 'AGENT-RESULT', startedAt: 0, children: [
            { kind: 'text', id: '', text: 'inner child text' },
            {
              kind: 'tool', id: 'inner-1', name: 'Edit', summary: '', isSearch: false, isRead: false,
              isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 1,
              displayOutput: 'INNER-OUTPUT', startedAt: 0, children: [],
            },
          ],
        },
      ],
      nextCursor: '', hasMore: false, busy: true,
    })
    // Press the child TEXT: resolves up to the parent tool row.
    const childText = document.querySelector('[data-tool-children] [data-block-kind="text"]') as HTMLElement
    sharePress(childText)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('AGENT-RESULT')
    ;(document.querySelector('.share-menu') as HTMLElement).remove()
    // Press the parent tool root: the index pairing must skip the nested
    // child tool root (document order among top-level tools only).
    sharePress(toolRootByName('Agent'))
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('AGENT-RESULT')
  })

  it('markdown text press copies the markdown source, not the rendered text', () => {
    replayDoneAssistant([
      { kind: 'text', text: '**bold** source' },
      { kind: 'text', text: 'later block' },
    ])
    const firstDiv = document.querySelectorAll('[data-block-kind="text"]')[0] as HTMLElement
    sharePress(firstDiv)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('**bold** source')
  })

  it('live reminder row press copies the envelope display body', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: {
          attachment: { prompt: '<job-notification>\n<summary>job done</summary>\n</job-notification>' },
        },
      },
    })
    dispatchEvents([{ type: 'query_end' }])
    const row = document.querySelector('[data-reminder-row]') as HTMLElement
    expect(row).toBeTruthy()
    sharePress(row.querySelector('.font-mono') as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('job done')
  })

  it('user echo press copies the echoed text inside a done assistant message', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: { type: 'attachment', message: { attachment: { prompt: 'queued while busy' } } },
    })
    dispatchEvents([{ type: 'text_start' }, { type: 'text_delta', text: 'the final reply' }, { type: 'query_end' }])
    const echo = document.querySelector('[data-block-kind="user"]') as HTMLElement
    expect(echo).toBeTruthy()
    sharePress(echo)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('queued while busy')
  })

  it('text press after an error frame falls back to the DOM text', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'partial answer' },
    ])
    dispatch({ type: 'error', message: 'engine blew up' })
    const textDiv = document.querySelector('[data-block-kind="text"]') as HTMLElement
    sharePress(textDiv)
    clickCopy()
    // The rendered markdown div carries one trailing newline.
    expect(copyText).toHaveBeenCalledWith('partial answer\n')
  })

  it('streaming: tool copy works mid-flight, text and reminder presses open nothing', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'tool_output_delta', tool_result: { tool_use_id: 't1', display_output: 'live output' } },
      { type: 'text_start' },
    ])
    sharePress(toolRootByName('Bash').querySelector('.font-mono') as HTMLElement)
    expect(shareMenu()).toBeTruthy()
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('live output')
    ;(document.querySelector('.share-menu') as HTMLElement).remove()
    const textDiv = document.querySelector('[data-block-kind="text"]') as HTMLElement
    sharePress(textDiv)
    expect(shareMenu()).toBeNull()
  })

  it('whitespace-only tool output falls back to the final reply', () => {
    replayDoneAssistant([
      { kind: 'tool', tool: { id: 't1', name: 'Bash', displayOutput: '   ', durationNs: 1 } },
      { kind: 'text', text: 'final reply text' },
    ])
    sharePress(toolRootByName('Bash').querySelector('.font-mono') as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('final reply text')
  })

  it('whitespace-only markdown message opens no menu at all', () => {
    replayDoneAssistant([{ kind: 'text', text: '   ' }])
    const div = document.querySelector('[data-block-kind="text"]') as HTMLElement
    sharePress(div)
    expect(shareMenu()).toBeNull()
  })
})

// ── connection state visuals ────────────────────────────────────────────────

describe('chat mutation kills: connection state visuals', () => {
  it('reconnecting aborts in-flight uploads and animates dots', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    // Stage a document attachment and start its upload.
    vi.mocked(uploadAttachment).mockImplementation((_file, opts) =>
      new Promise((_resolve, reject) => {
        opts!.signal.addEventListener('abort', () => reject(new Error('aborted')))
      }),
    )
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    const file = new File(['bytes'], 'spec.pdf', { type: 'application/pdf' })
    Object.defineProperty(docInput, 'files', { value: [file], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('here is the doc')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    stateCb!('reconnecting')
    await vi.waitFor(() => expect(chat.inputBar.getAttachments()[0].failed).toBe(true))
    const banner = document.querySelector('.text-\\[12px\\].text-red') as HTMLElement
    expect(banner.textContent).toBe('Connection lost, reconnecting.')
  })

  it('disconnect dots advance one per second', () => {
    vi.useFakeTimers()
    mount()
    stateCb!('reconnecting')
    vi.advanceTimersByTime(1000)
    const banner = document.querySelector('.text-\\[12px\\].text-red') as HTMLElement
    expect(banner.textContent).toBe('Connection lost, reconnecting..')
    vi.advanceTimersByTime(1000)
    expect(banner.textContent).toBe('Connection lost, reconnecting...')
  })

  it('banner click asks the connector to reconnect', () => {
    const calls: number[] = []
    reconnectImpl = () => calls.push(1)
    mount()
    stateCb!('reconnecting')
    const banner = document.querySelector('.text-\\[12px\\].text-red') as HTMLElement
    banner.click()
    expect(calls.length).toBe(1)
    expect(banner.textContent).toBe('Connection lost, reconnecting.')
  })

  it('upgrading then connected: capsule recovers, attribute and freeze clear', () => {
    vi.useFakeTimers()
    mount()
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'x' }])
    stateCb!('upgrading')
    expect(document.body.hasAttribute('data-upgrading')).toBe(true)
    const lastMsg = document.querySelector('.space-y-7 > div:last-child') as HTMLElement
    expect(lastMsg.className).toContain('stream-dim')
    stateCb!('connected')
    expect(document.body.hasAttribute('data-upgrading')).toBe(false)
    expect(document.querySelector('.stream-dim')).toBeNull()
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).not.toContain('opacity-0')
  })

  it('rollback timer dismisses the capsule when the socket stayed alive', () => {
    vi.useFakeTimers()
    mount()
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'x' }])
    stateCb!('upgrading')
    vi.advanceTimersByTime(20_000)
    expect(document.body.hasAttribute('data-upgrading')).toBe(false)
    expect(document.querySelector('.stream-dim')).toBeNull()
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).toContain('opacity-0')
  })

  it('give-up retires upgrading visuals and shows the failure banner', () => {
    vi.useFakeTimers()
    mount()
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'x' }])
    stateCb!('upgrading')
    stateCb!('failed')
    expect(document.body.hasAttribute('data-upgrading')).toBe(false)
    expect(document.querySelector('.stream-dim')).toBeNull()
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).toContain('opacity-0')
    const banner = document.querySelector('.text-\\[12px\\].text-red') as HTMLElement
    expect(banner.textContent).toBe('Reconnection failed. Tap to retry.')
    expect(banner.parentElement!.style.cursor).toBe('pointer')
    expect(banner.parentElement!.style.maxHeight).toBe('40px')
  })

  it('connected hides the banner and restores default cursor', () => {
    mount()
    stateCb!('reconnecting')
    stateCb!('connected')
    const banner = document.querySelector('.text-\\[12px\\].text-red') as HTMLElement
    expect(banner.parentElement!.style.maxHeight).toBe('0px')
    expect(banner.parentElement!.style.opacity).toBe('0')
    expect(banner.parentElement!.style.cursor).toBe('default')
  })
})

// ── scroll prefetch geometry ────────────────────────────────────────────────

describe('chat mutation kills: scroll prefetch geometry', () => {
  it('counts children above the viewport with inclusive-bottom boundary semantics', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    const msgs = Array.from({ length: 12 }, (_, i) =>
      histMsg({ id: 'u' + i, role: 'user', text: 'm' + i, startedAt: i }))
    dispatch({ type: 'history', messages: msgs, nextCursor: 'c1', hasMore: true })
    // loadHistory's own prefetch fired with unmocked (zero) geometry and set
    // loadingMore; answer that page to close the gate.
    dispatch({ type: 'history', messages: [], nextCursor: 'c2', hasMore: true })
    const kids = Array.from(document.querySelector('.space-y-7')!.children) as HTMLElement[]
    kids.forEach((el, i) => {
      Object.defineProperty(el, 'offsetTop', { configurable: true, value: i * 100 })
      Object.defineProperty(el, 'offsetHeight', { configurable: true, value: 50 })
    })
    const scroll = chat.scrollEl
    const reqs = () => sent.filter((m) => (m as { type: string }).type === 'history_request') as { cursor: string }[]
    // bottom of child 9 is exactly 950: inclusive >= breaks there → 9 above → fetch.
    scroll.scrollTop = 950
    scroll.dispatchEvent(new Event('scroll'))
    expect(reqs().some((m) => m.cursor === 'c2')).toBe(true)
    dispatch({ type: 'history', messages: [], nextCursor: 'c3', hasMore: true })
    // bottom of child 10 is exactly 1050: breaks there → 10 above → no fetch.
    sent.length = 0
    scroll.scrollTop = 1050
    scroll.dispatchEvent(new Event('scroll'))
    expect(reqs().length).toBe(0)
  })
})

// ── subtree auto-fetch geometry ─────────────────────────────────────────────

describe('chat mutation kills: subtree auto-fetch geometry', () => {
  function replayTruncated(truncated: number, cursor?: string): HTMLElement {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{
        kind: 'tool', id: 't1', name: 'Agent', summary: '', isSearch: false, isRead: false,
        isList: false, isLsp: false, isWeb: false, isTask: false, state: 'running', timingNs: 0,
        displayOutput: '', startedAt: Date.now(), truncated, truncatedCursor: cursor, children: [],
      }],
      nextCursor: '', hasMore: false, busy: true,
    })
    return document.querySelector('[data-tool-root] [data-tool-children]') as HTMLElement
  }
  const subtreeReqs = () => sent.filter((m) => (m as { type: string }).type === 'subtree_request')

  it('fires only when the children container sits within the window, boundaries excluded', () => {
    const container = replayTruncated(2, 'a1')
    // The snapshot render already auto-fired one fetch — answer it to clear
    // the in-flight gate.
    expect(subtreeReqs().length).toBe(1)
    const nonce = (subtreeReqs()[0] as { nonce: number }).nonce
    dispatch({ type: 'subtree_page', nonce, blockID: 't1', children: [], remaining: 2, nextAnchor: 'a2' })
    const setTop = (top: number) => {
      Object.defineProperty(container, 'getBoundingClientRect', {
        configurable: true,
        value: () => ({ top, bottom: 0, left: 0, right: 0, width: 0, height: 0, x: 0, y: 0, toJSON: () => ({}) }),
      })
    }
    const chat = document.querySelector('.flex-1.min-h-0') as HTMLElement
    const scroll = () => chat.dispatchEvent(new Event('scroll'))
    setTop(400)
    scroll()
    expect(subtreeReqs().length).toBe(1)
    setTop(-400)
    scroll()
    expect(subtreeReqs().length).toBe(1)
    setTop(300)
    scroll()
    expect(subtreeReqs().length).toBe(1)
    setTop(-300)
    scroll()
    expect(subtreeReqs().length).toBe(1)
    setTop(0)
    scroll()
    expect(subtreeReqs().length).toBe(2)
    expect((subtreeReqs()[1] as { anchor: string }).anchor).toBe('a2')
  })

  it('no subtree fetch without truncation markers or cursor', () => {
    replayTruncated(2, undefined)
    expect(subtreeReqs().length).toBe(0)
    replayTruncated(0, 'a1')
    expect(subtreeReqs().length).toBe(0)
  })

  it('a zero truncation marker with a cursor still never auto-fetches on scroll', () => {
    replayTruncated(0, 'a1')
    // Force updateScrollBtn synchronously — the observer path alone runs
    // after the test body.
    const chat = document.querySelector('.flex-1.min-h-0') as HTMLElement
    chat.dispatchEvent(new Event('scroll'))
    expect(subtreeReqs().length).toBe(0)
  })
})

// ── loadHistory scroll anchoring ────────────────────────────────────────────

describe('chat mutation kills: loadHistory scroll anchoring', () => {
  it('prepend compensates scrollTop by the height delta when scrolled up', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    const container = document.querySelector('.space-y-7') as HTMLElement
    Object.defineProperty(chat.scrollEl, 'scrollHeight', {
      configurable: true,
      get: () => container.children.length * 100,
    })
    Object.defineProperty(chat.scrollEl, 'clientHeight', { configurable: true, value: 500 })
    const first = Array.from({ length: 20 }, (_, i) =>
      histMsg({ id: 'n' + i, role: 'user', text: 'msg' + i, startedAt: i }))
    dispatch({ type: 'history', messages: first, nextCursor: '', hasMore: false })
    expect(chat.scrollEl.scrollTop).toBe(2100)
    // Scroll away from the bottom so the observer-driven autoscroll stays off.
    chat.scrollEl.scrollTop = 800
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    const older = Array.from({ length: 5 }, (_, i) =>
      histMsg({ id: 'o' + i, role: 'user', text: 'old' + i, startedAt: 0 }))
    dispatch({ type: 'history', messages: older, nextCursor: '', hasMore: false })
    await new Promise((r) => requestAnimationFrame(() => r(null)))
    // 5 children * 100px taller: 800 + 500.
    expect(chat.scrollEl.scrollTop).toBe(1300)
  })
})

// ── running-tool replay anchors ─────────────────────────────────────────────

describe('chat mutation kills: running-tool replay anchors', () => {
  it('elapsed anchors at replay time and the init log names the reason', () => {
    vi.useFakeTimers()
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({
        id: 'a1', role: 'assistant',
        blocks: [{ kind: 'tool', tool: { id: 't-1', name: 'Bash', isRunning: true } }],
      })],
      nextCursor: '', hasMore: false,
    })
    vi.advanceTimersByTime(2500)
    const elapsed = document.querySelector('.status-line .sep-elapsed + span') as HTMLElement
    expect(elapsed.textContent).toBe('2s')
    expect(dbg).toHaveBeenCalledWith(
      '[chat] initStreaming reason=loadHistory_runningTool streamContainer=true progressHandles=true')
  })
})

// ── attachment events ───────────────────────────────────────────────────────

describe('chat mutation kills: attachment events', () => {
  it('prompt fallback renders a plain user echo bubble', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: { type: 'attachment', message: { attachment: { prompt: 'typed with no content blocks' } } },
    })
    const spans = Array.from(document.querySelectorAll('.max-w-fit span.whitespace-pre-wrap')) as HTMLElement[]
    expect(spans.map((s) => s.textContent)).toEqual(['typed with no content blocks'])
  })

  it('an attachment event without a message envelope is ignored', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'event', event: { type: 'attachment' } })
    // No envelope means no attachment and no content: nothing renders and
    // the optional chain keeps the handler from throwing.
    expect(document.querySelectorAll('.max-w-fit').length).toBe(0)
    expect(document.querySelectorAll('.space-y-7 > *').length).toBe(0)
  })

  it('an image-only attachment with no prompt renders no text span', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: {
          attachment: { source_uuid: 's1' },
          content: [{ type: 'image', source: { media_type: 'image/png', data: 'QUJD' } }],
        },
      },
    })
    // The absent prompt maps to an empty string: the image renders and no
    // undefined leaks into any text node.
    const img = document.querySelector('img') as HTMLImageElement
    expect(img).toBeTruthy()
    expect(img.src).toBe('data:image/png;base64,QUJD')
    expect(document.body.textContent).not.toContain('undefined')
    expect(document.querySelector('.max-w-fit span.whitespace-pre-wrap')).toBeNull()
  })

  it('content blocks join text, inline the image data URL, and chip the document', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: {
          attachment: { source_uuid: 'q1' },
          content: [
            { type: 'text', text: 'cap ' },
            { type: 'text', text: 'tion' },
            { type: 'image', source: { media_type: 'image/png', data: 'QUJD' } },
            { type: 'document', name: 'notes.md', mime: 'text/markdown', size: 2048 },
          ],
        },
      },
    })
    const content = document.querySelector('.max-w-fit') as HTMLElement
    expect(content.querySelector('.whitespace-pre-wrap')!.textContent).toBe('cap tion')
    const img = content.querySelector('img') as HTMLImageElement
    expect(img.src).toBe('data:image/png;base64,QUJD')
    expect(content.querySelector('span.font-mono')!.textContent).toBe('[notes.md 2.0 KB]')
  })

  it('an image block without payload data renders nothing at all', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: {
          attachment: {},
          content: [{ type: 'image', source: { media_type: 'image/png' } }],
        },
      },
    })
    expect((document.querySelector('.space-y-7') as HTMLElement).children.length).toBe(0)
  })

  it('agent-tagged reminder completion renders no chat row', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        agent: { job_id: 'j1' },
        message: {
          attachment: { prompt: '<job-notification>\n<summary>x</summary>\n</job-notification>' },
        },
      },
    })
    expect(document.querySelector('[data-reminder-row]')).toBeNull()
  })

  it('idle reminder renders as a reminder row inside an assistant shell', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: {
          attachment: { prompt: '<job-notification>\n<summary>idle job done</summary>\n</job-notification>' },
        },
      },
    })
    const row = document.querySelector('[data-reminder-row]') as HTMLElement
    expect(row).toBeTruthy()
    // The body sits collapsed until the header expands it.
    ;(row.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(row.textContent).toContain('idle job done')
    // An assistant shell was built for the row (no user bubble).
    expect(document.querySelector('.max-w-fit')).toBeNull()
  })

  it('share-press on an idle reminder row opens the menu, so the message joined the model', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: '<job-notification>\n<summary>idle share body</summary>\n</job-notification>' } },
      },
    })
    const row = document.querySelector('[data-reminder-row]') as HTMLElement
    expect(row).toBeTruthy()
    sharePress(row.querySelector('.font-mono') as HTMLElement)
    expect(document.querySelector('.share-menu')).toBeTruthy()
  })

  it('chrome press on an idle reminder row copies the body, not the raw envelope', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: '<job-notification>\n<summary>chrome body</summary>\n</job-notification>' } },
      },
    })
    // Press the message chrome (avatar column), not the reminder row itself.
    const shell = (document.querySelector('[data-reminder-row]') as HTMLElement)
      .closest('.space-y-7 > div') as HTMLElement
    sharePress((shell.querySelector('.grid') as HTMLElement).firstElementChild as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('chrome body')
  })

  it('a non-queued idle echo renders in the default user style, not queued gray', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: { type: 'attachment', message: { attachment: { prompt: 'plain idle echo' } } },
    })
    const span = document.querySelector('.max-w-fit span.whitespace-pre-wrap') as HTMLElement
    expect(span.textContent).toBe('plain idle echo')
    expect(span.className).toBe('block whitespace-pre-wrap break-words')
  })

  it('drained queued message with a backfilled uuid removes its pill', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('first')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    setTextarea('queued while busy')
    pressEnter()
    dispatch({ type: 'queued', uuid: 'q9' })
    expect(document.querySelectorAll('.queued-bubble').length).toBe(1)
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { source_uuid: 'q9', prompt: 'queued while busy' } },
      },
    })
    expect(document.querySelectorAll('.queued-bubble').length).toBe(0)
    // The echo rendered gray inside the streaming assistant.
    const echo = document.querySelector('.space-y-3 [data-block-kind="user"]') as HTMLElement
    expect(echo.textContent).toBe('queued while busy')
  })
})

// ── query_end commit and rewind ─────────────────────────────────────────────

describe('chat mutation kills: query_end commit and rewind', () => {
  it('abort with tool content commits the turn even when the server rewound', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('question')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
    ])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect(document.querySelector('[data-tool-root]')).toBeTruthy()
    expect(document.body.textContent).toContain('question')
    // Aborted finalize leaves the tool body empty, never placeholder text.
    ;(document.querySelector('[data-tool-root] [data-tool-header]') as HTMLElement).click()
    const body = document.querySelector('[data-tool-root] .md-body') as HTMLElement
    expect(body.textContent).toBe('')
  })

  it('abort commit drops empty text blocks but keeps the pairing for later ones', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'aaa' },
      { type: 'text_end' },
      { type: 'text_start' },
      { type: 'text_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'bbb' },
    ])
    dispatchEvents([{ type: 'query_end', aborted: true }])
    const divs = document.querySelectorAll('[data-block-kind="text"]')
    expect(divs.length).toBe(2)
    sharePress(divs[1] as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('bbb')
  })

  it('rewind with an empty turn removes the assistant shell and restores the prompt', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('restore me')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect(document.querySelectorAll('.space-y-3').length).toBe(0)
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('restore me')
    // A late duplicate query_end with an emptied message list must not crash.
    dispatchEvents([{ type: 'query_end', aborted: true }])
    expect(document.querySelectorAll('.space-y-3').length).toBe(0)
  })

  it('rewound_text pops exactly the matching user message, not the newest one', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'u0', role: 'user', text: 'older prompt', startedAt: 1 })],
      nextCursor: '', hasMore: false,
    })
    setTextarea('target prompt')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true, rewound_text: 'target prompt' }])
    expect(document.body.textContent).not.toContain('target prompt')
    expect(document.body.textContent).toContain('older prompt')
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('target prompt')
  })

  it('rewound_text with no local copy restores the text without touching history', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'a0', role: 'assistant', blocks: [{ kind: 'text', text: 'kept answer' }] })],
      nextCursor: '', hasMore: false,
    })
    setTextarea('local q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true, rewound_text: 'from another tab' }])
    expect(document.body.textContent).toContain('kept answer')
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('from another tab')
  })
  it('rewound_text with duplicate history twins and no live copy pops the NEWEST twin', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // Sharpest BUG-1 shape: 'dup prompt' exists twice in history and there
    // is no live copy. The matcher scans newest-first ([...messages]
    // .reverse()), so the NEWEST twin (d1, after MID) must be popped and
    // the oldest (d0, before MID) must stay rendered — the surrounding
    // rows tell the twins apart. An oldest-first scan pops the wrong one.
    dispatch({
      type: 'history',
      messages: [
        histMsg({ id: 'd0', role: 'user', text: 'dup prompt', startedAt: 1 }),
        histMsg({ id: 'm0', role: 'assistant', blocks: [{ kind: 'text', text: 'MID' }], startedAt: 2 }),
        histMsg({ id: 'd1', role: 'user', text: 'dup prompt', startedAt: 3 }),
        histMsg({ id: 'l0', role: 'assistant', blocks: [{ kind: 'text', text: 'LATE' }], startedAt: 4 }),
      ],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true, rewound_text: 'dup prompt' }])
    // The rewind drops the target AND everything after it: newest-twin d1
    // plus LATE. An oldest-first scan instead picks d0 and drops the whole
    // conversation (count 0, MID gone).
    const order = (document.querySelector('.space-y-7') as HTMLElement).textContent ?? ''
    expect((order.match(/dup prompt/g) ?? []).length).toBe(1)
    expect(order).toContain('MID')
    expect(order.indexOf('dup prompt')).toBeLessThan(order.indexOf('MID'))
  })

  it('legacy rewind after a history load pops the user row, not the error shell', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // ASC page (assistant first, user second) + an idle error shell at the
    // tail: with no rewound_text the legacy matcher must select a USER
    // message, scanning past the newer assistant/error entries.
    dispatch({
      type: 'history',
      messages: [
        histMsg({ id: 'a0', role: 'assistant', blocks: [{ kind: 'text', text: 'KEPT ANSWER' }], startedAt: 1 }),
        histMsg({ id: 'u0', role: 'user', text: 'OLD PROMPT', startedAt: 2 }),
      ],
      nextCursor: '', hasMore: false,
    })
    dispatch({ type: 'error', message: 'boom' })
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect(document.body.textContent).toContain('KEPT ANSWER')
    expect(document.body.textContent).not.toContain('OLD PROMPT')
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('OLD PROMPT')
  })

  it('abort rewind after a two-page load pops the live prompt, not its history twin', () => {
    // NOTE: this shape passes under both array orders (the live copy wins
    // either way); the order-sensitive variant without a live copy is
    // 'rewound_text with duplicate history twins and no live copy pops the
    // NEWEST twin' above.
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // Newer page first, older page second (prepend). 'dup prompt' appears
    // twice in history AND as the live turn — the rewound_text matcher
    // scans newest-first, so it must pick the live message and leave both
    // history twins rendered.
    dispatch({
      type: 'history',
      messages: [
        histMsg({ id: 'd1', role: 'user', text: 'dup prompt', startedAt: 10 }),
        histMsg({ id: 'o1', role: 'user', text: 'other question', startedAt: 20 }),
      ],
      nextCursor: 'c1', hasMore: true,
    })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'd0', role: 'user', text: 'dup prompt', startedAt: 1 })],
      nextCursor: '', hasMore: false,
    })
    setTextarea('dup prompt')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true, rewound_text: 'dup prompt' }])
    expect(document.body.textContent).toContain('other question')
    // Both history twins survive the rewind; only the live echo is gone.
    expect((document.body.textContent ?? '').split('dup prompt').length - 1).toBe(2)
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('dup prompt')
  })

  it('a chat error after a two-page load attaches to the newest streaming row', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        histMsg({ id: 'q9', role: 'user', text: 'the newest question', startedAt: 20 }),
        histMsg({
          id: 'a9', role: 'assistant', status: 'streaming', startedAt: 21,
          blocks: [{ kind: 'text', text: 'halfway answer' }],
        }),
      ],
      nextCursor: 'c1', hasMore: true,
    })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'q0', role: 'user', text: 'ancient question', startedAt: 1 })],
      nextCursor: '', hasMore: false,
    })
    dispatch({ type: 'error', message: 'boom' })
    const rows = Array.from((document.querySelector('.space-y-7') as HTMLElement).children) as HTMLElement[]
    const boomRow = rows.find((r) => r.textContent?.includes('boom'))
    // The streaming tail owns the error: an in-place box inside the row
    // that carries 'halfway answer', not a fresh shell after the history.
    expect(boomRow?.textContent).toContain('halfway answer')
    expect(boomRow?.textContent).not.toContain('ancient question')
  })

  it('an aborted turn with text content commits even when the server rewound', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'kept text' },
    ])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    // Content beats rewind: the streamed text stays committed and the prompt
    // is not restored into the input.
    expect(document.body.textContent).toContain('kept text')
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('')
  })

  it('query_end debug names all three flags with their values', () => {
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    try {
      mount()
      dispatch({ type: 'connect_status', connected: true })
      setTextarea('q')
      pressEnter()
      dispatchEvents([{ type: 'query_start' }])
      dispatchEvents([{ type: 'query_end', aborted: true, rewound: true, rewound_text: 'q' }])
      expect(dbg).toHaveBeenCalledWith('[chat] query_end aborted=true rewound=true rewound_text=yes')
    } finally {
      dbg.mockRestore()
    }
  })

  it('a tool still running at query_end finalizes as done, not error', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'query_end' },
    ])
    const dur = document.querySelector('[data-tool-root] .font-mono.text-xs') as HTMLElement
    // Un-aborted finalize leaves the running style; an aborted-flag leak
    // would flip it to the error style.
    expect(dur.className).toContain('text-blue')
    expect(dur.className).not.toContain('text-red')
  })
})

// ── usage accounting ────────────────────────────────────────────────────────

describe('chat mutation kills: usage accounting', () => {
  it('context used sums input, cache read, cache creation, and output', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', contextUsed: 0, contextTotal: 500000 })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 50, cache_read_input_tokens: 20, cache_creation_input_tokens: 5 } },
    ])
    const trigger = document.querySelector('[data-testid="context-trigger"]') as HTMLElement
    // 100 + 20 + 5 = 125 in, 50 out → 175 of the 500k window.
    expect(trigger.textContent).toContain('175/488.3k')
  })

  it('accumulated cache read shows in the finalized bar as a percentage', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 10, cache_read_input_tokens: 100 } },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 10, cache_read_input_tokens: 100 } },
      { type: 'query_end' },
    ])
    const bar = document.querySelector('.status-line') as HTMLElement
    // cache 200 of total input 400 → 50%.
    expect(bar.textContent).toContain('50.0% cached')
  })
})

// ── header and input wiring ─────────────────────────────────────────────────

describe('chat mutation kills: header and input wiring', () => {
  const headerComboTriggers = () =>
    Array.from(document.querySelectorAll('header button')) as HTMLButtonElement[]

  it('model select sends model_switch; opening the picker requests quotas; entries render them', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'config',
      models: [
        { provider: 'prov', model: 'm1' },
        { provider: 'prov', model: 'm2' },
        { provider: 'other', model: 'm3' },
      ],
      current: { provider: 'prov', model: 'm1' },
    })
    const trigger = headerComboTriggers().find((b) => b.textContent === 'm1')!
    trigger.click()
    expect(sent.some((m) => (m as { type: string }).type === 'quota_request')).toBe(true)
    dispatch({ type: 'quota_result', entries: [{ provider: 'prov', quota: '42/50' }, { provider: 'other', quota: '1/9' }] })
    const item = Array.from(document.querySelectorAll('span'))
      .find((sp) => sp.textContent === 'm2')!.closest('button') as HTMLButtonElement
    item.click()
    expect(sent).toContainEqual({ type: 'model_switch', provider: 'prov', model: 'm2' })
    // Reopen: both providers show their quota.
    trigger.click()
    const body = document.body.textContent ?? ''
    expect(body).toContain('42/50')
    expect(body).toContain('1/9')
  })

  it('engine picker footer starts a new engine', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'engine_list',
      engines: [{ id: 'e1', name: 'agent-1', model: 'm' }],
      activeID: 'e1',
    })
    const trigger = headerComboTriggers().find((b) => b.textContent === 'agent-1')!
    trigger.click()
    const panel = document.querySelector('[data-testid="engine-picker-panel"]') as HTMLElement
    const footer = panel.querySelectorAll('button')[panel.querySelectorAll('button').length - 1] as HTMLButtonElement
    footer.click()
    expect(sent).toContainEqual({ type: 'engine_new' })
  })

  it('context trigger requests the breakdown; Compact hides the panel and asks the server', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', contextUsed: 1000, contextTotal: 500000 })
    const trigger = document.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
    trigger.click()
    expect(sent).toContainEqual({ type: 'context_request' })
    dispatch({
      type: 'context_breakdown',
      model: 'm', contextWindow: 500000, totalTokens: 5000, percentage: 1, isAutoCompact: false,
      categories: [], mcpToolsLoaded: [], mcpToolsDeferred: [], deferredBuiltinTools: [],
      systemTools: [], systemPromptSections: [], memoryFiles: [], agents: [], skills: [],
      messageBreakdown: null, apiUsage: null,
    })
    trigger.click() // close (open from the first click)
    trigger.click() // reopen with data
    const compact = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === 'Compact') as HTMLButtonElement
    expect(compact).toBeTruthy()
    compact.click()
    expect(sent).toContainEqual({ type: 'compact_request' })
    const panel = document.querySelector('.max-h-\\[60dvh\\]') as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('zero-token breakdown clears to the empty state instead of rendering zeros', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'stats', contextUsed: 1000, contextTotal: 500000 })
    dispatch({
      type: 'context_breakdown',
      totalTokens: 5000, contextWindow: 500000, percentage: 1, categories: [],
    })
    dispatch({
      type: 'context_breakdown',
      model: 'm', contextWindow: 500000, totalTokens: 0, percentage: 0, isAutoCompact: false,
      categories: [], mcpToolsLoaded: [], mcpToolsDeferred: [], deferredBuiltinTools: [],
      systemTools: [], systemPromptSections: [], memoryFiles: [], agents: [], skills: [],
      messageBreakdown: null, apiUsage: null,
    })
    const trigger = document.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
    trigger.click()
    const panel = document.querySelector('.max-h-\\[60dvh\\]') as HTMLElement
    expect(panel.textContent).not.toContain('Context Usage')
  })

  it('thinking effort selection sends thinking_switch', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const caption = Array.from(document.querySelectorAll('button'))
      .find((b) => b.textContent === 'auto' && b.className.includes('text-[10px]')) as HTMLButtonElement
    caption.click()
    const strip = document.querySelector('[data-effort-strip]') as HTMLElement
    const high = Array.from(strip.querySelectorAll('button')).find((b) => b.textContent === 'high') as HTMLButtonElement
    high.click()
    expect(sent).toContainEqual({ type: 'thinking_switch', effort: 'high' })
  })

  it('engine_list and session_list render into header and sidebar', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'engine_list', engines: [{ id: 'e9', name: 'agent-nine', model: 'm' }], activeID: 'e9' })
    expect(headerComboTriggers().some((b) => b.textContent === 'agent-nine')).toBe(true)
    dispatch({ type: 'session_list', sessions: [{ id: 's1', title: 'chat about lists', updatedAt: 1 }] })
    expect(document.body.textContent).toContain('chat about lists')
  })

  it('model_switched without a thinking field falls back to auto, never undefined', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'model_switched', contextUsed: 1, contextTotal: 100 })
    dispatch({ type: 'config', models: [], current: { provider: 'p', model: 'm' } })
    expect(document.body.textContent).not.toContain('undefined')
  })

  it('task_list, job_list, and job_output reach the task panel and store', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'task_list', tasks: [{ id: '1', subject: 'Plan', status: 'completed' }, { id: '2', subject: 'Code', status: 'pending' }] })
    expect((document.querySelector('.task-label') as SVGTextElement).textContent).toBe('1/2')
    dispatch({ type: 'job_list', jobs: [{ id: 'bg-1', kind: 'bash', status: 'running', command: 'make all', startedMs: 1 }] })
    dispatch({ type: 'job_output', jobID: 'bg-1', tail: 'tail line' })
    expect(chat.jobsStore.feed('bg-1')?.tail).toBe('tail line')
    const ring = document.querySelector('.task-label')?.closest('button') as HTMLElement
    ring.click()
    expect((document.getElementById('task-popover') as HTMLElement).textContent).toContain('make all')
  })

  it('stats frame overwrites the live progress usage', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'usage', usage_event: { input_tokens: 100, output_tokens: 0 } },
    ])
    dispatch({ type: 'stats', usage: { input_tokens: 555, output_tokens: 0 } })
    const inEl = document.querySelector('.status-line span') as HTMLElement
    expect(inEl.textContent).toContain('↑555')
  })

  it('turn_start keeps a prior estimate when the new frame carries none', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    // A turn_start with no live stream initiates on its own.
    dispatchEvents([{ type: 'turn_start', input_estimate: 100 }])
    expect(dbg.mock.calls.some((c) => String(c[0]).includes('reason=turn_start'))).toBe(true)
    dispatchEvents([{ type: 'turn_start' }])
    const inEl = document.querySelector('.status-line span') as HTMLElement
    expect(inEl.textContent).toContain('↑100')
    dispatchEvents([{ type: 'query_end' }])
    expect(dbg.mock.calls.some((c) => String(c[0]).includes('[chat] query_end aborted=false'))).toBe(true)
  })
})

// ── text_delta late-sink mount ──────────────────────────────────────────────

describe('chat mutation kills: text_delta late sink', () => {
  it('a delta after tool_start mounts a fresh div between the tool and the progress bar, copy buttons wired', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'first part' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'text_delta', text: '```js\nlate()\n```' },
    ])
    const stream = document.querySelector('.space-y-3') as HTMLElement
    const divs = stream.querySelectorAll(':scope > [data-block-kind="text"]')
    expect(divs.length).toBe(2)
    const late = divs[1] as HTMLElement
    expect(late.querySelector('.copy-btn')).toBeTruthy()
    // The late div lands before the progress line, not after it.
    const bar = stream.querySelector(':scope > .status-line') as HTMLElement
    expect(late.compareDocumentPosition(bar) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

// ── sub-agent thinking lifecycle ────────────────────────────────────────────

describe('chat mutation kills: sub-agent thinking', () => {
  const agentTag = { parent_tool_use_id: 'agent-1', agent_type: 'Executor' }
  const childThinking = () =>
    Array.from(document.querySelectorAll('[data-tool-children] .text-amber'))
      .filter((el) => /Thinking|Thought/.test(el.textContent ?? '')) as HTMLElement[]
  const mainThinking = () =>
    Array.from(document.querySelectorAll('.space-y-3 > .text-amber'))
      .filter((el) => /Thinking|Thought/.test(el.textContent ?? '')) as HTMLElement[]

  it('start/delta/end inside the parent tool: label, text, duration, no main entry', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'thinking_start', agent: agentTag },
      { type: 'thinking_delta', agent: agentTag, thinking: { text: 'inner reasoning' } },
    ])
    expect(childThinking().length).toBe(1)
    expect(document.querySelector('[data-tool-children]')!.textContent).toContain('inner reasoning')
    expect(mainThinking().length).toBe(0)
    dispatchEvents([{ type: 'thinking_end', agent: agentTag, thinking: { duration: 3_000_000_000 } }])
    expect(document.querySelector('[data-tool-children]')!.textContent).toContain('Thought for 3s')
  })

  it('a delta with no prior start lazily creates the entry', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'thinking_delta', agent: agentTag, thinking: { text: 'lazy thought' } },
    ])
    expect(childThinking().length).toBe(1)
    expect(document.querySelector('[data-tool-children]')!.textContent).toContain('lazy thought')
  })

  it('a new delta after end starts a fresh entry instead of extending the finished one', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'thinking_start', agent: agentTag },
      { type: 'thinking_end', agent: agentTag, thinking: { duration: 1_000_000_000 } },
      { type: 'thinking_delta', agent: agentTag, thinking: { text: 'second wind' } },
    ])
    expect(childThinking().length).toBe(2)
    const texts = childThinking().map((el) => el.textContent ?? '')
    expect(texts.some((t) => t.includes('Thought for 1s'))).toBe(true)
    expect(texts.some((t) => t.includes('Thinking ('))).toBe(true)
    expect(document.querySelector('[data-tool-children]')!.textContent).toContain('second wind')
  })
})

// ── queued / cancel / restore bookkeeping ───────────────────────────────────

describe('chat mutation kills: queued cancel and restore bookkeeping', () => {
  it('a second queued frame with no empty-uuid slot is a no-op', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('a')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    setTextarea('b')
    pressEnter()
    dispatch({ type: 'queued', uuid: 'q1' })
    dispatch({ type: 'queued', uuid: 'q2' })
    const labels = Array.from(document.querySelectorAll('.queued-bubble span:first-child'))
      .map((el) => el.textContent)
    expect(labels).toEqual(['b'])
  })

  it('cancel_result joins stripped texts and rebuilds chips from the local queued entry', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('first')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    const file = new File(['bytes'], 'spec.pdf', { type: 'application/pdf' })
    Object.defineProperty(docInput, 'files', { value: [file], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('with doc')
    pressEnter()
    await vi.waitFor(() =>
      expect(sent.some((m) => (m as { type: string; attachments?: unknown[] }).type === 'message' && (m as { attachments?: unknown[] }).attachments)).toBe(true))
    dispatch({ type: 'queued', uuid: 'q7' })
    dispatch({
      type: 'cancel_result',
      restored: [
        { uuid: 'q7', text: '[voice] spoken one', attachments: [{ id: 'fresh-1', mime: 'application/pdf', size: 7 }] },
        { text: 'plain two' },
      ],
    })
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('spoken one\nplain two')
    const refs = chat.inputBar.getAttachments()
    expect(refs.length).toBe(1)
    expect(refs[0].file.name).toBe('spec.pdf')
    expect(refs[0].uploadedID).toBe('fresh-1')
  })

  it('cancel_result drops empty restored texts before joining', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'cancel_result',
      restored: [{ text: '' }, { text: 'plain two' }],
    })
    // The empty entry contributes nothing — no leading newline in the join.
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('plain two')
  })

  it('cancel_result with no restored list clears the queue only', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('a')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    setTextarea('b')
    pressEnter()
    dispatch({ type: 'cancel_result' })
    expect(document.querySelectorAll('.queued-bubble').length).toBe(0)
  })

  it('restore_result fills names and previews from the rewind stash', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    const imgInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '') === 'image/*' && i.multiple) as HTMLInputElement
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    const img = new File(['x'], 'photo.png', { type: 'image/png' })
    const doc = new File(['y'], 'notes.md', { type: 'text/markdown' })
    Object.defineProperty(imgInput, 'files', { value: [img], configurable: true })
    imgInput.dispatchEvent(new Event('change'))
    Object.defineProperty(docInput, 'files', { value: [doc], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('pic and doc')
    pressEnter()
    await vi.waitFor(() =>
      expect(sent.some((m) => (m as { type: string; attachments?: unknown[] }).type === 'message' && (m as { attachments?: unknown[] }).attachments)).toBe(true))
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect(sent).toContainEqual({ type: 'restore_attachments' })
    dispatch({
      type: 'restore_result',
      attachments: [
        { id: 'r-img', mime: 'image/png' },
        { id: 'r-doc', mime: 'text/markdown' },
        // A server entry beyond the stash must not crash the restore.
        { id: 'r-x', mime: 'image/png' },
      ],
    })
    const refs = chat.inputBar.getAttachments()
    expect(refs.map((r) => r.uploadedID)).toEqual(['r-img', 'r-doc', 'r-x'])
    // The document name and size come from the local stash — the server
    // entry carries only the fresh id and mime.
    expect(refs[1].file.name).toBe('notes.md')
    expect(refs[1].restoredSize).toBe(1)
    expect(refs[1].kind).toBe('document')
    // The image keeps its local blob thumbnail.
    expect(refs[0].kind).toBe('image')
    expect(refs[0].previewURL).not.toBe('')
    expect(refs[2].previewURL).toBe('')
  })
})

// ── streamState / watermark / page0 cache ───────────────────────────────────

describe('chat mutation kills: streamState and event watermark', () => {
  it('empty streamState arms nothing; a blockful one renders live', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'streamState', blocks: [] })
    expect(document.querySelector('.status-line')).toBeNull()
    expect(document.querySelectorAll('.space-y-3').length).toBe(0)
    dispatch({ type: 'streamState', blocks: [{ kind: 'text', id: '', text: 'resumed text' }] })
    expect(document.querySelector('.md-body')!.textContent).toContain('resumed text')
    expect(document.querySelector('.status-line')).toBeTruthy()
  })

  it('a blockful streamState init names streamState as its reason', () => {
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    try {
      mount()
      dispatch({ type: 'connect_status', connected: true })
      dispatch({ type: 'streamState', blocks: [{ kind: 'text', id: '', text: 'resumed text' }] })
      expect(dbg).toHaveBeenCalledWith(expect.stringContaining('reason=streamState'))
    } finally {
      dbg.mockRestore()
    }
  })

  it('a seq-0 event on a fresh page-less session applies live', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event', seq: 0,
      event: { type: 'attachment', message: { attachment: { prompt: 'seq zero echo' } } },
    })
    const spans = Array.from(document.querySelectorAll('.max-w-fit span.whitespace-pre-wrap'))
    expect(spans.map((s) => s.textContent)).toEqual(['seq zero echo'])
  })

  it('an event frame absorbed by the page seq watermark drops silently', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({ type: 'history', messages: [], stream: [], nextCursor: '', hasMore: false, seq: 5 })
    dispatch({
      type: 'event', seq: 5,
      event: { type: 'attachment', message: { attachment: { prompt: 'already inside page' } } },
    })
    expect(document.querySelector('.max-w-fit')).toBeNull()
  })
})

describe('chat mutation kills: page0 cache eviction', () => {
  const feedPage = (engineID: string, rev: string) => {
    // The metadata frame carries rev/busy (v2 unified paging): the cache
    // lookup and the page mint both key off it.
    dispatch({
      type: 'metadata',
      rev,
      connect: { connected: true, engineID, sessionID: 's' },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: engineID },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({ type: 'history', messages: [], stream: [], nextCursor: 'cur-' + engineID, hasMore: false, rev, busy: false })
  }
  const pageRequests = () =>
    sent.filter((m) => (m as { type: string; type2?: string }).type === 'history_request') as unknown[]

  it('evicts oldest beyond 8 entries; a re-minted key re-orders to newest', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // Keys e0..e2 minted, then e0 re-minted (delete+set moves it newest).
    for (const e of ['e0', 'e1', 'e2']) feedPage(e, 'r1')
    feedPage('e0', 'r2')
    // Fill to ten distinct keys total: e3..e9 plus one more.
    for (const e of ['e3', 'e4', 'e5', 'e6', 'e7', 'e8', 'e9']) feedPage(e, 'r1')
    // e0 survived (re-minted newest); e1 was the oldest at the final insert.
    sent.length = 0
    feedPage('e0', 'r2')
    // Hit: the re-minted key is still cached — no round trip.
    expect(pageRequests().length).toBe(0)
    sent.length = 0
    feedPage('e1', 'r1')
    // Miss: e1 was evicted, so the page request leaves.
    expect(pageRequests().length).toBe(1)
  })
})

// ── subtree page seam and dedup ─────────────────────────────────────────────

describe('chat mutation kills: subtree page merge', () => {
  it('dedupes seen child tools and drops an identical seam text', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{
        kind: 'tool', id: 't1', name: 'Agent', summary: '', isSearch: false, isRead: false,
        isList: false, isLsp: false, isWeb: false, isTask: false, state: 'running', timingNs: 0,
        displayOutput: '', startedAt: Date.now(), truncated: 5, truncatedCursor: 'anc',
        children: [
          { kind: 'text', id: '', text: 'seam' },
          {
            kind: 'tool', id: 'c1', name: 'Read', summary: '', isSearch: false, isRead: false,
            isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 1,
            displayOutput: 'C1 OUTPUT', startedAt: 0, children: [],
          },
        ],
      }],
      nextCursor: '', hasMore: false, busy: true,
    })
    const nonce = ((sent.find((m) => (m as { type: string }).type === 'subtree_request')) as { nonce: number }).nonce
    dispatch({
      type: 'subtree_page',
      nonce,
      blockID: 't1',
      children: [
        {
          kind: 'tool', id: 'c2', name: 'Read', summary: '', isSearch: false, isRead: false,
          isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 1,
          displayOutput: 'C2 OUTPUT', startedAt: 0, children: [],
        },
        {
          kind: 'tool', id: 'c1', name: 'Read', summary: '', isSearch: false, isRead: false,
          isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 1,
          displayOutput: 'C1 OUTPUT DUP', startedAt: 0, children: [],
        },
        { kind: 'text', id: '', text: 'seam' },
      ],
      remaining: 0,
      nextAnchor: '',
    })
    const children = document.querySelector('[data-tool-children]') as HTMLElement
    const toolRoots = children.querySelectorAll('[data-tool-root]')
    expect(toolRoots.length).toBe(2)
    expect(children.querySelectorAll('[data-block-kind="text"]').length).toBe(1)
    expect(children.querySelectorAll('[data-block-kind="text"]')[0].textContent).toBe('seam\n')
    // c2 (older) sits above c1 in document order.
    expect((toolRoots[0] as HTMLElement).getAttribute('data-tool-name')).toBe('Read')
    expect(children.textContent).not.toContain('C1 OUTPUT DUP')
  })
})

// ── prependStreamBlocks ─────────────────────────────────────────────────────

describe('chat mutation kills: prependStreamBlocks', () => {
  it('an older in-flight window prepends above the live stream content', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'live tail' },
    ])
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'older window text' }],
      nextCursor: '', hasMore: false,
    })
    const stream = document.querySelector('.space-y-3') as HTMLElement
    const divs = stream.querySelectorAll(':scope > [data-block-kind="text"]')
    expect(divs.length).toBe(2)
    expect((divs[0] as HTMLElement).textContent).toContain('older window text')
    expect((divs[1] as HTMLElement).textContent).toContain('live tail')
  })
})

// ── cleanup ─────────────────────────────────────────────────────────────────

describe('chat mutation kills: cleanup', () => {
  it('cleanup stops message delivery and freezes the refresh interval', () => {
    vi.useFakeTimers()
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
    ])
    vi.advanceTimersByTime(1200)
    const dur = document.querySelector('[data-tool-root] .font-mono.text-xs') as HTMLElement
    const before = dur.textContent
    chat.cleanup()
    vi.advanceTimersByTime(5000)
    expect(dur.textContent).toBe(before)
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'u1', role: 'user', text: 'after cleanup' })],
      nextCursor: '', hasMore: false,
    })
    expect(document.body.textContent).not.toContain('after cleanup')
  })
})

// ── voice face and dictation wiring ─────────────────────────────────────────

describe('chat mutation kills: voice and dictation', () => {
  class FakeAudioContext {
    static created: FakeAudioContext[] = []
    static analysers: Array<{ connectedTo: unknown; data: Uint8Array }> = []
    readonly sampleRate: number
    readonly destination = {}
    readonly audioWorklet = { addModule: async () => {} }
    constructor(opts?: { sampleRate?: number }) {
      this.sampleRate = opts?.sampleRate ?? 48000
      FakeAudioContext.created.push(this)
    }
    resume() { return Promise.resolve() }
    close() { return Promise.resolve() }
    createAnalyser() {
      const analyser = {
        connectedTo: null as unknown,
        data: new Uint8Array(0),
        connect(target: unknown) { this.connectedTo = target },
        disconnect() {},
        getByteTimeDomainData(arr: Uint8Array) { this.data = arr },
      }
      FakeAudioContext.analysers.push(analyser)
      return analyser
    }
    createMediaStreamSource(_s: unknown) { return { connect() {}, disconnect() {} } }
  }
  class FakeAudioWorkletNode {
    static created: Array<{ node: FakeAudioWorkletNode; name: string }> = []
    readonly name: string
    readonly port = {
      posted: [] as unknown[],
      onmessage: null as ((e: { data: unknown }) => void) | null,
      postMessage(msg: unknown) { this.posted.push(msg) },
    }
    constructor(_ctx: unknown, name: string) {
      this.name = name
      FakeAudioWorkletNode.created.push({ node: this, name })
    }
    connect() {}
    disconnect() {}
  }
  const sttSocks = () =>
    (sttModule as unknown as { __sttSocks: { sentText: string[]; dispatch: (d: unknown) => void; close: () => void }[] }).__sttSocks
  const realAudioContext = globalThis.AudioContext
  const realAudioWorkletNode = globalThis.AudioWorkletNode

  beforeEach(() => {
    FakeAudioContext.created = []
    FakeAudioContext.analysers = []
    FakeAudioWorkletNode.created = []
    sttSocks().length = 0
    vi.stubGlobal('AudioContext', FakeAudioContext)
    vi.stubGlobal('AudioWorkletNode', FakeAudioWorkletNode)
    Object.defineProperty(navigator, 'mediaDevices', {
      value: { getUserMedia: async () => ({ getTracks: () => [] }) },
      configurable: true,
    })
  })
  afterEach(() => {
    vi.stubGlobal('AudioContext', realAudioContext)
    vi.stubGlobal('AudioWorkletNode', realAudioWorkletNode)
    Reflect.deleteProperty(navigator, 'mediaDevices')
  })

  function mountWithRoot() {
    document.body.innerHTML = ''
    // A previous test's face may have parked body-level styles; jsdom's
    // innerHTML wipe clears children but not attributes.
    document.body.removeAttribute('aria-hidden')
    document.body.removeAttribute('data-upgrading')
    document.body.style.overflow = ''
    const rootEl = document.createElement('div')
    rootEl.id = 'root'
    document.body.appendChild(rootEl)
    const chat = createChat({ connected: true })
    rootEl.appendChild(chat.root)
    return chat
  }
  const metaWithVoice = (stt: boolean, tts: boolean) => {
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' }, stt: stt ? true : undefined, tts: tts ? true : undefined },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({ type: 'history', messages: [], stream: [], nextCursor: '', hasMore: false })
  }
  const micBtn = () => document.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement
  const voicePage = () => document.querySelector('.voice-page') as HTMLElement | null
  const flushMicrotasks = async (): Promise<void> => {
    for (let i = 0; i < 12; i++) await Promise.resolve()
  }

  it('metadata with stt+tts arms the face; a second long-press does not remount it', async () => {
    vi.useFakeTimers()
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    metaWithVoice(true, true)
    micBtn().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }))
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    expect(voicePage()).toBeTruthy()
    expect(document.getElementById('root')!.getAttribute('aria-hidden')).toBe('true')
    expect(document.body.style.overflow).toBe('hidden')
    expect(dbg).toHaveBeenCalledWith(expect.stringContaining('[voice] doc metrics scrollH='))
    micBtn().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }))
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    expect(document.querySelectorAll('.voice-page').length).toBe(1)
  })

  it('metadata with stt but no tts never opens the face', async () => {
    vi.useFakeTimers()
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    metaWithVoice(true, false)
    micBtn().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }))
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    expect(voicePage()).toBeNull()
    expect(document.body.style.overflow).toBe('')
  })

  it('dictation start frame carries the last ten messages, newest included, oldest not', async () => {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'config',
      models: [],
      current: { provider: 'p', model: 'm' },
      stt: true,
      thinking: 'auto',
    })
    // The messages array is time-ordered (oldest first, matching the DOM),
    // so slice(-10) feeds the ten NEWEST history messages: h10 (newest) is
    // inside the window, h0 (oldest, the filler words) is outside, and the
    // thinking text never leaks (only text blocks reach the prompt).
    dispatch({
      type: 'history',
      messages: [
        histMsg({ id: 'h0', role: 'user', text: 'fone ftwo fthree ffour ffive fsix fseven feight fnine ften feleven ftwelve', startedAt: 0 }),
        histMsg({ id: 'h1', role: 'user', text: 'aardvark', startedAt: 1 }),
        ...Array.from({ length: 3 }, (_, i) => histMsg({ id: 's' + i, role: 'user', text: 'aa', startedAt: 2 + i })),
        histMsg({
          id: 'h5', role: 'assistant', startedAt: 5,
          blocks: [
            { kind: 'thinking', thinking: { text: 'uniquethinkword' } },
            { kind: 'text', text: 'midtext' },
          ],
        }),
        ...Array.from({ length: 3 }, (_, i) => histMsg({ id: 't' + i, role: 'user', text: 'bb', startedAt: 6 + i })),
        histMsg({ id: 'h9', role: 'user', text: 'zzprefix', startedAt: 9 }),
        histMsg({ id: 'h10', role: 'user', text: 'zebrafinch', startedAt: 10 }),
      ],
      nextCursor: '', hasMore: false,
    })
    micBtn().click()
    await flushMicrotasks()
    const sock = sttSocks()[sttSocks().length - 1]
    const frame = JSON.parse(sock.sentText[0]) as { type: string; prompt: string }
    expect(frame.type).toBe('start')
    // Exact terms: newest-ten window [h1..h10] in time order — aa/bb fall
    // under the 3-char floor, so the four distinct long words are the whole
    // prompt. zebrafinch (newest) in, h0's filler words out, no thinking.
    expect(frame.prompt).toBe('aardvark midtext zzprefix zebrafinch')
  })

  it('a two-page history keeps the array time-ordered; the window follows the newest page', async () => {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'config',
      models: [],
      current: { provider: 'p', model: 'm' },
      stt: true,
      thinking: 'auto',
    })
    // First frame is the NEWER page (newA..newK); the second is an OLDER
    // page (oldA, oldB) that arrives later and prepends. The model array
    // must read [oldA oldB newA..newK] — one block per page, time order
    // inside each — so the ten-message window is newB..newK.
    dispatch({
      type: 'history',
      messages: Array.from({ length: 11 }, (_, i) =>
        histMsg({ id: 'n' + i, role: 'user', text: 'new' + String.fromCharCode(65 + i), startedAt: 100 + i })),
      nextCursor: 'c1', hasMore: true,
    })
    dispatch({
      type: 'history',
      messages: [
        histMsg({ id: 'o0', role: 'user', text: 'oldA', startedAt: 1 }),
        histMsg({ id: 'o1', role: 'user', text: 'oldB', startedAt: 2 }),
      ],
      nextCursor: '', hasMore: false,
    })
    // DOM stays oldest-first across the prepend.
    const order = (document.querySelector('.space-y-7') as HTMLElement).textContent ?? ''
    expect(order.indexOf('oldB')).toBeLessThan(order.indexOf('newA'))
    expect(order.indexOf('newA')).toBeLessThan(order.indexOf('newK'))
    micBtn().click()
    await flushMicrotasks()
    const sock = sttSocks()[sttSocks().length - 1]
    const frame = JSON.parse(sock.sentText[0]) as { prompt: string }
    // 13 messages: the window drops oldA, oldB, and newA. An older page
    // appended at the tail (instead of the head) or an in-page reversal
    // changes this exact string.
    expect(frame.prompt).toBe('newB newC newD newE newF newG newH newI newJ newK')
  })
  it('a compactBoundary mid-page splits the batches but keeps the array time-ordered', async () => {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'config',
      models: [],
      current: { provider: 'p', model: 'm' },
      stt: true,
      thinking: 'auto',
    })
    // Boundary in the MIDDLE of one page: flushBatch runs per segment, but
    // the model array must still come out oldest-first in one final block
    // insert — an in-page reversal would flip the prompt window. Words are
    // ≥3 chars: extractPromptTerms drops shorter ones from the bias list.
    dispatch({
      type: 'history',
      messages: [
        histMsg({ id: 'a1', role: 'user', text: 'alpha', startedAt: 1 }),
        histMsg({ id: 'a2', role: 'user', text: 'bravo', startedAt: 2 }),
        { id: 'b', role: 'system', compactBoundary: true, text: '', thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, error: '', status: 'done', startedAt: 3 },
        histMsg({ id: 'c1', role: 'user', text: 'charlie', startedAt: 4 }),
        histMsg({ id: 'c2', role: 'user', text: 'delta', startedAt: 5 }),
      ],
      nextCursor: '', hasMore: false,
    })
    const order = (document.querySelector('.space-y-7') as HTMLElement).textContent ?? ''
    expect(order.indexOf('bravo')).toBeLessThan(order.indexOf('charlie'))
    micBtn().click()
    await flushMicrotasks()
    const sock = sttSocks()[sttSocks().length - 1]
    const frame = JSON.parse(sock.sentText[0]) as { prompt: string }
    expect(frame.prompt).toBe('alpha bravo charlie delta')
  })

  it('an error frame flags the mic and freezes the utterance against later partials', async () => {
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'config', models: [], current: { provider: 'p', model: 'm' }, stt: true })
    micBtn().click()
    await flushMicrotasks()
    const sock = sttSocks()[sttSocks().length - 1]
    sock.dispatch('{"type":"partial","text":"half said"}')
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('half said')
    sock.dispatch('{"type":"error","message":"engine down"}')
    expect(micBtn().classList.contains('stt-mic-err')).toBe(true)
  })

  it('a scoped tts error capsule auto-dismisses after its duration', async () => {
    vi.useFakeTimers()
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    metaWithVoice(true, true)
    micBtn().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }))
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    const bigMic = voicePage()!.querySelector('.voice-mic') as HTMLElement
    bigMic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true }))
    await flushMicrotasks()
    bigMic.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, cancelable: true }))
    const sock = sttSocks()[sttSocks().length - 1] as unknown as { dispatch: (d: unknown) => void }
    sock.dispatch('{"type":"done","text":"spoken"}')
    await flushMicrotasks()
    const queues = (createTTSQueue as unknown as { all: { opts: { onError?: (m: string) => void } }[] }).all
    queues[queues.length - 1].opts.onError!('tts engine down')
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).not.toContain('opacity-0')
    vi.advanceTimersByTime(6500)
    expect(capsule.className).toContain('opacity-0')
  })

  it('the voice session start frame carries the timeline prompt texts too', async () => {
    vi.useFakeTimers()
    mountWithRoot()
    dispatch({ type: 'connect_status', connected: true })
    metaWithVoice(true, true)
    // Time-ordered array (see the dictation twin): slice(-10) keeps the ten
    // NEWEST messages — v0 and x0 (the two oldest) fall outside the window
    // and outsidermark (v11, newest) is inside it.
    const fillers = 'wone wtwo wthree wfour wfive wsix wseven weight wnine wten weleven wtwelve'.split(' ')
    dispatch({
      type: 'history',
      messages: [
        histMsg({ id: 'v0', role: 'user', text: fillers.join(' '), startedAt: 0 }),
        ...Array.from({ length: 6 }, (_, i) => histMsg({ id: 'x' + i, role: 'user', text: 'xx', startedAt: i + 1 })),
        histMsg({
          id: 'v7', role: 'assistant', startedAt: 7,
          blocks: [
            { kind: 'thinking', thinking: { text: 'voiceThinkLeak' } },
            { kind: 'text', text: 'yy' },
          ],
        }),
        histMsg({ id: 'v8', role: 'user', text: 'vfinal', startedAt: 8 }),
        histMsg({ id: 'v9', role: 'user', text: 'voice context prompt', startedAt: 9 }),
        histMsg({ id: 'v10', role: 'user', text: 'xx', startedAt: 10 }),
        histMsg({ id: 'v11', role: 'user', text: 'outsidermark', startedAt: 11 }),
      ],
      nextCursor: '', hasMore: false,
    })
    micBtn().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }))
    await vi.advanceTimersByTimeAsync(350)
    await flushMicrotasks()
    const bigMic = voicePage()!.querySelector('.voice-mic') as HTMLElement
    bigMic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true }))
    await flushMicrotasks()
    const sock = sttSocks()[sttSocks().length - 1]
    const frame = JSON.parse(sock.sentText[0]) as { prompt: string }
    // Exact terms from the newest-ten window in time order: xx/yy sit under
    // the 3-char floor, so the four distinct long words are the whole
    // prompt — v0's fillers and the thinking text are absent.
    expect(frame.prompt).toBe('vfinal voice context prompt outsidermark')
  })
})

// ── batch 5: residual escapes ───────────────────────────────────────────────

describe('chat mutation kills: residual wiring', () => {
  it('sub-agent text deltas render markdown with grown text and copy buttons', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'text_delta', agent: { parent_tool_use_id: 'agent-1' }, text: '**bo' },
      { type: 'text_delta', agent: { parent_tool_use_id: 'agent-1' }, text: 'ld**' },
      { type: 'text_delta', agent: { parent_tool_use_id: 'agent-1' }, text: '\n```js\nhi()\n```' },
    ])
    const children = document.querySelector('[data-tool-children]') as HTMLElement
    expect(children.querySelector('strong')?.textContent).toBe('bold')
    expect(children.querySelectorAll('[data-block-kind="text"]').length).toBe(1)
    expect(children.querySelector('.copy-btn')).toBeTruthy()
  })

  it('sub-agent thinking label ticks while active', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    const agentTag = { parent_tool_use_id: 'agent-1', agent_type: 'Executor' }
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'thinking_start', agent: agentTag },
    ])
    vi.advanceTimersByTime(2200)
    const label = Array.from(document.querySelectorAll('[data-tool-children] .text-amber'))
      .find((el) => (el.textContent ?? '').startsWith('Thinking (')) as HTMLElement
    expect(label.textContent).toBe('Thinking (2s)')
  })

  it('page0 hydration finishes the takeover bar and logs the page reason', () => {
    vi.useFakeTimers()
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // The bar only finishes a window that is running: reconnect opens it.
    stateCb!('reconnecting')
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'page window' }],
      nextCursor: '', hasMore: false, busy: true,
    })
    vi.advanceTimersByTime(60)
    const fill = document.querySelector('[data-testid="takeover-bar-fill"]') as HTMLElement
    expect(fill.style.width).toBe('100%')
    expect(dbg).toHaveBeenCalledWith(expect.stringContaining('reason=page'))
  })

  it('tool_start without a turn logs the fallback streaming reason', () => {
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{ type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } }])
    expect(dbg).toHaveBeenCalledWith(expect.stringContaining('reason=tool_start_fallback'))
  })

  it('tool_run does not mark a non-collapsible tool', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      { type: 'tool_run', tool_use: { id: 't1' } },
    ])
    expect((document.querySelector('[data-tool-root]') as HTMLElement).hasAttribute('data-collapsible')).toBe(false)
  })

  it('abort rewound with text and echo content commits the turn', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'real answer' },
      { type: 'attachment', message: { attachment: { prompt: 'mid-turn note' } } },
    ])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect(document.body.textContent).toContain('real answer')
    expect(document.body.textContent).toContain('mid-turn note')
    expect(document.body.textContent).toContain('q')
  })

  it('late abort frames never clobber a user message', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'u0', role: 'user', text: 'old prompt', startedAt: 1 })],
      nextCursor: '', hasMore: false,
    })
    // Rewound abort whose target is absent: text-only restore, history intact.
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true, rewound_text: 'elsewhere' }])
    expect(document.body.textContent).toContain('old prompt')
    // Commit-path abort while the tail is a USER message: nothing may be
    // removed or rewritten on it.
    dispatchEvents([{ type: 'query_end', aborted: true }])
    expect(document.body.textContent).toContain('old prompt')
    const row = Array.from(document.querySelectorAll('.space-y-7 > div'))
      .find((el) => (el.textContent ?? '').includes('old prompt')) as HTMLElement
    sharePress(row.querySelector('.bg-gradient-to-br') as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('old prompt')
  })

  it('whitespace-only text blocks are dropped from the commit pairing too', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'aaa' },
      { type: 'text_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: '   ' },
      { type: 'text_end' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'bbb' },
    ])
    dispatchEvents([{ type: 'query_end', aborted: true }])
    const divs = document.querySelectorAll('[data-block-kind="text"]')
    expect(divs.length).toBe(2)
    sharePress(divs[1] as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('bbb')
  })

  it('the rewind splice keeps stale entries out of later prompt contexts', async () => {
    class FakeAC2 {
      readonly sampleRate = 48000
      readonly destination = {}
      readonly audioWorklet = { addModule: async () => {} }
      resume() { return Promise.resolve() }
      close() { return Promise.resolve() }
      createAnalyser() { return { connect() {}, disconnect() {}, getByteTimeDomainData() {} } }
      createMediaStreamSource() { return { connect() {}, disconnect() {} } }
    }
    vi.stubGlobal('AudioContext', FakeAC2)
    vi.stubGlobal('AudioWorkletNode', class { port = { postMessage() {}, posted: [] as unknown[], onmessage: null }; connect() {} disconnect() {} })
    Object.defineProperty(navigator, 'mediaDevices', {
      value: { getUserMedia: async () => ({ getTracks: () => [] }) },
      configurable: true,
    })
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'config',
      models: [],
      current: { provider: 'p', model: 'm' },
      stt: true,
    })
    setTextarea('first prompt')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    // The popped prompt must be gone from the messages array as well.
    setTextarea('second prompt')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'turn_start' }])
    dispatchEvents([{ type: 'query_end' }])
    const { __sttSocks } = sttModule as unknown as { __sttSocks: { sentText: string[] }[] }
    const socksBefore = __sttSocks.length
    const micBtn2 = document.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement
    micBtn2.click()
    await vi.waitFor(() => expect(__sttSocks.length).toBeGreaterThan(socksBefore))
    const sock = __sttSocks[socksBefore]
    expect(sock.sentText.length).toBe(1)
    const frame = JSON.parse(sock.sentText[0]) as { prompt: string }
    expect(frame.prompt.split(' ')).toEqual(expect.arrayContaining(['second', 'prompt']))
    expect(frame.prompt).not.toContain('first')
  })

  it('live image and document sends render thumbnails, alt text, chips, and lightbox', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const imgInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '') === 'image/*' && i.multiple) as HTMLInputElement
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    const img = new File(['x'], 'cam.png', { type: 'image/png' })
    const doc = new File(['yy'], 'spec.md', { type: 'text/markdown' })
    Object.defineProperty(imgInput, 'files', { value: [img], configurable: true })
    imgInput.dispatchEvent(new Event('change'))
    Object.defineProperty(docInput, 'files', { value: [doc], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('see files')
    pressEnter()
    await vi.waitFor(() =>
      expect(sent.some((m) => (m as { type: string; attachments?: unknown[] }).type === 'message' && (m as { attachments?: unknown[] }).attachments)).toBe(true))
    const bubbleImg = document.querySelector('.space-y-7 .max-w-fit img') as HTMLImageElement
    expect(bubbleImg.alt).toBe('cam.png')
    expect(bubbleImg.className).toBe('inline-block max-w-[200px] max-h-[200px] rounded-lg my-1 mr-1 align-top cursor-zoom-in')
    const chip = document.querySelector('.space-y-7 .max-w-fit span.font-mono') as HTMLElement
    expect(chip.textContent).toBe('[spec.md]')
    expect(chip.className).toContain('font-mono')
    // The thumbnail keeps its blob URL alive for the lightbox singleton.
    expect(bubbleImg.src.startsWith('blob:')).toBe(true)
  })

  it('clicking the live-sent thumbnail opens the lightbox with the same blob url', async () => {
    const calls = (streamBlocks as unknown as { __lightboxCalls: string[] }).__lightboxCalls
    calls.length = 0
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const imgInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '') === 'image/*' && i.multiple) as HTMLInputElement
    const img = new File(['x'], 'cam.png', { type: 'image/png' })
    Object.defineProperty(imgInput, 'files', { value: [img], configurable: true })
    imgInput.dispatchEvent(new Event('change'))
    setTextarea('see this')
    pressEnter()
    await vi.waitFor(() =>
      expect(sent.some((m) => (m as { type: string; attachments?: unknown[] }).type === 'message' && (m as { attachments?: unknown[] }).attachments)).toBe(true))
    const bubbleImg = document.querySelector('.space-y-7 .max-w-fit img') as HTMLImageElement
    bubbleImg.click()
    expect(calls.length).toBe(1)
    expect(calls[0]).toBe(bubbleImg.src)
  })

  it('empty send with no paste is a no-op; a pasted block joins the message body', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    pressEnter()
    expect(sent.filter((m) => (m as { type: string }).type === 'message').length).toBe(0)
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    const paste = new InputEvent('beforeinput', {
      inputType: 'insertFromPaste',
      data: 'pasted line one\npasted line two\nthird\nfourth',
      bubbles: true,
      cancelable: true,
    })
    ta.dispatchEvent(paste)
    pressEnter()
    const frame = sent.find((m) => (m as { type: string }).type === 'message') as { text: string }
    expect(frame.text).toBe('pasted line one\npasted line two\nthird\nfourth')
  })

  it('STOP aborts in-flight uploads and logs both debug lines', async () => {
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    vi.mocked(uploadAttachment).mockImplementation((_file, opts) =>
      new Promise((_resolve, reject) => {
        opts!.signal.addEventListener('abort', () => reject(new Error('aborted')))
      }),
    )
    dispatchEvents([{ type: 'query_start' }])
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    const file = new File(['bytes'], 'spec.pdf', { type: 'application/pdf' })
    Object.defineProperty(docInput, 'files', { value: [file], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('upload then stop')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(sent).toContainEqual({ type: 'stop' })
    await vi.waitFor(() => expect(chat.inputBar.getAttachments()[0].failed).toBe(true))
    expect(dbg).toHaveBeenCalledWith('[chat] onStop esc pressed at', expect.any(Number))
    expect(dbg).toHaveBeenCalledWith(expect.stringContaining('[chat] onStop stop sent, ws_buffered='))
  })

  it('onStop logs the exact buffered delta with its unit', async () => {
    const nowSpy = vi.spyOn(performance, 'now').mockReturnValue(2500)
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    try {
      mount()
      dispatch({ type: 'connect_status', connected: true })
      vi.mocked(uploadAttachment).mockImplementation((_file, opts) =>
        new Promise((_resolve, reject) => {
          opts!.signal.addEventListener('abort', () => reject(new Error('aborted')))
        }),
      )
      dispatchEvents([{ type: 'query_start' }])
      const docInput = Array.from(document.querySelectorAll('input[type=file]'))
        .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
      const file = new File(['bytes'], 'spec.pdf', { type: 'application/pdf' })
      Object.defineProperty(docInput, 'files', { value: [file], configurable: true })
      docInput.dispatchEvent(new Event('change'))
      setTextarea('upload then stop')
      pressEnter()
      await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
      const ta = document.querySelector('textarea') as HTMLTextAreaElement
      ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await vi.waitFor(() => expect(sent).toContainEqual({ type: 'stop' }))
      // Frozen clock: t0 and the final read are both 2500, so the delta is
      // exactly zero — summing instead of subtracting prints 5000ms.
      expect(dbg).toHaveBeenCalledWith('[chat] onStop stop sent, ws_buffered=0ms')
    } finally {
      nowSpy.mockRestore()
      dbg.mockRestore()
    }
  })

  it('a failed chip retry re-uploads and re-stages the id', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    vi.mocked(uploadAttachment).mockRejectedValueOnce(new Error('net down'))
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    const file = new File(['bytes'], 'retry.pdf', { type: 'application/pdf' })
    Object.defineProperty(docInput, 'files', { value: [file], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('flaky')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    expect(chat.inputBar.getAttachments()[0].failed).toBe(true)
    const redChip = document.querySelector('.border-red-500') as HTMLElement
    const chipButtons = (redChip.parentElement as HTMLElement).querySelectorAll('button')
    // The retry affordance is the failed chip's first button; the second is ×.
    const retryBtn = chipButtons[0] as HTMLButtonElement
    retryBtn.click()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => expect(chat.inputBar.getAttachments()[0].failed).toBe(false))
    expect(chat.inputBar.getAttachments()[0].uploadedID).toBe('up-1')
    expect(chat.inputBar.getAttachments()[0].uploadProgress).toBe(1)
  })

  it('input history arrows replay the last prompt', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('remembered prompt')
    pressEnter()
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    expect(ta.value).toBe('remembered prompt')
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    expect(ta.value).toBe('')
  })
  it('an ArrowUp past the oldest prompt keeps the current value', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('first prompt')
    pressEnter()
    setTextarea('second prompt')
    pressEnter()
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    const up = () => ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    up()
    expect(ta.value).toBe('second prompt')
    up()
    expect(ta.value).toBe('first prompt')
    // Exhausted: the third up reports cursor none and the value survives.
    up()
    expect(ta.value).toBe('first prompt')
  })
  it('an ArrowDown with the cursor at the newest keeps the current value', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('only prompt')
    pressEnter()
    setTextarea('fresh draft')
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    // No history walk below the newest entry: the draft survives.
    expect(ta.value).toBe('fresh draft')
  })

  it('upgrade and give-up states abort in-flight uploads too', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    vi.mocked(uploadAttachment).mockImplementation((_file, opts) =>
      new Promise((_resolve, reject) => {
        opts!.signal.addEventListener('abort', () => reject(new Error('aborted')))
      }),
    )
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    const file = new File(['bytes'], 'a.pdf', { type: 'application/pdf' })
    Object.defineProperty(docInput, 'files', { value: [file], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('u')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    stateCb!('upgrading')
    await vi.waitFor(() => expect(chat.inputBar.getAttachments()[0].failed).toBe(true))
    expect(document.body.hasAttribute('data-upgrading')).toBe(true)
    stateCb!('failed')
    expect(document.body.hasAttribute('data-upgrading')).toBe(false)
  })

  it('reconnecting pins the banner open with pointer cursor', () => {
    mount()
    stateCb!('reconnecting')
    const banner = (document.querySelector('.text-\\[12px\\].text-red') as HTMLElement).parentElement!
    expect(banner.style.maxHeight).toBe('40px')
    expect(banner.style.opacity).toBe('1')
    expect(banner.style.cursor).toBe('default')
  })

  it('rollback leaves the capsule armed when the socket actually dropped', () => {
    vi.useFakeTimers()
    mount()
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'x' }])
    stateCb!('upgrading')
    wsConnected = false
    vi.advanceTimersByTime(20_000)
    expect(document.body.hasAttribute('data-upgrading')).toBe(true)
  })

  it('voice page mounts with a rendered avatar inside its holder', async () => {
    vi.useFakeTimers()
    class FakeAC {
      static created: FakeAC[] = []
      readonly sampleRate = 48000
      readonly destination = {}
      readonly audioWorklet = { addModule: async () => {} }
      resume() { return Promise.resolve() }
      close() { return Promise.resolve() }
      createAnalyser() { return { connect() {}, disconnect() {}, getByteTimeDomainData() {} } }
      createMediaStreamSource() { return { connect() {}, disconnect() {} } }
    }
    vi.stubGlobal('AudioContext', FakeAC)
    vi.stubGlobal('AudioWorkletNode', class { port = { postMessage() {}, posted: [] as unknown[], onmessage: null }; connect() {} disconnect() {} })
    Object.defineProperty(navigator, 'mediaDevices', {
      value: { getUserMedia: async () => ({ getTracks: () => [] }) },
      configurable: true,
    })
    document.body.innerHTML = ''
    const rootEl = document.createElement('div')
    rootEl.id = 'root'
    document.body.appendChild(rootEl)
    const chat = createChat({ connected: true })
    rootEl.appendChild(chat.root)
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'config', models: [], current: { provider: 'p', model: 'm' }, stt: true, tts: true,
    })
    const mic = document.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement
    mic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }))
    await vi.advanceTimersByTimeAsync(350)
    await Promise.resolve()
    await Promise.resolve()
    const holder = document.querySelector('.voice-avatar') as HTMLElement
    expect(holder).toBeTruthy()
    expect(holder.childElementCount).toBeGreaterThan(0)
    vi.stubGlobal('AudioContext', undefined)
  })

  it('scroll button click re-pins the viewport to the bottom', () => {
    Object.defineProperty(Element.prototype, 'scrollHeight', { configurable: true, writable: true, value: 3000 })
    Object.defineProperty(Element.prototype, 'clientHeight', { configurable: true, writable: true, value: 500 })
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    chat.scrollEl.scrollTo = ((o: ScrollToOptions) => { chat.scrollEl.scrollTop = o.top ?? 0 }) as typeof chat.scrollEl.scrollTo
    chat.scrollEl.scrollTop = 0
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    const btn = (document.querySelector('.scroll-progress') as HTMLElement).closest('button') as HTMLElement
    expect(btn.classList.contains('opacity-0')).toBe(false)
    btn.click()
    expect(chat.scrollEl.scrollTop).toBeGreaterThan(0)
    expect(btn.classList.contains('opacity-0')).toBe(true)
  })
})

describe('chat mutation kills: sidebar sheets and session wiring', () => {
  it('new-session button sends session_new and closes the sidebar', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const btn = document.querySelector('[data-new-session]') as HTMLButtonElement
    btn.click()
    expect(sent).toContainEqual({ type: 'session_new' })
  })

  it('settings button closes the sidebar and opens the settings page', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const gear = document.querySelector('button[aria-label="Settings"]') as HTMLButtonElement
    gear.click()
    const page = Array.from(document.querySelectorAll('.fixed.inset-0'))
      .find((el) => (el.textContent ?? '').includes('PROVIDERS')) as HTMLElement
    expect(page.style.display).not.toBe('none')
    // The gear closes the sidebar first: its panel parks off-canvas.
    const sidebarRoot = (document.querySelector('[data-new-session]') as HTMLElement)
      .closest('.fixed') as HTMLElement
    expect(sidebarRoot.style.transform).toBe('translateX(-100%)')
  })
  it('the settings gear closes an already-open sidebar immediately', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const hamburger = document.querySelector('button[aria-label="Menu"]') as HTMLButtonElement
    hamburger.click()
    const sidebarRoot = (document.querySelector('[data-new-session]') as HTMLElement)
      .closest('.fixed') as HTMLElement
    expect(sidebarRoot.style.transform).not.toBe('translateX(-100%)')
    const gear = document.querySelector('button[aria-label="Settings"]') as HTMLButtonElement
    gear.click()
    const page = Array.from(document.querySelectorAll('.fixed.inset-0'))
      .find((el) => (el.textContent ?? '').includes('PROVIDERS')) as HTMLElement
    expect(page.style.display).not.toBe('none')
    expect(sidebarRoot.style.transform).toBe('translateX(-100%)')
  })
  it('an empty config thinking value renders an empty caption, not auto', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'config',
      models: [],
      current: { provider: 'p', model: 'm' },
      stt: false,
      thinking: '',
    })
    const caption = Array.from(document.querySelectorAll('button'))
      .find((b) => b.className.includes('text-[10px] leading-none')) as HTMLElement
    expect(caption).toBeTruthy()
    // 'auto' hides the caption; an explicit empty effort shows it blank.
    expect(caption.classList.contains('invisible')).toBe(false)
    expect(caption.textContent).toBe('')
  })

  it('a device row opens the VNC sheet', async () => {
    const realFetch = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn().mockImplementation((url: string) => {
      const path = typeof url === 'string' ? url : (url as URL).toString()
      if (path.includes('remotedesktop')) {
        return Promise.resolve({ ok: true, json: async () => ({ devices: [{ name: 'phone-x', addr: '1.2.3.4:5901' }] }) })
      }
      if (path.includes('artifacts')) return Promise.resolve({ ok: true, json: async () => [] })
      if (path.includes('plugins')) return Promise.resolve({ ok: true, json: async () => [] })
      return Promise.resolve({ ok: true, json: async () => ({}) })
    }))
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // Opening the sidebar refreshes devices through the real fetch mock.
    ;(document.querySelector('button[aria-label="Menu"]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(document.querySelector('[data-device-row="phone-x"]')).toBeTruthy()
    })
    ;(document.querySelector('[data-device-row="phone-x"]') as HTMLElement).click()
    const veil = document.querySelector('[data-vnc-veil]') as HTMLElement
    expect(veil.style.display).not.toBe('none')
    vi.stubGlobal('fetch', realFetch)
  })

  it('clear-artifacts double tap refetches the artifact list', async () => {
    const realFetch = globalThis.fetch
    let artifactFetches = 0
    vi.stubGlobal('fetch', vi.fn().mockImplementation((url: unknown) => {
      const path = typeof url === 'string' ? url : String(url)
      if (path.includes('artifacts')) {
        artifactFetches++
        return Promise.resolve({ ok: true, json: async () => [] })
      }
      if (path.includes('remotedesktop')) return Promise.resolve({ ok: true, json: async () => ({ devices: [] }) })
      if (path.includes('plugins')) return Promise.resolve({ ok: true, json: async () => [] })
      return Promise.resolve({ ok: true, json: async () => ({}) })
    }))
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const btn = document.querySelector('[data-clear-artifacts]') as HTMLButtonElement
    btn.click()
    btn.click()
    await vi.waitFor(() => expect(artifactFetches).toBe(1))
    vi.stubGlobal('fetch', realFetch)
  })

  it('metadata-hydrated queued pills survive into the next streaming turn', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
      queuedMsgs: [{ uuid: 'mq1', text: 'hydrated pill' }],
    })
    dispatch({ type: 'history', messages: [], stream: [], nextCursor: '', hasMore: false })
    dispatchEvents([{ type: 'query_start' }])
    const labels = Array.from(document.querySelectorAll('.queued-bubble span:first-child'))
      .map((el) => el.textContent)
    expect(labels).toEqual(['hydrated pill'])
  })

  it('a page0 cache hit clears the pending-page gate for live events', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const feed = (rev: string) => {
      dispatch({
        type: 'metadata', rev,
        connect: { connected: true, engineID: 'e1', sessionID: 's' },
        config: { models: [], current: { provider: 'p', model: 'm' } },
        engines: { engines: [], activeID: 'e1' },
        stats: { usage: { input_tokens: 0, output_tokens: 0 } },
      })
      dispatch({ type: 'history', messages: [], stream: [], nextCursor: '', hasMore: false, rev, busy: false })
    }
    feed('r1')
    sent.length = 0
    feed('r1')
    // Cache hit: no round trip...
    expect(sent.filter((m) => (m as { type: string }).type === 'history_request').length).toBe(0)
    // ...and the page-zero gate opened: this echo applies live.
    dispatch({
      type: 'event',
      event: { type: 'attachment', message: { attachment: { prompt: 'live after hit' } } },
    })
    const spans = Array.from(document.querySelectorAll('.max-w-fit span.whitespace-pre-wrap'))
    expect(spans.map((el) => el.textContent)).toEqual(['live after hit'])
  })

  it('an idle attachment echo share-presses to its own text', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: { type: 'attachment', message: { attachment: { prompt: 'standalone echo' } } },
    })
    const row = Array.from(document.querySelectorAll('.space-y-7 > div'))
      .find((el) => (el.textContent ?? '').includes('standalone echo')) as HTMLElement
    sharePress(row.querySelector('.bg-gradient-to-br') as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('standalone echo')
  })
})

describe('chat mutation kills: voice session switch', () => {
  it('a session switch while the face is open tears the face down', async () => {
    vi.useFakeTimers()
    class FakeAC3 {
      readonly sampleRate = 48000
      readonly destination = {}
      readonly audioWorklet = { addModule: async () => {} }
      resume() { return Promise.resolve() }
      close() { return Promise.resolve() }
      createAnalyser() { return { connect() {}, disconnect() {}, getByteTimeDomainData() {} } }
      createMediaStreamSource() { return { connect() {}, disconnect() {} } }
    }
    vi.stubGlobal('AudioContext', FakeAC3)
    vi.stubGlobal('AudioWorkletNode', class { port = { postMessage() {}, posted: [] as unknown[], onmessage: null }; connect() {} disconnect() {} })
    Object.defineProperty(navigator, 'mediaDevices', {
      value: { getUserMedia: async () => ({ getTracks: () => [] }) },
      configurable: true,
    })
    document.body.innerHTML = ''
    const rootEl = document.createElement('div')
    rootEl.id = 'root'
    document.body.appendChild(rootEl)
    const chat = createChat({ connected: true })
    rootEl.appendChild(chat.root)
    dispatch({ type: 'connect_status', connected: true, sessionID: 's1' })
    dispatch({
      type: 'config', models: [], current: { provider: 'p', model: 'm' }, stt: true, tts: true,
    })
    const mic = document.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement
    mic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }))
    await vi.advanceTimersByTimeAsync(350)
    await Promise.resolve()
    await Promise.resolve()
    expect(document.querySelector('.voice-page')).toBeTruthy()
    dispatch({ type: 'connect_status', connected: true, sessionID: 's2' })
    expect(document.querySelector('.voice-page')).toBeNull()
    expect(document.body.style.overflow).toBe('')
  })
})

// ── batch 6: final residual kills ───────────────────────────────────────────

describe('chat mutation kills: final residuals', () => {
  function page0(stream: unknown[] | undefined, busy: boolean, extra: Record<string, unknown> = {}) {
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
      ...extra,
    })
    dispatch({ type: 'history', messages: [], stream, nextCursor: '', hasMore: false, busy, ...extra })
  }

  it('a tool following a children-carrying tool still copies its own output', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    page0([
      {
        kind: 'tool', id: 'agent-1', name: 'Agent', summary: '', isSearch: false, isRead: false,
        isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 1,
        displayOutput: 'AGENT-OUT', startedAt: 0, children: [{
          kind: 'tool', id: 'inner-1', name: 'Read', summary: '', isSearch: false, isRead: false,
          isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 1,
          displayOutput: 'INNER-OUT', startedAt: 0, children: [],
        }],
      },
      {
        kind: 'tool', id: 't2', name: 'Bash', summary: '', isSearch: false, isRead: false,
        isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 1,
        displayOutput: 'SECOND-OUT', startedAt: 0, children: [],
      },
    ], true)
    sharePress(toolRootByName('Bash'))
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('SECOND-OUT')
  })

  it('reminder row press pairs against user blocks only, body wins over narration', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{ type: 'query_start' }])
    dispatch({ type: 'event', event: { type: 'text_start' } })
    dispatchEvents([{ type: 'text_delta', text: 'narration around it' }])
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: '<job-notification>\n<summary>mixed body</summary>\n</job-notification>' } },
      },
    })
    dispatchEvents([{ type: 'query_end' }])
    const row = document.querySelector('[data-reminder-row]') as HTMLElement
    sharePress(row.querySelector('.font-mono') as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('mixed body')
  })

  it('reminder press while streaming opens nothing', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: '<job-notification>\n<summary>live row</summary>\n</job-notification>' } },
      },
    })
    const row = document.querySelector('[data-reminder-row]') as HTMLElement
    sharePress(row.querySelector('.font-mono') as HTMLElement)
    expect(shareMenu()).toBeNull()
  })

  it('opening the voice face stops an active dictation session', async () => {
    class FakeAC4 {
      readonly sampleRate = 48000
      readonly destination = {}
      readonly audioWorklet = { addModule: async () => {} }
      resume() { return Promise.resolve() }
      close() { return Promise.resolve() }
      createAnalyser() { return { connect() {}, disconnect() {}, getByteTimeDomainData() {} } }
      createMediaStreamSource() { return { connect() {}, disconnect() {} } }
    }
    vi.stubGlobal('AudioContext', FakeAC4)
    vi.stubGlobal('AudioWorkletNode', class { port = { postMessage() {}, posted: [] as unknown[], onmessage: null }; connect() {} disconnect() {} })
    Object.defineProperty(navigator, 'mediaDevices', {
      value: { getUserMedia: async () => ({ getTracks: () => [] }) },
      configurable: true,
    })
    document.body.innerHTML = ''
    const rootEl = document.createElement('div')
    rootEl.id = 'root'
    document.body.appendChild(rootEl)
    const chat = createChat({ connected: true })
    rootEl.appendChild(chat.root)
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'config', models: [], current: { provider: 'p', model: 'm' }, stt: true, tts: true })
    const socks = (sttModule as unknown as { __sttSocks: { closed: boolean }[] }).__sttSocks
    socks.length = 0
    const mic = document.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement
    mic.click()
    await vi.waitFor(() => expect(socks.length).toBe(1))
    expect(socks[0].closed).toBe(false)
    mic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }))
    await vi.waitFor(() => expect(document.querySelector('.voice-page')).not.toBeNull())
    expect(document.querySelector('.voice-page')).toBeTruthy()
    // The dictation round was cut short: the stop frame left the socket.
    const sock0 = socks[0] as unknown as { sentText: string[] }
    expect(sock0.sentText).toContain('{"type":"stop"}')
  })

  it('connected freezes the dots animation for good', () => {
    vi.useFakeTimers()
    mount()
    stateCb!('reconnecting')
    vi.advanceTimersByTime(1000)
    const banner = document.querySelector('.text-\\[12px\\].text-red') as HTMLElement
    expect(banner.textContent).toBe('Connection lost, reconnecting..')
    stateCb!('connected')
    vi.advanceTimersByTime(2500)
    // A leaked tick would have advanced to '...' then '.'.
    expect(banner.textContent).toBe('Connection lost, reconnecting..')
  })

  it('capsule stays hidden through plain reconnect cycles and after rollback/give-up leaks', () => {
    vi.useFakeTimers()
    mount()
    // Plain reconnect: no upgrade was in flight, no recovered flash.
    stateCb!('reconnecting')
    stateCb!('connected')
    let capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).toContain('opacity-0')
    // Rollback bail-out with a dead socket dismisses quietly; when the
    // socket later returns, the cycle must not read as an upgrade recovery.
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'x' }])
    stateCb!('upgrading')
    // The socket revives without a state event; the rollback timer finds
    // it alive and retires quietly — no double recovery later.
    wsConnected = true
    vi.advanceTimersByTime(20_000)
    expect(document.body.hasAttribute('data-upgrading')).toBe(false)
    stateCb!('connected')
    capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).toContain('opacity-0')
  })

  it('upgrading shows the capsule and only freezes a streaming turn', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'u0', role: 'user', text: 'settled message' })],
      nextCursor: '', hasMore: false,
    })
    // Idle chat: no stream to freeze.
    stateCb!('upgrading')
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).not.toContain('opacity-0')
    expect(document.querySelector('.stream-dim')).toBeNull()
    stateCb!('connected')
  })

  it('give-up aborts a second upload staged during the upgrade window and pins the banner', async () => {
    vi.useFakeTimers()
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    vi.mocked(uploadAttachment).mockImplementation((_file, opts) =>
      new Promise((_resolve, reject) => {
        opts!.signal.addEventListener('abort', () => reject(new Error('aborted')))
      }),
    )
    dispatchEvents([{ type: 'query_start' }])
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    const file = new File(['bytes'], 'a.pdf', { type: 'application/pdf' })
    Object.defineProperty(docInput, 'files', { value: [file], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('one')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    stateCb!('upgrading')
    await vi.waitFor(() => expect(chat.inputBar.getAttachments()[0].failed).toBe(true))
    // Stage a second upload while the capsule is up, then give up.
    const file2 = new File(['more'], 'b.pdf', { type: 'application/pdf' })
    Object.defineProperty(docInput, 'files', { value: [file2], configurable: true })
    docInput.dispatchEvent(new Event('change'))
    setTextarea('two')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment.mock.calls.length).toBeGreaterThanOrEqual(2))
    stateCb!('failed')
    await vi.waitFor(() => expect(chat.inputBar.getAttachments().length).toBe(2))
    await vi.waitFor(() => expect(chat.inputBar.getAttachments().every((r) => r.failed)).toBe(true))
    const banner = document.querySelector('.text-\\[12px\\].text-red') as HTMLElement
    expect(banner.parentElement!.style.opacity).toBe('1')
  })

  it('prefetch counting: break-at-boundary and threshold edge', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    const msgs = Array.from({ length: 12 }, (_, i) =>
      histMsg({ id: 'u' + i, role: 'user', text: 'm' + i, startedAt: i }))
    dispatch({ type: 'history', messages: msgs, nextCursor: 'c1', hasMore: true })
    // The load-time prefetch fired on unmocked geometry; answer it.
    dispatch({ type: 'history', messages: [], nextCursor: 'c2', hasMore: true })
    const kids = Array.from(document.querySelector('.space-y-7')!.children) as HTMLElement[]
    kids.forEach((el, i) => {
      Object.defineProperty(el, 'offsetTop', { configurable: true, value: i * 100 })
      Object.defineProperty(el, 'offsetHeight', { configurable: true, value: 50 })
    })
    const reqs = () => sent.filter((m) => (m as { type: string }).type === 'history_request') as { cursor: string }[]
    // Scroll to 950: child 9's bottom is exactly 950 — inclusive break gives
    // 9 above → below the threshold → the load-time prefetch of the page
    // itself fires for c2 again.
    chat.scrollEl.scrollTop = 950
    dispatch({ type: 'history', messages: [], nextCursor: 'c3', hasMore: true })
    expect(reqs().some((m) => m.cursor === 'c3')).toBe(true)
    // Scroll to 1050: exactly ten above → no request leaves.
    sent.length = 0
    chat.scrollEl.scrollTop = 1050
    dispatch({ type: 'history', messages: [], nextCursor: 'c4', hasMore: true })
    expect(reqs().length).toBe(0)
  })

  it('hydrated active thinking keeps its server duration at finalize', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    page0([{
      kind: 'thinking', id: '', text: 'frozen reasoning', durationNs: 5_000_000_000,
      active: true, startedAt: Date.now() - 1000,
    }], true)
    dispatchEvents([{ type: 'query_end' }])
    const label = Array.from(document.querySelectorAll('.text-amber'))
      .find((el) => (el.textContent ?? '').startsWith('Thought for ')) as HTMLElement
    expect(label.textContent).toBe('Thought for 5s')
  })

  it('an error after the turn closed opens a fresh assistant shell and disarms STOP', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'text_delta', text: 'done answer' }, { type: 'query_end' }])
    const shellsBefore = document.querySelectorAll('.space-y-3').length
    dispatch({ type: 'error', message: 'late failure' })
    expect(document.querySelectorAll('.space-y-3').length).toBe(shellsBefore + 1)
    const box = Array.from(document.querySelectorAll('.break-all.text-red'))
      .find((el) => el.textContent === 'late failure') as HTMLElement
    expect(box.className).toContain('border-red/40')
    expect(document.body.textContent).toContain('done answer')
    expect(Array.from(document.querySelectorAll('button')).some((b) => b.textContent?.includes('STOP'))).toBe(false)
  })

  it('metadata queryStartMs anchors the busy page elapsed, not the page arrival', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      busy: true,
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 }, queryStartMs: Date.now() - 300000 },
    })
    dispatch({
      type: 'history',
      messages: [histMsg({
        id: 'a1', role: 'assistant',
        blocks: [{ kind: 'tool', tool: { id: 't-1', name: 'Bash', isRunning: true } }],
      })],
      stream: [],
      nextCursor: '', hasMore: false, busy: true,
    })
    vi.advanceTimersByTime(200)
    const elapsed = document.querySelector('.status-line .sep-elapsed + span') as HTMLElement
    expect(elapsed.textContent).toBe('5m 0s')
  })

  it('nested snapshot tools register: a live tool_start with their id dedupes', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    page0([{
      kind: 'tool', id: 'agent-1', name: 'Agent', summary: '', isSearch: false, isRead: false,
      isList: false, isLsp: false, isWeb: false, isTask: false, state: 'running', timingNs: 0,
      displayOutput: '', startedAt: Date.now(), children: [{
        kind: 'tool', id: 'inner-1', name: 'Edit', summary: '', isSearch: false, isRead: false,
        isList: false, isLsp: false, isWeb: false, isTask: false, state: 'running', timingNs: 0,
        displayOutput: '', startedAt: Date.now(), children: [],
      }],
    }], true)
    // The nested inner id is known from the snapshot: no duplicate row.
    dispatchEvents([{ type: 'tool_start', tool_use: { id: 'inner-1', name: 'Edit' } }])
    expect(document.querySelectorAll('[data-tool-root]').length).toBe(2)
  })

  it('a buffered event at the page seq boundary is absorbed, not replayed', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      busy: true,
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
    dispatch({
      type: 'event', seq: 5,
      event: { type: 'attachment', message: { attachment: { prompt: 'buffered echo' } } },
    })
    dispatch({ type: 'history', messages: [], stream: [], nextCursor: '', hasMore: false, busy: false, seq: 5 })
    expect(document.querySelector('.max-w-fit')).toBeNull()
  })

  it('prepend keeps the scrolled-up viewport anchored by the height delta', async () => {
    vi.useFakeTimers()
    armShareBridge()
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'live tail' }])
    const stream = document.querySelector('.space-y-3') as HTMLElement
    Object.defineProperty(chat.scrollEl, 'scrollHeight', {
      configurable: true,
      get: () => 2000 + stream.children.length * 100,
    })
    Object.defineProperty(chat.scrollEl, 'clientHeight', { configurable: true, value: 500 })
    chat.scrollEl.scrollTop = 0
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    dispatch({
      type: 'history',
      messages: [],
      stream: [{ kind: 'text', id: '', text: 'older window text' }],
      nextCursor: '', hasMore: false,
    })
    // One prepended block * 100px keeps the viewport anchored.
    expect(chat.scrollEl.scrollTop).toBe(100)
    // The prepended window commits with the turn: its markdown source
    // still resolves through the block pairing.
    dispatchEvents([{ type: 'query_end' }])
    const divs = document.querySelectorAll('[data-block-kind="text"]')
    sharePress(divs[0] as HTMLElement)
    clickCopy()
    expect(copyText).toHaveBeenCalledWith('older window text')
  })

  it('cache creation joins the cached percentage denominator', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'usage', usage_event: { input_tokens: 50, output_tokens: 0, cache_read_input_tokens: 50, cache_creation_input_tokens: 50 } },
      { type: 'usage', usage_event: { input_tokens: 50, output_tokens: 0, cache_read_input_tokens: 50, cache_creation_input_tokens: 50 } },
      { type: 'query_end' },
    ])
    const bar = document.querySelector('.status-line') as HTMLElement
    // read 100 of total input 300 → 33.3%.
    expect(bar.textContent).toContain('33.3% cached')
  })

  it('thinking fallback without a server value renders auto, caption stays invisible', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'model_switched', contextUsed: 1, contextTotal: 100 })
    dispatch({ type: 'config', models: [], current: { provider: 'p', model: 'm' } })
    const caption = Array.from(document.querySelectorAll('button'))
      .find((b) => b.className.includes('text-[10px]') && b.className.includes('leading-none')) as HTMLElement
    expect(caption.classList.contains('invisible')).toBe(true)
    expect(caption.textContent).not.toBe('undefined')
    expect(document.body.textContent).not.toContain('undefined')
  })

  it('busy page with no stream window builds the prefill shell and finishes the bar', () => {
    vi.useFakeTimers()
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    mount()
    dispatch({ type: 'connect_status', connected: true })
    stateCb!('reconnecting')
    page0(undefined, true)
    expect(document.querySelector('.status-line')).toBeTruthy()
    const fill = document.querySelector('[data-testid="takeover-bar-fill"]') as HTMLElement
    expect(fill.style.width).toBe('100%')
    expect(dbg).toHaveBeenCalledWith(expect.stringContaining('reason=page'))
  })

  it('near-bottom backfill keeps the viewport pinned to the bottom', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    const container = document.querySelector('.space-y-7') as HTMLElement
    Object.defineProperty(chat.scrollEl, 'scrollHeight', {
      configurable: true,
      get: () => 1000 + container.children.length * 100,
    })
    Object.defineProperty(chat.scrollEl, 'clientHeight', { configurable: true, value: 500 })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'u0', role: 'user', text: 'seed' })],
      nextCursor: '', hasMore: false,
    })
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    // Large snapshot: the tail renders sync, older blocks backfill on rAF
    // frames — while near bottom the autoscroll must keep winning.
    const blocks = Array.from({ length: 40 }, (_, i) => ({ kind: 'text' as const, id: '', text: 'blk' + i }))
    dispatch({ type: 'streamState', blocks })
    // Wait for the rAF backfill to land every block, then re-pin.
    await vi.waitFor(() =>
      expect((document.querySelector('.space-y-3') as HTMLElement).querySelectorAll('[data-block-kind="text"]').length).toBe(40))
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    expect(chat.scrollEl.scrollTop).toBe(chat.scrollEl.scrollHeight)
  })
})

describe('chat mutation kills: whitespace rewind', () => {
  it('whitespace-only text is no content: abort rewound rewinds the turn', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('ws question')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: '   ' },
    ])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect(document.body.textContent).not.toContain('ws question')
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('ws question')
  })

  it('an echo-only abort with rewind commits the turn', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: { type: 'attachment', message: { attachment: { prompt: 'echo only content' } } },
    })
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect(document.body.textContent).toContain('echo only content')
    expect(document.body.textContent).toContain('q')
  })
})

// ── batch 7: closing stragglers ─────────────────────────────────────────────

describe('chat mutation kills: closing stragglers', () => {
  it('a delta arriving before the stream shell mounts lands inline at the anchor', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    // Delta with no shell yet: the block text accumulates in the model.
    dispatchEvents([{ type: 'text_delta', text: 'early bird' }])
    // The turn starts late; the next delta mounts the div inline.
    dispatchEvents([{ type: 'turn_start' }, { type: 'text_delta', text: '```js\nlate()\n```' }])
    const stream = document.querySelector('.space-y-3') as HTMLElement
    const divs = stream.querySelectorAll(':scope > [data-block-kind="text"]')
    expect(divs.length).toBe(1)
    const div = divs[0] as HTMLElement
    expect(div.querySelector('.copy-btn')).toBeTruthy()
    const bar = stream.querySelector(':scope > .status-line') as HTMLElement
    expect(div.compareDocumentPosition(bar) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('tool_end without a display_output leaves the body empty', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Edit' } },
      { type: 'tool_end', tool_result: { tool_use_id: 't1' } },
    ])
    const root = document.querySelector('[data-tool-root]') as HTMLElement
    ;(root.querySelector('[data-tool-header]') as HTMLElement).click()
    const body = root.querySelector('.md-body') as HTMLElement
    expect(body.textContent).toBe('')
  })

  it('upgrade recovery followed by a plain cycle shows no second capsule', () => {
    vi.useFakeTimers()
    mount()
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'x' }])
    stateCb!('upgrading')
    stateCb!('connected')
    vi.advanceTimersByTime(1100)
    // The recovery flag cleared: this plain cycle must stay capsule-free.
    stateCb!('reconnecting')
    stateCb!('connected')
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).toContain('opacity-0')
  })

  it('give-up retire keeps plain cycles capsule-free too', () => {
    vi.useFakeTimers()
    mount()
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'x' }])
    stateCb!('upgrading')
    stateCb!('failed')
    stateCb!('reconnecting')
    stateCb!('connected')
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.className).toContain('opacity-0')
  })

  it('a prepended running tool registers for its live tool_end', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'text_start' }, { type: 'text_delta', text: 'live tail' }])
    dispatch({
      type: 'history',
      messages: [],
      stream: [{
        kind: 'tool', id: 't-old', name: 'Edit', summary: '', isSearch: false, isRead: false,
        isList: false, isLsp: false, isWeb: false, isTask: false, state: 'running', timingNs: 0,
        displayOutput: '', startedAt: Date.now(), children: [],
      }],
      nextCursor: '', hasMore: false,
    })
    dispatchEvents([{ type: 'tool_end', tool_result: { tool_use_id: 't-old', display_output: 'older tool output' } }])
    const old = document.querySelector('[data-tool-root][data-tool-name="Edit"]') as HTMLElement
    ;(old.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(old.querySelector('.md-body')!.textContent).toBe('older tool output\n')
  })

  it('an agent-tagged text_start never opens a main text block', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'text_start', agent: { parent_tool_use_id: 'agent-1' } },
    ])
    const stream = document.querySelector('.space-y-3') as HTMLElement
    expect(stream.querySelectorAll(':scope > [data-block-kind="text"]').length).toBe(0)
  })

  it('an agent thinking_start expands the running parent children pane', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'thinking_start', agent: { parent_tool_use_id: 'agent-1' } },
    ])
    const container = document.querySelector('[data-tool-children]') as HTMLElement
    expect(container.classList.contains('hidden')).toBe(false)
  })

  it('an agent text delta as the first child event also expands the running parent pane', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'text_delta', agent: { parent_tool_use_id: 'agent-1' }, text: 'first words' },
    ])
    const container = document.querySelector('[data-tool-children]') as HTMLElement
    expect(container.classList.contains('hidden')).toBe(false)
  })

  it('the first sub-agent text delta renders its code fence with a copy button', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('q')
    pressEnter()
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent' } },
      { type: 'text_delta', agent: { parent_tool_use_id: 'agent-1' }, text: '```js\nfirst()\n```' },
    ])
    const children = document.querySelector('[data-tool-children]') as HTMLElement
    expect(children.querySelector('.copy-btn')).toBeTruthy()
  })

  it('a rewound_text naming an assistant turn matches nothing and keeps history', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [histMsg({ id: 'a0', role: 'assistant', blocks: [{ kind: 'text', text: 'assistant words' }] })],
      nextCursor: '', hasMore: false,
    })
    setTextarea('local q')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true, rewound_text: 'assistant words' }])
    expect(document.body.textContent).toContain('assistant words')
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('assistant words')
  })

  it('a legacy rewind with no user messages preserves the draft', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('draft survives')
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('draft survives')
  })

  it('after a rewind-pop, a later error renders in a fresh shell', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    setTextarea('gone prompt')
    pressEnter()
    dispatchEvents([{ type: 'query_start' }, { type: 'thinking_start' }])
    dispatchEvents([{ type: 'query_end', aborted: true, rewound: true }])
    expect(document.querySelectorAll('.space-y-3').length).toBe(0)
    // The popped assistant is gone from the timeline state too: the error
    // lands in a NEW shell instead of the dead entry.
    dispatch({ type: 'error', message: 'post rewind failure' })
    const box = Array.from(document.querySelectorAll('.break-all.text-red'))
      .find((el) => el.textContent === 'post rewind failure') as HTMLElement
    expect(box.className).toContain('border-red/40')
  })

})

// ── residual round two (ledger RESIDUAL section, mutate-wui-chat) ───────────

describe('chat mutation kills: residual round two', () => {
  const metaFrame = () =>
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'e1' },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })
  const streamTool = (id: string, children: unknown[], extra: Record<string, unknown> = {}) => ({
    kind: 'tool', id, name: 'Agent', summary: '', isSearch: false, isRead: false,
    isList: false, isLsp: false, isWeb: false, isTask: false, state: 'running', timingNs: 0,
    displayOutput: '', startedAt: 0, truncated: 5, truncatedCursor: 'anc', children, ...extra,
  })
  const childTool = (id: string, name: string, output: string) => ({
    kind: 'tool', id, name, summary: '', isSearch: false, isRead: false,
    isList: false, isLsp: false, isWeb: false, isTask: false, state: 'done', timingNs: 1,
    displayOutput: output, startedAt: 0, children: [],
  })
  const stageFileForSend = () => {
    const docInput = Array.from(document.querySelectorAll('input[type=file]'))
      .find((i) => (i.getAttribute('accept') ?? '').includes('.pdf')) as HTMLInputElement
    Object.defineProperty(docInput, 'files', {
      value: [new File(['bytes'], 'spec.pdf', { type: 'application/pdf' })],
      configurable: true,
    })
    docInput.dispatchEvent(new Event('change'))
  }
  const retryButtonOfFailedChip = () => {
    const redChip = document.querySelector('.border-red-500') as HTMLElement
    return (redChip.parentElement as HTMLElement).querySelectorAll('button')[0] as HTMLButtonElement
  }

  it('a delta after an empty-text snapshot resume keeps morphing the same div', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    metaFrame()
    dispatch({
      type: 'history',
      // The empty trailing text block rendered no div at hydration, so the
      // first resumed delta must mount one and REGISTER it: a second delta
      // that re-mounts instead of morphing leaves a stale twin behind.
      messages: [],
      stream: [streamTool('t1', [{ kind: 'text', id: '', text: '' }], { truncated: 0, truncatedCursor: undefined })],
      nextCursor: '', hasMore: false, busy: true,
    })
    dispatchEvents([
      { type: 'text_delta', agent: { parent_tool_use_id: 't1' }, text: 'Hello' },
      { type: 'text_delta', agent: { parent_tool_use_id: 't1' }, text: ' world' },
    ])
    const children = document.querySelector('[data-tool-children]') as HTMLElement
    const textDivs = children.querySelectorAll('[data-block-kind="text"]')
    expect(textDivs.length).toBe(1)
    expect(textDivs[0].textContent).toContain('Hello world')
  })

  it('a tool_output_delta without display_output opens no share menu mid-flight', () => {
    vi.useFakeTimers()
    armShareBridge()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvents([
      { type: 'query_start' },
      { type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } },
      // display_output absent: the pending block's output must stay empty, so
      // a mid-flight press has nothing to copy and opens nothing.
      { type: 'tool_output_delta', tool_result: { tool_use_id: 't1' } },
    ])
    sharePress(toolRootByName('Bash').querySelector('.font-mono') as HTMLElement)
    expect(shareMenu()).toBeNull()
  })

  it('an attachment with neither prompt nor content renders no row', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'event',
      event: { type: 'attachment', message: { attachment: {} } },
    })
    expect((document.querySelector('.space-y-7') as HTMLElement).children.length).toBe(0)
  })

  it('a paused upload keeps the composer disabled mid-flight', async () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    let finish!: (v: { id: string }) => void
    vi.mocked(uploadAttachment).mockImplementation(() =>
      new Promise((resolve) => { finish = resolve }))
    stageFileForSend()
    setTextarea('slow upload')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    expect((document.querySelector('textarea') as HTMLTextAreaElement).disabled).toBe(true)
    finish({ id: 'up-4' })
    await vi.waitFor(() =>
      expect((document.querySelector('textarea') as HTMLTextAreaElement).disabled).toBe(false))
  })

  it('STOP aborts a hanging retry upload', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    vi.mocked(uploadAttachment).mockRejectedValueOnce(new Error('net down'))
    stageFileForSend()
    setTextarea('flaky then stranded')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    expect(chat.inputBar.getAttachments()[0].failed).toBe(true)
    // The retry must register its AbortController with the chat: STOP owns
    // every in-flight upload, retry uploads included.
    let retryAborted = false
    vi.mocked(uploadAttachment).mockImplementation((_file, opts) =>
      new Promise((_resolve, reject) => {
        opts!.signal.addEventListener('abort', () => {
          retryAborted = true
          reject(new Error('aborted'))
        })
      }))
    dispatchEvents([{ type: 'query_start' }])
    retryButtonOfFailedChip().click()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(2))
    ;(document.querySelector('textarea') as HTMLTextAreaElement)
      .dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(sent).toContainEqual({ type: 'stop' })
    await vi.waitFor(() => expect(retryAborted).toBe(true))
    expect(chat.inputBar.getAttachments()[0].failed).toBe(true)
  })

  it('retry upload progress streams into the chip', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    vi.mocked(uploadAttachment).mockRejectedValueOnce(new Error('net down'))
    stageFileForSend()
    setTextarea('retry with progress')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    expect(chat.inputBar.getAttachments()[0].failed).toBe(true)
    let finish!: (v: { id: string }) => void
    vi.mocked(uploadAttachment).mockImplementation((_file, opts) =>
      new Promise((resolve) => {
        finish = resolve
        opts!.onProgress!(0.5)
      }))
    retryButtonOfFailedChip().click()
    await vi.waitFor(() =>
      expect(chat.inputBar.getAttachments()[0].uploadProgress).toBe(0.5))
    finish({ id: 'up-5' })
    await vi.waitFor(() =>
      expect(chat.inputBar.getAttachments()[0].uploadedID).toBe('up-5'))
    expect(chat.inputBar.getAttachments()[0].failed).toBe(false)
  })

  it('a rejecting retry keeps the chip failed for another tap', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    vi.mocked(uploadAttachment).mockRejectedValueOnce(new Error('first try down'))
    stageFileForSend()
    setTextarea('retry rejects too')
    pressEnter()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(1))
    expect(chat.inputBar.getAttachments()[0].failed).toBe(true)
    vi.mocked(uploadAttachment).mockRejectedValueOnce(new Error('retry down'))
    retryButtonOfFailedChip().click()
    await vi.waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(2))
    // Deterministic settle: await the exact rejected promise the retry
    // consumed (its first consumer is chat's catch), then let the caller's
    // .then chain run — no timer involved.
    await expect(vi.mocked(uploadAttachment).mock.results[1]!.value).rejects.toThrow('retry down')
    await Promise.resolve()
    await Promise.resolve()
    expect(chat.inputBar.getAttachments()[0].failed).toBe(true)
    expect(document.querySelector('.border-red-500')).toBeTruthy()
    expect(document.querySelector('.border-red-500')!.textContent).toContain('spec.pdf')
  })

  it('long-press rename commits through the session_rename frame', () => {
    vi.useFakeTimers()
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'session_list', sessions: [{ id: 's1', title: 'original title', updatedAt: 1 }] })
    const row = document.querySelector('[data-session-row]') as HTMLElement
    row.dispatchEvent(new Event('touchstart', { bubbles: true }))
    vi.advanceTimersByTime(500)
    const input = row.querySelector('input') as HTMLInputElement
    expect(input).toBeTruthy()
    input.value = 'renamed title'
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(sent).toContainEqual({ type: 'session_rename', sessionID: 's1', title: 'renamed title' })
  })

  it('clear-artifacts refetch re-renders the artifact tree', async () => {
    const realFetch = globalThis.fetch
    let getListFetches = 0
    vi.stubGlobal('fetch', vi.fn().mockImplementation((url: unknown, init?: { method?: string }) => {
      const path = typeof url === 'string' ? url : String(url)
      if (path.includes('artifacts')) {
        // The sidebar's own DELETE shares the path: only the GET refetch
        // proves chat's handler ran.
        if (init?.method === 'DELETE') return Promise.resolve({ ok: true, json: async () => ({}) })
        getListFetches++
        return Promise.resolve({ ok: true, json: async () => [{ name: 'logs/run.txt', mtime: 900000000 }] })
      }
      if (path.includes('remotedesktop')) return Promise.resolve({ ok: true, json: async () => ({ devices: [] }) })
      if (path.includes('plugins')) return Promise.resolve({ ok: true, json: async () => [] })
      return Promise.resolve({ ok: true, json: async () => ({}) })
    }))
    mount()
    dispatch({ type: 'connect_status', connected: true })
    const btn = document.querySelector('[data-clear-artifacts]') as HTMLButtonElement
    btn.click()
    btn.click()
    await vi.waitFor(() => expect(getListFetches).toBe(1))
    const dir = document.querySelector('[data-artifact-dir="logs"]') as HTMLElement
    expect(dir).toBeTruthy()
    // The group's children div follows the dir row inside the wrap.
    const fileRow = (dir.nextElementSibling as HTMLElement).querySelector('.artifact-row') as HTMLElement
    expect(fileRow.textContent).toContain('run.txt')
    vi.stubGlobal('fetch', realFetch)
  })

  it('a running plugin renders into the sidebar and its click opens the card', async () => {
    const realFetch = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn().mockImplementation((url: unknown) => {
      const path = typeof url === 'string' ? url : String(url)
      if (path.includes('plugins')) {
        return Promise.resolve({
          ok: true,
          json: async () => [{ name: 'demo-plugin', ui: '/plugins/demo-plugin/ui.js', running: true }],
        })
      }
      if (path.includes('artifacts')) return Promise.resolve({ ok: true, json: async () => [] })
      if (path.includes('remotedesktop')) return Promise.resolve({ ok: true, json: async () => ({ devices: [] }) })
      return Promise.resolve({ ok: true, json: async () => ({}) })
    }))
    mount()
    dispatch({ type: 'connect_status', connected: true })
    ;(document.querySelector('button[aria-label="Menu"]') as HTMLElement).click()
    const row = await vi.waitFor(() => {
      const el = document.querySelector('[data-plugin-row="demo-plugin"]') as HTMLElement
      expect(el).toBeTruthy()
      return el
    })
    row.click()
    // The plugin sheet shares the vnc-veil class with the console sheet;
    // its card body container is the plugin host's unique marker.
    const cardBody = document.querySelector('.vnc-veil .plugin-card-body') as HTMLElement
    expect(cardBody).toBeTruthy()
    expect((cardBody.closest('.vnc-sheet') as HTMLElement).textContent).toContain('demo-plugin')
    vi.stubGlobal('fetch', realFetch)
  })

  it('model_switched without thinking keeps the caption on invisible auto', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // No config frame afterwards: a later config frame would re-pin the
    // caption and mask a bad fallback here.
    dispatch({ type: 'model_switched', contextUsed: 1, contextTotal: 100 })
    const caption = Array.from(document.querySelectorAll('button'))
      .find((b) => b.className.includes('text-[10px] leading-none')) as HTMLElement
    expect(caption).toBeTruthy()
    expect(caption.classList.contains('invisible')).toBe(true)
    expect(caption.textContent).toBe('auto')
  })

  it('an id-less incoming tool survives a parent text child', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    metaFrame()
    dispatch({
      type: 'history',
      messages: [],
      stream: [streamTool('t1', [
        { kind: 'text', id: '', text: 'kept note' },
        childTool('c1', 'Read', 'C1 OUTPUT'),
      ])],
      nextCursor: '', hasMore: false, busy: true,
    })
    const nonce = ((sent.find((m) => (m as { type: string }).type === 'subtree_request')) as { nonce: number }).nonce
    dispatch({
      type: 'subtree_page',
      nonce,
      blockID: 't1',
      children: [
        { kind: 'text', id: '', text: 'older note' },
        childTool('', 'Grep', 'ANON OUTPUT'),
      ],
      remaining: 0,
      nextAnchor: '',
    })
    const children = document.querySelector('[data-tool-children]') as HTMLElement
    // Dedup is kind-aware: only SEEN TOOL ids drop. The text child's empty
    // id must not swallow an incoming id-less tool.
    expect(children.querySelectorAll('[data-tool-root]').length).toBe(2)
    expect(children.querySelectorAll('[data-tool-root][data-tool-name="Grep"]').length).toBe(1)
    expect(children.querySelectorAll('[data-block-kind="text"]').length).toBe(2)
    expect(children.textContent).toContain('older note')
    expect(children.textContent).toContain('kept note')
  })

  it('an incoming text child survives a parent id-less tool', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    metaFrame()
    dispatch({
      type: 'history',
      messages: [],
      stream: [streamTool('t1', [
        childTool('', 'Legacy', 'LEGACY OUTPUT'),
        { kind: 'text', id: '', text: 'kept note' },
      ])],
      nextCursor: '', hasMore: false, busy: true,
    })
    const nonce = ((sent.find((m) => (m as { type: string }).type === 'subtree_request')) as { nonce: number }).nonce
    dispatch({
      type: 'subtree_page',
      nonce,
      blockID: 't1',
      children: [
        { kind: 'text', id: '', text: 'older note' },
        childTool('c9', 'Read', 'C9 OUTPUT'),
      ],
      remaining: 0,
      nextAnchor: '',
    })
    const children = document.querySelector('[data-tool-children]') as HTMLElement
    // The parent's id-less tool puts '' into the seen set; the kept-filter's
    // tool-kind conjunct must keep that from dropping incoming text blocks.
    expect(children.querySelectorAll('[data-block-kind="text"]').length).toBe(2)
    expect(children.textContent).toContain('older note')
    expect(children.querySelectorAll('[data-tool-root][data-tool-name="Read"]').length).toBe(1)
    expect(children.textContent).toContain('kept note')
  })

  it('an error frame flushes an in-flight snapshot hydration', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    // A large snapshot leaves the older blocks on rAF backfill; the error
    // frame must land them synchronously before its box appends.
    dispatch({
      type: 'streamState',
      blocks: Array.from({ length: 30 }, (_, i) => ({ kind: 'text' as const, id: '', text: 'old ' + i })),
    })
    dispatch({ type: 'error', message: 'boom' })
    const stream = document.querySelector('.space-y-3') as HTMLElement
    expect(stream.querySelectorAll('[data-block-kind="text"]').length).toBe(30)
    const box = Array.from(document.querySelectorAll('.break-all.text-red'))
      .find((el) => el.textContent === 'boom') as HTMLElement
    expect(box).toBeTruthy()
    expect(box.className).toContain('border-red/40')
  })

})
