package llm

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// Each state operation must release the mutex: the next operation on the same
// state completes instead of deadlocking. The bounded wait turns a missing
// Unlock into a fast failure rather than a hung suite.
func TestReasoningFallbackStateReleasesMutex(t *testing.T) {
	t.Parallel()

	s := newReasoningFallbackState()
	ops := []struct {
		name string
		fn   func()
	}{
		{"remember", func() { s.remember("k", &reasoningEffortFallback{value: "low"}) }},
		{"get", func() { _ = s.get("k") }},
		{"clear", func() { s.clear() }},
		{"remember-again", func() { s.remember("k2", nil) }},
	}
	for _, op := range ops {
		done := make(chan struct{})
		go func() {
			op.fn()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not complete — mutex not released", op.name)
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// A nested reasoning object holding a foreign key (even the empty-string key)
// must keep the parent "reasoning" field when the effort is dropped.
func TestApplyFallback_NestedReasoningEmptyKeyKeepsParent(t *testing.T) {
	t.Parallel()

	params := map[string]any{
		"reasoning": map[string]any{"effort": "high", "": true},
	}
	if !applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{}) {
		t.Fatal("applyOpenAIReasoningEffortFallback = false, want true")
	}
	if params["reasoning"] == nil {
		t.Error("parent \"reasoning\" deleted while a sibling key remained")
	}
}

// Dropping the effort when only the nested spelling exists must still report a
// change (and rewrite the body), not silently no-op.
func TestApplyFallback_NestedOnlyDropRewritesBody(t *testing.T) {
	t.Parallel()

	body := mustMarshal(t, map[string]any{"reasoning": map[string]any{"effort": "high"}})
	got := applyReasoningFallbackToBody(body, &reasoningEffortFallback{})
	if bytes.Equal(got, body) {
		t.Errorf("body unchanged: %s, want the nested effort dropped", got)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("rewritten body does not parse: %v", err)
	}
	if _, ok := m["reasoning"]; ok {
		t.Errorf("empty reasoning object not pruned: %s", got)
	}
}

// An unparseable body is passed through byte-for-byte: the fallback layer
// never invents a rewrite it cannot round-trip.
func TestApplyFallback_UnparseableBodyUntouched(t *testing.T) {
	t.Parallel()

	body := []byte(`{"reasoning_effort":"low","x":`)
	got := applyReasoningFallbackToBody(body, &reasoningEffortFallback{value: "minimal"})
	if !bytes.Equal(got, body) {
		t.Errorf("body rewritten from invalid JSON: %s", got)
	}
}

// Each structured field alone (param, code, type) marks the rejection as
// reasoning-effort related.
func TestMentionsReasoningEffort_StructuredArms(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"param", "code", "type"} {
		c := newCapturedHTTPError(400, []byte(`{"error":{"`+field+`":"reasoning_effort"}}`))
		if !mentionsReasoningEffort(nil, c) {
			t.Errorf("mentionsReasoningEffort = false with %s mention", field)
		}
	}
	// Body text alone (unparseable payload) still counts.
	c := newCapturedHTTPError(400, []byte(`plain reasoning_effort rejection`))
	if !mentionsReasoningEffort(nil, c) {
		t.Error("mentionsReasoningEffort = false with body-text mention")
	}
}

// A rejection that never mentions the effort field (broad pattern: field
// name, "reasoning value", or levels/values vocabulary) is not negotiable
// even when it quotes the current value next to a verdict word. "unsupported"
// alone avoids every broad-pattern phrase, and "none" is the only tier whose
// quoted-match verdict can fire without a field mention.
func TestIsInvalidReasoningEffort_NoMentionNotNegotiable(t *testing.T) {
	t.Parallel()

	for _, current := range []string{"low", "none"} {
		c := newCapturedHTTPError(400, []byte(`unsupported option: "`+current+`"`))
		if isInvalidReasoningEffortError(&APIError{Status: 400}, c, current) {
			t.Errorf("current %q: isInvalidReasoningEffortError = true without any effort-field mention", current)
		}
	}
}

// A GLM-style reasoning_content mention does not exclude the rejection when
// the effort field is named alongside — the fielded verdict still applies.
// (The pure content-only case cannot pass the mentionsReasoningEffort gate:
// every structured field folds into the message parts, so any gate-passing
// rejection also names the field in the message.)
func TestIsInvalidReasoningEffort_GLMContentNotExcludedWhenFieldNamed(t *testing.T) {
	t.Parallel()

	c := newCapturedHTTPError(400, []byte(`{"error":{"message":"reasoning_content and reasoning_effort are invalid"}}`))
	if !isInvalidReasoningEffortError(&APIError{Status: 400}, c, "low") {
		t.Error("reasoning_content alongside a named effort field must not exclude the rejection")
	}
}

// A foreign param defeats the heuristic (the rejection is about another
// field), while levels vocabulary without any param keeps the quoted
// rejection negotiable.
func TestIsInvalidReasoningEffort_ForeignParamVsMessageMention(t *testing.T) {
	t.Parallel()

	// Foreign param: about another field — not negotiable even though the
	// current value is quoted with a verdict.
	foreign := newCapturedHTTPError(400, []byte(`{"error":{"param":"temperature","message":"invalid value for temperature, valid levels: \"low\", \"high\""}}`))
	if isInvalidReasoningEffortError(&APIError{Status: 400}, foreign, "low") {
		t.Error("foreign param must defeat the heuristic")
	}

	// No param, levels vocabulary only: quoted rejection stays negotiable.
	levels := newCapturedHTTPError(400, []byte(`{"error":{"message":"invalid value, valid levels: \"low\", \"high\""}}`))
	if !isInvalidReasoningEffortError(&APIError{Status: 400}, levels, "low") {
		t.Error("levels-vocabulary rejection without a param must stay negotiable")
	}
}

// Bare values inside an allowed-cue segment are collected even when nothing is
// quoted — the fallback must remap onto them.
func TestResolveFallback_BareCueValues(t *testing.T) {
	t.Parallel()

	body := mustMarshal(t, map[string]any{"reasoning_effort": "low"})
	c := newCapturedHTTPError(400, []byte(`{"error":{"message":"Invalid value for reasoning_effort, must be one of: low, minimal"}}`))
	fb := resolveOpenAIReasoningEffortFallback(&APIError{Status: 400}, c, chatBodyParams(body), false)
	if fb == nil {
		t.Fatal("resolveOpenAIReasoningEffortFallback = nil, want a remap conclusion")
	}
	if fb.value != "minimal" {
		t.Errorf("fallback value = %q, want minimal (nearest allowed tier to low)", fb.value)
	}
}

// Explicit-disable remap picks the lowest allowed tier when none is not in
// the vocabulary — it must not invent a none value the server never offered.
func TestResolveFallback_ExplicitDisableNoNoneInVocabulary(t *testing.T) {
	t.Parallel()

	body := mustMarshal(t, map[string]any{"reasoning_effort": "low"})
	c := newCapturedHTTPError(400, []byte(`{"error":{"message":"Invalid reasoning_effort \"low\", valid levels are: minimal, low"}}`))
	fb := resolveOpenAIReasoningEffortFallback(&APIError{Status: 400}, c, chatBodyParams(body), true)
	if fb == nil {
		t.Fatal("resolveOpenAIReasoningEffortFallback = nil, want the lowest-tier conclusion")
	}
	if fb.value != "minimal" {
		t.Errorf("fallback value = %q, want minimal (lowest allowed tier)", fb.value)
	}
}

// When the current tier is itself allowed, the search skips it and still
// finds the nearest other candidate.
func TestNearestEnabledFallback_CurrentInAllowed(t *testing.T) {
	t.Parallel()

	got := nearestEnabledReasoningFallback("high", map[string]bool{"high": true, "max": true})
	if got != "max" {
		t.Errorf("nearestEnabledReasoningFallback(high, {high,max}) = %q, want max", got)
	}
}

// resolveReasoningFallback requires a captured body: a bare APIError without
// one is never negotiable, no matter what its message says.
func TestResolveReasoningFallback_RequiresCapturedBody(t *testing.T) {
	t.Parallel()

	p := &OpenAIProvider{reasoningFallbacks: newReasoningFallbackState()}
	body := mustMarshal(t, map[string]any{"reasoning_effort": "low"})
	fb := p.resolveReasoningFallback(
		&APIError{Status: 400, Message: `Invalid reasoning_effort "low", valid levels are: minimal, low`},
		nil, body, false)
	if fb != nil {
		t.Errorf("resolveReasoningFallback without captured body = %+v, want nil", fb)
	}
}

// Concurrent hammering of the fallback state: the -race build detects map
// accesses that slip outside the mutex window (e.g. an Unlock moved ahead of
// the guarded write), which single-threaded semantics cannot expose.
func TestReasoningFallbackStateConcurrentHammers(t *testing.T) {
	t.Parallel()

	s := newReasoningFallbackState()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				s.remember("k", &reasoningEffortFallback{value: "low"})
				_ = s.get("k")
				s.clear()
			}
		})
	}
	wg.Wait()
}
