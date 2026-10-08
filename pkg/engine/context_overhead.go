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
	e.contextOverheadSet = true
	e.calibMu.Unlock()
}

// seedContextOverhead fills the unlearned overhead from a transcript's usage
// anchor, so the first compact after a restart (before any response has
// landed in this engine instance) still bills the request-shape overhead in
// AfterTokens. anchored = base + est(tail-after-anchor) and pure =
// est(head-including-anchor) + est(tail); the tail cancels, so
// anchored − pure = base − est(head) — the same residual recordContextOverhead
// learns at response time, computed on the exact message range the anchor
// bills. Only fills the unset state: a learned value (or a previous seed)
// always wins, and a non-positive residual (estimator overshoot) leaves the
// state unset. Takes calibMu only, never e.mu — callers pass a message
// snapshot, preserving the global e.mu → calibMu order. Must not be called
// while holding e.mu (calibratedEstimator re-acquires calibMu).
func (e *Engine) seedContextOverhead(messages []types.Message) {
	e.calibMu.Lock()
	set := e.contextOverheadSet
	e.calibMu.Unlock()
	if set {
		return
	}
	est := e.calibratedEstimator()
	anchored, ok := lastAnchoredTokenCount(messages, defaultMessageEnvelopeTokens, est)
	if !ok {
		return
	}
	pure := estimateMessagesTokensWith(messages, defaultMessageEnvelopeTokens, est)
	seed := anchored - pure
	if seed <= 0 {
		return
	}
	e.calibMu.Lock()
	if !e.contextOverheadSet {
		e.contextOverhead = seed
		e.contextOverheadSet = true
	}
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
