import { describe, it, expect, vi } from 'vitest'
import { TokenRate } from './token_rate'

describe('TokenRate', () => {
  it('tool_param_delta text extends streamDuration', () => {
    vi.useFakeTimers()
    vi.setSystemTime(10000)
    const tr = new TokenRate()

    // Text burst: t=10000ms and t=10100ms
    tr.add('hello world response text part one', 10)
    vi.setSystemTime(10100)
    tr.add('continuing the response here', 10)

    const durBefore = tr.streamDurationMs()
    expect(durBefore).toBe(100) // 10100 - 10000 = 100ms

    // Tool param delta extends the burst: t=10200ms and t=10300ms
    vi.setSystemTime(10200)
    tr.add('{"command":"ls -la /home"}', 8)
    vi.setSystemTime(10300)
    tr.add('{"description":"check files"}', 6)

    const durAfter = tr.streamDurationMs()
    expect(durAfter).toBe(300) // 10300 - 10000 = 300ms (single burst, no gap)

    vi.useRealTimers()
  })

  it('an empty meter reads null until a delta arms the clock', () => {
    // TTFT semantics: no delta has arrived, so there is nothing to read —
    // the bar shows 0.0 t/s until the first delta.
    const tr = new TokenRate()
    expect(tr.rate()).toBe(null)
    expect(tr.streamDurationMs()).toBe(0)
    expect(tr.localTokens()).toBe(0)
  })

  it('empty text or zero tokens is a no-op', () => {
    vi.useFakeTimers()
    const tr = new TokenRate()
    tr.add('', 50)
    tr.add('some text', 0)
    expect(tr.localTokens()).toBe(0)
    expect(tr.rate()).toBe(null)
    expect(tr.streamDurationMs()).toBe(0)
    vi.useRealTimers()
  })

  it('multi-byte deltas count by wire tokens, not local length', () => {
    vi.useFakeTimers()
    const tr = new TokenRate()
    tr.add('中文增量不按长度计', 42)
    expect(tr.localTokens()).toBe(42)
    vi.useRealTimers()
  })

  it('reset wipes the meter for the next query: fresh evidence, per-query accounting clears', () => {
    // Full clear (user decision, see clear()'s doc): the omp seed — the
    // previous query's evidence carrying over — polluted the next query's
    // first seconds whenever the meter belonged to another engine, so every
    // query is metered independently. The local ledger and stream-duration
    // accounting are per-query and still clear.
    vi.useFakeTimers()
    const tr = new TokenRate()
    for (let i = 0; i < 12; i++) {
      tr.add('chunk', 100)
      if (i < 11) vi.advanceTimersByTime(500)
    }
    const held = tr.rate() // dt=0 read right at the last delta
    expect(held).not.toBe(null)
    tr.pause() // query ended on a tool boundary; reset must clear the flag
    tr.reset()
    // TTFT: reset disarms the clock — no reading until this query's first
    // delta.
    expect(tr.rate()).toBe(null)
    expect(tr.localTokens()).toBe(0)
    expect(tr.streamDurationMs()).toBe(0)
    // The new query's first delta reads alone: 100 tokens landing on both
    // scales over the 300ms prior. A surviving seed would hold the reading
    // near `held` (~200 t/s) instead of this fresh-delta value.
    tr.add('chunk', 100)
    const fresh = tr.rate()
    expect(fresh).not.toBe(null)
    expect(fresh!).toBeCloseTo((200 * 1000) / 300, 6)
    expect(fresh!).toBeGreaterThan(held! * 2)
    vi.useRealTimers()
  })
  it('clear wipes the sums at a session boundary', () => {
    // New session (sidebar new-session / TUI /clear): another session's
    // recording must not blend into this one — clear() wipes the sums and
    // the meter reads fresh.
    vi.useFakeTimers()
    const tr = new TokenRate()
    for (let i = 0; i < 12; i++) {
      tr.add('chunk', 100)
      if (i < 11) vi.advanceTimersByTime(500)
    }
    const held = tr.rate()
    expect(held).not.toBe(null)
    expect(held!).toBeGreaterThan(0)
    tr.clear()
    expect(tr.rate()).toBe(null)
    tr.add('chunk', 10)
    const fresh = tr.rate()
    expect(fresh).not.toBe(null)
    // 10 tokens on both scales over the prior alone — no old-session blend.
    expect(fresh!).toBeLessThanOrEqual((20 / 300) * 1000 * 1.05)
    vi.useRealTimers()
  })
  it('reset wipes the sums: the first delta reads alone', () => {
    // A weak previous turn (60 tokens over 0.75s) reads ~67 t/s; after
    // reset the first delta reads alone — 1 token on both scales over the
    // 300ms prior. The exact value pins that none of the old evidence
    // survived the reset.
    vi.useFakeTimers()
    const tr = new TokenRate()
    for (let i = 0; i < 4; i++) {
      tr.add('c', 15)
      if (i < 3) vi.advanceTimersByTime(250)
    }
    const held = tr.rate() // dt=0 read right at the last delta
    expect(held).not.toBe(null)
    tr.reset()
    // TTFT blank, then the first delta arms the clock on an empty kernel.
    expect(tr.rate()).toBe(null)
    tr.add('c', 1)
    const fresh = tr.rate()
    expect(fresh).not.toBe(null)
    expect(fresh!).toBeLessThan(held! * 0.5)
    expect(fresh!).toBeCloseTo((2 * 1000) / 300, 6)
    vi.useRealTimers()
  })

  it('a cold-start dense burst is bounded by the prior, not the span', () => {
    // 90 tokens packed into the same millisecond: without a gate the reading
    // exists immediately, and the 300ms prior alone bounds it to exactly
    // 180 tokens / 0.3s = 600 t/s — a span-based read would divide by ~0.
    vi.useFakeTimers()
    const tr = new TokenRate()
    tr.add('burst', 30)
    tr.add('burst', 30)
    tr.add('burst', 30)
    expect(tr.rate()).toBeCloseTo((180 * 1000) / 300, 9)
    vi.useRealTimers()
  })

  it('a turn-end burst is bounded by the prior, not the span', () => {
    // 300 tokens within ~4ms: the 300ms prior bounds the reading to ~1950
    // t/s (kernel Σtokens ≈ 600 across the two scales over ~0.008s of time
    // integral) — a span-based read would report 600/0.008 ≈ 75000 t/s.
    vi.useFakeTimers()
    const tr = new TokenRate()
    for (let i = 0; i < 3; i++) {
      tr.add('backlog', 100)
      vi.advanceTimersByTime(2)
    }
    const r = tr.rate()
    expect(r).not.toBe(null)
    expect(r!).toBeGreaterThan(1000)
    expect(r!).toBeLessThan(2500)
    vi.useRealTimers()
  })

  it('reads immediately from the first deltas (~200 t/s stream)', () => {
    // 50 tokens every 250ms: the first delta already reads — exactly
    // Σtokens over the prior (100/0.3 ≈ 333, prior-suppressed rather than
    // span-inflated) — then the reading descends monotonically toward the
    // true rate as real streamed time dilutes the pad.
    vi.useFakeTimers()
    const tr = new TokenRate()
    let prev = Infinity
    for (let i = 1; i <= 24; i++) {
      tr.add('chunk', 50)
      const r = tr.rate() // dt=0 read right at the delta
      expect(r).not.toBe(null)
      if (i === 1) expect(r!).toBeCloseTo((100 * 1000) / 300, 9)
      expect(r!).toBeLessThanOrEqual(prev + 1e-9)
      prev = r!
      vi.advanceTimersByTime(250)
    }
    expect(prev).toBeGreaterThanOrEqual(170)
    expect(prev).toBeLessThanOrEqual(210)
    vi.useRealTimers()
  })

  it('prior convergence: reads immediately, starts low, converges monotonically', () => {
    // tqdm-style: a uniform 500 t/s stream reads from the first deltas,
    // starts below the true rate (the prior dilutes early evidence), rises
    // monotonically as real streamed time dilutes the pad, and is within
    // 30% of the truth by 2s.
    vi.useFakeTimers()
    const tr = new TokenRate()
    const readings: number[] = []
    for (let i = 0; i <= 20; i++) {
      tr.add('chunk', 50) // 50 tokens every 100ms = 500 t/s
      if (i === 2 || i === 3 || i === 10 || i === 20) {
        const r = tr.rate() // dt=0 read right at the delta
        expect(r).not.toBe(null)
        readings.push(r!)
      }
      if (i < 20) vi.advanceTimersByTime(100)
    }
    const [early, mid, late, conv] = readings
    expect(early).toBeLessThanOrEqual(mid + 1e-9)
    expect(mid).toBeLessThanOrEqual(late + 1e-9)
    expect(late).toBeLessThanOrEqual(conv + 1e-9)
    expect(early).toBeLessThan(500) // prior dilutes early evidence
    expect(conv).toBeGreaterThanOrEqual(350) // >= 0.7 x true rate
    vi.useRealTimers()
  })

  it('a single delta reads immediately: Σtokens over the prior alone', () => {
    // A single delta at dt=0 has zero streamed time on every scale: the
    // reading is exactly Σtokens·1000/priorMs, pinning both the two-scale
    // token sum and the pad — 1 token reads 2/0.3 ≈ 6.7, 100 read 666.7.
    vi.useFakeTimers()
    const one = new TokenRate()
    one.add('chunk', 1)
    expect(one.rate()).toBeCloseTo((2 * 1000) / 300, 9)
    const big = new TokenRate()
    big.add('chunk', 100)
    expect(big.rate()).toBeCloseTo((200 * 1000) / 300, 9)
    vi.useRealTimers()
  })

  it('a dead stream slides to zero on its own', () => {
    // No gate: a stopped stream (no pause) must reach zero by itself — the
    // denominator keeps advancing with wall time while the token mass
    // decays. From the single-100-token-delta construction the reading
    // slides monotonically 666.7 → ~6.8 (10s) → ~1.4 (40s) → 0.38 t/s at
    // 60s, where both decay exponents are integer powers of two (2^-12,
    // 2^-3) so the exact value is assertable.
    vi.useFakeTimers()
    const tr = new TokenRate()
    tr.add('chunk', 100)
    expect(tr.rate()).toBeCloseTo((200 * 1000) / 300, 9)
    let prev = Infinity
    for (const step of [10000, 30000, 20000]) {
      vi.advanceTimersByTime(step)
      const r = tr.rate()
      expect(r).not.toBe(null)
      expect(r!).toBeLessThanOrEqual(prev + 1e-9)
      expect(r!).toBeGreaterThan(0)
      prev = r!
    }
    const f5 = 2 ** (-60000 / 5000)
    const f20 = 2 ** (-60000 / 20000)
    const want = (100 * (f5 + f20) * 1000) / ((5000 / Math.LN2) * (1 - f5) + (20000 / Math.LN2) * (1 - f20) + 300)
    expect(prev).toBeCloseTo(want, 6)
    expect(prev).toBeLessThan(2)
    vi.useRealTimers()
  })

  it('tail backlog is bounded, then slides monotonically toward zero', () => {
    vi.useFakeTimers()
    const tr = new TokenRate()
    // Sustained ~100 t/s stream over 10s.
    for (let i = 0; i < 10; i++) {
      tr.add('chunk', 100)
      vi.advanceTimersByTime(1000)
    }
    const r0 = tr.rate()
    expect(r0).not.toBe(null)
    expect(r0!).toBeGreaterThanOrEqual(60)
    expect(r0!).toBeLessThanOrEqual(160)
    // Turn-end backlog: 3 deltas within ~6ms must not replace the reading.
    for (let i = 0; i < 3; i++) {
      tr.add('backlog', 100)
      vi.advanceTimersByTime(2)
    }
    const after = tr.rate()
    expect(after).not.toBe(null)
    expect(after!).toBeLessThan(2 * r0!)
    // Stream stopped: with no gate the endpoint of the slide is zero, not a
    // vanished reading — the denominator keeps advancing while the token
    // mass decays, so the reading slides down monotonically forever.
    let prev = after!
    for (let i = 0; i < 14; i++) {
      vi.advanceTimersByTime(5000)
      const got = tr.rate()
      expect(got).not.toBe(null)
      expect(got!).toBeLessThanOrEqual(prev + 1e-9)
      expect(got!).toBeGreaterThan(0)
      prev = got!
    }
    // ~140s after the stop the construction gives 0.25 t/s: toward zero, on
    // its own.
    vi.advanceTimersByTime(70000)
    const final = tr.rate()
    expect(final).not.toBe(null)
    expect(final!).toBeGreaterThan(0)
    expect(final!).toBeLessThan(5)
    vi.useRealTimers()
  })

  it('reading slides with wall time after the stream stops', () => {
    vi.useFakeTimers()
    const tr = new TokenRate()
    for (let i = 0; i < 12; i++) {
      tr.add('chunk', 100)
      vi.advanceTimersByTime(500)
    }
    const r1 = tr.rate()
    expect(r1).not.toBe(null)
    expect(r1!).toBeGreaterThan(0)
    vi.advanceTimersByTime(5000)
    const r2 = tr.rate()
    expect(r2).not.toBe(null)
    expect(r2!).toBeGreaterThan(0)
    expect(r2!).toBeLessThan(r1!)
    vi.useRealTimers()
  })

  it('localTokens accumulates wire tokens and reconcileLocal zeroes only them', () => {
    vi.useFakeTimers()
    const tr = new TokenRate()
    vi.setSystemTime(10000)
    tr.add('chunk one', 150)
    vi.setSystemTime(14400)
    tr.add('chunk two', 150)
    expect(tr.localTokens()).toBe(300)
    const before = tr.rate()
    expect(before).not.toBe(null)
    tr.reconcileLocal()
    expect(tr.localTokens()).toBe(0)
    // Reconciling the live counter must not touch the rate sums.
    expect(tr.rate()).toBe(before)
    vi.useRealTimers()
  })

  it('reconcileLocal before any delta is safe (usage-first)', () => {
    const tr = new TokenRate()
    tr.reconcileLocal()
    expect(tr.localTokens()).toBe(0)
    expect(tr.rate()).toBe(null)
  })

  it('pause stops the clock; the tool gap neither ages the sums nor counts as time', () => {
    // omp begin()/end(): "Tool execution between messages neither ages the
    // sums nor counts as time". While paused there is no reading; the first
    // delta of the next message re-arms the clock at its own timestamp.
    vi.useFakeTimers()
    const paused = new TokenRate()
    for (let i = 0; i < 12; i++) {
      paused.add('chunk', 100)
      vi.advanceTimersByTime(500)
    }
    paused.pause()
    expect(paused.rate()).toBe(null)
    vi.advanceTimersByTime(5000) // tool execution
    paused.add('chunk', 100) // first delta of the next message resumes
    const p = paused.rate() // read at the resume instant (dt=0)

    // Control: identical wall-clock sequence without the pause — the 5s gap
    // ages the sums and lands in the time denominator (~113 t/s vs ~238 t/s
    // on a 200 t/s stream, well past the 1.5x margin).
    const control = new TokenRate()
    for (let i = 0; i < 12; i++) {
      control.add('chunk', 100)
      vi.advanceTimersByTime(500)
    }
    vi.advanceTimersByTime(5000)
    control.add('chunk', 100)

    const c = control.rate()
    expect(p).not.toBe(null)
    expect(c).not.toBe(null)
    expect(p!).toBeGreaterThan(1.5 * c!)
    vi.useRealTimers()
  })

  it('pause closes the burst at message end; tool gaps (long and short) are excluded', () => {
    // Bursts are delimited by pause (tool_start), not a duration threshold:
    // a burst closes at the pause time — span runs first delta → boundary,
    // silent tail included — and any tool gap, long or short, is excluded.
    // Under the old 2s threshold this same sequence read 2000ms.
    vi.useFakeTimers()
    vi.setSystemTime(10000)
    const tr = new TokenRate()
    tr.add('message one tokens', 10) // t=10000
    vi.advanceTimersByTime(100)
    tr.add('message one more', 10) // t=10100
    vi.advanceTimersByTime(300)
    tr.pause() // t=10400: burst 1 = 400ms (tail included)
    vi.advanceTimersByTime(5000) // long tool
    tr.add('message two tokens', 10) // t=15400
    vi.advanceTimersByTime(200)
    tr.add('message two more', 10) // t=15600
    vi.advanceTimersByTime(100)
    tr.pause() // t=15700: burst 2 = 300ms
    vi.advanceTimersByTime(1500) // short tool (merged under the old 2s threshold)
    tr.add('message three tokens', 10) // t=17200
    vi.advanceTimersByTime(100)
    tr.add('message three more', 10) // t=17300
    // 400 + 300 + 100 = 800ms; the 5s and 1.5s tool gaps count nothing.
    expect(tr.streamDurationMs()).toBe(800)
    vi.useRealTimers()
  })

  it('a mid-message thinking stall counts as stream time', () => {
    // No tool_start → no message boundary: omp counts stream time begin→end.
    // Guards against re-introducing a duration threshold.
    vi.useFakeTimers()
    vi.setSystemTime(10000)
    const tr = new TokenRate()
    tr.add('streaming tokens', 10)
    vi.advanceTimersByTime(1500)
    tr.add('more streaming tokens', 10)
    expect(tr.streamDurationMs()).toBe(1500)
    vi.useRealTimers()
  })

  it('pause ages the silent tail into the kernel (omp end #advance)', () => {
    // The silent tail from the last delta to the message boundary is stream
    // time: pausing 300ms after the last delta pushes those 300ms of decay
    // and time integral into the sums before the clock stops. Monotone in
    // tail length; an immediate pause (dt=0) ages nothing.
    vi.useFakeTimers()
    const run = (tailMs: number) => {
      const tr = new TokenRate()
      for (let i = 0; i < 12; i++) {
        tr.add('chunk', 100)
        if (i < 11) vi.advanceTimersByTime(500)
      }
      vi.advanceTimersByTime(tailMs)
      tr.pause()
      vi.advanceTimersByTime(5000) // tool gap: never enters the kernel
      tr.add('chunk', 100)
      return tr.rate() // dt=0 at the resume delta
    }
    const immediate = run(0)
    const tailed = run(300)
    const longer = run(600)
    expect(immediate).not.toBe(null)
    expect(tailed).not.toBe(null)
    expect(longer).not.toBe(null)
    expect(tailed!).toBeLessThan(immediate!)
    expect(longer!).toBeLessThan(tailed!)
    vi.useRealTimers()
  })

  it('double pause without an intervening add is a no-op', () => {
    // The second pause must not age the sums through the tool gap (tool
    // time, not stream time) and must not close a second burst. Compared
    // bit-exactly: both runs leave the sums decayed to the same boundary
    // instant, so any drift means tool time leaked into the kernel.
    vi.useFakeTimers()
    const run = (double: boolean) => {
      const tr = new TokenRate()
      for (let i = 0; i < 12; i++) {
        tr.add('chunk', 100)
        if (i < 11) vi.advanceTimersByTime(500)
      }
      tr.pause()
      if (double) {
        vi.advanceTimersByTime(5000)
        tr.pause()
      }
      vi.advanceTimersByTime(5000)
      tr.add('chunk', 100)
      return [tr.rate(), tr.streamDurationMs()] as const
    }
    const [onceP, onceD] = run(false)
    const [twiceP, twiceD] = run(true)
    expect(onceP).not.toBe(null)
    expect(twiceP).toBe(onceP) // second pause ages nothing
    expect(twiceD).toBe(onceD) // burst closes once
    vi.useRealTimers()
  })

  it('cold pause before any add: the first add arms normally', () => {
    // Nothing to age (advancedTo zero), no burst to close; the pre-stream
    // wall time must never enter the kernel — bit-exact against a control
    // meter that never saw the cold pause.
    vi.useFakeTimers()
    vi.setSystemTime(10000)
    const r = new TokenRate()
    r.pause()
    expect(r.rate()).toBe(null)
    vi.advanceTimersByTime(5000) // pre-stream wall time
    const control = new TokenRate()
    for (let i = 0; i < 12; i++) {
      r.add('chunk', 100)
      control.add('chunk', 100)
      if (i < 11) vi.advanceTimersByTime(500)
    }
    expect(r.rate()).toBe(control.rate())
    vi.useRealTimers()
  })
})
