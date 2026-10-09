package tui

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/utils"
)

func TestTokenRate_NilReceiver(t *testing.T) {
	t.Parallel()
	var r *TokenRate
	if got := r.Rate(); got != 0.0 {
		t.Errorf("nil Rate() = %v, want 0.0", got)
	}
	wantZero := time.Duration(0)
	if got := r.StreamDuration(); got != wantZero {
		t.Errorf("nil StreamDuration() = %v, want 0", got)
	}
	r.Add("hello")
	r.Reset()
}

func TestTokenRate_EmptyAdd(t *testing.T) {
	t.Parallel()
	r := NewTokenRate()
	r.Add("")
	r.Add("   ")
	if got := r.Rate(); got != 0.0 {
		t.Errorf("Rate() after empty Add = %v, want 0.0", got)
	}
	wantZero := time.Duration(0)
	if got := r.StreamDuration(); got != wantZero {
		t.Errorf("StreamDuration() after empty Add = %v, want 0", got)
	}
}

func TestTokenRate_Rate_UsesActualElapsed(t *testing.T) {
	// Rate() should reflect actual streaming time, not fixed window.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		for range 5 {
			r.Add("six estimated token chunk of words") // 6 tokens each = 30 total
			time.Sleep(150 * time.Millisecond)
		}
		rate := r.Rate()
		// 30 tokens over ~600ms = ~50 t/s, not 30/2=15.
		if rate < 40.0 || rate > 60.0 {
			t.Errorf("Rate() = %.1f, want 40-60 (30 tokens over 600ms)", rate)
		}
	})
}

func TestTokenRate_Rate_DecaysToZero(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.Add("hello world this is a test")
		time.Sleep(3 * time.Second) // past 2s window
		if got := r.Rate(); got != 0.0 {
			t.Errorf("Rate() after window expiry = %v, want 0.0", got)
		}
	})
}

func TestTokenRate_StreamDuration_SingleBurst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.Add("tokens here")
		time.Sleep(500 * time.Millisecond)
		r.Add("more tokens here")
		dur := r.StreamDuration()
		// Single burst ~500ms
		if dur < 400*time.Millisecond || dur > 600*time.Millisecond {
			t.Errorf("StreamDuration() = %v, want ~500ms", dur)
		}
	})
}

func TestTokenRate_StreamDuration_BurstGapExcluded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		// Burst 1: 100ms
		r.Add("burst one tokens")
		time.Sleep(100 * time.Millisecond)
		r.Add("burst one more")
		// Gap 3s (excluded)
		time.Sleep(3 * time.Second)
		// Burst 2: 200ms
		r.Add("burst two tokens")
		time.Sleep(200 * time.Millisecond)
		r.Add("burst two more")
		dur := r.StreamDuration()
		// ~300ms total streaming, not 3.3s
		if dur > 500*time.Millisecond {
			t.Errorf("StreamDuration() = %v, want <= 500ms (gap excluded)", dur)
		}
		if dur < 200*time.Millisecond {
			t.Errorf("StreamDuration() = %v, want >= 200ms (both bursts counted)", dur)
		}
	})
}

func TestTokenRate_Reset(t *testing.T) {
	t.Parallel()
	r := NewTokenRate()
	r.Add("hello world test")
	r.Add("another chunk")
	r.Reset()

	if got := r.Rate(); got != 0.0 {
		t.Errorf("Rate() after Reset = %v, want 0.0", got)
	}
	wantZero := time.Duration(0)
	if got := r.StreamDuration(); got != wantZero {
		t.Errorf("StreamDuration() after Reset = %v, want 0", got)
	}
	if r.totalStreamingNs != 0 {
		t.Errorf("totalStreamingNs after Reset = %d, want 0", r.totalStreamingNs)
	}
}

func TestTokenRate_EvictCompactsBackingArray(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		for range 100 {
			r.Add("sample token text here")
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(3 * time.Second)
		r.Rate() // triggers evict

		if liveCount := len(r.samples); liveCount != 0 {
			t.Errorf("live samples after expiry+evict = %d, want 0", liveCount)
		}
		if capAfterEvict := cap(r.samples); capAfterEvict > 32 {
			t.Errorf("cap after evict = %d, want <= 32 (should compact)", capAfterEvict)
		}
	})
}

func TestTokenRate_Rate_BurstPackedSamples(t *testing.T) {
	// Network jitter delivers several deltas in the same millisecond: the
	// samples' own span is 0. The old 1ms floor displayed thousands of t/s
	// (9 tokens / 1ms = 9000); a later sample restores a real value.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		now := time.Now()
		for range 3 {
			r.mu.Lock()
			r.samples = append(r.samples, rateSample{ts: now, tokens: 3})
			r.mu.Unlock()
		}
		if got := r.Rate(); got != 0.0 {
			t.Errorf("Rate() with same-ms samples = %v, want 0 (span not computable)", got)
		}
		// A delta 500ms later opens the span: 12 tokens / 500ms. Sleep first
		// so evict's clock moves with the samples.
		time.Sleep(500 * time.Millisecond)
		r.mu.Lock()
		r.samples = append(r.samples, rateSample{ts: now.Add(500 * time.Millisecond), tokens: 3})
		r.mu.Unlock()
		if got := r.Rate(); got != 24.0 {
			t.Errorf("Rate() after a later sample = %.1f, want 24.0 (12 tokens over 500ms)", got)
		}
	})
}

func TestTokenRate_Rate_SingleSample(t *testing.T) {
	// One sample has no span; the old floor turned it into tokens*1000.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.mu.Lock()
		r.samples = append(r.samples, rateSample{ts: time.Now(), tokens: 3})
		r.mu.Unlock()
		if got := r.Rate(); got != 0.0 {
			t.Errorf("Rate() with a single sample = %v, want 0 (no span)", got)
		}
	})
}

func TestTokenRate_Rate_BacklogFlushSpike(t *testing.T) {
	// Turn-end backlog delivers several deltas within a few ms; tokens over
	// that span is noise with unbounded variance, not a rate.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		base := time.Now()
		for _, s := range []rateSample{
			{ts: base, tokens: 50},
			{ts: base.Add(2 * time.Millisecond), tokens: 50},
			{ts: base.Add(5 * time.Millisecond), tokens: 50},
		} {
			r.mu.Lock()
			r.samples = append(r.samples, s)
			r.mu.Unlock()
		}
		if got := r.Rate(); got != 0.0 {
			t.Errorf("Rate() after 5ms backlog flush = %v, want 0 (span below minRateSpan — no estimate)", got)
		}
	})
}

func TestTokenRate_Rate_MinSpanBoundary(t *testing.T) {
	// Exactly minRateSpan is accepted (strict <): 24 tokens / 0.5s = 48.0.
	// 0.5s is binary-exact, no epsilon needed.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		base := time.Now()
		r.mu.Lock()
		r.samples = append(r.samples, rateSample{ts: base, tokens: 12})
		r.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
		r.mu.Lock()
		r.samples = append(r.samples, rateSample{ts: base.Add(minRateSpan), tokens: 12})
		r.mu.Unlock()
		if got := r.Rate(); got != 48.0 {
			t.Errorf("Rate() at exactly minRateSpan = %v, want 48.0 (24 tokens over 0.5s)", got)
		}
	})
	// One millisecond under the gate: still no estimate.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		base := time.Now()
		r.mu.Lock()
		r.samples = append(r.samples, rateSample{ts: base, tokens: 12})
		r.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
		r.mu.Lock()
		r.samples = append(r.samples, rateSample{ts: base.Add(minRateSpan - time.Millisecond), tokens: 12})
		r.mu.Unlock()
		if got := r.Rate(); got != 0.0 {
			t.Errorf("Rate() below minRateSpan = %v, want 0 (no usable observation interval)", got)
		}
	})
}

func TestTokenRate_Rate_ClusterThenSpreadRecovers(t *testing.T) {
	// A burst-start cluster delays, never corrupts, the estimate: once a
	// later sample opens the span to exactly 500ms, all 100 tokens count.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		base := time.Now()
		for _, s := range []rateSample{
			{ts: base, tokens: 30},
			{ts: base.Add(2 * time.Millisecond), tokens: 30},
			{ts: base.Add(5 * time.Millisecond), tokens: 30},
		} {
			r.mu.Lock()
			r.samples = append(r.samples, s)
			r.mu.Unlock()
		}
		time.Sleep(500 * time.Millisecond)
		r.mu.Lock()
		r.samples = append(r.samples, rateSample{ts: base.Add(500 * time.Millisecond), tokens: 10})
		r.mu.Unlock()
		if got := r.Rate(); got != 200.0 {
			t.Errorf("Rate() once spread reaches 500ms = %v, want 200.0 (100 tokens over 0.5s)", got)
		}
	})
}

func TestTokenRate_NowFnControlsAddAndEvict(t *testing.T) {
	// Writes the package-level nowFn; must not run in parallel. Every time
	// the estimator sees after this anchor comes from the seam below.
	// REAL-TIME: read the wall clock once for the base timestamp.
	base := time.Now()
	cur := base
	orig := nowFn
	nowFn = func() time.Time { return cur }
	t.Cleanup(func() { nowFn = orig })

	s1 := "a chunk of tokens"
	s2 := "another chunk of tokens"
	r := NewTokenRate()
	r.Add(s1)
	cur = base.Add(1 * time.Second)
	r.Add(s2)

	// Two samples exactly 1s apart (binary-exact span): the exact positive
	// rate must come back before any further clock advance.
	want := float64(utils.EstimateTokens(s1)+utils.EstimateTokens(s2)) / 1.0
	if got := r.Rate(); got != want {
		t.Fatalf("Rate() under seam clock = %v, want %v", got, want)
	}

	// Advance past the 2s window: Rate()'s own evict() must drop both
	// samples per the seam clock (cutoff = base+1100ms).
	cur = base.Add(3100 * time.Millisecond)
	if got := r.Rate(); got != 0.0 {
		t.Errorf("Rate() after nowFn advanced past the window = %v, want 0 (samples evicted per the seam clock)", got)
	}
}
