package tui

import (
	"testing"
	"testing/synctest"
	"time"
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
			r.Add("six token chunk") // 6 tokens each = 30 total
			time.Sleep(60 * time.Millisecond)
		}
		rate := r.Rate()
		// 30 tokens over ~300ms = ~100 t/s, not 30/2=15.
		if rate < 50.0 {
			t.Errorf("Rate() = %.1f, want >= 50 (30 tokens over 300ms)", rate)
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
