package wui

import (
	"math"
	"testing"
)

// --- fixtures ------------------------------------------------------------
//
// All tones are pinned to 1000Hz: 16000/1000 = 16 samples per cycle, and
// every block size in this file is a multiple of 16 samples, so any window
// holds an integer number of periods and the ideal discrete RMS is exactly
// A/√2 (Σsin² over a full period is constant, phase-independent). Only
// quantization noise remains (≤8.6e-6 across all amplitudes, below the
// half-sample 1.53e-5) — the exact-value assertions in TestFrameRMS and the
// 0.27% ConstantTone kill margin both depend on this. Quantization is
// round-half-away like the frontend floatTo16Le (×32767). Truncation instead
// of rounding would drop the gap RMS to 0.007053 and flip the ×30 threshold
// below the syllable level.

const (
	epdFrameSamples = 512
	epdToneHz       = 1000
)

func silencePCM(samples int) []byte {
	return make([]byte, samples*2)
}

func tonePCM(amp float64, samples int) []byte {
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := int(math.Round(amp * 32767 * math.Sin(2*math.Pi*epdToneHz*float64(i)/16000)))
		out[2*i] = byte(uint16(int16(v)) & 0xFF)
		out[2*i+1] = byte(uint16(int16(v)) >> 8)
	}
	return out
}

// speechPCM renders a speech-like envelope, frame-aligned: 6 syllable frames
// (3072 samples @0.3) alternating with 3 gap frames (1536 samples @0.01),
// truncated to samples. Callers pass lengths that are non-multiples of the
// 4608-sample period (e.g. 64 frames = 32768 = 7 periods + 512) so the tail
// always lands on a syllable frame — entering the trailing silence,
// silentSec is 0 and frame numbers are unambiguous.
func speechPCM(samples int) []byte {
	syllable := tonePCM(0.3, 3072)
	gap := tonePCM(0.01, 1536)
	var out []byte
	for len(out) < samples*2 {
		out = append(out, syllable...)
		if len(out) >= samples*2 {
			break
		}
		out = append(out, gap...)
	}
	return out[:samples*2]
}

func framesOf(pcm []byte) [][]byte {
	var frames [][]byte
	for i := 0; i+epdFrameSamples*2 <= len(pcm); i += epdFrameSamples * 2 {
		frames = append(frames, pcm[i:i+epdFrameSamples*2])
	}
	return frames
}

// observeFrames feeds frames sequentially and returns the 1-based index of
// the first frame where observe reported stop (0 = never).
func observeFrames(e *endpointer, frames [][]byte) int {
	for i, f := range frames {
		if e.observe(f) {
			return i + 1
		}
	}
	return 0
}

func TestFrameRMS(t *testing.T) {
	t.Parallel()
	if got := frameRMS(silencePCM(epdFrameSamples)); got != 0 {
		t.Errorf("silence RMS = %g, want 0", got)
	}
	// 1e-6 tolerance (not ±half-sample): a /32768 mutant rescales RMS by
	// 32767/32768 — 6.5e-6 on the 0.3 side, inside the half-sample band but
	// outside 1e-6. The 0.01 side only shifts 2.2e-7, so 0.3 carries the kill.
	if got, want := frameRMS(tonePCM(0.3, epdFrameSamples)), 0.212134; math.Abs(got-want) > 1e-6 {
		t.Errorf("tone(0.3) RMS = %.9f, want %.6f ± 1e-6", got, want)
	}
	if got, want := frameRMS(tonePCM(0.01, epdFrameSamples)), 0.007076; math.Abs(got-want) > 1e-6 {
		t.Errorf("tone(0.01) RMS = %.9f, want %.6f ± 1e-6", got, want)
	}
	// Odd trailing byte: 512 samples of tone + 1 stray byte — no panic, same
	// value as the aligned frame.
	odd := append(tonePCM(0.3, epdFrameSamples), 0x00)
	if got, want := frameRMS(odd), frameRMS(tonePCM(0.3, epdFrameSamples)); got != want {
		t.Errorf("odd-length RMS = %.9f, want %.9f", got, want)
	}
}

// Trailing-silence stop at exactly frame 63 (2.016s ≥ 2.0); frame 62
// (1.984s) must still be false. Leading silence (0.512s) and speech (gap
// runs ≤ 3 frames = 0.096s) never accumulate 2s.
func TestEndpointer_TrailingSilenceStops(t *testing.T) {
	t.Parallel()
	var e endpointer
	all := append(append(silencePCM(16*epdFrameSamples), speechPCM(64*epdFrameSamples)...), silencePCM(96*epdFrameSamples)...)
	frames := framesOf(all)
	for i, f := range frames {
		stop := e.observe(f)
		// 176 frames total; trailing silence starts at frame 81 (index 80).
		if i < 80 && stop {
			t.Fatalf("observe stopped early at frame %d (want no stop before trailing silence)", i+1)
		}
		if i >= 80 && i < 80+62 && stop {
			t.Fatalf("observe stopped at trailing-silence frame %d (1-based from silence start: %d), want only ≥63", i+1, i+1-80)
		}
		if i == 80+62 && !stop {
			t.Fatal("observe did not stop at trailing-silence frame 63 (2.016s)")
		}
		if i == 80+61 && stop {
			t.Fatal("observe stopped at trailing-silence frame 62 (1.984s < 2.0)")
		}
	}
}

// Mis-tap 2s of pure silence auto-closes (no arming semantics).
func TestEndpointer_LeadingSilenceStops(t *testing.T) {
	t.Parallel()
	var e endpointer
	if got := observeFrames(&e, framesOf(silencePCM(96*epdFrameSamples))); got != 63 {
		t.Errorf("stop frame = %d, want 63", got)
	}
}

func TestEndpointer_ContinuousSpeechNoStop(t *testing.T) {
	t.Parallel()
	var e endpointer
	if stopAt := observeFrames(&e, framesOf(speechPCM(320*epdFrameSamples))); stopAt != 0 {
		t.Fatalf("continuous speech stopped at frame %d, want never (gap runs are 3 frames = 0.096s)", stopAt)
	}
	if got := observeFrames(&e, framesOf(silencePCM(78*epdFrameSamples))); got != 63 {
		t.Errorf("trailing silence stop frame = %d, want 63", got)
	}
}

// 60s insurance on exact sample count: 50 × 1.2s segments, stop lands on
// frame 50 (cumulative 960,000 samples = 60.0s), never on frame 49 (58.8s).
// Alternating silence/tone keeps every silent run at 1.2s < 2s so only the
// insurance rule can fire.
func TestEndpointer_MaxDurationInsurance(t *testing.T) {
	t.Parallel()
	var e endpointer
	for i := 0; i < 25; i++ {
		if e.observe(silencePCM(19200)) {
			t.Fatalf("silence segment %d stopped (cumulative %.1fs < 60)", i+1, float64((2*i+1)*19200)/16000)
		}
		if got := e.observe(tonePCM(0.3, 19200)); got != (i == 24) {
			t.Fatalf("tone segment %d stop = %v (cumulative %.1fs), want stop only on segment 50", i+1, got, float64((2*i+2)*19200)/16000)
		}
	}
	if e.totalSamples != 960000 {
		t.Errorf("totalSamples = %d, want 960000", e.totalSamples)
	}
}

// Constant tone = noise class (floor = the signal itself, signal < 3×itself).
// Also pins the ×3 factor's lower bound via the drift margin documented in
// the fixture comment.
func TestEndpointer_ConstantToneIsSilence(t *testing.T) {
	t.Parallel()
	var e endpointer
	if got := observeFrames(&e, framesOf(tonePCM(0.3, 96*epdFrameSamples))); got != 63 {
		t.Errorf("constant tone stop frame = %d, want 63", got)
	}
}

// ×3 upper bound: a 0.02 tone (rms 0.0141) sits between the speech gap floor
// (0.0071) and 3×floor (0.0213) — it must count as silence. A factor-1
// mutant puts the threshold below the tone and never stops.
func TestEndpointer_FactorPinsThreshold(t *testing.T) {
	t.Parallel()
	var e endpointer
	if stopAt := observeFrames(&e, framesOf(speechPCM(64*epdFrameSamples))); stopAt != 0 {
		t.Fatalf("speech preamble stopped at frame %d, want never", stopAt)
	}
	if got := observeFrames(&e, framesOf(tonePCM(0.02, 96*epdFrameSamples))); got != 63 {
		t.Errorf("between-floor-and-3x tone stop frame = %d, want 63", got)
	}
}

// Digital silence: floor clamped at ε, 0 < 3e-3 accumulates. A deleted clamp
// (floor=0, threshold=0, strict < makes 0<0 false) never stops — this test
// is what keeps the clamp observable.
func TestEndpointer_DigitalSilence(t *testing.T) {
	t.Parallel()
	var e endpointer
	if got := observeFrames(&e, framesOf(silencePCM(96*epdFrameSamples))); got != 63 {
		t.Errorf("digital silence stop frame = %d, want 63", got)
	}
}

// Immediate loud onset self-heals: the onset sets floor = onset energy, the
// first syllable/gap pair pulls it down; peak silent run is 0.800s < 2s.
func TestEndpointer_LoudOnsetRecovers(t *testing.T) {
	t.Parallel()
	var e endpointer
	if stopAt := observeFrames(&e, framesOf(tonePCM(0.5, 16*epdFrameSamples))); stopAt != 0 {
		t.Fatalf("onset stopped at frame %d, want never", stopAt)
	}
	if got := observeFrames(&e, framesOf(speechPCM(64*epdFrameSamples))); got != 0 {
		t.Fatalf("speech after onset stopped at frame %d, want never (peak run 0.800s)", got)
	}
	if got := observeFrames(&e, framesOf(silencePCM(78*epdFrameSamples))); got != 63 {
		t.Errorf("trailing silence stop frame = %d, want 63", got)
	}
}

// Ambient noise rising >3× above floor: no stop while the drifted floor is
// still below the noise, stop once drift catches up. Time-interval
// assertion: no stop within 8s of noise start, stop within 12s.
func TestEndpointer_RisingNoiseDrift(t *testing.T) {
	t.Parallel()
	var e endpointer
	if stopAt := observeFrames(&e, framesOf(speechPCM(64*epdFrameSamples))); stopAt != 0 {
		t.Fatalf("speech preamble stopped at frame %d, want never", stopAt)
	}
	stopAt := 0
	for i := 1; i <= 30; i++ {
		if e.observe(tonePCM(0.05, 16000)) {
			stopAt = i
			break
		}
	}
	if stopAt == 0 {
		t.Fatal("rising noise never stopped within 30s — floor drift broken")
	}
	if stopAt <= 8 {
		t.Errorf("stopped at noise second %d, want > 8s (noise sits 1.66× above the initial 3×floor threshold)", stopAt)
	}
	if stopAt > 12 {
		t.Errorf("stopped at noise second %d, want <= 12s (drift 6.3%%/s catches up in ~10s)", stopAt)
	}
}

// Same audio, three chunkings: 32ms frames, ≤1s blocks that never cross a
// segment boundary, and one big block.
func TestEndpointer_FrameSizeAgnostic(t *testing.T) {
	t.Parallel()
	audio := append(append(silencePCM(16*epdFrameSamples), speechPCM(64*epdFrameSamples)...), silencePCM(96*epdFrameSamples)...)
	short := audio[:len(audio)-50*epdFrameSamples*2] // trailing silence cut to 46 frames (1.472s)

	t.Run("stop on all chunkings", func(t *testing.T) {
		t.Parallel()
		var a endpointer
		if got := observeFrames(&a, framesOf(audio)); got != 143 { // 80 speech part + 63
			t.Errorf("32ms frames stop = %d, want 143 (trailing-silence frame 63)", got)
		}
		var b endpointer
		blocks := [][]byte{
			silencePCM(8192),
			speechPCM(16000), speechPCM(16000), speechPCM(768),
			silencePCM(16000), silencePCM(16000), // 2.0s cumulative at block 6
			silencePCM(16000), silencePCM(1152),
		}
		stop := 0
		for i, blk := range blocks {
			if b.observe(blk) {
				stop = i + 1
				break
			}
		}
		if stop != 6 {
			t.Errorf("1s blocks stop = block %d, want 6 (trailing silence cumulative 2.0s)", stop)
		}
		var c endpointer
		if !c.observe(audio) {
			t.Error("single big block should stop (first frame sets floor=block RMS, rms<3×rms, 5.632s ≥ 2s)")
		}
	})
	t.Run("no stop when trailing silence is 1.472s", func(t *testing.T) {
		t.Parallel()
		var a endpointer
		if got := observeFrames(&a, framesOf(short)); got != 0 {
			t.Errorf("32ms frames stopped at %d, want never (1.472s < 2.0s)", got)
		}
		var b endpointer
		blocks := [][]byte{
			silencePCM(8192),
			speechPCM(16000), speechPCM(16000), speechPCM(768),
			silencePCM(16000), silencePCM(7552), // 1.472s cumulative
		}
		for i, blk := range blocks {
			if b.observe(blk) {
				t.Fatalf("1s blocks stopped at block %d, want never (1.472s < 2.0s)", i+1)
			}
		}
	})
	t.Run("single short block never stops", func(t *testing.T) {
		t.Parallel()
		var c endpointer
		if c.observe(speechPCM(32 * epdFrameSamples)) {
			t.Error("1.024s single block stopped, want never")
		}
	})
}

// Exact-boundary pins for the 2.0s silence window in SAMPLE units: one
// 32,000-sample silence frame is dt = 2.0s exactly (stops, `>=` semantics);
// 31,999 samples stays under. Together they nail epdSampleRate == 16000 to
// the single sample.
func TestEndpointer_TwoSecondBoundary(t *testing.T) {
	t.Parallel()
	var e endpointer
	if !e.observe(silencePCM(32000)) {
		t.Error("32,000 samples of silence (2.000s) must stop")
	}
	var f endpointer
	if f.observe(silencePCM(31999)) {
		t.Error("31,999 samples of silence (1.99994s) must not stop")
	}
}

// frameRMS degenerate inputs: empty/odd-tail windows are defined, not NaN.
func TestFrameRMS_EmptyAndTiny(t *testing.T) {
	t.Parallel()
	if got := frameRMS(nil); got != 0 {
		t.Errorf("frameRMS(nil) = %v, want 0", got)
	}
	if got := frameRMS([]byte{0x01}); got != 0 {
		t.Errorf("frameRMS(one byte) = %v, want 0 (odd tail ignored)", got)
	}
	// Two-sample window exercises the n==0 guard's false side with real
	// values: sample 0 is sin(0)=0, sample 1 is sin(2π/16).
	a := float64(int(math.Round(0.3*32767*0))) / 32767
	b := float64(int(math.Round(0.3*32767*math.Sin(2*math.Pi/16)))) / 32767
	want := math.Sqrt((a*a + b*b) / 2)
	if got := frameRMS(tonePCM(0.3, 2)); math.Abs(got-want) > 1e-12 {
		t.Errorf("frameRMS(2 samples) = %.9f, want %.9f", got, want)
	}
}
