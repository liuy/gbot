import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { t } from './i18n'
import {
  WORKLET_SRC,
  floatTo16Le,
  joinSttText,
  extractPromptTerms,
  sttWsUrl,
  SttSession,
  type SttSessionOpts,
  type SttDeps,
  STT_FRAME_SAMPLES,
} from './stt'

// Mirrors ws.test.ts's stubGlobal mock, extended for the STT protocol:
// binary sends and a settable bufferedAmount.
class MockWebSocket {
  static instances: MockWebSocket[] = []
  static OPEN = 1
  static CLOSED = 3
  readyState = MockWebSocket.OPEN
  bufferedAmount = 0
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  sentText: string[] = []
  sentBinary: ArrayBuffer[] = []
  closed = false

  constructor() {
    MockWebSocket.instances.push(this)
    // Real WebSocket handshakes complete asynchronously — the open event
    // NEVER fires synchronously with construction. Without this, tests
    // would pass against an eagerly-open socket the browser never offers.
    queueMicrotask(() => {
      this.readyState = MockWebSocket.OPEN
      this.onopen?.()
    })
  }
  send(data: string | ArrayBuffer) {
    if (typeof data === 'string') this.sentText.push(data)
    else this.sentBinary.push(data)
  }
  close() {
    this.closed = true
    this.readyState = MockWebSocket.CLOSED
  }
  dispatch(data: unknown) {
    this.onmessage!({ data })
  }
  dispatchClose() {
    this.onclose!()
  }
}

// Fake capture-chain collaborators. Every SttSession test injects all four
// deps; jsdom has no real AudioContext/AudioWorklet/getUserMedia.
function makeDeps(capture: { onFrame?: (f: Float32Array) => void } = {}): {
  deps: SttDeps
  state: {
    ctxClosed: boolean
    tracksStopped: boolean
    workletDisconnected: boolean
    frame: (f: Float32Array) => void
  }
} {
  const state = {
    ctxClosed: false,
    tracksStopped: false,
    workletDisconnected: false,
    frame: (f: Float32Array) => capture.onFrame?.(f),
  }
  const deps: SttDeps = {
    createAudioContext: () =>
      ({
        resume: () => Promise.resolve(),
        close: () => {
          state.ctxClosed = true
          return Promise.resolve()
        },
      }) as unknown as AudioContext,
    getUserMedia: () =>
      Promise.resolve({
        getTracks: () => [{ stop: () => (state.tracksStopped = true) }],
      }) as unknown as MediaStream,
    loadWorklet: (_ctx, _stream, onFrame) => {
      capture.onFrame = onFrame
      return Promise.resolve({
        disconnect: () => (state.workletDisconnected = true),
      })
    },
    connectSocket: () => new MockWebSocket() as unknown as SttSocketShim,
  }
  return { deps, state }
}

// SttSocket is structural: the mock satisfies it directly.
interface SttSocketShim {
  send(data: string | ArrayBuffer): void
  close(): void
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: (() => void) | null
  bufferedAmount: number
}

function makeOpts(overrides: Partial<SttSessionOpts> = {}): SttSessionOpts {
  return {
    onText: vi.fn(),
    onPartial: vi.fn(),
    onDone: vi.fn(),
    onListening: vi.fn(),
    onStatus: vi.fn(),
    onUtteranceStart: vi.fn(),
    getPromptTexts: () => [],
    ...overrides,
  }
}

beforeEach(() => {
  MockWebSocket.instances = []
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('floatTo16Le', () => {
  it('encodes clamp ×32767 round into exact little-endian bytes', () => {
    const bytes = floatTo16Le(new Float32Array([0, 1, -1, 2]))
    // -1 → 0x8001 (=-32767), NOT 0xFFFF: the codec scales by 32767.
    expect(Array.from(bytes)).toEqual([
      0x00, 0x00, // 0
      0xff, 0x7f, // 1 → 32767
      0x01, 0x80, // -1 → -32767 = 0x8001
      0xff, 0x7f, // 2 clamps to 1
    ])
  })
  it('empty input yields empty output', () => {
    expect(floatTo16Le(new Float32Array(0)).length).toBe(0)
  })
})

describe('joinSttText', () => {
  it('applies exactly the two splice rules', () => {
    expect(joinSttText('hello', 'world')).toBe('hello world')
    expect(joinSttText('你好', ' 是流式')).toBe('你好是流式') // CJK strips leading space
    expect(joinSttText('', '你好')).toBe('你好')
    expect(joinSttText('hi', '世界')).toBe('hi世界') // non-ASCII boundary: no space added
    expect(joinSttText('你好。', 'work')).toBe('你好。work') // neither rule applies
    expect(joinSttText('end', '世界')).toBe('end世界')
  })
})

describe('extractPromptTerms', () => {
  it('collects ASCII words in first-seen order, dedupes case-insensitively', () => {
    expect(extractPromptTerms(['你好 gbot 语音输入 test STT'])).toBe('gbot test STT')
    expect(extractPromptTerms(['GBOT gbot', 'gbot WUI'])).toBe('GBOT WUI')
  })
  it('filters short words; hyphenated words are single words', () => {
    expect(extractPromptTerms(['a ab abc 12 123 gbot-1'])).toBe('abc 123 gbot-1')
  })
  it('caps at maxTerms and at the character budget', () => {
    expect(extractPromptTerms(['one two three four'], 2, 100)).toBe('one two')
    expect(extractPromptTerms(['abcdefghij klmnopqrst uvwxyzabcd'], 12, 25)).toBe('abcdefghij klmnopqrst')
    expect(extractPromptTerms(['你好'])).toBe('')
  })
  it('stops at the first word that does not fit (no skip-and-continue)', () => {
    expect(extractPromptTerms(['abcdefghij klmnopqrst wxy'], 12, 14)).toBe('abcdefghij')
  })
  it('includes a word that lands exactly on the budget', () => {
    expect(extractPromptTerms(['abcdefghij wxy'], 12, 14)).toBe('abcdefghij wxy')
  })
})

describe('sttWsUrl', () => {
  it('derives scheme from the page protocol', () => {
    vi.stubGlobal('location', { protocol: 'https:', host: 's.example' })
    expect(sttWsUrl()).toBe('wss://s.example/ws/stt')
    vi.stubGlobal('location', { protocol: 'http:', host: 'h.example' })
    expect(sttWsUrl()).toBe('ws://h.example/ws/stt')
  })
})

describe('WORKLET_SRC contract', () => {
  it('registers the processor, stays self-contained, and resamples', () => {
    expect(WORKLET_SRC).toContain("registerProcessor('stt-capture'")
    expect(WORKLET_SRC).not.toContain('import ')
    expect(WORKLET_SRC).toContain('sampleRate')
    expect(WORKLET_SRC).toContain(String(STT_FRAME_SAMPLES))
  })
})

describe('default loadWorklet wiring', () => {
  it('loads via blob URL, wires port.onmessage to onFrame', async () => {
    const blobs: Blob[] = []
    vi.stubGlobal('URL', {
      createObjectURL: (b: Blob) => {
        blobs.push(b)
        return 'blob:x'
      },
    })
    let nodePortOnmessage: ((e: { data: Float32Array }) => void) | null = null
    let connected = false
    class FakeNode {
      port = {
        get onmessage() {
          return nodePortOnmessage
        },
        set onmessage(f: ((e: { data: Float32Array }) => void) | null) {
          nodePortOnmessage = f
        },
      }
      disconnect() {}
    }
    class FakeSource {
      connect() {
        connected = true
      }
      disconnect() {}
    }
    const addedModules: string[] = []
    const fakeCtx = {
      audioWorklet: {
        addModule: async (u: string) => {
          addedModules.push(u)
        },
      },
      createMediaStreamSource: () => new FakeSource(),
    } as unknown as AudioContext
    vi.stubGlobal('AudioWorkletNode', FakeNode)

    const { SttSession: S } = await import('./stt')
    const opts = makeOpts()
    const s = new S(opts)
    // The default wiring is private; exercise it directly on the session.
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const handle = await (s as any).defaultLoadWorklet(
      fakeCtx,
      {} as MediaStream,
      (f) => opts.onText(String(f[0])),
    )
    expect(addedModules).toEqual(['blob:x'])
    expect(connected).toBe(true)
    const blobText = await blobs[0].text()
    expect(blobText).toContain("registerProcessor('stt-capture'")
    nodePortOnmessage!({ data: new Float32Array([7]) })
    expect(opts.onText).toHaveBeenCalledWith('7')
    expect(typeof handle.disconnect).toBe('function')
  })
})

describe('SttSession', () => {
  async function startedSession(overrides: Partial<SttSessionOpts> = {}) {
    const opts = makeOpts(overrides)
    const { deps, state } = makeDeps()
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws = MockWebSocket.instances[0]
    return { s, opts, ws, state, deps }
  }

  // Real WebSocket: send() while CONNECTING throws InvalidStateError. The
  // start message must wait for the handshake, or the utterance dies before
  // onListening(true) — mic never turns green.
  it('CONNECTING socket: start waits for open before sending', async () => {
    const opts = makeOpts()
    const { deps } = makeDeps()
    let opened = false
    const sentText: string[] = []
    const sock = {
      readyState: 0, // CONNECTING
      bufferedAmount: 0,
      onopen: null as (() => void) | null,
      onerror: null as (() => void) | null,
      onclose: null as (() => void) | null,
      onmessage: null as ((ev: { data: unknown }) => void) | null,
      send(data: string | ArrayBuffer) {
        if (!opened) throw new DOMException('Still in CONNECTING state.', 'InvalidStateError')
        if (typeof data === 'string') sentText.push(data)
      },
      close() {},
    }
    const s = new SttSession({
      ...opts,
      deps: { ...deps, connectSocket: () => sock as unknown as SttSocket },
    })
    const pending = s.toggle() // handshake in flight — not awaited yet
    await vi.waitFor(() => expect(sock.onopen).toBeTruthy())
    opened = true
    sock.onopen!()
    await pending
    expect(sentText.some((d) => d.includes('"type":"start"'))).toBe(true)
    expect(opts.onListening).toHaveBeenCalledWith(true)
    expect(opts.onUtteranceStart).toHaveBeenCalled()
  })

  // Handshake death: onerror resolves the open wait without onopen, so the
  // recheck after the await must report the failure and release the mic —
  // previously the start frame went out on a dead socket and the mic stayed
  // hot with no feedback.
  it('socket dies mid-handshake: status + cleanup, start frame never sent', async () => {
    const opts = makeOpts()
    const { deps, state } = makeDeps()
    const sent: (string | ArrayBuffer)[] = []
    const sock = {
      bufferedAmount: 0,
      onopen: null as (() => void) | null,
      onerror: null as (() => void) | null,
      onclose: null as (() => void) | null,
      onmessage: null as ((ev: { data: unknown }) => void) | null,
      send(data: string | ArrayBuffer) {
        sent.push(data)
      },
      close() {},
    }
    const s = new SttSession({
      ...opts,
      deps: { ...deps, connectSocket: () => sock as unknown as SttSocketShim },
    })
    const pending = s.toggle() // handshake in flight — not awaited yet
    await vi.waitFor(() => expect(sock.onerror).toBeTruthy())
    sock.onerror!()
    await pending
    expect(opts.onStatus).toHaveBeenCalledWith(t('sttTranscribeFailed'))
    expect(opts.onListening).not.toHaveBeenCalledWith(true)
    expect(opts.onUtteranceStart).not.toHaveBeenCalled()
    expect(state.workletDisconnected).toBe(true)
    expect(state.tracksStopped).toBe(true)
    expect(state.ctxClosed).toBe(true)
    expect(sent.some((d) => typeof d === 'string' && d.includes('"type":"start"'))).toBe(false)
    expect(s.active).toBe(false)
  })

  it('hot path: start → delta → done auto-teardown', async () => {
    const { s, opts, ws } = await startedSession({ getPromptTexts: () => ['gbot voice'] })
    expect(opts.onListening).toHaveBeenCalledWith(true)
    expect(opts.onUtteranceStart).toHaveBeenCalled()
    const startMsg = JSON.parse(ws.sentText[0])
    expect(startMsg.type).toBe('start')
    expect(startMsg.prompt).toBe('gbot voice')
    expect(s.active).toBe(true)

    ws.dispatch('{"type":"ready","mode":"live"}')
    ws.dispatch('{"type":"delta","text":"你好"}')
    expect(opts.onText).toHaveBeenCalledWith('你好') // raw passthrough, no join
    ws.dispatch('{"type":"partial","text":"你好世"}')
    expect(opts.onPartial).toHaveBeenCalledWith('你好世')

    ws.dispatch('{"type":"done","text":"你好 全文"}')
    expect(opts.onDone).toHaveBeenCalledWith('你好 全文')
    expect(opts.onListening).toHaveBeenCalledWith(false)
    expect(s.active).toBe(false)
    expect(ws.closed).toBe(true)
  })

  it('sends captured frames as exact floatTo16Le bytes in order', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws = MockWebSocket.instances[0]
    ws.dispatch('{"type":"ready","mode":"live"}')
    cap.onFrame!(new Float32Array([0.25]))
    cap.onFrame!(new Float32Array([-0.25, 0.75]))
    const want = [floatTo16Le(new Float32Array([0.25])), floatTo16Le(new Float32Array([-0.25, 0.75]))]
    expect(ws.sentBinary.length).toBe(2)
    expect(new Uint8Array(ws.sentBinary[0])).toEqual(want[0])
    expect(new Uint8Array(ws.sentBinary[1])).toEqual(want[1])


  })

  it('buffers frames before ready and flushes in order', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws = MockWebSocket.instances[0]
    cap.onFrame!(new Float32Array([0.1]))
    cap.onFrame!(new Float32Array([0.2]))
    expect(ws.sentBinary.length).toBe(0) // nothing before ready
    ws.dispatch('{"type":"ready","mode":"live"}')
    expect(ws.sentBinary.length).toBe(2) // flushed in order

  })

  it('drops frames once pending exceeds 4MB', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws = MockWebSocket.instances[0]
    // One 3MB frame fits (≤4MB); a second would push the total past 4MB
    // and is dropped — only the capped prefix ever flushes.
    const big = new Float32Array((3 << 20) / 2)
    cap.onFrame!(big)
    cap.onFrame!(big)
    expect(ws.sentBinary.length).toBe(0)
    ws.dispatch('{"type":"ready","mode":"live"}')
    expect(ws.sentBinary.length).toBe(1)
    expect(ws.sentBinary[0].byteLength).toBe(3 << 20)

  })

  it('manual stop sends stop and tears down on done', async () => {
    const { s, opts, ws } = await startedSession()
    const p = s.toggle() // close direction
    expect(ws.sentText.some((m) => m.includes('"stop"'))).toBe(true)
    ws.dispatch('{"type":"done","text":"尾段"}')
    await p
    expect(opts.onDone).toHaveBeenCalledWith('尾段')
    expect(opts.onListening).toHaveBeenCalledWith(false)
    expect(s.active).toBe(false)
  })

  it('manual stop without done times out after 5s and reports', async () => {
    vi.useFakeTimers()
    const { s, opts } = await startedSession()

    const p = s.toggle()
    vi.advanceTimersByTime(5000)
    await p
    const msgs = (opts.onStatus as ReturnType<typeof vi.fn>).mock.calls.map((c) => c[0])
    expect(msgs.some((m: unknown) => typeof m === 'string' && m.length > 0)).toBe(true)
    expect(opts.onListening).toHaveBeenCalledWith(false)
  })

  it('error frame: status message + console.error + teardown', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { s, opts, ws } = await startedSession()
    ws.dispatch('{"type":"error","message":"upstream boom"}')
    expect(opts.onStatus).toHaveBeenCalled()
    expect(errSpy).toHaveBeenCalledWith('stt:', 'upstream boom')
    expect(opts.onListening).toHaveBeenCalledWith(false)
    expect(ws.closed).toBe(true)
    expect(s.active).toBe(false)
    errSpy.mockRestore()
  })

  it('unexpected socket close mid-utterance: status + teardown', async () => {
    const { s, opts, ws } = await startedSession()
    ws.dispatchClose()
    expect(opts.onStatus).toHaveBeenCalled()
    expect(opts.onListening).toHaveBeenCalledWith(false)
    expect(s.active).toBe(false)
  })

  it('bufferedAmount over 4MB drops frames until it drains', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws = MockWebSocket.instances[0]
    ws.dispatch('{"type":"ready","mode":"live"}')
    ws.bufferedAmount = (4 << 20) + 1
    cap.onFrame!(new Float32Array([0.5]))
    expect(ws.sentBinary.length).toBe(0)
    ws.bufferedAmount = 0
    cap.onFrame!(new Float32Array([0.5]))
    expect(MockWebSocket.instances[0].sentBinary.length).toBe(1)

  })

  it('getUserMedia reject: mic-denied status, ctx closed, no socket', async () => {
    const opts = makeOpts()
    let ctxClosed = false
    const deps: SttDeps = {
      createAudioContext: () =>
        ({
          resume: () => Promise.resolve(),
          close: () => {
            ctxClosed = true
            return Promise.resolve()
          },
        }) as unknown as AudioContext,
      getUserMedia: () => Promise.reject(new DOMException('denied', 'NotAllowedError')),
      connectSocket: () => {
        throw new Error('socket must not be dialed')
      },
    }
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    expect(opts.onStatus).toHaveBeenCalledWith(t('sttMicDenied'))
    expect(ctxClosed).toBe(true)
    expect(MockWebSocket.instances.length).toBe(0)
    expect(s.active).toBe(false)
  })

  it('loadWorklet reject: init-failed status, tracks stopped, ctx closed', async () => {
    const opts = makeOpts()
    let ctxClosed = false
    let tracksStopped = false
    const deps: SttDeps = {
      createAudioContext: () =>
        ({
          resume: () => Promise.resolve(),
          close: () => {
            ctxClosed = true
            return Promise.resolve()
          },
        }) as unknown as AudioContext,
      getUserMedia: () =>
        Promise.resolve({
          getTracks: () => [{ stop: () => (tracksStopped = true) }],
        }) as unknown as MediaStream,
      loadWorklet: () => Promise.reject(new Error('worklet boom')),
      connectSocket: () => {
        throw new Error('socket must not be dialed')
      },
    }
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    expect(opts.onStatus).toHaveBeenCalledWith(t('sttInitFailed'))
    expect(tracksStopped).toBe(true)
    expect(ctxClosed).toBe(true)
    expect(s.active).toBe(false)
  })

  it('toggle during assembly is a no-op; stop before start is inert', async () => {
    const opts = makeOpts()
    const { deps } = makeDeps()
    const s = new SttSession({ ...opts, deps })
    await expect(s.stop()).resolves.toBeUndefined() // never started: no throw
    const opts2 = makeOpts()
    const slowDeps: SttDeps = {
      ...deps,
      getUserMedia: () => new Promise<MediaStream>(() => {}), // never resolves
    }
    const s2 = new SttSession({ ...opts2, deps: slowDeps })
    void s2.toggle() // never resolves (getUserMedia pending forever)
    await s2.toggle() // assembling → no-op
    expect(s2.active).toBe(false)
  })

  // chat.ts calls stop() directly on connection loss — that races an
  // in-flight start() parked in the getUserMedia prompt. The teardown cannot
  // see the not-yet-assigned stream, so start() itself must refuse to
  // resurrect the session once torn.
  it('external stop() during assembly aborts start: mic released, no socket', async () => {
    const opts = makeOpts()
    let ctxClosed = false
    let tracksStopped = false
    let grantMic: ((s: MediaStream) => void) | null = null
    let parked: (() => void) | null = null
    const parkedAtGum = new Promise<void>((r) => {
      parked = r
    })
    const deps: SttDeps = {
      createAudioContext: () =>
        ({
          resume: () => Promise.resolve(),
          close: () => {
            ctxClosed = true
            return Promise.resolve()
          },
        }) as unknown as AudioContext,
      getUserMedia: () => {
        parked!() // start() has reached this await — signal before parking
        return new Promise<MediaStream>((resolve) => {
          grantMic = resolve
        })
      },
      loadWorklet: () => Promise.resolve({ disconnect: () => {} }),
      connectSocket: () => new MockWebSocket() as unknown as SttSocketShim,
    }
    const s = new SttSession({ ...opts, deps })
    const starting = s.toggle()
    await parkedAtGum // start() is now parked on the getUserMedia await
    await s.stop() // disconnect mid-assembly → fullTeardown, torn=true
    grantMic!({
      getTracks: () => [{ stop: () => (tracksStopped = true) }],
    } as unknown as MediaStream)
    await starting
    expect(tracksStopped).toBe(true)
    expect(ctxClosed).toBe(true)
    expect(MockWebSocket.instances.length).toBe(0)
    expect(opts.onUtteranceStart).not.toHaveBeenCalled()
    expect(s.active).toBe(false)
  })

  it('teardown disconnects worklet, stops tracks, closes ctx and socket', async () => {
    const { s, state, ws } = await startedSession()
    ws.dispatch('{"type":"done","text":""}')
    expect(state.workletDisconnected).toBe(true)
    expect(state.tracksStopped).toBe(true)
    expect(state.ctxClosed).toBe(true)
    expect(ws.closed).toBe(true)
    expect(s.active).toBe(false)
  })
})
