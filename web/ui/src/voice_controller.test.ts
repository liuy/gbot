import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  reduceVoiceState,
  createVoiceController,
  type VoiceState,
  type VoiceMicLike,
} from './voice_controller'
import { createEventBlockStore } from './components/event_blocks'
import type { TTSQueue, TTSQueueOpts } from './voice_tts'
import type { Playback } from './voice_playback'

// ── fakes ────────────────────────────────────────────────────────────────

// The TTS queue fake: enqueue/abort are observed, and the test drives the
// two callbacks that matter to the controller — write (PCM arrives) and
// settle (one item finished). inFlightCount DECREMENTS on settle: it reaches
// 0 only when every enqueued item settled, exactly like the real queue's
// drain semantics.
class FakeTTSQueue implements TTSQueue {
  texts: string[] = []
  aborts = 0
  inFlightCount = 0
  opts: TTSQueueOpts
  constructor(opts: TTSQueueOpts) {
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
  // test drivers
  emitPCM(bytes: Uint8Array): void {
    this.opts.write(bytes)
  }
  settle(): void {
    if (this.inFlightCount === 0) return
    this.inFlightCount--
    this.opts.onSettled?.()
    // Mirrors the real queue: the settle that empties the queue is the
    // fetch-settle EOS signal.
    if (this.inFlightCount === 0) this.opts.onEOS?.()
  }
  emitError(message: string): void {
    this.opts.onError?.(message)
  }
}

// Mic leg fake: start/stop per round; done drives the transcript path.
// micRMS simulates the capture analyser's live reading.
class FakeMic implements VoiceMicLike {
  startCalls = 0
  stopCalls = 0
  teardownCalls = 0
  micRMSValue = 0
  onDone: (text: string) => void
  constructor(onDone: (text: string) => void) {
    this.onDone = onDone
  }
  start(): Promise<void> {
    this.startCalls++
    return Promise.resolve()
  }
  stop(): Promise<void> {
    this.stopCalls++
    return Promise.resolve()
  }
  teardown(): void {
    this.teardownCalls++
  }
  micRMS(): number {
    return this.micRMSValue
  }
  emitFinal(text: string): void {
    this.onDone(text)
  }
}

function fakePlayback() {
  const writes: Uint8Array[] = []
  let interrupted = 0
  let ensured = 0
  let ended = 0
  let level = 0
  let drainedCb: (() => void) | null = null
  const pb: Playback = {
    ensureAudio: () => {
      ensured++
      return Promise.resolve()
    },
    write: (b: Uint8Array) => writes.push(b),
    interrupt: () => interrupted++,
    end: () => {
      ended++
    },
    onDrained: (cb: () => void) => {
      drainedCb = cb
    },
    rms: () => level,
    teardown: () => Promise.resolve(),
  }
  return {
    pb,
    writes,
    setLevel: (v: number) => {
      level = v
    },
    get interrupted() {
      return interrupted
    },
    get ensured() {
      return ensured
    },
    get endCalls() {
      return ended
    },
    drain() {
      drainedCb?.()
    },
  }
}

function makeConn() {
  const sent: object[] = []
  const listeners: ((m: unknown) => void)[] = []
  return {
    conn: {
      subscribe: (fn: (m: unknown) => void) => {
        listeners.push(fn)
        return () => {
          const i = listeners.indexOf(fn)
          if (i >= 0) listeners.splice(i, 1)
        }
      },
      send: (p: object) => sent.push(p),
    },
    sent,
    emit: (m: unknown) => listeners.forEach((l) => l(m)),
    listenerCount: () => listeners.length,
  }
}

function setup() {
  const c = makeConn()
  const pb = fakePlayback()
  let ttsQueue: FakeTTSQueue | null = null
  let mic: FakeMic | null = null
  const states: VoiceState[] = []
  const utterances: string[] = []
  const blockStore = createEventBlockStore()
  const controller = createVoiceController({
    conn: c.conn as never,
    createTTSQueue: (opts) => (ttsQueue = new FakeTTSQueue(opts)),
    createMic: (opts) => (mic = new FakeMic(opts.onDone)),
    createPlayback: () => pb.pb,
    getPromptTexts: () => [],
    blockStore,
    onState: (s) => states.push(s),
    sendUtterance: (t: string) => {
      utterances.push(t)
    },
  })
  return {
    controller, conn: c, pb, utterances, blockStore,
    mic: () => mic!, ttsQueue: () => ttsQueue!, states,
  }
}

const ev = (type: string, text?: string) => ({ type: 'event', event: { type, text } })

// One speakable sentence (terminal 。, >8 bytes — flushes on its own).
const SENTENCE = '这是一句足够长的回答正文。'

// ── reducer table (D6) ───────────────────────────────────────────────────

describe('reduceVoiceState', () => {
  const hold = (held: boolean) => ({ kind: 'hold', held } as const)
  const engine = (type: string) => ({ kind: 'engine', type } as const)

  it('hold from idle/wait/think/tool enters listen; speak rejects', () => {
    expect(reduceVoiceState('idle', hold(true))).toBe('listen')
    expect(reduceVoiceState('wait', hold(true))).toBe('listen')
    expect(reduceVoiceState('think', hold(true))).toBe('listen')
    expect(reduceVoiceState('tool', hold(true))).toBe('listen')
    expect(reduceVoiceState('speak', hold(true))).toBe('speak')
  })

  it('release from listen enters wait; release elsewhere is a no-op', () => {
    expect(reduceVoiceState('listen', hold(false))).toBe('wait')
    expect(reduceVoiceState('think', hold(false))).toBe('think')
    expect(reduceVoiceState('speak', hold(false))).toBe('speak')
  })

  it('engine mapping: think/tool/speak + query_start keeps wait', () => {
    expect(reduceVoiceState('wait', engine('query_start'))).toBe('wait')
    expect(reduceVoiceState('wait', engine('thinking_start'))).toBe('think')
    expect(reduceVoiceState('think', engine('thinking_end'))).toBe('wait')
    expect(reduceVoiceState('wait', engine('text_start'))).toBe('speak')
    expect(reduceVoiceState('wait', engine('tool_start'))).toBe('tool')
    expect(reduceVoiceState('tool', engine('tool_end'))).toBe('wait')
  })

  it('engine events never demote listen — the hold gesture owns it until release', () => {
    // thinking_end/tool_end map to wait, but a user holding the mic keeps
    // listen: the gesture source owns listen and only release() may end it.
    expect(reduceVoiceState('listen', engine('thinking_end'))).toBe('listen')
    expect(reduceVoiceState('listen', engine('tool_end'))).toBe('listen')
    // release is still the exit: listen → wait.
    expect(reduceVoiceState('listen', hold(false))).toBe('wait')
  })

  it('query_end: speak holds for drain, everything else idles', () => {
    expect(reduceVoiceState('speak', engine('query_end'))).toBe('speak')
    expect(reduceVoiceState('wait', engine('query_end'))).toBe('idle')
    expect(reduceVoiceState('think', engine('query_end'))).toBe('idle')
    expect(reduceVoiceState('tool', engine('query_end'))).toBe('idle')
  })

  it('drained: speak falls to idle', () => {
    expect(reduceVoiceState('speak', { kind: 'drained' })).toBe('idle')
    expect(reduceVoiceState('wait', { kind: 'drained' })).toBe('wait')
  })
})

// ── controller ───────────────────────────────────────────────────────────

describe('VoiceController', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('cold start: construction creates no AudioContext; open does (mount click gesture)', async () => {
    const { controller, pb, mic, ttsQueue } = setup()
    expect(controller.state()).toBe('idle')
    expect(pb.ensured).toBe(0)
    controller.open({ autoListen: false })
    // open rides the mount click's user activation — the worklet is warm
    // long before the first TTS PCM arrives.
    expect(pb.ensured).toBe(1)
    controller.hold(true)
    await vi.runAllTimersAsync()
    expect(pb.ensured).toBe(1) // open is the single trigger; hold never re-arms
    expect(mic().startCalls).toBe(1)
    expect(ttsQueue().texts).toEqual([]) // no tts traffic from a hold
    expect(controller.state()).toBe('listen')
    controller.hold(false)
    expect(mic().stopCalls).toBe(1)
    expect(controller.state()).toBe('wait')
  })

  // 10-28 final ruling: voice mode is retired — opening sends nothing. A final
  // routes to deps.sendUtterance (the chat's single commit point); the
  // transient voice:true flag is sendUserMessage's concern, pinned at the chat
  // level (chat.test.ts voice face cases), and the connector prefixes the
  // marker onto the text server-side.
  it('open sends nothing; every final routes to sendUtterance', () => {
    const { controller, conn, mic, utterances } = setup()
    controller.open({ autoListen: false })
    expect(conn.sent).toEqual([])
    controller.hold(true)
    controller.hold(false)
    mic().emitFinal('你好世界。')
    expect(utterances).toEqual(['你好世界。'])
    controller.hold(true)
    controller.hold(false)
    mic().emitFinal('第二句。')
    expect(utterances).toEqual(['你好世界。', '第二句。'])
    expect(controller.state()).toBe('wait')
  })

  // Teardown sends nothing (the off frame died with voice mode): finals of the
  // next session route to sendUtterance exactly like the first session's, and
  // the controller itself writes zero frames to the socket.
  it('teardown sends no frames; the next session\'s finals still route to sendUtterance', () => {
    const { controller, conn, mic, utterances } = setup()
    controller.open({ autoListen: false })
    controller.hold(true)
    controller.hold(false)
    mic().emitFinal('一。')
    expect(utterances).toEqual(['一。'])
    controller.teardown()
    expect(conn.sent).toEqual([])
    controller.open({ autoListen: false })
    controller.hold(true)
    controller.hold(false)
    mic().emitFinal('二。')
    expect(utterances).toEqual(['一。', '二。'])
  })

  // ⑤: one wave renderer, two signal sources — listen reads the mic
  // analyser (you speak, the band moves), every other state keeps the TTS
  // playback analyser (speak behavior unchanged).
  it('level() reads the mic analyser in listen, the TTS analyser elsewhere', async () => {
    const { controller, conn, mic, pb } = setup()
    pb.setLevel(0.4)
    controller.open({ autoListen: false })
    // idle: playback source (silent before any audio).
    expect(controller.level()).toBe(0.4)
    // hold → listen: the mic's live energy drives the wave band.
    controller.hold(true)
    mic().micRMSValue = 0.7
    expect(controller.state()).toBe('listen')
    expect(controller.level()).toBe(0.7)
    mic().micRMSValue = 0.1
    expect(controller.level()).toBe(0.1)
    // release → wait: back to the playback source.
    controller.hold(false)
    expect(controller.state()).toBe('wait')
    expect(controller.level()).toBe(0.4)
    // speak: TTS analyser, unchanged by the mic reading.
    conn.emit(ev('text_start'))
    mic().micRMSValue = 0.9
    expect(controller.state()).toBe('speak')
    expect(controller.level()).toBe(0.4)
  })

  it('speak rejects hold (no barge-in)', async () => {
    const { controller, conn, mic } = setup()
    controller.open({ autoListen: false })
    conn.emit(ev('text_start'))
    expect(controller.state()).toBe('speak')
    controller.hold(true)
    await vi.runAllTimersAsync()
    expect(mic().startCalls).toBe(0)
    expect(controller.state()).toBe('speak')
  })

  it('hold while the previous release is still settling is ignored', async () => {
    const { controller, mic } = setup()
    controller.open({ autoListen: false })
    controller.hold(true)
    expect(mic().startCalls).toBe(1)
    // A stop whose done never arrives yet — the release stays in flight.
    let releaseStop: () => void = () => {}
    const pendingStop = new Promise<void>((resolve) => {
      releaseStop = resolve
    })
    const stopSpy = vi.spyOn(mic(), 'stop').mockReturnValueOnce(pendingStop)
    controller.hold(false)
    expect(stopSpy.mock.calls.length).toBe(1)
    // Quick re-tap during the settle window: no new round may start.
    controller.hold(true)
    expect(mic().startCalls).toBe(1)
    releaseStop()
    await vi.runAllTimersAsync()
    controller.hold(true)
    expect(mic().startCalls).toBe(2)
  })

  // Hot path, ordered feed end to end: thinking is silent (10-31 ruling —
  // the hum played multiple times per turn and felt wrong), the run batches
  // into ONE POST at text_end, and query_end NEVER aborts a round with body.
  it('hot path: thinking stays silent, one POST at text_end, drain gate holds', () => {
    const { controller, conn, pb, ttsQueue } = setup()
    controller.open({ autoListen: false })
    controller.hold(true)
    controller.hold(false)
    conn.emit(ev('query_start'))
    conn.emit(ev('thinking_start'))
    conn.emit(ev('thinking_end'))
    expect(ttsQueue().texts).toEqual([]) // thinking is silent now
    conn.emit(ev('text_start'))
    conn.emit(ev('text_delta', '**你好**世界。'))
    // Mid-run — even past a sentence delimiter — nothing POSTs: batching
    // waits for the run boundary (per-sentence requests made prosody drift;
    // qwen3-tts has no cross-request context).
    expect(ttsQueue().texts).toEqual([])
    conn.emit(ev('text_delta', '再见。'))
    conn.emit(ev('text_end'))
    // One contiguous run, one POST, markdown projected away.
    expect(ttsQueue().texts).toEqual(['你好世界。再见。'])
    ttsQueue().emitPCM(new Uint8Array([1]))
    expect(pb.writes).toEqual([new Uint8Array([1])])
    conn.emit(ev('query_end'))
    expect(ttsQueue().aborts).toBe(0) // body enqueued: the round must drain
    expect(controller.state()).toBe('speak') // items still in flight
    ttsQueue().settle()
    expect(controller.state()).toBe('speak') // the worklet still holds PCM
    pb.drain()
    expect(controller.state()).toBe('idle')
  })

  // Either arrival order settles the round: playback drains before the last
  // queue item settles.
  it('hot path settles with drain-before-settle arrival too', () => {
    const { controller, conn, pb, ttsQueue } = setup()
    controller.open({ autoListen: false })
    controller.hold(true)
    controller.hold(false)
    conn.emit(ev('query_start'))
    conn.emit(ev('text_start'))
    conn.emit(ev('text_delta', SENTENCE))
    conn.emit(ev('text_end'))
    expect(ttsQueue().texts).toEqual([SENTENCE])
    ttsQueue().emitPCM(new Uint8Array([1]))
    conn.emit(ev('query_end'))
    expect(controller.state()).toBe('speak')
    pb.drain() // worklet finished first
    expect(controller.state()).toBe('speak') // queue item still in flight
    ttsQueue().settle()
    expect(controller.state()).toBe('idle')
  })

  // Think-only round (10-31 ruling: the hum is gone, thinking is silent):
  // nothing is ever enqueued, so there is nothing to abort — the round
  // settles straight to idle with zero tts traffic.
  it('think-only round: zero tts traffic, no abort, settles idle', () => {
    const { controller, conn, pb, ttsQueue } = setup()
    controller.open({ autoListen: false })
    controller.hold(true)
    controller.hold(false)
    conn.emit(ev('query_start'))
    conn.emit(ev('thinking_start'))
    conn.emit(ev('thinking_end'))
    conn.emit(ev('query_end')) // sub-second think
    expect(ttsQueue().texts).toEqual([])
    expect(ttsQueue().aborts).toBe(0)
    expect(pb.writes.length).toBe(0)
    expect(pb.interrupted).toBe(0)
    expect(controller.state()).toBe('idle')
  })

  // A round that enqueued body text is NEVER aborted at query_end — the
  // final run flushed by query_end is body too.
  it('a final run flushed at query_end is enqueued, never aborted', () => {
    const { controller, conn, ttsQueue } = setup()
    controller.open({ autoListen: false })
    conn.emit(ev('query_start'))
    conn.emit(ev('thinking_start'))
    // Unterminated text: buffered until a boundary, emitted only by the
    // query_end flush.
    conn.emit(ev('text_delta', '这是一段没有句号的长回答正文'))
    expect(ttsQueue().texts).toEqual([])
    conn.emit(ev('query_end'))
    expect(ttsQueue().texts).toEqual(['这是一段没有句号的长回答正文'])
    expect(ttsQueue().aborts).toBe(0)
  })

  it('no-think turn: the run buffers and speaks as one POST at text_end', () => {
    const { controller, conn, ttsQueue } = setup()
    controller.open({ autoListen: false })
    controller.hold(true)
    controller.hold(false)
    conn.emit(ev('query_start'))
    conn.emit(ev('text_start'))
    conn.emit(ev('text_delta', SENTENCE))
    expect(ttsQueue().texts).toEqual([]) // buffered mid-run
    conn.emit(ev('text_end'))
    expect(ttsQueue().texts).toEqual([SENTENCE])
  })

  // Tool use splits the reply into contiguous runs: the preamble must POST
  // at tool_start (it must sound before the tool runs), the post-tool text
  // is its own run — exactly two POSTs for a text→tool→text reply.
  it('text → tool_start → text → text_end batches into exactly two POSTs', () => {
    const { controller, conn, ttsQueue } = setup()
    controller.open({ autoListen: false })
    conn.emit(ev('query_start'))
    conn.emit(ev('text_start'))
    conn.emit(ev('text_delta', '我先查一下。'))
    expect(ttsQueue().texts).toEqual([]) // run buffer holds past the delimiter
    conn.emit({ type: 'event', event: { type: 'tool_start', tool_use: { name: 'bash' } } })
    expect(ttsQueue().texts).toEqual(['我先查一下。'])
    conn.emit(ev('tool_end'))
    conn.emit(ev('text_start'))
    conn.emit(ev('text_delta', '结果是这样的。'))
    conn.emit(ev('text_end'))
    conn.emit(ev('query_end'))
    expect(ttsQueue().texts).toEqual(['我先查一下。', '结果是这样的。'])
  })

  // Runs whose projection is empty synthesize nothing: whitespace-only text
  // and fully-swallowed content (a lone code fence) never hit /api/tts.
  it('empty projection: whitespace run and code-fence-only run POST nothing', () => {
    const { controller, conn, ttsQueue } = setup()
    controller.open({ autoListen: false })
    conn.emit(ev('query_start'))
    conn.emit(ev('text_start'))
    conn.emit(ev('text_delta', ' \n\n '))
    conn.emit(ev('text_end'))
    conn.emit(ev('text_delta', '```\nls -la\n```'))
    conn.emit(ev('query_end'))
    expect(ttsQueue().texts).toEqual([])
  })

  // Thinking rounds and mic finals synthesize nothing: every tts POST is
  // body text (10-31 ruling removed the hum).
  it('thinking rounds enqueue nothing; a mic final alone enqueues nothing', () => {
    const { controller, conn, mic, ttsQueue } = setup()
    controller.open({ autoListen: false })
    controller.hold(true)
    mic().emitFinal('问题。') // the final itself: no prefetch
    expect(ttsQueue().texts).toEqual([])
    conn.emit(ev('query_start'))
    conn.emit(ev('thinking_start'))
    conn.emit(ev('thinking_end'))
    conn.emit(ev('tool_start'))
    conn.emit(ev('tool_end'))
    conn.emit(ev('thinking_start')) // a tool-triggered second thinking round
    conn.emit(ev('thinking_end'))
    expect(ttsQueue().texts).toEqual([])
  })

  // The tool preamble must sound BEFORE the tool runs: buffered text is
  // force-flushed into the queue at tool_start.
  it('tool preamble: tool_start force-flushes the buffered sentence into the queue', () => {
    const { controller, conn, ttsQueue } = setup()
    controller.open({ autoListen: false })
    conn.emit(ev('query_start'))
    conn.emit(ev('text_delta', '这是一段没有句号的长回答正文'))
    expect(ttsQueue().texts).toEqual([]) // no terminal delimiter: buffered
    conn.emit({ type: 'event', event: { type: 'tool_start', tool_use: { name: 'bash' } } })
    expect(ttsQueue().texts).toEqual(['这是一段没有句号的长回答正文'])
  })

  // Render-side rewiring: the discrete onTool/onThink* callbacks are gone —
  // engine events reduce into the shared block store (the same pipeline the
  // task panel uses), and the voice face renders from it.
  it('engine events reduce into the shared block store (think + tool rows)', () => {
    const { controller, conn, blockStore } = setup()
    controller.open({ autoListen: false })
    expect(blockStore.blocks()).toEqual([])
    conn.emit(ev('query_start'))
    conn.emit(ev('thinking_start'))
    conn.emit({ type: 'event', event: { type: 'thinking_delta', thinking: { text: '先想' } } })
    conn.emit({ type: 'event', event: { type: 'thinking_delta', thinking: { text: '一步' } } })
    expect(blockStore.blocks()[0]).toMatchObject({ kind: 'thinking', text: '先想一步', active: true })
    conn.emit({ type: 'event', event: { type: 'thinking_end', thinking: { duration: 1_500_000_000 } } })
    expect(blockStore.blocks()[0]).toMatchObject({
      kind: 'thinking', text: '先想一步', active: false, durationNs: 1_500_000_000,
    })
    // Tool rows: param deltas stream into the summary (previously dropped —
    // the controller had no tool_param_delta case) and tool_end stamps the
    // elapsed duration (previously the row never completed).
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
    conn.emit({ type: 'event', event: { type: 'tool_start', tool_use: { id: 't1', name: 'web', input: {} } } })
    conn.emit({
      type: 'event',
      event: { type: 'tool_param_delta', partial_input: { id: 't1', name: 'web', delta: '{}', summary: '杭州 天气', is_search: true } },
    })
    // Output streams live into the running row (reviewer fix: the controller
    // previously dropped tool_output_delta — rows stayed blank mid-run).
    conn.emit({
      type: 'event',
      event: { type: 'tool_output_delta', tool_result: { tool_use_id: 't1', display_output: 'fetching…' } },
    })
    expect(blockStore.blocks()[1]).toMatchObject({ kind: 'tool', id: 't1', displayOutput: 'fetching…' })
    vi.setSystemTime(new Date('2026-01-01T00:00:02Z'))
    conn.emit({
      type: 'event',
      event: { type: 'tool_end', tool_result: { tool_use_id: 't1', output: '', display_output: 'Sunny' } },
    })
    expect(blockStore.blocks()[1]).toMatchObject({
      kind: 'tool', id: 't1', name: 'web', summary: '杭州 天气', isSearch: true,
      state: 'done', timingNs: 2_000_000_000, displayOutput: 'Sunny',
    })
    // A fresh query does not clear the store — clearing is the page's
    // speak/listen/idle semantics.
    conn.emit(ev('query_start'))
    expect(blockStore.blocks()).toHaveLength(2)
  })

  it('teardown while playing: interrupt, queue abort, mic teardown, no frames, unsubscribe', async () => {
    const { controller, conn, pb, ttsQueue, mic } = setup()
    controller.open({ autoListen: false })
    conn.emit(ev('text_start'))
    controller.teardown()
    expect(pb.interrupted).toBe(1)
    expect(ttsQueue().aborts).toBe(1)
    expect(mic().teardownCalls).toBe(1)
    // Voice mode is retired: teardown must not send anything.
    expect(conn.sent).toEqual([])
    expect(conn.listenerCount()).toBe(0)
    expect(controller.state()).toBe('idle')
    // Reusable after teardown.
    controller.open({ autoListen: false })
    expect(controller.state()).toBe('idle')
  })

  it('a late mic done after teardown sends nothing', () => {
    const { controller, conn, mic } = setup()
    controller.open({ autoListen: false })
    controller.hold(true)
    controller.hold(false)
    controller.teardown()
    mic().emitFinal('迟到的识别。')
    expect(conn.sent.filter((m) => (m as { type?: string }).type === 'message')).toEqual([])
  })

  it('external teardown (session switch) fires onTeardown and resets', () => {
    const onTeardown = vi.fn()
    const { controller, conn, ttsQueue, pb } = setup()
    // Rebuild with the teardown hook: setup() has no slot for it, so assert
    // via a second controller wired to the same fake conn shape.
    const c2 = makeConn()
    let queue2: FakeTTSQueue | null = null
    const controller2 = createVoiceController({
      conn: c2.conn as never,
      createTTSQueue: (opts) => (queue2 = new FakeTTSQueue(opts)),
      createMic: (opts) => new FakeMic(opts.onDone),
      createPlayback: () => pb.pb,
      getPromptTexts: () => [],
      blockStore: createEventBlockStore(),
      sendUtterance: () => {},
      currentSessionID: () => 'sess-a',
      onTeardown,
    })
    controller2.open({ autoListen: false })
    expect(c2.sent).toEqual([]) // lazy marking: opening sends nothing
    c2.emit({ type: 'connect_status', connected: true, sessionID: 'sess-b' })
    expect(onTeardown).toHaveBeenCalledTimes(1)
    expect(queue2!.aborts).toBe(1)
    expect(controller2.state()).toBe('idle')
    // Page-initiated teardown does NOT re-fire onTeardown.
    onTeardown.mockClear()
    controller2.open({ autoListen: false })
    controller2.teardown()
    expect(onTeardown).not.toHaveBeenCalled()
    void controller
    void conn
    void ttsQueue
  })

  // EOS root fix wiring: the queue's settle-driven end signal forwards to
  // playback.end() — the worklet must learn the stream ended from the fetch
  // settle, never from a silence guess.
  it('queue EOS forwards to playback.end() exactly when the queue empties', () => {
    const { controller, conn, pb, ttsQueue } = setup()
    controller.open({ autoListen: false })
    conn.emit(ev('query_start'))
    conn.emit(ev('text_start'))
    conn.emit(ev('text_delta', SENTENCE))
    conn.emit(ev('text_end'))
    conn.emit(ev('query_end'))
    expect(pb.endCalls).toBe(0) // the item is still in flight
    ttsQueue().emitPCM(new Uint8Array([1])) // PCM is in the worklet
    ttsQueue().settle()
    expect(pb.endCalls).toBe(1)
    expect(controller.state()).toBe('speak') // PCM still in the worklet
    pb.drain()
    expect(controller.state()).toBe('idle')
  })

  it('a TTS queue error routes to deps.onTTSError with the message only', () => {
    const c = makeConn()
    let queue: FakeTTSQueue | null = null
    const onTTSError = vi.fn()
    const controller = createVoiceController({
      conn: c.conn as never,
      createTTSQueue: (opts) => (queue = new FakeTTSQueue(opts)),
      createMic: (opts) => new FakeMic(opts.onDone),
      createPlayback: () => fakePlayback().pb,
      getPromptTexts: () => [],
      blockStore: createEventBlockStore(),
      sendUtterance: () => {},
      onTTSError,
    })
    controller.open({ autoListen: false })
    queue!.emitError('tts: no default voice')
    expect(onTTSError).toHaveBeenCalledWith('tts: no default voice')
  })

  it('a failed audio prime surfaces the error and the round settles without a drain', async () => {
    const c = makeConn()
    let queue: FakeTTSQueue | null = null
    const onTTSError = vi.fn()
    const pb = fakePlayback()
    const livePb = fakePlayback()
    const deadPb = { ...pb.pb, ensureAudio: () => Promise.reject(new Error('worklet blocked')) }
    let useDead = true
    const controller = createVoiceController({
      conn: c.conn as never,
      createTTSQueue: (opts) => (queue = new FakeTTSQueue(opts)),
      createMic: (opts) => new FakeMic(opts.onDone),
      createPlayback: () => (useDead ? deadPb : livePb.pb),
      getPromptTexts: () => [],
      blockStore: createEventBlockStore(),
      sendUtterance: () => {},
      onTTSError,
    })
    controller.open({ autoListen: false })
    await vi.runAllTimersAsync()
    expect(onTTSError).toHaveBeenCalledWith('tts: audio unavailable')
    // The round must settle to idle with no drain — drained can never fire
    // when the worklet never loaded (the pre-fix wedge: stuck in speak,
    // mic blocked by no-barge-in until the page closes).
    c.emit(ev('query_start'))
    c.emit(ev('text_start'))
    c.emit(ev('text_delta', SENTENCE))
    c.emit(ev('text_end'))
    c.emit(ev('query_end'))
    queue!.emitPCM(new Uint8Array([1]))
    queue!.settle()
    expect(controller.state()).toBe('idle')
    // audioDead must not leak into the next session: reopen with working
    // audio and the round WAITS for the real drain again.
    controller.teardown()
    useDead = false
    controller.open({ autoListen: false })
    await vi.runAllTimersAsync()
    c.emit(ev('query_start'))
    c.emit(ev('text_start'))
    c.emit(ev('text_delta', SENTENCE))
    c.emit(ev('text_end'))
    c.emit(ev('query_end'))
    queue!.emitPCM(new Uint8Array([1]))
    queue!.settle()
    expect(controller.state()).toBe('speak')
    livePb.drain()
    expect(controller.state()).toBe('idle')
  })

  it('a rejection landing after teardown belongs to the dead session — no error, the next session drains normally', async () => {
    const c = makeConn()
    let queue: FakeTTSQueue | null = null
    const onTTSError = vi.fn()
    let rejectPrime!: (e: Error) => void
    const deadPb = { ...fakePlayback().pb, ensureAudio: () => new Promise<void>((_, rej) => (rejectPrime = rej)) }
    const livePb = fakePlayback()
    let useDead = true
    const controller = createVoiceController({
      conn: c.conn as never,
      createTTSQueue: (opts) => (queue = new FakeTTSQueue(opts)),
      createMic: (opts) => new FakeMic(opts.onDone),
      createPlayback: () => (useDead ? deadPb : livePb.pb),
      getPromptTexts: () => [],
      blockStore: createEventBlockStore(),
      sendUtterance: () => {},
      onTTSError,
    })
    controller.open({ autoListen: false }) // prime hangs
    controller.teardown() // face closed mid-prime
    rejectPrime(new Error('closed ctx')) // rejection lands after teardown
    await vi.runAllTimersAsync()
    expect(onTTSError).not.toHaveBeenCalled() // dead session: swallowed
    useDead = false
    controller.open({ autoListen: false }) // fresh session, working audio
    c.emit(ev('query_start'))
    c.emit(ev('text_start'))
    c.emit(ev('text_delta', SENTENCE))
    c.emit(ev('text_end'))
    c.emit(ev('query_end'))
    queue!.emitPCM(new Uint8Array([1]))
    queue!.settle()
    // audioDead must not have leaked: the round WAITS for the drain
    expect(controller.state()).toBe('speak')
    livePb.drain()
    expect(controller.state()).toBe('idle')
  })

  // Scoped silence regression: a sentence whose synthesis fails surfaces the
  // error and the round still settles — no speak deadlock.
  it('a failed sentence surfaces its error and the round still settles to idle', () => {
    const c = makeConn()
    let queue: FakeTTSQueue | null = null
    const onTTSError = vi.fn()
    const controller = createVoiceController({
      conn: c.conn as never,
      createTTSQueue: (opts) => (queue = new FakeTTSQueue(opts)),
      createMic: (opts) => new FakeMic(opts.onDone),
      createPlayback: () => fakePlayback().pb,
      getPromptTexts: () => [],
      blockStore: createEventBlockStore(),
      sendUtterance: () => {},
      onTTSError,
    })
    controller.open({ autoListen: false })
    c.emit(ev('query_start'))
    c.emit(ev('text_start'))
    c.emit(ev('text_delta', SENTENCE))
    c.emit(ev('query_end'))
    expect(controller.state()).toBe('speak') // the sentence is still owed
    queue!.emitError('tts: engine down')
    queue!.settle() // the failed item settles; nothing ever played
    expect(onTTSError).toHaveBeenCalledWith('tts: engine down')
    expect(controller.state()).toBe('idle')
  })
})
