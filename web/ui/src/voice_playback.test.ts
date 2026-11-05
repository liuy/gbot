import { describe, it, expect, vi } from 'vitest'
import {
  createPlayback,
  JitterBufferCore,
  PLAYBACK_WORKLET_SRC,
  type PlaybackDeps,
} from './voice_playback'

// Structural fakes — jsdom has no Web Audio. FakeWorkletNode models the real
// worklet's death semantics: once the processor returned false (interrupt or
// buffer drain, announced as 'stop'), its port drops every postMessage —
// writes after death fall into the void unless the caller rebuilt the node.
class FakeWorkletNode {
  port: FakePort
  connectedTo: unknown[] = []
  disconnected = false
  dead = false
  constructor() {
    this.port = new FakePort(this)
  }
  connect(t: unknown) {
    this.connectedTo.push(t)
  }
  disconnect() {
    this.disconnected = true
  }
}

class FakePort {
  posted: unknown[] = [] // messages the live processor received
  dropped: unknown[] = [] // messages lost to a dead processor
  onmessage: ((e: { data: unknown }) => void) | null = null
  constructor(private node: FakeWorkletNode) {}
  postMessage(data: unknown) {
    if (this.node.dead) {
      this.dropped.push(data)
      return
    }
    this.posted.push(data)
    if ((data as { event?: string })?.event === 'interrupt') {
      // process() returns false on the next render quantum and posts 'stop';
      // collapsed to a microtask here.
      this.node.dead = true
      queueMicrotask(() => this.onmessage?.({ data: { event: 'stop' } }))
    }
  }
}

class FakeAnalyser {
  connectedTo: unknown[] = []
  data = new Uint8Array(1024)
  fftSize?: number
  smoothingTimeConstant?: number
  connect(t: unknown) {
    this.connectedTo.push(t)
  }
  getByteTimeDomainData(arr: Uint8Array) {
    arr.set(this.data.subarray(0, arr.length))
  }
}

class FakeCtx {
  static created = 0
  sampleRate = 24000
  destination = { name: 'destination' }
  addedModules: string[] = []
  nodes: FakeWorkletNode[] = []
  workletNode: FakeWorkletNode | null = null
  resumed = false
  closed = false
  audioWorklet = {
    addModule: async (url: string) => {
      this.addedModules.push(url)
    },
  }
  createAnalyser() {
    return new FakeAnalyser()
  }
  resume() {
    this.resumed = true
    return Promise.resolve()
  }
  close() {
    this.closed = true
    return Promise.resolve()
  }
}

function makeDeps(): { deps: PlaybackDeps; ctx: FakeCtx } {
  const ctx = new FakeCtx()
  const deps: PlaybackDeps = {
    createContext: () => {
      FakeCtx.created++
      return ctx as unknown as never
    },
    makeNode: () => {
      const n = new FakeWorkletNode()
      ctx.nodes.push(n)
      ctx.workletNode = n
      return n as unknown as never
    },
  }
  return { deps, ctx }
}

// Holds ensureAudio before its addModule step resolves: writes landing in
// this window see no makeNode — the cold-start buffering path under test.
function gatedDeps(): { deps: PlaybackDeps; ctx: FakeCtx; release: () => void } {
  const { deps, ctx } = makeDeps()
  let release!: () => void
  const gate = new Promise<void>((r) => (release = r))
  const realAdd = ctx.audioWorklet.addModule
  ctx.audioWorklet.addModule = async (url: string) => {
    await gate
    return realAdd(url)
  }
  return { deps, ctx, release }
}

beforeEach(() => {
  FakeCtx.created = 0
})

describe('PLAYBACK_WORKLET_SRC', () => {
  it('is the OpenAI stream processor, self-contained (no imports)', () => {
    expect(PLAYBACK_WORKLET_SRC).toContain("registerProcessor('stream_processor'")
    expect(PLAYBACK_WORKLET_SRC).toContain('writeData')
    expect(PLAYBACK_WORKLET_SRC).not.toMatch(/\bimport\b/)
  })
})

interface WorkletProc {
  port: { postMessage(data: unknown): void; onmessage: ((e: { data: unknown }) => void) | null }
  process(inputs: unknown, outputs: Float32Array[][]): boolean
}

// Drives the worklet source headlessly: the src is authored for
// AudioWorkletGlobalScope, so fake the three globals it touches
// (AudioWorkletProcessor base class, sampleRate, registerProcessor) and run
// the registered processor's message handlers + process() directly.
class WorkletHost {
  posted: { event?: string }[] = []
  dead = false
  private proc: WorkletProc

  constructor(src: string, sampleRate = 24000) {
    const posted = this.posted
    class BaseProcessor {
      port: WorkletProc['port']
      constructor() {
        this.port = {
          postMessage: (data: unknown) => {
            posted.push(data as { event?: string })
          },
          onmessage: null,
        }
      }
    }
    let Ctor: (new () => WorkletProc) | null = null
    const registerProcessor = (_name: string, cls: new () => WorkletProc) => {
      Ctor = cls
    }
    // Under Stryker instrumentation, JitterBufferCore's class body (embedded
    // into the worklet src via toString()) calls the module-scoped helpers
    // stryMutAct_*/stryCov_* which do not exist inside new Function's scope.
    // Inject them as parameters — no active mutant and no-op coverage — so
    // the embedded copy always runs original code; JitterBufferCore mutants
    // are judged by the direct JitterBufferCore suites below.
    const strykerHelpers = [...new Set(src.match(/\bstry(?:MutAct|Cov)_\w+/g) ?? [])]
    new Function(
      'AudioWorkletProcessor',
      'sampleRate',
      'registerProcessor',
      ...strykerHelpers,
      `"use strict";\n${src}`,
    )(
      BaseProcessor,
      sampleRate,
      registerProcessor,
      ...strykerHelpers.map((h) => (h.includes('MutAct') ? () => false : () => {})),
    )
    if (!Ctor) throw new Error('worklet source registered no processor')
    this.proc = new Ctor()
  }

  // A dead processor's port drops messages (same semantics as FakePort).
  write(samples: Float32Array): void {
    if (this.dead) return
    const pcm = new Int16Array(samples.length)
    for (let i = 0; i < samples.length; i++) pcm[i] = Math.round(samples[i] * 0x8000)
    this.proc.port.onmessage?.({ data: { event: 'write', buffer: pcm, trackId: 'tts' } })
  }

  interrupt(): void {
    if (this.dead) return
    this.proc.port.onmessage?.({ data: { event: 'interrupt' } })
  }

  end(): void {
    if (this.dead) return
    this.proc.port.onmessage?.({ data: { event: 'end' } })
  }

  tick(quanta: number): Float32Array[] {
    const outs: Float32Array[] = []
    for (let q = 0; q < quanta && !this.dead; q++) {
      const out = new Float32Array(128)
      if (!this.proc.process([], [[out]])) this.dead = true
      outs.push(out)
    }
    return outs
  }
}

const tone = (amp: number, frames: number): Float32Array => new Float32Array(frames).fill(amp)

const everySilent = (outs: Float32Array[]): boolean => {
  for (const q of outs) {
    for (const s of q) if (s !== 0) return false
  }
  return true
}

const audibleFrames = (outs: Float32Array[], threshold = 0.1): number => {
  let n = 0
  for (const q of outs) {
    for (const s of q) if (Math.abs(s) > threshold) n++
  }
  return n
}

const events = (h: WorkletHost, kind: string): unknown[] =>
  h.posted.filter((m) => m.event === kind)

describe('worklet jitter buffer (hf-realtime-voice PR #13 port)', () => {
  it('does not start playback on the first chunk — prebuffer holds 500ms', () => {
    const h = new WorkletHost(PLAYBACK_WORKLET_SRC)
    h.write(tone(0.25, 2400)) // 100ms
    const outs = h.tick(4)
    expect(outs.length).toBe(4)
    expect(everySilent(outs)).toBe(true)
  })

  it('starts once 500ms is queued, fading in from zero amplitude', () => {
    const h = new WorkletHost(PLAYBACK_WORKLET_SRC)
    h.write(tone(0.25, 2400)) // 100ms — under the floor
    const wait = h.tick(2)
    expect(everySilent(wait)).toBe(true)
    h.write(tone(0.25, 9600)) // +400ms → 500ms queued
    const start = h.tick(1)[0]
    expect(start[0]).toBe(0) // fade-in frame 0: gain 0
    expect(start[1]).toBeCloseTo(0.25 / 96, 5) // gain 1/96
    expect(Math.abs(start[96])).toBeGreaterThan(0.2) // ramp done: full amplitude
  })

  it('no time-based flush: under the floor it waits for the floor, however long', () => {
    // A forced thin start is what turned the cold first sentence into a
    // cascade of underrun pops — waiting for the floor is the whole point.
    const h = new WorkletHost(PLAYBACK_WORKLET_SRC)
    h.write(tone(0.25, 2400)) // 100ms only — the 500ms floor is never met
    const waits = h.tick(160) // 20480 frames ≈ 850ms of waiting: still silent
    expect(everySilent(waits)).toBe(true)
    // Crossing the floor resumes with a fade-in.
    h.write(tone(0.25, 9600)) // +400ms → 500ms queued
    const resume = h.tick(1)[0]
    expect(resume[0]).toBe(0)
    expect(resume[1]).toBeCloseTo(0.25 / 96, 5)
    expect(Math.abs(resume[96])).toBeGreaterThan(0.2)
  })

  it('underrun mid-playback: ramps out, re-arms prebuffer, ramps back in', () => {
    const h = new WorkletHost(PLAYBACK_WORKLET_SRC)
    h.write(tone(0.25, 12000)) // 500ms — the floor exactly
    const start = h.tick(1)[0]
    expect(start[0]).toBe(0) // fade-in from zero
    // Playback consumes 12000 real frames; dry at frame 12000, ramp 12000..12095.
    const mid = h.tick(93) // quanta 1..93 = frames 128..12031
    const u = mid[92] // quantum 93 = frames 11904..12031
    expect(Math.abs(u[95])).toBeGreaterThan(0.2) // last real sample (frame 11999)
    expect(u[96]).toBeCloseTo(0.25, 5) // ramp frame 0 CONTINUES the last sample — no step
    expect(u[97]).toBeCloseTo((0.25 * 95) / 96, 5) // linear decay begins
    const tail = h.tick(1)[0] // quantum 94 = frames 12032..12159
    expect(tail[63]).toBeCloseTo(0.25 / 96, 5) // ramp tail (frame 12095)
    expect(tail[64]).toBe(0) // silence after the ramp
    expect(events(h, 'underrun')).toHaveLength(1)
    expect(events(h, 'stop')).toHaveLength(0) // an underrun is not a drain
    // Re-arm: 100ms more does not resume…
    h.write(tone(0.25, 2400))
    const stillWaiting = h.tick(3)
    expect(stillWaiting.length).toBe(3)
    expect(everySilent(stillWaiting)).toBe(true)
    // …another 400ms crosses the 500ms floor and resumes with a fade-in.
    h.write(tone(0.25, 9600))
    const resume = h.tick(1)[0]
    expect(resume[0]).toBe(0)
    expect(resume[1]).toBeCloseTo(0.25 / 96, 5)
    expect(Math.abs(resume[96])).toBeGreaterThan(0.2)
  })

  it('a 35s backlog is never trimmed and plays out in full (runaway net stays retired)', () => {
    const h = new WorkletHost(PLAYBACK_WORKLET_SRC)
    for (let i = 0; i < 35; i++) h.write(tone(0.25, 24000)) // 35s backlog
    const outs = h.tick(6700) // drain all 840000 frames (6563 quanta + edges)
    h.end()
    const fin = h.tick(3)
    expect(h.dead).toBe(true) // end-of-stream retires the processor
    expect(events(h, 'stop')).toHaveLength(1)
    const audible = audibleFrames([...outs, ...fin])
    expect(audible).toBeGreaterThan(839500) // full 35s kept…
    expect(audible).toBeLessThan(840500) // …(± fade/edge bookkeeping noise)
  })

  it('a normal-length reply is never trimmed (2s plays in full)', () => {
    const h = new WorkletHost(PLAYBACK_WORKLET_SRC)
    for (let i = 0; i < 2; i++) h.write(tone(0.25, 24000)) // 2s
    const outs = h.tick(600)
    h.end()
    h.tick(3)
    expect(h.dead).toBe(true)
    const audible = audibleFrames(outs)
    expect(audible).toBeGreaterThan(47900)
    expect(audible).toBeLessThan(48100)
  })

  // EOS regression (5G + tailscale burst arrival): a chunk gap far past any
  // silence heuristic must never kill the node — the fetch settle's explicit
  // end is the only stream terminator besides interrupt. Old behavior: the
  // 500ms drain guess died mid-gap and dropped every later burst.
  it('burst arrival: a >500ms mid-stream gap never kills the node; end() plays out the tail', () => {
    const h = new WorkletHost(PLAYBACK_WORKLET_SRC)
    h.write(tone(0.25, 24000)) // burst one: 1s
    const gap = h.tick(300) // 38400 frames: burst one + ramp + >500ms dry
    expect(h.dead).toBe(false) // the old drain detector died in this window
    h.write(tone(0.25, 24000)) // burst two lands after the gap
    const tail = h.tick(200)
    expect(h.dead).toBe(false)
    h.end() // the fetch settled: explicit end-of-stream
    const fin = h.tick(3)
    expect(h.dead).toBe(true)
    expect(events(h, 'stop')).toHaveLength(1)
    const audible = audibleFrames([...gap, ...tail, ...fin])
    expect(audible).toBeGreaterThan(47600) // both full seconds played out
    expect(audible).toBeLessThan(48200)
  })
})

describe('JitterBufferCore', () => {
  const run = (core: JitterBufferCore, frames: number): Float32Array => {
    const all = new Float32Array(frames)
    let alive = true
    for (let f = 0; f < frames && alive; f += 128) {
      alive = core.tick(all.subarray(f, Math.min(f + 128, frames)))
    }
    return all
  }

  it('holds playback one sample short of the 500ms floor, then fades in exactly', () => {
    const core = new JitterBufferCore(24000, () => {})
    core.write(new Float32Array(11999).fill(0.5))
    expect(run(core, 256).every((s) => s === 0)).toBe(true)
    core.write(new Float32Array(1).fill(0.5)) // 12000 queued = the floor
    const start = run(core, 128)
    expect(start[0]).toBe(0) // gain 0/96
    expect(start[1]).toBeCloseTo(0.5 / 96, 6)
    expect(start[95]).toBeCloseTo((0.5 * 95) / 96, 6)
    expect(start[96]).toBe(0.5) // ramp done
    expect(start[127]).toBe(0.5)
  })

  it('EOS is the only bypass: under the floor, end() plays the short tail out', () => {
    const core = new JitterBufferCore(24000, () => {})
    core.write(new Float32Array(100).fill(0.5))
    expect(run(core, 13000).every((s) => s === 0)).toBe(true) // no time-based flush
    core.end()
    const out = run(core, 200)
    expect(out[0]).toBe(0) // fade-in from silence
    expect(out[1]).toBeCloseTo(0.5 / 96, 6)
    expect(out[96]).toBe(0.5) // ramp done by frame 96
    expect(out[99]).toBe(0.5) // last real sample
    expect(out[100]).toBeCloseTo(0.5, 6) // EOS ramp continues it — no step
    expect(out[195]).toBeCloseTo(0.5 / 96, 6) // ramp tail
    expect(out.slice(196).some((s) => s !== 0)).toBe(false) // silence after
  })

  it('underrun ramp continues the last sample and decays linearly to zero', () => {
    const got: string[] = []
    const core = new JitterBufferCore(24000, (e) => got.push(e))
    core.write(new Float32Array(12100).fill(0.4)) // over the floor: starts at once
    const out = run(core, 12200) // real frames 0..12099, ramp 12100..12195
    expect(out[12099]).toBeCloseTo(0.4, 6) // last real sample
    expect(out[12100]).toBeCloseTo(0.4, 6) // ramp frame 0 CONTINUES it — no step
    expect(out[12101]).toBeCloseTo((0.4 * 95) / 96, 6)
    expect(out[12195]).toBeCloseTo(0.4 / 96, 6) // ramp tail
    expect(out[12196]).toBe(0) // silence after the ramp
    expect(got).toEqual(['underrun'])
  })

  it('re-arms the floor after an underrun before resuming', () => {
    const core = new JitterBufferCore(24000, () => {})
    core.write(new Float32Array(12000).fill(0.3)) // the floor exactly
    run(core, 128) // starts at frame 0
    run(core, 12000) // consumes through frame 12095 (real + ramp), idle after
    core.write(new Float32Array(11999).fill(0.3))
    expect(run(core, 100).every((s) => s === 0)).toBe(true) // still under the floor
    core.write(new Float32Array(1).fill(0.3))
    const resume = run(core, 128)
    expect(resume[0]).toBe(0) // resume fades in from zero — no hard step
    expect(resume[1]).toBeCloseTo(0.3 / 96, 6)
    expect(resume[96]).toBeCloseTo(0.3, 6) // Float32(0.3) ≠ double 0.3
    expect(resume[127]).toBeCloseTo(0.3, 6)
  })

  it('survives a dry gap far past the old 500ms drain window (end is the only retirement)', () => {
    const core = new JitterBufferCore(24000, () => {})
    core.write(new Float32Array(12000).fill(0.3)) // the floor exactly: starts at once
    let alive = true
    let quanta = 0
    // 12000 real + 96 ramp frames end at 12096; a mid-stream gap is an
    // underrun, not a stream end — the core keeps running forever until the
    // explicit end.
    while (alive && quanta < 600) {
      alive = core.tick(new Float32Array(128))
      quanta++
    }
    expect(quanta).toBe(600)
    expect(alive).toBe(true)
    core.end()
    expect(core.tick(new Float32Array(128))).toBe(false)
  })

  it('trims one runaway chunk to the 15min ceiling and announces the drop', () => {
    // 1kHz sample rate keeps the allocation small; the ceiling is in ms, so
    // 900000ms = 900000 samples here either way.
    const emitted: Array<[string, number?]> = []
    const core = new JitterBufferCore(1000, (ev, dropped) => emitted.push([ev, dropped]))
    core.write(new Float32Array(900001).fill(0.3)) // 15min + 1 sample in ONE chunk
    expect(core._queuedSamples()).toBe(900000)
    // The drop must be loud — silent loss is what hid the skip from the user.
    expect(emitted).toContainEqual(['trim', 1])
  })

  // A local engine (RTF 0.5) legitimately outpaces playback on long runs:
  // the queue grows past the reference's 30s cap and every trimmed sample is
  // an audible forward skip. The ceiling is a runaway net, not a flow
  // controller — a 35s backlog (impossible for cloud pacing, routine for a
  // local engine) must be retained sample-exact.
  it('a 35s backlog past the old 30s cap is retained sample-exact (no trim)', () => {
    const core = new JitterBufferCore(24000, () => {})
    for (let i = 0; i < 35; i++) {
      // Distinct amplitudes so a trimmed sample is distinguishable in content.
      const chunk = new Float32Array(24000).fill((i + 1) / 36)
      core.write(chunk)
      run(core, 35) // the live path drains while producing; 35 samples total
    }
    // Lossless proof: after end, every written sample plays out — old 30s
    // cap trimmed to 720000 (~14% loss) → red; the 15min ceiling retains
    // all but ~0.13% of fade/quantum-edge bookkeeping.
    core.end()
    let played = 0
    const buf = new Float32Array(128)
    let alive = true
    while (alive) {
      alive = core.tick(buf)
      played += buf.length
    }
    expect(played).toBeGreaterThan(838500)
    expect(played).toBeLessThan(840500)
  })

  it('end() with a dry queue retires on the next quantum', () => {
    const core = new JitterBufferCore(24000, () => {})
    core.write(new Float32Array(100).fill(0.4))
    core.end() // bounded data: the short tail plays out now (no flush existed anyway)
    const out = run(core, 200) // 100 real + 96 ramp, then retire
    expect(out[99]).toBeCloseTo(0.4, 6) // last real sample
    expect(core.tick(new Float32Array(128))).toBe(false) // bounded data: done
  })

  it('end() flushes a prebuffered tail at once instead of waiting the floor', () => {
    const core = new JitterBufferCore(24000, () => {})
    core.write(new Float32Array(100).fill(0.5)) // far under the 500ms floor
    core.end()
    const out = run(core, 128)
    expect(out[0]).toBe(0) // starts NOW with the fade-in's zero frame
    expect(out[1]).toBeCloseTo(0.5 / 96, 6)
    expect(out[95]).toBeCloseTo((0.5 * 95) / 96, 6)
  })

  it('end() during playback plays the queue out, ramps down, retires — no underrun report', () => {
    const got: string[] = []
    const core = new JitterBufferCore(24000, (e) => got.push(e))
    core.write(new Float32Array(12000).fill(0.4))
    run(core, 128) // starts at the floor, 128 consumed
    core.end()
    const out = run(core, 13000) // 11872 real + 96 ramp frames, then retire
    expect(out[11871]).toBeCloseTo(0.4, 6) // last real sample
    expect(out[11872]).toBeCloseTo(0.4, 6) // ramp frame 0 CONTINUES it — no step
    expect(out[11967]).toBeCloseTo(0.4 / 96, 6) // linear decay tail
    expect(got).toEqual([]) // the final ramp is the EOS tail, not an underrun
    expect(core.tick(new Float32Array(128))).toBe(false)
  })

  it('a write landing mid-ramp-out re-arms the floor when the underrun finishes', () => {
    const core = new JitterBufferCore(24000, () => {})
    core.write(new Float32Array(12000).fill(0.3)) // the floor exactly
    run(core, 12048) // mid ramp-out: real frames consumed, ramp 48/96 deep
    // write() cannot arm while the ramp owns the core — only _finishRampOut
    // may arm the queued data for the next burst.
    core.write(new Float32Array(12000).fill(0.3))
    const out = run(core, 600)
    // ramp ends at frame 12095; re-armed floor is already met → resume at 12096
    expect(out[400]).toBeCloseTo(0.3, 6)
    expect(out[599]).toBeCloseTo(0.3, 6)
  })

  it('trim drops whole head buffers once the head alone cannot cover the excess', () => {
    const emitted: Array<[string, number?]> = []
    const core = new JitterBufferCore(1000, (ev, dropped) => emitted.push([ev, dropped]))
    core.write(new Float32Array(100).fill(0.1))
    core.write(new Float32Array(901000).fill(0.2))
    expect(core._queuedSamples()).toBe(900000)
    expect(emitted).toEqual([['trim', 1100]])
  })

  it('trim clamps a partial drop to the head buffer’s unread samples', () => {
    const emitted: Array<[string, number?]> = []
    const core = new JitterBufferCore(1000, (ev, dropped) => emitted.push([ev, dropped]))
    core.write(new Float32Array(5000).fill(0.1))
    core.write(new Float32Array(896000).fill(0.2))
    expect(core._queuedSamples()).toBe(900000)
    expect(emitted).toEqual([['trim', 1000]])
  })

  it('trim resumes inside a partially consumed head on later writes', () => {
    const emitted: Array<[string, number?]> = []
    const core = new JitterBufferCore(1000, (ev, dropped) => emitted.push([ev, dropped]))
    core.write(new Float32Array(60).fill(0.1))
    core.write(new Float32Array(30).fill(0.2))
    core.write(new Float32Array(899950).fill(0.3))
    expect(core._queuedSamples()).toBe(900000)
    expect(emitted).toEqual([['trim', 40]])
    core.write(new Float32Array(260).fill(0.4))
    expect(core._queuedSamples()).toBe(900000)
    expect(emitted).toEqual([['trim', 40], ['trim', 260]])
  })

  it('a trimmed queue plays out its exact remaining samples — no phantom reads', () => {
    const core = new JitterBufferCore(1000, () => {})
    core.write(new Float32Array(60).fill(0.1))
    core.write(new Float32Array(30).fill(0.2))
    core.write(new Float32Array(899950).fill(0.3)) // trims 40: A keeps 20, then B, then C
    core.end()
    const out = run(core, 900400)
    for (let i = 0; i < out.length; i++) {
      if (!Number.isFinite(out[i])) throw new Error(`non-finite sample at ${i}`)
    }
    // Fade-in done by frame 96; the tail still carries C's amplitude.
    expect(out[100]).toBeCloseTo(0.3, 6)
    expect(out[5000]).toBeCloseTo(0.3, 6)
    expect(out[899900]).toBeCloseTo(0.3, 6)
  })
})

describe('createPlayback', () => {
  it('creates zero AudioContexts at construction (D8: creation waits for a user gesture)', () => {
    const { deps } = makeDeps()
    createPlayback(deps)
    expect(FakeCtx.created).toBe(0)
  })

  it('ensureAudio creates ctx+worklet+analyser chain once, idempotent on repeat', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    expect(FakeCtx.created).toBe(1)
    expect(ctx.addedModules.length).toBe(1)
    expect(ctx.resumed).toBe(true)
    const node = ctx.workletNode!
    const analyser = node.connectedTo[0] as FakeAnalyser
    expect(analyser).toBeTruthy()
    expect(analyser.connectedTo[0]).toBe(ctx.destination)
    await p.ensureAudio()
    expect(FakeCtx.created).toBe(1)
    expect(ctx.addedModules.length).toBe(1)
  })

  it('write() forwards even-length PCM as Int16 write events with carry for odd tails', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02, 0xff, 0xfe]))
    p.write(new Uint8Array([0x03, 0x04])) // pairs with the carried 0xff? no: [03 04] even
    const posted = ctx.workletNode!.port.posted as { event: string; buffer: Int16Array }[]
    expect(posted.length).toBe(2)
    expect(posted[0].event).toBe('write')
    expect(posted[0].buffer).toEqual(new Int16Array([0x0201, 0xfeff]))
    expect(posted[1].buffer).toEqual(new Int16Array([0x0403]))
  })

  it('write() carries an odd trailing byte across calls', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02, 0x03]))
    expect(ctx.workletNode!.port.posted.length).toBe(1)
    p.write(new Uint8Array([0x04, 0x05, 0x06]))
    const posted = ctx.workletNode!.port.posted as { buffer: Int16Array }[]
    expect(posted.length).toBe(2)
    expect(posted[1].buffer).toEqual(new Int16Array([0x0403, 0x0605]))
  })

  it('write() during the cold-start window queues PCM; attach flushes it in order', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02, 0x03]))
    p.write(new Uint8Array([0x04]))
    p.write(new Uint8Array([0x05, 0x06]))
    expect(ctx.nodes).toHaveLength(0) // no node exists yet — nothing to receive
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { event?: string; buffer?: Int16Array }[]
    expect(posted).toHaveLength(3)
    expect(posted[0].buffer).toEqual(new Int16Array([0x0201]))
    expect(posted[1].buffer).toEqual(new Int16Array([0x0403]))
    expect(posted[2].buffer).toEqual(new Int16Array([0x0605]))
    p.write(new Uint8Array([0x07, 0x08]))
    expect((posted.at(-1) as { buffer: Int16Array }).buffer).toEqual(new Int16Array([0x0807]))
  })

  it('interrupt() discards queued PCM — the cut stream never plays on the next attach', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02]))
    p.interrupt()
    p.write(new Uint8Array([0x05, 0x06]))
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { buffer?: Int16Array }[]
    expect(posted).toHaveLength(1)
    expect(posted[0].buffer).toEqual(new Int16Array([0x0605]))
  })

  it('odd tail at flush stays a tail — queued bytes go out verbatim, carry survives for the next live write', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02, 0x03, 0x04, 0x05])) // odd count: 05 lands in carry
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { buffer?: Int16Array }[]
    expect(posted).toHaveLength(1)
    expect(posted[0].buffer).toEqual(new Int16Array([0x0201, 0x0403]))
    p.write(new Uint8Array([0x06, 0x07])) // live write: the carried 05 pairs with 06
    expect((posted.at(-1) as { buffer: Int16Array }).buffer).toEqual(new Int16Array([0x0605]))
  })

  it('end() in the node-less window is deferred — the attached worklet still retires the stream', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02]))
    p.end()
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { event?: string }[]
    // Order is load-bearing: data before EOS — 'end' first would retire the
    // worklet on a stream it never played.
    expect(posted.map((m) => m.event)).toEqual(['write', 'end'])
  })

  it('interrupt() clears a deferred EOS — the next stream starts without a stale end', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02]))
    p.end() // deferred
    p.interrupt() // the round is cut — its EOS is void
    p.write(new Uint8Array([0x05, 0x06])) // next stream, still node-less
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { event?: string; buffer?: Int16Array }[]
    expect(posted.filter((m) => m.event === 'write')).toHaveLength(1)
    expect((posted[0] as { buffer: Int16Array }).buffer).toEqual(new Int16Array([0x0605]))
    expect(posted.filter((m) => m.event === 'end')).toHaveLength(0)
  })

  it('teardown() discards queued PCM — a dead session never reaches a node', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02]))
    await p.teardown()
    release()
    await ready
    expect(ctx.nodes).toHaveLength(0)
  })

  it('pending caps at ~30s: oldest whole chunk drops, newest survives', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    for (let i = 0; i < 31; i++) p.write(new Uint8Array(48000).fill(i + 1)) // 31 × 1s
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { buffer?: Int16Array }[]
    const totalBytes = posted.reduce((a, m) => a + m.buffer!.length * 2, 0)
    expect(totalBytes).toBe(1440000) // 31s written, the oldest 1s fell off
    expect(posted[0].buffer![0]).toBe(0x0202) // marker 2: chunk 0 is gone
    expect((posted.at(-1) as { buffer: Int16Array }).buffer[0]).toBe(0x1f1f) // marker 31: newest kept
  })

  it('pending trim is sample-aligned: a partial cut lands on an even byte offset', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    const head = new Uint8Array(48000)
    for (let k = 0; k < 48000; k++) head[k] = k & 0xff
    p.write(head)
    for (let i = 1; i < 30; i++) p.write(new Uint8Array(48000).fill(i + 1)) // 30s total
    p.write(new Uint8Array(100).fill(0xff)) // 100 bytes over the cap
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { buffer?: Int16Array }[]
    const totalBytes = posted.reduce((a, m) => a + m.buffer!.length * 2, 0)
    expect(totalBytes).toBe(1440000)
    // Exactly 50 samples fell off the front at a sample boundary: the first
    // delivered sample is bytes 100|101, not a mispaired half-sample splice.
    expect(posted[0].buffer![0]).toBe(100 | (101 << 8))
    expect(posted[0].buffer!.length).toBe((48000 - 100) / 2)
  })

  // Uneven blocks make the trim loop drop MORE than one whole head in a
  // single push (worklet never loads + TTS chunk sizes vary naturally). Each
  // whole-drop must shift the head: keeping it as an empty subarray makes
  // drop=min(0,excess)=0 forever and the loop never terminates.
  it('pending cap with uneven blocks drops several whole heads in one trim', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    for (let i = 0; i < 30; i++) p.write(new Uint8Array(48000).fill(i + 1)) // 30 × 1s = cap
    p.write(new Uint8Array(96000).fill(0x7f)) // one 2s block: excess spans two heads
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { buffer?: Int16Array }[]
    const totalBytes = posted.reduce((a, m) => a + m.buffer!.length * 2, 0)
    expect(totalBytes).toBe(1440000) // exactly the cap retained
    expect(posted).toHaveLength(29) // chunks 1+2 gone whole; 28 kept + the 2s block
    expect(posted[0].buffer![0]).toBe(0x0303) // chunk 3 now leads
    expect((posted.at(-1) as { buffer: Int16Array }).buffer[0]).toBe(0x7f7f) // 2s block survives whole
    expect((posted.at(-1) as { buffer: Int16Array }).buffer.length).toBe(48000)
  })

  it('interrupt() posts the interrupt event', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    p.interrupt()
    const posted = ctx.workletNode!.port.posted as { event: string }[]
    expect(posted.at(-1)!.event).toBe('interrupt')
  })

  it('worklet stop event fires the drained callback', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    const drained = vi.fn()
    p.onDrained(drained)
    ctx.workletNode!.port.onmessage!({ data: { event: 'stop' } })
    expect(drained).toHaveBeenCalledTimes(1)
  })

  it('stop event kills the node (disconnect) and the next write builds a fresh one', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    const n1 = ctx.workletNode!
    const drained = vi.fn()
    p.onDrained(drained)
    n1.port.onmessage!({ data: { event: 'stop' } })
    expect(n1.disconnected).toBe(true)
    expect(drained).toHaveBeenCalledTimes(1)
    // The dead processor drops postMessage — the write must reach a NEW node
    // wired to the same analyser (module registered once, node rebuilt).
    p.write(new Uint8Array([0x01, 0x02]))
    const n2 = ctx.workletNode!
    expect(n2).not.toBe(n1)
    expect(n1.port.posted).toHaveLength(0) // nothing routed to the corpse
    expect(n1.port.dropped).toHaveLength(0)
    const last = n2.port.posted.at(-1) as { event?: string }
    expect(last.event).toBe('write')
    expect(n2.connectedTo[0]).toBe(n1.connectedTo[0]) // same analyser
    expect(n2.disconnected).toBe(false)
    expect(ctx.nodes).toHaveLength(2)
    expect(ctx.addedModules.length).toBe(1) // no module re-registration
  })

  it('interrupt retires the node at once; a same-tick body write reaches the new node', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    const n1 = ctx.workletNode!
    const drained = vi.fn()
    p.onDrained(drained)
    p.interrupt()
    // The stop event only arrives after a render quantum — the node must be
    // gone synchronously so the filler→body transition's same-tick write
    // cannot land on the dying processor.
    expect((n1.port.posted.at(-1) as { event?: string }).event).toBe('interrupt')
    expect(n1.disconnected).toBe(true)
    expect(drained).toHaveBeenCalledTimes(1)
    p.write(new Uint8Array([0x01, 0x02]))
    const n2 = ctx.workletNode!
    expect(n2).not.toBe(n1)
    expect((n2.port.posted.at(-1) as { event?: string }).event).toBe('write')
    expect(n2.port.dropped).toHaveLength(0)
    // The real worklet's late stop lands on the retired node: no second
    // drain callback, the rebuilt node stays connected.
    await Promise.resolve()
    await Promise.resolve()
    expect(drained).toHaveBeenCalledTimes(1)
    expect(n2.disconnected).toBe(false)
    expect(ctx.workletNode).toBe(n2)
  })

  it('rms() reads time-domain RMS (readVol spec: 1024 frames, 0.55 smoothing, ×3.2 clamp 1)', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    const node = ctx.workletNode!
    const analyser = node.connectedTo[0] as FakeAnalyser
    expect(analyser.fftSize).toBe(1024)
    expect(analyser.smoothingTimeConstant).toBeCloseTo(0.55, 5)
    // Silence (all 128) → 0.
    analyser.data.fill(128)
    expect(p.rms()).toBe(0)
    // ±18 around 128 → RMS 0.140625 → ×3.2 = 0.45.
    for (let i = 0; i < 1024; i++) analyser.data[i] = i % 2 === 0 ? 146 : 110
    expect(p.rms()).toBeCloseTo(0.45, 5)
    // Loud speech clamps at 1 (±64 → 0.5×3.2 = 1.6).
    for (let i = 0; i < 1024; i++) analyser.data[i] = i % 2 === 0 ? 192 : 64
    expect(p.rms()).toBe(1)
  })

  it('rms() is 0 before ensureAudio', () => {
    const { deps } = makeDeps()
    const p = createPlayback(deps)
    expect(p.rms()).toBe(0)
  })

  it('end() posts the end event to the live node', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    p.end()
    const posted = ctx.workletNode!.port.posted as { event?: string }[]
    expect(posted.at(-1)!.event).toBe('end')
  })

  it('end() with no queued PCM and no node records nothing — no stale EOS for the next stream', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    p.end() // before ensureAudio — must not throw, must not arm pendingEnd
    const ready = p.ensureAudio()
    await ready
    p.write(new Uint8Array([0x01, 0x02])) // next stream's data
    const posted = ctx.workletNode!.port.posted as { event?: string }[]
    expect(posted.filter((m) => m.event === 'end')).toHaveLength(0)
  })

  it('empty and odd-carry writes post nothing until a whole sample exists', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    p.write(new Uint8Array(0))
    p.write(new Uint8Array([0x01]))
    expect(ctx.workletNode!.port.posted).toHaveLength(0)
    p.write(new Uint8Array([0x02]))
    const posted = ctx.workletNode!.port.posted as {
      event?: string
      buffer?: Int16Array
      trackId?: string
    }[]
    expect(posted).toHaveLength(1)
    expect(posted[0].event).toBe('write')
    expect(posted[0].trackId).toBe('tts')
    expect(posted[0].buffer).toEqual(new Int16Array([0x0201]))
  })

  it('an empty pre-attach write arms no deferred EOS', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    p.write(new Uint8Array(0))
    p.end()
    release()
    await ready
    const posted = ctx.workletNode!.port.posted as { event?: string }[]
    expect(posted.map((m) => m.event)).toEqual([])
  })

  it('a malformed worklet message (no data) is ignored, not a stop', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    const node = ctx.workletNode!
    const drained = vi.fn()
    p.onDrained(drained)
    expect(() => node.port.onmessage!({ data: undefined })).not.toThrow()
    expect(node.disconnected).toBe(false)
    expect(drained).not.toHaveBeenCalled()
  })

  it('worklet trim/underrun diagnostics log to console.debug and do not retire the node', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    const node = ctx.workletNode!
    const drained = vi.fn()
    p.onDrained(drained)
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    node.port.onmessage!({ data: { event: 'underrun', dropped: 5 } })
    expect(dbg).toHaveBeenLastCalledWith('[tts-playback]', 'underrun', 'dropped=5')
    node.port.onmessage!({ data: { event: 'trim' } })
    expect(dbg).toHaveBeenLastCalledWith('[tts-playback]', 'trim', '')
    dbg.mockRestore()
    expect(node.disconnected).toBe(false)
    expect(drained).not.toHaveBeenCalled()
  })

  it('a rebuilt node is not handed a stale end from the retired stream', async () => {
    const { deps, ctx, release } = gatedDeps()
    const p = createPlayback(deps)
    const ready = p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02])) // pending: no node yet
    p.end() // deferred EOS armed
    release()
    await ready // first attach: write, then end — the deferred flag resets
    ctx.workletNode!.port.onmessage!({ data: { event: 'stop' } }) // stream retired
    p.write(new Uint8Array([0x03, 0x04])) // next stream builds a fresh node
    const posted = ctx.workletNode!.port.posted as { event?: string }[]
    expect(posted.map((m) => m.event)).toEqual(['write'])
  })

  it('teardown clears a deferred EOS — a reopened session starts without one', async () => {
    const { deps, ctx } = makeDeps()
    const p = createPlayback(deps)
    await p.ensureAudio()
    p.write(new Uint8Array([0x01, 0x02]))
    await p.teardown()
    await p.ensureAudio()
    p.write(new Uint8Array([0x03, 0x04]))
    const posted = ctx.workletNode!.port.posted as { event?: string }[]
    expect(posted.map((m) => m.event)).toEqual(['write'])
  })

  it('ensureAudio delivers the worklet module as a text/javascript blob of PLAYBACK_WORKLET_SRC', async () => {
    const { deps } = makeDeps()
    let captured: Blob | null = null
    const spy = vi.spyOn(URL, 'createObjectURL').mockImplementation((obj) => {
      captured = obj as Blob
      return 'blob:spy'
    })
    const p = createPlayback(deps)
    await p.ensureAudio()
    spy.mockRestore()
    expect(captured).toBeTruthy()
    expect(captured!.type).toBe('text/javascript')
    await expect(captured!.text()).resolves.toBe(PLAYBACK_WORKLET_SRC)
  })
})
