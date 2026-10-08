package engine

// context_overhead.go — response-time residual learning of the request-shape
// overhead (system prompt, tool schemas, skill listing, memory files) that
// real usage bills but message-content estimation cannot see (~20k measured).
// Consumption sites: AutoCompactor AfterTokens (post-compact ledger) and the
// fallback branch of the engine's token estimation, so both match the anchored
// (real-usage) basis.

import (
	"github.com/liuy/gbot/pkg/types"
)

// recordContextOverhead learns the request-shape overhead from the response
// that just entered history: real billed total (input incl. cache + output —
// the output becomes the next request's input, matching the anchor base
// convention in lastAnchoredTokenCount) minus the calibrated estimate of the
// same transcript. With no estimator bias this isolates the static overhead;
// residual bias is bounded by the calibrated estimator and refreshed every
// response. Mirrors recordCalibrationObservation's lock order (e.mu → calibMu).
func (e *Engine) recordContextOverhead(msg *types.Message) {
	if msg == nil || msg.Usage == nil {
		return
	}
	total := msg.Usage.TotalInputTokens() + msg.Usage.OutputTokens
	if total <= 0 {
		return
	}
	est := e.calibratedEstimator()
	e.mu.RLock()
	msgs := e.messages
	e.mu.RUnlock()
	pure := estimateMessagesTokensWith(msgs, defaultMessageEnvelopeTokens, est)
	d := max(total-pure,
		// Estimator overshoot: the content estimate already covers the bill.
		0)
	e.calibMu.Lock()
	e.contextOverhead = d
	e.calibMu.Unlock()
}

// ContextOverheadTokens returns the learned request-shape overhead, 0 before
// any response has been observed. Probed by NewAutoCompactor so post-compact
// accounting (AfterTokens) bills the same total the next request will.
func (e *Engine) ContextOverheadTokens() int {
	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	return e.contextOverhead
}
