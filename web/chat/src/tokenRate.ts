// Port of pkg/tui/rate.go. Tracks streaming token arrivals in a sliding
// window for real-time t/s display, and a cumulative stream duration that
// excludes gaps (tool exec, thinking pauses) for query-end average.

const WINDOW_MS = 2000
const BURST_GAP_MS = 2000

interface Sample {
  ts: number
  tokens: number
}

function estimateTokens(text: string): number {
  // Rough estimate: 4 chars per token (matches types.EstimateTokens heuristic).
  return Math.ceil(text.length / 4)
}

export class TokenRate {
  private samples: Sample[] = []
  private totalStreamingNs = 0
  private curBurstStart = 0
  private lastSampleTs = 0

  add(text: string): void {
    if (!text) return
    const tokens = estimateTokens(text)
    if (tokens === 0) return
    const now = Date.now()
    this.samples.push({ ts: now, tokens })
    this.evict()

    if (this.curBurstStart !== 0 && now - this.lastSampleTs > BURST_GAP_MS) {
      this.totalStreamingNs += (this.lastSampleTs - this.curBurstStart) * 1e6
      this.curBurstStart = now
    } else if (this.curBurstStart === 0) {
      this.curBurstStart = now
    }
    this.lastSampleTs = now
  }

  rate(): number {
    this.evict()
    if (this.samples.length === 0) return 0
    let total = 0
    for (const s of this.samples) total += s.tokens
    let elapsedMs = this.samples[this.samples.length - 1].ts - this.samples[0].ts
    if (elapsedMs <= 0) elapsedMs = 1
    return total / (elapsedMs / 1000)
  }

  streamDurationMs(): number {
    let totalMs = this.totalStreamingNs / 1e6
    if (this.curBurstStart !== 0) {
      totalMs += this.lastSampleTs - this.curBurstStart
    }
    return totalMs > 0 ? totalMs : 0
  }

  reset(): void {
    this.samples = []
    this.totalStreamingNs = 0
    this.curBurstStart = 0
    this.lastSampleTs = 0
  }

  private evict(): void {
    const cutoff = Date.now() - WINDOW_MS
    let i = 0
    while (i < this.samples.length && this.samples[i].ts < cutoff) i++
    if (i > 0) this.samples = this.samples.slice(i)
  }
}
