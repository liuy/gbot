package engine

// token_calibration_test.go — tests for the 6-feature calibrated token
// estimator: seed path, least-squares recovery, clamping, per-model-key
// isolation, and observation anchor lifecycle.

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// calibStubProvider is the minimal llm.Provider for model-key tests.
type calibStubProvider struct{ name string }

func (p *calibStubProvider) Name() string { return p.name }
func (p *calibStubProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}
func (p *calibStubProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, nil
}

// calibAssistant builds an assistant message carrying usage.
func calibAssistant(text string, inputTokens int) *types.Message {
	msg := types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock(text)})
	msg.Usage = &types.Usage{InputTokens: inputTokens}
	return &msg
}

func calibTextMessage(text string) types.Message {
	return types.NewUserMessage([]types.ContentBlock{types.NewTextBlock(text)})
}

// ---------------------------------------------------------------------------
// Seed path
// ---------------------------------------------------------------------------

func TestEstimateTokensCalibrated_SeedCJK(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	text := strings.Repeat("世", 1000)
	// Seed R_cjk=0.72: 1000 CJK chars → 720. Fields counts one word (no
	// whitespace) adding 1×0.48 → 720.48 → 720.
	got := e.EstimateTokensCalibrated(text)
	if got < 648 || got > 792 {
		t.Errorf("EstimateTokensCalibrated(1000 CJK) = %d, want 720 ±10%% [648,792]", got)
	}
	if got != 720 {
		t.Errorf("EstimateTokensCalibrated(1000 CJK) = %d, want exactly 720 (0.72×1000 + 1 word × 0.48)", got)
	}
}

func TestEstimateTokensCalibrated_FloorsAtZero(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	want := 0
	// Indent-only text with the negative seed ratio must not go below 0.
	if got := e.EstimateTokensCalibrated("\t\t\t\t"); got != want {
		t.Errorf("EstimateTokensCalibrated(indent-only) = %d, want %d (negative ratio floored)", got, want)
	}
	if got := e.EstimateTokensCalibrated(""); got != want {
		t.Errorf("EstimateTokensCalibrated(\"\") = %d, want %d", got, want)
	}
}

func TestEstimateTokensCalibrated_UsesSeedUntilMinObs(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	text := strings.Repeat("界", 500)
	first := e.EstimateTokensCalibrated(text)
	// One observation (below the 3-observation minimum) must not change the
	// estimate: seeds stay in effect.
	e.calibMu.Lock()
	e.calibrations = map[string]*tokenCalibration{
		e.modelKey(): {obs: []calibObservation{{f: utils.TokenFeatures{CJK: 10}, real: 100}}},
	}
	e.calibMu.Unlock()
	if second := e.EstimateTokensCalibrated(text); second != first {
		t.Errorf("estimate changed with %d observations (< min 3): %d → %d", 1, first, second)
	}
}

// ---------------------------------------------------------------------------
// Fit path
// ---------------------------------------------------------------------------

// calibFeedObservations generates n observations whose real token counts are
// exactly the dot product of the given ratios and feature vectors spanning
// all six dimensions independently (no two columns proportional — a
// collinear set makes the normal equations singular).
func calibFeedObservations(cal *tokenCalibration, R [6]float64, n int) {
	features := [][6]int{
		{1000, 50, 20, 10, 100, 300},
		{200, 900, 40, 20, 150, 250},
		{30, 60, 800, 30, 200, 400},
		{10, 20, 30, 500, 250, 350},
		{40, 80, 60, 40, 700, 450},
		{20, 40, 50, 60, 300, 600},
		{500, 100, 200, 80, 400, 100},
		{300, 300, 100, 10, 50, 700},
	}
	for i := range n {
		v := features[i%len(features)]
		f := utils.TokenFeatures{CJK: v[0], Struct: v[1], Digit: v[2], Indent: v[3], Words: v[4], Other: v[5]}
		real := 0.0
		for j := range 6 {
			real += float64(v[j]) * R[j]
		}
		cal.observe(f, int(math.Round(real)))
	}
}

func TestTokenCalibration_FitRecoversRatios(t *testing.T) {
	t.Parallel()
	// Ratios from the spec test design: within all clamp ranges.
	want := [6]float64{0.7, 1.4, 1.0, -0.1, 0.5, 0.12}
	cal := &tokenCalibration{}
	calibFeedObservations(cal, want, 10)

	if len(cal.obs) != 10 {
		t.Fatalf("obs count = %d, want 10", len(cal.obs))
	}
	if !cal.fitted {
		t.Fatal("calibration not fitted after 10 observations (>= min 3)")
	}
	for j, got := range cal.R {
		if math.Abs(got-want[j]) > 0.15 {
			t.Errorf("fitted R[%d] = %.4f, want %.4f ±0.15", j, got, want[j])
		}
	}
}

func TestTokenCalibration_SingularSystemKeepsPreviousRatios(t *testing.T) {
	t.Parallel()
	// Observations varying only CJK leave five zero rows in the normal
	// equations — the system is singular and the seed ratios must survive.
	cal := &tokenCalibration{}
	for i := 1; i <= 5; i++ {
		cal.observe(utils.TokenFeatures{CJK: 100 * i}, 72*i)
	}
	if cal.fitted {
		t.Error("fit reported success on a singular system")
	}
	if cal.ratios() != calibrationSeedRatios {
		t.Error("ratios changed on singular system; seeds must be kept")
	}
}

func TestTokenCalibration_ClampsExtremeFit(t *testing.T) {
	t.Parallel()
	// Ratios far outside the clamp ranges: CJK 5.0 → clamp 1.5, indent
	// -2.0 → clamp -1.0; the in-range components pass through.
	src := [6]float64{5.0, 1.0, 1.0, -2.0, 0.5, 0.1}
	cal := &tokenCalibration{}
	calibFeedObservations(cal, src, 12)

	if cal.R[calibFeatCJK] != 1.5 {
		t.Errorf("R_cjk = %.4f, want clamped 1.5", cal.R[calibFeatCJK])
	}
	if cal.R[calibFeatIndent] != -1.0 {
		t.Errorf("R_indent = %.4f, want clamped -1.0", cal.R[calibFeatIndent])
	}
	for j, want := range map[int]float64{calibFeatStruct: 1.0, calibFeatDigit: 1.0, calibFeatWords: 0.5, calibFeatOther: 0.1} {
		if math.Abs(cal.R[j]-want) > 0.15 {
			t.Errorf("R[%d] = %.4f, want %.2f ±0.15 (in-range component must not be distorted)", j, cal.R[j], want)
		}
	}
}

func TestTokenCalibration_WindowSlides(t *testing.T) {
	t.Parallel()
	cal := &tokenCalibration{}
	for i := range calibrationWindow + 10 {
		cal.observe(utils.TokenFeatures{CJK: 100 + i, Words: 10, Other: 50}, 100+i)
	}
	if len(cal.obs) != calibrationWindow {
		t.Errorf("obs count = %d, want capped at %d", len(cal.obs), calibrationWindow)
	}
	// The oldest observations must have slid out: the first remaining
	// observation carries CJK = 100 + 10.
	if cal.obs[0].f.CJK != 110 {
		t.Errorf("window head CJK = %d, want 110 (oldest 10 slid out)", cal.obs[0].f.CJK)
	}
}

// ---------------------------------------------------------------------------
// Observation collection (recordCalibrationObservation)
// ---------------------------------------------------------------------------

func TestRecordCalibrationObservation_Differential(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("first question")}
	e.mu.Unlock()

	// Turn 1 answer: anchors (total + the assistant message's own index),
	// no observation yet.
	a1 := calibAssistant("first answer", 1000)
	e.mu.Lock()
	e.messages = append(e.messages, *a1)
	e.mu.Unlock()
	e.recordCalibrationObservation(a1)

	key := e.modelKey()
	e.calibMu.Lock()
	cal := e.calibrations[key]
	e.calibMu.Unlock()
	if cal == nil {
		t.Fatal("calibration not registered on first usage")
	}
	if len(cal.obs) != 0 {
		t.Fatalf("first turn recorded %d observations, want 0 (anchor-only)", len(cal.obs))
	}
	if !cal.anchored || cal.anchorTotal != 1000 || cal.anchorMsgIdx != 1 {
		t.Errorf("anchor = (total %d, idx %d, anchored %v), want (1000, 1, true)",
			cal.anchorTotal, cal.anchorMsgIdx, cal.anchored)
	}

	// Turn 2: a tool result and the second answer land. The observation is
	// the usage delta (300) against the features of messages since the
	// anchor — the first answer (its output tokens entered this request's
	// input) plus the tool result; the second answer itself is excluded.
	tool := types.NewUserMessage([]types.ContentBlock{{
		Type:    types.ContentTypeToolResult,
		Content: []byte(`"file contents with 123 numbers"`),
	}})
	a2 := calibAssistant("second answer", 1300)
	e.mu.Lock()
	e.messages = append(e.messages, tool, *a2)
	e.mu.Unlock()
	e.recordCalibrationObservation(a2)

	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	cal = e.calibrations[key]
	if len(cal.obs) != 1 {
		t.Fatalf("second turn recorded %d observations, want 1", len(cal.obs))
	}
	obs := cal.obs[0]
	if obs.real != 300 {
		t.Errorf("observation real = %d, want 300 (1300-1000)", obs.real)
	}
	wantFeatures := addTokenFeatures(
		messageTokenFeatures(e.messages[1]), // first answer
		messageTokenFeatures(e.messages[2]), // tool result
	)
	if obs.f != wantFeatures {
		t.Errorf("observation features = %+v, want %+v (assistant reply + tool result since anchor)", obs.f, wantFeatures)
	}
	if cal.anchorTotal != 1300 || cal.anchorMsgIdx != 3 {
		t.Errorf("anchor = (total %d, idx %d), want (1300, 3)", cal.anchorTotal, cal.anchorMsgIdx)
	}
}

func TestRecordCalibrationObservation_SkipsDegenerate(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	// Textless assistant replies keep the feature windows controlled by the
	// user-side messages alone.
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("q"), *calibAssistant("", 500)}
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[1])

	// Non-positive delta: usage went down with the message list growing —
	// observation must be skipped, anchor advanced.
	a2 := calibAssistant("", 400)
	e.mu.Lock()
	e.messages = append(e.messages, calibTextMessage("more"), *a2)
	e.mu.Unlock()
	e.recordCalibrationObservation(a2)

	// All-zero feature window: an image-only message contributes no text
	// features — observation must be skipped, anchor still advanced.
	img := types.NewUserMessage([]types.ContentBlock{{Type: types.ContentTypeImage}})
	a3 := calibAssistant("", 900)
	e.mu.Lock()
	e.messages = append(e.messages, img, *a3)
	e.mu.Unlock()
	e.recordCalibrationObservation(a3)

	key := e.modelKey()
	e.calibMu.Lock()
	cal := e.calibrations[key]
	e.calibMu.Unlock()
	if cal == nil {
		t.Fatal("calibration not registered")
	}
	if len(cal.obs) != 0 {
		t.Errorf("recorded %d observations from degenerate windows, want 0", len(cal.obs))
	}
	if cal.anchorTotal != 900 {
		t.Errorf("anchorTotal = %d, want 900 (anchor advances even on skipped observations)", cal.anchorTotal)
	}

	// A healthy window after the skips still produces a correct observation
	// against the advanced anchor: real = 1200-900, features = the textless
	// reply at the anchor plus the single new message.
	healthy := calibTextMessage("healthy window with words 123")
	a4 := calibAssistant("", 1200)
	e.mu.Lock()
	e.messages = append(e.messages, healthy, *a4)
	e.mu.Unlock()
	e.recordCalibrationObservation(a4)

	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	if len(cal.obs) != 1 {
		t.Fatalf("healthy window recorded %d observations, want 1", len(cal.obs))
	}
	if cal.obs[0].real != 300 {
		t.Errorf("observation real = %d, want 300 (1200-900)", cal.obs[0].real)
	}
	if cal.obs[0].f != messageTokenFeatures(healthy) {
		t.Errorf("observation features = %+v, want features of the single window message %+v",
			cal.obs[0].f, messageTokenFeatures(healthy))
	}
}

func TestRecordCalibrationObservation_ShrinkResetsAnchor(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	// Grow history so the anchor sits deep in the list.
	e.mu.Lock()
	e.messages = []types.Message{
		calibTextMessage("q1"), *calibAssistant("a1", 2000),
		calibTextMessage("q2"), *calibAssistant("a2", 2600),
		calibTextMessage("q3"), *calibAssistant("a3", 3000),
	}
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[5])
	key := e.modelKey()
	e.calibMu.Lock()
	before := len(e.calibrations[key].obs)
	anchorIdx := e.calibrations[key].anchorMsgIdx
	e.calibMu.Unlock()
	if anchorIdx != 5 {
		t.Fatalf("pre-shrink anchor idx = %d, want 5", anchorIdx)
	}

	// Compact rewrote history shorter than the anchored index.
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("summary")}
	e.mu.Unlock()
	post := calibAssistant("post-compact", 300)
	e.mu.Lock()
	e.messages = append(e.messages, *post)
	e.mu.Unlock()
	e.recordCalibrationObservation(post)

	e.calibMu.Lock()
	cal := e.calibrations[key]
	e.calibMu.Unlock()
	if len(cal.obs) != before {
		t.Fatalf("shrink-reset turn recorded observations: %d → %d", before, len(cal.obs))
	}
	if !cal.anchored || cal.anchorTotal != 300 || cal.anchorMsgIdx != 1 {
		t.Errorf("anchor after reset = (total %d, idx %d, anchored %v), want (300, 1, true)",
			cal.anchorTotal, cal.anchorMsgIdx, cal.anchored)
	}

	// Usage grew after the reset — the fresh anchor must yield a positive,
	// correctly-scoped observation (not a negative pre-shrink delta).
	next := calibAssistant("after reset", 1000)
	e.mu.Lock()
	e.messages = append(e.messages, calibTextMessage("new turn"), *next)
	e.mu.Unlock()
	e.recordCalibrationObservation(next)

	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	if len(cal.obs) != before+1 {
		t.Fatalf("post-reset observation count = %d, want %d", len(cal.obs), before+1)
	}
	obs := cal.obs[len(cal.obs)-1]
	if obs.real != 700 {
		t.Errorf("post-reset observation real = %d, want 700 (1000-300)", obs.real)
	}
	want := addTokenFeatures(
		messageTokenFeatures(e.messages[1]), // post-compact reply at fresh anchor
		messageTokenFeatures(e.messages[2]), // new turn message
	)
	if obs.f != want {
		t.Errorf("post-reset observation features = %+v, want %+v", obs.f, want)
	}
}

func TestRecordCalibrationObservation_ZeroUsageIgnored(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("q")}
	e.mu.Unlock()
	// Provider reported no usage; anchoring on 0 is skipped entirely.
	zero := calibAssistant("a", 0)
	e.mu.Lock()
	e.messages = append(e.messages, *zero)
	e.mu.Unlock()
	e.recordCalibrationObservation(zero)

	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	if cal := e.calibrations[e.modelKey()]; cal != nil {
		t.Errorf("zero usage anchored a calibration: %+v", cal)
	}
}

// ---------------------------------------------------------------------------
// modelKey isolation
// ---------------------------------------------------------------------------

func TestCalibration_ModelKeyIsolation(t *testing.T) {
	t.Parallel()
	e := &Engine{provider: &calibStubProvider{name: "p1"}, model: "m1"}

	// Fit m1 away from the seeds, then anchor it mid-list.
	fitted := &tokenCalibration{}
	calibFeedObservations(fitted, [6]float64{0.7, 1.4, 1.0, -0.1, 0.5, 0.12}, 10)
	e.calibMu.Lock()
	e.calibrations = map[string]*tokenCalibration{"p1/m1": fitted}
	e.calibMu.Unlock()
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("q"), *calibAssistant("a", 1000)}
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[1])

	// Switching models: m2's first usage registers seeds + its own anchor;
	// m1's fit and anchor must be untouched.
	e.mu.Lock()
	e.model = "m2"
	e.mu.Unlock()
	b1 := calibAssistant("b1", 5000)
	e.mu.Lock()
	e.messages = append(e.messages, calibTextMessage("other model"), *b1)
	e.mu.Unlock()
	e.recordCalibrationObservation(b1)

	e.calibMu.Lock()
	m1 := e.calibrations["p1/m1"]
	m2 := e.calibrations["p1/m2"]
	e.calibMu.Unlock()
	if m2 == nil {
		t.Fatal("p1/m2 not registered after its first usage")
	}
	if len(m2.obs) != 0 {
		t.Errorf("m2 recorded %d observations on its anchoring turn, want 0", len(m2.obs))
	}
	if !m2.anchored || m2.anchorTotal != 5000 || m2.anchorMsgIdx != 3 {
		t.Errorf("m2 anchor = (total %d, idx %d, anchored %v), want (5000, 3, true)",
			m2.anchorTotal, m2.anchorMsgIdx, m2.anchored)
	}
	if m2.ratios() != calibrationSeedRatios {
		t.Error("m2 ratios must be seeds — m1 observations must not leak across keys")
	}
	if len(m1.obs) != 10 {
		t.Errorf("m1 observations = %d, want 10 (untouched by m2 usage)", len(m1.obs))
	}
	if m1.anchorTotal != 1000 {
		t.Errorf("m1 anchorTotal = %d, want 1000 (untouched by m2 usage)", m1.anchorTotal)
	}

	// Switching back restores m1's fit and continues from m1's anchor.
	e.mu.Lock()
	e.model = "m1"
	e.mu.Unlock()
	if got := e.ratiosFor("p1/m1"); got != fitted.R {
		t.Errorf("m1 ratios after model switch = %v, want prior fit %v", got, fitted.R)
	}
	a2 := calibAssistant("a2", 1500)
	e.mu.Lock()
	e.messages = append(e.messages, calibTextMessage("back on m1"), *a2)
	e.mu.Unlock()
	e.recordCalibrationObservation(a2)

	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	if len(m1.obs) != 11 {
		t.Errorf("m1 observations = %d, want 11 (continues from its own anchor)", len(m1.obs))
	}
	if m1.obs[10].real != 500 {
		t.Errorf("m1 observation real = %d, want 500 (1500-1000 against m1's anchor)", m1.obs[10].real)
	}
	if m2.anchorTotal != 5000 {
		t.Errorf("m2 anchorTotal = %d, want 5000 (untouched)", m2.anchorTotal)
	}
}

// ---------------------------------------------------------------------------
// Calibrated message/token estimation wrappers
// ---------------------------------------------------------------------------

func TestTokenCountWithEstimation_CalibratedDelta(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	msgs := []types.Message{
		calibTextMessage("question one"),
		*calibAssistant("answer one", 8000),
		calibTextMessage(strings.Repeat("世", 100)),
	}
	// base = 8000; delta = calibrated estimate of the trailing user message
	// (100 CJK + 1 word: 100×0.72 + 0.48 = 72.48 → 72) + envelope 5.
	want := 8000 + 72 + defaultMessageEnvelopeTokens
	if got := e.tokenCountWithEstimation(msgs); got != want {
		t.Errorf("tokenCountWithEstimation = %d, want %d (8000 base + calibrated delta)", got, want)
	}
	// No usage anchor anywhere → full calibrated estimation of both messages.
	full := e.tokenCountWithEstimation(msgs[:1])
	if full <= 0 {
		t.Errorf("full-estimation fallback = %d, want > 0", full)
	}
}

func TestEstimateTokensCalibratedNilProvider(t *testing.T) {
	t.Parallel()
	// Bare engines (nil provider) must not panic; the key degrades to "/model".
	// "hello world": Words 2, Other 11 → 2×0.48 + 11×0.11 = 2.17 → 2.
	e := &Engine{model: "test"}
	if got := e.EstimateTokensCalibrated("hello world"); got != 2 {
		t.Errorf("EstimateTokensCalibrated with nil provider = %d, want 2", got)
	}
	if key := calibrationKey(nil, "test"); key != "/test" {
		t.Errorf("calibrationKey(nil, test) = %q, want %q", key, "/test")
	}
}

// ---------------------------------------------------------------------------
// Message/block feature extraction (mirrors the estimator's text branches)
// ---------------------------------------------------------------------------

func TestMessageTokenFeatures_BlockTypes(t *testing.T) {
	t.Parallel()
	msg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "plain text 42"},
			{Type: types.ContentTypeThinking, Thinking: "think about it"},
			{Type: types.ContentTypeRedacted, Data: "redacted-data"},
			{Type: types.ContentTypeToolUse, Name: "Bash", Input: json.RawMessage(`{"cmd":"ls"}`)},
			{Type: types.ContentTypeImage},
			{Type: types.ContentTypeDocument, EstTokens: 5000, Size: 20000},
			{Type: "custom", Text: "odd"},
		},
	}
	want := utils.TokenFeatures{}
	want = addTokenFeatures(want, utils.CountTokenFeatures("plain text 42"))
	want = addTokenFeatures(want, utils.CountTokenFeatures("think about it"))
	want = addTokenFeatures(want, utils.CountTokenFeatures("redacted-data"))
	want = addTokenFeatures(want, utils.CountTokenFeatures(`Bash{"cmd":"ls"}`))
	// Image/document blocks carry fixed-size estimates, no char signal.
	custom, err := json.Marshal(msg.Content[6])
	if err != nil {
		t.Fatalf("marshal custom block: %v", err)
	}
	want = addTokenFeatures(want, utils.CountTokenFeatures(string(custom)))

	got := messageTokenFeatures(msg)
	if got != want {
		t.Errorf("messageTokenFeatures = %+v, want %+v", got, want)
	}
}

func TestToolResultTokenFeatures_ContentShapes(t *testing.T) {
	t.Parallel()
	// JSON string content.
	str := toolResultTokenFeatures(json.RawMessage(`"ls -la output 123"`))
	if str != utils.CountTokenFeatures("ls -la output 123") {
		t.Errorf("string content = %+v, want features of the decoded string", str)
	}
	// Array of blocks: text blocks counted, image/document skipped.
	arr := toolResultTokenFeatures(json.RawMessage(
		`[{"type":"text","text":"first"},{"type":"image","source":{}},{"type":"text","text":"second"}]`))
	wantArr := addTokenFeatures(
		utils.CountTokenFeatures("first"), utils.CountTokenFeatures("second"))
	if arr != wantArr {
		t.Errorf("array content = %+v, want %+v (text blocks only)", arr, wantArr)
	}
	// Not valid JSON at all → raw fallback.
	raw := toolResultTokenFeatures(json.RawMessage(`plain not json`))
	if raw != utils.CountTokenFeatures("plain not json") {
		t.Errorf("raw fallback = %+v, want features of the raw bytes", raw)
	}
	// Empty content → zero features.
	if got := toolResultTokenFeatures(nil); got != (utils.TokenFeatures{}) {
		t.Errorf("nil content = %+v, want zero features", got)
	}
}

func TestRecordCalibrationObservation_NilUsage(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	// Defensive guards: nil message and usage-less message are no-ops.
	e.recordCalibrationObservation(nil)
	e.recordCalibrationObservation(&types.Message{Role: types.RoleAssistant})
	e.calibMu.Lock()
	registered := len(e.calibrations)
	e.calibMu.Unlock()
	if registered != 0 {
		t.Errorf("registered %d calibrations from nil/usage-less messages, want 0", registered)
	}
}

func TestSolveLinear6_DiagonalSystem(t *testing.T) {
	t.Parallel()
	// A diagonal system keeps zeros below every pivot, exercising the
	// zero-factor elimination skip, and has an exact solution.
	var a [6][7]float64
	solution := [6]float64{4, 3, 3, 3, 2, 3.5}
	for i, x := range solution {
		a[i][i] = float64(i + 2) // pivots 2..7
		a[i][6] = float64(i+2) * x
	}
	got, ok := solveLinear6(a)
	if !ok {
		t.Fatal("diagonal system reported singular")
	}
	for i, x := range solution {
		if math.Abs(got[i]-x) > 1e-9 {
			t.Errorf("x[%d] = %.6f, want %.1f", i, got[i], x)
		}
	}
}

func TestSolveLinear6_ZeroMatrix(t *testing.T) {
	t.Parallel()
	if x, ok := solveLinear6([6][7]float64{}); ok {
		t.Errorf("zero matrix reported solvable: %v", x)
	}
}

// ---------------------------------------------------------------------------
// Estimator injection into AutoCompactor
// ---------------------------------------------------------------------------

// injectableMeta implements EngineCompactorMeta plus the calibrated-estimator
// interface NewAutoCompactor probes for; a rename of
// EstimateTokensCalibrated must fail this test instead of silently reverting
// compact estimation to the 2-feature heuristic.
type injectableMeta struct {
	testEngineMeta
	called *int32
}

func (m *injectableMeta) EstimateTokensCalibrated(s string) int {
	atomic.AddInt32(m.called, 1)
	// Distinct value: 6-feature seed on a pure-CJK string differs from the
	// 2-feature 0.65 ratio (0.72 vs 0.65 per char).
	return int(float64(len([]rune(s))) * 0.72)
}

func TestNewAutoCompactor_InjectsCalibratedEstimator(t *testing.T) {
	store, err := short.NewStore(t.TempDir() + "/inject.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	called := int32(0)
	meta := &injectableMeta{model: "m", sessionID: "s", contextWindow: 200000, provider: &compactMockProvider{}, called: &called}
	sc := NewAutoCompactor(store, meta)

	text := strings.Repeat("丁", 500) // 500 CJK chars
	// 2-feature: 500×0.65 = 325. Calibrated seed: 500×0.72 = 360.
	if got := sc.estimator(text); got != 360 {
		t.Errorf("AutoCompactor estimator not calibrated: got %d, want 360 (6-feature seed); silent reversion to 2-feature heuristic (325)?", got)
	}
	if atomic.LoadInt32(&called) == 0 {
		t.Error("EstimateTokensCalibrated was never called — injection branch broken")
	}
}
