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
type FakeAnalyser = {
  data: Uint8Array
  fftSize?: number
  smoothingTimeConstant?: number
  connectedTo: unknown
  disconnected: boolean
  connect(target: unknown): void
  disconnect(): void
  getByteTimeDomainData(arr: Uint8Array): void
}

function makeDeps(capture: { onFrame?: (f: Float32Array) => void } = {}): {
  deps: SttDeps
  state: {
    ctxClosed: boolean
    tracksStopped: boolean
    workletDisconnected: boolean
    micSourceConnected: boolean
    micSourceDisconnected: boolean
    analyser: FakeAnalyser | null
    frame: (f: Float32Array) => void
  }
} {
  const state = {
    ctxClosed: false,
    tracksStopped: false,
    workletDisconnected: false,
    micSourceConnected: false,
    micSourceDisconnected: false,
    analyser: null as FakeAnalyser | null,
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
        createMediaStreamSource: (_stream: MediaStream) => ({
          connect: (_t: unknown) => (state.micSourceConnected = true),
          disconnect: () => (state.micSourceDisconnected = true),
        }),
        createAnalyser: () => {
          const analyser: FakeAnalyser = {
            data: new Uint8Array(1024).fill(128),
            connectedTo: null,
            disconnected: false,
            connect(target: unknown) {
              this.connectedTo = target
            },
            disconnect() {
              this.disconnected = true
            },
            getByteTimeDomainData(arr: Uint8Array) {
              arr.set(this.data.subarray(0, arr.length))
            },
          }
          state.analyser = analyser
          return analyser
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
  it('clamps values below -1 to -1 (0x8001), not the raw sample', () => {
    // An unclamped -2 would encode as round(-2*32767)+0x10000 = 2 → 02 00.
    expect(Array.from(floatTo16Le(new Float32Array([-2])))).toEqual([0x01, 0x80])
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
  it('empty prev or next never triggers a splice seat', () => {
    // prev[-1]/next[0] coerce to "undefined", which the ASCII regex matches —
    // only the emptiness guards keep the space from appearing.
    expect(joinSttText('', 'x')).toBe('x')
    expect(joinSttText('x', '')).toBe('x')
    expect(joinSttText('', '')).toBe('')
  })
  it('space-led next after ASCII prev keeps the space (rule 1 is CJK-only)', () => {
    expect(joinSttText('hello', ' world')).toBe('hello world')
  })
  it('CJK strip removes every leading space, and only leading ones', () => {
    expect(joinSttText('你好', '  two')).toBe('你好two')
    expect(joinSttText('你好', 'a b')).toBe('你好a b')
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
  it('the first word pays no separator seat: exact-budget first word fits', () => {
    expect(extractPromptTerms(['abcdefghij'], 12, 10)).toBe('abcdefghij')
  })
  it('a later word pays the +1 separator seat: exact-budget word stays out', () => {
    // 3 + (1+5) = 9 > 8 — without the seat the second word would sneak in.
    expect(extractPromptTerms(['abc defgh'], 12, 8)).toBe('abc')
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
    let srcDisconnected = false
    let nodeDisconnected = false
    const nodeCtorArgs: unknown[] = []
    class FakeNode {
      port = {
        get onmessage() {
          return nodePortOnmessage
        },
        set onmessage(f: ((e: { data: Float32Array }) => void) | null) {
          nodePortOnmessage = f
        },
      }
      constructor(_ctx: unknown, _name: unknown, opts: unknown) {
        nodeCtorArgs.push(opts)
      }
      disconnect() {
        nodeDisconnected = true
      }
    }
    class FakeSource {
      connect() {
        connected = true
      }
      disconnect() {
        srcDisconnected = true
      }
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
    // The blob must carry the JS MIME: some engines refuse text/javascript-less
    // blob module loads.
    expect(blobs[0].type).toBe('text/javascript')
    // Zero outputs: a pure sink node — the graph must not reserve a speaker leg.
    expect(nodeCtorArgs).toEqual([{ numberOfOutputs: 0 }])
    handle.disconnect()
    expect(srcDisconnected).toBe(true)
    expect(nodeDisconnected).toBe(true)
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

  // Socket whose handshake never auto-opens: tests drive onopen/onclose by
  // hand to park start() inside the open wait.
  function manualSock() {
    const sentText: string[] = []
    const sentBinary: ArrayBuffer[] = []
    const sock = {
      bufferedAmount: 0,
      onopen: null as (() => void) | null,
      onerror: null as (() => void) | null,
      onclose: null as (() => void) | null,
      onmessage: null as ((ev: { data: unknown }) => void) | null,
      send(data: string | ArrayBuffer) {
        if (typeof data === 'string') sentText.push(data)
        else sentBinary.push(data)
      },
      close() {},
    }
    return { sock, sentText, sentBinary }
  }

  it('stop before any start still announces listening:false (full teardown)', async () => {
    const opts = makeOpts()
    const { deps } = makeDeps()
    const s = new SttSession({ ...opts, deps })
    await s.stop()
    expect(opts.onListening).toHaveBeenCalledWith(false)
  })

  it('default AudioContext is created at 16kHz', async () => {
    const ctorArgs: unknown[] = []
    class FakeAC {
      constructor(arg: unknown) {
        ctorArgs.push(arg)
      }
      resume() {
        return Promise.resolve()
      }
      close() {
        return Promise.resolve()
      }
      createMediaStreamSource() {
        return { connect() {}, disconnect() {} }
      }
      createAnalyser() {
        return { connect() {}, disconnect() {}, getByteTimeDomainData() {} }
      }
    }
    vi.stubGlobal('AudioContext', FakeAC)
    const full = makeDeps().deps
    const deps: SttDeps = {
      getUserMedia: full.getUserMedia,
      loadWorklet: full.loadWorklet,
      connectSocket: full.connectSocket,
    }
    const s = new SttSession({ ...makeOpts(), deps })
    await s.toggle()
    // 16kHz is the model's rate: the worklet's sampleRate===16000 fast path
    // skips the resampler entirely.
    expect(ctorArgs).toEqual([{ sampleRate: 16000 }])
  })

  it('default getUserMedia requests echo-cancelled, noise-suppressed, AGC audio', async () => {
    const calls: unknown[] = []
    const stream = {
      getTracks: () => [{ stop: () => {} }],
    }
    vi.stubGlobal('navigator', {
      mediaDevices: {
        getUserMedia: (constraints: unknown) => {
          calls.push(constraints)
          return Promise.resolve(stream as unknown as MediaStream)
        },
      },
    })
    const full = makeDeps().deps
    const deps: SttDeps = {
      createAudioContext: full.createAudioContext,
      loadWorklet: full.loadWorklet,
      connectSocket: full.connectSocket,
    }
    const opts = makeOpts()
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    expect(calls).toEqual([
      { audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } },
    ])
    expect(opts.onListening).toHaveBeenCalledWith(true)
  })

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
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {})
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
    expect(logSpy).toHaveBeenCalledWith('[stt] 6 FAIL ws error')
    logSpy.mockRestore()
  })

  // A server that refuses the handshake can die with onclose only (no
  // onerror): the close must wake the open wait or the mic stays hot forever.
  it('socket closes mid-handshake without error: report, release, never announce listening', async () => {
    const opts = makeOpts()
    const { deps, state } = makeDeps()
    const { sock } = manualSock()
    const s = new SttSession({
      ...opts,
      deps: { ...deps, connectSocket: () => sock as unknown as SttSocketShim },
    })
    const pending = s.toggle()
    await vi.waitFor(() => expect(sock.onclose).toBeTruthy())
    sock.onclose!()
    await pending
    expect(opts.onStatus).toHaveBeenCalledWith(t('sttTranscribeFailed'))
    // The utterance never went live: a listening:false here would desync
    // the mic button state machine.
    expect(opts.onListening).not.toHaveBeenCalled()
    expect(state.workletDisconnected).toBe(true)
    expect(state.tracksStopped).toBe(true)
    expect(state.ctxClosed).toBe(true)
    expect(s.active).toBe(false)
  })

  // The voice page can close mid-handshake: teardown releases worklet/mic/
  // ctx before start() resumes, so the resume path must not re-release
  // (null-safe) and must not cry failure after a deliberate stop.
  it('external teardown during ws handshake: start aborts silently, no status, no throw', async () => {
    const opts = makeOpts()
    const { deps } = makeDeps()
    const { sock } = manualSock()
    const s = new SttSession({
      ...opts,
      deps: { ...deps, connectSocket: () => sock as unknown as SttSocketShim },
    })
    const pending = s.toggle()
    await vi.waitFor(() => expect(sock.onopen).toBeTruthy())
    s.teardown()
    sock.onopen!()
    await expect(pending).resolves.toBeUndefined()
    expect(opts.onStatus).not.toHaveBeenCalled()
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
    // Going live clears whatever status line the failed attempts left.
    expect(opts.onStatus).toHaveBeenCalledWith(null)

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

  it('ready breadcrumb names the mode, and ? when the server omits it', async () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {})
    const { ws } = await startedSession()
    ws.dispatch('{"type":"ready","mode":"live"}')
    expect(logSpy).toHaveBeenCalledWith('[stt] 7 ready (live) — speak')
    ws.dispatch('{"type":"ready"}')
    expect(logSpy).toHaveBeenCalledWith('[stt] 7 ready (?) — speak')
    logSpy.mockRestore()
  })

  it('textless delta/partial/done callbacks receive empty string, not undefined', async () => {
    const { s, opts, ws } = await startedSession()
    ws.dispatch('{"type":"delta"}')
    expect(opts.onText).toHaveBeenCalledWith('')
    ws.dispatch('{"type":"partial"}')
    expect(opts.onPartial).toHaveBeenCalledWith('')
    ws.dispatch('{"type":"done"}')
    expect(opts.onDone).toHaveBeenCalledWith('')
    expect(s.active).toBe(false)
  })

  // Voice-face mic leg: ptt rides the start frame. The dictation default
  // (no ptt opt) must stay byte-identical — {type,prompt} with no extra key.
  it('ptt start frame carries ptt:true; the default start frame stays bare', async () => {
    const bare = await startedSession({ getPromptTexts: () => ['bias'] })
    expect(JSON.parse(bare.ws.sentText[0])).toEqual({ type: 'start', prompt: 'bias' })

    const pttOpts = makeOpts({ ptt: true, getPromptTexts: () => ['bias'] })
    const { deps } = makeDeps()
    const pttSession = new SttSession({ ...pttOpts, deps })
    await pttSession.toggle()
    const pttWs = MockWebSocket.instances.at(-1)!
    expect(JSON.parse(pttWs.sentText[0])).toEqual({ type: 'start', prompt: 'bias', ptt: true })
    const stop = pttSession.stop()
    pttWs.dispatch('{"type":"done","text":"ok"}')
    await stop
  })

  // Listen-state aura volume source (⑤): a sink-only analyser taps the same
  // getUserMedia stream the capture worklet reads; micRMS() turns live
  // time-domain energy into the [0,1] the aura field consumes (readVol spec:
  // fftSize 1024, smoothing 0.55, RMS ×3.2 clamp 1). You speak, the field
  // swells; you go quiet, it settles to zero.
  it('mic analyser: sink-only tap, live RMS reads, teardown release', async () => {
    const opts = makeOpts({ ptt: true })
    const { deps, state } = makeDeps()
    const s = new SttSession({ ...opts, deps })
    await s.start()
    // The tap exists and connects ONLY source → analyser: probing the level
    // must never loop back into the uplink or the speakers.
    expect(state.micSourceConnected).toBe(true)
    expect(state.analyser).not.toBe(null)
    expect((state.analyser as unknown as { connectedTo: unknown }).connectedTo).toBe(null)
    // The tap is configured for the aura's readVol, not the raw default.
    expect(state.analyser!.fftSize).toBe(1024)
    expect(state.analyser!.smoothingTimeConstant).toBeCloseTo(0.55, 5)
    // Speech: ±18 around 128 → RMS 0.140625 ×3.2 = 0.45.
    for (let i = 0; i < state.analyser!.data.length; i++) {
      state.analyser!.data[i] = i % 2 === 0 ? 146 : 110
    }
    expect(s.micRMS()).toBeCloseTo(0.45, 5)
    // Louder speech reads clamped 1.
    for (let i = 0; i < state.analyser!.data.length; i++) {
      state.analyser!.data[i] = i % 2 === 0 ? 192 : 64
    }
    expect(s.micRMS()).toBe(1)
    // Silence (all 128) reads zero.
    state.analyser!.data.fill(128)
    expect(s.micRMS()).toBe(0)
    // Teardown releases the branch and the level reads dead.
    s.teardown()
    expect(state.micSourceDisconnected).toBe(true)
    expect(state.analyser!.disconnected).toBe(true)
    expect(s.micRMS()).toBe(0)
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

  // Double-tapping stop arms two timeouts but teardown clears the live one:
  // exactly one failure report, and both stop promises resolve.
  it('double stop while pending: one failure report, both stops resolve', async () => {
    vi.useFakeTimers()
    const { s, opts } = await startedSession()
    const p1 = s.toggle()
    const p2 = s.toggle()
    vi.advanceTimersByTime(5000)
    await p1
    await p2
    const fails = (opts.onStatus as ReturnType<typeof vi.fn>).mock.calls
      .map((c) => c[0])
      .filter((m: unknown) => typeof m === 'string')
    expect(fails.length).toBe(1)
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
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {})
    const { s, opts, ws } = await startedSession()
    ws.dispatchClose()
    expect(opts.onStatus).toHaveBeenCalled()
    expect(opts.onListening).toHaveBeenCalledWith(false)
    expect(s.active).toBe(false)
    expect(logSpy).toHaveBeenCalledWith('[stt] ws closed mid-utterance')
    logSpy.mockRestore()
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

  it('bufferedAmount exactly at 4MB still flows — the cap is >, not >=', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws = MockWebSocket.instances[0]
    ws.dispatch('{"type":"ready","mode":"live"}')
    ws.bufferedAmount = 4 << 20
    cap.onFrame!(new Float32Array([0.5]))
    expect(ws.sentBinary.length).toBe(1)
    expect(ws.sentBinary[0].byteLength).toBe(2)
  })

  it('pending exactly at 4MB still buffers both halves — the cap is >, not >=', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws = MockWebSocket.instances[0]
    const half = new Float32Array((2 << 20) / 2)
    cap.onFrame!(half)
    cap.onFrame!(half)
    expect(ws.sentBinary.length).toBe(0)
    ws.dispatch('{"type":"ready","mode":"live"}')
    expect(ws.sentBinary.length).toBe(2)
    expect(ws.sentBinary[0].byteLength).toBe(2 << 20)
    expect(ws.sentBinary[1].byteLength).toBe(2 << 20)
  })

  it('an empty captured frame sends nothing', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws = MockWebSocket.instances[0]
    ws.dispatch('{"type":"ready","mode":"live"}')
    cap.onFrame!(new Float32Array(0))
    expect(ws.sentBinary.length).toBe(0)
  })

  it('frames captured before the utterance goes live are dropped, not buffered', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const { sock, sentBinary } = manualSock()
    const s = new SttSession({
      ...opts,
      deps: { ...deps, connectSocket: () => sock as unknown as SttSocketShim },
    })
    const pending = s.toggle()
    await vi.waitFor(() => expect(sock.onopen).toBeTruthy())
    // Mic is already capturing while the handshake is still in flight.
    cap.onFrame!(new Float32Array([0.5]))
    sock.onopen!()
    await pending
    sock.onmessage!({ data: '{"type":"ready","mode":"live"}' })
    expect(sentBinary.length).toBe(0)
    expect(s.active).toBe(true)
  })

  it('getUserMedia reject: mic-denied status, ctx closed, no socket', async () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {})
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
    // The breadcrumb must name the DOMException, not its string form.
    expect(logSpy).toHaveBeenCalledWith('[stt] 2 FAIL getUserMedia: NotAllowedError')
    logSpy.mockRestore()
  })

  it('NotFoundError (no mic hardware) reports init-failed, not mic-denied', async () => {
    const opts = makeOpts()
    const deps: SttDeps = {
      createAudioContext: () =>
        ({
          resume: () => Promise.resolve(),
          close: () => Promise.resolve(),
        }) as unknown as AudioContext,
      getUserMedia: () => Promise.reject(new DOMException('no device', 'NotFoundError')),
      connectSocket: () => {
        throw new Error('socket must not be dialed')
      },
    }
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    expect(opts.onStatus).toHaveBeenCalledWith(t('sttInitFailed'))
    expect(s.active).toBe(false)
  })

  it('getUserMedia rejecting without a value: init-failed status, no crash', async () => {
    const opts = makeOpts()
    const deps: SttDeps = {
      createAudioContext: () =>
        ({
          resume: () => Promise.resolve(),
          close: () => Promise.resolve(),
        }) as unknown as AudioContext,
      getUserMedia: () => Promise.reject(undefined),
      connectSocket: () => {
        throw new Error('socket must not be dialed')
      },
    }
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    expect(opts.onStatus).toHaveBeenCalledWith(t('sttInitFailed'))
    expect(s.active).toBe(false)
  })

  it('loadWorklet reject: init-failed status, tracks stopped, ctx closed', async () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {})
    const opts = makeOpts()
    let ctxClosed = false
    let tracksStopped = false
    // >80 chars: the breadcrumb must carry the truncated prefix only.
    const boom = new Error('w'.repeat(100))
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
      loadWorklet: () => Promise.reject(boom),
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
    expect(logSpy).toHaveBeenCalledWith('[stt] 4 FAIL worklet: ' + String(boom).slice(0, 80))
    logSpy.mockRestore()
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

  it('fullTeardown is idempotent: done then explicit teardown stays one listening:false', async () => {
    const { s, opts, ws } = await startedSession()
    ws.dispatch('{"type":"done","text":""}')
    s.teardown()
    const offCalls = (opts.onListening as ReturnType<typeof vi.fn>).mock.calls.filter(
      (c) => c[0] === false,
    )
    expect(offCalls.length).toBe(1)
  })

  // Restart after a finished round: ready must reset, or the new round's
  // cold-model frames bypass the pending buffer and the server drops them.
  it('restart after teardown buffers frames until the fresh ready', async () => {
    const opts = makeOpts()
    const cap: { onFrame?: (f: Float32Array) => void } = {}
    const { deps } = makeDeps(cap)
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws1 = MockWebSocket.instances[0]
    ws1.dispatch('{"type":"ready","mode":"live"}')
    ws1.dispatch('{"type":"done","text":""}')
    expect(s.active).toBe(false)
    await s.toggle()
    const ws2 = MockWebSocket.instances[1]
    cap.onFrame!(new Float32Array([0.5]))
    expect(ws2.sentBinary.length).toBe(0)
    ws2.dispatch('{"type":"ready","mode":"live"}')
    expect(ws2.sentBinary.length).toBe(1)
  })

  it('stop timer cleared on teardown: a later restart round survives the stale timeout', async () => {
    vi.useFakeTimers()
    const opts = makeOpts()
    const { deps } = makeDeps()
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws1 = MockWebSocket.instances[0]
    const p = s.toggle()
    ws1.dispatch('{"type":"done","text":"x"}')
    await p
    await s.toggle()
    const ws2 = MockWebSocket.instances[1]
    expect(s.active).toBe(true)
    vi.advanceTimersByTime(5000)
    // The stale timer firing here would report failure and tear down the
    // live restart round.
    expect(opts.onStatus).not.toHaveBeenCalledWith(t('sttTranscribeFailed'))
    expect(s.active).toBe(true)
    expect(ws2.closed).toBe(false)
  })

  // Stale first stop timer after done resolved only the second
  // stop (fullTeardown clears the LATEST stopTimer, not the overwritten
  // earlier handle) — the 313 torn branch is the only resolver for p1, and a
  // mutated branch reports a spurious failure after a successful done.
  it('stale first stop timer after done: p1 resolves, no spurious failure', async () => {
    vi.useFakeTimers()
    const opts = makeOpts()
    const { deps } = makeDeps()
    const s = new SttSession({ ...opts, deps })
    await s.toggle()
    const ws1 = MockWebSocket.instances[0]
    const p1 = s.toggle() // arms t1 (fires at 5000)
    vi.advanceTimersByTime(1000)
    const p2 = s.toggle() // arms t2 (fires at 6000); stopTimer handle now t2
    ws1.dispatch('{"type":"done","text":"ok"}') // fullTeardown clears t2 only
    await p2
    vi.advanceTimersByTime(5000) // stale t1 fires at t=5000 with torn=true
    await p1
    expect(opts.onStatus).not.toHaveBeenCalledWith(t('sttTranscribeFailed'))
  }, 3000)

  it('diag breadcrumb carries the [stt] prefix', () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {})
    const s = new SttSession(makeOpts())
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(s as any).diag('mic granted')
    expect(logSpy).toHaveBeenCalledWith('[stt] mic granted')
    logSpy.mockRestore()
  })
})
