// Kernel-weighted streaming token rate: Σtokens/Σtime over two decayed-sum
// half-life scales (5s/20s) with a 0.3s prior padded into the denominator —
// port of pkg/tui/rate.go's decayed core. Token counts arrive on the wire
// (engine-computed); this file never estimates locally. Also carries a
// burst-aware cumulative stream duration (query-end average) and a local
// token accumulator for the live ↓output display, zeroed at every usage
// event where the authoritative output count takes over.

const HALF_LIVES_MS = [5000, 20000]
/** Pseudo-time padded into rate()'s denominator (additive smoothing): the
 * reading starts conservative and converges monotonically to the true rate
 * as real evidence dilutes the pad. There is no evidence gate (user
 * decision): the kernel self-corrects a TTFT dump (a 300-token 5ms burst
 * reads ~600/0.31 ≈ 1.9k t/s instead of ~60k and slides to the true rate
 * within a second or two), and a dead stream's reading decays to zero on
 * its own because the denominator keeps advancing. In ms to match the
 * kernel's time unit. */
const METER_PRIOR_MS = 300
const LN2 = Math.LN2

/** Exponentially decayed token and time sums on one half-life. `time` is the
 * exact integral of the kernel over streamed time, so `tokens / time` is a
 * kernel-weighted rate and `time` doubles as the effective window. */
class DecayedSums {
  tokens = 0
  time = 0
}

export class TokenRate {
  private sums: DecayedSums[] = HALF_LIVES_MS.map(() => new DecayedSums())
  /** Wall time (epoch ms) the sums were last advanced to. */
  private advancedTo = 0
  /** Clock stopped at a message boundary (tool_start); see pause(). */
  private paused = false
  /** Wire tokens since the last usage event; reconcileLocal zeroes them. */
  private local = 0
  private totalStreamingNs = 0
  private curBurstStart = 0
  private lastSampleTs = 0

  add(text: string, tokens: number): void {
    if (!text || tokens <= 0) return
    const now = Date.now()
    if (this.paused) {
      // First delta of the next message: re-arm the clock here so the tool
      // gap is skipped entirely (omp: "Tool execution between messages
      // neither ages the sums nor counts as time").
      this.paused = false
      this.advancedTo = now
    } else if (this.advancedTo === 0) {
      this.advancedTo = now
    } else {
      // Decay the kernel up to now and accumulate the time integral,
      // which only grows with new evidence.
      this.age(now)
    }
    for (const s of this.sums) s.tokens += tokens
    this.local += tokens

    // Bursts are delimited by pause (message boundaries), not by a sample
    // gap: a mid-message thinking stall is message span (omp runs stream
    // time begin→end), so it stays inside one burst.
    if (this.curBurstStart === 0) this.curBurstStart = now
    this.lastSampleTs = now
  }

  /** Decay the kernel sums up to now and accumulate the time integral (omp
   * #advance(nowMs)). No-op when the clock is not armed yet or now is not
   * past it. */
  private age(now: number): void {
    if (this.advancedTo === 0) return
    const dtMs = now - this.advancedTo
    if (dtMs <= 0) return
    for (let k = 0; k < this.sums.length; k++) {
      const f = 2 ** (-dtMs / HALF_LIVES_MS[k])
      this.sums[k].tokens *= f
      this.sums[k].time = this.sums[k].time * f + (HALF_LIVES_MS[k] / LN2) * (1 - f)
    }
    this.advancedTo = now
  }

  /** Stop the clock at a message boundary (gbot tool_start; omp end). Like
   * omp end()'s #advance(nowMs), the sums are first aged to the boundary:
   * the silent tail from the last delta to the message end is stream time,
   * so it decays the tokens and lands in the time integral before the clock
   * stops. A pause while already paused ages nothing — that interval is
   * tool execution, not stream time. The open burst closes at the pause
   * time — a message's span runs from its first delta to the boundary,
   * silent tail included. */
  pause(): void {
    const now = Date.now()
    if (!this.paused) this.age(now)
    if (this.curBurstStart !== 0) {
      this.totalStreamingNs += (now - this.curBurstStart) * 1e6
      this.curBurstStart = 0
    }
    this.paused = true
  }

  /** Tokens per second over the decayed window, or null while the clock is
   * paused for tool execution
   * (the display falls back to 0.0 t/s). Pure read: the decay to now is
   * previewed, never written back. Approved divergence from omp: omp
   * freezes its last reading across the gap (dt=0 between messages); gbot
   * returns null — 0.0 t/s is more honest than a stale frozen value. */
  rate(): number | null {
    if (this.paused) return null
    // Between reset and the first delta of the query (TTFT): the seeded sums
    // are last query's recording, and the bar belongs to this query — null
    // until the first delta arms the clock.
    if (this.advancedTo === 0) return null
    const dtMs = Date.now() - this.advancedTo
    let tokens = 0
    let time = 0
    for (let k = 0; k < this.sums.length; k++) {
      const f = 2 ** (-dtMs / HALF_LIVES_MS[k])
      const integral = (HALF_LIVES_MS[k] / LN2) * (1 - f)
      tokens += this.sums[k].tokens * f
      time += this.sums[k].time * f + integral
    }
    return (tokens * 1000) / (time + METER_PRIOR_MS)
  }

  /** Wire tokens accumulated since the last usage event. */
  localTokens(): number {
    return this.local
  }

  /** Zero the local accumulator (the usage event's authoritative output
   * count now includes those tokens). Never touches the rate sums. */
  reconcileLocal(): void {
    this.local = 0
  }

  streamDurationMs(): number {
    let totalMs = this.totalStreamingNs / 1e6
    if (this.curBurstStart !== 0) {
      totalMs += this.lastSampleTs - this.curBurstStart
    }
    return totalMs > 0 ? totalMs : 0
  }

  /** Start a new query. The rate sums are kept on purpose: like omp's
   * seed(), the previous query's evidence carries over ("even short turns
   * read immediately... the reading holds until the next message blends
   * with it"), so the first deltas of the new query blend with a live
   * reading. omp seeds
   * scaled output/duration; keeping the sums is the equivalent — and
   * simpler — implementation. Per-query accounting (local ledger, stream
   * duration, burst state, pause flag) still clears, and advancedTo zeroes
   * so no cross-query wall time ages the sums before the first delta
   * re-arms the clock. */
  reset(): void {
    this.advancedTo = 0
    this.paused = false
    this.local = 0
    this.totalStreamingNs = 0
    this.curBurstStart = 0
    this.lastSampleTs = 0
  }

  /** Wipe entirely, sums included. Session boundary use only (new session):
   * reset() seeds the next query of the SAME session from the surviving
   * sums, but another session's rate is another world's recording. */
  clear(): void {
    this.reset()
    for (const s of this.sums) {
      s.tokens = 0
      s.time = 0
    }
  }
}
