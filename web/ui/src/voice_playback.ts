// Voice playback graph: jitter-buffered StreamProcessor worklet →
// AnalyserNode → destination, on a dedicated 24kHz context. The context is
// NOT created at construction — autoplay policy ties it to the first hold
// gesture (D8), so ensureAudio() is the creation point and is idempotent.
//
// The worklet's buffering is a line-by-line port of the hf-realtime-voice
// AudioPlaybackProcessor (PR #13, a production fork port): playback waits for
// a jitter floor before starting, flushes after a max-wait even under the
// floor (bounded latency for short/slow tails), ramps ~4ms linear de-click
// fades at every boundary, re-arms the prebuffer after an underrun instead of
// dying, and caps the queue as a runaway safety net. Mobile parameters are
// locked harder than the reference's browser→cloud ones: 250ms floor, 500ms
// max-wait. Engine PCM is s16le mono 24kHz on a 24kHz context, so the
// reference's input→context upsampler would always step by exactly 1.0 and
// is dead code here — stated, not silently dropped.
import { rmsOf } from './voice_aura'

//
// JitterBufferCore is authored with no TS-only syntax beyond annotations so
// JitterBufferCore.toString() embeds verbatim into the worklet module below:
// the worklet and the headless tests execute the same code.
export class JitterBufferCore {
  _queue: Float32Array[]
  _readIdx: number
  _playing: boolean
  _lastSample: number
  _prebufferActive: boolean
  _prebufferWaitFrames: number
  _fadeIn: number
  /** Frames remaining in a linear ramp-out (0 = not ramping). */
  _underrunRamp: number
  /** Sample value at the start of the current ramp-out. */
  _underrunStartSample: number
  /** Explicit end-of-stream flag: the bounded data finished, nothing more
   *  will arrive. Only this (or interrupt) may retire the processor. */
  _ended: boolean
  _fadeFrames: number
  _prebufferTarget: number
  _maxWaitFrames: number
  _maxQueueSamples: number
  _emit: (event: string) => void

  constructor(sampleRate: number, emit: (event: string) => void) {
    this._queue = []
    this._readIdx = 0
    this._playing = false
    this._lastSample = 0
    this._prebufferActive = false
    this._prebufferWaitFrames = 0
    this._fadeIn = 0
    this._underrunRamp = 0
    this._underrunStartSample = 0
    this._ended = false
    // ~4ms de-click ramp, scaled from the reference's 192 frames @48kHz —
    // long enough to de-click boundaries, inaudible as a fade.
    this._fadeFrames = 96
    this._prebufferTarget = Math.ceil((250 / 1000) * sampleRate)
    this._maxWaitFrames = Math.ceil((500 / 1000) * sampleRate)
    this._maxQueueSamples = Math.ceil((30000 / 1000) * sampleRate)
    this._emit = emit
  }

  write(samples: Float32Array): void {
    if (samples.length === 0) return
    this._queue.push(samples)
    this._trimQueue()
    this._armPrebuffer()
  }

  /** Explicit end-of-stream (the fetch settled): everything written plays
   *  out, then the processor ramps down and retires. */
  end(): void {
    this._ended = true
  }

  /** Begin waiting for the jitter floor when audio is queued but not playing. */
  _armPrebuffer(): void {
    if (this._playing || this._underrunRamp > 0) return
    if (this._queue.length === 0) return
    this._prebufferActive = true
    this._prebufferWaitFrames = 0
  }

  /** End a ramp-out: stop playback and re-arm prebuffer for any queued
   *  audio. Reports the underrun upward (the reference posts it; the main
   *  thread's message guard currently ignores it — kept for parity). After
   *  an explicit end the ramp was the stream's de-click tail, not a stutter
   *  — there is nothing left to re-arm or report. */
  _finishRampOut(): void {
    this._playing = false
    this._lastSample = 0
    this._underrunRamp = 0
    if (this._ended) return
    this._emit('underrun')
    this._armPrebuffer()
  }

  /** Flip from prebuffer to playback once the floor or max-wait is met. An
   *  ended stream flushes its tail immediately — the floor waits for data
   *  that will never arrive. */
  _tryStartPlayback(): boolean {
    if (!this._prebufferActive || this._playing) return false
    const queued = this._queuedSamples()
    const target = this._prebufferTarget
    const maxWait = this._maxWaitFrames
    if (!this._ended && queued < target && this._prebufferWaitFrames < maxWait) return false
    this._prebufferActive = false
    this._playing = true
    this._fadeIn = this._fadeFrames
    this._underrunRamp = 0
    return true
  }

  _queuedSamples(): number {
    let total = -this._readIdx
    for (const buf of this._queue) total += buf.length
    return Math.max(0, total)
  }

  /**
   * Runaway safety net: drop oldest samples only past the ~30s ceiling. Must
   * stay high enough that a normal TTS reply never hits this path —
   * discarding next-to-play (oldest unread) audio mid-phrase causes an
   * audible forward skip (~0.1s phrase swap), which is worse than the memory
   * it saves.
   */
  _trimQueue(): void {
    const maxSamples = this._maxQueueSamples
    while (this._queuedSamples() > maxSamples && this._queue.length > 0) {
      if (this._queue.length === 1) {
        // Last buffer: keep at least one unread sample so trimming can never
        // empty the queue out from under a live read.
        const head = this._queue[0]
        const unread = head.length - this._readIdx
        const drop = Math.min(unread - 1, this._queuedSamples() - maxSamples)
        if (drop <= 0) break
        this._readIdx += drop
        if (this._readIdx >= head.length) {
          this._readIdx = 0
          this._queue.shift()
        }
        break
      }
      const head = this._queue[0]
      if (this._readIdx >= head.length) {
        this._readIdx -= head.length
        this._queue.shift()
      } else {
        const drop = Math.min(head.length - this._readIdx, this._queuedSamples() - maxSamples)
        this._readIdx += drop
        if (this._readIdx >= head.length) {
          this._readIdx -= head.length
          this._queue.shift()
        }
      }
    }
  }

  // The reference reads with linear interpolation because it upsamples
  // (inputRate ≠ context rate). Our engine PCM is 24kHz on a 24kHz context,
  // so its step ratio is exactly 1.0 and the fractional position stays 0 —
  // the interpolator would always return head[idx] unchanged. Dead code.
  _readSample(): number | null {
    if (this._queue.length === 0) return null
    return this._queue[0][this._readIdx]
  }

  _advance(): void {
    this._readIdx += 1
    while (this._queue.length > 0 && this._readIdx >= this._queue[0].length) {
      this._readIdx -= this._queue[0].length
      this._queue.shift()
    }
  }

  /** Linear fade-in at the start of every playback burst: gain 0 → 1, so a
   *  (re)start never steps out of silence at full amplitude. */
  _applyFadeIn(sample: number): number {
    if (this._fadeIn > 0) {
      const gain = 1 - this._fadeIn / this._fadeFrames
      sample *= gain
      this._fadeIn -= 1
    }
    return sample
  }

  // One render quantum: fill `out`, return false only when the stream ended
  // (explicit end, fully played out — see _ended). Port of the reference
  // process() frame loop.
  tick(out: Float32Array): boolean {
    for (let i = 0; i < out.length; i++) {
      let sample = 0

      if (this._prebufferActive && !this._playing) {
        this._prebufferWaitFrames += 1
        this._tryStartPlayback()
      }

      if (this._playing) {
        if (this._underrunRamp > 0) {
          // Mid ramp-out (underrun): decay the held sample to zero.
          sample = this._underrunStartSample * (this._underrunRamp / this._fadeFrames)
          this._lastSample = sample
          this._underrunRamp -= 1
          if (this._underrunRamp <= 0) this._finishRampOut()
        } else {
          const v = this._readSample()
          if (v === null) {
            // Queue ran dry: begin a linear ramp-out from the last sample so
            // the out→silence boundary de-clicks, then re-prebuffer.
            this._underrunStartSample = this._lastSample
            this._underrunRamp = this._fadeFrames
            sample = this._underrunStartSample
            this._lastSample = sample
            this._underrunRamp -= 1
            if (this._underrunRamp <= 0) this._finishRampOut()
          } else {
            sample = v
            this._lastSample = v
            this._advance()
          }
        }

        sample = this._applyFadeIn(sample)
      } else if (this._ended) {
        // Bounded data fully played out (any ramp-out finished above): retire
        // instead of emitting silence forever waiting for a heuristic.
        return false
      }

      out[i] = sample
    }
    return true
  }
}

export const PLAYBACK_WORKLET_SRC = `
const JitterCore = ${JitterBufferCore.toString()};
class StreamProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.hasInterrupted = false;
    this.core = new JitterCore(sampleRate, (ev) => this.port.postMessage({ event: ev }));
    this.port.onmessage = (event) => {
      if (!event.data) return;
      const payload = event.data;
      if (payload.event === 'write') {
        const int16Array = payload.buffer;
        const float32Array = new Float32Array(int16Array.length);
        for (let i = 0; i < int16Array.length; i++) float32Array[i] = int16Array[i] / 0x8000;
        this.writeData(float32Array, payload.trackId);
      } else if (payload.event === 'interrupt') {
        this.hasInterrupted = true;
      } else if (payload.event === 'end') {
        this.core.end();
      }
    };
  }
  // trackId rides the write event (controller protocol) but playback treats
  // every write as one contiguous stream, exactly as before.
  writeData(float32Array, trackId) {
    this.core.write(float32Array);
  }
  process(inputs, outputs) {
    const outputChannelData = outputs[0][0];
    if (this.hasInterrupted || !this.core.tick(outputChannelData)) {
      this.port.postMessage({ event: 'stop' });
      return false;
    }
    return true;
  }
}
registerProcessor('stream_processor', StreamProcessor);
`

// Minimal structural types: jsdom has none of these, so the deps injection
// fakes satisfy exactly what the module touches.
export interface PlaybackAnalyser {
  connect(target: unknown): unknown
  getByteTimeDomainData(arr: Uint8Array): void
}

export interface PlaybackWorkletNode {
  port: { postMessage(data: unknown): void; onmessage: ((e: { data: unknown }) => void) | null }
  connect(target: unknown): unknown
  disconnect(): void
}

export interface PlaybackContext {
  readonly sampleRate: number
  readonly destination: unknown
  resume(): Promise<void>
  close(): Promise<void>
  readonly audioWorklet: { addModule(url: string): Promise<void> }
  createAnalyser(): PlaybackAnalyser
}

export interface PlaybackDeps {
  createContext?: () => PlaybackContext
  makeNode?: () => PlaybackWorkletNode
}

export interface Playback {
  // Creates the 24kHz context + worklet + analyser chain. Idempotent; the
  // ONLY creation point (called inside the first hold gesture).
  ensureAudio(): Promise<void>
  write(pcm: Uint8Array): void
  interrupt(): void
  // Explicit end-of-stream (the fetch settled): the worklet plays out its
  // queue, ramps down, and retires — drained fires. Silence-gap guessing is
  // gone; this is the only drain trigger besides interrupt.
  end(): void
  onDrained(cb: () => void): void
  // One synchronous read: time-domain RMS gain-compensated to [0,1] (the
  // aura visualizer's volume input). Called from the page's rAF draw loop.
  rms(): number
  teardown(): Promise<void>
}

export function createPlayback(deps: PlaybackDeps = {}): Playback {
  let ctx: PlaybackContext | null = null
  let node: PlaybackWorkletNode | null = null
  let analyser: PlaybackAnalyser | null = null
  let td: Uint8Array | null = null
  let ensurePromise: Promise<void> | null = null
  let carry = new Uint8Array(0)
  let drainedCb: (() => void) | null = null
  let makeNode: (() => PlaybackWorkletNode) | null = null

  // Node lifecycle (voice-go page.go:142-147 precedent): one node per stream.
  // The worklet's 'stop' event (interrupt or drain — the processor returns
  // false either way) kills the node with disconnect()+null; the next write
  // lazily builds a fresh node on the already-registered module. A dead
  // processor drops every postMessage, so a write after death must land on
  // the rebuild — otherwise the stream falls into the void (silent body
  // after a filler interrupt, silence from round two on, dead waveform).
  const attachNode = (): void => {
    if (node || !analyser || !makeNode) return
    const n = makeNode()
    node = n
    n.port.onmessage = (e) => {
      if ((e.data as { event?: string })?.event !== 'stop') return
      n.disconnect()
      // A rebuilt successor already owns the graph — a late stop from this
      // retired node only cleans itself up.
      if (node === n) {
        node = null
        drainedCb?.()
      }
    }
    n.connect(analyser)
  }

  const ensureAudio = (): Promise<void> => {
    if (ensurePromise) return ensurePromise
    ensurePromise = (async () => {
      const create =
        deps.createContext ??
        (() => new AudioContext({ sampleRate: 24000 }) as unknown as PlaybackContext)
      ctx = create()
      await ctx.resume().catch(() => {})
      const url = URL.createObjectURL(new Blob([PLAYBACK_WORKLET_SRC], { type: 'text/javascript' }))
      await ctx.audioWorklet.addModule(url)
      makeNode =
        deps.makeNode ??
        (() =>
          new AudioWorkletNode(ctx as unknown as BaseAudioContext, 'stream_processor', {
            numberOfInputs: 0,
            outputChannelCount: [1],
          }) as unknown as PlaybackWorkletNode)
      analyser = ctx.createAnalyser()
      // Aura RMS tap (readVol spec): 1024 time-domain frames, 0.55 smoothing.
      // Structural fakes carry the props as plain assignments.
      const sized = analyser as PlaybackAnalyser & { fftSize?: number; smoothingTimeConstant?: number }
      sized.fftSize = 1024
      sized.smoothingTimeConstant = 0.55
      analyser.connect(ctx.destination)
      td = new Uint8Array(1024)
      attachNode()
    })()
    return ensurePromise
  }

  return {
    ensureAudio,
    write(pcm: Uint8Array): void {
      if (!node) attachNode()
      if (!node) return
      // s16le little-endian with odd-tail carry (voice-demo player
      // semantics): a split sample must never be dropped or misaligned.
      const m = new Uint8Array(carry.length + pcm.length)
      m.set(carry)
      m.set(pcm, carry.length)
      const n = m.length - (m.length % 2)
      carry = m.slice(n)
      if (n < 2) return
      const i16 = new Int16Array(n >> 1)
      for (let i = 0, j = 0; i < n; i += 2, j++) i16[j] = (m[i] | (m[i + 1] << 8)) as number
      node.port.postMessage({ event: 'write', buffer: i16, trackId: 'tts' })
    },
    interrupt(): void {
      carry = new Uint8Array(0)
      const n = node
      if (!n) return
      n.port.postMessage({ event: 'interrupt' })
      // The processor only returns false on its next render quantum — retire
      // the node NOW so a same-tick write (the filler→body transition) cannot
      // land on the dying processor. The late 'stop' still arrives on n's
      // port; the identity guard in its handler keeps it a no-op.
      n.disconnect()
      node = null
      drainedCb?.()
    },
    end(): void {
      node?.port.postMessage({ event: 'end' })
    },
    onDrained(cb: () => void): void {
      drainedCb = cb
    },
    rms(): number {
      if (!analyser || !td) return 0
      analyser.getByteTimeDomainData(td)
      return rmsOf(td)
    },
    async teardown(): Promise<void> {
      node?.disconnect()
      node = null
      analyser = null
      td = null
      ensurePromise = null
      carry = new Uint8Array(0)
      const c = ctx
      ctx = null
      await c?.close().catch(() => {})
    },
  }
}
