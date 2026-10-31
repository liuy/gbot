// TTS transport for the voice page: synthesize POSTs one sentence to
// /api/tts (raw PCM s16le@24k mono streams back), and createTTSQueue turns
// a stream of sentences into strictly ordered playback with a bounded
// synthesis pipeline. Browser coupling (fetch) goes through deps for jsdom.

// audio.cpp synthesizes serially, so a deeper pipeline only moves the wait
// around; depth 3 keeps the engine non-idle across sentence boundaries while
// bounding buffered audio. Excess sentences WAIT — they are never dropped.
export const TTS_PIPELINE_DEPTH = 3

export interface SynthesizeDeps {
  fetchFn?: typeof fetch
}

// synthesize POSTs one sentence and hands back the response body stream. A
// non-2xx answer throws the server's {"error":...} text (falling back to the
// raw body, then the status line) — that message is what the chat capsule
// shows, so it must stay human-readable.
export function synthesize(
  text: string,
  signal: AbortSignal,
  deps?: SynthesizeDeps,
): Promise<ReadableStream<Uint8Array>> {
  const doFetch = deps?.fetchFn ?? fetch
  return doFetch('/api/tts', {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: text,
    signal,
  }).then(async (resp) => {
    if (!resp.ok) {
      let msg = `tts HTTP ${resp.status}`
      try {
        const raw = await resp.text()
        if (raw) {
          try {
            const parsed = JSON.parse(raw) as { error?: string }
            if (parsed.error) msg = parsed.error
          } catch {
            msg = raw
          }
        }
      } catch {
        // Body unreadable: the status line above already says something true.
      }
      throw new Error(msg)
    }
    if (!resp.body) throw new Error('tts: empty response body')
    return resp.body
  })
}

// ── ordered playback queue ────────────────────────────────────────────────

export interface TTSQueueOpts {
  write(pcm: Uint8Array): void
  onError?(message: string): void
  // One item finished (done or failed) — the drain gate re-checks here.
  onSettled?(): void
  // Every item settled: the bounded data ended, no more PCM can arrive.
  // The fetch settle IS the stream's end signal — forwarded to playback so
  // the worklet retires on truth instead of a silence guess.
  onEOS?(): void
  deps?: { synthesize?: typeof synthesize }
}

export interface TTSQueue {
  enqueue(text: string): void
  inFlight(): number // queued + streaming items
  abort(): void // abort in-flight, clear all state; instance reusable
}

interface ttsItem {
  text: string
  ctl: AbortController | null
  chunks: Uint8Array[]
  started: boolean
  done: boolean
}

export function createTTSQueue(opts: TTSQueueOpts): TTSQueue {
  const synth = opts.deps?.synthesize ?? synthesize
  let items: ttsItem[] = []
  // Index of the next item to flush (all predecessors fully written).
  let flushed = 0
  // Bumped by abort(): settles and chunks from a dropped round must not
  // touch the rebuilt queue state.
  let gen = 0

  const inFlight = (): number => items.filter((it) => !it.done).length

  const startItem = (item: ttsItem): void => {
    const g = gen
    const ctl = new AbortController()
    item.ctl = ctl
    void (async () => {
      try {
        const body = await synth(item.text, ctl.signal)
        const reader = body.getReader()
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          if (g !== gen) return
          // Identity, never a captured index: compaction renumbers items,
          // so liveness of "I am the flush head" can only be answered by
          // comparing against the object at the head slot right now.
          if (items[flushed] === item) opts.write(value)
          else item.chunks.push(value)
        }
        if (g !== gen) return
        finish(item)
      } catch (err) {
        if (g !== gen) return
        finish(item, err instanceof Error ? err : new Error(String(err)))
      }
    })()
  }

  const finish = (item: ttsItem, err?: Error): void => {
    if (item.done) return
    item.done = true
    if (err) {
      // A failed sentence never plays, even mid-buffered: drop what arrived
      // before the failure instead of flushing a truncated tail.
      item.chunks = []
    }
    pump()
    advance()
    if (err && !item.ctl?.signal.aborted) opts.onError?.(err.message)
    opts.onSettled?.()
    // EOS AFTER advance(): buffered chunks are written first, the end signal
    // follows the last byte. Fires on error settles too — a failed tail is
    // just as final as a played one.
    if (inFlight() === 0) opts.onEOS?.()
  }

  // Advance the flush head across settled items, writing each one's buffered
  // chunks in order. Settled items' PCM is dropped after writing — the queue
  // must not retain a long reply's audio for its whole lifetime.
  const advance = (): void => {
    while (items.length > flushed && items[flushed].done) {
      const head = items[flushed]
      for (const c of head.chunks) opts.write(c)
      head.chunks = []
      flushed++
    }
    const head = items[flushed]
    if (head && !head.done && head.chunks.length > 0) {
      // A promoted-but-live head hands over its buffer now: its next chunk
      // writes live and would otherwise jump ahead of what it buffered while
      // it waited.
      for (const c of head.chunks) opts.write(c)
      head.chunks = []
    }
    // Prefix compaction: without it a long reply grows the array without
    // bound. Safe ONLY because the head test above is identity-based
    // (items[flushed] === item) — renumbering cannot desynchronize it.
    if (flushed > 8) {
      items = items.slice(flushed)
      flushed = 0
    }
  }

  const pump = (): void => {
    let active = 0
    for (const it of items) {
      if (it.started) {
        if (!it.done) active++
      } else if (active < TTS_PIPELINE_DEPTH) {
        it.started = true
        startItem(it)
        active++
      }
    }
  }

  return {
    enqueue(text: string): void {
      items.push({ text, ctl: null, chunks: [], started: false, done: false })
      pump()
    },
    inFlight,
    abort(): void {
      gen++
      for (const it of items) it.ctl?.abort()
      items = []
      flushed = 0
    },
  }
}
