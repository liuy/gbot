package engine

// token_calibration_test.go — tests for the 6-feature calibrated token
// estimator: seed path, least-squares recovery, clamping, per-model-key
// isolation, and observation anchor lifecycle.

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
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
	if cal.ratios() != utils.SeedTokenRatios {
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
	if m2.ratios() != utils.SeedTokenRatios {
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

// ---------------------------------------------------------------------------
// Token calibration persistence pins
// ---------------------------------------------------------------------------

// TestClampTableBounds pins every bound of calibrationClamps. The fit is
// fed ratios far outside the table in both directions, so each of the twelve
// bounds is the value a fitted ratio actually lands on.
func TestClampTableBounds(t *testing.T) {
	t.Parallel()

	high := &tokenCalibration{}
	calibFeedObservations(high, [6]float64{10, 20, 30, 40, 50, 60}, 10)
	if !high.fitted {
		t.Fatal("all-high fit not applied")
	}
	wantHigh := [6]float64{1.5, 3.0, 2.0, 0.5, 1.2, 0.5}
	for j, want := range wantHigh {
		if high.R[j] != want {
			t.Errorf("upper clamp R[%d] = %v, want exactly %v (fit source 10..60 is far above the table)", j, high.R[j], want)
		}
	}

	low := &tokenCalibration{}
	calibFeedObservations(low, [6]float64{-10, -10, -10, -5, -10, -10}, 10)
	if !low.fitted {
		t.Fatal("all-low fit not applied")
	}
	wantLow := [6]float64{0.3, 0.3, 0.2, -1.0, 0.1, 0.05}
	for j, want := range wantLow {
		if low.R[j] != want {
			t.Errorf("lower clamp R[%d] = %v, want exactly %v (fit source -10 is far below the table)", j, low.R[j], want)
		}
	}
}

// TestWindowCapExact pins calibrationWindow with literal counts; deriving
// the feed length from the constant would let a changed constant slide the
// window unnoticed.
func TestWindowCapExact(t *testing.T) {
	t.Parallel()
	cal := &tokenCalibration{}
	for i := range 74 {
		cal.observe(utils.TokenFeatures{CJK: 100 + i}, 10+i)
	}
	if len(cal.obs) != 64 {
		t.Fatalf("obs count = %d, want exactly 64 (calibrationWindow)", len(cal.obs))
	}
	if cal.obs[0].f.CJK != 110 {
		t.Errorf("window head CJK = %d, want 110 (10 oldest of 74 slid out)", cal.obs[0].f.CJK)
	}
	if cal.obs[63].f.CJK != 173 {
		t.Errorf("window tail CJK = %d, want 173 (newest observation)", cal.obs[63].f.CJK)
	}
}

// TestMinObsGateExact pins calibrationMinObs at both sides of the gate and
// pins each conjunct of the ratios() guard separately.
func TestMinObsGateExact(t *testing.T) {
	t.Parallel()
	fittedR := [6]float64{0.9, 0.9, 0.9, -0.5, 0.9, 0.2}
	if fittedR == utils.SeedTokenRatios {
		t.Fatal("test ratios must differ from the seeds")
	}
	obs := func(n int) []calibObservation {
		o := make([]calibObservation, n)
		for i := range o {
			o[i] = calibObservation{f: utils.TokenFeatures{CJK: 10 * (i + 1)}, real: 7 * (i + 1)}
		}
		return o
	}

	two := &tokenCalibration{obs: obs(2), R: fittedR, fitted: true}
	if got := two.ratios(); got != utils.SeedTokenRatios {
		t.Errorf("ratios with 2 observations = %v, want seeds (minimum is 3)", got)
	}
	three := &tokenCalibration{obs: obs(3), R: fittedR, fitted: true}
	if got := three.ratios(); got != fittedR {
		t.Errorf("ratios with 3 observations = %v, want the fitted %v (3 meets the minimum)", got, fittedR)
	}
	// fitted must be honoured independently of the observation count.
	unfitted := &tokenCalibration{obs: obs(3), R: fittedR, fitted: false}
	if got := unfitted.ratios(); got != utils.SeedTokenRatios {
		t.Errorf("ratios of an unfitted calibration = %v, want seeds", got)
	}
}

// TestPartialPivotRequired feeds a system whose only usable pivot for
// column 0 sits below the diagonal, so the solve is possible only if the
// search runs and the winning row is actually swapped into place.
func TestPartialPivotRequired(t *testing.T) {
	t.Parallel()
	var a [6][7]float64
	a[0][1], a[0][6] = 2, 6
	a[1][0], a[1][6] = 3, 12
	a[2][2], a[2][6] = 5, 10
	a[3][3], a[3][6] = 7, 21
	a[4][4], a[4][6] = 11, 33
	a[5][5], a[5][6] = 13, 52
	want := [6]float64{4, 3, 2, 3, 3, 4}

	got, ok := solveLinear6(a)
	if !ok {
		t.Fatal("system solvable only with row pivoting reported singular")
	}
	if got != want {
		t.Errorf("solveLinear6 = %v, want %v", got, want)
	}
}

// TestSingularityThresholdAtEps puts a pivot exactly on the threshold
// (maxAbs·1e-12, computed the same way the solver does it): a pivot that equals
// eps is usable, only a strictly smaller one is singular.
func TestSingularityThresholdAtEps(t *testing.T) {
	t.Parallel()
	eps := 1e12 * 1e-12
	var a [6][7]float64
	a[0][0], a[0][6] = 1e12, 1e12
	a[1][1], a[1][6] = eps, eps*3
	a[2][2], a[2][6] = 5, 10
	a[3][3], a[3][6] = 7, 21
	a[4][4], a[4][6] = 11, 33
	a[5][5], a[5][6] = 13, 52

	got, ok := solveLinear6(a)
	if !ok {
		t.Fatalf("pivot exactly at eps (%v) rejected as singular; the guard is strictly-less-than", eps)
	}
	want := [6]float64{1, 3, 2, 3, 3, 4}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("x[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestEliminationFactorTraps builds a system with elimination factors of
// exactly -1 and exactly +1. Skipping either elimination (a guard that tests
// the factor against the wrong constant) changes the answer.
func TestEliminationFactorTraps(t *testing.T) {
	t.Parallel()
	var a [6][7]float64
	a[0][0], a[0][1], a[0][6] = 2, 4, 6
	a[1][0], a[1][1], a[1][6] = -2, 3, 1
	a[2][2], a[2][6] = 5, 10
	a[3][3], a[3][4], a[3][6] = 1, 2, 3
	a[4][3], a[4][4], a[4][6] = 2, 5, 7
	a[5][3], a[5][5], a[5][6] = 2, 7, 16

	got, ok := solveLinear6(a)
	if !ok {
		t.Fatal("factor-trap system reported singular")
	}
	want := [6]float64{1, 1, 2, 1, 1, 2}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("x[%d] = %v, want %v (factor ±1 elimination skipped)", i, got[i], want[i])
		}
	}
}

// TestEliminationAllRows uses a dense system so every column needs more
// than one row eliminated; a loop that stops after the first row below the
// diagonal cannot recover the solution.
func TestEliminationAllRows(t *testing.T) {
	t.Parallel()
	m := [6][6]float64{
		{4, 1, 0, 2, 0, 1},
		{1, 5, 2, 0, 1, 0},
		{0, 2, 6, 1, 0, 3},
		{3, 0, 1, 7, 2, 0},
		{0, 1, 0, 2, 8, 1},
		{2, 0, 3, 0, 1, 9},
	}
	want := [6]float64{1, 2, 3, 4, 5, 6}
	var a [6][7]float64
	for i := range m {
		for j := range m[i] {
			a[i][j] = m[i][j]
			a[i][6] += m[i][j] * want[j]
		}
	}
	got, ok := solveLinear6(a)
	if !ok {
		t.Fatal("dense system reported singular")
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("x[%d] = %v, want %v (rows below the first pivot row not eliminated)", i, got[i], want[i])
		}
	}
}

// TestEstimateUsesEveryFeature pins the linear model's arithmetic on a
// vector with all six features non-zero, so no term can be dropped, zeroed or
// turned into a division without changing the result.
func TestEstimateUsesEveryFeature(t *testing.T) {
	t.Parallel()
	f := utils.TokenFeatures{CJK: 10, Struct: 20, Digit: 30, Indent: 40, Words: 50, Other: 60}
	// Seeds 0.72/1.38/1.34/-0.04/0.48/0.11 → 7.2+27.6+40.2-1.6+24+6.6 = 104.
	if got := estimateWithFeatures(f, utils.SeedTokenRatios); got != 104 {
		t.Errorf("estimateWithFeatures = %d, want exactly 104", got)
	}
}

// TestEstimateFloorsMinusOne needs int(v) to land on -1: flooring at 0 is
// only observable when the un-floored value is negative.
func TestEstimateFloorsMinusOne(t *testing.T) {
	t.Parallel()
	R := [6]float64{0, 0, 0, -1, 0, 0}
	want := 0 // int(-1) floored at zero, never negative
	if got := estimateWithFeatures(utils.TokenFeatures{Indent: 1}, R); got != want {
		t.Errorf("estimateWithFeatures(indent 1, ratio -1) = %d, want %d (floored, never negative)", got, want)
	}
}

// lockProbeProvider records whether e.mu is still held at the moment
// calibrationKey reads the provider name.
type lockProbeProvider struct {
	mu     *sync.RWMutex
	held   bool
	probed bool
}

func (p *lockProbeProvider) Name() string {
	if p.mu.TryLock() {
		p.mu.Unlock()
		p.held = false
	} else {
		p.held = true
	}
	p.probed = true
	return "probe"
}

func (p *lockProbeProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (p *lockProbeProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, nil
}

// TestModelKeyHoldsLockWhileReading pins the defer on modelKey's RUnlock:
// the unlock must happen after the provider and model fields are read.
func TestModelKeyHoldsLockWhileReading(t *testing.T) {
	t.Parallel()
	e := &Engine{model: "m"}
	p := &lockProbeProvider{mu: &e.mu}
	e.provider = p

	if got := e.modelKey(); got != "probe/m" {
		t.Errorf("modelKey() = %q, want %q", got, "probe/m")
	}
	if !p.probed {
		t.Fatal("provider.Name was never called — modelKey no longer derives the key from the provider")
	}
	if !p.held {
		t.Error("e.mu was already released while the key was being computed — RUnlock is no longer deferred")
	}
}

// TestRatiosForRegistersCalibration pins the lazy registration: a ratios
// lookup must leave a calibration in the map for later observations.
func TestRatiosForRegistersCalibration(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	if got := e.ratiosFor("p/m"); got != utils.SeedTokenRatios {
		t.Errorf("ratiosFor = %v, want seeds", got)
	}
	e.calibMu.Lock()
	cal := e.calibrations["p/m"]
	e.calibMu.Unlock()
	if cal == nil {
		t.Fatal("ratiosFor did not register a calibration for the key — its observations would be dropped")
	}
	if cal.fitted || len(cal.obs) != 0 {
		t.Errorf("freshly registered calibration = %+v, want unfitted with no observations", cal)
	}
}

// TestOneTokenUsageAnchors pins the usage guard at total <= 0: a provider
// reporting a single input token is real usage and must anchor.
func TestOneTokenUsageAnchors(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("q")}
	e.mu.Unlock()
	one := calibAssistant("a", 1)
	e.mu.Lock()
	e.messages = append(e.messages, *one)
	e.mu.Unlock()
	e.recordCalibrationObservation(one)

	e.calibMu.Lock()
	cal := e.calibrations[e.modelKey()]
	e.calibMu.Unlock()
	if cal == nil {
		t.Fatal("usage of 1 input token was discarded; only total <= 0 may skip anchoring")
	}
	if cal.anchorTotal != 1 || cal.anchorMsgIdx != 1 || !cal.anchored {
		t.Errorf("anchor = (total %d, idx %d, anchored %v), want (1, 1, true)",
			cal.anchorTotal, cal.anchorMsgIdx, cal.anchored)
	}
}

// TestZeroDeltaSkipped pins the observation gate at real > 0: a turn whose
// usage did not grow carries no differential signal.
func TestZeroDeltaSkipped(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("first question"), *calibAssistant("first answer", 1000)}
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[1])

	e.mu.Lock()
	e.messages = append(e.messages, calibTextMessage("more context"), *calibAssistant("second answer", 1000))
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[3])

	e.calibMu.Lock()
	cal := e.calibrations[e.modelKey()]
	e.calibMu.Unlock()
	if len(cal.obs) != 0 {
		t.Errorf("recorded %d observations for a zero token delta, want 0", len(cal.obs))
	}
	if cal.anchorTotal != 1000 || cal.anchorMsgIdx != 3 {
		t.Errorf("anchor = (total %d, idx %d), want (1000, 3) — the anchor still advances",
			cal.anchorTotal, cal.anchorMsgIdx)
	}
}

// TestOneTokenDeltaRecorded pins the other side of the same gate: a delta
// of one token is a positive delta and must be recorded.
func TestOneTokenDeltaRecorded(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("first question"), *calibAssistant("first answer", 1000)}
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[1])

	e.mu.Lock()
	e.messages = append(e.messages, calibTextMessage("more context"), *calibAssistant("second answer", 1001))
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[3])

	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	cal := e.calibrations[e.modelKey()]
	if len(cal.obs) != 1 {
		t.Fatalf("recorded %d observations for a 1-token delta, want 1", len(cal.obs))
	}
	if cal.obs[0].real != 1 {
		t.Errorf("observation real = %d, want 1", cal.obs[0].real)
	}
}

// TestSingleMessageWindowObservable anchors on a turn and appends only the
// next reply: the window is exactly the one message between them, so the gate
// must accept n-1 > windowStart with a one-message gap.
func TestSingleMessageWindowObservable(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	first := calibAssistant("first answer", 1000)
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("first question"), *first}
	e.mu.Unlock()
	e.recordCalibrationObservation(first)

	second := calibAssistant("second answer", 1300)
	e.mu.Lock()
	e.messages = append(e.messages, *second)
	e.mu.Unlock()
	e.recordCalibrationObservation(second)

	e.calibMu.Lock()
	defer e.calibMu.Unlock()
	cal := e.calibrations[e.modelKey()]
	if len(cal.obs) != 1 {
		t.Fatalf("recorded %d observations for a one-message window, want 1", len(cal.obs))
	}
	if cal.obs[0].real != 300 {
		t.Errorf("observation real = %d, want 300", cal.obs[0].real)
	}
	if cal.obs[0].f != messageTokenFeatures(e.messages[1]) {
		t.Errorf("observation features = %+v, want the anchored reply's features %+v",
			cal.obs[0].f, messageTokenFeatures(e.messages[1]))
	}
}

// TestZeroFeatureWindowSkipped keeps an observable window that carries no
// text (image-only) out of the fit: a zero feature vector is not a data point.
func TestZeroFeatureWindowSkipped(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	e.mu.Lock()
	e.messages = []types.Message{calibTextMessage("first question"), *calibAssistant("", 1000)}
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[1])

	img := types.NewUserMessage([]types.ContentBlock{{Type: types.ContentTypeImage}})
	e.mu.Lock()
	e.messages = append(e.messages, img, *calibAssistant("", 1400))
	e.mu.Unlock()
	e.recordCalibrationObservation(&e.messages[3])

	e.calibMu.Lock()
	cal := e.calibrations[e.modelKey()]
	e.calibMu.Unlock()
	if len(cal.obs) != 0 {
		t.Errorf("recorded %d observations from a textless window, want 0", len(cal.obs))
	}
	if cal.anchorTotal != 1400 || cal.anchorMsgIdx != 3 {
		t.Errorf("anchor = (total %d, idx %d), want (1400, 3) — the anchor still advances",
			cal.anchorTotal, cal.anchorMsgIdx)
	}
}

// TestAddTokenFeaturesSumsAllFields pins every field of the feature sum.
func TestAddTokenFeaturesSumsAllFields(t *testing.T) {
	t.Parallel()
	a := utils.TokenFeatures{CJK: 1, Struct: 2, Digit: 3, Indent: 4, Words: 5, Other: 6}
	b := utils.TokenFeatures{CJK: 10, Struct: 20, Digit: 30, Indent: 40, Words: 50, Other: 60}
	want := utils.TokenFeatures{CJK: 11, Struct: 22, Digit: 33, Indent: 44, Words: 55, Other: 66}
	if got := addTokenFeatures(a, b); got != want {
		t.Errorf("addTokenFeatures = %+v, want %+v", got, want)
	}
	if got := addTokenFeatures(a, utils.TokenFeatures{}); got != a {
		t.Errorf("addTokenFeatures(a, zero) = %+v, want %+v (identity)", got, a)
	}
}

// TestMessageTokenFeaturesToolResult pins the tool_result branch of the
// message feature scan.
func TestMessageTokenFeaturesToolResult(t *testing.T) {
	t.Parallel()
	content := json.RawMessage(`"ls output 123"`)
	msg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, Content: content}},
	}
	want := toolResultTokenFeatures(content)
	if want == (utils.TokenFeatures{}) {
		t.Fatal("fixture content produced no features")
	}
	if got := messageTokenFeatures(msg); got != want {
		t.Errorf("messageTokenFeatures(tool_result) = %+v, want %+v", got, want)
	}
}

// TestToolResultSingleByteContent pins the empty-content guard with a
// length that is neither 0 nor large: a one-byte payload still carries signal.
func TestToolResultSingleByteContent(t *testing.T) {
	t.Parallel()
	got := toolResultTokenFeatures(json.RawMessage(`0`))
	if got != utils.CountTokenFeatures("0") {
		t.Errorf("toolResultTokenFeatures(one byte) = %+v, want %+v", got, utils.CountTokenFeatures("0"))
	}
}
