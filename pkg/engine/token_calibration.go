package engine

// token_calibration.go implements dynamic token-estimation calibration.
//
// The 2-feature chars-per-token heuristic this system replaced measured
// 25-29% median error against real tokenizers, worst on code/JSON — which
// dominates agent context (tool output). The 6-feature linear model
// (utils.CountTokenFeatures, seed ratios in utils.SeedTokenRatios) starts
// from cross-model seed values and is refit online by least squares from
// real API usage deltas observed between consecutive turns.

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// Feature-vector layout for ratios: [CJK, Struct, Digit, Indent, Words, Other].
const (
	calibFeatCJK    = 0
	calibFeatStruct = 1
	calibFeatDigit  = 2
	calibFeatIndent = 3
	calibFeatWords  = 4
	calibFeatOther  = 5
)

// Seed ratios live in utils.SeedTokenRatios so display-side estimates
// (utils.EstimateTokens) and this calibration layer share one table.

// Post-fit clamps per feature. Least squares over a small sliding window of
// live traffic can produce wild coefficients when features are collinear
// (e.g. a session of pure JSON makes Struct and Other nearly proportional);
// clamping keeps every ratio inside the range the 2026-10-04 measurement
// campaign deemed physically plausible, so one noisy window cannot distort
// estimates arbitrarily.
var calibrationClamps = [6][2]float64{
	{0.3, 1.5},  // CJK
	{0.3, 3.0},  // Struct
	{0.2, 2.0},  // Digit
	{-1.0, 0.5}, // Indent
	{0.1, 1.2},  // Words
	{0.05, 0.5}, // Other
}

const (
	// calibrationWindow bounds the observation window so the fit tracks the
	// current traffic mix instead of averaging the whole session.
	calibrationWindow = 64
	// calibrationMinObs is the minimum observation count before a fitted
	// result is trusted over the seeds.
	calibrationMinObs = 3
)

// calibObservation is one differential data point: the features of the
// messages added between two consecutive requests and the real input-token
// growth the API reported for the same interval.
type calibObservation struct {
	f    utils.TokenFeatures
	real int
}

// tokenCalibration is the per-model calibration state. R holds the current
// ratios; until fitted (or with fewer than calibrationMinObs observations)
// the seed ratios are used instead.
type tokenCalibration struct {
	obs    []calibObservation
	R      [6]float64
	fitted bool

	// Anchors for the next differential observation: the last real
	// TotalInputTokens reported for this model key, and the index of the
	// assistant message carrying it (that reply's output tokens are part of
	// the next request's input, so the next feature window starts there).
	anchorTotal  int
	anchorMsgIdx int
	anchored     bool
}

// ratios returns the ratios currently in effect.
func (c *tokenCalibration) ratios() [6]float64 {
	if !c.fitted || len(c.obs) < calibrationMinObs {
		return utils.SeedTokenRatios
	}
	return c.R
}

// observe appends an observation and refits. A 6x6 solve costs microseconds,
// so refitting on every observation is cheaper than tracking staleness.
func (c *tokenCalibration) observe(f utils.TokenFeatures, real int) {
	c.obs = append(c.obs, calibObservation{f: f, real: real})
	if len(c.obs) > calibrationWindow {
		c.obs = c.obs[len(c.obs)-calibrationWindow:]
	}
	if len(c.obs) >= calibrationMinObs {
		c.refit()
	}
}

// refit solves the normal equations A·R = b (A = Σ fᵢfᵢᵀ, b = Σ fᵢ·realᵢ)
// with plain Gaussian elimination — no external dependencies. On a singular
// system (fewer independent observations than features, routine at 3-5
// samples) it keeps the previous ratios rather than inventing a solution.
func (c *tokenCalibration) refit() {
	var aug [6][7]float64
	for _, o := range c.obs {
		fv := featureVector(o.f)
		for j := range fv {
			for k := range fv {
				aug[j][k] += fv[j] * fv[k]
			}
			aug[j][6] += fv[j] * float64(o.real)
		}
	}
	x, ok := solveLinear6(aug)
	if !ok {
		return
	}
	for j := range x {
		x[j] = min(max(x[j], calibrationClamps[j][0]), calibrationClamps[j][1])
	}
	c.R = x
	c.fitted = true
}

// featureVector flattens TokenFeatures into ratio order.
func featureVector(f utils.TokenFeatures) [6]float64 {
	return [6]float64{
		float64(f.CJK), float64(f.Struct), float64(f.Digit),
		float64(f.Indent), float64(f.Words), float64(f.Other),
	}
}

// solveLinear6 solves a 6x6 augmented system [A|b] via Gaussian elimination
// with partial pivoting. The singularity threshold is scaled to the matrix
// magnitude because normal-equation entries scale with the square of the
// feature counts (millions for large contexts).
func solveLinear6(a [6][7]float64) ([6]float64, bool) {
	const n = 6
	maxAbs := 0.0
	for row := range a {
		for col := range a[row] {
			maxAbs = max(maxAbs, math.Abs(a[row][col]))
		}
	}
	if maxAbs == 0 {
		return [6]float64{}, false
	}
	eps := maxAbs * 1e-12

	for col := range n {
		pivot := col
		for r := col + 1; r < n; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[pivot][col]) {
				pivot = r
			}
		}
		if math.Abs(a[pivot][col]) < eps {
			return [6]float64{}, false
		}
		a[col], a[pivot] = a[pivot], a[col]
		for r := col + 1; r < n; r++ {
			factor := a[r][col] / a[col][col]
			if factor == 0 {
				continue
			}
			for k := col; k <= n; k++ {
				a[r][k] -= factor * a[col][k]
			}
		}
	}

	var x [6]float64
	for row := n - 1; row >= 0; row-- {
		sum := a[row][n]
		for k := row + 1; k < n; k++ {
			sum -= a[row][k] * x[k]
		}
		x[row] = sum / a[row][row]
	}
	return x, true
}

// estimateWithFeatures applies the linear model. The sum can go negative
// (Indent's ratio may be negative), so the result is floored at 0.
func estimateWithFeatures(f utils.TokenFeatures, R [6]float64) int {
	v := float64(f.CJK)*R[calibFeatCJK] +
		float64(f.Struct)*R[calibFeatStruct] +
		float64(f.Digit)*R[calibFeatDigit] +
		float64(f.Indent)*R[calibFeatIndent] +
		float64(f.Words)*R[calibFeatWords] +
		float64(f.Other)*R[calibFeatOther]
	return max(int(v), 0)
}

// ---------------------------------------------------------------------------
// Engine integration
// ---------------------------------------------------------------------------

// calibrationKey builds the per-model map key. A nil provider (bare test
// engines) degrades to "/model" rather than panicking.
func calibrationKey(p llm.Provider, model string) string {
	name := ""
	if p != nil {
		name = p.Name()
	}
	return name + "/" + model
}

// modelKey snapshots the current calibration key. Callers must not already
// hold e.mu: Go's RWMutex can deadlock on recursive RLock once a writer
// queues between the two acquisitions.
func (e *Engine) modelKey() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return calibrationKey(e.provider, e.model)
}

// ratiosFor returns the ratios in effect for key, registering the seed
// calibration on first use. Lazy map init keeps bare &Engine{} (tests)
// working without New().
func (e *Engine) ratiosFor(key string) [6]float64 {
	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	if e.calibrations == nil {
		e.calibrations = make(map[string]*tokenCalibration)
	}
	cal := e.calibrations[key]
	if cal == nil {
		cal = &tokenCalibration{}
		e.calibrations[key] = cal
	}
	return cal.ratios()
}

// calibratedEstimator returns a text→tokens closure with the current
// model's ratios snapshotted once, so per-message estimation loops do not
// re-resolve the key per string. Must not be called while holding e.mu.
func (e *Engine) calibratedEstimator() func(string) int {
	R := e.ratiosFor(e.modelKey())
	return func(s string) int {
		return estimateWithFeatures(utils.CountTokenFeatures(s), R)
	}
}

// EstimateTokensCalibrated estimates tokens with the 6-feature linear model,
// using seed ratios until a fit exists for the current provider/model.
func (e *Engine) EstimateTokensCalibrated(text string) int {
	return estimateWithFeatures(utils.CountTokenFeatures(text), e.ratiosFor(e.modelKey()))
}

// recordCalibrationObservation records a differential observation for the
// model that just answered: real = TotalInputTokens growth since the
// previous anchored turn, features = the messages appended in between —
// exactly the input growth between the two requests (the previous assistant
// reply is included because its output tokens entered this request's input;
// the current reply is not, and anchoring on its index covers it next turn).
//
// Called right after the assistant message enters history, so the anchored
// index is len(e.messages)-1. Lock order is strictly e.mu → calibMu (never
// reversed — RewindToScoped acquires calibMu while holding e.mu in write
// mode), so the feature scan runs between two calibMu sections instead of
// under it. Records are serialized per engine (one query goroutine at a
// time), so the anchor snapshot cannot go stale across the gap.
func (e *Engine) recordCalibrationObservation(msg *types.Message) {
	if msg == nil || msg.Usage == nil {
		return
	}
	total := msg.Usage.TotalInputTokens()
	if total <= 0 {
		// Provider reported no usage. Anchoring on 0 would make the next
		// delta equal the whole context against a small feature window.
		return
	}
	key := e.modelKey()

	// Snapshot the slice under the SAME lock that produced n: a rewind between
	// two separate RLocks would shrink e.messages and make a later indexed
	// read panic for i < n-1. The snapshot is self-consistent even under
	// interleaving (the only in-place mutator runs on this same goroutine).
	e.mu.RLock()
	msgs := e.messages
	n := len(msgs)
	e.mu.RUnlock()

	e.calibMu.Lock()
	if e.calibrations == nil {
		e.calibrations = make(map[string]*tokenCalibration)
	}
	cal := e.calibrations[key]
	if cal == nil {
		cal = &tokenCalibration{}
		e.calibrations[key] = cal
	}
	// The anchored message index is out of range (compact/rewind rewrote
	// history shorter), so the anchored pair no longer describes adjacent
	// requests — restart anchoring from this turn instead of emitting a
	// bogus observation.
	if cal.anchored && cal.anchorMsgIdx >= n {
		cal.anchored = false
	}
	real := total - cal.anchorTotal
	windowStart := cal.anchorMsgIdx
	observable := cal.anchored && real > 0 && n-1 > windowStart
	e.calibMu.Unlock()

	var f utils.TokenFeatures
	if observable {
		for i := windowStart; i < n-1; i++ {
			f = addTokenFeatures(f, messageTokenFeatures(msgs[i]))
		}
	}

	e.calibMu.Lock()
	if observable && tokenFeaturesNonZero(f) {
		cal.observe(f, real)
	}
	cal.anchorTotal = total
	cal.anchorMsgIdx = n - 1
	cal.anchored = true
	e.calibMu.Unlock()
}

// addTokenFeatures sums two feature vectors.
func addTokenFeatures(a, b utils.TokenFeatures) utils.TokenFeatures {
	return utils.TokenFeatures{
		CJK:    a.CJK + b.CJK,
		Struct: a.Struct + b.Struct,
		Digit:  a.Digit + b.Digit,
		Indent: a.Indent + b.Indent,
		Words:  a.Words + b.Words,
		Other:  a.Other + b.Other,
	}
}

// tokenFeaturesNonZero reports whether any feature is set — an all-zero
// window (e.g. only image messages appended) carries no fitting signal.
func tokenFeaturesNonZero(f utils.TokenFeatures) bool {
	return f != utils.TokenFeatures{}
}

// messageTokenFeatures extracts the text features of a message, mirroring
// the text branches of the message estimator (microcompact.go) so the fit
// learns on the same feature distribution estimation later consumes.
// Image/document blocks are skipped: they contribute fixed-size estimates,
// not char signal.
func messageTokenFeatures(msg types.Message) utils.TokenFeatures {
	var f utils.TokenFeatures
	for _, block := range msg.Content {
		switch block.Type {
		case types.ContentTypeText:
			f = addTokenFeatures(f, utils.CountTokenFeatures(block.Text))
		case types.ContentTypeToolResult:
			f = addTokenFeatures(f, toolResultTokenFeatures(block.Content))
		case types.ContentTypeThinking:
			f = addTokenFeatures(f, utils.CountTokenFeatures(block.Thinking))
		case types.ContentTypeRedacted:
			f = addTokenFeatures(f, utils.CountTokenFeatures(block.Data))
		case types.ContentTypeToolUse:
			f = addTokenFeatures(f, utils.CountTokenFeatures(block.Name+string(block.Input)))
		case types.ContentTypeImage, types.ContentTypeDocument:
			// Fixed-size estimates; no char signal to learn from.
		default:
			raw, _ := json.Marshal(block)
			f = addTokenFeatures(f, utils.CountTokenFeatures(string(raw)))
		}
	}
	return f
}

// toolResultTokenFeatures mirrors calculateToolResultTokens' content parsing
// (JSON string, array of blocks, raw fallback) but skips the fixed-size
// image/document branches.
func toolResultTokenFeatures(content json.RawMessage) utils.TokenFeatures {
	if len(content) == 0 {
		return utils.TokenFeatures{}
	}
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		return utils.CountTokenFeatures(str)
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(content, &blocks); err == nil {
		var f utils.TokenFeatures
		for _, block := range blocks {
			if strings.Trim(string(block["type"]), `"`) == "text" {
				var text string
				if err := json.Unmarshal(block["text"], &text); err == nil {
					f = addTokenFeatures(f, utils.CountTokenFeatures(text))
				}
			}
		}
		return f
	}
	return utils.CountTokenFeatures(string(content))
}
