package tui

import (
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/utils"
)

// minRateSpan is the shortest observation interval Rate() will divide by.
// Backlogged deltas (tool-call or turn end) land several samples apart within
// 1-5ms; tokens over that span is noise with unbounded variance, not a rate.
// 500ms equals the display's refresh period (app.go samples Rate() every
// 500ms), so anything shorter can never be rendered stably anyway; Rate()
// reports 0 until real spread accumulates.
const minRateSpan = 500 * time.Millisecond

// nowFn is rate.go's clock seam: tests drive exact sub-millisecond arrival
// patterns without sleeping; production reads the wall clock. Tests that
// override it must not run in parallel (they write this package variable).
var nowFn = time.Now

// TokenRate tracks streaming token arrivals in a sliding window for real-time
// t/s display. All streaming data (text, thinking, tool params) is fed via Add.
type TokenRate struct {
	mu      sync.Mutex
	samples []rateSample
	window  time.Duration

	// Cumulative tracking for query-end average.
	// totalStreamingNs sums the wall-clock duration of each burst
	// (lastSampleTs - curBurstStart per burst).
	totalStreamingNs int64
	curBurstStart    time.Time
	lastSampleTs     time.Time
}

type rateSample struct {
	ts     time.Time
	tokens int
}

func NewTokenRate() *TokenRate {
	return &TokenRate{window: 2 * time.Second}
}

// Add records tokens received at the current time.
func (r *TokenRate) Add(text string) {
	if r == nil || text == "" {
		return
	}
	tokens := utils.EstimateTokens(text)
	if tokens == 0 {
		return
	}
	now := nowFn()
	r.mu.Lock()
	defer r.mu.Unlock()

	r.samples = append(r.samples, rateSample{ts: now, tokens: tokens})
	r.evict()

	// Burst detection: gap > 2s means new burst (tool exec, thinking pause).
	// Close previous burst by accumulating its wall-clock duration.
	if !r.curBurstStart.IsZero() && now.Sub(r.lastSampleTs) > 2*time.Second {
		r.totalStreamingNs += r.lastSampleTs.Sub(r.curBurstStart).Nanoseconds()
		r.curBurstStart = now
	} else if r.curBurstStart.IsZero() {
		r.curBurstStart = now
	}
	r.lastSampleTs = now
}

// Rate returns tokens/second over the samples' own span (last - first).
// Same-instant samples AND any span below minRateSpan return 0: there is no
// usable observation interval (burst jitter packs deltas into the same
// millisecond; backlog flushes land several samples a few ms apart, and
// tokens over such spans displayed thousands of t/s). A later sample that
// opens the span restores the value.
func (r *TokenRate) Rate() float64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evict()
	if len(r.samples) == 0 {
		return 0
	}
	total := 0
	for _, s := range r.samples {
		total += s.tokens
	}
	elapsed := r.samples[len(r.samples)-1].ts.Sub(r.samples[0].ts)
	if elapsed < minRateSpan {
		return 0
	}
	return float64(total) / elapsed.Seconds()
}

// Reset clears all state (call at query start).
func (r *TokenRate) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = nil
	r.totalStreamingNs = 0
	r.curBurstStart = time.Time{}
	r.lastSampleTs = time.Time{}
}

// StreamDuration returns the total wall-clock time spent streaming,
// excluding gaps between bursts (tool execution, thinking pauses).
func (r *TokenRate) StreamDuration() time.Duration {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	totalNs := r.totalStreamingNs
	if !r.curBurstStart.IsZero() {
		totalNs += r.lastSampleTs.Sub(r.curBurstStart).Nanoseconds()
	}
	if totalNs <= 0 {
		return 0
	}
	return time.Duration(totalNs)
}

// evict removes expired samples and compacts the backing array to prevent
// unbounded memory growth over long streaming sessions.
func (r *TokenRate) evict() {
	cutoff := nowFn().Add(-r.window)
	i := 0
	for i < len(r.samples) && r.samples[i].ts.Before(cutoff) {
		i++
	}
	if i > 0 {
		n := copy(r.samples, r.samples[i:])
		r.samples = r.samples[:n]
	}
	// Compact if we've drained more than half the capacity.
	if cap(r.samples) > 32 && len(r.samples) < cap(r.samples)/4 {
		r.samples = append([]rateSample(nil), r.samples...)
	}
}
