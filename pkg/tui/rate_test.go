package tui

import (
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestTokenRate_NilReceiver(t *testing.T) {
	t.Parallel()
	var r *TokenRate
	if got, ok := r.Rate(); ok || got != 0.0 {
		t.Errorf("nil Rate() = (%v, %v), want (0.0, false)", got, ok)
	}
	wantZero := time.Duration(0)
	if got := r.StreamDuration(); got != wantZero {
		t.Errorf("nil StreamDuration() = %v, want 0", got)
	}
	r.Add("hello", 1)
	r.Pause()
	r.Reset()
}

func TestTokenRate_PauseStopsClockUntilNextDelta(t *testing.T) {
	// omp begin()/end(): "Tool execution between messages neither ages the
	// sums nor counts as time". tool_start (Pause) is gbot's message
	// boundary. While paused there is no reading (TUI omits the segment,
	// wui shows 0.0 t/s); the first delta of the next message re-arms the
	// clock at its own timestamp, so the tool gap never enters the kernel.
	synctest.Test(t, func(t *testing.T) {
		paused := NewTokenRate()
		for range 12 {
			paused.Add("chunk", 100)
			time.Sleep(500 * time.Millisecond)
		}
		paused.Pause()
		if got, ok := paused.Rate(); ok || got != 0.0 {
			t.Fatalf("Rate() while paused = (%v, %v), want (0.0, false) (clock stopped)", got, ok)
		}
		time.Sleep(5 * time.Second) // tool execution
		paused.Add("chunk", 100)    // first delta of the next message resumes
		p, okP := paused.Rate()     // read at the resume instant (dt=0)

		// Control: identical wall-clock sequence without the Pause — the 5s
		// gap ages the sums and lands in the time denominator (~105 t/s vs
		// ~233 t/s on a 200 t/s stream, well past the 1.5x margin).
		control := NewTokenRate()
		for range 12 {
			control.Add("chunk", 100)
			time.Sleep(500 * time.Millisecond)
		}
		time.Sleep(5 * time.Second)
		control.Add("chunk", 100)

		c, okC := control.Rate()
		if !okP {
			t.Fatal("Rate() after resume = not ok, want ok (evidence carried across the gap)")
		}
		if !okC {
			t.Fatal("Rate() control = not ok, want ok")
		}
		if p <= 1.5*c {
			t.Errorf("resumed reading = %.1f, control = %.1f; the tool gap must not age the sums (want > 1.5x control)", p, c)
		}
	})
}

func TestTokenRate_PauseAgesSilentTail(t *testing.T) {
	// omp end()'s #advance(nowMs): the silent tail from the last delta to the
	// message boundary is stream time. Pausing 300ms after the last delta
	// must push those 300ms of kernel decay and time integral into the sums
	// before the clock stops — the resumed reading carries them — while an
	// immediate Pause (dt=0) ages nothing. Monotone in tail length: the
	// longer the tail, the lower the resumed reading (more decay, bigger
	// time denominator).
	synctest.Test(t, func(t *testing.T) {
		run := func(tail time.Duration) float64 {
			r := NewTokenRate()
			for i := range 12 {
				r.Add("chunk", 100)
				if i < 11 {
					time.Sleep(500 * time.Millisecond)
				}
			}
			time.Sleep(tail)
			r.Pause()
			time.Sleep(5 * time.Second) // tool gap: never enters the kernel
			r.Add("chunk", 100)
			p, ok := r.Rate() // dt=0 at the resume delta
			if !ok {
				t.Fatal("Rate() after resume = not ok, want ok")
			}
			return p
		}
		immediate := run(0)
		tailed := run(300 * time.Millisecond)
		longer := run(600 * time.Millisecond)
		if tailed >= immediate {
			t.Errorf("resumed rate after 300ms tail = %.9f, want < %.9f (tail decayed tokens and entered the time integral)", tailed, immediate)
		}
		if longer >= tailed {
			t.Errorf("resumed rate after 600ms tail = %.9f, want < %.9f (tail length monotonically lowers the reading)", longer, tailed)
		}
	})
}

func TestTokenRate_DoublePauseWithoutAddIsNoOp(t *testing.T) {
	// A second Pause with no Add in between must not age the sums through
	// the tool gap — that interval is tool time, not stream time — and must
	// not close a second burst. The kernels are compared bit-exactly: both
	// runs leave the sums decayed to the same boundary instant, so any
	// drift means the second Pause leaked tool time into the kernel.
	synctest.Test(t, func(t *testing.T) {
		run := func(double bool) (float64, time.Duration) {
			r := NewTokenRate()
			for i := range 12 {
				r.Add("chunk", 100)
				if i < 11 {
					time.Sleep(500 * time.Millisecond)
				}
			}
			r.Pause()
			if double {
				time.Sleep(5 * time.Second)
				r.Pause()
			}
			time.Sleep(5 * time.Second)
			r.Add("chunk", 100)
			p, ok := r.Rate() // dt=0 at the resume delta
			if !ok {
				t.Fatal("Rate() after resume = not ok, want ok")
			}
			return p, r.StreamDuration()
		}
		onceP, onceD := run(false)
		twiceP, twiceD := run(true)
		if twiceP != onceP {
			t.Errorf("rate after double Pause = %.9f, want exactly %.9f (second Pause ages nothing)", twiceP, onceP)
		}
		if twiceD != onceD {
			t.Errorf("StreamDuration after double Pause = %v, want %v (burst closes once)", twiceD, onceD)
		}
	})
}

func TestTokenRate_ColdPauseBeforeAnyAdd(t *testing.T) {
	// Pause before the meter has ever run: nothing to age (advancedTo
	// zero), no burst to close. The first Add afterwards must arm the clock
	// normally and the pre-stream wall time must never enter the kernel —
	// bit-exact against a control meter that never saw the cold Pause.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.Pause()
		if got, ok := r.Rate(); ok || got != 0.0 {
			t.Errorf("Rate() after cold Pause = (%v, %v), want (0.0, false)", got, ok)
		}
		time.Sleep(5 * time.Second) // pre-stream wall time
		control := NewTokenRate()
		for i := range 12 {
			r.Add("chunk", 100)
			control.Add("chunk", 100)
			if i < 11 {
				time.Sleep(500 * time.Millisecond)
			}
		}
		rP, okP := r.Rate()
		cP, okC := control.Rate()
		if !okP {
			t.Fatal("Rate() after cold Pause stream = not ok, want ok (meter armed by first Add)")
		}
		if !okC {
			t.Fatal("Rate() control = not ok, want ok")
		}
		if rP != cP {
			t.Errorf("Rate() after cold Pause = %.9f, want exactly %.9f (pre-stream pause is invisible)", rP, cP)
		}
	})
}

func TestTokenRate_ConcurrentHammerSurvives(t *testing.T) {
	// The meter is shared across the TUI update loop and the display tick
	// cadence; this exercises the mutex discipline, not the math. Under
	// -race (make check runs with it) interleaved Add/Rate/Pause from
	// several goroutines must not deadlock or panic. The reading itself is
	// inherently nondeterministic, but the final state is not: every
	// goroutine's last call is Pause, so the meter must end paused with
	// the exact no-reading tuple.
	r := NewTokenRate()
	const goroutines = 8
	const rounds = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range rounds {
				r.Add("chunk", 10)
				r.Rate()
				r.Pause()
			}
		}()
	}
	wg.Wait()
	if got, ok := r.Rate(); ok || got != 0.0 {
		t.Errorf("Rate() after hammer = (%v, %v), want (0.0, false) (every goroutine's last call is Pause)", got, ok)
	}
}

func TestTokenRate_EmptyAdd(t *testing.T) {
	t.Parallel()
	fresh := NewTokenRate()
	if got, ok := fresh.Rate(); ok || got != 0.0 {
		t.Errorf("Rate() of a fresh meter = (%v, %v), want (0, false) (no delta has armed the clock yet)", got, ok)
	}
	r := NewTokenRate()
	r.Add("", 5)
	r.Add("   ", 0) // whitespace-only text estimates to 0 tokens: no-op
	if got, ok := r.Rate(); ok || got != 0.0 {
		t.Errorf("Rate() after no-op Add = (%v, %v), want (0, false) (no delta has armed the clock)", got, ok)
	}
	wantZero := time.Duration(0)
	if got := r.StreamDuration(); got != wantZero {
		t.Errorf("StreamDuration() after no-op Add = %v, want 0", got)
	}
}

func TestTokenRate_Rate_ReadsImmediatelyFromFirstDeltas(t *testing.T) {
	// A ~200 t/s stream (50 tokens every 250ms) reads from the very first
	// delta: with no streamed time yet the reading is exactly Σtokens/prior
	// (100/0.3 — prior-suppressed, not span-inflated), then descends
	// monotonically toward the true rate as real streamed time dilutes the
	// pad and the young token mass stops dominating.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		prev := math.Inf(1)
		for i := 1; i <= 24; i++ {
			r.Add("chunk", 50)
			rate, ok := r.Rate() // dt=0 read right at the delta
			if !ok {
				t.Fatalf("Rate() after delta %d = not ok, want ok (no gate: any evidence reads)", i)
			}
			if i == 1 {
				if want := 100.0 / 0.3; math.IsNaN(rate) || math.Abs(rate-want) > 1e-9 {
					t.Errorf("Rate() after the first delta = %.12f, want %.12f (Σtokens over the 0.3s prior alone)", rate, want)
				}
			}
			if rate > prev+1e-9 {
				t.Errorf("Rate() rebounded on delta %d: %.4f after %.4f (descending convergence to the true rate)", i, rate, prev)
			}
			prev = rate
			time.Sleep(250 * time.Millisecond)
		}
		if prev < 170 || prev > 210 {
			t.Errorf("Rate() after 24 chunks over 6s = %.1f, want 170-210 (200 t/s stream)", prev)
		}
	})
}

func TestTokenRate_Rate_PriorConvergenceCurve(t *testing.T) {
	// tqdm-style responsiveness: a uniform 500 t/s stream reads from the
	// first deltas, starts below the true rate (the 0.3s pseudo-time in the
	// denominator dilutes early evidence), rises
	// monotonically as real streamed time dilutes the pad, and is within 30%
	// of the truth by 2s (~0.89x at 0.3s, ~0.97x at 2s).
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		const trueRate = 500.0
		var readings []float64
		for i := 0; i <= 20; i++ {
			r.Add("chunk", 50) // 50 tokens every 100ms = 500 t/s
			if i == 2 || i == 3 || i == 10 || i == 20 {
				rate, ok := r.Rate() // dt=0 read right at the delta
				if !ok {
					t.Fatalf("Rate() at t=%.1fs = not ok, want ok (>= 100 tokens streamed)", float64(i)/10)
				}
				readings = append(readings, rate)
			}
			if i < 20 {
				time.Sleep(100 * time.Millisecond)
			}
		}
		early, mid, late, conv := readings[0], readings[1], readings[2], readings[3]
		if early > mid+1e-9 || mid > late+1e-9 || late > conv+1e-9 {
			t.Errorf("readings not monotone non-decreasing: %.3f %.3f %.3f %.3f", early, mid, late, conv)
		}
		if early >= trueRate {
			t.Errorf("reading at 0.2s = %.1f, want < %.0f (prior dilutes early evidence)", early, trueRate)
		}
		if conv < 0.7*trueRate {
			t.Errorf("reading at 2s = %.1f, want >= %.1f (0.7 x true rate)", conv, 0.7*trueRate)
		}
	})
}

func TestTokenRate_Rate_SingleDeltaReadsImmediately(t *testing.T) {
	// The first reading is immediate: a single delta at dt=0 has zero
	// streamed time on every scale, so the reading is exactly
	// Σtokens/(0+prior) — 1 token reads 2/0.3 ≈ 6.7, 100 tokens read
	// 200/0.3 ≈ 666.7 — pinning both the two-scale token sum and the pad.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.Add("chunk", 1)
		rate, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() after a single 1-token delta = not ok, want ok (no gate)")
		}
		if want := 2.0 / 0.3; math.IsNaN(rate) || math.Abs(rate-want) > 1e-9 {
			t.Errorf("Rate() = %.12f, want %.12f (2 tokens over the 0.3s prior alone)", rate, want)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.Add("chunk", 100)
		rate, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() after a single 100-token delta = not ok, want ok (no gate)")
		}
		if want := 200.0 / 0.3; math.IsNaN(rate) || math.Abs(rate-want) > 1e-9 {
			t.Errorf("Rate() = %.12f, want %.12f (200 tokens over the 0.3s prior alone)", rate, want)
		}
	})
}

func TestTokenRate_Rate_DeadStreamSlidesTowardZero(t *testing.T) {
	// With no gate a dead stream (no Pause, the stream just stops) must
	// reach zero on its own: the denominator keeps advancing with wall time
	// while the token mass decays. From the single-100-token-delta
	// construction the reading slides monotonically 666.7 → ~6.8 (10s) →
	// ~1.4 (40s) → 0.38 t/s at 60s, where both decay exponents are integer
	// powers of two (2^-12, 2^-3) so the exact value is assertable.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.Add("chunk", 100)
		first, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() at the delta = not ok, want ok")
		}
		if want := 200.0 / 0.3; math.Abs(first-want) > 1e-9 {
			t.Errorf("Rate() at the delta = %.12f, want %.12f", first, want)
		}
		prev := first
		for _, d := range []time.Duration{10 * time.Second, 30 * time.Second, 20 * time.Second} {
			time.Sleep(d)
			got, ok := r.Rate()
			if !ok {
				t.Fatal("Rate() on a dead stream = not ok, want ok (no gate: the reading decays, it never vanishes)")
			}
			if got > prev+1e-9 {
				t.Errorf("Rate() rebounded: %.9f after %.9f", got, prev)
			}
			if math.IsNaN(got) || got <= 0.0 {
				t.Errorf("Rate() = %.9f, want > 0 (positive for finite dt)", got)
			}
			prev = got
		}
		f5 := math.Exp2(-12.0) // 60s / 5s half-life
		f20 := math.Exp2(-3.0) // 60s / 20s half-life
		want := 100 * (f5 + f20) / (5/math.Ln2*(1-f5) + 20/math.Ln2*(1-f20) + 0.3)
		if math.IsNaN(prev) || math.Abs(prev-want) > 1e-9 {
			t.Errorf("Rate() 60s after the stop = %.12f, want %.12f (exact kernel value)", prev, want)
		}
		if prev >= 2.0 {
			t.Errorf("Rate() 60s after the stop = %.4f, want < 2 (decays toward zero)", prev)
		}
	})
}

func TestTokenRate_Rate_TailBacklogBoundedAndMonotonicSlide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		// Sustained ~100 t/s stream over 10s.
		for range 10 {
			r.Add("chunk", 100)
			time.Sleep(1 * time.Second)
		}
		r0, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() after sustained stream = not ok, want ok")
		}
		if r0 < 60 || r0 > 160 {
			t.Fatalf("R0 = %.1f, want 60-160 (100 t/s stream)", r0)
		}
		// Turn-end backlog: 3 deltas within ~6ms. An unbounded span-based
		// implementation would read >10000 t/s here; the kernel bounds the
		// burst's effect by its token mass.
		for range 3 {
			r.Add("backlog", 100)
			time.Sleep(2 * time.Millisecond)
		}
		after, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() after backlog = not ok, want ok")
		}
		if after >= 2*r0 {
			t.Errorf("Rate() after backlog = %.1f, want < 2*%.1f (bounded burst)", after, r0)
		}
		// Stream stopped: with no gate the endpoint of the slide is zero,
		// not a vanished reading — the denominator keeps advancing while the
		// token mass decays, so the reading slides down monotonically (no
		// rebound spikes, no abrupt drop) forever. 140s after the stop the
		// construction gives 0.25 t/s; anything at or above 5 means the
		// slide stalled.
		prev := after
		for range 14 {
			time.Sleep(5 * time.Second)
			got, ok := r.Rate()
			if !ok {
				t.Fatal("Rate() on a dead stream = not ok, want ok (no gate: the reading decays, it never vanishes)")
			}
			if got > prev+1e-9 {
				t.Errorf("Rate() rebounded: %.9f after %.9f", got, prev)
			}
			if math.IsNaN(got) || got <= 0.0 {
				t.Errorf("Rate() = %.9f, want > 0 (positive for finite dt)", got)
			}
			prev = got
		}
		// ~140s after the stop the reading has slid from ~155 to well under
		// 5 t/s — toward zero, on its own.
		time.Sleep(70 * time.Second)
		final, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() 140s after stream stop = not ok, want ok (no gate: the reading never disappears)")
		}
		if math.IsNaN(final) || final <= 0 || final >= 5 {
			t.Errorf("Rate() 140s after stream stop = %.9f, want in (0, 5) (slides toward zero; construction gives 0.25)", final)
		}
	})
}

func TestTokenRate_Rate_WallTimeSlideAfterStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		for range 12 {
			r.Add("chunk", 100)
			time.Sleep(500 * time.Millisecond)
		}
		r1, ok := r.Rate()
		if !ok || r1 <= 0 {
			t.Fatalf("Rate() at stop = (%.1f, %v), want ok and > 0", r1, ok)
		}
		time.Sleep(5 * time.Second)
		r2, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() 5s after stop = not ok, want ok (reading slides, does not vanish)")
		}
		if r2 <= 0 || r2 >= r1 {
			t.Errorf("Rate() 5s after stop = %.1f, want in (0, %.1f) — sliding, not frozen, not zeroed", r2, r1)
		}
	})
}

func TestTokenRate_StreamDuration_SingleBurst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.Add("tokens here", 0)
		time.Sleep(500 * time.Millisecond)
		r.Add("more tokens here", 0)
		dur := r.StreamDuration()
		// Single burst ~500ms
		if dur < 400*time.Millisecond || dur > 600*time.Millisecond {
			t.Errorf("StreamDuration() = %v, want ~500ms", dur)
		}
	})
}

func TestTokenRate_StreamDuration_PauseSeparatesMessages(t *testing.T) {
	// Bursts are delimited by Pause (tool_start), not a duration threshold:
	// a burst closes at the pause time — message span runs from first delta
	// to the boundary, silent tail included (omp spans run begin→end) — and
	// any tool gap, long or short, is excluded. Under the old 2s threshold
	// this same sequence read 2000ms: the tail was dropped and the 1.5s
	// short tool merged into the burst.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		// Message 1: deltas at t=0 and t=100ms, 300ms silent tail.
		r.Add("message one tokens", 0)
		time.Sleep(100 * time.Millisecond)
		r.Add("message one more", 0)
		time.Sleep(300 * time.Millisecond)
		r.Pause() // t=400ms: burst 1 = 400ms
		time.Sleep(5 * time.Second)
		// Message 2: 200ms of deltas, 100ms tail.
		r.Add("message two tokens", 0)
		time.Sleep(200 * time.Millisecond)
		r.Add("message two more", 0)
		time.Sleep(100 * time.Millisecond)
		r.Pause() // t=5700ms: burst 2 = 300ms
		time.Sleep(1500 * time.Millisecond)
		// Message 3: 100ms of deltas, burst still open.
		r.Add("message three tokens", 0)
		time.Sleep(100 * time.Millisecond)
		r.Add("message three more", 0)
		dur := r.StreamDuration()
		// 400 + 300 + 100 = 800ms; the 5s and 1.5s tool gaps count nothing.
		if dur < 750*time.Millisecond || dur > 850*time.Millisecond {
			t.Errorf("StreamDuration() = %v, want ~800ms (three messages only, tool gaps excluded)", dur)
		}
	})
}

func TestTokenRate_StreamDuration_ThinkingPauseCountsAsStreaming(t *testing.T) {
	// A mid-message thinking stall (no tool_start) is message span, not a
	// gap: omp counts stream time begin→end; only tool execution pauses the
	// clock. Guards against re-introducing a duration threshold.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		r.Add("streaming tokens", 10)
		time.Sleep(1500 * time.Millisecond)
		r.Add("more streaming tokens", 10)
		dur := r.StreamDuration()
		if dur < 1400*time.Millisecond || dur > 1600*time.Millisecond {
			t.Errorf("StreamDuration() = %v, want ~1500ms (thinking stall is stream time)", dur)
		}
	})
}

func TestTokenRate_ResetWipesMeterForNewQuery(t *testing.T) {
	// Reset is a full wipe (user decision, see Clear's doc): the omp seed —
	// the previous query's evidence carrying over — polluted the next
	// query's first seconds whenever the meter belonged to another engine,
	// so every query is metered independently. Per-query accounting still
	// clears: stream duration, burst state, and the pause flag.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		for i := range 12 {
			r.Add("chunk", 100)
			if i < 11 {
				time.Sleep(500 * time.Millisecond)
			}
		}
		held, ok := r.Rate() // dt=0 read right at the last delta
		if !ok || held <= 0 {
			t.Fatalf("held reading = (%v, %v), want a live one", held, ok)
		}
		r.Pause() // query ended on a tool boundary; Reset must clear the flag
		r.Reset()
		// TTFT: Reset disarms the clock, so the bar shows no reading until
		// this query's first delta.
		if _, ok := r.Rate(); ok {
			t.Fatal("Rate() between Reset and the first delta = ok, want false (TTFT blank)")
		}
		if r.totalStreamingNs != 0 {
			t.Errorf("totalStreamingNs after Reset = %d, want 0", r.totalStreamingNs)
		}
		if !r.curBurstStart.IsZero() || !r.lastSampleTs.IsZero() {
			t.Errorf("burst fields after Reset = (%v, %v), want zero times", r.curBurstStart, r.lastSampleTs)
		}
		if !r.advancedTo.IsZero() {
			t.Errorf("advancedTo after Reset = %v, want zero (no cross-query decay before the first delta)", r.advancedTo)
		}
		if r.paused {
			t.Error("paused after Reset = true, want false")
		}
		wantZero := time.Duration(0)
		if got := r.StreamDuration(); got != wantZero {
			t.Errorf("StreamDuration() after Reset = %v, want 0", got)
		}
		// The new query's first delta reads alone: 100 tokens landing on both
		// scales over the 0.3s prior. A surviving seed would hold the reading
		// near `held` (~200 t/s) instead of this fresh-delta value.
		r.Add("chunk", 100)
		fresh, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() after first delta of the new query = not ok, want ok")
		}
		if want := 200.0 / meterPriorSec; math.IsNaN(fresh) || math.Abs(fresh-want) > 1e-9 {
			t.Errorf("Rate() after first delta = %.9f, want %.9f (fresh delta alone over the prior, no seed)", fresh, want)
		}
	})
}

func TestTokenRate_ClearWipesSumsAtSessionBoundary(t *testing.T) {
	// /clear starts another session: another session's recording must not
	// blend into this one — Clear wipes the sums so the meter reads fresh.
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		for range 12 {
			r.Add("chunk", 100)
			time.Sleep(500 * time.Millisecond)
		}
		held, ok := r.Rate()
		if !ok || held <= 0 {
			t.Fatalf("held reading = (%v, %v), want a live one", held, ok)
		}
		r.Clear()
		if _, ok := r.Rate(); ok {
			t.Error("Rate() after Clear = ok, want false (fresh meter)")
		}
		r.Add("chunk", 10)
		fresh, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() after Clear+Add = not ok, want ok")
		}
		if want := 20.0 / (meterPriorSec); fresh > want*1.05 {
			t.Errorf("Rate() after Clear+Add = %.9f, want ≤ %.9f (10-token delta alone over the prior — no old-session blend)", fresh, want*1.05)
		}
	})
}

func TestTokenRate_ResetWipesSums(t *testing.T) {
	// A weak previous turn (60 tokens over 0.75s) reads ~67 t/s; after Reset
	// the first delta reads alone: 1 token on both scales over the 0.3s
	// prior. The exact value pins that none of the old evidence survived —
	// the seed-era reading sat inside (held, held*1.05].
	synctest.Test(t, func(t *testing.T) {
		r := NewTokenRate()
		for i := range 4 {
			r.Add("c", 15)
			if i < 3 {
				time.Sleep(250 * time.Millisecond)
			}
		}
		held, ok := r.Rate() // dt=0 read right at the last delta
		if !ok {
			t.Fatal("Rate() with weak evidence = not ok, want ok (no gate)")
		}
		r.Reset()
		if _, ok := r.Rate(); ok {
			t.Fatal("Rate() between Reset and the first delta = ok, want false (TTFT blank)")
		}
		r.Add("c", 1) // first delta arms the clock on an empty kernel
		fresh, ok := r.Rate()
		if !ok {
			t.Fatal("Rate() after the first delta = not ok, want ok")
		}
		if fresh > held*0.5 {
			t.Errorf("Rate() after first delta = %.12f, want ≤ %.12f — old evidence survived the Reset", fresh, held*0.5)
		}
		// Exact kernel value from the construction: 1 token on each scale,
		// zero time integral, prior in the shared denominator.
		if want := 2.0 / meterPriorSec; math.IsNaN(fresh) || math.Abs(fresh-want) > 1e-9 {
			t.Errorf("Rate() after first delta = %.12f, want %.12f (fresh delta alone over the prior)", fresh, want)
		}
	})
}

func TestTokenRate_AddUsesWireTokensOverEstimate(t *testing.T) {
	// Both Adds share one pinned instant: real wall time elapsing between
	// them would apply a legitimate decay factor and break the exact sum.
	orig := nowFn
	nowFn = func() time.Time { return time.Unix(0, 0) }
	t.Cleanup(func() { nowFn = orig })

	r := NewTokenRate()
	r.Add("word", 7)
	r.Add("x", 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if got := r.sums[1].tokens; got != 8.0 {
		t.Errorf("sums[1].tokens after Add(7)+Add(1) = %v, want 8.0 (wire counts used verbatim, 1 is a real count)", got)
	}
}

func TestTokenRate_BackwardsClockNeverAgesSums(t *testing.T) {
	// A clock regression (now earlier than advancedTo) must be a no-op on
	// the kernel: aging through a negative dt would make every decay
	// factor exceed 1 and inflate the token sums with a negative time
	// integral. Asserted exact — the inflation is large (dt=-500ms on the
	// 5s scale multiplies by 2^0.1).
	// REAL-TIME: read the wall clock once for the base timestamp.
	base := time.Now()
	cur := base
	orig := nowFn
	nowFn = func() time.Time { return cur }
	t.Cleanup(func() { nowFn = orig })

	r := NewTokenRate()
	r.Add("chunk", 150)
	cur = base.Add(-500 * time.Millisecond) // wall clock went backwards
	r.Add("chunk", 150)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.sums {
		if got := r.sums[k].tokens; got != 300.0 {
			t.Errorf("sums[%d].tokens after backwards clock = %v, want 300.0 (negative dt must not age)", k, got)
		}
		if got := r.sums[k].time; got != 0.0 {
			t.Errorf("sums[%d].time after backwards clock = %v, want 0.0 (no integral over a negative span)", k, got)
		}
	}
}

func TestTokenRate_OneNanosecondDeltaStillAgesKernel(t *testing.T) {
	// The no-op boundary of the aging guard is dt <= 0, not dt <= 1: a
	// 1ns delta must still age the kernel (dt/τ = 2e-10 on the 5s scale —
	// representable in float64, so the decay is directly assertable) and
	// advance the clock to its sample.
	// REAL-TIME: read the wall clock once for the base timestamp.
	base := time.Now()
	cur := base
	orig := nowFn
	nowFn = func() time.Time { return cur }
	t.Cleanup(func() { nowFn = orig })

	r := NewTokenRate()
	r.Add("chunk", 150)
	cur = base.Add(time.Nanosecond)
	r.Add("chunk", 150)
	r.mu.Lock()
	defer r.mu.Unlock()
	if got := r.advancedTo; !got.Equal(cur) {
		t.Errorf("advancedTo after 1ns delta = %v, want %v (clock advanced to the latest sample)", got, cur)
	}
	for k, hl := range rateHalfLives {
		f := math.Exp2(-float64(time.Nanosecond) / float64(hl))
		want := 150*f + 150
		if got := r.sums[k].tokens; got != want {
			t.Errorf("sums[%d].tokens after 1ns delta = %.17g, want %.17g (1ns of decay applied)", k, got, want)
		}
	}
}

func TestTokenRate_Rate_ExactKernelValue(t *testing.T) {
	// Pins the kernel's numeric output to the half-life constants [5s, 20s]
	// and ln2, recomputed inline from literals so any drift in rateHalfLives
	// or the integral math shows up as a mismatch. Phase two reads at dt=2s
	// so the preview integral carries a nonzero (1-f) factor — at dt=0 it is
	// identically zero and a wrong coefficient would hide. NaN is checked
	// explicitly: a zeroed half-life poisons every sum with NaN, and NaN
	// compares false against any tolerance, silently passing otherwise.
	// REAL-TIME: read the wall clock once for the base timestamp.
	base := time.Now()
	cur := base
	orig := nowFn
	nowFn = func() time.Time { return cur }
	t.Cleanup(func() { nowFn = orig })

	r := NewTokenRate()
	r.Add("chunk", 150)
	cur = base.Add(4400 * time.Millisecond)
	r.Add("chunk", 150)
	got, ok := r.Rate()
	if !ok {
		t.Fatal("Rate() after 2x150 over 4.4s = not ok, want ok")
	}
	const dtSec = 4.4
	f5 := math.Exp2(-dtSec / 5)
	f20 := math.Exp2(-dtSec / 20)
	wantTokens := (150*f5 + 150) + (150*f20 + 150)
	wantTime := (5 / math.Ln2 * (1 - f5)) + (20 / math.Ln2 * (1 - f20))
	// The 0.3s prior is literal, like the half-lives: a drift in meterPriorSec
	// must surface here, not cancel against the package constant.
	want := wantTokens / (wantTime + 0.3)
	if math.IsNaN(got) || math.Abs(got-want) > 1e-9 {
		t.Errorf("Rate() = %.12f, want %.12f (kernel value for τ=[5s,20s], prior 0.3)", got, want)
	}

	cur = base.Add(6400 * time.Millisecond)
	gotSlide, ok := r.Rate()
	if !ok {
		t.Fatal("Rate() 2s after the last Add = not ok, want ok")
	}
	const slideSec = 2.0
	f5s := math.Exp2(-slideSec / 5)
	f20s := math.Exp2(-slideSec / 20)
	wantSlideTokens := (150*f5+150)*f5s + (150*f20+150)*f20s
	wantSlideTime := (5/math.Ln2*(1-f5))*f5s + 5/math.Ln2*(1-f5s) +
		(20/math.Ln2*(1-f20))*f20s + 20/math.Ln2*(1-f20s)
	wantSlide := wantSlideTokens / (wantSlideTime + 0.3)
	if math.IsNaN(gotSlide) || math.Abs(gotSlide-wantSlide) > 1e-9 {
		t.Errorf("Rate() 2s after last Add = %.12f, want %.12f (previewed decay from literals)", gotSlide, wantSlide)
	}
}

func TestTokenRate_Rate_SingleDeltaDenominatorIsPreviewOnly(t *testing.T) {
	// A single delta never accumulates stored time (ageLocked only runs on a
	// later Add), so at dt>0 the whole denominator is Rate()'s previewed
	// kernel integral plus the prior — the reading exists with zero stored
	// stream time, at the exact previewed value from the construction.
	// REAL-TIME: read the wall clock once for the base timestamp.
	base := time.Now()
	cur := base
	orig := nowFn
	nowFn = func() time.Time { return cur }
	t.Cleanup(func() { nowFn = orig })

	r := NewTokenRate()
	r.Add("chunk", 240)
	cur = base.Add(4400 * time.Millisecond)
	rate, ok := r.Rate()
	if !ok {
		t.Fatal("Rate() 4.4s after a 240-token delta = not ok, want ok")
	}
	const dtSec = 4.4
	f5 := math.Exp2(-dtSec / 5)
	f20 := math.Exp2(-dtSec / 20)
	want := 240 * (f5 + f20) / (5/math.Ln2*(1-f5) + 20/math.Ln2*(1-f20) + 0.3)
	if math.IsNaN(rate) || math.Abs(rate-want) > 1e-9 {
		t.Errorf("Rate() = %.12f, want %.12f (previewed decay + prior, no stored time)", rate, want)
	}
}

func TestTokenRate_NowFnControlsAddAndRate(t *testing.T) {
	// Writes the package-level nowFn; must not run in parallel. The seam
	// pins both Add timestamps and Rate()'s wall-clock preview to fake times.
	// REAL-TIME: read the wall clock once for the base timestamp.
	base := time.Now()
	cur := base
	orig := nowFn
	nowFn = func() time.Time { return cur }
	t.Cleanup(func() { nowFn = orig })

	r := NewTokenRate()
	r.Add("chunk", 150)
	cur = base.Add(4400 * time.Millisecond)
	r.Add("chunk", 150)
	rate, ok := r.Rate()
	if !ok || rate <= 0 {
		t.Fatalf("Rate() under seam clock = (%.1f, %v), want ok and > 0", rate, ok)
	}

	// Advancing only the seam clock slides the reading down without any Add:
	// Rate() previews the wall-clock decay as a pure read.
	prev := rate
	cur = base.Add(12 * time.Second)
	rate2, ok2 := r.Rate()
	if !ok2 {
		t.Fatal("Rate() after seam advance = not ok, want ok (no gate: the reading persists)")
	}
	if rate2 >= prev {
		t.Errorf("Rate() after seam advance to +12s = %.1f, want < %.1f (decayed by wall time)", rate2, prev)
	}
}
