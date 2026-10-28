package tui

import (
	"math"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/utils"
)

// nowFn is rate.go's clock seam: tests drive exact sub-millisecond arrival
// patterns without sleeping; production reads the wall clock. Tests that
// override it must not run in parallel (they write this package variable).
var nowFn = time.Now

// rateHalfLives are the half-lives of the decayed-sum scales. Summing the
// scales gives a kernel Σ 2^(-age/τ): the 20s tail lends inertia against
// bursts (a turn-end backlog shifts the reading in proportion to its token
// mass instead of replacing it), the 5s tail keeps a sustained rate change
// visible within a couple of seconds. gbot streams run seconds to tens of
// seconds, so a minute-scale third scale (the omp reference carries one)
// would only add state without display value.
var rateHalfLives = [2]time.Duration{5 * time.Second, 20 * time.Second}

// meterPriorSec is pseudo-time padded into Rate()'s denominator (additive
// smoothing): the reading starts conservative and converges monotonically to
// the true rate as real evidence dilutes the pad. There is no evidence gate
// (user decision): the kernel self-corrects a TTFT dump (a 300-token 5ms
// burst reads ~600/0.31 ≈ 1.9k t/s instead of ~60k and slides to the true
// rate within a second or two), and a dead stream's reading decays to zero
// on its own because the denominator keeps advancing.
const meterPriorSec = 0.3

// decayedSums holds exponentially decayed token and time sums on one
// half-life. time is the exact integral of the kernel over streamed time,
// so tokens/time is a kernel-weighted rate and time doubles as the
// effective window length.
type decayedSums struct {
	tokens   float64
	time     float64 // seconds
	halfLife time.Duration
}

// TokenRate tracks streaming token arrivals as a decayed-sum kernel for the
// real-time t/s display, plus a burst-aware cumulative stream duration for
// the query-end average. All streaming data (text, thinking, tool params)
// is fed via Add. The meter's clock only runs inside a message: Pause
// (tool_start, gbot's message boundary — omp's end) stops it so tool
// execution neither ages the sums nor counts as stream time.
type TokenRate struct {
	mu   sync.Mutex
	sums [len(rateHalfLives)]decayedSums

	// advancedTo is the wall time the sums were last decayed to.
	advancedTo time.Time

	// paused marks the between-messages clock stop. The first Add after a
	// Pause re-arms the clock at its own timestamp (omp begin(): the tool
	// gap is skipped, not decayed through).
	paused bool

	// Cumulative tracking for query-end average.
	// totalStreamingNs sums the wall-clock duration of each burst
	// (Pause-time - curBurstStart per burst; the open burst runs to its
	// last sample).
	totalStreamingNs int64
	curBurstStart    time.Time
	lastSampleTs     time.Time
}

func NewTokenRate() *TokenRate {
	r := &TokenRate{}
	for k, hl := range rateHalfLives {
		r.sums[k].halfLife = hl
	}
	return r
}

// Clear wipes the meter entirely, sums included. Both boundary spellings land
// here: the session boundary (/clear, new session) and — via Reset — every
// query boundary. omp's seed() kept the sums so the next query's first deltas
// blended with a live reading; dropped deliberately (user decision): the
// reading belongs to the query that is producing it — carried-over evidence
// is a stale recording of a different stream state (context grown, cache
// cold), and its 1-2s of opening smoothness is not worth a reading that
// starts describing the previous query. The 0.3s prior keeps the cold
// restart conservative enough that the climb is barely visible.
func (r *TokenRate) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, hl := range rateHalfLives {
		r.sums[k] = decayedSums{halfLife: hl}
	}
	r.advancedTo = time.Time{}
	r.paused = false
	r.totalStreamingNs = 0
	r.curBurstStart = time.Time{}
	r.lastSampleTs = time.Time{}
}

// Add records tokens received at the current time. tokens is the engine's
// calibrated estimate carried on the wire; when absent (tests, error paths)
// it falls back to the seed-model estimate of text — same estimator family,
// just unfitted.
func (r *TokenRate) Add(text string, tokens int) {
	if r == nil || text == "" {
		return
	}
	if tokens <= 0 {
		tokens = utils.EstimateTokens(text)
	}
	if tokens == 0 {
		return
	}
	now := nowFn()
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.paused {
		// First delta of the next message: re-arm the clock here so the
		// tool gap is skipped entirely (omp: "Tool execution between
		// messages neither ages the sums nor counts as time").
		r.paused = false
		r.advancedTo = now
	} else if r.advancedTo.IsZero() {
		r.advancedTo = now
	} else {
		// Decay the kernel up to now and accumulate the time integral,
		// which only grows on an Add, i.e. with new evidence.
		r.ageLocked(now)
	}
	for k := range r.sums {
		r.sums[k].tokens += float64(tokens)
	}

	// Bursts are delimited by Pause (message boundaries), not by a sample
	// gap: a mid-message thinking stall is message span (omp runs stream
	// time begin→end), so it stays inside one burst.
	if r.curBurstStart.IsZero() {
		r.curBurstStart = now
	}
	r.lastSampleTs = now
}

// ageLocked decays the kernel sums up to now and accumulates the time
// integral (omp #advance(nowMs)). No-op when the clock has not been armed
// (advancedTo zero) or now is not past it. Caller holds r.mu.
//
// Mutate-exemption note (make mutate): boundary mutants of the dt<=0 guard
// (dt<0, dt<=-1) are semantically equivalent — the only dt they treat
// differently is 0, and aging by dt=0 is bit-identical to returning
// (Exp2(-0)=1.0 exactly, integral (1-f)=0 exactly, advancedTo assigned its
// own value), so no test can separate them from the original.
func (r *TokenRate) ageLocked(now time.Time) {
	if r.advancedTo.IsZero() {
		return
	}
	dt := now.Sub(r.advancedTo)
	if dt <= 0 {
		return
	}
	for k := range r.sums {
		f := math.Exp2(-float64(dt) / float64(r.sums[k].halfLife))
		r.sums[k].tokens *= f
		// halfLife converts to seconds before the integral: the time
		// field is seconds, the Durations are nanoseconds.
		r.sums[k].time = r.sums[k].time*f + r.sums[k].halfLife.Seconds()/math.Ln2*(1-f)
	}
	r.advancedTo = now
}

// Pause stops the meter's clock at a message boundary (tool_start). Like
// omp end()'s #advance(nowMs), the sums are first aged to the boundary: the
// silent tail from the last delta to the message end is stream time, so it
// decays the tokens and lands in the time integral before the clock stops.
// A Pause while already paused ages nothing — that interval is tool
// execution, not stream time. The open burst closes at the pause time — a
// message's span runs from its first delta to the boundary, silent tail
// included.
func (r *TokenRate) Pause() {
	if r == nil {
		return
	}
	now := nowFn()
	r.mu.Lock()
	// Mutate-exemption note (make mutate): an unlock-after-return mutant of
	// this defer is unobservable in the deterministic single-goroutine
	// tests, but the lock is real in production — the update loop, the tick
	// cadence and (in the wui twin) WS event batches race here; the
	// hammer test only catches it under -race.
	defer r.mu.Unlock()
	if !r.paused {
		r.ageLocked(now)
	}
	if !r.curBurstStart.IsZero() {
		r.totalStreamingNs += now.Sub(r.curBurstStart).Nanoseconds()
		r.curBurstStart = time.Time{}
	}
	r.paused = true
}

// Rate returns the kernel-weighted tokens/second — Σtokens/Σtime across the
// decayed scales — or ok=false while the evidence gate is closed or the
// clock is paused for tool execution (the TUI omits the segment, wui shows
// 0.0 t/s). It is a pure read: the decay to now is previewed, never written
// back, so reading at a high display cadence cannot distort the sums.
// Approved divergence from omp: omp freezes its last reading across the gap
// (rate() computes with dt=0 while between messages); gbot returns no
// reading — 0.0 t/s is more honest than a stale frozen value.
func (r *TokenRate) Rate() (float64, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return 0, false
	}
	// Between Reset and the first delta of the query (TTFT): the meter holds
	// no evidence (Reset wiped it), and the bar belongs to this query — no
	// reading until the first delta arms the clock.
	if r.advancedTo.IsZero() {
		return 0, false
	}
	dt := nowFn().Sub(r.advancedTo)
	var totalTokens, totalTime float64
	for k := range r.sums {
		f := math.Exp2(-float64(dt) / float64(r.sums[k].halfLife))
		integral := r.sums[k].halfLife.Seconds() / math.Ln2 * (1 - f)
		totalTokens += r.sums[k].tokens * f
		totalTime += r.sums[k].time*f + integral
	}
	return totalTokens / (totalTime + meterPriorSec), true
}

// Reset starts a new query on a fresh meter: it delegates to Clear — after
// the seed semantics died (see Clear), a query boundary and a session
// boundary wipe the same, so every query is metered independently.
func (r *TokenRate) Reset() {
	r.Clear()
}

// StreamDuration returns the total wall-clock time spent streaming,
// excluding tool execution between messages (Pause-delimited bursts).
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
