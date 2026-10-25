package wui

import "math"

// Energy-threshold endpointer for the STT bridge (D5). Pure and frame-driven
// — no wall clock — so tests can feed frames at any speed.

const (
	epdSampleRate       = 16000
	epdSilenceFactor    = 3.0   // threshold = floor × 3
	epdSilenceMinSec    = 2.0   // consecutive below-threshold seconds to stop
	epdMaxStreamSec     = 60.0  // forgot-to-stop insurance, sample-count driven
	epdFloorDriftPerSec = 0.063 // floor may rise ~6.3%/s (e^0.063≈1.065)
	epdFloorMin         = 1e-3  // floor lower bound (−60dBFS)
)

// frameRMS returns the linear RMS of an s16le frame on a [-1,1] scale,
// normalizing each sample by 32767 — the exact inverse of the frontend's
// floatTo16Le encoding (×32767), not the conventional /32768. An odd
// trailing byte is ignored.
func frameRMS(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(uint16(pcm[i])|uint16(pcm[i+1])<<8)) / 32767
		sum += v * v
	}
	return math.Sqrt(sum / float64(n))
}

// endpointer tracks per-utterance endpointing state. Single-goroutine use
// (the bridge readLoop); no locks.
type endpointer struct {
	floor        float64
	floorInit    bool
	silentSec    float64
	totalSamples int
}

// observe processes one PCM frame of any length and reports whether the
// utterance should be finalized (2s silence or 60s insurance).
func (e *endpointer) observe(pcm []byte) (stop bool) {
	n := len(pcm) / 2
	dt := float64(n) / epdSampleRate
	rms := frameRMS(pcm)
	e.totalSamples += n

	// Floor update first — the threshold for THIS frame already reflects it.
	// Any frame can drop the floor instantly (real silence becomes the new
	// floor); it may only rise at 6.3%/s so speech never inflates it.
	if !e.floorInit {
		e.floor = max(rms, epdFloorMin)
		e.floorInit = true
	} else {
		drifted := e.floor * (1 + epdFloorDriftPerSec*dt)
		e.floor = max(min(drifted, rms), epdFloorMin)
	}

	threshold := epdSilenceFactor * e.floor
	// Strict less-than: equality counts as non-silent. This is what makes a
	// deleted ε clamp observable (floor=0 → 0<0 false → never accumulates).
	if rms < threshold {
		e.silentSec += dt
	} else {
		e.silentSec = 0
	}

	return float64(e.totalSamples) >= epdMaxStreamSec*epdSampleRate || e.silentSec >= epdSilenceMinSec
}
