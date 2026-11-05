import { describe, it, expect, vi } from 'vitest'
import {
  synthesize,
  createTTSQueue,
  TTS_PIPELINE_DEPTH,
  type TTSQueueOpts,
} from './voice_tts'

// A Response-shaped fake: ok/status/body/text are all synthesize touches.
function fakeResp(opts: {
  ok?: boolean
  status?: number
  body?: ReadableStream<Uint8Array> | null
  text?: string
}) {
  return {
    ok: opts.ok ?? true,
    status: opts.status ?? 200,
    body: opts.body === undefined ? new ReadableStream<Uint8Array>() : opts.body,
    text: () => Promise.resolve(opts.text ?? ''),
  }
}

describe('synthesize', () => {
  it('POSTs the sentence to /api/tts and resolves with the body stream', async () => {
    const body = new ReadableStream<Uint8Array>()
    const fetchFn = vi.fn(async () => fakeResp({ body }))
    const signal = new AbortController().signal
    const stream = await synthesize('你好。', signal, { fetchFn })
    expect(stream).toBe(body)
    expect(fetchFn).toHaveBeenCalledTimes(1)
    const [url, init] = fetchFn.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/tts')
    expect(init.method).toBe('POST')
    expect(init.headers).toEqual({ 'Content-Type': 'text/plain' })
    expect(init.body).toBe('你好。')
    expect(init.signal).toBe(signal)
  })

  it('a JSON error body throws its error field verbatim', async () => {
    const fetchFn = vi.fn(async () =>
      fakeResp({ ok: false, status: 503, text: '{"error":"tts: no default"}' }),
    )
    await expect(synthesize('hi', new AbortController().signal, { fetchFn })).rejects.toThrow(
      'tts: no default',
    )
  })

  it('a plain-text error body throws the text itself', async () => {
    const fetchFn = vi.fn(async () => fakeResp({ ok: false, status: 502, text: 'gateway broke' }))
    await expect(synthesize('hi', new AbortController().signal, { fetchFn })).rejects.toThrow(
      'gateway broke',
    )
  })

  it('an empty error body falls back to the status line', async () => {
    const fetchFn = vi.fn(async () => fakeResp({ ok: false, status: 503, text: '' }))
    await expect(synthesize('hi', new AbortController().signal, { fetchFn })).rejects.toThrow(
      'tts HTTP 503',
    )
  })

  it('a missing body on an ok response throws', async () => {
    const fetchFn = vi.fn(async () => fakeResp({ ok: true, status: 200, body: null }))
    await expect(synthesize('hi', new AbortController().signal, { fetchFn })).rejects.toThrow(
      'tts: empty response body',
    )
  })
})

// heldStream builds a ReadableStream the test pushes into, standing in for
// the chunked /api/tts response body.
function heldStream() {
  let ctl: ReadableStreamDefaultController<Uint8Array> | null = null
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      ctl = c
    },
  })
  return {
    stream,
    push: (b: Uint8Array) => ctl?.enqueue(b),
    end: () => ctl?.close(),
    error: (e: unknown) => ctl?.error(e),
  }
}

// Microtask ladder: enough turns for the queue's async loops to observe
// every push/close above.
const tick = async (): Promise<void> => {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

function makeSynth() {
  const calls: Array<{ text: string; signal: AbortSignal }> = []
  const streams: Array<ReturnType<typeof heldStream>> = []
  const fn = (text: string, signal: AbortSignal): Promise<ReadableStream<Uint8Array>> => {
    calls.push({ text, signal })
    const h = heldStream()
    streams.push(h)
    return Promise.resolve(h.stream)
  }
  return { calls, streams, fn }
}

function makeQueue(synth: (text: string, signal: AbortSignal) => Promise<ReadableStream<Uint8Array>>) {
  const writes: Uint8Array[] = []
  const errors: string[] = []
  let settled = 0
  const opts: TTSQueueOpts = {
    write: (pcm) => writes.push(pcm),
    onError: (m) => errors.push(m),
    onSettled: () => settled++,
    deps: { synthesize: synth },
  }
  return { queue: createTTSQueue(opts), writes, errors, settled: () => settled }
}

describe('createTTSQueue', () => {
  it('plays strictly in send order: the head streams live, later items flush buffered', async () => {
    const s = makeSynth()
    const { queue, writes, settled } = makeQueue(s.fn)
    queue.enqueue('s1')
    queue.enqueue('s2')
    await tick()
    expect(s.calls.map((c) => c.text)).toEqual(['s1', 's2'])

    // s2 completes fully first: nothing may play out of order.
    s.streams[1].push(new Uint8Array([3]))
    s.streams[1].push(new Uint8Array([4]))
    s.streams[1].end()
    await tick()
    expect(writes).toEqual([])

    // s1 is the head: its chunks play live as they arrive.
    s.streams[0].push(new Uint8Array([1]))
    await tick()
    expect(writes.map((w) => Array.from(w))).toEqual([[1]])
    s.streams[0].push(new Uint8Array([2]))
    await tick()
    expect(writes.map((w) => Array.from(w))).toEqual([[1], [2]])

    // s1 ending flushes s2's buffered chunks after it, in order.
    s.streams[0].end()
    await tick()
    expect(writes.map((w) => Array.from(w))).toEqual([[1], [2], [3], [4]])
    expect(settled()).toBe(2)
    expect(queue.inFlight()).toBe(0)
  })

  // Regression: s2 buffers a chunk while s1 is head, then s1 ends — s2 is
  // promoted to flush head while still streaming. The buffered prefix must
  // play before s2's later chunks, not after (write order was [3,2]).
  it('a promoted-but-live head plays its buffered prefix before later chunks', async () => {
    const s = makeSynth()
    const { queue, writes, settled } = makeQueue(s.fn)
    queue.enqueue('s1')
    queue.enqueue('s2')
    await tick()

    s.streams[1].push(new Uint8Array([2]))
    await tick()
    expect(writes).toEqual([])

    s.streams[0].end()
    await tick()
    expect(writes.map((w) => Array.from(w))).toEqual([[2]])

    s.streams[1].push(new Uint8Array([3]))
    s.streams[1].end()
    await tick()
    expect(writes.map((w) => Array.from(w))).toEqual([[2], [3]])
    expect(settled()).toBe(2)
    expect(queue.inFlight()).toBe(0)
  })

  it('caps concurrent synthesis at TTS_PIPELINE_DEPTH; excess sentences wait, never drop', async () => {
    expect(TTS_PIPELINE_DEPTH).toBe(3)
    const s = makeSynth()
    const { queue } = makeQueue(s.fn)
    for (let i = 1; i <= 4; i++) queue.enqueue(`s${i}`)
    await tick()
    expect(s.calls).toHaveLength(3)
    // Settling the head starts the 4th.
    s.streams[0].end()
    await tick()
    expect(s.calls.map((c) => c.text)).toEqual(['s1', 's2', 's3', 's4'])
    // Drain the rest.
    for (const h of s.streams.slice(1)) h.end()
    await tick()
    expect(queue.inFlight()).toBe(0)
  })

  it('abort aborts every in-flight signal, keeps write silent, and leaves the queue reusable', async () => {
    const s = makeSynth()
    const { queue, writes } = makeQueue(s.fn)
    for (let i = 1; i <= 3; i++) queue.enqueue(`s${i}`)
    await tick()
    queue.abort()
    expect(s.calls.map((c) => c.signal.aborted)).toEqual([true, true, true])
    expect(queue.inFlight()).toBe(0)
    // A settle landing after the abort must not write stale PCM.
    s.streams[0].push(new Uint8Array([1]))
    s.streams[0].end()
    await tick()
    expect(writes).toEqual([])
    // Reuse: a fresh enqueue synthesizes and plays normally.
    queue.enqueue('after')
    await tick()
    expect(s.calls.at(-1)?.text).toBe('after')
    expect(s.calls.at(-1)?.signal.aborted).toBe(false)
    s.streams[3].push(new Uint8Array([9]))
    s.streams[3].end()
    await tick()
    expect(writes.map((w) => Array.from(w))).toEqual([[9]])
  })

  it('a failed item fires onError once and never writes; later items still play', async () => {
    const s = makeSynth()
    const { queue, writes, errors, settled } = makeQueue((text, signal) => {
      if (text === 's1') return Promise.reject(new Error('tts: engine down'))
      return s.fn(text, signal)
    })
    queue.enqueue('s1')
    queue.enqueue('s2')
    await tick()
    expect(errors).toEqual(['tts: engine down'])
    expect(writes).toEqual([]) // the failed sentence never plays
    expect(queue.inFlight()).toBe(1) // s2 is still owed
    s.streams[0].push(new Uint8Array([5]))
    s.streams[0].end()
    await tick()
    expect(writes.map((w) => Array.from(w))).toEqual([[5]])
    expect(settled()).toBe(2)
    expect(queue.inFlight()).toBe(0)
  })

  it('onSettled fires exactly once per finished item, done or failed', async () => {
    const s = makeSynth()
    const { queue, settled } = makeQueue((text, signal) => {
      if (text === 'boom') return Promise.reject(new Error('x'))
      return s.fn(text, signal)
    })
    queue.enqueue('ok1')
    queue.enqueue('boom')
    queue.enqueue('ok2')
    await tick()
    expect(settled()).toBe(1) // boom
    s.streams[0].end()
    await tick()
    expect(settled()).toBe(2)
    s.streams[1].end()
    await tick()
    expect(settled()).toBe(3)
    expect(queue.inFlight()).toBe(0)
  })

  // Compaction drops the settled prefix once flushed exceeds 8; the write
  // sequence across the boundary must stay exact (no drop, duplicate, or
  // reorder) and inFlight must return to 0.
  it('a 10-sentence chain across the compaction threshold plays byte-exact in order', async () => {
    const s = makeSynth()
    const { queue, writes } = makeQueue(s.fn)
    for (let i = 1; i <= 10; i++) queue.enqueue(`s${i}`)
    await tick()
    expect(s.calls).toHaveLength(TTS_PIPELINE_DEPTH) // depth holds the start burst

    // Mixed settle order: within each live window, the younger item settles
    // fully (buffers) before the older one (whose chunks then play live and
    // flush the buffer behind them).
    const chunk = (item: number, c: 1 | 2) => new Uint8Array([item * 2 - 2 + c])
    for (let pair = 0; pair < 5; pair++) {
      const younger = pair * 2 + 1
      const older = pair * 2
      // Wait for both streams to exist (the pump starts them as slots free).
      await vi.waitFor(() => {
        expect(s.streams[younger]).toBeTruthy()
        expect(s.streams[older]).toBeTruthy()
      })
      s.streams[younger].push(chunk(younger + 1, 1))
      s.streams[younger].push(chunk(younger + 1, 2))
      s.streams[younger].end()
      s.streams[older].push(chunk(older + 1, 1))
      s.streams[older].push(chunk(older + 1, 2))
      s.streams[older].end()
      await tick()
    }
    expect(queue.inFlight()).toBe(0)
    // s1c1, s1c2, s2c1, ... s10c2 — byte values 1..20 in order.
    expect(writes.map((w) => Array.from(w))).toEqual(
      Array.from({ length: 20 }, (_, i) => [i + 1]),
    )
  })

  // EOS root fix: TTS is bounded data — every item settled means no more PCM
  // can ever arrive. That settle is the explicit end signal for the playback
  // chain; it must fire exactly when the queue empties, AFTER every buffered
  // chunk was written, and never mid-reply.
  it('onEOS fires once when the last item settles, after every buffered chunk was written', async () => {
    const s = makeSynth()
    const log: string[] = []
    const queue = createTTSQueue({
      write: (pcm) => log.push('w' + pcm[0]),
      onSettled: () => log.push('settled'),
      onEOS: () => log.push('eos'),
      deps: { synthesize: s.fn },
    })
    queue.enqueue('s1')
    queue.enqueue('s2')
    await tick()
    s.streams[1].push(new Uint8Array([4]))
    s.streams[1].end() // s2 settles first and buffers — s1 still owed
    await tick()
    expect(log).toEqual(['settled']) // no eos while items remain in flight
    s.streams[0].push(new Uint8Array([1]))
    s.streams[0].end()
    await tick()
    // finish(): advance() writes s1's live chunk then s2's buffered chunk,
    // then onSettled — the eos lands strictly after the last byte.
    expect(log).toEqual(['settled', 'w1', 'w4', 'settled', 'eos'])
  })

  it('a failed final item still ends the stream (onEOS fires)', async () => {
    let eos = 0
    const queue = createTTSQueue({
      write: () => {},
      onEOS: () => eos++,
      deps: {
        synthesize: (_text, _signal) => Promise.reject(new Error('tts: engine down')),
      },
    })
    queue.enqueue('boom')
    await tick()
    expect(eos).toBe(1)
  })

  it('abort never fires onEOS', async () => {
    const s = makeSynth()
    let eos = 0
    const queue = createTTSQueue({
      write: () => {},
      onEOS: () => eos++,
      deps: { synthesize: s.fn },
    })
    queue.enqueue('s1')
    await tick()
    queue.abort()
    s.streams[0].push(new Uint8Array([1]))
    s.streams[0].end()
    await tick()
    expect(eos).toBe(0)
  })

  it('abort announces itself on the debug log', async () => {
    const s = makeSynth()
    const { queue } = makeQueue(s.fn)
    queue.enqueue('s1')
    await tick()
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    queue.abort()
    expect(dbg).toHaveBeenCalledWith('[tts] abort')
    dbg.mockRestore()
  })

  it('debug logs carry measured ttf/ms/bytes and a 24-char head', async () => {
    vi.useFakeTimers({ toFake: ['performance'] })
    try {
      const s = makeSynth()
      const logs: string[] = []
      const spy = vi
        .spyOn(console, 'debug')
        .mockImplementation((...a: unknown[]) => logs.push(a.map(String).join(' ')))
      const { queue } = makeQueue(s.fn)
      // Seed the performance clock past zero: t0 > 0 keeps elapsed-time math
      // distinguishable from accumulated-time math in the logs.
      vi.advanceTimersByTime(5000)
      const text = Array.from({ length: 40 }, (_, i) => String.fromCharCode(65 + i)).join('')
      queue.enqueue(text)
      await tick()
      vi.advanceTimersByTime(100)
      s.streams[0].push(new Uint8Array([1]))
      await tick()
      vi.advanceTimersByTime(100)
      s.streams[0].push(new Uint8Array([2]))
      s.streams[0].end()
      await tick()
      spy.mockRestore()
      const synthLog = logs.find((l) => l.includes('[tts] synth'))
      expect(synthLog).toContain('len=40')
      expect(synthLog).toContain('head="ABCDEFGHIJKLMNOPQRSTUVWX"')
      const doneLog = logs.find((l) => l.includes('item done'))
      expect(doneLog).toContain('ttf=100ms')
      expect(doneLog).toContain('ms=200 bytes=2')
    } finally {
      vi.useRealTimers()
    }
  })

  it('a failed item logs the elapsed ms and the error message', async () => {
    vi.useFakeTimers({ toFake: ['performance'] })
    try {
      const logs: string[] = []
      const spy = vi
        .spyOn(console, 'debug')
        .mockImplementation((...a: unknown[]) => logs.push(a.map(String).join(' ')))
      let rejectSynth!: (e: Error) => void
      const queue = createTTSQueue({
        write: () => {},
        deps: { synthesize: () => new Promise((_res, rej) => (rejectSynth = rej)) },
      })
      vi.advanceTimersByTime(5000) // t0 > 0 before the item starts
      queue.enqueue('boom')
      await tick()
      vi.advanceTimersByTime(50) // the failure lands 50ms into the item
      rejectSynth(new Error('tts: engine down'))
      await tick()
      spy.mockRestore()
      const failLog = logs.find((l) => l.includes('item failed'))
      expect(failLog).toContain('ms=50 err=tts: engine down')
    } finally {
      vi.useRealTimers()
    }
  })

  it('a sentence that fails mid-stream never plays its buffered partial audio', async () => {
    const s = makeSynth()
    const { queue, writes, errors, settled } = makeQueue(s.fn)
    queue.enqueue('s1')
    queue.enqueue('s2')
    await tick()
    s.streams[1].push(new Uint8Array([9]))
    await tick() // s2 (not the head) buffers its partial chunk
    s.streams[1].error(new Error('mid-fail'))
    await tick()
    expect(errors).toEqual(['mid-fail'])
    s.streams[0].end() // the head settles; advance walks past the failed s2
    await tick()
    expect(writes).toEqual([])
    expect(settled()).toBe(2)
    expect(queue.inFlight()).toBe(0)
  })

  it('a later enqueue cannot grow the pipeline past depth while the first three run', async () => {
    const s = makeSynth()
    const { queue } = makeQueue(s.fn)
    for (let i = 1; i <= 3; i++) queue.enqueue(`s${i}`)
    await tick()
    expect(s.calls).toHaveLength(3)
    queue.enqueue('s4') // pump re-runs with three live items: s4 must still wait
    await tick()
    expect(s.calls).toHaveLength(3)
    for (const h of s.streams) h.end()
    await tick()
    expect(s.calls.map((c) => c.text)).toEqual(['s1', 's2', 's3', 's4'])
  })

  it('a stream ending after abort never settles or fires EOS', async () => {
    const s = makeSynth()
    let eos = 0
    let settled = 0
    const queue = createTTSQueue({
      write: () => {},
      onSettled: () => settled++,
      onEOS: () => eos++,
      deps: { synthesize: s.fn },
    })
    queue.enqueue('s1')
    await tick()
    s.streams[0].push(new Uint8Array([1]))
    await tick() // the chunk read completes pre-abort
    queue.abort()
    s.streams[0].end() // done arrives post-abort
    await tick()
    expect(settled).toBe(0)
    expect(eos).toBe(0)
  })

  it('a synthesis rejection landing after abort never settles or fires EOS', async () => {
    let rejectSynth!: (e: Error) => void
    let eos = 0
    let settled = 0
    const queue = createTTSQueue({
      write: () => {},
      onSettled: () => settled++,
      onEOS: () => eos++,
      deps: { synthesize: () => new Promise((_res, rej) => (rejectSynth = rej)) },
    })
    queue.enqueue('s1')
    await tick() // the item is awaiting the synth promise
    queue.abort()
    rejectSynth(new Error('late failure'))
    await tick()
    expect(settled).toBe(0)
    expect(eos).toBe(0)
  })

  it('a freed slot starts exactly one waiter — never a burst past depth', async () => {
    const s = makeSynth()
    const { queue } = makeQueue(s.fn)
    for (let i = 1; i <= 3; i++) queue.enqueue(`s${i}`)
    await tick()
    for (let i = 4; i <= 6; i++) queue.enqueue(`s${i}`) // all wait: pipeline full
    s.streams[0].end() // one slot frees
    await tick()
    expect(s.calls.map((c) => c.text)).toEqual(['s1', 's2', 's3', 's4'])
    for (const h of s.streams.slice(1)) h.end()
    await tick()
    for (const h of s.streams.slice(4)) h.end() // s5/s6 started as the slots freed
    await tick()
    expect(queue.inFlight()).toBe(0)
  })
})
