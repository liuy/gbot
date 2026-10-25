// Speech-to-text input: getUserMedia + AudioWorklet capture → /ws/stt →
// progressive transcript. Browser API coupling (AudioContext/getUserMedia/
// AudioWorklet/WebSocket) goes through SttDeps so the pure logic runs under
// jsdom; the worklet source is a self-contained string (blob-URL addModule,
// e3 omni-talk precedent) so no separate JS asset or route exists.
import { t } from './i18n'

export const STT_FRAME_SAMPLES = 512

// Self-contained by contract: blob modules cannot import bundle siblings,
// so WORKLET_SRC must never contain an import statement.
export const WORKLET_SRC = `
class SttCaptureProcessor extends AudioWorkletProcessor {
  constructor() {
    super()
    // Fractional read position over the (virtual) contiguous input stream,
    // carried across process() calls so resampling never loses or
    // duplicates samples. phase sits in [0, ratio) relative to the block
    // tail (the tail acts as sample index -1 of the next block).
    this.phase = 0
    this.tail = 0
    this.out = new Float32Array(${STT_FRAME_SAMPLES})
    this.outLen = 0
  }
  emit(samples) {
    let i = 0
    while (i < samples.length) {
      const room = ${STT_FRAME_SAMPLES} - this.outLen
      const take = Math.min(room, samples.length - i)
      this.out.set(samples.subarray(i, i + take), this.outLen)
      this.outLen += take
      i += take
      if (this.outLen === ${STT_FRAME_SAMPLES}) {
        this.port.postMessage(this.out.slice())
        this.outLen = 0
      }
    }
  }
  process(inputs) {
    const input = inputs[0] && inputs[0][0]
    if (!input || input.length === 0) return true
    if (sampleRate === 16000) {
      this.emit(input)
      return true
    }
    const ratio = sampleRate / 16000
    const L = input.length
    let p = -1 + this.phase
    const chunk = []
    while (p + 1 <= L - 1) {
      const j = Math.floor(p)
      const frac = p - j
      const s0 = j < 0 ? this.tail : input[j]
      const s1 = input[j + 1]
      chunk.push(s0 + frac * (s1 - s0))
      p += ratio
    }
    this.tail = input[L - 1]
    this.phase = p - (L - 1)
    this.emit(new Float32Array(chunk))
    return true
  }
}
registerProcessor('stt-capture', SttCaptureProcessor)
`

// Clamp to [-1,1], scale by 32767 with Math.round semantics (ties round
// toward +∞), emit manual little-endian int16 bytes. ×32767 (not ×32768)
// keeps the codec the exact inverse of the Go endpointer's /32767
// normalization; -1 maps to -32767 (0x8001), never 0xFFFF.
export function floatTo16Le(frame: Float32Array): Uint8Array {
  const out = new Uint8Array(frame.length * 2)
  for (let i = 0; i < frame.length; i++) {
    const v = frame[i] < -1 ? -1 : frame[i] > 1 ? 1 : frame[i]
    const n = Math.round(v * 32767)
    const u = n < 0 ? n + 0x10000 : n
    out[2 * i] = u & 0xff
    out[2 * i + 1] = (u >> 8) & 0xff
  }
  return out
}

const CJK_TAIL = /[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]$/
const ASCII_WORD = /^[A-Za-z0-9]/

// Two splice rules, applied where the textarea holds the text:
// - ASCII word boundary: both sides alphanumeric → one space between.
// - qwen windows emit a leading space before CJK continuations — strip it
//   or every window boundary grows a stray gap.
export function joinSttText(prev: string, next: string): string {
  if (prev && next && CJK_TAIL.test(prev) && next.startsWith(' ')) {
    return prev + next.replace(/^ +/, '')
  }
  if (
    prev &&
    next &&
    ASCII_WORD.test(prev[prev.length - 1]) &&
    ASCII_WORD.test(next[0])
  ) {
    return prev + ' ' + next
  }
  return prev + next
}

// ASR biasing terms: ASCII words (≥3 chars, internal '-/' allowed) pulled
// from the recent conversation, first-seen spelling wins, lowercase-deduped.
// stop-at-first-misfit: the first word that exceeds the character budget
// ends collection — skipping long words to pack later short ones couples
// "which words" to budget trivia a biasing hint doesn't justify.
export function extractPromptTerms(
  texts: string[],
  maxTerms = 12,
  maxChars = 400,
): string {
  const seen = new Set<string>()
  const out: string[] = []
  let used = 0
  const re = /[A-Za-z0-9][A-Za-z0-9'-]*/g
  for (const text of texts) {
    for (const m of text.matchAll(re)) {
      const word = m[0]
      if (word.length < 3) continue
      const lower = word.toLowerCase()
      if (seen.has(lower)) continue
      const cost = (out.length > 0 ? 1 : 0) + word.length
      if (used + cost > maxChars) return out.join(' ')
      seen.add(lower)
      out.push(word)
      used += cost
      if (out.length >= maxTerms) return out.join(' ')
    }
  }
  return out.join(' ')
}

export function sttWsUrl(): string {
  return `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws/stt`
}

// Structural superset of the plan's {send, close}: the session must also
// receive frames and observe unexpected closure. A real WebSocket satisfies
// this shape; tests inject a MockWebSocket.
export interface SttSocket {
  send(data: string | ArrayBuffer): void
  close(): void
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: (() => void) | null
  onopen?: (() => void) | null
  onerror?: (() => void) | null
  readonly bufferedAmount: number
}

export interface SttWorkletHandle {
  disconnect(): void // src.disconnect + node.disconnect
}

export interface SttDeps {
  createAudioContext?: () => AudioContext
  getUserMedia?: () => Promise<MediaStream>
  loadWorklet?: (
    ctx: AudioContext,
    stream: MediaStream,
    onFrame: (f: Float32Array) => void,
  ) => Promise<SttWorkletHandle>
  connectSocket?: () => SttSocket
}

export interface SttSessionOpts {
  onText(text: string): void // delta: raw increment, joining happens in input_bar
  onPartial?(text: string): void
  onDone(text: string): void // terminal; session tears itself down
  onListening(on: boolean): void
  onStatus(msg: string | null): void
  onUtteranceStart?(): void
  getPromptTexts(): string[]
  deps?: SttDeps
}

// 4MB of queued PCM ≈ 130s of audio — a healthy link never approaches it;
// the cap only bounds pathological backpressure (pending buffer before
// ready and ws.bufferedAmount after).
const MAX_QUEUED_BYTES = 4 << 20
const STOP_TIMEOUT_MS = 5000

export class SttSession {
  private opts: SttSessionOpts
  private deps: Required<SttDeps>
  private activeUtterance = false
  private assembling = false
  private torn = false
  private ready = false
  private pending: Uint8Array[] = []
  private pendingBytes = 0
  private socket: SttSocket | null = null
  private ctx: AudioContext | null = null
  private stream: MediaStream | null = null
  private worklet: SttWorkletHandle | null = null
  private stopTimer: number | undefined
  private stopResolve: (() => void) | null = null

  constructor(opts: SttSessionOpts) {
    this.opts = opts
    this.deps = {
      createAudioContext: () => new AudioContext({ sampleRate: 16000 }),
      getUserMedia: () =>
        navigator.mediaDevices.getUserMedia({
          audio: {
            echoCancellation: true,
            noiseSuppression: true,
            autoGainControl: true,
          },
        }),
      loadWorklet: (ctx, stream, onFrame) => this.defaultLoadWorklet(ctx, stream, onFrame),
      connectSocket: () => {
        const ws = new WebSocket(sttWsUrl())
        ws.binaryType = 'arraybuffer'
        return ws as unknown as SttSocket
      },
      ...opts.deps,
    }
  }

  get active(): boolean {
    return this.activeUtterance
  }

  private async defaultLoadWorklet(
    ctx: AudioContext,
    stream: MediaStream,
    onFrame: (f: Float32Array) => void,
  ): Promise<SttWorkletHandle> {
    const url = URL.createObjectURL(new Blob([WORKLET_SRC], { type: 'text/javascript' }))
    await ctx.audioWorklet.addModule(url)
    const node = new AudioWorkletNode(ctx, 'stt-capture', { numberOfOutputs: 0 })
    node.port.onmessage = (e: MessageEvent) => onFrame(e.data as Float32Array)
    const src = ctx.createMediaStreamSource(stream)
    src.connect(node)
    return {
      disconnect() {
        src.disconnect()
        node.disconnect()
      },
    }
  }

  async toggle(): Promise<void> {
    // Repeat toggle while the open path is still assembling is a no-op:
    // the user double-tapped; a stop here would tear down the utterance
    // that is about to start.
    if (this.assembling) return
    if (this.activeUtterance) {
      await this.stop()
      return
    }
    await this.start()
  }

  async stop(): Promise<void> {
    if (this.torn) return
    if (!this.activeUtterance) {
      this.fullTeardown()
      return
    }
    this.socket?.send('{"type":"stop"}')
    await new Promise<void>((resolve) => {
      // done/error tear down the session, which resolves this promise; the
      // timer is the fallback when the bridge never answers.
      this.stopResolve = resolve
      this.stopTimer = window.setTimeout(() => {
        if (this.torn) {
          resolve()
          return
        }
        this.opts.onStatus(t('sttTranscribeFailed'))
        this.fullTeardown()
        resolve()
      }, STOP_TIMEOUT_MS)
    })
  }

  // Diagnostic breadcrumbs through the whole assembly chain: console plus
  // the WUI log buffer via log.ts's console wrapping (mic grant ≠ green —
  // five more hops live between them), so a remote user can paste exactly
  // where the flow stalls.
  private diag(msg: string): void {
    console.log(`[stt] ${msg}`)
  }

  private async start(): Promise<void> {
    // Reset at entry, not on success below: torn stays true after every
    // teardown, so a leftover would both neuter stop() racing this assembly
    // and trip the recheck below on a legitimate restart.
    this.torn = false
    this.assembling = true
    try {
      // The context must be created inside the click gesture (iOS).
      const ctx = this.deps.createAudioContext()
      this.ctx = ctx
      await ctx.resume().catch(() => {})
      let stream: MediaStream
      try {
        stream = await this.deps.getUserMedia()
        this.stream = stream
      } catch (err) {
        // Not every failure is a denial: a non-secure page has no
        // navigator.mediaDevices at all (TypeError), and a hardware-less
        // device reads NotFoundError — blanket "denied" sent the user
        // hunting for a permission toggle that was never the problem.
        const name = (err as DOMException)?.name
        console.log('[stt] ' + `2 FAIL getUserMedia: ${name ?? String(err)}`)
        this.opts.onStatus(t(name === 'NotAllowedError' ? 'sttMicDenied' : 'sttInitFailed'))
        await ctx.close().catch(() => {})
        this.ctx = null
        return
      }
      try {
        this.worklet = await this.deps.loadWorklet(ctx, stream, (f) => this.onFrame(f))
      } catch (err) {
        console.log('[stt] ' + `4 FAIL worklet: ${String(err).slice(0, 80)}`)
        this.opts.onStatus(t('sttInitFailed'))
        stream.getTracks().forEach((tr) => tr.stop())
        await ctx.close().catch(() => {})
        this.stream = null
        this.ctx = null
        return
      }
      // An external stop() (chat.ts disconnect handler) during the awaits
      // above ran fullTeardown before this.stream was assigned, so it could
      // not stop the mic. Abort instead of resurrecting the session.
      if (this.torn) {
        this.worklet?.disconnect()
        this.worklet = null
        stream.getTracks().forEach((tr) => tr.stop())
        this.stream = null
        await ctx.close().catch(() => {})
        return
      }
      const sock = this.deps.connectSocket()
      this.socket = sock
      sock.onmessage = (ev) => this.onSocketMessage(ev.data)
      // Real handshakes take ~200ms; sending the start frame while the
      // socket is CONNECTING throws InvalidStateError and would kill the
      // utterance before onListening — hold it until onopen. onerror and
      // onclose also resolve the wait: a socket that dies mid-handshake
      // never fires onopen, and the torn/!opened recheck below reports and
      // unwinds instead of hanging (or sending on a dead socket).
      let opened = false
      let wakeOpen: () => void = () => {}
      const openPromise = new Promise<void>((resolve) => {
        wakeOpen = resolve
        sock.onopen = () => {
          opened = true
          wakeOpen()
        }
        sock.onerror = () => {
          console.log('[stt] ' + '6 FAIL ws error')
          wakeOpen()
        }
      })
      sock.onclose = () => {
        if (!opened) wakeOpen() // died mid-handshake: wake the await below
        // Unexpected only while an utterance is live; teardown's own close
        // finds torn=true.
        if (!this.torn && this.activeUtterance) {
          console.log('[stt] ' + 'ws closed mid-utterance')
          this.opts.onStatus(t('sttTranscribeFailed'))
          this.fullTeardown()
        }
      }
      await openPromise
      // torn: an external stop() already unwound via fullTeardown — it
      // released the resources, so the optional-chain cleanups below are
      // no-op races, and no status fires (the stop was deliberate).
      // !opened: the socket died mid-handshake; nobody else tears this
      // frame's worklet/mic/context down, so do it here and report,
      // mirroring the assembly abort paths above.
      if (this.torn || !opened) {
        if (!this.torn) this.opts.onStatus(t('sttTranscribeFailed'))
        this.worklet?.disconnect()
        this.worklet = null
        this.stream?.getTracks().forEach((tr) => tr.stop())
        this.stream = null
        await this.ctx?.close().catch(() => {})
        this.ctx = null
        this.socket = null
        return
      }
      const prompt = extractPromptTerms(this.opts.getPromptTexts())
      sock.send(JSON.stringify({ type: 'start', prompt }))
      this.activeUtterance = true
      this.opts.onStatus(null)
      this.opts.onUtteranceStart?.()
      this.opts.onListening(true)
    } finally {
      this.assembling = false
    }
  }

  private onFrame(f: Float32Array): void {
    if (!this.activeUtterance) return
    const bytes = floatTo16Le(f)
    if (bytes.length === 0) return
    const sock = this.socket
    if (!sock) return
    if (sock.bufferedAmount > MAX_QUEUED_BYTES) return // backpressure drop
    if (!this.ready) {
      // Cold model load can delay ready; pending is bounded above.
      if (this.pendingBytes + bytes.length > MAX_QUEUED_BYTES) return
      this.pending.push(bytes)
      this.pendingBytes += bytes.length
      return
    }
    sock.send(bytes.buffer as ArrayBuffer)
  }

  private onSocketMessage(data: unknown): void {
    if (typeof data !== 'string') return
    let msg: {
      type: string
      mode?: string
      text?: string
      message?: string
    }
    try {
      msg = JSON.parse(data)
    } catch {
      return
    }
    switch (msg.type) {
      case 'ready':
        this.ready = true
        console.log('[stt] ' + `7 ready (${msg.mode ?? '?'}) — speak`)
        for (const b of this.pending) this.socket?.send(b.buffer as ArrayBuffer)
        this.pending = []
        this.pendingBytes = 0
        break
      case 'delta':
        this.opts.onText(msg.text ?? '')
        break
      case 'partial':
        this.opts.onPartial?.(msg.text ?? '')
        break
      case 'done':
        this.opts.onDone(msg.text ?? '')
        this.fullTeardown()
        break
      case 'error':
        console.error('stt:', msg.message)
        this.opts.onStatus(t('sttTranscribeFailed'))
        this.fullTeardown()
        break
    }
  }

  private fullTeardown(): void {
    if (this.torn) return
    this.torn = true
    this.activeUtterance = false
    this.ready = false
    this.pending = []
    this.pendingBytes = 0
    window.clearTimeout(this.stopTimer)
    if (this.stopResolve) {
      const resolve = this.stopResolve
      this.stopResolve = null
      resolve()
    }
    this.worklet?.disconnect()
    this.worklet = null
    this.stream?.getTracks().forEach((tr) => tr.stop())
    this.stream = null
    const sock = this.socket
    this.socket = null
    if (sock) {
      // Neuter callbacks first: our own close must not re-enter teardown.
      sock.onclose = null
      sock.onmessage = null
      sock.close()
    }
    const ctx = this.ctx
    this.ctx = null
    void ctx?.close().catch(() => {})
    this.opts.onListening(false)
  }
}
